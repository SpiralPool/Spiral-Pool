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
| `spiralctl coin status` | Show all coins and their enabled/disabled state |
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

**Coin values:** `bc2`, `bch`, `btc`, `cat`, `dgb`, `dgb-scrypt`, `doge`, `fbtc`, `ltc`, `nmc`, `pep`, `qbx`, `sys`, `xmy`, `all`

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

**Examples:**
```
spiralctl mining status
spiralctl mining solo dgb
spiralctl mining multi btc,bch,dgb
spiralctl mining merge enable
spiralctl mining merge disable
```

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
- `--coin <coin>` - Specific coin (dgb, dgb-scrypt, btc, bch, bc2, nmc, sys, xmy, fbtc, qbx, ltc, doge, pep, cat)
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
- `credentials` - Show HA cluster credentials (requires root)
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
- `validate` - Dry-run check of `config.yaml` and sentinel `config.json`: YAML/JSON syntax, placeholder wallet addresses (absent `wallet_address` is valid — only explicit placeholders like `YOUR_DGB_ADDRESS` are flagged), admin API key cross-check (accepts both v2 `admin_api_key` and v1 `adminApiKey` formats), Telegram/XMPP completeness, SMTP completeness (including password), v1.2.3 alert config range checks (disk_warn_pct < disk_critical_pct, dry_streak_multiplier ≥ 1, difficulty_alert_threshold_pct 1–100, backup_stale_days ≥ 1), `scheduled_maintenance_windows` format (HH:MM start/end, valid days 0–6). Skips Sentinel config check with an informational note when `spiralsentinel.service` is not enabled.
- `notify-test` - Send a test message to every configured notification channel (Discord, Telegram, ntfy, email, XMPP). Reports pass/fail per channel.
- `list-cooldowns` - Show all active Sentinel alert cooldowns with time remaining (queries the health endpoint)

**Keys:**
- `expected_hashrate` - Expected fleet hashrate in TH/s
- `discord_webhook` - Discord webhook URL
- `telegram_token` - Telegram bot token
- `telegram_chat_id` - Telegram chat ID

**Examples:**
```
spiralctl config show
spiralctl config get expected_hashrate
spiralctl config set expected_hashrate 50
spiralctl config set discord_webhook https://discord.com/api/webhooks/...
spiralctl config validate
spiralctl config notify-test
spiralctl config list-cooldowns
```

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

**Supported coins:** BC2, BCH, BTC, CAT, DGB, DGB-SCRYPT, DOGE, FBTC, LTC, NMC, PEP, QBX, SYS, XMY

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

### spiralctl add-coin

Add a **custom** coin not natively supported by Spiral Pool. This is an advanced command for coins outside the 14 built-in tickers.

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
| `/spiralpool/data/miners.json` | Discovered miners database |
| `/spiralpool/scripts/blockchain-export.sh` | Blockchain export script |
| `/spiralpool/scripts/blockchain-restore.sh` | Blockchain restore script |
| `/usr/local/bin/spiralpool-*` | Legacy individual command scripts |

## EXIT CODES

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | General error or invalid usage |
| `2` | Command not found / missing dependency |

## SUPPORTED COINS

**SHA-256d:** BC2, BCH, BTC, DGB, FBTC, NMC, QBX, SYS, XMY

**Scrypt:** CAT, DGB-SCRYPT, DOGE, LTC, PEP

**AuxPoW merge-mining pairs:** BTC+NMC, BTC+FBTC, BTC+SYS, BTC+XMY, DGB+NMC, DGB+SYS, DGB+XMY, DGB+FBTC, LTC+DOGE, LTC+PEP

**Standalone SHA-256d (not merge-mineable):** BC2, BCH, QBX

## SEE ALSO

- Spiral Pool Dashboard: `http://<server>:1618`
- Spiral Pool API: `http://<server>:4000`
- Sentinel configuration: `spiralctl config show`
- HA setup guide: `spiralctl ha setup`
