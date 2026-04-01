# Multi Coin Smart Port

Single stratum port that mines multiple SHA-256d coins. Miners connect once — the pool handles everything.

---

## How It Works

The multi-coin port runs a standard stratum server on a dedicated port (default: **16180**). When a miner connects, the pool assigns it to whichever coin's time slot is active. At regular intervals (`check_interval`), the pool checks the current time (in the configured timezone) against the 24-hour schedule and may rotate miners to a different coin by sending a new `mining.notify` with `clean_jobs=true`. The miner doesn't know or care which coin it's hashing — it just works on whatever job template the pool sends.

### The Rotation Cycle

```
1. Miner connects → assigned to whichever coin's time slot is active now
2. Pool sends mining.notify with that coin's block template
3. Miner hashes, submits shares → pool routes shares to correct coin pool
4. Every check_interval, pool checks current time against the schedule:
   - If the time slot boundary has been crossed, switch to the next coin
5. If switched: pool sends new mining.notify (clean_jobs=true)
   - 10-second grace window accepts shares for the old coin's job
6. Miner continues hashing the new template — seamless transition
```

### Block Creation

When a miner submits a share that meets the network target, it becomes a block. The block is submitted to whichever coin daemon that share was routed to. No special handling needed — the miner was already hashing that coin's block template, so the proof-of-work is valid for that coin's chain.

### Share Routing

Each miner session is tracked with its current coin assignment. When shares arrive:

1. Look up session's assigned coin
2. Resolve the correct payout wallet for this coin (see [Wallet Resolution](#wallet-resolution))
3. If the session was recently switched (within 10s grace window), try the old coin's pool first
4. Route the share to the correct coin pool's `HandleMultiPortShare()`
5. The coin pool validates, records, and checks for blocks as usual

---

## Scheduling

Coin weights map directly to wall-clock time on a **24-hour cycle** in your configured timezone (set during install, stored in `multi_port.timezone`). Weights become contiguous time slots — deterministic, not random.

**Example:** With `DGB: 80, BCH: 15, BTC: 5` and timezone `America/New_York`:

| Coin | Weight | Daily Hours | Time Window |
|------|--------|-------------|-------------|
| DGB  | 80%    | 19.2 hours  | 00:00 – 19:12 |
| BCH  | 15%    | 3.6 hours   | 19:12 – 22:48 |
| BTC  | 5%     | 1.2 hours   | 22:48 – 24:00 |

**All miners switch at the same wall-clock boundaries.** At 19:12 local time, the entire pool moves from DGB to BCH. At 22:48, everyone moves to BTC. At midnight, back to DGB. No randomness, no variance — you get exactly the time allocation you configured.

With a 50/50 split between DGB and BTC:
- DGB: 00:00 – 12:00 (12 hours)
- BTC: 12:00 – 24:00 (12 hours)

Difficulty comparison is meaningless across coins of vastly different magnitudes (e.g., DGB ~1,000 vs BCH ~500,000 vs BTC ~80 trillion). What matters is how much time you want to allocate to each coin.

**Use case:** "Mine DGB 19 hours a day for steady shares, but throw 1.2 hours at BTC as a lottery shot."

---

## Configuration

```yaml
multi_port:
  enabled: true
  port: 16180                # Dedicated stratum port for multi-coin mining
  coins:
    QBX:
      weight: 96             # 96% of mining time on Q-BitX (~23 hours)
      start_hour: 0          # Start at midnight (optional — omit to auto-sequence)
    BC2:
      weight: 4              # 4% on Bitcoin II (~1 hour)
      start_hour: 23         # Start at 11 PM (optional)
  check_interval: 5m         # Re-evaluate every 5 minutes
  prefer_coin: QBX           # Default coin on connect / tie-breaker
  min_time_on_coin: 60s      # Minimum time before allowing a switch
  timezone: America/New_York # IANA timezone for 24h schedule (auto-set from install)
```

### Field Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable/disable the multi-coin port |
| `port` | int | `16180` | Stratum port for multi-coin mining |
| `coins` | map | required | Coin symbol to routing config. Each coin needs `weight` (0-100). All weights must sum to exactly 100 |
| `coins.*.weight` | int | required | Percentage of daily mining time (0-100) |
| `coins.*.start_hour` | float | auto | Optional custom start hour (0-23.99) in the configured timezone. If omitted, coins are sequenced from midnight in alphabetical order |
| `check_interval` | duration | `30s` | How often to re-evaluate coin assignments |
| `prefer_coin` | string | first coin | Default coin for new connections and tie-breaking |
| `min_time_on_coin` | duration | `60s` | Minimum time a miner stays on a coin before switching. Prevents rapid flip-flopping. Bypassed if the current coin's daemon goes down |
| `timezone` | string | from sentinel config | IANA timezone for the 24h schedule (e.g., `America/New_York`). Auto-populated from `display_timezone` during setup. Falls back to UTC |
| `wallet_map` | map | none | Optional per-worker per-coin wallet overrides. See [Wallet Resolution](#wallet-resolution) below |

### Choosing `check_interval`

This controls how often the pool checks whether it's time to switch coins. It does **not** control how long miners stay on each coin — that's determined by the weights and the 24-hour cycle.

- **30s** (default): detects slot boundary crossings within 30 seconds.
- **5s-10s**: more responsive — miners switch within seconds of a boundary. Slightly more CPU.
- **1m-5m**: less responsive. At a slot boundary, miners may continue on the old coin for up to `check_interval` before switching. Fine for large time slots.

### Choosing `min_time_on_coin`

This prevents a miner from being switched back and forth too quickly. Set it to at least the coin's average share time for your smallest miners, so they can find at least one share before being moved.

- **60s** (default): good for most setups
- **0**: defaults to 60s
- **5m+**: very conservative, miners stay put longer
- Bypassed automatically if the current coin is unavailable (daemon down)

### Setting Weights

Weights must sum to exactly **100**. Each weight represents the percentage of daily mining time allocated to that coin.

```yaml
coins:
  DGB: { weight: 80 }    # 80% = 19.2 hours/day
  BCH: { weight: 15 }    # 15% = 3.6 hours/day
  BTC: { weight: 5 }     # 5% = 1.2 hours/day
  # Total: 100
```

At least 2 coins must have a positive weight. Set `weight: 0` to exclude a coin from the schedule.

### Custom Start Times

By default, coins are sequenced from midnight in alphabetical order. You can override this with `start_hour` to control exactly when each coin's time slot begins.

```yaml
coins:
  QBX:
    weight: 96           # 23 hours
    start_hour: 0        # Midnight to 11 PM
  BC2:
    weight: 4            # 1 hour
    start_hour: 23       # 11 PM to midnight
```

`start_hour` is a float in the configured timezone: `0` = midnight, `12.5` = 12:30 PM, `23` = 11 PM. Coins without `start_hour` are placed after those with one. The dashboard settings page provides a visual schedule preview with time inputs.

### Wallet Resolution

When the pool switches coins, different chains may use different address formats. The pool handles this automatically:

1. **Automatic (default):** Each share is credited to the pool's configured payout address for the active coin (the `address` field in each coin's pool config). This is the normal mode — miners connect with any worker name, and the pool operator's wallet per coin receives the payouts. **No additional configuration needed.**

2. **Manual override (`wallet_map`):** For public pools where individual miners need per-coin wallets, you can map worker names to specific addresses:

```yaml
multi_port:
  # ... coins, weights, etc.
  wallet_map:
    Heat2Sats:
      QBX: "Mqbx1abc..."
      BC2: "1bc2xyz..."
    worker2:
      QBX: "Mqbx1def..."
      BC2: "1bc2uvw..."
```

Resolution priority: explicit `wallet_map` entry > pool's coin payout address > miner's connect address.

Worker name matching is case-insensitive and extracts the worker from `wallet.worker` format (e.g., a miner connecting as `abc123.Heat2Sats` matches the `Heat2Sats` entry).

---

## Port Assignment

The multi-coin port must not conflict with any per-coin stratum port. The default port **16180** is reserved for this feature and does not conflict with any existing coin port.

### All Stratum Ports (for reference)

| Coin | V1 | V2 | TLS |
|------|----|----|-----|
| DigiByte (SHA-256d) | 3333 | 3334 | 3335 |
| DigiByte (Scrypt) | 3336 | 3337 | 3338 |
| Bitcoin | 4333 | 4334 | 4335 |
| Bitcoin Cash | 5333 | 5334 | 5335 |
| Bitcoin II | 6333 | 6334 | 6335 |
| Litecoin | 7333 | 7334 | 7335 |
| Dogecoin | 8335 | 8337 | 8342 |
| PepeCoin | 10335 | 10336 | 10337 |
| Catcoin | 12335 | 12336 | 12337 |
| Namecoin | 14335 | 14336 | 14337 |
| Syscoin | 15335 | 15336 | 15337 |
| Myriadcoin | 17335 | 17336 | 17337 |
| Fractal Bitcoin | 18335 | 18336 | 18337 |
| Q-BitX | 20335 | 20336 | 20337 |
| **Multi-Coin Port** | **16180** | — | — |

### Service Ports (no conflicts)

| Port | Service |
|------|---------|
| 1618 | Dashboard |
| 4000 | REST API |
| 9100 | Prometheus |
| 5354 | VIP Status |
| 5363 | VIP Discovery |

---

## Miner Setup

Miners connect to the multi-coin port exactly like a normal stratum port. No special configuration needed.

```
stratum+tcp://pool.example.com:16180
```

The miner's username/worker format is the same as any per-coin port. The pool handles all coin routing server-side.

### What Miners See

- Normal stratum connection — `mining.subscribe`, `mining.authorize`, `mining.notify`
- Periodic `mining.notify` with `clean_jobs=true` when the pool rotates them to a different coin
- Their existing firmware and software works unchanged

### Compatibility

All SHA-256d miners are supported:
- BitAxe (Ultra, Hex, Supra)
- NerdMiner, NerdQAxe
- Antminer S9, S19, S19+, S21
- Avalon Nano, A1246, A1366, A1466
- Any stratum V1 compatible SHA-256d miner

The multi-coin port only works for SHA-256d coins. Scrypt coins (LTC, DOGE, DGB-Scrypt) cannot participate because the miner hardware is algorithm-specific.

---

## API Endpoints

### `GET /api/multiport`

Returns current multi-port configuration, computed schedule, and live stats.

```json
{
  "enabled": true,
  "port": 16180,
  "coins": { "QBX": { "weight": 96, "start_hour": 0 }, "BC2": { "weight": 4, "start_hour": 23 } },
  "prefer_coin": "QBX",
  "timezone": "America/New_York",
  "schedule": [
    { "symbol": "QBX", "weight": 96, "start_h": 0, "end_h": 23 },
    { "symbol": "BC2", "weight": 4, "start_h": 23, "end_h": 24 }
  ],
  "wallet_map": {},
  "live": { "active_coin": "QBX", "next_switch": "BC2", "time_remaining": "2h 15m" },
  "available_coins": [{ "symbol": "QBX", "name": "Q-BitX", "enabled": true }]
}
```

### `POST /api/multiport`

Update multi-port configuration (admin only). Restarts stratum on success.

### `GET /api/multiport/wallets`

Returns the current `wallet_map` and active coin list.

### `POST /api/multiport/wallets`

Update worker wallet mappings (admin only). Restarts stratum on success.

```json
{ "wallet_map": { "Heat2Sats": { "QBX": "Mqbx1...", "BC2": "1bc2..." } } }
```

### `GET /api/multiport/switches?limit=50`

Returns recent coin switch events.

```json
[
  {
    "session_id": 42,
    "worker_name": "bitaxe-01",
    "miner_class": "low",
    "from_coin": "DGB",
    "to_coin": "BCH",
    "reason": "weighted_rotation",
    "timestamp": "2026-03-29T14:23:01Z"
  }
]
```

### `GET /api/multiport/difficulty`

Returns network difficulty state for all multi-port coins.

```json
{
  "DGB": {
    "symbol": "DGB",
    "network_diff": 1234.5,
    "block_time": 15.0,
    "available": true,
    "last_updated": "2026-03-29T14:22:45Z"
  },
  "BTC": {
    "symbol": "BTC",
    "network_diff": 81725892002603,
    "block_time": 600,
    "available": true,
    "last_updated": "2026-03-29T14:22:45Z"
  }
}
```

---

## Sentinel Alerts

The Sentinel monitors the multi-coin port and fires alerts for:

| Alert | Severity | Trigger |
|-------|----------|---------|
| `multi_port_difficulty_spike` | Warning | >15% difficulty change on any multi-port coin |
| `multi_port_coin_switch` | Info | 5+ coin switches in a single check interval |

---

## Failover Behavior

If a coin daemon goes down:

1. Monitor detects the coin is unavailable (next poll)
2. All sessions on that coin are immediately re-evaluated — `min_time_on_coin` is bypassed
3. Sessions are reassigned to the next available coin from the schedule
4. When the daemon recovers, miners gradually rotate back on subsequent evaluations

If **all** coin daemons go down:

1. Sessions retain their last coin assignment
2. Shares will be rejected with "coin pool not available"
3. No crash — the multi-port server stays up and reconnects when daemons recover
