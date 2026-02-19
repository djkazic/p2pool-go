package stratum

import (
	"time"
)

const (
	// VardiffTargetTime is the desired time between shares per miner.
	VardiffTargetTime = 10 * time.Second

	// VardiffRetargetTime is how often to recalculate difficulty.
	VardiffRetargetTime = 60 * time.Second

	// VardiffMinDifficulty is the minimum stratum difficulty.
	VardiffMinDifficulty = 0.001

	// VardiffMaxDifficulty is the maximum stratum difficulty.
	VardiffMaxDifficulty = 1000000.0

	// VardiffVariancePercent is the acceptable variance before adjustment.
	VardiffVariancePercent = 25.0
)

// Vardiff manages per-miner variable difficulty.
type Vardiff struct {
	difficulty     float64
	prevDifficulty float64 // previous difficulty, accepted during grace period after a change
	targetTime     time.Duration

	// Tracking
	lastRetarget time.Time
	shareCount   int
}

// NewVardiff creates a new variable difficulty manager.
func NewVardiff(initialDifficulty float64) *Vardiff {
	return &Vardiff{
		difficulty:   initialDifficulty,
		targetTime:   VardiffTargetTime,
		lastRetarget: time.Now(),
	}
}

// SetDifficulty sets the difficulty to the given value, clamped to
// [VardiffMinDifficulty, VardiffMaxDifficulty]. It resets the retarget
// timer and share count so vardiff doesn't immediately override.
func (v *Vardiff) SetDifficulty(diff float64) {
	if diff < VardiffMinDifficulty {
		diff = VardiffMinDifficulty
	}
	if diff > VardiffMaxDifficulty {
		diff = VardiffMaxDifficulty
	}
	v.prevDifficulty = v.difficulty
	v.difficulty = diff
	v.lastRetarget = time.Now()
	v.shareCount = 0
}

// Difficulty returns the current difficulty.
func (v *Vardiff) Difficulty() float64 {
	return v.difficulty
}

// PrevDifficulty returns the difficulty before the most recent retarget.
// Returns 0 if no retarget has occurred.
func (v *Vardiff) PrevDifficulty() float64 {
	return v.prevDifficulty
}

// RecordShare records a share submission and returns true if difficulty should change.
func (v *Vardiff) RecordShare() bool {
	v.shareCount++

	elapsed := time.Since(v.lastRetarget)
	if elapsed < VardiffRetargetTime {
		return false
	}

	return v.retarget(elapsed)
}

// retarget adjusts the difficulty and returns true if it changed.
func (v *Vardiff) retarget(elapsed time.Duration) bool {
	if v.shareCount == 0 {
		return false
	}

	// Actual time per share
	actualTime := elapsed.Seconds() / float64(v.shareCount)
	targetTime := v.targetTime.Seconds()

	// Check if within acceptable variance
	low := targetTime * (1.0 - VardiffVariancePercent/100.0)
	high := targetTime * (1.0 + VardiffVariancePercent/100.0)

	if actualTime >= low && actualTime <= high {
		v.lastRetarget = time.Now()
		v.shareCount = 0
		return false
	}

	// Adjust: newDiff = oldDiff * (targetTime / actualTime)
	ratio := targetTime / actualTime
	newDiff := v.difficulty * ratio

	// Clamp
	if newDiff < VardiffMinDifficulty {
		newDiff = VardiffMinDifficulty
	}
	if newDiff > VardiffMaxDifficulty {
		newDiff = VardiffMaxDifficulty
	}

	v.prevDifficulty = v.difficulty
	v.difficulty = newDiff
	v.lastRetarget = time.Now()
	v.shareCount = 0

	return true
}
