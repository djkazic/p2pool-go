package sharechain

import (
	"context"
	"fmt"
	"math/big"
	"sync"

	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/pkg/util"

	"go.uber.org/zap"
)

// EventType represents sharechain events.
type EventType int

const (
	EventNewTip   EventType = iota // New tip share accepted
	EventNewBlock                  // A share that is also a valid Bitcoin block
	EventReorg                     // Chain reorganization occurred
)

// Event is emitted when the sharechain state changes.
type Event struct {
	Type       EventType
	Share      *types.Share
	OldTipHash [32]byte // populated on EventReorg
	ReorgDepth int      // shares rolled back on old fork (populated on EventReorg)
}

// ShareChain manages the share chain state.
type ShareChain struct {
	mu         sync.RWMutex
	store      ShareStore
	validator  *Validator
	forkChoice *ForkChoice
	diffCalc   *DifficultyCalculator
	logger     *zap.Logger

	windowSize int

	// Event subscribers
	subscribers []chan Event
	subMu       sync.RWMutex
}

// NewShareChain creates a new share chain.
func NewShareChain(store ShareStore, diffCalc *DifficultyCalculator, windowSize int, network string, logger *zap.Logger) *ShareChain {
	sc := &ShareChain{
		store:      store,
		forkChoice: NewForkChoice(store),
		diffCalc:   diffCalc,
		logger:     logger,
		windowSize: windowSize,
	}
	sc.validator = NewValidator(store, sc.getExpectedTargetForParent, network)
	return sc
}

// Subscribe returns a channel that receives sharechain events.
// When the context is cancelled, the subscription is automatically removed.
func (sc *ShareChain) Subscribe(ctx context.Context) chan Event {
	ch := make(chan Event, 16)
	sc.subMu.Lock()
	sc.subscribers = append(sc.subscribers, ch)
	sc.subMu.Unlock()
	go func() {
		<-ctx.Done()
		sc.Unsubscribe(ch)
	}()
	return ch
}

// Unsubscribe removes an event subscriber.
func (sc *ShareChain) Unsubscribe(ch chan Event) {
	sc.subMu.Lock()
	defer sc.subMu.Unlock()
	for i, sub := range sc.subscribers {
		if sub == ch {
			sc.subscribers = append(sc.subscribers[:i], sc.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

func (sc *ShareChain) emit(event Event) {
	sc.subMu.RLock()
	defer sc.subMu.RUnlock()
	for _, ch := range sc.subscribers {
		select {
		case ch <- event:
		default:
			sc.logger.Warn("dropping event, subscriber channel full")
		}
	}
}

// AddShare validates and adds a share to the chain.
func (sc *ShareChain) AddShare(share *types.Share) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	hash := share.Hash()

	// Check if already known
	if sc.store.Has(hash) {
		return nil // Duplicate, silently ignore
	}

	// Validate (validator computes expected target and checks ShareTarget matches)
	if err := sc.validator.ValidateShare(share); err != nil {
		return fmt.Errorf("invalid share: %w", err)
	}

	// Store
	if err := sc.store.Add(share); err != nil {
		return fmt.Errorf("store share: %w", err)
	}

	// Update tip via fork choice
	oldTip, hadTip := sc.store.Tip()
	var oldTipHash [32]byte
	if hadTip {
		oldTipHash = oldTip.Hash()
	}

	newTipHash := sc.forkChoice.SelectTip(oldTipHash, hash, sc.windowSize)
	if err := sc.store.SetTip(newTipHash); err != nil {
		return fmt.Errorf("set tip: %w", err)
	}

	// Emit events
	if newTipHash != oldTipHash {
		if hadTip && share.PrevShareHash != oldTipHash {
			_, reorgDepth, _ := sc.forkChoice.FindCommonAncestor(oldTipHash, newTipHash, sc.windowSize)
			sc.logger.Info("sharechain reorg",
				zap.String("old_tip", oldTip.HashHex()),
				zap.String("new_tip", share.HashHex()),
				zap.Int("reorg_depth", reorgDepth),
			)
			sc.emit(Event{
				Type:       EventReorg,
				Share:      share,
				OldTipHash: oldTipHash,
				ReorgDepth: reorgDepth,
			})
		}
		sc.emit(Event{Type: EventNewTip, Share: share})
	}

	if sc.validator.IsBlock(share) {
		sc.logger.Info("BLOCK FOUND!",
			zap.String("hash", share.HashHex()),
			zap.String("miner", share.MinerAddress),
		)
		sc.emit(Event{Type: EventNewBlock, Share: share})
	}

	sc.logger.Info("share added",
		zap.String("hash", share.HashHex()),
		zap.String("miner", share.MinerAddress),
		zap.Int("chain_length", sc.store.Count()),
	)

	return nil
}

// AddShareQuiet validates and adds a share without emitting events.
// Use this for bulk operations like initial sync, then trigger a single
// event/work regeneration afterward.
func (sc *ShareChain) AddShareQuiet(share *types.Share) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	hash := share.Hash()

	if sc.store.Has(hash) {
		return nil
	}

	if err := sc.validator.ValidateShare(share); err != nil {
		return fmt.Errorf("invalid share: %w", err)
	}

	if err := sc.store.Add(share); err != nil {
		return fmt.Errorf("store share: %w", err)
	}

	oldTip, hadTip := sc.store.Tip()
	var oldTipHash [32]byte
	if hadTip {
		oldTipHash = oldTip.Hash()
	}

	newTipHash := sc.forkChoice.SelectTip(oldTipHash, hash, sc.windowSize)
	if err := sc.store.SetTip(newTipHash); err != nil {
		return fmt.Errorf("set tip: %w", err)
	}

	sc.logger.Debug("share added (sync)",
		zap.String("hash", share.HashHex()),
		zap.String("miner", share.MinerAddress),
		zap.Int("chain_length", sc.store.Count()),
	)

	return nil
}

// Tip returns the current chain tip.
func (sc *ShareChain) Tip() (*types.Share, bool) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.store.Tip()
}

// GetShare returns a share by hash.
func (sc *ShareChain) GetShare(hash [32]byte) (*types.Share, bool) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.store.Get(hash)
}

// GetAncestors returns shares from the tip walking backwards.
func (sc *ShareChain) GetAncestors(hash [32]byte, count int) []*types.Share {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.store.GetAncestors(hash, count)
}

// Count returns the number of shares in the store.
func (sc *ShareChain) Count() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.store.Count()
}

// GetExpectedTarget returns the expected share target for the next share.
func (sc *ShareChain) GetExpectedTarget() *big.Int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.getExpectedTarget()
}

// GetExpectedTargetForParent returns the expected share target for a share
// whose parent is identified by parentHash. This must be used (instead of
// GetExpectedTarget) when the share's parent may differ from the current tip,
// e.g. during rapid difficulty ramps where the tip moves between job creation
// and share submission.
func (sc *ShareChain) GetExpectedTargetForParent(parentHash [32]byte) *big.Int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.getExpectedTargetForParent(parentHash)
}

// getExpectedTarget must be called with sc.mu held.
func (sc *ShareChain) getExpectedTarget() *big.Int {
	tip, ok := sc.store.Tip()
	if !ok {
		return new(big.Int).Set(MaxShareTarget)
	}

	tipHash := tip.Hash()
	ancestors := sc.store.GetAncestors(tipHash, DifficultyAdjustmentWindow)
	return sc.diffCalc.NextTarget(ancestors)
}

// getExpectedTargetForParent computes the expected target for a share whose
// parent is identified by parentHash. Must be called with sc.mu held.
func (sc *ShareChain) getExpectedTargetForParent(parentHash [32]byte) *big.Int {
	var zeroHash [32]byte
	if parentHash == zeroHash {
		return new(big.Int).Set(MaxShareTarget)
	}

	ancestors := sc.store.GetAncestors(parentHash, DifficultyAdjustmentWindow)
	newTarget := sc.diffCalc.NextTarget(ancestors)

	// Log difficulty adjustments
	if len(ancestors) > 0 {
		parentTarget := ancestors[0].ShareTarget
		if parentTarget != nil {
			oldBits := util.TargetToCompact(parentTarget)
			newBits := util.TargetToCompact(newTarget)
			if oldBits != newBits {
				sc.logger.Debug("difficulty adjustment",
					zap.String("old_bits", fmt.Sprintf("0x%08x", oldBits)),
					zap.String("new_bits", fmt.Sprintf("0x%08x", newBits)),
					zap.Float64("old_difficulty", util.TargetToDifficulty(parentTarget, MaxShareTarget)),
					zap.Float64("new_difficulty", util.TargetToDifficulty(newTarget, MaxShareTarget)),
				)
			}
		}
	}

	return newTarget
}

// PruneOrphans removes shares that are not on the main chain.
// Returns the number of shares pruned.
func (sc *ShareChain) PruneOrphans() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	tip, ok := sc.store.Tip()
	if !ok {
		return 0
	}

	// Collect all hashes on the main chain
	tipHash := tip.Hash()
	ancestors := sc.store.GetAncestors(tipHash, sc.store.Count())
	mainChain := make(map[[32]byte]struct{}, len(ancestors))
	for _, s := range ancestors {
		mainChain[s.Hash()] = struct{}{}
	}

	// Delete any share not on the main chain
	pruned := 0
	for _, h := range sc.store.AllHashes() {
		if _, onMain := mainChain[h]; !onMain {
			if err := sc.store.Delete(h); err == nil {
				pruned++
			}
		}
	}

	return pruned
}

// PruneOldShares removes shares beyond the most recent maxKeep on the main chain.
// Returns the number of shares pruned.
func (sc *ShareChain) PruneOldShares(maxKeep int) int {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	tip, ok := sc.store.Tip()
	if !ok || sc.store.Count() <= maxKeep {
		return 0
	}

	tipHash := tip.Hash()
	ancestors := sc.store.GetAncestors(tipHash, maxKeep)

	keepSet := make(map[[32]byte]struct{}, len(ancestors))
	for _, s := range ancestors {
		keepSet[s.Hash()] = struct{}{}
	}

	allHashes := sc.store.AllHashes()
	var toDelete [][32]byte
	for _, h := range allHashes {
		if _, keep := keepSet[h]; !keep {
			toDelete = append(toDelete, h)
		}
	}

	if len(toDelete) == 0 {
		return 0
	}

	pruned := 0

	// Try batch delete if supported
	type batchDeleter interface {
		DeleteBatch([][32]byte) (int, error)
	}
	if bd, ok := sc.store.(batchDeleter); ok {
		n, err := bd.DeleteBatch(toDelete)
		if err != nil {
			sc.logger.Error("batch delete failed during pruning", zap.Error(err))
		}
		pruned = n
	} else {
		for _, h := range toDelete {
			if err := sc.store.Delete(h); err == nil {
				pruned++
			}
		}
	}

	sc.logger.Info("pruned old shares",
		zap.Int("pruned", pruned),
		zap.Int("remaining", sc.store.Count()),
		zap.Int("max_keep", maxKeep),
	)

	return pruned
}

// ValidateLoaded validates all shares loaded from disk.
// Walks the main chain from genesis to tip, validating each share in order.
// Returns an error on the first invalid share found.
func (sc *ShareChain) ValidateLoaded() error {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	tip, ok := sc.store.Tip()
	if !ok {
		return nil // nothing to validate
	}

	tipHash := tip.Hash()
	ancestors := sc.store.GetAncestors(tipHash, sc.store.Count())

	// Reverse to genesis-first order
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}

	sc.logger.Info("validating loaded shares", zap.Int("count", len(ancestors)))

	// Skip timestamp checks during replay — these shares were already
	// validated against the clock when they were first accepted.
	sc.validator.skipTimeChecks = true
	defer func() { sc.validator.skipTimeChecks = false }()

	for _, share := range ancestors {
		if err := sc.validator.ValidateShare(share); err != nil {
			return fmt.Errorf("invalid share %x: %w", share.Hash(), err)
		}
	}

	sc.logger.Info("loaded shares validated", zap.Int("count", len(ancestors)))
	return nil
}

// AllHashes returns the hashes of all shares in the store.
func (sc *ShareChain) AllHashes() [][32]byte {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.store.AllHashes()
}

// Store returns the underlying share store.
func (sc *ShareChain) Store() ShareStore {
	return sc.store
}
