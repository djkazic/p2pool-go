package stratum

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// SessionState represents the state of a miner session.
type SessionState int

const (
	StateConnected SessionState = iota
	StateSubscribed
	StateAuthorized
)

const (
	// VersionRollingMask defines which bits of the block version the miner
	// may modify. Bits 13-28 (0x1fffe000) is the standard mask used by
	// ASICs for extra nonce space via BIP 310 (version rolling).
	VersionRollingMask = "1fffe000"
)

// Session represents a connected miner session.
type Session struct {
	mu sync.Mutex

	ID      string
	Codec   *Codec
	State   SessionState
	Vardiff *Vardiff
	Logger  *zap.Logger

	// Miner info
	WorkerName      string
	Extranonce1     string // Hex-encoded unique per-session extranonce
	Extranonce2Size int

	// Version rolling (BIP 310)
	VersionRollingEnabled bool
	VersionRollingMask    string

	// Current job
	currentJobID string

	// Submit channel - sends validated submissions to the server
	submitCh chan *ShareSubmission

	submitLimiter *rate.Limiter
}

// ShareSubmission represents a share submitted by a miner.
type ShareSubmission struct {
	SessionID      string
	WorkerName     string
	JobID          string
	Extranonce1    string // Hex-encoded, from the session
	Extranonce2    string
	NTime          string
	Nonce          string
	VersionBits    string  // BIP 310 version rolling bits (hex), empty if not used
	Difficulty     float64 // Current stratum difficulty for this miner
	PrevDifficulty float64 // Previous difficulty (before most recent retarget), 0 if none
}

// NewSession creates a new miner session.
func NewSession(id string, codec *Codec, extranonce1 string, extranonce2Size int, startDifficulty float64, submitCh chan *ShareSubmission, logger *zap.Logger) *Session {
	return &Session{
		ID:              id,
		Codec:           codec,
		State:           StateConnected,
		Vardiff:         NewVardiff(startDifficulty),
		Logger:          logger.With(zap.String("session", id)),
		Extranonce1:     extranonce1,
		Extranonce2Size: extranonce2Size,
		submitCh:        submitCh,
		submitLimiter:   rate.NewLimiter(100, 20),
	}
}

// HandleRequest processes a single Stratum request.
func (s *Session) HandleRequest(req *Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch req.Method {
	case "mining.configure":
		return s.handleConfigure(req)
	case "mining.subscribe":
		return s.handleSubscribe(req)
	case "mining.authorize":
		return s.handleAuthorize(req)
	case "mining.suggest_difficulty":
		return s.handleSuggestDifficulty(req)
	case "mining.submit":
		return s.handleSubmit(req)
	case "mining.extranonce.subscribe":
		return s.sendResult(req.ID, true)
	default:
		s.Logger.Debug("unknown method", zap.String("method", req.Method))
		return s.sendError(req.ID, 20, "Unknown method")
	}
}

// handleConfigure handles the mining.configure method (BIP 310 version rolling).
//
// Request params: [["version-rolling", ...], {"version-rolling.mask": "...", "version-rolling.min-bit-count": N}]
// Response result: {"version-rolling": true, "version-rolling.mask": "1fffe000"}
func (s *Session) handleConfigure(req *Request) error {
	var params []json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 1 {
		return s.sendResult(req.ID, map[string]interface{}{})
	}

	// Parse the list of requested extensions
	var extensions []string
	if err := json.Unmarshal(params[0], &extensions); err != nil {
		return s.sendResult(req.ID, map[string]interface{}{})
	}

	result := make(map[string]interface{})

	for _, ext := range extensions {
		switch ext {
		case "version-rolling":
			// Accept version rolling with our mask.
			// If the miner proposed a mask, intersect with ours.
			mask := VersionRollingMask

			if len(params) > 1 {
				var extParams map[string]json.RawMessage
				if err := json.Unmarshal(params[1], &extParams); err == nil {
					if minerMaskRaw, ok := extParams["version-rolling.mask"]; ok {
						var minerMask string
						if err := json.Unmarshal(minerMaskRaw, &minerMask); err == nil {
							mask = intersectMasks(minerMask, VersionRollingMask)
						}
					}
				}
			}

			s.VersionRollingEnabled = true
			s.VersionRollingMask = mask
			result["version-rolling"] = true
			result["version-rolling.mask"] = mask

			s.Logger.Debug("version rolling configured", zap.String("mask", mask))

		default:
			// Unsupported extension - reject it
			result[ext] = false
		}
	}

	return s.sendResult(req.ID, result)
}

// intersectMasks ANDs two hex mask strings. Falls back to the server mask on error.
func intersectMasks(minerMask, serverMask string) string {
	var miner, server uint64
	if _, err := fmt.Sscanf(minerMask, "%x", &miner); err != nil {
		return serverMask
	}
	if _, err := fmt.Sscanf(serverMask, "%x", &server); err != nil {
		return serverMask
	}
	return fmt.Sprintf("%08x", miner&server)
}

func (s *Session) handleSuggestDifficulty(req *Request) error {
	var params []float64
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 1 {
		return s.sendError(req.ID, 20, "Invalid suggest_difficulty params")
	}

	diff := params[0]
	s.Vardiff.SetDifficulty(diff)
	s.Logger.Info("miner suggested difficulty",
		zap.Float64("suggested", diff),
		zap.Float64("effective", s.Vardiff.Difficulty()),
	)

	// If already subscribed, re-send set_difficulty so the miner knows it took effect
	if s.State >= StateSubscribed {
		if err := s.sendDifficulty(s.Vardiff.Difficulty()); err != nil {
			return err
		}
	}

	return s.sendResult(req.ID, true)
}

func (s *Session) handleSubscribe(req *Request) error {
	s.State = StateSubscribed

	s.Logger.Debug("miner subscribed", zap.String("extranonce1", s.Extranonce1))

	subscriptions := [][]string{
		{"mining.set_difficulty", s.ID},
		{"mining.notify", s.ID},
	}

	// If version rolling was negotiated via mining.configure, advertise
	// the set_version_mask subscription as well.
	if s.VersionRollingEnabled {
		subscriptions = append(subscriptions, []string{"mining.set_version_mask", s.ID})
	}

	result := []interface{}{
		subscriptions,
		s.Extranonce1,
		s.Extranonce2Size,
	}

	if err := s.sendResult(req.ID, result); err != nil {
		return err
	}

	// Send initial difficulty
	if err := s.sendDifficulty(s.Vardiff.Difficulty()); err != nil {
		return err
	}

	// Send version mask notification so the miner can begin version rolling.
	// This is required by BOSminer and other Stratum V2-to-V1 translation layers.
	if s.VersionRollingEnabled {
		if err := s.sendVersionMask(s.VersionRollingMask); err != nil {
			return err
		}
	}

	return nil
}

const maxWorkerNameLen = 256

func (s *Session) handleAuthorize(req *Request) error {
	var params []string
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 1 {
		return s.sendError(req.ID, 20, "Invalid authorize params")
	}

	name := params[0]
	if len(name) > maxWorkerNameLen {
		name = name[:maxWorkerNameLen]
	}
	s.WorkerName = name
	s.State = StateAuthorized
	s.Logger.Info("miner authorized", zap.String("worker", s.WorkerName))

	return s.sendResult(req.ID, true)
}

func (s *Session) handleSubmit(req *Request) error {
	if s.State != StateAuthorized {
		return s.sendError(req.ID, 24, "Not authorized")
	}

	if !s.submitLimiter.Allow() {
		s.Logger.Warn("rate limit exceeded")
		return s.sendError(req.ID, 25, "Rate limit exceeded")
	}

	var params []string
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 5 {
		return s.sendError(req.ID, 20, "Invalid submit params")
	}

	// Validate extranonce2 length matches expected size (hex-encoded, so 2 chars per byte)
	expectedEN2Len := s.Extranonce2Size * 2
	if len(params[2]) != expectedEN2Len {
		return s.sendError(req.ID, 20, fmt.Sprintf("Invalid extranonce2 length: got %d, want %d", len(params[2]), expectedEN2Len))
	}

	// Validate ntime and nonce are 8-char hex strings (4 bytes each)
	if !isHex(params[3], 8) {
		return s.sendError(req.ID, 20, "Invalid ntime format")
	}
	if !isHex(params[4], 8) {
		return s.sendError(req.ID, 20, "Invalid nonce format")
	}

	submission := &ShareSubmission{
		SessionID:      s.ID,
		WorkerName:     params[0],
		JobID:          params[1],
		Extranonce1:    s.Extranonce1,
		Extranonce2:    params[2],
		NTime:          params[3],
		Nonce:          params[4],
		Difficulty:     s.Vardiff.Difficulty(),
		PrevDifficulty: s.Vardiff.PrevDifficulty(),
	}

	// BIP 310: if version rolling is enabled, the 6th param is the rolled version bits
	if s.VersionRollingEnabled && len(params) >= 6 {
		if !isHex(params[5], 8) {
			return s.sendError(req.ID, 20, "Invalid version bits format")
		}
		submission.VersionBits = params[5]
	}

	// Record for vardiff
	if s.Vardiff.RecordShare() {
		// Difficulty changed, notify miner
		s.sendDifficulty(s.Vardiff.Difficulty())
	}

	// Send submission to server for validation
	select {
	case s.submitCh <- submission:
	default:
		s.Logger.Warn("submit channel full, dropping share")
	}

	return s.sendResult(req.ID, true)
}

// NotifyJob sends a mining.notify message to the miner.
func (s *Session) NotifyJob(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.currentJobID = job.ID

	notif := &Notification{
		ID:     nil,
		Method: "mining.notify",
		Params: []interface{}{
			job.ID,
			job.PrevHash,
			job.Coinbase1,
			job.Coinbase2,
			job.MerkleBranches,
			job.Version,
			job.NBits,
			job.NTime,
			job.CleanJobs,
		},
	}

	return s.Codec.SendNotification(notif)
}

func (s *Session) sendDifficulty(diff float64) error {
	notif := &Notification{
		ID:     nil,
		Method: "mining.set_difficulty",
		Params: []interface{}{diff},
	}
	return s.Codec.SendNotification(notif)
}

func (s *Session) sendVersionMask(mask string) error {
	notif := &Notification{
		ID:     nil,
		Method: "mining.set_version_mask",
		Params: []interface{}{mask},
	}
	return s.Codec.SendNotification(notif)
}

func (s *Session) sendResult(id interface{}, result interface{}) error {
	return s.Codec.SendResponse(&Response{
		ID:     id,
		Result: result,
		Error:  nil,
	})
}

func (s *Session) sendError(id interface{}, code int, msg string) error {
	return s.Codec.SendResponse(&Response{
		ID:     id,
		Result: nil,
		Error:  []interface{}{code, msg, nil},
	})
}

// Close closes the session.
func (s *Session) Close() error {
	return s.Codec.Close()
}

// Job represents a mining job sent to miners.
type Job struct {
	ID             string
	PrevHash       string
	Coinbase1      string
	Coinbase2      string
	MerkleBranches []string
	Version        string
	NBits          string
	NTime          string
	CleanJobs      bool
}

// String returns a brief description of the job.
func (j *Job) String() string {
	prefix := j.PrevHash
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return fmt.Sprintf("Job{id=%s, prevhash=%s..., clean=%v}", j.ID, prefix, j.CleanJobs)
}

// isHex returns true if s is a valid hex string of exactly length n.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
