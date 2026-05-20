package p2p

import (
	"bytes"
	"testing"
)

func TestDecompressCoinbase_RoundTrip(t *testing.T) {
	original := bytes.Repeat([]byte("hello world "), 100) // ~1.2 KB
	compressed := CompressCoinbase(original)

	decompressed, err := DecompressCoinbase(compressed)
	if err != nil {
		t.Fatalf("DecompressCoinbase failed: %v", err)
	}
	if !bytes.Equal(decompressed, original) {
		t.Errorf("round-trip mismatch: got %d bytes, want %d", len(decompressed), len(original))
	}
}

func TestDecompressCoinbase_PassthroughNonZstd(t *testing.T) {
	// Non-zstd input must be returned as-is (forward-compat with uncompressed shares).
	data := []byte{0x01, 0x02, 0x03}
	out, err := DecompressCoinbase(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("passthrough mismatch: got %x, want %x", out, data)
	}
}

// TestDecompressCoinbase_BombRejected is the regression test for the
// decompression amplification: a small zstd payload that decodes to more
// than the 128 KB cap (WithDecoderMaxMemory) must error out rather than
// returning a huge buffer to the caller.
func TestDecompressCoinbase_BombRejected(t *testing.T) {
	// 256 KB of zeros compresses to ~30 bytes with zstd.
	bomb := CompressCoinbase(make([]byte, 256*1024))
	if len(bomb) > 1024 {
		t.Fatalf("test setup: bomb payload unexpectedly large: %d bytes", len(bomb))
	}

	_, err := DecompressCoinbase(bomb)
	if err == nil {
		t.Fatal("expected error decoding payload that exceeds 128 KB cap, got nil")
	}
}
