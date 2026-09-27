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

	// VardiffGraceWindow is how long a difficulty stays acceptable after it
	// has been superseded. A miner that was told difficulty D keeps hashing
	// at D until it receives and applies the next set_difficulty, so shares
	// found at D can still arrive after the change.
	VardiffGraceWindow = 60 * time.Second

	// maxGraceDifficulties bounds the retained history.
	maxGraceDifficulties = 8
)

// issuedDifficulty is a difficulty the miner was told, and when.
type issuedDifficulty struct {
	value float64
	at    time.Time
}

// Vardiff manages per-miner variable difficulty.
type Vardiff struct {
	difficulty float64
	targetTime time.Duration

	// superseded holds difficulties this miner was told previously and which
	// have not yet aged out, newest first. A single "previous" slot is not
	// enough: RecordShare cannot retarget more often than VardiffRetargetTime,
	// but SetDifficulty — reached from mining.suggest_difficulty with a
	// miner-supplied value — has no minimum interval, so two changes can land
	// back to back and strand shares that were already in flight.
	superseded []issuedDifficulty

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

// supersede records the outgoing difficulty as still-acceptable and drops any
// entries that have aged past the grace window.
func (v *Vardiff) supersede(old float64) {
	now := time.Now()
	kept := make([]issuedDifficulty, 0, len(v.superseded)+1)
	if old > 0 {
		kept = append(kept, issuedDifficulty{value: old, at: now})
	}
	for _, d := range v.superseded {
		if now.Sub(d.at) < VardiffGraceWindow && len(kept) < maxGraceDifficulties {
			kept = append(kept, d)
		}
	}
	v.superseded = kept
}

// AcceptableDifficulties returns the current difficulty followed by every
// superseded difficulty still inside the grace window, newest first. A share
// meeting any of them represents work the miner was legitimately asked for.
func (v *Vardiff) AcceptableDifficulties() []float64 {
	now := time.Now()
	out := []float64{v.difficulty}
	for _, d := range v.superseded {
		if now.Sub(d.at) < VardiffGraceWindow && d.value != v.difficulty {
			out = append(out, d.value)
		}
	}
	return out
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
	v.supersede(v.difficulty)
	v.difficulty = diff
	v.lastRetarget = time.Now()
	v.shareCount = 0
}

// Difficulty returns the current difficulty.
func (v *Vardiff) Difficulty() float64 {
	return v.difficulty
}

// PrevDifficulty returns the most recent superseded difficulty, or 0 if there
// is none inside the grace window.
func (v *Vardiff) PrevDifficulty() float64 {
	if acc := v.AcceptableDifficulties(); len(acc) > 1 {
		return acc[1]
	}
	return 0
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

	v.supersede(v.difficulty)
	v.difficulty = newDiff
	v.lastRetarget = time.Now()
	v.shareCount = 0

	return true
}
