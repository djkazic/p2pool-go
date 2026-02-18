package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"go.uber.org/zap"
)

// newTestHost creates a libp2p host on an ephemeral local port for testing.
func newTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create test host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// connectHosts connects host B to host A.
func connectHosts(t *testing.T, a, b host.Host) {
	t.Helper()
	aInfo := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Connect(ctx, aInfo); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}
}

// noopDataHandler returns an empty DataResp.
func noopDataHandler(req *DataReq) *DataResp {
	return &DataResp{Type: MsgTypeDataResp}
}

func TestInvProtocol_RoundTrip(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	// Host A serves 2 hash inventories
	hashC := [32]byte{0x0c}
	hashD := [32]byte{0x0d}
	NewSyncer(hostA, func(req *InvReq) *InvResp {
		return &InvResp{
			Type:   MsgTypeInvResp,
			Hashes: [][32]byte{hashC, hashD},
		}
	}, noopDataHandler, logger)

	syncerB := NewSyncer(hostB, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := syncerB.RequestInventory(ctx, hostA.ID(), nil, 10000)
	if err != nil {
		t.Fatalf("RequestInventory: %v", err)
	}

	if len(resp.Hashes) != 2 {
		t.Fatalf("expected 2 hashes, got %d", len(resp.Hashes))
	}

	if resp.Hashes[0] != hashC {
		t.Errorf("hash[0] = %x, want %x", resp.Hashes[0], hashC)
	}
	if resp.Hashes[1] != hashD {
		t.Errorf("hash[1] = %x, want %x", resp.Hashes[1], hashD)
	}
}

func TestInvProtocol_EmptyChain(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	NewSyncer(hostA, func(req *InvReq) *InvResp {
		return &InvResp{Type: MsgTypeInvResp}
	}, noopDataHandler, logger)

	syncerB := NewSyncer(hostB, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := syncerB.RequestInventory(ctx, hostA.ID(), nil, 10000)
	if err != nil {
		t.Fatalf("RequestInventory: %v", err)
	}

	if len(resp.Hashes) != 0 {
		t.Errorf("expected 0 hashes, got %d", len(resp.Hashes))
	}
}

func TestInvProtocol_MaxCountRejected(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	NewSyncer(hostA, func(req *InvReq) *InvResp {
		return &InvResp{Type: MsgTypeInvResp}
	}, noopDataHandler, logger)

	syncerB := NewSyncer(hostB, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Request more than maxInvCount — server rejects at decode
	_, err := syncerB.RequestInventory(ctx, hostA.ID(), nil, 50000)
	if err == nil {
		t.Fatal("expected error for oversized MaxCount, got nil")
	}
}

func TestDataProtocol_RoundTrip(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	hashX := [32]byte{0xaa}
	hashY := [32]byte{0xbb}

	// Host A serves full share data for known hashes
	shareDB := map[[32]byte]ShareMsg{
		hashX: {Type: MsgTypeShare, Version: 536870912, Nonce: 100, MinerAddress: "tb1qx"},
		hashY: {Type: MsgTypeShare, Version: 536870912, Nonce: 200, MinerAddress: "tb1qy"},
	}

	noopInv := func(req *InvReq) *InvResp { return &InvResp{Type: MsgTypeInvResp} }

	NewSyncer(hostA, noopInv, func(req *DataReq) *DataResp {
		var shares []ShareMsg
		for _, h := range req.Hashes {
			if s, ok := shareDB[h]; ok {
				shares = append(shares, s)
			}
		}
		return &DataResp{Type: MsgTypeDataResp, Shares: shares}
	}, logger)

	syncerB := NewSyncer(hostB, noopInv, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := syncerB.RequestData(ctx, hostA.ID(), [][32]byte{hashX, hashY})
	if err != nil {
		t.Fatalf("RequestData: %v", err)
	}

	if len(resp.Shares) != 2 {
		t.Fatalf("expected 2 shares, got %d", len(resp.Shares))
	}

	if resp.Shares[0].MinerAddress != "tb1qx" {
		t.Errorf("share[0] miner = %q, want tb1qx", resp.Shares[0].MinerAddress)
	}
	if resp.Shares[1].MinerAddress != "tb1qy" {
		t.Errorf("share[1] miner = %q, want tb1qy", resp.Shares[1].MinerAddress)
	}
}

func TestInvProtocol_LocatorForkPoint(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	hashA := [32]byte{0x01}
	hashB := [32]byte{0x02}
	hashC := [32]byte{0x03}
	hashD := [32]byte{0x04}

	mainChain := [][32]byte{hashA, hashB, hashC, hashD} // oldest-first

	// Host A: find fork point from locators, return hashes after it
	NewSyncer(hostA, func(req *InvReq) *InvResp {
		chainSet := make(map[[32]byte]int)
		for i, h := range mainChain {
			chainSet[h] = i
		}

		forkIdx := -1
		for _, loc := range req.Locators {
			if idx, ok := chainSet[loc]; ok {
				forkIdx = idx
				break
			}
		}

		startIdx := 0
		if forkIdx >= 0 {
			startIdx = forkIdx + 1
		}

		var hashes [][32]byte
		for i := startIdx; i < len(mainChain); i++ {
			hashes = append(hashes, mainChain[i])
		}

		return &InvResp{
			Type:   MsgTypeInvResp,
			Hashes: hashes,
		}
	}, noopDataHandler, logger)

	syncerB := NewSyncer(hostB, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Client sends locator [B] — should get [C, D] back
	resp, err := syncerB.RequestInventory(ctx, hostA.ID(), [][32]byte{hashB}, 10000)
	if err != nil {
		t.Fatalf("RequestInventory: %v", err)
	}

	if len(resp.Hashes) != 2 {
		t.Fatalf("expected 2 hashes (C, D), got %d", len(resp.Hashes))
	}

	if resp.Hashes[0] != hashC {
		t.Errorf("hash[0] = %x, want %x", resp.Hashes[0], hashC)
	}
	if resp.Hashes[1] != hashD {
		t.Errorf("hash[1] = %x, want %x", resp.Hashes[1], hashD)
	}
}

func TestDataProtocol_BatchSizeRejected(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	noopInv := func(req *InvReq) *InvResp { return &InvResp{Type: MsgTypeInvResp} }

	NewSyncer(hostA, noopInv, func(req *DataReq) *DataResp {
		return &DataResp{Type: MsgTypeDataResp}
	}, logger)

	syncerB := NewSyncer(hostB, noopInv, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send more hashes than maxDataReqHashes — server rejects at decode
	hashes := make([][32]byte, 200)
	for i := range hashes {
		hashes[i][0] = byte(i)
		hashes[i][1] = byte(i >> 8)
	}

	_, err := syncerB.RequestData(ctx, hostA.ID(), hashes)
	if err == nil {
		t.Fatal("expected error for oversized hash count, got nil")
	}
}

func TestInvProtocol_MoreFlag(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	NewSyncer(hostA, func(req *InvReq) *InvResp {
		return &InvResp{
			Type:   MsgTypeInvResp,
			Hashes: [][32]byte{{0x01}},
			More:   true,
		}
	}, noopDataHandler, logger)

	syncerB := NewSyncer(hostB, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := syncerB.RequestInventory(ctx, hostA.ID(), nil, 1)
	if err != nil {
		t.Fatalf("RequestInventory: %v", err)
	}

	if !resp.More {
		t.Error("expected More=true")
	}
	if len(resp.Hashes) != 1 {
		t.Errorf("expected 1 hash, got %d", len(resp.Hashes))
	}
}

func TestInvProtocol_NilHandler(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	NewSyncer(hostA, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	syncerB := NewSyncer(hostB, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := syncerB.RequestInventory(ctx, hostA.ID(), nil, 100)
	if err != nil {
		t.Fatalf("RequestInventory: %v", err)
	}

	if len(resp.Hashes) != 0 {
		t.Errorf("expected 0 hashes from nil handler, got %d", len(resp.Hashes))
	}
}

func TestDataProtocol_EmptyRequest(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	noopInv := func(req *InvReq) *InvResp { return &InvResp{Type: MsgTypeInvResp} }

	NewSyncer(hostA, noopInv, func(req *DataReq) *DataResp {
		return &DataResp{Type: MsgTypeDataResp}
	}, logger)

	syncerB := NewSyncer(hostB, noopInv, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := syncerB.RequestData(ctx, hostA.ID(), nil)
	if err != nil {
		t.Fatalf("RequestData: %v", err)
	}

	if len(resp.Shares) != 0 {
		t.Errorf("expected 0 shares, got %d", len(resp.Shares))
	}
}

func TestInvProtocol_LocatorRejected(t *testing.T) {
	logger := zap.NewNop()

	hostA := newTestHost(t)
	hostB := newTestHost(t)

	NewSyncer(hostA, func(req *InvReq) *InvResp {
		return &InvResp{Type: MsgTypeInvResp}
	}, noopDataHandler, logger)

	syncerB := NewSyncer(hostB, func(req *InvReq) *InvResp {
		return nil
	}, noopDataHandler, logger)

	connectHosts(t, hostA, hostB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send more locators than maxLocatorCount — server rejects at decode
	locators := make([][32]byte, 100)
	for i := range locators {
		locators[i][0] = byte(i)
	}

	_, err := syncerB.RequestInventory(ctx, hostA.ID(), locators, 100)
	if err == nil {
		t.Fatal("expected error for oversized locator count, got nil")
	}
}
