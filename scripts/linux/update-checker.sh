#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool Update Checker
# Checks for new versions and either auto-updates or notifies the user
#
# This script is run periodically via cron or systemd timer
# Settings are stored in /spiralpool/config/update-settings.conf
#

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

# Configuration
INSTALL_DIR="/spiralpool"
CONFIG_FILE="${INSTALL_DIR}/config/update-settings.conf"
VERSION_FILE="${INSTALL_DIR}/VERSION"
LOG_FILE="${INSTALL_DIR}/logs/update-checker.log"
LOCK_FILE="/run/spiralpool/update-check.lock"
SENTINEL_CONFIG=""  # Will be set by load_sentinel_config()

# GitHub repository
GITHUB_REPO="SpiralPool/Spiral-Pool"
GITHUB_API="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"

# Default settings
UPDATE_MODE="notify"  # "auto", "notify", or "disabled"
CHECK_INTERVAL="daily"  # "hourly", "daily", "weekly"
NOTIFY_DISCORD=false
NOTIFY_TELEGRAM=false
DISCORD_WEBHOOK=""
TELEGRAM_BOT_TOKEN=""
TELEGRAM_CHAT_ID=""

# =============================================================================
# Helper Functions
# =============================================================================

log() {
    local level="$1"
    local message="$2"
    local timestamp
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')

    # SECURITY: Create log directory and file with restrictive permissions
    # Use || true on all operations — log failures must never kill the script (set -e)
    mkdir -p "$(dirname "${LOG_FILE}")" 2>/dev/null || true
    if [[ ! -f "${LOG_FILE}" ]]; then
        touch "${LOG_FILE}" 2>/dev/null || true
        chmod 640 "${LOG_FILE}" 2>/dev/null || true
    fi

    echo "[${timestamp}] [${level}] ${message}" >> "${LOG_FILE}" 2>/dev/null || true

    if [[ "$VERBOSE" == "true" ]]; then
        case "$level" in
            INFO) echo -e "${BLUE}[INFO]${NC} ${message}" ;;
            SUCCESS) echo -e "${GREEN}[SUCCESS]${NC} ${message}" ;;
            WARN) echo -e "${YELLOW}[WARNING]${NC} ${message}" ;;
            ERROR) echo -e "${RED}[ERROR]${NC} ${message}" ;;
        esac
    fi
}

load_sentinel_config() {
    # Find and load settings from Sentinel config.json
    # This is the authoritative source for update mode settings
    local pool_user
    pool_user=$(stat -c '%U' "${INSTALL_DIR}" 2>/dev/null || echo "spiralpool")
    # SECURITY: Validate username format (prevents path traversal in config path)
    if ! [[ "$pool_user" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
        log "WARN" "Invalid pool user name: $pool_user, falling back to spiraluser"
        pool_user="spiraluser"
    fi
    # Primary: install dir fallback (works under systemd ProtectHome=yes)
    # Secondary: home dir (works for manual invocation)
    SENTINEL_CONFIG="${INSTALL_DIR}/config/sentinel/config.json"
    if [[ ! -f "${SENTINEL_CONFIG}" ]]; then
        SENTINEL_CONFIG="/home/${pool_user}/.spiralsentinel/config.json"
    fi

    if [[ -f "${SENTINEL_CONFIG}" ]]; then
        # SECURITY: Use jq for safe JSON parsing (falls back to grep if jq unavailable)
        if command -v jq &>/dev/null; then
            # Read auto_update_mode from JSON config using jq
            local mode
            mode=$(jq -r '.auto_update_mode // ""' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            if [[ -n "$mode" ]] && [[ "$mode" != "null" ]]; then
                # SECURITY: Validate mode is one of expected values
                case "$mode" in
                    auto|notify|disabled)
                        UPDATE_MODE="$mode"
                        log "INFO" "Loaded update mode from Sentinel config: ${UPDATE_MODE}"
                        ;;
                    *)
                        log "WARN" "Invalid update mode in config: $mode, using default"
                        ;;
                esac
            fi

            # Read update_check_enabled
            local enabled
            enabled=$(jq -r '.update_check_enabled // ""' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            if [[ "$enabled" == "false" ]]; then
                UPDATE_MODE="disabled"
                log "INFO" "Update checking disabled in Sentinel config"
            fi

            # Read Discord webhook
            local webhook
            webhook=$(jq -r '.discord_webhook_url // ""' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            if [[ -n "$webhook" ]] && [[ "$webhook" != "null" ]] && [[ "$webhook" != "" ]]; then
                DISCORD_WEBHOOK="$webhook"
                NOTIFY_DISCORD=true
            fi

            # Read Telegram settings
            local bot_token chat_id tg_enabled
            bot_token=$(jq -r '.telegram_bot_token // ""' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            chat_id=$(jq -r '.telegram_chat_id // ""' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            tg_enabled=$(jq -r '.telegram_enabled // false' "${SENTINEL_CONFIG}" 2>/dev/null || echo "false")
            if [[ "$tg_enabled" == "true" ]] && [[ -n "$bot_token" ]] && [[ -n "$chat_id" ]]; then
                TELEGRAM_BOT_TOKEN="$bot_token"
                TELEGRAM_CHAT_ID="$chat_id"
                NOTIFY_TELEGRAM=true
            fi
        else
            # Fallback to grep if jq not available (less secure)
            log "WARN" "jq not installed, using grep for JSON parsing (install jq for better security)"
            local mode
            mode=$(grep -oP '"auto_update_mode":\s*"\K[^"]+' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            if [[ -n "$mode" ]]; then
                case "$mode" in
                    auto|notify|disabled) UPDATE_MODE="$mode" ;;
                esac
            fi

            local enabled
            enabled=$(grep -oP '"update_check_enabled":\s*\K(true|false)' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            [[ "$enabled" == "false" ]] && UPDATE_MODE="disabled"

            local webhook
            webhook=$(grep -oP '"discord_webhook_url":\s*"\K[^"]+' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            if [[ -n "$webhook" ]] && [[ "$webhook" != "null" ]]; then
                DISCORD_WEBHOOK="$webhook"
                NOTIFY_DISCORD=true
            fi

            local bot_token chat_id tg_enabled
            bot_token=$(grep -oP '"telegram_bot_token":\s*"\K[^"]+' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            chat_id=$(grep -oP '"telegram_chat_id":\s*"\K[^"]+' "${SENTINEL_CONFIG}" 2>/dev/null || echo "")
            tg_enabled=$(grep -oP '"telegram_enabled":\s*\K(true|false)' "${SENTINEL_CONFIG}" 2>/dev/null || echo "false")
            if [[ "$tg_enabled" == "true" ]] && [[ -n "$bot_token" ]] && [[ -n "$chat_id" ]]; then
                TELEGRAM_BOT_TOKEN="$bot_token"
                TELEGRAM_CHAT_ID="$chat_id"
                NOTIFY_TELEGRAM=true
            fi
        fi

        log "INFO" "Loaded settings from Sentinel config: ${SENTINEL_CONFIG}"
    else
        log "WARN" "Sentinel config not found at ${SENTINEL_CONFIG}"
    fi
}

load_settings() {
    # First load from legacy config file (for backwards compatibility)
    # SECURITY: Parse config file safely without sourcing to prevent arbitrary code execution
    if [[ -f "${CONFIG_FILE}" ]]; then
        # Validate file is not a symlink and has safe permissions
        if [[ -L "${CONFIG_FILE}" ]]; then
            log "WARN" "Config file is a symlink - skipping for security"
        else
            local perms
            perms=$(stat -c '%a' "${CONFIG_FILE}" 2>/dev/null || echo "644")
            if [[ "${perms: -1}" =~ [2367] ]]; then
                log "WARN" "Config file is world-writable - skipping for security"
            else
                # Safe parsing: extract known variables only
                local parsed_mode parsed_interval parsed_discord parsed_tg
                parsed_mode=$(grep -oP '^UPDATE_MODE="?\K[^"\s]+' "${CONFIG_FILE}" 2>/dev/null || echo "")
                parsed_interval=$(grep -oP '^CHECK_INTERVAL="?\K[^"\s]+' "${CONFIG_FILE}" 2>/dev/null || echo "")
                parsed_discord=$(grep -oP '^NOTIFY_DISCORD=\K(true|false)' "${CONFIG_FILE}" 2>/dev/null || echo "")
                parsed_tg=$(grep -oP '^NOTIFY_TELEGRAM=\K(true|false)' "${CONFIG_FILE}" 2>/dev/null || echo "")

                # Validate and apply only expected values
                case "$parsed_mode" in
                    auto|notify|disabled) UPDATE_MODE="$parsed_mode" ;;
                esac
                case "$parsed_interval" in
                    hourly|daily|weekly) CHECK_INTERVAL="$parsed_interval" ;;
                esac
                [[ "$parsed_discord" == "true" ]] && NOTIFY_DISCORD=true
                [[ "$parsed_tg" == "true" ]] && NOTIFY_TELEGRAM=true

                log "INFO" "Loaded settings from ${CONFIG_FILE} (safe parse)"
            fi
        fi
    else
        log "WARN" "No settings file found, using defaults"
    fi

    # Then override with Sentinel config (authoritative source)
    load_sentinel_config
}

save_settings() {
    # SECURITY: Create directory if needed with restrictive permissions
    mkdir -p "$(dirname "${CONFIG_FILE}")" 2>/dev/null || true

    # SECURITY: Use temp file with restrictive permissions from creation
    # Set umask before mktemp to ensure file is created with 0600 permissions
    local old_umask
    old_umask=$(umask)
    umask 077  # Files created with mode 0600 (rw-------)

    local temp_file
    # Prefer /run/spiralpool (owned by POOL_USER via tmpfiles.d, not world-writable)
    # Fall back to /tmp if /run/spiralpool is unavailable (e.g., before first reboot)
    temp_file=$(mktemp /run/spiralpool/update-settings-XXXXXX 2>/dev/null) || \
    temp_file=$(mktemp /tmp/spiralpool-update-settings-XXXXXX) || {
        umask "$old_umask"
        log "ERROR" "Failed to create temp file"
        return 1
    }

    # Restore original umask
    umask "$old_umask"

    # Double-check permissions (defense in depth)
    chmod 600 "$temp_file"

    # Use printf per-line instead of heredoc to prevent heredoc delimiter breakout
    # if a variable value happens to contain the delimiter string
    {
        printf '# Spiral Pool Update Settings\n'
        printf '# Generated on %s\n\n' "$(date)"
        printf '# Update mode: "auto", "notify", or "disabled"\n'
        printf 'UPDATE_MODE="%s"\n\n' "$UPDATE_MODE"
        printf '# Check interval: "hourly", "daily", "weekly"\n'
        printf 'CHECK_INTERVAL="%s"\n\n' "$CHECK_INTERVAL"
        printf '# Discord notifications\n'
        printf 'NOTIFY_DISCORD=%s\n' "$NOTIFY_DISCORD"
        printf 'DISCORD_WEBHOOK="%s"\n\n' "$DISCORD_WEBHOOK"
        printf '# Telegram notifications\n'
        printf 'NOTIFY_TELEGRAM=%s\n' "$NOTIFY_TELEGRAM"
        printf 'TELEGRAM_BOT_TOKEN="%s"\n' "$TELEGRAM_BOT_TOKEN"
        printf 'TELEGRAM_CHAT_ID="%s"\n\n' "$TELEGRAM_CHAT_ID"
        printf '# Last check timestamp\n'
        printf 'LAST_CHECK="%s"\n\n' "$(date +%s)"
        printf '# Last notified version (to avoid duplicate notifications)\n'
        printf 'LAST_NOTIFIED_VERSION="%s"\n' "${LAST_NOTIFIED_VERSION:-}"
    } > "$temp_file"

    # Atomic move
    mv "$temp_file" "${CONFIG_FILE}"
    log "INFO" "Settings saved to ${CONFIG_FILE}"
}

get_current_version() {
    if [[ -f "${VERSION_FILE}" ]]; then
        cat "${VERSION_FILE}" | tr -d '[:space:]'
    else
        echo "1.1.1"
    fi
}

get_latest_version() {
    local response
    response=$(curl -s -H "Accept: application/vnd.github.v3+json" "${GITHUB_API}" 2>/dev/null)

    if [[ $? -ne 0 ]] || [[ -z "$response" ]]; then
        log "ERROR" "Failed to fetch latest version from GitHub"
        return 1
    fi

    # Extract version from tag_name (e.g., "v1.0.1" -> "1.0.1")
    local version
    version=$(echo "$response" | grep -oP '"tag_name":\s*"v?\K[^"]+' | head -1)

    if [[ -z "$version" ]]; then
        log "ERROR" "Could not parse version from GitHub response"
        return 1
    fi

    echo "$version"
}

version_compare() {
    # Returns: 0 if equal, 1 if $1 > $2, 2 if $1 < $2
    local v1="$1"
    local v2="$2"

    if [[ "$v1" == "$v2" ]]; then
        return 0
    fi

    local IFS='.'
    local i
    local v1_parts=($v1)
    local v2_parts=($v2)

    # Fill empty parts with zeros
    for ((i=${#v1_parts[@]}; i<${#v2_parts[@]}; i++)); do
        v1_parts[i]=0
    done
    for ((i=${#v2_parts[@]}; i<${#v1_parts[@]}; i++)); do
        v2_parts[i]=0
    done

    for ((i=0; i<${#v1_parts[@]}; i++)); do
        if ((10#${v1_parts[i]} > 10#${v2_parts[i]})); then
            return 1
        fi
        if ((10#${v1_parts[i]} < 10#${v2_parts[i]})); then
            return 2
        fi
    done

    return 0
}

send_discord_notification() {
    local message="$1"

    if [[ "$NOTIFY_DISCORD" != "true" ]] || [[ -z "$DISCORD_WEBHOOK" ]]; then
        return
    fi

    # SECURITY: Validate webhook URL format to prevent SSRF/injection
    if ! [[ "$DISCORD_WEBHOOK" =~ ^https://discord(app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]+$ ]]; then
        log "ERROR" "Invalid Discord webhook URL format - skipping notification"
        return 1
    fi

    # SECURITY: Use jq to properly escape JSON, preventing injection attacks
    local payload
    local node_name
    node_name=$(hostname 2>/dev/null || echo "unknown")

    if command -v jq &>/dev/null; then
        payload=$(jq -n \
            --arg title "Spiral Pool Update Available" \
            --arg desc "$message" \
            --arg footer "$node_name" \
            '{embeds: [{title: $title, description: $desc, color: 3447003, footer: {text: $footer}}]}')
    else
        # Fallback: manually escape special characters for JSON
        local escaped_message
        escaped_message=$(printf '%s' "$message" | sed ':a;N;$!ba;s/\\/\\\\/g; s/"/\\"/g; s/\n/\\n/g')
        payload="{\"embeds\": [{\"title\": \"Spiral Pool Update Available\", \"description\": \"${escaped_message}\", \"color\": 3447003, \"footer\": {\"text\": \"${node_name}\"}}]}"
    fi

    # SECURITY: Use --max-time and --data-raw for safe curl execution
    curl -s --max-time 10 -H "Content-Type: application/json" --data-raw "${payload}" "${DISCORD_WEBHOOK}" > /dev/null 2>&1
    log "INFO" "Discord notification sent"
}

send_telegram_notification() {
    local message="$1"

    if [[ "$NOTIFY_TELEGRAM" != "true" ]] || [[ -z "$TELEGRAM_BOT_TOKEN" ]] || [[ -z "$TELEGRAM_CHAT_ID" ]]; then
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

    local node_name
    node_name=$(hostname 2>/dev/null || echo "unknown")

    # SECURITY: Use jq to properly escape JSON, preventing injection attacks
    local payload
    if command -v jq &>/dev/null; then
        payload=$(jq -n \
            --arg chat_id "$TELEGRAM_CHAT_ID" \
            --arg text "$(printf '%s\n\n%s' "$message" "$node_name")" \
            '{chat_id: $chat_id, text: $text, parse_mode: "HTML"}')
    else
        # Fallback: manually escape special characters for JSON
        local escaped_message
        escaped_message=$(printf '%s\n\n%s' "$message" "$node_name" | sed ':a;N;$!ba;s/\\/\\\\/g; s/"/\\"/g; s/\n/\\n/g')
        payload="{\"chat_id\": \"${TELEGRAM_CHAT_ID}\", \"text\": \"${escaped_message}\", \"parse_mode\": \"HTML\"}"
    fi

    # SECURITY: Use --max-time and --data-raw for safe curl execution
    curl -s --max-time 10 -H "Content-Type: application/json" --data-raw "${payload}" "${url}" > /dev/null 2>&1
    log "INFO" "Telegram notification sent"
}

notify_user() {
    local current="$1"
    local latest="$2"

    local message="A new version of Spiral Pool is available!\n\nCurrent: v${current}\nLatest: v${latest}\n\nTo upgrade, run:\nsudo /spiralpool/upgrade.sh"

    # Console notification (for manual runs)
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "  ${WHITE}SPIRAL POOL UPDATE AVAILABLE${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "  Current version: ${YELLOW}${current}${NC}"
    echo -e "  Latest version:  ${GREEN}${latest}${NC}"
    echo ""
    echo -e "  To upgrade, run:"
    echo -e "    ${GREEN}sudo /spiralpool/upgrade.sh${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    # Send external notifications
    local discord_msg
    printf -v discord_msg "Current: v%s\nLatest: v%s\n\nRun: sudo /spiralpool/upgrade.sh" "${current}" "${latest}"
    send_discord_notification "$discord_msg"
    local tg_msg
    printf -v tg_msg "<b>Spiral Pool Update Available</b>\n\nCurrent: v%s\nLatest: v%s\n\nRun: <code>sudo /spiralpool/upgrade.sh</code>" "${current}" "${latest}"
    send_telegram_notification "$tg_msg"

    # Update last notified version
    LAST_NOTIFIED_VERSION="$latest"
    save_settings
}

get_node_uuid() {
    # Get unique node identifier for this Spiral Pool instance
    local uuid_file="${INSTALL_DIR}/config/node-uuid"
    if [[ -f "$uuid_file" ]]; then
        cat "$uuid_file"
    else
        # Generate new UUID if none exists
        local new_uuid
        if command -v uuidgen &>/dev/null; then
            new_uuid=$(uuidgen)
        else
            new_uuid=$(cat /proc/sys/kernel/random/uuid 2>/dev/null || echo "node-$(hostname)-$(date +%s)")
        fi
        mkdir -p "${INSTALL_DIR}/config" 2>/dev/null || true
        echo "$new_uuid" > "$uuid_file" 2>/dev/null || true
        echo "$new_uuid"
    fi
}

suppress_sentinel_alerts() {
    # Use unified maintenance mode script
    local duration_minutes="${1:-60}"
    local reason="${2:-Automatic upgrade}"

    if [[ -x "${INSTALL_DIR}/scripts/maintenance-mode.sh" ]]; then
        # || true: maintenance mode failure must not abort an auto-update
        "${INSTALL_DIR}/scripts/maintenance-mode.sh" enable "$duration_minutes" "$reason" > /dev/null 2>&1 || true
        log "INFO" "Maintenance mode enabled for ${duration_minutes} minutes"
    else
        # Fallback: create flag file directly
        local suppress_until=$(($(date +%s) + (duration_minutes * 60)))
        local suppress_file="${INSTALL_DIR}/config/.maintenance-mode"
        mkdir -p "${INSTALL_DIR}/config" 2>/dev/null || true
        # SECURITY: Use jq for safe JSON construction (prevents injection via reason/uuid)
        if command -v jq &>/dev/null; then
            jq -n \
                --argjson start "$(date +%s)" \
                --argjson end "$suppress_until" \
                --argjson dur "$duration_minutes" \
                --arg reason "$reason" \
                --arg uuid "$(get_node_uuid)" \
                '{enabled: true, start_time: $start, end_time: $end, duration_minutes: $dur, reason: $reason, started_by: "update-checker", node_uuid: $uuid}' \
                > "$suppress_file"
        else
            # Minimal safe fallback — sanitize reason to alphanumeric
            local safe_reason
            safe_reason=$(printf '%s' "$reason" | tr -dc 'A-Za-z0-9 _-')
            cat > "$suppress_file" << SUPPRESSEOF
{
    "enabled": true,
    "start_time": $(date +%s),
    "end_time": ${suppress_until},
    "duration_minutes": ${duration_minutes},
    "reason": "${safe_reason}",
    "started_by": "update-checker",
    "node_uuid": "fallback"
}
SUPPRESSEOF
        fi
        log "INFO" "Alert suppression enabled for ${duration_minutes} minutes (fallback)"
    fi
}

clear_alert_suppression() {
    # Use unified maintenance mode script
    if [[ -x "${INSTALL_DIR}/scripts/maintenance-mode.sh" ]]; then
        # || true: clearing maintenance mode must not abort post-upgrade cleanup
        "${INSTALL_DIR}/scripts/maintenance-mode.sh" disable > /dev/null 2>&1 || true
        log "INFO" "Maintenance mode disabled"
    else
        # Fallback: remove flag file directly
        rm -f "${INSTALL_DIR}/config/.maintenance-mode" 2>/dev/null
        log "INFO" "Alert suppression cleared (fallback)"
    fi
}

is_alerts_suppressed() {
    # Use unified maintenance mode script
    if [[ -x "${INSTALL_DIR}/scripts/maintenance-mode.sh" ]]; then
        "${INSTALL_DIR}/scripts/maintenance-mode.sh" check > /dev/null 2>&1
        return $?
    else
        # Fallback: check flag file directly
        local suppress_file="${INSTALL_DIR}/config/.maintenance-mode"
        if [[ -f "$suppress_file" ]]; then
            local suppress_until=$(grep -oP '"end_time":\s*\K[0-9]+' "$suppress_file" 2>/dev/null || echo "0")
            local now=$(date +%s)
            if [[ $now -lt $suppress_until ]]; then
                return 0  # Alerts ARE suppressed
            else
                rm -f "$suppress_file"  # Expired, clean up
            fi
        fi
        return 1  # Alerts are NOT suppressed
    fi
}

perform_auto_update() {
    local current="$1"
    local latest="$2"
    local countdown_minutes=15
    local node_uuid=$(get_node_uuid)
    local node_name
    node_name=$(hostname 2>/dev/null || echo "unknown")

    # HA SAFETY: Only auto-update on the VIP-holding (primary) node.
    # Running upgrade.sh on the backup node while primary is serving miners
    # could restart services and cause split-brain or service conflicts.
    if [[ -f "${INSTALL_DIR}/config/ha-enabled" ]]; then
        local ha_role
        ha_role=$(curl -s --max-time 3 "http://localhost:5354/status" 2>/dev/null | \
            jq -r '.localRole // "UNKNOWN"' 2>/dev/null || echo "UNKNOWN")
        if [[ "$ha_role" != "MASTER" ]] && [[ "$ha_role" != "STANDALONE" ]]; then
            log "INFO" "HA role is ${ha_role} — skipping auto-update (only primary/standalone nodes auto-update)"
            return 0
        fi
    fi

    # SECURITY: Validate version strings are in semver format
    local semver_pattern='^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+)?$'
    if ! [[ "$current" =~ $semver_pattern ]] || ! [[ "$latest" =~ $semver_pattern ]]; then
        log "ERROR" "Invalid version format detected, aborting auto-update"
        log "ERROR" "Current: $current, Latest: $latest"
        return 1
    fi

    log "INFO" "=============================================="
    log "INFO" "AUTOMATIC UPDATE SCHEDULED"
    log "INFO" "Node UUID: ${node_uuid}"
    log "INFO" "Current: ${current} -> Target: ${latest}"
    log "INFO" "Update will begin in ${countdown_minutes} minutes"
    log "INFO" "=============================================="

    # Send countdown notification
    local countdown_msg
    printf -v countdown_msg "AUTOMATIC UPDATE SCHEDULED\n\nCurrent: v%s\nTarget: v%s\n\nUpdate will begin in %s minutes.\nServices will be temporarily stopped.\n\nTo cancel, run: touch %s/config/.cancel-auto-update" "${current}" "${latest}" "${countdown_minutes}" "${INSTALL_DIR}"

    send_discord_notification "$countdown_msg"
    local tg_msg
    printf -v tg_msg "<b>Spiral Pool Auto-Update Scheduled</b>\n\n%s" "$countdown_msg"
    send_telegram_notification "$tg_msg"

    # Create cancellation file check
    local cancel_file="${INSTALL_DIR}/config/.cancel-auto-update"
    rm -f "$cancel_file" 2>/dev/null

    # Countdown with periodic checks for cancellation
    local countdown_seconds=$((countdown_minutes * 60))
    local check_interval=60  # Check every minute

    while [[ $countdown_seconds -gt 0 ]]; do
        # Check for cancellation
        if [[ -f "$cancel_file" ]]; then
            log "INFO" "Auto-update CANCELLED by user"
            rm -f "$cancel_file"

            local cancel_msg
            printf -v cancel_msg "Auto-update CANCELLED by user.\n\nTo upgrade manually: sudo %s/upgrade.sh" "${INSTALL_DIR}"
            send_discord_notification "$cancel_msg"
            local tg_msg
            printf -v tg_msg "<b>Auto-Update Cancelled</b>\n\n%s" "$cancel_msg"
            send_telegram_notification "$tg_msg"

            return 0
        fi

        sleep $check_interval
        countdown_seconds=$((countdown_seconds - check_interval))

        # Log remaining time every 5 minutes
        if [[ $((countdown_seconds % 300)) -eq 0 ]] && [[ $countdown_seconds -gt 0 ]]; then
            log "INFO" "Auto-update in $((countdown_seconds / 60)) minutes..."
        fi
    done

    # Check one last time for cancellation
    if [[ -f "$cancel_file" ]]; then
        log "INFO" "Auto-update CANCELLED by user (last check)"
        rm -f "$cancel_file"
        return 0
    fi

    # Suppress all alerts during upgrade (1 hour)
    suppress_sentinel_alerts 60

    local start_time=$(date +%s)

    # Notify that auto-update is starting NOW
    local start_msg
    printf -v start_msg "AUTOMATIC UPDATE STARTING NOW\n\nFrom: v%s to v%s\n\nServices will be stopped momentarily.\nAlerts are suppressed during upgrade." "${current}" "${latest}"
    send_discord_notification "$start_msg"
    local tg_msg
    printf -v tg_msg "<b>Auto-Update Starting</b>\n\n%s" "$start_msg"
    send_telegram_notification "$tg_msg"

    # Run the upgrade script with --auto flag for unattended operation
    # sudo required: this script runs as POOL_USER (spiraluser) via cron,
    # but upgrade.sh needs root for package installs and service restarts.
    # Sudoers entry created by install.sh in /etc/sudoers.d/spiralpool-dashboard.
    if [[ -x "${INSTALL_DIR}/upgrade.sh" ]]; then
        local upgrade_log="${INSTALL_DIR}/logs/auto-upgrade-$(date +%Y%m%d-%H%M%S).log"

        # Run upgrade and capture output
        if sudo "${INSTALL_DIR}/upgrade.sh" --force --auto >> "$upgrade_log" 2>&1; then
            local end_time=$(date +%s)
            local duration=$((end_time - start_time))
            local duration_min=$((duration / 60))
            local duration_sec=$((duration % 60))

            log "SUCCESS" "Auto-update completed in ${duration_min}m ${duration_sec}s"

            # Clear alert suppression
            clear_alert_suppression

            # Verify services are running
            local services_status=""
            for svc in spiralstratum spiraldash spiralsentinel; do
                if systemctl is-active --quiet "$svc" 2>/dev/null; then
                    services_status="${services_status}${svc}: Running\n"
                else
                    services_status="${services_status}${svc}: NOT RUNNING\n"
                fi
            done

            # Notify about successful update
            local success_msg
            printf -v success_msg "AUTOMATIC UPDATE COMPLETED\n\nFrom: v%s\nTo: v%s\nDuration: %sm %ss\n\nServices Status:\n%s\n\nAlerts have been re-enabled." "${current}" "${latest}" "${duration_min}" "${duration_sec}" "${services_status}"
            send_discord_notification "$success_msg"
            local tg_msg
            printf -v tg_msg "<b>Spiral Pool Updated Successfully</b>\n\n%s" "$success_msg"
            send_telegram_notification "$tg_msg"

            # Update last notified version
            LAST_NOTIFIED_VERSION="$latest"
            save_settings
        else
            log "ERROR" "Auto-update FAILED - check log: ${upgrade_log}"

            # Clear alert suppression so Sentinel can alert about issues
            clear_alert_suppression

            # Notify about failed update
            local fail_msg
            printf -v fail_msg "AUTOMATIC UPDATE FAILED!\n\nFrom: v%s to v%s\n\nPlease check the server and run manually:\nsudo %s/upgrade.sh\n\nLog: %s" "${current}" "${latest}" "${INSTALL_DIR}" "${upgrade_log}"
            send_discord_notification "$fail_msg"
            local tg_msg
            printf -v tg_msg "<b>Spiral Pool Auto-Update FAILED</b>\n\n%s" "$fail_msg"
            send_telegram_notification "$tg_msg"

            return 1
        fi
    else
        log "ERROR" "Upgrade script not found or not executable at ${INSTALL_DIR}/upgrade.sh"

        clear_alert_suppression

        local fail_msg
        printf -v fail_msg "AUTOMATIC UPDATE FAILED!\n\nUpgrade script not found.\nPlease reinstall Spiral Pool."
        send_discord_notification "$fail_msg"
        local tg_msg
        printf -v tg_msg "<b>Spiral Pool Auto-Update FAILED</b>\n\n%s" "$fail_msg"
        send_telegram_notification "$tg_msg"

        return 1
    fi
}

check_for_updates() {
    log "INFO" "Checking for updates..."

    local current_version=$(get_current_version)
    local latest_version=$(get_latest_version)

    if [[ -z "$latest_version" ]]; then
        log "WARN" "Could not fetch latest version from GitHub"
        echo -e "${YELLOW}Could not reach GitHub to check for updates.${NC}"
        echo "Check your internet connection, or upgrade manually with: sudo /spiralpool/upgrade.sh --local"
        return 0
    fi

    log "INFO" "Current: ${current_version}, Latest: ${latest_version}"

    # version_compare returns 0=equal, 1=newer, 2=older — non-zero is NOT an error,
    # but set -e would kill the script. Capture exit code safely.
    local result=0
    version_compare "$latest_version" "$current_version" && result=$? || result=$?

    if [[ $result -eq 1 ]]; then
        # New version available
        log "INFO" "Update available: ${current_version} -> ${latest_version}"

        # Check if we already notified about this version
        if [[ "$LAST_NOTIFIED_VERSION" == "$latest_version" ]] && [[ "$UPDATE_MODE" == "notify" ]]; then
            log "INFO" "Already notified about version ${latest_version}, skipping"
            return 0
        fi

        case "$UPDATE_MODE" in
            auto)
                perform_auto_update "$current_version" "$latest_version"
                ;;
            notify)
                notify_user "$current_version" "$latest_version"
                ;;
            disabled)
                log "INFO" "Updates disabled, skipping notification"
                ;;
        esac
    else
        log "INFO" "No updates available (current: ${current_version})"
        if [[ "$VERBOSE" == "true" ]]; then
            echo -e "${GREEN}You are running the latest version (${current_version})${NC}"
        fi
    fi
}

# =============================================================================
# Commands
# =============================================================================

cmd_check() {
    # HA SAFETY: On HA backup nodes, skip update checks entirely to prevent
    # duplicate Discord/Telegram notifications (both nodes would notify).
    # Only the primary/standalone node should check and notify.
    if [[ -f "${INSTALL_DIR}/config/ha-enabled" ]]; then
        local ha_role
        ha_role=$(curl -s --max-time 3 "http://localhost:5354/status" 2>/dev/null | \
            jq -r '.localRole // "UNKNOWN"' 2>/dev/null || echo "UNKNOWN")
        if [[ "$ha_role" != "MASTER" ]] && [[ "$ha_role" != "STANDALONE" ]] && [[ "$ha_role" != "UNKNOWN" ]]; then
            log "INFO" "HA role is ${ha_role} — skipping update check (only primary checks)"
            return 0
        fi
    fi

    # Acquire lock to prevent concurrent check cycles (e.g., cron overlap)
    # /run/spiralpool is created by tmpfiles.d (owned by POOL_USER).
    # If unavailable, fall back to /tmp lock (less robust but functional).
    # NOTE: Do NOT use "exec 9>file 2>/dev/null" — exec with only redirections
    # applies ALL redirections to the current shell permanently (2>/dev/null
    # would silence stderr for the rest of the script).
    mkdir -p "$(dirname "$LOCK_FILE")" 2>/dev/null || true
    if [[ ! -w "$(dirname "$LOCK_FILE")" ]]; then
        LOCK_FILE="/tmp/spiralpool-update-check.lock"
    fi
    exec 9>"$LOCK_FILE"
    if ! flock -n 9; then
        log "INFO" "Another update check is already running, skipping"
        return 0
    fi
    trap 'flock -u 9 2>/dev/null || true' EXIT

    check_for_updates
}

cmd_status() {
    local current_version=$(get_current_version)

    echo ""
    echo -e "${CYAN}Spiral Pool Update Checker Status${NC}"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo -e "Current Version:    ${GREEN}${current_version}${NC}"

    # Show update mode with description
    case "$UPDATE_MODE" in
        auto)
            echo -e "Update Mode:        ${GREEN}${UPDATE_MODE}${NC} (fully automatic - updates install without asking)"
            ;;
        notify)
            echo -e "Update Mode:        ${YELLOW}${UPDATE_MODE}${NC} (notification only - you decide when to upgrade)"
            ;;
        disabled)
            echo -e "Update Mode:        ${RED}${UPDATE_MODE}${NC} (no update checking)"
            ;;
        *)
            echo -e "Update Mode:        ${YELLOW}${UPDATE_MODE}${NC}"
            ;;
    esac

    echo -e "Check Interval:     ${CHECK_INTERVAL}"
    echo -e "Discord Notify:     ${NOTIFY_DISCORD}"
    echo -e "Telegram Notify:    ${NOTIFY_TELEGRAM}"
    echo ""

    if [[ -n "$SENTINEL_CONFIG" ]] && [[ -f "$SENTINEL_CONFIG" ]]; then
        echo -e "${DIM}Config source: ${SENTINEL_CONFIG}${NC}"
    fi
    echo ""
    echo ""
}

update_sentinel_config() {
    # Update the auto_update_mode in the Sentinel config.json
    if [[ -n "$SENTINEL_CONFIG" ]] && [[ -f "$SENTINEL_CONFIG" ]]; then
        # SECURITY: Set restrictive umask before mktemp to prevent race condition
        local old_umask
        old_umask=$(umask)
        umask 077

        local temp_file
        # Prefer /run/spiralpool (owned by POOL_USER via tmpfiles.d, not world-writable)
        # Fall back to /tmp if /run/spiralpool is unavailable
        temp_file=$(mktemp /run/spiralpool/sentinel-config-XXXXXX 2>/dev/null) || \
        temp_file=$(mktemp /tmp/spiralpool-sentinel-config-XXXXXX) || {
            umask "$old_umask"
            log "ERROR" "Failed to create temp file"
            return 1
        }

        umask "$old_umask"
        chmod 600 "$temp_file"  # Defense in depth

        # SECURITY: Use jq for safe JSON modification if available
        if command -v jq &>/dev/null; then
            local enabled="true"
            [[ "$UPDATE_MODE" == "disabled" ]] && enabled="false"
            if jq --arg mode "$UPDATE_MODE" --argjson enabled "$enabled" \
                '.auto_update_mode = $mode | .update_check_enabled = $enabled' \
                "$SENTINEL_CONFIG" > "$temp_file" 2>/dev/null; then
                # SECURITY: Verify ownership before moving
                local config_owner
                config_owner=$(stat -c '%U' "$SENTINEL_CONFIG" 2>/dev/null || echo "")
                if [[ -n "$config_owner" ]]; then
                    chown "$config_owner:$config_owner" "$temp_file" 2>/dev/null || true
                fi
                mv "$temp_file" "$SENTINEL_CONFIG"
                log "INFO" "Updated Sentinel config with auto_update_mode=${UPDATE_MODE}"
            else
                rm -f "$temp_file"
                log "WARN" "Failed to update Sentinel config with jq"
            fi
        else
            # Fallback to sed
            if sed "s/\"auto_update_mode\": *\"[^\"]*\"/\"auto_update_mode\": \"${UPDATE_MODE}\"/" "$SENTINEL_CONFIG" > "$temp_file" 2>/dev/null; then
                local enabled="true"
                [[ "$UPDATE_MODE" == "disabled" ]] && enabled="false"
                sed -i "s/\"update_check_enabled\": *[a-z]*/\"update_check_enabled\": ${enabled}/" "$temp_file" 2>/dev/null || true
                # SECURITY: Verify ownership before moving
                local config_owner
                config_owner=$(stat -c '%U' "$SENTINEL_CONFIG" 2>/dev/null || echo "")
                if [[ -n "$config_owner" ]]; then
                    chown "$config_owner:$config_owner" "$temp_file" 2>/dev/null || true
                fi
                mv "$temp_file" "$SENTINEL_CONFIG"
                log "INFO" "Updated Sentinel config with auto_update_mode=${UPDATE_MODE}"
            else
                rm -f "$temp_file"
                log "WARN" "Failed to update Sentinel config"
            fi
        fi
    fi
}

cmd_configure() {
    echo ""
    echo -e "${CYAN}Spiral Pool Update Configuration${NC}"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    # Update mode
    echo "How would you like to handle updates?"
    echo ""
    echo -e "  ${GREEN}1)${NC} Auto-update (automatically install updates)"
    echo -e "     ${DIM}Updates will be downloaded and installed automatically${NC}"
    echo -e "     ${DIM}Services will be stopped/restarted during updates${NC}"
    echo ""
    echo -e "  ${YELLOW}2)${NC} Notify only (alert when updates are available)"
    echo -e "     ${DIM}You control when to upgrade manually${NC}"
    echo ""
    echo -e "  ${RED}3)${NC} Disabled (no update checking)"
    echo ""
    read -p "Select option [1-3] (current: ${UPDATE_MODE}): " mode_choice

    case "$mode_choice" in
        1)
            UPDATE_MODE="auto"
            echo ""
            echo -e "${YELLOW}⚠️  AUTO-UPDATE ENABLED${NC}"
            echo -e "${DIM}When a new version is detected:${NC}"
            echo -e "${DIM}  - Services will be gracefully stopped${NC}"
            echo -e "${DIM}  - Update will be downloaded and installed${NC}"
            echo -e "${DIM}  - Services will be automatically restarted${NC}"
            echo -e "${DIM}  - You'll receive a notification of the completed update${NC}"
            ;;
        2) UPDATE_MODE="notify" ;;
        3) UPDATE_MODE="disabled" ;;
        *) echo "Keeping current setting: ${UPDATE_MODE}" ;;
    esac

    if [[ "$UPDATE_MODE" != "disabled" ]]; then
        # Check interval
        echo ""
        echo "How often should we check for updates?"
        echo "  1) Hourly"
        echo "  2) Daily (recommended)"
        echo "  3) Weekly"
        echo ""
        read -p "Select option [1-3] (current: ${CHECK_INTERVAL}): " interval_choice

        case "$interval_choice" in
            1) CHECK_INTERVAL="hourly" ;;
            2) CHECK_INTERVAL="daily" ;;
            3) CHECK_INTERVAL="weekly" ;;
            *) echo "Keeping current setting: ${CHECK_INTERVAL}" ;;
        esac

        # Discord notifications
        echo ""
        read -p "Enable Discord notifications? [y/N]: " discord_choice
        if [[ "$discord_choice" == "y" || "$discord_choice" == "Y" ]]; then
            NOTIFY_DISCORD=true
            read -p "Enter Discord webhook URL: " DISCORD_WEBHOOK
        else
            NOTIFY_DISCORD=false
        fi

        # Telegram notifications
        echo ""
        read -p "Enable Telegram notifications? [y/N]: " telegram_choice
        if [[ "$telegram_choice" == "y" || "$telegram_choice" == "Y" ]]; then
            NOTIFY_TELEGRAM=true
            read -p "Enter Telegram bot token: " TELEGRAM_BOT_TOKEN
            read -p "Enter Telegram chat ID: " TELEGRAM_CHAT_ID
        else
            NOTIFY_TELEGRAM=false
        fi
    fi

    # Save settings to both config files
    save_settings
    update_sentinel_config

    echo ""
    echo -e "${GREEN}Settings saved!${NC}"
    echo ""

    # Setup cron job if not disabled
    if [[ "$UPDATE_MODE" != "disabled" ]]; then
        setup_cron
    else
        remove_cron
    fi
}

setup_cron() {
    # cron may not be installed (Ubuntu minimal/cloud images skip it)
    if ! command -v crontab &>/dev/null; then
        log "WARN" "crontab not available — install 'cron' package to enable scheduled update checks"
        echo "Update checker: crontab not installed (skipping scheduled checks)"
        return 0
    fi

    local cron_schedule

    case "$CHECK_INTERVAL" in
        hourly) cron_schedule="0 * * * *" ;;
        daily) cron_schedule="0 4 * * *" ;;  # 4 AM daily
        weekly) cron_schedule="0 4 * * 0" ;;  # 4 AM Sunday
        *) cron_schedule="0 4 * * *" ;;
    esac

    # Remove existing entry
    crontab -l 2>/dev/null | grep -v "spiralpool.*update-checker" | crontab - 2>/dev/null || true

    # Add new entry
    (crontab -l 2>/dev/null; echo "${cron_schedule} ${INSTALL_DIR}/scripts/update-checker.sh check >> ${LOG_FILE} 2>&1") | crontab - 2>/dev/null || {
        log "WARN" "Failed to install crontab entry"
        echo "Warning: Could not schedule update checker in crontab"
        return 0
    }

    log "INFO" "Cron job configured: ${cron_schedule}"
    echo "Update checker scheduled: ${CHECK_INTERVAL}"
}

remove_cron() {
    command -v crontab &>/dev/null || return 0
    crontab -l 2>/dev/null | grep -v "spiralpool.*update-checker" | crontab - 2>/dev/null || true
    log "INFO" "Cron job removed"
    echo "Update checker disabled"
}

cmd_enable() {
    if [[ "$UPDATE_MODE" == "disabled" ]]; then
        UPDATE_MODE="notify"
        save_settings
    fi
    setup_cron
    echo -e "${GREEN}Update checking enabled${NC}"
}

cmd_disable() {
    UPDATE_MODE="disabled"
    save_settings
    remove_cron
    echo -e "${YELLOW}Update checking disabled${NC}"
}

show_help() {
    echo ""
    echo "Spiral Pool Update Checker"
    echo ""
    echo "Usage: $0 <command> [options]"
    echo ""
    echo "Commands:"
    echo "  check       Check for updates now"
    echo "  status      Show current status and settings"
    echo "  configure   Configure update preferences"
    echo "  enable      Enable update checking"
    echo "  disable     Disable update checking"
    echo "  help        Show this help message"
    echo ""
    echo "Options:"
    echo "  --verbose   Show detailed output"
    echo ""
    echo "Examples:"
    echo "  $0 check              # Check for updates"
    echo "  $0 configure          # Configure settings interactively"
    echo "  $0 status             # Show current configuration"
    echo ""
}

# =============================================================================
# Main
# =============================================================================

main() {
    # Create log directory (suppress errors — log() has its own fallback)
    mkdir -p "$(dirname "${LOG_FILE}")" 2>/dev/null || true

    # Parse options
    VERBOSE=false
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --verbose|-v)
                VERBOSE=true
                shift
                ;;
            check|status|configure|enable|disable|help)
                COMMAND="$1"
                shift
                ;;
            *)
                echo "Unknown option: $1"
                show_help
                exit 1
                ;;
        esac
    done

    # Load settings
    load_settings

    # Execute command
    case "${COMMAND:-check}" in
        check) cmd_check ;;
        status) cmd_status ;;
        configure) cmd_configure ;;
        enable) cmd_enable ;;
        disable) cmd_disable ;;
        help) show_help ;;
        *) show_help ;;
    esac
}

# Run if not sourced
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
