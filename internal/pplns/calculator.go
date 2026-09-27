package pplns

import (
	"math/big"
	"sort"

	"github.com/djkazic/p2pool-go/internal/types"
)

// sortedAddresses returns the payout addresses in lexicographic order, so
// that every pass over the map is deterministic.
func sortedAddresses(payouts map[string]int64) []string {
	out := make([]string, 0, len(payouts))
	for addr := range payouts {
		out = append(out, addr)
	}
	sort.Strings(out)
	return out
}

// Calculator computes PPLNS payouts.
//
// Every node has to reach the same answer: validation recomputes the split
// from the share's own window and compares it against the coinbase, so any
// disagreement is a rejected share. The arithmetic is therefore exact
// integer arithmetic throughout, and the parameters are protocol constants
// rather than per-node configuration.
type Calculator struct {
	finderFeeBasisPoints int64
	dustThresholdSats    int64
}

// NewCalculator creates a new PPLNS calculator. The finder fee is given in
// basis points (hundredths of a percent) so the split needs no float
// arithmetic: 50 is 0.50%.
func NewCalculator(finderFeeBasisPoints int64, dustThresholdSats int64) *Calculator {
	return &Calculator{
		finderFeeBasisPoints: finderFeeBasisPoints,
		dustThresholdSats:    dustThresholdSats,
	}
}

// CalculatePayouts computes payout amounts for each miner in the PPLNS window.
// totalReward is the total coinbase value (block subsidy + fees) in satoshis.
// finderAddress is the miner who found the block (receives the finder fee).
func (c *Calculator) CalculatePayouts(window *Window, totalReward int64, finderAddress string) []types.PayoutEntry {
	if window.ShareCount() == 0 || totalReward <= 0 {
		return nil
	}

	// Calculate finder fee (exact integer arithmetic; see the type comment)
	finderFee := totalReward * c.finderFeeBasisPoints / 10000
	distributableReward := totalReward - finderFee

	// Get per-miner weights
	minerWeights := window.MinerWeights()
	totalWeight := window.TotalWeight()

	if totalWeight.Sign() == 0 {
		return nil
	}

	// Calculate per-miner payouts proportional to weight
	payouts := make(map[string]int64)
	var distributed int64

	// Sort addresses for deterministic output
	addresses := make([]string, 0, len(minerWeights))
	for addr := range minerWeights {
		addresses = append(addresses, addr)
	}
	sort.Strings(addresses)

	for _, addr := range addresses {
		weight := minerWeights[addr]
		// payout = distributableReward * weight / totalWeight
		payout := new(big.Int).Mul(big.NewInt(distributableReward), weight)
		payout.Div(payout, totalWeight)

		if !payout.IsInt64() {
			// Shouldn't happen (payout <= distributableReward), but clamp for safety.
			payout.SetInt64(distributableReward)
		}
		amount := payout.Int64()

		if amount > 0 {
			payouts[addr] = amount
			distributed += amount
		}
	}

	// Add finder fee to finder's payout
	if finderAddress != "" && finderFee > 0 {
		payouts[finderAddress] += finderFee
		distributed += finderFee
	}

	// Handle rounding remainder: give to finder (or first miner)
	remainder := totalReward - distributed
	if remainder > 0 {
		if finderAddress != "" {
			payouts[finderAddress] += remainder
		} else if len(addresses) > 0 {
			payouts[addresses[0]] += remainder
		}
	}

	// Consolidate dust outputs: payouts below dust threshold get redistributed
	var dustTotal int64
	var dustAddresses []string
	for addr, amount := range payouts {
		if amount < c.dustThresholdSats && addr != finderAddress {
			dustTotal += amount
			dustAddresses = append(dustAddresses, addr)
		}
	}

	// Remove dust payouts and give to finder (or largest remaining miner).
	// If ALL payouts are below dust, skip consolidation entirely — it's better
	// to have many small outputs than to lose funds.
	if len(dustAddresses) < len(payouts) {
		for _, addr := range dustAddresses {
			delete(payouts, addr)
		}
		if dustTotal > 0 {
			if finderAddress != "" {
				payouts[finderAddress] += dustTotal
			} else {
				for _, addr := range addresses {
					if _, ok := payouts[addr]; ok {
						payouts[addr] += dustTotal
						break
					}
				}
			}
		}
	}

	// Final standardness pass. Two outputs can still sit below the dust
	// threshold after the sweep above: the finder, who is deliberately exempt
	// from it, and every output at once when consolidation was skipped because
	// they were all dust. Either makes the coinbase non-standard, so the block
	// would not relay and the whole reward would be lost.
	//
	// Fold each remaining dust output into the largest other one. Nothing is
	// discarded — the total is unchanged, only the recipient moves — and each
	// pass removes one entry, so this terminates. If a single output is left
	// and it is still below the threshold, the reward itself is smaller than
	// the dust limit and there is nothing better to do.
	for len(payouts) > 1 {
		ordered := sortedAddresses(payouts)

		smallest, haveSmall := "", false
		for _, addr := range ordered {
			if payouts[addr] >= c.dustThresholdSats {
				continue
			}
			if !haveSmall || payouts[addr] < payouts[smallest] {
				smallest, haveSmall = addr, true
			}
		}
		if !haveSmall {
			break
		}

		largest, haveLarge := "", false
		for _, addr := range ordered {
			if addr == smallest {
				continue
			}
			if !haveLarge || payouts[addr] > payouts[largest] {
				largest, haveLarge = addr, true
			}
		}
		if !haveLarge {
			break
		}

		payouts[largest] += payouts[smallest]
		delete(payouts, smallest)
	}

	// Build sorted result
	result := make([]types.PayoutEntry, 0, len(payouts))
	for addr, amount := range payouts {
		result = append(result, types.PayoutEntry{
			Address: addr,
			Amount:  amount,
		})
	}

	// Sort by amount descending, then address for determinism
	sort.Slice(result, func(i, j int) bool {
		if result[i].Amount != result[j].Amount {
			return result[i].Amount > result[j].Amount
		}
		return result[i].Address < result[j].Address
	})

	return result
}
