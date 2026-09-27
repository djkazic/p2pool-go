// Regression tests for the findings recorded in specs/README.md, each one
// reproducing against the real implementation what a TLA+ counterexample
// showed at the model level:
//
//	6  a node could not join a pool whose peers had pruned, and a node
//	   offline longer than the window could not re-anchor
//	7  a timestamp rejection, which depends on the validating node's own
//	   clock, was charged to the relaying peer as provable misbehavior
//	8  a node at its prune horizon rejected shares the rest of the network
//	   accepts, because it computed the consensus target from a window its
//	   peers did not share
package sharechain

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/pkg/util"
)

// rebuildShare returns a fresh Share value carrying the same consensus data,
// so the receiving node does not inherit the sender's cached hash or work.
func rebuildShare(s *types.Share) *types.Share {
	return &types.Share{
		Header:          s.Header,
		ShareVersion:    s.ShareVersion,
		PrevShareHash:   s.PrevShareHash,
		ShareTarget:     new(big.Int).Set(s.ShareTarget),
		MinerAddress:    s.MinerAddress,
		CoinbaseTx:      append([]byte(nil), s.CoinbaseTx...),
		ShareChainNonce: s.ShareChainNonce,
	}
}

func newChain(keep int) *ShareChain {
	return NewShareChain(NewMemoryStore(), NewDifficultyCalculator(30*time.Second),
		keep, testNetwork, testLogger())
}

// mainChainOldestFirst is what node.go handleInvRequest/handleDataRequest
// serve to a peer with no locators: the whole main chain, oldest first.
func mainChainOldestFirst(c *ShareChain) []*types.Share {
	tip, ok := c.Tip()
	if !ok {
		return nil
	}
	anc := c.GetAncestors(tip.Hash(), c.Count())
	out := make([]*types.Share, 0, len(anc))
	for i := len(anc) - 1; i >= 0; i-- {
		out = append(out, anc[i])
	}
	return out
}

// syncInto mirrors the add loop in node.go syncFromAllPeers: the oldest share
// of the batch goes in via AddShareAsRoot, the rest via AddShareQuiet.
func syncInto(c *ShareChain, served []*types.Share) (added int, firstErr error) {
	rootPending := true
	for _, s := range served {
		var err error
		if rootPending {
			err = c.AddShareAsRoot(rebuildShare(s))
			rootPending = false
		} else {
			err = c.AddShareQuiet(rebuildShare(s))
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		added++
	}
	return added, firstErr
}

func bootstrap(t *testing.T, served []*types.Share, keep int) (added int, firstErr error) {
	t.Helper()
	return syncInto(newChain(keep), served)
}

func TestProbe_BootstrapAfterPrune(t *testing.T) {
	const chainLen, keep = 20, 10
	now := uint32(time.Now().Unix()) - uint32(chainLen*30) - 60

	seed := newChain(keep)
	var prev [32]byte
	for i := 0; i < chainLen; i++ {
		s := makeTestShare(prev, testMiner1, now+uint32(i*30))
		if err := seed.AddShare(s); err != nil {
			t.Fatalf("seed add %d: %v", i, err)
		}
		prev = s.Hash()
	}

	// Control: no pruning yet — a fresh node must be able to sync.
	served := mainChainOldestFirst(seed)
	added, firstErr := bootstrap(t, served, keep)
	t.Logf("BEFORE prune: seed serves %d shares, fresh node added %d (firstErr=%v)",
		len(served), added, firstErr)
	if added != len(served) {
		t.Errorf("control failed: fresh node could not sync an unpruned chain")
	}

	// Now prune, exactly as the node.go prune ticker does.
	seed.PruneOldShares(keep)
	served = mainChainOldestFirst(seed)
	added, firstErr = bootstrap(t, served, keep)
	t.Logf("AFTER prune:  seed serves %d shares, fresh node added %d (firstErr=%v)",
		len(served), added, firstErr)
	if added != len(served) {
		t.Errorf("FINDING: fresh node added %d of %d shares served; first error: %v",
			added, len(served), firstErr)
	}
}

// makeShareAtTarget is makeTestShare with an explicit share target.
func makeShareAtTarget(prevShareHash [32]byte, minerAddr string, timestamp uint32, target *big.Int) *types.Share {
	builder := types.NewCoinbaseBuilder(testNetwork)
	commitment := types.BuildShareCommitment(prevShareHash)
	coinbaseTx, _, err := builder.BuildCoinbase(800000, commitment,
		[]types.PayoutEntry{{Address: minerAddr, Amount: 5000000000}}, "", 8)
	if err != nil {
		panic(err)
	}
	var merkleRoot [32]byte
	copy(merkleRoot[:], []byte(minerAddr))

	sh := &types.Share{
		Header: types.ShareHeader{
			Version:       536870912,
			PrevBlockHash: prevShareHash,
			MerkleRoot:    merkleRoot,
			Timestamp:     timestamp,
			Bits:          0x207fffff,
		},
		ShareVersion:  1,
		PrevShareHash: prevShareHash,
		ShareTarget:   new(big.Int).Set(target),
		MinerAddress:  minerAddr,
		CoinbaseTx:    coinbaseTx,
	}
	for nonce := uint32(0); ; nonce++ {
		sh.Header.Nonce = nonce
		if util.HashMeetsTarget(sh.Header.Hash(), target) {
			return sh
		}
	}
}

// TestProbe_PruneHorizonVerdict checks that a node whose prune horizon sits at
// a share's parent reaches the same verdict as a node with the full history.
// The two still COMPUTE different expected targets — NextTarget returns
// MaxShareTarget for a window shorter than two shares — so the question is
// whether that difference turns into a rejection, and into a provable-
// misbehavior charge against the peer that relayed it.
// A node offline for longer than the window comes back holding a chain that
// sits entirely below every peer's prune horizon. It must be able to re-anchor
// on what peers still serve, rather than being stranded on its stale chain.
func TestProbe_StaleNodeCanResync(t *testing.T) {
	const chainLen, keep, staleHas = 40, 10, 5
	now := uint32(time.Now().Unix()) - uint32(chainLen*30) - 60

	seed := newChain(keep)
	var all []*types.Share
	var prev [32]byte
	for i := 0; i < chainLen; i++ {
		sh := makeTestShare(prev, testMiner1, now+uint32(i*30))
		if err := seed.AddShare(sh); err != nil {
			t.Fatalf("seed add %d: %v", i, err)
		}
		all = append(all, sh)
		prev = sh.Hash()
	}

	// The stale node holds only the first few shares, then went away.
	stale := newChain(keep)
	if added, err := syncInto(stale, all[:staleHas]); added != staleHas {
		t.Fatalf("stale node setup: added %d of %d (%v)", added, staleHas, err)
	}

	// Meanwhile the seed pruned past everything the stale node holds.
	seed.PruneOldShares(keep)
	served := mainChainOldestFirst(seed)
	for _, sh := range served {
		if _, ok := stale.GetShare(sh.Hash()); ok {
			t.Fatalf("probe is not exercising a disjoint chain")
		}
	}

	added, err := syncInto(stale, served)
	t.Logf("stale node held %d shares, seed serves %d disjoint shares, added %d (err=%v)",
		staleHas, len(served), added, err)
	if added != len(served) {
		t.Errorf("FINDING: stale node could not re-anchor: added %d of %d; first error: %v",
			added, len(served), err)
	}
	tip, _ := stale.Tip()
	if tip == nil || tip.Hash() != served[len(served)-1].Hash() {
		t.Errorf("stale node did not adopt the heavier synced chain as its tip")
	}
}

func TestProbe_PruneHorizonVerdict(t *testing.T) {
	const total, bHolds = 100, 40
	target := new(big.Int).Div(MaxShareTarget, big.NewInt(64))
	base := uint32(time.Now().Unix()) - uint32(total*30) - 600

	// Build the chain directly in the store: these shares declare a target the
	// live retarget would not have chosen from genesis, which is the point —
	// we are exercising validation, not chain construction.
	var shares []*types.Share
	storeA := NewMemoryStore()
	prev := [32]byte{}
	for i := 0; i < total; i++ {
		sh := makeShareAtTarget(prev, testMiner1, base+uint32(i*30), target)
		if err := storeA.Add(sh); err != nil {
			t.Fatalf("store add %d: %v", i, err)
		}
		shares = append(shares, sh)
		prev = sh.Hash()
	}
	if err := storeA.SetTip(prev); err != nil {
		t.Fatal(err)
	}
	full := NewShareChain(storeA, NewDifficultyCalculator(30*time.Second), 8640, testNetwork, testLogger())

	// The pruned node holds only the newest bHolds shares.
	storeB := NewMemoryStore()
	for _, sh := range shares[total-bHolds:] {
		if err := storeB.Add(rebuildShare(sh)); err != nil {
			t.Fatal(err)
		}
	}
	if err := storeB.SetTip(prev); err != nil {
		t.Fatal(err)
	}
	pruned := NewShareChain(storeB, NewDifficultyCalculator(30*time.Second), 8640, testNetwork, testLogger())

	// A parent sitting exactly at the pruned node's horizon.
	horizon := shares[total-bHolds]
	p := horizon.Hash()

	expFull := full.GetExpectedTargetForParent(p)
	expPruned := pruned.GetExpectedTargetForParent(p)
	t.Logf("expected target, full history : %x", expFull)
	t.Logf("expected target, at horizon   : %x", expPruned)
	if expFull.Cmp(expPruned) == 0 {
		t.Fatal("probe is not exercising the divergence; targets agree")
	}

	// A share that is correct by the full-history rule.
	child := makeShareAtTarget(p, testMiner1, horizon.Header.Timestamp+30, expFull)

	errFull := full.AddShare(child)
	errPruned := pruned.AddShare(rebuildShare(child))
	t.Logf("verdict, full history : %v", errFull)
	t.Logf("verdict, at horizon   : %v", errPruned)

	if errFull != nil {
		t.Fatalf("full-history node rejected a correct share: %v", errFull)
	}
	if errPruned != nil {
		var vErr *ValidationError
		provable := errors.As(errPruned, &vErr) && vErr.Category == CategoryProvable
		t.Errorf("FINDING: node at its prune horizon rejected a share the rest of "+
			"the network accepts (provable=%v): %v", provable, errPruned)
	}
}

func TestProbe_FutureTimestampClassifiedProvable(t *testing.T) {
	chain := newChain(10)
	now := uint32(time.Now().Unix())

	g := makeTestShare([32]byte{}, testMiner1, now-60)
	if err := chain.AddShare(g); err != nil {
		t.Fatalf("genesis: %v", err)
	}

	// A timestamp past MaxTimeFuture relative to THIS node's clock. An honest
	// peer whose clock runs ahead would have accepted and relayed it.
	future := now + uint32(MaxTimeFuture.Seconds()) + 60
	s := makeTestShare(g.Hash(), testMiner1, future)

	err := chain.AddShare(s)
	if err == nil {
		t.Fatal("expected the future-timestamped share to be rejected")
	}
	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("not a ValidationError: %v", err)
	}
	t.Logf("reason: %s", vErr.Reason)
	t.Logf("category: %d (CategoryProvable=%d, CategoryIndeterminate=%d)",
		vErr.Category, CategoryProvable, CategoryIndeterminate)
	if vErr.Category == CategoryProvable {
		t.Errorf("FINDING: a rejection that depends on the validating node's own " +
			"clock is classified CategoryProvable, so node.go charges it to the " +
			"peer that relayed it")
	}
}

// Finding 9: the coinbase must pay the PPLNS window, not just the submitter.
func TestProbe_CoinbaseMustPayTheWindow(t *testing.T) {
	chain := newChain(8640)
	now := uint32(time.Now().Unix()) - 400

	// miner1 does all the work: ten shares in the window.
	var prev [32]byte
	for i := 0; i < 10; i++ {
		s := makeTestShare(prev, testMiner1, now+uint32(i*30))
		if err := chain.AddShare(s); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		prev = s.Hash()
	}

	// miner2 now submits a share whose coinbase pays 100% to miner2 and
	// nothing to miner1, who earned essentially the whole window.
	greedy := makeTestShare(prev, testMiner2, now+uint32(10*30))
	outs, err := types.ParseCoinbaseOutputs(greedy.CoinbaseTx)
	if err != nil {
		t.Fatalf("parse outputs: %v", err)
	}
	t.Logf("greedy share coinbase has %d output(s)", len(outs))
	for _, o := range outs {
		t.Logf("   %d sats to script %x", o.Value, o.Script[:8])
	}

	err = chain.AddShare(greedy)
	t.Logf("verdict: %v", err)
	if err == nil {
		t.Error("a share paying the entire coinbase to its own miner was accepted; " +
			"validation is not checking the payout distribution against the window")
	}

	// The mirror image: a share that DOES pay the window must be accepted, so
	// the rule cannot be satisfied by rejecting everything.
	honest := makeValidShare(chain, prev, testMiner2, now+uint32(10*30))
	if err := chain.AddShare(honest); err != nil {
		t.Errorf("a share paying the correct PPLNS split was rejected: %v", err)
	}
}
