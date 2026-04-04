# Spiral Pool Documentation

---

## Recommended Reading Order

If you are new to Spiral Pool, read in this order:

1. [README.md](../README.md) — Project overview, features, and quick start
2. [WARNINGS.md](../WARNINGS.md) — Read before installing; specific hazard warnings
3. [OPERATIONS.md](setup/OPERATIONS.md) — Installation, configuration, and day-to-day operations
4. [CLOUD_OPERATIONS.md](setup/CLOUD_OPERATIONS.md) — Only if deploying on cloud/VPS
5. [REFERENCE.md](reference/REFERENCE.md) — Ports, CLI commands, API endpoints (lookup reference)
6. [ARCHITECTURE.md](architecture/ARCHITECTURE.md) — How it works under the hood

Legal documents ([LICENSE](../LICENSE), [TERMS.md](../TERMS.md), [PRIVACY.md](../PRIVACY.md), [SECURITY.md](../SECURITY.md), [EXPORT.md](../EXPORT.md)) should be reviewed before production deployment.

---

## Setup

Getting started, installation, and deployment guides.

| Document | Description |
|----------|-------------|
| [OPERATIONS.md](setup/OPERATIONS.md) | Installation, configuration, monitoring, HA setup, upgrading, troubleshooting |
| [CLOUD_OPERATIONS.md](setup/CLOUD_OPERATIONS.md) | Cloud/VPS deployment: dashboard SSH tunnel, firewall layout, SSH hardening, HTTPS options, provider-specific config |
| [UPGRADE_GUIDE.md](setup/UPGRADE_GUIDE.md) | Upgrading to v2.2.1 (Phi Hash Reactor): compatibility, all coin types, step-by-step |
| [DOCKER_GUIDE.md](setup/DOCKER_GUIDE.md) | Docker & WSL2 deployment guide (V1 + V2 single-coin and multi-coin mode) |
| [WINDOWS_GUIDE.md](setup/WINDOWS_GUIDE.md) | Windows installation: Docker Desktop vs WSL2 Native, decision tree, troubleshooting |

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
| [DASHBOARD.md](reference/DASHBOARD.md) | Spiral Dash: web dashboard, ~125 API routes, miner management, 25 themes |
| [EXTERNAL_ACCESS.md](reference/EXTERNAL_ACCESS.md) | External access: port forwarding, Cloudflare tunnels, hashrate marketplace integration |
| [MULTI_COIN_PORT.md](reference/MULTI_COIN_PORT.md) | Multi coin smart port: weighted time allocation, difficulty routing, miner setup, API |

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

*Spiral Pool -- Phi Hash Reactor 2.2.1*
