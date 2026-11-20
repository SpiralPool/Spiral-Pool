#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#===============================================================================
# SPIRAL POOL - BLOCK FOUND CELEBRATION
#===============================================================================
# Triggers LED celebration on all CGMiner-compatible miners (Avalon Nano 3s)
# when a block is found by any miner on the pool.
#
# Usage:
#   block-celebrate.sh [--test] [--duration SECONDS] [--miners "IP1 IP2 ..."]
#                      [--block HASH] [--stop [--block HASH]]
#
# Called automatically by Sentinel when a block is found.
#===============================================================================

set -euo pipefail

# Configuration
CELEBRATION_DURATION="${CELEBRATION_DURATION:-7200}"  # 2 hour default (matches config.yaml)
CGMINER_PORT=4028
NC_TIMEOUT=2
# Runtime directory (tmpfs: cleared on every boot). Overridable for tests.
RUN_DIR="${CELEBRATE_RUN_DIR:-/run/spiralpool}"
MINER_CACHE_FILE="$RUN_DIR/cgminers.cache"
MINER_CACHE_TTL=3600  # 1 hour cache for miner discovery
LOCK_FILE="$RUN_DIR/celebrate.lock"
DEADLINE_FILE="$RUN_DIR/celebrate.deadline"
# The block hashes this celebration is for, one per line. A celebration is
# started by a block and extended by later ones, so stopping it correctly means
# knowing which blocks it still stands for: an orphaned block withdraws its own
# entry, and the LEDs only go dark once no block is left celebrating.
BLOCKS_FILE="$RUN_DIR/celebrate.blocks"
MINER_DB_FILE="/spiralpool/data/miners.json"
CELEBRATE_LOG="/spiralpool/logs/celebrate.log"

#-------------------------------------------------------------------------------
# Power-cycle recovery (v3.0.3)
#-------------------------------------------------------------------------------
# An Avalon keeps the last LED effect it was sent and shows it again after it
# boots. Everything above lives in /run (tmpfs) and in this process, so a power
# cut took the celebration's whole memory with it: nobody was left to send the
# final "LED off", and the miners came back up still strobing and kept at it
# for hours. A miner that was itself rebooting at the moment the single restore
# write was due ended the same way.
#
# STATE_FILE is the durable half: written to disk BEFORE the first LED write,
# removed only once every miner has confirmed its restore. Whatever finds it
# without a live owner returns those miners to idle (see recover_stale_celebration).
# A celebration is never resumed from it after the host restarts — the record
# exists to switch the lights off, not to keep them on.
STATE_FILE="${CELEBRATE_STATE_FILE:-/spiralpool/data/celebrate.state}"
# Identifies this boot. A record written under a different boot id belongs to a
# process that cannot still be running, whatever PID it names.
BOOT_ID_FILE="${CELEBRATE_BOOT_ID_FILE:-/proc/sys/kernel/random/boot_id}"
# Hard ceiling on one process's celebration, extensions included. The deadline
# is the normal stop; this guarantees no corrupted deadline, clock step or bug
# can keep a celebration going indefinitely. A block that lands after it ends
# simply starts a new celebration.
CELEBRATION_MAX_SECONDS="${CELEBRATION_MAX_SECONDS:-86400}"
# How long the end of a celebration keeps retrying a miner that did not take its
# restore (it is usually mid-reboot), and how often.
RESTORE_RETRY_WINDOW="${RESTORE_RETRY_WINDOW:-300}"
RESTORE_RETRY_INTERVAL="${RESTORE_RETRY_INTERVAL:-15}"
# Recovery passes before a miner that never answers is dropped from the record.
# Sentinel runs one every 5 minutes while a record exists, so 288 is ~24 hours.
RECOVERY_MAX_ATTEMPTS="${RECOVERY_MAX_ATTEMPTS:-288}"
[[ "$CELEBRATION_MAX_SECONDS" =~ ^[1-9][0-9]*$ ]] || CELEBRATION_MAX_SECONDS=86400
[[ "$RESTORE_RETRY_WINDOW" =~ ^[0-9]+$ ]]          || RESTORE_RETRY_WINDOW=300
[[ "$RESTORE_RETRY_INTERVAL" =~ ^[1-9][0-9]*$ ]]   || RESTORE_RETRY_INTERVAL=15
[[ "$RECOVERY_MAX_ATTEMPTS" =~ ^[1-9][0-9]*$ ]]    || RECOVERY_MAX_ATTEMPTS=288

# Track child PIDs for cleanup on termination
declare -a CHILD_PIDS=()
declare -A SAVED_STATES=()  # ip -> led state, for cleanup on signal
# Set once this process has written STATE_FILE; only the writer finalizes it.
STATE_OWNED=0
# Per-run directory where each miner's subshell marks a confirmed restore.
RESULT_DIR=""
# Outcome of the last restore_idle_led call: 1 = done (accepted, or nothing to
# do), 0 = the miner did not take it. A variable, not a return code, because
# callers run under `set -e` and a refused restore must never abort them.
LED_RESTORE_OK=0

#-------------------------------------------------------------------------------
# Clocks and process identity
#-------------------------------------------------------------------------------
_wall_now() { date +%s; }

# Seconds since boot. Immune to wall-clock steps (NTP correcting a host that
# booted without an RTC, an operator setting the date), which is what every
# in-run comparison uses. Falls back to the wall clock where /proc is absent.
_mono_now() {
    local up=""
    if read -r up _ 2>/dev/null < /proc/uptime && [[ "${up%%.*}" =~ ^[0-9]+$ ]]; then
        echo "${up%%.*}"
    else
        date +%s
    fi
}

_boot_id() {
    local id=""
    id=$(tr -d '[:space:]' 2>/dev/null < "$BOOT_ID_FILE") || id=""
    [[ "$id" =~ ^[A-Za-z0-9-]+$ ]] || id="unknown"
    echo "$id"
}

# Is <pid> a live block-celebrate process? kill -0 alone is not enough: a lock
# left by a process that never ran its cleanup (power cut, SIGKILL) names a PID
# the kernel will eventually hand to something else, and --stop would then send
# TERM to an unrelated process, and a new block would "extend" a celebration
# that is not running. Where /proc is readable the command line must name this
# script; where it is not (hidepid, non-Linux) kill -0 is all there is.
_is_celebrate_pid() {
    local pid="${1:-}" cmd=""
    [[ "$pid" =~ ^[0-9]+$ ]] || return 1
    kill -0 "$pid" 2>/dev/null || return 1
    if [[ -r "/proc/$pid/cmdline" ]]; then
        cmd=$(tr '\0' ' ' 2>/dev/null < "/proc/$pid/cmdline") || cmd=""
        [[ -z "$cmd" || "$cmd" == *celebrate* ]] || return 1
    fi
    return 0
}

# Record a block as one this celebration stands for. Idempotent: the same block
# reaches this script from the stratum, from Sentinel and sometimes by hand.
record_block() {
    local hash="${1:-}"
    [[ -z "$hash" ]] && return 0
    touch "$BLOCKS_FILE" 2>/dev/null || return 0
    grep -qxF "$hash" "$BLOCKS_FILE" 2>/dev/null || echo "$hash" >> "$BLOCKS_FILE"
}

# Stop a running celebration, optionally only on behalf of one block.
#
# Without --block this is an unconditional stop (an operator saying "enough").
# With --block it withdraws that block and stops only if it was the last one
# standing, so a block that orphans cannot switch the lights off on a different
# block that is still perfectly good.
#
# A celebration with no recorded blocks is left alone: it was started by a
# caller that did not name one, and cancelling on an unprovable attribution is
# worse than leaving the LEDs on for their remaining time.
stop_celebration() {
    local hash="${1:-}"
    local pid remaining

    if [[ ! -f "$LOCK_FILE" ]]; then
        log "No celebration is running"
        return 0
    fi
    pid=$(cat "$LOCK_FILE" 2>/dev/null || true)
    if ! _is_celebrate_pid "$pid"; then
        log "No celebration is running (removing stale lock)"
        rm -f "$LOCK_FILE" "$DEADLINE_FILE" "$BLOCKS_FILE"
        return 0
    fi

    if [[ -n "$hash" ]]; then
        if [[ ! -s "$BLOCKS_FILE" ]]; then
            log "Celebration (PID $pid) is not attributed to any block — leaving it running"
            return 0
        fi
        if ! grep -qxF "$hash" "$BLOCKS_FILE" 2>/dev/null; then
            log "Celebration (PID $pid) is not for block $hash — leaving it running"
            return 0
        fi
        grep -vxF "$hash" "$BLOCKS_FILE" > "${BLOCKS_FILE}.tmp" 2>/dev/null || true
        mv -f "${BLOCKS_FILE}.tmp" "$BLOCKS_FILE" 2>/dev/null || true
        if [[ -s "$BLOCKS_FILE" ]]; then
            remaining=$(grep -c . "$BLOCKS_FILE" 2>/dev/null) || remaining=0
            log "Block $hash withdrawn — $remaining other block(s) still celebrating"
            return 0
        fi
        log "Block $hash withdrawn and it was the last one — stopping celebration"
    fi

    # TERM, not KILL: the incumbent traps it, restores every LED it changed and
    # releases the lock. KILL would leave the miners lit in celebration colours.
    log "Stopping celebration (PID $pid)"
    kill -TERM "$pid" 2>/dev/null || true
    return 0
}

#-------------------------------------------------------------------------------
# Durable celebration record (STATE_FILE)
#-------------------------------------------------------------------------------
# Format, one key=value per line:
#   format=1
#   id=<boot>-<pid>-<mono>    unique per write; recovery only removes what it read
#   boot=<boot id>            the boot it was written under
#   pid=<pid>                 owning process; empty once no process owns it
#   started=<epoch>           informational
#   idle=<off|restore>        informational
#   attempts=<n>              recovery passes made so far
#   miner=<ip> <saved|->      one per lit miner, with its pre-celebration LED state
#   end=1                     written last: a record without it is torn
# Written to a temp file and renamed, so a reader sees the old record or the new
# one, never half of either. A file that fails to parse is treated as corrupt.

# write_state <pid> <attempts> ["<ip> <saved|->" ...]
write_state() {
    local pid="$1" attempts="$2" dir tmp entry boot
    shift 2
    dir=$(dirname "$STATE_FILE")
    [[ -d "$dir" && -w "$dir" ]] || return 1
    tmp="${STATE_FILE}.tmp.$$"
    boot=$(_boot_id)
    {
        echo "format=1"
        echo "id=${boot}-${pid:-none}-$(_mono_now)-${RANDOM}"
        echo "boot=${boot}"
        echo "pid=${pid}"
        echo "started=$(_wall_now)"
        echo "idle=${LED_IDLE_STATE}"
        echo "attempts=${attempts}"
        for entry in "$@"; do echo "miner=${entry}"; done
        echo "end=1"
    } > "$tmp" 2>/dev/null || { rm -f "$tmp"; return 1; }
    # Flush before the rename so a power cut cannot leave an empty file behind
    # the new name (GNU sync accepts a file argument; harmless if it does not).
    sync "$tmp" 2>/dev/null || true
    mv -f "$tmp" "$STATE_FILE" 2>/dev/null || { rm -f "$tmp"; return 1; }
}

# read_state — parse STATE_FILE into the STATE_* globals.
# Returns 0 well-formed, 1 missing, 2 malformed/torn.
read_state() {
    STATE_ID=""; STATE_BOOT=""; STATE_PID=""; STATE_ATTEMPTS=0
    STATE_MINERS=(); STATE_SAVED=()
    [[ -f "$STATE_FILE" ]] || return 1
    local line key val ip saved have_format=0 have_end=0
    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ -z "$line" ]] && continue
        [[ "$line" == *=* ]] || return 2
        key="${line%%=*}"; val="${line#*=}"
        case "$key" in
            format)   [[ "$val" == "1" ]] && have_format=1 ;;
            id)       STATE_ID="$val" ;;
            boot)     STATE_BOOT="$val" ;;
            pid)      STATE_PID="$val" ;;
            attempts) [[ "$val" =~ ^[0-9]+$ ]] || return 2; STATE_ATTEMPTS="$val" ;;
            miner)
                [[ "$val" == *" "* ]] || return 2
                ip="${val%% *}"; saved="${val#* }"
                [[ "$ip" =~ ^[A-Za-z0-9._:-]+$ ]] || return 2
                [[ "$saved" == "-" || "$saved" =~ ^[0-9]+(-[0-9]+){5}$ ]] || return 2
                STATE_MINERS+=("$ip"); STATE_SAVED+=("$saved")
                ;;
            end)      have_end=1 ;;
            *)        ;;  # unknown keys are ignored (forward compatibility)
        esac
    done 2>/dev/null < "$STATE_FILE"
    (( have_format && have_end )) || return 2
    [[ -z "$STATE_PID" || "$STATE_PID" =~ ^[0-9]+$ ]] || return 2
    [[ -n "$STATE_BOOT" ]] || return 2
    return 0
}

# classify_state — decide what a recorded celebration is. Sets CELEBRATION_VERDICT:
#   none      nothing recorded
#   owned     its owner is alive on this boot; the owner will restore, hands off
#   rebooted  written under another boot: its owner died with the power
#   orphaned  same boot, but the owner is gone without confirming every restore
#   corrupt   torn, truncated or malformed
# Deliberately clock-free: whether a celebration's deadline has passed never
# decides anything here, because after a power cut the wall clock is the one
# input that cannot be trusted (a host without an RTC boots in the past).
classify_state() {
    local rc=0 boot_now
    read_state || rc=$?
    if (( rc == 1 )); then CELEBRATION_VERDICT=none; return 0; fi
    if (( rc != 0 )); then CELEBRATION_VERDICT=corrupt; return 0; fi
    boot_now=$(_boot_id)
    if [[ "$STATE_BOOT" != "unknown" && "$boot_now" != "unknown" && "$STATE_BOOT" != "$boot_now" ]]; then
        CELEBRATION_VERDICT=rebooted; return 0
    fi
    # $$ excluded: a record naming this very PID is a previous holder of a
    # recycled PID, since this process has not written one yet when it asks.
    if [[ -n "$STATE_PID" && "$STATE_PID" != "$$" ]] && _is_celebrate_pid "$STATE_PID"; then
        CELEBRATION_VERDICT=owned; return 0
    fi
    CELEBRATION_VERDICT=orphaned
}

_state_id_now() {
    local line
    line=$(grep -m1 '^id=' "$STATE_FILE" 2>/dev/null) || line=""
    echo "${line#id=}"
}

# recover_stale_celebration — return to idle every miner a dead celebration left
# lit, then clear its record. A miner that does not answer stays on the record
# for the next pass. Never resumes a celebration.
recover_stale_celebration() {
    local i ip saved attempts id_before id_now
    local -a ips=() saveds=() pending=()

    classify_state
    case "$CELEBRATION_VERDICT" in
        none)  return 0 ;;
        owned) log "Celebration record belongs to running PID $STATE_PID — nothing to recover"; return 0 ;;
    esac
    id_before="$STATE_ID"

    if [[ "$CELEBRATION_VERDICT" == "corrupt" ]]; then
        # Which miners were lit is unknown, so fall back to this host's own
        # inventory. Safe with led_idle_state=off: off is their idle state anyway.
        # With "restore" there is no saved state to put back, and restore_idle_led
        # leaves them alone rather than guess.
        log_error "Celebration record $STATE_FILE is unreadable — returning this host's configured miners to idle"
        while IFS= read -r ip; do
            [[ -n "$ip" ]] && { ips+=("$ip"); saveds+=("-"); }
        done < <(load_configured_miners 2>/dev/null || true)
    else
        log "Recovering a celebration left by PID ${STATE_PID:-none} (${CELEBRATION_VERDICT}) — returning ${#STATE_MINERS[@]} miner(s) to idle"
        ips=(${STATE_MINERS[@]+"${STATE_MINERS[@]}"})
        saveds=(${STATE_SAVED[@]+"${STATE_SAVED[@]}"})
    fi

    for i in "${!ips[@]}"; do
        saved="${saveds[$i]}"
        [[ "$saved" == "-" ]] && saved=""
        restore_idle_led "${ips[$i]}" "$saved"
        [[ "$LED_RESTORE_OK" == "1" ]] || pending+=("${ips[$i]} ${saveds[$i]}")
    done

    # A fresh celebration may have started and written its own record while
    # this pass ran. Only the record that was read may be removed or rewritten.
    id_now=$(_state_id_now)
    if [[ -f "$STATE_FILE" && "$id_now" != "$id_before" ]]; then
        log "Celebration record was replaced during recovery — leaving the new one alone"
        return 0
    fi

    if (( ${#pending[@]} == 0 )); then
        rm -f "$STATE_FILE"
        log_success "Recovery complete — no miner is left in celebration colours"
        return 0
    fi

    attempts=$(( STATE_ATTEMPTS + 1 ))
    if (( attempts >= RECOVERY_MAX_ATTEMPTS )); then
        log_error "Giving up after ${attempts} recovery passes: ${pending[*]%% *} never answered. Clear by hand with: ascset|0,ledset,0-0-0-0-0-0"
        rm -f "$STATE_FILE"
        return 0
    fi
    write_state "" "$attempts" "${pending[@]}" || true
    log "${#pending[@]} miner(s) did not answer (likely still booting) — will retry (pass ${attempts}/${RECOVERY_MAX_ATTEMPTS})"
}

# finalize_state <"ip saved" ...> — the owner's last word on its record: gone if
# every miner confirmed its restore, otherwise handed over (owner-less) to
# recovery with just the miners still lit.
finalize_state() {
    (( STATE_OWNED )) || return 0
    STATE_OWNED=0
    if (( $# == 0 )); then
        rm -f "$STATE_FILE"
        return 0
    fi
    write_state "" 0 "$@" || true
    log_error "$# miner(s) did not confirm their LED restore — left on ${STATE_FILE} for the next recovery pass"
}

# Cleanup handler: restore LEDs and release lock on termination
cleanup() {
    log "Caught signal — stopping celebration and restoring LEDs..."
    # Kill all tracked child processes
    for pid in "${CHILD_PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
    # Return LEDs to their idle state. Unlike the old restore-only path this also
    # covers a miner whose pre-celebration state was never captured: with
    # led_idle_state=off there is nothing to know, the LED just goes off.
    # No 2>/dev/null here. Every log function writes to stderr, so suppressing it
    # discarded the whole of this loop's output — including a refused restore,
    # which is exactly the case an operator needs to see and the one path where
    # nobody is watching the miners at the time.
    # A miner whose own subshell already confirmed its restore is skipped; any
    # other gets one more attempt, and one that still refuses stays on the
    # durable record for recovery rather than being forgotten.
    local -a pending=()
    for ip in "${!SAVED_STATES[@]}"; do
        if [[ -n "$RESULT_DIR" && -e "$RESULT_DIR/ok.$ip" ]]; then
            continue
        fi
        restore_idle_led "$ip" "${SAVED_STATES[$ip]}" || true
        [[ "$LED_RESTORE_OK" == "1" ]] || pending+=("$ip ${SAVED_STATES[$ip]:--}")
    done
    [[ -n "$RESULT_DIR" ]] && rm -rf "$RESULT_DIR"
    finalize_state ${pending[@]+"${pending[@]}"}
    # Release lock — only if it is ours. main() exits early on several paths
    # (quiet hours, --list, --help, no miners); those must not evict a
    # celebration that another process is legitimately running.
    if [[ "$(cat "$LOCK_FILE" 2>/dev/null)" == "$$" ]]; then
        rm -f "$LOCK_FILE" "$DEADLINE_FILE" "$BLOCKS_FILE"
    fi
    log "Cleanup complete"
}
trap cleanup EXIT

# Colors for terminal output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
GOLD='\033[1;33m'
NC='\033[0m'

#===============================================================================
# QUIET HOURS — suppress LED celebration during configured quiet window
#===============================================================================
# Reads quiet_hours_start, quiet_hours_end, and display_timezone from
# Sentinel config.json. Defaults: 22:00-06:00, America/New_York.
# Called at startup (skip entirely) and periodically during celebration
# (stop early if quiet hours begin mid-celebration).

SENTINEL_CONFIG_PATHS=(
    "/spiralpool/config/sentinel/config.json"
    "${HOME}/.spiralsentinel/config.json"
)

_read_sentinel_config_val() {
    local key="$1" default="$2"
    for cfg in "${SENTINEL_CONFIG_PATHS[@]}"; do
        if [[ -f "$cfg" ]]; then
            # Simple JSON extraction — no jq dependency
            local val
            val=$(grep -o "\"${key}\"[[:space:]]*:[[:space:]]*[^,}]*" "$cfg" 2>/dev/null \
                  | head -1 | sed 's/.*:[[:space:]]*//' | tr -d '"' | tr -d ' ')
            if [[ -n "$val" ]]; then
                echo "$val"
                return
            fi
        fi
    done
    echo "$default"
}

is_quiet_hours() {
    local qs qe tz_name hour_now
    qs=$(_read_sentinel_config_val "quiet_hours_start" "22")
    qe=$(_read_sentinel_config_val "quiet_hours_end" "6")
    tz_name=$(_read_sentinel_config_val "display_timezone" "America/New_York")

    # Get current hour in the configured timezone
    hour_now=$(TZ="$tz_name" date '+%-H' 2>/dev/null || date '+%-H')

    if (( qs < qe )); then
        # Simple range (e.g., 8-18)
        (( hour_now >= qs && hour_now < qe ))
    else
        # Overnight range (e.g., 22-6)
        (( hour_now >= qs || hour_now < qe ))
    fi
}

# Idle LED state — what the LED shows when no celebration is running.
#   "off"     turn the LED off when the celebration ends (default)
#   "restore" put back whatever the LED showed before the celebration
LED_IDLE_STATE="$(_read_sentinel_config_val "led_idle_state" "off")"

#===============================================================================
# LOGGING
#===============================================================================
log() {
    echo -e "${CYAN}[CELEBRATE]${NC} $(date '+%H:%M:%S') $*" >&2
}

log_success() {
    echo -e "${GREEN}[CELEBRATE]${NC} $(date '+%H:%M:%S') $*" >&2
}

log_error() {
    echo -e "${RED}[CELEBRATE]${NC} $(date '+%H:%M:%S') $*" >&2
}

#===============================================================================
# CGMINER API FUNCTIONS
#===============================================================================

# Send command to CGMiner API
# Usage: cgminer_cmd <ip> <command>
cgminer_cmd() {
    local ip="$1"
    local cmd="$2"
    nc -w "$NC_TIMEOUT" "$ip" "$CGMINER_PORT" 2>/dev/null <<< "$cmd" | tr -d '\0' || true
}

# Check if miner is CGMiner compatible (has port 4028 open)
is_cgminer() {
    local ip="$1"
    local response
    response=$(cgminer_cmd "$ip" "version" 2>/dev/null) || true
    if [[ "$response" == *"CGMiner"* ]] || [[ "$response" == *"cgminer"* ]]; then
        return 0
    fi
    # Avalon MM firmware (Nano3s / MM319, cgminer 4.11.1) rejects the plain-text
    # "version" with Code=14 "Invalid command" but answers the JSON dialect. It
    # accepts plain-text ascset, so the device is fully drivable — it just could
    # not be identified, and a subnet scan therefore discovered nothing at all.
    response=$(cgminer_cmd "$ip" '{"command":"version"}' 2>/dev/null) || true
    [[ "$response" == *"CGMiner"* ]] || [[ "$response" == *"cgminer"* ]]
}

# Get current LED state from miner
# Returns: mode-brightness-speed-r-g-b
get_led_state() {
    local ip="$1"
    local stats

    # Extract LEDUser[mode-brightness-speed-r-g-b].
    # If the miner does not report LEDUser we do NOT invent a value: the previous
    # fallback ("0-...") restored mode 0, which is OFF, so every celebration ended
    # by darkening an LED whose original state we never knew.
    stats=$(cgminer_cmd "$ip" "stats" 2>/dev/null) || true
    if [[ "$stats" =~ LEDUser\[([0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+)\] ]]; then
        echo "${BASH_REMATCH[1]}"
        return 0
    fi

    # Avalon MM firmware answers the plain-text "stats" with Code=14 the same way
    # it does "version", so the field is simply absent rather than unsupported.
    # Reading it as "this miner has no LED state" is what left a Nano3s pulsing
    # cyan for six hours after a celebration ended and declined to guess.
    stats=$(cgminer_cmd "$ip" '{"command":"stats"}' 2>/dev/null) || true
    if [[ "$stats" =~ LEDUser\[([0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+)\] ]]; then
        echo "${BASH_REMATCH[1]}"
        return 0
    fi

    echo ""
}

# Check if miner is responsive (LED control works regardless of LCD state)
is_miner_responsive() {
    local ip="$1"
    local response
    response=$(cgminer_cmd "$ip" "version" 2>/dev/null) || return 1
    [[ -n "$response" ]]
}

# Set LED on miner
# Usage: set_led <ip> <mode> <brightness> <speed> <r> <g> <b>
set_led() {
    local ip="$1"
    local mode="$2"
    local brightness="$3"
    local speed="$4"
    local r="$5"
    local g="$6"
    local b="$7"

    local reply
    reply=$(cgminer_cmd "$ip" "ascset|0,ledset,$mode-$brightness-$speed-$r-$g-$b" 2>/dev/null)

    # A successful ascset replies STATUS=I ("led set ok"). Anything else means the
    # miner rejected the command. Warn once per miner per run rather than on every
    # call — a celebration issues thousands of these.
    # Always returns 0: the script runs under `set -e`, and a transient nc timeout
    # must not abort a multi-hour celebration. Whether this particular write was
    # accepted is recorded in LED_LAST_ACCEPTED instead, so the restore at the end
    # can report what happened rather than assume it worked. The once-per-run
    # warning is deliberately not the mechanism for that: by the time the restore
    # runs it has usually already fired, and every later refusal is silent.
    if [[ "$reply" == *"STATUS=I"* ]]; then
        LED_LAST_ACCEPTED=1
    else
        LED_LAST_ACCEPTED=0
        if [[ -z "${LED_WARNED:-}" ]]; then
            LED_WARNED=1
            log_error "$ip rejected ledset (reply: ${reply:-no response}) — LED effects will not display"
        fi
    fi
    return 0
}

# Return one miner's LED to its idle state at the end of a celebration.
# With led_idle_state=off (the default) that is mode 0. With "restore" it is the
# state captured before the celebration began, and a state that was never
# captured is left untouched rather than guessed — guessing is what used to turn
# LEDs off by accident.
# Say what the restore actually achieved, rather than what it attempted.
#
# The celebration's last act used to log "LED off on <ip>" whether or not the
# miner took the command. On 18 September that produced a log reading
#   21:22:14  192.168.1.14 rejected ledset (reply: no response)
#   21:32:25  LED off on 192.168.1.14
#   21:32:25  CELEBRATION COMPLETE - LEDs RESTORED
# for a miner that stayed lit in celebration colours for a further ten hours.
# The restore is the one write whose outcome the operator has to be able to
# trust, because nothing checks it afterwards and nobody is watching the rig at
# the time.
_report_led_restore() {
    local ip="$1" success_msg="$2"

    if [[ "${LED_LAST_ACCEPTED:-0}" == "1" ]]; then
        log "$success_msg"
        return 0
    fi
    log_error "$ip did not accept the LED restore — it may still be showing celebration colours. Clear it with: ascset|0,ledset,0-0-0-0-0-0"
    return 0
}

restore_idle_led() {
    local ip="$1" saved="$2"
    LED_RESTORE_OK=0

    if [[ "$LED_IDLE_STATE" != "restore" ]]; then
        set_led "$ip" 0 0 0 0 0 0
        _report_led_restore "$ip" "LED off on $ip"
        LED_RESTORE_OK="${LED_LAST_ACCEPTED:-0}"
        return 0
    fi

    if [[ -z "$saved" ]]; then
        log "No saved LED state for $ip — leaving LED as-is"
        LED_RESTORE_OK=1   # nothing to put back is not a failure to retry
        return 0
    fi

    local mode brightness speed r g b
    IFS='-' read -r mode brightness speed r g b <<< "$saved"
    set_led "$ip" "$mode" "$brightness" "$speed" "$r" "$g" "$b"
    _report_led_restore "$ip" "Restored LED on $ip"
    LED_RESTORE_OK="${LED_LAST_ACCEPTED:-0}"
}

# The end of a celebration is the one LED write that has to land, and it falls
# due exactly when a miner that power-cycled may still be booting. One attempt
# plus a log line is what left LEDs lit for hours. Keep trying for a bounded
# window instead; a miner still silent after it stays on the durable record and
# the next recovery pass finishes the job. Returns 0 once the restore landed.
restore_idle_led_retry() {
    local ip="$1" saved="$2" start
    start=$(_mono_now)
    while :; do
        restore_idle_led "$ip" "$saved"
        [[ "$LED_RESTORE_OK" == "1" ]] && return 0
        (( $(_mono_now) - start + RESTORE_RETRY_INTERVAL <= RESTORE_RETRY_WINDOW )) || return 1
        log "$ip did not take its LED restore (rebooting?) — retrying in ${RESTORE_RETRY_INTERVAL}s"
        sleep "$RESTORE_RETRY_INTERVAL"
    done
}

# Set LED mode (0=off, 1=solid, 2=flash, 3=pulse, 4=loop)
set_led_mode() {
    local ip="$1"
    local mode="$2"
    cgminer_cmd "$ip" "ascset|0,ledmode,$mode" >/dev/null 2>&1
}

#===============================================================================
# MINER DISCOVERY
#===============================================================================

# Discover CGMiner-compatible miners on the local network
discover_miners() {
    local miners=()
    local subnet

    # Get local subnet (assumes /24)
    subnet=$(ip route | grep -oP 'src \K[0-9.]+' | head -1 | sed 's/\.[0-9]*$/./')

    if [[ -z "$subnet" ]]; then
        log_error "Could not determine local subnet"
        return 1
    fi

    log "Scanning ${subnet}0/24 for CGMiner devices..."

    # Quick parallel scan for port 4028
    for i in $(seq 1 254); do
        (
            ip="${subnet}${i}"
            if nc -z -w 1 "$ip" "$CGMINER_PORT" 2>/dev/null; then
                if is_miner_responsive "$ip"; then
                    echo "$ip"
                fi
            fi
        ) &

        # Limit parallel connections
        if (( i % 50 == 0 )); then
            wait
        fi
    done
    wait
}

# Read this host's own miners from the shared miner database.
# Preferred over the /24 scan: when two independent pools share a LAN, a blind
# subnet scan makes every host drive every other host's miners.
load_configured_miners() {
    [[ -f "$MINER_DB_FILE" ]] || return 1

    python3 -c '
import json, sys
try:
    with open(sys.argv[1]) as f:
        db = json.load(f)
except Exception:
    sys.exit(1)
for ip in db.get("miners", {}):
    print(ip)
' "$MINER_DB_FILE" 2>/dev/null
}

# Get cached miners or discover new ones
get_miners() {
    local force_scan="${1:-false}"

    # This host's configured miners take precedence over a subnet scan
    local configured
    configured=$(load_configured_miners) || configured=""
    if [[ -n "$configured" ]]; then
        echo "$configured"
        return 0
    fi
    log "No miners in $MINER_DB_FILE — falling back to subnet scan"

    # Check cache
    if [[ "$force_scan" != "true" ]] && [[ -f "$MINER_CACHE_FILE" ]]; then
        local cache_age
        cache_age=$(( $(date +%s) - $(stat -c %Y "$MINER_CACHE_FILE" 2>/dev/null || echo 0) ))

        if (( cache_age < MINER_CACHE_TTL )); then
            cat "$MINER_CACHE_FILE"
            return 0
        fi
    fi

    # Discover and cache
    local miners
    miners=$(discover_miners)

    if [[ -n "$miners" ]]; then
        echo "$miners" > "$MINER_CACHE_FILE"
        echo "$miners"
    fi
}

#===============================================================================
# CELEBRATION SEQUENCES
#===============================================================================

# Flash a color rapidly
flash_color() {
    local ip="$1"
    local r="$2"
    local g="$3"
    local b="$4"
    local duration="${5:-5}"

    set_led "$ip" 2 100 20 "$r" "$g" "$b"
    sleep "$duration"
}

# Pulse/breathe a color
pulse_color() {
    local ip="$1"
    local r="$2"
    local g="$3"
    local b="$4"
    local duration="${5:-5}"

    set_led "$ip" 3 100 30 "$r" "$g" "$b"
    sleep "$duration"
}

# Solid color
solid_color() {
    local ip="$1"
    local r="$2"
    local g="$3"
    local b="$4"
    local duration="${5:-5}"

    set_led "$ip" 1 100 50 "$r" "$g" "$b"
    sleep "$duration"
}

# Rainbow loop mode
rainbow_loop() {
    local ip="$1"
    local duration="${2:-10}"

    set_led "$ip" 4 100 30 0 0 0
    sleep "$duration"
}

# Rapid strobe between two colors
strobe_two() {
    local ip="$1"
    local r1="$2" g1="$3" b1="$4"
    local r2="$5" g2="$6" b2="$7"
    local cycles="${8:-4}"

    for ((i=0; i<cycles; i++)); do
        set_led "$ip" 1 100 50 "$r1" "$g1" "$b1"
        sleep 0.3
        set_led "$ip" 1 100 50 "$r2" "$g2" "$b2"
        sleep 0.3
    done
}

# Rapid color chase through the spectrum
color_chase() {
    local ip="$1"
    local delay="${2:-0.4}"

    # Red -> Orange -> Yellow -> Green -> Cyan -> Blue -> Purple -> Pink
    set_led "$ip" 1 100 50 255 0 0;     sleep "$delay"
    set_led "$ip" 1 100 50 255 128 0;   sleep "$delay"
    set_led "$ip" 1 100 50 255 255 0;   sleep "$delay"
    set_led "$ip" 1 100 50 0 255 0;     sleep "$delay"
    set_led "$ip" 1 100 50 0 255 255;   sleep "$delay"
    set_led "$ip" 1 100 50 0 0 255;     sleep "$delay"
    set_led "$ip" 1 100 50 128 0 255;   sleep "$delay"
    set_led "$ip" 1 100 50 255 0 128;   sleep "$delay"
}

# Fast pulse at high speed
fast_pulse() {
    local ip="$1"
    local r="$2" g="$3" b="$4"
    local duration="${5:-3}"

    set_led "$ip" 3 100 90 "$r" "$g" "$b"
    sleep "$duration"
}

# The shared celebration deadline. A second block landing mid-celebration
# extends it instead of killing and restarting the run, which is what used to
# leave LEDs on a stale colour.
#
# Format: "<wall epoch> <monotonic seconds> <boot id>". Every comparison uses the
# monotonic value, so a wall-clock step can neither stretch a celebration nor
# cut it short; the wall value is for humans. A bare epoch (the format written
# by 3.0.2 and earlier) is still understood. Written via rename, so a reader
# never sees a half-written deadline.
write_deadline() {
    local mono="$1" now_m tmp
    now_m=$(_mono_now)
    tmp="${DEADLINE_FILE}.tmp.$$"
    if echo "$(( $(_wall_now) + mono - now_m )) ${mono} $(_boot_id)" > "$tmp" 2>/dev/null; then
        mv -f "$tmp" "$DEADLINE_FILE" 2>/dev/null || rm -f "$tmp"
    fi
}

# read_deadline <fallback> — the effective deadline on the monotonic clock.
# Anything unreadable, from another boot, or further away than
# CELEBRATION_MAX_SECONDS is not a deadline this run will honour; it falls back
# to its own end time instead.
read_deadline() {
    local fallback="$1" wall="" mono="" boot="" extra="" now_m rem
    now_m=$(_mono_now)
    if read -r wall mono boot extra 2>/dev/null < "$DEADLINE_FILE"; then
        if [[ "$wall" =~ ^[0-9]+$ && "$mono" =~ ^[0-9]+$ && -z "$extra" && "$boot" == "$(_boot_id)" ]]; then
            :   # this boot's monotonic deadline
        elif [[ "$wall" =~ ^[0-9]+$ && -z "$mono" ]]; then
            rem=$(( wall - $(_wall_now) ))
            (( rem < 0 )) && rem=0
            mono=$(( now_m + rem ))
        else
            mono=""
        fi
    fi
    if [[ "$mono" =~ ^[0-9]+$ ]] && (( mono - now_m <= CELEBRATION_MAX_SECONDS )); then
        echo "$mono"
    else
        echo "$fallback"
    fi
}

# Epic celebration sequence for a single miner
celebrate_miner() {
    local ip="$1"
    local duration="$2"
    local original_state="$3"

    log "Starting celebration on $ip for ${duration}s..."

    local start_m end_m now_m
    start_m=$(_mono_now)
    end_m=$(( start_m + duration ))
    local phase=0

    # The deadline is checked between effect phases, so a run ends within one
    # phase of it: normally under 20s, up to about a minute while a miner is
    # unreachable and every write waits out its timeout.
    while :; do
        now_m=$(_mono_now)
        (( now_m < $(read_deadline "$end_m") )) || break
        if (( now_m - start_m >= CELEBRATION_MAX_SECONDS )); then
            log_error "Celebration on $ip reached the ${CELEBRATION_MAX_SECONDS}s ceiling — ending it"
            break
        fi
        # Stop early if quiet hours have started (check every cycle)
        if is_quiet_hours 2>/dev/null; then
            log "Quiet hours started — stopping celebration on $ip"
            break
        fi
        case $((phase % 24)) in
            0)
                # OPENING BURST: Rapid gold-white strobe
                strobe_two "$ip" 255 215 0  255 255 255 6
                ;;
            1)
                # Fast color chase through full spectrum
                color_chase "$ip" 0.3
                ;;
            2)
                # Rapid green-gold strobe (MONEY!)
                strobe_two "$ip" 0 255 0  255 215 0 5
                ;;
            3)
                # Fast pulse bright GOLD
                fast_pulse "$ip" 255 215 0 4
                ;;
            4)
                # Rainbow cycle (fast speed)
                set_led "$ip" 4 100 90 0 0 0
                sleep 5
                ;;
            5)
                # Red-blue police strobe
                strobe_two "$ip" 255 0 0  0 0 255 5
                ;;
            6)
                # Color chase (even faster)
                color_chase "$ip" 0.2
                color_chase "$ip" 0.2
                ;;
            7)
                # Flash white strobe (high speed)
                set_led "$ip" 2 100 90 255 255 255
                sleep 3
                ;;
            8)
                # Cyan-magenta strobe
                strobe_two "$ip" 0 255 255  255 0 255 5
                ;;
            9)
                # Pulse deep green
                fast_pulse "$ip" 0 255 0 4
                ;;
            10)
                # Orange-yellow fire effect
                strobe_two "$ip" 255 80 0  255 200 0 6
                ;;
            11)
                # Rainbow (medium speed)
                set_led "$ip" 4 100 60 0 0 0
                sleep 6
                ;;
            12)
                # Gold-green-white rapid chase
                set_led "$ip" 1 100 50 255 215 0;   sleep 0.4
                set_led "$ip" 1 100 50 0 255 0;     sleep 0.4
                set_led "$ip" 1 100 50 255 255 255;  sleep 0.4
                set_led "$ip" 1 100 50 255 215 0;   sleep 0.4
                set_led "$ip" 1 100 50 0 255 0;     sleep 0.4
                set_led "$ip" 1 100 50 255 255 255;  sleep 0.4
                ;;
            13)
                # Fast pulse purple
                fast_pulse "$ip" 180 0 255 4
                ;;
            14)
                # White-gold-green victory strobe
                strobe_two "$ip" 255 255 255  0 255 0 4
                strobe_two "$ip" 255 215 0  255 255 255 4
                ;;
            15)
                # Rapid full spectrum chase
                color_chase "$ip" 0.15
                color_chase "$ip" 0.15
                color_chase "$ip" 0.15
                ;;
            16)
                # Slow pulse GOLD glory
                pulse_color "$ip" 255 215 0 6
                ;;
            17)
                # Pink-cyan strobe
                strobe_two "$ip" 255 50 150  50 255 200 5
                ;;
            18)
                # Flash bright orange
                set_led "$ip" 2 100 80 255 128 0
                sleep 3
                ;;
            19)
                # Rainbow (fast)
                set_led "$ip" 4 100 95 0 0 0
                sleep 5
                ;;
            20)
                # Green-white-gold triple strobe
                strobe_two "$ip" 0 255 0  255 255 255 3
                strobe_two "$ip" 255 215 0  0 255 0 3
                ;;
            21)
                # Color chase reverse (fast)
                set_led "$ip" 1 100 50 255 0 128;   sleep 0.3
                set_led "$ip" 1 100 50 128 0 255;   sleep 0.3
                set_led "$ip" 1 100 50 0 0 255;     sleep 0.3
                set_led "$ip" 1 100 50 0 255 255;   sleep 0.3
                set_led "$ip" 1 100 50 0 255 0;     sleep 0.3
                set_led "$ip" 1 100 50 255 255 0;   sleep 0.3
                set_led "$ip" 1 100 50 255 128 0;   sleep 0.3
                set_led "$ip" 1 100 50 255 0 0;     sleep 0.3
                ;;
            22)
                # Fast pulse cyan
                fast_pulse "$ip" 0 255 255 4
                ;;
            23)
                # Grand finale: rapid multicolor burst
                strobe_two "$ip" 255 0 0  0 255 0 3
                strobe_two "$ip" 0 0 255  255 215 0 3
                strobe_two "$ip" 255 0 255  0 255 255 3
                set_led "$ip" 2 100 95 255 255 255
                sleep 2
                ;;
        esac
        phase=$((phase + 1))
    done

    if restore_idle_led_retry "$ip" "$original_state"; then
        [[ -n "$RESULT_DIR" ]] && : > "$RESULT_DIR/ok.$ip"
    fi
    return 0
}

#===============================================================================
# MAIN CELEBRATION ORCHESTRATOR
#===============================================================================

run_celebration() {
    local duration="$1"
    shift
    local miners=("$@")

    if [[ ${#miners[@]} -eq 0 ]]; then
        log_error "No miners specified"
        return 1
    fi

    log_success ""
    log_success "${GOLD}=============================================${NC}"
    log_success "${GOLD}   BLOCK FOUND! STARTING CELEBRATION!${NC}"
    log_success "${GOLD}=============================================${NC}"
    log_success ""
    log "Duration: ${duration} seconds ($(( duration / 60 )) minutes)"
    log "Miners: ${#miners[@]} device(s) - ${miners[*]}"

    # Save original states and check responsiveness
    declare -A original_states
    local active_miners=()

    for ip in "${miners[@]}"; do
        if ! is_miner_responsive "$ip"; then
            log "Skipping $ip - not responding to CGMiner API"
            continue
        fi

        local state
        state=$(get_led_state "$ip")
        original_states[$ip]="$state"
        SAVED_STATES[$ip]="$state"  # Global copy for signal handler cleanup
        active_miners+=("$ip")
        log "Saved state for $ip: $state"
    done

    if [[ ${#active_miners[@]} -eq 0 ]]; then
        log_error "No responsive CGMiner miners found"
        return 1
    fi

    # Record what is about to be lit BEFORE the first LED write, so a power cut
    # at any point from here on leaves something for recovery to act on.
    local -a entries=()
    for ip in "${active_miners[@]}"; do
        entries+=("$ip ${original_states[$ip]:--}")
    done
    if write_state "$$" 0 "${entries[@]}"; then
        STATE_OWNED=1
    else
        log_error "Could not write ${STATE_FILE} — if power is lost mid-celebration the LEDs will need clearing by hand"
    fi
    RESULT_DIR=$(mktemp -d "${TMPDIR:-/tmp}/spiralpool-celebrate.XXXXXX" 2>/dev/null) || RESULT_DIR=""

    # Run celebrations in parallel — track PIDs for cleanup on signal
    CHILD_PIDS=()
    for ip in "${active_miners[@]}"; do
        celebrate_miner "$ip" "$duration" "${original_states[$ip]}" &
        CHILD_PIDS+=($!)
    done

    # Wait for all celebrations to complete
    # Use || true to prevent set -e from killing the script if a child exits non-zero
    # (e.g., miner goes offline mid-celebration, nc times out)
    wait || true

    # Only claim the LEDs are restored if every miner confirmed it.
    local unconfirmed=0
    if [[ -n "$RESULT_DIR" ]]; then
        for ip in "${active_miners[@]}"; do
            [[ -e "$RESULT_DIR/ok.$ip" ]] || unconfirmed=$(( unconfirmed + 1 ))
        done
    fi
    log_success ""
    log_success "${GOLD}=============================================${NC}"
    if (( unconfirmed == 0 )); then
        log_success "${GOLD}   CELEBRATION COMPLETE - LEDs RESTORED${NC}"
    else
        log_error "   CELEBRATION COMPLETE - ${unconfirmed} miner(s) did NOT confirm the LED restore"
    fi
    log_success "${GOLD}=============================================${NC}"
    log_success ""
}

#===============================================================================
# CLI INTERFACE
#===============================================================================

usage() {
    cat << EOF
Usage: $(basename "$0") [OPTIONS]

Trigger LED celebration on CGMiner-compatible miners when a block is found.

Options:
  --test              Run a quick 30-second test celebration
  --duration SECONDS  Celebration duration (default: 7200 = 2 hours)
  --miners "IP ..."   Space-separated list of miner IPs (auto-discovers if not set)
  --scan              Force rescan for miners (ignore cache)
  --list              List discovered miners and exit
  --force             Bypass quiet hours check
  --block HASH        Record the block this celebration is for
  --stop              Stop a running celebration and restore the LEDs.
                      With --block, withdraws only that block and stops
                      only if it was the last one still celebrating.
  --recover           Return to idle any miner left lit by a celebration that
                      was cut off (power cut, kill, miner rebooting at the end)
                      and clear its record. Never resumes a celebration.
                      Sentinel runs this at startup and every 5 minutes while
                      a record exists.
  -h, --help          Show this help message

Examples:
  $(basename "$0")                          # Auto-discover miners, 2 hour celebration
  $(basename "$0") --test                   # Quick 30 second test
  $(basename "$0") --miners "192.168.1.14"  # Specific miner
  $(basename "$0") --duration 600           # 10 minute celebration

Environment Variables:
  CELEBRATION_DURATION     Default duration in seconds (default: 7200)
  CELEBRATION_MAX_SECONDS  Hard ceiling for one celebration, extensions
                           included (default: 86400 = 24 hours)
  CELEBRATE_STATE_FILE     Durable celebration record
                           (default: /spiralpool/data/celebrate.state)

EOF
}

main() {
    # All logging goes to stderr, and every automated caller (Sentinel's Popen,
    # the stratum's exec.Command) sends that to the null device — so nothing this
    # script did was ever recorded. When not attached to a terminal, append to a
    # log file instead.
    if [[ ! -t 2 ]] && [[ -w "$(dirname "$CELEBRATE_LOG")" ]]; then
        exec 2>>"$CELEBRATE_LOG"
    fi

    mkdir -p "$RUN_DIR" 2>/dev/null || true
    # Fallback: if /run/spiralpool couldn't be created (ProtectSystem=strict without
    # RuntimeDirectory), use /tmp for lock and cache files. /tmp may survive a
    # reboot, which is why a lock is only trusted if its PID is a live
    # block-celebrate process (_is_celebrate_pid), never on its existence alone.
    if [[ ! -d "$RUN_DIR" ]]; then
        MINER_CACHE_FILE="/tmp/spiralpool-cgminers.cache"
        LOCK_FILE="/tmp/spiralpool-celebrate.lock"
        DEADLINE_FILE="/tmp/spiralpool-celebrate.deadline"
        BLOCKS_FILE="/tmp/spiralpool-celebrate.blocks"
    fi

    local duration="$CELEBRATION_DURATION"
    local miners_arg=""
    local force_scan=false
    local force_celebrate=false
    local list_only=false
    local test_mode=false
    local block_hash=""
    local stop_mode=false
    local recover_mode=false

    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --test)
                test_mode=true
                duration=30
                shift
                ;;
            --duration)
                if ! [[ "$2" =~ ^[0-9]+$ ]]; then
                    log_error "Duration must be a positive integer (seconds)"
                    exit 1
                fi
                duration="$2"
                shift 2
                ;;
            --miners)
                [[ -z "${2:-}" ]] && { log_error "--miners requires an argument (space-separated IPs)"; exit 1; }
                miners_arg="$2"
                shift 2
                ;;
            --scan)
                force_scan=true
                shift
                ;;
            --force)
                force_celebrate=true
                shift
                ;;
            --list)
                list_only=true
                shift
                ;;
            --block)
                [[ -z "${2:-}" ]] && { log_error "--block requires a block hash"; exit 1; }
                block_hash="$2"
                shift 2
                ;;
            --stop)
                stop_mode=true
                shift
                ;;
            --recover)
                recover_mode=true
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                exit 1
                ;;
        esac
    done

    # --stop signals the incumbent and touches no miner itself, so it runs
    # before miner discovery, before the quiet-hours gate, and without taking
    # the lock. It must also not fire the EXIT trap: this process never owned
    # the LEDs, and cleanup() would try to release a lock belonging to another.
    if [[ "$stop_mode" == "true" ]]; then
        trap - EXIT
        stop_celebration "$block_hash"
        # If no live process owned the celebration, nothing above touched the
        # LEDs — a dead owner may have left them lit. Recovery leaves a live
        # owner (still running its own cleanup) alone.
        recover_stale_celebration || true
        exit 0
    fi

    # --recover: lights off for anything a dead celebration left lit. Ignores
    # quiet hours on purpose — switching LEDs off is never what they suppress.
    if [[ "$recover_mode" == "true" ]]; then
        trap - EXIT
        recover_stale_celebration || true
        exit 0
    fi

    if (( duration > CELEBRATION_MAX_SECONDS )); then
        log "Duration ${duration}s exceeds the ${CELEBRATION_MAX_SECONDS}s ceiling — clamping"
        duration="$CELEBRATION_MAX_SECONDS"
    fi

    # Quiet hours check — skip celebration if quiet hours are active
    # (unless --force or --test was passed)
    if [[ "$force_celebrate" != "true" ]] && [[ "$test_mode" != "true" ]]; then
        if is_quiet_hours; then
            log "Quiet hours active — LED celebration suppressed (use --force to override)"
            exit 0
        fi
    fi

    # Get miners
    local miners=()

    if [[ -n "$miners_arg" ]]; then
        read -ra miners <<< "$miners_arg"
    else
        log "Discovering CGMiner devices..."
        while IFS= read -r ip; do
            [[ -n "$ip" ]] && miners+=("$ip")
        done < <(get_miners "$force_scan")
    fi

    if [[ "$list_only" == "true" ]]; then
        echo "Discovered CGMiner miners:"
        for ip in "${miners[@]}"; do
            local state
            state=$(get_led_state "$ip" 2>/dev/null) || state="unknown"
            local status="RESPONSIVE"
            is_miner_responsive "$ip" || status="NOT RESPONDING"
            echo "  $ip - LED:[$state] $status"
        done
        exit 0
    fi

    if [[ ${#miners[@]} -eq 0 ]]; then
        log_error "No CGMiner-compatible miners found"
        log "Tip: Make sure your Avalon miner is on the same network"
        exit 1
    fi

    if [[ "$test_mode" == "true" ]]; then
        log "Running TEST celebration (30 seconds)..."
    fi

    # Overlapping celebrations: extend the running one instead of killing it.
    # This script is launched independently by the stratum, by Sentinel, and by
    # hand, so a single block reaches it several times. Killing and restarting
    # meant the new run re-read the LED after only a 2s wait and could capture a
    # mid-celebration colour as the "original" to restore hours later.
    #
    # The lock is created with noclobber, so taking it is atomic: two launches
    # for the same block (stratum and Sentinel, often within a second) can no
    # longer both see "no lock" and both start driving the LEDs.
    local attempt old_pid lock_taken=false
    for attempt in 1 2 3; do
        if ( set -o noclobber; echo "$$" > "$LOCK_FILE" ) 2>/dev/null; then
            lock_taken=true
            break
        fi
        old_pid=$(cat "$LOCK_FILE" 2>/dev/null || true)
        if _is_celebrate_pid "$old_pid"; then
            local now_m new_deadline current_deadline
            now_m=$(_mono_now)
            new_deadline=$(( now_m + duration ))
            current_deadline=$(read_deadline 0)
            if (( new_deadline > current_deadline )); then
                write_deadline "$new_deadline"
                log "Celebration already running (PID $old_pid) — extended by $(( duration / 60 ))m"
            else
                log "Celebration already running (PID $old_pid) — deadline unchanged"
            fi
            # Join this block to the running celebration either way: the
            # deadline may not move, but the celebration now stands for this
            # block too and must not end when an earlier one is withdrawn.
            record_block "$block_hash"
            # Do not run cleanup: the incumbent owns the LEDs and the lock
            trap - EXIT
            exit 0
        fi
        # Stale lock from a process that died without cleanup (or a PID the
        # kernel has since given to something else)
        log "Removing stale lock (PID ${old_pid:-unknown} not running)"
        rm -f "$LOCK_FILE" "$DEADLINE_FILE"
    done
    if [[ "$lock_taken" != "true" ]]; then
        log_error "Could not take the celebration lock ${LOCK_FILE} — giving up"
        trap - EXIT
        exit 1
    fi

    # Publish our deadline, then deal with anything a dead celebration left
    # behind before reading any LED: with led_idle_state=restore this run is
    # about to capture each miner's "original" state, and a miner still lit
    # from the dead run would hand it a celebration colour to restore later.
    write_deadline "$(( $(_mono_now) + duration ))"
    rm -f "$BLOCKS_FILE"
    record_block "$block_hash"
    recover_stale_celebration || true

    run_celebration "$duration" "${miners[@]}"

    # Lock, deadline, block list and the durable record are released by
    # cleanup() on EXIT, after the final LED restore — never before it, or a new
    # celebration could start while this one is still switching LEDs off.
}

# Run if executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
