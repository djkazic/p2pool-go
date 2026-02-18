package node

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/djkazic/p2pool-go/internal/bitcoin"
	"github.com/djkazic/p2pool-go/internal/config"
	"github.com/djkazic/p2pool-go/internal/metrics"
	"github.com/djkazic/p2pool-go/internal/p2p"
	"github.com/djkazic/p2pool-go/internal/pplns"
	"github.com/djkazic/p2pool-go/internal/sharechain"
	"github.com/djkazic/p2pool-go/internal/stratum"
	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/internal/web"
	"github.com/djkazic/p2pool-go/internal/work"
	"github.com/djkazic/p2pool-go/pkg/util"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

const syncBatchSize = 100

// stratumDiff1Target is the "pool difficulty 1" target used to convert
// stratum difficulty values to hash targets. Corresponds to compact 0x1d00ffff.
var stratumDiff1Target = util.CompactToTarget(0x1d00ffff)

// Node is the top-level orchestrator for a p2pool node.
type Node struct {
	config *config.Config
	logger *zap.Logger

	bitcoinRPC bitcoin.BitcoinRPC
	store      sharechain.ShareStore
	chain      *sharechain.ShareChain
	pplnsCalc  *pplns.Calculator
	stratumSrv *stratum.Server
	workGen    *work.Generator
	p2pNode    *p2p.Node

	minerAddress string

	// Sync
	syncMu sync.Mutex

	// Reorg tracking: skip duplicate EventNewTip after reorg
	lastReorgTip [32]byte

	// Diagnostics
	shareRejectCount uint64
	startTime        time.Time

	// Local hashrate tracking (rolling window of valid stratum shares)
	localShares   []localShareEvent
	localSharesMu sync.Mutex

	// Last Bitcoin block found by the pool
	lastBlockTime time.Time
	lastBlockHash string
	lastBlockMu   sync.RWMutex

	// Dashboard graph history (ring buffer, recorded every status tick)
	graphHistory   []web.HistoryPoint
	graphHistoryMu sync.Mutex

	cancel context.CancelFunc
}

// localShareEvent records a valid stratum share for hashrate estimation.
type localShareEvent struct {
	time       time.Time
	difficulty float64 // stratum difficulty (Bitcoin-standard)
}

// NewNode creates a new p2pool node.
func NewNode(cfg *config.Config, minerAddress string, logger *zap.Logger) *Node {
	return &Node{
		config:       cfg,
		logger:       logger,
		minerAddress: minerAddress,
	}
}

// Start initializes and starts all subsystems.
func (n *Node) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	n.cancel = cancel

	// Bitcoin RPC
	n.bitcoinRPC = bitcoin.NewRPCClient(
		n.config.BitcoinRPCURL(),
		n.config.BitcoinRPCUser,
		n.config.BitcoinRPCPassword,
	)

	// Verify bitcoin connection
	height, err := n.bitcoinRPC.GetBlockCount(ctx)
	if err != nil {
		return fmt.Errorf("bitcoin RPC connection failed: %w", err)
	}
	n.logger.Info("connected to bitcoind", zap.Int64("height", height))

	// Sharechain
	if err := os.MkdirAll(n.config.DataDir, 0700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	store, err := sharechain.NewBoltStore(filepath.Join(n.config.DataDir, "sharechain.db"), n.logger)
	if err != nil {
		return fmt.Errorf("open sharechain store: %w", err)
	}
	n.store = store
	diffCalc := sharechain.NewDifficultyCalculator(n.config.ShareTargetTime)
	n.chain = sharechain.NewShareChain(store, diffCalc, n.config.PPLNSWindowSize, n.config.BitcoinNetwork, n.logger)

	if err := n.chain.ValidateLoaded(); err != nil {
		return fmt.Errorf("sharechain validation failed: %w", err)
	}

	// Scan chain for the most recent Bitcoin block found
	n.initLastBlock()

	// PPLNS Calculator
	n.pplnsCalc = pplns.NewCalculator(n.config.FinderFeePercent, n.config.DustThresholdSats)

	// Stratum Server
	n.stratumSrv = stratum.NewServer(n.config.StartDifficulty, n.logger)
	n.startTime = time.Now()

	// Web dashboard (served on the same port as stratum)
	webHandler := web.NewHandler(n.dashboardData, n.lookupShare)
	n.stratumSrv.SetHTTPHandler(webHandler)

	if err := n.stratumSrv.Start(fmt.Sprintf("0.0.0.0:%d", n.config.StratumPort)); err != nil {
		return fmt.Errorf("stratum server: %w", err)
	}

	// Work Generator
	n.workGen = work.NewGenerator(
		n.bitcoinRPC,
		n.config.BitcoinNetwork,
		8, // extranonce1 (4 bytes) + extranonce2 (4 bytes)
		n.getPayouts,
		n.getPrevShareHash,
		n.logger,
	)
	n.workGen.Start(ctx)

	// P2P Node — create host and register handlers before discovery starts
	n.p2pNode, err = p2p.NewNode(ctx, n.config.P2PPort, n.config.DataDir, n.logger)
	if err != nil {
		return fmt.Errorf("p2p node: %w", err)
	}

	// Register sync protocol BEFORE discovery so peers can't connect
	// before the handler is ready (fixes "protocols not supported" race)
	n.p2pNode.InitSyncer(n.handleLocatorRequest)

	// Now start discovery — peers will find us with all handlers registered
	allBootnodes := append(config.DefaultBootnodes(n.config.BitcoinNetwork), n.config.P2PBootnodes...)
	if err := n.p2pNode.StartDiscovery(ctx, n.config.EnableMDNS, allBootnodes); err != nil {
		return fmt.Errorf("p2p discovery: %w", err)
	}

	// Start event loop
	go n.eventLoop(ctx)

	n.logger.Info("p2pool node started",
		zap.String("miner_address", n.minerAddress),
		zap.Int("stratum_port", n.config.StratumPort),
		zap.Int("p2p_port", n.config.P2PPort),
	)

	return nil
}

// Stop gracefully stops all subsystems.
func (n *Node) Stop() {
	n.logger.Info("shutting down p2pool node...")

	if n.cancel != nil {
		n.cancel()
	}
	if n.stratumSrv != nil {
		n.stratumSrv.Stop()
	}
	if n.p2pNode != nil {
		n.p2pNode.Close()
	}
	if n.store != nil {
		n.store.Close()
	}

	n.logger.Info("p2pool node stopped")
}

// eventLoop is the central orchestrator select loop.
func (n *Node) eventLoop(ctx context.Context) {
	chainEvents := n.chain.Subscribe(ctx)
	defer n.chain.Unsubscribe(chainEvents)

	statusTicker := time.NewTicker(30 * time.Second)
	defer statusTicker.Stop()

	pruneTicker := time.NewTicker(5 * time.Minute)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		// New job from work generator (new block template)
		case job := <-n.workGen.JobChannel():
			n.handleNewJob(job)

		// Share submission from stratum miner
		case submission := <-n.stratumSrv.SubmitChannel():
			n.handleSubmission(submission)

		// Share from P2P network
		case shareMsg := <-n.p2pNode.IncomingShares():
			n.handleP2PShare(shareMsg)

		// Sharechain events (new tip, new block, reorg)
		case event := <-chainEvents:
			n.handleChainEvent(event)

		// New peer connected — trigger sync
		case peerID := <-n.p2pNode.PeerConnected():
			go n.syncFromPeer(ctx, peerID)

		// Periodic status log
		case <-statusTicker.C:
			n.logStatus()

		// Periodic orphan and old-share pruning
		case <-pruneTicker.C:
			if pruned := n.chain.PruneOrphans(); pruned > 0 {
				n.logger.Info("pruned orphan shares", zap.Int("count", pruned))
			}
			n.chain.PruneOldShares(n.config.PPLNSWindowSize * 2)
		}
	}
}

func (n *Node) handleNewJob(job *work.JobData) {
	stratumJob := &stratum.Job{
		ID:             job.ID,
		PrevHash:       job.PrevBlockHash,
		Coinbase1:      job.Coinbase1,
		Coinbase2:      job.Coinbase2,
		MerkleBranches: job.MerkleBranches,
		Version:        job.Version,
		NBits:          job.NBits,
		NTime:          job.NTime,
		CleanJobs:      job.CleanJobs,
	}

	n.stratumSrv.BroadcastJob(stratumJob)
	n.logger.Debug("broadcast job",
		zap.String("job_id", job.ID),
		zap.Int64("height", job.Height),
		zap.Bool("clean", job.CleanJobs),
	)
}

func (n *Node) handleSubmission(sub *stratum.ShareSubmission) {
	n.logger.Debug("share submission",
		zap.String("worker", sub.WorkerName),
		zap.String("job", sub.JobID),
		zap.String("nonce", sub.Nonce),
	)

	// 1. Look up the job
	job := n.workGen.GetJob(sub.JobID)
	if job == nil {
		n.logger.Debug("rejected share: stale or unknown job", zap.String("job_id", sub.JobID))
		return
	}

	// 2. Compute the actual block version (apply BIP 310 version rolling if used)
	version := job.Version
	if sub.VersionBits != "" {
		version = applyVersionRolling(job.Version, sub.VersionBits)
	}

	// 3. Reconstruct the block header and coinbase from the submission
	header, coinbaseBytes, err := work.ReconstructHeader(
		job,
		version,
		sub.Extranonce1,
		sub.Extranonce2,
		sub.NTime,
		sub.Nonce,
	)
	if err != nil {
		n.logger.Warn("failed to reconstruct header from submission", zap.Error(err))
		return
	}

	// 4. Hash the header (double-SHA256)
	headerHash := util.DoubleSHA256(header)

	// 5. Check against stratum difficulty (per-miner target).
	// After a vardiff retarget the miner may still be working at the old
	// difficulty, so accept shares meeting either current or previous.
	stratumTarget := stratumDiffToTarget(sub.Difficulty)
	meetsTarget := util.HashMeetsTarget(headerHash, stratumTarget)
	if !meetsTarget && sub.PrevDifficulty > 0 && sub.PrevDifficulty != sub.Difficulty {
		prevTarget := stratumDiffToTarget(sub.PrevDifficulty)
		meetsTarget = util.HashMeetsTarget(headerHash, prevTarget)
	}
	if !meetsTarget {
		n.shareRejectCount++
		metrics.SharesRejected.Inc()
		if n.shareRejectCount == 1 || n.shareRejectCount%1000 == 0 {
			n.logger.Info("share below stratum difficulty (possible header reconstruction mismatch)",
				zap.String("worker", sub.WorkerName),
				zap.Float64("difficulty", sub.Difficulty),
				zap.String("hash", util.HashToHex(headerHash)),
				zap.String("version", version),
				zap.String("version_bits", sub.VersionBits),
				zap.String("nonce", sub.Nonce),
				zap.String("extranonce1", sub.Extranonce1),
				zap.String("extranonce2", sub.Extranonce2),
				zap.String("ntime", sub.NTime),
				zap.String("job_id", sub.JobID),
				zap.Uint64("total_rejected", n.shareRejectCount),
			)
		}
		return
	}
	n.logger.Debug("valid stratum share",
		zap.String("worker", sub.WorkerName),
		zap.String("hash", util.HashToHex(headerHash)),
	)

	// Record for local hashrate estimation
	metrics.SharesAccepted.Inc()
	n.recordLocalShare(sub.Difficulty)

	// 6. Check against sharechain difficulty.
	// Use the share's actual parent (from coinbase commitment) rather than the
	// current tip — the tip may have moved since job creation, especially during
	// rapid difficulty ramps where many shares are added per second.
	prevShareHash, err := types.ExtractShareCommitment(coinbaseBytes)
	if err != nil {
		n.logger.Warn("failed to extract share commitment for target check", zap.Error(err))
		return
	}
	shareTarget := n.chain.GetExpectedTargetForParent(prevShareHash)
	if !util.HashMeetsTarget(headerHash, shareTarget) {
		return // Valid stratum share but doesn't meet sharechain difficulty
	}

	// This share meets the sharechain target - add to chain and broadcast
	share := n.buildShareFromHeader(header, coinbaseBytes, shareTarget, job)
	if share == nil {
		return
	}
	if err := n.chain.AddShare(share); err != nil {
		n.logger.Warn("failed to add local share to chain", zap.Error(err))
		return
	}

	n.logger.Debug("sharechain share found",
		zap.String("hash", util.HashToHex(headerHash)),
		zap.String("miner", sub.WorkerName),
		zap.Int64("height", job.Height),
	)

	// Broadcast via P2P
	n.p2pNode.BroadcastShare(shareToP2PMsg(share))

	// 7. Check against Bitcoin network difficulty
	btcTarget := util.CompactToTarget(share.Header.Bits)
	if util.HashMeetsTarget(headerHash, btcTarget) {
		hashHex := util.HashToHex(headerHash)
		n.logger.Info("BITCOIN BLOCK FOUND!",
			zap.String("hash", hashHex),
			zap.String("miner", sub.WorkerName),
			zap.Int64("height", job.Height),
		)
		metrics.BlocksFound.Inc()
		n.recordBlockFound(hashHex)
		n.submitBlock(header, coinbaseBytes, job.Template)
	}
}

func (n *Node) handleP2PShare(msg *p2p.ShareMsg) {
	share := p2pShareToShare(msg)
	if err := n.chain.AddShare(share); err != nil {
		n.logger.Debug("rejected P2P share", zap.Error(err))
		return
	}
	n.logger.Debug("accepted P2P share", zap.String("hash", share.HashHex()))
}

func (n *Node) handleChainEvent(event sharechain.Event) {
	switch event.Type {
	case sharechain.EventNewTip:
		// Skip if this tip was already handled by a reorg event
		tipHash := event.Share.Hash()
		if n.lastReorgTip != ([32]byte{}) && tipHash == n.lastReorgTip {
			n.lastReorgTip = [32]byte{}
			return
		}

		// Regenerate jobs when the chain tip changes
		job, err := n.workGen.GenerateJob()
		if err != nil {
			n.logger.Error("failed to generate job after new tip", zap.Error(err))
			return
		}
		n.handleNewJob(job)

	case sharechain.EventNewBlock:
		// Block submission is handled in handleSubmission() for locally-mined shares.
		// P2P-received blocks are submitted by their original finder's node.
		n.logger.Info("block confirmed on sharechain",
			zap.String("hash", event.Share.HashHex()),
			zap.String("miner", event.Share.MinerAddress),
		)
		n.recordBlockFound(event.Share.HashHex())

	case sharechain.EventReorg:
		n.logger.Warn("sharechain reorg detected",
			zap.String("old_tip", util.HashToHex(event.OldTipHash)),
			zap.String("new_tip", event.Share.HashHex()),
			zap.Int("reorg_depth", event.ReorgDepth),
			zap.String("miner", event.Share.MinerAddress),
		)

		// Generate a clean job so miners abandon stale work immediately
		job, err := n.workGen.GenerateJob()
		if err != nil {
			n.logger.Error("failed to generate job after reorg", zap.Error(err))
			return
		}
		job.CleanJobs = true
		n.handleNewJob(job)

		// Track this tip so we skip the subsequent EventNewTip
		n.lastReorgTip = event.Share.Hash()
	}
}

// handleLocatorRequest serves share data to a peer performing locator-based sync.
func (n *Node) handleLocatorRequest(req *p2p.ShareLocatorReq) *p2p.ShareLocatorResp {
	tip, ok := n.chain.Tip()
	if !ok {
		return &p2p.ShareLocatorResp{Type: p2p.MsgTypeLocatorResp}
	}

	tipHash := tip.Hash()

	// Get all ancestors of our tip (tip → genesis order)
	ancestors := n.chain.GetAncestors(tipHash, n.chain.Count())

	// Build a set of main-chain hashes for fast lookup
	mainSet := make(map[[32]byte]int, len(ancestors))
	for i, s := range ancestors {
		mainSet[s.Hash()] = i
	}

	// Find the first locator hash that exists on our main chain (fork point)
	forkIdx := -1
	for _, loc := range req.Locators {
		if idx, found := mainSet[loc]; found {
			forkIdx = idx
			break
		}
	}

	// ancestors is tip→genesis (index 0=tip, len-1=genesis).
	// We want shares AFTER the fork point, in oldest-first order.
	//
	// forkIdx == -1 → no locator matched → send all shares from genesis
	// forkIdx == 0  → locator matched the tip → peer is in sync
	// forkIdx == N  → N shares exist between fork point and tip
	var afterFork int
	if forkIdx < 0 {
		// No locator matched — peer has nothing, send everything
		afterFork = len(ancestors)
	} else {
		afterFork = forkIdx
	}

	if afterFork == 0 {
		// Fork point is the tip — peer is already in sync
		return &p2p.ShareLocatorResp{Type: p2p.MsgTypeLocatorResp}
	}

	maxCount := req.MaxCount
	if maxCount <= 0 {
		maxCount = syncBatchSize
	}

	// Build oldest-first slice from ancestors
	more := false
	count := afterFork
	if count > maxCount {
		count = maxCount
		more = true
	}

	shares := make([]p2p.ShareMsg, count)
	for i := 0; i < count; i++ {
		// ancestors[afterFork-1] is oldest after fork, ancestors[0] is tip
		shares[i] = *shareToP2PMsg(ancestors[afterFork-1-i])
	}

	return &p2p.ShareLocatorResp{
		Type:   p2p.MsgTypeLocatorResp,
		Shares: shares,
		More:   more,
	}
}

// buildLocator builds an exponentially-spaced list of share hashes from our
// chain tip, used for locator-based sync. Returns hashes at positions:
// tip, tip-1, tip-2, ..., tip-9, tip-11, tip-15, tip-23, ..., genesis.
func (n *Node) buildLocator() [][32]byte {
	tip, ok := n.chain.Tip()
	if !ok {
		return nil
	}

	tipHash := tip.Hash()
	ancestors := n.chain.GetAncestors(tipHash, n.chain.Count())
	if len(ancestors) == 0 {
		return nil
	}

	// ancestors[0] = tip, ancestors[len-1] = genesis
	var locators [][32]byte
	step := 1
	idx := 0
	for idx < len(ancestors) {
		locators = append(locators, ancestors[idx].Hash())
		// First 10 hashes at step 1, then double
		if len(locators) >= 10 {
			step *= 2
		}
		idx += step
	}

	// Always include genesis if not already included
	genesisHash := ancestors[len(ancestors)-1].Hash()
	if len(locators) == 0 || locators[len(locators)-1] != genesisHash {
		locators = append(locators, genesisHash)
	}

	return locators
}

// syncFromPeer performs locator-based sharechain sync from a connected peer.
// Only one sync runs at a time (protected by syncMu).
func (n *Node) syncFromPeer(ctx context.Context, peerID peer.ID) {
	if !n.syncMu.TryLock() {
		return // another sync is already running
	}
	defer n.syncMu.Unlock()

	syncer := n.p2pNode.Syncer()
	if syncer == nil {
		return
	}

	n.logger.Info("starting sharechain sync", zap.String("peer", peerID.String()))

	totalAdded := 0
	totalSkipped := 0

	for {
		locators := n.buildLocator()

		resp, err := syncer.RequestLocator(ctx, peerID, locators, syncBatchSize)
		if err != nil {
			n.logger.Warn("sync request failed", zap.Error(err), zap.String("peer", peerID.String()))
			break
		}

		if len(resp.Shares) == 0 {
			break
		}

		// Process response: shares are already in oldest-first order
		added := 0
		skipped := 0
		for _, msg := range resp.Shares {
			share := p2pShareToShare(&msg)
			if _, known := n.chain.GetShare(share.Hash()); known {
				skipped++
				continue
			}
			if err := n.chain.AddShareQuiet(share); err != nil {
				n.logger.Debug("sync: rejected share", zap.Error(err))
				continue
			}
			added++
		}

		totalAdded += added
		totalSkipped += skipped

		if !resp.More {
			break
		}

		// More shares available — rebuild locator from updated chain and repeat
	}

	n.logger.Info("sync complete",
		zap.Int("new_shares", totalAdded),
		zap.Int("already_known", totalSkipped),
		zap.String("peer", peerID.String()),
	)

	// Trigger a single work regeneration now that the chain tip has changed
	if totalAdded > 0 {
		if job, err := n.workGen.GenerateJob(); err != nil {
			n.logger.Error("failed to generate job after sync", zap.Error(err))
		} else {
			n.handleNewJob(job)
		}
	}
}

const maxGraphHistory = 60

func (n *Node) logStatus() {
	target := n.chain.GetExpectedTarget()
	difficulty := util.TargetToDifficulty(target, sharechain.MinShareTarget)

	shareCount := n.chain.Count()
	minerCount := n.stratumSrv.SessionCount()
	peerCount := n.p2pNode.PeerCount()
	poolHR := n.dashboardPoolHashrate()

	n.logger.Info("status",
		zap.Int("shares", shareCount),
		zap.Int("miners", minerCount),
		zap.Int("peers", peerCount),
		zap.String("target", fmt.Sprintf("0x%08x", util.TargetToCompact(target))),
		zap.Float64("difficulty", difficulty),
	)

	// Update Prometheus gauges
	metrics.SharechainHeight.Set(float64(shareCount))
	metrics.MinersConnected.Set(float64(minerCount))
	metrics.PeersConnected.Set(float64(peerCount))
	metrics.ShareDifficulty.Set(difficulty)
	metrics.PoolHashrate.Set(poolHR)
	metrics.LocalHashrate.Set(n.localHashrate())
	metrics.UptimeSeconds.Set(time.Since(n.startTime).Seconds())

	// Record graph history point
	n.recordGraphPoint(poolHR, shareCount)
}

func (n *Node) recordGraphPoint(poolHashrate float64, shareCount int) {
	n.graphHistoryMu.Lock()
	defer n.graphHistoryMu.Unlock()
	n.graphHistory = append(n.graphHistory, web.HistoryPoint{
		Timestamp:    time.Now().Unix(),
		PoolHashrate: poolHashrate,
		ShareCount:   shareCount,
	})
	if len(n.graphHistory) > maxGraphHistory {
		n.graphHistory = n.graphHistory[len(n.graphHistory)-maxGraphHistory:]
	}
}

func (n *Node) getGraphHistory() []web.HistoryPoint {
	n.graphHistoryMu.Lock()
	defer n.graphHistoryMu.Unlock()
	out := make([]web.HistoryPoint, len(n.graphHistory))
	copy(out, n.graphHistory)
	return out
}

const localHashrateWindow = 10 * time.Minute

// recordLocalShare records a valid stratum share for local hashrate tracking.
func (n *Node) recordLocalShare(difficulty float64) {
	n.localSharesMu.Lock()
	defer n.localSharesMu.Unlock()
	n.localShares = append(n.localShares, localShareEvent{
		time:       time.Now(),
		difficulty: difficulty,
	})
	// Prune events older than the window
	cutoff := time.Now().Add(-localHashrateWindow)
	i := 0
	for i < len(n.localShares) && n.localShares[i].time.Before(cutoff) {
		i++
	}
	if i > 0 {
		n.localShares = n.localShares[i:]
	}
}

// localHashrate computes the local hashrate (H/s) from recent stratum shares.
func (n *Node) localHashrate() float64 {
	n.localSharesMu.Lock()
	defer n.localSharesMu.Unlock()
	if len(n.localShares) < 2 {
		return 0
	}
	var totalDiff float64
	for _, e := range n.localShares {
		totalDiff += e.difficulty
	}
	elapsed := n.localShares[len(n.localShares)-1].time.Sub(n.localShares[0].time).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return totalDiff * math.Pow(2, 32) / elapsed
}

// initLastBlock scans the sharechain from tip to find the most recent Bitcoin block.
func (n *Node) initLastBlock() {
	tip, ok := n.chain.Tip()
	if !ok {
		return
	}
	ancestors := n.chain.GetAncestors(tip.Hash(), n.chain.Count())
	for _, s := range ancestors {
		if s.MeetsBitcoinTarget() {
			n.lastBlockHash = s.HashHex()
			n.lastBlockTime = time.Unix(int64(s.Header.Timestamp), 0)
			n.logger.Info("loaded last block found from sharechain",
				zap.String("hash", s.HashHex()),
				zap.Time("time", n.lastBlockTime),
			)
			return
		}
	}
}

// recordBlockFound records the most recent Bitcoin block found by the pool.
func (n *Node) recordBlockFound(hashHex string) {
	n.lastBlockMu.Lock()
	defer n.lastBlockMu.Unlock()
	n.lastBlockTime = time.Now()
	n.lastBlockHash = hashHex
}

// dashboardData collects all metrics for the web dashboard.
func (n *Node) dashboardData() *web.StatusData {
	target := n.chain.GetExpectedTarget()
	difficulty := util.TargetToDifficulty(target, sharechain.MinShareTarget)

	var tipHash, tipMiner string
	var tipTime int64
	var recentShares []web.ShareInfo
	var pplnsAncestors []*types.Share
	if tip, ok := n.chain.Tip(); ok {
		tipHash = tip.HashHex()
		tipMiner = tip.MinerAddress
		tipTime = int64(tip.Header.Timestamp)
		// Single walk for both recent shares and PPLNS window
		pplnsAncestors = n.chain.GetAncestors(tip.Hash(), n.config.PPLNSWindowSize)
		recentCount := 20
		if recentCount > len(pplnsAncestors) {
			recentCount = len(pplnsAncestors)
		}
		for _, s := range pplnsAncestors[:recentCount] {
			recentShares = append(recentShares, web.ShareInfo{
				Hash:      s.HashHex(),
				Miner:     s.MinerAddress,
				Timestamp: int64(s.Header.Timestamp),
				IsBlock:   s.IsBlock(),
			})
		}
	}

	minerWeights := make(map[string]float64)
	if len(pplnsAncestors) > 0 {
		window := pplns.NewWindow(pplnsAncestors, sharechain.MaxShareTarget)
		weights := window.MinerWeights()
		totalWeight := window.TotalWeight()
		if totalWeight.Sign() > 0 {
			totalF := new(big.Float).SetInt(totalWeight)
			for addr, w := range weights {
				wF := new(big.Float).SetInt(w)
				pct, _ := new(big.Float).Quo(wF, totalF).Float64()
				minerWeights[addr] = pct * 100
			}
		}
	}

	poolHashrate := poolHashrateFromShares(pplnsAncestors)
	shareCount := n.chain.Count()

	// Record a graph history point on each dashboard poll
	n.recordGraphPoint(poolHashrate, shareCount)

	// Estimated time to find a Bitcoin block
	var estTimeToBlock int64
	if poolHashrate > 0 {
		tmpl := n.workGen.CurrentTemplate()
		if tmpl != nil {
			var btcBits uint32
			fmt.Sscanf(tmpl.Bits, "%x", &btcBits)
			btcTarget := util.CompactToTarget(btcBits)
			diff1 := util.CompactToTarget(0x1d00ffff)
			btcDiff, _ := new(big.Float).Quo(
				new(big.Float).SetInt(diff1),
				new(big.Float).SetInt(btcTarget),
			).Float64()
			estTimeToBlock = int64(btcDiff * math.Pow(2, 32) / poolHashrate)
		}
	}

	// Last block found
	n.lastBlockMu.RLock()
	var lastBlockTime int64
	lastBlockHash := n.lastBlockHash
	if !n.lastBlockTime.IsZero() {
		lastBlockTime = n.lastBlockTime.Unix()
	}
	n.lastBlockMu.RUnlock()

	return &web.StatusData{
		ShareCount:         shareCount,
		MinerCount:         n.stratumSrv.SessionCount(),
		PeerCount:          n.p2pNode.PeerCount(),
		Difficulty:         difficulty,
		TargetBits:         fmt.Sprintf("0x%08x", util.TargetToCompact(target)),
		TipHash:            tipHash,
		TipMiner:           tipMiner,
		TipTime:            tipTime,
		RecentShares:       recentShares,
		MinerWeights:       minerWeights,
		Network:            n.config.BitcoinNetwork,
		StratumPort:        n.config.StratumPort,
		P2PPort:            n.config.P2PPort,
		ShareTargetTime:    int(n.config.ShareTargetTime.Seconds()),
		PPLNSWindowSize:    n.config.PPLNSWindowSize,
		Uptime:             int64(time.Since(n.startTime).Seconds()),
		PoolHashrate:       poolHashrate,
		LocalHashrate:      n.localHashrate(),
		LastBlockFoundTime: lastBlockTime,
		LastBlockFoundHash: lastBlockHash,
		EstTimeToBlock:     estTimeToBlock,
		History:            n.getGraphHistory(),
	}
}

// lookupShare returns full details for a share by its display-order hex hash.
func (n *Node) lookupShare(hashHex string) *web.ShareDetail {
	h, err := util.HexToHash(hashHex)
	if err != nil {
		return nil
	}
	share, ok := n.chain.GetShare(h)
	if !ok {
		return nil
	}

	var diffStr string
	if share.ShareTarget != nil && share.ShareTarget.Sign() > 0 {
		diff := util.TargetToDifficulty(share.ShareTarget, sharechain.MinShareTarget)
		diffStr = fmt.Sprintf("%.2f", diff)
	}

	return &web.ShareDetail{
		Hash:          share.HashHex(),
		Miner:         share.MinerAddress,
		Timestamp:     int64(share.Header.Timestamp),
		IsBlock:       share.IsBlock(),
		Version:       share.Header.Version,
		PrevBlockHash: util.HashToHex(share.Header.PrevBlockHash),
		MerkleRoot:    util.HashToHex(share.Header.MerkleRoot),
		Bits:          share.Header.Bits,
		Nonce:         share.Header.Nonce,
		PrevShareHash: share.PrevShareHashHex(),
		ShareVersion:  share.ShareVersion,
		Difficulty:    diffStr,
	}
}

// poolHashrateFromShares computes pool hashrate from a slice of shares.
func poolHashrateFromShares(shares []*types.Share) float64 {
	if len(shares) < 2 {
		return 0
	}
	diff1 := util.CompactToTarget(0x1d00ffff)
	diff1Float := new(big.Float).SetInt(diff1)
	var totalWork float64
	for _, s := range shares {
		if s.ShareTarget != nil && s.ShareTarget.Sign() > 0 {
			shareDiff, _ := new(big.Float).Quo(
				diff1Float,
				new(big.Float).SetInt(s.ShareTarget),
			).Float64()
			totalWork += shareDiff
		}
	}
	newest := shares[0].Header.Timestamp
	oldest := shares[len(shares)-1].Header.Timestamp
	elapsed := float64(newest - oldest)
	if elapsed <= 0 {
		return 0
	}
	return totalWork * math.Pow(2, 32) / elapsed
}

// dashboardPoolHashrate estimates pool hashrate from the PPLNS window.
// Used by logStatus where pplnsAncestors aren't already loaded.
func (n *Node) dashboardPoolHashrate() float64 {
	tip, ok := n.chain.Tip()
	if !ok {
		return 0
	}
	return poolHashrateFromShares(n.chain.GetAncestors(tip.Hash(), n.config.PPLNSWindowSize))
}

// getPayouts returns the current PPLNS payouts for the coinbase.
func (n *Node) getPayouts() []types.PayoutEntry {
	tip, ok := n.chain.Tip()
	if !ok {
		// No shares yet, all reward to our miner
		return []types.PayoutEntry{
			{Address: n.minerAddress, Amount: 5000000000}, // placeholder
		}
	}

	tipHash := tip.Hash()
	ancestors := n.chain.GetAncestors(tipHash, n.config.PPLNSWindowSize)
	maxTarget := sharechain.MaxShareTarget
	window := pplns.NewWindow(ancestors, maxTarget)

	// Use the current template's coinbase value
	tmpl := n.workGen.CurrentTemplate()
	totalReward := int64(5000000000) // fallback
	if tmpl != nil {
		totalReward = tmpl.CoinbaseValue
	}

	return n.pplnsCalc.CalculatePayouts(window, totalReward, n.minerAddress)
}

// getPrevShareHash returns the current chain tip hash for the sharechain commitment.
func (n *Node) getPrevShareHash() [32]byte {
	tip, ok := n.chain.Tip()
	if !ok {
		return [32]byte{}
	}
	return tip.Hash()
}

// p2pShareToShare converts a P2P share message to a types.Share.
func p2pShareToShare(msg *p2p.ShareMsg) *types.Share {
	return &types.Share{
		Header: types.ShareHeader{
			Version:       msg.Version,
			PrevBlockHash: msg.PrevBlockHash,
			MerkleRoot:    msg.MerkleRoot,
			Timestamp:     msg.Timestamp,
			Bits:          msg.Bits,
			Nonce:         msg.Nonce,
		},
		ShareVersion:  msg.ShareVersion,
		PrevShareHash: msg.PrevShareHash,
		ShareTarget:   util.CompactToTarget(msg.ShareTargetBits),
		MinerAddress:  msg.MinerAddress,
		CoinbaseTx:    msg.CoinbaseTx,
	}
}

// shareToP2PMsg converts a types.Share to a P2P share message.
func shareToP2PMsg(share *types.Share) *p2p.ShareMsg {
	var shareTargetBits uint32
	if share.ShareTarget != nil && share.ShareTarget.Sign() > 0 {
		shareTargetBits = util.TargetToCompact(share.ShareTarget)
	}

	return &p2p.ShareMsg{
		Type:            p2p.MsgTypeShare,
		Version:         share.Header.Version,
		PrevBlockHash:   share.Header.PrevBlockHash,
		MerkleRoot:      share.Header.MerkleRoot,
		Timestamp:       share.Header.Timestamp,
		Bits:            share.Header.Bits,
		Nonce:           share.Header.Nonce,
		ShareVersion:    share.ShareVersion,
		PrevShareHash:   share.PrevShareHash,
		ShareTargetBits: shareTargetBits,
		MinerAddress:    share.MinerAddress,
		CoinbaseTx:      share.CoinbaseTx,
	}
}

// ShareTarget returns the current sharechain difficulty target.
func (n *Node) ShareTarget() *big.Int {
	return n.chain.GetExpectedTarget()
}

// buildShareFromHeader constructs a types.Share from a reconstructed 80-byte
// block header and the associated job data.
func (n *Node) buildShareFromHeader(header []byte, coinbase []byte, shareTarget *big.Int, job *work.JobData) *types.Share {
	var sh types.ShareHeader
	sh.Version = int32(binary.LittleEndian.Uint32(header[0:4]))
	copy(sh.PrevBlockHash[:], header[4:36])
	copy(sh.MerkleRoot[:], header[36:68])
	sh.Timestamp = binary.LittleEndian.Uint32(header[68:72])
	sh.Bits = binary.LittleEndian.Uint32(header[72:76])
	sh.Nonce = binary.LittleEndian.Uint32(header[76:80])

	// Extract PrevShareHash from the coinbase commitment rather than
	// re-querying the chain tip, which may have moved since the job was built.
	prevShareHash, err := types.ExtractShareCommitment(coinbase)
	if err != nil {
		n.logger.Warn("failed to extract share commitment from coinbase", zap.Error(err))
		return nil
	}

	return &types.Share{
		Header:        sh,
		ShareVersion:  1,
		PrevShareHash: prevShareHash,
		ShareTarget:   shareTarget,
		MinerAddress:  n.minerAddress,
		CoinbaseTx:    coinbase,
	}
}

// submitBlock reconstructs the full block from the header, coinbase, and
// the job's block template transactions, then submits it to bitcoind.
func (n *Node) submitBlock(header []byte, coinbase []byte, tmpl *bitcoin.BlockTemplate) {
	// Pre-submission verification: independently compute the merkle root
	// and compare with the header's merkle root to catch any issues early.
	if err := work.VerifyMerkleRoot(header, coinbase, tmpl); err != nil {
		n.logger.Error("MERKLE ROOT VERIFICATION FAILED — block will likely be rejected", zap.Error(err))
		// Still attempt submission so we can see bitcoind's response
	} else {
		n.logger.Info("merkle root verification passed")
	}

	blockHex, err := work.ReconstructBlock(header, coinbase, tmpl)
	if err != nil {
		n.logger.Error("failed to reconstruct block for submission", zap.Error(err))
		return
	}

	// Retry with exponential backoff on transient RPC errors.
	// Don't retry on explicit block rejections (consensus failure).
	const maxRetries = 3
	delay := 1 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := n.bitcoinRPC.SubmitBlock(ctx, blockHex)
		cancel()

		if err == nil {
			n.logger.Info("block submitted to Bitcoin network successfully")
			metrics.BlockSubmissions.WithLabelValues("success").Inc()
			return
		}

		// Don't retry if the network explicitly rejected the block
		var rejected *bitcoin.BlockRejectedError
		if errors.As(err, &rejected) {
			n.logger.Error("block rejected by network (not retrying)", zap.Error(err))
			metrics.BlockSubmissions.WithLabelValues("rejected").Inc()
			return
		}

		if attempt < maxRetries {
			n.logger.Warn("block submission RPC failed, retrying",
				zap.Error(err),
				zap.Int("attempt", attempt+1),
				zap.Duration("retry_in", delay),
			)
			time.Sleep(delay)
			delay *= 2
		} else {
			n.logger.Error("block submission failed after all retries", zap.Error(err))
			metrics.BlockSubmissions.WithLabelValues("failed").Inc()
		}
	}
}


// applyVersionRolling computes the actual block version by merging the miner's
// rolled version bits into the original job version using the BIP 310 mask.
// Both jobVersion and versionBits are big-endian hex strings (e.g., "20000000").
func applyVersionRolling(jobVersion, versionBits string) string {
	var orig, rolled uint32
	fmt.Sscanf(jobVersion, "%x", &orig)
	fmt.Sscanf(versionBits, "%x", &rolled)

	const mask uint32 = 0x1fffe000 // VersionRollingMask
	actual := (orig &^ mask) | (rolled & mask)
	return fmt.Sprintf("%08x", actual)
}

// stratumDiffToTarget converts a stratum difficulty value to a target hash.
// stratum_target = diff1_target / difficulty
func stratumDiffToTarget(difficulty float64) *big.Int {
	if difficulty <= 0 {
		return new(big.Int).Set(stratumDiff1Target)
	}
	diff1Float := new(big.Float).SetInt(stratumDiff1Target)
	diffFloat := new(big.Float).SetFloat64(difficulty)
	targetFloat := new(big.Float).Quo(diff1Float, diffFloat)
	target, _ := targetFloat.Int(nil)
	return target
}
