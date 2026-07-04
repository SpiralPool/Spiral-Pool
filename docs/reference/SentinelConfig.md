# Spiral Sentinel — Config & Alert Tuning Reference

A practical guide to every alert Sentinel can fire, every knob that controls it, and how to silence or tune the noisy ones.

## Where the config lives on the server

Sentinel runs as the `spiraluser` service account. The config file lives in **one of two places**, and Sentinel picks whichever it can write to at startup:

| Priority | Path | When this one is used |
|---|---|---|
| 1 (normal) | `~spiraluser/.spiralsentinel/config.json` | Standard install — `spiraluser`'s home directory |
| 2 (fallback) | `$INSTALL_DIR/config/sentinel/config.json` | When systemd `ProtectHome=yes` blocks home-dir access |

> **Note on paths:** `spiraluser`'s home is usually `/home/spiraluser/`, but on some systems it may differ. `$INSTALL_DIR` defaults to `/spiralpool` but can be customized — check the `SPIRALPOOL_INSTALL_DIR` environment variable in the service unit. **Don't hard-code these paths; resolve them first.**

**Resolve both paths on your specific server:**
```bash
# spiraluser's actual home directory
getent passwd spiraluser | cut -d: -f6
# → e.g. /home/spiraluser

# Current install directory (from the live service unit)
systemctl show spiralsentinel -p Environment | grep -oP 'SPIRALPOOL_INSTALL_DIR=\K\S+'
# → e.g. /spiralpool
```

**Find which config file is actually in use:**
```bash
SPIRALUSER_HOME=$(getent passwd spiraluser | cut -d: -f6)
INSTALL_DIR=$(systemctl show spiralsentinel -p Environment | grep -oP 'SPIRALPOOL_INSTALL_DIR=\K\S+')
ls -la "$SPIRALUSER_HOME/.spiralsentinel/config.json" "$INSTALL_DIR/config/sentinel/config.json" 2>/dev/null
```
Whichever file exists (and has a recent mtime) is the live one.

**Edit it (must be done as root or `spiraluser`):**
```bash
# primary location
sudo -u spiraluser nano "$(getent passwd spiraluser | cut -d: -f6)/.spiralsentinel/config.json"

# — or, if the fallback is in use:
sudo nano "$(systemctl show spiralsentinel -p Environment | grep -oP 'SPIRALPOOL_INSTALL_DIR=\K\S+')/config/sentinel/config.json"
```

**Reload after every edit:**
```bash
sudo systemctl restart spiralsentinel
```

**Permissions:** Sentinel re-applies `chmod 600` on every load — don't worry about preserving them manually.

**Other state lives in the same directory:**
- `miners.json` — miner inventory (hot-reloadable with `--reload`)
- `maintenance_pause` — touch this file to mute alerts immediately, delete to resume
- `*.state` / `*.json` — assorted history/cooldown state (safe to leave alone)

> **Tip:** every key listed below also has a default in `DEFAULT_CONFIG` inside [SpiralSentinel.py](../../src/sentinel/SpiralSentinel.py#L889). If you delete a key from your `config.json`, the default is used.

---

## Table of Contents

1. [The Ways to Silence an Alert](#the-ways-to-silence-an-alert)
2. [Alert Cooldowns (the throttle table)](#alert-cooldowns-the-throttle-table)
3. [Network & Pool Alerts](#network--pool-alerts)
4. [Miner Fleet Alerts](#miner-fleet-alerts)
5. [Wallet & Earnings Alerts](#wallet--earnings-alerts)
6. [Difficulty & Dry-Streak Alerts](#difficulty--dry-streak-alerts)
7. [Disk & Backup Alerts](#disk--backup-alerts)
8. [Market & Sats Surge Alerts](#market--sats-surge-alerts)
9. [Mempool Congestion (BTC)](#mempool-congestion-btc)
10. [Infrastructure Health (Prometheus-fed)](#infrastructure-health-prometheus-fed)
11. [HA / Failover Alerts](#ha--failover-alerts)
12. [Periodic Reports](#periodic-reports)
13. [Quiet Hours & Maintenance Windows](#quiet-hours--maintenance-windows)
14. [Startup Suppression](#startup-suppression)
15. [Alert Batching (digest mode)](#alert-batching-digest-mode)
16. [Notification Channels](#notification-channels)
17. [Recipes — common silencing tasks](#recipes--common-silencing-tasks)

---

## The Ways to Silence an Alert

| Method | Effect | When to use |
|---|---|---|
| **Mute by name** — `spiralctl alerts disable <type>` (adds to `disabled_alerts`) | That alert/report never fires | Works for *any* alert or report, including ones with no `*_enabled` flag |
| **Disable** — set the `*_enabled` flag to `false` | Alert never fires | The alert has a dedicated toggle and you don't care about the signal |
| **Raise the threshold** — increase the `*_threshold*` value | Alert fires only on bigger swings | You want it, but it's too sensitive |
| **Increase cooldown** — raise the entry in `alert_cooldowns` | Alert fires at most once per N seconds | The check is fine, the repetition is the problem |

A cooldown of `0` means *no throttle, fire every time*. Set it to a large number (e.g. `86400` = 24h) instead of disabling if you still want occasional reminders.

### Mute by name (`disabled_alerts`)

The simplest universal off-switch. `spiralctl alerts disable <type>` appends the alert type to a `disabled_alerts` list in `config.json`; `send_alert()` drops any alert whose type is in that list. Because the match also strips `infra_`/`pool_` prefixes, one canonical name silences the native, Prometheus, and Go-bridged variants together.

```json
"disabled_alerts": ["difficulty_change", "weekly_report", "chain_tip_stall"]
```

```bash
spiralctl alerts list                      # every alert/report + on/off state
spiralctl alerts disable difficulty_change
spiralctl alerts enable  difficulty_change
spiralctl alerts reset                     # clear all mutes
sudo systemctl restart spiralsentinel      # required to apply
```

`block_found` is the one type that can never be muted. Unlike the `*_enabled` flags (which only exist for ~a dozen alerts), this works for every alert type — including flag-less ones like `zombie_miner`, `miner_reboot`, and `hashrate_divergence`. See [spiralctl-reference.md](spiralctl-reference.md#spiralctl-alerts) for the full command.

---

## Alert Cooldowns (the throttle table)

The single most useful block for spam control. Every value is in **seconds**.

```json
"alert_cooldowns": {
    "hashrate_crash": 21600,
    "miner_offline": 0,
    "miner_online": 0,
    "miner_reboot": 600,
    "temp_warning": 3600,
    "temp_critical": 0,
    "zombie_miner": 3600,
    "degradation": 3600,
    "power_event": 600,
    "pool_hashrate_drop": 1800,
    "block_found": 0,
    "sats_surge": 0,
    "wallet_drop": 3600,
    "dry_streak": 21600,
    "difficulty_change": 21600,
    "disk_warning": 3600,
    "disk_critical": 300,
    "mempool_congestion": 3600,
    "backup_stale": 86400
}
```

| Key | Default | What it throttles |
|---|---|---|
| `hashrate_crash` | 6h | Pool-wide hashrate falls off a cliff |
| `miner_offline` | none | Individual rig stops sending shares |
| `miner_online` | none | Rig comes back |
| `miner_reboot` | 10m | Rig restarted (uptime reset detected) |
| `temp_warning` | 1h | Chip/board temp above warn threshold |
| `temp_critical` | none | Critical temp — never throttled by design |
| `zombie_miner` | 1h | Hashing but no shares (broken pointer) |
| `degradation` | 1h | Sustained hashrate drop on one rig |
| `power_event` | 10m | Multiple rigs offline simultaneously |
| `pool_hashrate_drop` | 30m | Pool's own hashrate degraded |
| `block_found` | none | You always want this |
| `sats_surge` | none | Has its own per-coin internal cooldown |
| `wallet_drop` | 1h | Solo wallet balance dropped unexpectedly |
| `dry_streak` | 6h | Long stretch with no block |
| `difficulty_change` | 6h | Network diff swung past threshold |
| `disk_warning` | 1h | Disk usage above warn % |
| `disk_critical` | 5m | Disk usage above critical % |
| `mempool_congestion` | 1h | BTC mempool huge |
| `backup_stale` | 24h | Newest backup older than threshold |

**To make any of these less spammy:** raise the number. **To always-fire:** set to `0`. **To never-fire:** see the per-feature toggle in the sections below.

---

## Network & Pool Alerts

| Key | Default | Purpose |
|---|---|---|
| `net_drop_threshold_phs` | 48 | Network hashrate (PH/s) below this → "drop" alert |
| `net_reset_threshold_phs` | 52 | Recovery threshold (hysteresis) |
| `pool_share_validation` | true | Cross-check pool API for missing shares |
| `pool_no_shares_threshold_min` | 30 | Alert if no pool shares for N minutes |

**Disable network drop alerts:** lower thresholds to `0` (effectively disables) or remove pool drop checks via cooldown `pool_hashrate_drop = 86400`.

---

## Miner Fleet Alerts

| Key | Default | Purpose |
|---|---|---|
| `miner_offline_threshold_min` | 10 | Minutes offline before alert |
| `temp_warning` | 75 | °C — chip/board warn temp |
| `temp_critical` | 85 | °C — chip/board critical temp |
| `health_warn_threshold` | 70 | Health score (0-100) warn level |
| `expected_fleet_ths` | 22.0 | Expected total fleet TH/s for delta calcs |
| `expected_fleet_ths_disabled` | false | Set `true` to opt out of fleet delta math |
| `auto_restart_enabled` | true | Sentinel can SSH-reboot offline rigs |
| `auto_restart_min_offline` | 20 | Minutes offline before auto-restart attempt |
| `auto_restart_cooldown` | 1800 | Seconds between auto-restart attempts |
| `blip_detection_enabled` | true | Suppress 1-cycle false offlines |
| `firmware_auto_detect` | true | Probe BraiinsOS/Vnish on port 80 if CGMiner fails |
| `pool_url` | "" | Expected stratum URL for mismatch detection |
| `fallback_pool_urls` | [] | Additional valid stratum URLs |
| `push_device_hints` | true | Push device info to pool for diff hints |

**Mute a chatty rig temporarily:** put it on the maintenance list (see `scheduled_maintenance_windows`) or stop the miner — Sentinel won't alert on intentionally-stopped rigs that have never been seen.

---

## Wallet & Earnings Alerts

| Key | Default | Purpose |
|---|---|---|
| `wallet_drop_alert_enabled` | true | Solo wallet balance unexpectedly dropped |
| `wallet_address` | "YOUR_DGB_ADDRESS" | Legacy single-coin wallet (V1) |
| `coins[].wallet_address` | per-coin | V2 multi-coin wallet addresses |

**To disable:** `"wallet_drop_alert_enabled": false`.

---

## Difficulty & Dry-Streak Alerts

```json
"difficulty_alert_enabled": true,
"difficulty_alert_threshold_pct": 25,

"dry_streak_enabled": true,
"dry_streak_multiplier": 5,
```

| Key | Default | Purpose |
|---|---|---|
| `difficulty_alert_enabled` | true | Network diff swing alert (the spammy one) |
| `difficulty_alert_threshold_pct` | 25 | % change from baseline before alerting |
| `dry_streak_enabled` | true | Alert when no block found for N × expected interval |
| `dry_streak_multiplier` | 5 | Multiplier — 5× expected = alert |

**Silence diff alerts entirely:** `"difficulty_alert_enabled": false`.
**Make them less sensitive:** raise `difficulty_alert_threshold_pct` to 50 or higher.
**Make them less repetitive:** raise the `difficulty_change` cooldown (default already 6h).

---

## Disk & Backup Alerts

```json
"disk_monitor_enabled": true,
"disk_warn_pct": 85,
"disk_critical_pct": 95,
"disk_monitor_paths": ["/", "/spiralpool", "/var"],

"backup_stale_enabled": true,
"backup_stale_days": 2,
```

**Disable disk monitoring:** `"disk_monitor_enabled": false`.
**Stop watching a specific mount:** remove it from `disk_monitor_paths`.
**Loosen backup staleness:** raise `backup_stale_days`.

---

## Market & Sats Surge Alerts

| Key | Default | Purpose |
|---|---|---|
| `sats_change_alert_pct` | 15 | Coin price moved this % → alert |
| `sats_surge_enabled` | true | Multi-day sat-value surge detection |
| `sats_surge_threshold_pct` | 25 | % rise over baseline to trigger |
| `sats_surge_lookback_days` | 7 | Window for baseline comparison |
| `sats_surge_sample_interval` | 3600 | Sample period (s) |
| `sats_surge_cooldown_hours` | 24 | Per-coin re-alert cooldown |
| `odds_alert_threshold` | 40 | High-luck/odds alert sensitivity |

**Silence:** `"sats_surge_enabled": false` and raise `sats_change_alert_pct` to a number you'll never hit (e.g. 999).

---

## Mempool Congestion (BTC)

```json
"mempool_alert_enabled": true,
"mempool_alert_threshold": 50000,
```

Only relevant if you're mining BTC. Disable with `"mempool_alert_enabled": false`.

---

## Infrastructure Health (Prometheus-fed)

These watch the Spiral Stratum Go backend's `/metrics` endpoint.

| Key | Default | Purpose |
|---|---|---|
| `metrics_enabled` | true | Pull Prometheus metrics at all |
| `metrics_url` | http://localhost:9100/metrics | Where to scrape |
| `metrics_token` | "" | Bearer token (from `SPIRAL_METRICS_TOKEN`) |
| `metrics_fetch_interval` | 60 | Scrape every N seconds |
| `infra_circuit_breaker_alert` | true | Daemon circuit-breaker state changes |
| `infra_backpressure_alert` | true | Share-pipeline backpressure level ≥ 2 |
| `infra_zmq_health_alert` | true | ZMQ degradation (note: never has ZMQ — ignore) |
| `infra_wal_errors_alert` | true | WAL write/commit errors |
| `infra_share_loss_alert` | true | Share batch drops |

**To turn off the entire infra block:** `"metrics_enabled": false`. **To silence a single signal:** flip its `infra_*_alert` to `false`.

---

## HA / Failover Alerts

```json
"ha_role_change_confirm_secs": 90,
```

Sentinel waits this many seconds before alerting on a keepalived MASTER↔BACKUP transition, so VRRP election blips don't page you. Raise it if your network has noisy elections.

---

## Periodic Reports

| Key | Default | Purpose |
|---|---|---|
| `report_frequency` | "6h" | "6h" / "daily" / "off" |
| `report_hours` | [6, 12, 18] | Hours to fire 6h reports |
| `final_report_time` | "21:55" | Last report before quiet hours (or `null`) |
| `major_report_hour` | 6 | Hour for the daily "major" report |
| `weekly_report_day` | 0 | 0 = Monday |
| `monthly_report_day` | 1 | Day-of-month |
| `enable_6h_reports` | true | Toggle 6h/daily intel reports |
| `enable_weekly_reports` | true | Toggle weekly summary |
| `enable_monthly_reports` | true | Toggle monthly earnings |
| `enable_quarterly_reports` | true | Toggle quarterly |

**Silence all scheduled reports:** `"report_frequency": "off"` AND set the four `enable_*_reports` flags to `false`. (Some report types check the toggle, others check the frequency — set both to be sure.)

---

## Quiet Hours & Maintenance Windows

```json
"quiet_hours_start": 22,
"quiet_hours_end": 6,
```

Non-critical alerts are suppressed between these hours (24h clock). Critical temps and `block_found` always bypass.

```json
"scheduled_maintenance_windows": [
    {"start": "02:00", "end": "04:00", "days": [6], "reason": "Weekly backup"}
]
```

`days` uses 0 = Monday … 6 = Sunday. Omit `days` to apply every day. Useful for backup windows, planned reboots, ISP maintenance, etc.

---

## Startup Suppression

When Sentinel restarts, it suppresses non-critical alerts for the first N minutes so the grace-period chaos doesn't page you.

```json
"startup_alert_suppression_min": 30,
"startup_suppression_bypass": [
    "block_found",
    "startup_summary",
    "temp_critical",
    "6h_report",
    "weekly_report",
    "monthly_earnings",
    "quarterly_report"
]
```

**Add an alert type to `startup_suppression_bypass`** if you'd rather hear about it immediately even at startup.

---

## Alert Batching (digest mode)

```json
"alert_batching_enabled": true,
"alert_batch_window_seconds": 300
```

When enabled, miner-related alerts that fire within the same 5-minute window are bundled into a single digest notification (huge spam reducer during power outages). The following types are **never batched** regardless: `block_found`, `startup_summary`, scheduled reports, `hashrate_crash`, `high_odds`.

**Disable digesting:** `"alert_batching_enabled": false`.
**Tighter / looser batching:** adjust `alert_batch_window_seconds`.

---

## Notification Channels

Each channel has its own enable flag and credentials. Leaving credentials blank disables that channel automatically.

| Channel | Enable flag | Required keys |
|---|---|---|
| Discord | (auto — set `discord_webhook_url`) | `discord_webhook_url` |
| Telegram | `telegram_enabled: true` | `telegram_bot_token`, `telegram_chat_id` |
| XMPP | `xmpp_enabled: true` | `xmpp_jid`, `xmpp_password`, `xmpp_recipient` |
| ntfy | (auto — set `ntfy_url`) | `ntfy_url` (`ntfy_token` optional) |
| Webhook | (auto — set `webhook_url`) | `webhook_url` (`webhook_headers` optional) |
| SMTP / Email | `smtp_enabled: true` | `smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_to` |

**To temporarily mute everything:** clear `discord_webhook_url` and set the other `*_enabled` flags to `false`. Sentinel keeps logging locally — see `journalctl -u spiralsentinel`.

**Test all configured channels:**
```bash
python3 /spiralpool/bin/SpiralSentinel.py --test
```

---

## Recipes — common silencing tasks

### "Just turn off one specific alert or report"
```bash
spiralctl alerts disable difficulty_change   # any alert type, or a report like weekly_report
sudo systemctl restart spiralsentinel
```
The universal off-switch — works even for alerts that have no `*_enabled` flag. `spiralctl alerts list` shows every name.

### "Stop the network difficulty alert spam"
```json
"difficulty_alert_enabled": false
```
*Or, keep it but only on big swings:* `"difficulty_alert_threshold_pct": 50`. *Or just:* `spiralctl alerts disable difficulty_change`.

### "I'm doing planned maintenance for the next two hours"
```bash
touch ~/.spiralsentinel/maintenance_pause   # via PAUSE_FILE
# ...do work...
rm ~/.spiralsentinel/maintenance_pause
```

### "Stop paging me at night"
Already on by default. Adjust:
```json
"quiet_hours_start": 22,
"quiet_hours_end": 7
```

### "I never want temperature warnings, only criticals"
```json
"temp_warning": 999
```
(Critical still fires at `temp_critical`.)

### "Disable disk monitoring on /var only"
```json
"disk_monitor_paths": ["/", "/spiralpool"]
```

### "Stop the dry-streak alerts — I know I'm unlucky"
```json
"dry_streak_enabled": false
```

### "Less notification spam during a power outage"
Already handled — `alert_batching_enabled: true` digests miner alerts within 5-minute windows. Increase `alert_batch_window_seconds` to 600 for even fewer notifications.

### "Stop the weekly/monthly reports"
```json
"enable_weekly_reports": false,
"enable_monthly_reports": false,
"enable_quarterly_reports": false
```

### "Mute everything for one weekend"
```json
"scheduled_maintenance_windows": [
    {"start": "00:00", "end": "23:59", "days": [5, 6], "reason": "Weekend mute"}
]
```

### "Sentinel keeps alerting about ZMQ on "
 doesn't support ZMQ — polling fallback is by design. Either ignore or:
```json
"infra_zmq_health_alert": false
```

---

## Validation & Hot-Reload

- On every load, Sentinel runs `validate_config()` and **auto-corrects** invalid values (e.g. swaps `temp_warning` and `temp_critical` if reversed, clamps quiet hours to 0–23, resets negative cooldowns).
- Validation issues are logged at WARNING level — check `journalctl -u spiralsentinel | grep "Config:"` after editing.
- The miner database (`miners.json`) supports hot-reload via `--reload` — config changes still require a service restart.

---

## Where to look in code

- All defaults: [SpiralSentinel.py:889](../../src/sentinel/SpiralSentinel.py#L889) (`DEFAULT_CONFIG`)
- Validation rules: [SpiralSentinel.py:1271](../../src/sentinel/SpiralSentinel.py#L1271) (`validate_config`)
- Config loading: [SpiralSentinel.py:1442](../../src/sentinel/SpiralSentinel.py#L1442) (`load_config`)
- Difficulty alert logic: [SpiralSentinel.py:3856](../../src/sentinel/SpiralSentinel.py#L3856) (`check_difficulty_changes`)
- Cooldown enforcement: search `alert_cooldowns` in `SpiralSentinel.py`
