package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"

	ma "github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
)

const peersFile = "peers.json"

// Node manages the libp2p host and P2P networking.
type Node struct {
	Host   host.Host
	Logger *zap.Logger

	dataDir string

	pubsub    *PubSub
	discovery *Discovery
	syncer    *Syncer

	incomingShares chan *ShareMsg
	peerConnected  chan peer.ID
}

// NewNode creates a new libp2p node with GossipSub but does NOT start
// discovery. Call StartDiscovery after registering all stream handlers
// (e.g. InitSyncer) to avoid races where peers connect before handlers
// are ready.
func NewNode(ctx context.Context, listenPort int, dataDir string, logger *zap.Logger) (*Node, error) {
	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort)

	// Load or create persistent identity (stable peer ID across restarts)
	privKey, err := LoadOrCreateIdentity(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}

	cm, err := connmgr.NewConnManager(50, 100, connmgr.WithGracePeriod(time.Minute))
	if err != nil {
		return nil, fmt.Errorf("create connection manager: %w", err)
	}

	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),
		libp2p.ConnectionManager(cm),
	)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	node := &Node{
		Host:           h,
		Logger:         logger,
		dataDir:        dataDir,
		incomingShares: make(chan *ShareMsg, 256),
		peerConnected:  make(chan peer.ID, 16),
	}

	// Register connection notifier to trigger sync on new peers
	h.Network().Notify(&peerNotifiee{peerConnected: node.peerConnected})

	// Setup GossipSub
	node.pubsub, err = NewPubSub(ctx, h, node.incomingShares, logger)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("setup pubsub: %w", err)
	}

	logger.Info("p2p node started",
		zap.String("peer_id", h.ID().String()),
		zap.Int("port", listenPort),
	)

	for _, addr := range h.Addrs() {
		logger.Info("listening on", zap.String("addr", fmt.Sprintf("%s/p2p/%s", addr, h.ID())))
	}

	return node, nil
}

// StartDiscovery begins mDNS and DHT peer discovery. Must be called after
// all stream handlers are registered (InitSyncer, etc.).
func (n *Node) StartDiscovery(ctx context.Context, enableMDNS bool, bootnodes []string) error {
	savedPeers, err := LoadPeers(n.dataDir)
	if err != nil {
		n.Logger.Warn("failed to load saved peers", zap.Error(err))
	} else if len(savedPeers) > 0 {
		n.Logger.Info("loaded saved peers", zap.Int("count", len(savedPeers)))
	}

	n.discovery, err = NewDiscovery(ctx, n.Host, enableMDNS, bootnodes, savedPeers, n.dataDir, n.Logger)
	if err != nil {
		return fmt.Errorf("setup discovery: %w", err)
	}
	return nil
}

// IncomingShares returns the channel of shares received from peers.
func (n *Node) IncomingShares() <-chan *ShareMsg {
	return n.incomingShares
}

// BroadcastShare publishes a share to the network.
func (n *Node) BroadcastShare(share *ShareMsg) error {
	return n.pubsub.PublishShare(share)
}

// PeerCount returns the number of connected peers.
func (n *Node) PeerCount() int {
	return len(n.Host.Network().Peers())
}

// ConnectedPeers returns info about connected peers.
func (n *Node) ConnectedPeers() []peer.ID {
	return n.Host.Network().Peers()
}

// InitSyncer creates the Syncer and registers stream handlers for
// inv-based sync (hash discovery) and data protocol (targeted download).
func (n *Node) InitSyncer(invHandler InvHandler, dataHandler DataHandler) {
	n.syncer = NewSyncer(n.Host, invHandler, dataHandler, n.Logger)
}

// PeerConnected returns a channel that receives peer IDs when new peers connect.
func (n *Node) PeerConnected() <-chan peer.ID {
	return n.peerConnected
}

// Syncer returns the sync protocol handler.
func (n *Node) Syncer() *Syncer {
	return n.syncer
}

// Close shuts down the node, saving connected peers before stopping.
func (n *Node) Close() error {
	if err := n.SavePeers(); err != nil {
		n.Logger.Warn("failed to save peers on shutdown", zap.Error(err))
	}
	if n.discovery != nil {
		n.discovery.Close()
	}
	return n.Host.Close()
}

// SavePeers writes the multiaddrs of currently connected peers to peers.json.
func (n *Node) SavePeers() error {
	peers := n.Host.Network().Peers()
	var infos []peer.AddrInfo
	for _, pid := range peers {
		addrs := n.Host.Peerstore().Addrs(pid)
		if len(addrs) > 0 {
			infos = append(infos, peer.AddrInfo{ID: pid, Addrs: addrs})
		}
	}

	if len(infos) == 0 {
		return nil
	}

	// Marshal via an intermediate type so the JSON is human-readable.
	type jsonPeer struct {
		ID    string   `json:"id"`
		Addrs []string `json:"addrs"`
	}
	var jp []jsonPeer
	for _, info := range infos {
		var addrs []string
		for _, a := range info.Addrs {
			addrs = append(addrs, a.String())
		}
		jp = append(jp, jsonPeer{ID: info.ID.String(), Addrs: addrs})
	}

	data, err := json.MarshalIndent(jp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal peers: %w", err)
	}

	path := filepath.Join(n.dataDir, peersFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	n.Logger.Info("saved peers", zap.Int("count", len(infos)), zap.String("path", path))
	return nil
}

// LoadPeers reads previously saved peer addresses from peers.json.
func LoadPeers(dataDir string) ([]peer.AddrInfo, error) {
	path := filepath.Join(dataDir, peersFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	type jsonPeer struct {
		ID    string   `json:"id"`
		Addrs []string `json:"addrs"`
	}
	var jp []jsonPeer
	if err := json.Unmarshal(data, &jp); err != nil {
		return nil, fmt.Errorf("unmarshal peers: %w", err)
	}

	var infos []peer.AddrInfo
	for _, p := range jp {
		pid, err := peer.Decode(p.ID)
		if err != nil {
			continue
		}
		var addrs []ma.Multiaddr
		for _, a := range p.Addrs {
			maddr, err := ma.NewMultiaddr(a)
			if err != nil {
				continue
			}
			addrs = append(addrs, maddr)
		}
		if len(addrs) > 0 {
			infos = append(infos, peer.AddrInfo{ID: pid, Addrs: addrs})
		}
	}

	return infos, nil
}

// peerNotifiee implements network.Notifiee to detect new peer connections.
type peerNotifiee struct {
	peerConnected chan peer.ID
}

func (pn *peerNotifiee) Connected(_ network.Network, conn network.Conn) {
	// Non-blocking send; drop if channel is full (sync will happen on next connect)
	select {
	case pn.peerConnected <- conn.RemotePeer():
	default:
	}
}

func (pn *peerNotifiee) Disconnected(network.Network, network.Conn) {}
func (pn *peerNotifiee) Listen(network.Network, ma.Multiaddr)      {}
func (pn *peerNotifiee) ListenClose(network.Network, ma.Multiaddr) {}
