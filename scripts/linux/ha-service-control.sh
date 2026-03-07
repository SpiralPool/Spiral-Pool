#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool HA Service Control
# Manages service state based on HA role (MASTER/BACKUP/OBSERVER)
#
# Usage:
#   ha-service-control.sh promote [--force]  - Start services (only if MASTER or --force)
#   ha-service-control.sh demote [--force]   - Stop services (only if NOT MASTER or --force)
#   ha-service-control.sh status             - Show current service and HA status
#   ha-service-control.sh auto               - Auto-detect role and sync services
#   ha-service-control.sh check              - Silent check (for scripts)
#
# IMPORTANT: This script verifies the node's ACTUAL role from the HA cluster
# before starting/stopping services. This prevents duplicate services running.
#
# If HA is NOT enabled (single-node mode):
#   - 'auto' will start services (single node is always the master)
#   - 'promote' will start services
#   - 'demote' will be rejected (can't demote the only node)
#
# If HA IS enabled:
#   - 'auto' queries the cluster and syncs services to match role
#   - 'promote' verifies this node is MASTER before starting
#   - 'demote' verifies this node is NOT MASTER before stopping
#   - Use --force to override verification (dangerous - can cause duplicates!)
#

# set -e removed — this script uses explicit error handling.
# Using set -e in a service control script would cause silent failures
# when any sub-command returns non-zero (e.g., systemctl on stopped service).

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
LOG_FILE="${INSTALL_DIR}/logs/ha-service-control.log"
STATE_FILE="${INSTALL_DIR}/config/.ha-service-state"
LOCK_FILE="${INSTALL_DIR}/config/.ha-service-control.lock"
HA_STATUS_PORT=5354

# Services managed by HA
HA_MANAGED_SERVICES=(
    "spiralsentinel"
    "spiraldash"
)

# Force flag — set once per invocation by parse_args() from --force/−f.
# Not restored after use; safe because main() runs once per process.
FORCE_MODE=false

get_pool_user() {
    stat -c '%U' "${INSTALL_DIR}" 2>/dev/null || echo "spiraluser"
}

# =============================================================================
# HA Cluster Status Functions
# =============================================================================

# Query the HA status endpoint to get cluster info
# Returns JSON or empty string if HA not available
get_ha_status_json() {
    local url="http://localhost:${HA_STATUS_PORT}/status"
    curl -s --max-time 5 "$url" 2>/dev/null || echo ""
}

# Check if HA mode is enabled
# Returns: 0 if enabled, 1 if disabled/unavailable
is_ha_enabled() {
    local status_json
    status_json=$(get_ha_status_json)

    if [[ -z "$status_json" ]]; then
        return 1  # HA endpoint not available
    fi

    local enabled
    enabled=$(echo "$status_json" | jq -r '.enabled // false' 2>/dev/null || echo "false")

    [[ "$enabled" == "true" ]]
}

# Get the local node's role from the HA cluster
# Returns: MASTER, BACKUP, OBSERVER, or STANDALONE (if HA disabled)
get_cluster_role() {
    local status_json
    status_json=$(get_ha_status_json)

    if [[ -z "$status_json" ]]; then
        echo "STANDALONE"
        return
    fi

    local enabled
    enabled=$(echo "$status_json" | jq -r '.enabled // false' 2>/dev/null || echo "false")

    if [[ "$enabled" != "true" ]]; then
        echo "STANDALONE"
        return
    fi

    local role
    role=$(echo "$status_json" | jq -r '.localRole // "UNKNOWN"' 2>/dev/null || echo "UNKNOWN")
    echo "$role"
}

# Verify this node should be running services
# Returns: 0 if should run (MASTER or STANDALONE), 1 if should NOT run
should_run_services() {
    local role
    role=$(get_cluster_role)

    case "$role" in
        MASTER|STANDALONE)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

# =============================================================================
# File Locking Functions (prevents race conditions)
# =============================================================================

# Use flock for race-free file locking (replaces noclobber approach)
LOCK_FD=9
_LOCK_HELD=false

acquire_lock() {
    # Re-entrant: if this process already holds the lock, skip re-acquisition.
    # Without this, auto_sync → promote/demote would close fd 9 (releasing the
    # lock) then re-open it, creating a tiny race window for another process.
    if [[ "$_LOCK_HELD" == "true" ]]; then
        return 0
    fi
    local timeout="${1:-10}"
    mkdir -p "$(dirname "$LOCK_FILE")"
    exec 9>"$LOCK_FILE"
    if ! flock -w "$timeout" "$LOCK_FD"; then
        log "ERROR" "Could not acquire lock after ${timeout} seconds"
        return 1
    fi
    _LOCK_HELD=true
}

release_lock() {
    flock -u "$LOCK_FD" 2>/dev/null || true
    _LOCK_HELD=false
}

# =============================================================================
# Helper Functions
# =============================================================================

log() {
    local level="$1"
    local message="$2"
    local timestamp
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')

    mkdir -p "$(dirname "${LOG_FILE}")"

    if [[ ! -f "${LOG_FILE}" ]]; then
        touch "${LOG_FILE}"
        chmod 600 "${LOG_FILE}"
    fi

    echo "[${timestamp}] [${level}] ${message}" >> "${LOG_FILE}"

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
        local uuid_content
        uuid_content=$(head -c 64 "$uuid_file" 2>/dev/null | tr -cd 'a-zA-Z0-9-')
        if [[ -n "$uuid_content" ]]; then
            echo "$uuid_content"
            return
        fi
    fi
    # Generate and persist a new UUID if file is missing or empty
    local new_uuid
    if command -v uuidgen &>/dev/null; then
        new_uuid=$(uuidgen)
    else
        new_uuid=$(cat /proc/sys/kernel/random/uuid 2>/dev/null || echo "node-$(hostname)-$(date +%s)")
    fi
    mkdir -p "${INSTALL_DIR}/config" 2>/dev/null || true
    echo "$new_uuid" > "$uuid_file" 2>/dev/null || true
    echo "$new_uuid"
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
    local color="${2:-3447003}"

    if [[ -z "$DISCORD_WEBHOOK" ]] || [[ "$DISCORD_WEBHOOK" == "null" ]]; then
        return
    fi

    if ! [[ "$DISCORD_WEBHOOK" =~ ^https://discord(app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]+$ ]]; then
        log "ERROR" "Invalid Discord webhook URL format - skipping notification"
        return 1
    fi

    local payload
    local node_id
    node_id=$(get_node_uuid | cut -c1-8)

    if command -v jq &>/dev/null; then
        # jq --arg handles newline escaping for JSON automatically
        payload=$(jq -n \
            --arg title "Spiral Pool HA Event" \
            --arg desc "$message" \
            --argjson color "$color" \
            --arg footer "Node: ${node_id}..." \
            '{embeds: [{title: $title, description: $desc, color: $color, footer: {text: $footer}}]}')
    else
        # Fallback: escape backslashes and quotes first (per line), then replace newlines with \n for JSON
        local escaped_message
        escaped_message=$(printf '%s' "$message" | sed 's/\\/\\\\/g; s/"/\\"/g')
        escaped_message="${escaped_message//$'\n'/\\n}"
        payload="{\"embeds\": [{\"title\": \"Spiral Pool HA Event\", \"description\": \"${escaped_message}\", \"color\": ${color}, \"footer\": {\"text\": \"Node: ${node_id}...\"}}]}"
    fi

    curl -s --max-time 10 -H "Content-Type: application/json" --data-raw "${payload}" "${DISCORD_WEBHOOK}" > /dev/null 2>&1 || true
}

send_telegram_notification() {
    local message="$1"

    if [[ "$TELEGRAM_ENABLED" != "true" ]] || [[ -z "$TELEGRAM_BOT_TOKEN" ]] || [[ -z "$TELEGRAM_CHAT_ID" ]]; then
        return
    fi

    if ! [[ "$TELEGRAM_BOT_TOKEN" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]]; then
        log "ERROR" "Invalid Telegram bot token format - skipping notification"
        return 1
    fi

    if ! [[ "$TELEGRAM_CHAT_ID" =~ ^-?[0-9]+$ ]]; then
        log "ERROR" "Invalid Telegram chat ID format - skipping notification"
        return 1
    fi

    local url="https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage"
    local node_id
    node_id=$(get_node_uuid | cut -c1-8)

    # Append node footer with actual newlines
    local full_text="${message}"$'\n\n'"Node: ${node_id}..."

    if command -v jq &>/dev/null; then
        # jq --arg handles newline escaping for JSON automatically
        local payload
        payload=$(jq -n \
            --arg chat_id "$TELEGRAM_CHAT_ID" \
            --arg text "$full_text" \
            '{chat_id: $chat_id, text: $text, parse_mode: "HTML"}')
        curl -s --max-time 10 -H "Content-Type: application/json" --data-raw "${payload}" "${url}" > /dev/null 2>&1 || true
    else
        # Fallback: --data-urlencode handles newlines via URL encoding
        curl -s --max-time 10 -X POST "$url" \
            --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
            --data-urlencode "text=${full_text}" \
            --data-urlencode "parse_mode=HTML" > /dev/null 2>&1 || true
    fi
}

# =============================================================================
# Dashboard Data Sync (HA Failover)
# =============================================================================

DASHBOARD_DATA_DIR="${INSTALL_DIR}/dashboard/data"
HA_YAML="${INSTALL_DIR}/config/ha.yaml"

# Discover peer node IP address
# Tries: ha.yaml peers → etcdctl member list → patroni.yml etcd hosts
get_peer_ip() {
    local local_ip
    local_ip=$(grep '^\s*nodeIp:' "$HA_YAML" 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    if [[ -z "$local_ip" ]]; then
        local_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    fi

    # Method 1: ha.yaml peers section
    local peer
    peer=$(grep '^\s*-\s*host:' "$HA_YAML" 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    if [[ -n "$peer" ]] && [[ "$peer" != "$local_ip" ]]; then
        echo "$peer"
        return 0
    fi

    # Method 2: etcdctl member list (filter out our own IP)
    if command -v etcdctl &>/dev/null; then
        local members
        members=$(etcdctl member list 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' || true)
        for ip in $members; do
            if [[ "$ip" != "$local_ip" ]] && [[ "$ip" != "127.0.0.1" ]]; then
                echo "$ip"
                return 0
            fi
        done
    fi

    # Method 3: patroni.yml etcd3 hosts
    local patroni_hosts
    patroni_hosts=$(grep -A1 'etcd3:' /etc/patroni/patroni.yml 2>/dev/null | grep 'hosts:' | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' || true)
    for ip in $patroni_hosts; do
        if [[ "$ip" != "$local_ip" ]] && [[ "$ip" != "127.0.0.1" ]]; then
            echo "$ip"
            return 0
        fi
    done

    return 1
}

# Sync dashboard data from peer node before starting spiraldash
# Non-blocking: logs warning and continues if peer is unreachable
sync_dashboard_data() {
    local pool_user
    pool_user=$(get_pool_user)

    local peer_ip
    peer_ip=$(get_peer_ip) || true

    if [[ -z "$peer_ip" ]]; then
        log "WARN" "Dashboard sync: no peer IP found — skipping"
        return 0
    fi

    log "INFO" "Dashboard sync: pulling data from peer ${peer_ip}..."
    echo -e "Syncing dashboard data from peer ${CYAN}${peer_ip}${NC}..."

    # Ensure local dashboard data dir exists with correct ownership
    mkdir -p "$DASHBOARD_DATA_DIR"
    chown "${pool_user}:${pool_user}" "$DASHBOARD_DATA_DIR"

    # rsync as pool_user (SSH keys set up during HA install)
    # --timeout=10: don't block promotion if peer is slow/unreachable
    # -az: archive mode + compression
    local rsync_output
    if rsync_output=$(sudo -u "$pool_user" rsync -az --timeout=10 \
        -e "ssh -o ConnectTimeout=5 -o BatchMode=yes -o StrictHostKeyChecking=accept-new" \
        "${pool_user}@${peer_ip}:${DASHBOARD_DATA_DIR}/" \
        "${DASHBOARD_DATA_DIR}/" 2>&1); then
        # Fix ownership after sync
        chown -R "${pool_user}:${pool_user}" "$DASHBOARD_DATA_DIR"
        log "SUCCESS" "Dashboard data synced from peer ${peer_ip}"
        echo -e "  ${GREEN}Dashboard data synced successfully${NC}"
    else
        log "WARN" "Dashboard sync failed (peer may be down): ${rsync_output}"
        echo -e "  ${YELLOW}Dashboard sync failed (peer may be down) — using local data${NC}"
    fi

    return 0
}

# =============================================================================
# Service Control Functions
# =============================================================================

# Check if a service is active
is_service_active() {
    local service="$1"
    systemctl is-active --quiet "${service}" 2>/dev/null
}

# Check if a service exists
service_exists() {
    local service="$1"
    systemctl list-unit-files --no-pager "${service}.service" 2>/dev/null | grep -q "${service}"
}

# Start a service
start_service() {
    local service="$1"

    if ! service_exists "$service"; then
        log "WARN" "Service ${service} does not exist, skipping"
        return 0
    fi

    if is_service_active "$service"; then
        log "INFO" "Service ${service} is already running"
        return 0
    fi

    log "INFO" "Starting service: ${service}"
    if sudo systemctl start "${service}" 2>&1; then
        log "SUCCESS" "Service ${service} started"
        return 0
    else
        log "ERROR" "Failed to start service ${service}"
        return 1
    fi
}

# Stop a service
stop_service() {
    local service="$1"

    if ! service_exists "$service"; then
        log "WARN" "Service ${service} does not exist, skipping"
        return 0
    fi

    if ! is_service_active "$service"; then
        log "INFO" "Service ${service} is already stopped"
        return 0
    fi

    log "INFO" "Stopping service: ${service}"
    if sudo systemctl stop "${service}" 2>&1; then
        log "SUCCESS" "Service ${service} stopped"
        return 0
    else
        log "ERROR" "Failed to stop service ${service}"
        return 1
    fi
}

# =============================================================================
# HA Role Functions
# =============================================================================

# Save current HA state (atomic write via temp + rename)
save_state() {
    local role="$1"
    # Validate role before writing state
    case "$role" in
        MASTER|BACKUP|STANDALONE|OBSERVER|UNKNOWN) ;;
        *) log "ERROR" "Invalid role for save_state: ${role}"; return 1 ;;
    esac
    local timestamp=$(date +%s)

    mkdir -p "$(dirname "${STATE_FILE}")"

    local tmp
    tmp=$(mktemp "${STATE_FILE}.XXXXXX")
    cat > "$tmp" << EOF
{
    "role": "${role}",
    "timestamp": ${timestamp},
    "node_uuid": "$(get_node_uuid)"
}
EOF
    chmod 600 "$tmp"
    mv "$tmp" "${STATE_FILE}"
}

# Get current saved role
get_saved_role() {
    if [[ -f "${STATE_FILE}" ]]; then
        if command -v jq &>/dev/null; then
            jq -r '.role // "UNKNOWN"' "${STATE_FILE}" 2>/dev/null || echo "UNKNOWN"
        else
            grep -oP '"role":\s*"\K[^"]+' "${STATE_FILE}" 2>/dev/null || echo "UNKNOWN"
        fi
    else
        echo "UNKNOWN"
    fi
}

# Promote: Node became MASTER - start services
# Verifies this node is actually MASTER (or STANDALONE) before starting
promote() {
    local reason="${1:-HA failover}"

    if ! acquire_lock 10; then
        echo -e "${RED}Another HA operation is in progress${NC}"
        return 1
    fi
    trap 'release_lock' EXIT

    # Verify this node should actually be running services
    local cluster_role
    cluster_role=$(get_cluster_role)

    if [[ "$FORCE_MODE" != "true" ]]; then
        case "$cluster_role" in
            MASTER|STANDALONE)
                # OK to promote
                ;;
            BACKUP|OBSERVER)
                echo -e "${RED}${BOLD}PROMOTE REJECTED${NC}"
                echo ""
                echo -e "This node's cluster role is: ${YELLOW}${cluster_role}${NC}"
                echo -e "Cannot start services on a ${cluster_role} node - would cause duplicates!"
                echo ""
                echo -e "${DIM}If you need to force this (dangerous), use: --force${NC}"
                echo -e "${DIM}The actual MASTER node should be running these services.${NC}"
                log "WARN" "Promote rejected: node is ${cluster_role}, not MASTER"
                return 1
                ;;
            *)
                echo -e "${YELLOW}Warning: Could not determine cluster role (${cluster_role})${NC}"
                echo -e "${DIM}Proceeding with promote...${NC}"
                ;;
        esac
    else
        echo -e "${YELLOW}${BOLD}WARNING: --force mode enabled${NC}"
        echo -e "${DIM}Skipping cluster role verification. This may cause duplicate services!${NC}"
        log "WARN" "Force mode: promoting despite cluster role being ${cluster_role}"
    fi

    log "INFO" "=== HA PROMOTE: Node becoming MASTER ==="
    log "INFO" "Reason: ${reason}"
    log "INFO" "Cluster role: ${cluster_role}"

    local old_role
    old_role=$(get_saved_role)

    echo ""
    echo -e "${GREEN}${BOLD}HA PROMOTE: Becoming MASTER${NC}"
    echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "Cluster role:  ${cluster_role}"
    echo -e "Previous role: ${old_role}"
    echo -e "New role:      ${GREEN}MASTER${NC}"
    echo ""

    # Sync dashboard data from peer before starting services
    # This ensures the dashboard has auth/config data after failover
    if is_ha_enabled; then
        sync_dashboard_data
    fi

    local failed=0
    for service in "${HA_MANAGED_SERVICES[@]}"; do
        echo -e "Starting ${CYAN}${service}${NC}..."
        if ! start_service "$service"; then
            failed=$((failed + 1))
        fi
    done

    if [[ $failed -eq 0 ]]; then
        save_state "MASTER"
        log "SUCCESS" "All services started successfully"
        echo ""
        echo -e "${GREEN}All services started successfully${NC}"

        # Send notifications (delayed so Sentinel's startup message appears first in Discord)
        load_notification_settings
        local msg
        printf -v msg 'HA PROMOTE: Node is now MASTER\n\nServices started:\n- Sentinel\n- Dashboard\n\nReason: %s' "${reason}"
        local tg_msg
        printf -v tg_msg '<b>HA PROMOTE</b>\n\nNode is now <b>MASTER</b>\n\nServices started:\n- Sentinel\n- Dashboard\n\nReason: %s' "${reason}"
        (sleep 120 && send_discord_notification "$msg" 3066993) &
        (sleep 120 && send_telegram_notification "$tg_msg") &
    else
        log "ERROR" "Some services failed to start"
        echo ""
        echo -e "${YELLOW}Some services failed to start (${failed} failures)${NC}"
    fi

    echo ""
    return $failed
}

# Demote: Node became BACKUP/OBSERVER - stop services
# Verifies this node is NOT the MASTER before stopping (safety check)
demote() {
    local new_role="${1:-BACKUP}"
    local reason="${2:-HA failover}"

    if ! acquire_lock 10; then
        echo -e "${RED}Another HA operation is in progress${NC}"
        return 1
    fi
    trap 'release_lock' EXIT

    # Verify this node should NOT be running services
    local cluster_role
    cluster_role=$(get_cluster_role)

    if [[ "$FORCE_MODE" != "true" ]]; then
        case "$cluster_role" in
            STANDALONE)
                echo -e "${RED}${BOLD}DEMOTE REJECTED${NC}"
                echo ""
                echo -e "HA is not enabled - this is a ${YELLOW}STANDALONE${NC} node."
                echo -e "Cannot demote the only node - services must run somewhere!"
                echo ""
                echo -e "${DIM}If you need to stop services anyway, use: --force${NC}"
                log "WARN" "Demote rejected: STANDALONE mode, cannot demote"
                return 1
                ;;
            MASTER)
                echo -e "${RED}${BOLD}DEMOTE REJECTED${NC}"
                echo ""
                echo -e "This node's cluster role is: ${GREEN}${cluster_role}${NC}"
                echo -e "Cannot stop services on the MASTER node - it's supposed to run them!"
                echo ""
                echo -e "${DIM}If this node should no longer be MASTER:${NC}"
                echo -e "${DIM}  1. Trigger a failover: spiralctl vip failover${NC}"
                echo -e "${DIM}  2. Or use --force (dangerous)${NC}"
                log "WARN" "Demote rejected: node is MASTER, cannot demote"
                return 1
                ;;
            BACKUP|OBSERVER)
                # OK to demote (should already be demoted, but safe to run)
                ;;
            *)
                echo -e "${YELLOW}Warning: Could not determine cluster role (${cluster_role})${NC}"
                echo -e "${DIM}Proceeding with demote...${NC}"
                ;;
        esac
    else
        echo -e "${YELLOW}${BOLD}WARNING: --force mode enabled${NC}"
        echo -e "${DIM}Skipping cluster role verification. Services will be stopped!${NC}"
        log "WARN" "Force mode: demoting despite cluster role being ${cluster_role}"
    fi

    log "INFO" "=== HA DEMOTE: Node becoming ${new_role} ==="
    log "INFO" "Reason: ${reason}"
    log "INFO" "Cluster role: ${cluster_role}"

    local old_role
    old_role=$(get_saved_role)

    echo ""
    echo -e "${YELLOW}${BOLD}HA DEMOTE: Becoming ${new_role}${NC}"
    echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "Cluster role:  ${cluster_role}"
    echo -e "Previous role: ${old_role}"
    echo -e "New role:      ${YELLOW}${new_role}${NC}"
    echo ""

    local failed=0
    for service in "${HA_MANAGED_SERVICES[@]}"; do
        echo -e "Stopping ${CYAN}${service}${NC}..."
        if ! stop_service "$service"; then
            failed=$((failed + 1))
        fi
    done

    if [[ $failed -eq 0 ]]; then
        save_state "$new_role"
        log "SUCCESS" "All services stopped successfully"
        echo ""
        echo -e "${GREEN}All services stopped successfully${NC}"

        # Send notifications (backgrounded to avoid blocking service control)
        load_notification_settings
        local msg
        printf -v msg 'HA DEMOTE: Node is now %s\n\nServices stopped:\n- Sentinel\n- Dashboard\n\nReason: %s' "${new_role}" "${reason}"
        local tg_msg
        printf -v tg_msg '<b>HA DEMOTE</b>\n\nNode is now <b>%s</b>\n\nServices stopped:\n- Sentinel\n- Dashboard\n\nReason: %s' "${new_role}" "${reason}"
        (sleep 120 && send_discord_notification "$msg" 16776960) &
        (sleep 120 && send_telegram_notification "$tg_msg") &
    else
        log "ERROR" "Some services failed to stop"
        echo ""
        echo -e "${YELLOW}Some services failed to stop (${failed} failures)${NC}"
    fi

    echo ""
    return $failed
}

# Show current status
show_status() {
    echo ""
    echo -e "${CYAN}${BOLD}HA Service Control Status${NC}"
    echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    # Get cluster role from HA endpoint
    local cluster_role
    cluster_role=$(get_cluster_role)

    local saved_role
    saved_role=$(get_saved_role)

    # Show HA status
    if is_ha_enabled; then
        echo -e "HA Mode:     ${GREEN}ENABLED${NC}"
        echo -e "Cluster Role: ${BOLD}${cluster_role}${NC}"
    else
        echo -e "HA Mode:     ${DIM}DISABLED (standalone)${NC}"
        echo -e "Cluster Role: ${BOLD}STANDALONE${NC}"
    fi
    echo -e "Saved Role:  ${saved_role}"
    echo ""
    echo -e "${BOLD}Managed Services:${NC}"

    for service in "${HA_MANAGED_SERVICES[@]}"; do
        if is_service_active "$service"; then
            echo -e "  ${GREEN}●${NC} ${service}: ${GREEN}running${NC}"
        elif service_exists "$service"; then
            echo -e "  ${RED}●${NC} ${service}: ${RED}stopped${NC}"
        else
            echo -e "  ${DIM}○${NC} ${service}: ${DIM}not installed${NC}"
        fi
    done

    echo ""

    # Show expected state based on cluster role
    case "$cluster_role" in
        MASTER|STANDALONE)
            echo -e "${DIM}Expected: Services should be ${GREEN}running${NC}${DIM} (${cluster_role})${NC}"
            ;;
        BACKUP|OBSERVER)
            echo -e "${DIM}Expected: Services should be ${RED}stopped${NC}${DIM} (${cluster_role})${NC}"
            ;;
        *)
            echo -e "${DIM}Cluster role unknown - run 'auto' to detect and sync${NC}"
            ;;
    esac
    echo ""
}

# Silent check for scripts
check_status() {
    local saved_role
    saved_role=$(get_saved_role)

    if [[ "$saved_role" == "MASTER" ]]; then
        # MASTER - all services should be running
        for service in "${HA_MANAGED_SERVICES[@]}"; do
            if ! is_service_active "$service"; then
                return 1  # Service not running when it should be
            fi
        done
        return 0  # All good
    else
        # Not MASTER - services should be stopped
        for service in "${HA_MANAGED_SERVICES[@]}"; do
            if is_service_active "$service"; then
                return 1  # Service running when it shouldn't be
            fi
        done
        return 0  # All good
    fi
}

# Sync services with saved role (legacy - use auto_sync instead)
sync_services() {
    local saved_role
    saved_role=$(get_saved_role)

    log "INFO" "Syncing services with saved role: ${saved_role}"

    case "$saved_role" in
        MASTER)
            for service in "${HA_MANAGED_SERVICES[@]}"; do
                start_service "$service"
            done
            ;;
        BACKUP|OBSERVER)
            for service in "${HA_MANAGED_SERVICES[@]}"; do
                stop_service "$service"
            done
            ;;
        *)
            log "WARN" "Unknown saved role: ${saved_role}, skipping sync"
            ;;
    esac
}

# Auto-detect cluster role and sync services to match
# This is the SAFE way to ensure services match the actual HA state
auto_sync() {
    local reason="${1:-Auto-sync with cluster}"

    # Acquire lock BEFORE reading role to prevent TOCTOU race
    if ! acquire_lock 10; then
        echo -e "${RED}Another HA operation is in progress${NC}"
        return 1
    fi
    trap 'release_lock' EXIT

    echo ""
    echo -e "${CYAN}${BOLD}Auto-detecting HA role and syncing services...${NC}"
    echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    local cluster_role
    cluster_role=$(get_cluster_role)

    echo -e "Detected cluster role: ${BOLD}${cluster_role}${NC}"
    echo ""

    case "$cluster_role" in
        MASTER|STANDALONE)
            log "INFO" "Auto-sync: Role is ${cluster_role}, ensuring services are running"
            echo -e "Action: ${GREEN}Starting services${NC} (this node should run them)"
            echo ""

            # Use FORCE_MODE to skip the redundant check in promote
            # (we just verified the role above)
            local old_force="$FORCE_MODE"
            FORCE_MODE=true
            promote "$reason"
            local result=$?
            FORCE_MODE="$old_force"
            return $result
            ;;
        BACKUP|OBSERVER)
            log "INFO" "Auto-sync: Role is ${cluster_role}, ensuring services are stopped"
            echo -e "Action: ${YELLOW}Stopping services${NC} (another node is MASTER)"
            echo ""

            local old_force="$FORCE_MODE"
            FORCE_MODE=true
            demote "$cluster_role" "$reason"
            local result=$?
            FORCE_MODE="$old_force"
            return $result
            ;;
        *)
            log "WARN" "Auto-sync: Unknown cluster role: ${cluster_role}"
            echo -e "${YELLOW}Could not determine cluster role.${NC}"
            echo -e "${DIM}HA endpoint may not be responding. Check 'spiralctl ha status'.${NC}"
            echo ""
            return 1
            ;;
    esac
}

show_help() {
    echo ""
    echo -e "${CYAN}${BOLD}Spiral Pool HA Service Control${NC}"
    echo ""
    echo "Usage: $0 <command> [options]"
    echo ""
    echo "Commands:"
    echo "  auto [reason]              Auto-detect role and sync services (RECOMMENDED)"
    echo "  promote [reason]           Start services (verifies node is MASTER first)"
    echo "  demote [role] [reason]     Stop services (verifies node is NOT MASTER first)"
    echo "  status                     Show current service and HA status"
    echo "  check                      Silent check (exit 0 if consistent, 1 if not)"
    echo "  sync                       Sync services with saved role (legacy)"
    echo "  help                       Show this help message"
    echo ""
    echo "Options:"
    echo "  --force                    Skip cluster role verification (DANGEROUS)"
    echo ""
    echo "Examples:"
    echo "  $0 auto                          # Detect role and sync (safest)"
    echo "  $0 status                        # Show current status"
    echo "  $0 promote                       # Start services (if MASTER)"
    echo "  $0 demote                        # Stop services (if BACKUP)"
    echo "  $0 promote --force               # Force start (skip verification)"
    echo ""
    echo "Managed services:"
    for service in "${HA_MANAGED_SERVICES[@]}"; do
        echo "  - ${service}"
    done
    echo ""
    echo -e "${BOLD}How it works:${NC}"
    echo "  - If HA is DISABLED: Node is STANDALONE, services always run"
    echo "  - If HA is ENABLED:  Only MASTER node runs services"
    echo ""
    echo -e "${BOLD}Safety:${NC}"
    echo "  promote/demote verify the cluster role before acting to prevent"
    echo "  duplicate services running. Use 'auto' for fully automatic handling."
    echo ""
}

# =============================================================================
# Main
# =============================================================================

# Parse global options (--force) from any position
# Uses global FILTERED_ARGS array instead of echo to preserve quoting
FILTERED_ARGS=()
parse_args() {
    FILTERED_ARGS=()
    for arg in "$@"; do
        case "$arg" in
            --force|-f)
                FORCE_MODE=true
                ;;
            *)
                FILTERED_ARGS+=("$arg")
                ;;
        esac
    done
}

main() {
    # Parse out --force flag from anywhere in args
    parse_args "$@"
    set -- "${FILTERED_ARGS[@]}"

    local command="${1:-status}"
    shift || true

    case "$command" in
        auto|auto-sync|detect)
            local reason="${1:-Auto-sync with cluster}"
            auto_sync "$reason"
            ;;
        promote|master|primary)
            local reason="${1:-HA failover}"
            promote "$reason"
            ;;
        demote|backup|secondary|observer)
            local role="${1:-BACKUP}"
            local reason="${2:-HA failover}"
            # If first arg doesn't look like a role, treat it as reason
            if [[ "$role" != "BACKUP" ]] && [[ "$role" != "OBSERVER" ]] && [[ "$role" != "SECONDARY" ]]; then
                reason="$role"
                role="BACKUP"
            fi
            demote "$role" "$reason"
            ;;
        status|show)
            show_status
            ;;
        check)
            check_status
            ;;
        sync)
            sync_services
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
