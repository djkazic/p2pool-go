package sharechain

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"math/big"
	"sync"

	"github.com/djkazic/p2pool-go/internal/types"

	"go.etcd.io/bbolt"
	"go.uber.org/zap"
)

var (
	bucketShares = []byte("shares")
	bucketMeta   = []byte("meta")
	keyTip       = []byte("tip")
)

// BoltStore is a write-through persistent ShareStore backed by bbolt.
// All reads come from in-memory maps; writes go to both memory and disk.
type BoltStore struct {
	mu      sync.RWMutex
	db      *bbolt.DB
	shares  map[[32]byte]*types.Share
	tipHash [32]byte
	hasTip  bool
	logger  *zap.Logger
}

// NewBoltStore opens (or creates) a bbolt database at path, loads all
// existing shares and the tip into memory, and returns the store.
func NewBoltStore(path string, logger *zap.Logger) (*BoltStore, error) {
	db, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bolt db: %w", err)
	}

	// Ensure buckets exist.
	err = db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketShares); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketMeta)
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	s := &BoltStore{
		db:     db,
		shares: make(map[[32]byte]*types.Share),
		logger: logger,
	}

	// Load all shares from disk into memory.
	err = db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketShares)
		return b.ForEach(func(k, v []byte) error {
			share, err := decodeShare(v)
			if err != nil {
				return fmt.Errorf("decode share %x: %w", k, err)
			}
			var hash [32]byte
			copy(hash[:], k)
			s.shares[hash] = share
			return nil
		})
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load shares: %w", err)
	}

	// Load tip.
	err = db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		v := b.Get(keyTip)
		if v != nil && len(v) == 32 {
			copy(s.tipHash[:], v)
			s.hasTip = true
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load tip: %w", err)
	}

	logger.Info("sharechain loaded from disk",
		zap.Int("shares_loaded", len(s.shares)),
		zap.Bool("has_tip", s.hasTip),
	)

	return s, nil
}

func (s *BoltStore) Add(share *types.Share) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hash := share.Hash()
	if _, exists := s.shares[hash]; exists {
		return fmt.Errorf("share %x already exists", hash[:8])
	}

	data, err := encodeShare(share)
	if err != nil {
		return fmt.Errorf("encode share: %w", err)
	}

	err = s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketShares).Put(hash[:], data)
	})
	if err != nil {
		return fmt.Errorf("persist share: %w", err)
	}

	s.shares[hash] = share
	return nil
}

func (s *BoltStore) Get(hash [32]byte) (*types.Share, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	share, ok := s.shares[hash]
	return share, ok
}

func (s *BoltStore) Has(hash [32]byte) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.shares[hash]
	return ok
}

func (s *BoltStore) Tip() (*types.Share, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasTip {
		return nil, false
	}
	share, ok := s.shares[s.tipHash]
	return share, ok
}

func (s *BoltStore) SetTip(hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.shares[hash]; !ok {
		return fmt.Errorf("cannot set tip to unknown share %x", hash[:8])
	}

	err := s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketMeta).Put(keyTip, hash[:])
	})
	if err != nil {
		return fmt.Errorf("persist tip: %w", err)
	}

	s.tipHash = hash
	s.hasTip = true
	return nil
}

func (s *BoltStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.shares)
}

func (s *BoltStore) GetAncestors(hash [32]byte, count int) []*types.Share {
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

func (s *BoltStore) Delete(hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.shares[hash]; !ok {
		return fmt.Errorf("share %x not found", hash[:8])
	}

	err := s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketShares).Delete(hash[:])
	})
	if err != nil {
		return fmt.Errorf("delete share from disk: %w", err)
	}

	delete(s.shares, hash)
	return nil
}

// DeleteBatch removes multiple shares in a single bolt transaction.
func (s *BoltStore) DeleteBatch(hashes [][32]byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	deleted := 0
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketShares)
		for _, h := range hashes {
			if _, ok := s.shares[h]; !ok {
				continue
			}
			if err := b.Delete(h[:]); err != nil {
				return err
			}
			delete(s.shares, h)
			deleted++
		}
		return nil
	})
	return deleted, err
}

func (s *BoltStore) AllHashes() [][32]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hashes := make([][32]byte, 0, len(s.shares))
	for h := range s.shares {
		hashes = append(hashes, h)
	}
	return hashes
}

func (s *BoltStore) HashesNotIn(keep map[[32]byte]struct{}) [][32]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect hashes that are in the store but not in the keep set
	toDelete := make([][32]byte, 0, len(s.shares)-len(keep))
	// Iterate over the whole store and check if the hash is in the keep set.
	// If not, add it to the list of hashes to delete.
	//  TODO: This can be obtimized by creating an inex of the store
	// making this function o(1) instead of o(n)
	for h := range s.shares {
		if _, ok := keep[h]; !ok {
			toDelete = append(toDelete, h)
		}
	}
	return toDelete
}
func (s *BoltStore) Close() error {
	return s.db.Close()
}

// gobShare is the serialization form for Share. It mirrors types.Share but
// stores ShareTarget as bytes to avoid gob's issues with nil *big.Int.
type gobShare struct {
	Header           types.ShareHeader
	ShareVersion     uint32
	PrevShareHash    [32]byte
	ShareTargetBytes []byte
	MinerAddress     string
	CoinbaseTx       []byte
	ShareChainNonce  uint64
}

func encodeShare(s *types.Share) ([]byte, error) {
	gs := gobShare{
		Header:          s.Header,
		ShareVersion:    s.ShareVersion,
		PrevShareHash:   s.PrevShareHash,
		MinerAddress:    s.MinerAddress,
		CoinbaseTx:      s.CoinbaseTx,
		ShareChainNonce: s.ShareChainNonce,
	}
	if s.ShareTarget != nil {
		gs.ShareTargetBytes = s.ShareTarget.Bytes()
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&gs); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeShare(data []byte) (*types.Share, error) {
	var gs gobShare
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&gs); err != nil {
		return nil, err
	}

	s := &types.Share{
		Header:          gs.Header,
		ShareVersion:    gs.ShareVersion,
		PrevShareHash:   gs.PrevShareHash,
		MinerAddress:    gs.MinerAddress,
		CoinbaseTx:      gs.CoinbaseTx,
		ShareChainNonce: gs.ShareChainNonce,
	}
	if len(gs.ShareTargetBytes) > 0 {
		s.ShareTarget = new(big.Int).SetBytes(gs.ShareTargetBytes)
	}

	return s, nil
}
