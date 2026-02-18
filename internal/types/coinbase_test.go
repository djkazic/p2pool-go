package types

import (
	"bytes"
	"testing"
)

func TestSerializeHeight(t *testing.T) {
	tests := []struct {
		height int64
		minLen int
	}{
		{0, 2},
		{1, 2},
		{16, 2},
		{17, 2},
		{255, 2},
		{256, 3},
		{800000, 4},
	}

	for _, tt := range tests {
		result := serializeHeight(tt.height)
		if len(result) < tt.minLen {
			t.Errorf("serializeHeight(%d) length = %d, want >= %d", tt.height, len(result), tt.minLen)
		}
		// First byte should be the length of the following bytes
		if tt.height > 16 {
			dataLen := int(result[0])
			if dataLen != len(result)-1 {
				t.Errorf("serializeHeight(%d): length prefix %d != actual %d", tt.height, dataLen, len(result)-1)
			}
		}
	}
}

func TestBech32AddressToScript(t *testing.T) {
	// P2WPKH testnet address
	script, err := bech32AddressToScript("tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// P2WPKH script should be: OP_0 <20 bytes>
	if len(script) != 22 {
		t.Errorf("script length = %d, want 22", len(script))
	}
	if script[0] != 0x00 {
		t.Errorf("script[0] = %x, want 0x00 (OP_0)", script[0])
	}
	if script[1] != 20 {
		t.Errorf("script[1] = %d, want 20 (push 20 bytes)", script[1])
	}
}

func TestBuildCoinbase(t *testing.T) {
	builder := NewCoinbaseBuilder("testnet3")

	payouts := []PayoutEntry{
		{Address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", Amount: 5000000000},
	}

	commitment := BuildShareCommitment([32]byte{})

	tx, extranonceOffset, err := builder.BuildCoinbase(
		800000,
		commitment,
		payouts,
		"", // no witness commitment for simplicity
		8,  // 8 byte extranonce
	)
	if err != nil {
		t.Fatalf("BuildCoinbase failed: %v", err)
	}

	if len(tx) < 100 {
		t.Errorf("coinbase tx too short: %d bytes", len(tx))
	}

	if extranonceOffset <= 0 || extranonceOffset >= len(tx) {
		t.Errorf("invalid extranonce offset: %d (tx len: %d)", extranonceOffset, len(tx))
	}
}

func TestBuildShareCommitment(t *testing.T) {
	var hash [32]byte
	hash[0] = 0xab
	hash[31] = 0xcd

	commitment := BuildShareCommitment(hash)
	if len(commitment) != len(SharechainCommitmentTag)+32 {
		t.Errorf("commitment length = %d, want %d", len(commitment), len(SharechainCommitmentTag)+32)
	}

	// Check tag
	tag := string(commitment[:len(SharechainCommitmentTag)])
	if tag != SharechainCommitmentTag {
		t.Errorf("tag = %s, want %s", tag, SharechainCommitmentTag)
	}

	// Check hash
	if commitment[len(SharechainCommitmentTag)] != 0xab {
		t.Error("hash not correctly embedded")
	}
}

// buildTestCoinbase is a helper that builds a coinbase with the given prevShareHash and miner address.
func buildTestCoinbase(t *testing.T, prevShareHash [32]byte, minerAddr string) []byte {
	t.Helper()
	builder := NewCoinbaseBuilder("testnet3")
	commitment := BuildShareCommitment(prevShareHash)
	payouts := []PayoutEntry{
		{Address: minerAddr, Amount: 5000000000},
	}
	tx, _, err := builder.BuildCoinbase(800000, commitment, payouts, "", 8)
	if err != nil {
		t.Fatalf("BuildCoinbase failed: %v", err)
	}
	return tx
}

func TestExtractShareCommitment(t *testing.T) {
	var prevShareHash [32]byte
	prevShareHash[0] = 0xde
	prevShareHash[15] = 0xad
	prevShareHash[31] = 0xef

	tx := buildTestCoinbase(t, prevShareHash, "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")

	extracted, err := ExtractShareCommitment(tx)
	if err != nil {
		t.Fatalf("ExtractShareCommitment failed: %v", err)
	}
	if extracted != prevShareHash {
		t.Errorf("extracted hash = %x, want %x", extracted, prevShareHash)
	}
}

func TestExtractShareCommitment_Missing(t *testing.T) {
	// Build a coinbase without the sharechain commitment (empty commitment)
	builder := NewCoinbaseBuilder("testnet3")
	payouts := []PayoutEntry{
		{Address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", Amount: 5000000000},
	}
	tx, _, err := builder.BuildCoinbase(800000, nil, payouts, "", 8)
	if err != nil {
		t.Fatalf("BuildCoinbase failed: %v", err)
	}

	_, err = ExtractShareCommitment(tx)
	if err == nil {
		t.Error("expected error for missing commitment")
	}
}

func TestParseCoinbaseOutputs(t *testing.T) {
	builder := NewCoinbaseBuilder("testnet3")
	commitment := BuildShareCommitment([32]byte{})
	payouts := []PayoutEntry{
		{Address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", Amount: 3000000000},
		{Address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", Amount: 2000000000},
	}
	tx, _, err := builder.BuildCoinbase(800000, commitment, payouts, "", 8)
	if err != nil {
		t.Fatalf("BuildCoinbase failed: %v", err)
	}

	outputs, err := ParseCoinbaseOutputs(tx)
	if err != nil {
		t.Fatalf("ParseCoinbaseOutputs failed: %v", err)
	}
	if len(outputs) != 2 {
		t.Fatalf("got %d outputs, want 2", len(outputs))
	}
	if outputs[0].Value != 3000000000 {
		t.Errorf("output[0].Value = %d, want 3000000000", outputs[0].Value)
	}
	if outputs[1].Value != 2000000000 {
		t.Errorf("output[1].Value = %d, want 2000000000", outputs[1].Value)
	}
	// Each output should have a P2WPKH script (OP_0 <20 bytes> = 22 bytes)
	for i, out := range outputs {
		if len(out.Script) != 22 {
			t.Errorf("output[%d].Script length = %d, want 22", i, len(out.Script))
		}
	}
}

func TestParseCoinbaseOutputs_Malformed(t *testing.T) {
	// Truncated data
	_, err := ParseCoinbaseOutputs([]byte{0x01, 0x00})
	if err == nil {
		t.Error("expected error for malformed coinbase")
	}
}

func TestValidateMinerInOutputs(t *testing.T) {
	minerAddr := "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx"
	tx := buildTestCoinbase(t, [32]byte{}, minerAddr)

	outputs, err := ParseCoinbaseOutputs(tx)
	if err != nil {
		t.Fatalf("ParseCoinbaseOutputs failed: %v", err)
	}

	err = ValidateMinerInOutputs(outputs, minerAddr, "testnet3")
	if err != nil {
		t.Errorf("ValidateMinerInOutputs failed: %v", err)
	}
}

func TestValidateMinerInOutputs_Missing(t *testing.T) {
	// Build coinbase paying to one address
	tx := buildTestCoinbase(t, [32]byte{}, "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")

	outputs, err := ParseCoinbaseOutputs(tx)
	if err != nil {
		t.Fatalf("ParseCoinbaseOutputs failed: %v", err)
	}

	// Validate with a different address — construct a different scriptPubKey manually
	// Use a different valid testnet address
	differentAddr := "tb1qqqqqp399et2xygdj5xreqhjjvcmzhxw4aywxecjdzew6hylgvsesrxh6hy"
	err = ValidateMinerInOutputs(outputs, differentAddr, "testnet3")
	if err == nil {
		t.Error("expected error when miner address not in outputs")
	}
}

func TestValidateAddress(t *testing.T) {
	// Valid testnet address
	err := ValidateAddress("tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", "testnet3")
	if err != nil {
		t.Errorf("ValidateAddress failed for valid address: %v", err)
	}

	// Valid testnet P2WSH address
	err = ValidateAddress("tb1qqqqqp399et2xygdj5xreqhjjvcmzhxw4aywxecjdzew6hylgvsesrxh6hy", "testnet3")
	if err != nil {
		t.Errorf("ValidateAddress failed for valid P2WSH address: %v", err)
	}

	// Valid mainnet address
	err = ValidateAddress("bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "mainnet")
	if err != nil {
		t.Errorf("ValidateAddress failed for valid mainnet address: %v", err)
	}

	// Invalid checksum (last char changed)
	err = ValidateAddress("tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx"[:len("tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")-1]+"q", "testnet3")
	if err == nil {
		t.Error("expected error for invalid bech32 checksum")
	}

	// Invalid address
	err = ValidateAddress("not-an-address", "testnet3")
	if err == nil {
		t.Error("expected error for invalid address")
	}

	// Wrong network (mainnet address on testnet)
	err = ValidateAddress("bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "testnet3")
	if err == nil {
		t.Error("expected error for wrong network address")
	}
}

func TestExtractShareCommitment_ZeroHash(t *testing.T) {
	// A zero PrevShareHash (genesis) should still be extractable
	var zeroHash [32]byte
	tx := buildTestCoinbase(t, zeroHash, "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")

	extracted, err := ExtractShareCommitment(tx)
	if err != nil {
		t.Fatalf("ExtractShareCommitment failed: %v", err)
	}
	if extracted != zeroHash {
		t.Errorf("extracted hash = %x, want zero hash", extracted)
	}
}

func TestParseCoinbaseOutputs_WithWitnessCommitment(t *testing.T) {
	builder := NewCoinbaseBuilder("testnet3")
	commitment := BuildShareCommitment([32]byte{})
	payouts := []PayoutEntry{
		{Address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", Amount: 5000000000},
	}
	// Use a dummy witness commitment (OP_RETURN script)
	wcHex := "6a24aa21a9ed" + "e2f61c3f71d1defd3fa999dfa36953755c690689799962b48bebd836974e8cf9"
	tx, _, err := builder.BuildCoinbase(800000, commitment, payouts, wcHex, 8)
	if err != nil {
		t.Fatalf("BuildCoinbase failed: %v", err)
	}

	outputs, err := ParseCoinbaseOutputs(tx)
	if err != nil {
		t.Fatalf("ParseCoinbaseOutputs failed: %v", err)
	}
	if len(outputs) != 2 {
		t.Fatalf("got %d outputs, want 2 (payout + witness commitment)", len(outputs))
	}
	// Second output should be the OP_RETURN witness commitment (value 0)
	if outputs[1].Value != 0 {
		t.Errorf("witness commitment output value = %d, want 0", outputs[1].Value)
	}
	// Should start with OP_RETURN (0x6a)
	if !bytes.HasPrefix(outputs[1].Script, []byte{0x6a}) {
		t.Error("witness commitment output should start with OP_RETURN")
	}
}
