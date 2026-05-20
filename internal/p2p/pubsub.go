package p2p

import (
	"context"
	"fmt"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// ShareValidator is a cheap pre-propagation check for an incoming share.
// Return false to reject the message: gossipsub will not forward it and
// will downscore the peer that sent it. The check must avoid any work
// that requires chain state or locks — full validation runs later in the
// orchestrator. Format / PoW-vs-declared-target are appropriate; parent
// lookups are not.
type ShareValidator func(*ShareMsg) bool

// IncomingShare bundles a decoded share with the peer that delivered it,
// so the orchestrator can attribute later validation failures back to the
// sender and apply per-peer misbehavior policy.
type IncomingShare struct {
	Share *ShareMsg
	From  peer.ID
}

// PubSub manages GossipSub for share propagation.
type PubSub struct {
	ps     *pubsub.PubSub
	topic  *pubsub.Topic
	sub    *pubsub.Subscription
	self   peer.ID
	logger *zap.Logger

	peerLimiters   map[peer.ID]*rate.Limiter
	peerLimitersMu sync.Mutex
}

// NewPubSub creates a new GossipSub instance. If validate is non-nil, it is
// registered as a topic validator so invalid shares are rejected before
// gossipsub propagates them and the sending peer is downscored.
func NewPubSub(ctx context.Context, h host.Host, incomingShares chan *IncomingShare, validate ShareValidator, logger *zap.Logger) (*PubSub, error) {
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, err
	}

	topic, err := ps.Join(ShareTopicName)
	if err != nil {
		return nil, err
	}

	// Register the topic validator BEFORE subscribing so the very first
	// message we receive on this topic is gated by the validator.
	if validate != nil {
		v := func(ctx context.Context, sender peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
			share, decErr := DecodeShareMsg(msg.Data)
			if decErr != nil {
				logger.Debug("rejecting malformed share from peer",
					zap.String("peer", sender.String()), zap.Error(decErr))
				return pubsub.ValidationReject
			}
			if !validate(share) {
				logger.Debug("rejecting share that failed cheap validation",
					zap.String("peer", sender.String()))
				return pubsub.ValidationReject
			}
			// Stash the decoded share so the read loop does not decode again.
			msg.ValidatorData = share
			return pubsub.ValidationAccept
		}
		if err := ps.RegisterTopicValidator(ShareTopicName, v); err != nil {
			return nil, fmt.Errorf("register share validator: %w", err)
		}
	}

	sub, err := topic.Subscribe()
	if err != nil {
		return nil, err
	}

	p := &PubSub{
		ps:           ps,
		topic:        topic,
		sub:          sub,
		self:         h.ID(),
		logger:       logger,
		peerLimiters: make(map[peer.ID]*rate.Limiter),
	}

	go p.readLoop(ctx, incomingShares)

	return p, nil
}

// PublishShare publishes a share to the gossipsub network.
func (p *PubSub) PublishShare(share *ShareMsg) error {
	share.Type = MsgTypeShare
	data, err := Encode(share)
	if err != nil {
		return err
	}
	return p.topic.Publish(context.Background(), data)
}

func (p *PubSub) readLoop(ctx context.Context, incomingShares chan *IncomingShare) {
	for {
		msg, err := p.sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.logger.Error("pubsub read error", zap.Error(err))
			continue
		}

		from := msg.GetFrom()

		// Ignore our own messages
		if from == p.self {
			continue
		}

		if !p.getPeerLimiter(from).Allow() {
			p.logger.Warn("peer rate limited", zap.String("peer", from.String()))
			continue
		}

		// Reuse the share decoded by the topic validator, when present.
		// Falls through to a fresh decode if no validator was registered.
		share, ok := msg.ValidatorData.(*ShareMsg)
		if !ok {
			var err error
			share, err = DecodeShareMsg(msg.Data)
			if err != nil {
				p.logger.Debug("invalid share message", zap.Error(err))
				continue
			}
		}

		select {
		case incomingShares <- &IncomingShare{Share: share, From: from}:
		default:
			p.logger.Warn("incoming shares channel full, dropping share")
		}
	}
}

func (p *PubSub) getPeerLimiter(peerID peer.ID) *rate.Limiter {
	p.peerLimitersMu.Lock()
	defer p.peerLimitersMu.Unlock()

	if lim, ok := p.peerLimiters[peerID]; ok {
		return lim
	}

	// Evict a random entry if map is too large
	if len(p.peerLimiters) >= 500 {
		for id := range p.peerLimiters {
			delete(p.peerLimiters, id)
			break
		}
	}

	lim := rate.NewLimiter(10, 20)
	p.peerLimiters[peerID] = lim
	return lim
}
