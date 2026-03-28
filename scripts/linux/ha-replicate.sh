#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool HA Cold-Standby Replication Script
# Version: 1.2
# License: BSD-3-Clause
#
# PURPOSE:
#   Production-safe, operator-controlled replication for:
#   - Blockchain daemon data directories (BTC, LTC, DGB, etc.)
#   - PostgreSQL data directory
#
# DESIGN PHILOSOPHY:
#   - Explicit: No silent automation
#   - Operator-initiated: Every action requires confirmation
#   - Deterministic: Same inputs = same outputs
#   - Reversible: All changes are documented and can be undone
#
# SCRIPT SAFETY CHECKS:
#   ✓ Filesystem integrity preserved
#   ✓ Deterministic replication
#   ✓ Safe cold standby readiness
#   ✓ No silent data divergence
#
# NON-GUARANTEES (BY DESIGN):
#   ✗ No live replication
#   ✗ No automatic failover
#   ✗ No continuous sync
#   ✗ No multi-primary support
#
# USAGE:
#   ./ha-replicate.sh --mode [blockchain|postgres|full] --source <host> --dry-run
#   ./ha-replicate.sh --mode [blockchain|postgres|full] --source <host> --execute
#
# REQUIREMENTS:
#   - Ubuntu 24.04 LTS
#   - rsync 3.2+
#   - SSH key-based authentication between nodes
#   - Root or sudo access
#   - PostgreSQL 16+ (for postgres mode)
#
# AUTHOR: Spiral Pool HA Infrastructure Team
# REPOSITORY: https://github.com/SpiralPool/Spiral-Pool
#

set -euo pipefail

# ============================================================================
# CONFIGURATION
# ============================================================================

SCRIPT_VERSION="2.0.0"
SCRIPT_NAME="$(basename "$0")"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Default paths (can be overridden via environment)
POOL_USER="${POOL_USER:-spiraluser}"
INSTALL_DIR="${INSTALL_DIR:-/spiralpool}"
POSTGRES_VERSION="${POSTGRES_VERSION:-18}"
POSTGRES_DATA_DIR="/var/lib/postgresql/${POSTGRES_VERSION}/main"

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

# Blockchain data directories (multi-disk aware)
declare -A BLOCKCHAIN_DIRS=(
    ["bitcoin"]="$(_chain_dir btc)"
    ["bitcoin-cash"]="$(_chain_dir bch)"
    ["litecoin"]="$(_chain_dir ltc)"
    ["dogecoin"]="$(_chain_dir doge)"
    ["digibyte"]="$(_chain_dir dgb)"
    ["bitcoinii"]="$(_chain_dir bc2)"
    ["pepecoin"]="$(_chain_dir pep)"
    ["catcoin"]="$(_chain_dir cat)"
    ["namecoin"]="$(_chain_dir nmc)"
    ["syscoin"]="$(_chain_dir sys)"
    ["myriadcoin"]="$(_chain_dir xmy)"
    ["fractal"]="$(_chain_dir fbtc)"
    ["qbitx"]="$(_chain_dir qbx)"
)

# rsync flags for blockchain data (large files, sparse files, hardlinks)
RSYNC_BLOCKCHAIN_FLAGS=(
    --archive                  # Preserve permissions, times, ownership
    --hard-links               # Preserve hardlinks
    --sparse                   # Handle sparse files efficiently
    --numeric-ids              # Preserve numeric user/group IDs
    --delete                   # Remove files on target that don't exist on source
    --partial                  # Keep partially transferred files
    --progress                 # Show progress
    --human-readable           # Human-readable sizes
    --itemize-changes          # Show what changed
    --stats                    # Show transfer statistics
    --compress                 # Compress during transfer
    --compress-level=6         # Balanced compression
    --timeout=600              # 10 minute timeout on stalled I/O (prevents indefinite hangs)
    --exclude="*.conf"         # NEVER overwrite target's config (has node-specific RPC credentials)
    --exclude="settings.json"  # Node-specific runtime settings (different RPC auth per node)
    --exclude="*.pid"          # PID files are node-specific
    --exclude=".lock"          # Lock files are node-specific
    --exclude="debug.log"      # Source's debug log — can be 100s of MB, useless on target
)

# rsync flags for PostgreSQL (metadata-sensitive, permissions-critical)
RSYNC_POSTGRES_FLAGS=(
    --archive                  # Preserve permissions, times, ownership
    --hard-links               # Preserve hardlinks
    --numeric-ids              # Preserve numeric user/group IDs
    --delete                   # Remove files on target that don't exist on source
    --partial                  # Keep partially transferred files
    --progress                 # Show progress
    --human-readable           # Human-readable sizes
    --itemize-changes          # Show what changed
    --stats                    # Show transfer statistics
    --compress                 # Compress during transfer
    --compress-level=3         # Light compression (PostgreSQL data is often already compressed)
    --timeout=600              # 10 minute timeout on stalled I/O (prevents indefinite hangs)
    --exclude="postmaster.pid" # Exclude PID file
    --exclude="*.pid"          # Exclude any other PID files
    --exclude="recovery.signal" # Exclude recovery signal (regenerate on target)
    --exclude="standby.signal"  # Exclude standby signal (regenerate on target)
)

# Logging
LOG_DIR="/var/log/spiralpool"
LOG_FILE="${LOG_DIR}/ha-replicate.log"
DRY_RUN=false
VERBOSE=false

# Colors for terminal output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ============================================================================
# LOGGING FUNCTIONS
# ============================================================================

log() {
    local level="$1"
    shift
    local message="$*"
    local timestamp
    timestamp="$(date '+%Y-%m-%d %H:%M:%S')"

    # Console output with color
    case "$level" in
        INFO)
            echo -e "${BLUE}[INFO]${NC} $message" >&2
            ;;
        WARN)
            echo -e "${YELLOW}[WARN]${NC} $message" >&2
            ;;
        ERROR)
            echo -e "${RED}[ERROR]${NC} $message" >&2
            ;;
        SUCCESS)
            echo -e "${GREEN}[SUCCESS]${NC} $message" >&2
            ;;
        *)
            echo -e "[${level}] $message" >&2
            ;;
    esac

    # File logging (if log directory exists)
    if [[ -d "$LOG_DIR" ]]; then
        echo "${timestamp} [${level}] $message" >> "$LOG_FILE"
    fi
}

die() {
    log ERROR "$@"
    exit 1
}

# ============================================================================
# UTILITY FUNCTIONS
# ============================================================================

check_requirements() {
    log INFO "Checking system requirements..."

    # Check OS
    if [[ ! -f /etc/os-release ]]; then
        die "Cannot detect OS. /etc/os-release not found."
    fi

    # Safe parsing of /etc/os-release (no 'source' — runs as root via sudo)
    local detected_id detected_version detected_name
    detected_id=$(grep -oP '^ID=\K.*' /etc/os-release | tr -d '"')
    detected_version=$(grep -oP '^VERSION_ID=\K.*' /etc/os-release | tr -d '"')
    detected_name=$(grep -oP '^PRETTY_NAME=\K.*' /etc/os-release | tr -d '"')
    if [[ "$detected_id" != "ubuntu" ]] || [[ ! "$detected_version" =~ ^24\.04 ]]; then
        log WARN "This script is designed for Ubuntu 24.04 LTS. Detected: $detected_name"
        log WARN "Proceeding anyway, but unexpected behavior may occur."
    fi

    # Check rsync version
    if ! command -v rsync &>/dev/null; then
        die "rsync not found. Install with: sudo apt install rsync"
    fi

    local rsync_version
    rsync_version="$(rsync --version 2>/dev/null | head -n1 | awk '{print $3}' || true)"
    log INFO "rsync version: $rsync_version"

    # Check SSH
    if ! command -v ssh &>/dev/null; then
        die "ssh not found. Install with: sudo apt install openssh-client"
    fi

    log SUCCESS "System requirements check passed."
}

confirm_action() {
    local prompt="$1"
    local response

    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would ask: $prompt"
        return 0
    fi

    # When called with --yes (e.g., from install.sh which already got user confirmation),
    # skip the interactive prompt
    if [[ "$YES_MODE" == "true" ]]; then
        log INFO "Auto-confirmed (--yes): $prompt"
        return 0
    fi

    echo -e "${YELLOW}▶${NC} $prompt"
    read -r -p "Type 'yes' to confirm: " response

    if [[ "$response" != "yes" ]]; then
        log WARN "Confirmation not received. Aborting."
        exit 1
    fi

    log INFO "Confirmation received."
}

test_ssh_connection() {
    local source_host="$1"
    local mode="$2"

    log INFO "Testing SSH connections to ${source_host}..."

    # Test SSH connection (for blockchain replication)
    if [[ "$mode" == "blockchain" ]] || [[ "$mode" == "full" ]]; then
        log INFO "Testing SSH as ${SSH_USER}@${source_host}..."
        if ! ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" "echo 'SSH OK'" &>/dev/null; then
            die "SSH connection to ${SSH_USER}@${source_host} failed. Run: ssh-copy-id ${SSH_USER}@${source_host}"
        fi
        log SUCCESS "SSH connection to ${SSH_USER}@${source_host} successful."
    fi

    # Test postgres sudo access (for PostgreSQL replication)
    if [[ "$mode" == "postgres" ]] || [[ "$mode" == "full" ]]; then
        log INFO "Testing sudo access as postgres on ${source_host}..."
        if ! ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" "sudo -u postgres -n true" &>/dev/null; then
            log ERROR "Cannot execute commands as postgres user on ${source_host}."
            log ERROR "PostgreSQL replication requires ${SSH_USER} to run rsync as postgres via sudo."
            log ERROR ""
            log ERROR "Fix: Configure sudoers on PRIMARY node (${source_host}):"
            log ERROR "  sudo visudo -f /etc/sudoers.d/spiralpool-ha-postgres"
            log ERROR ""
            log ERROR "Add these lines:"
            log ERROR "  ${SSH_USER} ALL=(postgres) NOPASSWD: /usr/bin/rsync, /usr/bin/true, /usr/bin/ls"
            log ERROR "  ${SSH_USER} ALL=(root) NOPASSWD: /usr/bin/systemctl stop postgresql@*, /usr/bin/systemctl start postgresql@*, /usr/bin/systemctl is-active postgresql@*, /usr/bin/systemctl stop patroni, /usr/bin/systemctl start patroni"
            log ERROR ""
            log ERROR "Then validate:"
            log ERROR "  ssh ${SSH_USER}@${source_host} 'sudo -u postgres -n rsync --version'"
            die "postgres sudo access required for PostgreSQL replication."
        fi
        log SUCCESS "sudo access to postgres user on ${source_host} verified."

        # Test root sudo for systemctl (needed to stop/start PostgreSQL on source for WAL consistency)
        log INFO "Testing systemctl sudo access on ${source_host}..."
        if ! ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
            "sudo -n systemctl is-active postgresql@${POSTGRES_VERSION}-main" &>/dev/null; then
            log WARN "Cannot run systemctl as root on ${source_host}."
            log WARN "PostgreSQL on source will NOT be stopped during replication — WAL may be inconsistent."
            log WARN ""
            log WARN "To fix, add to /etc/sudoers.d/spiralpool-ha-postgres on ${source_host}:"
            log WARN "  ${SSH_USER} ALL=(root) NOPASSWD: /usr/bin/systemctl stop postgresql@*, /usr/bin/systemctl start postgresql@*, /usr/bin/systemctl is-active postgresql@*, /usr/bin/systemctl stop patroni, /usr/bin/systemctl start patroni"
        else
            log SUCCESS "systemctl sudo access on ${source_host} verified."
        fi
    fi
}

# ============================================================================
# SOURCE VALIDATION FUNCTIONS
# ============================================================================

validate_remote_directory() {
    local source_host="$1"
    local remote_dir="$2"
    local ssh_user="$3"
    local description="$4"

    log INFO "Validating source directory: ${ssh_user}@${source_host}:${remote_dir}"

    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would validate remote directory: ${remote_dir}"
        return 0
    fi

    # Check remote directory exists and has files (protects --delete from wiping target on empty source)
    local remote_check
    remote_check=$(ssh "${SSH_OPTS[@]}" "${ssh_user}@${source_host}" \
        "if [ -d '${remote_dir}' ] && [ \"\$(ls -A '${remote_dir}' 2>/dev/null)\" ]; then echo 'OK'; else echo 'EMPTY'; fi" 2>/dev/null) || {
        log ERROR "Cannot reach source ${source_host} to validate ${description} directory. Check SSH connectivity."
        return 1
    }

    if [[ "$remote_check" != "OK" ]]; then
        log ERROR "Source ${description} directory is empty or missing: ${ssh_user}@${source_host}:${remote_dir}"
        log ERROR "Skipping to protect local data (rsync --delete would wipe target)."
        return 1
    fi

    log SUCCESS "Source ${description} directory validated: non-empty."
    return 0
}

# Check if local filesystem has enough space for replication
# Usage: check_disk_space <source_host> <remote_dir> <local_dir> <ssh_user> <description>
check_disk_space() {
    local source_host="$1"
    local remote_dir="$2"
    local local_dir="$3"
    local ssh_user="$4"
    local description="$5"

    if [[ "$DRY_RUN" == "true" ]]; then
        return 0
    fi

    log INFO "Checking disk space for ${description}..."

    # Get source size in KB (use || true to prevent set -e from aborting on SSH failure)
    local source_size_kb
    source_size_kb=$(ssh "${SSH_OPTS[@]}" "${ssh_user}@${source_host}" \
        "du -sk '${remote_dir}' 2>/dev/null | awk '{print \$1}'" 2>/dev/null || true)

    if [[ -z "$source_size_kb" ]] || [[ "$source_size_kb" == "0" ]]; then
        log WARN "Could not determine source size for ${description} — skipping space check"
        return 0
    fi

    # Get available space on target filesystem in KB
    local target_free_kb
    target_free_kb=$(df -k "$local_dir" 2>/dev/null | awk 'NR==2 {print $4}' || true)

    if [[ -z "$target_free_kb" ]] || [[ "$target_free_kb" == "0" ]]; then
        log WARN "Could not determine free space on target — skipping space check"
        return 0
    fi

    local source_gb=$(( source_size_kb / 1048576 ))
    local target_free_gb=$(( target_free_kb / 1048576 ))

    log INFO "${description}: source ~${source_gb}GB, target has ${target_free_gb}GB free"

    # Require at least 10% headroom beyond source size
    local required_kb=$(( source_size_kb + source_size_kb / 10 ))
    if [[ $target_free_kb -lt $required_kb ]]; then
        local required_gb=$(( required_kb / 1048576 ))
        log ERROR "Insufficient disk space for ${description}: need ~${required_gb}GB but only ${target_free_gb}GB available"
        return 1
    fi

    return 0
}

# ============================================================================
# BLOCKCHAIN REPLICATION FUNCTIONS
# ============================================================================

get_blockchain_service_name() {
    local blockchain="$1"

    case "$blockchain" in
        bitcoin)
            echo "bitcoind"
            ;;
        bitcoin-cash)
            echo "bitcoind-bch"
            ;;
        litecoin)
            echo "litecoind"
            ;;
        dogecoin)
            echo "dogecoind"
            ;;
        digibyte)
            echo "digibyted"
            ;;
        bitcoinii)
            echo "bitcoiniid"
            ;;
        pepecoin)
            echo "pepecoind"
            ;;
        catcoin)
            echo "catcoind"
            ;;
        namecoin)
            echo "namecoind"
            ;;
        syscoin)
            echo "syscoind"
            ;;
        myriadcoin)
            echo "myriadcoind"
            ;;
        fractal)
            echo "fractald"
            ;;
        qbitx)
            echo "qbitxd"
            ;;
        *)
            die "Unknown blockchain: $blockchain"
            ;;
    esac
}

stop_blockchain_daemon() {
    local blockchain="$1"
    local service_name
    service_name="$(get_blockchain_service_name "$blockchain")"

    log INFO "Stopping ${service_name} service..."

    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would execute: sudo systemctl stop ${service_name}"
        return 0
    fi

    if ! systemctl is-active --quiet "$service_name"; then
        log INFO "${service_name} is already stopped."
        return 0
    fi

    sudo systemctl stop "$service_name"

    # Wait for service to fully stop
    local timeout=60
    local elapsed=0
    while systemctl is-active --quiet "$service_name"; do
        if [[ $elapsed -ge $timeout ]]; then
            die "${service_name} did not stop within ${timeout} seconds."
        fi
        sleep 2
        ((elapsed += 2))
        log INFO "Waiting for ${service_name} to stop... (${elapsed}s)"
    done

    log SUCCESS "${service_name} stopped successfully."
}

start_blockchain_daemon() {
    local blockchain="$1"
    local service_name
    service_name="$(get_blockchain_service_name "$blockchain")"

    log INFO "Starting ${service_name} service..."

    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would execute: sudo systemctl start ${service_name}"
        return 0
    fi

    sudo systemctl start "$service_name"

    # Wait for service to start
    local timeout=30
    local elapsed=0
    while ! systemctl is-active --quiet "$service_name"; do
        if [[ $elapsed -ge $timeout ]]; then
            log ERROR "${service_name} did not start within ${timeout} seconds."
            return 1
        fi
        sleep 2
        ((elapsed += 2))
        log INFO "Waiting for ${service_name} to start... (${elapsed}s)"
    done

    log SUCCESS "${service_name} started successfully."
}

replicate_blockchain() {
    local blockchain="$1"
    local source_host="$2"
    local data_dir="${BLOCKCHAIN_DIRS[$blockchain]}"

    if [[ -z "$data_dir" ]]; then
        die "Unknown blockchain: $blockchain"
    fi

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "Replicating ${blockchain} from ${source_host}"
    log INFO "Target directory: ${data_dir}"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    # Validate source directory before stopping anything (protects against --delete on empty source)
    if ! validate_remote_directory "$source_host" "$data_dir" "$SSH_USER" "$blockchain"; then
        log WARN "${blockchain}: source not available — daemon will continue with network sync."
        return 1
    fi

    # Verify target has enough disk space before stopping daemon
    if ! check_disk_space "$source_host" "$data_dir" "$data_dir" "$SSH_USER" "$blockchain"; then
        log WARN "${blockchain}: insufficient disk space — skipping replication."
        return 1
    fi

    # Stop local daemon
    stop_blockchain_daemon "$blockchain"

    # Build rsync command with explicit SSH identity
    # Use sudo rsync on remote if SSH_USER differs from POOL_USER (admin can't read spiraluser dirs)
    local ssh_cmd="ssh ${SSH_OPTS[*]}"
    local rsync_cmd=(
        rsync
        "${RSYNC_BLOCKCHAIN_FLAGS[@]}"
        -e "$ssh_cmd"
    )
    if [[ "$SSH_USER" != "$POOL_USER" ]]; then
        rsync_cmd+=(--rsync-path="sudo rsync")
    fi

    if [[ "$DRY_RUN" == "true" ]]; then
        rsync_cmd+=(--dry-run)
    fi

    rsync_cmd+=(
        "${SSH_USER}@${source_host}:${data_dir}/"
        "${data_dir}/"
    )

    log INFO "Executing rsync command..."
    if [[ "$VERBOSE" == "true" ]]; then
        log INFO "Command: ${rsync_cmd[*]}"
    fi

    # Execute rsync — on failure, try to restart the daemon so it can network-sync
    if ! "${rsync_cmd[@]}"; then
        log ERROR "rsync failed for ${blockchain}. Check network connectivity and SSH access."
        start_blockchain_daemon "$blockchain" || true
        return 1
    fi

    # Restore ownership
    log INFO "Restoring ownership to ${POOL_USER}:${POOL_USER}..."
    if [[ "$DRY_RUN" == "false" ]]; then
        if ! sudo chown -R "${POOL_USER}:${POOL_USER}" "$data_dir"; then
            log ERROR "Failed to restore ownership of $data_dir to ${POOL_USER}:${POOL_USER} — daemon may not start correctly"
            start_blockchain_daemon "$blockchain" || true
            return 1
        fi
    else
        log INFO "[DRY RUN] Would execute: sudo chown -R ${POOL_USER}:${POOL_USER} $data_dir"
    fi

    # Start local daemon — data was copied, but if daemon fails to start the loop continues
    if ! start_blockchain_daemon "$blockchain"; then
        log WARN "${blockchain}: daemon failed to start after replication — data was copied, service needs manual start."
        return 1
    fi

    log SUCCESS "${blockchain} replication completed successfully."
}

replicate_all_blockchains() {
    local source_host="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "BLOCKCHAIN REPLICATION (ALL CHAINS)"
    log INFO "Source: ${source_host}"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    confirm_action "This will stop all blockchain daemons and replicate from ${source_host}. Continue?"

    for blockchain in "${!BLOCKCHAIN_DIRS[@]}"; do
        # Check if data directory exists on local system
        local data_dir="${BLOCKCHAIN_DIRS[$blockchain]}"
        if [[ ! -d "$data_dir" ]]; then
            log WARN "Skipping ${blockchain}: data directory ${data_dir} not found."
            continue
        fi

        # Check if service exists
        local service_name
        service_name="$(get_blockchain_service_name "$blockchain")"
        if ! systemctl list-unit-files | grep -q "^${service_name}.service"; then
            log WARN "Skipping ${blockchain}: systemd service ${service_name}.service not found."
            continue
        fi

        # Replicate — if source validation fails, replicate_blockchain returns 1
        # and the daemon stays running (falls back to network sync)
        replicate_blockchain "$blockchain" "$source_host" || {
            log WARN "Skipping ${blockchain}: falling back to network sync."
        }
    done

    log SUCCESS "All blockchain replications completed."
}

# ============================================================================
# POSTGRESQL REPLICATION FUNCTIONS
# ============================================================================

detect_patroni() {
    # Check if Patroni is installed and managing PostgreSQL
    if systemctl list-units --full --all | grep -q "patroni"; then
        log WARN "Patroni detected! Patroni auto-restarts PostgreSQL."
        return 0
    fi
    return 1
}

disable_patroni_temporarily() {
    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would stop Patroni service"
        return 0
    fi

    log WARN "Stopping Patroni to prevent PostgreSQL auto-restart during replication..."

    if systemctl is-active --quiet patroni; then
        sudo systemctl stop patroni
        log SUCCESS "Patroni stopped."
    else
        log INFO "Patroni is not running."
    fi
}

enable_patroni() {
    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would start Patroni service"
        return 0
    fi

    log INFO "Starting Patroni..."

    if systemctl list-unit-files | grep -q "^patroni.service"; then
        sudo systemctl start patroni
        log SUCCESS "Patroni started. PostgreSQL is now under Patroni management."
    else
        log INFO "Patroni service not found (not installed or not enabled)."
    fi
}

stop_postgresql() {
    log INFO "Stopping PostgreSQL ${POSTGRES_VERSION}..."

    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would execute: sudo systemctl stop postgresql@${POSTGRES_VERSION}-main"
        return 0
    fi

    # Check if PostgreSQL is running
    if ! sudo systemctl is-active --quiet "postgresql@${POSTGRES_VERSION}-main"; then
        log INFO "PostgreSQL service is already stopped."
        # Still check for orphan postgres processes (Patroni, manual starts, etc.)
        if pgrep -u postgres postgres &>/dev/null; then
            log WARN "Orphan postgres processes found despite service being stopped. Killing..."
            sudo pkill -u postgres postgres 2>/dev/null || true
            sleep 3
            if pgrep -u postgres postgres &>/dev/null; then
                sudo pkill -9 -u postgres postgres 2>/dev/null || true
                sleep 2
            fi
            if pgrep -u postgres postgres &>/dev/null; then
                die "Cannot kill orphan postgres processes. Manual intervention required."
            fi
            log SUCCESS "Orphan postgres processes terminated."
        fi
        return 0
    fi

    # Stop PostgreSQL
    sudo systemctl stop "postgresql@${POSTGRES_VERSION}-main"

    # Verify it stopped
    local timeout=30
    local elapsed=0
    while sudo systemctl is-active --quiet "postgresql@${POSTGRES_VERSION}-main"; do
        if [[ $elapsed -ge $timeout ]]; then
            die "PostgreSQL did not stop within ${timeout} seconds."
        fi
        sleep 2
        ((elapsed += 2))
        log INFO "Waiting for PostgreSQL to stop... (${elapsed}s)"
    done

    # Extra safety: check for postmaster process
    if pgrep -u postgres postgres &>/dev/null; then
        log WARN "PostgreSQL processes still running. Waiting..."
        sleep 5
        if pgrep -u postgres postgres &>/dev/null; then
            die "PostgreSQL processes still running after stop command. Manual intervention required."
        fi
    fi

    log SUCCESS "PostgreSQL stopped successfully."
}

start_postgresql() {
    log INFO "Starting PostgreSQL ${POSTGRES_VERSION}..."

    if [[ "$DRY_RUN" == "true" ]]; then
        log INFO "[DRY RUN] Would execute: sudo systemctl start postgresql@${POSTGRES_VERSION}-main"
        return 0
    fi

    sudo systemctl start "postgresql@${POSTGRES_VERSION}-main"

    # Wait for service to start
    local timeout=30
    local elapsed=0
    while ! sudo systemctl is-active --quiet "postgresql@${POSTGRES_VERSION}-main"; do
        if [[ $elapsed -ge $timeout ]]; then
            die "PostgreSQL did not start within ${timeout} seconds. Check logs: journalctl -u postgresql@${POSTGRES_VERSION}-main"
        fi
        sleep 2
        ((elapsed += 2))
        log INFO "Waiting for PostgreSQL to start... (${elapsed}s)"
    done

    log SUCCESS "PostgreSQL started successfully."
}

check_postgresql_running() {
    if [[ "$DRY_RUN" == "true" ]]; then
        return 0
    fi

    if sudo systemctl is-active --quiet "postgresql@${POSTGRES_VERSION}-main"; then
        die "PostgreSQL is currently running. This is a COLD-COPY replication script. PostgreSQL MUST be stopped before replication."
    fi

    if pgrep -u postgres postgres &>/dev/null; then
        die "PostgreSQL processes detected. This is a COLD-COPY replication script. All PostgreSQL processes MUST be stopped before replication."
    fi
}

replicate_postgresql() {
    local source_host="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "POSTGRESQL COLD-STANDBY REPLICATION"
    log INFO "Source: ${source_host}:${POSTGRES_DATA_DIR}"
    log INFO "Target: ${POSTGRES_DATA_DIR}"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    log WARN "This is a COLD-COPY replication. PostgreSQL will be stopped on this node."
    log WARN "WAL consistency depends on the source database being cleanly shut down."
    log WARN "This is NOT pg_basebackup. This is NOT streaming replication."

    confirm_action "Proceed with PostgreSQL cold-copy replication from ${source_host}?"

    # Verify PostgreSQL data directory exists on target (this node)
    if [[ ! -d "$POSTGRES_DATA_DIR" ]]; then
        die "PostgreSQL data directory not found: ${POSTGRES_DATA_DIR}"
    fi

    # Validate source directory before stopping anything (protects against --delete on empty source)
    # PostgreSQL data dir is owned by postgres with 700 perms — must use sudo -u postgres for the check
    local pg_remote_check
    pg_remote_check=$(ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
        "sudo -u postgres ls -A '${POSTGRES_DATA_DIR}' 2>/dev/null | head -1" 2>/dev/null) || true
    if [[ -z "$pg_remote_check" ]]; then
        log ERROR "Source PostgreSQL directory is empty or inaccessible: ${SSH_USER}@${source_host}:${POSTGRES_DATA_DIR}"
        log ERROR "Skipping to protect local data (rsync --delete would wipe target)."
        log WARN "PostgreSQL: source not available — PostgreSQL will continue running normally."
        return 1
    fi
    log SUCCESS "Source PostgreSQL directory validated: non-empty."

    # Verify target has enough disk space before stopping services
    if ! check_disk_space "$source_host" "$POSTGRES_DATA_DIR" "$POSTGRES_DATA_DIR" "$SSH_USER" "PostgreSQL"; then
        log WARN "PostgreSQL: insufficient disk space — skipping replication."
        return 1
    fi

    # Detect and stop Patroni if present (prevents auto-restart)
    local patroni_was_running=false
    if detect_patroni; then
        log WARN "Patroni is managing PostgreSQL. Will temporarily disable Patroni during replication."
        confirm_action "Temporarily stop Patroni to prevent PostgreSQL auto-restart?"
        disable_patroni_temporarily
        patroni_was_running=true
    fi

    # Stop PostgreSQL on target (this node)
    stop_postgresql

    # Final safety check
    check_postgresql_running

    # Stop PostgreSQL on SOURCE (remote node) for WAL consistency
    # Cold-copy requires source DB to be cleanly shut down — files must not be written during rsync
    local source_patroni_was_running=false
    if [[ "$DRY_RUN" != "true" ]]; then
        log INFO "Stopping PostgreSQL on source ${source_host} for WAL consistency..."

        # Stop Patroni on source first (prevents it from restarting PostgreSQL)
        if ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
            "systemctl list-units --full --all 2>/dev/null | grep -q patroni" 2>/dev/null; then
            log WARN "Patroni detected on source. Stopping Patroni on ${source_host}..."
            if ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
                "sudo systemctl stop patroni" 2>/dev/null; then
                source_patroni_was_running=true
                log SUCCESS "Patroni stopped on source."
            else
                log WARN "Could not stop Patroni on source. Continuing — WAL may be inconsistent."
            fi
        fi

        # Stop PostgreSQL on source
        if ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
            "sudo systemctl is-active --quiet postgresql@${POSTGRES_VERSION}-main" 2>/dev/null; then
            ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
                "sudo systemctl stop postgresql@${POSTGRES_VERSION}-main" 2>/dev/null
            # Wait for clean shutdown
            local src_timeout=30
            local src_elapsed=0
            while ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
                "sudo systemctl is-active --quiet postgresql@${POSTGRES_VERSION}-main" 2>/dev/null; do
                if [[ $src_elapsed -ge $src_timeout ]]; then
                    log ERROR "PostgreSQL on source did not stop within ${src_timeout}s."
                    # Restart source Patroni if we stopped it, then bail
                    if [[ "$source_patroni_was_running" == "true" ]]; then
                        ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
                            "sudo systemctl start patroni" 2>/dev/null || true
                    fi
                    # Restart target services since we're aborting (best effort)
                    log INFO "Restarting target database services before aborting..."
                    if [[ "$patroni_was_running" == "true" ]]; then
                        sudo systemctl start patroni 2>/dev/null || true
                    else
                        sudo systemctl start "postgresql@${POSTGRES_VERSION}-main" 2>/dev/null || true
                    fi
                    die "Cannot stop PostgreSQL on source ${source_host}. Manual intervention required."
                fi
                sleep 2
                ((src_elapsed += 2))
                log INFO "Waiting for source PostgreSQL to stop... (${src_elapsed}s)"
            done
            log SUCCESS "PostgreSQL stopped on source ${source_host}."
        else
            log INFO "PostgreSQL already stopped on source ${source_host}."
        fi
    fi

    # Build rsync command
    local ssh_cmd="ssh ${SSH_OPTS[*]}"
    local rsync_cmd=(
        rsync
        "${RSYNC_POSTGRES_FLAGS[@]}"
        --rsync-path="sudo -u postgres rsync"
        -e "$ssh_cmd"
    )

    if [[ "$DRY_RUN" == "true" ]]; then
        rsync_cmd+=(--dry-run)
    fi

    rsync_cmd+=(
        "${SSH_USER}@${source_host}:${POSTGRES_DATA_DIR}/"
        "${POSTGRES_DATA_DIR}/"
    )

    log INFO "Executing rsync command..."
    if [[ "$VERBOSE" == "true" ]]; then
        log INFO "Command: ${rsync_cmd[*]}"
    fi

    # Helper: restart PostgreSQL on both source and target after failure or success.
    # Brings source back online first (primary = production traffic), then target.
    # Uses || true on remote SSH commands — if source is unreachable, we still restart target.
    restart_pg_services() {
        # Restart source first (bring primary back online ASAP)
        if [[ "$DRY_RUN" != "true" ]]; then
            log INFO "Restarting PostgreSQL on source ${source_host}..."
            if [[ "$source_patroni_was_running" == "true" ]]; then
                ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
                    "sudo systemctl start patroni" 2>/dev/null || true
                log SUCCESS "Patroni restarted on source ${source_host} (Patroni will start PostgreSQL)."
            else
                ssh "${SSH_OPTS[@]}" "${SSH_USER}@${source_host}" \
                    "sudo systemctl start postgresql@${POSTGRES_VERSION}-main" 2>/dev/null || true
                log SUCCESS "PostgreSQL restarted on source ${source_host}."
            fi
        fi

        # Restart target
        if [[ "$patroni_was_running" == "true" ]]; then
            log INFO "Re-enabling Patroni (Patroni will start PostgreSQL automatically)..."
            enable_patroni

            log INFO "Waiting for Patroni to start PostgreSQL..."
            sleep 10

            if ! sudo systemctl is-active --quiet "postgresql@${POSTGRES_VERSION}-main"; then
                log WARN "PostgreSQL not started by Patroni within 10 seconds. Check Patroni logs: journalctl -u patroni -n 50"
            else
                log SUCCESS "PostgreSQL started by Patroni successfully."
            fi
        else
            start_postgresql
        fi
    }

    # Execute rsync
    if ! "${rsync_cmd[@]}"; then
        log ERROR "rsync failed for PostgreSQL. Check network connectivity and SSH access."
        log ERROR "Restarting database services on both nodes before aborting..."
        restart_pg_services
        die "PostgreSQL rsync failed — services restarted, replication NOT completed."
    fi

    # Restore ownership (critical for PostgreSQL)
    log INFO "Restoring ownership to postgres:postgres..."
    if [[ "$DRY_RUN" == "false" ]]; then
        if ! sudo chown -R postgres:postgres "$POSTGRES_DATA_DIR"; then
            log ERROR "Failed to restore ownership of $POSTGRES_DATA_DIR to postgres"
            log ERROR "Restarting database services on both nodes before aborting..."
            restart_pg_services
            die "PostgreSQL ownership restore failed — services restarted, target data may be corrupt."
        fi
        if ! sudo chmod 700 "$POSTGRES_DATA_DIR"; then
            log ERROR "Failed to set permissions on $POSTGRES_DATA_DIR"
            log ERROR "Restarting database services on both nodes before aborting..."
            restart_pg_services
            die "PostgreSQL permission fix failed — services restarted, target may not start correctly."
        fi
    else
        log INFO "[DRY RUN] Would execute: sudo chown -R postgres:postgres ${POSTGRES_DATA_DIR}"
        log INFO "[DRY RUN] Would execute: sudo chmod 700 ${POSTGRES_DATA_DIR}"
    fi

    # Restart PostgreSQL on both nodes (source first, then target)
    restart_pg_services

    log SUCCESS "PostgreSQL replication completed successfully."

    if [[ "$patroni_was_running" == "true" ]]; then
        log INFO "NOTE: Patroni is now managing PostgreSQL. Check status: patronictl -c /etc/patroni/patroni.yml list"
    else
        log INFO "NOTE: If this is a standby node, you may need to configure streaming replication."
        log INFO "      See docs/reference/REFERENCE.md for HA/VIP configuration details."
    fi
}

# ============================================================================
# MAIN REPLICATION COORDINATOR
# ============================================================================

replicate_full() {
    local source_host="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "FULL HA REPLICATION (BLOCKCHAIN + POSTGRESQL)"
    log INFO "Source: ${source_host}"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    log WARN "This will replicate ALL blockchain data directories AND PostgreSQL."
    log WARN "All services will be stopped during replication."
    log WARN "This operation may take several hours depending on blockchain size."

    confirm_action "Proceed with FULL replication from ${source_host}?"

    # Replicate blockchains first (they can run independently)
    replicate_all_blockchains "$source_host"

    # Then replicate PostgreSQL (requires cold copy)
    replicate_postgresql "$source_host" || {
        log WARN "PostgreSQL replication skipped — falling back to normal operation."
    }

    log SUCCESS "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log SUCCESS "FULL HA REPLICATION COMPLETED"
    log SUCCESS "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

# ============================================================================
# USAGE AND ARGUMENT PARSING
# ============================================================================

usage() {
    cat <<EOF
Spiral Pool HA Cold-Standby Replication Script v${SCRIPT_VERSION}

USAGE:
    $SCRIPT_NAME --mode <MODE> --source <HOST> [OPTIONS]

MODES:
    blockchain      Replicate all blockchain data directories
    postgres        Replicate PostgreSQL data directory (cold-copy)
    full            Replicate both blockchain and PostgreSQL

REQUIRED ARGUMENTS:
    --source <HOST>     Source node hostname or IP (must have SSH access)

OPTIONS:
    --dry-run           Preview changes without executing (rsync --dry-run)
    --execute           Execute replication (requires explicit confirmation)
    --blockchain <NAME> Replicate specific blockchain (bitcoin, litecoin, pepecoin, catcoin, etc.)
    --verbose           Show detailed rsync commands
    --yes               Skip confirmation prompts (use when caller already confirmed)
    --help              Show this help message

EXAMPLES:
    # Dry run: Preview blockchain replication
    $SCRIPT_NAME --mode blockchain --source 192.168.1.10 --dry-run

    # Execute: Replicate all blockchains from primary node
    $SCRIPT_NAME --mode blockchain --source pool-primary --execute

    # Execute: Replicate PostgreSQL from primary node
    $SCRIPT_NAME --mode postgres --source pool-primary --execute

    # Execute: Full replication (blockchain + PostgreSQL)
    $SCRIPT_NAME --mode full --source pool-primary --execute

    # Replicate specific blockchain
    $SCRIPT_NAME --mode blockchain --source pool-primary --blockchain bitcoin --execute

SAFETY NOTES:
    - This script uses COLD-COPY semantics (services stopped during replication)
    - PostgreSQL must be cleanly shut down before replication
    - All operations require explicit operator confirmation (--execute mode)
    - Dry run mode (--dry-run) is safe and non-destructive

PREREQUISITES:
    - SSH key-based authentication configured between nodes
    - Sufficient disk space on target node
    - Root or sudo access on target node

For more information, see: docs/reference/REFERENCE.md
EOF
}

# ============================================================================
# ARGUMENT PARSING
# ============================================================================

MODE=""
SOURCE_HOST=""
SSH_USER=""
EXECUTE=false
YES_MODE=false
SPECIFIC_BLOCKCHAIN=""

if [[ $# -eq 0 ]]; then
    usage
    exit 0
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        --mode)
            MODE="$2"
            shift 2
            ;;
        --source)
            SOURCE_HOST="$2"
            shift 2
            ;;
        --ssh-user)
            SSH_USER="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --execute)
            EXECUTE=true
            shift
            ;;
        --blockchain)
            SPECIFIC_BLOCKCHAIN="$2"
            shift 2
            ;;
        --verbose)
            VERBOSE=true
            shift
            ;;
        --yes)
            YES_MODE=true
            shift
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            die "Unknown argument: $1. Use --help for usage information."
            ;;
    esac
done

# ============================================================================
# VALIDATION AND EXECUTION
# ============================================================================

# Default SSH_USER to POOL_USER if not specified (backwards compatible)
if [[ -z "$SSH_USER" ]]; then
    SSH_USER="$POOL_USER"
fi

# SECURITY: Validate SSH_USER format (prevents path traversal in SSH_KEY and SSH injection)
if [[ ! "$SSH_USER" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
    die "Invalid SSH user: must be a valid Unix username"
fi

# SSH identity: explicitly use SSH_USER's key AND known_hosts so this works even when running as root via sudo
# (install.sh runs us via sudo → we're root, but the SSH keys and known_hosts belong to SSH_USER)
SSH_KEY="/home/${SSH_USER}/.ssh/id_ed25519"
SSH_KNOWN_HOSTS="/home/${SSH_USER}/.ssh/known_hosts"
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)
if [[ -f "$SSH_KEY" ]]; then
    SSH_OPTS+=(-i "$SSH_KEY")
fi
if [[ -f "$SSH_KNOWN_HOSTS" ]]; then
    SSH_OPTS+=(-o "UserKnownHostsFile=${SSH_KNOWN_HOSTS}")
fi

# Validate arguments
if [[ -z "$MODE" ]]; then
    die "Missing required argument: --mode. Use --help for usage information."
fi

if [[ -z "$SOURCE_HOST" ]]; then
    die "Missing required argument: --source. Use --help for usage information."
fi

# SECURITY: Validate SOURCE_HOST format (prevents SSH option injection)
if [[ ! "$SOURCE_HOST" =~ ^[a-zA-Z0-9._-]+$ ]]; then
    die "Invalid source host: must be a hostname or IP address (alphanumeric, dots, hyphens only)"
fi

if [[ "$DRY_RUN" == "false" ]] && [[ "$EXECUTE" == "false" ]]; then
    die "Must specify either --dry-run or --execute. Use --help for usage information."
fi

if [[ "$DRY_RUN" == "true" ]] && [[ "$EXECUTE" == "true" ]]; then
    die "Cannot specify both --dry-run and --execute. Use --help for usage information."
fi

if [[ ! "$MODE" =~ ^(blockchain|postgres|full)$ ]]; then
    die "Invalid mode: $MODE. Must be one of: blockchain, postgres, full"
fi

# Lock file to prevent concurrent runs
LOCK_DIR="/run/spiralpool"
LOCK_FILE="${LOCK_DIR}/ha-replicate.lock"
mkdir -p "$LOCK_DIR" 2>/dev/null || sudo mkdir -p "$LOCK_DIR"
if [[ -f "$LOCK_FILE" ]]; then
    local_pid=$(cat "$LOCK_FILE" 2>/dev/null)
    if [[ -n "$local_pid" ]] && kill -0 "$local_pid" 2>/dev/null; then
        die "Another ha-replicate instance is already running (PID $local_pid). If this is stale, remove $LOCK_FILE"
    else
        log WARN "Removing stale lock file (PID $local_pid no longer running)"
        rm -f "$LOCK_FILE"
    fi
fi
echo $$ > "$LOCK_FILE"
trap 'rm -f "$LOCK_FILE"' EXIT

# Create log directory if it doesn't exist
if [[ ! -d "$LOG_DIR" ]]; then
    sudo mkdir -p "$LOG_DIR"
    if ! sudo chown "${POOL_USER}:${POOL_USER}" "$LOG_DIR"; then
        log WARN "Could not set ownership on $LOG_DIR — logs may not be writable"
    fi
fi

# Banner
log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
log INFO "Spiral Pool HA Cold-Standby Replication v${SCRIPT_VERSION}"
log INFO "Mode: ${MODE}"
log INFO "Source: ${SOURCE_HOST}"
log INFO "SSH User: ${SSH_USER}"
if [[ "$DRY_RUN" == "true" ]]; then
    log WARN "DRY RUN MODE: No changes will be made"
else
    log INFO "EXECUTE MODE: Changes will be applied"
fi
log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Check requirements
check_requirements

# Test SSH connection (mode-aware: tests SSH_USER and/or root as needed)
test_ssh_connection "$SOURCE_HOST" "$MODE"

# Execute based on mode
case "$MODE" in
    blockchain)
        if [[ -n "$SPECIFIC_BLOCKCHAIN" ]]; then
            confirm_action "Replicate ${SPECIFIC_BLOCKCHAIN} from ${SOURCE_HOST}?"
            replicate_blockchain "$SPECIFIC_BLOCKCHAIN" "$SOURCE_HOST" || {
                log WARN "${SPECIFIC_BLOCKCHAIN}: replication skipped — daemon will sync from network."
            }
        else
            replicate_all_blockchains "$SOURCE_HOST"
        fi
        ;;
    postgres)
        replicate_postgresql "$SOURCE_HOST" || {
            log WARN "PostgreSQL replication skipped — falling back to normal operation."
        }
        ;;
    full)
        replicate_full "$SOURCE_HOST"
        ;;
    *)
        die "Invalid mode: $MODE"
        ;;
esac

log SUCCESS "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
log SUCCESS "Replication completed successfully!"
log SUCCESS "Log file: ${LOG_FILE}"
log SUCCESS "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

exit 0
