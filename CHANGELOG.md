# Changelog

All notable changes to Spiral Pool are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows `MAJOR.MINOR.PATCH` - patch releases are applied in-place on the same tag.

---

## [v2.7.0] - 2026-08-16 - Spiral Citadel

Chain-identity release. On 8 August 2026 Bitcoin split at block 961,632 over BIP-110 ("RDTS", the Reduced Data Temporary Softfork), and Spiral Pool was shipping a Bitcoin Knots build that enforces it. Every operator who installed or upgraded BTC since roughly 8 May was pointed at a daemon that follows a minority chain which has produced only a handful of blocks in total and whose coins are not traded anywhere. Nothing in the software reports a fault in that state: stratum connects, shares validate, vardiff converges, the dashboard shows healthy hashrate. The only symptom is that blocks never arrive, which for a solo miner is indistinguishable from variance — so the expected value is exactly zero with no way for the operator to find out. This release moves all BTC daemon acquisition to Bitcoin Core, refuses to mine a chain it cannot verify, and repairs nodes already stranded on the wrong one. No database migrations, no config format changes. Drop-in upgrade from v2.6.7.

Also carried on this tag: two miner-classification defects on the stratum side. `MinerClassLottery` was calibrated for hardware roughly four times slower than what actually connects, and NerdNOS was not classified at all — both left a miner holding a difficulty its hardware could not work at, and in neither case could vardiff correct it, because every retarget path runs from an accepted share and these miners land none. Reported against DigiByte, where a 15-second block time scales the profile constants down and turns a merely suboptimal value into a failure. Stratum-side only: **no version string change, no migrations, no config changes** — the pool picks the new profiles up on restart.

### Fixed
- **Three code paths shipped the RDTS-enforcing Bitcoin Knots build, and one resolved the version at runtime so it would have drifted onto an enforcing build regardless of any pin.** `docker/Dockerfile.bitcoin` pinned `29.3.knots20260508` with no checksum verification at all; `install.sh` and `coin-upgrade.sh` pinned the same enforcing build. The enforcing and non-enforcing releases differ by **one digit in a datestamp** — `knots20260508` enforces, `knots20260507` does not — and the datestamp is the enforcement marker rather than a release date, so `29.4.knots20260508` also enforces despite the higher base version. That makes any "latest version" lookup actively dangerous, and two existed. `scripts/linux/pool-mode.sh` scraped the bitcoinknots.org directory listing and took `sort -V | tail -1` **unconditionally**, with no checksum, installing straight to `/usr/local/bin/bitcoind` and writing the version cache — so `coin-upgrade.sh` afterwards believed BTC was legitimately on Knots. `install.sh`'s `detect_latest_knots_version()` was functionally the same scrape but opt-in behind `BITCOIN_KNOTS_AUTO_DETECT=1`, with the pinned version used and logged by default. Run today, either selects an enforcing build deterministically. BTC acquisition now pins **Bitcoin Core 31.1** from bitcoincore.org with SHA256 `b80d9c3e04da78fb6f0569685673418cf686fadba9042d926d13fb87ff503f9e` committed in-repo, taken from `bitcoincore.org/bin/bitcoin-core-31.1/SHA256SUMS` and cross-checked byte-for-byte against two independent Guix attestations (`achow101`, `fanquake`) in `bitcoin-core/guix.sigs`. The previous design fetched the checksum from the same host serving the binary, which one compromised host satisfies on both sides. Verification is a **hard gate with no override** in the four production paths — `install.sh`, `coin-upgrade.sh`, `docker/Dockerfile.bitcoin`, `scripts/linux/pool-mode.sh` — and the `ALLOW_UNVERIFIED_BTC_DOWNLOAD=true` escape hatch is removed. `scripts/linux/regtest.sh` and `tests/test-coin-configs.sh` are repointed to Bitcoin Core 31.1 for consistency but, as before, verify no checksum; they build local throwaway chains and are not a production path. Both resolvers are deleted, not bypassed. Note `install.sh`'s BTC download now has a single source (bitcoincore.org) where Knots had two, so a bitcoincore.org outage fails the install rather than falling back. `upgrade.sh`'s `_VC_PREV` map still carries `29.3.knots20260210` — that is a *previous-version* fallback used to seed the version cache when `--version` parsing fails, not an acquisition path, and the value is a non-enforcing build; leaving it makes BTC correctly show as upgradeable.
- **A BTC upgrade could SIGKILL the operator's Bitcoin Cash or Fractal daemon and corrupt its chainstate.** `ensure_daemon_stopped` identified the process with `pgrep -x`/`pkill -x` against `COIN_DAEMON_CMD[$coin]`, i.e. the `/usr/local/bin` symlink name. But BTC, BCH and FBTC all exec a binary literally named `bitcoind` from different directories (see the three `ExecStart` lines `install.sh` writes for BTC, BCH and FBTC), and `pgrep -x` matches the kernel `comm`, so it cannot tell them apart. `systemctl stop bitcoind.service` stopped BTC only; BCH kept running, so the loop never saw the process disappear and escalated to `pkill` (SIGTERM, attempts 4-10) and then `pkill -9` (SIGKILL, attempts 11-20) — against the **Bitcoin Cash** node, mid-UTXO-flush. Once BCH died the check finally passed and the script reported `bitcoind confirmed stopped`, having killed the wrong daemon and verified nothing; BCH then crash-looped on a corrupted LevelDB until systemd's `StartLimitBurst` latched it off. The mirror-image defect also existed: `COIN_DAEMON_CMD[BCH]="bitcoind-bch"` matches no running process at all, so a BCH upgrade read "confirmed stopped" on attempt 1 while the daemon was still live, and `install_binaries` then copied over a running binary with its `cp` exit status unchecked. This was latent: `COIN_TARGET[BTC]` matched the installed Knots build, so `upgrade_coin` hit its already-at-target early return and `ensure_daemon_stopped BTC` had never executed in production. Retargeting BTC to Core is what makes it reachable. Matching is now anchored to the **resolved binary path** (`pgrep -f "^<path>([[:space:]]|$)"`), and when a path cannot be resolved the function refuses to signal by name if that name is shared with another configured coin, rather than guessing.
- **Swapping the binary is not sufficient to leave the minority chain, and a node that only swaps looks healthier than one that fails.** An RDTS-enforcing daemon marks the majority chain's block 961,632 `BLOCK_FAILED_VALID`, and that status bit is serialised into the block index on disk (`CDiskBlockIndex::SERIALIZE_METHODS` writes `nStatus` verbatim; `LoadBlockIndexGuts` restores it without recomputation). Bitcoin Core reads the same index, honours the inherited verdict, and `FindMostWorkChain` erases the entire branch from `setBlockIndexCandidates` on the flag alone with no work-based override — so the node sits on the minority tip with a far heavier valid chain available and reports nothing wrong. There is exactly one non-test caller of `ResetBlockFailureFlags` in Core, the `reconsiderblock` RPC; no restart hook, no version check, no automatic reconsideration exists. Reproduced on regtest against Bitcoin Core v31.1.0: a node holding a persisted failure flag refused to reorganise onto a chain with three more blocks of work, **still refused after a restart on the same datadir** (the swap), and converged immediately once `reconsiderblock` cleared the flag — with no reindex, no resync, and its pre-split history intact and byte-identical. `coin-upgrade.sh` now verifies `getblockhash 961632` after the swap, sweeps `getchaintips` and reconsiders **every** tip reporting `invalid` rather than one hardcoded hash, then polls for convergence and distinguishes *downloading* from *wedged* before recommending the expensive fallback. Note `-reindex-chainstate` does **not** clear these flags — only a full `-reindex` rebuilds the block index — which is why the `MAJOR` risk class points at the latter. (`coin-upgrade.sh`, `verify_btc_majority_chain`.)
- **`coin-upgrade.sh` reported BTC as up to date and skipped it, so the tool an operator would reach for to fix this told them there was nothing to fix.** `COIN_RISK[BTC]` was `NONE`, meaning "already at target", against a `COIN_TARGET[BTC]` of the enforcing build. `download_BTC()` also went straight from `wget` to `tar -xzf` with no verification of any kind — unlike `install.sh`, which at least attempted it — and hardcoded a `29.x` path segment that would break for any non-29.x version. Now `MAJOR`, targeting Core 31.1, with a mandatory hash gate.
- **Wallet recovery ran the wrong daemon for every coin.** `_recover_salvagewallet` declared its coin→daemon table with `local svc_map=(…)`, which creates an **indexed** array: every `[btc]`-style subscript is arithmetic-evaluated, unset names become `0`, so all fourteen entries collapse onto index 0 and the last one wins. Verified empirically — `btc → ecashd`, `dgb → ecashd`, every key → `ecashd`. Method 4 of the wallet-backup recovery therefore launched the **eCash daemon** pointed at another coin's config and datadir. The `[[ -z "$daemon" ]] && return 1` guard never tripped, because the value was non-empty, merely wrong, and `bash -n` cannot see it — an indexed array with word subscripts is valid syntax. Now `local -A`. Also adds the missing `[cat]` entry, which was absent from the table entirely.
- **The same recovery path passed `-salvagewallet`, removed from Bitcoin Core in 0.19.** Unknown command-line arguments are fatal in Core — this repo documents that model itself — so the daemon exited with a parse error before doing anything, for every Core-derived daemon (BTC, LTC, DGB, SYS, NMC, BCH, BCH2, BC2, BTCS, FBTC, XEC). `2>/dev/null` hid the reason, so the operator saw only `Trying -salvagewallet (BerkeleyDB recovery)... failed`. Combined with the array defect above, method 4 could never succeed for any coin. It now **deliberately does not automate salvage** for modern daemons: `<coin>-wallet salvage` rewrites the wallet file in place, and this code path is only reached because every non-destructive backup already failed — so the wallet is likely damaged and there is no copy. It locates the tool beside the daemon binary, prints the exact commands with a copy-the-wallet-aside step first, and returns failure. The legacy `-salvagewallet` path is retained only for the older daemons (Dogecoin 1.14, PepeCoin 1.1) that still accept the flag, which is the one case that ever worked. The manual-recovery instructions printed on failure were wrong in both halves too: they suggested `-salvagewallet`, and built a config path as `<coin>/<coin>.conf`, which is wrong for every coin whose config is not named after its ticker (BTC's is `bitcoin.conf`, FBTC's is `fractal.conf`). The path is now taken from the CLI invocation rather than reconstructed.
- **`pool-mode.sh` destroyed hand-edited coin configs with no backup.** All thirteen coin branches regenerate their config wholesale with `cat > … << EOF`. Regenerating is intentional — switching pool modes changes ports and merge-mining settings — but any operator customisation (extra `addnode`/`seednode` lines, a raised `dbcache`, custom `maxconnections`, `rpcauth`, `wallet=`, `blocksdir`) was silently and irrecoverably lost, and the only value carried across was pruning, which `get_existing_prune` reads from `coins.env` rather than from the file it was handed. Each branch now takes a timestamped `.bak-<stamp>` copy first, and says so.
- **`upgrade.sh` silently overwrote deliberate `dbcache` and `maxconnections` settings on every run.** The caps exist to stop a node OOMing and remain in force, but an operator who set `dbcache=16384` on a large host had it rewritten with only a `log_info` line and no way back. The edit now takes a timestamped backup of the config before its first change and reports at warning level that an operator value was overridden. (Both edits were already injection-safe — the substitutions interpolate only digits captured by `grep -oP '\K\d+'`, and GNU `sed -i` is temp-file-plus-rename, so neither could corrupt a config or truncate it on an interrupt.)
- **A bare `externalip=` in the Tor config prevented Bitcoin, DigiByte and Litecoin from starting at all.** An empty value is not treated as "unset": the parser stores `""` (`InterpretValue` returns `value ? *value : ""`, and `GetArgs` applies no emptiness filter), the resolve loop passes it to `Lookup()`, which rejects the empty string on an explicit `if (name.empty())` guard, and the `else` branch returns `InitError`. Verified in each project's own source at the exact version installed — Bitcoin Core 31.1 `init.cpp:1803`, DigiByte 9.26.5 `init.cpp:1678`, Litecoin 0.21.5.4 `init.cpp:1510` (the older `bool Lookup` form, same guard) — and independently confirmed fatal in Namecoin 28.0, Syscoin 5.0.5, Myriadcoin 0.18.1.0, Fractal and Bitcoin ABC 0.31.12. With `Restart=always` and `StartLimitBurst=5` in the generated unit this is a crash loop, not a silent failure; it went unnoticed because `TOR_ENABLED` defaults to false. Omitting the option entirely *is* the do-not-advertise behaviour — there is no empty or sentinel form. Ten of the thirteen Tor configs never carried the line, so this restores consistency rather than changing intent. Removal has **no behavioural effect** beyond the daemon starting: `-discover` is soft-set to 0 twice before the `externalip` interaction is reached, once from `-proxy` and once from `listen=0`, and `SoftSetBoolArg` is a no-op once a value is set — so `fDiscover` was already false with the line present. Pre-existing and unrelated to the chain split; surfaced by auditing the configs for the migration. (`install.sh`, BTC/DGB/LTC Tor branches.)

- **Five places parsed `bitcoin.conf` with `grep -oP '^key='`, which is not how Bitcoin Core reads a config file, and one of them could leave a daemon that never starts again.** Core scopes every option under a `[main]`/`[test]`/`[regtest]` header to that network, tolerates whitespace around `=`, and takes the *first* assignment. A bare `^key=` match honours none of that. The worst case was `scripts/linux/pool-mode.sh`: `get_existing_prune` returned **every** match, and the result is interpolated directly into the generated config as `prune=$EXISTING_PRUNE` — so a config with `prune=5000` at the top level and `prune=1000` under `[test]` emitted a bare `1000` line with no `=`, which is a fatal parse error the daemon does not come back from. The same regex missed `prune = 5000` (Core-legal whitespace) and silently rewrote a **pruned** node to `prune=0`, which Core refuses to start against a pruned datadir without a full resync; and it copied a `[test]`-scoped `prune` up to the top level, enabling irreversible block deletion on a full mainnet node — destroying precisely the ability to reorg off the minority chain that this release depends on. `upgrade.sh`'s `rightsize_daemon_resources` had the same defect with a different symptom: a multi-line value made `[[ -gt ]]` throw an arithmetic error, so the `dbcache`/`maxconnections` caps were **silently skipped** and the run reported `All daemon resource limits OK` — a false green on exactly the multi-coin OOM scenario the function exists to prevent. All sites now read the top-level section only, tolerate whitespace, take the first match, strip CR, and accept integers only.
- **`upgrade.sh` appended mainnet peer settings into whatever section a config happened to end in.** `ensure_daemon_peer_config` used `>>`, so on any config ending in `[test]` the `forcednsseed=1` line and all twelve mainnet `addnode=…:8333` entries landed under `[test]`, where mainnet never sees them. Its guard read the whole file, so it then matched the line it had written into the wrong section and reported `All daemon peer configs up to date` on every subsequent run — permanently. The function was added specifically because fresh installs got zero peers; against these configs it never fixed that and said it had. It now inserts before the first section header, and its guards read the top-level section only. `coin-upgrade.sh`'s DGB pruning path had the same shape — file-wide deletes plus an EOF append — and now declines section-bearing configs with manual instructions instead of half-applying an edit and then deleting the transaction index anyway.
- **Two config backups could fail silently and let the edit proceed unbacked-up.** `upgrade.sh`'s `rightsize_daemon_resources` and `coin-upgrade.sh`'s `dgb_enable_pruning_config` both logged a warning on backup failure and continued — overwriting a deliberate operator value, or deleting lines and then removing the transaction index, with no copy on disk. Both now fail closed. Both backups are also `chmod 600` now: these files contain `rpcpassword` in plaintext, and `cp -p` was carrying the original's group-readable 640.
- **A failed daemon start left `coin-upgrade.sh` reporting the coin as healthy.** `systemctl start` was unguarded under `set -e` in both the normal and `--reindex` branches, so a daemon that installed cleanly but failed to start aborted the script mid-statement — skipping `wait_for_daemon`, the config-hazard check, and the chain-identity verification entirely. Because the binary swap had already succeeded, `--check` then read the new version off disk and reported BTC `current` while the daemon was down and the chain had never been verified. In the `--reindex` branch the abort also stranded the one-shot `-reindex` drop-in on disk, which with `Restart=always` is an endless reindex loop. Both are now guarded, the drop-in is always removed, and a failed start reports what did not run.
- **`install.sh`'s offline-install path checked the same location twice and could delete the operator's tarball.** The pre-placed-tarball loop used `$(pwd)`, but `cd /tmp` runs earlier in the same function, so both loop entries resolved to `/tmp` and the documented "drop it next to the installer" location was unreachable — with the failure message printing the identical path twice. `rm -rf /tmp/bitcoin-*` after extraction then matched the staged tarball itself, so the air-gapped workflow worked exactly once. The loop now uses `$SCRIPT_DIR`, cleanup is scoped to the extracted directory, the staging `cp` is guarded (unguarded, it aborted the whole installer under `set -e`), and an unreadable file is reported as unreadable rather than as a checksum mismatch.
- **HA binary replication accepted any binary that was not provably Knots.** The test was a negated grep, so a truncated, wrong-architecture or unexecutable replicated binary produced no output, passed, and got symlinked into `/usr/local/bin` — after which the install reported success and the daemon failed to start. It now requires a positive `Bitcoin Core` identification and discards anything else in favour of the verified download. Relatedly, the Knots probe read only the first line of `--version`; it now reads the whole output, since this is the one check whose failure mode is mining a worthless chain. The `/usr/local/bin` symlinks are also refreshed on the already-at-target path, which previously skipped them — leaving `coin-upgrade.sh --check` reporting BTC as not installed after a repair re-run that was supposed to fix exactly that.
- **The Sentinel chain-identity alert could write thousands of identical ERROR lines a day.** The alert cooldown is deliberately stamped only on successful delivery so a suppressed alert is retried, but the journal line sat outside that gate — so a persistently suppressed alert (quiet hours, maintenance mode, HA replica) logged on every monitor cycle indefinitely. The log line is now throttled on its own latch. The alert also rendered `Block 961,632: unreadable` on the BIP-110-enforcement path, because the block hash was never queried there; enforcement alone is conclusive, but the embed shows the field and "unreadable" reads like the check failed rather than like it found the wrong chain.
- **Smaller correctness fixes.** `install.sh`'s BTC download retry loop reused the variable name `download_with_retry` uses for its own loop, so `max_attempts=3` was really 1 and the retry prompt was unreachable. `pool-mode.sh` wrote config backups keyed on basename alone, so the three coins whose config is named `bitcoin.conf` (btc, bch, xec) overwrote each other's backups; they are now qualified by coin directory. The BTC template emitted no `txindex` line at all, silently dropping `txindex=1` to Core's default of off when a full-archival node's config was regenerated. `assumevalid` was pinned *below* Bitcoin Core 31.1's own shipped value, making initial sync slower than omitting it; the line is removed. `ensure_daemon_stopped`'s fallback matcher could never be reached safely — the guard it depended on compared symlink names that are unique by construction, so it always fell through to a `pgrep -x` that matches the process `comm` and therefore reported BCH as stopped while it was running; the fallback now verifies through systemd and refuses rather than reporting an unverified stop.

- **The version comparison would have silently broken on the next Bitcoin Core release.** Both `coin-upgrade.sh`'s `_ver_matches` and `install.sh`'s already-installed check compared with a trailing `.0` stripped from each side. That is asymmetric: for a future `32.0` target, an installed `32.0.0` strips to `32.0` while the target strips to `32`, so they would not match and every run would re-download and reinstall an already-correct binary — exactly the dead-branch bug the `.0` stripping was added to fix. Both now pad to three components (`X.Y.Z`), which is symmetric. Verified across `31.1.0/31.1`, `32.0.0/32.0`, `31.10.0/31.10` and a genuine mismatch.
- **`install.sh`'s BTC version-cache fallback was unreachable.** `grep … | head -1 || cat BTC.ver || echo unknown` reads as a fallback chain, but a pipeline's exit status is `head`'s, which is 0 even when grep matched nothing — so neither fallback could ever run and an unparseable `--version` yielded an empty string. The result is now tested explicitly.
- **A failed coin upgrade could leave the daemon stopped without saying so.** The upgrade stops the daemon before swapping the binary, but several failure paths — `die` on a malformed archive, an unresolvable binary directory — exit straight through the `EXIT` trap, which cleaned up the work directory and maintenance mode and said nothing about the node being down. The operator saw a one-line error and no indication that mining had stopped. The trap now tracks which service this run stopped, clears it once the daemon is confirmed back up (normal, `--reindex` and rollback paths alike), and otherwise reports the service by name with the commands to inspect and start it.
- **The stratum chain gate failed on a single unreachable RPC call.** The check ran once with a 60-second timeout immediately after the sync gate. Since the sync gate has already proved the daemon answers RPC, arriving here unreachable means a transient blip — a restart, a reloading wallet, a saturated RPC work queue — but the pool refused to start with `CHAIN GATE: refusing to mine`, which reads as *you are on the wrong chain* and sends the operator to `coin-upgrade.sh` for a problem that does not exist. It now retries up to five times at six-second intervals, but **only** when the daemon is unreachable; a `minority` or stale verdict is conclusive and still fails immediately.
- **`TRADEMARKS.md` and `THIRD_PARTY_LICENSES.txt` still credited Bitcoin Knots as the shipped node implementation.** The trademark table listed `Bitcoin Knots | Luke Dashjr | Bitcoin node implementation (Docker image, native installer)` with no Bitcoin Core row, and the third-party licence entry read `Bitcoin Core / Bitcoin Knots`. Neither was true after this release. Both now name Bitcoin Core.
- **`tests/test_upgrade_regression.sh` had an assertion that failed against correct code.** Its build-before-stop ordering check grepped `main()` for `stop_services` and matched the *comment* "Must run BEFORE stop_services" (line 357) rather than the call (line 368), so the ordering compared 363 < 357 and failed. Verified pre-existing by running it against `git show HEAD:upgrade.sh`. It now blanks comment text before matching, which preserves line numbering and also handles `deploy_stratum`, invoked as `$UPDATE_STRATUM && deploy_stratum` rather than as a bare statement. The suite goes from 141 assertions with one failure to 142 passing.

- **Nothing anywhere said that a pool-generated wallet has no seed phrase.** Bitcoin Core and its derivatives have never supported BIP39, so a wallet the installer creates has no 12- or 24-word recovery phrase — the exported backup file is the only copy of the keys that will ever exist. The repo had zero occurrences of "seed phrase", "recovery phrase" or "mnemonic" in any script or document, while the installer told operators to "back up the wallet keys". Someone whose expectations come from Electrum or a hardware wallet reads that, waits for words that never appear, and concludes either that they missed a step or that the pool stores a phrase somewhere. Both conclusions cost them the funds. All thirteen coin wallet prompts now state it **before** the choice is made, both post-generation backup boxes state it again once keys are on disk, and `WARNINGS.md` gains a section covering it along with the restore commands for each backup format. The prompts also now point at the better option explicitly: create the wallet on another machine (hardware wallet, Electrum, Sparrow), keep the seed phrase there, and paste only the receiving address into the installer — so the private keys never touch the pool server at all. That path already existed and was already marked "recommended"; it simply never explained *why*.
- **The wallet-backup reminder is re-emitted during a coin upgrade.** `backup_coin` copies wallets before the binary swap, but that copy is on the same machine — it protects against the upgrade, not against losing the box. Anyone upgrading BTC today generated their wallet months ago and has had no reminder since. The upgrade now says so, repeats that no seed phrase exists, and prints a ready-to-paste `scp` command.

- **The upgrade tool trusted its own version cache over the binary on disk, so it could report a Knots node as current and never replace it.** `coin-upgrade.sh`'s `get_installed_version` returned the contents of `config/coin-versions/<COIN>.ver` unconditionally, consulting the daemon only when no cache file existed. The cache is there for daemons whose `--version` omits a number, but checking it first let it shadow reality for every coin. Concretely: an earlier run writes `BTC.ver=31.1`, then a rollback — or an operator following the restore command this same script prints on failure — puts a Bitcoin Knots binary back on disk. `get_installed_version` still answered `31.1`, `upgrade_coin` took its "already current" shortcut, `--check` printed a green tick, and the node went on following the BIP-110 minority chain. That is the silent failure this release exists to remove, reproduced inside the tool meant to remediate it. The binary is now asked first and the cache consulted only when the binary ran but printed nothing parseable; the version string keeps its build suffix so a Knots build can never compare equal to a Core release.
- **`get_existing_txindex` changed the setting it was written to preserve, in five input classes.** The helper added earlier in this release read the config with shell intuition — `case "$v" in 1|true)` — rather than the daemon's parser. Bitcoin Core and Dogecoin resolve a boolean with `InterpretBool`: an empty value is **true**, and otherwise the result is `atoi(value) != 0`. `atoi("true")` is 0, so `txindex=true` is **false** to the daemon while the helper emitted `txindex=1`; conversely `txindex=`, `txindex=01`, `txindex=2` and `txindex=1#comment` are all **true** to the daemon (the last because `GetConfigOptions` truncates each line at the first `#`) while the helper emitted nothing. On DigiByte's and PepeCoin's pre-0.17 bases a txindex change in either direction is a fatal `You need to rebuild the database using -reindex-chainstate to change -txindex` at startup — precisely the outage the helper exists to prevent, now caused by it. The resolution now mirrors `GetConfigOptions` + `InterpretBool` exactly, including comment truncation and `atoi` semantics, and `tests/test_conf_parser_semantics.sh` asserts all of it.
- **`spiralctl coin prune` reported success while pruning nothing, and left the config unreadable by the daemon.** The command stripped existing `prune=` lines and appended `prune=5000` at end of file. On any config carrying a section header the appended line lands **inside that section**, so mainnet is never pruned; the verification step then grepped the whole file, found the line, and confirmed the false success before restarting the daemon and letting the disk keep filling. The same class had already been fixed correctly in `upgrade.sh` (`_add_toplevel`) and `coin-upgrade.sh` (`dgb_enable_pruning_config`) — `spiralctl` was the third site and had neither guard. It now inserts into the top-level scope ahead of the first section header and verifies the **effective** mainnet value. Two further defects in the same block: no backup was taken (both siblings take one, fail-closed), and `mv` installed a temp inode created under `umask 077` by root, leaving the config `root:root 0600` while the daemon runs as the pool user — which then could not read its own config on the restart three lines later. Ownership and mode are now carried across with `chown/chmod --reference`.
- **An existing config no longer inherits the pool-wide prune target.** With `PRUNE_ENABLED=true`, a config whose prune value was unparseable (`prune=`, `prune=abc`) or absent fell through to the global default of 5000. The daemon reads all of those as 0 — archival — so regeneration wrote `prune=5000` onto a node that had been keeping full block data all along and began deleting it irreversibly on next start. An existing config is now authoritative; only a coin with no config yet takes the pool-wide default.
- **Wallet backup was skipped for every coin on a multi-disk install, and marked confirmed so it would never retry.** The guard tested `$INSTALL_DIR/<coin>/`, but `get_blockchain_dir` returns `$CHAIN_MOUNT_POINT/<coin>` when the installer has placed chains on separate disks. On such a host the directory never exists, the guard was always true, and the installer logged "address was manually provided, no pool wallet on server", wrote a `.backup-confirmed-<coin>` marker, and moved on — for all 15 coins. A freshly generated wallet's only copy of its keys was therefore never written anywhere, and the marker stopped a re-run from noticing. Every path in that block, including each coin's CLI invocation, now resolves through `get_blockchain_dir`.
- **`-chain=main` was fatal to every Bitcoin Cash RPC call.** `bitcoin-cli-bch` is a symlink to Bitcoin Cash Node's `bitcoin-cli`, and BCHN never took the Core 0.17 refactor that introduced `-chain`; its `ParseParameters` rejects an unregistered command-line argument outright with `Invalid parameter -chain`. The same key had already been removed from BCH's config file in this release, but one CLI definition kept it, so BCH address validation and wallet operations exited non-zero. Mainnet is BCHN's default and the flag is simply gone. The coins that legitimately keep `chain=main` — BTC, BC2, BCH2, BTCS — are all Core-derived and do register `-chain`; each was checked against its own upstream rather than assumed.
- **The Bitcoin Cash II Docker image could not be built at all.** `docker/Dockerfile.bitcoincashii` requested `bitcoincashII-v${VERSION}-linux-x86_64.tar.gz`; the release publishes eight assets and that is not one of them. The real x86-64 Linux asset is `bitcoincashII-27.0.2-linux64.tar.gz` — no `v` prefix, `linux64` rather than `linux-x86_64` — so `wget` 404'd and the build aborted, which also made this release's BCH2 `-datadir`/`-conf` fix unreachable code. The comment claiming the URL had been "verified against the releases page" was false.
- **Every consensus daemon image now verifies a SHA256, not just Bitcoin.** `Dockerfile.bitcoin` fails the build closed on a checksum mismatch and argues in-repo that shipping an unverified consensus daemon is unacceptable; the five other coin images verified nothing but `tar -tzf`, which is a gzip-integrity check that a substituted or tampered asset passes unchanged — and four of those five had their versions bumped in this same release. LTC and SYS are pinned to digests that match the publishers' signed `SHA256SUMS.asc`; XEC's digest was confirmed by fetching the identical artifact from two independent hosts (github.com and download.bitcoinabc.org); BCH and BCH2 publish no plain-URL checksum file, so those digests are computed from the release asset and are labelled in-file as change-detectors rather than authenticity attestations, so nobody later mistakes one for the other.
- **Four images advertised the version they shipped *before* the bump.** `LABEL version` was hardcoded and read 29.0.0/0.31.12/0.21.5.4/5.0.5 against actual contents of 29.1.0/0.33.10/0.21.5.6/5.1.0, so `docker inspect` and any image inventory reported the wrong daemon. All five coin images now interpolate the build ARG the way `Dockerfile.bitcoin` already did, which makes the drift structurally impossible.
- **A bare `read` at five retry prompts killed the installer outright on any non-interactive run.** `install.sh` runs under `set -e`; at EOF `read` returns 1 and terminates the script. Under Ansible, `ssh host 'sudo bash install.sh'`, cron, or a piped script, a single transient download failure ended the install silently and mid-way — in exactly the unattended runs the retry exists to serve. Bitcoin's prompt had been guarded earlier in this release; DigiByte, Bitcoin Cash, Bitcoin II, Bitcoin Cash II and Go had not. All five now test for a tty, read from `/dev/tty`, and retry automatically when there is no terminal.
- **`download_with_retry` overwrote its callers' loop counter.** The function declared six locals but not `url` or `attempt`, and five callers loop on a bare `attempt`. The callee left it at its own maximum, so a caller's `max_attempts=3` was really 1 and its retry branch was unreachable. Fixed at the root with `local url attempt` rather than by renaming the variable in each caller.
- **The Docker and PostgreSQL apt sources now refuse to be written with an empty codename.** `${DOCKER_DISTRO}` and `${OS_CODENAME}` are exported by `scripts/linux/detect-os.sh` and validated by `require_supported_os()` before any install step runs, so in practice they are always set — but nothing checked, and an empty value would have produced `https://download.docker.com/linux//gpg` and a sources.list line with no distro and no codename, which apt rejects as malformed. Both call sites now fail with an explanation instead. (An earlier draft of this entry claimed the variables were never assigned at all; that was wrong — it came from grepping `install.sh` without reading what it sources.)
- **The health monitor watched 13 of 16 daemons.** Bitcoin Cash II, Bitcoin Silver and eCash were absent from `check_blockchain_health`, so those three could stay down indefinitely with no alert while every other coin was checked. Service and CLI names were taken from the sibling call sites in the same file that already handle them.
- **Bitcoin Cash II's CLI was invoked under a name that is not on `PATH`.** `install_bitcoincashii` creates only the lowercase symlink `bitcoincashii-cli`; the capital-`II` binary exists solely inside the install directory. Three sites invoked the capital-`II` name bare, so BCH2 always read as unsynced and its wallet backup failed.
- **The legacy-wallet check failed open, and did its thorough check only when it knew least.** `coin-upgrade.sh` piped `getwalletinfo` straight into `grep -q '"descriptors".*false'`; when the RPC produced no output at all — a transient error, a wallet that failed to load, a daemon shutting down — grep simply found no match and the wallet was classified as descriptor and safe. Separately, the magic-byte scan of wallet files on disk ran **only** when RPC was unavailable, while the RPC-up path consulted `listwallets`, which reports only *loaded* wallets: an unloaded legacy `wallet.dat` was reported all-clear precisely when the daemon was healthy enough to be upgraded. The disk scan is ground truth and needs no daemon, so it now runs on both paths, an unreadable file is no longer counted toward an "all descriptor" result, and an unreadable `getwalletinfo` is treated as legacy.
- **`pgrep` returning "command not found" was read as proof the daemon had stopped.** `if ! pgrep …` negates exit 127 into success, so on a host without procps `ensure_daemon_stopped` reported a confirmed stop and the caller overwrote the binary under a live daemon — and because `rm -f` unlinks an open file happily, the "Text file busy" backstop never fired either. The function now refuses outright if `pgrep` is unavailable and distinguishes "ran, matched nothing" from "failed to run".
- **The multi-disk guard failed open.** `check_multi_disk_layout` read `CHAIN_MOUNT_POINT` with a pattern that matched none of the shapes `coins.env` legitimately takes — an `export ` prefix, leading whitespace, single quotes, spaces around `=`, CRLF — and an empty read means "single disk, proceed". A hand-edited `coins.env` therefore silently disabled a guard whose failure mode is a full resync of every coin. It now uses the same tolerant reader as the sibling in the same file.
- **`_conf_int` skipped the OOM cap on any value written with an explicit `+`.** Core parses these with `atoi`, so `dbcache=+16384` is 16384 to the daemon; the reader returned empty, the caller treated the key as absent, and the cap it exists to apply was never written. Also in `upgrade.sh`, a bail-out mid-loop skipped the ownership fix at the end of that iteration, leaving a root-owned `0600` config behind if an earlier edit in the same pass had already succeeded.
- **The DigiByte preserve-prune reader was not section-aware.** A bare `^[[:space:]]*prune=` would copy a `[test]`-scoped value up as the mainnet target, and rejected `prune = 5000` and `prune=+5000` — both of which Core accepts — so an already-pruned node read as unpruned and had the 5000 default written over its real target.

- **The script-integrity guards were unrunnable and, once they did run, wrong.** GUARD 1 forked `grep`+`sed`+`sort` for every line of every `install_*` body — roughly 24,000 processes — and never finished on a developer machine, so the guard written to catch the exact class of bug that shipped in this release could not be executed at all; it is now a single `awk` pass and completes in about four seconds. GUARD 2 was a quote-parity heuristic that reported dozens of healthy lines (`tr -d '"'"'"` and `sed "s/'/''/g"` are ordinary shell with an odd number of single quotes) and would have buried a genuine hit; it is now a real quote-state scanner that tracks single/double/escape state across the file and skips heredoc bodies, which are data rather than shell. Both were then verified by planting the original defects — the `$BTC_DIR`-inside-`install_digibyte` leak and a `sed` program broken across a line — and confirming each guard fails on them while `bash -n` still reports the file as clean.
- **`test_config_safety.sh` never exercised the branch where a wrong answer destroys data.** The harness pinned `SPIRALPOOL_DIR` to a nonexistent root, so `PRUNE_ENABLED` was always `"false"` and all 48 assertions ran with a starting value of 0 — the setting under which a parse failure is harmless. Both prune defects found in this pass lived only in the `true` branch and passed the suite by never being reached. It now runs both settings.
- **`tests/test_conf_parser_semantics.sh` (new, 51 assertions).** Executes the four config readers that rewrite coin configs — `get_existing_txindex`, `get_existing_prune`, `_conf_effective`, `_conf_add_toplevel` — plus the multi-disk guard's reader, against the daemon's documented parsing rules: `atoi` boolean resolution, `#` truncation, `[main]`-beats-top-level, first-wins within a scope, the pre-0.17 bases' lack of section support, CRLF, and files with no trailing newline. Suite totals for the release are now 316 assertions across five shell suites, all passing.

- **Five defects introduced by this remediation pass itself, found by an adversarial re-review and fixed.** `upgrade.sh`'s `_add_toplevel` replaces the config inode, and the owner/mode repair sat at the end of the caller's loop behind a counter that only increments when a whole three-call `&&` chain succeeds — so a failure in the second or third call left the config already replaced, root-owned and unreadable by the daemon, with the repair skipped; ownership is now carried inside the helper, before the `mv`, where no caller can skip it. `coin-upgrade.sh` captured pgrep's status as a bare command followed by `$?`, which is fatal under `set -e` on the common "daemon already stopped" result — inert today only because both callers happen to invoke it as a condition. `spiralctl`'s new pre-write backup was never removed on any exit path, leaving a file holding `rpcpassword` beside the config after every run, and its `umask 077` wrapper was inert because `cp -p` restores the source's mode afterwards. The `/etc/os-release` probes inherited the subshell's exit status, so a missing file killed the installer under `set -e` before the explanatory error could print. And `_conf_effective` returned the same empty string for "key absent" and "key present with an empty value" — which are opposites, since `InterpretBool` reads an empty value as true — so `txindex=` read as off and pruning was enabled without disabling the index; a new `_conf_bool` applies the daemon's own boolean rules.
- **The eCash health check was added with the wrong calling convention and would have restarted a healthy daemon three times an hour.** `install.sh` has two conventions for the same argument: `check_blockchain_sync` invokes `$cli` unquoted so a two-word string word-splits, while `check_blockchain_daemon_health` invokes `"$cli"` quoted. The eCash entry copied `ecash-cli -rpcport=9004` from the unquoted call site into the quoted one, so the shell would look for a single executable with a space in its name, exit 127, and — since 127 matches none of the startup-tolerance patterns — log "not responding", sleep, retry identically, and restart a perfectly healthy `ecashd`, permanently, while adding 60 seconds of dead sleep to every monitor cycle. The port was never needed on the command line. A new guard in `tests/test_script_integrity.sh` fails if any multi-word CLI reaches a quoted invocation.
- **The no-seed-phrase notice now appears on all 30 wallet-generation offers, not 13.** An operator who accepted "Generate one for me" at one of the other 17 was never told that no 12/24-word recovery phrase exists and that the backup file is the only copy of the keys. Eleven pre-existing notice blocks were also indented inconsistently with their surroundings and are now aligned.
- **`coin-upgrade.sh` addressed Bitcoin Cash II by a CLI name that is not on `PATH`.** `COIN_CLI_CMD[BCH2]` held the capital-`II` binary name, but `install.sh` only ever creates the lowercase `bitcoincashii-cli` symlink — the fourth instance of that casing defect, in the one file the other three were not in.
- **Bitcoin Silver would have reported "update available" forever.** BTCS is pinned to a source commit (`source-<40 hex>`), which a compiled binary cannot report from `--version`, so the version cache is the only record of what is installed. Making the binary authoritative for every coin — the fix that stops a stale cache hiding a Knots build — would have broken exactly that case. Source-commit pins keep reading the cache; every version pin asks the binary. Both directions are asserted in `tests/test_coin_config_upgrade.sh`.
- **`get_existing_txindex` ignored Core's `no` negation prefix.** `notxindex=0` means txindex **on**; the reader saw no `txindex=` line, reported the setting absent, and emitted nothing — the same fatal mismatch on a pre-0.17 base whose index is built.
- **Two comments written during this release cited the wrong upstream versions.** `-chain=<chain>` was attributed to the Core 0.17 refactor; it first appears in v0.19.0. DigiByte Core was described as 0.21-derived; 9.26.x ships `common/args.cpp`, `util/chaintype.h` and `kernel/chainparams.cpp`, none of which exist before Core 26. Both conclusions were independently correct, but a comment stating the wrong base version is what a maintainer would trust when deciding whether some other era-specific behaviour applies. DigiByte's own `init.cpp:959` ("DigiDollar requires -txindex=1") is now cited as the reason DGB asserts the index.
- **The four ragged warning boxes in `upgrade.sh` are square.** Eleven rows were 1–4 columns short of their box, measured as rendered terminal columns rather than string length, so the right-hand border stepped in and out down the box.

- **`_conf_bool` and `get_existing_txindex` disagreed on five inputs despite being documented as sharing semantics.** Core supports a `no` negation prefix, so `notxindex=0` means the transaction index is **on**; only the `pool-mode.sh` reader handled it, while `spiralctl`'s reported the key absent. The consequence is concrete: `spiralctl coin prune` would not fire its txindex guard, would write `prune=5000` beside a live index, and the daemon aborts on the restart that follows. Two halves to the same hole — even a firing guard was a no-op, because the `sed` that comments the line out could not match a `notxindex=` line either. Both are fixed, and the suite now runs a **differential** check across 19 inputs asserting the two readers agree; unlike an assertion that expects an empty string, that comparison cannot pass against a gutted implementation, which was verified by mutation.
- **The backup taken before enabling pruning was deleted on the success path.** The `RETURN` trap added to stop backup files accumulating fired on every exit — including seconds after the config had been rewritten and the daemon restarted into irreversible block deletion, which is precisely when the backup is the only way back. It is now discarded only on the failure paths, where the config was never modified and the copy is genuinely worthless, and the success path prints where it was kept.
- **An empty version-cache file made `get_installed_version` return an empty string** rather than one of its sentinels, reachable through a truncated write (disk full, or a crash mid-`echo >`). Now tested with `-s` rather than `-f`.
- **Wallet classification no longer prints a shell warning for every legacy wallet it finds.** A Berkeley DB wallet begins with NUL bytes, and redirecting stderr *inside* the command substitution cannot suppress `ignored null byte in input` — the shell emits it while performing the substitution. The NULs are now stripped before the substitution sees them; classification is unchanged, verified against real BDB and SQLite magic-byte fixtures.
- **The wallet-backup warning box is square for every coin symbol.** One row embeds `${_wg_coin}`, which runs from 3 characters (`BTC`) to 10 (`DGB-SCRYPT`), against a hardcoded run of spaces — so the border overflowed for the long symbols and fell short for the short ones. The padding is computed from the symbol length; verified at 67 columns for the shortest and longest.

- **`get_existing_prune` ignored Core's `no` negation prefix, on the one setting where a wrong answer destroys data.** `noprune` was not recognised at all, so the *next* `prune=` line in the file was taken as the first assignment — which is not what the daemon reads. Core resolves a negated option to a bool and flips it back on a double negative, so `noprune=1` is prune 0 while `noprune=0` and `noprune=false` are both prune **1**, never the value's digits. The two boolean readers had already been corrected for this; the integer one had not.
  Found by a differential fuzz built for this release: 1,500 randomly generated configs — comments, sections, duplicate keys, CRLF, whitespace, negation prefixes, values like `+5000`, `5000M`, `007`, `true` — run through the real shell readers and compared against a model of Core's parsing rules written independently from `common/config.cpp`, `common/args.cpp` and `common/settings.cpp` rather than transliterated from the code under test. `get_existing_txindex`, `_conf_bool` and their agreement with each other diverged on **0** inputs; `get_existing_prune` diverged on **158**, every one containing `noprune`. After the fix all four measures are 0, confirmed on a second, unseen seed.
- **The config readers and the guards are now covered by measurement, not just assertions.** A mutation campaign reintroduces each real defect this release fixed — the version cache shadowing a Knots binary, the section-blind prune write, the dropped negation, empty-value-reads-as-false, `[main]` precedence, the missing `local url attempt` — into a throwaway tree and checks that the suites fail. The campaign refuses to run at all unless the unmutated baseline is green, which caught an incomplete tree copy that would otherwise have made every mutant look caught. `tests/test_conf_parser_semantics.sh` also gained a **differential** section asserting the two boolean readers agree across 19 inputs; unlike an assertion that expects an empty string, it cannot pass against a gutted implementation, verified by mutation.

- **The Bitcoin Cash II container built successfully and could not run.** Three independent defects, each hiding the next. The download URL requested an asset that does not exist, so the image had never been built by anyone. With that corrected it builds and passes its checksum — and then every binary dies at exec, because bitcoincashII v27.0.2 ships DYNAMICALLY linked binaries (unlike the self-contained Guix builds the other coins publish) and the image installed none of `libevent`, `libevent_pthreads`, `libminiupnpc`, `libnatpmp` or `libsqlite3`. With those added it still fails: the binaries need libminiupnpc soname **17**, and ubuntu:26.04 ships only `libminiupnpc21` with no 17 available at any version. This one image is therefore pinned to `ubuntu:24.04`, which provides the full unresolved set. Symlinking soname 21 to 17 was rejected — a soname bump signals an ABI break, so it might appear to work and fail later under load.
  The image now also **fails its own build** if either binary has an unresolved library or cannot execute `--version`. That gate exists because a green build, a passing SHA256 and a correct release URL were all true of an artifact that crash-looped on first start: a successful build proves the download and the checksum, and says nothing about whether the daemon runs. Found only by running `ldd` inside the built image on a real Linux host.

- **The bare-metal installer shipped older daemons than the release declared current, on four coins.** The version bumps in this release reached the Docker images and `coin-upgrade.sh` but not `install.sh`, so a fresh install delivered BCH 29.0.0, LTC 0.21.5.4, SYS 5.0.5 and XEC 0.31.12 while `coin-upgrade.sh --check` called 29.1.0 / 0.21.5.6 / 5.1.0 / 0.33.10 current. LTC forced the issue: this project's own upgrade matrix classifies 0.21.5.6 as **consensus-critical** — a soft-forking rule active at mainnet height 3,154,440, a height that has already passed — so a fresh install put Litecoin on a node missing an active rule, structurally the same failure this release exists to remove for Bitcoin. All four are now aligned.
  Litecoin was in fact wrong in **four** places at once: the installer fetched 0.21.5.4, the version cache was seeded `0.21.4`, `coin-upgrade.sh` targeted 0.21.5.6, and `tests/test-coin-configs.sh` downloaded and booted 0.21.4 — so every green result for LTC validated software no operator would ever receive. `BCHN_VERSION` and `LITECOIN_VERSION` were `local` to their install functions, which is why the version-cache seeding hardcoded literals instead of referencing them; both are now global constants and the seeding uses the variables. A new guard in `tests/test_script_integrity.sh` compares each coin's version across all four files and fails on any disagreement, verified by reintroducing the LTC drift and confirming it is caught.

- **The installer waited ten minutes per coin for an RPC it had made itself unable to reach, then blamed the daemon.** `start_services`' `wait_for_daemon` polls `$cli_cmd -conf="$conf_path" getblockchaininfo` as the **invoking operator**, but every coin config is written `chmod 640` owned by `$POOL_USER` — deliberately, because it contains `rpcpassword` in plaintext. The comment on that `chmod` reads "640 allows group read for CLI tools", which only holds if the operator is a member of the `spiraluser` group, and a normal `sudo ./install.sh` run from a personal account is not. So `bitcoin-cli` exited on `specified config file … could not be opened` before it ever opened a socket, the loop failed all 300 attempts, and the installer reported `daemon still not answering RPC after 10 minutes` — against a daemon that was healthy the entire time, RPC listening on `127.0.0.1:8332`, `NRestarts=0`, 42 MB resident. The message then sends the operator to look at the daemon, which is the one place the problem is not. With sixteen selectable coins this is up to **two and a half hours** of dead waiting on a correct install, and because `wait_for_daemon` returns 1 rather than aborting, wallet and address setup proceeded against an RPC the installer believed was down. `check_coin_sync` and `get_coin_sync_progress` had the identical defect, which is why sync progress rendered as `unknown`/`initializing...` indefinitely. All three now invoke the CLI as `sudo -u "$POOL_USER"` — the same pattern the wallet-generation path beside them already used correctly, which is what makes this a divergence rather than an oversight in the design. The config permissions are deliberately **not** widened: 644 would fix the symptom by publishing the RPC password. Verified by classifying every CLI call site in `install.sh` by execution context — the three inside the generated sync-monitor heredoc run as root under systemd and were already fine. Found by the first end-to-end `install.sh` run on real hardware; it is not reachable from Cygwin, where POSIX modes are not represented.

- **`Pool` and `Coordinator` stored a context cancel function and never called it.** Both `Run` methods derive a cancellable context (`ctx, p.cancel = context.WithCancel(ctx)`) and assign the cancel func to a struct field that nothing in the tree ever invokes — verified by grepping every `.cancel` reference across the module. Every sibling component does call it: `CoinPool` at `coinpool.go:2301`, and likewise `VIPManager`, `ReplicationManager`, `NodeManager`, the scheduler and the failover manager. These two were the only exceptions. **This was not a production fault** and is recorded here for completeness rather than as an outage avoided: `Run` blocks on `<-ctx.Done()` and the parent is the process-lifetime context created in `main`, so real shutdowns propagate correctly and nothing leaked past process exit. What it cost was the ability to stop either type without cancelling the parent, and it was invisible to `go vet` — `lostcancel` does not track a cancel func stored in a struct field, which is also why it survived every prior audit. Both now `defer` the cancel immediately after deriving the context. The change is a no-op on the normal path (the context is already cancelled by the time `Run` returns) and on the error paths it stops the subsystems that did start — the share pipeline, job manager and block notifications can all be up before the stratum-server failure return — where previously they were left running on a live context while `Run` returned an error. `main` treats that error with `log.Fatalw`, which exits the process, so neither path can change behaviour an operator would observe. Found by triaging the GitHub code-scanning backlog (gosec G118); it was the only genuine finding in 308 open alerts.

- **A single lowercase letter in a config would have silently disabled the chain gate — the feature this entire release exists to add.** `CoinPool` stored `coinSymbol` verbatim from the config (`coinSymbol: cfg.CoinConfig.Symbol`), and three sites compare it against an uppercase literal **case-sensitively**: `verifyChainIdentity` (`coinSymbol != "BTC"`), the eCash RTT header handling (`== "XEC"`) and the Fractal difficulty clamp (`== "FBTC"`). Meanwhile the config layer tolerates any casing everywhere else — `Validate()` calls `strings.ToUpper(coin.Symbol)` at each of its checks and keys its duplicate-detection map `// upper → original`, explicitly acknowledging that the original casing varies. So a config reading `symbol: btc` passes validation cleanly, and then `verifyChainIdentity` returns `nil` on its first statement: **no verdict, no refusal, no log line**, and the pool mines the BIP-110 minority chain exactly as if the gate had never been written. `install.sh` writes uppercase (`- symbol: "BTC"`) and the V1→V2 converter was verified on real hardware to do the same, so no stock install was ever affected — a hand-edited config was, and this is a project whose configs invite hand-editing. V1's equivalent guard already used `strings.EqualFold`; V2 was the outlier. `coinSymbol` is now normalised with `strings.ToUpper` at construction, which fixes all three comparisons at once and brings V2 to parity with V1. Note the symbol is also a Prometheus label and a log field, so a deployment that *did* carry lowercase symbols will see those label values change case once — for uppercase configs, which is every generated one, nothing changes at all. Found while verifying that the chain gate could not fire on a production DigiByte pool before upgrading it.

- **The `coin-upgrade.sh` banner printed a broken border.** The version line carried no trailing padding, so `V2.7.0-SPIRAL_CITADEL║` ran straight into the box edge, and the `To ADD a coin` line was one column short. Every other line in that box pads to the border's 62-column interior. Both now do too, verified by measuring each `║…║` line against its own box's `═` count rather than assuming a single width — the file contains two box widths and a naive check flags the wider one falsely. `upgrade.sh` was checked the same way and is clean.

- **`MinerClassLottery`'s `InitialDiff` was calibrated for hardware roughly four times slower than what actually connects, and block-time scaling multiplied the error until modern ESP32 boards were unusable on fast chains.** The value was `0.001`, which at the profile's 60s target implies a ~72 KH/s miner (`0.001 x 2^32 / 60`). The comment beside it did the arithmetic for the class the profile is named after — `500 KH/s x 60s / 2^32 = 0.007` — and then set a value seven times lower. Field-measured boards run 250-430 KH/s: an ESP32-D0WD "CYD" NerdMiner at ~430 KH/s, NMMiner on ESP32-S3 at ~390 KH/s, LilyGo T-Display-S3 and T-Dongle-S3 at ~250 KH/s each. On Bitcoin's 600s blocks that mismatch is merely wasteful. On DigiByte `scaleProfilesForBlockTime` multiplies both `TargetShareTime` and the difficulty constants by `15/60`, giving an assigned difficulty of `0.00025` — at which a 430 KH/s board produces a share roughly every 2.5 seconds. The reporter observed 66 sessions over 6 hours producing 10,894 stale shares and **zero** accepted, with `banning.invalidSharesThreshold` tripping within minutes of each reconnect. `InitialDiff` is now `0.004` (~286 KH/s at the 60s target — the middle of the measured range rather than either end), scaling to `0.001` on DigiByte. **`MinDiff` is deliberately unchanged at `0.0001`.** It binds only on miners vardiff has already walked down, so the raise proposed in the report (`0.002`) would have lifted the slowest supported device in the class from ~7 KH/s to ~143 KH/s, stranding the Arduino (~1 KH/s) and LeafMiner (~3 KH/s) boards that share this tier behind the same `(?i)arduino` and `(?i)leafminer` patterns. Note the reject mechanism proposed in the report does not hold on inspection: `cleanJobs` is set only on a new block, not on rebroadcast, and DigiByte retains 24 jobs (~120s) against a 60s `maxJobAge`, so a share submitted 2.5s after its job was issued is well inside the acceptance window. The calibration defect is real and is what this fixes; the residual **0 accepted** figure is not explained by pool-side job handling, and if rejects persist the remaining cause is there rather than in difficulty. (`spiralrouter.go`, `DefaultProfiles[MinerClassLottery]`.)
- **NerdNOS matched no user-agent pattern and fell through to `MinerClassUnknown` at difficulty 10000 — roughly 60x too high for the hardware.** The NerdNOS is an add-on board that attaches to a NerdMiner and lifts it to ~150-200 GH/s using a BM1397, the same chip as the BitAxe Max. The host runs NerdMiner_v2 built with `-D NERD_NOS` and sends `NerdNOS/{version}`, which the classifier did not recognise. `MinerClassLow` is not a substitute despite covering single-ASIC boards: it pins `MinDiff == InitialDiff == 580`, calibrated for ~500 GH/s, so a board this size lands on that floor and can **never** retarget down from it — sitting at ~2.9x its target share time indefinitely. Adds `MinerClassNerdNOS` (`InitialDiff` 203 = ~175 GH/s x 5s / 2^32, `MinDiff` 140, `MaxDiff` 1200, 5s target), which unlike `MinerClassLow` keeps room below the start so slower clocks can descend — the same shape as the existing `MinerClassS19` exemption. The pattern **must precede** `(?i)nerdminerv2`: matching is first-match-wins and the host genuinely is a NerdMiner running the same firmware family, so a user-agent naming both would otherwise hand an ASIC a fractional lottery difficulty. Routed on the `DeviceHints` path as well (`asic == "bm1397"`, previously `MinerClassMid`, which pinned these boards at ~12x their target share time), because IP-based hints take priority over the user-agent in `DetectMinerWithIP` and would otherwise silently override the classification wherever Sentinel is running. (`spiralrouter.go`, `devicehints.go`.)
- **Vardiff had no way to lower a difficulty that was set too high, because every retarget path requires an accepted share to run.** `RecordShare` and `AggressiveRetarget` are both called only inside the `if result.Accepted` branch of share processing, so a session issued a difficulty above its hardware's reach produces no accepted shares — and the signal vardiff needs in order to lower the difficulty is the exact thing the difficulty prevents. Nothing else closed that loop; the session stayed stranded for as long as it stayed connected. This is the mirror image of the too-low failure above, and it is what made `InitialDiff` load-bearing for the slowest member of a class rather than a starting estimate. Adds `vardiff.IdleDescend`, which halves a session's difficulty after `6x` its target share time with no accepted share, floored at `MinDiff`. Descent is geometric rather than a jump to the floor, so a session that merely hit an unlucky gap recovers on its next accepted share instead of restarting its ramp; at 6x the window elapses with probability `e^-6` (~0.25%) for a correctly-tuned session. It is **opt-in per session and enabled only for `MinerClassLottery`** — the only SHA-256d class where `MinDiff` sits meaningfully below `InitialDiff`, and the only one whose members (~1 KH/s to ~430 KH/s) are indistinguishable by user-agent, since a 78 KH/s single-core ESP32 and a 430 KH/s CYD both send `NerdMinerV2/{version}`. Classes with `MinDiff == InitialDiff` have nowhere to descend to, and the cgminer-based classes go share-free by design while working in progress — the asymmetric decrease caps and 30s retarget cooldowns exist specifically to tolerate that, and reading those quiet stretches as "stranded" would fight them. Driven by a 30s ticker in both `Pool` and `CoinPool`. (`vardiff.go`, `pool.go`, `coinpool.go`.)
- **The difficulty announced to a miner and the difficulty its shares were validated against could differ, rejecting shares that met exactly what the miner was told.** `SendDifficulty` renders the value with `%f`, which is six decimal places, and then stores the *unrounded* float on the session for share validation. At ASIC magnitudes the two are identical; at lottery magnitudes the seventh decimal is significant, and every vardiff retarget produces an arbitrary float. Whenever `%f` rounded down — about half of all retargets — the miner mined to the announced value while the pool checked against a fractionally higher one, and shares landing in that band rejected as `low-difficulty`. The band is roughly 0.02% of shares, small enough to be invisible in a reject-rate summary and to survive a clean field report at 99.15% acceptance, which is why it is fixed with a test rather than found from logs. The value is now rounded once and that single value is both announced and stored. A positive difficulty below `1e-6` previously announced a literal `0.000000` while storing something non-zero; it is now floored at `1e-6`, since announcing zero is a division by zero on the miner side. The wire format is deliberately unchanged rather than widened to full precision, because some firmware parses exponent notation poorly. The rounding lives in an exported `QuantizeDifficulty` so producers and the wire agree by construction rather than by each site remembering to round: `handleMinerClassified`'s post-classify guard compares a profile's `InitialDiff` against what the session already holds, and with only `SendDifficulty` rounding, an unrounded profile value would never compare equal and would re-announce a difficulty the miner already has. Not reachable with the current coin set - the lowest configured block time is DigiByte's 15s, and every lottery profile scaled by 15/60, 30/60 or 60/60 lands exactly on six decimals - but a sub-10s chain scales by 10/60 and makes it live. Found while auditing the vardiff paths above against a field report; it is a separate defect and explains neither that incident nor anything observed after the fix. (`server.go`, `multiserver.go`.)
- **A data race in the WAL rotation tests, and an intermittent failure in the duplicate-tracker memory test, both under `-race`.** Neither is a production defect. `WAL.rotate` assumes its caller already holds `w.mu` — `WAL.Write`, the only production caller, locks across it — but `wal_rotation_test.go` called the bare helper at five sites, racing the `syncLoop` goroutine that `NewWAL` starts, which reads `w.file` and `w.writer` under that same mutex via `Sync`. The detector caught it as `Sync` (`wal.go:205`) against `openWALFile` (`wal.go:140`) via `rotate`. The test sites now go through a `rotateLocked` helper that takes the mutex exactly as `Write` does, and `rotate`'s locking contract — real but previously unwritten, which is what allowed the violation — is now documented on the production side. Separately, `TestDuplicateTrackerMemoryGrowth` asserts heap growth under 100 MB and failed intermittently under `-race`: the cause is not the tracker but that `runtime.MemStats.HeapAlloc` is process-wide, so allocations from other tests in the package — and from their background goroutines, which outlive the test that started them — land inside the measurement. Measured ~0.15 MB running alone versus ~105 MB during a full-package `-race` run. The `trackedJobs` assertion is the real leak check, so the heap bound is relaxed to 250 MB under `-race` via a build-tagged `raceEnabled` flag rather than dropped there. (`wal.go`, `wal_rotation_test.go`, `memory_leak_test.go`.)

### Added
- **A chain-identity gate in the stratum startup path that refuses to serve work for a chain it cannot verify.** Runs after the sync gate and before any miner connects or any block template is built, implementing all three checks: `getdeploymentinfo` for BIP-110 enforcement (presence of the `reduced_data` deployment key — never a version string, since the enforcing and non-enforcing builds differ by one datestamp digit), `getblockhash 961632` against a pinned majority-chain constant, and tip age against wall clock. **`unknown` is a distinct verdict from `minority`**: an unreachable daemon, or one still syncing below the split height, is never reported as wrong-chain, because conflating the two teaches operators to ignore the alarm that matters. Both still fail closed. A node below 961,632 is `unknown` and never a pass. `getdeploymentinfo` failing does not abort the check, since chain identity remains decidable from the block hash, which is the stronger signal. The verdict is logged on **every** startup — verdict, tip height, tip age, enforcement, block hash, reachability — so operators can grep for it instead of inferring chain identity from blocks that never arrive. `allow_nonmajority_chain` (default false, modelled on the existing `skip_genesis_verify` escape hatch) permits mining anyway and logs at WARN with `CHAIN GATE: MINING WITHOUT A CLEAN VERDICT`. Note it also disables the unrelated stale-tip refusal, which is documented on the field itself. Nine test functions covering fourteen cases, including the three RPC-failure variants separately and the non-mainnet exemption below. (`internal/daemon/chainguard.go`, `internal/pool/coinpool.go`, `internal/config/v2.go`.)
- **A Sentinel `chain_identity` alert on the existing notification paths.** Fires on BIP-110 enforcement, a block 961,632 mismatch, or a BTC tip older than three hours, with a six-hour cooldown, and flows through Discord, Telegram, XMPP, ntfy, SMTP and webhooks with no new plumbing. Silent when the daemon is unreachable or still syncing, for the same reason as the gate. `getblockhash` and `getdeploymentinfo` were added to `_RPC_ALLOWED_METHODS`; without that the calls are rejected by the security whitelist and return `None`, and the check would never have fired. The alert text assumes no knowledge of BIP-110 and states what happened, that blocks found there are unlikely to be worth anything, the exact repair command, and that the pool has already stopped mining BTC. (`SpiralSentinel.py`.)
- **A persistent chain verdict on the dashboard node cards.** `chain_verdict`, `chain_verdict_detail`, `client_name` and `_mediantime` are added to `fetch_coin_node_health`, so `/api/nodes` and `/api/nodes/<symbol>` carry them without route changes, and the card renders one of four states: Majority, **MINORITY** (red), Stale tip (amber — right chain, wedged node, which is a different problem with a different remedy), and Unverified. The reasoning appears in the tooltip. `populate_chain_identity` is also called for the primary coin so the single-node fallback card carries a verdict, not just the multi-node grid. Non-BTC coins and older cached payloads render nothing rather than an empty or bogus row. Every other stat on that card is identical between a healthy node and one on the minority chain, which is precisely why the verdict needs its own row. (`dashboard.py`, `templates/dashboard.html`.)
- **A dedicated `MinerClassS19` vardiff tier for the Antminer S19 series.** Contributed by [Kamakhu](https://github.com/bkhuraijam/Spiral-Pool). S19 hardware previously shared `MinerClassPro` with the S21 and Whatsminer M50+, a tier spanning roughly 90 TH/s to over 200 TH/s; one difficulty range across a 2x hashrate spread costs the slower half in avoidable rejects. The new class targets the 90-141 TH/s band — S19 (95T), S19j Pro (104T), S19k Pro (120T), S19 XP (141T) — with `InitialDiff` 24000 (~100 TH/s at a 1s target), `MinDiff` 16384 and `MaxDiff` 65536, the cap chosen to cover the S19 XP Hyd without letting S21s spill into the tier. Detection is by user-agent (`antminer.?s19|s19k|s19j|s19.?pro|s19.?xp|s19.?hydro|s19a`, covering Vnish, Braiins and LuxOS alternate strings), placed **above** the generic `antminer` and `bmminer` patterns so the broader tiers do not claim these first, and by `classifyDevice()` for hosts reporting a device model. Three corrections were made applying it. The contributed `Vendor()` change mapped `MinerClassPro` to `"bitmain"` alongside S19; Pro also holds Whatsminer M50+, which is MicroBT, so that would have mislabelled every Whatsminer — `"bitmain"` now applies to S19 only and Pro keeps its existing `"generic"` result, leaving current behaviour unchanged. Indentation was space-based and is now tabs. And the profile violated the repo-wide `MinDiff == InitialDiff` invariant asserted by `TestVerifyDefaultProfilesValid`; rather than relax the assertion silently, S19 joins Lottery, Unknown and FarmProxy as a **documented** exemption, on the same grounds as FarmProxy — the series runs from ~70 TH/s underclocked to ~255 TH/s on an XP Hyd, a 3.6x spread, and at `InitialDiff` 24000 a 70 TH/s unit needs 1.47s per share against a 1s target, so vardiff requires headroom below the starting point. No other miner class was reordered or retuned; `MinerClass` is serialised as a string and never persisted numerically, so inserting the constant beside `MinerClassPro` renumbers nothing observable. (`stratum/spiralrouter.go`, `stratum/devicehints.go`.)
- **Guards for three ways an operator could end up back on the wrong chain without noticing.** `coin-upgrade.sh` reports and exits non-zero if `bitcoin.conf` contains `maxtipage`, listing the offending line numbers — miners on the stalled minority chain were advised to set `maxtipage=2592000` to suppress stale-tip warnings, and on the majority chain that same line disarms the daemon's initial-block-download gating for thirty days. It does **not** edit the config: a safe repair would have to know whether the value is actually unsafe (anything at or below Core's `86400` default is stricter, not weaker), which network section it sits in, and could only be applied with the daemon stopped, which is not where this check runs. Worth stating precisely, because an earlier draft of this entry overstated it: `maxtipage` **cannot** blind the stratum. The chain gate computes tip staleness itself from `mediantime` against its own Go constant and never reads the daemon's setting, so the pool still refuses to mine a dead tip. What the option actually breaks is the daemon's own `initialblockdownload` flag, which the dashboard, the Sentinel and the sync gate all trust — so the symptom is that every health surface disagrees with the stratum, not that the pool hashes into the void. It also refuses to swap while a legacy BDB wallet is loaded, since Core 30+ cannot load one (recoverable — Core retains a read-only BDB parser for `migratewallet` — but discovering it after the swap means a daemon up with its wallet missing in the same window the consensus binary changed). `install.sh` now rejects a Knots binary replicated from an HA primary: `copy_binaries_from_primary` verifies SSH reachability and file counts but never which client it copied, so an HA backup would silently inherit the enforcing binary and never reach the verified download.

- **The chain gate is mainnet-only.** Block 961,632 is a mainnet constant, so gating a regtest chain sitting at height ~100 against it would refuse to start a perfectly correct node — permanently, since it can never reach the split height. `getblockchaininfo.chain` other than `main` now yields a distinct `n/a` verdict that permits mining, matching the regtest exemption `waitForSync` already had. The dashboard and the Sentinel carry the same guard; without it a regtest node would have reported "still syncing below the split height" forever and a regtest build listing a `reduced_data` deployment would have tripped the red wrong-chain alert.
- **Version comparison tolerates Core's third component.** Bitcoin Core publishes release `31.1` but the binary reports `v31.1.0`. A plain string compare read a correct install as a failed one — which triggered `rollback_coin`, i.e. an attempt to restore Knots. `_ver_matches` normalises both sides identically and is now used at every comparison site, including the three that decide what the operator is *shown*: a healthy Core node would otherwise have been listed as still pending and been sent the chain-split notice.
- **A failed BTC check no longer silently abandons the remaining coins.** `upgrade_coin` can now return non-zero, and a non-zero return inside a `for` body is not protected by `errexit` — with BTC first in `ALL_COINS`, selecting "All of the above" meant a BTC problem killed the run and DGB was never upgraded, with nothing printed to say so. Failures are collected and reported at the end instead.
- **Re-running the upgrade re-verifies the chain.** Being at the target *version* does not mean being on the right *chain*, and the version cache is written before the chain is checked — so the recovery the script itself prints (`--coin BTC --reindex`) returned "already at 31.1 — nothing to do" forever, leaving the node stranded while `--check` reported it green. The already-at-target path now re-runs the config and chain checks; `--reindex` takes the full path.
- **`wait_for_daemon` could kill the upgrade before the chain was ever verified.** Under `set -o pipefail`, a daemon whose only RPC output is a bare `error code: -28` with no stage line made `grep -v` match nothing and return 1, and the bare assignment exited the script — silently, immediately after "Start daemon" and before the BTC verification. Pre-existing, but directly upstream of the new check.
- **Config backups no longer leak RPC credentials to an HA peer.** `pool-mode.sh` now backs up each coin config before regenerating it, but a file named `bitcoin.conf.bak-<stamp>` does not match the `*.conf` exclude that `ha-replicate.sh` uses precisely to keep node-specific `rpcuser`/`rpcpassword` off the backup node. Backups are written outside the datadir at mode 600, and the rsync excludes were widened to match.

- **NMMiner is named in the dashboard's ESP32 section, and recognised by the importer and scanner.** The Spiral Router already classified it by user-agent (`(?i)nmminer` → `MinerClassLottery`, displayed "NMMiner"), so a connected NMMiner always got the right difficulty and name — but nothing in the dashboard mentioned it, so an operator had no way to tell where it belonged. **It is added manually, under the existing "ESP32 Miner" section**, whose description now names NMMiner and NerdMiner explicitly; there is no separate NMMiner button, and adding one would mean registering a new device type in the 23 places `esp32miner` appears in `dashboard.py` alone, plus `settings.html` and `rescan-miners.sh` — power defaults, valid-type lists, fleet aggregation, the Sentinel sync — where a single omission silently drops the device out of that surface. Sharing the `esp32miner` type inherits all of them, and the hardware class matches exactly: NMMiner is ESP32/ESP32-S3 running the closed-source `NMminer1024` firmware at ~50-100 kH/s and 1-2W, which is already the `esp32miner` power default. Two supporting paths were also covered: `import_miners_from_sentinel()` maps an incoming `nmminer` type onto `esp32miner` (the same aliasing the pre-existing `"esp32"` entry beside it uses), and the network scanner matches `nmminer` in `boardVersion`/`hostname` and reports the model as "NMMiner" rather than the generic "ESP32 Miner". That scanner path is defensive rather than practical — NMMiner does not serve the AxeOS-style API the scanner probes, so discovery will not find it, which is precisely why the manual route needed labelling. (`src/dashboard/dashboard.py`, `src/dashboard/templates/settings.html`.)

- **`tests/test_config_safety.sh` — 43 assertions pinning every place that reads or rewrites an operator's `bitcoin.conf`.** These functions run unattended as root against hand-edited files, and their failure mode is not a stack trace: it is a daemon that will not start, or a node that has irreversibly deleted its own block data. The suite extracts the **real** functions out of the shipping scripts with an `awk` matcher rather than reimplementing them, so it fails if the production code changes. Covers all three Bitcoin Core config rules a plain `^key=` grep violates (section scoping, whitespace tolerance, first-assignment-wins) plus CRLF handling and non-integer values; asserts the generated `prune=` line always contains an `=`; proves the RAM cap actually fires on a duplicated key and emits nothing on stderr; proves peer settings land above a trailing `[test]` header and that a second run is a byte-identical no-op; proves two coins whose config is both named `bitcoin.conf` get distinct backups; and proves the DGB pruning path declines section-bearing configs byte-identically and aborts non-zero when its backup fails. The file-mode assertion probes whether `chmod` is honoured by the filesystem and skips rather than reporting a false failure where it is not.

### Changed
- Version string moves from `2.6.7` to `2.7.0` across the installer, upgrade script, Sentinel, dashboard, stratum, `spiralctl`, helper scripts, Docker labels, manifests, and documentation — 93 occurrences across 52 files. Codename remains **Spiral Citadel**. This is the first **minor** bump on that line rather than a patch: the tag convention is that patch releases apply in place on the same tag, and this release changes which Bitcoin daemon is installed and adds a startup gate that can refuse to mine, so it is not an in-place patch. References that deliberately keep an older version are historical rather than identity — the `What's new in v2.6.x` sections of the upgrade guide, and `upgrade.sh`'s `_VC_PREV` map, which records the previous BTC version (`29.3.knots20260210`) so the version cache can be seeded when `--version` parsing fails.
- **BTC relay and mining policy now follows Bitcoin Core defaults rather than Knots'.** (Documented in the Docker config template; the bare-metal generator carries only the header change.) These differ in ways that change block templates: `datacarriersize` 83 → 100000, `blockmintxfee` 1000 → 1 sat/kvB, `permitbaremultisig` false → true, `minrelaytxfee` 1000 → 100 sat/kvB. None were pinned in any BTC config, so they shift with the daemon. This is an intentional consequence of running Core, stated explicitly in both config templates. The Docker template previously pinned `minrelaytxfee=0.00001` — Knots' default — while the bare-metal, pool-mode and test configs did not, so the four deployment paths disagreed about relay policy; that pin is removed and all four now agree **for newly generated configs**. Existing Docker installs keep `minrelaytxfee=0.00001` on their named volume, because the entrypoint deliberately does not regenerate a config that already exists — the template documents this at length. It is harmless (a valid Core setting, merely stricter than the default) but means an upgraded Docker node does not silently match a fresh one; delete the file from the volume if you want the new default.
- **A failed BTC upgrade no longer rolls back to Bitcoin Knots.** `rollback_coin` restores the previous binary so a failed upgrade is not an outage, which is right for the other fourteen coins. For BTC the previous binary is RDTS-enforcing, so a "successful" rollback hands the operator a daemon that runs perfectly and mines nothing — strictly worse than a stopped daemon, because it looks fine. BTC rollback now refuses when the backed-up binary reports Knots, leaves the daemon stopped, and says why.

- **Operator-facing surfaces and documentation.** `upgrade.sh` gained a BTC chain-split block in both the console summary and the Discord embed, worded to make clear the stack upgrade does **not** fix it and that `coin-upgrade.sh` must be run. `WARNINGS.md` gained a hazard section covering which builds follow which chain, the one-digit datestamp trap, and the fact that merge-mined coins keep paying while BTC earns nothing. `OPERATIONS.md`, `DOCKER_GUIDE.md`, `WINDOWS_GUIDE.md`, `REFERENCE.md` and `UPGRADE_GUIDE.md` gained matching sections, the last with a full Knots→Core migration procedure. `docs/reference/SENTINEL.md` documents the new alert. Display strings across `spiralctl`, the systemd unit template, the regtest config and the installer menus now say Bitcoin Core.
- **Smaller fixes made along the way.** `install.sh` force-replaces any locally installed Knots binary regardless of the version it claims, and rejects one replicated from an HA primary. `pool-mode.sh` re-downloads rather than trusting whatever tarball is left in `/tmp`. `download_BTC` writes its success message to stderr — the function returns the extracted directory name on stdout, so a stray line there corrupted the path and broke every BTC upgrade. The Tor config for BTC sets `natpmp=0`, since Core 30.0 changed that default to enabled and a node behind Tor should not be asking the router to map a port. `install.sh` also accepts a pre-placed, checksum-verified tarball at `/tmp/<file>` or beside the installer: Core publishes binaries from bitcoincore.org only, so unlike the old Knots path there is no second mirror, and inventing one would be worse than having none.

- **`pool-mode.sh` now explains its longest silence instead of looking hung.** Adding a coin ends with `systemctl restart spiralstratum`, which blocks until the unit goes active — and the unit runs `wait-for-node.sh` as `ExecStartPre` with an 1800-second budget, so on a node still in initial block download the command prints one line and then sits for up to thirty minutes. On a fresh install the pool may not come up for **days**, because that is how long a full chain sync takes. All of that is correct and deliberate: no work is served while a node cannot validate the chain, `wait-for-node.sh` says so itself at timeout, and the restart loop cannot wedge — each cycle takes 1810s against a `StartLimitIntervalSec` of 300, so the burst counter resets every pass and `StartLimitBurst=10` never trips. The only thing missing was any way for the operator to know that from where they were standing; `wait-for-node.sh` reports real progress every ten seconds, but to the journal. The restart now states the time it can take, that a fresh install may take days, why, that systemd retries on its own, and points at `journalctl -fu spiralstratum`. This is the same failure shape as the installer's RPC-poll defect above — correct behaviour, no way to tell it apart from a hang. (`scripts/linux/pool-mode.sh`.)

- **The security workflow now excludes two gosec rules that had made the code-scanning tab unreadable.** GitHub code scanning carried **308 open alerts, all from gosec**, of which G115 (integer overflow conversion) and G104 (unhandled errors) were 239 — and every one triaged as a false positive. G115 fires on `int64(stats.Accepted)` share counters, `int64(info.Blocks)` block heights and `byte(idx)` into an already-bounds-checked 32-character CashAddr charset, all of them domain-bounded orders of magnitude below the conversion limit. G104 fires on best-effort `dir.Sync()` metadata syncs and on zap's `logger.Sync()`, which is the conventional ignore; the block-found durability path that actually matters **is** fully error-checked, at `block_wal.go` 187/236/250/261 and throughout `shares/wal.go`. Two hundred and thirty-nine unfixable alerts is not a security posture, it is a reason to stop reading the page, so the remaining rules could never surface anything. The other categories were triaged and left enabled: no `exec.Command` in the tree passes through a shell, coin arguments are validated against `coinRegistry` at every call site, `backupFile` already strips traversal with `filepath.Base`, the weak-RNG hits are all startup jitter for thundering-herd avoidance while token generation correctly uses `crypto/rand` and fails hard rather than falling back, and the two "hardcoded credentials" are a `CONFIGURE_RPC_PASSWORD` template placeholder and a variable named `tokenFile` holding a path. The exclusions carry that reasoning inline in the workflow, along with an instruction not to add more without triaging first. (`.github/workflows/security.yml`.)

- **Documentation.** `docs/reference/DASHBOARD.md` gains a chain-identity section documenting the four new node-payload fields (`chain_verdict`, `chain_verdict_detail`, `client_name`, `_mediantime`) and the four card states, including why `stale` is deliberately not rendered as a wrong-chain error. `docs/reference/SENTINEL.md` gains `chain_identity_enabled` in the config-key table, the `spiralctl alerts disable chain_identity` route, and the three suppression paths the alert deliberately bypasses. `docs/setup/UPGRADE_GUIDE.md` gains a `What's new in v2.7.0` section, which every release back to v1.1.0 had and this one did not.


- **`MinerClassLow`'s `MaxDiff` comment read "Cap for ~645 GH/s" but the value works out to ~129 TH/s.** The number 645 is correct — `150000 x 2^32` is 644 TH/s — but the unit is wrong by a factor of 1000, and it was derived at a 1s target like its sibling classes, while `MinerClassLow` is the one class on a 5s target. The parenthetical "covers NMAxe at 500 GH/s with headroom" is a rationalisation built on that wrong unit. **The value is left alone deliberately:** the `sgminer`/`cpuminer`/`ccminer` patterns all resolve to this class, as does every `DeviceHints` device reporting under 1 TH/s, so a ceiling matching the comment would sit below the class's own documented intake and cap those sessions below their capability. Comment only; no behavioural change.

### Notes
- **Merge mining is unaffected by the split, and that makes the failure harder to spot.** `CAuxPow::check` (verified in Namecoin nc28.0) validates the parent chain ID, the aux merkle branch, the coinbase's presence in the parent block's merkle tree, and the merged-mining header — and consults no parent-chain state whatsoever: no `chainActive`, no `LookupBlockIndex`, no tip, no height. It cannot, since a Namecoin node has no copy of Bitcoin's chain. So an AuxPoW proof carrying an RDTS parent header remains valid, and an operator stranded on the minority chain **keeps earning NMC, SYS and XMY normally** while BTC silently earns nothing. Blocks continue to arrive and confirm, payouts happen, and the only missing piece is the one that pays best — which is why no amount of watching pool statistics would reveal this, and why the gate is a startup refusal rather than a report.
- The BIP-110 chain had produced only a handful of blocks in total since the split, sat hundreds of blocks behind the majority chain, and inherited the majority chain's ~127T difficulty with a tiny fraction of its hashpower, so its first retarget is thousands of blocks away. (Deliberately not quoting an exact block count or tip height: the branch is barely mined, the figure moves, and the explorer that tracked it has since reorged onto the majority chain — so any number written here would be both stale and hard to re-verify. The block **hashes** the code depends on are pinned and were verified against multiple independent sources; only the running total is left approximate.) A proof-of-work change to BLAKE2b has been announced for that chain but has **no released client and no activation height**; operator-facing wording avoids treating it as scheduled.

- Regression coverage for the miner-classification fixes asserts **block-time-scaled** outcomes rather than the raw profile constants. Every defect in that area only appeared after `scaleProfilesForBlockTime` multiplied the constants down for a fast chain, so tests against the unscaled values would not have caught the original failure. The new tests were verified to fail against the pre-fix values, reproducing the reported 2.5s share interval.
- No miner other than the two classes named above changes classification; this is asserted directly for eleven representative user-agents spanning lottery, BitAxe, NerdQAxe, Bitmain and cgminer.

---

## [v2.6.7] - 2026-08-12 - Spiral Citadel

Hardware-support release. Both defects leave a supported device unusable while the pool itself reports nothing wrong: a PEP pool that will not start against genuine Pepecoin Core, and an ESP32 lottery miner that subscribes, authorizes, receives jobs, and then needs 509 days to produce a single share. Neither touches mining correctness — no share is miscounted and no block is lost — but in both cases the operator's only symptom is silence, with nothing in the logs pointing at the cause. No database migrations, no config format changes. Drop-in upgrade from v2.6.6.

### Fixed
- **Pepecoin's genesis hash was from a different chain, so a PEP pool could not start against genuine Pepecoin Core.** `PepeCoinGenesisBlockHash` was `00008cae6a01358d774087e2daf3b2108252b0b5a440195ffec4fd38f9892272`, where [pepecoinppc/pepecoin `src/chainparams.cpp`](https://github.com/pepecoinppc/pepecoin/blob/master/src/chainparams.cpp) asserts `consensus.hashGenesisBlock == uint256S("0x37981c0c48b8d48965376c8a42ece9a0838daadb93ff975cb091f57f8c2a5faa")` for mainnet. The startup genesis check has only existed since 2.6.x, which is why this went unseen for as long as it did — before it, nothing ever compared the constant to the node. With the check in place the mismatch is unconditional and fatal: `VerifyGenesisBlock` fails on every start with "CRITICAL: Genesis block mismatch - WRONG CHAIN!", so a PEP pool never comes up at all. The wrong value is also visibly implausible on inspection: Pepecoin is a Dogecoin-lineage Scrypt chain, so its genesis identity hash is SHA256d and carries no leading zeros (Dogecoin's own is `1a91e3da…`), whereas `00008cae…` has the four-zero prefix of a Scrypt PoW hash from some other chain. This is the same defect class as the `0x37` → `0x38` P2PKH version byte fixed in v2.6.5 below — both constants were modelled on a chain that is not pepecoinppc. Corrected in `internal/coin/pepecoin.go` and in the `PEP` entry of `config/coins.manifest.yaml`; the manifest-vs-Go cross-check in `internal/coin/manifest.go` had been asserting agreement between two copies of the same wrong value since PEP was onboarded, exactly as it did for the version bytes. Reported by an operator who confirmed the patched build logs "Genesis block verified - correct chain confirmed" against Pepecoin Core on mainnet.
- **NMMiner and LeafMiner were missing from the Spiral Router user-agent table, so both fell to the config difficulty floor and appeared to be receiving no work.** An unmatched user-agent resolves to `MinerClassUnknown`, and `MinerClassifiedHandler` reads that class as an instruction to use the operator's YAML difficulty in place of a router profile — `useConfig := cfgDiff.VarDiff.UseConfigDifficulty || profile.Class == stratum.MinerClassUnknown`. That branch exists so an operator can pin difficulty when auto-detection guesses wrong, but for an *unrecognised* miner it silently substitutes a farm-scale number: **1024** on the multi-coin smart port. NMMiner runs on an ESP32 at roughly 100 KH/s, and `1024 × 2³² / 100000` is 43,980,465 seconds — **509 days per share**. Nothing about the session looks wrong from either end: it subscribes, authorizes, and receives jobs normally. It simply never returns a share, and because vardiff only adjusts on *submitted* shares, nothing ever pulls the difficulty back down — the miner stays pinned there for the life of the connection. The operator sees a device that is connected, idle, and silent. What kept this hidden is a near-miss already in the table: `` `(?i)\bnminer` `` looks like it would catch it, but the name is `n-m-`**`m`**`-i-n-e-r` and the doubled `m` means the pattern never fires; nothing else matches either, so the miner falls through all five tiers to Unknown. Added `` `(?i)nmminer` `` → `NMMiner` and `` `(?i)leafminer` `` → `LeafMiner`, both `MinerClassLottery` — InitialDiff 0.001, MinDiff 0.0001, 60-second target, which is **42.9 seconds per share** for the same device, scaling down proportionally on chains fast enough to pull the target below 60s. Both sit at the top of Tier 4, ahead of the existing generic `` `(?i)esp32` ``: a user-agent such as `NMMiner/v0.6.30 (ESP32-S3)` would otherwise match `esp32` first and report the right class under the wrong device name. Tagged `[MEDIUM]` rather than `[CONFIRMED]`, since NMMiner's firmware is closed-source and there is no manufacturer repository to verify the string against. **Blast radius is these two names only.** The new patterns sit below Tiers 1–3, so every ASIC, BitAxe, NerdQAxe, cgminer and marketplace user-agent is matched earlier and is untouched, and no existing entry was reordered; the other ESP32 miners (NerdMiner V2, ESP32 Miner, SparkMiner, BitMaker, Arduino) already matched and already received the lottery profile, so they are unchanged too. Verified against a 49-entry corpus of real user-agents including adversarial near-misses — `bmminer`, `Antminer/bmminer`, `NMAxe`, `minminer`, `NM-Miner` — with zero captures. Regression coverage pins the class, the resolved name, the 0.001 difficulty, and the ordering case where `esp32` must not win. (`stratum/spiralrouter.go`, Tier 4 lottery patterns.)

### Release identity
- Version string moves from `2.6.6` to `2.6.7` across the installer, upgrade script, Sentinel, dashboard, stratum, `spiralctl`, helper scripts, Docker labels, manifests, and documentation — 87 occurrences across 49 files. Codename remains **Spiral Citadel**, a patch release on the same tag line. One reference outside this changelog deliberately keeps `2.6.6`: the `What's new in v2.6.6` section of the upgrade guide, which is historical rather than identity. Unlike the previous two releases there is no third-party version to disambiguate — no shipped dependency carries `2.6.6` as a substring, and DigiByte Core's `9.26.5` is unaffected.

---

## [v2.6.6] - 2026-08-08 - Spiral Citadel

Diagnostic-integrity release. Every defect here made the pool misreport its own state — an 8x-inflated network hashrate served from the API to every consumer, a block-history command that could not run at all, and config readers that answered with invented values rather than admitting they lacked permission. Mining itself was correct throughout. One exception carries real money: BIP22 `inconclusive` rejections were treated as permanent, abandoning blocks the daemon explicitly invited us to resubmit. Found while investigating a seven-day block drought on a field pool that turned out to be variance — the instruments were wrong, not the mining. No database migrations, no config format changes. Drop-in upgrade from v2.6.5.

### Fixed
- **`spiralctl stats blocks` died with `SyntaxError: invalid character '─' (U+2500)` on every install.** The v2.6.5 entry below fixed a *locale* failure in this renderer; this is a second, independent defect in the same block that the encoding fix could never have reached, because the program never reaches the interpreter intact. The renderer is passed as `python3 -c "<program>"` — a double-quoted shell word — and one line inside it, `sep = "─" * 50`, used bare double quotes. The shell consumes them as string delimiters, so Python receives `sep = ─ * 50` and refuses to parse. Confirmed against the shell's own trace, which expands the argument to `$'\nsep = \342\224\200 * 50\n…'` — `\342\224\200` being U+2500 with no quotes around it. Reproduced by expanding the committed heredoc through bash and parsing the result: `SyntaxError: invalid character '─' (U+2500)` at **line 119**, matching a field report byte for byte. Every other string in the program is single-quoted, which is why this was the only line affected. Now single-quoted to match. A sweep of all fifteen generated `spiralpool-*` commands for unescaped double quotes inside `python3 -c "…"` bodies found exactly one other occurrence, in `spiralpool-config`, which is correctly written as `\"` and needs no change. (`install.sh`, generated `spiralpool-blocks`.)
- **`/api/pools` reported network hashrate 8x high for DigiByte, and every consumer inherited it.** `Pool.updateStats` derives it as `difficulty × 2³² / algoBlockTime` and sourced the block time from `getAlgoBlockTime(p.cfg.Pool.Coin)`. That helper switches on a *ticker*, but `cfg.Pool.Coin` holds a coin **name** — `config.go` carries a separate `extractSymbolFromCoin` for precisely this reason. The switch therefore never matched and every coin silently fell through to the 600-second default: Bitcoin's block time, applied to a chain that produces a SHA256d block every 75 seconds. The failure is invisible by construction — no error, no log, just a plausible number — and it is served from the API, so the dashboard, `spiralctl stats`, and Sentinel all reported it alike. Confirmed arithmetically against a live pool: `558862782.8971367 × 2³² / 600 = 4000495625824583.5`, matching the API's `networkHashrate` field to the last digit, where the daemon's own `getmininginfo` reported 39.4 PH/s for the same chain. Now passes `coinImpl.Symbol()`, which yields `DGB`/`DGB-SCRYPT` and is never nil, since a `coin.Create` failure aborts pool construction. `CoinPool` was already correct — it uses `cfg.CoinConfig.Symbol`, a real ticker — so only the single-coin path was affected. The hazard is easy to re-introduce because the coin registry accepts names as aliases (`Register("DIGIBYTE", …)`), so passing `cfg.Pool.Coin` compiles and runs; a regression test now pins `getAlgoBlockTime(Symbol())` for five coins and pins the silent-default behaviour on a coin name. (`pool/pool.go`, `updateStats`.)
- **The same ticker-vs-name confusion in `spiralpool-stats` and `spiralpool-watch`.** Both scripts carry their own `difficulty × 2³² / blockTime` fallback for when the API omits `networkHashrate`, and both keyed the block-time table off `coin.type` — a coin name, so `.upper()` gave `DIGIBYTE`, missed `DGB`, and took the same 600s default. In practice this path is dormant, because the API always supplies `networkHashrate`; the displayed 8x error came from the Go defect above, not from here. Fixed regardless, since the fallback exists to be correct when it does fire. Both now derive the ticker from the pool id (`<ticker>_<algo>_<n>`) and fall back to `coin.type`; `spiralpool-watch` keeps `coin.type` for its displayed SYMBOL column, so only the arithmetic changes. Verified against the real `/api/pools` payload: DGB resolves to 75s, BTC and every 600s coin are bit-identical, and the branch is skipped entirely when `networkHashrate` is present. (`install.sh`, generated `spiralpool-stats` and `spiralpool-watch`.)
- **`spiralctl config get` and `config show` printed invented values when they could not read the config.** Every reader ends in `${val:-<default>}` and every `grep` sends errors to `/dev/null`, so an unreadable file is indistinguishable from an unset key. The Sentinel config is mode 600 owned by the Sentinel user, which makes this the *normal* outcome of omitting `sudo`: `spiralctl config get missing_payout_days` confidently answered `7 days` immediately after a `sudo … set` had written `10`, and `config show` would render a complete but entirely fictional configuration. Both now test readability up front and direct the operator to re-run with `sudo` rather than answering wrongly. (`scripts/spiralctl.sh`.)
- **BIP22 `inconclusive` was classified as a permanent rejection, so recoverable blocks were abandoned.** `submitblock` returns `inconclusive` when the node's state catcher never observed a validation result — the block's fate is *unknown*, not invalid — and the daemon's own guidance is to resubmit. `isPermanentRejection` returned true for it, which breaks the retry loop immediately and orphans the candidate. A field WAL records a block lost exactly this way: height 23717090, `"status":"rejected"`, `"submit_error":"block rejected: inconclusive"`, and the block is absent from that pool's UTXO set. Note the daemon phrasing — the string also contains `rejected`, which is itself in the permanent-pattern list, so the check must run *before* the pattern loop rather than after it as the old special case did. `duplicate-inconclusive` is deliberately excluded and stays permanent: it carries `duplicate`, which callers already resolve through `verifyBlockAcceptance` rather than a blind retry. Both submission paths gate on this predicate (`pool.go` and `coinpool.go`), so one change covers both. (`pool/pool.go`, `isPermanentRejection`.)
- **The DGB `job_rebroadcast` migration could never reach the installs that needed it most.** The gate added in `da63e87` skipped any install already at or past 2.6.5, on the reasoning that such an install had already had its chance to migrate. That is false for anyone who upgraded to 2.6.5 during the roughly ninety minutes between the release and the migration landing: they are at 2.6.5, so the gate skips them — and will skip them on every future upgrade too, stranding them at 30s permanently with no path out short of hand-editing `config.yaml`. Replaced with an explicit marker file (`config/.migrated-dgb-job-rebroadcast`), which records whether *this migration* has run — the question the version gate was trying to approximate. At-most-once is preserved, so an operator who deliberately sets 30s afterwards is still never flipped back, and the marker is written on the no-op path as well so the migration does not stay live forever for configs that had nothing to change. Verified across a mixed config (DGB and quoted `DGB-SCRYPT` rewritten, BTC untouched at 30s, an operator-tuned 15s left alone), a second run as a no-op against a deliberate 30s, and the stranded case: `CURRENT_VERSION=2.6.5` with no marker now migrates. (`upgrade.sh`, `migrate_dgb_job_rebroadcast`.)
- **A failed or skipped `spiralpool-*` command refresh was invisible.** These commands exist only in `/usr/local/bin` and are written only from `install.sh` heredocs, so if `update-commands.sh` does not run, the deployed copies stay at whatever version first installed them — across every subsequent upgrade, indefinitely. The call site discarded stderr and had no `else`, so both a hard failure and a missing source tree passed in silence; an operator's first indication is hitting a bug fixed releases ago. Stderr is no longer discarded, a failure now prints the manual re-run command, and the missing-source case is reported instead of skipped. (`upgrade.sh`, `update_utility_scripts`.)

### Changed
- **The missing-payout alert now requires evidence that a payout was owed, instead of counting days.** "Wallet balance unchanged for N days" cannot distinguish *we are unlucky* from *our rewards are going somewhere else*, and those need opposite responses. It is also calibrated for exactly one block rate. Seven days is reasonable for a pool averaging a block every day or two; for a solo BTC miner with one S21 against a ~800 EH/s network the mean gap is **about 76 years**, so P(no block in 7 days) is **99.97%** — the alert is a certainty, not a signal. Worse, `coin_missing_payout_alerted` latches until a balance change clears it, so that operator gets exactly one guaranteed-false alert on day 7 and then permanent silence: useless in both directions. `missing_payout` now fires only when the pool has recorded at least one block since the balance last moved and the grace period has passed — the wrong-address, hijack, and orphan-storm case, which is deterministic rather than statistical. Because `blocksFound` counts orphans too, a single unpaid block inside the grace window is not enough on its own; a real misdirection stays unpaid while an orphan is followed by blocks that do credit the wallet and reset the anchor. A new `missing_payout_max_days` (default 0, disabled) restores an unconditional day-count alert for operators who want one, and covers the case neither test sees: blocks landing but never being recorded, where both counters stay frozen. The embed now states which of the two fired and lists causes accordingly. This is a real behaviour change — a pool that today alerts on a quiet week will no longer do so; the drought question moved to `block_drought` below. (`SpiralSentinel.py`, new `coin_blocks_at_last_balance` state.)
- **The `block_drought` alert derives its threshold from probability instead of a fixed hour count, and is on by default.** It previously fired after `block_drought_hours` and shipped disabled (`0`), which is the same defect as above in a different unit: 24 hours is an unremarkable gap for a small DigiByte pool and a physical impossibility for a solo BTC miner. Block discovery is Poisson, and the pool already computes effort as `100 · elapsed / expected` from live difficulty and hashrate — which *is* `100 · λ · t` — so the chance of a gap at least this long is exactly `e^(-effort/100)`. The alert now fires when that probability drops below `block_drought_probability` (default `0.01`), i.e. when effort exceeds `-100 · ln(p)` = **460.5%**. One setting is then correct on every coin and re-derives itself as difficulty and hashrate move: at 1-in-100 it lands at **~6.9 days** for a pool averaging 0.67 blocks/day — where the old fixed defaults sat, so that calibration is unchanged — and at **~350 years** for the solo BTC miner. `block_drought_hours` still overrides with an explicit wall-clock threshold, and a negative probability disables the alert. Effort of 0 (no hashrate, no difficulty, no block ever found) is treated as "nothing to judge" and stays silent, because a pool with no hashrate is a miner-offline problem rather than a luck problem. Enabling it by default exposed a latent flaw: `fireAlert` deduplicates only on `AlertCooldown` (15m), but a drought is a single condition that persists until a block is found, so the alert would have re-announced every fifteen minutes for the whole drought. A per-coin latch, cleared only when a block is found, limits it to one announcement per round. (`pool/sentinel.go`, `config/v2.go`, `config.example.yaml`.)

### Added
- **`spiralctl config get|set missing_payout_days`.** Sentinel has read `missing_payout_days` since multi-coin payout tracking landed, but the key was absent from `spiralctl config`, so the only way to change the missing-payout alert threshold was to hand-edit `config.json`. The default of 7 days is tight for a small solo pool: at roughly 0.67 blocks/day a 7-day gap carries about 1% probability, which is around two false alarms a year on a pool that is working perfectly. Validates whole days ≥ 1 and reports the effective default when the key is absent. Joined by `missing_payout_max_days`, where 0 is meaningful rather than invalid — it disables the backstop. (`scripts/spiralctl.sh`.)

### Changed
- **`update-commands.sh` validates extracted commands before installing them.** Extraction keys off heredoc markers; if a marker drifts or a heredoc moves, `sed` yields truncated or spliced text that was previously written straight over a working executable. Each extraction is now `bash -n` checked in the temp file and the existing copy is kept on failure. Confirmed non-regressive against all fifteen shipped commands, and confirmed to reject a deliberately malformed extraction while leaving the installed command intact. This catches structural damage, not errors inside embedded `python3 -c` bodies — the `spiralpool-blocks` defect above is that second class and is guarded by single-quoting instead. (`scripts/linux/update-commands.sh`.)

### Release identity
- Version string moves from `2.6.5` to `2.6.6` across the installer, upgrade script, Sentinel, dashboard, stratum, `spiralctl`, helper scripts, Docker labels, manifests, and documentation — 92 occurrences across 49 files. Codename remains **Spiral Citadel**, a patch release on the same tag line. Five references deliberately keep `2.6.5` because they are historical rather than identity: the `What's new in v2.6.5` section of the upgrade guide, and four lines of commentary in `migrate_dgb_job_rebroadcast` that describe when the old default shipped and why its version gate stranded 2.6.5 installs. DigiByte Core's own `9.26.5` is unaffected — it shares no substring with `2.6.5`.

---

## [v2.6.5] - 2026-08-07 - Spiral Citadel

Address-validation correctness release, plus one unbounded-work fix in WAL reconciliation. Two coins carried Base58 version bytes that do not match their reference client, so the pool disagreed with the daemon about which addresses are valid; both were found by operators trying to configure a payout address the node itself accepts. No database migrations, no config format changes. Drop-in upgrade from v2.6.4.

### Fixed
- **`spiralctl stats blocks` printed absolutely nothing — no rows, no error, no banner.** The block renderer in `spiralpool-blocks` draws a box-drawing header (`╔═╗`) and status glyphs (`✔ ⏳ ✘`), and its `python3 -c` block ended with `" 2>/dev/null`. `spiralctl` delegates every read-only command through `as_pool_user`, which runs `sudo -u "$POOL_USER"`; sudo's `env_reset` is on by default and `LANG` is not in `env_keep`, so the child inherits the POSIX/C locale, Python selects ASCII for stdout, and the first box character raises `UnicodeEncodeError`. The traceback went to the suppressed stderr and the command exited non-zero having printed nothing — indistinguishable from a pool that has never found a block, and unaffected by `sudo spiralctl …` because that only adds a second hop. Reproduced end-to-end against a live pool's `/api/pools/{id}/blocks` payload: identical silent failure under an ASCII stdout encoding, correct rendering with `PYTHONIOENCODING=utf-8`. The renderer now sets that encoding explicitly and no longer discards stderr, so a future failure is visible rather than silent. The sibling suppression in Sentinel's config-value helper is deliberate — it has its own `except` fallback and emits no Unicode — and was left alone. (`install.sh`, generated `spiralpool-blocks`.)
- **The V48 cross-RPC consistency check warned on healthy operation.** It compared the ZMQ tip hash against the template's `previousBlockHash` and warned on any difference. On DigiByte that is a false positive by construction: the chain produces a block every ~15 seconds across five algorithms, so the tip routinely advances between the ZMQ notification and the `getblocktemplate` response, leaving the template built on a *newer* block than the one ZMQ announced. Field logs show the two values crossing over within one second — a hash reported as `templatePrevHash` at 09:04:59 arrives as `zmqTipHash` at 09:05:00 — with the warning firing dozens of times per hour and burying the adjacent "Template unchanged after ZMQ - node slow" warning, which is the one that actually indicates lost work. The hashes also resist eyeball inspection: a DGB block's identity hash is SHA256d whatever algorithm mined it, so only the ~1-in-5 SHA256d blocks carry the leading zeros an operator expects (7 of 49 sampled, against 20% expected). The check now compares **heights** and warns only when the template fails to advance past the height already held — which is what a stuck RPC backend or a same-height reorg actually looks like. (`jobs/manager.go`.)
- **The job-history scaling formula never scaled anything, and the flat floor bit hardest on the fastest chain.** `maxJobHistory` was meant to be `max(10, 2 × blockTime / rebroadcastInterval)` capped at 50, gated behind `blockTime >= 60`. Substituting the interval the formula itself derives — `blockTime / 3` — makes the block time cancel: `2 × bt / (bt/3)` is **exactly 6 for every coin**, which never beats the floor of 10. The branch has been dead since it was written, and every coin has always retained exactly 10 jobs regardless of block time. That floor is a job *count*, but jobs are minted by both the rebroadcast ticker and every new block, so what it buys in wall-clock time varies by an order of magnitude across coins — and shrinks precisely when the rebroadcast interval is shortened. With DGB moving to a 5s rebroadcast (above), 10 jobs would have covered barely 37 seconds, down from ~100s at 30s rebroadcast: the acceptance window for a late share would have *narrowed* as a side effect of an unrelated fix. Sub-60s chains are now sized by wall clock instead, targeting the same 120s the slow-chain path yields, which restores DGB to ~90s. Verified across all 16 supported coins: **13 are bit-identical**, and only DGB (10→24), DGB-Scrypt (10→24) and FBTC (10→12) change — all chains whose templates carry few transactions, so no coin with large templates retains more of them and the 50-job memory cap is never approached. (`jobs/manager.go`.)
- **The `effort` column recorded the finder's share difficulty, not the round's effort.** `handleBlock` set `Effort: share.Difficulty` when building the block record, so the column reported whichever miner happened to win — its vardiff level — rather than how hard the round was. In a 39-block sample the value read exactly `1165` seventeen times (the pool's vardiff floor) while the outliers tracked individual workers: `6779` and `3871` for the same high-difficulty device, `671` for a low-difficulty one. The column was unusable for the round-cost diagnosis it exists to support. Now computed as `(actual round seconds / expected round seconds) × 100` from the pool's current hashrate, matching `CoinPool`'s existing calculation. The value is derived **before** the stats block resets `lastBlockFoundAt` — after that reset the round length is zero — and for every block status, so orphaned and rejected candidates carry a real figure too. The FBTC network-difficulty fallback is preserved. (`pool/pool.go`, new `roundEffortPercent`.)

### Changed
- **Existing DigiByte pools now receive the new `job_rebroadcast` on upgrade.** Changing `config.example.yaml` only reaches fresh installs — `upgrade.sh` deliberately preserves a deployed `config.yaml`, since it carries credentials and per-deployment tuning, so every existing DGB pool would have kept 30s indefinitely and the fix would have shipped to nobody who already runs one. A one-time migration now rewrites it in place before services restart, so the new interval is live after the same restart rather than needing a second. It is deliberately narrow: only the exact value the old template shipped (`30s`) is rewritten, and only inside a `DGB` or `DGB-SCRYPT` block — an operator who tuned the interval to anything else keeps their choice, and every other coin is untouched. Idempotent, backs the config up first, and writes through the existing inode so ownership and mode 600 survive. It is also gated on the upgrade actually crossing into 2.6.5: value-matching alone is narrow but not *one-time*, and without the gate the migration would re-run on every future upgrade and silently flip an operator who had deliberately set `30s` back. An install already at or past 2.6.5 is skipped, because a `30s` there is a choice rather than a stale default. Pre-v2 configs need nothing: `migrate_v2_config` runs first and never writes a `job_rebroadcast` key, and a config without one falls through to the coin-aware default in `config.go`, which computes `blockTime / 3` (minimum 5s) — exactly 5s for DigiByte. (`upgrade.sh`, `migrate_dgb_job_rebroadcast`.)
- **DGB `job_rebroadcast` lowered from 30s to 5s.** The interval was more than twice DigiByte's 15-second block time, so whenever the ZMQ-triggered refresh failed to land, miners could keep hashing a template two blocks stale. That path is not hypothetical: a field pool logged 50 "Template unchanged after ZMQ - node slow" events in a single day. Every other coin in the shipped config sits between a third and a tenth of its block time, and **DGB-Scrypt — the same chain, the same 15-second blocks — was already at 5s**, so the SHA-256d entry was an outlier against its own chain rather than a considered choice. Existing deployments are unaffected until `job_rebroadcast` is changed in their own `config.yaml`; this changes the shipped default only.

- **WAL-DB reconciliation re-inserted every block ever found, every five minutes, forever.** `reconcileWALWithDB` calls `RecoverSubmittedBlocks`, which globs the whole WAL directory and returns every entry in a terminal success state. Those entries are never retired — `CleanupOldWALFiles` only removes files past the 30-day retention window, and an active pool keeps writing to the current file — so each pass re-inserted the pool's entire recent block history. `InsertBlock` is idempotent, so nothing corrupted; the cost is a full DB round-trip per historical block per pass and an `INFO`-level "Block already recorded, skipping duplicate" line for each, both scaling linearly with lifetime blocks found. On a pool with nine blocks in the window that is 108 redundant queries an hour and ~1,600 log lines a day; the growth is what makes it worth fixing rather than the current magnitude. A `sync.Map` of block hashes reconciled during this process now short-circuits the repeat work. Entries whose insert *failed* are deliberately not recorded, so genuine WAL→DB gaps still retry on the next pass — the crash-recovery guarantee the loop exists for is unchanged. The completion log is now emitted only on passes that actually reconciled something, and reports how many entries were skipped as already-done. Reported by an operator who spotted the repeating block list while investigating an unrelated block drought.
- **Pepecoin used Peercoin's Base58 version bytes, so genuine Pepecoin addresses failed pool-side validation.** `PepeCoinP2PKHVersion` was `0x37` (55) where [pepecoinppc/pepecoin `src/chainparams.cpp`](https://github.com/pepecoinppc/pepecoin/blob/master/src/chainparams.cpp) sets `base58Prefixes[PUBKEY_ADDRESS] = 56` (`0x38`); `PepeCoinP2SHVersion` was `0x55` (85) against a reference `SCRIPT_ADDRESS` of `22` (`0x16`). The P2PKH error hid well because 55 and 56 *both* render a leading `P` — the address looks right and only the checksummed version byte disagrees, so the failure surfaces as a flat "invalid pool address" against a string `pepecoind` validates without complaint, and a PEP solo pool cannot be configured with a real address at all. The inverse is the quieter half: a Peercoin address (version 55) was accepted as Pepecoin. That does not corrupt the payout string — `BuildCoinbaseScript` pays the decoded `hash160`, not the version byte — but it silently binds the coinbase to a key hash from the wrong chain's wallet, recoverable only by importing that key somewhere that can spend on Pepecoin. Both constants now match the reference client, along with the `DecodeAddress` error text that enumerated them and the `PEP` entry in `config/coins.manifest.yaml`. The manifest-vs-Go cross-check in `internal/coin/manifest.go` caught the manifest half of the rename automatically; it had been asserting agreement between two copies of the same wrong value since PEP was onboarded. Reported by an operator who verified the mismatch against `pepecoind` on v2.6.2.
- **Myriad Taproot addresses were accepted for payout on a chain that has no Taproot.** `MyriadCoin.DecodeAddress` returned `AddressTypeP2TR` for any well-formed `my1p…` witness-v1 address. Myriad's mainnet chainparams deploy `DEPLOYMENT_SEGWIT` but contain **no `DEPLOYMENT_TAPROOT`** — verified against both [myriadcoin/myriadcoin](https://github.com/myriadcoin/myriadcoin) and [myriadteam/myriadcoin](https://github.com/myriadteam/myriadcoin). Absent Taproot activation a v1 witness output is *anyone-can-spend*, so an operator who pasted a `my1p…` address would have had the pool build coinbases paying an output any third party could sweep — on a solo pool, the entire block reward. Both the decoder and the dashboard pattern now reject witness v1 for XMY with an explanation; witness v0 (`my1q…`, P2WPKH and P2WSH) is unaffected. Found while closing the bech32 gap below rather than from a field report, so there is no evidence of loss — but the exposure was real for anyone who used one.
- **Myriad bech32 addresses were rejected by the dashboard's wallet field.** Beyond the Base58 forms below, Myriad defines `bech32_hrp = "my"`, and `internal/coin` has always decoded `my1q…` — only the dashboard pattern did not, so a valid native-SegWit payout address failed the form. Now accepted for witness v0 at both program lengths: P2WPKH (38 characters after `my1q`) and P2WSH (58), lengths confirmed by generating real addresses against the BIP173 checksum rather than assumed. Verified across 12,000 generated addresses spanning all four accepted forms with zero false rejections, and 200 `my1p…` Taproot addresses all correctly refused.
- **Myriad P2SH addresses were rejected by the dashboard's wallet field.** The `XMY` pattern was anchored `^M`, covering only `PUBKEY_ADDRESS = 50`. Myriad also defines `SCRIPT_ADDRESS = 9`, and an operator pasting a valid P2SH address got a validation failure with no indication of why. The pattern now accepts `^[45M]`. Both leading characters are required: a 21-byte payload prefixed with version 9 does not have a fixed Base58 leading digit — sampling 20,000 random hash160s puts **94.2% at `4` and 5.8% at `5`** — so an `^[4M]` pattern would have kept rejecting roughly one Myriad P2SH address in seventeen, which is a far more confusing bug than the one it replaced. This is dashboard-side input validation only — `internal/coin` already decoded both forms, so mining was never affected for anyone who got past the form.

### Release identity
- Version string moves from `2.6.4` to `2.6.5` across the installer, Sentinel, dashboard, scripts, Docker labels, and documentation. Codename remains **Spiral Citadel** — a patch release on the same tag line.

## [v2.6.4] - 2026-08-06 - Spiral Citadel

Diagnostics and reporting-accuracy release. Five defects that all shared one trait: they made the pool misreport its own state to the operator, while mining itself ran correctly throughout. Two of them are actively misleading rather than merely cosmetic — `/api/pools` advertised a hard-coded `2.4.2-PHI_HASH_REACTOR` on every build, and `spiralctl`'s stats commands reported an idle pool while miners were connected and submitting accepted shares. Between them they can send an operator hunting a mining fault that does not exist. No pool-stack behaviour changes, no database migrations, no config format changes. Drop-in upgrade from v2.6.3.

### Fixed
- **`spiralctl workers` and `spiralctl miners` reported an empty pool while miners were connected.** Both walk `/api/pools` with `jq -r '.[].id'`, but that endpoint returns the envelope `{"software":…,"version":…,"pools":[…]}` — not a bare array. `.[]` iterates an *object's values*, so the first thing it hands `.id` is the string `"spiral-stratum"`, and jq aborts with `Cannot index string with string "id"`. The error was routed to `2>/dev/null` and the failure fed a `while read` loop, so zero iterations was indistinguishable from zero miners: the commands printed a confident `No workers currently connected.` against a pool actively accepting shares. Both call sites now use `.pools[].id`. The same functions also read `.coin.symbol`, a field `CoinInfo` has never defined (it carries `type` and `algorithm`), which `jq -r` renders as the literal string `null` in the COIN column — they now read `.coin.type` and fall back to the pool ID when it is absent. The sibling `/miners` and `/miners/{addr}/workers` selectors were already correct: those endpoints genuinely do return bare arrays.
- **`spiralctl pool stats` printed empty sections for every field it sourced from the stratum API.** The same envelope mismatch in Go form: `json.Decode` into `[]map[string]interface{}` fails outright with `cannot unmarshal object into Go value of type []map[string]interface {}`, and the call site tested `Decode(...) == nil` — so a hard decode error silently skipped the entire block rather than surfacing. Compounding it, the field names it looked for (`hashrate`, `workers`, `blocks`) do not exist at any level of the response; the real counters are nested under `poolStats` as `poolHashrate`, `connectedMiners`, and `blocksFound`. Even a successful decode would have populated nothing. Replaced with a typed struct matching the actual schema, which additionally recovers `networkDifficulty`, `acceptedShares`, `rejectedShares`, and the coin type for the previously blank `[Pool Information]` header. The existing "only fill what an earlier source left at zero" precedence is preserved. Spiral Sentinel consumes the same endpoint but was never affected — `fetch_pool_stats_for_coin` tests for `"pools" in data` and unwraps the envelope correctly.
- **`/api/pools` reported a hard-coded version, not the running build.** `PoolsResponse.Version` was the string literal `"2.4.2-PHI_HASH_REACTOR"` in `internal/api/server.go`, frozen since v2.4.2 and never updated by a release since. Both `install.sh` and `upgrade.sh` already inject the real version at build time via `-ldflags -X main.Version=…` (and `-X …/internal/ha.SpiralPoolVersion=…`), but neither reached the API package, so the literal survived every rebuild. The practical cost is diagnostic: an operator comparing `/api/pools` against `VERSION` sees a multi-release gap that does not exist, and reasonably concludes the upgrade never applied. The version is now a package-level `api.Version` var with a matching `api.Codename` const, injected by both build scripts alongside the existing targets, and the reported string keeps its established `X.Y.Z-CODENAME` shape. `internal/api/server_v2.go` carried the same literal in its V2 handler (`"2.6.3-SPIRAL_CITADEL-V2"`) and now derives from the same vars — it was correct at the time of writing but would have drifted at the next bump exactly as the V1 string did. Ground truth was always available from `spiralpool --version`, which reads the correctly-injected `main.Version`.
- **`upgrade.sh` prompted for wallet-backup acknowledgement even under `--auto`.** The reminder block ran unconditionally, printing an ANSI-coloured banner and calling `read -r`, while every other interactive prompt in the script is guarded by `[[ "$AUTO_MODE" != "true" ]] && [[ -t 0 ]]`. Under the dashboard's `upgrade.sh --auto` invocation stdin is at EOF, so `read` returned immediately and the upgrade completed — the visible symptom was only the banner appearing in the dashboard's result panel. The latent failure is worse than the cosmetic one: `subprocess.run` inherits the caller's stdin, and in any launch context where that is a live pipe rather than `/dev/null`, `read` blocks until the endpoint's 5-minute timeout and reports a spurious "Upgrade timed out". The block now uses the same guard as its siblings; interactive runs are unchanged and still show the reminder in full.
- **The dashboard rendered raw ANSI escape codes in upgrade output.** `/api/system/pool-upgrade/apply` passed `upgrade.sh`'s stdout/stderr straight into its JSON response, so `${RED}`/`${WHITE}`/`${NC}` reached the browser as literal `[0m` / `[1;33m` sequences. The truncation compounded it: the payload was sliced to `[-500:]` *before* any stripping, which can cut an escape sequence in half and, separately, is why the panel opened mid-word. Escape sequences (CSI and OSC) are now stripped before truncation, on both the success and failure paths. The corresponding `app.logger.warning` call still logs raw stderr — the codes are noise in the journal but harmless, and it was left alone to keep the change scoped.

### Release identity
- Version string moves from `2.6.3` to `2.6.4` across the installer, Sentinel, dashboard, scripts, Docker labels, and documentation. Codename remains **Spiral Citadel** — a patch release on the same tag line. One version reference is deliberately *not* bumped: the `# XEC (eCash) daemon control (v2.6.3)` comment `upgrade.sh` writes into the dashboard sudoers file records which release introduced that entry, and is historical rather than current.

## [v2.6.3] - 2026-07-27 - Spiral Citadel

DigiByte node-upgrade release. The bundled DigiByte Core daemon moves from **9.26.4 to 9.26.5**, which fixes a **DigiDollar oracle startup stall** that held node initialization — and therefore DGB block templates — for 15 minutes or considerably longer on *every* daemon restart. v9.26.5 changes no consensus rules on mainnet or testnet, so there is no coordination deadline; upgrading is an in-place binary swap with **no reindex and no config changes**, for full and pruned nodes alike. The pool stack itself is a drop-in upgrade from v2.6.2 — no database migrations, no config format changes. See [UPGRADE_GUIDE.md](docs/setup/UPGRADE_GUIDE.md).

### Changed
- **DigiByte Core upgraded to v9.26.5** (from 9.26.4) across `install.sh`, `coin-upgrade.sh`, `upgrade.sh`, the Docker images, `regtest.sh`, and the config/upgrade tests. v9.26.4 re-evaluated the DigiDollar activation gate once per scanned block during startup — allocating a throwaway versionbits cache and re-running the BIP9 threshold state machine roughly 172,800 times — so `AppInitMain` never reached `SetRPCWarmupFinished()` for 15+ minutes. v9.26.5 reuses the node's shared memoized versionbits cache (the same lookup block validation already performs) and the scan completes in ~3 seconds. v9.26.4's pruning support and its narrowly-scoped DigiDollar consensus rule (redemption collateral gated on the activation floor, mainnet height 23,627,520) carry forward unchanged, so nodes still on 9.26.3 pick that rule up in this upgrade — which is why DGB stays classified **MINOR** rather than PATCH.
- **The DGB upgrade notice describes the oracle fix.** `upgrade.sh`'s console notice and its Discord coin-upgrade summary previously announced v9.26.4's consensus rule; they now lead with the v9.26.5 startup-stall fix and keep the optional-pruning offer as the secondary note.
- **`coin-upgrade.sh`'s one-time DGB pruning offer is gated on the target version, not a hard-coded `9.26.4`.** The gate read `installed_ver != "9.26.4"`, which was correct only while 9.26.4 *was* the target. With the pin moved to 9.26.5 it inverted: a full node already on 9.26.4 — precisely the population the offer exists for — failed the test and was never asked, while any node further behind that had declined would have been re-prompted at every future bump. It now compares against `${COIN_TARGET[DGB]}`, so every full DGB node below the target gets the offer exactly once, it cannot re-fire once the node is at target, and the gate needs no editing on the next version bump.

### Fixed
- **DGB block templates were unavailable — and miners' shares rejected — for 15+ minutes after every daemon restart.** The symptom is easy to misread as a corrupt or hung node: `digibyte-cli getblockchaininfo` returns `error -28 "Starting network threads…"`, `debug.log` shows `Oracle: Scanning last 172800 blocks for oracle prices` with no completion line, and the process sits at 100% of one CPU core. The tell that it is neither disk-bound nor deadlocked is that `/proc/<pid>/io` `read_bytes` stays completely flat — the scan iterates the in-memory block index and never touches block files — while the main thread shows no syscall in its backtrace and the `b-addcon`/`b-opencon` threads block behind the lock it holds. Resolved by the v9.26.5 daemon upgrade above; no chain data, config, or resync is involved. `UPGRADE_GUIDE.md` documents the recognition steps.
- **`install.sh` reported a corrupted stratum service state.** All three stratum status checks used `stratum_state=$(systemctl is-active spiralstratum 2>/dev/null || echo "unknown")`. `systemctl is-active` exits **non-zero for every state except `active`** while still printing the state name, so the `|| echo` branch always fired and appended a second line — leaving `stratum_state` as the literal two-line string `"activating\nunknown"`. Every downstream comparison against `activating`, `inactive`, `auto-restart`, and `failed` therefore never matched: the per-state backoff intervals in the polling loops were dead code, the friendly "starting up (waiting for node RPC)" message was unreachable, and the operator saw a mangled `(state: activating` / `unknown)` spread across two lines. The exit code is now ignored and an empty result falls back to `unknown`. (`install.sh`, three sites.)
- **`wait-for-node.sh` hid which coin was blocking pool startup.** `check_all_nodes` printed a `✓ / ✗ <SYMBOL> node ready` line per coin to stdout, but all three callers invoke it as `$(… | check_all_nodes | tail -1)` to read the one-word summary — so the per-coin lines were captured by the command substitution and discarded. On a multi-coin node the operator saw only `Checking nodes... (N s / 1800 s)` repeating for up to 30 minutes with no indication of which daemon was failing its readiness gate. Those lines now go to stderr (matching `check_rpc`, which already logs there), so they reach the journal while the summary still flows to the caller.
- **`coin-upgrade.sh` false-warned on every healthy DigiByte upgrade.** `wait_for_daemon` allowed a flat 120 s for any coin, but DGB reloads a ~24-million-entry block index before opening RPC — 4-5 minutes on ordinary hardware — so a completely successful upgrade always ended with `⚠ DGB did not respond within 120s — may still be starting or reindexing`, which reads as a failure. DGB now gets a 600 s budget, and while waiting, the daemon's `error -28` init stage (`Loading block index…`, `Verifying blocks…`, `Pruning blockstore…`) is echoed as it changes, so the wait shows progress instead of appearing hung.

### Release identity
- Version string moves from `2.6.2` to `2.6.3` across the installer, Sentinel, dashboard, scripts, Docker labels, and documentation. Codename remains **Spiral Citadel** — a patch release on the same tag line.

## [v2.6.2] - 2026-07-06 - Spiral Citadel

DigiByte node-upgrade release. The bundled DigiByte Core daemon moves from **9.26.3 to 9.26.4**, which makes **DigiDollar compatible with pruning** — reversing the v9.26.3 restriction that forced every DGB node to run a full, txindexed archive. A pruned v9.26.4 node keeps only the `[DigiDollar-activation-floor, tip]` window (a few GB) instead of the full ~80 GB node (block history + transaction index), turns `txindex` off automatically, and still validates, mines, mints, sends, redeems, and can run a DigiDollar oracle exactly like a full node. Upgrading a full node is an in-place binary swap with **no reindex**. The pool stack itself is a drop-in upgrade from v2.6.1 — no database migrations, no config format changes. See [UPGRADE_GUIDE.md](docs/setup/UPGRADE_GUIDE.md).

### Changed
- **DigiByte Core upgraded to v9.26.4** (from 9.26.3) across `install.sh`, `coin-upgrade.sh`, `upgrade.sh`, the Docker images, `regtest.sh`, and the config test. v9.26.4 is a patch on top of v9.26.3 that adds **one narrowly-scoped DigiDollar consensus rule** (redemption collateral classification is gated on the activation floor, so pruned and full nodes reach identical verdicts). The Groestl algolock and DigiDollar BIP9 deployment carry forward unchanged. Treat it as a consensus-rule addition when assessing upgrade urgency; a full node that does not set `-prune` behaves like v9.26.3 apart from this rule.
- **DigiByte pruning is supported again.** DGB rejoins the pool-wide prune toggle exactly like BTC/BCH/LTC: with pruning enabled it uses `prune=5000` (~5 GB) and no `txindex` (v9.26.4 drops the index automatically under prune); as a full node it keeps `txindex=1`/`prune=0`. Applies to `install.sh` (the pruning prompt and both generated `digibyte.conf` blocks), `scripts/linux/pool-mode.sh` (DGB now calls `get_existing_prune`), `scripts/spiralctl.sh` (`spiralctl coin prune DGB` is no longer blocked), and the Docker config template (documents how to enable pruning).

### Added
- **One-time pruning offer on the DGB upgrade.** Because v9.26.3 required a full node, any DGB node reaching `coin-upgrade.sh` is coming from a full node — so the 9.26.x → v9.26.4 upgrade now asks whether to switch DGB to a pruned node. If accepted it edits `digibyte.conf` in place (sets `prune=5000`, removes `txindex`) after backing it up, and the node prunes in place with **no resync**. Declining leaves it a full node. DGB is reclassified from **MAJOR** to **MINOR** (in-place binary swap, no forced reindex).
- **Enabling pruning actually reclaims the disk, and sets expectations.** When the offer is accepted, `coin-upgrade.sh` also deletes the now-orphaned `indexes/txindex/` directory (built while the node was full; unused under prune, and Core never deletes it on its own) so that space is freed immediately. The prompt and completion log warn that on its **first start** the daemon runs a one-time block-store prune (`getblockchaininfo` returns `error -28 "Pruning blockstore…"`) during which DGB serves no templates, so **DGB miners' shares are rejected until it completes** (duration varies with chain size, disk speed, and load — several minutes to an hour or more for a full DGB node, with no fixed figure) — with the exact command to watch progress; after that first pass, ongoing pruning is gradual/background and mining runs normally. The same warning is shown by `spiralctl coin prune` and documented in [UPGRADE_GUIDE.md](docs/setup/UPGRADE_GUIDE.md).
- **`install.sh` preserves an existing prune setting on reconfigure.** Regenerating `digibyte.conf` no longer reverts a deliberately-pruned DGB node (pruned via the installer, `coin-upgrade.sh`, or `spiralctl`) back to a full node — it only ever preserves the pruned state; enabling prune still wins.

### Removed
- **The v9.26.3 forced pruned→full migration.** `coin-upgrade.sh` no longer detects a pruned DGB node, demands a `UPGRADE` confirmation, rewrites the config to a full node, and reindexes — v9.26.4 no longer requires any of that. The `dgb_needs_pruning_migration` / `dgb_free_gb` / `dgb_apply_config_migration` helpers and their upgrade-path gate are gone.

### Fixed
- **`coin-upgrade.sh` could leave a stale `reindex-once.conf` systemd drop-in.** The drop-in that appends `-reindex` for a MAJOR upgrade is meant to be deleted right after the daemon starts; if that cleanup didn't complete, the leftover silently forced a **full chainstate rebuild on the next daemon restart** — potentially days later, when an unrelated upgrade bounced the node (the block store stays intact, so it rebuilds from local blocks rather than re-downloading, but it takes the node offline for the rebuild). The tool now removes any stale reindex drop-in before starting the daemon for every coin, and `coin-upgrade.sh --check` (and the version-status table) now flag any that are present along with the exact command to clear them.
- **`coin-upgrade.sh` exited right after the version-status table instead of offering the upgrade.** The new `warn_stale_reindex_dropins` helper ended in `[[ $found == true ]] && echo ""`, which returns non-zero whenever there are no stale drop-ins (the normal case). Under `set -euo pipefail` that non-zero return propagated through `show_version_table` and aborted the script **before the interactive upgrade menu ever ran** — and before `--coin <TICKER>`'s `upgrade_coin` call — so no coin could actually be upgraded (the status table printed, then the script silently exited 1). The helper now always returns 0. (`coin-upgrade.sh`.)
- **A pruned config could leave the live daemon still running as a full node, with no signal to the operator.** Two paths caused the drift: `install.sh` rewrites `digibyte.conf` on a reconfigure, but the daemon is (re)started later with `systemctl start`, which is a no-op on an already-running unit — so the freshly-written `prune=5000` never took effect on the live node; and `spiralctl coin prune <COIN>` early-returned `already configured` whenever the config already had `prune` set, without ever checking whether the running node was actually pruned. `install.sh` now restarts `digibyted` if it is already active after regenerating the config, and `spiralctl coin prune` now queries `getblockchaininfo` and — on drift (config pruned but the daemon reports `"pruned": false`) — comments out any stale `txindex`, removes the orphaned `indexes/txindex` directory to reclaim disk, and restarts the daemon to apply pruning. Already-pruned and daemon-not-responding cases remain no-ops. (`install.sh`, `scripts/spiralctl.sh`.)
- **"Blocks Found" widget was capped in multi-coin setups.** The Stratum API's `handlePoolBlocks` handler defaulted to returning only 100 blocks per pool when no `pageSize` was supplied, and the dashboard fetched `/api/pools/{pid}/blocks` without one — so a fleet with several coins had its aggregated block widget and block leaderboard silently truncated. The handler now defaults to the maximum supported limit (5000, unchanged cap) and the dashboard explicitly requests `?pageSize=5000` at both fetch sites. (`src/stratum/internal/api/server.go`, `src/dashboard/dashboard.py`.) Thanks to [Kamakhu](https://github.com/bkhuraijam) ([`8f4aa5d`](https://github.com/bkhuraijam/Spiral-Pool/commit/8f4aa5d97fef47f828257fdc77cfc5ccda932e3d)).

### Release identity
- Version string moves from `2.6.1` to `2.6.2` across the installer, Sentinel, dashboard, scripts, Docker labels, and documentation. Codename remains **Spiral Citadel** — a patch release on the same tag line.

## [v2.6.1] - 2026-07-04 - Spiral Citadel

Sentinel alert-management release. Adds an operator-facing way to silence individual alerts and periodic reports without hand-editing config, and lifts the previous limitation that only alerts with a dedicated `*_enabled` flag could be turned off. Drop-in upgrade from v2.6.0 — no database migrations, no config format changes; existing `config.json` files gain the feature automatically (the new key defaults to empty, so behaviour is unchanged until an operator opts in).

### Added
- **Per-alert/report mute list (`spiralctl alerts`).** A new `spiralctl alerts [list|disable|enable|reset] <type>` command turns any individual Sentinel alert or scheduled report on or off. It is backed by a new `disabled_alerts` list in the Sentinel `config.json`, enforced by a single guard at the top of `send_alert()` — the one gate every notification passes through (native fleet alerts, Prometheus `infra_*` alerts, Go-bridged `pool_*` alerts, and the 6h/weekly/monthly/quarterly reports). The guard matches the exact `alert_type` or its `infra_`/`pool_`-stripped canonical name, so one name silences every variant. `block_found` can never be muted and unknown/mistyped names are rejected. Unlike the per-feature `*_enabled` flags (which exist for only ~a dozen alerts), this works for every alert type — including flag-less ones such as `zombie_miner`, `miner_reboot`, and `hashrate_divergence`. Changes require a Sentinel restart to take effect. (`sentinel/SpiralSentinel.py`, `scripts/spiralctl.sh`; documented in `SENTINEL.md`, `SentinelConfig.md`, `spiralctl-reference.md`.)

### Fixed
- **Upgrade summary falsely reported `spiralstratum` as FAILED when it actually started fine.** The post-upgrade status summary in `upgrade.sh` waited only for `spiraldash`/`spiralsentinel` to become active (stratum is intentionally excluded because its `ExecStartPre` node-wait can take much longer), then took a **single** `systemctl is-active` snapshot of stratum. Because stratum runs with `Restart=always` (`RestartSec=10`) and is started `--no-block` alongside PostgreSQL and the blockchain daemons, that one sample frequently landed mid-restart (`failed`/`auto-restart`) on the first attempt — printing `FAILED` and the "this is NOT normal startup" warning even though systemd brought the service online seconds later. The summary now polls stratum for up to 60 s (letting `Restart=always` settle) before classifying its state, mirroring the retry logic already used at the other stratum-startup call sites in `install.sh`; a genuinely still-failed service after the poll is still reported as `FAILED`, and a service legitimately waiting on the node still shows `Starting`. (`upgrade.sh`.)

### Release identity
- Version string moves from `2.6.0` to `2.6.1` across the installer, Sentinel, dashboard, scripts, Docker labels, and documentation. Codename remains **Spiral Citadel** — a patch release on the same tag line.

## [v2.6.0] - 2026-07-02 - Spiral Citadel

DigiByte node-upgrade release, and the start of the **Spiral Citadel** codename line (succeeding Phi Hash Reactor). The bundled DigiByte Core daemon moves from **8.26.2 to 9.26.3** — a **mandatory** network-consensus upgrade for both DGB (SHA-256d) and DGB-Scrypt, which share one daemon. The pool stack itself is a drop-in upgrade from v2.5.3 (no database migrations, no config format changes); the DGB **node** upgrade is a separate, operator-driven step handled by `coin-upgrade.sh` — see [UPGRADE_GUIDE.md](docs/setup/UPGRADE_GUIDE.md). **This release is also a required pool-side fix** for anyone already on v9.26.3: the node's new DigiDollar version bit broke ASIC share validation and halved BM1366 hashrate until the stratum layer was corrected — see **Fixed** below.

### Changed
- **DigiByte Core upgraded to v9.26.3** (from 8.26.2) across `install.sh`, `coin-upgrade.sh`, `upgrade.sh`, the Docker images, `regtest.sh`, and the config test. This is a **mandatory consensus upgrade**: v9.26.3 restores retired-algorithm (Groestl) enforcement, which activates at mainnet block **23,808,000** regardless of signaling. Every DGB node must be on v9.26.3 before that height.

### Breaking
- **Pruning is no longer supported for DigiByte (DGB).** DigiByte Core v9.26.3 makes `txindex` mandatory on mainnet (required by DigiDollar), and `txindex` is mutually exclusive with `prune`, so a pruned DGB node **refuses to start** on v9.26.3. Changes:
  - `install.sh` now hard-codes `txindex=1` / `prune=0` in `digibyte.conf` regardless of the global pruning choice, and the pruning prompt states DGB is excluded (it always runs as a full node, ~80 GB).
  - `coin-upgrade.sh` classifies DGB as **MAJOR**. When it detects a *pruned* DGB node it shows an explicit warning, checks free disk space, requires the operator to type `UPGRADE` to accept that pruning is removed, migrates the config (comments out `prune=`, sets `txindex=1` — nothing else is touched, and no chain data or wallets are deleted), and starts the daemon with `-reindex` to fully resync.
  - `upgrade.sh` surfaces a separate MAJOR notice (console + Discord) when a DGB upgrade is pending, explaining the pruning removal and resync.
  - `spiralctl prune enable DGB` is now blocked with an explanatory error.

### Added
- **DigiDollar-aware mining (Phase 2).** The pool now requests the `digidollar-oracle` getblocktemplate rule for DGB and DGB-Scrypt, and — when the node returns `default_oracle_commitment` (DigiDollar active with a fresh MuSig2 oracle bundle) — copies that script verbatim into the coinbase as a single zero-value output, appended after the witness commitment in *both* the solo and merge-mining-parent coinbase builders. This is **self-gating**: with no commitment present the pool mines normal DGB blocks exactly as before, so mining is unaffected before and through BIP9 activation. Unit-tested in `jobs_building_test.go` and `digibyte_gbtrules_test.go`; **pending end-to-end validation on testnet26** (DigiDollar active at block 600) before mainnet activation. Note: DigiDollar is not a mining reward — the pool carries users' mint/redeem transactions and still earns the normal DGB block reward plus fees.
- **BIP9 version-bits status captured from `getblocktemplate`.** `BlockTemplate` now reads the node's `vbavailable` (map of signalable soft-fork → version-bit, e.g. `{"digidollar": 23}`) and `vbrequired` (mask of bits the node *requires* be set) fields. These are read-only for visibility and safety-gating — the pool does **not** auto-apply `vbavailable` bits — and confirm the property our fix relies on: the DigiDollar bit is advertised as optional (`vbavailable`) and never mandatory (`vbrequired`), so clearing it cannot invalidate a block. Field support originally contributed by **Kamakhu** (`3277cce`); Spiral Pool's DigiDollar oracle coinbase integration above is a separate, independently-written implementation.

### Fixed
- **All version-rolled shares rejected as "Low difficulty" after the DGB v9.26.3 upgrade.** DigiByte Core v9.26.3 sets BIP9 bit `0x00800000` (bit 23, the DigiDollar signal) in the block template's base version — inside the BIP320 version-rolling mask. The share validator reconstructed the header version as `(v &^ mask) | (bits & mask)`, which *stripped* that daemon-set bit, so every version-rolled (ASICBoost) share's reconstructed header disagreed with the miner's and failed the difficulty check — ASICs saw 100% rejects. Reconstruction now ORs the rolled bits onto the full daemon version (`v |= bits & mask`), preserving base bits. The same builder produces submitted blocks, so block solutions are correct too. (`shares/validator.go`, regression test `TestVersionRollingPreservesDaemonSetBits`.)
- **BM1366 miners (e.g. NMAxe) mining DGB at ~half nameplate hashrate after the upgrade.** A single-chip BM1366 dropped from ~500 to ~250 GH/s the moment the pool served v9.26.3 templates. Root cause: the BM1366's version-rolling silicon loses roughly half its effective hashrate when bit `0x00800000` is set in the base version it rolls from (multi-chip BM1370 miners are unaffected — a chip-level difference). The pool now clears that **optional** DigiDollar signal bit from the miner-facing job version for DGB and DGB-Scrypt, restoring pre-upgrade hashrate (verified in the field: 262 → 458 GH/s). As defense in depth, the advertised version-rolling mask also excludes any daemon-set base-version bits so a miner cannot roll them. **Trade-off:** while this is in effect the pool does not signal DigiDollar activation; it remains reversible. Safe because the bit is optional per BIP9 (see `vbavailable`/`vbrequired` above). (`jobs/manager.go`, `stratum/v1/handler.go`, `stratum/server.go`.)
- **Sentinel misclassified the NMAxe as an unknown miner.** `SpiralSentinel.py`'s device-info parser only understood the flat AxeOS JSON schema; ESP-Miner v3.0.21 on the NMAxe returns a nested schema (`identity`/`asic`/`miner` objects), so the miner fell through to "Unknown" and was never pushed a proper difficulty class. The parser now reads both the flat and nested shapes (model, ASIC model/count, hashrate), correctly classifying the BM1366 as a low-difficulty device. (`sentinel/SpiralSentinel.py`.)

### Release identity
- Codename advanced from **Phi Hash Reactor** to **Spiral Citadel**, and the version string moves from `2.5.3` to `2.6.0` across the installer, Sentinel, dashboard, scripts, Docker labels, and documentation. Historical changelog entries retain their original Phi Hash Reactor codename.

## [v2.5.3] - 2026-06-21 - Phi Hash Reactor

Sentinel alerting-reliability release. A reported "Zombie state" chronic alert turned out to be a false positive: a healthy NerdQAxe/BitAxe-class miner whose *self-reported* (cgminer/Avalon API) hardware-reject rate spiked above 90%, while the pool itself was accepting its shares at a ~2.7% reject rate. That prompted a full audit of every Sentinel alert for the same class of defect — trusting untrusted, miner-self-reported data (or a transient/partial reading from an external source) without cross-referencing the authoritative pool-side signal, and counting raw detection cycles instead of delivered alerts. The audit produced the fixes below, all in `SpiralSentinel.py`. Drop-in upgrade from v2.5.2 — no database migrations, no config format changes, and no manual steps required. Every new threshold is config-overridable.

This release also completes the Q-BitX (QBX) coin removal begun in v2.5.2 — which shipped with orphaned QBX artifacts still present in the Go backend, dashboard, and installer — and fixes several installer and deployment issues surfaced while auditing that cleanup, including a long-standing gap where eCash (XEC) was never written into the Docker stratum config (see **Fixed (installer & deployment)** below).

### Fixed
- **Zombie false positive from miner-reported hardware rejects** — `check_zombie_miner()`'s Method 1 flagged a miner as a zombie at a ≥90% reject rate computed purely from the miner's own share counters. BitAxe/Avalon firmware counts internal hardware rejects that never reach the pool, inflating the self-reported rate far above the true pool-side rate (95–100% reported vs 2.7% pool-side in the field case), so a healthy miner was repeatedly flagged **and auto-kicked**. The reject-rate verdict (`status == "zombie"`) is now gated behind the same pool-side cross-reference the share-rejection-spike detector already uses, via a new `compute_pool_side_reject_pct()` helper reading `stratum_shares_accepted_total` / `stratum_shares_rejected_total` (stale-excluded) from Prometheus; it only fires when pool-side reject also exceeds `POOL_REJECT_CONFIRM_PCT` (default 5%). The `no_shares` (truly idle) and `pool_invisible` paths are unchanged.
- **Chronic-issue alert counted detection cycles, not delivered alerts** — `track_chronic_issue()` was called every ~2-minute monitoring cycle the condition persisted, so a single ongoing issue reached the 5× chronic threshold in ~10 minutes ("occurred 5× over 0.2 hours"). Count increments are now throttled to once per `CHRONIC_COUNT_MIN_INTERVAL` (default 1 hour) per (miner, alert type) — `last_seen` still refreshes every cycle so the 2-hour auto-reset reflects true recurrence, but the count advances at most hourly. Counting from detection (rather than from alert delivery) keeps chronic tracking independent of the global per-alert-type send cooldown, so multiple simultaneously-affected miners each accrue their own chronic count. The `miner_offline` site additionally no longer re-counts (or double-increments `weekly_stats`) once per group-grace cycle.
- **Thermal shutdown could trigger on a single implausible temperature** — the emergency-stop path acted on the first miner-reported reading at or above the emergency threshold with no upper sanity bound, so a single glitched/misparsed sample could `emergency_stop_axeos()` a healthy miner. Readings above `TEMP_SANITY_MAX` (default 150 °C, well above the 95 °C emergency band a real runaway trips first) are now treated as sensor glitches and ignored, in both the per-miner and group-temp paths; normal-range readings (including the recovery/clear branch) are unaffected.
- **Wallet-drop alert fired on a transient non-zero balance dip** — only a balance reading of exactly 0 had multi-read confirmation; any other decrease fired a panic-grade "possible theft" alert immediately, so a partial `scantxoutset` or a flaky external balance API could false-alarm. Confirmation is now generalized to **all** drops (`WALLET_DROP_CONFIRM_READS`, default 3 consecutive reads); the pre-drop balance is held while pending, so a real drain still alerts after a couple of cycles while a transient reading self-clears.
- **False offline / auto-restart when a miner's HTTP API was briefly unreachable** — offline status was derived solely from the local HTTP poll, so a miner mining fine to the pool but with a momentarily-unreachable web API was flagged offline and could be force-restarted. A new `is_miner_connected_to_pool()` cross-reference reclassifies such a miner as online — but only on positive evidence (the admin connections API is configured and the miner matches a live stratum connection by IP or worker name); when it cannot verify, the miner is left offline so genuine outages are never masked.
- **Per-worker hashrate-divergence never fired (unit mismatch)** — in per-worker mode the pool hashrate (H/s) was compared against the miner hashrate (GH/s) without conversion, making the ratio ~1e9× off so divergence was silently never detected. The per-worker path now converts pool hashrate to GH/s using the same heuristic as the aggregate path.
- **Fan-failure and dead-hashboard alerts fired on a single cycle** — a single 0-RPM or 0-hashrate sample (routine during the miner's own fan ramp or board re-init) triggered an alert. Both now require the condition to persist (`FAN_FAILURE_SUSTAINED_SEC` / `HASHBOARD_DEAD_SUSTAINED_SEC`, default 90 s ≈ one confirming cycle) and reset on recovery.
- **Power-event / miner-reboot false positives from clock skew** — any backward step in a miner's self-reported uptime counted as a reboot, so an NTP step correction (and two coinciding ones) produced false `miner_reboot` and `power_event` alerts. A reboot now requires the uptime to drop by more than `UPTIME_REBOOT_MIN_DROP_SEC` (default 120 s), the signature of a real counter reset.
- **Coin-node-down fired on a single transient pool-API timeout** — `handle_coin_health_alerts()` now requires `COIN_NODE_DOWN_CONFIRM` consecutive failing health checks (default 2 ≈ 10 minutes) before alerting, tracked via a per-coin failure streak; the recovery alert only fires if a node-down was actually sent (no more orphan "recovered" notices after a one-check blip).
- **HA VIP and state-change alert storms during normal failover** — `vip_change` and `ha_state_change` fired immediately on any transition, so a normal keepalived election (running→failover→running within seconds) produced multiple alerts. Both are now debounced with the same confirm-and-revert logic already used for role changes, via a shared `_debounce_ha_scalar()` helper (`HA_ROLE_CHANGE_CONFIRM_SECS`).
- **HA replica-drop fired on a momentary disconnect** — a replica that briefly dropped during maintenance/failover fired a red alert. The drop must now persist across two consecutive checks (the baseline is held while pending) before alerting.
- **Infrastructure-metric alerts misfired on stratum restarts and partial scrapes** — `worker_count_drop` now skips a 0/missing reading (scrape gap / reconnect) and requires two consecutive sub-threshold reads; `block_notify_mode_change`'s getter returns `None` (not `0`) when the metric is absent, so a partial scrape no longer flips ZMQ→polling→ZMQ; `wal_errors` re-baselines on a counter reset instead of masking post-restart errors behind a stale high-water mark, and gained a send cooldown.
- **Financial alerts misfired on transient price-feed problems** — `price_crash` now requires two consecutive samples below the crash threshold against a ~2-hour baseline (a single thin-liquidity/exchange-glitch tick no longer fires; sustained crashes still alert one sample later); `revenue_decline` is skipped when any coin with earnings has a missing/zero price (a partial price feed no longer manufactures a decline); and `payout_received` ignores sub-`PAYOUT_MIN_CHANGE` increases as dust/rounding noise between balance sources. The balance recovery that ends a pending (unconfirmed) wallet drop is no longer mistaken for a credit, so a transient low reading that comes back up no longer fires a phantom payout.
- **Follow-up review hardening** — `coin_node_down` confirmation now advances once per *fresh* 5-minute health check rather than once per ~2-minute monitor loop on cached results (the streak previously confirmed in ~one real check); the `coin_node_down` recovery alert and the `ha_replica_drop` baseline advance are now gated on the alert actually being delivered (a cooldown/quiet-hours-suppressed event is retried instead of silently absorbing the change, matching the wallet-drop anchoring); and the `worker_count_drop` confirmation streak resets on an unreadable (0 / missing) sample so a scrape gap can't count toward confirmation. All of the above are covered by `tests/test_alert_debounce.py` (18 tests).

### Fixed (installer & deployment)
- **Completed the Q-BitX (QBX) removal** — v2.5.2 announced QBX removal but shipped with orphaned artifacts that silently mis-configured unrelated coins. `dashboard.py` carried a dangling `default_port = 8344` (QBX's defunct RPC port) that immediately overwrote Fractal Bitcoin's correct `8340`, so FBTC node auto-detection probed a dead port. The Go backend had an empty-key sync-requirements map entry and an empty `case ""` in `GetDefaultRPCPort` both returning the dead QBX port `8344`, a stray blank line in `spiralctl node` help, and the QBX stratum port `20335` left in the discovery scanner's port list. All removed (`go build` / `go vet` / `go test` clean). QBX is now absent from every tracked file — earlier removal passes had skipped the Go backend.
- **Coin-selection menu numbering gaps closed** — removing QBX left a hole in the numbered coin menus (the solo and multi-coin toggle menus in `install.sh`, the three coin menus in `scripts/linux/pool-mode.sh`, and the coin menu in `install-windows.ps1`), so the list skipped a number and one menu position routed to no coin. All renumbered contiguously, with display labels and case handlers verified to map to the same coin; the `wsl2-stratum-proxy.ps1` coin table was likewise closed. Documentation port tables referencing the removed QBX ports (20335–20337) were cleaned.
- **eCash (XEC) was never emitted into the Docker stratum config** — a long-standing gap: XEC was added to the native installer's stratum config generator but not the Docker one. Enabling eCash in Docker mode started the `ecash` daemon container, but `generate_docker_stratum_config_multicoin` produced a `config.yaml` with no XEC pool, so the stratum coordinator never served XEC and miners could not connect on port 18338. Added the XEC pool block (stratum `18338`, node `ecash:9004` user `spiralxec`, ZMQ `tcp://ecash:28335`), bringing the Docker generator to parity with the native one at 16 coins.
- **HA Docker stack could not start (YAML parse error)** — `docker-compose.ha.yml` carried an orphaned `depends_on` entry left over from the v2.5.2 QBX service removal (a bare ` :` key with a dangling `condition`/`required` block), so the file failed YAML parsing and any HA deployment (`docker compose -f docker-compose.yml -f docker-compose.ha.yml --profile <coin> --profile ha up`) aborted immediately — HA mode was wholly unbootable, though single-node compose was unaffected. Removed the dead entry (the stack now parses to 9 services and boots). A stray QBX reference in the commented-out multi-port schedule example in `config.example.yaml` was cleaned up at the same time.
- **HA regtest failover daemon lost `-fallbackfee`** — while clearing the blanked QBX alternative out of a coin-matching regex in `scripts/linux/regtest.sh`, the entire conditional that adds `-fallbackfee=0.0001` to the HA (VIP) daemon for BTC/LTC/DOGE/PEP/CAT/FBTC had been removed, so wallet sends against the failover daemon failed with "Fee estimation failed." Restored, with the QBX alternative dropped from the regex.
- **WSL2 proxy script could not run** — `scripts/windows/wsl2-stratum-proxy.ps1` had two pre-existing PowerShell parse errors where `$lanIP:` and `$port:` inside interpolated strings were parsed as drive-qualified variable references; fixed with `${...}` delimiting so the script parses and runs.
- **Completed eCash (XEC) integration across all subsystems** — a coin-enumeration audit found XEC (the most recently added coin) was only partially wired up, leaving it missing from ~18 sites unrelated to QBX. Installer: XEC's daemon `ecashd` was absent from the daemon-management loops and the sudoers NOPASSWD list (the dashboard/health-monitor could not restart/start/stop the eCash node), and the solo-mode stratum-port map had no XEC arm (a solo XEC install advertised DGB's port 3333 instead of 18338); `pool-mode.sh` could not detect or version-check the eCash node; `CREDENTIALS.txt` omitted BCH2/BTCS. Sentinel: XEC had no network-hashrate favorability bands, no ZMQ-stale threshold, no block-explorer link, and **no price source** — so all XEC fiat/sats valuation, revenue, and price-crash detection were dead; added XEC to `COIN_THRESHOLDS`, `COIN_ZMQ_STALE_THRESHOLDS`, `BLOCK_EXPLORER_URLS`, and the CoinGecko price fetch (`fetch_xec_price` + bulk fetch, id `ecash`). Dashboard: XEC was missing from every config/POOL_ID coin-detection path, and several detection chains mis-detected `BCH2→BCH` and `BTCS→BTC` by substring shadowing — added XEC throughout and reordered the chains so specific symbols match before generic ones. Secondary BCH2/BTCS/SYS/DGB-SCRYPT enumeration gaps in the same sites were closed. None of this was caused by the QBX removal; it predated this release.

### Security & robustness (installer / upgrade)
- **A failed re-run no longer wipes a pre-existing install** — `install.sh`'s `cleanup_on_failure` would `rm -rf $INSTALL_DIR` (plus the pool user/group and per-coin blockchain data) when the operator chose "clean up" after a failure. On a *re-run* against a working pool (e.g. adding a coin) that could destroy already-synced chains, wallets, and configs. The installer now records whether `$INSTALL_DIR` existed before the run and refuses automatic destructive cleanup of a pre-existing install (manual removal is still offered).
- **`upgrade.sh --auto` no longer silently confirms a failed wallet backup** — unattended runs created the per-coin `.backup-confirmed` marker even when the wallet backup failed or was skipped, permanently suppressing future attempts (a fund-loss risk if the host later died). In `--auto` the marker is now written only when the backup actually succeeded; failures are left unconfirmed and logged so a later run retries.
- **Unverified Bitcoin download is now fail-closed** — when the Bitcoin Knots `SHA256SUMS` cannot be fetched, the installer no longer proceeds without verification; it retries/aborts unless explicitly overridden with `ALLOW_UNVERIFIED_BTC_DOWNLOAD=true`.
- **Coinbase text is sanitized** — operator-supplied coinbase text is stripped of dangerous metacharacters (double-quote, backslash, backtick, dollar-sign) before interpolation into YAML/heredocs, preventing accidental config corruption or `$(...)` command substitution at config-generation time.
- **`upgrade.sh` surfaces a failed service** — `verify_upgrade` now reports a `failed` stratum/dashboard/sentinel service as a red **FAILED** ("not normal startup — investigate") instead of masking it as "still starting"; building from the `main` branch (when a release tag is missing) now emits a clear provenance warning.
- Minor: anchored the ufw "is SSH already allowed?" check so it no longer matches ports like 2200/22556/8022; removed a duplicate cleanup line.

### Dependency upgrades
Routine same-major bumps, verified by `go build` / `go vet` / `go test` (31 packages pass) and config/YAML validation:
- **Go modules**: `jackc/pgx/v5` v5.7.2→v5.10.0, `redis/go-redis/v9` v9.17.2→v9.20.1, `prometheus/client_golang` v1.20.5→v1.23.2, `golang.org/x/crypto` v0.46.0→v0.53.0 (plus transitive).
- **Images / toolchain**: PostgreSQL 18.1→18.4, etcd v3.5.11→v3.5.31, Go 1.26.1→1.26.4.
- **Python**: Flask 3.1.2→3.1.3, Werkzeug 3.1.5→3.1.8, and **requests 2.32.5→2.34.2** (CVE-2026-25645) in both dashboard and sentinel.

Larger, cross-major upgrades — config reviewed for documented breaking changes and no app-level incompatibility found, but these **require a `docker compose build/up` smoke-test before production** (a pre-upgrade backup was taken): **Redis** 7→8, **Prometheus** v2.51→v3, **Grafana** 10.4→13, **HAProxy** 2.9 (EOL)→3.4 LTS, **Python** runtime 3.12→3.14, **gunicorn** 25→26.

> **Bare-metal vs Docker:** the cross-major **image** bumps (Redis 8, Prometheus v3, Grafana 13, HAProxy 3.4, Python 3.14) live only in the Docker stack and reach a deployment only when the operator runs `docker compose pull/build` — that is the path to smoke-test. A bare-metal install upgraded via `upgrade.sh` is unaffected by those image bumps, but **does** now receive dependency currency for the components it manages — see **Added: automatic component currency** below.

### Added — automatic component currency on upgrade (`upgrade.sh`)
`upgrade.sh` now keeps the bare-metal components it manages current as part of every run, driven by a version manifest (read from install.sh's `*_VERSION` vars — single source of truth), so a release that bumps a version is applied automatically:
- **Go toolchain** — upgraded to the required `GO_VERSION` when older (previously hardcoded to install a frozen 1.26.1); arch-aware (amd64/arm64), extracted to a temp dir and swapped in only on success so a bad download can't leave the host with no Go.
- **Python venvs** — the dashboard venv was already refreshed from `requirements.txt`; the sentinel venv now is too (so the `requests` CVE fix reaches both).
- **PostgreSQL minor + Redis** — `apt --only-upgrade` security patches within the pinned major (no data-format change).
- **PostgreSQL major** — migrated via Debian's `pg_upgradecluster` (logical dump→restore) when a release raises the required major, wrapped in safety nets: an independent **verified** pre-migration dump; the **old cluster is kept intact** (only stopped) so rollback is trivial; verification requires the new cluster to serve the old port **and** the app role (`spiralstratum`) to authenticate over TCP; any failure reverts to the old cluster behind a `pg_isready` health-gate. **HA/Patroni-aware** — skipped entirely when Patroni manages PostgreSQL (those use Patroni's coordinated rolling upgrade). Dormant until `POSTGRES_VERSION` is raised past the installed major, and never fatal to the overall upgrade.

---

## [v2.5.2] - 2026-06-20 - Phi Hash Reactor

Maintenance release. Removes Q-BitX (QBX) support — dropped due to lack of liquidity — and fixes a Sentinel hashrate-degradation false alarm. Drop-in upgrade from v2.5.1 — no database migrations, no config format changes, and no manual steps required.

### Fixed
- **Sentinel hashrate-degradation false alarms from a poisoned baseline** — `SpiralSentinel.update_hashrate_baseline()` learns a per-miner rolling baseline hashrate and alerts when a reading drops far below it, but it only rejected readings that were *too low* as outliers — any reading *above* the baseline was always folded in. A single glitched or units-misparsed sample (e.g. a NerdQAxe momentarily reporting ~167 TH/s instead of its real ~5 TH/s) therefore ratcheted the baseline up permanently, after which every healthy reading looked like a ~97% crash and the `degradation` alert re-fired roughly once an hour indefinitely — the baseline intentionally stops adapting while a miner reads "degraded", so it never self-heals. Added a symmetric high-side outlier guard: a sample exceeding 2× the baseline is now ignored instead of absorbed, mirroring the existing 0.5× low-side guard. On first start after upgrading, the Sentinel also runs a one-time migration that automatically clears any already-poisoned baseline (detected as a stored baseline more than 2× the median of the miner's recent readings) so affected miners relearn a correct baseline with no manual intervention; it is guarded by a persisted flag and runs exactly once. The standalone `scripts/reset-hashrate-baseline.py` utility is also included for resetting a specific miner's baseline on demand.

### Removed
- **Q-BitX (QBX) coin support** —  Removed from all components: installer (`install.sh`, `install-windows.ps1`), stratum server, Sentinel monitoring, dashboard, Docker configs, `coin-upgrade.sh`, and all documentation. The `qbitxd` systemd service definition, `Dockerfile.qbitx`, `docker/config/qbitx.conf.template`, `config/regtest/config-qbx-regtest.yaml`, and the `src/stratum/internal/coin/qbx.go` coin implementation are deleted. All QBX-specific environment variables (`ENABLE_QBX`, `QBX_RPC_PASSWORD`, `QBX_POOL_ADDRESS`, etc.), ports (Stratum 20335/20336/20337, RPC 8344, P2P 8345, ZMQ 28344), and wallet address validation are gone. Supported coin count drops from 17 to 16.

---

## [v2.5.1] - 2026-06-04 - Phi Hash Reactor

Consolidation patch release. Promotes the bug fixes and minor improvements applied in-place on the v2.5.0 line into a single tagged version (2.5.1). Drop-in upgrade from v2.5.0 — no database migrations, no config format changes, and no manual steps required.

### Fixed
- **Sentinel metrics-token auto-discovery on V1-schema configs** - `SpiralSentinel.py` only auto-discovered the Prometheus bearer token from the V2 key `metrics_auth_token:`, never the V1 layout where it lives nested as `metrics.authToken`. On V1 installs the token stayed empty, so every `/metrics` fetch returned HTTP 401 and silently disabled the best-share milestone alert, rejection-spike pool-side cross-referencing, and the Prometheus-derived infrastructure-health signals — with no user-visible error, since the dashboard reads the token correctly (`metrics.authToken`) and was unaffected. The auto-discovery loop is now section-aware: it reads `metrics.authToken` only when inside the `metrics:` block (matching the dashboard's long-standing behavior) and ignores an `authToken` found under any other section. The existing V2 `metrics_auth_token:` path is unchanged.

Also consolidated under this tag (applied in-place on the v2.5.0 line): the toggleable high-odds and network-hashrate-drop Sentinel alerts (`high_odds_enabled` / `hashrate_crash_enabled`, both default `true`); the fix for maintenance mode silently failing to suppress alerts when enabled as root; the fix for the missing `actual_difficulty` column on fresh installs; the fix for Sentinel intel-report delays caused by system clock drift after VM suspend; the flaky `TestMoneyLoss_ConcurrentBlockFindsUnderLoad` test-mock race fix; and the v0.3.0 hard-fork update.

---

## [v2.5.0] - 2026-05-14 - Phi Hash Reactor

### Added
- **Worker Statistics panel — per-coin per-worker hashrate, shares, best diff** — New overview-tab section listing every worker that has submitted shares in the selected time window (10 min / 1 h / 24 h), grouped by coin. Backed by a new `actual_difficulty` column on the per-pool `shares_<id>` tables (migration v11) populated from `result.ActualDifficulty` in `coinpool.go` (`handleShare`, `HandleMultiPortShare`) and `pool.go` (`handleShare`). `WriteBatch` / `WriteBatchForPool` carry the new column in their COPY statements; the initial `CREATE TABLE shares_*` also includes it for fresh installs. `dashboard.py` adds `get_worker_stats_from_db(pool_ids, minutes)` (best-diff = `GREATEST(MAX(difficulty), MAX(actual_difficulty))`, hashrate estimated as `shares/elapsed × avg_diff × 2^32`) and an admin-gated `/api/worker-stats?minutes=10|60|1440` endpoint generalized to all 17 supported coins (the upstream port only enumerated 5). Frontend renders client-side via `fetchWorkerStats()` + `renderWorkerStats()`; the overview coin-selector filters `.worker-coin-group` divs in-place. (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@8474355](https://github.com/bkhuraijam/Spiral-Pool/commit/847435523dface3d9e2bc23d08f454b9f1ef12b8), [bkhuraijam/Spiral-Pool@63941f1](https://github.com/bkhuraijam/Spiral-Pool/commit/63941f1ef66cca0626ee5f783fe9f9699a612bc4))
- **Smart Port DIFFICULTY routing mode** — Multi-coin smart port (port 16180) now supports a second routing strategy: `mode: DIFFICULTY` selects the coin with the lowest current network difficulty in real time, polling all configured coins every `check_interval` (default 30s) and rotating with the same `min_time_on_coin` guard used by TIME mode. Existing TIME-based routing is unchanged and remains the default. Configurable via `multi_port.mode` in `config.yaml`, `MULTIPORT_MODE` in `coins.env` (bare metal), `SMARTPORT_MODE` in Docker, or the dashboard Settings → Multi-Coin Mode panel. The stratum `/api/multiport` response now includes `routing_mode`. Covered by 10 new unit tests in `selector_difficulty_test.go`.
- **Smart Port difficulty exclusion list** — New `multi_port.exclude_coins` config field (DIFFICULTY mode only) prevents specific installed coins from ever being auto-selected by difficulty routing, even if they currently have the lowest network difficulty. Configurable in `config.yaml` as a list of coin symbols (`exclude_coins: [DGB]`) or interactively via Settings → Multi-Coin Mode → **Exclude from Rotation** pill picker, which appears automatically when Difficulty-Based mode is selected and shows every installed coin as a toggleable button (grey = eligible, red ✗ = excluded). If a miner session is already on an excluded coin (e.g. the exclusion list was updated while the pool was running), the `min_time_on_coin` guard is bypassed and the miner is rotated to an eligible coin immediately. If all coins are excluded the pool falls back to `prefer_coin`. The stratum `/api/multiport` response now includes `exclude_coins`; both the Smart Port panel bars and the Rotation Widget on the overview dashboard display excluded coins below active coins at reduced opacity with a ✗ marker. Covered by 3 new unit tests: `TestDifficultyMode_ExcludedCoinSkipped`, `TestDifficultyMode_ExcludedCurrentCoinBypassesMinTime`, `TestDifficultyMode_AllCoinsExcluded_FallbackToPrefer`.
- **Debian 13 "Trixie" bare metal support** — `install.sh` now accepts Debian 13 as a supported host OS alongside Ubuntu 24.04/26.04 LTS. No flags or workarounds required; the installer auto-detects the OS.
- `scripts/linux/detect-os.sh` — new OS abstraction module that exports `OS_ID`, `OS_VERSION`, `OS_CODENAME`, `OS_PRETTY_NAME`, `DOCKER_DISTRO`, and `UNATTENDED_UPGRADES_EXTRA_ORIGINS`. Provides `is_ubuntu()`, `is_debian()`, `is_debian_13()`, and `require_supported_os()` helpers. Sourced early in `install.sh`; eliminates all direct `/etc/os-release` reads from install logic.
- **Ubuntu 26.04 LTS (Resolute Raccoon) support** — `install.sh` now accepts both Ubuntu 24.04 LTS and 26.04 LTS. All Dockerfiles updated to `ubuntu:26.04`. Both versions are fully supported for native and Docker deployments (x86_64 only)
- **BCH2 (Bitcoin Cash II) and BTCS (Bitcoin Silver)** — full SHA-256d coin implementations; see Ported Upstream Commits for details
- **XEC (eCash / Bitcoin ABC) full integration** — eCash is now a first-class SHA-256d coin across every Spiral Pool component. Binary name collision resolved via unique symlinks (`ecashd`→`bitcoind`, `ecash-cli`→`bitcoin-cli`) stored in `/spiralpool/xec-bin/` with service name `ecashd` (mirrors FBTC's `fractald` pattern). CashAddr addressing (`ecash:q…` P2PKH / `ecash:p…` P2SH); does not support `address_type` parameter in `getnewaddress`. Ports: RPC 9004, P2P 8343, ZMQ 28335, Stratum V1/V2/TLS 18338/18339/18340. `src/stratum/internal/config/config.go`: `"ecash"` entry added to `SupportedCoins` map with `DefaultPort: 9004`, `P2PPort: 8343`, `BlockTime: 600`, and coin-name alias entries `ecash`/`bitcoin-abc`/`xec`. `src/stratum/internal/config/v2.go`: `"XEC": "ecash"` in `symbolToCoinName`, `case "XEC", "ECASH": return 9004` in `getDefaultPortForCoin`, `case "XEC", "ECASH": return 600` in `getBlockTimeForCoin`, XEC added to supported-coins error message. Docker: `Dockerfile.ecash` (Bitcoin ABC v0.31.12, x86_64), `docker-compose.yml` profile `xec` with stratum ports 18338/18339/18340 and P2P on 8343, `docker/config/ecash.conf` generated by `generate_docker_xec_config()` with RPC 9004 and ZMQ 28335. `.env.example`: `XEC_RPC_USER`, `XEC_RPC_PASSWORD`, `ENABLE_XEC`, `XEC_POOL_ADDRESS`. `install.sh`: `install_ecash()` function (Bitcoin ABC v0.31.12 tarball, ecashd/ecash-cli symlinks, systemd unit), address prompt with CashAddr regex validation (`ecash:[qp][a-z0-9]{41,}`), UFW rules for 18338–18340/tcp and 8343/tcp, coin menu option 17 (single-coin) and multi-coin toggle, `configure_stratum_single()` and `configure_stratum_multi()` cases, Docker config generation and `data/ecash` directory creation, disk-space calculation (+20 GB), upgrade-path credential/address preservation, and `ecashd` in `reset-failed` and start lists. `coin-upgrade.sh`: `[XEC]="0.31.12"` in `COIN_TARGET`, download function `download_XEC()`, service name `ecashd`. `wait-for-node.sh`: RPC credential lookup, CLI path, wallet dir, conf path, and address-type group. `src/dashboard/dashboard.py`: XEC added to `MULTI_COIN_NODES` (service `ecashd`, RPC 9004, conf `/spiralpool/xec/bitcoin.conf`, block time 600s), `COIN_BLOCK_REWARDS["XEC"] = 3125000`, `COIN_BLOCK_TIMES["XEC"] = 600`, both inline `coin_block_times` dicts, block-reward fallback handler, `PORT_TO_COIN[9004] = "XEC"`, alias map `"ecash"/"xec"`, both `COIN_WHITELIST` sets, `default_ports["XEC"] = 18338`, `default_rpc_ports["XEC"] = 9004`, `batch_update_pool` default port, `VALID_COIN_TYPES_EXTENDED` (`XEC`, `ECASH`, `BITCOIN-ABC`), and extended normalisation map (`ECASH`/`BITCOIN-ABC` → `XEC`). `COINGECKO_IDS["XEC"] = "ecash"`. `docs/reference/MULTI_COIN_PORT.md`: XEC row added to stratum port reference table. (contributed by [bkhuraijam](https://github.com/bkhuraijam), [commit d7c1939](https://github.com/bkhuraijam/Spiral-Pool/commit/d7c19395ef3e6f3335c4ca7482d4b5e83f081b8b))
- **IBD regression tests** — `src/stratum/internal/daemon/ibd_regression_test.go`: three tests pin the IBD state handling that failed during XEC mid-sync recovery. `TestGetBlockchainInfo_IBD` verifies `GetBlockchainInfo` correctly parses `initialblockdownload=true` with exact field values from the incident (451977/948279 blocks, 47.66 % progress, pruned). `TestGetBlockchainInfo_FullySynced` covers the fully-synced flip side. `TestSubmitBlockWithVerification_NodeInIBD` proves the submit pipeline (`submitblock` → `preciousblock` → `getblockhash`) is independent of IBD state — a found block must be credited even when the node reports mid-sync.
- **XEC coin-level tests covering mining, submission, reward, and maturation** — `src/stratum/internal/coin/ecash_test.go`: 20 tests covering the full XEC coinbase pipeline. Address validation tests use the package's internal `cashAddrPolymod`/`bchConvertBits` helpers to generate valid `ecash:q`/`ecash:p` CashAddr test vectors, verifying encode→decode round-trips and correct rejection of BCH-checksummed and BTC bech32 addresses. `TestECashCoinbaseScript_P2PKH_CashAddr` and `_P2SH_CashAddr` verify the output scripts byte-for-byte (OP_DUP OP_HASH160 / OP_HASH160 opcodes, embedded hash identity). `TestDecodeMinerFundScript` and `TestDecodeStakingScript` cover the mandatory IFP and staking-rewards coinbase outputs — the building blocks of every valid XEC block. `TestECashCoinbaseRewardPipeline` pins the complete mining → submission → reward → maturation path at height 951,001: pool reward script (P2PKH), MinerFund script (P2SH), StakingRewards script (P2SH from node hex), and maturation window (100 blocks × 600 s = 60,000 s ≈ 16.67 h). Genesis hash constant pinned against Bitcoin's genesis (shared by BTC/BCH/XEC). Registry tests cover both `XEC` and `ECASH` aliases with case-insensitive lookup.
- **Block history table** — Last 5,000 blocks shown in a collapsible table on the Blocks tab with height, time, miner/worker, net diff, miner diff, effort %, and status badges
- **PostgreSQL-backed block fetching** — `get_blocks_from_db()` queries block history directly from Postgres for accurate historical data across all pool tables
- **Difficulty-from-hash computation** — `hash_to_difficulty()` computes actual miner difficulty from block hash for correct effort/luck calculation
- **Coin explorer links** — Clickable block explorer links for BTC (mempool.space), BCH (blockchair), DGB (chainz.cryptoid), FBTC (mempool.fractalbitcoin.io), 
- **Coin badges in block history** — Each block row shows a coin badge with its symbol
- **Status badge CSS** — Confirmed (green), Pending (yellow), Orphaned (red, strikethrough) styling
- **psycopg2-binary dependency** — Added to requirements.txt for Postgres connectivity
- **pytz dependency** — Added to requirements.txt for timezone-aware schedule computation in rotation widget
- **Multi-Coin Rotation widget** — Visual 24h timeline bar, live status (active coin, time remaining, next switch), schedule breakdown table, auto-hides when multi-port is disabled (contributed by bkhuraijam)
- **Network difficulty in Est. Time to Block** — ETB stat card now shows current network difficulty alongside 24h probability; updates when switching coins; no-hashrate state clears ETB while still displaying difficulty
- **Initial network difficulty fetch on startup** — Stratum now fetches network difficulty synchronously before accepting miners, preventing blocks found during the startup jitter window from recording `networkdifficulty=1` (contributed by bkhuraijam)
- **Dynamic pool discovery** — `index()` route discovers running pools from stratum API instead of hardcoded pool IDs
- **Block DB → API → cache fallback** — `get_blocks()` function tries Postgres first, falls back to pool API, then local file cache
- **True pool effort calculation** — `CoinPool` and `Pool` now track `currentRoundDifficulty` per round; effort stored at block-find time as `(roundDiff / networkDiff) × 100`; `GetPoolEffort()` returns live round effort; `GetBlocksWithOrphans()` accepts `?pageSize=` query parameter (default 100/200, max 5,000)
- **Per-coin accepted/rejected shares** — `GetAcceptedShares()` and `GetRejectedShares()` added to `StatsProvider` and `CoinPoolProvider` interfaces; dashboard displays accept/reject rate per coin
- **Per-coin session and all-time best share difficulty** — `GetBestShareDiff()` added to pool interfaces; all-time best persisted in `lifetime_stats["per_coin_best_diff"]` across dashboard restarts
- `docker/config/dashboard_config.json` — sanitized example config (wallet address and device IPs replaced with documented placeholders)
- **Docker container management in Management tab** — New `GET /api/docker/containers` endpoint lists all containers with run state; `POST /api/docker/containers/<name>/<action>` performs start/stop/restart on any named container. Container name and action are validated before execution. All actions recorded to the activity log. UI card in the Management tab shows per-container status and control buttons; card is hidden when Docker is not available. `_docker_available()` helper checks Docker daemon reachability with a 5-second timeout. Mock container support via `SPIRAL_DOCKER_MOCK=1` env var for local testing
- **System package update management** — New `GET /api/system/updates` endpoint reports available apt package upgrades; `POST /api/system/updates/refresh` runs `apt-get update`; `POST /api/system/updates/apply` runs `apt-get dist-upgrade` via `scripts/linux/apt-noninteractive.sh`. All three endpoints are admin-gated and rate-limited per client IP
- `scripts/linux/apt-noninteractive.sh` — new helper script wrapping apt operations for non-interactive execution from the dashboard backend; used by the system update endpoints
- **Sentinel price crash detection** — `check_price_crash()` alerts when any enabled coin drops 15%+ in USD value within a 1-hour window; per-coin 4-hour cooldown prevents alert storms; returns current price, baseline price, and percentage drop
- **Sentinel revenue velocity decline detection** — `check_revenue_velocity()` compares current-month earnings pace against the previous month; requires a minimum of 3 days of current-month data before firing; fires at most once per month per coin; supports multi-currency conversion for previous-month comparison
- **Sentinel enhanced miner health score** — `calc_enhanced_health_score()` produces a 0–100 composite score from six weighted components: uptime (25%), temperature stability (15%), temperature trend (10%), hashrate consistency (25%), stale rate (15%), and restart stability (10%); returns both the score and a per-component breakdown for diagnostic display
- **Sentinel zombie miner detection** — `check_zombie_miner()` detects miners that are online and connected but not submitting valid shares; applies a 15-minute post-restart cooldown to avoid false positives; distinguishes stale shares from true rejections
- **High-odds and network-hashrate-drop alerts now toggleable at install** — The Spiral Sentinel monitoring configuration menu in `install.sh` adds two new per-alert toggles: **High odds** (block-finding odds favorable, sustained 1h+) and **Network hashrate drop** (network drops 25%+, sustained 2h+), shown as items 9 and 10 under the master alerts switch. Both default to ON and write `high_odds_enabled` / `hashrate_crash_enabled` to both generated `config.json` blocks (single- and multi-coin). `SpiralSentinel.py` now gates the `high_odds` and `hashrate_crash` emission sites on these keys (`CONFIG.get(..., True)`), so disabling a toggle suppresses the corresponding alert; existing installs without the keys keep firing both alerts as before.
- **Share difficulty logging and near-miss detection** — Every accepted share now emits a structured `debug`-level log entry with `actualDiff`, `shareDiff`, `nonce`, `worker`, and `coin` fields (suppressed in production; enable with `logLevel: debug`). A `warn`-level "NEAR MISS" entry fires when `actualDiff` exceeds 50% of the current network difficulty, logging the exact `percentOfNetwork` for block analysis. Both standalone (`handleShare`) and multi-port (`HandleMultiPortShare`) paths are covered. (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@7b9e7cf](https://github.com/bkhuraijam/Spiral-Pool/commit/7b9e7cf34e186ea3d608f94c93552130c4af2e58))

### Changed
- **Sentinel clock drift detection and chrony hardening** — Spiral Sentinel now checks NTP clock sync on startup and logs a warning if the system clock is more than 60 seconds from NTP time (caused by machine/VM suspend without `makestep`). The main monitor loop detects suspend/resume events by measuring iteration wall-clock gaps: if a gap exceeds the expected check interval by more than 5 minutes, a warning is logged with remediation instructions (`sudo chronyc makestep`). `install.sh` now configures chrony with `makestep 1.0 -1` (replace the Ubuntu default `makestep 1 3`), ensuring the clock is corrected immediately after any suspend/resume instead of drifting for hours — the root cause of intel reports arriving hours late on machines that sleep.
- **Bitcoin Knots upgraded to v29.3.knots20260508** — `docker/Dockerfile.bitcoin` now builds from `debian:bookworm-slim` and downloads the official release tarball directly from GitHub Releases (`bitcoin-29.3.knots20260508-x86_64-linux-gnu.tar.gz`), eliminating the dependency on the third-party `bitcoinknots/bitcoin` Docker Hub image. Installs `bitcoind`, `bitcoin-cli`, and `bitcoin-tx`; creates a dedicated `bitcoin` user with restricted privileges. Pinned version updated in `install.sh` (`BITCOIN_KNOTS_PINNED_VERSION` and BTC version cache fallback). (contributed by [bkhuraijam](https://github.com/bkhuraijam), [commit 35fc61f](https://github.com/bkhuraijam/Spiral-Pool/commit/35fc61f585e30dc2562a4ca31cb773ff65d1c9cb))
- **Wallet backup confirmation requires explicit typed acknowledgement** — All three wallet backup prompt locations in `install.sh` (spiralpool-wallet success path, spiralpool-wallet auto-export-failed path, and start_services() wallet block) now require the operator to type `I HAVE BACKED UP THE WALLET` exactly before proceeding. Simply pressing ENTER is no longer accepted. The prompt loops until the exact phrase is entered. Each backup display now also shows the file type (`wallet.dat` — binary wallet file, or descriptor dump — JSON export of private keys) with the correct restore command for each format.
- **`backupwallet` replaces `dumpwallet` as primary backup method everywhere** — `dumpwallet` is unsupported on descriptor wallets (DGB v8+, Bitcoin Knots, XEC) and would fail silently, leaving operators with no backup. All backup calls in `install.sh` (spiralpool-wallet, start_services wallet block, early-sync backup) and `upgrade.sh` now use `backupwallet` as primary, which works for both legacy (BerkeleyDB) and descriptor (SQLite) wallets. Fallback to `listdescriptors true` (JSON key export) is used only when `backupwallet` explicitly errors. Backup files use `.dat` extension for `backupwallet` output and `.dump` for descriptor fallback so the format is unambiguous.
- **`.backup-confirmed-{coin}` marker system** — After an operator confirms a wallet backup in `install.sh`, a marker file is written to `/spiralpool/backups/.backup-confirmed-{coin}`. The start_services() wallet backup block now triggers on either `PENDING_GENERATION` address OR absence of this marker, so reinstalls and re-runs always prompt for a backup confirmation unless the operator has already confirmed one. Prevents re-prompting on clean reinstalls where the backup was already saved.
- **`ismine` verification after wallet address generation** — After `getnewaddress`, `spiralpool-wallet` now calls `getaddressinfo` (with `validateaddress` fallback for older daemons) and checks `ismine: true`. If the address does not belong to the target named wallet (`pool-{coin}`), the script exits with an error rather than writing a wrong address to `config.yaml`. Catches wrong-wallet scenarios caused by silent `createwallet` failures, leftover default wallets from previous installs, or HA replication edge cases.
- **`scripts/linux/wallet-backup.sh`** — New standalone emergency backup script for existing installations. Reads `config.yaml` to detect all enabled coins, calls `backupwallet` for each live daemon, falls back to `listdescriptors true` if needed, prints the exact SCP command for every successful backup file, and lists failed coins with manual recovery instructions. Usage: `sudo bash wallet-backup.sh`. Intended for operators who installed before the backup hardening changes shipped.
- **`upgrade.sh` wallet backup repair on every upgrade** — `repair_wallet_backups()` function added to `upgrade.sh`. Runs during every upgrade while daemons are live: for each enabled coin that lacks a `.backup-confirmed-{coin}` marker, attempts a 4-method recovery cascade (`backupwallet` → `listdescriptors true` → SQLite `.recover` for descriptor wallets → `-salvagewallet` restart for legacy wallets), displays the backup file path and SCP command, and blocks on typed `I HAVE BACKED UP THE WALLET` confirmation before marking the coin confirmed. `--recover-wallets` flag added to `upgrade.sh` to run the repair standalone at any time (clears all existing markers and re-prompts for every coin).
- **XEC added to `_wg_cli` case in start_services() wallet block** — XEC was missing from the coin CLI lookup table, causing wallet backup to be silently skipped for eCash installations. Added `xec) _wg_cli="ecash-cli -conf=$INSTALL_DIR/xec/bitcoin.conf -rpcwallet=pool-xec"`.
- **Backup directory ownership corrected** — `backupwallet` RPC is executed by the daemon process (running as `spiralpool` user). Backup dir is now explicitly `chown`ed to `POOL_USER:POOL_USER` before any backup call, preventing silent write failures when the directory was owned by root.
- **Async single-coin job broadcast** — `cp.stratumServer.BroadcastJob(job)` in the ZMQ job callback is now launched in a goroutine, preventing the single-coin session-iteration loop (which can take 2–5 s with many miners) from blocking the callback and delaying multi-port relay. Multi-port relay was already async; this makes the single-coin path consistent. (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@a1050ac](https://github.com/bkhuraijam/Spiral-Pool/commit/a1050ac0962ad186589e6c883b62643288c1141b))
- **ZMQ block notification logs include endpoint** — `"endpoint", z.cfg.Endpoint` added to the `hashblock` notification log entry so multi-daemon deployments can trace which ZMQ socket each block arrived on. (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@a1050ac](https://github.com/bkhuraijam/Spiral-Pool/commit/a1050ac0962ad186589e6c883b62643288c1141b))
- **ARM64 support removed** — All ARM/aarch64 code paths, download branches, detection functions, and documentation removed; project is x86_64 (amd64) only
- **Postgres env vars in docker-compose** — Dashboard service now receives Postgres connection vars via `${DB_PASSWORD}`, `${DB_NAME}`, `${DB_USER}` (env vars, never hardcoded)
- **Block history coin badges** — All coin badges use consistent cyan styling (no hardcoded per-coin colors)
- `docker-compose.yml` — ZMQ ports exposed for DGB (28532), BTC (28332), BCH (28432), FBTC (28340); `extra_hosts: host.docker.internal` added to stratum and dashboard; SmartPort 16180 added; PROMETHEUS_URL added to dashboard env
- `SpiralSentinel.py` — sync check interval reduced from 60s to 30s; block alert retry queue persists failed Discord notifications to disk and retries each cycle; Discord retries increased from 3 to 5 for block-found alerts; block dedup switched to hash-only (fixes CGMiner worker name mismatch suppressing alerts)
- `dashboard.py` — blocks with `effort=0` (legacy rows) now display `---` instead of a garbage fallback calculation
- Docker CE and PostgreSQL PGDG apt repository setup now uses distro-aware variables (`${DOCKER_DISTRO}`, `${OS_CODENAME}`) instead of hardcoded `ubuntu` and `lsb_release -cs`
- `etcd` installation on Debian 13 downloads binary from GitHub Releases (v3.5.16) — `etcd-server`/`etcd-client` packages do not exist in Debian 13's official repositories; Ubuntu install path unchanged
- `unattended-upgrades` configuration no longer writes Ubuntu ESM/Pro origins on Debian installs

### Fixed
- **Maintenance mode silently failed to suppress alerts when enabled as root — GRID POWER EVENT, EXCESSIVE RESTARTS, and all other alerts fired during the maintenance window** — `maintenance-mode.sh enable`/`extend` wrote `/spiralpool/config/.maintenance-mode` with `chmod 600` but never `chown`ed it, so any invocation as root (operator `sudo spiralpool-maintenance`, or the automated `coin-upgrade.sh` / `update-checker.sh` upgrade hooks) left a `root:root 0600` file. The Sentinel runs as the pool user and could not read it; `check_ha_maintenance_propagation()` in `SpiralSentinel.py` caught the resulting `PermissionError` under a broad `except (… OSError …)`, logged it only at `DEBUG`, and fell through to "not in maintenance" — failing **open** so every alert was sent during the window. Three fixes: (1) `maintenance-mode.sh` now `chown`s the file to the pool user (`get_pool_user()`) after both `enable` and `extend`; (2) `check_ha_maintenance_propagation()` now handles `PermissionError` separately and fails **safe** — an existing-but-unreadable maintenance file is treated as ACTIVE (alerts suppressed) and logged at `WARNING` with remediation, and other read/parse errors are also upgraded from `DEBUG` to `WARNING`; (3) the dashboard maintenance toggle (`POST /api/fleet/maintenance`), which previously only set an in-process dict the separate Sentinel process never saw, now writes/removes the same unified `.maintenance-mode` file (atomically, as the pool user) so the web UI button actually suppresses Sentinel alerts. Pre-existing stale file from before the fix must be corrected once: `sudo chown spiraluser:spiraluser /spiralpool/config/.maintenance-mode`.
- **Sentinel wallet address not auto-synced from stratum `config.yaml` — block alerts, payout alerts, wallet balance display, and Avalon LED celebration all silently suppressed** — The sentinel config (`/spiralpool/config/sentinel/config.json`) is a separate file from the stratum config (`config.yaml`). When the sentinel config was regenerated (e.g. during upgrade or after a config reset) the `wallet_address` field reverted to the installer placeholder `PENDING_GENERATION`. The sentinel's block filter at `check_pool_for_new_blocks()` compares each block's `miner` field against the configured wallet address; with `PENDING_GENERATION` as the filter value, every block was silently added to `seen_pool_block_hashes` and discarded — no `block_found` Discord alert, no Avalon LED celebration via `trigger_block_celebration()`, no `payout_received` alert (balance fetch returns `None` after security regex rejects `_` in the placeholder), and no `🏦 WALLET` section in 6-hour intel reports. Fixed in `load_config()` by adding a wallet address auto-sync block that reads coin `address:` entries from `config.yaml` at startup, replaces any placeholder `wallet_address` value in both the top-level config (single-coin / auto-detect mode) and the per-coin `coins` array (multi-coin mode), then persists the corrected values back to `config.json` so they survive future restarts. The stratum refuses to start with `PENDING_GENERATION` addresses (`config.go`), so `config.yaml` is always authoritative when it contains a real address. The fix is non-destructive — real addresses already in `config.json` are never overwritten.
- **Sentinel Prometheus metrics `HTTP 401: Unauthorized` — infrastructure health alerts non-functional** — The `spiralsentinel` systemd service did not receive the `SPIRAL_METRICS_TOKEN` environment variable that `install.sh` injects only into the `spiralstratum` service unit. As a result `fetch_prometheus_metrics()` sent unauthenticated requests to `localhost:9100/metrics`, received HTTP 401, and returned `None` every cycle — silently disabling all Prometheus-fed infrastructure alerts (`circuit_breaker`, `backpressure`, `wal_errors`, `zmq_disconnected`, share batch loss rate, etc.) and suppressing the `🏦 WALLET` infrastructure health section from intel reports. Fixed by extending the existing `pool_admin_api_key` auto-discovery block in `load_config()` to also read `metrics_auth_token:` from `config.yaml` in the same single-pass file read. Both keys are now discovered together at startup; the loop exits early once both are found. Operators do not need to configure either value manually — both are read from the single authoritative source in `config.yaml`.
- **XEC dead peer-discovery seeds causing 20+ minute connection delays on fresh sync** — `seeder.ecash.network` and `seeder2.ecash.network` return NXDOMAIN. Both replaced with `seeder.status.cash` and `seeder.fabien.cash` (verified active in Bitcoin ABC's DNS seed rotation) in `docker/config/ecash.conf.template` and `install.sh`. The Docker heredoc in `generate_docker_xec_config()` also listed `electrum.bitcoinabc.org:8343` (an Electrum server, not a P2P node) and `seed.flowee.cash:8343` (wrong port — outbound peer connections use each peer's advertised port, 8333, not the local listen port 8343); both replaced with correct seed hostnames. The native Linux clearnet config block had `dnsseed=1` but no `addnode` fallbacks, leaving fresh installs and post-recovery restarts entirely dependent on DNS timing; three `addnode` entries now added. `dnsseed=1` added to the Docker heredoc where it was previously absent.
- **XEC dbcache hardcoded at 2048 MB causing OOM restarts during IBD** — The 948k-block eCash chain with `dbcache=2048` regularly exhausted the `MemoryMax=4G` systemd cgroup, killing the daemon mid-sync and forcing re-download from an earlier checkpoint. XEC now uses the same auto-sizing formula as DGB/BTC/BCH: 55 % of total RAM capped at 8192 MB, with `MemoryMax` set to `(dbcache_mb + 3072) / 1024` GB to cover UTXO set, mempool, and OS overhead. The Docker heredoc's `dbcache=2048` is corrected to 4096 to match `docker/config/ecash.conf.template` which already had the correct value.
- **`spiralctl sync --coin xec` falsely reporting "daemon is not running" during manual recovery** — `check_daemon_running()` in the sync script and `isServiceRunning()` in the Go spiralctl binary relied solely on `systemctl is-active`, giving false negatives for daemons started outside systemd (e.g. `bitcoind` launched directly with `-reindex` during chainstate recovery). `check_daemon_running()` now falls back to `pgrep -f "datadir=$DATADIR"` matching any process started with the coin's data directory regardless of binary name. The Go binary gains `isCoinServiceRunning(service, rpcPort)` in `status.go` which falls back to a 1-second TCP dial of the RPC port; `coinStatus()` in `coin.go` now uses this instead of `isServiceRunning()`.
- **XEC CashAddr checksum never verified — corrupted or wrong-network addresses silently accepted** — `cashAddrDecode()` in `ecash.go` stripped the 8 trailing 5-bit checksum groups from the address but discarded them without verification. Any address with valid CashAddr characters and correct length would pass, including typos and BCH addresses (which are checksummed with the `"bitcoincash"` prefix, not `"ecash"`). Added `cashAddrPolymod()` (BCH polynomial, generator constants matching the CashAddr spec) and `cashAddrVerifyChecksum()` which expands the network prefix, appends a zero separator, appends the full bare address 5-bit values, and confirms `polymod == 0`. `decodeCashAddr()` now calls this before decoding — rejects wrong-network addresses and catches any address corruption at entry time for both pool-operator config addresses and per-miner solo addresses.
- **XEC RTT block-submission compile error** — `skipSubmission = true` added in the RTT rejection branch (`coinpool.go:1200`) referenced a variable not yet declared in that scope (first declared with `:=` at line 1377 inside the submission block). This would prevent the entire pool binary from compiling. The assignment was also dead code: `finalStatus = "orphaned"` set two lines earlier already prevents submission via the `if finalStatus != "pending"` gate at line 1215. Removed the erroneous assignment; added a comment explaining why `finalStatus` alone is sufficient.
- **XEC RTT fields lost when Job is cloned for secondary stratum ports** — `Job.Clone()` in `protocol.go` performed deep copies of all slice fields (MerkleBranches, TransactionData, AuxBlocks, etc.) but silently omitted the four RTT fields (`RTTPrevHeaderTime []int64`, `RTTPrevBits`, `RTTNextTarget`, `RTTBits`). In multi-port or HA multiserver mode the stratum clones the job before sending it to secondary listeners; the clone arrived with a nil RTTPrevHeaderTime slice, causing the RTT check in `coinpool.go` to skip validation entirely (`len(job.RTTPrevHeaderTime) >= 2` is false), so blocks found on secondary ports bypassed RTT and were submitted to the network unvalidated — guaranteed orphans. Fixed by deep-copying `RTTPrevHeaderTime` and copying the three string fields into the clone's return struct.
- **XEC VarDiff and job-rebroadcast interval defaulting to wrong values** — Two `symbolToCoin` lookup maps in `v2.go` (used to resolve coin symbol → `SupportedCoins` key for block-time–aware defaults) listed every coin except XEC. When XEC fell through to the fallback `coinName = coinSymbol` ("xec"), the subsequent `SupportedCoins["xec"]` lookup found nothing (the key is "ecash"), so VarDiff `TargetTime` defaulted to 4 s instead of the correct 30 s (capped from 600 s / 4), and `JobRebroadcast` defaulted to 5 s instead of 60 s. Added `"xec": "ecash"` to both maps in `v2.go` (lines 962 and 1033).
- **XEC missing from `getAlgoBlockTime()` switch** — The function in `pool.go` that returns expected block time for network-difficulty estimation had explicit cases for all other coins but not XEC, relying on the `default: return 600` fallback. Added `"XEC"` explicitly to the 600-second case alongside BTC, BCH, and NMC.
- **XEC address validation rejecting `ecash:` prefixed addresses** — `validateCashAddr()` in `config.go` stripped `bitcoincash:` and `bitcoincashii:` prefixes but not `ecash:`, so a pool config containing a full CashAddr like `ecash:qzxx...` failed validation at startup with a misleading character error. Fixed by adding `"ecash:"` to both the dispatch `case` (line 2103) and the prefix-stripping block inside `validateCashAddr()`. Bare `q`/`p` forms were already accepted; only the full-prefix form was broken.
- **XEC Docker image missing `ecashd` / `ecash-cli` symlinks** — `Dockerfile.ecash` installed `bitcoind` and `bitcoin-cli` from the Bitcoin ABC release tarball but never created the canonical `ecashd` / `ecash-cli` symlinks that all scripts, the stratum entrypoint, and operator tooling expect (mirrors the `fractald` / `fractal-cli` pattern in `Dockerfile.fractalbitcoin`). Added `ln -sf` for both after the install step. Updated the `docker-compose.yml` ecash healthcheck from `bitcoin-cli` → `ecash-cli` for consistency.
- **XEC RTT validation not actually preventing block submission** — `skipSubmission = true` was never set in the RTT rejection branch, so even when a block failed the Real Time Target check the submission code still executed (the `if !skipSubmission {` guard at the HA layer was never reached with a true value from RTT). Added `skipSubmission = true` inside the `else if !meetsRTT` path in `coinpool.go` so RTT-failed XEC blocks are truly rejected before the RPC submit. Previously: RTT said "no" → submit ran anyway → block guaranteed-orphaned on-chain. Now: RTT says "no" → submit is skipped → block correctly discarded. (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@30d2e11](https://github.com/bkhuraijam/Spiral-Pool/commit/30d2e113b29ed6ae8390f14da84f795e038f921e))
- **Daily blocks chart blank on page refresh** — `fetchDailyBlocks()` is now called on `DOMContentLoaded` so the chart populates immediately on hard refresh; `updateAllStatsCharts()` skips `blocks_found` to prevent the daily-bar series from being overwritten by the time-series update path (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@b6a7e98](https://github.com/bkhuraijam/Spiral-Pool/commit/b6a7e9890450615338904d73be0fd4f22537ea90))
- **Daily blocks chart not refreshed on coin switch or periodic refresh cycle** — `fetchDailyBlocks()` was missing from the coin-select handler and the main data fetch cycle, leaving the chart stale after switching coins or waiting for the next full-page refresh; added to both call sites (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@7987d93](https://github.com/bkhuraijam/Spiral-Pool/commit/7987d93))
- **Pool effort inflated after daemon/stratum restart** — `lastBlockTime` was always initialized to the zero value on startup, so effort calculation used "epoch start" as the previous block time; `initBlockStats()` now loads the last found block timestamp from Postgres, preventing wildly over-estimated effort values after restarts (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@b1de5fe](https://github.com/bkhuraijam/Spiral-Pool/commit/b1de5fe))
- **FBTC effort calculation skewed by indexing provider difficulty cycle** — When FBTC's `getdifficulty` RPC returns 1 (indexing provider) or an astronomical merged-mining value (>1T), effort was computed against the wrong difficulty; replaced round-accumulator effort with time-based effort (`actualSeconds / expectedSeconds × 100`) and introduced `lastGoodNetworkDiff` caching (values in range 1 < diff < 1e12) to provide a stable fallback; `checkMultiPortDifficultySpike()` in sentinel also skips FBTC readings outside this range (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@7987d93](https://github.com/bkhuraijam/Spiral-Pool/commit/7987d93))
- **Race condition on startup** — Network difficulty was 0 during the first 10-second jitter window, causing blocks found in that window to record `networkdifficulty=1`
- **FBTC indexing provider cycle** — When `getdifficulty` returns 1, network difficulty falls back to the cached validator difficulty for both the block record and effort calculation
- **Hardcoded pool IDs** — `index()` route no longer hardcodes pool IDs; dynamically discovers from `/api/pools`
- **Coin badge styling** — Removed hardcoded FBTC-orange styling from block history coin badges
- **Dashboard luck calculation using all-time blocks vs session time window** — `update_luck_tracker()` and `get_luck_overview()` used `per_coin["blocks"]` (all-time total) combined with `mining_duration` (session uptime), producing 37,000%+ absurd luck values. Fixed by computing `dashboard_blocks` as a delta from a per-coin block baseline captured on first poll, ensuring both blocks_found and blocks_expected cover the same session window
- **Dashboard Blocks chart showing aggregate data regardless of selected coin** — Cumulative blocks chart ignored the coin selector; fixed by storing per-coin breakdown in each luck history entry and filtering by coin in `get_luck_overview()`
- **Dashboard ETB stuck on stale value when miner detection returns 0** — `update_etb_calculation()` now falls back to pool total hashrate instead of returning early
- **`bestShareDiff` variable used without `var` declaration in `server.go`** (pre-existing compilation bug)
- **Block History dropped historical blocks from coins not exposed by the stratum API** — `index()` route's Block History query in `dashboard.py` relied solely on `/api/pools` dynamic discovery (which only returns running pools) with a single-coin env-var fallback, so blocks from any coin that wasn't currently active disappeared from the table. Fixed by seeding `pool_ids` with the complete set of all 17 supported pool IDs (12 SHA-256d + 5 Scrypt) before merging in API-discovered IDs. `get_blocks_from_db` already skips pool IDs whose `blocks_<id>` table doesn't exist, so the comprehensive list is safe on every deployment regardless of which coins are installed. (contributed by [kamakhu](https://github.com/kamakhu), [bkhuraijam/Spiral-Pool@fdb5fe3](https://github.com/bkhuraijam/Spiral-Pool/commit/fdb5fe34ced6233b1e27a987e1da1fc4b1e10d3a))
- **Health monitor PostgreSQL check causing false node-down alerts and cascading stratum restarts** — `health-monitor.sh` checked PostgreSQL health with `sudo -u postgres /usr/bin/psql -c "SELECT 1"`, which requires a `NOPASSWD` sudoers entry for `spiraluser`. When `/etc/sudoers.d/spiralpool` was empty (sudoers entries not written during install), the `sudo` call silently exited non-zero on every tick, causing the health monitor to incorrectly declare PostgreSQL down, stop and restart it, stop and restart `spiralstratum` as a cascade dependency, and repeat up to 3 times per hour — even though PostgreSQL and the DGB node were perfectly healthy the entire time. Sentinel, which polls the stratum API to determine node health, would then fire "node down" and "node recovered" alerts on each stratum restart cycle. Fixed by replacing the `sudo psql` check with `/usr/bin/pg_isready -h 127.0.0.1 -p 5432 -q`, which confirms PostgreSQL is accepting connections without requiring any authentication or sudoers configuration. The now-unnecessary `NOPASSWD: /usr/bin/psql -c SELECT 1` entry removed from both `install.sh` and `upgrade.sh`. `upgrade.sh` now includes a migration step that patches the deployed `health-monitor.sh` on existing servers and restarts the health monitor service automatically.

### Fixed · XEC Deep Audit 

**`src/stratum/internal/pool/coinpool.go`**
- **XEC RTT block-hash byte order inverted — every valid XEC block fails RTT** — `hex.DecodeString(result.BlockHash)` already produces big-endian bytes (most-significant byte first), matching the `new(big.Int).SetBytes(blockHashBE)` call inside `CheckRTTTargetRaw()`. The code then immediately reversed the 32 bytes in a for-loop labelled "Convert from daemon display order (big-endian) to internal order", making the input to `CheckRTTTargetRaw` little-endian. Since RTT compares the hash numerically against the target, an inverted hash would compare as a completely different value — a valid block would fail RTT and be discarded; an invalid block might (probabilistically) pass. Removed the reversal loop entirely; `blockHashBytes` is now passed to `CheckRTTTargetRaw` as-is from `hex.DecodeString`.

**`src/stratum/internal/jobs/manager.go`**
- **XEC output script length encoded as single byte — breaks for scripts ≥ 253 bytes** — `buildCoinbaseTx()` used `byte(len(script))` to write the output script length for the pool-reward output (line 1233), the MinerFund output (line 1241), and the StakingRewards output (line 1250). Bitcoin varint encoding requires a three-byte prefix (`0xfd` + uint16 LE) for lengths ≥ 253; a single byte is only valid for lengths 0–252. The witness commitment at line 1296 already used `crypto.EncodeVarInt()` correctly. Changed all three to `crypto.EncodeVarInt(uint64(len(script)))...` for consistency and protocol correctness.
- **XEC mandatory outputs dropped on invalid witness commitment** — When `template.DefaultWitnessCommitment` failed hex-decode or format validation, the fallback code rebuilt `cb2` from scratch using hard-coded `0x01` output count and a single pool-reward output, discarding already-computed MinerFund and StakingRewards data. The network requires these outputs unconditionally; a block missing them is rejected. Fixed both fallback paths (invalid-hex and invalid-format) to compute the correct output count (`1 + hasMinerFund + hasStakingReward`), re-append MinerFund and StakingRewards outputs in the correct order, and use `crypto.EncodeVarInt` for script lengths.
- **MinerFund output skipped when MinimumValue == 0 but Addresses present** — `mf.MinimumValue > 0` was used as the gate for including the MinerFund output; if the network sends a MinimumValue of 0 (e.g. during a soft-fork activation grace period) but still includes addresses (indicating the output is required), the pool would build a block without the mandatory output and get rejected. Changed to `len(mf.Addresses) > 0` — presence of addresses is the authoritative signal. If MinimumValue is 0, the output is still included with a 0-satoshi value, which is valid.
- **StakingRewards output skipped when MinimumValue == 0** — Same issue: `sr.MinimumValue > 0` gate removed; changed to `sr.PayoutScript.Hex != ""` as the single gate for including the staking output.

**`src/stratum/internal/coin/ecash.go`**
- **Duplicate `cashAddrPolymod` / `cashAddrVerifyChecksum` / `cashAddrDecode` in same package** — Previous session added three functions to `ecash.go` that already existed in `bitcoincash.go` (same `coin` package), causing a redeclaration compile error. Additionally, the `ecash.go` version of `cashAddrVerifyChecksum` had a bug: it used `uint64(c)` for prefix expansion instead of `uint64(c) & 0x1f` (lower 5 bits only), so checksum verification would always fail for valid addresses. Removed all three functions from `ecash.go`; replaced the two-step verify+decode call in `decodeCashAddr()` with a single call to `decodeCashAddrDataWithPrefix(XECCashAddrPrefix, bare)` which already exists in `bitcoincash.go` and uses the correct prefix expansion.

**`scripts/linux/regtest.sh`**
- **XEC missing from `COIN_DATA_DIR` cleanup map** — `COIN_SYMBOL=XEC` fell through to the `*)` case, leaving `COIN_DATA_DIR=""` and printing "Unknown coin data directory for XEC — manual cleanup may be needed" on every run. Added `XEC) COIN_DATA_DIR="$HOME/.bitcoin-abc" ;;` (matching `DATA_DIR=.bitcoin-abc` set in the xec coin block at line 396).
- **XEC daemon data directory collides with BTC on startup** — `ecashd` is a symlink to `bitcoind`; without an explicit `-datadir` it defaults to `~/.bitcoin`, overwriting BTC chain data in multi-coin test environments. Added `-datadir="$HOME/.bitcoin-abc"` to all four startup paths where the same fix already existed for FBTC: initial `DAEMON_ARGS`, restart `RESTART_ARGS`, HA `HA_DAEMON_ARGS`, and auxiliary daemon `AUX_DAEMON_ARGS`.

### Fixed · Audit

**`install.sh`**
- **`pip` silent failure under `set -e`** — `pip_output=$(cmd)` would exit the installer silently on pip failure; replaced with `|| pip_exit=$?` capture pattern so failures are logged and handled
- **DGB stratum log line unconditional** — `log "DGB Stratum: port 3333..."` fired regardless of `ENABLE_DGB`; wrapped in guard
- **BCH2/BTCS missing from `cleanup_on_failure()`** — stop/disable/rm-service blocks omitted `bitcoincashIId` and `bitcoinsilverd`; added
- **`libminiupnpc17` has no install candidate on Ubuntu 24.04+/Debian 13** — replaced with `libminiupnpc-dev`; added `.so.17 → .so.21` compat symlink block after apt-get
- **`libevent-pthreads-2.1-7t64` split on Ubuntu 26.04** — replaced with `libevent-dev`, which pulls the correct split packages on all supported distros
- **BTCS symlink not guaranteed after install** — `install_bitcoinsilver()` skipped symlink creation when binary already existed; added guaranteed symlink block after both install paths
- **`python3-requests` missing from apt package list** — sentinel uses system Python with `requests` as a third-party dep; added to apt install list
- **`wait_for_daemon()` infinite loop on timeout** — `wait_count=0` reset instead of `break` when timeout was hit; fixed to `return 0` after 5-minute ceiling
- **`while ! all_synced` loop had no daemon liveness check** — added per-coin `systemctl is-active` check; warns after 3 consecutive daemon-down cycles instead of spinning forever
- **`gunzip` corruption silent in DB restore** — `gunzip | psql || log_warn "normal"` produced an empty database on a corrupt archive with no fatal error; added `gunzip -t` integrity check; restore now aborts on corruption
- **New-coin RPC passwords blank on native upgrade** — upgrade path read existing passwords but never generated them for newly-added coins; added `_gen_rpc_pass()` fallback for all 15 coins after credential recovery
- **New-coin RPC passwords blank on Docker upgrade** — same gap in Docker upgrade path; same fix applied
- **BCH2/BTCS missing from coins.env read block on upgrade** — `BCH2_RPC_PASSWORD` and `BTCS_RPC_PASSWORD` not read from `coins.env` on native upgrade; added both
- **Sudoers syntax error logged as warning** — demoted `log_warn` to `log_error` with remediation message so operators are not misled
- **BCH2/BTCS missing from Enabled-coins config comment** — header comment omitted the two coins from the enabled list; added
- **`coins.env` non-atomic write** — `tee coins.env` then `chmod 600` left the file briefly world-readable on crash; fixed to write to `.tmp.$$`, chmod, then `mv`
- **`compress_backup()` non-atomic write + no size check** — `tar` wrote directly to the output file, producing a partial archive on interruption; fixed to write to `.tmp.$$`, validate non-zero size, then `mv`
- **`LITECOIN_VERSION` global constant stale** — top-level constant was `0.21.4`; updated to `0.21.5.4` to match the local function constant and coin-upgrade.sh

**`upgrade.sh`**
- **`pip` failures silently swallowed** — `2>/dev/null` hid all pip output; replaced with captured output and `|| _pip_rc=$?` pattern

**`src/dashboard/requirements.txt`**
- **`psycopg2-binary==2.9.9` incompatible with Python 3.13+** — `_PyInterpreterState_Get` was removed in Python 3.13; pinned version fails to compile; changed to `>=2.9.10`

**`scripts/linux/pool-mode.sh`**
- **`install_node_if_needed` skipped version comparison** — all coins returned early if the binary existed with no version check, preventing upgrades via SpiralDash; now compares installed version against target before deciding to skip or re-download
- **Hardcoded version strings in `install_node_if_needed`** — all per-coin version constants replaced with `_coin_upgrade_target()` calls; versions are now resolved dynamically at install time rather than baked in at release
- **Stale `/tmp` archives reused across runs** — `if [[ ! -f "$ARCHIVE" ]]` skipped re-download of stale or partial archives; replaced with unconditional `rm -f` before each `wget`
- **NMC GitHub latest tag has no Linux binary assets** — `namecoin/namecoin-core` `releases/latest` returns `nc31.0` which ships no pre-built binaries; removed NMC from the GitHub lookup so it falls through to coin-upgrade.sh's pinned `28.0`

**`docker/docker-compose.yml`**
- **Healthcheck credential exposure (CR-2)** — All 15 coin container healthchecks previously passed `-rpcuser=` and `-rpcpassword=` as CLI arguments, making credentials visible in `docker inspect` output and readable from `/proc/*/cmdline` by any process in the container. Changed all healthchecks to use `-conf=/home/<coin>/.<coin>/<coin>.conf`, reading credentials from the mounted config file at runtime (DGB-Scrypt has no separate container — it shares the DGB daemon)
- **Container privilege escalation prevention** — Added `security_opt: no-new-privileges:true` to stratum and dashboard service containers; prevents container processes from gaining elevated privileges via setuid/setgid binaries

**`install.sh` — BCH2 / BC2 RPC port isolation**
- **BCH2 rpcbind corrected to `127.0.0.1:8533`** — BCH2 was previously binding on all interfaces on port 8339, conflicting with BC2's assigned RPC port. Fixed `rpcbind` to `127.0.0.1` and `rpcport` to `8533`, isolating BCH2 to its own port and freeing 8339 exclusively for BC2 (`bitcoiniid`)

### Changed · Audit 

**`scripts/linux/pool-mode.sh`**
- **Dynamic coin version resolution** — added `_github_latest_version()`, `_coin_upgrade_target()`, `_write_version_cache()`, and `_coin_installed_version()` helpers; `install_node_if_needed` now queries the GitHub `releases/latest` API for each coin's target version and falls back to the `COIN_TARGET` array in `coin-upgrade.sh` when offline or rate-limited; installed version is read from the version cache (written on first install), then from binary `--version` output

### Attribution
- Block history, Multi-Coin Rotation widget, and initial difficulty fetch ported and modified from [bkhuraijam](https://github.com/bkhuraijam/Spiral-Pool)
- ETB network difficulty display ported and modified from bkhuraijam
- Bitcoin Knots v29.3.knots20260508 upgrade and source-build Dockerfile approach by [bkhuraijam](https://github.com/bkhuraijam/Spiral-Pool) ([commit 35fc61f](https://github.com/bkhuraijam/Spiral-Pool/commit/35fc61f585e30dc2562a4ca31cb773ff65d1c9cb))
- FBTC difficulty fallback fix ported from [kamakhu](https://github.com/bkhuraijam/Spiral-Pool/commit/8100071fc0498dcf5d9922cb38486e754e334f14) (bkhuraijam/Spiral-Pool)
- Time-based effort calculation, FBTC difficulty hardening, daily blocks chart refresh, and lastBlockTime database initialization ported and modified from [kamakhu](https://github.com/kamakhu) (bkhuraijam/Spiral-Pool)

### Documentation
- `docs/setup/OPERATIONS.md` — added "0a. Server Preparation — Debian 13 Trixie" subsection
- `README.md` — added Debian 13 row to Platform Support table; updated Prerequisites
- `docs/setup/WINDOWS_GUIDE.md` — updated production OS recommendation to include Debian 13

### Ported Upstream Commits

The following commits were ported from forks of this repository.
All commits were security-scanned, personally-identifiable data sanitized,
and personal operational customizations reverted before integration.

### Commit 1 — Full stack fixes (stratum API, Sentinel, Docker)
**Commit:** https://github.com/SpiralPool/Spiral-Pool/commit/91464e2c04c50b473e945f81069ca730e48d002a
**Date:** 2026-05-14 | **Contributor:** Kamakhu (SpiralPool)
**Security:** CLEAN — personal wallet address, hostname, and LAN IPs sanitized from `dashboard_config.json`; port remap, Werkzeug flag, Grafana exposure, and hardcoded stratum difficulty all reverted

**Changes:**
- `GetBlocksWithOrphans()` now accepts a `limit int` parameter; V1 and V2 API endpoints parse `?pageSize=` query parameter (default 100/200, max 5000)
- `Effort: share.Difficulty` now stored in block DB record at block-find time
- `SpiralSentinel.py` — sync check interval reduced from 60s to 30s
- `SpiralSentinel.py` — block alert retry queue: failed Discord notifications for block-found alerts are persisted to disk and retried each monitoring cycle instead of being dropped to a log file
- `SpiralSentinel.py` — Discord retries increased from 3 to 5 for block-found alerts specifically
- `SpiralSentinel.py` — block dedup switched from worker-name + hash to hash-only (CGMiner worker name mismatch was silently suppressing pool-side block alerts)
- `docker-compose.yml` — ZMQ ports exposed for DGB (28532), BTC (28332), BCH (28432), FBTC (28340); `extra_hosts: host.docker.internal` added to stratum and dashboard; SmartPort 16180 added; PROMETHEUS_URL added to dashboard env
- `docker/config/dashboard_config.json` — sanitized example config created (wallet address and device IPs replaced with documented placeholders)

---

### Commit 2 — Network difficulty in ETB widget
**Commit:** https://github.com/SpiralPool/Spiral-Pool/commit/6e5e0783760d45d91397328dba0ab8ceff6422ce
**Date:** 2026-05-14 | **Contributor:** Kamakhu (SpiralPool)

**Changes:**
- Est. Time to Block card now shows current network difficulty alongside the 24h probability
- Difficulty updates when switching coins even with no hashrate
- No-hashrate state clears ETB while still displaying difficulty

---

### Commit 3 — True pool effort calculation
**Commit:** https://github.com/SpiralPool/Spiral-Pool/commit/78612934d00596fc0345b7e705b55f5d9d9d4622
**Date:** 2026-05-14 | **Contributor:** Kamakhu (SpiralPool)

**Changes:**
- `CoinPool` and `Pool` now track cumulative share difficulty per mining round (`currentRoundDifficulty`) using a mutex-protected accumulator
- Effort calculated at block-find time as `(roundDiff / networkDiff) × 100` and stored in the blocks table
- Round accumulator resets to 0 after each block for the next round
- `GetPoolEffort()` now returns live round effort instead of 0
- `dashboard.py` — blocks with `effort=0` (legacy blocks) now display `---` instead of a garbage fallback calculation
- Effort uses FBTC-corrected network difficulty in `CoinPool.handleBlock()`

---

### Commit 4 — FBTC indexing provider cycle fix
**Commit:** https://github.com/SpiralPool/Spiral-Pool/commit/8100071fc0498dcf5d9922cb38486e754e334f14
**Date:** 2026-05-14 | **Contributor:** Kamakhu (SpiralPool)

**Changes:**
- When `getdifficulty` RPC returns 1 (FBTC indexing provider cycle), network difficulty now falls back to the cached validator difficulty for both `NetworkDifficulty` in the block record and for effort calculation

---

### Commit 5 — Per-coin accepted and rejected shares
**Commit:** https://github.com/SpiralPool/Spiral-Pool/commit/41261f1ae47e1e106aa86a230da1853cdeb5d1b1
**Date:** 2026-05-14 | **Contributor:** Kamakhu (SpiralPool)

**Changes:**
- `GetAcceptedShares()` and `GetRejectedShares()` added to `StatsProvider` (V1) and `CoinPoolProvider` (V2) interfaces
- Implemented in `Pool`, `CoinPool`, and stubbed in `auxPoolProvider`
- `acceptedShareCount atomic.Int64` per-pool field added; incremented on each accepted share
- V1 and V2 API endpoints now expose `acceptedShares` and `rejectedShares` in pool stats responses
- Dashboard displays per-coin accepted shares and reject rate when a specific coin is selected from the header badge
- Fixed pre-existing compilation bug: `bestShareDiff` variable was used without a `var` declaration in `server.go`

---

### Commit 6 — Per-coin session best share difficulty
**Commit:** https://github.com/bkhuraijam/Spiral-Pool/commit/a1db09e965dea2ccb619148de3ccc9fcc7ad4bd2
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)

**Changes:**
- `GetBestShareDiff()` added to `StatsProvider` (V1) and `CoinPoolProvider` (V2) interfaces
- Implemented in `Pool` and `CoinPool` using `atomic.Uint64` storing float64 bits with a lock-free CAS loop for thread-safe updates
- `bestShareDiffBits atomic.Uint64` per-pool field; updated on every accepted share via CAS loop
- V1 and V2 API endpoints now expose `bestShareDiff` in pool stats responses
- Dashboard displays per-coin session best share diff when a coin is selected

---

### Commit 7 — Per-coin all-time best share difficulty
**Commit:** https://github.com/SpiralPool/Spiral-Pool/commit/66ef12ad17313993caabb8cc6069f16df2ad534f
**Date:** 2026-05-14 | **Contributor:** Kamakhu (SpiralPool)

**Changes:**
- `lifetime_stats["per_coin_best_diff"]` dict persists per-coin all-time best share difficulty across dashboard restarts
- Dashboard shows per-coin all-time best share diff (from `lifetime_stats`) when a coin is selected
- Aggregate best-diff view still shows global all-time best when no coin is selected
- `_lastLifetimeStats` JS variable caches lifetime stats for fast per-coin lookups in the UI

---

### Commit 8 — BCH2 and BTCS coin support (PHASE 5)
**Commit:** https://github.com/MESKONE0722/Spiral-Pool/commit/47e86a9009ed1a86985e61c7c20dc90a4eeca9dd
**Date:** 2026-05-14 | **Contributor:** MESKONE0722

**Changes:**
- **BCH2 (Bitcoin Cash II)** — full SHA-256d coin implementation:
 - `src/stratum/internal/coin/bch2.go` — address validation (CashAddr `bitcoincashii:` prefix + legacy Base58Check), coinbase script builder, SHA256d header hashing, complete `Coin` interface
 - `docker/Dockerfile.bitcoincashii` — v27.0.2, x86_64 only, corrected release URL format (`bitcoincashII-v{V}-linux-x86_64.tar.gz`), ZMQ confirmed in BCH2 source tree
 - `docker/config/bitcoincashii.conf.template` — BCH-style config, ports 8534/8533/28533
 - `docker-compose.yml` — `bitcoincashii` service block with profiles `["bch2", "multi"]`, stratum ports 5336-5338, healthcheck, named volume
 - `install.sh` — port vars, BCH2_RPC_USER=spiralbch2, ENABLE_BCH2, BCH2_POOL_ADDRESS, address prompt with CashAddr validation
 - `config/coins.manifest.yaml` — BCH2 entry with genesis hash, ports, and CashAddr flag
 - `src/sentinel/SpiralSentinel.py` — BCH2 default coin config entry
 - `src/dashboard/dashboard.py` — BCH2 added to VALID_COINS, WALLET_PATTERNS, port map, coin-type map
- **BTCS (Bitcoin Silver)** — full SHA-256d coin implementation:
 - `src/stratum/internal/coin/btcs.go` — address validation (B-prefix P2PKH, 3 P2SH, bs1q SegWit, bs1p Taproot), full BTC-style coinbase script builder with P2WPKH/P2WSH/P2TR support
 - `docker/Dockerfile.bitcoinsilver` — source build pinned to commit `ff5c3c3d` via targeted `git fetch --depth=1` (supply chain protection), ZMQ confirmed in BTCS source tree
 - `docker/config/bitcoinsilver.conf.template` — BTC-style config, ports 10566/10567/28567
 - `docker-compose.yml` — `bitcoinsilver` service block with profiles `["btcs", "multi"]`, stratum ports 11335-11337, healthcheck, named volume
 - `install.sh` — port vars, BTCS_RPC_USER=spiralbtcs, ENABLE_BTCS, BTCS_POOL_ADDRESS, address prompt with B-prefix/bech32 validation
 - `config/coins.manifest.yaml` — BTCS entry with genesis hash, ports, SegWit flag
 - `src/sentinel/SpiralSentinel.py` — BTCS default coin config entry
 - `src/dashboard/dashboard.py` — BTCS added to VALID_COINS, WALLET_PATTERNS, port map, coin-type map
- **Both coins** — SmartPort multiport rotation supported (standard SHA-256d, no coordinator changes required)
- `src/stratum/internal/coin/manifest_test.go` — expected coin count updated 14→16, SHA256d 9→11

---

### Commit 9 — lastBlockTime database initialization on startup
**Commit:** https://github.com/bkhuraijam/Spiral-Pool/commit/b1de5fe
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)

**Changes:**
- `initBlockStats()` in `coinpool.go` now calls `postgresDB.GetLastBlockFoundTime()` on startup and seeds `cp.lastBlockTime` from the database result
- Prevents effort calculation from treating epoch start as "previous block time" after a daemon or stratum restart, which inflated effort to unrealistic values until the first block was found

---

### Commit 10 — Time-based effort, daily blocks endpoint, FBTC difficulty hardening
**Commit:** https://github.com/bkhuraijam/Spiral-Pool/commit/7987d93
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)
**Security:** CLEAN — personal operational defaults (report currency, display timezone, difficulty alert flag) reverted to project defaults before integration

**Changes:**
- `coinpool.go` — replaced round-accumulator effort with time-based effort: `effortPercent = (actualSeconds / expectedSeconds) × 100` where `expectedSeconds = (networkDiff × 2^32) / poolHashrate`; `lastBlockTime` mutex-protected field tracks inter-block intervals
- `coinpool.go` — added `lastGoodNetworkDiff` / `lastGoodDiffMu` fields; `GetMiningDifficulty()` returns the cached last-known-good difficulty for FBTC when live difficulty is outside the valid range (1 < diff < 1e12), covering both the indexing-provider cycle (=1) and merged-mining spike (>1T)
- `coinpool.go` — `lastGoodNetworkDiff` updated in `Start()` and both fetch paths of `difficultyLoop()` for FBTC
- `sentinel.go` — `checkMultiPortDifficultySpike()` skips FBTC difficulty readings where `prev ≤ 1`, `current ≤ 1`, `prev > 1e12`, or `current > 1e12`, preventing false spike alerts during indexing or merged-mining cycles
- `dashboard.py` — new `GET /api/blocks/daily` endpoint: aggregates per-coin block counts by date over the last 30 days; coin-filterable via `?coin=` query param; protected by `@api_key_or_login_required`; uses `POOL_API_URL` env var and `get_enabled_coins()` for dynamic discovery (no hardcoded credentials or pool IDs)
- `dashboard.html` — `fetchDailyBlocks()` now called in the coin-select handler (after `applyStatsCoinFilter()`) and in the main data fetch cycle (between `fetchETBData()` and `fetchLeaderboard()`), in addition to the existing `DOMContentLoaded` call ported in commit b6a7e98

---

### Commit 11 — Per-coin lastBlockTime initialization for effort calculation
**Commit:** https://github.com/bkhuraijam/Spiral-Pool/commit/ecca3e7c3260d4ff6137f91772903e9d80f8a1f1
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)

**Changes:**
- `GetLastBlockFoundTimeForPool(ctx, poolID)` added to `postgres.go` — queries the per-coin `blocks_<poolID>` table directly using a caller-supplied `poolID` instead of relying on the shared `db.poolID` field of the DB connection
- `initBlockStats()` in `coinpool.go` now calls `GetLastBlockFoundTimeForPool(ctx, cp.poolID)` instead of `GetLastBlockFoundTime(ctx)`, ensuring each coin pool seeds its `lastBlockTime` from its own block table at startup
- Fixes: when multiple coins shared a single DB connection, all coins were inheriting DGB's last block time because `GetLastBlockFoundTime` used `db.poolID` (DGB's ID) regardless of which `CoinPool` called it

---

### Commit 12 — XEC (eCash / Bitcoin ABC) full coin integration
**Fork:** https://github.com/bkhuraijam/Spiral-Pool
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)
**Security:** CLEAN — no hardcoded wallets, credentials, or personal addresses; all passwords injected via environment variables; XEC P2P port set to 8343 (avoids collision with BTC 8333)

**Changes:**
- `src/stratum/internal/coin/ecash.go` — `ECashCoin` type implementing the full `Coin` interface: CashAddr address validation and decoding (`ecash:q…` P2PKH, `ecash:p…` P2SH), coinbase script builder (P2PKH + P2SH), SHA-256d block header serialization, RTT (Real Time Target) difficulty checking via `CheckRTTTargetRaw`, `GetBlockTemplate` consuming `coinbasetxn.minerfund` and `coinbasetxn.stakingrewards` for mandatory post-Nov-2025 coinbase outputs, full `SerializeXECCoinbaseTx` with MinerFund and StakingRewards outputs
- `docker/Dockerfile.ecash` — Bitcoin ABC v0.31.12, x86_64 only, official release tarball from GitHub Releases
- `docker/docker-compose.yml` — `ecash` service block, profiles `["xec", "multi"]`, stratum ports 18338/18339/18340, P2P 8343, RPC 9004, ZMQ 28335, healthcheck via `bitcoin-cli -conf=…/bitcoin.conf getblockchaininfo`
- `docker/config/ecash.conf.template` — standard Bitcoin ABC config: RPC on 9004, ZMQ on 28335, P2P on 8343, credential injection via `${RPC_USER}` / `${RPC_PASSWORD}`
- `docker/.env.example` — `XEC_RPC_USER`, `XEC_RPC_PASSWORD`, `ENABLE_XEC`, `XEC_POOL_ADDRESS`, `XEC_DATA_DIR` (commented), daemon override table updated
- `install.sh` — port variables (XEC_RPC_PORT=9004, XEC_P2P_PORT=8343, XEC_ZMQ_PORT=28335), address prompt with CashAddr regex (`^ecash:[qp][a-z0-9]{41,}$`), UFW rules for 18338–18340/tcp and 8343/tcp, coin menu option 17 (single-coin) and multi-coin toggle (sel_xec), Docker profile injection, upgrade-path credential/address preservation, `_PASS_RECOVERY` map entry `[XEC]="xec:bitcoin.conf"`
- `coin-upgrade.sh` — `COIN_TARGET[XEC]="0.31.12"`, `COIN_RISK[XEC]="NONE"`, `COIN_SERVICE[XEC]="bitcoind"`, `COIN_DAEMON_CMD[XEC]="bitcoind"`, `COIN_CLI_CMD[XEC]="bitcoin-cli"`, `COIN_CONF[XEC]`, `COIN_ENV_FLAG[XEC]="ENABLE_XEC"`, `download_XEC()` fetching `bitcoin-abc-0.31.12-x86_64-linux-gnu.tar.gz`, install case `BCH|FBTC|XEC`
- `scripts/spiralctl.sh` — daemon `ecashd`, CLI `bitcoin-cli -conf=…/bitcoin.conf`, `conf_name="bitcoin.conf"`, built-in coin guard, `ecash → XEC` normalisation in single-coin detect, SHA-256d help text updated
- `src/dashboard/dashboard.py` — `XEC` in `VALID_COINS` (both locations), CashAddr `WALLET_PATTERNS` regex, `COIN_CONFIGS["XEC"]` (RPC 9004, conf `/spiralpool/xec/bitcoin.conf`), `COINGECKO_IDS["XEC"] = "ecash"`, `normalize_coin` entries for `ECASH` and `BITCOIN-ABC`
- `config/coins.manifest.yaml` — XEC entry: `algorithm: sha256d`, `rpc_port: 9004`, `p2p_port: 8343`, `zmq_port: 28335`, `stratum_port: 18338`, `stratum_v2_port: 18339`, `stratum_tls_port: 18340`, `supports_cashaddr: true`, `genesis_hash` (Bitcoin genesis), `coingecko_id: "ecash"`
- `src/stratum/internal/coin/manifest_test.go` — expected coin count updated 16→17, SHA256d 11→12
- `docs/reference/MULTI_COIN_PORT.md` — XEC row added to stratum port reference table

---

### Commit 13 — Block History includes all supported coins
**Commit:** https://github.com/bkhuraijam/Spiral-Pool/commit/fdb5fe34ced6233b1e27a987e1da1fc4b1e10d3a
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)

**Changes:**

---

### Commit 14 — Worker Statistics section (foundation)
**Commit:** https://github.com/bkhuraijam/Spiral-Pool/commit/847435523dface3d9e2bc23d08f454b9f1ef12b8
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)

**Changes:**
- `src/stratum/internal/database/migrate.go` — `actual_difficulty DOUBLE PRECISION NOT NULL DEFAULT 0` added to the `CREATE TABLE shares_*` template in `createPoolTables`; new migration v11 (`add_actual_difficulty`) registered in both the standard migrations slice and `poolMigrations` (idempotent `ALTER TABLE shares_<poolID> ADD COLUMN IF NOT EXISTS actual_difficulty`).
- `src/stratum/internal/database/postgres.go` — `actual_difficulty` added to the `WriteBatch` `CopyFrom` column list and value tuple, sourced from `s.ActualDifficulty`.
- `src/stratum/internal/database/postgres_v2.go` — same change for `WriteBatchForPool`.
- `src/stratum/internal/pool/coinpool.go` — `share.ActualDifficulty = result.ActualDifficulty` set immediately before `cp.sharePipeline.Submit(share)` in both `handleShare` and `HandleMultiPortShare`, so the true per-share hash difficulty (rather than the assigned target) reaches the DB.
- `src/stratum/internal/pool/pool.go` — same assignment in the v1 `handleShare`.
- `src/stratum/pkg/protocol/protocol.go` — `ActualDifficulty float64` field already present on `Share`; no change required in this codebase.
- `src/dashboard/dashboard.py` — new `get_worker_stats_from_db(pool_ids, minutes=1440)` helper queries each `shares_<pool_id>` table (skipping any whose table does not exist), groups by `worker`, returns shares count, best diff (`GREATEST(MAX(difficulty), MAX(actual_difficulty))`), avg diff, max network diff, first/last share timestamps, and a hashrate estimate (`shares/elapsed × avg_diff × 2^32`).
- `src/dashboard/templates/dashboard.html` — new collapsible `Worker Statistics` section inserted above the Multi-Coin Rotation widget; `worker-stats-section` added to `restoreCollapsedStates`; coin-selector now filters `.worker-coin-group` blocks in place.

---

### Commit 15 — Worker Statistics time window switcher
**Commit:** https://github.com/bkhuraijam/Spiral-Pool/commit/63941f1ef66cca0626ee5f783fe9f9699a612bc4
**Date:** 2026-05-14 | **Contributor:** Kamakhu (bkhuraijam fork)

**Changes:**
- `src/dashboard/dashboard.py` — `get_worker_stats_from_db` parameter changed from `hours: int = 24` to `minutes: int = 1440` so the same helper serves the 10 min, 1 h, and 24 h windows; SQL `WHERE` clause now uses `INTERVAL '{minutes} minutes'`. New admin-gated `GET /api/worker-stats?minutes=10|60|1440` endpoint (`api_worker_stats`) reuses the helper and projects per-pool rows into a coin-keyed JSON payload. The upstream port enumerated only five pool IDs in the endpoint; this port introduces a `WORKER_STATS_POOL_MAP` covering all 17 supported coins so the panel works on every deployment.
- `src/dashboard/templates/dashboard.html` — collapsible header gains three time-window buttons (`ws-btn-10`, `ws-btn-60`, `ws-btn-1440`) with `event.stopPropagation()` so clicks don't toggle the section; body container renamed to `worker-stats-body`; client-side `fetchWorkerStats(minutes)` / `renderWorkerStats(data)` replace the section innerHTML and update the title and button styling; `formatWorkerHashrate` / `formatWorkerDiff` render H/s through PH/s and K through P. Coin color/background tables (`WORKER_COIN_COLORS`, `WORKER_COIN_BG`) and a canonical `WORKER_COIN_ORDER` are added covering every supported coin (upstream only styled the five sha256d coins); unmapped coins fall back to the cyan accent. Initial fetch fires on `DOMContentLoaded`, eliminating the Jinja-side server-render path (so `worker_stats` no longer needs to be passed to the template).

---

## [2.4.2] - 2026-04-14 - Phi Hash Reactor

> *Multi-port stale share storm fix, Antminer user-agent classification fix for proper vardiff assignment.*

### Fixed

**Stratum — Multi-Port Stale Share Storm During Slow GetBlockTemplate**

- **All multi-port shares rejected as stale for 1-3+ seconds after every ZMQ block notification** — `OnBlockNotificationWithHash()` in both `manager.go` and `manager_v2.go` immediately set all existing jobs to `JobStateInvalidated` the moment ZMQ fired, BEFORE calling `GetBlockTemplate` to fetch the replacement template. When the daemon was busy processing a new block, the GBT RPC took 1-3+ seconds to return. During that entire window, every multi-port share was validated against the already-invalidated jobs and rejected as stale. Direct (single-coin) miners were unaffected because their stale check uses the stratum server's `s.jobs` map, which isn't cleared until `BroadcastJob(cleanJobs=true)` runs after the new template is ready. On DGB with 15-second blocks, a 3.4-second stale window meant ~25 rejected shares per block transition across 6 sessions — and any block-level solution found during that window was discarded, causing orphaned blocks
- **Fix**: Removed the premature job invalidation from both `manager.go` and `manager_v2.go`. Old jobs now stay valid until `RefreshJob` succeeds and `BroadcastJob(cleanJobs=true)` naturally invalidates them — matching how direct miners already work. The height epoch advance (which cancels in-flight block submission contexts) is preserved. The narrow risk of a share solving a block against an outdated `prevBlockHash` is handled by the daemon rejecting the submission, which is far less costly than rejecting ALL shares for the entire GBT fetch duration

**Stratum — Antminer User-Agent Not Recognized by SpiralRouter**

- **All Antminers sending model-based user-agents classified as `MinerClassUnknown`** — Some Antminer firmware versions (notably S19 series and S19k Pro) send `"Antminer S19k Pro/{date}"` or `"Antminer BHB42XXX/{date}"` as their stratum user-agent instead of the expected `"bmminer/{version}"`. The SpiralRouter pattern list only had `(?i)bmminer` for Bitmain devices, so these miners fell through to `MinerClassUnknown`. Because `multiserver.go` forces config difficulty for unknown miners (`useConfig := ... || profile.Class == MinerClassUnknown`), all 8 affected miners were stuck on static config difficulty instead of receiving the correct Pro-tier vardiff profile (25,600 initial / 500,000 max / 1s target)
- **Fix**: Added `(?i)antminer` → `MinerClassPro` pattern to the SpiralRouter detection list, directly after the existing `(?i)bmminer` pattern. Antminers sending model-name user-agents are now correctly classified and receive the Pro difficulty profile tuned for S19-class hardware (~110 TH/s)

---

## [2.4.1] - 2026-04-13 - Phi Hash Reactor

> *Smart Port start_hour scheduling fix, ZMQ job broadcast race condition fix, sentinel tuning.*

### Fixed

**Stratum — Smart Port `start_hour` Ignored**

- **Fix**: `StartHour *float64` is now passed from config through `coordinator.go` into `CoinWeight`. `buildTimeSlots()` sorts coins by `StartHour` (then alphabetically for coins without one), computes an `anchorFrac` from the earliest `StartHour`, and returns it alongside the time slots. `SelectCoin()` shifts the current day-fraction into anchor-relative space before slot lookup, ensuring coins mine at their configured hours. The logic matches the dashboard's Python schedule builder exactly

**Stratum — ZMQ Job Broadcast Delayed for Multi-Port Miners**

- **Multi-port miners received new block jobs 4-8 seconds late after ZMQ notification** — in `coinpool.go`, the job callback called `BroadcastJob()` synchronously (which iterates and writes to every single-coin session) BEFORE calling the multi-port listener. With 8+ miners, `BroadcastJob()` blocked for 2-5 seconds, causing the multi-port callback to fire late. Multi-port miners missed the ZMQ update and waited for the next `job_rebroadcast` tick instead. With DGB's 15-second block time, a 4-8 second delay meant miners worked on stale templates for ~30% of each block interval
- **Fix**: The multi-port listener callback is now launched in a goroutine (`go listener(job)`) BEFORE the blocking `BroadcastJob()` call. Multi-port miners receive the new job within milliseconds of the ZMQ notification, in parallel with the single-coin broadcast. `handleCoinJobUpdate()` is already concurrency-safe (uses `sync.Map` and per-session write locks)

**Sentinel — Difficulty Spike Alert Too Sensitive for DGB**

- **DGB +66% difficulty spikes triggered alerts despite being normal retarget behavior** — DGB adjusts difficulty every block (~2 min), causing frequent 50-66% swings. The 50% threshold caught these routine retargets. Raised threshold to 80% so only genuinely unusual spikes fire

**Sentinel — Share Rejection Alerts Fired Without Pool-Side Confirmation**

- **Rejection spike alerts fired for miner-reported 20% when pool-side was 0%** — the cross-reference was supposed to verify miner-reported rejections against pool-side Prometheus metrics before alerting. Two bugs caused false alerts: (1) when Prometheus metrics were unavailable (`_infra_health.metrics` is None), the fallback defaulted to `_pool_side_confirmed = True`, firing unverified alerts. (2) `stratum_shares_rejected_total` summed ALL labels including `reason="stale"`, inflating pool-side counts. Production logs confirmed: Heat2Sats fired 6 alerts in one day at 20% miner-reported / 0.0% pool-side — all were internal BitAxe hardware rejects that never reached the pool. Fixed by: (a) changing both fallback paths to suppress (`_pool_side_confirmed = False`) instead of confirm, and (b) excluding stale shares from pool-side rejection count

### Changed

- **Version bump** -- all version strings updated to 2.4.2

---

## [2.4.0] - 2026-04-10 - Phi Hash Reactor

### Fixed

**Dashboard — Setup Wizard Showed Solo Mode With Two Coins**

- **`syncingCoins` JavaScript scoping error crashed mode detection** — `const syncingCoins` was declared inside the `if (allActiveCoins.length > 0)` block but referenced outside it in the status text section. JavaScript `const` is block-scoped, so accessing it outside threw a `ReferenceError`. The `catch` handler caught this and called `selectPoolMode('solo')`, resetting the correctly-detected Multi-Coin mode back to Solo and truncating `selectedCoins` to one coin. Fixed by moving the `syncingCoins` declaration to the outer scope before the inner `if` block
- **Validation error persisted after coins were populated** — `selectPoolMode()` was called before `selectedCoins` was populated, triggering `validateCoinSelection()` with an empty array. The "Multi-Coin Mode requires at least 2 coins" error was displayed and never cleared because `validateCoinSelection()` was not called again after the coin loop populated `selectedCoins`. Fixed by adding a `validateCoinSelection()` call after `updateCoinSelectionUI()` / `updateWalletInputs()`

### Changed

- **Version bump** -- all version strings updated to 2.4.0

---

## [2.3.5] - 2026-04-09 - Phi Hash Reactor

> *Native multi-port config fix, health monitor false restart, startup timeout for syncing daemons.*

### Fixed

**Installer — Native Multi-Port Config Missing**

- **Native V2 config writer omitted entire `multi_port` section** — `configure_stratum_multicoin()` never wrote the `multi_port:` YAML block even when the user enabled Smart Port during installation. The Docker config writer (`generate_docker_stratum_config_multicoin`) had the code, but the native path was missing it entirely. Users who said "yes" to Smart Port got no `multi_port` config, so Smart Port never started. Fixed by adding the same `multi_port` generation block to the native V2 config writer

**Stratum — Health Monitor False Restart**

- **`health-monitor.sh` killed stratum on every cycle in multi-coin mode** — health monitor checked `mining.subscribe` on port 3333 (the V1 single-coin default). In V2 multi-coin mode, stratum listens on per-coin ports and Smart Port (16180), not 3333. The health monitor saw "protocol failure" and force-restarted stratum every check cycle, preventing Smart Port from ever starting (killed before DGB sync timeout could expire)

**Stratum — Startup Timeout**

- **Pool startup blocked indefinitely on syncing daemons** — `CoinPool.Start()` was called with the coordinator's root context (no timeout). If a daemon was syncing (e.g., DGB at 18% after fresh install), `waitForSync()` blocked forever, preventing `startWg.Wait()` from completing. Smart Port and retry loop code was never reached. Fixed by wrapping each `pool.Start()` call with a 90-second `context.WithTimeout`, allowing syncing coins to fail fast and move to the retry list while online coins proceed

**wait-for-node.sh — Awk Parsing**

- **`extract_v2_nodes()` returned empty output for V2 YAML configs** — awk rules for section detection (e.g., `nodes:`, `daemon:`, `stratum:`) matched AND reset flags in the same pass. The `nodes:` line matched both the "set `in_nodes=1`" rule and the "reset on unknown key" rule, immediately clearing the flag. Fixed by adding `next` statements to all section-detection rules so they skip to the next line after setting flags
- **All 6 awk functions vulnerable to quoting issues under systemd** — inline awk scripts with complex quoting broke under systemd's `PrivateTmp=yes` environment. Converted `extract_v2_nodes`, `get_coin_field`, `update_coin_field`, `get_auxchain_field`, `update_auxchain_field`, and `extract_v2_auxchains` to temp-file approach (`mktemp` + `cat > file <<'DELIM'` + `awk -f file`)
- **Pipe subshell lost variables with `set -e`** — `echo "$coins_raw" | while read` ran in a subshell; `get_rpc_creds` failures triggered `set -e` exit in the subshell, silently discarding results. Converted to here-string (`while read ... done <<< "$coins_raw"`) with `|| true` on `get_rpc_creds`

### Changed

- **Version bump** -- all version strings updated to 2.3.5

---

## [2.3.2] - 2026-04-09 - Phi Hash Reactor

> *Partial startup after reboot, block timestamp UTC fix, wallet balance reliability, upgrade service restart.*

### Fixed

**Stratum — Partial Startup After Reboot (Smart Port)**

- **Smart Port refused to start if any coin daemon was still syncing** — after a server reboot, `wait-for-node.sh` (ExecStartPre) required ALL daemons to be online before starting stratum. One slow daemon (e.g., DGB in D-state, BTC re-syncing) blocked mining on ALL coins indefinitely. Fixed with a layered partial startup: (1) `wait-for-node.sh` accepts partial readiness after 60 seconds, (2) coordinator starts Smart Port with whatever coins are available instead of requiring all, (3) late coins join seamlessly via `RegisterCoinPool` as their daemons come online
- **12 CLI calls in wallet setup could hang forever on unresponsive daemons** — `listwallets`, `loadwallet`, `createwallet`, and `getnewaddress` calls in `wait-for-node.sh` had no timeout. A daemon accepting connections but not responding (D-state) caused indefinite hang. All 12 CLI calls now wrapped with `timeout 10`
- **Wallet processing in partial mode touched ALL coins including offline ones** — `process_v2_wallets` iterated every configured coin. On fresh installs with `PENDING_GENERATION` addresses, each offline coin added 30 seconds of timeout delay (3 CLI calls × 10s). New `filter_ready_nodes()` function re-checks RPC (3s timeout) and only processes wallets for online coins
- **`initBlockStats` blocked `CoinPool.Start()` on slow database queries** — ran synchronously during startup; a slow or unavailable PostgreSQL connection stalled the entire coin pool. Now runs in a background goroutine with a 10-second timeout, tracked by the pool's WaitGroup for clean shutdown
- **`initBlockStats` race condition with `handleBlock`** — used `=` assignment which overwrote any blocks already counted by `handleBlock` during the DB query window. Changed to `+=` so in-session block increments are preserved
- **Selector routed miners to coins with no running pool** — `GetState()` returning `ok=false` (unregistered coin) caused the availability check to be skipped entirely, treating the coin as available. Miners were assigned to non-existent pools with silent share rejection. Fixed to treat unregistered coins as unavailable
- **`switchSessionCoin` had no pool validation** — could switch a miner to a coin whose pool didn't exist or wasn't running, causing all shares to be silently rejected until the next evaluation cycle. Now validates pool existence and `IsRunning()` before switching
- **`RegisterCoinPool` didn't trigger immediate miner re-evaluation** — when a recovered coin was registered with the MultiServer, miners stayed on their current coin until the next scheduled evaluation (up to 30s). Now calls `reevaluateAll()` immediately so miners can be routed to the new coin within seconds
- **Deferred multi-port startup required ALL coins to recover** — the retry loop only started Smart Port when `len(stillFailed) == 0`. One permanently-down coin prevented Smart Port from ever starting. Now starts as soon as any coin recovers (`len(succeeded) > 0`)

**Stratum — Block Timestamps**

- **Dashboard "Last Block Found" showed 4-hour offset** — `time.Now()` stored local EDT time in PostgreSQL `TIMESTAMP` (no timezone) column. When pgx read it back, the bare timestamp was interpreted as UTC, creating a 4-hour discrepancy. Blocks found minutes ago showed as "4h 20m ago" on the dashboard. Fixed by using `time.Now().UTC()` in all 4 block insert paths (coinpool.go primary + auxpow, pool.go primary + auxpow)

**Sentinel — Wallet Balance**

- **DGB wallet balance showed 0.00 in intel report** — `fetch_wallet_balance_for_coin()` used chainz.cryptoid.info API for DGB, which intermittently returned 0. Now uses local node `scantxoutset` RPC as primary method for ALL coins (authoritative, no external dependency). External APIs demoted to fallback only
- **Wallet address mismatch warning for multi-coin auto-detection** — when Sentinel auto-detected coins from the pool API, it inherited the pool's payout address as the wallet address. For multi-coin setups (DGB + FBTC), the DGB address was applied to failing `validateaddress`. Now prefers per-coin wallet address from Sentinel config, and suppresses mismatch warnings for auto-detected addresses

**Sentinel — Pool ID Patterns**

**Sentinel — Block Counter**

**Sentinel — Network Hashrate**

- **99.9% network hashrate crash false positive** — DGB showed a 54.39→0.08 PH/s drop that never happened on the network (garbage RPC reading during Smart Port switch). Added RPC cross-validation: for drops >95%, queries `getnetworkhashps` directly. If RPC shows <50% drop, rejects the garbage reading and skips crash detection + baseline EMA update

**Sentinel — Log Noise**

- **`get_primary_coin()` warning spammed every cycle** — with 2+ coins enabled, a WARNING-level message fired on every call (dozens per cycle). Now logs once at startup as INFO

**Upgrade**

- **`upgrade.sh` left Sentinel and Dashboard stopped after upgrade** — `systemctl stop` was called during upgrade but only stratum was restarted. Sentinel and Dashboard stayed dead until manual intervention. Now restarts all enabled services after stratum comes up

### Changed

- **Version bump** -- all version strings updated to 2.3.2

---

## [2.3.1] - 2026-04-08 - Phi Hash Reactor

> *Critical payment processor fix, Smart Port false positive suppression, production observability.*

### Fixed

**Payments — Critical**

- **Payment processor completely invisible in production logs** — every log message in `processCycle()` and `updateBlockConfirmations()` was at Debug level. With production log level set to Info, the processor emitted zero log lines — no cycle start, no block count, no errors, nothing. Impossible to diagnose why blocks were stuck. Added Info-level logs: cycle start (with HA state), pending block count per coin, and "no pending blocks" message. Existing Warn/Error logs for failures were already at correct level

**Sentinel — Smart Port False Positives**

- **`miner_disconnect_spike` false positive during Smart Port coin switch** — when the scheduler rotated miners from DGB to DGB connections dropped to near-zero, triggering a disconnect spike alert. The sentinel had no awareness of Smart Port — it only looked at per-coin connections. Fixed by checking total connections across ALL pools when `multiServer` is active. If total connections are stable (>50% of previous), the alert is suppressed as a coin switch, not a real disconnect event
- **`hashrate_drop` false positive during Smart Port coin switch** — same root cause as disconnect spike. DGB hashrate dropped when miners switched to triggering hashrate drop alert. Fixed with same Smart Port awareness: checks total hashrate across all pools before alerting. Only fires when aggregate fleet hashrate actually drops

**Sentinel — Intel Report**

- **Coins added via dashboard or pool-mode.sh have payments silently disabled** -- Go's `bool` zero-value is `false`. Any coin added without an explicit `payments: enabled: true` in config.yaml had its payment processor skipped — blocks were found and recorded but never confirmed or paid out. Three fixes applied: (1) Go `SetDefaults()` now unconditionally forces `Payments.Enabled = true` for every coin, (2) dashboard `save_pool_config()` now injects `payments: {enabled: true}` for coins missing the section, (3) `pool-mode.sh` coin templates changed from `enabled: false` to `enabled: true`

**Smart Multi-Port — Coin Switching**

- **Cross-coin job invalidation on coin switch** -- `SendJobToSession(cleanJobs=true)` invalidated ALL jobs in the shared `s.jobs` map, including jobs belonging to sessions on other coins. When one coin found a new block, other coins' session jobs were wiped from bookkeeping. Fixed by removing blanket invalidation; now stores the new job and evicts oldest when map exceeds 10 entries
- **Missing `mining.set_difficulty` on coin switch** -- `switchSessionCoin()` sent the new coin's job but never sent `mining.set_difficulty` beforehand. cgminer/bmminer firmware applies the last received `set_difficulty` to the next job — without re-sending it, miners used the previous coin's difficulty for the new coin's shares, causing rejection. `sendCoinJob()` now calls `SendDifficulty(session, currentDiff)` before sending the job when `cleanJobs=true`

**Upgrade**

- **`upgrade.sh` skips hotfixes when version tag matches** -- version comparison (`sort -V`) treated same-version or hotfix-patched releases as "already on latest" and silently skipped the upgrade. Users had to know about `--force` to apply hotfixes. `--force` is now the default behavior; `--auto` mode still blocks downgrades
- **`upgrade.sh` auto-fixes disabled payments in existing configs** -- `migrate_v2_config()` now patches `payments: enabled: false` → `enabled: true` for all coins during every upgrade. No manual config editing required

**Sentinel — RPC & Monitoring**

- **Fragile RPC credential parser** -- `_get_rpc_auth_for_port()` used substring matching (`str(port) in line`) which could match wrong port lines (e.g., port 333 matching 3333). Rewrote with exact port value parsing, indentation-aware block detection, and mtime-based caching. Verified against all 13 supported coins with zero false positives
- **Hashrate divergence false positive in Smart Port mode (root cause)** -- `get_pool_share_stats()` only queried the primary `pool_id`. When Smart Port rotated miners to the DGB pool showed 0 hashrate, triggering false divergence alerts. Previous fix (v2.3.0 initial) only patched the aggregate branch, but code was going through the per-worker branch. Root fix: `get_pool_share_stats()` now merges miners from ALL enabled coin pools at the source, fixing both code paths
- **Alert digest shows no useful information** -- "Alert Digest: 2 Alerts" with no indication of what type of alert fired. 33 of 63 alert types (including `hashrate_divergence`, `block_found`, `block_orphaned`, `coin_node_down`) were missing from the digest type map, falling through to a generic "Multiple alerts triggered" default. Added all missing types with proper emoji, titles, descriptions, and severity colors. Unknown future types now auto-format their name instead of showing "Alerts"
- **Difficulty alert threshold too sensitive** -- DGB adjusts difficulty every block. Threshold raised from 25% to 50% to reduce noise

**Payment Processor**

- **False "payment processor stalled" alert during normal block confirmation** -- Go sentinel's `checkPaymentProcessors()` only tracked the count of pending blocks. A single block confirming from 0% → 100% stays at count=1 the entire time, triggering "stalled" after 5 checks even though the block is actively progressing. Fixed by tracking the full pipeline (`confirmed + paid` blocks) — stall only fires when NOTHING moves through the pipeline

**Tests**

- **Nil map panic in `TestCheckPaymentProcessors_EscalatesToCritical`** -- test helper `testSentinel()` was missing `paymentStability` map initialization, causing panic when `checkPaymentProcessors()` accessed it. Also fixed `TestCheckPaymentProcessors_AlertOnStall` — test loop count was too low for the effective threshold (`PaymentStallChecks * checksPerInterval`)

**Daemon Resource Limits**

- **`dbcache=8192` causes swap thrashing and RPC timeouts on multi-coin setups** -- two daemons each with 8GB dbcache exceeded the 12GB memory limit, pushing into swap. RPC calls returned EOF, stalling block confirmations for 4+ hours. Reduced defaults: BTC/DGB/BCH/LTC/DOGE=4096, all other coins=2048 (minimum floor). `maxconnections` reduced from 256 to 64 across all coins
- **Auto-sizing only ran on WSL2** -- RAM-based dbcache auto-sizing (25% of total RAM, capped) was gated behind a WSL2 detection check. Now runs on all platforms
- **Existing configs not updated on upgrade** -- `upgrade.sh` now includes `rightsize_daemon_resources()` migration that detects and reduces oversized dbcache/maxconnections in existing daemon configs during every upgrade

**Daemon Stability**

### Changed

- **Version bump** -- all version strings updated to 2.3.1

---

## [2.2.4] - 2026-04-06 - Phi Hash Reactor

> *Block detection uses per-coin wallet address. False positive alert fixes.*

### Fixed

- **False positive wallet balance drop alert** -- external balance API (`chainz.cryptoid.info`) can return 0 on rate-limit or timeout, triggering a false "100% balance drop" alert showing the entire wallet as drained. Now requires 3 consecutive zero-balance readings before firing
- **Noisy difficulty spike alerts for inactive coins** -- multi-port difficulty spike alert fired for ALL monitored coins regardless of whether miners were actively mining them. Now only alerts for coins with active miners. Threshold raised from 15% to 25% to match Sentinel. Removed misleading "routing small miners to easier coins" text

### Changed

- **Version bump** -- all version strings updated to 2.2.4

---

## [2.2.3] - 2026-04-06 - Phi Hash Reactor

> *HA election race condition fix, Sentinel crash fix, upgrade.sh self-update.*

### Fixed

- **HA election race condition — node stuck as BACKUP on startup** -- VIP manager ran election before coin pools reported sync status to the VIP subsystem. All coins showed `syncPct=0` during the election window, so the node stayed as BACKUP for ~60s until the masterless-cluster detector fired. Added pre-populate step in `coordinator.go` that queries each coin daemon's `getblockchaininfo` RPC and calls `UpdateCoinSyncStatus()` before `vipManager.Start()`, so election has accurate sync data immediately
- **False positive wallet balance drop alert** -- external balance API (`chainz.cryptoid.info`) can return 0 on rate-limit or timeout, triggering a false "100% balance drop" alert showing the entire wallet as drained. Now requires 3 consecutive zero-balance readings before firing, preventing single API failures from causing false alarms

### Added

- **upgrade.sh self-update mechanism** -- `upgrade.sh` now automatically re-launches itself from the downloaded version when the script has changed, so users never need to manually `git pull` before upgrading

### Changed

- **Version bump** -- all version strings updated to 2.2.3

---

## [2.2.2] - 2026-04-06 - Phi Hash Reactor

> *Smart Port per-coin connection visibility fix.*

### Fixed

- **Smart Port workers invisible to per-coin APIs** -- miners connected via the Smart Port (port 16180) were tracked only by the `MultiServer` coordinator, not by individual `CoinPool` instances. Per-coin API endpoints (`/api/pools/{id}/connections`, `connectedMiners`) showed 0–1 connections and `"connected": false` for workers, even though shares were processed correctly. Dashboard displayed "Connections 1" instead of the actual 7+ miners. Added `MultiPortSessionProvider` interface so each `CoinPool` merges smart-port sessions assigned to its coin into `GetConnections()` and `GetActiveConnections()`, giving the API and dashboard accurate worker counts regardless of connection port
- **Sentinel crashes on startup in multi-coin mode** -- `get_enabled_coins()` called `detected.get("symbol")` on the result of `auto_detect_pool_coin()`, which returns a list (not a dict) in multi-coin V2 mode. Caused `AttributeError: 'list' object has no attribute 'get'` crash loop. Sentinel was down, preventing Discord block notifications. Added `isinstance(detected, list)` check to return the list directly
- **HA election blocked indefinitely with Smart Port** -- `isLocalNodeFullySyncedLocked()` in VIP manager required ALL coins to be synced before allowing master election. When a secondary coin was added via Smart Port, its sync status started at 0% or `nil` after stratum restart, permanently blocking election. Node stayed as BACKUP, preventing block submission for all coins including the fully-synced primary. Changed election to only require the primary coin (first in config) to be synced; secondary coins sync in background without blocking

---

## [2.2.1] - 2026-04-04 - Phi Hash Reactor

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

**Dashboard — Thread Safety**

- **`/api/miners` and `/api/combined` race condition on miner_cache** -- reader endpoints iterated `miner_cache["miners"]` dict without holding `_miner_cache_lock`, while the background poller could replace it mid-iteration. Under load (many miners, frequent polls), this causes `RuntimeError: dictionary changed size during iteration` crashing the API response. Both endpoints now snapshot the dict under the lock before iterating
- **Duplicate block celebration announcements** -- `fetch_pool_stats()` detected new blocks by count comparison (`new_count > old_count`) but did not deduplicate by block identity. If the API returned a slightly different block list between polls (reordered, or a block changed status causing a re-count), the same block could be announced multiple times. Now tracks announced blocks by `(height, hash)` tuple and skips duplicates

**Other**

- **`GetSharesPerSecond()` returns meaningless lifetime average** -- divided total lifetime accepted shares by 3600 (a fixed constant), producing the same value regardless of actual current submission rate. Now divides by actual elapsed time since pool start

**Sentinel — HA**

- **False fleet hashrate drop alert on HA backup nodes** -- pool drop detection ran on all nodes regardless of HA role, relying solely on `send_alert()` suppression. If Sentinel accidentally started on a backup node (e.g., after upgrade restart), and the stratum briefly reported `localRole: MASTER` during cluster re-discovery, the backup's 0 TH/s triggered a 100% drop alert. Added `is_master_sentinel()` guard directly to the pool drop detection block so backup nodes skip it entirely
- **HA node role int-to-string mapping** -- Go stratum serializes `ClusterNode.Role` as an integer (`RoleBackup=2`), but `ha_manager.py` only accepted string values (`"BACKUP"`). Every 30-second status poll logged `Unknown HA node role from API: 2, treating as UNKNOWN` for each node. Added `INT_ROLE_MAP` to translate Go's integer role codes to their string equivalents

**Installer**

- **HA sync skips coin install on peer** -- `sync_ha_cluster()` detected missing coins on HA peers but only printed a warning with manual instructions. On failover, the backup node couldn't serve coins it never installed. Now auto-installs missing coins on peers via `pool-mode.sh --add <coin> --yes` over SSH, with wallet address forwarded from the master's config. Added sudoers entry for the HA SSH user to run `pool-mode.sh --add`

**Config — V1/V2 Hybrid**

- **Stratum falls back to V1 mode on hybrid config** -- when `pool-mode.sh --add` appended a coin to a config that already had a `coins:` array alongside stale V1 top-level sections (`pool:`, `stratum:`, `daemon:`), it treated the config as V2 and only appended. The V1 sections were never stripped, creating a hybrid that `LoadV2()` silently failed to parse. Stratum fell back to V1, ignoring all coins except the first, with the failure logged at DEBUG level (invisible in production). Fixed `--add` to detect and strip stale V1 sections when a `coins:` array already exists, including per-coin `daemon:` → `nodes:` conversion and `pool_id` generation
- **V2 config fallback error invisible in production logs** -- `main.go` logged the V2→V1 fallback reason at DEBUG level, making it impossible to diagnose why multi-coin configs were silently ignored. Changed to WARN level so operators see exactly why V2 loading failed
- **New `pool-mode.sh --repair-config`** -- repairs existing hybrid V1/V2 config.yaml files in-place: strips stale V1 sections, converts per-coin `daemon:` → `nodes:[]`, adds missing `pool_id` fields, migrates API keys, sets `version: 2`. Creates `.pre-repair.bak` backup before modifying
- **`wait-for-node.sh` now parses V2 `nodes:` arrays** -- the AWK parser in `extract_v2_nodes()` only matched `daemon:` sections inside coin entries, causing startup hangs or silent failures on pure V2 configs using `nodes:[]`. Now parses both `daemon:` (V1 compat) and `nodes:` (V2) formats
- **`daemon:` backward-compat entries added on config write** -- `pool-mode.sh --add`, `--remove`, and `--repair-config` now inject a lightweight `daemon:` section (mirroring the first node's host/port) alongside `nodes:` in each coin entry. This ensures `wait-for-node.sh` AWK compatibility regardless of `yaml.dump()` output formatting
- **HA-safe stratum restart on coin add/remove** -- `pool-mode.sh --add` and `--remove` now automatically restart stratum with HA watcher protection. The HA watcher is paused before restart and resumed after stratum is confirmed running, preventing cascading failures where the watcher detects a brief API outage, demotes the node to BACKUP, and kills dashboard + sentinel

### Changed

- **Version bump** -- all version strings updated to 2.2.1

---

## [2.2.0] - 2026-03-31 - Phi Hash Reactor

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
- **6 coins missing `-pid=` flag** -- PEP, CAT, NMC, SYS, XMY, FBTC service templates had `PIDFile=` directives but no matching `-pid=` flag in ExecStart. Bitcoin-fork daemons create default PID filenames that don't match, causing perpetual "activating" state. Added `-pid=` to all 6

**Dashboard Display**
- **All coins show DGB network hashrate** -- multi-coin node health used a global `pool_stats_cache["node_networkhashps"]` which only cached the primary coin's value. Changed to per-coin `coin_rpc(symbol, "getnetworkhashps")` call
- **Wallet generation times out on slow chains** -- `generate-wallet` endpoint returned 503 with generic "not running or synced" error during block index loading (RPC error -28). Frontend capped at 60 attempts (5 min). Now distinguishes retryable (node loading) vs permanent (wallet disabled) errors, frontend polls up to 360 attempts (30 min), and stops immediately on permanent failures

**Sentinel**

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
- **Dashboard "Waiting" status on Smart Port panel** -- stratum API `MultiPortStats` has no `active_coin` field. Dashboard always showed "Waiting" even while mining. Now derives `active_coin` from `coin_distribution` (coin with most miners) in the GET endpoint

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

## [2.1.0] - 2026-03-30 - Phi Hash Reactor

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
- **Bitcoin II missing PIDFile** -- `bitcoiniid.service` template lacked `-pid=` flag in ExecStart and `PIDFile=` directive, preventing systemd from properly tracking the daemon process
- **BTC/XMY/BC2 `/tmp` glob vulnerability** -- `ls -d bitcoin-*/` in `/tmp` could match attacker-created directories. BTC now derives directory from known version; XMY and BC2 use `tar -tzf` to extract the actual directory name from the tarball
- **Daemon configs owned by root after upgrade** -- `cleanup_daemon_configs()` in `upgrade.sh` used `awk`/`sed` rewrite patterns that created new files owned by root. Daemon processes running as `spiraluser` could fail to read their configs. Added ownership/permission restoration
- **`admin_api_key` migration corrupts config on `/` or `\` in key** -- `fix_config_issues()` and `migrate_v2_config()` in `upgrade.sh` used `sed s///` with the API key as replacement text. Keys containing `/`, `\`, or `&` broke the sed delimiter or triggered backreference expansion, silently corrupting `config.yaml`. Added sanitization and replaced the `sed 1s` prepend with a safe heredoc+cat approach
- **UFW rule missing for multi coin smart port** -- port 16180 (multi-coin stratum) was never opened in UFW during install. External miners could not connect. Added conditional `ufw allow 16180/tcp` when `MULTIPORT_ENABLED=true`
- **`restart_service()` flapping not detected** -- successful restart reset `restart_counts` to 0, so a service that crash-looped (starts OK, dies 30s later) never reached `MAX_RESTART_ATTEMPTS`. Count now increments on every restart; the hourly reset clears it for genuinely recovered services
- **Stratum TLS port not opened in UFW (single-coin mode)** -- single-coin setup opened V1 and V2 stratum ports but never the TLS port. TLS miners were silently blocked by the firewall
- **Connlimit rules missing for 5 coins** -- iptables connection-limit rules (max 200/IP) only covered 9 of 14 coins. DGB-Scrypt, PEP, CAT, FBTC, and the multi-coin port had zero connection-exhaustion protection
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
- **FBTC DNS seeds all dead** -- all 3 Fractal Bitcoin DNS seeds (`dnsseed-mainnet.fractalbitcoin.io`, `dnsseed-mainnet.unisat.io`, `dnsseed.fractalbitcoin.io`) return no records. Added `fixedseeds=1` explicitly and 5 `addnode=` peers obtained from a live FBTC daemon's `getpeerinfo`
- **FBTC missing third DNS seed** -- `dnsseed.fractalbitcoin.io` was compiled into the binary (found via `strings`) but not in the config's seednode list. Added as third seednode entry

**Multi-Coin Scheduler**
- **`switchJob := *job` copies sync.RWMutex** -- `multiserver.go` line 369 copied a `protocol.Job` struct by value during coin switches. `Job` embeds `sync.RWMutex` at field `stateMu`; copying a mutex is undefined behavior that can cause deadlocks or data races. Detected by `go vet`. Fixed to use `job.Clone()` which properly initializes a fresh mutex

### Changed

- **Package rename `internal/difficulty` -> `internal/scheduler`** -- the "difficulty switching" concept was removed; the package contains scheduling, monitoring, and routing logic. All imports updated
- **Version bump** -- all version strings, documentation, templates, MOTD, and config files updated to 2.1.0
- **MOTD consolidated** -- reduced from 22 commands to 14, organized into Status/Monitoring, Mining/Coins, and Management sections. Added `mining multiport` command. Updated in both `install.sh` and `upgrade.sh`

---

## [2.0.1] - 2026-03-29 - Phi Hash Reactor

### Fixed

**WSL2 / Docker Bug Audit**
- **DNS peer discovery disabled on 11 coins** - `dnsseed=hostname` entries in install.sh (DGB, BTC, BC2, LTC, DOGE, PEP, CAT, NMC, SYS, XMY, FBTC) were parsed as `atoi("hostname") = 0` by Bitcoin Core's `GetBoolArg()`, overriding the earlier `dnsseed=1` and silently disabling DNS seeding. Root cause of XMY single-peer issue. Removed all `dnsseed=hostname` lines; DNS seed hostnames are hardcoded in each daemon's `chainparams.cpp` and cannot be configured via conf file
- **Docker stratum-entrypoint.sh `set -e` bypass** - `envsubst ... && mv ...` exempts the left side from `set -e`; a failed envsubst would silently continue with a corrupt config. Split into two separate commands
- **Docker patroni-entrypoint.sh password file race** - between `envsubst > patroni.yml` and `chmod 600`, the file briefly had default umask permissions (world-readable). Added `umask 077` before the write
- **Windows configure-coin-firewall.ps1 wrong-coin port matching** - `Get-CoinConfigFromManifest` regex matched against the entire manifest YAML, returning the first coin's ports regardless of the target symbol. Rewrote to split manifest into per-coin blocks before matching
- **Windows firewall scripts `.Substring()` crash** - trailing commas in `-FirewallProfiles` produced empty strings after split, crashing `.Substring(0,1)`. Added `Where-Object { $_ -ne "" }` filter in both `configure-firewall.ps1` and `configure-coin-firewall.ps1`
- **upgrade.sh `--fix-config` / `--update-services` run unconditionally** - defaults were `true` despite help text and comments saying "off by default" / "only when explicitly requested". Changed to `false`; these flags now require explicit opt-in as documented
- **upgrade.sh multi-disk backup path ignores quotes** - `resolve_coin_dir` regex `\K.+$` captured literal quotes from `CHAIN_MOUNT_POINT="/mnt/data"`, causing the `-d` check to fail and silently falling back to the wrong directory. Fixed regex to `"?\K[^"]+` matching the pattern used everywhere else
- **spiralctl.sh / coin-upgrade.sh multi-disk path ignores unquoted entries** - regex `"\K[^"]*` required a leading quote, but install.sh line 35151 writes `CHAIN_MOUNT_POINT=/mnt/data` (unquoted). On multi-disk setups, all `spiralctl` coin commands silently used wrong paths. Fixed regex to `"?\K[^"]+` (matches both forms)
- **spiralctl.sh owned by spiraluser - privilege escalation** - `spiralctl.sh` was deployed with `chown spiraluser:spiraluser` but is symlinked to `/usr/local/bin/spiralctl` and calls `sudo` internally. spiraluser could modify the script to inject arbitrary root commands. Changed to `chown root:root` in both upgrade.sh and install.sh, consistent with other sudoers-whitelisted scripts
- **upgrade.sh Python code injection via string interpolation** - `fix_config_issues()` and `migrate_v2_config()` embedded shell variables directly into Python string literals (`'$sentinel_cfg'`, `'$final_api_key'`). A path or key containing a single quote would crash the Python inline or corrupt the JSON. Changed to pass values via `sys.argv[]`
- **upgrade.sh stale lock not re-acquired** - after clearing a dead process's lock file, the script continued without holding any flock. A concurrent `upgrade.sh` (cron + manual) could race on the new inode. Now re-opens fd and re-acquires flock after cleanup
- **Dashboard XSS in upgrade/update management UI** - `result.output`, `result.error`, `result.current_version`, `result.latest_version`, and `result.packages[]` were injected into `innerHTML` without `escapeHtml()` in 6 locations. Upgrade script output or error messages containing HTML would execute in the admin's browser. Wrapped all with `escapeHtml()`
- **Dashboard raw exception strings in API responses** - three endpoints (reboot, upgrade apply, HTTPS enable) returned `str(e)` to the client, leaking internal paths and library versions. Replaced with generic error messages; real exceptions logged server-side
- **Dashboard `shutil.move` not atomic across filesystems** - `_atomic_json_save` used `shutil.move` which falls back to copy-then-delete across filesystem boundaries. Changed to `os.replace` (always atomic)
- **Sentinel webhook 5xx retry hammering** - on server errors, the retry loop immediately re-sent without backoff. The `URLError`/timeout path correctly slept `2 * (attempt + 1)` seconds but the 5xx path did not. Added matching backoff
- **Sentinel `_dashboard_url()` breaks with non-default stratum port** - `pool_api_url.replace(":4000", ":1618")` only worked when stratum was on port 4000. Custom ports (e.g., `:8080`) were left unchanged, causing all Sentinel → dashboard API calls to silently fail. Now parses URL properly and always sets port 1618
- **add-coin.py generated install script defaults to wrong user** - generated native install script set `POOL_USER=spiralpool` instead of `spiraluser`, causing permission mismatches with existing Spiral Pool data directories and wallet files
- **add-coin.py non-deterministic RPC port generation** - `hash(symbol)` is randomized per Python session (PYTHONHASHSEED since 3.3). Running add-coin twice for the same symbol produced different ports. Changed to deterministic `hashlib.md5`
- **add-coin.py port allocation can exceed 65535** - stratum port search loop had no upper bound, producing invalid ports on systems with many coins. Added bounds check
- **spiralctl external disable zeros rate-limit config** - `revertSecurityHardening` wrote zero values for `maxConnPerIP`, `maxSharesPerSec`, and `banThreshold` when originals were never saved (pre-hardening configs), disabling all rate limiting. Now falls back to safe defaults (100/100/10/30m)
- **spiralctl vip rotate-token panics on short tokens** - `oldToken[:12]` slice panic when cluster token is shorter than 12 characters (e.g., manually set via `--token`). Added length guard
- **spiralctl vip join allows priority 0** - `joinCluster` skipped the minimum-100 priority enforcement that `enableVIP` had, allowing a joining node to silently become highest-priority and win all elections. Now enforces same 100–999 range
- **spiralctl gdpr-delete PromQL regex injection** - wallet addresses containing regex metacharacters (`.`, `+`, `|`) were passed unescaped into Prometheus `delete_series` match parameter, potentially deleting metrics for unrelated miners. Now escapes with `regexp.QuoteMeta`
- **Docker init-db.sh SQL injection via shell expansion** - `<<-EOSQL` (unquoted heredoc) allowed bash to expand `${GRANT_USER}` directly into SQL GRANT statements. A username containing SQL metacharacters could inject arbitrary SQL. Changed to quoted heredoc (`<<-'EOSQL'`) with psql `-v` variable binding and `:"grant_user"` identifier quoting
- **Dashboard run.sh gunicorn CWD not set** - `gunicorn dashboard:app` requires the working directory to contain `dashboard.py` for Python module import. If invoked from any other directory (e.g., systemd without `WorkingDirectory`), gunicorn fails with `ModuleNotFoundError`. Added `cd "$SCRIPT_DIR"` before launch
- **Windows installer `.Substring(0, 2)` crash on short path input** - `$storagePath.Substring(0, 2)` throws `ArgumentOutOfRangeException` if the user enters fewer than 2 characters, killing the entire installer. Added length and format validation before the substring call
- **Windows installer Grafana password has no repeated characters** - `Get-Random -Count 24` samples without replacement, so the 24-character password can never contain a repeated character. Changed to per-character sampling with replacement
- **Dockerfile version label not bumped** - `LABEL version="2.0.0"` in docker/Dockerfile was missed during the v2.0.1 version bump
- **maintenance-mode.sh TOCTOU lock race** - noclobber-based lock had a race between reading the PID and checking if it's alive; two concurrent callers (coin-upgrade + dashboard API) could both acquire the "lock". Replaced with `flock` (matching `ha-service-control.sh` pattern)
- **maintenance-mode.sh expired file deleted without lock** - `show_status()` and `is_maintenance_active()` deleted the maintenance file without holding the lock, racing with `extend_maintenance()`. Now acquires lock before deleting expired files
- **WAL recovery uint64 underflow discards valid blocks** - `currentHeight - block.Height` wraps to ~1.8×10¹⁹ when `block.Height > currentHeight` (possible after reorg or testnet reset), causing the block to be permanently rejected as "too old". Added underflow guard
- **Payment processor data race on `consecutiveFailedCycles`** - `processCycle` wrote the counter without holding `mu`, but the health-check goroutine read it under `mu`. Go race detector would flag this. Moved writes under the existing mutex
- **Migration rows hold DB connection through entire migration loop** - `defer rows.Close()` in `runMigrations` kept the `schema_migrations` query connection open for the duration of all DDL statements. On small pools (`MaxConns=2`), this can deadlock. Now closes rows immediately after reading
- **Block insert retry sleeps on miner message-loop goroutine** - `handleBlock`'s 2-second retry sleep blocked the miner's connection goroutine, preventing reads/writes. The keepalive timer could fire during the sleep, hitting the 5-second write deadline and disconnecting the miner who just found a block. Moved retry to a background goroutine
- **coin-upgrade.sh maintenance mode silently never activates** - `enable_maintenance` passed `"coin-upgrade"` as the duration parameter (first positional arg). `maintenance-mode.sh enable` validates duration with `^[0-9]+$`, so the call always fails — silently swallowed by `|| true`. Discord alerts fire during the entire upgrade window. Fixed to pass `60 "coin-upgrade"` (duration then reason)
- **coin-upgrade.sh predictable temp directory (local privilege escalation)** - `WORK_DIR="/tmp/spiral-coin-upgrade-$$"` used a PID-based path. Between assignment and `mkdir -p`, another user could pre-create the path as a symlink. Since coin-upgrade runs as root, `tar -xzf` would extract files to the symlink target. Changed to `mktemp -d`
- **maintenance-mode.sh `show_status` dead expired-check path** - duplicate `$now -ge $end_time` checks at lines 520 and 536; the first returned early with "INACTIVE" status, making the second block ("EXPIRED (auto-clearing...)") unreachable dead code. Removed the first early-return so the informative EXPIRED message is displayed
- **ha-replicate.sh TOCTOU lock race** - PID-based `cat`/`kill -0` lock had a race window between reading the PID file and checking if the process is alive. Two concurrent `ha-replicate` runs (cron overlap) could both acquire the "lock". Replaced with `flock` (matching `blockchain-restore.sh` and `maintenance-mode.sh` patterns)
- **Windows installer DB/RPC passwords use weak PRNG** - `Get-Random` uses `System.Random` (seeded from clock), not a CSPRNG. Database and RPC passwords were predictable if an attacker knew the approximate installation time. Changed all password generation (DB, RPC, Grafana) to `System.Security.Cryptography.RandomNumberGenerator`
- **spiralpool-add-coin.bat stale `%ERRORLEVEL%` in nested blocks** - cmd.exe expands `%ERRORLEVEL%` at parse time inside parenthesized blocks, not at execution time. All nested checks (winget availability, install result, firewall, pip) saw stale values from the outer block. Changed to `!ERRORLEVEL!` (delayed expansion, already enabled)
- **spiralpool-add-coin.bat predictable temp file name** - `%RANDOM%` produces only 32768 values; combined with PID prediction, an attacker could pre-create the temp file as a junction to redirect Python output or inject false port data parsed by the firewall configuration step. Changed to triple `%RANDOM%` concatenation
- **Dashboard run.sh `grep -oP` breaks macOS** - `grep -oP` (Perl regex) is not available on macOS's BSD grep. `find_python` and `check_debian_deps` silently fail, reporting "Python 3.8+ not found" even when installed. Changed to portable `grep -oE`
- **rescan-miners.sh `--reset` silent failure on permission denied** - `rm -f` suppresses errors, so `clear_database` reported "Database cleared!" even when the file (owned by spiraluser) was not actually removed. Stale data persisted into the next scan. Now falls back to `sudo rm` and verifies deletion
- **rescan-miners.sh `wait -n` fallback breaks job throttling** - on bash < 4.3, `wait -n` is unavailable and the `|| wait` fallback waits for all jobs but only decrements the counter by 1. After the first batch, `id_jobs` goes negative and all remaining miners are launched simultaneously. Now resets counter to 0 on full wait
- **TLS stratum accept loop blocks graceful shutdown** - `tls.Listen()` returns an unexported `*tls.listener` type, so the `listener.(*net.TCPListener)` type assertion always fails for TLS connections. `SetDeadline` was never called, causing `Accept()` to block indefinitely. The TLS accept goroutine could not exit during shutdown until `listener.Close()` was called. Now creates the TCP listener first, stores it, and wraps with `tls.NewListener`
- **Connection classifier regex false positives on `.00` worker names** - `\.0{2,}\d*$` matched any string ending in `.00` (two zeros, no trailing digit), misclassifying legitimate worker names like `farm.v2.009`. Changed `\d*` to `\d+` to require at least one trailing digit
- **`globalDeviceHints` data race between production and test goroutines** - package-level `globalDeviceHints` pointer was read/written without synchronization. Production goroutines calling `GetGlobalDeviceHints()` could race with `SetGlobalDeviceHints()`. Added `sync.RWMutex` protection
- **spiralctl config backup silently overwritten on consecutive saves** - `backupFile` always wrote to `config.yaml.backup`, destroying the previous backup. Two config changes in succession meant the original good config was lost. Added timestamp to backup filename (`config.yaml.20260329-120000.backup`)
- **`GetRouterProfiles` API returns unscaled default difficulty profiles** - always read from `DefaultProfiles` (base SHA-256d/600s), ignoring block-time scaling and algorithm selection (Scrypt). The API reported incorrect difficulty values for Scrypt coins, Fractal Bitcoin, or any chain with non-600s block times. Now reads from the router's active scaled profiles via `GetAllProfiles()`
- **Windows installer WSL2 portproxy exposes RPC and DB on 0.0.0.0** - daemon RPC and PostgreSQL (also wrong port 5432 vs docker-compose's 5433) were forwarded on `0.0.0.0`, exposing them to the LAN. RPC and DB should never be LAN-accessible. Split into public ports (`0.0.0.0` — stratum, P2P, dashboard, API, metrics) and internal ports (`127.0.0.1` — RPC, PostgreSQL). Fixed PostgreSQL to port 5433
- **Windows installer WSL2 scheduled task uses `-AtStartup`** - WSL2 is not available before user login (Store-installed `wsl.exe` requires a user session). The port forwarding task silently failed on every boot. Changed to `-AtLogOn`
- **pool-mode.sh hardcoded "spiralpool-ha" username** - `chown` and sudoers entries referenced "spiralpool-ha" but the `$HA_SSH_USER` variable defaults to "spiralha". The key exchange handler, `.ssh` directory ownership, and sudo permissions all targeted a nonexistent user. Changed all references to use `$HA_SSH_USER`
- **Windows installer WSL2 portproxy missing Stratum V2 port** - `CoinConfig` hashtable lacked `V2Port`, so portproxy fallback rules and port conflict checks only forwarded V1 and TLS. Miners using Stratum V2 (Noise protocol) could not connect through WSL2 NAT. Added `V2Port` to all 14 coin entries, the portproxy public ports array, and the port availability check
- **Windows installer port conflict check tests wrong PostgreSQL port** - checked port 5432 but docker-compose maps PostgreSQL as `127.0.0.1:5433:5432` (host port is 5433). Real conflicts on 5433 were missed; false positives on 5432. Changed to 5433
- **configure-coin-firewall.ps1 `$MyInvocation.ScriptName` empty inside function** - `$MyInvocation.ScriptName` is unreliable inside functions in some PowerShell contexts, returning empty. The manifest path computation failed and the script exited with "manifest not found" even when it existed. Changed to `$PSScriptRoot` with fallback (also fixed in `configure-firewall.ps1`)

### Changed
- All version strings, documentation, templates, and config files bumped to 2.0.1

---

## [2.0.0] - 2026-03-27 - Phi Hash Reactor

> *System upgrade complete. All nodes nominal.*

This is a major release. All changes are backward-compatible: no database migrations, no config format changes, no reinstall required.

**Dashboard overhaul** - Spiral Dash has been rebuilt with a three-tab layout (Overview, Blocks, Management), interactive Chart.js analytics, fleet group views with per-group stats, per-firmware miner controls (AxeOS, Avalon, Vnish, ePIC, LuxOS), Avalon power scheduling, HTTPS/TLS with self-signed certificates, a full Management section (service control, log viewer, system updates, system reboot, resource monitoring), 23 built-in themes with custom theme editor, and CSV/JSON data export.

### Added

**Sentinel - Security & Firmware Monitoring**
- **Stratum URL mismatch detection** - Sentinel compares each miner's reported pool URL against the expected stratum host:port. Alerts on first detection (6h cooldown) if a miner has been pointed at a different pool - catches firmware hijacking and misconfiguration
- **BraiinsOS/Vnish auto-scan** - when the CGMiner probe fails on port 4028, Sentinel falls back to HTTP on port 80 and probes BraiinsOS (`GET /api/v1/auth/login`) and Vnish (`POST /api/v1/unlock`) with default credentials. Successful detection auto-classifies the device; failed detection logs "requires manual credential setup"
- **Wallet mismatch warning** - at startup, Sentinel validates each coin's configured pool wallet against the node's `validateaddress`/`getaddressinfo` RPC. Mismatches trigger a Discord/notification alert and a red warning banner on the dashboard
- **Generic webhook notifications** - new notification channel: raw HTTP POST to any URL with a JSON payload (`event`, `title`, `description`, `fields`, `timestamp`). Supports custom headers. Enables Zapier, Home Assistant, IFTTT, PagerDuty, n8n, and custom scripts. Configured alongside existing channels in install.sh setup menu
- **Fleet group offline/online alerting** - when all miners in a user-defined worker group go offline past the threshold, Sentinel fires a `group_offline` alert naming the group and listing affected miners (max 8 in embed). When the group recovers, a `group_online` alert fires showing online/total count. Individual miner alerts are suppressed for group members to avoid duplicates. A 2-minute grace window allows staggered outages (e.g., power propagating across a switch) to coalesce into a single group alert. Groups loaded from `miner_groups.json` with 60-second cache
- **Group-aware temperature alerting** - when 2+ miners in a user-defined group hit temp warning or critical thresholds in the same monitoring cycle, Sentinel sends a single group thermal alert ("check HVAC at this location") instead of individual alerts per miner. Thermal shutdowns remain individual (safety-critical, never suppressed). Falls back to individual alerts when no groups are defined or only 1 miner in a group is affected
- **Group-aware degradation alerting** - when 2+ miners in a user-defined group show hashrate degradation simultaneously, Sentinel sends a single group degradation alert ("check power/cooling at this location") with per-miner baselines and drop percentages. Individual miners degrading alone still get individual alerts
- **HTTPS auto-detection** - Sentinel auto-detects whether the dashboard is running HTTPS by reading the spiraldash service file. Dashboard API calls use the correct protocol without manual configuration. Self-signed certs on localhost are accepted automatically

**Dashboard - Hashrate, Analytics & Export**
- **Interactive hashrate charts** - Chart.js powered graphs for per-coin and aggregate hashrate with 15M/1H/6H/24H/7D/30D time range selector. Data sourced from Sentinel's existing hashrate history
- **Block odds / luck tracking** - live display of network hashrate share %, estimated time to block (ETB), projected blocks per day/month, and a luck ratio (expected vs actual block interval). Also surfaced in Sentinel intel reports
- **Fleet power consumption & efficiency** - aggregate per-miner watts into fleet-wide total (kW), W/TH efficiency metric, and optional electricity cost estimate (configured via `power_cost.rate_per_kwh` in `config.json`, hidden if not set)
- **Earnings calculator** - earnings section showing block reward value, coin price, and monthly earnings estimate using existing ETB math
- **CSV/JSON export endpoints** - three download endpoints: `/api/export/blocks`, `/api/export/earnings`, `/api/export/hashrate`. Streams from PostgreSQL, available in CSV and JSON formats. Requires dashboard auth

**Dashboard - Miner Detail & Monitoring**
- **Per-hashboard temperature stats** - Antminer S19/S21, Whatsminer, and CGMiner devices reporting chain data now expose per-board chip and PCB temperature arrays in the miner detail view (not just the single highest temp)
- **Device type breakdown chart** - pie/donut chart of miner types in the fleet (Antminer, Bitaxe, Avalon, etc.) using Chart.js
- **Block finder history** - every block found by the pool is attributed to the specific miner and worker that submitted the winning share. Records: block hash, block height, worker name, miner IP, device type, and timestamp. Persisted to `block_history.json` (last 100 blocks). Shown on the dashboard as `Last Block Found By` in the pool stats panel

**Dashboard - Miner Control (Manual)**

Device Configuration modal in the Miner Management tab - per-firmware controls for all supported device families:

- **AxeOS devices** (Bitaxe, NerdQAxe, Hammer, LuckyMiner, JingleMiner, Zyber) - fan speed %, frequency (MHz), and voltage (mV) via `POST /api/system`
- **Avalon/Canaan devices** - three power modes (Efficiency / Balanced / High) via CGMiner `ascset|0,workmode` + `ascset|0,freq` + `ascset|0,voltage`. Model-aware profiles for every generation: Nano 3/3S, Q series, A1066/A1166/A1246/A1346/A1366/A1466/A1566, Avalon 7/8. Fan speed via `ascset|0,fan,MIN-MAX`
- **Vnish firmware** (Antminer with Vnish aftermarket firmware) - fan speed and manual overclock (frequency, voltage) via REST `/api/v1/settings`. Autotune preset enumeration via `/api/v1/autotune/presets`
- **ePIC BlockMiner** - fan speed %, overclock (frequency MHz, voltage mV), and reboot via HTTP REST on port 4028
- **LuxOS firmware** (Braiins LuxOS on Antminer) - fan speed, frequency, named profile switching (list and apply profiles), and restart via LuxOS session protocol

**Dashboard - Avalon Power Schedules**
- **Time-based power profile scheduling** - configure automatic Efficiency/Balanced/High mode switches by time of day for any Avalon device. Overnight low-power mode, peak-hours performance mode. Rules support overnight ranges (e.g. 21:00–09:00). Persisted to `avalon_schedules.json`
- API: `GET/PUT/DELETE /api/avalon/schedules/<ip>`, `POST /api/avalon/schedules/<ip>/apply`, `GET /api/avalon/profiles`

**Dashboard - Worker Groups & Tags**
- **Worker groups** - miners can be organized into named groups via `miners.json` or `spiralctl miner group set <IP> <group>`. Dashboard shows aggregate stats per group
- **Worker tags** - optional freeform tags on miners (e.g. `asic,garage,s21`). Manageable via `spiralctl miner tag set/list/clear` and dashboard API

**Dashboard - Fleet Group View**
- **Fleet group view mode** - three-way miner grid toggle: flat → grouped (by hardware type) → fleet (by user-defined worker groups). Fleet view organizes miner cards under group headers with per-group hashrate, power, and online/total counts. Ungrouped miners shown in a separate section
- **Fleet group summary bar** - chip-style summary strip above the miner grid showing each group's name, aggregated hashrate, power draw, and online miner count. Groups with all miners offline are highlighted in red
- **Fleet group API aggregation** - `/api/pool/stats` response now includes `fleet_groups` array with per-group totals (hashrate_ths, power_watts, online_count, total_count) resolved from `miner_groups.json`

**Dashboard - Block Analytics Tab**
- **Dedicated Blocks tab** - new top-level tab with block analytics: pool hashrate share %, expected blocks, luck ratio, and a dual bar chart (actual vs expected blocks found). Auto-refreshes every 60 seconds when active
- **Luck history API** - `/api/luck` now returns full history (up to 720 hourly samples) and pool/network hashrate, enabling the Blocks tab charts

**Dashboard - Charts & Statistics**
- **Blocks Found bar chart** - bar chart in the Statistics grid showing block discovery history per coin
- **Shares Rate line chart** - real-time line chart showing accepted share rate over time
- **Chart theme integration** - chart colors (grid lines, labels, datasets) are wired to the theme system via CSS variables. All built-in theme JSONs updated with chart color definitions. Custom theme editor includes chart color pickers. Block analytics colors (actual, expected, pool share) added to theme editor

**Dashboard - Log Viewer Live Mode**
- **Live auto-refresh** - log viewer in the Management tab now has a Live button that enables 2-second auto-refresh polling of `journalctl` output. Green indicator when active. Toggleable on/off without losing scroll position

**Dashboard - HTTPS / TLS**
- **Self-signed TLS certificate** - dashboard serves over HTTPS by default using gunicorn's native `--certfile` / `--keyfile` flags. Self-signed ECDSA P-256 certificate generated during installation with 10-year validity and SANs for hostname, all detected LAN IPs, localhost, and 127.0.0.1
- **HTTP insecure connection warning banner** - context-aware: if HTTPS is enabled, warns and links to the HTTPS URL. If cert exists but HTTPS not yet enabled, nudges user to the Management tab. If neither, banner is hidden (nothing actionable). Dismissable per session
- **Secure cookie auto-detection** - `SESSION_COOKIE_SECURE` now auto-detects from the spiraldash service file instead of defaulting to false. Ensures cookies are marked secure when HTTPS is active without requiring a manual env var

**Dashboard - Management Section** *(new tab)*
- **Service control panel** - start/stop/restart spiralstratum, spiralsentinel, spiraldash, and coin daemons from the dashboard. Shows service status and uptime
- **System resources panel** - real-time CPU load average (1/5/15 min), RAM usage (total/used/available/%), disk usage per mount (/, /spiralpool, /var), and system uptime. Sourced from `/proc` - no psutil dependency
- **Log viewer** - streams `journalctl` output for any pool service with color-coded severity levels, auto-scroll, pause button, and live auto-refresh mode
- **System updates** - lists available apt packages with last-checked timestamp. One-click refresh (`apt-get update`) and apply (`apt-get dist-upgrade`) with confirmation. Runs via `apt-noninteractive.sh` wrapper that uses `systemd-run --pipe` to escape the dashboard's `ProtectSystem=strict` mount namespace
- **System reboot button** - one-click graceful reboot from the Management tab. Uses `systemctl --no-block reboot` so the dashboard can send its response before systemd begins the shutdown sequence. Confirmation dialog required
- **System info API endpoint** - `GET /api/system/info` provides programmatic access to all host metrics (CPU, memory, disk, service statuses)

**Installer & Upgrade**
- **Pruned node support** - install.sh offers a pruning option during coin setup ("Full node or Pruned"). Sets `prune=5000` (5GB) in daemon conf. All pool operations work on pruned nodes (getblocktemplate, submitblock, ZMQ). Savings: BTC 600GB→5GB, DGB 60GB→5GB, BCH 200GB→5GB - critical for WSL2 and small-disk deployments
- **`spiralctl coin prune <TICKER>`** - enable blockchain pruning on an existing coin node without reinstalling
- **Pruned node badge** - dashboard indicator next to node status when the backing coin daemon is running in pruned mode
- **Notification channel menu** - unified selection menu in install.sh for Discord, Telegram, XMPP, ntfy, Email, and Webhooks
- **Dashboard TLS certificate generation** - install.sh generates a self-signed ECDSA P-256 certificate with SANs (hostname, LAN IPs, localhost) during installation. Certificate stored in `$INSTALL_DIR/certs/`

**Upgrade - v2.0 Migration**
- **Automatic config migration** - upgrade.sh now always runs `migrate_v2_config()` which handles: metrics section creation, api section creation, `admin_api_key` v1→v2 field migration, and sentinel `config.json` sync. Each migration is idempotent (grep before modify)
- **Service files and config fixes always enabled** - upgrade.sh now regenerates systemd service files and runs config fixes on every upgrade by default (previously required `--full` flag). All migrations are idempotent and preserve HTTPS, dependencies, and custom settings
- **Major version auto-detection** - upgrade.sh detects major version jumps (e.g. 1.x → 2.x) and logs the change. Ensures critical service file updates are never skipped on major upgrades
- **Docker deployment guard** - upgrade.sh detects if it's running inside a Docker container or if Docker containers are the active deployment, and blocks/warns with correct Docker upgrade instructions instead of corrupting the install
- **WSL2 pre-flight checks** - upgrade.sh warns about clock drift, memory pressure, and missing systemd on WSL2 before proceeding
- **Sudoers migration for existing installs** - upgrade.sh detects missing sudoers entries (journalctl, apt wrapper, upgrade.sh, psql, enable-https, system reboot) in existing `/etc/sudoers.d/spiralpool-dashboard` and appends them individually with `visudo -c` validation
- **HTTPS migration (opt-in)** - upgrade.sh pre-generates a self-signed ECDSA P-256 TLS certificate and deploys the `enable-https.sh` script. Existing HTTP-only installs stay on HTTP — operators enable HTTPS when ready from the Dashboard Management tab. This avoids broken bookmarks and unexpected cert warnings on existing installs

**Windows / WSL2**
- **WSL2 graceful shutdown hook** (`scripts/windows/wsl2-shutdown-hook.ps1`) - Windows Task Scheduler task that gracefully stops all Spiral Pool services and coin daemons before Windows shuts down, restarts, or enters sleep. Without this, Windows kills WSL2 mid-write and corrupts LevelDB blocks/chainstate, requiring a full blockchain resync. Stop order: sentinel/dash/health → stratum → coin daemons → wait for sync. Triggers on Event 1074 (shutdown/restart) and Event 42 (sleep). Logs to `%APPDATA%\SpiralPool\shutdown-hook.log`. Install/uninstall via `-Uninstall` flag. Wired into `wsl2-stratum-proxy.ps1` as a recommended setup prompt

**Docker**
- **Webhook environment variables** - `WEBHOOK_URL` and `WEBHOOK_HEADERS` env vars passed through to Docker containers for generic webhook notification support
- **TLS certificate generation in entrypoint** - Docker entrypoint generates a self-signed ECDSA P-256 TLS certificate for the dashboard, matching the native install behavior
- **Health check tuning** - PostgreSQL health check `start_period` increased to 120s to accommodate WAL recovery after crashes
- **Entrypoint error handling** - multi-coin config generation now validates the write succeeded and cleans up temp files on failure instead of silently continuing with a partial config

**Documentation**
- **Miner API limitations reference** - `docs/reference/MINER_SUPPORT.md` expanded with confirmed API limitations for four device families: iPollo (CGMiner API disabled by default - requires `--api-listen` flag), Innosilicon (CGMiner disabled by default on most models), Elphapex DG series (LuCI CGI primary, CGMiner on port 4028 unconfirmed), and ESP32 miners (no device API - online/offline and hashrate tracked via stratum connections only; temperature and fan alerts unavailable)

**Stratum V2 API - Full Endpoint Parity**
- **Worker/miner stats endpoints** - V2 multi-coin API now serves all worker and miner endpoints that V1 provides: `GET /api/pools/{id}/miners`, `/miners/{addr}`, `/miners/{addr}/workers`, `/miners/{addr}/workers/{w}`, `/miners/{addr}/workers/{w}/history`, `/hashrate/history`, and `/workers` (admin). All queries are pool-scoped via `WithPoolID()` for multi-coin isolation
- **Runtime provider endpoints** - V2 now serves `/workers-by-class`, `/router/profiles`, `/pipeline/stats`, and `/payments/stats` per pool, sourced from live CoinPool state (Spiral Router, share pipeline, block stats). Dashboard features that depended on these endpoints no longer 404 on V2
- **Admin endpoints** - V2 now serves `/api/admin/stats` (aggregated across all pools with per-pool breakdown and totals), `/api/admin/kick` (disconnects miner by IP across all pools), and `/api/coins` (registered coin registry for Sentinel/Dashboard validation)
- **Security headers middleware** - V2 API responses now include `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and `Cache-Control: no-store` (matching V1 parity)
- **Dynamic payout scheme** - V2 `/api/pools` response now reads `PayoutScheme` and `MinimumPayment` from the per-coin config instead of hardcoding `"SOLO"` / `1.0`

**Codename Theme**
- **V2.0 - Phi Hash Reactor** theme added to the dashboard theme selector - reactor-core black with critical red accents, scan lines, and a reactor pulse animation on block found

### Fixed

**HTTPS / TLS - Critical Detection & Deployment**
- **HTTPS detection matches template comment instead of ExecStart** - all 13 `grep -q "\-\-certfile"` checks across 6 files (install.sh, upgrade.sh, enable-https.sh, spiraldash.service, dashboard.py, SpiralSentinel.py, spiralctl.sh) matched the template comment `(runs enable-https.sh to add --certfile/--keyfile)` instead of the actual ExecStart line. HTTPS was silently detected as enabled even on HTTP-only installs, or silently lost during upgrades when the template was rewritten. Fixed all 13 locations to use `^ExecStart` line anchoring (`grep -q "^ExecStart.*\-\-certfile"` in bash, `line.strip().startswith("ExecStart")` in Python)
- **`set -e` kills installer/upgrader on openssl failure** - `openssl req -x509` ran as a bare command under `set -e`. If certificate generation failed (missing openssl, permission denied, disk full), the entire install/upgrade script aborted immediately. The `if [[ $? -eq 0 ]]` fallback was dead code - it never ran because `set -e` exited first. Wrapped openssl as the `if` condition directly
- **Certificate directory permission denied on fresh install** - install.sh runs as a non-root user with sudo. `sudo mkdir` created `$INSTALL_DIR/certs/` as root-owned, but `openssl` ran without sudo and couldn't write the certificate. Added `sudo chown` on the directory and `sudo` on the openssl command
- **`sed -i` fails with "Read-only file system" when enabling HTTPS** - `enable-https.sh` runs via sudo from the dashboard, which inherits the gunicorn process's mount namespace where `ProtectSystem=strict` makes `/etc` read-only. Even as root, `sed -i` can't create its temp file. Added `/etc/systemd/system` to `ReadWritePaths` in the spiraldash service - file permissions (root:root 644) still prevent the unprivileged dashboard process from writing directly
- **`apt-get update` fails with "Read-only file system" from dashboard** - same `ProtectSystem=strict` issue. `apt-get` needs write access to `/var/lib/apt`, `/var/cache/apt`, and `/var/log/apt`. Added these paths to `ReadWritePaths`
- **`sudo` fails with "unable to change to root gid" from dashboard** - `CapabilityBoundingSet=` (empty) in the spiraldash service template blocked all capability acquisition. Even with `NoNewPrivileges=no`, sudo couldn't get `CAP_SETGID`/`CAP_SETUID`/`CAP_AUDIT_WRITE`. Set to `CAP_SETUID CAP_SETGID CAP_DAC_OVERRIDE CAP_AUDIT_WRITE CAP_FOWNER` - only the capabilities sudo needs
- **Dashboard XSS via innerHTML injection** - 3 locations in dashboard.html injected unsanitized server responses into innerHTML: cert expiry date, HTTPS enable error message, and JS exception message. Added `escapeHtml()` to all 3 locations
- **MOTD shows hardcoded port 1618** - the MOTD banner displayed `Dashboard: https://IP:1618` regardless of the actual configured port. Replaced with dynamic detection from the spiraldash service file (`grep -oP '0\.0\.0\.0:\K[0-9]+'`)
- **Post-upgrade summary shows wrong protocol** - upgrade completion banner always showed `http://` regardless of HTTPS status. Added runtime detection from the service file's ExecStart line
- **HA-backup completion banner shows wrong protocol** - same issue in install.sh's HA-backup completion message. Added the same runtime protocol detection
- **`spiralctl` hardcoded protocol and port** - `spiralctl.sh` dashboard URLs used hardcoded `http://` and port 1618 in 2 locations. Added dynamic port and protocol detection
- **`enable-https.sh` missing from sudoers on upgrade** - the sudoers fresh-create heredoc in upgrade.sh was missing the `enable-https.sh` NOPASSWD entry, so the dashboard's "Enable HTTPS" button would fail with a password prompt on upgraded (non-fresh) installs
- **Dashboard upgrade.sh path wrong** - dashboard.py called `sudo /spiralpool/scripts/upgrade.sh` but the script is deployed to `/spiralpool/upgrade.sh` (install root, not scripts/). Sudoers allows `/spiralpool/upgrade.sh *` so the mismatched path triggered password prompts. Fixed to use `$SPIRALPOOL_INSTALL_DIR/upgrade.sh`

**Sentinel - Group Alert Ordering**
- **Group offline alert fires after individual alerts (duplicate notifications)** - when all miners in a group went offline, the individual miner offline loop ran first and sent per-miner alerts. The group check ran after and sent the group alert - resulting in N+1 alerts instead of 1. Restructured: pre-compute which miners will be covered by group alerts before the individual loop, suppress individual alerts for those miners
- **Staggered outage produces both individual and group alerts** - in a real power outage, miners go offline seconds apart across polling cycles. The first miner to cross the 10-minute threshold would get an individual alert before its siblings caught up. Added a 2-minute grace window: miners in multi-member groups defer their individual alerts to allow siblings to coalesce into a single group alert
- **Individual miner_online alerts fire for group-covered miners** - when a group offline alert was active and miners recovered one by one, each got an individual `miner_online` alert even though the group recovery path should handle it. Added group membership check to the recovery loop - miners in active group alerts are suppressed from individual online alerts

**Stratum V2 API - Bugs & Missing Features**
- **V2 API returns 404 on 13 endpoints the dashboard depends on** - V2 multi-coin API only implemented 9 of 26 V1 endpoints. Dashboard worker stats, miner stats, hashrate history, workers-by-class, router profiles, pipeline stats, and payment stats all silently failed with 404 when pointed at a V2 stratum server. Added all missing endpoints scoped to per-pool database tables
- **V2 `/api/pools` hardcoded payout scheme `SOLO` / minimum payment `1.0`** - the `/api/pools` response returned hardcoded `"SOLO"` and `1.0` regardless of the per-coin payment configuration in the V2 config file. Dashboard displayed wrong payment info. Fixed to read from `coin.Payments.Scheme` and `coin.Payments.MinimumPayment`
- **V2 API missing security headers** - V1 API applies `X-Content-Type-Options`, `X-Frame-Options`, and `Cache-Control` via `securityHeadersMiddleware`. V2 was missing this middleware entirely - responses had no security headers. Added to the V2 middleware chain
- **V2 API missing `/api/admin/kick` endpoint** - the kick worker endpoint existed in V1 but was never ported to V2. Dashboard's "Kick" button would fail. V2 implementation kicks across all registered coin pools
- **V2 API missing `/api/admin/stats` endpoint** - admin stats endpoint existed in V1 but was never ported to V2. V2 implementation aggregates stats across all pools with per-pool breakdown
- **V2 API missing `/api/coins` endpoint** - coin registry endpoint existed in V1 but was never ported to V2. Sentinel and Dashboard coin validation calls would 404
- **V2 `GetPaymentStats` nil dereference on CoinbaseMaturity** - `cp.coin.CoinbaseMaturity()` would panic if the coin interface was nil (possible during early startup or test harness). Added nil guard with a safe default of 100 confirmations
- **V2 `GetPaymentStats` silent failure on DB type assertion** - if the database provider was not a `*PostgresDB` (e.g., mock or test DB), the type assertion silently failed and returned empty stats with no indication of why. Added debug-level log so operators can diagnose missing payment data

**Connection Classifier Tests**
- **Proxy classification tests updated for raised threshold** - `TestClassifier_Level2_InstantAuthorize` and `TestClassifier_Level2_FastAuthorize` expected PROXY classification from timing signals alone, but the proxy confidence threshold was raised from 0.15 to 0.40 in v1.2.0. Tests now correctly expect UNKNOWN for timing-only signals and a new test validates that timing + proxy worker name pattern (combined score >= 0.40) still classifies as PROXY

**Installer - UFW & Sync View**
- **UFW crashes with `UnicodeEncodeError` on fresh install** - UFW rule comments in install.sh contained Unicode em-dashes (U+2014) which caused `bytes(out, 'ascii')` in UFW's Python backend to crash. Replaced em-dashes with regular dashes in connlimit/metrics rule comments and added a defensive `sed` cleanup before the first `ufw` command to sanitize any existing rules
- **Sync live view - progress bar two rows below controls bar** - cursor positioning was 2 rows too low, leaving a double blank gap between the controls bar and the progress bar. Adjusted all `\033[row;1H` positions and placeholder echo lines to place progress directly below the controls border

**Dashboard - System Updates & Reboot**
- **`apt-get dist-upgrade` fails from dashboard with read-only filesystem** - `ProtectSystem=strict` in spiraldash.service creates a kernel mount namespace that makes the entire filesystem read-only for all child processes, including those escalated via sudo. `apt-get` couldn't write to `/var/lib/dpkg/lock`. Fixed with a wrapper script (`apt-noninteractive.sh`) that uses `systemd-run --pipe` to make systemd (PID 1) start apt-get in the root namespace, completely outside the dashboard's restrictions
- **`apt-get update` refresh also fails from dashboard** - the Refresh button called bare `sudo apt-get update` which hit the same `ProtectSystem=strict` and sudoers issues as dist-upgrade. Switched to use the same `apt-noninteractive.sh` wrapper
- **`apt-get upgrade` doesn't upgrade kernel packages** - `apt-get upgrade` refuses to install packages that require new dependencies (like `linux-generic` meta-packages). Changed to `apt-get dist-upgrade` which handles dependency changes. Added `--force-confold` and `--force-confdef` dpkg options to suppress config file prompts
- **`apt-get dist-upgrade` shows `debconf: unable to initialize frontend: Dialog`** - `DEBIAN_FRONTEND=noninteractive` set via subprocess `env=` parameter was stripped by sudo's `env_reset`. Solved by the `apt-noninteractive.sh` wrapper which sets the env var inside the root process before exec'ing apt-get

**Dashboard - Gunicorn Worker Deadlock on Reboot**
- **Dashboard unresponsive after reboot (gunicorn worker deadlock)** - after a system reboot, the gunicorn master process started and bound to port 1618, but the forked worker process deadlocked before reaching `init_process()` (post-fork inherited threading lock). Gunicorn's `--timeout 120` failed to detect the stuck worker, and systemd saw the master as "active" - the dashboard was unreachable indefinitely until manually killed. Added an `ExecStartPost` health check to the spiraldash systemd service: polls `curl http://127.0.0.1:<port>/` every 2 seconds for up to 60 seconds; if no response, sends `SIGKILL` to the main process so `Restart=always` recovers it automatically
- **`spiralpool-sync` does not start dashboard after blockchain sync** - when `spiralpool-sync` detected a fully synced blockchain, it started `spiralstratum` but never started `spiraldash` or `spiralsentinel`. If these services were stopped (e.g., failed on earlier boot), the user had no dashboard after sync completed. Added startup for both services (if enabled) after stratum comes online, in both single-coin and multi-coin sync paths

**Sentinel - RPC Allowlist**
- **`getnetworkhashps` rejected by RPC allowlist** - Sentinel's `_RPC_ALLOWED_METHODS` frozenset was missing `getnetworkhashps`, causing a warning on every call. Added to the allowlist

**Stratum V2 - Nil Guards**
- **V2 `KickWorkerByIP` panics if stratum server is nil** - all other new CoinPool methods had nil guards for `cp.stratumServer` but `KickWorkerByIP` was missed. Added nil guard returning 0

**Tests**
- **`TestBlockQueue_ConcurrentEnqueueDequeue` flaky** - under contention, `DequeueWithCommit()` returns nil while items are in-flight between goroutines. Dequeue goroutines exited on first nil, losing items and failing the count assertion. Added a `nilStreak` retry counter (100 consecutive nils before exit) with `runtime.Gosched()` to yield to enqueue goroutines

**Sentinel - Temperature Monitoring**
- **Goldshell `all_temps` list crashes temperature alerting** - Goldshell miners return temperature as a list instead of a scalar value. The temperature comparison (`temp_value >= threshold`) threw a TypeError on lists. Added type guard to skip non-scalar temperature values

**General**
- **`spiralctl` HTTPS auto-detection** - `spiralctl` dashboard commands now auto-detect HTTP vs HTTPS from the spiraldash service file and accept self-signed certificates on localhost, matching the dashboard and Sentinel behavior
- **`spiralctl status` timer next-run shows garbage** - bash expands `$(( {} / 1000000 ))` before `xargs` substitutes `{}`, producing `$(( / 1000000 ))` syntax errors and a blank or wrong next-run time in `spiralctl status`. Rewrote `_timer_next_run()` to capture `usec` into a variable first, then compute the date expression directly
- **Miner status boolean vs string mismatch** - dashboard miner cards checked `m.online` (boolean) but some code paths returned `m.status` (string). Unified to consistent boolean check
- **Fan RPM misidentified as percentage** - fan values >100 are RPM readings, not percentages. Dashboard now detects and displays RPM values correctly with appropriate units
- **Disk usage double-counting** - multiple mount points on the same filesystem (same device) caused disk usage to be counted multiple times. Added filesystem fingerprinting to deduplicate
- **Log viewer inconsistent text color** - info-level and debug-level log lines used different shades, making the log viewer visually noisy. Unified to the same muted color (errors still red, warnings still orange)
- **Custom theme editor layout** - section headers and grid layout cleaned up for better usability in the theme customization panel

**Coin Daemon Config Audit**
- **Removed invalid/unsupported config options across all coins** - full audit of all 14 coin daemon configs (install.sh, docker templates, pool-mode.sh, tests) removed options that are not valid config-file parameters or are daemon-specific copy-paste errors: `maxoutconnections` (BCHN doesn't support it), `maxconnections` (unnecessary in docker), `maxdebugfilesize` (DGB-only, was on 4 non-DGB coins), `nblocks` (not a valid config option), `blockstallingtimeout` (not a valid config option), `checkpoints=1` (not a valid config option), `maxblocksinprogress` (DGB-specific), `maxorphantx` (DGB-specific), `blockreconstructionextratxn` (DGB-specific), `deprecatedrpc=` (empty value, DGB-specific), `debug=zmq` (unnecessary verbose ZMQ logging on DOGE/PEP/CAT/NMC), `forcednsseed=1` (aggressive, replaced by existing `dnsseed=1`)
- **WSL2 tee permission denied** - `mktemp` without sudo creates a temp file that `sudo tee` cannot write to on some WSL2 setups. Changed to `sudo mktemp` and `sudo rm -f` in the sshd hardening block

### Removed
- `CalculateBlockReward()` - processor.go (7 test-only callers, zero production use)
- `Dequeue()` - circuitbreaker.go (12 test-only callers, zero production use)
- `BuildTLSConfig()` - replication.go (4 test-only callers, zero production use)
- `fetch_block_reward()` no-arg wrapper - SpiralSentinel.py (0 callers)
- `Authorized`/`Subscribed` exported struct fields - protocol.go: converted to private atomic `authorized`/`subscribed uint32` fields with `SetAuthorized`/`IsAuthorized` accessors, eliminating data races on concurrent session access
- **Lifetime Statistics section** - removed from dashboard Overview; uptime moved to top stats row, remaining metrics were redundant with the Statistics charts

### Changed
- All version strings, documentation, themes, and config files bumped to 2.0.0
- Codename comments updated from `V1.1.0-PHI_FORGE` → `V2.0.0-PHI_HASH_REACTOR`
- `CoinbaseMessage` updated from `SpiralPool/v1.2.0/` → `SpiralPool/v2.0.0/`
- `spiralctl config validate` - alert config range check description updated to v2.0.0
- All dashboard theme and template JSON files bumped to version 2.0.0
- `apply_profile_now()` endpoint now accepts `model` in the request body (request body > saved schedule > generic), enabling the dashboard UI to pass the correct Avalon model without requiring a schedule to exist first
- Dashboard restructured into three tabs: **Overview** (pool monitoring, miner grid, stats), **Blocks** (block analytics, luck tracking, charts), and **Management** (service control, log viewer, system updates, miner management)
- **Miner card buttons consolidated** - duplicate "Configure" buttons on Overview miner cards replaced with a single "Web UI" button
- **Uptime moved to top stats row** - system uptime relocated from the removed Lifetime Statistics section to the main stats bar for better visibility

### Notes
- **Zero breaking changes** - v1.0.0 / v1.1.x / v1.2.x installations upgrade in-place via `upgrade.sh` with no config changes, no migrations, and no coin daemon restarts required. Dashboard stays on HTTP; operators can opt in to HTTPS from the Management tab (self-signed cert; browser will show a one-time certificate warning)

---

## [1.2.3] - 2026-03-27

### Fixed

**Installer - Firewall & Back Navigation**
- **Silent exit at "Configuring firewall..."** - `[[ -n "$STRATUM_PORT" ]] && sudo ufw allow ...` returns exit 1 when the variable is empty, which under `set -e` kills the entire installer. Replaced with `if/then` guards for both `STRATUM_PORT` and `STRATUM_V2_PORT`.
- **Back navigation ('b') kills installer** - `select_coin_mode`, `select_ha_mode`, `select_merge_mining_parent`, and `select_aux_chains` all use `return 1` to signal "go back". Under `set -e`, checking the return with `func; if [[ $? -eq 1 ]]` exits the script before the `$?` check runs. Rewrote all callers to use `if func; then` pattern.
- **`systemctl reset-failed` polkit auth failure** - `start_services()` called `systemctl reset-failed` without `sudo`, triggering polkit authentication prompts in non-interactive mode. Added `sudo` and added `reset-failed *` to the sudoers NOPASSWD allowlist.

**Installer - Dashboard Service**
- **Stale gunicorn control socket prevents dashboard start** - added `ExecStartPre=-/bin/rm -f gunicorn.ctl` to the spiraldash systemd service file and explicit `--worker-class gthread` to the `ExecStart` line.

**Upgrade - Dashboard Not Starting**
- **Dashboard hangs after upgrade** - stale `__pycache__` bytecode from the previous Python version and leftover `gunicorn.ctl` sockets from killed processes caused the dashboard to hang or fail on restart. `update_dashboard()` now cleans both before copying new files. Changed dashboard start from `--no-block` to blocking with a health check and automatic restart on failure.
- **Upgrade summary not waiting for services** - the summary screen now polls dashboard and sentinel for up to 120 seconds before reporting status, skipping stratum (which depends on blockchain node sync).

**Stratum Server**
- **Stratum hangs on shutdown (120s → SIGKILL)** - `connWg.Wait()` in `server.go:Stop()` had no timeout, hanging indefinitely when connection goroutines were stuck. Added a 10-second select timeout before proceeding with shutdown.
- **ESP32 miners showing 0 shares on dashboard** - `Session.IncrementShareCount()` existed but was never called in production code. Added the call in both `pool.go` (V1) and `coinpool.go` (V2) when a share is accepted. The dashboard's ESP32 panel reads this counter via the connections API.

**Spiral Sentinel - Block Alert**
- **Block alert shows wrong explorer page** - when a block is found seconds before the explorer indexes it, the "View Block" link opens a stale page. Added the block hash (first 16 chars) directly in the alert text so the user can verify without depending on the explorer.
- **Pool block counter wrong after Sentinel restart** - `pool_blocks_found` started from 0 on fresh state instead of initializing from the pool API's existing block count. Block #4 would show as "Pool Block #1" after a Sentinel restart.

**spiralctl**
- **`spiralctl coin enable` fails with "command not found"** - `prompt_input()` was defined in `install.sh` but never added to `spiralctl.sh`, causing all coin enable/onboard commands to fail immediately.

- All version strings, documentation, themes, and config files bumped to 1.2.3

---

## [1.2.2] - 2026-03-25

### Fixed

**Installer - Reinstall / Upgrade Guard Pattern (all 13 coins)**
- **Daemon not stopped before config regeneration on reinstall** - if a daemon was already running and the user reinstalled, the installer would regenerate config files underneath a live daemon, causing port conflicts, stale PID files, and LevelDB lock contention. All 13 coin install functions now stop the running daemon (`systemctl stop`), call `reset-failed` (clears systemd's `StartLimitBurst` crash counter), and remove stale PID files before reconfiguring.
- **Reinstall skipped config regeneration entirely** - all 13 install functions had an early `return` when the binary already existed (`if [[ -f .../bitcoind ]]; then return`). This meant reinstalling skipped config regeneration, systemd service creation, and all downstream setup. Changed to an `*_binary_exists` + `*_download_needed` guard pattern: binary download is skipped, but config regen, service file, and wallet setup always run.
- **RPC password recovery on reinstall** - if `coins.env` was corrupted or truncated during a reinstall, all `*_RPC_PASSWORD` variables would be empty. The installer would then generate new passwords that don't match the passwords already written in each daemon's conf file, causing RPC auth failures on every coin. Added a 13-coin password recovery loop that reads `rpcpassword=` from each daemon's existing conf file before falling back to generating a new password.
- **BCH-specific empty password guard** - added an additional safety net for BCH: if `BCH_RPC_PASSWORD` is still empty after `coins.env` parsing and the recovery loop, attempts to recover from the existing `bitcoin.conf` before generating a new password. BCH was the coin triggering the crash report.

**Installer - WSL2 Resource Scaling (DGB, BTC, BCH)**
- **Daemons OOM-killed on WSL2** - `dbcache=8192` (8 GB) was hardcoded for DGB, BTC, and BCH regardless of available RAM. WSL2 instances typically have limited memory via `.wslconfig`, and 8 GB dbcache would consume all available RAM, triggering OOM kills. All three coins now detect WSL2 (`/proc/version` check), cap dbcache to 25% of total RAM (floor 1024 MB, ceiling 4096 MB), and scale `MemoryMax`/`MemoryHigh` systemd limits proportionally.

**Installer - systemd Service Files (all 13 coins)**
- **DGB missing PIDFile directive** - DGB systemd service had `Type=forking` but no `PIDFile=` or `-pid=` argument. systemd couldn't reliably track the daemon process, leading to false "active (running)" status when the daemon had already exited. Added `PIDFile=` to service and `-pid=` to `ExecStart`.
- **BC2 missing PIDFile directive** - same fix as DGB. Bitcoin II systemd service now has `PIDFile=` and `-pid=` argument.
- **BTC missing PIDFile directive** - Bitcoin Knots systemd service now has `PIDFile=` and `-pid=` argument.
- **BCH missing PIDFile directive** - Bitcoin Cash systemd service now has `PIDFile=` and `-pid=` argument.
- **LimitNOFILE=65535 (off-by-one)** - 11 coin systemd services used `LimitNOFILE=65535` instead of the correct `65536` (2^16). While functionally harmless on most kernels, 65536 is the conventional power-of-two value. Standardized across all coins.

**Installer - BCH Config**
- **BCH missing `blockmaxsize` setting** - BCH config had `excessiveblocksize=32000000` (accept 32 MB blocks from the network) but was missing `blockmaxsize=32000000` (generate blocks up to 32 MB when mining). Without this, mined blocks would be capped at the Bitcoin Core default of 2 MB.

**Multi-Disk Storage (CHAIN_MOUNT_POINT)**
- **CHAIN_MOUNT_POINT grep pattern included literal quotes** - `coins.env` writes values as `CHAIN_MOUNT_POINT="/mnt/data"` (with quotes), but the `grep -oP '\K\S+'` pattern extracted `"/mnt/data"` including the quote characters. Every `-d` directory check silently failed, causing all multi-disk setups to fall back to `$INSTALL_DIR/<coin>/` regardless of configuration. Fixed across 12 instances in 5 files: `install.sh`, `spiralctl.sh`, `blockchain-export.sh`, `blockchain-restore.sh`, `wait-for-node.sh`.
- **spiralctl.sh `get_coin_cli()` ignored multi-disk paths** - all 13 coin CLI commands used hardcoded `$INSTALL_DIR/<coin>/` paths instead of checking `CHAIN_MOUNT_POINT`. Coin daemon CLI commands (getblockchaininfo, stop, etc.) would target the wrong config file on multi-disk setups. Added `_chain_dir()` helper and updated all 13 coin entries.
- **spiralctl.sh Tor status check hardcoded DGB path** - used `$INSTALL_DIR/dgb/digibyte.conf` instead of `$(_chain_dir dgb)/digibyte.conf`
- **blockchain-export.sh missing multi-disk support** - all 13 `COIN_DIRS` entries were hardcoded to `$INSTALL_DIR/<coin>/`. Added `_chain_dir()` helper with `CHAIN_MOUNT_POINT` lookup.
- **blockchain-restore.sh missing multi-disk support** - same fix as blockchain-export.sh
- **ha-replicate.sh missing multi-disk support** - all 13 `BLOCKCHAIN_DIRS` entries were hardcoded. Added `_chain_dir()` helper with `CHAIN_MOUNT_POINT` lookup.

**Daemon & Docker Config**
- **pool-mode.sh BC2 wallet commands hardcoded `/spiralpool/`** - 5 occurrences in the BC2 wallet creation block used `/spiralpool/bc2/bitcoinii.conf` instead of `$SPIRALPOOL_DIR/bc2/bitcoinii.conf`, failing on non-default install paths.
- **DigiByte Docker config missing `zmqpubrawblock`** - `digibyte.conf.template` had `zmqpubhashblock` and `zmqpubrawtx` but was missing `zmqpubrawblock`. All other 12 ZMQ-enabled coins had all three topics. Docker-mode DGB would miss raw block notifications.

**HA & Recovery**
- **ha-role-watcher.sh recovery health check matched error pages** - `grep -q "enabled"` on the HA status endpoint would match HTML error pages containing the word "enabled" anywhere, causing false-positive health checks. Replaced with `jq -e '.enabled == true'` for proper JSON validation.

**Regtest & Testing**
- **regtest.sh PepeCoin SIGABRT crash** - ZMQ arguments (`-zmqpubhashblock`, `-zmqpubrawblock`) were passed unconditionally to all coin daemons. PepeCoin v1.1.0 is compiled without ZMQ support and crashes with SIGABRT on startup when zmqpub* arguments are present. ZMQ args now conditionally skipped for PEP.
- All version strings, documentation, themes, and config files bumped to 1.2.2

---

## [1.2.1] - 2026-03-24

### Added

- **DigiByte as merge mining parent chain** - install.sh now offers DGB as an explicit SHA-256d parent option (option 3) for merge mining with NMC, SYS, XMY, and FBTC auxiliary chains. Previously DGB was only an implicit fallback when BTC was disabled; now it is a first-class selection alongside BTC and LTC.
- **Back navigation in installer** - pressing `b` at any menu prompt returns to the previous step. Covers install mode, merge mining, coin selection, aux chain selection, and HA mode. No more Ctrl+C to fix a fat-finger.
- `spiralctl mining merge enable` also updated to recognize DGB as a valid SHA-256d parent
- Multi-coin mode merge mining prompt now detects DGB as SHA-256d parent when BTC is not present
- MOTD, Docker guide, spiralctl reference, and docker-compose.yml updated to list DGB as merge mining parent

### Fixed

- **LED celebration ignoring quiet hours** - the stratum Go code (`pool.go`, `coinpool.go`) launched `block-celebrate.sh` directly on block found, bypassing Sentinel's quiet hours check. The bash script now reads Sentinel's `quiet_hours_start`, `quiet_hours_end`, and `display_timezone` from config.json and enforces quiet hours at startup. Additionally, running celebrations now check periodically and stop early if quiet hours begin mid-celebration. `--force` flag added for manual override.
- **MOTD not updating on upgrade** - `update_motd()` in upgrade.sh used `cat >` to write to `/etc/update-motd.d/`, which silently fails without root. Now uses `sudo tee` matching install.sh.
- **Dashboard section ordering** - Lifetime Statistics section now renders below Statistics (charts) instead of above it
- **Flaky stress test** - `TestRapidFireHeightUpdates` widened stale RPC tolerance from 0 to 1; on slow CI runners a goroutine can slip through the cancellation window
- All version strings, documentation, themes, and config files bumped to 1.2.1

---

## [1.2.0] - 2026-03-23 - Convergent Spiral

> *One pool. Every coin. No limits.*

### Added

**Docker Multi-Coin Support**
- New `POOL_MODE=multi` for running multiple coins in a single Docker deployment
- `--profile multi` launches all enabled coin daemons and shared services
- Per-coin `ENABLE_<COIN>=true` flags and `<COIN>_POOL_ADDRESS` wallet addresses in `.env`
- V2 config generation in entrypoint: programmatic YAML output matching install.sh's multi-coin format
- All 13 supported coins available: DGB, BTC, BCH, BC2, NMC, SYS, XMY, FBTC, LTC, DOGE, DGB-SCRYPT, PEP, CAT

**Docker Merge Mining**
- Merge mining now supported in Docker multi-coin mode
- SHA-256d: BTC+NMC, BTC+FBTC, BTC+SYS, BTC+XMY (or DGB as parent if BTC disabled)
- Scrypt: LTC+DOGE, LTC+PEP
- Configured via `MERGE_MINING_ENABLED`, `MERGE_MINING_ALGO`, `MERGE_MINING_AUX_CHAINS_SHA256D`, `MERGE_MINING_AUX_CHAINS_SCRYPT`

**Docker Stratum V2 (Noise Protocol Encryption)**
- V2 Enhanced Stratum now available in Docker via `STRATUM_V2_ENABLED=true` in `.env`
- Uses `Noise_NX_secp256k1_ChaChaPoly_SHA256` - ephemeral keys generated in memory at startup
- No certificate files, no key management - zero-config encryption
- Works in both single-coin and multi-coin Docker modes
- Each coin gets a dedicated V2 port (V1 port + 1, e.g. DGB: 3334, BTC: 4334)
- Docker is now at full feature parity with native install for single-node deployments

**Dashboard Statistics Chart Grid**
- New 2×2 chart grid showing Pool Hashrate, Network Hashrate, Difficulty, and Workers & Miners - each with a current value and time-series chart
- Shared time-range dropdown selector: 15M, 1H, 6H, 12H, 24H, 7D, 30D
- Chart colors are fully theme-aware - each of the 23 built-in themes defines its own chart palette via `chart-pool-hashrate`, `chart-network-hashrate`, `chart-difficulty`, `chart-workers` color keys
- Chart colors customizable in the Custom Theme Editor (4 new color pickers: Pool HR, Net HR, Difficulty, Workers)
- Pool Hashrate stat card restored to the stats overview row (first position)

**Activity & Top Block Finders Section**
- Activity Feed and Top Block Finders now displayed side-by-side in a 2-column layout (stacks on mobile)
- Top Block Finders moved out of the Health section into its own dedicated panel
- Leaderboard now consolidates workers that map to the same device - e.g. `HashForge` and `HashForge.worker1` are merged into a single entry with combined block count and rewards

**V1.2 Convergent Spiral Codename Theme**
- New release codename theme with its own distinct palette - deeper charcoal backgrounds, brighter gold convergence points, stronger amethyst purple accents
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
- `'>' not supported between instances of 'str' and 'int'` when adding stock Antminer to dashboard - CGMiner API returns numeric values as strings
- Added `_safe_num()` helper for safe string-to-number conversion across all 11 miner fetch functions: `fetch_antminer`, `fetch_braiins`, `fetch_vnish`, `fetch_luxos`, `fetch_epic_http`, `fetch_axeos`, `fetch_esp32miner`, `fetch_avalon`, `fetch_whatsminer`, `fetch_innosilicon`, `fetch_goldshell`
- Innosilicon firmware confirmed highest risk - returns string-encoded values for power, fan speed, temperature, and error codes

**Backup ACL Inheritance**
- New backup files created by cron were not inheriting read permissions for the pool user
- Added default ACL (`setfacl -R -d -m`) in `install.sh` so new files automatically inherit the correct permissions

**Sentinel Backup Status Display**
- Removed `du -sh` size check from backup report section - fails with "Permission denied" when pool user lacks recursive read on `/spiralpool/backups/`
- Now displays snapshot count only (`💾 Snapshots: 2`) instead of erroring with a `setfacl` hint

**Theme Mojibake**
- Fixed double-encoded UTF-8 em dashes in `black-ice.json` (name, description) and `bitcoin-laser.json` (description, customCSS) - displayed as garbled `â€"` characters

**Spiral Router - User-Agent Pattern Cleanup**
- Removed ~70% of miner detection patterns that were dead code - matched hardware model names (e.g. "Antminer S19", "Avalon Nano 3S") that manufacturers never include in stratum user-agent strings
- All remaining patterns verified against firmware source code (ESP-Miner, cgminer, bmminer, NerdMiner, etc.)
- `cgminer` and `bfgminer` reclassified from `MinerClassMid` to `MinerClassUnknown` - these generic mining clients span a 45,000× hashrate range (GekkoScience 2 TH/s to Avalon A16XP 300 TH/s); vardiff now handles classification, and Sentinel's DeviceHints provides model-specific difficulty for known devices
- Pattern count reduced from ~280 to 47 verified patterns; all 15 SHA-256d and 8 Scrypt difficulty profiles unchanged

**Scrypt Miner Test Accuracy**
- Removed SHA-256d-only miners from Scrypt test suite: `bmminer` (SHA-256d only per bitmaintech/bmminer-mix), `btminer` (MicroBT makes no Scrypt miners), `Braiins OS` (SHA-256d only, no L-series support), `sgminer` (GPU - not supported), NerdMiner/ESP32/BitAxe/NerdQAxe (BM-series SHA-256d ASICs)
- Antminer L-series (L3+, L7, L9) correctly identified as sending `cgminer/X.X.X` (per bitmaintech/cgminer-ltc), not `bmminer`
- Algorithm switch test updated to use `cgminer/4.10.1` (real Scrypt firmware UA) instead of `bmminer/2.0.0`

**Sentinel Network Hashrate**

**Wood Paneling Theme**
- Complete palette rework - replaced all-amber/gold colors with walnut browns, copper/burnt sienna accents, cream text, and forest green status indicators

**Avalon Restart Button**
- Avalon/Canaan devices showed a "Restart" button that always failed - Avalon firmware does not support the CGMiner `restart` command
- Miner card now shows "⚙ Configure" which opens the Avalon web UI in a new tab; detail modal hides the restart button entirely for Avalon devices
- Removed `avalon` from the CGMiner restart code path in the backend

**Block Celebration Stale Alert**
- Block celebration (confetti/audio) fired for blocks found hours ago after a page reload or service restart - `sessionStorage` block count was stale
- Celebrations now only fire for blocks found within the last 5 minutes; older blocks silently update the counter

**Pool Hashrate Farm Fallback**
- Pool Hashrate stat card was falling back to farm hashrate (self-reported by miner devices) when the stratum reported 0 - displayed wildly inaccurate numbers (e.g. 32 TH/s when actual pool hashrate was 0)
- Removed farm hashrate fallback; pool hashrate now shows stratum-reported value only

**Miners Connected Stat Card**
- "Miners Online" stat card showed a confusing `X / Y` mixing stratum-connected miners with fleet device count, making it look like devices were mining on the pool when they weren't
- Renamed to "Miners Connected" showing only stratum-connected count; fleet device count and average temperature shown as subtitle

**RPC Credential Loading**
- `coin_rpc()` silently returned `None` when RPC credentials were not loaded into `MULTI_COIN_NODES` - `load_multi_coin_config()` loads ports and enabled status but not credentials

**Network Hashrate History Recording**
- `record_historical_data()` was using the formula (`difficulty × 2³² / block_time`) instead of `_compute_network_hashrate()` which prefers the accurate RPC value - chart history oscillated wildly on coins with fast block times
- Now uses `_compute_network_hashrate()` for consistent RPC-backed values in both live display and chart history

**Codename Theme Switching**
- V1.2 Convergent Spiral theme was missing from the `themeColors` JavaScript object - selecting it cleared the previous theme's customCSS but applied no new colors until the API fetch completed, making the theme appear broken
- `phi-forge.json` was incorrectly overwritten with Convergent Spiral data - the V1.1 Phi Forge codename theme was lost
- Restored `phi-forge.json` as V1.1 Phi Forge; created `convergent-spiral.json` as V1.2 Convergent Spiral with its own distinct palette (deeper backgrounds, brighter gold convergence, stronger purple)
- Both codename themes now have instant-switch entries in `themeColors` alongside V1.0 Black Ice

**Version String Consistency**
- 21 stale `1.2` references (missing `.0` patch) found and fixed across 19 files - script variables, Docker labels, display banners, and documentation taglines now all read `1.2.0`
- Affected: `install.sh` (3), `docker/Dockerfile`, `scripts/spiralctl.sh`, `scripts/linux/blockchain-export.sh`, `scripts/linux/blockchain-restore.sh`, `scripts/linux/ha-replicate.sh`, `scripts/linux/ha-setup-ssh.sh`, `scripts/linux/update-checker.sh`, `install-windows.ps1`, `dashboard.py`, `dashboard.html`, `upgrade.sh`, `SpiralSentinel.py` (2), `UPGRADE_GUIDE.md` (4), `README.md` (2), and 9 documentation taglines

### Changed

- Dashboard statistics chart period selector changed from button group to dropdown, added 15M and 12H periods
- Added `--chart-pool-hashrate`, `--chart-network-hashrate`, `--chart-difficulty`, `--chart-workers` CSS variable defaults and theme-overridable color keys across all themes
- Responsive rules for statistics chart grid, period dropdown, and activity/leaderboard split layout
- Mobile CSS improvements: statistics chart grid, activity feed, and leaderboard panels now properly sized and readable on mobile and small phones
- All version strings bumped to semver `1.2.0` - variables, labels, banners, and documentation taglines across all scripts, Docker, dashboard, Sentinel, and docs
- MOTD command grid column padding widened (24→26 chars) to fix `spiralctl chain export/restore` alignment
- All coin daemon containers now include `"multi"` profile in docker-compose.yml
- Updated docker-compose.yml header to document both single-coin and multi-coin usage
- Removed "Docker limitations" block from docker-compose.yml - multi-coin and merge mining are no longer unsupported
- `POOL_COIN`, `POOL_ID`, `POOL_ADDRESS` no longer required in Docker - defaults to empty for multi-coin mode
- `.env.example` expanded with full multi-coin configuration section (per-coin enable flags, wallet addresses, merge mining settings)
- Dockerfile description updated from "Single-Coin Mode" to "Single + Multi-Coin Mode"
- `config.docker.template` comments clarified as single-coin only; multi-coin mode generates config programmatically
- Coin daemon config templates (Fractal, Myriadcoin, Namecoin) updated to reference Docker multi-coin mode availability
- `stratum-entrypoint.sh` now branches on `POOL_MODE` with mode-aware validation (single requires `POOL_COIN`/`POOL_ADDRESS`; multi validates at least one coin enabled)

---

## [1.1.2] - 2026-03-22 - Phi Forge

> *When the miner speaks, the pool listens.*

### Fixed

**Unknown Miner Difficulty Override**
- ASICs sending empty or unrecognized user-agents (e.g. some Antminer S19 stock firmware) were forced into the "unknown" miner profile with `MinDiff=500 / MaxDiff=50000` - far too restrictive for ASIC hardware, preventing vardiff from reaching proper operating difficulty
- Unknown SHA-256d profile widened to `MinDiff=100 / MaxDiff=1000000` - vardiff now ramps up naturally to optimal difficulty for any miner class
- When Spiral Router cannot identify a miner, the pool now falls back to the operator's YAML/env config values instead of overriding with hardcoded defaults

**Connection Classifier - False PROXY on LAN**
- ASICs on local networks authorize in <5ms, which the timing heuristic misclassified as "automated software (proxy)" at 0.40 confidence
- Timing score reduced from 0.40 to 0.25 for <5ms auth delay; timing analysis now skipped entirely when Level 1 already identified the miner via user-agent

**Docker - AsicBoost / Version Rolling**
- `versionRolling` section was completely missing from the Docker config template - Vnish firmware reported pool offline because AsicBoost was not advertised
- Now enabled by default: `enabled: true`, `mask: 536862720` (standard BIP320)
- Configurable via `STRATUM_VERSION_ROLLING` and `STRATUM_VERSION_ROLLING_MASK` in `.env`

**Docker - Difficulty Environment Variables**
- `STRATUM_DIFF_INITIAL`, `STRATUM_DIFF_MIN`, `STRATUM_DIFF_MAX`, `STRATUM_VARDIFF_TARGET_TIME` were defined in `.env.example` but the config template used hardcoded values - operator overrides were silently ignored
- Template now uses `${STRATUM_DIFF_*}` substitution; defaults set in `stratum-entrypoint.sh`

### Changed

- All version strings bumped from 1.1.1 to 1.1.2

### Acknowledgements

- Thanks to **Kamakhu** for reporting the S19/S19K Pro classification bug and providing detailed logs and Docker config that helped diagnose both the difficulty and AsicBoost issues

---

## [1.1.1] - 2026-03-21 - Phi Forge

> *Built on what came before. Growing toward phi.*

### Added

**Custom Theme Editor**
- New in-dashboard theme editor panel in the Appearance sidebar - create custom themes without editing JSON files
- 13 color pickers: background, cards, 8 accent colors (blue, cyan, purple, pink, orange, yellow, green, red), text primary/secondary, border color
- Border radius selector (Sharp 0px → Extra 16px)
- Live preview - all color changes apply instantly as you pick
- Save to browser localStorage - custom themes persist across sessions
- Export as `.json` - download your custom theme in the standard Spiral Pool theme format
- Import `.json` - load any exported theme (or any Spiral Pool theme JSON) directly into the editor
- Custom themes appear in a "Custom" optgroup in the theme dropdown
- Validates imported themes: requires `colors` object with minimum keys (`bg-primary`, `bg-card`, `neon-blue`, `text-primary`)
- Handles localStorage quota errors gracefully ("Storage full - export instead")
- Editor pickers auto-refresh when switching themes via the dropdown

**Top Block Finders Leaderboard (Dashboard)**
- New leaderboard widget inside System Health section - ranks miners by blocks found with medal icons (gold/silver/bronze)
- Per-coin reward breakdown (e.g. "125.00 BTC + 500.00 NMC") instead of a single total
- Multi-coin support: queries all pools for solo, multi-coin, and merge-mining setups with single-pool fallback
- Blocks with no source attribution are filtered out
- Retroactive - pulls all historical blocks from PostgreSQL via the pool API

**Profitability Tracker Module (Sentinel)**
- New `compute_coin_profitability()` and `compute_profitability_rankings()` functions in Spiral Sentinel
- Calculates daily fiat revenue per coin: `(block_reward × blocks_per_day × hashrate) / network_hashrate × coin_price`
- Groups coins by algorithm family (SHA-256d, Scrypt) for profitability ranking
- Module is present in code but **not active** - staging for v1.2.0 profit-switching

### Changed

**Theme Quality Overhaul**
- **Phi Forge**: Redesigned - all-gold monochromatic palette replaced with gold + amethyst purple accents on dark charcoal background; added visual hierarchy with contrasting secondary color
- **Bitcoin Laser**: Background changed to true black (#050505); secondary accent changed from grey to laser red (#cc2200); stripped to minimal effects for maximalist aesthetic
- **Vaporwave**: Background changed from deep purple (duplicate of Rainbow Unicorn) to dark teal (#0a1018) with sunset horizon glow; primary accent shifted to cyan; completely distinct visual identity
- **Solar Flare**: Background changed from warm brown (duplicate of Autumn Harvest) to near-black (#080808); hotter plasma yellows (#ffee00) for a coronal ejection feel
- **Midnight Aurora**: Background changed from deep purple to neutral dark; primary accent changed from cyan to aurora green (#40d8a0); now green/purple curtain effect, distinct from Ocean Depths' blue/cyan
- **Wood Paneling**: Fonts changed from Playfair Display + Lato (identical to Autumn Harvest) to Libre Baskerville + Source Sans 3
- **Nebula Command**: Display font changed from Orbitron (shared with Cyberpunk) to Titillium Web

**Sentinel - Backup Reporting**
- Backup size display now shows actual size instead of `?` when permissions are correct
- Shows "no access" instead of `?` when `Permission denied` is detected - diagnosable instead of opaque
- Backup snapshot count added to report: `💾 Size: 3.1M (2 snapshots)`
- Recursive ACL (`setfacl -R`) applied during install so spiralpool user can read backup subdirectories - no manual setup needed
- `acl` package added to installer prerequisites

**Dashboard - ETB Display**
- Estimated Time to Block now shows minutes when under 1 hour (e.g. "12 minutes" instead of "0.2 hours")

**External Access - Rented Hashrate**
- `sharesPerSecond` now configurable in `spiralctl external setup` wizard with tiered options:
 - Small (<10 TH/s): 200/sec, Medium (10–100 TH/s): 500/sec, Large (100TH–50PH): 1000/sec, XL (50+ PH/s): 2000/sec, Custom: 10–100000
- Default `sharesPerSecond` changed from 50 to 500 (Medium tier)
- Cloudflare Tunnel setup now warns that Spectrum (paid add-on) is required for raw TCP proxying
- Documentation updated with Spectrum prerequisite and shares-per-second configuration table

**Go Toolchain**
- Go version updated from 1.25.6 to 1.26.1 across all build paths (go.mod, install.sh, upgrade.sh, Dockerfile, test.sh)
- Minimum build requirement is now Go 1.26.1 (enforced by go.mod) - `install.sh` and `upgrade.sh` download Go 1.26.1 automatically from go.dev; existing installs with older Go will be upgraded on next `upgrade.sh` run

### Security

- **Theme CSS injection hardening**: `customCSS` field in theme JSON files is now sanitized before injection - `url()`, `@import`, `expression()`, `javascript:`, `-moz-binding`, `behavior:`, and Unicode escape obfuscation are all blocked and replaced with `/* blocked */`
- **CSS variable value sanitization**: all CSS custom property values from theme JSON are validated - values containing `url()`, `expression()`, or `javascript:` are rejected before `setProperty` to prevent data exfiltration via computed styles
- **Imported theme confirmation prompt**: importing a `.json` theme that contains `customCSS` now shows a confirmation dialog with a preview of the CSS - operator can cancel to apply colors only without the custom CSS

### Fixed

- Backup script permissions: added `chown -R root:spiralpool` step so Sentinel can read backup sizes
- 7 themes fixed for visual similarity - eliminated duplicate-looking pairs across all 23 themes
- Dashboard "Miners Online" display could show numerator exceeding denominator (e.g. 8/7) during stratum reconnection spikes - clamped to `min(realtime, configured)` so the count never exceeds the fleet total; also fixed unclamped workers count in hashrate subtitle

**`upgrade.sh` - Service Status Display**
- Post-upgrade service status check ran immediately after `systemctl start --no-block`, showing services as `inactive` / `deactivating` - added 10-second wait before verification and 5-second wait before summary display
- Summary now shows contextual note when services aren't yet active: "Services may take up to 30 seconds to fully start" with a re-check command

**`upgrade.sh` - API Key Migration**
- Admin API key grep patterns required double-quoted values (`"\K[^"]+`); unquoted YAML values (valid syntax) silently failed, causing the upgrade to generate a new API key instead of preserving the existing one
- Fixed all 6 grep patterns (Fix 6, Fix 7, Fix 8) to accept both quoted and unquoted values (`"?\K[^"\s]+`)

**`upgrade.sh` - Go Download Hang**
- Go 1.26.1 download used `curl -fsSL` (silent mode) - a ~150MB download with no progress output appeared to hang indefinitely
- Fixed: removed `-s` flag, added `--connect-timeout 15` and `--max-time 300`, added "Downloading Go 1.26.1" log message; also fixed in `test.sh`

**Notification Formatting - Discord / Telegram**
- All maintenance-mode, HA, and update-checker notifications used literal `\n` in double-quoted bash strings - Discord and Telegram displayed `\n` as text instead of newlines
- Fixed: all notification messages now use `printf -v` to produce real newline characters
- Node identifier in notification footers changed from truncated UUID (`Node: 8990382...`) to hostname (e.g. `spiralpool-dgb-109`) - consistent with Sentinel's existing approach

**Dashboard - Coin Daemon Version Display**
- Dashboard showed incorrect version for daemons with broken `subversion` strings (e.g. some daemons report a fixed version string regardless of installed version)
- Fixed: dashboard now reads from version cache (`/spiralpool/config/coin-versions/<COIN>.ver`) when available, which reflects the actual installed binary version

**Documentation - Lottery Miner Support**
- README now lists NerdMiner, NM Miner, and other ESP32-based lottery miners as supported hardware
- Explicitly noted support for any Stratum V1-compatible device regardless of hash power

**Documentation - `git clone` Instructions**
- All user-facing `git clone` instructions now use `--depth 1` to skip git history (~29MB), reducing download size to source files only (~16MB)

---

## [1.1.0] - 2026-03-19 - Phi Forge

> *Convergent difficulty. Minimal oscillation.*

### Added

**Installer - Native Existing-Install Detection**
- `detect_existing_native_install()` - new function mirrors the existing Docker detection path; reads `/spiralpool/config/coins.env` on re-run, detects which coins are already enabled, and presents a clear menu:
 - `[1] Add coins to existing installation` - loads all existing RPC passwords, pool addresses, and wallet addresses; skips prompts for already-configured coins; preserves DB password and admin API key
 - `[2] Fresh installation` - clean run, no state carried forward
- `coins.env` now persists per-coin RPC passwords and pool addresses for all 13 coins so they can be recovered on re-run without user re-entry
- Multi-coin address collection blocks now guard against overwriting existing wallet addresses - if an address is already present from a previous install, it is preserved silently and the prompt is skipped

**`spiralctl coin enable` - Add Supported Coins**
- New `spiralctl coin enable <TICKER>` command to add any of the 14 natively supported coins
- Launches the installer in "Add coins to existing installation" mode - handles daemon install, wallet generation, config.yaml, firewall ports, and service restart automatically
- After enabling, the Dashboard at `/setup` auto-detects the new coins and shows wallet inputs
- `spiralctl coin disable <TICKER>` stops and disables a coin daemon (wallet and blockchain data preserved)
- `spiralctl add-coin` is now explicitly for **custom/unsupported coins only** (advanced)
- `spiralctl add-coin <TICKER>` still guards against built-in tickers and redirects to `coin enable`

**`add-coin.py` - Scope Clarification**
- Module docstring and usage examples updated to explicitly state this tool is for **NET NEW coins only** - coins not natively supported by Spiral Pool
- Built-in coin list displayed prominently in help output
- Examples updated to use placeholder tickers instead of natively-supported coins

**`spiralctl coin-upgrade` - Coin Daemon Upgrade Utility**
- New `coin-upgrade.sh` script and `spiralctl coin-upgrade` subcommand for in-place coin daemon binary upgrades
- Upgrades the binary only - config files, wallets, blockchain data, and pool settings are never modified
- Risk classification per upgrade: `PATCH` (binary swap, reindex not expected), `MINOR` (reindex may be needed), `MAJOR` (reindex almost certainly required)
- `--check` flag shows current vs target version status with no changes made
- `--coin <TICKER>` targets a specific coin; `--reindex` starts the daemon with `-reindex` after upgrade
- Operator-initiated only - never triggered automatically by `upgrade.sh` or Sentinel

**ntfy Push Notifications**
- New notification channel: [ntfy](https://ntfy.sh) - free, no-account mobile/desktop push notifications
- Configure with `ntfy_url` (full topic URL) and optional `ntfy_token` for private/self-hosted topics
- Wired into `send_notifications()` alongside Discord, Telegram, and XMPP - participates in retry logic and fallback logging
- Block found embeds include an ntfy Action button ("View Block") linking to the block explorer when available
- install.sh notification setup now includes an ntfy configuration step

**Block Explorer Links**
- Block found Discord notifications now include a **View Block** field with a link to the canonical block explorer for each coin
- Discord embed title is also a hyperlink (clickable in Discord client)
- Explorer URL is passed as an ntfy Action button for one-tap mobile access
- Per-coin explorer map: BTC → mempool.space, BCH/LTC/DOGE/SYS → blockchair.com, DGB → digiexplorer.info, NMC → bchain.info, FBTC → fractalbitcoin explorer; coins without public explorers (BC2, XMY, PEP, CAT) show no link

**Installer - Consolidated Sentinel Configuration Menu**
- All Sentinel configuration (alerts, health monitoring, reports, update mode) is now presented as a single interactive toggle menu instead of 3–4 sequential question screens
- 11 items in one view: master alerts switch, 7 individual alert types (dry streak, difficulty change, disk space, BTC mempool, backup staleness, sats surge, wallet drop), health monitoring, report frequency, and update mode
- When master alerts is toggled OFF, items 2–8 are greyed out with a note that they are muted - no false impression of individual control while the master switch suppresses everything
- Report frequency cycles through three states: `4x Daily` → `1x Daily` → `Off`
- Update mode cycles through: `Notify Only` → `Auto-Update` → `Disabled`
- Per-alert preferences are written directly into `config.json` at install time; Sentinel respects them immediately with no manual config editing required
- New config keys written at install time: `sats_surge_enabled` (default `true`) and `wallet_drop_alert_enabled` (default `true`) - previously these alert types were always on with no per-install control

**Installer - Notification Setup UX**
- Each notification channel (Discord, Telegram, XMPP, ntfy, SMTP) now gets its own dedicated full-screen section with a clear header - terminal is cleared between each channel so output from the previous section does not crowd the next
- Fleet configuration (expected hashrate prompt) also gets its own cleared screen
- Alert theme description updated to accurately name all five supported notification channels instead of only "Discord/Telegram"

**Cloud Deployments - Hardening**
- **Individual risk acknowledgment gates**: cloud installs now require typing `YES` to each of the five risks separately (ToS violation, account termination / data loss, provider access to credentials and disk, bandwidth billing, IPv6 disabled at kernel level) - a single combined gate was replaced with per-risk prompts
- **Legal terms YES gate on cloud**: cloud operators must type `YES` (non-cloud: `I AGREE`) - consistent with the per-risk prompts; `--accept-terms` CLI flag removed (all risk acknowledgment is now manual and interactive)
- **Risk 5 - IPv6 disabled**: explicit acknowledgment added; IPv6 is disabled at the kernel level (`/etc/sysctl.conf`) because it causes kernel routing cache corruption during keepalived VIP failover operations
- **HA forced to standalone on cloud**: selecting HA Primary or HA Backup on a cloud provider now auto-reverts to Standalone with an explanation; cloud provider networks block VRRP (keepalived) multicast/broadcast required for VIP failover
- **Tor disabled on cloud**: Tor is automatically disabled on cloud installs (most provider AUPs prohibit Tor; it also doesn't protect against provider hypervisor access - the primary cloud threat)
- **ZMQ bindings hardened**: all `zmqpubhashblock`, `zmqpubrawtx`, and `zmqpubrawblock` daemon config entries changed from `tcp://0.0.0.0:PORT` to `tcp://127.0.0.1:PORT` - ZMQ is a local IPC channel between the daemon and stratum; it never needs to be reachable from outside the server
- **Prometheus metrics loopback-only on cloud**: port 9100 is restricted to `127.0.0.1/::1` on cloud (UFW); the cloud provider's "local subnet" is a shared tenant network, not a trusted private network
- **Wallet security warning**: cloud installs show a red warning before wallet address collection explaining that `wallet.dat` written by "Generate one for me" (option 2) stores unencrypted private keys on provider-managed disk - operators are directed to use a hardware wallet address (option 1)
- **Credentials security notice**: post-install completion shows a red notice instructing operators to copy the admin API key offline, delete `credentials.txt`, and clear terminal history; swap-to-disk risk and auto-reboot behavior also documented here
- **Swap security**: 4 GB swapfile creation now logs a cloud-specific warning that in-memory credential data can be written to swap on provider-managed disk; documented in `CLOUD_OPERATIONS.md`
- **Auto-reboot notice**: `unattended-upgrades` auto-reboot at 04:00 UTC is logged as a cloud-specific warning with instructions to disable if desired; documented in `CLOUD_OPERATIONS.md`
- **SSH tunnel for dashboard**: cloud completion output replaces the direct dashboard URL with SSH tunnel instructions (`ssh -L 1618:localhost:1618 user@server`); port 1618 is intentionally closed in UFW on cloud
- **API port annotation**: cloud completion output annotates the pool API URL as world-accessible (intentional - public pool stats) with a note that admin routes require the API key
- **CLOUD_OPERATIONS.md expanded**: new sections added for IPv6, HA not supported, wallet security, ZMQ/RPC port security, credentials security, swap security, automatic reboots, and PostgreSQL data durability; post-install checklist updated with all new items
- **`--simulate-cloud <provider>` flag**: test flag added to simulate cloud install paths on local VMs without a real cloud provider

**Documentation**
- `docs/setup/UPGRADE_GUIDE.md` - new upgrade guide covering all coin types, merge mining compatibility, database migration analysis (zero new migrations in v1.1.0), and all `upgrade.sh` flags

**Sentinel - New Monitoring Alerts**
- **Dry streak alert**: fires when no block has been found in `dry_streak_multiplier × ETB` (default 3×). Configurable via `dry_streak_enabled` / `dry_streak_multiplier`. Cooldown 6h.
- **Network difficulty change alert**: fires when difficulty drifts ≥ `difficulty_alert_threshold_pct` (default 25%) from the baseline at last alert. Comparison is against the previous alert baseline, not tick-to-tick - prevents constant noise on per-block difficulty coins (DGB, DOGE). Configurable via `difficulty_alert_enabled` / `difficulty_alert_threshold_pct`. Cooldown 1h.
- **Disk space monitoring**: checks `/`, `/spiralpool`, `/var` (configurable via `disk_monitor_paths`). Enabled via `disk_monitor_enabled` (default true). Warning at `disk_warn_pct` (default 85%), critical at `disk_critical_pct` (default 95%). Per-path cooldowns: 1h warning, 5min critical.
- **BTC mempool congestion alert**: fires when Bitcoin mempool exceeds `mempool_alert_threshold` transactions (default 50,000). Configurable via `mempool_alert_enabled` / `mempool_alert_threshold`. Cooldown 1h.
- **Stratum-down alert**: fires via `send_notifications()` (bypasses quiet hours) when the pool API has been unreachable for 5+ minutes. Clears automatically with a recovery notification when the pool comes back online.
- **Backup staleness alert**: fires when the newest backup in `/spiralpool/backups/` is older than `backup_stale_days` (default 2 days). Only active when `/etc/cron.d/spiralpool-backup` exists (i.e., user opted in during install). Cooldown 24h.
- **Config validation → Discord**: at startup, if `validate_config()` finds any issues (placeholder wallets, invalid URLs, etc.), a yellow warning embed is sent immediately after the startup summary. Fires once per Sentinel restart.

**Sentinel - Intel Report Enhancements**
- **Per-coin ETB** (Expected Time to Block): shown in the NETWORK section of 6h/daily reports below the difficulty line. Displays as days, hours, or minutes depending on magnitude.
- **Per-miner health score**: each miner line in the RIGS section now includes a colour-coded health score (💚 ≥90, 💛 ≥75, 🔴 <75).
- **Backup status field**: when the backup cron is installed, intel reports include a `💿 BACKUPS` field showing last backup timestamp, age, total size, and the cron schedule.

**Sentinel - Scheduled Maintenance Windows**
- New config key `scheduled_maintenance_windows`: a list of time windows during which non-critical alerts are suppressed
- Each window supports `start`/`end` times, optional `days` list (0=Monday), and overnight ranges
- Scheduled reports and `block_found` always go through regardless of maintenance windows

**Sentinel - HA Blip Suppression**
- Role change alerts (`ha_demoted` / `ha_promoted`) are now suppressed for brief keepalived VRRP election blips
- Changed from cycle-based debounce (one 30s poll) to **timestamp-based debounce**: a role change must hold for `ha_role_change_confirm_secs` (default 90s) before an alert fires
- If the node reverts to its original role within the window (at any point), the blip is silently suppressed with a log entry
- Configurable via `ha_role_change_confirm_secs` in `config.json`

**spiralctl - Status Command Improvements**
- **Service uptime**: each service line in the SERVICES section now shows how long the service has been running (e.g. `up 3d 2h 15m`)
- **Miner connection ports**: MINER CONNECTION section moved to immediately after SERVICES (was at the bottom), so port addresses are visible without scrolling
- **Scheduled Tasks section**: new section at the bottom of `spiralctl status` showing the backup cron schedule and next PG maintenance timer run
- **Pool version**: version line shown at the top of the SERVICES section (read from `$INSTALL_DIR/VERSION`)
- **Sentinel version**: when Sentinel is running, its version string is queried from the health endpoint and appended to the Sentinel uptime line (e.g. `up 2h · v1.1.0-PHI_FORGE`)
- **Alert pause status**: if Sentinel alerts are paused, an ALERT STATUS section appears showing time remaining and reason with a tip to run `spiralpool-pause resume`

**spiralctl - Version Command Improvements**
- `spiralctl version` now shows a full version table: spiralctl, stratum binary (from `spiralstratum --version`), Sentinel, and all installed coin daemon versions

**Installer - PostgreSQL Auto-Maintenance Timer**
- `setup_pg_maintenance()`: installs a weekly systemd timer (`spiralpool-pg-maintenance.timer`, Sunday 03:00) that runs `VACUUM ANALYZE` on all pool tables
- Safely skips on Patroni replicas (`pg_is_in_recovery()` check prevents conflicts with streaming replication)
- Timer is `Persistent=true` - runs missed schedule after downtime on next boot
- Deployed by both `install.sh` and `upgrade.sh`

**Installer / Backup - Backup Integrity Verification**
- Daily backup script now verifies each `.sql.gz` dump with `gzip -t` after creation
- Generates `sha256sum` checksums for all backup files
- Sends a Discord notification (via webhook from Sentinel config) on backup completion or failure

**Documentation - Single-Operator Architecture Notice**
- New warning added to `install.sh` legal acceptance screen (red box before `I AGREE` prompt)
- New section "Single-Operator Architecture - Wallet Control" added to `WARNINGS.md`
- New `TERMS.md` Section 5E: Single-Operator Architecture - explicit legal acknowledgment
- `README.md`: operator notice added to the What Is Spiral Pool? section
- `docs/reference/MINER_SUPPORT.md`: prominent notice at top for miners connecting to operator-run pools

**Email / SMTP Notifications**
- New notification channel: SMTP email - send alerts to any email address via any SMTP server (Gmail, Outlook, self-hosted)
- Configure via `smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_to` in `config.json`
- STARTTLS (port 587, recommended) and SSL/TLS (port 465) both supported via `smtp_use_tls`
- Multiple recipients supported via comma-separated `smtp_to`
- Credentials stored in `config.json` (chmod 600, spiraluser only) - same hardening as Discord webhook and Telegram bot token
- Wired into `send_notifications()` alongside Discord, Telegram, XMPP, and ntfy - full retry and fallback logging
- install.sh notification setup now includes an SMTP configuration step

**Telegram Bot Commands**
- Sentinel now responds to commands sent to the configured Telegram bot:
 - `/status` - pool overview (coins, connected miners, hashrate)
 - `/miners` - per-miner address, hashrate, and shares/sec
 - `/hashrate` - pool hashrate and network difficulty per coin
 - `/blocks` - last 5 blocks found per coin
 - `/help` - command list
- Runs as a background daemon thread (long-poll `getUpdates`); only responds to the configured `telegram_chat_id` - all other senders silently ignored
- Configurable via `telegram_commands_enabled` (default `true` when Telegram is enabled)
- install.sh prompts to enable/disable bot commands when Telegram is configured

**`spiralctl miners` - Live Miner Table**
- New `spiralctl miners` command shows all connected miners with address, hashrate, shares/sec, and total shares - formatted table, per-coin grouping
- `spiralctl miners kick <IP>` disconnects all stratum sessions from the given IP; miner reconnects automatically on its own reconnect timer
- Kick uses `POST /api/admin/kick` (admin API key required from `config.yaml`)

**`spiralctl miner nick` - Miner Nickname Management**
- `spiralctl miner nick <IP> <name>` - set a display name for a miner in Sentinel
- `spiralctl miner nick list` - list all configured nicknames
- `spiralctl miner nick clear <IP>` - remove a nickname
- Edits `config.json` directly via Python; prints restart reminder

**`spiralctl config validate` - Dry-Run Config Check**
- `spiralctl config validate` checks both `config.yaml` (stratum) and `config.json` (Sentinel) for issues without restarting any services
- Checks: YAML/JSON syntax, placeholder wallet addresses, invalid notification URLs, SMTP completeness, `check_interval` sanity
- Also accessible as `spiralctl config validate` (added as a subcommand of `config`)

**`POST /api/admin/kick` - Stratum Kick Endpoint**
- New admin API endpoint: `POST /api/admin/kick?ip=X.X.X.X` (requires `X-API-Key` header)
- Closes all stratum sessions matching the given IP; returns `{"ip": "...", "kicked": N}`
- Used by `spiralctl miners kick`; also callable directly from scripts or monitoring tools

**Sentinel - Zombie Miner Kick-First Remediation**
- Zombie miner handling now uses a two-stage escalation: **kick stratum session first**, only escalate to a full miner reboot if the zombie condition persists 15 minutes after the kick
- Kick forces an immediate stratum reconnect (~5 seconds) without a 2-minute power cycle - resolves most zombie cases caused by stale connections
- If the kick resolves the issue, no reboot is triggered; if the zombie persists, Sentinel escalates and reboots as before
- Share rejection spikes now also trigger a stratum kick on first detection (forces reconnect + difficulty re-negotiation without a reboot)

**`spiralctl config notify-test` - Notification Channel Test**
- New subcommand: `spiralctl config notify-test` sends a test message to every configured notification channel and reports pass/fail per channel
- Covers Discord, Telegram, ntfy, SMTP email, and XMPP - shows ` - not configured` for channels not set up
- Eliminates the need to wait for a real alert to verify notification delivery

**`spiralctl config validate` - Expanded Checks**
- Admin API key cross-check: warns if `pool_admin_api_key` in sentinel config does not match `admin_api_key` in `config.yaml` - a silent mismatch caused all stratum kick calls to fail with 401
- Telegram completeness: warns if `telegram_bot_token` is set without `telegram_chat_id` or vice versa
- XMPP completeness: warns if any of `xmpp_jid` / `xmpp_password` / `xmpp_recipient` are set without the others
- `pool_api_url` format check: warns if the value is not a valid HTTP/HTTPS URL

**`spiralctl log errors` - Per-Service Filter**
- `spiralctl log errors [service] [window]` now accepts an optional service name to scope output to a single service
- Aliases: `stratum`, `sentinel`, `dash` / `dashboard`, `patroni` / `postgres` / `pg`, `ha` / `watcher`
- Examples: `spiralctl log errors sentinel`, `spiralctl log errors stratum 24h`

**Telegram Bot - `/uptime` Command**
- New bot command `/uptime` reports Sentinel process uptime and stratum service uptime (via `systemctl show`)
- Added to `/help` listing

**`upgrade.sh` - Post-Upgrade Config Validate**
- `spiralctl config validate` now runs automatically at the end of every upgrade, after the summary, to surface any key mismatches or placeholder values introduced by config migration

**Telegram Bot - `/pause` and `/resume` Commands**
- `/pause [minutes]` - pause non-critical Sentinel alerts for N minutes (default 30, max 1440). Writes the same pause file as `spiralctl pause` and `spiralctl maintenance on`. Shows time remaining in confirmation.
- `/resume` - cancel an active pause immediately and restore alerts. Reports if already unpaused.
- Both commands added to the `/help` listing

**`spiralctl config validate` - v1.1.0 Alert Config Range Checks**
- Added sanity checks for all new v1.1.0 alert configuration keys:
 - `disk_warn_pct` must be less than `disk_critical_pct`
 - `dry_streak_multiplier` must be ≥ 1
 - `difficulty_alert_threshold_pct` must be between 1 and 100
 - `backup_stale_days` must be ≥ 1
 - `mempool_alert_threshold` must be ≥ 100

**Installer - Coin Daemon Configuration Hardening**
- `dbcache` minimum raised to 4,096 MB for all coins (8,192 MB for BTC, BCH, and DGB) - a ceiling applied during IBD to reduce disk I/O; coins that already had a higher value are unchanged
- `dnsseed=1` enabled on all clearnet (non-Tor) coin configs for fast peer discovery

**Installer - DNS Seeds Verified and Updated**
- Stale or defunct seeds removed; active seeds confirmed

**Installer - Multi-Coin RAM Warning**
- RAM warning block added to the multi-coin selection flow - calculates minimum required memory for the selected coin combination and warns the operator if available RAM may be insufficient for concurrent initial sync

**Installer - Per-Coin CLI Address Flags**
- Enables fully non-interactive deployments and automated re-installs with pre-supplied addresses for all coin types

**Installer - `--version` Flag**
- `install.sh --version` prints the installer version string and exits - useful for scripted pre-flight checks and automated provisioning workflows

**`spiralctl` - Automatic Pool User Elevation**
- `spiralctl` commands that operate on pool files and services are now automatically re-executed as `spiraluser` via `sudo -u` when invoked as root or another user
- Eliminates "permission denied" errors when operators run `spiralctl` as root

**MOTD - Consistent Column Alignment**
- Login MOTD redesigned with uniform column spacing - service status, command grid, and coin list use fixed-width `printf`-padded columns throughout
- Status icons and color codes decoupled from column width calculation; padding computed in plain variables before color embedding - eliminates display misalignment caused by invisible color escape bytes being counted as printable width
- All section dividers unified to 90 characters; section labels removed for a cleaner layout
- `spiralctl coin-upgrade` replaces the old `coin-upgrade.sh` reference in the command grid
- Version string updated to `V1.1.0 - PHI FORGE EDITION`

**Docker - ntfy and SMTP Environment Variable Support**
- `docker/.env.example`: added `NTFY_URL`, `NTFY_TOKEN`, `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_TO` fields
- `docker/docker-compose.yml`: ntfy and SMTP vars now passed through to the Sentinel container
- `SpiralSentinel.py`: all 8 variables added to `env_overrides` - Docker deployments can configure ntfy and SMTP via environment variables without editing `config.json`
- Docker installer (single-coin and multi-coin paths) now includes ntfy and SMTP configuration prompts

**Documentation - Sentinel Configuration Reference Expanded**
- `docs/reference/SENTINEL.md`: 15 previously undocumented configuration keys added with descriptions, types, defaults, and examples
- `scheduled_maintenance_windows` format documented with `start` / `end` / `days` / `reason` field descriptions
- ntfy (`ntfy_url`, `ntfy_token`) and SMTP (`smtp_host`, `smtp_port`, `smtp_username`, `smtp_password`, `smtp_from`, `smtp_to`) added to the environment variables table for Docker operators

### Security

**Stratum - `POST /api/admin/kick` Input Validation**
- The `ip` query parameter was passed directly to `KickWorkerByIP` without validation - a crafted value could match unintended sessions via prefix matching
- Fixed: strict IP format validation via `net.ParseIP()` applied before the call

**Sentinel - SMTP No TLS Certificate Verification**
- Both STARTTLS and SMTP_SSL paths used the default (unverified) context, leaving email credentials exposed to MITM on untrusted networks
- Fixed: `ssl.create_default_context()` used for both paths - verifies cert chain and hostname

### Fixed

**Sentinel - Zombie Miner Kick-First Remediation - Inverted Escalation Logic**
- The two-stage escalation condition was backwards: the `else` branch (kick age < 15 min, i.e., kick just happened) was triggering an immediate miner reboot on the very next monitoring cycle (~30 seconds after the kick)
- Fixed: proper three-state check - `last_kick == 0` kicks, `kick_age < window` waits, `kick_age >= window` escalates

**Sentinel - Telegram Message Truncation**
- Messages truncated at exactly 4096 bytes could be cut mid-MarkdownV2 escape sequence, causing Telegram to reject the entire message with a 400 parse error
- Fixed: truncates at 4000 characters and appends `...` leaving room for a clean escape boundary

**Sentinel - Health Server Thread Exits Permanently on Error**
- If the health endpoint port was already in use at startup, or if `serve_forever()` encountered an unexpected exception, the background thread exited silently and the `/health` and `/cooldowns` endpoints became permanently unavailable
- Fixed: retry loop with 30-second backoff restores the endpoint once the port clears

**Sentinel - Alert Deduplication After Quiet Hours**
- `update_available` and `missing_payout` alerts were silently dropped instead of being re-queued when they fired during quiet hours
- Fixed: suppressed alerts are now correctly re-delivered after quiet hours end

**Stratum - `client.reconnect` Params Field**
- `BuildReconnect` emitted `"params": null` - some mining firmware rejects non-array `params` in stratum JSON-RPC
- Fixed: `"params": []`

**`spiralctl config list-cooldowns` - Port Hardcoded**
- The Sentinel health port was hardcoded to 9191, ignoring the `sentinel_health_port` value in `config.json`
- Fixed: port read from `config.json` at runtime, with 9191 as fallback

**`spiralctl log errors` - Subcommand Consumed as Window Argument**
- `spiralctl log errors 24h` passed `"errors"` as the window argument, failing the `^[0-9]+[smhd]$` validation - the command was effectively unusable with a time argument
- Fixed: `"errors"` subcommand is consumed before the window is parsed

**`spiralctl config validate` - Config Path Interpolated into Python String**
- The YAML syntax check used `open('$config_yaml')` inside a `-c` string - a config path containing a single quote would break the Python expression
- Fixed: path passed via `sys.argv[1]` through a heredoc

**`_send_cooldowns` - Dict Iteration Race**
- `state.last_alerts` was iterated directly while the monitor loop could be writing to it, risking a `RuntimeError: dictionary changed size during iteration`
- Fixed: snapshot copy taken before iteration

**Sentinel - `difficulty_alert_threshold_pct` Fallback Default Mismatch**
- `check_difficulty_changes()` called `CONFIG.get("difficulty_alert_threshold_pct", 10)` while the `DEFAULT_CONFIG` dict sets the key to `25` - the safety-net fallback and the real default were out of sync
- Fixed: fallback changed to `25` to match the documented and intended default

**Sentinel - `hashrate_crash` Cooldown Not Applied in DEFAULT_CONFIG**
- CHANGELOG documented the cooldown increase from 1 hour to 6 hours, and the comment was updated, but the actual value in `DEFAULT_CONFIG["alert_cooldowns"]["hashrate_crash"]` was never changed from `3600` - existing installs without a custom `config.json` override would still get 1-hour cooldowns
- Fixed: value corrected to `21600`

**Telegram Bot - `/pause [minutes]` Argument Never Parsed**
- `_handle_telegram_command` normalized `cmd` with `.split("@")[0]`, which preserved the full text including arguments - `"/pause 30"` stayed `"/pause 30"`, so `if cmd == "/pause":` never matched when arguments were present; `/pause 30` fell through silently to "Unknown command" and bare `/pause` was the only form that worked
- The handler also referenced an undefined `text` variable for argument splitting, which would raise `NameError` on execution
- Fixed: normalization now extracts just the command word (`raw_text.split()[0].split("@")[0]`); the `/pause` handler reads `raw_text` for argument parsing

**install.sh - New v1.1.0 Alert Threshold Keys Missing from Generated `config.json`**
- Fresh installs wrote the boolean enable/disable flags for new v1.1.0 alert features but omitted the corresponding threshold values (`dry_streak_multiplier`, `difficulty_alert_threshold_pct`, `disk_warn_pct`, `disk_critical_pct`, `mempool_alert_threshold`, `backup_stale_days`, `ha_role_change_confirm_secs`, `scheduled_maintenance_windows`) - Sentinel used its `DEFAULT_CONFIG` fallbacks correctly, but the generated `config.json` was incomplete
- Fixed: all 8 threshold keys now written with their defaults during installation

**Sentinel - Disk Space, Difficulty, and Dry Streak Alerts Silently Blocked for Second Resource**
- `check_disk_space` tracks per-path cooldowns (`"disk_critical:/"`, `"disk_critical:/spiralpool"` etc.) before calling `send_alert`, but `send_alert`'s internal generic rate limiter re-tracks under the bare key `"disk_critical"` - the first path's alert set the generic key, blocking the second path's alert for the entire cooldown period
- Same issue in `check_difficulty_changes` (per-coin pre-check key `"difficulty_change:BTC"` vs generic send_alert key `"difficulty_change"`) and `check_dry_streak` (per-coin `_dry_streak_tracking` vs generic `"dry_streak"` key)
- Fixed: all three functions now pass `state=None` to `send_alert` to bypass the redundant generic rate limiter, since they already manage their own per-resource cooldown tracking

**Installer - Wallet Manager Numeric Selection**
- Wallet manager address selection accepted free-form input but failed to map numeric menu choices to the correct wallet entry - selecting by number returned an invalid or empty address
- Fixed: numeric input now correctly resolved to the corresponding wallet record before proceeding

**Installer - DGB-SCRYPT Not Counted in Multi-Coin Sync Warning**
- `DGB-SCRYPT` was omitted from the post-install sync warning counter - the "N coins enabled" message showed a count one lower than the actual number of enabled coins when DGB-SCRYPT was selected
- Fixed: `ENABLE_DGB_SCRYPT` guard added to the counter block

**Installer - DGB-SCRYPT `POOL_ADDRESS` Not Inherited from CLI Flag**
- When `--address` was supplied on the command line, the `dgb-scrypt` case in `apply_cli_coin_config()` did not fall back to `CLI_ADDRESS` - the address was silently dropped and a manual prompt appeared even in non-interactive installs
- Fixed: `POOL_ADDRESS="${POOL_ADDRESS:-$CLI_ADDRESS}"` added to the `dgb-scrypt` case

- Fixed: `get_installed_version()` now checks a version cache file (`$INSTALL_DIR/config/coin-versions/<COIN>.ver`) before running `--version`. After a successful upgrade, the target version is written to the cache when the binary reports `unknown`. Future `--check` runs read the cache and show the correct version.

**`spiralctl coin` - `list` Subcommand Missing from Help Text**
- `spiralctl help` displayed `coin [status|disable]`, omitting the `list` subcommand
- Fixed: `show_help()` and the inline `cmd_coin()` fallback both updated to `coin [status|list|disable]`

**`upgrade.sh` Fix 7 - `admin_api_key` Not Migrated from v1 Config Format**
- v1.0.0 config stored the admin API key as `adminApiKey` under the `api:` YAML section; v1.1.0 stratum reads `admin_api_key` under `global:` only - after upgrading, the key was present in the config file but silently ignored by the new binary, leaving admin endpoints inaccessible and stratum kick disabled
- Fixed: `upgrade.sh` Fix 7 now reads `adminApiKey` from the `api:` section (v1 location), injects it as `admin_api_key` under `global:` (v2 location), and logs the migration; if neither location has a value, a new secure key is generated; if `global.admin_api_key` is already present (idempotent re-runs or fresh v1.1 installs), the fix is skipped

**`spiralctl config validate` - `wallet_address` Incorrectly Flagged as Missing**
- The validator always flagged `wallet_address` as empty/missing, even when the config intentionally omits it (multi-coin mode, custom coin setups) - every validate run showed a spurious warning
- Fixed: an absent `wallet_address` key is now valid; only explicit placeholder strings (`YOUR_DGB_ADDRESS`, `YOUR_ADDRESS`, `PENDING_GENERATION`, or any value containing `YOUR`) trigger the warning

**`spiralctl config validate` - `admin_api_key` Not Detected in v1 Config Format**
- The validator checked only for `admin_api_key:` (v2 snake_case) - configs upgraded from v1.0.0 that still had `adminApiKey:` (v1 camelCase) in the `api:` section were incorrectly flagged as missing the key
- Fixed: grep pattern updated to `admin_api_key:|adminApiKey:` - both formats satisfy the check

**`spiralctl config validate` - Sentinel Config Checked When Sentinel Is Not Installed**
- On installations without Sentinel enabled, `spiralctl config validate` attempted to check `config.json` and printed misleading errors about missing Sentinel configuration
- Fixed: Sentinel config block is skipped with an informational message when `spiralsentinel.service` is not enabled

**Dashboard - Setup Page Device Type Parity**
- Setup wizard (`/setup`) now shows all 26 individual device type sections, matching the settings page - previously only 2 grouped sections (AxeOS and CGMiner API) were shown
- Each device type has its own container, add button, icon, and description
- Device scanner on setup correctly routes discovered devices to their individual sections
- `VALID_DEVICE_TYPES` and `CGMINER_DEVICE_TYPES` sets defined for consistent type handling across all JS functions
- QAxe+ correctly shares the QAxe container (special-cased throughout)

**Dashboard - Pool-Specific Statistics**
- "Miners Online" stat card now shows stratum-connected miner count (`pool_connected_miners`) as the primary number, with fleet count as secondary "(Fleet: N online)" - previously showed fleet-wide network device count which was misleading for multi-pool operators
- "Pool Hashrate" label replaces "Total Hashrate" - value already preferred pool stratum hashrate, but the label implied it was a fleet total
- "Pool Shares" in Lifetime Statistics now reads `pool_accepted_shares` directly from Prometheus (`stratum_shares_accepted_total`) - previously showed miner-reported combined total from all pools
- Hashrate sub-text fallback shows pool-connected count instead of fleet count

**Dashboard - BitAxe / NMaxe Device Separation**
- "AxeOS / NMAXE Devices" section renamed to "BitAxe Devices" on both setup and settings pages - NMaxe has its own dedicated section
- Button labels updated: "Add AxeOS Device" → "Add BitAxe Device"

**Dashboard - Theme Ambient Glow Brightness**
- Cyberpunk base CSS ambient glows brightened to match Summer Vibes blending intensity: cyan 0.08→0.22, purple 0.04→0.14, red/orange 0.03→0.10; background grid lines 0.02→0.04
- 8 themes updated: Meltdown, Chrome Warfare, Gruvbox Dark, Black Ice, Nord, Tokyo Night, Dracula, Ocean Depths

**install.sh - Scanner BitAxe / NMaxe Separation**
- `detect_miner_type()`: BitAxe variants (Supra, Ultra, Gamma, Hex) now correctly output `axeos` type - previously misclassified as `nmaxe` because both shared a single detection branch
- NMaxe detection narrowed to match only `nmaxe` string
- Manual device type selection menu: BitAxe added as option 1 (`axeos`), NMaxe as option 2, all 24 options renumbered with corrected case statement
- Initial `miners.json` template updated from 6 device types to all 26

**Dashboard - NerdQAxe++ Missing Temperature, Firmware, Frequency, Voltage, Fan Speed, Pool URL, and Best Difficulty**
- `fetch_axeos()` NMAxe detection (`isinstance(data.get('stratum'), dict)`) was too broad - NerdQAxe++ firmware v1.0.36+ includes a `stratum` object in its `/api/system/info` response, causing it to be misclassified as NMAxe
- NMAxe branch reads different field names: `asicTemp` instead of `temp`, `fwVersion` instead of `version`, `freqReq` instead of `frequency`, `fans[0].rpm` instead of `fanspeed`, `hostName` instead of `hostname`, `bestDiffEver` instead of `bestDiff`, `stratum.used.url` instead of `stratumURL:stratumPort`
- All fields returned `0`/`Unknown`/empty, causing the dashboard to show `--` for temperature, firmware, frequency, voltage, fan speed, best difficulty, and pool URL on all NerdQAxe++ devices
- Fixed: NMAxe detection now requires `asicTemp` field presence alongside the `stratum` dict check - devices with a `stratum` object but standard AxeOS field names correctly fall through to the standard path

**Dashboard - Miners Online Showed Fleet Count Instead of Pool-Connected Count**
- "Miners Online" displayed `totals.online_count` (all configured devices responding on the network) instead of `data.pool_connected_miners` (miners with active stratum sessions on this pool)
- Multi-pool operators saw all 7 network miners as "online" even when only 1 was connected to this pool's stratum

**Dashboard - Lifetime Pool Shares Showed Miner-Reported Fleet Total**
- `lifetime.total_pool_shares || lifetime.total_shares` used JS `||` which treats `0` as falsy - `total_pool_shares` started at `0` (new field), so it always fell through to `total_shares` (miner-reported combined total from all pools)
- Fixed: uses explicit `> 0` checks and reads `data.pool_accepted_shares` (live Prometheus value) as primary source

**Dashboard - 90-Second Delay Before Miners Appear After Setup**
- `miner_cache["last_update"]` was initialized to `time.time()` at startup, making an empty cache appear fresh for 90 seconds
- First dashboard load after setup showed "No Devices Configured" until the cache expired
- Fixed: initialized to `0`; config save endpoint also resets to `0` for immediate re-fetch

**Dashboard - Settings Gear Icon Not Centered**
- Settings button (`⚙`) used padding-only centering on an `<a>` tag - emoji glyph rendered off-center due to uneven Unicode metrics
- Fixed: explicit `display: inline-flex; align-items: center; justify-content: center` with fixed dimensions

**install.sh - BitAxe Devices Misclassified as NMaxe by Scanner**
- `detect_miner_type()` lumped BitAxe and NMaxe into a single branch matching `nmaxe|bitaxe|supra|ultra|gamma|hex` - all BitAxe variants were tagged `nmaxe`
- Fixed: NMaxe matches only on `nmaxe`; BitAxe variants match on `bitaxe|supra|ultra|gamma|hex` and output `axeos`

**install.sh - Manual Device Type Menu Had Duplicate Number and Missing BitAxe Option**
- Menu items 16 and 17 were both numbered `17)` (ebang and gekkoscience); BitAxe (`axeos`) was not listed as a selectable option at all
- Fixed: BitAxe added as option 1, all 24 options renumbered sequentially with matching case statement

**Sentinel - `global _stratum_down_alerted` Syntax Error on Startup**
- Redundant `global _stratum_down_alerted` declaration in `check_pool_status()` at line 17977 - the variable was already declared global at line 17960 in the same function scope
- Python 3 treats a `global` declaration after any use of the variable name in the same scope as a `SyntaxError`, causing Sentinel to crash-loop immediately on startup
- Fixed: removed the redundant `global` statement

**`upgrade.sh` - Service Drain Loop Exited Immediately for "deactivating" Services**
- `systemctl is-active --quiet` returns exit code 3 for the `deactivating` state (not just `inactive`) - the drain loop's boolean check treated "deactivating" as "not active" and exited at `wait_count=0`
- With the loop exiting immediately, `start_services()` ran against a still-deactivating service, causing stratum and sentinel to fail to start after every upgrade
- Fixed: drain loop now captures the actual state string via `systemctl is-active` and only breaks on `inactive` or `failed` - `deactivating` and `activating` states are correctly waited out

**`upgrade.sh` - `systemctl is-active` Capture Patterns Incompatible with `set -e`**
- Three locations used `$(systemctl is-active "$service" 2>/dev/null || echo "unknown")` - `systemctl is-active` prints its state to stdout even on non-zero exit, so `|| echo` appended `"unknown"` on a new line, producing multiline values that broke status display and comparisons
- Removing `|| echo` fixed the multiline issue but exposed the non-zero exit code to `set -e` (enabled at line 100), which killed the entire upgrade script mid-run
- Fixed: all four locations (drain loop ×2, status verification, final display) now use `svc_state=$(systemctl is-active "$service" 2>/dev/null) || true` - `|| true` outside `$()` suppresses `set -e` without appending to stdout

**`upgrade.sh` - `migrate_coin_version_cache()` Wrote Target Version Instead of Installed Version**
- Fixed: renamed `_VC_VER` (target versions) to `_VC_PREV` (v1.0 shipped versions) with ; function now tries `--version` detection first and falls back to `_VC_PREV` only when detection fails

**`coin-upgrade.sh` - False Version Warning for Daemons Without Parseable `--version` Output**

**`coin-upgrade.sh` - Garbled Backup Path Display**
- `backup_coin()` used `log_success` (stdout) for progress messages inside a function whose stdout was captured by `backup_path=$(backup_coin "$coin")` - log messages were concatenated into the backup path variable
- Fixed: all log messages inside `backup_coin()` redirected to stderr (`>&2`)

**`coin-upgrade.sh` - CLI Calls Missing `-conf` Flag (Wrong RPC Port)**
- Fixed: added `COIN_CONF` map and `get_coin_cli()` helper; all CLI calls now include `-conf=<path>` matching the patterns in install.sh's `get_cli_cmd()`; multi-disk (`CHAIN_MOUNT_POINT`) supported

**Dashboard - Pool Hashrate Showed Farm Hashrate When No Miners Connected**
- "Pool Hashrate" stat card fell back to farm device hashrate (`farmHashrateThs`) when stratum-reported pool hashrate was 0 - a fresh install with 7 fleet miners configured but none connected to the pool showed 32 TH/s under "Pool Hashrate"
- Fixed: when `pool_connected_miners` is 0, the display shows 0 instead of falling back to farm hashrate

**Sentinel - Pool Block Counter Reset After Database Restore**
- `_init_state()` seeded `pool_blocks_found` from the database API, but `load()` ran immediately after and overwrote it with the stale value from `state.json` - after a database restore importing historical blocks, Discord notifications showed "Block #17" instead of "#643"
- Fixed: API re-seeding moved into `load()` after state.json is applied; uses `max(state_value, db_count)` so database restores, fresh installs, and normal restarts all produce the correct count

### Changed

- Version strings updated throughout: `1.0.0 / BLACKICE` → `1.1.0 / PHI_FORGE`
- Sentinel `hashrate_crash` alert cooldown increased from 1 hour to 6 hours - reduces repeated notifications during sustained network hashrate drops
- HA role change debounce changed from cycle-based (1 × 30s poll) to timestamp-based (configurable, default 90s) - suppresses longer VRRP election blips that the old debounce missed
- Dashboard "Total Hashrate" stat label renamed to "Pool Hashrate" for clarity

---

## [1.0.0] - BlackICE

> *Initial release.*

### Added

**Core Stratum Engine**
- Stratum V1, V2 (Noise Protocol encryption), and TLS - multi-port per coin
- SHA-256d and Scrypt algorithm support with dedicated difficulty profiles per algorithm
- Lock-free share pipeline: ring buffer (1M capacity, MPSC) → WAL → PostgreSQL COPY batch insert
- Per-session atomic vardiff state; asymmetric ramp limits (4× up / 0.75× down); 50% variance floor
- Non-custodial solo payout: block reward embedded directly in the coinbase transaction to the miner's wallet - no pool wallet, no intermediate custody, no fees

**Spiral Router - Miner Classification**
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
- Advisory lock payment fencing - prevents double-payment during failover
- `spiralpool-ha-watcher.service` - manages Sentinel start/stop based on HA role

**Spiral Sentinel**
- Autonomous monitoring daemon: device discovery, connection tracking, hashrate monitoring, temperature alerts, block find notifications
- Quiet hours: configurable suppression window (default 22:00–06:00)
- Scheduled reports: configurable intervals plus a final pre-quiet-hours report
- SimpleSwap swap alerts: optional notification when a mined coin rises 25%+ vs BTC over 7 days, with pre-filled conversion link (operator-initiated only - no automatic swaps)
- Achievement system, miner nicknames, and historical stats

**Spiral Dash**
- Real-time web dashboard on port 1618
- Multi-theme support
- Per-miner worker statistics, block history, hashrate charts

**`spiralctl` CLI**
- Runtime operator control: coin management, pool status, miner listing, difficulty inspection, maintenance mode, HA management, GDPR/data purge, Tor control
- `spiralctl add-coin` - onboarding automation for NET NEW unsupported coins

**Installer (`install.sh`)**
- Two deployment paths: native/VM and Docker bare-metal
- Docker existing-install detection (`detect_existing_docker_install()`) - reads `docker/.env`, offers Add Coins vs Fresh Install
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

*Spiral Pool - BSD-3-Clause - Non-Custodial - Solo Mining - Proof-of-Work*
