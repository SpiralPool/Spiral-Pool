#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Stopping an LED celebration when its block is orphaned.
#
# A celebration runs for two hours by default and nothing could end it early:
# block-celebrate.sh took --test, --duration, --miners, --scan, --force and
# --list, and finished on its deadline, on quiet hours, or on a signal. A block
# that was found and then orphaned minutes later left the miners celebrating a
# reward that was never collected, for the rest of the two hours.
#
# The correctness question is not "can it stop" but "can it stop the WRONG
# celebration". A later block extends the running one, so the lights may stand
# for several blocks at once; one of them orphaning must not switch the lights
# off on the others. These tests pin that.
#
# Usage: bash tests/test_celebration_stop.sh
# Exit:  0 all pass, 1 otherwise
# =============================================================================

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BC="$ROOT/scripts/linux/block-celebrate.sh"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'
RUN=0; PASSED=0; FAILED=0
log_test() { echo -e "${CYAN}[TEST]${NC} $1"; }
pass() { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { RUN=$((RUN+1)); FAILED=$((FAILED+1)); echo -e "  ${RED}FAIL${NC}: $1"; [[ -n "${2:-}" ]] && echo -e "    $2"; }

TMP="$(mktemp -d)"
cleanup_all() { pkill -P $$ sleep 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup_all EXIT

LIB="$TMP/lib.sh"
{
    echo 'set -uo pipefail'
    echo "LOCK_FILE=\"$TMP/celebrate.lock\""
    echo "DEADLINE_FILE=\"$TMP/celebrate.deadline\""
    echo "BLOCKS_FILE=\"$TMP/celebrate.blocks\""
    echo 'log() { :; }'
    echo 'log_error() { :; }'
    sed -n '/^record_block() {/,/^}/p' "$BC"
    sed -n '/^stop_celebration() {/,/^}/p' "$BC"
} > "$LIB"
# shellcheck disable=SC1090
source "$LIB"

# A stand-in for a running celebration: a real process holding the lock.
start_fake() {
    rm -f "$TMP/celebrate.blocks"
    sleep 120 &
    FAKE_PID=$!
    echo "$FAKE_PID" > "$TMP/celebrate.lock"
}
still_running() { kill -0 "$FAKE_PID" 2>/dev/null; }
stop_fake() { kill "$FAKE_PID" 2>/dev/null || true; wait "$FAKE_PID" 2>/dev/null || true; }

log_test "stopping when nothing is running is not an error"
rm -f "$TMP/celebrate.lock"
if stop_celebration ""; then pass "no lock file: returns cleanly"; else fail "no lock file: returns cleanly"; fi

echo "999999999" > "$TMP/celebrate.lock"
stop_celebration "" >/dev/null 2>&1
if [[ ! -f "$TMP/celebrate.lock" ]]; then pass "a stale lock is cleared"; else fail "a stale lock is cleared"; fi

log_test "an orphaned block stops the celebration it is the last one for"
start_fake
record_block "hashA"
stop_celebration "hashA" >/dev/null 2>&1
sleep 0.3
if still_running; then fail "the last block orphaning stops the celebration"; stop_fake; else pass "the last block orphaning stops the celebration"; fi

log_test "an orphaned block does NOT stop a celebration other blocks still stand for"
start_fake
record_block "hashA"
record_block "hashB"
stop_celebration "hashA" >/dev/null 2>&1
sleep 0.3
if still_running; then pass "a second block keeps the lights on"; else fail "a second block keeps the lights on" "orphaning hashA killed hashB's celebration"; fi
if grep -qxF "hashB" "$TMP/celebrate.blocks" 2>/dev/null; then pass "the surviving block is still recorded"; else fail "the surviving block is still recorded"; fi
if grep -qxF "hashA" "$TMP/celebrate.blocks" 2>/dev/null; then fail "the orphaned block is withdrawn"; else pass "the orphaned block is withdrawn"; fi
# ...and once the survivor orphans too, the lights go out.
stop_celebration "hashB" >/dev/null 2>&1
sleep 0.3
if still_running; then fail "withdrawing the last survivor stops it"; stop_fake; else pass "withdrawing the last survivor stops it"; fi

log_test "a block that is not the one being celebrated changes nothing"
start_fake
record_block "hashA"
stop_celebration "hashZ" >/dev/null 2>&1
sleep 0.3
if still_running; then pass "an unrelated block leaves the celebration alone"; else fail "an unrelated block leaves the celebration alone"; fi
stop_fake

log_test "an unattributed celebration is left alone rather than guessed at"
start_fake   # no record_block: started by a caller that named no block
stop_celebration "hashA" >/dev/null 2>&1
sleep 0.3
if still_running; then pass "no recorded blocks: not stopped on an unprovable attribution"; else fail "no recorded blocks: not stopped on an unprovable attribution"; fi

log_test "an unconditional stop always stops it"
stop_celebration "" >/dev/null 2>&1
sleep 0.3
if still_running; then fail "an operator stop works with no block named"; stop_fake; else pass "an operator stop works with no block named"; fi

echo ""
echo "==========================================================="
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "==========================================================="
[[ $FAILED -eq 0 ]] || exit 1
exit 0
