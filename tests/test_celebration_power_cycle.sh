#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Avalon Nano 3 celebration vs. power cycles (v3.0.3 regression suite)
#
# The bug: an Avalon keeps the last LED effect it was sent and shows it again
# when it boots. A celebration's whole memory lived in /run (tmpfs) and in the
# running process, and the end of a celebration was a single "LED off" write.
# So a power cut — or a miner that happened to be rebooting when that one write
# was due — left the miners strobing for hours with nothing left to stop them.
#
# This suite drives the REAL block-celebrate.sh main() as separate processes.
# Only three things are simulated:
#   - the miner: a CGMiner API that accepts ledset while "up", answers nothing
#     while "down", and — like the hardware — keeps its last LED across a
#     power cycle;
#   - the clock: a fake monotonic/wall clock, so hours run in seconds and wall
#     clock jumps can be injected;
#   - sleep: advances the fake clock instead of waiting.
#
# Usage: bash tests/test_celebration_power_cycle.sh
# Exit:  0 all pass, 1 otherwise
# =============================================================================

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
# Overridable so the suite can be pointed at an older copy of the script, to
# show which of these cases it fails.
BC="${CELEBRATE_SCRIPT_UNDER_TEST:-$ROOT/scripts/linux/block-celebrate.sh}"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'
RUN=0; PASSED=0; FAILED=0
log_test() { echo -e "${CYAN}[TEST]${NC} $1"; }
pass() { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { RUN=$((RUN+1)); FAILED=$((FAILED+1)); echo -e "  ${RED}FAIL${NC}: $1"; [[ -n "${2:-}" ]] && echo -e "    $2"; }
check() { if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "want [$3] got [$2]"; fi; }

TOP="$(mktemp -d)"
BG_PIDS=()
cleanup_all() {
    local p
    for p in ${BG_PIDS[@]+"${BG_PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
    rm -rf "$TOP"
}
trap cleanup_all EXIT

OFF="0-0-0-0-0-0"
M1="10.0.0.1"; M2="10.0.0.2"

# ── The harness: the real script, with the hardware and the clock simulated ──
HARNESS="$TOP/sim-block-celebrate.sh"   # its name contains "celebrate", as a real one does
cat > "$HARNESS" <<'HARNESS_EOF'
set -uo pipefail
# shellcheck source=/dev/null
source "$SIM_BC"
FAKE_MS="${SIM_START_MS:-0}"
SIM_CHILD=0

# Children (one per miner) run on their own copy of the clock. The parent does
# nothing time-dependent until they finish, so it reads the furthest any child got.
_sim_ms() {
    local m="$FAKE_MS" f v
    if [[ "$SIM_CHILD" == "0" ]]; then
        for f in "$SIM"/clock.*; do
            [[ -f "$f" ]] || continue
            read -r v < "$f" || continue
            (( v > m )) && m=$v
        done
    fi
    echo "$m"
}
_mono_now() { echo $(( $(_sim_ms) / 1000 )); }
_wall_now() {
    local skew=0
    [[ -f "$SIM/wallskew" ]] && read -r skew < "$SIM/wallskew"
    echo $(( 1800000000 + $(_sim_ms) / 1000 + skew ))
}
sleep() {
    local s="$1" i f
    if [[ "$s" == *.* ]]; then
        i="${s%%.*}"; f="${s#*.}000"
        FAKE_MS=$(( FAKE_MS + ${i:-0} * 1000 + 10#${f:0:3} ))
    else
        FAKE_MS=$(( FAKE_MS + s * 1000 ))
    fi
}
is_quiet_hours() { return 1; }
load_configured_miners() { [[ -f "$SIM/configured" ]] && cat "$SIM/configured"; }

eval "$(declare -f celebrate_miner | sed '1s/celebrate_miner/_sim_real_celebrate_miner/')"
celebrate_miner() {
    SIM_CHILD=1
    _sim_real_celebrate_miner "$@"
    echo "$FAKE_MS" > "$SIM/clock.$BASHPID"
}

# down.<ip>: lines of "<from> <to>" in simulated seconds — the miner is powered off.
_sim_down() {
    local ip="$1" t="$2" from to
    [[ -f "$SIM/down.$ip" ]] || return 1
    while read -r from to; do
        (( t >= from && t < to )) && return 0
    done < "$SIM/down.$ip"
    return 1
}
_sim_hooks() {
    local t="$1" at
    # Another block extends the deadline on every write: only the ceiling can end it.
    if [[ -f "$SIM/keep_extending" ]]; then
        echo "$(( 1800000000 + t + 55 )) $(( t + 55 )) $(_boot_id)" > "$DEADLINE_FILE"
    fi
    # Wall clock steps back three hours at simulated second <at>.
    if [[ -f "$SIM/wall_jump_at" ]]; then
        read -r at < "$SIM/wall_jump_at"
        (( t >= at )) && echo -10800 > "$SIM/wallskew"
    fi
    return 0
}
cgminer_cmd() {
    local ip="$1" cmd="$2" t led=""
    t=$(( $(_sim_ms) / 1000 ))
    _sim_hooks "$t"
    if _sim_down "$ip" "$t"; then
        echo "$t $ip DOWN" >> "$SIM/history"
        return 0
    fi
    case "$cmd" in
        "ascset|0,ledset,"*)
            led="${cmd#ascset|0,ledset,}"
            echo "$led" > "$SIM/led.$ip"
            echo "$t $ip SET $led" >> "$SIM/history"
            printf '%s' "STATUS=I,When=0,Code=118,Msg=ASC 0 set info 'led set ok',Description=cgminer"
            ;;
        version) printf '%s' "STATUS=S,Msg=CGMiner versions|VERSION,CGMiner=4.11.1" ;;
        stats)
            [[ -f "$SIM/led.$ip" ]] && read -r led < "$SIM/led.$ip"
            printf 'STATS=0,ID=AVA100,LEDUser[%s]' "$led"
            ;;
        *) printf '%s' "STATUS=E,Code=14,Msg=Invalid command" ;;
    esac
}
if [[ -n "${SIM_RUN_STUB:-}" ]]; then
    run_celebration() { echo "$$" >> "$SIM/runners"; command sleep "$SIM_RUN_STUB"; }
fi
main "$@"
HARNESS_EOF

# new_sim — a fresh simulated host: run dir, data dir, boot id, two miners up
# showing a solid blue LED.
new_sim() {
    SIM="$(mktemp -d "$TOP/sim.XXXXXX")"
    mkdir -p "$SIM/run" "$SIM/data"
    echo "boot-A" > "$SIM/boot_id"
    echo "1-100-50-0-0-255" > "$SIM/led.$M1"
    echo "1-100-50-0-0-255" > "$SIM/led.$M2"
    printf '%s\n%s\n' "$M1" "$M2" > "$SIM/configured"
    : > "$SIM/history"
    mkdir -p "$SIM/home"
    START_MS=0
}
# sim [args...] — run the real script on the simulated host, in the foreground.
sim() {
    env SIM="$SIM" SIM_BC="$BC" SIM_START_MS="$START_MS" \
        CELEBRATE_RUN_DIR="$SIM/run" CELEBRATE_STATE_FILE="$SIM/data/celebrate.state" \
        CELEBRATE_BOOT_ID_FILE="$SIM/boot_id" CELEBRATE_LOG="$SIM/celebrate.log" \
        CELEBRATION_MAX_SECONDS="${MAX_SECONDS:-86400}" HOME="$SIM/home" \
        ${SIM_TIMEOUT:+timeout "$SIM_TIMEOUT"} bash "$HARNESS" "$@" 2>>"$SIM/celebrate.log"
}
led_of()      { cat "$SIM/led.$1" 2>/dev/null; }
state_file()  { echo "$SIM/data/celebrate.state"; }
logged()      { grep -qF "$1" "$SIM/celebrate.log" 2>/dev/null && echo yes || echo no; }
# Last simulated second at which <ip> was sent anything other than "off".
last_effect_t() { awk -v ip="$1" -v off="$OFF" '$2==ip && $3=="SET" && $4!=off {t=$1} END{print t+0}' "$SIM/history"; }
first_off_t()   { awk -v ip="$1" -v off="$OFF" '$2==ip && $3=="SET" && $4==off {print $1; exit}' "$SIM/history"; }
effects_after() { awk -v ip="$1" -v from="$2" -v off="$OFF" '$2==ip && $3=="SET" && $4!=off && $1>=from {n++} END{print n+0}' "$SIM/history"; }
nothing_left() {
    local f leftovers=""
    for f in "$SIM/run/celebrate.lock" "$SIM/run/celebrate.deadline" "$SIM/run/celebrate.blocks" "$(state_file)"; do
        [[ -e "$f" ]] && leftovers+=" $(basename "$f")"
    done
    echo "${leftovers:-none}"
}

# The script reads led_idle_state from the Sentinel config like production; the
# simulated host has none, so the default ("off") applies unless a test writes
# one. sim() points HOME into the sim, so the per-user config path is the sim's.
with_policy() {
    mkdir -p "$SIM/home/.spiralsentinel"
    echo "{\"led_idle_state\": \"$1\"}" > "$SIM/home/.spiralsentinel/config.json"
}
sim_home() { sim "$@"; }

# ─────────────────────────────────────────────────────────────────────────────
log_test "1. normal celebration: starts, runs to its deadline, ends dark, leaves nothing behind"
new_sim
sim --duration 60 --miners "$M1 $M2" --force --block H1; rc=$?
check "exits 0" "$rc" "0"
check "$M1 ends with its LED off" "$(led_of $M1)" "$OFF"
check "$M2 ends with its LED off" "$(led_of $M2)" "$OFF"
n=$(effects_after "$M1" 0)
if (( n > 20 )); then pass "the celebration actually drove the LEDs ($n effect writes)"; else fail "the celebration actually drove the LEDs" "only $n writes"; fi
t_end=$(last_effect_t "$M1")
if (( t_end >= 45 && t_end <= 60 + 20 )); then pass "effects stop within one phase of the 60s deadline (last at ${t_end}s)"; else fail "effects stop within one phase of the deadline" "last effect at ${t_end}s"; fi
check "lock, deadline, block list and durable record all cleared" "$(nothing_left)" "none"

# ─────────────────────────────────────────────────────────────────────────────
log_test "2. miner power-cycles mid-celebration (host stays up): celebration continues, then ends dark"
new_sim
echo "20 45" > "$SIM/down.$M2"
sim --duration 90 --miners "$M1 $M2" --force --block H1
check "$M2 (power-cycled) ends with its LED off" "$(led_of $M2)" "$OFF"
check "$M1 (unaffected) ends with its LED off" "$(led_of $M1)" "$OFF"
n=$(effects_after "$M2" 45)
if (( n > 0 )); then pass "celebration resumed on $M2 after it booted ($n writes after 45s)"; else fail "celebration resumed on $M2 after it booted"; fi
t_end=$(last_effect_t "$M2")
if (( t_end <= 90 + 20 )); then pass "and still ended on time (last effect at ${t_end}s)"; else fail "and still ended on time" "last effect at ${t_end}s"; fi
check "nothing left behind" "$(nothing_left)" "none"

# ─────────────────────────────────────────────────────────────────────────────
log_test "3. miner is rebooting exactly when the restore is due: the restore is retried until it lands"
# The pre-3.0.3 failure: one "LED off" write at the deadline, refused because the
# miner was booting, and the miner then came back showing the last effect.
new_sim
echo "50 140" > "$SIM/down.$M2"
sim --duration 60 --miners "$M1 $M2" --force --block H1
check "$M2 ends with its LED off" "$(led_of $M2)" "$OFF"
t_off=$(first_off_t "$M2")
if [[ -n "$t_off" ]] && (( t_off >= 140 && t_off <= 140 + 15 )); then pass "restore landed once the miner was back (${t_off}s)"; else fail "restore landed once the miner was back" "first off at '${t_off}'"; fi
check "retry was logged" "$(logged 'retrying in 15s')" "yes"
check "no effect was sent after the deadline" "$(effects_after "$M2" 81)" "0"
check "nothing left behind" "$(nothing_left)" "none"

# ─────────────────────────────────────────────────────────────────────────────
log_test "4. miner still down after the retry window: kept on the record, darkened by recovery when it returns"
new_sim
echo "50 100000" > "$SIM/down.$M2"
sim --duration 60 --miners "$M1 $M2" --force --block H1
check "$M1 ends dark" "$(led_of $M1)" "$OFF"
check "$M2 still shows a celebration effect (it never answered)" "$([[ "$(led_of $M2)" != "$OFF" ]] && echo yes || echo no)" "yes"
check "the durable record survives, naming only $M2" "$(grep -c '^miner=' "$(state_file)" 2>/dev/null)" "1"
check "it names $M2" "$(grep -c "^miner=$M2 " "$(state_file)" 2>/dev/null)" "1"
check "it is owner-less (pid empty)" "$(grep -c '^pid=$' "$(state_file)" 2>/dev/null)" "1"
check "lock and deadline released" "$([[ -e $SIM/run/celebrate.lock || -e $SIM/run/celebrate.deadline ]] && echo held || echo released)" "released"
# A recovery pass while it is still down: nothing changes, the attempt is counted.
START_MS=$(( 5000 * 1000 ))
sim --recover
check "recovery while still down keeps the record" "$([[ -f "$(state_file)" ]] && echo kept || echo gone)" "kept"
check "and counts the attempt" "$(grep '^attempts=' "$(state_file)" 2>/dev/null)" "attempts=1"
# It comes back: the next pass darkens it and clears the record.
START_MS=$(( 200000 * 1000 ))
sim --recover
check "recovery after it boots turns $M2 off" "$(led_of $M2)" "$OFF"
check "and clears the record" "$(nothing_left)" "none"

# ─────────────────────────────────────────────────────────────────────────────
log_test "5. host AND miners lose power mid-celebration: the next boot darkens them, and does not resume"
new_sim
# What a power cut leaves: a record from the old boot, miners showing an effect
# (the Avalon re-displays it on boot), and nothing in /run (tmpfs is gone).
cat > "$(state_file)" <<EOF
format=1
id=boot-A-4242-100-1
boot=boot-A
pid=4242
started=1800000000
idle=off
attempts=0
miner=$M1 -
miner=$M2 -
end=1
EOF
echo "4-100-90-0-0-0" > "$SIM/led.$M1"
echo "2-100-90-255-255-255" > "$SIM/led.$M2"
echo "boot-B" > "$SIM/boot_id"
: > "$SIM/history"
sim --recover
check "$M1 is dark after the reboot" "$(led_of $M1)" "$OFF"
check "$M2 is dark after the reboot" "$(led_of $M2)" "$OFF"
check "the celebration was NOT resumed (only off writes)" "$(effects_after "$M1" 0)$(effects_after "$M2" 0)" "00"
check "verdict logged as rebooted" "$(logged '(rebooted)')" "yes"
check "record cleared" "$(nothing_left)" "none"

# ─────────────────────────────────────────────────────────────────────────────
log_test "6. restart just BEFORE the deadline: still not resumed (conservative policy), still dark"
new_sim
cat > "$(state_file)" <<EOF
format=1
id=x
boot=boot-A
pid=4242
started=1800000000
idle=off
attempts=0
miner=$M1 -
end=1
EOF
# The old run's deadline was 5s away when power went; /tmp-fallback hosts keep it.
echo "$(( 1800000000 + 5 ))" > "$SIM/run/celebrate.deadline"
echo "4-100-90-0-0-0" > "$SIM/led.$M1"
echo "boot-B" > "$SIM/boot_id"
: > "$SIM/history"
sim --recover
check "$M1 dark" "$(led_of $M1)" "$OFF"
check "no effects sent" "$(effects_after "$M1" 0)" "0"

# ─────────────────────────────────────────────────────────────────────────────
log_test "7. restart AFTER the deadline with a new block: the old deadline/lock cannot hijack the new run"
new_sim
echo "999999" > "$SIM/run/celebrate.lock"                      # dead PID
echo "$(( 1800000000 - 3600 ))" > "$SIM/run/celebrate.deadline"  # legacy format, an hour stale
sim --duration 30 --miners "$M1" --force --block H2
check "new celebration ran and ended dark" "$(led_of $M1)" "$OFF"
t_end=$(last_effect_t "$M1")
if (( t_end >= 15 && t_end <= 30 + 20 )); then pass "it ran for its own 30s, not the stale deadline (last effect ${t_end}s)"; else fail "it ran for its own duration" "last effect ${t_end}s"; fi
check "stale lock was replaced and released" "$(nothing_left)" "none"

# ─────────────────────────────────────────────────────────────────────────────
log_test "8. missing, corrupted, torn and malformed records"
new_sim
: > "$SIM/history"
sim --recover; rc=$?
check "no record: --recover exits 0" "$rc" "0"
check "no record: touches no miner" "$(wc -l < "$SIM/history" | tr -d ' ')" "0"

for kind in garbage torn badminer badpid empty; do
    new_sim
    echo "4-100-90-0-0-0" > "$SIM/led.$M1"; echo "4-100-90-0-0-0" > "$SIM/led.$M2"
    case "$kind" in
        garbage)  printf '\x00\x01not a record at all\n' > "$(state_file)" ;;
        torn)     printf 'format=1\nid=t\nboot=boot-A\npid=\nattempts=0\nminer=%s -\n' "$M1" > "$(state_file)" ;;  # no end=1
        badminer) printf 'format=1\nid=t\nboot=boot-A\npid=\nattempts=0\nminer=10.0.0.1;rm -rf / -\nend=1\n' > "$(state_file)" ;;
        badpid)   printf 'format=1\nid=t\nboot=boot-A\npid=abc\nattempts=0\nminer=%s -\nend=1\n' "$M1" > "$(state_file)" ;;
        empty)    : > "$(state_file)" ;;
    esac
    sim --recover
    check "$kind record: treated as corrupt, configured miners darkened" "$(led_of $M1)/$(led_of $M2)" "$OFF/$OFF"
    check "$kind record: removed" "$([[ -f "$(state_file)" ]] && echo present || echo removed)" "removed"
done

new_sim
printf 'format=1\nid=half' > "$(state_file).tmp.777"   # a write interrupted before its rename
: > "$SIM/history"
sim --recover
check "an interrupted write (orphan .tmp, no record) is ignored" "$(wc -l < "$SIM/history" | tr -d ' ')" "0"

# ─────────────────────────────────────────────────────────────────────────────
log_test "9. stale active flag without valid timing information"
new_sim
echo "999999" > "$SIM/run/celebrate.lock"     # 'active' lock, dead owner, no deadline file at all
sim --stop; rc=$?
check "--stop on a dead lock exits 0" "$rc" "0"
check "and clears it" "$([[ -e $SIM/run/celebrate.lock ]] && echo present || echo cleared)" "cleared"
new_sim
echo "garbage deadline" > "$SIM/run/celebrate.deadline"
sim --duration 20 --miners "$M1" --force
t_end=$(last_effect_t "$M1")
if (( t_end <= 20 + 20 )); then pass "an unreadable deadline falls back to the run's own end (last effect ${t_end}s)"; else fail "an unreadable deadline falls back to the run's own end" "${t_end}s"; fi
new_sim
# A record whose owner is gone on the same boot (killed with SIGKILL).
printf 'format=1\nid=k\nboot=boot-A\npid=999999\nattempts=0\nminer=%s -\nend=1\n' "$M1" > "$(state_file)"
echo "3-100-90-0-255-0" > "$SIM/led.$M1"
sim --recover
check "same boot, owner gone: miner darkened" "$(led_of $M1)" "$OFF"
check "verdict logged as orphaned" "$(logged '(orphaned)')" "yes"

# ─────────────────────────────────────────────────────────────────────────────
log_test "10. a live owner's record is never touched by recovery"
new_sim
bash -c 'exec -a fake-block-celebrate sleep 60' & OWNER=$!; BG_PIDS+=("$OWNER")
command sleep 0.3
printf 'format=1\nid=o\nboot=boot-A\npid=%s\nattempts=0\nminer=%s -\nend=1\n' "$OWNER" "$M1" > "$(state_file)"
echo "4-100-90-0-0-0" > "$SIM/led.$M1"
: > "$SIM/history"
sim --recover
check "LED left to its owner" "$(led_of $M1)" "4-100-90-0-0-0"
check "record left in place" "$([[ -f "$(state_file)" ]] && echo kept || echo gone)" "kept"
kill "$OWNER" 2>/dev/null; wait "$OWNER" 2>/dev/null

# ─────────────────────────────────────────────────────────────────────────────
log_test "11. repeated initialization: concurrent launches start exactly one celebration"
new_sim
for i in 1 2 3 4 5; do
    SIM_RUN_STUB=2 sim --duration 60 --miners "$M1" --force --block "B$i" &
    BG_PIDS+=($!)
done
wait
check "exactly one process ran the celebration" "$(wc -l < "$SIM/runners" 2>/dev/null | tr -d ' ')" "1"
check "the other four joined it" "$(grep -c 'Celebration already running' "$SIM/celebrate.log")" "4"
check "nothing left behind" "$(nothing_left)" "none"

new_sim
SIM_RUN_STUB=4 sim --duration 60 --miners "$M1" --force --block A1 &
OWNER_RUN=$!; BG_PIDS+=("$OWNER_RUN")
command sleep 1.5
for b in A2 A3 A4; do sim --duration 60 --miners "$M1" --force --block "$b"; done
check "re-triggers while running record every block" "$(sort "$SIM/run/celebrate.blocks" 2>/dev/null | tr '\n' ' ')" "A1 A2 A3 A4 "
read -r _w _m _b < "$SIM/run/celebrate.deadline" 2>/dev/null
check "deadline is in the monotonic format, tagged with this boot" "${_b:-}" "boot-A"
wait "$OWNER_RUN"
check "still exactly one runner" "$(wc -l < "$SIM/runners" | tr -d ' ')" "1"

# ─────────────────────────────────────────────────────────────────────────────
log_test "12. repeated power cycles in one celebration"
new_sim
printf '10 20\n30 40\n55 70\n' > "$SIM/down.$M1"
sim --duration 80 --miners "$M1 $M2" --force --block H1
check "$M1 ends dark after three power cycles" "$(led_of $M1)" "$OFF"
check "$M2 ends dark" "$(led_of $M2)" "$OFF"
check "nothing left behind" "$(nothing_left)" "none"
new_sim
# ...and across repeated host reboots while the miner never answers.
printf 'format=1\nid=r\nboot=boot-A\npid=1\nattempts=0\nminer=%s -\nend=1\n' "$M1" > "$(state_file)"
echo "0 100000000" > "$SIM/down.$M1"
for b in boot-B boot-C boot-D; do echo "$b" > "$SIM/boot_id"; sim --recover; done
check "three reboots, miner never up: record kept, 3 attempts counted" "$(grep '^attempts=' "$(state_file)")" "attempts=3"
rm -f "$SIM/down.$M1"
echo "boot-E" > "$SIM/boot_id"; sim --recover
check "miner finally up: darkened and record cleared" "$(led_of $M1)|$(nothing_left)" "$OFF|none"

# ─────────────────────────────────────────────────────────────────────────────
log_test "13. clock changes and timer precision"
new_sim
echo 20 > "$SIM/wall_jump_at"      # wall clock steps back 3 hours at t=20s
sim --duration 60 --miners "$M1" --force
t_end=$(last_effect_t "$M1")
if (( t_end <= 60 + 20 )); then pass "a 3h backwards wall-clock step does not stretch the celebration (last effect ${t_end}s)"; else fail "a backwards wall-clock step does not stretch the celebration" "last effect ${t_end}s"; fi
check "and it ends dark" "$(led_of $M1)" "$OFF"

new_sim
MAX_SECONDS=60
touch "$SIM/keep_extending"         # every write pushes the deadline 55s ahead
sim --duration 50 --miners "$M1" --force
MAX_SECONDS=86400
t_end=$(last_effect_t "$M1")
if (( t_end >= 50 && t_end <= 60 + 20 )); then pass "endless extensions are cut off by the ceiling (last effect ${t_end}s)"; else fail "endless extensions are cut off by the ceiling" "last effect ${t_end}s"; fi
check "ceiling hit is logged" "$(logged 'ceiling')" "yes"
check "and it ends dark" "$(led_of $M1)" "$OFF"

new_sim
MAX_SECONDS=60
sim --duration 999999 --miners "$M1" --force
MAX_SECONDS=86400
check "an oversized --duration is clamped to the ceiling" "$(logged 'clamping')" "yes"
t_end=$(last_effect_t "$M1")
if (( t_end <= 60 + 20 )); then pass "and the run honours it (last effect ${t_end}s)"; else fail "and the run honours it" "${t_end}s"; fi

new_sim
echo "$(( 1800000000 + 999999 )) 999999 boot-A" > "$SIM/run/celebrate.deadline"
sim --duration 20 --miners "$M1" --force
t_end=$(last_effect_t "$M1")
if (( t_end <= 20 + 20 )); then pass "a deadline further away than the ceiling is not trusted (last effect ${t_end}s)"; else fail "a deadline further away than the ceiling is not trusted" "${t_end}s"; fi

# ─────────────────────────────────────────────────────────────────────────────
log_test "14. led_idle_state=restore survives a power cut too"
new_sim
with_policy restore
printf 'format=1\nid=p\nboot=boot-A\npid=1\nattempts=0\nminer=%s 1-100-50-0-0-255\nminer=%s -\nend=1\n' "$M1" "$M2" > "$(state_file)"
echo "4-100-90-0-0-0" > "$SIM/led.$M1"; echo "4-100-90-0-0-0" > "$SIM/led.$M2"
echo "boot-B" > "$SIM/boot_id"
sim_home --recover
check "$M1 gets its saved pre-celebration state back" "$(led_of $M1)" "1-100-50-0-0-255"
check "$M2 had no saved state: left as-is rather than guessed" "$(led_of $M2)" "4-100-90-0-0-0"
check "record cleared" "$(nothing_left)" "none"

new_sim
with_policy restore
# A new block after a power cut: stale lights are restored BEFORE the new run
# captures "original" states — or it would capture a celebration colour.
printf 'format=1\nid=q\nboot=boot-A\npid=1\nattempts=0\nminer=%s 1-100-50-0-0-255\nend=1\n' "$M1" > "$(state_file)"
echo "4-100-90-0-0-0" > "$SIM/led.$M1"
echo "boot-B" > "$SIM/boot_id"
sim_home --duration 20 --miners "$M1" --force
check "the new run ends on the true original, not the stale effect" "$(led_of $M1)" "1-100-50-0-0-255"

echo ""
echo "==========================================================="
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "==========================================================="
[[ $FAILED -eq 0 ]] || exit 1
exit 0
