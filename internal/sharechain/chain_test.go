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

func TestShareChain_PruneOldShares(t *testing.T) {
	store := NewMemoryStore()
	diffCalc := NewDifficultyCalculator(30 * time.Second)
	const windowSize = 25
	// Creates an empty chain
	chain := NewShareChain(store, diffCalc, windowSize, testNetwork, testLogger())

	//The chain will be populated with windowSize*2 + 10 shares
	// This will mimic a real scenario where the chain removes the older 5 minutes of shares,
	// and keeps a length of windowSize*2 shares
	chainLen := windowSize*2 + 10
	// Add shares with timestamps 30s apart
	baseTime := time.Now().Add(-time.Duration(chainLen) * 30 * time.Second)

	// Genesis
	genesis := makeTestShare([32]byte{}, testMiner1, uint32(baseTime.Unix()))
	if err := chain.AddShare(genesis); err != nil {
		t.Fatalf("AddShare genesis: %v", err)
	}
	genesisHash := genesis.Hash()

	prev := genesisHash
	for i := 0; i < chainLen-1; i++ {
		s := makeTestShare(prev, testMiner1, uint32(baseTime.Unix()+int64((i+1)*30)))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("AddShare main[%d]: %v", i, err)
		}
		prev = s.Hash()
	}

	//remove the last 5 minutes of shares (10 shares at 30s intervals)
	prunedShares := chain.PruneToDepth(chainLen - 10)

	if prunedShares != 10 {
		t.Errorf("pruned shares = %d, want 10", prunedShares)
	}

	if chain.Count() != chainLen-10 {
		t.Errorf("count after prune = %d, want %d", chain.Count(), chainLen-10)
	}

	// The tip should still be the same after pruning
	postTip, ok := chain.Tip()
	if !ok {
		t.Fatal("chain should have tip after prune")
	}
	if postTip.Hash() != prev {
		t.Error("tip should be unchanged after prune")
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
