#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#===============================================================================
# SPIRALCTL - Spiral Pool Control Utility
# Unified management tool for Spiral Stratum Pool
#===============================================================================

set -e

# ── etcd credentials ─────────────────────────────────────────────────────────
# SECURITY (etcd-auth): Load etcd root password if authentication is configured.
# Written by the installer to /spiralpool/config/etcd-auth.conf (mode 640,
# root-owned, spiralpool-group-readable). Sets ETCDCTL_USER so all etcdctl calls
# in this script authenticate automatically without per-call --user flags.
if [[ -f /spiralpool/config/etcd-auth.conf ]]; then
    # shellcheck source=/dev/null
    source /spiralpool/config/etcd-auth.conf
    [[ -n "${ETCD_ROOT_PASS:-}" ]] && export ETCDCTL_USER="root:${ETCD_ROOT_PASS}"
fi
export ETCDCTL_API=3

# ── Patroni REST API credentials ─────────────────────────────────────────────
# SECURITY (patroni-auth): Load Patroni REST API credentials if configured.
# Written by the installer to /spiralpool/config/patroni-api.conf (mode 600,
# root-only). Populates PATRONI_CURL_AUTH for all curl calls to Patroni REST API.
PATRONI_CURL_AUTH=()
if [[ -f /spiralpool/config/patroni-api.conf ]]; then
    # shellcheck source=/dev/null
    source /spiralpool/config/patroni-api.conf
    [[ -n "${PATRONI_API_USERNAME:-}" ]] && PATRONI_CURL_AUTH=(-u "${PATRONI_API_USERNAME}:${PATRONI_API_PASSWORD}")
fi

INSTALL_DIR="${INSTALL_DIR:-/spiralpool}"
VERSION="$(cat "$INSTALL_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
VERSION="${VERSION:-2.1.0}"
CONFIG_FILE="$INSTALL_DIR/config/config.yaml"
POOL_USER="${POOL_USER:-spiraluser}"

# Multi-disk support: resolve coin data directory (respects CHAIN_MOUNT_POINT from coins.env)
_CHAIN_MOUNT_POINT=""
if [[ -f "$INSTALL_DIR/config/coins.env" ]]; then
    _CHAIN_MOUNT_POINT=$(grep -oP '^CHAIN_MOUNT_POINT="?\K[^"]+' "$INSTALL_DIR/config/coins.env" 2>/dev/null || echo "")
fi
_chain_dir() {
    local coin_lower="$1"
    if [[ -n "$_CHAIN_MOUNT_POINT" && -d "${_CHAIN_MOUNT_POINT}/${coin_lower}" ]]; then
        echo "${_CHAIN_MOUNT_POINT}/${coin_lower}"
    else
        echo "${INSTALL_DIR}/${coin_lower}"
    fi
}

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
DIM='\033[2m'
NC='\033[0m'

#===============================================================================
# HELPER FUNCTIONS
#===============================================================================

log_info() { echo -e "${CYAN}ℹ${NC} $1"; }
log_success() { echo -e "${GREEN}✓${NC} $1"; }
log_warn() { echo -e "${YELLOW}⚠${NC} $1"; }
log_error() { echo -e "${RED}✗${NC} $1"; }
prompt_input() { echo -ne "  ${GREEN}▸${NC} ${WHITE}${1}${NC}"; }

check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This command requires root privileges. Run with sudo."
        exit 1
    fi
}

# Re-exec a spiralpool-* script as POOL_USER if not already running as that user.
# Root can always sudo to spiraluser; other users will get a sudo password prompt.
# Usage: as_pool_user /usr/local/bin/spiralpool-foo [args...]
as_pool_user() {
    local target="$1"; shift
    if [[ "$(id -un)" != "$POOL_USER" ]]; then
        exec sudo -u "$POOL_USER" "$target" "$@"
    fi
    exec "$target" "$@"
}

get_enabled_coins() {
    local coins=""
    local config_file="$INSTALL_DIR/config/config.yaml"
    local env_file="$INSTALL_DIR/.env"

    # Method 1: Check config.yaml for enabled coins (most authoritative)
    # Parse YAML to find coins with enabled: true
    if [[ -f "$config_file" ]]; then
        # Use awk to parse coin entries - handles various YAML formats
        local yaml_coins=$(awk '
            /^[[:space:]]*-[[:space:]]*(symbol|id):/ {
                in_coin = 1
                enabled = 0
                symbol = ""
            }
            in_coin && /symbol:/ {
                gsub(/.*symbol:[[:space:]]*/, "")
                gsub(/["'"'"']/, "")
                gsub(/[[:space:]].*/, "")
                symbol = toupper($0)
            }
            in_coin && /^[[:space:]]+enabled:[[:space:]]*true/ {
                enabled = 1
            }
            in_coin && symbol != "" && enabled {
                print symbol
                symbol = ""
                enabled = 0
            }
            /^[a-z]/ && !/^[[:space:]]/ { in_coin = 0 }
        ' "$config_file" 2>/dev/null)

        if [[ -n "$yaml_coins" ]]; then
            coins="$yaml_coins"
        fi
    fi

    # Method 2: Check .env file for ENABLE_* flags (fallback)
    # Alphabetically ordered
    if [[ -z "$coins" ]] && [[ -f "$env_file" ]]; then
        source "$env_file" 2>/dev/null || true
        [[ "$ENABLE_BC2" == "true" ]] && coins="$coins BC2"
        [[ "$ENABLE_BCH" == "true" ]] && coins="$coins BCH"
        [[ "$ENABLE_BTC" == "true" ]] && coins="$coins BTC"
        [[ "$ENABLE_CAT" == "true" ]] && coins="$coins CAT"
        [[ "$ENABLE_DGB" == "true" ]] && coins="$coins DGB"
        [[ "$ENABLE_DGB_SCRYPT" == "true" ]] && coins="$coins DGB-SCRYPT"
        [[ "$ENABLE_DOGE" == "true" ]] && coins="$coins DOGE"
        [[ "$ENABLE_FBTC" == "true" ]] && coins="$coins FBTC"
        [[ "$ENABLE_LTC" == "true" ]] && coins="$coins LTC"
        [[ "$ENABLE_NMC" == "true" ]] && coins="$coins NMC"
        [[ "$ENABLE_PEP" == "true" ]] && coins="$coins PEP"
        [[ "$ENABLE_QBX" == "true" ]] && coins="$coins QBX"
        [[ "$ENABLE_SYS" == "true" ]] && coins="$coins SYS"
        [[ "$ENABLE_XMY" == "true" ]] && coins="$coins XMY"
    fi

    # Method 3: Check for enabled systemd services (fallback) - Alphabetically ordered
    if [[ -z "$coins" ]]; then
        systemctl is-enabled bitcoiniid &>/dev/null 2>&1 && coins="$coins BC2"
        systemctl is-enabled bitcoind-bch &>/dev/null 2>&1 && coins="$coins BCH"
        systemctl is-enabled bitcoind &>/dev/null 2>&1 && coins="$coins BTC"
        systemctl is-enabled catcoind &>/dev/null 2>&1 && coins="$coins CAT"
        systemctl is-enabled digibyted &>/dev/null 2>&1 && coins="$coins DGB"
        systemctl is-enabled dogecoind &>/dev/null 2>&1 && coins="$coins DOGE"
        systemctl is-enabled fractald &>/dev/null 2>&1 && coins="$coins FBTC"
        systemctl is-enabled litecoind &>/dev/null 2>&1 && coins="$coins LTC"
        systemctl is-enabled namecoind &>/dev/null 2>&1 && coins="$coins NMC"
        systemctl is-enabled pepecoind &>/dev/null 2>&1 && coins="$coins PEP"
        systemctl is-enabled qbitxd &>/dev/null 2>&1 && coins="$coins QBX"
        systemctl is-enabled syscoind &>/dev/null 2>&1 && coins="$coins SYS"
        systemctl is-enabled myriadcoind &>/dev/null 2>&1 && coins="$coins XMY"
    fi

    # Method 4: Check for running services (last resort) - Alphabetically ordered
    if [[ -z "$coins" ]]; then
        systemctl is-active --quiet bitcoiniid 2>/dev/null && coins="$coins BC2"
        systemctl is-active --quiet bitcoind-bch 2>/dev/null && coins="$coins BCH"
        systemctl is-active --quiet bitcoind 2>/dev/null && coins="$coins BTC"
        systemctl is-active --quiet catcoind 2>/dev/null && coins="$coins CAT"
        systemctl is-active --quiet digibyted 2>/dev/null && coins="$coins DGB"
        systemctl is-active --quiet dogecoind 2>/dev/null && coins="$coins DOGE"
        systemctl is-active --quiet fractald 2>/dev/null && coins="$coins FBTC"
        systemctl is-active --quiet litecoind 2>/dev/null && coins="$coins LTC"
        systemctl is-active --quiet namecoind 2>/dev/null && coins="$coins NMC"
        systemctl is-active --quiet pepecoind 2>/dev/null && coins="$coins PEP"
        systemctl is-active --quiet qbitxd 2>/dev/null && coins="$coins QBX"
        systemctl is-active --quiet syscoind 2>/dev/null && coins="$coins SYS"
        systemctl is-active --quiet myriadcoind 2>/dev/null && coins="$coins XMY"
    fi

    echo "$coins" | xargs
}

get_coin_daemon() {
    case "${1^^}" in
        DGB|DGB-SCRYPT) echo "digibyted" ;;
        BTC) echo "bitcoind" ;;
        BCH) echo "bitcoind-bch" ;;
        BC2) echo "bitcoiniid" ;;
        FBTC) echo "fractald" ;;
        LTC) echo "litecoind" ;;
        QBX) echo "qbitxd" ;;
        DOGE) echo "dogecoind" ;;
        NMC) echo "namecoind" ;;
        PEP|PEPECOIN|MEME) echo "pepecoind" ;;
        CAT|CATCOIN) echo "catcoind" ;;
        SYS) echo "syscoind" ;;
        XMY) echo "myriadcoind" ;;
        *) echo "" ;;
    esac
}

get_coin_cli() {
    case "${1^^}" in
        DGB|DGB-SCRYPT) echo "digibyte-cli -conf=$(_chain_dir dgb)/digibyte.conf" ;;
        BTC) echo "bitcoin-cli -conf=$(_chain_dir btc)/bitcoin.conf" ;;
        BCH) echo "bitcoin-cli-bch -conf=$(_chain_dir bch)/bitcoin.conf" ;;
        BC2) echo "bitcoinii-cli -conf=$(_chain_dir bc2)/bitcoinii.conf" ;;
        FBTC) echo "fractal-cli -conf=$(_chain_dir fbtc)/fractal.conf" ;;
        LTC) echo "litecoin-cli -conf=$(_chain_dir ltc)/litecoin.conf" ;;
        QBX) echo "qbitx-cli -conf=$(_chain_dir qbx)/qbitx.conf" ;;
        DOGE) echo "dogecoin-cli -conf=$(_chain_dir doge)/dogecoin.conf" ;;
        NMC) echo "namecoin-cli -conf=$(_chain_dir nmc)/namecoin.conf" ;;
        PEP|PEPECOIN|MEME) echo "pepecoin-cli -conf=$(_chain_dir pep)/pepecoin.conf" ;;
        CAT|CATCOIN) echo "catcoin-cli -conf=$(_chain_dir cat)/catcoin.conf" ;;
        SYS) echo "syscoin-cli -conf=$(_chain_dir sys)/syscoin.conf" ;;
        XMY) echo "myriadcoin-cli -conf=$(_chain_dir xmy)/myriadcoin.conf" ;;
        *) echo "" ;;
    esac
}

# Return the installed version of a coin daemon binary (e.g. "29.3.0").
# Uses the binary's --version flag — fast, no RPC, works even when daemon is stopped.
get_coin_daemon_version() {
    local coin="$1"
    local daemon_cmd
    daemon_cmd=$(get_coin_daemon "$coin")
    [[ -z "$daemon_cmd" ]] && echo "" && return
    local bin
    bin=$(readlink -f "/usr/local/bin/${daemon_cmd}" 2>/dev/null)
    [[ -z "$bin" || ! -x "$bin" ]] && echo "" && return
    "$bin" --version 2>&1 | grep -oP '\d+\.\d+[\.\d]*' | head -1
}

#===============================================================================
# STATUS COMMAND
#===============================================================================

# Print the active-for duration of a systemd service (e.g. "3d 2h 15m").
# Returns "" if the service is not active.
_service_uptime() {
    local svc="$1"
    local ts
    ts=$(systemctl show "$svc" --property=ActiveEnterTimestamp 2>/dev/null \
        | sed 's/ActiveEnterTimestamp=//')
    [[ -z "$ts" || "$ts" == "n/a" ]] && return
    local epoch_start
    epoch_start=$(date -d "$ts" +%s 2>/dev/null) || return
    [[ -z "$epoch_start" || "$epoch_start" -eq 0 ]] && return
    local now
    now=$(date +%s)
    local secs=$(( now - epoch_start ))
    [[ "$secs" -le 0 ]] && return
    local days=$(( secs / 86400 ))
    local hours=$(( (secs % 86400) / 3600 ))
    local mins=$(( (secs % 3600) / 60 ))
    if [[ "$days" -gt 0 ]]; then
        echo "${days}d ${hours}h ${mins}m"
    elif [[ "$hours" -gt 0 ]]; then
        echo "${hours}h ${mins}m"
    else
        echo "${mins}m"
    fi
}

# Print the next OnCalendar trigger time for a systemd timer.
# Returns "" if the timer is not found or not scheduled.
_timer_next_run() {
    local timer="$1"
    local usec
    usec=$(systemctl show "$timer" --property=NextElapseUSecRealtime 2>/dev/null \
        | sed 's/NextElapseUSecRealtime=//')
    [[ -z "$usec" || "$usec" == "0" || "$usec" == "n/a" ]] && return
    # Newer systemd returns a human-readable timestamp; older returns microseconds
    if [[ "$usec" =~ ^[0-9]+$ ]]; then
        date -d "@$(( usec / 1000000 ))" "+%Y-%m-%d %H:%M" 2>/dev/null
    else
        date -d "$usec" "+%Y-%m-%d %H:%M" 2>/dev/null
    fi
}

cmd_status() {
    echo ""
    echo -e "${CYAN}SPIRAL POOL STATUS${NC}"
    echo -e "────────────────────────────────────────────────────────────────"
    echo ""

    # Pool Service - use printf for consistent alignment
    echo -e "${WHITE}SERVICES${NC}"
    echo -e "────────────────────────────────────────"

    local _ut _sv
    [[ -n "$VERSION" ]] && printf "  %-24s %s\n" "Version" "v${VERSION}"
    echo ""
    if systemctl is-active --quiet spiralstratum 2>/dev/null; then
        _ut=$(_service_uptime spiralstratum)
        printf "  %-24s ${GREEN}%-12s${NC} %s\n" "Pool (spiralstratum)" "Running" "${_ut:+up $_ut}"
    else
        printf "  %-24s ${RED}%-12s${NC}\n" "Pool (spiralstratum)" "Stopped"
    fi

    if systemctl is-active --quiet patroni 2>/dev/null; then
        _ut=$(_service_uptime patroni)
        printf "  %-24s ${GREEN}%-12s${NC} %s\n" "Database (patroni)" "Running" "${_ut:+up $_ut}"
    elif systemctl is-active --quiet postgresql 2>/dev/null; then
        _ut=$(_service_uptime postgresql)
        printf "  %-24s ${GREEN}%-12s${NC} %s\n" "Database (postgresql)" "Running" "${_ut:+up $_ut}"
    else
        printf "  %-24s ${RED}%-12s${NC}\n" "Database (postgresql)" "Stopped"
    fi

    if systemctl is-active --quiet spiraldash 2>/dev/null; then
        _ut=$(_service_uptime spiraldash)
        printf "  %-24s ${GREEN}%-12s${NC} %s\n" "Dashboard" "Running" "${_ut:+up $_ut}"
    else
        printf "  %-24s ${YELLOW}%-12s${NC}\n" "Dashboard" "Not running"
    fi

    if systemctl is-active --quiet spiralsentinel 2>/dev/null; then
        _ut=$(_service_uptime spiralsentinel)
        _sv=$(curl -sf --max-time 1 http://127.0.0.1:9191/health 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('version',''))" 2>/dev/null || true)
        printf "  %-24s ${GREEN}%-12s${NC} %s\n" "Sentinel" "Running" "${_ut:+up $_ut}${_sv:+ · v$_sv}"
    else
        printf "  %-24s ${YELLOW}%-12s${NC}\n" "Sentinel" "Not running"
    fi

    # Miner Connection Info (shown early for quick reference)
    echo ""
    echo -e "${WHITE}MINER CONNECTION${NC}"
    echo -e "────────────────────────────────────────"

    # Determine which IP to show (HA VIP or server IP)
    local _mc_ha_enabled="false"
    local _mc_vip=""
    if systemctl is-active --quiet keepalived 2>/dev/null; then
        _mc_ha_enabled="true"
        _mc_vip=$(grep '^\s*address:' "$INSTALL_DIR/config/ha.yaml" 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+')
        if [[ -z "$_mc_vip" ]]; then
            _mc_vip=$(grep -A2 'virtual_ipaddress' /etc/keepalived/keepalived.conf 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1)
        fi
        if [[ -z "$_mc_vip" ]]; then
            _mc_vip=$(ip addr show 2>/dev/null | grep 'spiralpool-vip' | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1)
        fi
    fi
    local _mc_ip=""
    if [[ "$_mc_ha_enabled" == "true" ]] && [[ -n "$_mc_vip" ]]; then
        _mc_ip="$_mc_vip"
        echo -e "  ${YELLOW}Using VIP address for HA failover${NC}"
    else
        _mc_ip=$(ip route get 8.8.8.8 2>/dev/null | grep -oP 'src \K[\d.]+' | head -1)
        [[ -z "$_mc_ip" ]] && _mc_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
        [[ -z "$_mc_ip" ]] && _mc_ip="YOUR_SERVER_IP"
    fi

    local _mc_sha=""
    systemctl is-enabled --quiet bitcoiniid 2>/dev/null   && _mc_sha="${_mc_sha}  BC2:        ${GREEN}stratum+tcp://$_mc_ip:6333${NC}  (V2: 6334)\n"
    systemctl is-enabled --quiet bitcoind-bch 2>/dev/null && _mc_sha="${_mc_sha}  BCH:        ${GREEN}stratum+tcp://$_mc_ip:5333${NC}  (V2: 5334)\n"
    systemctl is-enabled --quiet bitcoind 2>/dev/null     && _mc_sha="${_mc_sha}  BTC:        ${GREEN}stratum+tcp://$_mc_ip:4333${NC}  (V2: 4334)\n"
    systemctl is-enabled --quiet digibyted 2>/dev/null    && _mc_sha="${_mc_sha}  DGB:        ${GREEN}stratum+tcp://$_mc_ip:3333${NC}  (V2: 3334)\n"
    systemctl is-enabled --quiet fractald 2>/dev/null     && _mc_sha="${_mc_sha}  FBTC:       ${GREEN}stratum+tcp://$_mc_ip:18335${NC} (V2: 18336)\n"
    systemctl is-enabled --quiet namecoind 2>/dev/null    && _mc_sha="${_mc_sha}  NMC:        ${GREEN}stratum+tcp://$_mc_ip:14335${NC} (V2: 14336)\n"
    systemctl is-enabled --quiet qbitxd 2>/dev/null       && _mc_sha="${_mc_sha}  QBX:        ${GREEN}stratum+tcp://$_mc_ip:20335${NC} (V2: 20336)\n"
    systemctl is-enabled --quiet syscoind 2>/dev/null     && _mc_sha="${_mc_sha}  SYS:        ${GREEN}stratum+tcp://$_mc_ip:15335${NC} (V2: 15336)\n"
    systemctl is-enabled --quiet myriadcoind 2>/dev/null  && _mc_sha="${_mc_sha}  XMY:        ${GREEN}stratum+tcp://$_mc_ip:17335${NC} (V2: 17336)\n"
    [[ -n "$_mc_sha" ]] && echo -e "  ${DIM}SHA-256d:${NC}" && echo -e "$_mc_sha"

    local _mc_scrypt=""
    systemctl is-enabled --quiet catcoind 2>/dev/null     && _mc_scrypt="${_mc_scrypt}  CAT:        ${GREEN}stratum+tcp://$_mc_ip:12335${NC} (V2: 12336)\n"
    systemctl is-enabled --quiet digibyted 2>/dev/null    && _mc_scrypt="${_mc_scrypt}  DGB-SCRYPT: ${GREEN}stratum+tcp://$_mc_ip:3336${NC}  (V2: 3337)\n"
    systemctl is-enabled --quiet dogecoind 2>/dev/null    && _mc_scrypt="${_mc_scrypt}  DOGE:       ${GREEN}stratum+tcp://$_mc_ip:8335${NC}  (V2: 8337)\n"
    systemctl is-enabled --quiet litecoind 2>/dev/null    && _mc_scrypt="${_mc_scrypt}  LTC:        ${GREEN}stratum+tcp://$_mc_ip:7333${NC}  (V2: 7334)\n"
    systemctl is-enabled --quiet pepecoind 2>/dev/null    && _mc_scrypt="${_mc_scrypt}  PEP:        ${GREEN}stratum+tcp://$_mc_ip:10335${NC} (V2: 10336)\n"
    [[ -n "$_mc_scrypt" ]] && echo -e "  ${DIM}Scrypt:${NC}" && echo -e "$_mc_scrypt"

    if [[ -z "$_mc_sha" && -z "$_mc_scrypt" ]]; then
        echo -e "  ${DIM}No coin daemons enabled yet${NC}"
    fi
    [[ "$_mc_ha_enabled" == "true" && -n "$_mc_vip" ]] && \
        echo -e "  ${YELLOW}Note: Use VIP address ($_mc_vip) for miners for HA failover${NC}"

    echo ""
    echo -e "${WHITE}BLOCKCHAIN NODES${NC}"
    echo -e "────────────────────────────────────────"

    # SHA-256d coins
    # Alphabetically ordered
    local sha256_shown=false
    for coin in BC2 BCH BTC DGB FBTC NMC QBX SYS XMY; do
        daemon=$(get_coin_daemon $coin)
        if systemctl is-enabled --quiet "$daemon" 2>/dev/null; then
            if [[ "$sha256_shown" == "false" ]]; then
                echo -e "  ${DIM}SHA-256d:${NC}"
                sha256_shown=true
            fi
            local ver label
            ver=$(get_coin_daemon_version "$coin")
            label="${coin}${ver:+ v${ver}}"
            if systemctl is-active --quiet "$daemon" 2>/dev/null; then
                cli=$(get_coin_cli $coin)
                if info=$($cli getblockchaininfo 2>/dev/null); then
                    blocks=$(echo "$info" | grep -o '"blocks":[^,]*' | cut -d: -f2 | tr -d ' ')
                    headers=$(echo "$info" | grep -o '"headers":[^,]*' | cut -d: -f2 | tr -d ' ')
                    if [[ "${headers:-0}" -gt 0 ]]; then
                        pct=$(( blocks * 100 / headers ))
                    else
                        pct=0
                    fi
                    if [[ "$blocks" == "$headers" ]] && [[ "$pct" -ge 99 ]]; then
                        printf "    %-26s ${GREEN}%-12s${NC} (block %s)\n" "$label" "Synced" "$blocks"
                    else
                        printf "    %-26s ${YELLOW}%-12s${NC} (%s/%s - %s%%)\n" "$label" "Syncing" "$blocks" "$headers" "${pct:-0}"
                    fi
                else
                    printf "    %-26s ${YELLOW}%-12s${NC}\n" "$label" "Starting..."
                fi
            else
                printf "    %-26s ${RED}%-12s${NC}\n" "$label" "Stopped"
            fi
        fi
    done

    # Scrypt coins
    # Alphabetically ordered
    local scrypt_shown=false
    for coin in CAT DOGE LTC PEP; do
        daemon=$(get_coin_daemon $coin)
        if systemctl is-enabled --quiet "$daemon" 2>/dev/null; then
            if [[ "$scrypt_shown" == "false" ]]; then
                echo -e "  ${DIM}Scrypt:${NC}"
                scrypt_shown=true
            fi
            local ver label
            ver=$(get_coin_daemon_version "$coin")
            label="${coin}${ver:+ v${ver}}"
            if systemctl is-active --quiet "$daemon" 2>/dev/null; then
                cli=$(get_coin_cli $coin)
                if info=$($cli getblockchaininfo 2>/dev/null); then
                    blocks=$(echo "$info" | grep -o '"blocks":[^,]*' | cut -d: -f2 | tr -d ' ')
                    headers=$(echo "$info" | grep -o '"headers":[^,]*' | cut -d: -f2 | tr -d ' ')
                    if [[ "${headers:-0}" -gt 0 ]]; then
                        pct=$(( blocks * 100 / headers ))
                    else
                        pct=0
                    fi
                    if [[ "$blocks" == "$headers" ]] && [[ "$pct" -ge 99 ]]; then
                        printf "    %-26s ${GREEN}%-12s${NC} (block %s)\n" "$label" "Synced" "$blocks"
                    else
                        printf "    %-26s ${YELLOW}%-12s${NC} (%s/%s - %s%%)\n" "$label" "Syncing" "$blocks" "$headers" "${pct:-0}"
                    fi
                else
                    printf "    %-26s ${YELLOW}%-12s${NC}\n" "$label" "Starting..."
                fi
            else
                printf "    %-26s ${RED}%-12s${NC}\n" "$label" "Stopped"
            fi
        fi
    done

    # DGB-SCRYPT uses the same daemon as DGB but different stratum port
    # Show it in Scrypt section if DGB daemon is enabled
    if systemctl is-enabled --quiet digibyted 2>/dev/null; then
        if [[ "$scrypt_shown" == "false" ]]; then
            echo -e "  ${DIM}Scrypt:${NC}"
        fi
        # DGB-SCRYPT shares sync status with DGB
        printf "    %-22s ${DIM}%-12s${NC}\n" "DGB-SCRYPT" "(same as DGB)"
    fi

    # HA Status (if enabled)
    local ha_enabled="false"
    local vip_address=""
    if [[ -f "$INSTALL_DIR/config/ha.yaml" ]] || systemctl is-active --quiet keepalived 2>/dev/null; then
        echo ""
        echo -e "${WHITE}HIGH AVAILABILITY${NC}"
        echo -e "────────────────────────────────────────"
        if systemctl is-active --quiet keepalived 2>/dev/null; then
            ha_enabled="true"
            printf "  %-24s ${GREEN}%-12s${NC}\n" "Keepalived" "Running"

            # Get VIP from ha.yaml (keepalived.conf is chmod 600 root-only)
            vip_address=$(grep '^\s*address:' "$INSTALL_DIR/config/ha.yaml" 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+')
            # Fallback: try keepalived.conf (works if running as root/sudo)
            if [[ -z "$vip_address" ]]; then
                vip_address=$(grep -A2 'virtual_ipaddress' /etc/keepalived/keepalived.conf 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1)
            fi
            # Fallback: extract from ip addr (only works on MASTER node)
            if [[ -z "$vip_address" ]]; then
                vip_address=$(ip addr show 2>/dev/null | grep 'spiralpool-vip' | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1)
            fi

            # Check VIP status - look for the actual VIP IP on any interface
            local is_master="false"
            if [[ -n "$vip_address" ]]; then
                # Check if this node has the VIP bound
                if ip addr show 2>/dev/null | grep -q " ${vip_address}/"; then
                    is_master="true"
                fi
            else
                # Fallback: check for spiralpool-vip label
                if ip addr show 2>/dev/null | grep -q "spiralpool-vip"; then
                    is_master="true"
                fi
            fi

            if [[ "$is_master" == "true" ]]; then
                printf "  %-24s ${GREEN}%-12s${NC}\n" "VIP Status" "MASTER"
            else
                printf "  %-24s ${YELLOW}%-12s${NC}\n" "VIP Status" "BACKUP"
            fi

            if [[ -n "$vip_address" ]]; then
                printf "  %-24s %s\n" "VIP Address" "$vip_address"
            else
                printf "  %-24s ${DIM}%-12s${NC}\n" "VIP Address" "(not found in config)"
            fi
        else
            printf "  %-24s ${DIM}%-12s${NC}\n" "HA Cluster" "Not configured"
        fi
    fi

    # ── ALERT PAUSE STATUS ────────────────────────────────────────────────────────
    local _sentinel_home _pause_file _pause_until _pause_reason _now _mins_left
    _sentinel_home=$(getent passwd spiraluser 2>/dev/null | cut -d: -f6)
    _sentinel_home="${_sentinel_home:-/home/spiraluser}"
    if [[ -d "${_sentinel_home}/.spiralsentinel" ]]; then
        _pause_file="${_sentinel_home}/.spiralsentinel/maintenance_pause"
    else
        _pause_file="/spiralpool/config/sentinel/maintenance_pause"
    fi
    if [[ -f "$_pause_file" ]]; then
        _pause_until=$(python3 -c "import json,sys; d=json.load(open('$_pause_file')); print(d['pause_until'])" 2>/dev/null)
        _pause_reason=$(python3 -c "import json,sys; d=json.load(open('$_pause_file')); print(d.get('reason',''))" 2>/dev/null)
        _now=$(date +%s)
        if [[ -n "$_pause_until" ]] && (( _now < ${_pause_until%.*} )); then
            _mins_left=$(python3 -c "import math; print(math.ceil((${_pause_until%.*} - $_now) / 60))" 2>/dev/null)
            echo ""
            echo -e "${WHITE}ALERT STATUS${NC}"
            echo -e "────────────────────────────────────────"
            printf "  %-24s ${YELLOW}%s${NC}\n" "Sentinel Alerts" "PAUSED — ${_mins_left}m remaining"
            [[ -n "$_pause_reason" ]] && printf "  %-24s %s\n" "Reason" "$_pause_reason"
            echo -e "  ${DIM}Run 'spiralpool-pause resume' to cancel${NC}"
        fi
    fi

    # ── SCHEDULED TASKS ──────────────────────────────────────────────────────────
    local _shown_sched=0
    local _next=""

    # Backup cron (spiralpool-backup)
    if [[ -f /etc/cron.d/spiralpool-backup ]]; then
        [[ "$_shown_sched" -eq 0 ]] && echo "" && echo -e "${WHITE}SCHEDULED TASKS${NC}" && echo -e "────────────────────────────────────────"
        _shown_sched=1
        # Extract the cron schedule expression from the cron.d file
        local _cron_expr
        # Skip comments, blank lines, and env var lines (KEY=value) to find the actual cron entry
        _cron_expr=$(grep -v '^#' /etc/cron.d/spiralpool-backup 2>/dev/null | grep -v '^[[:space:]]*$' | grep -v '^[A-Za-z_]*=' | head -1 | awk '{print $1,$2,$3,$4,$5}')
        if [[ -n "$_cron_expr" ]]; then
            printf "  %-28s %s\n" "Backup (cron)" "schedule: ${_cron_expr}"
        else
            printf "  %-28s %s\n" "Backup (cron)" "enabled"
        fi
    fi

    # PostgreSQL maintenance timer
    if systemctl list-timers --all 2>/dev/null | grep -q 'spiralpool-pg-maintenance'; then
        [[ "$_shown_sched" -eq 0 ]] && echo "" && echo -e "${WHITE}SCHEDULED TASKS${NC}" && echo -e "────────────────────────────────────────"
        _shown_sched=1
        _next=$(_timer_next_run spiralpool-pg-maintenance.timer)
        if [[ -n "$_next" ]]; then
            printf "  %-28s next: %s\n" "PG Maintenance (timer)" "$_next"
        else
            printf "  %-28s %s\n" "PG Maintenance (timer)" "scheduled"
        fi
    fi

    echo ""

}

#===============================================================================
# TOR COMMAND
#===============================================================================

cmd_tor() {
    local action="${1:-status}"

    case "$action" in
        status)
            echo ""
            echo -e "${WHITE}TOR STATUS${NC}"
            echo -e "─────────────────────────────────────────────────────────────────────────"
            if systemctl is-active --quiet tor 2>/dev/null; then
                echo -e "  Tor Service             ${GREEN}● Running${NC}"
                if grep -q "^proxy=" "$(_chain_dir dgb)/digibyte.conf" 2>/dev/null; then
                    echo -e "  DigiByte via Tor        ${GREEN}● Enabled${NC}"
                else
                    echo -e "  DigiByte via Tor        ${DIM}○ Disabled${NC}"
                fi
            else
                echo -e "  Tor Service             ${DIM}○ Not installed${NC}"
            fi
            echo ""
            ;;
        enable)
            check_root
            log_info "Enabling Tor for blockchain connections..."
            log_warn "This feature requires re-running the installer with Tor option."
            log_info "Run: sudo ./install.sh --tor"
            ;;
        disable)
            check_root
            log_info "Disabling Tor..."
            log_warn "This feature requires re-running the installer without Tor option."
            ;;
        *)
            echo "Usage: spiralctl tor [status|enable|disable]"
            exit 1
            ;;
    esac
}

#===============================================================================
# VIP COMMAND (Virtual IP for failover)
#===============================================================================

cmd_vip() {
    local action="${1:-status}"

    case "$action" in
        status)
            echo ""
            echo -e "${WHITE}VIP STATUS${NC}"
            echo -e "─────────────────────────────────────────────────────────────────────────"
            if systemctl is-active --quiet keepalived 2>/dev/null; then
                echo -e "  Keepalived              ${GREEN}● Running${NC}"

                # Check if we hold the VIP
                # BUG FIX (M12): Old regex expected IP on same line as virtual_ipaddress {.
                # Generated config puts IP on the next line. Use grep -A2 to capture it.
                local vip=$(grep -A2 'virtual_ipaddress' /etc/keepalived/keepalived.conf 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
                if [[ -n "$vip" ]]; then
                    if ip addr show 2>/dev/null | grep -q " ${vip}/" 2>/dev/null; then
                        echo -e "  VIP ($vip)       ${GREEN}● MASTER (we own it)${NC}"
                    else
                        echo -e "  VIP ($vip)       ${YELLOW}○ BACKUP (owned by peer)${NC}"
                    fi
                fi

                # Show peer status if available
                if command -v vrrpadm &>/dev/null; then
                    echo ""
                    echo -e "  ${DIM}Cluster peers:${NC}"
                    vrrpadm -d 2>/dev/null || true
                fi
            else
                echo -e "  VIP/Keepalived          ${DIM}○ Not configured${NC}"
                echo ""
                echo -e "  ${DIM}To enable VIP failover:${NC}"
                echo -e "  ${CYAN}sudo spiralctl vip enable --address 192.168.1.100 --interface eth0${NC}"
            fi
            echo ""
            ;;
        enable)
            check_root
            shift  # Remove 'enable' from args
            vip_enable "$@"
            ;;
        disable)
            check_root
            vip_disable
            ;;
        failover)
            check_root
            log_warn "Manual VIP failover is rarely needed."
            echo ""
            echo "Keepalived uses nopreempt mode. VIP stays on the current master"
            echo "until it fails — a returning node does NOT automatically reclaim VIP."
            echo "This prevents VIP/DB primary split."
            echo ""
            echo -e "${WHITE}To trigger a database + VIP switchover:${NC}"
            echo -e "  ${CYAN}sudo spiralctl ha promote${NC}  (run on the desired primary)"
            echo ""
            echo -e "${WHITE}To release VIP from THIS node (force failover to backup):${NC}"
            echo -e "  ${CYAN}sudo systemctl stop keepalived${NC}  (VIP moves to backup)"
            echo -e "  ${CYAN}sudo systemctl start keepalived${NC} (restarts as BACKUP)"
            echo ""
            echo -e "${WHITE}To check current VIP ownership:${NC}"
            echo -e "  ${CYAN}spiralctl vip status${NC}"
            echo ""
            ;;
        *)
            echo "Usage: spiralctl vip [status|enable|disable|failover]"
            echo ""
            echo "Commands:"
            echo "  status                    Show VIP cluster status"
            echo "  enable [options]          Enable VIP on this node"
            echo "  disable                   Disable VIP on this node"
            echo "  failover                  Display VIP failover instructions"
            echo ""
            echo "Enable Options:"
            echo "  --address <ip>            Virtual IP address (required)"
            echo "  --interface <name>        Network interface (auto-detected if omitted)"
            echo "  --priority <num>          Priority (100=primary, 101+=backup)"
            echo "  --token <token>           Cluster token (generated if omitted)"
            echo "  --netmask <cidr>          CIDR netmask (default: 32)"
            echo ""
            echo "Examples:"
            echo "  # Primary node (first to enable):"
            echo "  sudo spiralctl vip enable --address 192.168.1.100"
            echo ""
            echo "  # Backup node (use token from primary):"
            echo "  sudo spiralctl vip enable --address 192.168.1.100 --token <token> --priority 101"
            exit 1
            ;;
    esac
}

# Generate a random cluster token
generate_cluster_token() {
    # 32 character hex token with spiral- prefix
    local hex
    hex=$(openssl rand -hex 16 2>/dev/null) || hex=""
    if [[ -z "$hex" ]] || [[ ${#hex} -ne 32 ]]; then
        # Fallback to /dev/urandom directly
        hex=$(LC_ALL=C tr -dc 'a-f0-9' < /dev/urandom 2>/dev/null | head -c 32) || hex=""
    fi
    if [[ -z "$hex" ]] || [[ ${#hex} -lt 16 ]]; then
        return 1
    fi
    echo "spiral-${hex}"
}

# Detect primary network interface
detect_interface() {
    # Get the interface used for default route
    ip route get 8.8.8.8 2>/dev/null | grep -oP 'dev \K\S+' | head -1
}

# Enable VIP with Keepalived
vip_enable() {
    local vip_address=""
    local interface=""
    local priority=100
    local token=""
    local netmask="32"

    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --address)
                [[ -z "${2:-}" ]] && { log_error "--address requires a value"; exit 1; }
                vip_address="$2"
                shift 2
                ;;
            --interface)
                [[ -z "${2:-}" ]] && { log_error "--interface requires a value"; exit 1; }
                interface="$2"
                shift 2
                ;;
            --priority)
                [[ -z "${2:-}" ]] && { log_error "--priority requires a value"; exit 1; }
                priority="$2"
                if ! [[ "$priority" =~ ^[0-9]+$ ]] || [[ "$priority" -lt 1 ]] || [[ "$priority" -gt 255 ]]; then
                    log_error "--priority must be a number between 1 and 255"
                    exit 1
                fi
                shift 2
                ;;
            --token)
                [[ -z "${2:-}" ]] && { log_error "--token requires a value"; exit 1; }
                token="$2"
                if [[ ! "$token" =~ ^[A-Za-z0-9-]+$ ]]; then
                    log_error "--token must contain only alphanumeric characters and hyphens"
                    exit 1
                fi
                shift 2
                ;;
            --netmask)
                [[ -z "${2:-}" ]] && { log_error "--netmask requires a value"; exit 1; }
                netmask="$2"
                if ! [[ "$netmask" =~ ^[0-9]+$ ]] || [[ "$netmask" -lt 1 ]] || [[ "$netmask" -gt 32 ]]; then
                    log_error "--netmask must be a CIDR prefix length between 1 and 32"
                    exit 1
                fi
                shift 2
                ;;
            *)
                log_error "Unknown option: $1"
                exit 1
                ;;
        esac
    done

    # Validate VIP address
    if [[ -z "$vip_address" ]]; then
        log_error "--address is required"
        echo "Example: sudo spiralctl vip enable --address 192.168.1.100"
        exit 1
    fi

    # Validate IP format and octet ranges
    if ! [[ "$vip_address" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
        log_error "Invalid IP address format: $vip_address"
        exit 1
    fi
    IFS='.' read -r _vip1 _vip2 _vip3 _vip4 <<< "$vip_address"
    if [[ "$_vip1" -gt 255 || "$_vip2" -gt 255 || "$_vip3" -gt 255 || "$_vip4" -gt 255 ]]; then
        log_error "Invalid IP address (octet out of range 0-255): $vip_address"
        exit 1
    fi

    # Auto-detect interface if not provided
    if [[ -z "$interface" ]]; then
        interface=$(detect_interface)
        if [[ -z "$interface" ]]; then
            log_error "Could not auto-detect network interface. Use --interface"
            exit 1
        fi
        log_info "Auto-detected interface: $interface"
    fi

    # Validate interface exists
    if ! ip link show "$interface" &>/dev/null; then
        log_error "Interface '$interface' does not exist"
        exit 1
    fi

    # Generate token if not provided (primary node)
    if [[ -z "$token" ]]; then
        token=$(generate_cluster_token)
        # SECURITY (F-09): Fail-fast if token generation failed (empty = unauthenticated cluster)
        if [[ -z "$token" ]]; then
            log_error "FATAL: Cluster token generation failed (is openssl or /dev/urandom available?)"
            exit 1
        fi
        echo ""
        echo -e "${YELLOW}╔═══════════════════════════════════════════════════════════════╗${NC}"
        echo -e "${YELLOW}║                    CLUSTER TOKEN GENERATED                    ║${NC}"
        echo -e "${YELLOW}╚═══════════════════════════════════════════════════════════════╝${NC}"
        echo ""
        echo -e "  Token: ${GREEN}$token${NC}"
        echo ""
        echo -e "  ${RED}SAVE THIS TOKEN!${NC} You'll need it to add backup nodes."
        echo ""
    fi

    # Determine keepalived priority based on role
    # IMPORTANT: Keepalived uses HIGHER number = MORE likely to be MASTER.
    # Spiral Pool uses LOWER number = HIGHER priority (primary=100, backup=101+).
    # We must convert: keepalived_priority = 200 - spiral_priority
    # Primary (spiral 100) → keepalived 100, Backup (spiral 101) → keepalived 99
    #
    # ALL nodes use state BACKUP + nopreempt. This prevents the primary from
    # automatically reclaiming VIP on return (which causes VIP/DB split).
    local state="BACKUP"
    local keepalived_priority=$((200 - priority))
    # Ensure keepalived priority stays in valid range (1-254)
    [[ $keepalived_priority -lt 1 ]] && keepalived_priority=1
    [[ $keepalived_priority -gt 254 ]] && keepalived_priority=254
    if [[ "$priority" -le 100 ]]; then
        keepalived_priority=100
    fi

    # Install keepalived if not present
    if ! command -v keepalived &>/dev/null; then
        log_info "Installing keepalived..."
        apt-get update -qq > /dev/null 2>&1
        apt-get install -y keepalived > /dev/null 2>&1 || {
            log_error "Failed to install keepalived"
            return 1
        }
        if ! command -v keepalived &>/dev/null; then
            log_error "keepalived not found after install attempt"
            return 1
        fi
    fi

    # Get local IP for unicast peer
    local local_ip=$(ip -4 addr show "$interface" | grep -oP 'inet \K[\d.]+' | head -1)

    # Extract auth password from token (use first 8 chars after 'spiral-' prefix)
    # Sanitize to alphanumeric only — prevents keepalived config injection
    local auth_pass
    auth_pass=$(echo "${token:7:8}" | tr -dc 'A-Za-z0-9')
    if [[ -z "$auth_pass" ]]; then
        auth_pass=$(tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 8)
        log_warn "Token too short — generated random keepalived auth password"
    fi

    # Sanitize hostname for keepalived router_id (alphanumeric + dash only)
    local hostname_short
    hostname_short=$(hostname -s 2>/dev/null | tr -dc 'A-Za-z0-9_-' || echo "spiralpool")
    [[ -z "$hostname_short" ]] && hostname_short="spiralpool"

    # Derive virtual_router_id from cluster token (1-255 range).
    # Prevents VRRP ID conflicts when multiple Spiral Pool clusters share a L2 segment.
    local vrid=51
    if [[ -n "$token" ]]; then
        local _sum=0 _i
        for (( _i=0; _i<${#token}; _i++ )); do
            _sum=$(( _sum + $(printf '%d' "'${token:_i:1}") ))
        done
        vrid=$(( (_sum % 254) + 1 ))
    fi

    # Create keepalived config
    log_info "Configuring keepalived..."

    # Verify pgrep exists before baking its path into keepalived config
    if ! command -v pgrep &>/dev/null; then
        apt-get install -y -qq procps > /dev/null 2>&1 || true
        if ! command -v pgrep &>/dev/null; then
            log_error "pgrep not found (install procps package)"
            return 1
        fi
    fi

    mkdir -p /etc/keepalived

    cat > /etc/keepalived/keepalived.conf << EOF
# Spiral Pool VIP Configuration
# Generated by spiralctl vip enable
# Token: [configured]

global_defs {
    router_id spiralpool_${hostname_short}
    script_user root
    enable_script_security
}

# Health check script - verify stratum server is running
# fall 5 = 10s of failures before reducing priority (prevents VIP flapping
# on transient crashes — systemd Restart=always recovers stratum in ~5s)
# rise 2 = 4s of success before restoring priority
vrrp_script check_stratum {
    script "$(command -v pgrep) -x spiralstratum"
    interval 2
    weight 2
    fall 5
    rise 2
}

vrrp_instance SPIRALPOOL_VIP {
    state $state
    interface $interface
    virtual_router_id $vrid
    priority $keepalived_priority
    advert_int 1
    nopreempt

    authentication {
        auth_type PASS
        auth_pass $auth_pass
    }

    virtual_ipaddress {
        $vip_address/$netmask dev $interface label spiralpool-vip
    }

    track_script {
        check_stratum
    }

    # Notify scripts for logging
    notify_master "/bin/logger -t keepalived 'SPIRALPOOL: Became MASTER for VIP $vip_address'"
    notify_backup "/bin/logger -t keepalived 'SPIRALPOOL: Became BACKUP for VIP $vip_address'"
    notify_fault  "/bin/logger -t keepalived 'SPIRALPOOL: FAULT state for VIP $vip_address'"
}
EOF

    # Restrict keepalived.conf permissions (contains auth_pass derived from cluster token)
    chmod 600 /etc/keepalived/keepalived.conf

    # Save token for reference
    echo "$token" > /etc/keepalived/.cluster_token
    chmod 600 /etc/keepalived/.cluster_token

    # Enable and start keepalived
    systemctl enable keepalived >/dev/null 2>&1
    systemctl restart keepalived

    # Wait a moment for VIP assignment
    sleep 2

    # Flush routing cache — keepalived VIP changes can leave stale broadcast entries
    ip route flush cache 2>/dev/null || true

    echo ""
    log_success "VIP enabled successfully!"
    echo ""
    echo -e "${WHITE}Configuration:${NC}"
    echo "  VIP Address:  $vip_address"
    echo "  Interface:    $interface"
    echo "  Priority:     $priority (BACKUP/nopreempt)"
    echo "  Local IP:     $local_ip"
    echo ""

    # Check if we got the VIP
    if ip addr show "$interface" | grep -q " ${vip_address}/"; then
        echo -e "  VIP Status:   ${GREEN}● MASTER (VIP is on this node)${NC}"
    else
        echo -e "  VIP Status:   ${YELLOW}○ BACKUP (waiting for master)${NC}"
    fi
    echo ""

    if [[ "$priority" -le 100 ]]; then
        echo -e "${WHITE}Next Steps:${NC}"
        echo "  On backup node(s), run:"
        echo -e "  ${CYAN}sudo spiralctl vip enable --address $vip_address --token $token --priority 101${NC}"
        echo ""
    fi

    echo -e "${WHITE}Miners should connect to VIP:${NC}"
    echo -e "  ${GREEN}$vip_address${NC} (use 'spiralctl status' to see stratum ports)"
    echo ""
}

# Disable VIP
vip_disable() {
    log_info "Disabling VIP..."

    if systemctl is-active --quiet keepalived 2>/dev/null; then
        systemctl stop keepalived
        systemctl disable keepalived >/dev/null 2>&1
        log_success "Keepalived stopped and disabled"
    fi

    # Remove VIP from interface if still present
    # Extract full IP/CIDR from keepalived config (e.g. "192.168.1.100/24")
    local vip_cidr=$(grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+(?=\s+dev\s)' /etc/keepalived/keepalived.conf 2>/dev/null | head -1)
    local iface=$(grep -oP 'interface\s+\K\S+' /etc/keepalived/keepalived.conf 2>/dev/null | head -1)

    if [[ -n "$vip_cidr" ]] && [[ -n "$iface" ]]; then
        ip addr del "$vip_cidr" dev "$iface" 2>/dev/null || true
        log_info "VIP $vip_cidr removed from $iface"
    fi

    # Flush routing cache — removing VIP can leave stale broadcast entries
    ip route flush cache 2>/dev/null || true

    log_success "VIP disabled"
}

#===============================================================================
# HA COMMAND (High Availability)
#===============================================================================

cmd_ha() {
    local action="${1:-status}"

    case "$action" in
        status)
            echo ""
            echo -e "${WHITE}HIGH AVAILABILITY STATUS${NC}"
            echo -e "─────────────────────────────────────────────────────────────────────────"

            # Check Patroni (preferred HA manager for PostgreSQL)
            if systemctl is-active --quiet patroni 2>/dev/null; then
                echo -e "  Patroni                 ${GREEN}● Running${NC}"
                local patroni_json
                patroni_json=$(curl -s "${PATRONI_CURL_AUTH[@]}" --connect-timeout 3 --max-time 5 http://localhost:8008/patroni 2>/dev/null)
                if [[ -n "$patroni_json" ]]; then
                    local p_role p_state p_timeline p_lag
                    p_role=$(echo "$patroni_json" | grep -oP '"role"\s*:\s*"\K[^"]+' 2>/dev/null || true)
                    p_state=$(echo "$patroni_json" | grep -oP '"state"\s*:\s*"\K[^"]+' 2>/dev/null || true)
                    p_timeline=$(echo "$patroni_json" | grep -oP '"timeline"\s*:\s*\K[0-9]+' 2>/dev/null || true)

                    if [[ "$p_role" == "master" || "$p_role" == "primary" ]]; then
                        echo -e "  Role                    ${GREEN}● Primary${NC} (timeline $p_timeline)"
                        # Show connected replicas
                        local replicas
                        replicas=$(sudo -u postgres psql -tAc "SELECT client_addr || ' (' || state || ')' FROM pg_stat_replication;" 2>/dev/null || true)
                        if [[ -n "$replicas" ]]; then
                            echo -e "  Replicas                ${GREEN}● Connected${NC}"
                            while IFS= read -r line; do
                                echo -e "    ${DIM}└─ $line${NC}"
                            done <<< "$replicas"
                        else
                            echo -e "  Replicas                ${YELLOW}○ None connected${NC}"
                        fi
                    elif [[ "$p_role" == "replica" ]]; then
                        echo -e "  Role                    ${YELLOW}● Replica${NC} (state: $p_state)"
                        p_lag=$(echo "$patroni_json" | grep -oP '"replay_lag"\s*:\s*\K[0-9]+' 2>/dev/null || true)
                        if [[ -n "$p_lag" ]] && [[ "$p_lag" -gt 0 ]]; then
                            echo -e "  Replication Lag         ${YELLOW}${p_lag} bytes${NC}"
                        else
                            echo -e "  Replication Lag         ${GREEN}● In sync${NC}"
                        fi
                    else
                        echo -e "  Role                    ${DIM}$p_role ($p_state)${NC}"
                    fi
                else
                    echo -e "  Patroni API             ${RED}● Not responding${NC}"
                fi
            elif systemctl is-active --quiet postgresql 2>/dev/null; then
                # Fallback: check raw PostgreSQL replication (no Patroni)
                echo -e "  Patroni                 ${DIM}○ Not installed${NC}"
                echo -e "  PostgreSQL              ${GREEN}● Running${NC}"

                if sudo -u postgres psql -c "SELECT * FROM pg_stat_replication;" 2>/dev/null | grep -q "streaming"; then
                    echo -e "  Replication             ${GREEN}● Streaming (Primary)${NC}"
                    echo ""
                    echo -e "  ${DIM}Connected replicas:${NC}"
                    sudo -u postgres psql -t -c "SELECT client_addr, state FROM pg_stat_replication;" 2>/dev/null || true
                elif sudo -u postgres psql -c "SELECT * FROM pg_stat_wal_receiver;" 2>/dev/null | grep -q "streaming"; then
                    echo -e "  Replication             ${GREEN}● Streaming (Standby)${NC}"
                else
                    echo -e "  Replication             ${DIM}○ Not configured${NC}"
                fi
            else
                echo -e "  PostgreSQL              ${RED}○ Stopped${NC}"
            fi

            # Check etcd (Patroni consensus store)
            echo ""
            if systemctl is-active --quiet etcd 2>/dev/null; then
                echo -e "  etcd                    ${GREEN}● Running${NC}"
                local etcd_health
                etcd_health=$(ETCDCTL_API=3 etcdctl endpoint health --command-timeout=5s 2>&1 || true)
                if echo "$etcd_health" | grep -q "is healthy"; then
                    echo -e "  Consensus               ${GREEN}● Healthy${NC}"
                else
                    echo -e "  Consensus               ${RED}● Unhealthy${NC}"
                fi
            else
                echo -e "  etcd                    ${DIM}○ Not running${NC}"
            fi

            # Check Keepalived/VIP
            echo ""
            if systemctl is-active --quiet keepalived 2>/dev/null; then
                echo -e "  Keepalived              ${GREEN}● Running${NC}"
                # BUG FIX (M12): Same fix as cmd_vip status — use grep -A2 for multi-line match.
                local vip=$(grep -A2 'virtual_ipaddress' /etc/keepalived/keepalived.conf 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
                if [[ -n "$vip" ]]; then
                    if ip addr show 2>/dev/null | grep -q " ${vip}/" 2>/dev/null; then
                        echo -e "  VIP ($vip)       ${GREEN}● MASTER${NC}"
                    else
                        echo -e "  VIP ($vip)       ${YELLOW}○ BACKUP${NC}"
                    fi
                fi
            else
                echo -e "  Keepalived              ${DIM}○ Not configured${NC}"
                echo ""
                echo -e "  ${DIM}To enable HA, run:${NC}"
                echo -e "  ${CYAN}sudo spiralctl ha enable --vip 192.168.1.100${NC}"
            fi

            # Check HA watcher
            echo ""
            if systemctl is-active --quiet spiralpool-ha-watcher 2>/dev/null; then
                echo -e "  HA Watcher              ${GREEN}● Running${NC}"
            elif systemctl is-enabled --quiet spiralpool-ha-watcher 2>/dev/null; then
                echo -e "  HA Watcher              ${RED}○ Enabled but stopped${NC}"
            fi

            # Check stratum VIP manager
            local vip_status
            vip_status=$(curl -s --connect-timeout 2 --max-time 3 http://localhost:5354/status 2>/dev/null)
            if [[ -n "$vip_status" ]]; then
                local vip_role
                vip_role=$(echo "$vip_status" | grep -oP '"localRole"\s*:\s*"\K[^"]+' 2>/dev/null)
                echo -e "  Stratum VIP Manager     ${GREEN}● $vip_role${NC}"
            else
                echo -e "  Stratum VIP Manager     ${RED}● Not responding${NC}"
            fi

            echo ""
            ;;
        enable)
            check_root
            shift  # Remove 'enable' from args
            ha_enable "$@"
            ;;
        disable)
            check_root
            shift  # Remove 'disable' from args
            ha_disable "$@"
            ;;
        credentials|creds|info)
            check_root
            ha_show_credentials
            ;;
        setup)
            check_root
            echo ""
            log_info "HA Setup Guide"
            echo ""
            echo "High Availability requires:"
            echo "  1. Two or more pool servers (same subnet)"
            echo "  2. Available Virtual IP address on the subnet"
            echo "  3. spiraluser account on both nodes (created during install)"
            echo ""
            echo -e "${WHITE}Step 1: Enable HA on the PRIMARY node${NC}"
            echo "  sudo spiralctl ha enable --vip 192.168.1.100"
            echo ""
            echo "  This outputs credentials and a copy-paste command for backup nodes."
            echo ""
            echo -e "${WHITE}Step 2: Enable HA on each BACKUP node${NC}"
            echo "  Copy the command from the primary's output. It includes all credentials."
            echo "  For additional backups, increment --priority (101, 102, 103...)."
            echo ""
            echo -e "${WHITE}If you lost the primary's output, retrieve credentials:${NC}"
            echo "  Superuser password:   sudo awk '/superuser:/{f=1} f&&/password:/{gsub(/.*password:[[:space:]]*/,\"\"); gsub(/[\"]/,\"\"); print; exit}' /etc/patroni/patroni.yml"
            echo "  Replication password: sudo grep SPIRAL_REPLICATION_PASSWORD /etc/spiralpool/ha.env"
            echo "  Cluster token:        sudo cat /etc/keepalived/.cluster_token"
            echo "  spiraluser password:  (set during install — check your records)"
            echo ""
            echo -e "${WHITE}To revert any node to standalone:${NC}"
            echo "  sudo spiralctl ha disable"
            echo ""
            ;;
        failback)
            check_root
            echo ""
            log_info "HA Failback Guide"
            echo ""
            echo "Failback restores the original primary node as master after a failover."
            echo ""
            echo -e "${WHITE}Step 1: Verify the returning node is healthy${NC}"
            echo -e "  ${CYAN}sudo spiralctl ha status${NC}"
            echo "  Patroni should show this node as 'replica' or 'sync_standby'."
            echo "  If etcd is dead, rejoin it first (see HA docs)."
            echo ""
            echo -e "${WHITE}Step 2: Patroni switchover (move DB primary)${NC}"
            echo -e "  ${CYAN}sudo spiralctl ha promote${NC}  (run on the node you want as primary)"
            echo "  This moves PostgreSQL primary to this node via Patroni."
            echo ""
            echo -e "${WHITE}Step 3: Move VIP (nopreempt — manual step required)${NC}"
            echo "  Keepalived uses nopreempt mode. VIP stays on the current master"
            echo "  until it fails. To move VIP after a Patroni switchover:"
            echo -e "  On the ${WHITE}old master${NC} (the node giving up VIP):"
            echo -e "    ${CYAN}sudo systemctl stop keepalived${NC}   # releases VIP"
            echo -e "    ${CYAN}sudo systemctl start keepalived${NC}  # rejoins as BACKUP"
            echo ""
            echo -e "${WHITE}If Patroni can't rejoin automatically (extended outage):${NC}"
            echo -e "  ${CYAN}sudo spiralpool-ha-replicate --source <primary-ip>${NC}"
            echo "  This performs a full cold-standby replication of blockchain data"
            echo "  and PostgreSQL from the current primary to this node."
            echo ""
            echo -e "${WHITE}Verify cluster health:${NC}"
            echo -e "  ${CYAN}sudo spiralctl ha validate${NC}"
            echo ""
            ;;
        promote)
            check_root
            log_info "Promoting this node to primary..."

            # Prefer Patroni switchover (HA-safe) over raw pg_ctl promote (splits brain)
            if systemctl is-active --quiet patroni 2>/dev/null; then
                local patroni_status
                patroni_status=$(curl -s "${PATRONI_CURL_AUTH[@]}" --connect-timeout 3 --max-time 5 http://localhost:8008/patroni 2>/dev/null)
                local current_role=$(echo "$patroni_status" | grep -oP '"role"\s*:\s*"\K[^"]+' 2>/dev/null)

                if [[ "$current_role" == "replica" || "$current_role" == "standby_leader" ]]; then
                    log_info "Requesting Patroni switchover (current role: $current_role)..."
                    # Patroni handles the promotion safely: updates etcd, fences old primary,
                    # promotes this replica, and updates replication topology
                    curl -s "${PATRONI_CURL_AUTH[@]}" --connect-timeout 5 --max-time 30 \
                        -X POST http://localhost:8008/switchover \
                        -H "Content-Type: application/json" \
                        -d "{}" 2>/dev/null && \
                        log_success "Patroni switchover initiated — check 'spiralctl ha status'" || \
                        log_error "Patroni switchover failed. Check: journalctl -u patroni"
                    echo ""
                    echo -e "${YELLOW}IMPORTANT: VIP is still on the old master (nopreempt mode).${NC}"
                    echo -e "To move VIP to this node, run on the ${WHITE}old master${NC}:"
                    echo -e "  ${CYAN}sudo systemctl stop keepalived${NC}   # releases VIP"
                    echo -e "  ${CYAN}sudo systemctl start keepalived${NC}  # rejoins as BACKUP"
                    echo ""
                elif [[ "$current_role" == "master" || "$current_role" == "primary" ]]; then
                    log_warn "This node is already the primary."
                else
                    log_error "Unexpected Patroni role: $current_role"
                fi
            elif command -v pg_ctl &>/dev/null; then
                log_warn "Patroni not running — falling back to raw pg_ctl promote"
                log_warn "WARNING: This bypasses HA consensus and may cause split-brain!"
                # Find the newest PG data directory (handles multiple versions safely)
                local pg_data_dir
                pg_data_dir=$(ls -d /var/lib/postgresql/*/main 2>/dev/null | sort -V | tail -1)
                if [[ -z "$pg_data_dir" ]]; then
                    log_error "No PostgreSQL data directory found under /var/lib/postgresql/"
                elif ! sudo -u postgres pg_ctl promote -D "$pg_data_dir"; then
                    log_error "Failed to promote PostgreSQL ($pg_data_dir). Is this a standby?"
                fi
            else
                log_error "Neither Patroni nor pg_ctl found."
            fi
            ;;
        validate)
            check_root
            shift
            exec /usr/local/bin/spiralpool-ha-validate "$@"
            ;;
        service)
            check_root
            shift
            exec /usr/local/bin/spiralpool-ha-service "$@"
            ;;
        vip)
            shift
            cmd_vip "$@"
            ;;
        *)
            echo "Usage: spiralctl ha [status|enable|disable|credentials|setup|failback|promote|validate|service|vip]"
            echo ""
            echo "Commands:"
            echo "  status                    Show HA cluster status"
            echo "  enable [options]          Enable full HA stack (etcd + Patroni + keepalived)"
            echo "  disable [--yes]           Disable HA and revert to standalone (full teardown)"
            echo "  credentials               Show credentials + backup node command"
            echo "  setup                     Show HA setup instructions"
            echo "  failback                  Trigger failback to primary"
            echo "  promote                   Promote this standby to primary"
            echo "  validate                  Validate HA configuration"
            echo "  service                   Manage HA service lifecycle"
            echo "  vip [status|enable|disable|failover]"
            echo "                            Virtual IP for miner failover"
            echo ""
            echo "Enable Options:"
            echo "  --vip <ip>                Virtual IP address (required)"
            echo "  --interface <name>        Network interface (auto-detected)"
            echo "  --priority <num>          Priority (100=primary, 101+=backup)"
            echo "  --token <token>           Cluster token (generated if omitted on primary)"
            echo "  --netmask <cidr>          CIDR netmask (default: 32)"
            echo "  --primary-ip <ip>         Primary node's real IP (required for backup)"
            echo "  --repl-password <pw>      Replication password (required for backup)"
            echo "  --superuser-password <pw> PostgreSQL superuser password (required for backup)"
            echo "  --db-password <pw>        App DB password (optional, read from config.yaml)"
            echo "  --ssh-password <pw>       spiraluser system password on primary (for SSH key exchange)"
            echo "  --force                   Re-run on partially-configured state"
            echo ""
            echo "Environment variable alternatives (avoids passwords in shell history / ps aux):"
            echo "  SPIRAL_REPL_PASSWORD        Alternative to --repl-password"
            echo "  SPIRAL_SUPERUSER_PASSWORD   Alternative to --superuser-password"
            echo "  SPIRAL_DB_PASSWORD          Alternative to --db-password"
            echo "  SPIRAL_SSH_PASSWORD         Alternative to --ssh-password"
            echo "  CLI flags take precedence over env vars when both are provided."
            echo ""
            echo "What 'ha enable' does:"
            echo "  1. Installs and configures etcd (distributed consensus)"
            echo "  2. Installs and configures Patroni (PostgreSQL HA failover)"
            echo "  3. Migrates standalone PostgreSQL to Patroni management"
            echo "  4. Configures keepalived VIP for transparent miner failover"
            echo "  5. Updates config.yaml with HA settings"
            echo "  6. Deploys HA scripts and role watcher service"
            echo ""
            echo "Disable Options:"
            echo "  --yes, -y                 Skip confirmation prompt"
            echo ""
            echo "What 'ha disable' does:"
            echo "  1. Stops watcher, keepalived, Patroni, etcd"
            echo "  2. Reverts PostgreSQL to standalone mode"
            echo "  3. Removes HA config from config.yaml"
            echo "  4. Removes HA markers (data preserved for manual cleanup)"
            echo ""
            echo "Examples:"
            echo "  # Primary node (generates credentials + backup command):"
            echo "  sudo spiralctl ha enable --vip 192.168.1.100"
            echo ""
            echo "  # Backup node (use command from primary's output):"
            echo "  sudo spiralctl ha enable --vip 192.168.1.100 --token <token> --priority 101 \\"
            echo "    --primary-ip 192.168.1.104 --superuser-password <pw> --repl-password <pw> \\"
            echo "    --ssh-password <spiraluser-system-password>"
            echo ""
            echo "  # Third node (higher priority number = lower priority):"
            echo "  sudo spiralctl ha enable --vip 192.168.1.100 --token <token> --priority 102 \\"
            echo "    --primary-ip 192.168.1.104 --superuser-password <pw> --repl-password <pw> \\"
            echo "    --ssh-password <spiraluser-system-password>"
            echo ""
            echo "  # Revert to standalone (interactive confirmation):"
            echo "  sudo spiralctl ha disable"
            echo ""
            echo "  # Revert to standalone (skip confirmation):"
            echo "  sudo spiralctl ha disable --yes"
            exit 1
            ;;
    esac
}

#===============================================================================
# HA HELPER FUNCTIONS
#===============================================================================

# Detect current HA state of this node
# Sets HA_STATE to "standalone" | "ha-full" | "ha-partial"
detect_ha_state() {
    local components=0
    local total=5

    # Check etcd
    if [[ -f /etc/default/etcd ]] && systemctl is-enabled --quiet etcd 2>/dev/null; then
        components=$((components + 1))
    fi

    # Check Patroni
    if [[ -f /etc/patroni/patroni.yml ]] && systemctl is-enabled --quiet patroni 2>/dev/null; then
        components=$((components + 1))
    fi

    # Check keepalived
    if [[ -f /etc/keepalived/keepalived.conf ]] && systemctl is-enabled --quiet keepalived 2>/dev/null; then
        components=$((components + 1))
    fi

    # Check ha-enabled marker
    if [[ -f "$INSTALL_DIR/config/ha-enabled" ]]; then
        components=$((components + 1))
    fi

    # Check watcher service
    if systemctl is-enabled --quiet spiralpool-ha-watcher 2>/dev/null; then
        components=$((components + 1))
    fi

    if [[ $components -eq 0 ]]; then
        HA_STATE="standalone"
    elif [[ $components -eq $total ]]; then
        HA_STATE="ha-full"
    else
        HA_STATE="ha-partial"
    fi
}

# Detect PostgreSQL version from installed data directory
detect_pg_version() {
    local pg_dir
    pg_dir=$(ls -d /var/lib/postgresql/*/main 2>/dev/null | sort -V | tail -1)
    if [[ -n "$pg_dir" ]]; then
        # Extract version number from path: /var/lib/postgresql/16/main → 16
        PG_VERSION=$(echo "$pg_dir" | grep -oP '/var/lib/postgresql/\K[0-9]+')
    else
        # Fallback: check for backup dirs from previous --force run (main.backup.*)
        pg_dir=$(ls -d /var/lib/postgresql/*/main.backup.* 2>/dev/null | sort -V | tail -1)
        if [[ -n "$pg_dir" ]]; then
            PG_VERSION=$(echo "$pg_dir" | grep -oP '/var/lib/postgresql/\K[0-9]+')
        else
            # Fallback: check PG binary installation
            pg_dir=$(ls -d /usr/lib/postgresql/*/bin/postgres 2>/dev/null | sort -V | tail -1)
            if [[ -n "$pg_dir" ]]; then
                PG_VERSION=$(echo "$pg_dir" | grep -oP '/usr/lib/postgresql/\K[0-9]+')
            else
                PG_VERSION=""
            fi
        fi
    fi
}

# Detect this server's primary IP address
detect_server_ip() {
    SERVER_IP=$(ip route get 8.8.8.8 2>/dev/null | grep -oP 'src \K\S+' | head -1)
}

# Read database credentials from config.yaml
# Sets DB_USER, DB_PASSWORD, DB_NAME
read_db_credentials() {
    DB_USER=""
    DB_PASSWORD=""
    DB_NAME=""

    if [[ ! -f "$CONFIG_FILE" ]]; then
        log_warn "config.yaml not found — using default DB credentials"
        DB_USER="spiralstratum"
        DB_NAME="spiralpool"
        return 0
    fi

    DB_USER=$(awk '/^database:/{found=1} found && /user:/{gsub(/.*user:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null || echo "")
    DB_PASSWORD=$(awk '/^database:/{found=1} found && /password:/{gsub(/.*password:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null || echo "")
    DB_NAME=$(awk '/^database:/{found=1} found && /name:/{gsub(/.*name:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null || echo "")

    # Defaults
    [[ -z "$DB_USER" ]] && DB_USER="spiralstratum"
    [[ -z "$DB_NAME" ]] && DB_NAME="spiralpool"
}

# Generate a random password (alphanumeric, 32 chars)
ha_generate_password() {
    LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32
}

#===============================================================================
# HA INFRASTRUCTURE FUNCTIONS
#===============================================================================

# Install etcd packages (modeled on install.sh:15848)
ha_install_etcd() {
    if command -v etcd &>/dev/null; then
        log_info "etcd already installed"
        return 0
    fi

    log_info "Installing etcd..."
    add-apt-repository -y universe > /dev/null 2>&1 || true
    apt-get update -qq > /dev/null 2>&1
    apt-get install -y -qq etcd-server etcd-client || {
        log_error "Failed to install etcd"
        return 1
    }

    # etcd may auto-start after apt install with default config, creating stale data
    # in /var/lib/etcd/member that conflicts with our custom config.
    # Stop it and wipe data so ha_configure_etcd starts from a clean state.
    if systemctl is-active --quiet etcd 2>/dev/null; then
        log_info "Stopping auto-started etcd (will reconfigure)..."
        systemctl stop etcd 2>/dev/null || true
    fi
    rm -rf /var/lib/etcd/member 2>/dev/null || true

    log_success "etcd installed"
}

# Configure etcd for this node (modeled on install.sh:15868)
# Usage: ha_configure_etcd <node_ip> <ha_mode> [primary_ip]
ha_configure_etcd() {
    local node_ip="$1"
    local ha_mode="$2"
    local primary_ip="${3:-}"

    if [[ -z "$node_ip" ]]; then
        log_error "Node IP is empty — cannot configure etcd"
        return 1
    fi

    local etcd_name=""
    local cluster_peers=""
    local etcd_hosts=""
    local cluster_state=""

    if [[ "$ha_mode" == "ha-master" ]]; then
        # PRIMARY: Bootstrap as single-node cluster
        etcd_name="etcd-$(hostname -s)"
        cluster_peers="${etcd_name}=http://${node_ip}:2380"
        etcd_hosts="${node_ip}:2379"

        # Use "existing" if etcd member data already exists and etcd is healthy
        # (--force re-run on a working cluster). Using "new" with existing data
        # causes etcd to see conflicting cluster state and refuse to start.
        if [[ -d "/var/lib/etcd/member" ]] && \
           ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 | grep -q "is healthy"; then
            cluster_state="existing"
            log_info "etcd primary (${etcd_name}): reconfiguring existing cluster"
        else
            cluster_state="new"
            log_info "etcd primary (${etcd_name}): single-node bootstrap"
        fi

    elif [[ "$ha_mode" == "ha-backup" ]]; then
        # BACKUP: Join existing cluster as learner
        etcd_name="etcd-$(hostname -s)"
        cluster_state="existing"
        etcd_hosts="${node_ip}:2379,${primary_ip}:2379"

        log_info "etcd backup (${etcd_name}): joining cluster on ${primary_ip}..."

        # Check if this node is already a member (idempotent on re-run)
        # Anchor etcd_name match: comma-delimited fields in etcdctl output prevent
        # substring matches (e.g., "etcd-ha" incorrectly matching "etcd-ha-1")
        local existing_member
        existing_member=$(ETCDCTL_API=3 etcdctl --command-timeout=10s --endpoints="http://${primary_ip}:2379" \
            member list --write-out=simple 2>/dev/null | grep -E "(, ${etcd_name},|http://${node_ip}:2380)" || echo "")

        if [[ -n "$existing_member" ]]; then
            log_info "etcd member ${etcd_name} already exists, skipping add"
        else
            # Add as learner (preserves primary quorum)
            local add_output add_ok=false add_attempts=5
            for ((attempt=1; attempt<=add_attempts; attempt++)); do
                add_output=$(ETCDCTL_API=3 etcdctl --command-timeout=10s --endpoints="http://${primary_ip}:2379" \
                    member add "${etcd_name}" --learner --peer-urls="http://${node_ip}:2380" 2>&1) && {
                    add_ok=true
                    break
                }
                if [[ $attempt -lt $add_attempts ]]; then
                    log_warn "etcd member add attempt $attempt/$add_attempts failed, retrying in ${attempt}s..."
                    sleep "$attempt"
                fi
            done
            if [[ "$add_ok" != "true" ]]; then
                log_error "Failed to add this node to primary's etcd cluster after $add_attempts attempts"
                log_error "Output: $add_output"
                log_error "Ensure primary is running and etcd is healthy"
                return 1
            fi
            log_info "etcd member add succeeded"
        fi

        # Build cluster_peers from primary's member list
        local member_list=""
        local list_retries=3
        while [[ $list_retries -gt 0 ]] && [[ -z "$member_list" ]]; do
            member_list=$(ETCDCTL_API=3 etcdctl --command-timeout=10s --endpoints="http://${primary_ip}:2379" \
                member list --write-out=simple 2>/dev/null || echo "")
            if [[ -z "$member_list" ]]; then
                list_retries=$((list_retries - 1))
                [[ $list_retries -gt 0 ]] && sleep 2
            fi
        done
        if [[ -n "$member_list" ]]; then
            cluster_peers=""
            while IFS= read -r line; do
                local m_name m_peer_url
                m_name=$(echo "$line" | awk -F', ' '{print $3}')
                m_peer_url=$(echo "$line" | awk -F', ' '{print $4}' | tr -d '[]')
                if [[ -z "$m_name" ]] && [[ "$m_peer_url" == "http://${node_ip}:2380" ]]; then
                    m_name="$etcd_name"
                fi
                if [[ -n "$m_name" ]] && [[ -n "$m_peer_url" ]]; then
                    [[ -n "$cluster_peers" ]] && cluster_peers+=","
                    cluster_peers+="${m_name}=${m_peer_url}"
                fi
            done <<< "$member_list"
            log_info "etcd cluster peers: $cluster_peers"
        fi

        if [[ -z "$cluster_peers" ]]; then
            log_error "Could not query etcd member list from primary"
            return 1
        fi
    else
        log_error "Unknown ha_mode: $ha_mode"
        return 1
    fi

    # Store for Patroni configuration
    ETCD_HOSTS_CONFIG="$etcd_hosts"

    # Clear stale etcd data if name changed or unhealthy
    if [[ -d "/var/lib/etcd/member" ]]; then
        local current_etcd_name=""
        if [[ -f "/etc/default/etcd" ]]; then
            current_etcd_name=$(grep '^ETCD_NAME=' /etc/default/etcd 2>/dev/null | cut -d'"' -f2)
        fi

        if [[ -n "$current_etcd_name" ]] && [[ "$current_etcd_name" != "$etcd_name" ]]; then
            log_info "etcd name changed (${current_etcd_name} → ${etcd_name}) — clearing data..."
            systemctl stop etcd 2>/dev/null || true
            rm -rf /var/lib/etcd/member
        elif ! ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 | grep -q "is healthy"; then
            log_info "Clearing stale etcd data (not healthy)..."
            systemctl stop etcd 2>/dev/null || true
            rm -rf /var/lib/etcd/member
        fi
    fi

    # TOCTOU guard: if data was cleared above but cluster_state was set to "existing"
    # based on an earlier health check, etcd will refuse to start (no member data for
    # "existing" state). Re-evaluate cluster_state after data clearing.
    if [[ "$ha_mode" == "ha-master" ]] && [[ "$cluster_state" == "existing" ]] && [[ ! -d "/var/lib/etcd/member" ]]; then
        cluster_state="new"
        log_info "etcd data was cleared — forcing cluster_state=new"
    fi

    # Write etcd configuration
    tee /etc/default/etcd > /dev/null << EOF || { log_error "Failed to write /etc/default/etcd"; return 1; }
# Spiral Pool etcd configuration
# Generated by spiralctl ha enable on $(date)

ETCD_NAME="$etcd_name"
ETCD_DATA_DIR="/var/lib/etcd"
ETCD_LISTEN_CLIENT_URLS="http://${node_ip}:2379,http://127.0.0.1:2379"
ETCD_ADVERTISE_CLIENT_URLS="http://${node_ip}:2379"
ETCD_LISTEN_PEER_URLS="http://${node_ip}:2380"
ETCD_INITIAL_ADVERTISE_PEER_URLS="http://${node_ip}:2380"
ETCD_INITIAL_CLUSTER="$cluster_peers"
ETCD_INITIAL_CLUSTER_STATE="$cluster_state"
ETCD_INITIAL_CLUSTER_TOKEN="spiralpool-etcd-cluster"
EOF

    # Enable and start etcd
    systemctl enable etcd >/dev/null 2>&1 || true
    systemctl restart etcd || { log_error "Failed to start etcd"; return 1; }

    # BACKUP: Wait for learner "started", then promote to voter
    if [[ "$ha_mode" == "ha-backup" ]] && [[ -n "$primary_ip" ]]; then
        local learner_started=false
        for ((lw=1; lw<=30; lw++)); do
            if ETCDCTL_API=3 etcdctl --command-timeout=5s --endpoints="http://${primary_ip}:2379" \
                member list --write-out=simple 2>/dev/null | grep "http://${node_ip}:2380" | \
                awk -F', ' '{print $2}' | tr -d ' ' | grep -qx "started"; then
                learner_started=true
                break
            fi
            [[ $lw -eq 1 ]] && log_info "Waiting for etcd learner to connect to leader..."
            sleep 1
        done

        if [[ "$learner_started" != "true" ]]; then
            log_warn "etcd learner did not reach 'started' state in 30s — attempting promotion anyway"
        fi

        # Promote learner to voter
        local promote_member_line promote_member_id promote_is_learner
        promote_member_line=$(ETCDCTL_API=3 etcdctl --command-timeout=5s --endpoints="http://${primary_ip}:2379" \
            member list --write-out=simple 2>/dev/null | grep "http://${node_ip}:2380" | head -1 || echo "")
        promote_member_id=$(echo "$promote_member_line" | awk -F', ' '{print $1}' | tr -d '[:space:]')
        promote_is_learner=$(echo "$promote_member_line" | awk -F', ' '{print $6}' | tr -d ' ')

        if [[ "${promote_is_learner:-}" == "true" ]] && [[ -n "${promote_member_id:-}" ]]; then
            local promote_ok=false
            for ((pt=1; pt<=6; pt++)); do
                if ETCDCTL_API=3 etcdctl --command-timeout=10s --endpoints="http://${primary_ip}:2379" \
                    member promote "$promote_member_id" &>/dev/null; then
                    log_success "etcd learner promoted to voting member"
                    promote_ok=true
                    break
                fi
                log_info "Learner not ready for promotion (attempt $pt/6) — waiting..."
                sleep 5
            done
            if [[ "$promote_ok" != "true" ]]; then
                log_error "etcd learner promotion failed after 30s — this node is a non-voting member"
                log_error "HA failover will NOT work until this node is promoted to voter"
                log_error "Try manually: ETCDCTL_API=3 etcdctl --endpoints=http://${primary_ip}:2379 member promote <id>"
                return 1
            fi
        fi
    fi

    # Wait for etcd to be healthy
    local retries=30
    while [[ $retries -gt 0 ]]; do
        if ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 | grep -q "is healthy"; then
            log_success "etcd is healthy"

            # BACKUP: Final promotion check (in case pre-health promotion didn't complete)
            if [[ "$ha_mode" == "ha-backup" ]] && [[ -n "$primary_ip" ]]; then
                local my_member_line my_member_id my_is_learner
                my_member_line=$(ETCDCTL_API=3 etcdctl --command-timeout=5s --endpoints="http://${primary_ip}:2379" \
                    member list --write-out=simple 2>/dev/null | grep "http://${node_ip}:2380" | head -1 || echo "")
                my_member_id=$(echo "$my_member_line" | awk -F', ' '{print $1}' | tr -d '[:space:]')
                my_is_learner=$(echo "$my_member_line" | awk -F', ' '{print $6}' | tr -d ' ')

                if [[ "${my_is_learner:-}" == "true" ]] && [[ -n "${my_member_id:-}" ]]; then
                    log_info "Promoting etcd learner to voting member..."
                    if ETCDCTL_API=3 etcdctl --command-timeout=10s --endpoints="http://${primary_ip}:2379" \
                        member promote "$my_member_id" > /dev/null 2>&1; then
                        log_success "etcd promoted — this node is now a voting member"
                    else
                        log_error "etcd learner promotion failed — this node cannot participate in quorum"
                        return 1
                    fi
                fi
            fi

            # BACKUP: Final learner status verification using LOCAL endpoint.
            # If the primary was unreachable during promotion, the promotion block above
            # is silently skipped (empty member_line → is_learner != "true"). Use local
            # etcd to definitively confirm we are NOT a learner before declaring success.
            if [[ "$ha_mode" == "ha-backup" ]]; then
                local final_learner_status
                final_learner_status=$(ETCDCTL_API=3 etcdctl --command-timeout=5s \
                    member list --write-out=simple 2>/dev/null | grep -F "http://${node_ip}:2380" | \
                    awk -F', ' '{print $6}' | tr -d ' ' || echo "unknown")
                if [[ "$final_learner_status" == "true" ]]; then
                    log_error "This node is still an etcd learner (non-voting). HA failover will NOT work."
                    log_error "Promote manually: ETCDCTL_API=3 etcdctl --endpoints=http://${primary_ip}:2379 member promote <id>"
                    return 1
                fi
            fi

            return 0
        fi
        sleep 1
        retries=$((retries - 1))
    done

    log_error "etcd failed to become healthy within 30 seconds"
    log_error "Check: journalctl -u etcd -n 50"
    return 1
}

# Install Patroni (modeled on install.sh:16330)
ha_install_patroni() {
    log_info "Installing Patroni..."

    # python3-venv is required for 'python3 -m venv' but is not installed by default
    # on Ubuntu. install.sh installs it in setup_system(), but spiralctl ha enable
    # runs independently — must ensure it's available.
    if ! python3 -c "import venv" &>/dev/null; then
        log_info "Installing python3-venv..."
        apt-get update -qq > /dev/null 2>&1
        apt-get install -y -qq python3-venv || {
            log_error "Failed to install python3-venv (required for Patroni)"
            return 1
        }
    fi

    local patroni_venv="/opt/patroni/venv"
    mkdir -p /opt/patroni
    python3 -m venv "$patroni_venv" || {
        log_error "Failed to create Python venv at $patroni_venv"
        return 1
    }
    "$patroni_venv/bin/pip" install --upgrade pip -q 2>/dev/null
    "$patroni_venv/bin/pip" install --quiet patroni[etcd] python-etcd psycopg2-binary || {
        log_error "Failed to install Patroni packages"
        return 1
    }

    # Symlinks
    ln -sf "$patroni_venv/bin/patroni" /usr/local/bin/patroni
    ln -sf "$patroni_venv/bin/patronictl" /usr/local/bin/patronictl

    # Create directories
    mkdir -p /etc/patroni /var/log/patroni
    chown -R postgres:postgres /etc/patroni /var/log/patroni

    log_success "Patroni installed"
}

# Configure Patroni (modeled on install.sh:16354)
# Usage: ha_configure_patroni <node_ip> <pg_version> <superuser_pw> <repl_user> <repl_pw> <db_user> <db_password> <db_name> <peer_ip>
ha_configure_patroni() {
    local node_ip="$1"
    local pg_version="$2"
    local superuser_pw="$3"
    local repl_user="$4"
    local repl_pw="$5"
    local db_user="$6"
    local db_password="$7"
    local db_name="$8"
    local peer_ip="${9:-}"

    local patroni_name="patroni-$(hostname -s)"
    local pg_data_dir="/var/lib/postgresql/$pg_version/main"
    local pg_bin_dir="/usr/lib/postgresql/$pg_version/bin"
    local pg_conf_dir="/etc/postgresql/$pg_version/main"

    if [[ ! -d "$pg_bin_dir" ]]; then
        log_error "PostgreSQL version ${pg_version} not found at ${pg_bin_dir}"
        return 1
    fi

    # Escape passwords for YAML double-quoted strings
    # Must escape backslashes FIRST (before adding new ones), then double-quotes
    local escaped_superuser_pw escaped_repl_pw
    escaped_superuser_pw=$(printf '%s' "$superuser_pw" | sed 's/\\/\\\\/g; s/"/\\"/g')
    escaped_repl_pw=$(printf '%s' "$repl_pw" | sed 's/\\/\\\\/g; s/"/\\"/g')

    # etcd hosts
    local etcd_hosts="${ETCD_HOSTS_CONFIG:-${node_ip}:2379}"
    if [[ -n "$peer_ip" ]] && [[ ! "$etcd_hosts" =~ "${peer_ip}:2379" ]]; then
        etcd_hosts+=",${peer_ip}:2379"
    fi

    # Calculate shared_buffers
    local total_ram_mb shared_buffers_mb
    total_ram_mb=$(free -m | awk '/^Mem:/ {print $2}')
    shared_buffers_mb=$((total_ram_mb / 4))
    [[ $shared_buffers_mb -lt 256 ]] && shared_buffers_mb=256
    [[ $shared_buffers_mb -gt 4096 ]] && shared_buffers_mb=4096

    # Build pg_hba entries
    # Use /24 subnet for the node's network — allows any future backup on the same
    # subnet to connect for replication without reconfiguring the primary's Patroni.
    # This is critical: primary doesn't know backup IPs at setup time.
    local node_subnet
    node_subnet=$(echo "$node_ip" | sed 's/\.[0-9]*$/.0/')

    local pg_hba_entries=""
    pg_hba_entries+="    - local all all peer"$'\n'
    pg_hba_entries+="    - host replication ${repl_user} ${node_subnet}/24 scram-sha-256"$'\n'
    pg_hba_entries+="    - host all all ${node_subnet}/24 scram-sha-256"$'\n'
    if [[ -n "$peer_ip" ]]; then
        # Also add specific peer entry (in case peer is on a different subnet)
        local peer_subnet
        peer_subnet=$(echo "$peer_ip" | sed 's/\.[0-9]*$/.0/')
        if [[ "$peer_subnet" != "$node_subnet" ]]; then
            pg_hba_entries+="    - host replication ${repl_user} ${peer_subnet}/24 scram-sha-256"$'\n'
            pg_hba_entries+="    - host all all ${peer_subnet}/24 scram-sha-256"$'\n'
        fi
    fi
    pg_hba_entries+="    - host replication ${repl_user} 127.0.0.1/32 scram-sha-256"$'\n'
    pg_hba_entries+="    - host all all 127.0.0.1/32 scram-sha-256"

    local pg_hba_runtime_entries="${pg_hba_entries}"

    # Derive Patroni REST API credentials from cluster token.
    # Reuse patroni-api.conf if it already exists (e.g. generated by install.sh).
    local patroni_api_user="spiralpool"
    local patroni_api_pass=""
    if [[ -f /spiralpool/config/patroni-api.conf ]]; then
        # shellcheck source=/dev/null
        source /spiralpool/config/patroni-api.conf
        patroni_api_pass="${PATRONI_API_PASSWORD:-}"
    fi
    if [[ -z "${patroni_api_pass}" ]]; then
        # Derive from cluster token (same as install.sh); fall back to random
        local cluster_tok=""
        [[ -f /etc/keepalived/.cluster_token ]] && cluster_tok=$(cat /etc/keepalived/.cluster_token 2>/dev/null)
        if [[ -n "$cluster_tok" ]]; then
            patroni_api_pass=$(echo -n "${cluster_tok}" | sha256sum | cut -c1-32)
        else
            patroni_api_pass=$(openssl rand -hex 16)
        fi
        mkdir -p /spiralpool/config
        tee /spiralpool/config/patroni-api.conf > /dev/null << PATRONIEOF
# Spiral Pool -- Patroni REST API credentials
# Generated by spiralctl ha enable on $(date)
PATRONI_API_USERNAME="${patroni_api_user}"
PATRONI_API_PASSWORD="${patroni_api_pass}"
PATRONIEOF
        local pool_grp="${POOL_USER:-spiraluser}"
        chown root:"${pool_grp}" /spiralpool/config/patroni-api.conf 2>/dev/null || chown root:root /spiralpool/config/patroni-api.conf
        chmod 640 /spiralpool/config/patroni-api.conf
        log_success "Patroni REST API credentials written to /spiralpool/config/patroni-api.conf"
    fi

    # Write Patroni configuration
    tee /etc/patroni/patroni.yml > /dev/null << EOF || { log_error "Failed to write patroni.yml"; return 1; }
# Spiral Pool - Patroni Configuration
# Generated by spiralctl ha enable on $(date)

scope: spiralpool-postgres
namespace: /spiralpool/
name: ${patroni_name}

restapi:
  listen: 0.0.0.0:8008
  connect_address: ${node_ip}:8008
  authentication:
    username: ${patroni_api_user}
    password: ${patroni_api_pass}

etcd3:
  hosts: ${etcd_hosts}

bootstrap:
  dcs:
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    maximum_lag_on_failover: 1048576
    failsafe_mode: true
    synchronous_mode: true
    synchronous_mode_strict: false
    postgresql:
      use_pg_rewind: true
      use_slots: true
      remove_data_directory_on_diverged_timelines: true
      parameters:
        max_connections: 200
        wal_level: replica
        hot_standby: 'on'
        max_wal_senders: 10
        max_replication_slots: 10
        wal_keep_size: '1GB'
        synchronous_commit: 'on'
        shared_buffers: '${shared_buffers_mb}MB'
        effective_cache_size: '$((shared_buffers_mb * 3))MB'
        log_destination: 'stderr'
        logging_collector: 'off'
        log_statement: 'ddl'
        log_min_duration_statement: 1000

  initdb:
    - encoding: UTF8
    - data-checksums

  pg_hba:
${pg_hba_entries}

  post_init: /etc/patroni/post_init.sh

postgresql:
  listen: 0.0.0.0:5432
  connect_address: ${node_ip}:5432
  data_dir: ${pg_data_dir}
  bin_dir: ${pg_bin_dir}
  config_dir: ${pg_conf_dir}

  authentication:
    replication:
      username: ${repl_user}
      password: "${escaped_repl_pw}"
    superuser:
      username: postgres
      password: "${escaped_superuser_pw}"

  parameters:
    unix_socket_directories: '/var/run/postgresql'

  pg_hba:
${pg_hba_runtime_entries}

tags:
  nofailover: false
  noloadbalance: false
  clonefrom: false
  nosync: false
EOF

    # Create post-init script
    # IMPORTANT: Both heredocs are QUOTED ('POSTINIT' and 'EOF') to prevent ALL
    # variable expansion. The literal ${DB_USER} etc. are replaced by sed below.
    # The inner heredoc MUST be quoted to prevent bash from expanding $, backtick,
    # and \ in the substituted password at runtime (e.g., password "my$ecret" would
    # become "my" + shell-expansion-of-$ecret if the inner heredoc were unquoted).
    tee /etc/patroni/post_init.sh > /dev/null << 'POSTINIT'
#!/bin/bash
set -e
sleep 5
psql -U postgres -h /var/run/postgresql << 'EOF'
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${DB_USER}') THEN
        CREATE ROLE "${DB_USER}" WITH LOGIN PASSWORD '${DB_PASSWORD}' CREATEDB;
    END IF;
END
$$;
SELECT 'CREATE DATABASE "${DB_NAME}" OWNER "${DB_USER}"'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '${DB_NAME}')\gexec
GRANT ALL PRIVILEGES ON DATABASE "${DB_NAME}" TO "${DB_USER}";
EOF
POSTINIT

    # Replace variables in post_init.sh
    # Escape all values for sed (backslash, ampersand, pipe are sed metacharacters)
    local safe_password="${db_password//\'/\'\'}"
    local sed_safe_password sed_safe_user sed_safe_name
    # Also escape $ to prevent bash variable expansion in the double-quoted sed command
    sed_safe_password=$(printf '%s' "$safe_password" | sed 's/[\\&|]/\\&/g; s/\$/\\$/g')
    sed_safe_user=$(printf '%s' "$db_user" | sed 's/[\\&|]/\\&/g; s/\$/\\$/g')
    sed_safe_name=$(printf '%s' "$db_name" | sed 's/[\\&|]/\\&/g; s/\$/\\$/g')
    sed -i "s|\${DB_USER}|${sed_safe_user}|g" /etc/patroni/post_init.sh || { log_error "Failed to substitute DB_USER in post_init.sh"; return 1; }
    sed -i "s|\${DB_NAME}|${sed_safe_name}|g" /etc/patroni/post_init.sh || { log_error "Failed to substitute DB_NAME in post_init.sh"; return 1; }
    sed -i "s|\${DB_PASSWORD}|${sed_safe_password}|g" /etc/patroni/post_init.sh || { log_error "Failed to substitute DB_PASSWORD in post_init.sh"; return 1; }

    chmod 700 /etc/patroni/post_init.sh
    chown postgres:postgres /etc/patroni/post_init.sh
    chmod 600 /etc/patroni/patroni.yml
    chown postgres:postgres /etc/patroni/patroni.yml

    log_success "Patroni configured"
}

# Create Patroni systemd service (modeled on install.sh:16562)
ha_create_patroni_service() {
    tee /etc/systemd/system/patroni.service > /dev/null << 'EOF'
[Unit]
Description=Patroni PostgreSQL HA Manager for Spiral Pool
Documentation=https://patroni.readthedocs.io
After=network.target etcd.service
Requires=etcd.service

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=/opt/patroni/venv/bin/patroni /etc/patroni/patroni.yml
ExecReload=/bin/kill -HUP $MAINPID
KillMode=process
TimeoutSec=30
Restart=always
RestartSec=5

StandardOutput=journal
StandardError=journal
SyslogIdentifier=patroni

NoNewPrivileges=false
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/etc/postgresql /etc/patroni /var/lib/postgresql /var/log/patroni /var/run/postgresql

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload || true
    log_success "Patroni service created"
}

# Migrate standalone PostgreSQL to Patroni (modeled on install.sh:16604)
# Usage: ha_migrate_to_patroni <ha_mode> <pg_version> <superuser_pw> <repl_user> <repl_pw>
ha_migrate_to_patroni() {
    local ha_mode="$1"
    local pg_version="$2"
    local superuser_pw="$3"
    local repl_user="$4"
    local repl_pw="$5"

    log_info "Migrating PostgreSQL to Patroni management..."

    # PRIMARY: Update credentials in a running PostgreSQL instance.
    # Two cases: (1) standalone PG (postgresql service active) — first run
    #            (2) Patroni-managed PG (patroni service active) — --force re-run
    # Both use Unix socket auth (local peer) so no password is needed for the postgres OS user.
    local pg_is_running=false
    if [[ "$ha_mode" == "ha-master" ]]; then
        if systemctl is-active --quiet postgresql 2>/dev/null; then
            pg_is_running=true
        elif systemctl is-active --quiet patroni 2>/dev/null; then
            pg_is_running=true
            log_info "Patroni-managed PostgreSQL detected (--force re-run)"
        elif [[ -d "/var/lib/postgresql/$pg_version/main/base" ]]; then
            # --force re-run: Patroni was stopped in step 2 (to avoid DCS disconnect during
            # etcd reconfigure). PG is also stopped. We need to temporarily start standalone
            # PG to apply new credentials via ALTER USER. Without this, new passwords are
            # written to patroni.yml but the PG catalog still has the OLD passwords.
            # Remove standby.signal if present (left by previous Patroni replica setup) —
            # standalone PG won't start with it (tries to connect to nonexistent primary).
            rm -f "/var/lib/postgresql/$pg_version/main/standby.signal"
            log_info "Starting standalone PostgreSQL temporarily to update credentials..."
            systemctl enable postgresql >/dev/null 2>&1 || true
            if systemctl start postgresql 2>/dev/null; then
                pg_is_running=true
                log_info "Standalone PostgreSQL started for credential update"
            else
                log_warn "Could not start standalone PG — credentials will be set by Patroni on next start"
            fi
        fi
    fi
    if [[ "$pg_is_running" == "true" ]] && [[ "$ha_mode" == "ha-master" ]]; then
        log_info "Preparing PostgreSQL for Patroni takeover..."

        # Set postgres superuser password
        # Pipe SQL to psql to avoid bash variable expansion of $ in passwords
        local escaped_su_pw
        escaped_su_pw=$(printf '%s' "$superuser_pw" | sed "s/'/''/g")
        if printf "ALTER USER postgres WITH PASSWORD '%s';\n" "$escaped_su_pw" | sudo -u postgres psql -q 2>/dev/null; then
            log_success "PostgreSQL superuser password set"
        else
            log_error "Failed to set PostgreSQL superuser password"
            return 1
        fi

        # Create replication user
        # Pipe SQL to psql to avoid bash variable expansion of $ in passwords
        local escaped_repl_pw
        escaped_repl_pw=$(printf '%s' "$repl_pw" | sed "s/'/''/g")
        printf "DO \$spiral_ha\$
        BEGIN
            IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN
                CREATE ROLE \"%s\" WITH REPLICATION LOGIN ENCRYPTED PASSWORD '%s';
            ELSE
                ALTER ROLE \"%s\" WITH ENCRYPTED PASSWORD '%s';
            END IF;
        END
        \$spiral_ha\$;\n" "$repl_user" "$repl_user" "$escaped_repl_pw" "$repl_user" "$escaped_repl_pw" \
            | sudo -u postgres psql -q 2>/dev/null || log_warn "Could not create replication user (Patroni will handle)"

        # Ensure pg_hba.conf allows TCP from localhost
        local pg_hba="/etc/postgresql/$pg_version/main/pg_hba.conf"
        if [[ -f "$pg_hba" ]] && ! grep -q "host.*all.*all.*127.0.0.1/32.*scram-sha-256" "$pg_hba" 2>/dev/null; then
            log_info "Adding localhost TCP auth entry to pg_hba.conf..."
            echo "host all all 127.0.0.1/32 scram-sha-256" >> "$pg_hba"
        fi

        # Reload to pick up pg_hba changes
        systemctl reload postgresql 2>/dev/null || true
        log_success "PostgreSQL prepared for Patroni takeover"
    fi

    # Stop standalone PostgreSQL
    log_info "Stopping standalone PostgreSQL service..."
    systemctl stop postgresql 2>/dev/null || true
    systemctl disable postgresql 2>/dev/null || true

    # Stop Patroni if running (--force re-run: Patroni may be managing PG from a previous attempt).
    if systemctl is-active --quiet patroni 2>/dev/null; then
        log_info "Stopping Patroni (will re-bootstrap)..."
        systemctl stop patroni 2>/dev/null || true
    fi

    # Unconditionally wait for ALL postgres processes to exit, regardless of source
    # (postgresql service, Patroni, or orphans from crashed process manager).
    # Without this, the data dir move below fails with "Device or resource busy".
    if pgrep -x postgres &>/dev/null; then
        log_info "Waiting for postgres processes to exit..."
        local pg_wait=15
        while [[ $pg_wait -gt 0 ]] && pgrep -x postgres &>/dev/null; do
            sleep 1
            pg_wait=$((pg_wait - 1))
        done
        if pgrep -x postgres &>/dev/null; then
            log_warn "postgres still running after 15s — sending SIGTERM..."
            pkill -x postgres 2>/dev/null || true
            sleep 3
            if pgrep -x postgres &>/dev/null; then
                log_warn "postgres still running — sending SIGKILL..."
                pkill -9 -x postgres 2>/dev/null || true
                sleep 1
            fi
        fi
    fi

    # Backup existing data directory on backup node — Patroni needs a clean data dir
    # to do pg_basebackup from primary. If the move fails, Patroni starts with stale
    # data instead of basebackup, causing replication divergence.
    local pg_data_dir="/var/lib/postgresql/$pg_version/main"
    if [[ "$ha_mode" == "ha-backup" ]] && [[ -d "$pg_data_dir" ]]; then
        log_info "Backing up existing data directory (Patroni will bootstrap from primary)..."
        mv "$pg_data_dir" "${pg_data_dir}.backup.$(date +%Y%m%d%H%M%S)" || {
            log_error "Failed to move existing PG data directory: $pg_data_dir"
            log_error "A process may be holding locks. Check: lsof +D $pg_data_dir"
            return 1
        }
        # Verify it's actually gone
        if [[ -d "$pg_data_dir" ]]; then
            log_error "PG data directory still exists after move: $pg_data_dir"
            return 1
        fi
    fi

    # Start Patroni
    log_info "Starting Patroni..."
    systemctl enable patroni >/dev/null 2>&1 || true
    systemctl start patroni || { log_error "Failed to start Patroni"; return 1; }

    # Wait for Patroni to become healthy
    # Backup nodes do a full pg_basebackup from primary — for large databases this
    # can take several minutes. Use longer timeout for backups (600s vs 120s).
    local retries=60
    if [[ "$ha_mode" == "ha-backup" ]]; then
        retries=300
        log_info "Waiting for Patroni to become healthy (backup: pg_basebackup may take several minutes)..."
    else
        log_info "Waiting for Patroni to become healthy..."
    fi
    while [[ $retries -gt 0 ]]; do
        if curl -sf --connect-timeout 3 --max-time 5 http://localhost:8008/health &>/dev/null; then
            log_success "Patroni is healthy"

            local role="unknown"
            local role_retries=15
            while [[ "$role" == "unknown" ]] && [[ $role_retries -gt 0 ]]; do
                role=$(curl -sf "${PATRONI_CURL_AUTH[@]}" --connect-timeout 3 --max-time 5 http://localhost:8008/ 2>/dev/null | grep -oP '"role"\s*:\s*"\K[^"]+' || echo "unknown")
                [[ "$role" != "unknown" ]] && break
                sleep 2
                role_retries=$((role_retries - 1))
            done

            if [[ "$role" == "unknown" ]]; then
                log_warn "Patroni role not resolved — check etcd connectivity"
            else
                log_success "Patroni role: $role"
            fi
            return 0
        fi
        sleep 2
        retries=$((retries - 1))
        if [[ $((retries % 10)) -eq 0 ]] && [[ $retries -gt 0 ]]; then
            log_info "Still waiting for Patroni... ($retries retries left)"
        fi
    done

    log_error "Patroni failed to become healthy within timeout"
    log_error "Check: journalctl -u patroni -n 50"
    return 1
}

# Append HA configuration to config.yaml (modeled on install.sh:17118)
# Usage: ha_append_config_yaml <vip> <iface> <netmask> <priority> <token> <ha_mode> <node_ip> <primary_ip> <repl_pw>
ha_append_config_yaml() {
    local vip="$1"
    local iface="$2"
    local netmask="$3"
    local priority="$4"
    local token="$5"
    local ha_mode="$6"
    local node_ip="$7"
    local primary_ip="${8:-}"
    local repl_pw="${9:-}"

    # Verify config.yaml exists before appending (tee -a on missing file creates
    # a config with ONLY HA settings — stratum would fail to start)
    if [[ ! -f "$CONFIG_FILE" ]]; then
        log_error "config.yaml not found at $CONFIG_FILE — cannot append HA configuration"
        return 1
    fi

    local node_role="primary"
    local peer_host="$node_ip"
    if [[ "$ha_mode" == "ha-backup" ]]; then
        node_role="backup"
        peer_host="${primary_ip:-127.0.0.1}"
    fi

    # Only append config.yaml sections if vip: doesn't exist yet.
    # ha.env and ha.yaml are ALWAYS written (even on --force re-run when vip: persists
    # after ha_remove_config_yaml failure), so passwords and display info stay current.
    if grep -q "^vip:" "$CONFIG_FILE" 2>/dev/null; then
        log_warn "vip: section already exists in config.yaml — skipping config.yaml append"
    else
    sudo -u "$POOL_USER" tee -a "$CONFIG_FILE" > /dev/null << EOF

# ═══════════════════════════════════════════════════════════════════════════════
# HIGH AVAILABILITY CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

# VIP (Virtual IP) Configuration - for transparent miner failover
vip:
  enabled: true
  address: "$vip"
  interface: "$iface"
  netmask: $netmask
  priority: $priority
  autoPriority: false
  clusterToken: "$token"
  canBecomeMaster: true
  discoveryPort: 5363
  statusPort: 5354
  heartbeatInterval: 30s
  failoverTimeout: 90s

# HA Coordination - database replication and failover settings
ha:
  enabled: true
  primaryHost: "${peer_host}"
  replicaHost: ""
  checkInterval: 5s
  failoverTimeout: 30s
EOF
    fi  # end of "vip: section doesn't exist" guard

    # Create ha.env (always written — keeps replication password current on --force re-run)
    # Uses printf instead of heredoc to prevent bash expansion of $ in passwords
    mkdir -p /etc/spiralpool
    {
        echo "# Spiral Pool HA Environment Variables"
        echo "# Generated by spiralctl ha enable on $(date)"
        echo ""
        printf 'SPIRAL_REPLICATION_PASSWORD="%s"\n' "$repl_pw"
    } > /etc/spiralpool/ha.env
    chmod 600 /etc/spiralpool/ha.env
    chown "$POOL_USER:$POOL_USER" /etc/spiralpool/ha.env

    # Create ha.yaml — used by spiralctl status (VIP display) and pool-mode.sh (peer discovery).
    # This is a lightweight display/discovery file separate from config.yaml's HA sections.
    local ha_yaml_path="$INSTALL_DIR/config/ha.yaml"
    mkdir -p "$INSTALL_DIR/config"
    local peer_list_ip=""
    if [[ "$ha_mode" == "ha-backup" ]] && [[ -n "$primary_ip" ]]; then
        peer_list_ip="$primary_ip"
    fi
    tee "$ha_yaml_path" > /dev/null << HAYAML
# Spiral Pool HA Display Configuration
# Generated by spiralctl ha enable on $(date)
# Used by: spiralctl status (VIP display), pool-mode.sh (peer discovery)

enabled: true
address: "$vip"
interface: "$iface"
netmask: $netmask
priority: $priority
mode: "$node_role"
nodeIp: "$node_ip"
HAYAML
    if [[ -n "$peer_list_ip" ]]; then
        tee -a "$ha_yaml_path" > /dev/null << HAYAML
peers:
  - host: $peer_list_ip
HAYAML
    fi
    chown "$POOL_USER:$POOL_USER" "$ha_yaml_path"
    chmod 640 "$ha_yaml_path"  # SECURITY (F-07): 640 = owner rw, group r, world none

    log_success "HA configuration added to config.yaml"
}

# Deploy HA scripts (verify they exist, create symlinks if needed)
ha_deploy_scripts() {
    local scripts_dir="$INSTALL_DIR/scripts"

    local -a script_pairs=(
        "ha-service-control.sh:spiralpool-ha-service"
        "etcd-quorum-recover.sh:spiralpool-etcd-recover"
        "patroni-force-bootstrap.sh:spiralpool-patroni-bootstrap"
        "ha-validate.sh:spiralpool-ha-validate"
    )

    for pair in "${script_pairs[@]}"; do
        local script="${pair%%:*}"
        local link="${pair##*:}"
        if [[ -f "$scripts_dir/$script" ]]; then
            chmod +x "$scripts_dir/$script"
            ln -sf "$scripts_dir/$script" "/usr/local/bin/$link"
        else
            log_warn "HA script not found: $scripts_dir/$script"
        fi
    done

    # Scripts without symlinks — ha-role-watcher.sh is MANDATORY (the watcher service
    # ExecStart points to it; without it, the watcher fails and HA is non-functional)
    if [[ ! -f "$scripts_dir/ha-role-watcher.sh" ]]; then
        log_error "CRITICAL: ha-role-watcher.sh not found at $scripts_dir/"
        log_error "This script is required for HA operation. Re-run install.sh or upgrade.sh first."
        return 1
    fi
    for script in ha-role-watcher.sh etcd-cluster-rejoin.sh ha-failback.sh; do
        if [[ -f "$scripts_dir/$script" ]]; then
            chmod +x "$scripts_dir/$script"
        else
            log_warn "Optional HA script not found: $scripts_dir/$script"
        fi
    done

    log_success "HA scripts verified"
}

# Install HA role watcher systemd service (modeled on install.sh:30752)
ha_install_watcher() {
    local install_dir="$1"
    local pool_user="$2"

    # Try to use template from source repo if available
    local service_src="$install_dir/scripts/linux/systemd/spiralpool-ha-watcher.service"
    if [[ ! -f "$service_src" ]]; then
        service_src="$install_dir/scripts/systemd/spiralpool-ha-watcher.service"
    fi

    if [[ -f "$service_src" ]]; then
        cp "$service_src" /etc/systemd/system/spiralpool-ha-watcher.service
        sed -i "s|{{INSTALL_DIR}}|$install_dir|g" /etc/systemd/system/spiralpool-ha-watcher.service
        sed -i "s|{{POOL_USER}}|$pool_user|g" /etc/systemd/system/spiralpool-ha-watcher.service
    elif [[ -f /etc/systemd/system/spiralpool-ha-watcher.service ]]; then
        log_info "Watcher service unit already exists"
    else
        # Standalone→HA conversion: template doesn't exist on disk.
        # Generate the service file inline (matches the template from install.sh).
        log_info "Generating watcher service unit inline..."
        tee /etc/systemd/system/spiralpool-ha-watcher.service > /dev/null << EOF
# Spiral Pool HA Role Watcher Service
# Generated by spiralctl ha enable

[Unit]
Description=Spiral Pool HA Role Watcher
Documentation=https://github.com/SpiralPool/Spiral-Pool
After=network-online.target spiralstratum.service
Wants=network-online.target

[Service]
Type=simple
User=${pool_user}
Group=${pool_user}
WorkingDirectory=${install_dir}
ExecStart=${install_dir}/scripts/ha-role-watcher.sh start

Environment="SPIRALPOOL_INSTALL_DIR=${install_dir}"

Restart=always
RestartSec=5

StandardOutput=journal
StandardError=journal

PrivateTmp=yes
ProtectSystem=full
ProtectHome=no
RuntimeDirectory=spiralpool
RuntimeDirectoryPreserve=yes
ReadWritePaths=${install_dir} /run/spiralpool /home/${pool_user}/.ssh /etc/default

ProtectKernelLogs=yes
ProtectKernelModules=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes

RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

RestrictRealtime=yes
LockPersonality=yes
PrivateDevices=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
EOF
    fi

    systemctl daemon-reload || true
    systemctl enable spiralpool-ha-watcher >/dev/null 2>&1 || true
    if ! systemctl start spiralpool-ha-watcher 2>/dev/null; then
        log_error "Failed to start HA watcher"
        return 1
    fi
    # Verify it's actually running (systemctl start can return 0 for Type=simple even if it crashes)
    sleep 2
    if ! systemctl is-active --quiet spiralpool-ha-watcher 2>/dev/null; then
        log_error "HA watcher service exited shortly after start"
        return 1
    fi
    log_success "HA role watcher service started"
}

# Configure firewall for HA peer (modeled on install.sh:12152)
# Usage: ha_configure_firewall <peer_ip>
ha_configure_firewall() {
    local peer_ip="$1"

    if ! command -v ufw &>/dev/null; then
        log_warn "ufw not found — skipping firewall configuration"
        return 0
    fi

    # All ufw commands use || true — ufw can return non-zero if inactive, if the
    # rule already exists (some versions), or for various system reasons. Under set -e,
    # an unguarded ufw failure would kill the entire ha_enable script.
    ufw allow from "$peer_ip" to any port 22 proto tcp > /dev/null 2>&1 || true    # SSH
    ufw allow from "$peer_ip" to any port 5363 proto udp > /dev/null 2>&1 || true  # HA discovery
    ufw allow from "$peer_ip" to any port 5354 proto tcp > /dev/null 2>&1 || true  # HA status API
    ufw allow from "$peer_ip" to any port 5432 proto tcp > /dev/null 2>&1 || true  # PostgreSQL
    ufw allow from "$peer_ip" to any port 2379 proto tcp > /dev/null 2>&1 || true  # etcd client
    ufw allow from "$peer_ip" to any port 2380 proto tcp > /dev/null 2>&1 || true  # etcd peer
    ufw allow from "$peer_ip" to any port 8008 proto tcp > /dev/null 2>&1 || true  # Patroni REST
    ufw allow proto vrrp from "$peer_ip" > /dev/null 2>&1 || true                  # VRRP

    log_success "Firewall rules added for peer $peer_ip"
}

# Create HA marker files (modeled on install.sh:30744)
# Usage: ha_create_markers <ha_mode>
ha_create_markers() {
    local ha_mode="$1"

    mkdir -p "$INSTALL_DIR/config"
    touch "$INSTALL_DIR/config/ha-enabled"
    chown "$POOL_USER:$POOL_USER" "$INSTALL_DIR/config/ha-enabled"

    echo "$ha_mode" > "$INSTALL_DIR/config/ha-mode"
    chown "$POOL_USER:$POOL_USER" "$INSTALL_DIR/config/ha-mode"

    log_success "HA markers created (mode: $ha_mode)"
}

# Create replication sudoers on primary (modeled on install.sh:16763)
# Usage: ha_setup_replication_sudoers <pool_user>
ha_setup_replication_sudoers() {
    local pool_user="$1"
    local ha_sudoers="/etc/sudoers.d/spiralpool-ha-postgres"

    if [[ -f "$ha_sudoers" ]]; then
        log_info "HA replication sudoers already exists"
        return 0
    fi

    tee "$ha_sudoers" > /dev/null << SUDOERS_EOF
# Allow ${pool_user} to run rsync as postgres (for HA PostgreSQL replication)
${pool_user} ALL=(postgres) NOPASSWD: /usr/bin/rsync, /usr/bin/true, /usr/bin/ls
# Allow ${pool_user} to stop/start PostgreSQL and Patroni (for cold-copy replication)
${pool_user} ALL=(root) NOPASSWD: /usr/bin/systemctl stop postgresql@*, /usr/bin/systemctl start postgresql@*, /usr/bin/systemctl is-active postgresql@*, /usr/bin/systemctl stop patroni, /usr/bin/systemctl start patroni
SUDOERS_EOF
    chmod 440 "$ha_sudoers"
    log_success "HA replication sudoers created"
}

# Set up SSH keys for HA replication (modeled on install.sh:7822)
# PRIMARY: Generates SSH key if missing (backup will push its key to us)
# BACKUP: Generates SSH key, copies to primary via sshpass, sets up bidirectional SSH
# Usage: ha_setup_ssh <ha_mode> <pool_user> [primary_ip] [ssh_password]
ha_setup_ssh() {
    local ha_mode="$1"
    local pool_user="$2"
    local primary_ip="${3:-}"
    local ssh_password="${4:-}"

    local ssh_dir="/home/${pool_user}/.ssh"
    local ssh_key_path="${ssh_dir}/id_ed25519"

    # Ensure .ssh directory exists with correct permissions
    if [[ ! -d "$ssh_dir" ]]; then
        sudo -u "$pool_user" mkdir -p "$ssh_dir"
    fi
    chown "${pool_user}:${pool_user}" "$ssh_dir"
    chmod 700 "$ssh_dir"

    # Generate SSH key if it doesn't exist
    if [[ -f "$ssh_key_path" ]]; then
        chown "${pool_user}:${pool_user}" "$ssh_key_path"
        chmod 600 "$ssh_key_path"
        [[ -f "${ssh_key_path}.pub" ]] && chown "${pool_user}:${pool_user}" "${ssh_key_path}.pub" && chmod 644 "${ssh_key_path}.pub"
        log_info "Using existing SSH key: $ssh_key_path"
    else
        log_info "Generating SSH key for ${pool_user}..."
        sudo -u "$pool_user" ssh-keygen -t ed25519 -f "$ssh_key_path" -N "" -C "spiralpool-ha-replication" -q || {
            log_error "ssh-keygen failed — cannot generate SSH key"
            return 1
        }
        if [[ ! -f "$ssh_key_path" ]]; then
            log_error "SSH key not found after generation: $ssh_key_path"
            return 1
        fi
        log_success "SSH key generated"
    fi

    # PRIMARY: Just ensure key exists (backup will push its key via ssh-copy-id)
    if [[ "$ha_mode" == "ha-master" ]]; then
        log_success "SSH key ready (backup nodes will exchange keys during their setup)"
        return 0
    fi

    # BACKUP: Deploy key to primary and set up bidirectional SSH
    if [[ -z "$primary_ip" ]]; then
        log_warn "No primary IP — skipping SSH key exchange"
        return 0
    fi

    # Install sshpass for automated key deployment (required for backup SSH setup)
    if ! command -v sshpass &>/dev/null; then
        log_info "Installing sshpass..."
        apt-get update -qq > /dev/null 2>&1
        apt-get install -y -qq sshpass || {
            log_error "Failed to install sshpass (required for automated SSH key exchange)"
            echo "  Install manually: sudo apt-get install -y sshpass"
            return 1
        }
        if ! command -v sshpass &>/dev/null; then
            log_error "sshpass not found after install — SSH key exchange will fail"
            return 1
        fi
    fi

    # Test if we already have passwordless access
    if sudo -u "$pool_user" ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
        "${pool_user}@${primary_ip}" "echo 'SSH OK'" &>/dev/null 2>&1; then
        log_success "SSH key already authorized on primary"
    else
        # Need password to deploy key
        if [[ -z "$ssh_password" ]]; then
            log_error "SSH key not authorized on primary and no --ssh-password provided"
            echo ""
            echo "  The backup node needs to deploy its SSH key to the primary."
            echo "  Provide the spiraluser system password from the primary node:"
            echo ""
            echo "    --ssh-password <password>"
            echo ""
            echo "  To find it on the primary, check the install output or run:"
            echo "    sudo grep spiraluser /etc/shadow  (verify user exists)"
            echo ""
            return 1
        fi

        log_info "Deploying SSH key to primary (${primary_ip})..."
        local ssh_copy_result
        ssh_copy_result=$(sudo -u "$pool_user" bash -c \
            'SSHPASS="$1" sshpass -e ssh-copy-id -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -i "$2" "$3"' \
            _ "$ssh_password" "${ssh_key_path}.pub" "${pool_user}@${primary_ip}" 2>&1) || true

        # Clear password from memory
        unset ssh_password 2>/dev/null

        # Verify access
        if sudo -u "$pool_user" ssh -o BatchMode=yes -o ConnectTimeout=10 \
            "${pool_user}@${primary_ip}" "echo 'SSH OK'" &>/dev/null 2>&1; then
            log_success "SSH key deployed to primary"
        else
            log_error "SSH key deployment failed"
            log_error "Output: $ssh_copy_result"
            echo ""
            echo "  Possible causes:"
            echo "    - Wrong spiraluser password"
            echo "    - SSH password auth disabled on primary"
            echo "    - Firewall blocking port 22"
            echo ""
            echo "  Manual fix: sudo -u ${pool_user} ssh-copy-id ${pool_user}@${primary_ip}"
            return 1
        fi
    fi

    # Bidirectional SSH: Fetch primary's public key so primary can SSH back for failback
    log_info "Setting up bidirectional SSH (primary→backup for failback)..."
    local primary_pubkey
    primary_pubkey=$(sudo -u "$pool_user" ssh -o BatchMode=yes -o ConnectTimeout=10 \
        "${pool_user}@${primary_ip}" "cat /home/${pool_user}/.ssh/id_ed25519.pub" 2>/dev/null || echo "")

    if [[ -n "$primary_pubkey" ]]; then
        local auth_keys="$ssh_dir/authorized_keys"
        if ! grep -qF "$primary_pubkey" "$auth_keys" 2>/dev/null; then
            echo "$primary_pubkey" | tee -a "$auth_keys" > /dev/null
            chown "${pool_user}:${pool_user}" "$auth_keys"
            chmod 600 "$auth_keys"
            log_success "Primary's SSH key added to local authorized_keys"
        else
            log_info "Primary's SSH key already in local authorized_keys"
        fi
    else
        log_warn "Could not fetch primary's SSH public key"
        log_warn "  Fix: Copy primary's /home/${pool_user}/.ssh/id_ed25519.pub to backup's authorized_keys"
    fi

    # SSH Mesh: Exchange keys with other backup nodes via primary relay
    log_info "Checking for other backup nodes for SSH mesh..."
    local local_pub_key
    local_pub_key=$(cat "${ssh_key_path}.pub" 2>/dev/null || echo "")
    if [[ -n "$local_pub_key" ]]; then
        local member_ips
        member_ips=$(sudo -u "$pool_user" ssh -o BatchMode=yes -o ConnectTimeout=10 \
            "${pool_user}@${primary_ip}" \
            "ETCDCTL_API=3 etcdctl member list --write-out=simple 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | sort -u" 2>/dev/null || echo "")

        if [[ -n "$member_ips" ]]; then
            local my_ips
            my_ips=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' || echo "")
            local mesh_count=0

            while IFS= read -r member_ip; do
                [[ -z "$member_ip" ]] && continue
                # Skip self
                echo "$my_ips" | grep -qx "$member_ip" && continue
                # Skip primary (already have bidirectional SSH)
                [[ "$member_ip" == "$primary_ip" ]] && continue

                log_info "SSH mesh with peer $member_ip..."

                # Push our pubkey to peer via primary relay
                sudo -u "$pool_user" ssh -o BatchMode=yes -o ConnectTimeout=10 \
                    "${pool_user}@${primary_ip}" \
                    "ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
                     ${pool_user}@${member_ip} \
                     'grep -qF \"${local_pub_key}\" ~/.ssh/authorized_keys 2>/dev/null || echo \"${local_pub_key}\" >> ~/.ssh/authorized_keys'" 2>/dev/null || true

                # Fetch peer's pubkey and add locally
                local peer_pub_key
                peer_pub_key=$(sudo -u "$pool_user" ssh -o BatchMode=yes -o ConnectTimeout=10 \
                    "${pool_user}@${primary_ip}" \
                    "ssh -o BatchMode=yes -o ConnectTimeout=10 ${pool_user}@${member_ip} \
                     'cat ~/.ssh/id_ed25519.pub 2>/dev/null'" 2>/dev/null || echo "")
                if [[ -n "$peer_pub_key" ]]; then
                    local mesh_auth="$ssh_dir/authorized_keys"
                    if ! grep -qF "$peer_pub_key" "$mesh_auth" 2>/dev/null; then
                        echo "$peer_pub_key" | tee -a "$mesh_auth" > /dev/null
                        chown "${pool_user}:${pool_user}" "$mesh_auth"
                        chmod 600 "$mesh_auth"
                    fi
                    mesh_count=$((mesh_count + 1))
                fi

                # Accept peer host key
                sudo -u "$pool_user" ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=5 \
                    "${pool_user}@${member_ip}" "echo 'mesh-ok'" &>/dev/null 2>&1 || true
            done <<< "$member_ips"

            if [[ $mesh_count -gt 0 ]]; then
                log_success "SSH mesh with $mesh_count other backup node(s)"
            else
                log_info "No other backup nodes found (2-node cluster)"
            fi
        fi
    fi

    log_success "SSH setup complete"
}

# Remove HA configuration from config.yaml
ha_remove_config_yaml() {
    if [[ ! -f "$CONFIG_FILE" ]]; then
        return 0
    fi

    # Backup first
    cp "$CONFIG_FILE" "${CONFIG_FILE}.pre-ha-disable.$(date +%Y%m%d%H%M%S)"
    log_info "Config backed up"

    # Find the HA section header and delete from there through EOF
    local header_line
    header_line=$(grep -n "# HIGH AVAILABILITY CONFIGURATION" "$CONFIG_FILE" 2>/dev/null | head -1 | cut -d: -f1)

    if [[ -n "$header_line" ]]; then
        # Delete from 2 lines before header (the blank line + separator) through EOF
        local start_line=$((header_line - 2))
        [[ $start_line -lt 1 ]] && start_line=$header_line
        sed -i "${start_line},\$d" "$CONFIG_FILE"
        # Verify removal succeeded (sed can silently fail on read-only FS, disk full, etc.)
        if grep -q "^vip:" "$CONFIG_FILE" 2>/dev/null || grep -q "^ha:" "$CONFIG_FILE" 2>/dev/null; then
            log_error "HA sections still present in config.yaml after sed removal"
            return 1
        fi
        log_success "HA sections removed from config.yaml"
        return 0
    fi

    # Fallback: find vip: line
    local vip_line
    vip_line=$(grep -n "^vip:" "$CONFIG_FILE" 2>/dev/null | head -1 | cut -d: -f1)
    if [[ -n "$vip_line" ]]; then
        # Delete from 1 line before vip: through EOF
        local start_line=$((vip_line - 1))
        [[ $start_line -lt 1 ]] && start_line=$vip_line
        sed -i "${start_line},\$d" "$CONFIG_FILE"
        # Verify removal
        if grep -q "^vip:" "$CONFIG_FILE" 2>/dev/null || grep -q "^ha:" "$CONFIG_FILE" 2>/dev/null; then
            log_error "VIP/HA sections still present in config.yaml after sed removal"
            return 1
        fi
        log_success "VIP/HA sections removed from config.yaml"
        return 0
    fi

    log_info "No HA sections found in config.yaml"
}

# Revert PostgreSQL from Patroni to standalone
# Usage: ha_revert_postgresql <pg_version>
ha_revert_postgresql() {
    local pg_version="$1"

    # Stop Patroni
    if systemctl is-active --quiet patroni 2>/dev/null; then
        log_info "Stopping Patroni..."
        systemctl stop patroni
    fi
    systemctl disable patroni 2>/dev/null || true

    # Wait for postgres processes to exit (Patroni may have spawned them)
    local wait_count=15
    while [[ $wait_count -gt 0 ]] && pgrep -x postgres &>/dev/null; do
        sleep 1
        wait_count=$((wait_count - 1))
    done
    # Force-kill orphaned postgres if still running (prevents data dir locks)
    if pgrep -x postgres &>/dev/null; then
        log_warn "postgres still running after 15s — sending SIGTERM..."
        pkill -x postgres 2>/dev/null || true
        sleep 3
        if pgrep -x postgres &>/dev/null; then
            log_warn "postgres still running — sending SIGKILL..."
            pkill -9 -x postgres 2>/dev/null || true
            sleep 1
        fi
    fi

    local pg_data_dir="/var/lib/postgresql/$pg_version/main"

    # Remove standby.signal — Patroni creates this on replicas. If left in place,
    # standalone PostgreSQL starts as a standby and tries to connect to a primary that
    # no longer exists (or isn't configured), failing to start.
    if [[ -f "$pg_data_dir/standby.signal" ]]; then
        log_info "Removing standby.signal (was a Patroni replica)..."
        rm -f "$pg_data_dir/standby.signal"
    fi

    # Truncate postgresql.auto.conf — Patroni writes many settings here via ALTER SYSTEM:
    # synchronous_standby_names, primary_conninfo, restore_command, primary_slot_name,
    # recovery_target_timeline, and DCS-managed params. The most dangerous is
    # synchronous_standby_names: with synchronous_commit=on, standalone PG hangs on
    # ALL writes forever waiting for replicas that no longer exist.
    # Selective sed is fragile (missed settings → broken DB). Truncating is safe:
    # PG falls back to postgresql.conf defaults for everything.
    if [[ -f "$pg_data_dir/postgresql.auto.conf" ]]; then
        log_info "Truncating postgresql.auto.conf (removing all Patroni ALTER SYSTEM overrides)..."
        cp "$pg_data_dir/postgresql.auto.conf" "$pg_data_dir/postgresql.auto.conf.ha-backup.$(date +%Y%m%d%H%M%S)"
        echo "# postgresql.auto.conf — cleared by spiralctl ha disable on $(date)" > "$pg_data_dir/postgresql.auto.conf"
        chown postgres:postgres "$pg_data_dir/postgresql.auto.conf"
    fi

    # Remove conf.d/spiral-ha.conf — install.sh creates this with HA-specific settings
    # (listen_addresses='*', wal_level=replica, synchronous_commit=on, etc.).
    # If left in place, standalone PG stays exposed on all interfaces and has HA
    # settings that don't make sense without replicas.
    local pg_conf_dir="/etc/postgresql/$pg_version/main"
    if [[ -f "$pg_conf_dir/conf.d/spiral-ha.conf" ]]; then
        log_info "Removing conf.d/spiral-ha.conf (HA-specific PostgreSQL settings)..."
        cp "$pg_conf_dir/conf.d/spiral-ha.conf" "$pg_conf_dir/conf.d/spiral-ha.conf.bak.$(date +%Y%m%d%H%M%S)"
        rm -f "$pg_conf_dir/conf.d/spiral-ha.conf"
    fi

    # Revert pg_hba.conf — remove HA network entries, ensure local peer auth exists
    local pg_hba="$pg_conf_dir/pg_hba.conf"
    if [[ -f "$pg_hba" ]]; then
        # Remove replication entries for cluster subnets (scram-sha-256 replication lines)
        if grep -q "host.*replication.*scram-sha-256" "$pg_hba" 2>/dev/null; then
            log_info "Removing HA replication entries from pg_hba.conf..."
            sed -i '/^host.*replication.*scram-sha-256/d' "$pg_hba"
        fi
        # Remove broad network host entries (host all all <subnet> scram-sha-256)
        # but preserve localhost entries
        if grep -qE "^host\s+all\s+all\s+[0-9]+\." "$pg_hba" 2>/dev/null; then
            log_info "Removing HA network host entries from pg_hba.conf..."
            sed -i '/^host[[:space:]]\+all[[:space:]]\+all[[:space:]]\+[0-9]/d' "$pg_hba"
        fi
        # Ensure local peer auth exists
        if ! grep -q "^local.*all.*all.*peer" "$pg_hba" 2>/dev/null; then
            log_info "Restoring local peer auth in pg_hba.conf..."
            sed -i '1i local all all peer' "$pg_hba"
        fi
        # Ensure localhost TCP entry (needed for spiralstratum)
        if ! grep -q "^host.*all.*all.*127.0.0.1/32" "$pg_hba" 2>/dev/null; then
            echo "host all all 127.0.0.1/32 scram-sha-256" >> "$pg_hba"
        fi
    fi

    # Re-enable and start standalone PostgreSQL
    systemctl enable postgresql >/dev/null 2>&1 || true
    systemctl start postgresql || {
        log_error "Failed to start standalone PostgreSQL"
        log_error "Check: journalctl -u postgresql -n 50"
        return 1
    }

    # Verify running
    if systemctl is-active --quiet postgresql 2>/dev/null; then
        log_success "PostgreSQL running in standalone mode"
    else
        log_error "PostgreSQL is not running after revert"
        return 1
    fi

    # Drop orphaned replication slots left by Patroni.
    # If not dropped, PostgreSQL retains WAL segments indefinitely for replicas
    # that will never reconnect, eventually filling the disk.
    local slots
    slots=$(sudo -u postgres psql -tAc "SELECT slot_name FROM pg_replication_slots;" 2>/dev/null || echo "")
    if [[ -n "$slots" ]]; then
        log_info "Dropping orphaned replication slots..."
        while IFS= read -r slot; do
            [[ -z "$slot" ]] && continue
            # Validate slot name format (PG slot names: lowercase, digits, underscores only)
            if [[ ! "$slot" =~ ^[a-z0-9_]+$ ]]; then
                log_warn "Skipping unexpected slot name format: $slot"
                continue
            fi
            if sudo -u postgres psql -qc "SELECT pg_drop_replication_slot('${slot}');" 2>/dev/null; then
                log_info "Dropped replication slot: $slot"
            else
                log_warn "Could not drop replication slot: $slot (may be active)"
            fi
        done <<< "$slots"
    fi

    # Drop orphaned replication role left by ha_enable.
    # The 'replicator' role was created for streaming replication and has
    # REPLICATION + LOGIN privileges with a known password. Not needed standalone.
    if sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'replicator';" 2>/dev/null | grep -q 1; then
        log_info "Dropping orphaned replication role 'replicator'..."
        sudo -u postgres psql -qc "DROP ROLE IF EXISTS \"replicator\";" 2>/dev/null || \
            log_warn "Could not drop replication role 'replicator' (may own objects)"
    fi
}

#===============================================================================
# HA ENABLE/DISABLE (full HA stack management)
#===============================================================================

# Show HA credentials and backup node command
ha_show_credentials() {
    detect_ha_state
    if [[ "$HA_STATE" == "standalone" ]]; then
        log_error "This node is not configured for HA"
        echo "  Run 'sudo spiralctl ha enable --vip <ip>' to enable HA first."
        return 1
    fi

    detect_server_ip

    # Read credentials from config files
    local superuser_pw=""
    local repl_pw=""
    local cluster_token=""
    local vip_address=""
    local ha_mode=""
    local interface=""
    local netmask=""
    local priority=""

    # Superuser password from patroni.yml (first password: line under superuser:)
    if [[ -f /etc/patroni/patroni.yml ]]; then
        superuser_pw=$(awk '/superuser:/{found=1} found && /password:/{gsub(/.*password:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' /etc/patroni/patroni.yml 2>/dev/null)
    fi

    # Replication password from ha.env
    if [[ -f /etc/spiralpool/ha.env ]]; then
        repl_pw=$(grep '^SPIRAL_REPLICATION_PASSWORD=' /etc/spiralpool/ha.env 2>/dev/null | cut -d'"' -f2)
        [[ -z "$repl_pw" ]] && repl_pw=$(grep '^SPIRAL_REPLICATION_PASSWORD=' /etc/spiralpool/ha.env 2>/dev/null | cut -d= -f2 | tr -d '"')
    fi

    # Cluster token from keepalived
    if [[ -f /etc/keepalived/.cluster_token ]]; then
        cluster_token=$(cat /etc/keepalived/.cluster_token 2>/dev/null)
    fi

    # VIP, interface, netmask, priority from config.yaml
    if [[ -f "$CONFIG_FILE" ]]; then
        vip_address=$(awk '/^vip:/{found=1} found && /address:/{gsub(/.*address:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null)
        interface=$(awk '/^vip:/{found=1} found && /interface:/{gsub(/.*interface:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null)
        netmask=$(awk '/^vip:/{found=1} found && /netmask:/{gsub(/.*netmask:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null)
        priority=$(awk '/^vip:/{found=1} found && /priority:/{gsub(/.*priority:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null)
    fi

    # HA mode from marker
    if [[ -f "$INSTALL_DIR/config/ha-mode" ]]; then
        ha_mode=$(cat "$INSTALL_DIR/config/ha-mode" 2>/dev/null)
    fi

    echo ""
    echo -e "${WHITE}╔═══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${WHITE}║                    HA CREDENTIALS & INFO                      ║${NC}"
    echo -e "${WHITE}╚═══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "${WHITE}Node:${NC}"
    echo "  Server IP:            ${SERVER_IP:-unknown}"
    echo "  HA Mode:              ${ha_mode:-unknown}"
    echo "  HA State:             $HA_STATE"
    echo ""
    echo -e "${WHITE}VIP Configuration:${NC}"
    echo "  VIP Address:          ${vip_address:-not set}"
    echo "  Interface:            ${interface:-not set}"
    echo "  Netmask:              ${netmask:-not set}"
    echo "  Priority:             ${priority:-not set}"
    echo ""
    echo -e "${WHITE}Credentials:${NC}"
    echo -e "  Superuser Password:   ${CYAN}${superuser_pw:-<not found in /etc/patroni/patroni.yml>}${NC}"
    echo -e "  Replication Password: ${CYAN}${repl_pw:-<not found in /etc/spiralpool/ha.env>}${NC}"
    echo -e "  Cluster Token:        ${CYAN}${cluster_token:-<not found in /etc/keepalived/.cluster_token>}${NC}"
    echo -e "  spiraluser Password:  ${DIM}(set during install — check your records)${NC}"
    echo ""

    # Determine next priority for a new backup
    local next_priority=101
    if [[ -n "$priority" ]] && [[ "$priority" =~ ^[0-9]+$ ]]; then
        # Count existing etcd members to determine next priority
        local member_count=0
        if ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 | grep -q "is healthy"; then
            member_count=$(ETCDCTL_API=3 etcdctl --command-timeout=5s member list --write-out=simple 2>/dev/null | wc -l)
        fi
        if [[ $member_count -gt 0 ]]; then
            next_priority=$((100 + member_count))
        fi
    fi

    # Only show the backup command if we have enough info
    if [[ -n "$vip_address" ]] && [[ -n "$cluster_token" ]]; then
        echo -e "${WHITE}Command to add a new backup node (run on the backup):${NC}"
        echo ""
        echo -e "  ${CYAN}sudo spiralctl ha enable \\"
        echo -e "    --vip $vip_address \\"
        [[ -n "$netmask" ]] && [[ "$netmask" != "32" ]] && echo -e "    --netmask $netmask \\"
        echo -e "    --token $cluster_token \\"
        echo -e "    --priority $next_priority \\"
        echo -e "    --primary-ip ${SERVER_IP:-<this-node-ip>} \\"
        [[ -n "$superuser_pw" ]] && echo -e "    --superuser-password '$superuser_pw' \\"
        [[ -n "$repl_pw" ]] && echo -e "    --repl-password '$repl_pw' \\"
        echo -e "    --ssh-password <spiraluser-system-password>${NC}"
        echo ""
        if [[ $next_priority -gt 101 ]]; then
            echo -e "${DIM}  Priority $next_priority based on $member_count existing cluster members.${NC}"
            echo -e "${DIM}  Adjust --priority if nodes were previously removed.${NC}"
        fi
        echo ""
    else
        echo -e "${YELLOW}  Cannot generate backup command — missing VIP or cluster token.${NC}"
        echo ""
    fi

    echo -e "${WHITE}Manual credential retrieval commands:${NC}"
    echo "  sudo grep -oP 'password: \"\\K[^\"]+' /etc/patroni/patroni.yml | head -1  # superuser pw"
    echo "  sudo grep SPIRAL_REPLICATION_PASSWORD /etc/spiralpool/ha.env               # replication pw"
    echo "  sudo cat /etc/keepalived/.cluster_token                                    # cluster token"
    echo ""
}

# Enable HA with full stack: etcd + Patroni + keepalived + config + watcher
ha_enable() {
    local vip_address=""
    local interface=""
    local priority=100
    local token=""
    local netmask="32"
    local primary_ip=""
    local repl_password=""
    local superuser_password=""
    local db_password_flag=""
    local ssh_password=""
    local force=false

    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --vip|--address)
                [[ -z "${2:-}" ]] && { log_error "--vip/--address requires a value"; exit 1; }
                vip_address="$2"
                shift 2
                ;;
            --interface)
                [[ -z "${2:-}" ]] && { log_error "--interface requires a value"; exit 1; }
                interface="$2"
                shift 2
                ;;
            --priority)
                [[ -z "${2:-}" ]] && { log_error "--priority requires a value"; exit 1; }
                priority="$2"
                if ! [[ "$priority" =~ ^[0-9]+$ ]] || [[ "$priority" -lt 1 ]] || [[ "$priority" -gt 255 ]]; then
                    log_error "--priority must be a number between 1 and 255"
                    exit 1
                fi
                shift 2
                ;;
            --token)
                [[ -z "${2:-}" ]] && { log_error "--token requires a value"; exit 1; }
                token="$2"
                # Validate token format: alphanumeric + hyphens only (prevents YAML/shell injection)
                if [[ ! "$token" =~ ^[A-Za-z0-9-]+$ ]]; then
                    log_error "--token must contain only alphanumeric characters and hyphens"
                    exit 1
                fi
                shift 2
                ;;
            --netmask)
                [[ -z "${2:-}" ]] && { log_error "--netmask requires a value"; exit 1; }
                netmask="$2"
                if ! [[ "$netmask" =~ ^[0-9]+$ ]] || [[ "$netmask" -lt 1 ]] || [[ "$netmask" -gt 32 ]]; then
                    log_error "--netmask must be a CIDR prefix length between 1 and 32"
                    exit 1
                fi
                shift 2
                ;;
            --primary-ip)
                [[ -z "${2:-}" ]] && { log_error "--primary-ip requires a value"; exit 1; }
                primary_ip="$2"
                shift 2
                ;;
            --repl-password)
                [[ -z "${2:-}" ]] && { log_error "--repl-password requires a value"; exit 1; }
                repl_password="$2"
                shift 2
                ;;
            --superuser-password)
                [[ -z "${2:-}" ]] && { log_error "--superuser-password requires a value"; exit 1; }
                superuser_password="$2"
                shift 2
                ;;
            --db-password)
                [[ -z "${2:-}" ]] && { log_error "--db-password requires a value"; exit 1; }
                db_password_flag="$2"
                shift 2
                ;;
            --ssh-password)
                [[ -z "${2:-}" ]] && { log_error "--ssh-password requires a value"; exit 1; }
                ssh_password="$2"
                shift 2
                ;;
            --force)
                force=true
                shift
                ;;
            *)
                log_error "Unknown option: $1"
                exit 1
                ;;
        esac
    done

    # ── Environment variable fallbacks for sensitive credentials ──────────────
    # CLI flags take precedence. Env vars allow users to avoid passwords in
    # shell history (bash/zsh record command lines) and ps aux output.
    # Env vars are unset immediately after reading to prevent leakage to
    # child processes.
    [[ -z "$repl_password"       && -n "${SPIRAL_REPL_PASSWORD:-}"      ]] && repl_password="$SPIRAL_REPL_PASSWORD"
    [[ -z "$superuser_password"  && -n "${SPIRAL_SUPERUSER_PASSWORD:-}" ]] && superuser_password="$SPIRAL_SUPERUSER_PASSWORD"
    [[ -z "$db_password_flag"    && -n "${SPIRAL_DB_PASSWORD:-}"        ]] && db_password_flag="$SPIRAL_DB_PASSWORD"
    [[ -z "$ssh_password"        && -n "${SPIRAL_SSH_PASSWORD:-}"       ]] && ssh_password="$SPIRAL_SSH_PASSWORD"
    unset SPIRAL_REPL_PASSWORD SPIRAL_SUPERUSER_PASSWORD SPIRAL_DB_PASSWORD SPIRAL_SSH_PASSWORD 2>/dev/null || true

    # ── Cloud / VPS deployment guard ──
    # HA requires keepalived VRRP for VIP failover — VRRP uses broadcast/multicast MAC election
    # which is blocked by virtually all cloud hypervisors (AWS, GCP, Azure, DigitalOcean, etc.).
    # Probe IMDS link-local 169.254.169.254 — present on all major cloud platforms.
    local _cloud_env=""
    if curl -s --connect-timeout 1 --max-time 2 http://169.254.169.254/latest/meta-data/ >/dev/null 2>&1 || \
       curl -s --connect-timeout 1 --max-time 2 http://169.254.169.254/metadata/v1/ >/dev/null 2>&1 || \
       curl -s --connect-timeout 1 --max-time 2 \
           -H "Metadata: true" \
           "http://169.254.169.254/metadata/instance?api-version=2021-02-01" >/dev/null 2>&1; then
        _cloud_env="detected"
    fi
    if [[ -n "$_cloud_env" ]]; then
        echo ""
        echo -e "  ${RED}╔══════════════════════════════════════════════════════════════════╗${NC}"
        echo -e "  ${RED}║       ✗  HA IS NOT SUPPORTED ON CLOUD / VPS DEPLOYMENTS         ║${NC}"
        echo -e "  ${RED}╠══════════════════════════════════════════════════════════════════╣${NC}"
        echo -e "  ${RED}║  HA clustering requires keepalived VRRP for VIP failover.       ║${NC}"
        echo -e "  ${RED}║  VRRP relies on broadcast/multicast MAC-based election which is  ║${NC}"
        echo -e "  ${RED}║  blocked by virtually all cloud hypervisors (AWS, GCP, Azure,   ║${NC}"
        echo -e "  ${RED}║  DigitalOcean, Vultr, etc.). Your VIP will NOT fail over.       ║${NC}"
        echo -e "  ${RED}║                                                                  ║${NC}"
        echo -e "  ${RED}║  Consequences if forced:                                         ║${NC}"
        echo -e "  ${RED}║  • VIP failover silently fails — miners stay offline             ║${NC}"
        echo -e "  ${RED}║  • etcd split-brain on cloud networking                         ║${NC}"
        echo -e "  ${RED}║  • Patroni may fail to promote due to fencing limitations        ║${NC}"
        echo -e "  ${RED}║  • False HA — false confidence with no real redundancy           ║${NC}"
        echo -e "  ${RED}╠══════════════════════════════════════════════════════════════════╣${NC}"
        echo -e "  ${RED}║  Use a managed database (RDS, Cloud SQL, etc.) instead.         ║${NC}"
        echo -e "  ${RED}╚══════════════════════════════════════════════════════════════════╝${NC}"
        echo ""
        exit 1
    fi

    # ── Validate VIP address ──
    if [[ -z "$vip_address" ]]; then
        log_error "--vip is required"
        echo "Example: sudo spiralctl ha enable --vip 192.168.1.100"
        exit 1
    fi
    if ! [[ "$vip_address" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
        log_error "Invalid IP address format: $vip_address"
        exit 1
    fi
    IFS='.' read -r _vip1 _vip2 _vip3 _vip4 <<< "$vip_address"
    if [[ "$_vip1" -gt 255 || "$_vip2" -gt 255 || "$_vip3" -gt 255 || "$_vip4" -gt 255 ]]; then
        log_error "Invalid IP address (octet out of range 0-255): $vip_address"
        exit 1
    fi

    # ── Auto-detect interface ──
    if [[ -z "$interface" ]]; then
        interface=$(detect_interface)
        if [[ -z "$interface" ]]; then
            log_error "Could not auto-detect network interface. Use --interface"
            exit 1
        fi
        log_info "Auto-detected interface: $interface"
    fi
    if ! ip link show "$interface" &>/dev/null; then
        log_error "Interface '$interface' does not exist"
        exit 1
    fi

    # ── Determine role ──
    local ha_mode="ha-master"
    local role_label="PRIMARY"
    if [[ "$priority" -gt 100 ]]; then
        ha_mode="ha-backup"
        role_label="BACKUP"
    else
        priority=100
    fi

    # ── Validate backup-specific requirements ──
    if [[ "$ha_mode" == "ha-backup" ]]; then
        if [[ -z "$primary_ip" ]]; then
            log_error "--primary-ip is required for backup nodes"
            exit 1
        fi
        if ! [[ "$primary_ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
            log_error "Invalid --primary-ip format: $primary_ip"
            exit 1
        fi
        IFS='.' read -r _pip1 _pip2 _pip3 _pip4 <<< "$primary_ip"
        if [[ "$_pip1" -gt 255 || "$_pip2" -gt 255 || "$_pip3" -gt 255 || "$_pip4" -gt 255 ]]; then
            log_error "Invalid --primary-ip (octet out of range 0-255): $primary_ip"
            exit 1
        fi
        if [[ -z "$repl_password" ]]; then
            log_error "--repl-password is required for backup nodes"
            exit 1
        fi
        if [[ -z "$superuser_password" ]]; then
            log_error "--superuser-password is required for backup nodes"
            exit 1
        fi
        if [[ -z "$token" ]]; then
            log_error "--token is required for backup nodes (use the token from the primary's setup output)"
            exit 1
        fi
    fi

    # ── Check current state ──
    detect_ha_state
    if [[ "$HA_STATE" == "ha-full" ]] && [[ "$force" != "true" ]]; then
        log_error "This node is already fully HA-configured (state: $HA_STATE)"
        echo "  Use --force to re-run on partially-configured state"
        exit 1
    fi
    if [[ "$HA_STATE" == "ha-partial" ]] && [[ "$force" != "true" ]]; then
        log_error "This node has partial HA configuration (state: $HA_STATE)"
        echo "  Use --force to continue, or run 'spiralctl ha disable' first to clean up"
        exit 1
    fi

    # On --force re-run, remove old HA config sections so ha_append_config_yaml
    # doesn't skip ("vip: already exists"). This ensures updated VIP/HA params are applied.
    if [[ "$force" == "true" ]] && [[ "$HA_STATE" != "standalone" ]]; then
        log_info "Force mode: removing old HA sections from config.yaml..."
        if ! ha_remove_config_yaml; then
            log_error "Could not remove old HA config sections from config.yaml"
            log_error "Stratum would start with stale VIP/HA settings."
            log_error "Manual fix: edit $CONFIG_FILE and delete everything from '# HIGH AVAILABILITY' to end of file"
            exit 1
        fi
    fi

    # ── Detect environment ──
    detect_server_ip
    if [[ -z "$SERVER_IP" ]]; then
        log_error "Could not detect this server's IP address"
        exit 1
    fi

    detect_pg_version
    if [[ -z "$PG_VERSION" ]]; then
        log_error "No PostgreSQL installation found under /var/lib/postgresql/"
        exit 1
    fi

    read_db_credentials
    if [[ -n "$db_password_flag" ]]; then
        DB_PASSWORD="$db_password_flag"
    fi
    if [[ -z "$DB_PASSWORD" ]]; then
        log_error "DB_PASSWORD is empty. Cannot configure Patroni without it."
        echo "  Either set database.password in config.yaml, or provide --db-password <pw>"
        exit 1
    fi

    # ── Generate/set credentials ──
    local repl_user="replicator"
    if [[ "$ha_mode" == "ha-master" ]]; then
        # Primary generates passwords
        [[ -z "$superuser_password" ]] && superuser_password=$(ha_generate_password)
        [[ -z "$repl_password" ]] && repl_password=$(ha_generate_password)
    fi

    # Validate generated credentials are non-empty (openssl/urandom failure)
    if [[ -z "$superuser_password" ]]; then
        log_error "Superuser password is empty — password generation failed"
        exit 1
    fi
    if [[ -z "$repl_password" ]]; then
        log_error "Replication password is empty — password generation failed"
        exit 1
    fi

    # ── Generate token if not provided ──
    if [[ -z "$token" ]]; then
        token=$(generate_cluster_token)
    fi
    if [[ -z "$token" ]]; then
        log_error "Cluster token is empty — token generation failed (is openssl installed?)"
        exit 1
    fi

    # ── Peer IP for firewall ──
    local peer_ip=""
    if [[ "$ha_mode" == "ha-master" ]]; then
        # Primary doesn't know backup IP yet (backup will --force or we skip)
        peer_ip=""
    else
        peer_ip="$primary_ip"
    fi

    # ── Confirmation banner ──
    echo ""
    echo -e "${YELLOW}╔═══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${YELLOW}║              ENABLING HIGH AVAILABILITY ($role_label)              ║${NC}"
    echo -e "${YELLOW}╚═══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "  VIP Address:    $vip_address/$netmask"
    echo "  Interface:      $interface"
    echo "  Role:           $role_label (priority $priority)"
    echo "  Node IP:        $SERVER_IP"
    echo "  PG Version:     $PG_VERSION"
    echo "  DB User:        $DB_USER"
    echo "  DB Name:        $DB_NAME"
    if [[ "$ha_mode" == "ha-backup" ]]; then
        echo "  Primary IP:     $primary_ip"
    fi
    echo ""

    local step_failed=""

    # ── Step 1/9: SSH key setup ──
    log_info "[1/9] Setting up SSH keys for HA replication..."
    if ! ha_setup_ssh "$ha_mode" "$POOL_USER" "$primary_ip" "$ssh_password"; then
        log_error "Failed at step 1 (SSH setup)"
        echo "  For backup nodes, provide --ssh-password <spiraluser-password-from-primary>"
        exit 1
    fi
    log_success "[1/9] SSH ready"

    # Backup: verify PG version matches primary's AFTER SSH keys are exchanged.
    # (Moved here from pre-SSH position — BatchMode=yes requires key auth.)
    if [[ "$ha_mode" == "ha-backup" ]] && [[ -n "$primary_ip" ]]; then
        local primary_pg_ver
        primary_pg_ver=$(sudo -u "$POOL_USER" ssh -o BatchMode=yes -o ConnectTimeout=10 \
            "${POOL_USER}@${primary_ip}" \
            "ls -d /var/lib/postgresql/*/main 2>/dev/null | sort -V | tail -1 | grep -oP '/var/lib/postgresql/\K[0-9]+'" 2>/dev/null || echo "")
        if [[ -n "$primary_pg_ver" ]] && [[ "$primary_pg_ver" != "$PG_VERSION" ]]; then
            log_error "PostgreSQL version mismatch: this node has PG $PG_VERSION, primary has PG $primary_pg_ver"
            log_error "Patroni pg_basebackup requires identical PG major versions"
            exit 1
        fi
        if [[ -n "$primary_pg_ver" ]]; then
            log_info "PG version match confirmed: $PG_VERSION (both nodes)"
        else
            log_warn "Could not verify primary's PG version via SSH"
        fi
    fi

    # ── Step 2/9: etcd ──
    # --force re-run: Stop Patroni BEFORE touching etcd. Patroni holds a session to
    # the etcd DCS — restarting etcd while Patroni is connected causes DCS disconnect,
    # which can trigger an unintended failover or leave Patroni in an error loop.
    if [[ "${force:-false}" == "true" ]] && systemctl is-active --quiet patroni 2>/dev/null; then
        log_info "Stopping Patroni before etcd reconfigure (--force)..."
        systemctl stop patroni 2>/dev/null || true
        # Wait for postgres to exit (Patroni-spawned)
        local pg_wait=15
        while [[ $pg_wait -gt 0 ]] && pgrep -x postgres &>/dev/null; do
            sleep 1
            pg_wait=$((pg_wait - 1))
        done
    fi
    log_info "[2/9] Installing and configuring etcd..."
    if ! ha_install_etcd; then
        step_failed="2 (etcd install)"
    elif ! ha_configure_etcd "$SERVER_IP" "$ha_mode" "$primary_ip"; then
        step_failed="2 (etcd configure)"
    fi
    if [[ -n "$step_failed" ]]; then
        log_error "Failed at step $step_failed"
        echo "  Fix the issue and re-run with --force, or run 'spiralctl ha disable' to clean up"
        echo "  Diagnostics: journalctl -u etcd -n 50"
        exit 1
    fi
    log_success "[2/9] etcd ready"

    # ── Step 3/9: Patroni install + configure ──
    log_info "[3/9] Installing and configuring Patroni..."
    if ! ha_install_patroni; then
        step_failed="3 (Patroni install)"
    elif ! ha_configure_patroni "$SERVER_IP" "$PG_VERSION" "$superuser_password" "$repl_user" "$repl_password" "$DB_USER" "$DB_PASSWORD" "$DB_NAME" "$peer_ip"; then
        step_failed="3 (Patroni configure)"
    elif ! ha_create_patroni_service; then
        step_failed="3 (Patroni service)"
    fi
    if [[ -n "$step_failed" ]]; then
        log_error "Failed at step $step_failed"
        echo "  Fix the issue and re-run with --force, or run 'spiralctl ha disable' to clean up"
        echo "  Diagnostics: journalctl -u patroni -n 50"
        exit 1
    fi
    log_success "[3/9] Patroni ready"

    # ── Step 4/9: Migrate PostgreSQL to Patroni ──
    log_info "[4/9] Migrating PostgreSQL to Patroni management..."
    if ! ha_migrate_to_patroni "$ha_mode" "$PG_VERSION" "$superuser_password" "$repl_user" "$repl_password"; then
        log_error "Failed at step 4 (PostgreSQL migration)"
        echo "  Diagnostics: journalctl -u patroni -n 50"
        echo "  To revert: systemctl stop patroni && systemctl enable postgresql && systemctl start postgresql"
        exit 1
    fi
    log_success "[4/9] PostgreSQL under Patroni management"

    # ── Step 5/9: Replication sudoers (primary only) ──
    if [[ "$ha_mode" == "ha-master" ]]; then
        log_info "[5/9] Creating replication sudoers..."
        if ! ha_setup_replication_sudoers "$POOL_USER"; then
            log_error "Failed at step 5 (replication sudoers)"
            echo "  Fix: manually create /etc/sudoers.d/spiralpool-ha-postgres"
            exit 1
        fi
    else
        log_info "[5/9] Skipping sudoers (backup node)"
    fi
    log_success "[5/9] Sudoers ready"

    # ── Step 6/9: Keepalived (VIP) ──
    log_info "[6/9] Configuring VIP failover (keepalived)..."
    if ! vip_enable_internal "$vip_address" "$interface" "$priority" "$token" "$netmask"; then
        log_error "Failed at step 6 (keepalived/VIP)"
        echo "  Diagnostics: journalctl -u keepalived -n 50"
        exit 1
    fi
    log_success "[6/9] Keepalived configured"

    # ── Step 7/9: config.yaml + firewall ──
    log_info "[7/9] Updating config.yaml and firewall..."
    if ! ha_append_config_yaml "$vip_address" "$interface" "$netmask" "$priority" "$token" "$ha_mode" "$SERVER_IP" "$primary_ip" "$repl_password"; then
        log_error "Failed at step 7 (config.yaml update)"
        exit 1
    fi
    if [[ -n "$peer_ip" ]]; then
        ha_configure_firewall "$peer_ip"
    elif [[ "$ha_mode" == "ha-master" ]] && command -v ufw &>/dev/null; then
        # PRIMARY: backup IP unknown at setup time. Open HA ports for local /24 subnet
        # so backup nodes can connect for etcd, Patroni, replication, VRRP, etc.
        local server_subnet
        server_subnet=$(echo "$SERVER_IP" | sed 's/\.[0-9]*$/.0/')
        if [[ -n "$server_subnet" ]]; then
            log_info "Opening HA ports for subnet ${server_subnet}/24..."
            ufw allow from "${server_subnet}/24" to any port 22 proto tcp > /dev/null 2>&1 || true
            ufw allow from "${server_subnet}/24" to any port 2379 proto tcp > /dev/null 2>&1 || true
            ufw allow from "${server_subnet}/24" to any port 2380 proto tcp > /dev/null 2>&1 || true
            ufw allow from "${server_subnet}/24" to any port 5432 proto tcp > /dev/null 2>&1 || true
            ufw allow from "${server_subnet}/24" to any port 5354 proto tcp > /dev/null 2>&1 || true
            ufw allow from "${server_subnet}/24" to any port 5363 proto udp > /dev/null 2>&1 || true
            ufw allow from "${server_subnet}/24" to any port 8008 proto tcp > /dev/null 2>&1 || true
            ufw allow proto vrrp from "${server_subnet}/24" > /dev/null 2>&1 || true
        fi
    fi
    log_success "[7/9] Configuration updated"

    # ── Step 8/9: HA scripts + watcher + markers ──
    log_info "[8/9] Deploying HA scripts, markers..."
    if ! ha_deploy_scripts; then
        log_error "Failed at step 8 (HA script deployment)"
        exit 1
    fi
    if ! ha_create_markers "$ha_mode"; then
        log_error "Failed at step 8 (HA marker creation)"
        exit 1
    fi
    log_success "[8/9] HA infrastructure deployed"

    # ── Step 9/9: Restart stratum + start watcher ──
    # Restart stratum BEFORE starting the watcher. The watcher monitors VIP state
    # via the stratum API — starting it before stratum has HA config leads to
    # incorrect role decisions during the brief startup window.
    log_info "[9/9] Restarting spiralstratum and starting watcher..."
    # BUG FIX (C2): Update stratum/health systemd service files to depend on patroni
    # instead of postgresql. Without this, systemd can't satisfy Requires=postgresql.service
    # (postgresql is disabled under Patroni), and stratum fails to start.
    for _ha_svc in spiralstratum spiralpool-health; do
        if [[ -f "/etc/systemd/system/${_ha_svc}.service" ]]; then
            sed -i 's/postgresql\.service/patroni.service/g' "/etc/systemd/system/${_ha_svc}.service"
        fi
    done
    systemctl daemon-reload 2>/dev/null || true
    systemctl restart spiralstratum 2>/dev/null || log_warn "spiralstratum restart failed (may not be running yet)"
    if ! ha_install_watcher "$INSTALL_DIR" "$POOL_USER"; then
        log_error "Failed at step 9 (watcher service)"
        echo "  Diagnostics: journalctl -u spiralpool-ha-watcher -n 50"
        exit 1
    fi
    log_success "[9/9] Stratum restarted, watcher running"

    # ── Success output ──
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║                    HA ENABLED SUCCESSFULLY                    ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════════════╝${NC}"
    echo ""

    if [[ "$ha_mode" == "ha-master" ]]; then
        echo -e "${WHITE}Credentials (save these — needed for backup nodes):${NC}"
        echo -e "  Superuser Password:    ${CYAN}$superuser_password${NC}"
        echo -e "  Replication Password:  ${CYAN}$repl_password${NC}"
        echo -e "  Cluster Token:         ${CYAN}$token${NC}"
        echo ""
        echo -e "${WHITE}To add a backup node, run this on the backup:${NC}"
        echo -e "  ${CYAN}sudo spiralctl ha enable \\"
        echo -e "    --vip $vip_address \\"
        [[ "$netmask" != "32" ]] && echo -e "    --netmask $netmask \\"
        echo -e "    --token $token \\"
        echo -e "    --priority 101 \\"
        echo -e "    --primary-ip $SERVER_IP \\"
        echo -e "    --superuser-password '$superuser_password' \\"
        echo -e "    --repl-password '$repl_password' \\"
        echo -e "    --ssh-password <spiraluser-system-password>${NC}"
        echo ""
        echo -e "${DIM}  For a third node, use --priority 102 (higher number = lower priority)${NC}"
        echo ""
        echo -e "${WHITE}If you need these credentials later, run on this node:${NC}"
        echo -e "  Superuser password:   ${CYAN}sudo awk '/superuser:/{f=1} f&&/password:/{gsub(/.*password:[[:space:]]*/,\"\"); gsub(/[\"]/,\"\"); print; exit}' /etc/patroni/patroni.yml${NC}"
        echo -e "  Replication password: ${CYAN}sudo grep SPIRAL_REPLICATION_PASSWORD /etc/spiralpool/ha.env${NC}"
        echo -e "  Cluster token:        ${CYAN}sudo cat /etc/keepalived/.cluster_token${NC}"
        echo -e "  spiraluser password:  ${CYAN}(set during install — check your records)${NC}"
        echo ""
    else
        echo -e "  Role:       BACKUP (priority $priority)"
        echo -e "  Primary:    $primary_ip"
        echo -e "  Patroni:    $(curl -sf "${PATRONI_CURL_AUTH[@]}" http://localhost:8008/ 2>/dev/null | grep -oP '"role"\s*:\s*"\K[^"]+' || echo 'initializing...')"
        echo ""
    fi

    echo -e "${WHITE}Miners should connect to VIP:${NC}"
    echo -e "  ${GREEN}$vip_address${NC} (use 'spiralctl status' to see stratum ports)"
    echo ""
}

# Internal function to configure keepalived (reuses vip_enable logic)
vip_enable_internal() {
    local vip_address="$1"
    local interface="$2"
    local priority="$3"
    local token="$4"
    local netmask="${5:-32}"

    # Determine keepalived priority based on HA mode
    # IMPORTANT: Keepalived uses HIGHER number = MORE likely to be MASTER.
    # Spiral Pool uses LOWER number = HIGHER priority (primary=100, backup=101+).
    # We must convert: keepalived_priority = 200 - spiral_priority
    # Primary (spiral 100) → keepalived 100, Backup (spiral 101) → keepalived 99
    #
    # ALL nodes use state BACKUP + nopreempt. This prevents the primary from
    # automatically reclaiming VIP on return (which causes VIP/DB split).
    local state="BACKUP"
    local keepalived_priority=$((200 - priority))
    # Ensure keepalived priority stays in valid range (1-254)
    [[ $keepalived_priority -lt 1 ]] && keepalived_priority=1
    [[ $keepalived_priority -gt 254 ]] && keepalived_priority=254
    if [[ "$priority" -le 100 ]]; then
        keepalived_priority=100
    fi

    # Install keepalived if not present
    if ! command -v keepalived &>/dev/null; then
        log_info "Installing keepalived..."
        apt-get update -qq > /dev/null 2>&1
        apt-get install -y keepalived > /dev/null 2>&1 || {
            log_error "Failed to install keepalived"
            return 1
        }
        if ! command -v keepalived &>/dev/null; then
            log_error "keepalived not found after install attempt"
            return 1
        fi
    fi

    # Sanitize hostname for keepalived router_id (alphanumeric + dash only)
    local hostname_short
    hostname_short=$(hostname -s 2>/dev/null | tr -dc 'A-Za-z0-9_-' || echo "spiralpool")
    [[ -z "$hostname_short" ]] && hostname_short="spiralpool"

    # Sanitize auth_pass to alphanumeric only — prevents keepalived config injection
    local auth_pass
    auth_pass=$(echo "${token:7:8}" | tr -dc 'A-Za-z0-9')
    if [[ -z "$auth_pass" ]]; then
        # Generate a random auth pass rather than using a hardcoded fallback
        auth_pass=$(tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 8)
        log_warn "Token too short — generated random keepalived auth password"
    fi

    # Derive virtual_router_id from cluster token (1-255 range).
    # Prevents VRRP ID conflicts when multiple Spiral Pool clusters share a L2 segment.
    local vrid=51
    if [[ -n "$token" ]]; then
        local _sum=0 _i
        for (( _i=0; _i<${#token}; _i++ )); do
            _sum=$(( _sum + $(printf '%d' "'${token:_i:1}") ))
        done
        vrid=$(( (_sum % 254) + 1 ))
    fi

    # Verify pgrep exists — its absolute path is baked into keepalived config.
    # If pgrep is missing (procps not installed), the health check becomes an invalid
    # empty command and keepalived never properly tracks stratum health.
    if ! command -v pgrep &>/dev/null; then
        log_info "Installing procps (pgrep required for keepalived health check)..."
        apt-get install -y -qq procps > /dev/null 2>&1 || true
        if ! command -v pgrep &>/dev/null; then
            log_error "pgrep not found (install procps package)"
            return 1
        fi
    fi

    mkdir -p /etc/keepalived

    cat > /etc/keepalived/keepalived.conf << EOF
# Spiral Pool HA Configuration
# Generated by spiralctl ha enable
# Token: [configured]

global_defs {
    router_id spiralpool_${hostname_short}
    script_user root
    enable_script_security
}

# Health check script - verify stratum server is running
# fall 5 = 10s of failures before reducing priority (prevents VIP flapping
# on transient crashes — systemd Restart=always recovers stratum in ~5s)
# rise 2 = 4s of success before restoring priority
vrrp_script check_stratum {
    script "$(command -v pgrep) -x spiralstratum"
    interval 2
    weight 2
    fall 5
    rise 2
}

vrrp_instance SPIRALPOOL_VIP {
    state $state
    interface $interface
    virtual_router_id $vrid
    priority $keepalived_priority
    advert_int 1
    nopreempt

    authentication {
        auth_type PASS
        auth_pass $auth_pass
    }

    virtual_ipaddress {
        $vip_address/$netmask dev $interface label spiralpool-vip
    }

    track_script {
        check_stratum
    }

    notify_master "/bin/logger -t keepalived 'SPIRALPOOL: Became MASTER for VIP $vip_address'"
    notify_backup "/bin/logger -t keepalived 'SPIRALPOOL: Became BACKUP for VIP $vip_address'"
    notify_fault  "/bin/logger -t keepalived 'SPIRALPOOL: FAULT state for VIP $vip_address'"
}
EOF

    # Restrict keepalived.conf permissions (contains auth_pass derived from cluster token)
    chmod 600 /etc/keepalived/keepalived.conf

    echo "$token" > /etc/keepalived/.cluster_token
    chmod 600 /etc/keepalived/.cluster_token

    systemctl enable keepalived >/dev/null 2>&1
    systemctl restart keepalived || {
        log_error "keepalived failed to start"
        log_error "Check: journalctl -u keepalived -n 30"
        return 1
    }

    sleep 2

    # Verify keepalived is actually running (restart can exit 0 but service dies immediately)
    if ! systemctl is-active --quiet keepalived 2>/dev/null; then
        log_error "keepalived is not running after restart"
        log_error "Check: journalctl -u keepalived -n 30"
        return 1
    fi

    # Flush routing cache — keepalived VIP changes can leave stale broadcast entries
    ip route flush cache 2>/dev/null || true

    if ip addr show "$interface" | grep -q " ${vip_address}/"; then
        log_success "VIP $vip_address is active (MASTER)"
    else
        if [[ "$priority" -le 100 ]]; then
            log_warn "VIP not assigned yet - stratum may not be running"
        else
            log_success "VIP configured (BACKUP mode)"
        fi
    fi
}

# Disable HA
# Usage: ha_disable [--yes]
ha_disable() {
    local skip_confirm=false
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --yes|-y) skip_confirm=true; shift ;;
            *) log_error "Unknown option: $1"; exit 1 ;;
        esac
    done

    # CRITICAL: Disable set -e for the entire teardown function.
    # A teardown MUST run all steps even if individual steps fail. With set -e,
    # a failure at step 3 (PG won't start) kills the script, leaving etcd,
    # keepalived, configs, and markers still in HA state — a half-disabled system
    # that is worse than either fully HA or fully standalone. We collect errors
    # and report them at the end instead.
    set +e
    local teardown_errors=0

    # ── Check current state ──
    detect_ha_state
    if [[ "$HA_STATE" == "standalone" ]]; then
        log_info "This node is already standalone (no HA components detected)"
        set -e
        return 0
    fi

    echo ""
    echo -e "${YELLOW}╔═══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${YELLOW}║                    DISABLING HIGH AVAILABILITY               ║${NC}"
    echo -e "${YELLOW}╚═══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    log_warn "This will stop all HA services, clean up HA configs, and revert to standalone."
    log_warn "Stratum will be restarted. Miners may experience a brief disconnect."
    echo ""

    if [[ "$skip_confirm" != "true" ]]; then
        echo -n "Are you sure you want to disable HA? [y/N] "
        local confirm
        read -r confirm
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
            log_info "Cancelled."
            set -e
            return 0
        fi
        echo ""
    fi

    detect_pg_version

    # ── Step 1/9: Stop HA role watcher ──
    log_info "[1/9] Stopping HA role watcher..."
    if systemctl is-active --quiet spiralpool-ha-watcher 2>/dev/null; then
        systemctl stop spiralpool-ha-watcher
    fi
    systemctl disable spiralpool-ha-watcher 2>/dev/null || true
    rm -f /etc/systemd/system/spiralpool-ha-watcher.service
    systemctl daemon-reload 2>/dev/null || true
    log_success "[1/9] Watcher stopped and removed"

    # ── Step 2/9: Stop keepalived, remove VIP, clean up config ──
    log_info "[2/9] Stopping keepalived and removing VIP..."
    # Extract VIP info before stopping
    local vip_cidr iface
    vip_cidr=$(grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+(?=\s+dev\s)' /etc/keepalived/keepalived.conf 2>/dev/null | head -1)
    iface=$(grep -oP 'interface\s+\K\S+' /etc/keepalived/keepalived.conf 2>/dev/null | head -1)

    if systemctl is-active --quiet keepalived 2>/dev/null; then
        systemctl stop keepalived
    fi
    systemctl disable keepalived 2>/dev/null || true

    # Remove VIP from interface
    if [[ -n "$vip_cidr" ]] && [[ -n "$iface" ]]; then
        ip addr del "$vip_cidr" dev "$iface" 2>/dev/null || true
        log_info "VIP $vip_cidr removed from $iface"
    fi
    ip route flush cache 2>/dev/null || true

    # Clean up keepalived config
    rm -f /etc/keepalived/keepalived.conf
    rm -f /etc/keepalived/.cluster_token
    log_success "[2/9] Keepalived stopped, VIP removed, config cleaned"

    # ── Step 3/9: Revert PostgreSQL from Patroni to standalone ──
    # Stop stratum BEFORE PG revert — stratum holds active DB connections that delay
    # postgres shutdown (waits for backends) and reconnects with stale HA config after
    # PG restarts. Stratum is restarted in step 9 with clean (non-HA) config.
    if systemctl is-active --quiet spiralstratum 2>/dev/null; then
        log_info "Stopping spiralstratum before PG revert..."
        systemctl stop spiralstratum 2>/dev/null || true
    fi
    log_info "[3/9] Reverting PostgreSQL to standalone mode..."
    if [[ -n "$PG_VERSION" ]]; then
        if ha_revert_postgresql "$PG_VERSION"; then
            log_success "[3/9] PostgreSQL reverted to standalone"
        else
            log_error "[3/9] PostgreSQL revert had errors — continuing teardown"
            teardown_errors=$((teardown_errors + 1))
        fi
    else
        # Fallback: PG version unknown. Try common paths for cleanup.
        log_warn "PG version not detected — attempting best-effort cleanup"
        systemctl stop patroni 2>/dev/null || true
        systemctl disable patroni 2>/dev/null || true
        # Try to find and clean postgresql.auto.conf in any PG data dir
        for pgdir in /var/lib/postgresql/*/main; do
            if [[ -d "$pgdir" ]]; then
                [[ -f "$pgdir/standby.signal" ]] && rm -f "$pgdir/standby.signal"
                if [[ -f "$pgdir/postgresql.auto.conf" ]]; then
                    cp "$pgdir/postgresql.auto.conf" "$pgdir/postgresql.auto.conf.ha-backup.$(date +%Y%m%d%H%M%S)" 2>/dev/null || true
                    echo "# postgresql.auto.conf — cleared by spiralctl ha disable on $(date)" > "$pgdir/postgresql.auto.conf"
                    chown postgres:postgres "$pgdir/postgresql.auto.conf" 2>/dev/null || true
                fi
            fi
        done
        # Remove conf.d/spiral-ha.conf and clean pg_hba.conf from any PG version
        for confdir in /etc/postgresql/*/main; do
            [[ -f "$confdir/conf.d/spiral-ha.conf" ]] && rm -f "$confdir/conf.d/spiral-ha.conf"
            # Clean pg_hba.conf (same logic as ha_revert_postgresql)
            local pg_hba="$confdir/pg_hba.conf"
            if [[ -f "$pg_hba" ]]; then
                sed -i '/^host.*replication.*scram-sha-256/d' "$pg_hba" 2>/dev/null || true
                sed -i '/^host[[:space:]]\+all[[:space:]]\+all[[:space:]]\+[0-9]/d' "$pg_hba" 2>/dev/null || true
                if ! grep -q "^local.*all.*all.*peer" "$pg_hba" 2>/dev/null; then
                    sed -i '1i local all all peer' "$pg_hba" 2>/dev/null || true
                fi
                if ! grep -q "^host.*all.*all.*127.0.0.1/32" "$pg_hba" 2>/dev/null; then
                    echo "host all all 127.0.0.1/32 scram-sha-256" >> "$pg_hba" 2>/dev/null || true
                fi
            fi
        done
        systemctl enable postgresql 2>/dev/null || true
        systemctl start postgresql 2>/dev/null || log_warn "Could not start standalone PostgreSQL"
        if systemctl is-active --quiet postgresql 2>/dev/null; then
            # Best-effort: drop orphaned replication slots (same logic as ha_revert_postgresql)
            local slots
            slots=$(sudo -u postgres psql -tAc "SELECT slot_name FROM pg_replication_slots;" 2>/dev/null || echo "")
            if [[ -n "$slots" ]]; then
                log_info "Dropping orphaned replication slots..."
                while IFS= read -r slot; do
                    [[ -z "$slot" ]] && continue
                    [[ ! "$slot" =~ ^[a-z0-9_]+$ ]] && continue
                    sudo -u postgres psql -qc "SELECT pg_drop_replication_slot('${slot}');" 2>/dev/null || true
                done <<< "$slots"
            fi
            # Drop orphaned replication role
            if sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'replicator';" 2>/dev/null | grep -q 1; then
                sudo -u postgres psql -qc "DROP ROLE IF EXISTS \"replicator\";" 2>/dev/null || true
            fi
            log_success "[3/9] PostgreSQL reverted (best-effort, PG version unknown)"
        else
            log_error "[3/9] PostgreSQL revert incomplete — PG not running"
            teardown_errors=$((teardown_errors + 1))
        fi
    fi

    # ── Step 4/9: Remove Patroni config and service ──
    log_info "[4/9] Removing Patroni configuration..."
    rm -f /etc/systemd/system/patroni.service
    rm -rf /etc/patroni/
    rm -f /usr/local/bin/patroni /usr/local/bin/patronictl
    systemctl daemon-reload 2>/dev/null || true
    log_success "[4/9] Patroni config and symlinks removed"

    # ── Step 5/9: Stop etcd and clean up ──
    log_info "[5/9] Stopping etcd and cleaning up..."

    # Try to remove this node from the etcd cluster gracefully (helps the peer)
    local my_ip
    my_ip=$(ip route get 8.8.8.8 2>/dev/null | grep -oP 'src \K\S+' | head -1)
    if [[ -n "$my_ip" ]] && ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 | grep -q "is healthy"; then
        # Find peer endpoints (any member that isn't us)
        local peer_endpoint=""
        local member_list
        member_list=$(ETCDCTL_API=3 etcdctl --command-timeout=5s member list --write-out=simple 2>/dev/null || echo "")
        if [[ -n "$member_list" ]]; then
            # Use -F for fixed-string match with port suffix to prevent IP substring matches
            # (e.g., 192.168.1.10 matching 192.168.1.100). IPs in etcd output are always IP:port.
            peer_endpoint=$(echo "$member_list" | grep -vF "${my_ip}:" | grep -oP 'http://[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:2379' | head -1 || echo "")
            if [[ -n "$peer_endpoint" ]]; then
                # Remove ourselves from the cluster via the peer so it stays healthy
                local my_member_id
                my_member_id=$(echo "$member_list" | grep -F "${my_ip}:" | awk -F', ' '{print $1}' | tr -d '[:space:]')
                if [[ -n "$my_member_id" ]]; then
                    log_info "Removing this node from etcd cluster via peer..."
                    if ETCDCTL_API=3 etcdctl --command-timeout=10s --endpoints="$peer_endpoint" \
                        member remove "$my_member_id" 2>/dev/null; then
                        log_info "Removed from etcd cluster"
                    else
                        log_error "Could not remove this node from etcd cluster"
                        log_error "Peer's etcd may lose quorum. On the peer, run: sudo etcdctl member remove $my_member_id"
                        teardown_errors=$((teardown_errors + 1))
                    fi
                fi
            fi
        fi
    fi

    if systemctl is-active --quiet etcd 2>/dev/null; then
        systemctl stop etcd
    fi
    systemctl disable etcd 2>/dev/null || true

    # Clean up etcd data and config
    rm -rf /var/lib/etcd/member
    rm -f /etc/default/etcd
    log_success "[5/9] etcd stopped and cleaned"

    # ── Step 6/9: Clean up peer nodes (remote) ──
    log_info "[6/9] Cleaning up peer node references..."
    # Find peer IPs from the etcd member list we captured earlier, or from config.yaml
    local peer_ips=""
    if [[ -n "${member_list:-}" ]] && [[ -n "$my_ip" ]]; then
        # Use "${my_ip}:" with colon suffix to prevent IP substring matches in etcd output
        # (e.g., 192.168.1.10 matching 192.168.1.100). etcd always outputs IP:port format.
        peer_ips=$(echo "$member_list" | grep -vF "${my_ip}:" | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | sort -u || echo "")
    fi
    # Fallback: read primary IP from config.yaml ha: section
    if [[ -z "$peer_ips" ]] && [[ -f "$CONFIG_FILE" ]]; then
        local cfg_primary
        cfg_primary=$(awk '/^ha:/{found=1} found && /primaryHost:/{gsub(/.*primaryHost:[[:space:]]*/, ""); gsub(/["'"'"']/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null)
        if [[ -n "$cfg_primary" ]] && [[ "$cfg_primary" != "127.0.0.1" ]] && [[ "$cfg_primary" != "${my_ip:-}" ]]; then
            peer_ips="$cfg_primary"
        fi
    fi

    local has_peer_cleanup=false
    if [[ -n "$peer_ips" ]]; then
        local my_hostname_short
        my_hostname_short=$(hostname -s 2>/dev/null || echo "")

        while IFS= read -r pip; do
            [[ -z "$pip" ]] && continue
            log_info "Cleaning up peer $pip..."
            has_peer_cleanup=true

            # Try to reach peer via SSH (spiraluser has no sudo for ufw, so firewall
            # cleanup must be done manually by root on the peer)
            if sudo -u "$POOL_USER" ssh -o BatchMode=yes -o ConnectTimeout=5 \
                "${POOL_USER}@${pip}" "echo 'SSH OK'" &>/dev/null 2>&1; then

                # Notify the peer that this node is leaving. There is no Patroni REST API
                # endpoint for removing members — the member auto-expires from the DCS
                # after the TTL (~30s). We do two things that help:
                # 1. The etcd member removal in step 5 already cleaned the DCS
                # 2. Ask the peer's Patroni to reload, which picks up the DCS change
                sudo -u "$POOL_USER" ssh -o BatchMode=yes -o ConnectTimeout=10 \
                    "${POOL_USER}@${pip}" \
                    'PCAUTH=(); [[ -f /spiralpool/config/patroni-api.conf ]] && source /spiralpool/config/patroni-api.conf && [[ -n "${PATRONI_API_USERNAME:-}" ]] && PCAUTH=(-u "${PATRONI_API_USERNAME}:${PATRONI_API_PASSWORD}"); curl -sf -X POST "${PCAUTH[@]}" http://localhost:8008/reload 2>/dev/null' \
                    2>/dev/null || true
                log_info "Sent Patroni reload to peer $pip (member will auto-expire from DCS)"
            else
                log_warn "Cannot SSH to peer $pip"
            fi
        done <<< "$peer_ips"
    else
        log_info "No peer IPs found — skipping remote cleanup"
    fi

    # Always print manual peer firewall cleanup instructions (spiraluser can't sudo ufw)
    if [[ "$has_peer_cleanup" == "true" ]] && [[ -n "$my_ip" ]]; then
        echo ""
        log_warn "Peer firewall cleanup requires root on each peer node."
        echo "  Run these commands on each remaining HA peer:"
        echo ""
        echo "  sudo ufw delete allow from $my_ip to any port 5363 proto udp"
        echo "  sudo ufw delete allow from $my_ip to any port 5354 proto tcp"
        echo "  sudo ufw delete allow from $my_ip to any port 5432 proto tcp"
        echo "  sudo ufw delete allow from $my_ip to any port 2379 proto tcp"
        echo "  sudo ufw delete allow from $my_ip to any port 2380 proto tcp"
        echo "  sudo ufw delete allow from $my_ip to any port 8008 proto tcp"
        echo "  sudo ufw delete allow from $my_ip to any port 22 proto tcp"
        echo "  sudo ufw delete allow proto vrrp from $my_ip"
        echo ""
    fi
    log_success "[6/9] Peer cleanup done"

    # ── Step 7/9: Remove HA sections from config.yaml ──
    log_info "[7/9] Removing HA configuration from config.yaml..."
    if ha_remove_config_yaml; then
        log_success "[7/9] Config cleaned"
    else
        log_error "[7/9] Config cleanup failed — HA sections may remain in config.yaml"
        log_error "Stratum may still enable VIP management. Manually remove vip: and ha: sections."
        teardown_errors=$((teardown_errors + 1))
    fi

    # ── Step 8/9: Remove markers and HA env ──
    log_info "[8/9] Removing HA markers and environment files..."
    rm -f "$INSTALL_DIR/config/ha-enabled"
    rm -f "$INSTALL_DIR/config/ha-mode"
    rm -f "$INSTALL_DIR/config/ha.yaml"
    rm -f /etc/spiralpool/ha.env
    # Clean up HA service-control state files (stale state causes wrong role transitions
    # if HA is re-enabled later on the same machine)
    rm -f "$INSTALL_DIR/config/.ha-service-state"
    rm -f "$INSTALL_DIR/config/.ha-service-control.lock"
    rm -f "$INSTALL_DIR/config/.ha-watcher-state"

    # Remove HA replication sudoers
    if [[ -f /etc/sudoers.d/spiralpool-ha-postgres ]]; then
        rm -f /etc/sudoers.d/spiralpool-ha-postgres
        log_info "HA replication sudoers removed"
    fi

    # Remove HA script symlinks created by ha_deploy_scripts
    for link in spiralpool-ha-service spiralpool-etcd-recover spiralpool-patroni-bootstrap spiralpool-ha-validate spiralpool-ha-replicate spiralpool-ha-setup-ssh; do
        rm -f "/usr/local/bin/$link"
    done

    # Remove local firewall rules for HA ports (if this node had rules for peers)
    if command -v ufw &>/dev/null && [[ -n "$peer_ips" ]]; then
        while IFS= read -r pip; do
            [[ -z "$pip" ]] && continue
            ufw delete allow from "$pip" to any port 22 proto tcp 2>/dev/null || true
            ufw delete allow from "$pip" to any port 5363 proto udp 2>/dev/null || true
            ufw delete allow from "$pip" to any port 5354 proto tcp 2>/dev/null || true
            ufw delete allow from "$pip" to any port 5432 proto tcp 2>/dev/null || true
            ufw delete allow from "$pip" to any port 2379 proto tcp 2>/dev/null || true
            ufw delete allow from "$pip" to any port 2380 proto tcp 2>/dev/null || true
            ufw delete allow from "$pip" to any port 8008 proto tcp 2>/dev/null || true
            ufw delete allow proto vrrp from "$pip" 2>/dev/null || true
        done <<< "$peer_ips"
        log_info "Local HA firewall rules removed"
    fi
    log_success "[8/9] Markers, env, and firewall cleaned"

    # ── Step 9/9: Restart stratum and ensure all services running ──
    log_info "[9/9] Restarting spiralstratum and ensuring all services are running..."
    # BUG FIX (C3): Revert stratum/health systemd service files from patroni back to
    # postgresql. Without this, Requires=patroni.service references a removed unit,
    # and stratum fails to start.
    for _ha_svc in spiralstratum spiralpool-health; do
        if [[ -f "/etc/systemd/system/${_ha_svc}.service" ]]; then
            sed -i 's/patroni\.service/postgresql.service/g' "/etc/systemd/system/${_ha_svc}.service"
        fi
    done
    systemctl daemon-reload 2>/dev/null || true
    if ! systemctl restart spiralstratum; then
        log_warn "spiralstratum restart failed — check: journalctl -u spiralstratum -n 20"
        teardown_errors=$((teardown_errors + 1))
    fi
    # Sentinel and Dashboard may have been stopped by ha-service-control demote (backup nodes).
    # As standalone, all services should run. Only start them if they're enabled (installed).
    if systemctl is-enabled --quiet spiralsentinel 2>/dev/null; then
        if ! systemctl is-active --quiet spiralsentinel 2>/dev/null; then
            if systemctl start spiralsentinel 2>/dev/null; then
                log_info "Sentinel started"
            else
                log_warn "spiralsentinel start failed"
                teardown_errors=$((teardown_errors + 1))
            fi
        fi
    fi
    if systemctl is-enabled --quiet spiraldash 2>/dev/null; then
        if ! systemctl is-active --quiet spiraldash 2>/dev/null; then
            if systemctl start spiraldash 2>/dev/null; then
                log_info "Dashboard started"
            else
                log_warn "spiraldash start failed"
                teardown_errors=$((teardown_errors + 1))
            fi
        fi
    fi
    log_success "[9/9] Services restarted"

    # Restore set -e for the rest of the script
    set -e

    echo ""
    if [[ $teardown_errors -gt 0 ]]; then
        echo -e "${YELLOW}╔═══════════════════════════════════════════════════════════════╗${NC}"
        echo -e "${YELLOW}║              HA DISABLED WITH ${teardown_errors} ERROR(S)                       ║${NC}"
        echo -e "${YELLOW}╚═══════════════════════════════════════════════════════════════╝${NC}"
        echo ""
        echo "  Some steps had errors. Review the output above."
        echo "  The teardown continued through all steps despite errors."
        echo "  You may need to manually verify PostgreSQL is running:"
        echo "    sudo systemctl status postgresql"
        echo "    sudo journalctl -u postgresql -n 50"
    else
        echo -e "${GREEN}╔═══════════════════════════════════════════════════════════════╗${NC}"
        echo -e "${GREEN}║                   HA DISABLED SUCCESSFULLY                    ║${NC}"
        echo -e "${GREEN}╚═══════════════════════════════════════════════════════════════╝${NC}"
    fi
    echo ""
    echo "  This node is now a fully standalone server."
    echo "  PostgreSQL: standalone mode"
    echo "  etcd: stopped, data wiped, removed from cluster"
    echo "  Patroni: config removed, service removed"
    echo "  Keepalived: stopped, config removed, VIP released"
    echo "  Firewall: HA peer rules removed (local + remote)"
    echo "  Config: HA sections removed from config.yaml"
    echo ""
    echo -e "${DIM}  SSH keys in /home/${POOL_USER}/.ssh/ preserved (harmless).${NC}"
    echo -e "${DIM}  Patroni venv at /opt/patroni/ preserved (rm -rf /opt/patroni to remove).${NC}"
    echo ""
}

#===============================================================================
# COIN COMMAND
#===============================================================================

cmd_coin() {
    local action="${1:-status}"
    local coin="${2:-}"

    case "$action" in
        status|list)
            echo ""
            echo -e "${WHITE}ENABLED COINS${NC}"
            echo -e "─────────────────────────────────────────────────────────────────────────"

            # Alphabetically ordered
            for c in BC2 BCH BTC CAT DGB DGB-SCRYPT DOGE FBTC LTC NMC PEP QBX SYS XMY; do
                daemon=$(get_coin_daemon $c)
                if systemctl is-enabled --quiet "$daemon" 2>/dev/null; then
                    if systemctl is-active --quiet "$daemon" 2>/dev/null; then
                        echo -e "  $c                       ${GREEN}● Enabled & Running${NC}"
                    else
                        echo -e "  $c                       ${YELLOW}● Enabled (stopped)${NC}"
                    fi
                else
                    echo -e "  $c                       ${DIM}○ Disabled${NC}"
                fi
            done
            echo ""
            echo -e "${DIM}Use 'spiralctl coin enable <coin>' to add a supported coin${NC}"
            echo -e "${DIM}Use 'spiralctl coin disable <coin>' to disable a coin${NC}"
            echo -e "${DIM}Use 'spiralctl mining' to switch mining modes (solo/multi/merge)${NC}"
            echo ""
            ;;
        enable)
            # Enable a supported coin — launches the installer in "add coin" mode
            check_root
            if [[ -z "$coin" ]]; then
                echo ""
                echo -e "${WHITE}USAGE${NC}"
                echo "    spiralctl coin enable <SYMBOL>"
                echo ""
                echo -e "${WHITE}SUPPORTED COINS${NC}"
                echo "    SHA-256d: BC2, BCH, BTC, DGB, FBTC, NMC, QBX, SYS, XMY"
                echo "    Scrypt:   CAT, DGB-SCRYPT, DOGE, LTC, PEP"
                echo ""
                echo -e "${WHITE}EXAMPLES${NC}"
                echo "    spiralctl coin enable BTC       Enable Bitcoin"
                echo "    spiralctl coin enable LTC       Enable Litecoin"
                echo "    spiralctl coin enable NMC       Enable Namecoin (merge-mine with BTC)"
                echo ""
                echo -e "${DIM}This launches the installer in 'Add coins to existing installation' mode.${NC}"
                echo -e "${DIM}Wallet generation, config, firewall, and services are handled automatically.${NC}"
                echo ""
                return 0
            fi
            coin="${coin^^}"

            # Validate it's a supported coin
            local -A SUPPORTED_COINS=(
                [BC2]=1 [BCH]=1 [BTC]=1 [CAT]=1 [DGB]=1 [DGB-SCRYPT]=1 [DOGE]=1
                [FBTC]=1 [LTC]=1 [NMC]=1 [PEP]=1 [QBX]=1 [SYS]=1 [XMY]=1
            )
            if [[ -z "${SUPPORTED_COINS[$coin]+x}" ]]; then
                log_error "Unknown coin: ${coin}"
                echo ""
                echo -e "  ${DIM}Supported: BC2, BCH, BTC, CAT, DGB, DGB-SCRYPT, DOGE, FBTC, LTC, NMC, PEP, QBX, SYS, XMY${NC}"
                echo -e "  ${DIM}For unsupported/custom coins, use: spiralctl add-coin <SYMBOL> --github <URL>${NC}"
                exit 1
            fi

            # SYS is merge-mining only — requires BTC parent chain
            if [[ "$coin" == "SYS" ]]; then
                local btc_daemon
                btc_daemon=$(get_coin_daemon "BTC" 2>/dev/null || echo "")
                if [[ -z "$btc_daemon" ]] || ! systemctl is-enabled --quiet "$btc_daemon" 2>/dev/null; then
                    echo ""
                    log_error "SYS (Syscoin) is a merge-mining-only coin."
                    echo -e "  ${DIM}SYS requires BTC to be enabled first as the parent chain.${NC}"
                    echo -e "  ${DIM}Enable BTC first:  ${NC}${CYAN}sudo spiralctl coin enable BTC${NC}"
                    echo -e "  ${DIM}Then enable SYS:   ${NC}${CYAN}sudo spiralctl coin enable SYS${NC}"
                    echo ""
                    exit 1
                fi
                echo ""
                log_info "SYS is a merge-mining coin. It will be mined alongside BTC."
            fi

            # Check if already enabled
            local daemon
            daemon=$(get_coin_daemon "$coin" 2>/dev/null || echo "")
            if [[ -n "$daemon" ]] && systemctl is-enabled --quiet "$daemon" 2>/dev/null; then
                echo ""
                log_info "${coin} is already enabled."
                if ! systemctl is-active --quiet "$daemon" 2>/dev/null; then
                    echo -e "  ${DIM}Daemon is stopped. Start it with: ${NC}${CYAN}sudo systemctl start ${daemon}${NC}"
                fi
                echo ""
                return 0
            fi

            echo ""
            echo -e "${WHITE}  Enabling ${coin}...${NC}"
            echo ""
            echo -e "  This will launch the Spiral Pool installer to add ${coin}."
            echo -e "  It will install the daemon, generate a wallet address, update"
            echo -e "  config.yaml, open firewall ports, and restart services."
            echo ""
            prompt_input "Continue? [Y/n]: "
            read -r answer
            if [[ "${answer,,}" == "n" ]]; then
                log_info "Cancelled."
                return 0
            fi

            local installer=""
            for candidate in \
                "${INSTALL_DIR}/install.sh" \
                "$HOME/Spiral-Pool/install.sh" \
                "/home/*/Spiral-Pool/install.sh"; do
                # shellcheck disable=SC2086
                for f in $candidate; do
                    if [[ -f "$f" ]]; then installer="$f"; break 2; fi
                done
            done
            if [[ -z "$installer" ]]; then
                log_error "Installer not found."
                echo -e "  ${DIM}Expected at: ${INSTALL_DIR}/install.sh${NC}"
                echo -e "  ${DIM}Copy install.sh to ${INSTALL_DIR}/ or re-clone from: https://github.com/SpiralPool/Spiral-Pool${NC}"
                exit 1
            fi
            exec sudo bash "$installer"
            ;;
        disable)
            check_root
            if [[ -z "$coin" ]]; then
                log_error "Usage: spiralctl coin disable <bc2|bch|btc|cat|dgb|dgb-scrypt|doge|fbtc|ltc|nmc|pep|qbx|sys|xmy>"
                exit 1
            fi
            coin="${coin^^}"
            # DGB-SCRYPT shares the DGB daemon - cannot stop it without killing DGB too
            if [[ "$coin" == "DGB-SCRYPT" ]]; then
                log_error "DGB-SCRYPT shares the DigiByte daemon with DGB."
                log_info "To disable DGB-SCRYPT stratum, remove the DGB-SCRYPT entry from config.yaml and restart spiralstratum."
                exit 1
            fi
            daemon=$(get_coin_daemon $coin)
            if [[ -z "$daemon" ]]; then
                log_error "Unknown coin: $coin. Use bc2, bch, btc, cat, dgb, dgb-scrypt, doge, fbtc, ltc, nmc, pep, qbx, sys, or xmy."
                exit 1
            fi
            log_warn "Disabling $coin..."
            systemctl stop "$daemon"
            systemctl disable "$daemon"
            log_success "$coin disabled."
            ;;
        prune)
            check_root
            if [[ -z "$coin" ]]; then
                echo ""
                echo -e "${WHITE}USAGE${NC}"
                echo "    spiralctl coin prune <SYMBOL>"
                echo ""
                echo -e "${WHITE}DESCRIPTION${NC}"
                echo "    Enable blockchain pruning for a coin to save disk space."
                echo "    Sets prune=5000 (5 GB) in the daemon config and restarts."
                echo "    All pool operations (mining, ZMQ, block submission) work on pruned nodes."
                echo ""
                echo -e "${WHITE}SAVINGS${NC}"
                echo "    BTC: ~600 GB → 5 GB    DGB: ~60 GB → 5 GB"
                echo "    BCH: ~200 GB → 5 GB    LTC: ~100 GB → 5 GB"
                echo ""
                echo -e "${YELLOW}⚠  This requires a full resync — blockchain data will be re-downloaded.${NC}"
                return 0
            fi
            coin="${coin^^}"
            daemon=$(get_coin_daemon "$coin")
            if [[ -z "$daemon" ]]; then
                log_error "Unknown coin: $coin"
                exit 1
            fi

            # Resolve config file path from the CLI command
            local coin_lower="${coin,,}"
            # Handle DGB-SCRYPT → dgb
            [[ "$coin_lower" == "dgb-scrypt" ]] && coin_lower="dgb"

            local conf_name=""
            case "$coin" in
                DGB|DGB-SCRYPT) conf_name="digibyte.conf" ;;
                BTC) conf_name="bitcoin.conf" ;;
                BCH) conf_name="bitcoin.conf" ;;
                BC2) conf_name="bitcoinii.conf" ;;
                FBTC) conf_name="fractal.conf" ;;
                LTC) conf_name="litecoin.conf" ;;
                QBX) conf_name="qbitx.conf" ;;
                DOGE) conf_name="dogecoin.conf" ;;
                NMC) conf_name="namecoin.conf" ;;
                PEP) conf_name="pepecoin.conf" ;;
                CAT) conf_name="catcoin.conf" ;;
                SYS) conf_name="syscoin.conf" ;;
                XMY) conf_name="myriadcoin.conf" ;;
                *) log_error "No config mapping for $coin"; exit 1 ;;
            esac

            local conf_file="$(_chain_dir "$coin_lower")/${conf_name}"
            if [[ ! -f "$conf_file" ]]; then
                log_error "Config file not found: $conf_file"
                exit 1
            fi

            # Check if already pruned (prune=0 means NOT pruned — full node default)
            if grep -qE "^prune=[1-9]" "$conf_file" 2>/dev/null; then
                local current_prune
                current_prune=$(grep "^prune=" "$conf_file" | head -1 | cut -d= -f2)
                log_info "$coin is already configured with prune=${current_prune}"
                return 0
            fi

            # Check for txindex (incompatible with pruning)
            if grep -q "^txindex=1" "$conf_file" 2>/dev/null; then
                log_warn "Removing txindex=1 (incompatible with pruning)"
                sed -i 's/^txindex=1/#txindex=1  # disabled for pruning/' "$conf_file"
            fi

            echo ""
            log_warn "This will enable pruning for $coin and restart the daemon."
            log_warn "The blockchain data may need to resync from scratch."
            echo ""
            prompt_input "Continue? (y/N) "
            read -r confirm
            if [[ "${confirm,,}" != "y" ]]; then
                log_info "Cancelled."
                return 0
            fi

            # Add prune=5000 to config
            echo "" >> "$conf_file"
            echo "# Pruning enabled by spiralctl (saves 95%+ disk)" >> "$conf_file"
            echo "prune=5000" >> "$conf_file"

            # Restart the daemon
            log_info "Restarting $daemon..."
            systemctl restart "$daemon"
            log_success "$coin pruning enabled (prune=5000, ~5 GB). Daemon restarted."
            echo ""
            echo -e "  ${DIM}Monitor sync progress: spiralctl sync${NC}"
            echo ""
            ;;
        *)
            echo "Usage: spiralctl coin [status|list|enable|disable|prune] <coin>"
            echo ""
            echo "  spiralctl coin status               Show all coins and their state"
            echo "  spiralctl coin enable <SYMBOL>      Add a supported coin to the pool"
            echo "  spiralctl coin disable <SYMBOL>     Stop and disable a coin daemon"
            echo "  spiralctl coin prune <SYMBOL>       Enable blockchain pruning (~95% disk savings)"
            echo ""
            echo "For mining mode changes, use 'spiralctl mining':"
            echo "  spiralctl mining solo <coin>        Switch to solo mining"
            echo "  spiralctl mining multi <coins>      Switch to multi-coin mining"
            echo "  spiralctl mining merge enable       Enable merge mining"
            exit 1
            ;;
    esac
}

#===============================================================================
# SYNC COMMAND (Blockchain sync status)
#===============================================================================

cmd_sync() {
    local args=("$@")

    if ! command -v spiralpool-sync &>/dev/null; then
        log_error "spiralpool-sync not found. Was the installer run completely?"
        exit 1
    fi

    # Detect the pool user from systemd service files (most reliable - configured during install)
    local pool_user=""

    # Check systemd service files for the configured User=
    # Checks all supported coins (alphabetically): BC2, BCH, BTC, CAT, DGB, DOGE, FBTC, LTC, NMC, PEP, QBX, SYS, XMY + stratum
    for service in bitcoiniid bitcoind-bch bitcoind catcoind digibyted dogecoind fractald litecoind namecoind pepecoind qbitxd syscoind myriadcoind spiralstratum; do
        if [[ -f "/etc/systemd/system/${service}.service" ]]; then
            pool_user=$(grep -oP '^User=\K.*' "/etc/systemd/system/${service}.service" 2>/dev/null | head -1)
            [[ -n "$pool_user" ]] && break
        fi
    done

    # Fallback: detect from directory ownership (checks all coin directories)
    if [[ -z "$pool_user" ]] || [[ "$pool_user" == "root" ]]; then
        for dir in "$INSTALL_DIR/dgb" "$INSTALL_DIR/btc" "$INSTALL_DIR/bch" "$INSTALL_DIR/bc2" "$INSTALL_DIR/fbtc" "$INSTALL_DIR/ltc" "$INSTALL_DIR/nmc" "$INSTALL_DIR/doge" "$INSTALL_DIR/pep" "$INSTALL_DIR/qbx" "$INSTALL_DIR/sys" "$INSTALL_DIR/xmy" "$INSTALL_DIR/cat"; do
            if [[ -d "$dir" ]]; then
                pool_user=$(stat -c '%U' "$dir" 2>/dev/null)
                [[ -n "$pool_user" ]] && [[ "$pool_user" != "root" ]] && break
            fi
        done
    fi

    # Security: Validate pool_user is a valid Unix username (alphanumeric, underscore, hyphen)
    # This prevents command injection via malicious usernames
    if [[ -n "$pool_user" ]] && [[ ! "$pool_user" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
        log_error "Invalid pool user detected: $pool_user"
        exit 1
    fi

    # Security: Verify user actually exists before attempting sudo
    if [[ -n "$pool_user" ]] && ! id "$pool_user" &>/dev/null; then
        log_warn "Pool user '$pool_user' not found, running as current user"
        pool_user=""
    fi

    # If we found a pool user and we're not already that user, switch to them
    if [[ -n "$pool_user" ]] && [[ "$(whoami)" != "$pool_user" ]]; then
        exec sudo -u "$pool_user" spiralpool-sync "${args[@]}"
    fi

    # Already running as the correct user or couldn't detect pool user
    exec spiralpool-sync "${args[@]}"
}

#===============================================================================
# DELEGATED COMMANDS
# These subcommands delegate to existing spiralpool-* scripts via exec.
# The underlying scripts remain unchanged — spiralctl is a unified entry point.
#===============================================================================

cmd_logs()        { as_pool_user /usr/local/bin/spiralpool-logs "$@"; }
cmd_restart_all() { check_root; exec /usr/local/bin/spiralpool-restart "$@"; }
cmd_wallet()      { as_pool_user /usr/local/bin/spiralpool-wallet "$@"; }
cmd_backup()      { check_root; exec /usr/local/bin/spiralpool-backup "$@"; }
cmd_restore_pool(){ check_root; exec /usr/local/bin/spiralpool-restore "$@"; }
cmd_pause()       { as_pool_user /usr/local/bin/spiralpool-pause "$@"; }
cmd_stats() {
    if [[ "${1:-}" == "blocks" ]]; then
        shift
        as_pool_user /usr/local/bin/spiralpool-blocks "$@"
    fi
    as_pool_user /usr/local/bin/spiralpool-stats "$@"
}
cmd_blocks()      { as_pool_user /usr/local/bin/spiralpool-blocks "$@"; }
cmd_test()        { as_pool_user /usr/local/bin/spiralpool-test "$@"; }
cmd_scan()        { as_pool_user /usr/local/bin/spiralpool-scan "$@"; }
cmd_watch()       { as_pool_user /usr/local/bin/spiralpool-watch "$@"; }
cmd_export()      { as_pool_user /usr/local/bin/spiralpool-export "$@"; }
cmd_update()      { as_pool_user /usr/local/bin/spiralpool-update "$@"; }
cmd_maintenance() { as_pool_user /usr/local/bin/spiralpool-maintenance "$@"; }

#===============================================================================
# SHUTDOWN COMMAND
#===============================================================================

cmd_shutdown() {
    check_root

    local action="shutdown"  # "shutdown" or "reboot"
    local force=0

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --reboot|-r)   action="reboot"; shift ;;
            --yes|-y)      force=1; shift ;;
            *)
                log_error "Unknown option: $1"
                echo "Usage: spiralctl shutdown [--reboot] [--yes]"
                echo "  --reboot, -r    Reboot instead of power off"
                echo "  --yes, -y       Skip confirmation prompt"
                exit 1
                ;;
        esac
    done

    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    if [[ "$action" == "reboot" ]]; then
        echo -e "  ${WHITE}SPIRAL POOL — GRACEFUL REBOOT${NC}"
    else
        echo -e "  ${WHITE}SPIRAL POOL — GRACEFUL SHUTDOWN${NC}"
    fi
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  Services will be stopped in order:"
    echo -e "    1. ${DIM}spiralstratum${NC}   (drops miner connections cleanly)"
    echo -e "    2. ${DIM}spiralsentinel${NC}  (flush monitoring state)"
    echo -e "    3. ${DIM}spiraldash${NC}      (dashboard)"
    if systemctl is-enabled --quiet keepalived 2>/dev/null; then
        echo -e "    4. ${DIM}keepalived${NC}     (release VIP)"
        echo -e "    5. ${DIM}patroni${NC}        (flush PostgreSQL)"
        echo -e "    6. ${DIM}etcd${NC}           (HA consensus)"
    else
        echo -e "    4. ${DIM}patroni${NC}        (flush PostgreSQL)"
    fi
    echo ""

    if [[ $force -eq 0 ]]; then
        if [[ "$action" == "reboot" ]]; then
            echo -ne "  ${YELLOW}Reboot this machine now? [y/N]${NC} "
        else
            echo -ne "  ${YELLOW}Shut down this machine now? [y/N]${NC} "
        fi
        local answer
        read -r answer
        echo ""
        if [[ ! "$answer" =~ ^[yY]$ ]]; then
            log_info "Cancelled."
            exit 0
        fi
    fi

    # ── Step 1: Stratum ──
    if systemctl is-active --quiet spiralstratum 2>/dev/null; then
        log_info "Stopping spiralstratum (miners will disconnect)..."
        systemctl stop spiralstratum
        log_success "spiralstratum stopped"
    else
        echo -e "  ${DIM}spiralstratum already stopped${NC}"
    fi

    # ── Step 2: Sentinel ──
    if systemctl is-active --quiet spiralsentinel 2>/dev/null; then
        log_info "Stopping spiralsentinel..."
        systemctl stop spiralsentinel
        log_success "spiralsentinel stopped"
    else
        echo -e "  ${DIM}spiralsentinel already stopped${NC}"
    fi

    # ── Step 3: Dashboard ──
    if systemctl is-active --quiet spiraldash 2>/dev/null; then
        log_info "Stopping spiraldash..."
        systemctl stop spiraldash
        log_success "spiraldash stopped"
    else
        echo -e "  ${DIM}spiraldash already stopped${NC}"
    fi

    # ── Step 4: HA stack (keepalived → Patroni → etcd), if present ──
    if systemctl is-enabled --quiet keepalived 2>/dev/null; then
        if systemctl is-active --quiet keepalived 2>/dev/null; then
            log_info "Stopping keepalived (releasing VIP)..."
            systemctl stop keepalived
            log_success "keepalived stopped"
        fi
    fi

    if systemctl is-active --quiet patroni 2>/dev/null; then
        log_info "Stopping patroni (flushing PostgreSQL)..."
        systemctl stop patroni
        log_success "patroni stopped"
    elif systemctl is-active --quiet postgresql 2>/dev/null; then
        log_info "Stopping postgresql..."
        systemctl stop postgresql
        log_success "postgresql stopped"
    fi

    if systemctl is-active --quiet etcd 2>/dev/null; then
        log_info "Stopping etcd..."
        systemctl stop etcd
        log_success "etcd stopped"
    fi

    echo ""
    echo -e "${CYAN}────────────────────────────────────────────────────────────────────────${NC}"

    if [[ "$action" == "reboot" ]]; then
        log_success "All services stopped — rebooting now..."
        echo ""
        systemctl reboot
    else
        log_success "All services stopped — shutting down now..."
        echo ""
        systemctl poweroff
    fi
}

#===============================================================================
# DATA COMMAND (backup / restore / export — consolidated)
#===============================================================================

cmd_data() {
    local action="${1:-}"
    shift || true

    case "$action" in
        backup)  check_root; exec /usr/local/bin/spiralpool-backup "$@" ;;
        restore) check_root; exec /usr/local/bin/spiralpool-restore "$@" ;;
        export)  as_pool_user /usr/local/bin/spiralpool-export "$@" ;;
        *)
            echo "Usage: spiralctl data [backup|restore|export]"
            echo ""
            echo "  backup    Backup pool data and configuration (requires root)"
            echo "  restore   Restore pool data from backup (requires root)"
            echo "  export    Export mining history to CSV"
            exit 1
            ;;
    esac
}

#===============================================================================
# CHAIN COMMAND (Blockchain export/restore)
#===============================================================================

cmd_chain() {
    local action="${1:-}"
    case "$action" in
        export)  check_root; shift; exec /spiralpool/scripts/blockchain-export.sh "$@" ;;
        restore) check_root; shift; exec /spiralpool/scripts/blockchain-restore.sh "$@" ;;
        *)
            echo "Usage: spiralctl chain [export|restore]"
            echo ""
            echo "Commands:"
            echo "  export    Push blockchain data TO a remote machine (you're on the synced box)"
            echo "  restore   Pull blockchain data FROM a remote machine (you're on the new box)"
            echo ""
            echo "Pick ONE — you don't need both. Each is a complete operation."
            echo "Coins are transferred one at a time: daemon stop → rsync → daemon restart."
            echo ""
            echo "You will be prompted for:"
            echo "  - Remote IP address"
            echo "  - SSH user on remote (your admin login, NOT spiraluser)"
            echo "  - Which coins to transfer"
            echo ""
            echo "Examples:"
            echo "  sudo spiralctl chain export     # Push from here to remote"
            echo "  sudo spiralctl chain restore    # Pull from remote to here"
            exit 1
            ;;
    esac
}

#===============================================================================
# NODE COMMAND
#===============================================================================

cmd_node() {
    local action="${1:-status}"
    local coin="${2:-all}"

    get_daemons() {
        if [[ "$1" == "all" ]]; then
            echo "digibyted bitcoind bitcoind-bch bitcoiniid litecoind dogecoind pepecoind catcoind fractald qbitxd namecoind syscoind myriadcoind"
        else
            get_coin_daemon "${1^^}"
        fi
    }

    case "$action" in
        status)
            cmd_status
            ;;
        start)
            check_root
            for daemon in $(get_daemons "$coin"); do
                if systemctl is-enabled --quiet "$daemon" 2>/dev/null; then
                    log_info "Starting $daemon..."
                    systemctl start "$daemon"
                    log_success "$daemon started."
                fi
            done
            ;;
        stop)
            check_root
            if [[ "${coin^^}" == "DGB-SCRYPT" ]]; then
                log_error "DGB-SCRYPT shares the DigiByte daemon with DGB. Stopping it would also stop DGB mining."
                log_info "Use 'spiralctl node stop dgb' to stop both, or update config.yaml to disable DGB-SCRYPT stratum."
                exit 1
            fi
            for daemon in $(get_daemons "$coin"); do
                if systemctl is-active --quiet "$daemon" 2>/dev/null; then
                    log_info "Stopping $daemon..."
                    systemctl stop "$daemon"
                    log_success "$daemon stopped."
                fi
            done
            ;;
        restart)
            check_root
            if [[ "${coin^^}" == "DGB-SCRYPT" ]]; then
                log_error "DGB-SCRYPT shares the DigiByte daemon with DGB. Restarting it would also restart DGB mining."
                log_info "Use 'spiralctl node restart dgb' to restart both."
                exit 1
            fi
            for daemon in $(get_daemons "$coin"); do
                if systemctl is-enabled --quiet "$daemon" 2>/dev/null; then
                    log_info "Restarting $daemon..."
                    systemctl restart "$daemon"
                    log_success "$daemon restarted."
                fi
            done
            ;;
        *)
            echo "Usage: spiralctl node [status|start|stop|restart] [bc2|bch|btc|cat|dgb|dgb-scrypt|doge|fbtc|ltc|nmc|pep|qbx|sys|xmy|all]"
            exit 1
            ;;
    esac
}

#===============================================================================
# CONFIG COMMAND
#===============================================================================

cmd_config() {
    local action="${1:-}"
    local key="${2:-}"
    local value="${3:-}"

    local pool_home
    pool_home=$(getent passwd "$POOL_USER" 2>/dev/null | cut -d: -f6)
    pool_home="${pool_home:-/home/$POOL_USER}"
    local SENTINEL_CONFIG="${pool_home}/.spiralsentinel/config.json"

    case "$action" in
        validate)
            cmd_config_validate
            return
            ;;
        notify-test)
            cmd_config_notify_test
            return
            ;;
        list-cooldowns)
            # Query the Sentinel health endpoint for active alert cooldowns.
            # Read the configured port from sentinel config.json (falls back to 9191).
            local health_port
            health_port=$(python3 - << 'PYEOF' 2>/dev/null
import json, sys
paths = [
    f"/home/spiraluser/.spiralsentinel/config.json",
    "/spiralpool/config/sentinel/config.json",
]
for p in paths:
    try:
        cfg = json.load(open(p))
        print(cfg.get("sentinel_health_port", 9191))
        sys.exit(0)
    except Exception:
        pass
print(9191)
PYEOF
)
            health_port="${health_port:-9191}"
            local result
            result=$(curl -sf --max-time 5 "http://127.0.0.1:${health_port}/cooldowns" 2>/dev/null || echo "")
            if [[ -z "$result" ]]; then
                log_error "Sentinel health endpoint unavailable — is spiralsentinel running? (port ${health_port})"
                exit 1
            fi
            local count
            count=$(echo "$result" | jq 'length' 2>/dev/null || echo 0)
            echo ""
            echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
            echo -e "  ${WHITE}ACTIVE ALERT COOLDOWNS${NC}"
            echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
            if [[ "$count" == "0" ]]; then
                echo ""
                echo -e "  ${GREEN}✓ No active cooldowns — all alert types ready to fire${NC}"
            else
                printf "  %-32s %12s %12s  %s\n" "ALERT TYPE" "EXPIRES IN" "COOLDOWN" "LAST SENT"
                echo "  ────────────────────────────────────────────────────────────────────────"
                echo "$result" | jq -r '.[] | [.type, .expires_in_s, .cooldown_s, .last_sent] | @tsv' 2>/dev/null \
                | while IFS=$'\t' read -r atype exp_s cool_s last; do
                    local exp_str
                    local exp_i; exp_i=$((exp_s + 0))
                    if   (( exp_i >= 3600 )); then exp_str=$(printf "%dh %dm" $((exp_i / 3600)) $(((exp_i % 3600) / 60)))
                    elif (( exp_i >= 60   )); then exp_str=$(printf "%dm %ds"  $((exp_i / 60))  $((exp_i % 60)))
                    else                           exp_str=$(printf "%ds"      "$exp_i")
                    fi
                    local cool_str; cool_i=$((cool_s + 0))
                    if   (( cool_i >= 3600 )); then cool_str=$(printf "%dh"  $((cool_i / 3600)))
                    elif (( cool_i >= 60   )); then cool_str=$(printf "%dm"  $((cool_i / 60)))
                    else                           cool_str=$(printf "%ds"   "$cool_i")
                    fi
                    printf "  ${YELLOW}%-32s${NC} %12s %12s  %s\n" "$atype" "$exp_str" "$cool_str" "$last"
                done
            fi
            echo ""
            return
            ;;
        get)
            if [[ -z "$key" ]]; then
                echo "Usage: spiralctl config get <key>"
                echo ""
                echo "Available keys:"
                echo "  expected_hashrate    Expected fleet hashrate in TH/s"
                echo "  discord_webhook      Discord webhook URL"
                echo "  telegram_token       Telegram bot token"
                echo "  telegram_chat_id     Telegram chat ID"
                exit 1
            fi

            if [[ ! -f "$SENTINEL_CONFIG" ]]; then
                log_error "Config file not found: $SENTINEL_CONFIG"
                exit 1
            fi

            case "$key" in
                expected_hashrate|expected_fleet_ths)
                    local val=$(grep -oP '"expected_fleet_ths"\s*:\s*\K[0-9.]+' "$SENTINEL_CONFIG" 2>/dev/null)
                    echo "${val:-22} TH/s"
                    ;;
                discord_webhook|discord_webhook_url)
                    local val=$(grep -oP '"discord_webhook_url"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
                    echo "${val:-(not set)}"
                    ;;
                telegram_token|telegram_bot_token)
                    local val=$(grep -oP '"telegram_bot_token"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
                    if [[ -n "$val" ]]; then
                        echo "${val:0:10}...(hidden)"
                    else
                        echo "(not set)"
                    fi
                    ;;
                telegram_chat_id)
                    local val=$(grep -oP '"telegram_chat_id"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
                    echo "${val:-(not set)}"
                    ;;
                *)
                    log_error "Unknown key: $key"
                    exit 1
                    ;;
            esac
            ;;
        set)
            if [[ -z "$key" ]] || [[ -z "$value" ]]; then
                echo "Usage: spiralctl config set <key> <value>"
                echo ""
                echo "Available keys:"
                echo "  expected_hashrate <TH/s>    Expected fleet hashrate (e.g., 22)"
                echo "  discord_webhook <url>       Discord webhook URL"
                echo "  telegram_token <token>      Telegram bot token"
                echo "  telegram_chat_id <id>       Telegram chat ID"
                exit 1
            fi

            if [[ ! -f "$SENTINEL_CONFIG" ]]; then
                log_error "Config file not found: $SENTINEL_CONFIG"
                echo "Run the installer first to create the config file."
                exit 1
            fi

            # Create backup
            if ! cp "$SENTINEL_CONFIG" "${SENTINEL_CONFIG}.bak"; then
                log_error "Failed to backup config file"
                exit 1
            fi

            case "$key" in
                expected_hashrate|expected_fleet_ths)
                    # Validate numeric
                    if ! [[ "$value" =~ ^[0-9]+\.?[0-9]*$ ]]; then
                        log_error "Invalid value: $value (must be a number)"
                        exit 1
                    fi
                    # Use Python to update JSON safely (paths via sys.argv to prevent injection)
                    if ! python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['expected_fleet_ths'] = float(sys.argv[2])
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG" "$value"; then
                        log_error "Failed to update config"
                        mv "${SENTINEL_CONFIG}.bak" "$SENTINEL_CONFIG"
                        exit 1
                    fi
                    log_success "Expected hashrate set to $value TH/s"
                    echo ""
                    echo "Restart Sentinel to apply: sudo systemctl restart spiralsentinel"
                    ;;
                discord_webhook|discord_webhook_url)
                    if ! python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['discord_webhook_url'] = sys.argv[2]
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG" "$value"; then
                        log_error "Failed to update config"
                        mv "${SENTINEL_CONFIG}.bak" "$SENTINEL_CONFIG"
                        exit 1
                    fi
                    log_success "Discord webhook URL updated"
                    echo ""
                    echo "Restart Sentinel to apply: sudo systemctl restart spiralsentinel"
                    ;;
                telegram_token|telegram_bot_token)
                    if ! python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['telegram_bot_token'] = sys.argv[2]
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG" "$value"; then
                        log_error "Failed to update config"
                        mv "${SENTINEL_CONFIG}.bak" "$SENTINEL_CONFIG"
                        exit 1
                    fi
                    log_success "Telegram bot token updated"
                    echo ""
                    echo "Restart Sentinel to apply: sudo systemctl restart spiralsentinel"
                    ;;
                telegram_chat_id)
                    if ! python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['telegram_chat_id'] = sys.argv[2]
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG" "$value"; then
                        log_error "Failed to update config"
                        mv "${SENTINEL_CONFIG}.bak" "$SENTINEL_CONFIG"
                        exit 1
                    fi
                    log_success "Telegram chat ID updated"
                    echo ""
                    echo "Restart Sentinel to apply: sudo systemctl restart spiralsentinel"
                    ;;
                *)
                    log_error "Unknown key: $key"
                    rm -f "${SENTINEL_CONFIG}.bak"
                    exit 1
                    ;;
            esac
            ;;
        show|list)
            echo ""
            echo -e "${WHITE}SENTINEL CONFIGURATION${NC}"
            echo -e "─────────────────────────────────────────────────────────────────────────"
            if [[ -f "$SENTINEL_CONFIG" ]]; then
                echo -e "  Config file:       $SENTINEL_CONFIG"
                echo ""
                local hashrate=$(grep -oP '"expected_fleet_ths"\s*:\s*\K[0-9.]+' "$SENTINEL_CONFIG" 2>/dev/null)
                echo -e "  Expected Hashrate: ${GREEN}${hashrate:-22} TH/s${NC}"
                local discord=$(grep -oP '"discord_webhook_url"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
                if [[ -n "$discord" ]]; then
                    echo -e "  Discord Webhook:   ${GREEN}Configured${NC}"
                else
                    echo -e "  Discord Webhook:   ${DIM}Not set${NC}"
                fi
                local telegram=$(grep -oP '"telegram_bot_token"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
                if [[ -n "$telegram" ]]; then
                    echo -e "  Telegram:          ${GREEN}Configured${NC}"
                else
                    echo -e "  Telegram:          ${DIM}Not set${NC}"
                fi
                echo ""
                echo -e "  ${DIM}For detailed webhook config: spiralctl webhook status${NC}"
            else
                echo -e "  ${YELLOW}Config file not found${NC}"
                echo "  Run the installer to create configuration."
            fi
            echo ""
            ;;
        *)
            echo "Usage: spiralctl config <command> [key] [value]"
            echo ""
            echo "Commands:"
            echo "  show / list               Show current configuration"
            echo "  get <key>                 Get a config value"
            echo "  set <key> <value>         Set a config value"
            echo ""
            echo "Keys:"
            echo "  expected_hashrate         Expected fleet hashrate in TH/s"
            echo "  discord_webhook           Discord webhook URL"
            echo "  telegram_token            Telegram bot token"
            echo "  telegram_chat_id          Telegram chat ID"
            echo ""
            echo "Examples:"
            echo "  spiralctl config show"
            echo "  spiralctl config get expected_hashrate"
            echo "  spiralctl config set expected_hashrate 50"
            exit 1
            ;;
    esac
}

#===============================================================================
# SECURITY COMMAND — unified security status view
#===============================================================================

cmd_security() {
    # Route fail2ban and tor as subcommands
    case "${1:-}" in
        fail2ban) shift; cmd_fail2ban "$@"; return ;;
        tor)      shift; cmd_tor "$@"; return ;;
    esac

    local period="${1:-24h}"

    local NC='\033[0m'    CYAN='\033[0;36m' WHITE='\033[1;37m'
    local GREEN='\033[0;32m' YELLOW='\033[1;33m' RED='\033[0;31m' DIM='\033[2m'

    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${WHITE}  SECURITY STATUS${NC}  ${DIM}(last $period)${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════${NC}"
    echo ""

    # ── Firewall ──────────────────────────────────────────────────────────────
    echo -e "  ${WHITE}Firewall${NC}"

    local ufw_status
    ufw_status=$(sudo ufw status 2>/dev/null | head -1)
    if echo "$ufw_status" | grep -qi "active"; then
        echo -e "    UFW:       ${GREEN}ACTIVE${NC}  (default: deny incoming)"
    else
        echo -e "    UFW:       ${RED}INACTIVE${NC}  ${YELLOW}⚠  pool is unprotected${NC}"
    fi

    if command -v fail2ban-client &>/dev/null && systemctl is-active --quiet fail2ban 2>/dev/null; then
        echo -e "    fail2ban:  ${GREEN}ACTIVE${NC}  (72h ban on brute-force / IP violation)"
    elif command -v fail2ban-client &>/dev/null; then
        echo -e "    fail2ban:  ${YELLOW}INSTALLED but not running${NC}"
    else
        echo -e "    fail2ban:  ${RED}NOT INSTALLED${NC}"
    fi
    echo ""

    # ── fail2ban jail status ──────────────────────────────────────────────────
    if command -v fail2ban-client &>/dev/null && systemctl is-active --quiet fail2ban 2>/dev/null; then
        echo -e "  ${WHITE}Active Bans${NC}"
        printf "    %-28s %s\n" "Jail" "Total / Currently banned"
        echo -e "    ${DIM}──────────────────────────────────────────────────────${NC}"

        local any_banned=false
        declare -A jail_banned_ips

        for jail in spiralpool-dashboard spiralpool-api spiralpool-stratum; do
            local raw
            raw=$(sudo fail2ban-client status "$jail" 2>/dev/null)
            local total currently
            total=$(echo "$raw" | grep "Total banned:" | awk '{print $NF}')
            currently=$(echo "$raw" | grep "Currently banned:" | awk '{print $NF}')
            local banned_list
            banned_list=$(echo "$raw" | grep "Banned IP list:" | sed 's/.*Banned IP list://' | xargs)

            total="${total:-0}"; currently="${currently:-0}"
            [[ -n "$banned_list" ]] && jail_banned_ips[$jail]="$banned_list" && any_banned=true

            local cur_str
            if [[ "$currently" -gt 0 ]]; then
                cur_str="${RED}${currently}${NC}"
            else
                cur_str="${currently}"
            fi
            printf "    %-28s %s / %b\n" "$jail" "$total" "$cur_str"
        done
        echo ""

        if $any_banned; then
            echo -e "  ${WHITE}Currently Banned IPs${NC}"
            for jail in "${!jail_banned_ips[@]}"; do
                for ip in ${jail_banned_ips[$jail]}; do
                    echo -e "    ${RED}${ip}${NC}  ${DIM}[${jail}]${NC}"
                done
            done
            echo ""
        fi
    fi

    # ── Stratum event counts from journald ────────────────────────────────────
    echo -e "  ${WHITE}Stratum Events  ${DIM}(last $period)${NC}"
    echo -e "    ${DIM}──────────────────────────────────────────────────────${NC}"

    local stratum_journal
    stratum_journal=$(journalctl -u spiralstratum --since "$period ago" --no-pager 2>/dev/null)

    local ip_bans violations preauth_floods msg_too_large conn_refused
    ip_bans=$(echo "$stratum_journal" | grep -c '"IP banned"\|"IP auto-banned due to violations"' 2>/dev/null || echo 0)
    violations=$(echo "$stratum_journal" | grep -c '"Rate limit violation"' 2>/dev/null || echo 0)
    preauth_floods=$(echo "$stratum_journal" | grep -c '"Pre-auth message limit exceeded"' 2>/dev/null || echo 0)
    msg_too_large=$(echo "$stratum_journal" | grep -c '"Message too large"' 2>/dev/null || echo 0)
    conn_refused=$(echo "$stratum_journal" | grep -c '"Connection rate limited"\|"Global partial buffer limit exceeded"' 2>/dev/null || echo 0)

    _sec_row() { printf "    %-32s %s\n" "$1" "$2"; }

    if [[ "$ip_bans" -gt 0 ]]; then
        _sec_row "IP bans (rate limiter):" "${RED}${ip_bans}${NC}"
    else
        _sec_row "IP bans (rate limiter):" "${ip_bans}"
    fi
    if [[ "$violations" -gt 0 ]]; then
        _sec_row "Rate limit violations:" "${YELLOW}${violations}${NC}"
    else
        _sec_row "Rate limit violations:" "${violations}"
    fi
    _sec_row "Pre-auth floods:" "${preauth_floods}"
    _sec_row "Oversized messages:" "${msg_too_large}"
    _sec_row "Connections refused:" "${conn_refused}"
    echo ""

    # ── Connection type breakdown ─────────────────────────────────────────────
    echo -e "  ${WHITE}Connection Fingerprints  ${DIM}(last $period)${NC}"
    echo -e "    ${DIM}──────────────────────────────────────────────────────${NC}"

    local classified_lines
    classified_lines=$(echo "$stratum_journal" | grep '"Connection classified"')

    local n_asic n_proxy n_market n_unknown
    n_asic=$(echo "$classified_lines" | grep -c '"type":"ASIC"' 2>/dev/null || echo 0)
    n_proxy=$(echo "$classified_lines" | grep -c '"type":"PROXY"' 2>/dev/null || echo 0)
    n_market=$(echo "$classified_lines" | grep -c '"type":"MARKETPLACE"' 2>/dev/null || echo 0)
    n_unknown=$(echo "$classified_lines" | grep -c '"type":"UNKNOWN"' 2>/dev/null || echo 0)
    local total_classified=$(( n_asic + n_proxy + n_market + n_unknown ))

    if [[ "$total_classified" -eq 0 ]]; then
        echo -e "    ${DIM}No classified connections in this window${NC}"
    else
        _sec_row "ASIC:"        "${n_asic}"
        _sec_row "PROXY:"       "${n_proxy}"
        _sec_row "MARKETPLACE:" "${n_market}"
        _sec_row "UNKNOWN:"     "${n_unknown}"
    fi
    echo ""

    # ── Recent security events ────────────────────────────────────────────────
    echo -e "  ${WHITE}Recent Security Events${NC}"
    echo -e "    ${DIM}──────────────────────────────────────────────────────${NC}"

    local stratum_events dash_events combined_events
    # Stratum: IP bans, violations, pre-auth floods
    stratum_events=$(echo "$stratum_journal" \
        | grep '"IP banned"\|"IP auto-banned\|"Rate limit violation"\|"Pre-auth message limit exceeded"' \
        | tail -8 \
        | sed 's/^[A-Za-z]* [0-9]* //' \
        | awk '{ts=$1; $1=""; printf "    [stratum]   %-12s %s\n", ts, $0}')

    # Dashboard: failed logins
    dash_events=$(journalctl -u spiraldash --since "$period ago" --no-pager 2>/dev/null \
        | grep "SECURITY:" \
        | tail -5 \
        | sed 's/^[A-Za-z]* [0-9]* //' \
        | awk '{ts=$1; $1=""; printf "    [dashboard] %-12s %s\n", ts, $0}')

    combined_events=$(printf '%s\n%s' "$stratum_events" "$dash_events" \
        | grep -v '^$' | tail -12)

    if [[ -z "$combined_events" ]]; then
        echo -e "    ${DIM}None in this window — clean${NC}"
    else
        echo "$combined_events" | while IFS= read -r line; do
            if echo "$line" | grep -qE '"IP banned"|"IP auto-banned"'; then
                echo -e "    ${RED}${line#    }${NC}"
            elif echo "$line" | grep -qE 'Failed login|Rate limited login'; then
                echo -e "    ${YELLOW}${line#    }${NC}"
            else
                echo -e "    ${line#    }"
            fi
        done
    fi
    echo ""

    # ── Whitelist reminder ────────────────────────────────────────────────────
    local whitelist
    whitelist=$(grep "^ignoreip" /etc/fail2ban/jail.d/spiralpool.conf 2>/dev/null \
        | sed 's/ignoreip\s*=\s*//' | tr ' ' '\n' \
        | grep -v '^127\.\|^::1\|^10\.\|^172\.1[6-9]\.\|^172\.2[0-9]\.\|^172\.3[0-1]\.\|^192\.168\.' \
        | grep -v '^$')
    if [[ -n "$whitelist" ]]; then
        echo -e "  ${WHITE}Marketplace Whitelist${NC}"
        echo "$whitelist" | while read -r cidr; do
            echo -e "    ${GREEN}${cidr}${NC}"
        done
        echo ""
    fi

    echo -e "  ${DIM}spiralctl security fail2ban unban <IP>   spiralctl security fail2ban whitelist-add <CIDR>${NC}"
    echo ""
}

#===============================================================================
# FAIL2BAN COMMAND
#===============================================================================

cmd_fail2ban() {
    local action="${1:-status}"
    shift || true

    local JAIL_CONF="/etc/fail2ban/jail.d/spiralpool.conf"

    case "$action" in
        status)
            echo ""
            echo "fail2ban jail status:"
            echo ""
            if ! command -v fail2ban-client &>/dev/null; then
                log_error "fail2ban is not installed"
                exit 1
            fi
            for jail in spiralpool-dashboard spiralpool-api spiralpool-stratum; do
                echo "  ── $jail ──────────────────────────────────────────"
                sudo fail2ban-client status "$jail" 2>/dev/null || echo "  (jail not loaded)"
                echo ""
            done
            ;;

        banned)
            # Show currently banned IPs across all Spiral Pool jails
            if ! command -v fail2ban-client &>/dev/null; then
                log_error "fail2ban is not installed"
                exit 1
            fi
            echo ""
            echo "Currently banned IPs:"
            echo ""
            local found=false
            for jail in spiralpool-dashboard spiralpool-api spiralpool-stratum; do
                local banned
                banned=$(sudo fail2ban-client status "$jail" 2>/dev/null \
                    | grep "Banned IP list:" | sed 's/.*Banned IP list://' | xargs)
                if [[ -n "$banned" ]]; then
                    echo "  [$jail]  $banned"
                    found=true
                fi
            done
            $found || echo "  (none)"
            echo ""
            ;;

        unban)
            # Usage: spiralctl fail2ban unban <IP>
            local ip="${1:-}"
            if [[ -z "$ip" ]]; then
                log_error "Usage: spiralctl fail2ban unban <IP>"
                exit 1
            fi
            echo "Unbanning $ip from all Spiral Pool jails..."
            for jail in spiralpool-dashboard spiralpool-api spiralpool-stratum; do
                sudo fail2ban-client set "$jail" unbanip "$ip" 2>/dev/null && \
                    echo "  ✓ $jail" || true
            done
            echo ""
            ;;

        whitelist-add)
            # Usage: spiralctl fail2ban whitelist-add <CIDR> [comment]
            # Adds a CIDR to the [DEFAULT] ignoreip in the jail config so that
            # IP range is never banned.  Useful for hashrate marketplace CIDRs.
            local cidr="${1:-}"
            local comment="${2:-}"
            if [[ -z "$cidr" ]]; then
                log_error "Usage: spiralctl fail2ban whitelist-add <CIDR> [comment]"
                log_error "Example: spiralctl fail2ban whitelist-add 5.9.0.0/16 'NiceHash'"
                exit 1
            fi
            if [[ ! -f "$JAIL_CONF" ]]; then
                log_error "Jail config not found: $JAIL_CONF"
                exit 1
            fi
            # Append CIDR to the ignoreip line
            local comment_str=""
            [[ -n "$comment" ]] && comment_str=" # $comment"
            sudo sed -i "s|^ignoreip\s*=.*|& $cidr|" "$JAIL_CONF"
            echo "# whitelisted by spiralctl$([ -n "$comment_str" ] && echo ":$comment_str")" | \
                sudo tee -a "$JAIL_CONF" > /dev/null
            sudo systemctl reload fail2ban 2>/dev/null || sudo systemctl restart fail2ban 2>/dev/null
            echo "  ✓ $cidr added to whitelist and fail2ban reloaded"
            ;;

        whitelist-show)
            # Show current ignoreip list
            if [[ ! -f "$JAIL_CONF" ]]; then
                log_error "Jail config not found: $JAIL_CONF"
                exit 1
            fi
            echo ""
            echo "Current fail2ban whitelist (ignoreip):"
            grep "^ignoreip" "$JAIL_CONF" | sed 's/ignoreip\s*=\s*/  /' | tr ' ' '\n' | grep -v '^\s*$'
            echo ""
            ;;

        reload)
            sudo systemctl reload fail2ban 2>/dev/null || sudo systemctl restart fail2ban 2>/dev/null
            echo "  ✓ fail2ban reloaded"
            ;;

        logs)
            sudo journalctl -u fail2ban -n 100 --no-pager
            ;;

        *)
            echo "Usage: spiralctl fail2ban <action>"
            echo ""
            echo "Actions:"
            echo "  status                       Show ban counts for all Spiral Pool jails"
            echo "  banned                       List currently banned IPs"
            echo "  unban <IP>                   Remove a ban across all jails"
            echo "  whitelist-add <CIDR> [note]  Add CIDR to never-ban list (e.g. marketplace IPs)"
            echo "  whitelist-show               Show current whitelist"
            echo "  reload                       Reload fail2ban after manual config changes"
            echo "  logs                         Show recent fail2ban log entries"
            echo ""
            echo "Examples:"
            echo "  spiralctl fail2ban status"
            echo "  spiralctl fail2ban unban 1.2.3.4"
            echo "  spiralctl fail2ban whitelist-add 5.9.0.0/16 'NiceHash'"
            echo "  spiralctl fail2ban whitelist-add 192.168.100.0/24 'internal miners'"
            ;;
    esac
}

#===============================================================================
# WEBHOOK COMMAND
#===============================================================================

cmd_webhook() {
    local action="${1:-}"
    shift || true

    # Detect sentinel config location (same logic as cmd_config)
    local SENTINEL_HOME=""
    local SENTINEL_CONFIG=""

    # Try pool user home first (from systemd service)
    for service in spiralsentinel spiralstratum bitcoiniid bitcoind-bch bitcoind catcoind digibyted dogecoind fractald litecoind myriadcoind namecoind pepecoind qbitxd syscoind; do
        if [[ -f "/etc/systemd/system/${service}.service" ]]; then
            local svc_user=$(grep -oP '^User=\K.*' "/etc/systemd/system/${service}.service" 2>/dev/null | head -1)
            if [[ -n "$svc_user" ]] && [[ "$svc_user" != "root" ]]; then
                local svc_home=$(getent passwd "$svc_user" 2>/dev/null | cut -d: -f6)
                if [[ -d "$svc_home/.spiralsentinel" ]]; then
                    SENTINEL_HOME="$svc_home"
                    break
                fi
            fi
        fi
    done

    # Fallback to current user
    if [[ -z "$SENTINEL_HOME" ]]; then
        SENTINEL_HOME="$HOME"
    fi
    SENTINEL_CONFIG="$SENTINEL_HOME/.spiralsentinel/config.json"

    case "$action" in
        status)
            echo ""
            echo -e "${WHITE}WEBHOOK CONFIGURATION${NC}"
            echo -e "─────────────────────────────────────────────────────────────────────────"
            echo -e "  Config file: $SENTINEL_CONFIG"
            echo ""

            if [[ ! -f "$SENTINEL_CONFIG" ]]; then
                echo -e "  ${YELLOW}Config file not found${NC}"
                echo "  Run the installer to create configuration."
                echo ""
                return
            fi

            # Discord status
            local discord=$(grep -oP '"discord_webhook_url"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
            if [[ -n "$discord" ]]; then
                # Mask the URL for security (show domain + first 20 chars of path)
                local masked="${discord:0:45}...(hidden)"
                echo -e "  Discord:    ${GREEN}Configured${NC}"
                echo -e "  URL:        ${DIM}${masked}${NC}"
            else
                echo -e "  Discord:    ${DIM}Not configured${NC}"
            fi
            echo ""

            # Telegram status
            local tg_token=$(grep -oP '"telegram_bot_token"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
            local tg_chat=$(grep -oP '"telegram_chat_id"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
            local tg_enabled=$(python3 -c "
import json, sys
try:
    with open(sys.argv[1], 'r') as f:
        cfg = json.load(f)
    print('true' if cfg.get('telegram_enabled', False) else 'false')
except:
    print('false')
" "$SENTINEL_CONFIG" 2>/dev/null)

            if [[ -n "$tg_token" ]] && [[ -n "$tg_chat" ]]; then
                if [[ "$tg_enabled" == "true" ]]; then
                    echo -e "  Telegram:   ${GREEN}Configured & Enabled${NC}"
                else
                    echo -e "  Telegram:   ${YELLOW}Configured (disabled)${NC}"
                fi
                echo -e "  Token:      ${DIM}${tg_token:0:10}...(hidden)${NC}"
                echo -e "  Chat ID:    ${DIM}${tg_chat}${NC}"
            else
                echo -e "  Telegram:   ${DIM}Not configured${NC}"
            fi
            echo ""
            ;;

        set)
            local platform="${1:-}"
            shift || true

            case "$platform" in
                discord)
                    local url="${1:-}"
                    if [[ -z "$url" ]]; then
                        echo "Usage: spiralctl webhook set discord <webhook_url>"
                        exit 1
                    fi

                    # Validate Discord webhook URL format
                    if ! [[ "$url" =~ ^https://discord(app)?\.com/api/webhooks/ ]]; then
                        log_error "Invalid Discord webhook URL."
                        echo "URL must start with: https://discord.com/api/webhooks/"
                        exit 1
                    fi

                    if [[ ! -f "$SENTINEL_CONFIG" ]]; then
                        log_error "Config file not found: $SENTINEL_CONFIG"
                        exit 1
                    fi

                    # Backup before modification
                    cp "$SENTINEL_CONFIG" "${SENTINEL_CONFIG}.bak"

                    python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['discord_webhook_url'] = sys.argv[2]
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG" "$url"
                    log_success "Discord webhook URL configured"
                    echo "Sentinel hot-reloads config — no restart needed."
                    ;;

                telegram)
                    local token="${1:-}"
                    local chat_id="${2:-}"
                    if [[ -z "$token" ]] || [[ -z "$chat_id" ]]; then
                        echo "Usage: spiralctl webhook set telegram <bot_token> <chat_id>"
                        exit 1
                    fi

                    # Validate token format (must contain colon)
                    if ! [[ "$token" == *":"* ]]; then
                        log_error "Invalid Telegram bot token (must contain ':')."
                        echo "Example: 123456789:ABCdefGHIjklmnoPQRstuv"
                        exit 1
                    fi

                    if [[ ! -f "$SENTINEL_CONFIG" ]]; then
                        log_error "Config file not found: $SENTINEL_CONFIG"
                        exit 1
                    fi

                    # Backup before modification
                    cp "$SENTINEL_CONFIG" "${SENTINEL_CONFIG}.bak"

                    python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['telegram_bot_token'] = sys.argv[2]
cfg['telegram_chat_id'] = sys.argv[3]
cfg['telegram_enabled'] = True
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG" "$token" "$chat_id"
                    log_success "Telegram configured and enabled"
                    echo "Sentinel hot-reloads config — no restart needed."
                    ;;

                *)
                    echo "Usage: spiralctl webhook set <discord|telegram> ..."
                    echo ""
                    echo "  spiralctl webhook set discord <webhook_url>"
                    echo "  spiralctl webhook set telegram <bot_token> <chat_id>"
                    exit 1
                    ;;
            esac
            ;;

        clear)
            local platform="${1:-}"

            if [[ ! -f "$SENTINEL_CONFIG" ]]; then
                log_error "Config file not found: $SENTINEL_CONFIG"
                exit 1
            fi

            # Backup before modification
            cp "$SENTINEL_CONFIG" "${SENTINEL_CONFIG}.bak"

            case "$platform" in
                discord)
                    python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['discord_webhook_url'] = ''
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG"
                    log_success "Discord webhook cleared"
                    ;;

                telegram)
                    python3 -c "
import json, sys
with open(sys.argv[1], 'r') as f:
    cfg = json.load(f)
cfg['telegram_bot_token'] = ''
cfg['telegram_chat_id'] = ''
cfg['telegram_enabled'] = False
with open(sys.argv[1], 'w') as f:
    json.dump(cfg, f, indent=2)
" "$SENTINEL_CONFIG"
                    log_success "Telegram configuration cleared"
                    ;;

                *)
                    echo "Usage: spiralctl webhook clear <discord|telegram>"
                    exit 1
                    ;;
            esac
            ;;

        test)
            echo -e "${CYAN}Testing webhook endpoints...${NC}"
            echo ""

            if [[ ! -f "$SENTINEL_CONFIG" ]]; then
                log_error "Config file not found: $SENTINEL_CONFIG"
                exit 1
            fi

            # Test Discord
            local discord_url=$(grep -oP '"discord_webhook_url"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
            if [[ -n "$discord_url" ]]; then
                echo -n "  Discord: "
                local http_code=$(curl -s -o /dev/null -w "%{http_code}" \
                    -H "Content-Type: application/json" \
                    -d '{"embeds":[{"title":"Spiral Sentinel Test","description":"If you see this, your Discord webhook is working!","color":65345}]}' \
                    "$discord_url" 2>/dev/null)
                if [[ "$http_code" == "204" ]] || [[ "$http_code" == "200" ]]; then
                    echo -e "${GREEN}OK${NC} (HTTP $http_code)"
                else
                    echo -e "${RED}FAILED${NC} (HTTP $http_code)"
                fi
            else
                echo -e "  Discord: ${DIM}Not configured (skipped)${NC}"
            fi

            # Test Telegram
            local tg_token=$(grep -oP '"telegram_bot_token"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
            local tg_chat=$(grep -oP '"telegram_chat_id"\s*:\s*"\K[^"]+' "$SENTINEL_CONFIG" 2>/dev/null)
            if [[ -n "$tg_token" ]] && [[ -n "$tg_chat" ]]; then
                echo -n "  Telegram: "
                local http_code=$(curl -s -o /dev/null -w "%{http_code}" \
                    -H "Content-Type: application/json" \
                    -d "{\"chat_id\":\"$tg_chat\",\"text\":\"Spiral Sentinel Test - If you see this, your Telegram notifications are working!\"}" \
                    "https://api.telegram.org/bot${tg_token}/sendMessage" 2>/dev/null)
                if [[ "$http_code" == "200" ]]; then
                    echo -e "${GREEN}OK${NC} (HTTP $http_code)"
                else
                    echo -e "${RED}FAILED${NC} (HTTP $http_code)"
                fi
            else
                echo -e "  Telegram: ${DIM}Not configured (skipped)${NC}"
            fi
            echo ""
            ;;

        *)
            echo "Usage: spiralctl webhook <command> [options]"
            echo ""
            echo "Commands:"
            echo "  status                              Show webhook configuration"
            echo "  set discord <url>                   Configure Discord webhook"
            echo "  set telegram <token> <chat_id>      Configure Telegram notifications"
            echo "  clear discord                       Remove Discord webhook"
            echo "  clear telegram                      Remove Telegram configuration"
            echo "  test                                Send test message to all configured endpoints"
            echo ""
            echo "Examples:"
            echo "  spiralctl webhook status"
            echo "  spiralctl webhook set discord https://discord.com/api/webhooks/123/abc"
            echo "  spiralctl webhook set telegram 123456:ABCdef -12345678"
            echo "  spiralctl webhook test"
            exit 1
            ;;
    esac
}

#===============================================================================
# ADD COIN
#===============================================================================

cmd_add_coin() {
    local symbol=""
    local extra_args=()

    # Show usage if no args given
    if [[ $# -eq 0 ]]; then
        echo ""
        echo -e "${WHITE}USAGE${NC}"
        echo "    spiralctl add-coin <SYMBOL> --github <URL> [--algorithm sha256d|scrypt]"
        echo "    spiralctl add-coin <SYMBOL> --interactive"
        echo "    spiralctl add-coin <SYMBOL> --from-json <file>"
        echo ""
        echo -e "${WHITE}EXAMPLES${NC}"
        echo "    spiralctl add-coin FOO  --github https://github.com/foocoin/foocoin"
        echo "    spiralctl add-coin BAR  --github https://github.com/barcoin/bar --algorithm scrypt"
        echo "    spiralctl add-coin FOO  --interactive"
        echo ""
        echo -e "${DIM}For adding net-new coins that are NOT natively supported by Spiral Pool.${NC}"
        echo -e "${DIM}Fetches chain parameters from GitHub, queries CoinGecko for metadata,${NC}"
        echo -e "${DIM}detects the mining algorithm, and generates a complete coin implementation${NC}"
        echo -e "${DIM}and manifest entry with minimal manual input.${NC}"
        echo ""
        echo -e "${YELLOW}NOTE:${NC} ${DIM}The following coins are natively supported and installed via the installer:${NC}"
        echo -e "${DIM}  SHA-256d: BTC, BCH, BC2, DGB, FBTC, NMC, QBX, SYS, XMY${NC}"
        echo -e "${DIM}  Scrypt:   LTC, DOGE, DGB-SCRYPT, PEP, CAT${NC}"
        echo -e "${DIM}  Run ${NC}${CYAN}sudo bash ${INSTALL_DIR}/install.sh${NC}${DIM} to enable them.${NC}"
        echo ""
        return 0
    fi

    # First positional arg is the symbol (must not start with -)
    if [[ "$1" == -* ]]; then
        log_error "Expected coin symbol as first argument, got option: $1"
        echo "Usage: spiralctl add-coin <SYMBOL> --github <URL>"
        exit 1
    fi
    symbol="${1^^}"
    local symbol_lower="${symbol,,}"
    shift
    extra_args=("--symbol" "$symbol")

    # ── Built-in coin check ───────────────────────────────────────────────────
    # These coins ship with Spiral Pool and are installed via the installer.
    # add-coin is only for net-new coins not natively supported.
    local -A BUILTIN_COINS=(
        [DGB]="DigiByte"
        [BTC]="Bitcoin"
        [BCH]="Bitcoin Cash"
        [BC2]="Bitcoin II"
        [LTC]="Litecoin"
        [DOGE]="Dogecoin"
        [DGB-SCRYPT]="DigiByte (Scrypt)"
        [PEP]="Pepecoin"
        [CAT]="Catcoin"
        [NMC]="Namecoin"
        [SYS]="Syscoin"
        [XMY]="Myriadcoin"
        [FBTC]="FreeBitcoin"
        [QBX]="QbitX"
    )
    if [[ -n "${BUILTIN_COINS[$symbol]+x}" ]]; then
        local coin_name="${BUILTIN_COINS[$symbol]}"
        echo ""
        echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo -e "${WHITE}  ${coin_name} (${symbol}) is natively supported by Spiral Pool${NC}"
        echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo ""
        echo -e "  ${DIM}spiralctl add-coin is for net-new coins that are NOT natively supported${NC}"
        echo -e "  ${DIM}by Spiral Pool. ${coin_name} already has a full, tested implementation${NC}"
        echo -e "  ${DIM}built in — no onboarding script is needed.${NC}"
        echo ""
        echo -e "  To install ${WHITE}${coin_name}${NC}, re-run the installer and choose"
        echo -e "  ${GREEN}\"Add coins to existing installation\"${NC} when prompted."
        echo ""
        prompt_input "Would you like to run the installer now? [y/N]: "
        read -r run_installer
        if [[ "${run_installer,,}" == "y" || "${run_installer,,}" == "yes" ]]; then
            local installer=""
            for candidate in \
                "${INSTALL_DIR}/install.sh" \
                "$HOME/Spiral-Pool/install.sh" \
                "/home/*/Spiral-Pool/install.sh"; do
                # shellcheck disable=SC2086
                for f in $candidate; do
                    if [[ -f "$f" ]]; then installer="$f"; break 2; fi
                done
            done
            if [[ -z "$installer" ]]; then
                log_error "Installer not found."
                echo -e "  ${DIM}Expected at: ${INSTALL_DIR}/install.sh${NC}"
                echo -e "  ${DIM}Copy install.sh to ${INSTALL_DIR}/ or re-clone from: https://github.com/SpiralPool/Spiral-Pool${NC}"
                exit 1
            fi
            exec sudo bash "$installer"
        else
            echo ""
            echo -e "  Run ${CYAN}sudo bash ${INSTALL_DIR}/install.sh${NC} when ready."
            echo ""
        fi
        return 0
    fi
    # ─────────────────────────────────────────────────────────────────────────

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --github|-g)    extra_args+=("--github" "$2"); shift 2 ;;
            --algorithm|-a) extra_args+=("--algorithm" "$2"); shift 2 ;;
            --interactive)  extra_args+=("--interactive"); shift ;;
            --from-json)    extra_args+=("--from-json" "$2"); shift 2 ;;
            *)
                log_error "Unknown option: $1"
                echo "Run 'spiralctl add-coin' with no arguments for usage."
                exit 1
                ;;
        esac
    done

    # Locate add-coin.py (installed path or repo layout)
    local script=""
    for candidate in \
        "${INSTALL_DIR}/scripts/add-coin.py" \
        "$(dirname "$(realpath "$0")")/add-coin.py" \
        "$(dirname "$(realpath "$0")")/../scripts/add-coin.py"
    do
        if [[ -f "$candidate" ]]; then
            script="$candidate"
            break
        fi
    done

    if [[ -z "$script" ]]; then
        log_error "add-coin.py not found under ${INSTALL_DIR}/scripts/"
        exit 1
    fi

    if ! command -v python3 &>/dev/null; then
        log_error "python3 is required but not installed."
        exit 1
    fi

    # ── Run add-coin.py ───────────────────────────────────────────────────────
    log_info "Running coin onboarding for ${symbol}..."

    # Interactive / from-json modes need a real TTY — run without output capture
    # so prompts are visible. Port parsing falls back to the params JSON.
    local sv1="" sv2="" stls=""
    local needs_tty=false
    for arg in "${extra_args[@]}"; do
        [[ "$arg" == "--interactive" || "$arg" == "--from-json" ]] && needs_tty=true
    done

    if [[ "$needs_tty" == true ]]; then
        python3 "$script" "${extra_args[@]}"
        local py_exit=$?
        if [[ $py_exit -ne 0 ]]; then
            log_error "add-coin.py exited with error ($py_exit)"
            exit $py_exit
        fi
    else
        # Automated mode: capture output so we can parse the port line
        local py_output
        py_output=$(python3 "$script" "${extra_args[@]}" 2>&1)
        local py_exit=$?
        echo "$py_output"
        if [[ $py_exit -ne 0 ]]; then
            log_error "add-coin.py exited with error ($py_exit)"
            exit $py_exit
        fi
        # ── Parse stratum ports from add-coin.py output ──────────────────────
        local port_line
        port_line=$(echo "$py_output" | grep "^__STRATUM_PORTS__:" | tail -1)
        if [[ -n "$port_line" ]]; then
            IFS=':' read -r _tag sv1 sv2 stls <<< "$port_line"
        fi
    fi

    # ── Read P2P port from generated params JSON ─────────────────────────────
    local params_json="${INSTALL_DIR}/scripts/${symbol_lower}_params.json"
    local p2p_port=""
    if [[ -f "$params_json" ]] && command -v python3 &>/dev/null; then
        p2p_port=$(python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print(d.get('p2p_port',''))" "$params_json" 2>/dev/null || true)
    fi

    # ── Sync generated Go file into stratum-src ──────────────────────────────
    local go_src="${INSTALL_DIR}/src/stratum/internal/coin/${symbol_lower}.go"
    local go_dst_dir="${INSTALL_DIR}/stratum-src/internal/coin"
    if [[ -f "$go_src" && -d "$go_dst_dir" ]]; then
        cp "$go_src" "${go_dst_dir}/${symbol_lower}.go"
        log_success "Synced ${symbol_lower}.go → stratum-src"
    elif [[ -f "$go_src" ]]; then
        log_warn "stratum-src not found — skipping Go file sync (run install.sh first)"
    fi

    # ── Open firewall ports ───────────────────────────────────────────────────
    if command -v ufw &>/dev/null; then
        local fw_opened=()
        for port in "$sv1" "$sv2" "$stls"; do
            if [[ -n "$port" && "$port" =~ ^[0-9]+$ ]]; then
                ufw allow "${port}/tcp" > /dev/null 2>&1 && fw_opened+=("${port}/tcp")
            fi
        done
        if [[ -n "$p2p_port" && "$p2p_port" =~ ^[0-9]+$ ]]; then
            ufw allow "${p2p_port}/tcp" > /dev/null 2>&1 && fw_opened+=("${p2p_port}/tcp (P2P)")
        fi
        if [[ ${#fw_opened[@]} -gt 0 ]]; then
            log_success "Firewall ports opened: ${fw_opened[*]}"
        fi
    else
        log_warn "ufw not found — open these ports manually:"
        [[ -n "$sv1"   ]] && echo "  ${sv1}/tcp (stratum V1)"
        [[ -n "$sv2"   ]] && echo "  ${sv2}/tcp (stratum V2)"
        [[ -n "$stls"  ]] && echo "  ${stls}/tcp (stratum TLS)"
        [[ -n "$p2p_port" ]] && echo "  ${p2p_port}/tcp (P2P)"
    fi

    # ── Offer to rebuild and restart stratum ─────────────────────────────────
    echo ""
    local src_dir="${INSTALL_DIR}/stratum-src"
    local bin="${INSTALL_DIR}/bin/spiralstratum"
    if [[ -d "$src_dir" ]] && command -v go &>/dev/null; then
        read -rp "Rebuild and restart stratum now? [Y/n] " answer
        if [[ "${answer,,}" != "n" ]]; then
            log_info "Building stratum..."
            if sudo -u "$POOL_USER" bash -c \
                "cd '${src_dir}' && PATH=\$PATH:/usr/local/go/bin CGO_ENABLED=1 go build -o '${bin}' ./cmd/spiralpool" 2>&1; then
                log_success "Stratum rebuilt successfully"
                log_info "Restarting spiralstratum..."
                systemctl restart spiralstratum && log_success "spiralstratum restarted"
            else
                log_error "Build failed — review errors above and rebuild manually:"
                echo "  cd ${src_dir} && go build -o ${bin} ./cmd/spiralpool"
            fi
        else
            echo -e "${DIM}To rebuild manually:${NC}"
            echo "  cd ${src_dir} && sudo -u ${POOL_USER} go build -o ${bin} ./cmd/spiralpool"
            echo "  sudo systemctl restart spiralstratum"
        fi
    else
        echo -e "${DIM}Rebuild stratum to activate ${symbol}:${NC}"
        echo "  cd ${src_dir} && sudo -u ${POOL_USER} go build -o ${bin} ./cmd/spiralpool"
        echo "  sudo systemctl restart spiralstratum"
    fi

    # Remind user to configure wallet address in the dashboard
    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${WHITE}  CONFIGURE WALLET ADDRESS${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${YELLOW}After stratum restarts, set the ${symbol} wallet address in the Dashboard:${NC}"
    local dash_ip; dash_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    local dash_proto="http"
    if grep -q "^ExecStart.*\-\-certfile" /etc/systemd/system/spiraldash.service 2>/dev/null; then dash_proto="https"; fi
    local dash_port; dash_port=$(grep -oP '0\.0\.0\.0:\K[0-9]+' /etc/systemd/system/spiraldash.service 2>/dev/null | head -1)
    dash_port="${dash_port:-1618}"
    echo -e "  ${GREEN}${dash_proto}://${dash_ip:-<your-server-ip>}:${dash_port}/setup${NC}"
    echo ""
    echo -e "  ${DIM}The setup wizard auto-detects active coins and shows wallet inputs for each.${NC}"
    echo ""
}

#===============================================================================
# REMOVE COIN
#===============================================================================

cmd_remove_coin() {
    if [[ $# -eq 0 ]]; then
        echo ""
        echo -e "${WHITE}USAGE${NC}"
        echo "    spiralctl remove-coin <SYMBOL> [--yes]"
        echo ""
        echo -e "${WHITE}EXAMPLES${NC}"
        echo "    spiralctl remove-coin DOGE"
        echo "    spiralctl remove-coin DOGE --yes   (skip confirmation)"
        echo ""
        echo -e "${DIM}Removes the Go implementation, Dockerfile, node config template,${NC}"
        echo -e "${DIM}native installer, params JSON, and manifest entry for the coin.${NC}"
        echo -e "${DIM}Wallet data and blockchain data are NEVER deleted.${NC}"
        echo ""
        return 0
    fi

    local symbol="${1^^}"
    local symbol_lower="${symbol,,}"
    local confirm=false
    shift

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --yes|-y) confirm=true; shift ;;
            *)
                log_error "Unknown option: $1"
                echo "Usage: spiralctl remove-coin <SYMBOL> [--yes]"
                exit 1
                ;;
        esac
    done

    # Files that add-coin generates
    local go_file="${INSTALL_DIR}/src/stratum/internal/coin/${symbol_lower}.go"
    local dockerfile="${INSTALL_DIR}/docker/Dockerfile.${symbol_lower}"
    local conf_template="${INSTALL_DIR}/docker/config/${symbol_lower}.conf.template"
    local install_script="${INSTALL_DIR}/scripts/install-${symbol}.sh"
    local params_json="${INSTALL_DIR}/scripts/${symbol_lower}_params.json"
    local manifest="${INSTALL_DIR}/config/coins.manifest.yaml"

    # Inventory what actually exists
    local found=()
    for f in "$go_file" "$dockerfile" "$conf_template" "$install_script" "$params_json"; do
        [[ -f "$f" ]] && found+=("$f")
    done

    if [[ -f "$manifest" ]] && grep -qE "^\s+-\s+symbol:\s+['\"]?${symbol}['\"]?\s*$" "$manifest" 2>/dev/null; then
        found+=("${manifest} (entry for ${symbol})")
    fi

    if [[ ${#found[@]} -eq 0 ]]; then
        log_warn "No generated files found for coin: ${symbol}"
        echo "If the coin was added manually, remove its entries from:"
        echo "  ${manifest}"
        echo "  ${INSTALL_DIR}/src/stratum/internal/coin/${symbol_lower}.go"
        exit 1
    fi

    echo ""
    echo -e "${WHITE}Files to remove for ${symbol}:${NC}"
    for f in "${found[@]}"; do
        echo "  - $f"
    done
    echo ""

    if [[ "$confirm" == false ]]; then
        read -rp "Remove these files? [y/N] " answer
        [[ "${answer,,}" != "y" ]] && { log_info "Aborted."; exit 0; }
    fi

    # Disable coin daemon if running
    local daemon
    daemon=$(get_coin_daemon "$symbol" 2>/dev/null || echo "")
    if [[ -n "$daemon" ]] && systemctl is-active --quiet "$daemon" 2>/dev/null; then
        log_info "Stopping ${daemon}..."
        systemctl stop "$daemon" 2>/dev/null || true
        systemctl disable "$daemon" 2>/dev/null || true
    fi

    # Read ports from params JSON before deleting it (for firewall cleanup)
    local p2p_port="" sv1="" sv2="" stls=""
    if [[ -f "$params_json" ]] && command -v python3 &>/dev/null; then
        local port_data
        port_data=$(python3 -c "
import json, sys
d = json.load(open(sys.argv[1]))
print(d.get('p2p_port',''))
print(d.get('stratum_port',''))
print(d.get('stratum_v2_port',''))
print(d.get('stratum_tls_port',''))
" "$params_json" 2>/dev/null || true)
        local -a _ports
        mapfile -t _ports <<< "$port_data"
        p2p_port="${_ports[0]:-}"
        sv1="${_ports[1]:-}"
        sv2="${_ports[2]:-}"
        stls="${_ports[3]:-}"
    fi

    # Remove files
    for f in "$go_file" "$dockerfile" "$conf_template" "$install_script" "$params_json"; do
        if [[ -f "$f" ]]; then
            rm -f "$f" && log_success "Removed: $f"
        fi
    done

    # Remove Go file from stratum-src if it exists there
    local go_dst="${INSTALL_DIR}/stratum-src/internal/coin/${symbol_lower}.go"
    if [[ -f "$go_dst" ]]; then
        rm -f "$go_dst" && log_success "Removed: $go_dst"
    fi

    # Close firewall ports
    if command -v ufw &>/dev/null; then
        local fw_closed=()
        for port in "$sv1" "$sv2" "$stls"; do
            if [[ -n "$port" && "$port" =~ ^[0-9]+$ ]]; then
                ufw delete allow "${port}/tcp" > /dev/null 2>&1 && fw_closed+=("${port}/tcp")
            fi
        done
        if [[ -n "$p2p_port" && "$p2p_port" =~ ^[0-9]+$ ]]; then
            ufw delete allow "${p2p_port}/tcp" > /dev/null 2>&1 && fw_closed+=("${p2p_port}/tcp (P2P)")
        fi
        if [[ ${#fw_closed[@]} -gt 0 ]]; then
            log_success "Firewall ports closed: ${fw_closed[*]}"
        fi
    fi

    # Remove manifest entry using line-based removal (preserves comments for other coins)
    if [[ -f "$manifest" ]] && grep -qE "^\s+-\s+symbol:\s+['\"]?${symbol}['\"]?\s*$" "$manifest" 2>/dev/null; then
        if command -v python3 &>/dev/null; then
            python3 - "$manifest" "$symbol" << 'PYEOF'
import sys, re, pathlib

manifest_path = pathlib.Path(sys.argv[1])
target_symbol = sys.argv[2].upper()

lines = manifest_path.read_text().splitlines(keepends=True)
out = []
i = 0
# Matches "  - symbol: DOGE" with optional quotes and any indent
block_start = re.compile(
    r'^\s+-\s+symbol:\s+["\']?' + re.escape(target_symbol) + r'["\']?\s*$',
    re.IGNORECASE
)
while i < len(lines):
    line = lines[i]
    if block_start.match(line):
        # Remove any preceding comment lines that belong to this block
        while out and re.match(r'^\s+#', out[-1]):
            out.pop()
        # Determine indent of the list item marker
        indent = len(line) - len(line.lstrip())
        i += 1
        # Skip lines until the next list item at the same or shallower indent
        while i < len(lines):
            stripped = lines[i].lstrip()
            next_indent = len(lines[i]) - len(stripped)
            if stripped.startswith('- ') and next_indent <= indent:
                break
            i += 1
        print(f"  Manifest entry for {target_symbol} removed.")
        continue
    out.append(line)
    i += 1

manifest_path.write_text("".join(out))
PYEOF
        else
            log_warn "python3 not found — remove the ${symbol} block from ${manifest} manually."
        fi
    fi

    echo ""
    log_success "Coin ${symbol} removed."
    echo ""
    echo -e "  ${GREEN}Wallet data and blockchain data are preserved.${NC}"
    echo -e "  ${DIM}To re-add this coin later, re-run: sudo bash install.sh${NC}"

    # Remind user to update dashboard
    echo ""
    echo -e "  ${YELLOW}After restarting stratum, update wallet config in the Dashboard:${NC}"
    local dash_ip; dash_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    local dash_proto="http"
    if grep -q "^ExecStart.*\-\-certfile" /etc/systemd/system/spiraldash.service 2>/dev/null; then dash_proto="https"; fi
    local dash_port; dash_port=$(grep -oP '0\.0\.0\.0:\K[0-9]+' /etc/systemd/system/spiraldash.service 2>/dev/null | head -1)
    dash_port="${dash_port:-1618}"
    echo -e "  ${GREEN}${dash_proto}://${dash_ip:-<your-server-ip>}:${dash_port}/setup${NC}"
    echo -e "  ${DIM}The setup wizard will reflect the updated coin list automatically.${NC}"

    local src_dir="${INSTALL_DIR}/stratum-src"
    local bin="${INSTALL_DIR}/bin/spiralstratum"
    if [[ -d "$src_dir" ]] && command -v go &>/dev/null; then
        read -rp "Rebuild and restart stratum now to apply removal? [Y/n] " answer
        if [[ "${answer,,}" != "n" ]]; then
            log_info "Building stratum..."
            if sudo -u "$POOL_USER" bash -c \
                "cd '${src_dir}' && PATH=\$PATH:/usr/local/go/bin CGO_ENABLED=1 go build -o '${bin}' ./cmd/spiralpool" 2>&1; then
                log_success "Stratum rebuilt"
                systemctl restart spiralstratum && log_success "spiralstratum restarted"
            else
                log_error "Build failed — rebuild manually after reviewing errors"
            fi
        fi
    else
        echo -e "${DIM}Rebuild stratum to apply removal:${NC}"
        echo "  cd ${src_dir} && sudo -u ${POOL_USER} go build -o ${bin} ./cmd/spiralpool"
        echo "  sudo systemctl restart spiralstratum"
    fi
    echo ""
}

#===============================================================================
# HELP & VERSION
#===============================================================================

show_help() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "  ${WHITE}SPIRALCTL${NC} - Spiral Pool Control Utility  v${VERSION}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "${WHITE}USAGE${NC}"
    echo "    spiralctl <command> [subcommand] [options]"
    echo ""
    echo -e "${WHITE}OPERATIONS${NC}"
    echo "    status              Show full service & node status overview"
    echo "    restart             Restart all Spiral Pool services"
    echo "    logs                View stratum log output"
    echo "    watch               Live monitoring dashboard"
    echo "    test                Run diagnostic / connectivity tests"
    echo "    update              Check for Spiral Pool updates"
    echo "    maintenance         Enter or leave maintenance mode"
    echo "    pause [minutes]     Pause Sentinel alerts temporarily"
    echo "    shutdown [--reboot] Gracefully stop all services, then power off (or reboot)"
    echo ""
    echo -e "${WHITE}BLOCKCHAIN${NC}"
    echo "    sync [--watch|-w] [--coin <coin>]"
    echo "                        Blockchain sync progress"
    echo "    chain export        Push blockchain data to a remote machine"
    echo "    chain restore       Pull blockchain data from a remote machine"
    echo "    node [status|start|stop|restart] [coin|all]"
    echo "                        Manage blockchain node daemons"
    echo "    coin [status|list|enable|disable] <coin>"
    echo "                        Manage cryptocurrency support"
    echo "    coin enable <SYMBOL>"
    echo "                        Add a supported coin (installs daemon, generates wallet)"
    echo "    add-coin <SYMBOL> --github <URL> [--algorithm sha256d|scrypt]"
    echo "                        Add a CUSTOM coin from its GitHub repository (advanced)"
    echo "    remove-coin <SYMBOL> [--yes]"
    echo "                        Remove a coin and all its generated files"
    echo "    coin-upgrade [--check] [--coin <TICKER>] [--reindex]"
    echo "                        Upgrade coin daemon binaries (run after upgrade.sh if prompted)"
    echo ""
    echo -e "${WHITE}MINING${NC}"
    echo "    mining [status|solo|multi|merge|multiport] [options]"
    echo "                        Mining mode management"
    echo "    mining multiport [status|enable|disable|weights]"
    echo "                        Multi-coin smart port (24h UTC schedule, port 16180)"
    echo "    pool stats          Pool hashrate and worker statistics"
    echo "    stats [blocks [N]]  Quick pool stats; 'stats blocks' shows last N blocks"
    echo "    miners [list]       Show connected miners with hashrate and shares"
    echo "    miners kick <IP>    Disconnect all sessions from a miner IP"
    echo "    workers             Per-worker breakdown (miner → rig → hashrate + acceptance)"
    echo "    miner nick [list | <IP> <name> | clear <IP>]"
    echo "                        View or set miner nicknames in Sentinel"
    echo "    scan                Scan network for miners"
    echo "    wallet              Show or generate wallet addresses"
    echo "    external [setup|enable|disable|status|test]"
    echo "                        External access / hashrate rental"
    echo ""
    echo -e "${WHITE}DATA${NC}"
    echo "    data backup         Backup pool data and configuration"
    echo "    data restore        Restore pool data from backup"
    echo "    data export         Export mining history to CSV"
    echo "    gdpr-delete         Delete miner data (GDPR/CCPA)"
    echo ""
    echo -e "${WHITE}HA / FAILOVER${NC}"
    echo "    ha [status|enable|disable|credentials|setup|failback|promote|validate|service]"
    echo "                        High Availability cluster management"
    echo "    ha vip [status|enable|disable|failover]"
    echo "                        Virtual IP for miner failover"
    echo "    sync-addresses [--apply] [--force] [--dry-run]"
    echo "                        Sync pool addresses from HA master"
    echo ""
    echo -e "${WHITE}CONFIGURATION${NC}"
    echo "    config [show|list|get|set] [key] [value]"
    echo "                        View or update Sentinel configuration"
    echo "    config validate     Dry-run config check (no restart required)"
    echo "    config notify-test  Send a test notification to every configured channel"
    echo "    config list-cooldowns"
    echo "                        Show active alert cooldowns with time remaining"
    echo "    log [errors] [service] [window]"
    echo "                        Filter service logs for errors/warnings (default: 1h)"
    echo "                        Services: stratum, sentinel, dash, patroni, ha"
    echo "    webhook [status|set|clear|test]"
    echo "                        Manage Discord & Telegram notifications"
    echo ""
    echo -e "${WHITE}SECURITY${NC}"
    echo "    security [period]   Security status overview (default: 24h)"
    echo "    security fail2ban [status|banned|unban|whitelist-add|whitelist-show|reload|logs]"
    echo "                        Manage fail2ban jails"
    echo "    security tor [status|enable|disable]"
    echo "                        Manage Tor privacy settings"
    echo ""
    echo -e "${WHITE}EXAMPLES${NC}"
    echo "    spiralctl status                           Overview of all services"
    echo "    spiralctl sync --watch                     Live blockchain sync progress"
    echo "    spiralctl logs                             Tail stratum logs"
    echo "    spiralctl restart                          Restart all pool services"
    echo "    spiralctl wallet --coin btc                Show BTC wallet address"
    echo "    spiralctl scan                             Discover miners on the network"
    echo "    spiralctl data backup                      Backup pool data"
    echo "    spiralctl stats blocks                     Show recent blocks"
    echo "    spiralctl chain export --host 10.0.0.2     Push blockchain to remote"
    echo "    spiralctl mining solo dgb                  Switch to solo DGB mining"
    echo "    spiralctl mining multiport enable dgb:19.2h,btc:4.8h"
    echo "                                              Enable smart port (hours, sum=24)"
    echo "    spiralctl mining multiport enable dgb:80,btc:20"
    echo "                                              Enable smart port (weights, sum=100)"
    echo "    spiralctl ha enable --vip 192.168.1.100    Enable HA on this node"
    echo "    spiralctl ha vip status                    Check VIP / keepalived state"
    echo "    spiralctl security                         Security overview (last 24h)"
    echo "    spiralctl security fail2ban unban 1.2.3.4  Remove a ban"
    echo "    spiralctl config set expected_hashrate 50  Update expected TH/s"
    echo "    spiralctl config validate                  Check config for issues (no restart)"
    echo "    spiralctl config notify-test               Send test notification to all channels"
    echo "    spiralctl config list-cooldowns            Show which alerts are on cooldown"
    echo "    spiralctl log errors                       Errors in the last 1h across all services"
    echo "    spiralctl log errors sentinel              Sentinel errors only, last 1h"
    echo "    spiralctl log errors stratum 24h           Stratum errors, last 24h"
    echo "    spiralctl miners                           List connected miners + hashrate"
    echo "    spiralctl miners kick 192.168.1.50         Disconnect a miner"
    echo "    spiralctl workers                          Per-worker hashrate breakdown"
    echo "    spiralctl miner nick 192.168.1.50 \"Rig 1\"  Set miner nickname"
    echo "    spiralctl coin enable LTC                   Add Litecoin to the pool"
    echo "    spiralctl coin disable DOGE                Disable Dogecoin daemon"
    echo "    spiralctl remove-coin DOGE                 Remove DOGE generated files (keeps wallet)"
    echo "    spiralctl shutdown                         Gracefully stop all services and power off"
    echo "    spiralctl shutdown --reboot                Gracefully stop all services and reboot"
    echo ""
    echo -e "${WHITE}SUPPORTED COINS${NC}"
    echo "    SHA-256d: bc2, bch, btc, dgb, fbtc, nmc, qbx, sys, xmy"
    echo "    Scrypt:   cat, dgb-scrypt, doge, ltc, pep"
    echo ""
}

show_version() {
    echo ""
    echo -e "${CYAN}SPIRAL POOL${NC}"
    echo -e "────────────────────────────────────────"
    printf "  %-24s %s\n" "spiralctl" "$VERSION"

    # Stratum binary version
    local stratum_bin="${INSTALL_DIR}/bin/spiralstratum"
    if [[ -x "$stratum_bin" ]]; then
        local sv
        sv=$("$stratum_bin" --version 2>&1 | grep -oP '\d+\.\d+[\.\d]*' | head -1)
        [[ -z "$sv" ]] && sv="unknown"
        printf "  %-24s %s\n" "spiralstratum" "$sv"
    fi

    # Sentinel version (from sentinel source header)
    local sentinel_ver=""
    local sentinel_py="${INSTALL_DIR}/src/sentinel/SpiralSentinel.py"
    if [[ -f "$sentinel_py" ]]; then
        sentinel_ver=$(grep -m1 -oP '(?<=version[[:space:]])[\d.]+' "$sentinel_py" 2>/dev/null \
            || grep -m1 '__version__\s*=' "$sentinel_py" 2>/dev/null | grep -oP '[\d.]+')
    fi
    [[ -z "$sentinel_ver" ]] && sentinel_ver="$VERSION"
    printf "  %-24s %s\n" "sentinel" "$sentinel_ver"

    echo ""
    echo -e "${WHITE}COIN DAEMONS${NC}"
    echo -e "────────────────────────────────────────"
    local coins=("bc2:bitcoiniid" "bch:bitcoind-bch" "btc:bitcoind" "cat:catcoind"
                 "dgb:digibyted" "doge:dogecoind" "fbtc:fractald" "ltc:litecoind"
                 "nmc:namecoind" "pep:pepecoind" "qbx:qbitxd" "sys:syscoind" "xmy:myriadcoind")
    local shown_any=0
    for entry in "${coins[@]}"; do
        local ticker="${entry%%:*}"
        local svc="${entry##*:}"
        if systemctl is-enabled --quiet "$svc" 2>/dev/null; then
            local dv
            dv=$(get_coin_daemon_version "$ticker")
            [[ -z "$dv" ]] && dv="not found"
            printf "  %-24s %s\n" "${ticker^^}" "$dv"
            shown_any=1
        fi
    done
    [[ "$shown_any" -eq 0 ]] && echo -e "  ${DIM}No coin daemons enabled${NC}"
    echo ""
}

#===============================================================================
# COIN DAEMON UPGRADE
#===============================================================================

cmd_coin_upgrade() {
    local script="${INSTALL_DIR}/scripts/coin-upgrade.sh"
    if [[ ! -x "$script" ]]; then
        log_error "coin-upgrade.sh not found at ${script}"
        echo "Run 'sudo upgrade.sh' first to deploy it, or install Spiral Pool v2.0.0+."
        exit 1
    fi
    # Pass all arguments through — supports --check, --coin <TICKER>, --reindex, --list
    exec "$script" "$@"
}

#===============================================================================
# ADDRESS SYNC (HA)
#===============================================================================

cmd_sync_addresses() {
    local do_apply=false
    local do_force=false
    local dry_run=false

    # Parse flags
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --apply)   do_apply=true; shift ;;
            --force)   do_force=true; shift ;;
            --dry-run) dry_run=true; shift ;;
            *)
                log_error "Unknown option: $1"
                echo "Usage: spiralctl sync-addresses [--apply] [--force] [--dry-run]"
                exit 1
                ;;
        esac
    done

    # Verify jq is available (required for JSON parsing)
    if ! command -v jq &>/dev/null; then
        log_error "jq is required for HA address sync. Install with: sudo apt install jq"
        exit 1
    fi

    # Query the HA status API
    local status_json
    status_json=$(curl -s --max-time 5 "http://localhost:5354/status" 2>/dev/null || echo "")

    if [[ -z "$status_json" ]]; then
        log_error "HA status API unavailable (localhost:5354)"
        exit 1
    fi

    local ha_enabled
    ha_enabled=$(echo "$status_json" | jq -r '.enabled // false' 2>/dev/null || echo "false")
    if [[ "$ha_enabled" != "true" ]]; then
        log_error "HA is not enabled on this node"
        exit 1
    fi

    # Check local role — refuse on MASTER unless --force
    local local_role
    local_role=$(echo "$status_json" | jq -r '.localRole // "UNKNOWN"' 2>/dev/null || echo "UNKNOWN")

    if [[ "$local_role" == "MASTER" && "$do_force" != "true" ]]; then
        log_error "This node is the MASTER — address sync pulls FROM the master"
        echo "Use --force to override (e.g., to push a manually-set address)"
        exit 1
    fi

    # Find MASTER node's poolAddresses
    local master_addrs_raw
    master_addrs_raw=$(echo "$status_json" | jq -r '
        .nodes[] | select(.role == "MASTER") |
        .poolAddresses // {} | to_entries[] |
        "\(.key)=\(.value)"
    ' 2>/dev/null || echo "")

    if [[ -z "$master_addrs_raw" ]]; then
        log_warn "Master node has no poolAddresses — nothing to sync"
        exit 0
    fi

    # Build associative array of master addresses
    declare -A master_addrs
    while IFS='=' read -r coin addr; do
        [[ -z "$coin" ]] && continue
        coin="${coin^^}"  # uppercase
        master_addrs["$coin"]="$addr"
    done <<< "$master_addrs_raw"

    # Parse local config.yaml for current addresses
    if [[ ! -f "$CONFIG_FILE" ]]; then
        log_error "Config file not found: $CONFIG_FILE"
        exit 1
    fi

    declare -A local_addrs
    local current_coin=""
    while IFS= read -r line; do
        if [[ "$line" =~ symbol:[[:space:]]*\"?([A-Za-z0-9-]+)\"? ]]; then
            current_coin="${BASH_REMATCH[1]^^}"
        elif [[ "$line" =~ ^[[:space:]]*coin:[[:space:]]*\"?([A-Za-z0-9-]+)\"? ]] && [[ -z "$current_coin" ]]; then
            # V1 single-coin mode — map coin name to symbol
            local coin_name="${BASH_REMATCH[1],,}"
            case "$coin_name" in
                digibyte)        current_coin="DGB" ;;
                bitcoin)         current_coin="BTC" ;;
                litecoin)        current_coin="LTC" ;;
                dogecoin)        current_coin="DOGE" ;;
                namecoin)        current_coin="NMC" ;;
                syscoin)         current_coin="SYS" ;;
                myriadcoin)      current_coin="XMY" ;;
                pepecoin)        current_coin="PEP" ;;
                catcoin)         current_coin="CAT" ;;
                bitcoincash)     current_coin="BCH" ;;
                bitcoinii)       current_coin="BC2" ;;
                digibyte-scrypt) current_coin="DGB-SCRYPT" ;;
                fractalbitcoin)  current_coin="FBTC" ;;
                qbitx)           current_coin="QBX" ;;
                *)               current_coin="${coin_name^^}" ;;
            esac
        fi
        if [[ "$line" =~ address:[[:space:]]*\"?([^\"[:space:]]+)\"? ]] && [[ -n "$current_coin" ]]; then
            local_addrs["$current_coin"]="${BASH_REMATCH[1]}"
            current_coin=""
        fi
    done < "$CONFIG_FILE"

    # Compare and collect changes
    local changes=0
    local changed_coins=()
    local changed_old=()
    local changed_new=()

    for coin in "${!master_addrs[@]}"; do
        local master_addr="${master_addrs[$coin]}"
        local local_addr="${local_addrs[$coin]:-}"

        # Skip PENDING_GENERATION addresses
        if [[ "$master_addr" == "PENDING_GENERATION" ]]; then
            log_warn "  ${coin}: master address is PENDING_GENERATION — skipping"
            continue
        fi

        if [[ "$master_addr" != "$local_addr" ]]; then
            changed_coins+=("$coin")
            changed_old+=("${local_addr:-<not set>}")
            changed_new+=("$master_addr")
            changes=$((changes + 1))
        fi
    done

    if [[ $changes -eq 0 ]]; then
        log_success "All addresses in sync with master"
        return 0
    fi

    # Show diff summary
    echo ""
    echo -e "${WHITE}Address changes needed:${NC}"
    echo -e "────────────────────────────────────────"
    for i in "${!changed_coins[@]}"; do
        printf "  %-12s %s → %s\n" "${changed_coins[$i]}:" "${changed_old[$i]}" "${changed_new[$i]}"
    done
    echo ""

    if [[ "$dry_run" == "true" ]]; then
        log_info "Dry run — no changes made"
        return 0
    fi

    # Apply changes to config.yaml
    # NOTE: check_root removed — sync-addresses only needs write access to config.yaml
    # (owned by spiraluser). systemctl restart uses sudo (sudoers entry exists).
    # This allows ha-role-watcher.sh to call spiralctl as spiraluser without root.
    if [[ ! -w "$CONFIG_FILE" ]]; then
        log_error "Config file not writable: $CONFIG_FILE (run as owner or root)"
        exit 1
    fi

    # Backup config before modifying
    cp "$CONFIG_FILE" "${CONFIG_FILE}.bak"
    log_info "Config backed up to ${CONFIG_FILE}.bak"

    local applied=0
    for i in "${!changed_coins[@]}"; do
        local coin="${changed_coins[$i]}"
        local new_addr="${changed_new[$i]}"

        # Find the coin's symbol line in config, then the next address line
        local sym_line
        sym_line=$(grep -nE "symbol:[[:space:]]*\"?${coin}\"?[[:space:]]*$" "$CONFIG_FILE" | head -1 | cut -d: -f1)

        if [[ -z "$sym_line" ]]; then
            # Check if this is genuinely V1 (no symbol: lines at all) or V2 with missing coin
            if grep -q "symbol:" "$CONFIG_FILE"; then
                # V2 config — this coin doesn't exist locally, can't update
                log_warn "  ${coin}: not found in local config — add coin manually then re-sync"
                continue
            fi
            # V1 single-coin mode — look for the single pool address line (skip comments)
            local single_addr_line
            single_addr_line=$(grep -n "^[^#]*address:" "$CONFIG_FILE" | head -1 | cut -d: -f1)
            if [[ -n "$single_addr_line" ]]; then
                sed -i "${single_addr_line}s|address:.*|address: \"${new_addr}\"|" "$CONFIG_FILE"
                log_success "  ${coin}: updated (V1 single-coin)"
                applied=$((applied + 1))
            else
                log_error "  ${coin}: could not find address line in config"
            fi
        else
            # V2 multi-coin — find the address line after the symbol line (skip comments)
            local addr_offset
            addr_offset=$(tail -n "+${sym_line}" "$CONFIG_FILE" | grep -n "^[^#]*address:" | head -1 | cut -d: -f1)
            if [[ -n "$addr_offset" ]]; then
                local target_line=$((sym_line + addr_offset - 1))
                sed -i "${target_line}s|address:.*|address: \"${new_addr}\"|" "$CONFIG_FILE"
                log_success "  ${coin}: updated"
                applied=$((applied + 1))
            else
                log_error "  ${coin}: could not find address line after symbol"
            fi
        fi
    done

    echo ""
    log_info "${applied}/${changes} address(es) updated in config.yaml"

    if [[ "$do_apply" == "true" && $applied -gt 0 ]]; then
        log_info "Restarting spiralstratum to apply changes..."
        sudo /bin/systemctl restart spiralstratum
        log_success "Stratum restarted with updated addresses"
    elif [[ $applied -gt 0 ]]; then
        echo "Run with --apply to restart stratum, or restart manually:"
        echo "  sudo systemctl restart spiralstratum"
    fi
}

#===============================================================================
# WORKERS COMMAND — per-worker breakdown (address → worker name → stats)
#===============================================================================

cmd_workers() {
    local pool_api="http://localhost:4000"

    local pools_json
    pools_json=$(curl -sf --max-time 8 "$pool_api/api/pools" 2>/dev/null || echo "")
    if [[ -z "$pools_json" ]] || ! command -v jq &>/dev/null; then
        log_error "Pool API unavailable or jq not installed"
        exit 1
    fi

    local header_printed=false

    while IFS= read -r pool_id; do
        [[ -z "$pool_id" ]] && continue
        local symbol
        symbol=$(echo "$pools_json" | jq -r --arg id "$pool_id" '.[] | select(.id==$id) | .coin.symbol' 2>/dev/null || echo "$pool_id")

        local miners_json
        miners_json=$(curl -sf --max-time 8 "$pool_api/api/pools/${pool_id}/miners" 2>/dev/null || echo "")
        [[ -z "$miners_json" ]] && continue

        while IFS= read -r addr; do
            [[ -z "$addr" ]] && continue
            local workers_json
            workers_json=$(curl -sf --max-time 8 "$pool_api/api/pools/${pool_id}/miners/${addr}/workers" 2>/dev/null || echo "")
            [[ -z "$workers_json" ]] && continue

            local wcount
            wcount=$(echo "$workers_json" | jq 'length' 2>/dev/null || echo 0)
            [[ "$wcount" == "0" ]] && continue

            if [[ "$header_printed" == "false" ]]; then
                echo ""
                echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                echo -e "  ${WHITE}WORKER BREAKDOWN${NC}"
                echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                printf "  %-6s %-18s %-14s %12s %8s %8s\n" "COIN" "MINER" "WORKER" "HASHRATE" "ACC%" "ONLINE"
                echo "  ────────────────────────────────────────────────────────────────────────"
                header_printed=true
            fi

            local addr_short="${addr:0:16}…"
            echo "$workers_json" | jq -r '.[] | [.worker, (.currentHashrate // 0), (.acceptanceRate // 0), (.connected // false)] | @tsv' 2>/dev/null \
            | while IFS=$'\t' read -r worker hr acc connected; do
                local hr_str
                if   (( $(echo "$hr >= 1000000000000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f TH/s" "$(echo "$hr/1000000000000" | bc -l)")
                elif (( $(echo "$hr >= 1000000000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f GH/s" "$(echo "$hr/1000000000" | bc -l)")
                elif (( $(echo "$hr >= 1000000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f MH/s" "$(echo "$hr/1000000" | bc -l)")
                elif (( $(echo "$hr >= 1000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f KH/s" "$(echo "$hr/1000" | bc -l)")
                else hr_str=$(printf "%.0f H/s" "$hr")
                fi
                local acc_str; acc_str=$(printf "%.1f%%" "$(echo "$acc * 100" | bc -l 2>/dev/null || echo 0)")
                local online_str="🔴"; [[ "$connected" == "true" ]] && online_str="🟢"
                printf "  %-6s %-18s %-14s %12s %8s %8s\n" "$symbol" "$addr_short" "${worker:0:14}" "$hr_str" "$acc_str" "$online_str"
            done
        done < <(echo "$miners_json" | jq -r '.[].address' 2>/dev/null)
    done < <(echo "$pools_json" | jq -r '.[].id' 2>/dev/null)

    if [[ "$header_printed" == "false" ]]; then
        echo ""
        echo "  No workers currently connected."
    fi
    echo ""
}

#===============================================================================
# LOG ERRORS COMMAND — filter service logs for ERROR/WARN/CRIT within a time window
#===============================================================================

cmd_log_errors() {
    # Consume optional "errors" subcommand so both forms work:
    #   spiralctl log errors          (window defaults to 1h)
    #   spiralctl log errors 24h      (window = 24h)
    #   spiralctl log 24h             (window = 24h, "errors" omitted)
    if [[ "${1:-}" == "errors" ]]; then
        shift
    fi

    # Optional service filter — accepts short aliases or full names.
    # If the next arg doesn't look like a time window, treat it as a service name.
    #   spiralctl log errors stratum 24h
    #   spiralctl log errors sentinel
    local service_filter=""
    local service_label="all services"
    if [[ -n "${1:-}" && ! "${1:-}" =~ ^[0-9]+[smhd]$ ]]; then
        local svc_arg="${1,,}"   # lowercase
        shift
        case "$svc_arg" in
            stratum|spiralstratum)         service_filter="spiralstratum" ;;
            sentinel|spiralsentinel)       service_filter="spiralsentinel" ;;
            dash|dashboard|spiraldash)     service_filter="spiraldash" ;;
            patroni|postgres|pg)           service_filter="patroni" ;;
            ha|watcher|ha-watcher)         service_filter="spiralpool-ha-watcher" ;;
            *)
                log_error "Unknown service '${svc_arg}'. Known: stratum, sentinel, dash, patroni, ha"
                exit 1
                ;;
        esac
        service_label="$service_filter"
    fi

    local window="${1:-1h}"

    # Validate window format (e.g. 30m, 2h, 24h, 7d)
    if ! [[ "$window" =~ ^[0-9]+[smhd]$ ]]; then
        log_error "Usage: spiralctl log errors [service] [window]  (e.g. 1h, 30m, 24h, 7d)"
        log_error "  Services: stratum, sentinel, dash, patroni, ha"
        exit 1
    fi

    # Convert to systemd --since format
    local since_arg=""
    local unit="${window: -1}"
    local val="${window%?}"
    case "$unit" in
        s) since_arg="${val} seconds ago" ;;
        m) since_arg="${val} minutes ago" ;;
        h) since_arg="${val} hours ago"   ;;
        d) since_arg="${val} days ago"    ;;
    esac

    # Build service list — filtered or full
    local services
    if [[ -n "$service_filter" ]]; then
        services=("$service_filter")
    else
        services=("spiralstratum" "spiralsentinel" "spiraldash" "patroni" "spiralpool-ha-watcher")
    fi
    local found=0

    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "  ${WHITE}LOG ERRORS — last ${window} / ${service_label}${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

    for svc in "${services[@]}"; do
        # Check if service exists
        if ! systemctl list-units --full --all "${svc}.service" 2>/dev/null | grep -q "${svc}.service"; then
            continue
        fi

        local lines
        lines=$(journalctl -u "${svc}.service" --since "$since_arg" --no-pager -q 2>/dev/null \
            | grep -iE '\b(error|warn(ing)?|crit(ical)?|fatal|panic|emerg)\b' \
            | tail -100)

        if [[ -n "$lines" ]]; then
            local count; count=$(echo "$lines" | wc -l | tr -d ' ')
            echo ""
            echo -e "  ${YELLOW}${svc}${NC}  (${count} entries)"
            echo "  ────────────────────────────────────────────────────────"
            echo "$lines" | while IFS= read -r line; do
                # Colour-code by severity
                if echo "$line" | grep -qiE '\b(crit(ical)?|fatal|panic|emerg)\b'; then
                    echo -e "  ${RED}${line}${NC}"
                elif echo "$line" | grep -qiE '\berror\b'; then
                    echo -e "  ${RED}${line}${NC}"
                else
                    echo -e "  ${YELLOW}${line}${NC}"
                fi
            done
            found=$((found + 1))
        fi
    done

    echo ""
    if [[ $found -eq 0 ]]; then
        echo -e "  ${GREEN}✓ No errors or warnings in the last ${window}${NC}"
    fi
    echo ""
}

#===============================================================================
# MINERS COMMAND — list connected miners, kick by IP
#===============================================================================

_get_admin_api_key() {
    # Read admin_api_key from config.yaml (V2 format: admin_api_key: "value")
    local key=""
    if [[ -f "$INSTALL_DIR/config/config.yaml" ]]; then
        key=$(grep -m1 'admin_api_key:' "$INSTALL_DIR/config/config.yaml" 2>/dev/null \
              | sed 's/.*admin_api_key:[[:space:]]*//' | tr -d '"' | tr -d "'" | tr -d '[:space:]')
    fi
    echo "$key"
}

cmd_miners() {
    local subcommand="${1:-list}"
    shift || true

    local pool_api="http://localhost:4000"
    local admin_key
    admin_key=$(_get_admin_api_key)

    case "$subcommand" in
        list|"")
            # Query /api/pools to get pool IDs, then list miners per pool
            local pools_json
            pools_json=$(curl -sf --max-time 8 "$pool_api/api/pools" 2>/dev/null || echo "")
            if [[ -z "$pools_json" ]] || ! command -v jq &>/dev/null; then
                log_error "Pool API unavailable or jq not installed"
                exit 1
            fi

            local header_printed=false
            while IFS= read -r pool_id; do
                [[ -z "$pool_id" ]] && continue
                local miners_json
                miners_json=$(curl -sf --max-time 8 "$pool_api/api/pools/${pool_id}/miners" 2>/dev/null || echo "")
                [[ -z "$miners_json" ]] && continue

                local symbol
                symbol=$(echo "$pools_json" | jq -r --arg id "$pool_id" '.[] | select(.id==$id) | .coin.symbol' 2>/dev/null || echo "$pool_id")

                local count
                count=$(echo "$miners_json" | jq 'length' 2>/dev/null || echo 0)
                [[ "$count" == "0" ]] && continue

                if [[ "$header_printed" == "false" ]]; then
                    echo ""
                    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                    echo -e "  ${WHITE}CONNECTED MINERS${NC}"
                    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                    printf "  %-8s %-20s %12s %10s %10s\n" "COIN" "ADDRESS" "HASHRATE" "SH/S" "SHARES"
                    echo "  ────────────────────────────────────────────────────────────────────────"
                    header_printed=true
                fi

                echo "$miners_json" | jq -r '.[] | [.address, (.hashrate // 0), (.sharesPerSecond // 0), (.sharesPerMinute // 0)] | @tsv' 2>/dev/null \
                | while IFS=$'\t' read -r addr hr sps shares; do
                    local addr_short="${addr:0:18}…"
                    local hr_str
                    if   (( $(echo "$hr >= 1000000000000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f TH/s" "$(echo "$hr/1000000000000" | bc -l)")
                    elif (( $(echo "$hr >= 1000000000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f GH/s" "$(echo "$hr/1000000000" | bc -l)")
                    elif (( $(echo "$hr >= 1000000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f MH/s" "$(echo "$hr/1000000" | bc -l)")
                    elif (( $(echo "$hr >= 1000" | bc -l 2>/dev/null || echo 0) )); then hr_str=$(printf "%.2f KH/s" "$(echo "$hr/1000" | bc -l)")
                    else hr_str=$(printf "%.0f H/s" "$hr")
                    fi
                    printf "  %-8s %-20s %12s %10.2f %10.0f\n" "$symbol" "$addr_short" "$hr_str" "$sps" "$shares"
                done
            done < <(echo "$pools_json" | jq -r '.[].id' 2>/dev/null)

            if [[ "$header_printed" == "false" ]]; then
                echo ""
                echo "  No miners currently connected."
            fi
            echo ""
            ;;

        kick)
            local ip="${1:-}"
            if [[ -z "$ip" ]]; then
                log_error "Usage: spiralctl miners kick <IP>"
                exit 1
            fi
            # Whitelist IP characters — blocks shell metacharacters before the request leaves.
            # Full validation is done server-side by net.ParseIP, this is defence-in-depth.
            if ! [[ "$ip" =~ ^[0-9a-fA-F:.]+$ ]]; then
                log_error "Invalid IP address: $ip"
                exit 1
            fi
            if [[ -z "$admin_key" ]]; then
                log_error "admin_api_key not found in config.yaml — cannot authenticate kick request"
                exit 1
            fi
            local result
            result=$(curl -sf --max-time 8 -X POST \
                -H "X-API-Key: $admin_key" \
                "$pool_api/api/admin/kick?ip=${ip}" 2>/dev/null || echo "")
            if [[ -z "$result" ]]; then
                log_error "Kick request failed — pool API unavailable or request rejected"
                exit 1
            fi
            local kicked
            kicked=$(echo "$result" | jq -r '.kicked // 0' 2>/dev/null || echo "?")
            if [[ "$kicked" == "0" ]]; then
                log_warn "No active sessions found for IP $ip"
            else
                log_success "Kicked $kicked session(s) from $ip — miner will reconnect automatically"
            fi
            ;;

        *)
            echo "Usage: spiralctl miners [list|kick <IP>]"
            echo ""
            echo "  list              Show all connected miners with hashrate and shares"
            echo "  kick <IP>         Disconnect all sessions from the given IP"
            echo ""
            echo "Examples:"
            echo "  spiralctl miners"
            echo "  spiralctl miners kick 192.168.1.50"
            exit 1
            ;;
    esac
}

#===============================================================================
# MINER NICK COMMAND — set/clear miner nicknames in sentinel config
#===============================================================================

cmd_miner() {
    local subcommand="${1:-}"
    shift || true

    case "$subcommand" in
        nick)
            local action_or_ip="${1:-}"
            shift || true

            # Resolve sentinel config path (same logic as cmd_config)
            local sentinel_cfg=""
            local candidate_paths=(
                "/home/${POOL_USER}/.spiralsentinel/config.json"
                "${INSTALL_DIR}/config/sentinel/config.json"
            )
            for p in "${candidate_paths[@]}"; do
                [[ -f "$p" ]] && sentinel_cfg="$p" && break
            done
            if [[ -z "$sentinel_cfg" ]]; then
                log_error "Sentinel config not found"
                exit 1
            fi

            case "$action_or_ip" in
                clear)
                    local ip="${1:-}"
                    if [[ -z "$ip" ]]; then
                        log_error "Usage: spiralctl miner nick clear <IP>"
                        exit 1
                    fi
                    check_root
                    # Remove the key using Python (preserves all other JSON)
                    sudo python3 - "$sentinel_cfg" "$ip" << 'PYEOF'
import sys, json
cfg_path, ip = sys.argv[1], sys.argv[2]
with open(cfg_path) as f:
    cfg = json.load(f)
nicks = cfg.get("miner_nicknames", {})
if ip in nicks:
    del nicks[ip]
    cfg["miner_nicknames"] = nicks
    with open(cfg_path, "w") as f:
        json.dump(cfg, f, indent=2)
    print(f"Cleared nickname for {ip}")
else:
    print(f"No nickname set for {ip}")
PYEOF
                    ;;

                list|"")
                    # List all current nicknames
                    python3 - "$sentinel_cfg" << 'PYEOF'
import sys, json
cfg_path = sys.argv[1]
with open(cfg_path) as f:
    cfg = json.load(f)
nicks = cfg.get("miner_nicknames", {})
if not nicks:
    print("  No miner nicknames configured.")
else:
    print()
    for ip, name in sorted(nicks.items()):
        print(f"  {ip:<20} → {name}")
    print()
PYEOF
                    ;;

                *)
                    # Treat as IP — next arg is the name
                    local ip="$action_or_ip"
                    local name="${1:-}"
                    if [[ -z "$name" ]]; then
                        log_error "Usage: spiralctl miner nick <IP> <name>"
                        exit 1
                    fi
                    check_root
                    sudo python3 - "$sentinel_cfg" "$ip" "$name" << 'PYEOF'
import sys, json
cfg_path, ip, name = sys.argv[1], sys.argv[2], sys.argv[3]
with open(cfg_path) as f:
    cfg = json.load(f)
if "miner_nicknames" not in cfg:
    cfg["miner_nicknames"] = {}
cfg["miner_nicknames"][ip] = name
with open(cfg_path, "w") as f:
    json.dump(cfg, f, indent=2)
print(f"Set nickname: {ip} → {name}")
print("Restart spiralsentinel to apply: sudo systemctl restart spiralsentinel")
PYEOF
                    ;;
            esac
            ;;

        group)
            local action_or_ip="${1:-}"
            shift || true

            # Resolve sentinel config path
            local sentinel_cfg=""
            local candidate_paths=(
                "/home/${POOL_USER}/.spiralsentinel/config.json"
                "${INSTALL_DIR}/config/sentinel/config.json"
            )
            for p in "${candidate_paths[@]}"; do
                [[ -f "$p" ]] && sentinel_cfg="$p" && break
            done
            if [[ -z "$sentinel_cfg" ]]; then
                log_error "Sentinel config not found"
                exit 1
            fi

            case "$action_or_ip" in
                list|"")
                    python3 - "$sentinel_cfg" << 'PYEOF'
import sys, json
cfg_path = sys.argv[1]
with open(cfg_path) as f:
    cfg = json.load(f)
groups = cfg.get("miner_groups", {})
if not groups:
    print("  No miner groups configured.")
else:
    # Invert: group → list of IPs
    by_group = {}
    for ip, grp in sorted(groups.items()):
        by_group.setdefault(grp, []).append(ip)
    print()
    for grp, ips in sorted(by_group.items()):
        print(f"  [{grp}]")
        for ip in ips:
            print(f"    {ip}")
    print()
PYEOF
                    ;;

                clear)
                    local ip="${1:-}"
                    if [[ -z "$ip" ]]; then
                        log_error "Usage: spiralctl miner group clear <IP>"
                        exit 1
                    fi
                    check_root
                    sudo python3 - "$sentinel_cfg" "$ip" << 'PYEOF'
import sys, json
cfg_path, ip = sys.argv[1], sys.argv[2]
with open(cfg_path) as f:
    cfg = json.load(f)
groups = cfg.get("miner_groups", {})
if ip in groups:
    del groups[ip]
    cfg["miner_groups"] = groups
    with open(cfg_path, "w") as f:
        json.dump(cfg, f, indent=2)
    print(f"Removed {ip} from its group")
else:
    print(f"No group set for {ip}")
PYEOF
                    ;;

                *)
                    # spiralctl miner group <IP> <group-name>
                    local ip="$action_or_ip"
                    local group_name="${1:-}"
                    if [[ -z "$group_name" ]]; then
                        log_error "Usage: spiralctl miner group <IP> <group-name>"
                        exit 1
                    fi
                    check_root
                    sudo python3 - "$sentinel_cfg" "$ip" "$group_name" << 'PYEOF'
import sys, json
cfg_path, ip, group_name = sys.argv[1], sys.argv[2], sys.argv[3]
with open(cfg_path) as f:
    cfg = json.load(f)
if "miner_groups" not in cfg:
    cfg["miner_groups"] = {}
cfg["miner_groups"][ip] = group_name
with open(cfg_path, "w") as f:
    json.dump(cfg, f, indent=2)
print(f"Set group: {ip} → {group_name}")
print("Restart spiralsentinel to apply: sudo systemctl restart spiralsentinel")
PYEOF
                    ;;
            esac
            ;;

        tag)
            local action_or_ip="${1:-list}"
            shift 2>/dev/null || true

            # Resolve sentinel config path (same logic as nick/group)
            local sentinel_cfg=""
            local tag_candidate_paths=(
                "/home/${POOL_USER}/.spiralsentinel/config.json"
                "${INSTALL_DIR}/config/sentinel/config.json"
            )
            for p in "${tag_candidate_paths[@]}"; do
                [[ -f "$p" ]] && sentinel_cfg="$p" && break
            done
            if [[ -z "$sentinel_cfg" ]]; then
                log_error "Sentinel config not found"
                exit 1
            fi

            case "$action_or_ip" in
                list|"")
                    python3 - "$sentinel_cfg" << 'PYEOF'
import sys, json
cfg_path = sys.argv[1]
with open(cfg_path) as f:
    cfg = json.load(f)
tags = cfg.get("miner_tags", {})
if not tags:
    print("  No miner tags configured.")
else:
    print()
    for ip, tag_list in sorted(tags.items()):
        print(f"  {ip}: {', '.join(tag_list)}")
    print()
PYEOF
                    ;;

                clear)
                    local ip="${1:-}"
                    if [[ -z "$ip" ]]; then
                        log_error "Usage: spiralctl miner tag clear <IP>"
                        exit 1
                    fi
                    check_root
                    sudo python3 - "$sentinel_cfg" "$ip" << 'PYEOF'
import sys, json
cfg_path, ip = sys.argv[1], sys.argv[2]
with open(cfg_path) as f:
    cfg = json.load(f)
tags = cfg.get("miner_tags", {})
if ip in tags:
    del tags[ip]
    cfg["miner_tags"] = tags
    with open(cfg_path, "w") as f:
        json.dump(cfg, f, indent=2)
    print(f"Removed tags from {ip}")
else:
    print(f"No tags set for {ip}")
PYEOF
                    ;;

                *)
                    # spiralctl miner tag <IP> <tag1,tag2,...>
                    local ip="$action_or_ip"
                    local tag_csv="${1:-}"
                    if [[ -z "$tag_csv" ]]; then
                        log_error "Usage: spiralctl miner tag <IP> <tag1,tag2,...>"
                        exit 1
                    fi
                    check_root
                    sudo python3 - "$sentinel_cfg" "$ip" "$tag_csv" << 'PYEOF'
import sys, json
cfg_path, ip, tag_csv = sys.argv[1], sys.argv[2], sys.argv[3]
with open(cfg_path) as f:
    cfg = json.load(f)
if "miner_tags" not in cfg:
    cfg["miner_tags"] = {}
tags = [t.strip() for t in tag_csv.split(",") if t.strip()]
cfg["miner_tags"][ip] = tags
with open(cfg_path, "w") as f:
    json.dump(cfg, f, indent=2)
print(f"Set tags: {ip} → {', '.join(tags)}")
PYEOF
                    ;;
            esac
            ;;

        *)
            echo "Usage: spiralctl miner <command>"
            echo ""
            echo "  nick  [list | <IP> <name> | clear <IP>]       Manage miner nicknames"
            echo "  group [list | <IP> <name> | clear <IP>]       Manage miner groups"
            echo "  tag   [list | <IP> <t1,t2> | clear <IP>]      Manage miner tags"
            echo ""
            echo "Examples:"
            echo "  spiralctl miner nick 192.168.1.50 \"Living Room Miner\""
            echo "  spiralctl miner nick list"
            echo "  spiralctl miner group 192.168.1.50 \"Garage\""
            echo "  spiralctl miner group list"
            echo "  spiralctl miner tag 192.168.1.50 \"asic,garage,s21\""
            echo "  spiralctl miner tag list"
            exit 1
            ;;
    esac
}

#===============================================================================
# CONFIG NOTIFY-TEST — send a test notification to every configured channel
#===============================================================================

cmd_config_notify_test() {
    local pool_home
    pool_home=$(getent passwd "$POOL_USER" 2>/dev/null | cut -d: -f6)
    pool_home="${pool_home:-/home/$POOL_USER}"

    local sentinel_cfg=""
    local candidate_paths=(
        "${pool_home}/.spiralsentinel/config.json"
        "${INSTALL_DIR}/config/sentinel/config.json"
    )
    for p in "${candidate_paths[@]}"; do
        [[ -f "$p" ]] && sentinel_cfg="$p" && break
    done

    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "  ${WHITE}NOTIFICATION TEST${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    if [[ -z "$sentinel_cfg" ]]; then
        log_error "Sentinel config not found — is Sentinel installed?"
        exit 1
    fi

    python3 - "$sentinel_cfg" << 'PYEOF' 2>&1
import sys, json, urllib.request, urllib.error, ssl, smtplib, time

cfg_path = sys.argv[1]
try:
    with open(cfg_path) as f:
        cfg = json.load(f)
except Exception as e:
    print(f"ERROR: Cannot read sentinel config: {e}")
    sys.exit(1)

RESET  = "\033[0m"
GREEN  = "\033[32m"
RED    = "\033[31m"
YELLOW = "\033[33m"
CYAN   = "\033[36m"
WHITE  = "\033[97m"

ts = time.strftime("%Y-%m-%d %H:%M UTC", time.gmtime())
any_configured = False
all_ok = True

# ── Discord ──────────────────────────────────────────────────────────────────
webhook = cfg.get("discord_webhook_url", "")
if webhook and "YOUR" not in webhook:
    any_configured = True
    sys.stdout.write(f"  Discord        ... ")
    sys.stdout.flush()
    payload = json.dumps({
        "embeds": [{
            "title": "🔔 Test Notification — Spiral Pool",
            "description": f"This is a test notification from `spiralctl config notify-test`.\nIf you see this, Discord alerts are working correctly.\n\n`{ts}`",
            "color": 0x00BFFF
        }]
    }).encode()
    try:
        req = urllib.request.Request(webhook, data=payload, headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10):
            pass
        print(f"{GREEN}✓ sent{RESET}")
    except Exception as e:
        print(f"{RED}✗ failed: {e}{RESET}")
        all_ok = False
else:
    print(f"  Discord        {YELLOW}— not configured{RESET}")

# ── Telegram ─────────────────────────────────────────────────────────────────
tg_token = cfg.get("telegram_bot_token", "")
tg_chat  = str(cfg.get("telegram_chat_id", ""))
if tg_token and tg_chat:
    any_configured = True
    sys.stdout.write(f"  Telegram       ... ")
    sys.stdout.flush()
    url = f"https://api.telegram.org/bot{tg_token}/sendMessage"
    text = (
        "*Spiral Pool — Test Notification*\n\n"
        "This is a test from `spiralctl config notify\\-test`\\.\n"
        "If you see this, Telegram alerts are working correctly\\.\n\n"
        f"`{ts}`"
    )
    payload = json.dumps({"chat_id": tg_chat, "text": text, "parse_mode": "MarkdownV2"}).encode()
    try:
        req = urllib.request.Request(url, data=payload, headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10):
            pass
        print(f"{GREEN}✓ sent{RESET}")
    except Exception as e:
        err = str(e).replace(tg_token, "***")
        print(f"{RED}✗ failed: {err}{RESET}")
        all_ok = False
else:
    print(f"  Telegram       {YELLOW}— not configured{RESET}")

# ── ntfy ─────────────────────────────────────────────────────────────────────
ntfy_url = cfg.get("ntfy_url", "")
if ntfy_url:
    any_configured = True
    sys.stdout.write(f"  ntfy           ... ")
    sys.stdout.flush()
    ntfy_token = cfg.get("ntfy_token", "")
    headers = {"Title": "Spiral Pool — Test Notification", "Content-Type": "text/plain"}
    if ntfy_token:
        headers["Authorization"] = f"Bearer {ntfy_token}"
    try:
        req = urllib.request.Request(ntfy_url, data=f"Test from spiralctl config notify-test ({ts})".encode(), headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=10):
            pass
        print(f"{GREEN}✓ sent{RESET}")
    except Exception as e:
        print(f"{RED}✗ failed: {e}{RESET}")
        all_ok = False
else:
    print(f"  ntfy           {YELLOW}— not configured{RESET}")

# ── Email / SMTP ──────────────────────────────────────────────────────────────
smtp_enabled = cfg.get("smtp_enabled", False)
smtp_host    = cfg.get("smtp_host", "")
smtp_to      = cfg.get("smtp_to", [])
if smtp_enabled and smtp_host and smtp_to:
    any_configured = True
    sys.stdout.write(f"  Email (SMTP)   ... ")
    sys.stdout.flush()
    from email.mime.text import MIMEText
    from email.mime.multipart import MIMEMultipart
    smtp_port  = int(cfg.get("smtp_port", 587))
    smtp_user  = cfg.get("smtp_username", "")
    smtp_pass  = cfg.get("smtp_password", "")
    smtp_from  = cfg.get("smtp_from", smtp_user)
    use_tls    = cfg.get("smtp_use_tls", True)
    recipients = smtp_to if isinstance(smtp_to, list) else [smtp_to]
    msg = MIMEMultipart("alternative")
    msg["Subject"] = "Spiral Pool — Test Notification"
    msg["From"]    = smtp_from
    msg["To"]      = ", ".join(recipients)
    msg.attach(MIMEText(f"This is a test notification from spiralctl config notify-test.\n\nIf you received this, email alerts are working correctly.\n\n{ts}", "plain"))
    try:
        _ctx = ssl.create_default_context()
        if use_tls:
            with smtplib.SMTP(smtp_host, smtp_port, timeout=15) as s:
                s.ehlo(); s.starttls(context=_ctx); s.ehlo()
                if smtp_user: s.login(smtp_user, smtp_pass)
                s.sendmail(smtp_from, recipients, msg.as_string())
        else:
            with smtplib.SMTP_SSL(smtp_host, smtp_port, timeout=15, context=_ctx) as s:
                if smtp_user: s.login(smtp_user, smtp_pass)
                s.sendmail(smtp_from, recipients, msg.as_string())
        print(f"{GREEN}✓ sent{RESET}")
    except smtplib.SMTPAuthenticationError as e:
        print(f"{RED}✗ auth failed: {e}{RESET}")
        all_ok = False
    except Exception as e:
        print(f"{RED}✗ failed: {e}{RESET}")
        all_ok = False
else:
    print(f"  Email (SMTP)   {YELLOW}— not configured{RESET}")

# ── XMPP ─────────────────────────────────────────────────────────────────────
xmpp_jid  = cfg.get("xmpp_jid", "")
xmpp_pass = cfg.get("xmpp_password", "")
xmpp_to   = cfg.get("xmpp_recipient", "")
if xmpp_jid and xmpp_pass and xmpp_to:
    any_configured = True
    sys.stdout.write(f"  XMPP           ... ")
    sys.stdout.flush()
    try:
        import slixmpp, asyncio

        async def _xmpp_test(jid, password, recipient, message, use_tls, is_muc):
            xmpp = slixmpp.ClientXMPP(jid, password)
            xmpp.register_plugin('xep_0030')
            xmpp.register_plugin('xep_0199')
            if is_muc:
                xmpp.register_plugin('xep_0045')
            success = [False]
            async def on_start(event):
                xmpp.send_presence()
                await xmpp.get_roster()
                if is_muc:
                    await xmpp.plugin['xep_0045'].join_muc_wait(recipient, 'SpiralSentinel')
                    xmpp.send_message(mto=recipient, mbody=message, mtype='groupchat')
                else:
                    xmpp.send_message(mto=recipient, mbody=message, mtype='chat')
                success[0] = True
                xmpp.disconnect()
            xmpp.add_event_handler("session_start", on_start)
            xmpp.connect(use_tls=cfg.get("xmpp_use_tls", True))
            try:
                await asyncio.wait_for(xmpp.disconnected, timeout=15)
            except asyncio.TimeoutError:
                xmpp.disconnect()
                return False
            return success[0]

        is_muc = bool(cfg.get("xmpp_muc", ""))
        ok = asyncio.run(_xmpp_test(
            xmpp_jid, xmpp_pass, xmpp_to,
            f"Spiral Pool test notification from spiralctl config notify-test ({ts})",
            cfg.get("xmpp_use_tls", True), is_muc
        ))
        if ok:
            print(f"{GREEN}✓ sent{RESET}")
        else:
            print(f"{RED}✗ failed (no confirmation from server){RESET}")
            all_ok = False
    except ImportError:
        print(f"{YELLOW}⚠ slixmpp not installed — install with: pip install slixmpp{RESET}")
    except Exception as e:
        print(f"{RED}✗ failed: {e}{RESET}")
        all_ok = False
else:
    print(f"  XMPP           {YELLOW}— not configured{RESET}")

# ── Summary ───────────────────────────────────────────────────────────────────
print("")
if not any_configured:
    print(f"  {YELLOW}No notification channels configured in sentinel config.{RESET}")
    print(f"  Run {WHITE}spiralctl config{RESET} to set up Discord, Telegram, email, or ntfy.")
elif all_ok:
    print(f"  {GREEN}✓ All configured channels delivered successfully{RESET}")
else:
    print(f"  {RED}✗ One or more channels failed — check credentials and connectivity{RESET}")
print("")
PYEOF
}

#===============================================================================
# CONFIG VALIDATE — dry-run config check without restarting services
#===============================================================================

cmd_config_validate() {
    local sentinel_cfg=""
    local candidate_paths=(
        "/home/${POOL_USER}/.spiralsentinel/config.json"
        "${INSTALL_DIR}/config/sentinel/config.json"
    )
    for p in "${candidate_paths[@]}"; do
        [[ -f "$p" ]] && sentinel_cfg="$p" && break
    done

    local config_yaml="${INSTALL_DIR}/config/config.yaml"
    local issues=0

    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "  ${WHITE}CONFIG VALIDATE${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    # --- config.yaml ---
    echo -e "  ${WHITE}config.yaml${NC} ($config_yaml)"
    if [[ ! -f "$config_yaml" ]]; then
        echo -e "    ${RED}✗ File not found${NC}"
        issues=$((issues + 1))
    else
        # YAML syntax check via Python — pass path via sys.argv to handle spaces/quotes safely
        local yaml_result
        yaml_result=$(python3 - "$config_yaml" << 'PYEOF' 2>/dev/null
import sys
path = sys.argv[1]
try:
    import yaml
    yaml.safe_load(open(path))
    print('ok')
except ImportError:
    print('no_yaml_module')
except Exception as e:
    print(f'error: {e}')
PYEOF
)

        case "$yaml_result" in
            ok)  echo -e "    ${GREEN}✓ YAML syntax valid${NC}" ;;
            no_yaml_module)
                # Fallback: check for obvious YAML issues with grep
                if grep -qP "^\s+\S" "$config_yaml" 2>/dev/null; then
                    echo -e "    ${YELLOW}⚠ PyYAML not installed — syntax check skipped${NC}"
                fi
                ;;
            error:*)
                echo -e "    ${RED}✗ YAML syntax error: ${yaml_result#error: }${NC}"
                issues=$((issues + 1))
                ;;
        esac

        # Check for placeholder wallet addresses
        if grep -q 'PENDING_GENERATION\|YOUR_.*_ADDRESS\|placeholder' "$config_yaml" 2>/dev/null; then
            echo -e "    ${YELLOW}⚠ Placeholder wallet address detected — update before mining${NC}"
            issues=$((issues + 1))
        fi

        # Check admin_api_key is set (v1: adminApiKey, v2: admin_api_key)
        if ! grep -qE 'admin_api_key:|adminApiKey:' "$config_yaml" 2>/dev/null || \
           grep -E 'admin_api_key:|adminApiKey:' "$config_yaml" 2>/dev/null | grep -q '""\|'"''"'\|: $'; then
            echo -e "    ${YELLOW}⚠ admin_api_key not set — miners kick and admin endpoints disabled${NC}"
            issues=$((issues + 1))
        fi

        echo -e "    ${GREEN}✓ config.yaml checks passed${NC}"
    fi

    echo ""

    # --- sentinel config.json ---
    echo -e "  ${WHITE}sentinel config.json${NC}"
    if ! systemctl is-enabled --quiet spiralsentinel 2>/dev/null; then
        echo -e "    ${CYAN}ℹ Sentinel not enabled — skipping config check${NC}"
    elif [[ -z "$sentinel_cfg" ]]; then
        echo -e "    ${YELLOW}⚠ Sentinel config not found${NC}"
        issues=$((issues + 1))
    else
        echo "    Path: $sentinel_cfg"
        local sentinel_result
        sentinel_result=$(python3 - "$sentinel_cfg" "$config_yaml" << 'PYEOF' 2>&1
import sys, json, re

sentinel_path = sys.argv[1]
stratum_yaml_path = sys.argv[2] if len(sys.argv) > 2 else ""
issues = []
try:
    with open(sentinel_path) as f:
        cfg = json.load(f)
except json.JSONDecodeError as e:
    print(f"FATAL: JSON syntax error: {e}")
    sys.exit(0)
except Exception as e:
    print(f"FATAL: Cannot read config: {e}")
    sys.exit(0)

# Placeholder wallet checks (only flag if explicitly set to a placeholder — empty is valid)
wallet = cfg.get("wallet_address", "")
if wallet in ("YOUR_DGB_ADDRESS", "YOUR_ADDRESS", "PENDING_GENERATION") or (wallet and "YOUR" in wallet.upper()):
    issues.append("wallet_address is a placeholder — update to your wallet address")

# Notification checks
discord = cfg.get("discord_webhook_url", "")
if discord and "YOUR" in discord:
    issues.append("discord_webhook_url contains placeholder")
if discord and not discord.startswith("https://discord.com/api/webhooks/"):
    issues.append("discord_webhook_url has unexpected format")

ntfy_url = cfg.get("ntfy_url", "")
if ntfy_url and not ntfy_url.startswith("https://"):
    issues.append("ntfy_url must use https://")

# Telegram: token and chat_id must both be set or both absent
tg_token = cfg.get("telegram_bot_token", "")
tg_chat  = cfg.get("telegram_chat_id", "")
if bool(tg_token) != bool(tg_chat):
    issues.append("telegram_bot_token and telegram_chat_id must both be set (one is missing)")

# XMPP: jid, password, and recipient must all be set together
xmpp_jid  = cfg.get("xmpp_jid", "")
xmpp_pass = cfg.get("xmpp_password", "")
xmpp_to   = cfg.get("xmpp_recipient", "")
xmpp_set  = [bool(xmpp_jid), bool(xmpp_pass), bool(xmpp_to)]
if any(xmpp_set) and not all(xmpp_set):
    missing = [k for k, v in [("xmpp_jid", xmpp_jid), ("xmpp_password", xmpp_pass), ("xmpp_recipient", xmpp_to)] if not v]
    issues.append(f"XMPP partially configured — missing: {', '.join(missing)}")

# pool_api_url basic format check
pool_url = cfg.get("pool_api_url", "")
if pool_url and not re.match(r'^https?://', pool_url):
    issues.append(f"pool_api_url does not look like a URL: {pool_url}")

smtp_enabled = cfg.get("smtp_enabled", False)
if smtp_enabled:
    if not cfg.get("smtp_host"):     issues.append("smtp_enabled but smtp_host is empty")
    if not cfg.get("smtp_to"):       issues.append("smtp_enabled but smtp_to is empty")
    if not cfg.get("smtp_username"): issues.append("smtp_enabled but smtp_username is empty")
    if not cfg.get("smtp_password"): issues.append("smtp_enabled but smtp_password is empty — email delivery will fail")

# Check required keys have sensible values
check_interval = cfg.get("check_interval", 120)
if not isinstance(check_interval, (int, float)) or check_interval < 10:
    issues.append(f"check_interval={check_interval} is unusually low (< 10s)")

# v2.0.0 new alert config range checks
disk_warn = cfg.get("disk_warn_pct", 85)
disk_crit = cfg.get("disk_critical_pct", 95)
if disk_warn >= disk_crit:
    issues.append(f"disk_warn_pct ({disk_warn}) must be less than disk_critical_pct ({disk_crit})")
if not (1 <= disk_warn <= 99) or not (1 <= disk_crit <= 99):
    issues.append(f"disk_warn_pct/disk_critical_pct must be between 1 and 99")

dry_mult = cfg.get("dry_streak_multiplier", 3)
if not isinstance(dry_mult, (int, float)) or dry_mult < 1:
    issues.append(f"dry_streak_multiplier={dry_mult} must be >= 1")

diff_pct = cfg.get("difficulty_alert_threshold_pct", 25)
if not isinstance(diff_pct, (int, float)) or not (1 <= diff_pct <= 100):
    issues.append(f"difficulty_alert_threshold_pct={diff_pct} must be between 1 and 100")

backup_days = cfg.get("backup_stale_days", 2)
if not isinstance(backup_days, (int, float)) or backup_days < 1:
    issues.append(f"backup_stale_days={backup_days} must be >= 1")

mempool_thresh = cfg.get("mempool_alert_threshold", 50000)
if not isinstance(mempool_thresh, (int, float)) or mempool_thresh < 100:
    issues.append(f"mempool_alert_threshold={mempool_thresh} is unusually low (< 100 txns)")

_hhmm = re.compile(r"^([01]\d|2[0-3]):([0-5]\d)$")
for i, win in enumerate(cfg.get("scheduled_maintenance_windows", [])):
    pfx = f"scheduled_maintenance_windows[{i}]"
    if not isinstance(win, dict):
        issues.append(f"{pfx}: must be an object with 'start' and 'end' keys"); continue
    for fld in ("start", "end"):
        v = win.get(fld, "")
        if not _hhmm.match(str(v)):
            issues.append(f"{pfx}.{fld}={v!r}: must be HH:MM (00:00–23:59)")
    days = win.get("days")
    if days is not None:
        if not isinstance(days, list) or not all(isinstance(d, int) and 0 <= d <= 6 for d in days):
            issues.append(f"{pfx}.days must be a list of integers 0–6 (0=Monday)")

# Cross-check: pool_admin_api_key in sentinel config must match admin_api_key in stratum config.yaml
sentinel_admin_key = cfg.get("pool_admin_api_key", "").strip()
if stratum_yaml_path:
    try:
        stratum_admin_key = ""
        with open(stratum_yaml_path) as f:
            for line in f:
                s = line.strip()
                if s.startswith("admin_api_key:") or s.startswith("adminApiKey:"):
                    stratum_admin_key = s.split(":", 1)[1].strip().strip('"').strip("'")
                    break
        if sentinel_admin_key and stratum_admin_key and sentinel_admin_key != stratum_admin_key:
            issues.append("pool_admin_api_key in sentinel config does not match admin_api_key in config.yaml — stratum kick will fail with 401")
        elif not sentinel_admin_key and stratum_admin_key:
            # Sentinel auto-discovers from config.yaml on startup, so this is fine — just informational
            print("INFO: pool_admin_api_key not set in sentinel config — will auto-discover from config.yaml")
    except Exception:
        pass  # stratum config.yaml not readable — skip cross-check

if issues:
    for i in issues:
        print(f"WARN: {i}")
else:
    print("OK")
PYEOF
)
        if echo "$sentinel_result" | grep -q "^FATAL:"; then
            echo -e "    ${RED}✗ ${sentinel_result#FATAL: }${NC}"
            issues=$((issues + 1))
        else
            if echo "$sentinel_result" | grep -q "^WARN:"; then
                echo "$sentinel_result" | grep "^WARN:" | while IFS= read -r line; do
                    echo -e "    ${YELLOW}⚠ ${line#WARN: }${NC}"
                done
                issues=$((issues + 1))
            fi
            if echo "$sentinel_result" | grep -q "^INFO:"; then
                echo "$sentinel_result" | grep "^INFO:" | while IFS= read -r line; do
                    echo -e "    ${CYAN}ℹ ${line#INFO: }${NC}"
                done
            fi
            if ! echo "$sentinel_result" | grep -q "^WARN:\|^FATAL:"; then
                echo -e "    ${GREEN}✓ Sentinel config valid${NC}"
            fi
        fi
    fi

    echo ""
    echo -e "${CYAN}────────────────────────────────────────────────────────────────────────${NC}"
    if [[ $issues -eq 0 ]]; then
        echo -e "  ${GREEN}✓ All checks passed — no issues found${NC}"
    else
        echo -e "  ${YELLOW}⚠ $issues issue(s) found — review above${NC}"
    fi
    echo ""
}

#===============================================================================
# MAIN
#===============================================================================

main() {
    local command="${1:-help}"
    shift || true

    case "$command" in
        # Core status & sync
        status)     cmd_status "$@" ;;
        sync)       cmd_sync "$@" ;;

        # Delegated operations
        logs)       cmd_logs "$@" ;;
        restart)    cmd_restart_all "$@" ;;
        wallet)     cmd_wallet "$@" ;;
        pause)      cmd_pause "$@" ;;
        stats)      cmd_stats "$@" ;;      # also: stats blocks [N]
        test)       cmd_test "$@" ;;
        scan)       cmd_scan "$@" ;;
        watch)      cmd_watch "$@" ;;
        update)     cmd_update "$@" ;;
        maintenance) cmd_maintenance "$@" ;;
        shutdown)    cmd_shutdown "$@" ;;

        # Data management (canonical)
        data)       cmd_data "$@" ;;
        # Backward-compat aliases for data subcommands
        backup)     cmd_backup "$@" ;;
        restore)    cmd_restore_pool "$@" ;;
        export)     cmd_export "$@" ;;
        # Backward-compat alias for stats subcommand
        blocks)     cmd_blocks "$@" ;;

        # Blockchain data transfer
        chain)      cmd_chain "$@" ;;

        # Miner management
        miners)     cmd_miners "$@" ;;
        miner)      cmd_miner "$@" ;;
        workers)    cmd_workers "$@" ;;
        log)        cmd_log_errors "$@" ;;

        # Node & coin management
        coin)       cmd_coin "$@" ;;
        node)       cmd_node "$@" ;;
        add-coin)    cmd_add_coin "$@" ;;
        remove-coin) cmd_remove_coin "$@" ;;
        coin-upgrade) cmd_coin_upgrade "$@" ;;

        # Infrastructure
        ha)         cmd_ha "$@" ;;        # also: ha vip [action]
        sync-addresses) cmd_sync_addresses "$@" ;;
        # Backward-compat aliases for ha subcommands
        vip)        cmd_vip "$@" ;;
        tor)        cmd_tor "$@" ;;

        # Configuration & notifications
        config)     cmd_config "$@" ;;
        webhook)    cmd_webhook "$@" ;;

        # Security (also: security fail2ban [...], security tor [...])
        security)   cmd_security "$@" ;;
        # Backward-compat aliases for security subcommands
        fail2ban)   cmd_fail2ban "$@" ;;

        # Go binary commands (pool-level)
        mining|pool|external|gdpr-delete)
            if [[ -x "$INSTALL_DIR/bin/spiralctl" ]]; then
                exec "$INSTALL_DIR/bin/spiralctl" "$command" "$@"
            else
                log_error "Go spiralctl binary not found at $INSTALL_DIR/bin/spiralctl"
                echo "Rebuild with: cd $INSTALL_DIR/stratum-src && go build -o $INSTALL_DIR/bin/spiralctl ./cmd/spiralctl"
                exit 1
            fi
            ;;

        help|--help|-h)  show_help ;;
        version|--version|-v)  show_version ;;
        *)
            log_error "Unknown command: $command"
            echo "Run 'spiralctl help' for usage."
            exit 1
            ;;
    esac
}

main "$@"
