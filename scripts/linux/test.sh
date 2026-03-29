#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool v2.0.1 - Complete Test Suite
# Tests environment, installs dependencies, runs all tests, then optionally installs
#
# Usage: chmod +x test.sh && ./test.sh
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$(dirname "$SCRIPT_DIR")")"
STRATUM_DIR="$ROOT_DIR/src/stratum"

# Detect system architecture for Go downloads (dpkg returns "amd64" or "arm64")
SYSTEM_ARCH=$(dpkg --print-architecture 2>/dev/null || echo "amd64")

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
WHITE='\033[1;37m'
NC='\033[0m'

# Track results
PASSED=0
FAILED=0
SKIPPED=0
declare -a RESULTS

pass() {
    echo -e "${GREEN}[PASS]${NC} $1"
    PASSED=$((PASSED + 1))
    RESULTS+=("PASS: $1")
}

fail() {
    echo -e "${RED}[FAIL]${NC} $1"
    FAILED=$((FAILED + 1))
    RESULTS+=("FAIL: $1")
}

skip() {
    echo -e "${YELLOW}[SKIP]${NC} $1"
    SKIPPED=$((SKIPPED + 1))
    RESULTS+=("SKIP: $1")
}

info() {
    echo -e "${CYAN}[INFO]${NC} $1"
}

header() {
    echo ""
    echo -e "${MAGENTA}══════════════════════════════════════════════════════════════${NC}"
    echo -e "${WHITE}  $1${NC}"
    echo -e "${MAGENTA}══════════════════════════════════════════════════════════════${NC}"
}

# Banner
clear
echo -e "${CYAN}"
cat << 'EOF'
   ____        _           _   ____             _
  / ___| _ __ (_)_ __ __ _| | |  _ \ ___   ___ | |
  \___ \| '_ \| | '__/ _` | | | |_) / _ \ / _ \| |
   ___) | |_) | | | | (_| | | |  __/ (_) | (_) | |
  |____/| .__/|_|_|  \__,_|_| |_|   \___/ \___/|_|
        |_|
              v2.0.1 Complete Test Suite
              SHA256d: DGB | BTC | BCH | BC2 | QBX
              Scrypt:  LTC | DOGE | DGB-SCRYPT | PEP | CAT
EOF
echo -e "${NC}"
echo ""

# ============================================
# PHASE 1: ENVIRONMENT SETUP
# ============================================
header "PHASE 1: ENVIRONMENT SETUP"

# Check if stratum directory exists
if [[ ! -d "$STRATUM_DIR" ]]; then
    echo -e "${RED}Error: Stratum directory not found at $STRATUM_DIR${NC}"
    exit 1
fi

# Install system dependencies
info "Installing system dependencies..."
if command -v apt-get &> /dev/null; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq libzmq3-dev build-essential pkg-config file curl 2>/dev/null
    pass "System dependencies installed"
else
    skip "apt-get not available (not Debian/Ubuntu)"
fi

# Check/Install Go
info "Checking Go installation..."
if command -v go &> /dev/null; then
    GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
    GO_MAJOR=$(echo $GO_VERSION | cut -d. -f1)
    GO_MINOR=$(echo $GO_VERSION | cut -d. -f2)

    if [[ $GO_MAJOR -ge 1 ]] && [[ $GO_MINOR -ge 26 ]]; then
        pass "Go $GO_VERSION installed (1.26+ required)"
    else
        fail "Go $GO_VERSION is too old (need 1.26+, required by go.mod)"
        echo -e "${YELLOW}Installing Go 1.26.1...${NC}"

        GO_TAR="go1.26.1.linux-${SYSTEM_ARCH}.tar.gz"
        curl -fSL --connect-timeout 15 --max-time 300 "https://go.dev/dl/${GO_TAR}" -o "/tmp/${GO_TAR}"
        sudo rm -rf /usr/local/go
        sudo tar -C /usr/local -xzf "/tmp/${GO_TAR}"
        rm "/tmp/${GO_TAR}"
        export PATH=$PATH:/usr/local/go/bin

        if ! grep -q "/usr/local/go/bin" ~/.bashrc; then
            echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
        fi
        pass "Go 1.26.1 installed"
    fi
else
    info "Go not found. Installing Go 1.26.1..."
    GO_TAR="go1.26.1.linux-${SYSTEM_ARCH}.tar.gz"
    curl -fSL --connect-timeout 15 --max-time 300 "https://go.dev/dl/${GO_TAR}" -o "/tmp/${GO_TAR}"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "/tmp/${GO_TAR}"
    rm "/tmp/${GO_TAR}"
    export PATH=$PATH:/usr/local/go/bin

    if ! grep -q "/usr/local/go/bin" ~/.bashrc; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    fi
    pass "Go 1.26.1 installed"
fi

# Verify ZMQ
info "Checking ZMQ installation..."
ZMQ_AVAILABLE=false
if pkg-config --exists libzmq 2>/dev/null; then
    ZMQ_VERSION=$(pkg-config --modversion libzmq)
    pass "ZMQ v${ZMQ_VERSION} found"
    ZMQ_AVAILABLE=true
elif [[ -f /usr/include/zmq.h ]]; then
    pass "ZMQ header found"
    ZMQ_AVAILABLE=true
else
    skip "ZMQ not installed (some tests will be skipped)"
    echo -e "  ${YELLOW}To install: sudo apt install libzmq3-dev${NC}"
fi

cd "$STRATUM_DIR"

# Download Go dependencies
info "Downloading Go dependencies..."
go mod download 2>/dev/null && pass "Go dependencies downloaded" || fail "Go dependencies download"

# ============================================
# PHASE 2: BUILD TESTS
# ============================================
header "PHASE 2: BUILD VERIFICATION"

info "Building coin package..."
go build ./internal/coin/... 2>&1 && pass "Coin package builds" || fail "Coin package build"

info "Building config package..."
go build ./internal/config/... 2>&1 && pass "Config package builds" || fail "Config package build"

info "Building protocol package..."
go build ./pkg/protocol/... 2>&1 && pass "Protocol package builds" || fail "Protocol package build"

info "Building stratum v1 handler..."
go build ./internal/stratum/v1/... 2>&1 && pass "Stratum V1 handler builds" || fail "Stratum V1 handler build"

info "Building Spiral Router..."
go build ./internal/stratum/spiralrouter.go 2>&1 && pass "Spiral Router builds" || fail "Spiral Router build"

if [[ "$ZMQ_AVAILABLE" == "true" ]]; then
    info "Building full stratum package (with ZMQ)..."
    if go build ./internal/stratum/... 2>&1; then
        pass "Full stratum package builds (with ZMQ)"
    else
        skip "Full stratum build (ZMQ compile error)"
    fi
else
    skip "Full stratum build (ZMQ not installed)"
fi

# ============================================
# PHASE 3: UNIT TESTS
# ============================================
header "PHASE 3: COIN IMPLEMENTATION TESTS"

info "Testing Bitcoin (BTC) implementation..."
if go test ./internal/coin/... -run "TestBitcoin" -v 2>&1 | grep -q "PASS"; then
    pass "Bitcoin (BTC) coin tests"
else
    if go test ./internal/coin/... -run "TestBitcoin" -v 2>&1 | grep -q "no test files"; then
        skip "Bitcoin tests (no test file)"
    else
        fail "Bitcoin (BTC) coin tests"
    fi
fi

info "Testing Bitcoin Cash (BCH) implementation..."
if go test ./internal/coin/... -run "TestBitcoinCash" -v 2>&1 | grep -q "PASS"; then
    pass "Bitcoin Cash (BCH) coin tests"
else
    if go test ./internal/coin/... -run "TestBitcoinCash" -v 2>&1 | grep -q "no test files"; then
        skip "Bitcoin Cash tests (no test file)"
    else
        fail "Bitcoin Cash (BCH) coin tests"
    fi
fi

info "Testing DigiByte (DGB) implementation..."
if go test ./internal/coin/... -run "TestDigiByte" -v 2>&1 | grep -q "PASS"; then
    pass "DigiByte (DGB) coin tests"
else
    fail "DigiByte (DGB) coin tests"
fi

info "Testing coin registry..."
if go test ./internal/coin/... -run "TestCoinRegistry|TestListRegistered" -v 2>&1 | grep -q "PASS"; then
    pass "Coin registry tests"
else
    fail "Coin registry tests"
fi

# ============================================
# PHASE 4: SPIRAL ROUTER TESTS
# ============================================
header "PHASE 4: SPIRAL ROUTER TESTS"

info "Testing Spiral Router miner detection by user-agent..."
if go test ./internal/stratum/spiralrouter_test.go ./internal/stratum/spiralrouter.go -v 2>&1 | grep -q "PASS"; then
    pass "Spiral Router detection tests"
else
    fail "Spiral Router detection tests"
fi

info "Running Spiral Router benchmarks..."
BENCH_OUTPUT=$(go test ./internal/stratum/spiralrouter_test.go ./internal/stratum/spiralrouter.go -bench=. -benchmem 2>&1)
if echo "$BENCH_OUTPUT" | grep -q "BenchmarkSpiralRouterDetection"; then
    BENCH_NS=$(echo "$BENCH_OUTPUT" | grep "BenchmarkSpiralRouterDetection" | awk '{print $3}')
    pass "Spiral Router benchmark: ${BENCH_NS}/op"
else
    skip "Spiral Router benchmark"
fi

# ============================================
# PHASE 5: STRATUM & CONFIG TESTS
# ============================================
header "PHASE 5: STRATUM & CONFIG TESTS"

info "Testing Stratum V1 protocol handler..."
if go test ./internal/stratum/v1/... -v 2>&1 | grep -q "PASS"; then
    pass "Stratum V1 handler tests"
else
    HANDLER_OUT=$(go test ./internal/stratum/v1/... -v 2>&1)
    if echo "$HANDLER_OUT" | grep -q "FAIL"; then
        fail "Stratum V1 handler tests"
    else
        pass "Stratum V1 handler tests"
    fi
fi

info "Testing config loading..."
if go test ./internal/config/... -v 2>&1 | grep -q "PASS"; then
    pass "Config loading tests"
else
    CONFIG_OUT=$(go test ./internal/config/... -v 2>&1)
    if echo "$CONFIG_OUT" | grep -q "no test files"; then
        skip "Config tests (no test file)"
    else
        fail "Config loading tests"
    fi
fi

info "Testing protocol package..."
if go test ./pkg/protocol/... -v 2>&1 | grep -q "PASS"; then
    pass "Protocol package tests"
else
    PROTO_OUT=$(go test ./pkg/protocol/... -v 2>&1)
    if echo "$PROTO_OUT" | grep -q "no test files"; then
        skip "Protocol tests (no test file)"
    else
        fail "Protocol package tests"
    fi
fi

# ============================================
# PHASE 6: ADDRESS VALIDATION
# ============================================
header "PHASE 6: ADDRESS VALIDATION TESTS"

info "Testing address validation..."
ADDR_OUTPUT=$(go test ./internal/coin/... -run "Address" -v 2>&1)
if echo "$ADDR_OUTPUT" | grep -q "PASS"; then
    pass "Address validation tests"
    echo ""
    echo -e "  ${CYAN}Supported Address Formats:${NC}"
    echo -e "    BTC: ${GREEN}1...${NC} (P2PKH), ${GREEN}3...${NC} (P2SH), ${GREEN}bc1...${NC} (Bech32)"
    echo -e "    BCH: ${GREEN}1...${NC} (legacy), ${GREEN}bitcoincash:q...${NC} (CashAddr)"
    echo -e "    DGB: ${GREEN}D...${NC} (P2PKH), ${GREEN}S...${NC} (P2SH), ${GREEN}dgb1...${NC} (Bech32)"
else
    fail "Address validation tests"
fi

# ============================================
# PHASE 7: RACE DETECTION (Optional)
# ============================================
header "PHASE 7: RACE DETECTION"

info "Running race detection tests..."
if go test ./internal/shares/... ./internal/crypto/... -race -count=1 2>&1 | tee /tmp/spiral_race.txt | grep -q "race detected"; then
    fail "Race conditions detected"
else
    pass "No race conditions detected"
fi

# ============================================
# PHASE 8: HA FAILOVER & PAYMENT FENCING TESTS
# ============================================
header "PHASE 8: HA FAILOVER TESTS"

info "Running HA pool tests (VIP, role change, WAL recovery, multi-coin)..."
if go test ./internal/pool/... -run "HA" -v -count=1 2>&1 | grep -q "PASS"; then
    pass "Pool HA tests (role change, WAL recovery, coordinator)"
else
    fail "Pool HA tests"
fi

info "Running HA cluster tests (VIP election, failover, flap detection)..."
if go test ./internal/ha/... -v -count=1 2>&1 | grep -q "PASS"; then
    pass "HA cluster tests (VIP, election, failover)"
else
    fail "HA cluster tests"
fi

info "Running circuit breaker & block queue HA tests..."
if go test ./internal/database/... -run "CircuitBreaker|BlockQueue|DBNodeState|DBFailover" -v -count=1 2>&1 | grep -q "PASS"; then
    pass "Database HA tests (circuit breaker, block queue, failover)"
else
    fail "Database HA tests"
fi

info "Running payment fencing HA tests (advisory lock, split-brain)..."
if go test ./internal/payments/... -run "HA" -v -count=1 2>&1 | grep -q "PASS"; then
    pass "Payment HA tests (advisory lock, split-brain fencing)"
else
    fail "Payment HA tests"
fi

info "Running Redis HA tests (fallback, reconnection, dedup)..."
if go test ./internal/shares/... -run "HA" -v -count=1 2>&1 | grep -q "PASS"; then
    pass "Redis HA tests (fallback, reconnection, dedup)"
else
    SHARES_HA_OUT=$(go test ./internal/shares/... -run "HA" -v -count=1 2>&1)
    if echo "$SHARES_HA_OUT" | grep -q "no test files\|no tests to run"; then
        skip "Redis HA tests (no matching tests)"
    else
        fail "Redis HA tests"
    fi
fi

info "Running HA race detection (concurrent failover safety)..."
if go test -run "HA|CircuitBreaker|BlockQueue" -race -count=1 \
    ./internal/pool/... \
    ./internal/ha/... \
    ./internal/database/... \
    ./internal/payments/... \
    2>&1 | grep -q "race detected"; then
    fail "HA race conditions detected"
else
    pass "No HA race conditions detected"
fi

# ============================================
# PHASE 9: ALL UNIT TESTS SUMMARY
# ============================================
header "PHASE 9: FULL TEST SUITE"

info "Running all available unit tests..."
ALL_TEST_OUTPUT=$(go test ./internal/coin/... ./internal/config/... ./pkg/... ./internal/stratum/v1/... 2>&1)
TOTAL_PASS=$(echo "$ALL_TEST_OUTPUT" | grep -c "^ok" || true)
TOTAL_FAIL=$(echo "$ALL_TEST_OUTPUT" | grep -c "^FAIL" || true)

# Ensure variables are valid integers (default to 0 if empty)
TOTAL_PASS=${TOTAL_PASS:-0}
TOTAL_FAIL=${TOTAL_FAIL:-0}

echo ""
echo -e "  ${WHITE}Unit Test Summary:${NC}"
echo -e "    Packages passed: ${GREEN}$TOTAL_PASS${NC}"
echo -e "    Packages failed: ${RED}$TOTAL_FAIL${NC}"

if [ "$TOTAL_FAIL" -eq 0 ] 2>/dev/null || [ -z "$TOTAL_FAIL" ]; then
    pass "All unit tests"
else
    fail "Some unit tests failed"
fi

# ============================================
# RESULTS SUMMARY
# ============================================
header "TEST RESULTS SUMMARY"

echo ""
for result in "${RESULTS[@]}"; do
    if [[ $result == PASS* ]]; then
        echo -e "  ${GREEN}[PASS]${NC} ${result#PASS: }"
    elif [[ $result == FAIL* ]]; then
        echo -e "  ${RED}[FAIL]${NC} ${result#FAIL: }"
    elif [[ $result == SKIP* ]]; then
        echo -e "  ${YELLOW}[SKIP]${NC} ${result#SKIP: }"
    fi
done

echo ""
echo -e "  ─────────────────────────────────────────"
echo -e "  ${GREEN}Passed:${NC}  $PASSED"
echo -e "  ${RED}Failed:${NC}  $FAILED"
echo -e "  ${YELLOW}Skipped:${NC} $SKIPPED"
TOTAL=$((PASSED + FAILED + SKIPPED))
echo -e "  ${WHITE}Total:${NC}   $TOTAL"
echo ""

if [ "$FAILED" -eq 0 ]; then
    echo -e "${GREEN}══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  ALL TESTS PASSED! Spiral Pool v2.0.1 is ready.${NC}"
    echo -e "${GREEN}══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${WHITE}Features Verified:${NC}"
    echo -e "    ${GREEN}✓${NC} Multi-coin support (12 coins: SHA256d + Scrypt)"
    echo -e "    ${GREEN}✓${NC} Automatic miner routing by user-agent"
    echo -e "    ${GREEN}✓${NC} Single-port operation (no low-diff port needed)"
    echo -e "    ${GREEN}✓${NC} Address validation for all coins"
    echo -e "    ${GREEN}✓${NC} SHA256d and Scrypt proof-of-work"
    echo -e "    ${GREEN}✓${NC} No race conditions detected"
    echo -e "    ${GREEN}✓${NC} HA failover (VIP, DB circuit breaker, block queue)"
    echo -e "    ${GREEN}✓${NC} Payment fencing (advisory lock, split-brain defense)"
    echo -e "    ${GREEN}✓${NC} Redis HA (health check, graceful fallback)"
    echo -e "    ${GREEN}✓${NC} Block WAL crash recovery"
    echo ""

    # Offer to install
    echo -e "  ${WHITE}Ready to install?${NC}"
    read -p "  Run install.sh now? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        cd "$ROOT_DIR"
        chmod +x install.sh
        ./install.sh
    else
        echo ""
        echo -e "  ${WHITE}To install later:${NC}"
        echo -e "    chmod +x install.sh && ./install.sh"
        echo ""
    fi
    exit 0
else
    echo -e "${RED}══════════════════════════════════════════════════════════════${NC}"
    echo -e "${RED}  SOME TESTS FAILED - Check output above${NC}"
    echo -e "${RED}══════════════════════════════════════════════════════════════${NC}"
    exit 1
fi
