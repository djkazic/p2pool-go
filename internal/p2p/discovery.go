package p2p

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"

	dht "github.com/libp2p/go-libp2p-kad-dht"
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
}

// NewDiscovery creates a new discovery service.
func NewDiscovery(ctx context.Context, h host.Host, enableMDNS bool, bootnodes []string, logger *zap.Logger) (*Discovery, error) {
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

	// Setup Kademlia DHT
	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		return nil, fmt.Errorf("create DHT: %w", err)
	}
	d.dht = kadDHT

	if err := kadDHT.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("bootstrap DHT: %w", err)
	}

	// Connect to bootnodes
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

	// Start routing discovery
	routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
	go d.advertiseLoop(ctx, routingDiscovery)
	go d.discoverLoop(ctx, routingDiscovery)

	return d, nil
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

func (d *Discovery) advertiseLoop(ctx context.Context, rd *drouting.RoutingDiscovery) {
	backoff := 5 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		_, err := rd.Advertise(ctx, DHTNamespace)
		if err != nil {
			d.logger.Debug("DHT advertise error", zap.Error(err), zap.Duration("retry_in", backoff))
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

		// Success — reset backoff and wait for context cancellation.
		// Advertise blocks until TTL expires, so we loop back to re-advertise.
		backoff = 5 * time.Second
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (d *Discovery) discoverLoop(ctx context.Context, rd *drouting.RoutingDiscovery) {
	backoff := 30 * time.Second
	const maxBackoff = 5 * time.Minute

	for {
		peerCh, err := rd.FindPeers(ctx, DHTNamespace)
		if err != nil {
			d.logger.Warn("DHT find peers error", zap.Error(err), zap.Duration("retry_in", backoff))
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
			if err := d.host.Connect(ctx, pi); err != nil {
				d.logger.Debug("failed to connect to DHT peer", zap.String("peer", pi.ID.String()), zap.Error(err))
			} else {
				d.logger.Info("connected to DHT peer", zap.String("peer", pi.ID.String()))
			}
		}

		// Channel closed — retry after backoff.
		d.logger.Debug("DHT peer channel closed, will retry", zap.Duration("retry_in", backoff))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}
