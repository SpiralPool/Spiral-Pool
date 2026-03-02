#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool HA Add Peer
# Adds a new peer IP to this node's UFW firewall rules and Patroni pg_hba config.
#
# Called remotely during install when a new node joins the HA cluster.
# The installing node SCPs this script and runs it via:
#   sudo /spiralpool/scripts/ha-add-peer.sh <peer_ip>
#
# Idempotent: safe to run multiple times with the same IP.
#
# Exit codes:
#   0 = Success (all rules/entries added or already existed)
#   1 = Failure (invalid input or critical error)
#

set -euo pipefail

PATRONI_YML="/etc/patroni/patroni.yml"
LOG_PREFIX="ha-add-peer"

log()      { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [${LOG_PREFIX}] $*"; }
log_warn() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [${LOG_PREFIX}] WARNING: $*" >&2; }

# ── Validate input ──────────────────────────────────────────────────────────
if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <peer_ip>"
    echo "  Adds UFW firewall rules and Patroni pg_hba entries for the given peer IP."
    exit 1
fi

PEER_IP="$1"

# Validate IP format (basic IPv4 check)
if ! [[ "$PEER_IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "ERROR: Invalid IP address: $PEER_IP"
    exit 1
fi

# Must run as root (for UFW and patroni.yml)
if [[ $EUID -ne 0 ]]; then
    echo "ERROR: This script must be run as root (use sudo)."
    exit 1
fi

# ── UFW Firewall Rules ─────────────────────────────────────────────────────
log "Adding UFW rules for peer ${PEER_IP}..."

# UFW deduplicates — adding an existing rule is a safe no-op
ufw_add() {
    ufw allow "$@" > /dev/null 2>&1 || true
}

ufw_add from "$PEER_IP" to any port 5363 proto udp   # HA cluster discovery
ufw_add from "$PEER_IP" to any port 5354 proto tcp   # HA status API
ufw_add from "$PEER_IP" to any port 5432 proto tcp   # PostgreSQL replication
ufw_add from "$PEER_IP" to any port 2379 proto tcp   # etcd client
ufw_add from "$PEER_IP" to any port 2380 proto tcp   # etcd peer
ufw_add from "$PEER_IP" to any port 8008 proto tcp   # Patroni REST API
ufw allow proto vrrp from "$PEER_IP" > /dev/null 2>&1 || true

log "UFW rules added for ${PEER_IP}"

# ── Patroni pg_hba Entries ──────────────────────────────────────────────────
if [[ ! -f "$PATRONI_YML" ]]; then
    log_warn "Patroni config not found at ${PATRONI_YML} — skipping pg_hba update"
    log "UFW rules were added successfully. pg_hba must be updated manually."
    exit 0
fi

# Detect the replication username from existing patroni.yml entries
REPL_USER=$(grep -oP 'host replication \K\S+' "$PATRONI_YML" 2>/dev/null | head -1 || true)
REPL_USER="${REPL_USER:-replicator}"

# Entries to add (replication + all-access for the peer IP)
REPL_ENTRY="host replication ${REPL_USER} ${PEER_IP}/32 scram-sha-256"
ALL_ENTRY="host all all ${PEER_IP}/32 scram-sha-256"

# Temp file for atomic patroni.yml edits (initialized for trap safety with set -u)
tmp_file=""
trap 'rm -f "$tmp_file"' EXIT

# Check if entries already exist in patroni.yml
if grep -qF "${PEER_IP}/32" "$PATRONI_YML"; then
    log "pg_hba entries for ${PEER_IP} already exist in ${PATRONI_YML} — skipping"
else
    log "Adding pg_hba entries for ${PEER_IP} to ${PATRONI_YML}..."

    # Patroni.yml has TWO pg_hba sections:
    #   1. bootstrap.dcs.postgresql (under bootstrap: > pg_hba:)
    #   2. postgresql (under postgresql: > pg_hba:)
    # Both use YAML list format: "    - host ..."
    #
    # Strategy: Insert new entries before the localhost (127.0.0.1) lines in each section.
    # This keeps localhost entries last (conventional order).

    # Use a temporary file for atomic edit
    tmp_file=$(mktemp)
    cp "$PATRONI_YML" "$tmp_file"

    # Insert before the first "127.0.0.1" line in each pg_hba section.
    # If no 127.0.0.1 line exists, append after the last "host " line in each pg_hba block.
    # sed approach: find lines with "127.0.0.1/32" and insert our entries before the first occurrence.
    #
    # We need to handle the case where the entry should go before 127.0.0.1 replication line.
    # The patroni.yml has entries like:
    #   - host replication replicator 127.0.0.1/32 scram-sha-256
    # We insert our two lines before EACH such 127.0.0.1 occurrence.

    # Count how many 127.0.0.1/32 replication lines exist (should be 2: bootstrap + postgresql sections)
    local_lines=$(grep -c "host replication.*127\.0\.0\.1/32" "$tmp_file" 2>/dev/null || echo "0")

    if [[ "$local_lines" -ge 1 ]]; then
        # Insert before each 127.0.0.1 replication line (two separate sed calls for reliability)
        # insert-before: 1st goes above match, 2nd goes between 1st and match
        # Result: REPL, ALL, 127-repl (matches existing convention)
        sed -i "/host replication.*127\.0\.0\.1\/32/i\\    - ${REPL_ENTRY}" "$tmp_file"
        sed -i "/host replication.*127\.0\.0\.1\/32/i\\    - ${ALL_ENTRY}" "$tmp_file"
    else
        # Fallback: append after the last "host all all" line
        # Find the last line number containing "host all all" and insert after it
        last_line=$(grep -n "host all all" "$tmp_file" 2>/dev/null | tail -1 | cut -d: -f1 || true)
        if [[ -n "$last_line" ]]; then
            # append-after with fixed line number: second append pushes first down
            # So append ALL first, then REPL → produces "replication, all" order
            sed -i "${last_line}a\\    - ${ALL_ENTRY}" "$tmp_file"
            sed -i "${last_line}a\\    - ${REPL_ENTRY}" "$tmp_file"
        else
            log_warn "Could not find insertion point in ${PATRONI_YML} — manual pg_hba update needed"
            rm -f "$tmp_file"
            exit 0
        fi
    fi

    # Validate the edited file is valid YAML (basic check: not empty, has key sections)
    if grep -q "^scope:" "$tmp_file" && grep -q "pg_hba:" "$tmp_file"; then
        cp "$tmp_file" "$PATRONI_YML"
        log "pg_hba entries added to ${PATRONI_YML}"
    else
        log_warn "Edited patroni.yml appears invalid — reverting. Manual pg_hba update needed."
        rm -f "$tmp_file"
        exit 0
    fi
    rm -f "$tmp_file"
fi

# ── Reload Patroni ──────────────────────────────────────────────────────────
if systemctl is-active --quiet patroni 2>/dev/null; then
    log "Reloading Patroni to apply pg_hba changes..."
    if patronictl -c "$PATRONI_YML" reload spiralpool-postgres --force 2>/dev/null; then
        log "Patroni reloaded successfully"
    else
        # Fallback: SIGHUP to Patroni process
        if pkill -HUP -f "patroni.*patroni.yml" 2>/dev/null; then
            log "Sent SIGHUP to Patroni process"
        else
            log_warn "Could not reload Patroni — pg_hba changes will apply on next restart"
        fi
    fi
else
    log "Patroni is not running — pg_hba changes will apply when Patroni starts"
fi

log "Peer ${PEER_IP} added successfully"
exit 0
