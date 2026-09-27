// Regression tests for finding 11 in specs/README.md: the vardiff grace
// window, and the difficulty bounds it must respect.

package stratum

import (
	"testing"
	"time"
)

// A share the miner found at the difficulty it was most recently told must
// still be accepted when it arrives. node.go accepts a submission that meets
// either Vardiff.Difficulty() or Vardiff.PrevDifficulty(), which gives exactly
// one change of grace. This asks whether two changes can land close enough
// together to close that window on work already in flight.
func TestVardiffGraceWindow(t *testing.T) {
	// node.go now accepts a share meeting any difficulty still inside the
	// grace window, not just the single most recent one.
	accepted := func(v *Vardiff, d float64) bool {
		for _, a := range v.AcceptableDifficulties() {
			if a == d {
				return true
			}
		}
		return false
	}

	// Case 1: two vardiff retargets. RecordShare only retargets once
	// VardiffRetargetTime has elapsed, so these are >= 60s apart.
	v := NewVardiff(100)
	inFlight := v.Difficulty()
	for i := 0; i < 2; i++ {
		v.lastRetarget = time.Now().Add(-VardiffRetargetTime - time.Second)
		v.shareCount = 1000 // far faster than target: difficulty must rise
		if !v.RecordShare() {
			t.Fatalf("retarget %d did not fire", i)
		}
	}
	t.Logf("two vardiff retargets (>=60s apart): in-flight %.3f, now cur=%.3f prev=%.3f, accepted=%v",
		inFlight, v.Difficulty(), v.PrevDifficulty(), accepted(v, inFlight))

	// Case 2: a vardiff retarget, then the miner calls mining.suggest_difficulty.
	// SetDifficulty has no timing guard, so this can follow immediately.
	v2 := NewVardiff(100)
	inFlight2 := v2.Difficulty()
	v2.lastRetarget = time.Now().Add(-VardiffRetargetTime - time.Second)
	v2.shareCount = 1000
	if !v2.RecordShare() {
		t.Fatal("retarget did not fire")
	}
	afterRetarget := v2.Difficulty()
	v2.SetDifficulty(50) // miner-controlled, arrives milliseconds later
	ok := accepted(v2, inFlight2)
	t.Logf("retarget then suggest_difficulty (no minimum gap): in-flight %.3f, "+
		"retargeted to %.3f, then set to %.3f; now cur=%.3f prev=%.3f, accepted=%v",
		inFlight2, afterRetarget, 50.0, v2.Difficulty(), v2.PrevDifficulty(), ok)
	if !ok {
		t.Error("a share found at the difficulty the miner was last told was " +
			"rejected after a retarget followed immediately by a " +
			"suggest_difficulty")
	}
}

// Whatever sequence of retargets and miner suggestions occurs, the difficulty
// must stay inside the configured bounds.
func TestVardiffStaysInBounds(t *testing.T) {
	suggestions := []float64{-5, 0, 0.0000001, 1, 1e12, 1e300}
	counts := []int{0, 1, 10, 100000}
	for _, s := range suggestions {
		for _, c := range counts {
			v := NewVardiff(100)
			v.SetDifficulty(s)
			v.lastRetarget = time.Now().Add(-VardiffRetargetTime - time.Second)
			v.shareCount = c
			v.RecordShare()
			d := v.Difficulty()
			if d < VardiffMinDifficulty || d > VardiffMaxDifficulty {
				t.Errorf("suggest=%g shares=%d -> difficulty %g outside [%g, %g]",
					s, c, d, VardiffMinDifficulty, VardiffMaxDifficulty)
			}
		}
	}
}
