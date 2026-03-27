# Changelog

All notable changes to Spiral Pool are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows `MAJOR.MINOR.PATCH` — patch releases are applied in-place on the same tag.

---

## [1.2.3] — 2026-03-27

### Fixed

**Installer — Firewall & Back Navigation**
- **Silent exit at "Configuring firewall..."** — `[[ -n "$STRATUM_PORT" ]] && sudo ufw allow ...` returns exit 1 when the variable is empty, which under `set -e` kills the entire installer. Replaced with `if/then` guards for both `STRATUM_PORT` and `STRATUM_V2_PORT`.
- **Back navigation ('b') kills installer** — `select_coin_mode`, `select_ha_mode`, `select_merge_mining_parent`, and `select_aux_chains` all use `return 1` to signal "go back". Under `set -e`, checking the return with `func; if [[ $? -eq 1 ]]` exits the script before the `$?` check runs. Rewrote all callers to use `if func; then` pattern.
- **`systemctl reset-failed` polkit auth failure** — `start_services()` called `systemctl reset-failed` without `sudo`, triggering polkit authentication prompts in non-interactive mode. Added `sudo` and added `reset-failed *` to the sudoers NOPASSWD allowlist.

**Installer — Dashboard Service**
- **Stale gunicorn control socket prevents dashboard start** — added `ExecStartPre=-/bin/rm -f gunicorn.ctl` to the spiraldash systemd service file and explicit `--worker-class gthread` to the `ExecStart` line.

**Upgrade — Dashboard Not Starting**
- **Dashboard hangs after upgrade** — stale `__pycache__` bytecode from the previous Python version and leftover `gunicorn.ctl` sockets from killed processes caused the dashboard to hang or fail on restart. `update_dashboard()` now cleans both before copying new files. Changed dashboard start from `--no-block` to blocking with a health check and automatic restart on failure.
- **Upgrade summary not waiting for services** — the summary screen now polls dashboard and sentinel for up to 120 seconds before reporting status, skipping stratum (which depends on blockchain node sync).

**Stratum Server**
- **Stratum hangs on shutdown (120s → SIGKILL)** — `connWg.Wait()` in `server.go:Stop()` had no timeout, hanging indefinitely when connection goroutines were stuck. Added a 10-second select timeout before proceeding with shutdown.
- **ESP32 miners showing 0 shares on dashboard** — `Session.IncrementShareCount()` existed but was never called in production code. Added the call in both `pool.go` (V1) and `coinpool.go` (V2) when a share is accepted. The dashboard's ESP32 panel reads this counter via the connections API.

**Spiral Sentinel — Block Alert**
- **Block alert shows wrong explorer page** — when a block is found seconds before the explorer indexes it, the "View Block" link opens a stale page. Added the block hash (first 16 chars) directly in the alert text so the user can verify without depending on the explorer.
- **Pool block counter wrong after Sentinel restart** — `pool_blocks_found` started from 0 on fresh state instead of initializing from the pool API's existing block count. Block #4 would show as "Pool Block #1" after a Sentinel restart.

**spiralctl**
- **`spiralctl coin enable` fails with "command not found"** — `prompt_input()` was defined in `install.sh` but never added to `spiralctl.sh`, causing all coin enable/onboard commands to fail immediately.

- All version strings, documentation, themes, and config files bumped to 1.2.3

---

## [1.2.2] — 2026-03-25

### Fixed

**Installer — Reinstall / Upgrade Guard Pattern (all 13 coins)**
- **Daemon not stopped before config regeneration on reinstall** — if a daemon was already running and the user reinstalled, the installer would regenerate config files underneath a live daemon, causing port conflicts, stale PID files, and LevelDB lock contention. All 13 coin install functions now stop the running daemon (`systemctl stop`), call `reset-failed` (clears systemd's `StartLimitBurst` crash counter), and remove stale PID files before reconfiguring.
- **Reinstall skipped config regeneration entirely** — all 13 install functions had an early `return` when the binary already existed (`if [[ -f .../bitcoind ]]; then return`). This meant reinstalling skipped config regeneration, systemd service creation, and all downstream setup. Changed to an `*_binary_exists` + `*_download_needed` guard pattern: binary download is skipped, but config regen, service file, and wallet setup always run.
- **RPC password recovery on reinstall** — if `coins.env` was corrupted or truncated during a reinstall, all `*_RPC_PASSWORD` variables would be empty. The installer would then generate new passwords that don't match the passwords already written in each daemon's conf file, causing RPC auth failures on every coin. Added a 13-coin password recovery loop that reads `rpcpassword=` from each daemon's existing conf file before falling back to generating a new password.
- **BCH-specific empty password guard** — added an additional safety net for BCH: if `BCH_RPC_PASSWORD` is still empty after `coins.env` parsing and the recovery loop, attempts to recover from the existing `bitcoin.conf` before generating a new password. BCH was the coin triggering the crash report.

**Installer — WSL2 Resource Scaling (DGB, BTC, BCH)**
- **Daemons OOM-killed on WSL2** — `dbcache=8192` (8 GB) was hardcoded for DGB, BTC, and BCH regardless of available RAM. WSL2 instances typically have limited memory via `.wslconfig`, and 8 GB dbcache would consume all available RAM, triggering OOM kills. All three coins now detect WSL2 (`/proc/version` check), cap dbcache to 25% of total RAM (floor 1024 MB, ceiling 4096 MB), and scale `MemoryMax`/`MemoryHigh` systemd limits proportionally.

**Installer — systemd Service Files (all 13 coins)**
- **DGB missing PIDFile directive** — DGB systemd service had `Type=forking` but no `PIDFile=` or `-pid=` argument. systemd couldn't reliably track the daemon process, leading to false "active (running)" status when the daemon had already exited. Added `PIDFile=` to service and `-pid=` to `ExecStart`.
- **BC2 missing PIDFile directive** — same fix as DGB. Bitcoin II systemd service now has `PIDFile=` and `-pid=` argument.
- **BTC missing PIDFile directive** — Bitcoin Knots systemd service now has `PIDFile=` and `-pid=` argument.
- **BCH missing PIDFile directive** — Bitcoin Cash systemd service now has `PIDFile=` and `-pid=` argument.
- **LimitNOFILE=65535 (off-by-one)** — 11 coin systemd services used `LimitNOFILE=65535` instead of the correct `65536` (2^16). While functionally harmless on most kernels, 65536 is the conventional power-of-two value. Standardized across all coins.

**Installer — BCH Config**
- **BCH missing `blockmaxsize` setting** — BCH config had `excessiveblocksize=32000000` (accept 32 MB blocks from the network) but was missing `blockmaxsize=32000000` (generate blocks up to 32 MB when mining). Without this, mined blocks would be capped at the Bitcoin Core default of 2 MB.

**Multi-Disk Storage (CHAIN_MOUNT_POINT)**
- **CHAIN_MOUNT_POINT grep pattern included literal quotes** — `coins.env` writes values as `CHAIN_MOUNT_POINT="/mnt/data"` (with quotes), but the `grep -oP '\K\S+'` pattern extracted `"/mnt/data"` including the quote characters. Every `-d` directory check silently failed, causing all multi-disk setups to fall back to `$INSTALL_DIR/<coin>/` regardless of configuration. Fixed across 12 instances in 5 files: `install.sh`, `spiralctl.sh`, `blockchain-export.sh`, `blockchain-restore.sh`, `wait-for-node.sh`.
- **spiralctl.sh `get_coin_cli()` ignored multi-disk paths** — all 13 coin CLI commands used hardcoded `$INSTALL_DIR/<coin>/` paths instead of checking `CHAIN_MOUNT_POINT`. Coin daemon CLI commands (getblockchaininfo, stop, etc.) would target the wrong config file on multi-disk setups. Added `_chain_dir()` helper and updated all 13 coin entries.
- **spiralctl.sh Tor status check hardcoded DGB path** — used `$INSTALL_DIR/dgb/digibyte.conf` instead of `$(_chain_dir dgb)/digibyte.conf`
- **blockchain-export.sh missing multi-disk support** — all 13 `COIN_DIRS` entries were hardcoded to `$INSTALL_DIR/<coin>/`. Added `_chain_dir()` helper with `CHAIN_MOUNT_POINT` lookup.
- **blockchain-restore.sh missing multi-disk support** — same fix as blockchain-export.sh
- **ha-replicate.sh missing multi-disk support** — all 13 `BLOCKCHAIN_DIRS` entries were hardcoded. Added `_chain_dir()` helper with `CHAIN_MOUNT_POINT` lookup.

**Daemon & Docker Config**
- **pool-mode.sh BC2 wallet commands hardcoded `/spiralpool/`** — 5 occurrences in the BC2 wallet creation block used `/spiralpool/bc2/bitcoinii.conf` instead of `$SPIRALPOOL_DIR/bc2/bitcoinii.conf`, failing on non-default install paths.
- **DigiByte Docker config missing `zmqpubrawblock`** — `digibyte.conf.template` had `zmqpubhashblock` and `zmqpubrawtx` but was missing `zmqpubrawblock`. All other 12 ZMQ-enabled coins had all three topics. Docker-mode DGB would miss raw block notifications.

**HA & Recovery**
- **ha-role-watcher.sh recovery health check matched error pages** — `grep -q "enabled"` on the HA status endpoint would match HTML error pages containing the word "enabled" anywhere, causing false-positive health checks. Replaced with `jq -e '.enabled == true'` for proper JSON validation.

**Regtest & Testing**
- **regtest.sh PepeCoin SIGABRT crash** — ZMQ arguments (`-zmqpubhashblock`, `-zmqpubrawblock`) were passed unconditionally to all coin daemons. PepeCoin v1.1.0 is compiled without ZMQ support and crashes with SIGABRT on startup when zmqpub* arguments are present. ZMQ args now conditionally skipped for PEP.
- **regtest-ha-full.sh missing 5 coins** — script advertised support for 13 coins but only implemented 8 in the case statement. Added NMC, SYS, XMY, FBTC, and QBX with correct port configurations.
- All version strings, documentation, themes, and config files bumped to 1.2.2

---

## [1.2.1] — 2026-03-24

### Added

- **DigiByte as merge mining parent chain** — install.sh now offers DGB as an explicit SHA-256d parent option (option 3) for merge mining with NMC, SYS, XMY, and FBTC auxiliary chains. Previously DGB was only an implicit fallback when BTC was disabled; now it is a first-class selection alongside BTC and LTC.
- **Back navigation in installer** — pressing `b` at any menu prompt returns to the previous step. Covers install mode, merge mining, coin selection, aux chain selection, and HA mode. No more Ctrl+C to fix a fat-finger.
- `spiralctl mining merge enable` also updated to recognize DGB as a valid SHA-256d parent
- Multi-coin mode merge mining prompt now detects DGB as SHA-256d parent when BTC is not present
- MOTD, Docker guide, spiralctl reference, and docker-compose.yml updated to list DGB as merge mining parent

### Fixed

- **LED celebration ignoring quiet hours** — the stratum Go code (`pool.go`, `coinpool.go`) launched `block-celebrate.sh` directly on block found, bypassing Sentinel's quiet hours check. The bash script now reads Sentinel's `quiet_hours_start`, `quiet_hours_end`, and `display_timezone` from config.json and enforces quiet hours at startup. Additionally, running celebrations now check periodically and stop early if quiet hours begin mid-celebration. `--force` flag added for manual override.
- **MOTD not updating on upgrade** — `update_motd()` in upgrade.sh used `cat >` to write to `/etc/update-motd.d/`, which silently fails without root. Now uses `sudo tee` matching install.sh.
- **Dashboard section ordering** — Lifetime Statistics section now renders below Statistics (charts) instead of above it
- **Flaky stress test** — `TestRapidFireHeightUpdates` widened stale RPC tolerance from 0 to 1; on slow CI runners a goroutine can slip through the cancellation window
- All version strings, documentation, themes, and config files bumped to 1.2.1

---

## [1.2.0] — 2026-03-23 - Convergent Spiral

> *One pool. Every coin. No limits.*

### Added

**Docker Multi-Coin Support**
- New `POOL_MODE=multi` for running multiple coins in a single Docker deployment
- `--profile multi` launches all enabled coin daemons and shared services
- Per-coin `ENABLE_<COIN>=true` flags and `<COIN>_POOL_ADDRESS` wallet addresses in `.env`
- V2 config generation in entrypoint: programmatic YAML output matching install.sh's multi-coin format
- All 14 supported coins available: DGB, BTC, BCH, BC2, NMC, SYS, XMY, FBTC, QBX, LTC, DOGE, DGB-SCRYPT, PEP, CAT

**Docker Merge Mining**
- Merge mining now supported in Docker multi-coin mode
- SHA-256d: BTC+NMC, BTC+FBTC, BTC+SYS, BTC+XMY (or DGB as parent if BTC disabled)
- Scrypt: LTC+DOGE, LTC+PEP
- Configured via `MERGE_MINING_ENABLED`, `MERGE_MINING_ALGO`, `MERGE_MINING_AUX_CHAINS_SHA256D`, `MERGE_MINING_AUX_CHAINS_SCRYPT`

**Docker Stratum V2 (Noise Protocol Encryption)**
- V2 Enhanced Stratum now available in Docker via `STRATUM_V2_ENABLED=true` in `.env`
- Uses `Noise_NX_secp256k1_ChaChaPoly_SHA256` — ephemeral keys generated in memory at startup
- No certificate files, no key management — zero-config encryption
- Works in both single-coin and multi-coin Docker modes
- Each coin gets a dedicated V2 port (V1 port + 1, e.g. DGB: 3334, BTC: 4334)
- Docker is now at full feature parity with native install for single-node deployments

**Dashboard Statistics Chart Grid**
- New 2×2 chart grid showing Pool Hashrate, Network Hashrate, Difficulty, and Workers & Miners — each with a current value and time-series chart
- Shared time-range dropdown selector: 15M, 1H, 6H, 12H, 24H, 7D, 30D
- Chart colors are fully theme-aware — each of the 23 built-in themes defines its own chart palette via `chart-pool-hashrate`, `chart-network-hashrate`, `chart-difficulty`, `chart-workers` color keys
- Chart colors customizable in the Custom Theme Editor (4 new color pickers: Pool HR, Net HR, Difficulty, Workers)
- Pool Hashrate stat card restored to the stats overview row (first position)

**Activity & Top Block Finders Section**
- Activity Feed and Top Block Finders now displayed side-by-side in a 2-column layout (stacks on mobile)
- Top Block Finders moved out of the Health section into its own dedicated panel
- Leaderboard now consolidates workers that map to the same device — e.g. `HashForge` and `HashForge.worker1` are merged into a single entry with combined block count and rewards

**V1.2 Convergent Spiral Codename Theme**
- New release codename theme with its own distinct palette — deeper charcoal backgrounds, brighter gold convergence points, stronger amethyst purple accents
- Each major release now has its own codename theme in the selector: V1.0 Black Ice, V1.1 Phi Forge, V1.2 Convergent Spiral

**Network Hashrate Tracking**
- Backend now records `network_difficulty` and `network_hashrate` to historical data for the statistics chart grid
- `/api/pool/history` response includes `network_difficulty` and `network_hashrate` arrays
- `/api/miners` response includes `network_hashrate` for live dashboard updates

### Fixed

**Network Hashrate Accuracy**
- All three network hashrate code paths (statistics charts, node health card, multi-node health) now prefer the node's `getnetworkhashps` RPC value over the theoretical formula (`difficulty × 2³² / block_time`)
- The RPC value uses a moving average over recent blocks and reflects actual network performance, rather than assuming blocks arrive exactly at the target rate
- Background polling loop now fetches and caches `getnetworkhashps` from the coin node each cycle

**Miner Dashboard String-to-Number Crash**
- `'>' not supported between instances of 'str' and 'int'` when adding stock Antminer to dashboard — CGMiner API returns numeric values as strings
- Added `_safe_num()` helper for safe string-to-number conversion across all 11 miner fetch functions: `fetch_antminer`, `fetch_braiins`, `fetch_vnish`, `fetch_luxos`, `fetch_epic_http`, `fetch_axeos`, `fetch_esp32miner`, `fetch_avalon`, `fetch_whatsminer`, `fetch_innosilicon`, `fetch_goldshell`
- Innosilicon firmware confirmed highest risk — returns string-encoded values for power, fan speed, temperature, and error codes

**Backup ACL Inheritance**
- New backup files created by cron were not inheriting read permissions for the pool user
- Added default ACL (`setfacl -R -d -m`) in `install.sh` so new files automatically inherit the correct permissions

**Sentinel Backup Status Display**
- Removed `du -sh` size check from backup report section — fails with "Permission denied" when pool user lacks recursive read on `/spiralpool/backups/`
- Now displays snapshot count only (`💾 Snapshots: 2`) instead of erroring with a `setfacl` hint

**Theme Mojibake**
- Fixed double-encoded UTF-8 em dashes in `black-ice.json` (name, description) and `bitcoin-laser.json` (description, customCSS) — displayed as garbled `â€"` characters

**Spiral Router — User-Agent Pattern Cleanup**
- Removed ~70% of miner detection patterns that were dead code — matched hardware model names (e.g. "Antminer S19", "Avalon Nano 3S") that manufacturers never include in stratum user-agent strings
- All remaining patterns verified against firmware source code (ESP-Miner, cgminer, bmminer, NerdMiner, etc.)
- `cgminer` and `bfgminer` reclassified from `MinerClassMid` to `MinerClassUnknown` — these generic mining clients span a 45,000× hashrate range (GekkoScience 2 TH/s to Avalon A16XP 300 TH/s); vardiff now handles classification, and Sentinel's DeviceHints provides model-specific difficulty for known devices
- Pattern count reduced from ~280 to 47 verified patterns; all 15 SHA-256d and 8 Scrypt difficulty profiles unchanged

**Scrypt Miner Test Accuracy**
- Removed SHA-256d-only miners from Scrypt test suite: `bmminer` (SHA-256d only per bitmaintech/bmminer-mix), `btminer` (MicroBT makes no Scrypt miners), `Braiins OS` (SHA-256d only, no L-series support), `sgminer` (GPU — not supported), NerdMiner/ESP32/BitAxe/NerdQAxe (BM-series SHA-256d ASICs)
- Antminer L-series (L3+, L7, L9) correctly identified as sending `cgminer/X.X.X` (per bitmaintech/cgminer-ltc), not `bmminer`
- Algorithm switch test updated to use `cgminer/4.10.1` (real Scrypt firmware UA) instead of `bmminer/2.0.0`

**Sentinel Network Hashrate**
- Sentinel `fetch_network_stats()` QBX section now calls `getnetworkhashps` RPC first, falling back to pool API and formula methods

**Wood Paneling Theme**
- Complete palette rework — replaced all-amber/gold colors with walnut browns, copper/burnt sienna accents, cream text, and forest green status indicators

**Avalon Restart Button**
- Avalon/Canaan devices showed a "Restart" button that always failed — Avalon firmware does not support the CGMiner `restart` command
- Miner card now shows "⚙ Configure" which opens the Avalon web UI in a new tab; detail modal hides the restart button entirely for Avalon devices
- Removed `avalon` from the CGMiner restart code path in the backend

**Block Celebration Stale Alert**
- Block celebration (confetti/audio) fired for blocks found hours ago after a page reload or service restart — `sessionStorage` block count was stale
- Celebrations now only fire for blocks found within the last 5 minutes; older blocks silently update the counter

**Pool Hashrate Farm Fallback**
- Pool Hashrate stat card was falling back to farm hashrate (self-reported by miner devices) when the stratum reported 0 — displayed wildly inaccurate numbers (e.g. 32 TH/s when actual pool hashrate was 0)
- Removed farm hashrate fallback; pool hashrate now shows stratum-reported value only

**Miners Connected Stat Card**
- "Miners Online" stat card showed a confusing `X / Y` mixing stratum-connected miners with fleet device count, making it look like devices were mining on the pool when they weren't
- Renamed to "Miners Connected" showing only stratum-connected count; fleet device count and average temperature shown as subtitle

**RPC Credential Loading**
- `coin_rpc()` silently returned `None` when RPC credentials were not loaded into `MULTI_COIN_NODES` — `load_multi_coin_config()` loads ports and enabled status but not credentials
- `coin_rpc()` now reads credentials directly from the coin's daemon conf file (e.g. `/spiralpool/qbx/qbitx.conf`) as a fallback when credentials are missing

**Network Hashrate History Recording**
- `record_historical_data()` was using the formula (`difficulty × 2³² / block_time`) instead of `_compute_network_hashrate()` which prefers the accurate RPC value — chart history oscillated wildly on coins with fast block times
- Now uses `_compute_network_hashrate()` for consistent RPC-backed values in both live display and chart history

**Codename Theme Switching**
- V1.2 Convergent Spiral theme was missing from the `themeColors` JavaScript object — selecting it cleared the previous theme's customCSS but applied no new colors until the API fetch completed, making the theme appear broken
- `phi-forge.json` was incorrectly overwritten with Convergent Spiral data — the V1.1 Phi Forge codename theme was lost
- Restored `phi-forge.json` as V1.1 Phi Forge; created `convergent-spiral.json` as V1.2 Convergent Spiral with its own distinct palette (deeper backgrounds, brighter gold convergence, stronger purple)
- Both codename themes now have instant-switch entries in `themeColors` alongside V1.0 Black Ice

**Version String Consistency**
- 21 stale `1.2` references (missing `.0` patch) found and fixed across 19 files — script variables, Docker labels, display banners, and documentation taglines now all read `1.2.0`
- Affected: `install.sh` (3), `docker/Dockerfile`, `scripts/spiralctl.sh`, `scripts/linux/blockchain-export.sh`, `scripts/linux/blockchain-restore.sh`, `scripts/linux/ha-replicate.sh`, `scripts/linux/ha-setup-ssh.sh`, `scripts/linux/update-checker.sh`, `install-windows.ps1`, `dashboard.py`, `dashboard.html`, `upgrade.sh`, `SpiralSentinel.py` (2), `UPGRADE_GUIDE.md` (4), `README.md` (2), and 9 documentation taglines

### Changed

- Dashboard statistics chart period selector changed from button group to dropdown, added 15M and 12H periods
- Added `--chart-pool-hashrate`, `--chart-network-hashrate`, `--chart-difficulty`, `--chart-workers` CSS variable defaults and theme-overridable color keys across all themes
- Responsive rules for statistics chart grid, period dropdown, and activity/leaderboard split layout
- Mobile CSS improvements: statistics chart grid, activity feed, and leaderboard panels now properly sized and readable on mobile and small phones
- All version strings bumped to semver `1.2.0` — variables, labels, banners, and documentation taglines across all scripts, Docker, dashboard, Sentinel, and docs
- MOTD command grid column padding widened (24→26 chars) to fix `spiralctl chain export/restore` alignment
- All coin daemon containers now include `"multi"` profile in docker-compose.yml
- Updated docker-compose.yml header to document both single-coin and multi-coin usage
- Removed "Docker limitations" block from docker-compose.yml — multi-coin and merge mining are no longer unsupported
- `POOL_COIN`, `POOL_ID`, `POOL_ADDRESS` no longer required in Docker — defaults to empty for multi-coin mode
- `.env.example` expanded with full multi-coin configuration section (per-coin enable flags, wallet addresses, merge mining settings)
- Dockerfile description updated from "Single-Coin Mode" to "Single + Multi-Coin Mode"
- `config.docker.template` comments clarified as single-coin only; multi-coin mode generates config programmatically
- Coin daemon config templates (Fractal, Myriadcoin, Namecoin) updated to reference Docker multi-coin mode availability
- `stratum-entrypoint.sh` now branches on `POOL_MODE` with mode-aware validation (single requires `POOL_COIN`/`POOL_ADDRESS`; multi validates at least one coin enabled)

---

## [1.1.2] — 2026-03-22 — Phi Forge

> *When the miner speaks, the pool listens.*

### Fixed

**Unknown Miner Difficulty Override**
- ASICs sending empty or unrecognized user-agents (e.g. some Antminer S19 stock firmware) were forced into the "unknown" miner profile with `MinDiff=500 / MaxDiff=50000` — far too restrictive for ASIC hardware, preventing vardiff from reaching proper operating difficulty
- Unknown SHA-256d profile widened to `MinDiff=100 / MaxDiff=1000000` — vardiff now ramps up naturally to optimal difficulty for any miner class
- When Spiral Router cannot identify a miner, the pool now falls back to the operator's YAML/env config values instead of overriding with hardcoded defaults

**Connection Classifier — False PROXY on LAN**
- ASICs on local networks authorize in <5ms, which the timing heuristic misclassified as "automated software (proxy)" at 0.40 confidence
- Timing score reduced from 0.40 to 0.25 for <5ms auth delay; timing analysis now skipped entirely when Level 1 already identified the miner via user-agent

**Docker — AsicBoost / Version Rolling**
- `versionRolling` section was completely missing from the Docker config template — Vnish firmware reported pool offline because AsicBoost was not advertised
- Now enabled by default: `enabled: true`, `mask: 536862720` (standard BIP320)
- Configurable via `STRATUM_VERSION_ROLLING` and `STRATUM_VERSION_ROLLING_MASK` in `.env`

**Docker — Difficulty Environment Variables**
- `STRATUM_DIFF_INITIAL`, `STRATUM_DIFF_MIN`, `STRATUM_DIFF_MAX`, `STRATUM_VARDIFF_TARGET_TIME` were defined in `.env.example` but the config template used hardcoded values — operator overrides were silently ignored
- Template now uses `${STRATUM_DIFF_*}` substitution; defaults set in `stratum-entrypoint.sh`

### Changed

- All version strings bumped from 1.1.1 to 1.1.2

### Acknowledgements

- Thanks to **Kamakhu** for reporting the S19/S19K Pro classification bug and providing detailed logs and Docker config that helped diagnose both the difficulty and AsicBoost issues

---

## [1.1.1] — 2026-03-21 — Phi Forge

> *Built on what came before. Growing toward phi.*

### Added

**Custom Theme Editor**
- New in-dashboard theme editor panel in the Appearance sidebar — create custom themes without editing JSON files
- 13 color pickers: background, cards, 8 accent colors (blue, cyan, purple, pink, orange, yellow, green, red), text primary/secondary, border color
- Border radius selector (Sharp 0px → Extra 16px)
- Live preview — all color changes apply instantly as you pick
- Save to browser localStorage — custom themes persist across sessions
- Export as `.json` — download your custom theme in the standard Spiral Pool theme format
- Import `.json` — load any exported theme (or any Spiral Pool theme JSON) directly into the editor
- Custom themes appear in a "Custom" optgroup in the theme dropdown
- Validates imported themes: requires `colors` object with minimum keys (`bg-primary`, `bg-card`, `neon-blue`, `text-primary`)
- Handles localStorage quota errors gracefully ("Storage full — export instead")
- Editor pickers auto-refresh when switching themes via the dropdown

**Top Block Finders Leaderboard (Dashboard)**
- New leaderboard widget inside System Health section — ranks miners by blocks found with medal icons (gold/silver/bronze)
- Per-coin reward breakdown (e.g. "125.00 BTC + 500.00 NMC") instead of a single total
- Multi-coin support: queries all pools for solo, multi-coin, and merge-mining setups with single-pool fallback
- Blocks with no source attribution are filtered out
- Retroactive — pulls all historical blocks from PostgreSQL via the pool API

**Profitability Tracker Module (Sentinel)**
- New `compute_coin_profitability()` and `compute_profitability_rankings()` functions in Spiral Sentinel
- Calculates daily fiat revenue per coin: `(block_reward × blocks_per_day × hashrate) / network_hashrate × coin_price`
- Groups coins by algorithm family (SHA-256d, Scrypt) for profitability ranking
- Module is present in code but **not active** — staging for v1.2.0 profit-switching

### Changed

**Theme Quality Overhaul**
- **Phi Forge**: Redesigned — all-gold monochromatic palette replaced with gold + amethyst purple accents on dark charcoal background; added visual hierarchy with contrasting secondary color
- **Bitcoin Laser**: Background changed to true black (#050505); secondary accent changed from grey to laser red (#cc2200); stripped to minimal effects for maximalist aesthetic
- **Vaporwave**: Background changed from deep purple (duplicate of Rainbow Unicorn) to dark teal (#0a1018) with sunset horizon glow; primary accent shifted to cyan; completely distinct visual identity
- **Solar Flare**: Background changed from warm brown (duplicate of Autumn Harvest) to near-black (#080808); hotter plasma yellows (#ffee00) for a coronal ejection feel
- **Midnight Aurora**: Background changed from deep purple to neutral dark; primary accent changed from cyan to aurora green (#40d8a0); now green/purple curtain effect, distinct from Ocean Depths' blue/cyan
- **Wood Paneling**: Fonts changed from Playfair Display + Lato (identical to Autumn Harvest) to Libre Baskerville + Source Sans 3
- **Nebula Command**: Display font changed from Orbitron (shared with Cyberpunk) to Titillium Web

**Sentinel — Backup Reporting**
- Backup size display now shows actual size instead of `?` when permissions are correct
- Shows "no access" instead of `?` when `Permission denied` is detected — diagnosable instead of opaque
- Backup snapshot count added to report: `💾 Size: 3.1M (2 snapshots)`
- Recursive ACL (`setfacl -R`) applied during install so spiralpool user can read backup subdirectories — no manual setup needed
- `acl` package added to installer prerequisites

**Dashboard — ETB Display**
- Estimated Time to Block now shows minutes when under 1 hour (e.g. "12 minutes" instead of "0.2 hours")

**External Access — Rented Hashrate**
- `sharesPerSecond` now configurable in `spiralctl external setup` wizard with tiered options:
  - Small (<10 TH/s): 200/sec, Medium (10–100 TH/s): 500/sec, Large (100TH–50PH): 1000/sec, XL (50+ PH/s): 2000/sec, Custom: 10–100000
- Default `sharesPerSecond` changed from 50 to 500 (Medium tier)
- Cloudflare Tunnel setup now warns that Spectrum (paid add-on) is required for raw TCP proxying
- Documentation updated with Spectrum prerequisite and shares-per-second configuration table

**Go Toolchain**
- Go version updated from 1.25.6 to 1.26.1 across all build paths (go.mod, install.sh, upgrade.sh, Dockerfile, test.sh)
- Minimum build requirement is now Go 1.26.1 (enforced by go.mod) — `install.sh` and `upgrade.sh` download Go 1.26.1 automatically from go.dev; existing installs with older Go will be upgraded on next `upgrade.sh` run

### Security

- **Theme CSS injection hardening**: `customCSS` field in theme JSON files is now sanitized before injection — `url()`, `@import`, `expression()`, `javascript:`, `-moz-binding`, `behavior:`, and Unicode escape obfuscation are all blocked and replaced with `/* blocked */`
- **CSS variable value sanitization**: all CSS custom property values from theme JSON are validated — values containing `url()`, `expression()`, or `javascript:` are rejected before `setProperty` to prevent data exfiltration via computed styles
- **Imported theme confirmation prompt**: importing a `.json` theme that contains `customCSS` now shows a confirmation dialog with a preview of the CSS — operator can cancel to apply colors only without the custom CSS

### Fixed

- Backup script permissions: added `chown -R root:spiralpool` step so Sentinel can read backup sizes
- 7 themes fixed for visual similarity — eliminated duplicate-looking pairs across all 23 themes
- Dashboard "Miners Online" display could show numerator exceeding denominator (e.g. 8/7) during stratum reconnection spikes — clamped to `min(realtime, configured)` so the count never exceeds the fleet total; also fixed unclamped workers count in hashrate subtitle

**`upgrade.sh` — Service Status Display**
- Post-upgrade service status check ran immediately after `systemctl start --no-block`, showing services as `inactive` / `deactivating` — added 10-second wait before verification and 5-second wait before summary display
- Summary now shows contextual note when services aren't yet active: "Services may take up to 30 seconds to fully start" with a re-check command

**`upgrade.sh` — API Key Migration**
- Admin API key grep patterns required double-quoted values (`"\K[^"]+`); unquoted YAML values (valid syntax) silently failed, causing the upgrade to generate a new API key instead of preserving the existing one
- Fixed all 6 grep patterns (Fix 6, Fix 7, Fix 8) to accept both quoted and unquoted values (`"?\K[^"\s]+`)

**`upgrade.sh` — Go Download Hang**
- Go 1.26.1 download used `curl -fsSL` (silent mode) — a ~150MB download with no progress output appeared to hang indefinitely
- Fixed: removed `-s` flag, added `--connect-timeout 15` and `--max-time 300`, added "Downloading Go 1.26.1" log message; also fixed in `test.sh`

**Notification Formatting — Discord / Telegram**
- All maintenance-mode, HA, and update-checker notifications used literal `\n` in double-quoted bash strings — Discord and Telegram displayed `\n` as text instead of newlines
- Fixed: all notification messages now use `printf -v` to produce real newline characters
- Node identifier in notification footers changed from truncated UUID (`Node: 8990382...`) to hostname (e.g. `spiralpool-qbx-109`) — consistent with Sentinel's existing approach

**Dashboard — Coin Daemon Version Display**
- Dashboard showed incorrect version for daemons with broken `subversion` strings (e.g. Q-BitX reports `0.1.0` regardless of installed version)
- Fixed: dashboard now reads from version cache (`/spiralpool/config/coin-versions/<COIN>.ver`) when available, which reflects the actual installed binary version

**Documentation — Lottery Miner Support**
- README now lists NerdMiner, NM Miner, and other ESP32-based lottery miners as supported hardware
- Explicitly noted support for any Stratum V1-compatible device regardless of hash power

**Documentation — `git clone` Instructions**
- All user-facing `git clone` instructions now use `--depth 1` to skip git history (~29MB), reducing download size to source files only (~16MB)

---

## [1.1.0] — 2026-03-19 — Phi Forge

> *Convergent difficulty. Minimal oscillation.*

### Added

**Q-BitX (QBX) — New Coin Support**
- Full native support for Q-BitX (QBX): SHA-256d, 2.5-minute (150s) block time, 12.5 QBX initial block reward, halving every 840,000 blocks
- QBX added to Spiral Sentinel monitoring — all lookup tables, hashrate crash thresholds, swap alert eligibility, and payout monitoring
- QBX wallet address validation: P2PKH (`M`-prefix, version `0x32`), P2SH (`P`-prefix, version `0x37`), post-quantum Dilithium (`pq`-prefix)
- QBX difficulty profiles added to Spiral Router (SHA-256d device classification)
- QBX standalone — no merge mining (no AuxPoW)

**Installer — Native Existing-Install Detection**
- `detect_existing_native_install()` — new function mirrors the existing Docker detection path; reads `/spiralpool/config/coins.env` on re-run, detects which coins are already enabled, and presents a clear menu:
  - `[1] Add coins to existing installation` — loads all existing RPC passwords, pool addresses, and wallet addresses; skips prompts for already-configured coins; preserves DB password and admin API key
  - `[2] Fresh installation` — clean run, no state carried forward
- `coins.env` now persists per-coin RPC passwords and pool addresses for all 13 coins so they can be recovered on re-run without user re-entry
- Multi-coin address collection blocks now guard against overwriting existing wallet addresses — if an address is already present from a previous install, it is preserved silently and the prompt is skipped

**`spiralctl coin enable` — Add Supported Coins**
- New `spiralctl coin enable <TICKER>` command to add any of the 14 natively supported coins
- Launches the installer in "Add coins to existing installation" mode — handles daemon install, wallet generation, config.yaml, firewall ports, and service restart automatically
- After enabling, the Dashboard at `/setup` auto-detects the new coins and shows wallet inputs
- `spiralctl coin disable <TICKER>` stops and disables a coin daemon (wallet and blockchain data preserved)
- `spiralctl add-coin` is now explicitly for **custom/unsupported coins only** (advanced)
- `spiralctl add-coin <TICKER>` still guards against built-in tickers and redirects to `coin enable`

**`add-coin.py` — Scope Clarification**
- Module docstring and usage examples updated to explicitly state this tool is for **NET NEW coins only** — coins not natively supported by Spiral Pool
- Built-in coin list displayed prominently in help output
- Examples updated to use placeholder tickers instead of natively-supported coins

**`spiralctl coin-upgrade` — Coin Daemon Upgrade Utility**
- New `coin-upgrade.sh` script and `spiralctl coin-upgrade` subcommand for in-place coin daemon binary upgrades
- Upgrades the binary only — config files, wallets, blockchain data, and pool settings are never modified
- Risk classification per upgrade: `PATCH` (binary swap, reindex not expected), `MINOR` (reindex may be needed), `MAJOR` (reindex almost certainly required)
- `--check` flag shows current vs target version status with no changes made
- `--coin <TICKER>` targets a specific coin; `--reindex` starts the daemon with `-reindex` after upgrade
- Operator-initiated only — never triggered automatically by `upgrade.sh` or Sentinel

**ntfy Push Notifications**
- New notification channel: [ntfy](https://ntfy.sh) — free, no-account mobile/desktop push notifications
- Configure with `ntfy_url` (full topic URL) and optional `ntfy_token` for private/self-hosted topics
- Wired into `send_notifications()` alongside Discord, Telegram, and XMPP — participates in retry logic and fallback logging
- Block found embeds include an ntfy Action button ("View Block") linking to the block explorer when available
- install.sh notification setup now includes an ntfy configuration step

**Block Explorer Links**
- Block found Discord notifications now include a **View Block** field with a link to the canonical block explorer for each coin
- Discord embed title is also a hyperlink (clickable in Discord client)
- Explorer URL is passed as an ntfy Action button for one-tap mobile access
- Per-coin explorer map: BTC → mempool.space, BCH/LTC/DOGE/SYS → blockchair.com, DGB → digiexplorer.info, NMC → bchain.info, FBTC → fractalbitcoin explorer; coins without public explorers (BC2, XMY, PEP, CAT, QBX) show no link

**Installer — Consolidated Sentinel Configuration Menu**
- All Sentinel configuration (alerts, health monitoring, reports, update mode) is now presented as a single interactive toggle menu instead of 3–4 sequential question screens
- 11 items in one view: master alerts switch, 7 individual alert types (dry streak, difficulty change, disk space, BTC mempool, backup staleness, sats surge, wallet drop), health monitoring, report frequency, and update mode
- When master alerts is toggled OFF, items 2–8 are greyed out with a note that they are muted — no false impression of individual control while the master switch suppresses everything
- Report frequency cycles through three states: `4x Daily` → `1x Daily` → `Off`
- Update mode cycles through: `Notify Only` → `Auto-Update` → `Disabled`
- Per-alert preferences are written directly into `config.json` at install time; Sentinel respects them immediately with no manual config editing required
- New config keys written at install time: `sats_surge_enabled` (default `true`) and `wallet_drop_alert_enabled` (default `true`) — previously these alert types were always on with no per-install control

**Installer — Notification Setup UX**
- Each notification channel (Discord, Telegram, XMPP, ntfy, SMTP) now gets its own dedicated full-screen section with a clear header — terminal is cleared between each channel so output from the previous section does not crowd the next
- Fleet configuration (expected hashrate prompt) also gets its own cleared screen
- Alert theme description updated to accurately name all five supported notification channels instead of only "Discord/Telegram"

**Cloud Deployments — Hardening**
- **Individual risk acknowledgment gates**: cloud installs now require typing `YES` to each of the five risks separately (ToS violation, account termination / data loss, provider access to credentials and disk, bandwidth billing, IPv6 disabled at kernel level) — a single combined gate was replaced with per-risk prompts
- **Legal terms YES gate on cloud**: cloud operators must type `YES` (non-cloud: `I AGREE`) — consistent with the per-risk prompts; `--accept-terms` CLI flag removed (all risk acknowledgment is now manual and interactive)
- **Risk 5 — IPv6 disabled**: explicit acknowledgment added; IPv6 is disabled at the kernel level (`/etc/sysctl.conf`) because it causes kernel routing cache corruption during keepalived VIP failover operations
- **HA forced to standalone on cloud**: selecting HA Primary or HA Backup on a cloud provider now auto-reverts to Standalone with an explanation; cloud provider networks block VRRP (keepalived) multicast/broadcast required for VIP failover
- **Tor disabled on cloud**: Tor is automatically disabled on cloud installs (most provider AUPs prohibit Tor; it also doesn't protect against provider hypervisor access — the primary cloud threat)
- **ZMQ bindings hardened**: all `zmqpubhashblock`, `zmqpubrawtx`, and `zmqpubrawblock` daemon config entries changed from `tcp://0.0.0.0:PORT` to `tcp://127.0.0.1:PORT` — ZMQ is a local IPC channel between the daemon and stratum; it never needs to be reachable from outside the server
- **Prometheus metrics loopback-only on cloud**: port 9100 is restricted to `127.0.0.1/::1` on cloud (UFW); the cloud provider's "local subnet" is a shared tenant network, not a trusted private network
- **Wallet security warning**: cloud installs show a red warning before wallet address collection explaining that `wallet.dat` written by "Generate one for me" (option 2) stores unencrypted private keys on provider-managed disk — operators are directed to use a hardware wallet address (option 1)
- **Credentials security notice**: post-install completion shows a red notice instructing operators to copy the admin API key offline, delete `credentials.txt`, and clear terminal history; swap-to-disk risk and auto-reboot behavior also documented here
- **Swap security**: 4 GB swapfile creation now logs a cloud-specific warning that in-memory credential data can be written to swap on provider-managed disk; documented in `CLOUD_OPERATIONS.md`
- **Auto-reboot notice**: `unattended-upgrades` auto-reboot at 04:00 UTC is logged as a cloud-specific warning with instructions to disable if desired; documented in `CLOUD_OPERATIONS.md`
- **SSH tunnel for dashboard**: cloud completion output replaces the direct dashboard URL with SSH tunnel instructions (`ssh -L 1618:localhost:1618 user@server`); port 1618 is intentionally closed in UFW on cloud
- **API port annotation**: cloud completion output annotates the pool API URL as world-accessible (intentional — public pool stats) with a note that admin routes require the API key
- **CLOUD_OPERATIONS.md expanded**: new sections added for IPv6, HA not supported, wallet security, ZMQ/RPC port security, credentials security, swap security, automatic reboots, and PostgreSQL data durability; post-install checklist updated with all new items
- **`--simulate-cloud <provider>` flag**: test flag added to simulate cloud install paths on local VMs without a real cloud provider

**Documentation**
- `docs/setup/UPGRADE_GUIDE.md` — new upgrade guide covering all coin types, merge mining compatibility, database migration analysis (zero new migrations in v1.1.0), and all `upgrade.sh` flags

**Sentinel — New Monitoring Alerts**
- **Dry streak alert**: fires when no block has been found in `dry_streak_multiplier × ETB` (default 3×). Configurable via `dry_streak_enabled` / `dry_streak_multiplier`. Cooldown 6h.
- **Network difficulty change alert**: fires when difficulty drifts ≥ `difficulty_alert_threshold_pct` (default 25%) from the baseline at last alert. Comparison is against the previous alert baseline, not tick-to-tick — prevents constant noise on per-block difficulty coins (DGB, DOGE). Configurable via `difficulty_alert_enabled` / `difficulty_alert_threshold_pct`. Cooldown 1h.
- **Disk space monitoring**: checks `/`, `/spiralpool`, `/var` (configurable via `disk_monitor_paths`). Enabled via `disk_monitor_enabled` (default true). Warning at `disk_warn_pct` (default 85%), critical at `disk_critical_pct` (default 95%). Per-path cooldowns: 1h warning, 5min critical.
- **BTC mempool congestion alert**: fires when Bitcoin mempool exceeds `mempool_alert_threshold` transactions (default 50,000). Configurable via `mempool_alert_enabled` / `mempool_alert_threshold`. Cooldown 1h.
- **Stratum-down alert**: fires via `send_notifications()` (bypasses quiet hours) when the pool API has been unreachable for 5+ minutes. Clears automatically with a recovery notification when the pool comes back online.
- **Backup staleness alert**: fires when the newest backup in `/spiralpool/backups/` is older than `backup_stale_days` (default 2 days). Only active when `/etc/cron.d/spiralpool-backup` exists (i.e., user opted in during install). Cooldown 24h.
- **Config validation → Discord**: at startup, if `validate_config()` finds any issues (placeholder wallets, invalid URLs, etc.), a yellow warning embed is sent immediately after the startup summary. Fires once per Sentinel restart.

**Sentinel — Intel Report Enhancements**
- **Per-coin ETB** (Expected Time to Block): shown in the NETWORK section of 6h/daily reports below the difficulty line. Displays as days, hours, or minutes depending on magnitude.
- **Per-miner health score**: each miner line in the RIGS section now includes a colour-coded health score (💚 ≥90, 💛 ≥75, 🔴 <75).
- **Backup status field**: when the backup cron is installed, intel reports include a `💿 BACKUPS` field showing last backup timestamp, age, total size, and the cron schedule.

**Sentinel — Scheduled Maintenance Windows**
- New config key `scheduled_maintenance_windows`: a list of time windows during which non-critical alerts are suppressed
- Each window supports `start`/`end` times, optional `days` list (0=Monday), and overnight ranges
- Scheduled reports and `block_found` always go through regardless of maintenance windows

**Sentinel — HA Blip Suppression**
- Role change alerts (`ha_demoted` / `ha_promoted`) are now suppressed for brief keepalived VRRP election blips
- Changed from cycle-based debounce (one 30s poll) to **timestamp-based debounce**: a role change must hold for `ha_role_change_confirm_secs` (default 90s) before an alert fires
- If the node reverts to its original role within the window (at any point), the blip is silently suppressed with a log entry
- Configurable via `ha_role_change_confirm_secs` in `config.json`

**spiralctl — Status Command Improvements**
- **Service uptime**: each service line in the SERVICES section now shows how long the service has been running (e.g. `up 3d 2h 15m`)
- **Miner connection ports**: MINER CONNECTION section moved to immediately after SERVICES (was at the bottom), so port addresses are visible without scrolling
- **Scheduled Tasks section**: new section at the bottom of `spiralctl status` showing the backup cron schedule and next PG maintenance timer run
- **Pool version**: version line shown at the top of the SERVICES section (read from `$INSTALL_DIR/VERSION`)
- **Sentinel version**: when Sentinel is running, its version string is queried from the health endpoint and appended to the Sentinel uptime line (e.g. `up 2h · v1.1.0-PHI_FORGE`)
- **Alert pause status**: if Sentinel alerts are paused, an ALERT STATUS section appears showing time remaining and reason with a tip to run `spiralpool-pause resume`

**spiralctl — Version Command Improvements**
- `spiralctl version` now shows a full version table: spiralctl, stratum binary (from `spiralstratum --version`), Sentinel, and all installed coin daemon versions

**Installer — PostgreSQL Auto-Maintenance Timer**
- `setup_pg_maintenance()`: installs a weekly systemd timer (`spiralpool-pg-maintenance.timer`, Sunday 03:00) that runs `VACUUM ANALYZE` on all pool tables
- Safely skips on Patroni replicas (`pg_is_in_recovery()` check prevents conflicts with streaming replication)
- Timer is `Persistent=true` — runs missed schedule after downtime on next boot
- Deployed by both `install.sh` and `upgrade.sh`

**Installer / Backup — Backup Integrity Verification**
- Daily backup script now verifies each `.sql.gz` dump with `gzip -t` after creation
- Generates `sha256sum` checksums for all backup files
- Sends a Discord notification (via webhook from Sentinel config) on backup completion or failure

**Documentation — Single-Operator Architecture Notice**
- New warning added to `install.sh` legal acceptance screen (red box before `I AGREE` prompt)
- New section "Single-Operator Architecture — Wallet Control" added to `WARNINGS.md`
- New `TERMS.md` Section 5E: Single-Operator Architecture — explicit legal acknowledgment
- `README.md`: operator notice added to the What Is Spiral Pool? section
- `docs/reference/MINER_SUPPORT.md`: prominent notice at top for miners connecting to operator-run pools

**Email / SMTP Notifications**
- New notification channel: SMTP email — send alerts to any email address via any SMTP server (Gmail, Outlook, self-hosted)
- Configure via `smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_to` in `config.json`
- STARTTLS (port 587, recommended) and SSL/TLS (port 465) both supported via `smtp_use_tls`
- Multiple recipients supported via comma-separated `smtp_to`
- Credentials stored in `config.json` (chmod 600, spiraluser only) — same hardening as Discord webhook and Telegram bot token
- Wired into `send_notifications()` alongside Discord, Telegram, XMPP, and ntfy — full retry and fallback logging
- install.sh notification setup now includes an SMTP configuration step

**Telegram Bot Commands**
- Sentinel now responds to commands sent to the configured Telegram bot:
  - `/status` — pool overview (coins, connected miners, hashrate)
  - `/miners` — per-miner address, hashrate, and shares/sec
  - `/hashrate` — pool hashrate and network difficulty per coin
  - `/blocks` — last 5 blocks found per coin
  - `/help` — command list
- Runs as a background daemon thread (long-poll `getUpdates`); only responds to the configured `telegram_chat_id` — all other senders silently ignored
- Configurable via `telegram_commands_enabled` (default `true` when Telegram is enabled)
- install.sh prompts to enable/disable bot commands when Telegram is configured

**`spiralctl miners` — Live Miner Table**
- New `spiralctl miners` command shows all connected miners with address, hashrate, shares/sec, and total shares — formatted table, per-coin grouping
- `spiralctl miners kick <IP>` disconnects all stratum sessions from the given IP; miner reconnects automatically on its own reconnect timer
- Kick uses `POST /api/admin/kick` (admin API key required from `config.yaml`)

**`spiralctl miner nick` — Miner Nickname Management**
- `spiralctl miner nick <IP> <name>` — set a display name for a miner in Sentinel
- `spiralctl miner nick list` — list all configured nicknames
- `spiralctl miner nick clear <IP>` — remove a nickname
- Edits `config.json` directly via Python; prints restart reminder

**`spiralctl config validate` — Dry-Run Config Check**
- `spiralctl config validate` checks both `config.yaml` (stratum) and `config.json` (Sentinel) for issues without restarting any services
- Checks: YAML/JSON syntax, placeholder wallet addresses, invalid notification URLs, SMTP completeness, `check_interval` sanity
- Also accessible as `spiralctl config validate` (added as a subcommand of `config`)

**`POST /api/admin/kick` — Stratum Kick Endpoint**
- New admin API endpoint: `POST /api/admin/kick?ip=X.X.X.X` (requires `X-API-Key` header)
- Closes all stratum sessions matching the given IP; returns `{"ip": "...", "kicked": N}`
- Used by `spiralctl miners kick`; also callable directly from scripts or monitoring tools

**Sentinel — Zombie Miner Kick-First Remediation**
- Zombie miner handling now uses a two-stage escalation: **kick stratum session first**, only escalate to a full miner reboot if the zombie condition persists 15 minutes after the kick
- Kick forces an immediate stratum reconnect (~5 seconds) without a 2-minute power cycle — resolves most zombie cases caused by stale connections
- If the kick resolves the issue, no reboot is triggered; if the zombie persists, Sentinel escalates and reboots as before
- Share rejection spikes now also trigger a stratum kick on first detection (forces reconnect + difficulty re-negotiation without a reboot)

**`spiralctl config notify-test` — Notification Channel Test**
- New subcommand: `spiralctl config notify-test` sends a test message to every configured notification channel and reports pass/fail per channel
- Covers Discord, Telegram, ntfy, SMTP email, and XMPP — shows `— not configured` for channels not set up
- Eliminates the need to wait for a real alert to verify notification delivery

**`spiralctl config validate` — Expanded Checks**
- Admin API key cross-check: warns if `pool_admin_api_key` in sentinel config does not match `admin_api_key` in `config.yaml` — a silent mismatch caused all stratum kick calls to fail with 401
- Telegram completeness: warns if `telegram_bot_token` is set without `telegram_chat_id` or vice versa
- XMPP completeness: warns if any of `xmpp_jid` / `xmpp_password` / `xmpp_recipient` are set without the others
- `pool_api_url` format check: warns if the value is not a valid HTTP/HTTPS URL

**`spiralctl log errors` — Per-Service Filter**
- `spiralctl log errors [service] [window]` now accepts an optional service name to scope output to a single service
- Aliases: `stratum`, `sentinel`, `dash` / `dashboard`, `patroni` / `postgres` / `pg`, `ha` / `watcher`
- Examples: `spiralctl log errors sentinel`, `spiralctl log errors stratum 24h`

**Telegram Bot — `/uptime` Command**
- New bot command `/uptime` reports Sentinel process uptime and stratum service uptime (via `systemctl show`)
- Added to `/help` listing

**`upgrade.sh` — Post-Upgrade Config Validate**
- `spiralctl config validate` now runs automatically at the end of every upgrade, after the summary, to surface any key mismatches or placeholder values introduced by config migration

**Telegram Bot — `/pause` and `/resume` Commands**
- `/pause [minutes]` — pause non-critical Sentinel alerts for N minutes (default 30, max 1440). Writes the same pause file as `spiralctl pause` and `spiralctl maintenance on`. Shows time remaining in confirmation.
- `/resume` — cancel an active pause immediately and restore alerts. Reports if already unpaused.
- Both commands added to the `/help` listing

**`spiralctl config validate` — v1.1.0 Alert Config Range Checks**
- Added sanity checks for all new v1.1.0 alert configuration keys:
  - `disk_warn_pct` must be less than `disk_critical_pct`
  - `dry_streak_multiplier` must be ≥ 1
  - `difficulty_alert_threshold_pct` must be between 1 and 100
  - `backup_stale_days` must be ≥ 1
  - `mempool_alert_threshold` must be ≥ 100

**Installer — Coin Daemon Configuration Hardening**
- `dbcache` minimum raised to 4,096 MB for all coins (8,192 MB for BTC, BCH, and DGB) — a ceiling applied during IBD to reduce disk I/O; coins that already had a higher value are unchanged
- `maxconnections` tuned per-coin based on network peer availability
- `maxoutboundconnections` added for all coins
- `forcednsseed=1` added to all clearnet (non-Tor) coin configs — ensures DNS seed lookup is performed even when the local peer cache appears populated, improving connectivity on fresh installs

**Installer — DNS Seeds Verified and Updated**
- DNS seed lists reviewed and updated for all 14 supported coins (BTC, BCH, DGB, BC2, LTC, DOGE, DGB-SCRYPT, PEP, CAT, NMC, SYS, XMY, FBTC, QBX)
- Stale or defunct seeds removed; active seeds confirmed

**Installer — Multi-Coin RAM Warning**
- RAM warning block added to the multi-coin selection flow — calculates minimum required memory for the selected coin combination and warns the operator if available RAM may be insufficient for concurrent initial sync

**Installer — Per-Coin CLI Address Flags**
- `install.sh` now accepts per-coin wallet address flags: `--ltc-address`, `--doge-address`, `--bc2-address`, `--nmc-address`, `--qbx-address`, `--pep-address`, `--cat-address`, `--sys-address`, `--xmy-address`, `--fbtc-address`
- Enables fully non-interactive deployments and automated re-installs with pre-supplied addresses for all coin types

**Installer — `--version` Flag**
- `install.sh --version` prints the installer version string and exits — useful for scripted pre-flight checks and automated provisioning workflows

**`spiralctl` — Automatic Pool User Elevation**
- `spiralctl` commands that operate on pool files and services are now automatically re-executed as `spiraluser` via `sudo -u` when invoked as root or another user
- Eliminates "permission denied" errors when operators run `spiralctl` as root

**MOTD — Consistent Column Alignment**
- Login MOTD redesigned with uniform column spacing — service status, command grid, and coin list use fixed-width `printf`-padded columns throughout
- Status icons and color codes decoupled from column width calculation; padding computed in plain variables before color embedding — eliminates display misalignment caused by invisible color escape bytes being counted as printable width
- All section dividers unified to 90 characters; section labels removed for a cleaner layout
- `spiralctl coin-upgrade` replaces the old `coin-upgrade.sh` reference in the command grid
- Version string updated to `V1.1.0 — PHI FORGE EDITION`

**Docker — ntfy and SMTP Environment Variable Support**
- `docker/.env.example`: added `NTFY_URL`, `NTFY_TOKEN`, `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_TO` fields
- `docker/docker-compose.yml`: ntfy and SMTP vars now passed through to the Sentinel container
- `SpiralSentinel.py`: all 8 variables added to `env_overrides` — Docker deployments can configure ntfy and SMTP via environment variables without editing `config.json`
- Docker installer (single-coin and multi-coin paths) now includes ntfy and SMTP configuration prompts

**Documentation — Sentinel Configuration Reference Expanded**
- `docs/reference/SENTINEL.md`: 15 previously undocumented configuration keys added with descriptions, types, defaults, and examples
- `scheduled_maintenance_windows` format documented with `start` / `end` / `days` / `reason` field descriptions
- ntfy (`ntfy_url`, `ntfy_token`) and SMTP (`smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_from`, `smtp_to`) added to the environment variables table for Docker operators

### Security

**Stratum — `POST /api/admin/kick` Input Validation**
- The `ip` query parameter was passed directly to `KickWorkerByIP` without validation — a crafted value could match unintended sessions via prefix matching
- Fixed: strict IP format validation via `net.ParseIP()` applied before the call

**Sentinel — SMTP No TLS Certificate Verification**
- Both STARTTLS and SMTP_SSL paths used the default (unverified) context, leaving email credentials exposed to MITM on untrusted networks
- Fixed: `ssl.create_default_context()` used for both paths — verifies cert chain and hostname

### Fixed

**Sentinel — Zombie Miner Kick-First Remediation — Inverted Escalation Logic**
- The two-stage escalation condition was backwards: the `else` branch (kick age < 15 min, i.e., kick just happened) was triggering an immediate miner reboot on the very next monitoring cycle (~30 seconds after the kick)
- Fixed: proper three-state check — `last_kick == 0` kicks, `kick_age < window` waits, `kick_age >= window` escalates

**Sentinel — Telegram Message Truncation**
- Messages truncated at exactly 4096 bytes could be cut mid-MarkdownV2 escape sequence, causing Telegram to reject the entire message with a 400 parse error
- Fixed: truncates at 4000 characters and appends `...` leaving room for a clean escape boundary

**Sentinel — Health Server Thread Exits Permanently on Error**
- If the health endpoint port was already in use at startup, or if `serve_forever()` encountered an unexpected exception, the background thread exited silently and the `/health` and `/cooldowns` endpoints became permanently unavailable
- Fixed: retry loop with 30-second backoff restores the endpoint once the port clears

**Sentinel — Alert Deduplication After Quiet Hours**
- `update_available` and `missing_payout` alerts were silently dropped instead of being re-queued when they fired during quiet hours
- Fixed: suppressed alerts are now correctly re-delivered after quiet hours end

**Stratum — `client.reconnect` Params Field**
- `BuildReconnect` emitted `"params": null` — some mining firmware rejects non-array `params` in stratum JSON-RPC
- Fixed: `"params": []`

**`spiralctl config list-cooldowns` — Port Hardcoded**
- The Sentinel health port was hardcoded to 9191, ignoring the `sentinel_health_port` value in `config.json`
- Fixed: port read from `config.json` at runtime, with 9191 as fallback

**`spiralctl log errors` — Subcommand Consumed as Window Argument**
- `spiralctl log errors 24h` passed `"errors"` as the window argument, failing the `^[0-9]+[smhd]$` validation — the command was effectively unusable with a time argument
- Fixed: `"errors"` subcommand is consumed before the window is parsed

**`spiralctl config validate` — Config Path Interpolated into Python String**
- The YAML syntax check used `open('$config_yaml')` inside a `-c` string — a config path containing a single quote would break the Python expression
- Fixed: path passed via `sys.argv[1]` through a heredoc

**`_send_cooldowns` — Dict Iteration Race**
- `state.last_alerts` was iterated directly while the monitor loop could be writing to it, risking a `RuntimeError: dictionary changed size during iteration`
- Fixed: snapshot copy taken before iteration

**Sentinel — `difficulty_alert_threshold_pct` Fallback Default Mismatch**
- `check_difficulty_changes()` called `CONFIG.get("difficulty_alert_threshold_pct", 10)` while the `DEFAULT_CONFIG` dict sets the key to `25` — the safety-net fallback and the real default were out of sync
- Fixed: fallback changed to `25` to match the documented and intended default

**Sentinel — `hashrate_crash` Cooldown Not Applied in DEFAULT_CONFIG**
- CHANGELOG documented the cooldown increase from 1 hour to 6 hours, and the comment was updated, but the actual value in `DEFAULT_CONFIG["alert_cooldowns"]["hashrate_crash"]` was never changed from `3600` — existing installs without a custom `config.json` override would still get 1-hour cooldowns
- Fixed: value corrected to `21600`

**Telegram Bot — `/pause [minutes]` Argument Never Parsed**
- `_handle_telegram_command` normalized `cmd` with `.split("@")[0]`, which preserved the full text including arguments — `"/pause 30"` stayed `"/pause 30"`, so `if cmd == "/pause":` never matched when arguments were present; `/pause 30` fell through silently to "Unknown command" and bare `/pause` was the only form that worked
- The handler also referenced an undefined `text` variable for argument splitting, which would raise `NameError` on execution
- Fixed: normalization now extracts just the command word (`raw_text.split()[0].split("@")[0]`); the `/pause` handler reads `raw_text` for argument parsing

**install.sh — New v1.1.0 Alert Threshold Keys Missing from Generated `config.json`**
- Fresh installs wrote the boolean enable/disable flags for new v1.1.0 alert features but omitted the corresponding threshold values (`dry_streak_multiplier`, `difficulty_alert_threshold_pct`, `disk_warn_pct`, `disk_critical_pct`, `mempool_alert_threshold`, `backup_stale_days`, `ha_role_change_confirm_secs`, `scheduled_maintenance_windows`) — Sentinel used its `DEFAULT_CONFIG` fallbacks correctly, but the generated `config.json` was incomplete
- Fixed: all 8 threshold keys now written with their defaults during installation

**Sentinel — Disk Space, Difficulty, and Dry Streak Alerts Silently Blocked for Second Resource**
- `check_disk_space` tracks per-path cooldowns (`"disk_critical:/"`, `"disk_critical:/spiralpool"` etc.) before calling `send_alert`, but `send_alert`'s internal generic rate limiter re-tracks under the bare key `"disk_critical"` — the first path's alert set the generic key, blocking the second path's alert for the entire cooldown period
- Same issue in `check_difficulty_changes` (per-coin pre-check key `"difficulty_change:BTC"` vs generic send_alert key `"difficulty_change"`) and `check_dry_streak` (per-coin `_dry_streak_tracking` vs generic `"dry_streak"` key)
- Fixed: all three functions now pass `state=None` to `send_alert` to bypass the redundant generic rate limiter, since they already manage their own per-resource cooldown tracking

**Installer — Wallet Manager Numeric Selection**
- Wallet manager address selection accepted free-form input but failed to map numeric menu choices to the correct wallet entry — selecting by number returned an invalid or empty address
- Fixed: numeric input now correctly resolved to the corresponding wallet record before proceeding

**Installer — DGB-SCRYPT Not Counted in Multi-Coin Sync Warning**
- `DGB-SCRYPT` was omitted from the post-install sync warning counter — the "N coins enabled" message showed a count one lower than the actual number of enabled coins when DGB-SCRYPT was selected
- Fixed: `ENABLE_DGB_SCRYPT` guard added to the counter block

**Installer — DGB-SCRYPT `POOL_ADDRESS` Not Inherited from CLI Flag**
- When `--address` was supplied on the command line, the `dgb-scrypt` case in `apply_cli_coin_config()` did not fall back to `CLI_ADDRESS` — the address was silently dropped and a manual prompt appeared even in non-interactive installs
- Fixed: `POOL_ADDRESS="${POOL_ADDRESS:-$CLI_ADDRESS}"` added to the `dgb-scrypt` case

**`coin-upgrade.sh` — QBX Version Always Reported as `unknown`**
- `qbitx --version` outputs `"Q-BitX daemon version"` with no version number — a bug in the QBX binary itself. The version regex (`(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*`) correctly fails to match and falls back to `"unknown"`, causing the version table to always show `unknown` for QBX regardless of what is installed
- Fixed: `get_installed_version()` now checks a version cache file (`$INSTALL_DIR/config/coin-versions/<COIN>.ver`) before running `--version`. After a successful upgrade, the target version is written to the cache when the binary reports `unknown`. Future `--check` runs read the cache and show the correct version.

**`spiralctl coin` — `list` Subcommand Missing from Help Text**
- `spiralctl help` displayed `coin [status|disable]`, omitting the `list` subcommand
- Fixed: `show_help()` and the inline `cmd_coin()` fallback both updated to `coin [status|list|disable]`

**`upgrade.sh` Fix 7 — `admin_api_key` Not Migrated from v1 Config Format**
- v1.0.0 config stored the admin API key as `adminApiKey` under the `api:` YAML section; v1.1.0 stratum reads `admin_api_key` under `global:` only — after upgrading, the key was present in the config file but silently ignored by the new binary, leaving admin endpoints inaccessible and stratum kick disabled
- Fixed: `upgrade.sh` Fix 7 now reads `adminApiKey` from the `api:` section (v1 location), injects it as `admin_api_key` under `global:` (v2 location), and logs the migration; if neither location has a value, a new secure key is generated; if `global.admin_api_key` is already present (idempotent re-runs or fresh v1.1 installs), the fix is skipped

**`spiralctl config validate` — `wallet_address` Incorrectly Flagged as Missing**
- The validator always flagged `wallet_address` as empty/missing, even when the config intentionally omits it (multi-coin mode, custom coin setups) — every validate run showed a spurious warning
- Fixed: an absent `wallet_address` key is now valid; only explicit placeholder strings (`YOUR_DGB_ADDRESS`, `YOUR_ADDRESS`, `PENDING_GENERATION`, or any value containing `YOUR`) trigger the warning

**`spiralctl config validate` — `admin_api_key` Not Detected in v1 Config Format**
- The validator checked only for `admin_api_key:` (v2 snake_case) — configs upgraded from v1.0.0 that still had `adminApiKey:` (v1 camelCase) in the `api:` section were incorrectly flagged as missing the key
- Fixed: grep pattern updated to `admin_api_key:|adminApiKey:` — both formats satisfy the check

**`spiralctl config validate` — Sentinel Config Checked When Sentinel Is Not Installed**
- On installations without Sentinel enabled, `spiralctl config validate` attempted to check `config.json` and printed misleading errors about missing Sentinel configuration
- Fixed: Sentinel config block is skipped with an informational message when `spiralsentinel.service` is not enabled

**Dashboard — Setup Page Device Type Parity**
- Setup wizard (`/setup`) now shows all 26 individual device type sections, matching the settings page — previously only 2 grouped sections (AxeOS and CGMiner API) were shown
- Each device type has its own container, add button, icon, and description
- Device scanner on setup correctly routes discovered devices to their individual sections
- `VALID_DEVICE_TYPES` and `CGMINER_DEVICE_TYPES` sets defined for consistent type handling across all JS functions
- QAxe+ correctly shares the QAxe container (special-cased throughout)

**Dashboard — Pool-Specific Statistics**
- "Miners Online" stat card now shows stratum-connected miner count (`pool_connected_miners`) as the primary number, with fleet count as secondary "(Fleet: N online)" — previously showed fleet-wide network device count which was misleading for multi-pool operators
- "Pool Hashrate" label replaces "Total Hashrate" — value already preferred pool stratum hashrate, but the label implied it was a fleet total
- "Pool Shares" in Lifetime Statistics now reads `pool_accepted_shares` directly from Prometheus (`stratum_shares_accepted_total`) — previously showed miner-reported combined total from all pools
- Hashrate sub-text fallback shows pool-connected count instead of fleet count

**Dashboard — BitAxe / NMaxe Device Separation**
- "AxeOS / NMAXE Devices" section renamed to "BitAxe Devices" on both setup and settings pages — NMaxe has its own dedicated section
- Button labels updated: "Add AxeOS Device" → "Add BitAxe Device"

**Dashboard — Theme Ambient Glow Brightness**
- Cyberpunk base CSS ambient glows brightened to match Summer Vibes blending intensity: cyan 0.08→0.22, purple 0.04→0.14, red/orange 0.03→0.10; background grid lines 0.02→0.04
- 8 themes updated: Meltdown, Chrome Warfare, Gruvbox Dark, Black Ice, Nord, Tokyo Night, Dracula, Ocean Depths

**install.sh — Scanner BitAxe / NMaxe Separation**
- `detect_miner_type()`: BitAxe variants (Supra, Ultra, Gamma, Hex) now correctly output `axeos` type — previously misclassified as `nmaxe` because both shared a single detection branch
- NMaxe detection narrowed to match only `nmaxe` string
- Manual device type selection menu: BitAxe added as option 1 (`axeos`), NMaxe as option 2, all 24 options renumbered with corrected case statement
- Initial `miners.json` template updated from 6 device types to all 26

**Dashboard — NerdQAxe++ Missing Temperature, Firmware, Frequency, Voltage, Fan Speed, Pool URL, and Best Difficulty**
- `fetch_axeos()` NMAxe detection (`isinstance(data.get('stratum'), dict)`) was too broad — NerdQAxe++ firmware v1.0.36+ includes a `stratum` object in its `/api/system/info` response, causing it to be misclassified as NMAxe
- NMAxe branch reads different field names: `asicTemp` instead of `temp`, `fwVersion` instead of `version`, `freqReq` instead of `frequency`, `fans[0].rpm` instead of `fanspeed`, `hostName` instead of `hostname`, `bestDiffEver` instead of `bestDiff`, `stratum.used.url` instead of `stratumURL:stratumPort`
- All fields returned `0`/`Unknown`/empty, causing the dashboard to show `--` for temperature, firmware, frequency, voltage, fan speed, best difficulty, and pool URL on all NerdQAxe++ devices
- Fixed: NMAxe detection now requires `asicTemp` field presence alongside the `stratum` dict check — devices with a `stratum` object but standard AxeOS field names correctly fall through to the standard path

**Dashboard — Miners Online Showed Fleet Count Instead of Pool-Connected Count**
- "Miners Online" displayed `totals.online_count` (all configured devices responding on the network) instead of `data.pool_connected_miners` (miners with active stratum sessions on this pool)
- Multi-pool operators saw all 7 network miners as "online" even when only 1 was connected to this pool's stratum

**Dashboard — Lifetime Pool Shares Showed Miner-Reported Fleet Total**
- `lifetime.total_pool_shares || lifetime.total_shares` used JS `||` which treats `0` as falsy — `total_pool_shares` started at `0` (new field), so it always fell through to `total_shares` (miner-reported combined total from all pools)
- Fixed: uses explicit `> 0` checks and reads `data.pool_accepted_shares` (live Prometheus value) as primary source

**Dashboard — 90-Second Delay Before Miners Appear After Setup**
- `miner_cache["last_update"]` was initialized to `time.time()` at startup, making an empty cache appear fresh for 90 seconds
- First dashboard load after setup showed "No Devices Configured" until the cache expired
- Fixed: initialized to `0`; config save endpoint also resets to `0` for immediate re-fetch

**Dashboard — Settings Gear Icon Not Centered**
- Settings button (`⚙`) used padding-only centering on an `<a>` tag — emoji glyph rendered off-center due to uneven Unicode metrics
- Fixed: explicit `display: inline-flex; align-items: center; justify-content: center` with fixed dimensions

**install.sh — BitAxe Devices Misclassified as NMaxe by Scanner**
- `detect_miner_type()` lumped BitAxe and NMaxe into a single branch matching `nmaxe|bitaxe|supra|ultra|gamma|hex` — all BitAxe variants were tagged `nmaxe`
- Fixed: NMaxe matches only on `nmaxe`; BitAxe variants match on `bitaxe|supra|ultra|gamma|hex` and output `axeos`

**install.sh — Manual Device Type Menu Had Duplicate Number and Missing BitAxe Option**
- Menu items 16 and 17 were both numbered `17)` (ebang and gekkoscience); BitAxe (`axeos`) was not listed as a selectable option at all
- Fixed: BitAxe added as option 1, all 24 options renumbered sequentially with matching case statement

**Sentinel — `global _stratum_down_alerted` Syntax Error on Startup**
- Redundant `global _stratum_down_alerted` declaration in `check_pool_status()` at line 17977 — the variable was already declared global at line 17960 in the same function scope
- Python 3 treats a `global` declaration after any use of the variable name in the same scope as a `SyntaxError`, causing Sentinel to crash-loop immediately on startup
- Fixed: removed the redundant `global` statement

**`upgrade.sh` — Service Drain Loop Exited Immediately for "deactivating" Services**
- `systemctl is-active --quiet` returns exit code 3 for the `deactivating` state (not just `inactive`) — the drain loop's boolean check treated "deactivating" as "not active" and exited at `wait_count=0`
- With the loop exiting immediately, `start_services()` ran against a still-deactivating service, causing stratum and sentinel to fail to start after every upgrade
- Fixed: drain loop now captures the actual state string via `systemctl is-active` and only breaks on `inactive` or `failed` — `deactivating` and `activating` states are correctly waited out

**`upgrade.sh` — `systemctl is-active` Capture Patterns Incompatible with `set -e`**
- Three locations used `$(systemctl is-active "$service" 2>/dev/null || echo "unknown")` — `systemctl is-active` prints its state to stdout even on non-zero exit, so `|| echo` appended `"unknown"` on a new line, producing multiline values that broke status display and comparisons
- Removing `|| echo` fixed the multiline issue but exposed the non-zero exit code to `set -e` (enabled at line 100), which killed the entire upgrade script mid-run
- Fixed: all four locations (drain loop ×2, status verification, final display) now use `svc_state=$(systemctl is-active "$service" 2>/dev/null) || true` — `|| true` outside `$()` suppresses `set -e` without appending to stdout

**`upgrade.sh` — `migrate_coin_version_cache()` Wrote Target Version Instead of Installed Version**
- When no `.ver` cache file existed (every v1.0 → v1.1 upgrade), the function wrote the v1.1 TARGET version to the cache — this made `coin-upgrade.sh --check` report QBX as already at the target version, silently skipping the upgrade
- QBX was especially affected because its `--version` output contains no parseable version number, so the fallback was always used
- Fixed: renamed `_VC_VER` (target versions) to `_VC_PREV` (v1.0 shipped versions) with QBX corrected from `0.2.0` to `0.1.0`; function now tries `--version` detection first and falls back to `_VC_PREV` only when detection fails

**`coin-upgrade.sh` — False Version Warning for Daemons Without Parseable `--version` Output**
- Post-upgrade version verification read the stale cache (pre-upgrade version), then compared it against the target — QBX always showed `"Binary reports '0.1.0' — expected '0.2.0'"` even after a successful upgrade
- Fixed: cache is written with the target version FIRST, then the binary's `--version` is checked directly (bypassing cache); daemons with no parseable version output (QBX) show a success message with "(version cached — daemon has no version output)" instead of a false warning

**`coin-upgrade.sh` — Garbled Backup Path Display**
- `backup_coin()` used `log_success` (stdout) for progress messages inside a function whose stdout was captured by `backup_path=$(backup_coin "$coin")` — log messages were concatenated into the backup path variable
- Fixed: all log messages inside `backup_coin()` redirected to stderr (`>&2`)

**`coin-upgrade.sh` — CLI Calls Missing `-conf` Flag (Wrong RPC Port)**
- `wait_for_daemon()` and the reindex monitor hint ran bare CLI commands (e.g. `qbitx-cli getblockchaininfo`) without `-conf=` — every coin uses a non-default RPC port, so the CLI defaulted to Bitcoin's port 8332 and timed out after 120s even though the daemon was healthy
- Fixed: added `COIN_CONF` map and `get_coin_cli()` helper; all CLI calls now include `-conf=<path>` matching the patterns in install.sh's `get_cli_cmd()`; multi-disk (`CHAIN_MOUNT_POINT`) supported

**Dashboard — Pool Hashrate Showed Farm Hashrate When No Miners Connected**
- "Pool Hashrate" stat card fell back to farm device hashrate (`farmHashrateThs`) when stratum-reported pool hashrate was 0 — a fresh install with 7 fleet miners configured but none connected to the pool showed 32 TH/s under "Pool Hashrate"
- Fixed: when `pool_connected_miners` is 0, the display shows 0 instead of falling back to farm hashrate

**Sentinel — Pool Block Counter Reset After Database Restore**
- `_init_state()` seeded `pool_blocks_found` from the database API, but `load()` ran immediately after and overwrote it with the stale value from `state.json` — after a database restore importing historical blocks, Discord notifications showed "Block #17" instead of "#643"
- Fixed: API re-seeding moved into `load()` after state.json is applied; uses `max(state_value, db_count)` so database restores, fresh installs, and normal restarts all produce the correct count

### Changed

- Version strings updated throughout: `1.0.0 / BLACKICE` → `1.1.0 / PHI_FORGE`
- Sentinel `hashrate_crash` alert cooldown increased from 1 hour to 6 hours — reduces repeated notifications during sustained network hashrate drops
- HA role change debounce changed from cycle-based (1 × 30s poll) to timestamp-based (configurable, default 90s) — suppresses longer VRRP election blips that the old debounce missed
- Dashboard "Total Hashrate" stat label renamed to "Pool Hashrate" for clarity

---

## [1.0.0] — BlackICE

> *Initial release.*

### Added

**Core Stratum Engine**
- Stratum V1, V2 (Noise Protocol encryption), and TLS — multi-port per coin
- SHA-256d and Scrypt algorithm support with dedicated difficulty profiles per algorithm
- Lock-free share pipeline: ring buffer (1M capacity, MPSC) → WAL → PostgreSQL COPY batch insert
- Per-session atomic vardiff state; asymmetric ramp limits (4× up / 0.75× down); 50% variance floor
- Non-custodial solo payout: block reward embedded directly in the coinbase transaction to the miner's wallet — no pool wallet, no intermediate custody, no fees

**Spiral Router — Miner Classification**
- Classifies connected miners at connection time using 280+ user-agent signatures
- 15 SHA-256d difficulty profiles and 8 Scrypt difficulty profiles
- Automatic fallback to safe default profile for unknown hardware
- Supports Antminer, Whatsminer, Avalon, BitAxe, NerdAxe, NerdQAxe, Compac F, LuckyMiner, FutureBit Apollo, iBeLink, and all Stratum V1-compatible hardware

**Supported Coins at Launch**
- **SHA-256d:** Bitcoin (BTC), Bitcoin Cash (BCH), DigiByte (DGB), Bitcoin II (BC2), Namecoin (NMC), Syscoin (SYS), Myriadcoin (XMY), Fractal Bitcoin (FBTC)
- **Scrypt:** Litecoin (LTC), Dogecoin (DOGE), DigiByte-Scrypt (DGB-SCRYPT), Pepecoin (PEP), Catcoin (CAT)
- Total: 13 coins, 2 algorithms

**Merge Mining (AuxPoW)**
- 6 AuxPoW pairs: NMC/BTC (chain ID 1), SYS/BTC (chain ID 16), XMY/BTC (chain ID 90), FBTC/BTC (chain ID 8228), and LTC-parent Scrypt pairs
- Syscoin is merge-mining only (no standalone solo mining due to CbTx/quorum commitment requirements)

**High Availability**
- VIP failover via keepalived
- Patroni-managed PostgreSQL replication
- Blockchain rsync between master and backup nodes
- Advisory lock payment fencing — prevents double-payment during failover
- `spiralpool-ha-watcher.service` — manages Sentinel start/stop based on HA role

**Spiral Sentinel**
- Autonomous monitoring daemon: device discovery, connection tracking, hashrate monitoring, temperature alerts, block find notifications
- Quiet hours: configurable suppression window (default 22:00–06:00)
- Scheduled reports: configurable intervals plus a final pre-quiet-hours report
- SimpleSwap swap alerts: optional notification when a mined coin rises 25%+ vs BTC over 7 days, with pre-filled conversion link (operator-initiated only — no automatic swaps)
- Achievement system, miner nicknames, and historical stats

**Spiral Dash**
- Real-time web dashboard on port 1618
- Multi-theme support
- Per-miner worker statistics, block history, hashrate charts

**`spiralctl` CLI**
- Runtime operator control: coin management, pool status, miner listing, difficulty inspection, maintenance mode, HA management, GDPR/data purge, Tor control
- `spiralctl add-coin` — onboarding automation for NET NEW unsupported coins

**Installer (`install.sh`)**
- Two deployment paths: native/VM and Docker bare-metal
- Docker existing-install detection (`detect_existing_docker_install()`) — reads `docker/.env`, offers Add Coins vs Fresh Install
- Automated TLS certificate provisioning (Let's Encrypt or self-signed)
- HA node setup: keepalived, etcd, Patroni, UFW rules, sudoers entries
- WSL2 support for Windows operators

**Observability**
- Prometheus metrics with per-session worker-level labels
- Grafana dashboard templates

**Testing**
- 3,500+ tests: unit, integration, chaos, and fuzz
- 10 numbered chaos test suites

---

*Spiral Pool — BSD-3-Clause — Non-Custodial — Solo Mining — Proof-of-Work*
