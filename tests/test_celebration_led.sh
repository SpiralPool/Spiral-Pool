#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool — Block Celebration LED Tests
# =============================================================================
# Covers the two parts of block-celebrate.sh that decide what a miner's LED does
# when nothing is celebrating, and which devices are eligible to celebrate at
# all. Both failed silently in the field: a wrong answer here does not raise an
# error, it leaves an LED lit for hours or discovers zero miners.
#
# Regressions pinned here:
#   1. is_cgminer probed with the plain-text "version" command and required the
#      reply to contain "CGMiner". Avalon MM firmware (Nano3s / MM319, cgminer
#      4.11.1) answers that with Code=14 "Invalid command" while answering the
#      JSON dialect normally — and it accepts plain-text ascset, so the device is
#      fully drivable. A subnet scan therefore discovered none of them.
#   2. The end of a celebration always restored the pre-celebration LED state,
#      with no way to ask for the LED to simply go dark between blocks.
#
# The script guards its main() behind a BASH_SOURCE check, so it can be sourced
# here and its functions called directly with the CGMiner API stubbed out.
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "$SCRIPT_DIR/../scripts/linux/block-celebrate.sh"
trap - EXIT  # drop the script's own cleanup trap; nothing here owns LEDs or locks

FAIL=0
assert() {
    if [[ "$2" == "$3" ]]; then
        echo "  PASS  $1"
    else
        echo "  FAIL  $1"
        echo "        want: [$3]"
        echo "        got : [$2]"
        FAIL=1
    fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Does the restore report what the miner actually did?
#
# This section runs FIRST because it needs the real set_led — the sections below
# replace it with a recorder, and once replaced there is no getting it back.
# Only cgminer_cmd is stubbed here, so the reply-checking path is the real one.
#
# `set_led` returns 0 whatever the miner answers, deliberately: the script runs
# under `set -e` and a transient nc timeout must not abort a multi-hour
# celebration. The restore then logged "LED off on <ip>" on the next line without
# consulting the reply, so a miner that refused the command was recorded as
# restored. On 18 September that produced a log reading
#
#   21:22:14  192.168.1.14 rejected ledset (reply: no response)
#   21:32:25  LED off on 192.168.1.14
#   21:32:25  CELEBRATION COMPLETE - LEDs RESTORED
#
# for a rig that stayed lit in celebration colours for another ten hours and had
# to be cleared by hand. Nothing checks the LEDs afterwards and nobody is
# watching the miners at the time, so the log is the only evidence there is.
# ─────────────────────────────────────────────────────────────────────────────

echo "restore_idle_led — does it report what the miner actually did?"

LED_ACCEPT="STATUS=I,When=0,Code=118,Msg=ASC 0 set info 'led set ok',Description=cgminer"
LOGS=()
log()         { LOGS+=("LOG $*"); }
log_error()   { LOGS+=("ERR $*"); }
log_success() { LOGS+=("OK $*"); }
cgminer_cmd() { printf '%s' "${LED_REPLY}"; }

# reset <miner reply> [warned]
reset_led_case() {
    LED_REPLY="$1"
    LED_IDLE_STATE=off
    LED_LAST_ACCEPTED=0
    LOGS=()
    if [[ "${2:-}" == "warned" ]]; then LED_WARNED=1; else unset LED_WARNED 2>/dev/null || true; fi
}
said() {
    local l
    for l in "${LOGS[@]:-}"; do [[ "$l" == *"$1"* ]] && return 0; done
    return 1
}
said_yn() { if said "$1"; then echo yes; else echo no; fi; }

reset_led_case "$LED_ACCEPT"
restore_idle_led 10.0.0.1 ""
assert "accepted: logs that the LED went off" "$(said_yn 'LOG LED off on 10.0.0.1')" "yes"
assert "accepted: raises nothing"             "$(said_yn 'ERR ')"                    "no"

reset_led_case ""
restore_idle_led 10.0.0.1 ""
assert "refused: does NOT claim the LED went off" "$(said_yn 'LOG LED off on 10.0.0.1')"              "no"
assert "refused: says so, and names the miner"    "$(said_yn 'ERR 10.0.0.1 did not accept')"          "yes"
assert "refused: prints the manual clear command" "$(said_yn 'ledset,0-0-0-0-0-0')"                   "yes"

# The 18 September case exactly: the miner began refusing ten minutes before the
# end, which set LED_WARNED, so every later refusal — including the restore's own
# — was silent. If LED_WARNED were the only mechanism, this would fail.
reset_led_case "" warned
restore_idle_led 10.0.0.1 ""
assert "refused after LED_WARNED already fired: still reported" "$(said_yn 'ERR 10.0.0.1 did not accept')" "yes"
assert "refused after LED_WARNED already fired: no false success" "$(said_yn 'LOG LED off on 10.0.0.1')"   "no"

# The inverse: an earlier refusal must not condemn a restore the miner accepted.
reset_led_case "$LED_ACCEPT" warned
restore_idle_led 10.0.0.1 ""
assert "accepted after an earlier refusal: reported honestly" "$(said_yn 'LOG LED off on 10.0.0.1')" "yes"

reset_led_case "$LED_ACCEPT"; LED_IDLE_STATE=restore
restore_idle_led 10.0.0.1 "1-2-3-4-5-6"
assert "restore-to-saved, accepted: logs Restored" "$(said_yn 'LOG Restored LED on 10.0.0.1')" "yes"

reset_led_case ""; LED_IDLE_STATE=restore
restore_idle_led 10.0.0.1 "1-2-3-4-5-6"
assert "restore-to-saved, refused: no false success" "$(said_yn 'LOG Restored LED on 10.0.0.1')" "no"

# The reason set_led returns 0 at all. A two-hour celebration cannot die on one
# dropped packet, and that must stay true now the reply is being inspected.
reset_led_case ""
if set_led 10.0.0.1 0 0 0 0 0 0; then r=yes; else r=no; fi
assert "a refused write still returns 0"        "$r"                  "yes"
assert "and records that it was refused"        "$LED_LAST_ACCEPTED"  "0"
reset_led_case "$LED_ACCEPT"
set_led 10.0.0.1 0 0 0 0 0 0
assert "and records an accepted write"          "$LED_LAST_ACCEPTED"  "1"

# ─────────────────────────────────────────────────────────────────────────────
# Everything below records set_led's arguments instead of calling it, so the
# real function — and its reply checking — is gone from here on.
# ─────────────────────────────────────────────────────────────────────────────
CALLS=()
set_led() { CALLS+=("$*"); return 0; }
log() { :; }
log_error() { :; }
log_success() { :; }

echo "restore_idle_led — what the LED shows between blocks"

# led_idle_state=off must win over a known saved state. This is the whole point
# of the setting: the operator wants dark miners, not the colour they had before.
LED_IDLE_STATE=off; CALLS=()
restore_idle_led 10.0.0.1 "3-100-90-0-255-255"
assert "off: LED off even when a state was saved" "${CALLS[*]:-}" "10.0.0.1 0 0 0 0 0 0"

# With "off" there is nothing to know, so an uncaptured state is not a reason to
# skip — unlike the restore path below.
LED_IDLE_STATE=off; CALLS=()
restore_idle_led 10.0.0.1 ""
assert "off: LED off when no state was saved" "${CALLS[*]:-}" "10.0.0.1 0 0 0 0 0 0"

LED_IDLE_STATE=restore; CALLS=()
restore_idle_led 10.0.0.1 "3-100-90-0-255-255"
assert "restore: puts the saved state back" "${CALLS[*]:-}" "10.0.0.1 3 100 90 0 255 255"

# Guessing here is what used to switch LEDs off by accident: the old fallback
# was "0-100-50-0-253-255", whose leading 0 is mode OFF, not a colour.
LED_IDLE_STATE=restore; CALLS=()
restore_idle_led 10.0.0.1 ""
assert "restore: unknown state left untouched" "${#CALLS[@]}" "0"

echo "is_cgminer — which devices are eligible to celebrate"

cgminer_cmd() { [[ "$2" == "version" ]] && echo 'STATUS=S|VERSION,CGMiner=4.9.0|' || echo ''; }
if is_cgminer 10.0.0.1; then r=yes; else r=no; fi
assert "plain-text version reply is accepted" "$r" "yes"

# The field case. Fails against the pre-fix probe, which saw only the Code=14.
cgminer_cmd() {
    if [[ "$2" == "version" ]]; then
        echo 'STATUS=E,When=1,Code=14,Msg=Invalid command|'
    elif [[ "$2" == *'"command"'*'version'* ]]; then
        echo '{"VERSION":[{"CGMiner":"4.11.1","PROD":"Avalon Nano3s"}]}'
    else
        echo ''
    fi
}
if is_cgminer 10.0.0.1; then r=yes; else r=no; fi
assert "Avalon Nano3s: plain rejected, JSON accepted" "$r" "yes"

cgminer_cmd() { echo ''; }
if is_cgminer 10.0.0.1; then r=yes; else r=no; fi
assert "a host that answers nothing is not a miner" "$r" "no"

echo "get_led_state — reading the state a restore would put back"

REAL_STATS='{"STATS":[{"MM ID0":"Ver[Nano3s] LED[0-3] LEDUser[3-100-90-0-255-255] LcdOnoff[1]"}]}'

cgminer_cmd() { [[ "$2" == "stats" ]] && echo 'MM ID0[Ver[x] LEDUser[1-100-50-255-255-255] Lcd[1]]' || echo ''; }
assert "plain-text stats is read" "$(get_led_state 10.0.0.1)" "1-100-50-255-255-255"

# The field case, and the one that stranded a real miner: the celebration ended,
# found no saved state, and declined to guess — leaving the LED lit for hours.
cgminer_cmd() {
    if [[ "$2" == "stats" ]]; then
        echo 'STATUS=E,When=1,Code=14,Msg=Invalid command|'
    elif [[ "$2" == *'"command"'*'stats'* ]]; then
        echo "$REAL_STATS"
    else
        echo ''
    fi
}
assert "Avalon Nano3s: plain rejected, JSON read" "$(get_led_state 10.0.0.1)" "3-100-90-0-255-255"

# A miner that genuinely has no LEDUser field must still report nothing, so the
# restore path leaves it alone rather than inventing mode 0.
cgminer_cmd() { echo 'MM ID0[Ver[x] LcdOnoff[1]]'; }
assert "no LEDUser in either dialect stays empty" "$(get_led_state 10.0.0.1)" ""

echo
echo "effect sequences — the colours and modes each one actually writes"
# These seven functions are the celebration itself: everything above decides
# WHETHER to light a miner, and these decide what it looks like. They had no
# coverage at all, and they are the part where a mistake is completely silent —
# the script succeeds, the miner accepts the command, and the only symptom is an
# LED doing the wrong thing in someone's garage, which nobody is watching at the
# moment a block lands.
#
# The mode number is the whole risk surface. set_led's second argument selects
# 1=solid, 2=flash, 3=pulse/breathe, 4=rainbow, and its fourth is speed. Two
# pairs are one digit apart and mean visibly different things: solid_color(1)
# against flash_color(2), and pulse_color(speed 30) against fast_pulse(speed 90),
# which share mode 3 and differ only in that number. Swapping either pair is
# invisible to every other check in this file.

# The effects sleep between writes. Stub it so the suite stays instant, and
# record the durations, since "how long does it hold" is half of what an effect
# is. Defined after the tests above, which use no sleeps.
SLEEPS=()
sleep() { SLEEPS+=("$1"); return 0; }

run_effect() { CALLS=(); SLEEPS=(); "$@"; }
led_calls() { local IFS='|'; echo "${CALLS[*]}"; }
led_sleeps() { local IFS='|'; echo "${SLEEPS[*]}"; }

run_effect flash_color 10.0.0.1 255 0 0
assert "flash_color writes mode 2"          "$(led_calls)"  "10.0.0.1 2 100 20 255 0 0"
assert "flash_color holds for 5s by default" "$(led_sleeps)" "5"

run_effect pulse_color 10.0.0.1 0 255 0
assert "pulse_color writes mode 3 at speed 30" "$(led_calls)" "10.0.0.1 3 100 30 0 255 0"

run_effect solid_color 10.0.0.1 0 0 255
assert "solid_color writes mode 1"          "$(led_calls)"  "10.0.0.1 1 100 50 0 0 255"

# Same mode as pulse_color; only the speed separates them.
run_effect fast_pulse 10.0.0.1 255 215 0
assert "fast_pulse writes mode 3 at speed 90" "$(led_calls)"  "10.0.0.1 3 100 90 255 215 0"
assert "fast_pulse holds for 3s by default"   "$(led_sleeps)" "3"

# Rainbow is generated by the firmware, so the RGB arguments are deliberately
# zero — passing a colour here would imply one is honoured when it is not.
run_effect rainbow_loop 10.0.0.1
assert "rainbow_loop writes mode 4 with no colour" "$(led_calls)"  "10.0.0.1 4 100 30 0 0 0"
assert "rainbow_loop runs 10s by default"          "$(led_sleeps)" "10"

# An explicit duration must reach sleep rather than the default.
run_effect solid_color 10.0.0.1 1 2 3 12
assert "an explicit duration is honoured" "$(led_sleeps)" "12"

# strobe_two alternates two colours: 2 writes per cycle, always ending on the
# second colour, so a miner is never left holding colour one.
run_effect strobe_two 10.0.0.1 255 0 0  0 0 255 3
assert "strobe_two n=3 alternates six times" "$(led_calls)" \
    "10.0.0.1 1 100 50 255 0 0|10.0.0.1 1 100 50 0 0 255|10.0.0.1 1 100 50 255 0 0|10.0.0.1 1 100 50 0 0 255|10.0.0.1 1 100 50 255 0 0|10.0.0.1 1 100 50 0 0 255"
assert "strobe_two sleeps 0.3 between each"  "$(led_sleeps)" "0.3|0.3|0.3|0.3|0.3|0.3"

run_effect strobe_two 10.0.0.1 1 1 1  2 2 2
assert "strobe_two defaults to 4 cycles (8 writes)" "${#CALLS[@]}" "8"

# color_chase walks the spectrum in a fixed order. The order is the effect: a
# reshuffle still writes eight valid colours and still looks like nothing wrong.
run_effect color_chase 10.0.0.1 0.1
assert "color_chase walks red→pink in order" "$(led_calls)" \
    "10.0.0.1 1 100 50 255 0 0|10.0.0.1 1 100 50 255 128 0|10.0.0.1 1 100 50 255 255 0|10.0.0.1 1 100 50 0 255 0|10.0.0.1 1 100 50 0 255 255|10.0.0.1 1 100 50 0 0 255|10.0.0.1 1 100 50 128 0 255|10.0.0.1 1 100 50 255 0 128"
assert "color_chase honours its delay on all 8 steps" "$(led_sleeps)" "0.1|0.1|0.1|0.1|0.1|0.1|0.1|0.1"

run_effect color_chase 10.0.0.1
assert "color_chase defaults to a 0.4s delay" "$(led_sleeps)" "0.4|0.4|0.4|0.4|0.4|0.4|0.4|0.4"

# Every effect drives the address it was handed. A hard-coded or dropped IP
# would light the wrong miner, or none, without failing.
_wrong_ip=0
for _e in "flash_color 192.168.7.9 1 2 3" "pulse_color 192.168.7.9 1 2 3" \
          "solid_color 192.168.7.9 1 2 3" "fast_pulse 192.168.7.9 1 2 3" \
          "rainbow_loop 192.168.7.9" "color_chase 192.168.7.9 0.1" \
          "strobe_two 192.168.7.9 1 1 1 2 2 2 1"; do
    # shellcheck disable=SC2086
    run_effect $_e
    for _c in "${CALLS[@]}"; do
        [[ "$_c" == "192.168.7.9 "* ]] || _wrong_ip=1
    done
done
assert "all seven effects address the miner they were given" "$_wrong_ip" "0"

# Brightness is the one parameter every effect shares, and a zero would look
# exactly like a dead miner.
_bad_brightness=0
for _e in "flash_color 10.0.0.1 1 2 3" "pulse_color 10.0.0.1 1 2 3" \
          "solid_color 10.0.0.1 1 2 3" "fast_pulse 10.0.0.1 1 2 3" \
          "rainbow_loop 10.0.0.1" "color_chase 10.0.0.1 0.1" \
          "strobe_two 10.0.0.1 1 1 1 2 2 2 1"; do
    # shellcheck disable=SC2086
    run_effect $_e
    for _c in "${CALLS[@]}"; do
        [[ "$(echo "$_c" | awk '{print $3}')" == "100" ]] || _bad_brightness=1
    done
done
assert "every effect writes full brightness" "$_bad_brightness" "0"

echo
if [[ $FAIL -eq 0 ]]; then
    echo "ALL CELEBRATION LED TESTS PASSED"
else
    echo "CELEBRATION LED TESTS FAILED"
fi
exit $FAIL
