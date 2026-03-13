package web

import (
	"embed"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/djkazic/p2pool-go/internal/metrics"
)

//go:embed assets/share_found.mp3
var shareSoundMP3 []byte

//go:embed templates/*
var htmlFiles embed.FS

// StatusData holds all dashboard metrics.
type StatusData struct {
	ShareCount         int                `json:"share_count"`
	MinerCount         int                `json:"miner_count"`
	PeerCount          int                `json:"peer_count"`
	Difficulty         float64            `json:"difficulty"`
	TargetBits         string             `json:"target_bits"`
	TipHash            string             `json:"tip_hash"`
	TipMiner           string             `json:"tip_miner"`
	TipTime            int64              `json:"tip_time"`
	RecentShares       []ShareInfo        `json:"recent_shares"`
	MinerWeights       map[string]float64 `json:"miner_weights"`
	Network            string             `json:"network"`
	StratumPort        int                `json:"stratum_port"`
	P2PPort            int                `json:"p2p_port"`
	ShareTargetTime    int                `json:"share_target_time_secs"`
	PPLNSWindowSize    int                `json:"pplns_window_size"`
	Uptime             int64              `json:"uptime_secs"`
	PoolHashrate       float64            `json:"pool_hashrate"`
	LocalHashrate      float64            `json:"local_hashrate"`
	LastBlockFoundTime int64              `json:"last_block_found_time"`
	LastBlockFoundHash string             `json:"last_block_found_hash"`
	EstTimeToBlock     int64              `json:"est_time_to_block"`
	History            []HistoryPoint     `json:"history"`
	OurAddress         string             `json:"our_address"`
	PayoutEntries      []PayoutInfo       `json:"payout_entries"`
	CoinbaseValue      int64              `json:"coinbase_value"`
	TreeShares         []TreeShare        `json:"tree_shares"`
	OurPeerID          string             `json:"our_peer_id"`
	Peers              []PeerInfo         `json:"peers"`
	Miners             []MinerStat        `json:"miners"`
}

// PayoutInfo describes a single payout output for the dashboard.
type PayoutInfo struct {
	Address string  `json:"address"`
	Amount  int64   `json:"amount"`
	Pct     float64 `json:"pct"`
}

// ShareInfo describes a single share for the dashboard.
type ShareInfo struct {
	Hash      string `json:"hash"`
	Miner     string `json:"miner"`
	Timestamp int64  `json:"timestamp"`
	IsBlock   bool   `json:"is_block"`
}

// TreeShare describes a share for the sharechain tree visualization.
type TreeShare struct {
	Hash          string `json:"hash"`
	PrevShareHash string `json:"prev_share_hash"`
	Miner         string `json:"miner"`
	Timestamp     int64  `json:"timestamp"`
	IsBlock       bool   `json:"is_block"`
	MainChain     bool   `json:"main_chain"`
}

// MinerStat describes a connected miner for the dashboard.
type MinerStat struct {
	Worker        string  `json:"worker"`
	Hashrate      float64 `json:"hashrate"`
	Difficulty    float64 `json:"difficulty"`
	ShareCount    int     `json:"shares"`
	LastShareTime int64   `json:"last_share_time"`
	ConnectedSecs int64   `json:"connected_secs"`
}

// PeerInfo describes a connected peer for the dashboard.
type PeerInfo struct {
	ID      string `json:"id"`
	Latency int64  `json:"latency_us"`
	Address string `json:"address"`
}

// HistoryPoint is a single data point for dashboard graphs.
type HistoryPoint struct {
	Timestamp     int64   `json:"t"`
	PoolHashrate  float64 `json:"ph"`
	LocalHashrate float64 `json:"lh"`
}

// ShareDetail holds full details for a single share.
type ShareDetail struct {
	Hash          string `json:"hash"`
	Miner         string `json:"miner"`
	Timestamp     int64  `json:"timestamp"`
	IsBlock       bool   `json:"is_block"`
	Version       int32  `json:"version"`
	PrevBlockHash string `json:"prev_block_hash"`
	MerkleRoot    string `json:"merkle_root"`
	Bits          uint32 `json:"bits"`
	Nonce         uint32 `json:"nonce"`
	PrevShareHash string `json:"prev_share_hash"`
	ShareVersion  uint32 `json:"share_version"`
	Difficulty    string `json:"difficulty"`
}

// ShareLookupFunc looks up a share by display-order hex hash.
type ShareLookupFunc func(hashHex string) *ShareDetail

// statusCache holds a cached JSON response to avoid expensive chain walks on every request.
type statusCache struct {
	mu      sync.Mutex
	data    []byte
	expires time.Time
}

const statusCacheTTL = 2 * time.Second

func (c *statusCache) get(dataFunc func() *StatusData) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return c.data
	}
	buf, _ := json.Marshal(dataFunc())
	c.data = buf
	c.expires = time.Now().Add(statusCacheTTL)
	return c.data
}

// NewHandler creates an HTTP handler serving the dashboard and JSON API.
func NewHandler(dataFunc func() *StatusData, shareLookup ShareLookupFunc) http.Handler {
	mux := http.NewServeMux()
	cache := &statusCache{}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; media-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		dashboardHTML, _ := htmlFiles.ReadFile("templates/dashboard.html")
		w.Write([]byte(dashboardHTML))
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(cache.get(dataFunc))
	})

	mux.HandleFunc("/api/share/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		hashHex := strings.TrimPrefix(r.URL.Path, "/api/share/")
		if len(hashHex) != 64 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid hash length"})
			return
		}

		detail := shareLookup(hashHex)
		if detail == nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "share not found"})
			return
		}

		json.NewEncoder(w).Encode(detail)
	})

	mux.HandleFunc("/assets/share_found.mp3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(shareSoundMP3)
	})

	mux.Handle("/metrics", metrics.Handler())

	return mux
}
