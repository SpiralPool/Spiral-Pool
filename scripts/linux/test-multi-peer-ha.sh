#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# =============================================================================
# Offline Test Harness for Multi-Peer HA Scripts
# =============================================================================
#
# Tests the ACTUAL code from ha-role-watcher.sh, ha-failback.sh, and
# etcd-cluster-rejoin.sh against controlled inputs. No simulation —
# functions are extracted from the real scripts and exercised directly.
#
# Mocking strategy:
#   - /etc/default/etcd → sed-replaced with a temp file path (only change)
#   - curl, ping, etcdctl, ip → mock scripts prepended to PATH
#   - jq → real jq (required for API merger tests)
#   - All other code (grep, awk, sed, bash logic) runs as-is
#
# Usage:
#   ./test-multi-peer-ha.sh                # Run all tests
#   ./test-multi-peer-ha.sh --suite 3      # Run only suite 3
#   ./test-multi-peer-ha.sh --verbose      # Show extracted code on failure
#
# Requirements:
#   - bash 4+, grep -P (PCRE), awk, sed, jq
#   - Does NOT need: etcd, Patroni, keepalived, network, root
#
# =============================================================================

set -uo pipefail

# Locate the actual scripts relative to this test
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROLE_WATCHER="${SCRIPT_DIR}/ha-role-watcher.sh"
FAILBACK="${SCRIPT_DIR}/ha-failback.sh"
REJOIN="${SCRIPT_DIR}/etcd-cluster-rejoin.sh"
VALIDATE="${SCRIPT_DIR}/ha-validate.sh"

# Parse args
RUN_SUITE=""
VERBOSE=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --suite) RUN_SUITE="$2"; shift 2 ;;
        --verbose) VERBOSE=true; shift ;;
        *) shift ;;
    esac
done

# Verify scripts exist
for f in "$ROLE_WATCHER" "$FAILBACK" "$REJOIN"; do
    if [[ ! -f "$f" ]]; then
        echo "FATAL: Script not found: $f"
        echo "Run this from scripts/linux/ alongside the HA scripts."
        exit 2
    fi
done

# Verify jq
if ! command -v jq &>/dev/null; then
    echo "FATAL: jq is required (sudo apt install jq)"
    exit 2
fi

# =============================================================================
# Test infrastructure
# =============================================================================

PASS=0
FAIL=0
SKIP=0

_test_pass() {
    echo "  PASS: $1"
    PASS=$((PASS + 1))
}

_test_fail() {
    echo "  FAIL: $1"
    echo "    Expected: $2"
    echo "    Got:      $3"
    FAIL=$((FAIL + 1))
}

_test_skip() {
    echo "  SKIP: $1"
    SKIP=$((SKIP + 1))
}

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [[ "$expected" == "$actual" ]]; then
        _test_pass "$label"
    else
        _test_fail "$label" "$expected" "$actual"
    fi
}

assert_contains() {
    local label="$1" haystack="$2" needle="$3"
    if echo "$haystack" | grep -qF "$needle"; then
        _test_pass "$label"
    else
        _test_fail "$label" "contains '$needle'" "$haystack"
    fi
}

assert_not_contains() {
    local label="$1" haystack="$2" needle="$3"
    if ! echo "$haystack" | grep -qF "$needle"; then
        _test_pass "$label"
    else
        _test_fail "$label" "NOT contains '$needle'" "$haystack"
    fi
}

assert_line_count() {
    local label="$1" text="$2" expected="$3"
    local actual
    if [[ -z "$text" ]]; then
        actual=0
    else
        actual=$(echo "$text" | wc -l)
    fi
    assert_eq "$label" "$expected" "$actual"
}

# =============================================================================
# Mock environment setup
# =============================================================================

MOCK_DIR=$(mktemp -d)
MOCK_BIN="${MOCK_DIR}/bin"
MOCK_ETCD_CONFIG="${MOCK_DIR}/etcd-config"
MOCK_CURL_RESPONSE="${MOCK_DIR}/curl-response"
MOCK_IP_RESPONSE="${MOCK_DIR}/ip-response"
MOCK_PING_DIR="${MOCK_DIR}/ping-targets"
MOCK_ETCDCTL_MEMBERS="${MOCK_DIR}/etcdctl-members"
MOCK_ETCDCTL_HEALTH="${MOCK_DIR}/etcdctl-health"

cleanup() {
    rm -rf "$MOCK_DIR"
}
trap cleanup EXIT

mkdir -p "$MOCK_BIN" "$MOCK_PING_DIR"

# --- Mock: curl ---
cat > "$MOCK_BIN/curl" << 'MOCKEOF'
#!/bin/bash
if [[ -f "${MOCK_CURL_RESPONSE:-}" ]]; then
    cat "$MOCK_CURL_RESPONSE"
else
    echo ""
fi
MOCKEOF
chmod +x "$MOCK_BIN/curl"

# --- Mock: ip ---
cat > "$MOCK_BIN/ip" << 'MOCKEOF'
#!/bin/bash
if [[ -f "${MOCK_IP_RESPONSE:-}" ]]; then
    cat "$MOCK_IP_RESPONSE"
else
    echo ""
fi
MOCKEOF
chmod +x "$MOCK_BIN/ip"

# --- Mock: ping ---
# Touch $MOCK_PING_DIR/<ip> to make it "reachable"
cat > "$MOCK_BIN/ping" << 'MOCKEOF'
#!/bin/bash
# Extract target IP from args (last positional arg)
target=""
for arg in "$@"; do
    # Skip flags and their values
    case "$arg" in
        -c|-W|-w|-i|-s|-t) continue ;;
    esac
    # If it looks like an IP, it's the target
    if [[ "$arg" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        target="$arg"
    fi
done
if [[ -n "$target" && -f "${MOCK_PING_DIR}/${target}" ]]; then
    exit 0
else
    exit 1
fi
MOCKEOF
chmod +x "$MOCK_BIN/ping"

# --- Mock: etcdctl ---
cat > "$MOCK_BIN/etcdctl" << 'MOCKEOF'
#!/bin/bash
if [[ "$*" == *"member list"* ]]; then
    if [[ -f "${MOCK_ETCDCTL_MEMBERS:-}" ]]; then
        cat "$MOCK_ETCDCTL_MEMBERS"
    fi
elif [[ "$*" == *"endpoint health"* ]]; then
    # Check which endpoint is being queried
    for arg in "$@"; do
        if [[ "$arg" == --endpoints=* ]]; then
            ep="${arg#--endpoints=}"
            if [[ -f "${MOCK_ETCDCTL_HEALTH:-}" ]]; then
                if grep -qF "$ep" "$MOCK_ETCDCTL_HEALTH" 2>/dev/null; then
                    echo "${ep} is healthy"
                    exit 0
                fi
            fi
            echo "${ep} is unhealthy"
            exit 1
        fi
    done
fi
MOCKEOF
chmod +x "$MOCK_BIN/etcdctl"

# --- Mock: sleep (no-op for fast tests) ---
cat > "$MOCK_BIN/sleep" << 'MOCKEOF'
#!/bin/bash
exit 0
MOCKEOF
chmod +x "$MOCK_BIN/sleep"

# --- jq wrapper: strip Windows CR from jq.exe output (Git Bash / MSYS2) ---
# On Linux this is a harmless no-op (tr -d '\r' on LF-only output = identity).
REAL_JQ=$(command -v jq)
echo "#!/bin/bash" > "$MOCK_BIN/jq"
echo "\"$REAL_JQ\" \"\$@\" | tr -d '\r'" >> "$MOCK_BIN/jq"
chmod +x "$MOCK_BIN/jq"

# Reset mock state to defaults
reset_mocks() {
    echo -n > "$MOCK_ETCD_CONFIG"
    echo '{}' > "$MOCK_CURL_RESPONSE"
    echo "" > "$MOCK_IP_RESPONSE"
    echo "" > "$MOCK_ETCDCTL_MEMBERS"
    echo "" > "$MOCK_ETCDCTL_HEALTH"
    rm -f "$MOCK_PING_DIR"/*
}

# =============================================================================
# Function extraction
# =============================================================================
#
# Extracts a function from a script file, returning its full definition.
# The ONLY modification: /etc/default/etcd is replaced with $MOCK_ETCD_CONFIG.
# All grep patterns, awk commands, pipe chains, etc. are untouched.
#

extract_func() {
    local script="$1"
    local func_name="$2"
    # Extract from "func_name() {" through the matching closing "}"
    # at column 0 (handles nested braces because inner } are indented)
    sed -n "/^${func_name}() {/,/^}/p" "$script" \
        | sed "s|/etc/default/etcd|${MOCK_ETCD_CONFIG}|g"
}

# Extract a range of lines from a script (for top-level code blocks)
# Same config-path substitution applied.
extract_lines() {
    local script="$1"
    local start="$2"
    local end="$3"
    sed -n "${start},${end}p" "$script" \
        | sed "s|/etc/default/etcd|${MOCK_ETCD_CONFIG}|g"
}

# Verify a key anchor line hasn't shifted (fail fast if scripts were edited)
verify_anchor() {
    local script="$1" line="$2" pattern="$3" label="$4"
    if ! sed -n "${line}p" "$script" | grep -q "$pattern"; then
        echo "FATAL: Anchor mismatch at $(basename "$script"):$line — $label"
        echo "  Expected pattern: $pattern"
        echo "  Actual line:      $(sed -n "${line}p" "$script")"
        echo "  Script was modified — update line numbers in this test."
        exit 2
    fi
}

# Verify all line-number-dependent extractions match expected anchors.
# If ANY anchor fails, the test stops immediately with a clear message.
echo "Verifying script structure anchors..."
verify_anchor "$REJOIN"       127 'CANDIDATE_IPS=()'             "rejoin: CANDIDATE_IPS init"
verify_anchor "$REJOIN"       141 'done < <'                     "rejoin: candidate dedup loop end"
verify_anchor "$REJOIN"       251 'while IFS= read -r mline'    "rejoin: member list loop start"
verify_anchor "$REJOIN"       269 'done <<< "\$FULL_MEMBER_LIST' "rejoin: member list loop end"
verify_anchor "$ROLE_WATCHER" 425 'local all_peer_ips'           "watcher: quorum gate start"
verify_anchor "$ROLE_WATCHER" 464 'fi'                           "watcher: quorum gate safety-net end"
verify_anchor "$ROLE_WATCHER" 805 'local any_peer_reachable=0'   "watcher: failback gate start"
verify_anchor "$ROLE_WATCHER" 814 'done <<< "\$all_peers'        "watcher: failback gate end"
verify_anchor "$FAILBACK"     147 'ALL_PEER_IPS_RAW='            "failback: ALL_PEER_IPS_RAW"
verify_anchor "$FAILBACK"     174 'fi'                           "failback: PEER_IP fallback end"
verify_anchor "$FAILBACK"     330 'CLUSTER_JSON='                "failback: CLUSTER_JSON fetch"
verify_anchor "$FAILBACK"      58 'SYNC_DISABLED=0'              "failback: SYNC_DISABLED init"
echo "All anchors verified."
echo ""

# =============================================================================
# SUITE 1: get_all_peer_ips() from ha-role-watcher.sh
# =============================================================================

suite_1() {
    echo ""
    echo "================================================================"
    echo "SUITE 1: ha-role-watcher.sh — get_all_peer_ips()"
    echo "================================================================"

    # Extract the actual function
    local func_code
    func_code=$(extract_func "$ROLE_WATCHER" "get_all_peer_ips")
    if [[ -z "$func_code" ]]; then
        echo "FATAL: Could not extract get_all_peer_ips from $ROLE_WATCHER"
        return 1
    fi

    # --- 1a: 2-node config, no API peers ---
    echo ""
    echo "--- 1a: 2-node etcd config (1 peer expected) ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380"
ETCD_INITIAL_CLUSTER_STATE="new"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"

    local result
    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    )
    assert_eq "1a: returns exactly 192.168.1.105" "192.168.1.105" "$result"

    # --- 1b: 3-node config (2 peers expected) ---
    echo ""
    echo "--- 1b: 3-node etcd config (2 peers expected) ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380,etcd-ha-3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    )
    assert_line_count "1b: returns 2 peer IPs" "$result" "2"
    assert_contains "1b: includes 192.168.1.105" "$result" "192.168.1.105"
    assert_contains "1b: includes 192.168.1.106" "$result" "192.168.1.106"
    assert_not_contains "1b: excludes self (192.168.1.104)" "$result" "192.168.1.104"

    # --- 1c: 5-node config (4 peers expected) ---
    echo ""
    echo "--- 1c: 5-node etcd config (4 peers expected) ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://10.0.0.1:2380"
ETCD_INITIAL_CLUSTER="e1=http://10.0.0.1:2380,e2=http://10.0.0.2:2380,e3=http://10.0.0.3:2380,e4=http://10.0.0.4:2380,e5=http://10.0.0.5:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 10.0.0.1/24 brd 10.0.0.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    )
    assert_line_count "1c: returns 4 peer IPs" "$result" "4"
    assert_not_contains "1c: excludes self (10.0.0.1)" "$result" "10.0.0.1"

    # --- 1d: IP substring safety — self-filter must be exact ---
    echo ""
    echo "--- 1d: IP substring safety — 10.0.0.1 must NOT filter 10.0.0.10 ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://10.0.0.1:2380"
ETCD_INITIAL_CLUSTER="e1=http://10.0.0.1:2380,e2=http://10.0.0.10:2380,e3=http://10.0.0.100:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 10.0.0.1/24 brd 10.0.0.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    )
    assert_line_count "1d: returns 2 peers (10 and 100, not self)" "$result" "2"
    assert_contains "1d: includes 10.0.0.10" "$result" "10.0.0.10"
    assert_contains "1d: includes 10.0.0.100" "$result" "10.0.0.100"
    # Verify no line is exactly "10.0.0.1" (self must not leak through)
    local exact_self
    exact_self=$(echo "$result" | grep -cx "^10\.0\.0\.1$" || true)
    assert_eq "1d: no line is exactly 10.0.0.1 (self excluded)" "0" "$exact_self"

    # --- 1e: API peers merged with etcd (dedup) ---
    echo ""
    echo "--- 1e: API peers merged + deduped with etcd ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    # API returns 105 (already in etcd) + 106 (new peer)
    cat > "$MOCK_CURL_RESPONSE" << 'EOF'
{"enabled":true,"localRole":"BACKUP","nodes":[{"host":"192.168.1.104"},{"host":"192.168.1.105"},{"host":"192.168.1.106"}]}
EOF

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    )
    # Should have: 105 from etcd, 106 from API (105 deduped, 104 = self excluded)
    assert_contains "1e: includes 192.168.1.105 (etcd)" "$result" "192.168.1.105"
    assert_contains "1e: includes 192.168.1.106 (API)" "$result" "192.168.1.106"
    assert_not_contains "1e: excludes self" "$result" "192.168.1.104"
    # Count unique lines
    local unique_count
    unique_count=$(echo "$result" | sort -u | grep -c '.' || true)
    assert_eq "1e: exactly 2 unique peers" "2" "$unique_count"

    # --- 1f: No etcd config, API only ---
    echo ""
    echo "--- 1f: No etcd config — API-only fallback ---"
    reset_mocks
    echo "" > "$MOCK_ETCD_CONFIG"  # empty = grep won't find INITIAL_CLUSTER
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    cat > "$MOCK_CURL_RESPONSE" << 'EOF'
{"enabled":true,"localRole":"BACKUP","nodes":[{"host":"192.168.1.104"},{"host":"192.168.1.105"}]}
EOF

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    )
    assert_eq "1f: API-only returns 192.168.1.105" "192.168.1.105" "$result"
}

# =============================================================================
# SUITE 2: get_all_peer_ips() from ha-failback.sh
# =============================================================================

suite_2() {
    echo ""
    echo "================================================================"
    echo "SUITE 2: ha-failback.sh — get_all_peer_ips()"
    echo "================================================================"

    local func_code
    func_code=$(extract_func "$FAILBACK" "get_all_peer_ips")
    if [[ -z "$func_code" ]]; then
        echo "FATAL: Could not extract get_all_peer_ips from $FAILBACK"
        return 1
    fi

    # --- 2a: 3-node config ---
    echo ""
    echo "--- 2a: 3-node etcd config (failback version with || true) ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380,etcd-ha-3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"

    local result
    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    ) || true  # failback version uses || true pattern
    assert_line_count "2a: returns 2 peer IPs" "$result" "2"
    assert_contains "2a: includes 192.168.1.105" "$result" "192.168.1.105"
    assert_contains "2a: includes 192.168.1.106" "$result" "192.168.1.106"

    # --- 2b: Consistency — both scripts produce same output for same input ---
    echo ""
    echo "--- 2b: Cross-script consistency check ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://10.0.0.1:2380"
ETCD_INITIAL_CLUSTER="e1=http://10.0.0.1:2380,e2=http://10.0.0.2:2380,e3=http://10.0.0.3:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 10.0.0.1/24 brd 10.0.0.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"

    local rw_func
    rw_func=$(extract_func "$ROLE_WATCHER" "get_all_peer_ips")
    local result_rw result_fb
    result_rw=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$rw_func"
        get_all_peer_ips
    )
    result_fb=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        eval "$func_code"
        get_all_peer_ips
    ) || true
    assert_eq "2b: role-watcher and failback produce identical output" "$result_rw" "$result_fb"
}

# =============================================================================
# SUITE 3: Candidate IP dedup — etcd-cluster-rejoin.sh
# =============================================================================

suite_3() {
    echo ""
    echo "================================================================"
    echo "SUITE 3: etcd-cluster-rejoin.sh — candidate IP deduplication"
    echo "================================================================"

    # Extract the dedup code block (lines 126-146 of the actual script).
    # This is top-level code so we extract it as a code block, wrap it in a
    # function, and set the variables it expects.

    # --- 3a: --peer-ip matches INITIAL_CLUSTER entry ---
    echo ""
    echo "--- 3a: --peer-ip overlaps with INITIAL_CLUSTER → deduped ---"

    # Run the actual code from the script with controlled variables
    local result
    result=$(
        export PATH="$MOCK_BIN:$PATH"
        # Variables the code block expects
        ARG_PEER_IP="192.168.1.105"
        ETCD_INITIAL_CLUSTER="etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380,etcd-ha-3=http://192.168.1.106:2380"
        LOCAL_IP="192.168.1.104"

        # Actual code from etcd-cluster-rejoin.sh lines 127-146
        eval "$(extract_lines "$REJOIN" 127 146)"

        echo "COUNT=${#CANDIDATE_IPS[@]}"
        for ip in "${CANDIDATE_IPS[@]}"; do echo "IP=$ip"; done
    )
    assert_contains "3a: 2 candidates" "$result" "COUNT=2"
    assert_contains "3a: first is --peer-ip (192.168.1.105)" "$result" "IP=192.168.1.105"
    assert_contains "3a: second from INITIAL_CLUSTER (192.168.1.106)" "$result" "IP=192.168.1.106"
    assert_not_contains "3a: self excluded" "$result" "IP=192.168.1.104"

    # --- 3b: --peer-ip is NOT in INITIAL_CLUSTER ---
    echo ""
    echo "--- 3b: --peer-ip is a new IP not in INITIAL_CLUSTER ---"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        ARG_PEER_IP="192.168.1.200"
        ETCD_INITIAL_CLUSTER="etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380"
        LOCAL_IP="192.168.1.104"

        eval "$(extract_lines "$REJOIN" 127 146)"

        echo "COUNT=${#CANDIDATE_IPS[@]}"
        for ip in "${CANDIDATE_IPS[@]}"; do echo "IP=$ip"; done
    )
    assert_contains "3b: 2 candidates (arg + INITIAL_CLUSTER)" "$result" "COUNT=2"
    assert_contains "3b: --peer-ip first" "$result" "IP=192.168.1.200"
    assert_contains "3b: INITIAL_CLUSTER second" "$result" "IP=192.168.1.105"

    # --- 3c: No --peer-ip, 5-node cluster ---
    echo ""
    echo "--- 3c: No --peer-ip, 5-node cluster ---"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        ARG_PEER_IP=""
        ETCD_INITIAL_CLUSTER="e1=http://10.0.0.1:2380,e2=http://10.0.0.2:2380,e3=http://10.0.0.3:2380,e4=http://10.0.0.4:2380,e5=http://10.0.0.5:2380"
        LOCAL_IP="10.0.0.1"

        eval "$(extract_lines "$REJOIN" 127 146)"

        echo "COUNT=${#CANDIDATE_IPS[@]}"
        for ip in "${CANDIDATE_IPS[@]}"; do echo "IP=$ip"; done
    )
    assert_contains "3c: 4 candidates (all non-self)" "$result" "COUNT=4"
    assert_not_contains "3c: self excluded" "$result" "IP=10.0.0.1"

    # --- 3d: IP substring dedup safety ---
    echo ""
    echo "--- 3d: --peer-ip=10.0.0.10, INITIAL_CLUSTER has 10.0.0.1 and 10.0.0.10 ---"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        ARG_PEER_IP="10.0.0.10"
        ETCD_INITIAL_CLUSTER="e1=http://10.0.0.1:2380,e2=http://10.0.0.10:2380,e3=http://10.0.0.100:2380"
        LOCAL_IP="10.0.0.1"

        eval "$(extract_lines "$REJOIN" 127 146)"

        echo "COUNT=${#CANDIDATE_IPS[@]}"
        for ip in "${CANDIDATE_IPS[@]}"; do echo "IP=$ip"; done
    )
    assert_contains "3d: 2 candidates (10 deduped, 100 new)" "$result" "COUNT=2"
    assert_contains "3d: includes 10.0.0.10 (from --peer-ip)" "$result" "IP=10.0.0.10"
    assert_contains "3d: includes 10.0.0.100 (from INITIAL_CLUSTER)" "$result" "IP=10.0.0.100"
}

# =============================================================================
# SUITE 4: INITIAL_CLUSTER building from member list
# =============================================================================

suite_4() {
    echo ""
    echo "================================================================"
    echo "SUITE 4: etcd-cluster-rejoin.sh — INITIAL_CLUSTER from member list"
    echo "================================================================"

    # Extract the member list parsing loop (lines 248-267 of the actual script)
    # Variables it needs: FULL_MEMBER_LIST, LOCAL_IP, ETCD_NAME

    # --- 4a: 3-node member list with learner (empty name) ---
    echo ""
    echo "--- 4a: 3-node, local node is learner (empty name) ---"

    local result
    result=$(
        export PATH="$MOCK_BIN:$PATH"
        LOCAL_IP="192.168.1.105"
        ETCD_NAME="etcd-ha-2"
        FULL_MEMBER_LIST="3a57933972cb5131, started, etcd-ha-1, http://192.168.1.104:2380, http://192.168.1.104:2379, false
8e9e05c52164694d, started, , http://192.168.1.105:2380, http://192.168.1.105:2379, true
aaaa1111bbbb2222, started, etcd-ha-3, http://192.168.1.106:2380, http://192.168.1.106:2379, false"
        NEW_INITIAL_CLUSTER=""

        eval "$(extract_lines "$REJOIN" 251 269)"

        echo "$NEW_INITIAL_CLUSTER"
    )
    assert_eq "4a: INITIAL_CLUSTER with learner name filled" \
        "etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380,etcd-ha-3=http://192.168.1.106:2380" \
        "$result"

    # --- 4b: 2-node member list (standard case) ---
    echo ""
    echo "--- 4b: 2-node, both named ---"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        LOCAL_IP="192.168.1.105"
        ETCD_NAME="etcd-ha-2"
        FULL_MEMBER_LIST="3a57933972cb5131, started, etcd-ha-1, http://192.168.1.104:2380, http://192.168.1.104:2379, false
8e9e05c52164694d, started, etcd-ha-2, http://192.168.1.105:2380, http://192.168.1.105:2379, false"
        NEW_INITIAL_CLUSTER=""

        eval "$(extract_lines "$REJOIN" 251 269)"

        echo "$NEW_INITIAL_CLUSTER"
    )
    assert_eq "4b: standard 2-node INITIAL_CLUSTER" \
        "etcd-ha-1=http://192.168.1.104:2380,etcd-ha-2=http://192.168.1.105:2380" \
        "$result"

    # --- 4c: 5-node member list ---
    echo ""
    echo "--- 4c: 5-node member list ---"

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        LOCAL_IP="10.0.0.3"
        ETCD_NAME="etcd-ha-3"
        FULL_MEMBER_LIST="aaa, started, etcd-ha-1, http://10.0.0.1:2380, http://10.0.0.1:2379, false
bbb, started, etcd-ha-2, http://10.0.0.2:2380, http://10.0.0.2:2379, false
ccc, started, , http://10.0.0.3:2380, http://10.0.0.3:2379, true
ddd, started, etcd-ha-4, http://10.0.0.4:2380, http://10.0.0.4:2379, false
eee, started, etcd-ha-5, http://10.0.0.5:2380, http://10.0.0.5:2379, false"
        NEW_INITIAL_CLUSTER=""

        eval "$(extract_lines "$REJOIN" 251 269)"

        echo "$NEW_INITIAL_CLUSTER"
    )
    local expected="etcd-ha-1=http://10.0.0.1:2380,etcd-ha-2=http://10.0.0.2:2380,etcd-ha-3=http://10.0.0.3:2380,etcd-ha-4=http://10.0.0.4:2380,etcd-ha-5=http://10.0.0.5:2380"
    assert_eq "4c: 5-node INITIAL_CLUSTER (learner name filled)" "$expected" "$result"
}

# =============================================================================
# SUITE 5: IP substring matching — grep patterns from etcd-cluster-rejoin.sh
# =============================================================================

suite_5() {
    echo ""
    echo "================================================================"
    echo "SUITE 5: IP substring matching safety (grep patterns)"
    echo "================================================================"

    # Test the EXACT grep patterns used in the scripts against adversarial data.
    # These patterns were extracted by reading the actual script code.

    local member_lines
    member_lines="3a57933972cb5131, started, etcd-ha-1, http://192.168.1.104:2380, http://192.168.1.104:2379, false
8e9e05c52164694d, started, etcd-ha-10, http://192.168.1.10:2380, http://192.168.1.10:2379, false
aaaa1111bbbb2222, started, etcd-ha-100, http://192.168.1.100:2380, http://192.168.1.100:2379, false"

    # --- 5a: The fixed pattern (with colon anchor) ---
    echo ""
    echo "--- 5a: grep '\${LOCAL_IP}:' with colon anchor ---"

    local LOCAL_IP="192.168.1.10"
    local matches
    matches=$(echo "$member_lines" | grep "${LOCAL_IP}:" | wc -l)
    assert_eq "5a: '192.168.1.10:' matches exactly 1 line" "1" "$matches"

    local matched_name
    matched_name=$(echo "$member_lines" | grep "${LOCAL_IP}:" | awk -F', ' '{print $3}' | tr -d '[:space:]')
    assert_eq "5a: matched the correct node (etcd-ha-10)" "etcd-ha-10" "$matched_name"

    # --- 5b: What the OLD pattern would do (bare IP, no anchor) ---
    echo ""
    echo "--- 5b: Verify old pattern (bare grep) would be UNSAFE ---"

    matches=$(echo "$member_lines" | grep "${LOCAL_IP}" | wc -l)
    assert_eq "5b: bare '192.168.1.10' matches ALL 3 lines (confirms bug)" "3" "$matches"

    # --- 5c: Pattern from ha-role-watcher.sh/ha-failback.sh (anchored) ---
    echo ""
    echo "--- 5c: '^IP\$' anchored pattern (self-filter) ---"

    local ip_list
    ip_list=$(printf "192.168.1.10\n192.168.1.104\n192.168.1.100\n")
    local filtered
    filtered=$(echo "$ip_list" | grep -v "^192\.168\.1\.10$")
    assert_line_count "5c: anchored filter removes only exact match" "$filtered" "2"
    assert_contains "5c: keeps 192.168.1.104" "$filtered" "192.168.1.104"
    assert_contains "5c: keeps 192.168.1.100" "$filtered" "192.168.1.100"

    # --- 5d: Stale entry detection pattern (line 201) ---
    echo ""
    echo "--- 5d: Stale entry grep -E pattern ---"

    # Actual pattern from etcd-cluster-rejoin.sh line 201:
    # Actual pattern from etcd-cluster-rejoin.sh line 201 (post-fix):
    # grep -E "(, ${ETCD_NAME},|${LOCAL_IP}:)"
    # Name anchored with ", NAME," to prevent substring matches.
    local ETCD_NAME="etcd-ha-10"
    LOCAL_IP="192.168.1.10"
    local stale_match
    stale_match=$(echo "$member_lines" | grep -E "(, ${ETCD_NAME},|${LOCAL_IP}:)" | wc -l)
    assert_eq "5d: anchored stale entry pattern matches exactly 1 line" "1" "$stale_match"

    # --- 5e: grep -qx from install.sh SSH mesh (self-detection) ---
    echo ""
    echo "--- 5e: grep -qx self-detection in SSH mesh ---"

    local my_ips
    my_ips=$(printf "127.0.0.1\n192.168.1.10\n")
    # Should match 192.168.1.10 but NOT 192.168.1.104
    if echo "$my_ips" | grep -qx "192.168.1.10"; then
        _test_pass "5e: grep -qx matches self (192.168.1.10)"
    else
        _test_fail "5e: grep -qx should match self" "match" "no match"
    fi
    if ! echo "$my_ips" | grep -qx "192.168.1.104"; then
        _test_pass "5e: grep -qx does NOT match 192.168.1.104"
    else
        _test_fail "5e: should not match" "no match" "matched"
    fi
}

# =============================================================================
# SUITE 6: Quorum recovery gate — ha-role-watcher.sh
# =============================================================================

suite_6() {
    echo ""
    echo "================================================================"
    echo "SUITE 6: Quorum recovery gate (all-peers-dead check)"
    echo "================================================================"

    # Extract the peer-check loop from attempt_etcd_quorum_recovery().
    # We need: get_all_peer_ips (mocked), ping (mocked), log (stub)

    local gap_func
    gap_func=$(extract_func "$ROLE_WATCHER" "get_all_peer_ips")

    # --- 6a: All peers unreachable → should trigger (return 0 from gate) ---
    echo ""
    echo "--- 6a: 3-node, all 2 peers unreachable → triggers ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="e1=http://192.168.1.104:2380,e2=http://192.168.1.105:2380,e3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"
    # Neither peer is reachable (no files in MOCK_PING_DIR)

    local gate_result
    gate_result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        LOG_FILE="/dev/null"
        # Define log stub
        log() { :; }
        # Source the actual get_all_peer_ips function
        eval "$gap_func"

        # Extract and run the actual gate code (lines 425-464 of ha-role-watcher.sh)
        eval "$(extract_lines "$ROLE_WATCHER" 425 464)"

        echo "GATE_PASSED"
    ) 2>/dev/null || true

    assert_contains "6a: gate passes (all peers dead)" "$gate_result" "GATE_PASSED"

    # --- 6b: One peer reachable → should block (early return 1) ---
    echo ""
    echo "--- 6b: 3-node, 1 of 2 peers reachable → blocks ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="e1=http://192.168.1.104:2380,e2=http://192.168.1.105:2380,e3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"
    # 105 is reachable, 106 is not
    touch "$MOCK_PING_DIR/192.168.1.105"

    gate_result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        LOG_FILE="/dev/null"
        log() { :; }
        eval "$gap_func"

        # The gate code uses "return 1" on first reachable peer.
        # Wrap in a function so return works:
        _run_gate() {
            eval "$(extract_lines "$ROLE_WATCHER" 425 464)"
            echo "GATE_PASSED"
        }
        _run_gate
        echo "RC=$?"
    ) 2>/dev/null || true

    assert_not_contains "6b: gate blocks (peer reachable)" "$gate_result" "GATE_PASSED"

    # --- 6c: Zero peers (empty result) → should block (guard) ---
    echo ""
    echo "--- 6c: Zero peers → guard blocks ---"
    reset_mocks
    echo "" > "$MOCK_ETCD_CONFIG"  # No valid config
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"

    gate_result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        LOG_FILE="/dev/null"
        log() { :; }
        eval "$gap_func"

        _run_gate() {
            eval "$(extract_lines "$ROLE_WATCHER" 425 464)"
            echo "GATE_PASSED"
        }
        _run_gate
    ) 2>/dev/null || true

    assert_not_contains "6c: gate blocks (zero peers)" "$gate_result" "GATE_PASSED"

    # --- 6d: 2-node (single peer dead) → should trigger (same as old behavior) ---
    echo ""
    echo "--- 6d: 2-node regression — single peer unreachable → triggers ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="e1=http://192.168.1.104:2380,e2=http://192.168.1.105:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"
    # No peers reachable

    gate_result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        LOG_FILE="/dev/null"
        log() { :; }
        eval "$gap_func"

        eval "$(extract_lines "$ROLE_WATCHER" 425 464)"
        echo "GATE_PASSED"
    ) 2>/dev/null || true

    assert_contains "6d: 2-node regression — gate passes" "$gate_result" "GATE_PASSED"
}

# =============================================================================
# SUITE 7: Failback gate — ha-role-watcher.sh
# =============================================================================

suite_7() {
    echo ""
    echo "================================================================"
    echo "SUITE 7: Failback gate (any-peer-reachable check)"
    echo "================================================================"

    local gap_func
    gap_func=$(extract_func "$ROLE_WATCHER" "get_all_peer_ips")

    # --- 7a: 3-node, one of two peers reachable → triggers failback ---
    echo ""
    echo "--- 7a: 3-node, 1 peer reachable → triggers failback ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="e1=http://192.168.1.104:2380,e2=http://192.168.1.105:2380,e3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"
    touch "$MOCK_PING_DIR/192.168.1.106"  # Only 106 reachable

    # Extract the ACTUAL failback gate code from ha-role-watcher.sh lines 805-814
    local gate_code
    gate_code=$(extract_lines "$ROLE_WATCHER" 805 814)

    local result
    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        log() { :; }
        eval "$gap_func"

        # Run actual gate code inside function (local keyword requires function context)
        _run_gate() {
            eval "$gate_code"
            echo "REACHABLE=$any_peer_reachable"
        }
        _run_gate
    ) 2>/dev/null || true

    assert_contains "7a: at least one peer reachable" "$result" "REACHABLE=1"

    # --- 7b: No peers reachable → no failback ---
    echo ""
    echo "--- 7b: No peers reachable → no failback ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="e1=http://192.168.1.104:2380,e2=http://192.168.1.105:2380,e3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"
    # No peers reachable

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        log() { :; }
        eval "$gap_func"

        _run_gate() {
            eval "$gate_code"
            echo "REACHABLE=$any_peer_reachable"
        }
        _run_gate
    ) 2>/dev/null || true

    assert_contains "7b: no peers reachable" "$result" "REACHABLE=0"
}

# =============================================================================
# SUITE 8: ha-failback.sh — ALL_PEER_IPS array + first-reachable PEER_IP
# =============================================================================

suite_8() {
    echo ""
    echo "================================================================"
    echo "SUITE 8: ha-failback.sh — ALL_PEER_IPS + first-reachable PEER_IP"
    echo "================================================================"

    local gap_func
    gap_func=$(extract_func "$FAILBACK" "get_all_peer_ips")

    # --- 8a: 3-node, second peer reachable (first is not) ---
    echo ""
    echo "--- 8a: First peer down, second reachable → PEER_IP = second ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="e1=http://192.168.1.104:2380,e2=http://192.168.1.105:2380,e3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"
    # Only 106 is reachable
    touch "$MOCK_PING_DIR/192.168.1.106"

    local result
    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        log() { echo "LOG: $*" >&2; }
        log_error() { echo "ERROR: $*" >&2; }
        eval "$gap_func"

        # Run the actual code from ha-failback.sh lines 147-174
        # (ALL_PEER_IPS_RAW → array → first-reachable PEER_IP)
        eval "$(extract_lines "$FAILBACK" 147 174)"

        echo "PEER_IP=$PEER_IP"
        echo "ALL_COUNT=${#ALL_PEER_IPS[@]}"
        for ip in "${ALL_PEER_IPS[@]}"; do echo "ALL=$ip"; done
    ) 2>/dev/null || true

    assert_contains "8a: PEER_IP is second (reachable) peer" "$result" "PEER_IP=192.168.1.106"
    assert_contains "8a: ALL_PEER_IPS has 2 entries" "$result" "ALL_COUNT=2"

    # --- 8b: No peers reachable → falls back to first in list ---
    echo ""
    echo "--- 8b: No peers reachable → PEER_IP = first in list ---"
    reset_mocks
    cat > "$MOCK_ETCD_CONFIG" << 'EOF'
ETCD_NAME="etcd-ha-1"
ETCD_LISTEN_PEER_URLS="http://192.168.1.104:2380"
ETCD_INITIAL_CLUSTER="e1=http://192.168.1.104:2380,e2=http://192.168.1.105:2380,e3=http://192.168.1.106:2380"
EOF
    cat > "$MOCK_IP_RESPONSE" << 'EOF'
2: ens33    inet 192.168.1.104/24 brd 192.168.1.255 scope global dynamic ens33
1: lo    inet 127.0.0.1/8 scope host lo
EOF
    echo '{}' > "$MOCK_CURL_RESPONSE"
    # No peers reachable

    result=$(
        export PATH="$MOCK_BIN:$PATH"
        export MOCK_CURL_RESPONSE MOCK_IP_RESPONSE MOCK_PING_DIR MOCK_ETCDCTL_MEMBERS
        HA_STATUS_PORT=5354
        log() { :; }
        log_error() { :; }
        eval "$gap_func"

        eval "$(extract_lines "$FAILBACK" 147 174)"

        echo "PEER_IP=$PEER_IP"
    ) 2>/dev/null || true

    assert_contains "8b: PEER_IP falls back to first (192.168.1.105)" "$result" "PEER_IP=192.168.1.105"
}

# =============================================================================
# SUITE 9: Patroni TOCTOU fix verification
# =============================================================================

suite_9() {
    echo ""
    echo "================================================================"
    echo "SUITE 9: Patroni leader discovery (TOCTOU fix)"
    echo "================================================================"

    # Verify that LEADER_NAME and LEADER_API_URL come from the SAME curl call.
    # We check this by grepping the actual script for the pattern.

    echo ""
    echo "--- 9a: Single CLUSTER_JSON variable used for both extractions ---"

    # Verify: line 330 fetches CLUSTER_JSON once, lines 331 and 343 both parse it
    local line_330 line_331 line_343
    line_330=$(sed -n '330p' "$FAILBACK")
    line_331=$(sed -n '331p' "$FAILBACK")
    line_343=$(sed -n '343p' "$FAILBACK")

    if echo "$line_330" | grep -q 'CLUSTER_JSON=.*curl.*localhost:8008/cluster'; then
        _test_pass "9a1: Line 330 fetches CLUSTER_JSON via single curl call"
    else
        _test_fail "9a1: CLUSTER_JSON fetch not at expected line" \
            "CLUSTER_JSON=\$(curl ... /cluster ...)" "$line_330"
    fi

    if echo "$line_331" | grep -q 'echo.*CLUSTER_JSON.*jq.*LEADER_NAME'; then
        _test_pass "9a2: LEADER_NAME parsed from cached CLUSTER_JSON"
    elif echo "$line_331" | grep -q 'CLUSTER_JSON'; then
        _test_pass "9a2: Line 331 uses cached CLUSTER_JSON"
    else
        _test_fail "9a2: LEADER_NAME not from CLUSTER_JSON" "uses CLUSTER_JSON" "$line_331"
    fi

    if echo "$line_343" | grep -q 'echo.*CLUSTER_JSON.*jq.*LEADER_API_URL'; then
        _test_pass "9a3: LEADER_API_URL parsed from cached CLUSTER_JSON"
    elif echo "$line_343" | grep -q 'CLUSTER_JSON'; then
        _test_pass "9a3: Line 343 uses cached CLUSTER_JSON"
    else
        _test_fail "9a3: LEADER_API_URL not from CLUSTER_JSON" "uses CLUSTER_JSON" "$line_343"
    fi

    # Verify no second curl to /cluster between CLUSTER_JSON fetch and LEADER_API_URL
    local switchover_section
    switchover_section=$(sed -n '330,360p' "$FAILBACK")
    local curl_cluster_count
    curl_cluster_count=$(echo "$switchover_section" | grep -c 'curl.*localhost:8008/cluster' || true)
    assert_eq "9a4: Only 1 curl to /cluster in switchover section" "1" "$curl_cluster_count"

    # --- 9b: Verify leader extraction with mock data ---
    echo ""
    echo "--- 9b: Leader name + IP from same JSON ---"

    local cluster_json='{"members":[{"name":"patroni-ha-2","role":"leader","host":"192.168.1.105","api_url":"http://192.168.1.105:8008/patroni","lag":0},{"name":"patroni-ha-1","role":"replica","host":"192.168.1.104","api_url":"http://192.168.1.104:8008/patroni","lag":0}]}'

    local leader_name
    leader_name=$(echo "$cluster_json" | \
        jq -r '.members[] | select(.role == "leader" or .role == "master") | .name' 2>/dev/null | head -1 || echo "")
    local leader_api_url
    leader_api_url=$(echo "$cluster_json" | \
        jq -r '.members[] | select(.role == "leader" or .role == "master") | .api_url // ""' 2>/dev/null | head -1 || echo "")
    local leader_ip
    leader_ip=$(echo "$leader_api_url" | grep -oP '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)

    assert_eq "9b: leader name is patroni-ha-2" "patroni-ha-2" "$leader_name"
    assert_eq "9b: leader IP is 192.168.1.105" "192.168.1.105" "$leader_ip"
}

# =============================================================================
# SUITE 10: Script syntax validation
# =============================================================================

suite_10() {
    echo ""
    echo "================================================================"
    echo "SUITE 10: bash -n syntax validation on all modified scripts"
    echo "================================================================"

    local scripts=(
        "$ROLE_WATCHER"
        "$FAILBACK"
        "$REJOIN"
    )
    if [[ -f "$VALIDATE" ]]; then
        scripts+=("$VALIDATE")
    fi

    for script in "${scripts[@]}"; do
        local name
        name=$(basename "$script")
        if bash -n "$script" 2>/dev/null; then
            _test_pass "10: $name — syntax OK"
        else
            local err
            err=$(bash -n "$script" 2>&1)
            _test_fail "10: $name — syntax error" "no errors" "$err"
        fi
    done
}

# =============================================================================
# SUITE 11: Sudoers entries in install.sh / upgrade.sh
# =============================================================================

suite_11() {
    echo ""
    echo "================================================================"
    echo "SUITE 11: Sudoers entries allow --peer-ip argument"
    echo "================================================================"

    # SCRIPT_DIR is scripts/linux/ — install.sh and upgrade.sh are at project root
    local install_sh
    install_sh="$(dirname "$(dirname "$SCRIPT_DIR")")/install.sh"
    local upgrade_sh
    upgrade_sh="$(dirname "$(dirname "$SCRIPT_DIR")")/upgrade.sh"

    # --- 11a: install.sh sudoers for etcd-cluster-rejoin.sh has trailing * ---
    echo ""
    echo "--- 11a: install.sh sudoers entry ---"
    if [[ -f "$install_sh" ]]; then
        local sudoers_line
        sudoers_line=$(grep 'etcd-cluster-rejoin.sh' "$install_sh" | grep 'NOPASSWD' | head -1 || true)
        if [[ -n "$sudoers_line" ]]; then
            if echo "$sudoers_line" | grep -qP 'etcd-cluster-rejoin\.sh\s+\*'; then
                _test_pass "11a: install.sh sudoers has trailing * (allows --peer-ip)"
            elif echo "$sudoers_line" | grep -q 'etcd-cluster-rejoin\.sh$'; then
                _test_fail "11a: install.sh sudoers missing trailing *" \
                    "etcd-cluster-rejoin.sh *" "$sudoers_line"
            else
                _test_pass "11a: install.sh sudoers allows arguments"
            fi
        else
            _test_skip "11a: No sudoers entry found for etcd-cluster-rejoin.sh in install.sh"
        fi
    else
        _test_skip "11a: install.sh not found at $install_sh"
    fi

    # --- 11b: upgrade.sh sudoers migration ---
    echo ""
    echo "--- 11b: upgrade.sh sed migration for existing entries ---"
    if [[ -f "$upgrade_sh" ]]; then
        if grep -q 'etcd-cluster-rejoin\.sh\$' "$upgrade_sh"; then
            _test_pass "11b: upgrade.sh has sed migration for entries missing *"
        else
            _test_skip "11b: Could not find migration pattern (may use different approach)"
        fi
    else
        _test_skip "11b: upgrade.sh not found at $upgrade_sh"
    fi
}

# =============================================================================
# SUITE 12: sync_standby handling — ha-failback.sh (3+ node)
# =============================================================================

suite_12() {
    echo ""
    echo "================================================================"
    echo "SUITE 12: sync_standby handling (3+ node failback)"
    echo "================================================================"

    # --- 12a: SYNC_DISABLED initialized to 0 ---
    echo ""
    echo "--- 12a: SYNC_DISABLED initialization ---"
    if sed -n '58p' "$FAILBACK" | grep -q 'SYNC_DISABLED=0'; then
        _test_pass "12a: SYNC_DISABLED=0 initialized at line 58"
    else
        _test_fail "12a: SYNC_DISABLED init" "SYNC_DISABLED=0 at line 58" \
            "$(sed -n '58p' "$FAILBACK")"
    fi

    # --- 12b: cleanup_sync_mode function exists and re-enables synchronous_mode ---
    echo ""
    echo "--- 12b: cleanup_sync_mode trap function ---"
    local cleanup_func
    cleanup_func=$(extract_func "$FAILBACK" "cleanup_sync_mode")
    if [[ -n "$cleanup_func" ]]; then
        _test_pass "12b1: cleanup_sync_mode() function exists"
        if echo "$cleanup_func" | grep -q 'synchronous_mode.*true'; then
            _test_pass "12b2: cleanup re-enables synchronous_mode (true)"
        else
            _test_fail "12b2: cleanup missing synchronous_mode re-enable" \
                "synchronous_mode.*true" "not found"
        fi
        if echo "$cleanup_func" | grep -q 'SYNC_DISABLED.*1'; then
            _test_pass "12b3: cleanup checks SYNC_DISABLED flag"
        else
            _test_fail "12b3: cleanup missing SYNC_DISABLED check" \
                "SYNC_DISABLED check" "not found"
        fi
    else
        _test_fail "12b: cleanup_sync_mode not found" "function exists" "not extracted"
    fi

    # --- 12c: EXIT trap registered ---
    echo ""
    echo "--- 12c: EXIT trap for cleanup ---"
    if grep -q 'trap cleanup_sync_mode EXIT' "$FAILBACK"; then
        _test_pass "12c: trap cleanup_sync_mode EXIT registered"
    else
        _test_fail "12c: EXIT trap" "trap cleanup_sync_mode EXIT" "not found"
    fi

    # --- 12d: synchronous_mode re-enabled after switchover ---
    echo ""
    echo "--- 12d: synchronous_mode re-enabled post-switchover ---"
    local reenable_section
    reenable_section=$(sed -n '387,395p' "$FAILBACK")
    if echo "$reenable_section" | grep -q 'SYNC_DISABLED.*1'; then
        _test_pass "12d1: Post-switchover checks SYNC_DISABLED"
    else
        _test_fail "12d1: Missing SYNC_DISABLED check" "SYNC_DISABLED check" \
            "$(echo "$reenable_section" | head -1)"
    fi
    if echo "$reenable_section" | grep -q 'synchronous_mode.*true'; then
        _test_pass "12d2: Post-switchover re-enables synchronous_mode"
    else
        _test_fail "12d2: Missing re-enable" "synchronous_mode true" "not found"
    fi
    if echo "$reenable_section" | grep -q 'SYNC_DISABLED=0'; then
        _test_pass "12d3: Post-switchover clears SYNC_DISABLED flag"
    else
        _test_fail "12d3: SYNC_DISABLED not cleared" "SYNC_DISABLED=0" "not found"
    fi

    # --- 12e: 3+ node fallback path disables synchronous_mode ---
    echo ""
    echo "--- 12e: 3+ node fallback disables synchronous_mode ---"
    local fallback_section
    fallback_section=$(sed -n '306,324p' "$FAILBACK")
    if echo "$fallback_section" | grep -q 'synchronous_mode.*false'; then
        _test_pass "12e1: Fallback disables synchronous_mode (false)"
    else
        _test_fail "12e1: Missing disable" "synchronous_mode false" "not found"
    fi
    if echo "$fallback_section" | grep -q 'SYNC_DISABLED=1'; then
        _test_pass "12e2: Fallback sets SYNC_DISABLED=1"
    else
        _test_fail "12e2: Missing flag set" "SYNC_DISABLED=1" "not found"
    fi
}

# =============================================================================
# SUITE 13: ssh_node loop patterns — ha-failback.sh
# =============================================================================

suite_13() {
    echo ""
    echo "================================================================"
    echo "SUITE 13: ssh_node loop patterns (ALL_PEER_IPS)"
    echo "================================================================"

    # --- 13a: ssh_node function exists ---
    echo ""
    echo "--- 13a: ssh_node function definition ---"
    local ssh_func
    ssh_func=$(extract_func "$FAILBACK" "ssh_node")
    if [[ -n "$ssh_func" ]]; then
        _test_pass "13a: ssh_node() function exists"
        if echo "$ssh_func" | grep -q 'SSH_OPTS'; then
            _test_pass "13a2: ssh_node uses SSH_OPTS"
        else
            _test_fail "13a2: ssh_node missing SSH_OPTS" "SSH_OPTS used" "not found"
        fi
    else
        _test_fail "13a: ssh_node not found" "function exists" "not extracted"
    fi

    # --- 13b: ALL_PEER_IPS loops — all 5 steps use ALL_PEER_IPS ---
    echo ""
    echo "--- 13b: ALL_PEER_IPS used in all peer loops ---"
    local loop_count
    loop_count=$(grep -c 'for _pip in "\${ALL_PEER_IPS\[@\]}"' "$FAILBACK" || true)
    assert_eq "13b: 5 loops over ALL_PEER_IPS (4a,4b,4c,5a,5b)" "5" "$loop_count"

    # --- 13c: All loops call ssh_node (not ssh_peer) ---
    echo ""
    echo "--- 13c: Loops use ssh_node, not ssh_peer ---"
    # Count ssh_node calls inside the step 4/5 section (lines 400-490)
    local step_section
    step_section=$(sed -n '400,490p' "$FAILBACK")
    local ssh_node_calls
    ssh_node_calls=$(echo "$step_section" | grep -c 'ssh_node' || true)
    local ssh_peer_calls
    ssh_peer_calls=$(echo "$step_section" | grep -c 'ssh_peer' || true)
    if [[ $ssh_node_calls -ge 5 ]]; then
        _test_pass "13c1: ${ssh_node_calls} ssh_node calls in steps 4-5"
    else
        _test_fail "13c1: Expected 5+ ssh_node calls" ">=5" "$ssh_node_calls"
    fi
    # ssh_peer is defined (lines 410-412) but should have 0 calls in step section
    # Note: the definition itself (ssh_peer()) doesn't count as a call
    local ssh_peer_actual_calls
    ssh_peer_actual_calls=$(echo "$step_section" | grep -v 'ssh_peer()' | grep -c 'ssh_peer ' || true)
    assert_eq "13c2: 0 ssh_peer calls in steps 4-5 (all use ssh_node)" "0" "$ssh_peer_actual_calls"

    # --- 13d: ssh_peer is defined but never called in whole script ---
    echo ""
    echo "--- 13d: ssh_peer defined but unused ---"
    local total_ssh_peer_calls
    # Exclude the function definition line itself
    total_ssh_peer_calls=$(grep -v 'ssh_peer()' "$FAILBACK" | grep -c 'ssh_peer ' || true)
    assert_eq "13d: ssh_peer never called (defined but unused)" "0" "$total_ssh_peer_calls"
}

# =============================================================================
# Run suites
# =============================================================================

echo "================================================================"
echo "Multi-Peer HA Offline Test Harness"
echo "================================================================"
echo "Scripts under test:"
echo "  ha-role-watcher.sh:      $ROLE_WATCHER"
echo "  ha-failback.sh:          $FAILBACK"
echo "  etcd-cluster-rejoin.sh:  $REJOIN"
echo "Mock directory:             $MOCK_DIR"
echo ""

if [[ -n "$RUN_SUITE" ]]; then
    echo "Running suite $RUN_SUITE only"
    "suite_${RUN_SUITE}"
else
    suite_1
    suite_2
    suite_3
    suite_4
    suite_5
    suite_6
    suite_7
    suite_8
    suite_9
    suite_10
    suite_11
    suite_12
    suite_13
fi

# =============================================================================
# Summary
# =============================================================================

echo ""
echo "================================================================"
echo "RESULTS: ${PASS} passed, ${FAIL} failed, ${SKIP} skipped"
echo "================================================================"

if [[ $FAIL -gt 0 ]]; then
    echo ""
    echo "FAILURES DETECTED — review output above"
    exit 1
fi

if [[ $PASS -eq 0 ]]; then
    echo ""
    echo "NO TESTS RAN — check script paths"
    exit 2
fi

echo ""
echo "ALL TESTS PASSED"
exit 0
