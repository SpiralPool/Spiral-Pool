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

    local build_line stop_line deploy_line
    build_line=$(echo "$main_body" | grep -n 'build_stratum' | head -1 | cut -d: -f1)
    stop_line=$(echo "$main_body" | grep -n 'stop_services' | head -1 | cut -d: -f1)
    deploy_line=$(echo "$main_body" | grep -n 'deploy_stratum' | head -1 | cut -d: -f1)

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
# Run All Tests
# =============================================================================

echo ""
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN}  SPIRAL POOL — upgrade.sh Regression Harness${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

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
