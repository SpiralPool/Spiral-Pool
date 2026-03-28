# Export Control Notice

**Last Updated:** 2026-03-27

## Project Origin

This software is developed and distributed from **Canada**. Export control analysis is provided for Canadian, U.S., and EU law, as users may be subject to one or more of these regimes.

---

## Publicly Available Software

This software qualifies as **publicly available** under most export control frameworks:
- Freely available to the public without restriction
- No access controls, payments, or license agreements required for access
- Published on public internet repositories
- Uses only standard, publicly documented cryptographic implementations

Publicly available open source software is generally **not controlled** under most export control regimes, though specific rules vary by jurisdiction.

---

## CANADA: Export Control Status

### Canadian Export Controls

Under the **Export and Import Permits Act (EIPA)** and Canada's **Export Control List (ECL)**:

- **Group 1 (Dual-Use List)** includes cryptographic items, but publicly available software is generally excluded
- **Category 5, Part 2** covers information security items, with exceptions for mass-market and publicly available software

Publicly available open source software is generally **not controlled** under Canadian export regulations.

### Canadian Sanctions

Canada maintains sanctions under the **Special Economic Measures Act (SEMA)** and **United Nations Act**. Operators are solely responsible for determining and complying with the sanctions laws applicable in their own jurisdiction. Canadian sanctions may differ from those imposed by other countries.

**The authors of this software provide no sanctions compliance services and accept no liability for sanctions violations by operators or users.**

---

## UNITED STATES: Export Control Status (For U.S. Users)

### U.S. Export Administration Regulations (EAR)

U.S. persons using or redistributing this software are subject to U.S. export controls.

This software is **publicly available source code** under U.S. law:
- **15 CFR 734.3(b)(3)** excludes "publicly available" technology and software from the EAR
- **15 CFR 734.7** defines what constitutes "published" software; however, 15 CFR 734.7(b) states that published encryption software classified under ECCN 5D002 **remains subject to the EAR** unless the notification requirements of 15 CFR 742.15(b) are met
- **15 CFR 734.17** addresses encryption source code export provisions
- **15 CFR 740.13(e)** (License Exception TSU) may provide additional authorization

### U.S. BIS Notification

Under 15 CFR 742.15(b), U.S. persons making encryption source code publicly available should notify BIS.

If you are a U.S. person redistributing this software, you should send notification to:
- `crypt@bis.doc.gov` (BIS)
- `enc@nsa.gov` (NSA)

### U.S. OFAC Sanctions

U.S. persons must comply with OFAC sanctions. Comprehensively sanctioned jurisdictions include:
- Cuba
- Iran
- North Korea (DPRK)
- Crimea, Donetsk, and Luhansk regions of Ukraine

Note: Syria's comprehensive sanctions program was revoked in mid-2025 (31 CFR Part 542 removed from CFR). Targeted sanctions on specific Syrian individuals remain in effect. Verify the current OFAC sanctions list before distribution.

### OFAC Informational Materials Exemption

OFAC sanctions programs contain exemptions for "informational materials" (**31 CFR 560.210(c)** and parallel provisions). However, 31 CFR 560.210(c) explicitly states that this exemption does not authorize transactions incident to the exportation of software subject to the Export Administration Regulations (EAR). Whether open-source software qualifies under the informational materials exemption is subject to OFAC interpretation and legal analysis. U.S. persons should consult qualified sanctions counsel for guidance specific to their circumstances.

---

## EUROPEAN UNION: Dual-Use Regulation (For EU Users)

### EU Dual-Use Regulation (EU 2021/821)

The European Union's Dual-Use Regulation (EU 2021/821) controls the export, brokering, technical assistance, transit, and transfer of dual-use items, including certain cryptographic software. As publicly available open source software, Spiral Pool may fall outside the scope of dual-use export controls under the General Software Note and General Technology Note, which exclude software and technology that is "in the public domain" from export controls.

However, EU operators should be aware:
- Re-export of this software to embargoed destinations may be restricted
- The Crypto Note exclusion may not apply if the software is modified with proprietary cryptographic enhancements
- Individual EU Member States may impose additional national controls

Operators within the EU are responsible for determining their obligations under the Dual-Use Regulation and any applicable national legislation. Consult a qualified legal professional for compliance guidance.

---

## Sanctions Awareness

Most jurisdictions maintain sanctions programs that restrict dealings with certain countries, entities, and individuals. Commonly sanctioned jurisdictions include Iran, North Korea (DPRK), and others depending on your jurisdiction. Note that sanctions programs change frequently (e.g., the U.S. revoked Syria's comprehensive sanctions in mid-2025) — always verify the current list for your jurisdiction.

**Operators are solely responsible for determining and complying with the sanctions laws applicable in their own jurisdiction.** Operators must:

1. Consult current official sanctions lists from their government
2. Seek qualified legal counsel for compliance guidance
3. Implement their own screening and compliance controls
4. Monitor for sanctions changes that may affect their operations

**The authors of this software provide no sanctions compliance services and accept no liability for sanctions violations by operators or users.**

---

## Cryptographic Functionality

This software includes standard cryptographic components:

| Component | Purpose | Standard |
|-----------|---------|----------|
| SHA-256/SHA-256d | Mining hash verification | Bitcoin protocol |
| Scrypt | Mining hash verification | Litecoin protocol |
| TLS 1.2/1.3 | Optional encrypted transport | IETF RFC 5246 (TLS 1.2), RFC 8446 (TLS 1.3) |
| Noise Protocol | Stratum V2 encryption | Public specification |
| AES-256-GCM | HA cluster message encryption | NIST SP 800-38D |
| HKDF-SHA256 | Key derivation from cluster token | IETF RFC 5869 |
| bcrypt | Dashboard password hashing | OpenBSD bcrypt |

These are standard, publicly documented cryptographic implementations used in accordance with published protocol specifications. No novel or proprietary cryptography is included.

---

## User Responsibilities

### All Users

By using this software, you acknowledge:

1. **Compliance Responsibility**: You are responsible for determining and complying with export control and sanctions laws applicable in your jurisdiction

2. **No Legal Advice**: This notice is informational only and does not constitute legal advice

3. **Publicly Available Software**: This software is publicly available without restriction

4. **Redistribution**: If you redistribute this software (modified or unmodified), you are responsible for your own export compliance

### Operators

Operators deploying this software are responsible for:
- Determining applicable laws in their jurisdiction
- Implementing any required access controls
- Not providing services to sanctioned persons or entities
- Consulting legal counsel for compliance guidance

---

## Software Neutrality

This software is a neutral, **non-custodial** infrastructure tool. Like web servers, databases, or operating systems, it can be used for lawful or unlawful purposes. The software does not take custody, control, or possession of any cryptocurrency — block rewards flow directly from the blockchain to the miner's wallet address via the coinbase transaction. The authors:
- Provide software functionality only
- Do not monitor, control, or restrict usage
- Are not responsible for how operators deploy the software
- Do not provide sanctions screening or compliance services

**This software does not implement geographic restrictions.** As open source software, it cannot verify user locations or enforce access controls. Operators must implement their own compliance controls.

---

## Summary

| Question | Answer |
|----------|--------|
| Where is this software from? | Canada |
| Is it subject to Canadian export controls? | Generally no - publicly available software |
| Is it subject to U.S. export controls? | U.S. persons: see U.S. section above |
| Can I download it? | Yes - no license required |
| Do I need sanctions compliance? | Yes - applicable laws depend on your location |

---

*This notice is provided for informational purposes. It is not legal advice. Users with specific compliance questions should consult qualified legal counsel in their jurisdiction.*

*Spiral Pool v2.0.0 - Export Control Notice*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
