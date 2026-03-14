# Spiral Stratum - Test Suite Reference

**Version**: 1.0
**Updated**: 2026-03-01
**Total Test Files**: 238
**Total Test Functions**: 3,500+

---

## Table of Contents

1. [Quick Start](#quick-start)
2. [Test Architecture](#test-architecture)
3. [Complete Test Inventory](#complete-test-inventory)
4. [Chaos Tests & Production Bug Findings](#chaos-tests--production-bug-findings)
5. [Production Fixes Applied](#production-fixes-applied)
6. [Execution Commands](#execution-commands)
7. [Related Documentation](#related-documentation)

---

## Quick Start

```bash
# Run all tests
go test ./internal/... ./pkg/...

# Run all tests with race detector
go test -race ./internal/... ./pkg/...

# Run chaos tests only
go test -v -race -run "TestChaos" -timeout 120s ./internal/...

# Run integration stress tests
go test -v -race -run "TestExt_" -timeout 300s ./internal/integration/...

# Run fuzz tests (60 seconds)
go test -fuzz=. -fuzztime=60s ./internal/auxpow/...
go test -fuzz=. -fuzztime=60s ./internal/shares/...
```

---

## Test Architecture

Tests are organized in three tiers:

| Tier | Purpose | Naming Convention | Typical Duration |
|------|---------|-------------------|------------------|
| **Unit** | Single function/struct correctness | `Test<FunctionName>` | < 1s per test |
| **Integration** | Cross-package interaction, lifecycle | `TestExt_`, `Test<Feature>_Integration` | 1-10s per test |
| **Chaos/Stress** | Concurrency, race conditions, edge cases | `TestChaos_`, `TestChaos<Name>` | 1-30s per test |

All tests are designed to run with `go test -race` and should produce zero data race warnings.

---

## Complete Test Inventory

> **Note:** This inventory highlights key test files per package. Some packages contain additional test files beyond those listed (particularly `pool/`, `shares/`, `payments/`, and `jobs/`). Run `find internal/ pkg/ -name '*_test.go' | wc -l` for the exact current count.

### API (`internal/api/`) — 3 files

| File | Focus |
|------|-------|
| `handlers_test.go` | REST API handler correctness |
| `security_test.go` | API authentication, authorization |
| `workers_test.go` | Worker stats API endpoints |

### AuxPoW / Merge Mining (`internal/auxpow/`) — 4 files

| File | Focus |
|------|-------|
| `auxpow_test.go` | AuxPoW proof construction and verification |
| `manager_test.go` | AuxPoW manager lifecycle |
| `merkle_fuzz_test.go` | Fuzz testing for Merkle tree building |
| `proof_edge_cases_test.go` | Edge cases: empty trees, single leaf, chain ID collisions |

### Coin Support (`internal/coin/`) — 19 files

| File | Focus |
|------|-------|
| `coin_test.go` | Coin interface compliance |
| `registry_test.go` | Coin registry lookup |
| `manifest_test.go` | Coin manifest validation |
| `multi_algorithm_test.go` | Multi-algorithm coin support (DGB 5-algo) |
| `blockchain_verification_test.go` | Block header verification per coin |
| `bitcoin_test.go` | BTC-specific params |
| `bitcoincash_test.go` | BCH-specific params |
| `bitcoinii_test.go` | BTCII-specific params |
| `catcoin_test.go` | CAT-specific params |
| `digibyte_test.go` | DGB SHA256d params |
| `digibyte_scrypt_test.go` | DGB Scrypt params |
| `dogecoin_test.go` | DOGE params and merge-mining |
| `fractalbtc_test.go` | FBTC-specific params |
| `qbitx_test.go` | QBX-specific params |
| `litecoin_test.go` | LTC Scrypt params |
| `myriad_test.go` | XMY multi-algo params |
| `namecoin_test.go` | NMC merge-mining params |
| `pepecoin_test.go` | PEP-specific params |
| `syscoin_test.go` | SYS merge-mining params |

### Config (`internal/config/`) — 5 files (key files shown)

| File | Focus |
|------|-------|
| `config_test.go` | Configuration parsing and validation |
| `config_error_test.go` | Invalid config detection |

### Crypto (`internal/crypto/`) — 3 files

| File | Focus |
|------|-------|
| `scrypt_test.go` | Scrypt hash correctness (known vectors) |
| `sha256d_test.go` | Double-SHA256 correctness |
| `stress_test.go` | Crypto hash throughput under load |

### Daemon / RPC (`internal/daemon/`) — 7 files (key files shown)

| File | Focus |
|------|-------|
| `client_test.go` | RPC client method correctness |
| `zmq_test.go` | ZMQ listener lifecycle |
| `submit_verify_test.go` | Block submission + verification flow |
| `network_failure_test.go` | RPC client under network errors |
| `chaos_zmq_test.go` | **CHAOS TEST 3**: ZMQ recovery loop double-spawn race |

### Database (`internal/database/`) — 16 files

| File | Focus |
|------|-------|
| `manager_test.go` | DB manager lifecycle |
| `manager_unit_test.go` | DB manager unit tests |
| `manager_ha_test.go` | DB manager HA scenarios |
| `db_integration_test.go` | Full DB integration (requires Postgres) |
| `circuitbreaker_test.go` | Circuit breaker state machine |
| `workers_test.go` | Worker stats persistence |
| `postgres_consistency_test.go` | Postgres constraint enforcement |
| `postgres_logic_test.go` | Postgres business logic |
| `postgres_integration_test.go` | Postgres integration tests |
| `postgres_advisory_lock_test.go` | Advisory lock behavior |
| `replication_test.go` | Read replica failover |
| `ha_failover_test.go` | HA advisory lock failover |
| `chaos_compound_test.go` | **CHAOS TEST 10**: Circuit breaker + block queue compound stress |
| `blockqueue_test.go` | Block queue operations |
| `status_guard_v12_test.go` | Status guard V12 logic |
| `migrate_unit_test.go` | Migration unit tests |

### Discovery (`internal/discovery/`) — 2 files

| File | Focus |
|------|-------|
| `discovery_test.go` | Peer discovery protocol |
| `failover_test.go` | Discovery failover behavior |

### Explorer (`internal/explorer/`) — 1 file

| File | Focus |
|------|-------|
| `explorer_test.go` | Block explorer API |

### High Availability (`internal/ha/`) — 5 files (key files shown)

| File | Focus |
|------|-------|
| `ha_integration_test.go` | HA cluster integration |
| `vip_test.go` | Virtual IP management |
| `chaos_test.go` | HA chaos: leader election, split-brain, node failure |

### Integration Tests (`internal/integration/`) — 5 files

| File | Focus |
|------|-------|
| `integration_test.go` | Basic cross-package integration |
| `lifecycle_e2e_test.go` | End-to-end pool lifecycle |
| `chaos_stress_test.go` | **32 tests**: HeightEpoch, DuplicateTracker, VarDiff, CircuitBreaker, WAL, RateLimiter, BlockQueue, multi-coin |
| `chaos_stress_extended_test.go` | **36 tests**: Goroutine lifecycle, connection churn, reorg storms, memory exhaustion, crash recovery, boundary conditions |
| `build_verification_test.go` | Build verification smoke tests |

### Jobs (`internal/jobs/`) — 15 files (key files shown)

| File | Focus |
|------|-------|
| `manager_test.go` | Job manager lifecycle |
| `manager_v2_test.go` | V2 job manager |
| `heightctx_test.go` | HeightContext basic operations |
| `heightctx_comprehensive_test.go` | 25 HeightContext edge cases |
| `heightctx_reorg_test.go` | Same-height reorg detection (12 tests) |
| `reorg_test.go` | Reorg handling in job dispatch |
| `coinbase_routing_test.go` | Coinbase transaction routing |
| `concurrency_chaos_test.go` | Job manager concurrency (14 tests) |
| `defense_in_depth_test.go` | Edge cases, defense-in-depth (17 tests) |
| `multicoin_isolation_test.go` | Multi-coin job isolation (12 tests) |
| `stress_test.go` | Job manager under sustained load |
| `heightctx_paranoid_test.go` | Paranoid HeightContext edge cases |

### Metrics (`internal/metrics/`) — 3 files

| File | Focus |
|------|-------|
| `prometheus_test.go` | Prometheus metric registration and values |
| `v43_v44_v45_test.go` | Metrics version compatibility tests |
| `metrics_comprehensive_test.go` | Comprehensive metrics coverage |

### Node Manager (`internal/nodemanager/`) — 7 files (key files shown)

| File | Focus |
|------|-------|
| `manager_test.go` | Node selection, health scoring |
| `manager_integration_test.go` | Multi-node failover integration |
| `chaos_failover_test.go` | **CHAOS TEST 9**: Concurrent failover during SelectNode |
| `chaos_health_fullpath_test.go` | Full-path health check chaos |

### Observability (`internal/observability/`) — 1 file

| File | Focus |
|------|-------|
| `observability_test.go` | Logging, tracing setup |

### Payments (`internal/payments/`) — 31 files (key files shown)

| File | Focus |
|------|-------|
| `processor_test.go` | Payment processor lifecycle |
| `processor_orphan_test.go` | Orphan detection: delayed orphaning, stability window (24 tests) |
| `processor_audit_test.go` | Payment processor audit scenarios |
| `processor_paranoid_test.go` | Paranoid payment edge cases |
| `solo_db_resilience_test.go` | Solo DB resilience under failure |
| `solo_edge_reality_test.go` | Solo edge case reality checks |
| `solo_vector_height_test.go` | Solo block height vector tests |
| `solo_vector_load_test.go` | Solo payment under load |

### Pool (`internal/pool/`) — 42 files (key files shown)

| File | Focus |
|------|-------|
| `pool_test.go` | Core pool operations |
| `coinpool_test.go` | Per-coin pool lifecycle |
| `coordinator_test.go` | Multi-coin coordinator |
| `production_test.go` | Production config validation |
| `block_race_test.go` | Block submission race conditions |
| `cross_coin_isolation_test.go` | Cross-coin contamination prevention |
| `heightcontext_integration_test.go` | HeightContext integration with pool |
| `heightcontext_stress_test.go` | HeightContext under sustained load |
| `multinode_divergence_test.go` | Multi-node chain divergence handling |
| `pool_race_multi_algo_test.go` | Multi-algorithm race conditions |
| `pool_stress_validation_test.go` | Pool validation under stress |
| `block_recovery_paranoid_test.go` | Paranoid block recovery scenarios |
| `block_submission_paranoid_test.go` | Paranoid block submission edge cases |
| `block_wal_audit_test.go` | WAL audit and integrity checks |
| `block_wal_solo_loss_test.go` | WAL solo block loss prevention |
| `block_recovery_test.go` | Block recovery after crash |
| `chaos_test.go` | **12 chaos tests**: 100 concurrent miners, rapid blocks, ZMQ lag, template delays, retry overlap, multi-coin, aux race, sustained pressure |
| `chaos_coordinator_test.go` | **CHAOS TEST 8**: Coordinator lock ordering race |

### Security (`internal/security/`) — 3 files

| File | Focus |
|------|-------|
| `ratelimiter_test.go` | Rate limiter correctness |
| `attack_simulation_test.go` | Simulated attack patterns |
| `persistence_test.go` | Ban persistence and recovery |

### Shares (`internal/shares/`) — 34 files (key files shown)

| File | Focus |
|------|-------|
| `validator_test.go` | Share validation correctness |
| `validator_v2_test.go` | V2 validator with coin integration |
| `validator_security_test.go` | Validator security edge cases |
| `pipeline_test.go` | Share pipeline lifecycle |
| `pipeline_v2_test.go` | V2 pipeline with circuit breaker |
| `difficulty_math_test.go` | Difficulty calculation correctness |
| `block_detection_test.go` | Block-meets-target detection |
| `backpressure_test.go` | Pipeline backpressure handling |
| `submission_lifecycle_test.go` | Share submission end-to-end |
| `solo_invariants_test.go` | Solo mining invariants |
| `memory_leak_test.go` | Memory bounds verification |
| `time_pathology_test.go` | Clock anomaly handling |
| `malicious_miner_test.go` | Malicious share patterns |
| `crash_consistency_test.go` | Crash recovery correctness |
| `stress_test.go` | Pipeline under sustained load |
| `fuzz_extended_test.go` | Extended fuzz testing |
| `chaos_helpers_test.go` | Shared helpers for chaos tests |
| `chaos_wal_test.go` | **CHAOS TESTS 1 & 7**: WAL rotation crash, commit sync failure |
| `chaos_pipeline_test.go` | **CHAOS TESTS 2 & 5**: Pipeline saturation, shutdown during backoff |
| `chaos_dedup_test.go` | **CHAOS TEST 4**: DuplicateTracker concurrent flood |
| `chaos_wal_truncation_test.go` | WAL truncation recovery |
| `rebuild_test.go` | Pipeline rebuild after failure |

### Stratum Protocol (`internal/stratum/`) — 22 files (key files shown)

| File | Focus |
|------|-------|
| `server_test.go` | Stratum server lifecycle |
| `server_tls_test.go` | TLS connection handling |
| `spiralrouter_test.go` | Spiral Router difficulty profiling |
| `e2e_virtual_miner_test.go` | End-to-end with virtual miner |
| `verify_all_miners_test.go` | All miner types verification |
| `chaos_server_test.go` | **CHAOS TEST 6**: Partial buffer TOCTOU race |
| **V1 Protocol** | |
| `v1/handler_test.go` | V1 JSON-RPC message handling |
| `v1/fuzz_protocol_test.go` | V1 protocol fuzzing |
| `v1/stress_test.go` | V1 handler under load |
| **V2 Protocol** | |
| `v2/server_test.go` | V2 server lifecycle |
| `v2/adapter_test.go` | V1↔V2 protocol adapter |
| `v2/encoding_test.go` | Binary encoding correctness |
| `v2/noise_test.go` | Noise protocol handshake |
| `v2/session_test.go` | V2 session management |
| `v2/types_test.go` | V2 type serialization |

### VarDiff (`internal/vardiff/`) — 2 files

| File | Focus |
|------|-------|
| `vardiff_test.go` | Variable difficulty engine |
| `extreme_vardiff_test.go` | Extreme edge cases: NaN, Inf, boundary oscillation |

### Workers (`internal/workers/`) — 2 files

| File | Focus |
|------|-------|
| `stats_test.go` | Worker stats tracking |
| `stats_security_test.go` | Worker stats security |

### Packages (`pkg/`) — 4 files

| File | Focus |
|------|-------|
| `pkg/atomicmap/shardedmap_test.go` | Sharded concurrent map |
| `pkg/protocol/protocol_test.go` | Protocol type definitions |
| `pkg/protocol/stratum_boundary_test.go` | Stratum protocol boundary cases |
| `pkg/ringbuffer/ringbuffer_test.go` | Lock-free ring buffer |

---

## Chaos Tests & Production Bug Findings

The chaos test suite consists of 10 numbered tests designed to find concurrency bugs, plus extensive pool/integration chaos tests. Each targets specific production code paths and documents invariants.

### Numbered Chaos Tests (Tests 1-10)

| # | Test | File | Target | Bug Found | Status |
|---|------|------|--------|-----------|--------|
| 1 | WAL Rotation Disk Exhaustion | `shares/chaos_wal_test.go` | `wal.go:388-426` | `rotate()` closes file before Rename; Rename failure leaves WAL broken | **FIXED** |
| 2 | Pipeline Dual Buffer Saturation | `shares/chaos_pipeline_test.go` | `pipeline.go:230-260` | Tests accounting invariant (submitted = written + dropped + buffered) | No bug (invariant holds) |
| 3 | ZMQ Recovery Double-Spawn | `daemon/chaos_zmq_test.go` | `zmq.go:601-676` | TOCTOU between `status.Load()` and `setStatus()` spawns multiple recovery loops | **FIXED** |
| 4 | Dedup Tracker Concurrent Flood | `shares/chaos_dedup_test.go` | `validator.go:978-1003` | Tests correctness under 1M concurrent ops | No bug (invariant holds) |
| 5 | Pipeline Shutdown During Backoff | `shares/chaos_pipeline_test.go` | `pipeline.go:414` | Tests context cancellation breaks backoff sleep | No bug (mechanism works) |
| 6 | Partial Buffer TOCTOU Race | `stratum/chaos_server_test.go` | `server.go:568-580` | Load→Check→Add pattern allows concurrent goroutines to bypass buffer limit | **FIXED** |
| 7 | WAL Commit Sync Failure | `shares/chaos_wal_test.go` | `wal.go:200-264` | Crash between CommitBatch and Sync loses commit marker (replay duplicates) | By design (use `CommitBatchVerified`) |
| 8 | Coordinator Lock Ordering | `pool/chaos_coordinator_test.go` | `coordinator.go:926-997` | Deadlock from inconsistent lock ordering (poolsMu vs failedCoinsMu) | Pre-existing fix verified |
| 9 | NodeManager SelectNode Race | `nodemanager/chaos_failover_test.go` | `manager.go:186` | `Health.Score` read without `node.mu.RLock()` — data race with `checkAllNodes` | **FIXED** |
| 10 | Circuit Breaker + Block Queue | `database/chaos_compound_test.go` | `circuitbreaker.go` | Tests block accounting under compound stress | No bug (invariant holds) |

### Pool Chaos Tests (12 tests in `pool/chaos_test.go`)

| Test | Target | What It Verifies |
|------|--------|------------------|
| 100 Concurrent Miners Randomized | `pool.go:1227-1229`, `heightctx.go:39-55` | Accounting: completed + cancelled + duplicates = total |
| Rapid Block Succession | `heightctx.go:39-55`, `pool.go:2057-2066` | HeightAborts occur under rapid height advances |
| ZMQ Notification Lag | `pool.go:1170-1194` | Submissions properly cancelled after delayed ZMQ notification |
| Template Fetch Delay | `pool.go:1016` | HeightContext protection during slow template fetches |
| Retry Loop Height Advance | `pool.go:2040-2079` | Abort classification: HeightAbort vs DeadlineAbort |
| Multi-Coin Isolation | `HeightEpoch` per coin | DGB advances don't cancel LTC contexts |
| Aux Submission Parent Race | `pool.go:2470-2547` | Aux submissions cancelled when parent chain advances |
| Randomized Rewards/Miners | `pool.go:2217-2245` | Reward attribution under chaos |
| Sustained Pressure (5s) | All submission paths | System stability under continuous load |
| Deadline Usage Distribution | `pool.go:2043-2046` | Deadline histogram captures realistic distribution |
| No Goroutine Leaks | HeightContext lifecycle | All goroutines exit cleanly after chaos |
| Exact Height Boundary | HeightContext timing | No stale submissions at exact height boundary |

### Integration Chaos Tests (`integration/chaos_stress_test.go` + `chaos_stress_extended_test.go`)

68 tests across 13 categories (some tests span multiple categories):

| Category | Test Count | Focus |
|----------|-----------|-------|
| Distributed / Multi-Node | 3 | Same-height reorg flapping, concurrent advance, partition heal |
| High-Load Stress | 4 | 10K concurrent shares, ring buffer MPSC, VarDiff convergence, goroutine leak |
| Memory & Resource Exhaustion | 5 | Circuit breaker lifecycle, block queue crash safety, nonce tracker bounds |
| Time Distortion & Deadlines | 5 | Clock jump backward/forward, aggressive retarget guards, mass context expiration |
| External Dependency Failures | 3 | Circuit breaker concurrent access, rate limiter connection churn |
| Multi-Coin & Merge-Mining | 4 | Job ID isolation, duplicate tracker isolation, block queue multi-coin, WAL isolation |
| Crash Consistency | 7 | WAL write/replay, commit/replay, concurrent write, empty replay, rotation |
| Goroutine Lifecycle | 5 | 10K context lifecycles, rapid connect/disconnect, worker churn, share rate limit |
| Reorg / Pipeline Stress | 6 | Reorg burst storm, concurrent consumers with reorgs, stale job cleanup |
| BlockQueue Crash Safety | 5 | Multi-cycle crash recovery, drain after circuit recovery, error tracking |
| VarDiff Edge Cases | 8 | Sustained rapid shares, boundary backoff, profiled sessions, all retarget guards |
| Boundary & Edge Conditions | 6 | SetDifficulty clamp, LRU eviction, flapping no-op, lower height ignored |
| Monitoring & Stats | 5 | WAL stats accuracy, ring buffer stats, circuit breaker stats, rate limiter stats |

### HA Chaos Tests (`ha/chaos_test.go`)

| Test | Focus |
|------|-------|
| Rapid Leader Election | 100 election rounds, leader change counting |
| Split-Brain Prevention | Dual-partition detection |
| Message Replay Prevention | Replay attack detection under concurrent senders |
| Node Failure Recovery | Failure injection + recovery verification |
| High Message Rate | Backpressure under flooding |
| Sequence Wraparound | uint64 wraparound handling |
| Context Cancellation | 100-goroutine clean shutdown |
| Map Concurrent Access | sync.Map stress with 50 workers |

---

## Production Fixes Applied

### Fix 1: WAL `rotate()` — Broken State on Rename Failure

**File**: `internal/shares/wal.go:403-409`
**Found by**: Chaos Test 1 (`TestChaos_WAL_RotationDiskExhaustion`)

**Root cause**: `rotate()` calls `w.file.Close()` before `os.Rename()`. If Rename fails (disk full, permissions), `w.file` is closed and `w.writer` wraps a stale fd. All subsequent writes silently fail or crash.

**Fix**: If Rename fails after Close, call `w.openWALFile()` to reopen `current.wal` (which still exists since Rename failed), restoring the WAL to a working state.

### Fix 2: `SelectNode` Data Race — Unprotected `Health.Score` Read

**File**: `internal/nodemanager/manager.go:186-192`
**Found by**: Chaos Test 9 (`TestChaos_NodeManager_ConcurrentFailoverSelectNode`)

**Root cause**: Line 186 reads `m.primary.Health.Score` while holding `m.mu.RLock()` but not `node.mu.RLock()`. `checkAllNodes()` writes `Health.Score` under `node.mu.Lock()`. These are different mutexes — data race detectable with `-race`.

**Fix**: Acquire `m.primary.mu.RLock()` before reading `Health.Score`, matching every other read site in the file.

### Fix 3: Server TOCTOU — Partial Buffer Limit Bypass

**File**: `internal/stratum/server.go:568-580`
**Found by**: Chaos Test 6 (`TestChaos_Server_PartialBufferTOCTOURace`)

**Root cause**: Load→Check→Add pattern on `partialBufferBytes`. Between Load and Add, concurrent goroutines can each pass the same limit check, then each Add, causing the counter to exceed the limit.

**Fix**: Atomic reserve-then-check using `Add(delta)` which returns the new total. If the new total exceeds the limit, rollback with `Add(-delta)`. No TOCTOU gap.

### Fix 4: ZMQ `checkHealth` — Recovery Loop Double-Spawn

**File**: `internal/daemon/zmq.go:649`
**Found by**: Chaos Test 3 (`TestChaos_ZMQ_RecoveryDoubleSpawnRace`)

**Root cause**: Comment says "Use CAS" but code uses `Load()` then `setStatus()` (which internally uses `Swap`, not CAS). Multiple goroutines calling `checkHealth()` can each see `status != Failed`, each enter the block, and each spawn a separate `recoveryLoop()`.

**Fix**: Replace `Load + setStatus(Swap)` with `CompareAndSwap`. Only the goroutine that wins the CAS proceeds to log, notify fallback, and spawn the recovery loop.

---

## Execution Commands

### Full Test Suite

```bash
# All tests (standard)
go test ./internal/... ./pkg/...

# All tests with race detector (recommended for CI)
go test -race ./internal/... ./pkg/...

# All tests verbose with race detector
go test -v -race -timeout 300s ./internal/... ./pkg/...
```

### By Category

```bash
# Chaos tests only (the 10 numbered tests)
go test -v -race -run "TestChaos_" -timeout 120s ./internal/...

# Pool chaos tests (12 pool-specific chaos tests)
go test -v -race -run "TestChaos" -timeout 120s ./internal/pool/...

# Integration stress tests
go test -v -race -run "TestExt_" -timeout 300s ./internal/integration/...

# HA chaos tests
go test -v -race -run "TestChaos" -timeout 60s ./internal/ha/...

# HeightContext reorg tests
go test -v -race -run "TestHeightContext" ./internal/jobs/...

# Orphan detection tests
go test -v -race -run "TestDelayedOrphan|TestStability" ./internal/payments/...

# VarDiff edge cases
go test -v -race -run "TestVarDiff" ./internal/vardiff/...

# Security / attack simulation
go test -v -race ./internal/security/...
```

### By Package

```bash
# Shares (validator, pipeline, WAL, dedup)
go test -v -race -timeout 120s ./internal/shares/...

# Stratum server
go test -v -race -timeout 60s ./internal/stratum/...

# Node manager
go test -v -race ./internal/nodemanager/...

# Database (requires Postgres for integration tests)
go test -v -race ./internal/database/...

# Jobs (HeightContext, job manager)
go test -v -race ./internal/jobs/...
```

### Fuzz Tests

```bash
# AuxPoW Merkle tree fuzzing
go test -fuzz=FuzzMerkleTree -fuzztime=60s ./internal/auxpow/...

# Share validation fuzzing
go test -fuzz=. -fuzztime=60s ./internal/shares/...

# V1 protocol fuzzing
go test -fuzz=. -fuzztime=60s ./internal/stratum/v1/...
```

### Verification After Fixes

```bash
# Verify all 4 production fixes pass with race detector
go test -v -race -run "TestChaos_WAL_RotationDiskExhaustion" ./internal/shares/...
go test -v -race -run "TestChaos_NodeManager_ConcurrentFailoverSelectNode" ./internal/nodemanager/...
go test -v -race -run "TestChaos_Server_PartialBufferTOCTOURace" ./internal/stratum/...
go test -v -race -run "TestChaos_ZMQ_RecoveryDoubleSpawnRace" ./internal/daemon/...

# Run all 10 chaos tests together
go test -v -race -run "TestChaos_" -timeout 120s ./internal/...
```

---

## Related Documentation

| Document | Content |
|----------|---------|
| [ARCHITECTURE.md](../architecture/ARCHITECTURE.md) | System architecture, Spiral Router, vardiff engine, share pipeline |
| [SECURITY_MODEL.md](../architecture/SECURITY_MODEL.md) | Security controls, FSM enforcement, rate limiting, TLS |
| [NOSEC.md](../../NOSEC.md) | Security architecture decisions, gosec suppressions |
