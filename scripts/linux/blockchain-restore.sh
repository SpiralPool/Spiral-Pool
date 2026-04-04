#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool Blockchain Restore (Pull)
# Version: 1.2
# License: BSD-3-Clause
#
# PURPOSE:
#   Pull blockchain data FROM a remote machine to this machine.
#   This is a complete, self-contained operation — it handles daemon
#   shutdown on both sides, data transfer, ownership, and restart.
#
# USAGE:
#   sudo spiralctl chain restore       (interactive wizard)
#   sudo spiralpool-chain-restore      (same thing, legacy name)
#
# WORKFLOW:
#   1. Prompts for remote machine details
#   2. Detects which coins exist on the remote machine
#   3. For each selected coin:
#      a. Stops remote daemon (ensures data consistency for reading)
#      b. Stops local daemon (avoids conflicts while writing)
#      c. Rsyncs remote data → local (same directory structure)
#      d. Fixes local ownership
#      e. Restarts daemons on both sides
#
# NOTE: You only need ONE of export or restore — not both.
#   • Run "export" if you're sitting on the SYNCED machine (push)
#   • Run "restore" if you're sitting on the NEW machine (pull)
#

set -euo pipefail

# ============================================================================
# CONFIGURATION
# ============================================================================

SCRIPT_VERSION="2.2.1"
SCRIPT_NAME="$(basename "$0")"

POOL_USER="${POOL_USER:-spiraluser}"
INSTALL_DIR="${INSTALL_DIR:-/spiralpool}"

# Validate environment-sourced variables to prevent injection in remote SSH commands
if [[ ! "$POOL_USER" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    echo "ERROR: Invalid POOL_USER '${POOL_USER}' — must be alphanumeric" >&2
    exit 1
fi
if [[ ! "$INSTALL_DIR" =~ ^/[a-zA-Z0-9/_-]+$ ]]; then
    echo "ERROR: Invalid INSTALL_DIR '${INSTALL_DIR}' — must be a safe absolute path" >&2
    exit 1
fi

# Multi-disk support: check if CHAIN_MOUNT_POINT is set in coins.env
_CHAIN_MOUNT_POINT=""
if [[ -f "${INSTALL_DIR}/config/coins.env" ]]; then
    _CHAIN_MOUNT_POINT=$(grep -oP '^CHAIN_MOUNT_POINT="\K[^"]*' "${INSTALL_DIR}/config/coins.env" 2>/dev/null || echo "")
fi

# Resolve coin data directory (respects CHAIN_MOUNT_POINT if set and dir exists)
_chain_dir() {
    local coin_lower="$1"
    if [[ -n "$_CHAIN_MOUNT_POINT" && -d "${_CHAIN_MOUNT_POINT}/${coin_lower}" ]]; then
        echo "${_CHAIN_MOUNT_POINT}/${coin_lower}"
    else
        echo "${INSTALL_DIR}/${coin_lower}"
    fi
}

# Coin symbol → data directory mapping (multi-disk aware)
declare -A COIN_DIRS=(
    ["BTC"]="$(_chain_dir btc)"
    ["BCH"]="$(_chain_dir bch)"
    ["BC2"]="$(_chain_dir bc2)"
    ["LTC"]="$(_chain_dir ltc)"
    ["DOGE"]="$(_chain_dir doge)"
    ["DGB"]="$(_chain_dir dgb)"
    ["PEP"]="$(_chain_dir pep)"
    ["CAT"]="$(_chain_dir cat)"
    ["NMC"]="$(_chain_dir nmc)"
    ["SYS"]="$(_chain_dir sys)"
    ["XMY"]="$(_chain_dir xmy)"
    ["FBTC"]="$(_chain_dir fbtc)"
    ["QBX"]="$(_chain_dir qbx)"
)

# Coin symbol → display label
declare -A COIN_LABELS=(
    ["BTC"]="Bitcoin (BTC)"
    ["BCH"]="Bitcoin Cash (BCH)"
    ["BC2"]="Bitcoin II (BC2)"
    ["LTC"]="Litecoin (LTC)"
    ["DOGE"]="Dogecoin (DOGE)"
    ["DGB"]="DigiByte (DGB)"
    ["PEP"]="Pepecoin (PEP)"
    ["CAT"]="Catcoin (CAT)"
    ["NMC"]="Namecoin (NMC)"
    ["SYS"]="Syscoin (SYS)"
    ["XMY"]="Myriadcoin (XMY)"
    ["FBTC"]="Fractal Bitcoin (FBTC)"
    ["QBX"]="Q-BitX (QBX)"
)

# Coin symbol → systemd service name
declare -A COIN_SERVICES=(
    ["BTC"]="bitcoind"
    ["BCH"]="bitcoind-bch"
    ["BC2"]="bitcoiniid"
    ["LTC"]="litecoind"
    ["DOGE"]="dogecoind"
    ["DGB"]="digibyted"
    ["PEP"]="pepecoind"
    ["CAT"]="catcoind"
    ["NMC"]="namecoind"
    ["SYS"]="syscoind"
    ["XMY"]="myriadcoind"
    ["FBTC"]="fractald"
    ["QBX"]="qbitxd"
)

# rsync flags for blockchain data
RSYNC_FLAGS=(
    --archive
    --hard-links
    --sparse
    --numeric-ids
    --partial
    --progress
    --human-readable
    --stats
    --compress
    --compress-level=6
)

# Logging
LOG_DIR="/var/log/spiralpool"
LOG_FILE="${LOG_DIR}/blockchain-restore.log"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

# ============================================================================
# LOGGING
# ============================================================================

log() {
    local level="$1"
    shift
    local message="$*"
    local timestamp
    timestamp="$(date '+%Y-%m-%d %H:%M:%S')"

    case "$level" in
        INFO)    echo -e "${BLUE}[INFO]${NC} $message" >&2 ;;
        WARN)    echo -e "${YELLOW}[WARN]${NC} $message" >&2 ;;
        ERROR)   echo -e "${RED}[ERROR]${NC} $message" >&2 ;;
        SUCCESS) echo -e "${GREEN}[OK]${NC} $message" >&2 ;;
        *)       echo -e "[${level}] $message" >&2 ;;
    esac

    if [[ -d "$LOG_DIR" ]]; then
        echo "${timestamp} [${level}] $message" >> "$LOG_FILE" 2>/dev/null || true
    fi
}

die() {
    log ERROR "$@"
    exit 1
}

# ============================================================================
# LOCAL DAEMON MANAGEMENT
# ============================================================================

stop_local_daemon() {
    local coin="$1"
    local service="${COIN_SERVICES[$coin]}"

    if ! systemctl is-active --quiet "$service" 2>/dev/null; then
        log INFO "Local ${service} is already stopped."
        return 0
    fi

    log INFO "Stopping local ${service}..."
    sudo systemctl stop "$service"

    local timeout=60
    local elapsed=0
    while systemctl is-active --quiet "$service" 2>/dev/null; do
        if [[ $elapsed -ge $timeout ]]; then
            die "Local ${service} did not stop within ${timeout} seconds."
        fi
        sleep 2
        ((elapsed += 2))
    done

    log SUCCESS "Local ${service} stopped."
}

start_local_daemon() {
    local coin="$1"
    local service="${COIN_SERVICES[$coin]}"

    log INFO "Starting local ${service}..."
    sudo systemctl start "$service"

    local timeout=30
    local elapsed=0
    while ! systemctl is-active --quiet "$service" 2>/dev/null; do
        if [[ $elapsed -ge $timeout ]]; then
            log WARN "Local ${service} did not start within ${timeout}s — may need manual check."
            return 1
        fi
        sleep 2
        ((elapsed += 2))
    done

    log SUCCESS "Local ${service} started."
}

# ============================================================================
# REMOTE DAEMON MANAGEMENT
# ============================================================================

stop_remote_daemon() {
    local coin="$1"
    local ssh_target="$2"
    local service="${COIN_SERVICES[$coin]}"

    log INFO "Stopping remote ${service} (ensures data consistency)..."
    if ssh -o ConnectTimeout=10 "$ssh_target" \
         "sudo systemctl stop '$service' 2>/dev/null && echo STOPPED || echo SKIPPED" 2>/dev/null | grep -q "STOPPED"; then
        # Wait for the remote daemon to actually stop
        local timeout=60
        local elapsed=0
        while ssh -o ConnectTimeout=10 "$ssh_target" \
              "systemctl is-active --quiet '$service' 2>/dev/null && echo RUNNING || echo STOPPED" 2>/dev/null | grep -q "RUNNING"; do
            if [[ $elapsed -ge $timeout ]]; then
                log WARN "Remote ${service} did not stop within ${timeout}s — proceeding anyway."
                return 1
            fi
            sleep 2
            ((elapsed += 2))
        done
        log SUCCESS "Remote ${service} stopped."
    else
        log WARN "Could not stop remote ${service} — data may be inconsistent if daemon is running."
    fi
}

start_remote_daemon() {
    local coin="$1"
    local ssh_target="$2"
    local service="${COIN_SERVICES[$coin]}"

    log INFO "Starting remote ${service}..."
    if ssh -o ConnectTimeout=10 "$ssh_target" \
         "sudo systemctl start '$service' 2>/dev/null && echo STARTED || echo SKIPPED" 2>/dev/null | grep -q "STARTED"; then
        log SUCCESS "Remote ${service} started."
    else
        log WARN "Could not start remote ${service} — may need manual start on remote."
    fi
}

# ============================================================================
# STRATUM MANAGEMENT
# ============================================================================

STRATUM_WAS_RUNNING=false

# Cleanup handler: restart stratum if stopped on unexpected exit (Ctrl+C, set -e, etc.)
_cleanup_restore() {
    if [[ "${STRATUM_WAS_RUNNING}" == "true" ]]; then
        log WARN "Interrupted — restarting spiralstratum..."
        sudo systemctl start spiralstratum 2>/dev/null || true
    fi
}
trap _cleanup_restore EXIT INT TERM

stop_stratum_if_running() {
    if systemctl is-active --quiet spiralstratum 2>/dev/null; then
        log INFO "Stopping spiralstratum (will restart after transfer)..."
        sudo systemctl stop spiralstratum
        STRATUM_WAS_RUNNING=true
    fi
}

start_stratum_if_was_running() {
    if [[ "${STRATUM_WAS_RUNNING}" == "true" ]]; then
        log INFO "Restarting spiralstratum..."
        sudo systemctl start spiralstratum
        log SUCCESS "spiralstratum restarted."
    fi
}

# ============================================================================
# DETECTION
# ============================================================================

detect_remote_coins() {
    local ssh_target="$1"

    log INFO "Scanning remote machine for blockchain data..."

    local -a found=()
    for coin in "${!COIN_DIRS[@]}"; do
        local data_dir="${COIN_DIRS[$coin]}"
        # Check if directory exists AND has content on the remote machine
        local check
        check=$(ssh -o ConnectTimeout=10 "$ssh_target" \
            "if [ -d '${data_dir}' ] && [ \"\$(ls -A '${data_dir}' 2>/dev/null)\" ]; then echo 'YES'; else echo 'NO'; fi" 2>/dev/null) || continue

        if [[ "$check" == "YES" ]]; then
            found+=("$coin")
        fi
    done

    if [[ ${#found[@]} -eq 0 ]]; then
        die "No blockchain data found on remote machine at ${INSTALL_DIR}/. Is Spiral Pool installed there?"
    fi

    IFS=$'\n' REMOTE_COINS=($(sort <<<"${found[*]}")); unset IFS
}

# ============================================================================
# SSH CHECK
# ============================================================================

test_ssh_connection() {
    local ssh_target="$1"

    log INFO "Testing SSH connection to ${ssh_target}..."
    echo -e "${DIM}(You may be prompted for a password or SSH key passphrase)${NC}"

    if ! ssh -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
         "$ssh_target" "echo OK" >/dev/null 2>&1; then
        echo ""
        log ERROR "Cannot connect to ${ssh_target}"
        echo -e "  Check that:"
        echo -e "  - The IP address is correct"
        echo -e "  - SSH is running on the remote machine"
        echo -e "  - Your credentials are valid"
        echo -e "  - Port 22 is open"
        echo ""
        die "SSH connection failed."
    fi

    log SUCCESS "SSH connection to ${ssh_target} verified."

    # Check if remote user has sudo access (needed for daemon control)
    if ssh -o ConnectTimeout=10 "$ssh_target" "sudo -n true 2>/dev/null && echo SUDO_OK || echo SUDO_FAIL" 2>/dev/null | grep -q "SUDO_OK"; then
        log SUCCESS "Remote sudo access confirmed."
    else
        echo ""
        log WARN "Remote user may not have passwordless sudo."
        echo -e "  ${DIM}The remote daemon should be stopped manually before transfer${NC}"
        echo -e "  ${DIM}to avoid pulling inconsistent data. The transfer will still work,${NC}"
        echo -e "  ${DIM}but the blockchain may need to re-verify some blocks.${NC}"
        echo ""
    fi
}

# ============================================================================
# RESTORE LOGIC
# ============================================================================

restore_coin() {
    local coin="$1"
    local ssh_target="$2"
    local data_dir="${COIN_DIRS[$coin]}"
    local label="${COIN_LABELS[$coin]:-$coin}"
    local service="${COIN_SERVICES[$coin]}"

    echo ""
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "Restoring ${label}"
    log INFO "  Remote: ${ssh_target}:${data_dir}/"
    log INFO "  Local:  ${data_dir}/"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    # Stop remote daemon first (ensures data consistency for reading)
    stop_remote_daemon "$coin" "$ssh_target"

    # Stop local daemon (avoid conflicts while writing)
    stop_local_daemon "$coin"

    # Ensure local target directory exists
    if [[ ! -d "$data_dir" ]]; then
        log INFO "Creating local directory: ${data_dir}"
        sudo mkdir -p "$data_dir"
    fi

    # rsync from remote (same path on both sides)
    local start_time
    start_time=$(date +%s)

    log INFO "Starting rsync transfer..."
    if ! rsync "${RSYNC_FLAGS[@]}" \
         "${ssh_target}:${data_dir}/" \
         "${data_dir}/"; then
        log ERROR "rsync failed for ${label}!"
        log WARN "Restarting remote ${service}..."
        start_remote_daemon "$coin" "$ssh_target"
        log WARN "Restarting local ${service}..."
        start_local_daemon "$coin"
        return 1
    fi

    local end_time
    end_time=$(date +%s)
    local duration=$(( end_time - start_time ))
    local minutes=$(( duration / 60 ))
    local seconds=$(( duration % 60 ))

    # Fix local ownership
    log INFO "Fixing ownership: chown -R ${POOL_USER}:${POOL_USER} ${data_dir}/"
    if ! sudo chown -R "${POOL_USER}:${POOL_USER}" "${data_dir}/"; then
        log ERROR "Failed to fix ownership on ${data_dir}/ — daemon may not start correctly"
    fi

    # Restart remote daemon (we stopped it for consistency)
    start_remote_daemon "$coin" "$ssh_target"

    # Restart local daemon
    start_local_daemon "$coin"

    log SUCCESS "${label} restored successfully (${minutes}m ${seconds}s)"
    return 0
}

# ============================================================================
# INTERACTIVE MENU
# ============================================================================

show_coin_menu() {
    local -a coins=("${REMOTE_COINS[@]}")

    echo ""
    echo -e "${BOLD}Available blockchains on remote machine:${NC}"
    echo ""

    for i in "${!coins[@]}"; do
        local coin="${coins[$i]}"
        local label="${COIN_LABELS[$coin]:-$coin}"
        local local_dir="${COIN_DIRS[$coin]}"
        local status_hint=""

        if [[ -d "$local_dir" ]] && [[ "$(ls -A "$local_dir" 2>/dev/null)" ]]; then
            status_hint="${DIM}(local dir exists — will overwrite)${NC}"
        else
            status_hint="${DIM}(fresh restore)${NC}"
        fi

        printf "  ${CYAN}%2d${NC}) %-30s %s\n" $((i + 1)) "$label" "$status_hint"
    done

    echo ""
    printf "  ${CYAN}%2d${NC}) %s\n" 0 "Restore ALL available blockchains"
    echo ""
}

prompt_coin_selection() {
    local count=${#REMOTE_COINS[@]}
    local choice

    while true; do
        read -r -p "Select coin(s) to restore [1-${count}, 0 for all, comma-separated]: " choice

        if [[ "$choice" == "0" ]]; then
            SELECTED_COINS=("${REMOTE_COINS[@]}")
            return 0
        fi

        # Parse comma-separated selections
        IFS=',' read -ra selections <<< "$choice"
        SELECTED_COINS=()

        local valid=true
        for sel in "${selections[@]}"; do
            sel=$(echo "$sel" | tr -d ' ')
            if [[ "$sel" =~ ^[0-9]+$ ]] && [[ "$sel" -ge 1 ]] && [[ "$sel" -le "$count" ]]; then
                SELECTED_COINS+=("${REMOTE_COINS[$((sel - 1))]}")
            else
                echo -e "${RED}Invalid selection: ${sel}${NC}"
                valid=false
                break
            fi
        done

        if [[ "$valid" == "true" ]] && [[ ${#SELECTED_COINS[@]} -gt 0 ]]; then
            return 0
        fi
    done
}

# ============================================================================
# MAIN
# ============================================================================

main() {
    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════════════════════════════╗${NC}"
    printf -v _restore_title "%-62s" "       Spiral Pool Blockchain Restore (Pull) v${SCRIPT_VERSION}"
    echo -e "${BOLD}║${_restore_title}║${NC}"
    echo -e "${BOLD}║   Pull blockchain data FROM a remote machine TO this one     ║${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "  ${DIM}Each coin is transferred one at a time — its daemon is stopped${NC}"
    echo -e "  ${DIM}on both sides, data is synced, then the daemon is restarted${NC}"
    echo -e "  ${DIM}before moving to the next coin. You do NOT need to run export first.${NC}"
    echo ""

    # Must run as root (for service control + chown)
    if [[ $EUID -ne 0 ]]; then
        die "This script must be run as root (or with sudo)."
    fi

    # Prevent concurrent runs
    mkdir -p /run/spiralpool 2>/dev/null || true
    exec 9>/run/spiralpool/blockchain-restore.lock
    if ! flock -n 9; then
        die "Another blockchain restore is already running."
    fi

    # HA safety check — warn if this is an HA node
    if systemctl is-active --quiet keepalived 2>/dev/null || [[ -f "${INSTALL_DIR}/config/ha-enabled" ]]; then
        echo -e "${YELLOW}WARNING: This machine is part of an HA cluster.${NC}"
        echo -e "${YELLOW}Restoring will temporarily stop daemons and overwrite local blockchain data.${NC}"
        echo ""
        read -r -p "Continue on this HA node? (yes/no): " ha_confirm
        if [[ "$ha_confirm" != "yes" ]]; then
            echo "Aborted."
            exit 0
        fi
    fi

    # Check rsync available
    if ! command -v rsync &>/dev/null; then
        die "rsync not found. Install with: sudo apt install rsync"
    fi

    # Prompt for remote details
    echo -e "${BOLD}Remote source machine (the one with synced blockchain data):${NC}"
    echo ""

    local source_ip source_user

    read -r -p "  Remote IP address: " source_ip
    if [[ -z "$source_ip" ]]; then
        die "IP address is required."
    fi
    if [[ ! "$source_ip" =~ ^[a-zA-Z0-9._-]+$ ]]; then
        die "Invalid IP address or hostname: ${source_ip}"
    fi

    local default_ssh_user
    default_ssh_user="${SUDO_USER:-$(whoami)}"
    echo ""
    echo -e "  ${DIM}Enter the username you use to SSH into the REMOTE machine.${NC}"
    echo -e "  ${DIM}This is your admin login (e.g. ubuntu, root, spiralpool) — not spiraluser.${NC}"
    read -r -p "  SSH username on remote machine [${default_ssh_user}]: " source_user
    source_user="${source_user:-$default_ssh_user}"
    if [[ ! "$source_user" =~ ^[a-zA-Z0-9._-]+$ ]]; then
        die "Invalid SSH username: ${source_user}"
    fi

    local ssh_target="${source_user}@${source_ip}"

    echo ""

    # Test SSH connection
    test_ssh_connection "$ssh_target"

    # Detect what's available on the remote machine
    detect_remote_coins "$ssh_target"

    # Show coin menu and get selection
    show_coin_menu
    prompt_coin_selection

    # Confirm
    echo ""
    echo -e "${BOLD}Restore summary:${NC}"
    echo -e "  Source:  ${ssh_target}:${INSTALL_DIR}/"
    echo -e "  Coins:   ${#SELECTED_COINS[@]} selected"
    for coin in "${SELECTED_COINS[@]}"; do
        local local_dir="${COIN_DIRS[$coin]}"
        local overwrite=""
        if [[ -d "$local_dir" ]] && [[ "$(ls -A "$local_dir" 2>/dev/null)" ]]; then
            overwrite=" ${YELLOW}(will overwrite existing data)${NC}"
        fi
        echo -e "    - ${COIN_LABELS[$coin]:-$coin}${overwrite}"
    done
    echo ""
    echo -e "${YELLOW}Daemons will be temporarily stopped on BOTH machines during transfer.${NC}"
    echo ""

    read -r -p "Proceed with restore? (yes/no): " confirm
    if [[ "$confirm" != "yes" ]]; then
        echo "Aborted."
        exit 0
    fi

    # Stop local stratum
    stop_stratum_if_running

    # Restore each selected coin
    local success_count=0
    local fail_count=0
    local total_start
    total_start=$(date +%s)

    for coin in "${SELECTED_COINS[@]}"; do
        if restore_coin "$coin" "$ssh_target"; then
            success_count=$((success_count + 1))
        else
            fail_count=$((fail_count + 1))
        fi
    done

    # Restart stratum if it was running
    start_stratum_if_was_running

    local total_end
    total_end=$(date +%s)
    local total_duration=$(( total_end - total_start ))
    local total_min=$(( total_duration / 60 ))
    local total_sec=$(( total_duration % 60 ))

    # Final summary
    echo ""
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BOLD}Restore Complete${NC}"
    echo -e "  Succeeded: ${GREEN}${success_count}${NC}"
    if [[ $fail_count -gt 0 ]]; then
        echo -e "  Failed:    ${RED}${fail_count}${NC}"
    fi
    echo -e "  Total time: ${total_min}m ${total_sec}s"
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    if [[ $success_count -gt 0 ]]; then
        log SUCCESS "Blockchains restored. Local daemons are running and syncing."
        echo -e "  ${DIM}Check sync progress: spiralctl sync --watch${NC}"
    fi
    echo ""

    [[ $fail_count -eq 0 ]]
}

main "$@"
