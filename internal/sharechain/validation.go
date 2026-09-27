package sharechain

import (
	"fmt"
	"math/big"
	"time"

	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/pkg/util"
)

const (
	// MaxTimeFuture is the maximum time a share's timestamp can be ahead of our clock.
	// Matches Bitcoin's MAX_FUTURE_BLOCK_TIME. Share timestamps come from bitcoind's
	// curtime (= max(MTP+1, local_time)), which can be well ahead of real time when
	// recent blocks had future timestamps — up to ~90min drift has been observed.
	// ASIC ntime rolling adds more on top. This does NOT affect difficulty calculation:
	// all shares are inflated by the same amount, so relative differences are correct.
	MaxTimeFuture = 2 * time.Hour

	// MaxTimePast is the maximum time a share's timestamp can be behind the parent.
	MaxTimePast = 10 * time.Minute

	// maxCoinbaseTxSize is the maximum allowed coinbase transaction size.
	// Bitcoin consensus allows up to ~1 MB, but a legitimate coinbase is
	// 1–4 KB; even a PPLNS payout with ~200 P2WPKH outputs fits well
	// under 8 KB. A tight cap limits attacker bandwidth and memory
	// amplification through this field.
	maxCoinbaseTxSize = 8 * 1024

	// maxMinerAddressLen is the maximum allowed miner address length.
	// Bech32m addresses are at most ~90 characters.
	maxMinerAddressLen = 128
)

// ValidationCategory classifies whether a rejection is the sender's fault.
//
// CategoryProvable: the share is definitively invalid, regardless of our
// chain state — wrong commitment, miner not in outputs, mismatched target,
// future timestamp past the protocol bound, etc. A peer relaying such a
// share is either malicious or buggy; misbehavior counters should fire.
//
// CategoryIndeterminate: we can't tell yet, typically because we're behind
// on sync and don't know the parent. An honest peer ahead of us will
// produce this; do NOT penalize.
type ValidationCategory int

const (
	CategoryProvable ValidationCategory = iota
	CategoryIndeterminate
)

// ValidationError represents a share validation failure.
type ValidationError struct {
	Reason   string
	Category ValidationCategory
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("share validation failed: %s", e.Reason)
}

// Validator validates incoming shares.
type Validator struct {
	store          ShareStore
	targetFunc     func(parentHash [32]byte) *big.Int
	payoutFunc     func(parentHash [32]byte, totalReward int64, finder string) []types.PayoutEntry
	windowSize     int
	network        string
	skipTimeChecks bool // set during ValidateLoaded replay
	allowRoot      bool // set by AddShareAsRoot when anchoring a synced chain
}

// historyIsComplete reports whether this node can see depth shares of ancestry
// behind parentHash — the history a consensus rule needs before it can be
// applied. Called with DifficultyAdjustmentWindow for the target check and
// with the PPLNS window size for the payout check.
//
// It returns true when the walk reaches the zero hash inside the window — a
// young chain that every node sees identically — or when a full window of
// ancestors is present. It returns false only when the walk runs into a share
// this node no longer holds, i.e. its prune horizon.
//
// That distinction is what makes the consensus target agreeable. Nodes prune
// at different points, so a node near its horizon sees fewer ancestors than
// its peers and NextTarget would hand it a different answer for the very same
// parent — a target mismatch it would then charge to the peer as provable
// misbehavior. Where the history is incomplete we do not claim to know the
// consensus target at all.
func (v *Validator) historyIsComplete(parentHash [32]byte, depth int) bool {
	var zeroHash [32]byte
	current := parentHash
	for i := 0; i < depth; i++ {
		if current == zeroHash {
			return true // reached the start of the chain within the window
		}
		share, ok := v.store.Get(current)
		if !ok {
			return false // prune horizon — peers may see further back
		}
		current = share.PrevShareHash
	}
	return true
}

// NewValidator creates a new share validator.
func NewValidator(
	store ShareStore,
	targetFunc func(parentHash [32]byte) *big.Int,
	payoutFunc func(parentHash [32]byte, totalReward int64, finder string) []types.PayoutEntry,
	windowSize int,
	network string,
) *Validator {
	return &Validator{
		store:      store,
		targetFunc: targetFunc,
		payoutFunc: payoutFunc,
		windowSize: windowSize,
		network:    network,
	}
}

// ValidateShare performs all validation checks on a share.
func (v *Validator) ValidateShare(share *types.Share) error {
	// 1. ShareVersion must equal 1
	if share.ShareVersion != 1 {
		return &ValidationError{Reason: fmt.Sprintf("unsupported share version %d, expected 1", share.ShareVersion)}
	}

	// 2. Size limits — reject before any expensive processing
	if len(share.MinerAddress) > maxMinerAddressLen {
		return &ValidationError{Reason: fmt.Sprintf("miner address too long: %d bytes", len(share.MinerAddress))}
	}
	if len(share.CoinbaseTx) > maxCoinbaseTxSize {
		return &ValidationError{Reason: fmt.Sprintf("coinbase tx too large: %d bytes", len(share.CoinbaseTx))}
	}

	// 3. MinerAddress must be valid bech32 for network
	if share.MinerAddress == "" {
		return &ValidationError{Reason: "missing miner address"}
	}
	if err := types.ValidateAddress(share.MinerAddress, v.network); err != nil {
		return &ValidationError{Reason: fmt.Sprintf("invalid miner address: %v", err)}
	}

	// 3. Parent exists (unless genesis, or unless this share is the root of a
	// chain we are bootstrapping). The only Indeterminate case: an honest peer
	// ahead of our sync would trip this, so do not penalize.
	var zeroHash [32]byte
	isRoot := false
	if share.PrevShareHash != zeroHash && !v.store.Has(share.PrevShareHash) {
		if !v.allowRoot {
			return &ValidationError{
				Reason:   fmt.Sprintf("parent share %x not found", share.PrevShareHash[:8]),
				Category: CategoryIndeterminate,
			}
		}
		// Every node prunes to a bounded window, so a peer serving us its
		// chain starts it at a share whose parent nobody has any more. Without
		// somewhere to anchor, that first share is unaddable and the whole
		// sync stalls — and once every peer has pruned, nobody can ever join
		// the pool again. Accept it as the chain root. AddShareQuiet only sets
		// allowRoot for an empty store on the sync path, so this happens at
		// most once per node and never from gossip.
		isRoot = true
	}

	// 4. Timestamp validation (skipped when replaying from disk)
	if !v.skipTimeChecks {
		now := time.Now()
		shareTime := share.Time()

		// Not too far in the future.
		//
		// Indeterminate, not provable: this compares against OUR clock. A peer
		// whose clock runs ahead of ours applies the identical rule and accepts,
		// so a rejection here says nothing about that peer's honesty. Share
		// timestamps come from bitcoind's curtime, which as noted above already
		// runs well ahead of real time, leaving little margin under the bound —
		// charging this to the sender would let modest clock skew, or an
		// attacker minting shares near the boundary, make honest nodes
		// disconnect each other.
		if shareTime.After(now.Add(MaxTimeFuture)) {
			return &ValidationError{
				Reason:   fmt.Sprintf("share timestamp %v is too far in the future", shareTime),
				Category: CategoryIndeterminate,
			}
		}

		// Not too far behind parent
		if share.PrevShareHash != zeroHash && !isRoot {
			parent, ok := v.store.Get(share.PrevShareHash)
			if ok {
				parentTime := parent.Time()
				if shareTime.Before(parentTime.Add(-MaxTimePast)) {
					return &ValidationError{Reason: "share timestamp is too far behind parent"}
				}
			}
		}
	}

	// 5-7. Target checks.
	//
	// The consensus target is only well defined where we hold the history it
	// is derived from. Inside our prune horizon every node computes the same
	// value and the share must match it exactly. At the horizon — the oldest
	// shares of a chain we bootstrapped, which other nodes may still see
	// further back than we do — we cannot compute an agreeable value, so we
	// fall back to checking the share against the target it declares, bounded
	// to the protocol range. ValidateLoaded has always skipped these oldest
	// shares for the same reason; this applies the same rule on the live path.
	//
	// The relaxation is confined to history: any share whose parent has a full
	// window behind it, which is every share at the tip of a chain longer than
	// the window, takes the strict path.
	if !isRoot && v.historyIsComplete(share.PrevShareHash, DifficultyAdjustmentWindow) {
		expectedTarget := v.targetFunc(share.PrevShareHash)

		// PoW check — share must meet the consensus-computed target
		if !share.MeetsTarget(expectedTarget) {
			return &ValidationError{Reason: "share does not meet required target"}
		}

		// ShareTarget consistency — declared target must match consensus
		declaredBits := util.TargetToCompact(share.ShareTarget)
		expectedBits := util.TargetToCompact(expectedTarget)
		if declaredBits != expectedBits {
			return &ValidationError{Reason: fmt.Sprintf(
				"share target mismatch: declared bits 0x%08x, expected 0x%08x", declaredBits, expectedBits)}
		}
	} else {
		if share.ShareTarget == nil || share.ShareTarget.Sign() <= 0 {
			return &ValidationError{Reason: "missing share target"}
		}
		if share.ShareTarget.Cmp(MaxShareTarget) > 0 {
			return &ValidationError{Reason: "share target above maximum"}
		}
		if share.ShareTarget.Cmp(MinShareTarget) < 0 {
			return &ValidationError{Reason: "share target below minimum"}
		}
		if !share.MeetsShareTarget() {
			return &ValidationError{Reason: "share does not meet its declared target"}
		}
	}

	// 8. Coinbase commitment — must contain correct PrevShareHash
	if len(share.CoinbaseTx) > 0 {
		committedHash, err := types.ExtractShareCommitment(share.CoinbaseTx)
		if err != nil {
			return &ValidationError{Reason: fmt.Sprintf("coinbase commitment extraction failed: %v", err)}
		}
		if committedHash != share.PrevShareHash {
			return &ValidationError{Reason: fmt.Sprintf(
				"coinbase commitment %x does not match PrevShareHash %x",
				committedHash[:8], share.PrevShareHash[:8])}
		}

		// 9. Coinbase outputs must be the PPLNS split for this share's window.
		//
		// This is the rule that makes the pool trustless. Without it the only
		// requirement is that the coinbase pays the submitting miner
		// *something*, and a miner can take the entire block reward while the
		// window's real contributors get nothing.
		//
		// The reward total is read from the share's own coinbase rather than
		// from a block template: the template depends on each node's mempool,
		// so peers cannot agree on an absolute figure. What they can agree on
		// is how a given total must be divided, which is what is checked here.
		outputs, err := types.ParseCoinbaseOutputs(share.CoinbaseTx)
		if err != nil {
			return &ValidationError{Reason: fmt.Sprintf("coinbase output parsing failed: %v", err)}
		}
		if err := types.ValidateMinerInOutputs(outputs, share.MinerAddress, v.network); err != nil {
			return &ValidationError{Reason: fmt.Sprintf("miner not in coinbase outputs: %v", err)}
		}

		// Only enforceable where we hold the window the split is derived from.
		// Near a node's prune horizon peers see further back than we do, so a
		// split computed here would differ from theirs; the same reasoning as
		// the target check above.
		if !isRoot && v.payoutFunc != nil && v.historyIsComplete(share.PrevShareHash, v.windowSize) {
			var totalReward int64
			for _, out := range outputs {
				if out.Value > 0 {
					totalReward += out.Value
				}
			}
			expected := v.payoutFunc(share.PrevShareHash, totalReward, share.MinerAddress)
			// An empty window (the first share of a chain) has no split to
			// enforce; the miner-in-outputs check above still applies.
			if len(expected) > 0 {
				if err := types.ValidatePayoutsInOutputs(outputs, expected, v.network); err != nil {
					return &ValidationError{Reason: fmt.Sprintf("coinbase does not pay the PPLNS window: %v", err)}
				}
			}
		}
	} else {
		return &ValidationError{Reason: "missing coinbase transaction"}
	}

	// Note: nBits (Bitcoin target) is not validated because we cannot know which
	// Bitcoin block template the miner used. The sharechain only requires the
	// share hash to meet the sharechain target.

	return nil
}

// IsBlock checks if a validated share also meets Bitcoin's full difficulty.
func (v *Validator) IsBlock(share *types.Share) bool {
	return share.MeetsBitcoinTarget()
}
