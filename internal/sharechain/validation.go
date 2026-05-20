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
	network        string
	skipTimeChecks bool // set during ValidateLoaded replay
}

// NewValidator creates a new share validator.
func NewValidator(store ShareStore, targetFunc func(parentHash [32]byte) *big.Int, network string) *Validator {
	return &Validator{
		store:      store,
		targetFunc: targetFunc,
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

	// 3. Parent exists (unless genesis). The only Indeterminate case: an
	// honest peer ahead of our sync would trip this, so do not penalize.
	var zeroHash [32]byte
	if share.PrevShareHash != zeroHash {
		if !v.store.Has(share.PrevShareHash) {
			return &ValidationError{
				Reason:   fmt.Sprintf("parent share %x not found", share.PrevShareHash[:8]),
				Category: CategoryIndeterminate,
			}
		}
	}

	// 4. Timestamp validation (skipped when replaying from disk)
	if !v.skipTimeChecks {
		now := time.Now()
		shareTime := share.Time()

		// Not too far in the future
		if shareTime.After(now.Add(MaxTimeFuture)) {
			return &ValidationError{Reason: fmt.Sprintf("share timestamp %v is too far in the future", shareTime)}
		}

		// Not too far behind parent
		if share.PrevShareHash != zeroHash {
			parent, ok := v.store.Get(share.PrevShareHash)
			if ok {
				parentTime := parent.Time()
				if shareTime.Before(parentTime.Add(-MaxTimePast)) {
					return &ValidationError{Reason: "share timestamp is too far behind parent"}
				}
			}
		}
	}

	// 5. Expected target — compute via targetFunc from parent
	expectedTarget := v.targetFunc(share.PrevShareHash)

	// 6. PoW check — share must meet the consensus-computed target
	if !share.MeetsTarget(expectedTarget) {
		return &ValidationError{Reason: "share does not meet required target"}
	}

	// 7. ShareTarget consistency — declared target must match consensus
	declaredBits := util.TargetToCompact(share.ShareTarget)
	expectedBits := util.TargetToCompact(expectedTarget)
	if declaredBits != expectedBits {
		return &ValidationError{Reason: fmt.Sprintf(
			"share target mismatch: declared bits 0x%08x, expected 0x%08x", declaredBits, expectedBits)}
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

		// 9. Miner in outputs — coinbase must pay MinerAddress
		outputs, err := types.ParseCoinbaseOutputs(share.CoinbaseTx)
		if err != nil {
			return &ValidationError{Reason: fmt.Sprintf("coinbase output parsing failed: %v", err)}
		}
		if err := types.ValidateMinerInOutputs(outputs, share.MinerAddress, v.network); err != nil {
			return &ValidationError{Reason: fmt.Sprintf("miner not in coinbase outputs: %v", err)}
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
