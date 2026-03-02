# Miner Device Support Reference

This document details the mining hardware supported by Spiral Dash, Spiral Sentinel, and the `spiralctl scan` utility, including API protocols, auto-detection capabilities, and known limitations.

## Support Tiers

| Tier | Description | Auto-Scan | Dashboard Monitoring | Sentinel Alerts |
|------|-------------|-----------|---------------------|-----------------|
| **Full** | Complete API integration with verified endpoints | Yes | Yes | Yes |
| **Best-Effort** | CGMiner TCP probe; may require manual enablement | Partial | Yes (if CGMiner enabled) | Yes (if CGMiner enabled) |
| **Manual Only** | Cannot be auto-detected; must be added via settings | No | Yes (if CGMiner enabled) | Yes (if CGMiner enabled) |

---

## Full Support (Auto-Scan + Full Monitoring)

### AxeOS HTTP API (Port 80)

Detected via `GET /api/system/info`. Fully automatic.

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| BitAxe Supra/Ultra/Gamma/Hex | `axeos` | 15W | Generic AxeOS family |
| NMaxe | `nmaxe` | 15W | BitAxe derivative |
| NerdQAxe++ | `nerdqaxe` | 15W | Includes NerdOctaxe |
| QAxe | `qaxe` | 80W | Quad-ASIC (~2 TH/s) |
| QAxe+ | `qaxeplus` | 100W | Enhanced QAxe |
| Lucky Miner LV06/LV07/LV08 | `luckyminer` | 50W | AxeOS-based |
| Jingle Miner BTC Solo Pro/Lite | `jingleminer` | 100W | AxeOS-based |
| Zyber TinyChipHub | `zyber` | 100W | AxeOS-based |
| Hammer/Heatbit | `hammer` | 25W | Scrypt AxeOS variants |

### Pool API (No Direct Device API)

ESP32 lottery miners (NerdMiner, ESP32 Miner V2, BitMaker, etc.) have **no HTTP or CGMiner API**. They communicate exclusively via Stratum protocol. Sentinel monitors them by polling the pool's connections and worker stats APIs instead of querying the device directly.

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| ESP32 Miner / NerdMiner | `esp32miner` | 2W | Lottery miner; stats from pool API |

**Requirements for ESP32 monitoring:**
1. **Manual configuration** — Must be added via `spiralctl scan --add <IP>` (select type `esp32miner`). Cannot be auto-discovered since no open ports to probe.
2. **`pool_admin_api_key`** — The pool connections endpoint is admin-only. This key is set automatically during install.
3. **Worker name** — When adding the miner, you must provide the Stratum worker name (the part after the dot in `ADDRESS.workername`). This is how the pool identifies the device.
4. **Active connection** — The ESP32 must be connected to the pool and mining. Offline ESP32 miners report as offline (no cached state).

**What Sentinel can track for ESP32 miners:** Online/offline status, hashrate (from pool), accepted/rejected shares, current difficulty.
**What Sentinel cannot track:** Temperature, fan speed, uptime, power consumption (no device API to query).

### Goldshell HTTP API (Port 80)

Detected via `GET /mcb/cgminer?cgminercmd=summary` (mining stats) and `GET /mcb/status` (device info, optional). Fully automatic.

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Goldshell Mini-DOGE/Box/HS-Box/etc. | `goldshell` | 200W | All Goldshell models |

### Bitmain Antminer (CGMiner TCP, Port 4028)

Detected via CGMiner `summary` + `stats` commands (hashrate, device model, temperature). CGMiner enabled by default on all Antminers.

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Antminer S19/S19 Pro/S19j Pro/S19 XP | `antminer` | 3250W | SHA-256 |
| Antminer S21/S21 Pro | `antminer` | 3500W | SHA-256 |
| Antminer T21 | `antminer` | 3276W | SHA-256 |
| Antminer L3+/L7/L9 | `antminer_scrypt` | 3000W | Scrypt |

### MicroBT Whatsminer (CGMiner TCP, Port 4028)

Detected via CGMiner `summary` + `stats` commands. Uses BTMiner (CGMiner-compatible).

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Whatsminer M30S/M30S+/M30S++ | `whatsminer` | 3400W | SHA-256 |
| Whatsminer M50/M50S/M56S | `whatsminer` | 3400W | SHA-256 |
| Whatsminer M60/M60S/M63S | `whatsminer` | 3400W | SHA-256 |

### Canaan AvalonMiner (CGMiner TCP, Port 4028)

Detected via CGMiner `summary` + `stats` commands; model identified from `stats` ID field containing "AVA" prefix.

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| AvalonMiner A12/A13/A14 series | `canaan` | 3000W | SHA-256 |

### Avalon Nano (CGMiner TCP, Port 4028)

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Avalon Nano 3/3s | `avalon` | (from device) | AC-powered desktop miner; power read from CGMiner stats MPO/PS fields |

### FutureBit Apollo (BFGMiner TCP, Port 4028)

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| FutureBit Apollo BTC/LTC | `futurebit` | 200W | CGMiner-compatible BFGMiner |

### GekkoScience (CGMiner TCP, Port 4028)

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| GekkoScience Compac F/R606/NewPac | `gekkoscience` | 5W | USB stick miners |

### Ebang/Ebit (CGMiner TCP, Port 4028)

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Ebang/Ebit E9/E10/E11/E12 | `ebang` | 2800W | SHA-256 |

### ePIC BlockMiner (HTTP REST, Port 4028)

**IMPORTANT**: ePIC uses an HTTP REST API on port 4028, NOT the CGMiner TCP socket protocol. Auto-detected via `GET http://<ip>:4028/summary`.

| Device | Type Key | Default Power | Default Credentials | Notes |
|--------|----------|---------------|---------------------|-------|
| ePIC BlockMiner 520i/720i/eLITE 1.0 | `epic` | 3000W | root / letmein | HTTP Basic Auth |

Endpoints used: `/summary`, `/hashrate`, `/fanspeed`, `/capabilities`

---

## Custom Firmware (Full Support with Manual Setup)

These require the firmware password to be configured in Spiral Dash settings.

### BraiinsOS / BOS+ (REST API, Port 80)

**Cannot be auto-scanned** (requires authentication). Must be added manually in settings.

| Device | Type Key | Default Power | Default Credentials | Notes |
|--------|----------|---------------|---------------------|-------|
| Any Antminer running BraiinsOS | `braiins` | 3250W | root / (empty) | Bearer token auth |

API: `/api/v1/auth/login`, `/api/v1/miner/stats`, `/api/v1/cooling/state`, `/api/v1/miner/details`

**Supported features**: Hashrate (GH/s), power consumption (watts), chip temperature, fan RPMs, accepted/rejected/stale shares, uptime, found blocks.

**Limitations**:
- No auto-scan: BraiinsOS requires authentication for all API endpoints. The network scanner cannot probe without credentials.
- Per-hashboard temperature: Only the highest temperature is returned by `/api/v1/cooling/state`. Individual hashboard temps are not available through the REST API.
- Legacy BOSminer API (CGMiner on port 4028) is deprecated; we use the Public REST API exclusively.

### Vnish Firmware (REST API Port 80 + CGMiner RPC Port 4028)

**Cannot be auto-scanned** (requires authentication). Must be added manually in settings.

| Device | Type Key | Default Power | Default Credentials | Notes |
|--------|----------|---------------|---------------------|-------|
| Any Antminer running Vnish | `vnish` | 3250W | (password) admin | Dual-API approach |

Vnish exposes two APIs:
1. **Web REST API (port 80)**: `/api/v1/unlock`, `/api/v1/summary`, `/api/v1/metrics`, `/api/v1/info`
2. **CGMiner-compatible RPC (port 4028)**: Traditional `summary`, `stats`, `pools` commands

Spiral Dash uses both: CGMiner RPC for hashrate/temps/fans (more reliable), Web API for power/model/status.

**Supported features**: Hashrate, temperatures, fan speeds, power consumption, accepted/rejected shares, uptime.

**Limitations**:
- No auto-scan: Vnish requires password authentication. Must be manually added.
- Auth token format: Vnish uses a plain token in the `Authorization` header (not `Bearer`) for data endpoints. Only `system/*` POST commands use `Bearer` prefix.
- Built-in API docs available at `http://<miner>/docs/`.

### LuxOS Firmware (CGMiner-compatible TCP, Port 4028)

**Cannot be auto-scanned** (firmware-specific responses not distinguishable from stock CGMiner). Must be added manually.

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Any Antminer running LuxOS | `luxos` | 3250W | CGMiner-compatible TCP socket |

API: Standard CGMiner commands plus LuxOS-specific `temps` and `fans` commands.

**Supported features**: Hashrate, temperatures (chip + board), fan speeds, power, shares, uptime.

**Limitations**:
- No auto-scan: LuxOS CGMiner responses look similar to stock Antminer firmware. Cannot be distinguished automatically without firmware-specific probing.
- Must be manually added as type `luxos` in settings to use LuxOS-specific features (dedicated `temps`/`fans` commands).

---

## Best-Effort Support (CGMiner May Need Manual Enablement)

These devices use proprietary web APIs as their primary interface. CGMiner TCP on port 4028 may or may not be available depending on firmware version and configuration. Monitoring will work if CGMiner is enabled, but auto-scan success is not guaranteed.

### iPollo (LuCI Web API Primary, CGMiner Secondary)

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| iPollo V1/V1 Mini/G1 | `ipollo` | 2000W | OpenWrt/LuCI-based firmware |

**Primary API**: LuCI CGI web interface on port 80 (not currently implemented for direct querying).
**Secondary API**: Modified CGMiner on port 4028 — may be **disabled by default** (requires `--api-listen` flag).

**Limitations**:
- Auto-scan: Will only detect iPollo if CGMiner API is enabled on port 4028. Many iPollo units ship with this disabled.
- If CGMiner is not available, the device must be manually added. Monitoring will show as offline until CGMiner is enabled.
- To enable CGMiner API: Check iPollo web interface settings or SSH into the miner and add `--api-listen --api-network --api-allow W:0/0` to the cgminer startup flags.
- pyasic (the major Python ASIC library) does NOT support iPollo, confirming limited API availability.

### Elphapex (LuCI Web API Primary, CGMiner Unconfirmed)

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Elphapex DG1/DG1+/DG Home | `elphapex` | 3000W | Scrypt ASIC miners |

**Primary API**: LuCI CGI web interface on port 80 with custom endpoints like `/cgi-bin/luci/setworkmode.cgi`.
**CGMiner TCP on port 4028**: NOT confirmed available. May not be exposed.

**Limitations**:
- Auto-scan: CGMiner detection is best-effort. If the device does not respond on port 4028, it will not be auto-detected.
- Must be manually added if auto-scan fails.
- Default web credentials: root / root.
- Full web API integration not implemented (would require reverse-engineering LuCI CGI endpoints).

### Innosilicon (HTTP REST Primary, CGMiner Disabled by Default)

| Device | Type Key | Default Power | Notes |
|--------|----------|---------------|-------|
| Innosilicon A10/A10 Pro/A11/T2T/T3 | `innosilicon` | 1500W | DragonMint-derived firmware |

**Primary API**: HTTP REST on port 80 with JWT authentication (documented by [dragon-rest](https://github.com/brndnmtthws/dragon-rest)).
**CGMiner TCP on port 4028**: **Disabled by default**. Must be manually enabled.

**Limitations**:
- Auto-scan: Will only work if CGMiner has been manually enabled. Most Innosilicon miners ship with CGMiner API disabled.
- To enable CGMiner: Telnet to port 8100 (default password: `innot1t2` or `t1t2t3a5`), then edit the config to add `--api-listen --api-network --api-allow W:0/0` and reboot.
- For Innosilicon A9 Zmaster: SSH as root (password: `blacksheepwall`), edit `/etc/systemd/system/multi-user.target.wants/cgminer.service`.
- Full HTTP REST API integration is not yet implemented (would provide complete monitoring without CGMiner enablement).

---

## Alert Coverage (Spiral Sentinel)

Spiral Sentinel monitors all configured devices for these alert conditions:

| Alert Type | AxeOS | Goldshell | CGMiner (Antminer/Whatsminer/etc.) | BraiinsOS | Vnish | ePIC | LuxOS | ESP32 (Pool API) |
|------------|-------|-----------|-----------------------------------|-----------|-------|------|-------|------------------|
| Offline | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes |
| Temp Warning (>80C) | Yes | Yes | Yes | Yes | Yes | Yes | Yes | N/A |
| Temp Critical (>95C) | Yes | Yes | Yes | Yes | Yes | Yes | Yes | N/A |
| Thermal Shutdown (>105C) | Yes* | N/A | Yes | Yes | Yes | Yes | Yes | N/A |
| Fan Failure (0 RPM) | N/A | N/A | Yes | Yes | Yes | Yes | Yes | N/A |
| Hashboard Dead | N/A | N/A | Yes | N/A | N/A | N/A | N/A | N/A |
| HW Error Rate | N/A | N/A | Yes | N/A | N/A | N/A | N/A | N/A |
| Stratum URL Mismatch | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| Hashrate Drop | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes |

\* Thermal shutdown alert for AxeOS covers: bitaxe, nerdaxe, luckyminer, jingleminer, zyber types.

ESP32 (Pool API): ESP32 miners have no device API. Online/offline and hashrate are tracked via the pool's stratum connections endpoint. Temperature, fan, and hardware alerts are not available.

---

## Device Type Quick Reference

Complete mapping of type keys to device families:

| Type Key | Family | API Protocol | Port | Auto-Scan |
|----------|--------|-------------|------|-----------|
| `axeos` | BitAxe (generic) | AxeOS HTTP | 80 | Yes |
| `nmaxe` | NMaxe | AxeOS HTTP | 80 | Yes |
| `nerdqaxe` | NerdQAxe++/NerdOctaxe | AxeOS HTTP | 80 | Yes |
| `qaxe` | QAxe | AxeOS HTTP | 80 | Yes |
| `qaxeplus` | QAxe+ | AxeOS HTTP | 80 | Yes |
| `luckyminer` | Lucky Miner | AxeOS HTTP | 80 | Yes |
| `jingleminer` | Jingle Miner | AxeOS HTTP | 80 | Yes |
| `zyber` | Zyber TinyChipHub | AxeOS HTTP | 80 | Yes |
| `hammer` | Hammer/Heatbit | AxeOS HTTP | 80 | Yes |
| `esp32miner` | ESP32 NerdMiner | Pool API* | N/A | Manual only |
| `goldshell` | Goldshell | HTTP | 80 | Yes |
| `antminer` | Bitmain Antminer (SHA-256) | CGMiner TCP | 4028 | Yes |
| `antminer_scrypt` | Bitmain Antminer (Scrypt) | CGMiner TCP | 4028 | Yes |
| `whatsminer` | MicroBT Whatsminer | CGMiner TCP | 4028 | Yes |
| `avalon` | Avalon Nano | CGMiner TCP | 4028 | Yes |
| `canaan` | Canaan AvalonMiner | CGMiner TCP | 4028 | Yes |
| `futurebit` | FutureBit Apollo | BFGMiner TCP | 4028 | Yes |
| `gekkoscience` | GekkoScience USB | CGMiner TCP | 4028 | Yes |
| `ebang` | Ebang/Ebit | CGMiner TCP | 4028 | Yes |
| `epic` | ePIC BlockMiner | HTTP REST | 4028 | Yes |
| `ipollo` | iPollo | CGMiner TCP* | 4028 | Best-effort |
| `elphapex` | Elphapex DG series | CGMiner TCP* | 4028 | Best-effort |
| `innosilicon` | Innosilicon | CGMiner TCP* | 4028 | Best-effort |
| `braiins` | BraiinsOS/BOS+ | REST API | 80 | Manual only |
| `vnish` | Vnish firmware | REST+CGMiner | 80+4028 | Manual only |
| `luxos` | LuxOS firmware | CGMiner TCP | 4028 | Manual only |

\* CGMiner may be disabled by default on these devices. See Best-Effort section for details.

\* Pool API: ESP32 miners have no device-level API. Sentinel polls the pool's stratum connections endpoint instead. See [Pool API](#pool-api-no-direct-device-api) section for requirements.
