# p2pool-go

A decentralized Bitcoin mining pool in Go, inspired by the original [p2pool](https://en.bitcoin.it/wiki/P2Pool). Every miner runs their own node and shares work over a peer-to-peer network — no central server, no trust required.

## How It Works

p2pool-go maintains a **sharechain** — a parallel blockchain of lower-difficulty "shares" that tracks each miner's contributions. When a share also meets Bitcoin's full network difficulty, it becomes a real Bitcoin block whose coinbase pays all recent contributors proportionally via **PPLNS** (Pay Per Last N Shares).

- **Decentralized** — No pool operator. Every miner runs their own node.
- **Trustless** — Payouts are enforced by sharechain consensus.
- **Fair** — PPLNS rewards discourage pool-hopping and reward consistent miners.

## Quick Start

### 1. Prerequisites

- **Go 1.22+**
- **Bitcoin Core** (bitcoind) with RPC enabled

### 2. Configure Bitcoin Core

Add to `~/.bitcoin/bitcoin.conf`:

```ini
server=1
rpcuser=your_rpc_user
rpcpassword=your_rpc_password
rpcallowip=127.0.0.1
```

Start and wait for sync:

```bash
bitcoind -daemon
bitcoin-cli getblockchaininfo
```

### 3. Build and Run

```bash
git clone https://github.com/djkazic/p2pool-go.git
cd p2pool-go
make build

./build/p2pool \
  -address bc1qyourmainnetaddress \
  -rpc-user your_rpc_user \
  -rpc-password your_rpc_password
```

### 4. Connect a Miner

Any Stratum v1 miner works. Example with cpuminer:

```bash
cpuminer -a sha256d -o stratum+tcp://127.0.0.1:3333 -u worker1 -p x
```

The `-u` username is just a worker label — payout addresses are set on the node with `-address`, not the miner.

### 5. Open the Dashboard

Navigate to [http://localhost:3333](http://localhost:3333) in your browser. The web UI is served on the same port as the stratum server.

## Docker

### 1. Configure

Create a `.env` file:

```
PAYOUT_ADDRESS=bc1pks29cz7w4hj265aanmyp8uwrl9hzykhwf5hl5gf40hlvj5hcs2es0sgm7z
BITCOIN_RPC_HOST=your_bitcoind_ip
BITCOIN_RPC_USER=your_rpc_user
BITCOIN_RPC_PASSWORD=your_rpc_password
LOG_LEVEL=info
```

| Variable | Description |
|---|---|
| `PAYOUT_ADDRESS` | Your mainnet Bitcoin address (`bc1...`) |
| `BITCOIN_RPC_HOST` | IP/hostname of your bitcoind |
| `BITCOIN_RPC_USER` | bitcoind RPC username |
| `BITCOIN_RPC_PASSWORD` | bitcoind RPC password |
| `LOG_LEVEL` | Log verbosity: `debug`, `info`, `warn`, `error` (default: `info`) |

The container defaults to mainnet on RPC port 8332.

### 2. Start

```bash
docker compose up --build -d
```

### 3. Connect a miner + open dashboard

Same as above — stratum at `stratum+tcp://localhost:3333`, dashboard at [http://localhost:3333](http://localhost:3333).

### 4. Logs & stopping

```bash
docker compose logs -f
docker compose down
```

## Features

### Web Dashboard

A real-time monitoring dashboard served directly on the stratum port (no extra HTTP port needed). HTTP and Stratum are multiplexed on the same TCP connection by peeking the first byte.

- **Stat cards** — Pool hashrate, local hashrate, connected miners, peers, shares, difficulty, estimated time to block, last block found
- **Live graphs** — Pool hashrate and share count history rendered on canvas (60-point rolling window)
- **Recent shares table** — Full untruncated hashes, clickable for details, golden highlight for shares that mined a Bitcoin block
- **Share detail modal** — Click any share hash to view full header fields (version, prev block hash, merkle root, bits, nonce, prev share hash, difficulty)
- **PPLNS distribution** — Bar chart showing each miner's proportional contribution
- **Auto-refresh** — Polls `/api/status` every 5 seconds
- **Web UI** — GitHub-inspired dark UI, responsive layout

### API Endpoints

| Endpoint | Description |
|---|---|
| `GET /` | Web dashboard |
| `GET /api/status` | Pool status JSON (2s cache) |
| `GET /api/share/{hash}` | Share details by hex hash |
| `GET /metrics` | Prometheus metrics |

### Prometheus Metrics

Exposed at `/metrics` on the stratum port. Gauges are updated every 30 seconds; counters increment in real time.

**Gauges:**
`p2pool_sharechain_height`, `p2pool_miners_connected`, `p2pool_peers_connected`, `p2pool_share_difficulty`, `p2pool_pool_hashrate`, `p2pool_local_hashrate`, `p2pool_uptime_seconds`

**Counters:**
`p2pool_stratum_shares_accepted_total`, `p2pool_stratum_shares_rejected_total`, `p2pool_blocks_found_total`, `p2pool_block_submissions_total{result="success|rejected|failed"}`

### Stratum Server

- **Stratum v1** protocol over newline-delimited JSON-RPC
- **Variable difficulty (vardiff)** — Adjusts per-miner difficulty targeting 10s between shares, retargets every 60s
- **BIP 310 version rolling** — Miners can roll bits 13–28 of the block version for extra nonce space
- **Grace period** — After a vardiff retarget, shares at the previous difficulty are still accepted
- **Rate limiting** — Per-session submit rate limiter prevents flooding
- **Session limit** — Max 1,000 concurrent miner sessions
- **TCP keepalive** — 30s keepalive probes detect dead connections
- **Read timeout** — 5-minute idle timeout per connection

### Sharechain

- **Difficulty adjustment** — 72-share window (~36 min), max 4x step, window trimming excludes stale-difficulty outliers
- **Heaviest-chain fork choice** — Cumulative work determines the best tip; ties broken by lowest hash
- **Validation** — Timestamp bounds (±2 min of now, ±10 min of parent), PoW check, parent existence, address validation
- **Pruning** — Orphans pruned every 5 minutes; old shares beyond 2x PPLNS window removed
- **Persistent storage** — BoltDB-backed store (`sharechain.db`) survives restarts
- **Events** — `NewTip`, `NewBlock`, `Reorg` events drive job regeneration and logging

### PPLNS Payouts

- **Window** — 8,640 shares (~3 days at 30s target)
- **Weighted** — Shares weighted by difficulty (higher-diff shares count more)
- **Finder fee** — 0.5% bonus to the miner whose share becomes a block
- **Dust consolidation** — Payouts below 546 satoshis are redistributed to avoid unspendable outputs
- **Deterministic** — Same window always produces identical payout sets

### P2P Network

- **libp2p** with Noise encryption and Yamux multiplexing
- **GossipSub** for share propagation (`/p2pool/shares/1.0.0`)
- **Discovery** — mDNS for LAN, Kademlia DHT for WAN, static bootnodes
- **Locator-based sync** — New nodes sync via exponentially-spaced hash locators (like Bitcoin's `getheaders`)
- **CBOR encoding** — Compact binary serialization for all P2P messages
- **Hardened** — 30s stream deadlines, message size caps (1MB sync, 100KB coinbase), field length validation

### Bootstrapping

On a LAN, nodes find each other automatically via mDNS. For WAN (internet) discovery, at least one public bootnode is needed:

1. **Run a bootnode** — Start p2pool on a server with a public IP (or port-forwarded port 9171). On startup it logs its multiaddr:
   ```
   listening on  {"addr": "/ip4/203.0.113.5/tcp/9171/p2p/12D3KooWABC..."}
   ```

2. **Connect other nodes** — Pass that multiaddr to other nodes:
   ```bash
   ./build/p2pool -address tb1q... -bootnodes /ip4/203.0.113.5/tcp/9171/p2p/12D3KooWABC...
   ```

3. **Multiple bootnodes** — Comma-separate for redundancy:
   ```bash
   -bootnodes /ip4/1.2.3.4/tcp/9171/p2p/12D3KooW...,/ip4/5.6.7.8/tcp/9171/p2p/12D3KooW...
   ```

Once connected to any peer, the Kademlia DHT propagates peer info so nodes discover each other transitively — the bootnode is only needed for the initial introduction.

### Block Submission

- **Retry with backoff** — Transient RPC failures retry up to 3 times (1s → 2s → 4s)
- **No retry on rejection** — Explicit consensus rejections from bitcoind are not retried
- **Merkle root verification** — Pre-submission sanity check catches reconstruction bugs
- **Full block reconstruction** — 80-byte header + coinbase + all template transactions

## Configuration

### CLI Flags

| Flag | Default | Description |
|---|---|---|
| `-address` | *(required)* | Payout address (bech32: `bc1...` for mainnet, `tb1...` for testnet) |
| `-rpc-host` | `127.0.0.1` | bitcoind RPC host |
| `-rpc-port` | `8332` | bitcoind RPC port |
| `-rpc-user` | `user` | bitcoind RPC username |
| `-rpc-password` | `pass` | bitcoind RPC password |
| `-network` | `mainnet` | Bitcoin network (`mainnet`, `testnet3`, `regtest`) |
| `-stratum-port` | `3333` | Stratum server port (also serves HTTP dashboard) |
| `-start-difficulty` | `100000` | Initial stratum difficulty (vardiff adjusts from here) |
| `-p2p-port` | `9171` | P2P listen port |
| `-bootnodes` | *(none)* | Comma-separated bootnode multiaddrs for WAN discovery |
| `-mdns` | `true` | Enable mDNS LAN discovery |
| `-data-dir` | `.p2pool` | Persistent data directory |
| `-log-level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |

### Environment Variables

Environment variables override flags (useful for containers):

| Variable | Overrides |
|---|---|
| `BITCOIN_RPC_HOST` | `-rpc-host` |
| `BITCOIN_RPC_USER` | `-rpc-user` |
| `BITCOIN_RPC_PASSWORD` | `-rpc-password` |
| `P2POOL_DATA_DIR` | `-data-dir` |
| `P2POOL_BOOTNODES` | `-bootnodes` |
| `LOG_LEVEL` | `-log-level` |

### Running on Testnet

Defaults are configured for mainnet. For testnet:

```bash
./build/p2pool \
  -address tb1qyourtestnetaddress \
  -network testnet3 \
  -rpc-port 18332 \
  -rpc-user your_rpc_user \
  -rpc-password your_rpc_password
```

### Default Ports

| Port | Protocol | Description |
|---|---|---|
| 3333 | TCP | Stratum v1 + HTTP dashboard + Prometheus metrics |
| 9171 | TCP | P2P network (libp2p) |

## Architecture

```
                    +-------------------+
                    |   Bitcoin Core    |
                    |   (bitcoind)      |
                    +---------+---------+
                              |  JSON-RPC
                              |  (getblocktemplate / submitblock)
                    +---------v---------+
        Stratum     |                   |     libp2p
Miners <----------->|   p2pool-go Node  |<-----------> Other Nodes
        (TCP:3333)  |                   |   (TCP:9171)
                    |   Web Dashboard   |
        HTTP  <---->|   /api + /metrics |
        (TCP:3333)  +---------+---------+
                              |
                    +---------v---------+
                    |    Sharechain     |
                    |   (BoltDB)        |
                    +-------------------+
```

### Packages

| Package | Description |
|---|---|
| `cmd/p2pool` | CLI entrypoint and flag parsing |
| `internal/node` | Central event loop coordinating all subsystems |
| `internal/sharechain` | Share storage, validation, difficulty adjustment, fork choice |
| `internal/pplns` | PPLNS payout calculation with finder fee and dust consolidation |
| `internal/stratum` | Stratum v1 TCP server, sessions, vardiff, version rolling |
| `internal/work` | Block template → Stratum job conversion, header reconstruction |
| `internal/p2p` | libp2p host, GossipSub, mDNS/DHT discovery, locator sync |
| `internal/bitcoin` | Bitcoin Core JSON-RPC client |
| `internal/web` | HTTP dashboard, JSON API, Prometheus metrics endpoint |
| `internal/metrics` | Prometheus gauge/counter definitions |
| `internal/types` | Share, ShareHeader, coinbase builder, payout types |
| `internal/config` | Configuration with defaults and validation |
| `pkg/util` | Double-SHA256, compact/target conversions, encoding helpers |

### Data Flow

1. **Work generation** — The node polls bitcoind for block templates, computes PPLNS payouts from the sharechain, builds a coinbase, and sends Stratum jobs to miners.
2. **Share submission** — When a miner submits work, the node reconstructs and hashes the 80-byte block header, then checks against three thresholds:
   - *Stratum difficulty* (per-miner, vardiff-adjusted) — accepted as valid work
   - *Sharechain difficulty* (global, ~30s target) — added to sharechain and broadcast via P2P
   - *Bitcoin difficulty* (full network) — submitted to bitcoind as a real block
3. **P2P propagation** — Valid shares are broadcast via GossipSub. New peers sync the full sharechain using locator-based requests.
4. **Payouts** — When a share meets Bitcoin's difficulty, its embedded coinbase transaction pays all miners in the PPLNS window proportionally.

## Building

```bash
make build          # Build to build/p2pool
make test           # Run tests
make test-race      # Run with race detector
make test-verbose   # Verbose test output
make vet            # go vet
make fmt            # go fmt
make clean          # Remove build artifacts
make run ARGS="-address bc1q..."   # Build and run
```

## License

MIT
