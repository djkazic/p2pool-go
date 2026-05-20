package types

import (
	"encoding/binary"
	"math/big"
	"time"

	"github.com/djkazic/p2pool-go/pkg/util"
)

// ShareHeader represents the header of a share, which is also a valid Bitcoin block header.
type ShareHeader struct {
	Version       int32    `json:"version"`
	PrevBlockHash [32]byte `json:"prev_block_hash"`
	MerkleRoot    [32]byte `json:"merkle_root"`
	Timestamp     uint32   `json:"timestamp"`
	Bits          uint32   `json:"bits"` // Bitcoin difficulty target (nBits)
	Nonce         uint32   `json:"nonce"`
}

// Serialize serializes the share header to an 80-byte Bitcoin block header.
func (h *ShareHeader) Serialize() []byte {
	buf := make([]byte, 80)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(h.Version))
	copy(buf[4:36], h.PrevBlockHash[:])
	copy(buf[36:68], h.MerkleRoot[:])
	binary.LittleEndian.PutUint32(buf[68:72], h.Timestamp)
	binary.LittleEndian.PutUint32(buf[72:76], h.Bits)
	binary.LittleEndian.PutUint32(buf[76:80], h.Nonce)
	return buf
}

// Hash computes the double-SHA256 hash of the block header (the block/share hash).
func (h *ShareHeader) Hash() [32]byte {
	return util.DoubleSHA256(h.Serialize())
}

// Share represents a share in the p2pool sharechain.
type Share struct {
	Header ShareHeader `json:"header"`

	// Sharechain-specific fields
	ShareVersion    uint32   `json:"share_version"`
	PrevShareHash   [32]byte `json:"prev_share_hash"`  // Previous share in the sharechain
	ShareTarget     *big.Int `json:"share_target"`     // Sharechain difficulty target
	MinerAddress    string   `json:"miner_address"`    // Miner's payout address (testnet)
	CoinbaseTx      []byte   `json:"coinbase_tx"`      // Full serialized coinbase transaction
	ShareChainNonce uint64   `json:"sharechain_nonce"` // Nonce for sharechain commitment

	ReceivedAt time.Time `json:"received_at"` // When the node first saw this share

	// Cached/computed fields
	hash *[32]byte

	// cumulativeWork is the total chain work back to genesis/prune-boundary
	// (inclusive of this share). Populated lazily by ForkChoice.ChainWork
	// and accessed only via CumulativeWork / SetCumulativeWork. Mutations
	// are serialized by the chain mutex (only ForkChoice writes, only from
	// AddShare/AddShareQuiet paths which hold sc.mu.Lock).
	cumulativeWork *big.Int
}

// CumulativeWork returns the cached cumulative chain work, or nil if it
// has not been computed yet. Returns a defensive copy; callers must not
// mutate the result.
func (s *Share) CumulativeWork() *big.Int {
	if s.cumulativeWork == nil {
		return nil
	}
	return new(big.Int).Set(s.cumulativeWork)
}

// SetCumulativeWork stores the cumulative chain work. Intended for use by
// fork choice; takes a defensive copy so the caller's big.Int can be
// mutated freely afterwards.
func (s *Share) SetCumulativeWork(work *big.Int) {
	s.cumulativeWork = new(big.Int).Set(work)
}

// Hash returns the share's hash (Bitcoin block header hash). Cached after first computation.
func (s *Share) Hash() [32]byte {
	if s.hash != nil {
		return *s.hash
	}
	h := s.Header.Hash()
	s.hash = &h
	return h
}

// Time returns the share's timestamp as a time.Time.
func (s *Share) Time() time.Time {
	return time.Unix(int64(s.Header.Timestamp), 0)
}

// MeetsTarget checks if the share hash meets the given target.
func (s *Share) MeetsTarget(target *big.Int) bool {
	hash := s.Hash()
	return util.HashMeetsTarget(hash, target)
}

// MeetsShareTarget checks if the share meets the sharechain difficulty target.
func (s *Share) MeetsShareTarget() bool {
	if s.ShareTarget == nil {
		return false
	}
	return s.MeetsTarget(s.ShareTarget)
}

// MeetsBitcoinTarget checks if the share meets Bitcoin's full difficulty target.
func (s *Share) MeetsBitcoinTarget() bool {
	btcTarget := util.CompactToTarget(s.Header.Bits)
	return s.MeetsTarget(btcTarget)
}

// IsBlock returns true if this share is also a valid Bitcoin block.
func (s *Share) IsBlock() bool {
	return s.MeetsBitcoinTarget()
}

// HashHex returns the hash as a human-readable hex string (reversed, Bitcoin display order).
func (s *Share) HashHex() string {
	hash := s.Hash()
	return util.HashToHex(hash)
}

// PrevShareHashHex returns the previous share hash as hex.
func (s *Share) PrevShareHashHex() string {
	return util.HashToHex(s.PrevShareHash)
}
