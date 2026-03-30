# Terms of Use

**Effective Date:** 2026-03-27
**Last Updated:** 2026-03-27

## 1. Acceptance of Terms

By downloading, installing, running, or otherwise using Spiral Pool ("the Software"), you agree to be bound by these Terms of Use ("Terms"). If you do not agree to these Terms, do not use the Software.

## 2. License

The Software is licensed under the BSD-3-Clause License. See the `LICENSE` file for the complete license text. These Terms supplement but do not replace the BSD-3-Clause License.

## 3. No Warranty

THE SOFTWARE IS PROVIDED "AS IS" WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, AND NONINFRINGEMENT.

The authors and contributors make no representations or warranties regarding:
- The reliability, accuracy, or completeness of the Software
- The security of the Software or any data processed by it
- The fitness of the Software for any particular purpose
- The legal compliance of the Software in any jurisdiction

## 4. Limitation of Liability

IN NO EVENT SHALL THE AUTHORS, CONTRIBUTORS, OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES, OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT, OR OTHERWISE, ARISING FROM, OUT OF, OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

This limitation applies to:
- Direct, indirect, incidental, special, or consequential damages
- Loss of profits, data, cryptocurrency, or business opportunities
- Hardware damage or system failures
- Legal fees, regulatory fines, or compliance costs
- Any damages arising from data loss, including but not limited to mining shares, block rewards, or transaction data

## 5. User Responsibilities

You are solely responsible for:
- Compliance with all applicable laws in your jurisdiction
- Determining whether operating mining pool software is legal in your jurisdiction
- Securing your systems, wallets, credentials, and infrastructure
- Backing up all data and maintaining disaster recovery procedures
- Any tax obligations arising from cryptocurrency mining
- Conducting your own security assessment before deployment
- Determining whether your pool operation triggers any financial regulatory obligations in your jurisdiction. Spiral Pool v2.1.0 operates as a non-custodial solo pool where block rewards pay the miner's wallet directly via the coinbase transaction, but regulatory interpretations vary (see WARNINGS.md for details)

## 5A. Tor Network Functionality

This Software includes optional Tor (The Onion Router) functionality. Tor is disabled by default and must be explicitly enabled by the operator. This Software configures Tor as a client only — it does not operate as a Tor relay, exit node, or bridge, and does not route traffic for other Tor users or provide anonymity services to third parties.

By enabling or using the Tor functionality, you:

1. Acknowledge that you have reviewed applicable laws in your jurisdiction regarding the use of Tor
2. Accept **full responsibility** for your use of the Tor feature
3. **Release** the software authors and contributors from **all liability** related to your use of Tor
4. Confirm that you will use this feature only for lawful purposes
5. Acknowledge that Tor usage may be restricted, monitored, or prohibited in some jurisdictions

The authors make no representations about the legality of Tor in any jurisdiction and accept no liability for any consequences arising from your use of this feature. See WARNINGS.md for specific Tor-related hazard warnings.

**If you do not agree with these terms, do not enable the Tor functionality.**

## 5B. Cloud Deployment Exclusion

This Software is designed exclusively for deployment on **operator-controlled infrastructure** — bare metal servers under the operator's physical control, or virtual machines running on hypervisors the operator owns and operates. It is **NOT** designed, supported, or recommended for deployment on cloud-based instances, VPS (Virtual Private Server), IaaS (Infrastructure as a Service), or any shared hosting environment where the operator does not own and control the underlying physical hardware, hypervisor, and network infrastructure.

The installer will **block installation** when a cloud provider is detected. Cloud providers that are automatically detected and blocked include, but are not limited to: AWS EC2, Microsoft Azure, Google Cloud, DigitalOcean, Linode/Akamai, Hetzner Cloud, Vultr, OVHcloud, Scaleway, UpCloud, Alibaba Cloud, and Tencent Cloud. Other cloud providers (including Oracle Cloud) may not be automatically detected but are equally excluded from support.

By using this Software, you acknowledge that:

1. Cloud deployment exposes wallet credentials, private keys, database contents, and all operational data to the hosting provider
2. The authors **accept no liability** for any losses, security incidents, or damages arising from cloud deployment
3. Cloud deployments receive **NO SUPPORT** — bug reports, feature requests, and support inquiries related to cloud-hosted installations may be closed without investigation
4. Circumventing the cloud detection mechanism does not create a support obligation or alter this exclusion

## 5C. ARM Architecture

This Software has **NOT** been tested on ARM architecture (including Raspberry Pi, ARM64/aarch64, and ARMv7). All packages, build references, and binary dependencies target **x86_64 (amd64)** on Ubuntu 24.04 LTS. ARM deployments are **NOT supported** and issues arising from ARM-based installations may not be investigated.

## 5D. SimpleSwap.io Integration

This Software includes an optional integration with [SimpleSwap.io](https://simpleswap.io), a third-party cryptocurrency exchange service. This feature is **disabled by default** and must be explicitly enabled by the operator during installation.

When enabled, Spiral Sentinel monitors the sat value (coin/BTC ratio) of mined coins and sends a swap recommendation alert when a coin appreciates 25% or more against BTC over a 7-day baseline. The alert includes a SimpleSwap.io link with the source coin and BTC pre-selected. **No swap is performed automatically. The pool software makes no API calls to SimpleSwap.io and stores no wallet addresses or API keys.** All swap activity occurs on the SimpleSwap.io website in the operator's own browser.

By enabling or using the SimpleSwap integration, you:

1. Acknowledge that **SimpleSwap.io is a third-party service** with its own Terms of Service, Privacy Policy, and operational policies, over which Spiral Pool has no control
2. Accept **full responsibility** for complying with SimpleSwap.io's Terms of Service and all applicable requirements for using their platform
3. Accept **full responsibility** for all applicable **AML (Anti-Money Laundering)** and **KYC (Know Your Customer)** requirements in your jurisdiction
4. Accept **full responsibility** for all **tax obligations**, reporting requirements, and financial regulatory obligations arising from any currency exchange or conversion activity
5. Acknowledge that exchange fees, rate spreads, minimum/maximum limits, and processing times are determined solely by SimpleSwap.io and may change at any time
6. Acknowledge that Spiral Pool **does not process, hold, intermediate, or have any visibility into** any exchange or conversion you initiate through SimpleSwap.io
7. Acknowledge that all transactions with SimpleSwap.io are **irreversible** and Spiral Pool accepts no responsibility for failed swaps, incorrect amounts, wrong destination addresses, or any other transaction outcome
8. **Release** the Software authors and contributors from **all liability** related to your use of SimpleSwap.io or any third-party exchange service

**If you do not agree with these terms, do not enable the SimpleSwap integration.**

The authors make no representations about the availability, reliability, regulatory status, or legality of SimpleSwap.io in any jurisdiction. See WARNINGS.md for additional hazard disclosures related to third-party exchange services.

## 5E. Single-Operator Architecture

This Software is designed for **single-operator use only**. A single wallet address per coin is configured at installation time by the operator. This is a fundamental architectural property, not a configurable option.

**Key facts operators must understand before deployment:**

1. **All block rewards go to the operator's configured wallet address**, embedded directly in the coinbase transaction. There is no mechanism to split rewards or route funds to any other party.
2. **Miners connecting to your pool receive no direct payment** from this software. Regardless of which connected miner found the block, the full block reward goes to the operator's wallet.
3. **Operator wallet control is exclusive.** Only the operator who configured the wallet address can access the mined funds. Connecting miners have no claim against the pool's software-level reward mechanism.
4. **No pooled payout schemes are supported.** PPLNS, PPS, PROP, and similar multi-participant reward distribution schemes are not implemented and will not be added.

**If you allow external miners to connect to your pool**, you acknowledge:

1. You are solely responsible for disclosing to those miners that their hashrate contributes to **your** wallet, not their own
2. You must make any compensation arrangements with external miners independently and outside this software
3. Operating without disclosing this to participants may constitute fraud, deceptive business practices, or theft of services under applicable law
4. Any legal, regulatory, or financial obligations arising from inviting external miners are solely your responsibility
5. The authors of this Software accept **no liability** for any disputes, claims, or legal consequences arising from your operation of the pool in a multi-participant context

**By installing and using this Software**, you confirm that you understand and accept this single-operator architecture, and that you will disclose the wallet control arrangement to any parties whose hashrate you use.

**If you do not agree with these terms or intend to operate a multi-participant pool without proper disclosure, do not use this Software.**

## 6. Data Loss Acknowledgment

You acknowledge that:
- The Software may experience crashes, failures, or data loss
- Mining shares, block data, and other information may be lost during crashes or failures
- The Software includes crash recovery mechanisms that may not recover all data
- You are responsible for implementing your own backup and recovery procedures
- The installer may offer to format unformatted disks as ext4 for blockchain data storage. **Disk formatting permanently and irreversibly destroys all existing data on the formatted device.** While the installer requires explicit `YES` confirmation before formatting, you accept full responsibility for verifying which disks are connected and confirming the correct device. The authors accept no liability for data loss resulting from disk formatting, whether accidental or intentional

## 7. Dispute Resolution

### 7.1 Informal Resolution First

Before initiating any formal dispute resolution, you agree to contact the project maintainers and attempt to resolve the dispute informally for at least 30 days.

### 7.2 Arbitration Agreement

If informal resolution fails, any dispute arising out of or relating to these Terms or the Software shall be resolved by binding arbitration, except as provided in Section 7.4.

Arbitration shall be:
- Administered by a mutually agreed-upon arbitration service, or if none can be agreed, by ADR Institute of Canada (ADRIC), JAMS, or AAA as appropriate for the parties' jurisdictions
- Conducted in English
- Based on written submissions unless either party requests a hearing
- Governed by the arbitration service's rules for consumer disputes

### 7.3 Class Action Waiver

To the extent permitted by applicable law, you agree that any dispute resolution proceedings will be conducted only on an individual basis and not in a class, consolidated, or representative action.

### 7.4 Exceptions to Arbitration

The following are NOT subject to arbitration:
- **Small claims court**: Either party may bring qualifying claims in small claims court
- **Injunctive relief**: Either party may seek injunctive relief in court for intellectual property disputes
- **Jurisdictions where unenforceable**: If arbitration is prohibited or unenforceable in your jurisdiction, disputes shall be resolved in the courts of competent jurisdiction

### 7.5 Opt-Out Right

You may opt out of this arbitration agreement by sending written notice within 30 days of first using the Software. Opt-out notices should be sent via GitHub issue or other contact method provided by the project.

### 7.6 Open Source Acknowledgment

You acknowledge that this is free, open source software provided at no cost. The authors do not charge for the Software and have no commercial relationship with users. Voluntary donations, if accepted, do not create a commercial relationship, warranty obligation, or entitlement to support or updates. This context should be considered in any dispute resolution.

## 8. Indemnification

You agree to indemnify, defend, and hold harmless the authors, contributors, and their respective officers, directors, employees, agents, and successors from and against any and all claims, damages, losses, liabilities, costs, and expenses (including reasonable legal fees) arising out of or relating to:

1. Your use of the Software
2. Your violation of these Terms
3. Your violation of any applicable law or regulation
4. Your violation of any third-party rights, including intellectual property rights
5. Any claim that your use of the Software caused damage to a third party
6. Any cryptocurrency, tax, or financial regulatory matters arising from your use

This indemnification obligation survives termination of these Terms and your use of the Software.

## 8A. No Indemnification to Downstream Parties

### 8A.1 No Indemnification Provided

The authors, contributors, and copyright holders:
- Do NOT indemnify you against any claims
- Do NOT indemnify any downstream recipients of your redistributions
- Do NOT indemnify users of any derivative works you create
- Do NOT assume any defense obligations for any party

### 8A.2 Downstream Redistributor Liability

If you redistribute, fork, modify, or create derivative works of this Software:
- YOU assume all liability for YOUR distribution
- YOU are solely responsible for claims arising from YOUR distribution
- End users of YOUR distribution have claims against YOU, not the original authors
- The original authors have no privity with your downstream users

### 8A.3 Commercial Derivatives

If you use this Software in a commercial product or service:
- YOU bear all responsibility for fitness, security, and merchantability representations
- YOUR customers' claims lie against YOU, not the original authors
- You may NOT represent that the original authors endorse, support, or warrant your product
- You may NOT contractually bind or obligate the original authors in any way

### 8A.4 No Third-Party Beneficiary Rights

These Terms do not create any third-party beneficiary rights. Specifically:
- Users of derivative works have no rights against the original authors under these Terms
- Customers of commercial forks have no rights against the original authors under these Terms
- No downstream party may enforce any provision of these Terms against the original authors

### 8A.5 Impleader / Third-Party Claims

If you are sued by a downstream user or third party:
- You may NOT implead, join, or otherwise bring the original authors into that action
- You waive any right to seek contribution or indemnification from the original authors
- You agree to defend any attempt to add the original authors as parties at your own expense

## 9. Modifications

These Terms may be modified at any time by updating this file in the project repository. Modifications apply prospectively to use of the Software after the date of publication. Continued use of the Software after modifications are published constitutes acceptance of the modified Terms. It is your responsibility to review these Terms periodically.

## 10. Governing Law and Jurisdiction

### 10.1 Governing Law

These Terms shall be governed by and construed in accordance with the laws of Canada, without regard to conflict of law provisions.

### 10.2 Jurisdiction

Subject to Section 7 (Dispute Resolution), any legal action or proceeding arising out of or relating to these Terms shall be brought exclusively in the courts of Canada, and you consent to the personal jurisdiction of such courts.

### 10.3 International Users

If you are located outside Canada:
- You are responsible for compliance with local laws
- These Terms are still governed by Canadian law
- You consent to jurisdiction in Canada for any disputes not resolved through arbitration
- Nothing in these Terms limits any consumer protection rights that cannot be waived under your local law

## 11. Severability

If any provision of these Terms is found to be unenforceable, the remaining provisions shall remain in full force and effect.

## 12. Waiver

The failure of the authors to enforce any right or provision of these Terms shall not constitute a waiver of such right or provision. Any waiver of any provision of these Terms will be effective only if in writing.

## 13. Survival

The following provisions shall survive any termination or expiration of these Terms: Sections 3 (No Warranty), 4 (Limitation of Liability), 5 (User Responsibilities), 5A (Tor Network Functionality), 5B (Cloud Deployment Exclusion), 5C (ARM Architecture), 5D (SimpleSwap.io Integration), 5E (Single-Operator Architecture), 6 (Data Loss Acknowledgment), 8 (Indemnification), 8A (No Indemnification to Downstream Parties), 10 (Governing Law and Jurisdiction), 14 (European Union Product Liability), and this Section 13 (Survival).

## 14. European Union Product Liability

The revised **EU Product Liability Directive (Directive 2024/2853)** may classify software as a "product." However, Article 2(2) provides that the Directive does not apply to free and open-source software that is not supplied in the course of a commercial activity. This software is:

- Made available free of charge under the BSD-3-Clause license
- Not supplied in the course of any trade, business, craft, or profession
- Developed by individual contributors, not by a commercial entity
- Provided without any commercial relationship with users (Section 7.6). Voluntary donations received by individual maintainers in their personal capacity (see README) do not constitute commercial activity, payment for goods or services, or consideration of any kind.

Accordingly, the authors and contributors assert that this software falls within the open-source safe harbor of Directive 2024/2853 and that the Directive's strict liability provisions do not apply to the original authors and contributors.

**Note:** Operators who incorporate this software into commercial products or services may be subject to the Directive as "manufacturers" in their own right. See Section 8A regarding redistributor liability.

## 15. Force Majeure

The authors shall not be liable for any failure or delay in performance due to circumstances beyond their reasonable control, including but not limited to: acts of God, natural disasters, war, terrorism, riots, embargoes, acts of civil or military authorities, fire, floods, accidents, pandemic, strikes, shortages of transportation, facilities, fuel, energy, labor, materials, or communications or information system failures. For the avoidance of doubt, this section does not create or imply any obligation to perform, maintain, update, or support the Software.

## 16. Entire Agreement

These Terms, together with the BSD-3-Clause License and the following supplemental documents, constitute the entire agreement between you and the authors regarding the Software: WARNINGS.md, PRIVACY.md, SECURITY.md, EXPORT.md, TRADEMARKS.md, NOSEC.md, and CONTRIBUTING.md. In the event of a conflict between these Terms and any supplemental document, these Terms shall control.

---

*By using Spiral Pool, you acknowledge that you have read, understood, and agree to be bound by these Terms of Use.*

*Spiral Pool v2.1.0 - Terms of Use*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
