# Spiral Pool — Open-Source Self-Hosted Solo Mining Pool Software

<p align="center">
  <img src="assets/logo.png" alt="Spiral Pool Logo" width="400">
</p>

<p align="center">
  <strong>Self-Hosted Bitcoin &amp; Altcoin Mining Pool Software &mdash; Stratum V1/V2/TLS, SHA-256d &amp; Scrypt</strong><br>
  <em>Black Ice 1.0 &mdash; Convergent difficulty. Minimal oscillation.</em>
</p>

<p align="center">
  Free and Open Source &bull; BSD-3-Clause &bull; Non-Custodial &bull; Solo Mining &bull; Proof-of-Work
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-BSD--3--Clause-blue?style=flat-square" alt="License"></a>
  <a href="https://github.com/SpiralPool/Spiral-Pool/releases"><img src="https://img.shields.io/github/v/tag/SpiralPool/Spiral-Pool?style=flat-square&label=release&color=brightgreen&v=2" alt="Release"></a>
  <a href="https://github.com/SpiralPool/Spiral-Pool/stargazers"><img src="https://img.shields.io/github/stars/SpiralPool/Spiral-Pool?style=flat-square&color=yellow&v=2" alt="Stars"></a>
  <a href="https://x.com/SpiralMiner"><img src="https://img.shields.io/badge/𝕏-@SpiralMiner-black?style=flat-square&logo=x" alt="X"></a>
</p>

---

> **IMPORTANT NOTICE**: This software is provided "AS IS" without warranty of any kind. It has NOT been audited by third-party security professionals. **No security guarantee is made.** Operators are solely responsible for compliance with all applicable laws, their own security assessment, and all consequences of operating this software. See [LICENSE](LICENSE), [TERMS.md](TERMS.md), [WARNINGS.md](WARNINGS.md), and [SECURITY.md](SECURITY.md).

---

## What Is Spiral Pool?

Spiral Pool is **free, open-source, self-hosted Stratum mining pool software** for proof-of-work (PoW) cryptocurrencies &mdash; install it on your own bare-metal server, connect your ASIC miners (Antminer, Whatsminer, Avalon, BitAxe) directly, and every block reward goes straight to your wallet. No custodians. No middlemen. No cloud.

It implements a non-custodial solo mining architecture where block rewards are embedded directly in the coinbase transaction paying the **miner's own wallet address**. The fund flow is absolute: **Blockchain &rarr; Coinbase Transaction &rarr; Miner's Wallet.** There is no pool wallet, no intermediate balance, no fees, and no withdrawal process &mdash; the full block reward goes directly to the miner, and the software never holds, routes, or has access to funds at any point in the payment path.

At its core is the **Spiral Router** &mdash; a miner classification engine that reads 280+ device signatures at connection time and maps each miner to the right difficulty profile before a single share is submitted. Paired with a **lock-free vardiff engine** using per-session atomic state, asymmetric ramp limits (4x up / 0.75x down), and a 50% variance floor, difficulty spirals toward equilibrium rather than oscillating around a target.

In this documentation, "operator" means the individual or entity that installs and runs Spiral Pool on their own infrastructure. The Spiral Pool project does not operate pool infrastructure, provide hosted services, or have any relationship with miners connecting to operator-run pools.

14 coins. 2 algorithms. 6 merge-mining pairs. One binary.

---

## Key Features

| Feature | Details |
|---------|---------|
| **Spiral Router** | Classifies miners at connection time via 280+ user-agent patterns across 14 SHA-256d and 6 Scrypt difficulty profiles |
| **Lock-free vardiff** | Per-session atomic state, asymmetric limits (4x up / 0.75x down), 50% variance floor |
| **Multi-algorithm** | SHA-256d and Scrypt with dedicated difficulty profiles per algorithm |
| **Stratum V1 + V2 + TLS** | Multi-port per coin; Noise Protocol encryption for V2 |
| **Merge mining** | 6 AuxPoW pairs across BTC and LTC parent chains |
| **Non-custodial solo payout** | Block reward embedded in coinbase transaction to miner's wallet &mdash; no pool wallet, no intermediate custody |
| **High availability** | VIP failover, Patroni database replication, blockchain rsync, advisory lock payment fencing |
| **Spiral Sentinel** | Autonomous monitoring: device discovery, health checks, temperature alerts, block notifications |
| **Spiral Dash** | Real-time web dashboard with multi-theme support (port 1618) |
| **Share pipeline** | Lock-free ring buffer (1M capacity, MPSC) &rarr; WAL &rarr; PostgreSQL COPY batch insert |
| **Prometheus metrics** | Per-session observability with worker-level labels |
| **Runtime tuning** | Live operator control via `spiralctl` CLI |
| **3,500+ tests** | Unit, integration, chaos, and fuzz tests including 10 numbered chaos test suites |

---

## Compatible Mining Hardware

Spiral Pool supports any Stratum V1-compatible ASIC miner or GPU rig. The Spiral Router automatically classifies hardware at connection time using 280+ device signatures.

**SHA-256d (Bitcoin, DigiByte, Namecoin, and more):**
Antminer S9 / S17 / S19 / S19 Pro / S21 / S21 Pro, Whatsminer M20S / M30S / M50S / M60S, Avalon A1246 / A1346 / A1366, BitAxe Gamma / Ultra / Max, iBeLink BM-S1 Max, FutureBit Apollo BTC, NerdAxe, NerdQAxe, Compac F, LuckyMiner

**Scrypt (Litecoin, Dogecoin, PepeCoin, and more):**
Antminer L3+ / L7 / L9, Whatsminer M31S, Innosilicon A6+ LTC Master, FutureBit Apollo LTC

**Low-power / DIY / lottery miners:**
BitAxe (ESP32-S3 open-source ASIC), NerdAxe, NerdQAxe, Compac F, LuckyMiner, any Stratum V1-compatible firmware

> The Spiral Router identifies miner model, firmware, and hashrate class from the Stratum user-agent string. Unknown hardware falls back to a safe default profile automatically.

---

## Supported Coins

### SHA-256d

| Coin | Symbol | Block Time | Merge-Mined With |
|------|--------|------------|------------------|
| Bitcoin | BTC | 10 min | Parent chain |
| Bitcoin Cash | BCH | 10 min | &mdash; |
| DigiByte | DGB | 15 sec | &mdash; |
| Bitcoin II | BC2 | 10 min | &mdash; |
| Namecoin | NMC | 10 min | BTC (AuxPoW, chain ID 1) |
| Syscoin | SYS | 2.5 min | BTC (AuxPoW, chain ID 16) &mdash; merge-mining only |
| Myriad | XMY | 1 min | BTC (AuxPoW, chain ID 90) |
| Fractal Bitcoin | FBTC | 30 sec | BTC (AuxPoW, chain ID 8228) |
| Q-BitX | QBX | 2.5 min | &mdash; |

### Scrypt

| Coin | Symbol | Block Time | Merge-Mined With |
|------|--------|------------|------------------|
| Litecoin | LTC | 2.5 min | Parent chain |
| Dogecoin | DOGE | 1 min | LTC (AuxPoW, chain ID 98) |
| DigiByte-Scrypt | DGB-SCRYPT | 15 sec | &mdash; |
| PepeCoin | PEP | 1 min | LTC (AuxPoW, chain ID 63) |
| Catcoin | CAT | 10 min | &mdash; |

> **Note:** Syscoin (SYS) is merge-mining only. It requires a BTC parent chain and cannot solo mine due to CbTx/quorum commitment requirements.

### Merge Mining Topology

```
BTC ──┬── NMC  (Namecoin)         LTC ──┬── DOGE (Dogecoin)
      ├── SYS  (Syscoin)                └── PEP  (PepeCoin)
      ├── XMY  (Myriad)
      └── FBTC (Fractal Bitcoin)

QBX (standalone — no merge mining)
```

---

## Architecture at a Glance

```
                       ┌────────────────────────────────────────────────┐
  Miners               │              Spiral Pool Node                  │
  ┌──────┐  Stratum    │                                                │
  │BitAxe├──V1/V2/TLS─►│  Spiral Router ──► VarDiff Engine              │
  ├──────┤             │       │                  │                     │
  │ S21  ├──►          │       ▼                  ▼                     │
  ├──────┤             │  Share Validation ──► Ring Buffer (1M)         │
  │ESP32 ├──►          │       │                  │                     │
  └──────┘             │       ▼                  ▼                     │
                       │  Block Submit        WAL ──► PostgreSQL        │
                       │       │                                        │
                       │       ▼                                        │
                       │  Coin Daemons (RPC + ZMQ)   Prometheus :9100   │
                       │                                                │
                       │  Sentinel ◄──► Dashboard :1618 ◄──► API :4000  │
                       └────────────────────────────────────────────────┘
```

---

## Who This Is For — Solo Miners &amp; Pool Operators

- **Solo miners** running dedicated ASIC hardware who want full control over their pool infrastructure
- **Home miners** with diverse hardware (ESP32 lottery miners, BitAxe, Avalon, Antminer) on the same pool
- **Pool operators** who need complete vardiff visibility and runtime tuning capability

## Who This Is Not For

- Operators seeking managed pool services or turnkey SaaS solutions
- Users without Linux system administration experience
- Mining operations requiring proportional payout splitting (Spiral Pool is solo-only)
- **Cloud/VPS deployments** &mdash; Spiral Pool is **NOT** supported on any cloud provider (AWS, Azure, GCP, DigitalOcean, Hetzner, Vultr, Linode, OVH, etc.). The installer blocks cloud deployments. You do not own the physical hardware on cloud infrastructure, which can expose wallet credentials, private keys, and all operational data to the hosting provider. See [WARNINGS.md](WARNINGS.md)

---

## Platform Support

| Platform | Status | Notes |
|----------|--------|-------|
| **Ubuntu 24.04.x LTS** (Noble Numbat) | **Primary** | Native installation (recommended). Docker available separately. **x86_64 (amd64) only.** |
| **Windows 11 (Docker)** | **Experimental** | Docker Desktop with WSL2 required. Not for production. See [Docker Guide](docs/setup/DOCKER_GUIDE.md). |
| **ARM / Raspberry Pi** | **Not Tested/Experimental** | All packages and binaries target x86_64. ARM may not work. See [WARNINGS.md](WARNINGS.md). |

---

## Quick Start

> **New to server setup?** See the [Server Preparation Guide](docs/setup/OPERATIONS.md#0-server-preparation--ubuntu-2404x-lts-noble-numbat) for step-by-step Ubuntu 24.04.x LTS installation and first-login instructions.

### Prerequisites

- Ubuntu Server 24.04.x LTS (minimized)
- x86_64 (amd64) architecture
- 10 GB RAM minimum (16 GB recommended)
- 150 GB SSD minimum (Bitcoin: ~600 GB, DigiByte: ~45 GB &mdash; see [Storage Requirements](docs/setup/OPERATIONS.md#2-storage-requirements))
- IPv4 network (IPv6 not supported)
- Bare metal or self-hosted VM &mdash; **no cloud/VPS**

```bash
sudo apt-get -y update && sudo apt-get -y upgrade
```

```bash
sudo apt-get -y install git    # or unzip for ZIP archives
```

### Install

**Option A &mdash; Git clone:**

```bash
git clone https://github.com/SpiralPool/Spiral-Pool.git
cd Spiral-Pool && ./install.sh
```

**Option B &mdash; ZIP archive:**

```bash
unzip Spiral-Pool.zip
cd Spiral-Pool && ./install.sh
```

The installer handles everything: coin daemon(s), PostgreSQL, Go toolchain, stratum compilation, TLS certificates, systemd services, firewall rules, and monitoring stack. Checkpoint resume means a failed install can be re-run safely.

### Connect Your Miners

```
URL:      stratum+tcp://YOUR_SERVER_IP:PORT
Worker:   YOUR_WALLET_ADDRESS.worker_name
Password: x
```

See [REFERENCE.md](docs/reference/REFERENCE.md) for all coin-specific stratum ports.

---

## Notifications

Spiral Sentinel supports real-time alerts via **Discord**, **Telegram**, and **XMPP/Jabber** for block discoveries, miner status changes, temperature warnings, and periodic hashrate reports.

**Telegram:** Message [@BotFather](https://t.me/BotFather) to create a bot &rarr; [@userinfobot](https://t.me/userinfobot) for your chat ID &rarr; add bot to your channel.

**Discord:** Server Settings &rarr; Integrations &rarr; Webhooks &rarr; Create webhook &rarr; copy URL.

**XMPP/Jabber:** Configure JID, password, and recipient in Sentinel config. Requires optional `slixmpp` package.

Enter credentials during installation or configure in `~/.spiralsentinel/config.json`.

---

## Docker Deployment (Optional)

> **Note:** Docker is for **Windows/WSL2 only**. Native installation via `./install.sh` provides the best performance with zero container I/O overhead. Do not use Docker on bare metal or VM installations.

Docker supports **V1 single-coin solo mining** with dashboard, Sentinel monitoring, Prometheus, and Grafana. 14 configurations available: DGB, BTC, BCH, BC2, NMC, SYS, XMY, FBTC, QBX, LTC, DOGE, DGB-SCRYPT, PEP, CAT.

```bash
cd docker
cp .env.example .env         # Configure POOL_COIN and POOL_ADDRESS
./generate-secrets.sh        # Auto-generate all passwords
docker compose --profile dgb up -d
```

For multi-coin, merge mining, Stratum V2, or full HA &mdash; use native installation (`./install.sh`).

See [DOCKER_GUIDE.md](docs/setup/DOCKER_GUIDE.md) for the complete guide including WSL2 setup and database HA overlay.

---

## Blockchain Replication

When deploying a second node (HA standby or additional instance), you can copy blockchain data from an existing Spiral Pool node instead of downloading from the P2P network. The installer offers this during the "Blockchain Data Synchronization" step. Post-installation, use `ha-replicate.sh` for on-demand replication of blockchain data, PostgreSQL data, or both.

See [OPERATIONS.md](docs/setup/OPERATIONS.md) for complete blockchain replication instructions, SSH key setup, and safety details.

---

## Documentation

### Setup &amp; Operations

| Document | Description |
|----------|-------------|
| [OPERATIONS.md](docs/setup/OPERATIONS.md) | Installation, configuration, monitoring, HA setup, upgrading, troubleshooting |
| [DOCKER_GUIDE.md](docs/setup/DOCKER_GUIDE.md) | Docker &amp; WSL2 deployment guide |

### Architecture

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](docs/architecture/ARCHITECTURE.md) | Spiral Router, vardiff engine, share pipeline, database schema, HA, Prometheus metrics |
| [SECURITY_MODEL.md](docs/architecture/SECURITY_MODEL.md) | FSM enforcement, JSON hardening, rate limiting, TLS, payment fencing |

### Reference

| Document | Description |
|----------|-------------|
| [REFERENCE.md](docs/reference/REFERENCE.md) | Ports, CLI commands, API endpoints, miner classes, configuration fields |
| [spiralctl-reference.md](docs/reference/spiralctl-reference.md) | Complete spiralctl CLI &mdash; all commands, options, examples |
| [MINER_SUPPORT.md](docs/reference/MINER_SUPPORT.md) | Mining hardware support: device APIs, auto-detection, monitoring |
| [EXTERNAL_ACCESS.md](docs/reference/EXTERNAL_ACCESS.md) | Port forwarding, Cloudflare tunnels, hashrate marketplace integration |

### Development

| Document | Description |
|----------|-------------|
| [TESTING.md](docs/development/TESTING.md) | 3,500+ tests: unit, integration, chaos, fuzz test suites |
| [COIN_ONBOARDING_SPEC.md](docs/development/COIN_ONBOARDING_SPEC.md) | Adding new coin support |

---

## Community

- [@SpiralMiner](https://x.com/SpiralMiner) &mdash; Release announcements and project updates
- [GitHub Issues](https://github.com/SpiralPool/Spiral-Pool/issues) &mdash; Bug reports and feature requests
- [GitHub Discussions](https://github.com/SpiralPool/Spiral-Pool/discussions) &mdash; Questions and community discussion

---

## Acknowledgments

Special thanks to **Hydden** ❤️, and **Xphox** ❤️ for their suggestions, feedback, throughout development. Your encouragement helped shape Spiral Pool into what it is today.

This implementation follows the [Stratum V2 Specification](https://github.com/stratum-mining/sv2-spec) for V2 protocol support and uses the [Noise Protocol Framework](https://noiseprotocol.org/) for encryption. Block template handling follows BIP 22/23 specifications.

---

## Donations

Spiral Pool is and always will be **free, open-source software** &mdash; fully yours to run, modify, and control. No paywalls, no premium tiers, no strings attached. BSD-3-Clause, forever.

If you find this project useful and want to support continued development, donations are appreciated but **never expected or required**. Thank you to everyone who has contributed feedback, testing, and support &mdash; it means the world.

| Coin | Address |
|------|---------|
| Bitcoin (BTC) | `bc1qnmps0ga6ms3lsd0f6zsm94mq44slgnac8w5fjj` |
| Bitcoin Cash (BCH) | `bitcoincash:qp2wmc5u0ehfglf2n7prsyc97l4hyetu8su8k76ztq` |
| DigiByte (DGB) | `DAjLRZ4ZsbUcLFFtf3GGbEKWmakNTLh6aq` |


> **Notice:** Donations are entirely voluntary, unconditional, and irrevocable gifts received by individual maintainers in their personal capacity. No services, features, priority support, contractual relationship, or other consideration of any kind is provided in exchange for donations. Donations do not create any commercial, contractual, or service relationship between you and the project maintainers. Spiral Pool is self-hosted, non-custodial software licensed under BSD-3-Clause &mdash; the project does not hold, manage, or have access to user funds at any time. Cryptocurrency transactions are irreversible; no refunds are possible. Recipients of donations may have tax reporting obligations depending on their jurisdiction; local tax laws may apply to both donors and recipients. This is not financial, legal, or tax advice.

---

## Disclaimer

**THIS SOFTWARE IS PROVIDED "AS IS" WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED.** This is software, not a service &mdash; no hosted infrastructure, managed platform, or financial product is provided.

Use at your own risk. The authors and contributors make no representations or warranties regarding:
- Security, reliability, or fitness for any particular purpose
- Accuracy of documentation or described functionality
- Legal compliance in any jurisdiction
- Suitability for any specific mining operation

**You are solely responsible for:**
- Compliance with all applicable laws in your jurisdiction, including regulations on cryptocurrency mining, network privacy tools (Tor), financial reporting, and data protection
- Determining whether this software is legal to operate in your jurisdiction
- Determining whether your pool operation triggers any financial regulatory obligations (see [WARNINGS.md](WARNINGS.md))
- Securing your systems, wallets, and credentials
- Any financial losses, hardware damage, or legal consequences arising from use of this software
- Conducting your own security assessment before production deployment
- **Verifying disk contents before confirming disk formatting during installation** &mdash; the installer can format unformatted disks as ext4 for blockchain storage; formatting permanently destroys all data on the selected device and cannot be undone (see [WARNINGS.md](WARNINGS.md))

**The authors accept no liability for damages of any kind, including but not limited to:**
- Direct, indirect, incidental, special, or consequential damages
- Loss of profits, data, cryptocurrency, or business opportunities
- Legal fees, regulatory fines, or compliance costs

**This is not legal, financial, or tax advice.** Consult qualified professionals for your specific situation.

## Legal

| Document | Description |
|----------|-------------|
| [LICENSE](LICENSE) | BSD-3-Clause License |
| [TERMS.md](TERMS.md) | Terms of Use (arbitration, governing law, class action waiver) |
| [WARNINGS.md](WARNINGS.md) | Specific Hazard Warnings (financial, security, legal, operational) |
| [PRIVACY.md](PRIVACY.md) | Privacy Notice (GDPR/CCPA/PIPEDA guidance) |
| [SECURITY.md](SECURITY.md) | Security Policy, Incident Response |
| [EXPORT.md](EXPORT.md) | Export Control and Sanctions Notice (Canada/U.S./EU) |
| [TRADEMARKS.md](TRADEMARKS.md) | Third-Party Trademark Notice |
| [NOSEC.md](NOSEC.md) | Security Architecture Decisions |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Contribution Guidelines (DCO, irrevocable license grant) |

All product names, logos, and brands mentioned in this documentation are property of their respective owners. Use of these names does not imply endorsement. See [TRADEMARKS.md](TRADEMARKS.md).

This project is licensed under BSD-3-Clause. See [LICENSE](LICENSE) and [THIRD_PARTY_LICENSES.txt](THIRD_PARTY_LICENSES.txt) for complete licensing information.

---

*Spiral Pool &mdash; Black Ice 1.0 &mdash; Convergent difficulty. Minimal oscillation.*

---

**Contact:** spiralpool@proton.me | Discord: Fibonacc#1618
