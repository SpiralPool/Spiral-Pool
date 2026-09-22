# Miner Device Support Reference

> **⚠️ IMPORTANT — READ BEFORE CONNECTING YOUR MINER**
>
> Spiral Pool is a **single-operator solo mining pool**, and the operator owns every wallet its miners pay. **Every block reward goes to the operator's configured wallet.** Where the operator has enabled worker-name payout (`spiralctl mining payout worker`, off by default) to route their own rigs to their own addresses, a miner connecting to a coin's own port with a valid address for that coin as its worker name (`ADDRESS.worker`) is paid at that address instead. Otherwise, on the multi-coin smart port, and for every merge-mined auxiliary chain, rewards go to the operator's wallet and you receive no direct payment from the pool software.
>
> If you are connecting your miner to someone else's Spiral Pool, confirm with that operator which wallet your hashrate pays and what compensation arrangement, if any, they have in place. The pool software itself has no mechanism to split rewards.

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

## Remote Control and Automation

Sentinel runs the schedules set on the dashboard's Automation page (`/automation`) and restarts miners. What each firmware family supports:

| Family (type keys) | Restart | Sleep / wake | Power | Login |
|--------------------|---------|--------------|-------|-------|
| Bitaxe / AxeOS (`axeos`, `bitaxe`, `nmaxe`, `nerdaxe`, `qaxe`, `qaxeplus`, `hammer`, `luckyminer`, `jingleminer`, `zyber`) | HTTP `POST /api/system/restart` | `POST /api/system/pause` / `resume` (ESP-Miner 2.14.0 and later) | – | none |
| NerdQAxe (`nerdqaxe`, `nerdoctaxe`) | HTTP `POST /api/system/restart` | – (its shutdown cannot be woken remotely) | low / normal: the eco or default frequency and voltage from `GET /api/system/asic`, written with `PATCH /api/system`, then a restart. Refused when OTP is enabled on the device. `normal` replaces any custom frequency and voltage with the firmware defaults. `low` needs firmware that reports an eco preset: a NerdQAxe++ on v1.1.0 reports `ecoFrequency` 0 and refuses it. Confirmed on that unit: `normal` applied, `low` refused without changes | none |
| Avalon Nano 3S, Avalon Q, Avalon Mini 3 (`avalon`, `canaan`, with the model set on the Automation page) | cgminer `ascset 0,reboot,0`, confirmed on a Nano 3S | none: the firmware has no standby command (a Nano 3S on MM319 refuses `softoff`/`softon` and lists no alternative in `ascset 0,help`) | low / normal / high: `ascset 0,workmode,set,0-2`, confirmed on a Nano 3S | none |
| Other Avalons | `ascset 0,reboot,0` | – | – (power profiles stay on the dashboard's Avalon scheduler) | none |
| Antminer, stock firmware (`antminer`, `antminer_scrypt`) | `/cgi-bin/reboot.cgi` | `set_miner_conf.cgi` with `miner-mode` 1 / 0 | low (`miner-mode` 3) / normal (0) | HTTP digest, default root/root |
| Braiins OS (`braiins`) | REST `PUT /api/v1/actions/reboot` | `actions/pause` / `resume` | power target in watts: `PUT /api/v1/performance/power-target` | username/password |
| LuxOS (`luxos`) | `rebootdevice` in a logon session | `curtail sleep` / `wakeup` | power target in watts: `profileset <N>W` | session only |
| Vnish (`vnish`) | `POST /api/v1/system/reboot` | `mining/pause` / `resume` | power target in watts: selects the autotune preset of that name | password, default admin |
| Whatsminer M60/M66 (`whatsminer`), API v3 on TCP 4433 | `set.system.reboot` | `set.miner.service stop` / `start` | low / normal / high: `set.miner.power_mode` | account super, default password super; the API must be enabled in WhatsMinerTool |
| Elphapex (`elphapex`) | `/cgi-bin/reboot.cgi` | – | – | HTTP digest, default root/root |
| Goldshell (`goldshell`) | `/mcb/restart` (existing method) | – | – | none |

- **Mostly not yet run on hardware.** Every command above comes from vendor API documentation or several independent public descriptions, and is tested against mock devices built from them. Two families have since been commanded on real hardware: an Avalon Nano 3S accepted `power low`, `normal`, `high` and `restart`, and a NerdQAxe++ on firmware v1.1.0 accepted `power normal` and refused `power low`, which that firmware has no preset for. Every other family in this table is still untested against a real miner. Please report any that fail.
- **Testing one miner:** `sudo spiralctl miner control <IP> info` shows what the pool can do with that miner and whether a login is stored. `sleep`, `wake`, `restart` and `power low|normal|high|<watts>` send that command immediately, through the same code and stored login Sentinel uses, and print the miner's answer. `OK` means the miner accepted the command, not that it took effect, so confirm on the miner itself. `restart` covers Avalon, Antminer, Braiins OS, LuxOS, Vnish, Whatsminer and Elphapex; Bitaxe, NerdQAxe and Goldshell restarts stay in Sentinel. A miner missing from Sentinel's list needs `--type`, and an Avalon whose model is not set on the Automation page needs `--model nano3s`, `avalon_q` or `mini3`.
- **Before a stock Antminer's mode changes,** Sentinel reads the miner's configuration and writes it back with only the mode changed. It refuses if the configuration has no pools, so the write cannot erase them.
- **Credentials** are stored per miner on the Automation page. They are write-only, and are kept in `/spiralpool/data/device_credentials.json` with mode 0600. Without them, Sentinel uses the firmware's default login.
- **Not supported yet:**
  - Elphapex sleep and power modes, where public descriptions of the API conflict.
  - Goldshell sleep and power modes, which are unconfirmed.
  - Whatsminer API v2.
  - The Avalon Mini 3's heater and night modes.

---

## Alert Coverage (Spiral Sentinel)

Spiral Sentinel monitors all configured devices for these alert conditions:

| Alert Type | AxeOS | Goldshell | CGMiner (Antminer/Whatsminer/etc.) | BraiinsOS | Vnish | ePIC | LuxOS | ESP32 (Pool API) |
|------------|-------|-----------|-----------------------------------|-----------|-------|------|-------|------------------|
| Offline | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes |
| Temp Warning (>=75C) | Yes | Yes | Yes | Yes | Yes | Yes | Yes | N/A |
| Temp Critical (>=85C) | Yes | Yes | Yes | Yes | Yes | Yes | Yes | N/A |
| Thermal Shutdown (>=95C immediate; >=85C sustained 90s) | Yes* | N/A | Yes | Yes | Yes | Yes | Yes | N/A |
| Fan Failure (0 RPM) | N/A | N/A | Yes | Yes | Yes | Yes | Yes | N/A |
| Hashboard Dead | N/A | N/A | Yes | N/A | N/A | N/A | N/A | N/A |
| HW Error Rate | N/A | N/A | Yes | N/A | N/A | N/A | N/A | N/A |
| Stratum URL Mismatch | Yes | N/A | Yes | N/A | Yes | Yes | Yes | N/A |
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

---

## Known Monitoring Limitations

The following device families have reduced monitoring capabilities due to manufacturer firmware restrictions. These are hardware/firmware limitations, not Spiral Pool bugs.

### Devices Requiring Manual CGMiner Enablement

These miners ship with CGMiner API **disabled by default**. Auto-scan will not detect them until CGMiner is manually enabled. Mining is unaffected — only Sentinel monitoring requires CGMiner.

| Device | How to Enable CGMiner | Without CGMiner |
|--------|----------------------|-----------------|
| **iPollo** V1/V1 Mini/G1 | Web UI settings or SSH: add `--api-listen --api-network --api-allow W:0/0` to cgminer flags | Must be manually added; shows as offline |
| **Innosilicon** A10/A10 Pro/A11/T2T/T3 | Telnet to port 8100 (password: `innot1t2` or `t1t2t3a5`), add API flags, reboot | Must be manually added; shows as offline |
| **Elphapex** DG1/DG1+/DG Home | Unknown — CGMiner availability unconfirmed | Must be manually added; monitoring unreliable |

**Why these native APIs are not implemented:**
- **iPollo**: Uses a proprietary LuCI CGI web interface. Endpoints are undocumented and would require reverse-engineering. Firmware updates may change endpoints without notice.
- **Innosilicon**: Uses an HTTP REST API with JWT authentication ([dragon-rest](https://github.com/brndnmtthws/dragon-rest)). Integration would require maintaining session management for a proprietary auth flow that varies across firmware versions.
- **Elphapex**: Uses custom LuCI CGI endpoints (e.g. `/cgi-bin/luci/setworkmode.cgi`). Endpoints are completely undocumented and untested.

Implementing proprietary APIs is fragile — firmware updates from the manufacturer can break integrations without warning. CGMiner on port 4028 is the stable, universal protocol supported across all ASIC manufacturers.

### Devices With No Monitoring API

| Device | Reason | What Sentinel Can Track | What Sentinel Cannot Track |
|--------|--------|------------------------|---------------------------|
| **ESP32** NerdMiner / BitMaker / ESP32 Miner V2 | Hardware limitation — sealed embedded device with no HTTP or CGMiner API | Online/offline, hashrate, shares, difficulty (via pool API) | Temperature, fan speed, uptime, power consumption |

This is a hardware limitation of the ESP32 platform. The chip runs a bare Stratum client with no management interface. No software change can add device-level monitoring for ESP32 miners.

### Stratum URL Mismatch

Stratum URL mismatch detection alerts when a miner is pointed at an unexpected pool — a common indicator of firmware hijacking or misconfiguration. Supported for all device types that expose pool URL via their API: AxeOS (HTTP), CGMiner (TCP pools command), Vnish (CGMiner), ePIC (HTTP), and LuxOS (CGMiner). Not available for Goldshell (HTTP API doesn't expose pool URL), BraiinsOS (pool URL not in REST API stats), or ESP32 (no device API).
