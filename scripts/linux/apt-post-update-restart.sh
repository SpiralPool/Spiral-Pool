#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool Post-APT-Update Service Restart
#
# Called by systemd after apt-daily-upgrade.service completes.
# Checks if critical system packages (Python, OpenSSL, glibc, etc.) were
# updated, and gracefully restarts pool services so they pick up the new
# libraries. Without this, services would run with stale security patches
# until the next reboot.
#
# This script does NOT restart blockchain daemons — those take minutes to
# reload their chain indexes and are handled by the 4 AM auto-reboot instead.
#
# Design:
#   - Only restarts if relevant packages were actually upgraded TODAY
#   - Debounces to prevent multiple restarts within 5 minutes
#   - Verifies each service comes back online after restart
#   - Logs everything to /spiralpool/logs/apt-restart.log
#   - Non-destructive: if anything fails, services have Restart=always in systemd
#

INSTALL_DIR="${INSTALL_DIR:-/spiralpool}"
LOG_FILE="${INSTALL_DIR}/logs/apt-restart.log"
DEBOUNCE_FILE="/run/spiralpool/.spiralpool-apt-restart-debounce"
DEBOUNCE_SECONDS=300  # Don't restart more than once per 5 minutes

# Packages that affect running pool services (Python apps + Go binary via libc)
CRITICAL_PATTERNS="python3|libssl|openssl|libcrypto|libc6|libstdc|libgcc|libffi|libsqlite|libreadline|libncurses|zlib"

# =============================================================================
# Logging
# =============================================================================

log() {
    local timestamp
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    mkdir -p "$(dirname "${LOG_FILE}")" 2>/dev/null
    echo "[${timestamp}] $1" >> "${LOG_FILE}" 2>/dev/null
}

# Simple log rotation — rotate when log exceeds 5MB
if [[ -f "${LOG_FILE}" ]]; then
    size=$(wc -c < "${LOG_FILE}" 2>/dev/null || echo 0)
    if [[ "$size" -gt 5242880 ]]; then
        mv "${LOG_FILE}" "${LOG_FILE}.old" 2>/dev/null
    fi
fi

# =============================================================================
# Debounce check
# =============================================================================

mkdir -p "$(dirname "$DEBOUNCE_FILE")" 2>/dev/null || true

if [[ -f "$DEBOUNCE_FILE" ]]; then
    last_run=$(cat "$DEBOUNCE_FILE" 2>/dev/null || echo 0)
    now=$(date +%s)
    if (( now - last_run < DEBOUNCE_SECONDS )); then
        exit 0
    fi
fi

# =============================================================================
# Check if critical packages were upgraded today
# =============================================================================

TODAY=$(date +%Y-%m-%d)
RECENT_UPGRADES=$(grep "^${TODAY}" /var/log/dpkg.log 2>/dev/null | grep " upgrade " | grep -iE "$CRITICAL_PATTERNS" || true)

if [[ -z "$RECENT_UPGRADES" ]]; then
    # No critical packages upgraded today — nothing to do
    exit 0
fi

# Extract just the package names for logging
UPDATED_PKGS=$(echo "$RECENT_UPGRADES" | awk '{print $4}' | sort -u | tr '\n' ', ' | sed 's/,$//')

log "================================================================"
log "Critical system packages updated — restarting pool services"
log "Updated packages: ${UPDATED_PKGS}"

# Write debounce timestamp
date +%s > "$DEBOUNCE_FILE" 2>/dev/null

# =============================================================================
# Graceful service restart
# =============================================================================

# Restart order (least critical first):
#   1. spiralsentinel  — monitoring/alerts (brief gap is fine)
#   2. spiraldash      — web UI (brief 503 is fine)
#   3. spiralstratum   — mining (miners auto-reconnect in <5s)
#
# NOT restarted:
#   - spiralpool-health  — shell script, no library dependencies
#   - spiralpool-sync    — on-demand tool, not a persistent service
#   - blockchain daemons — too slow to restart, handled by 4 AM reboot
#   - spiralpool-ha-watcher — shell script, no library dependencies

SERVICES=("spiralsentinel" "spiraldash" "spiralstratum")
RESTARTED=()
FAILED=()
SKIPPED=()

# HA SAFETY: On HA backup nodes, restarting spiralstratum mid-election can
# cause a double-election race condition (stratum re-enters election cycle).
# Check HA role before restarting stratum. Sentinel/dash are already stopped
# on backup nodes (ha-service-control.sh), so the is-active check skips them.
HA_SKIP_STRATUM=false
if [[ -f "${INSTALL_DIR}/config/ha-enabled" ]]; then
    _ha_role=$(curl -s --max-time 3 "http://localhost:5354/status" 2>/dev/null | \
        grep -oP '"localRole"\s*:\s*"\K[^"]+' 2>/dev/null || echo "UNKNOWN")
    if [[ "$_ha_role" != "MASTER" ]] && [[ "$_ha_role" != "STANDALONE" ]]; then
        # UNKNOWN means the VIP API is unreachable (stratum still starting after apt restart).
        # Safer to skip restart than risk disrupting an in-progress election.
        # ha-role-watcher will handle service management once stratum stabilizes.
        HA_SKIP_STRATUM=true
        log "  HA role is ${_ha_role} — will skip spiralstratum restart to avoid election disruption"
    fi
fi

for svc in "${SERVICES[@]}"; do
    if ! systemctl is-enabled --quiet "$svc" 2>/dev/null; then
        SKIPPED+=("$svc")
        continue
    fi

    # HA guard: don't restart stratum on backup nodes
    if [[ "$svc" == "spiralstratum" ]] && [[ "$HA_SKIP_STRATUM" == "true" ]]; then
        log "  $svc skipped (HA backup node — stratum participates in election)"
        SKIPPED+=("$svc")
        continue
    fi

    if ! systemctl is-active --quiet "$svc" 2>/dev/null; then
        log "  $svc is not running — skipping (systemd Restart=always will handle it)"
        SKIPPED+=("$svc")
        continue
    fi

    log "  Restarting $svc..."
    if systemctl restart "$svc" 2>/dev/null; then
        # Wait for service to stabilize
        sleep 5

        if systemctl is-active --quiet "$svc" 2>/dev/null; then
            log "  OK $svc restarted successfully"
            RESTARTED+=("$svc")
        else
            # Give it a second chance — some services need more startup time
            sleep 10
            if systemctl is-active --quiet "$svc" 2>/dev/null; then
                log "  OK $svc restarted successfully (slow start)"
                RESTARTED+=("$svc")
            else
                log "  FAIL $svc did not come back online — systemd Restart=always will retry"
                FAILED+=("$svc")
            fi
        fi
    else
        log "  FAIL $svc restart command failed — systemd Restart=always will retry"
        FAILED+=("$svc")
    fi
done

# =============================================================================
# Summary
# =============================================================================

log "Post-update restart complete:"
[[ ${#RESTARTED[@]} -gt 0 ]] && log "  Restarted: ${RESTARTED[*]}"
[[ ${#SKIPPED[@]} -gt 0 ]]   && log "  Skipped:   ${SKIPPED[*]}"
[[ ${#FAILED[@]} -gt 0 ]]    && log "  Failed:    ${FAILED[*]} (systemd will auto-retry)"
log "================================================================"

exit 0
