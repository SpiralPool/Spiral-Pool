#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║           SPIRAL STRATUM - DOCKER ENTRYPOINT SCRIPT                        ║
# ║                                                                            ║
# ║   Generates config.yaml from environment variables, then starts            ║
# ║   supervisord to manage the stratum process.                               ║
# ║                                                                            ║
# ║   POOL_MODE=single  → V1 single-coin (template + envsubst)               ║
# ║   POOL_MODE=multi   → V2 multi-coin (programmatic YAML generation)       ║
# ║                                                                            ║
# ║   V2 Enhanced Stratum: set STRATUM_V2_ENABLED=true in .env                ║
# ║   Noise encryption keys generated in memory — no certs needed             ║
# ╚════════════════════════════════════════════════════════════════════════════╝

set -e

CONFIG_TEMPLATE="/spiralpool/config/config.docker.template"
CONFIG_OUTPUT="/spiralpool/config/config.yaml"
POOL_MODE="${POOL_MODE:-single}"

echo "=== Spiral Stratum Entrypoint ==="
echo "Pool mode: $POOL_MODE"

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

# Stratum difficulty defaults (overridable via .env)
export STRATUM_DIFF_INITIAL="${STRATUM_DIFF_INITIAL:-5000}"
export STRATUM_DIFF_MIN="${STRATUM_DIFF_MIN:-0.001}"
export STRATUM_DIFF_MAX="${STRATUM_DIFF_MAX:-1000000000000}"
export STRATUM_VARDIFF_TARGET_TIME="${STRATUM_VARDIFF_TARGET_TIME:-4}"

# AsicBoost / Version Rolling (required for S19/Vnish firmware)
export STRATUM_VERSION_ROLLING="${STRATUM_VERSION_ROLLING:-true}"
export STRATUM_VERSION_ROLLING_MASK="${STRATUM_VERSION_ROLLING_MASK:-536862720}"

# Stratum V2 (Noise Protocol — keys generated in memory, no certs needed)
export STRATUM_V2_ENABLED="${STRATUM_V2_ENABLED:-false}"

# ═══════════════════════════════════════════════════════════════════════════════
# Validate based on pool mode
# ═══════════════════════════════════════════════════════════════════════════════
if [ "$POOL_MODE" = "single" ]; then
    # Single-coin mode: require POOL_COIN, POOL_ADDRESS, POOL_ID
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
elif [ "$POOL_MODE" != "multi" ]; then
    echo "ERROR: POOL_MODE must be 'single' or 'multi' (got: $POOL_MODE)"
    exit 1
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Auto-detect daemon connection from POOL_COIN (single-coin mode only)
# ═══════════════════════════════════════════════════════════════════════════════
# Derives host, ports, and RPC credentials from coin-specific env vars.
# Users can override any value by setting DAEMON_HOST, DAEMON_RPC_PORT, etc.
# in .env (advanced use only — auto-detection handles all standard setups).
if [ "$POOL_MODE" = "single" ]; then
case "$POOL_COIN" in
    digibyte)
        export DAEMON_HOST="${DAEMON_HOST:-digibyte}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-14022}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${DGB_RPC_USER:-spiraldgb}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${DGB_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28532}"
        export STRATUM_PORT="${STRATUM_PORT:-3333}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-3334}"
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
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-3337}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-3338}"
        ;;
    bitcoin)
        export DAEMON_HOST="${DAEMON_HOST:-bitcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8332}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${BTC_RPC_USER:-spiralbtc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${BTC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28332}"
        export STRATUM_PORT="${STRATUM_PORT:-4333}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-4334}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-4335}"
        ;;
    bitcoincash)
        export DAEMON_HOST="${DAEMON_HOST:-bitcoincash}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8432}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${BCH_RPC_USER:-spiralbch}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${BCH_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28432}"
        export STRATUM_PORT="${STRATUM_PORT:-5333}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-5334}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-5335}"
        ;;
    bitcoinii)
        export DAEMON_HOST="${DAEMON_HOST:-bitcoinii}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8339}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${BC2_RPC_USER:-spiralbc2}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${BC2_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28338}"
        export STRATUM_PORT="${STRATUM_PORT:-6333}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-6334}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-6335}"
        ;;
    namecoin)
        export DAEMON_HOST="${DAEMON_HOST:-namecoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8336}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${NMC_RPC_USER:-spiralnmc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${NMC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28336}"
        export STRATUM_PORT="${STRATUM_PORT:-14335}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-14336}"
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
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-15336}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-15337}"
        ;;
    myriadcoin)
        export DAEMON_HOST="${DAEMON_HOST:-myriadcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-10889}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${XMY_RPC_USER:-spiralxmy}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${XMY_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28889}"
        export STRATUM_PORT="${STRATUM_PORT:-17335}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-17336}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-17337}"
        ;;
    fractalbitcoin)
        export DAEMON_HOST="${DAEMON_HOST:-fractalbitcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8340}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${FBTC_RPC_USER:-spiralfbtc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${FBTC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28340}"
        export STRATUM_PORT="${STRATUM_PORT:-18335}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-18336}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-18337}"
        ;;
    qbitx)
        export DAEMON_HOST="${DAEMON_HOST:-qbitx}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-8344}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${QBX_RPC_USER:-spiralqbx}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${QBX_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28344}"
        export STRATUM_PORT="${STRATUM_PORT:-20335}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-20336}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-20337}"
        ;;
    litecoin)
        export DAEMON_HOST="${DAEMON_HOST:-litecoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-9332}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${LTC_RPC_USER:-spiralltc}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${LTC_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28933}"
        export STRATUM_PORT="${STRATUM_PORT:-7333}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-7334}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-7335}"
        ;;
    dogecoin)
        export DAEMON_HOST="${DAEMON_HOST:-dogecoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-22555}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${DOGE_RPC_USER:-spiraldoge}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${DOGE_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28555}"
        export STRATUM_PORT="${STRATUM_PORT:-8335}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-8337}"
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
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-10336}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-10337}"
        ;;
    catcoin)
        export DAEMON_HOST="${DAEMON_HOST:-catcoin}"
        export DAEMON_RPC_PORT="${DAEMON_RPC_PORT:-9932}"
        export DAEMON_RPC_USER="${DAEMON_RPC_USER:-${CAT_RPC_USER:-spiralcat}}"
        export DAEMON_RPC_PASSWORD="${DAEMON_RPC_PASSWORD:-${CAT_RPC_PASSWORD:-}}"
        export DAEMON_ZMQ_PORT="${DAEMON_ZMQ_PORT:-28932}"
        export STRATUM_PORT="${STRATUM_PORT:-12335}"
        export STRATUM_PORT_V2="${STRATUM_PORT_V2:-12336}"
        export STRATUM_PORT_TLS="${STRATUM_PORT_TLS:-12337}"
        ;;
    *)
        echo "ERROR: Unknown POOL_COIN: $POOL_COIN"
        echo "Valid: digibyte, dgb-scrypt, bitcoin, bitcoincash, bitcoinii, namecoin, syscoin,"
        echo "       myriadcoin, fractalbitcoin, qbitx, litecoin, dogecoin, pepecoin, catcoin"
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

# Set V2 listen address for single-coin mode template
if [ "$STRATUM_V2_ENABLED" = "true" ]; then
    export STRATUM_LISTEN_V2="0.0.0.0:${STRATUM_PORT_V2}"
    echo "Stratum V2 enabled (Noise NX encryption) on port $STRATUM_PORT_V2"
else
    export STRATUM_LISTEN_V2=""
fi

fi  # end single-coin mode auto-detection

if [ -z "$DB_PASSWORD" ]; then
    echo "ERROR: DB_PASSWORD is required"
    exit 1
fi

# ═══════════════════════════════════════════════════════════════════════════════
# V2 Multi-Coin Config Generator
# ═══════════════════════════════════════════════════════════════════════════════
# Mirrors generate_docker_stratum_config_multicoin() from install.sh.
# Generates a V2 config.yaml with per-coin pool sections, merge mining, and
# shared global/database settings.

generate_v2_config() {
    local enabled_coins=0

    # ── Build merge mining YAML fragments ─────────────────────────────────────
    local sha256d_merge_yaml=""
    local scrypt_merge_yaml=""

    if [ "${MERGE_MINING_ENABLED:-false}" = "true" ]; then
        local aux_check="${MERGE_MINING_AUX_CHAINS_SHA256D:-},${MERGE_MINING_AUX_CHAINS_SCRYPT:-}"
        local algo="${MERGE_MINING_ALGO:-sha256d}"

        # SHA-256d aux chains
        local sha256d_aux=""
        if [ "$algo" = "sha256d" ] || [ "$algo" = "both" ]; then
            if echo "$aux_check" | grep -q "NMC"; then
                sha256d_aux="${sha256d_aux}
        - symbol: \"NMC\"
          enabled: true
          address: \"${NMC_POOL_ADDRESS}\"
          daemon:
            host: \"namecoin\"
            port: 8336
            user: \"${NMC_RPC_USER:-spiralnmc}\"
            password: \"${NMC_RPC_PASSWORD}\""
            fi
            if echo "$aux_check" | grep -q "SYS"; then
                sha256d_aux="${sha256d_aux}
        - symbol: \"SYS\"
          enabled: true
          address: \"${SYS_POOL_ADDRESS}\"
          daemon:
            host: \"syscoin\"
            port: 8370
            user: \"${SYS_RPC_USER:-spiralsys}\"
            password: \"${SYS_RPC_PASSWORD}\""
            fi
            if echo "$aux_check" | grep -q "XMY"; then
                sha256d_aux="${sha256d_aux}
        - symbol: \"XMY\"
          enabled: true
          address: \"${XMY_POOL_ADDRESS}\"
          daemon:
            host: \"myriadcoin\"
            port: 10889
            user: \"${XMY_RPC_USER:-spiralxmy}\"
            password: \"${XMY_RPC_PASSWORD}\""
            fi
            if echo "$aux_check" | grep -q "FBTC"; then
                sha256d_aux="${sha256d_aux}
        - symbol: \"FBTC\"
          enabled: true
          address: \"${FBTC_POOL_ADDRESS}\"
          daemon:
            host: \"fractalbitcoin\"
            port: 8340
            user: \"${FBTC_RPC_USER:-spiralfbtc}\"
            password: \"${FBTC_RPC_PASSWORD}\""
            fi
        fi
        if [ -n "$sha256d_aux" ]; then
            sha256d_merge_yaml="
    mergeMining:
      enabled: true
      refreshInterval: 5s
      auxChains:${sha256d_aux}"
        fi

        # Scrypt aux chains
        local scrypt_aux=""
        if [ "$algo" = "scrypt" ] || [ "$algo" = "both" ]; then
            if echo "$aux_check" | grep -q "DOGE"; then
                scrypt_aux="${scrypt_aux}
        - symbol: \"DOGE\"
          enabled: true
          address: \"${DOGE_POOL_ADDRESS}\"
          daemon:
            host: \"dogecoin\"
            port: 22555
            user: \"${DOGE_RPC_USER:-spiraldoge}\"
            password: \"${DOGE_RPC_PASSWORD}\""
            fi
            if echo "$aux_check" | grep -q "PEP"; then
                scrypt_aux="${scrypt_aux}
        - symbol: \"PEP\"
          enabled: true
          address: \"${PEP_POOL_ADDRESS}\"
          daemon:
            host: \"pepecoin\"
            port: 33873
            user: \"${PEP_RPC_USER:-spiralpep}\"
            password: \"${PEP_RPC_PASSWORD}\""
            fi
        fi
        if [ -n "$scrypt_aux" ]; then
            scrypt_merge_yaml="
    mergeMining:
      enabled: true
      refreshInterval: 5s
      auxChains:${scrypt_aux}"
        fi
    fi

    # ── Build per-coin YAML sections ──────────────────────────────────────────
    local coins_yaml=""
    local v2_enabled="$STRATUM_V2_ENABLED"

    # --- SHA-256d coins ---

    if [ "${ENABLE_DGB:-false}" = "true" ]; then
        local dgb_merge=""
        # DGB gets SHA256d merge mining only if BTC is not the parent
        if [ "${ENABLE_BTC:-false}" != "true" ] && [ -n "$sha256d_merge_yaml" ]; then
            dgb_merge="$sha256d_merge_yaml"
        fi
        local dgb_v2_line=""
        [ "$v2_enabled" = "true" ] && dgb_v2_line="
      port_v2: 3334"
        coins_yaml="${coins_yaml}
  - symbol: \"DGB\"
    pool_id: \"dgb_sha256_1\"
    enabled: true
    address: \"${DGB_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 3333${dgb_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 10000000
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"digibyte\"
        port: 14022
        user: \"${DGB_RPC_USER:-spiraldgb}\"
        password: \"${DGB_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://digibyte:28532\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\"${dgb_merge}"
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_BTC:-false}" = "true" ]; then
        local btc_v2_line=""
        [ "$v2_enabled" = "true" ] && btc_v2_line="
      port_v2: 4334"
        coins_yaml="${coins_yaml}
  - symbol: \"BTC\"
    pool_id: \"btc_sha256_1\"
    enabled: true
    address: \"${BTC_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 4333${btc_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"bitcoin\"
        port: 8332
        user: \"${BTC_RPC_USER:-spiralbtc}\"
        password: \"${BTC_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://bitcoin:28332\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\"${sha256d_merge_yaml}"
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_BCH:-false}" = "true" ]; then
        local bch_v2_line=""
        [ "$v2_enabled" = "true" ] && bch_v2_line="
      port_v2: 5334"
        coins_yaml="${coins_yaml}
  - symbol: \"BCH\"
    pool_id: \"bch_sha256_1\"
    enabled: true
    address: \"${BCH_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 5333${bch_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"bitcoincash\"
        port: 8432
        user: \"${BCH_RPC_USER:-spiralbch}\"
        password: \"${BCH_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://bitcoincash:28432\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_BC2:-false}" = "true" ]; then
        local bc2_v2_line=""
        [ "$v2_enabled" = "true" ] && bc2_v2_line="
      port_v2: 6334"
        coins_yaml="${coins_yaml}
  - symbol: \"BC2\"
    pool_id: \"bc2_sha256_1\"
    enabled: true
    address: \"${BC2_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 6333${bc2_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"bitcoinii\"
        port: 8339
        user: \"${BC2_RPC_USER:-spiralbc2}\"
        password: \"${BC2_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://bitcoinii:28338\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_NMC:-false}" = "true" ]; then
        local nmc_v2_line=""
        [ "$v2_enabled" = "true" ] && nmc_v2_line="
      port_v2: 14336"
        coins_yaml="${coins_yaml}
  - symbol: \"NMC\"
    pool_id: \"nmc_sha256_1\"
    enabled: true
    address: \"${NMC_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 14335${nmc_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"namecoin\"
        port: 8336
        user: \"${NMC_RPC_USER:-spiralnmc}\"
        password: \"${NMC_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://namecoin:28336\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_SYS:-false}" = "true" ]; then
        local sys_v2_line=""
        [ "$v2_enabled" = "true" ] && sys_v2_line="
      port_v2: 15336"
        coins_yaml="${coins_yaml}
  - symbol: \"SYS\"
    pool_id: \"sys_sha256_1\"
    enabled: true
    address: \"${SYS_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 15335${sys_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 6
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"syscoin\"
        port: 8370
        user: \"${SYS_RPC_USER:-spiralsys}\"
        password: \"${SYS_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://syscoin:28370\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_XMY:-false}" = "true" ]; then
        local xmy_v2_line=""
        [ "$v2_enabled" = "true" ] && xmy_v2_line="
      port_v2: 17336"
        coins_yaml="${coins_yaml}
  - symbol: \"XMY\"
    pool_id: \"xmy_sha256_1\"
    enabled: true
    address: \"${XMY_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 17335${xmy_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 6
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"myriadcoin\"
        port: 10889
        user: \"${XMY_RPC_USER:-spiralxmy}\"
        password: \"${XMY_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://myriadcoin:28889\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_FBTC:-false}" = "true" ]; then
        local fbtc_v2_line=""
        [ "$v2_enabled" = "true" ] && fbtc_v2_line="
      port_v2: 18336"
        coins_yaml="${coins_yaml}
  - symbol: \"FBTC\"
    pool_id: \"fbtc_sha256_1\"
    enabled: true
    address: \"${FBTC_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 18335${fbtc_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 4
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"fractalbitcoin\"
        port: 8340
        user: \"${FBTC_RPC_USER:-spiralfbtc}\"
        password: \"${FBTC_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://fractalbitcoin:28340\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_QBX:-false}" = "true" ]; then
        local qbx_v2_line=""
        [ "$v2_enabled" = "true" ] && qbx_v2_line="
      port_v2: 20336"
        coins_yaml="${coins_yaml}
  - symbol: \"QBX\"
    pool_id: \"qbx_sha256_1\"
    enabled: true
    address: \"${QBX_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 20335${qbx_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 100000000
          targetTime: 8
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"qbitx\"
        port: 8344
        user: \"${QBX_RPC_USER:-spiralqbx}\"
        password: \"${QBX_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://qbitx:28344\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    # --- Scrypt coins ---

    if [ "${ENABLE_LTC:-false}" = "true" ]; then
        local ltc_v2_line=""
        [ "$v2_enabled" = "true" ] && ltc_v2_line="
      port_v2: 7334"
        coins_yaml="${coins_yaml}
  - symbol: \"LTC\"
    pool_id: \"ltc_scrypt_1\"
    enabled: true
    address: \"${LTC_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 7333${ltc_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 65536
          targetTime: 8
          retargetTime: 60
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"litecoin\"
        port: 9332
        user: \"${LTC_RPC_USER:-spiralltc}\"
        password: \"${LTC_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://litecoin:28933\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\"${scrypt_merge_yaml}"
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_DOGE:-false}" = "true" ]; then
        local doge_v2_line=""
        [ "$v2_enabled" = "true" ] && doge_v2_line="
      port_v2: 8337"
        coins_yaml="${coins_yaml}
  - symbol: \"DOGE\"
    pool_id: \"doge_scrypt_1\"
    enabled: true
    address: \"${DOGE_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 8335${doge_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 65536
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"dogecoin\"
        port: 22555
        user: \"${DOGE_RPC_USER:-spiraldoge}\"
        password: \"${DOGE_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://dogecoin:28555\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_DGB_SCRYPT:-false}" = "true" ]; then
        local dgbs_v2_line=""
        [ "$v2_enabled" = "true" ] && dgbs_v2_line="
      port_v2: 3337"
        coins_yaml="${coins_yaml}
  - symbol: \"DGB-SCRYPT\"
    pool_id: \"dgb_scrypt_1\"
    enabled: true
    address: \"${DGB_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 3336${dgbs_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 65536
          targetTime: 3
          retargetTime: 45
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"digibyte\"
        port: 14022
        user: \"${DGB_RPC_USER:-spiraldgb}\"
        password: \"${DGB_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://digibyte:28532\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_PEP:-false}" = "true" ]; then
        local pep_v2_line=""
        [ "$v2_enabled" = "true" ] && pep_v2_line="
      port_v2: 10336"
        coins_yaml="${coins_yaml}
  - symbol: \"PEP\"
    pool_id: \"pep_scrypt_1\"
    enabled: true
    address: \"${PEP_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 10335${pep_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 65536
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"pepecoin\"
        port: 33873
        user: \"${PEP_RPC_USER:-spiralpep}\"
        password: \"${PEP_RPC_PASSWORD}\"
        zmq:
          enabled: false
          endpoint: \"tcp://pepecoin:28873\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    if [ "${ENABLE_CAT:-false}" = "true" ]; then
        local cat_v2_line=""
        [ "$v2_enabled" = "true" ] && cat_v2_line="
      port_v2: 12336"
        coins_yaml="${coins_yaml}
  - symbol: \"CAT\"
    pool_id: \"cat_scrypt_1\"
    enabled: true
    address: \"${CAT_POOL_ADDRESS}\"
    coinbase_text: \"${COINBASE_TEXT}\"
    stratum:
      port: 12335${cat_v2_line}
      difficulty:
        varDiff:
          enabled: true
          minDiff: 0.001
          maxDiff: 65536
          targetTime: 8
          retargetTime: 90
          variancePercent: 30
    nodes:
      - id: \"primary\"
        host: \"catcoin\"
        port: 9932
        user: \"${CAT_RPC_USER:-spiralcat}\"
        password: \"${CAT_RPC_PASSWORD}\"
        zmq:
          enabled: true
          endpoint: \"tcp://catcoin:28932\"
    payments:
      enabled: true
      interval: 600s
      minimum_payment: 0.01
      scheme: \"SOLO\""
        enabled_coins=$((enabled_coins + 1))
    fi

    # ── Validate at least one coin enabled ────────────────────────────────────
    if [ "$enabled_coins" -eq 0 ]; then
        echo "ERROR: POOL_MODE=multi but no coins are enabled."
        echo "       Set ENABLE_<COIN>=true for at least one coin (e.g., ENABLE_DGB=true)"
        exit 1
    fi

    # ── Build webhook YAML ────────────────────────────────────────────────────
    local webhook_yaml=""
    if [ -n "${DISCORD_WEBHOOK_URL:-}" ]; then
        webhook_yaml="    webhooks:
      - url: \"${DISCORD_WEBHOOK_URL}\""
        if [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && [ -n "${TELEGRAM_CHAT_ID:-}" ]; then
            webhook_yaml="${webhook_yaml}
      - url: \"https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage\"
        chat_id: \"${TELEGRAM_CHAT_ID}\""
        fi
    elif [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && [ -n "${TELEGRAM_CHAT_ID:-}" ]; then
        webhook_yaml="    webhooks:
      - url: \"https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage\"
        chat_id: \"${TELEGRAM_CHAT_ID}\""
    fi

    # ── Write V2 config.yaml ──────────────────────────────────────────────────
    cat > "${CONFIG_OUTPUT}.tmp" << CONFIGEOF
# Spiral Stratum V2 Configuration
# Multi-Coin Docker Configuration - Generated $(date)

version: 2

global:
  log_level: "info"
  log_format: "json"
  metrics_port: 9100
  api_port: 4000
  api_enabled: true
  admin_api_key: "${ADMIN_API_KEY}"
  rate_limiting:
    enabled: true
    requests_per_second: 10

  sentinel:
    enabled: true
    check_interval: 60s
    wal_stuck_threshold: 10m
    alert_cooldown: 15m
${webhook_yaml}

  celebration:
    enabled: true
    duration_hours: 2

coins:${coins_yaml}

database:
  host: "${DB_HOST}"
  port: ${DB_PORT}
  user: "${DB_USER}"
  password: "${DB_PASSWORD}"
  database: "${DB_NAME}"
  sslMode: "disable"
  maxConnections: 200
  batching:
    size: 1000
    interval: 3s
CONFIGEOF

    if [ $? -ne 0 ]; then
        echo "ERROR: Failed to write multi-coin config to ${CONFIG_OUTPUT}.tmp"
        rm -f "${CONFIG_OUTPUT}.tmp"
        exit 1
    fi
    mv "${CONFIG_OUTPUT}.tmp" "${CONFIG_OUTPUT}"
    chmod 600 "${CONFIG_OUTPUT}"

    echo "V2 multi-coin configuration generated:"
    echo "  Coins enabled: $enabled_coins"
    echo "  Database:      $DB_HOST:$DB_PORT/$DB_NAME"
    if [ "$v2_enabled" = "true" ]; then
        echo "  Stratum V2:    enabled (Noise NX encryption)"
    fi
    if [ "${MERGE_MINING_ENABLED:-false}" = "true" ]; then
        echo "  Merge mining:  ${MERGE_MINING_ALGO} (SHA256d aux: ${MERGE_MINING_AUX_CHAINS_SHA256D:-none}, Scrypt aux: ${MERGE_MINING_AUX_CHAINS_SCRYPT:-none})"
    fi
}

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
    # Build SANs: localhost + container hostname + 127.0.0.1
    san="DNS:localhost,DNS:$(hostname),IP:127.0.0.1"
    lan_ips=$(ip -4 addr show 2>/dev/null | grep -oP 'inet \K[0-9.]+' | grep -v '^127\.' || true)
    for ip in $lan_ips; do
        san="${san},IP:${ip}"
    done

    if ! openssl req -x509 \
        -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
        -keyout "$TLS_KEY_FILE" -out "$TLS_CERT_FILE" \
        -sha256 -days 3650 -nodes \
        -subj "/O=Spiral Pool/CN=$(hostname)" \
        -addext "subjectAltName=${san}" \
        -addext "basicConstraints=critical,CA:FALSE" \
        -addext "keyUsage=critical,digitalSignature" \
        -addext "extendedKeyUsage=serverAuth" \
        2>/dev/null; then
        echo "WARNING: TLS certificate generation failed — stratum TLS may not work"
    else
        chmod 600 "$TLS_KEY_FILE"
        echo "TLS certificate generated: $TLS_CERT_FILE"
    fi
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Generate config.yaml based on pool mode
# ═══════════════════════════════════════════════════════════════════════════════

if [ "$POOL_MODE" = "single" ]; then
    # ─── Single-coin V1: envsubst on template ─────────────────────────────────
    echo "Generating single-coin configuration from template..."

    if [ ! -f "$CONFIG_TEMPLATE" ]; then
        echo "ERROR: Config template not found at $CONFIG_TEMPLATE"
        exit 1
    fi

    # R-12 FIX: Atomic write (tempfile + mv).
    # Commands split so set -e catches envsubst failure (left side of && is exempt).
    envsubst < "$CONFIG_TEMPLATE" > "${CONFIG_OUTPUT}.tmp"
    mv "${CONFIG_OUTPUT}.tmp" "${CONFIG_OUTPUT}"
    chmod 600 "${CONFIG_OUTPUT}"

    echo "Configuration generated:"
    echo "  Coin:        $POOL_COIN"
    echo "  Pool ID:     $POOL_ID"
    echo "  Address:     $POOL_ADDRESS"
    echo "  Daemon:      $DAEMON_HOST:$DAEMON_RPC_PORT"
    echo "  Database:    $DB_HOST:$DB_PORT/$DB_NAME"
    echo "  Stratum V1:  0.0.0.0:$STRATUM_PORT"
    if [ "$STRATUM_V2_ENABLED" = "true" ]; then
        echo "  Stratum V2:  0.0.0.0:$STRATUM_PORT_V2 (Noise NX encryption)"
    fi
    echo "  Stratum TLS: 0.0.0.0:$STRATUM_PORT_TLS"

else
    # ─── Multi-coin V2: programmatic YAML generation ──────────────────────────
    echo "Generating multi-coin V2 configuration..."
    generate_v2_config
fi

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
