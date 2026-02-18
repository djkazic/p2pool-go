package sharechain

import (
	"fmt"
	"sync"

	"github.com/djkazic/p2pool-go/internal/types"
)

// ShareStore defines the interface for storing and retrieving shares.
type ShareStore interface {
	Add(share *types.Share) error
	Get(hash [32]byte) (*types.Share, bool)
	Has(hash [32]byte) bool
	Tip() (*types.Share, bool)
	SetTip(hash [32]byte) error
	Count() int
	// GetAncestors returns shares walking backwards from the given hash, up to count.
	GetAncestors(hash [32]byte, count int) []*types.Share
	// Delete removes a share from the store by hash.
	Delete(hash [32]byte) error
	// AllHashes returns the hashes of all shares in the store.
	AllHashes() [][32]byte
	// HashesNotIn returns all hashes in the store that are not in the given keep set.
	HashesNotIn(keep map[[32]byte]struct{}) [][32]byte
	Close() error
}

// MemoryStore is an in-memory implementation of ShareStore.
type MemoryStore struct {
	mu      sync.RWMutex
	shares  map[[32]byte]*types.Share
	tipHash [32]byte
	hasTip  bool
}

// NewMemoryStore creates a new in-memory share store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		shares: make(map[[32]byte]*types.Share),
	}
}

func (s *MemoryStore) Add(share *types.Share) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hash := share.Hash()
	if _, exists := s.shares[hash]; exists {
		return fmt.Errorf("share %x already exists", hash[:8])
	}

	s.shares[hash] = share
	return nil
}

func (s *MemoryStore) Get(hash [32]byte) (*types.Share, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	share, ok := s.shares[hash]
	return share, ok
}

func (s *MemoryStore) Has(hash [32]byte) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.shares[hash]
	return ok
}

func (s *MemoryStore) Tip() (*types.Share, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasTip {
		return nil, false
	}
	share, ok := s.shares[s.tipHash]
	return share, ok
}

func (s *MemoryStore) SetTip(hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.shares[hash]; !ok {
		return fmt.Errorf("cannot set tip to unknown share %x", hash[:8])
	}
	s.tipHash = hash
	s.hasTip = true
	return nil
}

func (s *MemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.shares)
}

func (s *MemoryStore) Delete(hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.shares[hash]; !ok {
		return fmt.Errorf("share %x not found", hash[:8])
	}

	delete(s.shares, hash)
	return nil
}

func (s *MemoryStore) AllHashes() [][32]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hashes := make([][32]byte, 0, len(s.shares))
	for h := range s.shares {
		hashes = append(hashes, h)
	}
	return hashes
}

func (s *MemoryStore) HashesNotIn(keep map[[32]byte]struct{}) [][32]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect hashes that are in the store but not in the keep set
	toDelete := make([][32]byte, len(s.shares)-len(keep))
	// Iterate over the whole store and check if the hash is in the keep set.
	// If not, add it to the list of hashes to delete.
	for h := range s.shares {
		if _, ok := keep[h]; !ok {
			toDelete = append(toDelete, h)
		}
	}
	return toDelete
}

func (s *MemoryStore) Close() error { return nil }

func (s *MemoryStore) GetAncestors(hash [32]byte, count int) []*types.Share {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var ancestors []*types.Share
	current := hash

	for i := 0; i < count; i++ {
		share, ok := s.shares[current]
		if !ok {
			break
		}
		ancestors = append(ancestors, share)
		current = share.PrevShareHash
	}

	return ancestors
}
