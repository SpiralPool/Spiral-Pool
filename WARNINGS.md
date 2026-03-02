# Specific Hazard Warnings

**Last Updated:** 2026-02-14

**READ THIS DOCUMENT BEFORE DEPLOYING SPIRAL POOL**

This document contains specific warnings about hazards associated with operating cryptocurrency mining pool software. These warnings supplement the general disclaimers in LICENSE and TERMS.md.

---

## FINANCIAL LOSS HAZARDS

### Direct Financial Loss

**WARNING: YOU CAN LOSE MONEY**

Operating mining pool software involves direct financial risk:

| Hazard | Description | Potential Loss |
|--------|-------------|----------------|
| **Configuration errors** | Incorrect wallet address, wrong coin parameters | 100% of mined rewards |
| **Software bugs** | Share validation errors, block submission failures | Partial to complete reward loss |
| **Network issues** | Disconnections during block submission | Individual block rewards |
| **Database corruption** | Share data loss, accounting errors | Disputed or lost payouts |
| **Security breaches** | Unauthorized access, wallet compromise | Complete wallet balance |

**YOU ARE SOLELY RESPONSIBLE FOR:**
- Verifying all wallet addresses before deployment
- Testing configurations in safe environments
- Maintaining backups of all critical data
- Monitoring operations continuously
- Securing access to your systems

### Electricity and Hardware Costs

**WARNING: MINING COSTS MAY EXCEED REWARDS**

- Mining profitability fluctuates with cryptocurrency prices
- Electricity costs continue regardless of mining success
- Hardware depreciation and failure are operator responsibilities
- Pool software cannot guarantee profitability

---

## SECURITY HAZARDS

### Network Exposure

**WARNING: STRATUM PORTS MAY BE EXPOSED TO UNTRUSTED NETWORKS**

Spiral Pool is **not** designed to be deployed directly on the open internet. It is designed to operate on **fully operator-controlled infrastructure** — dedicated hardware on a private network, behind a properly configured firewall, with only the minimum necessary ports forwarded.

**Recommended deployment:**
- Bare-metal or dedicated hardware under your physical control
- Private subnet or segmented network, isolated from other services
- Firewall with default-deny policy; only stratum port(s) forwarded as needed (e.g., for rented hashrate)
- All management interfaces (API, metrics, database, RPC) restricted to local/VPN access only

**Not supported — cloud deployments are blocked by the installer where detectable:**
- **Any cloud provider** (AWS, Azure, Google Cloud, DigitalOcean, Hetzner, Vultr, Linode, OVH, Scaleway, UpCloud, Alibaba Cloud, Tencent Cloud, Oracle Cloud, or any other cloud/VPS/IaaS provider) — see **Cloud Deployment** section below

**Not recommended:**
- Exposing management ports (API, Prometheus, PostgreSQL, daemon RPC) to any untrusted network
- Running without a firewall or on a flat network shared with other services

When stratum ports **are** forwarded to accept external miners (e.g., rented hashrate from NiceHash or similar), the following hazards apply:

| Hazard | Risk Level | Potential Impact |
|--------|------------|------------------|
| **DDoS attacks** | HIGH | Service disruption, lost mining time |
| **Exploitation attempts** | HIGH | System compromise, data theft |
| **Malicious miners** | MEDIUM | Resource abuse, invalid shares |
| **Protocol attacks** | MEDIUM | Share manipulation, difficulty gaming |

**STRONGLY RECOMMENDED MITIGATIONS:**
- Firewall with default-deny; only stratum port(s) open, and only when actively needed
- Rate limiting enabled in pool configuration
- Regular security updates for OS, daemons, and pool software
- Log monitoring and alerting (Sentinel, Prometheus)
- Intrusion detection systems
- Network isolation for databases and daemon RPC — never expose these externally
- VPN or SSH tunnel for all administrative access

### Cloud Deployment

**WARNING: CLOUD DEPLOYMENT IS NOT SUPPORTED**

Spiral Pool is **NOT** designed, supported, or recommended for deployment on cloud-based instances, VPS (Virtual Private Server), IaaS (Infrastructure as a Service), or any shared hosting environment. **The installer will block installation when a cloud provider is detected.**

Cloud providers that are automatically detected and blocked include: AWS EC2, Microsoft Azure, Google Cloud, DigitalOcean, Linode/Akamai, Hetzner Cloud, Vultr, OVHcloud, Scaleway, UpCloud, Alibaba Cloud, and Tencent Cloud. Other cloud providers (including Oracle Cloud) may not be automatically detected but are equally unsupported.

| Risk | Description |
|------|-------------|
| **No physical control** | You do not own the hardware, hypervisor, or network infrastructure |
| **Provider access** | The cloud provider has unrestricted access to your server's memory, disk, and network traffic — including private keys, wallet credentials, and database contents |
| **Snapshotting** | Cloud instances can be snapshotted, cloned, or inspected by the hosting provider without your knowledge or consent |
| **No data confidentiality** | Shared infrastructure provides no guarantee of data confidentiality or physical security |
| **Side-channel attacks** | Shared hardware exposes you to side-channel attacks and noisy-neighbor resource contention |

**Why this matters for mining pools:**
- Wallet private keys stored on cloud infrastructure are accessible to the hosting provider
- Cryptocurrency daemon RPC credentials are stored in plaintext configuration files on disk
- Database contents (shares, block data, payout records) are not encrypted at rest by default
- Network traffic between stratum and daemons contains sensitive operational data

**Supported deployment targets:**
- Bare metal servers under your physical control
- Virtual machines running on hypervisors **you own and operate** (e.g., your own Proxmox, VMware ESXi, or KVM host)

**Cloud deployments receive NO SUPPORT.** Issues arising from cloud-hosted installations will not be investigated, and bug reports from cloud environments may be closed without investigation.

### ARM Architecture

**WARNING: ARM ARCHITECTURE HAS NOT BEEN TESTED**

Spiral Pool has **NOT** been tested on ARM architecture (including Raspberry Pi, ARM64/aarch64, and ARMv7). All packages, build references, and binary dependencies target **x86_64 (amd64)** on Ubuntu 24.04 LTS.

| Concern | Details |
|---------|---------|
| **Package availability** | Ubuntu 24.04 LTS packages may differ or be unavailable on ARM |
| **Go compilation** | Cross-compilation and binary compatibility are not verified |
| **Daemon binaries** | Cryptocurrency daemon binaries (Bitcoin Core, Litecoin, etc.) may not be available for ARM |
| **Performance** | Performance characteristics are unknown on ARM hardware |

The installer will detect ARM architecture and display a warning. You may choose to continue at your own risk, but ARM deployments are **NOT supported** and issues arising from ARM-based installations may not be investigated.

### No Security Audit

**WARNING: THIS SOFTWARE HAS NOT BEEN PROFESSIONALLY AUDITED**

- No third-party security review has been conducted
- Undiscovered vulnerabilities may exist
- The security measures described in documentation are design intentions, not guarantees
- You must conduct your own security assessment

### Credential Exposure

**WARNING: CREDENTIALS MAY BE EXPOSED**

Spiral Pool supports **environment variable overrides** for all sensitive credentials, allowing you to keep secrets out of configuration files entirely. The following environment variables are recognized:

| Environment Variable | Purpose |
|----------------------|---------|
| `SPIRAL_DATABASE_USER` | PostgreSQL username |
| `SPIRAL_DATABASE_PASSWORD` | PostgreSQL password |
| `SPIRAL_ADMIN_API_KEY` | Admin API authentication key |
| `SPIRAL_METRICS_TOKEN` | Prometheus metrics endpoint token |
| `SPIRAL_{COIN}_DAEMON_USER` | Per-coin daemon RPC username (e.g., `SPIRAL_BTC_DAEMON_USER`) |
| `SPIRAL_{COIN}_DAEMON_PASSWORD` | Per-coin daemon RPC password |
| `SPIRAL_DISCORD_WEBHOOK_URL` | Discord alert webhook URL |
| `SPIRAL_TELEGRAM_BOT_TOKEN` | Telegram bot token for alerts |
| `SPIRAL_TELEGRAM_CHAT_ID` | Telegram chat ID for alerts |

When set, environment variables take precedence over values in the configuration file.

| Location | Risk | Mitigation |
|----------|------|------------|
| Configuration files | Readable by local users | Restrict file permissions (`chmod 600`), or use environment variables instead |
| Log files | May contain sensitive data | Secure log storage, rotation, restrict access |
| Database | Contains operational data | Network isolation, authentication, never expose externally |

---

## LEGAL AND REGULATORY HAZARDS

### Cryptocurrency Regulation

**WARNING: CRYPTOCURRENCY MINING MAY BE REGULATED OR PROHIBITED IN YOUR JURISDICTION**

| Jurisdiction Type | Potential Requirements |
|-------------------|----------------------|
| Mining prohibited | Complete prohibition of operations |
| Licensed activity | Business license, registration |
| Tax reporting | Income reporting, capital gains |
| Energy regulations | Usage limits, reporting requirements |

**YOU MUST:**
- Research applicable laws BEFORE deployment
- Consult qualified legal counsel
- Obtain any required licenses or permits
- Comply with tax reporting obligations
- Implement required compliance measures

### Tor Network Functionality

**WARNING: TOR USAGE MAY BE ILLEGAL IN SOME JURISDICTIONS**

This software includes optional Tor (The Onion Router) functionality for privacy purposes. The Tor feature is provided for **legitimate privacy use only**.

**Jurisdictional compliance:** The use of Tor is legal in most countries, but may be restricted, monitored, or prohibited in some jurisdictions. **You** are solely responsible for determining the legality of Tor usage in your jurisdiction **before** enabling this feature. Nothing in this software or its documentation constitutes legal advice regarding Tor. If you are unsure about the legality of using Tor in your jurisdiction, consult with a qualified legal professional.

Tor usage is:
- **Prohibited** in some countries
- **Monitored** in many jurisdictions
- **Restricted** for certain uses

Using Tor does not guarantee anonymity and may attract additional scrutiny.

**Client-only mode:** This software configures Tor as a **client only**. It does NOT:
- Operate as a Tor relay, exit node, or bridge
- Route traffic for other Tor users
- Provide anonymity services to third parties

The authors and contributors:
- Make **no representations** about the legality of Tor in any jurisdiction
- Accept **no liability** for any illegal use of this software or its Tor feature
- Provide this feature AS-IS with absolutely **no warranty** of any kind
- Are **not responsible** for any legal consequences arising from your use of Tor

**By enabling or using the Tor functionality, you acknowledge that you have reviewed applicable laws in your jurisdiction, accept full responsibility for your use of the Tor feature, and confirm that you will use this feature only for lawful purposes.** See also TERMS.md Section 5A.

### Export Control

**WARNING: CRYPTOGRAPHIC SOFTWARE MAY BE SUBJECT TO EXPORT CONTROLS**

- This software contains cryptographic functionality
- Export to certain countries may violate U.S. law
- Re-export by users may require compliance measures
- See EXPORT.md for detailed information

### Money Transmission / Money Services Business

**Spiral Pool is a SOLO mining pool with a direct, non-custodial payout architecture.** Pooled payout schemes (PPLNS, PPS, PROP, etc.) are not supported and will never be implemented.

- Block rewards are embedded directly in the coinbase transaction, paying the **miner's own wallet address**
- The pool operator **never takes custody, control, or possession** of miner funds at any stage
- There is no operator wallet that collects rewards for later distribution
- The fund flow is: **Blockchain → Coinbase Transaction → Miner's Wallet** (no intermediary)

Under this architecture, the operator provides **computational coordination infrastructure** (difficulty adjustment, share validation, block template construction, block submission) — not financial services. Because no funds are accepted, held, controlled, or transmitted on behalf of another person, solo pool operation **may not constitute money transmission** in most jurisdictions.

However, regulatory interpretation varies. The authors make no legal determination regarding your specific situation.

**WARNING: IF YOU MODIFY THIS SOFTWARE TO OPERATE AS A TRADITIONAL POOL** (collecting rewards into an operator wallet and distributing payouts to miners), **this may constitute money transmission** and you are solely responsible for regulatory compliance. This software does not include AML/KYC features.

#### Recommendation for All Operators

**YOU SHOULD** consult with a qualified financial regulatory attorney in your jurisdiction to confirm whether your specific pool deployment triggers any registration or compliance obligations **BEFORE** accepting miners. Regulatory frameworks evolve, and interpretations may vary.

---

## OPERATIONAL HAZARDS

### Data Loss

**WARNING: DATA LOSS CAN AND WILL OCCUR**

| Cause | Data at Risk | Recovery |
|-------|--------------|----------|
| Software crash | In-memory shares, pending blocks | Partial via WAL |
| Database failure | Historical data, accounting | From backups only |
| Disk failure | All local data | From backups only |
| Configuration error | Operational state | Manual recovery |

**STRONGLY RECOMMENDED PROTECTIONS:**
- Regular automated backups
- Backup verification testing
- Offsite backup storage
- Documented recovery procedures

### Availability

**WARNING: 100% UPTIME IS NOT POSSIBLE**

- Software requires restarts for updates
- Hardware failures will occur
- Network outages affect operations
- Database maintenance requires downtime

Plan for outages. Implement high availability if continuous operation is required.

### Resource Exhaustion

**WARNING: RESOURCE EXHAUSTION CAN CAUSE FAILURES**

| Resource | Symptom | Prevention |
|----------|---------|------------|
| Memory | OOM kills, crashes | Monitor usage, set limits |
| Disk space | Write failures, corruption | Monitor capacity, rotate logs |
| File descriptors | Connection failures | Increase limits per documentation |
| CPU | Slow response, timeouts | Adequate provisioning |
| Network bandwidth | Dropped connections | Sufficient capacity |

---

## THIRD-PARTY HAZARDS

### Blockchain Node Dependencies

**WARNING: THIS SOFTWARE DEPENDS ON EXTERNAL CRYPTOCURRENCY NODES**

- Node failures affect pool operations
- Node bugs can cause invalid blocks
- Chain reorganizations can invalidate work
- Consensus changes require updates

### Hardware Dependencies

**WARNING: MINING HARDWARE MAY MALFUNCTION**

- Pool software sends commands to mining hardware
- Hardware damage from any cause is operator responsibility
- Firmware bugs in mining hardware are outside our control
- Warranty implications of third-party control software vary

---

## CRYPTOCURRENCY-SPECIFIC HAZARDS

### Price Volatility

**WARNING: CRYPTOCURRENCY VALUES ARE EXTREMELY VOLATILE**

- Cryptocurrency prices can drop 50%+ in days or hours
- Mining rewards valued today may be worth significantly less tomorrow
- Electricity costs are fixed; cryptocurrency revenue is not
- This software cannot predict or protect against price volatility
- Mining may become unprofitable at any time due to market conditions

**THIS SOFTWARE PROVIDES NO INVESTMENT ADVICE. CONSULT A FINANCIAL ADVISOR.**

### Blockchain Forks and Consensus Changes

**WARNING: BLOCKCHAIN PROTOCOLS CAN CHANGE WITHOUT NOTICE**

| Event | Impact | Your Responsibility |
|-------|--------|---------------------|
| **Hard forks** | Chain splits, potential replay attacks | Monitor announcements, update software |
| **Soft forks** | Rule changes, potential invalid blocks | Keep nodes updated |
| **Consensus changes** | Mining algorithm changes | May require software updates |
| **Difficulty adjustments** | Profitability changes | No mitigation possible |

The authors:
- Do NOT monitor all supported blockchains for changes
- Do NOT guarantee timely updates for consensus changes
- Accept NO liability for losses due to fork events

### Chain Reorganizations

**WARNING: MINED BLOCKS CAN BE INVALIDATED**

- Blockchain reorganizations ("reorgs") can invalidate previously mined blocks
- Rewards for invalidated blocks are lost
- Low network hashrate = higher reorg risk (easier for an attacker to build a competing chain)
- This is fundamental blockchain behavior, not a software defect

### Stale Blocks (Orphans)

**WARNING: YOUR MINED BLOCKS MAY BECOME STALE**

- Even valid blocks can become stale (commonly called "orphans") due to network propagation timing
- Stale block rewards are lost permanently
- Pool location and network latency affect stale rates
- This software cannot prevent stale blocks

### Wallet Security

**WARNING: CRYPTOCURRENCY WALLET SECURITY IS YOUR RESPONSIBILITY**

| Risk | Consequence |
|------|-------------|
| Lost private keys | Permanent loss of all funds |
| Compromised wallet | Theft of all funds |
| Incorrect address | Permanent loss of mined rewards |
| Unsupported address format | Failed or lost transactions |

This software:
- Does NOT store private keys
- Does NOT validate wallet address ownership
- Does NOT recover lost or stolen cryptocurrency
- Sends rewards to whatever address you configure

**VERIFY ALL WALLET ADDRESSES BEFORE DEPLOYMENT.**

### Tax and Reporting Obligations

**WARNING: CRYPTOCURRENCY MINING MAY BE TAXABLE**

- Mining rewards may be taxable income in your jurisdiction
- Tax obligations may arise at time of receipt (not sale)
- Record-keeping is your responsibility
- Tax laws vary significantly by jurisdiction and change frequently

**THIS SOFTWARE DOES NOT PROVIDE TAX REPORTING FEATURES OR TAX ADVICE.**

### No Investment Advice

**THIS SOFTWARE IS NOT AN INVESTMENT VEHICLE**

Nothing in this software or its documentation constitutes:
- Investment advice
- Financial advice
- Tax advice
- Legal advice
- A recommendation to mine any particular cryptocurrency
- A representation that mining will be profitable

Cryptocurrency mining is speculative. You may lose money.

---

## SUMMARY OF CRITICAL WARNINGS

1. **YOU CAN LOSE ALL MINED CRYPTOCURRENCY** due to configuration errors, bugs, or security breaches

2. **THIS SOFTWARE HAS NOT BEEN SECURITY AUDITED** - undiscovered vulnerabilities may exist

3. **CLOUD DEPLOYMENT IS NOT SUPPORTED AND IS BLOCKED** - the installer will refuse to run on cloud infrastructure (AWS, Azure, GCP, DigitalOcean, etc.); deploy only on operator-controlled bare metal or self-hosted VMs

4. **ARM ARCHITECTURE IS NOT TESTED** - all packages and binaries target x86_64 (amd64); ARM/Raspberry Pi may not work

5. **LEGAL COMPLIANCE IS YOUR RESPONSIBILITY** - mining may be regulated or prohibited

6. **DATA LOSS WILL OCCUR** - backups are mandatory, not optional

7. **NO WARRANTY OF ANY KIND** - you accept all risks by using this software

---

## ACKNOWLEDGMENT

By deploying Spiral Pool, you acknowledge that:

- You have read and understood these specific hazard warnings
- You accept the risks described herein
- You will implement appropriate mitigations
- You release the authors from liability for these known hazards
- You understand that unknown hazards may also exist

**IF YOU DO NOT ACCEPT THESE RISKS, DO NOT USE THIS SOFTWARE.**

---

*Spiral Pool v1.0 - Specific Hazard Warnings*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
