package util

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/big"
)

// DoubleSHA256 computes SHA256(SHA256(data)), used extensively in Bitcoin.
func DoubleSHA256(data []byte) [32]byte {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}

// ReverseBytes returns a new slice with bytes reversed.
func ReverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

// HashToHex returns a reversed hex string of a hash (Bitcoin display order).
func HashToHex(hash [32]byte) string {
	return hex.EncodeToString(ReverseBytes(hash[:]))
}

// HexToHash converts a display-order hex string back to a [32]byte hash.
func HexToHash(s string) ([32]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return [32]byte{}, err
	}
	if len(b) != 32 {
		return [32]byte{}, hex.ErrLength
	}
	var h [32]byte
	copy(h[:], ReverseBytes(b))
	return h, nil
}

// CompactToTarget converts a Bitcoin compact (nBits) representation to a big.Int target.
func CompactToTarget(compact uint32) *big.Int {
	exponent := compact >> 24
	mantissa := compact & 0x007fffff

	target := new(big.Int).SetUint64(uint64(mantissa))

	if exponent <= 3 {
		target.Rsh(target, uint(8*(3-exponent)))
	} else {
		target.Lsh(target, uint(8*(exponent-3)))
	}

	// Negative bit
	if compact&0x00800000 != 0 {
		target.Neg(target)
	}

	return target
}

// TargetToCompact converts a big.Int target to Bitcoin compact (nBits) representation.
func TargetToCompact(target *big.Int) uint32 {
	if target.Sign() == 0 {
		return 0
	}

	isNegative := target.Sign() < 0
	absTarget := new(big.Int).Abs(target)

	b := absTarget.Bytes()
	size := uint32(len(b))

	var mantissa uint32
	if size <= 3 {
		for i, v := range b {
			mantissa |= uint32(v) << uint(8*(2-uint32(i)-(3-size)))
		}
	} else {
		mantissa = (uint32(b[0]) << 16) | (uint32(b[1]) << 8) | uint32(b[2])
	}

	// If the high bit of mantissa is set, shift right to avoid being interpreted as negative
	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		size++
	}

	compact := (size << 24) | (mantissa & 0x007fffff)

	if isNegative {
		compact |= 0x00800000
	}

	return compact
}

// TargetToDifficulty converts a target to difficulty relative to the given max target.
func TargetToDifficulty(target, maxTarget *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}
	// difficulty = maxTarget / target
	maxFloat := new(big.Float).SetInt(maxTarget)
	targetFloat := new(big.Float).SetInt(target)
	diff := new(big.Float).Quo(maxFloat, targetFloat)
	result, _ := diff.Float64()
	return result
}

// DifficultyToTarget converts a difficulty to a target given the max target.
func DifficultyToTarget(difficulty float64, maxTarget *big.Int) *big.Int {
	if difficulty == 0 {
		return new(big.Int).Set(maxTarget)
	}
	maxFloat := new(big.Float).SetInt(maxTarget)
	diffFloat := new(big.Float).SetFloat64(difficulty)
	targetFloat := new(big.Float).Quo(maxFloat, diffFloat)

	target, _ := targetFloat.Int(nil)
	return target
}

// HashToDifficulty converts a hash (as little-endian 32 bytes) to the
// difficulty it actually achieved, relative to the given max target.
// This is the work the hash represents, not the difficulty it was mined at.
func HashToDifficulty(hash [32]byte, maxTarget *big.Int) float64 {
	hashInt := new(big.Int).SetBytes(ReverseBytes(hash[:]))
	if hashInt.Sign() == 0 {
		return 0
	}
	return TargetToDifficulty(hashInt, maxTarget)
}

// HashMeetsTarget checks if a hash (as little-endian 32 bytes) is <= target.
func HashMeetsTarget(hash [32]byte, target *big.Int) bool {
	// Bitcoin block hashes are compared as little-endian 256-bit integers.
	// Convert to big-endian for big.Int comparison.
	reversed := ReverseBytes(hash[:])
	hashInt := new(big.Int).SetBytes(reversed)
	return hashInt.Cmp(target) <= 0
}

// Uint32ToBytes converts a uint32 to 4-byte little-endian.
func Uint32ToBytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}
