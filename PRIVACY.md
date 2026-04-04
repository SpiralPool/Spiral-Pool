# Privacy Notice

**Effective Date:** 2026-03-27
**Last Updated:** 2026-03-27

## Overview

Spiral Pool is self-hosted software that runs on your own infrastructure. The authors and contributors do not operate any centralized servers or services that collect data from your installation.

## Non-Custodial Architecture

Spiral Pool v2.2.1 operates as a **solo mining pool with a non-custodial payout model**. Block rewards are embedded directly in the coinbase transaction paying the miner's own wallet address. The pool operator never takes custody, control, or possession of miner funds. No financial transaction data (transfers, payouts, withdrawals) is generated or stored by the pool software, because no such transactions occur — the blockchain pays the miner directly.

## Data Processed Locally

When you run Spiral Pool, the following data is processed and stored locally on YOUR infrastructure:

### Mining Data
- Miner IP addresses (for connection management and rate limiting)
- Wallet addresses (for coinbase transaction construction and share attribution)
- Worker names (for identification)
- Share submissions and validation results
- Block discovery records
- Difficulty adjustment history

### System Data
- User-agent strings (for miner type detection)
- Connection timestamps
- Performance metrics

### Configuration Data
- Cryptocurrency node credentials
- Database credentials
- API keys for external services (Discord, Telegram, if configured)

## Data Controller

YOU are the data controller for all data processed by your Spiral Pool installation. The authors and contributors have no access to your data.

### Data Processors (GDPR Article 28)

If you operate Spiral Pool on behalf of another party (e.g., as a hosting provider or managed mining service), you may be acting as a **data processor** under GDPR Article 28. In this case, you should ensure a written data processing agreement (DPA) is in place with the data controller.

#### DPA Checklist (Article 28 Minimum Requirements)

A compliant DPA should address at minimum:

- [ ] **Subject matter and duration** — Describe the mining pool operation and contract term
- [ ] **Nature and purpose of processing** — Share validation, block attribution, payment calculation
- [ ] **Types of personal data** — IP addresses, wallet addresses, worker names, user-agent strings
- [ ] **Categories of data subjects** — Miners connecting to the pool
- [ ] **Sub-processor list** — Document all sub-processors (hosting provider, database hosting, monitoring services, Discord/Telegram for notifications)
- [ ] **Sub-processor change notification** — Procedure for informing controller of sub-processor changes
- [ ] **Data return and deletion obligations** — Procedure for returning or deleting all personal data upon contract termination (see `spiralctl gdpr-delete`)
- [ ] **Audit rights** — Controller's right to audit processor's data handling practices
- [ ] **Breach notification timeline** — Processor must notify controller without undue delay (Article 33(2)); controller must notify supervisory authority within 72 hours (Article 33(1))
- [ ] **Technical and organizational measures** — Security measures implemented (encryption, access controls, logging)
- [ ] **Assistance with data subject rights** — Procedure for handling access, rectification, erasure, portability requests
- [ ] **Cross-border transfer safeguards** — SCCs or other mechanisms if data leaves the EEA (see Transfer Impact Assessment below)

Consult a qualified legal professional for guidance on data processor obligations.

## Your Obligations

As the operator of a Spiral Pool installation, you may have legal obligations regarding data processing, including but not limited to:

### GDPR (European Union)
If you process data of EU residents, you may be required to:
- Provide a privacy notice to miners
- Establish a legal basis for processing
- Implement appropriate security measures
- Respond to data subject requests

### CCPA (California)
If you process data of California residents, you may be required to:
- Disclose data collection practices
- Honor opt-out requests
- Provide data access upon request

### PIPEDA (Canada)
If you process personal information in Canada, the Personal Information Protection and Electronic Documents Act (PIPEDA) requires:
- Accountability for personal information under your control
- Identifying purposes for collection at or before collection time
- Obtaining meaningful consent for collection, use, or disclosure
- Limiting collection to what is necessary for identified purposes
- Limiting use, disclosure, and retention to identified purposes
- Maintaining accuracy of personal information
- Implementing appropriate security safeguards
- Making privacy policies readily available
- Providing individuals access to their personal information
- Allowing individuals to challenge compliance

**Note:** Some Canadian provinces (Quebec, Alberta, British Columbia) have substantially similar provincial legislation that may apply instead of or in addition to PIPEDA.

#### Quebec Law 25 (Act Respecting the Protection of Personal Information in the Private Sector)

If you process personal information of Quebec residents, Quebec's **Law 25** (formerly Bill 64) imposes additional requirements beyond PIPEDA:

- **Privacy Impact Assessment (PIA)**: Required before deploying any system that collects personal information, or when transferring personal information outside Quebec. A PIA should evaluate the sensitivity of data collected (IP addresses, wallet addresses), the purpose of collection, and the proportionality of collection to purpose.
- **Consent mechanisms**: Express consent is required for collecting sensitive personal information. Consent must be requested separately from other information and in clear, simple language. For mining pools, consider whether IP address collection for rate limiting constitutes collection requiring consent.
- **Data residency considerations**: Law 25 requires a PIA before transferring personal information outside Quebec. If your pool infrastructure (database, logs, or third-party integrations like Discord/Telegram) is hosted outside Quebec, you must conduct a PIA to assess whether the destination jurisdiction provides adequate privacy protection.
- **Privacy officer**: An organization must designate a person responsible for personal information protection and publish their title and contact information.
- **Breach notification**: Mandatory notification to the Commission d'accès à l'information (CAI) and affected individuals for breaches posing a risk of serious injury.
- **Right to data portability**: Individuals have the right to receive their personal information in a structured, commonly used technological format (effective September 2024).
- **De-indexation right**: Individuals can request that personal information cease to be disseminated if it causes injury.

### Other Jurisdictions
Other jurisdictions may have additional requirements. Consult with a qualified legal professional regarding your specific obligations.

## Third-Party Services

If you configure Spiral Pool to integrate with third-party services, data may be transmitted to those services:

- **Google Fonts CDN**: The dashboard loads fonts (Orbitron, Rajdhani, Share Tech Mono) from `fonts.googleapis.com` by default. This causes user browsers to connect to Google servers, transmitting IP addresses to Google. **For GDPR compliance** (per LG Munchen, Case No. 3 O 17493/20), operators serving EU users should self-host these fonts and remove the Google CDN references from dashboard HTML templates. See THIRD_PARTY_LICENSES.txt for detailed guidance.
- **jsDelivr CDN**: The dashboard loads JavaScript libraries (Chart.js, SortableJS) from `cdn.jsdelivr.net`. This causes user browsers to connect to jsDelivr servers, transmitting IP addresses. For operators concerned about third-party CDN connections, these libraries can be self-hosted.
- **Discord Webhooks**: Alert messages, miner statistics
- **Telegram Bots**: Alert messages, miner statistics
- **Cloudflare Tunnels**: Connection routing metadata
- **Cryptocurrency Nodes**: Transaction and block data
- **SimpleSwap.io** *(optional)*: If you enable the SimpleSwap swap alert feature, Sentinel includes a SimpleSwap.io link in `sats_surge` alerts. If you choose to act on the alert, your **browser** connects directly to SimpleSwap.io — you enter your BTC destination address and complete the swap on their website. Spiral Pool does not store any wallet address, API key, or exchange data. No data is transmitted to SimpleSwap.io by the pool software itself. You are responsible for reviewing [SimpleSwap.io's Privacy Policy](https://simpleswap.io/privacy-policy) before use.

Review the privacy policies of any third-party services you integrate.

**Cross-Border Data Transfers:** Configuring Discord or Telegram notifications may result in data being transferred to servers outside your jurisdiction (e.g., Discord servers in the United States, Telegram servers in various jurisdictions). If you are subject to GDPR or similar data protection laws, you should assess whether adequate safeguards are in place for such cross-border transfers and inform your miners accordingly.

#### Transfer Impact Assessment (TIA) Guidance

If you transfer personal data outside the EEA (European Economic Area), a Transfer Impact Assessment is recommended to evaluate the legal framework in the destination country. Key considerations:

1. **Adequacy Decisions**: Check whether the European Commission has issued an adequacy decision for the destination country (e.g., EU-US Data Privacy Framework for US transfers). If an adequacy decision exists, transfers may proceed without additional safeguards.

2. **Standard Contractual Clauses (SCCs)**: In the absence of an adequacy decision, implement the European Commission's Standard Contractual Clauses with your service providers. For Spiral Pool deployments, this may apply to:
   - Cloud hosting providers (if hosting outside EEA)
   - Discord (US-based, for webhook notifications)
   - Telegram (multi-jurisdiction)
   - Any external monitoring services (Grafana Cloud, etc.)

3. **Supplementary Measures**: Where SCCs alone may not provide adequate protection (per *Schrems II*), consider:
   - **Technical measures**: Encrypt data in transit (HTTPS for webhooks is validated at startup) and at rest; pseudonymize miner identifiers in alerts
   - **Organizational measures**: Implement access controls, data minimization policies, and incident response procedures
   - **Contractual measures**: Negotiate additional commitments from service providers regarding government access requests

4. **Documentation**: Maintain records of your TIA assessment, including the legal basis for each transfer, the safeguards implemented, and periodic review dates.

## Data Retention

Data retention is controlled by your configuration and infrastructure. The Software does not impose specific retention periods. You are responsible for implementing appropriate data retention and deletion policies.

## Data Deletion (GDPR Article 17 / CCPA)

As the data controller, you are responsible for handling data subject deletion requests. The following data locations should be addressed:

### PostgreSQL Database
```sql
-- Delete miner records by wallet address (repeat for each pool)
DELETE FROM miners WHERE address = 'wallet_address';
DELETE FROM shares_{poolID} WHERE miner = 'wallet_address';
DELETE FROM blocks_{poolID} WHERE miner = 'wallet_address';
DELETE FROM worker_hashrate_history_{poolID} WHERE miner = 'wallet_address';

-- Delete by IP address (shares table stores IP; miners table does not)
DELETE FROM shares_{poolID} WHERE ipaddress = 'x.x.x.x';
```

> **Note:** Replace `{poolID}` with your configured pool identifier (e.g., `shares_digibyte`). Run the deletions for each pool if you operate multiple coins.

### JSON Data Files
- `/spiralpool/data/bans.json` - Contains banned IP addresses
- `/spiralpool/data/device_hints.json` - Contains miner IP-to-device mappings
- `/spiralpool/data/fleet.json` - Contains fleet discovery data

### Log Files
- Application logs in `/spiralpool/logs/`
- Stratum server logs
- Sentinel logs

### Third-Party Services
If you configured integrations, data may also exist in:
- Discord message history
- Telegram message history
- Prometheus/Grafana metrics databases

**Note:** The Software includes a `spiralctl gdpr-delete` command for automated deletion of miner records from the PostgreSQL database. However, operators must still manually review and clean JSON data files, log files, and any third-party service data as described above. See `spiralctl gdpr-delete --help` for usage details.

## Security

Security measures are described in the documentation. However, no security measure is guaranteed to prevent all unauthorized access. You are responsible for:
- Securing your infrastructure
- Implementing appropriate access controls
- Monitoring for security incidents
- Responding to breaches in accordance with applicable law

## Contact

This software is provided by independent contributors. There is no centralized support organization or data protection officer. For questions about this Privacy Notice, consult with your own legal and technical advisors.

---

*This Privacy Notice describes data handling by the Spiral Pool software. As a self-hosted application, you control all data processing.*

*Spiral Pool v2.2.1 - Privacy Notice*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
