package p2p

import (
	"fmt"
	"math/big"

	"github.com/fxamacker/cbor/v2"
)

const (
	// maxP2PCoinbaseTxSize is the maximum coinbase tx size accepted from P2P peers.
	maxP2PCoinbaseTxSize = 100 * 1024 // 100KB
	// maxP2PMinerAddressLen is the maximum miner address length accepted from P2P peers.
	maxP2PMinerAddressLen = 128
	// maxShareRequestCount is the maximum number of shares a peer can request at once.
	maxShareRequestCount = 500
	// maxLocatorCount is the maximum number of locator hashes in an InvReq.
	maxLocatorCount = 64
	// maxInvCount is the maximum number of hashes an InvReq can request.
	maxInvCount = 10000
	// maxDataReqHashes is the maximum number of hashes in a DataReq.
	maxDataReqHashes = 100
)

const (
	// ProtocolVersion is the current P2P protocol version.
	ProtocolVersion = "1.0.0"

	// ShareTopicName is the GossipSub topic for share propagation.
	ShareTopicName = "/p2pool/shares/" + ProtocolVersion

	// SyncProtocolID is the protocol ID for initial sync.
	// Version 3.0.0: inv-based sync (hash discovery + targeted download).
	SyncProtocolID = "/p2pool/sync/3.0.0"

	// DataProtocolID is the protocol ID for hash-targeted share downloads.
	DataProtocolID = "/p2pool/data/1.0.0"
)

// MessageType identifies the type of P2P message.
type MessageType uint8

const (
	MsgTypeShare       MessageType = 1
	MsgTypeTipAnnounce MessageType = 2
	MsgTypeShareReq    MessageType = 3
	MsgTypeShareResp   MessageType = 4
	MsgTypeInvReq      MessageType = 7
	MsgTypeInvResp     MessageType = 8
	MsgTypeDataReq     MessageType = 9
	MsgTypeDataResp    MessageType = 10
)

// ShareMsg is a share broadcast via GossipSub.
type ShareMsg struct {
	Type MessageType `cbor:"1,keyasint"`

	// Share header fields (Bitcoin block header)
	Version       int32    `cbor:"2,keyasint"`
	PrevBlockHash [32]byte `cbor:"3,keyasint"`
	MerkleRoot    [32]byte `cbor:"4,keyasint"`
	Timestamp     uint32   `cbor:"5,keyasint"`
	Bits          uint32   `cbor:"6,keyasint"`
	Nonce         uint32   `cbor:"7,keyasint"`

	// Sharechain-specific fields
	ShareVersion    uint32   `cbor:"8,keyasint"`
	PrevShareHash   [32]byte `cbor:"9,keyasint"`
	ShareTargetBits uint32   `cbor:"10,keyasint"` // Compact representation of share target
	MinerAddress    string   `cbor:"11,keyasint"`
	CoinbaseTx      []byte   `cbor:"12,keyasint"`
}

// TipAnnounce announces a node's current chain tip.
type TipAnnounce struct {
	Type      MessageType `cbor:"1,keyasint"`
	TipHash   [32]byte    `cbor:"2,keyasint"`
	Height    int64       `cbor:"3,keyasint"`
	TotalWork []byte      `cbor:"4,keyasint"` // big.Int bytes
}

// ShareRequest requests a batch of shares by hash.
type ShareRequest struct {
	Type      MessageType `cbor:"1,keyasint"`
	StartHash [32]byte    `cbor:"2,keyasint"` // Walk backwards from here
	Count     int         `cbor:"3,keyasint"`
}

// ShareResponse contains a batch of shares.
type ShareResponse struct {
	Type   MessageType `cbor:"1,keyasint"`
	Shares []ShareMsg  `cbor:"2,keyasint"`
}

// Encode serializes a message to CBOR.
func Encode(msg interface{}) ([]byte, error) {
	return cbor.Marshal(msg)
}

// DecodeShareMsg decodes a CBOR-encoded ShareMsg.
func DecodeShareMsg(data []byte) (*ShareMsg, error) {
	var msg ShareMsg
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	if len(msg.CoinbaseTx) > maxP2PCoinbaseTxSize {
		return nil, fmt.Errorf("coinbase tx too large: %d bytes", len(msg.CoinbaseTx))
	}
	if len(msg.MinerAddress) > maxP2PMinerAddressLen {
		return nil, fmt.Errorf("miner address too long: %d bytes", len(msg.MinerAddress))
	}
	return &msg, nil
}

// DecodeTipAnnounce decodes a CBOR-encoded TipAnnounce.
func DecodeTipAnnounce(data []byte) (*TipAnnounce, error) {
	var msg TipAnnounce
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// DecodeShareRequest decodes a CBOR-encoded ShareRequest.
func DecodeShareRequest(data []byte) (*ShareRequest, error) {
	var msg ShareRequest
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	if msg.Count < 0 || msg.Count > maxShareRequestCount {
		return nil, fmt.Errorf("share request count out of range: %d", msg.Count)
	}
	return &msg, nil
}

// DecodeShareResponse decodes a CBOR-encoded ShareResponse.
func DecodeShareResponse(data []byte) (*ShareResponse, error) {
	var msg ShareResponse
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// InvReq requests a hash inventory from a peer using locators.
type InvReq struct {
	Type     MessageType `cbor:"1,keyasint"`
	Locators [][32]byte  `cbor:"2,keyasint"`
	MaxCount int         `cbor:"3,keyasint"`
}

// InvResp returns share hashes from the fork point forward.
type InvResp struct {
	Type   MessageType `cbor:"1,keyasint"`
	Hashes [][32]byte  `cbor:"2,keyasint"`
	More   bool        `cbor:"3,keyasint"`
}

// DataReq requests full share data by hash.
type DataReq struct {
	Type   MessageType `cbor:"1,keyasint"`
	Hashes [][32]byte  `cbor:"2,keyasint"`
}

// DataResp returns full share data for requested hashes.
type DataResp struct {
	Type   MessageType `cbor:"1,keyasint"`
	Shares []ShareMsg  `cbor:"2,keyasint"`
}

// DecodeInvReq decodes a CBOR-encoded InvReq.
func DecodeInvReq(data []byte) (*InvReq, error) {
	var msg InvReq
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	if len(msg.Locators) > maxLocatorCount {
		return nil, fmt.Errorf("inv request locator count too large: %d", len(msg.Locators))
	}
	if msg.MaxCount < 0 || msg.MaxCount > maxInvCount {
		return nil, fmt.Errorf("inv request max count out of range: %d", msg.MaxCount)
	}
	return &msg, nil
}

// DecodeInvResp decodes a CBOR-encoded InvResp.
func DecodeInvResp(data []byte) (*InvResp, error) {
	var msg InvResp
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// DecodeDataReq decodes a CBOR-encoded DataReq.
func DecodeDataReq(data []byte) (*DataReq, error) {
	var msg DataReq
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	if len(msg.Hashes) > maxDataReqHashes {
		return nil, fmt.Errorf("data request hash count too large: %d", len(msg.Hashes))
	}
	return &msg, nil
}

// DecodeDataResp decodes a CBOR-encoded DataResp.
func DecodeDataResp(data []byte) (*DataResp, error) {
	var msg DataResp
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// BigIntToBytes converts a big.Int to bytes for CBOR encoding.
func BigIntToBytes(n *big.Int) []byte {
	if n == nil {
		return nil
	}
	return n.Bytes()
}

// BytesToBigInt converts bytes back to a big.Int.
func BytesToBigInt(b []byte) *big.Int {
	if len(b) == 0 {
		return new(big.Int)
	}
	return new(big.Int).SetBytes(b)
}
