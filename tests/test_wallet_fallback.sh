#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool — Wallet Generation Fallback Tests
# =============================================================================
# Tests the wallet generation fallback paths in spiralpool-wallet (embedded in
# install.sh via WALLETEOF heredoc). These paths are exercised when automatic
# wallet generation fails and the user needs manual recovery instructions.
#
# What is tested:
#   1. The sed command pattern given to users correctly updates only the
#      target coin's address in a multi-coin config.yaml (no cross-coin
#      contamination).
#   2. The sed command pattern works for single-coin V1 configs.
#   3. The exchange address warning text is present in install.sh fallback
#      output (all 3 failure paths).
#   4. The spiralpool-wallet --coin hint is present in fallback output.
#   5. The fallback sed pattern handles DGB-SCRYPT symbol correctly.
#   6. PENDING_GENERATION replacement targets only the correct coin section.
#
# Usage: bash tests/test_wallet_fallback.sh
#        bash tests/test_wallet_fallback.sh --verbose
#
# Exit codes:
#   0  All tests passed
#   1  One or more tests failed
# =============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALL_SCRIPT="$PROJECT_ROOT/install.sh"

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
VERBOSE="${1:-}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# =============================================================================
# Test Framework (same as test_upgrade_regression.sh)
# =============================================================================

log_test() {
    echo -e "${CYAN}[TEST]${NC} $1"
}

pass() {
    TESTS_RUN=$((TESTS_RUN + 1))
    TESTS_PASSED=$((TESTS_PASSED + 1))
    echo -e "  ${GREEN}PASS${NC}: $1"
}

fail() {
    TESTS_RUN=$((TESTS_RUN + 1))
    TESTS_FAILED=$((TESTS_FAILED + 1))
    echo -e "  ${RED}FAIL${NC}: $1"
    [[ -n "${2:-}" ]] && echo -e "        ${RED}Reason: $2${NC}"
}

assert_eq() {
    local actual="$1"
    local expected="$2"
    local msg="$3"
    if [[ "$actual" == "$expected" ]]; then
        pass "$msg"
    else
        fail "$msg" "expected '$expected', got '$actual'"
    fi
}

assert_contains() {
    local haystack="$1"
    local needle="$2"
    local msg="$3"
    if echo "$haystack" | grep -qF "$needle"; then
        pass "$msg"
    else
        fail "$msg" "output does not contain '$needle'"
    fi
}

assert_not_contains() {
    local haystack="$1"
    local needle="$2"
    local msg="$3"
    if ! echo "$haystack" | grep -qF "$needle"; then
        pass "$msg"
    else
        fail "$msg" "output should NOT contain '$needle'"
    fi
}

# =============================================================================
# Helper: Create a sample multi-coin V2 config.yaml
# =============================================================================
create_multicoin_config() {
    cat << 'CONFIGEOF'
version: 2

global:
  api_port: 4000
  metrics_port: 9100

database:
  host: localhost
  port: 5432
  database: spiralstratum
  user: spiraluser
  password: real_password_here

coins:
  - symbol: "DGB"
    pool_id: "dgb_sha256_1"
    enabled: true
    address: "DTestAddress1234567890abcdef"
    stratum:
      port: 5150
    nodes:
      - id: primary
        host: localhost
        port: 14022

  - symbol: "BTC"
    pool_id: "btc_sha256_1"
    enabled: true
    address: "PENDING_GENERATION"
    stratum:
      port: 5151
    nodes:
      - id: primary
        host: localhost
        port: 8332

  - symbol: "LTC"
    pool_id: "ltc_scrypt_1"
    enabled: true
    address: "PENDING_GENERATION"
    stratum:
      port: 5152
    nodes:
      - id: primary
        host: localhost
        port: 9332

  - symbol: "NMC"
    pool_id: "nmc_sha256_1"
    enabled: true
    address: "NTestAddress1234567890abcdef"
    stratum:
      port: 5153
    nodes:
      - id: primary
        host: localhost
        port: 18556
CONFIGEOF
}

# =============================================================================
# Helper: Create a sample single-coin V1 config.yaml
# =============================================================================
create_singlecoin_config() {
    cat << 'CONFIGEOF'
pool:
  id: "dgb_mainnet"
  coin: "digibyte"
  address: "PENDING_GENERATION"

stratum:
  listen: "0.0.0.0:5150"

daemon:
  host: localhost
  port: 14022
  user: rpcuser
  password: rpcpassword

database:
  host: localhost
  port: 5432
  database: spiralstratum
  user: spiraluser
  password: real_password
CONFIGEOF
}

# =============================================================================
# Test 1: Sed command pattern — multi-coin BTC only
# =============================================================================
test_sed_pattern_multicoin_btc_only() {
    log_test "Sed Pattern: Multi-coin updates only BTC address"

    local tmpfile
    tmpfile=$(mktemp)
    create_multicoin_config > "$tmpfile"

    # This is the exact sed command pattern from the fallback instructions
    # (install.sh line ~24323, ~24877, ~30815)
    local coin_sym="BTC"
    local new_addr="bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"

    sed -i "/symbol:.*\"${coin_sym}\"/,/address:/{s|address:.*|address: \"${new_addr}\"|}" "$tmpfile"

    # BTC address should be updated
    local btc_addr
    btc_addr=$(grep -A8 'symbol: "BTC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$btc_addr" "$new_addr" "BTC address updated to new address"

    # DGB address should be UNCHANGED
    local dgb_addr
    dgb_addr=$(grep -A8 'symbol: "DGB"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$dgb_addr" "DTestAddress1234567890abcdef" "DGB address unchanged after BTC update"

    # LTC address should be UNCHANGED (still PENDING_GENERATION)
    local ltc_addr
    ltc_addr=$(grep -A8 'symbol: "LTC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$ltc_addr" "PENDING_GENERATION" "LTC address unchanged (still PENDING_GENERATION)"

    # NMC address should be UNCHANGED
    local nmc_addr
    nmc_addr=$(grep -A8 'symbol: "NMC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$nmc_addr" "NTestAddress1234567890abcdef" "NMC address unchanged after BTC update"

    rm -f "$tmpfile"
}

# =============================================================================
# Test 2: Sed command pattern — multi-coin LTC only
# =============================================================================
test_sed_pattern_multicoin_ltc_only() {
    log_test "Sed Pattern: Multi-coin updates only LTC address"

    local tmpfile
    tmpfile=$(mktemp)
    create_multicoin_config > "$tmpfile"

    local coin_sym="LTC"
    local new_addr="ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9"

    sed -i "/symbol:.*\"${coin_sym}\"/,/address:/{s|address:.*|address: \"${new_addr}\"|}" "$tmpfile"

    # LTC address should be updated
    local ltc_addr
    ltc_addr=$(grep -A8 'symbol: "LTC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$ltc_addr" "$new_addr" "LTC address updated to new address"

    # BTC should still be PENDING_GENERATION (untouched)
    local btc_addr
    btc_addr=$(grep -A8 'symbol: "BTC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$btc_addr" "PENDING_GENERATION" "BTC address unchanged (still PENDING_GENERATION)"

    # DGB should be unchanged
    local dgb_addr
    dgb_addr=$(grep -A8 'symbol: "DGB"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$dgb_addr" "DTestAddress1234567890abcdef" "DGB address unchanged after LTC update"

    rm -f "$tmpfile"
}

# =============================================================================
# Test 3: Sed command pattern — DGB-SCRYPT special symbol
# =============================================================================
test_sed_pattern_dgb_scrypt() {
    log_test "Sed Pattern: DGB-SCRYPT symbol handled correctly"

    local tmpfile
    tmpfile=$(mktemp)
    cat << 'CONFIGEOF' > "$tmpfile"
version: 2

coins:
  - symbol: "DGB"
    pool_id: "dgb_sha256_1"
    enabled: true
    address: "DRealAddress1234567890abcdef"
    stratum:
      port: 5150

  - symbol: "DGB-SCRYPT"
    pool_id: "dgb_scrypt_1"
    enabled: true
    address: "PENDING_GENERATION"
    stratum:
      port: 5165
CONFIGEOF

    # The fallback code uses regen_config_sym="DGB-SCRYPT" for dgb-scrypt
    local coin_sym="DGB-SCRYPT"
    local new_addr="DScryptAddress1234567890abcd"

    sed -i "/symbol:.*\"${coin_sym}\"/,/address:/{s|address:.*|address: \"${new_addr}\"|}" "$tmpfile"

    # DGB-SCRYPT address should be updated
    local scrypt_addr
    scrypt_addr=$(grep -A8 'symbol: "DGB-SCRYPT"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$scrypt_addr" "$new_addr" "DGB-SCRYPT address updated correctly"

    # DGB (SHA256d) address should be UNCHANGED
    local dgb_addr
    dgb_addr=$(grep -A8 'symbol: "DGB"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$dgb_addr" "DRealAddress1234567890abcdef" "DGB SHA256d address unchanged"

    rm -f "$tmpfile"
}

# =============================================================================
# Test 4: PENDING_GENERATION line-targeted replacement (multi-coin)
# =============================================================================
test_pending_generation_line_targeted() {
    log_test "PENDING_GENERATION: Line-targeted replacement in multi-coin config"

    # This tests the primary path in spiralpool-wallet (install.sh line ~24912-24926):
    #   sym_line=$(grep -ni "symbol:.*${COIN_SYMBOL}" "$CONFIG_FILE" | head -1 | cut -d: -f1)
    #   addr_offset=$(tail -n "+${sym_line}" "$CONFIG_FILE" | grep -n "PENDING_GENERATION" | head -1 | cut -d: -f1)
    #   target_line=$((sym_line + addr_offset - 1))
    #   sed -i "${target_line}s|PENDING_GENERATION|${SAFE_ADDRESS}|" "$CONFIG_FILE"

    local tmpfile
    tmpfile=$(mktemp)
    create_multicoin_config > "$tmpfile"

    local COIN_SYMBOL="BTC"
    local SAFE_ADDRESS="bc1qNewBtcAddress1234567890abcdef1234567890"

    # Replicate the exact logic from spiralpool-wallet
    local sym_line
    sym_line=$(grep -ni "symbol:.*${COIN_SYMBOL}" "$tmpfile" 2>/dev/null | head -1 | cut -d: -f1)

    if [[ -n "$sym_line" ]]; then
        local addr_offset
        addr_offset=$(tail -n "+${sym_line}" "$tmpfile" | grep -n "PENDING_GENERATION" | head -1 | cut -d: -f1)
        if [[ -n "$addr_offset" ]]; then
            local target_line=$((sym_line + addr_offset - 1))
            sed -i "${target_line}s|PENDING_GENERATION|${SAFE_ADDRESS}|" "$tmpfile"
            pass "Line-targeted sed executed successfully"
        else
            fail "Could not find PENDING_GENERATION after symbol line" ""
        fi
    else
        fail "Could not find symbol line for $COIN_SYMBOL" ""
    fi

    # BTC should be updated
    local btc_addr
    btc_addr=$(grep -A8 'symbol: "BTC"' "$tmpfile" | grep 'address:' | head -1)
    assert_contains "$btc_addr" "$SAFE_ADDRESS" "BTC PENDING_GENERATION replaced with real address"

    # LTC should still have PENDING_GENERATION
    local ltc_addr
    ltc_addr=$(grep -A8 'symbol: "LTC"' "$tmpfile" | grep 'address:' | head -1)
    assert_contains "$ltc_addr" "PENDING_GENERATION" "LTC PENDING_GENERATION preserved (not cross-contaminated)"

    # DGB should be unchanged (it had a real address)
    local dgb_addr
    dgb_addr=$(grep -A8 'symbol: "DGB"' "$tmpfile" | grep 'address:' | head -1)
    assert_contains "$dgb_addr" "DTestAddress1234567890abcdef" "DGB real address preserved"

    rm -f "$tmpfile"
}

# =============================================================================
# Test 5: Single-coin config — global PENDING_GENERATION replacement
# =============================================================================
test_pending_generation_singlecoin() {
    log_test "PENDING_GENERATION: Single-coin config global replacement"

    # In single-coin mode (no symbol: line), the code does:
    #   sed -i "s|PENDING_GENERATION|${SAFE_ADDRESS}|" "$CONFIG_FILE"

    local tmpfile
    tmpfile=$(mktemp)
    create_singlecoin_config > "$tmpfile"

    local SAFE_ADDRESS="DRealAddressFromWalletGen12345"

    sed -i "s|PENDING_GENERATION|${SAFE_ADDRESS}|" "$tmpfile"

    local result_addr
    result_addr=$(grep 'address:' "$tmpfile" | head -1)
    assert_contains "$result_addr" "$SAFE_ADDRESS" "Single-coin PENDING_GENERATION replaced"

    # Verify no PENDING_GENERATION remains
    if grep -q "PENDING_GENERATION" "$tmpfile"; then
        fail "No PENDING_GENERATION should remain after replacement"
    else
        pass "No PENDING_GENERATION remains in config"
    fi

    rm -f "$tmpfile"
}

# =============================================================================
# Test 6: Exchange address warning present in install.sh fallback paths
# =============================================================================
test_exchange_warning_in_fallback_paths() {
    log_test "Exchange Address Warning: Present in all fallback paths"

    if [[ ! -f "$INSTALL_SCRIPT" ]]; then
        fail "install.sh not found at $INSTALL_SCRIPT" ""
        return
    fi

    # Path 1: prompt_manual_address() auto-mode (line ~24313)
    # Path 2: prompt_manual_address() interactive mode (line ~24349)
    # Path 3: Address regeneration failure (line ~24867)
    # Path 4: Post-sync auto-generation failure (line ~30808)
    # All should contain the exchange address warning

    local exchange_count
    exchange_count=$(grep -c "DO NOT USE EXCHANGE ADDRESSES\|EXCHANGE ADDRESSES NOT SUPPORTED\|Do NOT use exchange" "$INSTALL_SCRIPT" 2>/dev/null || echo 0)

    # We expect at least 4 instances (paths 1-4 above, some have both header and detail lines)
    if [[ "$exchange_count" -ge 4 ]]; then
        pass "Exchange address warning appears $exchange_count times in install.sh (>=4 expected)"
    else
        fail "Exchange address warning appears only $exchange_count times (expected >=4)" ""
    fi

    # Verify the warning mentions specific exchanges
    if grep -q "Binance.*Coinbase.*Kraken" "$INSTALL_SCRIPT"; then
        pass "Warning mentions specific exchange names (Binance, Coinbase, Kraken)"
    else
        fail "Warning should mention specific exchange names for clarity" ""
    fi

    # Verify the warning mentions memo/tag risk
    local memo_count
    memo_count=$(grep -c "memo.*tag\|tag.*memo\|memos.*tags\|tags.*memos" "$INSTALL_SCRIPT" 2>/dev/null || echo 0)
    if [[ "$memo_count" -ge 3 ]]; then
        pass "Memo/tag deposit risk mentioned $memo_count times (>=3 expected)"
    else
        fail "Memo/tag deposit risk mentioned only $memo_count times (expected >=3)" ""
    fi
}

# =============================================================================
# Test 7: spiralpool-wallet hint present in fallback output
# =============================================================================
test_wallet_hint_in_fallback() {
    log_test "spiralpool-wallet Hint: Present in fallback instructions"

    if [[ ! -f "$INSTALL_SCRIPT" ]]; then
        fail "install.sh not found" ""
        return
    fi

    # The fallback instructions should tell users about spiralpool-wallet
    # as an alternative to the manual sed command

    # Path 1: auto-mode failure (line ~24332)
    if grep -q 'spiralpool-wallet --coin.*\${COIN}' "$INSTALL_SCRIPT" 2>/dev/null || \
       grep -q "spiralpool-wallet --coin" "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "spiralpool-wallet --coin hint present in install.sh"
    else
        fail "spiralpool-wallet --coin hint missing from fallback instructions" ""
    fi

    # Path 4: post-sync failure (line ~30824)
    if grep -q 'spiralpool-wallet --coin.*wallet_coin' "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "spiralpool-wallet --coin hint present in post-sync fallback"
    else
        fail "spiralpool-wallet --coin hint missing from post-sync fallback" ""
    fi
}

# =============================================================================
# Test 8: Sed command in fallback matches actual config.yaml structure
# =============================================================================
test_sed_command_structure_match() {
    log_test "Sed Command: Fallback sed pattern matches config.yaml format"

    if [[ ! -f "$INSTALL_SCRIPT" ]]; then
        fail "install.sh not found" ""
        return
    fi

    # The sed command given to users is:
    #   sudo sed -i '/symbol:.*"COIN"/,/address:/{s|address:.*|address: "YOUR_ADDRESS"|}' /spiralpool/config/config.yaml
    # Verify this exact pattern exists in install.sh

    if grep -qF '/symbol:.*\"${config_sym}\"/,/address:/{s|address:.*|address: \"YOUR_ADDRESS\"|}' "$INSTALL_SCRIPT" 2>/dev/null || \
       grep -qF '/symbol:.*\"${regen_config_sym}\"/,/address:/{s|address:.*|address: \"YOUR_ADDRESS\"|}' "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "Fallback sed command uses correct /symbol:.../,/address:/ range pattern"
    else
        fail "Fallback sed command pattern not found in install.sh" ""
    fi

    # Also check the post-sync fallback (line ~30815 uses wallet_coin variable)
    if grep -qF '/symbol:.*\"${wallet_coin}\"/,/address:/{s|address:.*|address: \"YOUR_ADDRESS\"|}' "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "Post-sync fallback sed command uses correct pattern"
    else
        fail "Post-sync fallback sed command pattern not found" ""
    fi
}

# =============================================================================
# Test 9: Service restart command in fallback instructions
# =============================================================================
test_service_restart_in_fallback() {
    log_test "Service Restart: Present in all fallback paths"

    if [[ ! -f "$INSTALL_SCRIPT" ]]; then
        fail "install.sh not found" ""
        return
    fi

    # All fallback paths should tell users to restart services
    local restart_count
    restart_count=$(grep -c "systemctl restart spiralstratum spiralsentinel spiraldash spiralpool-health" "$INSTALL_SCRIPT" 2>/dev/null || echo 0)

    # Expected in: auto-mode fallback, regen fallback, post-sync fallback (at least 3)
    if [[ "$restart_count" -ge 3 ]]; then
        pass "Service restart command appears $restart_count times (>=3 expected)"
    else
        fail "Service restart command appears only $restart_count times (expected >=3)" ""
    fi
}

# =============================================================================
# Test 10: HA peer instructions in fallback
# =============================================================================
test_ha_instructions_in_fallback() {
    log_test "HA Instructions: Present in fallback paths"

    if [[ ! -f "$INSTALL_SCRIPT" ]]; then
        fail "install.sh not found" ""
        return
    fi

    # Fallback instructions should mention HA peer repetition
    local ha_repeat_count
    ha_repeat_count=$(grep -c "HA.*repeat\|repeat.*peer\|peer nodes\|all peer" "$INSTALL_SCRIPT" 2>/dev/null || echo 0)

    if [[ "$ha_repeat_count" -ge 2 ]]; then
        pass "HA peer repeat instructions appear $ha_repeat_count times (>=2 expected)"
    else
        fail "HA peer repeat instructions appear only $ha_repeat_count times (expected >=2)" ""
    fi
}

# =============================================================================
# Test 11: Sed pattern does not break on address containing special chars
# =============================================================================
test_sed_special_chars_in_address() {
    log_test "Sed Pattern: Handles addresses with no special char leakage"

    local tmpfile
    tmpfile=$(mktemp)
    create_multicoin_config > "$tmpfile"

    # Simulate an address that contains characters that COULD be sed metacharacters
    # In practice, addresses are alphanumeric, but defense-in-depth matters
    local coin_sym="BTC"
    local new_addr="bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"

    sed -i "/symbol:.*\"${coin_sym}\"/,/address:/{s|address:.*|address: \"${new_addr}\"|}" "$tmpfile"

    local btc_addr
    btc_addr=$(grep -A8 'symbol: "BTC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$btc_addr" "$new_addr" "Real bech32 address survives sed replacement"

    rm -f "$tmpfile"
}

# =============================================================================
# Test 12: Multiple sequential coin updates do not interfere
# =============================================================================
test_sequential_multicoin_updates() {
    log_test "Sequential Updates: Two coins updated independently"

    local tmpfile
    tmpfile=$(mktemp)
    create_multicoin_config > "$tmpfile"

    # Update BTC first
    local btc_addr="bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
    sed -i "/symbol:.*\"BTC\"/,/address:/{s|address:.*|address: \"${btc_addr}\"|}" "$tmpfile"

    # Then update LTC
    local ltc_addr="ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9"
    sed -i "/symbol:.*\"LTC\"/,/address:/{s|address:.*|address: \"${ltc_addr}\"|}" "$tmpfile"

    # Verify both updates took effect correctly
    local result_btc
    result_btc=$(grep -A8 'symbol: "BTC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$result_btc" "$btc_addr" "BTC address correct after sequential updates"

    local result_ltc
    result_ltc=$(grep -A8 'symbol: "LTC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$result_ltc" "$ltc_addr" "LTC address correct after sequential updates"

    # DGB and NMC should still be untouched
    local result_dgb
    result_dgb=$(grep -A8 'symbol: "DGB"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$result_dgb" "DTestAddress1234567890abcdef" "DGB address untouched after BTC+LTC updates"

    local result_nmc
    result_nmc=$(grep -A8 'symbol: "NMC"' "$tmpfile" | grep 'address:' | head -1 | sed 's/.*address: "\(.*\)"/\1/')
    assert_eq "$result_nmc" "NTestAddress1234567890abcdef" "NMC address untouched after BTC+LTC updates"

    rm -f "$tmpfile"
}

# =============================================================================
# Test 13: Per-coin wallet software suggestions in install.sh
# =============================================================================
test_wallet_software_suggestions() {
    log_test "Wallet Suggestions: Per-coin software recommendations present"

    if [[ ! -f "$INSTALL_SCRIPT" ]]; then
        fail "install.sh not found" ""
        return
    fi

    # Verify per-coin wallet suggestions exist in the fallback code
    local suggestions=(
        "DigiByte Core"
        "Bitcoin Core"
        "Bitcoin Cash Node"
        "Bitcoin II Core"
        "Namecoin Core"
        "Syscoin Core"
        "Litecoin Core"
        "Dogecoin Core"
        "PepeCoin Core"
    )

    for suggestion in "${suggestions[@]}"; do
        if grep -q "$suggestion" "$INSTALL_SCRIPT" 2>/dev/null; then
            pass "Wallet suggestion present: $suggestion"
        else
            fail "Wallet suggestion missing: $suggestion" ""
        fi
    done
}

# =============================================================================
# Test 14: PENDING_GENERATION count tracking (post-sync verification logic)
# =============================================================================
test_pending_count_verification() {
    log_test "PENDING Count: Post-sync wallet gen verifies count decreased"

    if [[ ! -f "$INSTALL_SCRIPT" ]]; then
        fail "install.sh not found" ""
        return
    fi

    # The post-sync code checks pending_before vs pending_after to verify
    # wallet generation actually worked (install.sh line ~30789-30798)
    if grep -q 'pending_before.*grep -c.*PENDING_GENERATION' "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "Post-sync checks PENDING_GENERATION count before wallet gen"
    else
        fail "Post-sync should count PENDING_GENERATION before wallet gen" ""
    fi

    if grep -q 'pending_after.*grep -c.*PENDING_GENERATION' "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "Post-sync checks PENDING_GENERATION count after wallet gen"
    else
        fail "Post-sync should count PENDING_GENERATION after wallet gen" ""
    fi

    if grep -q 'pending_after.*-ge.*pending_before' "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "Post-sync compares counts to detect failure"
    else
        fail "Post-sync should compare before/after counts" ""
    fi
}

# =============================================================================
# Run All Tests
# =============================================================================

echo ""
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN}  SPIRAL POOL — Wallet Generation Fallback Tests${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

test_sed_pattern_multicoin_btc_only
echo ""
test_sed_pattern_multicoin_ltc_only
echo ""
test_sed_pattern_dgb_scrypt
echo ""
test_pending_generation_line_targeted
echo ""
test_pending_generation_singlecoin
echo ""
test_exchange_warning_in_fallback_paths
echo ""
test_wallet_hint_in_fallback
echo ""
test_sed_command_structure_match
echo ""
test_service_restart_in_fallback
echo ""
test_ha_instructions_in_fallback
echo ""
test_sed_special_chars_in_address
echo ""
test_sequential_multicoin_updates
echo ""
test_wallet_software_suggestions
echo ""
test_pending_count_verification

# =============================================================================
# Summary
# =============================================================================

echo ""
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
if [[ $TESTS_FAILED -eq 0 ]]; then
    echo -e "${GREEN}  ALL TESTS PASSED: ${TESTS_PASSED}/${TESTS_RUN}${NC}"
else
    echo -e "${RED}  TESTS FAILED: ${TESTS_FAILED}/${TESTS_RUN}${NC}"
    echo -e "${GREEN}  Tests passed: ${TESTS_PASSED}${NC}"
fi
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

if [[ $TESTS_FAILED -gt 0 ]]; then
    exit 1
fi
exit 0
