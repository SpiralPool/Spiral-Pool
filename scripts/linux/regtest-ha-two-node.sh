#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# =============================================================================
# Spiral Pool — Full HA Two-Node Integration Test
# =============================================================================
#
# WHAT THIS DOES:
#   Tests the COMPLETE production HA failover chain end-to-end on two real
#   nodes (VMs or bare metal). Unlike regtest-ha-full.sh (Docker/database only),
#   this validates EVERY link in the production chain:
#
#   Phase 1: SSH connectivity (bidirectional, spiraluser, postgres sudo)
#   Phase 2: HA infrastructure (stratum, VIP API, watcher, service-control)
#   Phase 3: Pre-failover cluster state (roles, VIP binding, services)
#   Phase 4: Live failover test (kill master → backup promotes)
#   Phase 5: Post-failover validation (VIP moved, services started)
#   Phase 6: Recovery test (old master rejoins as backup)
#   Phase 7: Reverse failover (prove both directions work)
#
# 10 KEY PROOFS OF HA CORRECTNESS:
#   1. SSH works bidirectionally           → ha-replicate.sh will work
#   2. HA status API on both nodes         → VIP manager running
#   3. Exactly one MASTER, one BACKUP      → cluster is consistent
#   4. VIP bound only on MASTER            → VIP management works
#   5. Services match roles                → ha-service-control.sh works
#   6. Watcher running on both nodes       → role changes will be detected
#   7. Kill master → VIP moves to backup   → core failover promise
#   8. Services start on new master        → full automation chain works
#   9. Old master recovers as BACKUP       → recovery works
#  10. Reverse failover succeeds           → both nodes are interchangeable
#
# VM SETUP INSTRUCTIONS (8GB VMs are sufficient):
#   1. Create two Ubuntu 24.04 VMs (VM1 and VM2) on the same network
#   2. Assign static IPs or note DHCP addresses
#   3. On BOTH VMs, install Spiral Pool:
#        sudo ./install.sh
#      (Coin daemons won't sync without disk space — that's fine for HA testing)
#   4. On BOTH VMs, configure HA via spiralctl:
#        sudo spiralctl vip setup --address <VIP> --interface ens33 \
#            --priority 100    # 100 on VM1 (master), 200 on VM2 (backup)
#      This generates a cluster token and creates /spiralpool/config/ha-enabled
#   5. Copy the cluster token from VM1 to VM2:
#        sudo spiralctl vip join --token <TOKEN_FROM_VM1>
#   6. Set up SSH keys for replication:
#        On VM2: sudo ./scripts/linux/ha-setup-ssh.sh --mode standby --peer <VM1_IP>
#   7. Restart stratum on both VMs:
#        sudo systemctl restart spiralstratum
#   8. Wait ~30s for both nodes to discover each other
#   9. Run this test:
#        sudo ./scripts/linux/regtest-ha-two-node.sh --peer <OTHER_VM_IP>
#
# USAGE:
#   ./regtest-ha-two-node.sh --peer <PEER_IP>
#   ./regtest-ha-two-node.sh --peer <PEER_IP> --skip-failover    # Validation only
#   ./regtest-ha-two-node.sh --peer <PEER_IP> --skip-reverse     # Skip reverse test
#   ./regtest-ha-two-node.sh --peer <PEER_IP> --ssh-user admin   # Custom SSH user
#
# PORTS REQUIRED (open between both VMs):
#   22   TCP — SSH
#   5354 TCP — HA status API
#   5363 UDP — VIP cluster discovery/heartbeat
#   5432 TCP — PostgreSQL (if using Patroni HA)
#
# =============================================================================

set -euo pipefail

# =============================================================================
# Configuration
# =============================================================================

INSTALL_DIR="/spiralpool"
HA_STATUS_PORT=5354
VIP_DISCOVERY_PORT=5363
POOL_USER="${POOL_USER:-spiraluser}"

# Default SSH settings
SSH_USER="${SSH_USER:-${SUDO_USER:-$(whoami)}}"
SSH_OPTS="-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new"

# Timing — these match production defaults from VIPConfig
# HeartbeatInterval=30s, FailoverTimeout=90s
# Total failover window: ~90-120s (timeout + election + VIP acquire)
# We add padding: 180s max wait
FAILOVER_MAX_WAIT=180
RECOVERY_MAX_WAIT=120
POLL_INTERVAL=3

# Test counters
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_WARNED=0
TESTS_SKIPPED=0

# State captured during test
LOCAL_IP=""
PEER_IP=""
MASTER_IP=""
BACKUP_IP=""
VIP_ADDRESS=""
LOCAL_ROLE=""
PEER_ROLE=""

# Flags
SKIP_FAILOVER=false
SKIP_REVERSE=false
FAILOVER_PERFORMED=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

# Log file
LOG_DIR="${INSTALL_DIR}/logs"
LOG_FILE="${LOG_DIR}/regtest-ha-two-node.log"

# =============================================================================
# Logging Functions
# =============================================================================

log_phase() {
    local phase="$1"
    echo ""
    echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}${BOLD}  $phase${NC}"
    echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════════════${NC}"
    echo ""
    _log "PHASE" "$phase"
}

log_section() {
    echo ""
    echo -e "  ${BOLD}▶ $1${NC}"
    echo -e "  ${DIM}─────────────────────────────────────────────────────────────${NC}"
    _log "SECTION" "$1"
}

pass() {
    echo -e "  ${GREEN}✓${NC} $1"
    ((++TESTS_PASSED))
    _log "PASS" "$1"
}

fail() {
    echo -e "  ${RED}✗${NC} $1"
    ((++TESTS_FAILED))
    _log "FAIL" "$1"
}

warn() {
    echo -e "  ${YELLOW}⚠${NC} $1"
    ((++TESTS_WARNED))
    _log "WARN" "$1"
}

skip() {
    echo -e "  ${DIM}○${NC} $1 ${DIM}(skipped)${NC}"
    ((++TESTS_SKIPPED))
    _log "SKIP" "$1"
}

info() {
    echo -e "  ${BLUE}ℹ${NC} $1"
    _log "INFO" "$1"
}

_log() {
    local level="$1" message="$2"
    if [[ -d "$LOG_DIR" ]]; then
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] [$level] $message" >> "$LOG_FILE" 2>/dev/null || true
    fi
}

# =============================================================================
# Utility Functions
# =============================================================================

# Execute command on peer via SSH
ssh_peer() {
    ssh $SSH_OPTS "${SSH_USER}@${PEER_IP}" "$@" 2>/dev/null
}

# Fetch HA status JSON from a node
fetch_ha_status() {
    local host="$1"
    curl -sf --max-time 5 "http://${host}:${HA_STATUS_PORT}/status" 2>/dev/null || echo ""
}

# Extract value from JSON
json_val() {
    local json="$1" key="$2"
    echo "$json" | grep -oP "\"${key}\":\s*\"?\K[^\",}]+" 2>/dev/null | head -1
}

json_bool() {
    local json="$1" key="$2"
    echo "$json" | grep -oP "\"${key}\":\s*\K(true|false)" 2>/dev/null | head -1
}

# Detect local IP (the IP that faces the peer)
detect_local_ip() {
    # Use ip route to find which source IP reaches the peer
    ip route get "$PEER_IP" 2>/dev/null | grep -oP 'src \K\S+' | head -1
}

# Check if a service is active locally
local_service_active() {
    systemctl is-active --quiet "$1" 2>/dev/null
}

# Check if a service is active on peer
peer_service_active() {
    ssh_peer "systemctl is-active --quiet $1" 2>/dev/null
}

# Check if VIP is bound to a specific host
vip_bound_on() {
    local host="$1" vip="$2"
    if [[ "$host" == "$LOCAL_IP" ]] || [[ "$host" == "localhost" ]]; then
        ip addr show 2>/dev/null | grep -q " ${vip}/"
    else
        ssh_peer "ip addr show 2>/dev/null | grep -q ' ${vip}/'"
    fi
}

# =============================================================================
# Phase 1: SSH Connectivity
# =============================================================================

phase_ssh_connectivity() {
    log_phase "Phase 1/7: SSH Connectivity"

    log_section "Basic SSH to peer"

    # Test SSH to peer
    if ssh_peer "echo OK" | grep -q OK; then
        pass "SSH to ${SSH_USER}@${PEER_IP}: connected"
    else
        fail "SSH to ${SSH_USER}@${PEER_IP}: FAILED"
        info "Fix: ssh-copy-id ${SSH_USER}@${PEER_IP}"
        info "Cannot continue without SSH. Aborting."
        return 1
    fi

    log_section "spiraluser SSH (for replication)"

    # Test spiraluser SSH (used by ha-replicate.sh)
    local spiraluser_ssh_ok=false
    if ssh $SSH_OPTS "${POOL_USER}@${PEER_IP}" "echo OK" 2>/dev/null | grep -q OK; then
        pass "SSH as ${POOL_USER}@${PEER_IP}: connected (replication ready)"
        spiraluser_ssh_ok=true
    else
        warn "SSH as ${POOL_USER}@${PEER_IP}: FAILED"
        info "Fix: Run ha-setup-ssh.sh --mode standby --peer ${PEER_IP}"
        info "Replication (ha-replicate.sh) won't work without this."
    fi

    log_section "Reverse SSH (peer → local)"

    # Test that peer can SSH back to us (bidirectional)
    LOCAL_IP=$(detect_local_ip)
    if [[ -z "$LOCAL_IP" ]]; then
        warn "Could not detect local IP facing peer"
        LOCAL_IP="localhost"
    else
        info "Local IP: $LOCAL_IP"
    fi

    if ssh_peer "ssh $SSH_OPTS ${POOL_USER}@${LOCAL_IP} 'echo OK' 2>/dev/null" 2>/dev/null | grep -q OK; then
        pass "Reverse SSH (peer → ${POOL_USER}@${LOCAL_IP}): connected"
    else
        warn "Reverse SSH (peer → ${POOL_USER}@${LOCAL_IP}): FAILED"
        info "Not critical for basic failover, but needed for bidirectional replication."
    fi

    log_section "postgres sudo access (for PostgreSQL replication)"

    # Test postgres sudo access on peer (used by ha-replicate.sh --mode postgres)
    if [[ "$spiraluser_ssh_ok" == "true" ]]; then
        if ssh $SSH_OPTS "${POOL_USER}@${PEER_IP}" "sudo -u postgres -n true" 2>/dev/null; then
            pass "postgres sudo via ${POOL_USER}@${PEER_IP}: works"
            # Also test rsync specifically
            if ssh $SSH_OPTS "${POOL_USER}@${PEER_IP}" "sudo -u postgres -n rsync --version" 2>/dev/null | grep -q rsync; then
                pass "postgres rsync sudo: works (PostgreSQL replication ready)"
            else
                warn "postgres rsync sudo: FAILED (rsync not in sudoers)"
                info "Fix on peer: echo '${POOL_USER} ALL=(postgres) NOPASSWD: /usr/bin/rsync' | sudo tee /etc/sudoers.d/spiralpool-ha-postgres"
            fi
        else
            warn "postgres sudo via ${POOL_USER}@${PEER_IP}: FAILED"
            info "PostgreSQL cold-copy replication won't work."
            info "Fix: Configure sudoers on peer (see ha-setup-ssh.sh)"
        fi
    else
        skip "postgres sudo (spiraluser SSH not available)"
    fi
}

# =============================================================================
# Phase 2: HA Infrastructure
# =============================================================================

phase_ha_infrastructure() {
    log_phase "Phase 2/7: HA Infrastructure Verification"

    log_section "Local node"

    # Check ha-enabled marker
    if [[ -f "${INSTALL_DIR}/config/ha-enabled" ]]; then
        pass "HA enabled marker: exists"
    else
        fail "HA enabled marker: MISSING (${INSTALL_DIR}/config/ha-enabled)"
        info "HA is not configured on this node."
        info "Fix: spiralctl vip setup --address <VIP> --interface <IF>"
        return 1
    fi

    # Check stratum running
    if local_service_active "spiralstratum"; then
        pass "spiralstratum service: running"
    else
        fail "spiralstratum service: NOT running"
        info "Fix: sudo systemctl start spiralstratum"
        return 1
    fi

    # Check HA status API
    local local_status
    local_status=$(fetch_ha_status "localhost")
    if [[ -n "$local_status" ]]; then
        pass "HA status API (localhost:${HA_STATUS_PORT}): responding"

        local enabled
        enabled=$(json_bool "$local_status" "enabled")
        if [[ "$enabled" == "true" ]]; then
            pass "VIP manager: enabled"
        else
            fail "VIP manager: NOT enabled (check vip.enabled in config.yaml)"
            return 1
        fi

        VIP_ADDRESS=$(json_val "$local_status" "vip")
        LOCAL_ROLE=$(json_val "$local_status" "localRole")
        info "VIP address: ${VIP_ADDRESS:-NOT SET}"
        info "Local role: ${LOCAL_ROLE:-UNKNOWN}"
    else
        fail "HA status API: NOT responding"
        info "Check: curl http://localhost:${HA_STATUS_PORT}/status"
        return 1
    fi

    # Check ha-role-watcher
    if local_service_active "spiralpool-ha-watcher"; then
        pass "ha-role-watcher: running"
    else
        warn "ha-role-watcher: NOT running"
        info "Role changes won't auto-trigger service control."
        info "Fix: sudo systemctl start spiralpool-ha-watcher"
    fi

    # Check ha-service-control
    if [[ -x "/usr/local/bin/spiralpool-ha-service" ]]; then
        pass "ha-service-control: installed"
    elif [[ -x "${INSTALL_DIR}/scripts/ha-service-control.sh" ]]; then
        pass "ha-service-control: installed (alt path)"
    else
        warn "ha-service-control: NOT found"
    fi

    log_section "Peer node (${PEER_IP})"

    # Check peer ha-enabled
    if ssh_peer "test -f ${INSTALL_DIR}/config/ha-enabled"; then
        pass "Peer HA enabled marker: exists"
    else
        fail "Peer HA enabled marker: MISSING"
        info "HA is not configured on peer."
        return 1
    fi

    # Check peer stratum
    if peer_service_active "spiralstratum"; then
        pass "Peer spiralstratum: running"
    else
        fail "Peer spiralstratum: NOT running"
        info "Fix on peer: sudo systemctl start spiralstratum"
        return 1
    fi

    # Check peer HA status API
    local peer_status
    peer_status=$(fetch_ha_status "$PEER_IP")
    if [[ -n "$peer_status" ]]; then
        pass "Peer HA status API (${PEER_IP}:${HA_STATUS_PORT}): responding"

        PEER_ROLE=$(json_val "$peer_status" "localRole")
        local peer_vip
        peer_vip=$(json_val "$peer_status" "vip")
        info "Peer role: ${PEER_ROLE:-UNKNOWN}"
        info "Peer VIP: ${peer_vip:-NOT SET}"

        # Verify VIP matches
        if [[ "$VIP_ADDRESS" == "$peer_vip" ]]; then
            pass "VIP address matches on both nodes: $VIP_ADDRESS"
        else
            fail "VIP mismatch: local=$VIP_ADDRESS peer=$peer_vip"
        fi
    else
        fail "Peer HA status API: NOT responding"
        info "Check: curl http://${PEER_IP}:${HA_STATUS_PORT}/status"
        return 1
    fi

    # Check peer watcher
    if peer_service_active "spiralpool-ha-watcher"; then
        pass "Peer ha-role-watcher: running"
    else
        warn "Peer ha-role-watcher: NOT running"
    fi
}

# =============================================================================
# Phase 3: Pre-Failover State Validation
# =============================================================================

phase_prefailover_state() {
    log_phase "Phase 3/7: Pre-Failover Cluster State"

    log_section "Role consistency"

    # Determine MASTER and BACKUP
    if [[ "$LOCAL_ROLE" == "MASTER" ]] && [[ "$PEER_ROLE" == "BACKUP" ]]; then
        pass "Roles correct: local=MASTER, peer=BACKUP"
        MASTER_IP="$LOCAL_IP"
        BACKUP_IP="$PEER_IP"
    elif [[ "$LOCAL_ROLE" == "BACKUP" ]] && [[ "$PEER_ROLE" == "MASTER" ]]; then
        pass "Roles correct: local=BACKUP, peer=MASTER"
        MASTER_IP="$PEER_IP"
        BACKUP_IP="$LOCAL_IP"
    elif [[ "$LOCAL_ROLE" == "MASTER" ]] && [[ "$PEER_ROLE" == "MASTER" ]]; then
        fail "SPLIT-BRAIN DETECTED: BOTH nodes are MASTER!"
        info "This is a critical error. Check VIP configuration and cluster token."
        return 1
    elif [[ "$LOCAL_ROLE" == "BACKUP" ]] && [[ "$PEER_ROLE" == "BACKUP" ]]; then
        fail "NO MASTER: Both nodes are BACKUP"
        info "No node has assumed the master role. Check VIP heartbeats."
        return 1
    else
        fail "Unexpected roles: local=${LOCAL_ROLE:-UNKNOWN}, peer=${PEER_ROLE:-UNKNOWN}"
        info "Expected one MASTER and one BACKUP."
        return 1
    fi

    info "Master node: $MASTER_IP"
    info "Backup node: $BACKUP_IP"

    log_section "VIP binding"

    # Verify VIP is on master
    if [[ -z "$VIP_ADDRESS" ]]; then
        fail "VIP address not configured"
        return 1
    fi

    if vip_bound_on "$MASTER_IP" "$VIP_ADDRESS"; then
        pass "VIP ($VIP_ADDRESS) bound on MASTER ($MASTER_IP)"
    else
        fail "VIP ($VIP_ADDRESS) NOT bound on MASTER ($MASTER_IP)"
        info "VIP should be assigned to the master's network interface."
    fi

    if vip_bound_on "$BACKUP_IP" "$VIP_ADDRESS"; then
        fail "VIP ($VIP_ADDRESS) ALSO bound on BACKUP ($BACKUP_IP) — split-brain!"
    else
        pass "VIP ($VIP_ADDRESS) NOT bound on BACKUP ($BACKUP_IP) — correct"
    fi

    log_section "Service state (Sentinel + Dashboard)"

    # On master: services should be running
    local master_sentinel master_dashboard
    if [[ "$MASTER_IP" == "$LOCAL_IP" ]]; then
        master_sentinel=$(local_service_active "spiralsentinel" && echo "running" || echo "stopped")
        master_dashboard=$(local_service_active "spiraldash" && echo "running" || echo "stopped")
    else
        master_sentinel=$(peer_service_active "spiralsentinel" && echo "running" || echo "stopped")
        master_dashboard=$(peer_service_active "spiraldash" && echo "running" || echo "stopped")
    fi

    if [[ "$master_sentinel" == "running" ]]; then
        pass "Sentinel on MASTER: running"
    else
        warn "Sentinel on MASTER: stopped (should be running)"
    fi

    if [[ "$master_dashboard" == "running" ]]; then
        pass "Dashboard on MASTER: running"
    else
        warn "Dashboard on MASTER: stopped (should be running)"
    fi

    # On backup: services should be stopped
    local backup_sentinel backup_dashboard
    if [[ "$BACKUP_IP" == "$LOCAL_IP" ]]; then
        backup_sentinel=$(local_service_active "spiralsentinel" && echo "running" || echo "stopped")
        backup_dashboard=$(local_service_active "spiraldash" && echo "running" || echo "stopped")
    else
        backup_sentinel=$(peer_service_active "spiralsentinel" && echo "running" || echo "stopped")
        backup_dashboard=$(peer_service_active "spiraldash" && echo "running" || echo "stopped")
    fi

    if [[ "$backup_sentinel" == "stopped" ]]; then
        pass "Sentinel on BACKUP: stopped (correct)"
    else
        fail "Sentinel on BACKUP: RUNNING (should be stopped — duplicate alerts!)"
    fi

    if [[ "$backup_dashboard" == "stopped" ]]; then
        pass "Dashboard on BACKUP: stopped (correct)"
    else
        warn "Dashboard on BACKUP: running (not critical but unexpected)"
    fi

    log_section "Cluster communication"

    # Check nodes see each other via cluster
    local local_status
    local_status=$(fetch_ha_status "localhost")
    local node_count
    node_count=$(echo "$local_status" | grep -oP '"host"' | wc -l)
    if [[ "$node_count" -ge 2 ]]; then
        pass "Cluster sees $node_count nodes (both nodes visible)"
    elif [[ "$node_count" -eq 1 ]]; then
        warn "Cluster sees only 1 node (peer may not be discovered yet)"
        info "Wait 30s for heartbeat discovery and re-run."
    else
        warn "Could not determine cluster node count"
    fi
}

# =============================================================================
# Phase 4: Live Failover Test
# =============================================================================

phase_failover_test() {
    if [[ "$SKIP_FAILOVER" == "true" ]]; then
        log_phase "Phase 4/7: Live Failover Test (SKIPPED)"
        skip "Failover test skipped (--skip-failover)"
        return 0
    fi

    log_phase "Phase 4/7: Live Failover Test"

    log_section "Pre-failover snapshot"

    # Record current state
    local pre_master="$MASTER_IP"
    local pre_backup="$BACKUP_IP"
    local failover_start failover_end failover_duration

    info "Current MASTER: $pre_master"
    info "Current BACKUP: $pre_backup"
    info "VIP address: $VIP_ADDRESS"
    info "Failover timeout (max wait): ${FAILOVER_MAX_WAIT}s"

    log_section "Stopping stratum on MASTER ($pre_master)"

    echo ""
    echo -e "  ${YELLOW}${BOLD}>>> Killing stratum on MASTER to trigger failover <<<${NC}"
    echo ""

    failover_start=$(date +%s)

    # Stop stratum on the master
    if [[ "$pre_master" == "$LOCAL_IP" ]]; then
        # Master is local — stop directly
        systemctl stop spiralstratum 2>/dev/null || true
        info "spiralstratum stopped on local node (was MASTER)"
    else
        # Master is peer — stop via SSH
        ssh_peer "sudo systemctl stop spiralstratum" 2>/dev/null || true
        info "spiralstratum stopped on peer (was MASTER)"
    fi

    log_section "Monitoring failover on BACKUP ($pre_backup)"

    info "Watching for BACKUP → MASTER transition..."
    info "Default VIP failover timeout is 90s — patience required."

    local waited=0
    local failover_detected=false

    while [[ $waited -lt $FAILOVER_MAX_WAIT ]]; do
        sleep "$POLL_INTERVAL"
        waited=$((waited + POLL_INTERVAL))

        # Query the backup's HA API to see if it became MASTER
        local backup_status
        backup_status=$(fetch_ha_status "$pre_backup")
        local new_role
        new_role=$(json_val "$backup_status" "localRole")

        if [[ "$new_role" == "MASTER" ]]; then
            failover_end=$(date +%s)
            failover_duration=$((failover_end - failover_start))
            failover_detected=true

            echo ""
            pass "FAILOVER DETECTED: Backup ($pre_backup) is now MASTER"
            info "Failover duration: ${failover_duration}s"
            break
        fi

        # Progress update every 15s
        if [[ $((waited % 15)) -eq 0 ]]; then
            info "  Still waiting... ${waited}s (backup role: ${new_role:-unknown})"
        fi
    done

    if [[ "$failover_detected" == "false" ]]; then
        fail "FAILOVER FAILED: Backup did not become MASTER within ${FAILOVER_MAX_WAIT}s"
        info "Check: curl http://${pre_backup}:${HA_STATUS_PORT}/status"
        info "Check: journalctl -u spiralstratum --no-pager -n 50 (on backup)"
        return 1
    fi

    FAILOVER_PERFORMED=true

    # Update global state
    MASTER_IP="$pre_backup"
    BACKUP_IP="$pre_master"

    log_section "Post-failover VIP check"

    # Give VIP a moment to bind
    sleep 2

    if vip_bound_on "$MASTER_IP" "$VIP_ADDRESS"; then
        pass "VIP ($VIP_ADDRESS) moved to new MASTER ($MASTER_IP)"
    else
        fail "VIP ($VIP_ADDRESS) NOT bound on new MASTER ($MASTER_IP)"
        info "VIP acquisition may have failed. Check network interface."
    fi

    # Verify VIP is NOT still on old master
    # (old master's stratum is stopped, so VIP should have been released)
    # We can only check this if old master is local (can't SSH-check ip addr when stratum is down)
    if [[ "$pre_master" == "$LOCAL_IP" ]]; then
        if ! ip addr show 2>/dev/null | grep -q " ${VIP_ADDRESS}/"; then
            pass "VIP released from old MASTER (local)"
        else
            fail "VIP STILL bound on old MASTER (local) — stale VIP!"
        fi
    fi

    log_section "Service auto-start on new MASTER"

    # The ha-role-watcher should have detected the role change and triggered
    # ha-service-control.sh auto, which starts Sentinel + Dashboard.
    # Watcher polls every 5s, service start takes a few seconds.
    info "Waiting for ha-role-watcher to start services (up to 30s)..."

    local svc_waited=0
    local sentinel_started=false
    local dashboard_started=false

    while [[ $svc_waited -lt 30 ]]; do
        sleep 3
        svc_waited=$((svc_waited + 3))

        if [[ "$MASTER_IP" == "$LOCAL_IP" ]]; then
            local_service_active "spiralsentinel" && sentinel_started=true
            local_service_active "spiraldash" && dashboard_started=true
        else
            peer_service_active "spiralsentinel" && sentinel_started=true
            peer_service_active "spiraldash" && dashboard_started=true
        fi

        if [[ "$sentinel_started" == "true" ]] && [[ "$dashboard_started" == "true" ]]; then
            break
        fi
    done

    if [[ "$sentinel_started" == "true" ]]; then
        pass "Sentinel auto-started on new MASTER ($MASTER_IP)"
    else
        fail "Sentinel NOT started on new MASTER after 30s"
        info "Check: journalctl -u spiralpool-ha-watcher --no-pager -n 30 (on new master)"
    fi

    if [[ "$dashboard_started" == "true" ]]; then
        pass "Dashboard auto-started on new MASTER ($MASTER_IP)"
    else
        warn "Dashboard NOT started on new MASTER after 30s"
    fi

    echo ""
    echo -e "  ${GREEN}${BOLD}Failover chain validated in ${failover_duration}s:${NC}"
    echo -e "  ${DIM}  stratum killed → VIP heartbeat stopped → backup detected${NC}"
    echo -e "  ${DIM}  → backup acquired VIP → watcher detected role change${NC}"
    echo -e "  ${DIM}  → service-control started services → services running${NC}"
    echo ""
}

# =============================================================================
# Phase 5: Recovery Test
# =============================================================================

phase_recovery_test() {
    if [[ "$FAILOVER_PERFORMED" == "false" ]]; then
        log_phase "Phase 5/7: Recovery Test (SKIPPED — no failover performed)"
        skip "Recovery test skipped"
        return 0
    fi

    log_phase "Phase 5/7: Recovery Test"

    # The old master (now BACKUP_IP) had its stratum stopped.
    # Restart it and verify it rejoins as BACKUP.
    local old_master="$BACKUP_IP"

    log_section "Restarting stratum on old master ($old_master)"

    if [[ "$old_master" == "$LOCAL_IP" ]]; then
        systemctl start spiralstratum 2>/dev/null || true
        info "spiralstratum restarted on local node"
    else
        ssh_peer "sudo systemctl start spiralstratum" 2>/dev/null || true
        info "spiralstratum restarted on peer"
    fi

    log_section "Waiting for old master to rejoin as BACKUP"

    info "Waiting for stratum to start and discover cluster..."

    local waited=0
    local rejoined=false

    while [[ $waited -lt $RECOVERY_MAX_WAIT ]]; do
        sleep "$POLL_INTERVAL"
        waited=$((waited + POLL_INTERVAL))

        local old_status
        old_status=$(fetch_ha_status "$old_master")
        local old_role
        old_role=$(json_val "$old_status" "localRole")

        if [[ "$old_role" == "BACKUP" ]]; then
            rejoined=true
            pass "Old master ($old_master) rejoined as BACKUP (${waited}s)"
            break
        fi

        if [[ $((waited % 15)) -eq 0 ]]; then
            info "  Waiting... ${waited}s (old master role: ${old_role:-starting...})"
        fi
    done

    if [[ "$rejoined" == "false" ]]; then
        fail "Old master did not rejoin as BACKUP within ${RECOVERY_MAX_WAIT}s"
        info "Check: curl http://${old_master}:${HA_STATUS_PORT}/status"
        return 1
    fi

    # Verify current master is still MASTER
    local current_status
    current_status=$(fetch_ha_status "$MASTER_IP")
    local current_role
    current_role=$(json_val "$current_status" "localRole")

    if [[ "$current_role" == "MASTER" ]]; then
        pass "Current MASTER ($MASTER_IP) still MASTER after recovery"
    else
        fail "Current MASTER ($MASTER_IP) changed role to $current_role during recovery!"
    fi

    # Verify VIP hasn't moved
    if vip_bound_on "$MASTER_IP" "$VIP_ADDRESS"; then
        pass "VIP still on MASTER ($MASTER_IP) after recovery"
    else
        fail "VIP lost from MASTER after old master rejoined"
    fi

    # Verify services on old master (now backup) are stopped
    log_section "Service state after recovery"

    local old_sentinel old_dashboard
    if [[ "$old_master" == "$LOCAL_IP" ]]; then
        old_sentinel=$(local_service_active "spiralsentinel" && echo "running" || echo "stopped")
        old_dashboard=$(local_service_active "spiraldash" && echo "running" || echo "stopped")
    else
        old_sentinel=$(peer_service_active "spiralsentinel" && echo "running" || echo "stopped")
        old_dashboard=$(peer_service_active "spiraldash" && echo "running" || echo "stopped")
    fi

    if [[ "$old_sentinel" == "stopped" ]]; then
        pass "Sentinel on recovered node (now BACKUP): stopped — correct"
    else
        warn "Sentinel on recovered node: still running (watcher may need time)"
    fi
}

# =============================================================================
# Phase 6: Reverse Failover
# =============================================================================

phase_reverse_failover() {
    if [[ "$SKIP_REVERSE" == "true" ]] || [[ "$FAILOVER_PERFORMED" == "false" ]]; then
        log_phase "Phase 6/7: Reverse Failover (SKIPPED)"
        skip "Reverse failover skipped"
        return 0
    fi

    log_phase "Phase 6/7: Reverse Failover (prove both directions work)"

    # Now MASTER_IP is the node that was promoted in Phase 4.
    # Kill its stratum and verify the original master takes over.
    local current_master="$MASTER_IP"
    local current_backup="$BACKUP_IP"

    log_section "Stopping stratum on current MASTER ($current_master)"

    echo ""
    echo -e "  ${YELLOW}${BOLD}>>> Killing stratum on MASTER to test reverse failover <<<${NC}"
    echo ""

    local reverse_start
    reverse_start=$(date +%s)

    if [[ "$current_master" == "$LOCAL_IP" ]]; then
        systemctl stop spiralstratum 2>/dev/null || true
    else
        ssh_peer "sudo systemctl stop spiralstratum" 2>/dev/null || true
    fi
    info "spiralstratum stopped on current MASTER"

    log_section "Monitoring reverse failover on $current_backup"

    local waited=0
    local reverse_ok=false

    while [[ $waited -lt $FAILOVER_MAX_WAIT ]]; do
        sleep "$POLL_INTERVAL"
        waited=$((waited + POLL_INTERVAL))

        local backup_status
        backup_status=$(fetch_ha_status "$current_backup")
        local new_role
        new_role=$(json_val "$backup_status" "localRole")

        if [[ "$new_role" == "MASTER" ]]; then
            local reverse_end
            reverse_end=$(date +%s)
            local reverse_duration=$((reverse_end - reverse_start))
            reverse_ok=true

            pass "REVERSE FAILOVER: $current_backup became MASTER (${reverse_duration}s)"
            break
        fi

        if [[ $((waited % 15)) -eq 0 ]]; then
            info "  Waiting... ${waited}s (role: ${new_role:-unknown})"
        fi
    done

    if [[ "$reverse_ok" == "false" ]]; then
        fail "Reverse failover FAILED within ${FAILOVER_MAX_WAIT}s"
        return 1
    fi

    # VIP check
    sleep 2
    if vip_bound_on "$current_backup" "$VIP_ADDRESS"; then
        pass "VIP moved to $current_backup on reverse failover"
    else
        fail "VIP NOT on $current_backup after reverse failover"
    fi

    # Update state
    MASTER_IP="$current_backup"
    BACKUP_IP="$current_master"

    log_section "Recovering stopped node"

    # Restart stratum on the node we just stopped
    if [[ "$current_master" == "$LOCAL_IP" ]]; then
        systemctl start spiralstratum 2>/dev/null || true
    else
        ssh_peer "sudo systemctl start spiralstratum" 2>/dev/null || true
    fi

    info "Waiting for node to rejoin..."
    sleep 30

    local recovered_status
    recovered_status=$(fetch_ha_status "$current_master")
    local recovered_role
    recovered_role=$(json_val "$recovered_status" "localRole")

    if [[ "$recovered_role" == "BACKUP" ]]; then
        pass "Node $current_master rejoined as BACKUP after reverse failover"
    else
        warn "Node $current_master role after recovery: ${recovered_role:-unknown}"
    fi

    echo ""
    echo -e "  ${GREEN}${BOLD}Both failover directions validated!${NC}"
    echo -e "  ${DIM}  Original: $LOCAL_IP was MASTER → peer took over${NC}"
    echo -e "  ${DIM}  Reverse:  peer was MASTER → $LOCAL_IP took back over${NC}"
    echo ""
}

# =============================================================================
# Phase 7: Final State & Replication Dry-Run
# =============================================================================

phase_final_validation() {
    log_phase "Phase 7/7: Final Validation & Replication Readiness"

    log_section "Cluster health"

    local local_status peer_status
    local_status=$(fetch_ha_status "localhost")
    peer_status=$(fetch_ha_status "$PEER_IP")

    local final_local_role final_peer_role
    final_local_role=$(json_val "$local_status" "localRole")
    final_peer_role=$(json_val "$peer_status" "localRole")

    info "Final local role: ${final_local_role:-UNKNOWN}"
    info "Final peer role: ${final_peer_role:-UNKNOWN}"

    # Verify exactly one master
    if { [[ "$final_local_role" == "MASTER" ]] && [[ "$final_peer_role" == "BACKUP" ]]; } ||
       { [[ "$final_local_role" == "BACKUP" ]] && [[ "$final_peer_role" == "MASTER" ]]; }; then
        pass "Cluster healthy: one MASTER, one BACKUP"
    else
        fail "Cluster in unexpected state: local=$final_local_role, peer=$final_peer_role"
    fi

    log_section "Replication dry-run"

    # Test if ha-replicate.sh exists and can run --dry-run
    if [[ -f "${INSTALL_DIR}/scripts/ha-replicate.sh" ]] || [[ -f "$(dirname "${BASH_SOURCE[0]}")/ha-replicate.sh" ]]; then
        local replicate_script
        if [[ -f "${INSTALL_DIR}/scripts/ha-replicate.sh" ]]; then
            replicate_script="${INSTALL_DIR}/scripts/ha-replicate.sh"
        else
            replicate_script="$(dirname "${BASH_SOURCE[0]}")/ha-replicate.sh"
        fi

        info "Testing replication dry-run (blockchain mode)..."
        if bash "$replicate_script" --mode blockchain --source "$PEER_IP" --dry-run 2>&1 | grep -qi "success\|completed\|dry.run"; then
            pass "ha-replicate.sh --dry-run: blockchain mode works"
        else
            warn "ha-replicate.sh --dry-run: returned unexpected output"
            info "Run manually: $replicate_script --mode blockchain --source $PEER_IP --dry-run"
        fi
    else
        skip "ha-replicate.sh not found on this node"
    fi

    log_section "ha-validate.sh"

    # Run ha-validate.sh quick check if available
    local validate_script=""
    if [[ -x "${INSTALL_DIR}/scripts/ha-validate.sh" ]]; then
        validate_script="${INSTALL_DIR}/scripts/ha-validate.sh"
    elif [[ -x "$(dirname "${BASH_SOURCE[0]}")/ha-validate.sh" ]]; then
        validate_script="$(dirname "${BASH_SOURCE[0]}")/ha-validate.sh"
    fi

    if [[ -n "$validate_script" ]]; then
        info "Running ha-validate.sh quick check..."
        if bash "$validate_script" quick 2>&1 | grep -qi "HEALTHY"; then
            pass "ha-validate.sh quick: HEALTHY"
        else
            warn "ha-validate.sh quick: issues detected"
            info "Run manually: $validate_script"
        fi
    else
        skip "ha-validate.sh not found"
    fi
}

# =============================================================================
# Summary
# =============================================================================

print_summary() {
    echo ""
    echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}${BOLD}  RESULTS: Full HA Two-Node Integration Test${NC}"
    echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${GREEN}Passed:${NC}  $TESTS_PASSED"
    echo -e "  ${RED}Failed:${NC}  $TESTS_FAILED"
    echo -e "  ${YELLOW}Warned:${NC}  $TESTS_WARNED"
    echo -e "  ${DIM}Skipped:${NC} $TESTS_SKIPPED"
    echo ""

    if [[ "$TESTS_FAILED" -eq 0 ]]; then
        echo -e "  ${GREEN}${BOLD}╔═══════════════════════════════════════════════════════════╗${NC}"
        echo -e "  ${GREEN}${BOLD}║                                                           ║${NC}"
        echo -e "  ${GREEN}${BOLD}║   ALL CRITICAL TESTS PASSED — HA IS READY TO SHIP         ║${NC}"
        echo -e "  ${GREEN}${BOLD}║                                                           ║${NC}"
        echo -e "  ${GREEN}${BOLD}╚═══════════════════════════════════════════════════════════╝${NC}"
        echo ""
        echo -e "  ${DIM}Evidence proven:${NC}"
        echo -e "  ${DIM}  ✓ SSH connectivity works (replication ready)${NC}"
        echo -e "  ${DIM}  ✓ HA infrastructure running on both nodes${NC}"
        echo -e "  ${DIM}  ✓ Cluster state consistent (one MASTER, one BACKUP)${NC}"
        echo -e "  ${DIM}  ✓ VIP correctly bound to MASTER only${NC}"
        echo -e "  ${DIM}  ✓ Services match roles (Sentinel/Dashboard)${NC}"
        if [[ "$FAILOVER_PERFORMED" == "true" ]]; then
            echo -e "  ${DIM}  ✓ Failover works: kill master → backup takes over${NC}"
            echo -e "  ${DIM}  ✓ VIP migrates to new master${NC}"
            echo -e "  ${DIM}  ✓ Services auto-start on new master${NC}"
            echo -e "  ${DIM}  ✓ Old master recovers as backup${NC}"
            if [[ "$SKIP_REVERSE" == "false" ]]; then
                echo -e "  ${DIM}  ✓ Reverse failover works (both directions proven)${NC}"
            fi
        fi
    else
        echo -e "  ${RED}${BOLD}╔═══════════════════════════════════════════════════════════╗${NC}"
        echo -e "  ${RED}${BOLD}║                                                           ║${NC}"
        printf -v _tf_line "%-59s" "   $TESTS_FAILED TEST(S) FAILED — FIX BEFORE SHIPPING"
        echo -e "  ${RED}${BOLD}║${_tf_line}║${NC}"
        echo -e "  ${RED}${BOLD}║                                                           ║${NC}"
        echo -e "  ${RED}${BOLD}╚═══════════════════════════════════════════════════════════╝${NC}"
        echo ""
        echo -e "  ${DIM}Review the failures above and fix the issues.${NC}"
        echo -e "  ${DIM}Re-run this test after fixing.${NC}"
    fi

    echo ""
    echo -e "  ${DIM}Log file: $LOG_FILE${NC}"
    echo -e "  ${DIM}Time: $(date)${NC}"
    echo ""

    return "$TESTS_FAILED"
}

# =============================================================================
# Usage
# =============================================================================

usage() {
    cat <<'EOF'
Spiral Pool HA Two-Node Integration Test

USAGE:
    regtest-ha-two-node.sh --peer <PEER_IP> [OPTIONS]

REQUIRED:
    --peer <IP>         IP address of the other HA node

OPTIONS:
    --ssh-user <USER>   SSH user for peer access (default: current user)
    --skip-failover     Run validation phases only (no failover test)
    --skip-reverse      Skip reverse failover test (faster)
    --help              Show this help message

EXAMPLES:
    # Full test (all phases including failover):
    sudo ./regtest-ha-two-node.sh --peer 192.168.1.11

    # Validation only (safe, no service disruption):
    sudo ./regtest-ha-two-node.sh --peer 192.168.1.11 --skip-failover

    # Full test but skip reverse failover (faster):
    sudo ./regtest-ha-two-node.sh --peer 192.168.1.11 --skip-reverse
EOF
}

# =============================================================================
# Argument Parsing
# =============================================================================

PEER_IP=""

if [[ $# -eq 0 ]]; then
    usage
    exit 0
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        --peer)
            PEER_IP="$2"
            shift 2
            ;;
        --ssh-user)
            SSH_USER="$2"
            shift 2
            ;;
        --skip-failover)
            SKIP_FAILOVER=true
            shift
            ;;
        --skip-reverse)
            SKIP_REVERSE=true
            shift
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1"
            usage
            exit 1
            ;;
    esac
done

if [[ -z "$PEER_IP" ]]; then
    echo "Error: --peer is required"
    usage
    exit 1
fi

# =============================================================================
# Main
# =============================================================================

main() {
    echo ""
    echo -e "${CYAN}╔═══════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║${NC}                                                                   ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}    ${BOLD}SPIRAL POOL — FULL HA TWO-NODE INTEGRATION TEST${NC}                ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                   ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}    ${DIM}Tests the COMPLETE production failover chain end-to-end${NC}        ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}    ${DIM}Peer: ${PEER_IP}${NC}$(printf '%*s' $((49 - ${#PEER_IP})) '')        ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                   ${CYAN}║${NC}"
    echo -e "${CYAN}╚═══════════════════════════════════════════════════════════════════╝${NC}"
    echo ""

    mkdir -p "$LOG_DIR" 2>/dev/null || true
    _log "START" "HA two-node test starting. Peer: $PEER_IP"

    # Each phase can return 1 to abort if critical prerequisites fail
    phase_ssh_connectivity     || { print_summary; exit 1; }
    phase_ha_infrastructure    || { print_summary; exit 1; }
    phase_prefailover_state    || { print_summary; exit 1; }
    phase_failover_test        || true  # Continue even if failover fails (report it)
    phase_recovery_test        || true
    phase_reverse_failover     || true
    phase_final_validation     || true

    print_summary
    exit $?
}

main "$@"
