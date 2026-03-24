# Spiral Pool - Docker & WSL2 Setup Guide

Complete guide for running Spiral Pool in Docker containers. Covers Linux Docker and Windows/WSL2 deployments.

---

## What Docker Supports

Docker supports **V1 + V2 Stratum** in both single-coin and multi-coin mode:

- **Single-coin mode:** One coin per deployment via `POOL_MODE=single` + `--profile <coin>`
- **Multi-coin mode:** All enabled coins in one deployment via `POOL_MODE=multi` + `--profile multi`
- **Stratum V1** (plain + TLS encrypted connections)
- **Stratum V2** (SV2 binary protocol with Noise NX encryption) — opt-in via `STRATUM_V2_ENABLED=true`
- All 14 coins: DGB, BTC, BCH, BC2, NMC, SYS, XMY, FBTC, QBX, LTC, DOGE, DGB-SCRYPT, PEP, CAT
- Merge mining in multi-coin mode: SHA-256d (BTC+NMC, BTC+FBTC, BTC+SYS, BTC+XMY, or DGB as parent) and Scrypt (LTC+DOGE, LTC+PEP)
- Dashboard, Sentinel monitoring, Prometheus, and Grafana included
- Self-signed TLS certificates auto-generated (V1 TLS); Noise keys generated in memory (V2)

**Optional: Database HA (experimental, not validated for production):**

- PostgreSQL high availability via `docker-compose.ha.yml` overlay (Patroni/etcd/HAProxy/Redis)

> **Note:** VIP failover (Keepalived) and multi-node stratum clustering are native-install features designed for bare-metal HA deployments. Docker is designed for single-node operation — if you need multi-node HA, use native installation (`sudo ./install.sh`).

---

## Prerequisites

### Linux

- Docker Engine 24+ with Compose V2
- 8 GB RAM minimum (16 GB recommended for BTC/LTC)
- SSD storage for blockchain data (HDD not recommended)

```bash
# Install Docker Engine (Ubuntu)
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
# Log out and back in for group changes to take effect
```

### ARM64 / aarch64 (Raspberry Pi, Apple Silicon, ARM bare-metal servers)

> **EXPERIMENTAL / UNTESTED** — ARM64 Docker support is provided on a best-effort
> basis. Upstream coin projects publish arm64 binaries and Docker will automatically
> pull the correct architecture, but this has **not been tested** by the Spiral Pool
> team. Known limitations:
>
> - **Fractal Bitcoin (FBTC)** and **Q-BitX (QBX)** have no arm64 binaries and will fail to build on ARM.
> - Go compilation and stratum binary compatibility are unverified on ARM.
> - Performance characteristics are unknown on ARM hardware.
> - 12 of 14 coin images include arm64 conditionals; FBTC and QBX are x86_64-only.
>
> If you encounter issues on ARM64, please report them with your architecture
> (`dpkg --print-architecture`) and full error output.

### Windows (WSL2) *(Experimental — not recommended for production)*

> **EXPERIMENTAL.** The Windows/Docker Desktop deployment has not been validated for production use. Docker Desktop can be terminated by Windows updates, sleep, or memory pressure. For 24/7 production mining, use native Ubuntu on dedicated hardware.

> **Two Windows paths exist.** This guide covers the **Docker Desktop path** (`install-windows.ps1`). If you installed Spiral Pool by running `install.sh` directly inside WSL2 Ubuntu, see the [WSL2 Native path](#wsl2-native-path-installsh-in-wsl2) below instead.

**Docker Desktop path requirements:**

- Windows 10/11 with WSL2 enabled
- Docker Desktop with WSL2 backend
- Ubuntu distribution installed in WSL2

```powershell
# 1. Install WSL2 (PowerShell as Admin)
wsl --install -d Ubuntu

# 2. Install Docker Desktop from https://www.docker.com/products/docker-desktop/
#    Enable WSL2 backend in Docker Desktop > Settings > General

# 3. Run the automated installer (as Administrator):
.\install-windows.ps1

# 4. Or open Ubuntu WSL2 terminal and follow the Linux Docker steps
```

**WSL2 limitations:**

- Blockchain sync takes 2-4x longer than native Linux (I/O overhead)
- Bridge networking only (no host mode)
- Not recommended for production mining
- Best for: development, testing, pool evaluation

---

## Quick Start — Single-Coin Mode

### Step 1: Configure

```bash
cd docker
cp .env.example .env
```

Edit `.env` and set these two values:

```bash
# 1. Choose your coin (must match the --profile you use)
POOL_COIN=digibyte

# 2. Set your wallet address
POOL_ADDRESS=YOUR_WALLET_ADDRESS_HERE
```

Valid `POOL_COIN` values: `digibyte`, `dgb-scrypt` (or `digibyte-scrypt`), `bitcoin`, `litecoin`, `bitcoincash`, `bitcoinii`, `dogecoin`, `pepecoin`, `catcoin`, `namecoin`, `syscoin`, `myriadcoin`, `fractalbitcoin`, `qbitx`

Also set your Pool ID to match your coin:

| Coin | POOL_ID |
|------|---------|
| DigiByte | `dgb_sha256_1` |
| DigiByte-Scrypt | `dgb_scrypt_1` |
| Bitcoin | `btc_sha256_1` |
| Bitcoin Cash | `bch_sha256_1` |
| Bitcoin II | `bc2_sha256_1` |
| Namecoin | `nmc_sha256_1` |
| Syscoin | `sys_sha256_1` |
| Myriadcoin | `xmy_sha256_1` |
| Fractal BTC | `fbtc_sha256_1` |
| Q-BitX | `qbx_sha256_1` |
| Litecoin | `ltc_scrypt_1` |
| Dogecoin | `doge_scrypt_1` |
| PepeCoin | `pep_scrypt_1` |
| Catcoin | `cat_scrypt_1` |

### Step 2: Generate Passwords

```bash
./generate-secrets.sh
```

This auto-generates unique random passwords for:
- All coin daemon RPC credentials (14 daemons; DGB-SCRYPT shares the DGB daemon)
- PostgreSQL database
- Grafana admin
- Admin API key
- HA infrastructure (if needed later)

The stratum automatically detects which coin's RPC password to use based on `POOL_COIN`.

### Step 3: Start

```bash
# Replace dgb with your coin's profile
docker compose --profile dgb up -d
```

Available single-coin profiles:

| Profile | Coin | Algorithm |
|---------|------|-----------|
| `dgb` | DigiByte | SHA-256d |
| `btc` | Bitcoin | SHA-256d |
| `bch` | Bitcoin Cash | SHA-256d |
| `bc2` | Bitcoin II | SHA-256d |
| `nmc` | Namecoin | SHA-256d |
| `sys` | Syscoin (merge-mining only — use multi-coin mode, see below) | SHA-256d |
| `xmy` | Myriadcoin | SHA-256d |
| `fbtc` | Fractal Bitcoin | SHA-256d |
| `qbx` | Q-BitX | SHA-256d |
| `ltc` | Litecoin | Scrypt |
| `doge` | Dogecoin | Scrypt |
| `dgb-scrypt` | DigiByte (Scrypt) | Scrypt |
| `pep` | PepeCoin | Scrypt |
| `cat` | Catcoin | Scrypt |

> **Syscoin note:** Syscoin is merge-mining only (requires BTC parent chain) and cannot solo mine. Use multi-coin mode with `ENABLE_BTC=true` and `ENABLE_SYS=true` plus merge mining enabled. The single-coin `sys` profile syncs the Syscoin daemon only — it cannot mine without a BTC parent.

---

## Quick Start — Multi-Coin Mode

Multi-coin mode runs multiple coins in a single Docker deployment. It also enables merge mining.

### Step 1: Configure

```bash
cd docker
cp .env.example .env
```

Edit `.env`:

```bash
# 1. Set multi-coin mode
POOL_MODE=multi

# 2. Enable each coin you want to mine and set its wallet address
ENABLE_DGB=true
DGB_POOL_ADDRESS=YOUR_DGB_WALLET_ADDRESS

ENABLE_BTC=true
BTC_POOL_ADDRESS=YOUR_BTC_WALLET_ADDRESS

ENABLE_LTC=true
LTC_POOL_ADDRESS=YOUR_LTC_WALLET_ADDRESS

# ... enable as many coins as you need
```

### Step 2: Configure Merge Mining (Optional)

To merge-mine auxiliary chains alongside a parent chain:

```bash
# Enable merge mining
MERGE_MINING_ENABLED=true

# Which algorithm(s): sha256d, scrypt, or both
MERGE_MINING_ALGO=both

# SHA-256d aux chains (parent: BTC, or DGB if BTC disabled)
MERGE_MINING_AUX_CHAINS_SHA256D=NMC,SYS,XMY,FBTC

# Scrypt aux chains (parent: LTC)
MERGE_MINING_AUX_CHAINS_SCRYPT=DOGE,PEP
```

**Merge mining topology:**

```
BTC ──┬── NMC  (Namecoin)         LTC ──┬── DOGE (Dogecoin)
      ├── SYS  (Syscoin)                └── PEP  (PepeCoin)
      ├── XMY  (Myriad)
      └── FBTC (Fractal Bitcoin)
```

> You must also enable the parent and aux coins (`ENABLE_BTC=true`, `ENABLE_NMC=true`, etc.) and provide wallet addresses for each.

### Step 3: Generate Passwords &amp; Start

```bash
./generate-secrets.sh
docker compose --profile multi up -d
```

The `multi` profile starts all enabled coin daemons plus shared services (stratum, PostgreSQL, dashboard, sentinel, Prometheus, Grafana). The entrypoint generates a V2 multi-coin config automatically from your `.env` settings.

---

## Connecting Miners

After the pool starts (allow 5-10 minutes for blockchain daemon initialization):

| Connection | URL | Access |
|------------|-----|--------|
| Stratum (plain) | `stratum+tcp://YOUR_IP:PORT` | LAN/WAN |
| Stratum (TLS) | `stratum+ssl://YOUR_IP:TLS_PORT` | LAN/WAN |
| Dashboard | `http://YOUR_IP:1618` | LAN/WAN |
| Pool API | `http://YOUR_IP:4000/api/pools` | LAN/WAN |
| Grafana | `http://localhost:3000` | Localhost only |
| Prometheus | `http://localhost:9090` | Localhost only |

> **Note:** Grafana and Prometheus are bound to `127.0.0.1` for security (no unauthenticated external access). To access remotely, use an SSH tunnel: `ssh -L 3000:localhost:3000 user@pool-server`

### Port Reference

| Coin | Stratum V1 | Stratum V2 (Noise) | Stratum TLS |
|------|-----------|---------------------|-------------|
| DigiByte | 3333 | 3334 | 3335 |
| DigiByte-Scrypt | 3336 | 3337 | 3338 |
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
| Fractal BTC | 18335 | 18336 | 18337 |
| Q-BitX | 20335 | 20336 | 20337 |

> V2 ports are only active when `STRATUM_V2_ENABLED=true` is set in `.env`. V2 uses the Noise NX protocol (`secp256k1 + ChaCha20-Poly1305 + SHA-256`) — encryption keys are generated in memory at startup, no certificate files needed.

### Miner Configuration Example (cgminer/bfgminer)

```
-o stratum+tcp://192.168.1.100:3333 -u YOUR_WALLET_ADDRESS -p x
```

For TLS-encrypted connections:

```
-o stratum+ssl://192.168.1.100:3335 -u YOUR_WALLET_ADDRESS -p x
```

---

## Managing Your Pool

### View Logs

```bash
# Single-coin mode (replace dgb with your profile)
docker compose --profile dgb logs -f
docker compose --profile dgb logs -f stratum

# Multi-coin mode
docker compose --profile multi logs -f
docker compose --profile multi logs -f stratum
docker compose --profile multi logs -f bitcoin      # specific daemon
```

### Check Status

```bash
docker compose --profile dgb ps       # single-coin
docker compose --profile multi ps     # multi-coin
```

### Stop All Services

```bash
docker compose --profile dgb down
```

### Restart

```bash
docker compose --profile dgb restart
```

### Rebuild After Code Changes

```bash
docker compose --profile dgb build --no-cache
docker compose --profile dgb up -d
```

---

## Blockchain Sync

The blockchain daemon must fully sync before mining can begin. Sync times vary:

| Coin | Approximate Sync Time (SSD) | Data Size |
|------|---------------------------|-----------|
| DigiByte | 4-8 hours | ~45 GB |
| Bitcoin | 2-5 days | ~600 GB |
| Litecoin | 12-24 hours | ~180 GB |
| Dogecoin | 12-24 hours | ~75 GB |
| Bitcoin Cash | 1-3 days | ~350 GB |
| Other coins | 2-12 hours | 1-85 GB |

Monitor sync progress:

```bash
# Check daemon logs
docker compose --profile dgb logs -f digibyte

# Check blockchain height (once daemon is responding)
docker exec spiralpool-digibyte digibyte-cli -conf=/home/digibyte/.digibyte/digibyte.conf getblockchaininfo
```

### Using External Blockchain Data

If you already have blockchain data from a native install:

```bash
# In .env, point to your existing data directory:
DGB_DATA_DIR=/path/to/existing/digibyte/data
```

---

## Notifications

Configure alerts in `.env` before starting:

### Discord

```bash
DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/YOUR_WEBHOOK_URL
```

### Telegram

```bash
TELEGRAM_BOT_TOKEN=YOUR_BOT_TOKEN
TELEGRAM_CHAT_ID=YOUR_CHAT_ID
```

### XMPP/Jabber

```bash
XMPP_JID=user@server.com
XMPP_PASSWORD=your_password
XMPP_RECIPIENT=recipient@server.com
```

### ntfy (Push Notifications)

```bash
NTFY_URL=https://ntfy.sh/your-topic
NTFY_TOKEN=                              # optional — for private topics
```

Self-hosted ntfy and ntfy.sh both supported. No account needed for public topics.

### Email (SMTP)

```bash
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USERNAME=you@gmail.com
SMTP_PASSWORD=your-app-password
SMTP_FROM=you@gmail.com
SMTP_TO=you@gmail.com
```

### Alert Theme

```bash
# cyberpunk (default) or professional
ALERT_THEME=cyberpunk
```

---

## Advanced Configuration

### Custom Daemon Connection

Normally, the stratum auto-detects daemon connection settings from `POOL_COIN`. Override only if connecting to a remote daemon or non-standard ports:

```bash
# In .env (uncomment to override):
DAEMON_HOST=192.168.1.50
DAEMON_RPC_PORT=14022
DAEMON_RPC_USER=myrpcuser
DAEMON_RPC_PASSWORD=myrpcpassword
DAEMON_ZMQ_PORT=28532
STRATUM_PORT=3333
STRATUM_PORT_TLS=3335
```

### Prometheus Metrics Authentication

Protect the `/metrics` endpoint with a Bearer token:

```bash
SPIRAL_METRICS_TOKEN=your_secret_token
```

Configure Prometheus to use the token in `docker/config/prometheus/prometheus.yml`.

### Stratum Difficulty

Defaults work for most setups. Override for ASIC farms or specific hardware:

```bash
STRATUM_DIFF_INITIAL=5000              # Starting difficulty for unrecognized miners
STRATUM_DIFF_MIN=0.001                 # Lowest vardiff can drop (0.001 supports ESP32 lottery miners)
STRATUM_DIFF_MAX=1000000000000         # Highest vardiff can reach (1T supports S21 on BTC)
STRATUM_VARDIFF_TARGET_TIME=4          # Target seconds between shares
```

### AsicBoost / Version Rolling

Required for S19/S21/Vnish firmware. Enabled by default:

```bash
STRATUM_VERSION_ROLLING=true
STRATUM_VERSION_ROLLING_MASK=536862720   # Standard BIP320 mask
```

Set `STRATUM_VERSION_ROLLING=false` only if you need to disable AsicBoost for debugging.

### Dashboard Authentication

Enabled by default. Configure in `.env`:

```bash
DASHBOARD_AUTH_ENABLED=true
DASHBOARD_SESSION_LIFETIME=24    # hours
```

---

## Troubleshooting

### Pool shows 0 hashrate

1. Check if the blockchain daemon is fully synced:
   ```bash
   docker compose --profile dgb logs digibyte | tail -20
   ```
2. Check stratum is connected to daemon:
   ```bash
   docker compose --profile dgb logs stratum | grep -i "daemon\|rpc\|error"
   ```
3. Verify your miner is connected to the correct port.

### "POOL_COIN must be set" error

This occurs in single-coin mode (`POOL_MODE=single`). Edit `.env` and ensure `POOL_COIN` is uncommented and set to a valid coin name. In multi-coin mode (`POOL_MODE=multi`), `POOL_COIN` is not required — enable coins with `ENABLE_<COIN>=true` instead.

### "DB_PASSWORD must be set" error

Run `./generate-secrets.sh` to auto-generate all passwords.

### Daemon won't start

Check for port conflicts:
```bash
# Check if another service is using the same port
docker compose --profile dgb logs digibyte 2>&1 | head -20
```

### Containers keep restarting

```bash
# Check which container is failing
docker compose --profile dgb ps

# Check its logs
docker compose --profile dgb logs <service-name> --tail 50
```

### Reset everything (fresh start)

```bash
docker compose --profile dgb down -v    # Removes all volumes (DESTROYS blockchain data)
docker compose --profile dgb up -d      # Fresh start
```

---

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                    Docker Network                         │
│                                                           │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐               │
│  │ Daemon 1 │  │ Stratum  │  │ Dashboard│   Port 1618   │
│  │ (e.g.DGB)│──│  Server  │──│   (UI)   │───────────────┤
│  ├──────────┤  │          │  └──────────┘               │
│  │ Daemon 2 │──│          │  ┌──────────┐               │
│  │ (e.g.BTC)│  │          │──│ Sentinel │               │
│  ├──────────┤  │          │  │ (alerts) │               │
│  │ Daemon N │──│          │  └──────────┘               │
│  │ (multi)  │  │          │                              │
│  └──────────┘  │          │                              │
│  ┌──────────┐  │  :4000   │                              │
│  │PostgreSQL│──│          │  ┌──────────┐               │
│  │          │  └──────────┘  │Prometheus│   Port 9090   │
│  └──────────┘                │          │───────────────┤
│                              └──────────┘               │
│                              ┌──────────┐               │
│                              │ Grafana  │   Port 3000   │
│                              │          │───────────────┤
│                              └──────────┘               │
└──────────────────────────────────────────────────────────┘
```

Single-coin mode runs one daemon; multi-coin mode runs one daemon per enabled coin.

### Services

| Service | Purpose | Container Name |
|---------|---------|---------------|
| Daemon(s) | Blockchain node(s) — one per coin in single-coin, multiple in multi-coin | `spiralpool-<coin>` |
| Stratum | Mining pool server (Stratum V1 + TLS) | `spiralpool-stratum` |
| PostgreSQL | Database for shares, blocks, stats | `spiralpool-postgres` |
| Dashboard | Web UI at port 1618 | `spiralpool-dashboard` |
| Sentinel | Monitoring and alert notifications | `spiralpool-sentinel` |
| Prometheus | Metrics collection | `spiralpool-prometheus` |
| Grafana | Dashboards and alerting | `spiralpool-grafana` |

---

## Database High Availability (Optional)

> **BARE METAL / SELF-HOSTED VMs ONLY** — Cloud/VPS deployments are not supported. This Docker HA overlay is intended for self-hosted infrastructure where you control the hardware. HA on cloud providers (AWS, GCP, Azure, DigitalOcean, etc.) is not supported.

Docker supports PostgreSQL high availability via the `docker-compose.ha.yml` overlay. This provides automatic database failover using Patroni, etcd, and HAProxy.

### What HA Adds

| Component | Purpose |
|-----------|---------|
| etcd (3 nodes) | Distributed consensus for leader election |
| Patroni (2 nodes) | PostgreSQL primary + replica with automatic failover |
| HAProxy | Routes database connections to current leader |
| Redis | Cross-node share/block deduplication |

### Setup

```bash
# 1. Generate HA passwords (if not already done)
./generate-secrets.sh

# 2. Start with both profiles
docker compose -f docker-compose.yml -f docker-compose.ha.yml \
  --profile dgb --profile ha up -d
```

The stratum automatically connects to HAProxy instead of standalone PostgreSQL. On leader failure, Patroni promotes the replica within ~30 seconds, and the stratum reconnects automatically.

### Limitations

Database HA provides automatic PostgreSQL failover only. VIP failover (Keepalived) and multi-node stratum clustering are native-install features for bare-metal HA deployments.

---

## WSL2 Native Path *(Experimental — not recommended for production)*

> **WSL2 support is experimental.** Windows can terminate WSL2 without warning (updates, sleep, hibernate, memory pressure), systemd reliability is reduced, I/O is 2–4× slower due to the virtual disk, clocks drift after sleep, and HA is non-functional. Use this path for evaluation and development only. For 24/7 production mining, use native Ubuntu on dedicated hardware.

An alternative to Docker Desktop is running `install.sh` directly inside WSL2 Ubuntu. This gives broader feature support than Docker — multi-coin, Stratum V2, and merge mining work. Note that keepalived/VIP HA and multi-node clustering require native Linux and will not function correctly inside WSL2.

### Setup

```powershell
# 1. Install WSL2 + Ubuntu (PowerShell as Admin)
wsl --install -d Ubuntu
```

```bash
# 2. Open Ubuntu, then clone and run the Spiral Pool installer
git clone --depth 1 https://github.com/SpiralPool/Spiral-Pool.git
cd Spiral-Pool
./install.sh    # self-elevates; no sudo needed
```

### ASIC / External Miner Port Forwarding

WSL2 runs in NAT mode by default. Your Windows LAN IP (e.g. `192.168.1.x`) does **not** automatically reach the stratum server running inside WSL2. Software miners on the same Windows machine can use `127.0.0.1`, but **ASIC miners and external hardware require port forwarding**.

Use [`start-wsl2-proxy.bat`](../../scripts/windows/start-wsl2-proxy.bat) to manage this:

```
Double-click start-wsl2-proxy.bat → Run as Administrator
```

What it does:
- Installs WSL2 + Ubuntu if not already present
- Detects all installed WSL2 distros; lets you pick one if multiple exist
- Auto-detects your Windows LAN IP and WSL2 IP (with manual override)
- Warns and exits cleanly if WSL2 mirrored networking is active (portproxy not needed)
- Shows a coin selection menu (all 14 Spiral Pool coins, ALL, or custom port)
- Adds `netsh portproxy` rules: `0.0.0.0:PORT → WSL2_IP:PORT`
- Adds Windows Firewall inbound rules for the selected ports
- Checks whether systemd is enabled in WSL2; offers to enable it if not
- Optionally creates a Windows Task Scheduler task to start Spiral Pool services and re-apply portproxy rules automatically at every logon (elevated, hidden)
- Monitors the WSL2 IP every 60 seconds and re-applies rules if the IP drifts after a WSL2 restart
- Prints a heartbeat while running so you know it's alive
- **Removes portproxy rules on Ctrl+C** (closing the window does not clean up — use Ctrl+C to stop)

Point your ASIC at your Windows LAN IP with the standard stratum port (e.g. `192.168.1.161:20335` for QBX). The proxy handles the rest.

> **Note:** The proxy must be running whenever miners need to connect. Portproxy rules are ephemeral — a reboot or `wsl --shutdown` wipes them. Firewall rules and the Task Scheduler entry persist. The auto-start task re-applies portproxy rules at next logon.

---

## Upgrading to Native Installation

Docker is at full feature parity for single-node deployments — single-coin, multi-coin, merge mining, and Stratum V1/V2 all work. Native installation is only needed for multi-node HA with VIP failover (Keepalived).

To migrate:

1. Set up a dedicated Ubuntu 24.04 LTS server
2. Run `sudo ./install.sh` for native installation
3. Copy blockchain data from Docker volumes to avoid re-syncing (see [OPERATIONS.md](OPERATIONS.md))

See [OPERATIONS.md](OPERATIONS.md) for the full native installation guide.
