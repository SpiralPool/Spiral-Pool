# Spiral Pool Operations Guide

System overview, installation, configuration, monitoring, high availability, upgrading, and troubleshooting.

---

## System Overview

Spiral Pool is a self-hosted cryptocurrency mining pool implementing Stratum V1/V2 protocols with two key innovations:

1. **Spiral Router** - Intelligent miner classification system that detects hardware via user-agent patterns and assigns optimal difficulty settings at connection time
2. **Lock-Free VarDiff** - Per-session difficulty adjustment using atomic operations with asymmetric limits (4x increase / 0.75x decrease) to prevent oscillation

These systems work together to ensure miners of vastly different hashrates (50 KH/s ESP32 to 200 TH/s S21) can connect to the same pool and reach stable difficulty within seconds.

### How Spiral Router Works

When a miner connects, Spiral Pool performs the following sequence:

1. **Extract User-Agent** - The Stratum `mining.subscribe` message includes a user-agent string
2. **Pattern Matching** - The Spiral Router matches the user-agent against 47 verified regex patterns
3. **Class Assignment** - Miner is assigned to a class (Lottery, Low, Mid, High, Pro, or 7 Avalon-specific classes) with an Unknown fallback for unrecognized devices
4. **Profile Lookup** - Each class has a profile with `InitialDiff`, `MinDiff`, `MaxDiff`, and `TargetShareTime`
5. **Block Time Scaling** - Profile values are scaled based on the blockchain's block time
6. **Difficulty Assignment** - Session starts at the profile's `InitialDiff`

**Example Classification:**

| User-Agent           | Detected Class | InitialDiff |
|----------------------|----------------|-------------|
| `ESP32-Miner/v1.0`  | Lottery        | 0.001       |
| `ESP-Miner/v2.1.5`  | Low            | 580         |
| `AvalonMiner A1566`  | Avalon Pro     | 45,000      |
| `Antminer S21`       | Pro            | 25,600      |

### How VarDiff Works

After initial difficulty assignment, the lock-free vardiff engine adjusts difficulty based on share rate:

1. **Share Recording** - Each share increments atomic counters (no locks)
2. **Retarget Check** - After `RetargetTime` (default 60s), calculate share rate
3. **Variance Calculation** - Compare actual share time vs target share time
4. **Asymmetric Adjustment** - If variance > 50%:
   - Shares too fast: Increase difficulty (up to 4x)
   - Shares too slow: Decrease difficulty (down to 0.75x only)
5. **Backoff** - If no change needed, increment backoff counter for stability

The asymmetric limits prevent oscillation caused by miner firmware work-queue delays.

---

## 1. Installation

> **Platform Requirements:** Ubuntu 24.04.x LTS on **x86_64 (amd64)** architecture. Bare metal or self-hosted VMs are recommended. Cloud/VPS deployments are supported but require written risk acknowledgment during install (provider ToS, bandwidth billing, data access risks). ARM/Raspberry Pi has not been tested. See [WARNINGS.md](../../WARNINGS.md) and [CLOUD_OPERATIONS.md](CLOUD_OPERATIONS.md).

### 0. Server Preparation — Ubuntu 24.04.x LTS (Noble Numbat)

If you already have a server running Ubuntu 24.04 LTS with SSH access, skip to [Linux (Primary Platform)](#linux-primary-platform).

**1. Download the ISO**

Get Ubuntu Server 24.04 LTS (Noble Numbat) from https://ubuntu.com/download/server — any 24.04.x point release works (tested with 24.04.3 and 24.04.4).

**2. Create boot media**

- **Bare metal**: Flash the ISO to a USB drive with [Balena Etcher](https://etcher.balena.io/) or [Rufus](https://rufus.ie/)
- **VM (Proxmox, VMware, VirtualBox)**: Attach the ISO to a new virtual machine

**3. VM resource recommendations**

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 4 cores | 8+ cores |
| RAM | 10 GB (16 GB recommended) | 32 GB |
| Storage | 150 GB SSD | Coin-dependent (see [Storage Requirements](#2-storage-requirements)) |
| Network | Bridged | Bridged (required for miner connectivity) |

**4. Installation steps (Ubuntu Server installer)**

1. Language: **English**
2. Select **"Ubuntu Server (minimized)"** — no desktop, no snaps, minimal footprint
3. Network: configure static IP or note the DHCP-assigned IP for later
4. Storage: use entire disk (default), or custom LVM if preferred
5. Profile: create a `spiralpool` user (or any username — `install.sh` creates its own service user)
6. **Enable OpenSSH server** — check the box during install
7. **Do NOT select any additional packages** (no Docker, no snaps, no featured server packs) — `install.sh` handles all dependencies
8. Reboot when prompted

**5. Reserve a static IP**

Reserve a static IPv4 address for the server on your router/firewall (DHCP reservation or set static in Netplan). Pool daemons, miners, and Sentinel all depend on a stable IP address. **IPv6 is not supported** — the installer disables it at the OS level.

**6. First SSH login and system update**

```bash
ssh YOUR_USER@<SERVER_IP>
```

```bash
sudo apt update && sudo apt upgrade -y
```

**7. Proceed to installation** — continue with the steps below.

### Linux (Primary Platform)

**Option A — Git clone:**

```bash
sudo apt-get -y install git
git clone --depth 1 https://github.com/SpiralPool/Spiral-Pool.git
cd Spiral-Pool
chmod +x install.sh
./install.sh
```

**Option B — ZIP archive (SCP / USB / direct transfer):**

On your local machine, zip the project folder and transfer it to the server:

```bash
scp Spiral-Pool.zip YOUR_USER@YOUR_SERVER_IP:~/
```

Then on the server:

```bash
sudo apt-get -y install unzip
unzip Spiral-Pool.zip
cd Spiral-Pool
chmod +x install.sh
./install.sh
```

> **Note:** `chmod +x install.sh` is always required after unzipping — ZIP archives do not preserve Unix execute permissions regardless of the source OS. The same applies to `upgrade.sh` if you transfer it manually. Git clone preserves execute permissions automatically.

Follow the prompts to select coins and enter wallet addresses.

**Requirements:** Ubuntu 24.04 LTS, 10 GB RAM minimum (16 GB recommended), SSD storage.

### Docker

Docker supports V1 + V2 Stratum in both single-coin and multi-coin mode, including merge mining. V2 uses Noise Protocol encryption (opt-in via `STRATUM_V2_ENABLED=true`). Docker is at full feature parity for single-node deployments.

**Requirements:** Docker Engine 24+ or Docker Desktop with Compose V2.

**Step 1 — Configure environment:**

```bash
cd docker
cp .env.example .env
nano .env    # or your preferred editor
```

You **must** set these variables in `.env`:

| Variable | Description | Example (DigiByte) |
|----------|-------------|-------------------|
| `POOL_COIN` | Coin type | `digibyte` |
| `POOL_ID` | Pool identifier | `dgb_sha256_1` |
| `POOL_ADDRESS` | Your wallet address | `DsG7...` |

Then auto-generate all passwords (daemon RPC, database, Grafana, admin key):
```bash
./generate-secrets.sh
```

Daemon host, ports, and RPC credentials are **auto-detected** from `POOL_COIN`. No manual port configuration needed. See `.env.example` Advanced section for override options.

**Step 2 — Start with the correct profile:**

```bash
# Use the profile matching your POOL_COIN
docker compose --profile dgb up -d       # DigiByte
docker compose --profile btc up -d       # Bitcoin
docker compose --profile bch up -d       # Bitcoin Cash
docker compose --profile bc2 up -d       # Bitcoin II
docker compose --profile nmc up -d       # Namecoin
docker compose --profile xmy up -d       # Myriadcoin
docker compose --profile fbtc up -d      # Fractal Bitcoin
docker compose --profile qbx up -d       # Q-BitX
docker compose --profile sys up -d       # Syscoin (daemon sync only — mining requires native install)
docker compose --profile ltc up -d       # Litecoin
docker compose --profile doge up -d      # Dogecoin
docker compose --profile dgb-scrypt up -d # DigiByte-Scrypt
docker compose --profile pep up -d       # PepeCoin
docker compose --profile cat up -d       # Catcoin
```

**Step 3 — Monitor:**

```bash
docker compose --profile dgb logs -f     # View logs
docker compose --profile dgb ps          # Check status
docker compose --profile dgb down        # Stop all services
```

**Ports:** Miners connect to the `STRATUM_PORT` configured in `.env` (e.g., `3333` for DigiByte). The dashboard is at `http://localhost:1618`. Grafana is at `http://localhost:3000`.

### Windows (Experimental)

Two Windows installation paths are available. Both are experimental and intended for development, testing, and pool evaluation. For production mining, use Linux native installation.

| Path | Installer | Features | Best For |
|------|-----------|----------|----------|
| **Docker Desktop** | `install-windows.ps1` | Single coin, V1+TLS, automated setup | Quick evaluation |
| **WSL2 Native** | `install.sh` inside WSL2 | All coins, multi-coin, merge mining, V2 | Full feature testing |

**Docker Desktop (quick start):**

```powershell
# Open PowerShell as Administrator
.\install-windows.ps1
```

**WSL2 Native (full features):**

```powershell
wsl --install -d Ubuntu-24.04
```
```bash
# Inside WSL2 Ubuntu terminal:
git clone --depth 1 https://github.com/SpiralPool/Spiral-Pool.git
cd Spiral-Pool && sudo ./install.sh
```

For the full Windows installation guide, decision tree, port forwarding setup, shutdown hook, and troubleshooting, see **[WINDOWS_GUIDE.md](WINDOWS_GUIDE.md)**.

**Use Windows for:** Development, testing, pool evaluation.
**Use Linux native for:** All production mining operations.

### How install.sh Works

The installer is a single self-contained script (~36,500 lines) that handles the entire deployment. Here is the high-level flow:

```
main()
 ├── parse_cli_args          # Parse --coin, --address, --simulate-cloud, etc.
 ├── show_banner             # Print version and ASCII art
 ├── check_prerequisites     # Verify root/sudo, disk space, memory; detect cloud provider (sets CLOUD_DETECTED)
 ├── show_legal_acceptance   # Terms of use prompt — cloud: YES gate; non-cloud: I AGREE gate
 ├── acquire_operation_lock  # Prevent concurrent install/upgrade
 ├── check_resume            # Resume from checkpoint if previous run was interrupted
 ├── detect_operating_system # Verify Ubuntu 24.04 LTS
 ├── select_deploy_method    # Docker (bare metal) or VM Native (traditional)
 │
 ├── [Docker path] ──────────> docker_main() → build images → start compose
 │
 ├── [VM Native path continues below]
 ├── select_install_mode     # "full" (pool + dashboard + sentinel) or "pool" (pool only)
 ├── select_coin_mode        # Single coin, multi-coin, or merge mining
 ├── select_ha_mode          # Standalone, HA Primary, or HA Backup
 ├── collect_configuration   # Wallet addresses, ports, passwords, HA settings
 │
 │   [Checkpointed steps — resume-safe on failure]
 ├── setup_system            # Create user, install apt packages, configure firewall
 ├── install_<coin>          # Download + configure each enabled blockchain daemon
 ├── ask_blockchain_rsync    # Optionally replicate chain data from another node
 ├── install_postgresql      # Install + configure PostgreSQL 18
 ├── install_redis           # (HA only) Redis for cross-node deduplication
 ├── install_go              # Install Go toolchain
 ├── build_stratum           # Compile spiralstratum + spiralctl from source
 ├── run_stratum_tests       # Run Go test suite
 ├── configure_stratum       # Generate config.yaml with coin/port/DB/HA settings
 ├── install_dashboard       # (full mode) Deploy Spiral Dash web UI
 ├── install_sentinel        # (full mode) Deploy Spiral Sentinel monitoring
 ├── install_health_monitor  # Self-healing service monitor
 ├── create_sync_monitor     # Auto-start stratum when blockchain syncs
 ├── create_helper_scripts   # wait-for-node.sh, backup/restore, etc.
 │
 ├── verify installation     # Check all binaries and services exist
 ├── start_services          # Enable + start systemd services
 └── print_completion        # Summary with connection info
```

**Multi-Disk Storage:**

If additional storage devices are detected during installation, the installer offers to use them for blockchain data (mounted at `/spiralpool/chain/`). If an unformatted (raw) disk is detected, the installer will offer to format it as ext4. **Formatting permanently destroys all data on the selected device.** You must type `YES` (uppercase) to confirm — any other input cancels safely. The OS disk is never offered for formatting. **Verify which disks are connected before running the installer.** See [WARNINGS.md](../../WARNINGS.md) for the complete disk formatting hazard warning.

**Key design features:**
- **Checkpoint resume**: Each major step saves a checkpoint. If the script fails mid-install (e.g., network timeout downloading a daemon), re-running `./install.sh` resumes from the last successful checkpoint.
- **Operation lock**: Prevents running install and upgrade simultaneously.
- **Password generation**: Database and RPC passwords are generated once and persisted. On resume, existing credentials are recovered from `config.yaml` or regenerated if missing.
- **Atomic config writes**: Configuration files are written to a temp file then moved into place to prevent corruption on power loss.

### How upgrade.sh Works

The upgrade script updates pool components while preserving all user data (blockchain data, configs, wallets, database).

```
main()
 ├── parse arguments         # --local, --force, --check, --rollback, component flags
 ├── check_root              # Must run as root
 ├── acquire_operation_lock  # Prevent concurrent install/upgrade
 ├── detect_services         # Find running spiralstratum, spiraldash, spiralsentinel
 ├── detect_pool_user        # Identify service user (spiraluser)
 │
 ├── [--check mode] ────────> check_for_updates() → print JSON → exit
 ├── [--rollback mode] ─────> rollback_to_backup() → restore from backup dir → exit
 │
 ├── detect_current_version  # Read /spiralpool/VERSION
 ├── get_target_version      # From GitHub release or local source
 ├── download_new_version    # git clone to temp dir (or use --local source)
 │
 ├── create_backup           # Snapshot binaries + configs + dashboard + sentinel
 │
 ├── build_stratum           # Compile binaries to temp dir (services still running)
 ├── stop_services           # Graceful stop (DOWNTIME STARTS)
 │
 ├── fix_config_issues       # (--fix-config) Patch known config problems
 ├── fix_database_ownership  # Ensure DB tables owned by app user
 ├── deploy_stratum          # Atomic mv from temp dir (seconds)
 ├── update_dashboard        # Copy new dashboard files, preserve config
 ├── update_sentinel         # Copy new sentinel files, preserve config
 ├── update_systemd_services # (--update-services) Refresh .service files
 ├── update_utility_scripts  # Copy helper scripts, update-checker
 ├── update_version_file     # Write new VERSION
 ├── update_upgrade_script   # Self-update upgrade.sh
 │
 ├── start_services          # Restart stopped services (DOWNTIME ENDS)
 ├── verify_upgrade          # Confirm binaries exist and services started
 └── show_summary            # Print what was updated + rollback instructions
```

**What is preserved:** Blockchain data, `config.yaml`, wallet files, PostgreSQL database, Sentinel state/config, dashboard config.

**What is upgraded:** `spiralstratum` binary, `spiralctl` binary, dashboard Python/HTML, Sentinel Python, systemd service files (with `--update-services`), helper scripts, documentation.

**Rollback:** Every upgrade creates a timestamped backup in `/spiralpool/backups/`. Use `sudo ./upgrade.sh --rollback <backup-name>` to restore.

---

## 2. Storage Requirements

| Coin | Symbol | Approximate Storage |
|------|--------|-------------------|
| Bitcoin | BTC | 600 GB |
| Bitcoin Cash | BCH | 250 GB |
| Litecoin | LTC | 150 GB |
| Syscoin | SYS | 25 GB |
| Dogecoin | DOGE | 80 GB |
| DigiByte | DGB | 60 GB |
| Fractal Bitcoin | FBTC | 10 GB |
| Q-BitX | QBX | 5 GB |
| Namecoin | NMC | 15 GB |
| Myriad | XMY | 6 GB |
| Bitcoin II | BC2 | 5 GB |
| PepeCoin | PEP | 5 GB |
| Catcoin | CAT | 5 GB |
| DGB-Scrypt | DGB-SCRYPT | (shares DGB data) |

> Storage values are approximate and will vary based on blockchain growth and index configuration. All nodes run as full (unpruned) nodes. Plan for additional headroom.

> Syscoin (SYS) is merge-mining only and requires BTC as parent chain. The SYS daemon must still be installed and synced.

---

## 3. Configuration

### Miner Setup

```
Pool: stratum+tcp://YOUR_SERVER_IP:PORT
User: YOUR_WALLET_ADDRESS.worker_name
Pass: x
```

Example for DigiByte:
```
stratum+tcp://192.168.1.100:3333
dgb1qxyz...abc.rig1
x
```

See [REFERENCE.md](../reference/REFERENCE.md) for port assignments per coin.

### Mining Modes

```bash
# Solo (one coin)
spiralctl mining solo dgb

# Multi-coin (same algorithm only)
spiralctl mining multi btc,bch,dgb

# Merge mining (parent + aux chains)
spiralctl mining merge enable nmc,sys    # BTC parent
spiralctl mining merge enable doge,pep   # LTC parent
```

### Merge Mining Pairs

| Parent | Auxiliary | Chain ID |
|--------|-----------|----------|
| BTC | NMC | 1 |
| BTC | FBTC | 8228 |
| BTC | SYS | 16 |
| BTC | XMY | 90 |
| LTC | DOGE | 98 |
| LTC | PEP | 63 |

### Key Service Ports

| Port | Service |
|------|---------|
| 3333-20337 | Stratum (see [REFERENCE.md](../reference/REFERENCE.md)) |
| 4000 | REST API |
| 1618 | Dashboard |
| 9100 | Prometheus metrics |

### SimpleSwap Swap Alerts (Optional)

Spiral Sentinel can send swap recommendations when a mined coin rises 25%+ against BTC over a 7-day window. Alerts are delivered via your configured notification channels (Discord, Telegram, XMPP, ntfy, or email) and include a [SimpleSwap.io](https://simpleswap.io) link with the source coin and BTC pre-selected. **No automatic swaps are performed.**

The pool software makes no API calls to SimpleSwap.io and stores no wallet addresses or API keys. All swap activity happens entirely on the SimpleSwap website in the operator's own browser — click the link in the alert, enter your BTC address on the site, and complete the swap there. This keeps the pool server completely out of any financial transaction.

**Enable during installation** — the installer will prompt to enable or disable the feature.

**Enable manually:**
```bash
sudo tee /etc/spiralpool/simpleswap.conf > /dev/null << 'EOF'
SIMPLESWAP_ENABLED=true
EOF
sudo chmod 600 /etc/spiralpool/simpleswap.conf
sudo chown root:root /etc/spiralpool/simpleswap.conf
```

**Disable:**
```bash
sudo sed -i 's/SIMPLESWAP_ENABLED=true/SIMPLESWAP_ENABLED=false/' /etc/spiralpool/simpleswap.conf
```

> **Operator responsibility:** You are solely responsible for compliance with SimpleSwap.io's Terms of Service, all AML/KYC requirements, fees, tax obligations, and financial regulations in your jurisdiction. See [TERMS.md](../../TERMS.md) section 5D and [WARNINGS.md](../../WARNINGS.md) for full disclosure.

---

## 4. Operations

### Status Commands

```bash
spiralctl status              # Overall status
spiralctl mining status       # Mining config
spiralctl node status         # Blockchain sync
spiralctl pool stats          # Hashrate, miners, blocks
```

### Node Management

```bash
spiralctl node start dgb
spiralctl node stop dgb
spiralctl node restart all
spiralctl logs
```

### Graceful Shutdown / Reboot

Always use `spiralctl shutdown` instead of bare `shutdown` or `reboot`. It stops all pool services in the correct order before the OS halts, preventing dirty database state and abrupt miner disconnections.

```bash
sudo spiralctl shutdown             # Stop all services, then power off
sudo spiralctl shutdown --reboot    # Stop all services, then reboot
sudo spiralctl shutdown --yes       # Skip confirmation prompt
```

**Stop order:** spiralstratum → spiralsentinel → spiraldash → keepalived (HA) → patroni → etcd (HA)

### API Endpoints

```bash
curl localhost:4000/api/pools                                # Pool list
curl localhost:4000/api/pools/dgb_sha256_1/stats             # Pool stats
curl localhost:4000/api/pools/dgb_sha256_1/miners/ADDRESS    # Miner stats
curl localhost:4000/health                                   # Health check
```

> **Note:** The pool ID in the URL matches the `pool_id` value in your `config.yaml` (e.g., `dgb_sha256_1`, `btc_sha256_1`).

Full API reference in [REFERENCE.md](../reference/REFERENCE.md).

---

## 5. Monitoring (Spiral Sentinel)

<p align="center">
  <img src="../../assets/Spiral Sentinel.jpg" alt="Spiral Sentinel" width="400">
</p>

Spiral Sentinel is the autonomous monitoring system:
- Self-healing miner management with auto-restart
- Temperature monitoring with critical alerts
- Block found notifications (Discord/Telegram)
- Periodic hashrate reports (6-hour, weekly, monthly)
- Device hint registration for Spiral Router

### Configuration

Edit `~spiraluser/.spiralsentinel/config.json`:
```json
{
  "discord_webhook_url": "https://discord.com/api/webhooks/...",
  "wallet_address": "dgb1q...",
  "pool_api_url": "http://localhost:4000"
}
```

### Key Alerts

| Alert | Trigger |
|-------|---------|
| block_found | Pool found a block |
| miner_offline | Miner unreachable 10+ min |
| temp_critical | Temperature >= 85C |
| hashrate_crash | 25%+ network drop |

### Supported Miner APIs

Sentinel monitors miners by actively polling their HTTP or CGMiner API. Only miners with a supported API can be monitored directly.

| API | Port | Supported Miners |
|-----|------|-----------------|
| AxeOS (HTTP) | 80 | BitAxe, NerdQAxe, NMAxe, Lucky Miner, Jingle, Zyber, Hammer |
| Goldshell (HTTP) | 80 | Goldshell Mini DOGE, LT5, LT6 |
| CGMiner (TCP) | 4028 | Avalon, Antminer, Whatsminer, Innosilicon, FutureBit, Elphapex |
| Pool API | N/A | ESP32 miners (NerdMiner, BitMaker, ESP32 Miner V2) |

### ESP32 Miner Monitoring

ESP32 lottery miners (NerdMiner, BitMaker, ESP32 Miner V2, etc.) have **no HTTP or CGMiner API**. They communicate exclusively via the Stratum protocol. Sentinel monitors them by polling the pool's connections and worker stats APIs (`/api/pools/{id}/connections`) instead of querying the device directly.

**What Sentinel can track:** Online/offline status, hashrate, accepted/rejected shares, current difficulty.
**What Sentinel cannot track:** Temperature, fan speed, uptime, power consumption (no device API to query).

**Setup requirements:**
1. Add the ESP32 miner manually: `spiralctl scan --add <IP>` → select type `esp32miner`
2. Provide the Stratum worker name when prompted (the part after the dot in `ADDRESS.workername`)
3. `pool_admin_api_key` must be set in Sentinel config (set automatically during install)
4. The ESP32 must be actively connected to the pool

### Limitations

**Custom firmware miners (BraiinsOS, Vnish, LuxOS):** Require manual setup with firmware credentials. Cannot be auto-discovered. See [MINER_SUPPORT.md](../reference/MINER_SUPPORT.md) for details.

**Trend data requires history:** Difficulty and network hashrate trends (24h, 7d, 30d) are calculated from samples collected every 15 minutes. After a fresh install or Sentinel restart, trends will show `+0.0%` until sufficient historical data accumulates. Expect 24h trends after ~6 hours, 7d trends after ~2 days, and 30d trends after ~1 week of continuous operation. History is persisted to `~/.spiralsentinel/history.json` and survives restarts.

### Commands

```bash
systemctl status spiralsentinel
systemctl restart spiralsentinel
spiralctl webhook test                            # Test webhook
```

---

## 6. High Availability

> **BARE METAL / SELF-HOSTED VMs ONLY** — HA is not supported on cloud or VPS deployments (AWS, GCP, Azure, DigitalOcean, Hetzner, Vultr, etc.). The installer and `spiralctl ha enable` both block HA setup when a cloud environment is detected. Keepalived VRRP requires broadcast/multicast MAC-based election which is blocked by cloud hypervisors — VIP failover will silently fail. Deploy on hardware you physically control.

> **NOTE**: "High Availability" refers to architectural patterns designed to improve resilience. It does not guarantee any specific uptime percentage or SLA. Failover times, data consistency during transitions, and overall reliability depend on your specific configuration, network conditions, and infrastructure.

### VIP Failover

**First node:**
```bash
spiralctl vip enable --address 192.168.1.200 --interface eth0 --netmask 32
# Save the cluster token
```

**Backup nodes:**
```bash
spiralctl vip enable --address 192.168.1.200 --priority 101 --token <cluster-token>
```

Miners connect to VIP: `stratum+tcp://192.168.1.200:3333`

### Database Replication

```bash
sudo spiralctl ha enable --vip 192.168.1.200
```

### Failover Commands

```bash
spiralctl ha promote      # Promote this node to primary (DB promotion only; VIP requires separate step on old primary)
spiralctl ha failback     # Rejoin cluster as backup after maintenance
```

### HA Architecture

- **Systemd (native)**: `ha-role-watcher.sh` polls every 5s, `ha-service-control.sh` starts/stops `spiralsentinel` + `spiraldash`. Only MASTER runs services.
- **Sentinel**: Defense-in-depth `is_master_sentinel()` suppresses alerts on non-MASTER nodes even if running.
- **Dashboard**: Relies on systemd service control for HA behavior.
- **Docker HA**: Known limitation. No systemd role watcher. Sentinel alerts are safe (master check built in), but polling and dashboard are duplicated across nodes.

### Payment Fencing

See [SECURITY_MODEL.md](../architecture/SECURITY_MODEL.md) for three-layer payment protection against split-brain double-payment.

---

## 7. Upgrading

```bash
cd /spiralpool
chmod +x upgrade.sh && sudo ./upgrade.sh
```

> **Windows SCP users:** Windows does not preserve Unix execute permissions when transferring files via SCP. The `chmod +x` above is required if upgrade.sh was copied from a Windows machine. Git clone and Linux-to-Linux SCP both preserve execute permissions automatically.

Options: `--check`, `--force`, `--no-backup`, `--local`, `--rollback`, `--auto`, `--update-services`, `--stratum-only`, `--dashboard-only`, `--sentinel-only`, `--skip-start`, `--full`, `--fix-config`

**Preserved:** blockchain data, configs, wallets, database
**Upgraded:** binaries, dashboard, sentinel, docs

### Automated upgrades

If `auto_update_mode: auto` is set in `config.yaml`, Sentinel handles upgrades automatically:
- Checks GitHub for new releases every 6 hours (no auth token needed — repo is public)
- Runs `sudo /spiralpool/upgrade.sh --auto` unattended
- Suppresses Discord alerts during upgrade (maintenance mode)
- Sends a completion or failure notification via Discord

The Windows execute-permission issue does not affect automated upgrades — Sentinel calls the installed copy at `/spiralpool/upgrade.sh`, which has its permissions set by the installer.

For a detailed breakdown of the upgrade flow, see [How upgrade.sh Works](#how-upgradesh-works) in the Installation section.

---

## 8. Files and Directories

```
/spiralpool/                            Installation root
  bin/spiralstratum                     Pool binary
  bin/spiralctl                         CLI tool
  config/config.yaml                    Pool configuration
  config/coins.manifest.yaml            Coin registry
  tls/                                  TLS certificates (auto-generated)
  dashboard/                            Web UI (Spiral Dash)
  bin/SpiralSentinel.py                 Monitoring (Spiral Sentinel)
  scripts/                              Operational scripts (regtest, HA, maintenance)
  data/bans.json                        Ban persistence
  data/miners.json                      Miner database
  data/wal/{poolID}/                    Share write-ahead log (binary WAL: current.wal)
  data/.metrics_token                   Prometheus auth token (chmod 600)
  logs/                                 Application logs
  dgb/, btc/, bch/, bc2/                Blockchain data + binaries (DGB, BTC, BCH, BC2)
  ltc/, doge/, pep/, cat/...             Blockchain data + config (Scrypt coins)
  ltc-bin/, doge-bin/, pep-bin/...       Daemon binaries (Scrypt coins, symlinked to /usr/local/bin/)
  nmc/, sys/, xmy/, fbtc/               Blockchain data + config (merge-mined coins)
  nmc-bin/, sys-bin/, xmy-bin/, fbtc-bin/ Daemon binaries (merge-mined coins)
  qbx/                                  Blockchain data + config (Q-BitX, standalone SHA-256d)
  qbx-bin/                              Daemon binaries (Q-BitX, symlinked to /usr/local/bin/)

~spiraluser/.spiralsentinel/             Sentinel state
  config.json                           Sentinel settings (webhook URLs, etc.)
  state.json                            Lifetime stats, achievements
  history.json                          Historical data
  nicknames.json                        Miner nicknames

/spiralpool/dashboard/data/             Dashboard state
  dashboard_config.json                 Dashboard settings
```

---

## 9. Troubleshooting

### Service Issues

```bash
systemctl status spiralstratum
journalctl -u spiralstratum -n 50
```

### Miners Cannot Connect

```bash
# Check port is listening
ss -tlnp | grep 3333

# Check firewall
sudo ufw allow 3333/tcp
```

### Blockchain Not Syncing

```bash
# Check peers
dgb-cli getpeerinfo | grep -c '"addr"'

# Check sync progress
dgb-cli getblockchaininfo | jq '.verificationprogress'
```

### Dashboard Not Loading

```bash
systemctl status spiraldash
curl -I http://localhost:1618
```

### Database Issues

```bash
systemctl status postgresql
sudo -u postgres psql -d spiralstratum -c "SELECT 1;"
```

### Reset Database (loses all history)

```bash
sudo systemctl stop spiralstratum
sudo -u postgres psql -c "DROP DATABASE spiralstratum; CREATE DATABASE spiralstratum;"
sudo systemctl start spiralstratum
```

### Firewall Configuration

```bash
# Only open stratum ports for your coins
sudo ufw allow 3333/tcp   # DGB
sudo ufw allow 4333/tcp   # BTC
sudo ufw allow 1618/tcp   # Dashboard (optional)
sudo ufw enable
```

### Credentials

- Database password: `/spiralpool/config/config.yaml`
- RPC credentials: Coin-specific `.conf` files (e.g., `/spiralpool/dgb/digibyte.conf`)
- Dashboard: Set on first access at `:1618`

---

## 10. Operator Legal Protection (Optional)

If you accept miners from the public, you may want to establish a direct legal relationship between you (the operator) and your miners. The Spiral Pool [LICENSE](../../LICENSE) and [TERMS.md](../../TERMS.md) govern the software license between authors and users of the code — they do **not** create a legal relationship between pool operators and miners connecting to operator-hosted pools.

### Stratum MOTD — Built-in Legal Banner

Spiral Pool implements a stratum-level "Message of the Day" via the `client.show_message` protocol extension ([server.go:732-743](../../src/stratum/internal/stratum/server.go#L732-L743)). This is sent over the Stratum TCP connection directly to mining hardware/software immediately after `mining.subscribe`, before any work is issued. Compatible miners display the message on their status screen or LCD; incompatible miners silently ignore it.

**Configuration** (in `config.yaml`):
```yaml
stratum:
  motd: "By connecting to this pool, you agree to our Terms of Service at https://example.com/tos"
```

**Legal use cases:** Terms of service acceptance notices, jurisdiction-specific disclaimers, data processing notices (GDPR), service-level expectations.

**Limitations:** MOTD acceptance is passive (display-only). Whether a displayed banner constitutes legally binding acceptance varies by jurisdiction. For stronger acceptance mechanisms, consider web-based registration with click-through terms.

### Suggested Operator Terms

Operators accepting public miners may wish to require acceptance of terms covering:
- Acknowledgment of financial risks and single-operator wallet architecture
- No guarantee of rewards or uptime
- Operator's limitation of liability
- Governing law for the operator's jurisdiction
- Data handling and privacy practices

Consult legal counsel in your jurisdiction. **The Spiral Pool authors provide no legal templates and accept no liability for operator-miner relationships.**

---

*Spiral Pool — Phi Hash Reactor 2.2.1*
