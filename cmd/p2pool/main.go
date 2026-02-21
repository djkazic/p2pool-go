package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/djkazic/p2pool-go/internal/config"
	"github.com/djkazic/p2pool-go/internal/node"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.DefaultConfig()

	// Define CLI flags
	var minerAddress string
	var bootnodes string

	flag.StringVar(&minerAddress, "address", "", "your payout address (required, bech32 testnet: tb1...)")
	flag.StringVar(&bootnodes, "bootnodes", "", "comma-separated list of bootnode multiaddrs for WAN discovery")
	flag.StringVar(&cfg.BitcoinRPCHost, "rpc-host", cfg.BitcoinRPCHost, "bitcoind RPC host")
	flag.IntVar(&cfg.BitcoinRPCPort, "rpc-port", cfg.BitcoinRPCPort, "bitcoind RPC port")
	flag.StringVar(&cfg.BitcoinRPCUser, "rpc-user", cfg.BitcoinRPCUser, "bitcoind RPC username")
	flag.StringVar(&cfg.BitcoinRPCPassword, "rpc-password", cfg.BitcoinRPCPassword, "bitcoind RPC password")
	flag.StringVar(&cfg.BitcoinNetwork, "network", cfg.BitcoinNetwork, "bitcoin network (testnet3, mainnet, regtest)")
	flag.IntVar(&cfg.StratumPort, "stratum-port", cfg.StratumPort, "stratum server listen port")
	flag.Float64Var(&cfg.StartDifficulty, "start-difficulty", cfg.StartDifficulty, "initial stratum difficulty for new miners (vardiff adjusts from here)")
	flag.IntVar(&cfg.P2PPort, "p2p-port", cfg.P2PPort, "p2p network listen port")
	flag.BoolVar(&cfg.EnableMDNS, "mdns", cfg.EnableMDNS, "enable mDNS peer discovery on LAN")
	flag.BoolVar(&cfg.DHTServer, "dht-server", cfg.DHTServer, "force DHT server mode (use on bootnodes / public nodes)")
	flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for persistent data")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level (debug, info, warn, error)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "p2pool-go - decentralized Bitcoin mining pool\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  p2pool -address <your_payout_address> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nEnvironment variables:\n")
		fmt.Fprintf(os.Stderr, "  BITCOIN_RPC_HOST      Override -rpc-host\n")
		fmt.Fprintf(os.Stderr, "  BITCOIN_RPC_USER      Override -rpc-user\n")
		fmt.Fprintf(os.Stderr, "  BITCOIN_RPC_PASSWORD   Override -rpc-password\n")
		fmt.Fprintf(os.Stderr, "  P2POOL_DATA_DIR       Override -data-dir\n")
		fmt.Fprintf(os.Stderr, "  P2POOL_BOOTNODES      Override -bootnodes\n")
		fmt.Fprintf(os.Stderr, "  LOG_LEVEL             Override -log-level\n")
	}

	flag.Parse()

	// Environment variables override flags (for containerized deployments)
	if v := os.Getenv("BITCOIN_RPC_HOST"); v != "" {
		cfg.BitcoinRPCHost = v
	}
	if v := os.Getenv("BITCOIN_RPC_USER"); v != "" {
		cfg.BitcoinRPCUser = v
	}
	if v := os.Getenv("BITCOIN_RPC_PASSWORD"); v != "" {
		cfg.BitcoinRPCPassword = v
	}
	if v := os.Getenv("P2POOL_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}

	// Parse bootnodes
	if bootnodes != "" {
		for _, bn := range strings.Split(bootnodes, ",") {
			bn = strings.TrimSpace(bn)
			if bn != "" {
				cfg.P2PBootnodes = append(cfg.P2PBootnodes, bn)
			}
		}
	}
	if v := os.Getenv("P2POOL_BOOTNODES"); v != "" {
		cfg.P2PBootnodes = nil // env var replaces flag entirely
		for _, bn := range strings.Split(v, ",") {
			bn = strings.TrimSpace(bn)
			if bn != "" {
				cfg.P2PBootnodes = append(cfg.P2PBootnodes, bn)
			}
		}
	}

	// Validate required flags
	if minerAddress == "" {
		fmt.Fprintf(os.Stderr, "Error: -address is required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Setup logger
	logger, err := newLogger(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("setup logger: %w", err)
	}
	defer logger.Sync()

	logger.Info("starting p2pool-go",
		zap.String("miner_address", minerAddress),
		zap.String("bitcoin_rpc", cfg.BitcoinRPCURL()),
		zap.String("network", cfg.BitcoinNetwork),
	)

	if cfg.BitcoinNetwork != "mainnet" {
		logger.Warn("NOT running on mainnet — this node is using "+cfg.BitcoinNetwork,
			zap.String("network", cfg.BitcoinNetwork),
			zap.Int("rpc_port", cfg.BitcoinRPCPort),
		)
	}

	// Create and start node
	n := node.NewNode(cfg, minerAddress, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := n.Start(ctx); err != nil {
		return fmt.Errorf("start node: %w", err)
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	logger.Info("received signal, shutting down", zap.String("signal", sig.String()))

	n.Stop()
	return nil
}

func newLogger(level string) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	switch level {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "info":
		zapLevel = zapcore.InfoLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapLevel),
		Development:      false,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}

	return cfg.Build()
}
