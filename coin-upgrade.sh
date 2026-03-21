#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# coin-upgrade.sh — Spiral Pool Coin Daemon Upgrade Utility
#                   V1.1.1-PHI_FORGE
#
# Upgrades coin node binaries in-place. Touches ONLY the binary.
# Config files, wallets, blockchain data, and pool settings are NEVER modified.
#
# This is a MANUAL, OPERATOR-INITIATED operation — never automated by upgrade.sh
# or Sentinel auto-update. Coin daemon upgrades may require a full chain reindex
# and must be supervised.
#
# Usage:
#   sudo ./coin-upgrade.sh                    # Interactive mode
#   sudo ./coin-upgrade.sh --check            # Show version status only, no changes
#   sudo ./coin-upgrade.sh --coin QBX         # Upgrade specific coin
#   sudo ./coin-upgrade.sh --coin NMC --reindex   # Upgrade + start with -reindex
#
# Risk levels:
#   PATCH   Binary swap only — reindex not expected (e.g. QBX 0.1.0 → 0.2.0)
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
WORK_DIR="/tmp/spiral-coin-upgrade-$$"
MAINTENANCE_SCRIPT="$INSTALL_DIR/scripts/linux/maintenance-mode.sh"
MAINTENANCE_ENABLED=false

# Multi-disk: read CHAIN_MOUNT_POINT from coins.env if present
CHAIN_MOUNT_POINT=""
if [[ -f "$ENV_FILE" ]]; then
    CHAIN_MOUNT_POINT=$(grep -oP '^CHAIN_MOUNT_POINT="\K[^"]*' "$ENV_FILE" 2>/dev/null || echo "")
fi

# ── Target versions — keep in sync with install.sh lines 41-46 ────────────────
declare -A COIN_TARGET=(
    [BTC]="29.3.knots20260210"
    [BCH]="29.0.0"
    [BC2]="29.1.0"
    [DGB]="8.26.2"
    [LTC]="0.21.4"
    [DOGE]="1.14.9"
    [PEP]="1.1.0"
    [CAT]="2.1.1"
    [NMC]="28.0"        # nc30.2 on GitHub has no binary assets yet — cannot upgrade
    [SYS]="5.0.5"
    [XMY]="0.18.1.0"
    [FBTC]="0.3.0"
    [QBX]="0.2.0"
)

# Risk classification for this upgrade cycle.
# Update alongside COIN_TARGET when new versions become available.
# NONE   = already at target (or no upgrade available)
# PATCH  = binary-only change; no reindex expected
# MINOR  = may need reindex; check release notes
# MAJOR  = reindex almost certainly required; use --reindex
declare -A COIN_RISK=(
    [BTC]="NONE"    # 29.3.knots20260210 — current
    [BCH]="NONE"    # 29.0.0 — current
    [BC2]="NONE"    # 29.1.0 — current
    [DGB]="NONE"    # 8.26.2 — current
    [LTC]="NONE"    # 0.21.4 — current
    [DOGE]="NONE"   # 1.14.9 — current
    [PEP]="NONE"    # 1.1.0  — current
    [CAT]="NONE"    # 2.1.1  — current
    [NMC]="NONE"    # 28.0   — nc30.2 has no binaries yet
    [SYS]="NONE"    # 5.0.5  — current
    [XMY]="NONE"    # 0.18.1.0 — current (project dormant since 2020)
    [FBTC]="NONE"   # 0.3.0  — current
    [QBX]="PATCH"   # 0.1.0 → 0.2.0: LevelDB static build only; no reindex expected
)

# systemd service unit names
declare -A COIN_SERVICE=(
    [BTC]="bitcoind"        [BCH]="bitcoind-bch"    [BC2]="bitcoiniid"
    [DGB]="digibyted"       [LTC]="litecoind"       [DOGE]="dogecoind"
    [PEP]="pepecoind"       [CAT]="catcoind"        [NMC]="namecoind"
    [SYS]="syscoind"        [XMY]="myriadcoind"     [FBTC]="fractald"
    [QBX]="qbitxd"
)

# /usr/local/bin daemon command (symlink name)
declare -A COIN_DAEMON_CMD=(
    [BTC]="bitcoind"        [BCH]="bitcoind-bch"    [BC2]="bitcoiniid"
    [DGB]="digibyted"       [LTC]="litecoind"       [DOGE]="dogecoind"
    [PEP]="pepecoind"       [CAT]="catcoind"        [NMC]="namecoind"
    [SYS]="syscoind"        [XMY]="myriadcoind"     [FBTC]="fractald"
    [QBX]="qbitx"
)

# /usr/local/bin CLI command (symlink name)
declare -A COIN_CLI_CMD=(
    [BTC]="bitcoin-cli"     [BCH]="bitcoin-cli"     [BC2]="bitcoin-cli"
    [DGB]="digibyte-cli"    [LTC]="litecoin-cli"    [DOGE]="dogecoin-cli"
    [PEP]="pepecoin-cli"    [CAT]="catcoin-cli"     [NMC]="namecoin-cli"
    [SYS]="syscoin-cli"     [XMY]="myriadcoin-cli"  [FBTC]="fractal-cli"
    [QBX]="qbitx-cli"
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
    [BC2]="$(_chain_dir bc2)/bitcoinii.conf"
    [DGB]="$(_chain_dir dgb)/digibyte.conf"
    [LTC]="$(_chain_dir ltc)/litecoin.conf"
    [DOGE]="$(_chain_dir doge)/dogecoin.conf"
    [PEP]="$(_chain_dir pep)/pepecoin.conf"
    [CAT]="$(_chain_dir cat)/catcoin.conf"
    [NMC]="$(_chain_dir nmc)/namecoin.conf"
    [SYS]="$(_chain_dir sys)/syscoin.conf"
    [XMY]="$(_chain_dir xmy)/myriadcoin.conf"
    [FBTC]="$(_chain_dir fbtc)/fractal.conf"
    [QBX]="$(_chain_dir qbx)/qbitx.conf"
)

# Build full CLI command with -conf flag
get_coin_cli() {
    local coin="$1"
    echo "${COIN_CLI_CMD[$coin]} -conf=${COIN_CONF[$coin]}"
}

# .env flag to check if a coin is enabled on this node
declare -A COIN_ENV_FLAG=(
    [BTC]="ENABLE_BTC"  [BCH]="ENABLE_BCH"  [BC2]="ENABLE_BC2"
    [DGB]="ENABLE_DGB"  [LTC]="ENABLE_LTC"  [DOGE]="ENABLE_DOGE"
    [PEP]="ENABLE_PEP"  [CAT]="ENABLE_CAT"  [NMC]="ENABLE_NMC"
    [SYS]="ENABLE_SYS"  [XMY]="ENABLE_XMY"  [FBTC]="ENABLE_FBTC"
    [QBX]="ENABLE_QBX"
)

ALL_COINS=(BTC BCH BC2 DGB LTC DOGE PEP CAT NMC SYS XMY FBTC QBX)

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
    if [[ "$MAINTENANCE_ENABLED" == "true" ]]; then
        disable_maintenance 2>/dev/null || true
    fi
}
trap cleanup EXIT

# ═══════════════════════════════════════════════════════════════════════════════
# UTILITIES
# ═══════════════════════════════════════════════════════════════════════════════

check_root() {
    [[ "$EUID" -eq 0 ]] || die "Must be run as root:  sudo ./coin-upgrade.sh $*"
}

get_system_arch() {
    case "$(uname -m)" in
        x86_64)  echo "x86_64" ;;
        aarch64) echo "aarch64" ;;
        *)       die "Unsupported architecture: $(uname -m)" ;;
    esac
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

    # Check version cache first — used for daemons whose --version omits the number (e.g. QBX)
    local cache_file="${VERSION_CACHE_DIR}/${coin}.ver"
    if [[ -f "$cache_file" ]]; then
        cat "$cache_file"
        return
    fi

    local raw
    raw=$("$bin_path" --version 2>/dev/null | head -1) || { echo "error"; return; }
    # Extracts versions like 29.3.knots20260210, 0.21.4, 0.2.0, 8.26.2, 0.18.1.0
    echo "$raw" | grep -oP '(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*' | head -1 \
        || echo "unknown"
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
        "$MAINTENANCE_SCRIPT" enable "coin-upgrade" 2>/dev/null || true
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
    local deadline=$(( SECONDS + 120 ))
    log_info "Waiting for ${coin} daemon to respond (up to 120s)..."
    while [[ $SECONDS -lt $deadline ]]; do
        if $cli getblockchaininfo &>/dev/null; then
            log_success "${coin} daemon is responding"
            return 0
        fi
        sleep 3
    done
    log_warn "${coin} did not respond within 120s — may still be starting or reindexing"
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

download_BTC() {
    local arch="$1" ver="${COIN_TARGET[BTC]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="bitcoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://bitcoinknots.org/files/29.x/${ver}/${fn}" 2>/dev/null \
        || _wget -O "$fn" "https://github.com/bitcoinknots/bitcoin/releases/download/v${ver}/${fn}" \
        || return 1
    tar -xzf "$fn" || return 1
    echo "bitcoin-${ver}"
}

download_BCH() {
    local arch="$1" ver="${COIN_TARGET[BCH]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="bitcoin-cash-node-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/bitcoin-cash-node/bitcoin-cash-node/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "bitcoin-cash-node-${ver}"
}

download_BC2() {
    local arch="$1" ver="${COIN_TARGET[BC2]}"
    local sfx="x86_64-linux-CLI"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-CLI"
    local fn="BitcoinII-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/Bitcoin-II/BitcoinII-Core/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    ls -d BitcoinII-* 2>/dev/null | head -1 || echo "."
}

download_DGB() {
    local arch="$1" ver="${COIN_TARGET[DGB]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
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
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="litecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/litecoin-project/litecoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "litecoin-${ver}"
}

download_DOGE() {
    local arch="$1" ver="${COIN_TARGET[DOGE]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="dogecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/dogecoin/dogecoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "dogecoin-${ver}"
}

download_PEP() {
    local arch="$1" ver="${COIN_TARGET[PEP]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="pepecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/pepecoinppc/pepecoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "pepecoin-${ver}"
}

download_CAT() {
    local arch="$1" ver="${COIN_TARGET[CAT]}"
    local zip_name="Catcoin-Linux.zip"; local dir_name="Catcoin-Linux"
    [[ "$arch" == "aarch64" ]] && zip_name="Catcoin-AArch.zip" && dir_name="Catcoin-AArch"
    cd "$WORK_DIR"
    _wget -O catcoin.zip "https://github.com/CatcoinCore/catcoincore/releases/download/v${ver}/${zip_name}" || return 1
    unzip -q catcoin.zip || return 1
    echo "$dir_name"
}

download_NMC() {
    local arch="$1" ver="${COIN_TARGET[NMC]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="namecoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://www.namecoin.org/files/namecoin-core/namecoin-core-${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "namecoin-${ver}"
}

download_SYS() {
    local arch="$1" ver="${COIN_TARGET[SYS]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="syscoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/syscoin/syscoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "syscoin-${ver}"
}

download_XMY() {
    local arch="$1" ver="${COIN_TARGET[XMY]}"
    local sfx="x86_64-linux-gnu"; [[ "$arch" == "aarch64" ]] && sfx="aarch64-linux-gnu"
    local fn="myriadcoin-${ver}-${sfx}.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/myriadteam/myriadcoin/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "myriadcoin-${ver}"
}

download_FBTC() {
    local arch="$1" ver="${COIN_TARGET[FBTC]}"
    [[ "$arch" == "aarch64" ]] && { log_error "Fractal Bitcoin has no ARM64 binaries"; return 1; }
    local fn="fractald-${ver}-x86_64-linux-gnu.tar.gz"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/fractal-bitcoin/fractald-release/releases/download/v${ver}/${fn}" || return 1
    tar -xzf "$fn" || return 1
    echo "fractald-${ver}-x86_64-linux-gnu"
}

download_QBX() {
    local arch="$1" ver="${COIN_TARGET[QBX]}"
    [[ "$arch" == "aarch64" ]] && { log_error "Q-BitX has no ARM64 binaries"; return 1; }
    local fn="qbitx-linux-x86_64-v${ver}.zip"
    cd "$WORK_DIR"
    _wget -O "$fn" "https://github.com/q-bitx/Source-/releases/download/v${ver}/${fn}" || return 1
    unzip -q "$fn" -d qbitx-extract || return 1
    echo "qbitx-extract"
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
        QBX)
            # QBX has no bin/ subdir — binaries at root of extract
            local d; d=$(find "$extracted" -name "qbitx" -not -name "*-cli" -type f | head -1)
            local c; c=$(find "$extracted" -name "qbitx-cli" -type f | head -1)
            [[ -z "$d" ]] && die "qbitx binary not found in archive"
            sudo install -m 755 -o "$POOL_USER" -g "$POOL_USER" "$d" "$bin_dir/qbitx"
            [[ -n "$c" ]] && sudo install -m 755 -o "$POOL_USER" -g "$POOL_USER" "$c" "$bin_dir/qbitx-cli" || true
            ;;
        BC2)
            # Bitcoin II: capital "II" in binary names
            find "$extracted" -type f \( -name "bitcoinIId" -o -name "bitcoinII-cli" \) \
                -exec sudo install -m 755 -o "$POOL_USER" -g "$POOL_USER" {} "$bin_dir/" \;
            ;;
        BCH|FBTC)
            # These have bin/ subdir but daemon binary is named bitcoind (not bch/fbtc-specific)
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
            sudo cp -r "$src_bin"/. "$bin_dir/"
            sudo chown -R "$POOL_USER:$POOL_USER" "$bin_dir"
            sudo chmod -R 755 "$bin_dir"
            ;;
    esac
    log_success "Binaries installed"
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
        if [[ -d "${data_dir}/wallets" ]]; then
            sudo cp -r "${data_dir}/wallets" "${backup_dir}/wallets-backup" 2>/dev/null || true
            log_success "Wallets backed up → ${backup_dir}/wallets-backup" >&2
        elif [[ -f "${data_dir}/wallet.dat" ]]; then
            sudo cp "${data_dir}/wallet.dat" "${backup_dir}/wallet.dat" 2>/dev/null || true
            log_success "wallet.dat backed up → ${backup_dir}/wallet.dat" >&2
        fi
    fi

    echo "$backup_dir"
}

# ═══════════════════════════════════════════════════════════════════════════════
# ROLLBACK
# ═══════════════════════════════════════════════════════════════════════════════

rollback_coin() {
    local coin="$1" backup_dir="$2" svc="${COIN_SERVICE[$1]}.service"
    log_warn "Rolling back ${coin} to backed-up binaries..."
    local bin_dir; bin_dir=$(get_binary_dir "$coin")
    if [[ -d "${backup_dir}/bin-backup" && -n "$bin_dir" ]]; then
        sudo cp -r "${backup_dir}/bin-backup/." "$bin_dir/" 2>/dev/null || true
        log_success "Rollback complete"
    fi
    sudo systemctl start "$svc" 2>/dev/null || log_warn "Could not restart ${svc} after rollback"
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

    if [[ "$installed_ver" == "$target_ver" ]]; then
        log_info "${coin} is already at ${target_ver} — nothing to do"
        return 0
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
        log_warn "database errors, rerun with:  sudo ./coin-upgrade.sh --coin ${coin} --reindex"
    fi

    echo ""
    printf "  Proceed with %s upgrade? [y/N] " "$coin"
    local confirm; read -r confirm
    [[ "$confirm" =~ ^[Yy]$ ]] || { log_info "Skipped ${coin}"; return 0; }

    mkdir -p "$WORK_DIR"
    local backup_path

    # ── Step 1: Backup ────────────────────────────────────────────────────────
    log_step "Step 1/5 — Backup ${coin}"
    backup_path=$(backup_coin "$coin")
    log_success "Backup: ${backup_path}"

    # ── Step 2: Maintenance mode ──────────────────────────────────────────────
    log_step "Step 2/5 — Enable maintenance mode"
    enable_maintenance

    # ── Step 3: Stop daemon ───────────────────────────────────────────────────
    log_step "Step 3/5 — Stop ${coin} daemon"
    if systemctl is-active --quiet "$svc" 2>/dev/null; then
        sudo systemctl stop "$svc"
        log_success "${svc} stopped"
    else
        log_info "${svc} was not running"
    fi

    # ── Step 4: Download + install ────────────────────────────────────────────
    log_step "Step 4/5 — Download ${coin} ${target_ver}"
    local extracted_dir
    if ! extracted_dir=$(download_"${coin}" "$arch"); then
        log_error "Download failed for ${coin}"
        rollback_coin "$coin" "$backup_path"
        disable_maintenance
        return 1
    fi

    log_step "Step 4/5 — Install ${coin} binaries"
    if ! install_binaries "$coin" "$extracted_dir"; then
        log_error "Binary installation failed for ${coin}"
        rollback_coin "$coin" "$backup_path"
        disable_maintenance
        return 1
    fi

    # ── Step 5: Start daemon ──────────────────────────────────────────────────
    log_step "Step 5/5 — Start ${coin} daemon"

    if [[ "$do_reindex" == "true" ]]; then
        # Write a systemd drop-in that appends -reindex to ExecStart.
        # The drop-in is removed immediately after start so the next
        # automatic restart (after reindex completes) runs without it.
        local dropin_dir="/etc/systemd/system/${svc}.d"
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
        sudo systemctl start "$svc"

        # Remove the reindex drop-in immediately — daemon keeps running,
        # next restart (post-reindex) will be clean.
        sleep 5
        sudo rm -f "${dropin_dir}/reindex-once.conf"
        [[ -d "$dropin_dir" ]] && sudo rmdir --ignore-fail-on-non-empty "$dropin_dir"
        sudo systemctl daemon-reload

        echo ""
        log_warn "Reindex in progress — this may take hours depending on chain size."
        log_warn "Monitor progress with:"
        echo -e "  ${CYAN}$(get_coin_cli "$coin") getblockchaininfo | grep -E 'blocks|headers|verificationprogress'${NC}"
        echo -e "  ${CYAN}sudo journalctl -u ${svc} -f${NC}"
        log_warn "The pool stratum will automatically reconnect when the daemon is healthy."
    else
        sudo systemctl start "$svc"
        log_success "${svc} started"
        wait_for_daemon "$coin"
    fi

    # ── Version verify ────────────────────────────────────────────────────────
    # Update cache FIRST — the binary was just installed, so the cache should
    # reflect the target version. This is essential for daemons like QBX whose
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

    if [[ "$bin_ver" == "$target_ver" ]]; then
        # Binary confirms the target version
        log_success "${coin}: ${installed_ver} → ${target_ver} ✓"
    elif [[ -z "$bin_ver" ]]; then
        # Daemon doesn't report a version number (e.g. QBX) — trust the install
        log_success "${coin}: ${installed_ver} → ${target_ver} ✓ (version cached — daemon has no version output)"
    else
        # Binary reports a different version than expected
        log_warn "Binary reports '${bin_ver}' — expected '${target_ver}'. Verify manually."
    fi

    disable_maintenance

    echo ""
    log_success "Backup preserved at: ${backup_path}"
    echo -e "  ${DIM}To restore if needed: sudo cp -r ${backup_path}/bin-backup/. \$(readlink -f \$(which ${COIN_DAEMON_CMD[$coin]}) | xargs dirname)/${NC}"
    echo ""
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
            # Binary exists but doesn't report a version — seed cache with target if risk is NONE
            # (safe assumption: if no upgrade is defined, it must already be at target)
            if [[ "$risk" == "NONE" ]]; then
                write_version_cache "$coin" "${COIN_TARGET[$coin]}"
                installed_ver="${COIN_TARGET[$coin]}"
                status_str="${GREEN}✓ current${NC}"
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
        elif [[ "$risk" == "NONE" || "$installed_ver" == "${COIN_TARGET[$coin]}" ]]; then
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
        [[ "$installed_ver" == "${COIN_TARGET[$coin]}" ]] && continue  # already at target
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
        [[ "$_iv" == "not_installed" || "$_iv" == "${COIN_TARGET[$coin]}" ]] && continue
        upgradeable+=("$coin")
    done

    if [[ ${#upgradeable[@]} -eq 0 ]]; then
        echo -e "  ${GREEN}Nothing to upgrade — all daemons are current.${NC}\n"
        return 0
    fi

    echo -e "  ${WHITE}Available upgrades:${NC}\n"
    for i in "${!upgradeable[@]}"; do
        local c="${upgradeable[$i]}"
        printf "    ${CYAN}%s${NC}.  ${WHITE}%-6s${NC}  %s  →  %s   " \
            "$((i+1))" "$c" "${COIN_TARGET[$c]}" ""
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

    for coin in "${selected[@]}"; do
        upgrade_coin "$coin" "false"
    done
}

# ═══════════════════════════════════════════════════════════════════════════════
# BANNER
# ═══════════════════════════════════════════════════════════════════════════════

print_banner() {
    echo ""
    echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║${NC}${WHITE}         SPIRAL POOL — COIN DAEMON UPGRADE UTILITY            ${NC}${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}${DIM}                       V1.1.1-PHI_FORGE                       ${NC}${CYAN}║${NC}"
    echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
    echo -e "${CYAN}║${NC}  ${YELLOW}⚠  Manual operation — never run via automation${NC}              ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}  ${DIM}Only the daemon binary is replaced. Config, wallets,${NC}        ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}  ${DIM}and blockchain data are never touched.${NC}                      ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                              ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}  ${DIM}To ADD a coin: ${WHITE}spiralctl coin enable <SYMBOL>${NC}              ${CYAN}║${NC}"
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
                echo "  Usage: sudo ./coin-upgrade.sh [OPTIONS]"
                echo ""
                echo "  Options:"
                echo "    --check           Show version status table only, no changes"
                echo "    --list            Machine-readable upgrade list (COIN VER TARGET RISK)"
                echo "    --coin TICKER     Upgrade a specific coin (e.g. QBX, DGB, LTC)"
                echo "    --reindex         Start daemon with -reindex after upgrade"
                echo "    --help            Show this help"
                echo ""
                echo "  Examples:"
                echo "    sudo ./coin-upgrade.sh --check"
                echo "    sudo ./coin-upgrade.sh --coin QBX"
                echo "    sudo ./coin-upgrade.sh --coin NMC --reindex"
                echo ""
                echo "  Note: This tool upgrades existing coin daemon binaries only."
                echo "  To ADD a new coin to your pool:  spiralctl coin enable <SYMBOL>"
                echo ""
                exit 0
                ;;
            *) die "Unknown argument: $1. Use --help for usage." ;;
        esac
        shift
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
