# Upgrading to Spiral Pool v1.1.0 (Phi Forge)

## Is a full reinstall required?

**No. There are zero incompatibilities between v1.0.0 and v1.1.0 for any coin.**

`upgrade.sh` handles the entire upgrade in-place. Your blockchain data, database records, wallet files, `config.yaml`, Sentinel state (achievements, miner nicknames, stats history), SSL certificates, and HA/VIP configuration are **all preserved**. The upgrade takes 2–5 minutes with automatic rollback if anything fails.

---

## What's new in v1.1.0

All features below are active immediately after `upgrade.sh` completes. No manual config changes are required — all new config keys have sensible defaults that are used automatically if not present in your `config.json`.

### New Sentinel alerts

| Alert | Default behaviour | Config keys to tune |
|-------|------------------|---------------------|
| **Dry streak** | Fires after 3× ETB with no block found | `dry_streak_enabled`, `dry_streak_multiplier` |
| **Difficulty change** | Fires when difficulty drifts ≥25% from last-alert baseline | `difficulty_alert_enabled`, `difficulty_alert_threshold_pct` |
| **Disk space** | Warning at 85%, critical at 95% on `/`, `/spiralpool`, `/var` | `disk_monitor_enabled`, `disk_warn_pct`, `disk_critical_pct`, `disk_monitor_paths` |
| **BTC mempool congestion** | Fires when BTC mempool exceeds 50,000 transactions | `mempool_alert_enabled`, `mempool_alert_threshold` |
| **Stratum down** | Fires after pool API unreachable for 5+ minutes (bypasses quiet hours) | — |
| **Backup staleness** | Fires if newest backup is >2 days old (only when backup cron is installed) | `backup_stale_enabled`, `backup_stale_days` |
| **Config validation** | Fires once at startup if `config.json` has placeholder/invalid values | — |

### Intel report enhancements

- **ETB** (Expected Time to Block) is now shown in the NETWORK section of every 6h/daily report
- **Per-miner health score** appears next to each miner in the RIGS section (💚/💛/🔴)
- **Backup status** appears in reports when the backup cron is installed

### HA blip suppression

Role-change alerts (`ha_demoted` / `ha_promoted`) now use a **90-second confirmation window** before firing. Brief keepalived VRRP election blips that self-resolve are suppressed silently. Tune with `ha_role_change_confirm_secs` in `config.json` (default: `90`).

### Scheduled maintenance windows

Add time windows during which non-critical Sentinel alerts are muted:

```json
// ~/.spiralsentinel/config.json  (or $INSTALL_DIR/config/sentinel/config.json)
{
  "scheduled_maintenance_windows": [
    { "start": "02:00", "end": "04:00", "days": [6] }
  ]
}
```

Block found and scheduled intel reports always go through regardless.

### spiralctl improvements

- `spiralctl status` now shows service uptime next to each service and a **SCHEDULED TASKS** section at the bottom
- `spiralctl version` now shows a full version table including stratum binary and all installed coin daemons
- `spiralctl miners` — list connected miners with live hashrate and share counts
- `spiralctl miners kick <IP>` — disconnect all stratum sessions from a miner IP
- `spiralctl workers` — per-worker breakdown (miner → rig → hashrate + acceptance rate)
- `spiralctl miner nick <IP> <name>` — set a display name for a miner in Sentinel
- `spiralctl log errors [service] [window]` — filter service logs for errors/warnings with optional service and time window filters
- `spiralctl config validate` — dry-run config check (YAML/JSON syntax, placeholder detection, admin key cross-checks)
- `spiralctl config notify-test` — send a test notification to every configured channel

### New notification channels

Two new channels are available. Both require configuration in `config.json` — no action required if you don't use them.

| Channel | Key(s) | Notes |
|---------|--------|-------|
| **ntfy** | `ntfy_url`, `ntfy_token` | Free push notifications. Set `ntfy_url` to your topic URL (e.g. `https://ntfy.sh/my-topic`). |
| **Email (SMTP)** | `smtp_enabled`, `smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_to` | Works with Gmail, Outlook, or self-hosted SMTP. STARTTLS (587) and SSL/TLS (465) supported. |

### Telegram bot — new commands

Three new commands are available when Telegram is configured. No configuration change needed — activate automatically.

| Command | What it does |
|---------|--------------|
| `/uptime` | Sentinel process uptime + stratum service uptime (from systemd) |
| `/pause [minutes]` | Pause non-critical alerts (default 30 min). Same as `spiralctl pause`. |
| `/resume` | Resume alerts immediately if paused |

Full command list after upgrade: `/status`, `/miners`, `/hashrate`, `/blocks`, `/uptime`, `/pause`, `/resume`, `/cooldowns`, `/help`.

### PostgreSQL maintenance timer

A weekly `VACUUM ANALYZE` timer (`spiralpool-pg-maintenance.timer`) is now installed automatically. It runs Sundays at 03:00 and is safe on HA replicas (skips automatically). No action required.

---

## Go code changes — compatibility analysis

The v1.0.0 → v1.1.0 changes are listed below. **None require a reinstall, OS change, config change, or manual migration.**

| Component | Change | Impact on existing installs |
|-----------|--------|-----------------------------|
| `pool.go` — `getAlgoBlockTime()` | QBX moved from 600s bucket to correct 150s bucket | Only affects QBX vardiff. All other coins (BTC, LTC, DGB, etc.) unchanged. |
| `api/server.go` — `POST /api/admin/kick` | New endpoint to disconnect miner stratum sessions by IP; requires `X-API-Key` header | New feature. No breaking changes to existing endpoints or clients. |
| `SpiralSentinel.py` | QBX added to all lookup tables; `update_available` and `missing_payout` alert dedup fixed | Discord notifications now reliably deliver after quiet-hours suppression. Behavioral only. |
| `database/migrate.go` | No new migrations in v1.1.0 | Existing schema (migrations 1–10) carried forward unchanged. |
| Version strings | `1.0.0 / BLACKICE` → `1.1.0 / PHI_FORGE` throughout | Cosmetic. |

### Database compatibility

The migration system (migrations 1–10) is entirely additive:
- `CREATE TABLE IF NOT EXISTS` — idempotent, never destructive
- `ADD COLUMN IF NOT EXISTS` — only adds; never drops or renames
- `CREATE INDEX IF NOT EXISTS` — idempotent
- `schema_migrations` table tracks applied versions — already-applied migrations are skipped

**v1.1.0 adds zero new migrations.** A v1.0.0 database requires no migration at all.

### Config.yaml compatibility

The config format is **unchanged**. Your existing `config.yaml` works without modification. All coin entries (BTC, LTC, DGB, merge-mined, multi-algo) continue working exactly as before.

---

## Standard upgrade

```bash
cd /spiralpool
chmod +x upgrade.sh && sudo ./upgrade.sh
```

> **Note for Windows users:** If you SCP'd the upgrade.sh file from a Windows machine, Windows does not preserve Unix execute permissions. The `chmod +x` above is required before running. If you deployed via git clone or SCP'd from another Linux machine, execute permissions are already set and `chmod +x` is harmless.

The upgrade script:
1. Detects your current version and confirms before proceeding
2. Backs up all critical files to `/spiralpool/backups/pre-upgrade-1.0.0-to-1.1.0-TIMESTAMP/`
3. Enables maintenance mode (suppresses Discord alerts during upgrade)
4. Stops services gracefully
5. Downloads v1.1.0 from GitHub, builds the new stratum binary
6. Updates Sentinel, Dashboard, and helper scripts
7. Starts services — database migrations run automatically on first start (no-ops for existing installs)
8. Disables maintenance mode

**If the upgrade fails at any step, it automatically rolls back to the backup.**

### Upgrade options

| Flag | Effect |
|------|--------|
| `--auto` | Unattended — no confirmation prompts (for scripted/cron use) |
| `--full` | Also updates systemd service files and fixes config issues |
| `--local` | Use local files instead of downloading from GitHub |
| `--force` | Force reinstall even if already at current version |
| `--update-services` | Regenerate service files from templates only |
| `--fix-config` | Fix common config issues (missing `name:` fields, duration suffixes) |
| `--no-backup` | Skip backup (faster, but no rollback) |
| `--stratum-only` | Update stratum binary only |
| `--sentinel-only` | Update Sentinel only |
| `--dashboard-only` | Update Dashboard only |

All flags follow the same pattern — always include `chmod +x` when running from a manually transferred file:

```bash
chmod +x upgrade.sh && sudo ./upgrade.sh --local --full
chmod +x upgrade.sh && sudo ./upgrade.sh --force
chmod +x upgrade.sh && sudo ./upgrade.sh --fix-config
```

Most operators only need `chmod +x upgrade.sh && sudo ./upgrade.sh`.

---

## Adding new coins after upgrading

The following covers adding any coin to an existing installation. This is an opt-in process — your current coins are unaffected.

### Quick method (recommended)

Use `spiralctl coin enable` to add any supported coin. This handles everything automatically — daemon installation, wallet generation, config.yaml update, firewall ports, and service restart:

```bash
spiralctl coin enable BTC       # Add Bitcoin
spiralctl coin enable LTC       # Add Litecoin
spiralctl coin enable NMC       # Add Namecoin (merge-mine with BTC)
```

After enabling, visit the Dashboard at `http://<server>:1618/setup` to verify wallet addresses are populated. The setup wizard auto-detects all active coins and shows a wallet input for each.

---

### Manual method (advanced)

If you prefer manual control, you can add coins by editing config files directly.

#### Standalone SHA-256d coins (BTC, BCH, BC2, DGB, QBX)

These run independently with no parent chain.

**1. Add a stanza to `/spiralpool/config/config.yaml`:**

```yaml
coins:
  - symbol: QBX                         # or BTC, BCH, BC2, DGB
    name: "Q-BitX"
    algorithm: "sha256d"
    address: ""                          # fill in step 2
    nodes:
      - host: "127.0.0.1"
        port: 8344                       # coin's RPC port
        user: "rpcuser"
        password: "rpcpassword"
        zmq:
          endpoint: "tcp://127.0.0.1:28344"   # coin's ZMQ port
    stratum:
      port: 20335                        # stratum V1 port
      port_v2: 20336                     # stratum V2 port (optional)
      tls_port: 20337                    # TLS port (optional)
```

**2. Create a wallet address** (for coins with CLI support):

```bash
spiralpool-wallet --coin QBX   # also works for BTC, BCH, BC2, DGB, NMC, SYS, XMY, FBTC, LTC, DOGE, PEP, CAT
```

Copy the address into the `address:` field above, or enter it via the Dashboard at `http://<server>:1618/setup`.

**3. Start the coin daemon**, then restart stratum:

```bash
sudo systemctl restart spiralstratum
```

Database tables for the new coin are created automatically on startup.

---

#### Standalone Scrypt coins (LTC, DOGE, DGB-SCRYPT, PEP, CAT)

Same process as SHA-256d. Set `algorithm: "scrypt"` instead:

```yaml
coins:
  - symbol: LTC
    name: "Litecoin"
    algorithm: "scrypt"
    address: ""
    nodes:
      - host: "127.0.0.1"
        port: 9332
        user: "rpcuser"
        password: "rpcpassword"
        zmq:
          endpoint: "tcp://127.0.0.1:28933"
    stratum:
      port: 7333
```

---

### Merge-mined (AuxPoW) coins

Merge mining is configured in `mergeMining.auxChains[]` alongside the parent coin. Both the parent chain daemon and the aux chain daemon must be running and fully synced.

**Supported AuxPoW pairs:**

| Parent | Aux chain(s) |
|--------|-------------|
| BTC | NMC, FBTC, SYS, XMY |
| LTC | DOGE, PEP |

**Example: add Namecoin merge-mined under Bitcoin**

```yaml
coins:
  - symbol: BTC
    name: "Bitcoin"
    algorithm: "sha256d"
    address: "your_btc_address"
    nodes:
      - host: "127.0.0.1"
        port: 8332
        user: "rpcuser"
        password: "rpcpassword"
        zmq:
          endpoint: "tcp://127.0.0.1:28332"
    stratum:
      port: 4333

mergeMining:
  enabled: true
  auxChains:
    - symbol: "NMC"
      enabled: true
      address: "your_nmc_address"
      daemon:
        host: "127.0.0.1"
        port: 8336         # Namecoin RPC port
        user: "rpcuser"
        password: "rpcpassword"
```

The pool automatically embeds the aux chain commitment in the parent coinbase. Miners only connect to the parent stratum port — merge mining is transparent to them.

**Note:** NMC, SYS, XMY require external wallet software to generate addresses (no CLI support in `spiralpool-wallet`).

---

### Multi-algorithm (running BTC + LTC on the same pool instance)

Add multiple entries to `coins[]` — each with its own stratum port. No special config key needed:

```yaml
coins:
  - symbol: BTC
    algorithm: "sha256d"
    stratum:
      port: 4333
    # ... BTC config ...

  - symbol: LTC
    algorithm: "scrypt"
    stratum:
      port: 7333
    # ... LTC config ...
```

Miners connect to the appropriate stratum port for their hardware algorithm. The pool enforces algorithm isolation — a SHA-256d miner cannot submit shares to a Scrypt pool and vice versa.

---

## Verifying the upgrade

```bash
spiralctl status
```

The version line should show `1.1.0`. If Sentinel is running:

```bash
sudo journalctl -u spiralsentinel -n 20
```

Look for `Spiral Sentinel v1.1.0-PHI_FORGE` followed by `PHI FORGE EDITION` in the startup log.

---

## Rolling back manually

Automatic rollback fires if the upgrade fails mid-way. If you need to manually roll back after a completed upgrade:

```bash
# List available backups
ls /spiralpool/backups/

# Restore from backup
sudo spiralpool-restore /spiralpool/backups/pre-upgrade-1.0.0-to-1.1.0-TIMESTAMP.tar.gz
```

---

## Troubleshooting

**Services fail to start after upgrade**
Check: `sudo journalctl -u spiralstratum -n 50`
Common cause: config.yaml issue. Run `chmod +x upgrade.sh && sudo ./upgrade.sh --fix-config` for automatic fixes.

**Stratum binary won't build**
Ensure Go 1.26.1 is installed: `go version`
If missing or wrong version, re-run `upgrade.sh` — it downloads and installs Go 1.26.1 automatically from go.dev. Do not use `sudo apt install golang-go` — the Ubuntu package is too old.

**Already on latest version**
Force reinstall: `chmod +x upgrade.sh && sudo ./upgrade.sh --force`

**"Permission denied" or "command not found" running upgrade.sh**
This happens when upgrade.sh was transferred from a Windows machine (SCP from Windows strips Unix execute permissions). Fix:
```bash
chmod +x upgrade.sh && sudo ./upgrade.sh
```
This does not affect git clone deployments or Linux-to-Linux SCP transfers, which preserve execute permissions.

**Locked by another operation**
This is automatically resolved. If a previous install or upgrade crashed, the stale lock file is detected (no running process holds it) and cleared automatically. No manual intervention is needed.

If for some reason the lock persists and the auto-clear fails:
```bash
sudo rm -f /var/lock/spiralpool-operation.lock /var/lock/spiralpool-operation.lock.info
```

**HA cluster upgrade**
Upgrade the MASTER node first. The BACKUP node will pick up the new binary when it resumes its sync cycle. HA configuration, VIP, etcd, and Patroni are untouched by `upgrade.sh`.

---

## Automated upgrades (Sentinel-driven)

If you selected **Auto-update** during installation, Sentinel handles upgrades automatically. No manual steps required.

**How it works:**
1. Sentinel checks GitHub for a new release every 6 hours (anonymous — no token needed, repo is public)
2. If a new version is available and `auto_update_mode: auto` is set in config, Sentinel runs `sudo /spiralpool/upgrade.sh --auto` automatically
3. Maintenance mode is enabled for the duration — Discord alerts are suppressed during the upgrade
4. On success, Sentinel sends a Discord notification confirming the upgrade completed
5. On failure, Sentinel sends an alert with the error output

**Sudoers:** The installer pre-configures `spiraluser` with passwordless sudo for `upgrade.sh`. Existing installs receive this entry automatically during their first upgrade via `upgrade.sh`.

**Execute permission:** When Sentinel runs `sudo /spiralpool/upgrade.sh --auto`, it uses the absolute installed path `/spiralpool/upgrade.sh` — not a freshly-SCPd file. The installed copy already has execute permissions set by the installer. The Windows SCP issue does not affect automated upgrades.

**To enable auto-update after initial install:**
```yaml
# In /spiralpool/config/config.yaml
auto_update_mode: auto    # Options: notify (default) | auto
```

**To check/trigger manually:**
```bash
spiralctl status            # Shows current version
sudo ./upgrade.sh --check   # Check GitHub for latest version
```

---

*Spiral Pool — Phi Forge 1.1.1 — Built on what came before. Growing toward phi.*
