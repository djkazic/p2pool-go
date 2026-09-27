package config

import (
	"fmt"
	"time"
)

// Config holds all configuration for a p2pool node.
type Config struct {
	// Bitcoin RPC
	BitcoinRPCHost     string `mapstructure:"bitcoin-rpc-host"`
	BitcoinRPCPort     int    `mapstructure:"bitcoin-rpc-port"`
	BitcoinRPCUser     string `mapstructure:"bitcoin-rpc-user"`
	BitcoinRPCPassword string `mapstructure:"bitcoin-rpc-password"`
	BitcoinNetwork     string `mapstructure:"bitcoin-network"`

	// Stratum server
	StratumPort     int     `mapstructure:"stratum-port"`
	StartDifficulty float64 `mapstructure:"start-difficulty"`

	// P2P
	P2PPort      int      `mapstructure:"p2p-port"`
	P2PBootnodes []string `mapstructure:"p2p-bootnodes"`
	EnableMDNS   bool     `mapstructure:"enable-mdns"`
	DHTServer    bool     `mapstructure:"dht-server"`

	// Sharechain
	ShareTargetTime   time.Duration `mapstructure:"share-target-time"`
	PPLNSWindowSize   int           `mapstructure:"pplns-window-size"`
	FinderFeePercent  float64       `mapstructure:"finder-fee-percent"`
	DustThresholdSats int64         `mapstructure:"dust-threshold-sats"`

	// Storage
	DataDir string `mapstructure:"data-dir"`

	// Logging
	LogLevel string `mapstructure:"log-level"`
}

// DefaultConfig returns a Config with sensible defaults for Bitcoin mainnet.
func DefaultConfig() *Config {
	return &Config{
		BitcoinRPCHost:     "127.0.0.1",
		BitcoinRPCPort:     8332,
		BitcoinRPCUser:     "user",
		BitcoinRPCPassword: "pass",
		BitcoinNetwork:     "mainnet",

		StratumPort:     3333,
		StartDifficulty: 100000,

		P2PPort:    9171,
		EnableMDNS: true,

		ShareTargetTime:   30 * time.Second,
		PPLNSWindowSize:   8640,
		FinderFeePercent:  0.5,
		DustThresholdSats: 546,

		DataDir: ".p2pool",

		LogLevel: "info",
	}
}

// Validate checks the config for errors.
func (c *Config) Validate() error {
	if c.BitcoinRPCHost == "" {
		return fmt.Errorf("bitcoin-rpc-host is required")
	}
	if c.BitcoinRPCPort <= 0 || c.BitcoinRPCPort > 65535 {
		return fmt.Errorf("bitcoin-rpc-port must be 1-65535")
	}
	if c.StratumPort <= 0 || c.StratumPort > 65535 {
		return fmt.Errorf("stratum-port must be 1-65535")
	}
	if c.P2PPort <= 0 || c.P2PPort > 65535 {
		return fmt.Errorf("p2p-port must be 1-65535")
	}
	if c.ShareTargetTime < time.Second {
		return fmt.Errorf("share-target-time must be at least 1s")
	}
	// Practical share target times are 10–60s. Anything above an hour is
	// almost certainly a unit typo (minutes confused with hours, etc.) and
	// would make sharechain difficulty math run on a window that takes days
	// to fill. Reject it at config load rather than silently misbehave.
	if c.ShareTargetTime > time.Hour {
		return fmt.Errorf("share-target-time must be at most 1h (got %v) — check units", c.ShareTargetTime)
	}
	if c.PPLNSWindowSize < 1 {
		return fmt.Errorf("pplns-window-size must be at least 1")
	}
	// These two are consensus parameters, not tunables: validation recomputes
	// each share's payout split and compares it against the coinbase, so a
	// node running different numbers would reject every share its peers
	// produce and have all of its own rejected in turn. They are kept as
	// fields so an existing config file still loads, but must match.
	if c.FinderFeePercent != DefaultConfig().FinderFeePercent {
		return fmt.Errorf("finder-fee-percent is fixed by consensus at %g and cannot be changed",
			DefaultConfig().FinderFeePercent)
	}
	if c.DustThresholdSats != DefaultConfig().DustThresholdSats {
		return fmt.Errorf("dust-threshold-sats is fixed by consensus at %d and cannot be changed",
			DefaultConfig().DustThresholdSats)
	}
	return nil
}

// DefaultBootnodes returns the built-in bootnode list for a given network.
// Users can add more via -bootnodes; these are always included.
func DefaultBootnodes(network string) []string {
	switch network {
	case "mainnet":
		return []string{
			// pool.eldamar.icu — primary mainnet bootnode
			"/dns4/pool.eldamar.icu/tcp/9171/p2p/12D3KooWQANcf3idikEnVF7cE5i6YGoHxSCRsuzkjWY2MZ2aKY4m",
		}
	default:
		return nil
	}
}

// BitcoinRPCURL returns the full RPC URL.
func (c *Config) BitcoinRPCURL() string {
	return fmt.Sprintf("http://%s:%d", c.BitcoinRPCHost, c.BitcoinRPCPort)
}
