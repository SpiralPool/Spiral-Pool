#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool HA Role Watcher
# Lightweight service that monitors HA cluster role and triggers service control
#
# This script runs on ALL nodes (primary and backup) and:
#   1. Polls the HA status API every few seconds
#   2. Detects when the local node's role changes
#   3. Calls spiralpool-ha-service to start/stop Sentinel & Dashboard
#
# This is necessary because:
#   - Sentinel won't be running on backup nodes (it's HA-managed)
#   - So we need an independent watcher to detect when backup becomes primary
#   - And start the services when that happens
#
# This service is NOT HA-managed - it always runs on all nodes.
#

# set -e is intentionally NOT used in this long-lived daemon.
# Each error is handled explicitly by the functions.
# Using set -e would kill the daemon on transient errors (e.g., grep in a pipeline).

# Configuration
INSTALL_DIR="/spiralpool"
LOG_FILE="${INSTALL_DIR}/logs/ha-role-watcher.log"
STATE_FILE="${INSTALL_DIR}/config/.ha-watcher-state"
HA_STATUS_PORT=5354
POLL_INTERVAL=5  # seconds between checks
SERVICE_CONTROL_SCRIPT="/usr/local/bin/spiralpool-ha-service"
FORCE_BOOTSTRAP_MAX_ATTEMPTS=3
FORCE_BOOTSTRAP_ATTEMPTS=0
FAILBACK_COOLDOWN=300  # Minimum 5 minutes between failback attempts
FAILBACK_BOOT_DELAY=120  # Wait 2 minutes after boot before attempting failback

# Verify required tools
if ! command -v jq &>/dev/null; then
    echo "[ERROR] jq is required but not installed. Install with: sudo apt install jq" >&2
    exit 1
fi

# Colors for console (when run manually)
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# =============================================================================
# Logging
# =============================================================================

log() {
    local level="$1"
    local message="$2"
    local timestamp
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')

    mkdir -p "$(dirname "${LOG_FILE}")"

    # Append to log file
    echo "[${timestamp}] [${level}] ${message}" >> "${LOG_FILE}"

    # Also output to stdout (captured by journald when running under systemd)
    if [[ -t 1 ]]; then
        # Interactive terminal — use colors
        case "$level" in
            INFO) echo -e "${BLUE}[INFO]${NC} ${message}" ;;
            WARN) echo -e "${YELLOW}[WARN]${NC} ${message}" ;;
            ERROR) echo -e "${RED}[ERROR]${NC} ${message}" ;;
            SUCCESS) echo -e "${GREEN}[OK]${NC} ${message}" ;;
        esac
    else
        # Non-interactive (systemd) — plain text for journald
        echo "[${level}] ${message}"
    fi
}

# =============================================================================
# HA Status Functions
# =============================================================================

# Get current role from HA cluster API
get_cluster_role() {
    local status_json
    status_json=$(curl -s --max-time 3 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null || echo "")

    if [[ -z "$status_json" ]]; then
        echo "UNAVAILABLE"
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

# Get saved role from state file
get_saved_role() {
    if [[ -f "${STATE_FILE}" ]]; then
        cat "${STATE_FILE}" 2>/dev/null || echo "UNKNOWN"
    else
        echo "UNKNOWN"
    fi
}

# Save role to state file (atomic write via temp + rename)
save_role() {
    local role="$1"
    mkdir -p "$(dirname "${STATE_FILE}")"
    local tmp="${STATE_FILE}.tmp.$$"
    echo "$role" > "$tmp"
    chmod 600 "$tmp"
    mv "$tmp" "${STATE_FILE}"
}

# =============================================================================
# Service Control
# =============================================================================

trigger_service_control() {
    local old_role="$1"
    local new_role="$2"

    log "INFO" "Role change detected: ${old_role} -> ${new_role}"

    if [[ ! -x "${SERVICE_CONTROL_SCRIPT}" ]]; then
        # Try alternate locations
        for alt in "/spiralpool/scripts/ha-service-control.sh" "/opt/spiralpool/scripts/ha-service-control.sh"; do
            if [[ -x "$alt" ]]; then
                SERVICE_CONTROL_SCRIPT="$alt"
                break
            fi
        done
    fi

    if [[ ! -x "${SERVICE_CONTROL_SCRIPT}" ]]; then
        log "ERROR" "Service control script not found: ${SERVICE_CONTROL_SCRIPT}"
        return 1
    fi

    # Call the auto command - it will detect role and sync services
    log "INFO" "Calling service control: auto"
    if "${SERVICE_CONTROL_SCRIPT}" auto "HA Watcher: role changed ${old_role} -> ${new_role}" >> "${LOG_FILE}" 2>&1; then
        log "SUCCESS" "Service control completed successfully"
        return 0
    else
        log "ERROR" "Service control failed"
        return 1
    fi
}

# =============================================================================
# etcd Quorum Recovery (cluster failover)
# =============================================================================
#
# Problem: Losing enough nodes kills etcd quorum (majority required).
# Without quorum, etcd goes read-only -> Patroni can't hold leader lock ->
# PostgreSQL stays/returns to read-only -> stratum crashes -> miners disconnect.
#
# Solution: When we detect VIP is local + etcd has no quorum + ALL peers dead,
# force etcd into single-node mode so Patroni can promote and mining resumes.
# For 2-node: 1 peer dead = all dead → triggers. For 3-node: both peers must
# be dead (if only 1 is down, etcd should still have 2/3 quorum).
#

# Check if the VIP is assigned to this node
# Uses the 'spiralpool-vip' interface label set by keepalived (no root needed)
has_local_vip() {
    ip addr show 2>/dev/null | grep -q "spiralpool-vip"
}

# Check if etcd can commit proposals (has quorum)
is_etcd_healthy() {
    ETCDCTL_API=3 etcdctl endpoint health \
        --endpoints=http://127.0.0.1:2379 \
        --command-timeout=3s 2>&1 | grep -q "is healthy"
}

# Get ALL non-local peer IPs (newline-separated).
# Tries etcd config first (INITIAL_CLUSTER has all peers), falls back to stratum API.
# For 2-node: returns 1 IP. For N-node: returns N-1 IPs.
get_all_peer_ips() {
    local found_any=0

    # Method 1: etcd config (all peers listed in INITIAL_CLUSTER)
    if [[ -f /etc/default/etcd ]]; then
        local local_ip
        local_ip=$(grep 'ETCD_LISTEN_PEER_URLS' /etc/default/etcd 2>/dev/null | grep -oP 'http://\K[0-9.]+' | head -1)
        if [[ -n "$local_ip" ]]; then
            local peers_from_etcd
            peers_from_etcd=$(grep 'ETCD_INITIAL_CLUSTER=' /etc/default/etcd 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | grep -v "^${local_ip}$")
            if [[ -n "$peers_from_etcd" ]]; then
                echo "$peers_from_etcd"
                found_any=1
            fi
        fi
    fi

    # Method 2: Stratum HA API (peers visible via UDP discovery)
    # Adds any peers not already found in etcd config
    local my_ip
    my_ip=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | grep -v '^127\.' | head -1)
    local peers_from_api
    peers_from_api=$(curl -s --max-time 3 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null | \
        jq -r --arg lip "$my_ip" '.nodes[]? | select(.host != $lip) | .host' 2>/dev/null)
    if [[ -n "$peers_from_api" ]]; then
        if [[ $found_any -eq 1 ]]; then
            # Merge: only add IPs not already in etcd list
            local existing
            existing=$(grep 'ETCD_INITIAL_CLUSTER=' /etc/default/etcd 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | grep -v "^${local_ip:-}$")
            while IFS= read -r api_ip; do
                [[ -z "$api_ip" ]] && continue
                if ! echo "$existing" | grep -qx "$api_ip"; then
                    echo "$api_ip"
                fi
            done <<< "$peers_from_api"
        else
            echo "$peers_from_api"
        fi
    fi

    # Method 3: Patroni pg_hba replication entries (fallback when etcd config
    # has no peers and stratum API is unavailable — e.g., after crash recovery
    # where etcd was force-new-clustered on the peer, leaving local INITIAL_CLUSTER
    # with only this node's IP)
    if [[ $found_any -eq 0 && -f /etc/patroni/patroni.yml ]]; then
        local my_ips
        my_ips=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' || true)
        local peers_from_patroni
        peers_from_patroni=$(grep 'host replication replicator' /etc/patroni/patroni.yml 2>/dev/null | \
            grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' || true)
        while IFS= read -r pat_ip; do
            [[ -z "$pat_ip" ]] && continue
            # Skip local IPs
            if echo "$my_ips" | grep -qx "$pat_ip" 2>/dev/null; then
                continue
            fi
            echo "$pat_ip"
        done <<< "$peers_from_patroni"
    fi
}

# Get single peer IP (first result from get_all_peer_ips).
# Retained for callers that only need one peer (address sync, failback gate).
get_etcd_peer_ip() {
    get_all_peer_ips | head -1
}

# Force etcd into single-node mode using the recovery script (runs as root)
force_etcd_single_node() {
    log "WARN" "ETCD RECOVERY: Forcing single-node cluster from existing data"

    # Always use the sudoers-listed path (sudoers does exact string matching;
    # the symlink at /usr/local/bin/spiralpool-etcd-recover is NOT in sudoers)
    local recover_script="${INSTALL_DIR}/scripts/etcd-quorum-recover.sh"
    if [[ ! -x "$recover_script" ]]; then
        log "ERROR" "ETCD RECOVERY: Recovery script not found at ${recover_script}"
        return 1
    fi

    log "INFO" "ETCD RECOVERY: Running ${recover_script}..."
    if sudo "$recover_script" >> "${LOG_FILE}" 2>&1; then
        log "SUCCESS" "ETCD RECOVERY: etcd healthy as single-node cluster"
        return 0
    else
        log "ERROR" "ETCD RECOVERY: Recovery script failed"
        return 1
    fi
}

# Wait for Patroni to promote PostgreSQL to read-write
# Uses Patroni REST API (no sudo needed) instead of psql
wait_for_pg_readwrite() {
    log "INFO" "ETCD RECOVERY: Waiting for Patroni to promote PostgreSQL..."
    for i in $(seq 1 60); do
        local patroni_role
        patroni_role=$(curl -s --max-time 3 "http://localhost:8008/patroni" 2>/dev/null | jq -r '.role // ""' 2>/dev/null || echo "")
        if [[ "$patroni_role" == "master" ]] || [[ "$patroni_role" == "primary" ]]; then
            log "SUCCESS" "ETCD RECOVERY: Patroni role=${patroni_role} (read-write)"
            return 0
        fi
        sleep 2
    done
    log "ERROR" "ETCD RECOVERY: Patroni did not promote within 120s"
    return 1
}

# Wait for stratum VIP election to complete after recovery
# After etcd recovery restarts stratum, the VIP election takes ~90s.
# During that time, get_cluster_role() returns BACKUP/unknown.
# Without this stabilization window, the main loop sees MASTER->BACKUP
# (3 debounced checks = 15s), triggers demotion (stops sentinel+dash),
# then sees BACKUP->MASTER after election completes, re-promotes.
# Net result: ~2.5 minutes of needless service outage.
#
# This function polls until MASTER is seen or timeout (150s), then
# updates last_role/state so the main loop resumes cleanly.
stabilize_after_recovery() {
    log "INFO" "POST-RECOVERY: Waiting for stratum VIP election to complete (up to 240s)..."
    local elapsed=0
    local max_wait=240
    local poll=5

    while [[ $elapsed -lt $max_wait ]]; do
        sleep $poll
        elapsed=$((elapsed + poll))

        local role
        role=$(get_cluster_role)

        if [[ "$role" == "MASTER" ]]; then
            log "SUCCESS" "POST-RECOVERY: Stratum election complete — role is MASTER after ${elapsed}s"
            # Promote services (sentinel, dash) — the main loop only triggers
            # on role CHANGES, but we just set last_role=MASTER so the main
            # loop will see MASTER==MASTER and skip promotion.
            trigger_service_control "${last_role}" "MASTER"
            save_role "MASTER"
            last_role="MASTER"
            return 0
        fi

        if [[ $((elapsed % 30)) -eq 0 ]]; then
            log "INFO" "POST-RECOVERY: Still waiting for MASTER (current: ${role}, ${elapsed}s elapsed)"
        fi
    done

    # Timeout — save whatever the current role is to prevent flap
    local final_role
    final_role=$(get_cluster_role)
    log "WARN" "POST-RECOVERY: VIP election did not reach MASTER within ${max_wait}s (final: ${final_role})"
    if [[ "$final_role" != "UNAVAILABLE" ]]; then
        save_role "$final_role"
        last_role="$final_role"
    fi
    return 1
}

# =============================================================================
# Automatic Failback (returns preferred primary to active duty)
# =============================================================================
#
# When the ha-master node returns after outage, it comes up as BACKUP.
# nopreempt prevents keepalived from grabbing VIP (safe — avoids VIP/DB split).
# Instead, ha-failback.sh orchestrates the correct order:
#   DB switchover FIRST → stratum election → keepalived VIP follows.
#
# Conditions (ALL must be true):
#   - This node is ha-master (preferred primary)
#   - Current role is BACKUP
#   - Peer is reachable (ping)
#   - System uptime > FAILBACK_BOOT_DELAY (services stabilized)
#   - Not in cooldown (FAILBACK_COOLDOWN between attempts)
#

attempt_failback() {
    local failback_script="${INSTALL_DIR}/scripts/ha-failback.sh"
    if [[ ! -x "$failback_script" ]]; then
        log "ERROR" "FAILBACK: Script not found at ${failback_script}"
        return 1
    fi

    log "INFO" "FAILBACK: Starting automatic failback to preferred primary..."
    if sudo "$failback_script" >> "${LOG_FILE}" 2>&1; then
        log "SUCCESS" "FAILBACK: Complete — this node is now primary"
        # Wait for stratum election to stabilize before resuming normal monitoring
        stabilize_after_recovery
        return 0
    else
        log "ERROR" "FAILBACK: Script failed — will retry after cooldown"
        return 1
    fi
}

# Force a fresh Patroni bootstrap (wipe PG data + clear etcd scope + restart)
# This is the nuclear option when Patroni can't promote due to:
#   - WAL corruption (PANIC: could not locate valid checkpoint)
#   - Stale standby.signal from previous replica state
#   - etcd "initialize" key preventing fresh bootstrap
#   - Diverged timelines from repeated failover cycles
#
# Runs patroni-force-bootstrap.sh as root via sudoers.
force_patroni_bootstrap() {
    log "WARN" "PATRONI RECOVERY: Patroni failed to promote — forcing fresh bootstrap"
    log "WARN" "PATRONI RECOVERY: This will wipe PostgreSQL data + clear etcd scope"

    # Always use the sudoers-listed path (sudoers does exact string matching;
    # the symlink at /usr/local/bin/spiralpool-patroni-bootstrap is NOT in sudoers)
    local recovery_script="${INSTALL_DIR}/scripts/patroni-force-bootstrap.sh"
    if [[ ! -x "$recovery_script" ]]; then
        log "ERROR" "PATRONI RECOVERY: Bootstrap script not found at ${recovery_script}"
        return 1
    fi

    log "INFO" "PATRONI RECOVERY: Running ${recovery_script}..."
    if sudo "$recovery_script" >> "${LOG_FILE}" 2>&1; then
        log "SUCCESS" "PATRONI RECOVERY: Fresh bootstrap succeeded — Patroni is primary"
        return 0
    else
        log "ERROR" "PATRONI RECOVERY: Fresh bootstrap script failed"
        return 1
    fi
}

# Main etcd quorum recovery — called when stratum API is down + VIP is local
#
# Return codes:
#   0 = Recovery succeeded
#   1 = Skipped (gate check failed — lightweight, no cooldown needed)
#   2 = Recovery attempted but failed (heavy operation ran — apply cooldown)
#
attempt_etcd_quorum_recovery() {
    # Gate 1: VIP must be on this node (keepalived promoted us to MASTER)
    if ! has_local_vip; then
        log "INFO" "ETCD RECOVERY: Skipped — VIP not on this node"
        return 1
    fi

    # Gate 2: etcd must actually be unhealthy (no quorum)
    if is_etcd_healthy; then
        log "INFO" "ETCD RECOVERY: Skipped — etcd is healthy"
        return 1
    fi

    log "WARN" "ETCD RECOVERY: VIP is local but etcd has no quorum — checking peers"

    # Gate 3: ALL peers must be confirmed unreachable (5 pings each over 10 seconds).
    # This prevents triggering on transient network blips or split-brain scenarios.
    # For 2-node (1 peer): 1 unreachable = all dead → same as before.
    # For 3-node (2 peers): both must be unreachable. If only 1 is down, etcd still
    # has 2/3 quorum and is_etcd_healthy() (gate 2) already blocks recovery. This
    # check is a safety net for edge cases where etcd reports unhealthy transiently
    # while a majority of peers are actually alive.
    local all_peer_ips
    all_peer_ips=$(get_all_peer_ips)
    if [[ -z "$all_peer_ips" ]]; then
        log "ERROR" "ETCD RECOVERY: Cannot determine any peer IPs"
        return 1
    fi

    local total_peers=0
    local unreachable_peers=0
    while IFS= read -r pip; do
        [[ -z "$pip" ]] && continue
        total_peers=$((total_peers + 1))
        local pip_alive=0
        for check in 1 2 3 4 5; do
            if ping -c 1 -W 2 "$pip" &>/dev/null; then
                pip_alive=1
                break
            fi
            sleep 2
        done
        if [[ $pip_alive -eq 0 ]]; then
            unreachable_peers=$((unreachable_peers + 1))
            log "WARN" "ETCD RECOVERY: Peer ${pip} unreachable after 5 pings"
        else
            log "INFO" "ETCD RECOVERY: Peer ${pip} is reachable — not all peers dead, waiting"
            return 1
        fi
    done <<< "$all_peer_ips"

    if [[ $total_peers -eq 0 ]]; then
        # get_all_peer_ips returned non-empty but no parseable IPs — don't destroy the cluster
        log "ERROR" "ETCD RECOVERY: Peer list was non-empty but no valid IPs found — aborting"
        return 1
    fi

    if [[ $unreachable_peers -lt $total_peers ]]; then
        # Should not reach here (we return 1 above on first reachable), but safety net
        log "INFO" "ETCD RECOVERY: ${unreachable_peers}/${total_peers} peers unreachable — not triggering"
        return 1
    fi

    log "WARN" "ETCD RECOVERY: CONFIRMED — all ${total_peers} peer(s) unreachable + etcd no quorum + VIP local"
    log "WARN" "ETCD RECOVERY: Initiating automatic failover..."

    # Step 1: Force etcd into single-node mode
    if ! force_etcd_single_node; then
        return 2
    fi

    # Step 2: Wait for Patroni to promote PostgreSQL to read-write
    if ! wait_for_pg_readwrite; then
        # Patroni failed to promote within 120s.
        # Common causes: WAL corruption, stale standby.signal, diverged timelines,
        # etcd "initialize" key blocking fresh bootstrap.
        # Nuclear option: wipe PG data + etcd scope → fresh Patroni bootstrap.
        # SAFETY: Max attempts prevents infinite data-wipe loops
        if [[ $FORCE_BOOTSTRAP_ATTEMPTS -ge $FORCE_BOOTSTRAP_MAX_ATTEMPTS ]]; then
            log "ERROR" "ETCD RECOVERY: Force bootstrap attempted ${FORCE_BOOTSTRAP_MAX_ATTEMPTS} times — giving up, manual intervention required"
            return 2
        fi
        FORCE_BOOTSTRAP_ATTEMPTS=$((FORCE_BOOTSTRAP_ATTEMPTS + 1))
        log "WARN" "ETCD RECOVERY: Patroni promotion failed — escalating to force bootstrap (attempt ${FORCE_BOOTSTRAP_ATTEMPTS}/${FORCE_BOOTSTRAP_MAX_ATTEMPTS})"
        if ! force_patroni_bootstrap; then
            log "ERROR" "ETCD RECOVERY: Force bootstrap also failed — manual intervention required"
            return 2
        fi
        FORCE_BOOTSTRAP_ATTEMPTS=0  # Reset on success
        log "SUCCESS" "ETCD RECOVERY: Force bootstrap recovered PostgreSQL"
    fi

    # Step 3: Restart stratum (crashed from read-only PG, likely in 'failed' state)
    log "INFO" "ETCD RECOVERY: Restarting stratum..."
    if ! sudo systemctl restart spiralstratum 2>&1; then
        log "ERROR" "ETCD RECOVERY: systemctl restart spiralstratum failed"
    fi

    # Step 4: Wait for stratum API to come back
    local stratum_ready=0
    for i in $(seq 1 30); do
        if curl -s --max-time 2 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null | grep -q "enabled"; then
            stratum_ready=1
            break
        fi
        sleep 2
    done

    if [[ $stratum_ready -eq 1 ]]; then
        log "SUCCESS" "ETCD RECOVERY: Complete — stratum serving, miners reconnecting"
        return 0
    else
        log "ERROR" "ETCD RECOVERY: etcd + Patroni recovered but stratum not responding after 60s"
        return 2
    fi
}

# =============================================================================
# Main Loop
# =============================================================================

run_watcher() {
    log "INFO" "HA Role Watcher starting..."
    log "INFO" "Poll interval: ${POLL_INTERVAL}s"
    log "INFO" "HA status port: ${HA_STATUS_PORT}"

    # R-5 FIX: Check ha-enabled file here instead of systemd ConditionPathExists.
    # This allows graceful waiting instead of hard startup failure.
    local ha_file="${INSTALL_DIR}/config/ha-enabled"
    if [[ ! -f "$ha_file" ]]; then
        log "INFO" "HA not enabled (${ha_file} missing) — waiting up to 5 minutes..."
        local ha_found=0
        for ((wait=1; wait<=60; wait++)); do
            if [[ -f "$ha_file" ]]; then
                log "INFO" "HA enabled file found after $((wait * 5))s"
                ha_found=1
                break
            fi
            sleep 5
        done
        if [[ "$ha_found" -eq 0 ]]; then
            log "INFO" "HA not enabled after 300s — exiting (not an error)"
            exit 0
        fi
    fi

    # Read ha-mode file to determine if this is the preferred primary
    # Used for automatic failback: only ha-master attempts to reclaim primary role
    local ha_mode_file="${INSTALL_DIR}/config/ha-mode"
    local HA_NODE_MODE="unknown"
    if [[ -f "$ha_mode_file" ]]; then
        HA_NODE_MODE=$(cat "$ha_mode_file" 2>/dev/null | tr -d '[:space:]')
        log "INFO" "HA node mode: ${HA_NODE_MODE}"
    else
        log "INFO" "No ha-mode file found — failback disabled"
    fi

    # R-4 FIX: Wait for HA API to be available before first role detection.
    # After power loss, spiralstratum may take minutes to start (daemon sync,
    # PG recovery). If we poll immediately, curl times out → STANDALONE assumed
    # → both nodes think they're standalone = split brain.
    # Wait up to 5 minutes for the HA API, retrying every 5 seconds.
    log "INFO" "Waiting for HA API to become available..."
    local api_ready=0
    for ((attempt=1; attempt<=60; attempt++)); do
        local test_json
        test_json=$(curl -s --max-time 3 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null || echo "")
        if [[ -n "$test_json" ]]; then
            log "INFO" "HA API ready after $((attempt * 5))s"
            api_ready=1
            break
        fi
        if [[ $((attempt % 6)) -eq 0 ]]; then
            log "INFO" "Still waiting for HA API... ($((attempt * 5))s elapsed)"
        fi
        sleep 5
    done
    if [[ "$api_ready" -eq 0 ]]; then
        log "WARN" "HA API not available after 300s — starting with saved/STANDALONE role"

        # Early failback: If ha-master can't reach local API after 300s but
        # a peer is reachable, the most likely cause is etcd dead from
        # force-new-cluster on peer during failover. Attempt failback now
        # instead of waiting for main loop to accumulate MAX_API_FAILURES.
        if [[ "$HA_NODE_MODE" == "ha-master" ]]; then
            local any_peer_reachable=0
            local all_peers
            all_peers=$(get_all_peer_ips)
            while IFS= read -r fp_ip; do
                [[ -z "$fp_ip" ]] && continue
                if ping -c 3 -W 2 "$fp_ip" &>/dev/null; then
                    any_peer_reachable=1
                    break
                fi
            done <<< "$all_peers"
            if [[ $any_peer_reachable -eq 1 ]]; then
                log "INFO" "FAILBACK: ha-master API unavailable on startup + peer reachable — attempting immediate failback"
                if attempt_failback; then
                    log "SUCCESS" "FAILBACK: Immediate failback from startup completed"
                else
                    log "WARN" "FAILBACK: Immediate attempt failed — will retry in main loop"
                fi
            fi
        fi
    fi

    local last_role
    last_role=$(get_saved_role)
    log "INFO" "Initial saved role: ${last_role}"

    # Initial sync on startup
    local current_role
    current_role=$(get_cluster_role)
    log "INFO" "Current cluster role: ${current_role}"

    if [[ "$current_role" != "UNAVAILABLE" && "$current_role" != "$last_role" ]]; then
        log "INFO" "Role mismatch on startup, triggering sync..."
        trigger_service_control "$last_role" "$current_role"
        if [[ $? -ne 0 ]]; then
            log "WARN" "Service control failed on startup sync — saving role anyway"
        fi
        save_role "$current_role"
        last_role="$current_role"
    fi

    # Main polling loop with debounce — require 3 consecutive same-role readings
    # before acting on a role change. This prevents transient API blips from
    # triggering service control actions.
    local consecutive_count=0
    local pending_role=""
    local api_fail_count=0
    local MAX_API_FAILURES=10
    local BACKOFF_INTERVAL=30  # seconds — used when API is consistently unavailable
    local current_interval="${POLL_INTERVAL}"
    local etcd_recovery_last_attempt=0
    local ETCD_RECOVERY_COOLDOWN=60  # Minimum 60s between actual recovery attempts
    local addr_sync_last_check=0
    local ADDR_SYNC_INTERVAL=60  # Check address sync every 60s
    local failback_last_attempt=0

    while true; do
        # Simple log rotation — rotate when log exceeds 10MB
        if [[ -f "${LOG_FILE}" ]] && [[ $(stat -f%z "${LOG_FILE}" 2>/dev/null || stat -c%s "${LOG_FILE}" 2>/dev/null || echo 0) -gt 10485760 ]]; then
            mv "${LOG_FILE}" "${LOG_FILE}.old"
            log "INFO" "Log rotated (exceeded 10MB)"
        fi

        sleep "${current_interval}"

        current_role=$(get_cluster_role)

        # Skip role changes when API is unavailable, with retry backoff
        if [[ "$current_role" == "UNAVAILABLE" ]]; then
            api_fail_count=$((api_fail_count + 1))
            if [[ $api_fail_count -ge $MAX_API_FAILURES && "$current_interval" != "$BACKOFF_INTERVAL" ]]; then
                current_interval=$BACKOFF_INTERVAL
                log "WARN" "API unavailable for ${api_fail_count} consecutive checks, backing off to ${BACKOFF_INTERVAL}s poll interval"
            fi

            # etcd quorum recovery: If stratum API is consistently down and
            # VIP is on this node, the primary may have died and etcd lost
            # quorum (cluster can't survive majority node loss). Detect this
            # and force etcd into single-node mode so Patroni can promote.
            if [[ $api_fail_count -ge 3 ]]; then
                local now
                now=$(date +%s)
                if [[ $((now - etcd_recovery_last_attempt)) -ge $ETCD_RECOVERY_COOLDOWN ]]; then
                    attempt_etcd_quorum_recovery
                    local rc=$?
                    if [[ $rc -eq 0 ]]; then
                        log "SUCCESS" "etcd quorum recovery succeeded — stabilizing before resuming"
                        api_fail_count=0
                        current_interval="${POLL_INTERVAL}"
                        etcd_recovery_last_attempt=$(date +%s)
                        # Wait for stratum VIP election to complete before
                        # resuming normal monitoring (prevents VIP flap)
                        stabilize_after_recovery
                    elif [[ $rc -eq 2 ]]; then
                        # Heavy operation was attempted but failed — apply cooldown
                        etcd_recovery_last_attempt=$(date +%s)
                    fi
                    # rc=1: gate skip (VIP not local, etcd healthy, peer reachable)
                    # No cooldown — check again at next poll
                fi
            fi

            # Failback from UNAVAILABLE state: After hard power-off, ha-master
            # reboots with local stratum unable to start (etcd dead from
            # force-new-cluster on peer → Patroni stuck → no DB → no stratum).
            # The normal failback path (after 'continue') requires last_role ==
            # "BACKUP" from local API, which is unreachable. Detect this and
            # trigger ha-failback.sh directly — it handles etcd rejoin, which
            # unblocks the entire Patroni → stratum → API chain.
            if [[ "$HA_NODE_MODE" == "ha-master" && $api_fail_count -ge $MAX_API_FAILURES ]]; then
                local uptime_secs
                uptime_secs=$(awk '{print int($1)}' /proc/uptime 2>/dev/null || echo "0")
                if [[ $uptime_secs -ge $FAILBACK_BOOT_DELAY ]]; then
                    local now
                    now=$(date +%s)
                    if [[ $((now - failback_last_attempt)) -ge $FAILBACK_COOLDOWN ]]; then
                        local any_peer_reachable=0
                        local all_peers
                        all_peers=$(get_all_peer_ips)
                        while IFS= read -r fp_ip; do
                            [[ -z "$fp_ip" ]] && continue
                            if ping -c 3 -W 2 "$fp_ip" &>/dev/null; then
                                any_peer_reachable=1
                                break
                            fi
                        done <<< "$all_peers"
                        if [[ $any_peer_reachable -eq 1 ]]; then
                            log "INFO" "FAILBACK: ha-master with local API unavailable + peer reachable — attempting failback (etcd rejoin)"
                            failback_last_attempt=$(date +%s)
                            if attempt_failback; then
                                log "SUCCESS" "FAILBACK: Automatic failback from UNAVAILABLE state completed"
                                api_fail_count=0
                                current_interval="${POLL_INTERVAL}"
                                etcd_recovery_last_attempt=0
                            else
                                log "WARN" "FAILBACK: Attempt failed — will retry after ${FAILBACK_COOLDOWN}s cooldown"
                            fi
                        fi
                    fi
                fi
            fi

            consecutive_count=0
            pending_role=""
            continue
        fi

        # API recovered — reset failure counter, poll interval, and recovery cooldown
        if [[ $api_fail_count -gt 0 ]]; then
            if [[ "$current_interval" != "$POLL_INTERVAL" ]]; then
                log "INFO" "API recovered after ${api_fail_count} failures, restoring ${POLL_INTERVAL}s poll interval"
            fi
            api_fail_count=0
            current_interval="${POLL_INTERVAL}"
            etcd_recovery_last_attempt=0  # Reset so next outage can recover immediately
        fi

        # FIX: Also check etcd recovery when API IS responsive.
        # Stratum API stays up while PG is read-only (lost etcd quorum),
        # so the UNAVAILABLE path above never fires. VIP being local +
        # etcd unhealthy still needs recovery regardless of API status.
        if has_local_vip && ! is_etcd_healthy; then
            local now
            now=$(date +%s)
            if [[ $((now - etcd_recovery_last_attempt)) -ge $ETCD_RECOVERY_COOLDOWN ]]; then
                attempt_etcd_quorum_recovery
                local rc=$?
                if [[ $rc -eq 0 ]]; then
                    log "SUCCESS" "etcd quorum recovery succeeded — stabilizing before resuming"
                    api_fail_count=0
                    current_interval="${POLL_INTERVAL}"
                    etcd_recovery_last_attempt=$(date +%s)
                    # Wait for stratum VIP election to complete before
                    # resuming normal monitoring (prevents VIP flap)
                    stabilize_after_recovery
                elif [[ $rc -eq 2 ]]; then
                    etcd_recovery_last_attempt=$(date +%s)
                fi
            fi
        fi

        # Address sync: On BACKUP/OBSERVER nodes, periodically sync pool
        # addresses from the master. Only when role is stable (no pending
        # role change) and API is responsive. Safe because backup stratum
        # restart doesn't affect miners (they connect to VIP = master).
        if [[ "$current_role" == "BACKUP" || "$current_role" == "OBSERVER" ]] && [[ $consecutive_count -eq 0 ]]; then
            local now
            now=$(date +%s)
            if [[ $((now - addr_sync_last_check)) -ge $ADDR_SYNC_INTERVAL ]]; then
                addr_sync_last_check=$now
                # Fetch full status JSON for address comparison
                local sync_status_json
                sync_status_json=$(curl -s --max-time 5 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null || echo "")
                # Compare master's poolAddresses with local config
                local master_addrs
                master_addrs=$(echo "$sync_status_json" | jq -r '
                    .nodes[] | select(.role == "MASTER") |
                    .poolAddresses // {} | to_entries[] |
                    "\(.key)=\(.value)"
                ' 2>/dev/null || echo "")
                if [[ -n "$master_addrs" ]]; then
                    local config_file="${INSTALL_DIR}/config/config.yaml"
                    local needs_sync=false
                    while IFS='=' read -r coin addr; do
                        [[ -z "$coin" || "$addr" == "PENDING_GENERATION" ]] && continue
                        coin="${coin^^}"
                        # Check if local config has a different address for this coin
                        local local_addr
                        local sym_line
                        # BUG FIX (M6): Case-insensitive match. API returns uppercase symbols
                        # but config.yaml may use lowercase. Without -i, grep misses every coin.
                        sym_line=$(grep -inE "symbol:[[:space:]]*\"?${coin}\"?[[:space:]]*$" "$config_file" 2>/dev/null | head -1 | cut -d: -f1)
                        if [[ -n "$sym_line" ]]; then
                            local_addr=$(tail -n "+${sym_line}" "$config_file" | grep -m1 "^[^#]*address:" | grep -oP 'address:\s*"?\K[^"[:space:]]+' || echo "")
                        else
                            # Coin not found — skip if V2 config (don't compare wrong coin's address)
                            if grep -q "symbol:" "$config_file" 2>/dev/null; then
                                continue
                            fi
                            # Genuine V1 single-coin mode (skip comments)
                            local_addr=$(grep -m1 "^[^#]*address:" "$config_file" | grep -oP 'address:\s*"?\K[^"[:space:]]+' || echo "")
                        fi
                        if [[ "$addr" != "$local_addr" ]]; then
                            needs_sync=true
                            break
                        fi
                    done <<< "$master_addrs"
                    if [[ "$needs_sync" == "true" ]]; then
                        log "INFO" "Address mismatch detected — syncing from master"
                        /usr/local/bin/spiralctl sync-addresses --apply 2>&1 | while IFS= read -r line; do
                            log "INFO" "sync-addresses: $line"
                        done
                    fi
                fi
            fi
        fi

        if [[ "$current_role" != "$last_role" ]]; then
            if [[ "$current_role" == "$pending_role" ]]; then
                consecutive_count=$((consecutive_count + 1))
            else
                pending_role="$current_role"
                consecutive_count=1
            fi

            if [[ $consecutive_count -ge 3 ]]; then
                log "INFO" "Role change confirmed after ${consecutive_count} consecutive checks: ${last_role} -> ${current_role}"
                trigger_service_control "$last_role" "$current_role"
                if [[ $? -ne 0 ]]; then
                    log "WARN" "Service control failed for ${last_role} -> ${current_role} — saving role anyway to prevent retry loop"
                fi
                # Always save the role. The cluster role is a fact — retrying
                # the same role transition every 15s forever won't fix a
                # persistent service control failure (e.g., stuck process).
                save_role "$current_role"
                last_role="$current_role"
                consecutive_count=0
                pending_role=""
            fi
        else
            consecutive_count=0
            pending_role=""
        fi

        # Automatic failback: If this is the preferred primary (ha-master) and
        # we're stable as BACKUP, attempt to reclaim primary role.
        # This runs AFTER the role-change block so we don't interfere with
        # normal role transitions. Only when role is stable (no pending change).
        if [[ "$HA_NODE_MODE" == "ha-master" && "$last_role" == "BACKUP" && $consecutive_count -eq 0 ]]; then
            local now
            now=$(date +%s)

            # Gate 1: System uptime must exceed boot delay (let services stabilize)
            local uptime_secs
            uptime_secs=$(awk '{print int($1)}' /proc/uptime 2>/dev/null || echo "0")
            if [[ $uptime_secs -lt $FAILBACK_BOOT_DELAY ]]; then
                # Too soon after boot — skip silently (don't spam logs every 5s)
                :
            # Gate 2: Cooldown between attempts
            elif [[ $((now - failback_last_attempt)) -lt $FAILBACK_COOLDOWN ]]; then
                :
            else
                # Gate 3: At least one peer must be reachable (don't failback if all peers are dead)
                # For 2-node: 1 peer reachable → triggers (same as before).
                # For N-node: any peer reachable → triggers (someone to failback from).
                local any_peer_reachable=0
                local all_peers
                all_peers=$(get_all_peer_ips)
                while IFS= read -r fp_ip; do
                    [[ -z "$fp_ip" ]] && continue
                    if ping -c 3 -W 2 "$fp_ip" &>/dev/null; then
                        any_peer_reachable=1
                        break
                    fi
                done <<< "$all_peers"
                if [[ $any_peer_reachable -eq 1 ]]; then
                    log "INFO" "FAILBACK: Conditions met — ha-master is BACKUP, peer reachable, attempting failback"
                    failback_last_attempt=$(date +%s)
                    if attempt_failback; then
                        log "SUCCESS" "FAILBACK: Automatic failback completed"
                        # last_role and state file updated by stabilize_after_recovery inside attempt_failback
                    else
                        log "WARN" "FAILBACK: Attempt failed — will retry after ${FAILBACK_COOLDOWN}s cooldown"
                    fi
                fi
            fi
        fi
    done
}

# =============================================================================
# One-shot check (for testing/manual use)
# =============================================================================

check_once() {
    local saved_role
    saved_role=$(get_saved_role)

    local current_role
    current_role=$(get_cluster_role)

    echo "Saved role:   ${saved_role}"
    echo "Current role: ${current_role}"

    if [[ "$current_role" != "$saved_role" ]]; then
        echo "Role CHANGED - would trigger service control"
        return 1
    else
        echo "Role unchanged"
        return 0
    fi
}

# =============================================================================
# Usage
# =============================================================================

show_help() {
    echo ""
    echo "Spiral Pool HA Role Watcher"
    echo ""
    echo "Usage: $0 <command>"
    echo ""
    echo "Commands:"
    echo "  start     Start the watcher daemon (runs in foreground)"
    echo "  check     One-shot check of current role"
    echo "  status    Show current and saved role"
    echo "  help      Show this help"
    echo ""
    echo "This service monitors the HA cluster and triggers service control"
    echo "when the local node's role changes (e.g., BACKUP -> MASTER)."
    echo ""
    echo "It should be run as a systemd service on ALL nodes."
    echo ""
}

# =============================================================================
# Main
# =============================================================================

main() {
    local command="${1:-start}"

    case "$command" in
        start|run|daemon)
            run_watcher
            ;;
        check|test)
            check_once
            ;;
        status)
            echo "Saved role:   $(get_saved_role)"
            echo "Current role: $(get_cluster_role)"
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            echo "Unknown command: $command"
            show_help
            exit 1
            ;;
    esac
}

# Trap signals for clean shutdown
trap 'log "INFO" "HA Role Watcher shutting down..."; exit 0' SIGTERM SIGINT

main "$@"
