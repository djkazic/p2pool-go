package p2p

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"go.uber.org/zap"
)

const (
	maxSyncMsgSize    = 1024 * 1024 // 1MB
	syncStreamTimeout = 30 * time.Second
)

// InvHandler handles inventory requests (locators → hash list).
type InvHandler func(req *InvReq) *InvResp

// DataHandler handles data requests (hashes → full shares).
type DataHandler func(req *DataReq) *DataResp

// Syncer handles initial sharechain synchronization using inv-based protocol.
type Syncer struct {
	host        host.Host
	logger      *zap.Logger
	invHandler  InvHandler
	dataHandler DataHandler
}

// NewSyncer creates a new sync handler with inv-based and data protocols.
func NewSyncer(h host.Host, invHandler InvHandler, dataHandler DataHandler, logger *zap.Logger) *Syncer {
	s := &Syncer{
		host:        h,
		logger:      logger,
		invHandler:  invHandler,
		dataHandler: dataHandler,
	}

	h.SetStreamHandler(protocol.ID(SyncProtocolID), s.handleSyncStream)
	h.SetStreamHandler(protocol.ID(DataProtocolID), s.handleDataStream)

	return s
}

// handleSyncStream handles incoming inv requests (sync/3.0.0).
func (s *Syncer) handleSyncStream(stream network.Stream) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(syncStreamTimeout))

	data, err := io.ReadAll(io.LimitReader(stream, maxSyncMsgSize))
	if err != nil {
		s.logger.Debug("sync read error", zap.Error(err))
		return
	}

	req, err := DecodeInvReq(data)
	if err != nil {
		s.logger.Debug("invalid inv request", zap.Error(err))
		return
	}

	resp := s.invHandler(req)
	if resp == nil {
		resp = &InvResp{Type: MsgTypeInvResp}
	}

	data, err = Encode(resp)
	if err != nil {
		s.logger.Error("encode inv response", zap.Error(err))
		return
	}

	stream.Write(data)
}

// handleDataStream handles incoming data requests (data/1.0.0).
func (s *Syncer) handleDataStream(stream network.Stream) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(syncStreamTimeout))

	data, err := io.ReadAll(io.LimitReader(stream, maxSyncMsgSize))
	if err != nil {
		s.logger.Debug("data read error", zap.Error(err))
		return
	}

	req, err := DecodeDataReq(data)
	if err != nil {
		s.logger.Debug("invalid data request", zap.Error(err))
		return
	}

	resp := s.dataHandler(req)
	if resp == nil {
		resp = &DataResp{Type: MsgTypeDataResp}
	}

	data, err = Encode(resp)
	if err != nil {
		s.logger.Error("encode data response", zap.Error(err))
		return
	}

	stream.Write(data)
}

// RequestInventory sends an inv request to a peer and returns the hash list.
func (s *Syncer) RequestInventory(ctx context.Context, peerID peer.ID, locators [][32]byte, maxCount int) (*InvResp, error) {
	stream, err := s.host.NewStream(ctx, peerID, protocol.ID(SyncProtocolID))
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	req := &InvReq{
		Type:     MsgTypeInvReq,
		Locators: locators,
		MaxCount: maxCount,
	}

	data, err := Encode(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	if _, err := stream.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	stream.CloseWrite()

	data, err = io.ReadAll(io.LimitReader(stream, maxSyncMsgSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	resp, err := DecodeInvResp(data)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return resp, nil
}

// RequestData sends a data request to a peer and returns full share data.
func (s *Syncer) RequestData(ctx context.Context, peerID peer.ID, hashes [][32]byte) (*DataResp, error) {
	stream, err := s.host.NewStream(ctx, peerID, protocol.ID(DataProtocolID))
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	req := &DataReq{
		Type:   MsgTypeDataReq,
		Hashes: hashes,
	}

	data, err := Encode(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	if _, err := stream.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	stream.CloseWrite()

	data, err = io.ReadAll(io.LimitReader(stream, maxSyncMsgSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	resp, err := DecodeDataResp(data)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return resp, nil
}
