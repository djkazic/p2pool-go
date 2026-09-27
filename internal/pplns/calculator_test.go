package pplns

import (
	"math/big"
	"testing"

	"github.com/djkazic/p2pool-go/internal/types"
)

func easyTarget() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
}

func makeShare(addr string, target *big.Int) *types.Share {
	return &types.Share{
		MinerAddress: addr,
		ShareTarget:  target,
	}
}

func TestCalculatePayouts_EqualShares(t *testing.T) {
	maxTarget := easyTarget()
	target := new(big.Int).Div(maxTarget, big.NewInt(2)) // difficulty 2 each

	shares := []*types.Share{
		makeShare("miner1", target),
		makeShare("miner2", target),
		makeShare("miner1", target),
		makeShare("miner2", target),
	}

	window := NewWindow(shares, maxTarget)
	calc := NewCalculator(0, 546) // No finder fee for simplicity

	payouts := calc.CalculatePayouts(window, 1000000, "miner1")

	// 4 shares, 2 each = 50/50 split
	var total int64
	for _, p := range payouts {
		total += p.Amount
	}

	if total != 1000000 {
		t.Errorf("total payouts = %d, want 1000000", total)
	}

	// Both miners should get roughly 500000 each
	for _, p := range payouts {
		if p.Amount < 490000 || p.Amount > 510000 {
			t.Errorf("miner %s got %d, expected ~500000", p.Address, p.Amount)
		}
	}
}

func TestCalculatePayouts_WithFinderFee(t *testing.T) {
	maxTarget := easyTarget()
	target := maxTarget // difficulty 1 each

	shares := []*types.Share{
		makeShare("miner1", target),
		makeShare("miner2", target),
	}

	window := NewWindow(shares, maxTarget)
	calc := NewCalculator(50, 546) // 0.50% finder fee (50 basis points)

	totalReward := int64(5000000000) // 50 BTC
	payouts := calc.CalculatePayouts(window, totalReward, "miner1")

	var total int64
	for _, p := range payouts {
		total += p.Amount
	}

	if total != totalReward {
		t.Errorf("total = %d, want %d", total, totalReward)
	}

	// Finder (miner1) should get slightly more than miner2 due to finder fee
	var miner1Payout, miner2Payout int64
	for _, p := range payouts {
		if p.Address == "miner1" {
			miner1Payout = p.Amount
		} else if p.Address == "miner2" {
			miner2Payout = p.Amount
		}
	}

	if miner1Payout <= miner2Payout {
		t.Errorf("finder (miner1=%d) should get more than miner2=%d", miner1Payout, miner2Payout)
	}
}

func TestCalculatePayouts_DustConsolidation(t *testing.T) {
	maxTarget := easyTarget()

	// One miner has 999 shares, another has 1 share
	// With 1000 sats total, the small miner gets ~1 sat (below dust)
	shares := make([]*types.Share, 1000)
	for i := 0; i < 999; i++ {
		shares[i] = makeShare("bigminer", maxTarget)
	}
	shares[999] = makeShare("tinyminer", maxTarget)

	window := NewWindow(shares, maxTarget)
	calc := NewCalculator(0, 546) // dust = 546 sats

	payouts := calc.CalculatePayouts(window, 1000, "bigminer")

	// tinyminer should be consolidated (below dust threshold)
	for _, p := range payouts {
		if p.Address == "tinyminer" {
			t.Error("tiny miner should have been consolidated as dust")
		}
	}

	// All funds should still be accounted for
	var total int64
	for _, p := range payouts {
		total += p.Amount
	}
	if total != 1000 {
		t.Errorf("total = %d, want 1000", total)
	}
}

func TestCalculatePayouts_Empty(t *testing.T) {
	maxTarget := easyTarget()
	window := NewWindow([]*types.Share{}, maxTarget)
	calc := NewCalculator(50, 546)

	payouts := calc.CalculatePayouts(window, 1000000, "miner1")
	if payouts != nil {
		t.Error("empty window should return nil payouts")
	}
}

func TestCalculatePayouts_SingleMiner(t *testing.T) {
	maxTarget := easyTarget()

	shares := []*types.Share{
		makeShare("solo", maxTarget),
		makeShare("solo", maxTarget),
		makeShare("solo", maxTarget),
	}

	window := NewWindow(shares, maxTarget)
	calc := NewCalculator(50, 546)

	payouts := calc.CalculatePayouts(window, 5000000000, "solo")

	if len(payouts) != 1 {
		t.Fatalf("expected 1 payout, got %d", len(payouts))
	}
	if payouts[0].Amount != 5000000000 {
		t.Errorf("solo miner should get everything: got %d", payouts[0].Amount)
	}
}

func TestWindow_MinerWeights(t *testing.T) {
	maxTarget := easyTarget()
	halfTarget := new(big.Int).Div(maxTarget, big.NewInt(2))

	shares := []*types.Share{
		makeShare("miner1", maxTarget),  // weight 1
		makeShare("miner1", maxTarget),  // weight 1
		makeShare("miner2", halfTarget), // weight 2
	}

	window := NewWindow(shares, maxTarget)
	weights := window.MinerWeights()

	w1 := weights["miner1"]
	w2 := weights["miner2"]

	// miner1 has weight 2 (1+1), miner2 has weight 2
	if w1.Cmp(w2) != 0 {
		t.Errorf("miner1 weight=%s, miner2 weight=%s, expected equal", w1, w2)
	}
}
