package node

import (
	"math/big"
	"testing"
	"time"

	"github.com/djkazic/p2pool-go/internal/p2p"
	"github.com/djkazic/p2pool-go/internal/sharechain"
	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/pkg/util"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

const (
	testNetwork = "testnet3"
	testMiner1  = "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx"
	testMiner2  = "tb1qrp33g0q5b5698ahp5jnf5yzjmgcea8e0gfk2ts"
)

// makeTestShare creates a share that passes validation.
// Mirrors the sharechain test helper.
func makeTestShare(prevShareHash [32]byte, minerAddr string, timestamp uint32) *types.Share {
	target := util.CompactToTarget(0x207fffff)

	builder := types.NewCoinbaseBuilder(testNetwork)
	commitment := types.BuildShareCommitment(prevShareHash)
	payouts := []types.PayoutEntry{
		{Address: minerAddr, Amount: 5000000000},
	}
	coinbaseTx, _, err := builder.BuildCoinbase(800000, commitment, payouts, "", 8)
	if err != nil {
		panic("makeTestShare: BuildCoinbase failed: " + err.Error())
	}

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
	for nonce := uint32(0); ; nonce++ {
		s.Header.Nonce = nonce
		hash := s.Header.Hash()
		if util.HashMeetsTarget(hash, target) {
			return s
		}
	}
}

// testNode creates a minimal Node with just a sharechain for handler tests.
func testNode(t *testing.T) (*Node, []*types.Share) {
	t.Helper()
	logger, _ := zap.NewDevelopment()

	store := sharechain.NewMemoryStore()
	diffCalc := sharechain.NewDifficultyCalculator(30 * time.Second)
	chain := sharechain.NewShareChain(store, diffCalc, 8640, testNetwork, logger)

	// Build a chain of 10 shares
	now := uint32(time.Now().Unix()) - 300
	var shares []*types.Share
	prevHash := [32]byte{}
	for i := 0; i < 10; i++ {
		s := makeTestShare(prevHash, testMiner1, now+uint32(i*30))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("add share %d: %v", i, err)
		}
		shares = append(shares, s)
		prevHash = s.Hash()
	}

	n := &Node{
		logger: logger,
		chain:  chain,
	}
	return n, shares
}

// --- handleInvRequest tests ---

func TestHandleInvRequest_EmptyChain(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	store := sharechain.NewMemoryStore()
	diffCalc := sharechain.NewDifficultyCalculator(30 * time.Second)
	chain := sharechain.NewShareChain(store, diffCalc, 8640, testNetwork, logger)

	n := &Node{logger: logger, chain: chain}
	resp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		MaxCount: 10000,
	})

	if len(resp.Hashes) != 0 {
		t.Errorf("expected 0 hashes for empty chain, got %d", len(resp.Hashes))
	}
}

func TestHandleInvRequest_NoLocators(t *testing.T) {
	n, shares := testNode(t)

	// No locators → return all share hashes
	resp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		MaxCount: 10000,
	})

	if len(resp.Hashes) != len(shares) {
		t.Fatalf("expected %d hashes, got %d", len(shares), len(resp.Hashes))
	}

	// Verify oldest-first order
	for i, h := range resp.Hashes {
		expected := shares[i].Hash()
		if h != expected {
			t.Errorf("hash[%d] mismatch: got %x, want %x", i, h[:4], expected[:4])
		}
	}
}

func TestHandleInvRequest_LocatorAtForkPoint(t *testing.T) {
	n, shares := testNode(t)

	// Locator at share[4] → should return shares[5..9]
	locators := [][32]byte{shares[4].Hash()}
	resp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		Locators: locators,
		MaxCount: 10000,
	})

	expected := shares[5:]
	if len(resp.Hashes) != len(expected) {
		t.Fatalf("expected %d hashes, got %d", len(expected), len(resp.Hashes))
	}

	for i, h := range resp.Hashes {
		if h != expected[i].Hash() {
			t.Errorf("hash[%d] mismatch", i)
		}
	}
}

func TestHandleInvRequest_LocatorAtTip(t *testing.T) {
	n, shares := testNode(t)

	// Locator at tip → peer is already in sync, return nothing
	locators := [][32]byte{shares[len(shares)-1].Hash()}
	resp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		Locators: locators,
		MaxCount: 10000,
	})

	if len(resp.Hashes) != 0 {
		t.Errorf("expected 0 hashes when locator is at tip, got %d", len(resp.Hashes))
	}
}

func TestHandleInvRequest_LocatorUnknown(t *testing.T) {
	n, shares := testNode(t)

	// Unknown locator → treated same as no locators, return all
	locators := [][32]byte{{0xff, 0xfe, 0xfd}}
	resp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		Locators: locators,
		MaxCount: 10000,
	})

	if len(resp.Hashes) != len(shares) {
		t.Fatalf("expected %d hashes for unknown locator, got %d", len(shares), len(resp.Hashes))
	}
}

func TestHandleInvRequest_MaxCountTruncation(t *testing.T) {
	n, _ := testNode(t) // 10 shares

	resp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		MaxCount: 3,
	})

	if len(resp.Hashes) != 3 {
		t.Fatalf("expected 3 hashes, got %d", len(resp.Hashes))
	}
	if !resp.More {
		t.Error("expected More=true when truncated")
	}
}

func TestHandleInvRequest_MultipleLocators(t *testing.T) {
	n, shares := testNode(t)

	// Send [unknown, shares[7], shares[3]] — should match shares[7] first
	locators := [][32]byte{
		{0xaa, 0xbb},      // unknown
		shares[7].Hash(),   // match this one
		shares[3].Hash(),   // ignored (first match wins)
	}
	resp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		Locators: locators,
		MaxCount: 10000,
	})

	// shares[7] is the fork point → return shares[8..9]
	if len(resp.Hashes) != 2 {
		t.Fatalf("expected 2 hashes, got %d", len(resp.Hashes))
	}
	if resp.Hashes[0] != shares[8].Hash() {
		t.Errorf("hash[0] should be shares[8]")
	}
	if resp.Hashes[1] != shares[9].Hash() {
		t.Errorf("hash[1] should be shares[9]")
	}
}

// --- handleDataRequest tests ---

func TestHandleDataRequest_KnownHashes(t *testing.T) {
	n, shares := testNode(t)

	hashes := [][32]byte{shares[2].Hash(), shares[5].Hash(), shares[8].Hash()}
	resp := n.handleDataRequest(&p2p.DataReq{
		Type:   p2p.MsgTypeDataReq,
		Hashes: hashes,
	})

	if len(resp.Shares) != 3 {
		t.Fatalf("expected 3 shares, got %d", len(resp.Shares))
	}

	// Verify the returned shares match the requested hashes
	for i, msg := range resp.Shares {
		share := p2pShareToShare(&msg)
		if share.Hash() != hashes[i] {
			t.Errorf("share[%d] hash mismatch", i)
		}
	}
}

func TestHandleDataRequest_UnknownHashes(t *testing.T) {
	n, shares := testNode(t)

	hashes := [][32]byte{
		shares[0].Hash(),
		{0xde, 0xad}, // unknown
		shares[1].Hash(),
	}
	resp := n.handleDataRequest(&p2p.DataReq{
		Type:   p2p.MsgTypeDataReq,
		Hashes: hashes,
	})

	// Unknown hash is skipped, so only 2 shares returned
	if len(resp.Shares) != 2 {
		t.Fatalf("expected 2 shares (unknown skipped), got %d", len(resp.Shares))
	}
}

func TestHandleDataRequest_Empty(t *testing.T) {
	n, _ := testNode(t)

	resp := n.handleDataRequest(&p2p.DataReq{
		Type: p2p.MsgTypeDataReq,
	})

	if len(resp.Shares) != 0 {
		t.Errorf("expected 0 shares for empty request, got %d", len(resp.Shares))
	}
}

// --- buildLocator tests ---

func TestBuildLocator_EmptyChain(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	store := sharechain.NewMemoryStore()
	diffCalc := sharechain.NewDifficultyCalculator(30 * time.Second)
	chain := sharechain.NewShareChain(store, diffCalc, 8640, testNetwork, logger)

	n := &Node{logger: logger, chain: chain}
	locators := n.buildLocator()

	if locators != nil {
		t.Errorf("expected nil locators for empty chain, got %d", len(locators))
	}
}

func TestBuildLocator_StartsAtTip(t *testing.T) {
	n, shares := testNode(t)

	locators := n.buildLocator()

	if len(locators) == 0 {
		t.Fatal("expected non-empty locators")
	}

	// First locator should be the tip
	tip := shares[len(shares)-1].Hash()
	if locators[0] != tip {
		t.Errorf("first locator should be tip hash")
	}

	// Last locator should be genesis
	genesis := shares[0].Hash()
	if locators[len(locators)-1] != genesis {
		t.Errorf("last locator should be genesis hash")
	}
}

func TestBuildLocator_ExponentialSpacing(t *testing.T) {
	// Build a longer chain to test exponential spacing
	logger, _ := zap.NewDevelopment()
	store := sharechain.NewMemoryStore()
	diffCalc := sharechain.NewDifficultyCalculator(30 * time.Second)
	chain := sharechain.NewShareChain(store, diffCalc, 8640, testNetwork, logger)

	now := uint32(time.Now().Unix()) - 3000
	var shares []*types.Share
	prevHash := [32]byte{}
	for i := 0; i < 50; i++ {
		s := makeTestShare(prevHash, testMiner1, now+uint32(i*30))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("add share %d: %v", i, err)
		}
		shares = append(shares, s)
		prevHash = s.Hash()
	}

	n := &Node{logger: logger, chain: chain}
	locators := n.buildLocator()

	// First 10 locators should be consecutive (step=1)
	for i := 0; i < 10 && i < len(locators); i++ {
		expected := shares[len(shares)-1-i].Hash()
		if locators[i] != expected {
			t.Errorf("locator[%d] should be share at index %d", i, len(shares)-1-i)
		}
	}

	// After 10, steps should double (exponential)
	if len(locators) <= 10 {
		t.Fatalf("expected more than 10 locators for 50-share chain, got %d", len(locators))
	}

	// Verify locators are all unique
	seen := make(map[[32]byte]bool)
	for _, loc := range locators {
		if seen[loc] {
			t.Error("duplicate locator hash")
		}
		seen[loc] = true
	}
}

// --- shareToP2PMsg / p2pShareToShare round-trip ---

func TestShareConversion_RoundTrip(t *testing.T) {
	share := makeTestShare([32]byte{}, testMiner1, 1700000000)

	msg := shareToP2PMsg(share)
	back := p2pShareToShare(msg)

	if back.Hash() != share.Hash() {
		t.Error("hash mismatch after round-trip")
	}
	if back.MinerAddress != share.MinerAddress {
		t.Errorf("miner mismatch: got %q, want %q", back.MinerAddress, share.MinerAddress)
	}
	if back.PrevShareHash != share.PrevShareHash {
		t.Error("PrevShareHash mismatch")
	}
}

// --- Per-peer misbehavior tracking ---

func newScoredNode(t *testing.T) *Node {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	return &Node{
		logger:     logger,
		peerScores: make(map[peer.ID]*peerScore),
	}
}

func TestRecordPeerOutcome_BelowMinBadDoesNotTrip(t *testing.T) {
	n := newScoredNode(t)
	id := peer.ID("test-peer-1")
	for i := 0; i < peerScoreMinBad-1; i++ {
		if n.recordPeerOutcome(id, true) {
			t.Fatalf("disconnect tripped at bad=%d, minimum is %d", i+1, peerScoreMinBad)
		}
	}
}

func TestRecordPeerOutcome_ThresholdTripsAtMinBad(t *testing.T) {
	n := newScoredNode(t)
	id := peer.ID("test-peer-1")
	for i := 0; i < peerScoreMinBad-1; i++ {
		n.recordPeerOutcome(id, true)
	}
	if !n.recordPeerOutcome(id, true) {
		t.Fatalf("expected disconnect at bad=%d", peerScoreMinBad)
	}
	// Once disconnected, subsequent calls don't re-trip.
	if n.recordPeerOutcome(id, true) {
		t.Error("disconnect tripped twice for the same peer")
	}
}

func TestRecordPeerOutcome_GoodSharesGateRatio(t *testing.T) {
	// 5 bad mixed into 100 good: bad/(good+bad) = 5/105 ≈ 4.8%, below
	// the 20% ratio gate. An otherwise-cooperative peer who occasionally
	// races during sync must not get banned.
	n := newScoredNode(t)
	id := peer.ID("test-peer-1")
	for i := 0; i < 100; i++ {
		if n.recordPeerOutcome(id, false) {
			t.Fatal("good share should never trip disconnect")
		}
	}
	for i := 0; i < peerScoreMinBad; i++ {
		if n.recordPeerOutcome(id, true) {
			t.Fatalf("disconnect tripped despite %d good shares against %d bad",
				100, i+1)
		}
	}
}

func TestRecordPeerOutcome_PerPeerIndependence(t *testing.T) {
	n := newScoredNode(t)
	bad := peer.ID("peer-bad")
	ok := peer.ID("peer-ok")
	for i := 0; i < peerScoreMinBad-1; i++ {
		n.recordPeerOutcome(bad, true)
		n.recordPeerOutcome(ok, false)
	}
	if n.recordPeerOutcome(ok, false) {
		t.Error("good peer disconnected")
	}
	if !n.recordPeerOutcome(bad, true) {
		t.Error("bad peer not disconnected at threshold")
	}
}

// TestHandleP2PShare_IndeterminateDoesNotPenalize confirms the end-to-end
// invariant: a share whose parent is unknown (the canonical sync race)
// reaches AddShare, is rejected with CategoryIndeterminate, and the
// sender's bad counter stays at zero.
func TestHandleP2PShare_IndeterminateDoesNotPenalize(t *testing.T) {
	n, _ := testNode(t)
	n.peerScores = make(map[peer.ID]*peerScore)

	unknownParent := [32]byte{}
	unknownParent[0] = 0xff
	share := makeTestShare(unknownParent, testMiner1, uint32(time.Now().Unix()))
	msg := shareToP2PMsg(share)
	from := peer.ID("test-peer-1")

	for i := 0; i < peerScoreMinBad+10; i++ {
		n.handleP2PShare(&p2p.IncomingShare{Share: msg, From: from})
	}

	// Indeterminate failures intentionally do not create a peer-score
	// entry; if one happens to exist (e.g. from an earlier success in a
	// larger test), it must show zero bad shares and not be disconnected.
	n.peerScoresMu.Lock()
	defer n.peerScoresMu.Unlock()
	if ps := n.peerScores[from]; ps != nil {
		if ps.bad != 0 {
			t.Errorf("bad counter = %d after %d indeterminate failures; want 0",
				ps.bad, peerScoreMinBad+10)
		}
		if ps.disconnected {
			t.Error("peer was disconnected for indeterminate failures")
		}
	}
}

// --- Gossipsub topic validator ---

func TestValidateGossipShare_AcceptsHonest(t *testing.T) {
	share := makeTestShare([32]byte{}, testMiner1, 1700000000)
	msg := shareToP2PMsg(share)
	if !validateGossipShare(msg) {
		t.Error("expected accept for honest share that passes its declared target")
	}
}

func TestValidateGossipShare_RejectsBadPoW(t *testing.T) {
	// Declare Bitcoin difficulty 1 (target ≈ 2^208). With any random Nonce
	// and otherwise-zero header, the hash misses this target with overwhelming
	// probability (~1 - 2^-48), so the cheap check must reject.
	msg := &p2p.ShareMsg{
		Type:            p2p.MsgTypeShare,
		Version:         1,
		ShareVersion:    1,
		MinerAddress:    testMiner1,
		ShareTargetBits: 0x1d00ffff,
		Nonce:           0xdeadbeef,
	}
	if validateGossipShare(msg) {
		t.Error("expected reject for share whose hash does not meet declared target")
	}
}

func TestValidateGossipShare_RejectsDeclaredAboveMax(t *testing.T) {
	// 0x217fffff decodes to a target 256× larger than MaxShareTarget, well
	// outside the protocol's allowed easiest difficulty. Any share claiming
	// such an easy target is bogus regardless of the PoW it carries.
	msg := &p2p.ShareMsg{
		Type:            p2p.MsgTypeShare,
		ShareTargetBits: 0x217fffff,
	}
	if validateGossipShare(msg) {
		t.Error("expected reject for declared target above MaxShareTarget")
	}
}

func TestValidateGossipShare_RejectsZeroTarget(t *testing.T) {
	// ShareTargetBits = 0 decodes to target = 0; no hash can meet it.
	// The cheap check must reject without dividing by zero or hashing.
	msg := &p2p.ShareMsg{
		Type:            p2p.MsgTypeShare,
		ShareTargetBits: 0,
	}
	if validateGossipShare(msg) {
		t.Error("expected reject for zero declared target")
	}
}

// --- InvRequest → DataRequest integration ---

func TestInvThenData_FullSync(t *testing.T) {
	// Simulate a full sync: inv discovery → data download → ordered insertion
	n, shares := testNode(t)

	// Phase 1: Inv request with no locators (fresh node)
	invResp := n.handleInvRequest(&p2p.InvReq{
		Type:     p2p.MsgTypeInvReq,
		MaxCount: 10000,
	})

	if len(invResp.Hashes) != len(shares) {
		t.Fatalf("inv: expected %d hashes, got %d", len(shares), len(invResp.Hashes))
	}

	// Phase 2: Data request for all hashes
	dataResp := n.handleDataRequest(&p2p.DataReq{
		Type:   p2p.MsgTypeDataReq,
		Hashes: invResp.Hashes,
	})

	if len(dataResp.Shares) != len(shares) {
		t.Fatalf("data: expected %d shares, got %d", len(shares), len(dataResp.Shares))
	}

	// Phase 3: Add to a fresh chain in order — all should succeed
	logger, _ := zap.NewDevelopment()
	freshStore := sharechain.NewMemoryStore()
	freshDiffCalc := sharechain.NewDifficultyCalculator(30 * time.Second)
	freshChain := sharechain.NewShareChain(freshStore, freshDiffCalc, 8640, testNetwork, logger)

	added := 0
	for _, msg := range dataResp.Shares {
		s := p2pShareToShare(&msg)
		if err := freshChain.AddShareQuiet(s); err != nil {
			t.Errorf("add share failed: %v", err)
			continue
		}
		added++
	}

	if added != len(shares) {
		t.Errorf("expected %d shares added, got %d", len(shares), added)
	}
	if freshChain.Count() != len(shares) {
		t.Errorf("fresh chain has %d shares, expected %d", freshChain.Count(), len(shares))
	}
}

// --- Pure function tests ---

func TestApplyVersionRolling(t *testing.T) {
	// Base version 0x20000000 with rolled bits 0x00004000 (within mask)
	result := applyVersionRolling("20000000", "00004000")
	if result != "20004000" {
		t.Errorf("expected 20004000, got %s", result)
	}

	// Bits outside the mask should be ignored
	result = applyVersionRolling("20000000", "e0001fff")
	// mask = 0x1fffe000; rolled & mask = 0x00000000; orig &^ mask = 0x20000000
	if result != "20000000" {
		t.Errorf("expected 20000000, got %s", result)
	}

	// Preserve non-mask bits from original
	result = applyVersionRolling("20800000", "1fffe000")
	// mask = 0x1fffe000; rolled & mask = 0x1fffe000; orig &^ mask = 0x20800000
	if result != "3fffe000" {
		t.Errorf("expected 3fffe000, got %s", result)
	}
}

func TestStratumDiffToTarget(t *testing.T) {
	// Difficulty 1 should return the diff1 target
	target1 := stratumDiffToTarget(1.0)
	if target1.Cmp(stratumDiff1Target) != 0 {
		t.Errorf("diff 1 target mismatch")
	}

	// Difficulty 2 should be half of diff1
	target2 := stratumDiffToTarget(2.0)
	expected2 := new(big.Int).Div(stratumDiff1Target, big.NewInt(2))
	if target2.Cmp(expected2) != 0 {
		t.Errorf("diff 2: got %v, want %v", target2, expected2)
	}

	// Zero/negative should return diff1 target
	targetZero := stratumDiffToTarget(0)
	if targetZero.Cmp(stratumDiff1Target) != 0 {
		t.Error("diff 0 should return diff1 target")
	}
	targetNeg := stratumDiffToTarget(-1)
	if targetNeg.Cmp(stratumDiff1Target) != 0 {
		t.Error("diff -1 should return diff1 target")
	}
}

func TestPoolHashrateFromShares(t *testing.T) {
	// Empty or single share → 0
	if poolHashrateFromShares(nil) != 0 {
		t.Error("nil shares should return 0")
	}
	if poolHashrateFromShares([]*types.Share{{}}) != 0 {
		t.Error("single share should return 0")
	}

	// Two shares 30 seconds apart with known target
	target := util.CompactToTarget(0x207fffff)
	now := uint32(time.Now().Unix())
	shares := []*types.Share{
		{Header: types.ShareHeader{Timestamp: now}, ShareTarget: target},
		{Header: types.ShareHeader{Timestamp: now - 30}, ShareTarget: target},
	}
	hr := poolHashrateFromShares(shares)
	if hr <= 0 {
		t.Errorf("expected positive hashrate, got %f", hr)
	}

	// Same timestamp → 0 (division by zero guard)
	sameTime := []*types.Share{
		{Header: types.ShareHeader{Timestamp: now}, ShareTarget: target},
		{Header: types.ShareHeader{Timestamp: now}, ShareTarget: target},
	}
	if poolHashrateFromShares(sameTime) != 0 {
		t.Error("same timestamp should return 0")
	}
}

func TestLocalHashrate(t *testing.T) {
	n := &Node{}

	// No shares → 0
	if n.localHashrate() != 0 {
		t.Error("expected 0 with no shares")
	}

	// One share → 0 (need at least 2)
	n.recordLocalShare(1.0, "test")
	if n.localHashrate() != 0 {
		t.Error("expected 0 with only 1 share")
	}

	// Two shares close together → 0 (below minElapsed of 30s)
	time.Sleep(time.Millisecond)
	n.recordLocalShare(1.0, "test")
	if n.localHashrate() != 0 {
		t.Error("expected 0 when elapsed < minElapsed")
	}

	// Simulate a mature window: backdate the first share so elapsed > 30s
	n.localSharesMu.Lock()
	n.localShares[0].time = time.Now().Add(-45 * time.Second)
	n.localSharesMu.Unlock()

	hr := n.localHashrate()
	if hr <= 0 {
		t.Errorf("expected positive hashrate with mature window, got %f", hr)
	}
}

func TestGetPrevShareHash(t *testing.T) {
	n, shares := testNode(t)

	prevHash := n.getPrevShareHash()
	expectedTip := shares[len(shares)-1].Hash()
	if prevHash != expectedTip {
		t.Error("getPrevShareHash should return tip hash")
	}

	// Empty chain → zero hash
	logger, _ := zap.NewDevelopment()
	emptyStore := sharechain.NewMemoryStore()
	emptyDiffCalc := sharechain.NewDifficultyCalculator(30 * time.Second)
	emptyChain := sharechain.NewShareChain(emptyStore, emptyDiffCalc, 8640, testNetwork, logger)
	emptyNode := &Node{logger: logger, chain: emptyChain}
	if emptyNode.getPrevShareHash() != ([32]byte{}) {
		t.Error("empty chain should return zero hash")
	}
}

func TestShareTarget(t *testing.T) {
	n, _ := testNode(t)

	target := n.ShareTarget()
	if target == nil || target.Sign() <= 0 {
		t.Error("ShareTarget should return a positive value")
	}
}
