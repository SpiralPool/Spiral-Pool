# Upgrading to Spiral Pool v2.7.0 (Spiral Citadel)

## Is a full reinstall required?

**No. There are zero incompatibilities between any prior version (v1.0.0, v1.1.x, v1.2.x, v2.4.x, v2.5.x, v2.6.x) and v2.7.0 for the pool stack.** (The DigiByte **node** upgrade below is a separate step.)

`upgrade.sh` handles the entire upgrade in-place. Your blockchain data, database records, wallet files, `config.yaml`, Sentinel state (achievements, miner nicknames, stats history), SSL certificates, and HA/VIP configuration are **all preserved**. The upgrade takes 2–5 minutes with automatic rollback if anything fails.

---

## Bitcoin (BTC) node upgrade — Bitcoin Knots → Bitcoin Core 31.1

**This is not a routine version bump. Do not skip it.**

Spiral Pool previously shipped Bitcoin Knots. Knots builds carrying the `knots20260508` datestamp or later enforce BIP-110 ("RDTS") and follow the minority chain that split from Bitcoin on 8 August 2026 at block 961,632. That chain has produced a handful of blocks, sits hundreds of blocks behind, and its coins are not traded on any exchange. Roughly 99.85% of hashpower stayed on the majority chain.

Enforcement is **compiled into the binary**. Removing `consensusrules=rdts` from `bitcoin.conf` records consent and silences a warning — it does not change which chain the node follows. Only replacing the binary does that.

**Why you would not otherwise notice.** A node on the minority chain behaves normally in every visible respect: miners connect, shares validate, vardiff converges, the dashboard reports healthy hashrate, sync shows 100%. The only symptom is that BTC blocks never arrive — indistinguishable from ordinary solo-mining variance. If you also merge-mine, NMC/SYS/XMY keep finding blocks normally, because AuxPoW proofs reference the parent block header and never check which chain it came from. The pool keeps looking productive while the coin that pays best earns nothing.

### Running the upgrade

```bash
sudo /spiralpool/scripts/coin-upgrade.sh --coin BTC
```

The upgrade:

1. **Refuses to start if a legacy (BDB) wallet is present.** Bitcoin Core 30+ cannot load one. It asks the daemon over RPC when the daemon is running; when it is not, it inspects the wallet files on disk instead (a descriptor wallet is SQLite, a legacy one is Berkeley DB), so a stopped node does not block the upgrade — that matters because the recovery paths deliberately leave BTC stopped and then tell you to re-run this command. Migrate first, under the current binary, and back up before you do — migration is one-way:
   ```bash
   bitcoin-cli -rpcwallet=<name> backupwallet /path/outside/datadir/<name>.bak
   bitcoin-cli -rpcwallet=<name> migratewallet
   ```
   Most operators are unaffected: Spiral Pool creates descriptor wallets, and if you supplied an external address (hardware or air-gapped wallet) there is no wallet on the server at all. Check with `getwalletinfo | grep descriptors` — `true` means nothing to do.
2. **Replaces the binary** with Bitcoin Core 31.1, downloaded from bitcoincore.org and verified against a SHA256 committed in this repository. A mismatch aborts.
3. **Repairs the chain.** A binary swap alone is *not* sufficient. The enforcing daemon marked the majority chain's block 961,632 as rejected, and that marker persists on disk — Bitcoin Core reads the same block index, honours it, and will sit on the minority chain looking perfectly healthy. The upgrade clears it with `reconsiderblock` and waits for the reorg.
4. **Verifies the result**, and reports what it found. Be aware of the cases where it returns success without having confirmed the hash, because they are legitimate but easy to misread: the daemon was unreachable, the node has not yet synced past height 961,632, or the reorg was accepted and the node is still downloading. Each says so explicitly rather than claiming verification. The check is also skipped entirely under `--reindex`, since the node is rebuilding for hours. In all of those cases the stratum chain gate is what actually protects you — it re-runs this check at every startup and refuses to serve work until the hash matches.

### What to expect

The node keeps its entire history below block 961,632 — that is shared between both chains and is not rebuilt. It discards the handful of minority-chain blocks above it and downloads the majority chain from the split forward, which is several GB and grows the longer this is left. **No reindex and no resync from genesis are required.**

Two exceptions:

- **Pruned nodes.** A pruned node cannot reorganise further back than its retained window (minimum 288 blocks). The split is well past that, so the block data needed to rewind is gone and a **full resync is unavoidable**. The upgrade detects this and says so rather than failing obscurely.
- **If `reconsiderblock` does not take**, rerun with `--reindex`. Note `-reindex-chainstate` does *not* clear rejected-block markers; only a full `-reindex` rebuilds the block index.

### Before you resume mining

Remove `maxtipage` from `bitcoin.conf` if present. Miners on the stalled minority chain were advised to set `maxtipage=2592000` to suppress stale-tip warnings. On the majority chain that same line disables the daemon's initial-block-download gating for thirty days, so a wedged node reports `initialblockdownload: false` and every health check — the dashboard, Sentinel, and the pool's own sync gate — reads green.

The upgrade **reports** this and exits non-zero, listing the offending line numbers, but it does not edit your config: doing that safely requires knowing whether the value is actually unsafe (anything at or below Core's `86400` default is stricter than the default, not weaker), which network section it sits in, and it can only be applied with the daemon stopped. Comment the line out yourself and restart:

```bash
sudo sed -i -E 's/^([[:space:]]*)(maxtipage)/\1# \2/' /spiralpool/btc/bitcoin.conf
sudo systemctl restart bitcoind
```

Worth knowing what this does **not** break: the stratum chain gate computes tip staleness itself from `mediantime` against its own threshold and never reads the daemon's `maxtipage`, so the pool still refuses to mine a dead tip. The damage is that your monitoring disagrees with it.

The pool will not mine BTC until the chain verifies, so you are not burning electricity while any of this is in progress. To mine a non-majority chain deliberately, set `allow_nonmajority_chain: true` for the coin. It also disables the stale-tip refusal, so a node wedged on the **correct** chain will mine a dead tip too — that second effect is easy to miss and is rarely what you want.

---

## DigiByte (DGB) node upgrade — v9.26.5

**Coin daemon upgrades are separate from the pool stack upgrade.** `upgrade.sh` upgrades the Spiral Pool software only; it never touches coin daemons (they can require a resync). After it runs, it *flags* any coin node that is behind and tells you to run `coin-upgrade.sh`.

DigiByte Core **v9.26.5** is a patch release on top of v9.26.4. It:

1. **Fixes a DigiDollar oracle startup stall.** v9.26.4 re-evaluated the DigiDollar activation gate once per scanned block at startup — allocating a throwaway versionbits cache and re-running the BIP9 threshold state machine roughly 172,800 times. The node sat in `Starting network threads…` with RPC returning `error -28` and one CPU core pegged for 15 minutes or considerably longer on slower hardware, serving no block templates the entire time. v9.26.5 reuses the node's shared memoized versionbits cache and the scan finishes in about 3 seconds. **This is the reason to upgrade from v9.26.4.**
2. **Carries forward v9.26.4's pruning support and consensus rule** unchanged — see below. Nodes still on v9.26.3 also pick up that rule (redemption collateral gated on the activation floor, mainnet height 23,627,520) in this upgrade.

v9.26.5 itself changes **no consensus rules** on mainnet or testnet, so there is no coordination deadline. Upgrading is an **in-place binary swap — no reindex and no config changes**, for full and pruned nodes alike. Run `sudo /spiralpool/scripts/coin-upgrade.sh` (or `spiralctl coin-upgrade`).

> **Recognising the v9.26.4 stall.** If a DGB node is stuck after a restart, `digibyte-cli getblockchaininfo` returns `error -28  "Starting network threads…"` and `debug.log` shows `Oracle: Scanning last 172800 blocks for oracle prices` with no completion line. `top -H` shows one thread at 100% CPU while `/proc/<pid>/io` `read_bytes` stays flat — the scan is CPU-bound on the in-memory block index and never touches disk. It does eventually finish; upgrading to v9.26.5 is the fix.

### Pruning is supported again

v9.26.3 required a full, txindexed node; v9.26.4 lifts that. Because every DGB node is currently full, `coin-upgrade.sh` makes a **one-time offer** during the upgrade:

- **Keep it full** — decline the prompt. Nothing changes beyond the binary swap.
- **Switch to pruned** — accept, and it edits `digibyte.conf` in place (sets `prune=5000` ≈ 5 GB, removes `txindex`) after backing it up, then starts the node, which **prunes in place with no resync**. Reverting to full later requires a resync.

> **⚠ One-time mining interruption when switching a full node to pruned.** On its first start after pruning is enabled, the daemon runs a one-time prune of the existing block store — `getblockchaininfo` returns `error -28 "Pruning blockstore…"` and the RPC is unavailable. During that window the pool cannot serve DGB block templates, so **DGB miners' shares are rejected until it completes**. This is expected and self-clearing. **How long it takes varies significantly with your system** — chain size, disk speed (SSD vs HDD/network storage), and load — ranging from several minutes to an hour or more for a full DGB node; there is no fixed duration. Watch for completion with:
>
> ```bash
> digibyte-cli getblockchaininfo    # error -28 while pruning; "pruned": true when done
> ```
>
> After this first pass, ongoing pruning is gradual and in the background — mining and all pool functions run normally.

New installs: `install.sh` configures DGB from the pool-wide pruning choice (pruned → `prune=5000`, no `txindex`; full → `txindex=1`, `prune=0`), and `spiralctl coin prune DGB` can enable pruning at any time.

> **DigiDollar mining** is now included: the pool requests the `digidollar-oracle` GBT rule and copies `default_oracle_commitment` into the coinbase when the node provides one. It is **self-gating** — before DigiDollar activates (BIP9) the node returns no commitment, so the pool mines normal DGB blocks and there is **no operator action** required for DigiDollar. (Pending end-to-end validation on testnet26 ahead of mainnet activation.)

---

## What's new in v2.7.0

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **BTC now runs Bitcoin Core, not Bitcoin Knots.** This is the reason for the release and it is not optional — see the BTC node upgrade section at the top of this guide. Every path that acquires a Bitcoin daemon (installer, `coin-upgrade.sh`, Docker image, `pool-mode.sh`) is pinned to Core 31.1 with a checksum committed in this repository, and both "resolve the latest version" lookups are deleted.
- **The pool refuses to mine BTC on a chain it cannot verify.** At stratum startup, after the sync gate and before any miner connects, it checks BIP-110 enforcement and block 961,632 against the majority chain. `unknown` (daemon unreachable, or still syncing below the split height) is a distinct verdict from `minority` and never reported as wrong-chain, but both block mining. Set `allow_nonmajority_chain: true` per coin to override — but note it also disables the stale-tip refusal.
- **Sentinel alerts and a dashboard verdict row.** The alert bypasses quiet hours and distinguishes a wrong chain (red) from a stalled tip on the correct chain (amber), which have different remedies. Disable with `chain_identity_enabled: false`.
- **Several config-handling bugs fixed that could break a daemon or delete block data.** These are independent of the chain work and affect all coins: a config parser that could emit an unparseable line, silently disable the RAM cap, or promote a `[test]`-scoped `prune` onto mainnet. Nothing is required of you — but if you keep a hand-edited `bitcoin.conf` with `[main]`/`[test]` sections, it is now read and written correctly.

**If you have a `maxtipage` line in `bitcoin.conf`, remove it** — see "Before you resume mining" above.

No database migrations, no config format changes. Drop-in upgrade from v2.6.7 for the pool stack; the BTC **node** upgrade is a separate, required step.

---

## What's new in v2.6.7

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **Pepecoin's genesis hash was from a different chain, so a PEP pool could not start at all.** The startup genesis check failed unconditionally with "CRITICAL: Genesis block mismatch - WRONG CHAIN!" against genuine Pepecoin Core. If you run PEP, it now comes up; no action beyond upgrading.
- **NMMiner and LeafMiner were not recognised and got the config difficulty floor instead of a lottery profile.** An unrecognised user-agent falls back to the operator's configured difficulty — 1024 on the multi-coin smart port. For an ESP32 at ~100 KH/s that works out to `1024 × 2³² / 100000` seconds per share, or **509 days**, so the miner subscribed, authorized, received jobs, and then sat there producing nothing — indistinguishable from getting no work at all. Both now classify as lottery and receive difficulty 0.001 (about 43 seconds per share) with a job immediately. If you have an NMMiner that looked dead, just reconnect it. **No other miner is affected:** the new patterns are matched after all the ASIC and BitAxe patterns, and the ESP32 miners that were already recognised (NerdMiner V2, ESP32 Miner, SparkMiner, BitMaker, Arduino) were already getting the lottery profile and are unchanged.

No database migrations, no config format changes. Drop-in upgrade from v2.6.6.

## What's new in v2.6.6

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **`/api/pools` reported network hashrate 8x high for DigiByte.** The block-time lookup was fed a coin *name* where it expected a ticker, so every coin silently fell back to Bitcoin's 600s instead of DGB's 75s per algorithm. Because the value is computed server-side, the dashboard, `spiralctl stats` and Sentinel all inherited it — a pool reading `0.61%` of the network was really at `0.076%`. Nothing about mining changed; only the reported figure was wrong. Verify after upgrading with `curl -s localhost:4000/api/pools`: `networkDifficulty × 2³² ÷ networkHashrate` must equal **75**, not 600.
- **`spiralctl stats blocks` failed with `SyntaxError: invalid character '─'` on every install.** One line of the renderer used double quotes inside a double-quoted `python3 -c "…"`, so the shell consumed them and Python received a bare box-drawing character. Distinct from the v2.6.5 locale fix, which could not reach it.
- **BIP22 `inconclusive` block rejections are retried instead of abandoned.** The daemon returns it when it could not determine validity — not that the block is invalid — and its own guidance is to resubmit. It was classified permanent, which broke the retry loop and orphaned recoverable blocks.
- **`spiralctl config get` / `show` no longer invent values when they cannot read the config.** Without `sudo` they printed defaults indistinguishable from real settings — `get missing_payout_days` answered `7` immediately after a `sudo set` wrote `10`.
- **New: `spiralctl config set missing_payout_days <days>`.** Raise it if a healthy small pool trips the missing-payout alert on normal variance.
- **The DGB `job_rebroadcast` migration now reaches installs that upgraded to v2.6.5 early.** Its version gate skipped anything already at 2.6.5, permanently stranding pools upgraded in the window before the migration shipped.

No database migrations, no config format changes. Drop-in upgrade from v2.6.5.

## What's new in v2.6.5

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **`/api/pools` now reports the version you are actually running.** It previously returned a hard-coded `2.4.2-PHI_HASH_REACTOR` on every build, which made a current pool look years out of date. No behaviour change beyond the reported string — but if you diagnosed a "failed upgrade" from this field, re-check with `spiralpool --version`.
- **Dashboard-triggered upgrades no longer print a wallet-backup prompt or raw terminal colour codes.** Cosmetic on the surface, but it also removes a path where `upgrade.sh --auto` could stall on a prompt until the dashboard's 5-minute timeout.

No database migrations, no config format changes. Drop-in upgrade from v2.6.3.

## What's new in v2.6.3

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **DigiByte Core 9.26.4 → 9.26.5** — fixes the DigiDollar oracle startup scan that held node init (and DGB block templates) for 15+ minutes on every restart. No consensus change, no reindex, no config changes. See the DigiByte node-upgrade section above.

## What's new in v2.6.2

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **DigiByte Core 9.26.3 → 9.26.4** — makes DigiDollar compatible with **pruning** (reversing the v9.26.3 full-node requirement) and adds one narrowly-scoped DigiDollar consensus rule. In-place binary swap, no reindex. DGB rejoins the pool-wide prune toggle, and `coin-upgrade.sh` offers a one-time switch to a pruned node. See the DigiByte node-upgrade section above.

## What's new in v2.6.1

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **Per-alert mute (`spiralctl alerts`)** — turn any individual Sentinel alert or scheduled report on or off (`spiralctl alerts` for the interactive menu, or `disable <type>`/`enable <type>`), backed by a new `disabled_alerts` list in the Sentinel config. Works for every alert type, not just those with a dedicated flag. Drop-in from v2.6.0 — existing configs gain it automatically.

## What was new in v2.6.0

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **DigiByte Core 8.26.2 → 9.26.3** — a mandatory network-consensus upgrade (Groestl enforcement at mainnet block 23,808,000; `txindex=1` now required, so pruning is no longer supported for DGB). See the DigiByte node-upgrade section above for the operator procedure.
- **Codename** — the release line advances from *Phi Hash Reactor* to *Spiral Citadel*.

## What was new in v2.5.3

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **Sentinel alerting-reliability audit** — fixes a false "Zombie state" chronic alert (a healthy miner whose self-reported hardware-reject rate spiked while the pool was accepting its shares) and hardens ~18 alert paths against false positives: pool-side cross-references for zombie/rejection/offline detection; confirmation/debounce windows for fan, hashboard, temperature, worker-count, replica, coin-node, and HA VIP/state alerts; counter-reset safety for Prometheus-derived alerts; and a chronic-issue count throttle. Covered by `tests/test_alert_debounce.py`.

## What was new in v2.5.2

See [CHANGELOG.md](../../CHANGELOG.md) for the full list. Key changes:

- **BCH2 (Bitcoin Cash II) and BTCS (Bitcoin Silver)** — two new SHA-256d coins; pool now supports 17 coins total (including XEC added in this release)
- **Smart Port DIFFICULTY routing mode** — multi-coin smart port can now route to the coin with the lowest live network difficulty
- **Debian 13 "Trixie" support** — bare metal installs now supported on Debian 13 alongside Ubuntu 24.04/26.04 LTS
- **Ubuntu 26.04 LTS (Resolute Raccoon) support** — all Dockerfiles updated to `ubuntu:26.04`
- **Block history table** — last 5,000 blocks shown in a collapsible table on the Blocks tab with effort %, status badges, and explorer links
- **True pool effort calculation** — effort computed at block-find time as `(roundDiff / networkDiff) × 100` and stored in the DB
- **Per-coin accepted/rejected shares** — dashboard displays accept/reject rate per coin
- **Per-coin best share difficulty** — session and all-time best share diff tracked per coin
- **Multi-Coin Rotation widget** — visual 24h timeline bar with live status, next switch, and schedule table
- **Network difficulty in Est. Time to Block** — ETB card now shows current network difficulty

## What was new in v2.4.3

- **Docker multi-coin support** — `POOL_MODE=multi` runs all coins in one deployment
- **Docker merge mining** — SHA-256d and Scrypt AuxPoW pairs in Docker
- **Docker Stratum V2** — Noise Protocol encryption via `STRATUM_V2_ENABLED=true`
- **Dashboard statistics chart grid** — 2×2 charts for pool hashrate, network hashrate, difficulty, workers
- **`_safe_num()` across all miner fetch functions** — prevents dashboard crash from firmware returning string-encoded numbers
- **Spiral Router cleanup** — 47 verified patterns (down from ~280 dead-code patterns), all confirmed against firmware source

## What was new in v1.1.0

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

## Go code changes — compatibility analysis (v1.0.0 → v1.1.0)

The v1.0.0 → v1.1.0 changes are listed below. **None require a reinstall, OS change, config change, or manual migration.** The v1.1.x → v2.7.0 changes are also fully backward-compatible — no new database migrations, no config format changes.

| Component | Change | Impact on existing installs |
|-----------|--------|-----------------------------|
| `pool.go` — `getAlgoBlockTime()` | moved from 600s bucket to correct 150s bucket | Only affects vardiff. All other coins (BTC, LTC, DGB, etc.) unchanged. |
| `api/server.go` — `POST /api/admin/kick` | New endpoint to disconnect miner stratum sessions by IP; requires `X-API-Key` header | New feature. No breaking changes to existing endpoints or clients. |
| `SpiralSentinel.py` | added to all lookup tables; `update_available` and `missing_payout` alert dedup fixed | Discord notifications now reliably deliver after quiet-hours suppression. Behavioral only. |
| `database/migrate.go` | No new migrations in v1.1.0 | Existing schema (migrations 1–10) carried forward unchanged. |
| Version strings | `1.0.0 / BLACKICE` → `2.0.0 / PHI_HASH_REACTOR` throughout | Cosmetic. |

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
2. Backs up all critical files to `/spiralpool/backups/pre-upgrade-OLDVER-to-NEWVER-TIMESTAMP/`
3. Enables maintenance mode (suppresses Discord alerts during upgrade)
4. Stops services gracefully
5. Downloads the new release from GitHub, builds the new stratum binary
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

#### Standalone SHA-256d coins (BTC, BCH, BCH2, BC2, BTCS, DGB, XEC)

These run independently with no parent chain.

**1. Add a stanza to `/spiralpool/config/config.yaml`:**

```yaml
coins:
 - symbol: # or BTC, BCH, BCH2, BC2, BTCS, DGB
 name: ""
    algorithm: "sha256d"
    address: ""                          # fill in step 2
    nodes:
      - host: "127.0.0.1"
        port: 9999                       # coin's RPC port
        user: "rpcuser"
        password: "rpcpassword"
        zmq:
          endpoint: "tcp://127.0.0.1:29999"   # coin's ZMQ port
    stratum:
      port: 19333                        # stratum V1 port
      port_v2: 19334                     # stratum V2 port (optional)
      tls_port: 19335                    # TLS port (optional)
```

**2. Create a wallet address** (for coins with CLI support):

```bash
spiralpool-wallet --coin # also works for BTC, BCH, BCH2, BC2, BTCS, DGB, NMC, SYS, XMY, FBTC, LTC, DOGE, PEP, CAT
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

The version line should show `2.7.0`. If Sentinel is running:

```bash
sudo journalctl -u spiralsentinel -n 20
```

Look for `Spiral Pool v2.7.0` followed by `Spiral Citadel` in the startup log.

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

*Spiral Pool — Spiral Citadel 2.7.0 — Built on what came before. Growing toward phi.*
