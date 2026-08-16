#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# coin-upgrade.sh — Spiral Pool Coin Daemon Upgrade Utility
#                   V2.7.0-SPIRAL_CITADEL
#
# Upgrades coin node binaries in-place. Touches ONLY the binary for every coin,
# wallets/blockchain data/pool settings are NEVER deleted.
# EXCEPTION: the 9.26.x → v9.26.5 DigiByte upgrade offers to switch the node to a
# pruned node (v9.26.4+ makes DigiDollar work while pruned). If accepted it edits
# digibyte.conf in place (sets prune=5000, removes txindex) after backing it up —
# no chain data is deleted and no resync is required. Declining leaves it full.
#
# This is a MANUAL, OPERATOR-INITIATED operation — never automated by upgrade.sh
# or Sentinel auto-update. Coin daemon upgrades may require a full chain reindex
# and must be supervised.
#
# Usage:
#   sudo /spiralpool/scripts/coin-upgrade.sh                    # Interactive mode
#   sudo /spiralpool/scripts/coin-upgrade.sh --check            # Show version status only, no changes
#   sudo /spiralpool/scripts/coin-upgrade.sh --coin NMC --reindex   # Upgrade + start with -reindex
#
# Risk levels:
#   PATCH   Binary swap only — reindex not expected
#   MINOR   Reindex may be needed — check release notes
#   MAJOR   Reindex almost certainly required — use --reindex flag
#
# Namecoin note: nc30.2 exists on GitHub but has ZERO published binary assets.
# The NMC entry reflects the last installable version (28.0). Update when
# namecoin.org publishes nc30.2 binaries.

# ── CRLF self-heal (identical to install.sh / upgrade.sh) ─────────────────────
chmod +x "${BASH_SOURCE[0]}" 2>/dev/null || true
head -c50 "$0"|od -c|grep -q '\\r'&&{ find "$(dirname "$0")" -type f \( -name "*.sh" \) -exec sed -i 's/\r$//' {} +;exec bash "$0" "$@"; } #

set -euo pipefail

# ═══════════════════════════════════════════════════════════════════════════════
# CONSTANTS
# ═══════════════════════════════════════════════════════════════════════════════

INSTALL_DIR="/spiralpool"
POOL_USER="spiraluser"
ENV_FILE="$INSTALL_DIR/config/coins.env"
BACKUP_ROOT="$INSTALL_DIR/backups/coin-upgrades"
WORK_DIR=$(mktemp -d /tmp/spiral-coin-upgrade-XXXXXX)
MAINTENANCE_SCRIPT="$INSTALL_DIR/scripts/linux/maintenance-mode.sh"
MAINTENANCE_ENABLED=false
# Services this run has CONFIRMED stopped and has not restarted. The EXIT trap
# reports them so a failure never leaves a node silently down.
#
# An array, not a scalar: "upgrade all" runs several coins in one invocation, and
# a single variable meant the second coin's successful start erased the first
# coin's pending warning — the run would end with a node down and say nothing.
#
# Entries are added only once a stop is VERIFIED. Marking on entry to the stop
# routine meant the failure paths — where the daemon is by definition still
# running — reported it as stopped.
STOPPED_SVCS=()

_mark_stopped() {
    local s
    for s in ${STOPPED_SVCS+"${STOPPED_SVCS[@]}"}; do
        [[ "$s" == "$1" ]] && return 0
    done
    STOPPED_SVCS+=("$1")
}

# One-shot systemd drop-in that forces -reindex. Recorded here so the EXIT trap
# can remove it however the run ends: if it survives, every future restart
# reindexes, and with Restart=always that never terminates.
REINDEX_DROPIN=""


_mark_started() {
    local -a _keep=() s
    for s in ${STOPPED_SVCS+"${STOPPED_SVCS[@]}"}; do
        [[ "$s" == "$1" ]] || _keep+=("$s")
    done
    STOPPED_SVCS=(${_keep+"${_keep[@]}"})
}

# Multi-disk: read CHAIN_MOUNT_POINT from coins.env if present
CHAIN_MOUNT_POINT=""
if [[ -f "$ENV_FILE" ]]; then
    CHAIN_MOUNT_POINT=$(grep -oP '^CHAIN_MOUNT_POINT="?\K[^"]+' "$ENV_FILE" 2>/dev/null || echo "")
fi

# ── Target versions — keep in sync with install.sh lines 41-46 ────────────────
declare -A COIN_TARGET=(
    [BTC]="31.1"            # Bitcoin Core — see download_BTC() for why not Knots
    [BCH]="29.1.0"
    [BCH2]="27.0.2"         # Bitcoin Cash II — binary release
    [BC2]="29.1.0"
    # Full 40-hex commit id. `git fetch --depth=1 origin <short>` fails —
    # the wire protocol will not resolve an abbreviated object id
    # ("fatal: couldn't find remote ref ff5c3c3"), so the source build
    # could never run.
    [BTCS]="source-ff5c3c3d381fa3c783862768d5a2e4fbb50f0931"
    [DGB]="9.26.5"
    [LTC]="0.21.5.6"
    [DOGE]="1.14.9"
    [PEP]="1.1.0"
    [CAT]="2.1.1"
    [NMC]="28.0"        # newest INSTALLABLE. No namecoin-core GitHub release ships
                        # binaries (nc28.0/nc31.0/nc31.1 all have empty asset lists);
                        # namecoin.org publishes binaries only up to 28.0. Latest
                        # source release is nc31.1 (2026-07-13).
    [SYS]="5.1.0"
    [XMY]="0.18.1.0"
    [FBTC]="0.3.0"
    [XEC]="0.33.10"   # ecash-node (Bitcoin ABC)
)

# Risk classification for this upgrade cycle.
# Update alongside COIN_TARGET when new versions become available.
# NONE   = already at target (or no upgrade available)
# PATCH  = binary-only change; no reindex expected
# MINOR  = may need reindex; check release notes
# MAJOR  = reindex almost certainly required; use --reindex
declare -A COIN_RISK=(
    [BTC]="MAJOR"   # Bitcoin Knots → Bitcoin Core 31.1. Knots builds carrying the
                    # knots20260508 datestamp enforce BIP-110 (RDTS) and follow the
                    # minority chain that split at block 961,632 on 2026-08-08.
                    # Blocks found there are unlikely to have value. Knots and Core
                    # share datadir format, so this is a binary swap — but the chain
                    # the node is following is re-verified afterwards.
    [BCH]="PATCH"   # 29.1.0 — RPC/perf work plus post-2026-upgrade checkpoints.
                    # 29.0.0 already carries the 15 May 2026 consensus rules, so a
                    # node on it is still valid; no reindex.
    [BCH2]="NONE"   # 27.0.2 — current, and it is the chain-split fix: v27.0.0 crashed
                    # at block 58595 on a CashToken deserialization bug and produced a
                    # shadow chain. A node still on 27.0.0/27.0.1 needs -reindex or a
                    # resync, not just this binary swap.
    [BC2]="NONE"    # 29.1.0 — current
    [BTCS]="NONE"   # source build — pinned commit ff5c3c3
    [DGB]="MINOR"   # 9.26.5 — fixes the DigiDollar oracle startup scan (9.26.4 re-ran the BIP9
                    # state machine per block, hanging init for 15+ min). Nodes still on 9.26.3
                    # also cross 9.26.4's narrowly-scoped consensus rule, so this stays MINOR.
                    # In-place binary swap, no reindex. Optional pruning (one-time offer).
    [LTC]="MAJOR"   # CONSENSUS. 0.21.5.6 adds a soft-forking rule active at mainnet
                    # height 3,154,440: an MWEB block whose kernel signals a pegout
                    # with an empty pegout list is rejected. That height has PASSED.
                    # 0.21.5.5 (also skipped by the old 0.21.5.4 pin) added consensus
                    # parameters for frozen/approved MWEB outputs. A pool still on
                    # 0.21.5.4 can produce blocks upgraded nodes reject. No reindex.
    [DOGE]="NONE"   # 1.14.9 — current
    [PEP]="NONE"    # 1.1.0  — current
    [CAT]="NONE"    # 2.1.1 — current. Note the release zip is FLAT (no bin/); see
                    # download_CAT, which repacks it into the expected layout.
    [NMC]="NONE"    # 28.0   — nc31.0 has no binaries yet
    [SYS]="MAJOR"   # CONSENSUS, WITH A DEADLINE. 5.1.0 "Liberty" is a mandatory
                    # coordinated upgrade at mainnet Core height 2,292,816, paired
                    # with sysgeth at NEVM block 975,316. A 5.0.5 node that crosses
                    # that height forks off the network — the same class of failure
                    # as the BTC/BIP-110 split. sysgeth ships inside bin/ in the
                    # release tarball, so Core and geth upgrade as a matched pair.
    [XMY]="NONE"    # 0.18.1.0 — current (project dormant since 2020)
    [FBTC]="MINOR"  # DEADLINE APPROACHING, pin deliberately NOT bumped. 0.4.0 is a
                    # consensus-tightening hard fork at height 2,100,000 (ETA early
                    # Sep 2026) with an extra one-time halving — but upstream still
                    # labels it a Release Candidate dated "September 2026". Pinning an
                    # RC is worse than being briefly behind. WATCH THIS: once 0.4.0 is
                    # final, bump to it and set MAJOR before the activation height.
    [XEC]="MAJOR"   # CONSENSUS, TWICE. The old 0.31.12 pin predates the 15 Nov 2025
                    # upgrade (64-bit script integers, Heartbeat difficulty adjustment,
                    # Avalanche Pre-Consensus; ABC states nodes MUST be on 0.32.x before
                    # activation) and the 15 May 2026 upgrade in 0.33.0. A node on
                    # 0.31.12 cannot be following eCash mainnet. Expect a long catch-up.
)

# systemd service unit names
declare -A COIN_SERVICE=(
    [BTC]="bitcoind"        [BCH]="bitcoind-bch"    [BCH2]="bitcoincashIId"
    [BC2]="bitcoiniid"      [BTCS]="bitcoinsilverd"
    [DGB]="digibyted"       [LTC]="litecoind"       [DOGE]="dogecoind"
    [PEP]="pepecoind"       [CAT]="catcoind"        [NMC]="namecoind"
    [SYS]="syscoind"        [XMY]="myriadcoind"     [FBTC]="fractald"
    [XEC]="ecashd"
)

# /usr/local/bin daemon command (symlink name)
declare -A COIN_DAEMON_CMD=(
    [BTC]="bitcoind"        [BCH]="bitcoind-bch"    [BCH2]="bitcoincashIId"
    [BC2]="bitcoiniid"      [BTCS]="bitcoinsilverd"
    [DGB]="digibyted"       [LTC]="litecoind"       [DOGE]="dogecoind"
    [PEP]="pepecoind"       [CAT]="catcoind"        [NMC]="namecoind"
    [SYS]="syscoind"        [XMY]="myriadcoind"     [FBTC]="fractald"
    [XEC]="ecashd"
)

# /usr/local/bin CLI command (symlink name)
declare -A COIN_CLI_CMD=(
    # BCH2 is the LOWERCASE symlink: install.sh links
    # $BCH2_DIR/bin/bitcoincashII-cli to /usr/local/bin/bitcoincashii-cli, so
    # the capital-II name is not on PATH.
    [BTC]="bitcoin-cli"     [BCH]="bitcoin-cli-bch"    [BCH2]="bitcoincashii-cli"
    [BC2]="bitcoinii-cli"   [BTCS]="bitcoinsilver-cli"
    [DGB]="digibyte-cli"    [LTC]="litecoin-cli"       [DOGE]="dogecoin-cli"
    [PEP]="pepecoin-cli"    [CAT]="catcoin-cli"        [NMC]="namecoin-cli"
    [SYS]="syscoin-cli"     [XMY]="myriadcoin-cli"     [FBTC]="fractal-cli"
    [XEC]="ecash-cli"
)

# Conf file path per coin (required for CLI calls — each coin uses a non-default RPC port)
# Multi-disk support: check CHAIN_MOUNT_POINT first, fall back to INSTALL_DIR
_chain_dir() {
    local coin_lower="$1"
    if [[ -n "${CHAIN_MOUNT_POINT:-}" && -d "${CHAIN_MOUNT_POINT}/${coin_lower}" ]]; then
        echo "${CHAIN_MOUNT_POINT}/${coin_lower}"
    else
        echo "${INSTALL_DIR}/${coin_lower}"
    fi
}
declare -A COIN_CONF=(
    [BTC]="$(_chain_dir btc)/bitcoin.conf"
    [BCH]="$(_chain_dir bch)/bitcoin.conf"
    [BCH2]="$(_chain_dir bch2)/bitcoincashii.conf"
    [BC2]="$(_chain_dir bc2)/bitcoinii.conf"
    [BTCS]="$(_chain_dir btcs)/bitcoinsilver.conf"
    [DGB]="$(_chain_dir dgb)/digibyte.conf"
    [LTC]="$(_chain_dir ltc)/litecoin.conf"
    [DOGE]="$(_chain_dir doge)/dogecoin.conf"
    [PEP]="$(_chain_dir pep)/pepecoin.conf"
    [CAT]="$(_chain_dir cat)/catcoin.conf"
    [NMC]="$(_chain_dir nmc)/namecoin.conf"
    [SYS]="$(_chain_dir sys)/syscoin.conf"
    [XMY]="$(_chain_dir xmy)/myriadcoin.conf"
    [FBTC]="$(_chain_dir fbtc)/fractal.conf"
    [XEC]="$(_chain_dir xec)/bitcoin.conf"
)

# Build full CLI command with -conf flag
get_coin_cli() {
    local coin="$1"
    echo "${COIN_CLI_CMD[$coin]} -conf=${COIN_CONF[$coin]}"
}

# .env flag to check if a coin is enabled on this node
declare -A COIN_ENV_FLAG=(
    [BTC]="ENABLE_BTC"   [BCH]="ENABLE_BCH"   [BCH2]="ENABLE_BCH2"
    [BC2]="ENABLE_BC2"   [BTCS]="ENABLE_BTCS"
    [DGB]="ENABLE_DGB"   [LTC]="ENABLE_LTC"   [DOGE]="ENABLE_DOGE"
    [PEP]="ENABLE_PEP"   [CAT]="ENABLE_CAT"   [NMC]="ENABLE_NMC"
    [SYS]="ENABLE_SYS"   [XMY]="ENABLE_XMY"   [FBTC]="ENABLE_FBTC"
    [XEC]="ENABLE_XEC"
)

ALL_COINS=(BTC BCH BCH2 BC2 BTCS DGB LTC DOGE PEP CAT NMC SYS XMY FBTC XEC)

# ═══════════════════════════════════════════════════════════════════════════════
# COLORS
# ═══════════════════════════════════════════════════════════════════════════════
RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'
WHITE='\033[1;37m'; MAGENTA='\033[0;35m'; DIM='\033[2m'; NC='\033[0m'; BOLD='\033[1m'

# ═══════════════════════════════════════════════════════════════════════════════
# LOGGING
# ═══════════════════════════════════════════════════════════════════════════════
log()         { echo -e "  ${DIM}$(date '+%H:%M:%S')${NC}  $*"; }
log_info()    { echo -e "  ${CYAN}ℹ${NC}  $*"; }
log_success() { echo -e "  ${GREEN}✓${NC}  $*"; }
log_warn()    { echo -e "  ${YELLOW}⚠${NC}  $*"; }
log_error()   { echo -e "  ${RED}✗${NC}  $*" >&2; }
log_step()    { echo -e "\n${CYAN}━━━${NC} ${WHITE}${BOLD}$*${NC}"; }

die() { log_error "$*"; cleanup; exit 1; }

# ═══════════════════════════════════════════════════════════════════════════════
# CLEANUP
# ═══════════════════════════════════════════════════════════════════════════════
cleanup() {
    rm -rf "$WORK_DIR" 2>/dev/null || true
    rm -rf /tmp/btcs-build 2>/dev/null || true
    if [[ "$MAINTENANCE_ENABLED" == "true" ]]; then
        disable_maintenance 2>/dev/null || true
    fi

    # An upgrade stops the daemon before swapping the binary. Several failure
    # paths (`die` on a malformed archive, an unresolvable binary directory)
    # exit straight through this trap, and previously said nothing about the
    # node being left DOWN — the operator saw a one-line error and no
    # indication that mining had stopped or how to resume it.
    if [[ -n "${REINDEX_DROPIN:-}" && -f "$REINDEX_DROPIN" ]]; then
        sudo rm -f "$REINDEX_DROPIN" 2>/dev/null || true
        sudo systemctl daemon-reload 2>/dev/null || true
        log_warn "Removed the one-shot reindex drop-in left by this run."
        log_warn "The next start will be a normal one, not a reindex."
        REINDEX_DROPIN=""
    fi

    local _svc
    for _svc in ${STOPPED_SVCS+"${STOPPED_SVCS[@]}"}; do
        echo ""
        log_warn "${_svc} was stopped for this upgrade and has NOT been restarted."
        log_warn "Check what state the binary is in before starting it:"
        echo -e "  ${CYAN}systemctl status ${_svc} --no-pager${NC}"
        echo -e "  ${CYAN}sudo systemctl start ${_svc}${NC}"
    done
    # Cleared after reporting: die() calls cleanup explicitly and then exits,
    # which re-fires the EXIT trap, so without this the whole block prints twice.
    STOPPED_SVCS=()
}
trap cleanup EXIT

# ═══════════════════════════════════════════════════════════════════════════════
# UTILITIES
# ═══════════════════════════════════════════════════════════════════════════════

check_root() {
    [[ "$EUID" -eq 0 ]] || die "Must be run as root:  sudo /spiralpool/scripts/coin-upgrade.sh $*"
}

get_system_arch() {
    echo "x86_64"
}

is_coin_enabled() {
    local flag="${COIN_ENV_FLAG[$1]:-}"
    [[ -z "$flag" || ! -f "$ENV_FILE" ]] && return 1
    local val
    val=$(grep -oP "^${flag}=\K(true|false)" "$ENV_FILE" 2>/dev/null || echo "false")
    [[ "$val" == "true" ]]
}

# Resolve /usr/local/bin symlink → real binary path
get_binary_path() {
    readlink -f "/usr/local/bin/${COIN_DAEMON_CMD[$1]}" 2>/dev/null || echo ""
}

get_binary_dir() {
    local p
    p=$(get_binary_path "$1")
    [[ -n "$p" ]] && dirname "$p" || echo ""
}

VERSION_CACHE_DIR="${INSTALL_DIR}/config/coin-versions"

get_installed_version() {
    local coin="$1"
    local bin_path
    bin_path=$(get_binary_path "$coin")
    [[ -z "$bin_path" || ! -x "$bin_path" ]] && echo "not_installed" && return

    # A source-commit pin is the one case where the cache must stay
    # authoritative: COIN_TARGET[BTCS] is "source-<40-hex commit>", and a
    # compiled binary cannot report a commit hash from --version, so the cache
    # written after a successful upgrade is the only record of what is
    # installed. Asking the binary first here would report "update available"
    # permanently, immediately after upgrading.
    local cache_file="${VERSION_CACHE_DIR}/${coin}.ver"
    case "${COIN_TARGET[$coin]:-}" in
        source-*)
            # -s, not -f: a truncated write (disk full, crash mid-`echo >`)
            # leaves a zero-byte file, and `cat` on it returned an empty string
            # rather than any of this function sentinels.
            if [[ -s "$cache_file" ]]; then
                cat "$cache_file"
                return
            fi
            ;;
    esac

    # ASK THE BINARY FIRST for everything else. The cache exists only for
    # daemons whose --version omits the number; consulting it first let it
    # shadow reality for every coin. Concretely: BTC.ver="31.1" is written by an
    # earlier run, then a rollback — or a hand restore following the command
    # this script itself prints on failure — puts a Bitcoin Knots binary back on
    # disk. get_installed_version still answered 31.1, upgrade_coin took its
    # "already current" shortcut, --check printed a green tick, and the node
    # went on following the BIP-110 minority chain. That is the silent failure
    # this whole release exists to remove, reproduced inside the tool meant to
    # remediate it.
    #
    # The version string deliberately keeps its build suffix (29.3.knots20260210)
    # so a Knots build can never compare equal to a Core release.
    local raw ver
    raw=$("$bin_path" --version 2>/dev/null | head -1)
    if [[ -n "$raw" ]]; then
        # Extracts versions like 29.3.knots20260210, 0.21.4, 0.2.0, 8.26.2, 0.18.1.0
        ver=$(echo "$raw" | grep -oP '(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*' | head -1)
        if [[ -n "$ver" ]]; then
            echo "$ver"
            return
        fi
    fi

    # Only now the cache: the binary either could not run or printed a version
    # string with no number in it, which is exactly what the cache is for.
    # -s, not -f — see above.
    if [[ -s "$cache_file" ]]; then
        cat "$cache_file"
        return
    fi

    [[ -n "$raw" ]] && echo "unknown" || echo "error"
}

write_version_cache() {
    local coin="$1" ver="$2"
    mkdir -p "$VERSION_CACHE_DIR"
    echo "$ver" > "${VERSION_CACHE_DIR}/${coin}.ver"
}

# Parse -datadir= from the systemd ExecStart of the coin's service
get_data_dir() {
    local svc="${COIN_SERVICE[$1]}.service"
    systemctl cat "$svc" 2>/dev/null \
        | grep -oP '\-datadir=\K\S+' \
        | head -1 \
        || echo ""
}

enable_maintenance() {
    if [[ -x "$MAINTENANCE_SCRIPT" ]]; then
        "$MAINTENANCE_SCRIPT" enable 60 "coin-upgrade" 2>/dev/null || true
        MAINTENANCE_ENABLED=true
        log_info "Maintenance mode enabled — Discord alerts suppressed"
    fi
}

disable_maintenance() {
    if [[ -x "$MAINTENANCE_SCRIPT" ]]; then
        "$MAINTENANCE_SCRIPT" disable "coin-upgrade" 2>/dev/null || true
        MAINTENANCE_ENABLED=false
        log_info "Maintenance mode disabled"
    fi
}

wait_for_daemon() {
    local coin="$1"
    local cli; cli=$(get_coin_cli "$coin")
    # DGB reloads a ~24M-entry block index before it opens RPC, which takes 4-5 minutes
    # on ordinary hardware — a flat 120s budget always expired and printed a "did not
    # respond" warning on a perfectly healthy upgrade. Give the slow starter room.
    local budget=120
    [[ "$coin" == "DGB" ]] && budget=600
    local deadline=$(( SECONDS + budget ))
    log_info "Waiting for ${coin} daemon to respond (up to ${budget}s)..."
    local out last_msg=""
    while [[ $SECONDS -lt $deadline ]]; do
        if out=$($cli getblockchaininfo 2>&1); then
            log_success "${coin} daemon is responding"
            return 0
        fi
        # A daemon still in init answers RPC with error -28 and a stage name
        # ("Loading block index…", "Verifying blocks…", "Pruning blockstore…").
        # Echo each new stage so the wait shows progress instead of looking hung.
        local msg
        # `|| true` is load-bearing under `set -euo pipefail`. When a daemon's only
        # output is a bare "error code: -28" with no stage line, grep -v matches
        # nothing and returns 1; the pipeline inherits it and this bare assignment
        # kills the whole script — silently, right after "Start daemon" and BEFORE
        # the BTC chain verification below ever runs.
        msg=$(printf '%s' "$out" | grep -vi '^error code' | tr -d '\r' | tail -1 || true)
        if [[ -n "$msg" && "$msg" != "$last_msg" ]]; then
            log_info "  ${coin}: ${msg}"
            last_msg="$msg"
        fi
        sleep 3
    done
    log_warn "${coin} did not respond within ${budget}s — may still be starting or reindexing"
    return 0  # non-fatal; operator can monitor manually
}

# ═══════════════════════════════════════════════════════════════════════════════
# DOWNLOAD FUNCTIONS
# Each function: cd into WORK_DIR, download, extract, echo the extracted dir name.
# Caller uses the returned dir name to locate binaries.
# ═══════════════════════════════════════════════════════════════════════════════

_wget() {
    wget -q --show-progress --tries=3 --timeout=60 "$@"
}

# Bitcoin Core 31.1 release checksum, x86_64-linux-gnu tarball.
#
# Provenance: https://bitcoincore.org/bin/bitcoin-core-31.1/SHA256SUMS
# Cross-verified byte-for-byte against two independent Guix attestations in
# https://github.com/bitcoin-core/guix.sigs (signers achow101 and fanquake).
# All three sources agree.
#
# This is Bitcoin Core, not Bitcoin Knots. Knots builds whose version carries the
# knots20260508 datestamp enforce BIP-110 (RDTS) and follow the minority chain
# that split from Bitcoin at block 961,632 on 2026-08-08. Do not reintroduce a
# Knots download here, and do not resolve this version dynamically — a
# latest-resolver is exactly how an operator drifts onto an enforcing build.
BITCOIN_CORE_SHA256="b80d9c3e04da78fb6f0569685673418cf686fadba9042d926d13fb87ff503f9e"

download_BTC() {
    local arch="$1" ver="${COIN_TARGET[BTC]}"
    local sfx="x86_64-linux-gnu"
    local fn="bitcoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://bitcoincore.org/bin/bitcoin-core-${ver}/${fn}" || return 1

    # Verification is mandatory. An unverified BTC daemon is how an operator ends
    # up mining a chain nobody settles against, so a mismatch aborts the upgrade
    # rather than falling through to install anyway.
    local actual
    # Guarded: under `set -euo pipefail` a bare assignment from a failing
    # pipeline exits the whole script instead of returning into the caller's
    # rollback path.
    actual=$(sha256sum "$fn" 2>/dev/null | awk '{print $1}') || {
        log_error "Could not compute a checksum for ${fn} — refusing to install"
        return 1
    }
    if [[ "$actual" != "$BITCOIN_CORE_SHA256" ]]; then
        log_error "SHA256 mismatch for ${fn} — refusing to install"
        log_error "  expected: ${BITCOIN_CORE_SHA256}"
        log_error "  actual:   ${actual}"
        return 1
    fi
    # MUST go to stderr: this function returns the extracted directory name via
    # stdout, and log_success writes to stdout. Anything printed here without
    # >&2 is captured into the caller's $extracted_dir and corrupts the path.
    log_success "SHA256 verified for ${fn}" >&2

    tar -xzf "$fn" || return 1
    echo "bitcoin-${ver}"
}

download_BCH() {
    local arch="$1" ver="${COIN_TARGET[BCH]}"
    local sfx="x86_64-linux-gnu"
    local fn="bitcoin-cash-node-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/bitcoin-cash-node/bitcoin-cash-node/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "bitcoin-cash-node-${ver}"
}

download_BCH2() {
    local arch="$1" ver="${COIN_TARGET[BCH2]}"
    # Asset name has NO "v" prefix and uses "linux64", not "linux-x86_64".
    # The old form 404'd, so this upgrade could never run — which matters more
    # than usual: v27.0.2 is a chain-split fix (a CashToken deserialization bug
    # crashed v27.0.0 nodes at block 58595 and produced a shadow chain), and
    # v27.0.0/v27.0.1 were replaced upstream with DO-NOT-USE placeholders.
    local fn="bitcoincashII-${ver}-linux64.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/BitcoincashII/bitcoincashII-core/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    ls -d bitcoincashII-* 2>/dev/null | head -1 || echo "."
}

download_BC2() {
    local arch="$1" ver="${COIN_TARGET[BC2]}"
    local sfx="x86_64-linux-CLI"
    local fn="BitcoinII-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/Bitcoin-II/BitcoinII-Core/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    ls -d BitcoinII-* 2>/dev/null | head -1 || echo "."
}

download_BTCS() {
    # Bitcoin Silver must be built from source — pinned to verified commit
    local commit="${COIN_TARGET[BTCS]##*-}"  # extract hash after "source-"
    local build_dir="$WORK_DIR/bitcoinsilver"
    mkdir -p "$build_dir"
    cd "$build_dir"
    git init && git remote add origin https://github.com/bitcoin-silver/core.git
    git fetch --depth=1 origin "$commit" || return 1
    git checkout FETCH_HEAD
    ./autogen.sh && ./configure --disable-tests --disable-bench --without-gui --without-miniupnpc --prefix=/tmp/btcs-build
    make -j"$(nproc)" && make install
    echo "/tmp/btcs-build"
}

download_DGB() {
    local arch="$1" ver="${COIN_TARGET[DGB]}"
    local sfx="x86_64-linux-gnu"
    local fn="digibyte-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/DigiByte-Core/digibyte/releases/download/v${ver}/${fn}" 2>/dev/null \
        || _wget -O "$fn" "https://github.com/digibyte/digibyte/releases/download/v${ver}/${fn}" \
        || return 1
    tar -xzf "$fn" || return 1
    echo "digibyte-${ver}"
}

download_LTC() {
    local arch="$1" ver="${COIN_TARGET[LTC]}"
    local sfx="x86_64-linux-gnu"
    local fn="litecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/litecoin-project/litecoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "litecoin-${ver}"
}

download_DOGE() {
    local arch="$1" ver="${COIN_TARGET[DOGE]}"
    local sfx="x86_64-linux-gnu"
    local fn="dogecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/dogecoin/dogecoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "dogecoin-${ver}"
}

download_PEP() {
    local arch="$1" ver="${COIN_TARGET[PEP]}"
    local sfx="x86_64-linux-gnu"
    local fn="pepecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/pepecoinppc/pepecoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "pepecoin-${ver}"
}

download_CAT() {
    local arch="$1" ver="${COIN_TARGET[CAT]}"
    local zip_name="Catcoin-Linux.zip"
    cd "$WORK_DIR"
    _wget -O catcoin.zip "https://github.com/CatcoinCore/catcoincore/releases/download/v${ver}/${zip_name}" || return 1

    # This archive is FLAT: catcoind, catcoin-cli, catcoin-tx, catcoin-wallet
    # and catcoin-qt sit at the top level, with no Catcoin-Linux/ directory and
    # no bin/. Returning a directory name that does not exist made
    # install_binaries find no bin/ and call die -- which EXITS rather than
    # returning, so rollback never ran and the daemon was left stopped.
    # Unpack into a bin/ layout so the standard install path works.
    local out="catcoin-${ver}"
    rm -rf "$out"; mkdir -p "$out/bin"
    unzip -q -j catcoin.zip -d "$out/bin" || return 1
    chmod +x "$out"/bin/* 2>/dev/null || true
    echo "$out"
}

download_NMC() {
    local arch="$1" ver="${COIN_TARGET[NMC]}"
    local sfx="x86_64-linux-gnu"
    local fn="namecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://www.namecoin.org/files/namecoin-core/namecoin-core-${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "namecoin-${ver}"
}

download_SYS() {
    local arch="$1" ver="${COIN_TARGET[SYS]}"
    local sfx="x86_64-linux-gnu"
    local fn="syscoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/syscoin/syscoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "syscoin-${ver}"
}

download_XMY() {
    local arch="$1" ver="${COIN_TARGET[XMY]}"
    local sfx="x86_64-linux-gnu"
    local fn="myriadcoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/myriadteam/myriadcoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "myriadcoin-${ver}"
}

download_FBTC() {
    local arch="$1" ver="${COIN_TARGET[FBTC]}"
    local fn="fractald-${ver}-x86_64-linux-gnu.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/fractal-bitcoin/fractald-release/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "fractald-${ver}-x86_64-linux-gnu"
}

download_XEC() {
    local arch="$1" ver="${COIN_TARGET[XEC]}"
    # ecash-node (Bitcoin ABC) — only x86_64 supported
    if [[ "$arch" != "x86_64" ]]; then
        log_error "XEC (ecash-node) only supports x86_64 — arm64 not available"
        return 1
    fi
    local fn="bitcoin-abc-${ver}-x86_64-linux-gnu.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/Bitcoin-ABC/bitcoin-abc/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "bitcoin-abc-${ver}"
}

# ═══════════════════════════════════════════════════════════════════════════════
# BINARY INSTALLATION
# Installs binaries from the extracted directory into the existing bin dir.
# Uses the same bin dir discovered via readlink — never changes dir structure.
# ═══════════════════════════════════════════════════════════════════════════════

install_binaries() {
    local coin="$1"
    local extracted="$WORK_DIR/$2"
    local bin_dir
    bin_dir=$(get_binary_dir "$coin")
    [[ -z "$bin_dir" || ! -d "$bin_dir" ]] && die "Cannot locate binary dir for ${coin}"

    log_info "Installing to ${bin_dir}..."

    case "$coin" in
        BCH2)
            # Bitcoin Cash II: capital "II" in binary names
            find "$extracted" -type f \( -name "bitcoincashIId" -o -name "bitcoincashII-cli" \) \
                -exec sudo install -m 755 -o "$POOL_USER" -g "$POOL_USER" {} "$bin_dir/" \;
            ;;
        BC2)
            # Bitcoin II: capital "II" in binary names
            find "$extracted" -type f \( -name "bitcoinIId" -o -name "bitcoinII-cli" \) \
                -exec sudo install -m 755 -o "$POOL_USER" -g "$POOL_USER" {} "$bin_dir/" \;
            ;;
        BTCS)
            # Bitcoin Silver: built from source — binaries installed to /tmp/btcs-build/bin
            sudo install -m 755 -o "$POOL_USER" -g "$POOL_USER" /tmp/btcs-build/bin/bitcoinsilverd "$bin_dir/"
            sudo install -m 755 -o "$POOL_USER" -g "$POOL_USER" /tmp/btcs-build/bin/bitcoinsilver-cli "$bin_dir/"
            ;;
        BCH|FBTC|XEC)
            # These have bin/ subdir but daemon binary is named bitcoind (not coin-specific)
            local src_bin
            src_bin=$(find "$extracted" -maxdepth 3 -type d -name "bin" | head -1)
            [[ -z "$src_bin" ]] && die "No bin/ found in ${coin} archive"
            sudo cp -r "$src_bin"/. "$bin_dir/"
            sudo chown -R "$POOL_USER:$POOL_USER" "$bin_dir"
            sudo chmod -R 755 "$bin_dir"
            ;;
        *)
            # Standard layout: extracted_dir/bin/<daemons>
            local src_bin
            src_bin=$(find "$extracted" -maxdepth 3 -type d -name "bin" | head -1)
            [[ -z "$src_bin" ]] && die "No bin/ found in ${coin} archive"
            # Unlink the existing daemon/cli first. Overwriting a binary that is
            # still mapped by a running process fails with "Text file busy"; rm
            # (unlink) always succeeds and cp then writes a fresh inode. FAIL the
            # install if the copy errors — never report success on a failed copy.
            sudo rm -f "$bin_dir/${COIN_DAEMON_CMD[$coin]}" "$bin_dir/${COIN_CLI_CMD[$coin]}" 2>/dev/null || true
            if ! sudo cp -rf "$src_bin"/. "$bin_dir/"; then
                log_error "Binary copy failed for ${coin} — is the daemon still running? ('Text file busy')"
                return 1
            fi
            sudo chown -R "$POOL_USER:$POOL_USER" "$bin_dir"
            sudo chmod -R 755 "$bin_dir"
            ;;
    esac
    log_success "Binaries installed"
}

# Verify a coin daemon PROCESS is fully stopped before we touch its binary.
# systemctl stop can return before the process actually exits (large-chain flush)
# or a supervisor can respawn it — either way the binary stays "Text file busy".
# This polls the real process, escalates SIGTERM -> SIGKILL, and RETURNS NON-ZERO
# if it cannot confirm the process is gone. It never assumes success from elapsed
# time — the verdict is always a live pgrep check.
ensure_daemon_stopped() {
    local coin="$1"
    local proc="${COIN_DAEMON_CMD[$coin]}"
    local svc="${COIN_SERVICE[$coin]}.service"

    # Identify the process by its FULL BINARY PATH, never by name.
    #
    # BTC, BCH and FBTC all exec a binary literally named "bitcoind", from three
    # different directories (see the ExecStart lines install.sh writes). `pgrep -x
    # bitcoind` matches all three, so signalling by name during a BTC upgrade
    # SIGTERMs and then SIGKILLs the operator's Bitcoin Cash node mid-UTXO-flush
    # — corrupting its chainstate — and then reports BTC "confirmed stopped"
    # because the OTHER daemon dying is what finally cleared the pgrep.
    #
    # The reverse also bites: COIN_DAEMON_CMD[BCH] is "bitcoind-bch", which is
    # the /usr/local/bin symlink name and matches no running process at all, so
    # a name check returns "stopped" on attempt 1 while the daemon is still up.
    #
    # get_binary_path resolves the symlink to the real executable, which is
    # unique per coin.
    local bin_path; bin_path=$(get_binary_path "$coin" 2>/dev/null || echo "")

    local -a match_args
    if [[ -n "$bin_path" && -x "$bin_path" ]]; then
        # Match the resolved path OR the /usr/local/bin symlink.
        #
        # Most units name the real path, but BTCS and XEC are written with
        # ExecStart=/usr/local/bin/<daemon> (see install.sh), so their
        # /proc/<pid>/cmdline begins with the SYMLINK. Matching only the
        # resolved path silently misses those two: pgrep finds nothing, the
        # function reports "confirmed stopped" on the first poll, and the
        # binary is then swapped under a live daemon — the exact chainstate
        # corruption this matcher exists to prevent.
        #
        # Both alternatives stay anchored, so a path that is a prefix of another
        # still cannot cross-match.
        match_args=(-f "^(${bin_path}|/usr/local/bin/${proc})([[:space:]]|$)")
    else
        # No resolvable binary, so there is no safe matcher left.
        #
        # An earlier version fell back to `pgrep -x "$proc"` when the name was
        # unique across configured coins. That test could never fail — the
        # COIN_DAEMON_CMD values are /usr/local/bin symlink names and are unique
        # by construction — so the fallback always ran, and it is exactly the
        # matcher the comment above explains is broken: `-x` matches the process
        # comm, which for BCH is "bitcoind", not "bitcoind-bch". It therefore
        # reported "stopped" on the first poll while the daemon was still up,
        # and the upgrade swapped the binary under a live process.
        #
        # Verify through systemd instead, which is authoritative for a unit we
        # just stopped, and refuse if even that is unavailable. Reporting an
        # unverified "stopped" is the one outcome that corrupts a chainstate.
        log_warn "Could not resolve the ${coin} binary path — verifying via systemd only."
        sudo systemctl stop "$svc" 2>/dev/null || true
        # 620s, matching the pgrep path below. A 30s ceiling here contradicted
        # the units' own TimeoutStopSec=600 and aborted the upgrade on any chain
        # large enough to take minutes to flush.
        local _w
        for _w in {1..620}; do
            if ! systemctl is-active --quiet "$svc"; then
                _mark_stopped "$svc"
                return 0
            fi
            sleep 1
        done
        log_error "${svc} is still active after 620s and the binary path could not be"
        log_error "resolved, so the process cannot be identified safely. Refusing to"
        log_error "continue — stop it manually, then re-run."
        return 1
    fi

    sudo systemctl stop "$svc" 2>/dev/null || true

    local attempt
    # Timings are matched to the unit files, which set TimeoutStopSec=600
    # because these daemons flush a LevelDB chainstate on shutdown and a large
    # chain legitimately takes minutes. Escalating to SIGKILL after ~11s
    # overrode the 600s the operator's own unit grants, and killing mid-flush
    # corrupts the chainstate -- the exact outcome this function exists to
    # prevent, on its only live code path.
    #
    # So: wait a long time, SIGTERM only after systemd's own patience would
    # have been tested, and treat SIGKILL as a genuine last resort. Total budget
    # is 620s, just past TimeoutStopSec so systemd has had its full window first.
    local _wait_quiet=300     # let a clean shutdown finish
    local _wait_term=600      # then re-send SIGTERM
    local attempt
    # pgrep must exist before its silence can mean anything. `if ! pgrep ...`
    # negates exit 127 (command not found — no procps installed) into "confirmed
    # stopped", after which the caller overwrites the binary under a live daemon.
    # rm -f unlinks an open file happily, so the "Text file busy" backstop never
    # fires either.
    if ! command -v pgrep >/dev/null 2>&1; then
        log_error "pgrep is not available, so this script cannot confirm ${svc} has stopped."
        log_error "Refusing to continue — install procps and re-run."
        return 1
    fi
    for attempt in $(seq 1 620); do
        # `cmd; rc=$?` is fatal under `set -e` when cmd returns non-zero, and
        # 1 (matched nothing) is the COMMON case here — the daemon has stopped.
        # It survives today only because both callers invoke this function as a
        # condition, which suppresses errexit for the whole call; the first
        # unconditional caller would kill the upgrade at the first poll.
        _pg=0; pgrep "${match_args[@]}" >/dev/null 2>&1 || _pg=$?
        if [[ $_pg -eq 1 ]]; then                      # 1 = ran, matched nothing
            _mark_stopped "$svc"
            return 0                                   # confirmed: process is gone
        elif [[ $_pg -gt 1 ]]; then                    # 2 = syntax, 3 = fatal
            log_error "pgrep failed (exit ${_pg}) — cannot confirm ${svc} stopped. Refusing to continue."
            return 1
        fi
        if   [[ "$attempt" -le "$_wait_quiet" ]]; then :
        elif [[ "$attempt" -le "$_wait_term" ]]; then
            sudo pkill "${match_args[@]}" 2>/dev/null || true    # SIGTERM
        else
            sudo pkill -9 "${match_args[@]}" 2>/dev/null || true # SIGKILL
        fi
        # Report progress: silence for ten minutes looks like a hang.
        if [[ $((attempt % 60)) -eq 0 ]]; then
            log_info "  still waiting for ${svc} to exit (${attempt}s; units allow 600s to flush)"
        fi
        sleep 1
    done

    # Final verdict is a check, not a timer: 0 only if truly gone.
    if ! pgrep "${match_args[@]}" >/dev/null 2>&1; then
        # Mark here too. The loop above marks on its own success path, but the
        # process can exit during the last sleep, so this is a real success
        # route -- and returning 0 unmarked leaves the EXIT trap silent about a
        # node that is down, which is what the array exists to prevent.
        _mark_stopped "$svc"
        return 0
    fi
    return 1
}

# ═══════════════════════════════════════════════════════════════════════════════
# BACKUP
# ═══════════════════════════════════════════════════════════════════════════════

backup_coin() {
    local coin="$1"
    local ts; ts=$(date '+%Y%m%d-%H%M%S')
    local backup_dir="${BACKUP_ROOT}/${coin}-${ts}"
    sudo mkdir -p "$backup_dir"

    # Backup binaries
    # Note: log messages go to stderr (&2) because stdout is captured as the return value
    local bin_dir; bin_dir=$(get_binary_dir "$coin")
    if [[ -n "$bin_dir" && -d "$bin_dir" ]]; then
        sudo cp -r "$bin_dir" "${backup_dir}/bin-backup" 2>/dev/null || true
        log_success "Binaries backed up → ${backup_dir}/bin-backup" >&2
    fi

    # Backup wallet files from data dir
    local data_dir; data_dir=$(get_data_dir "$coin")
    if [[ -n "$data_dir" && -d "$data_dir" ]]; then
        # Set _wallets_copied ONLY on a successful copy. With `|| true` and an
        # unconditional assignment, a failed cp still produced "Wallets backed
        # up" plus an scp line for a path that does not exist — the precise
        # false assurance the warning below exists to prevent.
        local _wallets_copied="" _wallets_is_dir=false
        if [[ -d "${data_dir}/wallets" ]]; then
            if sudo cp -r "${data_dir}/wallets" "${backup_dir}/wallets-backup" 2>/dev/null; then
                log_success "Wallets backed up → ${backup_dir}/wallets-backup" >&2
                _wallets_copied="${backup_dir}/wallets-backup"
                _wallets_is_dir=true
            else
                log_error "FAILED to back up ${data_dir}/wallets" >&2
                log_error "Copy it off this machine by hand before continuing." >&2
            fi
        elif [[ -f "${data_dir}/wallet.dat" ]]; then
            if sudo cp "${data_dir}/wallet.dat" "${backup_dir}/wallet.dat" 2>/dev/null; then
                log_success "wallet.dat backed up → ${backup_dir}/wallet.dat" >&2
                _wallets_copied="${backup_dir}/wallet.dat"
            else
                log_error "FAILED to back up ${data_dir}/wallet.dat" >&2
                log_error "Copy it off this machine by hand before continuing." >&2
            fi
        fi

        # Re-state the recovery model here, not only at install time.
        #
        # Anyone running this upgrade created their wallet months ago, clicked
        # past the installer's warning once, and has had no reminder since. The
        # copy above is on the SAME MACHINE — it protects against this script,
        # not against losing the box. And because the daemon issues no seed
        # phrase, that file is the only thing standing between them and a
        # permanent loss, which is precisely what an operator expecting 12 words
        # will not realise.
        if [[ -n "$_wallets_copied" ]]; then
            echo "" >&2
            log_warn "This backup is on the SAME MACHINE. Copy it somewhere else." >&2
            log_warn "There is no seed phrase — no 12/24 words exist for a wallet this" >&2
            log_warn "daemon generated. That file is the only copy of your keys." >&2
            # Not $(whoami): this script hard-requires EUID 0, so that always
            # renders "root@", which the default sshd PermitRootLogin
            # prohibit-password rejects. Use the pool user, and -r when the
            # backup is a directory.
            local _scp_user="${POOL_USER:-spiralpool}"
            local _scp_host; _scp_host=$(hostname -I 2>/dev/null | awk '{print $1}')
            local _scp_flag=""; [[ "$_wallets_is_dir" == "true" ]] && _scp_flag="-r "
            echo -e "  ${CYAN}scp ${_scp_flag}${_scp_user}@${_scp_host}:${_wallets_copied} ./${NC}" >&2
            echo "" >&2
        fi
    fi

    echo "$backup_dir"
}

# ═══════════════════════════════════════════════════════════════════════════════
# ROLLBACK
# ═══════════════════════════════════════════════════════════════════════════════

# Compare a daemon's self-reported version against the pinned target.
#
# Bitcoin Core publishes release "31.1" but the binary reports "v31.1.0" — the
# download path and the self-reported version differ by a trailing ".0". A plain
# string compare therefore reads a CORRECT install as a failed one and triggers a
# rollback to the old binary. Both sides are normalised identically, so equality
# is unchanged for every other coin (XMY's 0.18.1.0 still compares to itself).
_ver_matches() {
    # Bitcoin Core publishes release "31.1" but its binary reports "v31.1.0", so
    # a plain string equality never matches and the already-current branch is
    # dead code.
    #
    # Stripping a trailing ".0" from both sides is NOT sufficient: it is
    # asymmetric. For the next X.0 release, installed "32.0.0" strips to "32.0"
    # while target "32.0" strips to "32" — no match, reintroducing exactly the
    # bug this function exists to fix. Pad both to three components instead,
    # which is symmetric and handles every combination.
    # Pad to FOUR components and strip a leading "v". Three was not enough:
    # `IFS=. read -r a b c` puts everything past the second dot into c, so
    # 31.1.0.0 and 31.1 did not compare equal. Several targets are already
    # four-component (LTC 0.21.5.4, DOGE 0.18.1.0), so the moment a binary
    # reports a component its target omits, _ver_matches reads a SUCCESSFUL
    # install as a failure and calls rollback_coin -- which for BTC refuses to
    # restore Knots and leaves the node stopped.
    _norm4() {
        local _v="${1#v}" a b c d
        IFS=. read -r a b c d <<< "$_v"
        printf '%s.%s.%s.%s' "${a:-0}" "${b:-0}" "${c:-0}" "${d:-0}"
    }
    # Empty input is not a version. Comparing "" to "" as equal would make an
    # unreadable version look like a match.
    [[ -n "$1" && -n "$2" ]] || return 1
    [[ "$(_norm4 "$1")" == "$(_norm4 "$2")" ]]
}

rollback_coin() {
    local coin="$1" backup_dir="$2" svc="${COIN_SERVICE[$1]}.service"

    # BTC: never restore a Bitcoin Knots binary.
    #
    # Knots builds dated knots20260508 or later enforce BIP-110 (RDTS) and follow
    # the minority chain that split at block 961,632. That chain has produced a
    # handful of blocks and its coins are not traded anywhere. "Successfully"
    # rolling back to one hands the operator a daemon that runs perfectly and
    # mines nothing of value — strictly worse than a stopped daemon, because it
    # looks healthy. Leave it stopped and say so plainly.
    # Declared up front: the BTC branch below reads it, and this script runs under
    # `set -u`, so referencing it before this point aborts with "unbound variable"
    # in the one path that is trying to recover a node with no working daemon.
    local bin_dir; bin_dir=$(get_binary_dir "$coin")

    # No `| head -1` in this condition. Under `set -o pipefail` an early-exiting
    # head can SIGPIPE the producer, making the pipeline return 141 even when
    # grep matched — the guard would then read "not Knots", fall through, and
    # restore AND START the Knots binary, which is precisely what it exists to
    # prevent. Reading the whole output into a variable removes the pipeline.
    local _bak_ver=""
    if [[ "$coin" == "BTC" && -x "${backup_dir}/bin-backup/bitcoind" ]]; then
        _bak_ver=$("${backup_dir}/bin-backup/bitcoind" --version 2>/dev/null || true)
    fi

    # Fail closed on BTC: an empty _bak_ver means the backup binary was not
    # executable or --version produced nothing, so we CANNOT show it is Core.
    # Treating "unknown" as safe is how an operator lands back on the minority
    # chain, so unknown takes the same do-not-start path as a positive match.
    if [[ "$coin" == "BTC" ]] && { [[ -z "$_bak_ver" ]] || grep -qi 'knots' <<< "$_bak_ver"; }; then

        # The backed-up binary is Bitcoin Knots, which follows the BIP-110
        # minority chain. Restoring it AND STARTING IT would hand the operator a
        # daemon that runs perfectly and mines nothing of value — so we never
        # start it. But we must still put a working binary back, because
        # install_binaries unlinks the live binary BEFORE copying the new one:
        # if the copy failed, refusing to restore leaves no bitcoind on disk at
        # all, which is a worse outcome than a stopped one.
        local _live; _live=$(get_binary_path "$coin" 2>/dev/null || echo "")

        # Classify what is ACTUALLY on disk right now. The question is not "is
        # the live binary the one we backed up" — after a successful install
        # they legitimately differ (live Core, backup Knots), and treating that
        # as "nothing was restored" made this branch copy KNOTS OVER A WORKING
        # CORE BINARY. Under Restart=always the next start then lands the
        # operator back on the minority chain: a far worse outcome than the
        # misleading message it was meant to fix.
        #
        # Three states matter:
        #   live_is_core     -> the new binary installed. Never overwrite it.
        #   live_is_knots    -> the old binary is still there; nothing installed.
        #   live_missing     -> install unlinked and failed to copy; restore, but
        #                       do not start (the backup is Knots or unknown).
        local _live_ver="" _live_is_core=0 _live_missing=1
        if [[ -n "$_live" && -x "$_live" ]]; then
            _live_missing=0
            _live_ver=$("$_live" --version 2>/dev/null || true)
            if [[ -n "$_live_ver" ]] && ! grep -qi 'knots' <<< "$_live_ver"; then
                _live_is_core=1
            fi
        fi

        if [[ "$_live_is_core" -eq 1 ]]; then
            # Bitcoin Core is on disk and working. The upgrade failed AFTER the
            # swap (ownership, permissions, a version check, a failed start).
            # Restoring Knots here would undo the only part that succeeded.
            log_error "BTC upgrade failed AFTER the binary was replaced."
            log_error "Bitcoin Core is installed and has been LEFT IN PLACE — the backup is"
            log_error "Bitcoin Knots, and restoring it would put this node back on the BIP-110"
            log_error "minority chain. The daemon has been left STOPPED."
            log_error "  - inspect:  sudo journalctl -u ${svc} -n 50 --no-pager"
            log_error "  - retry:    sudo /spiralpool/scripts/coin-upgrade.sh --coin BTC"
            log_error "  - start it: sudo systemctl start ${svc}"
            log_error "The stratum re-checks chain identity at every startup, so it will refuse"
            log_error "to serve BTC work until the chain verifies."
            disable_maintenance
            return 1
        fi

        if [[ "$_live_missing" -eq 0 ]]; then
            # A binary is present and it is Knots — i.e. the failure happened
            # before install (download or checksum). Nothing to restore.
            log_error "BTC upgrade failed before the binary was replaced."
            if [[ -z "$_bak_ver" ]]; then
                log_error "The backup binary could not be identified, so it is being treated as"
                log_error "Bitcoin Knots and has NOT been started."
            else
                log_error "Your existing Bitcoin Knots daemon is intact but has been left STOPPED."
            fi
            log_error "Knots builds dated knots20260508+ follow the BIP-110 minority chain, where"
            log_error "mined blocks are unlikely to have any value, so it is not restarted for you."
            log_error "  - to retry the migration:   sudo /spiralpool/scripts/coin-upgrade.sh --coin BTC"
            log_error "  - to start it anyway (minority chain, blocks likely worthless):"
            log_error "        sudo systemctl start ${svc}"
        else
            # The live binary is gone. Restore it so the operator has something
            # to run, but leave the service stopped and say plainly what it is.
            log_warn "BTC binary is missing after a failed install — restoring the backup so the"
            log_warn "node is recoverable. It will NOT be started."
            if [[ -d "${backup_dir}/bin-backup" && -n "$bin_dir" ]]; then
                if sudo cp -r "${backup_dir}/bin-backup/." "$bin_dir/"; then
                    log_success "Restored the previous binary to ${bin_dir} (left stopped)"
                else
                    log_error "RESTORE FAILED — there is no working BTC daemon on this node."
                    log_error "Recover from ${backup_dir}/bin-backup/ by hand, or re-run the upgrade."
                fi
            else
                log_error "No backup available at ${backup_dir}/bin-backup — there is no working"
                log_error "BTC daemon on this node. Re-run the upgrade to install Bitcoin Core."
            fi
            log_error "The restored binary is Bitcoin Knots and follows the BIP-110 minority chain."
            log_error "Re-run 'sudo /spiralpool/scripts/coin-upgrade.sh --coin BTC' to complete the migration."
        fi
        return 0
    fi

    log_warn "Rolling back ${coin} to backed-up binaries..."
    if [[ -d "${backup_dir}/bin-backup" && -n "$bin_dir" ]]; then
        if ! sudo cp -r "${backup_dir}/bin-backup/." "$bin_dir/" 2>/dev/null; then
            # Do not claim a rollback that did not happen. This used `|| true`
            # and logged success regardless, so an operator could be told the
            # old binary was restored while the bin dir was empty.
            log_error "Rollback FAILED: could not restore binaries to ${bin_dir}"
            log_error "Backup is at ${backup_dir}/bin-backup — restore it by hand."
            return 1
        fi
        log_success "Rollback complete"
    fi
    if sudo systemctl start "$svc" 2>/dev/null; then
        _mark_started "$svc"
    else
        log_warn "Could not restart ${svc} after rollback"
    fi
}

# ═══════════════════════════════════════════════════════════════════════════════
# DIGIBYTE PRUNING (v9.26.4+)
# ═══════════════════════════════════════════════════════════════════════════════
# DigiByte Core v9.26.4 makes DigiDollar compatible with pruning: a pruned node
# keeps the [DigiDollar-activation-floor, tip] window and turns the transaction
# index off automatically. The v9.26.3 forced pruned→full config migration is
# gone. Because v9.26.3 REQUIRED a full node, any DGB node being upgraded here is
# coming from a full node — so on the 9.26.x → 9.26.4 upgrade we make a one-time
# offer to switch it to a pruned node (see upgrade_coin).

# True if the DGB config currently has active pruning enabled.
dgb_is_pruned() {
    local conf="${COIN_CONF[DGB]}"
    [[ -f "$conf" ]] || return 1
    grep -qE '^[[:space:]]*prune[[:space:]]*=[[:space:]]*[1-9]' "$conf"
}

# Switch the DGB config to a pruned node: drop txindex (mutually exclusive with
# prune; v9.26.4 turns the index off automatically) and any existing prune= line,
# set a single prune=5000 (~5 GB target), and delete the now-orphaned txindex
# directory so its space is reclaimed. The config is backed up first (outside the
# datadir so the service's ExecStartPre chown never trips on a root-owned file).
# v9.26.4 prunes in place — no reindex; the block files shrink in the background
# over the next few hours. Must run with the daemon STOPPED.
dgb_enable_pruning_config() {
    local conf="${COIN_CONF[DGB]}"
    [[ -f "$conf" ]] || { log_warn "DGB config not found at ${conf} — cannot enable pruning"; return 0; }
    # Section check FIRST. The edits below delete matching lines file-wide and
    # append at EOF, neither of which is section aware: on a config ending in
    # [test] the delete strips out-of-scope settings and the appended prune=5000
    # lands under [test], so mainnet gets neither prune nor txindex while the
    # txindex directory is removed anyway. Hand it back rather than half-apply
    # it, the same reasoning check_btc_conf_hazards uses for bitcoin.conf.
    #
    # Checked before the backup on purpose: backing up first left a copy of
    # rpcpassword on disk for every declined run.
    if grep -q '^[[:space:]]*\[' "$conf" 2>/dev/null; then
        log_warn "${conf} contains network sections ([main]/[test]/[regtest])."
        log_warn "These pruning edits are not section aware, so they are being skipped."
        log_warn "To enable pruning by hand, in the TOP-LEVEL section of the file:"
        echo -e "  ${CYAN}remove any txindex= line, then add:  prune=5000${NC}"
        log_warn "Then restart digibyted. Your config is unchanged; nothing was removed."
        return 0
    fi

    local _bakdir="${BACKUP_ROOT}/dgb-config"
    mkdir -p "$_bakdir"
    local _bak="${_bakdir}/digibyte.conf.pre-prune.$(date '+%Y%m%d-%H%M%S').bak"
    if cp "$conf" "$_bak" 2>/dev/null; then
        chown "${POOL_USER}:${POOL_USER}" "$_bak" 2>/dev/null || true
        chmod 600 "$_bak" 2>/dev/null || true
        log_info "Config backed up to ${_bak}"
    else
        # Fail closed. This function deletes lines and then removes the
        # transaction index; doing that with no backup on disk is not
        # recoverable, and the failure was previously silent.
        log_error "Could not back up ${conf} to ${_bak}"
        log_error "Refusing to edit the config without a backup. Pruning NOT enabled."
        return 1
    fi
    # `[[:space:]]*=` on both sides: Core trims whitespace around the key and
    # the value, so `txindex = 1` is a live setting. Matching only `txindex=`
    # left it in place while the index directory below was deleted regardless --
    # producing a config that asserts txindex AND prune at once, which Core
    # rejects at startup.
    sed -i -E '/^[[:space:]]*#?[[:space:]]*txindex[[:space:]]*=/d' "$conf"
    sed -i -E '/^[[:space:]]*#?[[:space:]]*prune[[:space:]]*=/d'   "$conf"
    printf '\n# DigiByte Core v9.26.4+: pruned DigiDollar node (~5 GB target). prune turns\n# the transaction index off automatically and keeps the DigiDollar window intact.\nprune=5000\n' >> "$conf"
    chown "${POOL_USER}:${POOL_USER}" "$conf" 2>/dev/null || true

    # Reclaim the orphaned transaction index. It was built while this was a full
    # node; under -prune Core no longer uses it and will NOT delete it on its own,
    # so the directory (tens of GB) would linger and defeat the point of pruning.
    # The daemon is stopped here, so removing it is safe — it is derived data,
    # rebuilt automatically only if the node is ever switched back to a full node.
    local _dd; _dd=$(get_data_dir "DGB"); [[ -z "$_dd" ]] && _dd="$(dirname "$conf")"
    if [[ -n "$_dd" && -d "${_dd}/indexes/txindex" ]]; then
        log_info "Removing orphaned transaction index (${_dd}/indexes/txindex) to reclaim disk…"
        rm -rf "${_dd}/indexes/txindex" 2>/dev/null || sudo rm -rf "${_dd}/indexes/txindex" 2>/dev/null || true
        log_success "Transaction index removed — that space is freed immediately"
    fi

    log_success "DigiByte pruning enabled: prune=5000, txindex removed"
    log_warn "On its FIRST start the daemon runs a ONE-TIME prune of the existing block"
    log_warn "store (RPC returns error -28 'Pruning blockstore…'). While it runs, DGB serves"
    log_warn "no block templates, so DGB miners' shares are REJECTED until it completes."
    log_warn "How long it takes VARIES with chain size, disk speed, and load — anywhere from"
    log_warn "several minutes to an hour or more for a full DGB node. Check progress with:"
    echo -e "  ${CYAN}digibyte-cli getblockchaininfo${NC}   ${DIM}# error -28 while pruning; '\"pruned\": true' when done${NC}"
    log_info "After that one-time pass, pruning is gradual and in the background — mining"
    log_info "and all pool functions run normally while the disk shrinks on block flush."
}

# ═══════════════════════════════════════════════════════════════════════════════
# UPGRADE ONE COIN
# ═══════════════════════════════════════════════════════════════════════════════

upgrade_coin() {
    local coin="$1"
    local do_reindex="${2:-false}"
    local arch; arch=$(get_system_arch)
    local svc="${COIN_SERVICE[$coin]}.service"
    local target_ver="${COIN_TARGET[$coin]}"
    local risk="${COIN_RISK[$coin]}"

    # ── Pre-flight checks ──────────────────────────────────────────────────────
    local installed_ver; installed_ver=$(get_installed_version "$coin")

    if [[ "$installed_ver" == "not_installed" ]]; then
        log_error "${coin} is not installed on this node — use install.sh to add it"
        return 1
    fi

    if _ver_matches "$installed_ver" "$target_ver"; then
        # BTC: being at the target VERSION does not mean being on the right
        # CHAIN. A node can run Bitcoin Core 31.1 and still follow the BIP-110
        # minority chain — that is the whole failure mode this release exists to
        # catch, and the version cache is written before the chain is verified.
        #
        # Without this branch the recovery the script itself prints
        # ("rerun with --coin BTC --reindex") returns "nothing to do" forever,
        # leaving the node stranded with --check reporting it green. Re-running
        # must therefore always re-verify, and repair if it can.
        #
        # A --reindex request skips the shortcut entirely and takes the full
        # path, because the operator is explicitly asking for the heavier fix.
        if [[ "$coin" == "BTC" && "$do_reindex" != "true" ]]; then
            log_info "${coin} is already at ${target_ver} — verifying chain identity"
            local _rc=0
            check_btc_conf_hazards  || _rc=1
            verify_btc_majority_chain || _rc=1
            return $_rc
        fi
        if [[ "$coin" != "BTC" ]]; then
            log_info "${coin} is already at ${target_ver} — nothing to do"
            return 0
        fi
    fi

    # ── Summary ───────────────────────────────────────────────────────────────
    echo ""
    echo -e "${CYAN}┌─────────────────────────────────────────────────────────────┐${NC}"
    echo -e "${CYAN}│${NC}  ${WHITE}${BOLD}${coin} Daemon Upgrade${NC}"
    echo -e "${CYAN}├─────────────────────────────────────────────────────────────┤${NC}"
    printf "${CYAN}│${NC}  %-22s %s\n" "Installed version:" "$installed_ver"
    printf "${CYAN}│${NC}  %-22s %s\n" "Target version:" "$target_ver"
    printf "${CYAN}│${NC}  %-22s " "Upgrade risk:"
    echo -e "$(risk_label "$risk")"
    printf "${CYAN}│${NC}  %-22s %s\n" "Reindex on start:" \
        "$([[ "$do_reindex" == "true" ]] && echo "YES — chain will resync (hours)" || echo "No")"
    echo -e "${CYAN}└─────────────────────────────────────────────────────────────┘${NC}"

    # Extra warning for MAJOR upgrade without --reindex
    if [[ "$risk" == "MAJOR" && "$do_reindex" == "false" ]]; then
        echo ""
        log_warn "MAJOR upgrade detected. If the daemon fails to start or reports"
        log_warn "database errors, rerun with:  sudo /spiralpool/scripts/coin-upgrade.sh --coin ${coin} --reindex"
    fi

    echo ""
    printf "  Proceed with %s upgrade? [y/N] " "$coin"
    local confirm; read -r confirm
    [[ "$confirm" =~ ^[Yy]$ ]] || { log_info "Skipped ${coin}"; return 0; }

    # ── DigiByte pruning offer (one-time, until the node is at target) ─────────
    # v9.26.3 REQUIRED a full node (txindex for DigiDollar); v9.26.4+ lets DigiDollar
    # run while pruned. Any DGB node below the target that is still full gets the
    # offer to switch to a pruned node now. Applied in place before the daemon
    # starts (no reindex). Declining leaves it a full node. Gate on the target
    # version so the offer does not re-fire once the node is already there.
    local _dgb_enable_prune=false
    if [[ "$coin" == "DGB" && "$installed_ver" != "${COIN_TARGET[DGB]}" ]] && ! dgb_is_pruned; then
        echo ""
        echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo -e "${CYAN}${BOLD}  DigiByte v9.26.4+ supports pruning${NC}"
        echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo ""
        echo -e "  v9.26.3 required a ${BOLD}full${NC} node (txindex) for DigiDollar. v9.26.4 lets a"
        echo -e "  ${BOLD}pruned${NC} node run DigiDollar too — it keeps only the DigiDollar window"
        echo -e "  (a few GB) instead of the full ~80 GB node (blocks + txindex), and ${BOLD}prunes in place${NC} with"
        echo -e "  ${BOLD}no resync${NC}."
        echo ""
        echo -e "  Enabling sets ${BOLD}prune=5000${NC} (~5 GB) and removes txindex (v9.26.4 turns the"
        echo -e "  index off automatically under prune). ${DIM}Reverting to full later needs a resync.${NC}"
        echo ""
        echo -e "  ${YELLOW}Note:${NC} the txindex space frees right away. On its first restart the node"
        echo -e "  runs a ${BOLD}one-time prune pass${NC} (RPC: error -28 'Pruning blockstore…') during which"
        echo -e "  it serves no templates — ${BOLD}DGB shares are rejected until it finishes${NC}. How long"
        echo -e "  this takes ${BOLD}varies with your system${NC} (chain size, disk speed, load) — from several"
        echo -e "  minutes to an hour or more. After that, pruning is gradual/background and mining is normal."
        echo -e "  Check progress:  ${CYAN}digibyte-cli getblockchaininfo${NC}  ${DIM}('\"pruned\": true' when done)${NC}"
        echo ""
        printf "  Enable pruning for DigiByte? [y/N] "
        local _pc; read -r _pc
        if [[ "$_pc" =~ ^[Yy]$ ]]; then
            _dgb_enable_prune=true
            log_info "DigiByte will be switched to a pruned node (prune=5000) during this upgrade"
        else
            log_info "DigiByte will remain a full node"
        fi
    fi

    mkdir -p "$WORK_DIR"
    local backup_path

    # ── Step 1: Backup ────────────────────────────────────────────────────────
    # BTC only: refuse to start if a legacy wallet would be stranded by the swap.
    # Must run BEFORE the daemon is stopped — it needs RPC to inspect wallets.
    if [[ "$coin" == "BTC" ]]; then
        if ! check_btc_legacy_wallets; then
            log_error "Aborting BTC upgrade — resolve the issue reported above first."
            log_error "Nothing has been changed."
            return 1
        fi
    fi

    log_step "Step 1/5 — Backup ${coin}"
    backup_path=$(backup_coin "$coin")
    log_success "Backup: ${backup_path}"

    # ── Step 2: Maintenance mode ──────────────────────────────────────────────
    log_step "Step 2/5 — Enable maintenance mode"
    enable_maintenance

    # ── Step 3: Stop daemon (VERIFIED — must confirm the process is gone before
    #    we overwrite its binary, or the install fails with 'Text file busy') ────
    log_step "Step 3/5 — Stop ${coin} daemon"
    if ensure_daemon_stopped "$coin"; then
        log_success "${COIN_DAEMON_CMD[$coin]} confirmed stopped (no process running)"
    else
        log_error "${coin}: ${COIN_DAEMON_CMD[$coin]} is STILL running after stop + SIGKILL."
        log_error "Refusing to install over a live binary. Something may be auto-restarting it —"
        # Deliberately not `pgrep -af ${COIN_DAEMON_CMD[$coin]}`: BTC, BCH and
        # FBTC all run a binary named bitcoind, so that command lists other
        # coins' daemons too — the same confusion ensure_daemon_stopped exists
        # to avoid. Point at the unit and the resolved path instead.
        log_error "check:  systemctl status ${svc} --no-pager"
        local _diag_path; _diag_path=$(get_binary_path "$coin" 2>/dev/null || true)
        [[ -n "$_diag_path" ]] || _diag_path="/usr/local/bin/${COIN_DAEMON_CMD[$coin]}"
        log_error "        pgrep -af \"^${_diag_path}\""
        log_error "The ${coin} daemon has been left STOPPED."
        disable_maintenance
        return 1
    fi

    # ── Step 4: Download + install ────────────────────────────────────────────
    log_step "Step 4a/5 — Download ${coin} ${target_ver}"
    local extracted_dir
    if ! extracted_dir=$(download_"${coin}" "$arch"); then
        log_error "Download failed for ${coin}"
        rollback_coin "$coin" "$backup_path"
        disable_maintenance
        return 1
    fi

    log_step "Step 4b/5 — Install ${coin} binaries"
    if ! install_binaries "$coin" "$extracted_dir"; then
        log_error "Binary installation failed for ${coin}"
        rollback_coin "$coin" "$backup_path"
        disable_maintenance
        return 1
    fi

    # ── Verify the installed binary is actually the target BEFORE we start it
    #    or reindex. If the copy silently failed (e.g. the daemon was still
    #    running), the on-disk binary still reports the OLD version — abort here
    #    rather than reindex for hours on the wrong binary. ────────────────────
    local _disk_ver=""
    local _disk_path; _disk_path=$(get_binary_path "$coin")
    if [[ -n "$_disk_path" && -x "$_disk_path" ]]; then
        _disk_ver=$("$_disk_path" --version 2>/dev/null | head -1 \
            | grep -oP '(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*' | head -1 || echo "")
    fi
    if [[ -n "$_disk_ver" ]] && ! _ver_matches "$_disk_ver" "$target_ver"; then
        log_error "${coin}: installed binary still reports ${_disk_ver}, expected ${target_ver}."
        log_error "The new binary did NOT take — aborting before start/reindex."
        log_error "No config or chain changes were made; the old binary is intact."
        rollback_coin "$coin" "$backup_path"
        disable_maintenance
        return 1
    fi

    # ── Apply the DGB pruning switch (must run BEFORE start so the node comes up
    #    already pruned; v9.26.4 prunes in place — no reindex) ───────────────────
    if [[ "$_dgb_enable_prune" == "true" ]]; then
        log_step "Enable DigiByte pruning — prune=5000, remove txindex"
        # Guarded: this function returns non-zero when it declines to edit
        # (backup failed). Unguarded, errexit turns a deliberate, safe refusal
        # into an aborted run that never restarts the daemon. Declining to prune
        # is not a reason to leave DGB down — the config is provably untouched.
        if ! dgb_enable_pruning_config; then
            log_warn "DigiByte pruning was NOT enabled (see above). Continuing:"
            log_warn "the config was left unchanged, so the node is safe to start."
        fi
    fi

    # ── Step 5: Start daemon ──────────────────────────────────────────────────
    log_step "Step 5/5 — Start ${coin} daemon"

    # Clear any StartLimitBurst failures from prior crash loops — without this,
    # systemd refuses to start the daemon if it crashed 5+ times before upgrade.
    sudo systemctl reset-failed "$svc" 2>/dev/null || true

    # Defensive: delete any stale reindex-once.conf left behind by a PRIOR upgrade
    # whose cleanup didn't complete. A leftover -reindex drop-in silently forces a
    # full chainstate rebuild (from local block files) on the very next daemon
    # restart — observed in the field days after a MAJOR upgrade. Remove it now;
    # the block below re-creates a fresh one only if THIS upgrade wants a reindex.
    local _svc_dropin_dir="/etc/systemd/system/${svc}.d"
    if [[ -f "${_svc_dropin_dir}/reindex-once.conf" ]]; then
        log_warn "Removing stale reindex drop-in from a previous upgrade (would have forced an unwanted reindex)"
        sudo rm -f "${_svc_dropin_dir}/reindex-once.conf"
        sudo rmdir --ignore-fail-on-non-empty "$_svc_dropin_dir" 2>/dev/null || true
        sudo systemctl daemon-reload
    fi

    if [[ "$do_reindex" == "true" ]]; then
        # Write a systemd drop-in that appends -reindex to ExecStart.
        # The drop-in is removed immediately after start so the next
        # automatic restart (after reindex completes) runs without it.
        local dropin_dir="/etc/systemd/system/${svc}.d"
        # Register with the EXIT trap before creating it. Any failure between
        # here and the removal below (a failing daemon-reload, an interrupted
        # sleep) would otherwise leave -reindex on the unit permanently, which
        # with Restart=always is an endless reindex loop.
        REINDEX_DROPIN="${dropin_dir}/reindex-once.conf"
        sudo mkdir -p "$dropin_dir"
        local exec_start
        exec_start=$(systemctl cat "$svc" 2>/dev/null \
            | grep '^ExecStart=' | tail -1 | sed 's/^ExecStart=//')
        sudo tee "${dropin_dir}/reindex-once.conf" > /dev/null << DROPIN
[Service]
ExecStart=
ExecStart=${exec_start} -reindex
DROPIN
        sudo systemctl daemon-reload
        # Unguarded, a failed start aborts under errexit BEFORE the drop-in is
        # removed, leaving -reindex on the unit permanently. With Restart=always
        # that is an endless reindex loop. Capture the result and always clean up.
        local _start_rc=0
        sudo systemctl start "$svc" || _start_rc=$?
        if [[ $_start_rc -eq 0 ]]; then _mark_started "$svc"; fi

        # Remove the reindex drop-in immediately — daemon keeps running,
        # next restart (post-reindex) will be clean.
        sleep 5
        sudo rm -f "${dropin_dir}/reindex-once.conf"
        REINDEX_DROPIN=""
        [[ -d "$dropin_dir" ]] && sudo rmdir --ignore-fail-on-non-empty "$dropin_dir"
        sudo systemctl daemon-reload

        if [[ $_start_rc -ne 0 ]]; then
            log_error "${svc} failed to start with -reindex (exit ${_start_rc})."
            log_error "The one-shot reindex drop-in has been removed, so the next"
            log_error "start will be a normal one. Check: journalctl -u ${svc} -n 50"
            return 1
        fi

        echo ""
        log_warn "Reindex in progress — this may take hours depending on chain size."
        log_warn "Monitor progress with:"
        echo -e "  ${CYAN}$(get_coin_cli "$coin") getblockchaininfo | grep -E 'blocks|headers|verificationprogress'${NC}"
        echo -e "  ${CYAN}sudo journalctl -u ${svc} -f${NC}"
        log_warn "The pool stratum will automatically reconnect when the daemon is healthy."
    else
        # Guarded on purpose. Unguarded, errexit aborts the run inside this
        # statement, skipping wait_for_daemon, check_btc_conf_hazards and
        # verify_btc_majority_chain. The binary swap has already succeeded at
        # this point, so get_installed_version reads the new version off disk
        # and --check reports the coin "current" while the daemon is down and
        # the chain was never verified. Report it instead.
        if sudo systemctl start "$svc"; then
            _mark_started "$svc"
            log_success "${svc} started"
            wait_for_daemon "$coin"
        else
            log_error "${coin}: the new binary installed, but ${svc} failed to start."
            log_error "The chain identity check did NOT run, so this node is unverified."
            log_error "Diagnose with:  sudo journalctl -u ${svc} -n 50 --no-pager"
            log_error "If the block index is damaged, re-run with:  --coin ${coin} --reindex"
            log_error "To go back to the previous binary:  ${backup_path:-the backup printed above}"
            disable_maintenance
            return 1
        fi
    fi

    # ── Version verify ────────────────────────────────────────────────────────
    # Update cache FIRST — the binary was just installed, so the cache should
    # reflect the target version. This is essential for daemons whose
    # --version output has no parseable version number.
    write_version_cache "$coin" "$target_ver"
    sleep 3

    # Try to verify via --version output (bypassing cache to check the real binary)
    local bin_ver=""
    local bin_path; bin_path=$(get_binary_path "$coin")
    if [[ -n "$bin_path" && -x "$bin_path" ]]; then
        bin_ver=$("$bin_path" --version 2>/dev/null | head -1 \
            | grep -oP '(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*' | head -1 || echo "")
    fi

    if [[ -n "$bin_ver" ]] && _ver_matches "$bin_ver" "$target_ver"; then
        # Binary confirms the target version
        log_success "${coin}: ${installed_ver} → ${target_ver} ✓"
    elif [[ -z "$bin_ver" ]]; then
        # Daemon doesn't report a version number — trust the install
        log_success "${coin}: ${installed_ver} → ${target_ver} ✓ (version cached — daemon has no version output)"
    else
        # Binary reports a different version than expected
        log_warn "Binary reports '${bin_ver}' — expected '${target_ver}'. Verify manually."
    fi

    # ── BTC: confirm the node actually follows the majority chain ─────────────
    # A Knots → Core swap inherits any rejected-block flag the enforcing binary
    # persisted to the block index, so the daemon can come up looking perfectly
    # healthy while still on the minority branch. Never report this upgrade as
    # done without checking. Skipped during a reindex, which takes hours — the
    # stratum preflight check covers that case when the daemon finally comes up.
    local _btc_chain_rc=0
    if [[ "$coin" == "BTC" ]]; then
        log_step "Check BTC config for hazardous settings"
        check_btc_conf_hazards || _btc_chain_rc=1
    fi
    if [[ "$coin" == "BTC" && "$do_reindex" != "true" ]]; then
        log_step "Verify BTC chain identity"
        verify_btc_majority_chain || _btc_chain_rc=1
    fi

    disable_maintenance

    echo ""
    log_success "Backup preserved at: ${backup_path}"
    echo -e "  ${DIM}To restore if needed: sudo cp -r ${backup_path}/bin-backup/. \$(readlink -f \$(which ${COIN_DAEMON_CMD[$coin]}) | xargs dirname)/${NC}"
    echo ""
    return $_btc_chain_rc
}

# ═══════════════════════════════════════════════════════════════════════════════
# BTC MAJORITY CHAIN VERIFICATION
# ═══════════════════════════════════════════════════════════════════════════════

# Hash of Bitcoin block 961,632 on the majority chain.
#
# Provenance: fetched 2026-08-14 from two independent block explorers,
# blockstream.info and mempool.space, which returned byte-identical values.
#
# BIP-110 (RDTS) enforcing nodes rejected the majority chain's block at this
# height on 2026-08-08 and have followed a minority branch ever since. Comparing
# the hash at this height is the authoritative test of which chain a node is on.
BITCOIN_MAJORITY_BLOCK_961632="00000000000000000000d1e01392faa65ceeaed307f0a3159144b84146ff24ba"
BITCOIN_SPLIT_HEIGHT=961632

# The competing block at the same height on the BIP-110 (RDTS) minority chain,
# mined by Roughnecks. Used only to name the chain in diagnostics — seeing this
# hash means the node is definitively on the RDTS fork rather than merely lost.
#
# Provenance: published in the BIP-110 project's own chain-switching guide at
# https://bip110.org/change-chains/ and corroborated by contemporaneous reporting
# of the Antpool (non-signalling) vs Roughnecks (signalling) split at 961,632.
BITCOIN_RDTS_BLOCK_961632="0000000000000000000169eb6f811ddbd0daf343af7b62180cdb13e7c78dbc16"

# Refuse to swap BTC to Bitcoin Core while a legacy (BDB) wallet is loaded.
#
# Bitcoin Core 30.0 removed the ability to CREATE OR LOAD legacy BDB wallets.
# Core can still migrate one (it keeps a read-only BDB parser for exactly this),
# so this is recoverable rather than fatal — but discovering it after the binary
# swap means the daemon comes up with the wallet missing, during a maintenance
# window where the consensus software also just changed. Migrating first keeps
# those two changes separate.
#
# Most operators are unaffected: Spiral Pool creates descriptor wallets, and
# operators who supplied an external address (hardware/air-gapped wallet) have
# no wallet on the server at all. This catches imported or restored wallets.
# Classify BTC wallet FILES on disk by magic bytes. Bitcoin Core stores a legacy
# wallet as Berkeley DB and a descriptor wallet as SQLite, so this needs no
# daemon — which is why it is the check that runs on every path, not only when
# RPC is unavailable. Returns 1 if any legacy wallet was found.
_btc_disk_wallet_scan() {
    local _wdir _legacy_found=0 _checked=0 _unclassified=0 _w
    _wdir="$(get_data_dir BTC 2>/dev/null || true)"
    [[ -n "$_wdir" ]] || _wdir="$(dirname "${COIN_CONF[BTC]}")"

    # Look everywhere Core can keep a wallet, not just wallets/*/wallet.dat:
    #   <datadir>/wallets/<name>/wallet.dat   modern default
    #   <datadir>/wallets/<name>.dat          -wallet=foo.dat
    #   <datadir>/wallet.dat                  pre-0.21 top-level layout
    # Missing the last two meant a legacy BDB wallet was reported as "all
    # clear" and the consensus binary was swapped on top of it.
    local _candidates=()
    [[ -d "${_wdir}/wallets" ]] && while IFS= read -r _w; do
        _candidates+=("$_w")
    done < <(find "${_wdir}/wallets" -maxdepth 3 -name '*.dat' -type f 2>/dev/null)
    [[ -f "${_wdir}/wallet.dat" ]] && _candidates+=("${_wdir}/wallet.dat")

    local _w
    for _w in ${_candidates+"${_candidates[@]}"}; do
        [[ -f "$_w" ]] || continue
        _checked=$((_checked + 1))

        # Classify explicitly. `head | grep` produces no output for an
        # unreadable OR a zero-byte file, so a negated match called both
        # "legacy" and hard-blocked the upgrade over something that is not a
        # wallet at all. Read the magic once and branch on all three cases.
        local _magic=""
        # Strip NULs BEFORE the substitution sees them. A Berkeley DB wallet
        # starts with NUL bytes and bash warns "ignored null byte in input"
        # once per file — noise on exactly the wallets this exists to find.
        # Redirecting inside the substitution does not help: the shell emits
        # that warning while performing the substitution itself. Classification
        # is unaffected: the BDB magic sits at offset 12 and survives, and the
        # SQLite header is ASCII with no NULs.
        _magic=$(head -c 15 "$_w" 2>/dev/null | tr -d ' ' || true)
        if [[ -z "$_magic" ]]; then
            _unclassified=$((_unclassified + 1))
            log_warn "Cannot read ${_w} (empty or unreadable) — cannot classify it."
            log_warn "If that is a real wallet, back it up and check it by hand."
        elif [[ "$_magic" == "SQLite format 3" ]]; then
            : # descriptor wallet, fine
        else
            _legacy_found=1
            log_error "Legacy (BDB) wallet file: ${_w}"
        fi
    done

    if [[ "$_legacy_found" -eq 1 ]]; then
        log_error "Bitcoin Core ${COIN_TARGET[BTC]} cannot load a legacy (BDB) wallet."
        log_error "Start the daemon on the CURRENT binary and migrate first:"
        echo -e "  ${CYAN}bitcoin-cli -rpcwallet=<name> backupwallet /path/outside/datadir/<name>.bak${NC}"
        echo -e "  ${CYAN}bitcoin-cli -rpcwallet=<name> migratewallet${NC}"
        return 1
    fi

    if [[ "$_checked" -eq 0 ]]; then
        log_warn "No wallet files found under ${_wdir}/wallets — nothing to migrate."
        log_warn "That is normal when the pool pays to an external address."
    elif [[ "$_unclassified" -gt 0 ]]; then
        # Do not report a clean result that was not obtained: $_checked counts
        # files we could not read as well as ones we classified.
        log_warn "Checked ${_checked} wallet file(s) on disk; ${_unclassified} could not be classified."
        log_warn "The remainder are descriptor (SQLite). Check the unreadable one(s) by hand."
    else
        log_success "Checked ${_checked} wallet file(s) on disk: all descriptor (SQLite)."
    fi
    return 0
}

check_btc_legacy_wallets() {
    local cli; cli=$(get_coin_cli BTC)

    # Parse listwallets as JSON rather than with tr/grep.
    #
    # The default wallet is reported as [ "" ] — an empty NAME, not an empty
    # list. Stripping quotes and dropping blank lines therefore erases exactly
    # the wallet most likely to be a legacy wallet.dat, which is the case this
    # function exists to catch. Python keeps the empty name as a real entry.
    local wallets_json
    if ! wallets_json=$($cli listwallets 2>/dev/null); then
        # Fail CLOSED. An unreachable daemon means we do not know whether a
        # legacy wallet is present, and proceeding would swap the binary out
        # from under one. This mirrors the stratum chain gate, which also
        # refuses on "unknown" rather than assuming the safe answer.
        # RPC is unavailable. Do not fail closed here: rollback_coin leaves BTC
        # stopped on purpose and then tells the operator to re-run this exact
        # command, and verify_btc_majority_chain points at --reindex the same
        # way. Refusing on an unreachable daemon makes both instructions
        # impossible to follow -- a deadlock in the recovery path.
        #
        # Answer the question from disk instead. Bitcoin Core stores a legacy
        # wallet as a Berkeley DB file and a descriptor wallet as SQLite; the
        # two are distinguishable by magic bytes, so the daemon is not needed.
        log_warn "Could not query BTC wallets over RPC (daemon not running?)."
        log_warn "Falling back to inspecting the wallet files directly."

        _btc_disk_wallet_scan || return 1
        return 0
    fi

    # The daemon answered. Still scan the files on disk BEFORE trusting
    # listwallets: listwallets reports only LOADED wallets, so an unloaded
    # legacy wallet.dat in the datadir was reported "all clear" exactly when the
    # daemon was healthy enough to be upgraded — the common case. The stronger
    # check used to run only on the path where we knew least.
    _btc_disk_wallet_scan || return 1

    # Extract each quoted string. This is deliberately not a tr/grep -v pipeline:
    # `grep -o '"[^"]*"'` matches the empty string "" as a real entry, whereas
    # stripping quotes and dropping blank lines silently discards it. No python
    # dependency — this script has none and should not gain one, since a missing
    # interpreter would then block every BTC upgrade.
    local -a wallet_names=()
    local _q
    while IFS= read -r _q; do
        _q="${_q#\"}"; _q="${_q%\"}"
        wallet_names+=("$_q")
    done < <(printf '%s' "$wallets_json" | grep -o '"[^"]*"' || true)

    [[ ${#wallet_names[@]} -eq 0 ]] && return 0

    # Collect into an ARRAY, not a space-joined string. A space-joined string
    # re-split by an unquoted `for` mangles exactly the cases this function is
    # here to catch: the default wallet (whose display name contains a space) and
    # any wallet name containing spaces or glob characters. The printed recovery
    # commands are the operator's only way forward, since this aborts the
    # upgrade — emitting `-rpcwallet=<default` is worse than emitting nothing.
    local w
    local -a legacy=()
    for w in "${wallet_names[@]}"; do
        # An empty name is the default wallet and is addressed as "" — valid.
        #
        # Fail CLOSED. Piping straight into grep classified a wallet as
        # "descriptor, safe" whenever the RPC produced no output at all — a
        # transient error, a wallet that failed to load, a daemon shutting down —
        # because grep simply found no match. This function's whole purpose is to
        # stop a consensus binary being swapped on top of a legacy BDB wallet, so
        # "I could not tell" must be treated as legacy, not as safe.
        local _wi
        _wi=$($cli -rpcwallet="$w" getwalletinfo 2>/dev/null)
        if [[ -z "$_wi" ]] || ! grep -q '"descriptors"' <<< "$_wi"; then
            log_warn "  Could not read wallet info for '${w:-(default)}' — treating it as legacy."
            legacy+=("$w")
        elif grep -q '"descriptors"[[:space:]]*:[[:space:]]*false' <<< "$_wi"; then
            legacy+=("$w")
        fi
    done

    [[ ${#legacy[@]} -eq 0 ]] && return 0

    # Display name for humans; the RPC argument is always the raw name, quoted.
    local _disp
    echo ""
    log_error "Legacy (BDB) wallet(s) found:"
    for w in "${legacy[@]}"; do
        _disp="${w:-(default unnamed wallet)}"
        log_error "  - ${_disp}"
    done
    log_error "Bitcoin Core ${COIN_TARGET[BTC]} cannot load these. Migrate them BEFORE upgrading,"
    log_error "while the current daemon is still running. Back up first — migration is one-way:"
    for w in "${legacy[@]}"; do
        _disp="${w:-(default unnamed wallet)}"
        echo -e "  ${DIM}# ${_disp}${NC}"
        echo -e "  ${CYAN}$cli -rpcwallet=\"${w}\" backupwallet \"/path/outside/datadir/btc-wallet.bak\"${NC}"
        echo -e "  ${CYAN}$cli -rpcwallet=\"${w}\" migratewallet${NC}"
    done
    log_error "Then re-run this upgrade. Confirm with: getwalletinfo | grep descriptors"
    return 1
}

# Check the operator's bitcoin.conf for settings that are dangerous on the
# majority chain. Spiral Pool never writes these itself, but operators who
# followed public advice while stuck on the minority chain may have added them
# by hand, and both are silent once set.
check_btc_conf_hazards() {
    local conf="${COIN_CONF[BTC]}"
    local found=0          # 0 = clean (success), 1 = hazard found (failure)
    [[ -f "$conf" ]] || return 0

    # maxtipage raises the tip-age threshold below which the node considers
    # itself in initial block download. Miners on the stalled RDTS chain were
    # advised to set it to 2592000 (30 days) to suppress stale-tip warnings.
    #
    # On the majority chain that is actively dangerous: getblocktemplate refuses
    # to serve work while in IBD, and that refusal is the last automatic backstop
    # against building templates on a dead tip. With a 30-day threshold the node
    # reports initialblockdownload:false and no stale-tip warning even if it has
    # been wedged for weeks, so every health check reads green while the pool
    # hashes against work that can never confirm.
    # DETECT ONLY — deliberately. An automatic repair was written and withdrawn.
    #
    # Editing an operator's hand-written config safely is harder than it looks,
    # and every one of these has to be right or the repair is worse than the
    # hazard:
    #   - it must trigger on VALUE, not presence: maxtipage=3600 is stricter than
    #     Core's 86400 default, so removing it makes the node less safe;
    #   - it must be scoped to mainnet and to the [main]/top-level section, or it
    #     silently strips a legitimate setting from a testnet/regtest section;
    #   - `sed -i` as root replaces the inode, so mode (640, per install.sh) and
    #     ownership must be restored, and a symlinked config is silently
    #     converted to a regular file;
    #   - the backup holds rpcpassword in plaintext and must be mode 0600;
    #   - and it can only be applied while the daemon is STOPPED, which this
    #     function is not — it runs after the daemon has already restarted.
    #
    # The stratum chain gate does not depend on any of this: it computes tip
    # staleness itself from mediantime against its own threshold and never reads
    # the daemon's maxtipage, so the pool still refuses to mine a dead tip. What
    # the setting actually breaks is the daemon's own initialblockdownload flag,
    # which the dashboard and Sentinel trust — so the symptom is that the health
    # surfaces disagree with the stratum, not that the pool mines into the void.
    #
    # Report it precisely and let the operator decide.
    if grep -qE '^[[:space:]]*maxtipage[[:space:]]*=' "$conf" 2>/dev/null; then
        local _mtp_lines
        _mtp_lines=$(grep -nE '^[[:space:]]*maxtipage[[:space:]]*=' "$conf" 2>/dev/null || true)
        echo ""
        log_error "BTC bitcoin.conf sets 'maxtipage' — remove it before mining."
        log_error "  file: ${conf}"
        while IFS= read -r _l; do
            [[ -n "$_l" ]] && log_error "  line ${_l}"
        done <<< "$_mtp_lines"
        log_error "It disables the daemon's initial-block-download gating, so a wedged node"
        log_error "reports initialblockdownload=false and every health check reads green."
        log_error "Comment the line out and restart the daemon:"
        echo -e "  ${CYAN}sudo sed -i -E 's/^([[:space:]]*)(maxtipage)/\\1# \\2/' ${conf}${NC}"
        echo -e "  ${CYAN}sudo systemctl restart ${COIN_SERVICE[BTC]}${NC}"
        log_error "Values at or below Core's default of 86400 are not a hazard — check yours."
        found=1
    fi

    # consensusrules is a Bitcoin Knots option. Core ignores it with only a
    # debug.log warning, so it is harmless where it sits — but it is fatal if it
    # ever reaches a command line, and it signals the node was RDTS-enforcing.
    if grep -qE '^[[:space:]]*consensusrules[[:space:]]*=' "$conf" 2>/dev/null; then
        echo ""
        log_warn "BTC bitcoin.conf sets 'consensusrules' — a Bitcoin Knots option."
        log_warn "  file: ${conf}"
        log_warn "Bitcoin Core ignores it with a debug.log warning, so it will not stop"
        log_warn "startup, but it is fatal if passed on a command line. Remove it."
        log_warn "Note it never controlled enforcement: that was compiled into the build."
    fi

    return $found
}

# Verify BTC follows the majority chain after an upgrade, and repair it if not.
#
# An RDTS-enforcing daemon marks the majority block at the split height
# BLOCK_FAILED_VALID, and that status bit is persisted in the block index on
# disk. Swapping the binary to Core does NOT clear it — Core reads the same
# index, honours the flag, and keeps following the minority branch while
# looking completely healthy. reconsiderblock clears the flag; the reorg then
# happens on its own within seconds.
#
# Verified against Bitcoin Core v31.1.0 on regtest (2026-08-14): a node holding a
# persisted failure flag refused to reorg onto a chain with more work, still
# refused after a restart on the same datadir, and converged immediately once
# reconsiderblock cleared the flag. No reindex and no resync were required — the
# chain below the split is shared history and is not rebuilt.
verify_btc_majority_chain() {
    local cli; cli=$(get_coin_cli BTC)
    local blocks actual

    if ! blocks=$($cli getblockcount 2>/dev/null); then
        log_warn "BTC: could not reach the daemon to verify chain identity."
        log_warn "This is NOT a wrong-chain verdict — the node may still be starting."
        log_warn "Re-check with:  $cli getblockhash ${BITCOIN_SPLIT_HEIGHT}"
        return 0
    fi

    # Below the split height there is nothing to compare yet. Treat as unknown,
    # never as a pass — a syncing node must not be reported as verified.
    if [[ "$blocks" -lt "$BITCOIN_SPLIT_HEIGHT" ]]; then
        log_warn "BTC: node is at height ${blocks}, below the split height ${BITCOIN_SPLIT_HEIGHT}."
        log_warn "Chain identity cannot be verified until it syncs past that point."
        return 0
    fi

    actual=$($cli getblockhash "$BITCOIN_SPLIT_HEIGHT" 2>/dev/null || echo "")

    # An unreadable hash is NOT a wrong-chain verdict. wait_for_daemon returns 0
    # on timeout, so the daemon may still be opening its RPC interface. Falling
    # through would report "BTC IS NOT ON THE MAJORITY CHAIN" and start the
    # reconsiderblock repair against a node that was never asked a question.
    # (The identical call inside the reorg-wait loop below is left alone: there,
    # an empty result legitimately means "not converged yet, keep polling".)
    if [[ -z "$actual" ]]; then
        log_warn "Could not read block ${BITCOIN_SPLIT_HEIGHT} from the BTC daemon."
        log_warn "This is NOT a wrong-chain verdict — the daemon may still be starting."
        log_warn "The stratum re-checks chain identity at every startup and refuses to"
        log_warn "serve work until it verifies, so nothing can mine the wrong chain."
        return 0
    fi
    if [[ "$actual" == "$BITCOIN_MAJORITY_BLOCK_961632" ]]; then
        log_success "BTC: on the majority chain (block ${BITCOIN_SPLIT_HEIGHT} verified, height ${blocks})"
        return 0
    fi

    echo ""
    if [[ "$actual" == "$BITCOIN_RDTS_BLOCK_961632" ]]; then
        log_error "BTC IS FOLLOWING THE BIP-110 (RDTS) MINORITY CHAIN."
    else
        log_error "BTC IS NOT ON THE MAJORITY CHAIN."
    fi
    log_error "  block ${BITCOIN_SPLIT_HEIGHT} expected: ${BITCOIN_MAJORITY_BLOCK_961632}"
    log_error "  block ${BITCOIN_SPLIT_HEIGHT} actual:   ${actual:-<none>}"
    log_error "Blocks mined against this chain are unlikely to have any value."

    # A pruned node cannot reorganise further back than its retained window (at
    # minimum the last 288 blocks). The majority chain is far more than 288
    # blocks past the split, so the block data needed to rewind is simply gone.
    # Clearing flags will not help; this is the one case that genuinely needs a
    # full resync, and saying so plainly beats letting it fail silently.
    if $cli getblockchaininfo 2>/dev/null | grep -q '"pruned"[[:space:]]*:[[:space:]]*true'; then
        echo ""
        log_error "This node is PRUNED. The blocks needed to rewind past the split have"
        log_error "been deleted, so it cannot reorganise onto the majority chain."
        log_error "A pruned node in this state requires a full resync from scratch."

        local _svc="${COIN_SERVICE[BTC]}.service"
        local _dd; _dd=$(get_data_dir BTC 2>/dev/null || true)
        [[ -n "$_dd" ]] || _dd="$(dirname "${COIN_CONF[BTC]}")"

        echo ""
        log_warn "Data directory: ${_dd}"
        if [[ -d "${_dd}/blocks" || -d "${_dd}/chainstate" || -d "${_dd}/indexes" ]]; then
            log_warn "To be removed:"
            local _d
            for _d in blocks chainstate indexes; do
                if [[ -d "${_dd}/${_d}" ]]; then
                    echo -e "  ${CYAN}${_dd}/${_d}${NC}   $(du -sh "${_dd}/${_d}" 2>/dev/null | cut -f1)"
                fi
            done
        fi
        # Wallets live in <datadir>/wallets and are NOT inside any directory
        # listed above, so a resync does not touch them. Say so explicitly: the
        # operator is about to be asked to delete things next to their money.
        if [[ -d "${_dd}/wallets" ]]; then
            echo -e "  ${GREEN}KEPT:${NC} ${_dd}/wallets   $(du -sh "${_dd}/wallets" 2>/dev/null | cut -f1)"
        fi
        echo ""
        log_warn "The node will then download the chain from genesis. That is days of work"
        log_warn "and BTC mines nothing until it completes."
        echo ""

        # Only offer to do it when a human is actually there. Piping this script
        # or running it from cron must never silently delete a chain.
        if [[ ! -t 0 ]]; then
            log_error "Not an interactive session — not offering to delete anything."
            log_error "Run these by hand once you have verified the paths above:"
            echo -e "  ${CYAN}sudo systemctl stop ${_svc}${NC}"
            echo -e "  ${CYAN}sudo rm -rf ${_dd}/blocks ${_dd}/chainstate ${_dd}/indexes${NC}"
            echo -e "  ${CYAN}sudo systemctl start ${_svc}${NC}"
            return 1
        fi

        printf "  Delete the chain data above and resync from genesis? Type %bRESYNC%b to confirm: " "${BOLD}" "${NC}"
        local _confirm; read -r _confirm
        if [[ "$_confirm" != "RESYNC" ]]; then
            log_info "Nothing deleted. Run these by hand when you are ready:"
            echo -e "  ${CYAN}sudo systemctl stop ${_svc}${NC}"
            echo -e "  ${CYAN}sudo rm -rf ${_dd}/blocks ${_dd}/chainstate ${_dd}/indexes${NC}"
            echo -e "  ${CYAN}sudo systemctl start ${_svc}${NC}"
            return 1
        fi

        # Copy the wallets aside before deleting anything. They are not in the
        # removal set, but this costs little and the failure being guarded
        # against is unrecoverable.
        if [[ -d "${_dd}/wallets" ]]; then
            local _wbak="${BACKUP_ROOT}/BTC-wallets-pre-resync-$(date '+%Y%m%d-%H%M%S')"
            mkdir -p "$_wbak"
            if cp -r "${_dd}/wallets/." "$_wbak/" 2>/dev/null; then
                chmod -R go-rwx "$_wbak" 2>/dev/null || true
                log_success "Wallets copied to ${_wbak}"
            else
                log_error "Could not copy wallets to ${_wbak} — refusing to delete anything."
                return 1
            fi
        fi

        log_step "Stopping ${_svc} and removing chain data"
        if ! ensure_daemon_stopped BTC; then
            log_error "Could not confirm the daemon stopped. Nothing deleted."
            return 1
        fi

        local _d
        for _d in blocks chainstate indexes; do
            [[ -d "${_dd}/${_d}" ]] || continue
            if sudo rm -rf "${_dd:?}/${_d}"; then
                log_success "Removed ${_dd}/${_d}"
            else
                log_error "Failed to remove ${_dd}/${_d} — stopping here."
                return 1
            fi
        done

        log_step "Starting ${_svc} to resync from genesis"
        if sudo systemctl start "$_svc"; then
            _mark_started "$_svc"
            log_success "${_svc} started — resyncing from genesis"
            log_warn "This takes days. BTC mines nothing until it completes; the stratum"
            log_warn "chain gate refuses to serve work until the chain verifies, so you are"
            log_warn "not burning electricity on a chain that cannot pay."
            echo -e "  Watch progress:  ${CYAN}bitcoin-cli getblockchaininfo | grep -E 'blocks|verificationprogress'${NC}"
        else
            log_error "${_svc} failed to start. Check: journalctl -u ${_svc} -n 50 --no-pager"
        fi
        return 1
    fi

    echo ""
    log_step "Clearing rejected-block flags and letting the node reorg"

    # Reconsider EVERY tip the node has marked invalid, not just the known
    # majority block. The enforcing binary may have flagged a different block
    # than the one we pin (it flags whatever it saw first), and any flagged
    # ancestor is enough to keep the whole branch out of consideration.
    local tip tips_cleared=0
    while read -r tip; do
        [[ -z "$tip" ]] && continue
        if $cli reconsiderblock "$tip" 2>/dev/null; then
            log "Reconsidered invalid tip: ${tip}"
            tips_cleared=$((tips_cleared + 1))
        fi
    done < <($cli getchaintips 2>/dev/null | tr -d ' ",' | awk -F: '/^hash:/{h=$2} /^status:invalid$/{print h}')

    # Belt and braces: also reconsider the pinned majority block directly, in
    # case it is buried rather than sitting at a tip.
    if $cli reconsiderblock "$BITCOIN_MAJORITY_BLOCK_961632" 2>/dev/null; then
        tips_cleared=$((tips_cleared + 1))
    fi

    if [[ "$tips_cleared" -eq 0 ]]; then
        log_warn "Nothing was reconsidered — the node may not have the majority block yet."
        log_warn "It must connect to majority-chain peers before it can fetch it."
    else
        log_success "Cleared ${tips_cleared} rejected-block flag(s)"
    fi

    # Disconnecting the minority branch is only ~2 blocks deep, but the node then
    # has to DOWNLOAD every majority block mined since the split — thousands of
    # blocks and several GB. That is not something to block an upgrade script on,
    # so poll briefly for the common case (blocks already present, reorg is
    # instant) and otherwise hand off with clear instructions. Mining safety does
    # not depend on this loop: the stratum preflight refuses to serve work until
    # the chain verifies, so a still-downloading node cannot mine the wrong chain.
    local start_height="$blocks"
    local waited=0
    while [[ $waited -lt 180 ]]; do
        sleep 10; waited=$((waited + 10))
        actual=$($cli getblockhash "$BITCOIN_SPLIT_HEIGHT" 2>/dev/null || echo "")
        if [[ "$actual" == "$BITCOIN_MAJORITY_BLOCK_961632" ]]; then
            log_success "BTC: reorged onto the majority chain (verified after ${waited}s)"
            return 0
        fi
    done

    # Not converged yet. Distinguish "downloading, working as intended" from
    # "wedged" — reporting a busy node as broken would send operators into an
    # unnecessary multi-hour reindex.
    local now_height; now_height=$($cli getblockcount 2>/dev/null || echo "0")
    local headers;    headers=$($cli getblockchaininfo 2>/dev/null | grep -oP '"headers"\s*:\s*\K[0-9]+' || echo "0")

    echo ""
    if [[ "$now_height" -gt "$start_height" || "$headers" -gt "$now_height" ]]; then
        log_warn "BTC: reorg accepted, node is downloading the majority chain."
        log_warn "  height ${start_height} → ${now_height}, headers known: ${headers}"
        log_warn "This is expected: every block mined since the split must be fetched"
        log_warn "(thousands of blocks, several GB). It will finish on its own."
        echo -e "  ${CYAN}Monitor:  $cli getblockchaininfo | grep -E 'blocks|headers|verificationprogress'${NC}"
        log_warn "The pool will not mine BTC until the chain verifies."
        return 0
    fi

    log_error "BTC is STILL on a minority chain and is making no progress."
    log_error "  height stuck at ${now_height}, headers known: ${headers}"
    log_error "Do not mine against this node — it will not produce blocks of value."
    log_error "If it has no majority-chain peers it cannot fetch the real chain; check"
    log_error "connectivity first:"
    echo -e "  ${CYAN}$cli getpeerinfo | grep -c subver${NC}"
    log_error "Otherwise rerun with a full reindex, which rebuilds the block index from"
    log_error "local block files and clears every rejected-block flag:"
    echo -e "  ${CYAN}sudo /spiralpool/scripts/coin-upgrade.sh --coin BTC --reindex${NC}"
    return 1
}

# ═══════════════════════════════════════════════════════════════════════════════
# STATUS TABLE
# ═══════════════════════════════════════════════════════════════════════════════

risk_label() {
    case "$1" in
        PATCH) echo -e "${GREEN}PATCH${NC}  — low risk, no reindex expected" ;;
        MINOR) echo -e "${YELLOW}MINOR${NC}  — medium risk, reindex may be needed" ;;
        MAJOR) echo -e "${RED}MAJOR${NC}  — high risk, reindex likely required" ;;
        NONE)  echo -e "${DIM}─      — no upgrade available${NC}" ;;
        *)     echo -e "${DIM}UNKNOWN${NC}" ;;
    esac
}

# Scan every coin's systemd service dir for a leftover reindex-once.conf drop-in.
# A stale one silently forces a full chainstate rebuild on the next daemon restart
# (observed: a drop-in from an earlier MAJOR upgrade firing days later). The upgrade
# path removes it automatically before starting, but surface it here so operators
# see it on boxes they haven't upgraded yet and can clear it proactively.
warn_stale_reindex_dropins() {
    local coin found=false
    for coin in "${ALL_COINS[@]}"; do
        local dropin="/etc/systemd/system/${COIN_SERVICE[$coin]}.service.d/reindex-once.conf"
        [[ -f "$dropin" ]] || continue
        if [[ "$found" == "false" ]]; then
            echo -e "  ${YELLOW}⚠  Stale reindex drop-in(s) detected${NC} — these force a full chainstate"
            echo -e "     rebuild on the next daemon restart. Upgrading the coin clears it"
            echo -e "     automatically; to clear one now without upgrading, run:"
            found=true
        fi
        echo -e "       ${DIM}${coin}:${NC} sudo rm -f ${dropin} && sudo systemctl daemon-reload"
    done
    [[ "$found" == "true" ]] && echo ""
    return 0
}

show_version_table() {
    echo ""
    echo -e "${CYAN}╔════════════════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║${NC}${WHITE}               Spiral Pool — Coin Daemon Version Status                     ${NC}${CYAN}║${NC}"
    echo -e "${CYAN}╚════════════════════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    printf "  ${WHITE}%-6s  %-24s  %-24s  %-7s  %s${NC}\n" "COIN" "INSTALLED" "TARGET" "ENABLED" "STATUS"
    echo -e "  ${DIM}──────  ────────────────────────  ────────────────────────  ───────  ─────────────────────────────${NC}"

    local has_upgrade=false
    for coin in "${ALL_COINS[@]}"; do
        local installed_ver risk enabled status_str
        installed_ver=$(get_installed_version "$coin")
        risk="${COIN_RISK[$coin]}"
        enabled="${DIM}no${NC}"
        is_coin_enabled "$coin" && enabled="${GREEN}YES${NC}"

        if [[ "$installed_ver" == "not_installed" ]]; then
            status_str="${DIM}not installed${NC}"
        elif [[ "$installed_ver" == "unknown" ]]; then
            # Binary exists but reports no parseable version.
            #
            # Do NOT write the target into the cache here. That assumed "no
            # upgrade defined means it must already be at target", which is only
            # true if the pin is current — and it permanently certified whatever
            # was on disk as up to date, hiding exactly the stale-pin case this
            # column exists to surface. Report the uncertainty instead.
            if [[ "$risk" == "NONE" ]]; then
                status_str="${YELLOW}? version unknown${NC}"
            else
                # Upgrade is available but we can't verify current — proceed conservatively
                has_upgrade=true
                case "$risk" in
                    PATCH) status_str="${GREEN}↑ PATCH available${NC}" ;;
                    MINOR) status_str="${YELLOW}↑ MINOR available${NC}" ;;
                    MAJOR) status_str="${RED}↑ MAJOR available${NC}" ;;
                    *)     status_str="${YELLOW}↑ update available${NC}" ;;
                esac
            fi
        elif _ver_matches "$installed_ver" "${COIN_TARGET[$coin]}"; then
            # Compare VERSIONS, never the risk class. `risk == NONE` used to
            # short-circuit to "current" here regardless of what was actually
            # installed, which made COIN_RISK double as an "is up to date" flag.
            # A stale pin, a missed consensus upgrade, or a node left on an old
            # build then displayed "✓ current" and was never offered an upgrade
            # — precisely how a BTC node sat on an RDTS-enforcing Knots build
            # while every status surface read healthy.
            status_str="${GREEN}✓ current${NC}"
        else
            has_upgrade=true
            case "$risk" in
                PATCH) status_str="${GREEN}↑ PATCH available${NC}" ;;
                MINOR) status_str="${YELLOW}↑ MINOR available${NC}" ;;
                MAJOR) status_str="${RED}↑ MAJOR available${NC}" ;;
                *)     status_str="${YELLOW}↑ update available${NC}" ;;
            esac
        fi

        printf "  %-6s  %-24s  %-24s  " \
            "$coin" "$installed_ver" "${COIN_TARGET[$coin]}"
        printf "%-7b  " "$enabled"
        echo -e "$status_str"
    done

    echo ""
    if [[ "$has_upgrade" == "false" ]]; then
        echo -e "  ${GREEN}All coin daemons are at their target versions.${NC}"
        echo ""
    fi
    warn_stale_reindex_dropins
}

# Machine-readable upgrade list for external callers (upgrade.sh).
# Outputs one line per available upgrade: "COIN INSTALLED TARGET RISK"
# No colors, no banner, no headers — safe to capture with $().
list_upgrades() {
    for coin in "${ALL_COINS[@]}"; do
        local risk="${COIN_RISK[$coin]:-NONE}"
        [[ "$risk" == "NONE" ]] && continue
        local installed_ver
        installed_ver=$(get_installed_version "$coin")
        [[ "$installed_ver" == "not_installed" ]] && continue
        # _ver_matches, not ==: Bitcoin Core reports "31.1.0" for release "31.1".
        # A plain compare would list a correctly-upgraded node as pending, and
        # upgrade.sh keys its BTC chain-split notice off this output.
        _ver_matches "$installed_ver" "${COIN_TARGET[$coin]}" && continue  # already at target
        echo "$coin $installed_ver ${COIN_TARGET[$coin]} $risk"
    done
}

# ═══════════════════════════════════════════════════════════════════════════════
# INTERACTIVE SELECTION
# ═══════════════════════════════════════════════════════════════════════════════

interactive_mode() {
    local upgradeable=()
    for coin in "${ALL_COINS[@]}"; do
        [[ "${COIN_RISK[$coin]}" == "NONE" ]] && continue
        local _iv; _iv=$(get_installed_version "$coin")
        # _ver_matches, not ==: Bitcoin Core reports "31.1.0" for release "31.1",
        # so a plain compare marks a correctly-upgraded node as still pending and
        # fires the chain-split notice at an operator who has nothing wrong.
        [[ "$_iv" == "not_installed" ]] && continue
        _ver_matches "$_iv" "${COIN_TARGET[$coin]}" && continue
        upgradeable+=("$coin")
    done

    if [[ ${#upgradeable[@]} -eq 0 ]]; then
        echo -e "  ${GREEN}Nothing to upgrade — all daemons are current.${NC}\n"
        return 0
    fi

    echo -e "  ${WHITE}Available upgrades:${NC}\n"
    for i in "${!upgradeable[@]}"; do
        local c="${upgradeable[$i]}"
        local _installed; _installed=$(get_installed_version "$c")
        printf "    ${CYAN}%s${NC}.  ${WHITE}%-6s${NC}  %s  →  %s   " \
            "$((i+1))" "$c" "$_installed" "${COIN_TARGET[$c]}"
        echo -e "$(risk_label "${COIN_RISK[$c]}")"
    done
    echo -e "    ${CYAN}a${NC}.  All of the above"
    echo -e "    ${CYAN}q${NC}.  Quit"
    echo ""
    printf "  Selection: "
    local choice; read -r choice

    local selected=()
    case "${choice,,}" in
        q)   log_info "Cancelled"; return 0 ;;
        a)   selected=("${upgradeable[@]}") ;;
        [0-9]*)
            local idx=$(( choice - 1 ))
            if [[ "$idx" -ge 0 && "$idx" -lt "${#upgradeable[@]}" ]]; then
                selected=("${upgradeable[$idx]}")
            else
                die "Invalid selection: ${choice}"
            fi
            ;;
        *)   die "Invalid selection: ${choice}" ;;
    esac

    # upgrade_coin can return non-zero (BTC chain verification failed, hazardous
    # bitcoin.conf, etc). A for-loop body is NOT protected by errexit, so an
    # unguarded call would abort the whole run on the first failure — and BTC is
    # first in ALL_COINS, so a BTC problem would silently prevent every other
    # coin from being upgraded, with nothing printed to say so. Record the
    # failure, keep going, and report at the end.
    local _failed=()
    for coin in "${selected[@]}"; do
        upgrade_coin "$coin" "false" || _failed+=("$coin")
    done

    if [[ ${#_failed[@]} -gt 0 ]]; then
        echo ""
        log_error "The following coin(s) did not complete cleanly: ${_failed[*]}"
        log_error "Scroll up for the reason. Other coins were still upgraded."
        return 1
    fi
    return 0
}

# ═══════════════════════════════════════════════════════════════════════════════
# BANNER
# ═══════════════════════════════════════════════════════════════════════════════

print_banner() {
    echo ""
    echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║${NC}${WHITE}         SPIRAL POOL — COIN DAEMON UPGRADE UTILITY            ${NC}${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}${DIM}                       V2.7.0-SPIRAL_CITADEL                  ${NC}${CYAN}║${NC}"
    echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
    echo -e "${CYAN}║${NC}  ${YELLOW}⚠  Manual operation — never run via automation${NC}              ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}  ${DIM}Only the daemon binary is replaced. Config, wallets,${NC}        ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}  ${DIM}and blockchain data are never touched.${NC}                      ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                              ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}  ${DIM}To ADD a coin: ${WHITE}spiralctl coin enable <SYMBOL>${NC}               ${CYAN}║${NC}"
    echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN
# ═══════════════════════════════════════════════════════════════════════════════

main() {
    local mode="interactive"
    local target_coin=""
    local do_reindex="false"

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --check)    mode="check" ;;
            --list)     mode="list" ;;
            --coin)     shift; target_coin="${1:-}"; mode="single" ;;
            --reindex)  do_reindex="true" ;;
            --help|-h)
                echo ""
                echo "  Usage: sudo /spiralpool/scripts/coin-upgrade.sh [OPTIONS]"
                echo ""
                echo "  Options:"
                echo "    --check           Show version status table only, no changes"
                echo "    --list            Machine-readable upgrade list (COIN VER TARGET RISK)"
                echo "    --coin TICKER     Upgrade a specific coin (e.g. DGB, LTC)"
                echo "    --reindex         Start daemon with -reindex after upgrade"
                echo "    --help            Show this help"
                echo ""
                echo "  Examples:"
                echo "    sudo /spiralpool/scripts/coin-upgrade.sh --check"
                echo "    sudo /spiralpool/scripts/coin-upgrade.sh --coin NMC --reindex"
                echo ""
                echo "  Note: This tool upgrades existing coin daemon binaries only."
                echo "  To ADD a new coin to your pool:  spiralctl coin enable <SYMBOL>"
                echo ""
                exit 0
                ;;
            *) die "Unknown argument: $1. Use --help for usage." ;;
        esac
        [[ $# -gt 0 ]] && shift
    done

    # --list: machine-readable output only — skip banner, root check, and ENV check
    if [[ "$mode" == "list" ]]; then
        list_upgrades
        exit 0
    fi

    print_banner
    check_root
    [[ -f "$ENV_FILE" ]] || die "Pool .env not found at ${ENV_FILE} — is Spiral Pool installed?"

    case "$mode" in
        check)
            show_version_table
            ;;
        single)
            [[ -z "$target_coin" ]] && die "No coin specified after --coin"
            target_coin="${target_coin^^}"
            [[ -v COIN_TARGET["$target_coin"] ]] || \
                die "Unknown coin: ${target_coin}. Valid tickers: ${ALL_COINS[*]}"
            show_version_table
            upgrade_coin "$target_coin" "$do_reindex"
            ;;
        interactive)
            show_version_table
            interactive_mode
            ;;
    esac
}

main "$@"
