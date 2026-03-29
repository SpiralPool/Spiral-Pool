# Spiral Pool Architecture

> Technical architecture of Spiral Pool — a multi-coin mining pool supporting Stratum V1/V2,
> intelligent miner classification, lock-free variable difficulty, high availability, and SOLO payouts.

For security controls, see [SECURITY_MODEL.md](SECURITY_MODEL.md).
For lookup tables (ports, miner classes, CLI), see [REFERENCE.md](../reference/REFERENCE.md).

---

## Table of Contents

1. [System Overview](#1-system-overview)
2. [High-Level Architecture](#2-high-level-architecture)
3. [Miner Connection and Classification (Spiral Router)](#3-miner-connection-and-classification-spiral-router)
4. [Stratum Protocol Layer](#4-stratum-protocol-layer)
5. [Job Generation and Distribution](#5-job-generation-and-distribution)
6. [Share Submission and Validation](#6-share-submission-and-validation)
7. [Variable Difficulty Engine (VARDIFF)](#7-variable-difficulty-engine-vardiff)
8. [Share Pipeline and Persistence](#8-share-pipeline-and-persistence)
9. [Block Discovery and Submission](#9-block-discovery-and-submission)
10. [Block Maturation and Reorg Detection](#10-block-maturation-and-reorg-detection)
11. [Payout Processing](#11-payout-processing)
12. [Daemon Communication](#12-daemon-communication)
13. [Database Schema and Storage](#13-database-schema-and-storage)
14. [High Availability and Failover](#14-high-availability-and-failover)
15. [Metrics and Observability](#15-metrics-and-observability)
16. [REST API](#16-rest-api)
17. [Sentinel Monitoring System](#17-sentinel-monitoring-system)
18. [Multi-Coin and Merge Mining](#18-multi-coin-and-merge-mining)
19. [Configuration System](#19-configuration-system)
20. [End-to-End Data Flows](#20-end-to-end-data-flows)

---

## 1. System Overview

Spiral Pool is a mining pool server written in Go that supports 14 coins across SHA-256d and Scrypt proof-of-work algorithms. It serves mining jobs to connected miners via the Stratum protocol (V1 JSON-RPC and V2 binary/Noise), validates submitted shares, detects blocks, tracks block maturation, and pays miners.

### Core Design Principles

- **Reliability first** — WAL journaling, crash recovery, reorg detection, payment fencing
- **Lock-free hot path** — atomic operations on share validation and difficulty adjustment
- **Intelligent defaults** — automatic miner hardware detection sets optimal difficulty
- **Operator control** — extensive tuning knobs, per-worker metrics, HA clustering
- **IPv4 only** — all networking is IPv4. IPv6 is disabled at the OS level by the installer

### Component Map

```
+-----------------------------------------------------------------+
|                        Spiral Pool Node                         |
|                                                                 |
|  +----------+  +-----------+  +-----------+  +----------------+ |
|  | Stratum  |  |   Job     |  |  Share    |  |   Payment      | |
|  | Server   |  |  Manager  |  |  Pipeline |  |   Processor    | |
|  | (V1/V2)  |  |           |  |           |  |                | |
|  +----+-----+  +-----+-----+  +-----+-----+  +-------+--------+ |
|       |              |              |                |          |
|  +----+-----+  +-----+-----+  +-----+-----+  +-------+------+   |
|  | Spiral   |  |  Daemon   |  |  VARDIFF  |  |  Maturation  |   |
|  | Router   |  |  Client   |  |  Engine   |  |  Tracker     |   |
|  +----------+  +-----+-----+  +-----------+  +--------------+   |
|                      |                                          |
|  +----------+  +-----+-----+  +-----------+  +--------------+   |
|  | Security |  |  Node     |  |  Metrics  |  | REST API     |   |
|  | Layer    |  |  Manager  |  | Prometheus|  | Server       |   |
|  +----------+  +-----------+  +-----------+  +--------------+   |
|                                                                 |
|  +------------------------------------------------------------+ |
|  |                   PostgreSQL Database                      | |
|  |  shares_{pool}  |  blocks_{pool}  |  worker_hashrate_history_{pool}  | |
|  +------------------------------------------------------------+ |
+-----------------------------------------------------------------+
       |                    |                       |
  Miners (TCP)         Daemon (RPC/ZMQ)        Sentinel
```

Source: `internal/pool/pool.go` (Pool struct), `internal/pool/coordinator.go` (CoinPool orchestration)

---

## 2. High-Level Architecture

```
                  +-------------------------+
                  |    Blockchain Daemon     |
                  |    (per coin)            |
                  |  RPC :8332   ZMQ :28332  |
                  +------+----------+-------+
                         |          |
                  +------+----------+-------+
                  |    Node Manager          |
                  |  (multi-node failover)   |
                  +------+----------+-------+
                RPC calls|          |ZMQ block notify
                         |          |
+------------------------+----------+-----------------------------+
|                       SPIRALPOOL NODE                           |
|                                                                 |
|  +-----------+     +------------+     +-------------------+     |
|  |  Stratum  |---->| Job Manager|<----|  Daemon Client    |     |
|  |  Server   |     |            |     |  (RPC + ZMQ)      |     |
|  |           |     | GBT + merge|     +-------------------+     |
|  | V1 :3333  |     | merkle     |                               |
|  | V2 :3334  |     | job bcast  |                               |
|  | TLS :3335 |     +------+-----+                               |
|  +------+----+            |                                     |
|         |          mining.notify                                |
|         |                 |                                     |
|  +------+----+     +------+-----+     +-------------------+     |
|  | Sessions  |---->|   Share    |---->|  Ring Buffer      |     |
|  | & Workers |     | Validator  |     |  (1M, lock-free)  |     |
|  |           |     |            |     +--------+----------+     |
|  | Router    |     | job lookup |              |                |
|  | VARDIFF   |     | dup detect |     +--------+----------+     |
|  | FSM       |     | diff verify|     |  Batch Writer     |     |
|  +-----------+     +------------+     |  (WAL + COPY)     |     |
|                                       +--------+----------+     |
|                                                |                |
|  +----------------------------------------------+-----------+   |
|  |                     PostgreSQL                           |   |
|  |  shares_{pool}  blocks_{pool}  worker_hashrate_history_{pool}  |   |
|  +----------------------------------------------------------+   |
|         |                                                       |
|  +------+----+     +------------+     +-------------------+     |
|  | Payment   |     | REST API   |     |  Prometheus       |     |
|  | Processor |     |  :4000     |     |  Metrics :9100    |     |
|  |           |     +------------+     +-------------------+     |
|  | maturation|                                                  |
|  | reorg     |                                                  |
|  | SOLO pay  |                                                  |
|  +-----------+                                                  |
+---------------------------+-------------------------------------+
       |                    |
  Miners (TCP)         Sentinel (monitoring)
```

---

## 3. Miner Connection and Classification (Spiral Router)

Spiral Router automatically detects mining hardware at connection time and assigns optimal initial difficulty without requiring miners to use separate ports per device class.

Source: `internal/stratum/spiralrouter.go`

### Detection Pipeline (3-Tier Priority)

```
Miner Connects
    |
    v
+----------------------------------+
|  Tier 1: IP-Based Device Hints   |  <-- Sentinel pre-registers known devices
|  (highest priority)              |      with exact hashrate measurements
|                                  |
|  Lookup IP in hint cache         |
|  If found: Diff = HR x Target    |
|            / 2^32                |
+----------+-----------------------+
           | miss
           v
+----------------------------------+
|  Tier 2: User-Agent Matching     |  <-- 47 verified regex patterns for
|  (primary classification)        |      mining hardware & firmware
|                                  |
|  Match against pattern database  |
|  Map to MinerClass enum          |
|  Return class-specific profile   |
+----------+-----------------------+
           | no match
           v
+----------------------------------+
|  Tier 3: Default (Unknown)       |  <-- Conservative initial difficulty
|  (fallback)                      |      VarDiff adjusts from there
+----------------------------------+
```

### Miner Classification

15 SHA-256d profiles and 8 Scrypt profiles (including Unknown fallback) with separate difficulty settings per algorithm. Full tables with exact values are in [REFERENCE.md](../reference/REFERENCE.md).

### Block-Time-Aware Scaling

Initial difficulty is scaled based on the coin's block time to maintain share cadence relative to block intervals. Implemented in `scaleProfilesForBlockTime()` at `spiralrouter.go:484`.

- **DigiByte** (15s blocks): Shorter target share times for faster feedback
- **Bitcoin** (600s blocks): Longer target share times to avoid flooding

---

## 4. Stratum Protocol Layer

### Supported Protocols

| Protocol | Description |
|----------|-------------|
| Stratum V1 | JSON-RPC over TCP, newline-delimited |
| Stratum V2 | Binary encoding, Noise Protocol encryption |
| TLS | Separate TLS listener port per coin (V1 with TLS 1.2+) |

Port assignments per coin are in [REFERENCE.md](../reference/REFERENCE.md).

### V1 Handshake

```
     Miner                            Pool
       |                                |
       |---- mining.subscribe --------->|
       |<--- subscribe response --------|  (extranonce1, en2_size)
       |<--- mining.set_difficulty -----|  (initial difficulty from Router)
       |---- mining.authorize --------->|  (address.worker, password)
       |<--- authorize response --------|
       |<--- mining.notify -------------|  (job_id, prevhash, coinbase, ...)
       |                                |
       |---- mining.submit ------------>|  (worker, job_id, en2, ntime, nonce)
       |<--- submit response -----------|
       |<--- mining.set_difficulty -----|  (VARDIFF adjustment)
       |<--- mining.notify -------------|  (new block template)
```

### Stratum V2

- Noise Protocol Framework encryption (authenticated, encrypted channel)
- Binary message encoding (lower bandwidth)
- Native version rolling support
- Job template negotiation

### Connection State Machine (FSM)

```
INITIAL --> SUBSCRIBED --> AUTHORIZED --> WORKING --> DISCONNECTED
```

Any message received out of FSM order is rejected. See [SECURITY_MODEL.md](SECURITY_MODEL.md) for enforcement details.

### Session Structure

```
Session
  ID                  uint64       // Unique session ID (atomic counter)
  ExtraNonce1         string       // 4-byte unique prefix
  ExtraNonce2Size     int          // Usually 4 bytes
  WorkerName          string       // "address.worker_name"
  MinerAddress        string       // Payout address
  UserAgent           string       // e.g. "cgminer/4.12.1"
  authorized          uint32       // Atomic: 1 if authorized (deprecated bool alias exists)
  subscribed          uint32       // Atomic: 1 if subscribed (deprecated bool alias exists)
  difficultyBits      uint64       // Current session difficulty (atomic, unexported)
  prevDifficultyBits  uint64       // Previous difficulty (grace period, unexported)
  // VARDIFF state stored externally in Pool/CoinPool sessionStates sync.Map, keyed by session.ID
```

Source: `pkg/protocol/protocol.go`

---

## 5. Job Generation and Distribution

### Job Lifecycle

1. **Block notification** arrives via ZMQ (preferred, <1s latency) or RPC polling (5s fallback)
2. **Block template** fetched via `getblocktemplate` (BIP 22/23)
3. **Coinbase transaction** built with pool payout address, coinbase text, witness commitment
4. **Merkle tree** computed and cached
5. **Job ID** assigned: 2-char coin prefix + 6-char hex counter (e.g., `dg00000001`)
6. **Broadcast** `mining.notify` to all connected, authorized sessions

Source: `internal/jobs/manager.go`

### Job Freshness

- **Maximum template age**: `blockTime x 4` (minimum 1 min, maximum 10 min)
- **Circuit breaker**: Trips if template remains stale beyond threshold (stops serving jobs)
- **Recovery**: Clears when fresh template arrives

### RPC Health Tracking

| Condition | State | Pool Behavior |
|-----------|-------|---------------|
| Score >= 0.8 | Healthy | Normal operation |
| Score >= 0.5 | Degraded | Serve cached jobs, log warnings |
| Score < 0.5 | Unhealthy | Reduced confidence, increased logging |
| 5+ consecutive fails | Offline | Stop serving jobs, alert operator |

---

## 6. Share Submission and Validation

### Validation Pipeline

```
mining.submit arrives
    |
    v
1. Parse parameters (worker, job_id, extranonce2, ntime, nonce)
    |
2. Rate limit check (allowShare per-IP throttle)
    |
3. Job lookup (hash map, RWMutex read lock)
    |
4. Staleness check (job age < maxJobAge)
    |
5. Duplicate check (per-job nonce tracking)
    |
6. Reconstruct block header
    |
7. Hash header (SHA256d or Scrypt)
    |
8. Difficulty check:
   share_diff >= session_diff?  --> Accept share
   share_diff >= network_diff?  --> BLOCK FOUND
```

Source: `internal/shares/validator.go`

### Grace Period for Difficulty Changes

When difficulty increases, miners with queued work at old difficulty submit valid shares that appear "too low". The pool maintains the previous difficulty for a grace window:

- Shares for Job < DiffChangeJob: Accept at `prevDiffBits`
- Shares for Job = DiffChangeJob: Accept at `min(prevDiffBits, currentDiffBits)`
- Shares for Job > DiffChangeJob: Require `currentDiffBits`

---

## 7. Variable Difficulty Engine (VARDIFF)

### Per-Session State (Lock-Free Atomics)

```
SessionState
  difficultyBits      atomic.Uint64   // Current difficulty (float64 bits)
  shareCount          atomic.Uint64   // Total shares
  lastShareNano       atomic.Int64    // Last share timestamp (ns)
  lastRetargetNano    atomic.Int64    // Last retarget timestamp (ns)
  sharesSinceRetarget atomic.Uint64   // Shares in current window
  consecutiveNoChange atomic.Uint64   // Backoff counter for stability
```

Source: `internal/vardiff/vardiff.go:53-75`

### Retarget Algorithm

```
Every retargetTime (default 60s):

    actualTime   = elapsed / sharesSinceRetarget
    targetTime   = per-session target (from Spiral Router profile)

    variance = (actualTime - targetTime) / targetTime * 100

    if abs(variance) <= 50%:   // Hardcoded floor (config cannot go lower)
        return  // No adjustment needed

    factor = targetTime / actualTime

    if factor > 4.0:
        factor = 4.0           // Max 4x increase
    else if factor < 0.75:
        factor = 0.75          // Max 0.75x decrease

    newDiff = currentDiff * factor
    clamp(newDiff, minDiff, maxDiff)
```

The `lastRetargetNano` field uses compare-and-swap (`vardiff.go:198`) to ensure exactly one goroutine performs retarget calculation when multiple shares arrive simultaneously.

### Why Asymmetric Limits (4x up, 0.75x down)

When difficulty increases, miners continue submitting valid shares at the old (lower) difficulty until their work queue empties. These shares arrive faster than expected, which looks like "miner is slow" to the pool. Symmetric adjustment would decrease difficulty, then the miner applies the increase and submits faster, triggering another increase. The asymmetric limits break this oscillation cycle.

### Aggressive Retarget (Ramp-Up)

During initial convergence, the engine triggers aggressive retarget with asymmetric thresholds:
- Shares too fast (ratio < 0.8): trigger increase
- Shares too slow (ratio > 2.0): trigger decrease

Only applies when change exceeds 5% to avoid noise.

Source: `internal/vardiff/vardiff.go:398-475`

### Retarget Backoff

When no adjustment is needed across multiple retarget windows, `consecutiveNoChange` increments. The pool handler applies a backoff multiplier (`count + 1`, capped at 4×) to extend the retarget interval and reduce unnecessary computation.

---

## 8. Share Pipeline and Persistence

### Lock-Free Pipeline Architecture

```
Validated Shares (from multiple Stratum handler goroutines)
        |
        |  concurrent, lock-free writes
        v
+-----------------------------------+
|      Ring Buffer (MPSC)           |
|  Capacity: 1,048,576 (1 << 20)    |
|  Write latency: <100us            |
|  Implementation: atomic CAS ops   |
+-----------------------------------+
        |  single consumer drains
        v
+-----------------------------------+
|     Batch Aggregator              |
|  Batch size: ~1000 shares         |
|  Flush interval: 5s               |
+-----------------------------------+
        |
        v
+-----------------------------------+
|  Write-Ahead Log (WAL)            |
|  Path: data/wal/{poolID}/         |
|  Format: binary (current.wal)     |
|  Replay on crash recovery         |
+-----------------------------------+
        |
        v
+-----------------------------------+
|  PostgreSQL COPY Writer           |
|  Bulk inserts: ~5K-10K shares/sec |
|  Retry: up to 3x on failure       |
|  Circuit breaker on sustained fail|
+-----------------------------------+
```

Source: `internal/shares/pipeline.go:160` (ring buffer capacity), `internal/shares/wal.go` (share WAL), `internal/pool/block_wal.go` (block WAL)

### Backpressure Mechanism

| Buffer Fill % | Level | Difficulty Multiplier |
|---------------|-------|----------------------|
| < 70% | None | 1.0x (normal) |
| 70-89% | Warn | 1.5x |
| 90-97% | Critical | 2.0x |
| > 98% | Emergency | 4.0x |

---

## 9. Block Discovery and Submission

A block is discovered when `share_difficulty >= network_difficulty`.

### Block Submission Flow

1. Build complete block (header + coinbase + all transactions)
2. Write block to WAL (durability before submission)
3. Submit via RPC: `submitblock(<block_hex>)`
   - Uses `callNoRetry()` — single attempt, no retries (block submission is time-critical; any delay risks the block going stale)
4. Record in `blocks_{pool}` table (status: `pending`)
5. Broadcast `mining.notify` with `clean_jobs=true` to all miners
6. Update Prometheus metrics
7. Send Discord/Telegram alert via Sentinel

Source: `internal/pool/pool.go` (handleBlock), `internal/daemon/client.go` (submitblock)

---

## 10. Block Maturation and Reorg Detection

### Block State Machine

```
PENDING --> CONFIRMED
              |
              v (reorg)
           ORPHANED
```

> **Note:** The `paid` status exists in the database schema but is never set in SOLO mode. Since the coinbase transaction pays the miner directly at block creation time, blocks remain in `confirmed` status permanently. The `processMatureBlocks()` and `executePendingPayments()` functions are no-op stubs.

### Maturation Tracking (Payment Processor Cycle, ~10 min)

**Step 1: Snapshot Chain Tip**
- `currentHeight = getblockchaininfo().blocks`
- If tip changes mid-cycle: ABORT (TOCTOU protection)

**Step 2: Check Pending Blocks**
- For each pending block with `confirmations >= maturityDepth` (default 100):
  - Verify: `getblockhash(block.height) == block.hash`
  - 3 consecutive matches: status -> `confirmed`
  - 3 consecutive mismatches: status -> `orphaned`

**Step 3: Deep Reorg Check (every 10th cycle, ~100 min)**
- Re-verify ALL confirmed (unpaid) blocks up to 1000 confirmations deep
- 3 consecutive hash mismatches: status -> `orphaned`, alert operator

Source: `internal/payments/processor.go`

---

## 11. Payout Processing

### Scheme: SOLO (Coinbase Payout)

100% of block reward (subsidy + transaction fees) goes directly to the miner via the **coinbase transaction** embedded in the block template. No separate payment RPC call is needed — the blockchain pays the miner directly.

### Maturation Flow

1. Block found — miner's address is already in the coinbase output
2. Payment processor tracks confirmations (status: `pending`)
3. After 100+ confirmations and 3 stable hash checks → `confirmed`
4. Block remains in `confirmed` status permanently (coinbase TX is the payment)

> **Advisory lock:** The payment processor acquires a PostgreSQL advisory lock (per-pool FNV hash) at the start of each cycle for HA fencing, but the actual `confirmed → paid` transition is a no-op in SOLO mode.

Payment fencing is described in [SECURITY_MODEL.md](SECURITY_MODEL.md).

Source: `internal/payments/processor.go`

---

## 12. Daemon Communication

### RPC Methods

| Method | Purpose | Frequency |
|--------|---------|-----------|
| `getblocktemplate` | Fetch block template | On ZMQ notify or periodic refresh (blockTime/3, min 5s) |
| `submitblock` | Submit found block | On block discovery |
| `getblockchaininfo` | Chain height + network state | Every poll cycle (1s) |
| `getblockhash` | Hash at specific height | Every payment cycle |
| `validateaddress` | Validate pool payout addr | At startup (once) |
| `getnetworkinfo` | Daemon version/capabilities | At startup (once) |

### Block Notification Modes

| Mode | Mechanism | Latency |
|------|-----------|---------|
| ZMQ (preferred) | `zmq:hashblock` push | <1 second |
| RPC Polling (fallback) | `getblockchaininfo` every 1s | up to 1 second |

Strategy: Start with polling, promote to ZMQ once connection stable.

### Retry Policy

Max retries: 3, initial backoff: 500ms, max backoff: 10s, factor: 2.0x

---

## 13. Database Schema and Storage

### PostgreSQL Tables (auto-created per pool)

```sql
-- Block records (per pool)
CREATE TABLE blocks_{poolID} (
    id                          BIGSERIAL PRIMARY KEY,
    poolid                      VARCHAR(64) NOT NULL,
    blockheight                 BIGINT NOT NULL,
    networkdifficulty           DOUBLE PRECISION NOT NULL,
    status                      VARCHAR(32) NOT NULL DEFAULT 'pending',
    type                        VARCHAR(32),
    confirmationprogress        DOUBLE PRECISION DEFAULT 0,
    effort                      DOUBLE PRECISION,
    transactionconfirmationdata TEXT,
    miner                       VARCHAR(256) NOT NULL,
    reward                      DECIMAL(28,12),
    source                      VARCHAR(64),
    hash                        VARCHAR(128),
    created                     TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_{poolID}_status  ON blocks_{poolID}(status);
CREATE INDEX idx_{poolID}_miner   ON blocks_{poolID}(miner);
CREATE INDEX idx_{poolID}_created ON blocks_{poolID}(created);
CREATE INDEX idx_{poolID}_height  ON blocks_{poolID}(blockheight);
-- Migration V8 adds: orphan_mismatch_count INTEGER DEFAULT 0
-- Migration V9 adds: stability_check_count INTEGER DEFAULT 0
-- Migration V10 adds: last_verified_tip VARCHAR(64) DEFAULT ''

-- Share records (high-volume, per pool)
CREATE TABLE shares_{poolID} (
    id                BIGSERIAL PRIMARY KEY,
    poolid            VARCHAR(64) NOT NULL,
    blockheight       BIGINT NOT NULL,
    difficulty        DOUBLE PRECISION NOT NULL,
    networkdifficulty DOUBLE PRECISION NOT NULL,
    miner             VARCHAR(256) NOT NULL,
    worker            VARCHAR(256),
    useragent         VARCHAR(256),
    ipaddress         VARCHAR(64),
    source            VARCHAR(64),
    created           TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_{poolID}_miner       ON shares_{poolID}(miner);
CREATE INDEX idx_{poolID}_created     ON shares_{poolID}(created);
CREATE INDEX idx_{poolID}_blockheight ON shares_{poolID}(blockheight);

-- Global miner registry
CREATE TABLE miners (
    id              BIGSERIAL PRIMARY KEY,
    poolid          VARCHAR(64) NOT NULL,
    address         VARCHAR(256) NOT NULL,
    created         TIMESTAMP NOT NULL DEFAULT NOW(),
    lastactivity    TIMESTAMP,
    UNIQUE(poolid, address)
);

-- Worker hashrate history (per pool, migration V5)
CREATE TABLE worker_hashrate_history_{poolID} (
    id                BIGSERIAL PRIMARY KEY,
    miner             VARCHAR(256) NOT NULL,
    worker            VARCHAR(256) NOT NULL DEFAULT 'default',
    hashrate          DOUBLE PRECISION NOT NULL,
    shares_submitted  BIGINT NOT NULL DEFAULT 0,
    shares_accepted   BIGINT NOT NULL DEFAULT 0,
    shares_rejected   BIGINT NOT NULL DEFAULT 0,
    total_difficulty  DOUBLE PRECISION NOT NULL DEFAULT 0,
    time_window       VARCHAR(8) NOT NULL DEFAULT '1m',
    timestamp         TIMESTAMP NOT NULL DEFAULT NOW()
);

-- V2 per-pool tables (created by migrate_v2.go):
-- node_health_{poolID}      Per-pool node health tracking
-- failover_events_{poolID}  Per-pool HA failover events

-- Additional tables (created by migrate.go, not per-pool):
-- schema_migrations    Tracks applied migration versions
-- poolstats            Aggregate pool statistics
-- payments             Payment records
-- balances             Miner balance tracking (migration V2)
-- miner_settings       Per-miner configuration (migration V3)

-- Note: HA state is persisted to ha_state.json (not a database table)
```

Source: `internal/database/migrate.go`, `internal/database/migrate_v2.go`

Note: Column names use no underscores in Go code (`poolid`, `blockheight`).

### Auxiliary (Merge-Mined) Pool Tables

Auxiliary pools created by merge mining get their own set of per-pool tables. The aux pool ID is constructed by appending the lowercase aux symbol to the parent pool ID:

```
auxPoolID = {parentPoolID}_{auxSymbol}
```

Source: `internal/pool/coordinator.go:222`

Each aux pool gets the same table set as a parent pool: `blocks_{auxPoolID}`, `shares_{auxPoolID}`, `worker_hashrate_history_{auxPoolID}`.

**Example table names for all merge mining pairs:**

| Parent | Aux | Aux Pool ID | Blocks Table |
|--------|-----|-------------|--------------|
| BTC | NMC | `btc_sha256_1_nmc` | `blocks_btc_sha256_1_nmc` |
| BTC | SYS | `btc_sha256_1_sys` | `blocks_btc_sha256_1_sys` |
| BTC | XMY | `btc_sha256_1_xmy` | `blocks_btc_sha256_1_xmy` |
| BTC | FBTC | `btc_sha256_1_fbtc` | `blocks_btc_sha256_1_fbtc` |
| LTC | DOGE | `ltc_scrypt_1_doge` | `blocks_ltc_scrypt_1_doge` |
| LTC | PEP | `ltc_scrypt_1_pep` | `blocks_ltc_scrypt_1_pep` |

Pool ID validation: `^[a-zA-Z_][a-zA-Z0-9_]{0,62}$` (no hyphens — not valid PostgreSQL identifiers).

### Write Performance

| Operation | Method | Throughput |
|-----------|--------|-----------|
| Share inserts | PostgreSQL COPY | ~5K-10K shares/sec |
| Block inserts | Standard INSERT | Negligible volume |
| Worker updates | UPSERT | Per-session cadence |

---

## 14. High Availability and Failover

> **Bare metal / self-hosted VMs only.** HA is not supported on cloud or VPS deployments. Keepalived VRRP (used for VIP failover) requires broadcast/multicast MAC election, which is blocked by cloud hypervisors. Both the installer and `spiralctl ha enable` hard-block HA setup when a cloud environment is detected.

### HA Architecture

```
+----------------+          +----------------+
|   Node 1       |          |   Node 2       |
|   (MASTER)     |          |   (BACKUP)     |
|                |          |                |
|  Spiral Pool   |<-- WAL ->|  Spiral Pool   |
|  PostgreSQL    |  stream  |  PostgreSQL    |
|  (read/write)  |  repl    |  (read-only)   |
|                |          |                |
|  Payments: ON  |          |  Payments: OFF |
+-------+--------+          +-------+--------+
        |                           |
        |     +----------+          |
        +---->|   VIP    |<---------+
              | Virtual  |  <-- Miners connect here
              | IP Addr  |
              +----------+
```

### Failover Manager

Health check: UDP broadcast heartbeats (default 30s interval) with HTTP `/status` fallback (port 5354). Primary declared dead after `FailoverTimeout` (default 90s, ≥ 3 missed heartbeats). Eligible backup with highest priority starts election and acquires VIP.

### HA Components

| Component | HA Behavior |
|-----------|-------------|
| `ha-role-watcher.sh` | Polls every 5s, controls service start/stop |
| `ha-service-control.sh` | Starts/stops spiralsentinel + spiraldash |
| Sentinel | Defense-in-depth: `is_master_sentinel()` suppresses alerts on non-MASTER |
| Dashboard | No built-in HA check; relies on systemd service control |
| Docker | Known limitation: no role watcher. Sentinel alerts safe, services duplicated |

Source: `internal/ha/`, `scripts/linux/ha-role-watcher.sh`, `scripts/linux/ha-service-control.sh`

---

## 15. Metrics and Observability

### Prometheus Metrics (endpoint `:9100/metrics`)

Source: `internal/metrics/prometheus.go` — **104 metrics** registered via `prometheus.MustRegister()`.

#### Connection Metrics
```
stratum_connections_total              counter
stratum_connections_active             gauge
stratum_connections_rejected_total     counter
```

#### Share Metrics
```
stratum_shares_submitted_total         counter
stratum_shares_accepted_total          counter
stratum_shares_rejected_total          counter_vec  {reason}
stratum_shares_stale_total             counter
```

#### Share Batch Loss Metrics (HA Failover Observability)
```
stratum_share_batches_dropped_total    counter
stratum_shares_in_dropped_batch_total  counter
stratum_share_batch_retries_total      counter
stratum_share_batch_loss_rate          gauge
```

#### Block Metrics
```
stratum_blocks_found_total             counter
stratum_blocks_confirmed_total         counter
stratum_blocks_orphaned_total          counter
stratum_blocks_submitted_total         counter
stratum_blocks_submission_failed_total counter
stratum_blocks_false_rejection_total   counter
stratum_block_submit_retries_total     counter
stratum_block_stale_race_total         counter
stratum_block_submit_latency_ms        histogram
stratum_block_rejections_total         counter_vec  {reason}
stratum_block_height_aborts_total      counter
stratum_block_deadline_aborts_total    counter
stratum_block_deadline_usage_ratio     histogram
stratum_block_status_guard_rejections_total  counter
stratum_target_source_used_total       counter_vec  {source}
```

#### Per-Coin Block Metrics (V2 Multi-Coin)
```
stratum_blocks_found_by_coin_total              counter_vec  {coin}
stratum_blocks_submitted_by_coin_total          counter_vec  {coin}
stratum_blocks_orphaned_by_coin_total           counter_vec  {coin}
stratum_blocks_confirmed_by_coin_total          counter_vec  {coin}
stratum_blocks_submission_failed_by_coin_total  counter_vec  {coin}
```

#### Hashrate Metrics
```
stratum_hashrate_pool_hps              gauge
stratum_hashrate_network_hps           gauge
```

#### Difficulty Metrics
```
stratum_network_difficulty             gauge
stratum_vardiff_adjustments_total      counter
stratum_best_share_difficulty          gauge
stratum_avg_share_difficulty           gauge
stratum_jobs_dispatched_total          counter
```

#### Latency Metrics
```
stratum_share_validation_seconds       histogram
stratum_job_broadcast_seconds          histogram
stratum_db_write_seconds               histogram
stratum_rpc_call_seconds               histogram
```

#### Payment Metrics
```
stratum_payments_pending_count         gauge
stratum_payments_sent_total            counter
stratum_payments_failed_total          counter
stratum_payment_processor_failed_cycles  gauge
stratum_paid_block_reorgs_total        counter
```

#### System Metrics
```
stratum_goroutines_count               gauge
stratum_memory_alloc_bytes             gauge
stratum_buffer_usage_ratio             gauge     (0-1)
```

#### ZMQ Metrics
```
stratum_zmq_connected                  gauge
stratum_zmq_messages_received_total    counter
stratum_zmq_reconnects_total           counter
stratum_zmq_health_status              gauge
stratum_zmq_last_message_age_seconds   gauge
stratum_block_notify_mode              gauge
```

#### Per-Worker Metrics (High Cardinality)
```
stratum_worker_hashrate_hps            gauge_vec    {miner, worker}
stratum_worker_shares_total            counter_vec  {miner, worker, status}
stratum_worker_connected               gauge_vec    {miner, worker}
stratum_worker_difficulty              gauge_vec    {miner, worker}
stratum_workers_active                 gauge
```

#### Circuit Breaker Metrics
```
stratum_circuit_breaker_state          gauge
stratum_circuit_breaker_blocked_total  counter
stratum_circuit_breaker_transitions_total  counter
```

#### Aux Block Metrics (Merge Mining)
```
stratum_aux_blocks_submitted_total     counter_vec  {coin}
stratum_aux_blocks_failed_total        counter_vec  {coin, reason}
```

#### WAL (Write-Ahead Log) Metrics
```
stratum_wal_replay_count_total         counter
stratum_wal_replay_duration_seconds    gauge
stratum_wal_write_errors_total         counter
stratum_wal_commit_errors_total        counter
stratum_wal_sync_errors_total          counter
stratum_wal_file_size_bytes            gauge
```

#### Backpressure Metrics
```
stratum_backpressure_level             gauge     (0-3)
stratum_backpressure_level_changes_total  counter
stratum_backpressure_buffer_fill_percent  gauge
stratum_backpressure_difficulty_multiplier  gauge
```

#### HA Cluster Metrics
```
stratum_ha_cluster_state               gauge_vec    {state}
stratum_ha_node_role                   gauge_vec    {role}
stratum_ha_failovers_total             counter
stratum_ha_elections_total             counter
stratum_ha_flap_detected_total         counter
stratum_ha_partition_detected_total    counter
stratum_ha_nodes_total                 gauge
stratum_ha_nodes_healthy               gauge
```

#### Database HA Metrics
```
stratum_db_failovers_total             counter
stratum_db_active_node                 gauge_vec    {node_id}
stratum_db_circuit_breaker_state       gauge_vec    {state}
stratum_db_block_queue_length          gauge
```

#### Redis HA Metrics
```
stratum_redis_healthy                  gauge
stratum_redis_reconnects_total         counter
stratum_redis_fallback_active          gauge
```

#### Daemon Health Metrics
```
stratum_daemon_block_height            gauge_vec    {coin}
stratum_daemon_peer_count              gauge_vec    {coin}
stratum_daemon_chain_tip_stale_seconds gauge_vec    {coin}
```

#### Block Maturity Metrics
```
stratum_blocks_pending_maturity_count  gauge
stratum_blocks_oldest_pending_age_seconds  gauge
```

#### Sentinel Alerting Metrics
```
stratum_sentinel_alerts_total          counter_vec  {type, severity, coin}
stratum_sentinel_alerts_firing         gauge
stratum_sentinel_check_duration_seconds  histogram
stratum_sentinel_webhook_errors_total  counter
stratum_sentinel_wal_pending_age_seconds  gauge
stratum_sentinel_nodes_down            gauge_vec    {coin}
stratum_sentinel_hashrate_change_percent  gauge_vec    {coin}
stratum_sentinel_connection_drop_percent  gauge_vec    {coin}
```

---

## 16. REST API

Endpoints documented in [REFERENCE.md](../reference/REFERENCE.md).

### Example Response: Pool Stats

```json
{
  "id": "digibyte",
  "symbol": "DGB",
  "blockHeight": 8245000,
  "networkDifficulty": 1234.56,
  "poolHashrate": 1.234e15,
  "activeWorkers": 245,
  "connectedMiners": 1823,
  "blocksFound24h": 3,
  "lastBlockHeight": 8244980
}
```

Source: `internal/api/server_v2.go`

---

## 17. Sentinel Monitoring System

Sentinel is a Python companion service providing device-level monitoring and alerting.

```
+----------------------------------------------------+
|                     Sentinel                       |
|                                                    |
|  Device Discovery                                  |
|  - AxeOS HTTP (BitAxe, NerdQAxe++, etc.)           |
|  - CGMiner (Avalon, Antminer, Whatsminer)          |
|  - Goldshell HTTP (Mini DOGE, LT5, LT6)            |
|  - Braiins / Vnish / LuxOS firmware APIs           |
|  - Pool API polling (ESP32 lottery miners)         |
|  - Network scan (ICMP + TCP probes)                |
|                                                    |
|  Health Monitoring                                 |
|  - Temperature, hashrate, fan speed                |
|  - Uptime tracking, network health                 |
|                                                    |
|  Alert System                                      |
|  - Discord webhooks / Telegram bot                 |
|  - Block found / miner offline alerts              |
|  - Periodic hashrate reports                       |
|                                                    |
|  Device Hint Registration                          |
|  - Pre-registers discovered devices for            |
|    Tier 1 Spiral Router classification             |
|    (IP -> exact hashrate mapping)                  |
+----------------------------------------------------+
```

Source: `src/sentinel/SpiralSentinel.py`, `src/dashboard/dashboard.py`

**Monitoring modes:** Most miners are monitored by directly polling their HTTP or CGMiner API for temperature, hashrate, uptime, and health data. ESP32 lottery miners (NerdMiner, BitMaker, etc.) are an exception — they have no device API, so Sentinel monitors them indirectly by polling the pool's stratum connections endpoint (`/api/pools/{id}/connections`). This provides online/offline status, hashrate, and share data, but not temperature, fan speed, or power consumption. ESP32 miners must be manually added via `spiralctl scan --add <IP>`. See [MINER_SUPPORT.md](../reference/MINER_SUPPORT.md) for full details.

---

## 18. Multi-Coin and Merge Mining

### Supported Coins (14 total)

| Coin | Symbol | Algorithm | Solo-Minable | Merge-Mined With |
|------|--------|-----------|--------------|------------------|
| Bitcoin | BTC | SHA-256d | Yes | Parent chain |
| Bitcoin Cash | BCH | SHA-256d | Yes | -- |
| DigiByte | DGB | SHA-256d | Yes | -- |
| Bitcoin II | BC2 | SHA-256d | Yes | -- |
| Namecoin | NMC | SHA-256d | Yes | BTC (chain ID 1) |
| Syscoin | SYS | SHA-256d | No | BTC (chain ID 16) |
| Myriad | XMY | SHA-256d | Yes | BTC (chain ID 90) |
| Fractal Bitcoin | FBTC | SHA-256d | Yes | BTC (chain ID 8228) |
| Q-BitX | QBX | SHA-256d | Yes | -- |
| Litecoin | LTC | Scrypt | Yes | Parent chain |
| Dogecoin | DOGE | Scrypt | Yes | LTC (chain ID 98) |
| DigiByte-Scrypt | DGB-SCRYPT | Scrypt | Yes | -- |
| PepeCoin | PEP | Scrypt | Yes | LTC (chain ID 63) |
| Catcoin | CAT | Scrypt | Yes | -- |

Syscoin cannot solo mine due to CbTx/quorum commitment requirements. It is supported only as a merge-mining auxiliary chain with BTC as parent.

Source: `internal/coin/*.go`, `config/coins.manifest.yaml`

### Merge Mining (AuxPoW)

```
Parent Chain (e.g., BTC)
    |
    |  Block template includes auxiliary chain commitments
    v
+-------------------------------+
|  AuxPoW Module                |
|  1. Fetch aux chain templates |
|  2. Build AuxPoW commitment   |
|     in parent coinbase        |
|  3. When parent block found:  |
|     - Submit to parent chain  |
|     - Build AuxPoW proof      |
|     - Submit to aux chain     |
+-------------------------------+
```

Source: `internal/auxpow/`

### Adding New Coins

Automated via CLI:
```
spiralpool-add-coin -s NEWCOIN -g https://github.com/newcoin/newcoin
```

See [COIN_ONBOARDING_SPEC.md](../development/COIN_ONBOARDING_SPEC.md) for complete documentation.

---

## 19. Configuration System

Configuration skeleton and all fields are documented in [REFERENCE.md](../reference/REFERENCE.md).

Source: `internal/config/v2.go` (production), `internal/config/config.go` (V1 legacy)

---

## 20. End-to-End Data Flows

### Flow 1: ESP32 Miner Connects and Submits First Share

1. TCP connect to pool:3333
2. Create Session (atomic session ID, unique extranonce1)
3. Spiral Router classifies "esp32-miner/1.0" -> MinerClass.Lottery
4. Profile: InitialDiff=0.001, MinDiff=0.0001, MaxDiff=100, TargetShareTime=60s
5. Initialize VARDIFF SessionState (all atomic fields)
6. Subscribe response + set_difficulty(0.001)
7. Authorize "DAddr.esp32worker"
8. mining.notify with job
9. Miner hashes ~60 seconds, submits share
10. Validate: rate limit -> job lookup -> dup check -> hash -> diff check
11. share_diff >= 0.001 -> VALID, < network_diff -> not a block
12. Ring buffer push (lock-free CAS), metrics update
13. VARDIFF: 1 share in 60s, ratio ~1.0, within 50% variance -> no adjustment
14. Batch writer drains ring buffer -> WAL -> PostgreSQL COPY

### Flow 2: Antminer S19 Finds a Block

1. Share hash meets network difficulty -> BLOCK FOUND
2. Build complete block (header + coinbase + all TXs)
3. Write to WAL (durability)
4. `submitblock` -> accepted
5. INSERT into blocks table (status: "pending")
6. Broadcast mining.notify (clean_jobs=true)
7. Prometheus: blocks_found_total++
8. Sentinel: Discord/Telegram alert
9. Payment processor (10-min cycles) checks confirmations
10. After 100+ confirmations and 3 stable hash checks -> "confirmed"
11. Block remains "confirmed" permanently (SOLO: coinbase already paid miner at block creation)

---

## Performance Characteristics

| Operation | Performance | Bottleneck |
|-----------|------------|-----------|
| Share validation | <100us per share | CPU (atomic ops) |
| Share persistence | 5K-10K shares/sec | PostgreSQL COPY |
| Job broadcast | <100ms to all miners | Network I/O |
| Block submission | 1-5 seconds | Daemon RPC |

| Component | Memory Usage |
|-----------|-------------|
| Per session | ~1 KB |
| Share ring buffer | ~256 MB (1M x 256 bytes) |
| Job cache | ~100 KB (100 jobs x 1 KB) |
| Metrics | <10 MB |

---

*Spiral Pool — Phi Hash Reactor 2.0.1*
