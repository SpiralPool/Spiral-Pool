# Spiral Sentinel Reference

<p align="center">
  <img src="../../assets/Spiral Sentinel.jpg" alt="Spiral Sentinel" width="400">
</p>

<p align="center">
  <em>Autonomous mining fleet monitoring, alerting, and self-healing.</em>
</p>

Spiral Sentinel is a Python-based monitoring system that watches your mining fleet, blockchain nodes, pool infrastructure, and market conditions. It sends real-time alerts via Discord, Telegram, and XMPP/Jabber with cyberpunk or professional theming.

**Source:** `src/sentinel/SpiralSentinel.py` (~17,600 lines)
**Service:** `spiralsentinel`
**State directory:** `~spiraluser/.spiralsentinel/`

---

## Table of Contents

1. [Quick Reference](#quick-reference)
2. [Configuration](#configuration)
3. [Alert Types](#alert-types)
4. [Notification Channels](#notification-channels)
5. [Monitoring Capabilities](#monitoring-capabilities)
6. [Poll Frequencies](#poll-frequencies)
7. [Periodic Reports](#periodic-reports)
8. [Miner Management](#miner-management)
9. [Achievement System](#achievement-system)
10. [Market Data (CoinGecko)](#market-data-coingecko)
11. [HA Behavior](#ha-behavior)
12. [State Files](#state-files)
13. [API Endpoints Called](#api-endpoints-called)
14. [Command-Line Arguments](#command-line-arguments)
15. [Environment Variables](#environment-variables)
16. [Alert Cooldowns](#alert-cooldowns)

---

## Quick Reference

```bash
# Service management
systemctl status spiralsentinel
systemctl restart spiralsentinel

# One-shot status check
python3 /spiralpool/bin/SpiralSentinel.py --status

# Test Discord/Telegram webhook
python3 /spiralpool/bin/SpiralSentinel.py --test

# Hot-reload miner database (no restart needed)
python3 /spiralpool/bin/SpiralSentinel.py --reload

# Fleet-wide reboot (interactive confirmation)
python3 /spiralpool/bin/SpiralSentinel.py --reset

# spiralctl integration
spiralctl webhook test
spiralctl pause 30          # Suppress alerts for 30 minutes
spiralctl maintenance on    # Maintenance mode
spiralctl config show       # Show Sentinel configuration
```

---

## Configuration

Config file: `~spiraluser/.spiralsentinel/config.json`
Fallback (if ProtectHome=yes): `$INSTALL_DIR/config/sentinel/config.json`

Permissions are set to `0600` on every load. Environment variables override config file values.

### General

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `alert_theme` | string | `"cyberpunk"` | `"cyberpunk"` or `"professional"` |
| `alerts_enabled` | bool | `true` | Master toggle for all alerts |
| `health_monitoring_enabled` | bool | `true` | Master toggle for health monitoring |
| `check_interval` | int | `120` | Seconds between monitoring cycles (overridden per-coin) |
| `display_timezone` | string | `"America/New_York"` | IANA timezone for user-facing times |
| `hostname_override` | string | `""` | Override hostname in alert footers |
| `pool_api_url` | string | `"http://localhost:4000"` | Spiral Stratum API endpoint |
| `pool_admin_api_key` | string | `""` | Admin API key for device hints |
| `pool_id` | string | `"dgb_sha256_1"` | Legacy single-coin pool ID |
| `wallet_address` | string | `""` | Legacy single-coin wallet address |
| `push_device_hints` | bool | `true` | Push device info to pool for difficulty hints |

### Temperature & Thresholds

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `temp_warning` | int | `75` | Warning threshold (Celsius) |
| `temp_critical` | int | `85` | Critical threshold (Celsius) |
| `health_warn_threshold` | int | `70` | Health score threshold (0-100) |
| `miner_offline_threshold_min` | int | `10` | Minutes before declaring miner offline |
| `pool_no_shares_threshold_min` | int | `30` | Minutes with no pool shares before zombie alert |

### Auto-Restart

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `auto_restart_enabled` | bool | `true` | Enable automatic miner restart |
| `auto_restart_min_offline` | int | `20` | Minutes offline before restart trigger |
| `auto_restart_cooldown` | int | `1800` | Seconds between restart attempts (30 min) |

### Fleet & Network

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `expected_fleet_ths` | float | `22.0` | Expected total fleet hashrate (TH/s) |
| `expected_fleet_ths_disabled` | bool | `false` | True if user skipped fleet hashrate setting |
| `net_drop_threshold_phs` | float | `48` | Network hashrate drop alert threshold (PH/s) |
| `net_reset_threshold_phs` | float | `52` | Network hashrate recovery threshold (PH/s) |
| `blip_detection_enabled` | bool | `true` | Enable power blip detection |

### Report Schedule

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `report_frequency` | string | `"6h"` | `"6h"`, `"daily"`, or `"off"` |
| `report_hours` | list | `[6, 12, 18]` | Hours for periodic reports |
| `final_report_time` | string | `"21:55"` | Last report before quiet hours |
| `major_report_hour` | int | `6` | Hour for detailed morning report |
| `weekly_report_day` | int | `0` | Day of week for weekly report (0=Monday) |
| `monthly_report_day` | int | `1` | Day of month for monthly report |
| `enable_6h_reports` | bool | `true` | Toggle periodic reports |
| `enable_weekly_reports` | bool | `true` | Toggle weekly reports |
| `enable_monthly_reports` | bool | `true` | Toggle monthly reports |
| `enable_quarterly_reports` | bool | `true` | Toggle quarterly reports |

### Quiet Hours

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `quiet_hours_start` | int | `22` | Hour quiet hours begin (10 PM) |
| `quiet_hours_end` | int | `6` | Hour quiet hours end (6 AM) |

### Currency & Financial

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `report_currency` | string | `"CAD"` | Fiat currency for reports (USD, CAD, EUR, GBP, JPY, AUD, CHF, CNY, NZD, SEK) |
| `power_currency` | string | `"CAD"` | Currency for power cost calculations |
| `power_rate_kwh` | float | `0.12` | Electricity rate per kWh |
| `sats_change_alert_pct` | int | `15` | Alert when sat value changes by N% |
| `wallet_drop_alert_enabled` | bool | `true` | Alert when wallet balance drops |
| `odds_alert_threshold` | int | `40` | Daily odds percentage to trigger alert |

### Sats Surge

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `sats_surge_enabled` | bool | `true` | Enable sats surge alerts |
| `sats_surge_threshold_pct` | int | `25` | Alert threshold (% increase) |
| `sats_surge_lookback_days` | int | `7` | Compare against N days ago |
| `sats_surge_cooldown_hours` | int | `24` | Per-coin cooldown |

### Prometheus Metrics

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `metrics_enabled` | bool | `true` | Enable Prometheus metrics fetching |
| `metrics_url` | string | `"http://localhost:9100/metrics"` | Metrics endpoint |
| `metrics_token` | string | `""` | Bearer token for metrics auth |
| `metrics_fetch_interval` | int | `60` | Fetch interval (seconds) |
| `infra_circuit_breaker_alert` | bool | `true` | Alert on circuit breaker state changes |
| `infra_backpressure_alert` | bool | `true` | Alert on high backpressure |
| `infra_wal_errors_alert` | bool | `true` | Alert on WAL write/commit errors |
| `infra_share_loss_alert` | bool | `true` | Alert on share batch drops |

### Alert Batching

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `alert_batching_enabled` | bool | `true` | Combine multiple miner alerts into digest |
| `alert_batch_window_seconds` | int | `300` | Collection window (5 minutes) |

### Startup Suppression

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `startup_alert_suppression_min` | int | `30` | Minutes to suppress non-critical alerts at startup |
| `startup_suppression_bypass` | list | See below | Alert types that always bypass suppression |

Bypass list: `block_found`, `startup_summary`, `temp_critical`, `6h_report`, `weekly_report`, `monthly_earnings`, `quarterly_report`

### Multi-Coin

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `multi_coin_enabled` | bool | `false` | Explicit multi-coin mode |
| `coins` | list | 13 coin defs | Per-coin configuration (symbol, pool_id, wallet_address, ports) |

### Historical Data

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `history_sample_interval` | int | `900` | 15 minutes between samples |
| `history_max_age_days` | int | `730` | 2 years retention |
| `history_disk_budget_mb` | int | `50` | Recommended disk budget |

---

## Alert Types

### Miner Health

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `miner_offline` | Miner unreachable for 10+ min | Bypasses | None |
| `miner_online` | Miner recovered | Bypasses | None |
| `miner_reboot` | Uptime counter reset detected | Respects | 600s |
| `temp_warning` | Chip temp >= 75C | Respects | 3600s |
| `temp_critical` | Chip temp >= 85C | Bypasses | None |
| `thermal_shutdown` | Sustained critical temp, ASIC freq set to 0 | Bypasses | None |
| `zombie_miner` | Online but no valid shares for 30 min | Respects | 3600s |
| `degradation` | Hashrate drops significantly below baseline | Respects | 3600s |
| `auto_restart` | Auto-restart triggered | Respects | 1800s |
| `excessive_restarts` | Frequent reboots (restart loop) | Bypasses | 3600s |
| `chronic_issue` | Recurring problems on same miner | Bypasses | 3600s |
| `power_event` | Fleet-wide power blip (multiple offline simultaneously) | Respects | 600s |
| `fan_failure` | Fan at 0 RPM while running | Respects | 1800s |
| `hashboard_dead` | Hashboard failure (33% capacity loss) | Bypasses | 3600s |
| `hw_error_rate` | Rising hardware error rate | Respects | 3600s |

### Performance

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `hashrate_divergence` | Pool vs miner hashrate mismatch | Respects | 3600s |
| `share_rejection_spike` | Abnormal rejection rate | Respects | 3600s |
| `worker_count_drop` | Multiple workers disconnected | Respects | 1800s |
| `share_loss_rate` | Shares lost between miner and pool | Respects | 1800s |

### Network & Fleet

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `hashrate_crash` | Network hashrate drops 25%+ for 30 min | Bypasses | 3600s |
| `pool_hashrate_drop` | Fleet hashrate drops 50%+ for 15 min | Bypasses | 1800s |
| `high_odds` | Mining odds exceed threshold (40%) | Respects | 4h internal |

### Block Events

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `block_found` | Solo block found | **Always sends** | None |
| `block_orphaned` | Previously confirmed block orphaned | Bypasses | None |
| `best_share` | New all-time highest difficulty share | Respects | None |

> **`block_found` is special:** It bypasses quiet hours, startup suppression, maintenance mode, HA suppression, and alert batching. All nodes celebrate.

### Infrastructure (Prometheus)

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `circuit_breaker` | Pool circuit breaker open | Bypasses | None |
| `backpressure` | Buffer backpressure level >= 2 | Bypasses | 300s |
| `wal_errors` | WAL write/commit errors increasing | Bypasses | None |
| `zmq_disconnected` | ZMQ socket connection lost | Bypasses | 1800s |
| `zmq_stale` | ZMQ message age too high | Respects | 1800s |
| `orphan_rate_spike` | Orphan rate increasing | Bypasses | 3600s |

### Go Stratum API Sentinel Alerts (bridged via `/api/sentinel/alerts`)

`pool_wal_stuck_entry`, `pool_block_drought`, `pool_share_db_critical`, `pool_share_db_degraded`, `pool_share_batch_dropped`, `pool_all_nodes_down`, `pool_chain_tip_stall`, `pool_daemon_no_peers`, `pool_daemon_low_peers`, `pool_wal_recovery_stuck`, `pool_miner_disconnect_spike`, `pool_hashrate_drop`, `pool_node_health_low`, `pool_wal_disk_space_low`, `pool_wal_file_count_high`, `pool_false_rejection_rate`, `pool_retry_storm`, `pool_payment_processor_stalled`, `pool_db_failover`, `pool_ha_flapping`, `pool_block_maturity_stall`, `pool_goroutine_limit`, `pool_goroutine_growth`

### HA Cluster

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `ha_vip_change` | VIP address reassigned | Bypasses | None |
| `ha_state_change` | Cluster state changed | Bypasses | None |
| `ha_promoted` | Node promoted to MASTER | Bypasses | None |
| `ha_demoted` | Node demoted to BACKUP | Bypasses | None |
| `ha_replication_lag` | DB replication falling behind | Bypasses | 3600s |
| `ha_replica_drop` | Replica count decreased | Bypasses | 3600s |

### Financial

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `sats_surge` | Sat value up 25%+ over 7-day baseline | Respects | 24h per-coin |
| `price_crash` | Sudden price drop | Bypasses | 14400s (4h) |
| `payout_received` | Wallet balance increased | Respects | None |
| `missing_payout` | Wallet balance unchanged for N days | Bypasses | 86400s |
| `wallet_drop` | Wallet balance decreased unexpectedly | Bypasses | 3600s |

### Security

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `stratum_url_mismatch` | Miner pointing at unexpected pool URL | Bypasses | None |

### Coin Node

| Alert Type | Trigger | Quiet Hours | Cooldown |
|------------|---------|-------------|----------|
| `coin_node_down` | Coin's blockchain node unreachable | Bypasses | 3600s |
| `coin_sync_behind` | Coin node syncing, blocks behind | Respects | 3600s |
| `coin_config_change` | Mode switch, coin add/remove | Respects | None |

---

## Notification Channels

### Discord

| Key | Value |
|-----|-------|
| Config | `discord_webhook_url` |
| Env var | `DISCORD_WEBHOOK_URL` |
| Format | Rich embeds (title, description, color, fields, footer, timestamp) |
| Rate limiting | Handles Discord 429 with `Retry-After` header |
| Retry | 3 attempts with exponential backoff (2s base) |

### Telegram

| Key | Value |
|-----|-------|
| Config | `telegram_bot_token`, `telegram_chat_id` |
| Env vars | `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` |
| Format | MarkdownV2 (converted from Discord embed format) |
| Rate limiting | Minimum 1.0s between messages |
| Auto-enable | Enabled when both token and chat_id are set |

### XMPP/Jabber

| Key | Value |
|-----|-------|
| Config | `xmpp_jid`, `xmpp_password`, `xmpp_recipient` |
| Env vars | `XMPP_JID`, `XMPP_PASSWORD`, `XMPP_RECIPIENT` |
| Format | Plain text |
| MUC support | `xmpp_muc: true` for group chat rooms |
| Requires | Optional `slixmpp` package (GPL-3.0) |
| Timeout | 15 seconds |

### Fallback

If all channels fail: retries once after 10s, then writes to `fallback_notifications.log` (5MB rotation).

---

## Monitoring Capabilities

| Domain | What's Monitored |
|--------|-----------------|
| **Miner health** | Hashrate, temperature, fan speed, uptime, chip count, ASIC model, health score (0-100) |
| **Device APIs** | AxeOS HTTP, CGMiner TCP:4028, Goldshell HTTP, BraiinsOS REST, Vnish REST, LuxOS API |
| **ESP32 miners** | Via pool API `/api/pools/{id}/miners` (no direct device API) |
| **Pool stats** | Fleet hashrate, connected miners, shares/sec, blocks found |
| **Network stats** | Per-coin difficulty, network hashrate, block times |
| **Infrastructure** | Circuit breaker, backpressure, WAL errors, ZMQ health (via Prometheus) |
| **Blockchain nodes** | RPC connectivity, sync progress, connected peers |
| **Wallet balance** | On-chain balance tracking (DGB, BTC, BCH) |
| **Market data** | Coin prices, sat values, price trends (via CoinGecko) |
| **HA cluster** | VIP state, role changes, replication lag, replica count |

---

## Poll Frequencies

| Target | Interval | Notes |
|--------|----------|-------|
| DGB / DGB-SCRYPT | 30s | Fast block time (15s) |
| FBTC | 20s | Fast block time (30s) |
| DOGE / SYS / XMY | 45s | |
| LTC / PEP / CAT | 60s | |
| BTC / BCH / BC2 / NMC | 120s | Slow block time (10 min) |
| Blockchain sync check | 60s | |
| HA role check | 30s | Cached |
| HA/VIP state check | 60s | |
| Coin health check | 300s | 5 min |
| Coin change detection | 900s | 15 min |
| Prometheus metrics | 60s | Configurable |
| History sample | 900s | 15 min |
| Wallet/payout check | 3600s | 1 hour |
| Update check | 21600s | 6 hours |
| Device hints push | 3600s | 1 hour |

---

## Periodic Reports

| Report | Schedule | Config Toggle |
|--------|----------|---------------|
| **6-hour report** | Default: 6am, 12pm, 6pm, 9:55pm | `enable_6h_reports` |
| **Weekly report** | Monday at `major_report_hour` | `enable_weekly_reports` |
| **Monthly earnings** | 1st of month at `major_report_hour` | `enable_monthly_reports` |
| **Quarterly report** | End of quarter (Mar/Jun/Sep/Dec) | `enable_quarterly_reports` |
| **Maintenance reminder** | 1st of month at 8am | Always on |
| **Special date** | Solstices and equinoxes | Always on |
| **Startup summary** | On Sentinel start | Always on |

---

## Miner Management

### Auto-Restart

- Sends restart via AxeOS HTTP (`POST /api/system/restart`) or CGMiner API (`restart` command)
- 30-minute startup grace period (no auto-restarts during initial startup)
- Zombie detection: online but no shares for 30 min triggers restart
- Cooldown: 30 minutes between restart attempts per miner

### Device Discovery Integration

- Miner database: `/spiralpool/data/miners.json` (shared with Spiral Dash)
- Hot reload: watches for `/spiralpool/data/.reload_miners` trigger file
- CLI trigger: `python3 SpiralSentinel.py --reload` or `spiralctl scan`
- Dashboard sync: reads from `dashboard_config.json` as fallback

### Supported Miner Types (26 device types)

Miner types are classified by **API protocol**, not algorithm. The same protocol supports both SHA-256d and Scrypt hardware.

**AxeOS / ESP-Miner HTTP API** (port 80, `/api/system/info`):
`axeos`, `nmaxe` (BitAxe), `nerdqaxe` (NerdQAxe/NerdAxe/NerdOctaxe), `qaxe`, `qaxeplus`, `luckyminer`, `jingleminer`, `zyber`, `hammer` (Scrypt), `esp32miner`

**CGMiner TCP API** (port 4028, JSON socket):
`avalon`, `antminer` (SHA-256d S19/S21/T21), `antminer_scrypt` (Scrypt L3+/L7/L9), `whatsminer`, `innosilicon`, `futurebit`, `canaan`, `ebang`, `gekkoscience`, `ipollo`, `elphapex` (Scrypt DG1/DG Home)

**Goldshell HTTP REST** (port 80, `/mcb/cgminer`):
`goldshell` (Scrypt — Mini DOGE, LT5, KD6)

**ePIC HTTP REST** (port 4028, HTTP not TCP):
`epic` (ePIC BlockMiner)

**Custom firmware** (manual config, underlying API varies):
`braiins` (BraiinsOS/BOS+), `vnish`, `luxos`

---

## Achievement System

205 achievements across 10 categories, tracked in `state.json` lifetime stats.

| Category | Count | Examples |
|----------|-------|---------|
| Block Milestones | 20 | `first_blood` (1 block) through `satoshi_heir` (21,000 blocks) |
| Coin Earnings (7 coins) | 75 | Per-coin tiers from first sats to legendary amounts |
| Uptime | 15 | `always_on` (24h) through `eternal_flame` (1 year 100%) |
| Hashrate | 15 | `getting_started` (1 TH/s) through `hash_god` (10 PH/s) |
| Fleet Management | 15 | `solo_warrior` (1 device) through `mega_farm` (100 devices) |
| Temperature Mastery | 10 | `cool_operator` (all <50C) through `overclocker` (stable at 80C+) |
| Timing & Luck | 15 | `midnight_miner`, `lightning_luck` (2 blocks in 1h) |
| Resilience & Recovery | 15 | `comeback_kid`, `maintenance_master` (100 auto-restarts) |
| Network Timing | 10 | `golden_hour` (block during dip) |
| Special & Secret | 15 | `palindrome_block`, `fibonacci_finder`, `answer_to_everything` |

New achievements are announced via Discord embed when unlocked.

---

## Market Data (CoinGecko)

### Price Fetching

- API: `https://api.coingecko.com/api/v3/simple/price`
- Returns prices in all 10 supported fiat currencies simultaneously
- Includes satoshi conversion (coin price in BTC sats)

### Supported CoinGecko IDs

| Symbol | CoinGecko ID |
|--------|-------------|
| DGB | `digibyte` |
| BTC | `bitcoin` |
| BCH | `bitcoin-cash` |
| LTC | `litecoin` |
| DOGE | `dogecoin` |
| NMC | `namecoin` |
| SYS | `syscoin` |
| XMY | `myriadcoin` |
| FBTC | `fractal-bitcoin` |
| PEP | `pepecoin` |
| CAT | `catcoin` |

### Wallet Balance Tracking

| Coin | API |
|------|-----|
| DGB | `chainz.cryptoid.info` |
| BTC | `blockchain.info` |
| BCH | `api.blockchair.com` |

Checked every 1 hour. Detects payouts received, wallet drops, and missing payouts.

---

## HA Behavior

### Alert Suppression on Non-Master Nodes

`is_master_sentinel()` returns `true` for STANDALONE or MASTER, `false` for BACKUP or OBSERVER.

When the node is not master, **all alerts are suppressed** except:
- `block_found` &mdash; all nodes celebrate
- `startup_summary` &mdash; per-node status
- `ha_demoted` &mdash; node's own demotion notification

### HA State Tracking

- Queries HA status every 30 seconds (cached)
- When HA API is unavailable, keeps last known role to prevent dual-master alerting
- When HA is explicitly disabled, returns STANDALONE
- Maintenance mode propagation: checks both local and cluster-wide maintenance files

---

## State Files

All persisted in `~spiraluser/.spiralsentinel/`:

| File | Purpose |
|------|---------|
| `config.json` | Full configuration (chmod 600) |
| `state.json` | 500+ persisted keys: report timestamps, alert cooldowns, miner state, block tracking, earnings, achievements, network baselines, chronic issues |
| `history.json` | Multi-coin historical data (v2): per-coin difficulty, network hashrate, fleet hashrate. Sampled every 15 min. 2-year retention. |
| `nicknames.json` | Miner nicknames |
| `maintenance_pause` | Maintenance mode state (pause_until, reason) |
| `fallback_notifications.log` | Written when all notification channels fail (5MB rotation) |

State is persisted via atomic write (temp file + fsync + rename).

---

## API Endpoints Called

### Spiral Stratum (`http://localhost:4000`)

| Endpoint | Purpose |
|----------|---------|
| `GET /api/pools` | Pool list, coin detection |
| `GET /api/pools/{id}/miners` | Connected miners |
| `GET /api/pools/{id}/blocks` | Block history for found/orphan detection |
| `POST /api/admin/device-hints` | Push device classification (requires X-API-Key) |
| `GET /api/sentinel/alerts` | Infrastructure alerts from Go stratum (supports `?since=`) |

### Prometheus (`http://localhost:9100/metrics`)

Circuit breaker, backpressure, WAL errors, ZMQ health, share loss. Requires Bearer token if configured.

### Dashboard (`http://localhost:1618`)

| Endpoint | Purpose |
|----------|---------|
| `GET /api/config/server-mode` | Detect solo vs multi-coin mode |

### External

| URL | Purpose |
|-----|---------|
| CoinGecko API | Coin prices in all fiat currencies |
| chainz.cryptoid.info | DGB wallet balance |
| blockchain.info | BTC wallet balance |
| api.blockchair.com | BCH wallet balance |

---

## Command-Line Arguments

| Argument | Short | Description |
|----------|-------|-------------|
| *(none)* | | Start monitoring (main loop) |
| `--help` | `-h` | Show help text |
| `--status` | `-s` | One-shot status check: network, fleet, miners, prices |
| `--test` | `-t` | Send test webhook message |
| `--reload` | `-r` | Hot-reload miner database (creates trigger file for running instance) |
| `--reset` | | Fleet-wide reboot of ALL miners (interactive confirmation) |

### Signal Handlers

- `SIGTERM` (systemd stop): Flush pending alerts, save state, exit 0
- `SIGINT` (Ctrl+C): Same as SIGTERM
- `SIGHUP` (Linux): Same as SIGTERM
- Second signal during shutdown: Force exit 1

---

## Environment Variables

| Variable | Maps To |
|----------|---------|
| `POOL_API_URL` | `pool_api_url` |
| `SPIRAL_ADMIN_API_KEY` | `pool_admin_api_key` |
| `DISCORD_WEBHOOK_URL` | `discord_webhook_url` |
| `TELEGRAM_BOT_TOKEN` | `telegram_bot_token` |
| `TELEGRAM_CHAT_ID` | `telegram_chat_id` |
| `XMPP_JID` | `xmpp_jid` |
| `XMPP_PASSWORD` | `xmpp_password` |
| `XMPP_RECIPIENT` | `xmpp_recipient` |
| `EXPECTED_FLEET_THS` | `expected_fleet_ths` |
| `WALLET_ADDRESS` | `wallet_address` |
| `ALERT_THEME` | `alert_theme` |

---

## Alert Cooldowns

The `alert_cooldowns` config key is a dict that merges with built-in defaults. Set any alert type to `0` for no cooldown, or a positive integer for seconds between alerts.

| Alert Type | Default Cooldown |
|------------|-----------------|
| `miner_offline` | 0 (always) |
| `miner_online` | 0 (always) |
| `block_found` | 0 (always) |
| `temp_critical` | 0 (always) |
| `circuit_breaker` | 0 (always) |
| `wal_errors` | 0 (always) |
| `temp_warning` | 3600s (1h) |
| `hashrate_crash` | 3600s (1h) |
| `degradation` | 3600s (1h) |
| `pool_hashrate_drop` | 1800s (30m) |
| `miner_reboot` | 600s (10m) |
| `power_event` | 600s (10m) |
| `price_crash` | 14400s (4h) |
| `update_available` | 86400s (24h) |

---

*Spiral Sentinel &mdash; Black Ice 1.0*
