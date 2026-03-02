#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool HA Validation Script
# Comprehensive testing of all HA components and scenarios
#
# Usage:
#   ha-validate.sh              Run all tests
#   ha-validate.sh quick        Quick health check only
#   ha-validate.sh component    Test specific component (vip|postgres|services|watcher|cluster)
#   ha-validate.sh failover     Test failover scenario (CAUTION: triggers actual failover)
#
# This script validates:
#   1. VIP Manager - acquisition, heartbeat, status API
#   2. PostgreSQL HA - Patroni, replication lag, promotion readiness
#   3. Service Control - role detection, service state consistency
#   4. HA Watcher - running, detecting roles correctly
#   5. Cluster Communication - token auth, node discovery, etcd
#   6. Failover Scenarios - simulated and actual failover tests
#

# NOTE: Do NOT use set -e — test functions return non-zero for failures,
# which is expected behavior in a validation tool, not an error.

# Configuration
INSTALL_DIR="/spiralpool"
HA_STATUS_PORT=5354
PATRONI_API_PORT=8008
ETCD_PORT=2379
VIP_DISCOVERY_PORT=5363

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
WHITE='\033[1;37m'
DIM='\033[2m'
BOLD='\033[1m'
NC='\033[0m'

# Test counters
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_WARNED=0
TESTS_SKIPPED=0

# =============================================================================
# Utility Functions
# =============================================================================

log_header() {
    echo ""
    echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}${BOLD}  $1${NC}"
    echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════════════════════════${NC}"
    echo ""
}

log_section() {
    echo ""
    echo -e "${WHITE}${BOLD}▶ $1${NC}"
    echo -e "${DIM}───────────────────────────────────────────────────────────────────────────────${NC}"
}

test_pass() {
    echo -e "  ${GREEN}✓${NC} $1"
    TESTS_PASSED=$((TESTS_PASSED + 1))
}

test_fail() {
    echo -e "  ${RED}✗${NC} $1"
    TESTS_FAILED=$((TESTS_FAILED + 1))
}

test_warn() {
    echo -e "  ${YELLOW}⚠${NC} $1"
    TESTS_WARNED=$((TESTS_WARNED + 1))
}

test_skip() {
    echo -e "  ${DIM}○${NC} $1 ${DIM}(skipped)${NC}"
    TESTS_SKIPPED=$((TESTS_SKIPPED + 1))
}

test_info() {
    echo -e "  ${BLUE}ℹ${NC} $1"
}

# Check if a command exists
cmd_exists() {
    command -v "$1" &>/dev/null
}

# Check if a service is active
service_active() {
    systemctl is-active --quiet "$1" 2>/dev/null
}

# Check if a TCP port is listening (word-boundary match to prevent suffix collision)
port_listening() {
    local port="$1"
    ss -tlnp 2>/dev/null | grep -qE ":${port}( |$)" || netstat -tlnp 2>/dev/null | grep -qE ":${port}( |$)"
}

# Check if a UDP port is listening
udp_port_listening() {
    local port="$1"
    ss -ulnp 2>/dev/null | grep -qE ":${port}( |$)" || netstat -ulnp 2>/dev/null | grep -qE ":${port}( |$)"
}

# Fetch JSON from URL (validates HTTP 2xx status)
fetch_json() {
    local url="$1"
    local response http_code body
    response=$(curl -s --connect-timeout 3 --max-time 5 -w "\n%{http_code}" "$url" 2>/dev/null) || { echo ""; return 1; }
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')
    # Only return body for 2xx responses
    if [[ "$http_code" =~ ^2[0-9][0-9]$ ]]; then
        echo "$body"
    else
        echo ""
        return 1
    fi
}

# Extract value from JSON — uses jq if available, falls back to grep
json_value() {
    local json="$1"
    local key="$2"
    if command -v jq &>/dev/null; then
        echo "$json" | jq -r ".${key} // empty" 2>/dev/null | head -1
    else
        # Fallback: escape regex metacharacters in key before using in grep
        local escaped_key
        escaped_key=$(printf '%s' "$key" | sed 's/[[\.*^$/+?(){}|]/\\&/g')
        echo "$json" | grep -oP "\"${escaped_key}\":\s*\"?\K[^\",}]+" 2>/dev/null | head -1
    fi
}

json_bool() {
    local json="$1"
    local key="$2"
    if command -v jq &>/dev/null; then
        echo "$json" | jq -r "if .${key} then \"true\" elif .${key} == false then \"false\" else empty end" 2>/dev/null | head -1
    else
        local escaped_key
        escaped_key=$(printf '%s' "$key" | sed 's/[[\.*^$/+?(){}|]/\\&/g')
        echo "$json" | grep -oP "\"${escaped_key}\":\s*\K(true|false)" 2>/dev/null | head -1
    fi
}

# =============================================================================
# Test: HA Enabled Check
# =============================================================================

test_ha_enabled() {
    log_section "HA Mode Detection"

    # Check for HA enabled marker
    if [[ -f "$INSTALL_DIR/config/ha-enabled" ]]; then
        test_pass "HA enabled marker exists"
    else
        test_warn "HA enabled marker not found ($INSTALL_DIR/config/ha-enabled)"
        test_info "HA may not be configured on this node"
        return 1
    fi

    # Check HA config in stratum config
    if [[ -f "$INSTALL_DIR/config/config.yaml" ]]; then
        if grep -q "ha:" "$INSTALL_DIR/config/config.yaml" && grep -q "enabled: true" "$INSTALL_DIR/config/config.yaml"; then
            test_pass "HA enabled in stratum config"
        else
            test_warn "HA not enabled in stratum config"
        fi
    fi

    return 0
}

# =============================================================================
# Test: VIP Manager
# =============================================================================

test_vip_manager() {
    log_section "VIP Manager"

    # Check if stratum is running (VIP manager is part of stratum)
    if service_active "spiralstratum"; then
        test_pass "Stratum service running (VIP manager active)"
    else
        test_fail "Stratum service not running"
        test_info "VIP manager requires stratum to be running"
        return 1
    fi

    # Check VIP status API
    local status_json
    status_json=$(fetch_json "http://localhost:${HA_STATUS_PORT}/status")

    if [[ -z "$status_json" ]]; then
        test_fail "VIP status API not responding (port $HA_STATUS_PORT)"
        return 1
    else
        test_pass "VIP status API responding"
    fi

    # Parse status
    local ha_enabled=$(json_bool "$status_json" "enabled")
    local local_role=$(json_value "$status_json" "localRole")
    local vip=$(json_value "$status_json" "vip")
    local master_host=$(json_value "$status_json" "masterHost")
    local node_count
    if command -v jq &>/dev/null; then
        node_count=$(echo "$status_json" | jq '.nodes | length' 2>/dev/null || echo 0)
    else
        node_count=$(echo "$status_json" | grep -oP '"priority"' | wc -l)
    fi

    if [[ "$ha_enabled" == "true" ]]; then
        test_pass "HA cluster enabled"
    else
        test_warn "HA cluster not enabled in VIP manager"
    fi

    test_info "Local role: ${local_role:-UNKNOWN}"
    test_info "VIP address: ${vip:-NOT_SET}"
    test_info "Master host: ${master_host:-UNKNOWN}"
    test_info "Nodes in cluster: ${node_count:-0}"

    # Check if VIP is configured
    if [[ -n "$vip" ]] && [[ "$vip" != "null" ]]; then
        test_pass "VIP address configured: $vip"

        # Check if VIP is bound to this node (if we're master)
        if [[ "$local_role" == "MASTER" ]]; then
            if ip addr show | grep -qF " ${vip}/"; then
                test_pass "VIP bound to this node (we are MASTER)"
            else
                test_fail "VIP not bound but we claim to be MASTER"
            fi
        else
            if ip addr show | grep -qF " ${vip}/"; then
                test_warn "VIP bound but we are not MASTER (role: $local_role)"
            else
                test_pass "VIP not bound (correct for $local_role role)"
            fi
        fi
    else
        test_warn "VIP address not configured"
    fi

    # Check discovery port (UDP, not TCP)
    if udp_port_listening "$VIP_DISCOVERY_PORT"; then
        test_pass "VIP discovery port listening (UDP $VIP_DISCOVERY_PORT)"
    else
        test_warn "VIP discovery port not listening"
    fi

    return 0
}

# =============================================================================
# Test: PostgreSQL HA (Patroni)
# =============================================================================

test_postgresql_ha() {
    log_section "PostgreSQL HA (Patroni)"

    # Check if Patroni is running
    if service_active "patroni"; then
        test_pass "Patroni service running"
    else
        test_warn "Patroni service not running"
        test_info "Checking for standalone PostgreSQL..."

        if service_active "postgresql"; then
            test_pass "Standalone PostgreSQL running (no Patroni)"
            test_info "PostgreSQL HA via Patroni not configured"
            return 0
        else
            test_fail "Neither Patroni nor PostgreSQL running"
            return 1
        fi
    fi

    # Check Patroni API
    local patroni_json
    patroni_json=$(fetch_json "http://localhost:${PATRONI_API_PORT}/patroni")

    if [[ -z "$patroni_json" ]]; then
        test_fail "Patroni API not responding (port $PATRONI_API_PORT)"
    else
        test_pass "Patroni API responding"

        local pg_role=$(json_value "$patroni_json" "role")
        local pg_state=$(json_value "$patroni_json" "state")
        local timeline=$(json_value "$patroni_json" "timeline")

        test_info "PostgreSQL role: ${pg_role:-unknown}"
        test_info "PostgreSQL state: ${pg_state:-unknown}"
        test_info "Timeline: ${timeline:-unknown}"

        if [[ "$pg_state" == "running" ]]; then
            test_pass "PostgreSQL state: running"
        else
            test_warn "PostgreSQL state: $pg_state"
        fi
    fi

    # Check etcd (required for Patroni)
    if service_active "etcd"; then
        test_pass "etcd service running"

        # Check etcd health
        if cmd_exists etcdctl; then
            local etcd_health
            etcd_health=$(ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 || echo "unhealthy")
            if echo "$etcd_health" | grep -q "is healthy"; then
                test_pass "etcd cluster healthy"

                # Check etcd member count (quorum requires majority of members)
                local member_count
                member_count=$(ETCDCTL_API=3 etcdctl --command-timeout=5s member list 2>/dev/null | wc -l)
                if [[ "${member_count:-0}" -ge 2 ]]; then
                    test_pass "etcd cluster has $member_count members (quorum possible)"
                elif [[ "${member_count:-0}" -eq 1 ]]; then
                    test_warn "etcd has only 1 member (no quorum if this node fails)"
                else
                    test_fail "etcd member list returned 0 members (command may have failed)"
                fi
            else
                test_warn "etcd health check failed"
                test_info "etcd output: $(echo "$etcd_health" | head -1)"
            fi
        fi
    else
        test_warn "etcd service not running"
    fi

    # Check replication status via SQL (with timeout to prevent hangs)
    if cmd_exists psql; then
        # Verify sudo -u postgres works before running queries
        if ! timeout 5 sudo -u postgres psql -t -c "SELECT 1;" &>/dev/null; then
            test_warn "Cannot connect to PostgreSQL via sudo -u postgres (password required or PG down)"
        else
            local is_in_recovery
            is_in_recovery=$(timeout 5 sudo -u postgres psql -t -c "SELECT pg_is_in_recovery();" 2>&1 | tr -d ' ')

            if [[ "$is_in_recovery" == "f" ]]; then
                test_pass "PostgreSQL is PRIMARY (not in recovery)"

                # Check for replicas
                local replica_count
                replica_count=$(timeout 5 sudo -u postgres psql -t -c "SELECT count(*) FROM pg_stat_replication;" 2>&1 | tr -d ' ')
                test_info "Connected replicas: ${replica_count:-0}"

                if [[ "${replica_count:-0}" -gt 0 ]]; then
                    test_pass "Replication active ($replica_count replicas connected)"

                    # Check replication lag
                    local max_lag
                    max_lag=$(timeout 5 sudo -u postgres psql -t -c "SELECT COALESCE(MAX(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)), 0) FROM pg_stat_replication;" 2>&1 | tr -d ' ')
                    test_info "Max replication lag: ${max_lag:-0} bytes"

                    if [[ ! "${max_lag:-0}" =~ ^[0-9]+$ ]]; then
                        test_warn "Could not parse replication lag (got: ${max_lag:0:40})"
                    elif [[ "${max_lag:-0}" -lt 1048576 ]]; then  # 1MB threshold
                        test_pass "Replication lag acceptable (<1MB)"
                    else
                        test_warn "Replication lag high: ${max_lag} bytes"
                    fi
                fi
            elif [[ "$is_in_recovery" == "t" ]]; then
                test_pass "PostgreSQL is REPLICA (in recovery mode)"

                # Check upstream connection
                local upstream
                upstream=$(timeout 5 sudo -u postgres psql -t -c "SELECT conninfo FROM pg_stat_wal_receiver;" 2>&1 | head -1)
                if [[ -n "$upstream" ]] && [[ "$upstream" != *"ERROR"* ]]; then
                    test_pass "Connected to upstream primary"
                else
                    test_warn "Not connected to upstream primary"
                fi
            else
                test_warn "Could not determine PostgreSQL role (got: ${is_in_recovery:0:40})"
            fi
        fi
    else
        test_skip "psql not available for detailed PostgreSQL checks"
    fi

    return 0
}

# =============================================================================
# Test: Service Control
# =============================================================================

test_service_control() {
    log_section "Service Control (Sentinel/Dashboard)"

    # Get current HA role
    local status_json
    status_json=$(fetch_json "http://localhost:${HA_STATUS_PORT}/status")
    local ha_enabled=$(json_bool "$status_json" "enabled")
    local local_role=$(json_value "$status_json" "localRole")

    if [[ "$ha_enabled" != "true" ]]; then
        local_role="STANDALONE"
    fi

    test_info "Current role: ${local_role:-UNKNOWN}"

    # Check service states
    local sentinel_running=false
    local dashboard_running=false

    if service_active "spiralsentinel"; then
        sentinel_running=true
        test_info "Sentinel: running"
    else
        test_info "Sentinel: stopped"
    fi

    if service_active "spiraldash"; then
        dashboard_running=true
        test_info "Dashboard: running"
    else
        test_info "Dashboard: stopped"
    fi

    # Validate service state matches role
    case "$local_role" in
        MASTER|STANDALONE)
            # Services SHOULD be running
            if [[ "$sentinel_running" == "true" ]]; then
                test_pass "Sentinel running (correct for $local_role)"
            else
                test_warn "Sentinel not running but should be ($local_role)"
            fi

            if [[ "$dashboard_running" == "true" ]]; then
                test_pass "Dashboard running (correct for $local_role)"
            else
                test_warn "Dashboard not running but should be ($local_role)"
            fi
            ;;
        BACKUP|OBSERVER)
            # Services should NOT be running
            if [[ "$sentinel_running" == "false" ]]; then
                test_pass "Sentinel stopped (correct for $local_role)"
            else
                test_fail "Sentinel running but should be stopped ($local_role)"
                test_info "This could cause duplicate alerts!"
            fi

            if [[ "$dashboard_running" == "false" ]]; then
                test_pass "Dashboard stopped (correct for $local_role)"
            else
                test_warn "Dashboard running on $local_role (not critical but unexpected)"
            fi
            ;;
        *)
            test_warn "Unknown role: $local_role - cannot validate service state"
            ;;
    esac

    # Check service control script
    if [[ -x "/usr/local/bin/spiralpool-ha-service" ]]; then
        test_pass "HA service control script installed"

        # Verify it can detect the role (with timeout to prevent hangs)
        local detected_role service_output
        service_output=$(timeout 10 /usr/local/bin/spiralpool-ha-service status 2>&1) || true
        detected_role=$(echo "$service_output" | grep "Cluster Role:" | awk '{print $NF}')
        if [[ -n "$detected_role" ]]; then
            test_pass "Service control can detect role: $detected_role"

            if [[ -n "$local_role" ]] && { [[ "$detected_role" == "$local_role" ]] || [[ "$detected_role" == "STANDALONE" && "$local_role" == "UNKNOWN" ]]; }; then
                test_pass "Service control role matches VIP role"
            elif [[ -z "$local_role" ]] && [[ "$detected_role" == "STANDALONE" ]]; then
                test_pass "Service control role matches VIP role (both standalone)"
            elif [[ -n "$local_role" ]]; then
                test_warn "Role mismatch: VIP=$local_role, ServiceControl=$detected_role"
            fi
        else
            test_warn "Service control could not detect role (output may be from old version)"
        fi
    else
        test_fail "HA service control script not installed"
    fi

    return 0
}

# =============================================================================
# Test: HA Watcher
# =============================================================================

test_ha_watcher() {
    log_section "HA Role Watcher"

    # Check if watcher service exists and is running
    if systemctl list-unit-files --no-pager 2>/dev/null | grep -q "spiralpool-ha-watcher"; then
        test_pass "HA watcher service installed"

        if service_active "spiralpool-ha-watcher"; then
            test_pass "HA watcher service running"
        else
            test_warn "HA watcher service not running"
            test_info "The watcher detects role changes and triggers service control"
        fi
    else
        test_warn "HA watcher service not installed"
        test_info "Install with: install.sh (HA mode)"
    fi

    # Check watcher script
    if [[ -x "$INSTALL_DIR/scripts/ha-role-watcher.sh" ]]; then
        test_pass "HA watcher script exists"
    else
        test_warn "HA watcher script not found"
    fi

    # Check watcher state file
    if [[ -f "$INSTALL_DIR/config/.ha-watcher-state" ]]; then
        local saved_role
        saved_role=$(cat "$INSTALL_DIR/config/.ha-watcher-state" 2>/dev/null)
        test_pass "Watcher state file exists"
        test_info "Saved role: $saved_role"
    else
        test_info "Watcher state file not found (first run or not configured)"
    fi

    # Check recovery scripts (needed for automated failover)
    local recovery_scripts=(
        "$INSTALL_DIR/scripts/etcd-quorum-recover.sh"
        "$INSTALL_DIR/scripts/patroni-force-bootstrap.sh"
        "$INSTALL_DIR/scripts/ha-service-control.sh"
    )
    for script in "${recovery_scripts[@]}"; do
        local script_name
        script_name=$(basename "$script")
        if [[ -x "$script" ]]; then
            test_pass "Recovery script exists: $script_name"
        else
            test_warn "Recovery script missing or not executable: $script_name"
        fi
    done

    # Check sudoers access for recovery scripts (non-destructive test)
    if sudo -n true 2>/dev/null; then
        test_info "sudo access available (recovery scripts can run)"
    else
        # Check specific scripts via sudo -l
        local can_sudo=false
        if sudo -l 2>/dev/null | grep -q "etcd-quorum-recover\|patroni-force-bootstrap"; then
            can_sudo=true
        fi
        if [[ "$can_sudo" == "true" ]]; then
            test_pass "Sudoers entries exist for recovery scripts"
        else
            test_warn "Cannot verify sudoers access for recovery scripts"
        fi
    fi

    # Check watcher logs
    if [[ -f "$INSTALL_DIR/logs/ha-role-watcher.log" ]]; then
        local last_log
        last_log=$(tail -1 "$INSTALL_DIR/logs/ha-role-watcher.log" 2>/dev/null)
        if [[ ${#last_log} -gt 80 ]]; then
            test_info "Last watcher log: ${last_log:0:80}... (truncated)"
        else
            test_info "Last watcher log: $last_log"
        fi
    fi

    return 0
}

# =============================================================================
# Test: Cluster Communication
# =============================================================================

test_cluster_communication() {
    log_section "Cluster Communication"

    # Get cluster info from VIP status
    local status_json
    status_json=$(fetch_json "http://localhost:${HA_STATUS_PORT}/status")

    if [[ -z "$status_json" ]]; then
        test_skip "VIP status API not available"
        return 1
    fi

    # Check cluster token
    local cluster_token=$(json_value "$status_json" "clusterToken")
    if [[ -n "$cluster_token" ]] && [[ "$cluster_token" != "null" ]]; then
        # Don't show full token for security
        test_pass "Cluster token configured (${cluster_token:0:12}...)"
    else
        test_warn "Cluster token not set"
    fi

    # Check nodes in cluster
    local nodes_json
    nodes_json=$(fetch_json "http://localhost:${HA_STATUS_PORT}/nodes")

    if [[ -n "$nodes_json" ]]; then
        local node_count
        if command -v jq &>/dev/null; then
            node_count=$(echo "$nodes_json" | jq 'if type == "array" then length else 0 end' 2>/dev/null || echo 0)
        else
            node_count=$(echo "$nodes_json" | grep -oP '"host"' | wc -l)
        fi
        test_info "Nodes in cluster: $node_count"

        if [[ "$node_count" -ge 2 ]]; then
            test_pass "Multiple nodes in cluster"
        elif [[ "$node_count" -eq 1 ]]; then
            test_warn "Only 1 node in cluster (no failover possible)"
        else
            test_warn "No nodes reported in cluster"
        fi

        # List nodes (use process substitution to avoid subshell variable scope issues)
        while IFS= read -r host; do
            test_info "  Node: $host"
        done < <(
            if command -v jq &>/dev/null; then
                echo "$nodes_json" | jq -r '.[].host // empty' 2>/dev/null
            else
                echo "$nodes_json" | grep -oP '"host":"[^"]*"' | cut -d'"' -f4
            fi
        )
    fi

    # Check peer connectivity (test ALL peers, not just the first)
    local local_ip
    local_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    local all_peer_ips=""
    if command -v jq &>/dev/null; then
        all_peer_ips=$(echo "$status_json" | jq -r '.nodes[]?.host // empty' 2>/dev/null | grep -v "^${local_ip}$")
    else
        all_peer_ips=$(echo "$status_json" | grep -oP '"nodes":\[.*?\]' | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | grep -v "^${local_ip}$")
    fi

    if [[ -n "$all_peer_ips" ]]; then
        local peer_count=0
        local peer_ip
        while IFS= read -r peer_ip; do
            [[ -z "$peer_ip" ]] && continue
            [[ "$peer_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
            peer_count=$((peer_count + 1))
            test_info "Testing connectivity to peer ${peer_count}: ${peer_ip}"

            # Ping test (with deadline to prevent hangs)
            if timeout 5 ping -c 1 -W 2 "$peer_ip" &>/dev/null; then
                test_pass "Peer ${peer_ip} reachable via ping"
            else
                test_warn "Peer ${peer_ip} not reachable via ping (may be firewall)"
            fi

            # VIP API test
            local peer_status
            peer_status=$(fetch_json "http://${peer_ip}:${HA_STATUS_PORT}/status")
            if [[ -n "$peer_status" ]]; then
                test_pass "Peer ${peer_ip} VIP API reachable"
                local peer_role
                peer_role=$(json_value "$peer_status" "localRole")
                test_info "Peer ${peer_ip} role: $peer_role"
            else
                test_warn "Peer ${peer_ip} VIP API not reachable"
            fi

            # PostgreSQL connectivity (if Patroni running locally)
            if port_listening 5432; then
                if timeout 3 bash -c "echo >/dev/tcp/\"$peer_ip\"/5432" 2>/dev/null; then
                    test_pass "Peer ${peer_ip} PostgreSQL port reachable"
                else
                    test_warn "Peer ${peer_ip} PostgreSQL port not reachable"
                fi
            fi
        done <<< "$all_peer_ips"

        if [[ $peer_count -eq 0 ]]; then
            test_info "No peer IPs detected (single node or not configured)"
        fi
    else
        test_info "No peer IPs detected (single node or not configured)"
    fi

    return 0
}

# =============================================================================
# Test: Failover Readiness
# =============================================================================

test_failover_readiness() {
    log_section "Failover Readiness"

    # Check all critical components
    local ready=true

    # VIP Manager
    if service_active "spiralstratum"; then
        test_pass "VIP Manager: Ready"
    else
        test_fail "VIP Manager: Not ready (stratum not running)"
        ready=false
    fi

    # PostgreSQL
    if service_active "patroni" || service_active "postgresql"; then
        test_pass "PostgreSQL: Ready"
    else
        test_fail "PostgreSQL: Not ready"
        ready=false
    fi

    # Service Control
    if [[ -x "/usr/local/bin/spiralpool-ha-service" ]]; then
        test_pass "Service Control: Ready"
    else
        test_warn "Service Control: Script missing"
    fi

    # HA Watcher (for backup node to detect promotion)
    if service_active "spiralpool-ha-watcher"; then
        test_pass "HA Watcher: Ready"
    else
        test_warn "HA Watcher: Not running (role changes may not trigger service control)"
    fi

    # Keepalived (VIP management)
    if service_active "keepalived"; then
        test_pass "Keepalived: Ready"
    else
        test_warn "Keepalived: Not running (VIP failover disabled)"
    fi

    # etcd (Patroni consensus)
    if service_active "etcd"; then
        test_pass "etcd: Ready"
    else
        test_warn "etcd: Not running (Patroni cannot reach consensus)"
        ready=false
    fi

    # Check if this node can be promoted
    local status_json
    status_json=$(fetch_json "http://localhost:${HA_STATUS_PORT}/status")
    local local_role
    local_role=$(json_value "$status_json" "localRole")

    if [[ "$local_role" == "BACKUP" ]]; then
        test_info "This is a BACKUP node - can be promoted to MASTER"

        # Check PostgreSQL replica status
        if cmd_exists psql; then
            local is_in_recovery
            is_in_recovery=$(timeout 5 sudo -u postgres psql -t -c "SELECT pg_is_in_recovery();" 2>&1 | tr -d ' ')
            if [[ "$is_in_recovery" == "t" ]]; then
                test_pass "PostgreSQL is replica (ready for promotion)"
            else
                test_warn "PostgreSQL is not in recovery mode"
            fi
        fi
    elif [[ "$local_role" == "MASTER" ]]; then
        test_info "This is the MASTER node - failover would demote this node"
    fi

    echo ""
    if [[ "$ready" == "true" ]]; then
        echo -e "  ${GREEN}${BOLD}✓ System is ready for failover${NC}"
    else
        echo -e "  ${RED}${BOLD}✗ System is NOT ready for failover${NC}"
    fi

    return 0
}

# =============================================================================
# Test: Simulate Failover (Dry Run)
# =============================================================================

test_failover_simulation() {
    log_section "Failover Simulation (Dry Run)"

    test_info "This simulates what would happen during failover WITHOUT actually triggering it"

    local status_json
    status_json=$(fetch_json "http://localhost:${HA_STATUS_PORT}/status")
    local local_role=$(json_value "$status_json" "localRole")
    local vip=$(json_value "$status_json" "vip")

    echo ""
    echo -e "  ${WHITE}Current State:${NC}"
    echo -e "    Role: $local_role"
    echo -e "    VIP: $vip"
    echo ""

    if [[ "$local_role" == "MASTER" ]]; then
        echo -e "  ${WHITE}If this node fails:${NC}"
        echo -e "    1. VIP heartbeat stops"
        echo -e "    2. Backup node detects missing heartbeat (within ~10s)"
        echo -e "    3. Backup acquires VIP"
        echo -e "    4. Backup's HA watcher detects role change"
        echo -e "    5. spiralpool-ha-service auto runs:"
        echo -e "       - Starts Sentinel on backup"
        echo -e "       - Starts Dashboard on backup"
        echo -e "    6. PostgreSQL: Patroni promotes replica to primary"
        echo -e "    7. Miners reconnect to VIP (now on backup)"
    elif [[ "$local_role" == "BACKUP" ]]; then
        echo -e "  ${WHITE}If MASTER node fails:${NC}"
        echo -e "    1. This node detects missing MASTER heartbeat"
        echo -e "    2. This node acquires VIP"
        echo -e "    3. HA watcher detects: BACKUP → MASTER"
        echo -e "    4. spiralpool-ha-service auto runs:"
        echo -e "       - Starts Sentinel"
        echo -e "       - Starts Dashboard"
        echo -e "    5. PostgreSQL: Patroni promotes this replica to primary"
        echo -e "    6. Miners reconnect to VIP (now on this node)"
    fi

    echo ""
    test_info "To actually test failover: spiralctl vip failover"
    test_info "To test service control: spiralpool-ha-service auto"

    return 0
}

# =============================================================================
# Quick Health Check
# =============================================================================

quick_health_check() {
    log_header "Quick HA Health Check"

    local all_good=true

    # VIP Status
    local status_json
    status_json=$(fetch_json "http://localhost:${HA_STATUS_PORT}/status")

    if [[ -n "$status_json" ]]; then
        local ha_enabled=$(json_bool "$status_json" "enabled")
        local local_role=$(json_value "$status_json" "localRole")
        local vip=$(json_value "$status_json" "vip")

        echo -e "  VIP Status API:  ${GREEN}●${NC} OK"
        echo -e "  HA Enabled:      $(if [[ "$ha_enabled" == "true" ]]; then echo "${GREEN}Yes${NC}"; else echo "${YELLOW}No${NC}"; fi)"
        echo -e "  Local Role:      ${BOLD}$local_role${NC}"
        echo -e "  VIP Address:     $vip"
    else
        echo -e "  VIP Status API:  ${RED}●${NC} Not responding"
        all_good=false
    fi

    # PostgreSQL
    if service_active "patroni"; then
        echo -e "  Patroni:         ${GREEN}●${NC} Running"
    elif service_active "postgresql"; then
        echo -e "  PostgreSQL:      ${GREEN}●${NC} Running (standalone)"
    else
        echo -e "  PostgreSQL:      ${RED}●${NC} Not running"
        all_good=false
    fi

    # Services
    if service_active "spiralstratum"; then
        echo -e "  Stratum:         ${GREEN}●${NC} Running"
    else
        echo -e "  Stratum:         ${RED}●${NC} Not running"
        all_good=false
    fi

    if service_active "spiralsentinel"; then
        echo -e "  Sentinel:        ${GREEN}●${NC} Running"
    else
        echo -e "  Sentinel:        ${DIM}○${NC} Stopped"
    fi

    if service_active "spiraldash"; then
        echo -e "  Dashboard:       ${GREEN}●${NC} Running"
    else
        echo -e "  Dashboard:       ${DIM}○${NC} Stopped"
    fi

    if service_active "spiralpool-ha-watcher"; then
        echo -e "  HA Watcher:      ${GREEN}●${NC} Running"
    else
        echo -e "  HA Watcher:      ${DIM}○${NC} Not running"
    fi

    echo ""
    if [[ "$all_good" == "true" ]]; then
        echo -e "  ${GREEN}${BOLD}Overall: HEALTHY${NC}"
    else
        echo -e "  ${RED}${BOLD}Overall: ISSUES DETECTED${NC}"
    fi
    echo ""
}

# =============================================================================
# Full Test Suite
# =============================================================================

run_full_tests() {
    log_header "Spiral Pool HA Validation"
    echo -e "  Running comprehensive HA tests..."
    echo -e "  Time: $(date)"
    echo ""

    # Check if HA is enabled first
    if ! test_ha_enabled; then
        echo ""
        echo -e "${YELLOW}HA does not appear to be enabled on this node.${NC}"
        echo -e "${DIM}Some tests will be skipped.${NC}"
        echo ""
    fi

    test_vip_manager
    test_postgresql_ha
    test_service_control
    test_ha_watcher
    test_cluster_communication
    test_failover_readiness
    test_failover_simulation

    # Summary
    log_header "Test Summary"

    echo -e "  ${GREEN}Passed:${NC}  $TESTS_PASSED"
    echo -e "  ${RED}Failed:${NC}  $TESTS_FAILED"
    echo -e "  ${YELLOW}Warned:${NC}  $TESTS_WARNED"
    echo -e "  ${DIM}Skipped:${NC} $TESTS_SKIPPED"
    echo ""

    local total=$((TESTS_PASSED + TESTS_FAILED))
    if [[ "$TESTS_FAILED" -eq 0 ]]; then
        echo -e "  ${GREEN}${BOLD}✓ All critical tests passed!${NC}"
        echo ""
        return 0
    else
        echo -e "  ${RED}${BOLD}✗ $TESTS_FAILED test(s) failed${NC}"
        echo ""
        return 1
    fi
}

# =============================================================================
# Usage
# =============================================================================

show_help() {
    echo ""
    echo -e "${CYAN}${BOLD}Spiral Pool HA Validation Script${NC}"
    echo ""
    echo "Usage: $0 [command]"
    echo ""
    echo "Commands:"
    echo "  (none)     Run full test suite"
    echo "  quick      Quick health check only"
    echo "  vip        Test VIP manager only"
    echo "  postgres   Test PostgreSQL HA only"
    echo "  services   Test service control only"
    echo "  watcher    Test HA watcher only"
    echo "  cluster    Test cluster communication only"
    echo "  failover   Test failover readiness"
    echo "  help       Show this help"
    echo ""
    echo "Examples:"
    echo "  $0              # Run all tests"
    echo "  $0 quick        # Quick status check"
    echo "  $0 vip          # Test VIP manager"
    echo ""
}

# =============================================================================
# Main
# =============================================================================

main() {
    local command="${1:-full}"

    case "$command" in
        quick|status|health)
            quick_health_check
            ;;
        vip)
            log_header "VIP Manager Test"
            test_vip_manager
            ;;
        postgres|postgresql|db)
            log_header "PostgreSQL HA Test"
            test_postgresql_ha
            ;;
        services|service)
            log_header "Service Control Test"
            test_service_control
            ;;
        watcher)
            log_header "HA Watcher Test"
            test_ha_watcher
            ;;
        cluster|communication)
            log_header "Cluster Communication Test"
            test_cluster_communication
            ;;
        failover|ready|readiness)
            log_header "Failover Readiness Test"
            test_failover_readiness
            test_failover_simulation
            ;;
        full|all|"")
            run_full_tests
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

main "$@"
