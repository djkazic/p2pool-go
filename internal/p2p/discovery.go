package p2p

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	leveldb "github.com/ipfs/go-ds-leveldb"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"

	"go.uber.org/zap"
)

const (
	// MDNSServiceTag is the mDNS service tag for LAN discovery.
	MDNSServiceTag = "p2pool-go.local"

	// DHTNamespace is the Kademlia DHT namespace for peer discovery.
	DHTNamespace = "p2pool-go"
)

// Discovery manages peer discovery via mDNS and Kademlia DHT.
type Discovery struct {
	host   host.Host
	logger *zap.Logger
	dht    *dht.IpfsDHT
	dhtDS  io.Closer // persistent DHT datastore (nil if in-memory)
}

// NewDiscovery creates a new discovery service.
func NewDiscovery(ctx context.Context, h host.Host, enableMDNS bool, bootnodes []string, savedPeers []peer.AddrInfo, dataDir string, dhtServer bool, logger *zap.Logger) (*Discovery, error) {
	d := &Discovery{
		host:   h,
		logger: logger,
	}

	// Setup mDNS for LAN discovery
	if enableMDNS {
		mdnsService := mdns.NewMdnsService(h, MDNSServiceTag, d)
		if err := mdnsService.Start(); err != nil {
			logger.Warn("mDNS setup failed", zap.Error(err))
		} else {
			logger.Info("mDNS discovery enabled")
		}
	}

	// Reconnect to previously known peers before DHT bootstrap
	for _, pi := range savedPeers {
		if pi.ID == h.ID() {
			continue
		}
		if err := h.Connect(ctx, pi); err != nil {
			logger.Debug("failed to connect to saved peer", zap.String("peer", pi.ID.String()), zap.Error(err))
		} else {
			logger.Info("connected to saved peer", zap.String("peer", pi.ID.String()))
		}
	}

	// Open persistent LevelDB datastore for DHT routing table
	dhtMode := dht.ModeAutoServer
	if dhtServer {
		dhtMode = dht.ModeServer
		logger.Info("DHT forced to server mode")
	}
	dhtOpts := []dht.Option{dht.Mode(dhtMode)}
	ds, err := leveldb.NewDatastore(filepath.Join(dataDir, "dht"), nil)
	if err != nil {
		logger.Warn("failed to open DHT datastore, falling back to in-memory", zap.Error(err))
	} else {
		d.dhtDS = ds
		dhtOpts = append(dhtOpts, dht.Datastore(ds))
		logger.Info("DHT using persistent datastore")
	}

	// Setup Kademlia DHT
	kadDHT, err := dht.New(ctx, h, dhtOpts...)
	if err != nil {
		if d.dhtDS != nil {
			d.dhtDS.Close()
		}
		return nil, fmt.Errorf("create DHT: %w", err)
	}
	d.dht = kadDHT

	// Connect to bootnodes BEFORE bootstrapping so the initial routing
	// table refresh has peers to query. Without this, Bootstrap()'s
	// refresh finds an empty table and the DHT stays inert until the
	// next periodic refresh (~10 minutes later).
	for _, bn := range bootnodes {
		addr, err := peer.AddrInfoFromString(bn)
		if err != nil {
			logger.Warn("invalid bootnode address", zap.String("addr", bn), zap.Error(err))
			continue
		}
		if err := h.Connect(ctx, *addr); err != nil {
			logger.Warn("failed to connect to bootnode", zap.String("addr", bn), zap.Error(err))
		} else {
			logger.Info("connected to bootnode", zap.String("peer", addr.ID.String()))
		}
	}

	if err := kadDHT.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("bootstrap DHT: %w", err)
	}

	// Bootstrap() fires a non-blocking RefreshNoWait(). Wait for the
	// routing table to actually populate before starting the
	// advertise/discover loops — otherwise they spin on an empty table.
	refreshCtx, refreshCancel := context.WithTimeout(ctx, 15*time.Second)
	defer refreshCancel()
	select {
	case err := <-kadDHT.RefreshRoutingTable():
		if err != nil {
			logger.Warn("DHT routing table refresh error", zap.Error(err))
		}
		logger.Info("DHT routing table ready", zap.Int("peers", kadDHT.RoutingTable().Size()))
	case <-refreshCtx.Done():
		logger.Warn("DHT routing table refresh timed out", zap.Int("peers", kadDHT.RoutingTable().Size()))
	}

	// Start routing discovery
	routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
	go d.advertiseLoop(ctx, routingDiscovery)
	go d.discoverLoop(ctx, routingDiscovery)

	return d, nil
}

// Close shuts down the DHT and its persistent datastore.
func (d *Discovery) Close() error {
	if err := d.dht.Close(); err != nil {
		d.logger.Warn("DHT close error", zap.Error(err))
	}
	if d.dhtDS != nil {
		return d.dhtDS.Close()
	}
	return nil
}

// HandlePeerFound is called by mDNS when a new peer is found.
func (d *Discovery) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == d.host.ID() {
		return
	}

	d.logger.Info("mDNS peer found", zap.String("peer", pi.ID.String()))
	if err := d.host.Connect(context.Background(), pi); err != nil {
		d.logger.Debug("failed to connect to mDNS peer", zap.Error(err))
	}
}

// waitForRoutingPeers blocks until the DHT routing table has at least one
// peer or the context is cancelled. This avoids hammering Advertise/FindPeers
// against an empty table (e.g. on a bootnode that has no bootnodes of its own).
func (d *Discovery) waitForRoutingPeers(ctx context.Context) bool {
	const pollInterval = 5 * time.Second
	for d.dht.RoutingTable().Size() == 0 {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollInterval):
		}
	}
	return true
}

func (d *Discovery) advertiseLoop(ctx context.Context, rd *drouting.RoutingDiscovery) {
	const defaultTTL = 10 * time.Minute

	for {
		// Wait for at least one routing table peer before attempting to advertise.
		if !d.waitForRoutingPeers(ctx) {
			return
		}

		ttl, err := rd.Advertise(ctx, DHTNamespace)
		if err != nil {
			d.logger.Debug("DHT advertise error", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Re-advertise when the TTL expires. Advertise returns immediately,
		// so we must sleep to avoid a hot loop.
		reAdvertise := defaultTTL
		if ttl > 0 {
			reAdvertise = ttl
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reAdvertise):
		}
	}
}

func (d *Discovery) discoverLoop(ctx context.Context, rd *drouting.RoutingDiscovery) {
	backoff := 30 * time.Second
	const maxBackoff = 5 * time.Minute

	for {
		// Wait for at least one routing table peer before attempting to discover.
		if !d.waitForRoutingPeers(ctx) {
			return
		}

		peerCh, err := rd.FindPeers(ctx, DHTNamespace)
		if err != nil {
			d.logger.Debug("DHT find peers error", zap.Error(err), zap.Duration("retry_in", backoff))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Reset backoff on successful FindPeers call.
		backoff = 30 * time.Second

		// Drain the peer channel until it closes.
		for pi := range peerCh {
			if pi.ID == d.host.ID() || pi.ID == "" {
				continue
			}
			if len(pi.Addrs) == 0 {
				continue
			}
			if d.host.Network().Connectedness(pi.ID) == network.Connected {
				continue
			}
			if err := d.host.Connect(ctx, pi); err != nil {
				d.logger.Debug("failed to connect to DHT peer", zap.String("peer", pi.ID.String()), zap.Error(err))
			} else {
				d.logger.Info("connected to DHT peer", zap.String("peer", pi.ID.String()))
			}
		}

		// Channel closed — retry after backoff.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}
