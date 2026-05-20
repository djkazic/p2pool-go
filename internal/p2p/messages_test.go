package p2p

import (
	"testing"
)

func TestShareMsg_RoundTrip(t *testing.T) {
	original := &ShareMsg{
		Type:            MsgTypeShare,
		Version:         536870912,
		Timestamp:       1700000000,
		Bits:            0x1d00ffff,
		Nonce:           12345,
		ShareVersion:    1,
		MinerAddress:    "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx",
		CoinbaseTx:      []byte{0x01, 0x02, 0x03},
		ShareTargetBits: 0x207fffff,
	}
	original.PrevShareHash[0] = 0xab

	data, err := Encode(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeShareMsg(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Version != original.Version {
		t.Errorf("version mismatch: %d != %d", decoded.Version, original.Version)
	}
	if decoded.MinerAddress != original.MinerAddress {
		t.Errorf("miner address mismatch")
	}
	if decoded.PrevShareHash[0] != 0xab {
		t.Errorf("prev share hash mismatch")
	}
	if decoded.ShareTargetBits != original.ShareTargetBits {
		t.Errorf("share target bits mismatch")
	}
}

func TestTipAnnounce_RoundTrip(t *testing.T) {
	original := &TipAnnounce{
		Type:      MsgTypeTipAnnounce,
		Height:    800000,
		TotalWork: []byte{0x01, 0x23, 0x45},
	}
	original.TipHash[0] = 0xcd

	data, err := Encode(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeTipAnnounce(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Height != 800000 {
		t.Errorf("height = %d, want 800000", decoded.Height)
	}
	if decoded.TipHash[0] != 0xcd {
		t.Errorf("tip hash mismatch")
	}
}

func TestShareRequest_RoundTrip(t *testing.T) {
	original := &ShareRequest{
		Type:  MsgTypeShareReq,
		Count: 50,
	}
	original.StartHash[0] = 0xef

	data, err := Encode(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeShareRequest(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Count != 50 {
		t.Errorf("count = %d, want 50", decoded.Count)
	}
	if decoded.StartHash[0] != 0xef {
		t.Errorf("start hash mismatch")
	}
}

func TestDecodeShareRequest_CountTooLarge(t *testing.T) {
	msg := &ShareRequest{Type: MsgTypeShareReq, Count: maxShareRequestCount + 1}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err = DecodeShareRequest(data)
	if err == nil {
		t.Fatal("expected error for oversized count")
	}
}

func TestDecodeShareRequest_NegativeCount(t *testing.T) {
	msg := &ShareRequest{Type: MsgTypeShareReq, Count: -1}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err = DecodeShareRequest(data)
	if err == nil {
		t.Fatal("expected error for negative count")
	}
}

func TestDecodeInvReq_MaxCountTooLarge(t *testing.T) {
	msg := &InvReq{Type: MsgTypeInvReq, MaxCount: maxInvCount + 1}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err = DecodeInvReq(data)
	if err == nil {
		t.Fatal("expected error for oversized MaxCount")
	}
}

func TestDecodeInvReq_TooManyLocators(t *testing.T) {
	locators := make([][32]byte, maxLocatorCount+1)
	msg := &InvReq{Type: MsgTypeInvReq, Locators: locators, MaxCount: 10}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err = DecodeInvReq(data)
	if err == nil {
		t.Fatal("expected error for oversized locator count")
	}
}

func TestDecodeDataReq_TooManyHashes(t *testing.T) {
	hashes := make([][32]byte, maxDataReqHashes+1)
	msg := &DataReq{Type: MsgTypeDataReq, Hashes: hashes}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err = DecodeDataReq(data)
	if err == nil {
		t.Fatal("expected error for oversized hash count")
	}
}

// TestDecodeInvResp_TooManyHashes asserts that an inv response advertising
// more than maxInvCount hashes is rejected at decode time, so a peer
// cannot drive the caller into hundreds of bogus DataReq round-trips.
func TestDecodeInvResp_TooManyHashes(t *testing.T) {
	hashes := make([][32]byte, maxInvCount+1)
	msg := &InvResp{Type: MsgTypeInvResp, Hashes: hashes}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err = DecodeInvResp(data)
	if err == nil {
		t.Fatal("expected error for oversized inv response hash count")
	}
}

// TestDecodeDataResp_TooManyShares ensures a peer cannot ship more shares
// than the protocol-wide per-request maximum, regardless of what the
// caller actually requested.
func TestDecodeDataResp_TooManyShares(t *testing.T) {
	shares := make([]ShareMsg, maxDataReqHashes+1)
	msg := &DataResp{Type: MsgTypeDataResp, Shares: shares}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err = DecodeDataResp(data)
	if err == nil {
		t.Fatal("expected error for oversized data response share count")
	}
}

// TestDecodeMode_RejectsArrayAboveCap confirms decMode enforces the
// MaxArrayElements cap directly. Every Decode* helper goes through this
// shared mode, so a bogus CBOR array claiming >100k elements is rejected
// at the codec layer before allocation — defence-in-depth beneath the
// per-message length checks above.
func TestDecodeMode_RejectsArrayAboveCap(t *testing.T) {
	type bigArray struct {
		Values []int32 `cbor:"1,keyasint"`
	}

	msg := bigArray{Values: make([]int32, 100_001)}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var out bigArray
	if err := decMode.Unmarshal(data, &out); err == nil {
		t.Fatal("expected error decoding array of 100,001 elements (cap is 100,000)")
	}
}

// TestDecodeMode_AcceptsArrayAtCap confirms the cap is inclusive: an array
// of exactly 100,000 elements decodes successfully (it's the rejection
// threshold itself we care about, not legitimate near-max arrays).
func TestDecodeMode_AcceptsArrayAtCap(t *testing.T) {
	type bigArray struct {
		Values []int32 `cbor:"1,keyasint"`
	}

	msg := bigArray{Values: make([]int32, 100_000)}
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var out bigArray
	if err := decMode.Unmarshal(data, &out); err != nil {
		t.Fatalf("expected success at the cap, got: %v", err)
	}
	if len(out.Values) != 100_000 {
		t.Errorf("decoded length = %d, want 100000", len(out.Values))
	}
}

func TestBigIntConversion(t *testing.T) {
	// Test with nil
	b := BigIntToBytes(nil)
	if b != nil {
		t.Error("nil input should give nil output")
	}

	result := BytesToBigInt(nil)
	if result.Sign() != 0 {
		t.Error("nil input should give zero")
	}

	// Test round trip
	original := BytesToBigInt([]byte{0x01, 0x00, 0x00})
	b = BigIntToBytes(original)
	result = BytesToBigInt(b)
	if result.Cmp(original) != 0 {
		t.Errorf("round trip failed: %s != %s", result, original)
	}
}
