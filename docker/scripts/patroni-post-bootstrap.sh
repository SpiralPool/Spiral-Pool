#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║           SPIRAL POOL - PATRONI POST-BOOTSTRAP SCRIPT                      ║
# ║                                                                            ║
# ║   Runs ONCE after Patroni initializes a fresh cluster.                     ║
# ║   Creates the application user, database, and base tables.                 ║
# ║                                                                            ║
# ║   Patroni passes the PostgreSQL connection string as $1.                   ║
# ║   Environment variables (DB_USER, DB_PASSWORD, DB_NAME) are inherited      ║
# ║   from the container environment set in docker-compose.                    ║
# ╚════════════════════════════════════════════════════════════════════════════╝

set -e

# Patroni passes connection string as first argument
CONNSTR="${1:-}"

APP_USER="${DB_USER:-spiralstratum}"
APP_DB="${DB_NAME:-spiralstratum}"
APP_PASS="${DB_PASSWORD:-}"

# FIX G-6: Escape single quotes in password to prevent SQL injection.
# A password containing ' would break the SQL statement or allow injection.
APP_PASS_ESCAPED="${APP_PASS//\'/\'\'}"

# Escape user/db names for SQL string literals (single quotes) and identifiers (double quotes)
APP_USER_LITERAL="${APP_USER//\'/\'\'}"
APP_USER_IDENT="${APP_USER//\"/\"\"}"
APP_DB_LITERAL="${APP_DB//\'/\'\'}"
APP_DB_IDENT="${APP_DB//\"/\"\"}"

echo "Post-bootstrap: Creating application user '${APP_USER}' and database '${APP_DB}'..."

# Create application user if not exists
psql "$CONNSTR" -c "
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '${APP_USER_LITERAL}') THEN
        CREATE ROLE \"${APP_USER_IDENT}\" WITH LOGIN PASSWORD '${APP_PASS_ESCAPED}' CREATEDB;
    END IF;
END
\$\$;"

# Create application database if not exists
psql "$CONNSTR" -tc "SELECT 1 FROM pg_database WHERE datname = '${APP_DB_LITERAL}'" | grep -q 1 || \
    psql "$CONNSTR" -c "CREATE DATABASE \"${APP_DB_IDENT}\" OWNER \"${APP_USER_IDENT}\";"

# Run init-db.sh if mounted (creates base tables, indexes, grants)
# Use postgres superuser via local socket targeting the app database.
# TCP as APP_USER would fail — pg_hba requires scram-sha-256 and no password is in the connstr.
# init-db.sh uses $POSTGRES_USER and $POSTGRES_DB internally to connect via psql.
INIT_SCRIPT="/docker-entrypoint-initdb.d/init-db.sh"
if [ -f "$INIT_SCRIPT" ]; then
    echo "Post-bootstrap: Running init-db.sh..."
    # FIX G-16: Don't suppress stderr — real errors were being hidden.
    # "already exists" notices are expected (IF NOT EXISTS) and non-fatal.
    POSTGRES_USER="postgres" POSTGRES_DB="${APP_DB}" GRANT_USER="${APP_USER}" bash "$INIT_SCRIPT" 2>&1 || \
        echo "Post-bootstrap: WARNING - init-db.sh had errors (check output above)"
fi

echo "Post-bootstrap: Done."
