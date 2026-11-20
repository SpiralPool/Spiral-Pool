#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# pool-upgrade-launch.sh — start a Spiral Pool upgrade on behalf of the dashboard.
#
# The dashboard runs as the unprivileged pool user inside a ProtectSystem=strict
# sandbox, and upgrade.sh stops the dashboard part-way through. Running upgrade.sh
# as a child of the dashboard therefore cannot work: it inherits a read-only view
# of the system, and systemd kills it together with the dashboard's cgroup.
#
# This launcher starts upgrade.sh as its own transient systemd unit instead, so the
# upgrade runs with full privileges, outside the dashboard, and survives the
# dashboard restarting. The dashboard follows it through:
#   systemctl is-active spiralpool-upgrade        (unprivileged)
#   /var/log/spiralpool/upgrade.log               (root-owned dir, group-readable)
#
# SECURITY: deployed root:root 0755 into /spiralpool/scripts (a root-owned
# directory) and allowed to the pool user through sudoers WITHOUT arguments. It
# accepts no arguments and runs only the root-owned copy of upgrade.sh in the same
# directory — never /spiralpool/upgrade.sh, which lives in a directory the pool
# user owns and could swap out.

set -euo pipefail

UNIT="spiralpool-upgrade"
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
INSTALL_DIR="$(dirname "$SCRIPT_DIR")"
UPGRADE="$SCRIPT_DIR/upgrade.sh"
LOG_DIR="/var/log/spiralpool"
LOG="$LOG_DIR/upgrade.log"

if [[ $EUID -ne 0 ]]; then
    echo "pool-upgrade-launch.sh must run as root" >&2
    exit 1
fi
if [[ $# -ne 0 ]]; then
    echo "pool-upgrade-launch.sh takes no arguments" >&2
    exit 2
fi
if [[ ! -f "$UPGRADE" ]]; then
    echo "upgrade.sh not found at $UPGRADE — run 'sudo spiralctl upgrade' once from a shell" >&2
    exit 3
fi
# Refuse to run anything the pool user could have written.
if [[ "$(stat -c '%u' "$UPGRADE")" != "0" ]] || [[ "$(stat -c '%u' "$SCRIPT_DIR")" != "0" ]] \
   || [[ -n "$(find "$UPGRADE" "$SCRIPT_DIR" -maxdepth 0 -perm /022 2>/dev/null)" ]]; then
    echo "refusing to run $UPGRADE: it or its directory is not root-owned and write-protected" >&2
    exit 3
fi

state=$(systemctl is-active "$UNIT" 2>/dev/null || true)
if [[ "$state" == "active" || "$state" == "activating" || "$state" == "deactivating" ]]; then
    echo "an upgrade is already running ($UNIT is $state)" >&2
    exit 4
fi
systemctl reset-failed "$UNIT" 2>/dev/null || true

# Fresh log in a root-owned directory (no symlink games possible), readable by the
# pool user's group so the dashboard can show progress.
pool_group=$(stat -c '%G' "$INSTALL_DIR" 2>/dev/null || echo root)
install -d -o root -g root -m 0755 "$LOG_DIR"
rm -f "$LOG"
install -o root -g "$pool_group" -m 0640 /dev/null "$LOG"
echo "=== Spiral Pool upgrade started at $(date -u '+%Y-%m-%d %H:%M:%S UTC') ===" >> "$LOG"

systemd-run \
    --unit="$UNIT" \
    --description="Spiral Pool upgrade (started by the dashboard or Sentinel)" \
    --collect \
    --no-block \
    --property=StandardOutput="append:$LOG" \
    --property=StandardError="append:$LOG" \
    --setenv=TERM=dumb \
    --setenv=NO_COLOR=1 \
    /bin/bash -c 'bash "$0" --auto; rc=$?; echo "=== Spiral Pool upgrade finished (exit $rc) at $(date -u "+%Y-%m-%d %H:%M:%S UTC") ==="; exit $rc' "$UPGRADE"
# --collect removes the unit (and its Result) once it ends, so the final
# "finished (exit N)" line above is how the dashboard learns the outcome.

echo "started $UNIT"
