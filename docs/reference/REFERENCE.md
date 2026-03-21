# Spiral Pool Reference

Strict lookup tables. No explanations. For context, see [ARCHITECTURE.md](../architecture/ARCHITECTURE.md).

---

> **IPv4 only.** All ports listed below listen on IPv4. IPv6 is not supported.

## Stratum Ports

| Coin | Symbol | V1 | V2 | TLS |
|------|--------|----|----|-----|
| DigiByte (SHA-256d) | DGB | 3333 | 3334 | 3335 |
| DigiByte (Scrypt) | DGB-SCRYPT | 3336 | 3337 | 3338 |
| Bitcoin | BTC | 4333 | 4334 | 4335 |
| Bitcoin Cash | BCH | 5333 | 5334 | 5335 |
| Bitcoin II | BC2 | 6333 | 6334 | 6335 |
| Litecoin | LTC | 7333 | 7334 | 7335 |
| Dogecoin | DOGE | 8335 | 8337 | 8342 |
| PepeCoin | PEP | 10335 | 10336 | 10337 |
| Catcoin | CAT | 12335 | 12336 | 12337 |
| Namecoin | NMC | 14335 | 14336 | 14337 |
| Syscoin | SYS | 15335 | 15336 | 15337 |
| Myriad | XMY | 17335 | 17336 | 17337 |
| Fractal Bitcoin | FBTC | 18335 | 18336 | 18337 |
| Q-BitX | QBX | 20335 | 20336 | 20337 |

## Service Ports

| Port | Service |
|------|---------|
| 4000 | REST API |
| 1618 | Dashboard (Spiral Dash) |
| 9100 | Prometheus metrics |
| 5354 | VIP Status |
| 5363 | VIP Discovery (UDP) |
| 5432 | PostgreSQL (HA replication) |
| 2379 | etcd client (Patroni → etcd) |
| 2380 | etcd peer (Raft consensus) |
| 8008 | Patroni REST API |

---

## CLI Commands (spiralctl)

| Command | Description |
|---------|-------------|
| `spiralctl status` | Overall pool status |
| `spiralctl logs` | View stratum log output |
| `spiralctl sync` | Blockchain sync status |
| `spiralctl restart` | Restart all pool services |
| `spiralctl shutdown [--reboot]` | Gracefully stop all services and power off (or reboot) |
| `spiralctl mining status` | Current mining configuration |
| `spiralctl mining solo <coin>` | Single coin mode |
| `spiralctl mining multi <a,b,c>` | Multi-coin mode (same algorithm) |
| `spiralctl mining merge enable\|disable` | Enable or disable merge mining |
| `spiralctl node status` | Show daemon status |
| `spiralctl node start\|stop\|restart <coin>` | Node control |
| `spiralctl coin list\|status` | Show coins and blockchain sync status |
| `spiralctl coin disable <coin>` | Disable a coin's daemon (requires root) |
| `spiralctl wallet` | Wallet management |
| `spiralctl stats blocks [N]` | Block history (alias: `spiralctl blocks`) |
| `spiralctl pool stats` | Pool statistics |
| `spiralctl watch` | Live pool monitoring |
| `spiralctl scan` | Miner network scan |
| `spiralctl test` | Connection test |
| `spiralctl data backup` | Backup pool data (alias: `spiralctl backup`) |
| `spiralctl data restore` | Restore pool from backup (alias: `spiralctl restore`) |
| `spiralctl data export` | Export pool data (alias: `spiralctl export`) |
| `spiralctl update` | Check for updates |
| `spiralctl pause` | Pause Sentinel alerts temporarily |
| `spiralctl maintenance` | Maintenance mode |
| `spiralctl chain export\|restore` | Chain management (push/pull blockchain data) |
| `spiralctl sync-addresses` | Sync miner addresses across HA nodes |
| `spiralctl vip enable\|disable\|status` | VIP control |
| `spiralctl vip failover` | Display VIP failover instructions |
| `spiralctl ha enable\|disable\|status` | HA control |
| `spiralctl ha promote\|failback` | Promote to primary / rejoin cluster |
| `spiralctl ha credentials\|setup\|validate\|service` | HA cluster utilities |
| `spiralctl miners` | List connected miners with hashrate and shares |
| `spiralctl miners kick <IP>` | Disconnect all stratum sessions from a miner IP |
| `spiralctl workers` | Per-worker breakdown (miner → rig → hashrate + acceptance rate) |
| `spiralctl miner nick <IP> <name>` | Set a display name for a miner in Sentinel |
| `spiralctl miner nick list\|clear` | List or clear miner nicknames |
| `spiralctl coin enable <TICKER>` | Add a supported coin (installs daemon, generates wallet) |
| `spiralctl coin disable <TICKER>` | Stop and disable a coin daemon |
| `spiralctl coin-upgrade` | In-place coin daemon binary upgrade |
| `spiralctl add-coin <TICKER>` | Add a custom/unsupported coin from GitHub (advanced) |
| `spiralctl remove-coin <TICKER>` | Remove a custom coin's generated files (wallet preserved) |
| `spiralctl config show\|list\|get\|set` | Sentinel configuration |
| `spiralctl config validate` | Dry-run config check (YAML/JSON syntax, placeholder detection) |
| `spiralctl config notify-test` | Send a test notification to every configured channel |
| `spiralctl config list-cooldowns` | Show active alert cooldowns with time remaining |
| `spiralctl log errors [service] [window]` | Filter service logs for errors/warnings |
| `spiralctl security [period]` | Security status overview (default: 24h) |
| `spiralctl security fail2ban [action]` | Manage fail2ban jails (alias: `spiralctl fail2ban`) |
| `spiralctl security tor [action]` | Tor configuration (alias: `spiralctl tor`) |
| `spiralctl webhook status\|set\|clear\|test` | Webhook management |
| `spiralctl stats` | Quick pool stats (hashrate, blocks) |
| `spiralctl version` | Show full version table (stratum binary + all coin daemons) |
| `spiralctl help` | Show help and available commands |
| `spiralctl external setup\|enable\|disable\|status\|test` | External access configuration |
| `spiralctl gdpr-delete` | GDPR data deletion |

---

## API Endpoints

Base: `http://localhost:4000`

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/pools` | List all configured pools |
| GET | `/api/pools/{id}/stats` | Pool statistics (hashrate, miners, blocks) |
| GET | `/api/pools/{id}/blocks` | Block history (paginated) |
| GET | `/api/pools/{id}/miners` | Miner list (paginated) |
| GET | `/api/pools/{id}/miners/{addr}` | Miner statistics and workers |
| GET | `/api/pools/{id}` | Pool info |
| GET | `/api/pools/{id}/hashrate/history` | Pool hashrate history |
| GET | `/api/pools/{id}/miners/{addr}/workers` | Miner's worker list |
| GET | `/api/pools/{id}/miners/{addr}/workers/{worker}` | Worker statistics |
| GET | `/api/pools/{id}/miners/{addr}/workers/{worker}/history` | Worker hashrate history |
| GET | `/api/pools/{id}/workers` | Worker list (admin, requires API key) |
| GET | `/api/pools/{id}/workers-by-class` | Workers grouped by miner class |
| GET | `/api/pools/{id}/connections` | Active stratum connections (admin, requires API key) |
| GET | `/api/pools/{id}/router/profiles` | Spiral Router difficulty profiles |
| GET | `/api/pools/{id}/pipeline/stats` | Share pipeline statistics |
| GET | `/api/pools/{id}/payments/stats` | Payment statistics |
| GET | `/api/coins` | Supported coins list |
| GET | `/api/sentinel/alerts` | Sentinel alert history |
| GET | `/api/admin/stats` | Admin statistics (admin, requires API key) |
| GET | `/api/admin/device-hints` | Device hint cache (admin, requires API key) |
| POST | `/api/admin/device-hints` | Push device classification hints (requires X-API-Key) |
| POST | `/api/admin/kick?ip=X.X.X.X` | Disconnect all stratum sessions from an IP (requires X-API-Key) |
| GET | `/api/ha/status` | HA cluster status (admin, requires API key) |
| GET | `/api/ha/database` | Database HA status (admin, requires API key) |
| GET | `/api/ha/failover` | Failover status (admin, requires API key) |
| GET | `/api/ha/alerts` | HA alert history (admin, requires API key) |
| GET | `/health` | Health check (returns 200 if healthy) |

---

## Systemd Services

| Service | Description |
|---------|-------------|
| `spiralstratum` | Stratum pool server |
| `spiraldash` | Dashboard web UI |
| `spiralsentinel` | Monitoring and alerting |
| `spiralpool-health` | Health monitor |
| `spiralpool-sync` | Multi-coin blockchain sync monitor |
| `spiralpool-ha-watcher` | HA role watcher (HA nodes only) |

Daemon service names follow the pattern of the coin's CLI name.

---

## Miner Classes (SHA-256d)

Source: `src/stratum/internal/stratum/spiralrouter.go:178-352`

| Class | Devices | Hashrate | InitialDiff | MinDiff | MaxDiff | Target |
|-------|---------|----------|-------------|---------|---------|--------|
| Unknown | (unrecognized devices) | Varies | 500 | 500 | 50,000 | 1s |
| Lottery | ESP32, Arduino | 50-500 KH/s | 0.001 | 0.0001 | 100 | 60s |
| Low | BitAxe Ultra/Supra, NMAxe, Lucky LV06 | 400-600 GH/s | 580 | 580 | 150,000 | 5s |
| Mid | NerdQAxe++, BitAxe Hex/Gamma, FutureBit Apollo | 1-10 TH/s | 1,165 | 1,165 | 50,000 | 1s |
| High | Antminer S9/S15, older gen | 10-20 TH/s | 3,260 | 3,260 | 100,000 | 1s |
| Pro | Antminer S19/S21, Whatsminer M50-M66 | 100 TH/s-2.1 PH/s | 25,600 | 25,600 | 500,000 | 1s |
| Avalon Nano | Nano 2/3/3S | 3-6 TH/s | 1,538 | 1,538 | 2,500 | 1s |
| Avalon Legacy Low | Avalon 3/3S/6 series | 0.8-3.5 TH/s | 815 | 815 | 1,500 | 1s |
| Avalon Legacy Mid | Avalon 7/8 series | 6-15 TH/s | 2,560 | 2,560 | 5,000 | 1s |
| Avalon Mid | Avalon 9/10/11 series | 18-81 TH/s | 11,650 | 11,650 | 25,000 | 1s |
| Avalon High | Avalon A12/A13 series | 85-130 TH/s | 25,000 | 25,000 | 40,000 | 1s |
| Avalon Pro | Avalon A14/A15/A16 series | 150-300 TH/s | 45,000 | 45,000 | 80,000 | 1s |
| Avalon Home | Mini 3, Avalon Q | 37-90 TH/s | 14,900 | 14,900 | 30,000 | 1s |
| Farm Proxy | Braiins Farm Proxy, stratum aggregators | ~500 GH/s–429 PH/s per connection | 500,000 | 25,600 | 100,000,000 | 1s |
| Hash Marketplace | NiceHash, MiningRigRentals | ~100 TH/s–214 PH/s per connection | 25,600 | 25,600 | 50,000,000 | 1s |

## Miner Classes (Scrypt)

Source: `src/stratum/internal/stratum/spiralrouter.go:373-441`

| Class | Devices | Hashrate | InitialDiff | MinDiff | MaxDiff | Target |
|-------|---------|----------|-------------|---------|---------|--------|
| Unknown | (unrecognized devices) | Varies | 8,000 | 128 | 2,048,000 | 10s |
| Lottery | CPU/GPU | ~100 H/s-17 KH/s | 0.1 | 0.001 | 16 | 60s |
| Low | Goldshell Mini DOGE | ~183-838 MH/s | 28,000 | 8,000 | 128,000 | 10s |
| Mid | Antminer L3+, Goldshell LT Lite, FluMiner L2 | ~498 MH/s-3.3 GH/s | 38,000 | 16,000 | 256,000 | 5s |
| High | LT5 Pro, DG Home | ~2.9-8.4 GH/s | 180,000 | 64,000 | 512,000 | 4s |
| Pro | Antminer L7/L9, LT6, DG1/DG1+ | ~9.5-67 GH/s | 290,000 | 128,000 | 2,048,000 | 2s |
| Farm Proxy | Scrypt stratum aggregators | ~500 GH/s–6.7 TH/s per connection | 2,048,000 | 128,000 | 200,000,000 | 2s |
| Hash Marketplace | Scrypt rental platforms | ~100 GH/s–3.4 TH/s per connection | 128,000 | 128,000 | 100,000,000 | 2s |

---

## VarDiff Parameters

Source: `src/stratum/internal/vardiff/vardiff.go`

| Parameter | Value |
|-----------|-------|
| Increase limit | 4x per retarget |
| Decrease limit | 0.75x per retarget |
| Variance floor | 50% minimum (hardcoded, config cannot go lower) |
| RetargetTime | 60s (configurable) |
| Clock jump guard | Skip retarget if elapsed < 0 or > 600s |
| Aggressive trigger (fast) | ratio < 0.8 |
| Aggressive trigger (slow) | ratio > 2.0 |
| Meaningful change threshold | > 5% difference |

**Difficulty formula:** `Difficulty = Hashrate (H/s) x TargetShareTime / 2^32`

---

## Detection APIs

| API Type | Port | Miners |
|----------|------|--------|
| AxeOS (HTTP) | 80 | BitAxe, NerdQAxe, NMAxe, Lucky Miner, Jingle, Zyber, Hammer |
| Goldshell (HTTP) | 80 | Goldshell Mini DOGE, LT5, LT6 |
| CGMiner | 4028 | Avalon, Antminer, Whatsminer, Innosilicon, FutureBit, Elphapex |
| Pool API | N/A | ESP32 miners (NerdMiner, BitMaker) — no device API, monitored via pool connections endpoint |

---

## Merge Mining Pairs

| Parent | Auxiliary | Chain ID |
|--------|-----------|----------|
| BTC | NMC | 1 |
| BTC | FBTC | 8228 |
| BTC | SYS | 16 |
| BTC | XMY | 90 |
| LTC | DOGE | 98 |
| LTC | PEP | 63 |

---

## Configuration Skeleton

```yaml
pool:
  id: digibyte
  coin: digibyte
  address: DAddress...
  coinbaseText: "/SpiralPool/"

stratum:
  listen: "0.0.0.0:3333"
  listenV2: "0.0.0.0:3334"
  tls:
    enabled: true
    listenTLS: "0.0.0.0:3335"
    certFile: /path/to/cert.pem
    keyFile: /path/to/key.pem
    minVersion: "1.2"          # Minimum TLS version ("1.2" or "1.3")
    clientAuth: false           # Require client certificate
    caFile: ""                  # CA certificate for client auth
  difficulty:
    initial: 1.0
    varDiff:
      enabled: true
      minDiff: 0.001
      maxDiff: 1000000
      targetTime: 5
      retargetTime: 60
      variancePercent: 50
      blockTime: 15
  rateLimiting:
    enabled: false
    connectionsPerIP: 100
    sharesPerSecond: 50
    workersPerIP: 100
    preAuthTimeout: 10s         # Timeout before authorization required
    preAuthMessageLimit: 20     # Max messages before authorization
    banPersistencePath: "/spiralpool/data/bans.json"  # Persist bans across restarts
  versionRolling:
    enabled: true
    mask: 0x1fffe000

daemon:
  host: localhost
  port: 14022                           # DGB RPC port (see Stratum Ports table for per-coin values)
  user: user
  password: pass
  zmq:
    enabled: true
    endpoint: "tcp://127.0.0.1:28532"  # DGB ZMQ port

database:
  host: localhost
  port: 5432
  user: spiralstratum
  password: secret
  database: spiralstratum
  maxConnections: 50
  ha:                            # Database HA failover (optional)
    enabled: false
    nodes: []                    # Additional DB nodes [{host, port, priority, readOnly}]
    healthCheckInterval: 15s
    failoverThreshold: 3

api:
  listen: "0.0.0.0:4000"

metrics:
  listen: "0.0.0.0:9100"

logging:
  level: info
  format: json

ha:
  enabled: false

vip:
  enabled: false
  address: ""              # Virtual IP address (e.g., "192.168.1.200")
  interface: ""            # Network interface (e.g., "ens33", auto-detected if omitted)
  netmask: 32              # CIDR netmask for VIP (32 = host-only route, avoids subnet conflicts)
  priority: 100            # Node priority (100 = primary, 101+ = backup)
  canBecomeMaster: true     # Allow this node to become master
  clusterToken: ""         # Shared cluster authentication token
  discoveryPort: 5363      # UDP port for cluster discovery
  statusPort: 5354         # HTTP port for VIP status API
  heartbeatInterval: 30s   # Interval between heartbeats
  failoverTimeout: 90s     # Time before declaring a node dead
  autoPriority: false      # Auto-assign priority based on join order

coins:                         # V2 multi-coin configuration (array)
  - name: bitcoin              # Human-readable name
    symbol: BTC                # Coin ticker
    algorithm: sha256d         # "sha256d" or "scrypt"
    enabled: true
    pool_id: btc_sha256_1      # Unique pool identifier (no hyphens)
    address: "bc1q..."         # Payout address
    daemon:                    # Per-coin daemon connection
      host: localhost
      port: 8332
      user: user
      password: pass
    stratumPort: 4333          # Stratum V1 port
    stratumV2Port: 4334        # Stratum V2 port
    stratumTLSPort: 4335       # Stratum TLS port
    blockTime: 600             # Target block time (seconds)
    confirmations: 100         # Required confirmations

mergeMining:                   # AuxPoW merge mining
  enabled: false
  refreshInterval: 5s          # Aux block template refresh interval
  merkleNonce: 0               # Aux merkle tree slot calculation
  auxChains:
    - symbol: NMC              # Auxiliary coin ticker
      enabled: true
      address: "N..."          # Aux coin payout address
      daemon:
        host: localhost
        port: 8334
        user: user
        password: pass

payments:
  enabled: true
  interval: 600s               # Payout processing interval (default: 10m)
  minimumPayment: 1.0          # Minimum payout amount
  scheme: SOLO                 # "SOLO" only (solo mining pool)
  blockMaturity: 0             # Confirmations before eligible (0 = coin default)
  deepReorgMaxAge: 0           # Max age to re-verify for reorgs (0 = auto-computed)

security:                      # Security settings (parsed, not all enforced at runtime)
  allowedIPs: []               # IP whitelist
  blockedIPs: []               # IP blacklist
  ddosProtection:
    enabled: false
    maxConnectionsPerIP: 100
    maxConnectionRate: 1000
    slowlorisTimeout: 30s

sentinel:                      # Spiral Sentinel monitoring (Go-side)
  enabled: false
  check_interval: 60s
  hashrate_drop_percent: 30    # Alert if hashrate drops > 30%
  disconnect_drop_percent: 30  # Alert if connections drop > 30%
  wal_stuck_threshold: 10m     # Alert if WAL not flushed
  false_rejection_threshold: 0.10
  orphan_rate_threshold: 0.20
  chain_tip_stall_minutes: 30
  min_peer_count: 3
  maturity_stall_hours: 6
  alert_cooldown: 15m
  goroutine_limit: 10000
  ha_flap_window: 10m
  ha_flap_threshold: 3

celebration:                   # Block found celebration messages
  enabled: false
  duration_hours: 4            # Duration of periodic "keep mining!" reminders

failover:                      # Pool-level failover
  enabled: false
  healthCheckInterval: 10s
  failoverThreshold: 3         # Consecutive failures before failover
  recoveryThreshold: 5         # Consecutive successes before recovery
  discovery:
    enabled: false
    autoDetect: false           # Auto-detect local subnet
    scanTimeout: 2s
    scanInterval: 5m
    maxConcurrent: 50
  backupPools: []              # [{id, host, port, priority, weight}]

backup:                        # Backup settings (parsed, not enforced at runtime)
  enabled: false
  retentionDays: 30
  backupPath: "/spiralpool/backups"
  compression: gzip            # "gzip", "zstd", "none"
```

---

## Dashboard Connection Quality Indicators

Source: `src/dashboard/templates/dashboard.html:getConnectionQuality()`

Each miner card displays a connection quality rating based on the share accept/reject/stale rate.

Formula: `qualityScore = acceptRate - (staleRate x 2)`

| Indicator | Rating | Threshold |
|-----------|--------|-----------|
| `●●●●` | Excellent | qualityScore ≥ 99% |
| `●●●○` | Good | qualityScore ≥ 97% |
| `●●○○` | Fair | qualityScore ≥ 95% |
| `●○○○` | Poor | qualityScore ≥ 90% |
| `○○○○` | Bad | qualityScore < 90% |

Stale shares are penalized at 2x weight. Miners with no share data yet display empty circles until shares accumulate.

---

## Security Constants

See [SECURITY_MODEL.md](../architecture/SECURITY_MODEL.md) for full details with source file references.

---

*Spiral Pool — Phi Forge 1.1.1*
