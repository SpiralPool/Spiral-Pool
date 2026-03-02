#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# etcd Quorum Recovery Script
# Forces etcd into single-node mode when the cluster has lost quorum.
#
# Called by ha-role-watcher.sh during automatic failover when:
#   1. VIP is on this node (keepalived promoted us)
#   2. etcd has no quorum (majority of nodes dead)
#   3. All peers confirmed unreachable
#
# This script MUST run as root (via sudoers) because:
#   - etcd systemctl stop/start
#   - etcd --force-new-cluster requires access to the data directory
#   - kill of the temporary etcd process
#
# Exit codes:
#   0 = Success (etcd is healthy as single-node)
#   1 = Failure (etcd could not be recovered)
#

set -euo pipefail

# Cleanup trap — kill orphan background etcd process if script is interrupted
ETCD_PID=""
ETCD_TMPLOG=""
cleanup() {
    if [[ -n "$ETCD_PID" ]]; then
        kill "$ETCD_PID" 2>/dev/null || true
        wait "$ETCD_PID" 2>/dev/null || true
    fi
    if [[ -n "$ETCD_TMPLOG" ]] && [[ -f "$ETCD_TMPLOG" ]]; then
        rm -f "$ETCD_TMPLOG"
    fi
}
trap cleanup EXIT INT TERM

# Read etcd environment config
if [[ ! -f /etc/default/etcd ]]; then
    echo "ERROR: /etc/default/etcd not found"
    exit 1
fi

# SECURITY: Safe parsing — do NOT source (runs as root via sudoers NOPASSWD)
# Only extract ETCD_* variable assignments, ignore everything else
while IFS='=' read -r key value; do
    # Skip comments, empty lines, non-ETCD variables
    [[ "$key" =~ ^[[:space:]]*# ]] && continue
    [[ -z "$key" ]] && continue
    key=$(echo "$key" | tr -d '[:space:]')
    [[ "$key" =~ ^ETCD_[A-Z_]+$ ]] || continue
    # Strip surrounding quotes from value
    value=$(echo "$value" | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
    # Export only recognized ETCD_ variables
    declare -g "$key=$value"
done < /etc/default/etcd

if [[ -z "${ETCD_NAME:-}" ]] || [[ -z "${ETCD_DATA_DIR:-}" ]]; then
    echo "ERROR: ETCD_NAME or ETCD_DATA_DIR not set in /etc/default/etcd"
    exit 1
fi

# SECURITY: Validate ETCD_NAME format
if ! [[ "$ETCD_NAME" =~ ^[a-zA-Z0-9._-]+$ ]]; then
    echo "ERROR: ETCD_NAME contains invalid characters: ${ETCD_NAME:0:40}"
    exit 1
fi

# SECURITY: Validate ETCD_DATA_DIR is under expected path (prevent path traversal)
if [[ "$ETCD_DATA_DIR" == *".."* ]]; then
    echo "ERROR: ETCD_DATA_DIR contains '..' — refusing to proceed"
    exit 1
fi
ETCD_DATA_DIR_REAL=$(realpath -m "$ETCD_DATA_DIR" 2>/dev/null || echo "$ETCD_DATA_DIR")
case "$ETCD_DATA_DIR_REAL" in
    /var/lib/etcd|/var/lib/etcd/*) ;;
    *)
        echo "ERROR: ETCD_DATA_DIR '${ETCD_DATA_DIR_REAL}' not under /var/lib/etcd/"
        exit 1
        ;;
esac
ETCD_DATA_DIR="$ETCD_DATA_DIR_REAL"

# Prevent concurrent execution — etcd recovery is destructive (data wipe, force-new-cluster)
mkdir -p /run/spiralpool 2>/dev/null || true
exec 8>/run/spiralpool/etcd-recovery.lock
if ! flock -n 8; then
    echo "ERROR: Another etcd recovery/rejoin is already running"
    exit 1
fi

echo "etcd-quorum-recover: Stopping etcd service..."
systemctl stop etcd 2>/dev/null || true

# Wait for etcd to fully release ports (stuck election loops can delay shutdown)
echo "etcd-quorum-recover: Waiting for etcd ports to be released..."
for i in $(seq 1 30); do
    if ! ss -tlnp 2>/dev/null | grep -q ':2380 '; then
        break
    fi
    if [[ $i -eq 15 ]]; then
        echo "etcd-quorum-recover: etcd still holding port after 15s, sending SIGKILL..."
        pkill -9 -x etcd 2>/dev/null || true
    fi
    sleep 1
done

# Final check
if ss -tlnp 2>/dev/null | grep -q ':2380 '; then
    echo "ERROR: Port 2380 still in use after 30s — cannot proceed"
    systemctl start etcd 2>/dev/null || true
    exit 1
fi

# Capture etcd output to a temp file for diagnostics (don't discard to /dev/null)
mkdir -p /run/spiralpool 2>/dev/null || true
chmod 700 /run/spiralpool 2>/dev/null || true
ETCD_TMPLOG=$(mktemp /run/spiralpool/etcd-recover-XXXXXX.log)

echo "etcd-quorum-recover: Running --force-new-cluster..."
etcd --force-new-cluster \
    --name "$ETCD_NAME" \
    --data-dir "$ETCD_DATA_DIR" \
    --listen-client-urls "${ETCD_LISTEN_CLIENT_URLS:-http://127.0.0.1:2379}" \
    --advertise-client-urls "${ETCD_ADVERTISE_CLIENT_URLS:-http://127.0.0.1:2379}" \
    --listen-peer-urls "${ETCD_LISTEN_PEER_URLS:-http://127.0.0.1:2380}" \
    --initial-advertise-peer-urls "${ETCD_INITIAL_ADVERTISE_PEER_URLS:-http://127.0.0.1:2380}" > "$ETCD_TMPLOG" 2>&1 &
ETCD_PID=$!

# Brief check that the process actually started
sleep 2
if ! kill -0 "$ETCD_PID" 2>/dev/null; then
    echo "ERROR: etcd --force-new-cluster failed to start. Output:"
    cat "$ETCD_TMPLOG" 2>/dev/null || true
    systemctl start etcd 2>/dev/null || true
    exit 1
fi

# Wait for etcd to become healthy (up to 30 seconds)
READY=0
for _ in $(seq 1 30); do
    # Check if process died during health loop
    if ! kill -0 "$ETCD_PID" 2>/dev/null; then
        echo "ERROR: etcd --force-new-cluster process died unexpectedly. Output:"
        cat "$ETCD_TMPLOG" 2>/dev/null || true
        break
    fi
    if ETCDCTL_API=3 etcdctl endpoint health --endpoints=http://127.0.0.1:2379 --command-timeout=2s 2>&1 | grep -q "is healthy"; then
        READY=1
        break
    fi
    sleep 1
done

# Kill temporary etcd process
kill "$ETCD_PID" 2>/dev/null || true
wait "$ETCD_PID" 2>/dev/null || true
ETCD_PID=""  # Clear so trap doesn't double-kill
sleep 1

if [[ $READY -eq 0 ]]; then
    echo "ERROR: --force-new-cluster did not become healthy within 30s"
    # Try to restart normally as a last resort
    systemctl start etcd 2>/dev/null || true
    exit 1
fi

# Restart etcd via systemd for normal operation
echo "etcd-quorum-recover: Restarting etcd via systemd..."
if ! systemctl start etcd; then
    echo "ERROR: systemctl start etcd failed"
    exit 1
fi
sleep 2

# Verify health
if ETCDCTL_API=3 etcdctl endpoint health --endpoints=http://127.0.0.1:2379 --command-timeout=3s 2>&1 | grep -q "is healthy"; then
    echo "etcd-quorum-recover: SUCCESS — etcd healthy as single-node cluster"
    exit 0
else
    echo "ERROR: etcd still unhealthy after systemd restart"
    exit 1
fi
