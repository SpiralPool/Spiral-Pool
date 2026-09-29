#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Which pool services the post-apt-update restart actually restarts.
#
# The script used to restart Sentinel, the dashboard and the stratum whenever
# dpkg.log showed a package upgraded that day whose name merely contained
# "openssl", "python3", "zlib" and so on. libevent-openssl, python3-jwt and
# -dev header packages each bounced the whole pool, on consecutive mornings,
# though no pool service loads any of them.
#
# It now restarts a service only when one of its processes still has a shared
# library mapped from a file an upgrade replaced. These tests pin that, and
# that a failed restart logs systemctl's own reason instead of discarding it.
#
# Usage: bash tests/test_apt_post_update_restart.sh
# Exit:  0 all pass, 1 otherwise
# =============================================================================

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SRC="$ROOT/scripts/linux/apt-post-update-restart.sh"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'
RUN=0; PASSED=0; FAILED=0
log_test() { echo -e "${CYAN}[TEST]${NC} $1"; }
pass() { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { RUN=$((RUN+1)); FAILED=$((FAILED+1)); echo -e "  ${RED}FAIL${NC}: $1"; [[ -n "${2:-}" ]] && echo -e "    $2"; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The script under test, pointed at a fake /proc, cgroup tree and debounce file.
SUT="$TMP/apt-post-update-restart.sh"
sed -e "s#/proc/#$TMP/proc/#g" \
    -e "s#/sys/fs/cgroup#$TMP/cgroup#g" \
    -e "s#^DEBOUNCE_FILE=.*#DEBOUNCE_FILE=\"$TMP/debounce\"#" \
    "$SRC" > "$SUT"

# Stand-ins for systemctl and sleep. Every service is enabled and active; each
# runs as MainPID 100/200/300, and spiraldash also has a gunicorn worker (201).
mkdir -p "$TMP/bin"
cat > "$TMP/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    is-active|is-enabled) exit 0 ;;
    show)
        svc="${!#}"
        if [[ "$3" == "ControlGroup" ]]; then echo "/system.slice/${svc}.service"; exit 0; fi
        case "$svc" in spiralsentinel) echo 100 ;; spiraldash) echo 200 ;; spiralstratum) echo 300 ;; esac
        ;;
    restart)
        echo "$2" >> "$FAKE_RESTARTS"
        if [[ "$2" == "${FAKE_FAIL_SVC:-}" ]]; then
            echo "Job for $2.service canceled." >&2
            exit 1
        fi
        ;;
esac
exit 0
EOF
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/systemctl" "$TMP/bin/sleep"

export FAKE_RESTARTS="$TMP/restarts"

# A clean library mapping every process has, plus mappings that end in
# "(deleted)" but are not shared libraries and must never trigger a restart.
CLEAN='7f1c2a000000-7f1c2a1b0000 r-xp 00000000 08:01 1201 /usr/lib/x86_64-linux-gnu/libc.so.6
7f1c2b000000-7f1c2b010000 rw-s 00000000 00:01 4411 /memfd:gunicorn (deleted)
7f1c2c000000-7f1c2c010000 rw-s 00000000 00:1a 5512 /dev/shm/sem.x (deleted)
00400000-00c00000 r-xp 00000000 08:01 7788 /spiralpool/bin/spiralstratum (deleted)'

reset() {
    rm -rf "$TMP/proc" "$TMP/cgroup" "$TMP/install" "$TMP/debounce" "$FAKE_RESTARTS"
    mkdir -p "$TMP/install"
    for pid in 100 200 201 300; do
        mkdir -p "$TMP/proc/$pid"
        echo "$CLEAN" > "$TMP/proc/$pid/maps"
    done
    for svc in spiralsentinel spiraldash spiralstratum; do
        mkdir -p "$TMP/cgroup/system.slice/$svc.service"
    done
    echo 100 > "$TMP/cgroup/system.slice/spiralsentinel.service/cgroup.procs"
    printf '200\n201\n' > "$TMP/cgroup/system.slice/spiraldash.service/cgroup.procs"
    echo 300 > "$TMP/cgroup/system.slice/spiralstratum.service/cgroup.procs"
}
stale() { echo "7f1c2d000000-7f1c2d0a0000 r-xp 00000000 08:01 9901 $2 (deleted)" >> "$TMP/proc/$1/maps"; }
run() { PATH="$TMP/bin:$PATH" INSTALL_DIR="$TMP/install" bash "$SUT"; }
restarted() { [[ -f "$FAKE_RESTARTS" ]] && tr '\n' ' ' < "$FAKE_RESTARTS" | sed 's/ $//'; }
LOG="$TMP/install/logs/apt-restart.log"

log_test "nothing restarts when no service has a replaced library loaded"
reset
run
if [[ -z "$(restarted)" ]]; then pass "no restarts"; else fail "no restarts" "restarted: $(restarted)"; fi
if [[ ! -f "$LOG" ]]; then pass "nothing logged"; else fail "nothing logged" "$(cat "$LOG")"; fi
if [[ ! -f "$TMP/debounce" ]]; then pass "debounce not armed"; else fail "debounce not armed"; fi

log_test "only the service running a replaced library restarts"
reset
stale 201 /usr/lib/x86_64-linux-gnu/libssl.so.3
run
if [[ "$(restarted)" == "spiraldash" ]]; then pass "a stale gunicorn worker restarts only spiraldash"; else fail "a stale gunicorn worker restarts only spiraldash" "restarted: $(restarted)"; fi
if grep -q "spiraldash: libssl.so.3" "$LOG" 2>/dev/null; then pass "the stale library is named in the log"; else fail "the stale library is named in the log"; fi

log_test "a replaced Python extension module restarts Sentinel"
reset
stale 100 /usr/lib/python3/dist-packages/cryptography/hazmat/bindings/_rust.abi3.so
run
if [[ "$(restarted)" == "spiralsentinel" ]]; then pass "extension module detected"; else fail "extension module detected" "restarted: $(restarted)"; fi

log_test "a failed restart logs systemctl's reason"
reset
stale 300 /usr/lib/x86_64-linux-gnu/libc.so.6
FAKE_FAIL_SVC=spiralstratum run
if [[ "$(restarted)" == "spiralstratum" ]]; then pass "only stratum attempted"; else fail "only stratum attempted" "restarted: $(restarted)"; fi
if grep -q "systemctl: Job for spiralstratum.service canceled." "$LOG" 2>/dev/null; then pass "reason recorded"; else fail "reason recorded" "$(cat "$LOG" 2>/dev/null)"; fi

echo ""
echo "Tests: $RUN  Passed: $PASSED  Failed: $FAILED"
[[ $FAILED -eq 0 ]] || exit 1
exit 0
