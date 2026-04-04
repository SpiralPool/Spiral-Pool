# Spiral Dash Reference

Spiral Dash is the real-time web dashboard for Spiral Pool. It provides fleet monitoring, miner control, analytics, and block tracking through a multi-theme web interface.

**Source:** `src/dashboard/dashboard.py`
**Service:** `spiraldash`
**Port:** 1618 (hardcoded, all interfaces)
**State directory:** `/spiralpool/dashboard/data/`

---

## Table of Contents

1. [Quick Reference](#quick-reference)
2. [Pages](#pages)
3. [Configuration](#configuration)
4. [Authentication](#authentication)
5. [Themes](#themes)
6. [Dashboard Features](#dashboard-features)
7. [Miner Management](#miner-management)
8. [API Endpoints](#api-endpoints)
9. [WebSocket](#websocket)
10. [Dependencies](#dependencies)
11. [HA Behavior](#ha-behavior)

---

## Quick Reference

```bash
# Service management
systemctl status spiraldash
systemctl restart spiraldash

# Access
http://YOUR_SERVER_IP:1618

# Health checks
curl http://localhost:1618/api/health/live     # Liveness (always 200)
curl http://localhost:1618/api/health/ready     # Readiness (checks pool API)
```

---

## Pages

| URL | Description |
|-----|-------------|
| `/` | Main dashboard &mdash; fleet stats, hashrate charts, miner cards, earnings, health |
| `/setup` | First-time setup wizard &mdash; coin selection, miner config, pool mode |
| `/settings` | Settings &mdash; theme, devices, wallet, alerts, auto-discovery, webhooks |
| `/login` | Login page &mdash; also handles first-time password creation |

---

## Configuration

### Config Files

| File | Path | Purpose |
|------|------|---------|
| `dashboard_config.json` | `/spiralpool/dashboard/data/` | Main dashboard config |
| `dashboard_stats.json` | `/spiralpool/dashboard/data/` | Lifetime statistics |
| `auth.json` | `/spiralpool/dashboard/data/` | Password hash |
| `secret_key` | `/spiralpool/dashboard/data/` | Flask session secret (64 hex chars, persisted) |

### Config Keys (dashboard_config.json)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `dashboard_title` | string | `"My Solo Pool"` | Dashboard title |
| `first_run` | bool | `true` | Shows setup wizard on first access |
| `refresh_interval` | int | `30` | Dashboard refresh interval (seconds) |
| `theme` | string | `"cyberpunk"` | Active theme ID |
| `display_currency` | string | `"CAD"` | Fiat currency for earnings |
| `devices` | object | 26 type arrays | Miner device lists (see below) |
| `power_cost.currency` | string | `"CAD"` | Power cost currency |
| `power_cost.rate_per_kwh` | float | `0.12` | Electricity rate per kWh |
| `power_cost.is_free_power` | bool | `false` | Free power mode |

### Device Types in Config (26 types)

Each key is a miner type with an array of device objects `{ip, name, port, password?}`. Types are protocol-based (not algorithm-based) — the same type supports both SHA-256d and Scrypt hardware where applicable.

**AxeOS HTTP API** (port 80): `axeos`, `nmaxe`, `nerdqaxe`, `qaxe`, `qaxeplus`, `luckyminer`, `jingleminer`, `zyber`, `hammer` (Scrypt), `esp32miner`
**CGMiner TCP API** (port 4028): `avalon`, `antminer` (SHA-256d), `antminer_scrypt` (Scrypt L-series), `whatsminer`, `innosilicon`, `futurebit`, `canaan`, `ebang`, `gekkoscience`, `ipollo`, `elphapex` (Scrypt)
**Goldshell HTTP REST** (port 80): `goldshell` (Scrypt)
**ePIC HTTP REST** (port 4028): `epic`
**Custom firmware**: `braiins`, `vnish`, `luxos`

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DASHBOARD_AUTH_ENABLED` | `"true"` | Enable/disable authentication |
| `DASHBOARD_ADMIN_PASSWORD` | `""` | Admin password override |
| `DASHBOARD_API_KEY` | `""` | API key for programmatic access (X-API-Key header) |
| `DASHBOARD_SESSION_LIFETIME` | `"24"` | Session timeout (hours) |
| `DASHBOARD_SECURE_COOKIES` | `"false"` | Require HTTPS for cookies |
| `DASHBOARD_CORS_ORIGINS` | `""` | Allowed CORS origins (comma-separated) |
| `POOL_API_URL` | `"http://127.0.0.1:4000"` | Spiral Stratum API |
| `PROMETHEUS_URL` | `"http://127.0.0.1:9100"` | Prometheus metrics endpoint |
| `SPIRAL_METRICS_TOKEN` | `""` | Bearer token for /metrics |
| `SPIRAL_ADMIN_API_KEY` | `""` | Stratum admin API key |
| `POOL_ID` | `""` | Override pool ID (auto-detected if empty) |
| `HA_STATUS_PORT` | `"5354"` | HA VIP manager status port |
| `SPIRALPOOL_INSTALL_DIR` | `"/spiralpool"` | Installation directory |

---

## Authentication

**Single-user admin model.** One username (`admin`), one password.

| Feature | Implementation |
|---------|---------------|
| Password hashing | bcrypt (preferred) or SHA-256 with random salt (fallback) |
| Timing attack protection | `hmac.compare_digest()` for constant-time comparison |
| Session lifetime | 24 hours (configurable via `DASHBOARD_SESSION_LIFETIME`) |
| Session fixation | Session regenerated on login |
| Cookie flags | `HttpOnly=True`, `SameSite=Strict` |
| Rate limiting | 5 failed attempts per IP within 5 min triggers lockout |
| CSRF protection | Origin/Referer header validation for POST/PUT/DELETE/PATCH |
| API key access | `X-API-Key` header (headers only; query params rejected and logged) |
| First-time setup | If no password exists, login page becomes "Create Password" form (min 8 chars) |

Auth can be disabled with `DASHBOARD_AUTH_ENABLED=false` (not recommended).

---

## Themes

25 themes available in `src/dashboard/static/themes/`:

| Theme | Category |
|-------|----------|
| **V1.0 — Black Ice** | Codename |
| **V1.2.2 — Convergent Spiral** | Codename |
| **V2.0 — Phi Hash Reactor** | Codename |
| **V2.2 — Phi Forge** | Codename |
| **cyberpunk** (default) | Core |
| 1337-h4x0r | Core |
| dracula | Developer |
| gruvbox-dark | Developer |
| nord | Developer |
| tokyo-night | Developer |
| midnight-aurora | Nature |
| ocean-depths | Nature |
| solar-flare | Nature |
| wood-paneling | Nature |
| autumn-harvest | Seasonal |
| spring-bloom | Seasonal |
| summer-vibes | Seasonal |
| winter-frost | Seasonal |
| bitcoin-laser | Fun |
| pixel-arcade | Fun |
| rainbow-unicorn | Fun |
| vaporwave | Fun |
| chrome-warfare | Industrial |
| meltdown | Industrial |
| nebula-command | Industrial |

Each theme is a JSON file defining CSS variables (`colors`, `gradients`). Theme manager (`theme-manager.js`) applies variables client-side. Theme ID sanitized with `^[a-zA-Z0-9_-]+$` regex to prevent path traversal.

Codename themes correspond to Spiral Pool release versions. Developer themes use color palettes derived from Dracula, Nord, Tokyo Night, and Gruvbox (MIT licensed). See `src/dashboard/static/themes/THEME-LICENSES.txt`.

### Background Patterns

Each theme includes a `backgroundStyle` in its `effects` object, controlling the background pattern overlay. Available styles:

| Style | Description | Credit |
|-------|-------------|--------|
| `grid` | Classic line grid | Spiral Pool |
| `dots` | Dot matrix | [MagicPattern](https://www.magicpattern.design/tools/css-backgrounds) |
| `hexagons` | Honeycomb mesh | Spiral Pool |
| `carbon` | Dark textured weave | [Lea Verou's CSS3 Patterns Gallery](https://projects.verou.me/css3patterns/) (MIT) |
| `blueprint` | Major + minor engineering grid | [Lea Verou's CSS3 Patterns Gallery](https://projects.verou.me/css3patterns/) (MIT) |
| `checkerboard` | Subtle offset squares | [Lea Verou's CSS3 Patterns Gallery](https://projects.verou.me/css3patterns/) (MIT) |
| `diagonal` | 45-degree repeating stripes | [Lea Verou's CSS3 Patterns Gallery](https://projects.verou.me/css3patterns/) (MIT) |
| `starfield` | Scattered stars at varying sizes | Spiral Pool |
| `crosshatch` | Diagonal cross-hatch lines | Spiral Pool |
| `crt` | LCD/CRT pixel grid | Spiral Pool |
| `none` | No pattern | — |

All patterns use theme CSS variables for colors and adapt automatically to any theme.

### Custom Theme Editor

The Appearance panel includes a built-in theme editor for creating custom themes without editing JSON files.

- **13 color pickers**: background, cards, 8 accent colors, text primary/secondary, border color
- **Border radius selector**: Sharp (0px) to Extra Round (16px)
- **Background style selector**: choose from 11 background patterns
- **Live preview**: changes apply instantly as colors are picked
- **Save**: persists to browser `localStorage`, appears in theme dropdown under "Custom" group
- **Export**: downloads as a standard `.json` theme file
- **Import**: load any `.json` theme file (including exported themes from other users)

Custom themes are stored client-side only — they do not modify server files.

---

## Dashboard Features

### Top Stats Bar (always visible)

- Farm hashrate (TH/s)
- Power consumption (watts)
- Accepted shares
- Blocks found
- Network difficulty
- Last block found (time + finder)
- Estimated Time to Block (ETB) + 24h probability
- Best difficulty (session / all-time)
- Miners online / total + average temperature

### Draggable Sections

All sections below the stats bar are drag-to-reorder (SortableJS). Order persisted to `localStorage`. Sections can be collapsed/expanded individually.

| Section | Content |
|---------|---------|
| **Lifetime Statistics** | Total shares, blocks found, best share, uptime. Clearable. |
| **Hashrate History** | Chart.js line chart with period selector: 1h, 24h, 7d, 30d |
| **Mining Devices** | Miner cards grid (grouped or flat view). Per-miner: hashrate, power, temp, fan, shares, status, pool URL. Click for detail modal with per-miner chart. |
| **Earnings Calculator** | Block reward, coin price (multi-currency), expected blocks/month, per-block value, monthly average, electricity cost (daily/monthly kWh, cost, net profit, margin) |
| **System Health** | Stratum status + blockchain node health cards. Per-node: sync progress, connections, block height. Restart buttons. |
| **Activity Feed** | Unified event timeline (blocks, alerts, miner events) |

### Layout Templates

5 built-in templates controlling which sections/sub-components are visible:

| Template | Focus |
|----------|-------|
| `spiral-pool-default` | Full experience (all sections) |
| `mining-ops-center` | Operations (no lifetime stats, no earnings calc) |
| `lottery-lucky` | Luck/lottery focused |
| `power-efficiency` | Power efficiency focused |
| `compact-minimal` | Minimal dashboard |

### PWA Support

Progressive Web App manifest (`static/manifest.json`) enables "Add to Home Screen" on mobile.

---

## Miner Management

### Adding Miners

1. **Network auto-scan** (`/api/scan/start`): Scans local subnet for miner API ports
2. **Import from Sentinel** (`/api/miners/import-from-sentinel`): Reads `/spiralpool/data/miners.json`
3. **Manual** via setup wizard or settings page: Add devices by IP per type

### Device Monitoring

- Background thread polls all configured devices every 60 seconds
- Per-miner data: hashrate, power, temperature, fan speed, shares, firmware, pool URL, hostname, uptime, efficiency (J/TH)
- Per-miner history: 7 days at 1-minute intervals
- Firmware tracking across fleet
- Downtime tracking with timestamps and durations

### Device Control

| Action | Supported Devices |
|--------|------------------|
| Restart | AxeOS, CGMiner, Goldshell, BraiinsOS, Vnish, LuxOS |
| Frequency change | AxeOS, CGMiner |
| Voltage change | AxeOS |
| Fan speed | AxeOS, CGMiner |
| Pool URL change | AxeOS, BraiinsOS, Vnish, LuxOS, CGMiner |
| WiFi config | AxeOS |
| Hostname | AxeOS |
| Overclock profiles | AxeOS |

### Fleet Groups

- Create named groups of miners
- Batch restart or batch pool-change per group
- Fleet status overview (total, online, offline, by type, by algorithm)

### Avalon Power Scheduling

- Per-device time-based power profiles (efficiency/performance)
- LED celebration on block find (1 hour, 10 rotating patterns)
- Quiet hours suppression

### Local Pool Detection

Determines if a miner is connected to the local Spiral Stratum or an external pool by comparing miner-reported pool URL against local IPs, VIP address, and stratum ports.

---

## API Endpoints

~125 route definitions. All non-login routes require authentication via session cookie or `X-API-Key` header. Admin-only routes additionally validate CSRF headers.

### Auth

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/auth/status` | User | Authentication status |
| POST | `/api/auth/change-password` | Admin | Change password |

### Config

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/config` | User | Current config (secrets redacted) |
| POST | `/api/config` | Admin | Update configuration |
| GET | `/api/config/server-mode` | User | Auto-detect pool mode from stratum |

### Miners

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/miners` | User | All miner data from cache |
| POST | `/api/miners/refresh` | User | Force re-poll all devices |
| GET | `/api/miner/<name>/history` | User | Per-miner hashrate history |
| GET | `/api/miner/<name>/stats` | User | Detailed miner stats |
| POST | `/api/miners/import-from-sentinel` | Admin | Import from Sentinel database |
| POST | `/api/miners/sync-to-sentinel` | Admin | Sync back to Sentinel database |

### Device Control

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/device/test` | Admin | Test device connectivity |
| POST | `/api/device/restart` | Admin | Restart miner |
| POST | `/api/device/frequency` | Admin | Change mining frequency |
| POST | `/api/device/overclock` | Admin | Apply overclock profile |
| POST | `/api/devices/add-discovered` | Admin | Add devices from network scan |

### Miner-Specific APIs

Per-device-type control endpoints for AxeOS, CGMiner, Whatsminer, BraiinsOS, Vnish, LuxOS:

`/api/miner/axeos/{stats,restart,frequency,voltage,fan,pool,wifi,hostname}`
`/api/miner/cgminer/{stats,restart,pools,fan,frequency}`
`/api/miner/whatsminer/info`
`/api/miner/braiins/pool`, `/api/miner/vnish/pool`, `/api/miner/luxos/pool`

### Network Scan

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/scan/start` | Admin | Start subnet scan |
| GET | `/api/scan/status` | User | Scan progress |
| GET | `/api/scan/subnet` | User | Detected local subnet |

### Pool Stats

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/pool/stats` | User | Pool stats from stratum |
| GET | `/api/pool/metrics` | User | Prometheus metrics |
| GET | `/api/pool/history` | User | Historical chart data |
| GET | `/api/combined` | User | All stats in one call |

### Blocks

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/blocks/found` | User | Found blocks (with coin filter) |
| GET | `/api/blocks/<hash>` | User | Block details |
| GET | `/api/blocks/finder/<hash>` | User | Which miner found a block |
| GET | `/api/blocks/history` | User | Block find history with time ranges |
| GET | `/api/blocks/leaderboard` | User | Miner leaderboard by blocks found |

### Fleet Management

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/fleet/status` | User | Fleet overview |
| GET | `/api/fleet/by-algorithm` | User | Fleet grouped by algorithm |
| GET/POST | `/api/fleet/groups` | Varies | List/create miner groups |
| PUT/DELETE | `/api/fleet/groups/<name>` | Admin | Update/delete group |
| POST | `/api/fleet/batch/restart` | Admin | Batch restart group |
| POST | `/api/fleet/batch/pool` | Admin | Batch pool URL change |
| GET/POST | `/api/fleet/maintenance` | Varies | Maintenance mode |

### Analytics

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/analytics/hashrate` | User | Hashrate analytics |
| GET | `/api/analytics/shares` | User | Share acceptance analytics |
| GET | `/api/analytics/temperature` | User | Temperature analytics |
| GET | `/api/analytics/efficiency` | User | Power efficiency (J/TH per miner) |
| GET | `/api/analytics/heatmap` | User | Share submission heatmap (24h x 7d) |
| GET | `/api/shares/rejection-analysis` | User | Share rejection analysis |
| GET | `/api/uptime/report` | User | Uptime report |

### Health & HA

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/health` | User | Pool + node health |
| GET | `/api/health/live` | User | Liveness probe (always 200) |
| GET | `/api/health/ready` | User | Readiness probe |
| GET | `/api/ha/status` | User | HA cluster status |

### Nodes

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/nodes` | User | All coin nodes |
| GET | `/api/nodes/<symbol>` | User | Specific node status |
| POST | `/api/nodes/<symbol>/restart` | Admin | Restart coin daemon |
| POST | `/api/nodes/<symbol>/stop` | Admin | Stop coin daemon |
| POST | `/api/nodes/<symbol>/start` | Admin | Start coin daemon |
| GET | `/api/nodes/<symbol>/sync` | User | Sync progress |
| POST | `/api/stratum/restart` | Admin | Restart stratum service |

### Management

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/system/info` | User | System resources, services, disks, version |
| POST | `/api/services/<service>/<action>` | Admin | Start/stop/restart a pool service |
| GET | `/api/logs/<service>?lines=N` | Admin | Recent journalctl logs (10-500 lines) |
| GET | `/api/system/updates` | Admin | Check available apt packages |
| POST | `/api/system/updates/refresh` | Admin | Run apt-get update |
| POST | `/api/system/updates/apply` | Admin | Apply available upgrades (apt-get upgrade) |

### Data Export

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/export/blocks?format=csv\|json` | User | Block history with fiat values in user's currency |
| GET | `/api/export/earnings?format=csv\|json` | User | Daily earnings summary with wallet balance |
| GET | `/api/export/hashrate?format=csv\|json&period=24h` | User | Hashrate history (1h/6h/24h/7d/30d) |

### Financial

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/etb` | User | Estimated Time to Block |
| GET | `/api/luck` | User | Mining luck statistics |
| GET | `/api/difficulty/predict` | User | Difficulty prediction |
| GET/POST | `/api/power/config` | Varies | Power cost configuration |
| GET | `/api/power/stats` | User | Power consumption stats |
| GET | `/api/power/efficiency` | User | Power efficiency stats |

### Themes & Webhooks

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/themes` | User | List all themes |
| GET | `/api/themes/<id>` | User | Full theme data |
| POST | `/api/webhook/test/discord` | Admin | Send test Discord message |
| POST | `/api/webhook/validate/telegram` | Admin | Validate Telegram credentials |

---

## WebSocket

Flask-SocketIO with `async_mode='threading'`.

### Server Events

| Event | Frequency | Data |
|-------|-----------|------|
| `realtime_update` | Every 5s | Pool hashrate, connected miners, shares/sec, blocks, farm hashrate, power, ETB, HA status |
| `block_found` | On event | Block coin, height, hash, miner, worker, celebration flag |
| `alert` | On event | Alert details |

### Client Events

| Event | Purpose |
|-------|---------|
| `connect` | Client connected |
| `disconnect` | Client disconnected |
| `subscribe` | Subscribe to channels |

> **Note:** The main dashboard template currently uses polling via `/api/combined` with `setInterval` rather than WebSocket subscriptions. The WebSocket channel is available for external consumers or future frontend use.

---

## Dependencies

From `src/dashboard/requirements.txt`:

| Package | Version | Purpose |
|---------|---------|---------|
| flask | 3.1.2 | Web framework |
| flask-socketio | 5.6.0 | WebSocket support |
| flask-login | 0.6.3 | Session authentication |
| requests | 2.32.5 | HTTP client (pool API, miner APIs) |
| werkzeug | 3.1.5 | WSGI utilities |
| gunicorn | 25.1.0 | Production WSGI server |
| gevent | 25.9.1 | Async networking |
| pyyaml | 6.0.3 | YAML parsing (stratum config) |
| bcrypt | 4.1.2 | Password hashing (optional, fallback to SHA-256) |

**CDN libraries:** Chart.js 4.5.1, SortableJS 1.15.7
**Fonts (Google CDN):** Orbitron, Rajdhani, Share Tech Mono

### Content Security Policy

```
default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net;
style-src 'self' 'unsafe-inline' https://fonts.googleapis.com;
font-src 'self' https://fonts.gstatic.com;
img-src 'self' data:; connect-src 'self' ws: wss:;
object-src 'none'; frame-ancestors 'none';
```

---

## HA Behavior

### BACKUP Node Optimization

On BACKUP nodes, the background data collection thread **skips all polling** (pool stats, Prometheus, miner polling, alerts, analytics) and only refreshes HA status every 60 seconds. This prevents duplicate polling.

### HA Status Detection

- Queries `http://127.0.0.1:5354/status` (VIP manager) every cycle
- Exponential backoff on failure (5s to 5min)
- Resets on success

### VIP-Aware

- Local pool address detection includes VIP address when HA is enabled
- Miners connected via VIP are correctly identified as "local pool" connections
- WebSocket broadcasts include HA data (role, state, VIP, healthy nodes)

### Service Control

Dashboard runs on ALL HA nodes but is started/stopped by `ha-service-control.sh` during promotion/demotion. On BACKUP nodes it is accessible but shows stale/minimal data. VIP routes users to the MASTER node's dashboard.

---

*Spiral Dash &mdash; Phi Hash Reactor 2.2.1*
