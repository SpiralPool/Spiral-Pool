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
#
# Called automatically by Sentinel when a block is found.
#===============================================================================

set -euo pipefail

# Configuration
CELEBRATION_DURATION="${CELEBRATION_DURATION:-7200}"  # 2 hour default (matches config.yaml)
CGMINER_PORT=4028
NC_TIMEOUT=2
MINER_CACHE_FILE="/run/spiralpool/cgminers.cache"
MINER_CACHE_TTL=3600  # 1 hour cache for miner discovery
LOCK_FILE="/run/spiralpool/celebrate.lock"

# Track child PIDs for cleanup on termination
declare -a CHILD_PIDS=()
declare -A SAVED_STATES=()  # ip -> led state, for cleanup on signal

# Cleanup handler: restore LEDs and release lock on termination
cleanup() {
    log "Caught signal — stopping celebration and restoring LEDs..."
    # Kill all tracked child processes
    for pid in "${CHILD_PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
    # Restore saved LED states
    for ip in "${!SAVED_STATES[@]}"; do
        local state="${SAVED_STATES[$ip]}"
        IFS='-' read -r mode brightness speed r g b <<< "$state"
        set_led "$ip" "$mode" "$brightness" "$speed" "$r" "$g" "$b" 2>/dev/null || true
        log "Restored LED on $ip"
    done
    # Release lock
    rm -f "$LOCK_FILE"
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
    response=$(cgminer_cmd "$ip" "version" 2>/dev/null) || return 1
    [[ "$response" == *"CGMiner"* ]] || [[ "$response" == *"cgminer"* ]]
}

# Get current LED state from miner
# Returns: mode-brightness-speed-r-g-b
get_led_state() {
    local ip="$1"
    local stats
    stats=$(cgminer_cmd "$ip" "stats" 2>/dev/null) || return 1

    # Extract LEDUser[mode-brightness-speed-r-g-b]
    if [[ "$stats" =~ LEDUser\[([0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+)\] ]]; then
        echo "${BASH_REMATCH[1]}"
    else
        echo "0-100-50-0-253-255"  # Default cyan
    fi
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

    cgminer_cmd "$ip" "ascset|0,ledset,$mode-$brightness-$speed-$r-$g-$b" >/dev/null 2>&1
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

# Get cached miners or discover new ones
get_miners() {
    local force_scan="${1:-false}"

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

# Epic celebration sequence for a single miner
celebrate_miner() {
    local ip="$1"
    local duration="$2"
    local original_state="$3"

    log "Starting celebration on $ip for ${duration}s..."

    local end_time=$(( $(date +%s) + duration ))
    local phase=0

    while (( $(date +%s) < end_time )); do
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

    # Restore original state
    log "Restoring original LED state on $ip..."
    IFS='-' read -r mode brightness speed r g b <<< "$original_state"
    set_led "$ip" "$mode" "$brightness" "$speed" "$r" "$g" "$b"
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

    log_success ""
    log_success "${GOLD}=============================================${NC}"
    log_success "${GOLD}   CELEBRATION COMPLETE - LEDs RESTORED${NC}"
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
  --duration SECONDS  Celebration duration (default: 3600 = 1 hour)
  --miners "IP ..."   Space-separated list of miner IPs (auto-discovers if not set)
  --scan              Force rescan for miners (ignore cache)
  --list              List discovered miners and exit
  --force             Bypass quiet hours check
  -h, --help          Show this help message

Examples:
  $(basename "$0")                          # Auto-discover miners, 1 hour celebration
  $(basename "$0") --test                   # Quick 30 second test
  $(basename "$0") --miners "192.168.1.14"  # Specific miner
  $(basename "$0") --duration 600           # 10 minute celebration

Environment Variables:
  CELEBRATION_DURATION   Default duration in seconds (default: 3600)

EOF
}

main() {
    mkdir -p /run/spiralpool 2>/dev/null || true
    # Fallback: if /run/spiralpool couldn't be created (ProtectSystem=strict without
    # RuntimeDirectory), use /tmp for lock and cache files
    if [[ ! -d /run/spiralpool ]]; then
        MINER_CACHE_FILE="/tmp/spiralpool-cgminers.cache"
        LOCK_FILE="/tmp/spiralpool-celebrate.lock"
    fi

    local duration="$CELEBRATION_DURATION"
    local miners_arg=""
    local force_scan=false
    local force_celebrate=false
    local list_only=false
    local test_mode=false

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
                [[ -z "${2:-}" ]] && { log_error "--miners requires an argument (comma-separated IPs)"; exit 1; }
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

    # Prevent overlapping celebrations — kill previous if still running
    if [[ -f "$LOCK_FILE" ]]; then
        local old_pid
        old_pid=$(cat "$LOCK_FILE" 2>/dev/null)
        if [[ "$old_pid" =~ ^[0-9]+$ ]] && kill -0 "$old_pid" 2>/dev/null; then
            log "Previous celebration (PID $old_pid) still running — sending SIGTERM"
            kill "$old_pid" 2>/dev/null || true
            # Brief wait for cleanup handler to restore LEDs
            sleep 2
        fi
        rm -f "$LOCK_FILE"
    fi

    # Write our PID as the lock
    echo $$ > "$LOCK_FILE"

    run_celebration "$duration" "${miners[@]}"

    # Release lock on normal exit
    rm -f "$LOCK_FILE"
}

# Run if executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
