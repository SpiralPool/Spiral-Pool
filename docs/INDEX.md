# Spiral Pool Documentation

---

## Setup

Getting started, installation, and deployment guides.

| Document | Description |
|----------|-------------|
| [OPERATIONS.md](setup/OPERATIONS.md) | Installation, configuration, monitoring, HA setup, upgrading, troubleshooting |
| [CLOUD_OPERATIONS.md](setup/CLOUD_OPERATIONS.md) | Cloud/VPS deployment: dashboard SSH tunnel, firewall layout, SSH hardening, HTTPS options, provider-specific config |
| [UPGRADE_GUIDE.md](setup/UPGRADE_GUIDE.md) | Upgrading to v1.1.0 (Phi Forge): compatibility, all coin types, step-by-step |
| [DOCKER_GUIDE.md](setup/DOCKER_GUIDE.md) | Docker & WSL2 deployment guide (V1 single-coin mode) |

## Architecture

System design, security model, and technical internals.

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](architecture/ARCHITECTURE.md) | Technical architecture: Spiral Router, vardiff engine, share pipeline, security, HA, database schema |
| [SECURITY_MODEL.md](architecture/SECURITY_MODEL.md) | Security controls: FSM enforcement, JSON hardening, rate limiting, TLS, ban persistence |

## Reference

Quick-lookup tables, CLI commands, hardware support, and external access.

| Document | Description |
|----------|-------------|
| [REFERENCE.md](reference/REFERENCE.md) | Quick lookup: ports, CLI commands, API endpoints, miner classes, config fields |
| [spiralctl-reference.md](reference/spiralctl-reference.md) | Complete spiralctl CLI reference: all commands, options, examples |
| [MINER_SUPPORT.md](reference/MINER_SUPPORT.md) | Supported mining hardware: device APIs, auto-detection, monitoring capabilities |
| [SENTINEL.md](reference/SENTINEL.md) | Spiral Sentinel: monitoring, alerts, achievements, CoinGecko integration, 26 miner types |
| [DASHBOARD.md](reference/DASHBOARD.md) | Spiral Dash: web dashboard, ~125 API routes, miner management, 19 themes |
| [EXTERNAL_ACCESS.md](reference/EXTERNAL_ACCESS.md) | External access: port forwarding, Cloudflare tunnels, hashrate marketplace integration |

## Development

Guides for extending and testing Spiral Pool.

| Document | Description |
|----------|-------------|
| [COIN_ONBOARDING_SPEC.md](development/COIN_ONBOARDING_SPEC.md) | Adding new coin support: manifest entries, Go implementation, validation |
| [TESTING.md](development/TESTING.md) | Test suite reference: 3,500+ tests, chaos tests, fuzz tests |

## Legal & Policy

Legal documents are in the repository root:

| Document | Description |
|----------|-------------|
| [LICENSE](../LICENSE) | BSD-3-Clause License |
| [TERMS.md](../TERMS.md) | Terms of Use |
| [WARNINGS.md](../WARNINGS.md) | Specific Hazard Warnings |
| [PRIVACY.md](../PRIVACY.md) | Privacy Notice (GDPR/CCPA/PIPEDA) |
| [SECURITY.md](../SECURITY.md) | Security Policy, Incident Response |
| [EXPORT.md](../EXPORT.md) | Export Control and Sanctions Notice |
| [TRADEMARKS.md](../TRADEMARKS.md) | Third-Party Trademark Notice |
| [NOSEC.md](../NOSEC.md) | Security Architecture Decisions |
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Contribution Guidelines (DCO) |

---

*Spiral Pool -- Phi Forge 1.1.1*
