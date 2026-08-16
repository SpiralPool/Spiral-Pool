# Specific Hazard Warnings

**Last Updated:** 2026-08-14

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

### Bitcoin Chain Split — Mining a Chain With No Value

**WARNING: YOUR NODE CAN MINE A WORTHLESS CHAIN WHILE APPEARING PERFECTLY HEALTHY**

On **8 August 2026** at block height **961,632**, Bitcoin split. BIP-110 — also called RDTS, the Reduced Data Temporary Softfork — entered a mandatory signalling window, and nodes enforcing it began rejecting any block that did not signal version bit 4. Approximately **99.85% of hashpower stayed on the majority chain**. The enforcing minority chain produces roughly one block every day or two, and its coins are not traded on any exchange.

> Two figures circulate and they measure different things. **2.53%** is *signalling* support — 51 of the 2,016 blocks in the window signalled BIP-110. **0.15%** is the hashpower that actually *followed* the minority chain once it split. Most miners who signalled stayed on the majority chain, which is why both numbers are correct at once.

**Which builds follow which chain:**

| Build | Chain | Safe to mine |
|-------|-------|--------------|
| Bitcoin Core (any current version) | Majority | **Yes** — this is what Spiral Pool installs |
| Bitcoin Knots `29.3.knots20260507` | Majority | Yes, but not shipped |
| Bitcoin Knots `29.3.knots20260508` | **Minority** | **No** |
| Bitcoin Knots `29.4.knots20260508` | **Minority** | **No** |

The datestamp suffix is the enforcement marker, **not** a release date — note that `29.4` carries the same enforcing `20260508` suffix as `29.3`. The two builds differ by **one digit**. Never select a Bitcoin daemon version by "latest".

**Enforcement is compiled into the build.** Removing `consensusrules=rdts` from `bitcoin.conf` does **not** disable it — that option only records consent and silences a warning. Changing chains requires changing binaries.

**WHY THIS IS DANGEROUS:**

A pool whose daemon follows the minority chain **reports no fault of any kind**. Stratum accepts connections. Shares validate. Vardiff converges. The dashboard shows healthy hashrate. Sync progress reads 100%. The only symptom is that blocks never arrive — which, for a solo miner, is indistinguishable from ordinary bad luck. Expected value is **exactly zero**, and nothing in the software would tell you.

**Merge mining makes this worse, not better.** AuxPoW proofs reference only the parent block *header* and do not verify which chain that header belongs to — an aux chain has no copy of Bitcoin's chain and cannot check. So NMC, SYS and XMY **keep finding and confirming blocks normally** while BTC earns nothing. Your pool continues to look productive. The only thing missing is the coin that pays best.

**WHAT SPIRAL POOL DOES ABOUT IT:**

- Installs and pins **Bitcoin Core**, verified against a checksum committed in this repository
- Detects an existing Bitcoin Knots binary on install or upgrade and replaces it
- **Refuses to mine BTC** until it has verified block 961,632 against the majority chain
- Repairs a node stranded on the minority chain (`reconsiderblock`), because a binary swap alone does **not** move it — the enforcing daemon leaves a persistent rejected-block marker that Bitcoin Core inherits and honours
- Alerts through Sentinel and shows a chain verdict on the dashboard

**YOU ARE STILL RESPONSIBLE FOR:**
- Not setting `allow_nonmajority_chain: true` unless you genuinely intend to mine a minority chain. Beyond permitting a minority chain, it also disables the stale-tip refusal, so a node wedged on the correct chain will mine a dead tip
- Not installing a Bitcoin daemon by hand outside the installer
- Not adding `maxtipage` to `bitcoin.conf` — it disables the daemon's own initial-block-download gating, so a wedged node reports `initialblockdownload: false` and the dashboard, Sentinel and sync gate all read green. The pool's chain gate is unaffected (it measures tip age itself and still refuses to mine a dead tip), so the damage is that your monitoring stops agreeing with the pool
- Verifying the chain verdict after any manual daemon change

**A proof-of-work change away from SHA-256d has been announced for the minority chain.** If it ships, SHA-256d hardware cannot mine that chain at all. As of this writing no client has been released and no activation height announced. Verify current status before making any decision based on this section.

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

**Cloud/VPS deployments require explicit risk acknowledgment during install — see Cloud Deployment section below.**

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

**WARNING: CLOUD DEPLOYMENT IS SUPPORTED BUT CARRIES SERIOUS RISKS — YOU MUST ACKNOWLEDGE THEM DURING INSTALLATION**

Spiral Pool can be installed on cloud-based VPS instances. The installer detects 100+ cloud providers and requires explicit written acknowledgment of all risks before proceeding. Cloud deployments apply additional hardening automatically (SSH restricted to operator IP, dashboard closed, HA disabled).

**Key risks — see [CLOUD_OPERATIONS.md](docs/setup/CLOUD_OPERATIONS.md) for full details:**

- **Provider access:** The cloud provider has unrestricted access to your server's memory, disk, and network traffic — including private keys, wallet credentials, and database contents
- **Provider ToS violations:** Most major providers prohibit mining-related workloads — violations can result in immediate account termination and permanent data loss
- **Bandwidth billing:** Blockchain synchronization generates hundreds of gigabytes of egress; multi-coin deployments multiply costs
- **No HA support:** Keepalived VRRP is blocked by cloud hypervisors — VIP failover silently fails

**Recommended deployment targets:** Bare metal servers or VMs on hypervisors you own and operate. Cloud deployments receive community support only.

**YOU ARE SOLELY RESPONSIBLE** for reading your provider's Terms of Service, monitoring bandwidth billing, and all consequences of cloud deployment. See [CLOUD_OPERATIONS.md](docs/setup/CLOUD_OPERATIONS.md) for the complete cloud operations guide.

### No Security Audit

**WARNING: THIS SOFTWARE HAS NOT BEEN PROFESSIONALLY AUDITED**

- No third-party security review has been conducted
- Undiscovered vulnerabilities may exist
- The security measures described in documentation are design intentions, not guarantees
- You must conduct your own security assessment

### Credential Exposure

**WARNING: CREDENTIALS MAY BE EXPOSED**

Spiral Pool supports **environment variable overrides** for sensitive credentials, allowing you to keep secrets out of configuration files. The following environment variables are recognized by Sentinel and Docker deployments:

| Environment Variable | Purpose |
|----------------------|---------|
| `SPIRAL_ADMIN_API_KEY` | Admin API authentication key |
| `SPIRAL_METRICS_TOKEN` | Prometheus metrics endpoint token |
| `POOL_API_URL` | Pool stratum API URL |
| `DISCORD_WEBHOOK_URL` | Discord alert webhook URL |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token for alerts |
| `TELEGRAM_CHAT_ID` | Telegram chat ID for alerts |
| `NTFY_URL` | ntfy push notification topic URL |
| `NTFY_TOKEN` | ntfy authentication token (for private/self-hosted topics) |
| `SMTP_HOST` | SMTP server hostname for email alerts |
| `SMTP_PORT` | SMTP server port |
| `SMTP_USERNAME` | SMTP authentication username |
| `SMTP_PASSWORD` | SMTP authentication password |
| `SMTP_FROM` | SMTP sender address |
| `SMTP_TO` | SMTP recipient address(es) |
| `XMPP_JID` | XMPP Jabber ID for alerts |
| `XMPP_PASSWORD` | XMPP account password |
| `XMPP_RECIPIENT` | XMPP recipient JID |
| `EXPECTED_FLEET_THS` | Expected fleet hashrate in TH/s |

When set, environment variables take precedence over values in the configuration file.

| Location | Risk | Mitigation |
|----------|------|------------|
| `config.yaml` (stratum) | Contains admin API key, RPC credentials | Restrict file permissions (`chmod 600`), owned by pool user only |
| `config.json` (Sentinel) | Contains Discord webhook, Telegram token, SMTP password, XMPP password, ntfy token | Restrict file permissions (`chmod 600`), owned by pool user only |
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

**By enabling or using the Tor functionality, you acknowledge that you have reviewed applicable laws in your jurisdiction, accept full responsibility for your use of the Tor feature, and confirm that you will use this feature only for lawful purposes.** See [TERMS.md Section 5A](TERMS.md) for the binding legal acknowledgment.

### Export Control

**WARNING: CRYPTOGRAPHIC SOFTWARE MAY BE SUBJECT TO EXPORT CONTROLS**

- This software contains cryptographic functionality
- Export to certain countries may violate U.S. law
- Re-export by users may require compliance measures
- See EXPORT.md for detailed information

### Single-Operator Architecture — Wallet Control

**WARNING: ALL BLOCK REWARDS GO TO THE OPERATOR'S CONFIGURED WALLET. MINERS CONNECTING TO YOUR POOL RECEIVE NO DIRECT PAYMENT.**

Spiral Pool is designed for **one operator** running **their own mining hardware**. One wallet address per coin is configured at install time by the operator. This is a fundamental architectural constraint, not a configurable option.

| Fact | Detail |
|------|--------|
| **One wallet per coin** | A single payout address is set per coin during installation |
| **Operator controls the wallet** | Only the operator who configured the address receives block rewards |
| **No per-miner payouts** | There is no mechanism to split rewards or pay external miners |
| **All hashrate benefits the operator** | Block rewards go to the configured address regardless of which connected miner found the block |

**If you allow other people to point their miners at your pool:**

- Those miners contribute hashrate to **your** wallet, not their own
- They receive **no cryptocurrency** from your pool, directly or indirectly
- You are solely responsible for informing them of this arrangement
- Any off-chain compensation you agree to provide them is your own responsibility
- You may have legal, regulatory, or contractual obligations toward participants — see Money Transmission section below

**This software provides no mechanism to:**
- Split block rewards between multiple wallets
- Pay participating miners based on contributed hashrate (PPLNS, PPS, etc.)
- Track or record what any external miner is owed

**If participants are unaware that rewards go entirely to the operator, this may constitute fraud under applicable law. You are solely responsible for transparency with any miners you invite to connect.** See [TERMS.md Section 5E](TERMS.md) for the binding legal acknowledgment.

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

### Disk Formatting

**WARNING: THE INSTALLER CAN FORMAT DISKS — DATA LOSS IS PERMANENT AND IRREVERSIBLE**

During installation, if Spiral Pool detects an unformatted (raw) disk or partition, it will offer to format it as ext4 for blockchain data storage. **Formatting permanently destroys all existing data on the selected device.** This action cannot be undone.

| Safeguard | Description |
|-----------|-------------|
| **Explicit confirmation required** | You must type `YES` (uppercase) to confirm formatting — any other input cancels |
| **Root disk protected** | The OS disk and its partitions are never offered for formatting |
| **Destructive warning displayed** | A prominent red warning box shows the exact device and size before confirmation |
| **Graceful cancellation** | Declining to format does not interrupt the installation — it continues on the root disk |

**DESPITE THESE SAFEGUARDS:**
- The authors and contributors accept **NO responsibility** for data loss resulting from disk formatting
- **YOU** are solely responsible for verifying which disks are connected to the system before running the installer
- **YOU** must ensure any disk offered for formatting does not contain data you need
- Formatting is **PERMANENT** — there is no undo, no recovery, and no recourse
- If you are unsure which disk is which, **do NOT confirm formatting** — press any key other than `YES` to cancel safely

**RECOMMENDATION:** Before running the installer on a system with multiple disks, verify disk contents with `lsblk -f` and `blkid` to identify all connected storage devices.

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

### Third-Party Exchange Services (SimpleSwap.io)

**WARNING: OPTIONAL SIMPLESWAP.IO INTEGRATION IS ENTIRELY OPERATOR-CONTROLLED AND CARRIES FINANCIAL AND LEGAL RISK**

The optional SimpleSwap swap alert feature connects to [SimpleSwap.io](https://simpleswap.io), a third-party cryptocurrency exchange service. Enabling this feature and acting on swap alerts carries the following risks and responsibilities:

- **No automatic swaps — ever.** Spiral Pool sends a notification only. The alert includes a SimpleSwap.io link that opens in your browser. All swap activity happens on the SimpleSwap website — the pool software makes no API calls to SimpleSwap.io, stores no wallet addresses, and has no involvement in any transaction.
- **Operator AML/KYC responsibility.** You are solely responsible for complying with all applicable Anti-Money Laundering (AML) and Know Your Customer (KYC) requirements imposed by SimpleSwap.io, your financial institution, and your jurisdiction. Failure to comply may result in account suspension, frozen funds, or legal consequences.
- **Operator tax responsibility.** Converting cryptocurrency to another cryptocurrency may constitute a taxable event in your jurisdiction. Consult a qualified tax professional before using this feature.
- **SimpleSwap.io Terms of Service.** You are solely responsible for compliance with SimpleSwap.io's Terms of Service, acceptable use policy, and any jurisdictional restrictions they impose. Some countries may be restricted from using SimpleSwap.io.
- **Exchange fees and rates.** SimpleSwap.io charges fees and applies exchange rates that are determined solely by SimpleSwap.io and may change at any time without notice.
- **Transaction irreversibility.** All cryptocurrency transactions are irreversible. Spiral Pool accepts no responsibility for incorrect amounts, wrong destination addresses, failed swaps, or any other transaction outcome.
- **No Spiral Pool liability.** Spiral Pool, its developers, and contributors have no visibility into, control over, or liability for any transaction you initiate through SimpleSwap.io. See TERMS.md section 5D.

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

#### There is no seed phrase

**WARNING: A WALLET GENERATED ON THE POOL SERVER HAS NO RECOVERY PHRASE**

If you let the installer generate a wallet, the daemon creates it — and Bitcoin Core and its derivatives have never supported BIP39. **There are no 12 or 24 words.** There is nothing to write on paper. If you are used to Electrum, Sparrow, BlueWallet or a hardware wallet, the thing you are waiting for does not exist here, and assuming you missed it is how people lose funds.

What exists instead is a **backup file**, created automatically at wallet generation and stored under `/spiralpool/backups/` at mode 600:

| File | What it is | Restore with |
|------|-----------|--------------|
| `wallet-<coin>-<timestamp>.dat` | Binary wallet copy (`backupwallet`) | `<coin>-cli loadwallet <path>` |
| `wallet-<coin>-<timestamp>.dump` | JSON export of private descriptors (`listdescriptors true`) | `<coin>-cli importdescriptors <contents>` |

**That file is the only copy of your keys that will ever exist.** Copy it off the server and store it in at least two offline locations. If you lose it and lose the server, the funds are gone — there is no phrase, no support channel, and no recovery procedure that can help you.

Note the backups the pool takes during upgrades and mode switches are on the **same machine**. They protect against the scripts, not against losing the box.

#### Recommended: generate the wallet somewhere else

The safest option, and the one the installer marks **recommended**, is to not generate a wallet on the pool server at all:

1. Create the wallet on a **separate machine** — a hardware wallet, Electrum, Sparrow, or any wallet that gives you a seed phrase.
2. Write down the seed phrase and store it offline.
3. Copy only the **receiving address**.
4. At the installer's wallet prompt choose **`[1] I have a wallet`** and paste that address.

The private keys then never touch the pool server. A compromised or failed miner box cannot cost you the funds, and you keep a real seed phrase you can restore from anywhere. This is strictly better than option `[2]` for anything holding meaningful value.

### BCH2 Address Collision Warning

**BCH2 (Bitcoin Cash II) uses IDENTICAL legacy address bytes to BCH and BTC.**

BCH2 P2PKH (version byte 0x00) produces addresses starting with `1...` — the same format as Bitcoin and Bitcoin Cash. BCH2 P2SH (0x05) produces `3...` addresses identical to BTC/BCH.

**If you use a BTC or BCH address as your BCH2 pool address, your block rewards will be sent to the wrong blockchain and will be permanently unrecoverable.**

Use CashAddr format (`bitcoincashii:q...`) for BCH2 to eliminate ambiguity. The installer and `spiralpool-wallet` always generate CashAddr format for BCH2.

### BTCS Address Format Warning

**BTCS (Bitcoin Silver) uses non-standard P2PKH version byte 0x1A**, which produces addresses starting with `B`. This is distinct from BTC (`1...`), BCH (`q...`/`bitcoincash:`), and all other supported coins.

BTCS also supports:
- `3...` P2SH addresses (same as BTC — use with care)
- `bs1q...` SegWit bech32 addresses
- `bs1p...` Taproot bech32m addresses

Standard BTC addresses are NOT valid BTCS addresses (wrong version byte). Sending BTCS rewards to a BTC address will result in permanent loss.

**VERIFY ALL WALLET ADDRESSES BEFORE DEPLOYMENT.** Cloud operators: see [CLOUD_OPERATIONS.md — Wallet Security on Cloud](docs/setup/CLOUD_OPERATIONS.md#wallet-security-on-cloud) for additional risks related to auto-generated wallets on provider infrastructure.

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

3. **CLOUD DEPLOYMENT CARRIES SERIOUS RISKS** — provider ToS violations can cause immediate account termination and permanent data loss; bandwidth charges can be hundreds of dollars per sync; the provider has unrestricted access to your wallet credentials and all server data. The installer requires written acknowledgment of all risks before proceeding on cloud. See the Cloud Deployment section above and [CLOUD_OPERATIONS.md](docs/setup/CLOUD_OPERATIONS.md)

4. **LEGAL COMPLIANCE IS YOUR RESPONSIBILITY** - mining may be regulated or prohibited

6. **DATA LOSS WILL OCCUR** - backups are mandatory, not optional

7. **DISK FORMATTING IS IRREVERSIBLE** - the installer can format unformatted disks as ext4; all existing data on formatted disks is permanently destroyed

8. **NO WARRANTY OF ANY KIND** - you accept all risks by using this software

9. **SINGLE-OPERATOR ONLY — ALL REWARDS GO TO THE OPERATOR'S WALLET** - miners connecting to your pool receive no direct payment; you must disclose this to any external participants or face potential legal liability

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

*Spiral Pool v2.7.0 - Specific Hazard Warnings*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
