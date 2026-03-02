# Spiral Pool - Docker & WSL2 Setup Guide

Complete guide for running Spiral Pool in Docker containers. Covers Linux Docker and Windows/WSL2 deployments.

---

## What Docker Supports

Docker supports **V1 single-coin solo mining** only:

- One cryptocurrency per deployment
- Stratum V1 protocol (plain + TLS encrypted connections)
- 13 supported configurations: DGB, BTC, BCH, BC2, NMC, SYS, XMY, FBTC, LTC, DOGE, DGB-SCRYPT, PEP, CAT
- Dashboard, Sentinel monitoring, Prometheus, and Grafana included
- Self-signed TLS certificates auto-generated

**Partially supported in Docker:**

- Database HA (Patroni/etcd/HAProxy/Redis) via `docker-compose.ha.yml` overlay — automatic PostgreSQL failover

**Not supported in Docker** (requires native `sudo ./install.sh`):

- V2 Enhanced Stratum (SV2 binary protocol with Noise encryption)
- Multi-coin mining (12 coins simultaneously)
- Merge mining (BTC+NMC, BTC+FBTC, BTC+SYS, BTC+XMY, LTC+DOGE, LTC+PEP)
- Full HA with VIP failover (Keepalived) and multi-node stratum

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

### ARM64 / aarch64 (Raspberry Pi, Apple Silicon, AWS Graviton)

> **EXPERIMENTAL / UNTESTED** — ARM64 Docker support is provided on a best-effort
> basis. Upstream coin projects publish arm64 binaries and Docker will automatically
> pull the correct architecture, but this has **not been tested** by the Spiral Pool
> team. Known limitations:
>
> - **Fractal Bitcoin (FBTC)** has no arm64 binary and will fail to build on ARM.
> - Go compilation and stratum binary compatibility are unverified on ARM.
> - Performance characteristics are unknown on ARM hardware.
> - 11 of 12 coin images include arm64 conditionals; FBTC is x86_64-only.
>
> If you encounter issues on ARM64, please report them with your architecture
> (`dpkg --print-architecture`) and full error output.

### Windows (WSL2)

- Windows 10/11 with WSL2 enabled
- Docker Desktop with WSL2 backend
- Ubuntu distribution installed in WSL2

```powershell
# 1. Install WSL2 (PowerShell as Admin)
wsl --install -d Ubuntu

# 2. Install Docker Desktop from https://www.docker.com/products/docker-desktop/
#    Enable WSL2 backend in Docker Desktop > Settings > General

# 3. Open Ubuntu WSL2 terminal for all remaining steps
```

**WSL2 limitations:**

- Blockchain sync takes 2-4x longer than native Linux (I/O overhead)
- Bridge networking only (no host mode)
- Not recommended for production mining
- Best for: development, testing, pool evaluation

---

## Quick Start (3 Steps)

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

Valid `POOL_COIN` values: `digibyte`, `dgb-scrypt` (or `digibyte-scrypt`), `bitcoin`, `litecoin`, `bitcoincash`, `bitcoinii`, `dogecoin`, `pepecoin`, `catcoin`, `namecoin`, `syscoin`, `myriadcoin`, `fractalbitcoin`

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
| Litecoin | `ltc_scrypt_1` |
| Dogecoin | `doge_scrypt_1` |
| PepeCoin | `pep_scrypt_1` |
| Catcoin | `cat_scrypt_1` |

### Step 2: Generate Passwords

```bash
./generate-secrets.sh
```

This auto-generates unique random passwords for:
- All coin daemon RPC credentials (12 daemons; DGB-SCRYPT shares the DGB daemon)
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

Available profiles:

| Profile | Coin | Algorithm |
|---------|------|-----------|
| `dgb` | DigiByte | SHA-256d |
| `btc` | Bitcoin | SHA-256d |
| `bch` | Bitcoin Cash | SHA-256d |
| `bc2` | Bitcoin II | SHA-256d |
| `nmc` | Namecoin | SHA-256d |
| `sys` | Syscoin (daemon sync only — mining requires native install, see note) | SHA-256d |
| `xmy` | Myriadcoin | SHA-256d |
| `fbtc` | Fractal Bitcoin | SHA-256d |
| `ltc` | Litecoin | Scrypt |
| `doge` | Dogecoin | Scrypt |
| `dgb-scrypt` | DigiByte (Scrypt) | Scrypt |
| `pep` | PepeCoin | Scrypt |
| `cat` | Catcoin | Scrypt |

> **Syscoin note:** Syscoin is merge-mining only (requires BTC parent chain) and cannot solo mine. Docker does not support merge mining. The `sys` profile syncs the Syscoin daemon only — actual Syscoin mining requires native installation (`sudo ./install.sh`).

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

| Coin | Stratum V1 | Stratum TLS |
|------|-----------|-------------|
| DigiByte | 3333 | 3335 |
| DigiByte-Scrypt | 3336 | 3338 |
| Bitcoin | 4333 | 4335 |
| Bitcoin Cash | 5333 | 5335 |
| Bitcoin II | 6333 | 6335 |
| Litecoin | 7333 | 7335 |
| Dogecoin | 8335 | 8342 |
| PepeCoin | 10335 | 10337 |
| Catcoin | 12335 | 12337 |
| Namecoin | 14335 | 14337 |
| Syscoin | 15335 | 15337 |
| Myriadcoin | 17335 | 17337 |
| Fractal BTC | 18335 | 18337 |

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
# All services
docker compose --profile dgb logs -f

# Specific service
docker compose --profile dgb logs -f stratum
docker compose --profile dgb logs -f digibyte
docker compose --profile dgb logs -f dashboard
docker compose --profile dgb logs -f sentinel
```

### Check Status

```bash
docker compose --profile dgb ps
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

Edit `.env` and ensure `POOL_COIN` is uncommented and set to a valid coin name.

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
┌─────────────────────────────────────────────────────────┐
│                    Docker Network                        │
│                                                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│  │  Daemon   │  │ Stratum  │  │ Dashboard│   Port 1618  │
│  │ (1 coin)  │──│  Server  │──│   (UI)   │──────────────┤
│  │           │  │          │  └──────────┘              │
│  └──────────┘  │          │  ┌──────────┐              │
│                │          │──│ Sentinel │              │
│  ┌──────────┐  │          │  │ (alerts) │              │
│  │PostgreSQL│──│          │  └──────────┘              │
│  │          │  │  :4000   │                             │
│  └──────────┘  └──────────┘  ┌──────────┐              │
│                              │Prometheus│   Port 9090  │
│                              │          │──────────────┤
│                              └──────────┘              │
│                              ┌──────────┐              │
│                              │ Grafana  │   Port 3000  │
│                              │          │──────────────┤
│                              └──────────┘              │
└─────────────────────────────────────────────────────────┘
```

### Services

| Service | Purpose | Container Name |
|---------|---------|---------------|
| Daemon | Blockchain node (one per deployment) | `spiralpool-<coin>` |
| Stratum | Mining pool server (Stratum V1 + TLS) | `spiralpool-stratum` |
| PostgreSQL | Database for shares, blocks, stats | `spiralpool-postgres` |
| Dashboard | Web UI at port 1618 | `spiralpool-dashboard` |
| Sentinel | Monitoring and alert notifications | `spiralpool-sentinel` |
| Prometheus | Metrics collection | `spiralpool-prometheus` |
| Grafana | Dashboards and alerting | `spiralpool-grafana` |

---

## Database High Availability (Optional)

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

Database HA does **not** provide stratum VIP failover (Keepalived) or multi-node stratum instances. For full HA with VIP failover, use native installation (`sudo ./install.sh`).

---

## Upgrading to Native Installation

When you're ready for production mining with full features:

1. Set up a dedicated Ubuntu 24.04 LTS server
2. Run `sudo ./install.sh` for native installation
3. Native install supports all features: V2 Stratum, multi-coin, merge mining, HA

See [OPERATIONS.md](OPERATIONS.md) for the full native installation guide.
