#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║                                                                            ║
# ║           SPIRAL POOL - AUTO-GENERATE SECRETS                              ║
# ║                                                                            ║
# ║   Generates unique random passwords for all infrastructure services.       ║
# ║                                                                            ║
# ║   USAGE:                                                                   ║
# ║     cp .env.example .env                                                   ║
# ║     bash generate-secrets.sh          # uses .env in current directory     ║
# ║     bash generate-secrets.sh .env     # explicit path                      ║
# ║                                                                            ║
# ║   BEHAVIOR:                                                                ║
# ║     - Replaces all CHANGE_THIS_TO_A_STRONG_PASSWORD placeholders           ║
# ║     - Each placeholder gets a unique 32-character hex password             ║
# ║     - Adds HA infrastructure passwords if not already present              ║
# ║     - Idempotent: won't overwrite existing non-placeholder passwords       ║
# ║     - Passwords persist in .env across docker compose restarts             ║
# ║                                                                            ║
# ║   IMPORTANT: Back up .env after running — passwords cannot be recovered    ║
# ║                                                                            ║
# ║   NOTE: Requires GNU sed (Linux default). On macOS: brew install gnu-sed   ║
# ║                                                                            ║
# ╚════════════════════════════════════════════════════════════════════════════╝

set -e

ENV_FILE="${1:-.env}"

if [ ! -f "$ENV_FILE" ]; then
    echo "ERROR: $ENV_FILE not found."
    echo "Run first: cp .env.example .env"
    exit 1
fi

# Symlink check — prevent writing secrets to unexpected locations
if [ -L "$ENV_FILE" ]; then
    echo "ERROR: $ENV_FILE is a symbolic link. Refusing to modify."
    exit 1
fi

# Restrict file permissions before writing secrets
chmod 600 "$ENV_FILE"

generate_password() {
    local pw
    pw=$(openssl rand -hex 16) || { echo "ERROR: openssl rand failed. Is openssl installed?" >&2; exit 1; }
    if [ -z "$pw" ] || [ ${#pw} -ne 32 ]; then
        echo "ERROR: Generated password is invalid (expected 32 hex chars, got: '${pw}')" >&2
        exit 1
    fi
    echo "$pw"
}

# ═══════════════════════════════════════════════════════════════════════════════
# PHASE 1: Replace all CHANGE_THIS_TO_A_STRONG_PASSWORD placeholders
# ═══════════════════════════════════════════════════════════════════════════════

count=0
while grep -q "CHANGE_THIS_TO_A_STRONG_PASSWORD" "$ENV_FILE"; do
    password=$(generate_password)
    sed -i "0,/CHANGE_THIS_TO_A_STRONG_PASSWORD/{s/CHANGE_THIS_TO_A_STRONG_PASSWORD/$password/}" "$ENV_FILE"
    count=$((count + 1))
done

if [ "$count" -gt 0 ]; then
    echo "Generated $count unique password(s) for placeholder entries."
else
    echo "No placeholder passwords found (already generated or manually set)."
fi

# Also handle CHANGE_THIS_TO_A_STRONG_KEY variant (used by ADMIN_API_KEY)
while grep -q "CHANGE_THIS_TO_A_STRONG_KEY" "$ENV_FILE"; do
    password=$(generate_password)
    sed -i "0,/CHANGE_THIS_TO_A_STRONG_KEY/{s/CHANGE_THIS_TO_A_STRONG_KEY/$password/}" "$ENV_FILE"
    count=$((count + 1))
done

# ═══════════════════════════════════════════════════════════════════════════════
# PHASE 2: Add HA infrastructure passwords if not present
# ═══════════════════════════════════════════════════════════════════════════════

added=0

add_if_missing() {
    local var_name="$1"
    local comment="$2"
    if ! grep -q "^${var_name}=" "$ENV_FILE"; then
        password=$(generate_password)
        echo "" >> "$ENV_FILE"
        if [ -n "$comment" ]; then
            echo "# $comment" >> "$ENV_FILE"
        fi
        echo "${var_name}=${password}" >> "$ENV_FILE"
        echo "  + ${var_name}"
        added=$((added + 1))
    fi
}

echo ""
echo "Checking HA infrastructure passwords..."
add_if_missing "REDIS_PASSWORD" "Redis authentication password (HA mode)"
add_if_missing "REPLICATION_PASSWORD" "Patroni PostgreSQL replication password (HA mode)"
add_if_missing "REWIND_PASSWORD" "Patroni pg_rewind password (HA mode)"
add_if_missing "PATRONI_REST_PASSWORD" "Patroni REST API password (HA mode)"

if [ "$added" -eq 0 ]; then
    echo "  All HA passwords already present."
fi

echo ""
echo "Done. All passwords in $ENV_FILE are unique and random."
echo ""
echo "IMPORTANT: Back up this .env file — passwords cannot be recovered if lost."
echo "  cp .env .env.backup"
