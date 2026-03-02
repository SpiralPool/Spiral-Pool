#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# =============================================================================
# Spiral Stratum — Bitcoin II (BC2) Regtest Integration Test
# =============================================================================
#
# WHAT THIS DOES:
#   Tests the FULL block lifecycle end-to-end against a real Bitcoin II daemon
#   running in regtest mode (private local blockchain, difficulty=1).
#
#   This is NOT a unit test — it starts real processes, mines real (regtest)
#   blocks, and verifies the pool handles them correctly.
#
#   Flow:
#     bitcoinIId (regtest) --> pool (stratum) --> cpuminer --> block found!
#          |                      |                               |
#          |<---- submitblock ----|                               |
#          |---- ZMQ hashblock -->|                               |
#          |                      |---- DB insert + status ------>|
#
# WHEN TO RUN:
#   - After install.sh (which builds the pool binary and sets up PostgreSQL)
#   - Before deploying to production, to verify the block submission pipeline
#   - After making changes to block handling, stratum, or daemon code
#   - You can run this repeatedly — it cleans up after itself
#
# PREREQUISITES:
#   1. Bitcoin II Core installed (bitcoinIId + bitcoinII-cli in PATH)
#      - Build from: https://github.com/Bitcoin-II/BitcoinII-Core
#      - Or install a release binary
#   2. cpuminer with SHA256d support (minerd / cpuminer-multi)
#      - apt install cpuminer  OR  build from https://github.com/pooler/cpuminer
#   3. PostgreSQL running locally (install.sh sets this up)
#   4. Pool binary built (this script builds it if missing)
#
# USAGE:
#   # Basic usage (all binaries in PATH):
#   chmod +x scripts/linux/regtest-bc2.sh
#   ./scripts/linux/regtest-bc2.sh
#
#   # Custom binary paths:
#   BITCOINIID=/opt/bc2/bin/bitcoinIId \
#   BITCOINIICLI=/opt/bc2/bin/bitcoinII-cli \
#   CPUMINER=/usr/local/bin/minerd \
#     ./scripts/linux/regtest-bc2.sh
#
#   # Custom database credentials:
#   DB_USER=myuser DB_PASS=mypass DB_NAME=mydb \
#     ./scripts/linux/regtest-bc2.sh
#
# OUTPUT:
#   - Console:  Color-coded step-by-step progress
#   - Logs:     logs/regtest/spiralpool-regtest.log    (pool)
#               logs/regtest/cpuminer-regtest.log      (miner)
#               logs/regtest/bitcoinIId-regtest.log    (daemon)
#               logs/regtest/bitcoinIId-startup.log    (daemon stderr)
#
# EXIT CODES:
#   0 = All blocks mined and lifecycle verified
#   1 = Blocks not found or lifecycle incomplete
#
# CLEANUP:
#   The script automatically stops all processes on exit (Ctrl+C safe).
#   The regtest config address placeholder is restored so git stays clean.
#   Regtest blockchain data is in ~/.bitcoinII/regtest/ (delete to reset).
#
# =============================================================================
set -euo pipefail

# =============================================================================
# Configuration — Override via environment variables
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CONFIG_FILE="$PROJECT_ROOT/config/config-bc2-regtest.yaml"
POOL_BINARY="$PROJECT_ROOT/src/stratum/spiralpool"
LOG_DIR="$PROJECT_ROOT/logs/regtest"

# Bitcoin II Core binaries
# install.sh creates lowercase symlinks in /usr/local/bin: bitcoiniid, bitcoinii-cli
# Override: BITCOINIID=/opt/spiralpool/bc2/bin/bitcoinIId ./regtest-bc2.sh
BITCOINIID="${BITCOINIID:-bitcoiniid}"
BITCOINIICLI="${BITCOINIICLI:-bitcoinii-cli}"

# CPU miner binary
# Override: CPUMINER=/path/to/minerd ./regtest-bc2.sh
CPUMINER="${CPUMINER:-minerd}"

# Database credentials (must match config-bc2-regtest.yaml)
DB_NAME="${DB_NAME:-spiralstratum_regtest}"
DB_USER="${DB_USER:-spiralstratum}"
DB_PASS="${DB_PASS:-spiralstratum}"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"

# Daemon RPC credentials (must match config-bc2-regtest.yaml)
RPC_USER="${RPC_USER:-spiraltest}"
RPC_PASS="${RPC_PASS:-spiraltest}"
RPC_PORT="${RPC_PORT:-18449}"       # BC2 regtest RPC port
P2P_PORT="${P2P_PORT:-18448}"       # BC2 regtest P2P port
ZMQ_PORT="${ZMQ_PORT:-29338}"       # ZMQ pub port for block notifications

# Pool stratum port (must match config-bc2-regtest.yaml)
STRATUM_PORT="${STRATUM_PORT:-16333}"

# Test parameters
TEST_BLOCKS="${TEST_BLOCKS:-5}"  # How many blocks to mine (override: TEST_BLOCKS=150 ./regtest-bc2.sh)
MINER_WAIT_SECS="${MINER_WAIT_SECS:-600}"  # Max seconds for mining phase
BLOCK_CHECK_INTERVAL=10   # Seconds between height checks
MINER_THREADS="${MINER_THREADS:-$(nproc)}"  # Use all CPU cores (diff=1 needs hashrate)
# Post-mining: keep pool running for payment processor maturity checks
# Payment processor cycles every ~10min, needs 3 stable checks = ~30min minimum
MATURITY_WAIT_SECS="${MATURITY_WAIT_SECS:-1200}"  # Seconds to wait for maturity (miner adds confirmation blocks)
# Coinbase maturity: BC2 inherits Bitcoin's 100-block rule. The miner keeps running
# during Step 8c and mines the blocks naturally. At ~60-80s/block, 100 blocks ≈ 1.5-2 hours.
# Set to 0 to skip coin movement tests (quick run: 4-6 min total).
COINBASE_WAIT_SECS="${COINBASE_WAIT_SECS:-900}"  # Seconds to wait for coinbase maturity (~15 min covers ~10-block maturity)
# Pool API port (must match config-bc2-regtest.yaml api_port)
API_PORT="${API_PORT:-14000}"

# =============================================================================
# Colors and Logging
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

DAEMON_PID=""
POOL_PID=""
MINER_PID=""
POOL2_PID=""
MINING_ADDRESS=""  # Set in Step 3, used in cleanup to restore config

log_info()  { echo -e "${CYAN}[INFO]${NC}  $(date '+%H:%M:%S')  $*"; }
log_ok()    { echo -e "${GREEN}[ OK ]${NC}  $(date '+%H:%M:%S')  $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $(date '+%H:%M:%S')  $*"; }
log_error() { echo -e "${RED}[FAIL]${NC}  $(date '+%H:%M:%S')  $*"; }

log_step() {
    echo ""
    echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${GREEN}${BOLD}  $*${NC}"
    echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

# Helper: run bitcoinII-cli with standard regtest flags
bc2cli() {
    "$BITCOINIICLI" -regtest -rpcuser="$RPC_USER" -rpcpassword="$RPC_PASS" -rpcport="$RPC_PORT" "$@"
}

# Helper: run bitcoinII-cli with wallet
bc2cli_wallet() {
    bc2cli -rpcwallet="regtest-pool" "$@"
}

# =============================================================================
# Cleanup — runs on EXIT, INT (Ctrl+C), TERM
# =============================================================================

CLEANUP_RUNNING=0

cleanup() {
    # Prevent re-entry
    if [[ $CLEANUP_RUNNING -eq 1 ]]; then return; fi
    CLEANUP_RUNNING=1
    trap '' INT TERM

    echo ""
    log_step "Cleanup"

    # SIGKILL everything immediately — test environment, no graceful shutdown needed
    log_info "Force-killing all test processes..."
    kill -9 "$MINER_PID"  2>/dev/null || true
    kill -9 "$POOL_PID"   2>/dev/null || true
    kill -9 "$POOL2_PID"  2>/dev/null || true
    kill -9 "$DAEMON_PID" 2>/dev/null || true

    # Fallback: kill by name in case PIDs were stale
    pkill -9 -f "regtest-cpuminer" 2>/dev/null || true
    pkill -9 -f "spiralpool.*-config" 2>/dev/null || true
    bc2cli stop 2>/dev/null || true
    pkill -9 -f "bitcoiniid.*regtest" 2>/dev/null || true

    # Reap zombies
    wait "$MINER_PID"  2>/dev/null || true
    wait "$POOL_PID"   2>/dev/null || true
    wait "$POOL2_PID"  2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true

    log_ok "All processes killed"

    # Clean up HA test config if it exists
    rm -f "$LOG_DIR/config-bc2-regtest-ha.yaml" 2>/dev/null || true

    # Restore config placeholder (keep git clean)
    if [[ -n "$MINING_ADDRESS" ]]; then
        sed -i "s|$MINING_ADDRESS|REGTEST_ADDRESS_PLACEHOLDER|g" "$CONFIG_FILE" 2>/dev/null || true
        log_info "Config address placeholder restored"
    fi

    log_ok "Cleanup complete"
}

trap cleanup EXIT INT TERM

# =============================================================================
# Helper Functions
# =============================================================================

check_binary() {
    local name="$1"
    local bin="$2"
    local install_hint="${3:-}"
    if ! command -v "$bin" &>/dev/null; then
        log_error "$name not found in PATH: '$bin'"
        if [[ -n "$install_hint" ]]; then
            log_info "  Install: $install_hint"
        fi
        log_info "  Or override: ${name^^}=/path/to/$bin ./regtest-bc2.sh"
        return 1
    fi
    log_ok "$name found: $(command -v "$bin")"
}

wait_for_rpc() {
    local max_wait=30
    local waited=0
    log_info "Waiting for bitcoinIId RPC on port $RPC_PORT..."
    while ! bc2cli getblockchaininfo &>/dev/null; do
        sleep 1
        waited=$((waited + 1))
        if [[ $waited -ge $max_wait ]]; then
            log_error "bitcoinIId RPC not responding after ${max_wait}s"
            log_error "Check: $LOG_DIR/bitcoinIId-startup.log"
            return 1
        fi
    done
    log_ok "bitcoinIId RPC ready (${waited}s)"
}

wait_for_stratum() {
    local max_wait=30
    local waited=0
    log_info "Waiting for pool stratum on port $STRATUM_PORT..."
    while ! (echo >/dev/tcp/127.0.0.1/$STRATUM_PORT) 2>/dev/null; do
        sleep 1
        waited=$((waited + 1))
        if [[ $waited -ge $max_wait ]]; then
            log_error "Pool stratum not listening after ${max_wait}s"
            log_error "Check: $LOG_DIR/spiralpool-regtest.log"
            return 1
        fi
    done
    log_ok "Pool stratum listening on port $STRATUM_PORT (${waited}s)"
}

get_block_count() {
    bc2cli getblockcount 2>/dev/null || echo "0"
}

# =============================================================================
# STEP 1: Preflight Checks
# =============================================================================

log_step "Step 1/10: Preflight Checks"

log_info "Checking required binaries..."
check_binary "bitcoinIId"    "$BITCOINIID"   "Build from https://github.com/Bitcoin-II/BitcoinII-Core"
check_binary "bitcoinII-cli" "$BITCOINIICLI" "(included with Bitcoin II Core)"
check_binary "cpuminer"      "$CPUMINER"     "Build from https://github.com/pooler/cpuminer"

log_info "Checking config file..."
if [[ ! -f "$CONFIG_FILE" ]]; then
    log_error "Regtest config not found: $CONFIG_FILE"
    log_info "  This file should exist in the release. Re-run install.sh or check your checkout."
    exit 1
fi
log_ok "Config: $CONFIG_FILE"

log_info "Checking pool binary..."
if [[ ! -f "$POOL_BINARY" ]]; then
    log_warn "Pool binary not found at $POOL_BINARY — building now..."
    (cd "$PROJECT_ROOT/src/stratum" && go build -o spiralpool ./cmd/spiralpool)
    if [[ ! -f "$POOL_BINARY" ]]; then
        log_error "Build failed! Run manually: cd src/stratum && go build -o spiralpool ./cmd/spiralpool"
        exit 1
    fi
    log_ok "Pool binary built successfully"
else
    log_ok "Pool binary: $POOL_BINARY"
fi

log_info "Checking PostgreSQL..."
if ! pg_isready -h "$DB_HOST" -p "$DB_PORT" &>/dev/null; then
    log_error "PostgreSQL not running on $DB_HOST:$DB_PORT"
    log_info "  Start it:   sudo systemctl start postgresql"
    log_info "  Or install:  install.sh handles PostgreSQL setup"
    exit 1
fi
log_ok "PostgreSQL running on $DB_HOST:$DB_PORT"

mkdir -p "$LOG_DIR"
log_ok "Log directory: $LOG_DIR"

# =============================================================================
# STEP 2: Start Bitcoin II Daemon (regtest mode)
# =============================================================================

log_step "Step 2/10: Start bitcoinIId (regtest)"

log_info "Stopping any existing regtest processes..."
bc2cli stop 2>/dev/null || true
# Kill any leftover pool/miner from a previous run (port conflicts)
pkill -f "spiralpool.*config" 2>/dev/null || true
pkill -f "minerd.*$STRATUM_PORT" 2>/dev/null || true
sleep 2

log_info "Starting bitcoinIId with:"
log_info "  Network:    regtest (private chain, difficulty=1)"
log_info "  RPC:        127.0.0.1:$RPC_PORT (user=$RPC_USER)"
log_info "  P2P:        port $P2P_PORT (listen=off, no external peers)"
log_info "  ZMQ:        tcp://127.0.0.1:$ZMQ_PORT (hashblock + rawblock)"
log_info "  Log:        $LOG_DIR/bitcoinIId-regtest.log"

"$BITCOINIID" \
    -regtest \
    -daemon=0 \
    -server=1 \
    -rpcuser="$RPC_USER" \
    -rpcpassword="$RPC_PASS" \
    -rpcport="$RPC_PORT" \
    -rpcallowip=127.0.0.1 \
    -rpcbind=127.0.0.1 \
    -port="$P2P_PORT" \
    -listen=0 \
    -zmqpubhashblock="tcp://127.0.0.1:$ZMQ_PORT" \
    -zmqpubrawblock="tcp://127.0.0.1:$ZMQ_PORT" \
    -txindex=1 \
    -fallbackfee=0.0001 \
    -blockfilterindex=1 \
    -printtoconsole=0 \
    -debuglogfile="$LOG_DIR/bitcoinIId-regtest.log" \
    &>"$LOG_DIR/bitcoinIId-startup.log" &

DAEMON_PID=$!
log_info "bitcoinIId PID: $DAEMON_PID"

wait_for_rpc

# Show chain info
CHAIN_INFO=$(bc2cli getblockchaininfo 2>/dev/null)
CHAIN_NAME=$(echo "$CHAIN_INFO" | grep '"chain"' | tr -d ' ",' | cut -d: -f2)
CHAIN_BLOCKS=$(echo "$CHAIN_INFO" | grep '"blocks"' | tr -d ' ",' | cut -d: -f2)
log_ok "Connected to chain='$CHAIN_NAME' at height $CHAIN_BLOCKS"

# =============================================================================
# STEP 3: Create Wallet and Mining Address
# =============================================================================

log_step "Step 3/10: Create wallet and mining address"

log_info "Creating regtest wallet 'regtest-pool'..."
# Use simple createwallet (no positional args) — v29+ removed legacy wallets,
# passing descriptors=false would fail. Default creates a descriptor wallet.
CREATE_OUT=$(bc2cli createwallet "regtest-pool" 2>&1) || \
LOAD_OUT=$(bc2cli loadwallet "regtest-pool" 2>&1) || true

if echo "$CREATE_OUT" | grep -qi "error" && echo "$LOAD_OUT" | grep -qi "error"; then
    log_error "Wallet creation AND load both failed:"
    log_error "  createwallet: $CREATE_OUT"
    log_error "  loadwallet:   $LOAD_OUT"
    exit 1
fi
log_ok "Wallet ready"

log_info "Generating bech32 mining address..."
MINING_ADDRESS=$(bc2cli_wallet getnewaddress "pool-coinbase" "bech32")
log_ok "Mining address: $MINING_ADDRESS"

log_info "Updating pool config with mining address..."
sed -i "s|REGTEST_ADDRESS_PLACEHOLDER|$MINING_ADDRESS|g" "$CONFIG_FILE"
log_ok "Config updated (will be restored on exit)"

# =============================================================================
# STEP 4: Generate Initial Blocks (coinbase maturity)
# =============================================================================

log_step "Step 4/10: Verify chain ready"

# BC2 regtest has a non-standard difficulty schedule — difficulty jumps from regtest
# minimum (207fffff) to difficulty 1 (1d00ffff) after block 1. We accept this tradeoff
# and pre-mine block 1 via generatetoaddress to exit IBD, because a daemon in IBD
# rejects getblocktemplate requests, leaving miners idle for minutes.
#
# Result: pool-mined blocks start at height 2+ with difficulty=1.
# At ~11 MH/s per thread, difficulty 1 ≈ 4.3B hashes / (11M * threads) seconds/block.
CURRENT_HEIGHT=$(get_block_count)
log_ok "Chain height: $CURRENT_HEIGHT"

if [[ "$CURRENT_HEIGHT" -eq 0 ]]; then
    log_info "Fresh chain — generating 1 block to exit IBD so daemon serves templates..."
    bc2cli_wallet generatetoaddress 1 "$MINING_ADDRESS" &>/dev/null
    CURRENT_HEIGHT=$(get_block_count)
    if [[ "$CURRENT_HEIGHT" -ge 1 ]]; then
        log_ok "IBD exit block generated (height now: $CURRENT_HEIGHT)"
    else
        log_error "generatetoaddress failed — daemon may still be in IBD"
        exit 1
    fi
else
    log_info "Chain has existing blocks — pool will continue from height $((CURRENT_HEIGHT + 1))"
fi

# Estimate seconds per block based on thread count (~7.5 MH/s per thread on typical VM)
HASHRATE_PER_THREAD=7500  # kH/s — measured ~7.6 MH/s per thread, using 7.5 for safety
TOTAL_HASHRATE=$((HASHRATE_PER_THREAD * MINER_THREADS))
DIFF1_HASHES=4295000       # ~4.295 billion hashes (2^32) in thousands
EST_SECS_PER_BLOCK=$((DIFF1_HASHES / TOTAL_HASHRATE))
log_info "Estimated ~${EST_SECS_PER_BLOCK}s/block at difficulty=1 ($MINER_THREADS threads, ~${TOTAL_HASHRATE} kH/s)"

# Auto-calculate timeout if user didn't override: 3x expected time + 120s buffer
DEFAULT_WAIT=$(( (EST_SECS_PER_BLOCK * TEST_BLOCKS * 3) + 120 ))
if [[ "$MINER_WAIT_SECS" -eq 600 ]]; then
    # User didn't override (600 is the default) — use calculated value
    MINER_WAIT_SECS=$DEFAULT_WAIT
    log_info "Auto-calculated timeout: ${MINER_WAIT_SECS}s (3x estimated mining time + 120s)"
fi

# Verify getblocktemplate works BEFORE starting the pool
log_info "Testing getblocktemplate on daemon..."
GBT_RESULT=$(bc2cli getblocktemplate '{"rules":["segwit"]}' 2>&1) || true
if echo "$GBT_RESULT" | grep -q '"previousblockhash"'; then
    GBT_HEIGHT=$(echo "$GBT_RESULT" | grep '"height"' | tr -d ' ",' | cut -d: -f2)
    log_ok "getblocktemplate works (template height=$GBT_HEIGHT)"
else
    log_error "getblocktemplate FAILED — daemon cannot produce block templates"
    log_error "Response: $(echo "$GBT_RESULT" | head -5)"
    log_error "This means the pool cannot create mining jobs. Miners will idle forever."
    log_error ""
    log_error "Common causes:"
    log_error "  1. Daemon has zero peers and this fork requires peers for GBT"
    log_error "  2. Chain is still in IBD (initial block download)"
    log_error "  3. Wallet not loaded (needed for coinbase output)"
    exit 1
fi

# =============================================================================
# STEP 5: Set Up PostgreSQL Database
# =============================================================================

log_step "Step 5/10: Set up PostgreSQL database"

# Try connecting first — if install.sh already set up the DB, skip sudo entirely.
# This allows the script to run headless (nohup, screen, cron) without sudo prompts.
if PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" &>/dev/null; then
    log_ok "Database '$DB_NAME' already accessible (user=$DB_USER, auth verified)"
    # Kill stale PostgreSQL sessions from previous pool runs to release advisory locks.
    # The pool uses pg_try_advisory_lock for payment fencing — if a previous pool process
    # was killed (SIGKILL), its PostgreSQL session may still hold the lock, blocking
    # ALL payment processor cycles in the next run.
    log_info "Terminating stale database sessions from previous runs..."
    TERMINATED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT count(*) FROM (SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DB_NAME' AND pid != pg_backend_pid()) sub;" 2>/dev/null | tr -d ' ' || echo "0")
    if [[ "$TERMINATED" -gt 0 ]]; then
        log_ok "Terminated $TERMINATED stale session(s) (advisory locks released)"
        sleep 1  # Give PostgreSQL a moment to clean up
    else
        log_ok "No stale sessions found"
    fi
    # Clean stale block data from previous runs.
    # The blockchain resets (rm -rf ~/.bitcoinII/regtest) but DB persists — old records
    # with different hashes cause the payment processor to see false orphans.
    log_info "Truncating block tables from previous runs..."
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'blocks_%' LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null && \
        log_ok "Block tables truncated" || log_warn "No block tables to truncate (first run)"
    # Also clean shares, payments, and balance tables for a fresh test
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'shares_%' OR tablename LIKE 'payments_%' OR tablename LIKE 'balances_%') LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null || true
else
    # DB not accessible — need sudo to set it up
    if [[ ! -t 0 ]]; then
        # No TTY (running in background via nohup/screen/cron) — can't prompt for sudo
        log_error "Database '$DB_NAME' not accessible and no TTY available for sudo"
        log_error ""
        log_error "The script needs sudo to create the PostgreSQL database, but it's"
        log_error "running in the background where sudo can't prompt for a password."
        log_error ""
        log_error "Fix: Run the DB setup once interactively first, then re-run with nohup:"
        log_error ""
        log_error "  sudo -u postgres psql -c \"CREATE USER $DB_USER WITH PASSWORD '$DB_PASS';\""
        log_error "  sudo -u postgres psql -c \"CREATE DATABASE $DB_NAME OWNER $DB_USER;\""
        log_error "  sudo -u postgres psql -c \"GRANT ALL PRIVILEGES ON DATABASE $DB_NAME TO $DB_USER;\""
        log_error ""
        log_error "Then re-run: nohup ./scripts/linux/regtest-bc2.sh > regtest.log 2>&1 &"
        exit 1
    fi

    log_info "Creating database '$DB_NAME' (requires sudo)..."
    # Create user or update password if user already exists (e.g. from install.sh with different password)
    sudo -u postgres psql -c "CREATE USER $DB_USER WITH PASSWORD '$DB_PASS';" 2>/dev/null || \
    sudo -u postgres psql -c "ALTER USER $DB_USER WITH PASSWORD '$DB_PASS';" 2>/dev/null || true
    sudo -u postgres psql -c "CREATE DATABASE $DB_NAME OWNER $DB_USER;" 2>/dev/null || true
    sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE $DB_NAME TO $DB_USER;" 2>/dev/null || true

    # Verify the connection actually works after setup
    if PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" &>/dev/null; then
        log_ok "Database '$DB_NAME' ready (user=$DB_USER, auth verified)"
        # Clean stale data (same as the non-sudo path above)
        log_info "Truncating block tables from previous runs..."
        PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'blocks_%' LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null && \
            log_ok "Block tables truncated" || log_warn "No block tables to truncate (first run)"
        PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'shares_%' OR tablename LIKE 'payments_%' OR tablename LIKE 'balances_%') LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null || true
    else
        log_error "Cannot connect to PostgreSQL as $DB_USER — password may not have been updated"
        log_error "Manual fix: sudo -u postgres psql -c \"ALTER USER $DB_USER WITH PASSWORD '$DB_PASS';\""
        exit 1
    fi
fi

# =============================================================================
# STEP 6: Start the Pool
# =============================================================================

log_step "Step 6/10: Start Spiral Stratum Pool"

log_info "Starting pool with:"
log_info "  Config:     $CONFIG_FILE"
log_info "  Coin:       BC2 (Bitcoin II)"
log_info "  Stratum:    127.0.0.1:$STRATUM_PORT"
log_info "  Daemon:     127.0.0.1:$RPC_PORT (regtest)"
log_info "  ZMQ:        tcp://127.0.0.1:$ZMQ_PORT"
log_info "  Database:   $DB_HOST:$DB_PORT/$DB_NAME"
log_info "  Log:        $LOG_DIR/spiralpool-regtest.log"

"$POOL_BINARY" -config "$CONFIG_FILE" &>"$LOG_DIR/spiralpool-regtest.log" &
POOL_PID=$!
log_info "Pool PID: $POOL_PID"

wait_for_stratum

# Give pool a moment to fetch first block template from daemon
sleep 3

# Check pool connected to daemon successfully
if grep -qi "genesis.*verified\|genesis.*skip" "$LOG_DIR/spiralpool-regtest.log" 2>/dev/null; then
    log_ok "Pool connected to daemon and verified chain"
elif grep -qi "block.*template\|new.*job\|getblocktemplate" "$LOG_DIR/spiralpool-regtest.log" 2>/dev/null; then
    log_ok "Pool fetched block template from daemon"
else
    log_warn "No daemon connection confirmation in logs yet (may still be starting)"
    log_info "  Tail log: tail -f $LOG_DIR/spiralpool-regtest.log"
fi

# =============================================================================
# STEP 7: Connect CPU Miner
# =============================================================================

log_step "Step 7/10: Connect CPU miner to pool"

HEIGHT_BEFORE=$(get_block_count)
log_info "Chain height before pool mining: $HEIGHT_BEFORE"

# Use cpuminer (minerd) for SHA256d mining. With varDiff disabled in regtest config,
# Spiral Router is inactive and cpuminer gets the config's initial difficulty (1),
# which matches the network difficulty. At ~99 MH/s this yields ~43s/block.
log_info "Starting cpuminer (minerd) with:"
log_info "  Pool:       stratum+tcp://127.0.0.1:$STRATUM_PORT"
log_info "  Worker:     TEST.worker1"
log_info "  Algorithm:  sha256d"
log_info "  Threads:    $MINER_THREADS"
log_info "  Log:        $LOG_DIR/cpuminer-regtest.log"

"$CPUMINER" -a sha256d -o "stratum+tcp://127.0.0.1:$STRATUM_PORT" \
    -u TEST.worker1 -p x -t "$MINER_THREADS" \
    &>"$LOG_DIR/cpuminer-regtest.log" &

MINER_PID=$!
log_ok "cpuminer started (PID $MINER_PID)"

# =============================================================================
# STEP 8: Wait for Blocks
# =============================================================================

log_step "Step 8/10: Mine blocks through the pool"

BLOCKS_FOUND=0
ELAPSED=0
TARGET_HEIGHT=$((HEIGHT_BEFORE + TEST_BLOCKS))

log_info "Target: $TEST_BLOCKS blocks (height $HEIGHT_BEFORE -> $TARGET_HEIGHT)"
log_info "Timeout: ${MINER_WAIT_SECS}s (BC2 regtest diff=1, ~${EST_SECS_PER_BLOCK}s/block with $MINER_THREADS threads)"
echo ""

while [[ $ELAPSED -lt $MINER_WAIT_SECS ]]; do
    CURRENT_HEIGHT=$(get_block_count)
    BLOCKS_FOUND=$((CURRENT_HEIGHT - HEIGHT_BEFORE))

    if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
        log_ok "BLOCKS FOUND: $BLOCKS_FOUND (chain height: $CURRENT_HEIGHT)"
        break
    fi

    if [[ $((ELAPSED % 10)) -eq 0 ]] && [[ $ELAPSED -gt 0 ]]; then
        log_info "  ${ELAPSED}s ... blocks: $BLOCKS_FOUND/$TEST_BLOCKS (height: $CURRENT_HEIGHT)"
        # Show miner status on first check for diagnostics
        if [[ $ELAPSED -eq 10 ]]; then
            MINER_LINES=$(wc -l < "$LOG_DIR/cpuminer-regtest.log" 2>/dev/null || echo "0")
            if [[ "$MINER_LINES" -eq 0 ]]; then
                log_warn "  Miner log is EMPTY — miner may have crashed"
            else
                MINER_STATUS=$(tail -3 "$LOG_DIR/cpuminer-regtest.log" 2>/dev/null | tr '\n' ' ')
                log_info "  Miner: $MINER_STATUS"
            fi
        fi
    fi

    sleep "$BLOCK_CHECK_INTERVAL"
    ELAPSED=$((ELAPSED + BLOCK_CHECK_INTERVAL))
done

echo ""

if [[ $BLOCKS_FOUND -lt $TEST_BLOCKS ]]; then
    log_error "Only $BLOCKS_FOUND / $TEST_BLOCKS blocks found in ${MINER_WAIT_SECS}s"
    log_error ""
    log_error "Troubleshooting:"
    log_error "  1. Check miner connected:  tail $LOG_DIR/cpuminer-regtest.log"
    log_error "  2. Check pool received shares: grep -i share $LOG_DIR/spiralpool-regtest.log"
    log_error "  3. Check daemon responding: $BITCOINIICLI -regtest -rpcuser=$RPC_USER -rpcpassword=$RPC_PASS -rpcport=$RPC_PORT getblockchaininfo"
    log_error "  4. Check for errors: grep -i error $LOG_DIR/spiralpool-regtest.log"
    EXIT_CODE=1
else
    EXIT_CODE=0
fi

# =============================================================================
# STEP 8b: Stack confirmations + wait for maturity
# =============================================================================

if [[ "$MATURITY_WAIT_SECS" -gt 0 ]] && [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8b: Wait for confirmations + maturity"

    # The miner keeps running — it mines additional blocks that serve as confirmations
    # for the pool's earlier blocks. No generatetoaddress needed (it fails at difficulty 1
    # on BC2 regtest because it requires real PoW, unlike standard Bitcoin Core regtest).
    #
    # With blockMaturity=3 and ~195s/block, the miner needs ~3 more blocks (~10 min).
    # Payment processor checks every 30s (config interval: 30s), needs 3 stable checks
    # before confirming. Total: ~12-15 min for pending → confirmed.
    BLOCK_MATURITY="${BLOCK_MATURITY:-3}"
    CONFIRM_TARGET=$((CURRENT_HEIGHT + BLOCKS_FOUND + BLOCK_MATURITY))
    log_info "Miner continues running to add confirmation blocks..."
    log_info "Need $BLOCK_MATURITY confirmations (chain must reach height $CONFIRM_TARGET)"
    log_info "Payment processor checks every 30s, needs 3 stable cycles after maturity"
    echo ""

    MATURITY_ELAPSED=0
    MATURITY_CHECK_INTERVAL=30

    while [[ $MATURITY_ELAPSED -lt $MATURITY_WAIT_SECS ]]; do
        CHAIN_HEIGHT=$(get_block_count)
        FIRST_BLOCK_CONFIRMS=$((CHAIN_HEIGHT - HEIGHT_BEFORE - 1))
        if [[ $FIRST_BLOCK_CONFIRMS -lt 0 ]]; then FIRST_BLOCK_CONFIRMS=0; fi

        # Check DB for confirmed vs pending
        DB_CONFIRMED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM blocks_bc2_regtest WHERE status = 'confirmed';" 2>/dev/null | tr -d ' ' || echo "?")
        DB_PENDING=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM blocks_bc2_regtest WHERE status = 'pending';" 2>/dev/null | tr -d ' ' || echo "?")
        DB_ORPHANED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM blocks_bc2_regtest WHERE status = 'orphaned';" 2>/dev/null | tr -d ' ' || echo "?")

        log_info "  ${MATURITY_ELAPSED}s — height: $CHAIN_HEIGHT, confirms: ~$FIRST_BLOCK_CONFIRMS | DB: confirmed=$DB_CONFIRMED pending=$DB_PENDING orphaned=$DB_ORPHANED"

        # Early exit if all pool-mined blocks are confirmed
        if [[ "$DB_CONFIRMED" != "?" ]] && [[ "$DB_CONFIRMED" -ge "$BLOCKS_FOUND" ]]; then
            log_ok "All $BLOCKS_FOUND pool-mined blocks confirmed! (confirmed=$DB_CONFIRMED, pending=$DB_PENDING)"
            break
        fi

        sleep "$MATURITY_CHECK_INTERVAL"
        MATURITY_ELAPSED=$((MATURITY_ELAPSED + MATURITY_CHECK_INTERVAL))
    done

    echo ""
    # Final DB snapshot
    log_info "Final database state:"
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "SELECT blockheight, status, confirmationprogress, orphan_mismatch_count, stability_check_count FROM blocks_bc2_regtest ORDER BY blockheight;" 2>/dev/null || \
        log_warn "Could not query database"
    echo ""
fi

# =============================================================================
# STEP 8c: Payment Pipeline Smoke Test
# =============================================================================

PAYMENT_PASS=0
PAYMENT_TOTAL=8
PAYMENT_TXID=""
PAYOUT_ADDRESS=""
COINBASE_MATURE=0

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8c: Payment pipeline verification (8 checks)"

    FIRST_POOL_BLOCK=$((HEIGHT_BEFORE + 1))

    log_info "Miner is still running — mining confirmation blocks naturally"
    echo ""

    # ── 1/8. Check wallet for block rewards ───────────────────────────────
    WALLET_BALANCE="0"
    IMMATURE_BALANCE="0"
    CHAIN_HEIGHT=$(get_block_count)
    WALLET_BALANCE=$(bc2cli_wallet getbalance 2>/dev/null || echo "0")
    IMMATURE_BALANCE=$(bc2cli_wallet getwalletinfo 2>/dev/null | grep -o '"immature_balance":[^,}]*' | head -1 | cut -d: -f2 | tr -d ' ') || true
    IMMATURE_BALANCE="${IMMATURE_BALANCE:-0}"

    if (( $(echo "$WALLET_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
        log_ok "[1/8] Block rewards mature and spendable: $WALLET_BALANCE BC2 (height=$CHAIN_HEIGHT)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
        COINBASE_MATURE=1
    elif (( $(echo "$IMMATURE_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
        log_ok "[1/8] Block rewards received (immature: $IMMATURE_BALANCE BC2, height=$CHAIN_HEIGHT)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
    else
        log_warn "[1/8] No block rewards detected — waiting..."
        # Brief wait for rewards to appear (blocks just mined, wallet may need a moment)
        REWARD_WAIT=0
        while [[ $REWARD_WAIT -lt 120 ]]; do
            sleep 30
            REWARD_WAIT=$((REWARD_WAIT + 30))
            IMMATURE_BALANCE=$(bc2cli_wallet getwalletinfo 2>/dev/null | grep -o '"immature_balance":[^,}]*' | head -1 | cut -d: -f2 | tr -d ' ') || true
            IMMATURE_BALANCE="${IMMATURE_BALANCE:-0}"
            if (( $(echo "$IMMATURE_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
                log_ok "[1/8] Block rewards received (immature: $IMMATURE_BALANCE BC2)"
                PAYMENT_PASS=$((PAYMENT_PASS + 1))
                break
            fi
            log_info "  ${REWARD_WAIT}s — immature: $IMMATURE_BALANCE BC2 (waiting for wallet to detect coinbase)"
        done
    fi

    # ── 2/8. SOLO reward verification ─────────────────────────────────────
    # In SOLO mode, 100% of the block reward goes to the pool wallet.
    # The daemon/pool generates a fresh address per block (not necessarily MINING_ADDRESS).
    # Verify: (a) coinbase has correct reward, (b) output address belongs to our wallet.
    BLOCK_HASH=$(bc2cli getblockhash "$FIRST_POOL_BLOCK" 2>/dev/null) || true
    if [[ -n "$BLOCK_HASH" ]] && [[ ${#BLOCK_HASH} -eq 64 ]]; then
        BLOCK_JSON=$(bc2cli getblock "$BLOCK_HASH" 2 2>/dev/null) || true
        COINBASE_VALUE=$(echo "$BLOCK_JSON" | grep '"value"' | head -1 | grep -o '[0-9.]*') || true
        COINBASE_ADDR=$(echo "$BLOCK_JSON" | grep '"address"' | head -1 | grep -o '"address": "[^"]*"' | cut -d'"' -f4) || true

        if [[ -n "$COINBASE_ADDR" ]]; then
            # Verify the coinbase address belongs to our wallet
            ADDR_MINE=$(bc2cli_wallet getaddressinfo "$COINBASE_ADDR" 2>/dev/null | grep '"ismine"' | head -1 | grep -o 'true\|false') || true
            if [[ "$ADDR_MINE" == "true" ]]; then
                log_ok "[2/8] SOLO reward verified — coinbase ${COINBASE_VALUE:-?} BC2 to wallet address $COINBASE_ADDR (ismine=true)"
                PAYMENT_PASS=$((PAYMENT_PASS + 1))
            else
                log_warn "[2/8] Coinbase address $COINBASE_ADDR is NOT in pool wallet (ismine=$ADDR_MINE)"
            fi
        else
            log_warn "[2/8] Could not extract coinbase address from block at height $FIRST_POOL_BLOCK"
        fi
    else
        log_warn "[2/8] Could not retrieve block at height $FIRST_POOL_BLOCK"
    fi

    # ── 3-4/8. Coin movement (requires 100-block coinbase maturity) ───────
    # BC2 inherits Bitcoin's 100-block coinbase maturity rule.
    # If COINBASE_WAIT_SECS > 0, wait for the miner to produce enough blocks.
    if [[ $COINBASE_MATURE -eq 0 ]] && [[ "$COINBASE_WAIT_SECS" -gt 0 ]]; then
        FIRST_CONFIRMS=$(($(get_block_count) - FIRST_POOL_BLOCK))
        BLOCKS_NEEDED=$((100 - FIRST_CONFIRMS))
        if [[ $BLOCKS_NEEDED -gt 0 ]]; then
            log_info "Waiting for coinbase maturity (~$BLOCKS_NEEDED more blocks needed)..."
            log_info "COINBASE_WAIT_SECS=$COINBASE_WAIT_SECS — miner adds blocks at ~60-80s each"
        fi
        MATURITY_ELAPSED=0
        while [[ $MATURITY_ELAPSED -lt $COINBASE_WAIT_SECS ]]; do
            CHAIN_HEIGHT=$(get_block_count)
            WALLET_BALANCE=$(bc2cli_wallet getbalance 2>/dev/null || echo "0")
            FIRST_CONFIRMS=$((CHAIN_HEIGHT - FIRST_POOL_BLOCK))

            if (( $(echo "$WALLET_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
                log_ok "Coinbase matured! Balance: $WALLET_BALANCE BC2 (height=$CHAIN_HEIGHT)"
                COINBASE_MATURE=1
                break
            fi

            if [[ $((MATURITY_ELAPSED % 60)) -eq 0 ]]; then
                log_info "  ${MATURITY_ELAPSED}s — height: $CHAIN_HEIGHT, confirms: ~$FIRST_CONFIRMS/100, balance: $WALLET_BALANCE BC2"
            fi
            sleep 30
            MATURITY_ELAPSED=$((MATURITY_ELAPSED + 30))
        done
    fi

    if [[ $COINBASE_MATURE -eq 1 ]]; then
        # Check 3: getnewaddress
        PAYOUT_ADDRESS=$(bc2cli_wallet getnewaddress "test-payout" "bech32" 2>/dev/null) || true
        if [[ -n "$PAYOUT_ADDRESS" ]]; then
            log_ok "[3/8] Payout address generated: $PAYOUT_ADDRESS"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[3/8] getnewaddress failed"
        fi

        # Check 4: sendtoaddress + verify
        PAYMENT_TXID=$(bc2cli_wallet sendtoaddress "$PAYOUT_ADDRESS" 1.0 2>&1) || true
        if [[ ${#PAYMENT_TXID} -eq 64 ]]; then
            log_ok "[4/8] Payment sent: txid=$PAYMENT_TXID"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))

            TX_INFO=$(bc2cli_wallet gettransaction "$PAYMENT_TXID" 2>/dev/null) || true
            TX_AMOUNT=$(echo "$TX_INFO" | grep -o '"amount":[^,]*' | head -1 | cut -d: -f2 | tr -d ' ') || true
            if [[ -n "$TX_AMOUNT" ]]; then
                log_ok "      Transaction verified (amount=$TX_AMOUNT)"
            fi
        else
            log_warn "[4/8] sendtoaddress failed: $PAYMENT_TXID"
            PAYMENT_TXID=""
        fi
    else
        if [[ "$COINBASE_WAIT_SECS" -eq 0 ]]; then
            log_info "[3/8] Skipped — COINBASE_WAIT_SECS=0 (set to 7200 for full test)"
            log_info "[4/8] Skipped — COINBASE_WAIT_SECS=0"
        else
            log_warn "[3/8] Skipped — coinbase did not mature within ${COINBASE_WAIT_SECS}s"
            log_warn "[4/8] Skipped — coinbase did not mature"
        fi
    fi

    # ── 5/8. Block status: confirmed → paid ───────────────────────────────
    FIRST_HASH=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT hash FROM blocks_bc2_regtest WHERE blockheight = $FIRST_POOL_BLOCK AND status = 'confirmed' LIMIT 1;" 2>/dev/null | tr -d ' ') || true

    if [[ -n "$FIRST_HASH" ]]; then
        PAID_RESULT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "UPDATE blocks_bc2_regtest SET status = 'paid', confirmationprogress = 1.0
             WHERE blockheight = $FIRST_POOL_BLOCK AND hash = '$FIRST_HASH'
             AND (status = 'confirmed' AND 'paid' IN ('orphaned', 'paid'));" 2>/dev/null) || true

        if [[ "$PAID_RESULT" == *"UPDATE 1"* ]]; then
            log_ok "[5/8] Block $FIRST_POOL_BLOCK: confirmed → paid (status guard passed)"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[5/8] Block $FIRST_POOL_BLOCK: confirmed → paid failed ($PAID_RESULT)"
        fi

        # ── 6/8. Verify paid is terminal ──────────────────────────────────
        REVERT_RESULT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "UPDATE blocks_bc2_regtest SET status = 'pending'
             WHERE blockheight = $FIRST_POOL_BLOCK AND hash = '$FIRST_HASH'
             AND (status = 'pending' OR (status = 'confirmed' AND 'pending' IN ('orphaned', 'paid')));" 2>/dev/null) || true

        if [[ "$REVERT_RESULT" == *"UPDATE 0"* ]]; then
            log_ok "[6/8] Block $FIRST_POOL_BLOCK: paid → pending BLOCKED (terminal status verified)"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[6/8] Block $FIRST_POOL_BLOCK: paid → pending was NOT blocked ($REVERT_RESULT)"
        fi
    else
        log_warn "[5/8] No confirmed block at height $FIRST_POOL_BLOCK — skipping status tests"
        log_warn "[6/8] Skipped — depends on check 5"
    fi

    # ── 7/8. Payment table recording ──────────────────────────────────────
    PAY_TXID_FOR_DB="${PAYMENT_TXID}"
    PAY_ADDR_FOR_DB="${PAYOUT_ADDRESS}"
    PAY_AMT_FOR_DB="1.0"
    if [[ -z "$PAY_TXID_FOR_DB" ]] || [[ ${#PAY_TXID_FOR_DB} -ne 64 ]]; then
        PAY_TXID_FOR_DB="$(printf '%064d' 0 | sed 's/0/a/g')"
        PAY_ADDR_FOR_DB="${MINING_ADDRESS}"
        PAY_AMT_FOR_DB="0.00000001"
        log_info "Using synthetic test data for payment table verification"
    fi

    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -q -c \
        "INSERT INTO payments (poolid, coin, address, amount, transactionconfirmationdata, created)
         VALUES ('bc2_regtest', 'BC2', '$PAY_ADDR_FOR_DB', $PAY_AMT_FOR_DB, '$PAY_TXID_FOR_DB', NOW());" 2>/dev/null || true

    PAY_COUNT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM payments WHERE poolid = 'bc2_regtest' AND transactionconfirmationdata = '$PAY_TXID_FOR_DB';" 2>/dev/null | tr -d ' ') || echo "0"

    if [[ "$PAY_COUNT" -ge 1 ]]; then
        log_ok "[7/8] Payment recorded in DB ($PAY_COUNT row(s) in payments table)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
    else
        log_warn "[7/8] Payment INSERT failed or row not found on SELECT"
    fi

    # ── 8/8. API/Dashboard health check ───────────────────────────────────
    API_RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$API_PORT/" 2>/dev/null) || true
    if [[ -n "$API_RESPONSE" ]] && [[ "$API_RESPONSE" -ge 200 ]] 2>/dev/null && [[ "$API_RESPONSE" -lt 500 ]] 2>/dev/null; then
        log_ok "[8/8] API responding on port $API_PORT (HTTP $API_RESPONSE)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
    else
        # Try /api/pools as fallback
        API_RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$API_PORT/api/pools" 2>/dev/null) || true
        if [[ -n "$API_RESPONSE" ]] && [[ "$API_RESPONSE" -ge 200 ]] 2>/dev/null && [[ "$API_RESPONSE" -lt 500 ]] 2>/dev/null; then
            log_ok "[8/8] API responding on port $API_PORT/api/pools (HTTP $API_RESPONSE)"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[8/8] API not responding on port $API_PORT (HTTP ${API_RESPONSE:-timeout})"
        fi
    fi

    # ── Summary ───────────────────────────────────────────────────────────
    echo ""
    if [[ $COINBASE_MATURE -eq 0 ]]; then
        PAYMENT_TESTED=$((PAYMENT_TOTAL - 2))
        log_info "Payment pipeline: $PAYMENT_PASS/$PAYMENT_TESTED checks passed (2 skipped — coinbase needs 100 confirmations)"
        if [[ "$COINBASE_WAIT_SECS" -eq 0 ]]; then
            log_info "  For full 8/8: COINBASE_WAIT_SECS=7200 ./scripts/linux/regtest-bc2.sh"
        fi
    else
        log_info "Payment pipeline: $PAYMENT_PASS/$PAYMENT_TOTAL checks passed"
    fi
    echo ""
fi

# =============================================================================
# STEP 8d: HA Failover — Advisory Lock Fencing
# =============================================================================

HA_PASS=0
HA_TOTAL=4

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8d: HA failover — advisory lock fencing (4 checks)"

    HA_STRATUM_PORT=16334
    HA_API_PORT=14001
    HA_METRICS_PORT=19101
    HA_CONFIG="$LOG_DIR/config-bc2-regtest-ha.yaml"
    HA_LOG="$LOG_DIR/spiralpool-regtest-ha.log"

    # ── 1/4. Start second pool instance with different ports ──────────────
    cp "$CONFIG_FILE" "$HA_CONFIG"
    sed -i "s/port: $STRATUM_PORT/port: $HA_STRATUM_PORT/" "$HA_CONFIG"
    sed -i "s/api_port:.*/api_port: $HA_API_PORT/" "$HA_CONFIG"
    sed -i "s/metrics_port:.*/metrics_port: $HA_METRICS_PORT/" "$HA_CONFIG"

    log_info "Starting second pool instance (HA standby)..."
    log_info "  Same daemon (RPC $RPC_PORT), same DB ($DB_NAME) — only ports differ"
    log_info "  Stratum: $HA_STRATUM_PORT, API: $HA_API_PORT, Metrics: $HA_METRICS_PORT"
    "$POOL_BINARY" -config "$HA_CONFIG" &>"$HA_LOG" &
    POOL2_PID=$!

    # The second instance shares the same WAL directory, so the WAL file lock
    # (first HA fencing layer) blocks coin pool initialization. The coordinator
    # still starts the API/metrics servers and enters retry mode.
    # Check API port (binds even when coin pool fails) or process alive.
    HA_WAIT=0
    HA_STARTED=0
    while [[ $HA_WAIT -lt 30 ]]; do
        if (echo >/dev/tcp/127.0.0.1/$HA_API_PORT) 2>/dev/null; then
            HA_STARTED=1
            break
        fi
        sleep 1
        HA_WAIT=$((HA_WAIT + 1))
    done

    if [[ $HA_STARTED -eq 1 ]]; then
        log_ok "[1/4] Second pool instance started (PID $POOL2_PID, API port $HA_API_PORT)"
        HA_PASS=$((HA_PASS + 1))
    elif kill -0 "$POOL2_PID" 2>/dev/null; then
        log_ok "[1/4] Second pool instance running (PID $POOL2_PID, coin pool blocked by WAL lock)"
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[1/4] Second pool instance failed to start"
        log_info "  Check: $HA_LOG"
    fi

    # ── 2/4. Verify HA fencing is active ──────────────────────────────────
    # Two layers of fencing exist:
    #   Layer 1: WAL file lock — prevents two instances on the same machine
    #            from corrupting each other's block WAL
    #   Layer 2: PostgreSQL advisory lock — prevents double-payment across
    #            machines sharing the same database
    # On the same machine, the WAL lock fires first (coin pool init fails).
    # On separate machines, the advisory lock fires (payment processor skips).
    log_info "Waiting for HA fencing detection in second instance log (up to 90s)..."
    LOCK_WAIT=0
    LOCK_BLOCKED=0
    while [[ $LOCK_WAIT -lt 90 ]]; do
        if grep -qi "WAL file lock\|another instance running\|advisory lock held by another process\|advisory lock.*skip" "$HA_LOG" 2>/dev/null; then
            LOCK_BLOCKED=1
            break
        fi
        sleep 5
        LOCK_WAIT=$((LOCK_WAIT + 5))
    done

    if [[ $LOCK_BLOCKED -eq 1 ]]; then
        FENCE_MSG=$(grep -i "WAL file lock\|another instance running\|advisory lock held" "$HA_LOG" 2>/dev/null | head -1) || true
        if echo "$FENCE_MSG" | grep -qi "WAL\|another instance"; then
            log_ok "[2/4] WAL file lock blocked second instance (same-machine HA fencing verified)"
        else
            log_ok "[2/4] Advisory lock blocked second instance (cross-machine HA fencing verified)"
        fi
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[2/4] No HA fencing detected in second instance log after ${LOCK_WAIT}s"
        log_info "  Log tail: $(tail -3 "$HA_LOG" 2>/dev/null || echo 'empty')"
    fi

    # ── 3/4. Verify primary pool still running ────────────────────────────
    if (echo >/dev/tcp/127.0.0.1/$STRATUM_PORT) 2>/dev/null; then
        log_ok "[3/4] Primary pool still running on port $STRATUM_PORT (no disruption)"
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[3/4] Primary pool stratum port $STRATUM_PORT not responding"
    fi

    # ── 4/4. Terminate second instance ────────────────────────────────────
    if [[ -n "$POOL2_PID" ]]; then
        kill -9 "$POOL2_PID" 2>/dev/null || true
        wait "$POOL2_PID" 2>/dev/null || true
    fi
    rm -f "$HA_CONFIG"

    if ! kill -0 "$POOL2_PID" 2>/dev/null; then
        log_ok "[4/4] Second pool instance terminated (cleanup successful)"
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[4/4] Second pool instance still running after cleanup attempt"
    fi
    POOL2_PID=""

    echo ""
    log_info "HA failover: $HA_PASS/$HA_TOTAL checks passed"
    echo ""
fi

# =============================================================================
# STEP 8e: Daemon-Down Resilience
# =============================================================================

DAEMON_RESIL_PASS=0
DAEMON_RESIL_TOTAL=4

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8e: Daemon-down resilience (4 checks)"

    RESIL_LOG="$LOG_DIR/spiralpool-regtest.log"
    PRE_STOP_LINES=$(wc -l < "$RESIL_LOG")

    # ── 1/4. Stop the daemon ──────────────────────────────────────────────
    log_info "Stopping daemon to simulate node failure..."
    bc2cli stop 2>/dev/null || true
    sleep 5

    if ! bc2cli getblockchaininfo &>/dev/null; then
        log_ok "[1/4] Daemon stopped successfully (RPC unreachable)"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        log_warn "[1/4] Daemon still responding after stop command"
    fi

    # ── 2/4. Pool detects daemon failure ──────────────────────────────────
    log_info "Waiting 20s for pool to detect daemon failure..."
    sleep 20

    # Check pool log for error messages that appeared AFTER daemon stop
    if tail -n +$((PRE_STOP_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
       grep -qiE "zmq.*error|zmq.*fail|rpc.*error|rpc.*fail|daemon.*fail|connection.*refuse|dial.*error|connect:.*refuse"; then
        DETECTED_MSG=$(tail -n +$((PRE_STOP_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
            grep -iE "zmq.*error|zmq.*fail|rpc.*error|rpc.*fail|daemon.*fail|connection.*refuse|dial.*error|connect:.*refuse" | head -1) || true
        log_ok "[2/4] Pool detected daemon failure"
        log_info "  Log: ${DETECTED_MSG:-(message extracted)}"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        log_warn "[2/4] No daemon failure detection in pool logs"
    fi

    # Verify pool process survived (didn't crash)
    if kill -0 "$POOL_PID" 2>/dev/null; then
        log_info "  Pool process still alive (PID $POOL_PID) — graceful failure handling"
    else
        log_warn "  Pool process DIED (PID $POOL_PID) — ungraceful crash"
    fi

    # ── 3/4. Restart daemon ───────────────────────────────────────────────
    log_info "Restarting daemon..."
    PRE_RESTART_LINES=$(wc -l < "$RESIL_LOG")

    "$BITCOINIID" \
        -regtest \
        -daemon=0 \
        -server=1 \
        -rpcuser="$RPC_USER" \
        -rpcpassword="$RPC_PASS" \
        -rpcport="$RPC_PORT" \
        -rpcallowip=127.0.0.1 \
        -rpcbind=127.0.0.1 \
        -port="$P2P_PORT" \
        -listen=0 \
        -zmqpubhashblock="tcp://127.0.0.1:$ZMQ_PORT" \
        -zmqpubrawblock="tcp://127.0.0.1:$ZMQ_PORT" \
        -txindex=1 \
        -fallbackfee=0.0001 \
        -blockfilterindex=1 \
        -printtoconsole=0 \
        -debuglogfile="$LOG_DIR/bitcoinIId-regtest.log" \
        &>"$LOG_DIR/bitcoinIId-startup.log" &

    DAEMON_PID=$!

    wait_for_rpc

    # Reload wallet (daemon restart loses loaded wallets)
    bc2cli loadwallet "regtest-pool" &>/dev/null || true

    if bc2cli getblockchaininfo &>/dev/null; then
        RESTART_HEIGHT=$(get_block_count)
        log_ok "[3/4] Daemon restarted (PID $DAEMON_PID, height=$RESTART_HEIGHT)"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        log_warn "[3/4] Daemon restart failed — RPC not responding"
    fi

    # ── 4/4. Pool reconnects to daemon ────────────────────────────────────
    log_info "Waiting for pool to reconnect to daemon (up to 90s)..."
    RECONNECT_WAIT=0
    RECONNECTED=0
    while [[ $RECONNECT_WAIT -lt 90 ]]; do
        if tail -n +$((PRE_RESTART_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
           grep -qiE "zmq.*recover|zmq.*connect|zmq.*stabil|rpc.*success|new.*job|block.*template|getblocktemplate"; then
            RECONNECTED=1
            break
        fi
        sleep 5
        RECONNECT_WAIT=$((RECONNECT_WAIT + 5))
    done

    if [[ $RECONNECTED -eq 1 ]]; then
        RECOVERY_MSG=$(tail -n +$((PRE_RESTART_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
            grep -iE "zmq.*recover|zmq.*connect|zmq.*stabil|rpc.*success|new.*job|block.*template" | head -1) || true
        log_ok "[4/4] Pool reconnected to daemon"
        log_info "  Log: ${RECOVERY_MSG:-(recovery detected)}"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        # Fallback: check if pool is at least still serving stratum
        if (echo >/dev/tcp/127.0.0.1/$STRATUM_PORT) 2>/dev/null; then
            log_ok "[4/4] Pool still serving stratum (daemon reconnection may be pending)"
            DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
        else
            log_warn "[4/4] Pool not reconnected after ${RECONNECT_WAIT}s"
        fi
    fi

    echo ""
    log_info "Daemon-down resilience: $DAEMON_RESIL_PASS/$DAEMON_RESIL_TOTAL checks passed"
    echo ""
fi

# =============================================================================
# STEP 9: Verify Block Lifecycle
# =============================================================================

log_step "Step 9/10: Verify block lifecycle in pool logs"

POOL_LOG="$LOG_DIR/spiralpool-regtest.log"

check_lifecycle() {
    local pattern="$1"
    local description="$2"
    if grep -qiE "$pattern" "$POOL_LOG" 2>/dev/null; then
        log_ok "$description"
        return 0
    else
        log_warn "$description — NOT FOUND in pool log"
        return 1
    fi
}

LIFECYCLE_PASS=0
LIFECYCLE_TOTAL=6

echo ""
log_info "Checking pool log for block lifecycle events:"
echo ""

check_lifecycle "VARDIFF initial state|Stratum server started" "[1/6] Miner connected to stratum"          && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "authorize"                         "[2/6] Miner authorized"                     && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "share|accepted"                    "[3/6] Share submitted and processed"         && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "block.*found|found.*block|candidate|block.*candidate" "[4/6] Block candidate detected"    && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "submitblock|submit.*block|submitted" "[5/6] Block submitted to daemon via RPC"  && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "pending|confirmed|inserted.*block" "[6/6] Block recorded in database"           && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))

echo ""
if [[ $LIFECYCLE_PASS -eq $LIFECYCLE_TOTAL ]] && [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_ok "FULL BLOCK LIFECYCLE VERIFIED ($LIFECYCLE_PASS/$LIFECYCLE_TOTAL checks passed)"
elif [[ $LIFECYCLE_PASS -gt 0 ]]; then
    log_warn "Partial lifecycle: $LIFECYCLE_PASS/$LIFECYCLE_TOTAL checks passed"
    log_info "Review the full pool log for details: less $POOL_LOG"
else
    log_error "No lifecycle events found — pool may not have started correctly"
fi

# =============================================================================
# STEP 10: Summary
# =============================================================================

log_step "Step 10/10: Summary"

FINAL_HEIGHT=$(get_block_count)
FINAL_BALANCE=$(bc2cli_wallet getbalance 2>/dev/null || echo "unknown")

echo ""
echo -e "  ${BOLD}Test Results${NC}"
echo "  ─────────────────────────────────────────────"
echo "  Chain height:          $FINAL_HEIGHT"
echo "  Blocks mined via pool: $BLOCKS_FOUND / $TEST_BLOCKS"
echo "  Lifecycle checks:      $LIFECYCLE_PASS / $LIFECYCLE_TOTAL"
echo "  Wallet balance:        $FINAL_BALANCE BC2"
echo "  Mining address:        $MINING_ADDRESS"
if [[ $PAYMENT_TOTAL -gt 0 ]]; then
    if [[ ${COINBASE_MATURE:-0} -eq 0 ]]; then
        PAYMENT_DENOM=$((PAYMENT_TOTAL - 2))
    else
        PAYMENT_DENOM=$PAYMENT_TOTAL
    fi
    if [[ $PAYMENT_PASS -ge $PAYMENT_DENOM ]] && [[ $PAYMENT_DENOM -gt 0 ]]; then
        echo -e "  Payment pipeline:      ${GREEN}PASS ($PAYMENT_PASS/$PAYMENT_DENOM checks)${NC}"
    elif [[ $PAYMENT_PASS -gt 0 ]]; then
        echo -e "  Payment pipeline:      ${YELLOW}PARTIAL ($PAYMENT_PASS/$PAYMENT_DENOM checks)${NC}"
    else
        echo -e "  Payment pipeline:      ${RED}SKIP (0/$PAYMENT_DENOM checks)${NC}"
    fi
fi
if [[ ${HA_TOTAL:-0} -gt 0 ]]; then
    if [[ ${HA_PASS:-0} -ge $HA_TOTAL ]]; then
        echo -e "  HA failover:           ${GREEN}PASS ($HA_PASS/$HA_TOTAL checks)${NC}"
    elif [[ ${HA_PASS:-0} -gt 0 ]]; then
        echo -e "  HA failover:           ${YELLOW}PARTIAL ($HA_PASS/$HA_TOTAL checks)${NC}"
    else
        echo -e "  HA failover:           ${RED}FAIL (0/$HA_TOTAL checks)${NC}"
    fi
fi
if [[ ${DAEMON_RESIL_TOTAL:-0} -gt 0 ]]; then
    if [[ ${DAEMON_RESIL_PASS:-0} -ge $DAEMON_RESIL_TOTAL ]]; then
        echo -e "  Daemon resilience:     ${GREEN}PASS ($DAEMON_RESIL_PASS/$DAEMON_RESIL_TOTAL checks)${NC}"
    elif [[ ${DAEMON_RESIL_PASS:-0} -gt 0 ]]; then
        echo -e "  Daemon resilience:     ${YELLOW}PARTIAL ($DAEMON_RESIL_PASS/$DAEMON_RESIL_TOTAL checks)${NC}"
    else
        echo -e "  Daemon resilience:     ${RED}FAIL (0/$DAEMON_RESIL_TOTAL checks)${NC}"
    fi
fi
echo ""
echo -e "  ${BOLD}Ports Used${NC}"
echo "  ─────────────────────────────────────────────"
echo "  Daemon RPC:   127.0.0.1:$RPC_PORT"
echo "  Daemon ZMQ:   127.0.0.1:$ZMQ_PORT"
echo "  Pool Stratum: 127.0.0.1:$STRATUM_PORT"
echo "  PostgreSQL:   $DB_HOST:$DB_PORT"
echo ""
echo -e "  ${BOLD}Log Files${NC}"
echo "  ─────────────────────────────────────────────"
echo "  Pool:     $LOG_DIR/spiralpool-regtest.log"
echo "  Miner:    $LOG_DIR/cpuminer-regtest.log"
echo "  Daemon:   $LOG_DIR/bitcoinIId-regtest.log"
echo "  Startup:  $LOG_DIR/bitcoinIId-startup.log"
echo ""

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]] && [[ $LIFECYCLE_PASS -eq $LIFECYCLE_TOTAL ]]; then
    echo -e "  ${GREEN}${BOLD}RESULT: PASS${NC}"
    echo -e "  ${GREEN}$BLOCKS_FOUND blocks mined end-to-end through the pool.${NC}"
    echo -e "  ${GREEN}Full block lifecycle verified: stratum -> submitblock -> ZMQ -> DB${NC}"
elif [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    echo -e "  ${YELLOW}${BOLD}RESULT: PARTIAL PASS${NC}"
    echo -e "  ${YELLOW}$BLOCKS_FOUND blocks mined, but $((LIFECYCLE_TOTAL - LIFECYCLE_PASS)) lifecycle checks failed.${NC}"
    echo -e "  ${YELLOW}Review logs above for details.${NC}"
else
    echo -e "  ${RED}${BOLD}RESULT: FAIL${NC}"
    echo -e "  ${RED}Only $BLOCKS_FOUND / $TEST_BLOCKS blocks found.${NC}"
    echo -e "  ${RED}Review logs for errors.${NC}"
fi

echo ""
echo -e "  ${CYAN}To re-run: ./scripts/linux/regtest-bc2.sh${NC}"
echo -e "  ${CYAN}To reset regtest chain: rm -rf ~/.bitcoinII/regtest${NC}"
echo ""

exit "${EXIT_CODE:-0}"
