#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool upgrade.sh Regression Harness
# =============================================================================
# Tests upgrade.sh functions in isolation by sourcing the script with mocked
# system commands. Does NOT perform actual upgrades — purely deterministic.
#
# Usage: bash tests/test_upgrade_regression.sh
#        bash tests/test_upgrade_regression.sh --verbose
#
# Exit codes:
#   0  All tests passed
#   1  One or more tests failed
# =============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
UPGRADE_SCRIPT="$PROJECT_ROOT/upgrade.sh"

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
# Test Framework
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
    [[ -n "$2" ]] && echo -e "        ${RED}Reason: $2${NC}"
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

assert_ne() {
    local actual="$1"
    local unexpected="$2"
    local msg="$3"
    if [[ "$actual" != "$unexpected" ]]; then
        pass "$msg"
    else
        fail "$msg" "expected NOT '$unexpected', got '$actual'"
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

assert_exit_code() {
    local actual="$1"
    local expected="$2"
    local msg="$3"
    if [[ "$actual" -eq "$expected" ]]; then
        pass "$msg"
    else
        fail "$msg" "expected exit code $expected, got $actual"
    fi
}

# =============================================================================
# Test: Version Comparison Logic
# =============================================================================
# Extracted from check_for_updates() — tests the sort -V comparison
test_version_comparison() {
    log_test "Version Comparison Logic"

    # Helper: returns "true" if target > current, "false" otherwise
    version_is_newer() {
        local current_clean="$1"
        local target_clean="$2"
        if [[ "$current_clean" != "$target_clean" ]]; then
            local newer
            newer=$(printf '%s\n' "$current_clean" "$target_clean" | sort -V | tail -1)
            if [[ "$newer" == "$target_clean" ]]; then
                echo "true"
                return
            fi
        fi
        echo "false"
    }

    # Same version
    assert_eq "$(version_is_newer "1.0.0" "1.0.0")" "false" "Same version → no update"

    # Newer version available
    assert_eq "$(version_is_newer "1.0.0" "1.1.0")" "true" "1.0.0 → 1.1.0 = update"
    assert_eq "$(version_is_newer "1.0.0" "2.0.0")" "true" "1.0.0 → 2.0.0 = update"
    assert_eq "$(version_is_newer "1.0.0" "1.0.1")" "true" "1.0.0 → 1.0.1 = update"

    # Downgrade attempt (target is older)
    assert_eq "$(version_is_newer "2.0.0" "1.0.0")" "false" "2.0.0 → 1.0.0 = no update (downgrade)"
    assert_eq "$(version_is_newer "1.1.0" "1.0.9")" "false" "1.1.0 → 1.0.9 = no update (downgrade)"

    # Pre-release versions
    assert_eq "$(version_is_newer "1.0.0" "1.0.1-beta")" "true" "1.0.0 → 1.0.1-beta = update"

    # Two-component versions
    assert_eq "$(version_is_newer "1.0" "1.1")" "true" "1.0 → 1.1 = update"
    assert_eq "$(version_is_newer "1.1" "1.0")" "false" "1.1 → 1.0 = no update"

    # Edge: empty strings (should not crash)
    assert_eq "$(version_is_newer "" "")" "false" "Empty versions → no update"
    assert_eq "$(version_is_newer "1.0.0" "")" "false" "Current vs empty → no update (sort -V)"
}

# =============================================================================
# Test: Version Format Validation
# =============================================================================
test_version_validation() {
    log_test "Version Format Validation"

    # Extracted regex from get_target_version()
    validate_version() {
        local v="$1"
        if [[ "$v" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?(-[a-zA-Z0-9]+)?$ ]]; then
            echo "valid"
        else
            echo "invalid"
        fi
    }

    assert_eq "$(validate_version "1.0.0")" "valid" "1.0.0 is valid"
    assert_eq "$(validate_version "1.0")" "valid" "1.0 is valid"
    assert_eq "$(validate_version "2.5.3-beta")" "valid" "2.5.3-beta is valid"
    assert_eq "$(validate_version "10.20.30")" "valid" "10.20.30 is valid"

    # Invalid formats
    assert_eq "$(validate_version "")" "invalid" "Empty string is invalid"
    assert_eq "$(validate_version "abc")" "invalid" "'abc' is invalid"
    assert_eq "$(validate_version "1")" "invalid" "'1' alone is invalid"
    assert_eq "$(validate_version "1.0.0.0")" "invalid" "Four components is invalid"
    assert_eq "$(validate_version "v1.0.0")" "invalid" "v-prefix is invalid (stripped before validation)"
    assert_eq "$(validate_version "1.0.0-")" "invalid" "Trailing dash is invalid"
    assert_eq "$(validate_version '1.0.0"; rm -rf /')" "invalid" "Injection attempt is invalid"
    assert_eq "$(validate_version '$(whoami)')" "invalid" "Command substitution is invalid"
}

# =============================================================================
# Test: Pool User Validation
# =============================================================================
test_pool_user_validation() {
    log_test "Pool User Validation (Security)"

    validate_user() {
        local u="$1"
        if [[ "$u" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
            echo "valid"
        else
            echo "invalid"
        fi
    }

    assert_eq "$(validate_user "spiraluser")" "valid" "spiraluser is valid"
    assert_eq "$(validate_user "pool_user")" "valid" "pool_user is valid"
    assert_eq "$(validate_user "_service")" "valid" "_service is valid"

    # Invalid (injection vectors)
    assert_eq "$(validate_user "root; rm -rf /")" "invalid" "Injection in username is rejected"
    assert_eq "$(validate_user 'user$(whoami)')" "invalid" "Command substitution in username rejected"
    assert_eq "$(validate_user "Root")" "invalid" "Uppercase rejected"
    assert_eq "$(validate_user "")" "invalid" "Empty username rejected"
    assert_eq "$(validate_user "123user")" "invalid" "Leading digit rejected"
}

# =============================================================================
# Test: GitHub API Response Parsing
# =============================================================================
test_github_api_parsing() {
    log_test "GitHub API Response Parsing"

    parse_tag_name() {
        echo "$1" | grep -oP '"tag_name": "\K[^"]+' | sed 's/^v//' || echo ""
    }

    parse_html_url() {
        echo "$1" | grep -oP '"html_url": "\K[^"]+' | head -1 || echo ""
    }

    # Valid response
    local valid_response='{"tag_name": "v1.2.3", "html_url": "https://github.com/SpiralPool/Spiral-Pool/releases/tag/v1.2.3"}'
    assert_eq "$(parse_tag_name "$valid_response")" "1.2.3" "Parse tag_name from valid response"
    assert_contains "$(parse_html_url "$valid_response")" "github.com" "Parse html_url from valid response"

    # Response without v prefix
    local no_v_response='{"tag_name": "1.2.3", "html_url": "https://example.com"}'
    assert_eq "$(parse_tag_name "$no_v_response")" "1.2.3" "Parse tag_name without v prefix"

    # Empty/malformed responses
    assert_eq "$(parse_tag_name "")" "" "Empty response → empty version"
    assert_eq "$(parse_tag_name '{"message": "Not Found"}')" "" "404 response → empty version"
    assert_eq "$(parse_tag_name '{"message": "API rate limit exceeded"}')" "" "Rate limit → empty version"
    assert_eq "$(parse_tag_name "<!DOCTYPE html>")" "" "HTML error page → empty version"

    # Malicious response (injection attempt)
    local injection_response='{"tag_name": "v1.0.0\"; rm -rf /; echo \"", "html_url": "http://evil.com"}'
    local parsed
    parsed=$(parse_tag_name "$injection_response")
    # The regex should stop at the first quote, so injection is truncated
    assert_eq "$parsed" '1.0.0\' "Injection in tag_name is truncated by parser"
}

# =============================================================================
# Test: HTTP Status Code Handling
# =============================================================================
test_http_status_handling() {
    log_test "HTTP Status Code Handling"

    # Simulate the curl -w '\n%{http_code}' output format
    extract_http_code() {
        echo "$1" | tail -1
    }

    extract_body() {
        echo "$1" | sed '$d'
    }

    # 200 OK
    local ok_response='{"tag_name": "v1.0.0"}
200'
    assert_eq "$(extract_http_code "$ok_response")" "200" "Extract 200 status code"
    assert_contains "$(extract_body "$ok_response")" "tag_name" "Extract body from 200 response"

    # 403 Rate Limited
    local rate_limited='{"message": "API rate limit exceeded"}
403'
    assert_eq "$(extract_http_code "$rate_limited")" "403" "Extract 403 status code"

    # 404 Not Found
    local not_found='{"message": "Not Found"}
404'
    assert_eq "$(extract_http_code "$not_found")" "404" "Extract 404 status code"

    # 500 Server Error
    local server_error='{"message": "Internal Server Error"}
500'
    assert_eq "$(extract_http_code "$server_error")" "500" "Extract 500 status code"

    # Empty response (curl failed entirely)
    local empty_response=''
    assert_eq "$(extract_http_code "$empty_response")" "" "Empty curl output → empty status"

    # Multiline body
    local multiline='{"tag_name": "v2.0.0",
"html_url": "https://example.com",
"body": "Release notes"}
200'
    assert_eq "$(extract_http_code "$multiline")" "200" "Extract status from multiline response"
    assert_contains "$(extract_body "$multiline")" "tag_name" "Extract body from multiline response"
}

# =============================================================================
# Test: JSON Output (check_for_updates format)
# =============================================================================
test_json_output() {
    log_test "JSON Output Format (check_for_updates)"

    # Simulate the manual JSON generation (when jq is unavailable)
    generate_json() {
        local cv="$1" lv="$2" ua="$3" url="$4"
        local SAFE_URL="${url//\\/\\\\}"
        SAFE_URL="${SAFE_URL//\"/\\\"}"
        cat << EOF
{
    "current_version": "${cv}",
    "latest_version": "${lv}",
    "update_available": ${ua},
    "release_url": "${SAFE_URL}",
    "upgrade_command": "cd /spiralpool && sudo ./upgrade.sh"
}
EOF
    }

    local json
    json=$(generate_json "1.0.0" "1.1.0" "true" "https://github.com/example")
    assert_contains "$json" '"current_version": "1.0.0"' "JSON has current_version"
    assert_contains "$json" '"latest_version": "1.1.0"' "JSON has latest_version"
    assert_contains "$json" '"update_available": true' "JSON has update_available boolean"

    # URL with special characters
    json=$(generate_json "1.0.0" "1.0.0" "false" 'https://example.com/path?a=1&b="2"')
    assert_contains "$json" 'release_url' "JSON with special URL chars doesn't crash"
}

# =============================================================================
# Test: Backup Name Generation
# =============================================================================
test_backup_name() {
    log_test "Backup Name Generation"

    local CURRENT_VERSION="1.0.0"
    local TARGET_VERSION="1.1.0"
    local TIMESTAMP="20260228_120000"
    local BACKUP_NAME="pre-upgrade-${CURRENT_VERSION}-to-${TARGET_VERSION}-${TIMESTAMP}"

    assert_eq "$BACKUP_NAME" "pre-upgrade-1.0.0-to-1.1.0-20260228_120000" "Backup name format is correct"
    assert_contains "$BACKUP_NAME" "pre-upgrade-" "Backup name has prefix"

    # Verify find pattern would match it
    if [[ "$BACKUP_NAME" == pre-upgrade-* ]]; then
        pass "Backup name matches find pattern 'pre-upgrade-*'"
    else
        fail "Backup name doesn't match find pattern"
    fi
}

# =============================================================================
# Test: Argument Parsing
# =============================================================================
test_argument_parsing() {
    log_test "Argument Parsing"

    # Simulate argument parsing
    parse_args() {
        local USE_LOCAL=false FETCH_LATEST=true FORCE_UPGRADE=false SKIP_BACKUP=false
        local CHECK_ONLY=false AUTO_MODE=false UPDATE_STRATUM=true UPDATE_DASHBOARD=true
        local UPDATE_SENTINEL=true UPDATE_SERVICES=true FIX_CONFIG=false SKIP_START=false

        while [[ $# -gt 0 ]]; do
            case $1 in
                --check)           CHECK_ONLY=true; shift ;;
                --local)           USE_LOCAL=true; FETCH_LATEST=false; shift ;;
                --force)           FORCE_UPGRADE=true; shift ;;
                --no-backup)       SKIP_BACKUP=true; shift ;;
                --auto)            AUTO_MODE=true; FORCE_UPGRADE=true; shift ;;
                --stratum-only)    UPDATE_DASHBOARD=false; UPDATE_SENTINEL=false; shift ;;
                --dashboard-only)  UPDATE_STRATUM=false; UPDATE_SENTINEL=false; shift ;;
                --sentinel-only)   UPDATE_STRATUM=false; UPDATE_DASHBOARD=false; shift ;;
                --no-stratum)      UPDATE_STRATUM=false; shift ;;
                --no-dashboard)    UPDATE_DASHBOARD=false; shift ;;
                --no-sentinel)     UPDATE_SENTINEL=false; shift ;;
                --fix-config)      FIX_CONFIG=true; shift ;;
                --skip-start)      SKIP_START=true; shift ;;
                --full)            UPDATE_SERVICES=true; FIX_CONFIG=true; shift ;;
                --rollback)        shift; shift 2>/dev/null || true ;;
                *)                 echo "error"; return 1 ;;
            esac
        done

        echo "LOCAL=$USE_LOCAL FETCH=$FETCH_LATEST FORCE=$FORCE_UPGRADE AUTO=$AUTO_MODE STRATUM=$UPDATE_STRATUM DASH=$UPDATE_DASHBOARD SENT=$UPDATE_SENTINEL"
    }

    local result

    result=$(parse_args --local)
    assert_contains "$result" "LOCAL=true" "--local sets USE_LOCAL"
    assert_contains "$result" "FETCH=false" "--local clears FETCH_LATEST"

    result=$(parse_args --auto)
    assert_contains "$result" "AUTO=true" "--auto sets AUTO_MODE"
    assert_contains "$result" "FORCE=true" "--auto implies FORCE_UPGRADE"

    result=$(parse_args --stratum-only)
    assert_contains "$result" "STRATUM=true" "--stratum-only keeps stratum"
    assert_contains "$result" "DASH=false" "--stratum-only disables dashboard"
    assert_contains "$result" "SENT=false" "--stratum-only disables sentinel"

    result=$(parse_args --dashboard-only)
    assert_contains "$result" "STRATUM=false" "--dashboard-only disables stratum"
    assert_contains "$result" "DASH=true" "--dashboard-only keeps dashboard"

    # Unknown argument
    result=$(parse_args --invalid 2>/dev/null) || true
    # Should have returned error
    if [[ $? -ne 0 ]] || [[ "$result" == "error" ]]; then
        pass "Unknown argument returns error"
    else
        fail "Unknown argument should return error"
    fi
}

# =============================================================================
# Test: Rollback Integrity Check
# =============================================================================
test_rollback_integrity() {
    log_test "Rollback Integrity Check (Checksum Verification)"

    local test_dir
    test_dir=$(mktemp -d)

    # Create fake backup with checksums
    mkdir -p "$test_dir/backup"
    echo "binary content" > "$test_dir/backup/spiralstratum"
    echo "config content" > "$test_dir/backup/config.yaml"
    echo "Upgrade performed: test" > "$test_dir/backup/upgrade-info.txt"
    (cd "$test_dir/backup" && find . -type f ! -name "CHECKSUMS.sha256" -exec sha256sum {} \; > CHECKSUMS.sha256)

    # Verify checksums pass
    if (cd "$test_dir/backup" && sha256sum -c CHECKSUMS.sha256 --quiet 2>/dev/null); then
        pass "Valid backup passes checksum verification"
    else
        fail "Valid backup should pass checksum verification"
    fi

    # Corrupt a file
    echo "corrupted" > "$test_dir/backup/spiralstratum"

    # Verify checksums fail
    if (cd "$test_dir/backup" && sha256sum -c CHECKSUMS.sha256 --quiet 2>/dev/null); then
        fail "Corrupted backup should fail checksum verification"
    else
        pass "Corrupted backup fails checksum verification"
    fi

    rm -rf "$test_dir"
}

# =============================================================================
# Test: Symlink Protection
# =============================================================================
test_symlink_protection() {
    log_test "Symlink Protection (VERSION file)"

    local test_dir
    test_dir=$(mktemp -d)

    # Create a normal VERSION file
    echo "1.0.0" > "$test_dir/VERSION"
    if [[ -f "$test_dir/VERSION" ]] && [[ ! -L "$test_dir/VERSION" ]]; then
        pass "Normal VERSION file passes symlink check"
    else
        fail "Normal VERSION file should pass"
    fi

    # Create a symlink VERSION (may fail on Windows — skip gracefully)
    rm -f "$test_dir/VERSION"
    if ln -s /etc/passwd "$test_dir/VERSION" 2>/dev/null; then
        if [[ -L "$test_dir/VERSION" ]]; then
            pass "Symlink VERSION detected correctly"
        else
            fail "Symlink VERSION should be detected"
        fi
    else
        pass "Symlink VERSION test skipped (symlinks unavailable on this platform)"
    fi

    rm -rf "$test_dir"
}

# =============================================================================
# Test: pg_dump Completeness Detection
# =============================================================================
test_pgdump_completeness() {
    log_test "pg_dump Completeness Detection"

    local test_dir
    test_dir=$(mktemp -d)

    # Complete dump (has marker)
    cat > "$test_dir/complete.sql" << 'EOF'
-- PostgreSQL database dump
CREATE TABLE test (id serial);
INSERT INTO test VALUES (1);
--
-- PostgreSQL database dump complete
--
EOF

    if tail -5 "$test_dir/complete.sql" | grep -q "PostgreSQL database dump complete"; then
        pass "Complete dump detected correctly"
    else
        fail "Complete dump should be detected"
    fi

    # Partial dump (missing marker)
    cat > "$test_dir/partial.sql" << 'EOF'
-- PostgreSQL database dump
CREATE TABLE test (id serial);
INSERT INTO test VALUES (1);
EOF

    if tail -5 "$test_dir/partial.sql" | grep -q "PostgreSQL database dump complete"; then
        fail "Partial dump should NOT pass completeness check"
    else
        pass "Partial dump correctly rejected"
    fi

    # Empty dump
    touch "$test_dir/empty.sql"
    if tail -5 "$test_dir/empty.sql" | grep -q "PostgreSQL database dump complete"; then
        fail "Empty dump should NOT pass completeness check"
    else
        pass "Empty dump correctly rejected"
    fi

    rm -rf "$test_dir"
}

# =============================================================================
# Test: Credential File Separation
# =============================================================================
test_credential_separation() {
    log_test "Credential File Separation (CRED_DIR != TEMP_DIR)"

    # Verify the script uses separate directories
    if grep -q 'CRED_DIR=$(mktemp -d "/tmp/spiralpool-cred-' "$UPGRADE_SCRIPT"; then
        pass "CRED_DIR uses separate temp directory"
    else
        fail "CRED_DIR should use separate temp directory from TEMP_DIR"
    fi

    # Verify CRED_DIR cleanup in cleanup_on_exit
    if grep -q 'CRED_DIR.*rm -rf' "$UPGRADE_SCRIPT"; then
        pass "CRED_DIR cleaned up in cleanup_on_exit"
    else
        fail "CRED_DIR should be cleaned in cleanup_on_exit"
    fi

    # Verify askpass uses SCRIPT_DIR-relative paths (not hardcoded TEMP_DIR)
    if grep -A3 'ASKPASSEOF' "$UPGRADE_SCRIPT" | grep -q 'SCRIPT_DIR'; then
        pass "Askpass script uses SCRIPT_DIR for credential paths"
    else
        fail "Askpass should use SCRIPT_DIR, not hardcoded paths"
    fi
}

# =============================================================================
# Test: Lock File Handling
# =============================================================================
test_lock_file() {
    log_test "Lock File Stale Detection"

    local test_lock
    test_lock=$(mktemp)
    local test_info="${test_lock}.info"

    # Simulate stale lock (PID that doesn't exist)
    echo "operation=upgrade pid=99999999 started=2026-01-01T00:00:00+00:00" > "$test_info"

    # Check if PID is alive
    if kill -0 99999999 2>/dev/null; then
        fail "PID 99999999 should not exist (test environment issue)"
    else
        pass "Stale PID correctly detected as dead"
    fi

    rm -f "$test_lock" "$test_info"
}

# =============================================================================
# Test: Downgrade Prevention in Auto Mode
# =============================================================================
test_downgrade_prevention() {
    log_test "Downgrade Prevention in Auto Mode"

    check_downgrade() {
        local current_clean="$1"
        local target_clean="$2"
        local newest
        newest=$(printf '%s\n' "$current_clean" "$target_clean" | sort -V | tail -1)
        if [[ "$newest" == "$current_clean" ]] && [[ "$current_clean" != "$target_clean" ]]; then
            echo "blocked"
        else
            echo "allowed"
        fi
    }

    assert_eq "$(check_downgrade "2.0.0" "1.0.0")" "blocked" "Downgrade 2.0.0 → 1.0.0 blocked"
    assert_eq "$(check_downgrade "1.1.0" "1.0.0")" "blocked" "Downgrade 1.1.0 → 1.0.0 blocked"
    assert_eq "$(check_downgrade "1.0.0" "1.1.0")" "allowed" "Upgrade 1.0.0 → 1.1.0 allowed"
    assert_eq "$(check_downgrade "1.0.0" "1.0.0")" "allowed" "Same version 1.0.0 allowed"
}

# =============================================================================
# Test: Service Detection
# =============================================================================
test_service_detection() {
    log_test "Service Detection Logic"

    # The script checks for service files to detect service names
    # Test the fallback logic
    local STRATUM_SERVICE=""
    local test_dir
    test_dir=$(mktemp -d)

    # Case 1: spiralstratum exists
    touch "$test_dir/spiralstratum.service"
    if [[ -f "$test_dir/spiralstratum.service" ]]; then
        STRATUM_SERVICE="spiralstratum"
    fi
    assert_eq "$STRATUM_SERVICE" "spiralstratum" "Detects spiralstratum service"

    # Case 2: only old stratum exists
    rm -f "$test_dir/spiralstratum.service"
    touch "$test_dir/stratum.service"
    STRATUM_SERVICE=""
    if [[ -f "$test_dir/spiralstratum.service" ]]; then
        STRATUM_SERVICE="spiralstratum"
    elif [[ -f "$test_dir/stratum.service" ]]; then
        STRATUM_SERVICE="stratum"
    else
        STRATUM_SERVICE="spiralstratum"
    fi
    assert_eq "$STRATUM_SERVICE" "stratum" "Falls back to legacy stratum service"

    rm -rf "$test_dir"
}

# =============================================================================
# Test: Disk Space Check
# =============================================================================
test_disk_space_check() {
    log_test "Disk Space Validation"

    # Simulate the check
    check_disk_space() {
        local avail_mb="$1"
        if [[ -n "$avail_mb" ]] && [[ "$avail_mb" -lt 500 ]]; then
            echo "insufficient"
        else
            echo "sufficient"
        fi
    }

    assert_eq "$(check_disk_space 100)" "insufficient" "100MB → insufficient"
    assert_eq "$(check_disk_space 499)" "insufficient" "499MB → insufficient"
    assert_eq "$(check_disk_space 500)" "sufficient" "500MB → sufficient"
    assert_eq "$(check_disk_space 10000)" "sufficient" "10000MB → sufficient"
    assert_eq "$(check_disk_space "")" "sufficient" "Empty → sufficient (df failed)"
}

# =============================================================================
# Test: SQL Identifier Validation (fix_database_ownership)
# =============================================================================
test_sql_identifier_validation() {
    log_test "SQL Identifier Validation (Injection Prevention)"

    validate_sql_id() {
        local id="$1"
        if [[ "$id" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]]; then
            echo "valid"
        else
            echo "invalid"
        fi
    }

    assert_eq "$(validate_sql_id "spiralstratum")" "valid" "spiralstratum is valid SQL identifier"
    assert_eq "$(validate_sql_id "test_db")" "valid" "test_db is valid"
    assert_eq "$(validate_sql_id "_internal")" "valid" "_internal is valid"

    # Injection attempts
    assert_eq "$(validate_sql_id "db; DROP TABLE--")" "invalid" "SQL injection rejected"
    assert_eq "$(validate_sql_id "db' OR '1'='1")" "invalid" "SQL quote injection rejected"
    assert_eq "$(validate_sql_id "")" "invalid" "Empty identifier rejected"
    assert_eq "$(validate_sql_id "123db")" "invalid" "Leading digit rejected"
}

# =============================================================================
# Test: Git Clone Retry Logic
# =============================================================================
test_git_clone_retry() {
    log_test "Git Clone Retry Logic"

    # Verify retry loop exists in download_new_version
    if grep -q 'for clone_attempt in 1 2 3' "$UPGRADE_SCRIPT"; then
        pass "Git clone has 3-attempt retry loop"
    else
        fail "Git clone should retry up to 3 times"
    fi

    # Verify failed clone cleans up TEMP_DIR before retry
    if grep -A5 'clone_attempt.*failed' "$UPGRADE_SCRIPT" | grep -q 'rm -rf.*TEMP_DIR'; then
        pass "Failed clone cleans up TEMP_DIR before retry"
    else
        fail "Failed clone should clean TEMP_DIR before retry"
    fi

    # Verify sleep between retries
    if grep -B2 -A4 'clone_attempt -lt 3' "$UPGRADE_SCRIPT" | grep -q 'sleep 5'; then
        pass "Clone retries have 5s backoff"
    else
        fail "Clone retries should have sleep between attempts"
    fi
}

# =============================================================================
# Test: METRICS_TOKEN Sed Sanitization
# =============================================================================
test_metrics_token_sanitization() {
    log_test "METRICS_TOKEN Sed Sanitization"

    # Verify pipe delimiter is stripped (breaks sed with | delimiter)
    if grep -q 'METRICS_TOKEN="${METRICS_TOKEN//|/}"' "$UPGRADE_SCRIPT"; then
        pass "METRICS_TOKEN strips pipe character"
    else
        fail "METRICS_TOKEN should strip | for sed safety"
    fi

    # Verify ampersand is stripped (inserts matched text in sed)
    if grep -q 'METRICS_TOKEN="${METRICS_TOKEN//&/}"' "$UPGRADE_SCRIPT"; then
        pass "METRICS_TOKEN strips ampersand"
    else
        fail "METRICS_TOKEN should strip & for sed safety"
    fi

    # Verify backslash is stripped
    if grep -Fq 'METRICS_TOKEN="${METRICS_TOKEN//\\/}"' "$UPGRADE_SCRIPT"; then
        pass "METRICS_TOKEN strips backslash"
    else
        fail "METRICS_TOKEN should strip \\ for sed safety"
    fi

    # Functional test: simulate sanitization
    sanitize_for_sed() {
        local val="$1"
        val="${val//|/}"
        val="${val//&/}"
        val="${val//\\/}"
        echo "$val"
    }

    assert_eq "$(sanitize_for_sed "abc123")" "abc123" "Clean token unchanged"
    assert_eq "$(sanitize_for_sed "abc|def")" "abcdef" "Pipe stripped from token"
    assert_eq "$(sanitize_for_sed "abc&def")" "abcdef" "Ampersand stripped from token"
    assert_eq "$(sanitize_for_sed 'abc\def')" "abcdef" "Backslash stripped from token"
    assert_eq "$(sanitize_for_sed 'a|b&c\d')" "abcd" "Multiple special chars stripped"
}

# =============================================================================
# Test: WANTS_DEPS Sed Sanitization
# =============================================================================
test_wants_deps_sanitization() {
    log_test "WANTS_DEPS Sed Sanitization"

    if grep -q 'WANTS_DEPS="${WANTS_DEPS//|/}"' "$UPGRADE_SCRIPT"; then
        pass "WANTS_DEPS strips pipe character"
    else
        fail "WANTS_DEPS should strip | for sed safety"
    fi

    if grep -q 'WANTS_DEPS="${WANTS_DEPS//&/}"' "$UPGRADE_SCRIPT"; then
        pass "WANTS_DEPS strips ampersand"
    else
        fail "WANTS_DEPS should strip & for sed safety"
    fi
}

# =============================================================================
# Test: Recovered Token Heredoc Safety
# =============================================================================
test_recovered_token_heredoc_safety() {
    log_test "Recovered Token/Key Heredoc Safety"

    # Test: recovered_metrics_token sanitizes $ (prevents shell expansion)
    # File contains literal: recovered_metrics_token="${recovered_metrics_token//\$/}"
    # In single quotes with -F: \$ = literal backslash + dollar (matches file content)
    if grep -A8 'Sanitize token for YAML safety' "$UPGRADE_SCRIPT" | grep -Fq 'recovered_metrics_token="${recovered_metrics_token//\$/}"'; then
        pass "recovered_metrics_token strips \$ for heredoc safety"
    else
        fail "recovered_metrics_token should strip \$ to prevent shell expansion"
    fi

    # Test: recovered_metrics_token sanitizes backtick
    if grep -A8 'Sanitize token for YAML safety' "$UPGRADE_SCRIPT" | grep -Fq 'recovered_metrics_token="${recovered_metrics_token//\`/}"'; then
        pass "recovered_metrics_token strips backtick for heredoc safety"
    else
        fail "recovered_metrics_token should strip backtick to prevent shell expansion"
    fi

    # Test: recovered_api_key sanitizes $ (prevents shell expansion)
    if grep -A8 'Sanitize key for YAML safety' "$UPGRADE_SCRIPT" | grep -Fq 'recovered_api_key="${recovered_api_key//\$/}"'; then
        pass "recovered_api_key strips \$ for heredoc safety"
    else
        fail "recovered_api_key should strip \$ to prevent shell expansion"
    fi

    # Test: recovered_api_key sanitizes backtick
    if grep -A8 'Sanitize key for YAML safety' "$UPGRADE_SCRIPT" | grep -Fq 'recovered_api_key="${recovered_api_key//\`/}"'; then
        pass "recovered_api_key strips backtick for heredoc safety"
    else
        fail "recovered_api_key should strip backtick to prevent shell expansion"
    fi

    # Functional test: simulate full sanitization
    sanitize_for_heredoc() {
        local val="$1"
        val="${val//[$'\n\r']/}"
        val="${val//\"/}"
        val="${val//\$/}"
        val="${val//\`/}"
        echo "$val"
    }

    assert_eq "$(sanitize_for_heredoc "abc123def")" "abc123def" "Clean key unchanged"
    assert_eq "$(sanitize_for_heredoc 'abc$HOME')" "abcHOME" "Dollar sign stripped"
    assert_eq "$(sanitize_for_heredoc 'abc`whoami`')" "abcwhoami" "Backticks stripped"
    assert_eq "$(sanitize_for_heredoc 'abc"def"ghi')" "abcdefghi" "Quotes stripped"
    assert_eq "$(sanitize_for_heredoc $'abc\ndef')" "abcdef" "Newlines stripped"
    assert_eq "$(sanitize_for_heredoc 'key$`"test')" "keytest" "All shell metacharacters stripped"
}

# =============================================================================
# Test: Dashboard Template Atomic Swap (Pass 7)
# =============================================================================
test_dashboard_template_swap() {
    log_test "Dashboard Template Atomic Swap"

    # Verify templates use copy-to-temp-then-swap pattern (not delete-then-copy)
    if grep -q 'templates.new' "$UPGRADE_SCRIPT"; then
        pass "Dashboard templates use .new temp directory"
    else
        fail "Dashboard templates should use atomic swap via .new temp dir"
    fi

    # Verify old templates are only deleted AFTER new ones are staged
    local swap_pattern
    swap_pattern=$(grep -n 'templates.new\|rm -rf.*templates"' "$UPGRADE_SCRIPT" | head -3)
    local cp_line rm_line mv_line
    cp_line=$(echo "$swap_pattern" | grep 'cp -r.*templates.new' | head -1 | cut -d: -f1)
    rm_line=$(echo "$swap_pattern" | grep 'rm -rf.*templates"' | head -1 | cut -d: -f1)
    mv_line=$(echo "$swap_pattern" | grep 'mv.*templates.new' | head -1 | cut -d: -f1)

    if [[ -n "$cp_line" ]] && [[ -n "$rm_line" ]] && [[ "$cp_line" -lt "$rm_line" ]]; then
        pass "Copy happens before delete (atomic swap order)"
    else
        fail "Copy should happen before delete for atomic swap"
    fi
}

# =============================================================================
# Test: Stratum Binary Atomic Install (Pass 7)
# =============================================================================
test_stratum_binary_atomic_install() {
    log_test "Stratum Binary Atomic Install"

    # Verify binary uses same-directory temp for cross-filesystem safety
    if grep -q 'STRATUM_BINARY.*\.new' "$UPGRADE_SCRIPT"; then
        pass "Stratum binary uses .new temp for atomic install"
    else
        fail "Stratum binary should use same-directory .new temp file"
    fi

    # Verify chmod/chown on .new BEFORE final mv
    local chown_new mv_final
    chown_new=$(grep -n 'chown.*STRATUM_BINARY.*\.new' "$UPGRADE_SCRIPT" | head -1 | cut -d: -f1)
    mv_final=$(grep -n 'mv.*STRATUM_BINARY.*\.new.*STRATUM_BINARY' "$UPGRADE_SCRIPT" | head -1 | cut -d: -f1)

    if [[ -n "$chown_new" ]] && [[ -n "$mv_final" ]] && [[ "$chown_new" -lt "$mv_final" ]]; then
        pass "Permissions set on .new before final rename"
    else
        fail "Permissions should be set on .new before atomic rename"
    fi
}

# =============================================================================
# Test: Backup Name Path Traversal (Pass 7)
# =============================================================================
test_backup_name_path_traversal() {
    log_test "Backup Name Path Traversal Prevention"

    # Verify the script validates backup names
    if grep -q 'backup_name.*\.\.\*' "$UPGRADE_SCRIPT"; then
        pass "Backup name rejects .. sequences"
    else
        fail "Backup name should reject path traversal (..)"
    fi

    if grep -q 'backup_name.*\/\*' "$UPGRADE_SCRIPT"; then
        pass "Backup name rejects / characters"
    else
        fail "Backup name should reject path separators (/)"
    fi

    # Functional test: simulate validation
    validate_backup_name() {
        local name="$1"
        if [[ "$name" == *..* ]] || [[ "$name" == */* ]]; then
            echo "rejected"
        else
            echo "accepted"
        fi
    }

    assert_eq "$(validate_backup_name "pre-upgrade-1.0.0-to-2.0.0-20260228")" "accepted" "Normal backup name accepted"
    assert_eq "$(validate_backup_name "../../etc/passwd")" "rejected" "Path traversal with .. rejected"
    assert_eq "$(validate_backup_name "foo/bar")" "rejected" "Path with / rejected"
    assert_eq "$(validate_backup_name "..hidden")" "rejected" "Name starting with .. rejected"
    assert_eq "$(validate_backup_name "name..with..dots")" "rejected" "Name with consecutive dots rejected"
    assert_eq "$(validate_backup_name "normal-name-1.2.3")" "accepted" "Name with single dots accepted"
}

# =============================================================================
# Test: TEMP_DIR Cleanup Nulling (Pass 7)
# =============================================================================
test_temp_dir_nulling() {
    log_test "TEMP_DIR Nulled After Cleanup"

    # Verify TEMP_DIR is set to "" after rm -rf in main()
    if grep -A1 'rm -rf "$TEMP_DIR"' "$UPGRADE_SCRIPT" | grep -q 'TEMP_DIR=""'; then
        pass "TEMP_DIR is nulled after explicit cleanup"
    else
        fail "TEMP_DIR should be set to empty after cleanup to prevent double-attempt"
    fi
}

# =============================================================================
# Test: Password Credential Clearing (Pass 7)
# =============================================================================
test_password_credential_clearing() {
    log_test "Password Credential Clearing on Mismatch"

    # Verify unset is called on empty password branch
    if grep -B2 'Password cannot be empty' "$UPGRADE_SCRIPT" | grep -q 'unset pool_pass pool_pass_confirm'; then
        pass "Credentials cleared on empty password"
    else
        fail "Credentials should be cleared immediately on empty password"
    fi

    # Verify unset is called on mismatch branch
    if grep -B2 'Passwords do not match' "$UPGRADE_SCRIPT" | grep -q 'unset pool_pass pool_pass_confirm'; then
        pass "Credentials cleared on password mismatch"
    else
        fail "Credentials should be cleared immediately on password mismatch"
    fi
}

# =============================================================================
# Test: Exit Code Capture Order (P8-H1)
# =============================================================================
test_exit_code_capture_order() {
    log_test "Exit Code Capture Order in cleanup_on_exit"

    # Verify that 'local exit_code=$?' is the FIRST statement in cleanup_on_exit,
    # BEFORE the re-entrancy guard [[ ]] test which clobbers $? to 1.
    # Bug: if [[ ]] runs first, exit_code is always 1 (script reports failure on success)
    local func_body
    func_body=$(grep -A5 '^cleanup_on_exit()' "$UPGRADE_SCRIPT")

    # exit_code=$? must appear BEFORE _CLEANUP_ALREADY_RAN check
    local exit_code_line
    local reentrancy_line
    exit_code_line=$(grep -n 'local exit_code=\$?' "$UPGRADE_SCRIPT" | head -1 | cut -d: -f1)
    reentrancy_line=$(grep -n '_CLEANUP_ALREADY_RAN.*true.*return' "$UPGRADE_SCRIPT" | head -1 | cut -d: -f1)

    if [[ -n "$exit_code_line" ]] && [[ -n "$reentrancy_line" ]] && [[ "$exit_code_line" -lt "$reentrancy_line" ]]; then
        pass "exit_code captured before re-entrancy guard"
    else
        fail "exit_code must be captured BEFORE [[ ]] re-entrancy guard (line $exit_code_line vs $reentrancy_line)"
    fi
}

# =============================================================================
# Test: spiralctl Binary Atomic Install (P8-M1)
# =============================================================================
test_spiralctl_atomic_install() {
    log_test "spiralctl Binary Atomic Install"

    # Verify spiralctl uses same atomic pattern as stratum binary:
    # cp to .new, chmod, chown, mv .new to final
    if grep -q 'spiralctl\.new' "$UPGRADE_SCRIPT"; then
        pass "spiralctl uses .new temp for atomic install"
    else
        fail "spiralctl should use .new temp file for atomic cross-filesystem install"
    fi

    # Verify permissions are set on .new BEFORE final rename
    local spiralctl_section
    spiralctl_section=$(grep -A6 'spiralctl-build.*spiralctl.new\|SPIRALCTL_OUTPUT.*spiralctl.new' "$UPGRADE_SCRIPT" 2>/dev/null || echo "")
    if echo "$spiralctl_section" | grep -q 'chmod.*spiralctl.new'; then
        pass "Permissions set on spiralctl.new before rename"
    else
        fail "chmod should target spiralctl.new before mv"
    fi
}

# =============================================================================
# Test: Password Prompt Auto-Mode Guard (P10-L1)
# =============================================================================
test_password_prompt_auto_mode_guard() {
    log_test "Password Prompt Auto-Mode Guard"

    # The password prompt (pass_status == "L" || "NP") must be guarded by:
    # 1. AUTO_MODE != true  — don't prompt in unattended upgrades
    # 2. -t 0               — don't prompt when stdin is not a terminal (piped input)
    # Without these guards, --auto mode gets 3 spurious "Password cannot be empty" warnings
    # because read gets EOF immediately on non-terminal stdin.

    local password_condition
    password_condition=$(grep 'pass_status.*==.*"L".*pass_status.*==.*"NP"' "$UPGRADE_SCRIPT")

    if echo "$password_condition" | grep -q 'AUTO_MODE.*!=.*true'; then
        pass "Password prompt guarded by AUTO_MODE check"
    else
        fail "Password prompt must check AUTO_MODE != true to avoid prompting in --auto mode"
    fi

    if echo "$password_condition" | grep -q '\-t 0'; then
        pass "Password prompt guarded by terminal check (-t 0)"
    else
        fail "Password prompt must check -t 0 to avoid prompting on non-terminal stdin"
    fi

    # Verify ${BOLD} is NOT used in the prompt (it was never defined in the color section)
    if grep 'No password set for.*POOL_USER' "$UPGRADE_SCRIPT" | grep -q 'BOLD'; then
        fail "Password prompt should not use undefined \${BOLD} variable"
    else
        pass "Password prompt does not use undefined \${BOLD}"
    fi
}

# =============================================================================
# Test: Stratum Build-Before-Stop Ordering (P10-L2, updated for build/deploy split)
# =============================================================================
test_stratum_build_deploy_split() {
    log_test "Stratum Build-Before-Stop Ordering"

    # upgrade.sh splits stratum update into build_stratum() (before stop) and
    # deploy_stratum() (after stop) to minimize miner downtime. Verify the
    # functions exist and are called in the correct order in main().

    # Verify build_stratum() function exists
    if grep -q '^build_stratum()' "$UPGRADE_SCRIPT"; then
        pass "build_stratum() function exists"
    else
        fail "build_stratum() function missing from upgrade.sh"
    fi

    # Verify deploy_stratum() function exists
    if grep -q '^deploy_stratum()' "$UPGRADE_SCRIPT"; then
        pass "deploy_stratum() function exists"
    else
        fail "deploy_stratum() function missing from upgrade.sh"
    fi

    # Verify build_stratum is called BEFORE stop_services in main()
    # Extract main() body and check ordering: build_stratum must appear before stop_services
    local main_body
    main_body=$(sed -n '/^main()/,/^}/p' "$UPGRADE_SCRIPT")

    # Match CALL SITES, not prose. A bare grep for the name also matches the
    # comment above the call ("Must run BEFORE stop_services"), which sits
    # earlier in the body — so stop_services resolved to the comment's line and
    # this ordering check failed against correct code.
    #
    # Strip comments rather than anchoring to line-start: calls are not always
    # bare statements (deploy_stratum is invoked as "$UPDATE_STRATUM && deploy_stratum").
    # Blanking the comment text preserves line numbering, so the reported
    # positions still refer to real lines in main().
    local main_code
    main_code=$(echo "$main_body" | sed 's/#.*//')

    local build_line stop_line deploy_line
    build_line=$(echo "$main_code" | grep -n 'build_stratum' | head -1 | cut -d: -f1)
    stop_line=$(echo "$main_code" | grep -n 'stop_services' | head -1 | cut -d: -f1)
    deploy_line=$(echo "$main_code" | grep -n 'deploy_stratum' | head -1 | cut -d: -f1)

    if [[ -n "$build_line" ]] && [[ -n "$stop_line" ]] && [[ "$build_line" -lt "$stop_line" ]]; then
        pass "build_stratum called before stop_services (build L${build_line}, stop L${stop_line})"
    else
        fail "build_stratum must be called BEFORE stop_services — build at L${build_line:-missing}, stop at L${stop_line:-missing}"
    fi

    # Verify deploy_stratum is called AFTER stop_services in main()
    if [[ -n "$deploy_line" ]] && [[ -n "$stop_line" ]] && [[ "$deploy_line" -gt "$stop_line" ]]; then
        pass "deploy_stratum called after stop_services (stop L${stop_line}, deploy L${deploy_line})"
    else
        fail "deploy_stratum must be called AFTER stop_services — stop at L${stop_line:-missing}, deploy at L${deploy_line:-missing}"
    fi

    # Verify old update_stratum() function does NOT exist (was split into build+deploy)
    if grep -q '^update_stratum()' "$UPGRADE_SCRIPT"; then
        fail "Stale update_stratum() function still exists — should have been split into build_stratum + deploy_stratum"
    else
        pass "No stale update_stratum() function"
    fi
}

# =============================================================================
# Test: Stratum V2 Opt-In Migration
# =============================================================================
# v3.0 makes Stratum V2 opt-in. An unattended upgrade must comment out port_v2,
# close the V2 firewall ports, record ENABLE_V2_STRATUM=false, leave V1/TLS alone,
# and run only once so an operator who re-enables V2 keeps it.

test_stratum_v2_opt_in_migration() {
    log_test "Stratum V2 Opt-In Migration"

    local fn
    fn=$(sed -n '/^migrate_stratum_v2_opt_in() {/,/^}/p' "$UPGRADE_SCRIPT" | tr -d '\r')
    if [[ -z "$fn" ]]; then
        fail "migrate_stratum_v2_opt_in() not found in upgrade.sh"
        return
    fi
    if grep -q '^[[:space:]]*migrate_stratum_v2_opt_in[[:space:]]*$' "$UPGRADE_SCRIPT"; then
        pass "migrate_stratum_v2_opt_in is called from main"
    else
        fail "migrate_stratum_v2_opt_in is never called"
    fi

    local tmp
    tmp=$(mktemp -d)
    mkdir -p "$tmp/config"
    cat > "$tmp/config/config.yaml" << 'EOF'
coins:
  - symbol: "DGB"
    stratum:
      port: 3333
      port_v2: 3334
      port_tls: 3335
  - symbol: "LTC"
    stratum:
      port: 7333
      port_v2: 7334
      port_tls: 7335
EOF
    printf 'STRATUM_PORT=\nSTRATUM_V2_PORT=\nENABLE_V2_STRATUM=true\n' > "$tmp/config/coins.env"

    run_v2_migration() {
        (
            INSTALL_DIR="$tmp"
            AUTO_MODE="true"
            # Not "$tmp": the migration declares its own local tmp, which would
            # shadow ours inside the ufw mock.
            V2_TEST_UFW_LOG="$tmp/ufw.log"
            ufw() { echo "$*" >> "$V2_TEST_UFW_LOG"; }
            log_success() { :; }
            log_warn() { :; }
            log_info() { :; }
            eval "$fn"
            migrate_stratum_v2_opt_in
        )
    }
    run_v2_migration

    local config_text
    config_text=$(cat "$tmp/config/config.yaml")
    assert_eq "$(grep -cE '^[[:space:]]*port_v2:' "$tmp/config/config.yaml" || true)" "0" "No active port_v2 lines remain"
    assert_contains "$config_text" "# port_v2: 3334" "port_v2 commented out, not deleted"
    assert_contains "$config_text" "port: 3333" "Stratum V1 port untouched"
    assert_contains "$config_text" "port_tls: 7335" "Stratum TLS port untouched"
    assert_contains "$(cat "$tmp/ufw.log" 2>/dev/null)" "delete allow 3334/tcp" "DGB V2 firewall port closed"
    assert_contains "$(cat "$tmp/ufw.log" 2>/dev/null)" "delete allow 7334/tcp" "LTC V2 firewall port closed"
    assert_contains "$(cat "$tmp/config/coins.env")" "ENABLE_V2_STRATUM=false" "Opt-out recorded in coins.env"
    if [[ -f "$tmp/config/config.yaml.bak.stratumv2" ]]; then
        pass "Config backed up before rewrite"
    else
        fail "No config backup written"
    fi
    if [[ -f "$tmp/config/.migrated-stratum-v2-opt-in" ]]; then
        pass "Migration marker written"
    else
        fail "Migration marker missing"
    fi

    # Operator re-enables V2 afterwards: the next upgrade must leave it alone.
    sed -i 's/# port_v2: 3334/port_v2: 3334/' "$tmp/config/config.yaml"
    rm -f "$tmp/ufw.log"
    run_v2_migration
    assert_eq "$(grep -cE '^[[:space:]]*port_v2: 3334' "$tmp/config/config.yaml" || true)" "1" "Re-enabled V2 survives the next upgrade"
    if [[ ! -f "$tmp/ufw.log" ]]; then
        pass "No firewall changes on re-run"
    else
        fail "Firewall changed on re-run"
    fi

    rm -rf "$tmp"
}

# =============================================================================
# Test: coin-upgrade.sh handles the LTC / FBTC / BC2 / XEC daemon targets
# =============================================================================
# Runs the real coin-upgrade.sh functions, extracted from the script, against
# the version banners the new daemons print, tarballs laid out like the BitcoinII
# v31.1.0 and Bitcoin Silver 31.1.3 releases, and a mocked bitcoinii-cli.

test_coin_upgrade_daemon_targets() {
    log_test "coin-upgrade.sh Daemon Targets (LTC/FBTC/BC2/BTCS/XEC)"

    local cu="$PROJECT_ROOT/coin-upgrade.sh"
    local defs arr
    defs=$( {
        for arr in COIN_TARGET COIN_SHA256 COIN_DAEMON_CMD COIN_CLI_CMD; do
            sed -n "/^declare -A ${arr}=(/,/^)/p" "$cu"
        done
        # -a: coin-upgrade.sh contains a NUL byte, so grep would call it binary
        grep -aE '^BC2_FORK_(HEIGHT|BLOCK_57750)=' "$cu"
        for f in _norm4 _ver_matches _verify_sha256 download_BC2 download_BTCS install_binaries _btc_disk_wallet_scan verify_bc2_fork_chain; do
            sed -n "/^${f}() {/,/^}/p" "$cu"
        done
    } | tr -d '\r\000')

    local tmp
    tmp=$(mktemp -d)

    # 1. The version each new daemon prints matches its target; the old one does not.
    local ver_out
    ver_out=$(
        eval "$defs"
        # Same extraction as get_installed_version and the post-install check
        parse() { echo "$1" | grep -oP '(?i)version\s+v?\K[\d]+\.[\d]+[\w.]*' | head -1; }
        check() {
            local v; v=$(parse "$2")
            if _ver_matches "$v" "${COIN_TARGET[$1]}"; then echo "new-ok $1"; else echo "new-bad $1 $v"; fi
            if _ver_matches "$3" "${COIN_TARGET[$1]}"; then echo "old-bad $1"; else echo "old-ok $1"; fi
        }
        check LTC  "Litecoin Core version v0.21.5.8"          0.21.5.6
        check FBTC "Bitcoin Core daemon version v0.4.0"       0.3.0
        check BC2  "BitcoinII daemon version v31.1.0"         29.1.0
        check BTCS "BitcoinSilver daemon version v31.1.3"     1.0.2
        check XEC  "Bitcoin ABC version v0.33.12-908dfae7c725" 0.33.10
        check SYS  "Syscoin Core version v5.1.2"              5.1.0
    )
    local c
    for c in LTC FBTC BC2 BTCS XEC SYS; do
        assert_contains "$ver_out" "new-ok $c" "$c: new daemon's --version matches COIN_TARGET"
        assert_contains "$ver_out" "old-ok $c" "$c: old daemon is still offered the upgrade"
    done

    # 2. BC2: new asset name, and bitcoinII-d installed under the bitcoinIId name.
    mkdir -p "$tmp/src/BitcoinII-v31.1-Linux-CLI" "$tmp/work" "$tmp/bin" "$tmp/fakebin"
    printf '#!/bin/sh\necho daemon\n' > "$tmp/src/BitcoinII-v31.1-Linux-CLI/bitcoinII-d"
    printf '#!/bin/sh\necho cli\n' > "$tmp/src/BitcoinII-v31.1-Linux-CLI/bitcoinII-cli"
    (cd "$tmp/src" && tar -czf "$tmp/bc2.tar.gz" BitcoinII-v31.1-Linux-CLI)
    # find -exec runs sudo as a program, so stub it on PATH. Drop install's
    # -o/-g owner flags: the harness is not root.
    cat > "$tmp/fakebin/sudo" << 'EOF'
#!/bin/bash
args=(); skip=0
for a in "$@"; do
    if (( skip )); then skip=0; continue; fi
    case "$a" in -o|-g) skip=1 ;; *) args+=("$a") ;; esac
done
exec "${args[@]}"
EOF
    chmod +x "$tmp/fakebin/sudo"
    (
        eval "$defs"
        PATH="$tmp/fakebin:$PATH"
        WORK_DIR="$tmp/work"
        POOL_USER="nobody"
        # The pin belongs to the real release asset; this harness builds its own
        # tarball, so pin that. The mismatch path is asserted separately below.
        COIN_SHA256[BC2]=$(sha256sum "$tmp/bc2.tar.gz" | awk '{print $1}')
        _wget() { echo "$3" > "$tmp/url"; cp "$tmp/bc2.tar.gz" "$2"; }
        get_binary_dir() { echo "$tmp/bin"; }
        log_info() { :; }; log_success() { :; }; log_error() { :; }
        die() { echo "die: $*"; exit 1; }
        extracted=$(download_BC2 x86_64)
        install_binaries BC2 "$extracted"
    ) > "$tmp/install.out" 2>&1 || true
    assert_eq "$(cat "$tmp/url" 2>/dev/null)" \
        "https://github.com/Bitcoin-II/BitcoinII-Core/releases/download/v31.1.0/BitcoinII-v31.1-Linux-CLI.tar.gz" \
        "BC2: download_BC2 fetches the v31.1.0 asset name"
    if cmp -s "$tmp/bin/bitcoinIId" "$tmp/src/BitcoinII-v31.1-Linux-CLI/bitcoinII-d"; then
        pass "BC2: bitcoinII-d installed as bitcoinIId"
    else
        fail "BC2: bitcoinIId missing after install" "$(cat "$tmp/install.out")"
    fi
    if [[ -f "$tmp/bin/bitcoinII-cli" ]]; then
        pass "BC2: bitcoinII-cli installed"
    else
        fail "BC2: bitcoinII-cli missing after install"
    fi

    # 2a. A download whose SHA256 is not the pin is refused. FBTC, BC2 and BTCS
    # publish no signed checksum file, so this pin is the only thing standing
    # between a swapped release asset and a consensus binary on disk.
    mkdir -p "$tmp/bin2"
    if (
        eval "$defs"
        PATH="$tmp/fakebin:$PATH"
        WORK_DIR="$tmp/work"
        POOL_USER="nobody"
        COIN_SHA256[BC2]="0000000000000000000000000000000000000000000000000000000000000000"
        _wget() { cp "$tmp/bc2.tar.gz" "$2"; }
        get_binary_dir() { echo "$tmp/bin2"; }
        log_info() { :; }; log_success() { :; }; log_error() { :; }
        download_BC2 x86_64
    ) > /dev/null 2>&1; then
        fail "BC2: an archive whose checksum is not the pin was accepted"
    else
        pass "BC2: an archive whose checksum is not the pin is refused"
    fi

    # 2b. BTCS: release binary asset name, and the archive's standard bin/ layout.
    local btcs_dir="bitcoinsilver-31.1.3-x86_64-linux-gnu"
    mkdir -p "$tmp/src/$btcs_dir/bin" "$tmp/btcsbin"
    printf '#!/bin/sh\necho daemon\n' > "$tmp/src/$btcs_dir/bin/bitcoinsilverd"
    printf '#!/bin/sh\necho cli\n' > "$tmp/src/$btcs_dir/bin/bitcoinsilver-cli"
    (cd "$tmp/src" && tar -czf "$tmp/btcs.tar.gz" "$btcs_dir")
    (
        eval "$defs"
        PATH="$tmp/fakebin:$PATH"
        WORK_DIR="$tmp/work"
        POOL_USER="nobody"
        COIN_SHA256[BTCS]=$(sha256sum "$tmp/btcs.tar.gz" | awk '{print $1}')
        _wget() { echo "$3" > "$tmp/btcs.url"; cp "$tmp/btcs.tar.gz" "$2"; }
        get_binary_dir() { echo "$tmp/btcsbin"; }
        log_info() { :; }; log_success() { :; }; log_error() { :; }
        die() { echo "die: $*"; exit 1; }
        extracted=$(download_BTCS x86_64)
        install_binaries BTCS "$extracted"
    ) > "$tmp/btcs.out" 2>&1 || true
    assert_eq "$(cat "$tmp/btcs.url" 2>/dev/null)" \
        "https://github.com/bitcoin-silver/core/releases/download/version31.1.3/${btcs_dir}.tar.gz" \
        "BTCS: download_BTCS fetches the 31.1.3 release asset"
    if [[ -f "$tmp/btcsbin/bitcoinsilverd" && -f "$tmp/btcsbin/bitcoinsilver-cli" ]]; then
        pass "BTCS: release binaries installed from the archive's bin/"
    else
        fail "BTCS: binaries missing after install" "$(cat "$tmp/btcs.out")"
    fi

    # 3. BC2/BTCS: a legacy (BDB) wallet on disk blocks the upgrade; descriptor
    # wallets do not. Both are built on Bitcoin Core 31.1, which skips legacy
    # wallets at load, so both must be scanned before the binary is swapped.
    mkdir -p "$tmp/walletdata/wallets/pool-bc2"
    printf 'SQLite format 3\000rest' > "$tmp/walletdata/wallets/pool-bc2/wallet.dat"
    run_wallet_scan() {
        local coin="${1:-BC2}"
        (
            eval "$defs"
            get_data_dir() { echo "$tmp/walletdata"; }
            get_coin_cli() { echo "${coin,,}-cli"; }
            log_warn() { :; }; log_success() { :; }; log_error() { :; }
            _btc_disk_wallet_scan "$coin"
        ) > /dev/null 2>&1
    }
    if run_wallet_scan BC2; then
        pass "BC2: descriptor wallet passes the legacy-wallet scan"
    else
        fail "BC2: descriptor wallet wrongly blocked the upgrade"
    fi
    mkdir -p "$tmp/walletdata/wallets/imported"
    printf '\000\000\000\000\000\000\000\000\000\000\000\000\142\061\005\000' > "$tmp/walletdata/wallets/imported/wallet.dat"
    if run_wallet_scan BC2; then
        fail "BC2: legacy wallet did not block the upgrade"
    else
        pass "BC2: legacy wallet blocks the upgrade"
    fi
    if run_wallet_scan BTCS; then
        fail "BTCS: legacy wallet did not block the upgrade"
    else
        pass "BTCS: legacy wallet blocks the upgrade"
    fi
    if grep -aq 'coin" == "BC2" || "$coin" == "BTCS"' "$cu"; then
        pass "BC2 and BTCS both run the legacy-wallet scan before upgrading"
    else
        fail "the legacy-wallet scan is not run for both BC2 and BTCS"
    fi

    # 4. BC2 fork check: verified node untouched, stuck node repaired, wrong chain fails.
    run_bc2_verify() {
        (
            eval "$defs"
            SCEN="$1"
            STATE="$tmp/verify.$1"
            : > "$STATE"
            get_coin_cli() { echo "bitcoinii-cli"; }
            sleep() { :; }
            log_warn() { :; }; log_success() { :; }; log_info() { :; }; log_error() { :; }
            bitcoinii-cli() {
                case "$1" in
                    getblockcount) echo 57749 ;;
                    getblockhash)
                        case "$SCEN" in
                            ok)    echo "$BC2_FORK_BLOCK_57750" ;;
                            stuck) grep -q reconsider "$STATE" && echo "$BC2_FORK_BLOCK_57750" || return 8 ;;
                            wrong) echo "00000000000000000000000000000000000000000000000000000000deadbeef" ;;
                        esac ;;
                    getchaintips)
                        printf '[\n  {\n    "height": 57750,\n    "hash": "%s",\n    "branchlen": 1,\n    "status": "invalid"\n  }\n]\n' "$BC2_FORK_BLOCK_57750" ;;
                    reconsiderblock) echo "reconsider $2" >> "$STATE" ;;
                esac
            }
            verify_bc2_fork_chain
        ) > /dev/null 2>&1
    }
    local fork_hash="0000000000000000283f16daab1be22eb25a53bac57cf92af1a484163325e9e1"
    if run_bc2_verify ok; then
        pass "BC2: node on the fork block verifies"
    else
        fail "BC2: node on the fork block failed verification"
    fi
    assert_eq "$(cat "$tmp/verify.ok")" "" "BC2: verified node gets no reconsiderblock"
    if run_bc2_verify stuck; then
        pass "BC2: node stuck below the fork is repaired"
    else
        fail "BC2: node stuck below the fork was not repaired"
    fi
    assert_contains "$(cat "$tmp/verify.stuck")" "reconsider $fork_hash" "BC2: rejected fork block is reconsidered"
    if run_bc2_verify wrong; then
        fail "BC2: node on a pre-fork chain passed verification"
    else
        pass "BC2: node on a pre-fork chain fails verification"
    fi

    rm -rf "$tmp"
}

# =============================================================================
# Test: update_dashboard copies every static asset directory the dashboard uses
# =============================================================================
# The block explorer's script lives in static/js. update_dashboard copied only
# static/css and static/templates, so an upgraded install served the explorer
# page with its script missing.

test_update_dashboard_copies_static_assets() {
    log_test "update_dashboard Static Assets"

    local fn
    fn=$(sed -n '/^update_dashboard() {/,/^}/p' "$UPGRADE_SCRIPT" | tr -d '\r')
    if [[ -z "$fn" ]]; then
        fail "update_dashboard() not found in upgrade.sh"
        return
    fi

    # Everything install.sh lays down under static/ except themes, which are
    # merged separately so user themes survive.
    local dir
    for dir in css js templates icons manifest.json service-worker.js; do
        if grep -qF "\$DASHBOARD_SOURCE/static/$dir\"" <<< "$fn"; then
            pass "update_dashboard copies static/$dir"
        else
            fail "update_dashboard does not copy static/$dir"
        fi
    done
}

# =============================================================================
# Test: the wallet-recovery descriptor list covers the Bitcoin Core 31 coins
# =============================================================================
# With the daemon unreachable, wallet recovery tries SQLite recovery only for the
# coins in descriptor_coins, then falls back to -salvagewallet. Bitcoin Core 30
# removed BDB legacy wallets ("can no longer be created or loaded") and that
# option with them, so for a coin on that base neither path runs. BC2 31.1 was
# missing from the list and its daemon no longer accepts -salvagewallet, which
# left a downed node with no automated recovery at all. BTCS 31.1 shares the base.

# =============================================================================
# Test: upgrade refreshes wait-for-node.sh and health-monitor.sh
# =============================================================================
test_bin_scripts_refreshed() {
    log_test "update_utility_scripts refreshes wait-for-node.sh and health-monitor.sh"

    local block tmp
    block=$(awk '/# wait-for-node.sh and health-monitor.sh live in \$INSTALL_DIR\/bin/{on=1} /if \[\[ \$updated -gt 0 \]\]; then/{on=0} on' "$UPGRADE_SCRIPT")
    if [[ -z "$block" ]]; then
        fail "refresh block found in upgrade.sh"
        return
    fi
    tmp=$(mktemp -d)
    mkdir -p "$tmp/spiralpool/bin" "$tmp/root/scripts/linux"
    cp "$PROJECT_ROOT/install.sh" "$tmp/root/install.sh"
    cp "$PROJECT_ROOT/scripts/linux/wait-for-node.sh" "$tmp/root/scripts/linux/"

    # The heredoc as install.sh would write it, found independently of upgrade.sh's sed
    awk "/health-monitor.sh\" > \\/dev\\/null << 'HEALTHEOF'/{on=1; next} /^HEALTHEOF\$/{on=0} on" \
        "$PROJECT_ROOT/install.sh" > "$tmp/expected-hm.sh"

    run_refresh() {
        bash -c "log_info() { echo \"\$*\"; }; log_warn() { echo \"WARN \$*\"; }
systemctl() { echo \"systemctl \$*\" >> '$tmp/systemctl.log'; return 1; }
chown() { :; }
INSTALL_DIR='$tmp/spiralpool' PROJECT_ROOT='$tmp/root' POOL_USER=spiraluser
f() {
local updated=0
$block
echo \"updated=\$updated\"
}
f" 2>&1
    }

    echo "old" > "$tmp/spiralpool/bin/wait-for-node.sh"
    echo "old" > "$tmp/spiralpool/bin/health-monitor.sh"
    local out
    out=$(run_refresh)
    assert_eq "$(cmp -s "$tmp/root/scripts/linux/wait-for-node.sh" "$tmp/spiralpool/bin/wait-for-node.sh" && echo same)" "same" \
        "an old wait-for-node.sh is replaced with the release copy"
    assert_eq "$(cmp -s "$tmp/expected-hm.sh" "$tmp/spiralpool/bin/health-monitor.sh" && echo same)" "same" \
        "an old health-monitor.sh is replaced with install.sh's heredoc"
    if grep -q "^ensure_pool_wallet_loaded() {" "$tmp/spiralpool/bin/health-monitor.sh" && head -1 "$tmp/spiralpool/bin/health-monitor.sh" | grep -q '^#!/bin/bash'; then
        pass "the refreshed health monitor is the full script, including the wallet check"
    else
        fail "the refreshed health monitor is the full script, including the wallet check"
    fi
    assert_contains "$out" "updated=3" "all three refreshes are counted"
    if [[ -x "$tmp/spiralpool/bin/daemon-reindex.sh" ]] &&        grep -q 'zz-spiral-reindex.conf' "$tmp/spiralpool/bin/daemon-reindex.sh" &&        ! grep -q '__INSTALL_DIR__' "$tmp/spiralpool/bin/daemon-reindex.sh"; then
        pass "daemon-reindex.sh is installed with its install directory filled in"
    else
        fail "daemon-reindex.sh is installed with its install directory filled in"
    fi

    out=$(run_refresh)
    assert_contains "$out" "updated=0" "a second run with current copies changes nothing"

    # A release whose heredoc is broken must not replace a working monitor
    echo "working" > "$tmp/spiralpool/bin/health-monitor.sh"
    sed -i "/health-monitor.sh\" > \/dev\/null << 'HEALTHEOF'/a if then" "$tmp/root/install.sh"
    out=$(run_refresh)
    assert_eq "$(cat "$tmp/spiralpool/bin/health-monitor.sh")" "working" "an invalid extracted monitor keeps the installed copy"
    assert_contains "$out" "keeping the installed copy" "the invalid monitor is reported"

    # A release where the heredoc cannot be found extracts nothing, and an empty file passes bash -n
    cp "$PROJECT_ROOT/install.sh" "$tmp/root/install.sh"
    sed -i "s/<< 'HEALTHEOF'/<< 'HEALTH_EOF'/" "$tmp/root/install.sh"
    out=$(run_refresh)
    assert_eq "$(cat "$tmp/spiralpool/bin/health-monitor.sh")" "working" "an empty extraction keeps the installed copy"
    rm -rf "$tmp"
}

test_wallet_recovery_descriptor_coins() {
    log_test "Wallet recovery — descriptor coin list"

    local line list cn
    line=$(grep -aoE 'local descriptor_coins="[^"]*"' "$UPGRADE_SCRIPT" | head -1)
    if [[ -z "$line" ]]; then
        fail "descriptor_coins is set in upgrade.sh" "        renamed or removed"
        return
    fi
    list="${line#*=\"}"
    list="${list%\"}"

    for cn in dgb btc xec fbtc bc2 btcs; do
        # The same membership test the recovery code performs
        if [[ "$list" == *"|${cn}|"* ]]; then
            pass "descriptor_coins covers ${cn}"
        else
            fail "descriptor_coins is missing ${cn} — with the daemon down its wallet has no recovery path" \
                 "        got [$list]"
        fi
    done
}

# =============================================================================
# Test: A Starting Service Counts As Running
# =============================================================================
# spiralstratum waits for its coin daemon in ExecStartPre, so a node that is
# catching up leaves the unit "activating" for minutes. `systemctl is-active
# --quiet` is false for the whole of that window, so an upgrade begun then
# recorded a running pool as "wasn't running" — and said so in its summary.

test_activating_service_counts_as_running() {
    log_test "A Starting Service Counts As Running"

    local fn
    fn=$(sed -n '/^stop_services() {/,/^}/p' "$UPGRADE_SCRIPT" | tr -d '\r')
    if [[ -z "$fn" ]]; then
        fail "stop_services() not found in upgrade.sh"
        return
    fi

    run_stop_services() {
        local state=$1
        (
            STRATUM_SERVICE="spiralstratum"
            DASHBOARD_SERVICE="spiraldash"
            SENTINEL_SERVICE="spiralsentinel"
            HEALTH_SERVICE="spiralpool-health"
            UPDATE_STRATUM=true
            UPDATE_DASHBOARD=false
            UPDATE_SENTINEL=false
            INSTALL_DIR="/nonexistent"
            SV_STATE="$state"
            systemctl() {
                case "$1" in
                    is-active)
                        # Report the state under test until the stop, then inactive.
                        if [[ "$2" == "spiralstratum" ]]; then
                            echo "$SV_STATE"
                            [[ "$SV_STATE" == "active" ]] && return 0 || return 3
                        fi
                        echo "inactive"; return 3 ;;
                    stop|kill) SV_STATE="inactive"; return 0 ;;
                    *) return 0 ;;
                esac
            }
            sleep() { :; }
            suppress_sentinel_alerts() { :; }
            log_info() { :; }
            log_warn() { :; }
            log_success() { :; }
            eval "$fn"
            stop_services
            printf '%s\n' "${SERVICES_WERE_RUNNING[@]:-}"
        )
    }

    assert_contains "$(run_stop_services active)" "spiralstratum" \
        "An active stratum is recorded as running"
    assert_contains "$(run_stop_services activating)" "spiralstratum" \
        "A stratum still waiting for its coin daemon is recorded as running"
    assert_eq "$(run_stop_services inactive | grep -c spiralstratum || true)" "0" \
        "An inactive stratum is not recorded as running"

    # The rollback path decides the same way, from its own detection loop.
    local rb
    rb=$(sed -n '/rollback_were_running+=/,+0p' "$UPGRADE_SCRIPT" | head -1)
    if sed -n '/rollback_were_running=()/,/^    fi$/p' "$UPGRADE_SCRIPT" | grep -q 'activating'; then
        pass "Rollback also treats a starting service as running"
    else
        fail "Rollback still uses is-active --quiet, so a starting service would not be restarted"
    fi
}

# A deliberately raised dbcache was silently halved on upgrade: the caps are
# sized for a box running several daemons, but they were applied flat, so a
# single-coin node with RAM to spare lost the value its operator had chosen.
# The cap must still fire when the daemons together would exhaust RAM.
test_dbcache_cap_respects_available_ram() {
    log_test "dbcache cap only fires when the box cannot afford the value"

    local fn
    fn=$(sed -n '/^rightsize_daemon_resources() {/,/^}/p' "$UPGRADE_SCRIPT" | tr -d '\r')
    if [[ -z "$fn" ]]; then
        fail "rightsize_daemon_resources() not found in upgrade.sh"
        return
    fi

    # coins: space-separated "dir:conf" pairs to create, each with dbcache=8192
    # and maxconnections at $RS_MAXCONN, so a scenario can isolate the dbcache
    # decision from the separate maxconnections cap (which also takes a backup).
    run_rightsize() {
        local ram_mb=$1 root=$2; shift 2
        local pair dir conf
        for pair in "$@"; do
            dir="${pair%%:*}"; conf="${pair##*:}"
            mkdir -p "$root/$dir"
            printf 'server=1\ndbcache=8192\nmaxconnections=%s\n' "${RS_MAXCONN:-64}" > "$root/$dir/$conf"
        done
        (
            SPIRAL_TEST_TOTAL_MB="$ram_mb"
            POOL_USER=""
            resolve_coin_dir() { echo "$root/$1"; }
            log_info() { :; }; log_warn() { :; }; log_success() { :; }
            eval "$fn"
            rightsize_daemon_resources
        ) >/dev/null 2>&1
    }

    local one five
    one=$(mktemp -d); five=$(mktemp -d)

    # 15 GB, one daemon: 55% of RAM is 8485MB, so 8192 is affordable — keep it.
    run_rightsize 15428 "$one" "dgb:digibyte.conf"
    assert_eq "$(grep -c '^dbcache=8192' "$one/dgb/digibyte.conf" || true)" "1" \
        "Single-coin box with headroom keeps the operator's dbcache=8192"
    if compgen -G "$one/dgb/digibyte.conf.bak-*" > /dev/null; then
        fail "No backup should be written when nothing is changed"
    else
        pass "No backup written when the config is left alone"
    fi

    # Same RAM, five daemons: 1697MB each, below the 4096 cap — cap still fires.
    RS_MAXCONN=256
    run_rightsize 15428 "$five" "dgb:digibyte.conf" "btc:bitcoin.conf" \
        "ltc:litecoin.conf" "doge:dogecoin.conf" "nmc:namecoin.conf"
    assert_eq "$(grep -c '^dbcache=4096' "$five/dgb/digibyte.conf" || true)" "1" \
        "Multi-coin box still caps dbcache to protect RAM"
    if compgen -G "$five/dgb/digibyte.conf.bak-*" > /dev/null; then
        pass "Overridden config is backed up before being rewritten"
    else
        fail "Overridden config must be backed up before being rewritten"
    fi
    assert_eq "$(grep -c '^maxconnections=64' "$five/dgb/digibyte.conf" || true)" "1" \
        "maxconnections cap is unaffected by the RAM budget"

    # A box the old flat cap already damaged: dbcache sits at exactly 4096 on a
    # long chain. The upgrade restores what install.sh would have written, and
    # moves the unit's memory ceiling with it so systemd cannot kill the daemon
    # mid-sync. A value the cap never wrote (2048) must be left alone.
    local fix sysd small
    fix=$(mktemp -d); sysd="$fix/systemd"
    mkdir -p "$sysd" "$fix/dgb"
    printf 'server=1\ndbcache=4096\nmaxconnections=64\n' > "$fix/dgb/digibyte.conf"
    printf '[Service]\nMemoryMax=11G\nMemoryHigh=10G\n' > "$sysd/digibyted.service"
    (
        SPIRAL_TEST_TOTAL_MB=15428
        SPIRAL_SYSTEMD_DIR="$sysd"
        POOL_USER=""
        resolve_coin_dir() { echo "$fix/$1"; }
        systemctl() { :; }
        log_info() { :; }; log_warn() { :; }; log_success() { :; }
        eval "$fn"
        rightsize_daemon_resources
    ) >/dev/null 2>&1

    # A short chain the cap never raised: not on the restore list, left as set.
    small=$(mktemp -d); mkdir -p "$small/btcs"
    printf 'server=1\ndbcache=2048\nmaxconnections=64\n' > "$small/btcs/bitcoinsilver.conf"
    (
        SPIRAL_TEST_TOTAL_MB=15428
        SPIRAL_SYSTEMD_DIR="$small/systemd"
        POOL_USER=""
        resolve_coin_dir() { echo "$small/$1"; }
        systemctl() { :; }
        log_info() { :; }; log_warn() { :; }; log_success() { :; }
        eval "$fn"
        rightsize_daemon_resources
    ) >/dev/null 2>&1

    assert_eq "$(grep -c '^dbcache=8192' "$fix/dgb/digibyte.conf" || true)" "1" \
        "A dbcache the old cap cut to 4096 is restored to 8192"
    assert_eq "$(grep -c '^dbcache=2048' "$small/btcs/bitcoinsilver.conf" || true)" "1" \
        "A short chain's dbcache is left exactly as the operator set it"
    local dropin="$sysd/digibyted.service.d/zz-spiral-memory.conf"
    if [[ -f "$dropin" ]]; then
        assert_contains "$(cat "$dropin")" "MemoryMax=13G" \
            "Memory ceiling raised to dbcache + 5GB alongside the restore"
    else
        fail "Memory ceiling raised to dbcache + 5GB alongside the restore"
    fi

    rm -rf "$one" "$five" "$fix" "$small"
}

# =============================================================================
# Run All Tests
# =============================================================================

echo ""
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN}  SPIRAL POOL — upgrade.sh Regression Harness${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

test_check_local_reports_the_local_version() {
    log_test "upgrade.sh --check honours --local instead of asking GitHub"

    # --check exits before main() ever calls detect_source_directory, and
    # check_for_updates only ever queried the GitHub releases API. So
    # "--local --check" against a release that is not tagged yet answered
    # update_available:false -- confident, reassuring and wrong, on the one
    # command an operator runs precisely to find out what is about to happen.
    local tmp; tmp=$(mktemp -d)
    mkdir -p "$tmp/tree/src/stratum" "$tmp/install"
    echo "3.0.0" > "$tmp/tree/VERSION"
    : > "$tmp/tree/src/stratum/go.mod"
    echo "2.7.1" > "$tmp/install/VERSION"

    # check_for_updates ends in a `cat << EOF` whose JSON body contains a line
    # that is just "}", so the usual sed '/^}/p' extraction truncates the
    # function mid-heredoc. Track the heredoc and stop at the real closing brace.
    local extract='
        $0 ~ "^" fn "[(][)] [{]" { f = 1; print; next }
        f {
            print
            if (hd == "") {
                if ($0 ~ /<<[[:space:]]*EOF/) { hd = "EOF"; next }
                if ($0 == "}") exit
            } else if ($0 == hd) hd = ""
        }'

    local harness="$tmp/tree/harness.sh"
    {
        echo 'set -u'
        echo 'RED=""; BLUE=""; NC=""'
        echo "INSTALL_DIR=\"$tmp/install\""
        echo 'PROJECT_ROOT=""; TARGET_VERSION=""; CURRENT_VERSION=""'
        echo 'USE_LOCAL="${USE_LOCAL:-false}"'
        echo 'log_info() { echo "[INFO] $1"; }'
        echo 'log_error() { echo "[ERROR] $1"; }'
        awk -v fn="detect_source_directory" "$extract" "$UPGRADE_SCRIPT"
        awk -v fn="check_for_updates"       "$extract" "$UPGRADE_SCRIPT"
        echo 'check_for_updates'
    } > "$harness"

    local out err info_lines
    out=$(USE_LOCAL=true bash "$harness" 2>"$tmp/err") || true
    err=$(cat "$tmp/err" 2>/dev/null || true)

    assert_contains "$out" '"latest_version": "3.0.0"' "--local --check reads VERSION from the local tree"
    assert_contains "$out" '"update_available": true'  "--local --check sees 2.7.1 -> 3.0.0 as an upgrade"

    # Sentinel consumes this as JSON, and detect_source_directory logs on stdout,
    # so calling it here without redirecting would corrupt the only consumer.
    info_lines=$(echo "$out" | grep -c '\[INFO\]' || true)
    assert_eq "$info_lines" "0" "the JSON on stdout carries no log lines"
    assert_contains "$err" "Source directory:" "detect_source_directory logs to stderr instead"

    rm -rf "$tmp"
}


test_version_comparison
echo ""
test_version_validation
echo ""
test_pool_user_validation
echo ""
test_github_api_parsing
echo ""
test_http_status_handling
echo ""
test_json_output
echo ""
test_backup_name
echo ""
test_argument_parsing
echo ""
test_rollback_integrity
echo ""
test_symlink_protection
echo ""
test_pgdump_completeness
echo ""
test_credential_separation
echo ""
test_lock_file
echo ""
test_downgrade_prevention
echo ""
test_service_detection
echo ""
test_disk_space_check
echo ""
test_sql_identifier_validation
echo ""
test_git_clone_retry
echo ""
test_metrics_token_sanitization
echo ""
test_wants_deps_sanitization
echo ""
test_recovered_token_heredoc_safety
echo ""
test_dashboard_template_swap
echo ""
test_stratum_binary_atomic_install
echo ""
test_backup_name_path_traversal
echo ""
test_temp_dir_nulling
echo ""
test_password_credential_clearing
echo ""
test_exit_code_capture_order
echo ""
test_spiralctl_atomic_install
echo ""
test_password_prompt_auto_mode_guard
echo ""
test_stratum_build_deploy_split
echo ""
test_stratum_v2_opt_in_migration
echo ""
test_coin_upgrade_daemon_targets
echo ""
test_update_dashboard_copies_static_assets
echo ""
test_wallet_recovery_descriptor_coins
echo ""
test_bin_scripts_refreshed
echo ""
test_activating_service_counts_as_running
echo ""
test_dbcache_cap_respects_available_ram

echo ""
test_check_local_reports_the_local_version

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
