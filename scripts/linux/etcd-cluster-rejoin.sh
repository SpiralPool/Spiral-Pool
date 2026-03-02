#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# etcd Cluster Rejoin Script
# Rejoins a returning node to the existing etcd cluster (supports N-node).
#
# Called by ha-failback.sh during automatic failback when:
#   1. This node is the preferred primary (ha-master) returning after outage
#   2. A peer node ran etcd-quorum-recover.sh (force-new-cluster)
#   3. etcd on this node can't rejoin because peer cluster has moved on
#
# Steps:
#   1. Parse /etc/default/etcd (same safe parsing as etcd-quorum-recover.sh)
#   2. Find a healthy peer from --peer-ip arg or ETCD_INITIAL_CLUSTER
#   3. Stop local etcd
#   4. Remove stale member entry from peer's etcd
#   5. Add this node back as LEARNER (peer quorum unaffected)
#   6. Update local etcd config (INITIAL_CLUSTER from peer's full member list)
#   7. Wipe local etcd data (stale state from previous cluster incarnation)
#   8. Start local etcd (learner — peer quorum unchanged)
#   9. Promote learner to voter
#  10. Verify cluster health (local + at least one peer)
#
# This script MUST run as root (via sudoers) because:
#   - etcd systemctl stop/start
#   - etcd data directory operations
#   - sed on /etc/default/etcd
#
# Exit codes:
#   0 = Success (etcd cluster healthy)
#   1 = Failure
#

set -euo pipefail

LOG_PREFIX="etcd-cluster-rejoin"

# Accept --peer-ip <IP> from caller (ha-failback.sh already knows the peer)
ARG_PEER_IP=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --peer-ip) ARG_PEER_IP="$2"; shift 2 ;;
        *) shift ;;
    esac
done

log() {
    echo "${LOG_PREFIX}: $1"
}

log_error() {
    echo "ERROR: $1" >&2
}

# Read etcd environment config
if [[ ! -f /etc/default/etcd ]]; then
    log_error "/etc/default/etcd not found"
    exit 1
fi

# SECURITY: Safe parsing — do NOT source (runs as root via sudoers NOPASSWD)
# Only extract ETCD_* variable assignments, ignore everything else
# (Same pattern as etcd-quorum-recover.sh lines 47-59)
while IFS='=' read -r key value; do
    # Skip comments, empty lines, non-ETCD variables
    [[ "$key" =~ ^[[:space:]]*# ]] && continue
    [[ -z "$key" ]] && continue
    key=$(echo "$key" | tr -d '[:space:]')
    [[ "$key" =~ ^ETCD_[A-Z_]+$ ]] || continue
    # Strip surrounding quotes from value
    value=$(echo "$value" | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
    # Export only recognized ETCD_ variables
    declare -g "$key=$value"
done < /etc/default/etcd

# Validate required variables
if [[ -z "${ETCD_NAME:-}" ]]; then
    log_error "ETCD_NAME not set in /etc/default/etcd"
    exit 1
fi
if [[ -z "${ETCD_DATA_DIR:-}" ]]; then
    log_error "ETCD_DATA_DIR not set in /etc/default/etcd"
    exit 1
fi

# SECURITY: Validate ETCD_NAME format (same as recover script)
if ! [[ "$ETCD_NAME" =~ ^[a-zA-Z0-9._-]+$ ]]; then
    log_error "ETCD_NAME contains invalid characters: ${ETCD_NAME:0:40}"
    exit 1
fi

# SECURITY: Validate ETCD_DATA_DIR is under expected path (prevent path traversal)
if [[ "$ETCD_DATA_DIR" == *".."* ]]; then
    log_error "ETCD_DATA_DIR contains '..' — refusing to proceed"
    exit 1
fi
ETCD_DATA_DIR_REAL=$(realpath -m "$ETCD_DATA_DIR" 2>/dev/null || echo "$ETCD_DATA_DIR")
case "$ETCD_DATA_DIR_REAL" in
    /var/lib/etcd|/var/lib/etcd/*) ;;
    *)
        log_error "ETCD_DATA_DIR '${ETCD_DATA_DIR_REAL}' not under /var/lib/etcd/"
        exit 1
        ;;
esac
ETCD_DATA_DIR="$ETCD_DATA_DIR_REAL"

# Prevent concurrent execution — shares lock with etcd-quorum-recover
mkdir -p /run/spiralpool 2>/dev/null || true
exec 8>/run/spiralpool/etcd-recovery.lock
if ! flock -n 8; then
    log_error "Another etcd recovery/rejoin is already running"
    exit 1
fi

# Get local IP from ETCD_LISTEN_PEER_URLS
# NOTE: || true prevents set -e exit on grep no-match so we can log a useful error
LOCAL_IP=$(echo "${ETCD_LISTEN_PEER_URLS:-}" | grep -oP 'http://\K[0-9.]+' | head -1 || true)
if [[ -z "$LOCAL_IP" ]]; then
    log_error "Cannot determine local IP from ETCD_LISTEN_PEER_URLS"
    exit 1
fi

# Build list of candidate peer IPs: --peer-ip arg first, then all non-local IPs from INITIAL_CLUSTER
CANDIDATE_IPS=()
if [[ -n "$ARG_PEER_IP" ]]; then
    CANDIDATE_IPS+=("$ARG_PEER_IP")
fi
# Add all non-local IPs from INITIAL_CLUSTER (may overlap with ARG_PEER_IP — deduped below)
while IFS= read -r cip; do
    [[ -z "$cip" ]] && continue
    # Skip if already in list
    dup=0
    for existing in "${CANDIDATE_IPS[@]+"${CANDIDATE_IPS[@]}"}"; do
        [[ "$existing" == "$cip" ]] && { dup=1; break; }
    done
    [[ $dup -eq 1 ]] && continue
    CANDIDATE_IPS+=("$cip")
done < <(echo "${ETCD_INITIAL_CLUSTER:-}" | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | grep -v "^${LOCAL_IP}$" || true)

if [[ ${#CANDIDATE_IPS[@]} -eq 0 ]]; then
    log_error "Cannot determine any peer IPs (pass --peer-ip or ensure ETCD_INITIAL_CLUSTER has peer nodes)"
    exit 1
fi

LOCAL_PEER_URL="http://${LOCAL_IP}:2380"

# Step 1: Find a healthy peer from candidates
# For 2-node: typically 1 candidate (tried once). For N-node: tries each until one responds.
PEER_IP=""
PEER_ENDPOINT=""
log "Searching for healthy peer among ${#CANDIDATE_IPS[@]} candidate(s)..."
for candidate in "${CANDIDATE_IPS[@]}"; do
    ep="http://${candidate}:2379"
    if ETCDCTL_API=3 etcdctl endpoint health --endpoints="${ep}" --command-timeout=5s 2>&1 | grep -q "is healthy"; then
        PEER_IP="$candidate"
        PEER_ENDPOINT="$ep"
        break
    fi
    log "Peer ${candidate} not healthy — trying next..."
done

if [[ -z "$PEER_IP" ]]; then
    log_error "No healthy peer found among candidates: ${CANDIDATE_IPS[*]}"
    exit 1
fi
log "Local: ${ETCD_NAME} (${LOCAL_IP}), Healthy peer: ${PEER_IP}"

# Step 2: Stop local etcd
log "Stopping local etcd..."
systemctl stop etcd 2>/dev/null || true

# Wait for port release
for i in $(seq 1 15); do
    if ! ss -tlnp 2>/dev/null | grep -q ':2380 '; then
        break
    fi
    if [[ $i -eq 10 ]]; then
        log "etcd still holding port after 10s, sending SIGKILL..."
        pkill -9 -x etcd 2>/dev/null || true
    fi
    sleep 1
done

# Verify port is actually released — proceeding with port held causes silent failures
if ss -tlnp 2>/dev/null | grep -q ':2380 '; then
    log_error "etcd port 2380 still in use after 15s — cannot proceed safely"
    log_error "Manual fix: pkill -9 etcd && sleep 2"
    exit 1
fi

# Step 3: Check for stale member entry on peer and remove it
log "Checking for stale member entry on peer..."
MEMBER_LIST=$(ETCDCTL_API=3 etcdctl member list --endpoints="${PEER_ENDPOINT}" 2>/dev/null || echo "")

# Find our stale entry by matching our name or peer URL
# NOTE: grep || true prevents set -e exit when no stale entry exists (common case)
# Name anchored with ", NAME," (etcd format: id, status, NAME, urls...) to prevent
# substring matches (e.g., etcd-ha-1 matching etcd-ha-10).
# IP anchored with colon (e.g., 192.168.1.10: won't match 192.168.1.104:).
STALE_ID=$(echo "$MEMBER_LIST" | grep -E "(, ${ETCD_NAME},|${LOCAL_IP}:)" | cut -d',' -f1 | tr -d '[:space:]' || true)
if [[ -n "$STALE_ID" ]]; then
    log "Removing stale member entry: ${STALE_ID}"
    if ! ETCDCTL_API=3 etcdctl member remove "$STALE_ID" --endpoints="${PEER_ENDPOINT}" 2>&1; then
        log_error "Failed to remove stale member — may need manual cleanup"
        exit 1
    fi
    log "Stale member removed"
    sleep 1
else
    log "No stale member entry found"
fi

# Step 4: Add this node back to the cluster as a LEARNER via peer.
# CRITICAL: --learner prevents peer quorum disruption. Without it, member add
# immediately increases the voting member count. If local etcd then fails
# to start, the cluster loses a voter — potentially dropping below quorum —
# Patroni loses its leader key — production DB goes read-only. With --learner,
# existing cluster quorum is unaffected until we explicitly promote.
log "Adding ${ETCD_NAME} back to cluster as learner via peer..."
# NOTE: if ! $() disables set -e for the substitution so we can handle failure
if ! ADD_OUTPUT=$(ETCDCTL_API=3 etcdctl member add "$ETCD_NAME" \
    --learner --peer-urls="${LOCAL_PEER_URL}" \
    --endpoints="${PEER_ENDPOINT}" --command-timeout=10s 2>&1); then
    log_error "Failed to add member: ${ADD_OUTPUT}"
    # Try to start etcd anyway so we don't leave it stopped
    systemctl start etcd 2>/dev/null || true
    exit 1
fi
log "Learner member added successfully"

# Step 5: Update local etcd config — INITIAL_CLUSTER_STATE + INITIAL_CLUSTER
# INITIAL_CLUSTER_STATE=existing tells etcd to join rather than bootstrap.
# INITIAL_CLUSTER must list all cluster members so etcd can discover peers.
# (On ha-master installed before ha-backup, INITIAL_CLUSTER only has itself.)
log "Updating /etc/default/etcd: INITIAL_CLUSTER_STATE=existing"
if grep -q 'ETCD_INITIAL_CLUSTER_STATE=' /etc/default/etcd; then
    sed -i 's/ETCD_INITIAL_CLUSTER_STATE=.*/ETCD_INITIAL_CLUSTER_STATE="existing"/' /etc/default/etcd
else
    echo 'ETCD_INITIAL_CLUSTER_STATE="existing"' >> /etc/default/etcd
fi

# Build INITIAL_CLUSTER from peer's FULL member list (supports N-node clusters).
# For 2-node: member list has 2 entries → same result as hardcoding 2 names.
# For N-node: all members are included, correctly handling 3+ node clusters.
FULL_MEMBER_LIST=$(ETCDCTL_API=3 etcdctl member list --endpoints="${PEER_ENDPOINT}" --write-out=simple --command-timeout=5s 2>/dev/null || echo "")
if [[ -n "$FULL_MEMBER_LIST" ]]; then
    NEW_INITIAL_CLUSTER=""
    while IFS= read -r mline; do
        [[ -z "$mline" ]] && continue
        # Format: <id>, <status>, <name>, <peer_urls>, <client_urls>, <is_learner>
        m_name=$(echo "$mline" | awk -F', ' '{print $3}' | tr -d '[:space:]')
        m_peer_url=$(echo "$mline" | awk -F', ' '{print $4}' | tr -d '[:space:]')

        # If this entry is our own (matched by IP) and name is empty (just added as learner),
        # use our ETCD_NAME instead
        if echo "$m_peer_url" | grep -q "${LOCAL_IP}:" && [[ -z "$m_name" ]]; then
            m_name="$ETCD_NAME"
        fi

        [[ -z "$m_name" || -z "$m_peer_url" ]] && continue

        if [[ -n "$NEW_INITIAL_CLUSTER" ]]; then
            NEW_INITIAL_CLUSTER="${NEW_INITIAL_CLUSTER},"
        fi
        NEW_INITIAL_CLUSTER="${NEW_INITIAL_CLUSTER}${m_name}=${m_peer_url}"
    done <<< "$FULL_MEMBER_LIST"

    if [[ -n "$NEW_INITIAL_CLUSTER" ]]; then
        log "Updating ETCD_INITIAL_CLUSTER: ${NEW_INITIAL_CLUSTER}"
        if grep -q 'ETCD_INITIAL_CLUSTER=' /etc/default/etcd; then
            sed -i "s|ETCD_INITIAL_CLUSTER=.*|ETCD_INITIAL_CLUSTER=\"${NEW_INITIAL_CLUSTER}\"|" /etc/default/etcd
        else
            echo "ETCD_INITIAL_CLUSTER=\"${NEW_INITIAL_CLUSTER}\"" >> /etc/default/etcd
        fi
    else
        log "WARNING: Could not parse member list — INITIAL_CLUSTER not updated"
    fi
else
    log "WARNING: Could not fetch member list from peer — INITIAL_CLUSTER not updated"
fi

# Step 6: Wipe local etcd data (stale state from previous cluster incarnation)
log "Wiping stale etcd data at ${ETCD_DATA_DIR}..."
if [[ -d "${ETCD_DATA_DIR}/member" ]]; then
    rm -rf "${ETCD_DATA_DIR}/member"
fi

# Step 7: Start local etcd
# Learner mode: peer cluster quorum is unaffected until we promote.
log "Starting local etcd (learner — peer quorum unaffected)..."
if ! systemctl start etcd; then
    log_error "systemctl start etcd failed"
    exit 1
fi

# Step 8: Wait for learner to connect, then promote BEFORE local health check.
# Learner nodes reject health RPCs (etcdserver: rpc not supported for learner),
# so checking local health first creates a chicken-and-egg: health never passes,
# promotion never runs. Fix: poll peer's member list for "started", promote, then verify.
log "Waiting for etcd learner to connect to peer..."
LEARNER_STARTED=0
for i in $(seq 1 30); do
    if ETCDCTL_API=3 etcdctl --command-timeout=5s --endpoints="${PEER_ENDPOINT}" \
        member list --write-out=simple 2>/dev/null | grep "${LOCAL_IP}:" | \
        awk -F', ' '{print $2}' | tr -d ' ' | grep -qx "started"; then
        LEARNER_STARTED=1
        break
    fi
    sleep 1
done

if [[ $LEARNER_STARTED -eq 0 ]]; then
    log "WARNING: learner did not reach 'started' state in 30s — attempting promotion anyway"
fi

# Step 9: Promote learner to full voting member BEFORE local health check.
# TOCTOU FIX: Fetch member list ONCE, parse twice.
MY_MEMBER_LINE=$(ETCDCTL_API=3 etcdctl member list --endpoints="${PEER_ENDPOINT}" --write-out=simple --command-timeout=5s 2>/dev/null \
    | grep "${LOCAL_IP}:" | head -1 || echo "")
MY_MEMBER_ID=$(echo "$MY_MEMBER_LINE" | awk -F', ' '{print $1}' | tr -d '[:space:]')
MY_IS_LEARNER=$(echo "$MY_MEMBER_LINE" | awk -F', ' '{print $6}' | tr -d ' ')

if [[ "${MY_IS_LEARNER}" == "true" ]] && [[ -n "${MY_MEMBER_ID}" ]]; then
    PROMOTE_OK=0
    for pt in $(seq 1 6); do
        if ETCDCTL_API=3 etcdctl member promote "${MY_MEMBER_ID}" --endpoints="${PEER_ENDPOINT}" 2>&1; then
            log "Learner promoted to voting member"
            PROMOTE_OK=1
            break
        fi
        log "Learner not ready for promotion (attempt $pt/6) — waiting..."
        sleep 5
    done
    if [[ $PROMOTE_OK -eq 0 ]]; then
        log_error "Failed to promote learner after 6 attempts. Manual fix:"
        log_error "  ETCDCTL_API=3 etcdctl --endpoints=${PEER_ENDPOINT} member promote ${MY_MEMBER_ID}"
        exit 1
    fi
fi

# Step 10: Verify cluster health (local + at least one peer must be healthy)
# For N-node: check local + all candidate peers. If any peer is unhealthy but local is
# healthy and at least one peer is healthy, still succeed (the unhealthy peer may be
# another node that hasn't rejoined yet).
log "Verifying cluster health after promotion..."
HEALTHY=0
for i in $(seq 1 30); do
    sleep 2
    LOCAL_HEALTH=$(ETCDCTL_API=3 etcdctl endpoint health --endpoints="http://127.0.0.1:2379" --command-timeout=3s 2>&1 || echo "")
    if ! echo "$LOCAL_HEALTH" | grep -q "is healthy"; then
        if [[ $((i % 5)) -eq 0 ]]; then
            log "Still waiting for local etcd health... ($((i * 2))s)"
        fi
        continue
    fi
    # Local is healthy — check if at least one peer is healthy
    any_peer_healthy=0
    for pip in "${CANDIDATE_IPS[@]}"; do
        pip_ep="http://${pip}:2379"
        if ETCDCTL_API=3 etcdctl endpoint health --endpoints="${pip_ep}" --command-timeout=3s 2>&1 | grep -q "is healthy"; then
            any_peer_healthy=1
            break
        fi
    done
    if [[ $any_peer_healthy -eq 1 ]]; then
        HEALTHY=1
        break
    fi
    if [[ $((i % 5)) -eq 0 ]]; then
        log "Still waiting for peer health... ($((i * 2))s)"
    fi
done

if [[ $HEALTHY -eq 1 ]]; then
    MEMBER_COUNT=$(ETCDCTL_API=3 etcdctl member list --endpoints="http://127.0.0.1:2379" 2>/dev/null | wc -l || true)
    log "SUCCESS — etcd healthy as ${MEMBER_COUNT}-node cluster"
    exit 0
else
    log_error "etcd cluster did not become healthy within 60s"
    # Clean up: remove this node's entry (learner OR voter) so peer cluster can restore quorum.
    # After promotion, member is a voter — removing it reduces the cluster size by 1,
    # allowing the remaining peers to regain quorum. Without this, the cluster may be stuck
    # at (N-1)/N quorum requirement with a dead member.
    # TOCTOU FIX: Single member list fetch, parse twice
    STALE_MEMBER_LINE=$(ETCDCTL_API=3 etcdctl member list --endpoints="${PEER_ENDPOINT}" --write-out=simple --command-timeout=5s 2>/dev/null \
        | grep "${LOCAL_IP}:" | head -1 || echo "")
    STALE_MEMBER_ID=$(echo "$STALE_MEMBER_LINE" | awk -F', ' '{print $1}' | tr -d '[:space:]')
    if [[ -n "${STALE_MEMBER_ID}" ]]; then
        STALE_IS_LEARNER=$(echo "$STALE_MEMBER_LINE" | awk -F', ' '{print $6}' | tr -d ' ')
        if [[ "${STALE_IS_LEARNER}" == "true" ]]; then
            log "Removing failed learner from peer cluster..."
        else
            log "Removing unhealthy voter from peer cluster to restore quorum..."
        fi
        ETCDCTL_API=3 etcdctl member remove "${STALE_MEMBER_ID}" --endpoints="${PEER_ENDPOINT}" 2>/dev/null && \
            log "Member removed — peer cluster quorum requirements reduced" || \
            log_error "Could not remove member — peer may need manual cleanup"
    fi
    # Show diagnostics
    ETCDCTL_API=3 etcdctl member list --endpoints="http://127.0.0.1:2379" 2>&1 || true
    ETCDCTL_API=3 etcdctl endpoint health --endpoints="http://127.0.0.1:2379" --command-timeout=3s 2>&1 || true
    exit 1
fi
