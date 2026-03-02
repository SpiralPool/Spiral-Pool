#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool HA Failback Orchestrator
# Returns the preferred primary (ha-master) to active duty after outage.
#
# Called by ha-role-watcher.sh when:
#   1. This node is ha-master (preferred primary)
#   2. Current role is BACKUP (not already primary)
#   3. Peer is reachable
#   4. System has been up > 2 minutes (services stabilized)
#
# Orchestration order (each step must succeed before the next):
#   1. Rejoin etcd cluster (if unhealthy)
#   2. Wait for Patroni to join as replica + replication lag = 0
#   3. Patroni switchover (make this node DB primary)
#   4. Stop ALL peer stratums → restart local stratum → wait for MASTER election
#   5. Restart LOCAL keepalived first (higher priority claims MASTER) → restart peer keepalived → start ALL peer stratums
#   6. Verify: VIP local, Patroni primary, stratum MASTER
#
# This script MUST run as root (via sudoers) because:
#   - Calls etcd-cluster-rejoin.sh
#   - systemctl restart spiralstratum / keepalived
#   - SSH to peer for service control (uses spiraluser keys — root can read any file)
#
# Exit codes:
#   0 = Success (failback complete)
#   1 = Failure (partial state — logged, manual check needed)
#

set -euo pipefail

INSTALL_DIR="/spiralpool"
LOG_PREFIX="ha-failback"
HA_STATUS_PORT=5354

# SSH options — explicit paths to avoid root context issues (Incident 4)
SSH_USER="spiraluser"
SSH_OPTS=(
    -o StrictHostKeyChecking=accept-new
    -o "UserKnownHostsFile=/home/${SSH_USER}/.ssh/known_hosts"
    -o ConnectTimeout=10
    -o BatchMode=yes
    -i "/home/${SSH_USER}/.ssh/id_ed25519"
)

# Verify required tools
if ! command -v jq &>/dev/null; then
    echo "${LOG_PREFIX}: ERROR: jq is required but not installed. Install with: sudo apt install jq" >&2
    exit 1
fi

# Track whether we temporarily disabled synchronous_mode (3+ node failback).
# If the script exits abnormally after disabling it, the trap re-enables it.
SYNC_DISABLED=0
cleanup_sync_mode() {
    if [[ $SYNC_DISABLED -eq 1 ]]; then
        echo "${LOG_PREFIX}: CLEANUP: Re-enabling synchronous_mode after abnormal exit..."
        curl -s --max-time 10 -X PATCH "http://localhost:8008/config" \
            -H "Content-Type: application/json" \
            -d '{"synchronous_mode": true}' 2>/dev/null || true
        echo "${LOG_PREFIX}: CLEANUP: synchronous_mode re-enabled"
    fi
}
trap cleanup_sync_mode EXIT

log() {
    echo "${LOG_PREFIX}: $1"
}

log_error() {
    echo "${LOG_PREFIX}: ERROR: $1" >&2
}

# =============================================================================
# Validation
# =============================================================================

# Verify this is the preferred primary
HA_MODE_FILE="${INSTALL_DIR}/config/ha-mode"
if [[ ! -f "$HA_MODE_FILE" ]]; then
    log_error "ha-mode file not found at ${HA_MODE_FILE}"
    exit 1
fi
HA_MODE=$(cat "$HA_MODE_FILE" 2>/dev/null | tr -d '[:space:]' || true)
if [[ "$HA_MODE" != "ha-master" ]]; then
    log_error "This node is '${HA_MODE}', not 'ha-master' — failback not applicable"
    exit 1
fi

# Prevent concurrent execution — failback involves multi-step orchestration
mkdir -p /run/spiralpool 2>/dev/null || true
exec 8>/run/spiralpool/ha-failback.lock
if ! flock -n 8; then
    log_error "Another failback is already running"
    exit 1
fi

# Get ALL non-local peer IPs (newline-separated).
# Tries etcd config first (INITIAL_CLUSTER has all peers), falls back to stratum API.
# NOTE: || true on all pipelines — this script uses set -euo pipefail.
get_all_peer_ips() {
    local found_any=0

    # Method 1: etcd config (all peers listed in INITIAL_CLUSTER)
    if [[ -f /etc/default/etcd ]]; then
        local local_ip
        local_ip=$(grep 'ETCD_LISTEN_PEER_URLS' /etc/default/etcd 2>/dev/null | grep -oP 'http://\K[0-9.]+' | head -1 || true)
        if [[ -n "$local_ip" ]]; then
            local peers_from_etcd
            peers_from_etcd=$(grep 'ETCD_INITIAL_CLUSTER=' /etc/default/etcd 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | grep -v "^${local_ip}$" || true)
            if [[ -n "$peers_from_etcd" ]]; then
                echo "$peers_from_etcd"
                found_any=1
            fi
        fi
    fi

    # Method 2: Stratum HA API (peers visible via UDP discovery)
    local my_ip
    my_ip=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | grep -v '^127\.' | head -1 || true)
    local peers_from_api
    peers_from_api=$(curl -s --max-time 3 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null | \
        jq -r --arg lip "$my_ip" '.nodes[]? | select(.host != $lip) | .host' 2>/dev/null || true)
    if [[ -n "$peers_from_api" ]]; then
        if [[ $found_any -eq 1 ]]; then
            local existing
            local local_ip_for_merge
            local_ip_for_merge=$(grep 'ETCD_LISTEN_PEER_URLS' /etc/default/etcd 2>/dev/null | grep -oP 'http://\K[0-9.]+' | head -1 || true)
            existing=$(grep 'ETCD_INITIAL_CLUSTER=' /etc/default/etcd 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | grep -v "^${local_ip_for_merge:-}$" || true)
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

# Build ALL_PEER_IPS array and find first reachable peer as PEER_IP
ALL_PEER_IPS_RAW=$(get_all_peer_ips || true)
if [[ -z "$ALL_PEER_IPS_RAW" ]]; then
    log_error "Cannot determine any peer IPs from etcd config or stratum API"
    exit 1
fi

# Convert to array
ALL_PEER_IPS=()
while IFS= read -r _ip; do
    [[ -z "$_ip" ]] && continue
    ALL_PEER_IPS+=("$_ip")
done <<< "$ALL_PEER_IPS_RAW"

# Find first reachable peer (for etcd rejoin and Patroni operations)
PEER_IP=""
for _candidate in "${ALL_PEER_IPS[@]}"; do
    if ping -c 1 -W 3 "$_candidate" &>/dev/null; then
        PEER_IP="$_candidate"
        break
    fi
    log "Peer ${_candidate} not reachable — trying next..."
done

if [[ -z "$PEER_IP" ]]; then
    # Fall back to first in list (etcd rejoin may still work if ping is blocked but SSH works)
    PEER_IP="${ALL_PEER_IPS[0]}"
    log "WARNING: No peer responded to ping — using ${PEER_IP} as best guess"
fi

log "Starting failback: this node is ha-master, ${#ALL_PEER_IPS[@]} peer(s), primary target: ${PEER_IP}"

# Get Patroni node name (must match what Patroni actually uses)
# Read from config file first; fall back to naming convention from install.sh
PATRONI_NAME=$(grep -m1 '^name:' /etc/patroni/patroni.yml 2>/dev/null | awk '{print $2}' | tr -d '[:space:]"' || true)
if [[ -z "$PATRONI_NAME" ]]; then
    PATRONI_NAME="patroni-$(hostname -s)"
    log "WARN: Could not read Patroni name from config, using convention: ${PATRONI_NAME}"
fi
log "Patroni node name: ${PATRONI_NAME}"

# =============================================================================
# Step 1: etcd cluster rejoin (if needed)
# =============================================================================

is_etcd_healthy() {
    ETCDCTL_API=3 etcdctl endpoint health \
        --endpoints=http://127.0.0.1:2379 \
        --command-timeout=3s 2>&1 | grep -q "is healthy"
}

if ! is_etcd_healthy; then
    log "Step 1: etcd unhealthy — running cluster rejoin..."
    REJOIN_SCRIPT="${INSTALL_DIR}/scripts/etcd-cluster-rejoin.sh"
    if [[ ! -x "$REJOIN_SCRIPT" ]]; then
        log_error "etcd-cluster-rejoin.sh not found at ${REJOIN_SCRIPT}"
        exit 1
    fi
    if ! "$REJOIN_SCRIPT" --peer-ip "$PEER_IP" 2>&1; then
        log_error "Step 1 FAILED: etcd cluster rejoin failed"
        exit 1
    fi
    log "Step 1: etcd cluster rejoin succeeded"
else
    log "Step 1: etcd already healthy — skipping rejoin"
fi

# =============================================================================
# Step 2: Wait for Patroni to join as replica
# =============================================================================

log "Step 2: Waiting for Patroni to join as replica (max 5 min)..."
PATRONI_READY=0
for i in $(seq 1 60); do
    PATRONI_JSON=$(curl -s --max-time 3 "http://localhost:8008/patroni" 2>/dev/null || echo "")
    PATRONI_ROLE=$(echo "$PATRONI_JSON" | jq -r '.role // ""' 2>/dev/null || echo "")
    PATRONI_STATE=$(echo "$PATRONI_JSON" | jq -r '.state // ""' 2>/dev/null || echo "")

    if [[ "$PATRONI_ROLE" == "replica" && "$PATRONI_STATE" == "running" ]]; then
        PATRONI_READY=1
        break
    fi
    if [[ "$PATRONI_ROLE" == "master" || "$PATRONI_ROLE" == "primary" ]]; then
        log "Step 2: Patroni already reports role=${PATRONI_ROLE} — skipping to step 4"
        PATRONI_READY=2  # Already primary
        break
    fi
    if [[ $((i % 12)) -eq 0 ]]; then
        log "Step 2: Still waiting (role=${PATRONI_ROLE:-unknown}, state=${PATRONI_STATE:-unknown}, $((i * 5))s elapsed)"
    fi
    sleep 5
done

if [[ $PATRONI_READY -eq 0 ]]; then
    log_error "Step 2 FAILED: Patroni did not join as replica within 5 minutes"
    exit 1
fi

# =============================================================================
# Step 3: Wait for replication lag = 0, then Patroni switchover
# =============================================================================

if [[ $PATRONI_READY -eq 1 ]]; then
    # Wait for replication to catch up
    log "Step 3a: Waiting for replication lag to reach 0..."
    LAG_ZERO=0
    for i in $(seq 1 60); do
        # Use /cluster endpoint — it reports lag per member directly
        CLUSTER_JSON=$(curl -s --max-time 3 "http://localhost:8008/cluster" 2>/dev/null || echo "")
        NODE_LAG=$(echo "$CLUSTER_JSON" | jq -r --arg host "$PATRONI_NAME" \
            '.members[] | select(.name == $host) | .lag // 0' 2>/dev/null || echo "unknown")

        # Empty string = jq select() matched nothing (node name not in members list)
        if [[ -z "$NODE_LAG" ]]; then
            if [[ $((i % 6)) -eq 0 ]]; then
                log "Step 3a: Node '${PATRONI_NAME}' not found in cluster members yet ($((i * 5))s elapsed)"
            fi
            sleep 5
            continue
        fi

        if [[ "$NODE_LAG" == "0" || "$NODE_LAG" == "null" ]]; then
            LAG_ZERO=1
            break
        fi
        if [[ $((i % 6)) -eq 0 ]]; then
            log "Step 3a: Replication lag=${NODE_LAG}, waiting... ($((i * 5))s elapsed)"
        fi
        sleep 5
    done

    if [[ $LAG_ZERO -eq 0 ]]; then
        log_error "Step 3a FAILED: Replication lag did not reach 0 within 5 minutes"
        exit 1
    fi
    log "Step 3a: Replication caught up (lag=0)"

    # Wait for sync_standby promotion (required for switchover in synchronous mode)
    # After catching up, Patroni eventually designates this node as sync_standby.
    # Switchover to a non-sync_standby is rejected: "candidate name does not match with sync_standby"
    # In a 2-node cluster, we're the only replica so Patroni always picks us.
    # In a 3+ node cluster, another replica may already be sync_standby — Patroni
    # won't redesignate. Fallback: temporarily disable synchronous_mode so switchover
    # works for any replica. Safe because Step 3a confirmed replication lag = 0.
    log "Step 3a2: Waiting for Patroni to promote to sync_standby..."
    IS_SYNC=0
    for i in $(seq 1 30); do
        CLUSTER_JSON=$(curl -s --max-time 3 "http://localhost:8008/cluster" 2>/dev/null || echo "")
        MY_ROLE=$(echo "$CLUSTER_JSON" | jq -r --arg host "$PATRONI_NAME" \
            '.members[] | select(.name == $host) | .role // ""' 2>/dev/null || echo "")
        if [[ "$MY_ROLE" == "sync_standby" ]]; then
            IS_SYNC=1
            break
        fi
        if [[ $((i % 6)) -eq 0 ]]; then
            log "Step 3a2: Current role=${MY_ROLE:-unknown}, waiting for sync_standby ($((i * 5))s elapsed)"
        fi
        sleep 5
    done

    if [[ $IS_SYNC -eq 0 ]]; then
        # 3+ node cluster: another replica is already sync_standby.
        # Patroni won't redesignate us. Temporarily disable synchronous_mode
        # so switchover works for any replica. Safe because Step 3a confirmed lag=0.
        log "WARN: Step 3a2: Not sync_standby after 2.5 min — likely 3+ node cluster"
        log "Step 3a2: Temporarily disabling synchronous_mode for switchover (lag verified = 0)"
        PATCH_RESULT=$(curl -s --max-time 10 -X PATCH "http://localhost:8008/config" \
            -H "Content-Type: application/json" \
            -d '{"synchronous_mode": false}' 2>/dev/null || echo "")
        if echo "$PATCH_RESULT" | grep -qi "error"; then
            log_error "Step 3a2 FAILED: Could not disable synchronous_mode: ${PATCH_RESULT}"
            exit 1
        fi
        SYNC_DISABLED=1
        log "Step 3a2: synchronous_mode disabled — proceeding with switchover"
        sleep 3  # Brief pause for Patroni to apply the config change
    else
        log "Step 3a2: Now sync_standby — eligible for switchover"
    fi

    # Trigger Patroni switchover
    log "Step 3b: Triggering Patroni switchover..."
    # Fetch cluster state ONCE to avoid TOCTOU (leader could change between two curl calls)
    # NOTE: || echo "" prevents set -e exit so we can log a useful error
    CLUSTER_JSON=$(curl -s --max-time 3 "http://localhost:8008/cluster" 2>/dev/null || echo "")
    LEADER_NAME=$(echo "$CLUSTER_JSON" | \
        jq -r '.members[] | select(.role == "leader" or .role == "master") | .name' 2>/dev/null | head -1 || echo "")

    if [[ -z "$LEADER_NAME" ]]; then
        log_error "Step 3b FAILED: Cannot determine current Patroni leader"
        exit 1
    fi

    # Find actual Patroni leader IP from the SAME /cluster response.
    # Critical for 3-node: if ha-3 is the leader but PEER_IP resolved to ha-2,
    # the switchover goes to the wrong node and fails.
    LEADER_API_IP=""
    LEADER_API_URL=$(echo "$CLUSTER_JSON" | \
        jq -r '.members[] | select(.role == "leader" or .role == "master") | .api_url // ""' 2>/dev/null | head -1 || echo "")
    if [[ -n "$LEADER_API_URL" ]]; then
        # Extract IP from api_url (format: http://IP:8008/patroni)
        LEADER_API_IP=$(echo "$LEADER_API_URL" | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
    fi
    # Fall back to PEER_IP if leader IP can't be determined
    if [[ -z "$LEADER_API_IP" ]]; then
        LEADER_API_IP="$PEER_IP"
        log "Step 3b: Could not determine leader IP from /cluster — falling back to ${PEER_IP}"
    fi

    log "Step 3b: Current leader: ${LEADER_NAME} (at ${LEADER_API_IP}), switchover target: ${PATRONI_NAME}"
    # Switchover must be sent to the LEADER's Patroni API, not the local replica
    SWITCHOVER_RESULT=$(curl -s --max-time 30 -X POST "http://${LEADER_API_IP}:8008/switchover" \
        -H "Content-Type: application/json" \
        -d "{\"leader\": \"${LEADER_NAME}\", \"candidate\": \"${PATRONI_NAME}\"}" 2>/dev/null || echo "")

    if echo "$SWITCHOVER_RESULT" | grep -qi "error\|failed"; then
        log_error "Step 3b: Switchover response: ${SWITCHOVER_RESULT}"
        # Don't exit — check if it actually worked despite error message
    else
        log "Step 3b: Switchover initiated: ${SWITCHOVER_RESULT}"
    fi

    # Wait for this node to become Patroni primary
    log "Step 3c: Waiting for Patroni to report primary role (max 60s)..."
    IS_PRIMARY=0
    for i in $(seq 1 30); do
        PATRONI_JSON=$(curl -s --max-time 3 "http://localhost:8008/patroni" 2>/dev/null || echo "")
        PATRONI_ROLE=$(echo "$PATRONI_JSON" | jq -r '.role // ""' 2>/dev/null || echo "")
        if [[ "$PATRONI_ROLE" == "master" || "$PATRONI_ROLE" == "primary" ]]; then
            IS_PRIMARY=1
            break
        fi
        sleep 2
    done

    if [[ $IS_PRIMARY -eq 0 ]]; then
        log_error "Step 3c FAILED: Patroni did not become primary within 60s"
        exit 1
    fi
    log "Step 3: Patroni switchover complete — this node is primary"

    # Re-enable synchronous_mode if we disabled it for 3+ node switchover
    if [[ $SYNC_DISABLED -eq 1 ]]; then
        log "Step 3c: Re-enabling synchronous_mode..."
        curl -s --max-time 10 -X PATCH "http://localhost:8008/config" \
            -H "Content-Type: application/json" \
            -d '{"synchronous_mode": true}' 2>/dev/null || true
        SYNC_DISABLED=0
        log "Step 3c: synchronous_mode re-enabled"
    fi
fi

# =============================================================================
# Step 4: Stratum election transfer
# =============================================================================

# Helper: SSH to any node by IP
ssh_node() {
    local target_ip="$1"
    shift
    ssh "${SSH_OPTS[@]}" "${SSH_USER}@${target_ip}" "$@"
}

# Helper: SSH to primary peer (backward compat wrapper)
ssh_peer() {
    ssh_node "$PEER_IP" "$@"
}

# Stop stratum on ALL peers (miners will briefly disconnect)
log "Step 4a: Stopping stratum on ${#ALL_PEER_IPS[@]} peer(s)..."
STOP_SUCCESSES=0
for _pip in "${ALL_PEER_IPS[@]}"; do
    log "Step 4a: Stopping stratum on ${_pip}..."
    if ssh_node "$_pip" "sudo /bin/systemctl stop spiralstratum" 2>&1; then
        STOP_SUCCESSES=$((STOP_SUCCESSES + 1))
    else
        log "WARN: Step 4a: Failed to stop stratum on ${_pip} via SSH — continuing"
    fi
done
if [[ $STOP_SUCCESSES -eq 0 ]]; then
    log "WARN: Step 4a: Could not stop stratum on ANY peer — election may still win via priority"
fi

log "Step 4b: Restarting local stratum (starting VIP election)..."
if ! systemctl restart spiralstratum 2>&1; then
    log_error "Step 4b FAILED: systemctl restart spiralstratum failed"
    # Critical failure — try to restart stratum on ALL peers so miners aren't orphaned
    for _pip in "${ALL_PEER_IPS[@]}"; do
        ssh_node "$_pip" "sudo /bin/systemctl start spiralstratum" 2>/dev/null || true
    done
    exit 1
fi

# Wait for local stratum to win election (up to 240s)
# NOTE: VIP election typically takes 90-180s. Original 150s timeout was too tight —
# election completed at ~177s during testing, 27s after the script declared failure.
log "Step 4c: Waiting for stratum VIP election to complete (up to 240s)..."
IS_MASTER=0
for i in $(seq 1 48); do
    sleep 5
    STATUS_JSON=$(curl -s --max-time 3 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null || echo "")
    LOCAL_ROLE=$(echo "$STATUS_JSON" | jq -r '.localRole // "UNKNOWN"' 2>/dev/null || echo "UNKNOWN")
    if [[ "$LOCAL_ROLE" == "MASTER" ]]; then
        IS_MASTER=1
        break
    fi
    if [[ $((i % 6)) -eq 0 ]]; then
        log "Step 4c: Waiting for MASTER (current: ${LOCAL_ROLE}, $((i * 5))s elapsed)"
    fi
done

if [[ $IS_MASTER -eq 0 ]]; then
    log_error "Step 4c: Did not become stratum MASTER within 240s"
    # Start stratum back on ALL peers — don't leave miners orphaned
    for _pip in "${ALL_PEER_IPS[@]}"; do
        ssh_node "$_pip" "sudo /bin/systemctl start spiralstratum" 2>/dev/null || true
    done
    exit 1
fi
log "Step 4: Stratum election won — this node is MASTER"

# =============================================================================
# Step 5: Bring peer back + transfer keepalived VIP
# =============================================================================

log "Step 5a: Starting stratum on ${#ALL_PEER_IPS[@]} peer(s) (rejoin as BACKUP)..."
for _pip in "${ALL_PEER_IPS[@]}"; do
    if ! ssh_node "$_pip" "sudo /bin/systemctl start spiralstratum" 2>&1; then
        log_error "Step 5a: Failed to start stratum on ${_pip} — may need manual start"
        # Non-fatal: this node is already MASTER, miners are connected
    fi
done

log "Step 5b: Restarting LOCAL keepalived first (higher priority claims MASTER)..."
if ! systemctl restart keepalived 2>&1; then
    log_error "Step 5b: Failed to restart local keepalived"
    # Non-fatal: stratum VIP manager handles VIP independently
fi

# Brief settle time — local keepalived must be advertising as MASTER before
# peer restarts, otherwise the peer (starting with no MASTER competitor)
# would claim MASTER via VRRP timeout, and nopreempt prevents local from
# reclaiming it.
sleep 3

log "Step 5c: Restarting keepalived on ${#ALL_PEER_IPS[@]} peer(s) (start as BACKUP)..."
for _pip in "${ALL_PEER_IPS[@]}"; do
    if ! ssh_node "$_pip" "sudo /bin/systemctl restart keepalived" 2>&1; then
        log_error "Step 5c: Failed to restart keepalived on ${_pip}"
    fi
done

# =============================================================================
# Step 6: Verification
# =============================================================================

log "Step 6: Verifying failback state..."
VERIFY_OK=true

# Check VIP is local — check for actual VIP IP (from stratum API) or keepalived label.
# Stratum VIP manager adds VIP without a label; keepalived adds with "spiralpool-vip" label.
# Either presence means VIP is local.
VIP_ADDR=$(curl -s --max-time 3 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null | \
    jq -r '.vip // ""' 2>/dev/null || echo "")
if [[ -n "$VIP_ADDR" ]] && ip addr show 2>/dev/null | grep -q " ${VIP_ADDR}/"; then
    log "  VIP: LOCAL (${VIP_ADDR}, ok)"
elif ip addr show 2>/dev/null | grep -q "spiralpool-vip"; then
    log "  VIP: LOCAL (keepalived label, ok)"
else
    log_error "  VIP: NOT local"
    VERIFY_OK=false
fi

# Check Patroni is primary
PATRONI_ROLE=$(curl -s --max-time 3 "http://localhost:8008/patroni" 2>/dev/null | \
    jq -r '.role // ""' 2>/dev/null || echo "")
if [[ "$PATRONI_ROLE" == "master" || "$PATRONI_ROLE" == "primary" ]]; then
    log "  Patroni: ${PATRONI_ROLE} (ok)"
else
    log_error "  Patroni: ${PATRONI_ROLE:-unknown}"
    VERIFY_OK=false
fi

# Check stratum is MASTER
LOCAL_ROLE=$(curl -s --max-time 3 "http://localhost:${HA_STATUS_PORT}/status" 2>/dev/null | \
    jq -r '.localRole // ""' 2>/dev/null || echo "")
if [[ "$LOCAL_ROLE" == "MASTER" ]]; then
    log "  Stratum: MASTER (ok)"
else
    log_error "  Stratum: ${LOCAL_ROLE:-unknown}"
    VERIFY_OK=false
fi

# Check etcd health
if is_etcd_healthy; then
    log "  etcd: healthy (ok)"
else
    log_error "  etcd: unhealthy"
    VERIFY_OK=false
fi

if [[ "$VERIFY_OK" == "true" ]]; then
    log "SUCCESS — Failback complete. This node is primary (VIP + DB + Stratum MASTER)"
    exit 0
else
    log_error "Failback partially complete — some checks failed (see above)"
    exit 1
fi
