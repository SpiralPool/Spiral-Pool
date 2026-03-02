#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Patroni Force Bootstrap Script
# Wipes PostgreSQL data directory and clears etcd scope to force a fresh
# Patroni bootstrap. Used as a last resort during automated failover when
# Patroni cannot promote due to WAL corruption or stale DCS state.
#
# Called by ha-role-watcher.sh when wait_for_pg_readwrite() times out:
#   1. etcd quorum was already recovered (single-node mode)
#   2. Patroni failed to promote within 120s (WAL corruption, stale standby.signal, etc.)
#   3. This script wipes everything and lets Patroni start fresh as primary
#
# This script MUST run as root (via sudoers) because:
#   - systemctl stop/start patroni
#   - rm -rf of the postgres-owned data directory
#   - mkdir/chown/chmod of the data directory
#
# Exit codes:
#   0 = Success (Patroni bootstrapped as primary)
#   1 = Failure
#

set -euo pipefail

# Prevent concurrent execution — this script wipes the PG data directory
mkdir -p /run/spiralpool 2>/dev/null || true
exec 8>/run/spiralpool/patroni-force-bootstrap.lock
if ! flock -n 8; then
    echo "patroni-force-bootstrap: ERROR — another instance is already running"
    exit 1
fi

# Determine PG data directory from patroni.yml
PATRONI_CONFIG="/etc/patroni/patroni.yml"
if [[ ! -f "$PATRONI_CONFIG" ]]; then
    echo "patroni-force-bootstrap: ERROR — ${PATRONI_CONFIG} not found"
    exit 1
fi

# Extract data_dir from patroni.yml
# Format: "  data_dir: /var/lib/postgresql/18/main"
PG_DATA_DIR=$(grep 'data_dir:' "$PATRONI_CONFIG" 2>/dev/null | head -1 | awk '{print $2}' | tr -d '"' | tr -d "'")
if [[ -z "$PG_DATA_DIR" ]]; then
    echo "patroni-force-bootstrap: ERROR — could not extract data_dir from ${PATRONI_CONFIG}"
    exit 1
fi

# SECURITY: Reject path traversal components before prefix check
if [[ "$PG_DATA_DIR" == *".."* ]]; then
    echo "patroni-force-bootstrap: ERROR — data_dir contains '..' — refusing to wipe"
    exit 1
fi

# Canonicalize path (resolve symlinks) for reliable prefix check
PG_DATA_DIR_REAL=$(realpath -m "$PG_DATA_DIR" 2>/dev/null || echo "$PG_DATA_DIR")

# Safety: verify the path looks like a PostgreSQL data directory
# Must be under /var/lib/postgresql/ to prevent catastrophic misuse
case "$PG_DATA_DIR_REAL" in
    /var/lib/postgresql/*)
        ;;
    *)
        echo "patroni-force-bootstrap: ERROR — data_dir '${PG_DATA_DIR_REAL}' not under /var/lib/postgresql/ — refusing to wipe"
        exit 1
        ;;
esac
PG_DATA_DIR="$PG_DATA_DIR_REAL"

echo "patroni-force-bootstrap: PG data dir: ${PG_DATA_DIR}"

# Step 1: Stop Patroni
echo "patroni-force-bootstrap: Stopping Patroni..."
systemctl stop patroni 2>/dev/null || true
sleep 2

# Also stop any stray postgres processes that Patroni may have left
# Use exact -D flag match to avoid killing unrelated postgres processes
pkill -9 -f "postgres.*-D ${PG_DATA_DIR}" 2>/dev/null || true
sleep 1

# Step 2: Wipe PostgreSQL data directory
# Use rm -rf on the DIRECTORY ITSELF (not glob) to avoid sudo glob expansion issue
echo "patroni-force-bootstrap: Wiping PostgreSQL data directory..."
if [[ -d "$PG_DATA_DIR" ]]; then
    rm -rf "$PG_DATA_DIR"
fi
mkdir -p "$PG_DATA_DIR"
chown postgres:postgres "$PG_DATA_DIR"
chmod 700 "$PG_DATA_DIR"
echo "patroni-force-bootstrap: Data directory wiped and recreated"

# Step 3: Clear etcd scope (remove initialize key + stale member/leader/config keys)
# This prevents "waiting for leader to bootstrap" loops where Patroni sees
# an initialize key but no leader, and waits indefinitely instead of bootstrapping
echo "patroni-force-bootstrap: Clearing etcd scope..."

# Verify etcd is reachable before attempting delete — if etcd is down,
# Patroni will see stale state and may fail to bootstrap
if ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 | grep -q "is healthy"; then
    ETCDCTL_API=3 etcdctl del /spiralpool/ --prefix 2>/dev/null || true
    echo "patroni-force-bootstrap: etcd scope cleared"
else
    echo "patroni-force-bootstrap: WARNING — etcd not healthy, attempting scope clear anyway..."
    # Try anyway — etcd might recover during Patroni startup
    ETCDCTL_API=3 etcdctl del /spiralpool/ --prefix 2>/dev/null || true
    echo "patroni-force-bootstrap: WARNING — etcd scope clear may have failed. If Patroni hangs, check etcd health."
fi
sleep 1

# Step 4: Start Patroni (will bootstrap fresh as primary with empty data dir + no DCS state)
echo "patroni-force-bootstrap: Starting Patroni for fresh bootstrap..."
if ! systemctl start patroni 2>&1; then
    echo "patroni-force-bootstrap: WARNING — systemctl start patroni returned non-zero, but continuing to check readiness..."
fi

# Step 5: Wait for Patroni to become primary (up to 60 seconds)
# Verify jq is available (silent failure via 2>/dev/null would mask "command not found")
if ! command -v jq >/dev/null 2>&1; then
    echo "patroni-force-bootstrap: WARNING — jq not installed, using grep fallback for role detection"
fi

READY=0
for i in $(seq 1 30); do
    patroni_json=$(curl -s --max-time 3 "http://localhost:8008/patroni" 2>/dev/null || echo "")
    if command -v jq >/dev/null 2>&1; then
        local_role=$(echo "$patroni_json" | jq -r '.role // ""' 2>/dev/null || echo "")
    else
        # Fallback: extract role via grep if jq is missing
        local_role=$(echo "$patroni_json" | grep -oP '"role"\s*:\s*"\K[^"]+' 2>/dev/null || echo "")
    fi
    if [[ "$local_role" == "master" ]] || [[ "$local_role" == "primary" ]]; then
        READY=1
        break
    fi
    sleep 2
done

if [[ $READY -eq 1 ]]; then
    echo "patroni-force-bootstrap: SUCCESS — Patroni bootstrapped as primary"
    exit 0
else
    echo "patroni-force-bootstrap: ERROR — Patroni did not become primary within 60s after fresh bootstrap"
    exit 1
fi
