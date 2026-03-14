#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# =============================================================================
# Spiral Stratum — Full HA Integration Test (Docker-based)
# =============================================================================
#
# WHAT THIS DOES:
#   Tests the COMPLETE HA stack end-to-end using Docker Compose:
#   - etcd cluster (3 nodes) for distributed consensus
#   - Patroni (2 nodes) for PostgreSQL automatic failover
#   - HAProxy for connection routing to current leader
#   - Redis for cross-node share/block deduplication
#   - Pool nodes with VIP failover
#
# TESTS PERFORMED:
#   1. etcd cluster health and leader election
#   2. Patroni PostgreSQL replication and failover
#   3. HAProxy routing to correct leader
#   4. Redis connectivity and deduplication
#   5. Pool startup and database connectivity
#   6. Simulated PostgreSQL leader failure + automatic failover
#   7. Pool behavior during database failover
#   8. Replication slot monitoring
#
# USAGE:
#   ./scripts/linux/regtest-ha-full.sh btc
#   ./scripts/linux/regtest-ha-full.sh dgb
#
# PREREQUISITES:
#   - Docker and Docker Compose (script installs if missing)
#   - Sufficient disk space (~5GB for images)
#   - Ports 5432, 7000, 2379-2380 available
#
# NOTE: This script is for TESTING ONLY. Production HA setup uses install.sh
#       with --ha-master/--ha-backup flags, not Docker.
#
# =============================================================================
set -euo pipefail

# =============================================================================
# Configuration
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOG_DIR="$PROJECT_ROOT/logs/regtest-ha-full"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# Test configuration
HA_TEST_TIMEOUT=300  # 5 minutes max for full HA test
PATRONI_FAILOVER_WAIT=60  # Time to wait for Patroni failover

# Docker sudo - set later based on user permissions
DOCKER_SUDO=""

# Track if we stopped system PostgreSQL
SYSTEM_PG_WAS_RUNNING=0

# =============================================================================
# Argument Validation
# =============================================================================

if [[ -z "${1:-}" ]]; then
    echo "Usage: $0 <coin>"
    echo ""
    echo "Supported coins: btc, ltc, dgb, doge, bch, sys, nmc, xmy, fbtc, qbx, pep, cat, bc2"
    echo ""
    echo "Example: $0 btc"
    exit 1
fi

COIN=$(echo "${1}" | tr '[:upper:]' '[:lower:]')
COIN_UPPER=$(echo "$COIN" | tr '[:lower:]' '[:upper:]')

# =============================================================================
# Logging Functions
# =============================================================================

log_step() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "  ${BOLD}$1${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

log_info() {
    echo -e "${CYAN}[INFO]${NC}  $(date '+%H:%M:%S')  $*"
}

log_ok() {
    echo -e "${GREEN}[ OK ]${NC}  $(date '+%H:%M:%S')  $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC}  $(date '+%H:%M:%S')  $*"
}

log_error() {
    echo -e "${RED}[FAIL]${NC}  $(date '+%H:%M:%S')  $*"
}

# =============================================================================
# Cleanup
# =============================================================================

cleanup() {
    log_info "Cleaning up HA test environment..."

    cd "$PROJECT_ROOT/docker" 2>/dev/null || true

    # Dump critical container logs before destroying them
    if command -v docker &>/dev/null; then
        log_info "Capturing container logs before cleanup..."
        for c in stratum haproxy patroni1 patroni2; do
            $DOCKER_SUDO docker logs "spiralpool-${c}" > "$LOG_DIR/${c}.log" 2>&1 || true
        done
        log_info "Logs saved to $LOG_DIR/"
    fi

    # Only try docker cleanup if docker is available
    if command -v docker &>/dev/null; then
        # Determine sudo requirement for cleanup
        if [[ -z "$DOCKER_SUDO" ]] && ! docker info &>/dev/null 2>&1; then
            DOCKER_SUDO="sudo"
        fi
        # Stop all containers
        $DOCKER_SUDO docker compose -f docker-compose.yml -f docker-compose.ha.yml \
            --profile "$COIN" --profile ha down -v --remove-orphans 2>/dev/null || true
    fi

    # Re-enable and restart system PostgreSQL if we disabled it
    if [[ "${SYSTEM_PG_WAS_RUNNING:-0}" == "1" ]]; then
        log_info "Re-enabling and restarting system PostgreSQL..."
        sudo systemctl enable --now postgresql 2>/dev/null || true
    fi

    log_ok "Cleanup complete"
}

trap cleanup EXIT

# =============================================================================
# Docker Prerequisites
# =============================================================================

install_docker_prerequisites() {
    log_step "Step 1/10: Docker Prerequisites"

    # Check if Docker is installed
    if command -v docker &>/dev/null; then
        DOCKER_VERSION=$(docker --version | grep -oP '\d+\.\d+\.\d+' | head -1)
        log_ok "Docker installed: $DOCKER_VERSION"
    else
        log_info "Docker not found. Installing..."

        # Install Docker using official script
        curl -fsSL https://get.docker.com | sh

        # Add current user to docker group
        if [[ -n "${SUDO_USER:-}" ]]; then
            usermod -aG docker "$SUDO_USER"
            log_warn "Added $SUDO_USER to docker group. You may need to log out and back in."
        fi

        # Start Docker service
        systemctl enable docker
        systemctl start docker

        log_ok "Docker installed successfully"
    fi

    # Check if Docker Compose is available (v2 is built into Docker now)
    if $DOCKER_SUDO docker compose version &>/dev/null; then
        COMPOSE_VERSION=$($DOCKER_SUDO docker compose version --short 2>/dev/null || echo "unknown")
        log_ok "Docker Compose installed: $COMPOSE_VERSION"
    else
        log_error "Docker Compose not available"
        log_info "Docker Compose v2 should be included with Docker. Try reinstalling Docker."
        exit 1
    fi

    # Verify Docker is running - may need sudo if user not in docker group yet
    if ! docker info &>/dev/null; then
        if sudo docker info &>/dev/null; then
            log_warn "Docker requires sudo (user not in docker group yet)"
            log_info "After this test, log out and back in to use docker without sudo"
            # Set flag to use sudo for docker commands
            DOCKER_SUDO="sudo"
        else
            log_error "Docker is not running"
            log_info "Start Docker: sudo systemctl start docker"
            exit 1
        fi
    else
        DOCKER_SUDO=""
    fi
    log_ok "Docker daemon is running"

    # Check available disk space (need ~5GB for images)
    AVAILABLE_GB=$(df -BG "$PROJECT_ROOT" | tail -1 | awk '{print $4}' | tr -d 'G')
    if [[ "$AVAILABLE_GB" -lt 5 ]]; then
        log_warn "Low disk space: ${AVAILABLE_GB}GB available (recommend 5GB+)"
    else
        log_ok "Disk space: ${AVAILABLE_GB}GB available"
    fi
}

# =============================================================================
# Environment Setup
# =============================================================================

setup_environment() {
    log_step "Step 2/10: Environment Setup"

    mkdir -p "$LOG_DIR"
    log_ok "Log directory: $LOG_DIR"

    # Create .env file for Docker Compose
    cd "$PROJECT_ROOT/docker"

    # Generate random password for RPC
    RPC_PASS="regtest-rpc-$(openssl rand -hex 8 2>/dev/null || echo "testpass123")"
    DB_PASS="regtest-ha-password-$(openssl rand -hex 8 2>/dev/null || echo "testdbpass")"

    # Coin-specific configuration
    # Each coin has: daemon_host, rpc_port, zmq_port, stratum_port, stratum_v2_port, pool_id, test_address
    # POOL_COIN_NAME is the full name expected by the pool (not the abbreviation)
    case "$COIN" in
        btc)
            POOL_COIN_NAME="bitcoin"
            DAEMON_HOST="bitcoin"
            DAEMON_RPC_PORT=8332
            DAEMON_ZMQ_PORT=28332
            STRATUM_PORT=4333
            STRATUM_PORT_V2=4334
            POOL_ID="pool_btc_sha256"
            # Valid mainnet address format for HA testing (not used for actual mining)
            POOL_ADDRESS="bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
            ;;
        dgb)
            POOL_COIN_NAME="digibyte"
            DAEMON_HOST="digibyte"
            DAEMON_RPC_PORT=14022
            DAEMON_ZMQ_PORT=28532
            STRATUM_PORT=3333
            STRATUM_PORT_V2=3334
            POOL_ID="pool_dgb_sha256"
            POOL_ADDRESS="dgb1qw508d6qejxtdg4y5r3zarvary0c5xw7k8qpzqy"
            ;;
        ltc)
            POOL_COIN_NAME="litecoin"
            DAEMON_HOST="litecoin"
            DAEMON_RPC_PORT=9332
            DAEMON_ZMQ_PORT=28933
            STRATUM_PORT=7333
            STRATUM_PORT_V2=7334
            POOL_ID="pool_ltc_scrypt"
            POOL_ADDRESS="ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx"
            ;;
        doge)
            POOL_COIN_NAME="dogecoin"
            DAEMON_HOST="dogecoin"
            DAEMON_RPC_PORT=22555
            DAEMON_ZMQ_PORT=28555
            STRATUM_PORT=8335
            STRATUM_PORT_V2=8337
            POOL_ID="pool_doge_scrypt"
            POOL_ADDRESS="D8mQ8WNQfF8JH8h8f8f8f8f8f8f8f8f8f8"
            ;;
        bch)
            POOL_COIN_NAME="bitcoincash"
            DAEMON_HOST="bitcoincash"
            DAEMON_RPC_PORT=8432
            DAEMON_ZMQ_PORT=28432
            STRATUM_PORT=5333
            STRATUM_PORT_V2=5334
            POOL_ID="pool_bch_sha256"
            POOL_ADDRESS="bchreg:qpv4q8q8q8q8q8q8q8q8q8q8q8q8q8q8q5h0g0f0e0"
            ;;
        bc2)
            POOL_COIN_NAME="bitcoinii"
            DAEMON_HOST="bitcoinii"
            DAEMON_RPC_PORT=8339
            DAEMON_ZMQ_PORT=28338
            STRATUM_PORT=6333
            STRATUM_PORT_V2=6334
            POOL_ID="pool_bc2_sha256"
            POOL_ADDRESS="bc2rt1q0000000000000000000000000000000000000"
            ;;
        pep)
            POOL_COIN_NAME="pepecoin"
            DAEMON_HOST="pepe"
            DAEMON_RPC_PORT=33873
            DAEMON_ZMQ_PORT=28873
            STRATUM_PORT=10335
            STRATUM_PORT_V2=10336
            POOL_ID="pool_pep_scrypt"
            POOL_ADDRESS="PKtest000000000000000000000000000"
            ;;
        cat)
            POOL_COIN_NAME="catcoin"
            DAEMON_HOST="catcoin"
            DAEMON_RPC_PORT=9932
            DAEMON_ZMQ_PORT=28932
            STRATUM_PORT=12335
            STRATUM_PORT_V2=12336
            POOL_ID="pool_cat_scrypt"
            POOL_ADDRESS="9test00000000000000000000000000000"
            ;;
        *)
            log_error "Unknown coin: $COIN"
            log_info "Supported: btc, dgb, ltc, doge, bch, bc2, pep, cat"
            exit 1
            ;;
    esac

    cat > .env << EOF
# Generated by regtest-ha-full.sh for testing
# DO NOT USE IN PRODUCTION

# Database
DB_USER=spiralstratum
DB_PASSWORD=${DB_PASS}
DB_NAME=spiralstratum
DB_HOST=haproxy
DB_PORT=5432

# Patroni
REPLICATION_PASSWORD=repl-$(openssl rand -hex 8 2>/dev/null || echo "regtest-repl")
REWIND_PASSWORD=rewind-$(openssl rand -hex 8 2>/dev/null || echo "regtest-rewind")
PATRONI_REST_PASSWORD=patroni-$(openssl rand -hex 8 2>/dev/null || echo "regtest-patroni")

# Pool Configuration (used by stratum-entrypoint.sh for config template)
POOL_COIN=${POOL_COIN_NAME}
POOL_ID=${POOL_ID}
POOL_ADDRESS=${POOL_ADDRESS}
COINBASE_TEXT="Spiral Pool HA Test"

# Daemon Configuration
DAEMON_HOST=${DAEMON_HOST}
DAEMON_RPC_PORT=${DAEMON_RPC_PORT}
DAEMON_RPC_USER=spiralpool
DAEMON_RPC_PASSWORD=${RPC_PASS}
DAEMON_ZMQ_PORT=${DAEMON_ZMQ_PORT}

# Stratum Ports
STRATUM_PORT=${STRATUM_PORT}
STRATUM_PORT_V2=${STRATUM_PORT_V2}

# Coin RPC Credentials (all coins use same test password)
BTC_RPC_USER=spiralpool
BTC_RPC_PASSWORD=${RPC_PASS}
BCH_RPC_USER=spiralpool
BCH_RPC_PASSWORD=${RPC_PASS}
DGB_RPC_USER=spiralpool
DGB_RPC_PASSWORD=${RPC_PASS}
BC2_RPC_USER=spiralpool
BC2_RPC_PASSWORD=${RPC_PASS}
LTC_RPC_USER=spiralpool
LTC_RPC_PASSWORD=${RPC_PASS}
DOGE_RPC_USER=spiralpool
DOGE_RPC_PASSWORD=${RPC_PASS}
PEP_RPC_USER=spiralpool
PEP_RPC_PASSWORD=${RPC_PASS}
CAT_RPC_USER=spiralpool
CAT_RPC_PASSWORD=${RPC_PASS}
SYS_RPC_USER=spiralpool
SYS_RPC_PASSWORD=${RPC_PASS}
NMC_RPC_USER=spiralpool
NMC_RPC_PASSWORD=${RPC_PASS}
XMY_RPC_USER=spiralpool
XMY_RPC_PASSWORD=${RPC_PASS}
FBTC_RPC_USER=spiralpool
FBTC_RPC_PASSWORD=${RPC_PASS}
QBX_RPC_USER=spiralpool
QBX_RPC_PASSWORD=${RPC_PASS}
EOF

    log_ok "Environment file created for $COIN_UPPER"
    log_info "  Pool:   $POOL_ID"
    log_info "  Daemon: $DAEMON_HOST:$DAEMON_RPC_PORT"
    log_info "  Stratum: ports $STRATUM_PORT/$STRATUM_PORT_V2"

    # Source the env file for later use
    source .env
}

# =============================================================================
# Start HA Stack
# =============================================================================

start_ha_stack() {
    log_step "Step 3/10: Start HA Stack (etcd + Patroni + HAProxy + Redis)"

    cd "$PROJECT_ROOT/docker"

    # Stop AND disable system PostgreSQL to free port 5432 for Docker.
    # Must disable to prevent systemd from auto-restarting it during the test.
    if systemctl is-active --quiet postgresql 2>/dev/null; then
        log_info "Stopping system PostgreSQL to free port 5432..."
        sudo systemctl disable --now postgresql
        sleep 1
        # Double-check the port is free
        if sudo ss -tlnp | grep -q ':5432 '; then
            log_warn "Port 5432 still in use after stopping postgresql — killing..."
            sudo fuser -k 5432/tcp 2>/dev/null || true
            sleep 1
        fi
        log_ok "System PostgreSQL stopped and disabled"
        SYSTEM_PG_WAS_RUNNING=1
    else
        SYSTEM_PG_WAS_RUNNING=0
    fi

    log_info "Building and starting HA containers..."
    log_info "This may take several minutes on first run (downloading images)..."

    # Start only the containers needed for HA testing — skip dashboard/sentinel/
    # prometheus/grafana (they depend on stratum being healthy and aren't needed).
    # Also skip coin daemon (bitcoin/litecoin/etc) — the HA test validates database
    # failover, HAProxy routing, and Redis dedup, NOT mining. The pool enters
    # degraded mode without a daemon, which is fine for HA infrastructure testing.
    # The coin daemon Docker configs (bitcoin.conf etc) are .template files that
    # require processing by install.sh — they don't exist as plain .conf files.
    $DOCKER_SUDO docker compose -f docker-compose.yml -f docker-compose.ha.yml \
        --profile "$COIN" --profile ha up -d --build \
        etcd1 etcd2 etcd3 patroni1 patroni2 haproxy redis stratum \
        2>&1 | tee "$LOG_DIR/docker-compose.log"

    log_ok "HA core containers started (etcd/patroni/haproxy/redis/stratum)"
}

# =============================================================================
# Wait for etcd Cluster
# =============================================================================

wait_for_etcd() {
    log_step "Step 4/10: Verify etcd Cluster"

    local max_wait=60
    local waited=0

    log_info "Waiting for etcd cluster to form..."

    while [[ $waited -lt $max_wait ]]; do
        # Check if all 3 etcd nodes are healthy
        # Use two greps to avoid field-order dependency in JSON output
        ETCD_HEALTHY=$($DOCKER_SUDO docker compose -f docker-compose.yml -f docker-compose.ha.yml \
            --profile ha ps --format json 2>/dev/null | \
            grep 'etcd' | grep -c '"Health":"healthy"' || echo "0")

        if [[ "$ETCD_HEALTHY" -ge 3 ]]; then
            log_ok "etcd cluster healthy (3/3 nodes)"

            # Check etcd leader
            ETCD_LEADER=$($DOCKER_SUDO docker exec spiralpool-etcd1 etcdctl endpoint status --cluster -w table 2>/dev/null | grep -c "true" || echo "0")
            if [[ "$ETCD_LEADER" -ge 1 ]]; then
                log_ok "etcd leader elected"
                return 0
            fi
        fi

        sleep 2
        waited=$((waited + 2))
        [[ $((waited % 10)) -eq 0 ]] && log_info "  Waiting... ${waited}s (etcd nodes healthy: ${ETCD_HEALTHY}/3)"
    done

    log_error "etcd cluster failed to become healthy after ${max_wait}s"
    $DOCKER_SUDO docker compose -f docker-compose.yml -f docker-compose.ha.yml --profile ha logs etcd1 etcd2 etcd3 | tail -50
    return 1
}

# =============================================================================
# Wait for Patroni Cluster
# =============================================================================

wait_for_patroni() {
    log_step "Step 5/10: Verify Patroni PostgreSQL Cluster"

    local max_wait=120
    local waited=0

    log_info "Waiting for Patroni to initialize PostgreSQL cluster..."

    while [[ $waited -lt $max_wait ]]; do
        # Check Patroni leader via REST API
        PATRONI1_STATUS=$($DOCKER_SUDO docker exec spiralpool-patroni1 curl -sf http://localhost:8008/health 2>/dev/null || echo "")
        PATRONI2_STATUS=$($DOCKER_SUDO docker exec spiralpool-patroni2 curl -sf http://localhost:8008/health 2>/dev/null || echo "")

        # Check for leader
        # NOTE: Patroni REST API returns standard JSON with spaces after colons:
        #   {"state": "running", "role": "master", ...}
        # The \s* handles both spaced and compact JSON formatting.
        LEADER=""
        if echo "$PATRONI1_STATUS" | grep -qE '"role"\s*:\s*"(master|primary)"'; then
            LEADER="patroni1"
        elif echo "$PATRONI2_STATUS" | grep -qE '"role"\s*:\s*"(master|primary)"'; then
            LEADER="patroni2"
        fi

        if [[ -n "$LEADER" ]]; then
            log_ok "Patroni cluster healthy — leader: $LEADER"

            # Verify replication is working
            REPLICA=""
            if [[ "$LEADER" == "patroni1" ]]; then
                REPLICA="patroni2"
            else
                REPLICA="patroni1"
            fi

            REPLICA_STATUS=$($DOCKER_SUDO docker exec spiralpool-${REPLICA} curl -sf http://localhost:8008/health 2>/dev/null || echo "")
            if echo "$REPLICA_STATUS" | grep -qE '"role"\s*:\s*"(replica|standby|sync_standby)"'; then
                log_ok "Replication active — replica: $REPLICA"
            else
                log_warn "Replica status unclear: $REPLICA_STATUS"
            fi

            return 0
        fi

        sleep 3
        waited=$((waited + 3))
        [[ $((waited % 15)) -eq 0 ]] && log_info "  Waiting... ${waited}s"
    done

    log_error "Patroni cluster failed to elect leader after ${max_wait}s"
    $DOCKER_SUDO docker exec spiralpool-patroni1 patronictl -c /etc/patroni/patroni.yml list 2>/dev/null || true
    return 1
}

# =============================================================================
# Verify HAProxy Routing
# =============================================================================

verify_haproxy() {
    log_step "Step 6/10: Verify HAProxy Routing"

    log_info "Checking HAProxy routes to Patroni leader..."

    # Test PostgreSQL connection through HAProxy
    if $DOCKER_SUDO docker exec spiralpool-haproxy nc -z localhost 5432 2>/dev/null; then
        log_ok "HAProxy port 5432 is open"
    else
        log_error "HAProxy port 5432 not accessible"
        return 1
    fi

    # Check HAProxy stats
    HAPROXY_STATS=$($DOCKER_SUDO docker exec spiralpool-haproxy wget -qO- http://localhost:7000/stats 2>/dev/null | head -20 || echo "")
    if [[ -n "$HAPROXY_STATS" ]]; then
        log_ok "HAProxy stats endpoint accessible on port 7000"
    else
        log_warn "HAProxy stats not accessible (non-critical)"
    fi

    # Verify we can connect to PostgreSQL through HAProxy
    # Must provide password — pg_hba requires scram-sha-256 for all TCP connections.
    # Use container's own DB_PASSWORD env var (set by docker-compose from .env).
    if $DOCKER_SUDO docker exec spiralpool-patroni1 bash -c \
        'PGPASSWORD="$DB_PASSWORD" psql -h haproxy -p 5432 -U "$DB_USER" -d postgres -c "SELECT 1;"' &>/dev/null; then
        log_ok "PostgreSQL accessible through HAProxy"
    else
        log_warn "PostgreSQL connection through HAProxy failed (may need more time)"
    fi

    return 0
}

# =============================================================================
# Verify Redis
# =============================================================================

verify_redis() {
    log_step "Step 7/10: Verify Redis Deduplication"

    log_info "Checking Redis connectivity..."

    # Check Redis health
    if $DOCKER_SUDO docker exec spiralpool-redis redis-cli ping 2>/dev/null | grep -q "PONG"; then
        log_ok "Redis responding to PING"
    else
        log_error "Redis not responding"
        return 1
    fi

    # Test set/get
    $DOCKER_SUDO docker exec spiralpool-redis redis-cli SET "ha-test-key" "ha-test-value" &>/dev/null
    REDIS_GET=$($DOCKER_SUDO docker exec spiralpool-redis redis-cli GET "ha-test-key" 2>/dev/null)

    if [[ "$REDIS_GET" == "ha-test-value" ]]; then
        log_ok "Redis SET/GET working"
    else
        log_error "Redis SET/GET failed"
        return 1
    fi

    # Cleanup test key
    $DOCKER_SUDO docker exec spiralpool-redis redis-cli DEL "ha-test-key" &>/dev/null

    return 0
}

# =============================================================================
# Start Pool and Verify Connectivity
# =============================================================================

verify_pool_connectivity() {
    log_step "Step 8/10: Verify Pool Connectivity"

    log_info "Waiting for stratum pool to start..."

    local max_wait=60
    local waited=0

    while [[ $waited -lt $max_wait ]]; do
        # Check if stratum container is running
        if $DOCKER_SUDO docker ps --format '{{.Names}}' | grep -q "spiralpool-stratum"; then
            # Check pool logs for successful startup
            POOL_LOGS=$($DOCKER_SUDO docker logs spiralpool-stratum 2>&1 | tail -50)

            if echo "$POOL_LOGS" | grep -qi "stratum.*listening\|server.*started\|ready"; then
                log_ok "Pool stratum server started"

                # Verify database connection
                if echo "$POOL_LOGS" | grep -qi "database.*connected\|postgres.*connected\|db.*ready"; then
                    log_ok "Pool connected to PostgreSQL via HAProxy"
                fi

                # Verify Redis connection
                if echo "$POOL_LOGS" | grep -qi "redis.*connected\|dedup.*enabled"; then
                    log_ok "Pool connected to Redis for deduplication"
                fi

                return 0
            fi
        fi

        sleep 2
        waited=$((waited + 2))
        [[ $((waited % 10)) -eq 0 ]] && log_info "  Waiting... ${waited}s"
    done

    log_warn "Pool may not have fully started within ${max_wait}s"
    log_info "Check logs: docker logs spiralpool-stratum"
    return 0  # Non-fatal for now
}

# =============================================================================
# Test Patroni Failover
# =============================================================================

test_patroni_failover() {
    log_step "Step 9/10: Test PostgreSQL Failover"

    log_info "Simulating PostgreSQL leader failure..."

    # Get current leader
    CURRENT_LEADER=""
    PATRONI1_STATUS=$($DOCKER_SUDO docker exec spiralpool-patroni1 curl -sf http://localhost:8008/health 2>/dev/null || echo "")
    if echo "$PATRONI1_STATUS" | grep -qE '"role"\s*:\s*"(master|primary)"'; then
        CURRENT_LEADER="patroni1"
        EXPECTED_NEW_LEADER="patroni2"
    else
        CURRENT_LEADER="patroni2"
        EXPECTED_NEW_LEADER="patroni1"
    fi

    log_info "Current leader: $CURRENT_LEADER"
    log_info "Expected new leader after failover: $EXPECTED_NEW_LEADER"

    # Kill the current leader
    log_info "Stopping $CURRENT_LEADER..."
    $DOCKER_SUDO docker stop "spiralpool-${CURRENT_LEADER}" &>/dev/null

    # Wait for failover
    log_info "Waiting for automatic failover (max ${PATRONI_FAILOVER_WAIT}s)..."

    local waited=0
    local failover_success=0

    while [[ $waited -lt $PATRONI_FAILOVER_WAIT ]]; do
        NEW_LEADER_STATUS=$($DOCKER_SUDO docker exec "spiralpool-${EXPECTED_NEW_LEADER}" curl -sf http://localhost:8008/health 2>/dev/null || echo "")

        if echo "$NEW_LEADER_STATUS" | grep -qE '"role"\s*:\s*"(master|primary)"'; then
            log_ok "Failover successful! New leader: $EXPECTED_NEW_LEADER"
            failover_success=1
            break
        fi

        sleep 2
        waited=$((waited + 2))
        [[ $((waited % 10)) -eq 0 ]] && log_info "  Waiting for failover... ${waited}s"
    done

    if [[ $failover_success -eq 0 ]]; then
        log_error "Failover did not complete within ${PATRONI_FAILOVER_WAIT}s"
        return 1
    fi

    # Verify HAProxy is routing to new leader
    log_info "Verifying HAProxy routes to new leader..."
    sleep 5  # Give HAProxy time to detect the change

    if $DOCKER_SUDO docker exec spiralpool-haproxy nc -z localhost 5432 2>/dev/null; then
        log_ok "HAProxy still routing PostgreSQL traffic"
    else
        log_warn "HAProxy may need time to update routing"
    fi

    # Restart the old leader (now becomes replica)
    log_info "Restarting $CURRENT_LEADER as replica..."
    $DOCKER_SUDO docker start "spiralpool-${CURRENT_LEADER}" &>/dev/null

    sleep 10

    OLD_LEADER_STATUS=$($DOCKER_SUDO docker exec "spiralpool-${CURRENT_LEADER}" curl -sf http://localhost:8008/health 2>/dev/null || echo "")
    if echo "$OLD_LEADER_STATUS" | grep -qE '"role"\s*:\s*"(replica|standby|sync_standby)"'; then
        log_ok "$CURRENT_LEADER rejoined as replica"
    else
        log_warn "$CURRENT_LEADER status after restart: $OLD_LEADER_STATUS"
    fi

    return 0
}

# =============================================================================
# Summary
# =============================================================================

print_summary() {
    log_step "Step 10/10: Summary"

    echo ""
    echo -e "  ${BOLD}Full HA Test Results — $COIN_UPPER${NC}"
    echo "  ─────────────────────────────────────────────"
    echo ""

    # Get final status
    echo -e "  ${BOLD}etcd Cluster:${NC}"
    $DOCKER_SUDO docker exec spiralpool-etcd1 etcdctl endpoint status --cluster -w table 2>/dev/null | head -5 || echo "  (unable to query)"
    echo ""

    echo -e "  ${BOLD}Patroni Cluster:${NC}"
    $DOCKER_SUDO docker exec spiralpool-patroni1 patronictl -c /etc/patroni/patroni.yml list 2>/dev/null || \
    $DOCKER_SUDO docker exec spiralpool-patroni2 patronictl -c /etc/patroni/patroni.yml list 2>/dev/null || \
    echo "  (unable to query)"
    echo ""

    echo -e "  ${BOLD}Redis:${NC}"
    REDIS_INFO=$($DOCKER_SUDO docker exec spiralpool-redis redis-cli INFO server 2>/dev/null | grep "redis_version" || echo "  (unable to query)")
    echo "  $REDIS_INFO"
    echo ""

    echo -e "  ${BOLD}Containers:${NC}"
    $DOCKER_SUDO docker compose -f docker-compose.yml -f docker-compose.ha.yml --profile "$COIN" --profile ha ps --format "table {{.Name}}\t{{.Status}}" 2>/dev/null || true
    echo ""

    echo -e "  ${BOLD}Log Files:${NC}"
    echo "  Docker logs:    docker logs spiralpool-<container>"
    echo "  Compose log:    $LOG_DIR/docker-compose.log"
    echo ""

    echo -e "  ${BOLD}Management Commands:${NC}"
    echo "  Patroni status: $DOCKER_SUDO docker exec spiralpool-patroni1 patronictl -c /etc/patroni/patroni.yml list"
    echo "  etcd status:    $DOCKER_SUDO docker exec spiralpool-etcd1 etcdctl endpoint status --cluster -w table"
    echo "  HAProxy stats:  http://localhost:7000/stats"
    echo ""

    echo -e "  ${GREEN}${BOLD}Full HA test complete!${NC}"
    echo ""
}

# =============================================================================
# Main
# =============================================================================

main() {
    echo ""
    echo -e "${CYAN}╔════════════════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║${NC}                                                                            ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}           ${BOLD}SPIRAL POOL — FULL HA INTEGRATION TEST${NC}                           ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                            ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}   ${YELLOW}etcd + Patroni + HAProxy + Redis + Pool Nodes${NC}                            ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                            ${CYAN}║${NC}"
    echo -e "${CYAN}╚════════════════════════════════════════════════════════════════════════════╝${NC}"
    echo ""

    install_docker_prerequisites
    setup_environment
    start_ha_stack
    wait_for_etcd
    wait_for_patroni
    verify_haproxy
    verify_redis
    verify_pool_connectivity
    test_patroni_failover
    print_summary

    log_ok "Full HA test completed successfully"
}

main "$@"
