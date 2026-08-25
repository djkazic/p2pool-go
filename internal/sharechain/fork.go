package sharechain

import (
	"math/big"

	"github.com/djkazic/p2pool-go/internal/types"
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

// shareDifficulty returns the work contribution of a single share.
// Helper for ChainWork.
func shareDifficulty(share *types.Share) *big.Int {
	if share.ShareTarget != nil && share.ShareTarget.Sign() > 0 {
		return new(big.Int).Div(MaxShareTarget, share.ShareTarget)
	}
	return big.NewInt(1)
}

// ChainWork calculates the cumulative work of a chain ending at the given share.
// Work is defined as the sum of difficulties of all shares in the chain.
//
// Each share's cumulative work is cached on the share itself the first time
// it's computed. Subsequent ChainWork calls then walk only as far as the
// first cached ancestor, accumulating forward. In steady state (where the
// parent is already cached from a previous SelectTip), each call is O(1).
//
// Callers must hold the chain mutex in write mode: the cache is mutated
// during the walk. ForkChoice is only invoked from AddShare/AddShareQuiet
// (chain.go), both of which hold sc.mu.Lock.
//
// maxDepth bounds the walk defensively; the result is only cached when the
// walk reaches a real boundary (genesis, prune-edge, or an already-cached
// ancestor) so we never store a partial sum.
func (fc *ForkChoice) ChainWork(tipHash [32]byte, maxDepth int) *big.Int {
	tip, ok := fc.store.Get(tipHash)
	if !ok {
		return new(big.Int)
	}
	if cached := tip.CumulativeWork(); cached != nil {
		return cached
	}

	// Walk back, collecting shares until we hit a cached ancestor or boundary.
	pending := []*types.Share{tip}
	current := tip.PrevShareHash
	var zeroHash [32]byte

	baseWork := new(big.Int)
	reachedBoundary := false
	for i := 1; i < maxDepth; i++ {
		if current == zeroHash {
			reachedBoundary = true
			break
		}
		s, ok := fc.store.Get(current)
		if !ok {
			reachedBoundary = true // prune edge — anything beyond is unknowable from here
			break
		}
		if cached := s.CumulativeWork(); cached != nil {
			baseWork = cached
			reachedBoundary = true
			break
		}
		pending = append(pending, s)
		current = s.PrevShareHash
	}

	// Walk forward (oldest pending first), accumulating and caching.
	work := new(big.Int).Set(baseWork)
	for i := len(pending) - 1; i >= 0; i-- {
		s := pending[i]
		work.Add(work, shareDifficulty(s))
		if reachedBoundary {
			s.SetCumulativeWork(work)
		}
	}
	return new(big.Int).Set(work)
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

	// If the candidate directly extends the current tip, it always wins.
	// This avoids a stale-tip problem: once the chain exceeds windowSize,
	// a child has the same cumulative work as its parent (drops the oldest
	// share, adds itself at the same difficulty = net zero), so work-based
	// comparison would fall to a hash tiebreaker that fails ~50% of the
	// time, causing the tip to stall and forks to accumulate.
	candidateShare, _ := fc.store.Get(candidate)
	if candidateShare != nil && candidateShare.PrevShareHash == currentTip {
		return candidate
	}

	// Compare cumulative work
	commonAncestor, depthA, depthB := fc.FindCommonAncestor(currentTip, candidate, windowSize)

	// They have a common ancestor, compare chain work from that point
	if commonAncestor != zeroHash {
		currentWork := fc.ChainWork(currentTip, depthA)
		candidateWork := fc.ChainWork(candidate, depthB)

		cmp := candidateWork.Cmp(currentWork)
		if cmp > 0 {
			return candidate
		}
		if cmp < 0 {
			return currentTip
		}
	}

	// The current tip and candidate do not share a common ancestor within the window
	// we must handle both as separate chains by walking windowSize blocks on each fork and
	// sum up the work
	currentWork := fc.ChainWork(currentTip, windowSize)
	candidateWork := fc.ChainWork(candidate, windowSize)
	cmp := candidateWork.Cmp(currentWork)
	if cmp > 0 {
		return candidate
	}
	if cmp < 0 {
		return currentTip
	}

	// Tie-breaking: lower hash wins (deterministic)
	currentShare, _ := fc.store.Get(currentTip)
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
