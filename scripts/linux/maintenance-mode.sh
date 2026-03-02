#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool Maintenance Mode
# Unified command to enable/disable maintenance mode across all components
#
# Usage:
#   maintenance-mode.sh enable [duration_minutes]  - Enable maintenance mode (default: 60 min)
#   maintenance-mode.sh disable                    - Disable maintenance mode
#   maintenance-mode.sh status                     - Check current status
#   maintenance-mode.sh extend [minutes]           - Extend current maintenance window
#
# This script manages the maintenance mode flag that:
#   - Suppresses Sentinel alerts (miner offline, service down, etc.)
#   - Shows maintenance banner on dashboard
#   - Prevents auto-updates during maintenance
#   - Can be triggered via CLI, dashboard API, or programmatically
#

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
DIM='\033[2m'
BOLD='\033[1m'
NC='\033[0m'

# Configuration
INSTALL_DIR="/spiralpool"
MAINTENANCE_FILE="${INSTALL_DIR}/config/.maintenance-mode"
LOG_FILE="${INSTALL_DIR}/logs/maintenance.log"
LOCK_FILE="${INSTALL_DIR}/config/.maintenance-mode.lock"

get_pool_user() {
    stat -c '%U' "${INSTALL_DIR}" 2>/dev/null || echo "spiraluser"
}

# =============================================================================
# Security Helper Functions
# =============================================================================

# SECURITY: Validate a JSON file is safe to read
validate_json_file() {
    local file="$1"

    # File must exist and be a regular file (not symlink)
    if [[ ! -f "$file" ]] || [[ -L "$file" ]]; then
        return 1
    fi

    # File must be owned by pool user or root
    local owner
    owner=$(stat -c '%U' "$file" 2>/dev/null)
    local pool_user
    pool_user=$(get_pool_user)
    if [[ "$owner" != "$pool_user" ]] && [[ "$owner" != "root" ]]; then
        log "WARN" "Suspicious file owner for $file: $owner"
        return 1
    fi

    # File must not be world-writable
    local perms
    perms=$(stat -c '%a' "$file" 2>/dev/null)
    if [[ "${perms: -1}" =~ [2367] ]]; then
        log "WARN" "File is world-writable: $file"
        return 1
    fi

    return 0
}

# =============================================================================
# File Locking Functions (prevents race conditions)
# =============================================================================

# SECURITY: Use file locking to prevent race conditions
acquire_lock() {
    local timeout="${1:-10}"
    local waited=0

    mkdir -p "$(dirname "$LOCK_FILE")" 2>/dev/null || true

    while [[ $waited -lt $timeout ]]; do
        # Try to create lock file atomically
        if (set -o noclobber; echo "$$" > "$LOCK_FILE") 2>/dev/null; then
            # Successfully acquired lock
            trap 'release_lock' EXIT
            return 0
        fi

        # Check if lock holder is still running
        local lock_pid
        lock_pid=$(cat "$LOCK_FILE" 2>/dev/null || echo "")
        if [[ -n "$lock_pid" ]] && ! kill -0 "$lock_pid" 2>/dev/null; then
            # Lock holder is dead, remove stale lock
            rm -f "$LOCK_FILE"
            continue
        fi

        sleep 1
        waited=$((waited + 1))
    done

    log "ERROR" "Could not acquire lock after ${timeout} seconds"
    return 1
}

release_lock() {
    rm -f "$LOCK_FILE" 2>/dev/null || true
}

# =============================================================================
# Helper Functions
# =============================================================================

log() {
    local level="$1"
    local message="$2"
    local timestamp
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')

    mkdir -p "$(dirname "${LOG_FILE}")" 2>/dev/null || true

    # SECURITY: Create log file with restrictive permissions if it doesn't exist
    if [[ ! -f "${LOG_FILE}" ]]; then
        touch "${LOG_FILE}" 2>/dev/null || true
        chmod 640 "${LOG_FILE}" 2>/dev/null || true
    fi

    echo "[${timestamp}] [${level}] ${message}" >> "${LOG_FILE}" 2>/dev/null || true

    case "$level" in
        INFO) echo -e "${BLUE}[INFO]${NC} ${message}" ;;
        SUCCESS) echo -e "${GREEN}[SUCCESS]${NC} ${message}" ;;
        WARN) echo -e "${YELLOW}[WARNING]${NC} ${message}" ;;
        ERROR) echo -e "${RED}[ERROR]${NC} ${message}" ;;
    esac
}

get_node_uuid() {
    local uuid_file="${INSTALL_DIR}/config/node-uuid"
    if [[ -f "$uuid_file" ]] && [[ ! -L "$uuid_file" ]]; then
        # SECURITY: Validate UUID format (alphanumeric and hyphens only, max 64 chars)
        local uuid_content
        uuid_content=$(head -c 64 "$uuid_file" 2>/dev/null | tr -cd 'a-zA-Z0-9-')
        if [[ -n "$uuid_content" ]]; then
            echo "$uuid_content"
            return
        fi
    fi
    echo "unknown"
}

# Load notification settings from Sentinel config
load_notification_settings() {
    local pool_user
    pool_user=$(stat -c '%U' "${INSTALL_DIR}" 2>/dev/null || echo "spiraluser")
    # Primary: install dir fallback (works under systemd ProtectHome=yes)
    # Secondary: home dir (works for manual invocation)
    local sentinel_config="${INSTALL_DIR}/config/sentinel/config.json"
    if [[ ! -f "$sentinel_config" ]]; then
        sentinel_config="/home/${pool_user}/.spiralsentinel/config.json"
    fi

    DISCORD_WEBHOOK=""
    TELEGRAM_BOT_TOKEN=""
    TELEGRAM_CHAT_ID=""
    TELEGRAM_ENABLED="false"

    if [[ -f "$sentinel_config" ]]; then
        if command -v jq &>/dev/null; then
            DISCORD_WEBHOOK=$(jq -r '.discord_webhook_url // ""' "$sentinel_config" 2>/dev/null || echo "")
            TELEGRAM_BOT_TOKEN=$(jq -r '.telegram_bot_token // ""' "$sentinel_config" 2>/dev/null || echo "")
            TELEGRAM_CHAT_ID=$(jq -r '.telegram_chat_id // ""' "$sentinel_config" 2>/dev/null || echo "")
            TELEGRAM_ENABLED=$(jq -r '.telegram_enabled // false' "$sentinel_config" 2>/dev/null || echo "false")
        else
            DISCORD_WEBHOOK=$(grep -oP '"discord_webhook_url":\s*"\K[^"]+' "$sentinel_config" 2>/dev/null || echo "")
            TELEGRAM_BOT_TOKEN=$(grep -oP '"telegram_bot_token":\s*"\K[^"]+' "$sentinel_config" 2>/dev/null || echo "")
            TELEGRAM_CHAT_ID=$(grep -oP '"telegram_chat_id":\s*"\K[^"]+' "$sentinel_config" 2>/dev/null || echo "")
            TELEGRAM_ENABLED=$(grep -oP '"telegram_enabled":\s*\K(true|false)' "$sentinel_config" 2>/dev/null || echo "false")
        fi
    fi
}

send_discord_notification() {
    local message="$1"
    local color="${2:-3447003}"  # Default blue

    if [[ -z "$DISCORD_WEBHOOK" ]] || [[ "$DISCORD_WEBHOOK" == "null" ]]; then
        return
    fi

    # SECURITY: Validate webhook URL format to prevent SSRF/injection
    if ! [[ "$DISCORD_WEBHOOK" =~ ^https://discord(app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]+$ ]]; then
        log "ERROR" "Invalid Discord webhook URL format - skipping notification"
        return 1
    fi

    # SECURITY: Use jq for safe JSON construction to prevent injection
    local payload
    local node_id
    node_id=$(get_node_uuid | cut -c1-8)

    if command -v jq &>/dev/null; then
        payload=$(jq -n \
            --arg title "Spiral Pool Maintenance" \
            --arg desc "$message" \
            --argjson color "$color" \
            --arg footer "Node: ${node_id}..." \
            '{embeds: [{title: $title, description: $desc, color: $color, footer: {text: $footer}}]}')
    else
        # Fallback: escape special characters manually
        local escaped_message
        escaped_message=$(printf '%s' "$message" | sed 's/\\/\\\\/g; s/"/\\"/g; s/\n/\\n/g')
        payload="{\"embeds\": [{\"title\": \"Spiral Pool Maintenance\", \"description\": \"${escaped_message}\", \"color\": ${color}, \"footer\": {\"text\": \"Node: ${node_id}...\"}}]}"
    fi

    # SECURITY: Use --max-time to prevent hanging, --data-raw to prevent interpretation
    curl -s --max-time 10 -H "Content-Type: application/json" --data-raw "${payload}" "${DISCORD_WEBHOOK}" > /dev/null 2>&1 || true
}

send_telegram_notification() {
    local message="$1"

    if [[ "$TELEGRAM_ENABLED" != "true" ]] || [[ -z "$TELEGRAM_BOT_TOKEN" ]] || [[ -z "$TELEGRAM_CHAT_ID" ]]; then
        return
    fi

    # SECURITY: Validate bot token format (numeric:alphanumeric)
    if ! [[ "$TELEGRAM_BOT_TOKEN" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]]; then
        log "ERROR" "Invalid Telegram bot token format - skipping notification"
        return 1
    fi

    # SECURITY: Validate chat ID format (numeric, may have - prefix for groups)
    if ! [[ "$TELEGRAM_CHAT_ID" =~ ^-?[0-9]+$ ]]; then
        log "ERROR" "Invalid Telegram chat ID format - skipping notification"
        return 1
    fi

    local url="https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage"
    local node_id
    node_id=$(get_node_uuid | cut -c1-8)

    # SECURITY: Use jq for safe JSON construction
    local payload
    if command -v jq &>/dev/null; then
        payload=$(jq -n \
            --arg chat_id "$TELEGRAM_CHAT_ID" \
            --arg text "${message}\n\nNode: ${node_id}..." \
            '{chat_id: $chat_id, text: $text, parse_mode: "HTML"}')
        curl -s --max-time 10 -H "Content-Type: application/json" --data-raw "${payload}" "${url}" > /dev/null 2>&1 || true
    else
        # Fallback: use URL encoding via curl's --data-urlencode
        curl -s --max-time 10 -X POST "$url" \
            --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
            --data-urlencode "text=${message}

Node: ${node_id}..." \
            --data-urlencode "parse_mode=HTML" > /dev/null 2>&1 || true
    fi
}

format_duration() {
    local seconds=$1
    local hours=$((seconds / 3600))
    local minutes=$(((seconds % 3600) / 60))

    if [[ $hours -gt 0 ]]; then
        echo "${hours}h ${minutes}m"
    else
        echo "${minutes}m"
    fi
}

# =============================================================================
# Maintenance Mode Functions
# =============================================================================

enable_maintenance() {
    local duration_minutes="${1:-60}"
    local reason="${2:-Manual maintenance}"

    # SECURITY: Acquire lock to prevent race conditions
    if ! acquire_lock 5; then
        echo -e "${RED}Another maintenance operation is in progress${NC}"
        return 1
    fi

    # SECURITY: Validate duration is a positive integer within safe bounds (1 min to 1 week)
    if ! [[ "$duration_minutes" =~ ^[0-9]+$ ]]; then
        log "ERROR" "Invalid duration: must be a number (got: $duration_minutes)"
        echo -e "${RED}Invalid duration: must be a number${NC}"
        return 1
    fi
    if [[ "$duration_minutes" -lt 1 ]] || [[ "$duration_minutes" -gt 10080 ]]; then
        log "ERROR" "Invalid duration: must be 1-10080 minutes (1 week max)"
        echo -e "${RED}Invalid duration: must be 1-10080 minutes (1 week max)${NC}"
        return 1
    fi

    # SECURITY: Sanitize reason to prevent JSON injection (remove special chars)
    reason=$(echo "$reason" | tr -cd '[:alnum:] [:space:]-_')
    reason="${reason:-Manual maintenance}"

    local now=$(date +%s)
    local end_time=$((now + (duration_minutes * 60)))

    mkdir -p "${INSTALL_DIR}/config" 2>/dev/null || true

    # Create maintenance mode file with metadata
    cat > "${MAINTENANCE_FILE}" << EOF
{
    "enabled": true,
    "start_time": ${now},
    "end_time": ${end_time},
    "duration_minutes": ${duration_minutes},
    "reason": "${reason}",
    "started_by": "${USER:-unknown}",
    "node_uuid": "$(get_node_uuid)"
}
EOF

    # SECURITY: Restrict file permissions to pool user only
    chmod 600 "${MAINTENANCE_FILE}"

    log "SUCCESS" "Maintenance mode ENABLED for ${duration_minutes} minutes"
    log "INFO" "Reason: ${reason}"
    log "INFO" "End time: $(date -d "@${end_time}" '+%Y-%m-%d %H:%M:%S')"

    # Send notifications
    load_notification_settings
    local msg="MAINTENANCE MODE ENABLED\n\nDuration: ${duration_minutes} minutes\nReason: ${reason}\nEnd: $(date -d "@${end_time}" '+%H:%M:%S')\n\nAlerts are suppressed during this period."
    send_discord_notification "$msg" 16776960  # Yellow
    send_telegram_notification "<b>Maintenance Mode Enabled</b>\n\nDuration: ${duration_minutes} minutes\nReason: ${reason}\nEnd: $(date -d "@${end_time}" '+%H:%M:%S')\n\nAlerts are suppressed."

    echo ""
    echo -e "${YELLOW}${BOLD}MAINTENANCE MODE ACTIVE${NC}"
    echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "Duration:    ${CYAN}${duration_minutes} minutes${NC}"
    echo -e "Reason:      ${reason}"
    echo -e "End time:    $(date -d "@${end_time}" '+%Y-%m-%d %H:%M:%S')"
    echo ""
    echo -e "${DIM}Alerts are suppressed. Services continue running.${NC}"
    echo -e "${DIM}To end early: ${NC}${CYAN}spiralpool-maintenance disable${NC}"
    echo ""
}

disable_maintenance() {
    # SECURITY: Acquire lock to prevent race conditions
    if ! acquire_lock 5; then
        echo -e "${RED}Another maintenance operation is in progress${NC}"
        return 1
    fi

    # Check if maintenance file exists
    if [[ ! -f "${MAINTENANCE_FILE}" ]]; then
        log "WARN" "Maintenance mode is not currently active"
        echo -e "${YELLOW}Maintenance mode is not currently active${NC}"
        return 0
    fi

    # Get info before removing
    local start_time="0"
    start_time=$(grep -oP '"start_time":\s*\K[0-9]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "0")

    local now=$(date +%s)
    local actual_duration=$((now - start_time))

    # Remove maintenance file
    rm -f "${MAINTENANCE_FILE}"

    log "SUCCESS" "Maintenance mode DISABLED"
    log "INFO" "Actual duration: $(format_duration $actual_duration)"

    # Send notifications
    load_notification_settings
    local msg="MAINTENANCE MODE ENDED\n\nActual duration: $(format_duration $actual_duration)\n\nAlerts are now active."
    send_discord_notification "$msg" 3066993  # Green
    send_telegram_notification "<b>Maintenance Mode Ended</b>\n\nActual duration: $(format_duration $actual_duration)\n\nAlerts are now active."

    echo ""
    echo -e "${GREEN}${BOLD}MAINTENANCE MODE DISABLED${NC}"
    echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "Duration was: $(format_duration $actual_duration)"
    echo ""
    echo -e "${GREEN}Alerts are now active.${NC}"
    echo ""
}

extend_maintenance() {
    local additional_minutes="${1:-30}"

    # SECURITY: Acquire lock to prevent race conditions
    if ! acquire_lock 5; then
        echo -e "${RED}Another maintenance operation is in progress${NC}"
        return 1
    fi

    # SECURITY: Validate duration is a positive integer within safe bounds
    if ! [[ "$additional_minutes" =~ ^[0-9]+$ ]]; then
        log "ERROR" "Invalid duration: must be a number"
        echo -e "${RED}Invalid duration: must be a number${NC}"
        return 1
    fi
    if [[ "$additional_minutes" -lt 1 ]] || [[ "$additional_minutes" -gt 10080 ]]; then
        log "ERROR" "Invalid duration: must be 1-10080 minutes"
        echo -e "${RED}Invalid duration: must be 1-10080 minutes${NC}"
        return 1
    fi

    if [[ ! -f "${MAINTENANCE_FILE}" ]]; then
        log "ERROR" "Maintenance mode is not active. Enable it first."
        echo -e "${RED}Maintenance mode is not active. Enable it first.${NC}"
        return 1
    fi

    # SECURITY: Check maintenance file is not a symlink
    if [[ -L "${MAINTENANCE_FILE}" ]]; then
        log "ERROR" "Maintenance file is a symlink - security violation"
        echo -e "${RED}Security error: maintenance file is a symlink${NC}"
        return 1
    fi

    local current_end
    current_end=$(grep -oP '"end_time":\s*\K[0-9]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "0")
    local new_end=$((current_end + (additional_minutes * 60)))

    # SECURITY: Validate new_end is strictly numeric and within bounds
    if ! [[ "$new_end" =~ ^[0-9]+$ ]]; then
        log "ERROR" "Invalid end time calculation"
        return 1
    fi

    # SECURITY: Check for timestamp overflow (year 2038 problem)
    local max_timestamp=2147483647
    if [[ "$new_end" -gt "$max_timestamp" ]]; then
        log "ERROR" "Duration too long - would overflow timestamp"
        echo -e "${RED}Duration too long${NC}"
        return 1
    fi

    # SECURITY: Use jq for safe JSON modification if available, otherwise use sed safely
    if command -v jq &>/dev/null; then
        # SECURITY: Set restrictive umask before mktemp to prevent race condition
        local old_umask
        old_umask=$(umask)
        umask 077

        local temp_file
        temp_file=$(mktemp) || { umask "$old_umask"; log "ERROR" "Failed to create temp file"; return 1; }

        umask "$old_umask"
        chmod 600 "$temp_file"  # Defense in depth

        if jq --argjson end "$new_end" '.end_time = $end' "${MAINTENANCE_FILE}" > "$temp_file" 2>/dev/null; then
            mv "$temp_file" "${MAINTENANCE_FILE}"
            chmod 600 "${MAINTENANCE_FILE}"
        else
            rm -f "$temp_file"
            log "ERROR" "Failed to update maintenance file"
            return 1
        fi
    else
        # Fallback to sed with validated numeric value
        sed -i "s/\"end_time\": *[0-9]*/\"end_time\": ${new_end}/" "${MAINTENANCE_FILE}"
    fi

    log "SUCCESS" "Maintenance extended by ${additional_minutes} minutes"
    log "INFO" "New end time: $(date -d "@${new_end}" '+%Y-%m-%d %H:%M:%S')"

    # Send notifications
    load_notification_settings
    local msg="MAINTENANCE EXTENDED\n\nAdditional: ${additional_minutes} minutes\nNew end: $(date -d "@${new_end}" '+%H:%M:%S')"
    send_discord_notification "$msg" 16776960  # Yellow
    send_telegram_notification "<b>Maintenance Extended</b>\n\nAdditional: ${additional_minutes} minutes\nNew end: $(date -d "@${new_end}" '+%H:%M:%S')"

    echo ""
    echo -e "${YELLOW}Maintenance extended by ${additional_minutes} minutes${NC}"
    echo -e "New end time: $(date -d "@${new_end}" '+%Y-%m-%d %H:%M:%S')"
    echo ""
}

show_status() {
    echo ""
    echo -e "${CYAN}${BOLD}Maintenance Mode Status${NC}"
    echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    local now=$(date +%s)

    # Check if maintenance file is active
    if [[ ! -f "${MAINTENANCE_FILE}" ]]; then
        echo -e "Status:      ${GREEN}INACTIVE${NC}"
        echo -e "Alerts:      ${GREEN}Active${NC}"
        echo ""
        return 0
    fi

    local end_time=$(grep -oP '"end_time":\s*\K[0-9]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "0")
    if [[ $now -ge $end_time ]]; then
        echo -e "Status:      ${GREEN}INACTIVE${NC}"
        echo -e "Alerts:      ${GREEN}Active${NC}"
        echo ""
        rm -f "${MAINTENANCE_FILE}" 2>/dev/null
        return 0
    fi

    local start_time=$(grep -oP '"start_time":\s*\K[0-9]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "0")
    local reason=$(grep -oP '"reason":\s*"\K[^"]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "Unknown")
    local started_by=$(grep -oP '"started_by":\s*"\K[^"]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "Unknown")

    if [[ $now -ge $end_time ]]; then
        # Maintenance period has expired
        echo -e "Status:      ${YELLOW}EXPIRED${NC} (auto-clearing...)"
        rm -f "${MAINTENANCE_FILE}"
        echo -e "Alerts:      ${GREEN}Active${NC}"
        echo ""
        log "INFO" "Maintenance mode expired and auto-cleared"
        return 0
    fi

    local remaining=$((end_time - now))
    local elapsed=$((now - start_time))

    echo -e "Status:      ${YELLOW}${BOLD}ACTIVE${NC}"
    echo -e "Alerts:      ${RED}Suppressed${NC}"
    echo ""
    echo -e "Reason:      ${reason}"
    echo -e "Started by:  ${started_by}"
    echo -e "Started:     $(date -d "@${start_time}" '+%Y-%m-%d %H:%M:%S')"
    echo -e "Ends:        $(date -d "@${end_time}" '+%Y-%m-%d %H:%M:%S')"
    echo ""
    echo -e "Elapsed:     $(format_duration $elapsed)"
    echo -e "Remaining:   ${CYAN}$(format_duration $remaining)${NC}"
    echo ""

    # Show progress bar
    local total=$((end_time - start_time))
    local pct=$((elapsed * 100 / total))
    local bar_len=30
    local filled=$((pct * bar_len / 100))
    local empty=$((bar_len - filled))

    printf "Progress:    ["
    printf "${YELLOW}%0.s█${NC}" $(seq 1 $filled) 2>/dev/null || true
    printf "%0.s░" $(seq 1 $empty) 2>/dev/null || true
    printf "] %d%%\n" "$pct"
    echo ""
}

# Check if maintenance mode is active (for use by other scripts)
# Checks both new and legacy locations for backwards compatibility
is_maintenance_active() {
    local now=$(date +%s)

    # Check new maintenance file
    if [[ -f "${MAINTENANCE_FILE}" ]]; then
        local end_time=$(grep -oP '"end_time":\s*\K[0-9]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "0")
        if [[ $now -lt $end_time ]]; then
            return 0  # Active
        else
            # Expired, clean up
            rm -f "${MAINTENANCE_FILE}"
        fi
    fi

    return 1  # Not active
}

# Get remaining maintenance time in seconds (for use by other scripts)
get_maintenance_remaining() {
    if ! is_maintenance_active; then
        echo "0"
        return
    fi

    local now=$(date +%s)

    if [[ -f "${MAINTENANCE_FILE}" ]]; then
        local end_time=$(grep -oP '"end_time":\s*\K[0-9]+' "${MAINTENANCE_FILE}" 2>/dev/null || echo "0")
        if [[ $now -lt $end_time ]]; then
            echo $((end_time - now))
            return
        fi
    fi

    echo "0"
}

show_help() {
    echo ""
    echo -e "${CYAN}${BOLD}Spiral Pool Maintenance Mode${NC}"
    echo ""
    echo "Usage: $0 <command> [options]"
    echo ""
    echo "Commands:"
    echo "  enable [minutes] [reason]  Enable maintenance mode (default: 60 minutes)"
    echo "  disable                    Disable maintenance mode"
    echo "  status                     Show current maintenance status"
    echo "  extend [minutes]           Extend current maintenance (default: 30 minutes)"
    echo "  check                      Silent check (exit 0 if active, 1 if not)"
    echo "  remaining                  Print remaining seconds (for scripts)"
    echo "  help                       Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 enable                  # Enable for 60 minutes"
    echo "  $0 enable 120              # Enable for 2 hours"
    echo "  $0 enable 30 \"Server upgrade\"  # Enable with reason"
    echo "  $0 extend 15               # Add 15 more minutes"
    echo "  $0 disable                 # End maintenance early"
    echo ""
    echo "During maintenance mode:"
    echo "  - Sentinel alerts are suppressed (miner offline, etc.)"
    echo "  - Dashboard shows maintenance banner"
    echo "  - Auto-updates are paused"
    echo "  - Services continue running normally"
    echo ""
}

# =============================================================================
# Main
# =============================================================================

main() {
    local command="${1:-status}"
    shift || true

    case "$command" in
        enable|on|start)
            local minutes="${1:-60}"
            local reason="${2:-Manual maintenance}"
            enable_maintenance "$minutes" "$reason"
            ;;
        disable|off|stop|end)
            disable_maintenance
            ;;
        extend|add)
            local minutes="${1:-30}"
            extend_maintenance "$minutes"
            ;;
        status|show)
            show_status
            ;;
        check|is-active)
            # Silent check for scripts
            if is_maintenance_active; then
                exit 0
            else
                exit 1
            fi
            ;;
        remaining|time-left)
            get_maintenance_remaining
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            echo -e "${RED}Unknown command: ${command}${NC}"
            show_help
            exit 1
            ;;
    esac
}

# Run if not sourced
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
