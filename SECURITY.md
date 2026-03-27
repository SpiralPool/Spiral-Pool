# Security Policy

**Last Updated:** 2026-03-18

## Reporting a Vulnerability

If you discover a security vulnerability in Spiral Pool, please report it responsibly.

### Reporting Process

1. **Do NOT** open a public GitHub issue for security vulnerabilities
2. Send a detailed report to the project maintainers via GitHub's private vulnerability reporting feature
3. For time-sensitive advisories, you may also reach the project via [@SpiralMiner](https://x.com/SpiralMiner) on X (DMs open)
4. Include:
   - Description of the vulnerability
   - Steps to reproduce
   - Potential impact
   - Suggested fix (if any)

### Response Timeline

Response times vary based on maintainer availability. This is a volunteer-maintained project with no guaranteed response times or SLAs.

### Disclosure Policy

- Please allow reasonable time for a fix before public disclosure
- Reporters may be credited in security advisories upon request (or remain anonymous)
- No bug bounties are offered

## Security Considerations

### What This Software Does

Spiral Pool is mining pool software that:
- Accepts network connections from mining hardware
- Processes cryptographic share submissions
- Communicates with cryptocurrency full nodes
- Stores mining statistics in a database

### Known Security Limitations

1. **No Third-Party Audit**: This software has not been audited by third-party security professionals.

2. **Self-Hosted**: Security depends on your infrastructure configuration, network security, and operational practices.

3. **Crash Recovery**: The software includes crash recovery mechanisms, but data loss may occur during unexpected failures.

4. **Rate Limiting**: Rate limiting and banning features do NOT prevent all denial-of-service conditions.

### Deployment Recommendations

1. **Operator-controlled infrastructure preferred**: Deploy on bare metal servers under your physical control, or VMs on hypervisors you own. Cloud/VPS deployments are supported but carry serious risks (provider ToS violations, bandwidth billing, provider access to wallet credentials) — the installer requires written risk acknowledgment. See [WARNINGS.md](WARNINGS.md) and [CLOUD_OPERATIONS.md](docs/setup/CLOUD_OPERATIONS.md).
2. **x86_64 architecture only**: All packages and binaries target x86_64 (amd64). ARM/Raspberry Pi has not been tested.
3. **Network Isolation**: Run database and internal services on private networks
4. **Firewall Configuration**: Only expose stratum ports for your enabled coins (see [docs/reference/REFERENCE.md](docs/reference/REFERENCE.md) for port list) and necessary API ports
5. **TLS/SSL**: Use TLS for API endpoints if exposed externally
6. **Updates**: Monitor for security updates and apply promptly
7. **Monitoring**: Implement logging and alerting for security events
8. **Backups**: Maintain regular backups of database and configuration
9. **Access Control**: Use strong, unique passwords for all services

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.1.x   | Supported |
| 1.0.x   | Supported |

Security updates may be released for the current major version at maintainer discretion. No update schedule, timeline, or commitment is guaranteed. Support may be discontinued at any time without notice.

## Credential Security

### Command Line Passwords

**WARNING**: Passwords passed via command line arguments may be visible in:
- Process listings (`ps aux`, Task Manager)
- Shell history files (`~/.bash_history`, `~/.zsh_history`)
- System logs and audit trails

**Recommendations**:
1. Use configuration files for credentials (with appropriate file permissions)
2. Use environment variables: `export DB_PASSWORD="..."`
3. Clear shell history after entering sensitive commands: `history -c`
4. On Windows, avoid PowerShell history: `Clear-History`

### Configuration File Security

Configuration files may contain sensitive data:
- Database credentials
- RPC passwords for cryptocurrency nodes
- API keys for Discord/Telegram

**Recommendations**:
1. Set restrictive permissions: `chmod 600 config.yaml`
2. Store configs outside web-accessible directories
3. Use secrets management for production deployments
4. Never commit credentials to version control

## Security-Related Configuration

See the documentation for security-related configuration options:
- Rate limiting settings
- Connection limits
- Banning thresholds
- API authentication

## Incident Response Guidance

### For Operators

If you experience a security incident while running Spiral Pool:

#### Immediate Actions

1. **Isolate affected systems** - Disconnect from network if active compromise suspected
2. **Preserve evidence** - Do not delete logs or modify configurations
3. **Assess scope** - Determine what systems and data may be affected
4. **Notify stakeholders** - Inform miners/users if their data may be affected

#### Investigation Checklist

- [ ] Review authentication logs for unauthorized access
- [ ] Check for configuration file modifications
- [ ] Examine network connections for anomalies
- [ ] Review database access logs
- [ ] Check cryptocurrency wallet transactions
- [ ] Inspect system process list for unexpected processes

#### Log Locations

```
Pool logs (location set via logging.file in config YAML, or stdout/journalctl)
System logs (journalctl, /var/log/auth.log, etc.)
```

#### Recovery Steps

1. **Identify entry point** - How did the attacker gain access?
2. **Close vulnerabilities** - Patch, update, or reconfigure as needed
3. **Reset credentials** - Change all passwords, API keys, wallet addresses
4. **Restore from backup** - Use known-good configuration and data
5. **Monitor closely** - Watch for signs of persistent access

### Reporting Security Issues

If the incident involves a Spiral Pool vulnerability:

1. **Do NOT** disclose publicly until patched
2. Report via GitHub private vulnerability reporting
3. Provide detailed reproduction steps
4. Allow reasonable time for a fix

### What the Authors May Provide (No Commitment)

The following may be provided at maintainer discretion, but are NOT guaranteed:
- Security patches for reported vulnerabilities
- Security advisories for disclosed issues
- Updated releases with fixes

### What the Authors Do NOT Provide

- Incident response services
- Forensic investigation
- Legal or compliance advice
- 24/7 support

Operators are responsible for their own incident response capabilities.

## Operator Legal Protection

If you accept miners from the public, you should implement your own legal framework (terms of service, MOTD banners, privacy policies) appropriate to your operations and jurisdiction. Spiral Pool includes a built-in Stratum MOTD feature for this purpose.

See [OPERATIONS.md Section 10](docs/setup/OPERATIONS.md#10-operator-legal-protection-optional) for configuration details, implementation options, and suggested operator terms.

---

*Security is a shared responsibility. This policy describes how to report vulnerabilities and provides general security guidance. You are responsible for securing your deployment.*

*Spiral Pool v1.2.3 - Security Policy*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
