// Command reproducer replays, against the real internal/sharechain code, the
// counterexamples TLC found for specs/ForkChoice.tla.
//
//	go run ./specs/reproducer
//
// Both were fixed; this now serves as a regression check and exits non-zero
// if either reappears. It builds shares directly rather than mining them:
// ForkChoice and MemoryStore do no validation, so only PrevShareHash and
// ShareTarget matter here.
package main

import (
	"fmt"
	"math/big"
	"os"

	"github.com/djkazic/p2pool-go/internal/sharechain"
	"github.com/djkazic/p2pool-go/internal/types"
)

var failed bool

func check(ok bool, format string, args ...any) {
	status := "ok  "
	if !ok {
		status = "FAIL"
		failed = true
	}
	fmt.Printf("   [%s] %s\n", status, fmt.Sprintf(format, args...))
}

// newShare returns a share with the given parent whose shareDifficulty() is
// exactly diff, made unique by its nonce.
func newShare(nonce uint32, parent [32]byte, diff int64) *types.Share {
	return &types.Share{
		Header:        types.ShareHeader{Nonce: nonce, Bits: 0x207fffff},
		ShareVersion:  1,
		PrevShareHash: parent,
		ShareTarget:   new(big.Int).Div(sharechain.MaxShareTarget, big.NewInt(diff)),
	}
}

// buildChain appends n shares of difficulty 1 to the store, starting from
// parent, assigning work as it goes just as AddShare does.
func buildChain(store *sharechain.MemoryStore, fc *sharechain.ForkChoice, parent [32]byte, n int, firstNonce uint32) [32]byte {
	cur := parent
	for i := 0; i < n; i++ {
		s := newShare(firstNonce+uint32(i), cur, 1)
		if err := store.Add(s); err != nil {
			panic(err)
		}
		fc.AssignWork(s)
		cur = s.Hash()
	}
	return cur
}

func main() {
	var zero [32]byte

	// ---------------------------------------------------------------
	// Finding 1: ChainWork must count the whole chain, not a window.
	// ---------------------------------------------------------------
	fmt.Println("== finding 1: ChainWork vs. true cumulative work ==")
	{
		store := sharechain.NewMemoryStore()
		fc := sharechain.NewForkChoice(store)
		tip := buildChain(store, fc, zero, 6, 1000)
		got := fc.ChainWork(tip)
		check(got.Cmp(big.NewInt(6)) == 0,
			"6 shares of difficulty 1 -> ChainWork = %s (want 6)", got)
	}
	// Same chain with no work assigned up front: the cold path, as after a
	// restart, must reach the same total.
	{
		store := sharechain.NewMemoryStore()
		fc := sharechain.NewForkChoice(store)
		cur := zero
		for i := 0; i < 6; i++ {
			s := newShare(uint32(2000+i), cur, 1)
			store.Add(s)
			cur = s.Hash()
		}
		got := fc.ChainWork(cur)
		check(got.Cmp(big.NewInt(6)) == 0,
			"same chain, work rebuilt from cold -> ChainWork = %s (want 6)", got)
	}

	// ---------------------------------------------------------------
	// Finding 2: the heavier chain must win, whatever the hashes are.
	// Mirrors the TLC trace: main chain 1<-2<-3<-5, fork 4 off share 2.
	// ---------------------------------------------------------------
	fmt.Println("\n== finding 2: SelectTip must pick the heavier chain ==")
	store := sharechain.NewMemoryStore()
	fc := sharechain.NewForkChoice(store)

	// Shared prefix of two shares.
	g1 := newShare(1, zero, 1)
	store.Add(g1)
	fc.AssignWork(g1)
	g2 := newShare(2, g1.Hash(), 1)
	store.Add(g2)
	fc.AssignWork(g2)

	// Main chain: two more on top of g2 — 4 shares of work.
	m1 := newShare(3, g2.Hash(), 1)
	store.Add(m1)
	fc.AssignWork(m1)
	m2 := newShare(4, m1.Hash(), 1)
	store.Add(m2)
	fc.AssignWork(m2)

	// Fork: one share off g2 — 3 shares of work.
	f1 := newShare(5, g2.Hash(), 1)
	store.Add(f1)
	fc.AssignWork(f1)

	mainWork := fc.ChainWork(m2.Hash())
	forkWork := fc.ChainWork(f1.Hash())
	check(mainWork.Cmp(big.NewInt(4)) == 0, "main chain ChainWork = %s (want 4)", mainWork)
	check(forkWork.Cmp(big.NewInt(3)) == 0, "fork chain ChainWork = %s (want 3)", forkWork)
	check(forkWork.Cmp(mainWork) < 0, "fork compares as lighter than main chain")

	check(fc.SelectTip(m2.Hash(), f1.Hash()) == m2.Hash(),
		"SelectTip(main, fork) keeps the 4-share main chain")
	// Order must not matter: fork choice is a function of the share set.
	check(fc.SelectTip(f1.Hash(), m2.Hash()) == m2.Hash(),
		"SelectTip(fork, main) also selects the 4-share main chain")

	fmt.Printf("\n   (hash order: main=%s fork=%s)\n", m2.HashHex()[:16], f1.HashHex()[:16])

	// ---------------------------------------------------------------
	// Finding 3: the retarget must not ratchet the target to zero.
	// ---------------------------------------------------------------
	fmt.Println("\n== finding 3: share target floor ==")
	check(sharechain.MinShareTarget.Sign() > 0, "MinShareTarget is positive")
	check(sharechain.MinShareTarget.Cmp(sharechain.MaxShareTarget) < 0,
		"MinShareTarget is below MaxShareTarget")
	headroom := new(big.Int).Div(sharechain.DifficultyOneTarget, sharechain.MinShareTarget)
	check(headroom.BitLen() > 48,
		"floor allows a share difficulty of ~2^%d, far above Bitcoin's ~2^47", headroom.BitLen()-1)

	if failed {
		fmt.Println("\nREGRESSION: at least one check failed")
		os.Exit(1)
	}
	fmt.Println("\nall checks passed")
}
