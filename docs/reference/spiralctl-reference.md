# spiralctl - Spiral Pool Control Utility

## NAME

**spiralctl** - unified command-line interface for Spiral Pool management

## SYNOPSIS

```
spiralctl <command> [subcommand] [options]
```

## DESCRIPTION

**spiralctl** is the single entry point for managing all aspects of a Spiral Pool
installation. It delegates to the underlying `spiralpool-*` scripts and Go binaries
while presenting a consistent, discoverable interface.

The legacy `spiralpool-*` commands remain functional but are no longer advertised.
All documentation and MOTD references use `spiralctl` exclusively.

## COMMANDS

### Operations

| Command | Description |
|---|---|
| `spiralctl status` | Show full service and blockchain node status |
| `spiralctl restart` | Restart all Spiral Pool services (requires root) |
| `spiralctl shutdown [--reboot]` | Gracefully stop all services and power off (or reboot) |
| `spiralctl logs` | View stratum log output |
| `spiralctl watch` | Live monitoring dashboard (like htop for the pool) |
| `spiralctl test` | Run diagnostic and connectivity tests |
| `spiralctl update` | Check for Spiral Pool updates |
| `spiralctl maintenance` | Enter or leave maintenance mode |
| `spiralctl pause [minutes]` | Pause Sentinel alerts temporarily |

### Blockchain

| Command | Description |
|---|---|
| `spiralctl sync [options]` | Show blockchain sync progress |
| `spiralctl chain export` | Push blockchain data to a remote machine (requires root) |
| `spiralctl chain restore` | Pull blockchain data from a remote machine (requires root) |
| `spiralctl node <action> [coin]` | Manage blockchain node daemons |
| `spiralctl coin <action> [coin]` | Show, list, or disable cryptocurrency support |

### Mining

| Command | Description |
|---|---|
| `spiralctl mining [action] [options]` | Mining mode management (Go binary) |
| `spiralctl v2 pubkey` | Print the Stratum V2 authority public key for miners and proxies (Go binary) |
| `spiralctl v2 keygen [--rotate]` | Create or replace the Stratum V2 keys (Go binary) |
| `spiralctl pool stats` | Pool hashrate and worker statistics (Go binary) |
| `spiralctl stats [blocks [N]]` | Quick pool stats; `stats blocks` shows last N blocks |
| `spiralctl scan` | Scan network for miners |
| `spiralctl wallet [options]` | Show or generate wallet addresses |
| `spiralctl external [action]` | External access / hashrate rental (Go binary) |

### Miner Management

| Command | Description |
|---|---|
| `spiralctl miners` | List connected miners with hashrate and shares |
| `spiralctl miners kick <IP>` | Disconnect all stratum sessions from an IP |
| `spiralctl workers` | Per-worker breakdown (miner → rig → hashrate + acceptance rate) |
| `spiralctl miner nick <IP> <name>` | Set a display name for a miner in Sentinel |
| `spiralctl miner nick list` | List all configured miner nicknames |
| `spiralctl miner nick clear <IP>` | Remove a miner nickname |
| `spiralctl miner group [list \| <IP> <name> \| clear <IP>]` | Manage fleet groups (same store as the dashboard; drives the group offline/online alerts) |
| `spiralctl miner tag [list \| <IP> <t1,t2> \| clear <IP>]` | Manage fleet tags (same store as the dashboard) |
| `spiralctl miner control <IP> <action>` | Send one command (`info`, `sleep`, `wake`, `restart`, `power`) to a miner, as automation would |

### Data

| Command | Description |
|---|---|
| `spiralctl data backup` | Backup pool data and configuration (requires root) |
| `spiralctl data restore` | Restore pool data from backup (requires root) |
| `spiralctl data export` | Export mining history to CSV |
| `spiralctl gdpr-delete` | Delete miner data for GDPR/CCPA compliance (Go binary) |

### Coin Management

| Command | Description |
|---|---|
| `spiralctl coin enable <TICKER>` | Add a supported coin (installs daemon, generates wallet, updates config) |
| `spiralctl coin disable <TICKER>` | Stop and disable a coin daemon |
| `spiralctl coin status` | Show all coins and their enabled/disabled state (`coin list` is an alias) |
| `spiralctl coin prune <TICKER>` | Enable blockchain pruning for a coin to reclaim disk (requires a full resync) |
| `spiralctl coin storage` | Measure each enabled chain on disk against its published size figure |
| `spiralctl coin-upgrade` | In-place coin daemon binary upgrade (config and data preserved) |
| `spiralctl add-coin <TICKER>` | Add a custom/unsupported coin from GitHub (advanced) |
| `spiralctl remove-coin <TICKER>` | Remove a custom coin's generated files (wallet and blockchain data preserved) |

### High Availability / Failover

| Command | Description |
|---|---|
| `spiralctl ha [action]` | High Availability cluster management |
| `spiralctl ha vip [action]` | Virtual IP for miner failover |
| `spiralctl sync-addresses` | Sync wallet addresses across HA nodes |

### Configuration

| Command | Description |
|---|---|
| `spiralctl config [action] [key] [value]` | View or update Sentinel configuration |
| `spiralctl config validate` | Dry-run config check — YAML/JSON syntax, placeholder detection, key cross-checks |
| `spiralctl config notify-test` | Send a test notification to every configured channel |
| `spiralctl config list-cooldowns` | Show active alert cooldowns with time remaining |
| `spiralctl alerts [action] [alert_type]` | Turn individual alerts & reports on or off |
| `spiralctl log [errors] [service] [window]` | Filter service logs for errors/warnings |
| `spiralctl webhook [action]` | Manage Discord & Telegram notifications |

### Security

| Command | Description |
|---|---|
| `spiralctl security [period]` | Security status overview (default: 24h) |
| `spiralctl security fail2ban [action]` | Manage fail2ban jails |
| `spiralctl security tor [action]` | Manage Tor privacy settings |

---

## COMMAND REFERENCE

### spiralctl status

Show a comprehensive overview of all services, blockchain nodes, HA state, and miner connection info.

```
spiralctl status
```

No options. Runs without root.

---

### spiralctl restart

Restart all Spiral Pool services (stratum, sentinel, dashboard, daemons).

```
sudo spiralctl restart
```

Requires root.

---

### spiralctl shutdown

Gracefully stop all Spiral Pool services in the correct order, then power off or reboot the machine.

```
sudo spiralctl shutdown              # Stop services, then power off
sudo spiralctl shutdown --reboot     # Stop services, then reboot
sudo spiralctl shutdown --yes        # Skip confirmation prompt
sudo spiralctl shutdown --reboot --yes
```

**Stop order:**
1. `spiralstratum` — drops miner connections cleanly
2. `spiralsentinel` — flushes monitoring state
3. `spiraldash` — dashboard
4. `keepalived` — releases the VIP (HA nodes only)
5. `patroni` — flushes PostgreSQL WAL
6. `etcd` — HA consensus (HA nodes only)

Requires root. Prompts for confirmation unless `--yes` / `-y` is passed.

**Options:**
- `--reboot`, `-r` — reboot instead of power off
- `--yes`, `-y` — skip confirmation prompt

---

### spiralctl logs

View stratum log output.

```
spiralctl logs
```

Delegates to `spiralpool-logs`.

---

### spiralctl watch

Live monitoring dashboard with real-time stats.

```
spiralctl watch
```

Delegates to `spiralpool-watch`. Press `q` to quit.

---

### spiralctl test

Run diagnostic and connectivity tests for all pool components.

```
spiralctl test
```

Delegates to `spiralpool-test`.

---

### spiralctl update

Check for available Spiral Pool updates.

```
spiralctl update
```

Delegates to `spiralpool-update`.

---

### spiralctl maintenance

Enter or leave maintenance mode.

```
spiralctl maintenance
```

Delegates to `spiralpool-maintenance`.

---

### spiralctl pause

Pause Sentinel alerts temporarily.

```
spiralctl pause [minutes]
```

**Arguments:**
- `minutes` - Duration to pause alerts (default varies by script)

---

### spiralctl sync

Show blockchain sync progress for all enabled coins.

```
spiralctl sync [--watch|-w] [--coin <coin>]
```

**Options:**
- `--watch`, `-w` - Live updating display
- `--coin`, `-c` - Show specific coin only

**Examples:**
```
spiralctl sync                    # One-shot sync status
spiralctl sync --watch            # Live sync progress
spiralctl sync --coin btc         # BTC sync only
```

---

### spiralctl chain

Transfer blockchain data between machines via rsync over SSH. Each command is a
**complete, self-contained operation** — daemons are stopped on both sides during
transfer, ownership is fixed, and daemons are restarted. You only need ONE command,
not both.

```
sudo spiralctl chain export       # Push FROM this machine TO a remote one
sudo spiralctl chain restore      # Pull FROM a remote machine TO this one
```

**Pick one based on where you're sitting:**
- On the **synced machine** → run `export` (push)
- On the **new machine** → run `restore` (pull)

Both subcommands require root and launch an interactive wizard.

**Coins are transferred one at a time.** If you select multiple coins, each goes
through the full cycle sequentially before moving to the next:

1. Remote coin daemon is stopped via SSH (ensures data consistency)
2. Local coin daemon is stopped
3. Data is transferred via rsync (same `/spiralpool/<coin>/` path on both sides)
4. Ownership is fixed (`chown spiraluser:spiraluser`)
5. Both daemons restarted for that coin
6. Repeat for next selected coin

**SSH user:** Defaults to your current admin username (detected via `$SUDO_USER`).
This must be the account you use to SSH into the remote machine — **not** `spiraluser`.
The remote user needs passwordless sudo for daemon stop/start; if unavailable, the
script warns and proceeds (you can stop the remote daemon manually).

**Examples:**
```
# Sitting on the synced machine, push BTC data to a new server:
sudo spiralctl chain export

# Sitting on the new machine, pull BTC data from the synced server:
sudo spiralctl chain restore
```

---

### spiralctl node

Manage blockchain node daemons.

```
spiralctl node [status|start|stop|restart] [coin|all]
```

**Actions:**
- `status` - Show daemon status (default)
- `start` - Start daemon(s) (requires root)
- `stop` - Stop daemon(s) (requires root)
- `restart` - Restart daemon(s) (requires root)

**Coin values:** `bc2`, `bch`, `bch2`, `btc`, `btcs`, `cat`, `dgb`, `dgb-scrypt`, `doge`, `fbtc`, `ltc`, `nmc`, `pep`, `sys`, `xec`, `xmy`, `all`

**Note:** DGB-SCRYPT shares the DigiByte daemon with DGB. Stopping/restarting DGB-SCRYPT alone is not supported.

**Examples:**
```
spiralctl node status             # All daemon statuses
sudo spiralctl node restart btc   # Restart Bitcoin daemon
sudo spiralctl node stop all      # Stop all daemons
```

---

### spiralctl coin

Show or disable cryptocurrency support.

```
spiralctl coin [status|list|disable] [coin]
```

**Actions:**
- `status` / `list` - Show all coins and their state (default)
- `disable` - Disable a coin's daemon (requires root)

**Examples:**
```
spiralctl coin status
sudo spiralctl coin disable bch
```

---

### spiralctl mining

Mining mode management. Delegates to the Go spiralctl binary.

```
spiralctl mining [status|solo|multi|merge] [options]
```

**Actions:**
- `status` - Show current mining mode
- `solo <coin>` - Switch to single-coin solo mining
- `multi <coin,coin,...>` - Switch to multi-coin mining
- `merge enable [chains]` - Enable merge mining
- `merge disable` - Disable merge mining
- `payout` - Show where found blocks pay (configured wallet, or the miner's worker name)
- `payout wallet` - Pay every block to the configured wallet (default)
- `payout worker` - Pay each miner at the address in its own worker name, after an on-screen disclosure. Single-operator use only: you must own every wallet and rig, and the stratum port must not be reachable from outside your network. Parent chain only — merge-mined auxiliary chains always pay their configured address

**Examples:**
```
spiralctl mining status
spiralctl mining solo dgb
spiralctl mining multi btc,bch,dgb
spiralctl mining merge enable
spiralctl mining merge disable
spiralctl mining payout
spiralctl mining payout wallet
spiralctl mining payout worker
```

---

### spiralctl v2

Stratum V2 key management. Delegates to the Go spiralctl binary. Requires root.

Stratum V2 miners and proxies authenticate the pool with its **authority public key**, so the operator has to read that key off the pool and configure it on each client. The pool creates the keys itself the first time a V2 port starts; `keygen` is only needed to create them ahead of time or to replace them.

```
spiralctl v2 pubkey              # print the authority public key
spiralctl v2 keygen              # create the keys if they are missing
spiralctl v2 keygen --rotate     # replace the keys
```

| Option | Description |
|---|---|
| `--dir DIR` | Directory holding `authority.key` and `static.key`. Defaults to `stratum-v2/` next to `config.yaml`; `global.stratum_v2_key_dir` overrides it in the config. |
| `--rotate` | `keygen` only. Moves the existing keys aside as `.bak-<UTC timestamp>` before generating new ones. |

`pubkey` prints two lines — the hex form first, then the base58 form that Stratum Reference Implementation configs take (the translator's `authority_pubkey`, for example):

```
8a3f...c91d
9auMaAz1zPN...
```

If no key exists yet, `pubkey` says so and names the directory rather than creating one silently.

**`--rotate` disconnects every V2 client.** The old keys are kept as `.bak-<timestamp>` files, but each miner and proxy must be given the new authority key before it can connect again. Restart the pool afterwards:

```
sudo systemctl restart spiralstratum
```

Keys created by root are chowned to match the owner of the config directory, so the pool user can still read them.

---

### spiralctl pool

Pool-level commands. Delegates to the Go spiralctl binary.

```
spiralctl pool stats
```

---

### spiralctl stats

Quick pool statistics (hashrate, blocks found). The `blocks` subcommand shows recent block history.

```
spiralctl stats
spiralctl stats blocks [count]
```

**Subcommands:**
- `blocks [count]` - Show last N blocks (default: 5), status: pending / confirmed / orphaned

**Backward-compat alias:** `spiralctl blocks [count]` still works.

Delegates to `spiralpool-stats` / `spiralpool-blocks`.

---

### spiralctl scan

Scan the local network for mining hardware.

```
spiralctl scan
```

Delegates to `spiralpool-scan`.

---

### spiralctl wallet

Show or generate wallet addresses.

```
spiralctl wallet [--coin <coin>] [--auto]
```

**Options:**
- `--coin <coin>` - Specific coin (bc2, bch, bch2, btc, btcs, cat, dgb, dgb-scrypt, doge, fbtc, ltc, nmc, pep, sys, xec, xmy)
- `--auto` - Auto-generate wallet address if none exists

**Examples:**
```
spiralctl wallet                  # Auto-detect and show
spiralctl wallet --coin btc       # Show BTC address
spiralctl wallet --coin dgb --auto
```

---

### spiralctl external

External access and hashrate rental. Delegates to the Go spiralctl binary.

```
spiralctl external [setup|enable|disable|status|test]
```

---

### spiralctl data

Manage pool data: backups, restores, and CSV exports.

```
sudo spiralctl data backup    # Backup pool data and configuration
sudo spiralctl data restore   # Restore pool data from backup
spiralctl data export         # Export mining history to CSV
```

**Subcommands:**
- `backup` - Full backup of pool config and data (requires root). Delegates to `spiralpool-backup`.
- `restore` - Restore from a previous backup (requires root). Delegates to `spiralpool-restore`.
- `export` - Export mining history to CSV. Delegates to `spiralpool-export`.

**Backward-compat aliases:** `spiralctl backup`, `spiralctl restore`, and `spiralctl export` still work.

---

### spiralctl gdpr-delete

Delete miner data for GDPR/CCPA compliance. Delegates to the Go spiralctl binary.

```
spiralctl gdpr-delete
```

---

### spiralctl ha

High Availability cluster management.

```
spiralctl ha [status|enable|disable|credentials|setup|failback|promote|validate|service|vip]
```

**Actions:**
- `status` - Show HA cluster status (default)
- `enable [options]` - Enable HA on this node (requires root)
- `disable [--yes|-y]` - Disable HA on this node (requires root)
- `promote` - Promote this node to primary (requires root)
- `failback` - Rejoin cluster after failover (requires root)
- `credentials` - Show HA cluster credentials (requires root). Also accepts `creds` and `info` as aliases.
- `setup` - Run HA setup wizard
- `validate` - Validate HA configuration
- `service` - Manage HA services
- `vip [status|enable|disable|failover]` - Virtual IP for miner failover (see below)

**Enable options:**
- `--vip <ip>` (or `--address <ip>`) - Virtual IP address (required)
- `--interface <name>` - Network interface (auto-detected if omitted)
- `--priority <num>` - Priority: 100 = primary, 101+ = backup
- `--token <token>` - Cluster token (generated if omitted)
- `--netmask <cidr>` - CIDR netmask for VIP (default: 32)
- `--primary-ip <ip>` - IP of existing primary node (backup setup)
- `--repl-password <pw>` - PostgreSQL replication password
- `--superuser-password <pw>` - PostgreSQL superuser password
- `--db-password <pw>` - Stratum database password
- `--ssh-password <pw>` - SSH password for spiraluser
- `--force` - Skip confirmation prompts

**Examples:**
```
spiralctl ha status
sudo spiralctl ha enable --vip 192.168.1.100
sudo spiralctl ha enable --vip 192.168.1.100 --token <token> --priority 101
sudo spiralctl ha promote
spiralctl ha vip status
sudo spiralctl ha vip enable --address 192.168.1.100
```

#### ha vip subcommand

Virtual IP management for miner failover (keepalived).

```
spiralctl ha vip [status|enable|disable|failover]
```

**Actions:**
- `status` - Show VIP / keepalived state (default)
- `enable [options]` - Enable VIP on this node (requires root)
- `disable` - Disable VIP on this node (requires root)
- `failover` - Display VIP failover instructions (does not move VIP directly; use `ha promote` instead)

**Enable options:**
- `--address <ip>` - Virtual IP address (required)
- `--interface <name>` - Network interface (auto-detected if omitted)
- `--netmask <num>` - CIDR netmask for VIP (default: 32, host-only route)
- `--priority <num>` - Priority: 100 = primary, 101+ = backup
- `--token <token>` - Cluster token (generated if omitted)

**Backward-compat alias:** `spiralctl vip [action]` still works.

---

### spiralctl sync-addresses

Sync wallet addresses across HA cluster nodes. Queries the HA status API and ensures wallet address configuration is consistent across all peers.

```
spiralctl sync-addresses [--apply] [--force] [--dry-run]
```

**Options:**
- `--apply` - Apply address changes to remote nodes
- `--force` - Force sync even if addresses match
- `--dry-run` - Show what would change without applying

Requires HA to be enabled. Uses the HA status API on localhost:5354.

---

### spiralctl config

View or update Sentinel configuration.

```
spiralctl config [show|list|get|set] [key] [value]
```

**Actions:**
- `show` / `list` - Show current Sentinel configuration
- `get <key>` - Get a specific config value
- `set <key> <value>` - Set a config value
- `validate` - Dry-run check of `config.yaml` and sentinel `config.json`: YAML/JSON syntax, placeholder wallet addresses (absent `wallet_address` is valid — only explicit placeholders like `YOUR_DGB_ADDRESS` are flagged), admin API key cross-check (accepts both v2 `admin_api_key` and v1 `adminApiKey` formats), Telegram/XMPP completeness, SMTP completeness (including password), v2.0.0 alert config range checks (disk_warn_pct < disk_critical_pct, dry_streak_multiplier ≥ 1, difficulty_alert_threshold_pct 1–100, backup_stale_days ≥ 1), `scheduled_maintenance_windows` format (HH:MM start/end, valid days 0–6). Skips Sentinel config check with an informational note when `spiralsentinel.service` is not enabled.
- `notify-test` - Send a test message to every configured notification channel (Discord, Telegram, ntfy, email, XMPP). Reports pass/fail per channel.
- `list-cooldowns` - Show all active Sentinel alert cooldowns with time remaining (queries the health endpoint)

**Keys:**
- `expected_hashrate` - Expected fleet hashrate in TH/s
- `discord_webhook` - Discord webhook URL
- `telegram_token` - Telegram bot token
- `telegram_chat_id` - Telegram chat ID
- `missing_payout_days` - Grace days before an unpaid found block alerts (default 7)
- `missing_payout_max_days` - Backstop: alert after N days regardless (0 = off, default 0)
- `simpleswap` - `on`/`off` for the SimpleSwap link inside `sats_surge` alerts (default `on`). Off keeps the surge alert and drops only the link; a config file without the key reads as `on`.

**Examples:**
```
spiralctl config show
spiralctl config get expected_hashrate
spiralctl config set expected_hashrate 50
spiralctl config set discord_webhook https://discord.com/api/webhooks/...
spiralctl config set simpleswap off
spiralctl config validate
spiralctl config notify-test
spiralctl config list-cooldowns
```

---

### spiralctl alerts

Turn individual Sentinel alerts and periodic reports on or off. This is a hard
mute — a disabled alert is dropped at the single `send_alert()` gate that every
notification passes through, so it silences the native, Prometheus (`infra_*`),
and Go-bridged pool (`pool_*`) variants of that alert together.

```
spiralctl alerts [menu|list|disable|enable|reset] [alert_type]
```

**Actions:**
- `menu` (default when run with no arguments on a terminal) - Interactive toggle menu: every alert/report is numbered and shown with its on/off state; type a number (or a name) to flip it, `r` to reset all, `s` to save & restart Sentinel, `q` to quit without saving. Edits are held in memory and written once on save.
- `list` - Show every alert and report grouped by domain, each marked `on` or `DISABLED` (non-interactive; also the default when stdout is not a terminal). Also accepts `show` and `status` as aliases.
- `disable <alert_type>` - Stop sending a specific alert or report
- `enable <alert_type>` - Resume sending it
- `reset` - Clear all mutes (re-enable everything)

**Notes:**
- Reports are alert types: `6h_report`, `weekly_report`, `monthly_earnings`, `quarterly_report`, `special_date`, `maintenance_reminder`, `update_available`.
- `block_found` can never be disabled — the command rejects it.
- Unknown/mistyped names are rejected; run `spiralctl alerts list` for the valid set.
- Backed by the `disabled_alerts` list in the Sentinel `config.json`. Changes require
  a Sentinel restart (`sudo systemctl restart spiralsentinel`) to take effect.

**Examples:**
```
spiralctl alerts                      # interactive menu
spiralctl alerts list
spiralctl alerts disable difficulty_change
spiralctl alerts disable weekly_report
spiralctl alerts enable difficulty_change
spiralctl alerts reset
```

**Relationship to other silencing methods:** `alerts disable` is the uniform on/off
switch. For alerts you want to keep but tune, the per-feature `*_enabled` flags and
`alert_cooldowns` in [SentinelConfig.md](SentinelConfig.md) still apply. `spiralctl pause`
mutes everything temporarily; `alerts disable` is per-type and persistent.

---

### spiralctl log

Filter service logs for errors and warnings.

```
spiralctl log errors [service] [window]
```

**Arguments:**
- `service` (optional) - Scope to one service. Aliases: `stratum`, `sentinel`, `dash`/`dashboard`, `patroni`/`postgres`/`pg`, `ha`/`watcher`
- `window` (optional) - Time window. Format: `<N><unit>` where unit is `s`, `m`, `h`, or `d`. Default: `1h`

Colour-codes output by severity: red for ERROR/CRITICAL/FATAL, yellow for WARN.

**Examples:**
```
spiralctl log errors                      # All services, last 1h
spiralctl log errors 24h                  # All services, last 24h
spiralctl log errors sentinel             # Sentinel only, last 1h
spiralctl log errors stratum 6h           # Stratum only, last 6h
spiralctl log errors patroni 7d           # Patroni only, last 7 days
```

---

### spiralctl miners

List connected miners and manage stratum sessions.

```
spiralctl miners
spiralctl miners kick <IP>
```

**Subcommands:**
- *(none)* - List all connected miners grouped by coin: wallet address, hashrate, shares/sec, total shares
- `kick <IP>` - Disconnect all stratum sessions from the given IP. The miner will reconnect automatically on its own timer.

`kick` requires `admin_api_key` to be set in `config.yaml`.

**Examples:**
```
spiralctl miners
spiralctl miners kick 192.168.1.50
```

---

### spiralctl workers

Show per-worker hashrate breakdown, grouped by coin and miner wallet.

```
spiralctl workers
```

Lists each worker name with current hashrate, acceptance rate, and online status. Useful for farms with multiple rigs per wallet address.

---

### spiralctl miner

Miner management subcommands.

```
spiralctl miner nick <IP> <name>     Set a display nickname for a miner
spiralctl miner nick list            List all configured nicknames
spiralctl miner nick clear <IP>      Remove a miner's nickname
```

Nicknames are stored in Sentinel's `config.json` and used in all alert messages and reports. Changes take effect after Sentinel is restarted.

**Groups and tags**

```
spiralctl miner group <IP> <group-name>   Assign a miner to a group
spiralctl miner group list                List groups and their members
spiralctl miner group clear <IP>          Remove a miner from its group
spiralctl miner tag <IP> <t1,t2,...>      Set a miner's tags (comma-separated)
spiralctl miner tag list                  List all tagged miners
spiralctl miner tag clear <IP>            Remove a miner's tags
```

These are the same fleet groups and tags the dashboard's Fleet page manages: they are
stored in `/spiralpool/dashboard/data/miner_groups.json` and `miner_tags.json`, so a group
set here shows up on the dashboard and feeds Sentinel's `group_offline` / `group_online`
alerts (Sentinel re-reads the file at most once a minute). A miner belongs to one group at a
time — assigning it to a new group removes it from the old one. Group names accept letters,
digits, spaces and `_ - . ( )` up to 64 characters, matching what the dashboard accepts.

**Sending one command to a miner**

```
spiralctl miner control <IP> info
spiralctl miner control <IP> sleep|wake|restart
spiralctl miner control <IP> power <low|normal|high|watts>
```

Runs the same driver and stored login Sentinel's automation uses, so it is the way to test
whether a miner accepts a command. `info` reports what the pool can do with that miner and
whether a login is stored. `OK` means the miner accepted the command, not that it took
effect. Requires root (it re-runs as the pool user) and needs
`/spiralpool/bin/miner_control.py`, which the installer deploys. A miner missing from
Sentinel's list needs `--type`; an Avalon whose model is not set on the Automation page
needs `--model nano3s`, `avalon_q` or `mini3`. See
[MINER_SUPPORT.md](MINER_SUPPORT.md) for what each firmware family supports.


**Examples:**
```
spiralctl miner nick 192.168.1.50 "Antminer S21"
spiralctl miner nick list
spiralctl miner nick clear 192.168.1.50
```

---

### spiralctl coin enable

Add a supported coin to the pool. Launches the installer in "Add coins to existing installation" mode, which handles the full setup: daemon installation, wallet generation, config.yaml update, firewall ports, and service restart.

```
spiralctl coin enable <TICKER>
```

**Supported coins:** BC2, BCH, BCH2, BTC, BTCS, CAT, DGB, DGB-SCRYPT, DOGE, FBTC, LTC, NMC, PEP, SYS, XEC, XMY

**Examples:**
```
spiralctl coin enable BTC       # Enable Bitcoin
spiralctl coin enable LTC       # Enable Litecoin
spiralctl coin enable NMC       # Enable Namecoin (merge-mine with BTC)
```

After enabling, visit the Dashboard at `http://<server>:1618/setup` to verify wallet addresses.

---

### spiralctl coin disable

Stop and disable a coin daemon. Wallet data and blockchain data are preserved.

```
spiralctl coin disable <TICKER>
```

---

### spiralctl coin storage

Report what each enabled chain actually occupies, next to the figure Spiral
Pool publishes for it.

```
spiralctl coin storage
```

Reads `size_on_disk` from each running daemon's `getblockchaininfo` and
compares it with `chain_gb` in `config/coins.manifest.yaml`, the single source
the installers and every storage table are checked against. A chain that has
outgrown its figure is flagged with the shortfall.

Why it exists: the published figures were badly wrong before v3.0.0 and no
amount of cross-checking between documents could have revealed it, because
every copy agreed with every other copy and all of them were stale — eCash was
listed at 20 GB against a real ~155 GB, Fractal Bitcoin at 10 GB against a
documented 2 TB. Chains only grow, so any static figure is a snapshot. The
daemons know their own size; this asks them.

- **Pruned nodes** are identified and excluded from the comparison — the
  published figure is a full-node size, and a pruned chain is capped near 5 GB
  regardless of it.
- **Stopped or unresponsive daemons** are reported as such rather than skipped,
  so a missing row never reads as a passing one.
- When a chain has genuinely outgrown its figure, correct `chain_gb` in the
  manifest. `tests/test_coin_storage_consistency.py` then requires both
  installers and all four documents to match, so it cannot be fixed in one
  place and left stale in five others.

---

### spiralctl coin prune

Enable blockchain pruning for a coin to save disk space. Sets `prune=5000` (5 GB) in the
daemon's config file and restarts it. All pool operations — mining, ZMQ, block submission —
work normally against a pruned node.

```
spiralctl coin prune <TICKER>
```

Run without a ticker to print the usage and savings summary. Requires root.

Approximate savings: BTC ~600 GB to 5 GB, BCH ~200 GB to 5 GB, LTC ~100 GB to 5 GB,
DGB ~80 GB to 5 GB.

DigiByte supports pruning as of DigiByte Core v9.26.4; DigiDollar runs pruned and `txindex`
is dropped automatically.

> **This requires a full resync** — the blockchain data is re-downloaded from scratch.

---

### spiralctl add-coin

Add a **custom** coin not natively supported by Spiral Pool. This is an advanced command for coins outside the 17 built-in tickers.

```
spiralctl add-coin <TICKER> --github <URL> [--algorithm sha256d|scrypt]
```

If a built-in ticker is provided, the command redirects to `spiralctl coin enable` instead.

---

### spiralctl remove-coin

Remove a custom coin's generated files (Go source, Dockerfile, manifest entry). Wallet data and blockchain data are **never deleted**.

```
spiralctl remove-coin <TICKER> [--yes]
```

---

### spiralctl coin-upgrade

Upgrade a coin daemon binary in-place. Config files, wallets, blockchain data, and pool settings are never modified.

```
spiralctl coin-upgrade [--coin <TICKER>] [--check] [--reindex]
```

**Options:**
- `--coin <TICKER>` - Target a specific coin
- `--check` - Show current vs target version without making changes
- `--reindex` - Start the daemon with `-reindex` after upgrade

**Risk classification shown before any change:**
- `PATCH` — Binary swap, reindex not expected
- `MINOR` — Reindex may be needed
- `MAJOR` — Reindex almost certainly required

---

### spiralctl webhook

Manage Discord and Telegram notification webhooks.

```
spiralctl webhook [status|set|clear|test]
```

**Actions:**
- `status` - Show webhook configuration
- `set discord <url>` - Configure Discord webhook
- `set telegram <token> <chat_id>` - Configure Telegram notifications
- `clear discord` - Remove Discord webhook
- `clear telegram` - Remove Telegram configuration
- `test` - Send test message to all configured endpoints

**Examples:**
```
spiralctl webhook status
spiralctl webhook set discord https://discord.com/api/webhooks/123/abc
spiralctl webhook set telegram 123456:ABCdef -12345678
spiralctl webhook test
```

---

### spiralctl security

Security status dashboard and management. Shows firewall state, active fail2ban bans, stratum security events, and connection fingerprints.

```
spiralctl security [period]
spiralctl security fail2ban [action]
spiralctl security tor [action]
```

**Top-level (status view):**
- `period` - Time window for event counts (default: `24h`). Accepts journald relative times: `1h`, `7d`, etc.

#### security fail2ban subcommand

Manage fail2ban jails for Spiral Pool services.

```
spiralctl security fail2ban [status|banned|unban|whitelist-add|whitelist-show|reload|logs]
```

**Actions:**
- `status` - Show all jail stats (default)
- `banned` - List currently banned IPs
- `unban <IP>` - Remove a ban (requires root)
- `whitelist-add <CIDR>` - Whitelist an IP/CIDR (requires root)
- `whitelist-show` - Show current whitelist
- `reload` - Reload fail2ban config (requires root)
- `logs` - Tail fail2ban log

**Backward-compat alias:** `spiralctl fail2ban [action]` still works.

**Examples:**
```
spiralctl security fail2ban banned
sudo spiralctl security fail2ban unban 1.2.3.4
sudo spiralctl security fail2ban whitelist-add 203.0.113.0/24
```

#### security tor subcommand

Manage Tor privacy settings for blockchain connections.

```
spiralctl security tor [status|enable|disable]
```

**Actions:**
- `status` - Show Tor status (default)
- `enable` - Enable Tor (requires re-running installer with `--tor`)
- `disable` - Disable Tor (requires re-running installer)

**Backward-compat alias:** `spiralctl tor [action]` still works.

---

### spiralctl help

Show the built-in help summary.

```
spiralctl help
```

---

### spiralctl version

Show full version table.

```
spiralctl version
```

Displays: spiralctl script version, stratum binary version (`spiralstratum --version`), Sentinel version, and all installed coin daemon versions.

---

## ENVIRONMENT VARIABLES

| Variable | Default | Description |
|---|---|---|
| `INSTALL_DIR` | `/spiralpool` | Spiral Pool installation directory |
| `POOL_USER` | `spiraluser` | System user that owns pool files |

## FILES

| Path | Description |
|---|---|
| `/usr/local/bin/spiralctl` | Main entry point (this script) |
| `/spiralpool/bin/spiralctl` | Go binary for mining/pool/external/gdpr commands |
| `/spiralpool/config/config.yaml` | Pool configuration (coins, ports, stratum) |
| `~<POOL_USER>/.spiralsentinel/config.json` | Sentinel configuration (webhooks, thresholds); home dir detected dynamically via `getent` |
| `/spiralpool/config/sentinel/config.json` | Fallback Sentinel configuration, used when the home-directory copy above is absent |
| `/spiralpool/config/etcd-auth.conf` | etcd root password (mode 640, root-owned); sourced to authenticate `etcdctl` calls |
| `/spiralpool/config/patroni-api.conf` | Patroni REST API credentials (mode 600, root-only) |
| `/spiralpool/data/miners.json` | Discovered miners database |
| `/spiralpool/scripts/blockchain-export.sh` | Blockchain export script |
| `/spiralpool/scripts/blockchain-restore.sh` | Blockchain restore script |
| `/usr/local/bin/spiralpool-*` | Legacy individual command scripts |

## EXIT CODES

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | General error or invalid usage |

## SUPPORTED COINS

**SHA-256d:** BC2, BCH, BCH2, BTC, BTCS, DGB, FBTC, NMC, SYS, XEC, XMY

**Scrypt:** CAT, DGB-SCRYPT, DOGE, LTC, PEP

**AuxPoW merge-mining pairs (6):** BTC+NMC, BTC+FBTC, BTC+SYS, BTC+XMY, LTC+DOGE, LTC+PEP

**Standalone SHA-256d (not merge-mineable):** BC2, BCH, BCH2, BTCS, DGB, XEC

## SEE ALSO

- Spiral Pool Dashboard: `http://<server>:1618`
- Spiral Pool API: `http://<server>:4000`
- Sentinel configuration: `spiralctl config show`
- HA setup guide: `spiralctl ha setup`
