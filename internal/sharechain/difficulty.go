package sharechain

import (
	"math/big"
	"sort"
	"time"

	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/pkg/util"
)

const (
	// DifficultyAdjustmentWindow is the number of shares to look back for difficulty adjustment.
	DifficultyAdjustmentWindow = 72 // ~36 minutes at 30s target

	// difficultyOneBits is Bitcoin's difficulty-1 target. It is the
	// denominator used to express a target as a difficulty for display and
	// metrics; it is not a bound on the sharechain.
	difficultyOneBits = 0x1d00ffff

	// minShareTargetBits is the absolute floor on the share target — the
	// hardest work the sharechain will ever ask of a miner.
	//
	// The operational floor is clampToNetwork (chain.go), which stops the
	// share target dropping below Bitcoin's own target, since a share harder
	// than a block is pointless. But that clamp is inert until the first
	// SetNetworkTarget call, and NextTarget can divide the target by 4 on
	// every step, so without a floor here the target ratchets toward zero —
	// and at zero no hash satisfies hash <= target, so the sharechain stops
	// accepting shares permanently.
	//
	// 0x1500ffff is a target of ~2^160, i.e. a share difficulty of ~2^64.
	// Bitcoin's own difficulty has never exceeded ~2^47, so this can never
	// bind on a live pool; it exists only to keep the retarget away from
	// zero. It is exactly representable in compact form, so the round-trip
	// at the end of NextTarget cannot push a clamped value back below it.
	minShareTargetBits = 0x1500ffff

	// MaxShareTarget is the easiest possible share target (highest allowed value).
	// Uses regtest-style max target so CPU miners can produce shares.
	maxShareTargetBits = 0x207fffff

	// MTPDepth is the number of shares at each end of the window whose
	// timestamps are reduced to a median before being used in the difficulty
	// calculation. Matches Bitcoin Core's GetMedianTimePast depth.
	//
	// MaxTimeFuture allows a single share's timestamp to be up to 2h ahead
	// of real time (bitcoind curtime drift + ntime rolling). If the newest
	// timestamp drove actualTime directly, one attacker-controlled share at
	// the window edge could swing the timing ratio enough to hit the 4x clamp
	// every step and hold the chain at artificially-easy difficulty.
	// Taking a median over the newest N samples means defeating it requires
	// a majority of the window — i.e., majority hashrate — not a single share.
	MTPDepth = 11
)

var (
	// DifficultyOneTarget is Bitcoin's difficulty-1 target, used to express
	// targets as difficulties.
	DifficultyOneTarget = util.CompactToTarget(difficultyOneBits)

	// MinShareTarget is the absolute floor on the share target.
	MinShareTarget = util.CompactToTarget(minShareTargetBits)

	// MaxShareTarget is the ceiling — the easiest share the chain will set.
	MaxShareTarget = util.CompactToTarget(maxShareTargetBits)
)

// Payout parameters are consensus rules, not tunables. Validation recomputes
// a share's payout split from its own PPLNS window and compares it against the
// coinbase, so two nodes configured differently would reject each other's
// shares and the pool would split.
const (
	// ConsensusFinderFeeBasisPoints is the block finder's cut, in hundredths
	// of a percent. 50 is 0.50%.
	ConsensusFinderFeeBasisPoints = 50

	// ConsensusDustThresholdSats is the smallest payout that may appear as its
	// own coinbase output; anything below is folded into a larger one.
	ConsensusDustThresholdSats = 546
)

// DifficultyCalculator adjusts sharechain difficulty.
type DifficultyCalculator struct {
	targetTime time.Duration
}

// NewDifficultyCalculator creates a new difficulty calculator.
func NewDifficultyCalculator(targetTime time.Duration) *DifficultyCalculator {
	return &DifficultyCalculator{
		targetTime: targetTime,
	}
}

// NextTarget calculates the next share target based on a window of recent shares.
// Uses: newTarget = currentTarget * (actualTime / expectedTime), clamped to 4x.
//
// The window is trimmed to only include shares whose target is within 4x of the
// newest share's target. During difficulty transitions (cold start, hashrate
// changes), the window may contain shares at wildly different difficulties.
// Including stale-difficulty shares distorts the timing data — e.g., 70 instant
// shares at MaxShareTarget would dominate the window average even after the
// algorithm has found the right difficulty, causing compounding overshoot or
// glacially slow convergence. Trimming ensures the algorithm uses only timing
// data from shares at a comparable difficulty level.
func (dc *DifficultyCalculator) NextTarget(shares []*types.Share) *big.Int {
	if len(shares) < 2 {
		return new(big.Int).Set(MaxShareTarget)
	}

	window := shares
	if len(window) > DifficultyAdjustmentWindow {
		window = window[:DifficultyAdjustmentWindow]
	}

	// window[0] is the most recent share, window[len-1] is the oldest
	newest := window[0]

	currentTarget := newest.ShareTarget
	if currentTarget == nil || currentTarget.Sign() == 0 {
		return new(big.Int).Set(MaxShareTarget)
	}

	// Trim window to shares with targets within 4x of the newest share.
	// This matches the 4x per-step clamp: shares more than 4x away are from
	// a different difficulty regime and their timing data is not comparable.
	upper := new(big.Int).Mul(currentTarget, big.NewInt(4))
	lower := new(big.Int).Div(currentTarget, big.NewInt(4))
	for i := 1; i < len(window); i++ {
		st := window[i].ShareTarget
		if st == nil || st.Sign() == 0 || st.Cmp(upper) > 0 || st.Cmp(lower) < 0 {
			window = window[:i]
			break
		}
	}

	if len(window) < 2 {
		// Not enough similar-difficulty shares for timing-based adjustment.
		// Return the newest share's target unchanged.
		return util.CompactToTarget(util.TargetToCompact(currentTarget))
	}

	// Use median-time-past at each window edge instead of single endpoints,
	// so that no single share's timestamp can swing the timing ratio. The
	// two groups must not overlap; cap n at len(window)/3 to keep a span
	// of at least 1/3 of the window between them.
	n := MTPDepth
	if maxN := len(window) / 3; n > maxN {
		n = maxN
	}
	if n < 1 {
		n = 1
	}
	newestMedian := medianTimestamp(window[:n])
	oldestMedian := medianTimestamp(window[len(window)-n:])

	actualTime := int64(newestMedian) - int64(oldestMedian)
	if actualTime <= 0 {
		actualTime = 1
	}

	// The timing span is between the two medians, so the expected time covers
	// the (len(window) - n) inter-share intervals separating them. Using
	// len(window)-1 would over-estimate the expected duration and bias the
	// adjustment toward "too easy."
	intervals := int64(len(window) - n)
	if intervals < 1 {
		intervals = 1
	}
	expectedTime := int64(dc.targetTime.Seconds()) * intervals
	if expectedTime <= 0 {
		expectedTime = 1
	}

	// newTarget = currentTarget * actualTime / expectedTime
	newTarget := new(big.Int).Mul(currentTarget, big.NewInt(actualTime))
	newTarget.Div(newTarget, big.NewInt(expectedTime))

	// Clamp to 4x adjustment per calculation
	maxAdjust := new(big.Int).Mul(currentTarget, big.NewInt(4))
	minAdjust := new(big.Int).Div(currentTarget, big.NewInt(4))

	if newTarget.Cmp(maxAdjust) > 0 {
		newTarget.Set(maxAdjust)
	}
	if newTarget.Cmp(minAdjust) < 0 {
		newTarget.Set(minAdjust)
	}

	// Clamp to global limits. Both bounds are applied: without the lower
	// one the 4x-per-step reduction above has nothing to stop it, and a
	// long enough run of fast shares drives the target to zero.
	if newTarget.Cmp(MaxShareTarget) > 0 {
		newTarget.Set(MaxShareTarget)
	}
	if newTarget.Cmp(MinShareTarget) < 0 {
		newTarget.Set(MinShareTarget)
	}
	// Normalize through compact round-trip so all nodes produce identical
	// big.Int values regardless of whether a share was mined locally or
	// received via P2P (where targets are transmitted as compact uint32).
	return util.CompactToTarget(util.TargetToCompact(newTarget))
}

// medianTimestamp returns the median Header.Timestamp of shares.
// Used to defang single-sample timestamp manipulation in NextTarget.
func medianTimestamp(shares []*types.Share) uint32 {
	times := make([]uint32, len(shares))
	for i, s := range shares {
		times[i] = s.Header.Timestamp
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2]
}
