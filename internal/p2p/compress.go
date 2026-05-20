package p2p

import (
	"github.com/klauspost/compress/zstd"
)

var (
	zstdEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	// Cap decoded output at 128 KB. Real coinbases are 1–4 KB and the
	// validator rejects anything over 8 KB (maxCoinbaseTxSize), so 128 KB
	// still has plenty of headroom; the cap exists to prevent a ~100-byte
	// zstd payload of zeros expanding to gigabytes before the validator
	// runs. Tightening further (e.g. to 16 KB) is a reasonable follow-up.
	zstdDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(128*1024))
)

// CompressCoinbase compresses coinbase transaction bytes using zstd.
func CompressCoinbase(data []byte) []byte {
	return zstdEncoder.EncodeAll(data, nil)
}

// DecompressCoinbase decompresses coinbase transaction bytes.
// If the data does not start with the zstd magic bytes, it is returned as-is
// for forward compatibility with uncompressed shares.
func DecompressCoinbase(data []byte) ([]byte, error) {
	if len(data) < 4 || data[0] != 0x28 || data[1] != 0xB5 || data[2] != 0x2F || data[3] != 0xFD {
		return data, nil
	}
	return zstdDecoder.DecodeAll(data, nil)
}
