package work

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/djkazic/p2pool-go/internal/bitcoin"
	"github.com/djkazic/p2pool-go/internal/types"
	"github.com/djkazic/p2pool-go/pkg/util"

	"go.uber.org/zap"
)

const (
	// PollInterval is how often to check for new block templates.
	PollInterval = 5 * time.Second

	// JobRefreshInterval is how often to send a non-clean job refresh
	// to keep miners connected and give them updated timestamps/transactions.
	JobRefreshInterval = 30 * time.Second
)

const maxStoredJobs = 20

// Generator produces mining jobs from block templates.
type Generator struct {
	rpc    bitcoin.BitcoinRPC
	logger *zap.Logger

	network        string
	extranonceSize int

	currentTemplate *bitcoin.BlockTemplate
	templateMu      sync.RWMutex

	jobCounter atomic.Uint64
	jobCh      chan *JobData

	// Recent jobs stored for share validation lookups
	jobs   map[string]*JobData
	jobsMu sync.RWMutex

	payoutsFn       func() []types.PayoutEntry
	prevShareHashFn func() [32]byte

	lastJobTime time.Time
}

// NewGenerator creates a new work generator.
func NewGenerator(
	rpc bitcoin.BitcoinRPC,
	network string,
	extranonceSize int,
	payoutsFn func() []types.PayoutEntry,
	prevShareHashFn func() [32]byte,
	logger *zap.Logger,
) *Generator {
	return &Generator{
		rpc:             rpc,
		logger:          logger,
		network:         network,
		extranonceSize:  extranonceSize,
		jobCh:           make(chan *JobData, 8),
		jobs:            make(map[string]*JobData),
		payoutsFn:       payoutsFn,
		prevShareHashFn: prevShareHashFn,
	}
}

// Start begins polling for block templates.
func (g *Generator) Start(ctx context.Context) {
	go g.pollLoop(ctx)
}

// JobChannel returns the channel of new jobs.
func (g *Generator) JobChannel() <-chan *JobData {
	return g.jobCh
}

// CurrentTemplate returns the current block template.
func (g *Generator) CurrentTemplate() *bitcoin.BlockTemplate {
	g.templateMu.RLock()
	defer g.templateMu.RUnlock()
	return g.currentTemplate
}

// GenerateJob creates a new job from the current template.
func (g *Generator) GenerateJob() (*JobData, error) {
	g.templateMu.RLock()
	tmpl := g.currentTemplate
	g.templateMu.RUnlock()

	if tmpl == nil {
		return nil, fmt.Errorf("no block template available")
	}

	payouts := g.payoutsFn()
	prevShareHash := g.prevShareHashFn()

	// Convert template to internal format
	tmplData := &types.BlockTemplateData{
		Height:            tmpl.Height,
		PrevBlockHash:     tmpl.PreviousBlockHash,
		Version:           fmt.Sprintf("%08x", tmpl.Version),
		Bits:              tmpl.Bits,
		CurTime:           fmt.Sprintf("%08x", tmpl.CurTime),
		CoinbaseValue:     tmpl.CoinbaseValue,
		WitnessCommitment: tmpl.DefaultWitnessCommitment,
		Network:           g.network,
		TxHashes:          extractTxHashes(tmpl),
	}

	seq := g.jobCounter.Add(1)
	jobID := fmt.Sprintf("%x", seq)
	job, err := BuildJobFromTemplate(jobID, tmplData, payouts, prevShareHash, g.extranonceSize)
	if err != nil {
		return nil, fmt.Errorf("build job: %w", err)
	}
	job.Seq = seq
	job.Template = tmpl

	g.storeJob(job)
	return job, nil
}

// GetJob returns a stored job by ID, or nil if not found.
func (g *Generator) GetJob(id string) *JobData {
	g.jobsMu.RLock()
	defer g.jobsMu.RUnlock()
	return g.jobs[id]
}

func (g *Generator) storeJob(job *JobData) {
	g.jobsMu.Lock()
	defer g.jobsMu.Unlock()

	g.jobs[job.ID] = job

	for len(g.jobs) > maxStoredJobs {
		oldestID := ""
		var oldestSeq uint64
		for id, j := range g.jobs {
			if oldestID == "" || j.Seq < oldestSeq {
				oldestID = id
				oldestSeq = j.Seq
			}
		}
		delete(g.jobs, oldestID)
	}
}

func (g *Generator) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	var consecutiveFailures int
	var lastFailureTime time.Time

	// Initial fetch
	if err := g.fetchTemplate(ctx); err != nil {
		consecutiveFailures++
		lastFailureTime = time.Now()
		g.logger.Warn("bitcoin RPC failed",
			zap.Error(err),
			zap.Int("consecutive_failures", consecutiveFailures),
			zap.Duration("next_retry", backoffDuration(consecutiveFailures)),
		)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if consecutiveFailures > 0 && time.Since(lastFailureTime) < backoffDuration(consecutiveFailures) {
				continue
			}

			if err := g.fetchTemplate(ctx); err != nil {
				consecutiveFailures++
				lastFailureTime = time.Now()
				g.logger.Warn("bitcoin RPC failed",
					zap.Error(err),
					zap.Int("consecutive_failures", consecutiveFailures),
					zap.Duration("next_retry", backoffDuration(consecutiveFailures)),
				)
			} else if consecutiveFailures > 0 {
				g.logger.Info("bitcoin RPC recovered",
					zap.Int("after_failures", consecutiveFailures),
				)
				consecutiveFailures = 0
			}
		}
	}
}

// backoffDuration computes exponential backoff capped at 60s.
func backoffDuration(failures int) time.Duration {
	if failures <= 0 {
		return PollInterval
	}
	d := PollInterval
	for i := 1; i < failures; i++ {
		d *= 2
		if d > 60*time.Second {
			return 60 * time.Second
		}
	}
	return d
}

func (g *Generator) fetchTemplate(ctx context.Context) error {
	tmpl, err := g.rpc.GetBlockTemplate(ctx)
	if err != nil {
		return err
	}

	g.templateMu.Lock()
	oldTemplate := g.currentTemplate
	g.currentTemplate = tmpl
	g.templateMu.Unlock()

	newBlock := oldTemplate == nil || tmpl.PreviousBlockHash != oldTemplate.PreviousBlockHash

	if newBlock {
		g.logger.Info("new block template",
			zap.Int64("height", tmpl.Height),
			zap.String("prevhash", tmpl.PreviousBlockHash[:16]+"..."),
		)
	}

	// Send a new job when: new block (clean), or periodic refresh to keep miners alive
	needsRefresh := !newBlock && time.Since(g.lastJobTime) >= JobRefreshInterval

	if newBlock || needsRefresh {
		job, err := g.GenerateJob()
		if err != nil {
			g.logger.Error("failed to generate job", zap.Error(err))
			return nil
		}
		job.CleanJobs = newBlock

		select {
		case g.jobCh <- job:
			g.lastJobTime = time.Now()
		default:
			g.logger.Warn("job channel full")
		}
	}

	return nil
}

func extractTxHashes(tmpl *bitcoin.BlockTemplate) []string {
	hashes := make([]string, len(tmpl.Transactions))
	for i, tx := range tmpl.Transactions {
		// getblocktemplate returns txids in display order (reversed).
		// The merkle tree needs internal byte order (raw hash output).
		b, _ := hex.DecodeString(tx.TxID)
		hashes[i] = hex.EncodeToString(util.ReverseBytes(b))
	}
	return hashes
}
