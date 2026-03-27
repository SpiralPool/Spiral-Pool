# Spiral Pool Security Model

Security controls implemented in Spiral Pool as documented below. Values shown are hardcoded constants unless noted as configurable. This document describes the intended design; it is not a guarantee of security.

> **IMPORTANT**: This software has NOT been audited by third-party security professionals. The controls described below represent implementation details, not guarantees. Operators are solely responsible for their own security assessment. See [SECURITY.md](../../SECURITY.md) for vulnerability reporting and incident response.

---

## Connection Security

> **IPv4 only.** Spiral Pool does not support IPv6. The installer disables IPv6 at the OS level via sysctl. All connections (stratum, API, daemon RPC, database) use IPv4.

| Control | Value | Source |
|---------|-------|--------|
| TLS minimum version | TLS 1.2 | `internal/stratum/server.go:244` |
| TLS listener | Separate port per coin (V1+2 offset) | Per-coin config |
| V1 message size limit | 16,384 bytes (16 KB) | `internal/stratum/server.go:578` |
| V2 message size limit | 1,048,576 bytes (1 MB) | `internal/stratum/v2/types.go:28` |
| Ban persistence | Saved to `/spiralpool/data/bans.json` | `internal/config/config.go:1412` |
| Keepalive monitoring | Idle connection detection | Configurable timeout |

## Protocol Security (FSM)

The connection state machine uses two atomic boolean flags (`subscribed`, `authorized`) to enforce ordering on security-critical operations.

```
(unsubscribed, unauthorized) -> (subscribed, unauthorized) -> (subscribed, authorized)
```

| Flag State | Transitions On | Guard |
|------------|---------------|-------|
| unsubscribed | `mining.subscribe` | None (always accepted) |
| subscribed, unauthorized | `mining.authorize` | Requires `subscribed` |
| subscribed, authorized | `mining.submit` | Requires both `subscribed` and `authorized` |

A miner cannot submit shares before subscribing and authorizing. Out-of-order `mining.authorize` and `mining.submit` are rejected. Protocol negotiation methods (`mining.configure`, `mining.suggest_difficulty`, `mining.extranonce.subscribe`, `mining.ping`) are accepted in any state.

Source: `pkg/protocol/protocol.go:109-178` (connection FSM: `authorized`/`subscribed` atomic flags and enforcement methods). Job lifecycle FSM at `pkg/protocol/protocol.go:494-505` (Created, Issued, Active, Invalidated, Solved) is a separate concern.

## Pre-Authentication Limits

| Control | Value | Source |
|---------|-------|--------|
| Max messages before auth | 20 | `internal/config/config.go:1408` |
| Auth timeout | 10 seconds | `internal/config/config.go:1402` (default), `internal/stratum/server.go:490` (30s fallback, overridden by config) |

These prevent subscribe-spam attacks and connection slot exhaustion. Connections that exceed either limit are dropped.

## JSON Hardening

Pre-parse validation applied to all incoming JSON before the standard parser processes it:

| Control | Value | Source |
|---------|-------|--------|
| Max nesting depth | 32 levels | `internal/stratum/v1/handler.go:36` |
| Max array elements | 100 (const), 101 accepted | `internal/stratum/v1/handler.go:37` (comma count > 100 rejects; 100 commas = 101 elements max accepted, 102 is first rejected) |
| Max object keys | 50 (const), 51 accepted | `internal/stratum/v1/handler.go:38` (comma count > 50 rejects; 50 commas = 51 keys max accepted, 52 is first rejected) |

These prevent deeply nested JSON attacks, oversized arrays, and large objects from reaching the JSON parser.

## Rate Limiting

Rate limiting is **disabled by default** for compatibility with hashrate marketplaces and rented hashpower services. Marketplace proxies multiplex thousands of workers through shared IPs, so per-IP limits would break legitimate connections.

| Parameter | Default | Recommended (private pool) | Source |
|-----------|---------|---------------------------|--------|
| `rateLimiting.enabled` | `false` | `true` | Config |
| `connectionsPerIP` | 0 (disabled) | 100 | `internal/config/config.go:92` |
| `sharesPerSecond` | 0 (disabled) | 50 | `internal/config/config.go:94` (struct), `:1391` (default comment) |
| `workersPerIP` | 0 (disabled) | 100 | `internal/config/config.go:100` (struct), `:1392` (default comment) |

Example configuration for private pools:

```yaml
rateLimiting:
  enabled: true
  connectionsPerIP: 100
  sharesPerSecond: 50
  workersPerIP: 100
```

## Share Validation

| Control | Description |
|---------|-------------|
| Extranonce binding | Shares include session's unique ExtraNonce1. Cross-session replay impossible. |
| Duplicate detection | Per-job nonce tracking. Replay attacks rejected. |
| Difficulty enforcement | Server controls difficulty. `mining.suggest_difficulty` acknowledged but not obeyed. |
| Grace period | Previous difficulty accepted for shares submitted during difficulty transition. |

## Panic Recovery

Per-connection panic recovery ensures one malformed message cannot crash the server. Each goroutine handling a miner connection has independent panic recovery.

## Log Sanitization

All client-supplied data is sanitized before logging to prevent log injection attacks.

## Worker Name Validation

Worker names are validated against the pattern `^[a-zA-Z0-9._\-:+=@ ]+$`. This allows alphanumeric characters plus dots, dashes, underscores, colons, plus, equals, at-signs, and spaces for hashrate rental compatibility. Control characters, quotes, backslashes, semicolons, and angle brackets are rejected.

## Address Format Validation

Pool payout addresses are validated via regex at startup. Miner addresses are validated at authorization time.

## Pool ID Validation

Pool identifiers are validated to prevent SQL injection in dynamically-created table names (`shares_{poolID}`, `blocks_{poolID}`).

## Payment Fencing (HA)

Three-layer protection against double-payment in HA configurations:

| Layer | Mechanism |
|-------|-----------|
| 1. Primary-only processing | Only the primary node runs payment cycles |
| 2. PostgreSQL advisory locks | `pg_try_advisory_lock(pool_hash)` acquired (non-blocking) before any payment operation. Per-pool lock IDs use FNV-1a hash of `"SPPAY:" + poolID`. |
| 3. Status guard SQL | `UPDATE blocks_{poolID} SET status=$1, confirmationprogress=$2 WHERE blockheight=$3 AND hash=$4 AND (status='submitting' OR status='pending' OR (status='confirmed' AND $1 IN ('orphaned','paid')))` — compound key (height+hash) with status guard prevents stale processes from re-paying |

Source: `internal/payments/processor.go`, `internal/database/postgres.go`

## Metrics Security

Prometheus metrics endpoint (`/metrics` on port 9100) is protected by `SPIRAL_METRICS_TOKEN` when configured. Without a token, the endpoint is accessible without authentication.

---

*Spiral Pool — Convergent Spiral 1.2.3*
