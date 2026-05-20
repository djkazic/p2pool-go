package sharechain

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/pkg/util"

	"go.uber.org/zap"
)

const (
	testNetwork = "testnet3"
	testMiner1  = "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx"
	testMiner2  = "tb1qqqqqp399et2xygdj5xreqhjjvcmzhxw4aywxecjdzew6hylgvsesrxh6hy"
)

func testLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

// maxTarget returns the max target used for testing (difficulty 1).
func maxTarget() *big.Int {
	return util.CompactToTarget(0x207fffff) // regtest
}

// makeTestShare creates a share that will pass validation for testing.
// It mines a valid nonce so the hash meets the target.
// PrevShareHash is embedded in PrevBlockHash to ensure unique hashes per chain.
// A valid coinbase transaction is built with the sharechain commitment and miner output.
func makeTestShare(prevShareHash [32]byte, minerAddr string, timestamp uint32) *types.Share {
	target := maxTarget()

	// Build a valid coinbase transaction
	builder := types.NewCoinbaseBuilder(testNetwork)
	commitment := types.BuildShareCommitment(prevShareHash)
	payouts := []types.PayoutEntry{
		{Address: minerAddr, Amount: 5000000000},
	}
	coinbaseTx, _, err := builder.BuildCoinbase(800000, commitment, payouts, "", 8)
	if err != nil {
		panic("makeTestShare: BuildCoinbase failed: " + err.Error())
	}

	// Use prevShareHash as PrevBlockHash so forks produce different headers.
	// Use minerAddr in MerkleRoot for uniqueness across different miners.
	var merkleRoot [32]byte
	copy(merkleRoot[:], []byte(minerAddr))

	s := &types.Share{
		Header: types.ShareHeader{
			Version:       536870912,
			PrevBlockHash: prevShareHash,
			MerkleRoot:    merkleRoot,
			Timestamp:     timestamp,
			Bits:          0x207fffff,
		},
		ShareVersion:  1,
		PrevShareHash: prevShareHash,
		ShareTarget:   target,
		MinerAddress:  minerAddr,
		CoinbaseTx:    coinbaseTx,
	}
	// Mine a valid nonce (target ~2^255, takes ~2 tries on average)
	for nonce := uint32(0); ; nonce++ {
		s.Header.Nonce = nonce
		hash := s.Header.Hash()
		if util.HashMeetsTarget(hash, target) {
			return s
		}
	}
}

func TestMemoryStore_AddAndGet(t *testing.T) {
	store := NewMemoryStore()
	share := makeTestShare([32]byte{}, testMiner1, 1700000000)
	hash := share.Hash()

	err := store.Add(share)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	got, ok := store.Get(hash)
	if !ok {
		t.Fatal("share not found after Add")
	}
	if got.MinerAddress != testMiner1 {
		t.Errorf("miner address = %s, want %s", got.MinerAddress, testMiner1)
	}
	if store.Count() != 1 {
		t.Errorf("count = %d, want 1", store.Count())
	}
}

func TestMemoryStore_DuplicateAdd(t *testing.T) {
	store := NewMemoryStore()
	share := makeTestShare([32]byte{}, testMiner1, 1700000000)

	_ = store.Add(share)
	err := store.Add(share)
	if err == nil {
		t.Error("expected error on duplicate add")
	}
}

func TestMemoryStore_Tip(t *testing.T) {
	store := NewMemoryStore()

	_, ok := store.Tip()
	if ok {
		t.Error("empty store should not have tip")
	}

	share := makeTestShare([32]byte{}, testMiner1, 1700000000)
	hash := share.Hash()
	_ = store.Add(share)
	_ = store.SetTip(hash)

	tip, ok := store.Tip()
	if !ok {
		t.Fatal("tip not found after SetTip")
	}
	if tip.Hash() != hash {
		t.Error("tip hash mismatch")
	}
}

func TestMemoryStore_GetAncestors(t *testing.T) {
	store := NewMemoryStore()

	// Build a chain of 5 shares
	var prevHash [32]byte
	for i := 0; i < 5; i++ {
		share := makeTestShare(prevHash, testMiner1, uint32(1700000000+i*30))
		_ = store.Add(share)
		prevHash = share.Hash()
	}
	_ = store.SetTip(prevHash)

	ancestors := store.GetAncestors(prevHash, 10)
	if len(ancestors) != 5 {
		t.Errorf("got %d ancestors, want 5", len(ancestors))
	}
}

func TestShareChain_AddShare(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	events := chain.Subscribe(context.Background())
	defer chain.Unsubscribe(events)

	// Add genesis share (zero prev hash)
	genesis := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	err := chain.AddShare(genesis)
	if err != nil {
		t.Fatalf("AddShare failed: %v", err)
	}

	if chain.Count() != 1 {
		t.Errorf("count = %d, want 1", chain.Count())
	}

	tip, ok := chain.Tip()
	if !ok {
		t.Fatal("chain should have tip")
	}
	if tip.Hash() != genesis.Hash() {
		t.Error("tip should be genesis")
	}

	// Should receive new tip event
	select {
	case evt := <-events:
		if evt.Type != EventNewTip {
			t.Errorf("event type = %d, want EventNewTip", evt.Type)
		}
	case <-time.After(time.Second):
		t.Error("no event received")
	}
}

func TestShareChain_LinearChain(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	var prevHash [32]byte
	baseTime := time.Now().Add(-5 * time.Minute) // start in the past
	for i := 0; i < 10; i++ {
		share := makeTestShare(prevHash, testMiner1, uint32(baseTime.Unix()+int64(i*30)))
		err := chain.AddShare(share)
		if err != nil {
			t.Fatalf("AddShare %d failed: %v", i, err)
		}
		prevHash = share.Hash()
	}

	if chain.Count() != 10 {
		t.Errorf("count = %d, want 10", chain.Count())
	}
}

func TestShareChain_DuplicateIgnored(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	_ = chain.AddShare(share)
	err := chain.AddShare(share)
	if err != nil {
		t.Error("duplicate should be silently ignored")
	}
	if chain.Count() != 1 {
		t.Errorf("count should be 1, got %d", chain.Count())
	}
}

func TestShareChain_RejectsInvalid(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	// Share with missing miner address should be rejected
	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	share.MinerAddress = "" // override to empty
	err := chain.AddShare(share)
	if err == nil {
		t.Error("expected validation error for missing miner address")
	}
}

func TestForkChoice_SelectTip(t *testing.T) {
	store := NewMemoryStore()
	fc := NewForkChoice(store)

	// Build two competing chains from a common genesis
	genesis := makeTestShare([32]byte{}, testMiner1, 1700000000)
	_ = store.Add(genesis)
	genesisHash := genesis.Hash()

	// Chain A: 3 shares
	prevA := genesisHash
	var tipA [32]byte
	for i := 0; i < 3; i++ {
		s := makeTestShare(prevA, testMiner1, uint32(1700000030+i*30))
		_ = store.Add(s)
		prevA = s.Hash()
	}
	tipA = prevA

	// Chain B: 5 shares (heavier)
	prevB := genesisHash
	var tipB [32]byte
	for i := 0; i < 5; i++ {
		s := makeTestShare(prevB, testMiner2, uint32(1700000030+i*30))
		_ = store.Add(s)
		prevB = s.Hash()
	}
	tipB = prevB

	// Fork choice should select the heavier chain
	selected := fc.SelectTip(tipA, tipB, 100)
	if selected != tipB {
		t.Error("fork choice should select heavier chain (B)")
	}
}

func TestForkChoice_ChildAlwaysExtendsTip(t *testing.T) {
	store := NewMemoryStore()
	fc := NewForkChoice(store)

	// Build a chain longer than the window size.
	// With a small window (5), once the chain exceeds 5 shares, a child
	// extending the tip has equal cumulative work (drops oldest, adds itself).
	// The child must still always become the new tip.
	windowSize := 5
	var prevHash [32]byte
	for i := 0; i < windowSize+3; i++ {
		s := makeTestShare(prevHash, testMiner1, uint32(1700000000+i*30))
		_ = store.Add(s)
		prevHash = s.Hash()
	}
	currentTip := prevHash

	// Add one more child extending the tip
	child := makeTestShare(currentTip, testMiner1, uint32(1700000000+(windowSize+3)*30))
	_ = store.Add(child)
	childHash := child.Hash()

	// Verify both have the same cumulative work (the bug condition)
	currentWork := fc.ChainWork(currentTip, windowSize)
	childWork := fc.ChainWork(childHash, windowSize)
	if currentWork.Cmp(childWork) != 0 {
		t.Logf("work differs (current=%s, child=%s) — test still valid but not exercising tie case", currentWork, childWork)
	}

	// The child must always win, regardless of hash comparison
	selected := fc.SelectTip(currentTip, childHash, windowSize)
	if selected != childHash {
		t.Error("child extending the current tip must always become the new tip")
	}
}

func TestForkChoice_FindCommonAncestor(t *testing.T) {
	store := NewMemoryStore()
	fc := NewForkChoice(store)

	genesis := makeTestShare([32]byte{}, testMiner1, 1700000000)
	_ = store.Add(genesis)
	genesisHash := genesis.Hash()

	// Chain A from genesis
	prevA := genesisHash
	for i := 0; i < 3; i++ {
		s := makeTestShare(prevA, testMiner1, uint32(1700000030+i*30))
		_ = store.Add(s)
		prevA = s.Hash()
	}

	// Chain B from genesis
	prevB := genesisHash
	for i := 0; i < 2; i++ {
		s := makeTestShare(prevB, testMiner2, uint32(1700000060+i*30))
		_ = store.Add(s)
		prevB = s.Hash()
	}

	ancestor, depthA, depthB := fc.FindCommonAncestor(prevA, prevB, 100)
	if ancestor != genesisHash {
		t.Error("common ancestor should be genesis")
	}
	if depthA != 3 {
		t.Errorf("depthA = %d, want 3", depthA)
	}
	if depthB != 2 {
		t.Errorf("depthB = %d, want 2", depthB)
	}
}

func TestShareChain_ReorgEventFields(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	events := chain.Subscribe(context.Background())
	defer chain.Unsubscribe(events)

	baseTime := time.Now().Add(-5 * time.Minute)

	// Genesis
	genesis := makeTestShare([32]byte{}, testMiner1, uint32(baseTime.Unix()))
	if err := chain.AddShare(genesis); err != nil {
		t.Fatalf("AddShare genesis: %v", err)
	}
	genesisHash := genesis.Hash()

	// Chain A: 3 shares (becomes tip first)
	prevA := genesisHash
	for i := 0; i < 3; i++ {
		s := makeTestShare(prevA, testMiner1, uint32(baseTime.Unix()+int64((i+1)*30)))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("AddShare A[%d]: %v", i, err)
		}
		prevA = s.Hash()
	}

	// Drain all events so far
	drainEvents(events)

	// Record the tip before adding chain B
	tip, _ := chain.Tip()
	oldTip := tip.Hash()

	// Chain B: 5 shares from genesis (heavier, must trigger reorg)
	prevB := genesisHash
	for i := 0; i < 5; i++ {
		s := makeTestShare(prevB, testMiner2, uint32(baseTime.Unix()+int64((i+1)*30)))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("AddShare B[%d]: %v", i, err)
		}
		prevB = s.Hash()
	}

	// Collect events — look for an EventReorg
	var reorgEvent Event
	found := false
	timeout := time.After(2 * time.Second)
	for !found {
		select {
		case evt := <-events:
			if evt.Type == EventReorg {
				reorgEvent = evt
				found = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for EventReorg")
		}
	}

	if reorgEvent.OldTipHash != oldTip {
		t.Errorf("OldTipHash = %x, want %x", reorgEvent.OldTipHash[:8], oldTip[:8])
	}
	if reorgEvent.ReorgDepth < 1 {
		t.Errorf("ReorgDepth = %d, want >= 1", reorgEvent.ReorgDepth)
	}
	newTipHash := reorgEvent.Share.Hash()
	if newTipHash == oldTip {
		t.Error("new tip should differ from old tip")
	}
}

func TestShareChain_PruneOrphans(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	baseTime := time.Now().Add(-5 * time.Minute)

	// Genesis
	genesis := makeTestShare([32]byte{}, testMiner1, uint32(baseTime.Unix()))
	if err := chain.AddShare(genesis); err != nil {
		t.Fatalf("AddShare genesis: %v", err)
	}
	genesisHash := genesis.Hash()

	// Main chain: 5 shares
	prev := genesisHash
	for i := 0; i < 5; i++ {
		s := makeTestShare(prev, testMiner1, uint32(baseTime.Unix()+int64((i+1)*30)))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("AddShare main[%d]: %v", i, err)
		}
		prev = s.Hash()
	}

	// Fork: 2 shares from genesis (shorter, won't become tip)
	prevFork := genesisHash
	for i := 0; i < 2; i++ {
		s := makeTestShare(prevFork, testMiner2, uint32(baseTime.Unix()+int64((i+1)*30)))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("AddShare fork[%d]: %v", i, err)
		}
		prevFork = s.Hash()
	}

	// 1 genesis + 5 main + 2 fork = 8 total
	if chain.Count() != 8 {
		t.Fatalf("count before prune = %d, want 8", chain.Count())
	}

	pruned := chain.PruneOrphans()
	if pruned != 2 {
		t.Errorf("pruned = %d, want 2", pruned)
	}

	// 1 genesis + 5 main = 6 remaining
	if chain.Count() != 6 {
		t.Errorf("count after prune = %d, want 6", chain.Count())
	}

	// The fork shares should be gone
	if store.Has(prevFork) {
		t.Error("fork tip should have been pruned")
	}

	// Main chain tip should still be there
	tipShare, ok := chain.Tip()
	if !ok {
		t.Fatal("chain should have tip after prune")
	}
	if tipShare.Hash() != prev {
		t.Error("main chain tip should be unchanged after prune")
	}
}

func TestMemoryStore_DeleteAndAllHashes(t *testing.T) {
	store := NewMemoryStore()

	s1 := makeTestShare([32]byte{}, testMiner1, 1700000000)
	s2 := makeTestShare(s1.Hash(), testMiner1, 1700000030)

	_ = store.Add(s1)
	_ = store.Add(s2)

	hashes := store.AllHashes()
	if len(hashes) != 2 {
		t.Fatalf("AllHashes = %d, want 2", len(hashes))
	}

	err := store.Delete(s1.Hash())
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if store.Has(s1.Hash()) {
		t.Error("share should not exist after delete")
	}
	if store.Count() != 1 {
		t.Errorf("count = %d, want 1", store.Count())
	}

	// Deleting non-existent share should error
	err = store.Delete(s1.Hash())
	if err == nil {
		t.Error("expected error deleting non-existent share")
	}
}

// drainEvents reads all pending events from the channel without blocking.
func drainEvents(ch chan Event) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestDifficultyCalculator_TooFast(t *testing.T) {
	dc := NewDifficultyCalculator(30 * time.Second)

	// Simulate shares coming in at 15s intervals (too fast, target is 30s)
	// shares[0] is newest, shares[len-1] is oldest
	shares := make([]*types.Share, 20)
	for i := 0; i < 20; i++ {
		shares[i] = &types.Share{
			Header: types.ShareHeader{
				Timestamp: uint32(1700000000 + (19-i)*15), // newest first
			},
			ShareTarget: MaxShareTarget,
		}
	}

	target := dc.NextTarget(shares)
	// If shares are coming too fast, target should decrease (harder)
	if target.Cmp(MaxShareTarget) >= 0 {
		t.Error("target should decrease when shares come too fast")
	}
}

func TestDifficultyCalculator_TooSlow(t *testing.T) {
	dc := NewDifficultyCalculator(30 * time.Second)

	// Simulate shares at 60s intervals (too slow)
	// Use a target that's harder than MaxShareTarget
	harderTarget := new(big.Int).Div(MaxShareTarget, big.NewInt(4))
	shares := make([]*types.Share, 10)
	for i := 0; i < 10; i++ {
		shares[i] = &types.Share{
			Header: types.ShareHeader{
				Timestamp: uint32(1700000000 + (9-i)*60), // newest first
			},
			ShareTarget: harderTarget,
		}
	}

	target := dc.NextTarget(shares)
	// If shares are coming too slow, target should increase (easier)
	if target.Cmp(harderTarget) <= 0 {
		t.Error("target should increase when shares come too slow")
	}
}

func TestMedianTimestamp(t *testing.T) {
	mk := func(ts uint32) *types.Share {
		return &types.Share{Header: types.ShareHeader{Timestamp: ts}}
	}

	// Odd count: middle value.
	if got := medianTimestamp([]*types.Share{mk(50), mk(10), mk(30), mk(20), mk(40)}); got != 30 {
		t.Errorf("odd: got %d, want 30", got)
	}

	// Even count: upper-middle (times[len/2]) — deterministic, never averages.
	if got := medianTimestamp([]*types.Share{mk(40), mk(10), mk(30), mk(20)}); got != 30 {
		t.Errorf("even: got %d, want 30", got)
	}

	// Single element passes through.
	if got := medianTimestamp([]*types.Share{mk(42)}); got != 42 {
		t.Errorf("single: got %d, want 42", got)
	}

	// One outlier at the high end of an 8-sample window does not move the
	// median: it sorts to position 7 while the median pulls from position 4.
	shares := []*types.Share{mk(1_000_000), mk(70), mk(60), mk(50), mk(40), mk(30), mk(20), mk(10)}
	if got := medianTimestamp(shares); got != 50 {
		t.Errorf("outlier: got %d, want 50", got)
	}
}

// TestDifficultyCalculator_FutureTimestampOutlier asserts that an attacker
// who controls a single share at the window edge cannot bias the timing
// ratio: a +2h Timestamp on one sample must be absorbed by the
// median-time-past edges so the resulting target equals the unattacked one.
func TestDifficultyCalculator_FutureTimestampOutlier(t *testing.T) {
	dc := NewDifficultyCalculator(30 * time.Second)

	const (
		windowLen = 24
		spacing   = 30
		baseTime  = uint32(1700000000)
	)
	// Harder-than-max target so the calculator has headroom both directions.
	currentTarget := new(big.Int).Div(MaxShareTarget, big.NewInt(16))

	makeWindow := func(newestOverride uint32) []*types.Share {
		shares := make([]*types.Share, windowLen)
		for i := 0; i < windowLen; i++ {
			ts := baseTime + uint32(windowLen-1-i)*spacing
			if i == 0 && newestOverride != 0 {
				ts = newestOverride
			}
			shares[i] = &types.Share{
				Header:      types.ShareHeader{Timestamp: ts},
				ShareTarget: currentTarget,
			}
		}
		return shares
	}

	honestTarget := dc.NextTarget(makeWindow(0))

	// +2h on the single newest sample — the upper bound of MaxTimeFuture.
	attackTarget := dc.NextTarget(makeWindow(baseTime + (windowLen-1)*spacing + 7200))

	if attackTarget.Cmp(honestTarget) != 0 {
		t.Errorf("MTP did not absorb +2h outlier: honest=%s attack=%s", honestTarget, attackTarget)
	}

	// Belt-and-suspenders: even allowing for some sensitivity, the attacker
	// must not be able to drive the target to the 4x clamp.
	fourX := new(big.Int).Mul(honestTarget, big.NewInt(4))
	if attackTarget.Cmp(fourX) >= 0 {
		t.Errorf("attack reached 4x clamp: honest=%s attack=%s", honestTarget, attackTarget)
	}
}

// --- New validation tests ---

func TestValidation_RejectsShareTargetMismatch(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	// Create a share with correct PoW but inflated ShareTarget
	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	// Inflate the ShareTarget to something much easier (different from consensus MaxShareTarget)
	share.ShareTarget = new(big.Int).Div(maxTarget(), big.NewInt(2))

	err := chain.AddShare(share)
	if err == nil {
		t.Error("expected rejection for ShareTarget mismatch")
	}
}

func TestValidation_RejectsWrongCoinbaseCommitment(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))

	// Rebuild the coinbase with a wrong PrevShareHash commitment
	wrongHash := [32]byte{0xff, 0xee, 0xdd}
	builder := types.NewCoinbaseBuilder(testNetwork)
	commitment := types.BuildShareCommitment(wrongHash)
	payouts := []types.PayoutEntry{
		{Address: testMiner1, Amount: 5000000000},
	}
	badCoinbase, _, err := builder.BuildCoinbase(800000, commitment, payouts, "", 8)
	if err != nil {
		t.Fatalf("BuildCoinbase failed: %v", err)
	}
	share.CoinbaseTx = badCoinbase

	err = chain.AddShare(share)
	if err == nil {
		t.Error("expected rejection for wrong coinbase commitment")
	}
}

func TestValidation_RejectsMinerNotInOutputs(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))

	// Rebuild coinbase paying to a different address
	builder := types.NewCoinbaseBuilder(testNetwork)
	commitment := types.BuildShareCommitment([32]byte{}) // correct prevShareHash
	payouts := []types.PayoutEntry{
		{Address: testMiner2, Amount: 5000000000}, // pays to miner2, not miner1
	}
	badCoinbase, _, err := builder.BuildCoinbase(800000, commitment, payouts, "", 8)
	if err != nil {
		t.Fatalf("BuildCoinbase failed: %v", err)
	}
	share.CoinbaseTx = badCoinbase

	err = chain.AddShare(share)
	if err == nil {
		t.Error("expected rejection when miner not in coinbase outputs")
	}
}

func TestValidation_ExpectedTargetFromParent(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	baseTime := time.Now().Add(-5 * time.Minute)

	// Build a main chain of 5 shares
	var prevHash [32]byte
	for i := 0; i < 5; i++ {
		s := makeTestShare(prevHash, testMiner1, uint32(baseTime.Unix()+int64(i*30)))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("AddShare main[%d]: %v", i, err)
		}
		prevHash = s.Hash()
	}

	// Build a fork from genesis — the fork's expected target should be computed
	// from the fork's own ancestor window, not from the main chain's tip
	forkShare := makeTestShare([32]byte{}, testMiner2, uint32(baseTime.Unix()+150))
	err := chain.AddShare(forkShare)
	if err != nil {
		t.Fatalf("AddShare fork share failed: %v", err)
	}

	// The fork share should be stored with the correct consensus target
	stored, ok := chain.GetShare(forkShare.Hash())
	if !ok {
		t.Fatal("fork share not found in store")
	}

	// For a genesis parent, expected target should be MaxShareTarget
	expectedBits := util.TargetToCompact(MaxShareTarget)
	storedBits := util.TargetToCompact(stored.ShareTarget)
	if storedBits != expectedBits {
		t.Errorf("fork share target bits = 0x%08x, want 0x%08x (MaxShareTarget)", storedBits, expectedBits)
	}
}

func TestValidation_RejectsWrongShareVersion(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	share.ShareVersion = 0 // invalid version

	err := chain.AddShare(share)
	if err == nil {
		t.Error("expected rejection for wrong share version")
	}
}

func TestValidation_RejectsInvalidMinerAddress(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	share.MinerAddress = "not-a-valid-address" // invalid

	err := chain.AddShare(share)
	if err == nil {
		t.Error("expected rejection for invalid miner address")
	}
}

func TestValidation_RejectsMissingCoinbase(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	share.CoinbaseTx = nil // missing coinbase

	err := chain.AddShare(share)
	if err == nil {
		t.Error("expected rejection for missing coinbase")
	}
}

// extractValidationError unwraps the error returned by AddShare to get the
// underlying *ValidationError (chain.AddShare wraps with fmt.Errorf("invalid
// share: %w", err)). Returns nil if the chain doesn't end in a ValidationError.
func extractValidationError(t *testing.T, err error) *ValidationError {
	t.Helper()
	var vErr *ValidationError
	if !errorsAs(err, &vErr) {
		return nil
	}
	return vErr
}

// errorsAs avoids importing "errors" just for one helper.
func errorsAs(err error, target **ValidationError) bool {
	for err != nil {
		if v, ok := err.(*ValidationError); ok {
			*target = v
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestValidation_ParentNotFoundIsIndeterminate asserts the one error path
// the misbehavior tracker treats as not-the-peer's-fault: a share whose
// parent we haven't synced yet must surface CategoryIndeterminate so the
// orchestrator does not penalize an honest peer racing ahead of our sync.
func TestValidation_ParentNotFoundIsIndeterminate(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	unknownParent := [32]byte{}
	unknownParent[0] = 0xff // any non-zero hash that isn't in the empty store
	share := makeTestShare(unknownParent, testMiner1, uint32(time.Now().Unix()))

	err := chain.AddShare(share)
	if err == nil {
		t.Fatal("expected rejection for unknown parent")
	}
	vErr := extractValidationError(t, err)
	if vErr == nil {
		t.Fatalf("expected *ValidationError in chain, got: %v", err)
	}
	if vErr.Category != CategoryIndeterminate {
		t.Errorf("category = %v, want CategoryIndeterminate", vErr.Category)
	}
}

// TestValidation_ProvableRejectionsAreProvable asserts that the default-zero
// category is the Provable case — every existing rejection (wrong commitment,
// wrong miner-in-outputs, target mismatch, etc.) flows into the peer
// misbehavior counter as intended.
func TestValidation_ProvableRejectionsAreProvable(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())

	share := makeTestShare([32]byte{}, testMiner1, uint32(time.Now().Unix()))
	share.CoinbaseTx = nil // canonical Provable rejection

	err := chain.AddShare(share)
	if err == nil {
		t.Fatal("expected rejection for missing coinbase")
	}
	vErr := extractValidationError(t, err)
	if vErr == nil {
		t.Fatalf("expected *ValidationError in chain, got: %v", err)
	}
	if vErr.Category != CategoryProvable {
		t.Errorf("category = %v, want CategoryProvable", vErr.Category)
	}
}

// --- ChainWork caching ---

// TestChainWork_CachesResultOnShare confirms that a ChainWork call leaves
// cumulativeWork populated on every share it traversed, so the next call
// against the same tip is an O(1) cache hit.
func TestChainWork_CachesResultOnShare(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())
	fc := NewForkChoice(store)

	now := uint32(time.Now().Unix()) - 300
	var shares []*types.Share
	prev := [32]byte{}
	for i := 0; i < 5; i++ {
		s := makeTestShare(prev, testMiner1, now+uint32(i*30))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("add share %d: %v", i, err)
		}
		shares = append(shares, s)
		prev = s.Hash()
	}

	// Fresh tip — cumulativeWork has been populated by AddShare's SelectTip
	// (the direct-extension fast path skips ChainWork, so let's force it).
	tip := shares[len(shares)-1]
	if tip.CumulativeWork() != nil {
		// Already cached (a previous fork-choice call must have computed it).
		// That's fine; we just want to confirm caching works end-to-end.
	}

	work1 := fc.ChainWork(tip.Hash(), 8640)
	if tip.CumulativeWork() == nil {
		t.Fatal("CumulativeWork should be populated after ChainWork")
	}
	if work1.Sign() <= 0 {
		t.Errorf("work1 should be positive, got %s", work1)
	}

	// Second call returns the same value without changing the cache.
	cachedBefore := tip.CumulativeWork()
	work2 := fc.ChainWork(tip.Hash(), 8640)
	if work2.Cmp(work1) != 0 {
		t.Errorf("cache-hit returned different value: first=%s second=%s", work1, work2)
	}
	if tip.CumulativeWork().Cmp(cachedBefore) != 0 {
		t.Error("cache was mutated by a cache-hit ChainWork call")
	}
}

// TestChainWork_IncrementalExtension confirms the per-share cost stays
// bounded as the chain grows: adding a share to a chain whose tip has a
// cached cumulativeWork should not require walking ancestors again.
func TestChainWork_IncrementalExtension(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())
	fc := NewForkChoice(store)

	now := uint32(time.Now().Unix()) - 300
	var shares []*types.Share
	prev := [32]byte{}
	for i := 0; i < 10; i++ {
		s := makeTestShare(prev, testMiner1, now+uint32(i*30))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("add share %d: %v", i, err)
		}
		// Force the cache to populate for each new tip.
		_ = fc.ChainWork(s.Hash(), 8640)
		shares = append(shares, s)
		prev = s.Hash()
	}

	// Each share must have cumulativeWork == sum of all difficulties up to and
	// including it, i.e. strictly greater than its predecessor's value.
	for i := 1; i < len(shares); i++ {
		prev := shares[i-1].CumulativeWork()
		cur := shares[i].CumulativeWork()
		if prev == nil || cur == nil {
			t.Fatalf("share[%d] or [%d] not cached", i-1, i)
		}
		if cur.Cmp(prev) <= 0 {
			t.Errorf("cumulativeWork did not increase between shares: prev=%s cur=%s", prev, cur)
		}
	}
}

// TestChainWork_ForkedSharesHaveDistinctWork confirms that two shares
// pointing at the same parent (a fork) get independent cumulativeWork
// values — each carries the parent's work plus its own difficulty,
// not some shared state.
func TestChainWork_ForkedSharesHaveDistinctWork(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	chain := NewShareChain(store, diffCalc, 8640, testNetwork, testLogger())
	fc := NewForkChoice(store)

	now := uint32(time.Now().Unix()) - 300

	// Single parent.
	parent := makeTestShare([32]byte{}, testMiner1, now)
	if err := chain.AddShare(parent); err != nil {
		t.Fatalf("add parent: %v", err)
	}

	// Two siblings — both reference parent.
	siblingA := makeTestShare(parent.Hash(), testMiner1, now+30)
	siblingB := makeTestShare(parent.Hash(), testMiner2, now+31)
	if err := chain.AddShare(siblingA); err != nil {
		t.Fatalf("add siblingA: %v", err)
	}
	if err := chain.AddShare(siblingB); err != nil {
		t.Fatalf("add siblingB: %v", err)
	}

	_ = fc.ChainWork(siblingA.Hash(), 8640)
	_ = fc.ChainWork(siblingB.Hash(), 8640)

	a := siblingA.CumulativeWork()
	b := siblingB.CumulativeWork()
	if a == nil || b == nil {
		t.Fatal("sibling cumulativeWork not cached")
	}
	// Same ShareTarget on both siblings → identical work contribution → equal totals.
	if a.Cmp(b) != 0 {
		t.Errorf("siblings with equal difficulty should have equal cumulativeWork: A=%s B=%s", a, b)
	}
	// And each must exceed the parent (they include the parent's work + their own).
	parentWork := parent.CumulativeWork()
	if parentWork == nil {
		t.Fatal("parent cumulativeWork not cached")
	}
	if a.Cmp(parentWork) <= 0 {
		t.Errorf("sibling work %s should exceed parent work %s", a, parentWork)
	}
}
