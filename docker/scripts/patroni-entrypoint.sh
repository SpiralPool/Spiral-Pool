#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║                                                                            ║
# ║           SPIRAL POOL - PATRONI ENTRYPOINT SCRIPT                          ║
# ║                                                                            ║
# ║   Initializes Patroni with environment-based configuration                 ║
# ║                                                                            ║
# ╚════════════════════════════════════════════════════════════════════════════╝

set -e

# Set defaults for environment variables (envsubst doesn't handle ${VAR:-default} syntax)
export PATRONI_NAME="${PATRONI_NAME:-postgres1}"
export PATRONI_HOST="${PATRONI_HOST:-127.0.0.1}"
export PATRONI_REST_PASSWORD="${PATRONI_REST_PASSWORD:?PATRONI_REST_PASSWORD is required}"
export ETCD_HOST="${ETCD_HOST:-etcd1}"
export ETCD_HOST2="${ETCD_HOST2:-etcd2}"
export ETCD_HOST3="${ETCD_HOST3:-etcd3}"
export REPLICATION_PASSWORD="${REPLICATION_PASSWORD:?REPLICATION_PASSWORD is required}"
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
export REWIND_PASSWORD="${REWIND_PASSWORD:?REWIND_PASSWORD is required}"
export DB_USER="${DB_USER:-spiralstratum}"
export DB_NAME="${DB_NAME:-spiralstratum}"

# Generate Patroni configuration from template with environment variables
# SECURITY: Set umask before writing to prevent passwords being briefly world-readable
umask 077
envsubst < /etc/patroni/patroni.yml.template > /etc/patroni/patroni.yml
umask 022

# Ensure data directory exists and has correct permissions
mkdir -p /var/lib/postgresql/data
chmod 700 /var/lib/postgresql/data

# Replication and rewind users are created by Patroni via bootstrap config
# (postgresql.authentication section in patroni.yml).
# Application user and database are created by post_bootstrap script
# (patroni-post-bootstrap.sh, referenced in patroni.yml bootstrap section).

echo "Starting Patroni..."
exec patroni /etc/patroni/patroni.yml
