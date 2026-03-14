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
| `spiralctl coin <action> [coin]` | Show or disable cryptocurrency support |

### Mining

| Command | Description |
|---|---|
| `spiralctl mining [action] [options]` | Mining mode management (Go binary) |
| `spiralctl pool stats` | Pool hashrate and worker statistics (Go binary) |
| `spiralctl stats` | Quick pool stats (hashrate, blocks) |
| `spiralctl blocks [count]` | View discovered blocks and status (default: last 5) |
| `spiralctl scan` | Scan network for miners |
| `spiralctl wallet [options]` | Show or generate wallet addresses |
| `spiralctl external [action]` | External access / hashrate rental (Go binary) |

### Data

| Command | Description |
|---|---|
| `spiralctl backup` | Backup pool data and configuration (requires root) |
| `spiralctl restore` | Restore pool data from backup (requires root) |
| `spiralctl export` | Export mining history to CSV |
| `spiralctl gdpr-delete` | Delete miner data for GDPR/CCPA compliance (Go binary) |

### High Availability / Failover

| Command | Description |
|---|---|
| `spiralctl ha [action]` | High Availability cluster management |
| `spiralctl vip [action]` | Virtual IP for miner failover |
| `spiralctl sync-addresses` | Sync wallet addresses across HA nodes |

### Configuration

| Command | Description |
|---|---|
| `spiralctl config [action] [key] [value]` | View or update Sentinel configuration |
| `spiralctl webhook [action]` | Manage Discord & Telegram notifications |
| `spiralctl tor [action]` | Manage Tor privacy settings |

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

Quick pool statistics (hashrate, blocks found).

```
spiralctl stats
```

Delegates to `spiralpool-stats`.

---

### spiralctl blocks

View discovered blocks and their current status (pending, confirmed, orphaned).

```
spiralctl blocks [count]
```

**Arguments:**
- `count` - Number of recent blocks to show (default: 5)

Delegates to `spiralpool-blocks`.

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

### spiralctl backup

Backup pool data and configuration.

```
sudo spiralctl backup
```

Requires root. Delegates to `spiralpool-backup`.

---

### spiralctl restore

Restore pool data from a previous backup.

```
sudo spiralctl restore
```

Requires root. Delegates to `spiralpool-restore`.

---

### spiralctl export

Export mining history to CSV format.

```
spiralctl export
```

Delegates to `spiralpool-export`.

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
spiralctl ha [status|enable|disable|credentials|setup|failback|promote|validate|service]
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
```

---

### spiralctl vip

Virtual IP management for miner failover.

```
spiralctl vip [status|enable|disable|failover]
```

**Actions:**
- `status` - Show VIP cluster status (default)
- `enable [options]` - Enable VIP on this node (requires root)
- `disable` - Disable VIP on this node (requires root)
- `failover` - Display VIP failover instructions (does not move VIP directly; use `ha promote` instead)

**Enable options:**
- `--address <ip>` - Virtual IP address (required)
- `--interface <name>` - Network interface (auto-detected if omitted)
- `--netmask <num>` - CIDR netmask for VIP (default: 32, host-only route)
- `--priority <num>` - Priority: 100 = primary, 101+ = backup
- `--token <token>` - Cluster token (generated if omitted)

**Examples:**
```
spiralctl vip status
sudo spiralctl vip enable --address 192.168.1.100
sudo spiralctl vip failover
```

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
- `show` / `list` - Show current configuration
- `get <key>` - Get a specific config value
- `set <key> <value>` - Set a config value

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
```

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

### spiralctl tor

Manage Tor privacy settings for blockchain connections.

```
spiralctl tor [status|enable|disable]
```

**Actions:**
- `status` - Show Tor status (default)
- `enable` - Enable Tor (requires re-running installer)
- `disable` - Disable Tor (requires re-running installer)

---

### spiralctl help

Show the built-in help summary.

```
spiralctl help
```

---

### spiralctl version

Show spiralctl version.

```
spiralctl version
```

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

**AuxPoW merge-mining pairs:** BTC+NMC, BTC+FBTC, BTC+SYS, BTC+XMY, LTC+DOGE, LTC+PEP

**Standalone SHA-256d (not merge-mineable):** BC2, BCH, DGB, QBX

## SEE ALSO

- Spiral Pool Dashboard: `http://<server>:1618`
- Spiral Pool API: `http://<server>:4000`
- Sentinel configuration: `spiralctl config show`
- HA setup guide: `spiralctl ha setup`
