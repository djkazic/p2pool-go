package types

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/djkazic/p2pool-go/pkg/util"
)

const (
	// CoinbaseScriptSigMaxLen is the maximum allowed coinbase scriptSig length.
	CoinbaseScriptSigMaxLen = 100

	// SharechainCommitmentTag is the tag for sharechain data in the coinbase.
	SharechainCommitmentTag = "p2pool"
)

// CoinbaseBuilder builds coinbase transactions for shares.
type CoinbaseBuilder struct {
	network string
}

// NewCoinbaseBuilder creates a new coinbase builder.
func NewCoinbaseBuilder(network string) *CoinbaseBuilder {
	return &CoinbaseBuilder{network: network}
}

// BuildCoinbase builds a complete coinbase transaction.
// blockHeight is encoded in the scriptSig per BIP34.
// shareCommitment is the sharechain data embedded in the scriptSig.
// payouts define the outputs.
// witnessCommitment is the segwit witness commitment (hex) if present.
func (cb *CoinbaseBuilder) BuildCoinbase(
	blockHeight int64,
	shareCommitment []byte,
	payouts []PayoutEntry,
	witnessCommitment string,
	extranonceSize int,
) ([]byte, int, error) {
	var buf bytes.Buffer

	// Version (4 bytes, little-endian)
	binary.Write(&buf, binary.LittleEndian, int32(2))

	// NOTE: No segwit marker/flag here. This builds the non-witness
	// serialization so that DoubleSHA256 produces the txid (not wtxid).
	// Witness data is added via AddCoinbaseWitness for block submission.

	// Input count (always 1 for coinbase)
	buf.Write(util.WriteVarInt(1))

	// Previous output (null for coinbase)
	buf.Write(make([]byte, 32)) // null hash
	binary.Write(&buf, binary.LittleEndian, uint32(0xffffffff))

	// Build scriptSig
	scriptSig := buildScriptSig(blockHeight, shareCommitment, extranonceSize)
	extranonceOffset := buf.Len() + len(util.WriteVarInt(uint64(len(scriptSig)))) + len(scriptSig) - extranonceSize

	buf.Write(util.WriteVarInt(uint64(len(scriptSig))))
	buf.Write(scriptSig)

	// Sequence
	binary.Write(&buf, binary.LittleEndian, uint32(0xffffffff))

	// Output count
	outputCount := len(payouts)
	if witnessCommitment != "" {
		outputCount++ // witness commitment output
	}
	buf.Write(util.WriteVarInt(uint64(outputCount)))

	// Payout outputs
	for _, payout := range payouts {
		script, err := addressToScript(payout.Address, cb.network)
		if err != nil {
			return nil, 0, fmt.Errorf("address to script for %s: %w", payout.Address, err)
		}
		binary.Write(&buf, binary.LittleEndian, payout.Amount)
		buf.Write(util.WriteVarInt(uint64(len(script))))
		buf.Write(script)
	}

	// Witness commitment output (OP_RETURN)
	if witnessCommitment != "" {
		wcBytes, err := util.HexToBytes(witnessCommitment)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid witness commitment hex: %w", err)
		}
		binary.Write(&buf, binary.LittleEndian, int64(0)) // 0 value
		buf.Write(util.WriteVarInt(uint64(len(wcBytes))))
		buf.Write(wcBytes)
	}

	// Locktime
	binary.Write(&buf, binary.LittleEndian, uint32(0))

	return buf.Bytes(), extranonceOffset, nil
}

// buildScriptSig builds the coinbase scriptSig with BIP34 height and sharechain commitment.
func buildScriptSig(height int64, shareCommitment []byte, extranonceSize int) []byte {
	var buf bytes.Buffer

	// BIP34: block height (serialized as minimal CScriptNum)
	heightBytes := serializeHeight(height)
	buf.Write(heightBytes)

	// Sharechain commitment
	if len(shareCommitment) > 0 {
		buf.Write(shareCommitment)
	}

	// Extranonce placeholder (will be filled per-miner)
	buf.Write(make([]byte, extranonceSize))

	return buf.Bytes()
}

// serializeHeight serializes a block height for BIP34 coinbase scriptSig.
func serializeHeight(height int64) []byte {
	if height <= 16 {
		// OP_0 through OP_16
		if height == 0 {
			return []byte{0x01, 0x00}
		}
		return []byte{0x01, byte(height)}
	}

	// Serialize as minimal little-endian with length prefix
	h := height
	var heightBytes []byte
	for h > 0 {
		heightBytes = append(heightBytes, byte(h&0xff))
		h >>= 8
	}
	// Ensure no sign ambiguity
	if heightBytes[len(heightBytes)-1]&0x80 != 0 {
		heightBytes = append(heightBytes, 0x00)
	}

	result := []byte{byte(len(heightBytes))}
	result = append(result, heightBytes...)
	return result
}

// addressToScript converts a Bitcoin address to a scriptPubKey.
// Supports P2WPKH (bech32), P2WSH (bech32), and basic P2PKH/P2SH.
func addressToScript(address string, network string) ([]byte, error) {
	// Handle bech32/bech32m addresses (testnet: tb1..., mainnet: bc1...)
	prefix := "tb1"
	if network == "mainnet" {
		prefix = "bc1"
	} else if network == "regtest" {
		prefix = "bcrt1"
	}

	if len(address) > len(prefix) && address[:len(prefix)] == prefix {
		return bech32AddressToScript(address)
	}

	return nil, fmt.Errorf("unsupported address format: %s (only bech32 supported)", address)
}

// bech32AddressToScript converts a bech32 address to a witness scriptPubKey.
// For simplicity, we decode the witness program directly.
func bech32AddressToScript(address string) ([]byte, error) {
	hrp, data, err := bech32Decode(address)
	if err != nil {
		return nil, fmt.Errorf("bech32 decode: %w", err)
	}
	_ = hrp

	if len(data) < 1 {
		return nil, fmt.Errorf("empty bech32 data")
	}

	witnessVersion := data[0]
	witnessProgram, err := convertBits(data[1:], 5, 8, false)
	if err != nil {
		return nil, fmt.Errorf("convert bits: %w", err)
	}

	// Build witness scriptPubKey: OP_n <len> <program>
	var script []byte
	if witnessVersion == 0 {
		script = append(script, 0x00) // OP_0
	} else {
		script = append(script, 0x50+witnessVersion) // OP_1 through OP_16
	}
	script = append(script, byte(len(witnessProgram)))
	script = append(script, witnessProgram...)

	return script, nil
}

// bech32Decode decodes a bech32/bech32m string with full checksum verification.
func bech32Decode(s string) (string, []byte, error) {
	// Find separator (last occurrence of '1')
	sepIdx := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '1' {
			sepIdx = i
			break
		}
	}
	if sepIdx < 1 || sepIdx+7 > len(s) {
		return "", nil, fmt.Errorf("invalid bech32 separator position")
	}

	hrp := s[:sepIdx]
	dataStr := s[sepIdx+1:]

	charset := "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	charMap := make(map[byte]byte)
	for i, c := range charset {
		charMap[byte(c)] = byte(i)
	}

	data := make([]byte, len(dataStr))
	for i := 0; i < len(dataStr); i++ {
		c := dataStr[i]
		if c >= 'A' && c <= 'Z' {
			c = c - 'A' + 'a'
		}
		val, ok := charMap[c]
		if !ok {
			return "", nil, fmt.Errorf("invalid bech32 character: %c", c)
		}
		data[i] = val
	}

	if len(data) < 6 {
		return "", nil, fmt.Errorf("bech32 data too short")
	}

	// Verify checksum
	check := bech32Polymod(bech32HRPExpand(hrp), data)
	if check != 1 && check != 0x2bc830a3 {
		return "", nil, fmt.Errorf("invalid bech32 checksum")
	}

	// Strip checksum (last 6 characters)
	data = data[:len(data)-6]

	return hrp, data, nil
}

// bech32Polymod computes the bech32 polynomial checksum over the HRP expansion and data.
func bech32Polymod(hrpExp []byte, data []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range hrpExp {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	for _, v := range data {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

// bech32HRPExpand expands the HRP for checksum computation.
func bech32HRPExpand(hrp string) []byte {
	ret := make([]byte, 0, len(hrp)*2+1)
	for _, c := range hrp {
		ret = append(ret, byte(c>>5))
	}
	ret = append(ret, 0)
	for _, c := range hrp {
		ret = append(ret, byte(c&31))
	}
	return ret
}

// convertBits converts between bit groups.
func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	acc := uint32(0)
	bits := uint(0)
	var result []byte
	maxv := uint32((1 << toBits) - 1)

	for _, val := range data {
		acc = (acc << fromBits) | uint32(val)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte((acc>>bits)&maxv))
		}
	}

	if pad {
		if bits > 0 {
			result = append(result, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits {
		return nil, fmt.Errorf("invalid padding")
	} else if (acc<<(toBits-bits))&maxv != 0 {
		return nil, fmt.Errorf("non-zero padding")
	}

	return result, nil
}

// AddCoinbaseWitness wraps a non-witness coinbase serialization with the segwit
// marker/flag and witness data needed for block submission.
func AddCoinbaseWitness(coinbase []byte) []byte {
	var buf bytes.Buffer
	// Version (first 4 bytes)
	buf.Write(coinbase[:4])
	// Segwit marker and flag
	buf.Write([]byte{0x00, 0x01})
	// Everything between version and locktime
	buf.Write(coinbase[4 : len(coinbase)-4])
	// Witness stack: 1 item of 32 zero bytes (witness nonce)
	buf.Write(util.WriteVarInt(1))
	buf.Write(util.WriteVarInt(32))
	buf.Write(make([]byte, 32))
	// Locktime (last 4 bytes)
	buf.Write(coinbase[len(coinbase)-4:])
	return buf.Bytes()
}

// BuildShareCommitment creates the sharechain commitment data for the coinbase scriptSig.
func BuildShareCommitment(prevShareHash [32]byte) []byte {
	tag := []byte(SharechainCommitmentTag)
	commitment := make([]byte, 0, len(tag)+32)
	commitment = append(commitment, tag...)
	commitment = append(commitment, prevShareHash[:]...)
	return commitment
}

// CoinbaseOutput represents a parsed coinbase transaction output.
type CoinbaseOutput struct {
	Value  int64
	Script []byte
}

// ExtractShareCommitment parses a serialized coinbase transaction and extracts
// the PrevShareHash from the scriptSig by searching for the "p2pool" tag.
func ExtractShareCommitment(coinbaseTx []byte) ([32]byte, error) {
	var zero [32]byte
	tag := []byte(SharechainCommitmentTag)

	// Skip version (4B)
	if len(coinbaseTx) < 4 {
		return zero, fmt.Errorf("coinbase too short for version")
	}
	pos := 4

	// Input count (varint, must be 1)
	inputCount, n, err := util.ReadVarInt(coinbaseTx[pos:])
	if err != nil {
		return zero, fmt.Errorf("read input count: %w", err)
	}
	pos += n
	if inputCount != 1 {
		return zero, fmt.Errorf("expected 1 coinbase input, got %d", inputCount)
	}

	// Previous outpoint: 32B hash + 4B index
	if pos+36 > len(coinbaseTx) {
		return zero, fmt.Errorf("coinbase too short for prev outpoint")
	}
	pos += 36

	// ScriptSig length + bytes
	scriptLen, n, err := util.ReadVarInt(coinbaseTx[pos:])
	if err != nil {
		return zero, fmt.Errorf("read scriptSig length: %w", err)
	}
	pos += n

	if pos+int(scriptLen) > len(coinbaseTx) {
		return zero, fmt.Errorf("coinbase too short for scriptSig")
	}
	scriptSig := coinbaseTx[pos : pos+int(scriptLen)]

	// Search for "p2pool" tag in scriptSig
	for i := 0; i <= len(scriptSig)-len(tag)-32; i++ {
		if bytes.Equal(scriptSig[i:i+len(tag)], tag) {
			var hash [32]byte
			copy(hash[:], scriptSig[i+len(tag):i+len(tag)+32])
			return hash, nil
		}
	}

	return zero, fmt.Errorf("sharechain commitment tag %q not found in scriptSig", SharechainCommitmentTag)
}

// ParseCoinbaseOutputs parses a serialized coinbase transaction and returns
// all outputs (value + scriptPubKey).
func ParseCoinbaseOutputs(coinbaseTx []byte) ([]CoinbaseOutput, error) {
	// Skip version (4B)
	if len(coinbaseTx) < 4 {
		return nil, fmt.Errorf("coinbase too short for version")
	}
	pos := 4

	// Input count (varint)
	inputCount, n, err := util.ReadVarInt(coinbaseTx[pos:])
	if err != nil {
		return nil, fmt.Errorf("read input count: %w", err)
	}
	pos += n
	if inputCount != 1 {
		return nil, fmt.Errorf("expected 1 coinbase input, got %d", inputCount)
	}

	// Previous outpoint (36B)
	if pos+36 > len(coinbaseTx) {
		return nil, fmt.Errorf("coinbase too short for prev outpoint")
	}
	pos += 36

	// ScriptSig length + bytes
	scriptLen, n, err := util.ReadVarInt(coinbaseTx[pos:])
	if err != nil {
		return nil, fmt.Errorf("read scriptSig length: %w", err)
	}
	pos += n

	if pos+int(scriptLen) > len(coinbaseTx) {
		return nil, fmt.Errorf("coinbase too short for scriptSig")
	}
	pos += int(scriptLen)

	// Sequence (4B)
	if pos+4 > len(coinbaseTx) {
		return nil, fmt.Errorf("coinbase too short for sequence")
	}
	pos += 4

	// Output count (varint)
	outputCount, n, err := util.ReadVarInt(coinbaseTx[pos:])
	if err != nil {
		return nil, fmt.Errorf("read output count: %w", err)
	}
	pos += n

	outputs := make([]CoinbaseOutput, 0, outputCount)
	for i := uint64(0); i < outputCount; i++ {
		// Value (8B LE int64)
		if pos+8 > len(coinbaseTx) {
			return nil, fmt.Errorf("coinbase too short for output %d value", i)
		}
		value := int64(binary.LittleEndian.Uint64(coinbaseTx[pos : pos+8]))
		pos += 8

		// ScriptPubKey length (varint)
		spkLen, n, err := util.ReadVarInt(coinbaseTx[pos:])
		if err != nil {
			return nil, fmt.Errorf("read output %d scriptPubKey length: %w", i, err)
		}
		pos += n

		if pos+int(spkLen) > len(coinbaseTx) {
			return nil, fmt.Errorf("coinbase too short for output %d scriptPubKey", i)
		}
		script := make([]byte, spkLen)
		copy(script, coinbaseTx[pos:pos+int(spkLen)])
		pos += int(spkLen)

		outputs = append(outputs, CoinbaseOutput{Value: value, Script: script})
	}

	return outputs, nil
}

// ValidateMinerInOutputs checks that at least one coinbase output pays to the
// given miner address.
func ValidateMinerInOutputs(outputs []CoinbaseOutput, minerAddress, network string) error {
	expectedScript, err := addressToScript(minerAddress, network)
	if err != nil {
		return fmt.Errorf("convert miner address to script: %w", err)
	}

	for _, out := range outputs {
		if bytes.Equal(out.Script, expectedScript) {
			return nil
		}
	}

	return fmt.Errorf("miner address %s not found in any coinbase output", minerAddress)
}

// ValidateAddress checks that the given address is valid for the specified network.
func ValidateAddress(address, network string) error {
	_, err := addressToScript(address, network)
	return err
}
