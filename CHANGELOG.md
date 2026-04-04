# Changelog

All notable changes to Spiral Pool are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows `MAJOR.MINOR.PATCH`  -  patch releases are applied in-place on the same tag.

---

## [2.2.1]  -  2026-04-04  -  Phi Hash Reactor

> *Smart Port multi-coin audit — 13 fixes across Go, Python, JS.*

### Fixed

**Smart Multi-Port — Shared-DB bugs (all coins shared first coin's database queries)**

- **Dashboard shows N× pool hashrate in Smart Multi-Port mode** -- `CoinPool.GetHashrate()` called `db.GetPoolHashrate()` which queries `shares_<firstPoolID>` (the shared DB's default pool ID set during coordinator init). All CoinPools returned the same hashrate from the first coin's share table. Dashboard summed N identical values → N× the actual hashrate on the overview card and "All Coins" statistics view. Fixed by switching to `db.GetPoolHashrateForPool(poolID, ...)` which queries the correct per-coin share table
- **Block reconciliation queries wrong coin's blocks table** -- `GetBlocksByStatus()` used `db.poolID` (shared firstPoolID). After a crash, non-first coins reconciled the first coin's "submitting" blocks instead of their own, potentially missing stuck blocks or reconciling the wrong coin's data. Created `GetBlocksByStatusForPool(poolID, ...)` and updated `reconcileSubmittingBlocks()` to pass `cp.poolID`
- **Stale share cleanup targets wrong coin's shares table** -- `CleanupStaleShares()` used `db.poolID` (shared firstPoolID). Every CoinPool cleaned `shares_<firstCoin>` on startup — first coin's shares got cleaned N times, other coins' shares never got cleaned, leading to unbounded table growth. Created `CleanupStaleSharesForPool(poolID, ...)` and updated `cleanupStaleShares()` to pass `cp.poolID`
- **Removed stale shared-DB methods from CoinPool interface** -- `GetPoolHashrate()`, `GetBlocksByStatus()`, and `CleanupStaleShares()` removed from `coinPoolDB` interface. The compiler now enforces that only the per-pool variants can be called, preventing regression

**Smart Multi-Port — Scheduler**

- **Broken sessions when no coin pools available** -- `handleConnect()` incremented `activeSessions` counter then returned early when no coin pools were running, leaving the session in a non-functional state (no coin assigned, can't submit shares, can't receive jobs). Counter corrected only on disconnect. Now decrements the counter before the early return
- **Cross-coin job invalidation on coin switch** -- `SendJobToSession(cleanJobs=true)` invalidated ALL jobs in the shared `s.jobs` map, including jobs belonging to sessions on other coins. When DGB found a new block, BTC/BCH session jobs were wiped from bookkeeping. Fixed by removing blanket invalidation in `SendJobToSession`; now stores the new job and evicts oldest when map exceeds 10 entries, matching `BroadcastJob`'s pruning pattern. `BroadcastJob` (single-coin path) left unchanged where full invalidation is correct
- **Missing `mining.set_difficulty` on coin switch** -- `switchSessionCoin()` sent the new coin's job but never sent `mining.set_difficulty` beforehand. cgminer/bmminer firmware applies the last received `set_difficulty` to the next job — without re-sending it, miners used the previous coin's difficulty for the new coin's shares, causing rejection. `sendCoinJob()` now calls `SendDifficulty(session, currentDiff)` before sending the job when `cleanJobs=true`

**Smart Multi-Port — Dashboard**

- **Network difficulty always from first pool, not active coin** -- `fetch_pool_stats()` hardcoded `pools[0]` for network difficulty and block height. In multi-coin mode, this always showed the first coin's difficulty regardless of which coin was being mined. Now uses the pool with the highest hashrate (the most actively mined coin)
- **Luck tracker produces wrong percentages in multi-coin mode** -- `update_luck_tracker()` mixed aggregate `blocks_found` from all coins with `network_difficulty` from primary pool only and `algorithm` from primary coin only. Finding a QBX block while using BTC difficulty gave wildly wrong luck. Now calculates expected blocks per-coin using each coin's own difficulty and algorithm, then sums

**Dashboard — Thread Safety**

- **`/api/miners` and `/api/combined` race condition on miner_cache** -- reader endpoints iterated `miner_cache["miners"]` dict without holding `_miner_cache_lock`, while the background poller could replace it mid-iteration. Under load (many miners, frequent polls), this causes `RuntimeError: dictionary changed size during iteration` crashing the API response. Both endpoints now snapshot the dict under the lock before iterating
- **Duplicate block celebration announcements** -- `fetch_pool_stats()` detected new blocks by count comparison (`new_count > old_count`) but did not deduplicate by block identity. If the API returned a slightly different block list between polls (reordered, or a block changed status causing a re-count), the same block could be announced multiple times. Now tracks announced blocks by `(height, hash)` tuple and skips duplicates

**Other**

- **`GetSharesPerSecond()` returns meaningless lifetime average** -- divided total lifetime accepted shares by 3600 (a fixed constant), producing the same value regardless of actual current submission rate. Now divides by actual elapsed time since pool start

### Changed

- **Version bump** -- all version strings updated to 2.2.1

---

## [2.2.0]  -  2026-03-31  -  Phi Hash Reactor

> *Coin management hardening. Surgical operations.*

### Added

- **Async coin install/remove** -- install and remove API endpoints now return 202 immediately and run in a background thread. New `GET /api/nodes/<symbol>/install-status` endpoint for polling progress. Dashboard UI polls every 3s with elapsed time display, preventing NetworkError on long installs or dashboard restarts
- **Two-button wallet install flow** -- coin install modal offers "I have a wallet address" (text input) or "Generate a wallet from the node" (installs first, polls `getnewaddress` RPC every 5s for up to 30 minutes, shows backup warning)
- **`POST /api/nodes/<sym>/generate-wallet`** -- generates a new wallet address from the running coin daemon's built-in wallet, validates it, and writes it to config.yaml
- **Stratum ports in coin nodes card** -- installed coins now show their V1 stratum port number inline
- **Auto-refresh coin nodes card** -- 15-second polling while any coin is syncing or being watched

### Fixed

**Coin Add/Remove (Critical)**
- **`add_coin` nukes entire config** -- called `generate_config` which destructively rewrote all of config.yaml, wiping wallet addresses, RPC credentials, and restarting ALL daemons. Replaced with surgical Python YAML append that only adds the new coin to the `coins:` array. V1-to-V2 config conversion preserves all existing settings
- **`remove_coin` nukes entire config** -- same root cause. Replaced with surgical Python YAML removal that deletes only the target coin entry. Other coins, HA settings, and all configuration preserved. Added safety guards: logs all symbols before/after removal, aborts if more than 1 entry would be removed or coins list would be left empty, restores backup on abort
- **`remove_coin` leaves config.yaml owned by root** -- Python YAML write via `systemd-run` (root) created the file as root:root 0600. Dashboard (spiraluser) could not read it, showing all coins as "not installed". Added `os.chown` to pool_user in both add and remove Python scripts
- **`add_coin` V1→V2 symbol mapping broken** -- converting V1 config (`pool.coin: digibyte`) to V2 used `.upper()` which gave `DIGIBYTE` instead of `DGB`. Dashboard lookup against `MULTI_COIN_NODES` failed silently. Added proper mapping table for all 14 coins (digibyte→DGB, bitcoin→BTC, bitcoincash→BCH, etc.)
- **Removed coin stays "enabled" in dashboard** -- `load_multi_coin_config()` set `enabled=True` for coins in config.yaml but never reset previously-enabled coins to `False`. After removal, the coin remained enabled in memory until dashboard restart. Now resets all coins to disabled before loading
- **Health cache not invalidated after add/remove** -- 10-second health cache was not cleared after install/remove operations. Dashboard refresh returned stale state. Now sets `last_update=0` to force fresh fetch
- **`setup_node` destroys pruned nodes** -- all 13 coins hardcoded `prune=0` in their conf file templates. Running `setup_node` on a node with `prune=5000` overwrote the setting, causing the daemon to crash-loop trying to run unpruned on pruned data. Added `get_existing_prune()` helper that reads the current prune value before overwriting
- **Service file left behind after remove** -- `stop_node` deleted the service file but `generate_config` recreated it. Added explicit post-removal cleanup with `systemctl daemon-reload` and `reset-failed`

**Systemd Service Files**
- **`spiraldash.service` hard-depends on stratum** -- `After=spiralstratum.service` prevented dashboard from starting when stratum was stuck waiting for a blockchain node to load. Dashboard handles stratum unavailability gracefully. Removed the dependency
- **QBX PID file mismatch** -- service expected `qbitxd.pid` but QBX binary (Bitcoin fork) creates `bitcoind.pid`. Systemd showed "activating" forever. Added explicit `-pid=` flag to ExecStart
- **6 coins missing `-pid=` flag** -- PEP, CAT, NMC, SYS, XMY, FBTC service templates had `PIDFile=` directives but no matching `-pid=` flag in ExecStart. Bitcoin-fork daemons create default PID filenames that don't match, causing perpetual "activating" state. Added `-pid=` to all 6
- **QBX/CAT binaries not executable after ZIP install** -- ZIP files don't preserve Unix execute permissions unlike tarballs. Added `chmod +x` after extraction for both coins

**Dashboard Display**
- **QBX sync shows 0%** -- `verificationprogress` RPC field reports near-zero for QBX due to low chain work. Multi-coin node health now uses `blocks/headers` ratio for QBX, matching the primary coin health check
- **All coins show DGB network hashrate** -- multi-coin node health used a global `pool_stats_cache["node_networkhashps"]` which only cached the primary coin's value. Changed to per-coin `coin_rpc(symbol, "getnetworkhashps")` call
- **Wallet generation times out on slow chains** -- `generate-wallet` endpoint returned 503 with generic "not running or synced" error during block index loading (RPC error -28). Frontend capped at 60 attempts (5 min). Now distinguishes retryable (node loading) vs permanent (wallet disabled) errors, frontend polls up to 360 attempts (30 min), and stops immediately on permanent failures

**Sentinel**
- **QBX missing from `detect_pool_mode`** -- coin type map in `detect_pool_mode()` was missing QBX entries. Sentinel reported "SOLO with coins: DGB" even when QBX was active. Added `qbitx`, `q-bitx`, `qbx` mappings

**V1→V2 Config Conversion**
- **V1 stratum settings lost during V1→V2 conversion** -- when `add_coin` converts a V1 config (single coin) to V2 (multi-coin), the entire V1 `stratum:` block was copied verbatim into the coin entry. But V2 `CoinStratumConfig` uses different field names (`port` not `listen`, `version_rolling` not `versionRolling`). Go's YAML parser silently ignored the mismatched fields, and V2 defaults kicked in: `initial: 50000` instead of the configured `initial: 5000`, `version_rolling.enabled: true` even if V1 had it disabled. Now properly translates V1 field names to V2 format during conversion, preserving difficulty, banning, connection, and version rolling settings

**Firewall (UFW)**
- **`stop_node` deletes stratum ports from UFW** -- removing a coin closed both its daemon P2P port and its stratum port via `ufw delete allow`. But stratum ports are managed by the stratum binary which listens on all configured coin ports simultaneously. Removing one coin's stratum port from UFW while stratum is still running (or before restart) breaks miner connectivity on that port. Now `stop_node` only closes the daemon P2P port; stratum port cleanup happens naturally when stratum restarts and no longer binds the removed coin's port

**Multi Coin Smart Port**
- **Multi-port miners never receive new block templates** -- when ZMQ or polling detects a new block, the job callback only broadcasts to the coin pool's dedicated stratum server. Miners on the multi-port (16180) kept mining stale blocks indefinitely. Added `SetMultiPortJobListener` callback so coin pools relay new jobs to the multi-port server, which broadcasts to all sessions assigned to that coin
- **Removing a coin leaves stale `multi_port` config** -- `pool-mode.sh --remove`, dashboard `POST /api/nodes/<sym>/remove`, and `spiralctl coin disable` all removed coins from the `coins:` array but left the `multi_port:` section referencing the removed coin. Next stratum restart would fail. All three paths now clean up `multi_port.coins`, redistribute weights proportionally, and disable multi-port if fewer than 2 coins remain
- **`spiralctl mining solo` leaves multi-port enabled** -- switching to solo mode cleared the coins list but left `multi_port.enabled: true`. Now explicitly disables multi-port when switching to solo mode
- **`spiralctl mining multi` ignores stale multi-port coins** -- switching to a different coin set didn't validate that multi-port scheduled coins still exist. Now removes stale coins from the schedule and redistributes weights
- **`coins.env` not synced after multi-port cleanup** -- when `spiralctl coin disable` or `pool-mode.sh --remove` redistributed smart port weights, `coins.env` still had the old coin list and weights. On re-install/upgrade, `install.sh` would restore the stale schedule. Now all cleanup paths sync `MULTIPORT_COINS`, `MULTIPORT_WEIGHTS`, and `MULTIPORT_PREFER_COIN` to `coins.env`
- **Nil coins map panic in cleanup** -- if config had `multi_port: enabled: true` with no `coins:` section, `cleanupMultiPortAfterCoinChange` would panic on nil map operations. Added nil guard
- **`pool-mode.sh` mode switch destroys config sections** -- `switch_to_solo()` and `switch_to_multi()` called `generate_config()` which rewrote config.yaml from scratch, losing `multi_port`, `ha`, `pool`, `vip`, `mergeMining`, custom `stratum` settings, and all other sections. Now preserves all unmanaged sections from backup, disables multi_port in solo mode, and cleans stale coins from the schedule in multi mode
- **`spiralctl` cannot load V2 config** -- `ExtendedConfig.Coins` was `map[string]interface{}` but V2 config uses a YAML list (`- symbol: BTC`). `yaml.Unmarshal` failed with "cannot unmarshal !!seq into map[string]interface{}" making `spiralctl coin disable`, `spiralctl mining solo`, and `spiralctl mining multi` completely broken on any V2 config. Changed to `interface{}` with typed accessor methods. Also fixed `switchToMulti` which wrote coins in map format (missing symbol, address, pool_id, ports) — now preserves existing coin entries and only adds minimal stubs for new coins
- **Dashboard `/api/config` doesn't disable multi-port on solo switch** -- switching from multi-coin to solo mode via dashboard settings left `multi_port.enabled: true`. Added `_disable_multiport_if_enabled()` to set it false atomically and sync coins.env
- **Dashboard `update_multiport()` accepts coins not in pool config** -- smart port could be configured with coins not in the `coins:` array, causing stratum startup failure. Added validation that all multi-port coins exist in pool config
- **`pool-mode.sh` section preserve silently crashes** -- f-string syntax error on the "Preserved from backup" print statement (`f'..{', '.join(..)}..` — inner single quotes terminate the f-string) caused the entire Python merge block to throw `SyntaxError`, caught by the `except` handler. The mode switch proceeded with the stripped config, silently losing all preserved sections (multi_port, ha, vip, etc.)
- **`spiralctl` non-atomic config writes** -- `saveConfig`, `saveExtendedConfig`, and 4 other write paths used `os.WriteFile` which can corrupt config.yaml if the process is killed mid-write. Replaced all 7 call sites with atomic temp+fsync+rename pattern matching dashboard and pool-mode.sh
- **`install.sh` "add coins" upgrade loses multi-port config** -- when reinstalling with "Add coins to existing installation", `MULTIPORT_COINS`, `MULTIPORT_WEIGHTS`, and `MULTIPORT_PREFER_COIN` were never read from `coins.env`. Only `MULTIPORT_ENABLED` was read (and only for port checking). Config regeneration produced an empty multi_port section, silently losing the smart port schedule
- **`spiralctl` coins.env written world-readable** -- `updateCoinsEnvLine()` wrote coins.env with mode 0644 instead of 0600, exposing RPC passwords and API keys to other system users. Also switched to atomic write for crash safety
- **`install.sh` multi-port weight overflow on short weights array** -- if `MULTIPORT_WEIGHTS` in coins.env had fewer entries than `MULTIPORT_COINS`, missing coins defaulted to weight 50, producing totals well over 100%. Now validates sum and redistributes equally if invalid
- **`install.sh` prefer_coin default inconsistent with Go** -- defaulted to first coin in array instead of highest-weight coin, causing different behavior between fresh install and runtime. Now picks the highest-weight coin, matching Go's `cleanupMultiPortAfterCoinChange` logic
- **`spiralctl` solo switch leaves stale coins in config** -- `switchToSolo` set `cfg.Coins = nil` but `saveExtendedConfig` only wrote coins when non-nil, preserving the old multi-coin list from the existing file. On next load, the system still saw multi-coin config. Now explicitly deletes the `coins` key when nil
- **`spiralctl` nil dereference on empty/corrupt config** -- 4 YAML document manipulation functions (`applyMultiPortConfig`, `multiportDisable`, `enableMergeMining`, `disableMergeMiningConfig`) accessed `doc.Content[0]` without bounds checks. An empty or corrupt config.yaml would panic. Added `docRoot()` helper with nil guard at all call sites
- **Multi-port startup failure silently swallowed** -- `coordinator.Start()` logged the error from `startMultiPort()` but continued successfully. Pool reported healthy while multi-port was dead. Miners on port 16180 got connection refused with no indication of why. Now propagates the error to fail startup
- **Pool start failure leaves pool permanently dead** -- during initial startup, if `pool.Start()` fails (e.g., daemon temporarily unavailable, port conflict), the error was logged but the pool remained in `c.pools` in a non-running state forever. Unlike pool *creation* failures which are properly queued for retry during the grace period, start failures were never retried. Now moves failed-start pools to the retry list, stops them to release resources, and lets `retryFailedCoinsLoop` recover them automatically
- **No multi-port config validation at load time** -- `ConfigV2.Validate()` had no checks for multi_port configuration. Invalid weights, missing coins, port conflicts, and case-insensitive symbol duplicates were only detected at runtime startup (or not at all). Added comprehensive validation: port range, port conflicts, minimum 2 coins, weights sum to 100, coin existence check, negative weight check, and case-insensitive duplicate detection
- **Dashboard coin removal drops zero-weight coins from schedule** -- `_cleanup_multiport_after_remove()` filtered out coins with weight=0 before counting remaining coins. With 3 coins (50, 50, 0), removing the first caused multi-port to be disabled (only 1 "remaining" weighted coin) and the zero-weight coin was permanently lost. Now preserves all coins and only redistributes among weighted ones
- **Dashboard `coins.env` write not atomic** -- `_update_coins_env_multiport()` truncated coins.env before writing via `open('w')`. A crash mid-write left it empty/partial. Switched to temp+fsync+rename pattern matching config.yaml writes
- **Dashboard `generate_wallet` silently fails to save address** -- if the coin wasn't found in the config.yaml coins array, the generated address was never written to config but the endpoint returned success with no warning. User believed their address was saved when it wasn't. Now returns explicit warning when config update is skipped
- **`pool-mode.sh` coin removal drops zero-weight coins from schedule** -- same bug as dashboard: `_cleanup_multiport_after_remove` filtered out coins with weight=0 before counting, prematurely disabling multi-port and permanently losing zero-weight coins from the schedule
- **`spiralctl` coin removal drops zero-weight coins from schedule** -- `cleanupMultiPortAfterCoinChange` counted only coins with `Weight > 0` to determine if multi-port should be disabled. With 3 coins (50, 50, 0) and one 50-weight coin removed, only 1 "remaining" weighted coin was counted, disabling multi-port even though 2 coins were still in the map. Now counts all coins regardless of weight
- **Dashboard `generate_wallet` uses wrong wallet** -- `coin_rpc("getnewaddress")` called without specifying the wallet name. `install.sh` creates per-coin named wallets (`pool-btc`, `pool-dgb`, etc.) via `createwallet`. Without `/wallet/<name>` in the RPC URL, `getnewaddress` either fails ("wallet not specified") or generates an address in the default wallet instead of the pool wallet. Now targets the correct named wallet with fallback to default for old daemons

**HA Cluster & Coin Sync**
- **`sync_ha_cluster` never syncs `coins.env`** -- config sync to HA secondary nodes copied config.yaml, ha.yaml, and ha_cluster.conf but not coins.env. After coin add/remove or multiport weight changes, secondary nodes ran with stale multiport schedules. Now copies coins.env with proper permissions (0600)
- **Non-interactive coin operations silently skip HA sync** -- `pool-mode.sh --yes` (used by dashboard and automation) always skipped the HA sync prompt (`NON_INTERACTIVE=true` bypassed the interactive confirmation). Secondary nodes were never synced when coins were added/removed via dashboard. Now auto-syncs HA peers in non-interactive mode
- **Dashboard has zero HA awareness for coin changes** -- `/api/config` POST, `POST /api/nodes/<sym>/install`, and `POST /api/nodes/<sym>/remove` modified local config with no HA detection, no warnings, and no sync. Users had no idea secondary nodes were divergent. Now checks `fetch_ha_status()` and returns `ha_sync_required: true` in the response when HA is active and coins changed. Install/remove status messages note HA sync was attempted
- **Dashboard `save_pool_coin_config` destroys existing config fields** -- `save_pool_coin_config()` cleared `pool_config["coins"]` to an empty list and rebuilt from scratch with only the 6 fields the dashboard manages (symbol, enabled, address, pool_id, stratum.port, daemon.port). All other stratum config (difficulty, banning, TLS, connection, version_rolling, job_rebroadcast), node failover configs, payment settings, merge mining config, and coinbase_text were silently dropped. Now merges dashboard-managed fields into existing coin configs, preserving all fields the dashboard doesn't manage
- **Dashboard config.yaml concurrent write race condition** -- five separate code paths (`save_pool_coin_config`, `update_multiport`, `_disable_multiport_if_enabled`, `_cleanup_multiport_after_remove`, `generate_wallet`) could read-modify-write POOL_CONFIG_PATH concurrently. While individual writes were atomic (temp+rename), two concurrent requests could read the same state and one would overwrite the other's changes. Added `_config_file_lock` (reentrant) to serialize all config read-modify-write cycles

**Dashboard UI**
- **Smart Port settings redesigned as two-column layout** -- coin management (install/remove) on the left, 24h schedule (hours inputs, preview bar, save) on the right. Cleaner separation of concerns, responsive stacking on mobile
- **Remove coin warns about smart port impact** -- removing a coin that's in the active smart port schedule now shows a warning with the coin's weight and whether smart port will be disabled
- **Smart Port status panel on main dashboard** -- new panel in System Health section showing active coin, next switch time, and schedule bars. Two-column layout: node health cards (left), smart port status (right). Links to settings page for full configuration. Hidden when smart port is disabled
- **Stop/Start buttons for coin nodes** -- added ⏹ Stop and ▶ Start buttons alongside the existing 🔄 Restart button in each coin node health card. Stop button uses 660s timeout matching service `TimeoutStopSec`. Start button auto-clears `reset-failed` before starting, fixing nodes stuck after `StartLimitBurst` exhaustion
- **Service list shows orphaned services** -- dashboard service status now detects installed-but-not-in-config services via `systemctl cat`. Prevents a crashed daemon from disappearing entirely from the UI when `get_enabled_coins()` cache refreshes
- **FBTC falsely labelled `[MERGE]` when solo** -- Fractal Bitcoin showed `[MERGE]` badge even without a BTC parent node installed. Now only shows merge-mining badges when the counterpart chain (parent or auxiliary) is actually enabled. Also applies to `[PARENT]` badge: BTC won't show `[PARENT]` if no aux chains are installed

**Coin Add/Remove (Service Lifecycle)**
- **`stop_node` disable-after-stop race** -- `stop_node()` ran `systemctl stop` then `systemctl disable`. If the dashboard's subprocess timeout (120s) killed pool-mode.sh between these steps, the service was never disabled and `Restart=always` restarted it after systemd's `TimeoutStopSec` killed the process. Reordered to disable-before-stop so the service cannot auto-restart even if the script is killed
- **`remove_coin` double-check has same race** -- `remove_coin()` had an identical stop-before-disable pattern in its double-check block. Reordered to disable-before-stop
- **Dashboard remove timeout too short** -- 120s timeout vs service `TimeoutStopSec=600`. Stopping a syncing daemon can take minutes. The subprocess was killed but the daemon kept running. Increased to 660s with descriptive timeout error message
- **Dashboard install timeout too short** -- 300s timeout for node installation. Installing binaries on slow connections could exceed this. Increased to 600s
- **No health check after add_coin** -- adding a new coin (FBTC) could OOM-kill existing daemons (qbitxd). Systemd `StartLimitBurst=5` prevents auto-restart after 5 failures in 10 minutes, leaving the daemon permanently down. Added post-install health check that verifies all existing coin daemons are still running, runs `reset-failed` and restarts any that crashed

**Global Prune Flag**
- **Newly added coins don't inherit pruning** -- coins added via dashboard or `pool-mode.sh --add` always got `prune=0` regardless of the existing installation's prune setting. Added global `PRUNE_ENABLED` flag to `coins.env`, read by `get_existing_prune()` in `pool-mode.sh` and by the "add coins" upgrade path in `install.sh`. Prune prompt skipped in "add coins" mode to prevent overwriting the inherited setting
- **Dashboard-added coins have no wallet** -- `generate_wallet` endpoint tried `getnewaddress` on named wallet `pool-<coin>` which doesn't exist because `createwallet` only runs in `install.sh`. Added `createwallet` → `loadwallet` → retry chain to the endpoint, covering both modern and old daemon APIs

**Payment Processor & Block Stats**
- **`GetBlockStats` silently drops "submitting" blocks** -- the switch statement in `GetBlockStats()` only counted `pending`, `confirmed`, `orphaned`, and `paid` blocks. Blocks stuck in `submitting` state (crash-safe initial marker from `InsertBlockForPool`) were invisible to stats, operator dashboards, and sentinel monitoring. Added `Submitting` field to `BlockStats` struct and `submittingBlocks` to processor `Stats` JSON response
- **`_update_coins_env_multiport` called outside config lock** -- all three multiport config functions (`update_multiport`, `_disable_multiport_if_enabled`, `_cleanup_multiport_after_remove`) released `_config_file_lock` before updating `coins.env`. A concurrent request could modify `config.yaml` between the lock release and the `coins.env` write, causing `config.yaml` and `coins.env` to go out of sync (e.g., multiport enabled in one but disabled in the other). Moved all `_update_coins_env_multiport` calls inside the lock scope
- **WAL recovery alert fires immediately instead of waiting** -- `checkWALRecoveryStuck` fired a CRITICAL alert on first observation of WAL recovery running, creating false alarms during normal recovery (which typically completes in seconds). Added duration tracking: records when recovery was first observed and only alerts after 5 continuous minutes, clearing the tracker when recovery stops
- **`recoverWALAfterPromotion` reads `roleCtx` without lock** -- `roleCtx` is protected by `roleMu` and can be reassigned by `OnHARoleChange` concurrently (lines 3171, 3204). The WAL recovery function accessed it directly at two call sites without locking, creating a data race that could use a cancelled context or panic. Now snapshots `roleCtx` under lock before use, matching the pattern at lines 1227, 1365, 2919
- **Multi-port weight validation allows individual weights >100** -- `Validate()` checked `weight < 0` but not `weight > 100`. A single coin with weight 200 would pass validation but produce nonsensical scheduling. Added upper bound check (0-100 range per coin)

**Stratum Server (TCP Write Safety)**
- **Concurrent TCP write corruption** -- `keepaliveLoop`, `sendJob`, `SendDifficulty`, `BroadcastJob`, and `BroadcastReconnect` all wrote to `session.Conn` from different goroutines without synchronization. On multi-core systems, interleaved writes corrupt JSON-RPC messages, causing miners to receive garbage and disconnect. Added `WriteMu` mutex to `protocol.Session` and wrapped all `Conn.Write` + `SetWriteDeadline` pairs in lock/unlock
- **`SendDifficulty` truncates fractional difficulty** -- `%g` formatting in `mining.set_difficulty` params dropped trailing zeros (e.g., `1.0` → `1`). Some ASIC firmware parsed the integer `1` differently from `1.000000`. Changed to `%f` for consistent decimal representation

**Merge Mining (AuxPoW)**
- **Per-chain AuxPoW merkle branches** -- `BuildAuxMerkleRoot` returned a single flat `[][]byte` branch (only the first aux chain's path). Multi-chain merge mining produced incorrect merkle proofs for all chains except the first, failing AuxPoW validation. Changed return type to `map[int][][]byte` keyed by `ChainIndex`. Updated `AuxBlockData` protocol struct to carry per-chain `MerkleBranch`, `Job.Clone()` to deep-copy branches, and `checkAuxTargets` in share validator to use per-chain branches
- **AuxPoW chain slot calculated only at parse time** -- `ParseAuxBlockResponse` hardcoded `ChainIndex=0` for every aux chain. With multiple aux chains, all chains claimed slot 0 in the merkle tree, producing invalid proofs. `RefreshAuxBlocks` now recalculates `ChainIndex` via `AuxChainSlot(chainID, nonce=0, treeSize)` after all aux blocks are collected

**Hashrate & Rate Limiting**
- **Hashrate windows report inflated rates for long sessions** -- all time windows (1m, 5m, 15m, 1h, 24h) used cumulative difficulty with `min(elapsed, window)` as denominator. For a 24h session, the 1-minute window still used all 24h of difficulty, inflating the 1m hashrate by 1440x. Now scales difficulty proportionally for windows shorter than session duration
- **Rate limiter violations never decay** -- occasional burst violations accumulated indefinitely per IP. Long-running legitimate miners would eventually hit the ban threshold from weeks of normal variance. Added per-cleanup-cycle (60s) violation decay for connected miners

**Network & Discovery**
- **CIDR expansion mutates network base** -- `expandCIDR` incremented the IP in-place via `incrementIP(ip)` where `ip` shared the backing array with `ipNet.IP`. After the first iteration, the `ipNet.Contains()` check used a shifted network base, skipping IPs or producing incorrect ranges. Now copies the masked IP before iteration
- **Explorer address regex rejects BCH CashAddr** -- `validAddress` regex `^[a-zA-Z0-9]{25,62}$` rejected BCH CashAddr format (`bitcoincash:qp...`) due to the colon character, and also rejected long bech32m addresses (up to 63 chars). Updated regex to allow colon and max length 65

**API**
- **Block history hides orphaned blocks** -- `handlePoolBlocks` called `GetBlocks` which excluded orphaned blocks from the API response. Operators couldn't see orphaned blocks in the dashboard block history. Changed to `GetBlocksWithOrphans`. Also fixed unconditional `"source": "stratum"` field that overwrote the actual worker source
- **Miner/worker stats returns 500 for unknown miners** -- `handleMinerStats` and `handleWorkerStats` didn't check for nil return from database query. Non-existent miners returned 500 Internal Server Error instead of 404. Added nil check with 404 response

**HA Cluster**
- **HA election promotes wrong node when late joiner arrives** -- after checking remote node sync status (which releases the lock for HTTP calls), a higher-priority node could join the cluster unnoticed. The election would promote the lower-priority node. Added post-HTTP re-check of `vm.nodes` priorities before finalizing
- **HA role callbacks fire out-of-order** -- `becomeMasterLocked()` fired `onRoleChange` and `onDatabaseFailover` as goroutines before launching the async `acquireVIP` goroutine. If VIP acquisition failed quickly, reverse callbacks could arrive before forward callbacks, leaving the coordinator stuck in MASTER state. Moved all callbacks inside the `acquireVIP` goroutine: forward on success (sequentially, before broadcast), reverse on failure
- **HA rate limiter token math truncates** -- `int(elapsed.Seconds()) * r.refillRate` cast to int before multiplication. With sub-second elapsed times, `int(0.5)` = 0 tokens regardless of refill rate. Changed to `int(elapsed.Seconds() * float64(r.refillRate))`

**Data Races & Concurrency**
- **`nodemanager.Stats()` returns dangling pointer** -- `LastFailover` pointed directly into the live `failoverHistory` slice. After the lock was released, `performFailover()` could re-slice the history, invalidating the pointer. Now copies the element by value
- **`processCycle` reads `cycleCount` outside lock** -- `cycleCount` is incremented under `p.mu` but the modulo check for deep reorg detection read it after unlock. A concurrent cycle could observe a stale value. Now captures the check condition under the lock
- **Stale session cleanup double-decrements counter** -- `cleanupStaleSessions` deleted from `sessionStates` sync.Map and decremented `sessionStateCount`. If a disconnect handler ran concurrently for the same session, both would decrement, driving the counter negative. Now uses `LoadAndDelete` to ensure only one path decrements

**HA Role Watcher**
- **Stratum restart falsely demotes sentinel+dash** -- when stratum is killed/restarted (e.g., for config changes), the VIP election takes ~90s. During that window, `get_cluster_role()` returns BACKUP. The 3-check debounce (15s) fires a false demotion, stopping sentinel and dashboard. Added 120-second VIP election grace period after API recovery from UNAVAILABLE state. Grace period ends early if MASTER is confirmed. Prevents stratum maintenance from cascading into sentinel/dashboard outage

**Docker**
- **Removed stale `docker/config/config.yaml.template`** -- dead V1 config template that predated multi-coin support. Referenced hardcoded ports and single-coin settings that no longer matched the runtime config generator
- **Pepecoin Dockerfile exposes unsupported ZMQ port** -- `Dockerfile.pepecoin` exposed ZMQ port 28873 but the PepeCoin binary has no ZMQ support. Removed the misleading EXPOSE directive and documented the limitation
- **Docker Compose missing stratum tuning vars** -- added pass-through environment variables for `STRATUM_DIFF_INITIAL`, `STRATUM_DIFF_MIN`, `STRATUM_DIFF_MAX`, `STRATUM_VARDIFF_TARGET_TIME`, `STRATUM_VERSION_ROLLING`, and `STRATUM_VERSION_ROLLING_MASK`

**Windows Installer**
- **CSPRNG password generation has modulo bias** -- `$chars[$_ % $chars.Length]` with 256 byte values mod 62 chars gives indices 0-7 a ~1.6% higher probability than indices 8-61. Added rejection sampling: bytes ≥248 are discarded, ensuring uniform distribution. Also added `$rng.Dispose()` to release the CSPRNG handle
- **`Configure-Firewall` and `Configure-WSL2Networking` use unapproved verb** -- PowerShell analyzer warnings for non-standard verb "Configure". Renamed to `Set-Firewall` and `Set-WSL2Networking` with `[CmdletBinding(SupportsShouldProcess)]`
- **Docker download uses deprecated `WebClient`** -- `System.Net.WebClient.DownloadFile()` lacks modern TLS negotiation. Replaced with `Invoke-WebRequest -UseBasicParsing`
- **Here-string port forwarding script breaks on special chars** -- `$updateScript` used PowerShell here-strings with embedded variables that broke on special characters. Replaced with explicit string array joined by CRLF
- **Legal acceptance comparison fragile** -- required exact case match and failed with trailing whitespace. Changed to trimmed case-insensitive comparison
- **Unattended mode blocks on RAM check and port conflicts** -- interactive prompts fired even in `-Unattended` mode. Now continues with warning (RAM) or aborts cleanly (ports)
- **Port conflict check returns all connections** -- `Get-NetTCPConnection` could return hundreds of matches. Added `Select-Object -First 1` since only one is needed
- **Unused variable assignments cause PSScriptAnalyzer warnings** -- replaced unused return captures with `$null =`
- **Firewall manifest lookup uses unreliable `$MyInvocation.ScriptName`** -- changed to `$PSScriptRoot` for consistent script directory resolution
- **Duplicate `$Script$Script:Version` typo** -- double-prefix in variable assignment. Fixed to `$Script:Version`

**Dashboard (Python)**
- **RPC error not checked in `digibyte_rpc`** -- `result.get("error")` was never inspected. RPC errors (e.g., method not found) returned the error object as if it were a valid result. Now checks and logs RPC errors, returning `None`

**Multi Coin Smart Port (Stratum)**
- **Smart Port miners never receive initial job** -- `handleConnect` sent the first job before the miner had subscribed or authorized. Firmware silently ignored the premature `mining.notify`. `handleMinerClassified` only sent a job on coin *change*, not on first classification. Miners sat idle indefinitely. Moved initial job delivery to `handleMinerClassified` so it always fires after authorize, and sends the assigned coin's job regardless of whether a coin change occurred

**Multi Coin Smart Port (Dashboard)**
- **Settings page "Error loading"** -- `hasCustomStarts` variable referenced but never declared in `renderMultiPortSchedule`, throwing a `ReferenceError` that propagated up through `renderMultiPortCoins` → `updateMultiPortTotal` → into `loadMultiPort`'s catch block, displaying "Error loading" on the entire settings page. Added `const hasCustomStarts = Object.keys(starts).length > 0` declaration
- **Schedule shows 24h/100% for every coin** -- when two coins shared the same `start_hour`, the start-to-next-start duration formula computed `0`, which wrapped to 24h for each coin. Rewrote schedule builder in all three locations (GET `/api/multiport` endpoint, POST `/api/multiport` enforcement, settings page JS preview) to use anchor + weight-based sequencing: only the first coin's `start_hour` matters, all coins are sequenced contiguously from there using weights for duration. Eliminates gaps, collisions, and same-start-time bugs
- **Floating point noise in schedule hours** -- `weight / 100 * 24` produced IEEE 754 artifacts (e.g., `8.399999999999999h` instead of `8.4h`). Added `Math.round(x * 10) / 10` at the source (input population) and in the slot builder
- **Start time inputs show stale config values** -- after schedule recomputation, the "at" time inputs still displayed old `start_hour` values from config instead of the computed contiguous times. Added sync step in `renderMultiPortSchedule` that updates all start inputs to match the displayed schedule windows
- **Coin input order doesn't match schedule** -- inputs sorted alphabetically (BC2 before QBX) but schedule sorted chronologically (QBX at 00:10 before BC2 at 08:34). Changed input list to sort by `start_hour` then alphabetically, matching the schedule display
- **Dashboard "Waiting" status on Smart Port panel** -- stratum API `MultiPortStats` has no `active_coin` field. Dashboard always showed "Waiting" even while mining. Now derives `active_coin` from `coin_distribution` (coin with most miners) in the GET endpoint
- **Per-coin network hashrate wrong** -- dashboard estimated network hashrate as `difficulty * 2^32 / 600` using Bitcoin's block time for all coins. QBX (and others) have different block times, producing wildly incorrect values. Now uses actual `networkHashrate` from stratum API per-coin data

### Changed

- **Version bump** -- all version strings, documentation, templates, themes, dashboard HTML, and config files updated to 2.2.0
- **`--wallet` optional for `--add`** -- coin install no longer requires a wallet address upfront. Address can be set later via dashboard wallet generation or manual config edit
- **Dashboard theme versions** -- all 22 theme JSON files bumped from 2.0.0 to 2.2.0
- **Dashboard HTML version** -- footer and JS config updated from 2.0.1 to 2.2.0
- **Per-coin chart history** -- selecting a specific coin in the stats dropdown now shows that coin's network hashrate, difficulty, and other metrics in the charts instead of aggregated data. History is tracked per-coin across all polls so switching coins doesn't wipe chart data

### Documentation

- **Comprehensive documentation audit** -- 40+ inconsistencies fixed across 15 doc files by auditing every claim against actual codebase
- **COIN_ONBOARDING_SPEC.md** -- fixed Go code templates: `baseCoin` struct doesn't exist (use empty struct), `AlgoSHA256d`/`AlgoScrypt` constants don't exist (return plain strings), `ChainID()` returns `int32` not `uint32`, method is `AuxPowVersionBit()` not `VersionBit()`, method is `GenesisBlockHash()` not `GenesisHash()`
- **REFERENCE.md** -- SHA-256d Unknown class MinDiff corrected from 500 to 100, MaxDiff from 50,000 to 1,000,000. Added note that `celebration.duration_hours` defaults to 2 when omitted
- **MULTI_COIN_PORT.md** -- sentinel alert names corrected: `multi_port_difficulty_spike` and `multi_port_coin_switch`
- **DASHBOARD.md** -- `refresh_interval` description corrected from "Miner poll interval" to "Dashboard refresh interval"
- **INDEX.md** -- version corrected to v2.2.0, Docker guide description updated to include V2, theme count corrected from 19 to 25
- **README.md** -- upgrade guide reference corrected to v2.2.0
- **UPGRADE_GUIDE.md** -- all stale v2.0.0 references updated to v2.2.0
- **Storage sizes normalized** -- BCH, LTC, SYS, DOGE, DGB, FBTC, NMC, PEP, CAT sizes aligned across OPERATIONS.md, CLOUD_OPERATIONS.md, and DOCKER_GUIDE.md using install-windows.ps1 as source of truth
- **CLOUD_OPERATIONS.md** -- admin API key path corrected from sentinel config to pool config.yaml
- **DOCKER_GUIDE.md** -- removed contradictory "no sudo needed" claim
- **WINDOWS_GUIDE.md** -- added missing SYS row to coin table
- **ARCHITECTURE.md** -- regex pattern count corrected to 48, coordinator.go line reference corrected
- **SECURITY_MODEL.md** -- 5 source line references corrected, removed nonexistent "30s fallback" claim
- **SENTINEL.md** -- line count corrected from ~19,500 to ~20,700
- **spiralctl-reference.md** -- merge-mining pairs corrected from 10 to 6, removed nonexistent DGB-as-parent pairs

---

## [2.1.0]  -  2026-03-30  -  Phi Hash Reactor

> *Multi coin smart port online. All ports nominal.*

### Added

- **Multi coin smart port** -- Single stratum port (16180) mines multiple SHA-256d coins on a 24-hour weighted time schedule with automatic rotation, per-session tracking, and daemon failover. See [MULTI_COIN_PORT.md](docs/reference/MULTI_COIN_PORT.md)
- **Non-interactive pool-mode.sh** -- `--yes`, `--wallet`, `--delete-data`, `--no-install-node` flags enable fully automated coin add/remove from the dashboard UI
- **Timezone-aware scheduling** -- Multi-coin schedule uses the operator's configured timezone instead of UTC

### Fixed

**Block Recording & Display**
- **Block finder attribution lost** -- `postgres_v2.go` `InsertBlockForPool` hardcoded `"stratum"` as the source column instead of using `block.Source`, permanently discarding the actual worker name that found the block. All future blocks now record the real worker suffix
- **Dashboard block finder showing "stratum"** -- field priority was `source > worker > miner`; changed to `worker > miner > source` so the actual worker name displays correctly
- **Werkzeug `RuntimeError` crash** -- production mode SocketIO was missing `allow_unsafe_werkzeug=True`, causing crashes on startup

**Multi Coin Smart Port (Scheduling)**
- **Late-started pools excluded from multi-port** -- when a coin pool failed initial startup and recovered via retry loop, it was never registered with the MultiServer or DifficultyMonitor. Multi-port miners were silently never routed to the recovered coin even though it was fully operational on its dedicated port
- **Miners assigned to down preferCoin** -- `handleConnect` checked map membership for `preferCoin` but not `IsRunning()`, so a miner connecting when preferCoin's pool was registered but stopped would sit idle until the next evaluation cycle. Now falls through to the first running coin
- **Non-deterministic coin schedule** -- Go map iteration order made the time-slot schedule unpredictable across restarts. Coin weights are now sorted deterministically
- **Selector failover to unmonitored coins** -- the fallback path could select coins that were registered but had no availability tracking from the DifficultyMonitor
- **DST-unsafe day fraction calculation** -- hardcoded 86400s caused 23h/25h DST-transition days to mis-align the coin schedule. Now computes actual day length from timezone-aware start-of-day and start-of-next-day
- **Monitor double-close panic** -- if `Monitor.Stop()` ran before `MultiServer.difficultyEventLoop` deferred `Unsubscribe`, the subscriber channel was closed twice, panicking. `Unsubscribe` is now a no-op if the channel was already removed by `Stop()`
- **Zero difficulty not marking coin unavailable** -- when an RPC returned zero/negative difficulty (syncing daemon), the coin was not marked unavailable, so the selector could route miners to a non-functional coin
- **Selector switchHistory memory leak** -- `s[1:]` reslice retained the old backing array indefinitely. Now copies to a new slice to release the old backing array
- **HandleMultiPortShare submitting rejected shares as blocks** -- block submission ran on all shares regardless of acceptance status, wasting RPC calls on stale/low-diff shares and polluting metrics. Now only processes blocks from accepted shares, consistent with regular share handler

**Health Monitor & Services**
- **BCH restart loop (BCHN RPC whitelist)** -- health monitor's RPC error whitelist only matched `error code: -28` but Bitcoin Cash Node returns different negative JSON-RPC codes during startup. Changed to regex `error code: -[0-9]+` and added `Activating best chain` to the whitelist
- **`restart_service()` silent failures** -- `systemctl start` exit code was not checked; systemd `start-limit-hit` rate limiting was not detected. Added exit code checking, pre-start rate limit detection with `reset-failed`, and post-failure diagnostics
- **qbitxd missing from daemon loops** -- Q-BitX was not included in 5 daemon detection/monitoring loops in `install.sh`, causing it to be skipped for health monitoring, status display, journal collection, and self-test
- **Bitcoin II missing PIDFile** -- `bitcoiniid.service` template lacked `-pid=` flag in ExecStart and `PIDFile=` directive, preventing systemd from properly tracking the daemon process
- **BTC/XMY/BC2 `/tmp` glob vulnerability** -- `ls -d bitcoin-*/` in `/tmp` could match attacker-created directories. BTC now derives directory from known version; XMY and BC2 use `tar -tzf` to extract the actual directory name from the tarball
- **Daemon configs owned by root after upgrade** -- `cleanup_daemon_configs()` in `upgrade.sh` used `awk`/`sed` rewrite patterns that created new files owned by root. Daemon processes running as `spiraluser` could fail to read their configs. Added ownership/permission restoration
- **`admin_api_key` migration corrupts config on `/` or `\` in key** -- `fix_config_issues()` and `migrate_v2_config()` in `upgrade.sh` used `sed s///` with the API key as replacement text. Keys containing `/`, `\`, or `&` broke the sed delimiter or triggered backreference expansion, silently corrupting `config.yaml`. Added sanitization and replaced the `sed 1s` prepend with a safe heredoc+cat approach
- **qbitxd missing from sudoers** -- `spiraluser` could not start/stop qbitxd via `sudo systemctl` because the sudoers entries were never created. Added start/stop entries
- **qbitxd missing from uninstall** -- `cleanup_and_exit` did not disable, stop, or remove the qbitxd service file in the disable block, service file removal block, or final cleanup block. A failed install left qbitxd running and its service file orphaned
- **UFW rule missing for multi coin smart port** -- port 16180 (multi-coin stratum) was never opened in UFW during install. External miners could not connect. Added conditional `ufw allow 16180/tcp` when `MULTIPORT_ENABLED=true`
- **`restart_service()` flapping not detected** -- successful restart reset `restart_counts` to 0, so a service that crash-looped (starts OK, dies 30s later) never reached `MAX_RESTART_ATTEMPTS`. Count now increments on every restart; the hourly reset clears it for genuinely recovered services
- **Stratum TLS port not opened in UFW (single-coin mode)** -- single-coin setup opened V1 and V2 stratum ports but never the TLS port. TLS miners were silently blocked by the firewall
- **Connlimit rules missing for 5 coins** -- iptables connection-limit rules (max 200/IP) only covered 9 of 14 coins. DGB-Scrypt, PEP, CAT, FBTC, QBX, and the multi-coin port had zero connection-exhaustion protection
- **`reset-failed` sudoers wildcard** -- `systemctl reset-failed *` allowed the pool user to reset failure state on ANY system service, masking crash-loop abuse. Restricted to explicit pool service names
- **`journalctl` sudoers wildcard** -- `journalctl *` allowed the pool user to read logs from ANY service (sshd, kernel, etc.). Restricted to `-u <service>` for pool-related services only
- **`pool-mode.sh` owned by spiraluser — privilege escalation** -- script was `chown spiraluser` but executed as root via sudoers `systemd-run`. spiraluser could replace its contents with arbitrary root commands. Changed to `chown root:root` in both `install.sh` and `upgrade.sh`
- **`coins.env` world-readable with RPC passwords** -- created with default 644 permissions exposing all coin RPC credentials to any local user. Added `chmod 600`
- **HA sudoers file has no `visudo -c` validation** -- unlike the dashboard sudoers, the HA sudoers file was never syntax-checked. A malformed sudoers include can break ALL sudo on the system. Added validation with auto-removal on failure

**Docker**
- **Config overwritten on container restart** -- all 13 coin Dockerfiles and the Patroni entrypoint ran `envsubst` unconditionally, overwriting user-provided or manually-edited config files on every restart. Now checks if config exists and is non-empty before generating from template
- **Stratum entrypoint overwriting config.yaml** -- multi-coin mode in `stratum-entrypoint.sh` did not check for existing config before auto-generating
- **Single-coin entrypoint overwriting config.yaml** -- same config overwrite issue in single-coin mode path of `stratum-entrypoint.sh`
- **Fractal Bitcoin wrong datadir** -- Docker entrypoint was missing explicit `-datadir=/home/fractal/.fractal`; the daemon (a Bitcoin Core fork) defaulted to `~/.bitcoin`, causing data/config path mismatch with the Docker volume mount
- **Missing HA env vars in .env.example** -- `REPLICATION_PASSWORD`, `REWIND_PASSWORD`, and `PATRONI_REST_PASSWORD` were required by `docker-compose.ha.yml` but not documented in the example config
- **Coin config files world-readable in Docker** -- all 13 coin Dockerfile entrypoints created config files (containing RPC passwords) with default 644 permissions. Added `chmod 600` after `envsubst` in every coin entrypoint
- **Dockerfile.pepecoin wrong GitHub organization** -- download URL used `pep-official` which doesn't exist; the correct org is `pepecoinppc`. Docker builds for Pepecoin always failed
- **Dockerfile.qbitx wrong download URL and binary name** -- used v0.1.0 filename pattern (`qbitx-linux-x86.zip`) and binary name (`qbitxd`) but ARG specified v0.2.0 which uses different naming. Docker builds for Q-BitX always failed
- **QBX install hardcoded v0.2.0 filename** -- four references in `install_qbx()` hardcoded `qbitx-linux-x86_64-v0.2.0.zip` instead of using `$QBX_VERSION` variable. Changing the version constant would break download/extract/cleanup
- **Patroni healthcheck `start_period` too short** -- 30s start period in `Dockerfile.patroni` was insufficient for fresh cluster bootstrap (initdb + WAL setup can take 60-120s), causing containers to be marked unhealthy prematurely. Increased to 120s
- **HAProxy healthcheck uses missing `wget`** -- `haproxy:2.9-alpine` does not include `wget`, so the health check always failed. Replaced with `haproxy -c` config validation + PID check
- **`DB_PORT` not passed to stratum container** -- `docker-compose.yml` environment block omitted `DB_PORT`, so user-configured non-standard database ports in `.env` were silently ignored by the stratum container

**Sentinel**
- **`_atomic_json_save` forward reference** -- function was defined at line 5390 but first called at line 527; worked due to Python late binding but fragile. Moved definition before first use
- **`port_config` type error** -- V2 API returning integer ports instead of dicts caused `isinstance(port_config, dict)` to fail. Added `isinstance(port_config, int)` check first
- **`pool_api_url` hostname validation** -- Docker service names (e.g., "stratum") and `.local`/`.internal`/`.lan`/`.home` suffixes were rejected by the hostname validator. Now allows dotless hostnames and local DNS suffixes
- **Difficulty threshold off-by-one** -- comparison used `<` instead of `<=`, causing threshold-exact values to be missed
- **`send_telegram` crash on auto-update** -- called with a raw string instead of an embed dict, causing `AttributeError: 'str' has no attribute 'get'` when the auto-update notification tried to send
- **`send_notifications` 10s blocking sleep** -- retry on all-channels-failed slept 10 seconds inline, stalling the entire monitoring loop. Removed the sleep; individual send functions already have their own retry/backoff
- **`send_notifications` redundant `load_config()` calls** -- two separate `load_config()` disk reads in the retry/fallback paths within the same function call. Consolidated to a single read
- **`_dashboard_url()` crash on malformed hostname** -- `parsed.hostname` returning `None` for malformed URLs caused `TypeError` on string concatenation. Now falls back to `"localhost"`
- **`flush_alert_batch` infinite retry loop** -- failed batched alerts were re-queued with type `"retry"` on every flush cycle, causing permanent re-queuing when notifications were broken. Added retry counter so each alert is retried at most once
- **`chronic_issues` memory leak** -- per-miner `chronic_issues` dict was not pruned by `prune_stale_miner_state()`, growing unboundedly as miners were removed. Added to the pruning list

**spiralctl**
- **`preferCoin` tie-breaker crash** -- empty string comparison `strings.ToUpper(coin) < preferCoin` where `preferCoin=""` always evaluated false. Fixed both locations to handle empty initial state
- **Resource leak in coordinator shutdown** -- `multiServer.Stop()` and `diffMonitor.Stop()` were not called during graceful shutdown, leaking goroutines and connections
- **Tor disable leaves stale `listen=0`** -- `removeTorSettings` in `tor.go` removed proxy/onion settings but not `listen=0` and `onlynet=ipv4`, leaving the node unable to accept inbound connections after disabling Tor
- **`pool stats` response body leak** -- `defer resp.Body.Close()` on a reassigned `resp` variable caused the first two HTTP response bodies to leak. Changed to inline `resp.Body.Close()` after each decode
- **`saveConfig()` destroys unknown YAML sections** -- `yaml.Marshal(cfg)` on a partial Go struct silently dropped all config sections not modeled by the struct (`stratum`, `logging`, `rateLimiting`, `api`, `metrics`, etc.). Every `spiralctl` write operation destroyed production configuration. Changed to round-trip-safe approach: read existing file into generic map, merge only managed fields, write back
- **`saveExtendedConfig()` same destructive pattern** -- identical to above but in the mining.go `ExtendedConfig` path. Same fix applied
- **`testDBConnection` hangs indefinitely** -- `psql` connection test had no timeout; unreachable hosts would block the CLI forever. Added 15-second `context.WithTimeout`

**Coordinator / Pool Core**
- **Sentinel reads `paymentProcessors` without lock** -- `checkPaymentProcessors()` and `checkOrphanRate()` iterated the coordinator's `paymentProcessors` map without acquiring `paymentProcessorMu.RLock()`. Concurrent map read/write during coin retry panics Go with a fatal runtime crash. Added RLock around both iterations
- **Multi-port server missing TLS config** -- the `StratumConfig` built for the multi-port server copied only 5 of 9 fields from the first enabled coin, silently dropping TLS cert/key paths. Multi-port miners could not use encrypted stratum even when TLS was configured
- **Late-started pools on master stuck in `RoleUnknown`** -- when a pool recovered via retry on the HA master node, the code only set `RoleBackup` (when `!IsMaster()`) but had no `else` branch for the master case. The pool's HA role stayed `RoleUnknown` until the next VIP election, potentially blocking block submissions
- **`HandleMultiPortShare` drops aux block rewards** -- multi-port share handler had no `handleAuxBlocks` call, silently discarding merge-mined aux chain blocks. Miners routed through the Multi coin smart port could find aux blocks that were never submitted, recorded, or paid. Direct revenue loss
- **`HandleMultiPortShare` missing Prometheus metrics** -- multi-port shares were invisible to Prometheus. Share acceptance rates, best share difficulty, and total counts were undercounted proportional to multi-port traffic volume. Dashboard, effort calculations, and Sentinel hashrate alerts all showed incorrect values
- **`HandleMultiPortShare` credits silent duplicate shares** -- `SilentDuplicate` shares (accepted to prevent miner retry floods but not meant to be credited) were submitted to the share pipeline and persisted to the database. Multi-port miners received double credit for duplicate shares, inflating their payout share relative to non-multi-port miners
- **`CoinPool.Stop()` never cancels `roleCancel`** -- the HA role context was not cancelled during shutdown. In-flight block submission goroutines using `roleCtx` continued running until their individual deadlines expired, unnecessarily extending shutdown by up to 60 seconds
- **`verifyBlockAcceptance` retry timing defeats propagation wait** -- retry intervals (5s/10s/15s) were used as RPC timeouts, not propagation wait times. If the daemon responded instantly with "not found", all 3 attempts fired in ~2s instead of the intended ~30s window, causing blocks near propagation timing to be falsely marked as orphaned
- **`haRoleHistory` slice backing array never shrinks** -- subslice trim `s.haRoleHistory[trimIdx:]` retained the full backing array. Under sustained HA flapping, memory grew monotonically. Now copies to a fresh slice

**Dashboard / Pool Mode**
- **QBX stratum config uses P2P port instead of RPC port** -- `generate_coin_config` in `pool-mode.sh` set the QBX node RPC port to 8345 (P2P) instead of 8344 (RPC), and ZMQ endpoint to 28345 instead of 28344. QBX mining was completely non-functional — stratum could not connect to the node or receive block notifications
- **`install_node` missing wallet validation** -- the coin install API endpoint accepted any string as a wallet address without calling `validate_wallet_address()`. Invalid addresses flowed through to pool config unchecked
- **RPC credential mismatch in `add_coin`** -- `pool-mode.sh` called `setup_node` (which generates random RPC credentials) after `generate_config` (which also generates credentials), overwriting the password already written to `config.yaml`. Stratum could not authenticate to the daemon. Removed the duplicate `setup_node` call
- **Config files created world-readable** -- `generate_config` in `pool-mode.sh` created `config.yaml` with default 0644 permissions, exposing RPC and database passwords. Added `chmod 600` after `chown`
- **DGB-SCRYPT `remove_coin` crashes on empty service name** -- `systemctl stop/disable/reset-failed` were called without checking if the service variable was non-empty, causing errors on partial installations. Added `-n "$service"` guards
- **Concurrent coin install/remove race condition** -- two simultaneous dashboard API requests (e.g., install DGB + remove BTC) could run `pool-mode.sh` concurrently, corrupting shared config files and systemd state. Added `_node_operation_lock` serialization with HTTP 409 response for concurrent requests
- **`axeos_api_call` missing SSRF validation** -- the AxeOS/NerdQAxe++ API helper accepted arbitrary IPs without `validate_miner_ip()` check. Callers validated individually but the helper itself was unprotected as defense-in-depth
- **CGMiner API port not validated** -- user-supplied `port` parameter passed directly to `socket.connect()` without range check, enabling internal port scanning via the miner management interface. Added 1-65535 range validation
- **Password change silently no-ops with env var** -- when `DASHBOARD_ADMIN_PASSWORD` env var was set, `change_password` verified against it but saved the hash to `auth.json`, which is never checked when the env var is active. User saw "success" but nothing changed. Now returns clear error explaining env var management
- **Non-atomic config write in `update_multiport`** -- `open()` + `pyyaml.dump()` directly to `config.yaml` could corrupt the file on crash mid-write. Changed to tempfile + fsync + `os.replace()` atomic pattern
- **`check_pool_upgrade` exception leaks internals** -- generic `except` handler returned `str(e)` to the client, exposing internal paths and library versions. Now logs server-side and returns generic error
- **`firmware_tracker` unbounded key injection** -- `known_versions` dict accepted arbitrary device_type keys with no size limit. Attacker could POST thousands of entries to grow memory. Added 50-entry cap and key/value length limits
- **WebSocket auth bypass when `AUTH_ENABLED=false`** -- HTTP routes enforce loopback-only bypass (F-03) but the SocketIO `connect` handler allowed all IPs when auth was disabled, exposing real-time pool data to the public internet. Now mirrors the loopback-only check
- **`add_discovered_devices` returns secrets** -- endpoint returned the full config dict including `pool_admin_api_key`, `metrics_auth_token`, and device passwords. Now strips secrets before returning, matching the `/api/config` GET endpoint
- **`cgminer_command_v2` socket leak** -- socket was not closed in `finally` block; exceptions between `socket()` and `close()` leaked file descriptors. Added `finally` cleanup matching the pattern in `cgminer_command()`
- **`verifyBlockAcceptance` compile error** -- V2 CoinPool referenced undefined `retryIntervals` instead of `retryWaits`, preventing block acceptance verification from executing. Valid blocks with ambiguous daemon responses were falsely orphaned (money loss)
- **`HandleMultiPortShare` inverted block priority** -- pipeline DB write happened before block submission, violating the "block first" rule. Added milliseconds of latency to block submissions in multi-port mode, increasing orphan risk (money loss)
- **Export endpoints use wrong coin price** -- `export_blocks()` and `export_earnings()` used the primary coin's price for ALL coins. A BTC block valued at DGB price would show $0.03 instead of $187,500. Added per-coin CoinGecko price lookup
- **Scrypt network hashrate formula** -- `_compute_network_hashrate()` fallback always used `2^32` (SHA-256d). Scrypt coins require `2^16`, causing 65,536x overestimation when RPC `getnetworkhashps` is unavailable
- **`fetch_block_reward()` pool mismatch** -- Method 1 blindly took `pools[0]` from API regardless of which pool is primary. In multi-coin mode, the wrong coin's block reward was displayed. Now matches by pool ID
- **QBX wallet validation accepts Bitcoin addresses** -- pool-mode.sh accepted `1...`, `3...`, `bc1q...` (Bitcoin formats) for QBX. QBX uses `M` (P2PKH), `P` (P2SH), and `pq` (post-quantum) prefixes. Bitcoin addresses on QBX network = permanently lost funds
- **Non-interactive `--wallet` skips all validation** -- `get_wallet_address()` returned the address with zero format checks in non-interactive mode. Invalid or wrong-network addresses passed through silently. Added per-coin prefix validation
- **Multi-coin `--wallet` applies same address to all coins** -- `switch_to_multi()` with a single `--wallet` flag set the same address for all coins. Coins with incompatible address formats (e.g., DGB + BTC) would lose all block rewards for mismatched coins. Added early cross-coin validation
- **WAL `cleanupArchives()` unsorted deletion** -- `filepath.Glob` does not guarantee sort order. Without sorting, the newest archives could be deleted instead of the oldest, destroying the most recent committed share data needed for crash recovery
- **V1 Pipeline missing WAL** -- `NewPipeline()` (used by V1 Pool) never set `walPath` or `poolID`, silently disabling WAL crash recovery. On crash, up to 1M in-flight shares were permanently lost. Now passes pool ID to enable WAL
- **`sendBatch()` silent share loss** -- when `batchChan` is full, shares were dropped with only a warning log. Added explicit CRITICAL-level logging when WAL is disabled (no recovery possible) vs informational when WAL will recover
- **System health missing coin daemon services** -- `/api/system/health` looked for `coins_config` key (from detect_mode API) in the `get_enabled_coins()` dict (which uses `enabled` key). Coin daemon service status was never included in health checks
- **`per_miner_hashrate` unbounded dict growth** -- historical hashrate dict never pruned keys for removed miners. Over weeks of miner churn, each stale entry retains a 10,080-entry deque. Now prunes stale miners on each recording cycle
- **HA `announce_to_cluster` SSH pubkey injection** -- `$local_pubkey` interpolated unquoted into remote SSH command string. Used single-quoted remote command with stdin pipe to prevent shell metacharacter expansion
- **HA `sync_ha_cluster` empty service variable** -- when `$service` was empty (unknown coin), `systemctl is-active --quiet` with no args returned exit-code 0, causing unrelated services to be stopped. Added empty-service guard
- **Non-atomic config write in `generate_config`** -- `cat >` truncated the config file before the coin loop completed. Script abort mid-loop left a partial/empty config that crashed the stratum. Now writes to temp file and atomic-moves on success
- **Wallet address shell/YAML injection via `--wallet`** -- addresses were interpolated into shell-expanded heredocs (`<< EOF`). A crafted address like `$(cmd)` would execute. Added character-class sanitization stripping all non-alphanumeric/colon characters

**Peer Discovery & Network Bootstrap**
- **`forcednsseed=1` stripped on every upgrade** -- `cleanup_daemon_configs()` in `upgrade.sh` listed `forcednsseed` in the "invalid options" array and deleted it from all 13 coin configs on every upgrade run. Fresh installs on nodes with flaky DNS seeds (6/8 DGB seeds dead, all 3 FBTC seeds dead) got 0 peers and could not sync. Root cause of .22 HA node having 0 peers after v2.1 install. Removed from invalid list; added `ensure_daemon_peer_config()` to restore it on upgrade
- **Zero hardcoded fallback peers across all coins** -- all 13 coin configs relied entirely on DNS seeds for peer discovery. When DNS seeds are unreachable (firewalled, dead, slow), nodes get 0 peers indefinitely. Added `addnode=` entries with verified live peer IPs to all coins across native install, Docker, and upgrade paths (204 total addnode entries)
- **QBX missing all seed configuration** -- Q-BitX config had zero `seednode=` entries. The only DNS seed (`seed.qbitx.org`, found via `strings` on the binary) was never added to the config. Fresh QBX installs relied entirely on compiled-in fixed seeds
- **FBTC DNS seeds all dead** -- all 3 Fractal Bitcoin DNS seeds (`dnsseed-mainnet.fractalbitcoin.io`, `dnsseed-mainnet.unisat.io`, `dnsseed.fractalbitcoin.io`) return no records. Added `fixedseeds=1` explicitly and 5 `addnode=` peers obtained from a live FBTC daemon's `getpeerinfo`
- **FBTC missing third DNS seed** -- `dnsseed.fractalbitcoin.io` was compiled into the binary (found via `strings`) but not in the config's seednode list. Added as third seednode entry

**Multi-Coin Scheduler**
- **`switchJob := *job` copies sync.RWMutex** -- `multiserver.go` line 369 copied a `protocol.Job` struct by value during coin switches. `Job` embeds `sync.RWMutex` at field `stateMu`; copying a mutex is undefined behavior that can cause deadlocks or data races. Detected by `go vet`. Fixed to use `job.Clone()` which properly initializes a fresh mutex

### Changed

- **Package rename `internal/difficulty` -> `internal/scheduler`** -- the "difficulty switching" concept was removed; the package contains scheduling, monitoring, and routing logic. All imports updated
- **Version bump** -- all version strings, documentation, templates, MOTD, and config files updated to 2.1.0
- **MOTD consolidated** -- reduced from 22 commands to 14, organized into Status/Monitoring, Mining/Coins, and Management sections. Added `mining multiport` command. Updated in both `install.sh` and `upgrade.sh`

---

## [2.0.1]  -  2026-03-29  -  Phi Hash Reactor

### Fixed

**WSL2 / Docker Bug Audit**
- **DNS peer discovery disabled on 11 coins**  -  `dnsseed=hostname` entries in install.sh (DGB, BTC, BC2, LTC, DOGE, PEP, CAT, NMC, SYS, XMY, FBTC) were parsed as `atoi("hostname") = 0` by Bitcoin Core's `GetBoolArg()`, overriding the earlier `dnsseed=1` and silently disabling DNS seeding. Root cause of XMY single-peer issue. Removed all `dnsseed=hostname` lines; DNS seed hostnames are hardcoded in each daemon's `chainparams.cpp` and cannot be configured via conf file
- **Docker stratum-entrypoint.sh `set -e` bypass**  -  `envsubst ... && mv ...` exempts the left side from `set -e`; a failed envsubst would silently continue with a corrupt config. Split into two separate commands
- **Docker patroni-entrypoint.sh password file race**  -  between `envsubst > patroni.yml` and `chmod 600`, the file briefly had default umask permissions (world-readable). Added `umask 077` before the write
- **Windows configure-coin-firewall.ps1 wrong-coin port matching**  -  `Get-CoinConfigFromManifest` regex matched against the entire manifest YAML, returning the first coin's ports regardless of the target symbol. Rewrote to split manifest into per-coin blocks before matching
- **Windows firewall scripts `.Substring()` crash**  -  trailing commas in `-FirewallProfiles` produced empty strings after split, crashing `.Substring(0,1)`. Added `Where-Object { $_ -ne "" }` filter in both `configure-firewall.ps1` and `configure-coin-firewall.ps1`
- **upgrade.sh `--fix-config` / `--update-services` run unconditionally**  -  defaults were `true` despite help text and comments saying "off by default" / "only when explicitly requested". Changed to `false`; these flags now require explicit opt-in as documented
- **upgrade.sh multi-disk backup path ignores quotes**  -  `resolve_coin_dir` regex `\K.+$` captured literal quotes from `CHAIN_MOUNT_POINT="/mnt/data"`, causing the `-d` check to fail and silently falling back to the wrong directory. Fixed regex to `"?\K[^"]+` matching the pattern used everywhere else
- **spiralctl.sh / coin-upgrade.sh multi-disk path ignores unquoted entries**  -  regex `"\K[^"]*` required a leading quote, but install.sh line 35151 writes `CHAIN_MOUNT_POINT=/mnt/data` (unquoted). On multi-disk setups, all `spiralctl` coin commands silently used wrong paths. Fixed regex to `"?\K[^"]+` (matches both forms)
- **spiralctl.sh owned by spiraluser  -  privilege escalation**  -  `spiralctl.sh` was deployed with `chown spiraluser:spiraluser` but is symlinked to `/usr/local/bin/spiralctl` and calls `sudo` internally. spiraluser could modify the script to inject arbitrary root commands. Changed to `chown root:root` in both upgrade.sh and install.sh, consistent with other sudoers-whitelisted scripts
- **upgrade.sh Python code injection via string interpolation**  -  `fix_config_issues()` and `migrate_v2_config()` embedded shell variables directly into Python string literals (`'$sentinel_cfg'`, `'$final_api_key'`). A path or key containing a single quote would crash the Python inline or corrupt the JSON. Changed to pass values via `sys.argv[]`
- **upgrade.sh stale lock not re-acquired**  -  after clearing a dead process's lock file, the script continued without holding any flock. A concurrent `upgrade.sh` (cron + manual) could race on the new inode. Now re-opens fd and re-acquires flock after cleanup
- **Dashboard XSS in upgrade/update management UI**  -  `result.output`, `result.error`, `result.current_version`, `result.latest_version`, and `result.packages[]` were injected into `innerHTML` without `escapeHtml()` in 6 locations. Upgrade script output or error messages containing HTML would execute in the admin's browser. Wrapped all with `escapeHtml()`
- **Dashboard raw exception strings in API responses**  -  three endpoints (reboot, upgrade apply, HTTPS enable) returned `str(e)` to the client, leaking internal paths and library versions. Replaced with generic error messages; real exceptions logged server-side
- **Dashboard `shutil.move` not atomic across filesystems**  -  `_atomic_json_save` used `shutil.move` which falls back to copy-then-delete across filesystem boundaries. Changed to `os.replace` (always atomic)
- **Sentinel webhook 5xx retry hammering**  -  on server errors, the retry loop immediately re-sent without backoff. The `URLError`/timeout path correctly slept `2 * (attempt + 1)` seconds but the 5xx path did not. Added matching backoff
- **Sentinel `_dashboard_url()` breaks with non-default stratum port**  -  `pool_api_url.replace(":4000", ":1618")` only worked when stratum was on port 4000. Custom ports (e.g., `:8080`) were left unchanged, causing all Sentinel → dashboard API calls to silently fail. Now parses URL properly and always sets port 1618
- **add-coin.py generated install script defaults to wrong user**  -  generated native install script set `POOL_USER=spiralpool` instead of `spiraluser`, causing permission mismatches with existing Spiral Pool data directories and wallet files
- **add-coin.py non-deterministic RPC port generation**  -  `hash(symbol)` is randomized per Python session (PYTHONHASHSEED since 3.3). Running add-coin twice for the same symbol produced different ports. Changed to deterministic `hashlib.md5`
- **add-coin.py port allocation can exceed 65535**  -  stratum port search loop had no upper bound, producing invalid ports on systems with many coins. Added bounds check
- **spiralctl external disable zeros rate-limit config**  -  `revertSecurityHardening` wrote zero values for `maxConnPerIP`, `maxSharesPerSec`, and `banThreshold` when originals were never saved (pre-hardening configs), disabling all rate limiting. Now falls back to safe defaults (100/100/10/30m)
- **spiralctl vip rotate-token panics on short tokens**  -  `oldToken[:12]` slice panic when cluster token is shorter than 12 characters (e.g., manually set via `--token`). Added length guard
- **spiralctl vip join allows priority 0**  -  `joinCluster` skipped the minimum-100 priority enforcement that `enableVIP` had, allowing a joining node to silently become highest-priority and win all elections. Now enforces same 100–999 range
- **spiralctl gdpr-delete PromQL regex injection**  -  wallet addresses containing regex metacharacters (`.`, `+`, `|`) were passed unescaped into Prometheus `delete_series` match parameter, potentially deleting metrics for unrelated miners. Now escapes with `regexp.QuoteMeta`
- **Docker init-db.sh SQL injection via shell expansion**  -  `<<-EOSQL` (unquoted heredoc) allowed bash to expand `${GRANT_USER}` directly into SQL GRANT statements. A username containing SQL metacharacters could inject arbitrary SQL. Changed to quoted heredoc (`<<-'EOSQL'`) with psql `-v` variable binding and `:"grant_user"` identifier quoting
- **Dashboard run.sh gunicorn CWD not set**  -  `gunicorn dashboard:app` requires the working directory to contain `dashboard.py` for Python module import. If invoked from any other directory (e.g., systemd without `WorkingDirectory`), gunicorn fails with `ModuleNotFoundError`. Added `cd "$SCRIPT_DIR"` before launch
- **Windows installer `.Substring(0, 2)` crash on short path input**  -  `$storagePath.Substring(0, 2)` throws `ArgumentOutOfRangeException` if the user enters fewer than 2 characters, killing the entire installer. Added length and format validation before the substring call
- **Windows installer Grafana password has no repeated characters**  -  `Get-Random -Count 24` samples without replacement, so the 24-character password can never contain a repeated character. Changed to per-character sampling with replacement
- **Dockerfile version label not bumped**  -  `LABEL version="2.0.0"` in docker/Dockerfile was missed during the v2.0.1 version bump
- **coin-upgrade.sh QBX CLI install failure silently swallowed**  -  `[[ -n "$c" ]] && sudo install ... || true` parsed as `(test && install) || true` due to operator precedence; a failed `sudo install` (disk full, permissions) was silently ignored, letting the upgrade report success without the CLI binary. Replaced with `if/then/fi`
- **maintenance-mode.sh TOCTOU lock race**  -  noclobber-based lock had a race between reading the PID and checking if it's alive; two concurrent callers (coin-upgrade + dashboard API) could both acquire the "lock". Replaced with `flock` (matching `ha-service-control.sh` pattern)
- **maintenance-mode.sh expired file deleted without lock**  -  `show_status()` and `is_maintenance_active()` deleted the maintenance file without holding the lock, racing with `extend_maintenance()`. Now acquires lock before deleting expired files
- **WAL recovery uint64 underflow discards valid blocks**  -  `currentHeight - block.Height` wraps to ~1.8×10¹⁹ when `block.Height > currentHeight` (possible after reorg or testnet reset), causing the block to be permanently rejected as "too old". Added underflow guard
- **Payment processor data race on `consecutiveFailedCycles`**  -  `processCycle` wrote the counter without holding `mu`, but the health-check goroutine read it under `mu`. Go race detector would flag this. Moved writes under the existing mutex
- **Migration rows hold DB connection through entire migration loop**  -  `defer rows.Close()` in `runMigrations` kept the `schema_migrations` query connection open for the duration of all DDL statements. On small pools (`MaxConns=2`), this can deadlock. Now closes rows immediately after reading
- **Block insert retry sleeps on miner message-loop goroutine**  -  `handleBlock`'s 2-second retry sleep blocked the miner's connection goroutine, preventing reads/writes. The keepalive timer could fire during the sleep, hitting the 5-second write deadline and disconnecting the miner who just found a block. Moved retry to a background goroutine
- **coin-upgrade.sh maintenance mode silently never activates**  -  `enable_maintenance` passed `"coin-upgrade"` as the duration parameter (first positional arg). `maintenance-mode.sh enable` validates duration with `^[0-9]+$`, so the call always fails — silently swallowed by `|| true`. Discord alerts fire during the entire upgrade window. Fixed to pass `60 "coin-upgrade"` (duration then reason)
- **coin-upgrade.sh predictable temp directory (local privilege escalation)**  -  `WORK_DIR="/tmp/spiral-coin-upgrade-$$"` used a PID-based path. Between assignment and `mkdir -p`, another user could pre-create the path as a symlink. Since coin-upgrade runs as root, `tar -xzf` would extract files to the symlink target. Changed to `mktemp -d`
- **maintenance-mode.sh `show_status` dead expired-check path**  -  duplicate `$now -ge $end_time` checks at lines 520 and 536; the first returned early with "INACTIVE" status, making the second block ("EXPIRED (auto-clearing...)") unreachable dead code. Removed the first early-return so the informative EXPIRED message is displayed
- **ha-replicate.sh TOCTOU lock race**  -  PID-based `cat`/`kill -0` lock had a race window between reading the PID file and checking if the process is alive. Two concurrent `ha-replicate` runs (cron overlap) could both acquire the "lock". Replaced with `flock` (matching `blockchain-restore.sh` and `maintenance-mode.sh` patterns)
- **Windows installer DB/RPC passwords use weak PRNG**  -  `Get-Random` uses `System.Random` (seeded from clock), not a CSPRNG. Database and RPC passwords were predictable if an attacker knew the approximate installation time. Changed all password generation (DB, RPC, Grafana) to `System.Security.Cryptography.RandomNumberGenerator`
- **spiralpool-add-coin.bat stale `%ERRORLEVEL%` in nested blocks**  -  cmd.exe expands `%ERRORLEVEL%` at parse time inside parenthesized blocks, not at execution time. All nested checks (winget availability, install result, firewall, pip) saw stale values from the outer block. Changed to `!ERRORLEVEL!` (delayed expansion, already enabled)
- **spiralpool-add-coin.bat predictable temp file name**  -  `%RANDOM%` produces only 32768 values; combined with PID prediction, an attacker could pre-create the temp file as a junction to redirect Python output or inject false port data parsed by the firewall configuration step. Changed to triple `%RANDOM%` concatenation
- **Dashboard run.sh `grep -oP` breaks macOS**  -  `grep -oP` (Perl regex) is not available on macOS's BSD grep. `find_python` and `check_debian_deps` silently fail, reporting "Python 3.8+ not found" even when installed. Changed to portable `grep -oE`
- **rescan-miners.sh `--reset` silent failure on permission denied**  -  `rm -f` suppresses errors, so `clear_database` reported "Database cleared!" even when the file (owned by spiraluser) was not actually removed. Stale data persisted into the next scan. Now falls back to `sudo rm` and verifies deletion
- **rescan-miners.sh `wait -n` fallback breaks job throttling**  -  on bash < 4.3, `wait -n` is unavailable and the `|| wait` fallback waits for all jobs but only decrements the counter by 1. After the first batch, `id_jobs` goes negative and all remaining miners are launched simultaneously. Now resets counter to 0 on full wait
- **TLS stratum accept loop blocks graceful shutdown**  -  `tls.Listen()` returns an unexported `*tls.listener` type, so the `listener.(*net.TCPListener)` type assertion always fails for TLS connections. `SetDeadline` was never called, causing `Accept()` to block indefinitely. The TLS accept goroutine could not exit during shutdown until `listener.Close()` was called. Now creates the TCP listener first, stores it, and wraps with `tls.NewListener`
- **Connection classifier regex false positives on `.00` worker names**  -  `\.0{2,}\d*$` matched any string ending in `.00` (two zeros, no trailing digit), misclassifying legitimate worker names like `farm.v2.009`. Changed `\d*` to `\d+` to require at least one trailing digit
- **`globalDeviceHints` data race between production and test goroutines**  -  package-level `globalDeviceHints` pointer was read/written without synchronization. Production goroutines calling `GetGlobalDeviceHints()` could race with `SetGlobalDeviceHints()`. Added `sync.RWMutex` protection
- **spiralctl config backup silently overwritten on consecutive saves**  -  `backupFile` always wrote to `config.yaml.backup`, destroying the previous backup. Two config changes in succession meant the original good config was lost. Added timestamp to backup filename (`config.yaml.20260329-120000.backup`)
- **`GetRouterProfiles` API returns unscaled default difficulty profiles**  -  always read from `DefaultProfiles` (base SHA-256d/600s), ignoring block-time scaling and algorithm selection (Scrypt). The API reported incorrect difficulty values for Scrypt coins, Fractal Bitcoin, or any chain with non-600s block times. Now reads from the router's active scaled profiles via `GetAllProfiles()`
- **Windows installer WSL2 portproxy exposes RPC and DB on 0.0.0.0**  -  daemon RPC and PostgreSQL (also wrong port 5432 vs docker-compose's 5433) were forwarded on `0.0.0.0`, exposing them to the LAN. RPC and DB should never be LAN-accessible. Split into public ports (`0.0.0.0` — stratum, P2P, dashboard, API, metrics) and internal ports (`127.0.0.1` — RPC, PostgreSQL). Fixed PostgreSQL to port 5433
- **Windows installer WSL2 scheduled task uses `-AtStartup`**  -  WSL2 is not available before user login (Store-installed `wsl.exe` requires a user session). The port forwarding task silently failed on every boot. Changed to `-AtLogOn`
- **pool-mode.sh hardcoded "spiralpool-ha" username**  -  `chown` and sudoers entries referenced "spiralpool-ha" but the `$HA_SSH_USER` variable defaults to "spiralha". The key exchange handler, `.ssh` directory ownership, and sudo permissions all targeted a nonexistent user. Changed all references to use `$HA_SSH_USER`
- **install.sh checkpoint missing QBX_POOL_ADDRESS**  -  all other `*_POOL_ADDRESS` variables were saved to checkpoint, but Q-BitX was omitted. Resuming from checkpoint after entering a QBX wallet address would silently lose it. Also removed duplicate `ENABLE_QBX` entry
- **Windows installer WSL2 portproxy missing Stratum V2 port**  -  `CoinConfig` hashtable lacked `V2Port`, so portproxy fallback rules and port conflict checks only forwarded V1 and TLS. Miners using Stratum V2 (Noise protocol) could not connect through WSL2 NAT. Added `V2Port` to all 14 coin entries, the portproxy public ports array, and the port availability check
- **Windows installer port conflict check tests wrong PostgreSQL port**  -  checked port 5432 but docker-compose maps PostgreSQL as `127.0.0.1:5433:5432` (host port is 5433). Real conflicts on 5433 were missed; false positives on 5432. Changed to 5433
- **configure-coin-firewall.ps1 `$MyInvocation.ScriptName` empty inside function**  -  `$MyInvocation.ScriptName` is unreliable inside functions in some PowerShell contexts, returning empty. The manifest path computation failed and the script exited with "manifest not found" even when it existed. Changed to `$PSScriptRoot` with fallback (also fixed in `configure-firewall.ps1`)

### Changed
- All version strings, documentation, templates, and config files bumped to 2.0.1

---

## [2.0.0]  -  2026-03-27  -  Phi Hash Reactor

> *System upgrade complete. All nodes nominal.*

This is a major release. All changes are backward-compatible: no database migrations, no config format changes, no reinstall required.

**Dashboard overhaul**  -  Spiral Dash has been rebuilt with a three-tab layout (Overview, Blocks, Management), interactive Chart.js analytics, fleet group views with per-group stats, per-firmware miner controls (AxeOS, Avalon, Vnish, ePIC, LuxOS), Avalon power scheduling, HTTPS/TLS with self-signed certificates, a full Management section (service control, log viewer, system updates, system reboot, resource monitoring), 23 built-in themes with custom theme editor, and CSV/JSON data export.

### Added

**Sentinel  -  Security & Firmware Monitoring**
- **Stratum URL mismatch detection**  -  Sentinel compares each miner's reported pool URL against the expected stratum host:port. Alerts on first detection (6h cooldown) if a miner has been pointed at a different pool  -  catches firmware hijacking and misconfiguration
- **BraiinsOS/Vnish auto-scan**  -  when the CGMiner probe fails on port 4028, Sentinel falls back to HTTP on port 80 and probes BraiinsOS (`GET /api/v1/auth/login`) and Vnish (`POST /api/v1/unlock`) with default credentials. Successful detection auto-classifies the device; failed detection logs "requires manual credential setup"
- **Wallet mismatch warning**  -  at startup, Sentinel validates each coin's configured pool wallet against the node's `validateaddress`/`getaddressinfo` RPC. Mismatches trigger a Discord/notification alert and a red warning banner on the dashboard
- **Generic webhook notifications**  -  new notification channel: raw HTTP POST to any URL with a JSON payload (`event`, `title`, `description`, `fields`, `timestamp`). Supports custom headers. Enables Zapier, Home Assistant, IFTTT, PagerDuty, n8n, and custom scripts. Configured alongside existing channels in install.sh setup menu
- **Fleet group offline/online alerting**  -  when all miners in a user-defined worker group go offline past the threshold, Sentinel fires a `group_offline` alert naming the group and listing affected miners (max 8 in embed). When the group recovers, a `group_online` alert fires showing online/total count. Individual miner alerts are suppressed for group members to avoid duplicates. A 2-minute grace window allows staggered outages (e.g., power propagating across a switch) to coalesce into a single group alert. Groups loaded from `miner_groups.json` with 60-second cache
- **Group-aware temperature alerting**  -  when 2+ miners in a user-defined group hit temp warning or critical thresholds in the same monitoring cycle, Sentinel sends a single group thermal alert ("check HVAC at this location") instead of individual alerts per miner. Thermal shutdowns remain individual (safety-critical, never suppressed). Falls back to individual alerts when no groups are defined or only 1 miner in a group is affected
- **Group-aware degradation alerting**  -  when 2+ miners in a user-defined group show hashrate degradation simultaneously, Sentinel sends a single group degradation alert ("check power/cooling at this location") with per-miner baselines and drop percentages. Individual miners degrading alone still get individual alerts
- **HTTPS auto-detection**  -  Sentinel auto-detects whether the dashboard is running HTTPS by reading the spiraldash service file. Dashboard API calls use the correct protocol without manual configuration. Self-signed certs on localhost are accepted automatically

**Dashboard  -  Hashrate, Analytics & Export**
- **Interactive hashrate charts**  -  Chart.js powered graphs for per-coin and aggregate hashrate with 15M/1H/6H/24H/7D/30D time range selector. Data sourced from Sentinel's existing hashrate history
- **Block odds / luck tracking**  -  live display of network hashrate share %, estimated time to block (ETB), projected blocks per day/month, and a luck ratio (expected vs actual block interval). Also surfaced in Sentinel intel reports
- **Fleet power consumption & efficiency**  -  aggregate per-miner watts into fleet-wide total (kW), W/TH efficiency metric, and optional electricity cost estimate (configured via `power_cost.rate_per_kwh` in `config.json`, hidden if not set)
- **Earnings calculator**  -  earnings section showing block reward value, coin price, and monthly earnings estimate using existing ETB math
- **CSV/JSON export endpoints**  -  three download endpoints: `/api/export/blocks`, `/api/export/earnings`, `/api/export/hashrate`. Streams from PostgreSQL, available in CSV and JSON formats. Requires dashboard auth

**Dashboard  -  Miner Detail & Monitoring**
- **Per-hashboard temperature stats**  -  Antminer S19/S21, Whatsminer, and CGMiner devices reporting chain data now expose per-board chip and PCB temperature arrays in the miner detail view (not just the single highest temp)
- **Device type breakdown chart**  -  pie/donut chart of miner types in the fleet (Antminer, Bitaxe, Avalon, etc.) using Chart.js
- **Block finder history**  -  every block found by the pool is attributed to the specific miner and worker that submitted the winning share. Records: block hash, block height, worker name, miner IP, device type, and timestamp. Persisted to `block_history.json` (last 100 blocks). Shown on the dashboard as `Last Block Found By` in the pool stats panel

**Dashboard  -  Miner Control (Manual)**

Device Configuration modal in the Miner Management tab  -  per-firmware controls for all supported device families:

- **AxeOS devices** (Bitaxe, NerdQAxe, Hammer, LuckyMiner, JingleMiner, Zyber)  -  fan speed %, frequency (MHz), and voltage (mV) via `POST /api/system`
- **Avalon/Canaan devices**  -  three power modes (Efficiency / Balanced / High) via CGMiner `ascset|0,workmode` + `ascset|0,freq` + `ascset|0,voltage`. Model-aware profiles for every generation: Nano 3/3S, Q series, A1066/A1166/A1246/A1346/A1366/A1466/A1566, Avalon 7/8. Fan speed via `ascset|0,fan,MIN-MAX`
- **Vnish firmware** (Antminer with Vnish aftermarket firmware)  -  fan speed and manual overclock (frequency, voltage) via REST `/api/v1/settings`. Autotune preset enumeration via `/api/v1/autotune/presets`
- **ePIC BlockMiner**  -  fan speed %, overclock (frequency MHz, voltage mV), and reboot via HTTP REST on port 4028
- **LuxOS firmware** (Braiins LuxOS on Antminer)  -  fan speed, frequency, named profile switching (list and apply profiles), and restart via LuxOS session protocol

**Dashboard  -  Avalon Power Schedules**
- **Time-based power profile scheduling**  -  configure automatic Efficiency/Balanced/High mode switches by time of day for any Avalon device. Overnight low-power mode, peak-hours performance mode. Rules support overnight ranges (e.g. 21:00–09:00). Persisted to `avalon_schedules.json`
- API: `GET/PUT/DELETE /api/avalon/schedules/<ip>`, `POST /api/avalon/schedules/<ip>/apply`, `GET /api/avalon/profiles`

**Dashboard  -  Worker Groups & Tags**
- **Worker groups**  -  miners can be organized into named groups via `miners.json` or `spiralctl miner group set <IP> <group>`. Dashboard shows aggregate stats per group
- **Worker tags**  -  optional freeform tags on miners (e.g. `asic,garage,s21`). Manageable via `spiralctl miner tag set/list/clear` and dashboard API

**Dashboard  -  Fleet Group View**
- **Fleet group view mode**  -  three-way miner grid toggle: flat → grouped (by hardware type) → fleet (by user-defined worker groups). Fleet view organizes miner cards under group headers with per-group hashrate, power, and online/total counts. Ungrouped miners shown in a separate section
- **Fleet group summary bar**  -  chip-style summary strip above the miner grid showing each group's name, aggregated hashrate, power draw, and online miner count. Groups with all miners offline are highlighted in red
- **Fleet group API aggregation**  -  `/api/pool/stats` response now includes `fleet_groups` array with per-group totals (hashrate_ths, power_watts, online_count, total_count) resolved from `miner_groups.json`

**Dashboard  -  Block Analytics Tab**
- **Dedicated Blocks tab**  -  new top-level tab with block analytics: pool hashrate share %, expected blocks, luck ratio, and a dual bar chart (actual vs expected blocks found). Auto-refreshes every 60 seconds when active
- **Luck history API**  -  `/api/luck` now returns full history (up to 720 hourly samples) and pool/network hashrate, enabling the Blocks tab charts

**Dashboard  -  Charts & Statistics**
- **Blocks Found bar chart**  -  bar chart in the Statistics grid showing block discovery history per coin
- **Shares Rate line chart**  -  real-time line chart showing accepted share rate over time
- **Chart theme integration**  -  chart colors (grid lines, labels, datasets) are wired to the theme system via CSS variables. All built-in theme JSONs updated with chart color definitions. Custom theme editor includes chart color pickers. Block analytics colors (actual, expected, pool share) added to theme editor

**Dashboard  -  Log Viewer Live Mode**
- **Live auto-refresh**  -  log viewer in the Management tab now has a Live button that enables 2-second auto-refresh polling of `journalctl` output. Green indicator when active. Toggleable on/off without losing scroll position

**Dashboard  -  HTTPS / TLS**
- **Self-signed TLS certificate**  -  dashboard serves over HTTPS by default using gunicorn's native `--certfile` / `--keyfile` flags. Self-signed ECDSA P-256 certificate generated during installation with 10-year validity and SANs for hostname, all detected LAN IPs, localhost, and 127.0.0.1
- **HTTP insecure connection warning banner**  -  context-aware: if HTTPS is enabled, warns and links to the HTTPS URL. If cert exists but HTTPS not yet enabled, nudges user to the Management tab. If neither, banner is hidden (nothing actionable). Dismissable per session
- **Secure cookie auto-detection**  -  `SESSION_COOKIE_SECURE` now auto-detects from the spiraldash service file instead of defaulting to false. Ensures cookies are marked secure when HTTPS is active without requiring a manual env var

**Dashboard  -  Management Section** *(new tab)*
- **Service control panel**  -  start/stop/restart spiralstratum, spiralsentinel, spiraldash, and coin daemons from the dashboard. Shows service status and uptime
- **System resources panel**  -  real-time CPU load average (1/5/15 min), RAM usage (total/used/available/%), disk usage per mount (/, /spiralpool, /var), and system uptime. Sourced from `/proc`  -  no psutil dependency
- **Log viewer**  -  streams `journalctl` output for any pool service with color-coded severity levels, auto-scroll, pause button, and live auto-refresh mode
- **System updates**  -  lists available apt packages with last-checked timestamp. One-click refresh (`apt-get update`) and apply (`apt-get dist-upgrade`) with confirmation. Runs via `apt-noninteractive.sh` wrapper that uses `systemd-run --pipe` to escape the dashboard's `ProtectSystem=strict` mount namespace
- **System reboot button**  -  one-click graceful reboot from the Management tab. Uses `systemctl --no-block reboot` so the dashboard can send its response before systemd begins the shutdown sequence. Confirmation dialog required
- **System info API endpoint**  -  `GET /api/system/info` provides programmatic access to all host metrics (CPU, memory, disk, service statuses)

**Installer & Upgrade**
- **Pruned node support**  -  install.sh offers a pruning option during coin setup ("Full node or Pruned"). Sets `prune=5000` (5GB) in daemon conf. All pool operations work on pruned nodes (getblocktemplate, submitblock, ZMQ). Savings: BTC 600GB→5GB, DGB 60GB→5GB, BCH 200GB→5GB  -  critical for WSL2 and small-disk deployments
- **`spiralctl coin prune <TICKER>`**  -  enable blockchain pruning on an existing coin node without reinstalling
- **Pruned node badge**  -  dashboard indicator next to node status when the backing coin daemon is running in pruned mode
- **Notification channel menu**  -  unified selection menu in install.sh for Discord, Telegram, XMPP, ntfy, Email, and Webhooks
- **Dashboard TLS certificate generation**  -  install.sh generates a self-signed ECDSA P-256 certificate with SANs (hostname, LAN IPs, localhost) during installation. Certificate stored in `$INSTALL_DIR/certs/`

**Upgrade  -  v2.0 Migration**
- **Automatic config migration**  -  upgrade.sh now always runs `migrate_v2_config()` which handles: metrics section creation, api section creation, `admin_api_key` v1→v2 field migration, and sentinel `config.json` sync. Each migration is idempotent (grep before modify)
- **Service files and config fixes always enabled**  -  upgrade.sh now regenerates systemd service files and runs config fixes on every upgrade by default (previously required `--full` flag). All migrations are idempotent and preserve HTTPS, dependencies, and custom settings
- **Major version auto-detection**  -  upgrade.sh detects major version jumps (e.g. 1.x → 2.x) and logs the change. Ensures critical service file updates are never skipped on major upgrades
- **Docker deployment guard**  -  upgrade.sh detects if it's running inside a Docker container or if Docker containers are the active deployment, and blocks/warns with correct Docker upgrade instructions instead of corrupting the install
- **WSL2 pre-flight checks**  -  upgrade.sh warns about clock drift, memory pressure, and missing systemd on WSL2 before proceeding
- **Sudoers migration for existing installs**  -  upgrade.sh detects missing sudoers entries (journalctl, apt wrapper, upgrade.sh, psql, enable-https, system reboot) in existing `/etc/sudoers.d/spiralpool-dashboard` and appends them individually with `visudo -c` validation
- **HTTPS migration (opt-in)**  -  upgrade.sh pre-generates a self-signed ECDSA P-256 TLS certificate and deploys the `enable-https.sh` script. Existing HTTP-only installs stay on HTTP — operators enable HTTPS when ready from the Dashboard Management tab. This avoids broken bookmarks and unexpected cert warnings on existing installs

**Windows / WSL2**
- **WSL2 graceful shutdown hook** (`scripts/windows/wsl2-shutdown-hook.ps1`)  -  Windows Task Scheduler task that gracefully stops all Spiral Pool services and coin daemons before Windows shuts down, restarts, or enters sleep. Without this, Windows kills WSL2 mid-write and corrupts LevelDB blocks/chainstate, requiring a full blockchain resync. Stop order: sentinel/dash/health → stratum → coin daemons → wait for sync. Triggers on Event 1074 (shutdown/restart) and Event 42 (sleep). Logs to `%APPDATA%\SpiralPool\shutdown-hook.log`. Install/uninstall via `-Uninstall` flag. Wired into `wsl2-stratum-proxy.ps1` as a recommended setup prompt

**Docker**
- **Webhook environment variables**  -  `WEBHOOK_URL` and `WEBHOOK_HEADERS` env vars passed through to Docker containers for generic webhook notification support
- **TLS certificate generation in entrypoint**  -  Docker entrypoint generates a self-signed ECDSA P-256 TLS certificate for the dashboard, matching the native install behavior
- **Health check tuning**  -  PostgreSQL health check `start_period` increased to 120s to accommodate WAL recovery after crashes
- **Entrypoint error handling**  -  multi-coin config generation now validates the write succeeded and cleans up temp files on failure instead of silently continuing with a partial config

**Documentation**
- **Miner API limitations reference**  -  `docs/reference/MINER_SUPPORT.md` expanded with confirmed API limitations for four device families: iPollo (CGMiner API disabled by default  -  requires `--api-listen` flag), Innosilicon (CGMiner disabled by default on most models), Elphapex DG series (LuCI CGI primary, CGMiner on port 4028 unconfirmed), and ESP32 miners (no device API  -  online/offline and hashrate tracked via stratum connections only; temperature and fan alerts unavailable)

**Stratum V2 API  -  Full Endpoint Parity**
- **Worker/miner stats endpoints**  -  V2 multi-coin API now serves all worker and miner endpoints that V1 provides: `GET /api/pools/{id}/miners`, `/miners/{addr}`, `/miners/{addr}/workers`, `/miners/{addr}/workers/{w}`, `/miners/{addr}/workers/{w}/history`, `/hashrate/history`, and `/workers` (admin). All queries are pool-scoped via `WithPoolID()` for multi-coin isolation
- **Runtime provider endpoints**  -  V2 now serves `/workers-by-class`, `/router/profiles`, `/pipeline/stats`, and `/payments/stats` per pool, sourced from live CoinPool state (Spiral Router, share pipeline, block stats). Dashboard features that depended on these endpoints no longer 404 on V2
- **Admin endpoints**  -  V2 now serves `/api/admin/stats` (aggregated across all pools with per-pool breakdown and totals), `/api/admin/kick` (disconnects miner by IP across all pools), and `/api/coins` (registered coin registry for Sentinel/Dashboard validation)
- **Security headers middleware**  -  V2 API responses now include `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and `Cache-Control: no-store` (matching V1 parity)
- **Dynamic payout scheme**  -  V2 `/api/pools` response now reads `PayoutScheme` and `MinimumPayment` from the per-coin config instead of hardcoding `"SOLO"` / `1.0`

**Codename Theme**
- **V2.0  -  Phi Hash Reactor** theme added to the dashboard theme selector  -  reactor-core black with critical red accents, scan lines, and a reactor pulse animation on block found

### Fixed

**HTTPS / TLS  -  Critical Detection & Deployment**
- **HTTPS detection matches template comment instead of ExecStart**  -  all 13 `grep -q "\-\-certfile"` checks across 6 files (install.sh, upgrade.sh, enable-https.sh, spiraldash.service, dashboard.py, SpiralSentinel.py, spiralctl.sh) matched the template comment `(runs enable-https.sh to add --certfile/--keyfile)` instead of the actual ExecStart line. HTTPS was silently detected as enabled even on HTTP-only installs, or silently lost during upgrades when the template was rewritten. Fixed all 13 locations to use `^ExecStart` line anchoring (`grep -q "^ExecStart.*\-\-certfile"` in bash, `line.strip().startswith("ExecStart")` in Python)
- **`set -e` kills installer/upgrader on openssl failure**  -  `openssl req -x509` ran as a bare command under `set -e`. If certificate generation failed (missing openssl, permission denied, disk full), the entire install/upgrade script aborted immediately. The `if [[ $? -eq 0 ]]` fallback was dead code  -  it never ran because `set -e` exited first. Wrapped openssl as the `if` condition directly
- **Certificate directory permission denied on fresh install**  -  install.sh runs as a non-root user with sudo. `sudo mkdir` created `$INSTALL_DIR/certs/` as root-owned, but `openssl` ran without sudo and couldn't write the certificate. Added `sudo chown` on the directory and `sudo` on the openssl command
- **`sed -i` fails with "Read-only file system" when enabling HTTPS**  -  `enable-https.sh` runs via sudo from the dashboard, which inherits the gunicorn process's mount namespace where `ProtectSystem=strict` makes `/etc` read-only. Even as root, `sed -i` can't create its temp file. Added `/etc/systemd/system` to `ReadWritePaths` in the spiraldash service  -  file permissions (root:root 644) still prevent the unprivileged dashboard process from writing directly
- **`apt-get update` fails with "Read-only file system" from dashboard**  -  same `ProtectSystem=strict` issue. `apt-get` needs write access to `/var/lib/apt`, `/var/cache/apt`, and `/var/log/apt`. Added these paths to `ReadWritePaths`
- **`sudo` fails with "unable to change to root gid" from dashboard**  -  `CapabilityBoundingSet=` (empty) in the spiraldash service template blocked all capability acquisition. Even with `NoNewPrivileges=no`, sudo couldn't get `CAP_SETGID`/`CAP_SETUID`/`CAP_AUDIT_WRITE`. Set to `CAP_SETUID CAP_SETGID CAP_DAC_OVERRIDE CAP_AUDIT_WRITE CAP_FOWNER`  -  only the capabilities sudo needs
- **Dashboard XSS via innerHTML injection**  -  3 locations in dashboard.html injected unsanitized server responses into innerHTML: cert expiry date, HTTPS enable error message, and JS exception message. Added `escapeHtml()` to all 3 locations
- **MOTD shows hardcoded port 1618**  -  the MOTD banner displayed `Dashboard: https://IP:1618` regardless of the actual configured port. Replaced with dynamic detection from the spiraldash service file (`grep -oP '0\.0\.0\.0:\K[0-9]+'`)
- **Post-upgrade summary shows wrong protocol**  -  upgrade completion banner always showed `http://` regardless of HTTPS status. Added runtime detection from the service file's ExecStart line
- **HA-backup completion banner shows wrong protocol**  -  same issue in install.sh's HA-backup completion message. Added the same runtime protocol detection
- **`spiralctl` hardcoded protocol and port**  -  `spiralctl.sh` dashboard URLs used hardcoded `http://` and port 1618 in 2 locations. Added dynamic port and protocol detection
- **`enable-https.sh` missing from sudoers on upgrade**  -  the sudoers fresh-create heredoc in upgrade.sh was missing the `enable-https.sh` NOPASSWD entry, so the dashboard's "Enable HTTPS" button would fail with a password prompt on upgraded (non-fresh) installs
- **Dashboard upgrade.sh path wrong**  -  dashboard.py called `sudo /spiralpool/scripts/upgrade.sh` but the script is deployed to `/spiralpool/upgrade.sh` (install root, not scripts/). Sudoers allows `/spiralpool/upgrade.sh *` so the mismatched path triggered password prompts. Fixed to use `$SPIRALPOOL_INSTALL_DIR/upgrade.sh`

**Sentinel  -  Group Alert Ordering**
- **Group offline alert fires after individual alerts (duplicate notifications)**  -  when all miners in a group went offline, the individual miner offline loop ran first and sent per-miner alerts. The group check ran after and sent the group alert  -  resulting in N+1 alerts instead of 1. Restructured: pre-compute which miners will be covered by group alerts before the individual loop, suppress individual alerts for those miners
- **Staggered outage produces both individual and group alerts**  -  in a real power outage, miners go offline seconds apart across polling cycles. The first miner to cross the 10-minute threshold would get an individual alert before its siblings caught up. Added a 2-minute grace window: miners in multi-member groups defer their individual alerts to allow siblings to coalesce into a single group alert
- **Individual miner_online alerts fire for group-covered miners**  -  when a group offline alert was active and miners recovered one by one, each got an individual `miner_online` alert even though the group recovery path should handle it. Added group membership check to the recovery loop  -  miners in active group alerts are suppressed from individual online alerts

**Stratum V2 API  -  Bugs & Missing Features**
- **V2 API returns 404 on 13 endpoints the dashboard depends on**  -  V2 multi-coin API only implemented 9 of 26 V1 endpoints. Dashboard worker stats, miner stats, hashrate history, workers-by-class, router profiles, pipeline stats, and payment stats all silently failed with 404 when pointed at a V2 stratum server. Added all missing endpoints scoped to per-pool database tables
- **V2 `/api/pools` hardcoded payout scheme `SOLO` / minimum payment `1.0`**  -  the `/api/pools` response returned hardcoded `"SOLO"` and `1.0` regardless of the per-coin payment configuration in the V2 config file. Dashboard displayed wrong payment info. Fixed to read from `coin.Payments.Scheme` and `coin.Payments.MinimumPayment`
- **V2 API missing security headers**  -  V1 API applies `X-Content-Type-Options`, `X-Frame-Options`, and `Cache-Control` via `securityHeadersMiddleware`. V2 was missing this middleware entirely  -  responses had no security headers. Added to the V2 middleware chain
- **V2 API missing `/api/admin/kick` endpoint**  -  the kick worker endpoint existed in V1 but was never ported to V2. Dashboard's "Kick" button would fail. V2 implementation kicks across all registered coin pools
- **V2 API missing `/api/admin/stats` endpoint**  -  admin stats endpoint existed in V1 but was never ported to V2. V2 implementation aggregates stats across all pools with per-pool breakdown
- **V2 API missing `/api/coins` endpoint**  -  coin registry endpoint existed in V1 but was never ported to V2. Sentinel and Dashboard coin validation calls would 404
- **V2 `GetPaymentStats` nil dereference on CoinbaseMaturity**  -  `cp.coin.CoinbaseMaturity()` would panic if the coin interface was nil (possible during early startup or test harness). Added nil guard with a safe default of 100 confirmations
- **V2 `GetPaymentStats` silent failure on DB type assertion**  -  if the database provider was not a `*PostgresDB` (e.g., mock or test DB), the type assertion silently failed and returned empty stats with no indication of why. Added debug-level log so operators can diagnose missing payment data

**Connection Classifier Tests**
- **Proxy classification tests updated for raised threshold**  -  `TestClassifier_Level2_InstantAuthorize` and `TestClassifier_Level2_FastAuthorize` expected PROXY classification from timing signals alone, but the proxy confidence threshold was raised from 0.15 to 0.40 in v1.2.0. Tests now correctly expect UNKNOWN for timing-only signals and a new test validates that timing + proxy worker name pattern (combined score >= 0.40) still classifies as PROXY

**Installer  -  UFW & Sync View**
- **UFW crashes with `UnicodeEncodeError` on fresh install**  -  UFW rule comments in install.sh contained Unicode em-dashes (U+2014) which caused `bytes(out, 'ascii')` in UFW's Python backend to crash. Replaced em-dashes with regular dashes in connlimit/metrics rule comments and added a defensive `sed` cleanup before the first `ufw` command to sanitize any existing rules
- **Sync live view  -  progress bar two rows below controls bar**  -  cursor positioning was 2 rows too low, leaving a double blank gap between the controls bar and the progress bar. Adjusted all `\033[row;1H` positions and placeholder echo lines to place progress directly below the controls border

**Dashboard  -  System Updates & Reboot**
- **`apt-get dist-upgrade` fails from dashboard with read-only filesystem**  -  `ProtectSystem=strict` in spiraldash.service creates a kernel mount namespace that makes the entire filesystem read-only for all child processes, including those escalated via sudo. `apt-get` couldn't write to `/var/lib/dpkg/lock`. Fixed with a wrapper script (`apt-noninteractive.sh`) that uses `systemd-run --pipe` to make systemd (PID 1) start apt-get in the root namespace, completely outside the dashboard's restrictions
- **`apt-get update` refresh also fails from dashboard**  -  the Refresh button called bare `sudo apt-get update` which hit the same `ProtectSystem=strict` and sudoers issues as dist-upgrade. Switched to use the same `apt-noninteractive.sh` wrapper
- **`apt-get upgrade` doesn't upgrade kernel packages**  -  `apt-get upgrade` refuses to install packages that require new dependencies (like `linux-generic` meta-packages). Changed to `apt-get dist-upgrade` which handles dependency changes. Added `--force-confold` and `--force-confdef` dpkg options to suppress config file prompts
- **`apt-get dist-upgrade` shows `debconf: unable to initialize frontend: Dialog`**  -  `DEBIAN_FRONTEND=noninteractive` set via subprocess `env=` parameter was stripped by sudo's `env_reset`. Solved by the `apt-noninteractive.sh` wrapper which sets the env var inside the root process before exec'ing apt-get

**Dashboard  -  Gunicorn Worker Deadlock on Reboot**
- **Dashboard unresponsive after reboot (gunicorn worker deadlock)**  -  after a system reboot, the gunicorn master process started and bound to port 1618, but the forked worker process deadlocked before reaching `init_process()` (post-fork inherited threading lock). Gunicorn's `--timeout 120` failed to detect the stuck worker, and systemd saw the master as "active"  -  the dashboard was unreachable indefinitely until manually killed. Added an `ExecStartPost` health check to the spiraldash systemd service: polls `curl http://127.0.0.1:<port>/` every 2 seconds for up to 60 seconds; if no response, sends `SIGKILL` to the main process so `Restart=always` recovers it automatically
- **`spiralpool-sync` does not start dashboard after blockchain sync**  -  when `spiralpool-sync` detected a fully synced blockchain, it started `spiralstratum` but never started `spiraldash` or `spiralsentinel`. If these services were stopped (e.g., failed on earlier boot), the user had no dashboard after sync completed. Added startup for both services (if enabled) after stratum comes online, in both single-coin and multi-coin sync paths

**Sentinel  -  RPC Allowlist**
- **`getnetworkhashps` rejected by RPC allowlist**  -  Sentinel's `_RPC_ALLOWED_METHODS` frozenset was missing `getnetworkhashps`, causing a warning on every call. Added to the allowlist

**Stratum V2  -  Nil Guards**
- **V2 `KickWorkerByIP` panics if stratum server is nil**  -  all other new CoinPool methods had nil guards for `cp.stratumServer` but `KickWorkerByIP` was missed. Added nil guard returning 0

**Tests**
- **`TestBlockQueue_ConcurrentEnqueueDequeue` flaky**  -  under contention, `DequeueWithCommit()` returns nil while items are in-flight between goroutines. Dequeue goroutines exited on first nil, losing items and failing the count assertion. Added a `nilStreak` retry counter (100 consecutive nils before exit) with `runtime.Gosched()` to yield to enqueue goroutines

**Sentinel  -  Temperature Monitoring**
- **Goldshell `all_temps` list crashes temperature alerting**  -  Goldshell miners return temperature as a list instead of a scalar value. The temperature comparison (`temp_value >= threshold`) threw a TypeError on lists. Added type guard to skip non-scalar temperature values

**General**
- **`spiralctl` HTTPS auto-detection**  -  `spiralctl` dashboard commands now auto-detect HTTP vs HTTPS from the spiraldash service file and accept self-signed certificates on localhost, matching the dashboard and Sentinel behavior
- **`spiralctl status` timer next-run shows garbage**  -  bash expands `$(( {} / 1000000 ))` before `xargs` substitutes `{}`, producing `$(( / 1000000 ))` syntax errors and a blank or wrong next-run time in `spiralctl status`. Rewrote `_timer_next_run()` to capture `usec` into a variable first, then compute the date expression directly
- **Miner status boolean vs string mismatch**  -  dashboard miner cards checked `m.online` (boolean) but some code paths returned `m.status` (string). Unified to consistent boolean check
- **Fan RPM misidentified as percentage**  -  fan values >100 are RPM readings, not percentages. Dashboard now detects and displays RPM values correctly with appropriate units
- **Disk usage double-counting**  -  multiple mount points on the same filesystem (same device) caused disk usage to be counted multiple times. Added filesystem fingerprinting to deduplicate
- **Log viewer inconsistent text color**  -  info-level and debug-level log lines used different shades, making the log viewer visually noisy. Unified to the same muted color (errors still red, warnings still orange)
- **Custom theme editor layout**  -  section headers and grid layout cleaned up for better usability in the theme customization panel

**Coin Daemon Config Audit**
- **Removed invalid/unsupported config options across all coins**  -  full audit of all 14 coin daemon configs (install.sh, docker templates, pool-mode.sh, tests) removed options that are not valid config-file parameters or are daemon-specific copy-paste errors: `maxoutconnections` (BCHN doesn't support it), `maxconnections` (unnecessary in docker), `maxdebugfilesize` (DGB-only, was on 4 non-DGB coins), `nblocks` (not a valid config option), `blockstallingtimeout` (not a valid config option), `checkpoints=1` (not a valid config option), `maxblocksinprogress` (DGB-specific), `maxorphantx` (DGB-specific), `blockreconstructionextratxn` (DGB-specific), `deprecatedrpc=` (empty value, DGB-specific), `debug=zmq` (unnecessary verbose ZMQ logging on DOGE/PEP/CAT/NMC), `forcednsseed=1` (aggressive, replaced by existing `dnsseed=1`)
- **WSL2 tee permission denied**  -  `mktemp` without sudo creates a temp file that `sudo tee` cannot write to on some WSL2 setups. Changed to `sudo mktemp` and `sudo rm -f` in the sshd hardening block

### Removed
- `CalculateBlockReward()`  -  processor.go (7 test-only callers, zero production use)
- `Dequeue()`  -  circuitbreaker.go (12 test-only callers, zero production use)
- `BuildTLSConfig()`  -  replication.go (4 test-only callers, zero production use)
- `fetch_block_reward()` no-arg wrapper  -  SpiralSentinel.py (0 callers)
- `Authorized`/`Subscribed` exported struct fields  -  protocol.go: converted to private atomic `authorized`/`subscribed uint32` fields with `SetAuthorized`/`IsAuthorized` accessors, eliminating data races on concurrent session access
- **Lifetime Statistics section**  -  removed from dashboard Overview; uptime moved to top stats row, remaining metrics were redundant with the Statistics charts

### Changed
- All version strings, documentation, themes, and config files bumped to 2.0.0
- Codename comments updated from `V1.1.0-PHI_FORGE` → `V2.0.0-PHI_HASH_REACTOR`
- `CoinbaseMessage` updated from `SpiralPool/v1.2.0/` → `SpiralPool/v2.0.0/`
- `spiralctl config validate`  -  alert config range check description updated to v2.0.0
- All dashboard theme and template JSON files bumped to version 2.0.0
- `apply_profile_now()` endpoint now accepts `model` in the request body (request body > saved schedule > generic), enabling the dashboard UI to pass the correct Avalon model without requiring a schedule to exist first
- Dashboard restructured into three tabs: **Overview** (pool monitoring, miner grid, stats), **Blocks** (block analytics, luck tracking, charts), and **Management** (service control, log viewer, system updates, miner management)
- **Miner card buttons consolidated**  -  duplicate "Configure" buttons on Overview miner cards replaced with a single "Web UI" button
- **Uptime moved to top stats row**  -  system uptime relocated from the removed Lifetime Statistics section to the main stats bar for better visibility

### Notes
- **Zero breaking changes**  -  v1.0.0 / v1.1.x / v1.2.x installations upgrade in-place via `upgrade.sh` with no config changes, no migrations, and no coin daemon restarts required. Dashboard stays on HTTP; operators can opt in to HTTPS from the Management tab (self-signed cert; browser will show a one-time certificate warning)

---

## [1.2.3]  -  2026-03-27

### Fixed

**Installer  -  Firewall & Back Navigation**
- **Silent exit at "Configuring firewall..."**  -  `[[ -n "$STRATUM_PORT" ]] && sudo ufw allow ...` returns exit 1 when the variable is empty, which under `set -e` kills the entire installer. Replaced with `if/then` guards for both `STRATUM_PORT` and `STRATUM_V2_PORT`.
- **Back navigation ('b') kills installer**  -  `select_coin_mode`, `select_ha_mode`, `select_merge_mining_parent`, and `select_aux_chains` all use `return 1` to signal "go back". Under `set -e`, checking the return with `func; if [[ $? -eq 1 ]]` exits the script before the `$?` check runs. Rewrote all callers to use `if func; then` pattern.
- **`systemctl reset-failed` polkit auth failure**  -  `start_services()` called `systemctl reset-failed` without `sudo`, triggering polkit authentication prompts in non-interactive mode. Added `sudo` and added `reset-failed *` to the sudoers NOPASSWD allowlist.

**Installer  -  Dashboard Service**
- **Stale gunicorn control socket prevents dashboard start**  -  added `ExecStartPre=-/bin/rm -f gunicorn.ctl` to the spiraldash systemd service file and explicit `--worker-class gthread` to the `ExecStart` line.

**Upgrade  -  Dashboard Not Starting**
- **Dashboard hangs after upgrade**  -  stale `__pycache__` bytecode from the previous Python version and leftover `gunicorn.ctl` sockets from killed processes caused the dashboard to hang or fail on restart. `update_dashboard()` now cleans both before copying new files. Changed dashboard start from `--no-block` to blocking with a health check and automatic restart on failure.
- **Upgrade summary not waiting for services**  -  the summary screen now polls dashboard and sentinel for up to 120 seconds before reporting status, skipping stratum (which depends on blockchain node sync).

**Stratum Server**
- **Stratum hangs on shutdown (120s → SIGKILL)**  -  `connWg.Wait()` in `server.go:Stop()` had no timeout, hanging indefinitely when connection goroutines were stuck. Added a 10-second select timeout before proceeding with shutdown.
- **ESP32 miners showing 0 shares on dashboard**  -  `Session.IncrementShareCount()` existed but was never called in production code. Added the call in both `pool.go` (V1) and `coinpool.go` (V2) when a share is accepted. The dashboard's ESP32 panel reads this counter via the connections API.

**Spiral Sentinel  -  Block Alert**
- **Block alert shows wrong explorer page**  -  when a block is found seconds before the explorer indexes it, the "View Block" link opens a stale page. Added the block hash (first 16 chars) directly in the alert text so the user can verify without depending on the explorer.
- **Pool block counter wrong after Sentinel restart**  -  `pool_blocks_found` started from 0 on fresh state instead of initializing from the pool API's existing block count. Block #4 would show as "Pool Block #1" after a Sentinel restart.

**spiralctl**
- **`spiralctl coin enable` fails with "command not found"**  -  `prompt_input()` was defined in `install.sh` but never added to `spiralctl.sh`, causing all coin enable/onboard commands to fail immediately.

- All version strings, documentation, themes, and config files bumped to 1.2.3

---

## [1.2.2]  -  2026-03-25

### Fixed

**Installer  -  Reinstall / Upgrade Guard Pattern (all 13 coins)**
- **Daemon not stopped before config regeneration on reinstall**  -  if a daemon was already running and the user reinstalled, the installer would regenerate config files underneath a live daemon, causing port conflicts, stale PID files, and LevelDB lock contention. All 13 coin install functions now stop the running daemon (`systemctl stop`), call `reset-failed` (clears systemd's `StartLimitBurst` crash counter), and remove stale PID files before reconfiguring.
- **Reinstall skipped config regeneration entirely**  -  all 13 install functions had an early `return` when the binary already existed (`if [[ -f .../bitcoind ]]; then return`). This meant reinstalling skipped config regeneration, systemd service creation, and all downstream setup. Changed to an `*_binary_exists` + `*_download_needed` guard pattern: binary download is skipped, but config regen, service file, and wallet setup always run.
- **RPC password recovery on reinstall**  -  if `coins.env` was corrupted or truncated during a reinstall, all `*_RPC_PASSWORD` variables would be empty. The installer would then generate new passwords that don't match the passwords already written in each daemon's conf file, causing RPC auth failures on every coin. Added a 13-coin password recovery loop that reads `rpcpassword=` from each daemon's existing conf file before falling back to generating a new password.
- **BCH-specific empty password guard**  -  added an additional safety net for BCH: if `BCH_RPC_PASSWORD` is still empty after `coins.env` parsing and the recovery loop, attempts to recover from the existing `bitcoin.conf` before generating a new password. BCH was the coin triggering the crash report.

**Installer  -  WSL2 Resource Scaling (DGB, BTC, BCH)**
- **Daemons OOM-killed on WSL2**  -  `dbcache=8192` (8 GB) was hardcoded for DGB, BTC, and BCH regardless of available RAM. WSL2 instances typically have limited memory via `.wslconfig`, and 8 GB dbcache would consume all available RAM, triggering OOM kills. All three coins now detect WSL2 (`/proc/version` check), cap dbcache to 25% of total RAM (floor 1024 MB, ceiling 4096 MB), and scale `MemoryMax`/`MemoryHigh` systemd limits proportionally.

**Installer  -  systemd Service Files (all 13 coins)**
- **DGB missing PIDFile directive**  -  DGB systemd service had `Type=forking` but no `PIDFile=` or `-pid=` argument. systemd couldn't reliably track the daemon process, leading to false "active (running)" status when the daemon had already exited. Added `PIDFile=` to service and `-pid=` to `ExecStart`.
- **BC2 missing PIDFile directive**  -  same fix as DGB. Bitcoin II systemd service now has `PIDFile=` and `-pid=` argument.
- **BTC missing PIDFile directive**  -  Bitcoin Knots systemd service now has `PIDFile=` and `-pid=` argument.
- **BCH missing PIDFile directive**  -  Bitcoin Cash systemd service now has `PIDFile=` and `-pid=` argument.
- **LimitNOFILE=65535 (off-by-one)**  -  11 coin systemd services used `LimitNOFILE=65535` instead of the correct `65536` (2^16). While functionally harmless on most kernels, 65536 is the conventional power-of-two value. Standardized across all coins.

**Installer  -  BCH Config**
- **BCH missing `blockmaxsize` setting**  -  BCH config had `excessiveblocksize=32000000` (accept 32 MB blocks from the network) but was missing `blockmaxsize=32000000` (generate blocks up to 32 MB when mining). Without this, mined blocks would be capped at the Bitcoin Core default of 2 MB.

**Multi-Disk Storage (CHAIN_MOUNT_POINT)**
- **CHAIN_MOUNT_POINT grep pattern included literal quotes**  -  `coins.env` writes values as `CHAIN_MOUNT_POINT="/mnt/data"` (with quotes), but the `grep -oP '\K\S+'` pattern extracted `"/mnt/data"` including the quote characters. Every `-d` directory check silently failed, causing all multi-disk setups to fall back to `$INSTALL_DIR/<coin>/` regardless of configuration. Fixed across 12 instances in 5 files: `install.sh`, `spiralctl.sh`, `blockchain-export.sh`, `blockchain-restore.sh`, `wait-for-node.sh`.
- **spiralctl.sh `get_coin_cli()` ignored multi-disk paths**  -  all 13 coin CLI commands used hardcoded `$INSTALL_DIR/<coin>/` paths instead of checking `CHAIN_MOUNT_POINT`. Coin daemon CLI commands (getblockchaininfo, stop, etc.) would target the wrong config file on multi-disk setups. Added `_chain_dir()` helper and updated all 13 coin entries.
- **spiralctl.sh Tor status check hardcoded DGB path**  -  used `$INSTALL_DIR/dgb/digibyte.conf` instead of `$(_chain_dir dgb)/digibyte.conf`
- **blockchain-export.sh missing multi-disk support**  -  all 13 `COIN_DIRS` entries were hardcoded to `$INSTALL_DIR/<coin>/`. Added `_chain_dir()` helper with `CHAIN_MOUNT_POINT` lookup.
- **blockchain-restore.sh missing multi-disk support**  -  same fix as blockchain-export.sh
- **ha-replicate.sh missing multi-disk support**  -  all 13 `BLOCKCHAIN_DIRS` entries were hardcoded. Added `_chain_dir()` helper with `CHAIN_MOUNT_POINT` lookup.

**Daemon & Docker Config**
- **pool-mode.sh BC2 wallet commands hardcoded `/spiralpool/`**  -  5 occurrences in the BC2 wallet creation block used `/spiralpool/bc2/bitcoinii.conf` instead of `$SPIRALPOOL_DIR/bc2/bitcoinii.conf`, failing on non-default install paths.
- **DigiByte Docker config missing `zmqpubrawblock`**  -  `digibyte.conf.template` had `zmqpubhashblock` and `zmqpubrawtx` but was missing `zmqpubrawblock`. All other 12 ZMQ-enabled coins had all three topics. Docker-mode DGB would miss raw block notifications.

**HA & Recovery**
- **ha-role-watcher.sh recovery health check matched error pages**  -  `grep -q "enabled"` on the HA status endpoint would match HTML error pages containing the word "enabled" anywhere, causing false-positive health checks. Replaced with `jq -e '.enabled == true'` for proper JSON validation.

**Regtest & Testing**
- **regtest.sh PepeCoin SIGABRT crash**  -  ZMQ arguments (`-zmqpubhashblock`, `-zmqpubrawblock`) were passed unconditionally to all coin daemons. PepeCoin v1.1.0 is compiled without ZMQ support and crashes with SIGABRT on startup when zmqpub* arguments are present. ZMQ args now conditionally skipped for PEP.
- **regtest-ha-full.sh missing 5 coins**  -  script advertised support for 13 coins but only implemented 8 in the case statement. Added NMC, SYS, XMY, FBTC, and QBX with correct port configurations.
- All version strings, documentation, themes, and config files bumped to 1.2.2

---

## [1.2.1]  -  2026-03-24

### Added

- **DigiByte as merge mining parent chain**  -  install.sh now offers DGB as an explicit SHA-256d parent option (option 3) for merge mining with NMC, SYS, XMY, and FBTC auxiliary chains. Previously DGB was only an implicit fallback when BTC was disabled; now it is a first-class selection alongside BTC and LTC.
- **Back navigation in installer**  -  pressing `b` at any menu prompt returns to the previous step. Covers install mode, merge mining, coin selection, aux chain selection, and HA mode. No more Ctrl+C to fix a fat-finger.
- `spiralctl mining merge enable` also updated to recognize DGB as a valid SHA-256d parent
- Multi-coin mode merge mining prompt now detects DGB as SHA-256d parent when BTC is not present
- MOTD, Docker guide, spiralctl reference, and docker-compose.yml updated to list DGB as merge mining parent

### Fixed

- **LED celebration ignoring quiet hours**  -  the stratum Go code (`pool.go`, `coinpool.go`) launched `block-celebrate.sh` directly on block found, bypassing Sentinel's quiet hours check. The bash script now reads Sentinel's `quiet_hours_start`, `quiet_hours_end`, and `display_timezone` from config.json and enforces quiet hours at startup. Additionally, running celebrations now check periodically and stop early if quiet hours begin mid-celebration. `--force` flag added for manual override.
- **MOTD not updating on upgrade**  -  `update_motd()` in upgrade.sh used `cat >` to write to `/etc/update-motd.d/`, which silently fails without root. Now uses `sudo tee` matching install.sh.
- **Dashboard section ordering**  -  Lifetime Statistics section now renders below Statistics (charts) instead of above it
- **Flaky stress test**  -  `TestRapidFireHeightUpdates` widened stale RPC tolerance from 0 to 1; on slow CI runners a goroutine can slip through the cancellation window
- All version strings, documentation, themes, and config files bumped to 1.2.1

---

## [1.2.0]  -  2026-03-23 - Convergent Spiral

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
- Uses `Noise_NX_secp256k1_ChaChaPoly_SHA256`  -  ephemeral keys generated in memory at startup
- No certificate files, no key management  -  zero-config encryption
- Works in both single-coin and multi-coin Docker modes
- Each coin gets a dedicated V2 port (V1 port + 1, e.g. DGB: 3334, BTC: 4334)
- Docker is now at full feature parity with native install for single-node deployments

**Dashboard Statistics Chart Grid**
- New 2×2 chart grid showing Pool Hashrate, Network Hashrate, Difficulty, and Workers & Miners  -  each with a current value and time-series chart
- Shared time-range dropdown selector: 15M, 1H, 6H, 12H, 24H, 7D, 30D
- Chart colors are fully theme-aware  -  each of the 23 built-in themes defines its own chart palette via `chart-pool-hashrate`, `chart-network-hashrate`, `chart-difficulty`, `chart-workers` color keys
- Chart colors customizable in the Custom Theme Editor (4 new color pickers: Pool HR, Net HR, Difficulty, Workers)
- Pool Hashrate stat card restored to the stats overview row (first position)

**Activity & Top Block Finders Section**
- Activity Feed and Top Block Finders now displayed side-by-side in a 2-column layout (stacks on mobile)
- Top Block Finders moved out of the Health section into its own dedicated panel
- Leaderboard now consolidates workers that map to the same device  -  e.g. `HashForge` and `HashForge.worker1` are merged into a single entry with combined block count and rewards

**V1.2 Convergent Spiral Codename Theme**
- New release codename theme with its own distinct palette  -  deeper charcoal backgrounds, brighter gold convergence points, stronger amethyst purple accents
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
- `'>' not supported between instances of 'str' and 'int'` when adding stock Antminer to dashboard  -  CGMiner API returns numeric values as strings
- Added `_safe_num()` helper for safe string-to-number conversion across all 11 miner fetch functions: `fetch_antminer`, `fetch_braiins`, `fetch_vnish`, `fetch_luxos`, `fetch_epic_http`, `fetch_axeos`, `fetch_esp32miner`, `fetch_avalon`, `fetch_whatsminer`, `fetch_innosilicon`, `fetch_goldshell`
- Innosilicon firmware confirmed highest risk  -  returns string-encoded values for power, fan speed, temperature, and error codes

**Backup ACL Inheritance**
- New backup files created by cron were not inheriting read permissions for the pool user
- Added default ACL (`setfacl -R -d -m`) in `install.sh` so new files automatically inherit the correct permissions

**Sentinel Backup Status Display**
- Removed `du -sh` size check from backup report section  -  fails with "Permission denied" when pool user lacks recursive read on `/spiralpool/backups/`
- Now displays snapshot count only (`💾 Snapshots: 2`) instead of erroring with a `setfacl` hint

**Theme Mojibake**
- Fixed double-encoded UTF-8 em dashes in `black-ice.json` (name, description) and `bitcoin-laser.json` (description, customCSS)  -  displayed as garbled `â€"` characters

**Spiral Router  -  User-Agent Pattern Cleanup**
- Removed ~70% of miner detection patterns that were dead code  -  matched hardware model names (e.g. "Antminer S19", "Avalon Nano 3S") that manufacturers never include in stratum user-agent strings
- All remaining patterns verified against firmware source code (ESP-Miner, cgminer, bmminer, NerdMiner, etc.)
- `cgminer` and `bfgminer` reclassified from `MinerClassMid` to `MinerClassUnknown`  -  these generic mining clients span a 45,000× hashrate range (GekkoScience 2 TH/s to Avalon A16XP 300 TH/s); vardiff now handles classification, and Sentinel's DeviceHints provides model-specific difficulty for known devices
- Pattern count reduced from ~280 to 47 verified patterns; all 15 SHA-256d and 8 Scrypt difficulty profiles unchanged

**Scrypt Miner Test Accuracy**
- Removed SHA-256d-only miners from Scrypt test suite: `bmminer` (SHA-256d only per bitmaintech/bmminer-mix), `btminer` (MicroBT makes no Scrypt miners), `Braiins OS` (SHA-256d only, no L-series support), `sgminer` (GPU  -  not supported), NerdMiner/ESP32/BitAxe/NerdQAxe (BM-series SHA-256d ASICs)
- Antminer L-series (L3+, L7, L9) correctly identified as sending `cgminer/X.X.X` (per bitmaintech/cgminer-ltc), not `bmminer`
- Algorithm switch test updated to use `cgminer/4.10.1` (real Scrypt firmware UA) instead of `bmminer/2.0.0`

**Sentinel Network Hashrate**
- Sentinel `fetch_network_stats()` QBX section now calls `getnetworkhashps` RPC first, falling back to pool API and formula methods

**Wood Paneling Theme**
- Complete palette rework  -  replaced all-amber/gold colors with walnut browns, copper/burnt sienna accents, cream text, and forest green status indicators

**Avalon Restart Button**
- Avalon/Canaan devices showed a "Restart" button that always failed  -  Avalon firmware does not support the CGMiner `restart` command
- Miner card now shows "⚙ Configure" which opens the Avalon web UI in a new tab; detail modal hides the restart button entirely for Avalon devices
- Removed `avalon` from the CGMiner restart code path in the backend

**Block Celebration Stale Alert**
- Block celebration (confetti/audio) fired for blocks found hours ago after a page reload or service restart  -  `sessionStorage` block count was stale
- Celebrations now only fire for blocks found within the last 5 minutes; older blocks silently update the counter

**Pool Hashrate Farm Fallback**
- Pool Hashrate stat card was falling back to farm hashrate (self-reported by miner devices) when the stratum reported 0  -  displayed wildly inaccurate numbers (e.g. 32 TH/s when actual pool hashrate was 0)
- Removed farm hashrate fallback; pool hashrate now shows stratum-reported value only

**Miners Connected Stat Card**
- "Miners Online" stat card showed a confusing `X / Y` mixing stratum-connected miners with fleet device count, making it look like devices were mining on the pool when they weren't
- Renamed to "Miners Connected" showing only stratum-connected count; fleet device count and average temperature shown as subtitle

**RPC Credential Loading**
- `coin_rpc()` silently returned `None` when RPC credentials were not loaded into `MULTI_COIN_NODES`  -  `load_multi_coin_config()` loads ports and enabled status but not credentials
- `coin_rpc()` now reads credentials directly from the coin's daemon conf file (e.g. `/spiralpool/qbx/qbitx.conf`) as a fallback when credentials are missing

**Network Hashrate History Recording**
- `record_historical_data()` was using the formula (`difficulty × 2³² / block_time`) instead of `_compute_network_hashrate()` which prefers the accurate RPC value  -  chart history oscillated wildly on coins with fast block times
- Now uses `_compute_network_hashrate()` for consistent RPC-backed values in both live display and chart history

**Codename Theme Switching**
- V1.2 Convergent Spiral theme was missing from the `themeColors` JavaScript object  -  selecting it cleared the previous theme's customCSS but applied no new colors until the API fetch completed, making the theme appear broken
- `phi-forge.json` was incorrectly overwritten with Convergent Spiral data  -  the V1.1 Phi Forge codename theme was lost
- Restored `phi-forge.json` as V1.1 Phi Forge; created `convergent-spiral.json` as V1.2 Convergent Spiral with its own distinct palette (deeper backgrounds, brighter gold convergence, stronger purple)
- Both codename themes now have instant-switch entries in `themeColors` alongside V1.0 Black Ice

**Version String Consistency**
- 21 stale `1.2` references (missing `.0` patch) found and fixed across 19 files  -  script variables, Docker labels, display banners, and documentation taglines now all read `1.2.0`
- Affected: `install.sh` (3), `docker/Dockerfile`, `scripts/spiralctl.sh`, `scripts/linux/blockchain-export.sh`, `scripts/linux/blockchain-restore.sh`, `scripts/linux/ha-replicate.sh`, `scripts/linux/ha-setup-ssh.sh`, `scripts/linux/update-checker.sh`, `install-windows.ps1`, `dashboard.py`, `dashboard.html`, `upgrade.sh`, `SpiralSentinel.py` (2), `UPGRADE_GUIDE.md` (4), `README.md` (2), and 9 documentation taglines

### Changed

- Dashboard statistics chart period selector changed from button group to dropdown, added 15M and 12H periods
- Added `--chart-pool-hashrate`, `--chart-network-hashrate`, `--chart-difficulty`, `--chart-workers` CSS variable defaults and theme-overridable color keys across all themes
- Responsive rules for statistics chart grid, period dropdown, and activity/leaderboard split layout
- Mobile CSS improvements: statistics chart grid, activity feed, and leaderboard panels now properly sized and readable on mobile and small phones
- All version strings bumped to semver `1.2.0`  -  variables, labels, banners, and documentation taglines across all scripts, Docker, dashboard, Sentinel, and docs
- MOTD command grid column padding widened (24→26 chars) to fix `spiralctl chain export/restore` alignment
- All coin daemon containers now include `"multi"` profile in docker-compose.yml
- Updated docker-compose.yml header to document both single-coin and multi-coin usage
- Removed "Docker limitations" block from docker-compose.yml  -  multi-coin and merge mining are no longer unsupported
- `POOL_COIN`, `POOL_ID`, `POOL_ADDRESS` no longer required in Docker  -  defaults to empty for multi-coin mode
- `.env.example` expanded with full multi-coin configuration section (per-coin enable flags, wallet addresses, merge mining settings)
- Dockerfile description updated from "Single-Coin Mode" to "Single + Multi-Coin Mode"
- `config.docker.template` comments clarified as single-coin only; multi-coin mode generates config programmatically
- Coin daemon config templates (Fractal, Myriadcoin, Namecoin) updated to reference Docker multi-coin mode availability
- `stratum-entrypoint.sh` now branches on `POOL_MODE` with mode-aware validation (single requires `POOL_COIN`/`POOL_ADDRESS`; multi validates at least one coin enabled)

---

## [1.1.2]  -  2026-03-22  -  Phi Forge

> *When the miner speaks, the pool listens.*

### Fixed

**Unknown Miner Difficulty Override**
- ASICs sending empty or unrecognized user-agents (e.g. some Antminer S19 stock firmware) were forced into the "unknown" miner profile with `MinDiff=500 / MaxDiff=50000`  -  far too restrictive for ASIC hardware, preventing vardiff from reaching proper operating difficulty
- Unknown SHA-256d profile widened to `MinDiff=100 / MaxDiff=1000000`  -  vardiff now ramps up naturally to optimal difficulty for any miner class
- When Spiral Router cannot identify a miner, the pool now falls back to the operator's YAML/env config values instead of overriding with hardcoded defaults

**Connection Classifier  -  False PROXY on LAN**
- ASICs on local networks authorize in <5ms, which the timing heuristic misclassified as "automated software (proxy)" at 0.40 confidence
- Timing score reduced from 0.40 to 0.25 for <5ms auth delay; timing analysis now skipped entirely when Level 1 already identified the miner via user-agent

**Docker  -  AsicBoost / Version Rolling**
- `versionRolling` section was completely missing from the Docker config template  -  Vnish firmware reported pool offline because AsicBoost was not advertised
- Now enabled by default: `enabled: true`, `mask: 536862720` (standard BIP320)
- Configurable via `STRATUM_VERSION_ROLLING` and `STRATUM_VERSION_ROLLING_MASK` in `.env`

**Docker  -  Difficulty Environment Variables**
- `STRATUM_DIFF_INITIAL`, `STRATUM_DIFF_MIN`, `STRATUM_DIFF_MAX`, `STRATUM_VARDIFF_TARGET_TIME` were defined in `.env.example` but the config template used hardcoded values  -  operator overrides were silently ignored
- Template now uses `${STRATUM_DIFF_*}` substitution; defaults set in `stratum-entrypoint.sh`

### Changed

- All version strings bumped from 1.1.1 to 1.1.2

### Acknowledgements

- Thanks to **Kamakhu** for reporting the S19/S19K Pro classification bug and providing detailed logs and Docker config that helped diagnose both the difficulty and AsicBoost issues

---

## [1.1.1]  -  2026-03-21  -  Phi Forge

> *Built on what came before. Growing toward phi.*

### Added

**Custom Theme Editor**
- New in-dashboard theme editor panel in the Appearance sidebar  -  create custom themes without editing JSON files
- 13 color pickers: background, cards, 8 accent colors (blue, cyan, purple, pink, orange, yellow, green, red), text primary/secondary, border color
- Border radius selector (Sharp 0px → Extra 16px)
- Live preview  -  all color changes apply instantly as you pick
- Save to browser localStorage  -  custom themes persist across sessions
- Export as `.json`  -  download your custom theme in the standard Spiral Pool theme format
- Import `.json`  -  load any exported theme (or any Spiral Pool theme JSON) directly into the editor
- Custom themes appear in a "Custom" optgroup in the theme dropdown
- Validates imported themes: requires `colors` object with minimum keys (`bg-primary`, `bg-card`, `neon-blue`, `text-primary`)
- Handles localStorage quota errors gracefully ("Storage full  -  export instead")
- Editor pickers auto-refresh when switching themes via the dropdown

**Top Block Finders Leaderboard (Dashboard)**
- New leaderboard widget inside System Health section  -  ranks miners by blocks found with medal icons (gold/silver/bronze)
- Per-coin reward breakdown (e.g. "125.00 BTC + 500.00 NMC") instead of a single total
- Multi-coin support: queries all pools for solo, multi-coin, and merge-mining setups with single-pool fallback
- Blocks with no source attribution are filtered out
- Retroactive  -  pulls all historical blocks from PostgreSQL via the pool API

**Profitability Tracker Module (Sentinel)**
- New `compute_coin_profitability()` and `compute_profitability_rankings()` functions in Spiral Sentinel
- Calculates daily fiat revenue per coin: `(block_reward × blocks_per_day × hashrate) / network_hashrate × coin_price`
- Groups coins by algorithm family (SHA-256d, Scrypt) for profitability ranking
- Module is present in code but **not active**  -  staging for v1.2.0 profit-switching

### Changed

**Theme Quality Overhaul**
- **Phi Forge**: Redesigned  -  all-gold monochromatic palette replaced with gold + amethyst purple accents on dark charcoal background; added visual hierarchy with contrasting secondary color
- **Bitcoin Laser**: Background changed to true black (#050505); secondary accent changed from grey to laser red (#cc2200); stripped to minimal effects for maximalist aesthetic
- **Vaporwave**: Background changed from deep purple (duplicate of Rainbow Unicorn) to dark teal (#0a1018) with sunset horizon glow; primary accent shifted to cyan; completely distinct visual identity
- **Solar Flare**: Background changed from warm brown (duplicate of Autumn Harvest) to near-black (#080808); hotter plasma yellows (#ffee00) for a coronal ejection feel
- **Midnight Aurora**: Background changed from deep purple to neutral dark; primary accent changed from cyan to aurora green (#40d8a0); now green/purple curtain effect, distinct from Ocean Depths' blue/cyan
- **Wood Paneling**: Fonts changed from Playfair Display + Lato (identical to Autumn Harvest) to Libre Baskerville + Source Sans 3
- **Nebula Command**: Display font changed from Orbitron (shared with Cyberpunk) to Titillium Web

**Sentinel  -  Backup Reporting**
- Backup size display now shows actual size instead of `?` when permissions are correct
- Shows "no access" instead of `?` when `Permission denied` is detected  -  diagnosable instead of opaque
- Backup snapshot count added to report: `💾 Size: 3.1M (2 snapshots)`
- Recursive ACL (`setfacl -R`) applied during install so spiralpool user can read backup subdirectories  -  no manual setup needed
- `acl` package added to installer prerequisites

**Dashboard  -  ETB Display**
- Estimated Time to Block now shows minutes when under 1 hour (e.g. "12 minutes" instead of "0.2 hours")

**External Access  -  Rented Hashrate**
- `sharesPerSecond` now configurable in `spiralctl external setup` wizard with tiered options:
  - Small (<10 TH/s): 200/sec, Medium (10–100 TH/s): 500/sec, Large (100TH–50PH): 1000/sec, XL (50+ PH/s): 2000/sec, Custom: 10–100000
- Default `sharesPerSecond` changed from 50 to 500 (Medium tier)
- Cloudflare Tunnel setup now warns that Spectrum (paid add-on) is required for raw TCP proxying
- Documentation updated with Spectrum prerequisite and shares-per-second configuration table

**Go Toolchain**
- Go version updated from 1.25.6 to 1.26.1 across all build paths (go.mod, install.sh, upgrade.sh, Dockerfile, test.sh)
- Minimum build requirement is now Go 1.26.1 (enforced by go.mod)  -  `install.sh` and `upgrade.sh` download Go 1.26.1 automatically from go.dev; existing installs with older Go will be upgraded on next `upgrade.sh` run

### Security

- **Theme CSS injection hardening**: `customCSS` field in theme JSON files is now sanitized before injection  -  `url()`, `@import`, `expression()`, `javascript:`, `-moz-binding`, `behavior:`, and Unicode escape obfuscation are all blocked and replaced with `/* blocked */`
- **CSS variable value sanitization**: all CSS custom property values from theme JSON are validated  -  values containing `url()`, `expression()`, or `javascript:` are rejected before `setProperty` to prevent data exfiltration via computed styles
- **Imported theme confirmation prompt**: importing a `.json` theme that contains `customCSS` now shows a confirmation dialog with a preview of the CSS  -  operator can cancel to apply colors only without the custom CSS

### Fixed

- Backup script permissions: added `chown -R root:spiralpool` step so Sentinel can read backup sizes
- 7 themes fixed for visual similarity  -  eliminated duplicate-looking pairs across all 23 themes
- Dashboard "Miners Online" display could show numerator exceeding denominator (e.g. 8/7) during stratum reconnection spikes  -  clamped to `min(realtime, configured)` so the count never exceeds the fleet total; also fixed unclamped workers count in hashrate subtitle

**`upgrade.sh`  -  Service Status Display**
- Post-upgrade service status check ran immediately after `systemctl start --no-block`, showing services as `inactive` / `deactivating`  -  added 10-second wait before verification and 5-second wait before summary display
- Summary now shows contextual note when services aren't yet active: "Services may take up to 30 seconds to fully start" with a re-check command

**`upgrade.sh`  -  API Key Migration**
- Admin API key grep patterns required double-quoted values (`"\K[^"]+`); unquoted YAML values (valid syntax) silently failed, causing the upgrade to generate a new API key instead of preserving the existing one
- Fixed all 6 grep patterns (Fix 6, Fix 7, Fix 8) to accept both quoted and unquoted values (`"?\K[^"\s]+`)

**`upgrade.sh`  -  Go Download Hang**
- Go 1.26.1 download used `curl -fsSL` (silent mode)  -  a ~150MB download with no progress output appeared to hang indefinitely
- Fixed: removed `-s` flag, added `--connect-timeout 15` and `--max-time 300`, added "Downloading Go 1.26.1" log message; also fixed in `test.sh`

**Notification Formatting  -  Discord / Telegram**
- All maintenance-mode, HA, and update-checker notifications used literal `\n` in double-quoted bash strings  -  Discord and Telegram displayed `\n` as text instead of newlines
- Fixed: all notification messages now use `printf -v` to produce real newline characters
- Node identifier in notification footers changed from truncated UUID (`Node: 8990382...`) to hostname (e.g. `spiralpool-qbx-109`)  -  consistent with Sentinel's existing approach

**Dashboard  -  Coin Daemon Version Display**
- Dashboard showed incorrect version for daemons with broken `subversion` strings (e.g. Q-BitX reports `0.1.0` regardless of installed version)
- Fixed: dashboard now reads from version cache (`/spiralpool/config/coin-versions/<COIN>.ver`) when available, which reflects the actual installed binary version

**Documentation  -  Lottery Miner Support**
- README now lists NerdMiner, NM Miner, and other ESP32-based lottery miners as supported hardware
- Explicitly noted support for any Stratum V1-compatible device regardless of hash power

**Documentation  -  `git clone` Instructions**
- All user-facing `git clone` instructions now use `--depth 1` to skip git history (~29MB), reducing download size to source files only (~16MB)

---

## [1.1.0]  -  2026-03-19  -  Phi Forge

> *Convergent difficulty. Minimal oscillation.*

### Added

**Q-BitX (QBX)  -  New Coin Support**
- Full native support for Q-BitX (QBX): SHA-256d, 2.5-minute (150s) block time, 12.5 QBX initial block reward, halving every 840,000 blocks
- QBX added to Spiral Sentinel monitoring  -  all lookup tables, hashrate crash thresholds, swap alert eligibility, and payout monitoring
- QBX wallet address validation: P2PKH (`M`-prefix, version `0x32`), P2SH (`P`-prefix, version `0x37`), post-quantum Dilithium (`pq`-prefix)
- QBX difficulty profiles added to Spiral Router (SHA-256d device classification)
- QBX standalone  -  no merge mining (no AuxPoW)

**Installer  -  Native Existing-Install Detection**
- `detect_existing_native_install()`  -  new function mirrors the existing Docker detection path; reads `/spiralpool/config/coins.env` on re-run, detects which coins are already enabled, and presents a clear menu:
  - `[1] Add coins to existing installation`  -  loads all existing RPC passwords, pool addresses, and wallet addresses; skips prompts for already-configured coins; preserves DB password and admin API key
  - `[2] Fresh installation`  -  clean run, no state carried forward
- `coins.env` now persists per-coin RPC passwords and pool addresses for all 13 coins so they can be recovered on re-run without user re-entry
- Multi-coin address collection blocks now guard against overwriting existing wallet addresses  -  if an address is already present from a previous install, it is preserved silently and the prompt is skipped

**`spiralctl coin enable`  -  Add Supported Coins**
- New `spiralctl coin enable <TICKER>` command to add any of the 14 natively supported coins
- Launches the installer in "Add coins to existing installation" mode  -  handles daemon install, wallet generation, config.yaml, firewall ports, and service restart automatically
- After enabling, the Dashboard at `/setup` auto-detects the new coins and shows wallet inputs
- `spiralctl coin disable <TICKER>` stops and disables a coin daemon (wallet and blockchain data preserved)
- `spiralctl add-coin` is now explicitly for **custom/unsupported coins only** (advanced)
- `spiralctl add-coin <TICKER>` still guards against built-in tickers and redirects to `coin enable`

**`add-coin.py`  -  Scope Clarification**
- Module docstring and usage examples updated to explicitly state this tool is for **NET NEW coins only**  -  coins not natively supported by Spiral Pool
- Built-in coin list displayed prominently in help output
- Examples updated to use placeholder tickers instead of natively-supported coins

**`spiralctl coin-upgrade`  -  Coin Daemon Upgrade Utility**
- New `coin-upgrade.sh` script and `spiralctl coin-upgrade` subcommand for in-place coin daemon binary upgrades
- Upgrades the binary only  -  config files, wallets, blockchain data, and pool settings are never modified
- Risk classification per upgrade: `PATCH` (binary swap, reindex not expected), `MINOR` (reindex may be needed), `MAJOR` (reindex almost certainly required)
- `--check` flag shows current vs target version status with no changes made
- `--coin <TICKER>` targets a specific coin; `--reindex` starts the daemon with `-reindex` after upgrade
- Operator-initiated only  -  never triggered automatically by `upgrade.sh` or Sentinel

**ntfy Push Notifications**
- New notification channel: [ntfy](https://ntfy.sh)  -  free, no-account mobile/desktop push notifications
- Configure with `ntfy_url` (full topic URL) and optional `ntfy_token` for private/self-hosted topics
- Wired into `send_notifications()` alongside Discord, Telegram, and XMPP  -  participates in retry logic and fallback logging
- Block found embeds include an ntfy Action button ("View Block") linking to the block explorer when available
- install.sh notification setup now includes an ntfy configuration step

**Block Explorer Links**
- Block found Discord notifications now include a **View Block** field with a link to the canonical block explorer for each coin
- Discord embed title is also a hyperlink (clickable in Discord client)
- Explorer URL is passed as an ntfy Action button for one-tap mobile access
- Per-coin explorer map: BTC → mempool.space, BCH/LTC/DOGE/SYS → blockchair.com, DGB → digiexplorer.info, NMC → bchain.info, FBTC → fractalbitcoin explorer; coins without public explorers (BC2, XMY, PEP, CAT, QBX) show no link

**Installer  -  Consolidated Sentinel Configuration Menu**
- All Sentinel configuration (alerts, health monitoring, reports, update mode) is now presented as a single interactive toggle menu instead of 3–4 sequential question screens
- 11 items in one view: master alerts switch, 7 individual alert types (dry streak, difficulty change, disk space, BTC mempool, backup staleness, sats surge, wallet drop), health monitoring, report frequency, and update mode
- When master alerts is toggled OFF, items 2–8 are greyed out with a note that they are muted  -  no false impression of individual control while the master switch suppresses everything
- Report frequency cycles through three states: `4x Daily` → `1x Daily` → `Off`
- Update mode cycles through: `Notify Only` → `Auto-Update` → `Disabled`
- Per-alert preferences are written directly into `config.json` at install time; Sentinel respects them immediately with no manual config editing required
- New config keys written at install time: `sats_surge_enabled` (default `true`) and `wallet_drop_alert_enabled` (default `true`)  -  previously these alert types were always on with no per-install control

**Installer  -  Notification Setup UX**
- Each notification channel (Discord, Telegram, XMPP, ntfy, SMTP) now gets its own dedicated full-screen section with a clear header  -  terminal is cleared between each channel so output from the previous section does not crowd the next
- Fleet configuration (expected hashrate prompt) also gets its own cleared screen
- Alert theme description updated to accurately name all five supported notification channels instead of only "Discord/Telegram"

**Cloud Deployments  -  Hardening**
- **Individual risk acknowledgment gates**: cloud installs now require typing `YES` to each of the five risks separately (ToS violation, account termination / data loss, provider access to credentials and disk, bandwidth billing, IPv6 disabled at kernel level)  -  a single combined gate was replaced with per-risk prompts
- **Legal terms YES gate on cloud**: cloud operators must type `YES` (non-cloud: `I AGREE`)  -  consistent with the per-risk prompts; `--accept-terms` CLI flag removed (all risk acknowledgment is now manual and interactive)
- **Risk 5  -  IPv6 disabled**: explicit acknowledgment added; IPv6 is disabled at the kernel level (`/etc/sysctl.conf`) because it causes kernel routing cache corruption during keepalived VIP failover operations
- **HA forced to standalone on cloud**: selecting HA Primary or HA Backup on a cloud provider now auto-reverts to Standalone with an explanation; cloud provider networks block VRRP (keepalived) multicast/broadcast required for VIP failover
- **Tor disabled on cloud**: Tor is automatically disabled on cloud installs (most provider AUPs prohibit Tor; it also doesn't protect against provider hypervisor access  -  the primary cloud threat)
- **ZMQ bindings hardened**: all `zmqpubhashblock`, `zmqpubrawtx`, and `zmqpubrawblock` daemon config entries changed from `tcp://0.0.0.0:PORT` to `tcp://127.0.0.1:PORT`  -  ZMQ is a local IPC channel between the daemon and stratum; it never needs to be reachable from outside the server
- **Prometheus metrics loopback-only on cloud**: port 9100 is restricted to `127.0.0.1/::1` on cloud (UFW); the cloud provider's "local subnet" is a shared tenant network, not a trusted private network
- **Wallet security warning**: cloud installs show a red warning before wallet address collection explaining that `wallet.dat` written by "Generate one for me" (option 2) stores unencrypted private keys on provider-managed disk  -  operators are directed to use a hardware wallet address (option 1)
- **Credentials security notice**: post-install completion shows a red notice instructing operators to copy the admin API key offline, delete `credentials.txt`, and clear terminal history; swap-to-disk risk and auto-reboot behavior also documented here
- **Swap security**: 4 GB swapfile creation now logs a cloud-specific warning that in-memory credential data can be written to swap on provider-managed disk; documented in `CLOUD_OPERATIONS.md`
- **Auto-reboot notice**: `unattended-upgrades` auto-reboot at 04:00 UTC is logged as a cloud-specific warning with instructions to disable if desired; documented in `CLOUD_OPERATIONS.md`
- **SSH tunnel for dashboard**: cloud completion output replaces the direct dashboard URL with SSH tunnel instructions (`ssh -L 1618:localhost:1618 user@server`); port 1618 is intentionally closed in UFW on cloud
- **API port annotation**: cloud completion output annotates the pool API URL as world-accessible (intentional  -  public pool stats) with a note that admin routes require the API key
- **CLOUD_OPERATIONS.md expanded**: new sections added for IPv6, HA not supported, wallet security, ZMQ/RPC port security, credentials security, swap security, automatic reboots, and PostgreSQL data durability; post-install checklist updated with all new items
- **`--simulate-cloud <provider>` flag**: test flag added to simulate cloud install paths on local VMs without a real cloud provider

**Documentation**
- `docs/setup/UPGRADE_GUIDE.md`  -  new upgrade guide covering all coin types, merge mining compatibility, database migration analysis (zero new migrations in v1.1.0), and all `upgrade.sh` flags

**Sentinel  -  New Monitoring Alerts**
- **Dry streak alert**: fires when no block has been found in `dry_streak_multiplier × ETB` (default 3×). Configurable via `dry_streak_enabled` / `dry_streak_multiplier`. Cooldown 6h.
- **Network difficulty change alert**: fires when difficulty drifts ≥ `difficulty_alert_threshold_pct` (default 25%) from the baseline at last alert. Comparison is against the previous alert baseline, not tick-to-tick  -  prevents constant noise on per-block difficulty coins (DGB, DOGE). Configurable via `difficulty_alert_enabled` / `difficulty_alert_threshold_pct`. Cooldown 1h.
- **Disk space monitoring**: checks `/`, `/spiralpool`, `/var` (configurable via `disk_monitor_paths`). Enabled via `disk_monitor_enabled` (default true). Warning at `disk_warn_pct` (default 85%), critical at `disk_critical_pct` (default 95%). Per-path cooldowns: 1h warning, 5min critical.
- **BTC mempool congestion alert**: fires when Bitcoin mempool exceeds `mempool_alert_threshold` transactions (default 50,000). Configurable via `mempool_alert_enabled` / `mempool_alert_threshold`. Cooldown 1h.
- **Stratum-down alert**: fires via `send_notifications()` (bypasses quiet hours) when the pool API has been unreachable for 5+ minutes. Clears automatically with a recovery notification when the pool comes back online.
- **Backup staleness alert**: fires when the newest backup in `/spiralpool/backups/` is older than `backup_stale_days` (default 2 days). Only active when `/etc/cron.d/spiralpool-backup` exists (i.e., user opted in during install). Cooldown 24h.
- **Config validation → Discord**: at startup, if `validate_config()` finds any issues (placeholder wallets, invalid URLs, etc.), a yellow warning embed is sent immediately after the startup summary. Fires once per Sentinel restart.

**Sentinel  -  Intel Report Enhancements**
- **Per-coin ETB** (Expected Time to Block): shown in the NETWORK section of 6h/daily reports below the difficulty line. Displays as days, hours, or minutes depending on magnitude.
- **Per-miner health score**: each miner line in the RIGS section now includes a colour-coded health score (💚 ≥90, 💛 ≥75, 🔴 <75).
- **Backup status field**: when the backup cron is installed, intel reports include a `💿 BACKUPS` field showing last backup timestamp, age, total size, and the cron schedule.

**Sentinel  -  Scheduled Maintenance Windows**
- New config key `scheduled_maintenance_windows`: a list of time windows during which non-critical alerts are suppressed
- Each window supports `start`/`end` times, optional `days` list (0=Monday), and overnight ranges
- Scheduled reports and `block_found` always go through regardless of maintenance windows

**Sentinel  -  HA Blip Suppression**
- Role change alerts (`ha_demoted` / `ha_promoted`) are now suppressed for brief keepalived VRRP election blips
- Changed from cycle-based debounce (one 30s poll) to **timestamp-based debounce**: a role change must hold for `ha_role_change_confirm_secs` (default 90s) before an alert fires
- If the node reverts to its original role within the window (at any point), the blip is silently suppressed with a log entry
- Configurable via `ha_role_change_confirm_secs` in `config.json`

**spiralctl  -  Status Command Improvements**
- **Service uptime**: each service line in the SERVICES section now shows how long the service has been running (e.g. `up 3d 2h 15m`)
- **Miner connection ports**: MINER CONNECTION section moved to immediately after SERVICES (was at the bottom), so port addresses are visible without scrolling
- **Scheduled Tasks section**: new section at the bottom of `spiralctl status` showing the backup cron schedule and next PG maintenance timer run
- **Pool version**: version line shown at the top of the SERVICES section (read from `$INSTALL_DIR/VERSION`)
- **Sentinel version**: when Sentinel is running, its version string is queried from the health endpoint and appended to the Sentinel uptime line (e.g. `up 2h · v1.1.0-PHI_FORGE`)
- **Alert pause status**: if Sentinel alerts are paused, an ALERT STATUS section appears showing time remaining and reason with a tip to run `spiralpool-pause resume`

**spiralctl  -  Version Command Improvements**
- `spiralctl version` now shows a full version table: spiralctl, stratum binary (from `spiralstratum --version`), Sentinel, and all installed coin daemon versions

**Installer  -  PostgreSQL Auto-Maintenance Timer**
- `setup_pg_maintenance()`: installs a weekly systemd timer (`spiralpool-pg-maintenance.timer`, Sunday 03:00) that runs `VACUUM ANALYZE` on all pool tables
- Safely skips on Patroni replicas (`pg_is_in_recovery()` check prevents conflicts with streaming replication)
- Timer is `Persistent=true`  -  runs missed schedule after downtime on next boot
- Deployed by both `install.sh` and `upgrade.sh`

**Installer / Backup  -  Backup Integrity Verification**
- Daily backup script now verifies each `.sql.gz` dump with `gzip -t` after creation
- Generates `sha256sum` checksums for all backup files
- Sends a Discord notification (via webhook from Sentinel config) on backup completion or failure

**Documentation  -  Single-Operator Architecture Notice**
- New warning added to `install.sh` legal acceptance screen (red box before `I AGREE` prompt)
- New section "Single-Operator Architecture  -  Wallet Control" added to `WARNINGS.md`
- New `TERMS.md` Section 5E: Single-Operator Architecture  -  explicit legal acknowledgment
- `README.md`: operator notice added to the What Is Spiral Pool? section
- `docs/reference/MINER_SUPPORT.md`: prominent notice at top for miners connecting to operator-run pools

**Email / SMTP Notifications**
- New notification channel: SMTP email  -  send alerts to any email address via any SMTP server (Gmail, Outlook, self-hosted)
- Configure via `smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_to` in `config.json`
- STARTTLS (port 587, recommended) and SSL/TLS (port 465) both supported via `smtp_use_tls`
- Multiple recipients supported via comma-separated `smtp_to`
- Credentials stored in `config.json` (chmod 600, spiraluser only)  -  same hardening as Discord webhook and Telegram bot token
- Wired into `send_notifications()` alongside Discord, Telegram, XMPP, and ntfy  -  full retry and fallback logging
- install.sh notification setup now includes an SMTP configuration step

**Telegram Bot Commands**
- Sentinel now responds to commands sent to the configured Telegram bot:
  - `/status`  -  pool overview (coins, connected miners, hashrate)
  - `/miners`  -  per-miner address, hashrate, and shares/sec
  - `/hashrate`  -  pool hashrate and network difficulty per coin
  - `/blocks`  -  last 5 blocks found per coin
  - `/help`  -  command list
- Runs as a background daemon thread (long-poll `getUpdates`); only responds to the configured `telegram_chat_id`  -  all other senders silently ignored
- Configurable via `telegram_commands_enabled` (default `true` when Telegram is enabled)
- install.sh prompts to enable/disable bot commands when Telegram is configured

**`spiralctl miners`  -  Live Miner Table**
- New `spiralctl miners` command shows all connected miners with address, hashrate, shares/sec, and total shares  -  formatted table, per-coin grouping
- `spiralctl miners kick <IP>` disconnects all stratum sessions from the given IP; miner reconnects automatically on its own reconnect timer
- Kick uses `POST /api/admin/kick` (admin API key required from `config.yaml`)

**`spiralctl miner nick`  -  Miner Nickname Management**
- `spiralctl miner nick <IP> <name>`  -  set a display name for a miner in Sentinel
- `spiralctl miner nick list`  -  list all configured nicknames
- `spiralctl miner nick clear <IP>`  -  remove a nickname
- Edits `config.json` directly via Python; prints restart reminder

**`spiralctl config validate`  -  Dry-Run Config Check**
- `spiralctl config validate` checks both `config.yaml` (stratum) and `config.json` (Sentinel) for issues without restarting any services
- Checks: YAML/JSON syntax, placeholder wallet addresses, invalid notification URLs, SMTP completeness, `check_interval` sanity
- Also accessible as `spiralctl config validate` (added as a subcommand of `config`)

**`POST /api/admin/kick`  -  Stratum Kick Endpoint**
- New admin API endpoint: `POST /api/admin/kick?ip=X.X.X.X` (requires `X-API-Key` header)
- Closes all stratum sessions matching the given IP; returns `{"ip": "...", "kicked": N}`
- Used by `spiralctl miners kick`; also callable directly from scripts or monitoring tools

**Sentinel  -  Zombie Miner Kick-First Remediation**
- Zombie miner handling now uses a two-stage escalation: **kick stratum session first**, only escalate to a full miner reboot if the zombie condition persists 15 minutes after the kick
- Kick forces an immediate stratum reconnect (~5 seconds) without a 2-minute power cycle  -  resolves most zombie cases caused by stale connections
- If the kick resolves the issue, no reboot is triggered; if the zombie persists, Sentinel escalates and reboots as before
- Share rejection spikes now also trigger a stratum kick on first detection (forces reconnect + difficulty re-negotiation without a reboot)

**`spiralctl config notify-test`  -  Notification Channel Test**
- New subcommand: `spiralctl config notify-test` sends a test message to every configured notification channel and reports pass/fail per channel
- Covers Discord, Telegram, ntfy, SMTP email, and XMPP  -  shows ` -  not configured` for channels not set up
- Eliminates the need to wait for a real alert to verify notification delivery

**`spiralctl config validate`  -  Expanded Checks**
- Admin API key cross-check: warns if `pool_admin_api_key` in sentinel config does not match `admin_api_key` in `config.yaml`  -  a silent mismatch caused all stratum kick calls to fail with 401
- Telegram completeness: warns if `telegram_bot_token` is set without `telegram_chat_id` or vice versa
- XMPP completeness: warns if any of `xmpp_jid` / `xmpp_password` / `xmpp_recipient` are set without the others
- `pool_api_url` format check: warns if the value is not a valid HTTP/HTTPS URL

**`spiralctl log errors`  -  Per-Service Filter**
- `spiralctl log errors [service] [window]` now accepts an optional service name to scope output to a single service
- Aliases: `stratum`, `sentinel`, `dash` / `dashboard`, `patroni` / `postgres` / `pg`, `ha` / `watcher`
- Examples: `spiralctl log errors sentinel`, `spiralctl log errors stratum 24h`

**Telegram Bot  -  `/uptime` Command**
- New bot command `/uptime` reports Sentinel process uptime and stratum service uptime (via `systemctl show`)
- Added to `/help` listing

**`upgrade.sh`  -  Post-Upgrade Config Validate**
- `spiralctl config validate` now runs automatically at the end of every upgrade, after the summary, to surface any key mismatches or placeholder values introduced by config migration

**Telegram Bot  -  `/pause` and `/resume` Commands**
- `/pause [minutes]`  -  pause non-critical Sentinel alerts for N minutes (default 30, max 1440). Writes the same pause file as `spiralctl pause` and `spiralctl maintenance on`. Shows time remaining in confirmation.
- `/resume`  -  cancel an active pause immediately and restore alerts. Reports if already unpaused.
- Both commands added to the `/help` listing

**`spiralctl config validate`  -  v1.1.0 Alert Config Range Checks**
- Added sanity checks for all new v1.1.0 alert configuration keys:
  - `disk_warn_pct` must be less than `disk_critical_pct`
  - `dry_streak_multiplier` must be ≥ 1
  - `difficulty_alert_threshold_pct` must be between 1 and 100
  - `backup_stale_days` must be ≥ 1
  - `mempool_alert_threshold` must be ≥ 100

**Installer  -  Coin Daemon Configuration Hardening**
- `dbcache` minimum raised to 4,096 MB for all coins (8,192 MB for BTC, BCH, and DGB)  -  a ceiling applied during IBD to reduce disk I/O; coins that already had a higher value are unchanged
- `dnsseed=1` enabled on all clearnet (non-Tor) coin configs for fast peer discovery

**Installer  -  DNS Seeds Verified and Updated**
- DNS seed lists reviewed and updated for all 14 supported coins (BTC, BCH, DGB, BC2, LTC, DOGE, DGB-SCRYPT, PEP, CAT, NMC, SYS, XMY, FBTC, QBX)
- Stale or defunct seeds removed; active seeds confirmed

**Installer  -  Multi-Coin RAM Warning**
- RAM warning block added to the multi-coin selection flow  -  calculates minimum required memory for the selected coin combination and warns the operator if available RAM may be insufficient for concurrent initial sync

**Installer  -  Per-Coin CLI Address Flags**
- `install.sh` now accepts per-coin wallet address flags: `--ltc-address`, `--doge-address`, `--bc2-address`, `--nmc-address`, `--qbx-address`, `--pep-address`, `--cat-address`, `--sys-address`, `--xmy-address`, `--fbtc-address`
- Enables fully non-interactive deployments and automated re-installs with pre-supplied addresses for all coin types

**Installer  -  `--version` Flag**
- `install.sh --version` prints the installer version string and exits  -  useful for scripted pre-flight checks and automated provisioning workflows

**`spiralctl`  -  Automatic Pool User Elevation**
- `spiralctl` commands that operate on pool files and services are now automatically re-executed as `spiraluser` via `sudo -u` when invoked as root or another user
- Eliminates "permission denied" errors when operators run `spiralctl` as root

**MOTD  -  Consistent Column Alignment**
- Login MOTD redesigned with uniform column spacing  -  service status, command grid, and coin list use fixed-width `printf`-padded columns throughout
- Status icons and color codes decoupled from column width calculation; padding computed in plain variables before color embedding  -  eliminates display misalignment caused by invisible color escape bytes being counted as printable width
- All section dividers unified to 90 characters; section labels removed for a cleaner layout
- `spiralctl coin-upgrade` replaces the old `coin-upgrade.sh` reference in the command grid
- Version string updated to `V1.1.0  -  PHI FORGE EDITION`

**Docker  -  ntfy and SMTP Environment Variable Support**
- `docker/.env.example`: added `NTFY_URL`, `NTFY_TOKEN`, `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_TO` fields
- `docker/docker-compose.yml`: ntfy and SMTP vars now passed through to the Sentinel container
- `SpiralSentinel.py`: all 8 variables added to `env_overrides`  -  Docker deployments can configure ntfy and SMTP via environment variables without editing `config.json`
- Docker installer (single-coin and multi-coin paths) now includes ntfy and SMTP configuration prompts

**Documentation  -  Sentinel Configuration Reference Expanded**
- `docs/reference/SENTINEL.md`: 15 previously undocumented configuration keys added with descriptions, types, defaults, and examples
- `scheduled_maintenance_windows` format documented with `start` / `end` / `days` / `reason` field descriptions
- ntfy (`ntfy_url`, `ntfy_token`) and SMTP (`smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_from`, `smtp_to`) added to the environment variables table for Docker operators

### Security

**Stratum  -  `POST /api/admin/kick` Input Validation**
- The `ip` query parameter was passed directly to `KickWorkerByIP` without validation  -  a crafted value could match unintended sessions via prefix matching
- Fixed: strict IP format validation via `net.ParseIP()` applied before the call

**Sentinel  -  SMTP No TLS Certificate Verification**
- Both STARTTLS and SMTP_SSL paths used the default (unverified) context, leaving email credentials exposed to MITM on untrusted networks
- Fixed: `ssl.create_default_context()` used for both paths  -  verifies cert chain and hostname

### Fixed

**Sentinel  -  Zombie Miner Kick-First Remediation  -  Inverted Escalation Logic**
- The two-stage escalation condition was backwards: the `else` branch (kick age < 15 min, i.e., kick just happened) was triggering an immediate miner reboot on the very next monitoring cycle (~30 seconds after the kick)
- Fixed: proper three-state check  -  `last_kick == 0` kicks, `kick_age < window` waits, `kick_age >= window` escalates

**Sentinel  -  Telegram Message Truncation**
- Messages truncated at exactly 4096 bytes could be cut mid-MarkdownV2 escape sequence, causing Telegram to reject the entire message with a 400 parse error
- Fixed: truncates at 4000 characters and appends `...` leaving room for a clean escape boundary

**Sentinel  -  Health Server Thread Exits Permanently on Error**
- If the health endpoint port was already in use at startup, or if `serve_forever()` encountered an unexpected exception, the background thread exited silently and the `/health` and `/cooldowns` endpoints became permanently unavailable
- Fixed: retry loop with 30-second backoff restores the endpoint once the port clears

**Sentinel  -  Alert Deduplication After Quiet Hours**
- `update_available` and `missing_payout` alerts were silently dropped instead of being re-queued when they fired during quiet hours
- Fixed: suppressed alerts are now correctly re-delivered after quiet hours end

**Stratum  -  `client.reconnect` Params Field**
- `BuildReconnect` emitted `"params": null`  -  some mining firmware rejects non-array `params` in stratum JSON-RPC
- Fixed: `"params": []`

**`spiralctl config list-cooldowns`  -  Port Hardcoded**
- The Sentinel health port was hardcoded to 9191, ignoring the `sentinel_health_port` value in `config.json`
- Fixed: port read from `config.json` at runtime, with 9191 as fallback

**`spiralctl log errors`  -  Subcommand Consumed as Window Argument**
- `spiralctl log errors 24h` passed `"errors"` as the window argument, failing the `^[0-9]+[smhd]$` validation  -  the command was effectively unusable with a time argument
- Fixed: `"errors"` subcommand is consumed before the window is parsed

**`spiralctl config validate`  -  Config Path Interpolated into Python String**
- The YAML syntax check used `open('$config_yaml')` inside a `-c` string  -  a config path containing a single quote would break the Python expression
- Fixed: path passed via `sys.argv[1]` through a heredoc

**`_send_cooldowns`  -  Dict Iteration Race**
- `state.last_alerts` was iterated directly while the monitor loop could be writing to it, risking a `RuntimeError: dictionary changed size during iteration`
- Fixed: snapshot copy taken before iteration

**Sentinel  -  `difficulty_alert_threshold_pct` Fallback Default Mismatch**
- `check_difficulty_changes()` called `CONFIG.get("difficulty_alert_threshold_pct", 10)` while the `DEFAULT_CONFIG` dict sets the key to `25`  -  the safety-net fallback and the real default were out of sync
- Fixed: fallback changed to `25` to match the documented and intended default

**Sentinel  -  `hashrate_crash` Cooldown Not Applied in DEFAULT_CONFIG**
- CHANGELOG documented the cooldown increase from 1 hour to 6 hours, and the comment was updated, but the actual value in `DEFAULT_CONFIG["alert_cooldowns"]["hashrate_crash"]` was never changed from `3600`  -  existing installs without a custom `config.json` override would still get 1-hour cooldowns
- Fixed: value corrected to `21600`

**Telegram Bot  -  `/pause [minutes]` Argument Never Parsed**
- `_handle_telegram_command` normalized `cmd` with `.split("@")[0]`, which preserved the full text including arguments  -  `"/pause 30"` stayed `"/pause 30"`, so `if cmd == "/pause":` never matched when arguments were present; `/pause 30` fell through silently to "Unknown command" and bare `/pause` was the only form that worked
- The handler also referenced an undefined `text` variable for argument splitting, which would raise `NameError` on execution
- Fixed: normalization now extracts just the command word (`raw_text.split()[0].split("@")[0]`); the `/pause` handler reads `raw_text` for argument parsing

**install.sh  -  New v1.1.0 Alert Threshold Keys Missing from Generated `config.json`**
- Fresh installs wrote the boolean enable/disable flags for new v1.1.0 alert features but omitted the corresponding threshold values (`dry_streak_multiplier`, `difficulty_alert_threshold_pct`, `disk_warn_pct`, `disk_critical_pct`, `mempool_alert_threshold`, `backup_stale_days`, `ha_role_change_confirm_secs`, `scheduled_maintenance_windows`)  -  Sentinel used its `DEFAULT_CONFIG` fallbacks correctly, but the generated `config.json` was incomplete
- Fixed: all 8 threshold keys now written with their defaults during installation

**Sentinel  -  Disk Space, Difficulty, and Dry Streak Alerts Silently Blocked for Second Resource**
- `check_disk_space` tracks per-path cooldowns (`"disk_critical:/"`, `"disk_critical:/spiralpool"` etc.) before calling `send_alert`, but `send_alert`'s internal generic rate limiter re-tracks under the bare key `"disk_critical"`  -  the first path's alert set the generic key, blocking the second path's alert for the entire cooldown period
- Same issue in `check_difficulty_changes` (per-coin pre-check key `"difficulty_change:BTC"` vs generic send_alert key `"difficulty_change"`) and `check_dry_streak` (per-coin `_dry_streak_tracking` vs generic `"dry_streak"` key)
- Fixed: all three functions now pass `state=None` to `send_alert` to bypass the redundant generic rate limiter, since they already manage their own per-resource cooldown tracking

**Installer  -  Wallet Manager Numeric Selection**
- Wallet manager address selection accepted free-form input but failed to map numeric menu choices to the correct wallet entry  -  selecting by number returned an invalid or empty address
- Fixed: numeric input now correctly resolved to the corresponding wallet record before proceeding

**Installer  -  DGB-SCRYPT Not Counted in Multi-Coin Sync Warning**
- `DGB-SCRYPT` was omitted from the post-install sync warning counter  -  the "N coins enabled" message showed a count one lower than the actual number of enabled coins when DGB-SCRYPT was selected
- Fixed: `ENABLE_DGB_SCRYPT` guard added to the counter block

**Installer  -  DGB-SCRYPT `POOL_ADDRESS` Not Inherited from CLI Flag**
- When `--address` was supplied on the command line, the `dgb-scrypt` case in `apply_cli_coin_config()` did not fall back to `CLI_ADDRESS`  -  the address was silently dropped and a manual prompt appeared even in non-interactive installs
- Fixed: `POOL_ADDRESS="${POOL_ADDRESS:-$CLI_ADDRESS}"` added to the `dgb-scrypt` case

**`coin-upgrade.sh`  -  QBX Version Always Reported as `unknown`**
- `qbitx --version` outputs `"Q-BitX daemon version"` with no version number  -  a bug in the QBX binary itself. The version regex (`(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*`) correctly fails to match and falls back to `"unknown"`, causing the version table to always show `unknown` for QBX regardless of what is installed
- Fixed: `get_installed_version()` now checks a version cache file (`$INSTALL_DIR/config/coin-versions/<COIN>.ver`) before running `--version`. After a successful upgrade, the target version is written to the cache when the binary reports `unknown`. Future `--check` runs read the cache and show the correct version.

**`spiralctl coin`  -  `list` Subcommand Missing from Help Text**
- `spiralctl help` displayed `coin [status|disable]`, omitting the `list` subcommand
- Fixed: `show_help()` and the inline `cmd_coin()` fallback both updated to `coin [status|list|disable]`

**`upgrade.sh` Fix 7  -  `admin_api_key` Not Migrated from v1 Config Format**
- v1.0.0 config stored the admin API key as `adminApiKey` under the `api:` YAML section; v1.1.0 stratum reads `admin_api_key` under `global:` only  -  after upgrading, the key was present in the config file but silently ignored by the new binary, leaving admin endpoints inaccessible and stratum kick disabled
- Fixed: `upgrade.sh` Fix 7 now reads `adminApiKey` from the `api:` section (v1 location), injects it as `admin_api_key` under `global:` (v2 location), and logs the migration; if neither location has a value, a new secure key is generated; if `global.admin_api_key` is already present (idempotent re-runs or fresh v1.1 installs), the fix is skipped

**`spiralctl config validate`  -  `wallet_address` Incorrectly Flagged as Missing**
- The validator always flagged `wallet_address` as empty/missing, even when the config intentionally omits it (multi-coin mode, custom coin setups)  -  every validate run showed a spurious warning
- Fixed: an absent `wallet_address` key is now valid; only explicit placeholder strings (`YOUR_DGB_ADDRESS`, `YOUR_ADDRESS`, `PENDING_GENERATION`, or any value containing `YOUR`) trigger the warning

**`spiralctl config validate`  -  `admin_api_key` Not Detected in v1 Config Format**
- The validator checked only for `admin_api_key:` (v2 snake_case)  -  configs upgraded from v1.0.0 that still had `adminApiKey:` (v1 camelCase) in the `api:` section were incorrectly flagged as missing the key
- Fixed: grep pattern updated to `admin_api_key:|adminApiKey:`  -  both formats satisfy the check

**`spiralctl config validate`  -  Sentinel Config Checked When Sentinel Is Not Installed**
- On installations without Sentinel enabled, `spiralctl config validate` attempted to check `config.json` and printed misleading errors about missing Sentinel configuration
- Fixed: Sentinel config block is skipped with an informational message when `spiralsentinel.service` is not enabled

**Dashboard  -  Setup Page Device Type Parity**
- Setup wizard (`/setup`) now shows all 26 individual device type sections, matching the settings page  -  previously only 2 grouped sections (AxeOS and CGMiner API) were shown
- Each device type has its own container, add button, icon, and description
- Device scanner on setup correctly routes discovered devices to their individual sections
- `VALID_DEVICE_TYPES` and `CGMINER_DEVICE_TYPES` sets defined for consistent type handling across all JS functions
- QAxe+ correctly shares the QAxe container (special-cased throughout)

**Dashboard  -  Pool-Specific Statistics**
- "Miners Online" stat card now shows stratum-connected miner count (`pool_connected_miners`) as the primary number, with fleet count as secondary "(Fleet: N online)"  -  previously showed fleet-wide network device count which was misleading for multi-pool operators
- "Pool Hashrate" label replaces "Total Hashrate"  -  value already preferred pool stratum hashrate, but the label implied it was a fleet total
- "Pool Shares" in Lifetime Statistics now reads `pool_accepted_shares` directly from Prometheus (`stratum_shares_accepted_total`)  -  previously showed miner-reported combined total from all pools
- Hashrate sub-text fallback shows pool-connected count instead of fleet count

**Dashboard  -  BitAxe / NMaxe Device Separation**
- "AxeOS / NMAXE Devices" section renamed to "BitAxe Devices" on both setup and settings pages  -  NMaxe has its own dedicated section
- Button labels updated: "Add AxeOS Device" → "Add BitAxe Device"

**Dashboard  -  Theme Ambient Glow Brightness**
- Cyberpunk base CSS ambient glows brightened to match Summer Vibes blending intensity: cyan 0.08→0.22, purple 0.04→0.14, red/orange 0.03→0.10; background grid lines 0.02→0.04
- 8 themes updated: Meltdown, Chrome Warfare, Gruvbox Dark, Black Ice, Nord, Tokyo Night, Dracula, Ocean Depths

**install.sh  -  Scanner BitAxe / NMaxe Separation**
- `detect_miner_type()`: BitAxe variants (Supra, Ultra, Gamma, Hex) now correctly output `axeos` type  -  previously misclassified as `nmaxe` because both shared a single detection branch
- NMaxe detection narrowed to match only `nmaxe` string
- Manual device type selection menu: BitAxe added as option 1 (`axeos`), NMaxe as option 2, all 24 options renumbered with corrected case statement
- Initial `miners.json` template updated from 6 device types to all 26

**Dashboard  -  NerdQAxe++ Missing Temperature, Firmware, Frequency, Voltage, Fan Speed, Pool URL, and Best Difficulty**
- `fetch_axeos()` NMAxe detection (`isinstance(data.get('stratum'), dict)`) was too broad  -  NerdQAxe++ firmware v1.0.36+ includes a `stratum` object in its `/api/system/info` response, causing it to be misclassified as NMAxe
- NMAxe branch reads different field names: `asicTemp` instead of `temp`, `fwVersion` instead of `version`, `freqReq` instead of `frequency`, `fans[0].rpm` instead of `fanspeed`, `hostName` instead of `hostname`, `bestDiffEver` instead of `bestDiff`, `stratum.used.url` instead of `stratumURL:stratumPort`
- All fields returned `0`/`Unknown`/empty, causing the dashboard to show `--` for temperature, firmware, frequency, voltage, fan speed, best difficulty, and pool URL on all NerdQAxe++ devices
- Fixed: NMAxe detection now requires `asicTemp` field presence alongside the `stratum` dict check  -  devices with a `stratum` object but standard AxeOS field names correctly fall through to the standard path

**Dashboard  -  Miners Online Showed Fleet Count Instead of Pool-Connected Count**
- "Miners Online" displayed `totals.online_count` (all configured devices responding on the network) instead of `data.pool_connected_miners` (miners with active stratum sessions on this pool)
- Multi-pool operators saw all 7 network miners as "online" even when only 1 was connected to this pool's stratum

**Dashboard  -  Lifetime Pool Shares Showed Miner-Reported Fleet Total**
- `lifetime.total_pool_shares || lifetime.total_shares` used JS `||` which treats `0` as falsy  -  `total_pool_shares` started at `0` (new field), so it always fell through to `total_shares` (miner-reported combined total from all pools)
- Fixed: uses explicit `> 0` checks and reads `data.pool_accepted_shares` (live Prometheus value) as primary source

**Dashboard  -  90-Second Delay Before Miners Appear After Setup**
- `miner_cache["last_update"]` was initialized to `time.time()` at startup, making an empty cache appear fresh for 90 seconds
- First dashboard load after setup showed "No Devices Configured" until the cache expired
- Fixed: initialized to `0`; config save endpoint also resets to `0` for immediate re-fetch

**Dashboard  -  Settings Gear Icon Not Centered**
- Settings button (`⚙`) used padding-only centering on an `<a>` tag  -  emoji glyph rendered off-center due to uneven Unicode metrics
- Fixed: explicit `display: inline-flex; align-items: center; justify-content: center` with fixed dimensions

**install.sh  -  BitAxe Devices Misclassified as NMaxe by Scanner**
- `detect_miner_type()` lumped BitAxe and NMaxe into a single branch matching `nmaxe|bitaxe|supra|ultra|gamma|hex`  -  all BitAxe variants were tagged `nmaxe`
- Fixed: NMaxe matches only on `nmaxe`; BitAxe variants match on `bitaxe|supra|ultra|gamma|hex` and output `axeos`

**install.sh  -  Manual Device Type Menu Had Duplicate Number and Missing BitAxe Option**
- Menu items 16 and 17 were both numbered `17)` (ebang and gekkoscience); BitAxe (`axeos`) was not listed as a selectable option at all
- Fixed: BitAxe added as option 1, all 24 options renumbered sequentially with matching case statement

**Sentinel  -  `global _stratum_down_alerted` Syntax Error on Startup**
- Redundant `global _stratum_down_alerted` declaration in `check_pool_status()` at line 17977  -  the variable was already declared global at line 17960 in the same function scope
- Python 3 treats a `global` declaration after any use of the variable name in the same scope as a `SyntaxError`, causing Sentinel to crash-loop immediately on startup
- Fixed: removed the redundant `global` statement

**`upgrade.sh`  -  Service Drain Loop Exited Immediately for "deactivating" Services**
- `systemctl is-active --quiet` returns exit code 3 for the `deactivating` state (not just `inactive`)  -  the drain loop's boolean check treated "deactivating" as "not active" and exited at `wait_count=0`
- With the loop exiting immediately, `start_services()` ran against a still-deactivating service, causing stratum and sentinel to fail to start after every upgrade
- Fixed: drain loop now captures the actual state string via `systemctl is-active` and only breaks on `inactive` or `failed`  -  `deactivating` and `activating` states are correctly waited out

**`upgrade.sh`  -  `systemctl is-active` Capture Patterns Incompatible with `set -e`**
- Three locations used `$(systemctl is-active "$service" 2>/dev/null || echo "unknown")`  -  `systemctl is-active` prints its state to stdout even on non-zero exit, so `|| echo` appended `"unknown"` on a new line, producing multiline values that broke status display and comparisons
- Removing `|| echo` fixed the multiline issue but exposed the non-zero exit code to `set -e` (enabled at line 100), which killed the entire upgrade script mid-run
- Fixed: all four locations (drain loop ×2, status verification, final display) now use `svc_state=$(systemctl is-active "$service" 2>/dev/null) || true`  -  `|| true` outside `$()` suppresses `set -e` without appending to stdout

**`upgrade.sh`  -  `migrate_coin_version_cache()` Wrote Target Version Instead of Installed Version**
- When no `.ver` cache file existed (every v1.0 → v1.1 upgrade), the function wrote the v1.1 TARGET version to the cache  -  this made `coin-upgrade.sh --check` report QBX as already at the target version, silently skipping the upgrade
- QBX was especially affected because its `--version` output contains no parseable version number, so the fallback was always used
- Fixed: renamed `_VC_VER` (target versions) to `_VC_PREV` (v1.0 shipped versions) with QBX corrected from `0.2.0` to `0.1.0`; function now tries `--version` detection first and falls back to `_VC_PREV` only when detection fails

**`coin-upgrade.sh`  -  False Version Warning for Daemons Without Parseable `--version` Output**
- Post-upgrade version verification read the stale cache (pre-upgrade version), then compared it against the target  -  QBX always showed `"Binary reports '0.1.0'  -  expected '0.2.0'"` even after a successful upgrade
- Fixed: cache is written with the target version FIRST, then the binary's `--version` is checked directly (bypassing cache); daemons with no parseable version output (QBX) show a success message with "(version cached  -  daemon has no version output)" instead of a false warning

**`coin-upgrade.sh`  -  Garbled Backup Path Display**
- `backup_coin()` used `log_success` (stdout) for progress messages inside a function whose stdout was captured by `backup_path=$(backup_coin "$coin")`  -  log messages were concatenated into the backup path variable
- Fixed: all log messages inside `backup_coin()` redirected to stderr (`>&2`)

**`coin-upgrade.sh`  -  CLI Calls Missing `-conf` Flag (Wrong RPC Port)**
- `wait_for_daemon()` and the reindex monitor hint ran bare CLI commands (e.g. `qbitx-cli getblockchaininfo`) without `-conf=`  -  every coin uses a non-default RPC port, so the CLI defaulted to Bitcoin's port 8332 and timed out after 120s even though the daemon was healthy
- Fixed: added `COIN_CONF` map and `get_coin_cli()` helper; all CLI calls now include `-conf=<path>` matching the patterns in install.sh's `get_cli_cmd()`; multi-disk (`CHAIN_MOUNT_POINT`) supported

**Dashboard  -  Pool Hashrate Showed Farm Hashrate When No Miners Connected**
- "Pool Hashrate" stat card fell back to farm device hashrate (`farmHashrateThs`) when stratum-reported pool hashrate was 0  -  a fresh install with 7 fleet miners configured but none connected to the pool showed 32 TH/s under "Pool Hashrate"
- Fixed: when `pool_connected_miners` is 0, the display shows 0 instead of falling back to farm hashrate

**Sentinel  -  Pool Block Counter Reset After Database Restore**
- `_init_state()` seeded `pool_blocks_found` from the database API, but `load()` ran immediately after and overwrote it with the stale value from `state.json`  -  after a database restore importing historical blocks, Discord notifications showed "Block #17" instead of "#643"
- Fixed: API re-seeding moved into `load()` after state.json is applied; uses `max(state_value, db_count)` so database restores, fresh installs, and normal restarts all produce the correct count

### Changed

- Version strings updated throughout: `1.0.0 / BLACKICE` → `1.1.0 / PHI_FORGE`
- Sentinel `hashrate_crash` alert cooldown increased from 1 hour to 6 hours  -  reduces repeated notifications during sustained network hashrate drops
- HA role change debounce changed from cycle-based (1 × 30s poll) to timestamp-based (configurable, default 90s)  -  suppresses longer VRRP election blips that the old debounce missed
- Dashboard "Total Hashrate" stat label renamed to "Pool Hashrate" for clarity

---

## [1.0.0]  -  BlackICE

> *Initial release.*

### Added

**Core Stratum Engine**
- Stratum V1, V2 (Noise Protocol encryption), and TLS  -  multi-port per coin
- SHA-256d and Scrypt algorithm support with dedicated difficulty profiles per algorithm
- Lock-free share pipeline: ring buffer (1M capacity, MPSC) → WAL → PostgreSQL COPY batch insert
- Per-session atomic vardiff state; asymmetric ramp limits (4× up / 0.75× down); 50% variance floor
- Non-custodial solo payout: block reward embedded directly in the coinbase transaction to the miner's wallet  -  no pool wallet, no intermediate custody, no fees

**Spiral Router  -  Miner Classification**
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
- Advisory lock payment fencing  -  prevents double-payment during failover
- `spiralpool-ha-watcher.service`  -  manages Sentinel start/stop based on HA role

**Spiral Sentinel**
- Autonomous monitoring daemon: device discovery, connection tracking, hashrate monitoring, temperature alerts, block find notifications
- Quiet hours: configurable suppression window (default 22:00–06:00)
- Scheduled reports: configurable intervals plus a final pre-quiet-hours report
- SimpleSwap swap alerts: optional notification when a mined coin rises 25%+ vs BTC over 7 days, with pre-filled conversion link (operator-initiated only  -  no automatic swaps)
- Achievement system, miner nicknames, and historical stats

**Spiral Dash**
- Real-time web dashboard on port 1618
- Multi-theme support
- Per-miner worker statistics, block history, hashrate charts

**`spiralctl` CLI**
- Runtime operator control: coin management, pool status, miner listing, difficulty inspection, maintenance mode, HA management, GDPR/data purge, Tor control
- `spiralctl add-coin`  -  onboarding automation for NET NEW unsupported coins

**Installer (`install.sh`)**
- Two deployment paths: native/VM and Docker bare-metal
- Docker existing-install detection (`detect_existing_docker_install()`)  -  reads `docker/.env`, offers Add Coins vs Fresh Install
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

*Spiral Pool  -  BSD-3-Clause  -  Non-Custodial  -  Solo Mining  -  Proof-of-Work*
