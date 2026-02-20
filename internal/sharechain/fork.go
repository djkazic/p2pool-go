package sharechain

import (
	"math/big"

	"github.com/djkazic/p2pool-go/pkg/util"
)

// ForkChoice implements heaviest-chain tip selection for the sharechain.
type ForkChoice struct {
	store ShareStore
}

// NewForkChoice creates a new fork choice instance.
func NewForkChoice(store ShareStore) *ForkChoice {
	return &ForkChoice{store: store}
}

// ChainWork calculates the cumulative work of a chain ending at the given share.
// Work is defined as the sum of difficulties of all shares in the chain.
func (fc *ForkChoice) ChainWork(tipHash [32]byte, maxDepth int) *big.Int {
	totalWork := new(big.Int)
	current := tipHash
	var zeroHash [32]byte

	for i := 0; i < maxDepth; i++ {
		share, ok := fc.store.Get(current)
		if !ok {
			break
		}

		// Work for this share = target_max / share_target (i.e., difficulty)
		if share.ShareTarget != nil && share.ShareTarget.Sign() > 0 {
			work := new(big.Int).Div(MaxShareTarget, share.ShareTarget)
			totalWork.Add(totalWork, work)
		} else {
			// If no share target, count as difficulty 1
			totalWork.Add(totalWork, big.NewInt(1))
		}

		current = share.PrevShareHash
		if current == zeroHash {
			break
		}
	}

	return totalWork
}

// SelectTip chooses between the current tip and a new candidate share.
// Returns the hash that should be the new tip.
func (fc *ForkChoice) SelectTip(currentTip, candidate [32]byte, windowSize int) [32]byte {
	var zeroHash [32]byte

	// If no current tip, the candidate wins
	if currentTip == zeroHash {
		return candidate
	}

	// If they're the same, no change
	if currentTip == candidate {
		return currentTip
	}

	// Compare cumulative work using the full store depth, not windowSize.
	// windowSize controls how far back difficulty adjustment looks, but chain
	// work must be computed over the full chain history, otherwise the window
	// clips the ancestor walk asymmetrically between two candidates (e.g. a
	// long-standing tip vs a newly added share), producing incorrect work totals
	// that trigger spurious reorgs.
	depth := fc.store.Count() + 1
	currentWork := fc.ChainWork(currentTip, depth)
	candidateWork := fc.ChainWork(candidate, depth)

	// Compare cumulative work
	cmp := candidateWork.Cmp(currentWork)
	if cmp > 0 {
		return candidate
	}
	if cmp < 0 {
		return currentTip
	}

	// Tie-breaking: lower hash wins (deterministic)
	currentShare, _ := fc.store.Get(currentTip)
	candidateShare, _ := fc.store.Get(candidate)
	if currentShare != nil && candidateShare != nil {
		currentHash := currentShare.Hash()
		candidateHash := candidateShare.Hash()
		currentInt := new(big.Int).SetBytes(util.ReverseBytes(currentHash[:]))
		candidateInt := new(big.Int).SetBytes(util.ReverseBytes(candidateHash[:]))
		if candidateInt.Cmp(currentInt) < 0 {
			return candidate
		}
	}

	return currentTip
}

// FindCommonAncestor finds the common ancestor between two chain tips.
// Returns the common ancestor hash and the depths from each tip.
func (fc *ForkChoice) FindCommonAncestor(tipA, tipB [32]byte, maxDepth int) ([32]byte, int, int) {
	// Build set of ancestors for tipA
	ancestorsA := make(map[[32]byte]int) // hash -> depth
	current := tipA
	var zeroHash [32]byte

	for depth := 0; depth < maxDepth; depth++ {
		ancestorsA[current] = depth
		share, ok := fc.store.Get(current)
		if !ok {
			break
		}
		current = share.PrevShareHash
		if current == zeroHash {
			ancestorsA[current] = depth + 1
			break
		}
	}

	// Walk tipB's ancestors looking for a match
	current = tipB
	for depth := 0; depth < maxDepth; depth++ {
		if depthA, found := ancestorsA[current]; found {
			return current, depthA, depth
		}
		share, ok := fc.store.Get(current)
		if !ok {
			break
		}
		current = share.PrevShareHash
		if current == zeroHash {
			if depthA, found := ancestorsA[current]; found {
				return current, depthA, depth + 1
			}
			break
		}
	}

	return zeroHash, -1, -1
}
