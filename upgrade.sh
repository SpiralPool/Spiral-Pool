#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# Self-heal execute permission (needed after SCP from Windows).
chmod +x "${BASH_SOURCE[0]}" 2>/dev/null || true
# CRLF self-heal: fix Windows line endings if present (single-line, tolerates \r at EOL)
head -c50 "$0"|od -c|grep -q '\\r'&&{ find "$(dirname "$0")" -type f \( -name "*.sh" -o -name "*.py" -o -name "*.sql" -o -name "*.conf" -o -name "*.yaml" -o -name "*.yml" -o -name "*.service" -o -name "*.template" \) -exec sed -i 's/\r$//' {} +;exec bash "$0" "$@"; } #

#
# Spiral Pool Unified Upgrade Script
# Handles both local source updates and GitHub remote updates
#
# Usage: sudo ./upgrade.sh [OPTIONS]
#
# Modes:
#   (default)         Download and install from GitHub (production upgrades)
#   --local           Update from local source (development/manual sync)
#   --check           Check for updates without installing (for Sentinel integration)
#
# Component Selection:
#   --stratum-only    Only update stratum binary
#   --dashboard-only  Only update dashboard
#   --sentinel-only   Only update Spiral Sentinel
#   --no-stratum      Skip stratum update
#   --no-dashboard    Skip dashboard update
#   --no-sentinel     Skip sentinel update
#
# Options:
#   --force             Force upgrade even if already on latest version
#   --no-backup         Skip backup before upgrading
#   --update-services   Also update systemd service files (off by default)
#   --fix-config        Fix common config issues (coin names, durations)
#   --skip-start        Don't start services after update
#   --full              All extras: service files + config fixes
#   --auto              Unattended automatic upgrade (no prompts)
#
# ============================================================================
# DATABASE MIGRATIONS
# ============================================================================
#   - Database migrations are handled AUTOMATICALLY by the stratum binary
#   - When stratum starts, it runs CreatePoolTablesV2() for each coin
#   - This uses "ALTER TABLE ... ADD COLUMN IF NOT EXISTS" which is idempotent
#   - No manual SQL commands needed - just start the new binary
# ============================================================================
# WHAT GETS PRESERVED (NEVER TOUCHED):
# ============================================================================
#   - Blockchain data (/spiralpool/dgb/, /spiralpool/btc/, etc.)
#   - PostgreSQL database (share history, block records - auto-migrated)
#   - config.yaml (your pool settings - only NEW sections added)
#   - Sentinel data (/spiralpool/config/sentinel/):
#     - Achievements and lifetime stats (state.json)
#     - Historical data and trends (history.json)
#     - Miner database with nicknames (miners.json)
#     - Notification settings (config.json)
#   - SSL certificates (/spiralpool/certs/)
#   - Logs (/spiralpool/logs/)
#   - Wallet files (never touched)
#   - HA/VIP cluster configuration and token
# ============================================================================
# WHAT GETS PRESERVED (configs are NEVER modified by default):
# ============================================================================
#   - config.yaml — use --fix-config to apply compatibility fixes
#   - Dashboard config (~/.spiralpool/dashboard_config.json)
#   - Systemd service files — use --update-services to regenerate from templates
#   - Use --full to opt in to both config fixes + service file updates
# ============================================================================
# ADDING NEW COINS AFTER INSTALLATION:
# ============================================================================
#   When adding new coins after initial installation, you need to:
#   1. Add the coin's configuration to config.yaml
#   2. Create a wallet address for the new coin BEFORE starting mining
#
#   For wallet addresses:
#   - Coins with CLI support (DGB, BTC, BCH, BC2, LTC, DOGE, FBTC, QBX):
#     Run: spiralpool-wallet --coin <symbol>
#
#   - Coins with limited CLI support (NMC, SYS, XMY, PEP, CAT):
#     Create an address externally using the coin's official wallet software.
#     Check each coin's documentation for wallet download and instructions:
#       - NMC: https://www.namecoin.org
#       - SYS: https://syscoin.org
#       - XMY: https://myriadcoin.org
#       - PEP: https://github.com/pepecoinppc/pepecoin
#       - CAT: https://github.com/CatcoinCore/catcoincore
#
#   After creating the address, update config.yaml with:
#     address: "your_wallet_address_here"
# ============================================================================
# SECURITY - GitHub Authentication:
# ============================================================================
#   - All credential prompts are terminal-only (no GUI popups)
#   - Supports: GITHUB_TOKEN env var, GH_TOKEN env var, gh CLI auth, SSH keys
#   - Tokens are never displayed on screen (read -s)
#   - Credentials are cleared from memory after use
#   - HTTPS connections use TLS encryption for secure transmission
# ============================================================================

set -e
# NOTE: pipefail is NOT set globally because 29+ pipelines use grep|head/sed
# patterns where grep returning 1 (no match) is expected behavior.
# pipefail is used surgically where pipeline failures must be caught
# (e.g., database restore at line ~1140).

# Upgrade state tracking for automatic rollback
UPGRADE_IN_PROGRESS="false"
CURRENT_BACKUP_NAME=""
AUTO_ROLLBACK_ENABLED="true"

# Lock file — prevents concurrent install.sh + upgrade.sh runs
SPIRALPOOL_LOCK_FILE="/var/lock/spiralpool-operation.lock"

acquire_operation_lock() {
    local operation="$1"

    sudo mkdir -p /var/lock 2>/dev/null || true

    # Always clean up stale lock before attempting to acquire.
    # A lock is stale if: no .info file, no PID in .info, or that PID is no longer alive.
    if [[ -f "$SPIRALPOOL_LOCK_FILE" ]]; then
        local lock_pid=""
        [[ -f "${SPIRALPOOL_LOCK_FILE}.info" ]] && \
            lock_pid=$(grep -oP 'pid=\K[0-9]+' "${SPIRALPOOL_LOCK_FILE}.info" 2>/dev/null || true)
        if [[ -z "$lock_pid" ]] || ! kill -0 "$lock_pid" 2>/dev/null; then
            sudo rm -f "$SPIRALPOOL_LOCK_FILE" "${SPIRALPOOL_LOCK_FILE}.info" 2>/dev/null || true
        fi
    fi

    # Open the lock file — force-remove and retry once if permission denied
    # (can happen if a previous root process left it with restrictive permissions)
    if ! exec 200>"$SPIRALPOOL_LOCK_FILE" 2>/dev/null; then
        sudo rm -f "$SPIRALPOOL_LOCK_FILE" 2>/dev/null || true
        exec 200>"$SPIRALPOOL_LOCK_FILE" 2>/dev/null || {
            log_warn "Could not create lock file — continuing without lock"
            return 0
        }
    fi

    # Try to acquire exclusive lock (5s timeout)
    if ! flock -w 5 -x 200 2>/dev/null; then
        # Lock is held — check if it's a live process
        local lock_holder=""
        [[ -f "${SPIRALPOOL_LOCK_FILE}.info" ]] && \
            lock_holder=$(cat "${SPIRALPOOL_LOCK_FILE}.info" 2>/dev/null || true)
        local holder_pid=""
        [[ -n "$lock_holder" ]] && \
            holder_pid=$(echo "$lock_holder" | grep -oP 'pid=\K[0-9]+' || true)
        if [[ -n "$holder_pid" ]] && kill -0 "$holder_pid" 2>/dev/null; then
            log_error "Another Spiral Pool operation is in progress (pid $holder_pid: $lock_holder)"
            log_error "Wait for it to finish or kill pid $holder_pid first."
            return 1
        fi
        # PID is dead — stale flock, force clear and continue
        sudo rm -f "$SPIRALPOOL_LOCK_FILE" "${SPIRALPOOL_LOCK_FILE}.info" 2>/dev/null || true
        log_warn "Cleared stale lock — continuing"
    fi

    echo "operation=$operation pid=$$ started=$(date -Iseconds)" | \
        sudo tee "${SPIRALPOOL_LOCK_FILE}.info" > /dev/null 2>&1 || true
    return 0
}

release_operation_lock() {
    sudo rm -f "${SPIRALPOOL_LOCK_FILE}.info" 2>/dev/null || true
    exec 200>&- 2>/dev/null || true
}

# SECURITY: Trap to clean up temporary directories on exit/error
# Also triggers automatic rollback on failure if upgrade was in progress
cleanup_on_exit() {
    # Capture exit code FIRST — before any tests/assignments that clobber $?
    local exit_code=$?
    # Re-entrancy guard: prevent double execution when both signal and EXIT trap fire
    [[ "${_CLEANUP_ALREADY_RAN:-}" == "true" ]] && return
    _CLEANUP_ALREADY_RAN="true"

    # In check-only mode, no cleanup needed — exit silently to avoid
    # contaminating the JSON output with log messages on stdout
    [[ "${CHECK_ONLY:-false}" == "true" ]] && return

    set +e  # Don't abort cleanup/rollback on individual failures

    # Automatic rollback on failure during upgrade
    if [[ $exit_code -ne 0 ]] && [[ "$UPGRADE_IN_PROGRESS" == "true" ]] && [[ "$AUTO_ROLLBACK_ENABLED" == "true" ]]; then
        if [[ -n "$CURRENT_BACKUP_NAME" ]] && [[ -d "${BACKUP_DIR}/${CURRENT_BACKUP_NAME}" ]]; then
            echo ""
            echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
            echo -e "${RED}  UPGRADE FAILED - INITIATING AUTOMATIC ROLLBACK${NC}"
            echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
            echo ""
            echo -e "${YELLOW}Rolling back to: ${CURRENT_BACKUP_NAME}${NC}"

            # Perform automatic rollback (non-interactive)
            AUTO_MODE="true"
            if rollback_to_backup "$CURRENT_BACKUP_NAME"; then
                echo ""
                echo -e "${YELLOW}System has been rolled back to previous state.${NC}"
                echo -e "${YELLOW}Review logs and try the upgrade again.${NC}"
            else
                echo ""
                echo -e "${RED}CRITICAL: Automatic rollback also failed!${NC}"
                echo -e "${RED}Manual intervention required. Backup at: ${BACKUP_DIR}/${CURRENT_BACKUP_NAME}${NC}"
            fi
        else
            echo ""
            echo -e "${RED}Upgrade failed but no backup available for rollback.${NC}"
            # At minimum, restart services that were running before the failed upgrade
            if [[ ${#SERVICES_WERE_RUNNING[@]} -gt 0 ]]; then
                echo -e "${YELLOW}Attempting to restart services that were running before upgrade...${NC}"
                for svc in "${SERVICES_WERE_RUNNING[@]}"; do
                    systemctl start "$svc" 2>/dev/null || true
                done
            fi
        fi
    fi

    # Always clear maintenance mode on exit so alerts resume
    clear_alert_suppression 2>/dev/null || true

    # Clean up temp directories securely
    if [[ -n "$TEMP_DIR" ]] && [[ -d "$TEMP_DIR" ]]; then
        rm -rf "$TEMP_DIR" 2>/dev/null || true
    fi
    # Clean up credential directory (separate from TEMP_DIR for git clone compatibility)
    if [[ -n "$CRED_DIR" ]] && [[ -d "$CRED_DIR" ]]; then
        rm -rf "$CRED_DIR" 2>/dev/null || true
    fi
    exit $exit_code
}
trap cleanup_on_exit EXIT INT TERM

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
DIM='\033[2m'
NC='\033[0m' # No Color

# Configuration
readonly DEFAULT_INSTALL_DIR="/spiralpool"
INSTALL_DIR="$DEFAULT_INSTALL_DIR"
BACKUP_DIR="${INSTALL_DIR}/backups"
GITHUB_REPO="https://github.com/SpiralPool/Spiral-Pool.git"
TEMP_DIR=""
CRED_DIR=""  # Separate temp dir for GitHub credentials (cleaned on exit)
PROJECT_ROOT=""

# Service names (auto-detected)
STRATUM_SERVICE=""
DASHBOARD_SERVICE=""
SENTINEL_SERVICE=""
HEALTH_SERVICE=""
DASHBOARD_PORT="1618"

# Detected pool user
POOL_USER=""

# Version tracking
CURRENT_VERSION=""
TARGET_VERSION=""

# Operation flags
FETCH_LATEST=true   # Default: download from GitHub
USE_LOCAL=false     # --local flag: use local source instead
FORCE_UPGRADE=false
SKIP_BACKUP=false
CHECK_ONLY=false
AUTO_MODE=false
UPDATE_STRATUM=true
UPDATE_DASHBOARD=true
UPDATE_SENTINEL=true
UPDATE_SERVICES=false
FIX_CONFIG=false
SKIP_START=false

# Track what services were running before upgrade
SERVICES_WERE_RUNNING=()

# Detect system architecture once (dpkg returns "amd64" or "arm64")
SYSTEM_ARCH=$(dpkg --print-architecture 2>/dev/null || echo "amd64")

# =============================================================================
# Helper Functions
# =============================================================================

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_banner() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${CYAN}  SPIRAL POOL - UPGRADE UTILITY${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root (sudo ./upgrade.sh)"
        exit 1
    fi
}

# =============================================================================
# Service Detection
# =============================================================================

detect_services() {
    log_info "Detecting installed services..."

    # Check for spiralstratum vs stratum (check service file directly — list-unit-files always returns 0)
    if [[ -f "/etc/systemd/system/spiralstratum.service" ]]; then
        STRATUM_SERVICE="spiralstratum"
    elif [[ -f "/etc/systemd/system/stratum.service" ]]; then
        STRATUM_SERVICE="stratum"
    else
        STRATUM_SERVICE="spiralstratum"
    fi

    # Check for spiraldash vs spiral-dashboard
    if [[ -f "/etc/systemd/system/spiraldash.service" ]]; then
        DASHBOARD_SERVICE="spiraldash"
    elif [[ -f "/etc/systemd/system/spiral-dashboard.service" ]]; then
        DASHBOARD_SERVICE="spiral-dashboard"
    else
        DASHBOARD_SERVICE="spiraldash"
    fi

    # Check for spiralsentinel vs spiral-sentinel
    if [[ -f "/etc/systemd/system/spiralsentinel.service" ]]; then
        SENTINEL_SERVICE="spiralsentinel"
    elif [[ -f "/etc/systemd/system/spiral-sentinel.service" ]]; then
        SENTINEL_SERVICE="spiral-sentinel"
    else
        SENTINEL_SERVICE="spiralsentinel"
    fi

    # Health monitor
    if [[ -f "/etc/systemd/system/spiralpool-health.service" ]]; then
        HEALTH_SERVICE="spiralpool-health"
    fi

    echo -e "  Stratum:   ${GREEN}$STRATUM_SERVICE${NC}"
    echo -e "  Dashboard: ${GREEN}$DASHBOARD_SERVICE${NC}"
    echo -e "  Sentinel:  ${GREEN}$SENTINEL_SERVICE${NC}"
    [[ -n "$HEALTH_SERVICE" ]] && echo -e "  Health:    ${GREEN}$HEALTH_SERVICE${NC}"
}

# =============================================================================
# Pool User Detection
# =============================================================================

detect_pool_user() {
    # Primary method: owner of install directory
    if [[ -d "$INSTALL_DIR" ]]; then
        POOL_USER=$(stat -c '%U' "$INSTALL_DIR" 2>/dev/null || ls -ld "$INSTALL_DIR" | awk '{print $3}')
    fi

    # If directory is owned by root, check config file ownership
    if [[ -z "$POOL_USER" ]] || [[ "$POOL_USER" == "root" ]]; then
        if [[ -f "$INSTALL_DIR/config/config.yaml" ]]; then
            POOL_USER=$(stat -c '%U' "$INSTALL_DIR/config/config.yaml" 2>/dev/null || echo "")
        fi
    fi

    # Check systemd service file for configured user
    if [[ -z "$POOL_USER" ]] || [[ "$POOL_USER" == "root" ]]; then
        if [[ -f "/etc/systemd/system/${STRATUM_SERVICE}.service" ]]; then
            local service_user
            service_user=$(grep -oP '^User=\K.+' "/etc/systemd/system/${STRATUM_SERVICE}.service" 2>/dev/null || echo "")
            if [[ -n "$service_user" ]] && [[ "$service_user" != "root" ]] && id "$service_user" &>/dev/null; then
                POOL_USER="$service_user"
            fi
        fi
    fi

    # Fallback: check for common pool user accounts
    if [[ -z "$POOL_USER" ]] || [[ "$POOL_USER" == "root" ]]; then
        for user in spiraluser; do
            if id "$user" &>/dev/null; then
                POOL_USER="$user"
                break
            fi
        done
    fi

    # Final validation
    if [[ -z "$POOL_USER" ]] || [[ "$POOL_USER" == "root" ]]; then
        log_error "Could not detect pool user. Please ensure /spiralpool is owned by your pool user."
        exit 1
    fi

    # Validate pool user
    if ! id "$POOL_USER" &>/dev/null; then
        log_error "Pool user '$POOL_USER' does not exist"
        exit 1
    fi

    if [[ "$POOL_USER" == "root" ]]; then
        log_error "Pool should not run as root user"
        exit 1
    fi

    # SECURITY: Validate username format (prevents injection in sudoers/sshd heredocs)
    if [[ ! "$POOL_USER" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
        log_error "Pool user '$POOL_USER' has invalid characters — must be a valid Unix username"
        exit 1
    fi

    log_info "Detected pool user: ${POOL_USER}"
}

# =============================================================================
# Source Detection (for local mode)
# =============================================================================

detect_source_directory() {
    local SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

    # Try to find project root relative to script location
    if [[ -f "$SCRIPT_DIR/src/stratum/go.mod" ]] || [[ -f "$SCRIPT_DIR/src/dashboard/dashboard.py" ]]; then
        PROJECT_ROOT="$SCRIPT_DIR"
    elif [[ -f "$SCRIPT_DIR/../src/stratum/go.mod" ]] || [[ -f "$SCRIPT_DIR/../src/dashboard/dashboard.py" ]]; then
        PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
    elif [[ -f "$INSTALL_DIR/src/stratum/go.mod" ]]; then
        PROJECT_ROOT="$INSTALL_DIR"
    elif [[ -f "/spiralpool/src/stratum/go.mod" ]]; then
        PROJECT_ROOT="/spiralpool"
    fi

    if [[ -z "$PROJECT_ROOT" ]] && [[ "$USE_LOCAL" == "true" ]]; then
        log_error "Cannot find Spiral Pool source directory"
        log_info "Ensure source files are synced, or omit --local to download from GitHub"
        exit 1
    fi

    [[ -n "$PROJECT_ROOT" ]] && log_info "Source directory: $PROJECT_ROOT"
}

# =============================================================================
# Version Detection
# =============================================================================

detect_current_version() {
    if [[ -f "${INSTALL_DIR}/VERSION" ]] && [[ ! -L "${INSTALL_DIR}/VERSION" ]]; then
        CURRENT_VERSION=$(tr -d '[:space:]' < "${INSTALL_DIR}/VERSION")
    elif [[ -f "${INSTALL_DIR}/bin/spiralstratum" ]]; then
        CURRENT_VERSION=$("${INSTALL_DIR}/bin/spiralstratum" --version 2>/dev/null | grep -oP '\d+\.\d+(\.\d+)?' || echo "1.2.1")
    elif [[ -f "/usr/local/bin/stratum" ]]; then
        CURRENT_VERSION=$("/usr/local/bin/stratum" --version 2>/dev/null | grep -oP '\d+\.\d+(\.\d+)?' || echo "1.2.1")
    else
        CURRENT_VERSION="unknown"
    fi

    # Validate version format
    if [[ "$CURRENT_VERSION" != "unknown" ]]; then
        if ! [[ "$CURRENT_VERSION" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?(-[a-zA-Z0-9]+)?$ ]]; then
            CURRENT_VERSION="1.2.1"
        fi
    fi

    log_info "Current version: ${CURRENT_VERSION}"
}

get_target_version() {
    if [[ "$FETCH_LATEST" == "true" ]]; then
        # Fetch latest release tag from GitHub (with retry for transient failures)
        if command -v curl &> /dev/null; then
            local response http_code attempt
            local -a api_auth=()
            [[ -n "${GITHUB_TOKEN:-}" ]] && api_auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
            for attempt in 1 2 3; do
                # -w appends HTTP status code after response body, separated by newline
                local raw
                raw=$(curl -s --connect-timeout 10 --max-time 30 \
                    "${api_auth[@]}" \
                    -w '\n%{http_code}' \
                    https://api.github.com/repos/SpiralPool/Spiral-Pool/releases/latest 2>/dev/null) || true
                http_code=$(echo "$raw" | tail -1)
                response=$(echo "$raw" | sed '$d')

                if [[ "$http_code" == "200" ]]; then
                    TARGET_VERSION=$(echo "$response" | grep -oP '"tag_name": "\K[^"]+' | sed 's/^v//' || echo "")
                    break
                elif [[ "$http_code" == "403" ]]; then
                    log_warn "GitHub API rate limited (HTTP 403) — attempt $attempt/3"
                elif [[ "$http_code" == "404" ]]; then
                    log_error "GitHub API returned 404 — repository not found or no releases published"
                    break
                else
                    log_warn "GitHub API returned HTTP $http_code — attempt $attempt/3"
                fi

                [[ $attempt -lt 3 ]] && sleep 5
            done
        fi

        if [[ -z "$TARGET_VERSION" ]]; then
            log_error "Could not fetch latest version from GitHub API"
            log_error "Check your internet connection or use --local for local source upgrade"
            exit 1
        fi
    else
        # Use version from local source
        if [[ -f "$PROJECT_ROOT/VERSION" ]]; then
            TARGET_VERSION=$(tr -d '[:space:]' < "$PROJECT_ROOT/VERSION")
        else
            TARGET_VERSION="$CURRENT_VERSION"
        fi
    fi

    # Validate version format
    if ! [[ "$TARGET_VERSION" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?(-[a-zA-Z0-9]+)?$ ]]; then
        log_error "Invalid version format: '${TARGET_VERSION}'"
        exit 1
    fi

    log_info "Target version: ${TARGET_VERSION}"
}

check_for_updates() {
    # Silent version check - outputs JSON for Sentinel integration
    if [[ -f "${INSTALL_DIR}/VERSION" ]] && [[ ! -L "${INSTALL_DIR}/VERSION" ]]; then
        CURRENT_VERSION=$(tr -d '[:space:]' < "${INSTALL_DIR}/VERSION")
    elif [[ -f "${INSTALL_DIR}/bin/spiralstratum" ]]; then
        CURRENT_VERSION=$("${INSTALL_DIR}/bin/spiralstratum" --version 2>/dev/null | grep -oP '\d+\.\d+(\.\d+)?' || echo "1.2.1")
    else
        CURRENT_VERSION="1.2.1"
    fi

    local RELEASE_URL=""
    local RELEASE_INFO=""
    if command -v curl &> /dev/null; then
        local raw http_code
        local -a api_auth=()
        [[ -n "${GITHUB_TOKEN:-}" ]] && api_auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
        raw=$(curl -s --connect-timeout 10 --max-time 30 \
            "${api_auth[@]}" \
            -w '\n%{http_code}' \
            https://api.github.com/repos/SpiralPool/Spiral-Pool/releases/latest 2>/dev/null) || true
        http_code=$(echo "$raw" | tail -1)
        RELEASE_INFO=$(echo "$raw" | sed '$d')
        if [[ "$http_code" == "200" ]]; then
            TARGET_VERSION=$(echo "$RELEASE_INFO" | grep -oP '"tag_name": "\K[^"]+' | sed 's/^v//' || echo "")
            RELEASE_URL=$(echo "$RELEASE_INFO" | grep -oP '"html_url": "\K[^"]+' | head -1 || echo "")
        fi
    fi

    [[ -z "$TARGET_VERSION" ]] && TARGET_VERSION="$CURRENT_VERSION"

    local CURRENT_CLEAN=$(echo "$CURRENT_VERSION" | sed 's/^v//')
    local TARGET_CLEAN=$(echo "$TARGET_VERSION" | sed 's/^v//')

    [[ -z "$RELEASE_URL" ]] && RELEASE_URL="https://github.com/SpiralPool/Spiral-Pool/releases"

    local UPDATE_AVAILABLE="false"
    # Use sort -V for proper semantic version comparison (prevents downgrades)
    if [[ "$CURRENT_CLEAN" != "$TARGET_CLEAN" ]]; then
        local newer
        newer=$(printf '%s\n' "$CURRENT_CLEAN" "$TARGET_CLEAN" | sort -V | tail -1)
        [[ "$newer" == "$TARGET_CLEAN" ]] && UPDATE_AVAILABLE="true"
    fi

    if command -v jq &>/dev/null; then
        jq -n \
            --arg cv "$CURRENT_CLEAN" \
            --arg lv "$TARGET_CLEAN" \
            --argjson ua "$UPDATE_AVAILABLE" \
            --arg url "$RELEASE_URL" \
            --arg cmd "cd /spiralpool && chmod +x upgrade.sh && sudo ./upgrade.sh" \
            '{current_version: $cv, latest_version: $lv, update_available: $ua, release_url: $url, upgrade_command: $cmd}'
    else
        # Escape special JSON characters in URL (quotes, backslashes)
        local SAFE_URL="${RELEASE_URL//\\/\\\\}"
        SAFE_URL="${SAFE_URL//\"/\\\"}"
        cat << EOF
{
    "current_version": "${CURRENT_CLEAN}",
    "latest_version": "${TARGET_CLEAN}",
    "update_available": ${UPDATE_AVAILABLE},
    "release_url": "${SAFE_URL}",
    "upgrade_command": "cd /spiralpool && chmod +x upgrade.sh && sudo ./upgrade.sh"
}
EOF
    fi
    exit 0
}

# =============================================================================
# Backup Functions
# =============================================================================

create_backup() {
    if [[ "$SKIP_BACKUP" == "true" ]]; then
        log_warn "Skipping backup (--no-backup flag)"
        log_warn "⚠️  DATABASE WILL NOT BE BACKED UP - DATA LOSS POSSIBLE IF UPGRADE FAILS"
        # Require explicit confirmation in interactive mode (skip in --auto)
        if [[ "$AUTO_MODE" != "true" ]] && [[ -t 0 ]]; then
            echo ""
            read -p "Are you sure you want to continue WITHOUT a database backup? [y/N]: " confirm
            if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
                log_error "Upgrade cancelled. Run without --no-backup to create a backup."
                exit 1
            fi
        fi
        return
    fi

    local TIMESTAMP=$(date +%Y%m%d_%H%M%S)
    local BACKUP_NAME="pre-upgrade-${CURRENT_VERSION}-to-${TARGET_VERSION}-${TIMESTAMP}"
    local BACKUP_PATH="${BACKUP_DIR}/${BACKUP_NAME}"

    log_info "Creating backup at ${BACKUP_PATH}..."

    (umask 077 && mkdir -p "${BACKUP_DIR}")
    (umask 077 && mkdir -p "${BACKUP_PATH}")

    # Backup config files
    [[ -f "${INSTALL_DIR}/config/config.yaml" ]] && [[ ! -L "${INSTALL_DIR}/config/config.yaml" ]] && \
        cp "${INSTALL_DIR}/config/config.yaml" "${BACKUP_PATH}/" && log_info "  - config.yaml backed up"

    # Backup all coin config files (support all coins, not just DGB)
    # SHA-256d coins
    [[ -f "${INSTALL_DIR}/dgb/digibyte.conf" ]] && cp "${INSTALL_DIR}/dgb/digibyte.conf" "${BACKUP_PATH}/digibyte.conf" && log_info "  - digibyte.conf backed up"
    [[ -f "${INSTALL_DIR}/btc/bitcoin.conf" ]] && cp "${INSTALL_DIR}/btc/bitcoin.conf" "${BACKUP_PATH}/bitcoin.conf" && log_info "  - bitcoin.conf backed up"
    [[ -f "${INSTALL_DIR}/bch/bitcoin.conf" ]] && cp "${INSTALL_DIR}/bch/bitcoin.conf" "${BACKUP_PATH}/bitcoincash.conf" && log_info "  - bitcoincash.conf backed up"
    [[ -f "${INSTALL_DIR}/bc2/bitcoinii.conf" ]] && cp "${INSTALL_DIR}/bc2/bitcoinii.conf" "${BACKUP_PATH}/bitcoinii.conf" && log_info "  - bitcoinii.conf backed up"
    # Scrypt coins
    [[ -f "${INSTALL_DIR}/ltc/litecoin.conf" ]] && cp "${INSTALL_DIR}/ltc/litecoin.conf" "${BACKUP_PATH}/litecoin.conf" && log_info "  - litecoin.conf backed up"
    [[ -f "${INSTALL_DIR}/doge/dogecoin.conf" ]] && cp "${INSTALL_DIR}/doge/dogecoin.conf" "${BACKUP_PATH}/dogecoin.conf" && log_info "  - dogecoin.conf backed up"
    [[ -f "${INSTALL_DIR}/pep/pepecoin.conf" ]] && cp "${INSTALL_DIR}/pep/pepecoin.conf" "${BACKUP_PATH}/pepecoin.conf" && log_info "  - pepecoin.conf backed up"
    [[ -f "${INSTALL_DIR}/cat/catcoin.conf" ]] && cp "${INSTALL_DIR}/cat/catcoin.conf" "${BACKUP_PATH}/catcoin.conf" && log_info "  - catcoin.conf backed up"
    # SHA-256d merge-mineable coins (AuxPoW)
    [[ -f "${INSTALL_DIR}/nmc/namecoin.conf" ]] && cp "${INSTALL_DIR}/nmc/namecoin.conf" "${BACKUP_PATH}/namecoin.conf" && log_info "  - namecoin.conf backed up"
    [[ -f "${INSTALL_DIR}/sys/syscoin.conf" ]] && cp "${INSTALL_DIR}/sys/syscoin.conf" "${BACKUP_PATH}/syscoin.conf" && log_info "  - syscoin.conf backed up"
    [[ -f "${INSTALL_DIR}/xmy/myriadcoin.conf" ]] && cp "${INSTALL_DIR}/xmy/myriadcoin.conf" "${BACKUP_PATH}/myriadcoin.conf" && log_info "  - myriadcoin.conf backed up"
    [[ -f "${INSTALL_DIR}/fbtc/fractal.conf" ]] && cp "${INSTALL_DIR}/fbtc/fractal.conf" "${BACKUP_PATH}/fractal.conf" && log_info "  - fractal.conf backed up"
    # Legacy location (older installs may have config here)
    [[ -f "${INSTALL_DIR}/config/digibyte.conf" ]] && [[ ! -L "${INSTALL_DIR}/config/digibyte.conf" ]] && \
        cp "${INSTALL_DIR}/config/digibyte.conf" "${BACKUP_PATH}/digibyte-legacy.conf" && log_info "  - digibyte.conf (legacy) backed up"

    # Backup dashboard config
    [[ -d "${INSTALL_DIR}/dashboard/config" ]] && \
        cp -r "${INSTALL_DIR}/dashboard/config" "${BACKUP_PATH}/dashboard-config" && \
        log_info "  - dashboard config backed up"

    # Backup dashboard auth data (password hash, secret key)
    [[ -d "${INSTALL_DIR}/dashboard/data" ]] && \
        cp -r "${INSTALL_DIR}/dashboard/data" "${BACKUP_PATH}/dashboard-data" && \
        log_info "  - dashboard auth data backed up"

    # Backup user dashboard config (this is the file upgrade deletes to trigger setup wizard)
    [[ -d "/home/${POOL_USER}/.spiralpool" ]] && \
        cp -r "/home/${POOL_USER}/.spiralpool" "${BACKUP_PATH}/user-dashboard-config" && \
        log_info "  - user dashboard config backed up (~/.spiralpool/)"

    # Backup sentinel config (prefer /spiralpool/config/sentinel/ — ProtectHome=yes
    # makes ~/.spiralsentinel/ invisible to systemd services, so the live config is
    # at /spiralpool/config/sentinel/ since that fix was applied)
    if [[ -d "${INSTALL_DIR}/config/sentinel" ]]; then
        cp -r "${INSTALL_DIR}/config/sentinel" "${BACKUP_PATH}/sentinel-config" && \
            log_info "  - sentinel config backed up (${INSTALL_DIR}/config/sentinel/)"
    elif [[ -d "/home/${POOL_USER}/.spiralsentinel" ]]; then
        cp -r "/home/${POOL_USER}/.spiralsentinel" "${BACKUP_PATH}/sentinel-config" && \
            log_info "  - sentinel config backed up (~/.spiralsentinel/ — legacy path)"
    fi

    # Backup shared data directory (miners.json)
    [[ -d "${INSTALL_DIR}/data" ]] && \
        cp -r "${INSTALL_DIR}/data" "${BACKUP_PATH}/shared-data" && \
        log_info "  - shared data backed up"

    # Backup VERSION file (needed for rollback version detection)
    [[ -f "${INSTALL_DIR}/VERSION" ]] && \
        cp "${INSTALL_DIR}/VERSION" "${BACKUP_PATH}/" && \
        log_info "  - VERSION file backed up ($(cat "${INSTALL_DIR}/VERSION" 2>/dev/null))"

    # Backup current binaries
    [[ -f "${INSTALL_DIR}/bin/spiralstratum" ]] && \
        cp "${INSTALL_DIR}/bin/spiralstratum" "${BACKUP_PATH}/" && \
        log_info "  - spiralstratum binary backed up"

    [[ -f "${INSTALL_DIR}/bin/spiralctl" ]] && \
        cp "${INSTALL_DIR}/bin/spiralctl" "${BACKUP_PATH}/" && \
        log_info "  - spiralctl binary backed up"

    [[ -f "/usr/local/bin/stratum" ]] && \
        cp "/usr/local/bin/stratum" "${BACKUP_PATH}/stratum.backup" && \
        log_info "  - stratum binary backed up"

    # Backup utility scripts (spiralpool-sync, spiralpool-wallet, etc.)
    mkdir -p "${BACKUP_PATH}/utility-scripts"
    local util_count=0
    for util_script in /usr/local/bin/spiralpool-*; do
        [[ -f "$util_script" ]] || continue
        cp "$util_script" "${BACKUP_PATH}/utility-scripts/"
        ((util_count++)) || true
    done
    [[ $util_count -gt 0 ]] && log_info "  - ${util_count} utility scripts backed up"

    # Backup dashboard code (needed for version-consistent rollback)
    if [[ -d "${INSTALL_DIR}/dashboard" ]]; then
        mkdir -p "${BACKUP_PATH}/dashboard-code"
        cp "${INSTALL_DIR}/dashboard/"*.py "${BACKUP_PATH}/dashboard-code/" 2>/dev/null || true
        [[ -d "${INSTALL_DIR}/dashboard/templates" ]] && \
            cp -r "${INSTALL_DIR}/dashboard/templates" "${BACKUP_PATH}/dashboard-code/"
        [[ -d "${INSTALL_DIR}/dashboard/static" ]] && \
            cp -r "${INSTALL_DIR}/dashboard/static" "${BACKUP_PATH}/dashboard-code/"
        log_info "  - dashboard code backed up"
    fi

    # Backup systemd service files
    for service in spiralstratum spiraldash spiralsentinel spiralpool-health stratum; do
        [[ -f "/etc/systemd/system/${service}.service" ]] && \
            cp "/etc/systemd/system/${service}.service" "${BACKUP_PATH}/"
    done
    log_info "  - service files backed up"

    # Backup database - MANDATORY for data safety
    # Database backup is critical for rollback capability
    local db_backup_failed=false
    if systemctl is-active --quiet postgresql 2>/dev/null || systemctl is-active --quiet patroni 2>/dev/null; then
        log_info "Creating mandatory database backup..."
        if sudo -u postgres pg_dump spiralstratum > "${BACKUP_PATH}/database.sql" 2>/dev/null && [[ -s "${BACKUP_PATH}/database.sql" ]]; then
            # Verify dump completeness: pg_dump writes this marker at the end of a successful dump
            if ! tail -5 "${BACKUP_PATH}/database.sql" | grep -q "PostgreSQL database dump complete" 2>/dev/null; then
                db_backup_failed=true
                log_error "DATABASE BACKUP INCOMPLETE (partial dump detected — possible disk full or timeout)"
                log_error "Incomplete dump kept at: ${BACKUP_PATH}/database.sql (review or delete manually)"
            elif gzip "${BACKUP_PATH}/database.sql" 2>/dev/null && [[ -f "${BACKUP_PATH}/database.sql.gz" ]]; then
                log_success "  - database backed up ($(du -h "${BACKUP_PATH}/database.sql.gz" 2>/dev/null | cut -f1))"
            else
                db_backup_failed=true
                log_error "DATABASE COMPRESSION FAILED (disk full?)"
            fi
        else
            db_backup_failed=true
            log_error "DATABASE BACKUP FAILED!"
        fi
    else
        db_backup_failed=true
        log_error "PostgreSQL is not running - cannot backup database!"
    fi

    # Fail if database backup failed (critical for rollback)
    if [[ "$db_backup_failed" == "true" ]]; then
        log_error "Cannot proceed without database backup."
        log_error "Please ensure PostgreSQL is running: sudo systemctl start postgresql"
        log_error "Or use --no-backup to skip (NOT RECOMMENDED - data loss possible)"
        log_error "Partial backup kept at: ${BACKUP_PATH} (review or delete manually)"
        exit 1
    fi

    # Save upgrade info
    cat > "${BACKUP_PATH}/upgrade-info.txt" << EOF
Upgrade performed: $(date)
Previous version: ${CURRENT_VERSION}
Target version: ${TARGET_VERSION}
Pool user: ${POOL_USER}
Mode: $([ "$USE_LOCAL" == "true" ] && echo "Local source" || echo "GitHub fetch")
EOF

    # Generate SHA256 checksums for backup integrity verification
    log_info "Generating backup checksums..."
    (cd "${BACKUP_PATH}" && find . -type f ! -name "CHECKSUMS.sha256" -exec sha256sum {} \; > CHECKSUMS.sha256 2>/dev/null)
    if [[ -f "${BACKUP_PATH}/CHECKSUMS.sha256" ]]; then
        log_success "Backup checksums generated: CHECKSUMS.sha256"
    else
        log_warn "Failed to generate checksums (non-critical)"
    fi

    log_success "Backup created: ${BACKUP_PATH}"
    mkdir -p "${INSTALL_DIR}/config" 2>/dev/null || true
    echo "${BACKUP_PATH}" > "${INSTALL_DIR}/config/.last-backup-path"

    # Store backup name for automatic rollback on failure
    # Note: UPGRADE_IN_PROGRESS is NOT set here — it's set in main() after
    # stop_services completes, right before files are actually modified.
    # Setting it here would trigger unnecessary rollback if stop_services fails
    # (backup IS the current state, so rolling back would be pointless).
    CURRENT_BACKUP_NAME="${BACKUP_NAME}"
}

# Rollback to a previous backup
# Usage: ./upgrade.sh --rollback [backup_name]
rollback_to_backup() {
    local backup_name="${1:-}"
    local backup_path=""

    if [[ -z "$backup_name" ]]; then
        # List available backups
        if [[ ! -d "${BACKUP_DIR}" ]]; then
            log_error "No backups found at ${BACKUP_DIR}"
            return 1
        fi

        echo ""
        echo "Available backups:"
        echo "===================="
        local backups=()
        while IFS= read -r -d '' dir; do
            local dirname=$(basename "$dir")
            if [[ -f "$dir/upgrade-info.txt" ]]; then
                backups+=("$dirname")
                local info=$(head -3 "$dir/upgrade-info.txt" 2>/dev/null)
                echo "  $dirname"
                echo "$info" | sed 's/^/    /'
                echo ""
            fi
        done < <(find "${BACKUP_DIR}" -maxdepth 1 -type d -name "pre-upgrade-*" -print0 | sort -rz)

        if [[ ${#backups[@]} -eq 0 ]]; then
            log_error "No valid backups found"
            return 1
        fi

        echo "Usage: $0 --rollback <backup_name>"
        echo "Example: $0 --rollback ${backups[0]}"
        return 0
    fi

    # SECURITY: Validate backup name — prevent path traversal (../../../etc/...)
    if [[ "$backup_name" == *..* ]] || [[ "$backup_name" == */* ]]; then
        log_error "Invalid backup name: contains path traversal characters"
        return 1
    fi

    backup_path="${BACKUP_DIR}/${backup_name}"

    if [[ ! -d "$backup_path" ]]; then
        log_error "Backup not found: $backup_path"
        return 1
    fi

    if [[ ! -f "$backup_path/upgrade-info.txt" ]]; then
        log_error "Invalid backup: missing upgrade-info.txt"
        return 1
    fi

    # Verify backup integrity if checksums exist
    if [[ -f "$backup_path/CHECKSUMS.sha256" ]]; then
        log_info "Verifying backup integrity..."
        if (cd "$backup_path" && sha256sum -c CHECKSUMS.sha256 --quiet 2>/dev/null); then
            log_success "Backup integrity verified"
        else
            log_error "BACKUP INTEGRITY CHECK FAILED!"
            log_error "One or more backup files are corrupted."
            log_error "Aborting rollback to prevent restoring corrupted data."
            if [[ "$AUTO_MODE" != "true" ]]; then
                read -p "Force rollback anyway (DANGEROUS)? [y/N] " force_confirm
                if [[ ! "$force_confirm" =~ ^[Yy]$ ]]; then
                    return 1
                fi
                log_warn "Proceeding with potentially corrupted backup at user request"
            else
                return 1
            fi
        fi
    else
        log_warn "No checksums found - skipping integrity verification"
    fi

    log_info "Rolling back to backup: $backup_name"
    cat "$backup_path/upgrade-info.txt"
    echo ""

    if [[ "$AUTO_MODE" != "true" ]]; then
        read -p "Proceed with rollback? [y/N] " confirm
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
            log_info "Rollback cancelled"
            return 0
        fi
    fi

    # Suppress sentinel alerts during rollback (services will be briefly down)
    suppress_sentinel_alerts 15 "Rollback in progress"

    # Stop services (record which were running so we only restart those)
    log_info "Stopping services..."
    local services=("$STRATUM_SERVICE" "$DASHBOARD_SERVICE" "$SENTINEL_SERVICE")
    [[ -n "$HEALTH_SERVICE" ]] && services+=("$HEALTH_SERVICE")
    local rollback_were_running=()

    # When called from automatic rollback (cleanup_on_exit), services are already
    # stopped by stop_services(). Use the global SERVICES_WERE_RUNNING array instead
    # of re-detecting, otherwise rollback_were_running will be empty and no services
    # will be restarted after rollback.
    if [[ "${UPGRADE_IN_PROGRESS:-false}" == "true" ]] && [[ ${#SERVICES_WERE_RUNNING[@]} -gt 0 ]]; then
        rollback_were_running=("${SERVICES_WERE_RUNNING[@]}")
    else
        for service in "${services[@]}"; do
            if systemctl is-active --quiet "$service" 2>/dev/null; then
                rollback_were_running+=("$service")
            fi
        done
    fi

    for service in "${services[@]}"; do
        systemctl stop "$service" 2>/dev/null || true
    done
    log_info "  ${#rollback_were_running[@]} services to restart after rollback"

    # Restore binary
    if [[ -f "$backup_path/spiralstratum" ]]; then
        log_info "Restoring stratum binary..."
        cp "$backup_path/spiralstratum" "${INSTALL_DIR}/bin/spiralstratum"
        chmod +x "${INSTALL_DIR}/bin/spiralstratum"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/bin/spiralstratum"
    fi

    if [[ -f "$backup_path/stratum.backup" ]]; then
        cp "$backup_path/stratum.backup" "/usr/local/bin/stratum"
        chmod +x "/usr/local/bin/stratum"
    fi

    # Restore spiralctl binary
    if [[ -f "$backup_path/spiralctl" ]]; then
        log_info "Restoring spiralctl binary..."
        cp "$backup_path/spiralctl" "${INSTALL_DIR}/bin/spiralctl"
        chmod +x "${INSTALL_DIR}/bin/spiralctl"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/bin/spiralctl"
    fi

    # Restore VERSION file
    if [[ -f "$backup_path/VERSION" ]]; then
        log_info "Restoring VERSION file..."
        cp "$backup_path/VERSION" "${INSTALL_DIR}/VERSION"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/VERSION"
    fi

    # Restore utility scripts (spiralpool-sync, spiralpool-wallet, etc.)
    if [[ -d "$backup_path/utility-scripts" ]]; then
        log_info "Restoring utility scripts..."
        for util_script in "$backup_path"/utility-scripts/spiralpool-*; do
            [[ -f "$util_script" ]] || continue
            cp "$util_script" /usr/local/bin/
            chmod +x "/usr/local/bin/$(basename "$util_script")"
        done
    fi

    # Restore dashboard code (version-consistent with rolled-back stratum)
    if [[ -d "$backup_path/dashboard-code" ]]; then
        log_info "Restoring dashboard code..."
        cp "$backup_path"/dashboard-code/*.py "${INSTALL_DIR}/dashboard/" 2>/dev/null || true
        [[ -d "$backup_path/dashboard-code/templates" ]] && \
            cp -r "$backup_path/dashboard-code/templates" "${INSTALL_DIR}/dashboard/"
        [[ -d "$backup_path/dashboard-code/static" ]] && \
            cp -r "$backup_path/dashboard-code/static" "${INSTALL_DIR}/dashboard/"
        chown -R "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/dashboard/" 2>/dev/null || true
    fi

    # Restore config
    if [[ -f "$backup_path/config.yaml" ]]; then
        log_info "Restoring config.yaml..."
        cp "$backup_path/config.yaml" "${INSTALL_DIR}/config/config.yaml"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/config/config.yaml"
        chmod 600 "${INSTALL_DIR}/config/config.yaml"
    fi

    # Restore all coin config files (support all coins)
    # SHA-256d coins
    if [[ -f "$backup_path/digibyte.conf" ]]; then
        log_info "Restoring digibyte.conf..."
        mkdir -p "${INSTALL_DIR}/dgb"
        cp "$backup_path/digibyte.conf" "${INSTALL_DIR}/dgb/digibyte.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/dgb/digibyte.conf"
        chmod 600 "${INSTALL_DIR}/dgb/digibyte.conf"
    fi
    if [[ -f "$backup_path/bitcoin.conf" ]]; then
        log_info "Restoring bitcoin.conf..."
        mkdir -p "${INSTALL_DIR}/btc"
        cp "$backup_path/bitcoin.conf" "${INSTALL_DIR}/btc/bitcoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/btc/bitcoin.conf"
        chmod 600 "${INSTALL_DIR}/btc/bitcoin.conf"
    fi
    if [[ -f "$backup_path/bitcoincash.conf" ]]; then
        log_info "Restoring bitcoincash.conf..."
        mkdir -p "${INSTALL_DIR}/bch"
        cp "$backup_path/bitcoincash.conf" "${INSTALL_DIR}/bch/bitcoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/bch/bitcoin.conf"
        chmod 600 "${INSTALL_DIR}/bch/bitcoin.conf"
    fi
    if [[ -f "$backup_path/bitcoinii.conf" ]]; then
        log_info "Restoring bitcoinii.conf..."
        mkdir -p "${INSTALL_DIR}/bc2"
        cp "$backup_path/bitcoinii.conf" "${INSTALL_DIR}/bc2/bitcoinii.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/bc2/bitcoinii.conf"
        chmod 600 "${INSTALL_DIR}/bc2/bitcoinii.conf"
    fi
    # Scrypt coins
    if [[ -f "$backup_path/litecoin.conf" ]]; then
        log_info "Restoring litecoin.conf..."
        mkdir -p "${INSTALL_DIR}/ltc"
        cp "$backup_path/litecoin.conf" "${INSTALL_DIR}/ltc/litecoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/ltc/litecoin.conf"
        chmod 600 "${INSTALL_DIR}/ltc/litecoin.conf"
    fi
    if [[ -f "$backup_path/dogecoin.conf" ]]; then
        log_info "Restoring dogecoin.conf..."
        mkdir -p "${INSTALL_DIR}/doge"
        cp "$backup_path/dogecoin.conf" "${INSTALL_DIR}/doge/dogecoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/doge/dogecoin.conf"
        chmod 600 "${INSTALL_DIR}/doge/dogecoin.conf"
    fi
    if [[ -f "$backup_path/pepecoin.conf" ]]; then
        log_info "Restoring pepecoin.conf..."
        mkdir -p "${INSTALL_DIR}/pep"
        cp "$backup_path/pepecoin.conf" "${INSTALL_DIR}/pep/pepecoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/pep/pepecoin.conf"
        chmod 600 "${INSTALL_DIR}/pep/pepecoin.conf"
    fi
    if [[ -f "$backup_path/catcoin.conf" ]]; then
        log_info "Restoring catcoin.conf..."
        mkdir -p "${INSTALL_DIR}/cat"
        cp "$backup_path/catcoin.conf" "${INSTALL_DIR}/cat/catcoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/cat/catcoin.conf"
        chmod 600 "${INSTALL_DIR}/cat/catcoin.conf"
    fi
    # SHA-256d merge-mineable coins (AuxPoW)
    if [[ -f "$backup_path/namecoin.conf" ]]; then
        log_info "Restoring namecoin.conf..."
        mkdir -p "${INSTALL_DIR}/nmc"
        cp "$backup_path/namecoin.conf" "${INSTALL_DIR}/nmc/namecoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/nmc/namecoin.conf"
        chmod 600 "${INSTALL_DIR}/nmc/namecoin.conf"
    fi
    if [[ -f "$backup_path/syscoin.conf" ]]; then
        log_info "Restoring syscoin.conf..."
        mkdir -p "${INSTALL_DIR}/sys"
        cp "$backup_path/syscoin.conf" "${INSTALL_DIR}/sys/syscoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/sys/syscoin.conf"
        chmod 600 "${INSTALL_DIR}/sys/syscoin.conf"
    fi
    if [[ -f "$backup_path/myriadcoin.conf" ]]; then
        log_info "Restoring myriadcoin.conf..."
        mkdir -p "${INSTALL_DIR}/xmy"
        cp "$backup_path/myriadcoin.conf" "${INSTALL_DIR}/xmy/myriadcoin.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/xmy/myriadcoin.conf"
        chmod 600 "${INSTALL_DIR}/xmy/myriadcoin.conf"
    fi
    if [[ -f "$backup_path/fractal.conf" ]]; then
        log_info "Restoring fractal.conf..."
        mkdir -p "${INSTALL_DIR}/fbtc"
        cp "$backup_path/fractal.conf" "${INSTALL_DIR}/fbtc/fractal.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/fbtc/fractal.conf"
        chmod 600 "${INSTALL_DIR}/fbtc/fractal.conf"
    fi
    # Legacy location restore (for older backups)
    if [[ -f "$backup_path/digibyte-legacy.conf" ]] && [[ -d "${INSTALL_DIR}/config" ]]; then
        log_info "Restoring digibyte.conf (legacy location)..."
        cp "$backup_path/digibyte-legacy.conf" "${INSTALL_DIR}/config/digibyte.conf"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/config/digibyte.conf"
        chmod 600 "${INSTALL_DIR}/config/digibyte.conf"
    fi

    # Restore dashboard config (copy to temp first, then swap to prevent data loss)
    if [[ -d "$backup_path/dashboard-config" ]]; then
        log_info "Restoring dashboard config..."
        if cp -r "$backup_path/dashboard-config" "${INSTALL_DIR}/dashboard/config.restore.tmp"; then
            rm -rf "${INSTALL_DIR}/dashboard/config"
            mv "${INSTALL_DIR}/dashboard/config.restore.tmp" "${INSTALL_DIR}/dashboard/config"
            chown -R "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/dashboard/config"
        else
            log_warn "Failed to copy dashboard config backup — skipping (original preserved)"
            rm -rf "${INSTALL_DIR}/dashboard/config.restore.tmp" 2>/dev/null
        fi
    fi

    # Restore dashboard auth data (copy to temp first, then swap)
    if [[ -d "$backup_path/dashboard-data" ]]; then
        log_info "Restoring dashboard auth data..."
        if cp -r "$backup_path/dashboard-data" "${INSTALL_DIR}/dashboard/data.restore.tmp"; then
            rm -rf "${INSTALL_DIR}/dashboard/data"
            mv "${INSTALL_DIR}/dashboard/data.restore.tmp" "${INSTALL_DIR}/dashboard/data"
            chown -R "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/dashboard/data"
        else
            log_warn "Failed to copy dashboard data backup — skipping (original preserved)"
            rm -rf "${INSTALL_DIR}/dashboard/data.restore.tmp" 2>/dev/null
        fi
    fi

    # Restore sentinel config — write to BOTH paths:
    # - /spiralpool/config/sentinel/ (used by systemd services under ProtectHome=yes)
    # - ~/.spiralsentinel/ (used by interactive runs, legacy)
    if [[ -d "$backup_path/sentinel-config" ]]; then
        log_info "Restoring sentinel config..."
        # Primary path (systemd-visible)
        sudo mkdir -p "${INSTALL_DIR}/config/sentinel"
        if cp -r "$backup_path/sentinel-config/." "${INSTALL_DIR}/config/sentinel/"; then
            chown -R "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/config/sentinel"
            log_info "  - restored to ${INSTALL_DIR}/config/sentinel/"
        else
            log_warn "Failed to restore sentinel config to ${INSTALL_DIR}/config/sentinel/"
        fi
        # Legacy path (interactive sessions)
        if cp -r "$backup_path/sentinel-config" "/home/${POOL_USER}/.spiralsentinel.restore.tmp" 2>/dev/null; then
            rm -rf "/home/${POOL_USER}/.spiralsentinel"
            mv "/home/${POOL_USER}/.spiralsentinel.restore.tmp" "/home/${POOL_USER}/.spiralsentinel"
            chown -R "${POOL_USER}:${POOL_USER}" "/home/${POOL_USER}/.spiralsentinel"
        else
            rm -rf "/home/${POOL_USER}/.spiralsentinel.restore.tmp" 2>/dev/null
        fi
    fi

    # Restore user dashboard config (~/.spiralpool/)
    if [[ -d "$backup_path/user-dashboard-config" ]]; then
        log_info "Restoring user dashboard config..."
        mkdir -p "/home/${POOL_USER}/.spiralpool"
        cp -r "$backup_path/user-dashboard-config/." "/home/${POOL_USER}/.spiralpool/"
        chown -R "${POOL_USER}:${POOL_USER}" "/home/${POOL_USER}/.spiralpool"
        chmod 700 "/home/${POOL_USER}/.spiralpool"
    fi

    # Restore shared data (copy to temp first, then swap)
    if [[ -d "$backup_path/shared-data" ]]; then
        log_info "Restoring shared data..."
        if cp -r "$backup_path/shared-data" "${INSTALL_DIR}/data.restore.tmp"; then
            rm -rf "${INSTALL_DIR}/data"
            mv "${INSTALL_DIR}/data.restore.tmp" "${INSTALL_DIR}/data"
            chown -R "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/data"
        else
            log_warn "Failed to copy shared data backup — skipping (original preserved)"
            rm -rf "${INSTALL_DIR}/data.restore.tmp" 2>/dev/null
        fi
    fi

    # Restore systemd services
    for service_file in "$backup_path"/*.service; do
        if [[ -f "$service_file" ]]; then
            local service_name=$(basename "$service_file")
            log_info "Restoring $service_name..."
            cp "$service_file" "/etc/systemd/system/"
        fi
    done

    # Reload systemd
    systemctl daemon-reload || log_warn "systemctl daemon-reload failed during rollback"

    # Restore database if backup exists and PostgreSQL is running
    # Must do this before starting services so they connect to restored data
    # Note: On HA streaming replication setups, changes propagate automatically to standby
    local db_restored="false"
    if [[ -f "$backup_path/database.sql.gz" ]]; then
        log_info "Restoring database from backup..."
        if systemctl is-active --quiet postgresql 2>/dev/null || systemctl is-active --quiet patroni 2>/dev/null; then
            # Check for HA replication - warn user if detected
            local is_primary
            is_primary=$(sudo -u postgres psql -tAc "SELECT NOT pg_is_in_recovery();" 2>/dev/null || echo "unknown")
            if [[ "$is_primary" == "f" ]]; then
                log_warn "This appears to be a STANDBY server - skipping database restore"
                log_warn "Restore should be performed on the PRIMARY server"
            elif [[ "$is_primary" == "unknown" ]]; then
                log_warn "Could not determine HA role (PostgreSQL query failed)"
                log_warn "Skipping database restore to avoid modifying a standby server"
                log_warn "To restore manually: gunzip -c '$backup_path/database.sql.gz' | sudo -u postgres psql spiralstratum"
            else
                # First, terminate all connections to the database
                log_info "Terminating active database connections..."
                sudo -u postgres psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'spiralstratum' AND pid <> pg_backend_pid();" 2>/dev/null || true

                # Create restore database, restore to it, then swap atomically
                if sudo -u postgres psql -c "DROP DATABASE IF EXISTS spiralstratum_restore;" 2>/dev/null && \
                   sudo -u postgres psql -c "CREATE DATABASE spiralstratum_restore OWNER spiralstratum;" 2>/dev/null && \
                   (set -o pipefail; gunzip -c "$backup_path/database.sql.gz" | sudo -u postgres psql spiralstratum_restore 2>/dev/null); then

                    # Terminate any new connections that may have opened
                    sudo -u postgres psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'spiralstratum' AND pid <> pg_backend_pid();" 2>/dev/null || true

                    # Swap databases atomically using rename
                    if sudo -u postgres psql -c "ALTER DATABASE spiralstratum RENAME TO spiralstratum_old;" 2>/dev/null && \
                       sudo -u postgres psql -c "ALTER DATABASE spiralstratum_restore RENAME TO spiralstratum;" 2>/dev/null; then
                        sudo -u postgres psql -c "DROP DATABASE IF EXISTS spiralstratum_old;" 2>/dev/null
                        log_success "Database restored successfully"
                        db_restored="true"

                        # On HA primary, changes will replicate to standby automatically
                        if [[ "$is_primary" == "t" ]]; then
                            log_info "Note: Database changes will replicate to standby server(s)"
                        fi
                    else
                        log_error "Database swap failed - attempting recovery"
                        # Try to recover original database if rename failed
                        sudo -u postgres psql -c "ALTER DATABASE spiralstratum_old RENAME TO spiralstratum;" 2>/dev/null || true
                        sudo -u postgres psql -c "DROP DATABASE IF EXISTS spiralstratum_restore;" 2>/dev/null || true
                    fi
                else
                    log_error "Database restore failed - keeping current database"
                    sudo -u postgres psql -c "DROP DATABASE IF EXISTS spiralstratum_restore;" 2>/dev/null || true
                fi
            fi
        else
            log_warn "PostgreSQL not running - cannot restore database"
        fi
    fi

    # Restart only services that were running before rollback
    log_info "Starting services..."
    systemctl daemon-reload || true
    local restart_failures=0
    for service in "${rollback_were_running[@]}"; do
        if systemctl start "$service" 2>/dev/null; then
            log_info "  - ${service} started"
        else
            log_error "  FAILED to start ${service} — manual restart required: systemctl start ${service}"
            restart_failures=$((restart_failures + 1))
        fi
    done
    if [[ $restart_failures -gt 0 ]]; then
        log_warn "WARNING: $restart_failures service(s) failed to restart after rollback"
        log_warn "Check status with: systemctl status spiralstratum spiralsentinel spiraldash"
    fi

    # Clear maintenance mode so Sentinel resumes alerting
    clear_alert_suppression 2>/dev/null || true

    log_success "Rollback complete!"
    log_info "Previous version restored from: $backup_name"

    if [[ "$db_restored" == "true" ]]; then
        log_success "Database was restored to pre-upgrade state"
    elif [[ -f "$backup_path/database.sql.gz" ]]; then
        echo ""
        log_warn "Database backup exists but automatic restore failed."
        log_warn "To restore database manually:"
        log_warn "  gunzip -c '$backup_path/database.sql.gz' | sudo -u postgres psql spiralstratum"
    fi

    return 0
}

# =============================================================================
# Alert Suppression (Maintenance Mode)
# =============================================================================

suppress_sentinel_alerts() {
    local duration_minutes="${1:-60}"
    local reason="${2:-Upgrade in progress}"
    # Sanitize reason for JSON safety
    reason="${reason//\\/\\\\}"
    reason="${reason//\"/\\\"}"

    if [[ -x "${INSTALL_DIR}/scripts/maintenance-mode.sh" ]]; then
        "${INSTALL_DIR}/scripts/maintenance-mode.sh" enable "$duration_minutes" "$reason" > /dev/null 2>&1
        log_info "Maintenance mode enabled for ${duration_minutes} minutes"
    else
        local suppress_until=$(($(date +%s) + (duration_minutes * 60)))
        local suppress_file="${INSTALL_DIR}/config/.maintenance-mode"
        mkdir -p "${INSTALL_DIR}/config"
        cat > "$suppress_file" << EOF
{"enabled": true, "start_time": $(date +%s), "end_time": ${suppress_until}, "duration_minutes": ${duration_minutes}, "reason": "${reason}", "started_by": "upgrade.sh"}
EOF
        chmod 644 "$suppress_file"
        # Sentinel runs as pool user — ensure it can read this file
        if [[ -n "$POOL_USER" ]]; then
            chown "${POOL_USER}:${POOL_USER}" "$suppress_file" 2>/dev/null || true
        fi
    fi
}

clear_alert_suppression() {
    if [[ -x "${INSTALL_DIR}/scripts/maintenance-mode.sh" ]]; then
        "${INSTALL_DIR}/scripts/maintenance-mode.sh" disable > /dev/null 2>&1
    else
        rm -f "${INSTALL_DIR}/config/.maintenance-mode" 2>/dev/null || true
    fi
    log_info "Maintenance mode disabled"
}

# =============================================================================
# Service Management
# =============================================================================

stop_services() {
    log_info "Stopping Spiral Pool services gracefully..."
    suppress_sentinel_alerts 60 "Upgrade in progress"

    SERVICES_WERE_RUNNING=()

    # Build service list based on what's being updated
    # When --dashboard-only or --sentinel-only, avoid stopping stratum (miners stay connected)
    local services=()
    $UPDATE_STRATUM && services+=("$STRATUM_SERVICE")
    $UPDATE_DASHBOARD && services+=("$DASHBOARD_SERVICE")
    $UPDATE_SENTINEL && services+=("$SENTINEL_SERVICE")
    # Health service monitors stratum — only cycle if stratum is being updated
    if $UPDATE_STRATUM && [[ -n "$HEALTH_SERVICE" ]]; then
        services+=("$HEALTH_SERVICE")
    fi

    # Check VIP status (only relevant when stopping stratum)
    if $UPDATE_STRATUM && [[ -f "${INSTALL_DIR}/bin/spiralctl" ]]; then
        local vip_status=$("${INSTALL_DIR}/bin/spiralctl" vip status 2>/dev/null || echo "")
        if echo "$vip_status" | grep -q "MASTER"; then
            log_info "  - This node is VIP MASTER - initiating graceful VIP release..."
        fi
    fi

    for service in "${services[@]}"; do
        if systemctl is-active --quiet "$service" 2>/dev/null; then
            SERVICES_WERE_RUNNING+=("$service")

            if [[ "$service" == "$STRATUM_SERVICE" ]]; then
                log_info "  - Initiating graceful shutdown of ${service}..."

                # First, signal the stratum to start draining connections
                # The stratum server has a shutdown timeout configured (default 10s)
                # During this time, it stops accepting new connections and finishes current work
                log_info "    Draining active miner connections..."

                # Send SIGTERM for graceful shutdown (stratum handles this with connection drain)
                systemctl stop "$service" --no-block 2>/dev/null || true

                # Wait for service to fully stop (inactive/failed), not just leave "active".
                # Note: "deactivating" means SIGTERM was received but the process is still
                # draining connections — is-active returns false for deactivating, so we
                # must check the actual state string to avoid exiting the loop too early.
                local wait_count=0
                local drain_timeout=60  # Allow up to 60s for connection drain
                local svc_state=""
                while [[ $wait_count -lt $drain_timeout ]]; do
                    # systemctl is-active prints state to stdout even on non-zero exit
                    # (e.g. "deactivating" exits 3). Use || true OUTSIDE $() to prevent
                    # set -e from killing the script, without appending to stdout.
                    svc_state=$(systemctl is-active "$service" 2>/dev/null) || true
                    [[ -z "$svc_state" ]] && svc_state="unknown"
                    [[ "$svc_state" == "inactive" || "$svc_state" == "failed" ]] && break
                    if [[ $((wait_count % 10)) -eq 0 ]] && [[ $wait_count -gt 0 ]]; then
                        log_info "    Still draining connections... (${wait_count}s, state: ${svc_state})"
                    fi
                    sleep 1
                    ((++wait_count))
                done

                # Force stop if still running/deactivating after drain timeout
                svc_state=$(systemctl is-active "$service" 2>/dev/null) || true
                [[ -z "$svc_state" ]] && svc_state="unknown"
                if [[ "$svc_state" != "inactive" && "$svc_state" != "failed" ]]; then
                    log_warn "  - Connection drain timeout (state: ${svc_state}) — forcing stop"
                    systemctl kill "$service" 2>/dev/null || true
                    sleep 2
                else
                    log_info "    Connection drain complete (${wait_count}s)"
                fi
            else
                systemctl stop "$service" 2>/dev/null || true
            fi
            log_info "  - ${service} stopped"
        fi
    done

    sleep 2
    log_success "Services stopped (${#SERVICES_WERE_RUNNING[@]} services were running)"
}

start_services() {
    log_info "Starting Spiral Pool services..."
    systemctl daemon-reload || true

    # HA awareness: On backup nodes, only start stratum (sentinel/dash managed by ha-role-watcher)
    local is_ha_backup=false
    if [[ -f /etc/keepalived/keepalived.conf ]] && grep -q "SPIRALPOOL" /etc/keepalived/keepalived.conf 2>/dev/null; then
        # HA is configured — check if this node holds the VIP
        local vip
        vip=$(grep -oP '^\s+\K[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(?=/)' /etc/keepalived/keepalived.conf 2>/dev/null | head -1)
        if [[ -n "$vip" ]] && ! ip addr show 2>/dev/null | grep -qF " ${vip}/"; then
            is_ha_backup=true
            log_warn "HA backup node detected — sentinel/dash restart deferred to ha-role-watcher"
        fi
    fi

    if [[ ${#SERVICES_WERE_RUNNING[@]} -eq 0 ]]; then
        log_warn "Service state unknown — defaulting to starting core services"
        SERVICES_WERE_RUNNING=("$STRATUM_SERVICE" "$DASHBOARD_SERVICE")
    fi

    local start_order=("$STRATUM_SERVICE" "$DASHBOARD_SERVICE" "$SENTINEL_SERVICE" "$HEALTH_SERVICE")

    for service in "${start_order[@]}"; do
        [[ -z "$service" ]] && continue

        # On HA backup nodes, only start stratum + health — sentinel/dash are managed by ha-role-watcher
        if [[ "$is_ha_backup" == "true" ]]; then
            if [[ "$service" == "$SENTINEL_SERVICE" ]] || [[ "$service" == "$DASHBOARD_SERVICE" ]]; then
                log_info "  - ${service} skipped (HA backup — managed by ha-role-watcher)"
                continue
            fi
        fi

        local should_start=false
        for was_running in "${SERVICES_WERE_RUNNING[@]}"; do
            [[ "$was_running" == "$service" ]] && should_start=true && break
        done

        if [[ "$should_start" == "true" ]] && [[ -f "/etc/systemd/system/${service}.service" ]]; then
            systemctl start --no-block "$service" 2>/dev/null || true
            sleep 1
            log_info "  - ${service} starting"
        fi
    done

    # Restart HA watcher if its service file was updated (picks up security context changes
    # like NoNewPrivileges removal — the running process keeps the OLD security context
    # until restarted, which blocks sudo in ha-service-control.sh)
    if systemctl is-active --quiet spiralpool-ha-watcher 2>/dev/null; then
        systemctl restart spiralpool-ha-watcher 2>/dev/null || true
        log_info "  - spiralpool-ha-watcher restarted (service file updated)"
    fi

    clear_alert_suppression
    log_success "Services started"
}

# =============================================================================
# Config Fixes
# =============================================================================

fix_config_issues() {
    log_info "Checking and fixing config issues..."

    local CONFIG_FILE="$INSTALL_DIR/config/config.yaml"
    local FIXES_APPLIED=0

    if [[ ! -f "$CONFIG_FILE" ]]; then
        log_warn "Config file not found at $CONFIG_FILE"
        return
    fi

    # Fix 0: YAML duplicate keys (critical - prevents stratum from starting)
    # This happens when sed commands accidentally add duplicate lines
    log_info "  - Checking for duplicate YAML keys..."
    local DUPLICATE_KEYS=("name" "symbol" "enabled" "address" "algorithm" "listen")
    for key in "${DUPLICATE_KEYS[@]}"; do
        # Find consecutive duplicate lines using grep and awk
        # This is more robust than bash regex which can fail with set -e
        local lines_to_delete
        lines_to_delete=$(awk -v key="$key" '
            /^[[:space:]]+'"$key"':/ {
                if (prev_match) {
                    print NR
                }
                prev_match = 1
                next
            }
            { prev_match = 0 }
        ' "$CONFIG_FILE" 2>/dev/null || echo "")

        # Delete duplicate lines (in reverse order to preserve line numbers)
        if [[ -n "$lines_to_delete" ]]; then
            for del_line in $(echo "$lines_to_delete" | sort -rn); do
                log_info "    - Removing duplicate '${key}:' at line ${del_line}"
                sed -i "${del_line}d" "$CONFIG_FILE" || true
                FIXES_APPLIED=$((FIXES_APPLIED + 1))
            done
        fi
    done

    # Fix 0b: Ensure coins section has required 'name' field
    # The stratum binary requires coins[].name but older configs may be missing it
    # This handles any coin: DGB -> DigiByte, BTC -> Bitcoin, BCH -> Bitcoin Cash, etc.
    if grep -q "^coins:" "$CONFIG_FILE" 2>/dev/null; then
        # Process each coin entry that has a symbol but no name
        # Use awk to track coin entries and check for name field within each
        local coin_fixes
        coin_fixes=$(awk '
            /^coins:/ { in_coins=1; next }
            # New coin entry starts with "  - " (list item)
            in_coins && /^[[:space:]]*-[[:space:]]/ {
                # If we had a previous coin without name, output it
                if (has_symbol && !has_name) {
                    print symbol_line ":" symbol
                }
                # Reset for new coin
                has_symbol=0; has_name=0; symbol=""; symbol_line=0
            }
            # Found name field
            in_coins && /^[[:space:]]+name:/ { has_name=1 }
            # Found symbol field
            in_coins && /^[[:space:]]+symbol:/ {
                has_symbol=1
                symbol_line=NR
                symbol=$2
                gsub(/["\047]/, "", symbol)  # Remove quotes
            }
            # End of coins section (next top-level key)
            in_coins && /^[a-z]+:/ && !/^[[:space:]]/ {
                if (has_symbol && !has_name) {
                    print symbol_line ":" symbol
                }
                exit
            }
            END {
                if (has_symbol && !has_name) {
                    print symbol_line ":" symbol
                }
            }
        ' "$CONFIG_FILE" 2>/dev/null || echo "")

        # Process each coin that needs a name field
        # IMPORTANT: Process in reverse line order so insertions don't shift later line numbers
        if [[ -n "$coin_fixes" ]]; then
            while IFS=: read -r line_num symbol; do
                [[ -z "$line_num" || -z "$symbol" ]] && continue
                # Map symbol to full name
                local coin_name=""
                case "$symbol" in
                    DGB) coin_name="DigiByte" ;;
                    BTC) coin_name="Bitcoin" ;;
                    BCH) coin_name="Bitcoin Cash" ;;
                    BC2) coin_name="Bitcoin II" ;;
                    LTC) coin_name="Litecoin" ;;
                    DOGE) coin_name="Dogecoin" ;;
                    DGB-SCRYPT) coin_name="DigiByte Scrypt" ;;
                    PEP) coin_name="PepeCoin" ;;
                    CAT) coin_name="Catcoin" ;;
                    NMC) coin_name="Namecoin" ;;
                    SYS) coin_name="Syscoin" ;;
                    XMY) coin_name="Myriad" ;;
                    FBTC) coin_name="Fractal Bitcoin" ;;
                    QBX) coin_name="Q-BitX" ;;
                    *) coin_name="${symbol//[^a-zA-Z0-9 _-]/}" ;;  # Sanitized symbol as fallback
                esac
                log_info "    - Adding missing 'name: ${coin_name}' after line ${line_num}"
                sed -i "${line_num}a\\    name: \"${coin_name}\"" "$CONFIG_FILE" || true
                FIXES_APPLIED=$((FIXES_APPLIED + 1))
            done < <(echo "$coin_fixes" | sort -t: -k1 -nr)
        fi
    fi

    # Fix 1: Coin name format
    if grep -q 'coin: "digibyte-sha256"' "$CONFIG_FILE" 2>/dev/null; then
        log_info "  - Fixing: coin name 'digibyte-sha256' -> 'digibyte'"
        sed -i 's/coin: "digibyte-sha256"/coin: "digibyte"/' "$CONFIG_FILE" || true
        FIXES_APPLIED=$((FIXES_APPLIED + 1))
    fi

    # Fix 2: Pool ID format (hyphens -> underscores for PostgreSQL)
    if grep -qE 'id:.*-sha256' "$CONFIG_FILE" 2>/dev/null; then
        log_info "  - Fixing: pool ID format (hyphens -> underscores)"
        sed -i 's/dgb-sha256-1/dgb_sha256_1/g; s/btc-sha256-1/btc_sha256_1/g; s/bch-sha256-1/bch_sha256_1/g' "$CONFIG_FILE" || true
        sed -i 's/dgb-sha256/dgb_sha256/g; s/btc-sha256/btc_sha256/g; s/bch-sha256/bch_sha256/g' "$CONFIG_FILE" || true
        FIXES_APPLIED=$((FIXES_APPLIED + 1))
    fi

    # Fix 3: Duration format - add 's' suffix to bare integers
    local DURATION_FIELDS=(
        "banDuration" "timeout" "keepaliveInterval" "shutdownTimeout"
        "jobRebroadcast" "interval" "reconnectInitial" "reconnectMax"
        "stabilityPeriod" "healthCheckInterval" "scanTimeout"
        "scanInterval" "heartbeatInterval" "failoverTimeout" "checkInterval"
        "slowlorisTimeout"
    )

    for field in "${DURATION_FIELDS[@]}"; do
        if grep -qE "^\s*${field}:\s*[0-9]+\s*$" "$CONFIG_FILE" 2>/dev/null; then
            log_info "  - Fixing: ${field} duration format (adding 's' suffix)"
            sed -i -E "s/^(\s*${field}:\s*)([0-9]+)\s*$/\1\2s/" "$CONFIG_FILE" || true
            FIXES_APPLIED=$((FIXES_APPLIED + 1))
        fi
    done

    # Fix 4: Remove WatchdogSec from systemd service (causes premature kills)
    local SERVICE_FILE="/etc/systemd/system/${STRATUM_SERVICE}.service"
    if [[ -f "$SERVICE_FILE" ]] && grep -q "WatchdogSec" "$SERVICE_FILE" 2>/dev/null; then
        log_info "  - Fixing: Removing WatchdogSec from systemd service"
        sed -i '/WatchdogSec/d' "$SERVICE_FILE" || true
        systemctl daemon-reload || true
        FIXES_APPLIED=$((FIXES_APPLIED + 1))
    fi

    # Fix 5: Ensure metrics section has enabled: true and listen: fields
    # The stratum needs these to expose Prometheus metrics for the dashboard
    if grep -q "^metrics:" "$CONFIG_FILE" 2>/dev/null; then
        # Metrics section exists - check if it has required fields
        if ! grep -A5 "^metrics:" "$CONFIG_FILE" | grep -q "enabled:"; then
            log_info "  - Adding: enabled: true to metrics section"
            sed -i '/^metrics:/a\  enabled: true' "$CONFIG_FILE" || true
            FIXES_APPLIED=$((FIXES_APPLIED + 1))
        fi
        if ! grep -A5 "^metrics:" "$CONFIG_FILE" | grep -q "listen:"; then
            log_info "  - Adding: listen: 0.0.0.0:9100 to metrics section"
            sed -i '/^metrics:/a\  listen: "0.0.0.0:9100"' "$CONFIG_FILE" || true
            FIXES_APPLIED=$((FIXES_APPLIED + 1))
        fi
    else
        # No metrics section - add complete section
        # Try to recover authToken from checkpoint file to preserve metrics auth
        local recovered_metrics_token=""
        local checkpoint_file="/var/lib/spiralpool/.checkpoint"
        if [[ -f "$checkpoint_file" ]]; then
            recovered_metrics_token=$(grep -oP '^METRICS_TOKEN="\K[^"]+' "$checkpoint_file" 2>/dev/null || echo "")
        fi
        log_info "  - Adding: Prometheus metrics section (required for dashboard)"
        if [[ -n "$recovered_metrics_token" ]]; then
            # Sanitize token for YAML safety and heredoc safety
            # Strip: newlines (break YAML), quotes (break YAML values),
            # $ and backtick (shell expansion in unquoted heredoc)
            recovered_metrics_token="${recovered_metrics_token//[$'\n\r']/}"
            recovered_metrics_token="${recovered_metrics_token//\"/}"
            recovered_metrics_token="${recovered_metrics_token//\$/}"
            recovered_metrics_token="${recovered_metrics_token//\`/}"
            log_info "  - Recovered metrics auth token from checkpoint"
            cat >> "$CONFIG_FILE" << METRICS_EOF

# Prometheus Metrics (added by upgrade.sh)
metrics:
  enabled: true
  listen: "0.0.0.0:9100"
  authToken: "$recovered_metrics_token"
METRICS_EOF
        else
            cat >> "$CONFIG_FILE" << 'METRICS_EOF'

# Prometheus Metrics (added by upgrade.sh)
metrics:
  enabled: true
  listen: "0.0.0.0:9100"
METRICS_EOF
        fi
        unset recovered_metrics_token
        FIXES_APPLIED=$((FIXES_APPLIED + 1))
    fi

    # Fix 6: Ensure api section exists (required for dashboard connection)
    if ! grep -q "^api:" "$CONFIG_FILE" 2>/dev/null; then
        # Try to recover adminApiKey from checkpoint file or config.yaml
        local recovered_api_key=""
        local checkpoint_file="/var/lib/spiralpool/.checkpoint"
        if [[ -f "$checkpoint_file" ]]; then
            recovered_api_key=$(grep -oP '^ADMIN_API_KEY="?\K[^"\s]+' "$checkpoint_file" 2>/dev/null || echo "")
        fi
        # Fallback: check if adminApiKey exists elsewhere in config.yaml (e.g. under a coin section)
        if [[ -z "$recovered_api_key" ]]; then
            recovered_api_key=$(grep -oP '(?:adminApiKey|admin_api_key):\s*"?\K[^"\s]+' "$CONFIG_FILE" 2>/dev/null | head -1 || echo "")
        fi
        log_info "  - Adding: API section (required for dashboard)"
        if [[ -n "$recovered_api_key" ]]; then
            # Sanitize key for YAML safety and heredoc safety
            # Strip: newlines (break YAML), quotes (break YAML values),
            # $ and backtick (shell expansion in unquoted heredoc)
            recovered_api_key="${recovered_api_key//[$'\n\r']/}"
            recovered_api_key="${recovered_api_key//\"/}"
            recovered_api_key="${recovered_api_key//\$/}"
            recovered_api_key="${recovered_api_key//\`/}"
            log_info "  - Recovered admin API key from checkpoint"
            cat >> "$CONFIG_FILE" << API_EOF

# API Server (added by upgrade.sh)
api:
  enabled: true
  listen: "0.0.0.0:4000"
  adminApiKey: "$recovered_api_key"
API_EOF
        else
            cat >> "$CONFIG_FILE" << 'API_EOF'

# API Server (added by upgrade.sh)
api:
  enabled: true
  listen: "0.0.0.0:4000"
API_EOF
        fi
        unset recovered_api_key
        FIXES_APPLIED=$((FIXES_APPLIED + 1))
    fi

    # Fix 7: Migrate admin API key from v1 format (api.adminApiKey) to v2 format (global.admin_api_key)
    # v1 config: api: { adminApiKey: "..." }
    # v2 config: global: { admin_api_key: "..." }
    # The v2 stratum binary only reads global.admin_api_key — v1 location is ignored.
    local v2_key=""
    local v1_key=""
    v2_key=$(grep -oP '^\s+admin_api_key:\s*"?\K[^"\s]+' "$CONFIG_FILE" 2>/dev/null | head -1 || echo "")
    v1_key=$(grep -oP '^\s+adminApiKey:\s*"?\K[^"\s]+' "$CONFIG_FILE" 2>/dev/null | head -1 || echo "")

    if [[ -z "$v2_key" ]]; then
        # v2 key missing — use v1 key if available, otherwise generate
        local new_key=""
        if [[ -n "$v1_key" ]]; then
            new_key="$v1_key"
            log_info "  - Migrating: adminApiKey (v1) → admin_api_key under global: (v2)"
        else
            local checkpoint_file="/var/lib/spiralpool/.checkpoint"
            if [[ -f "$checkpoint_file" ]]; then
                new_key=$(grep -oP '^ADMIN_API_KEY="?\K[^"\s]+' "$checkpoint_file" 2>/dev/null || echo "")
            fi
            if [[ -z "$new_key" ]]; then
                new_key=$(openssl rand -base64 24 | tr -dc 'a-zA-Z0-9' | head -c 32 || LC_ALL=C tr -dc 'a-zA-Z0-9' < /dev/urandom | head -c 32)
                log_info "  - Generating: admin_api_key (missing from config)"
                log_warn "  ┌──────────────────────────────────────────────────────────┐"
                log_warn "  │  NEW API KEY GENERATED — save this for dashboard access: │"
                log_warn "  │  $new_key  │"
                log_warn "  └──────────────────────────────────────────────────────────┘"
            else
                log_info "  - Recovering: admin_api_key from checkpoint"
            fi
        fi
        # Sanitize
        new_key="${new_key//[$'\n\r']/}"
        new_key="${new_key//\"/}"
        new_key="${new_key//\$/}"
        new_key="${new_key//\`/}"
        # Inject under global: (v2 location)
        if grep -q "^global:" "$CONFIG_FILE" 2>/dev/null; then
            sed -i "/^global:/a\\  admin_api_key: \"${new_key}\"" "$CONFIG_FILE" 2>/dev/null || true
        else
            # No global section — prepend it
            sed -i "1s/^/global:\n  admin_api_key: \"${new_key}\"\n/" "$CONFIG_FILE" 2>/dev/null || true
        fi
        unset new_key
        FIXES_APPLIED=$((FIXES_APPLIED + 1))
    fi
    unset v1_key v2_key

    # Fix 8: Sync admin API key to sentinel config.json so stratum kick doesn't get 401
    # Sentinel's config.json has its own pool_admin_api_key — if it holds a stale value
    # from v1, the auto-discovery from config.yaml is skipped (non-empty = truthy).
    local final_api_key=""
    final_api_key=$(grep -oP '^\s+admin_api_key:\s*"?\K[^"\s]+' "$CONFIG_FILE" 2>/dev/null | head -1 || echo "")
    if [[ -n "$final_api_key" ]]; then
        for sentinel_cfg in \
            "${INSTALL_DIR}/config/sentinel/config.json" \
            "/home/${POOL_USER}/.spiralsentinel/config.json"; do
            if [[ -f "$sentinel_cfg" ]]; then
                local old_sentinel_key=""
                old_sentinel_key=$(python3 -c "import json; d=json.load(open('$sentinel_cfg')); print(d.get('pool_admin_api_key',''))" 2>/dev/null || echo "")
                if [[ "$old_sentinel_key" != "$final_api_key" ]]; then
                    python3 -c "
import json, sys
cfg_path = '$sentinel_cfg'
try:
    with open(cfg_path) as f:
        d = json.load(f)
    d['pool_admin_api_key'] = '$final_api_key'
    with open(cfg_path, 'w') as f:
        json.dump(d, f, indent=2)
except Exception as e:
    print(f'Warning: could not update {cfg_path}: {e}', file=sys.stderr)
" 2>/dev/null || true
                    chown "${POOL_USER}:${POOL_USER}" "$sentinel_cfg" 2>/dev/null || true
                    log_info "  - Synced admin_api_key to $(basename $(dirname $sentinel_cfg))/config.json"
                    FIXES_APPLIED=$((FIXES_APPLIED + 1))
                fi
            fi
        done
    fi
    unset final_api_key

    if [[ $FIXES_APPLIED -gt 0 ]]; then
        # Restore ownership after modifications (sed -i changes owner to root)
        chown "${POOL_USER}:${POOL_USER}" "$CONFIG_FILE" 2>/dev/null || true
        chmod 600 "$CONFIG_FILE" 2>/dev/null || true
        log_success "Applied $FIXES_APPLIED config fixes"
    else
        log_info "  - No config fixes needed"
    fi
}

# Fix database ownership to ensure migrations work
# This is needed when tables were created by postgres user but app runs as spiralstratum
# Also grants schema creation privileges for Go binary table creation
fix_database_ownership() {
    local db_user="${1:-spiralstratum}"
    local db_name="${2:-spiralstratum}"

    # SECURITY: Validate db_user/db_name as safe SQL identifiers (alphanumeric + underscore only)
    # Prevents SQL injection if this function is ever called with untrusted input
    if [[ ! "$db_user" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]]; then
        log_error "Invalid database user: $db_user (must be alphanumeric)"
        return 1
    fi
    if [[ ! "$db_name" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]]; then
        log_error "Invalid database name: $db_name (must be alphanumeric)"
        return 1
    fi

    log_info "Ensuring database ownership is correct..."

    if ! systemctl is-active --quiet postgresql 2>/dev/null && ! systemctl is-active --quiet patroni 2>/dev/null; then
        log_warn "PostgreSQL not running, skipping ownership fix"
        return 0
    fi

    # HA: Skip on read-only replicas — DDL (ALTER OWNER, GRANT) requires writable primary.
    # Patroni replicates ownership changes from primary automatically.
    if systemctl is-active --quiet patroni 2>/dev/null; then
        local is_primary
        is_primary=$(sudo -u postgres psql -tAc "SELECT NOT pg_is_in_recovery();" 2>/dev/null || echo "unknown")
        if [[ "$is_primary" != "t" ]]; then
            log_info "  - Standby replica detected — skipping ownership fix (primary-only operation)"
            return 0
        fi
    fi

    # Check if database exists
    if ! sudo -u postgres psql -lqt | cut -d \| -f 1 | grep -qw "$db_name"; then
        log_info "Database $db_name does not exist, skipping ownership fix"
        return 0
    fi

    # Fix ownership of schema, tables, and sequences
    # Also grant CREATE privileges for Go binary to create tables on startup
    sudo -u postgres psql -d "$db_name" -q << OWNER_FIX_EOF 2>/dev/null || true
-- Transfer schema ownership (allows table creation)
ALTER SCHEMA public OWNER TO $db_user;

-- Grant schema privileges for table creation
GRANT CREATE ON SCHEMA public TO $db_user;
GRANT ALL PRIVILEGES ON SCHEMA public TO $db_user;

-- Grant default privileges for future tables/sequences
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO $db_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO $db_user;

-- Transfer existing table ownership (required for migrations)
DO \$\$
DECLARE
    tbl RECORD;
BEGIN
    FOR tbl IN SELECT tablename FROM pg_tables WHERE schemaname = 'public'
    LOOP
        EXECUTE format('ALTER TABLE public.%I OWNER TO %I', tbl.tablename, '$db_user');
    END LOOP;
END \$\$;

-- Transfer existing sequence ownership
DO \$\$
DECLARE
    seq RECORD;
BEGIN
    FOR seq IN SELECT sequencename FROM pg_sequences WHERE schemaname = 'public'
    LOOP
        EXECUTE format('ALTER SEQUENCE public.%I OWNER TO %I', seq.sequencename, '$db_user');
    END LOOP;
END \$\$;

-- Grant all privileges on existing objects
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO $db_user;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO $db_user;
OWNER_FIX_EOF

    log_success "Database ownership verified for user: $db_user"
}

# =============================================================================
# Download from GitHub (--fetch-latest mode)
# =============================================================================

# Setup GitHub authentication for private repos (terminal-only, no popups)
# SECURITY: Uses environment variables or prompts in terminal, never GUI dialogs
setup_github_auth() {
    local repo_url="$1"

    # Disable any GUI credential helpers - force terminal-only
    export GIT_TERMINAL_PROMPT=1
    export GIT_ASKPASS=""
    export SSH_ASKPASS=""
    export GCM_INTERACTIVE="never"

    # Check if repo is accessible without auth (public repo)
    if git ls-remote --quiet "$repo_url" HEAD &>/dev/null; then
        return 0  # Public repo, no auth needed
    fi

    # Check for existing credentials
    if [[ -n "${GITHUB_TOKEN:-}" ]]; then
        log_info "Using GITHUB_TOKEN environment variable"
        return 0
    fi

    if [[ -n "${GH_TOKEN:-}" ]]; then
        export GITHUB_TOKEN="$GH_TOKEN"
        _SP_SET_GH_TOKEN="true"
        log_info "Using GH_TOKEN environment variable"
        return 0
    fi

    # Check for gh CLI authentication
    if command -v gh &>/dev/null && gh auth status &>/dev/null; then
        log_info "Using GitHub CLI authentication"
        return 0
    fi

    # In auto mode, skip interactive prompt — proceed without auth
    if [[ "$AUTO_MODE" == "true" ]]; then
        log_warn "No GitHub credentials found for private repo (auto mode, skipping prompt)"
        return 0
    fi

    # Prompt for credentials in terminal (secure, no echo for token)
    echo -e "${YELLOW}GitHub authentication required for private repository${NC}"
    echo -e "${CYAN}Options:${NC}"
    echo "  1. Personal Access Token (recommended)"
    echo "  2. Username/Password (deprecated by GitHub)"
    echo "  3. Skip (if repo is public or you have SSH keys)"
    echo ""
    read -p "Select option [1-3]: " auth_choice

    case "$auth_choice" in
        1)
            echo -e "${CYAN}Enter your GitHub Personal Access Token${NC}"
            echo -e "${DIM}(Create one at: https://github.com/settings/tokens)${NC}"
            # SECURITY: -s flag prevents token from being displayed
            read -s -p "Token: " github_token
            echo ""
            if [[ -n "$github_token" ]]; then
                export GITHUB_TOKEN="$github_token"
                _SP_GH_USER="x-access-token"
                _SP_SET_GH_TOKEN="true"
                log_info "Token authentication configured"
                # SECURITY: Clear token from local variable after export
                unset github_token
            fi
            ;;
        2)
            echo -e "${YELLOW}Note: GitHub deprecated password auth. Use a token instead.${NC}"
            read -p "Username: " github_user
            read -s -p "Password/Token: " github_pass
            echo ""
            if [[ -n "$github_user" && -n "$github_pass" ]]; then
                export GITHUB_TOKEN="$github_pass"
                _SP_GH_USER="$github_user"
                _SP_SET_GH_TOKEN="true"
                log_info "Credential authentication configured"
                # SECURITY: Clear credentials from local variables
                unset github_pass github_user
            fi
            ;;
        3)
            log_info "Skipping authentication setup"
            ;;
        *)
            log_warn "Invalid option, proceeding without additional auth"
            ;;
    esac

    return 0
}

download_new_version() {
    log_info "Downloading new version from GitHub..."

    # SECURITY: Disable GUI credential prompts - terminal only
    export GIT_TERMINAL_PROMPT=1
    export GIT_ASKPASS=""
    export SSH_ASKPASS=""
    export GCM_INTERACTIVE="never"

    # Setup authentication if needed (prompts in terminal, not popup)
    setup_github_auth "$GITHUB_REPO"

    TEMP_DIR=$(mktemp -d "/tmp/spiralpool-upgrade-XXXXXX") || {
        log_error "Failed to create secure temporary directory"
        return 1
    }
    chmod 700 "${TEMP_DIR}"

    # SECURITY: If credentials are set, create a temporary GIT_ASKPASS script
    # so the token never appears on the command line (visible via ps aux).
    # Credential files go in a SEPARATE temp dir (not TEMP_DIR) because TEMP_DIR
    # is the git clone target and git refuses to clone into a non-empty directory.
    local askpass_file=""
    if [[ -n "${GITHUB_TOKEN:-}" ]]; then
        CRED_DIR=$(mktemp -d "/tmp/spiralpool-cred-XXXXXX") || {
            log_warn "Failed to create credential temp directory"
        }
        if [[ -n "$CRED_DIR" ]]; then
            chmod 700 "$CRED_DIR"
            askpass_file="${CRED_DIR}/.sp-askpass"
            # Write credentials to separate files (avoids heredoc expansion issues
            # with tokens containing shell metacharacters: ", $, `, \)
            ( umask 077 && printf '%s' "${_SP_GH_USER:-x-access-token}" > "${CRED_DIR}/.sp-user" )
            ( umask 077 && printf '%s' "${GITHUB_TOKEN}" > "${CRED_DIR}/.sp-pass" )
            ( umask 077 && cat > "$askpass_file" << 'ASKPASSEOF'
#!/bin/sh
SCRIPT_DIR="$(dirname "$0")"
case "$1" in
    Username*) cat "${SCRIPT_DIR}/.sp-user" ;;
    Password*) cat "${SCRIPT_DIR}/.sp-pass" ;;
esac
ASKPASSEOF
            )
            chmod 700 "$askpass_file"
            export GIT_ASKPASS="$askpass_file"
            # Disable other credential helpers that might interfere
            export GIT_TERMINAL_PROMPT=0
        fi
    fi

    local tag_version="v${TARGET_VERSION}"

    # Clone with credentials passed via GIT_ASKPASS (not in URL)
    if git clone --depth 1 --branch "$tag_version" "${GITHUB_REPO}" "${TEMP_DIR}" 2>/dev/null; then
        log_info "  - Downloaded release ${tag_version}"
    else
        log_warn "  - Tag ${tag_version} not found, trying main branch..."
        rm -rf "${TEMP_DIR:?}"
        TEMP_DIR=$(mktemp -d "/tmp/spiralpool-upgrade-XXXXXX") || {
            log_error "Failed to create temporary directory for fallback clone"
            [[ -n "$CRED_DIR" ]] && rm -rf "$CRED_DIR" && CRED_DIR=""
            [[ "${_SP_SET_GH_TOKEN:-}" == "true" ]] && unset GITHUB_TOKEN GIT_ASKPASS _SP_SET_GH_TOKEN _SP_GH_USER
            return 1
        }
        chmod 700 "${TEMP_DIR}"
        # Credential files persist in CRED_DIR (separate from TEMP_DIR), no recreation needed
        local clone_ok=false
        local clone_attempt
        for clone_attempt in 1 2 3; do
            if git clone --depth 1 "${GITHUB_REPO}" "${TEMP_DIR}" 2>/dev/null; then
                clone_ok=true
                break
            fi
            log_warn "  - Clone attempt $clone_attempt/3 failed"
            # Clean TEMP_DIR for retry (git refuses non-empty dir)
            rm -rf "${TEMP_DIR:?}"
            if [[ $clone_attempt -lt 3 ]]; then
                TEMP_DIR=$(mktemp -d "/tmp/spiralpool-upgrade-XXXXXX") || break
                chmod 700 "${TEMP_DIR}"
                sleep 5
            fi
        done
        if [[ "$clone_ok" != "true" ]]; then
            log_error "Failed to clone from GitHub after 3 attempts"
            log_error "If this is a private repo, ensure you have:"
            log_error "  - Set GITHUB_TOKEN environment variable, OR"
            log_error "  - Configured SSH keys for git@github.com, OR"
            log_error "  - Run 'gh auth login' if using GitHub CLI"
            # SECURITY: Clean up credential directory and env credentials
            [[ -n "$CRED_DIR" ]] && rm -rf "$CRED_DIR" && CRED_DIR=""
            [[ "${_SP_SET_GH_TOKEN:-}" == "true" ]] && unset GITHUB_TOKEN GIT_ASKPASS _SP_SET_GH_TOKEN _SP_GH_USER
            rm -rf "${TEMP_DIR}" 2>/dev/null
            return 1
        fi

        # Verify main branch source matches expected release version.
        # Without this check, the binary would be built from main-branch code
        # but stamped with the release tag version — a silent mismatch.
        if [[ -f "${TEMP_DIR}/VERSION" ]]; then
            local cloned_version
            cloned_version=$(tr -d '[:space:]' < "${TEMP_DIR}/VERSION" | sed 's/^v//')
            if [[ "$cloned_version" != "$TARGET_VERSION" ]]; then
                log_error "Version mismatch: GitHub release is v${TARGET_VERSION} but main branch contains ${cloned_version}"
                log_error "Release tag v${TARGET_VERSION} does not exist as a git tag — aborting download"
                [[ -n "$CRED_DIR" ]] && rm -rf "$CRED_DIR" && CRED_DIR=""
                [[ "${_SP_SET_GH_TOKEN:-}" == "true" ]] && unset GITHUB_TOKEN GIT_ASKPASS _SP_SET_GH_TOKEN _SP_GH_USER
                rm -rf "${TEMP_DIR}"
                return 1
            fi
            log_info "  - Main branch version verified: ${cloned_version}"
        else
            log_error "Downloaded source missing VERSION file — cannot verify source integrity"
            [[ -n "$CRED_DIR" ]] && rm -rf "$CRED_DIR" && CRED_DIR=""
            [[ "${_SP_SET_GH_TOKEN:-}" == "true" ]] && unset GITHUB_TOKEN GIT_ASKPASS _SP_SET_GH_TOKEN _SP_GH_USER
            rm -rf "${TEMP_DIR}"
            return 1
        fi
    fi

    # SECURITY: Clean up credential directory and clear env credentials
    [[ -n "$CRED_DIR" ]] && rm -rf "$CRED_DIR" && CRED_DIR=""
    [[ "${_SP_SET_GH_TOKEN:-}" == "true" ]] && unset GITHUB_TOKEN GIT_ASKPASS _SP_SET_GH_TOKEN _SP_GH_USER

    if [[ ! -d "${TEMP_DIR}/src/stratum" ]]; then
        log_error "Download verification failed - source not found"
        rm -rf "${TEMP_DIR}"
        return 1
    fi

    PROJECT_ROOT="$TEMP_DIR"
    log_success "Downloaded to ${TEMP_DIR}"
    return 0
}

# =============================================================================
# Component Updates
# =============================================================================

build_stratum() {
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${MAGENTA}  STRATUM BUILD${NC}"
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

    # Ensure TEMP_DIR exists for build outputs (created by mktemp for security)
    if [[ -z "$TEMP_DIR" ]] || [[ ! -d "$TEMP_DIR" ]]; then
        TEMP_DIR=$(mktemp -d "/tmp/spiralpool-upgrade-XXXXXX") || {
            log_error "Failed to create secure temporary directory"
            exit 1
        }
        chmod 700 "${TEMP_DIR}"
    fi

    local STRATUM_SOURCE="$PROJECT_ROOT/src/stratum"

    if [[ ! -f "$STRATUM_SOURCE/go.mod" ]]; then
        log_error "Stratum source not found at $STRATUM_SOURCE"
        exit 1
    fi

    # Check Go is installed - first check /usr/local/go (installed by install.sh)
    # then fall back to system Go, then install if needed
    if [[ -x /usr/local/go/bin/go ]]; then
        export PATH="/usr/local/go/bin:$PATH"
        log_info "Using Go from /usr/local/go/bin"
    elif ! command -v go &> /dev/null; then
        log_info "Go is not installed - installing automatically..."
        if command -v apt &> /dev/null; then
            apt update -qq && apt install -y -qq golang-go || {
                log_error "Failed to install Go via apt"
                exit 1
            }
        else
            log_error "Go is not installed and apt is not available"
            exit 1
        fi
    fi

    local GO_VER
    GO_VER=$(go version 2>/dev/null | grep -oP '\d+\.\d+' | head -1) || true
    echo -e "${CYAN}Go version:${NC} $(go version 2>/dev/null || echo 'unknown')"

    # Check Go version meets minimum requirement (1.26+ required by go.mod)
    local GO_MAJOR="${GO_VER%%.*}"
    local GO_MINOR="${GO_VER##*.}"
    if [[ -z "$GO_MAJOR" || -z "$GO_MINOR" ]]; then
        log_warn "Could not determine Go version — proceeding with build"
    elif [[ "$GO_MAJOR" -lt 1 ]] || { [[ "$GO_MAJOR" -eq 1 ]] && [[ "$GO_MINOR" -lt 26 ]]; }; then
        log_warn "Go ${GO_VER} is below 1.26 (required by go.mod). Installing Go 1.26.1..."
        local SYSTEM_ARCH_GO
        SYSTEM_ARCH_GO=$(dpkg --print-architecture 2>/dev/null || echo "amd64")
        [[ "$SYSTEM_ARCH_GO" == "arm64" ]] || SYSTEM_ARCH_GO="amd64"
        log_info "Downloading Go 1.26.1 (this may take a minute)..."
        if curl -fSL --connect-timeout 15 --max-time 300 "https://go.dev/dl/go1.26.1.linux-${SYSTEM_ARCH_GO}.tar.gz" -o "/tmp/go1.26.1.linux-${SYSTEM_ARCH_GO}.tar.gz"; then
            sudo rm -rf /usr/local/go
            sudo tar -C /usr/local -xzf "/tmp/go1.26.1.linux-${SYSTEM_ARCH_GO}.tar.gz"
            rm -f "/tmp/go1.26.1.linux-${SYSTEM_ARCH_GO}.tar.gz"
            export PATH="/usr/local/go/bin:$PATH"
            log_info "Go 1.26.1 installed successfully"
        else
            log_warn "Failed to download Go 1.26.1 — build may fail"
            log_warn "Manual fix: sudo rm -rf /usr/local/go && curl -fSL https://go.dev/dl/go1.26.1.linux-${SYSTEM_ARCH_GO}.tar.gz | sudo tar -C /usr/local -xzf -"
        fi
    fi

    # Build new stratum binary
    log_info "Building new stratum binary..."
    local BUILD_OUTPUT="${TEMP_DIR}/stratum-build"

    local git_commit="unknown"
    if [[ -d "${STRATUM_SOURCE}/.git" ]] || [[ -d "${PROJECT_ROOT}/.git" ]]; then
        git_commit=$(git -C "${PROJECT_ROOT}" rev-parse --short HEAD 2>/dev/null || echo "unknown")
    fi
    local ldflags="-X main.Version=${TARGET_VERSION} -X main.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.GitCommit=${git_commit} -X github.com/spiralpool/stratum/internal/ha.SpiralPoolVersion=${TARGET_VERSION}"

    # ZMQ support: The stratum uses go-zeromq/zmq4 (pure Go, no C library needed).
    # Always build with ZMQ enabled — the nozmq tag is only for minimal/test builds.
    # -buildvcs=false: disable automatic VCS stamping — git commit is already set via
    # ldflags above, and the source may be a zip extract with no .git directory.
    local build_tags="-buildvcs=false"
    log_info "  - Building with ZMQ support (pure Go implementation)"

    # Build as non-root if possible (sandboxed)
    # NOTE: All builds use explicit cd in subshell/subprocess to avoid changing main script CWD
    if [[ "$EUID" -eq 0 ]] && id "$POOL_USER" &>/dev/null; then
        log_info "  - Building as non-privileged user '${POOL_USER}' (sandboxed)..."
        chown -R "${POOL_USER}:${POOL_USER}" "$PROJECT_ROOT"
        if ! sudo -u "$POOL_USER" bash -c "cd '${STRATUM_SOURCE}' && go build ${build_tags} -ldflags '${ldflags}' -o '${BUILD_OUTPUT}' ./cmd/spiralpool/"; then
            log_warn "Sandboxed build failed, trying as root..."
            ( cd "$STRATUM_SOURCE" && go build ${build_tags} -ldflags "${ldflags}" -o "${BUILD_OUTPUT}" ./cmd/spiralpool/ ) || {
                log_error "Build failed!"
                exit 1
            }
        fi
    else
        ( cd "$STRATUM_SOURCE" && go build ${build_tags} -ldflags "${ldflags}" -o "${BUILD_OUTPUT}" ./cmd/spiralpool/ ) || {
            log_error "Build failed!"
            exit 1
        }
    fi
    log_success "Stratum binary built successfully"

    # Build spiralctl control utility (CGO_ENABLED=0 — no ZMQ needed)
    log_info "Building spiralctl..."
    SPIRALCTL_OUTPUT="${TEMP_DIR}/spiralctl-build"
    if [[ "$EUID" -eq 0 ]] && id "$POOL_USER" &>/dev/null; then
        sudo -u "$POOL_USER" bash -c "cd '${STRATUM_SOURCE}' && CGO_ENABLED=0 go build -buildvcs=false -ldflags '${ldflags}' -o '${SPIRALCTL_OUTPUT}' ./cmd/spiralctl/" 2>/dev/null || \
            ( cd "$STRATUM_SOURCE" && CGO_ENABLED=0 go build -buildvcs=false -ldflags "${ldflags}" -o "${SPIRALCTL_OUTPUT}" ./cmd/spiralctl/ ) || \
            log_warn "spiralctl build failed (non-fatal)"
    else
        ( cd "$STRATUM_SOURCE" && CGO_ENABLED=0 go build -buildvcs=false -ldflags "${ldflags}" -o "${SPIRALCTL_OUTPUT}" ./cmd/spiralctl/ ) || \
            log_warn "spiralctl build failed (non-fatal)"
    fi

    log_success "Build phase complete — binaries ready for deployment"
    echo
}

deploy_stratum() {
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${MAGENTA}  STRATUM DEPLOY${NC}"
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

    local STRATUM_BINARY="${INSTALL_DIR}/bin/spiralstratum"
    local LEGACY_BINARY="/usr/local/bin/stratum"
    local BUILD_OUTPUT="${TEMP_DIR}/stratum-build"

    # Deploy spiralctl
    if [[ -f "${TEMP_DIR}/spiralctl-build" ]]; then
        # Atomic install: copy to same-directory temp, then rename.
        # Direct mv from /tmp may cross filesystems (non-atomic copy+delete).
        cp "${TEMP_DIR}/spiralctl-build" "${INSTALL_DIR}/bin/spiralctl.new"
        chmod +x "${INSTALL_DIR}/bin/spiralctl.new"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/bin/spiralctl.new"
        mv -f "${INSTALL_DIR}/bin/spiralctl.new" "${INSTALL_DIR}/bin/spiralctl"
        log_success "spiralctl deployed"
    fi

    # Deploy stratum binary
    log_info "Installing new stratum binary..."
    if [[ ! -f "$BUILD_OUTPUT" ]]; then
        log_error "Build output not found at $BUILD_OUTPUT — build may have failed silently"
        exit 1
    fi
    mkdir -p "$(dirname "$STRATUM_BINARY")"
    # Atomic install: copy to same-directory temp, then rename.
    # Direct mv from /tmp may cross filesystems (non-atomic copy+delete).
    cp "$BUILD_OUTPUT" "${STRATUM_BINARY}.new"
    chmod +x "${STRATUM_BINARY}.new"
    chown "${POOL_USER}:${POOL_USER}" "${STRATUM_BINARY}.new"
    mv -f "${STRATUM_BINARY}.new" "$STRATUM_BINARY"

    # Also update legacy location if it exists
    if [[ -f "$LEGACY_BINARY" ]] || [[ -L "$LEGACY_BINARY" ]]; then
        cp "$STRATUM_BINARY" "$LEGACY_BINARY"
        chmod +x "$LEGACY_BINARY"
        chown root:root "$LEGACY_BINARY"
    fi

    local NEW_VERSION=$("$STRATUM_BINARY" --version 2>/dev/null || echo "unknown")
    log_success "Stratum deployed: $NEW_VERSION"
    echo
}

update_dashboard() {
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${MAGENTA}  DASHBOARD UPDATE${NC}"
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

    local DASHBOARD_SOURCE="$PROJECT_ROOT/src/dashboard"

    # Detect where the dashboard is actually installed (check systemd service)
    local DASHBOARD_INSTALL="$INSTALL_DIR/dashboard"
    if [[ -f "/etc/systemd/system/spiraldash.service" ]]; then
        local detected_dir=$(grep -oP 'WorkingDirectory=\K[^\s]+' "/etc/systemd/system/spiraldash.service" 2>/dev/null || echo "")
        [[ -n "$detected_dir" ]] && [[ -d "$detected_dir" ]] && DASHBOARD_INSTALL="$detected_dir"
    fi
    # Fallback: check common locations
    if [[ ! -d "$DASHBOARD_INSTALL" ]]; then
        if [[ -d "$INSTALL_DIR/dashboard" ]]; then
            DASHBOARD_INSTALL="$INSTALL_DIR/dashboard"
        elif [[ -d "$INSTALL_DIR/src/dashboard" ]]; then
            DASHBOARD_INSTALL="$INSTALL_DIR/src/dashboard"
        fi
    fi
    log_info "Dashboard install directory: $DASHBOARD_INSTALL"

    if [[ ! -f "$DASHBOARD_SOURCE/dashboard.py" ]]; then
        log_warn "Dashboard source not found at $DASHBOARD_SOURCE, skipping..."
        return
    fi

    # Stop dashboard service and kill any orphaned gunicorn processes
    log_info "[1/3] Stopping dashboard service..."
    local dashboard_was_running=false
    if systemctl is-active --quiet "$DASHBOARD_SERVICE" 2>/dev/null; then
        dashboard_was_running=true
        systemctl stop "$DASHBOARD_SERVICE" || true
        sleep 1
    fi
    # Kill any orphaned gunicorn processes holding the port
    pkill -9 -f "gunicorn.*dashboard:app" 2>/dev/null || true
    # Detect actual dashboard port from service file (don't assume 1618)
    local dash_port
    dash_port=$(grep -oP '0\.0\.0\.0:\K[0-9]+' /etc/systemd/system/spiraldash.service 2>/dev/null | head -1)
    [[ -z "$dash_port" ]] && dash_port="$DASHBOARD_PORT"
    fuser -k "${dash_port}/tcp" 2>/dev/null || true
    sleep 1

    # Update dashboard files
    log_info "[2/3] Updating dashboard files..."
    mkdir -p "$DASHBOARD_INSTALL"

    # Copy Python files
    cp "$DASHBOARD_SOURCE"/*.py "$DASHBOARD_INSTALL/" 2>/dev/null || true
    cp "$DASHBOARD_SOURCE/requirements.txt" "$DASHBOARD_INSTALL/" 2>/dev/null || true

    # Copy templates (overwrite) - atomic swap prevents data loss if cp fails
    if [[ -d "$DASHBOARD_SOURCE/templates" ]]; then
        if cp -r "$DASHBOARD_SOURCE/templates" "$DASHBOARD_INSTALL/templates.new"; then
            rm -rf "$DASHBOARD_INSTALL/templates"
            mv "$DASHBOARD_INSTALL/templates.new" "$DASHBOARD_INSTALL/templates"
        else
            log_error "Failed to copy new templates (disk full?) — keeping existing"
            rm -rf "$DASHBOARD_INSTALL/templates.new" 2>/dev/null
        fi
    else
        log_warn "New templates directory not found, keeping existing templates"
    fi

    # Copy static assets
    mkdir -p "$DASHBOARD_INSTALL/static"
    cp -r "$DASHBOARD_SOURCE/static/css" "$DASHBOARD_INSTALL/static/" 2>/dev/null || true
    cp -r "$DASHBOARD_SOURCE/static/templates" "$DASHBOARD_INSTALL/static/" 2>/dev/null || true

    # Merge themes (don't overwrite user custom themes)
    mkdir -p "$DASHBOARD_INSTALL/static/themes"
    for theme in "$DASHBOARD_SOURCE/static/themes"/*.json; do
        [[ -f "$theme" ]] && cp "$theme" "$DASHBOARD_INSTALL/static/themes/"
    done

    # Ensure dashboard data directory exists (for auth.json, secret_key)
    mkdir -p "$DASHBOARD_INSTALL/data"
    chown "${POOL_USER}:${POOL_USER}" "$DASHBOARD_INSTALL/data"
    chmod 700 "$DASHBOARD_INSTALL/data"

    # Ensure shared data directory exists
    local SHARED_DATA_DIR="${INSTALL_DIR}/data"
    mkdir -p "$SHARED_DATA_DIR"
    chown "${POOL_USER}:${POOL_USER}" "$SHARED_DATA_DIR"
    chmod 775 "$SHARED_DATA_DIR"

    # Add admin user to pool user's group for shared access
    local ADMIN_USER="${SUDO_USER:-}"
    if [[ -n "$ADMIN_USER" ]] && [[ "$ADMIN_USER" != "root" ]]; then
        usermod -aG "${POOL_USER}" "${ADMIN_USER}" 2>/dev/null || true
    fi

    # Fix ownership
    chown -R "${POOL_USER}:${POOL_USER}" "$DASHBOARD_INSTALL"

    # Update Python dependencies (use venv, create one if missing)
    log_info "[3/3] Updating Python dependencies..."
    if [[ -f "$DASHBOARD_INSTALL/requirements.txt" ]]; then
        if [[ ! -f "$DASHBOARD_INSTALL/venv/bin/pip" ]]; then
            log_info "  - Creating dashboard venv (missing from older install)..."
            sudo -u "$POOL_USER" python3 -m venv "$DASHBOARD_INSTALL/venv"
            sudo -u "$POOL_USER" "$DASHBOARD_INSTALL/venv/bin/pip" install --upgrade pip -q 2>/dev/null
        fi
        sudo -u "$POOL_USER" "$DASHBOARD_INSTALL/venv/bin/pip" install -q -r "$DASHBOARD_INSTALL/requirements.txt" 2>/dev/null || {
            log_warn "  - Failed to install dashboard Python dependencies"
        }
    fi

    # Ensure sudoers is configured for dashboard service control
    local DASH_SUDOERS="/etc/sudoers.d/spiralpool-dashboard"
    if [[ ! -f "$DASH_SUDOERS" ]]; then
        log_info "  - Configuring dashboard service control permissions..."
        cat > "$DASH_SUDOERS" << SUDOERS_EOF
# Spiral Pool Dashboard - Service control permissions
# Allows the dashboard and wallet generator to control pool services

# Pool service control (stratum + sentinel) - start, stop, restart, enable
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl start spiralstratum
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl stop spiralstratum
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart spiralstratum
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl enable spiralstratum
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart spiralstratum spiralsentinel
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart spiralsentinel

# Blockchain node restart (for settings changes) - all supported coins
# SHA-256d coins
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart digibyted
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart bitcoind
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart bitcoind-bch
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart bitcoiniid
# SHA-256d merge-mineable coins (AuxPoW)
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart namecoind
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart syscoind
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart myriadcoind
# Scrypt coins
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart litecoind
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart dogecoind
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart pepecoind
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart catcoind
# Fractal Bitcoin (AuxPoW merge-mineable with BTC)
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart fractald
# Q-BitX
$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart qbitxd
SUDOERS_EOF
        chmod 440 "$DASH_SUDOERS"
        chown root:root "$DASH_SUDOERS"
        if visudo -c -f "$DASH_SUDOERS" > /dev/null 2>&1; then
            log_info "  - Dashboard can now restart services from web UI"
        else
            log_warn "  - Sudoers syntax error, removing"
            rm -f "$DASH_SUDOERS"
        fi
    fi

    log_success "Dashboard updated"

    # Start dashboard service if it was running before (unless skip-start)
    if [[ "$SKIP_START" == "false" ]] && [[ "$dashboard_was_running" == "true" ]]; then
        systemctl start --no-block "$DASHBOARD_SERVICE" || log_warn "Failed to start $DASHBOARD_SERVICE"
        log_info "  - Dashboard service starting"
    fi
    echo
}

update_sentinel() {
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${MAGENTA}  SPIRAL SENTINEL UPDATE${NC}"
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

    local SENTINEL_SOURCE="$PROJECT_ROOT/src/sentinel"
    local SENTINEL_BIN="$INSTALL_DIR/bin/SpiralSentinel.py"

    if [[ ! -f "$SENTINEL_SOURCE/SpiralSentinel.py" ]]; then
        log_warn "Sentinel source not found at $SENTINEL_SOURCE, skipping..."
        return
    fi

    # Stop sentinel service
    log_info "[1/3] Stopping sentinel service..."
    local sentinel_was_running=false
    if systemctl is-active --quiet "$SENTINEL_SERVICE" 2>/dev/null; then
        sentinel_was_running=true
        systemctl stop "$SENTINEL_SERVICE" || true
        sleep 1
    fi

    # Update sentinel binary
    log_info "[2/3] Updating sentinel..."
    mkdir -p "$INSTALL_DIR/bin"
    chown "$POOL_USER:$POOL_USER" "$INSTALL_DIR/bin" 2>/dev/null || true
    chmod 775 "$INSTALL_DIR/bin" 2>/dev/null || true
    cp "$SENTINEL_SOURCE/SpiralSentinel.py" "$SENTINEL_BIN"
    chmod +x "$SENTINEL_BIN"

    # Copy HA manager module if present
    [[ -f "$SENTINEL_SOURCE/ha_manager.py" ]] && cp "$SENTINEL_SOURCE/ha_manager.py" "$INSTALL_DIR/bin/"

    # Fix ownership
    chown "$POOL_USER:$POOL_USER" "$SENTINEL_BIN" 2>/dev/null || true
    [[ -f "$INSTALL_DIR/bin/ha_manager.py" ]] && chown "$POOL_USER:$POOL_USER" "$INSTALL_DIR/bin/ha_manager.py"

    # Ensure fallback config directory exists with correct ownership
    # systemd's ProtectHome=yes blocks ~/.spiralsentinel — sentinel falls back to this dir
    # Directory must be owned by POOL_USER for state/history temp file creation (mkstemp)
    local SENTINEL_FALLBACK_DIR="$INSTALL_DIR/config/sentinel"
    mkdir -p "$SENTINEL_FALLBACK_DIR" 2>/dev/null || true
    chown "$POOL_USER:$POOL_USER" "$SENTINEL_FALLBACK_DIR" 2>/dev/null || true

    # Fix service file for proper logging and environment (only with --update-services or --full)
    if $UPDATE_SERVICES; then
        local SENTINEL_SERVICE_FILE="/etc/systemd/system/${SENTINEL_SERVICE}.service"
        local SERVICE_MODIFIED=false
        if [[ -f "$SENTINEL_SERVICE_FILE" ]]; then
            # Ensure HOME environment is set (fixes Path.home() for config location)
            if ! grep -q 'Environment="HOME=' "$SENTINEL_SERVICE_FILE"; then
                log_info "  - Adding HOME environment to service file..."
                sed -i '/^\[Service\]/a Environment="HOME=/home/'"$POOL_USER"'"' "$SENTINEL_SERVICE_FILE"
                SERVICE_MODIFIED=true
            fi

            # Ensure PYTHONUNBUFFERED is set for immediate log output
            if ! grep -q 'PYTHONUNBUFFERED' "$SENTINEL_SERVICE_FILE"; then
                log_info "  - Adding PYTHONUNBUFFERED for real-time logging..."
                sed -i '/^\[Service\]/a Environment="PYTHONUNBUFFERED=1"' "$SENTINEL_SERVICE_FILE"
                SERVICE_MODIFIED=true
            fi

            # Ensure StandardOutput=journal is set
            if ! grep -q 'StandardOutput=journal' "$SENTINEL_SERVICE_FILE"; then
                log_info "  - Adding StandardOutput=journal..."
                sed -i '/^\[Service\]/a StandardOutput=journal\nStandardError=journal' "$SENTINEL_SERVICE_FILE"
                SERVICE_MODIFIED=true
            fi

            # Ensure -u flag is in ExecStart for unbuffered Python
            if grep -q 'ExecStart=.*python3 [^-]' "$SENTINEL_SERVICE_FILE"; then
                log_info "  - Adding -u flag for unbuffered Python output..."
                sed -i 's|ExecStart=\(.*python3\) \(.*SpiralSentinel.py\)|ExecStart=\1 -u \2|' "$SENTINEL_SERVICE_FILE"
                SERVICE_MODIFIED=true
            fi

            if [[ "$SERVICE_MODIFIED" == "true" ]]; then
                systemctl daemon-reload || true
            fi
        fi
    fi

    log_success "Sentinel updated"

    # Start sentinel service if it was running before (unless skip-start)
    if [[ "$SKIP_START" == "false" ]] && [[ "$sentinel_was_running" == "true" ]]; then
        log_info "[3/3] Starting sentinel service..."
        systemctl start --no-block "$SENTINEL_SERVICE" || log_warn "Failed to start $SENTINEL_SERVICE"
        log_info "  - Sentinel service starting"
    fi
    echo
}

update_systemd_services() {
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${MAGENTA}  SYSTEMD SERVICE FILES UPDATE${NC}"
    echo -e "${MAGENTA}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

    local SYSTEMD_TEMPLATES="$PROJECT_ROOT/scripts/linux/systemd"
    local SYSTEMD_DIR="/etc/systemd/system"

    if [[ ! -d "$SYSTEMD_TEMPLATES" ]]; then
        log_warn "Systemd templates not found, skipping..."
        return
    fi

    # Detect daemon type from config.yaml or existing service file
    # Supports all coins: DGB, DGB-SCRYPT, BTC, BCH, BC2, LTC, DOGE, PEP, CAT, NMC, SYS, XMY, FBTC, QBX
    local DETECTED_DAEMON=""

    # First, try to detect from config.yaml (most reliable)
    if [[ -f "${INSTALL_DIR}/config/config.yaml" ]]; then
        local coin_type=$(grep -E "^\s*coin:" "${INSTALL_DIR}/config/config.yaml" 2>/dev/null | head -1 | sed 's/.*coin:\s*"\?\([^"]*\)"\?.*/\1/')
        case "$coin_type" in
            digibyte|digibyte-scrypt) DETECTED_DAEMON="digibyted" ;;
            bitcoin) DETECTED_DAEMON="bitcoind" ;;
            bitcoincash) DETECTED_DAEMON="bitcoind-bch" ;;
            bitcoinii) DETECTED_DAEMON="bitcoiniid" ;;
            litecoin) DETECTED_DAEMON="litecoind" ;;
            dogecoin) DETECTED_DAEMON="dogecoind" ;;
            pepecoin) DETECTED_DAEMON="pepecoind" ;;
            catcoin) DETECTED_DAEMON="catcoind" ;;
            namecoin) DETECTED_DAEMON="namecoind" ;;
            syscoin) DETECTED_DAEMON="syscoind" ;;
            myriad|myriadcoin) DETECTED_DAEMON="myriadcoind" ;;
            fractal|fractalbitcoin|fractal-bitcoin) DETECTED_DAEMON="fractald" ;;
            qbitx|q-bitx|qbitxcoin) DETECTED_DAEMON="qbitxd" ;;
        esac
    fi

    # Fallback: detect from existing service file
    if [[ -z "$DETECTED_DAEMON" ]] && [[ -f "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" ]]; then
        if grep -q "bitcoiniid" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="bitcoiniid"
        elif grep -q "bitcoind-bch\|bitcoincashd" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="bitcoind-bch"
        elif grep -q "bitcoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="bitcoind"
        elif grep -q "litecoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="litecoind"
        elif grep -q "dogecoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="dogecoind"
        elif grep -q "pepecoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="pepecoind"
        elif grep -q "catcoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="catcoind"
        elif grep -q "namecoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="namecoind"
        elif grep -q "syscoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="syscoind"
        elif grep -q "myriadcoind" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="myriadcoind"
        elif grep -q "fractald" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="fractald"
        elif grep -q "digibyted" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="digibyted"
        elif grep -q "qbitxd" "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null; then
            DETECTED_DAEMON="qbitxd"
        fi
    fi

    # Ultimate fallback: check which daemon services exist
    if [[ -z "$DETECTED_DAEMON" ]]; then
        for daemon in bitcoiniid bitcoind-bch bitcoind litecoind dogecoind pepecoind catcoind namecoind syscoind myriadcoind fractald digibyted qbitxd; do
            if [[ -f "/etc/systemd/system/${daemon}.service" ]]; then
                DETECTED_DAEMON="$daemon"
                break
            fi
        done
    fi

    # No daemon detected — log a warning; upgrade continues but daemon-specific steps will be skipped
    [[ -z "$DETECTED_DAEMON" ]] && log_warn "Could not detect installed coin daemon — daemon-specific steps will be skipped"

    log_info "Detected daemon: $DETECTED_DAEMON"

    # Detect dashboard port from existing service or config (updates script-level DASHBOARD_PORT)
    DASHBOARD_PORT="1618"
    if [[ -f "$SYSTEMD_DIR/spiraldash.service" ]]; then
        local existing_port=$(grep -oP '0\.0\.0\.0:\K[0-9]+' "$SYSTEMD_DIR/spiraldash.service" 2>/dev/null | head -1)
        [[ -n "$existing_port" && "$existing_port" != "5000" ]] && DASHBOARD_PORT="$existing_port"
    fi

    # Detect METRICS_TOKEN from existing dashboard service (preserve across upgrades)
    local METRICS_TOKEN=""
    if [[ -f "$SYSTEMD_DIR/spiraldash.service" ]]; then
        METRICS_TOKEN=$(grep -oP 'SPIRAL_METRICS_TOKEN=\K[^"]*' "$SYSTEMD_DIR/spiraldash.service" 2>/dev/null | head -1)
    fi
    # Fallback: try checkpoint file
    if [[ -z "$METRICS_TOKEN" ]] && [[ -f "/var/lib/spiralpool/.checkpoint" ]]; then
        METRICS_TOKEN=$(grep -oP '^METRICS_TOKEN="\K[^"]+' "/var/lib/spiralpool/.checkpoint" 2>/dev/null || echo "")
    fi
    # Last resort: generate a new token (upgrading from pre-metrics installation)
    if [[ -z "$METRICS_TOKEN" ]]; then
        METRICS_TOKEN=$(openssl rand -base64 24 2>/dev/null || head -c 24 /dev/urandom | base64 2>/dev/null || echo "")
        if [[ -n "$METRICS_TOKEN" ]]; then
            log_info "  - Generated new METRICS_TOKEN (not found in old service or checkpoint)"
        fi
    fi

    # Sanitize METRICS_TOKEN for sed safety (strip characters that break sed replacement:
    # | is our delimiter, & inserts matched text, \ is escape character)
    METRICS_TOKEN="${METRICS_TOKEN//|/}"
    METRICS_TOKEN="${METRICS_TOKEN//&/}"
    METRICS_TOKEN="${METRICS_TOKEN//\\/}"

    # Detect multi-coin Wants/After dependencies from existing stratum service
    # Single-coin installs have one daemon; multi-coin have space-separated list
    local WANTS_DEPS=""
    if [[ -f "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" ]]; then
        # Extract all daemon services from the Wants= line (everything after Wants=)
        local existing_wants
        existing_wants=$(grep -oP '^Wants=\K.*' "$SYSTEMD_DIR/${STRATUM_SERVICE}.service" 2>/dev/null | head -1)
        # Remove the primary daemon (already in {{DAEMON_SERVICE}}) to get extra deps
        if [[ -n "$existing_wants" ]]; then
            WANTS_DEPS=$(echo "$existing_wants" | sed "s|${DETECTED_DAEMON}\.service||g" | xargs)
        fi
    fi

    # Sanitize WANTS_DEPS for sed safety (same rationale as METRICS_TOKEN above)
    WANTS_DEPS="${WANTS_DEPS//|/}"
    WANTS_DEPS="${WANTS_DEPS//&/}"
    WANTS_DEPS="${WANTS_DEPS//\\/}"

    # Update service files
    if [[ -z "$DETECTED_DAEMON" ]]; then
        log_warn "Skipping service file update — daemon unknown. Existing service files preserved."
    fi
    for template in spiralstratum spiraldash spiralsentinel spiralpool-health spiralpool-ha-watcher; do
        if [[ -f "$SYSTEMD_TEMPLATES/${template}.service" ]] && [[ -n "$DETECTED_DAEMON" ]]; then
            sed -e "s|{{INSTALL_DIR}}|$INSTALL_DIR|g" \
                -e "s|{{POOL_USER}}|$POOL_USER|g" \
                -e "s|{{DAEMON_SERVICE}}|$DETECTED_DAEMON|g" \
                -e "s|{{DASHBOARD_PORT}}|$DASHBOARD_PORT|g" \
                -e "s|{{METRICS_TOKEN}}|$METRICS_TOKEN|g" \
                -e "s|{{WANTS_DEPS}}|$WANTS_DEPS|g" \
                "$SYSTEMD_TEMPLATES/${template}.service" > "$SYSTEMD_DIR/${template}.service"
            log_info "  - Updated: ${template}.service"
        fi
    done

    # HA: Replace postgresql.service dependency with patroni.service.
    # Templates hardcode postgresql.service, but on HA installations Patroni manages
    # PostgreSQL and postgresql.service is disabled. Without this, services fail to start
    # after upgrade because systemd can't satisfy Requires/Wants=postgresql.service.
    if systemctl is-enabled --quiet patroni 2>/dev/null; then
        for ha_svc in spiralstratum spiralpool-health; do
            if [[ -f "$SYSTEMD_DIR/${ha_svc}.service" ]]; then
                sed -i 's/postgresql\.service/patroni.service/g' "$SYSTEMD_DIR/${ha_svc}.service"
            fi
        done
        log_info "  - HA detected: postgresql.service → patroni.service in stratum + health units"
    fi

    systemctl daemon-reload || true
    log_success "Systemd service files updated"
    echo
}

update_utility_scripts() {
    log_info "Updating utility scripts..."
    local SCRIPTS_SRC="$PROJECT_ROOT/scripts"
    local SCRIPTS_DST="$INSTALL_DIR/scripts"
    local updated=0

    mkdir -p "$SCRIPTS_DST"
    # SECURITY: Directory owned by root to prevent spiraluser from adding scripts
    # that could be executed via sudoers NOPASSWD entries (privilege escalation).
    chown root:root "$SCRIPTS_DST" 2>/dev/null || true
    chmod 755 "$SCRIPTS_DST" 2>/dev/null || true

    # spiralctl control utility (shell dispatcher — always symlinked)
    # Note: /usr/local/bin/spiralctl is the shell wrapper that dispatches all commands.
    # /spiralpool/bin/spiralctl is the Go binary for mining/pool/external/gdpr-delete.
    # The shell wrapper delegates to the Go binary when needed — both must coexist.
    if [[ -f "$SCRIPTS_SRC/spiralctl.sh" ]]; then
        cp "$SCRIPTS_SRC/spiralctl.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/spiralctl.sh"
        ln -sf "$SCRIPTS_DST/spiralctl.sh" /usr/local/bin/spiralctl
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/spiralctl.sh"
        ((++updated))
    fi

    # pool-mode script
    if [[ -f "$SCRIPTS_SRC/linux/pool-mode.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/pool-mode.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/pool-mode.sh"
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/pool-mode.sh"
        ((++updated))
    fi

    # block-celebrate script (Avalon LED celebration)
    if [[ -f "$SCRIPTS_SRC/linux/block-celebrate.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/block-celebrate.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/block-celebrate.sh"
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/block-celebrate.sh"
        ((++updated))
    fi

    # maintenance-mode script
    if [[ -f "$SCRIPTS_SRC/linux/maintenance-mode.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/maintenance-mode.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/maintenance-mode.sh"
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/maintenance-mode.sh"
        ((++updated))
    fi

    # update-checker script
    if [[ -f "$SCRIPTS_SRC/linux/update-checker.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/update-checker.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/update-checker.sh"
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/update-checker.sh"
        ((++updated))
    fi

    # blockchain export script (spiralctl chain export)
    if [[ -f "$SCRIPTS_SRC/linux/blockchain-export.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/blockchain-export.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/blockchain-export.sh"
        ln -sf "$SCRIPTS_DST/blockchain-export.sh" /usr/local/bin/spiralpool-chain-export
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/blockchain-export.sh"
        ((++updated))
    fi

    # blockchain restore script (spiralctl chain restore)
    if [[ -f "$SCRIPTS_SRC/linux/blockchain-restore.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/blockchain-restore.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/blockchain-restore.sh"
        ln -sf "$SCRIPTS_DST/blockchain-restore.sh" /usr/local/bin/spiralpool-chain-restore
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/blockchain-restore.sh"
        ((++updated))
    fi

    # HA service control script (manages spiralsentinel/spiraldash based on HA role)
    if [[ -f "$SCRIPTS_SRC/linux/ha-service-control.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/ha-service-control.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/ha-service-control.sh"
        ln -sf "$SCRIPTS_DST/ha-service-control.sh" /usr/local/bin/spiralpool-ha-service
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/ha-service-control.sh"
        ((++updated))
    fi

    # HA role watcher script (polls HA API, triggers service control on role change)
    if [[ -f "$SCRIPTS_SRC/linux/ha-role-watcher.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/ha-role-watcher.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/ha-role-watcher.sh"
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/ha-role-watcher.sh"
        ((++updated))
    fi

    # SECURITY: Sudoers-whitelisted scripts MUST be root-owned.
    # These scripts run as root via NOPASSWD sudo — if spiraluser could modify them,
    # it would be a privilege escalation vector.

    # etcd quorum recovery script (PRIVILEGED — in sudoers NOPASSWD)
    if [[ -f "$SCRIPTS_SRC/linux/etcd-quorum-recover.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/etcd-quorum-recover.sh" "$SCRIPTS_DST/"
        chmod 755 "$SCRIPTS_DST/etcd-quorum-recover.sh"
        chown root:root "$SCRIPTS_DST/etcd-quorum-recover.sh"
        ln -sf "$SCRIPTS_DST/etcd-quorum-recover.sh" /usr/local/bin/spiralpool-etcd-recover
        ((++updated))
    fi

    # Patroni force bootstrap script (PRIVILEGED — in sudoers NOPASSWD)
    if [[ -f "$SCRIPTS_SRC/linux/patroni-force-bootstrap.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/patroni-force-bootstrap.sh" "$SCRIPTS_DST/"
        chmod 755 "$SCRIPTS_DST/patroni-force-bootstrap.sh"
        chown root:root "$SCRIPTS_DST/patroni-force-bootstrap.sh"
        ln -sf "$SCRIPTS_DST/patroni-force-bootstrap.sh" /usr/local/bin/spiralpool-patroni-bootstrap
        ((++updated))
    fi

    # etcd cluster rejoin script (PRIVILEGED — in sudoers NOPASSWD)
    if [[ -f "$SCRIPTS_SRC/linux/etcd-cluster-rejoin.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/etcd-cluster-rejoin.sh" "$SCRIPTS_DST/"
        chmod 755 "$SCRIPTS_DST/etcd-cluster-rejoin.sh"
        chown root:root "$SCRIPTS_DST/etcd-cluster-rejoin.sh"
        ((++updated))
    fi

    # HA failback orchestrator (PRIVILEGED — in sudoers NOPASSWD)
    if [[ -f "$SCRIPTS_SRC/linux/ha-failback.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/ha-failback.sh" "$SCRIPTS_DST/"
        chmod 755 "$SCRIPTS_DST/ha-failback.sh"
        chown root:root "$SCRIPTS_DST/ha-failback.sh"
        ((++updated))
    fi

    # HA add peer script (PRIVILEGED — in sudoers NOPASSWD)
    if [[ -f "$SCRIPTS_SRC/linux/ha-add-peer.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/ha-add-peer.sh" "$SCRIPTS_DST/"
        chmod 755 "$SCRIPTS_DST/ha-add-peer.sh"
        chown root:root "$SCRIPTS_DST/ha-add-peer.sh"
        ((++updated))
    fi

    # HA replication script (cold-standby blockchain + PostgreSQL replication)
    if [[ -f "$SCRIPTS_SRC/linux/ha-replicate.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/ha-replicate.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/ha-replicate.sh"
        ln -sf "$SCRIPTS_DST/ha-replicate.sh" /usr/local/bin/spiralpool-ha-replicate
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/ha-replicate.sh"
        ((++updated))
    fi

    # HA validation script (comprehensive HA test suite)
    if [[ -f "$SCRIPTS_SRC/linux/ha-validate.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/ha-validate.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/ha-validate.sh"
        ln -sf "$SCRIPTS_DST/ha-validate.sh" /usr/local/bin/spiralpool-ha-validate
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/ha-validate.sh"
        ((++updated))
    fi

    # HA SSH key setup script (SSH key exchange between nodes for ha-replicate)
    if [[ -f "$SCRIPTS_SRC/linux/ha-setup-ssh.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/ha-setup-ssh.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/ha-setup-ssh.sh"
        ln -sf "$SCRIPTS_DST/ha-setup-ssh.sh" /usr/local/bin/spiralpool-ha-setup-ssh
        chown "${POOL_USER}:${POOL_USER}" "$SCRIPTS_DST/ha-setup-ssh.sh"
        ((++updated))
    fi

    # apt post-update restart script (restarts pool services after library updates)
    if [[ -f "$SCRIPTS_SRC/linux/apt-post-update-restart.sh" ]]; then
        cp "$SCRIPTS_SRC/linux/apt-post-update-restart.sh" "$SCRIPTS_DST/"
        chmod +x "$SCRIPTS_DST/apt-post-update-restart.sh"
        chown root:root "$SCRIPTS_DST/apt-post-update-restart.sh"
        ((++updated))

        # Deploy systemd service and drop-in if not already present
        if [[ ! -f "/etc/systemd/system/spiralpool-apt-restart.service" ]]; then
            cat > /etc/systemd/system/spiralpool-apt-restart.service << 'APTEOF'
[Unit]
Description=Spiral Pool Post-APT-Update Service Restart
Documentation=https://github.com/SpiralPool/Spiral-Pool
After=apt-daily-upgrade.service

[Service]
Type=oneshot
ExecStart=/spiralpool/scripts/apt-post-update-restart.sh
User=root
TimeoutStartSec=120
StandardOutput=journal
StandardError=journal
SyslogIdentifier=spiralpool-apt-restart
APTEOF
        fi

        if [[ ! -f "/etc/systemd/system/apt-daily-upgrade.service.d/spiralpool-restart.conf" ]]; then
            mkdir -p /etc/systemd/system/apt-daily-upgrade.service.d
            cat > /etc/systemd/system/apt-daily-upgrade.service.d/spiralpool-restart.conf << 'APTEOF'
[Service]
ExecStartPost=-/bin/systemctl start --no-block spiralpool-apt-restart.service
APTEOF
        fi

        systemctl daemon-reload 2>/dev/null || true
        log_info "  - Post-update service restart hook deployed"
    fi

    # Update spiralpool-* commands from new install.sh heredocs
    if [[ -f "$PROJECT_ROOT/scripts/linux/update-commands.sh" && -f "$PROJECT_ROOT/install.sh" ]]; then
        log_info "Updating spiralpool-* commands from new install.sh..."
        if bash "$PROJECT_ROOT/scripts/linux/update-commands.sh" "$PROJECT_ROOT/install.sh" 2>/dev/null; then
            log_success "spiralpool-* commands updated"
        else
            log_warn "Some spiralpool-* commands could not be updated (non-fatal)"
        fi
    fi

    if [[ $updated -gt 0 ]]; then
        log_success "Updated $updated utility script(s)"
    else
        log_info "  - No utility scripts found to update"
    fi
}

update_motd() {
    # Regenerate the MOTD to show unified spiralctl commands
    if [[ ! -d /etc/update-motd.d ]]; then
        log_info "No MOTD directory found — skipping"
        return 0
    fi

    log_info "Updating MOTD..."

    sudo tee /etc/update-motd.d/00-spiralpool > /dev/null << 'MOTDEOF'
#!/bin/bash
# Spiral Pool MOTD

# Colors
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
WHITE='\033[1;37m'
DIM='\033[2m'
BOLD='\033[1m'
NC='\033[0m'

# System info
UPTIME=$(uptime -p 2>/dev/null | sed 's/up //' || echo "unknown")
LOAD=$(cat /proc/loadavg 2>/dev/null | awk '{print $1, $2, $3}' || echo "unknown")
MEM_TOTAL=$(free -h 2>/dev/null | awk '/^Mem:/{print $2}' || echo "?")
MEM_USED=$(free -h 2>/dev/null | awk '/^Mem:/{print $3}' || echo "?")
DISK_USED=$(df -h / 2>/dev/null | awk 'NR==2{print $3"/"$2" ("$5")"}' || echo "unknown")
IP_ADDR=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "unknown")

# Pool service status (stratum is the core pool service)
POOL_STATUS=$(systemctl is-active spiralstratum 2>/dev/null) || POOL_STATUS="inactive"
if [ "$POOL_STATUS" = "active" ]; then
    STATUS_COLOR="${GREEN}"
    STATUS_ICON="●"
elif [ "$POOL_STATUS" = "failed" ]; then
    STATUS_COLOR="${RED}"
    STATUS_ICON="●"
else
    STATUS_COLOR="${YELLOW}"
    STATUS_ICON="○"
fi

# Dashboard & Sentinel status
DASH_STATUS=$(systemctl is-active spiraldash 2>/dev/null) || DASH_STATUS="inactive"
SENT_STATUS=$(systemctl is-active spiralsentinel 2>/dev/null) || SENT_STATUS="inactive"
DASH_ICON="○"; [ "$DASH_STATUS" = "active" ] && DASH_ICON="●"
SENT_ICON="○"; [ "$SENT_STATUS" = "active" ] && SENT_ICON="●"
DASH_COLOR="${YELLOW}"; [ "$DASH_STATUS" = "active" ] && DASH_COLOR="${GREEN}"
SENT_COLOR="${YELLOW}"; [ "$SENT_STATUS" = "active" ] && SENT_COLOR="${GREEN}"

echo ""
echo -e "${CYAN}  █████████             ███                      ████     ███████████                    ████${NC}"
echo -e "${CYAN} ███░░░░░███           ░░░                      ░░███    ░░███░░░░░███                  ░░███${NC}"
echo -e "${CYAN}░███    ░░░  ████████  ████  ████████   ██████   ░███     ░███    ░███  ██████   ██████  ░███${NC}"
echo -e "${CYAN}░░█████████ ░░███░░███░░███ ░░███░░███ ░░░░░███  ░███     ░██████████  ███░░███ ███░░███ ░███${NC}"
echo -e "${CYAN} ░░░░░░░░███ ░███ ░███ ░███  ░███ ░░░   ███████  ░███     ░███░░░░░░  ░███ ░███░███ ░███ ░███${NC}"
echo -e "${CYAN} ███    ░███ ░███ ░███ ░███  ░███      ███░░███  ░███     ░███        ░███ ░███░███ ░███ ░███${NC}"
echo -e "${CYAN}░░█████████  ░███████  █████ █████    ░░████████ █████    █████       ░░██████ ░░██████  █████${NC}"
echo -e "${CYAN} ░░░░░░░░░   ░███░░░  ░░░░░ ░░░░░      ░░░░░░░░ ░░░░░    ░░░░░         ░░░░░░   ░░░░░░  ░░░░░${NC}"
echo -e "${CYAN}             ░███${NC}"
echo -e "${CYAN}             █████${NC}"
echo -e "${CYAN}            ░░░░░${NC}"
echo -e "                                 ${MAGENTA}Multi-Algorithm Solo Mining Pool${NC}"
echo -e "                                     ${DIM}V1.2.1 - CONVERGENT SPIRAL${NC}"
echo ""
echo -e "  ${STATUS_COLOR}${STATUS_ICON}${NC} Stratum: ${STATUS_COLOR}${POOL_STATUS}${NC}    ${DASH_COLOR}${DASH_ICON}${NC} Dash: ${DASH_COLOR}${DASH_STATUS}${NC}    ${SENT_COLOR}${SENT_ICON}${NC} Sentinel: ${SENT_COLOR}${SENT_STATUS}${NC}"
echo -e "    Uptime: ${GREEN}${UPTIME}${NC}    Load: ${GREEN}${LOAD}${NC}"
echo -e "    Memory: ${GREEN}${MEM_USED} / ${MEM_TOTAL}${NC}    Disk: ${GREEN}${DISK_USED}${NC}"
echo ""
echo -e "${CYAN}━━━ COMMANDS ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "    ${YELLOW}spiralctl status${NC}         Overview       ${YELLOW}spiralctl watch${NC}            Live monitor"
echo -e "    ${YELLOW}spiralctl stats${NC}          Pool stats     ${YELLOW}spiralctl logs${NC}             Stratum logs"
echo -e "    ${YELLOW}spiralctl sync${NC}           Sync status    ${YELLOW}spiralctl test${NC}             Connectivity"
echo -e "    ${YELLOW}spiralctl scan${NC}           Find miners    ${YELLOW}spiralctl restart${NC}          Restart all"
echo -e "    ${YELLOW}spiralctl mining${NC}         Mining mode    ${YELLOW}spiralctl config${NC}           Configuration"
echo -e "    ${YELLOW}spiralctl wallet${NC}         Addresses      ${YELLOW}spiralctl security${NC}         Security"
echo -e "    ${YELLOW}spiralctl data backup${NC}    Backup         ${YELLOW}spiralctl data restore${NC}     Restore"
echo -e "    ${YELLOW}spiralctl maintenance${NC}    Maintenance    ${YELLOW}spiralctl ha${NC}               HA cluster"
echo -e "    ${YELLOW}spiralctl chain export${NC}  Push chain     ${YELLOW}spiralctl chain restore${NC}    Pull chain"
echo -e "    ${YELLOW}spiralctl add-coin${NC}       Add new coin   ${YELLOW}spiralctl remove-coin${NC}      Remove a coin"
echo -e "    ${YELLOW}spiralctl stats blocks${NC}   Block history  ${CYAN}coin-upgrade.sh${NC}             Upgrade daemons"
echo -e "    ${CYAN}▶  spiralctl help${NC}          Full command reference & man page"
echo ""
echo -e "${CYAN}━━━ SUPPORTED COINS ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "    ${GREEN}SHA-256d${NC}: BTC  BCH  BC2  DGB  QBX   ${GREEN}Scrypt${NC}: LTC  DOGE  DGB-S  PEP  CAT"
echo -e "    ${GREEN}AuxPoW${NC}:  BTC+NMC  BTC+FBTC  BTC+SYS  BTC+XMY  DGB+NMC  LTC+DOGE  LTC+PEP"
echo ""
echo -e "${CYAN}━━━ WEB INTERFACES ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "    Dashboard: ${GREEN}http://${IP_ADDR}:1618${NC}    API: ${GREEN}http://${IP_ADDR}:4000${NC}"
echo ""
MOTDEOF

    sudo chmod +x /etc/update-motd.d/00-spiralpool
    log_success "MOTD updated"
}

migrate_ha_sudoers() {
    # Add missing HA sudoers entries for etcd quorum recovery and Patroni force bootstrap
    local DASH_SUDOERS="/etc/sudoers.d/spiralpool-dashboard"
    [[ -f "$DASH_SUDOERS" ]] || return 0

    local changed=0

    # Add etcd quorum recovery entry if missing
    if ! grep -q "etcd-quorum-recover" "$DASH_SUDOERS" 2>/dev/null; then
        echo "" >> "$DASH_SUDOERS"
        echo "# etcd quorum recovery (automatic failover for HA clusters)" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: ${INSTALL_DIR}/scripts/etcd-quorum-recover.sh" >> "$DASH_SUDOERS"
        changed=1
    fi

    # Add Patroni force bootstrap entry if missing
    if ! grep -q "patroni-force-bootstrap" "$DASH_SUDOERS" 2>/dev/null; then
        echo "" >> "$DASH_SUDOERS"
        echo "# Patroni force bootstrap (nuclear failover recovery — wipes PG data + etcd scope)" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: ${INSTALL_DIR}/scripts/patroni-force-bootstrap.sh" >> "$DASH_SUDOERS"
        changed=1
    fi

    # Add etcd cluster rejoin entry if missing (returning node rejoins after failback)
    if ! grep -q "etcd-cluster-rejoin" "$DASH_SUDOERS" 2>/dev/null; then
        echo "" >> "$DASH_SUDOERS"
        echo "# etcd cluster rejoin (returning node rejoins cluster after failback)" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: ${INSTALL_DIR}/scripts/etcd-cluster-rejoin.sh *" >> "$DASH_SUDOERS"
        changed=1
    fi
    # Fix: ensure etcd-cluster-rejoin.sh allows arguments (--peer-ip for manual use).
    # Older installs have the entry without trailing *, which rejects any arguments.
    if grep -q "etcd-cluster-rejoin\.sh$" "$DASH_SUDOERS" 2>/dev/null; then
        sed -i 's|etcd-cluster-rejoin\.sh$|etcd-cluster-rejoin.sh *|' "$DASH_SUDOERS"
        changed=1
    fi

    # Add HA failback orchestrator entry if missing (automatic return to preferred primary)
    if ! grep -q "ha-failback" "$DASH_SUDOERS" 2>/dev/null; then
        echo "" >> "$DASH_SUDOERS"
        echo "# HA failback orchestrator (automatic return to preferred primary)" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: ${INSTALL_DIR}/scripts/ha-failback.sh" >> "$DASH_SUDOERS"
        changed=1
    fi

    # Add HA add peer entry if missing (called remotely when a new node joins the cluster)
    if ! grep -q "ha-add-peer" "$DASH_SUDOERS" 2>/dev/null; then
        echo "" >> "$DASH_SUDOERS"
        echo "# HA add peer (called remotely when a new node joins the cluster)" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: ${INSTALL_DIR}/scripts/ha-add-peer.sh *" >> "$DASH_SUDOERS"
        changed=1
    fi

    # Add HA watcher service control entries if missing
    if ! grep -q "spiralpool-ha-watcher" "$DASH_SUDOERS" 2>/dev/null; then
        echo "" >> "$DASH_SUDOERS"
        echo "# HA watcher service control" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl start spiralpool-ha-watcher" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl stop spiralpool-ha-watcher" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: /bin/systemctl restart spiralpool-ha-watcher" >> "$DASH_SUDOERS"
        changed=1
    fi

    # Add auto-update sudoers entry if missing — allows Sentinel to run upgrade.sh
    # without a password when auto_update_mode is set to "auto" in config
    if ! grep -q "upgrade\.sh" "$DASH_SUDOERS" 2>/dev/null; then
        echo "" >> "$DASH_SUDOERS"
        echo "# Auto-update: Sentinel runs upgrade.sh --auto as spiraluser via sudo" >> "$DASH_SUDOERS"
        echo "# Trailing * allows --auto and other flags" >> "$DASH_SUDOERS"
        echo "$POOL_USER ALL=(ALL) NOPASSWD: ${INSTALL_DIR}/upgrade.sh *" >> "$DASH_SUDOERS"
        changed=1
    fi

    if [[ $changed -eq 1 ]]; then
        if visudo -c -f "$DASH_SUDOERS" > /dev/null 2>&1; then
            log_info "  - Added HA recovery sudoers entries"
        else
            log_warn "  - Sudoers syntax error after HA migration"
        fi
    fi
}

migrate_disable_ipv6() {
    # Disable IPv6 on existing installs — Spiral Pool is IPv4-only.
    # IPv6 causes routing cache corruption when network interfaces change
    # (keepalived VIP failover, sysctl changes), breaking outbound connectivity.
    local sysctl_file="/etc/sysctl.d/99-spiralpool.conf"
    if [[ -f "$sysctl_file" ]]; then
        if ! grep -q 'disable_ipv6' "$sysctl_file" 2>/dev/null; then
            log_info "  Disabling IPv6 (Spiral Pool is IPv4-only)..."
            cat >> "$sysctl_file" << 'EOF'

# IPv6 DISABLED — Spiral Pool is IPv4-only.
# Prevents routing cache corruption on network interface changes.
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
EOF
            sysctl -p "$sysctl_file" >/dev/null 2>&1 || true
            ip route flush cache 2>/dev/null || true
            log_success "  IPv6 disabled"
        fi
    fi
}

migrate_keepalived_config() {
    # Fix keepalived priority inversion bug in existing HA installations.
    #
    # Bug: Spiral Pool uses lower number = higher priority (primary=100, backup=101+),
    # but keepalived uses HIGHER number = MORE likely to be MASTER. Old configs passed
    # the raw Spiral Pool priority to keepalived, meaning backup (101) would always win
    # VRRP elections over master (100).
    #
    # Fix: Convert priority using keepalived_priority = 200 - spiral_priority
    # Also: Update vrrp_script fall from 2 → 5 to prevent VIP flapping.
    #
    # Detection: If state is BACKUP and priority > 100, the config has the old bug.
    # This is safe because after the fix, backup priorities are always < 100.

    local keepalived_conf="/etc/keepalived/keepalived.conf"

    # Only run if keepalived is configured (HA is active)
    if [[ ! -f "$keepalived_conf" ]]; then
        return 0
    fi

    # Only run if this looks like a Spiral Pool config
    if ! grep -q "SPIRALPOOL" "$keepalived_conf" 2>/dev/null; then
        return 0
    fi

    local changes_made=false

    # Backup config before modifications (enables rollback if restart fails)
    cp "$keepalived_conf" "${keepalived_conf}.pre-upgrade"

    # --- Priority inversion fix ---
    local current_state current_priority
    current_state=$(grep -oP 'state\s+\K\S+' "$keepalived_conf" 2>/dev/null | head -1)
    current_priority=$(grep -oP '^\s*priority\s+\K[0-9]+' "$keepalived_conf" 2>/dev/null | head -1)

    if [[ "$current_state" == "BACKUP" ]] && [[ -n "$current_priority" ]] && [[ "$current_priority" -gt 100 ]]; then
        local new_priority=$((200 - current_priority))
        # Clamp to valid range
        [[ $new_priority -lt 1 ]] && new_priority=1
        [[ $new_priority -gt 254 ]] && new_priority=254

        log_info "  Fixing keepalived priority inversion: $current_priority → $new_priority"
        sed -i "s/^\(\s*priority\s\+\)${current_priority}/\1${new_priority}/" "$keepalived_conf"
        changes_made=true
    fi

    # --- fall 2 → fall 5 fix (prevents VIP flapping on transient stratum crashes) ---
    if grep -qP '^\s*fall\s+2\s*$' "$keepalived_conf" 2>/dev/null; then
        log_info "  Fixing keepalived vrrp_script fall: 2 → 5"
        sed -i 's/^\(\s*fall\s\+\)2\s*$/\15/' "$keepalived_conf"
        changes_made=true
    fi

    # --- nopreempt fix (prevents VIP/DB split on node return) ---
    # Without nopreempt, a rebooted primary reclaims VIP before Patroni can switchover,
    # causing stratum to fail with read-only DB. Also change state MASTER → BACKUP
    # (nopreempt requires state BACKUP on all nodes).
    if ! grep -q 'nopreempt' "$keepalived_conf" 2>/dev/null; then
        log_info "  Adding nopreempt to keepalived (prevents VIP/DB split on failback)"
        # Add nopreempt after advert_int line
        sed -i '/advert_int/a\    nopreempt' "$keepalived_conf"
        changes_made=true
    fi
    if grep -qP '^\s*state\s+MASTER' "$keepalived_conf" 2>/dev/null; then
        log_info "  Changing keepalived state MASTER → BACKUP (required for nopreempt)"
        sed -i 's/^\(\s*state\s\+\)MASTER/\1BACKUP/' "$keepalived_conf"
        changes_made=true
    fi

    # --- Redact token from config comment (security) ---
    if grep -qP '^#\s*Token:\s+spiral-' "$keepalived_conf" 2>/dev/null; then
        sed -i 's/^#\s*Token:\s\+spiral-.*/# Token: [configured]/' "$keepalived_conf"
        changes_made=true
    fi

    # Restart keepalived if changes were made
    if [[ "$changes_made" == true ]]; then
        log_info "  Restarting keepalived to apply config fixes..."
        local kd_err
        if kd_err=$(systemctl restart keepalived 2>&1); then
            # Flush routing cache — keepalived VIP changes can leave stale broadcast entries
            ip route flush cache 2>/dev/null || true
            log_success "  Keepalived config migrated and restarted"
            # Clean up backup on success
            rm -f "${keepalived_conf}.pre-upgrade"
        else
            log_warn "  Failed to restart keepalived: ${kd_err}"
            log_warn "  Rolling back to pre-upgrade config..."
            if [[ -f "${keepalived_conf}.pre-upgrade" ]]; then
                cp "${keepalived_conf}.pre-upgrade" "$keepalived_conf"
                systemctl restart keepalived 2>/dev/null || true
            fi
            log_warn "  Pre-upgrade backup retained at: ${keepalived_conf}.pre-upgrade"
        fi
    fi
}

migrate_runtime_dir() {
    # Create tmpfiles.d config for /run/spiralpool/ (survives reboots).
    # /run is tmpfs — cleared on every reboot. Without this, scripts that run
    # outside RuntimeDirectory-aware services (update-checker, blockchain-export,
    # block-celebrate via cron) fail to create lock files in /run/spiralpool/.
    local tmpfiles_conf="/etc/tmpfiles.d/spiralpool.conf"
    if [[ -f "$tmpfiles_conf" ]]; then
        return 0  # Already exists (created by install.sh or a previous upgrade)
    fi
    cat > "$tmpfiles_conf" << TMPEOF
# Spiral Pool runtime directory — lock files, temp data, miner caches
d /run/spiralpool 0755 $POOL_USER $POOL_USER -
TMPEOF
    systemd-tmpfiles --create "$tmpfiles_conf" 2>/dev/null || true
    log_info "  Created tmpfiles.d config for /run/spiralpool/"
}

migrate_coin_version_cache() {
    # Seed /spiralpool/config/coin-versions/<COIN>.ver for all installed coins.
    # coin-upgrade.sh reads these files to display the installed version when a
    # daemon's --version output does not include a version number (e.g. QBX).
    # Files are only written if the binary exists and the cache is not already set.
    #
    # IMPORTANT: When seeding, we try --version detection first, then fall back to
    # _VC_PREV (what v1.0 shipped) — NOT _VC_TARGET. Writing the target version
    # would make coin-upgrade.sh think the coin is already upgraded.
    local vc_dir="${INSTALL_DIR}/config/coin-versions"
    mkdir -p "$vc_dir"

    # Previous version map — what v1.0 (BlackICE) shipped for each coin.
    # Used as fallback when --version detection fails (e.g. QBX).
    declare -A _VC_PREV=(
        [DGB]="8.26.2"          [DGB-SCRYPT]="8.26.2"
        [BTC]="29.3.knots20260210"
        [BCH]="29.0.0"          [BC2]="29.1.0"
        [LTC]="0.21.4"          [DOGE]="1.14.9"
        [PEP]="1.1.0"           [CAT]="2.1.1"
        [NMC]="28.0"            [SYS]="5.0.5"
        [XMY]="0.18.1.0"        [FBTC]="0.3.0"
        [QBX]="0.1.0"
    )
    # Daemon binary map — must match COIN_DAEMON_CMD in coin-upgrade.sh
    declare -A _VC_BIN=(
        [DGB]="digibyted"       [DGB-SCRYPT]="digibyted"
        [BTC]="bitcoind"        [BCH]="bitcoind-bch"
        [BC2]="bitcoiniid"      [LTC]="litecoind"
        [DOGE]="dogecoind"      [PEP]="pepecoind"
        [CAT]="catcoind"        [NMC]="namecoind"
        [SYS]="syscoind"        [XMY]="myriadcoind"
        [FBTC]="fractald"       [QBX]="qbitx"
    )

    local coin ver_file bin_path detected_ver
    for coin in "${!_VC_PREV[@]}"; do
        ver_file="${vc_dir}/${coin}.ver"
        [[ -f "$ver_file" ]] && continue   # already set — don't overwrite
        bin_path=$(readlink -f "/usr/local/bin/${_VC_BIN[$coin]}" 2>/dev/null || echo "")
        [[ -x "$bin_path" ]] || continue   # not installed on this node

        # Try --version detection first (works for most daemons)
        detected_ver=$("$bin_path" --version 2>/dev/null | head -1 \
            | grep -oP '(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*' | head -1 || echo "")
        if [[ -n "$detected_ver" ]]; then
            echo "$detected_ver" > "$ver_file"
        else
            # Fallback: use known previous version (e.g. QBX --version has no number)
            echo "${_VC_PREV[$coin]}" > "$ver_file"
        fi
    done

    chown -R "${POOL_USER}:${POOL_USER}" "$vc_dir" 2>/dev/null || true
    log_info "  Coin version cache seeded (${vc_dir})"
}

migrate_ha_mode_file() {
    # Create ha-mode file for existing HA installations (enables automatic failback).
    # The ha-role-watcher reads this file to determine if this node should attempt
    # failback when it's running as BACKUP but is the preferred primary (ha-master).
    # Without this file, failback is disabled (graceful degradation, no crash).
    local ha_mode_file="${INSTALL_DIR}/config/ha-mode"

    # Skip if already exists (created by install.sh or a previous upgrade)
    [[ -f "$ha_mode_file" ]] && return 0

    # Only create for HA installations (keepalived must be configured)
    local keepalived_conf="/etc/keepalived/keepalived.conf"
    [[ -f "$keepalived_conf" ]] || return 0
    grep -q "SPIRALPOOL" "$keepalived_conf" 2>/dev/null || return 0

    # Determine ha-mode from keepalived priority (after migrate_keepalived_config fix):
    #   priority 100 = ha-master (preferred primary)
    #   priority < 100 = ha-backup
    local kd_priority
    kd_priority=$(grep -oP '^\s*priority\s+\K[0-9]+' "$keepalived_conf" 2>/dev/null | head -1)

    local ha_mode="ha-backup"
    if [[ -n "$kd_priority" ]] && [[ "$kd_priority" -ge 100 ]]; then
        ha_mode="ha-master"
    fi

    echo "$ha_mode" > "$ha_mode_file"
    chown "${POOL_USER}:${POOL_USER}" "$ha_mode_file"
    log_info "  Created ha-mode file: ${ha_mode} (enables automatic failback)"
}

update_version_file() {
    # Guard against symlink attack (script runs as root, VERSION dir is user-writable)
    if [[ -L "${INSTALL_DIR}/VERSION" ]]; then
        log_warn "VERSION file is a symlink — removing and recreating"
        rm -f "${INSTALL_DIR}/VERSION"
    fi
    echo "${TARGET_VERSION}" > "${INSTALL_DIR}/VERSION"
    chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/VERSION"
    log_info "Version updated to ${TARGET_VERSION}"
}

deploy_pg_maintenance_timer() {
    # Idempotent: sets up (or re-creates) the PostgreSQL weekly VACUUM ANALYZE timer.
    # Safe to run on every upgrade — systemctl enable is a no-op if already enabled.
    log_info "  - Deploying PostgreSQL maintenance timer..."

    sudo tee /usr/local/bin/spiralpool-pg-maintenance > /dev/null << 'PGMAINTEOF'
#!/bin/bash
LOG_FILE="/spiralpool/logs/pg-maintenance.log"
DB_NAME="spiralstratum"
log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"; }
mkdir -p "$(dirname "$LOG_FILE")"
log "Starting PostgreSQL maintenance (VACUUM ANALYZE)..."
db_service="postgresql"
systemctl is-enabled --quiet "patroni" 2>/dev/null && db_service="patroni"
if ! systemctl is-active --quiet "$db_service" 2>/dev/null; then
    log "ERROR: $db_service is not running — skipping maintenance"; exit 1
fi
if systemctl is-enabled --quiet "patroni" 2>/dev/null; then
    ROLE=$(sudo -u postgres psql -tAc "SELECT pg_is_in_recovery();" 2>/dev/null | tr -d '[:space:]')
    [[ "$ROLE" == "t" ]] && { log "Replica node — skipping VACUUM (master only)"; exit 0; }
fi
START_TS=$(date +%s)
sudo -u postgres vacuumdb --analyze --dbname="$DB_NAME" 2>>"$LOG_FILE" && log "VACUUM ANALYZE complete on $DB_NAME" || log "WARNING: vacuumdb returned non-zero"
sudo -u postgres reindexdb --dbname="$DB_NAME" 2>/dev/null || true
log "Maintenance complete in $(( $(date +%s) - START_TS ))s"
PGMAINTEOF
    sudo chmod +x /usr/local/bin/spiralpool-pg-maintenance
    sudo chown root:root /usr/local/bin/spiralpool-pg-maintenance

    sudo tee /etc/systemd/system/spiralpool-pg-maintenance.service > /dev/null << 'SVCEOF'
[Unit]
Description=Spiral Pool PostgreSQL Maintenance (VACUUM ANALYZE)
After=network.target
[Service]
Type=oneshot
ExecStart=/usr/local/bin/spiralpool-pg-maintenance
User=root
SVCEOF

    sudo tee /etc/systemd/system/spiralpool-pg-maintenance.timer > /dev/null << 'TIMEREOF'
[Unit]
Description=Spiral Pool PostgreSQL Weekly Maintenance Timer
[Timer]
OnCalendar=Sun *-*-* 03:00:00
Persistent=true
RandomizedDelaySec=300
[Install]
WantedBy=timers.target
TIMEREOF

    sudo systemctl daemon-reload 2>/dev/null || true
    sudo systemctl enable --now spiralpool-pg-maintenance.timer 2>/dev/null || true
    log_success "PostgreSQL maintenance timer deployed (weekly Sun 03:00)"
}

update_upgrade_script() {
    # Copy this script itself for future upgrades
    # Use atomic write (cp to temp + mv) to avoid corrupting the running script
    if [[ -f "${PROJECT_ROOT}/upgrade.sh" ]]; then
        cp "${PROJECT_ROOT}/upgrade.sh" "${INSTALL_DIR}/upgrade.sh.new"
        chmod +x "${INSTALL_DIR}/upgrade.sh.new"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/upgrade.sh.new"
        mv -f "${INSTALL_DIR}/upgrade.sh.new" "${INSTALL_DIR}/upgrade.sh"
        log_info "  - upgrade.sh updated"
    fi

    # Deploy coin-upgrade.sh alongside upgrade.sh (repo root → /spiralpool/scripts/)
    if [[ -f "${PROJECT_ROOT}/coin-upgrade.sh" ]]; then
        cp "${PROJECT_ROOT}/coin-upgrade.sh" "${INSTALL_DIR}/scripts/coin-upgrade.sh.new"
        chmod +x "${INSTALL_DIR}/scripts/coin-upgrade.sh.new"
        chown "${POOL_USER}:${POOL_USER}" "${INSTALL_DIR}/scripts/coin-upgrade.sh.new"
        mv -f "${INSTALL_DIR}/scripts/coin-upgrade.sh.new" "${INSTALL_DIR}/scripts/coin-upgrade.sh"
        log_info "  - coin-upgrade.sh updated"
    fi
}

# =============================================================================
# Verification
# =============================================================================

verify_upgrade() {
    log_info "Verifying upgrade..."
    local errors=0
    local warnings=0

    # Check binary exists and runs
    if [[ -x "${INSTALL_DIR}/bin/spiralstratum" ]]; then
        local binary_version=$("${INSTALL_DIR}/bin/spiralstratum" --version 2>/dev/null || echo "unknown")
        log_info "  - spiralstratum binary: OK (${binary_version})"
    else
        log_error "  - spiralstratum binary: MISSING"
        ((++errors))
    fi

    # Check services — only verify components that were actually updated
    sleep 3
    local verify_services=()
    $UPDATE_STRATUM && verify_services+=("$STRATUM_SERVICE")
    $UPDATE_DASHBOARD && verify_services+=("$DASHBOARD_SERVICE")
    $UPDATE_SENTINEL && [[ -n "$SENTINEL_SERVICE" ]] && verify_services+=("$SENTINEL_SERVICE")
    $UPDATE_STRATUM && [[ -n "$HEALTH_SERVICE" ]] && verify_services+=("$HEALTH_SERVICE")

    # Give services time to initialize before checking status
    log_info "Waiting for services to start..."
    sleep 10

    for service in "${verify_services[@]}"; do
        local status; status=$(systemctl is-active "$service" 2>/dev/null) || true
        [[ -z "$status" ]] && status="inactive"
        case "$status" in
            active)      log_info "  - ${service}: ${GREEN}RUNNING${NC}" ;;
            activating)  log_info "  - ${service}: ${CYAN}STARTING${NC}" ;;
            reloading)   log_info "  - ${service}: ${CYAN}RELOADING${NC} (will be running shortly)" ;;
            deactivating) log_info "  - ${service}: ${YELLOW}STOPPING${NC} (restart in progress)" ;;
            failed)
                if [[ "$service" == "$STRATUM_SERVICE" ]]; then
                    log_warn "  - ${service}: ${YELLOW}STARTING${NC} - stratum takes ~30s to initialize"
                    log_info "    Wait 30 seconds, then check: systemctl status ${service}"
                else
                    log_warn "  - ${service}: ${RED}FAILED${NC} - check: systemctl status ${service}"
                    ((++warnings))
                fi
                ;;
            inactive)
                if [[ "$service" == "$STRATUM_SERVICE" ]]; then
                    log_warn "  - ${service}: ${YELLOW}STARTING${NC} - stratum takes ~30s to initialize"
                    log_info "    Wait 30 seconds, then check: systemctl status ${service}"
                else
                    log_warn "  - ${service}: ${YELLOW}INACTIVE${NC} - may be restarting, check: systemctl status ${service}"
                    ((++warnings))
                fi
                ;;
            *)           log_warn "  - ${service}: $status"; ((++warnings)) ;;
        esac
    done

    if [[ $errors -gt 0 ]]; then
        log_error "Upgrade verification found ${errors} error(s)"
        return 1
    elif [[ $warnings -gt 0 ]]; then
        log_warn "Upgrade completed with ${warnings} warning(s)"
    else
        log_success "Upgrade verified successfully"
    fi
}

show_summary() {
    echo
    echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${GREEN}  UPGRADE COMPLETE${NC}"
    echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo
    # Get the server's primary IP address (first non-loopback IPv4)
    local server_ip=$(ip -4 addr show scope global 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | head -1)
    [[ -z "$server_ip" ]] && server_ip="localhost"

    printf "  %-20s %s\n" "Version:" "${CURRENT_VERSION} → ${TARGET_VERSION}"
    printf "  %-20s %s\n" "Dashboard:" "http://${server_ip}:${DASHBOARD_PORT}"
    echo
    echo -e "${CYAN}Service Status:${NC}"

    # Brief wait so services have time to transition from deactivating/inactive
    sleep 5

    local all_active=true
    for service in "$STRATUM_SERVICE" "$DASHBOARD_SERVICE" "$SENTINEL_SERVICE"; do
        local status; status=$(systemctl is-active "$service" 2>/dev/null) || true
        [[ -z "$status" ]] && status="inactive"
        case "$status" in
            active)     printf "  %-24s ${GREEN}%s${NC}\n" "$service" "Running" ;;
            activating) printf "  %-24s ${CYAN}%s${NC}\n" "$service" "Starting"; all_active=false ;;
            *)          printf "  %-24s ${YELLOW}%s${NC}\n" "$service" "$status"; all_active=false ;;
        esac
    done

    if [[ "$all_active" != "true" ]]; then
        echo
        echo -e "  ${YELLOW}Note:${NC} Services may take up to 30 seconds to fully start."
        echo -e "  If any show inactive/deactivating, wait a moment and check:"
        echo -e "    systemctl is-active $STRATUM_SERVICE $DASHBOARD_SERVICE $SENTINEL_SERVICE"
    fi

    echo
    echo -e "${CYAN}To monitor startup:${NC}"
    echo "  journalctl -fu $STRATUM_SERVICE"
    echo
    echo -e "${CYAN}Configuration files preserved:${NC}"
    echo "  - $INSTALL_DIR/config/config.yaml"
    echo "  - Sentinel settings ($INSTALL_DIR/config/sentinel/)"

    # Check for available coin daemon upgrades and show a console hint
    local coin_upgrade_script="${INSTALL_DIR}/scripts/coin-upgrade.sh"
    if [[ -x "$coin_upgrade_script" ]]; then
        local upgrade_lines
        upgrade_lines=$(bash "$coin_upgrade_script" --list 2>/dev/null)
        if [[ -n "$upgrade_lines" ]]; then
            echo
            echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
            echo -e "${YELLOW}  COIN DAEMON UPGRADES AVAILABLE${NC}"
            echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
            echo
            while IFS=' ' read -r coin installed target risk; do
                local risk_color
                case "$risk" in
                    PATCH) risk_color="${GREEN}" ;;
                    MINOR) risk_color="${YELLOW}" ;;
                    MAJOR) risk_color="${RED}" ;;
                    *)     risk_color="${CYAN}" ;;
                esac
                printf "  %-6s  %s → %s  %b%s%b\n" \
                    "$coin" "$installed" "$target" "$risk_color" "$risk" "${NC}"
            done <<< "$upgrade_lines"
            echo
            echo -e "${CYAN}Coin daemon upgrades are NOT applied automatically — they may${NC}"
            echo -e "${CYAN}require a chain resync. Run manually when ready:${NC}"
            echo
            echo "  sudo ${coin_upgrade_script}"
            echo
        fi
    fi
    echo
}

# Send a Discord notification about available coin daemon upgrades.
# Called after upgrade completes. Reads webhook from Sentinel config, sends
# directly via curl (bypasses quiet hours by design).  Waits up to 45s for
# spiralsentinel.service to be active — if it never comes up, skip silently.
notify_coin_upgrades() {
    local coin_upgrade_script="${INSTALL_DIR}/scripts/coin-upgrade.sh"
    [[ -x "$coin_upgrade_script" ]] || return 0

    # Get machine-readable upgrade list (empty = nothing to do)
    local upgrade_lines
    upgrade_lines=$(bash "$coin_upgrade_script" --list 2>/dev/null)
    [[ -z "$upgrade_lines" ]] && return 0

    # Read Discord webhook URL from Sentinel config
    local webhook
    webhook=$(python3 - <<'PYEOF' 2>/dev/null
import yaml, os, sys
for path in [
    "/spiralpool/config/sentinel/config.yaml",
    os.path.expanduser("~spiraluser/.spiralsentinel/config.yaml"),
]:
    try:
        data = yaml.safe_load(open(path))
        url = (data or {}).get("discord_webhook_url", "")
        if url and "YOUR" not in url and url.startswith("https://discord.com/api/webhooks/"):
            print(url)
            sys.exit(0)
    except Exception:
        pass
PYEOF
)
    [[ -z "$webhook" ]] && return 0

    # Wait for spiralsentinel to be active (up to 45s)
    local waited=0
    while [[ $waited -lt 45 ]]; do
        if systemctl is-active --quiet "$SENTINEL_SERVICE" 2>/dev/null; then
            break
        fi
        sleep 3
        waited=$((waited + 3))
    done
    if ! systemctl is-active --quiet "$SENTINEL_SERVICE" 2>/dev/null; then
        log_warn "Sentinel not active after ${waited}s — skipping coin upgrade Discord notification"
        return 0
    fi

    # Build embed fields: one field per upgradeable coin
    local fields="[]"
    local server_ip
    server_ip=$(ip -4 addr show scope global 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | head -1)
    [[ -z "$server_ip" ]] && server_ip="this server"

    local coin_lines=""
    while IFS=' ' read -r coin installed target risk; do
        local risk_label
        case "$risk" in
            PATCH) risk_label="🟢 PATCH" ;;
            MINOR) risk_label="🟡 MINOR" ;;
            MAJOR) risk_label="🔴 MAJOR" ;;
            *)     risk_label="⬆ UPDATE" ;;
        esac
        coin_lines+="**${coin}** — ${installed} → ${target} (${risk_label})\n"
    done <<< "$upgrade_lines"

    local embed
    embed=$(python3 - "$coin_lines" <<'PYEOF' 2>/dev/null
import json, sys
lines = sys.argv[1]
embed = {
    "title": "🔧 Spiral Pool — Upgrade Complete",
    "description": (
        "**Spiral Pool stack upgraded successfully.**\n\n"
        "Coin daemon upgrades are available and must be applied manually — "
        "they are not part of the stack upgrade because they may require a chain resync.\n\n"
        "**Available coin daemon upgrades:**\n"
        + lines.replace("\\n", "\n") +
        "\n\nTo upgrade, run on the server:\n"
        "```\nsudo /spiralpool/scripts/coin-upgrade.sh\n```"
    ),
    "color": 0xFF6B35,
    "footer": {"text": "Spiral Pool v1.2.1 — Convergent Spiral  •  coin-upgrade.sh handles the chain resync risk"}
}
print(json.dumps(embed))
PYEOF
)
    [[ -z "$embed" ]] && return 0

    # Send directly to Discord (quiet-hours bypass — this is an operator action prompt)
    curl -s -o /dev/null -w "%{http_code}" \
        -H "Content-Type: application/json" \
        -d "{\"embeds\":[${embed}]}" \
        "$webhook" \
        --connect-timeout 10 --max-time 20 2>/dev/null | grep -qE "^20" \
        && log_success "Discord: coin upgrade notification sent" \
        || log_warn "Discord: coin upgrade notification failed (webhook unreachable?)"
}

# =============================================================================
# Main
# =============================================================================

main() {
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --check)           CHECK_ONLY=true; shift ;;
            --local)           USE_LOCAL=true; FETCH_LATEST=false; shift ;;
            --fetch-latest)    FETCH_LATEST=true; USE_LOCAL=false; shift ;;  # Legacy alias (no-op now)
            --force)           FORCE_UPGRADE=true; shift ;;
            --no-backup)       SKIP_BACKUP=true; shift ;;
            --auto)            AUTO_MODE=true; FORCE_UPGRADE=true; shift ;;
            --stratum-only)    UPDATE_DASHBOARD=false; UPDATE_SENTINEL=false; shift ;;
            --dashboard-only)  UPDATE_STRATUM=false; UPDATE_SENTINEL=false; shift ;;
            --sentinel-only)   UPDATE_STRATUM=false; UPDATE_DASHBOARD=false; shift ;;
            --no-stratum)      UPDATE_STRATUM=false; shift ;;
            --no-dashboard)    UPDATE_DASHBOARD=false; shift ;;
            --no-sentinel)     UPDATE_SENTINEL=false; shift ;;
            --update-services) UPDATE_SERVICES=true; shift ;;
            --skip-services)  UPDATE_SERVICES=false; shift ;;
            --fix-config)      FIX_CONFIG=true; shift ;;
            --skip-start)      SKIP_START=true; shift ;;
            --full)            UPDATE_SERVICES=true; FIX_CONFIG=true; shift ;;
            --rollback)
                DO_ROLLBACK=true
                shift
                ROLLBACK_TARGET="${1:-}"
                # Only shift again if the next arg is a backup name (not another flag or empty)
                if [[ -n "$ROLLBACK_TARGET" ]] && [[ "$ROLLBACK_TARGET" != --* ]]; then
                    shift
                else
                    ROLLBACK_TARGET=""
                fi
                ;;
            --help|-h)
                echo "Usage: sudo ./upgrade.sh [OPTIONS]"
                echo ""
                echo "Modes:"
                echo "  (default)         Download and install from GitHub"
                echo "  --local           Update from local source (development)"
                echo "  --check           Check for updates (returns JSON)"
                echo ""
                echo "Component Selection:"
                echo "  --stratum-only    Only update stratum binary"
                echo "  --dashboard-only  Only update dashboard"
                echo "  --sentinel-only   Only update Spiral Sentinel"
                echo "  --no-stratum      Skip stratum update"
                echo "  --no-dashboard    Skip dashboard update"
                echo "  --no-sentinel     Skip sentinel update"
                echo ""
                echo "Options:"
                echo "  --force             Force upgrade even if on latest version"
                echo "  --no-backup         Skip backup before upgrading"
                echo "  --update-services   Also update systemd service files"
                echo "  --fix-config        Fix common config issues (coin names, durations)"
                echo "  --skip-start        Don't start services after update"
                echo "  --full              All extras: service files + config fixes"
                echo "  --auto              Unattended automatic upgrade"
                echo "  --rollback [name]   Rollback to a previous backup"
                echo "  -h, --help          Show this help"
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                echo "Use --help for usage information"
                exit 1
                ;;
        esac
    done

    # Handle check-only mode (for Sentinel integration)
    if [[ "$CHECK_ONLY" == "true" ]]; then
        check_for_updates
        exit 0
    fi

    # Safety: warn loudly when --no-backup + --auto are combined
    if [[ "$SKIP_BACKUP" == "true" && "$AUTO_MODE" == "true" ]]; then
        log_warn "╔══════════════════════════════════════════════════════════════╗"
        log_warn "║  WARNING: --no-backup --auto skips ALL safety prompts.      ║"
        log_warn "║  If the upgrade fails, there is NO backup to roll back to.  ║"
        log_warn "╚══════════════════════════════════════════════════════════════╝"
    fi

    print_banner
    check_root

    # Acquire lock — prevents concurrent install.sh + upgrade.sh runs.
    # Auto-clears stale locks; only blocks if a live process genuinely holds it.
    if ! acquire_operation_lock "upgrade"; then
        exit 1
    fi

    trap '_trap_exit_code=$?; release_operation_lock; (exit $_trap_exit_code); cleanup_on_exit' EXIT INT TERM

    # Detect install directory from existing service
    if [[ -f "/etc/systemd/system/spiralstratum.service" ]]; then
        local detected_dir=$(grep -oP 'WorkingDirectory=\K[^\s]+' "/etc/systemd/system/spiralstratum.service" 2>/dev/null || echo "")
        [[ -n "$detected_dir" ]] && [[ -d "$detected_dir" ]] && INSTALL_DIR="$detected_dir"
    fi

    # Update BACKUP_DIR when INSTALL_DIR is reassigned (line 209 set it from the default)
    BACKUP_DIR="${INSTALL_DIR}/backups"

    # Initialize — MUST run before --rollback to populate POOL_USER and service names
    detect_services
    detect_pool_user

    # Check if pool user has a password set (needed for HA SSH, interactive sessions, sudo)
    # passwd --status returns: username L/NP/P ... (L=locked, NP=no password, P=password set)
    # Uses heredoc (<<<) to pass to chpasswd — avoids leaking password in process list
    local pass_status
    pass_status=$(passwd --status "$POOL_USER" 2>/dev/null | awk '{print $2}')
    if [[ "$pass_status" == "L" || "$pass_status" == "NP" ]] && [[ "$AUTO_MODE" != "true" ]] && [[ -t 0 ]]; then
        echo ""
        echo -e "${YELLOW}⚠ No password set for ${POOL_USER} user${NC}"
        echo -e "${DIM}This is needed for HA cluster SSH, service restarting, interactive logins, and other admin tasks.${NC}"
        echo ""
        read -p "  Set a password now? [Y/n]: " set_pass
        if [[ "${set_pass,,}" != "n" ]]; then
            local pass_set=false
            for attempt in 1 2 3; do
                local pool_pass pool_pass_confirm
                read -sp "  Enter password for ${POOL_USER}: " pool_pass
                echo ""
                read -sp "  Confirm password: " pool_pass_confirm
                echo ""
                if [[ -z "$pool_pass" ]]; then
                    unset pool_pass pool_pass_confirm
                    log_warn "Password cannot be empty."
                elif [[ "$pool_pass" != "$pool_pass_confirm" ]]; then
                    unset pool_pass pool_pass_confirm
                    log_warn "Passwords do not match."
                else
                    chpasswd <<< "${POOL_USER}:${pool_pass}"
                    unset pool_pass pool_pass_confirm
                    log_info "Password set for ${POOL_USER}"
                    pass_set=true
                    break
                fi
                [[ $attempt -lt 3 ]] && echo "  Please try again..."
            done
            if [[ "$pass_set" != "true" ]]; then
                log_warn "Password not set. You can set it later with: sudo passwd ${POOL_USER}"
            fi
        fi
        echo ""
    fi

    # Ensure sshd allows password auth for pool user (for HA, interactive admin, SCP)
    # Uses a Match User block so global SSH settings (e.g. key-only) are not weakened
    local sshd_dropin="/etc/ssh/sshd_config.d/spiralpool.conf"
    local sshd_main="/etc/ssh/sshd_config"
    local sshd_marker="# Spiral Pool — allow password auth for pool user"
    if ! grep -q "Match User ${POOL_USER}" "$sshd_main" 2>/dev/null && \
       ! grep -q "Match User ${POOL_USER}" "$sshd_dropin" 2>/dev/null; then
        if [[ -d "/etc/ssh/sshd_config.d" ]] && grep -q 'Include.*/etc/ssh/sshd_config.d/' "$sshd_main" 2>/dev/null; then
            tee "$sshd_dropin" > /dev/null << SSHDEOF
${sshd_marker}
Match User ${POOL_USER}
    PasswordAuthentication yes
SSHDEOF
            log_info "SSH password auth enabled for ${POOL_USER} (drop-in: $sshd_dropin)"
        else
            tee -a "$sshd_main" > /dev/null << SSHDEOF

${sshd_marker}
Match User ${POOL_USER}
    PasswordAuthentication yes
SSHDEOF
            log_info "SSH password auth enabled for ${POOL_USER} (appended to $sshd_main)"
        fi
        if sshd -t 2>/dev/null; then
            systemctl reload sshd 2>/dev/null || systemctl reload ssh 2>/dev/null || true
        else
            log_warn "sshd config test failed — check $sshd_main manually"
        fi
    fi

    # Detect dashboard port early so show_summary() always has the correct value
    if [[ -f "/etc/systemd/system/spiraldash.service" ]]; then
        local detected_port
        detected_port=$(grep -oP '0\.0\.0\.0:\K[0-9]+' /etc/systemd/system/spiraldash.service 2>/dev/null | head -1)
        [[ -n "$detected_port" ]] && DASHBOARD_PORT="$detected_port"
    fi

    # Handle deferred --rollback (needs detect_services/detect_pool_user to have run first)
    if [[ "${DO_ROLLBACK:-false}" == "true" ]]; then
        rollback_to_backup "$ROLLBACK_TARGET"
        exit $?
    fi

    # Get source (GitHub by default, or local with --local flag)
    if [[ "$USE_LOCAL" == "true" ]]; then
        detect_source_directory
        detect_current_version
        get_target_version
    else
        detect_current_version
        get_target_version
        if ! download_new_version; then
            log_warn "GitHub download failed, attempting fallback to local source..."
            detect_source_directory
            if [[ -z "$PROJECT_ROOT" ]]; then
                log_error "No local source available for fallback"
                exit 1
            fi
            log_info "Using local source as fallback: $PROJECT_ROOT"
            USE_LOCAL=true
            FETCH_LATEST=false  # Switch to local mode so get_target_version reads VERSION file
            # Re-derive target version from local VERSION file to match the source being built
            get_target_version
        fi
    fi

    echo
    log_info "Starting Spiral Pool upgrade process..."
    echo
    echo -e "${CYAN}Components to update:${NC}"
    $UPDATE_STRATUM && echo -e "  ${GREEN}✓${NC} Stratum binary" || echo -e "  ${YELLOW}○${NC} Stratum binary (skipped)"
    $UPDATE_DASHBOARD && echo -e "  ${GREEN}✓${NC} Dashboard" || echo -e "  ${YELLOW}○${NC} Dashboard (skipped)"
    $UPDATE_SENTINEL && echo -e "  ${GREEN}✓${NC} Spiral Sentinel" || echo -e "  ${YELLOW}○${NC} Spiral Sentinel (skipped)"
    $UPDATE_SERVICES && echo -e "  ${GREEN}✓${NC} Systemd service files" || echo -e "  ${YELLOW}○${NC} Systemd service files"
    $FIX_CONFIG && echo -e "  ${GREEN}✓${NC} Config fixes" || echo -e "  ${YELLOW}○${NC} Config fixes"
    echo

    # Confirmation (unless auto mode)
    local current_clean=$(echo "$CURRENT_VERSION" | sed 's/^v//')
    local target_clean=$(echo "$TARGET_VERSION" | sed 's/^v//')

    # Prevent silent downgrades in auto mode (check AUTO_MODE, not FORCE_UPGRADE,
    # because --auto implicitly sets FORCE_UPGRADE=true)
    if [[ "$AUTO_MODE" == "true" ]]; then
        local newest
        newest=$(printf '%s\n' "$current_clean" "$target_clean" | sort -V | tail -1)
        if [[ "$newest" == "$current_clean" ]] && [[ "$current_clean" != "$target_clean" ]]; then
            log_warn "Target version $target_clean is older than current $current_clean — skipping downgrade"
            exit 0
        fi
    fi

    if [[ "$AUTO_MODE" == "false" ]] && [[ "$FORCE_UPGRADE" == "false" ]]; then
        local confirm=""
        if [[ "$current_clean" == "$target_clean" ]]; then
            printf "\n  Already on version %s. Force reinstall? [y/N]: " "${CURRENT_VERSION}"
            read confirm
            [[ "$confirm" != "y" && "$confirm" != "Y" ]] && { log_info "Upgrade cancelled."; exit 0; }
        else
            local newest
            newest=$(printf '%s\n' "$current_clean" "$target_clean" | sort -V | tail -1)
            if [[ "$newest" == "$current_clean" ]]; then
                echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                echo -e "${YELLOW}  DOWNGRADE: ${CURRENT_VERSION} → ${TARGET_VERSION} (NOT recommended)${NC}"
                echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                echo ""
                printf "  Type 'yes' to confirm downgrade, or press ENTER to cancel: "
                read confirm
                [[ "$confirm" != "yes" ]] && { log_info "Downgrade cancelled."; exit 0; }
            else
                echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                echo -e "${WHITE}  Ready to upgrade: ${CURRENT_VERSION} → ${TARGET_VERSION}${NC}"
                echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
                echo ""
                printf "  Press ENTER to continue, or type 'n' to cancel: "
                read confirm
                [[ "$confirm" == "n" || "$confirm" == "N" ]] && { log_info "Upgrade cancelled."; exit 0; }
            fi
        fi
    fi

    echo

    # Pre-flight: check disk space (need ~500MB for backup + build artifacts)
    local avail_mb
    avail_mb=$(df -m "${INSTALL_DIR}" 2>/dev/null | awk 'NR==2 {print $4}')
    if [[ -n "$avail_mb" ]] && [[ "$avail_mb" -lt 500 ]]; then
        log_error "Insufficient disk space: ${avail_mb}MB available, need at least 500MB"
        log_error "Free disk space on $(df -h "${INSTALL_DIR}" | awk 'NR==2 {print $6}') before upgrading"
        exit 1
    fi

    # Execute upgrade
    create_backup

    # BUILD phase — compile while services are still running (miners stay connected)
    # Build failure exits here → services never stopped → zero downtime on build errors
    if $UPDATE_STRATUM; then
        build_stratum
    fi

    # STOP phase — minimize the downtime window to deploy + start only
    # Config fixes and binary deploys are fast file operations (seconds, not minutes)
    stop_services

    # Ensure HA VIP dependencies are installed (arping for split-brain detection,
    # ip for VIP management). These are installed by install.sh but may be missing
    # on deployments that predate the VIP manager capabilities fix.
    if ! command -v arping &>/dev/null; then
        log_info "Installing iputils-arping (required for HA split-brain detection)..."
        apt-get install -y -qq iputils-arping 2>/dev/null || log_warn "Could not install iputils-arping — arping unavailable"
    fi
    if ! command -v ip &>/dev/null; then
        log_info "Installing iproute2 (required for VIP management)..."
        apt-get install -y -qq iproute2 2>/dev/null || log_warn "Could not install iproute2 — ip command unavailable"
    fi
    if ! command -v jq &>/dev/null; then
        log_info "Installing jq (required for HA JSON parsing)..."
        apt-get install -y -qq jq 2>/dev/null || log_warn "Could not install jq — HA scripts may fail"
    fi

    # Mark upgrade in progress AFTER stop_services (needed even with --no-backup
    # so cleanup_on_exit restarts services if the upgrade fails)
    UPGRADE_IN_PROGRESS="true"

    # Suppress individual update function restarts — services will be
    # batch-started via start_services() after ALL updates complete
    local ORIG_SKIP_START="$SKIP_START"
    SKIP_START="true"

    # Run config fixes ONLY when explicitly requested with --fix-config or --full.
    # Upgrades should update binaries/code only — config.yaml is user-owned and
    # must not be modified without explicit opt-in.
    if $FIX_CONFIG; then
        fix_config_issues
    fi

    # Fix database ownership when stratum is being updated (ensures migrations can run)
    # This is needed when tables were created by postgres but app runs as spiralstratum
    if $UPDATE_STRATUM; then
        fix_database_ownership "spiralstratum" "spiralstratum"
    fi

    # DEPLOY phase — fast file operations only (services already stopped)
    $UPDATE_STRATUM && deploy_stratum
    $UPDATE_DASHBOARD && update_dashboard
    $UPDATE_SENTINEL && update_sentinel
    $UPDATE_SERVICES && update_systemd_services

    # Update version, scripts, and utilities
    update_utility_scripts
    migrate_ha_sudoers
    update_motd
    update_version_file
    update_upgrade_script
    deploy_pg_maintenance_timer || log_warn "PostgreSQL maintenance timer deploy failed (non-critical)"

    # Strip CRLF from all deployed shell scripts and Python files.
    # Required when source files are SCP'd from Windows (zip extract has \r\n).
    log_info "Stripping CRLF from deployed scripts..."
    find "$INSTALL_DIR/scripts" "$INSTALL_DIR/bin" /usr/local/bin \
        -maxdepth 2 \( -name "*.sh" -o -name "*.py" \) \
        -exec sed -i 's/\r//' {} \; 2>/dev/null || true
    log_success "CRLF strip complete"

    # Cleanup temp directory (null after to prevent double-cleanup in cleanup_on_exit)
    [[ -n "$TEMP_DIR" ]] && [[ -d "$TEMP_DIR" ]] && rm -rf "$TEMP_DIR"
    TEMP_DIR=""

    # Restore SKIP_START and start services that were running before
    SKIP_START="$ORIG_SKIP_START"
    if [[ "$SKIP_START" == "false" ]]; then
        start_services
    fi

    # IPv6 disable + keepalived migration AFTER services start — restarting
    # keepalived before stratum is running causes VRRP health checks to fail
    migrate_runtime_dir
    migrate_disable_ipv6
    migrate_keepalived_config
    migrate_ha_mode_file
    migrate_coin_version_cache

    # Mark upgrade complete BEFORE verification — prevents automatic rollback
    # on a completed upgrade. Rollback only protects the destructive upgrade
    # window, not post-upgrade health checks.
    UPGRADE_IN_PROGRESS="false"

    # Verify and show summary
    verify_upgrade || true

    show_summary

    # Notify operator via Discord if coin daemon upgrades are available.
    # Runs after show_summary so the terminal output is always complete first.
    # Skipped in --auto mode (unattended runs where a human may not be watching Discord).
    if [[ "$AUTO_MODE" != "true" ]]; then
        notify_coin_upgrades
    fi

    # Post-upgrade config sanity check — catches key mismatches or placeholder
    # values that may surface after a config migration.
    if command -v spiralctl &>/dev/null; then
        echo ""
        spiralctl config validate || true
    fi
}

# Run main function
main "$@"
