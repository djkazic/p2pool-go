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
//
// Every share is worth at least one unit. A share whose target somehow
// exceeded MaxShareTarget would otherwise contribute zero work, letting a
// child tie with its parent; fork choice would fall through to the hash
// tiebreak and the tip would stop advancing.
func shareDifficulty(share *types.Share) *big.Int {
	if share.ShareTarget != nil && share.ShareTarget.Sign() > 0 {
		d := new(big.Int).Div(MaxShareTarget, share.ShareTarget)
		if d.Sign() > 0 {
			return d
		}
	}
	return big.NewInt(1)
}

// AssignWork computes and stores a newly added share's cumulative work:
// its parent's work plus its own difficulty.
//
// Validation guarantees the parent is already in the store — validation.go
// rejects a share whose PrevShareHash is unknown — so by the time a share
// reaches this point its parent's work is known and the update is O(1).
//
// Shares below the prune horizon are gone, so the oldest share a node still
// holds anchors at zero. That makes the absolute numbers node-specific, but
// every share a given node holds is measured from the same anchor, which is
// all fork choice needs: it only ever compares two of this node's own tips
// against each other, never against a number from a peer.
//
// Callers must hold the chain mutex in write mode.
func (fc *ForkChoice) AssignWork(share *types.Share) *big.Int {
	work := new(big.Int)

	var zeroHash [32]byte
	if share.PrevShareHash != zeroHash {
		// ChainWork is O(1) once the parent is cached, which is the normal
		// case; it only walks for shares restored from disk.
		work.Set(fc.ChainWork(share.PrevShareHash))
	}

	work.Add(work, shareDifficulty(share))
	share.SetCumulativeWork(work)
	return work
}

// ChainWork returns the cumulative work of the chain ending at the given
// share: the sum of the difficulties of every share from this node's oldest
// known ancestor up to and including this one.
//
// AssignWork stores this on each share as it is added, so the common case is
// a cache hit. The walk below is the cold path — cumulative work is not
// persisted, so shares restored from disk arrive with none — and it fills in
// every share it passes, so it runs at most once per share.
//
// The walk deliberately has no depth limit. It previously stopped after
// windowSize shares, which meant a chain longer than the window reported only
// the work of its most recent windowSize shares. Two chains that diverged
// further back than that then compared as equal, fork choice fell through to
// the hash tiebreak, and a node could reorg onto a strictly lighter chain and
// stay there. The walk terminates at the zero hash, at the prune horizon, or
// at the first ancestor that already has a value.
//
// Callers must hold the chain mutex in write mode: the cache is mutated
// during the walk. ForkChoice is only invoked from AddShare/AddShareQuiet
// and RebuildWork (chain.go), all of which hold sc.mu.Lock.
func (fc *ForkChoice) ChainWork(tipHash [32]byte) *big.Int {
	tip, ok := fc.store.Get(tipHash)
	if !ok {
		return new(big.Int)
	}
	if cached := tip.CumulativeWork(); cached != nil {
		return cached
	}

	// Walk back, collecting shares until we hit a cached ancestor or a
	// boundary. A share chain cannot contain a cycle, so the store's size
	// bounds the walk; the counter is pure defence against a corrupt store.
	guard := fc.store.Count() + 1
	pending := []*types.Share{tip}
	current := tip.PrevShareHash
	var zeroHash [32]byte

	baseWork := new(big.Int)
	for i := 0; current != zeroHash && i < guard; i++ {
		s, ok := fc.store.Get(current)
		if !ok {
			break // prune horizon — this node's chain starts here
		}
		if cached := s.CumulativeWork(); cached != nil {
			baseWork = cached
			break
		}
		pending = append(pending, s)
		current = s.PrevShareHash
	}

	// Walk forward (oldest pending first), accumulating and caching. Every
	// walk ends at a genuine boundary, so each total is final.
	work := new(big.Int).Set(baseWork)
	for i := len(pending) - 1; i >= 0; i-- {
		s := pending[i]
		work.Add(work, shareDifficulty(s))
		s.SetCumulativeWork(work)
	}
	return new(big.Int).Set(work)
}

// SelectTip chooses between the current tip and a new candidate share.
// Returns the hash that should be the new tip: whichever chain carries more
// cumulative work, with ties broken by lower hash so every node resolves
// them identically.
//
// Because cumulative work now counts the whole chain rather than a window,
// a share that extends the current tip always outweighs it — the child
// carries its parent's work plus its own, and shareDifficulty never returns
// zero. The explicit "candidate extends the tip" shortcut this function used
// to carry is therefore redundant, and removing it makes tip selection a
// plain maximum over known shares: a pure function of the share set rather
// than of the order the shares arrived in.
func (fc *ForkChoice) SelectTip(currentTip, candidate [32]byte) [32]byte {
	var zeroHash [32]byte

	// If no current tip, the candidate wins
	if currentTip == zeroHash {
		return candidate
	}

	// If they're the same, no change
	if currentTip == candidate {
		return currentTip
	}

	// Compare cumulative work
	currentWork := fc.ChainWork(currentTip)
	candidateWork := fc.ChainWork(candidate)

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
