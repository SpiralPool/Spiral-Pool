#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║           SPIRAL STRATUM - DOCKER ENTRYPOINT SCRIPT                        ║
# ║                                                                            ║
# ║   Generates config.yaml from template using environment variables,         ║
# ║   then starts supervisord to manage the stratum process.                   ║
# ║                                                                            ║
# ║   Docker supports V1 single-coin solo mining only (plain + TLS).          ║
# ║   For V2 Enhanced Stratum, multi-coin, or merge mining,                   ║
# ║   use native installation: sudo ./install.sh                               ║
# ╚════════════════════════════════════════════════════════════════════════════╝

set -e

CONFIG_TEMPLATE="/spiralpool/config/config.docker.template"
CONFIG_OUTPUT="/spiralpool/config/config.yaml"

echo "=== Spiral Stratum Entrypoint ==="
echo "Generating configuration from template..."

# Set defaults for non-coin-specific environment variables
export TLS_CERT_FILE="${TLS_CERT_FILE:-/spiralpool/tls/stratum.crt}"
export TLS_KEY_FILE="${TLS_KEY_FILE:-/spiralpool/tls/stratum.key}"
export COINBASE_TEXT="${COINBASE_TEXT:-Mined by Spiral Pool}"
export DB_HOST="${DB_HOST:-postgres}"
export DB_PORT="${DB_PORT:-5432}"
export DB_USER="${DB_USER:-spiralstratum}"
export DB_NAME="${DB_NAME:-spiralstratum}"
export ADMIN_API_KEY="${ADMIN_API_KEY:-}"
export SPIRAL_METRICS_TOKEN="${SPIRAL_METRICS_TOKEN:-}"
export POOL_ID="${POOL_ID:-}"
export REDIS_DEDUP_ENABLED="${REDIS_DEDUP_ENABLED:-false}"
export REDIS_DEDUP_ADDR="${REDIS_DEDUP_ADDR:-}"
export REDIS_DEDUP_PASSWORD="${REDIS_DEDUP_PASSWORD:-}"
export DAEMON_ZMQ_ENABLED="${DAEMON_ZMQ_ENABLED:-true}"

# Validate required environment variables
if [ -z "$POOL_COIN" ]; then
    echo "ERROR: POOL_COIN is required (e.g., bitcoin, litecoin, digibyte, bitcoincash, bitcoinii, dogecoin)"
    exit 1
fi

if [ -z "$POOL_ADDRESS" ]; then
    echo "ERROR: POOL_ADDRESS is required"
    exit 1
fi

if [ -z "$POOL_ID" ]; then
    echo "ERROR: POOL_ID is required (format: <symbol>_<algo>_<n>, e.g., dgb_sha256_1)"
    exit 1
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Auto-detect daemon connection from POOL_COIN
# ═══════════════════════════════════════════════════════════════════════════════
# Derives host, ports, and RPC credentials from coin-specific env vars.
# Users can override any value by setting DAEMON_HOST, DAEMON_RPC_PORT, etc.
# in .env (advanced use only — auto-detection handles all standard setups).
# NOTE: STRATUM_PORT_V2 is not used — V2 Enhanced Stratum requires native install.
case "$POOL_COIN" in
    digibyte)
        export DAEMON_HOST="${DAEMON_HOST:-digibyte}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-14022}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${DGB_RPC_USER:-spiraldgb}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${DGB_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28532}"
        export STRATUM_PORT="${STRATUM_PORT:-3333}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-3335}"
        ;;
    dgb-scrypt|digibyte-scrypt)
        # DGB-SCRYPT shares the DigiByte daemon but uses different stratum ports
        export DAEMON_HOST="${DAEMON_HOST:-digibyte}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-14022}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${DGB_RPC_USER:-spiraldgb}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${DGB_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28532}"
        export STRATUM_PORT="${STRATUM_PORT:-3336}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-3338}"
        ;;
    bitcoin)
        export DAEMON_HOST="${DAEMON_HOST:-bitcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8332}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${BTC_RPC_USER:-spiralbtc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${BTC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28332}"
        export STRATUM_PORT="${STRATUM_PORT:-4333}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-4335}"
        ;;
    bitcoincash)
        export DAEMON_HOST="${DAEMON_HOST:-bitcoincash}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8432}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${BCH_RPC_USER:-spiralbch}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${BCH_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28432}"
        export STRATUM_PORT="${STRATUM_PORT:-5333}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-5335}"
        ;;
    bitcoinii)
        export DAEMON_HOST="${DAEMON_HOST:-bitcoinii}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8339}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${BC2_RPC_USER:-spiralbc2}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${BC2_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28338}"
        export STRATUM_PORT="${STRATUM_PORT:-6333}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-6335}"
        ;;
    namecoin)
        export DAEMON_HOST="${DAEMON_HOST:-namecoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8336}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${NMC_RPC_USER:-spiralnmc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${NMC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28336}"
        export STRATUM_PORT="${STRATUM_PORT:-14335}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-14337}"
        ;;
    syscoin)
        echo "WARNING: Syscoin is merge-mining only and requires a BTC parent chain."
        echo "         Launch with: docker compose --profile sys --profile btc up -d"
        export DAEMON_HOST="${DAEMON_HOST:-syscoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8370}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${SYS_RPC_USER:-spiralsys}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${SYS_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28370}"
        export STRATUM_PORT="${STRATUM_PORT:-15335}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-15337}"
        ;;
    myriadcoin)
        export DAEMON_HOST="${DAEMON_HOST:-myriadcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-10889}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${XMY_RPC_USER:-spiralxmy}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${XMY_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28889}"
        export STRATUM_PORT="${STRATUM_PORT:-17335}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-17337}"
        ;;
    fractalbitcoin)
        export DAEMON_HOST="${DAEMON_HOST:-fractalbitcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8340}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${FBTC_RPC_USER:-spiralfbtc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${FBTC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28340}"
        export STRATUM_PORT="${STRATUM_PORT:-18335}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-18337}"
        ;;
    litecoin)
        export DAEMON_HOST="${DAEMON_HOST:-litecoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-9332}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${LTC_RPC_USER:-spiralltc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${LTC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28933}"
        export STRATUM_PORT="${STRATUM_PORT:-7333}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-7335}"
        ;;
    dogecoin)
        export DAEMON_HOST="${DAEMON_HOST:-dogecoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-22555}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${DOGE_RPC_USER:-spiraldoge}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${DOGE_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28555}"
        export STRATUM_PORT="${STRATUM_PORT:-8335}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-8342}"
        ;;
    pepecoin)
        export DAEMON_HOST="${DAEMON_HOST:-pepecoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-33873}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${PEP_RPC_USER:-spiralpep}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${PEP_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28873}"
        export DAEMON_ZMQ_ENABLED="false"  # PepeCoin v1.1.0 compiled without ZMQ support
        export STRATUM_PORT="${STRATUM_PORT:-10335}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-10337}"
        ;;
    catcoin)
        export DAEMON_HOST="${DAEMON_HOST:-catcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-9932}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${CAT_RPC_USER:-spiralcat}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${CAT_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28932}"
        export STRATUM_PORT="${STRATUM_PORT:-12335}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-12337}"
        ;;
    *)
        echo "ERROR: Unknown POOL_COIN: $POOL_COIN"
        echo "Valid: digibyte, dgb-scrypt, bitcoin, bitcoincash, bitcoinii, namecoin, syscoin,"
        echo "       myriadcoin, fractalbitcoin, litecoin, dogecoin, pepecoin, catcoin"
        exit 1
        ;;
esac

# Validate daemon credentials (after coin auto-detection)
if [ -z "$DAEMON_RPC_PASSWORD" ]; then
    echo "ERROR: No RPC password found for $POOL_COIN."
    echo "       Set the coin-specific password in .env (e.g., DGB_RPC_PASSWORD)"
    echo "       or run: ./generate-secrets.sh"
    exit 1
fi

if [ -z "$DB_PASSWORD" ]; then
    echo "ERROR: DB_PASSWORD is required"
    exit 1
fi

# Check if template exists
if [ ! -f "$CONFIG_TEMPLATE" ]; then
    echo "ERROR: Config template not found at $CONFIG_TEMPLATE"
    exit 1
fi

# Generate self-signed TLS certificate if not provided
# The default TLS path (/spiralpool/tls/) may be a read-only bind mount.
# If we can't write there, generate to /spiralpool/data/tls/ (writable volume)
# and update the env vars so the config template uses the correct paths.
if [ ! -f "$TLS_CERT_FILE" ]; then
    echo "Generating self-signed TLS certificate..."
    tls_dir="$(dirname "$TLS_CERT_FILE")"
    if ! mkdir -p "$tls_dir" 2>/dev/null || ! touch "$tls_dir/.write-test" 2>/dev/null; then
        echo "  TLS directory $tls_dir is read-only, using /spiralpool/data/tls/ instead"
        tls_dir="/spiralpool/data/tls"
        mkdir -p "$tls_dir"
        export TLS_CERT_FILE="$tls_dir/stratum.crt"
        export TLS_KEY_FILE="$tls_dir/stratum.key"
    else
        rm -f "$tls_dir/.write-test"
    fi
    openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 \
        -nodes -keyout "$TLS_KEY_FILE" -out "$TLS_CERT_FILE" \
        -subj "/CN=spiralpool/O=SpiralPool/C=US" 2>/dev/null
    echo "TLS certificate generated: $TLS_CERT_FILE"
fi

# R-12 FIX: Generate config from template using atomic write (tempfile + mv).
# If power is lost mid-write, the .tmp file is orphaned but config.yaml stays intact.
envsubst < "$CONFIG_TEMPLATE" > "${CONFIG_OUTPUT}.tmp" && mv "${CONFIG_OUTPUT}.tmp" "${CONFIG_OUTPUT}"
chmod 600 "${CONFIG_OUTPUT}"

echo "Configuration generated:"
echo "  Coin:        $POOL_COIN"
echo "  Pool ID:     $POOL_ID"
echo "  Address:     $POOL_ADDRESS"
echo "  Daemon:      $DAEMON_HOST:$DAEMON_RPC_PORT"
echo "  Database:    $DB_HOST:$DB_PORT/$DB_NAME"
echo "  Stratum V1:  0.0.0.0:$STRATUM_PORT"
echo "  Stratum TLS: 0.0.0.0:$STRATUM_PORT_TLS"
echo ""
echo "  Docker supports V1 single-coin solo mining only."
echo "  For V2 Enhanced Stratum, multi-coin, or merge mining: sudo ./install.sh"

# Write metrics token file for Prometheus bearer_token_file (shared volume).
# Always write the file (even if empty) so Prometheus doesn't error on missing file.
# When token is empty, bearer_token_file sends an empty Authorization header which
# the stratum accepts as "no auth required" (authToken is also empty).
echo -n "${SPIRAL_METRICS_TOKEN}" > /spiralpool/data/.metrics_token
chmod 600 /spiralpool/data/.metrics_token
if [ -n "$SPIRAL_METRICS_TOKEN" ]; then
    echo "Metrics auth enabled — token written to /spiralpool/data/.metrics_token"
fi

# R-2 FIX: Wait for PostgreSQL to be ready before starting pool services.
# After power loss, PG may need 30-120s for WAL recovery. Without this check,
# supervisord starts spiralpool immediately → DB writes fail → circuit breaker
# opens → shares go to WAL only → pool reports 0 TH/s (repeat of Feb 13 incident).
echo "Waiting for PostgreSQL to be ready..."
pg_ready=0
for i in $(seq 1 120); do
    if pg_isready -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" >/dev/null 2>&1; then
        echo "PostgreSQL is ready (waited ${i}s)"
        pg_ready=1
        break
    fi
    if [ "$i" -eq 1 ] || [ $((i % 10)) -eq 0 ]; then
        echo "  Waiting for PostgreSQL at ${DB_HOST}:${DB_PORT}... (${i}/120s)"
    fi
    sleep 1
done
if [ "$pg_ready" -eq 0 ]; then
    echo "ERROR: PostgreSQL not ready after 120 seconds — aborting startup"
    exit 1
fi

# Start supervisord
echo "Starting supervisord..."
exec /usr/bin/supervisord -c /etc/supervisor/conf.d/spiralpool.conf -n
