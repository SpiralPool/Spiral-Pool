# Security Architecture Decisions

**Last Updated:** 2026-03-16

## Purpose

This document describes intentional security architecture decisions in Spiral Pool's design and provides transparency about code-level security controls verified through static analysis.

**This document provides transparency about design choices. It is not a security audit, vulnerability disclosure, or endorsement. The mitigations described below represent the project's best-effort approach; they are not warranties or guarantees. See LICENSE and TERMS.md for the complete "AS IS" disclaimer.**

## Design Philosophy

Spiral Pool is **non-custodial infrastructure software** that:
- Manages cryptocurrency node processes
- Constructs block templates with miner wallet addresses for direct coinbase payout
- Configures network interfaces for high availability
- Orchestrates system services
- Provides optional privacy features (Tor)

The pool operator never takes custody or control of miner funds. Block rewards flow directly from the blockchain to the miner's wallet via the coinbase transaction.

These capabilities require system-level access by design. The architecture prioritizes:
1. **Transparency** - All system interactions are documented
2. **Operator control** - Configuration determines behavior
3. **Defense in depth** - Multiple security layers recommended

## Architecture Decisions

### System Process Management

Spiral Pool manages external processes as core functionality:

| Component | Capability | Operator Control |
|-----------|------------|------------------|
| Node Manager | Start/stop cryptocurrency daemons | Configured binary paths |
| HA Manager | Network interface configuration | Explicit enable required |
| Service Control | Systemd service management | User-specified services |
| Tor Integration | Optional Tor process | Disabled by default |

**Design rationale:** Mining pool software must orchestrate cryptocurrency nodes. This is equivalent to how Docker manages containers or systemd manages services.

**Operator responsibilities:**
- Verify all configured binary paths point to trusted executables
- Use least-privilege service accounts where possible
- Implement process monitoring and auditing

### SimpleSwap — No Pool Server Involvement in Financial Transactions

Spiral Pool includes an optional SimpleSwap.io swap alert feature. When enabled, `SpiralSentinel` appends a SimpleSwap.io link to `sats_surge` alerts (fired when a mined coin rises 25%+ against BTC over 7 days).

**Decision: The pool server has no involvement in any swap transaction.**

| Property | Detail |
|----------|--------|
| Config file | `/etc/spiralpool/simpleswap.conf` (chmod 600, root:root) |
| Config stores | `SIMPLESWAP_ENABLED` only |
| API key | Not used, not stored — operator uses the SimpleSwap website directly |
| BTC address | Not stored — operator enters it on the SimpleSwap website |
| Pool server role | Sends the alert link only; no API calls, no coin transfers |

**Rationale:** All swap activity happens on the SimpleSwap.io website in the operator's browser. The pool server generates an alert with a pre-filled link (source coin and BTC destination pre-selected). The operator clicks the link, enters their BTC address on the SimpleSwap website, and completes the swap there. This design means:

- No API keys or wallet addresses are stored anywhere on the pool server
- No financial transaction data ever passes through the pool software
- No risk of the pool being classified as a money transmitter or financial intermediary
- No attack surface from stored exchange credentials

**Operator responsibilities:** Operators are solely responsible for complying with all applicable AML/KYC requirements, tax reporting obligations, and SimpleSwap.io's Terms of Service. See TERMS.md (Section 5D) and WARNINGS.md for full details.

---

### Randomness Usage

Spiral Pool uses different random sources for different purposes:

| Use Case | Source | Rationale |
|----------|--------|-----------|
| TLS, authentication | `crypto/rand` | Cryptographically secure |
| Node IDs, session data | `crypto/rand` | Cryptographically secure |
| Job nonce generation | `math/rand` | Performance; miners see all jobs |
| Load balancing | `math/rand` | Statistical distribution only |
| Timing jitter / retry backoff | `math/rand` | Operational, not security |

**Design rationale:** Cryptographic random is used for all security-sensitive operations. Non-cryptographic random is used only where security properties are not required and performance matters.

**Verified locations:**
- `internal/database/manager.go:52` - `crypto/rand` for node IDs
- `internal/stratum/v2/server.go:810` - `crypto/rand` for session random data
- `internal/pool/coinpool.go` - `math/rand` for retry jitter (acceptable)
- `internal/pool/pool.go` - `math/rand` for timing jitter (acceptable)

### Hash Function Selection

| Use Case | Algorithm | Rationale |
|----------|-----------|-----------|
| Mining (SHA256d coins) | SHA-256 | Protocol requirement |
| Mining (Scrypt coins) | Scrypt | Protocol requirement |
| TLS certificates | SHA-256+ | Modern cryptographic standards |
| Password hashing | bcrypt (primary), SHA-256+salt (fallback) | Dashboard authentication |

**Design rationale:** bcrypt is used as the primary password hashing algorithm with key stretching and configurable work factor. SHA-256 with a random salt is used as a fallback only when the bcrypt library is not available. No MD5 or SHA-1 is used anywhere in the codebase.

### Network Configuration

| Setting | Default | Rationale |
|---------|---------|-----------|
| Listen address | Configurable | Operator determines exposure |
| IP protocol | IPv4 only | IPv6 disabled at OS level by installer |
| TLS | Optional | Stratum V1 standard is plaintext |
| Certificate validation | Enabled in production | Disabled only for development |

**Design rationale:** Network configuration is operator-controlled. IPv6 is disabled at the OS level because it causes kernel routing cache corruption during keepalived VIP failover operations. All stratum, API, daemon, and database connections use IPv4. Development conveniences (skip verification) are configuration options, not defaults.

### TLS Certificate Verification

Database connections use PostgreSQL's native `sslmode` parameter via the `pgx` driver, which correctly implements all standard modes:

| Mode | Protection |
|------|------------|
| `require` | Encrypts connection, no certificate verification |
| `verify-ca` | Encrypts + verifies CA chain |
| `verify-full` | Encrypts + verifies CA chain + hostname |

**Production path:** `internal/config/config.go` (`ConnectionString()`) builds the connection URL with `sslmode=<mode>`. The `pgx` driver handles TLS negotiation. Default is `require` when no mode is specified. CA certificate path is appended via `sslrootcert=` when `SSLRootCert` is configured.

**Note:** `internal/database/replication.go` contains a `BuildTLSConfig()` helper that constructs a Go `tls.Config` struct. This code is not called from production — it exists for potential future use with direct TLS connections. Its `verify-ca` implementation is incomplete (behaves like `verify-full` due to missing `VerifyPeerCertificate` callback). Production connections are unaffected because they use the `pgx` driver's native sslmode handling.

**Design rationale:** The `require` mode is appropriate for internal database networks where man-in-the-middle is not the threat model. For untrusted networks, operators should configure `verify-ca` or `verify-full` mode with `sslRootCert` in their config.

## Code-Level Security Verification

### Gosec Suppressions (#nosec)

All 123 `#nosec` annotations across 29 files have been reviewed and are justified. The table below covers all suppressions grouped by category.

**Security-sensitive suppressions (G204, G304, G402, G407):**

| File | Suppression | Justification |
|------|-------------|---------------|
| `internal/stratum/server.go:220` | `#nosec G402` | MinVersion explicitly set to TLS 1.2 minimum |
| `internal/stratum/v2/noise.go:125` | `#nosec G407` | Nonce derived from counter per Noise Protocol spec |
| `cmd/spiralctl/cmd/node.go` (x2) | `#nosec G204` | Commands from internal validated service lists |
| `cmd/spiralctl/cmd/coin.go` (x2) | `#nosec G204` | CLI command from coinRegistry validation |
| `cmd/spiralctl/cmd/mining.go` (x3) | `#nosec G204` | Command/service names from coinRegistry |
| `cmd/spiralctl/cmd/gdpr.go` (x2) | `#nosec G204` | Identifier from CLI flag, key from redis-cli output |
| `internal/config/v2.go:267` | `#nosec G304` | `os.ReadFile` for admin-controlled config path |
| `internal/config/config.go:649` | `#nosec G304` | `os.ReadFile` for CLI-specified config path |
| `internal/stratum/server.go:258` | `#nosec G304` | `os.ReadFile` for CA certificate file |
| `cmd/spiralctl/cmd/root.go:254` | `#nosec G304` | `os.ReadFile` for known config file locations |
| `cmd/spiralctl/cmd/status.go:233` | `#nosec G304` | `os.ReadFile` for known daemon config locations |
| `cmd/spiralctl/cmd/config.go` (x2) | `#nosec G304` | `os.ReadFile` for known config/Docker paths |
| `cmd/spiralctl/cmd/gdpr.go:403` | `#nosec G304` | `os.ReadFile` for path from known directory |

**Routine G104 suppressions (error not checked) — 103 total across 29 files:**

These are standard Go patterns for ignoring errors on cleanup operations (`conn.Close()`, `SetDeadline()`, `Write()` during shutdown, `fs.Parse()`). The highest-density files are:

| File | Count | Pattern |
|------|-------|---------|
| `internal/ha/vip.go` | x25 | Network conn cleanup, discovery broadcasts, interface teardown |
| `internal/stratum/server.go` | x17 | Session write deadlines, conn cleanup, error responses |
| `internal/stratum/v2/server.go` | x14 | Session lifecycle, conn cleanup, broadcast best-effort |
| `internal/metrics/prometheus.go` | x5 | Health/readiness endpoint write responses |
| `internal/daemon/zmq.go` | x5 | ZMQ subscriber cleanup on reconnect |
| `cmd/spiralctl/cmd/coin.go` | x4 | Flag parse, `fmt.Sscanf` for display stats |
| `cmd/spiralctl/cmd/tor.go` | x4 | Flag parse, systemctl enable/restart |
| `internal/api/server.go` | x4 | HTTP Write errors — best-effort response delivery |
| `internal/stratum/v2/session.go` | x3 | Write deadlines, session close during shutdown |
| `internal/database/replication.go` | x3 | conn.Close() cleanup, crypto/rand.Read |
| All other files | x19 | Various cleanup/shutdown operations |

### SQL Injection Prevention

**Status: Mitigated** - All SQL queries use parameterized queries (`$1`, `$2`, etc.).

Table names are dynamically constructed from pool IDs (e.g., `shares_{poolID}`, `blocks_{poolID}`) but pool IDs are validated against a strict pattern at startup, preventing SQL injection in DDL statements.

**Credential validation:** `internal/database/replication.go` uses `validIdentifierRe = regexp.MustCompile('^[a-zA-Z_][a-zA-Z0-9_]{0,62}$')` for database user names.

### Command Injection Prevention

**Status: Mitigated** - All `exec.Command` calls use:
1. Static command names (no shell interpolation)
2. Arguments from validated sources (coinRegistry, config, systemctl output)
3. No `shell=true` equivalent

All command arguments are passed as separate strings to `exec.Command()`, never concatenated into a shell command string.

### Credential Handling

**Status: Addressed** — No hardcoded credentials. - All sensitive data externalized to environment variables:

| Variable | Purpose |
|----------|---------|
| `SPIRAL_REPLICATION_PASSWORD` | Database replication |
| `SPIRAL_DATABASE_PASSWORD` | Database access |
| `SPIRAL_DAEMON_PASSWORD` | Daemon RPC |
| `SPIRAL_ADMIN_API_KEY` | Admin API authentication |
| `SPIRAL_METRICS_TOKEN` | Prometheus metrics auth |
| `SPIRAL_TELEGRAM_BOT_TOKEN` | Telegram notifications |
| `SPIRAL_<COIN>_DAEMON_PASSWORD` | Per-coin daemon RPC |

**Password masking:** `internal/config/config.go:813-833` masks passwords as `"***"` in debug/log output.

**Secure file permissions:** `internal/database/replication.go` sets private keys to `0600`.

### URL/Connection String Injection Prevention

**Status: Mitigated** - Database connection strings use `url.QueryEscape()` for user and password fields:
- `internal/database/replication.go` (replication connections)
- `internal/database/migrate.go` (migration connections)

### Input Validation

| Control | Location | Method |
|---------|----------|--------|
| Worker name validation | Stratum server | Alphanumeric regex |
| Pool ID validation | Config loader | Prevents SQL injection in table names |
| Address format validation | Startup + authorization | Per-coin regex |
| Log sanitization | All client-facing handlers | Prevents log injection |
| Webhook URL validation | Sentinel | URL scheme whitelist |
| Dashboard XSS prevention | `dashboard.html` | `escapeHtml()` function |
| JSON hardening | Stratum handler | Max nesting (32), array (100), keys (50) |

## File Locations

Security-relevant code is concentrated in these areas:

```
src/stratum/cmd/spiralctl/cmd/     - CLI commands (process management)
src/stratum/internal/ha/           - High availability (network interfaces)
src/stratum/internal/nodemanager/  - Node process management
src/stratum/internal/stratum/      - Protocol implementation
src/stratum/internal/daemon/       - Daemon communication
src/stratum/internal/api/          - REST API with auth
src/stratum/internal/database/     - Database with advisory locks
src/stratum/internal/payments/     - Payment processor with fencing
```

## Cloud Deployments — Architectural Restrictions

When `CLOUD_DETECTED` is set during installation (100+ providers auto-detected plus a manual failsafe), the following restrictions are applied automatically. These are not operator-configurable on cloud — they are enforced by the installer.

| Restriction | Behavior | Rationale |
|-------------|----------|-----------|
| Tor | Disabled automatically | Most provider AUPs prohibit Tor; Tor does not protect against provider hypervisor access — the primary cloud threat |
| High Availability | Forced to Standalone | Cloud provider networks block VRRP multicast/broadcast required for keepalived VIP failover; etcd split-brain risk without physical node isolation |
| ZMQ bindings | `127.0.0.1` only | ZMQ is a local IPC channel between daemon and stratum — never needs external reachability; `0.0.0.0` binding would expose it to the provider's tenant network |
| Prometheus metrics | Loopback-only (UFW) | Cloud "local subnet" is a shared tenant network that may include other customers' VMs; `SPIRAL_METRICS_TOKEN` enforced |
| Dashboard port 1618 | UFW closed; SSH tunnel required | Dashboard is HTTP-only; exposing it on the public internet is insecure regardless of rate limiting |
| IPv6 | Disabled at kernel level | Causes kernel routing cache corruption during keepalived VIP failover operations; all services use IPv4 |

**Verified code locations:**

- Tor cloud block: `install.sh:10434` — `if [[ -n "${CLOUD_DETECTED:-}" ]]; then TOR_ENABLED="false" ...`
- HA standalone enforcement: `install.sh` (`select_ha_mode`) — options 2/3 auto-revert to standalone when `CLOUD_DETECTED` is set
- ZMQ bindings: all `zmqpubhashblock`, `zmqpubrawtx`, `zmqpubrawblock` entries use `tcp://127.0.0.1:PORT` in all coin daemon configs
- Metrics UFW: `install.sh:14564` — subnet `ufw allow` skipped; loopback-only rules applied on cloud
- Dashboard UFW: `install.sh:14545` — `if [[ -z "$CLOUD_DETECTED" ]]; then sudo ufw allow $DASHBOARD_PORT/tcp; fi`
- IPv6: `install.sh` (`configure_network`) — `net.ipv6.conf.all.disable_ipv6 = 1` written to `/etc/sysctl.conf`

---

## Operator Security Guidance

### Recommended Deployment

1. **Operator-controlled infrastructure preferred** - Bare metal or self-hosted VMs. Cloud/VPS is supported with explicit risk acknowledgment during install (provider ToS violations, bandwidth billing, provider access to credentials). See WARNINGS.md and CLOUD_OPERATIONS.md.
2. **x86_64 architecture only** - ARM/Raspberry Pi has not been tested
3. **Run as dedicated user** - Not root except for VIP management
4. **Audit configured paths** - Verify all binary paths before deployment
5. **Network isolation** - Database and internal services on private networks
6. **Firewall rules** - Expose only necessary ports
7. **Process monitoring** - Use auditd or equivalent
8. **Log aggregation** - Centralize logs for security review
9. **Regular updates** - Keep system and dependencies current
10. **Enable rate limiting** - Set `rateLimiting.enabled: true` for private pools
11. **Set metrics token** - Configure `SPIRAL_METRICS_TOKEN` with 32+ character value
12. **Use TLS for replication** - Configure `verify-ca` or `verify-full` for database TLS on untrusted networks

### Security Boundaries

| Boundary | Spiral Pool Responsibility | Operator Responsibility |
|----------|---------------------------|-------------------------|
| Process execution | Execute configured commands | Verify command safety |
| Network exposure | Bind to configured addresses | Firewall configuration |
| Data storage | Write to configured paths | File permissions, encryption |
| Authentication | Implement configured auth | Strong credentials |

## Transparency Statement

This document exists to provide operators with full visibility into Spiral Pool's system interactions. All capabilities documented here are:
- Intentional design decisions
- Required for core functionality
- Operator-configurable
- Standard for infrastructure software

Operators should review this document and conduct their own security assessment appropriate to their deployment environment.

---

*Spiral Pool v1.1.1 - Security Architecture Decisions*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
