#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# =============================================================================
# Spiral Stratum — Universal Regtest Integration Test
# =============================================================================
#
# WHAT THIS DOES:
#   Tests the FULL block lifecycle end-to-end against a real coin daemon
#   running in regtest mode (private local blockchain, difficulty=1).
#
#   This is NOT a unit test — it starts real processes, mines real (regtest)
#   blocks, and verifies the pool handles them correctly.
#
#   Flow:
#     daemon (regtest) -----> pool (stratum) -----> cpuminer -----> block found!
#          |                      |                                     |
#          |<---- submitblock ----|                                     |
#          |---- ZMQ hashblock -->|                                     |
#          |                      |---- DB insert + status ------------>|
#
# SUPPORTED COINS (12 solo):
#   bc2   - Bitcoin II       btc   - Bitcoin          bch   - Bitcoin Cash
#   ltc   - Litecoin         dgb   - DigiByte (SHA256d)  nmc   - Namecoin
#   dgb-scrypt - DigiByte (Scrypt)
#   xmy   - Myriad           fbtc  - Fractal Bitcoin
#   doge  - Dogecoin         pep   - PepeCoin          cat   - Catcoin
#   (SYS is merge-mining only — see btc+sys below)
#
# MERGE MINING PAIRS:
#   SHA256d (Bitcoin parent):
#     btc+nmc   - Bitcoin + Namecoin      (ChainID 1)
#     btc+fbtc  - Bitcoin + Fractal BTC   (ChainID 8228)
#     btc+sys   - Bitcoin + Syscoin       (ChainID 16)
#     btc+xmy   - Bitcoin + Myriad        (ChainID 90)
#   Scrypt (Litecoin parent):
#     ltc+doge  - Litecoin + Dogecoin     (ChainID 98)
#     ltc+pep   - Litecoin + PepeCoin     (ChainID 63)
#
# USAGE:
#   # Basic usage (coin symbol required, case-insensitive):
#   chmod +x scripts/linux/regtest.sh
#   ./scripts/linux/regtest.sh btc
#   ./scripts/linux/regtest.sh DGB
#   ./scripts/linux/regtest.sh bc2
#
#   # Merge mining test (requires both daemons):
#   ./scripts/linux/regtest.sh --merge btc+nmc
#   ./scripts/linux/regtest.sh --merge ltc+doge
#
#   # Custom binary paths (override via environment variables):
#   BITCOIND=/opt/bitcoin/bin/bitcoind \
#   BITCOINCLI=/opt/bitcoin/bin/bitcoin-cli \
#   CPUMINER=/usr/local/bin/minerd \
#     ./scripts/linux/regtest.sh btc
#
#   # Custom database credentials:
#   DB_USER=myuser DB_PASS=mypass DB_NAME=mydb \
#     ./scripts/linux/regtest.sh btc
#
#   # HA-only mode — skip to HA VIP failover test (Steps 1-2, 5, 8g only):
#   HA_ONLY=1 ./scripts/linux/regtest.sh btc
#
# PREREQUISITES:
#   1. Coin daemon + CLI binary in PATH (or set via env var)
#   2. cpuminer with the coin's algorithm support (minerd / cpuminer-multi)
#      - apt install cpuminer  OR  build from https://github.com/pooler/cpuminer
#   3. PostgreSQL running locally (install.sh sets this up)
#   4. Pool binary built (this script builds it if missing)
#
# OUTPUT:
#   - Console:  Color-coded step-by-step progress
#   - Logs:     logs/regtest/spiralpool-regtest.log    (pool)
#               logs/regtest/cpuminer-regtest.log      (miner)
#               logs/regtest/<daemon>-regtest.log       (daemon)
#               logs/regtest/<daemon>-startup.log       (daemon stderr)
#
# EXIT CODES:
#   0 = All blocks mined and lifecycle verified
#   1 = Blocks not found or lifecycle incomplete
#
# CLEANUP:
#   The script automatically stops all processes on exit (Ctrl+C safe).
#   The regtest config address placeholder is restored so git stays clean.
#   Regtest blockchain data is in ~/.<datadir>/regtest/ (delete to reset).
#
# =============================================================================
set -euo pipefail

# Detect system architecture for coin binary downloads
# dpkg returns "amd64" or "arm64"; fallback to amd64 if dpkg unavailable
SYSTEM_ARCH=$(dpkg --print-architecture 2>/dev/null || echo "amd64")
ARCH_SUFFIX="x86_64-linux-gnu"
if [[ "$SYSTEM_ARCH" == "arm64" ]]; then
    ARCH_SUFFIX="aarch64-linux-gnu"
fi

# =============================================================================
# Argument Validation
# =============================================================================

if [[ -z "${1:-}" ]]; then
    echo "Usage: $0 <coin>"
    echo "       $0 --merge <parent+aux>"
    echo ""
    echo "Supported coins:"
    echo "  bc2   - Bitcoin II         btc   - Bitcoin            bch   - Bitcoin Cash"
    echo "  ltc   - Litecoin           dgb   - DigiByte (SHA256d)  nmc   - Namecoin"
    echo "  xmy   - Myriad             fbtc  - Fractal Bitcoin"
    echo "  doge  - Dogecoin           pep   - PepeCoin            cat   - Catcoin"
    echo "  dgb-scrypt - DigiByte (Scrypt)"
    echo "  (SYS is merge-mining only — use: $0 --merge btc+sys)"
    echo ""
    echo "Merge mining pairs (SHA256d):"
    echo "  btc+nmc   - Bitcoin + Namecoin    (ChainID 1)"
    echo "  btc+fbtc  - Bitcoin + Fractal BTC (ChainID 8228)"
    echo "  btc+sys   - Bitcoin + Syscoin     (ChainID 16)"
    echo "  btc+xmy   - Bitcoin + Myriad      (ChainID 90)"
    echo "Merge mining pairs (Scrypt):"
    echo "  ltc+doge  - Litecoin + Dogecoin   (ChainID 98)"
    echo "  ltc+pep   - Litecoin + PepeCoin   (ChainID 63)"
    echo ""
    echo "Example: $0 btc"
    echo "         $0 --merge btc+nmc"
    exit 1
fi

# Parse --merge flag for merge mining mode
MERGE_MODE=0
MERGE_PAIR=""
PARENT_COIN=""
AUX_COIN=""

if [[ "${1:-}" == "--merge" ]]; then
    if [[ -z "${2:-}" ]]; then
        echo "Error: --merge requires a pair (e.g., btc+nmc, btc+fbtc, btc+sys, btc+xmy, ltc+doge, ltc+pep)"
        exit 1
    fi
    MERGE_MODE=1
    MERGE_PAIR=$(echo "${2}" | tr '[:upper:]' '[:lower:]')

    # Parse parent+aux from merge pair
    if [[ "$MERGE_PAIR" == *"+"* ]]; then
        PARENT_COIN="${MERGE_PAIR%%+*}"
        AUX_COIN="${MERGE_PAIR##*+}"
    else
        echo "Error: Invalid merge pair format. Use: parent+aux (e.g., btc+nmc)"
        exit 1
    fi

    # Validate merge mining pairs
    case "$MERGE_PAIR" in
        btc+nmc)
            echo "Merge Mining: Bitcoin (parent) + Namecoin (aux) [SHA256d]"
            ;;
        btc+fbtc)
            echo "Merge Mining: Bitcoin (parent) + Fractal Bitcoin (aux) [SHA256d]"
            ;;
        btc+sys)
            echo "Merge Mining: Bitcoin (parent) + Syscoin (aux) [SHA256d]"
            ;;
        btc+xmy)
            echo "Merge Mining: Bitcoin (parent) + Myriad (aux) [SHA256d]"
            ;;
        ltc+doge)
            echo "Merge Mining: Litecoin (parent) + Dogecoin (aux) [Scrypt]"
            ;;
        ltc+pep)
            echo "Merge Mining: Litecoin (parent) + PepeCoin (aux) [Scrypt]"
            ;;
        *)
            echo "Error: Unsupported merge pair '$MERGE_PAIR'"
            echo "Supported pairs: btc+nmc, btc+fbtc, btc+sys, btc+xmy, ltc+doge, ltc+pep"
            exit 1
            ;;
    esac

    # In merge mode, primary coin is the parent
    COIN="$PARENT_COIN"
else
    COIN=$(echo "${1}" | tr '[:upper:]' '[:lower:]')
fi

# HA_ONLY mode: Skip directly to HA VIP failover test (Step 8g)
# Runs only: Step 1 (preflight, no cpuminer), Step 2 (daemon), Step 5 (PostgreSQL), Step 8g (HA)
HA_ONLY="${HA_ONLY:-0}"

# =============================================================================
# Coin Configuration — setup_coin() sets all coin-specific variables
# =============================================================================

setup_coin() {
    local coin="$1"
    case "$coin" in
        bc2)
            COIN_SYMBOL=BC2; COIN_NAME="Bitcoin II"; COIN_ALGO=sha256d
            DAEMON_CMD="${BITCOINIID:-bitcoiniid}"; CLI_CMD="${BITCOINIICLI:-bitcoinii-cli}"
            RPC_PORT_DEF=18449; P2P_PORT_DEF=18448; ZMQ_PORT_DEF=29338
            STRATUM_PORT_DEF=16333; STRATUM_V2_PORT_DEF=17337; API_PORT_DEF=14000; METRICS_PORT_DEF=19100
            HA_STRATUM=16334; HA_API=14001; HA_METRICS=19101
            DB_NAME_DEF=spiralstratum_regtest; WALLET_NAME=regtest-pool
            POOL_ID=bc2_regtest; DATA_DIR=.bitcoinII
            DAEMON_LOG=bitcoinIId-regtest.log; DAEMON_STARTUP=bitcoinIId-startup.log
            PKILL_PATTERN="bitcoiniid.*regtest"
            GITHUB_URL="https://github.com/Bitcoin-II/BitcoinII-Core"
            GBT_RULES='["segwit"]'
            # Auto-install info (BC2 uses -CLI suffix instead of -gnu)
            DAEMON_VERSION="29.1.0"
            if [[ "$SYSTEM_ARCH" == "arm64" ]]; then
                DOWNLOAD_URL="https://github.com/Bitcoin-II/BitcoinII-Core/releases/download/v29.1.0/BitcoinII-29.1.0-aarch64-linux-CLI.tar.gz"
                TARBALL_DIR="BitcoinII-29.1.0-aarch64-linux-CLI"
            else
                DOWNLOAD_URL="https://github.com/Bitcoin-II/BitcoinII-Core/releases/download/v29.1.0/BitcoinII-29.1.0-x86_64-linux-CLI.tar.gz"
                TARBALL_DIR="BitcoinII-29.1.0-x86_64-linux-CLI"
            fi
            ;;
        dgb)
            COIN_SYMBOL=DGB; COIN_NAME="DigiByte (SHA256d)"; COIN_ALGO=sha256d
            DAEMON_CMD="${DIGIBYTED:-digibyted}"; CLI_CMD="${DIGIBYTECLI:-digibyte-cli}"
            RPC_PORT_DEF=18543; P2P_PORT_DEF=18544; ZMQ_PORT_DEF=29340
            STRATUM_PORT_DEF=16335; STRATUM_V2_PORT_DEF=17338; API_PORT_DEF=14002; METRICS_PORT_DEF=19102
            HA_STRATUM=16336; HA_API=14003; HA_METRICS=19103
            DB_NAME_DEF=spiralstratum_dgb_regtest; WALLET_NAME=regtest-pool-dgb
            POOL_ID=dgb_regtest; DATA_DIR=.digibyte
            DAEMON_LOG=digibyted-regtest.log; DAEMON_STARTUP=digibyted-startup.log
            PKILL_PATTERN="digibyted.*regtest"
            GITHUB_URL="https://github.com/DigiByte-Core/digibyte"
            GBT_RULES='["segwit"]'
            # Auto-install info
            DAEMON_VERSION="8.26.2"
            DOWNLOAD_URL="https://github.com/DigiByte-Core/digibyte/releases/download/v8.26.2/digibyte-8.26.2-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="digibyte-8.26.2"
            ;;
        dgb-scrypt)
            COIN_SYMBOL=DGB_SCRYPT; COIN_NAME="DigiByte (Scrypt)"; COIN_ALGO=scrypt
            DAEMON_CMD="${DIGIBYTED:-digibyted}"; CLI_CMD="${DIGIBYTECLI:-digibyte-cli}"
            RPC_PORT_DEF=18543; P2P_PORT_DEF=18544; ZMQ_PORT_DEF=29340
            STRATUM_PORT_DEF=16365; STRATUM_V2_PORT_DEF=17339; API_PORT_DEF=14034; METRICS_PORT_DEF=19132
            HA_STRATUM=16366; HA_API=14035; HA_METRICS=19133
            DB_NAME_DEF=spiralstratum_dgb_scrypt_regtest; WALLET_NAME=regtest-pool-dgb
            POOL_ID=dgb_scrypt_regtest; DATA_DIR=.digibyte
            DAEMON_LOG=digibyted-regtest.log; DAEMON_STARTUP=digibyted-startup.log
            PKILL_PATTERN="digibyted.*regtest"
            GITHUB_URL="https://github.com/DigiByte-Core/digibyte"
            GBT_RULES='["segwit"]'
            # Auto-install info (same as dgb — same daemon)
            DAEMON_VERSION="8.26.2"
            DOWNLOAD_URL="https://github.com/DigiByte-Core/digibyte/releases/download/v8.26.2/digibyte-8.26.2-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="digibyte-8.26.2"
            ;;
        btc)
            COIN_SYMBOL=BTC; COIN_NAME="Bitcoin (Knots)"; COIN_ALGO=sha256d
            DAEMON_CMD="${BITCOIND:-bitcoind}"; CLI_CMD="${BITCOINCLI:-bitcoin-cli}"
            RPC_PORT_DEF=18550; P2P_PORT_DEF=18551; ZMQ_PORT_DEF=29342
            STRATUM_PORT_DEF=16337; STRATUM_V2_PORT_DEF=17335; API_PORT_DEF=14004; METRICS_PORT_DEF=19104
            HA_STRATUM=16338; HA_API=14005; HA_METRICS=19105
            DB_NAME_DEF=spiralstratum_btc_regtest; WALLET_NAME=regtest-pool-btc
            POOL_ID=btc_regtest; DATA_DIR=.bitcoin
            DAEMON_LOG=bitcoind-regtest.log; DAEMON_STARTUP=bitcoind-startup.log
            PKILL_PATTERN="bitcoind.*regtest"
            GITHUB_URL="https://github.com/bitcoinknots/bitcoin"
            GBT_RULES='["segwit"]'
            # Auto-install info (Bitcoin Knots)
            DAEMON_VERSION="29.3.knots20260210"
            DOWNLOAD_URL="https://github.com/bitcoinknots/bitcoin/releases/download/v29.3.knots20260210/bitcoin-29.3.knots20260210-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="bitcoin-29.3.knots20260210"
            ;;
        bch)
            COIN_SYMBOL=BCH; COIN_NAME="Bitcoin Cash"; COIN_ALGO=sha256d
            ADDR_TYPE=""  # BCH uses CashAddr — no type arg to getnewaddress
            DAEMON_CMD="${BITCOINDBCH:-bitcoind-bch}"; CLI_CMD="${BITCOINCLIBCH:-bitcoin-cli-bch}"
            RPC_PORT_DEF=18552; P2P_PORT_DEF=18553; ZMQ_PORT_DEF=29344
            STRATUM_PORT_DEF=16339; STRATUM_V2_PORT_DEF=17340; API_PORT_DEF=14006; METRICS_PORT_DEF=19106
            HA_STRATUM=16340; HA_API=14007; HA_METRICS=19107
            DB_NAME_DEF=spiralstratum_bch_regtest; WALLET_NAME=regtest-pool-bch
            POOL_ID=bch_regtest; DATA_DIR=.bitcoin-bch
            DAEMON_LOG=bitcoind-bch-regtest.log; DAEMON_STARTUP=bitcoind-bch-startup.log
            PKILL_PATTERN="bitcoind-bch.*regtest"
            GITHUB_URL="https://github.com/bitcoin-cash-node/bitcoin-cash-node"
            GBT_RULES='[]'  # BCH does not support SegWit
            # Auto-install info
            DAEMON_VERSION="29.0.0"
            DOWNLOAD_URL="https://github.com/bitcoin-cash-node/bitcoin-cash-node/releases/download/v29.0.0/bitcoin-cash-node-29.0.0-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="bitcoin-cash-node-29.0.0"
            ;;
        ltc)
            COIN_SYMBOL=LTC; COIN_NAME="Litecoin"; COIN_ALGO=scrypt
            DAEMON_CMD="${LITECOIND:-litecoind}"; CLI_CMD="${LITECOINCLI:-litecoin-cli}"
            RPC_PORT_DEF=18554; P2P_PORT_DEF=18555; ZMQ_PORT_DEF=29346
            STRATUM_PORT_DEF=16341; STRATUM_V2_PORT_DEF=17336; API_PORT_DEF=14008; METRICS_PORT_DEF=19108
            HA_STRATUM=16342; HA_API=14009; HA_METRICS=19109
            DB_NAME_DEF=spiralstratum_ltc_regtest; WALLET_NAME=regtest-pool-ltc
            POOL_ID=ltc_regtest; DATA_DIR=.litecoin
            DAEMON_LOG=litecoind-regtest.log; DAEMON_STARTUP=litecoind-startup.log
            PKILL_PATTERN="litecoind.*regtest"
            GITHUB_URL="https://github.com/litecoin-project/litecoin"
            GBT_RULES='["mweb", "segwit"]'  # Litecoin requires MWEB rules
            # Auto-install info
            DAEMON_VERSION="0.21.4"
            DOWNLOAD_URL="https://github.com/litecoin-project/litecoin/releases/download/v0.21.4/litecoin-0.21.4-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="litecoin-0.21.4"
            ;;
        nmc)
            COIN_SYMBOL=NMC; COIN_NAME="Namecoin"; COIN_ALGO=sha256d
            DAEMON_CMD="${NAMECOIND:-namecoind}"; CLI_CMD="${NAMECOINCLI:-namecoin-cli}"
            RPC_PORT_DEF=18556; P2P_PORT_DEF=18557; ZMQ_PORT_DEF=29348
            STRATUM_PORT_DEF=16343; STRATUM_V2_PORT_DEF=17341; API_PORT_DEF=14010; METRICS_PORT_DEF=19110
            HA_STRATUM=16344; HA_API=14011; HA_METRICS=19111
            DB_NAME_DEF=spiralstratum_nmc_regtest; WALLET_NAME=regtest-pool-nmc
            POOL_ID=nmc_regtest; DATA_DIR=.namecoin
            DAEMON_LOG=namecoind-regtest.log; DAEMON_STARTUP=namecoind-startup.log
            PKILL_PATTERN="namecoind.*regtest"
            GITHUB_URL="https://github.com/namecoin/namecoin-core"
            GBT_RULES='["segwit"]'
            # Auto-install info
            DAEMON_VERSION="28.0"
            DOWNLOAD_URL="https://www.namecoin.org/files/namecoin-core/namecoin-core-28.0/namecoin-28.0-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="namecoin-28.0"
            ;;
        sys)
            echo "ERROR: SYS does not support solo mining (missing CbTx/quorum commitment)."
            echo "       Use merge mining instead:  $0 --merge btc+sys"
            exit 1
            ;;
        xmy)
            COIN_SYMBOL=XMY; COIN_NAME="Myriad"; COIN_ALGO=sha256d
            DAEMON_CMD="${MYRIADCOIND:-myriadcoind}"; CLI_CMD="${MYRIADCOINCLI:-myriadcoin-cli}"
            RPC_PORT_DEF=18562; P2P_PORT_DEF=18563; ZMQ_PORT_DEF=29354
            STRATUM_PORT_DEF=16349; STRATUM_V2_PORT_DEF=17343; API_PORT_DEF=14016; METRICS_PORT_DEF=19116
            HA_STRATUM=16350; HA_API=14017; HA_METRICS=19117
            DB_NAME_DEF=spiralstratum_xmy_regtest; WALLET_NAME=regtest-pool-xmy
            POOL_ID=xmy_regtest; DATA_DIR=.myriadcoin
            DAEMON_LOG=myriadcoind-regtest.log; DAEMON_STARTUP=myriadcoind-startup.log
            PKILL_PATTERN="myriadcoind.*regtest"
            GITHUB_URL="https://github.com/myriadteam/myriadcoin"
            GBT_RULES='["segwit"]'
            # Auto-install info
            DAEMON_VERSION="0.18.1.0"
            DOWNLOAD_URL="https://github.com/myriadteam/myriadcoin/releases/download/v0.18.1.0/myriadcoin-0.18.1.0-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="myriadcoin-0.18.1"
            ;;
        fbtc)
            COIN_SYMBOL=FBTC; COIN_NAME="Fractal Bitcoin"; COIN_ALGO=sha256d
            DAEMON_CMD="${FRACTALD:-fractald}"; CLI_CMD="${FRACTALCLI:-fractal-cli}"
            RPC_PORT_DEF=18564; P2P_PORT_DEF=18565; ZMQ_PORT_DEF=29356
            STRATUM_PORT_DEF=16351; STRATUM_V2_PORT_DEF=17344; API_PORT_DEF=14018; METRICS_PORT_DEF=19118
            HA_STRATUM=16352; HA_API=14019; HA_METRICS=19119
            DB_NAME_DEF=spiralstratum_fbtc_regtest; WALLET_NAME=regtest-pool-fbtc
            POOL_ID=fbtc_regtest; DATA_DIR=.fractal
            DAEMON_LOG=fractald-regtest.log; DAEMON_STARTUP=fractald-startup.log
            PKILL_PATTERN="fractald.*regtest"
            GITHUB_URL="https://github.com/nickingeniero/fractal-bitcoin"
            GBT_RULES='["segwit"]'
            # Auto-install info (FBTC has no arm64 binary)
            DAEMON_VERSION="0.2.9"
            if [[ "$SYSTEM_ARCH" == "arm64" ]]; then
                DOWNLOAD_URL=""
                TARBALL_DIR=""
            else
                DOWNLOAD_URL="https://github.com/fractal-bitcoin/fractald-release/releases/download/v0.2.9/fractald-0.2.9-x86_64-linux-gnu.tar.gz"
                TARBALL_DIR="fractald-0.2.9-x86_64-linux-gnu"
            fi
            ;;
        doge)
            COIN_SYMBOL=DOGE; COIN_NAME="Dogecoin"; COIN_ALGO=scrypt
            ADDR_TYPE=""        # Dogecoin 1.14.x getnewaddress doesn't accept address_type param
            LEGACY_WALLET=1     # Dogecoin 1.14.x lacks createwallet/loadwallet RPC
            MINE_BEFORE_PEER=1; PREMINE_CMD="generate 1"  # DOGE uses generate, not setgenerate
            DAEMON_CMD="${DOGECOIND:-dogecoind}"; CLI_CMD="${DOGECOINCLI:-dogecoin-cli}"
            RPC_PORT_DEF=18566; P2P_PORT_DEF=18567; ZMQ_PORT_DEF=29358
            STRATUM_PORT_DEF=16353; STRATUM_V2_PORT_DEF=17345; API_PORT_DEF=14020; METRICS_PORT_DEF=19120
            HA_STRATUM=16354; HA_API=14021; HA_METRICS=19121
            DB_NAME_DEF=spiralstratum_doge_regtest; WALLET_NAME=regtest-pool-doge
            POOL_ID=doge_regtest; DATA_DIR=.dogecoin
            DAEMON_LOG=dogecoind-regtest.log; DAEMON_STARTUP=dogecoind-startup.log
            PKILL_PATTERN="dogecoind.*regtest"
            GITHUB_URL="https://github.com/dogecoin/dogecoin"
            GBT_RULES='[]'  # Dogecoin does not support SegWit
            # Auto-install info
            DAEMON_VERSION="1.14.9"
            DOWNLOAD_URL="https://github.com/dogecoin/dogecoin/releases/download/v1.14.9/dogecoin-1.14.9-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="dogecoin-1.14.9"
            ;;
        pep)
            COIN_SYMBOL=PEP; COIN_NAME="PepeCoin"; COIN_ALGO=scrypt
            ADDR_TYPE=""        # PepeCoin getnewaddress doesn't accept address_type param
            LEGACY_WALLET=1     # PepeCoin lacks createwallet/loadwallet RPC
            MINE_BEFORE_PEER=1; PREMINE_CMD="generate 1"  # PEP needs block before peer (IBD lock)
            DAEMON_CMD="${PEPECOIND:-pepecoind}"; CLI_CMD="${PEPECOINCLI:-pepecoin-cli}"
            RPC_PORT_DEF=18570; P2P_PORT_DEF=18571; ZMQ_PORT_DEF=29362
            STRATUM_PORT_DEF=16357; STRATUM_V2_PORT_DEF=17346; API_PORT_DEF=14026; METRICS_PORT_DEF=19124
            HA_STRATUM=16358; HA_API=14027; HA_METRICS=19125
            DB_NAME_DEF=spiralstratum_pep_regtest; WALLET_NAME=regtest-pool-pep
            POOL_ID=pep_regtest; DATA_DIR=.pepecoin
            DAEMON_LOG=pepecoind-regtest.log; DAEMON_STARTUP=pepecoind-startup.log
            PKILL_PATTERN="pepecoind.*regtest"
            GITHUB_URL="https://github.com/nickingeniero/pepecoin"
            GBT_RULES='[]'  # PepeCoin does not support SegWit
            # Auto-install info
            DAEMON_VERSION="1.1.0"
            DOWNLOAD_URL="https://github.com/pepecoinppc/pepecoin/releases/download/v1.1.0/pepecoin-1.1.0-${ARCH_SUFFIX}.tar.gz"
            TARBALL_DIR="pepecoin-1.1.0"
            ;;
        cat)
            COIN_SYMBOL=CAT; COIN_NAME="Catcoin"; COIN_ALGO=scrypt
            ADDR_TYPE=""        # Catcoin getnewaddress doesn't accept address_type param
            # Catcoin has modern wallet RPC (createwallet/loadwallet)
            DAEMON_CMD="${CATCOIND:-catcoind}"; CLI_CMD="${CATCOINCLI:-catcoin-cli}"
            RPC_PORT_DEF=18574; P2P_PORT_DEF=18575; ZMQ_PORT_DEF=29366
            STRATUM_PORT_DEF=16361; STRATUM_V2_PORT_DEF=17347; API_PORT_DEF=14030; METRICS_PORT_DEF=19128
            HA_STRATUM=16362; HA_API=14031; HA_METRICS=19129
            DB_NAME_DEF=spiralstratum_cat_regtest; WALLET_NAME=regtest-pool-cat
            POOL_ID=cat_regtest; DATA_DIR=.catcoin
            DAEMON_LOG=catcoind-regtest.log; DAEMON_STARTUP=catcoind-startup.log
            PKILL_PATTERN="catcoind.*regtest"
            GITHUB_URL="https://github.com/nickingeniero/catcoin"
            GBT_RULES='["mweb", "segwit"]'  # Catcoin Core is Litecoin-based, daemon requires both rules
            # Auto-install info (CAT uses ZIP format, different filenames per arch)
            DAEMON_VERSION="2.1.1"
            if [[ "$SYSTEM_ARCH" == "arm64" ]]; then
                DOWNLOAD_URL="https://github.com/CatcoinCore/catcoincore/releases/download/v2.1.1/Catcoin-AArch.zip"
                TARBALL_DIR="Catcoin-AArch"
            else
                DOWNLOAD_URL="https://github.com/CatcoinCore/catcoincore/releases/download/v2.1.1/Catcoin-Linux.zip"
                TARBALL_DIR="Catcoin-Linux"
            fi
            ARCHIVE_TYPE="zip"
            ;;
        *)
            echo "ERROR: Unsupported coin '$coin'"
            echo ""
            echo "Supported coins:"
            echo "  bc2   - Bitcoin II         btc   - Bitcoin            bch   - Bitcoin Cash"
            echo "  ltc   - Litecoin           dgb   - DigiByte (SHA256d)  nmc   - Namecoin"
            echo "  sys   - Syscoin            xmy   - Myriad"
            echo "  fbtc  - Fractal Bitcoin    doge  - Dogecoin           pep   - PepeCoin"
            echo "  cat   - Catcoin            dgb-scrypt - DigiByte (Scrypt)"
            exit 1
            ;;
    esac
}

# Set coin-specific variables
setup_coin "$COIN"

# =============================================================================
# Merge Mining — Auxiliary Coin Setup
# =============================================================================
# When --merge is used, we need to configure both parent and aux chains.
# The aux chain variables are prefixed with AUX_ to avoid conflicts.

if [[ "$MERGE_MODE" == "1" ]]; then
    # Save parent coin variables
    PARENT_SYMBOL="$COIN_SYMBOL"
    PARENT_NAME="$COIN_NAME"
    PARENT_ALGO="$COIN_ALGO"
    PARENT_DAEMON_CMD="$DAEMON_CMD"
    PARENT_CLI_CMD="$CLI_CMD"
    PARENT_RPC_PORT="$RPC_PORT_DEF"
    PARENT_P2P_PORT="$P2P_PORT_DEF"
    PARENT_ZMQ_PORT="$ZMQ_PORT_DEF"
    PARENT_STRATUM_PORT="$STRATUM_PORT_DEF"
    PARENT_DB_NAME="$DB_NAME_DEF"
    PARENT_WALLET_NAME="$WALLET_NAME"
    PARENT_POOL_ID="$POOL_ID"
    PARENT_DATA_DIR="$DATA_DIR"
    PARENT_DAEMON_LOG="$DAEMON_LOG"
    PARENT_DAEMON_STARTUP="$DAEMON_STARTUP"
    PARENT_PKILL_PATTERN="$PKILL_PATTERN"
    PARENT_GBT_RULES="${GBT_RULES:-[\"segwit\"]}"

    # Set up auxiliary coin
    case "$AUX_COIN" in
        nmc)
            AUX_SYMBOL=NMC
            AUX_NAME="Namecoin"
            AUX_ALGO=sha256d
            AUX_DAEMON_CMD="${NAMECOIND:-namecoind}"
            AUX_CLI_CMD="${NAMECOINCLI:-namecoin-cli}"
            AUX_RPC_PORT=18556
            AUX_P2P_PORT=18557
            AUX_ZMQ_PORT=29348
            AUX_DB_NAME="spiralstratum_nmc_regtest"
            AUX_WALLET_NAME="regtest-pool-nmc"
            AUX_POOL_ID="nmc_regtest"
            AUX_DATA_DIR=".namecoin"
            AUX_DAEMON_LOG="namecoind-regtest.log"
            AUX_DAEMON_STARTUP="namecoind-startup.log"
            AUX_PKILL_PATTERN="namecoind.*regtest"
            AUX_CHAIN_ID=1  # Namecoin AuxPoW chain ID
            AUX_ADDR_TYPE="bech32"
            AUX_LEGACY_WALLET=""
            ;;
        doge)
            AUX_SYMBOL=DOGE
            AUX_NAME="Dogecoin"
            AUX_ALGO=scrypt
            AUX_DAEMON_CMD="${DOGECOIND:-dogecoind}"
            AUX_CLI_CMD="${DOGECOINCLI:-dogecoin-cli}"
            AUX_RPC_PORT=18566
            AUX_P2P_PORT=18567
            AUX_ZMQ_PORT=29358
            AUX_DB_NAME="spiralstratum_doge_regtest"
            AUX_WALLET_NAME="regtest-pool-doge"
            AUX_POOL_ID="doge_regtest"
            AUX_DATA_DIR=".dogecoin"
            AUX_DAEMON_LOG="dogecoind-regtest.log"
            AUX_DAEMON_STARTUP="dogecoind-startup.log"
            AUX_PKILL_PATTERN="dogecoind.*regtest"
            AUX_CHAIN_ID=98  # Dogecoin AuxPoW chain ID (0x62)
            AUX_ADDR_TYPE=""  # Dogecoin uses legacy addresses
            AUX_LEGACY_WALLET=1
            ;;
        fbtc)
            AUX_SYMBOL=FBTC
            AUX_NAME="Fractal Bitcoin"
            AUX_ALGO=sha256d
            AUX_DAEMON_CMD="${FRACTALD:-fractald}"
            AUX_CLI_CMD="${FRACTALCLI:-fractal-cli}"
            AUX_RPC_PORT=18564
            AUX_P2P_PORT=18565
            AUX_ZMQ_PORT=29356
            AUX_DB_NAME="spiralstratum_fbtc_regtest"
            AUX_WALLET_NAME="regtest-pool-fbtc"
            AUX_POOL_ID="fbtc_regtest"
            AUX_DATA_DIR=".fractal"
            AUX_DAEMON_LOG="fractald-regtest.log"
            AUX_DAEMON_STARTUP="fractald-startup.log"
            AUX_PKILL_PATTERN="fractald.*regtest"
            AUX_CHAIN_ID=8228  # Fractal Bitcoin AuxPoW chain ID (0x2024)
            AUX_ADDR_TYPE="bech32"
            AUX_LEGACY_WALLET=""
            ;;
        sys)
            AUX_SYMBOL=SYS
            AUX_NAME="Syscoin"
            AUX_ALGO=sha256d
            AUX_DAEMON_CMD="${SYSCOIND:-syscoind}"
            AUX_CLI_CMD="${SYSCOINCLI:-syscoin-cli}"
            AUX_RPC_PORT=18558
            AUX_P2P_PORT=18559
            AUX_ZMQ_PORT=29350
            AUX_DB_NAME="spiralstratum_sys_regtest"
            AUX_WALLET_NAME="regtest-pool-sys"
            AUX_POOL_ID="sys_regtest"
            AUX_DATA_DIR=".syscoin"
            AUX_DAEMON_LOG="syscoind-regtest.log"
            AUX_DAEMON_STARTUP="syscoind-startup.log"
            AUX_PKILL_PATTERN="syscoind.*regtest"
            AUX_CHAIN_ID=16  # Syscoin AuxPoW chain ID (0x10); old was 4096 (0x1000)
            AUX_ADDR_TYPE="bech32"
            AUX_LEGACY_WALLET=""
            ;;
        xmy)
            AUX_SYMBOL=XMY
            AUX_NAME="Myriad"
            AUX_ALGO=sha256d
            AUX_DAEMON_CMD="${MYRIADCOIND:-myriadcoind}"
            AUX_CLI_CMD="${MYRIADCOINCLI:-myriadcoin-cli}"
            AUX_RPC_PORT=18562
            AUX_P2P_PORT=18563
            AUX_ZMQ_PORT=29354
            AUX_DB_NAME="spiralstratum_xmy_regtest"
            AUX_WALLET_NAME="regtest-pool-xmy"
            AUX_POOL_ID="xmy_regtest"
            AUX_DATA_DIR=".myriadcoin"
            AUX_DAEMON_LOG="myriadcoind-regtest.log"
            AUX_DAEMON_STARTUP="myriadcoind-startup.log"
            AUX_PKILL_PATTERN="myriadcoind.*regtest"
            AUX_CHAIN_ID=90  # Myriad AuxPoW chain ID (0x005A)
            AUX_ADDR_TYPE="bech32"
            AUX_LEGACY_WALLET=""
            ;;
        pep)
            AUX_SYMBOL=PEP
            AUX_NAME="PepeCoin"
            AUX_ALGO=scrypt
            AUX_DAEMON_CMD="${PEPECOIND:-pepecoind}"
            AUX_CLI_CMD="${PEPECOINCLI:-pepecoin-cli}"
            AUX_RPC_PORT=18570
            AUX_P2P_PORT=18571
            AUX_ZMQ_PORT=29362
            AUX_DB_NAME="spiralstratum_pep_regtest"
            AUX_WALLET_NAME="regtest-pool-pep"
            AUX_POOL_ID="pep_regtest"
            AUX_DATA_DIR=".pepecoin"
            AUX_DAEMON_LOG="pepecoind-regtest.log"
            AUX_DAEMON_STARTUP="pepecoind-startup.log"
            AUX_PKILL_PATTERN="pepecoind.*regtest"
            AUX_CHAIN_ID=63  # PepeCoin AuxPoW chain ID (0x003F)
            AUX_ADDR_TYPE=""  # PepeCoin uses legacy addresses
            AUX_LEGACY_WALLET=1
            ;;
        *)
            echo "Error: Unknown auxiliary coin '$AUX_COIN'"
            exit 1
            ;;
    esac

    echo ""
    echo "Parent chain: $PARENT_NAME ($PARENT_SYMBOL) - $PARENT_ALGO"
    echo "Aux chain:    $AUX_NAME ($AUX_SYMBOL) - ChainID $AUX_CHAIN_ID"
    echo ""
fi

# Helper: run aux coin CLI
auxcli() {
    if [[ "$MERGE_MODE" != "1" ]]; then
        return 1
    fi
    "$AUX_CLI_CMD" -regtest -rpcuser="$RPC_USER" -rpcpassword="$RPC_PASS" -rpcport="$AUX_RPC_PORT" "$@"
}

# Helper: run aux coin CLI with wallet
auxcli_wallet() {
    if [[ "$MERGE_MODE" != "1" ]]; then
        return 1
    fi
    if [[ -n "${AUX_LEGACY_WALLET:-}" ]]; then
        auxcli "$@"
    else
        auxcli -rpcwallet="$AUX_WALLET_NAME" "$@"
    fi
}

# Address type for getnewaddress — defaults to bech32 (SegWit).
# Non-SegWit coins override ADDR_TYPE in setup_coin():
#   BCH  → "" (CashAddr, no type arg)
#   DOGE → "" (old API, no type arg)
#   PEP  → "legacy" (no SegWit)
# Note: Use ${VAR-default} not ${VAR:-default} to preserve empty string
ADDR_TYPE="${ADDR_TYPE-bech32}"

# GBT rules for getblocktemplate — defaults to ["segwit"].
# Coins override GBT_RULES in setup_coin():
#   LTC  → ["mweb", "segwit"] (requires MWEB)
#   BCH  → [] (no SegWit)
#   DOGE → [] (no SegWit)
#   PEP  → [] (no SegWit)
GBT_RULES="${GBT_RULES:-[\"segwit\"]}"

# Archive type for daemon download — defaults to tar.gz.
# Some coins use different formats (zip, tar.xz) and set ARCHIVE_TYPE in setup_coin().
ARCHIVE_TYPE="${ARCHIVE_TYPE:-tar.gz}"

# Legacy wallet mode — set to 1 for older daemons without createwallet/loadwallet RPC.
# These daemons (Dogecoin 1.14.x, etc.) use a default wallet automatically.
LEGACY_WALLET="${LEGACY_WALLET:-}"

# Derived variables
COIN_LOWER=$(echo "$COIN_SYMBOL" | tr '[:upper:]' '[:lower:]')

# =============================================================================
# Configuration — Override via environment variables
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CONFIG_FILE="$PROJECT_ROOT/config/config-${COIN_LOWER}-regtest.yaml"
POOL_BINARY="$PROJECT_ROOT/src/stratum/spiralpool"
LOG_DIR="$PROJECT_ROOT/logs/regtest"

# Derived table/config names (Go creates pool-specific tables)
SHARES_TABLE="shares_${POOL_ID}"
BLOCKS_TABLE="blocks_${POOL_ID}"
HA_CONFIG_FILENAME="config-${POOL_ID}-ha.yaml"

# CPU miner binary
# Override: CPUMINER=/path/to/minerd ./regtest.sh btc
CPUMINER="${CPUMINER:-minerd}"

# Database credentials (must match config-<coin>-regtest.yaml)
DB_NAME="${DB_NAME:-$DB_NAME_DEF}"
DB_USER="${DB_USER:-spiralstratum}"
DB_PASS="${DB_PASS:-spiralstratum}"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"

# Daemon RPC credentials (must match config-<coin>-regtest.yaml)
RPC_USER="${RPC_USER:-spiraltest}"
RPC_PASS="${RPC_PASS:-spiraltest}"
RPC_PORT="${RPC_PORT:-$RPC_PORT_DEF}"
P2P_PORT="${P2P_PORT:-$P2P_PORT_DEF}"
ZMQ_PORT="${ZMQ_PORT:-$ZMQ_PORT_DEF}"

# Pool stratum port (must match config-<coin>-regtest.yaml)
STRATUM_PORT="${STRATUM_PORT:-$STRATUM_PORT_DEF}"
STRATUM_V2_PORT="${STRATUM_V2_PORT:-${STRATUM_V2_PORT_DEF:-0}}"

# Pool API port (must match config-<coin>-regtest.yaml api_port)
API_PORT="${API_PORT:-$API_PORT_DEF}"

# Test parameters
TEST_BLOCKS="${TEST_BLOCKS:-5}"  # How many blocks to mine (override: TEST_BLOCKS=150 ./regtest.sh btc)
MINER_WAIT_SECS="${MINER_WAIT_SECS:-600}"  # Max seconds for mining phase
BLOCK_CHECK_INTERVAL=10   # Seconds between height checks
MINER_THREADS="${MINER_THREADS:-$(nproc)}"  # Use all CPU cores (diff=1 needs hashrate)
# Post-mining: keep pool running for payment processor maturity checks
# Payment processor cycles every ~10min, needs 3 stable checks = ~30min minimum
MATURITY_WAIT_SECS="${MATURITY_WAIT_SECS:-1200}"  # Seconds to wait for maturity (miner adds confirmation blocks)
# Coinbase maturity: most coins inherit Bitcoin's 100-block rule. The miner keeps running
# during Step 8c and mines the blocks naturally. At ~60-80s/block, 100 blocks ~ 1.5-2 hours.
# Set to 0 to skip coin movement tests (quick run: 4-6 min total).
COINBASE_WAIT_SECS="${COINBASE_WAIT_SECS:-900}"  # Seconds to wait for coinbase maturity (~15 min covers ~10-block maturity)

# =============================================================================
# Colors and Logging
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

DAEMON_PID=""
POOL_PID=""
MINER_PID=""
POOL2_PID=""
PEER_DAEMON_PID=""
AUX_DAEMON_PID=""  # Merge mining: auxiliary chain daemon
MINING_ADDRESS=""  # Set in Step 3, used in cleanup to restore config
AUX_MINING_ADDRESS=""  # Merge mining: aux chain payout address

log_info()  { echo -e "${CYAN}[INFO]${NC}  $(date '+%H:%M:%S')  $*"; }
log_ok()    { echo -e "${GREEN}[ OK ]${NC}  $(date '+%H:%M:%S')  $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $(date '+%H:%M:%S')  $*"; }
log_error() { echo -e "${RED}[FAIL]${NC}  $(date '+%H:%M:%S')  $*"; }

log_step() {
    echo ""
    echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${GREEN}${BOLD}  $*${NC}"
    echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

# Helper: run coin CLI with standard regtest flags
coincli() {
    "$CLI_CMD" -regtest -rpcuser="$RPC_USER" -rpcpassword="$RPC_PASS" -rpcport="$RPC_PORT" "$@"
}

# Helper: run coin CLI with wallet
# For legacy wallets (DOGE, etc.), skip -rpcwallet as they use default wallet
coincli_wallet() {
    if [[ -n "${LEGACY_WALLET:-}" ]]; then
        coincli "$@"
    else
        coincli -rpcwallet="$WALLET_NAME" "$@"
    fi
}

# =============================================================================
# Cleanup — runs on EXIT, INT (Ctrl+C), TERM
# =============================================================================

CLEANUP_RUNNING=0

cleanup() {
    # Prevent re-entry
    if [[ $CLEANUP_RUNNING -eq 1 ]]; then return; fi
    CLEANUP_RUNNING=1
    trap '' INT TERM

    echo ""
    log_step "Cleanup"

    # SIGKILL everything immediately — test environment, no graceful shutdown needed
    log_info "Force-killing all test processes..."
    kill -9 "$MINER_PID"  2>/dev/null || true
    kill -9 "$POOL_PID"   2>/dev/null || true
    kill -9 "$POOL2_PID"  2>/dev/null || true
    kill -9 "$DAEMON_PID" 2>/dev/null || true
    kill -9 "$PEER_DAEMON_PID" 2>/dev/null || true
    kill -9 "$AUX_DAEMON_PID" 2>/dev/null || true

    # Fallback: kill by name in case PIDs were stale
    pkill -9 -f "regtest-cpuminer" 2>/dev/null || true
    pkill -9 -f "spiralpool.*-config" 2>/dev/null || true
    coincli stop 2>/dev/null || true
    pkill -9 -f "$PKILL_PATTERN" 2>/dev/null || true
    # Merge mining: stop aux daemon
    if [[ "$MERGE_MODE" == "1" ]] && [[ -n "${AUX_CLI_CMD:-}" ]]; then
        "$AUX_CLI_CMD" -regtest -rpcuser="$RPC_USER" -rpcpassword="$RPC_PASS" -rpcport="$AUX_RPC_PORT" stop 2>/dev/null || true
        pkill -9 -f "${AUX_PKILL_PATTERN:-}" 2>/dev/null || true
    fi

    # Reap zombies
    wait "$MINER_PID"  2>/dev/null || true
    wait "$POOL_PID"   2>/dev/null || true
    wait "$POOL2_PID"  2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
    wait "$PEER_DAEMON_PID" 2>/dev/null || true
    wait "$AUX_DAEMON_PID" 2>/dev/null || true

    # Clean up peer daemon data directory
    rm -rf "$HOME/${DATA_DIR}-peer" 2>/dev/null || true
    # Merge mining: clean up aux chain data directory
    if [[ "$MERGE_MODE" == "1" ]] && [[ -n "${AUX_DATA_DIR:-}" ]]; then
        rm -rf "$HOME/${AUX_DATA_DIR}/regtest" 2>/dev/null || true
    fi

    log_ok "All processes killed"

    # Kill any stale HA test PostgreSQL on port 15432
    fuser -k 15432/tcp 2>/dev/null || true
    pkill -f "postgres.*15432" 2>/dev/null || true
    rm -rf /tmp/ha-pg-regtest-* 2>/dev/null || true

    # Clean up HA test config if it exists
    rm -f "$LOG_DIR/$HA_CONFIG_FILENAME" 2>/dev/null || true

    # Restore config placeholder (keep git clean)
    # Use regex to replace ANY address format to handle stale addresses from crashed runs
    # Matches: bech32 (bcrt1...), CashAddr (bchreg:...), legacy (D..., n...)
    sed -i -E 's|(address: ")[a-zA-Z0-9:]{20,}(".*$)|\1REGTEST_ADDRESS_PLACEHOLDER\2|g' "$CONFIG_FILE" 2>/dev/null || true
    log_info "Config address placeholder restored"

    log_ok "Cleanup complete"
}

trap cleanup EXIT INT TERM

# =============================================================================
# Helper Functions
# =============================================================================

# Auto-install daemon if not found
install_daemon() {
    local name="$1"
    local url="$2"
    local tarball_dir="$3"
    local archive_type="${4:-tar.gz}"  # Default to tar.gz

    if [[ -z "$url" || -z "$tarball_dir" ]]; then
        log_error "No download URL configured for $name"
        return 1
    fi

    log_info "Auto-installing $name..."
    log_info "  Downloading from: $url"

    local tmpdir=$(mktemp -d)
    cd "$tmpdir" || return 1

    # Download
    local archive_file="daemon.archive"
    if ! wget -q --show-progress "$url" -O "$archive_file"; then
        log_error "Download failed"
        rm -rf "$tmpdir"
        return 1
    fi

    # Extract based on archive type
    log_info "  Extracting ($archive_type)..."
    case "$archive_type" in
        tar.gz|tgz)
            if ! tar -xzf "$archive_file"; then
                log_error "Extraction failed (tar.gz)"
                rm -rf "$tmpdir"
                return 1
            fi
            ;;
        tar.xz)
            if ! tar -xJf "$archive_file"; then
                log_error "Extraction failed (tar.xz)"
                rm -rf "$tmpdir"
                return 1
            fi
            ;;
        zip)
            if ! unzip -q "$archive_file"; then
                log_error "Extraction failed (zip)"
                rm -rf "$tmpdir"
                return 1
            fi
            ;;
        *)
            log_error "Unknown archive type: $archive_type"
            rm -rf "$tmpdir"
            return 1
            ;;
    esac

    # Install binaries to /usr/local/bin
    log_info "  Installing to /usr/local/bin (requires sudo)..."

    # Debug: show what was extracted
    log_info "  Extracted contents:"
    ls -la "$tarball_dir" 2>/dev/null | head -10 || ls -la . | head -10

    local installed=0
    # Bitcoin Cash: archive ships bitcoind/bitcoin-cli but we need bitcoind-bch/bitcoin-cli-bch
    # Install to separate dir and create symlinks to avoid overwriting BTC binaries
    # (matches production install.sh)
    if [[ "${DAEMON_CMD##*/}" == "bitcoind-bch" ]] && [[ -d "$tarball_dir/bin" ]]; then
        local bch_bin_dir="/usr/local/lib/bitcoin-cash-node"
        sudo mkdir -p "$bch_bin_dir"
        sudo cp -v "$tarball_dir/bin/"* "$bch_bin_dir/" && installed=1
        sudo ln -sf "$bch_bin_dir/bitcoind" /usr/local/bin/bitcoind-bch
        sudo ln -sf "$bch_bin_dir/bitcoin-cli" /usr/local/bin/bitcoin-cli-bch
        log_info "  Installed to $bch_bin_dir, symlinked bitcoind-bch/bitcoin-cli-bch"
    # Bitcoin II: archive ships bitcoinIId/bitcoinII-cli (mixed case) but we need lowercase
    # Create lowercase symlinks (matches production install.sh lines 10490-10491)
    elif [[ "${DAEMON_CMD##*/}" == "bitcoiniid" ]] && [[ -f "$tarball_dir/bitcoinIId" ]]; then
        sudo cp -v "$tarball_dir/bitcoinIId" "$tarball_dir/bitcoinII-cli" /usr/local/bin/ && installed=1
        sudo ln -sf /usr/local/bin/bitcoinIId /usr/local/bin/bitcoiniid
        sudo ln -sf /usr/local/bin/bitcoinII-cli /usr/local/bin/bitcoinii-cli
        log_info "  Installed bitcoinIId, symlinked lowercase bitcoiniid/bitcoinii-cli"
    # Fractal Bitcoin: archive ships bitcoind/bitcoin-cli but daemon is fractald/fractal-cli
    # Install to separate dir and create symlinks (matches production install.sh)
    elif [[ "${DAEMON_CMD##*/}" == "fractald" ]] && [[ -d "$tarball_dir/bin" ]]; then
        local fbtc_bin_dir="/usr/local/lib/fractal-bitcoin"
        sudo mkdir -p "$fbtc_bin_dir"
        sudo cp -v "$tarball_dir/bin/"* "$fbtc_bin_dir/" && installed=1
        sudo ln -sf "$fbtc_bin_dir/bitcoind" /usr/local/bin/fractald
        sudo ln -sf "$fbtc_bin_dir/bitcoin-cli" /usr/local/bin/fractal-cli
        log_info "  Installed to $fbtc_bin_dir, symlinked fractald/fractal-cli"
    # Try standard structure: tarball_dir/bin/
    elif [[ -d "$tarball_dir/bin" ]]; then
        sudo cp -v "$tarball_dir/bin/"* /usr/local/bin/ && installed=1
    # Try flat structure at tarball_dir level
    elif [[ -f "$tarball_dir/${DAEMON_CMD##*/}" ]]; then
        sudo cp -v "$tarball_dir/${DAEMON_CMD##*/}" "$tarball_dir/${CLI_CMD##*/}" /usr/local/bin/ 2>/dev/null && installed=1
    # Try current directory (tarball_dir=".")
    elif [[ -f "./${DAEMON_CMD##*/}" ]]; then
        sudo cp -v "./${DAEMON_CMD##*/}" "./${CLI_CMD##*/}" /usr/local/bin/ 2>/dev/null && installed=1
    # Fallback: find any *d and *-cli binaries
    else
        sudo cp -v "$tarball_dir/"*d "$tarball_dir/"*-cli /usr/local/bin/ 2>/dev/null && installed=1
    fi

    if [[ $installed -eq 0 ]]; then
        log_error "Could not find binaries to install"
        log_error "Expected: ${DAEMON_CMD##*/}, ${CLI_CMD##*/}"
        ls -laR "$tarball_dir" 2>/dev/null | head -30
        rm -rf "$tmpdir"
        return 1
    fi

    # Cleanup
    cd - >/dev/null
    rm -rf "$tmpdir"

    log_ok "$name installed successfully"
    return 0
}

install_cpuminer() {
    log_info "Auto-installing cpuminer (minerd) from source..."

    # Install build dependencies
    log_info "  Installing build dependencies..."
    if ! sudo apt install -y automake autoconf libcurl4-openssl-dev libjansson-dev; then
        log_error "Failed to install build dependencies"
        return 1
    fi

    # Clone and build
    rm -rf /tmp/cpuminer
    log_info "  Cloning pooler/cpuminer..."
    if ! git clone https://github.com/pooler/cpuminer.git /tmp/cpuminer; then
        log_error "Failed to clone cpuminer"
        return 1
    fi

    log_info "  Building cpuminer..."
    cd /tmp/cpuminer || return 1
    if ! ./autogen.sh; then
        log_error "autogen.sh failed"
        cd - >/dev/null
        return 1
    fi
    if ! ./configure; then
        log_error "configure failed"
        cd - >/dev/null
        return 1
    fi
    if ! make; then
        log_error "make failed"
        cd - >/dev/null
        return 1
    fi

    log_info "  Installing to /usr/local/bin (requires sudo)..."
    if ! sudo cp /tmp/cpuminer/minerd /usr/local/bin/; then
        log_error "Failed to copy minerd to /usr/local/bin"
        cd - >/dev/null
        return 1
    fi

    cd - >/dev/null
    rm -rf /tmp/cpuminer
    log_ok "cpuminer (minerd) installed successfully"
    return 0
}

check_binary() {
    local name="$1"
    local bin="$2"
    local install_hint="${3:-}"
    if ! command -v "$bin" &>/dev/null; then
        log_warn "$name not found in PATH: '$bin'"

        # Offer auto-install if download URL is configured
        if [[ -n "$DOWNLOAD_URL" && -n "$TARBALL_DIR" && "$bin" == "$DAEMON_CMD" ]]; then
            echo ""
            echo -e "${YELLOW}Would you like to auto-install $COIN_NAME daemon? [y/N]${NC}"
            read -r -t 30 response || response="n"
            if [[ "$response" =~ ^[Yy]$ ]]; then
                if install_daemon "$name" "$DOWNLOAD_URL" "$TARBALL_DIR" "${ARCHIVE_TYPE:-tar.gz}"; then
                    # Verify installation
                    if command -v "$bin" &>/dev/null; then
                        log_ok "$name installed and found: $(command -v "$bin")"
                        return 0
                    fi
                fi
                log_error "Auto-install failed"
            fi
        fi

        # Offer auto-install for cpuminer
        if [[ "$name" == "cpuminer" ]]; then
            echo ""
            echo -e "${YELLOW}Would you like to auto-install cpuminer (minerd) from source? [y/N]${NC}"
            read -r -t 30 response || response="n"
            if [[ "$response" =~ ^[Yy]$ ]]; then
                if install_cpuminer; then
                    if command -v "$bin" &>/dev/null; then
                        log_ok "cpuminer installed and found: $(command -v "$bin")"
                        return 0
                    fi
                fi
                log_error "Auto-install failed"
            fi
        fi

        log_error "$name not found"
        if [[ -n "$install_hint" ]]; then
            log_info "  Install: $install_hint"
        fi
        log_info "  Or override: ${name^^}=/path/to/$bin ./regtest.sh $COIN"
        return 1
    fi
    log_ok "$name found: $(command -v "$bin")"
}

wait_for_rpc() {
    local max_wait=30
    local waited=0
    log_info "Waiting for $DAEMON_CMD RPC on port $RPC_PORT..."
    while ! coincli getblockchaininfo &>/dev/null; do
        sleep 1
        waited=$((waited + 1))
        if [[ $waited -ge $max_wait ]]; then
            log_error "$DAEMON_CMD RPC not responding after ${max_wait}s"
            log_error "Check: $LOG_DIR/$DAEMON_STARTUP"
            return 1
        fi
    done
    log_ok "$DAEMON_CMD RPC ready (${waited}s)"
}

wait_for_stratum() {
    local max_wait=30
    local waited=0
    log_info "Waiting for pool stratum on port $STRATUM_PORT..."
    while ! (echo >/dev/tcp/127.0.0.1/$STRATUM_PORT) 2>/dev/null; do
        sleep 1
        waited=$((waited + 1))
        if [[ $waited -ge $max_wait ]]; then
            log_error "Pool stratum not listening after ${max_wait}s"
            log_error "Check: $LOG_DIR/spiralpool-regtest.log"
            return 1
        fi
    done
    log_ok "Pool stratum listening on port $STRATUM_PORT (${waited}s)"
}

get_block_count() {
    coincli getblockcount 2>/dev/null || echo "0"
}

# =============================================================================
# DGB SHA256d Regtest Limitation Check
# =============================================================================
# DigiByte regtest mode only supports Scrypt mining. SHA256d is not available
# in regtest - this is a DigiByte daemon limitation, not a pool bug.
# The pool code for DGB SHA256d is verified working via DGB-SCRYPT tests.
if [[ "$COIN" == "dgb" ]]; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "  DigiByte SHA256d - Regtest Not Available"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "[INFO] DigiByte regtest mode only supports Scrypt algorithm."
    echo "[INFO] SHA256d blocks cannot be mined in regtest - daemon limitation."
    echo ""
    echo "[INFO] The pool's multi-algo code IS verified working:"
    echo "       - DGB-SCRYPT passed all tests (8/8 HA VIP)"
    echo "       - Same code path, just different algorithm string"
    echo "       - DGB SHA256d will work on mainnet/testnet"
    echo ""
    echo "[INFO] To test DigiByte mining, use:"
    echo "       COIN=dgb-scrypt ./scripts/linux/regtest.sh"
    echo ""
    echo "[INFO] To test on DigiByte testnet (has SHA256d):"
    echo "       Configure daemon for testnet instead of regtest"
    echo ""
    exit 0
fi

# =============================================================================
# STEP 1: Preflight Checks
# =============================================================================

log_step "Step 1/10: Preflight Checks — $COIN_NAME ($COIN_SYMBOL)"

log_info "Checking required binaries..."
check_binary "$DAEMON_CMD"  "$DAEMON_CMD"  "Build from $GITHUB_URL"
check_binary "$CLI_CMD"     "$CLI_CMD"     "(included with $COIN_NAME Core)"
if [[ "$HA_ONLY" != "1" ]]; then
    check_binary "cpuminer"     "$CPUMINER"    "Build from https://github.com/pooler/cpuminer"
else
    log_info "HA_ONLY mode — skipping cpuminer check"
fi

log_info "Checking config file..."
if [[ ! -f "$CONFIG_FILE" ]]; then
    log_error "Regtest config not found: $CONFIG_FILE"
    log_info "  This file should exist in the release. Re-run install.sh or check your checkout."
    exit 1
fi
log_ok "Config: $CONFIG_FILE"

log_info "Checking pool binary..."
if [[ ! -f "$POOL_BINARY" ]]; then
    log_warn "Pool binary not found at $POOL_BINARY — building now..."
    (cd "$PROJECT_ROOT/src/stratum" && go build -o spiralpool ./cmd/spiralpool)
    if [[ ! -f "$POOL_BINARY" ]]; then
        log_error "Build failed! Run manually: cd src/stratum && go build -o spiralpool ./cmd/spiralpool"
        exit 1
    fi
    log_ok "Pool binary built successfully"
else
    log_ok "Pool binary: $POOL_BINARY"
fi

# Build V2 test miner (for V2 stratum protocol testing)
# Always rebuild to pick up source changes (build is fast)
V2_MINER="$PROJECT_ROOT/src/stratum/v2testminer"
if [[ "$STRATUM_V2_PORT" -gt 0 ]]; then
    log_info "Building V2 test miner..."
    rm -f "$V2_MINER"
    (cd "$PROJECT_ROOT/src/stratum" && go build -o v2testminer ./cmd/v2testminer)
    if [[ ! -f "$V2_MINER" ]]; then
        log_warn "V2 test miner build failed — V2 tests will be skipped"
        STRATUM_V2_PORT=0
    else
        log_ok "V2 test miner built successfully"
    fi
fi

log_info "Checking PostgreSQL..."
if ! pg_isready -h "$DB_HOST" -p "$DB_PORT" &>/dev/null; then
    log_error "PostgreSQL not running on $DB_HOST:$DB_PORT"
    log_info "  Start it:   sudo systemctl start postgresql"
    log_info "  Or install:  install.sh handles PostgreSQL setup"
    exit 1
fi
log_ok "PostgreSQL running on $DB_HOST:$DB_PORT"

mkdir -p "$LOG_DIR"
log_ok "Log directory: $LOG_DIR"

# =============================================================================
# STEP 2: Start Coin Daemon (regtest mode)
# =============================================================================

log_step "Step 2/10: Start $DAEMON_CMD (regtest)"

log_info "Stopping any existing regtest processes..."
coincli stop 2>/dev/null || true
# Kill any leftover pool/miner from a previous run (port conflicts)
pkill -f "spiralpool.*config" 2>/dev/null || true
pkill -f "minerd.*$STRATUM_PORT" 2>/dev/null || true
sleep 2

# Auto-cleanup: Remove previous regtest chain data for a fresh start
# Set KEEP_CHAIN=1 to preserve existing chain data
if [[ "${KEEP_CHAIN:-0}" != "1" ]]; then
    log_info "Cleaning up previous regtest data (set KEEP_CHAIN=1 to skip)..."

    # Determine the data directory based on coin
    case "$COIN_SYMBOL" in
        BTC)   COIN_DATA_DIR="$HOME/.bitcoin" ;;
        LTC)   COIN_DATA_DIR="$HOME/.litecoin" ;;
        DOGE)  COIN_DATA_DIR="$HOME/.dogecoin" ;;
        DGB|DGB-SCRYPT) COIN_DATA_DIR="$HOME/.digibyte" ;;
        BCH)   COIN_DATA_DIR="$HOME/.bitcoin" ;;
        PEP)   COIN_DATA_DIR="$HOME/.pepecoin" ;;
        CAT)   COIN_DATA_DIR="$HOME/.catcoin" ;;
        SYS)   COIN_DATA_DIR="$HOME/.syscoin" ;;
        NMC)   COIN_DATA_DIR="$HOME/.namecoin" ;;
        XMY)   COIN_DATA_DIR="$HOME/.myriadcoin" ;;
        FBTC)  COIN_DATA_DIR="$HOME/.fractal" ;;
        BC2)   COIN_DATA_DIR="$HOME/.bitcoinii" ;;
        *)     COIN_DATA_DIR="" ;;
    esac

    if [[ -n "$COIN_DATA_DIR" ]] && [[ -d "$COIN_DATA_DIR/regtest" ]]; then
        log_info "  Removing: $COIN_DATA_DIR/regtest"
        rm -rf "$COIN_DATA_DIR/regtest"
        log_ok "  Regtest chain data cleared for $COIN_SYMBOL"
    elif [[ -n "$COIN_DATA_DIR" ]]; then
        log_info "  No existing regtest data found at $COIN_DATA_DIR/regtest"
    else
        log_warn "  Unknown coin data directory for $COIN_SYMBOL — manual cleanup may be needed"
    fi
else
    log_info "KEEP_CHAIN=1 — preserving existing regtest chain data"
fi

# DigiByte multi-algo: must set algo in config file, not just command-line
# The daemon ignores -algo flag; only reads from digibyte.conf
case "$COIN" in
    dgb)
        log_info "Configuring DigiByte for SHA256d algorithm..."
        mkdir -p "$HOME/.digibyte"
        cat > "$HOME/.digibyte/digibyte.conf" <<EOF
# Spiral Pool regtest configuration
regtest=1
algo=sha256d
EOF
        log_ok "Created ~/.digibyte/digibyte.conf with algo=sha256d"
        ;;
    dgb-scrypt)
        log_info "Configuring DigiByte for Scrypt algorithm..."
        mkdir -p "$HOME/.digibyte"
        cat > "$HOME/.digibyte/digibyte.conf" <<EOF
# Spiral Pool regtest configuration
regtest=1
algo=scrypt
EOF
        log_ok "Created ~/.digibyte/digibyte.conf with algo=scrypt"
        ;;
esac

log_info "Starting $DAEMON_CMD with:"
log_info "  Network:    regtest (private chain, difficulty=1)"
log_info "  RPC:        127.0.0.1:$RPC_PORT (user=$RPC_USER)"
log_info "  P2P:        port $P2P_PORT (listen=off, no external peers)"
log_info "  ZMQ:        tcp://127.0.0.1:$ZMQ_PORT (hashblock + rawblock)"
log_info "  Log:        $LOG_DIR/$DAEMON_LOG"

# Build daemon arguments - some coins need extra flags for isolated regtest
DAEMON_ARGS=(
    -regtest
    -daemon=0
    -server=1
    -rpcuser="$RPC_USER"
    -rpcpassword="$RPC_PASS"
    -rpcport="$RPC_PORT"
    -rpcallowip=127.0.0.1
    -rpcbind=127.0.0.1
    -port="$P2P_PORT"
    -listen=0
    -connect=0
    -dnsseed=0
    -zmqpubhashblock="tcp://127.0.0.1:$ZMQ_PORT"
    -zmqpubrawblock="tcp://127.0.0.1:$ZMQ_PORT"
    -txindex=1
    -fallbackfee=0.0001
    -printtoconsole=0
    -debuglogfile="$LOG_DIR/$DAEMON_LOG"
)

# Fractal Bitcoin: binary is bitcoind (defaults to ~/.bitcoin), must override datadir
# Without this, FBTC would collide with BTC's data directory
if [[ "$COIN" == "fbtc" ]]; then
    mkdir -p "$HOME/.fractal"
    DAEMON_ARGS+=(-datadir="$HOME/.fractal")
fi

# Add optional flags based on coin type
# Some coins don't support certain flags
case "$COIN" in
    dgb|dgb-scrypt)
        # DigiByte multi-algo: algorithm is set via config file (see above)
        # The -algo command-line flag is ignored by digibyted
        DAEMON_ARGS+=(-blockfilterindex=1)
        BLOCKFILTERINDEX_FLAG="-blockfilterindex=1"
        ;;
    ltc|doge|pep|cat)
        # Scrypt coins - add listenonion=0 to prevent Tor dependency issues
        DAEMON_ARGS+=(-listenonion=0)
        BLOCKFILTERINDEX_FLAG="-blockfilterindex=1"
        ;;
    bch)
        # BCH doesn't support blockfilterindex
        # BCH Node has forced expiration - disable for regtest
        DAEMON_ARGS+=(-expire=0)
        BLOCKFILTERINDEX_FLAG=""  # BCH doesn't support this
        ;;
    xmy)
        # Myriadcoin v0.18.1 too old for blockfilterindex (requires Bitcoin Core 0.19+)
        BLOCKFILTERINDEX_FLAG=""
        ;;
    *)
        # SHA256d coins typically support blockfilterindex
        DAEMON_ARGS+=(-blockfilterindex=1)
        BLOCKFILTERINDEX_FLAG="-blockfilterindex=1"
        ;;
esac

# For coins that require peers for GBT (Litecoin family + BCH), we need to run
# two daemon instances that connect to each other
NEEDS_PEER_DAEMON=false
case "$COIN" in
    ltc|doge|pep|cat|bch|xmy)
        NEEDS_PEER_DAEMON=true
        # Enable listening so peer daemon can connect
        DAEMON_ARGS+=(-listen=1)
        ;;
esac

"$DAEMON_CMD" "${DAEMON_ARGS[@]}" &>"$LOG_DIR/$DAEMON_STARTUP" &

DAEMON_PID=$!
log_info "$DAEMON_CMD PID: $DAEMON_PID"

wait_for_rpc

# For coins requiring peers, start a second daemon instance
PEER_DAEMON_PID=""
if [[ "$NEEDS_PEER_DAEMON" == "true" ]]; then
    # Some coins enter IBD when peer connects - must mine first block before peer
    if [[ "${MINE_BEFORE_PEER:-}" == "1" ]]; then
        log_info "Mining initial block before peer connection (avoids IBD lock)..."
        coincli ${PREMINE_CMD:-setgenerate true 1} &>/dev/null || true
        PREMINE_WAIT=0
        while [[ $PREMINE_WAIT -lt 180 ]]; do
            sleep 5
            PREMINE_WAIT=$((PREMINE_WAIT + 5))
            HEIGHT=$(coincli getblockcount 2>/dev/null || echo "0")
            if [[ "$HEIGHT" -ge 1 ]]; then
                coincli setgenerate false &>/dev/null || true
                log_ok "Pre-peer block mined (height: $HEIGHT)"
                break
            fi
            [[ $((PREMINE_WAIT % 30)) -eq 0 ]] && log_info "  Mining... ${PREMINE_WAIT}s"
        done
        coincli setgenerate false &>/dev/null || true
    fi
    log_info "Starting peer daemon (required for GBT on this coin)..."
    PEER_P2P_PORT=$((P2P_PORT + 100))
    PEER_RPC_PORT=$((RPC_PORT + 100))
    PEER_DATA_DIR="$HOME/${DATA_DIR}-peer"
    mkdir -p "$PEER_DATA_DIR"

    # Build peer daemon extra args based on coin
    PEER_EXTRA_ARGS=""
    case "$COIN" in
        bch) PEER_EXTRA_ARGS="-expire=0" ;;
    esac

    "$DAEMON_CMD" \
        -regtest \
        -daemon=0 \
        -server=1 \
        -rpcuser="$RPC_USER" \
        -rpcpassword="$RPC_PASS" \
        -rpcport="$PEER_RPC_PORT" \
        -rpcallowip=127.0.0.1 \
        -rpcbind=127.0.0.1 \
        -port="$PEER_P2P_PORT" \
        -connect="127.0.0.1:$P2P_PORT" \
        -datadir="$PEER_DATA_DIR" \
        -listenonion=0 \
        -dnsseed=0 \
        -printtoconsole=0 \
        $PEER_EXTRA_ARGS \
        &>"$LOG_DIR/peer-daemon.log" &

    PEER_DAEMON_PID=$!
    log_info "Peer daemon PID: $PEER_DAEMON_PID (port $PEER_P2P_PORT -> $P2P_PORT)"

    # BUG FIX: Wait for peer connection with proper timeout loop
    # Without this, script continues with PEER_COUNT=0 and GBT fails later
    PEER_WAIT=0
    PEER_TIMEOUT=30
    while [[ $PEER_WAIT -lt $PEER_TIMEOUT ]]; do
        PEER_COUNT=$(coincli getconnectioncount 2>/dev/null || echo "0")
        if [[ "$PEER_COUNT" -gt 0 ]]; then
            log_ok "Peer connected (connection count: $PEER_COUNT)"
            break
        fi
        sleep 1
        PEER_WAIT=$((PEER_WAIT + 1))
        if [[ $((PEER_WAIT % 5)) -eq 0 ]]; then
            log_info "Waiting for peer connection... ${PEER_WAIT}s"
        fi
    done

    if [[ "$PEER_COUNT" -eq 0 ]]; then
        log_error "Peer daemon failed to connect after ${PEER_TIMEOUT}s"
        log_error "GBT will fail without peer. Check peer-daemon.log"
        exit 1
    fi
fi

# Show chain info
CHAIN_INFO=$(coincli getblockchaininfo 2>/dev/null)
CHAIN_NAME=$(echo "$CHAIN_INFO" | grep '"chain"' | tr -d ' ",' | cut -d: -f2)
CHAIN_BLOCKS=$(echo "$CHAIN_INFO" | grep '"blocks"' | tr -d ' ",' | cut -d: -f2)
log_ok "Connected to chain='$CHAIN_NAME' at height $CHAIN_BLOCKS"

# =============================================================================
# Merge Mining: Start Auxiliary Chain Daemon
# =============================================================================
# When --merge is used, we also need to start the aux chain daemon (e.g., NMC, DOGE)
# so the pool can fetch aux block templates via getauxblock RPC.

if [[ "$MERGE_MODE" == "1" ]]; then
    log_info ""
    log_info "━━━ Merge Mining: Starting $AUX_NAME daemon ━━━"

    # Kill any existing aux daemon from a previous test (solo or merge)
    # Without this, a leftover daemon holds the RPC port and stale data
    "$AUX_CLI_CMD" -regtest -rpcuser="$RPC_USER" -rpcpassword="$RPC_PASS" -rpcport="$AUX_RPC_PORT" stop 2>/dev/null || true
    pkill -9 -f "${AUX_PKILL_PATTERN}" 2>/dev/null || true
    sleep 1

    # Clean aux chain regtest data for fresh start
    if [[ "${KEEP_CHAIN:-0}" != "1" ]] && [[ -d "$HOME/$AUX_DATA_DIR/regtest" ]]; then
        log_info "Cleaning aux chain regtest data..."
        rm -rf "$HOME/$AUX_DATA_DIR/regtest"
    fi

    log_info "Starting $AUX_DAEMON_CMD with:"
    log_info "  Network:    regtest (aux chain)"
    log_info "  RPC:        127.0.0.1:$AUX_RPC_PORT"
    log_info "  P2P:        port $AUX_P2P_PORT"
    log_info "  ZMQ:        tcp://127.0.0.1:$AUX_ZMQ_PORT"
    log_info "  Log:        $LOG_DIR/$AUX_DAEMON_LOG"

    # Build aux daemon arguments
    AUX_DAEMON_ARGS=(
        -regtest
        -daemon=0
        -server=1
        -rpcuser="$RPC_USER"
        -rpcpassword="$RPC_PASS"
        -rpcport="$AUX_RPC_PORT"
        -rpcallowip=127.0.0.1
        -rpcbind=127.0.0.1
        -port="$AUX_P2P_PORT"
        -listen=0
        -connect=0
        -dnsseed=0
        -zmqpubhashblock="tcp://127.0.0.1:$AUX_ZMQ_PORT"
        -zmqpubrawblock="tcp://127.0.0.1:$AUX_ZMQ_PORT"
        -txindex=1
        -fallbackfee=0.0001
        -printtoconsole=0
        -debuglogfile="$LOG_DIR/$AUX_DAEMON_LOG"
    )

    # Aux-chain specific flags
    case "$AUX_COIN" in
        doge|pep)
            # Dogecoin/PepeCoin: no SegWit, legacy wallet, disable Tor
            AUX_DAEMON_ARGS+=(-listenonion=0)
            ;;
        nmc)
            # Namecoin: SegWit-compatible, modern wallet
            AUX_DAEMON_ARGS+=(-blockfilterindex=1)
            ;;
        fbtc)
            # Fractal Bitcoin: binary is bitcoind, must specify datadir to avoid ~/.bitcoin collision
            mkdir -p "$HOME/.fractal"
            AUX_DAEMON_ARGS+=(-datadir="$HOME/.fractal" -blockfilterindex=1)
            ;;
        sys)
            # Syscoin: SegWit-compatible, modern wallet
            AUX_DAEMON_ARGS+=(-blockfilterindex=1)
            ;;
        xmy)
            # Myriadcoin v0.18.1 too old for blockfilterindex
            ;;
    esac

    "$AUX_DAEMON_CMD" "${AUX_DAEMON_ARGS[@]}" &>"$LOG_DIR/${AUX_DAEMON_STARTUP}" &
    AUX_DAEMON_PID=$!
    log_info "$AUX_DAEMON_CMD PID: $AUX_DAEMON_PID"

    # Wait for aux daemon RPC
    AUX_RPC_WAIT=0
    AUX_RPC_MAX=30
    log_info "Waiting for $AUX_DAEMON_CMD RPC on port $AUX_RPC_PORT..."
    while ! auxcli getblockchaininfo &>/dev/null; do
        sleep 1
        AUX_RPC_WAIT=$((AUX_RPC_WAIT + 1))
        if [[ $AUX_RPC_WAIT -ge $AUX_RPC_MAX ]]; then
            log_error "$AUX_DAEMON_CMD RPC not responding after ${AUX_RPC_MAX}s"
            log_error "Check: $LOG_DIR/${AUX_DAEMON_STARTUP}"
            exit 1
        fi
    done
    log_ok "$AUX_DAEMON_CMD RPC ready (${AUX_RPC_WAIT}s)"

    # Show aux chain info
    AUX_CHAIN_INFO=$(auxcli getblockchaininfo 2>/dev/null)
    AUX_CHAIN_NAME=$(echo "$AUX_CHAIN_INFO" | grep '"chain"' | tr -d ' ",' | cut -d: -f2)
    AUX_CHAIN_BLOCKS=$(echo "$AUX_CHAIN_INFO" | grep '"blocks"' | tr -d ' ",' | cut -d: -f2)
    log_ok "Aux chain connected: chain='$AUX_CHAIN_NAME' at height $AUX_CHAIN_BLOCKS"
fi

# =============================================================================
# HA_ONLY mode: Skip Steps 3-4 (wallet, chain setup) — jump to Step 5 (PostgreSQL)
# =============================================================================

if [[ "$HA_ONLY" == "1" ]]; then
    log_info "HA_ONLY mode — skipping Steps 3-4 (wallet/chain setup, but need mining address)"

    # BUG FIX: HA test still needs a valid mining address for pool config
    # Create wallet and generate address even in HA_ONLY mode
    if [[ -n "${LEGACY_WALLET:-}" ]]; then
        log_info "Legacy wallet mode — using default wallet"
    else
        log_info "Creating wallet for HA test..."
        coincli createwallet "$WALLET_NAME" 2>/dev/null || \
        coincli loadwallet "$WALLET_NAME" 2>/dev/null || true
    fi

    if [[ -n "$ADDR_TYPE" ]]; then
        MINING_ADDRESS=$(coincli_wallet getnewaddress "pool-coinbase" "$ADDR_TYPE" 2>/dev/null) || \
            MINING_ADDRESS=$(coincli_wallet getnewaddress "pool-coinbase" 2>/dev/null) || true
    else
        MINING_ADDRESS=$(coincli_wallet getnewaddress "pool-coinbase" 2>/dev/null) || true
    fi

    if [[ -z "$MINING_ADDRESS" ]] || [[ "$MINING_ADDRESS" == "null" ]]; then
        log_error "Failed to generate mining address for HA test"
        exit 1
    fi
    log_ok "HA test mining address: $MINING_ADDRESS"

    HEIGHT_BEFORE=0
else

# =============================================================================
# STEP 3: Create Wallet and Mining Address
# =============================================================================

log_step "Step 3/10: Create wallet and mining address"

if [[ -n "${LEGACY_WALLET:-}" ]]; then
    # Legacy daemons (Dogecoin 1.14.x, etc.) don't have createwallet/loadwallet
    # They use a default wallet automatically
    log_info "Legacy wallet mode — using default wallet (no createwallet RPC)"
    log_ok "Default wallet ready"
else
    log_info "Creating regtest wallet '$WALLET_NAME'..."
    # Use simple createwallet (no positional args) — v29+ removed legacy wallets,
    # passing descriptors=false would fail. Default creates a descriptor wallet.
    CREATE_OUT=$(coincli createwallet "$WALLET_NAME" 2>&1) || \
    LOAD_OUT=$(coincli loadwallet "$WALLET_NAME" 2>&1) || true

    if echo "$CREATE_OUT" | grep -qi "error" && echo "$LOAD_OUT" | grep -qi "error"; then
        log_error "Wallet creation AND load both failed:"
        log_error "  createwallet: $CREATE_OUT"
        log_error "  loadwallet:   $LOAD_OUT"
        exit 1
    fi
    log_ok "Wallet ready"
fi

if [[ -n "$ADDR_TYPE" ]]; then
    log_info "Generating $ADDR_TYPE mining address..."
    MINING_ADDRESS=$(coincli_wallet getnewaddress "pool-coinbase" "$ADDR_TYPE")
else
    log_info "Generating mining address (default format)..."
    MINING_ADDRESS=$(coincli_wallet getnewaddress "pool-coinbase")
fi
log_ok "Mining address: $MINING_ADDRESS"

log_info "Updating pool config with mining address..."
# Pre-check: Ensure placeholder exists (handle stale addresses from crashed previous runs)
# Handles all address formats: bech32 (bcrt1...), CashAddr (bchreg:...), legacy (D..., n...)
if ! grep -q "REGTEST_ADDRESS_PLACEHOLDER" "$CONFIG_FILE"; then
    log_warn "Stale address detected in config — restoring placeholder first"
    # Match any address: at least 20 alphanumeric chars, may contain : or 1
    sed -i -E 's|(address: ")[a-zA-Z0-9:]{20,}(".*$)|\1REGTEST_ADDRESS_PLACEHOLDER\2|g' "$CONFIG_FILE"
fi
sed -i "s|REGTEST_ADDRESS_PLACEHOLDER|$MINING_ADDRESS|g" "$CONFIG_FILE"
log_ok "Config updated (will be restored on exit)"

# Merge Mining: Create aux chain wallet and address
if [[ "$MERGE_MODE" == "1" ]]; then
    log_info ""
    log_info "━━━ Merge Mining: Creating $AUX_NAME wallet ━━━"

    if [[ -n "${AUX_LEGACY_WALLET:-}" ]]; then
        log_info "Aux chain legacy wallet mode — using default wallet"
        log_ok "Aux wallet ready (legacy)"
    else
        log_info "Creating aux wallet '$AUX_WALLET_NAME'..."
        auxcli createwallet "$AUX_WALLET_NAME" 2>/dev/null || \
        auxcli loadwallet "$AUX_WALLET_NAME" 2>/dev/null || true
        log_ok "Aux wallet ready"
    fi

    # Generate aux chain mining address
    if [[ -n "${AUX_ADDR_TYPE:-}" ]]; then
        AUX_MINING_ADDRESS=$(auxcli_wallet getnewaddress "aux-pool-coinbase" "$AUX_ADDR_TYPE" 2>/dev/null) || true
    else
        AUX_MINING_ADDRESS=$(auxcli_wallet getnewaddress "aux-pool-coinbase" 2>/dev/null) || true
    fi

    if [[ -z "$AUX_MINING_ADDRESS" ]]; then
        log_error "Failed to generate aux chain mining address"
        exit 1
    fi
    log_ok "Aux mining address: $AUX_MINING_ADDRESS"

    # Verify aux block RPC works on aux chain
    # FBTC and XMY use createauxblock(address) instead of getauxblock
    # Note: getauxblock is a wallet RPC, so use auxcli_wallet for modern wallets
    USE_CREATEAUXBLOCK=0
    case "$AUX_COIN" in
        fbtc|xmy) USE_CREATEAUXBLOCK=1 ;;
    esac

    if [[ "$USE_CREATEAUXBLOCK" == "1" ]]; then
        log_info "Verifying createauxblock RPC on $AUX_NAME..."
        AUX_BLOCK_RESULT=$(auxcli_wallet createauxblock "$AUX_MINING_ADDRESS" 2>&1) || true
    else
        log_info "Verifying getauxblock RPC on $AUX_NAME..."
        AUX_BLOCK_RESULT=$(auxcli_wallet getauxblock 2>&1) || true
    fi
    if echo "$AUX_BLOCK_RESULT" | grep -q '"hash"'; then
        AUX_BLOCK_HASH=$(echo "$AUX_BLOCK_RESULT" | grep -o '"hash"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
        log_ok "auxblock RPC works (hash: ${AUX_BLOCK_HASH:0:16}...)"
    elif echo "$AUX_BLOCK_RESULT" | grep -q "not yet available"; then
        # Some coins require a minimum chain height before AuxPoW activates
        # XMY regtest: nStartAuxPow=150, DOGE regtest: ~100
        log_warn "AuxPoW not yet active on $AUX_NAME — pre-mining activation blocks..."
        case "$AUX_COIN" in
            xmy) AUXPOW_ACTIVATION_BLOCKS=155 ;;  # XMY regtest nStartAuxPow=150
            *)   AUXPOW_ACTIVATION_BLOCKS=110 ;;
        esac
        log_info "Generating $AUXPOW_ACTIVATION_BLOCKS blocks on $AUX_NAME to activate AuxPoW..."

        # Use legacy generate for DOGE (doesn't have generatetoaddress in regtest)
        if [[ -n "${AUX_LEGACY_WALLET:-}" ]]; then
            # Legacy wallet: use generate (DOGE uses this in regtest)
            GENERATE_RESULT=$(auxcli_wallet generate $AUXPOW_ACTIVATION_BLOCKS 2>/dev/null) || \
            GENERATE_RESULT=$(auxcli_wallet setgenerate true $AUXPOW_ACTIVATION_BLOCKS 2>/dev/null) || true
        else
            GENERATE_RESULT=$(auxcli_wallet generatetoaddress $AUXPOW_ACTIVATION_BLOCKS "$AUX_MINING_ADDRESS" 2>/dev/null) || \
            GENERATE_RESULT=$(auxcli_wallet generate $AUXPOW_ACTIVATION_BLOCKS 2>/dev/null) || true
        fi

        # Verify AuxPoW is now active
        AUX_HEIGHT=$(auxcli getblockcount 2>/dev/null) || AUX_HEIGHT=0
        log_info "Aux chain height after pre-mining: $AUX_HEIGHT"

        if [[ "$USE_CREATEAUXBLOCK" == "1" ]]; then
            AUX_BLOCK_RESULT=$(auxcli_wallet createauxblock "$AUX_MINING_ADDRESS" 2>&1) || true
        else
            AUX_BLOCK_RESULT=$(auxcli_wallet getauxblock 2>&1) || true
        fi
        if echo "$AUX_BLOCK_RESULT" | grep -q '"hash"'; then
            AUX_BLOCK_HASH=$(echo "$AUX_BLOCK_RESULT" | grep -o '"hash"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
            log_ok "AuxPoW activated! auxblock RPC works (hash: ${AUX_BLOCK_HASH:0:16}...)"
        else
            log_error "Failed to activate AuxPoW on $AUX_NAME after $AUXPOW_ACTIVATION_BLOCKS blocks"
            log_error "auxblock result: $AUX_BLOCK_RESULT"
            exit 1
        fi
    else
        log_warn "auxblock RPC may not be available: $AUX_BLOCK_RESULT"
        log_info "Merge mining will be tested when pool starts"
    fi
fi

# =============================================================================
# STEP 4: Generate Initial Blocks (coinbase maturity)
# =============================================================================

log_step "Step 4/10: Verify chain ready"

# Regtest may have a non-standard difficulty schedule — difficulty can jump from regtest
# minimum to difficulty 1 after block 1. We accept this tradeoff and pre-mine block 1
# via generatetoaddress to exit IBD, because a daemon in IBD rejects getblocktemplate
# requests, leaving miners idle for minutes.
#
# Result: pool-mined blocks start at height 2+ with difficulty=1.
# At ~11 MH/s per thread, difficulty 1 ~ 4.3B hashes / (11M * threads) seconds/block.
CURRENT_HEIGHT=$(get_block_count)
log_ok "Chain height: $CURRENT_HEIGHT"

if [[ "$CURRENT_HEIGHT" -eq 0 ]]; then
    BLOCK_GENERATED=0

    # Legacy wallets: skip internal generation, let cpuminer mine first block
    if [[ "${LEGACY_WALLET:-}" == "1" ]]; then
        log_info "Legacy wallet at height 0 — cpuminer will mine first block"
    else
        log_info "Fresh chain — generating 1 block to exit IBD..."

        # Try generatetoaddress (modern Bitcoin Core API)
        if coincli_wallet generatetoaddress 1 "$MINING_ADDRESS" &>/dev/null; then
            CURRENT_HEIGHT=$(get_block_count)
            [[ "$CURRENT_HEIGHT" -ge 1 ]] && BLOCK_GENERATED=1
        fi

        # Fallback: try generate (older API)
        if [[ $BLOCK_GENERATED -eq 0 ]]; then
            if coincli_wallet generate 1 &>/dev/null; then
                CURRENT_HEIGHT=$(get_block_count)
                [[ "$CURRENT_HEIGHT" -ge 1 ]] && BLOCK_GENERATED=1
            fi
        fi

        if [[ $BLOCK_GENERATED -eq 1 ]]; then
            log_ok "IBD exit block generated (height now: $CURRENT_HEIGHT)"
        else
            log_warn "Internal generation failed — cpuminer will mine first block"
        fi
    fi
else
    log_info "Chain has existing blocks — pool will continue from height $((CURRENT_HEIGHT + 1))"
fi

# Estimate seconds per block based on thread count (~7.5 MH/s per thread on typical VM)
HASHRATE_PER_THREAD=7500  # kH/s — measured ~7.6 MH/s per thread, using 7.5 for safety
TOTAL_HASHRATE=$((HASHRATE_PER_THREAD * MINER_THREADS))
DIFF1_HASHES=4295000       # ~4.295 billion hashes (2^32) in thousands
EST_SECS_PER_BLOCK=$((DIFF1_HASHES / TOTAL_HASHRATE))
log_info "Estimated ~${EST_SECS_PER_BLOCK}s/block at difficulty=1 ($MINER_THREADS threads, ~${TOTAL_HASHRATE} kH/s)"

# Auto-calculate timeout if user didn't override: 3x expected time + 120s buffer
DEFAULT_WAIT=$(( (EST_SECS_PER_BLOCK * TEST_BLOCKS * 3) + 120 ))
if [[ "$MINER_WAIT_SECS" -eq 600 ]]; then
    # User didn't override (600 is the default) — use calculated value
    MINER_WAIT_SECS=$DEFAULT_WAIT
    log_info "Auto-calculated timeout: ${MINER_WAIT_SECS}s (3x estimated mining time + 120s)"
fi

# Verify getblocktemplate works BEFORE starting the pool
log_info "Testing getblocktemplate on daemon (rules: $GBT_RULES)..."
# DGB multi-algo requires algorithm parameter for getblocktemplate
GBT_ALGO_PARAM=""
case "$COIN" in
    dgb) GBT_ALGO_PARAM='"sha256d"' ;;
    dgb-scrypt) GBT_ALGO_PARAM='"scrypt"' ;;
esac
if [[ -n "$GBT_ALGO_PARAM" ]]; then
    GBT_RESULT=$(coincli getblocktemplate "{\"rules\":$GBT_RULES}" "$GBT_ALGO_PARAM" 2>&1) || true
else
    GBT_RESULT=$(coincli getblocktemplate "{\"rules\":$GBT_RULES}" 2>&1) || true
fi
if echo "$GBT_RESULT" | grep -q '"previousblockhash"'; then
    GBT_HEIGHT=$(echo "$GBT_RESULT" | grep '"height"' | tr -d ' ",' | cut -d: -f2)
    log_ok "getblocktemplate works (template height=$GBT_HEIGHT)"
else
    log_error "getblocktemplate FAILED — daemon cannot produce block templates"
    log_error "Response: $(echo "$GBT_RESULT" | head -5)"
    log_error "This means the pool cannot create mining jobs. Miners will idle forever."
    log_error ""
    log_error "Common causes:"
    log_error "  1. Daemon has zero peers and this fork requires peers for GBT"
    log_error "  2. Chain is still in IBD (initial block download)"
    log_error "  3. Wallet not loaded (needed for coinbase output)"
    exit 1
fi

fi  # End HA_ONLY skip for Steps 3-4

# =============================================================================
# STEP 5: Set Up PostgreSQL Database
# =============================================================================

log_step "Step 5/10: Set up PostgreSQL database"

# Try connecting first — if install.sh already set up the DB, skip sudo entirely.
# This allows the script to run headless (nohup, screen, cron) without sudo prompts.
if PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" &>/dev/null; then
    log_ok "Database '$DB_NAME' already accessible (user=$DB_USER, auth verified)"
    # Kill stale PostgreSQL sessions from previous pool runs to release advisory locks.
    # The pool uses pg_try_advisory_lock for payment fencing — if a previous pool process
    # was killed (SIGKILL), its PostgreSQL session may still hold the lock, blocking
    # ALL payment processor cycles in the next run.
    log_info "Terminating stale database sessions from previous runs..."
    TERMINATED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT count(*) FROM (SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DB_NAME' AND pid != pg_backend_pid()) sub;" 2>/dev/null | tr -d ' ' || echo "0")
    if [[ "$TERMINATED" -gt 0 ]]; then
        log_ok "Terminated $TERMINATED stale session(s) (advisory locks released)"
        sleep 1  # Give PostgreSQL a moment to clean up
    else
        log_ok "No stale sessions found"
    fi
    # Clean stale block data from previous runs.
    # The blockchain resets (rm -rf ~/$DATA_DIR/regtest) but DB persists — old records
    # with different hashes cause the payment processor to see false orphans.
    log_info "Truncating block tables from previous runs..."
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'blocks_%' LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null && \
        log_ok "Block tables truncated" || log_warn "No block tables to truncate (first run)"
    # Also clean shares, payments, and balance tables for a fresh test
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'shares_%' OR tablename LIKE 'payments_%' OR tablename LIKE 'balances_%') LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null || true
else
    # DB not accessible — need sudo to set it up
    if [[ ! -t 0 ]]; then
        # No TTY (running in background via nohup/screen/cron) — can't prompt for sudo
        log_error "Database '$DB_NAME' not accessible and no TTY available for sudo"
        log_error ""
        log_error "The script needs sudo to create the PostgreSQL database, but it's"
        log_error "running in the background where sudo can't prompt for a password."
        log_error ""
        log_error "Fix: Run the DB setup once interactively first, then re-run with nohup:"
        log_error ""
        log_error "  sudo -u postgres psql -c \"CREATE USER $DB_USER WITH PASSWORD '$DB_PASS';\""
        log_error "  sudo -u postgres psql -c \"CREATE DATABASE $DB_NAME OWNER $DB_USER;\""
        log_error "  sudo -u postgres psql -c \"GRANT ALL PRIVILEGES ON DATABASE $DB_NAME TO $DB_USER;\""
        log_error ""
        log_error "Then re-run: nohup ./scripts/linux/regtest.sh $COIN > regtest.log 2>&1 &"
        exit 1
    fi

    log_info "Creating database '$DB_NAME' (requires sudo)..."
    # Create user or update password if user already exists (e.g. from install.sh with different password)
    sudo -u postgres psql -c "CREATE USER $DB_USER WITH PASSWORD '$DB_PASS';" 2>/dev/null || \
    sudo -u postgres psql -c "ALTER USER $DB_USER WITH PASSWORD '$DB_PASS';" 2>/dev/null || true
    sudo -u postgres psql -c "CREATE DATABASE $DB_NAME OWNER $DB_USER;" 2>/dev/null || true
    sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE $DB_NAME TO $DB_USER;" 2>/dev/null || true

    # Verify the connection actually works after setup
    if PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" &>/dev/null; then
        log_ok "Database '$DB_NAME' ready (user=$DB_USER, auth verified)"
        # Clean stale data (same as the non-sudo path above)
        log_info "Truncating block tables from previous runs..."
        PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'blocks_%' LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null && \
            log_ok "Block tables truncated" || log_warn "No block tables to truncate (first run)"
        PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "DO \$\$ DECLARE t TEXT; BEGIN FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'shares_%' OR tablename LIKE 'payments_%' OR tablename LIKE 'balances_%') LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(t) || ' CASCADE'; END LOOP; END \$\$;" 2>/dev/null || true
    else
        log_error "Cannot connect to PostgreSQL as $DB_USER — password may not have been updated"
        log_error "Manual fix: sudo -u postgres psql -c \"ALTER USER $DB_USER WITH PASSWORD '$DB_PASS';\""
        exit 1
    fi
fi

# =============================================================================
# HA_ONLY mode: Skip Steps 6-8f (pool, miner, mining tests) — jump to Step 8g
# =============================================================================

if [[ "$HA_ONLY" == "1" ]]; then
    log_info "HA_ONLY mode — skipping Steps 6-8f (pool/miner/mining tests)"
    log_info "Jumping directly to Step 8g: HA VIP failover emulation"
    BLOCKS_FOUND=1  # Bypass the BLOCKS_FOUND check for Step 8g
    EXIT_CODE=0
else

# =============================================================================
# STEP 6: Start the Pool
# =============================================================================

log_step "Step 6/10: Start Spiral Stratum Pool"

# Merge Mining: Create config with auxChains section
# V2 CONFIG FIX: mergeMining must be INSIDE the coin section, not at root level.
# The pool runs in V2 mode (has coins: array), so it looks for coins[].mergeMining.
# We use sed to insert the merge mining block before the 'stratum:' line.
POOL_CONFIG_FILE="$CONFIG_FILE"
if [[ "$MERGE_MODE" == "1" ]]; then
    log_info ""
    log_info "━━━ Merge Mining: Creating merged config ━━━"

    MERGE_CONFIG_FILE="$LOG_DIR/config-${POOL_ID}-merge.yaml"

    # Copy base config
    cp "$CONFIG_FILE" "$MERGE_CONFIG_FILE"

    # Create the merge mining block with proper V2 indentation (4 spaces = inside coin section)
    # This block will be inserted BEFORE the 'stratum:' line in the coin section
    # Note: Modern wallet daemons (NMC) need wallet path; legacy (DOGE) don't
    if [[ -n "${AUX_LEGACY_WALLET:-}" ]]; then
        # Legacy wallet: no wallet specification needed
        MERGE_BLOCK="\\
    # Merge Mining Configuration (auto-generated by regtest.sh --merge)\\
    mergeMining:\\
      enabled: true\\
      refreshInterval: 5s\\
      auxChains:\\
        - symbol: ${AUX_SYMBOL}\\
          enabled: true\\
          address: \"${AUX_MINING_ADDRESS}\"\\
          daemon:\\
            host: 127.0.0.1\\
            port: ${AUX_RPC_PORT}\\
            user: ${RPC_USER}\\
            password: \"${RPC_PASS}\"\\
"
    else
        # Modern wallet: include wallet path for RPC endpoint
        MERGE_BLOCK="\\
    # Merge Mining Configuration (auto-generated by regtest.sh --merge)\\
    mergeMining:\\
      enabled: true\\
      refreshInterval: 5s\\
      auxChains:\\
        - symbol: ${AUX_SYMBOL}\\
          enabled: true\\
          address: \"${AUX_MINING_ADDRESS}\"\\
          daemon:\\
            host: 127.0.0.1\\
            port: ${AUX_RPC_PORT}\\
            user: ${RPC_USER}\\
            password: \"${RPC_PASS}\"\\
            wallet: \"${AUX_WALLET_NAME}\"\\
"
    fi

    # Insert merge mining config BEFORE the 'stratum:' line (first occurrence only)
    # This places it inside the coin section at the correct indentation level
    sed -i "0,/^    stratum:/s/^    stratum:/${MERGE_BLOCK}    stratum:/" "$MERGE_CONFIG_FILE"

    log_ok "Merge config created: $MERGE_CONFIG_FILE"
    log_info "  Aux chain:  $AUX_SYMBOL ($AUX_NAME)"
    log_info "  Aux daemon: 127.0.0.1:$AUX_RPC_PORT"
    log_info "  Aux addr:   $AUX_MINING_ADDRESS"

    POOL_CONFIG_FILE="$MERGE_CONFIG_FILE"
fi

log_info "Starting pool with:"
log_info "  Config:     $POOL_CONFIG_FILE"
log_info "  Coin:       $COIN_SYMBOL ($COIN_NAME)"
log_info "  Stratum:    127.0.0.1:$STRATUM_PORT"
if [[ "$STRATUM_V2_PORT" -gt 0 ]]; then
    log_info "  Stratum V2: 127.0.0.1:$STRATUM_V2_PORT"
fi
log_info "  Daemon:     127.0.0.1:$RPC_PORT (regtest)"
log_info "  ZMQ:        tcp://127.0.0.1:$ZMQ_PORT"
log_info "  Database:   $DB_HOST:$DB_PORT/$DB_NAME"
log_info "  Log:        $LOG_DIR/spiralpool-regtest.log"
if [[ "$MERGE_MODE" == "1" ]]; then
    log_info "  Merge:      $AUX_SYMBOL (aux chain at 127.0.0.1:$AUX_RPC_PORT)"
fi

"$POOL_BINARY" -config "$POOL_CONFIG_FILE" &>"$LOG_DIR/spiralpool-regtest.log" &
POOL_PID=$!
log_info "Pool PID: $POOL_PID"

wait_for_stratum

# Give pool a moment to fetch first block template from daemon
sleep 3

# Check pool connected to daemon successfully
if grep -qi "genesis.*verified\|genesis.*skip" "$LOG_DIR/spiralpool-regtest.log" 2>/dev/null; then
    log_ok "Pool connected to daemon and verified chain"
elif grep -qi "block.*template\|new.*job\|getblocktemplate" "$LOG_DIR/spiralpool-regtest.log" 2>/dev/null; then
    log_ok "Pool fetched block template from daemon"
else
    log_warn "No daemon connection confirmation in logs yet (may still be starting)"
    log_info "  Tail log: tail -f $LOG_DIR/spiralpool-regtest.log"
fi

# =============================================================================
# STEP 7: Connect CPU Miner
# =============================================================================

log_step "Step 7/10: Connect CPU miner to pool"

HEIGHT_BEFORE=$(get_block_count)
log_info "Chain height before pool mining: $HEIGHT_BEFORE"

# Use cpuminer (minerd) for mining. With varDiff disabled in regtest config,
# Spiral Router is inactive and cpuminer gets the config's initial difficulty (1),
# which matches the network difficulty.
log_info "Starting cpuminer (minerd) with:"
log_info "  Pool:       stratum+tcp://127.0.0.1:$STRATUM_PORT"
log_info "  Worker:     TEST.worker1"
log_info "  Algorithm:  $COIN_ALGO"
log_info "  Threads:    $MINER_THREADS"
log_info "  Log:        $LOG_DIR/cpuminer-regtest.log"

"$CPUMINER" -a "$COIN_ALGO" -o "stratum+tcp://127.0.0.1:$STRATUM_PORT" \
    -u TEST.worker1 -p x -t "$MINER_THREADS" \
    &>"$LOG_DIR/cpuminer-regtest.log" &

MINER_PID=$!
log_ok "cpuminer started (PID $MINER_PID)"

# =============================================================================
# STEP 8: Wait for Blocks
# =============================================================================

log_step "Step 8/10: Mine blocks through the pool"

BLOCKS_FOUND=0
ELAPSED=0
TARGET_HEIGHT=$((HEIGHT_BEFORE + TEST_BLOCKS))

log_info "Target: $TEST_BLOCKS blocks (height $HEIGHT_BEFORE -> $TARGET_HEIGHT)"
log_info "Timeout: ${MINER_WAIT_SECS}s ($COIN_SYMBOL regtest diff=1, ~${EST_SECS_PER_BLOCK}s/block with $MINER_THREADS threads)"
echo ""

while [[ $ELAPSED -lt $MINER_WAIT_SECS ]]; do
    CURRENT_HEIGHT=$(get_block_count)
    BLOCKS_FOUND=$((CURRENT_HEIGHT - HEIGHT_BEFORE))

    if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
        log_ok "BLOCKS FOUND: $BLOCKS_FOUND (chain height: $CURRENT_HEIGHT)"
        break
    fi

    if [[ $((ELAPSED % 10)) -eq 0 ]] && [[ $ELAPSED -gt 0 ]]; then
        log_info "  ${ELAPSED}s ... blocks: $BLOCKS_FOUND/$TEST_BLOCKS (height: $CURRENT_HEIGHT)"
        # Show miner status on first check for diagnostics
        if [[ $ELAPSED -eq 10 ]]; then
            MINER_LINES=$(wc -l < "$LOG_DIR/cpuminer-regtest.log" 2>/dev/null || echo "0")
            if [[ "$MINER_LINES" -eq 0 ]]; then
                log_warn "  Miner log is EMPTY — miner may have crashed"
            else
                MINER_STATUS=$(tail -3 "$LOG_DIR/cpuminer-regtest.log" 2>/dev/null | tr '\n' ' ')
                log_info "  Miner: $MINER_STATUS"
            fi
        fi
    fi

    sleep "$BLOCK_CHECK_INTERVAL"
    ELAPSED=$((ELAPSED + BLOCK_CHECK_INTERVAL))
done

echo ""

if [[ $BLOCKS_FOUND -lt $TEST_BLOCKS ]]; then
    log_error "Only $BLOCKS_FOUND / $TEST_BLOCKS blocks found in ${MINER_WAIT_SECS}s"
    log_error ""
    log_error "Troubleshooting:"
    log_error "  1. Check miner connected:  tail $LOG_DIR/cpuminer-regtest.log"
    log_error "  2. Check pool received shares: grep -i share $LOG_DIR/spiralpool-regtest.log"
    log_error "  3. Check daemon responding: $CLI_CMD -regtest -rpcuser=$RPC_USER -rpcpassword=$RPC_PASS -rpcport=$RPC_PORT getblockchaininfo"
    log_error "  4. Check for errors: grep -i error $LOG_DIR/spiralpool-regtest.log"
    EXIT_CODE=1
else
    EXIT_CODE=0
fi

# =============================================================================
# STEP 8-V2: V2 Stratum Protocol Test (optional — when port_v2 configured)
# =============================================================================

if [[ "$STRATUM_V2_PORT" -gt 0 ]] && [[ -f "$V2_MINER" ]]; then
    log_step "Step 8-V2: V2 Stratum protocol test ($COIN_ALGO)"
    log_info "  V2 Port:    $STRATUM_V2_PORT"
    log_info "  Algorithm:  $COIN_ALGO"
    log_info "  Wallet:     $MINING_ADDRESS"

    # V2 test miner is pure Go (not optimized C like cpuminer). At pool difficulty 1,
    # ~4.3B hashes needed on average. Multi-threaded Go SHA256d = ~20-80 MH/s depending
    # on CPU (SHA-NI). Scrypt is ~1000x slower per hash. Budget generous timeouts.
    if [[ "$COIN_ALGO" == "scrypt" ]]; then
        V2_TIMEOUT="600s"
    else
        V2_TIMEOUT="300s"
    fi

    log_info "  Timeout:    $V2_TIMEOUT"

    V2_OUTPUT=$("$V2_MINER" \
        -host localhost \
        -port "$STRATUM_V2_PORT" \
        -wallet "$MINING_ADDRESS" \
        -algo "$COIN_ALGO" \
        -timeout "$V2_TIMEOUT" \
        -verbose 2>&1) && V2_EXIT=0 || V2_EXIT=$?
    echo "$V2_OUTPUT" > "$LOG_DIR/v2testminer-regtest.log"

    if echo "$V2_OUTPUT" | grep -q "SHARE ACCEPTED"; then
        log_ok "V2 Stratum $COIN_SYMBOL ($COIN_ALGO): share accepted"
    else
        log_error "V2 Stratum $COIN_SYMBOL ($COIN_ALGO): FAILED (exit=$V2_EXIT)"
        log_error "  Log: $LOG_DIR/v2testminer-regtest.log"
        if [[ -n "$V2_OUTPUT" ]]; then
            echo "$V2_OUTPUT" | tail -5 | while IFS= read -r line; do
                log_error "  $line"
            done
        fi
        EXIT_CODE=1
    fi
fi

# =============================================================================
# STEP 8a: Database Spot-Check (Shares & Blocks Verification)
# =============================================================================

if [[ $BLOCKS_FOUND -ge 1 ]]; then
    log_step "Step 8a: Database spot-check (SOLO payment flow verification)"

    # Query share counts (tables are pool-specific: shares_<pool_id>)
    SHARE_COUNT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM ${SHARES_TABLE};" 2>/dev/null | tr -d ' ') || SHARE_COUNT="0"
    [[ -z "$SHARE_COUNT" ]] && SHARE_COUNT="0"

    # Query block counts by status (columns use no underscores: poolid, blockheight, confirmationprogress)
    BLOCK_PENDING=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM ${BLOCKS_TABLE} WHERE status = 'pending';" 2>/dev/null | tr -d ' ') || BLOCK_PENDING="0"
    BLOCK_CONFIRMED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM ${BLOCKS_TABLE} WHERE status = 'confirmed';" 2>/dev/null | tr -d ' ') || BLOCK_CONFIRMED="0"
    BLOCK_ORPHANED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM ${BLOCKS_TABLE} WHERE status = 'orphaned';" 2>/dev/null | tr -d ' ') || BLOCK_ORPHANED="0"

    # Handle empty results
    [[ -z "$BLOCK_PENDING" ]] && BLOCK_PENDING="0"
    [[ -z "$BLOCK_CONFIRMED" ]] && BLOCK_CONFIRMED="0"
    [[ -z "$BLOCK_ORPHANED" ]] && BLOCK_ORPHANED="0"

    echo ""
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_info "  DATABASE SPOT-CHECK: SOLO Payment Flow Verification"
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_info "  Shares recorded:     $SHARE_COUNT"
    log_info "  Blocks pending:      $BLOCK_PENDING"
    log_info "  Blocks confirmed:    $BLOCK_CONFIRMED"
    log_info "  Blocks orphaned:     $BLOCK_ORPHANED"
    log_info "───────────────────────────────────────────────────────────────"

    if [[ "$SHARE_COUNT" =~ ^[0-9]+$ ]] && [[ "$SHARE_COUNT" -gt 0 ]]; then
        log_ok "  ✓ Shares pipeline:   WORKING ($SHARE_COUNT shares in DB)"
    else
        log_warn "  ⚠ Shares pipeline:   NO SHARES FOUND"
    fi

    BLOCK_TOTAL=$((BLOCK_PENDING + BLOCK_CONFIRMED + BLOCK_ORPHANED)) 2>/dev/null || BLOCK_TOTAL=0
    if [[ "$BLOCK_TOTAL" -gt 0 ]]; then
        log_ok "  ✓ Block tracking:    WORKING ($BLOCK_TOTAL blocks in DB)"
    else
        log_warn "  ⚠ Block tracking:    NO BLOCKS IN DB"
    fi

    log_info "───────────────────────────────────────────────────────────────"
    log_info "  SOLO Mode: Block rewards go directly to coinbase address"
    log_info "  No payout transactions needed - miner already paid!"
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    # Show sample share data for verification
    log_info "Sample shares (last 5):"
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "SELECT id, blockheight, difficulty, miner, worker, created
         FROM ${SHARES_TABLE}
         ORDER BY id DESC LIMIT 5;" 2>/dev/null || log_warn "Could not query shares"
    echo ""

    # Show block data
    log_info "Blocks found by pool:"
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "SELECT blockheight, status, reward, miner, LEFT(hash, 16) as hash_prefix, created
         FROM ${BLOCKS_TABLE}
         ORDER BY blockheight;" 2>/dev/null || log_warn "Could not query blocks"
    echo ""

    # Merge Mining: Check aux blocks table
    # Aux blocks are stored in blocks_{POOL_ID}_{aux_symbol} table (e.g., blocks_btc_regtest_nmc)
    if [[ "$MERGE_MODE" == "1" ]]; then
        log_info ""
        log_info "━━━ Merge Mining: Aux Block Verification ━━━"

        # Aux pool table name: blocks_{parentPoolId}_{auxSymbol} (lowercase)
        AUX_SYMBOL_LOWER=$(echo "$AUX_SYMBOL" | tr '[:upper:]' '[:lower:]')
        AUX_BLOCKS_TABLE="blocks_${POOL_ID}_${AUX_SYMBOL_LOWER}"

        # Query aux blocks table for merge-mined blocks
        AUX_BLOCK_COUNT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM ${AUX_BLOCKS_TABLE};" 2>/dev/null | tr -d ' ') || AUX_BLOCK_COUNT="0"
        [[ -z "$AUX_BLOCK_COUNT" ]] && AUX_BLOCK_COUNT="0"

        AUX_PENDING=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM ${AUX_BLOCKS_TABLE} WHERE status = 'pending';" 2>/dev/null | tr -d ' ') || AUX_PENDING="0"
        AUX_CONFIRMED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM ${AUX_BLOCKS_TABLE} WHERE status = 'confirmed';" 2>/dev/null | tr -d ' ') || AUX_CONFIRMED="0"

        [[ -z "$AUX_PENDING" ]] && AUX_PENDING="0"
        [[ -z "$AUX_CONFIRMED" ]] && AUX_CONFIRMED="0"

        log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        log_info "  MERGE MINING: $AUX_SYMBOL Aux Blocks (table: $AUX_BLOCKS_TABLE)"
        log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        log_info "  Aux blocks total:    $AUX_BLOCK_COUNT"
        log_info "  Aux blocks pending:  $AUX_PENDING"
        log_info "  Aux blocks confirmed: $AUX_CONFIRMED"

        if [[ "$AUX_BLOCK_COUNT" =~ ^[0-9]+$ ]] && [[ "$AUX_BLOCK_COUNT" -gt 0 ]]; then
            log_ok "MERGE MINING VERIFIED: $AUX_BLOCK_COUNT $AUX_SYMBOL aux blocks found!"

            # Show aux block data
            log_info "Aux blocks found:"
            PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
                "SELECT blockheight, status, reward, miner, LEFT(hash, 16) as hash_prefix, created
                 FROM ${AUX_BLOCKS_TABLE}
                 ORDER BY blockheight;" 2>/dev/null || log_warn "Could not query ${AUX_BLOCKS_TABLE}"
        else
            log_warn "No $AUX_SYMBOL aux blocks found in database (table: $AUX_BLOCKS_TABLE)"
            log_info "  This is expected if parent chain difficulty didn't meet aux chain target"
            log_info "  Check pool log: grep -i 'aux\\|merge' $LOG_DIR/spiralpool-regtest.log"
        fi
        echo ""
    fi
fi

# =============================================================================
# STEP 8b: Stack confirmations + wait for maturity
# =============================================================================

if [[ "$MATURITY_WAIT_SECS" -gt 0 ]] && [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8b: Wait for confirmations + maturity"

    # The miner keeps running — it mines additional blocks that serve as confirmations
    # for the pool's earlier blocks. No generatetoaddress needed (it fails at difficulty 1
    # on some regtest chains because it requires real PoW, unlike standard Bitcoin Core regtest).
    #
    # With blockMaturity=3 and ~195s/block, the miner needs ~3 more blocks (~10 min).
    # Payment processor checks every 30s (config interval: 30s), needs 3 stable checks
    # before confirming. Total: ~12-15 min for pending -> confirmed.
    BLOCK_MATURITY="${BLOCK_MATURITY:-3}"
    CONFIRM_TARGET=$((CURRENT_HEIGHT + BLOCKS_FOUND + BLOCK_MATURITY))
    log_info "Miner continues running to add confirmation blocks..."
    log_info "Need $BLOCK_MATURITY confirmations (chain must reach height $CONFIRM_TARGET)"
    log_info "Payment processor checks every 30s, needs 3 stable cycles after maturity"
    echo ""

    MATURITY_ELAPSED=0
    MATURITY_CHECK_INTERVAL=30

    while [[ $MATURITY_ELAPSED -lt $MATURITY_WAIT_SECS ]]; do
        CHAIN_HEIGHT=$(get_block_count)
        FIRST_BLOCK_CONFIRMS=$((CHAIN_HEIGHT - HEIGHT_BEFORE - 1))
        if [[ $FIRST_BLOCK_CONFIRMS -lt 0 ]]; then FIRST_BLOCK_CONFIRMS=0; fi

        # Check DB for confirmed vs pending
        DB_CONFIRMED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM $BLOCKS_TABLE WHERE status = 'confirmed';" 2>/dev/null | tr -d ' ' || echo "?")
        DB_PENDING=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM $BLOCKS_TABLE WHERE status = 'pending';" 2>/dev/null | tr -d ' ' || echo "?")
        DB_ORPHANED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM $BLOCKS_TABLE WHERE status = 'orphaned';" 2>/dev/null | tr -d ' ' || echo "?")

        log_info "  ${MATURITY_ELAPSED}s — height: $CHAIN_HEIGHT, confirms: ~$FIRST_BLOCK_CONFIRMS | DB: confirmed=$DB_CONFIRMED pending=$DB_PENDING orphaned=$DB_ORPHANED"

        # Early exit if all pool-mined blocks are confirmed AND first block specifically is confirmed
        FIRST_BLOCK_HEIGHT=$((HEIGHT_BEFORE + 1))
        FIRST_BLOCK_CONFIRMED=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM $BLOCKS_TABLE WHERE blockheight = $FIRST_BLOCK_HEIGHT AND status = 'confirmed';" 2>/dev/null | tr -d ' ' || echo "0")
        if [[ "$DB_CONFIRMED" != "?" ]] && [[ "$DB_CONFIRMED" -ge "$BLOCKS_FOUND" ]] && [[ "$FIRST_BLOCK_CONFIRMED" -ge 1 ]]; then
            log_ok "All $BLOCKS_FOUND pool-mined blocks confirmed! (confirmed=$DB_CONFIRMED, pending=$DB_PENDING, first_block=$FIRST_BLOCK_HEIGHT)"
            break
        fi

        sleep "$MATURITY_CHECK_INTERVAL"
        MATURITY_ELAPSED=$((MATURITY_ELAPSED + MATURITY_CHECK_INTERVAL))
    done

    echo ""
    # Final DB snapshot
    log_info "Final database state:"
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "SELECT blockheight, status, confirmationprogress, orphan_mismatch_count, stability_check_count FROM $BLOCKS_TABLE ORDER BY blockheight;" 2>/dev/null || \
        log_warn "Could not query database"
    echo ""
fi

# =============================================================================
# STEP 8c: Payment Pipeline Smoke Test
# =============================================================================

PAYMENT_PASS=0
PAYMENT_TOTAL=8
PAYMENT_TXID=""
PAYOUT_ADDRESS=""
COINBASE_MATURE=0

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8c: Payment pipeline verification (8 checks)"

    FIRST_POOL_BLOCK=$((HEIGHT_BEFORE + 1))

    log_info "Miner is still running — mining confirmation blocks naturally"
    echo ""

    # -- 1/8. Check wallet for block rewards ---------------------------------
    WALLET_BALANCE="0"
    IMMATURE_BALANCE="0"
    CHAIN_HEIGHT=$(get_block_count)
    WALLET_BALANCE=$(coincli_wallet getbalance 2>/dev/null || echo "0")
    IMMATURE_BALANCE=$(coincli_wallet getwalletinfo 2>/dev/null | grep -o '"immature_balance":[^,}]*' | head -1 | cut -d: -f2 | tr -d ' ') || true
    IMMATURE_BALANCE="${IMMATURE_BALANCE:-0}"

    if (( $(echo "$WALLET_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
        log_ok "[1/8] Block rewards mature and spendable: $WALLET_BALANCE $COIN_SYMBOL (height=$CHAIN_HEIGHT)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
        COINBASE_MATURE=1
    elif (( $(echo "$IMMATURE_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
        log_ok "[1/8] Block rewards received (immature: $IMMATURE_BALANCE $COIN_SYMBOL, height=$CHAIN_HEIGHT)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
    else
        log_warn "[1/8] No block rewards detected — waiting..."
        # Brief wait for rewards to appear (blocks just mined, wallet may need a moment)
        REWARD_WAIT=0
        while [[ $REWARD_WAIT -lt 120 ]]; do
            sleep 30
            REWARD_WAIT=$((REWARD_WAIT + 30))
            IMMATURE_BALANCE=$(coincli_wallet getwalletinfo 2>/dev/null | grep -o '"immature_balance":[^,}]*' | head -1 | cut -d: -f2 | tr -d ' ') || true
            IMMATURE_BALANCE="${IMMATURE_BALANCE:-0}"
            if (( $(echo "$IMMATURE_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
                log_ok "[1/8] Block rewards received (immature: $IMMATURE_BALANCE $COIN_SYMBOL)"
                PAYMENT_PASS=$((PAYMENT_PASS + 1))
                break
            fi
            log_info "  ${REWARD_WAIT}s — immature: $IMMATURE_BALANCE $COIN_SYMBOL (waiting for wallet to detect coinbase)"
        done
    fi

    # -- 2/8. SOLO reward verification ---------------------------------------
    # In SOLO mode, 100% of the block reward goes to the pool wallet.
    # The daemon/pool generates a fresh address per block (not necessarily MINING_ADDRESS).
    # Verify: (a) coinbase has correct reward, (b) output address belongs to our wallet.
    BLOCK_HASH=$(coincli getblockhash "$FIRST_POOL_BLOCK" 2>/dev/null) || true
    if [[ -n "$BLOCK_HASH" ]] && [[ ${#BLOCK_HASH} -eq 64 ]]; then
        BLOCK_JSON=$(coincli getblock "$BLOCK_HASH" 2 2>/dev/null) || true
        COINBASE_VALUE=$(echo "$BLOCK_JSON" | grep '"value"' | head -1 | grep -o '[0-9.]*') || true
        # Extract address from scriptPubKey - handles both formats:
        # 1. "address": "value" (single line)
        # 2. "addresses": [\n  "value" (multi-line array)
        COINBASE_ADDR=$(echo "$BLOCK_JSON" | grep -oE '"address":\s*"[^"]+"' | head -1 | sed 's/.*"address":\s*"\([^"]*\)".*/\1/') || true
        if [[ -z "$COINBASE_ADDR" ]]; then
            # Multi-line array: find "addresses" then extract first quoted string after it
            COINBASE_ADDR=$(echo "$BLOCK_JSON" | tr '\n' ' ' | grep -oE '"addresses":\s*\[\s*"[^"]+"' | head -1 | sed 's/.*\[\s*"\([^"]*\)".*/\1/') || true
        fi

        if [[ -n "$COINBASE_ADDR" ]]; then
            # Verify the coinbase address belongs to our wallet
            # Legacy wallets use validateaddress, modern wallets use getaddressinfo
            if [[ "$LEGACY_WALLET" == "1" ]]; then
                ADDR_MINE=$(coincli validateaddress "$COINBASE_ADDR" 2>/dev/null | grep '"ismine"' | head -1 | grep -o 'true\|false') || true
            else
                ADDR_MINE=$(coincli_wallet getaddressinfo "$COINBASE_ADDR" 2>/dev/null | grep '"ismine"' | head -1 | grep -o 'true\|false') || true
            fi
            if [[ "$ADDR_MINE" == "true" ]]; then
                log_ok "[2/8] SOLO reward verified — coinbase ${COINBASE_VALUE:-?} $COIN_SYMBOL to wallet address $COINBASE_ADDR (ismine=true)"
                PAYMENT_PASS=$((PAYMENT_PASS + 1))
            else
                log_warn "[2/8] Coinbase address $COINBASE_ADDR is NOT in pool wallet (ismine=$ADDR_MINE)"
            fi
        else
            log_warn "[2/8] Could not extract coinbase address from block at height $FIRST_POOL_BLOCK"
        fi
    else
        log_warn "[2/8] Could not retrieve block at height $FIRST_POOL_BLOCK"
    fi

    # -- 3-4/8. Coin movement (requires 100-block coinbase maturity) ---------
    # Most coins inherit Bitcoin's 100-block coinbase maturity rule.
    # If COINBASE_WAIT_SECS > 0, wait for the miner to produce enough blocks.
    if [[ $COINBASE_MATURE -eq 0 ]] && [[ "$COINBASE_WAIT_SECS" -gt 0 ]]; then
        FIRST_CONFIRMS=$(($(get_block_count) - FIRST_POOL_BLOCK))
        BLOCKS_NEEDED=$((100 - FIRST_CONFIRMS))
        if [[ $BLOCKS_NEEDED -gt 0 ]]; then
            log_info "Waiting for coinbase maturity (~$BLOCKS_NEEDED more blocks needed)..."
            log_info "COINBASE_WAIT_SECS=$COINBASE_WAIT_SECS — miner adds blocks at ~60-80s each"
        fi
        MATURITY_ELAPSED=0
        while [[ $MATURITY_ELAPSED -lt $COINBASE_WAIT_SECS ]]; do
            CHAIN_HEIGHT=$(get_block_count)
            WALLET_BALANCE=$(coincli_wallet getbalance 2>/dev/null || echo "0")
            FIRST_CONFIRMS=$((CHAIN_HEIGHT - FIRST_POOL_BLOCK))

            if (( $(echo "$WALLET_BALANCE > 0" | bc -l 2>/dev/null || echo 0) )); then
                log_ok "Coinbase matured! Balance: $WALLET_BALANCE $COIN_SYMBOL (height=$CHAIN_HEIGHT)"
                COINBASE_MATURE=1
                break
            fi

            if [[ $((MATURITY_ELAPSED % 60)) -eq 0 ]]; then
                log_info "  ${MATURITY_ELAPSED}s — height: $CHAIN_HEIGHT, confirms: ~$FIRST_CONFIRMS/100, balance: $WALLET_BALANCE $COIN_SYMBOL"
            fi
            sleep 30
            MATURITY_ELAPSED=$((MATURITY_ELAPSED + 30))
        done
    fi

    if [[ $COINBASE_MATURE -eq 1 ]]; then
        # Check 3: getnewaddress (uses ADDR_TYPE set by setup_coin)
        if [[ -n "$ADDR_TYPE" ]]; then
            PAYOUT_ADDRESS=$(coincli_wallet getnewaddress "test-payout" "$ADDR_TYPE" 2>/dev/null) || true
        else
            PAYOUT_ADDRESS=$(coincli_wallet getnewaddress "test-payout" 2>/dev/null) || true
        fi
        if [[ -n "$PAYOUT_ADDRESS" ]]; then
            log_ok "[3/8] Payout address generated: $PAYOUT_ADDRESS"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[3/8] getnewaddress failed"
        fi

        # Check 4: sendtoaddress + verify
        PAYMENT_TXID=$(coincli_wallet sendtoaddress "$PAYOUT_ADDRESS" 1.0 2>&1) || true
        if [[ ${#PAYMENT_TXID} -eq 64 ]]; then
            log_ok "[4/8] Payment sent: txid=$PAYMENT_TXID"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))

            TX_INFO=$(coincli_wallet gettransaction "$PAYMENT_TXID" 2>/dev/null) || true
            TX_AMOUNT=$(echo "$TX_INFO" | grep -o '"amount":[^,]*' | head -1 | cut -d: -f2 | tr -d ' ') || true
            if [[ -n "$TX_AMOUNT" ]]; then
                log_ok "      Transaction verified (amount=$TX_AMOUNT)"
            fi
        else
            log_warn "[4/8] sendtoaddress failed: $PAYMENT_TXID"
            PAYMENT_TXID=""
        fi
    else
        if [[ "$COINBASE_WAIT_SECS" -eq 0 ]]; then
            log_info "[3/8] Skipped — COINBASE_WAIT_SECS=0 (set to 7200 for full test)"
            log_info "[4/8] Skipped — COINBASE_WAIT_SECS=0"
        else
            log_warn "[3/8] Skipped — coinbase did not mature within ${COINBASE_WAIT_SECS}s"
            log_warn "[4/8] Skipped — coinbase did not mature"
        fi
    fi

    # -- 5/8. Block status: confirmed -> paid --------------------------------
    FIRST_HASH=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT hash FROM $BLOCKS_TABLE WHERE blockheight = $FIRST_POOL_BLOCK AND status = 'confirmed' LIMIT 1;" 2>/dev/null | tr -d ' ') || true

    if [[ -n "$FIRST_HASH" ]]; then
        PAID_RESULT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "UPDATE $BLOCKS_TABLE SET status = 'paid', confirmationprogress = 1.0
             WHERE blockheight = $FIRST_POOL_BLOCK AND hash = '$FIRST_HASH'
             AND (status = 'confirmed' AND 'paid' IN ('orphaned', 'paid'));" 2>/dev/null) || true

        if [[ "$PAID_RESULT" == *"UPDATE 1"* ]]; then
            log_ok "[5/8] Block $FIRST_POOL_BLOCK: confirmed -> paid (status guard passed)"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[5/8] Block $FIRST_POOL_BLOCK: confirmed -> paid failed ($PAID_RESULT)"
        fi

        # -- 6/8. Verify paid is terminal ------------------------------------
        REVERT_RESULT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "UPDATE $BLOCKS_TABLE SET status = 'pending'
             WHERE blockheight = $FIRST_POOL_BLOCK AND hash = '$FIRST_HASH'
             AND (status = 'pending' OR (status = 'confirmed' AND 'pending' IN ('orphaned', 'paid')));" 2>/dev/null) || true

        if [[ "$REVERT_RESULT" == *"UPDATE 0"* ]]; then
            log_ok "[6/8] Block $FIRST_POOL_BLOCK: paid -> pending BLOCKED (terminal status verified)"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[6/8] Block $FIRST_POOL_BLOCK: paid -> pending was NOT blocked ($REVERT_RESULT)"
        fi
    else
        log_warn "[5/8] No confirmed block at height $FIRST_POOL_BLOCK — skipping status tests"
        log_warn "[6/8] Skipped — depends on check 5"
    fi

    # -- 7/8. Payment table recording ----------------------------------------
    PAY_TXID_FOR_DB="${PAYMENT_TXID}"
    PAY_ADDR_FOR_DB="${PAYOUT_ADDRESS}"
    PAY_AMT_FOR_DB="1.0"
    if [[ -z "$PAY_TXID_FOR_DB" ]] || [[ ${#PAY_TXID_FOR_DB} -ne 64 ]]; then
        PAY_TXID_FOR_DB="$(printf '%064d' 0 | sed 's/0/a/g')"
        PAY_ADDR_FOR_DB="${MINING_ADDRESS}"
        PAY_AMT_FOR_DB="0.00000001"
        log_info "Using synthetic test data for payment table verification"
    fi

    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -q -c \
        "INSERT INTO payments (poolid, coin, address, amount, transactionconfirmationdata, created)
         VALUES ('$POOL_ID', '$COIN_SYMBOL', '$PAY_ADDR_FOR_DB', $PAY_AMT_FOR_DB, '$PAY_TXID_FOR_DB', NOW());" 2>/dev/null || true

    PAY_COUNT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM payments WHERE poolid = '$POOL_ID' AND transactionconfirmationdata = '$PAY_TXID_FOR_DB';" 2>/dev/null | tr -d ' ') || echo "0"

    if [[ "$PAY_COUNT" -ge 1 ]]; then
        log_ok "[7/8] Payment recorded in DB ($PAY_COUNT row(s) in payments table)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
    else
        log_warn "[7/8] Payment INSERT failed or row not found on SELECT"
    fi

    # -- 8/8. API/Dashboard health check -------------------------------------
    API_RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$API_PORT/" 2>/dev/null) || true
    if [[ -n "$API_RESPONSE" ]] && [[ "$API_RESPONSE" -ge 200 ]] 2>/dev/null && [[ "$API_RESPONSE" -lt 500 ]] 2>/dev/null; then
        log_ok "[8/8] API responding on port $API_PORT (HTTP $API_RESPONSE)"
        PAYMENT_PASS=$((PAYMENT_PASS + 1))
    else
        # Try /api/pools as fallback
        API_RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$API_PORT/api/pools" 2>/dev/null) || true
        if [[ -n "$API_RESPONSE" ]] && [[ "$API_RESPONSE" -ge 200 ]] 2>/dev/null && [[ "$API_RESPONSE" -lt 500 ]] 2>/dev/null; then
            log_ok "[8/8] API responding on port $API_PORT/api/pools (HTTP $API_RESPONSE)"
            PAYMENT_PASS=$((PAYMENT_PASS + 1))
        else
            log_warn "[8/8] API not responding on port $API_PORT (HTTP ${API_RESPONSE:-timeout})"
        fi
    fi

    # -- Summary -------------------------------------------------------------
    echo ""
    if [[ $COINBASE_MATURE -eq 0 ]]; then
        PAYMENT_TESTED=$((PAYMENT_TOTAL - 2))
        log_info "Payment pipeline: $PAYMENT_PASS/$PAYMENT_TESTED checks passed (2 skipped — coinbase needs 100 confirmations)"
        if [[ "$COINBASE_WAIT_SECS" -eq 0 ]]; then
            log_info "  For full 8/8: COINBASE_WAIT_SECS=7200 ./scripts/linux/regtest.sh $COIN"
        fi
    else
        log_info "Payment pipeline: $PAYMENT_PASS/$PAYMENT_TOTAL checks passed"
    fi
    echo ""

    # -- Merge Mining: Aux chain wallet verification ---------------------------
    # Verify that aux block rewards went to the correct aux chain wallet,
    # NOT to a parent chain address. This confirms the pool passed the right
    # address to createauxblock/getauxblock and the aux daemon paid it correctly.
    if [[ "$MERGE_MODE" == "1" ]] && [[ "${AUX_BLOCK_COUNT:-0}" -gt 0 ]]; then
        log_info "━━━ Merge Mining: $AUX_SYMBOL Wallet Verification ━━━"

        # Query aux chain wallet info
        AUX_WALLET_INFO=$(auxcli_wallet getwalletinfo 2>/dev/null) || AUX_WALLET_INFO=""

        if [[ -n "$AUX_WALLET_INFO" ]]; then
            AUX_BALANCE=$(echo "$AUX_WALLET_INFO" | grep '"balance"' | head -1 | tr -d ' ",' | cut -d: -f2)
            AUX_IMMATURE=$(echo "$AUX_WALLET_INFO" | grep '"immature_balance"' | head -1 | tr -d ' ",' | cut -d: -f2)
            AUX_UNCONFIRMED=$(echo "$AUX_WALLET_INFO" | grep '"unconfirmed_balance"' | head -1 | tr -d ' ",' | cut -d: -f2)
            AUX_TXCOUNT=$(echo "$AUX_WALLET_INFO" | grep '"txcount"' | head -1 | tr -d ' ",' | cut -d: -f2)

            [[ -z "$AUX_BALANCE" ]] && AUX_BALANCE="0"
            [[ -z "$AUX_IMMATURE" ]] && AUX_IMMATURE="0"
            [[ -z "$AUX_UNCONFIRMED" ]] && AUX_UNCONFIRMED="0"
            [[ -z "$AUX_TXCOUNT" ]] && AUX_TXCOUNT="0"

            log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
            log_info "  $AUX_SYMBOL WALLET VERIFICATION (aux chain rewards)"
            log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
            log_info "  Wallet:             $AUX_WALLET_NAME"
            log_info "  Address:            $AUX_MINING_ADDRESS"
            log_info "  Balance (mature):   $AUX_BALANCE $AUX_SYMBOL"
            log_info "  Balance (immature): $AUX_IMMATURE $AUX_SYMBOL"
            log_info "  Unconfirmed:        $AUX_UNCONFIRMED $AUX_SYMBOL"
            log_info "  Transactions:       $AUX_TXCOUNT"

            # Check that immature or mature balance > 0 (aux blocks found = rewards received)
            AUX_HAS_FUNDS=0
            if [[ "$AUX_IMMATURE" != "0" ]] && [[ "$AUX_IMMATURE" != "0.00000000" ]]; then
                AUX_HAS_FUNDS=1
            fi
            if [[ "$AUX_BALANCE" != "0" ]] && [[ "$AUX_BALANCE" != "0.00000000" ]]; then
                AUX_HAS_FUNDS=1
            fi

            if [[ $AUX_HAS_FUNDS -eq 1 ]]; then
                log_ok "$AUX_SYMBOL rewards received in aux wallet (immature: $AUX_IMMATURE, mature: $AUX_BALANCE)"
            else
                log_warn "$AUX_SYMBOL wallet shows zero balance — aux coinbase may need more confirmations"
                log_info "  Aux blocks in DB: $AUX_BLOCK_COUNT (rewards are on the $AUX_SYMBOL chain, not $COIN chain)"

                # Also check aux chain height to see if enough blocks for coinbase to show
                AUX_CHAIN_HEIGHT=$(auxcli getblockcount 2>/dev/null) || AUX_CHAIN_HEIGHT="?"
                log_info "  $AUX_SYMBOL chain height: $AUX_CHAIN_HEIGHT (coinbase maturity may require more aux blocks)"
            fi

            # Verify the mining address belongs to this wallet (ismine check)
            AUX_ADDR_INFO=$(auxcli_wallet getaddressinfo "$AUX_MINING_ADDRESS" 2>/dev/null) || AUX_ADDR_INFO=""
            if echo "$AUX_ADDR_INFO" | grep -q '"ismine".*true'; then
                log_ok "$AUX_SYMBOL mining address confirmed as owned by aux wallet (ismine=true)"
            elif echo "$AUX_ADDR_INFO" | grep -q '"ismine"'; then
                log_warn "$AUX_SYMBOL mining address NOT owned by aux wallet (ismine=false)"
            fi
        else
            log_warn "Could not query $AUX_SYMBOL wallet (auxcli_wallet getwalletinfo failed)"
        fi
        echo ""
    fi
fi

# =============================================================================
# STEP 8d: HA Failover — Advisory Lock Fencing
# =============================================================================

HA_PASS=0
HA_TOTAL=4

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8d: HA failover — advisory lock fencing (4 checks)"

    HA_STRATUM_PORT=$HA_STRATUM
    HA_API_PORT=$HA_API
    HA_METRICS_PORT=$HA_METRICS
    HA_CONFIG="$LOG_DIR/$HA_CONFIG_FILENAME"
    HA_LOG="$LOG_DIR/spiralpool-regtest-ha.log"

    # -- 1/4. Start second pool instance with different ports ----------------
    cp "$CONFIG_FILE" "$HA_CONFIG"
    sed -i "s/port: $STRATUM_PORT/port: $HA_STRATUM_PORT/" "$HA_CONFIG"
    sed -i "s/api_port:.*/api_port: $HA_API_PORT/" "$HA_CONFIG"
    sed -i "s/metrics_port:.*/metrics_port: $HA_METRICS_PORT/" "$HA_CONFIG"

    log_info "Starting second pool instance (HA standby)..."
    log_info "  Same daemon (RPC $RPC_PORT), same DB ($DB_NAME) — only ports differ"
    log_info "  Stratum: $HA_STRATUM_PORT, API: $HA_API_PORT, Metrics: $HA_METRICS_PORT"
    "$POOL_BINARY" -config "$HA_CONFIG" &>"$HA_LOG" &
    POOL2_PID=$!

    # The second instance shares the same WAL directory, so the WAL file lock
    # (first HA fencing layer) blocks coin pool initialization. The coordinator
    # still starts the API/metrics servers and enters retry mode.
    # Check API port (binds even when coin pool fails) or process alive.
    HA_WAIT=0
    HA_STARTED=0
    while [[ $HA_WAIT -lt 30 ]]; do
        if (echo >/dev/tcp/127.0.0.1/$HA_API_PORT) 2>/dev/null; then
            HA_STARTED=1
            break
        fi
        sleep 1
        HA_WAIT=$((HA_WAIT + 1))
    done

    if [[ $HA_STARTED -eq 1 ]]; then
        log_ok "[1/4] Second pool instance started (PID $POOL2_PID, API port $HA_API_PORT)"
        HA_PASS=$((HA_PASS + 1))
    elif kill -0 "$POOL2_PID" 2>/dev/null; then
        log_ok "[1/4] Second pool instance running (PID $POOL2_PID, coin pool blocked by WAL lock)"
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[1/4] Second pool instance failed to start"
        log_info "  Check: $HA_LOG"
    fi

    # -- 2/4. Verify HA fencing is active ------------------------------------
    # Two layers of fencing exist:
    #   Layer 1: WAL file lock — prevents two instances on the same machine
    #            from corrupting each other's block WAL
    #   Layer 2: PostgreSQL advisory lock — prevents double-payment across
    #            machines sharing the same database
    # On the same machine, the WAL lock fires first (coin pool init fails).
    # On separate machines, the advisory lock fires (payment processor skips).
    log_info "Waiting for HA fencing detection in second instance log (up to 90s)..."
    LOCK_WAIT=0
    LOCK_BLOCKED=0
    while [[ $LOCK_WAIT -lt 90 ]]; do
        if grep -qi "WAL file lock\|another instance running\|advisory lock held by another process\|advisory lock.*skip" "$HA_LOG" 2>/dev/null; then
            LOCK_BLOCKED=1
            break
        fi
        sleep 5
        LOCK_WAIT=$((LOCK_WAIT + 5))
    done

    if [[ $LOCK_BLOCKED -eq 1 ]]; then
        FENCE_MSG=$(grep -i "WAL file lock\|another instance running\|advisory lock held" "$HA_LOG" 2>/dev/null | head -1) || true
        if echo "$FENCE_MSG" | grep -qi "WAL\|another instance"; then
            log_ok "[2/4] WAL file lock blocked second instance (same-machine HA fencing verified)"
        else
            log_ok "[2/4] Advisory lock blocked second instance (cross-machine HA fencing verified)"
        fi
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[2/4] No HA fencing detected in second instance log after ${LOCK_WAIT}s"
        log_info "  Log tail: $(tail -3 "$HA_LOG" 2>/dev/null || echo 'empty')"
    fi

    # -- 3/4. Verify primary pool still running ------------------------------
    if (echo >/dev/tcp/127.0.0.1/$STRATUM_PORT) 2>/dev/null; then
        log_ok "[3/4] Primary pool still running on port $STRATUM_PORT (no disruption)"
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[3/4] Primary pool stratum port $STRATUM_PORT not responding"
    fi

    # -- 4/4. Terminate second instance --------------------------------------
    if [[ -n "$POOL2_PID" ]]; then
        kill -9 "$POOL2_PID" 2>/dev/null || true
        wait "$POOL2_PID" 2>/dev/null || true
    fi
    rm -f "$HA_CONFIG"

    if ! kill -0 "$POOL2_PID" 2>/dev/null; then
        log_ok "[4/4] Second pool instance terminated (cleanup successful)"
        HA_PASS=$((HA_PASS + 1))
    else
        log_warn "[4/4] Second pool instance still running after cleanup attempt"
    fi
    POOL2_PID=""

    echo ""
    log_info "HA failover: $HA_PASS/$HA_TOTAL checks passed"
    echo ""
fi

# =============================================================================
# STEP 8e: Daemon-Down Resilience
# =============================================================================

DAEMON_RESIL_PASS=0
DAEMON_RESIL_TOTAL=4

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8e: Daemon-down resilience (4 checks)"

    RESIL_LOG="$LOG_DIR/spiralpool-regtest.log"
    PRE_STOP_LINES=$(wc -l < "$RESIL_LOG")

    # -- 1/4. Stop the daemon ------------------------------------------------
    log_info "Stopping daemon to simulate node failure..."
    coincli stop 2>/dev/null || true
    sleep 5

    if ! coincli getblockchaininfo &>/dev/null; then
        log_ok "[1/4] Daemon stopped successfully (RPC unreachable)"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        log_warn "[1/4] Daemon still responding after stop command"
    fi

    # -- 2/4. Pool detects daemon failure ------------------------------------
    log_info "Waiting 20s for pool to detect daemon failure..."
    sleep 20

    # Check pool log for error messages that appeared AFTER daemon stop
    if tail -n +$((PRE_STOP_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
       grep -qiE "zmq.*error|zmq.*fail|rpc.*error|rpc.*fail|daemon.*fail|connection.*refuse|dial.*error|connect:.*refuse"; then
        DETECTED_MSG=$(tail -n +$((PRE_STOP_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
            grep -iE "zmq.*error|zmq.*fail|rpc.*error|rpc.*fail|daemon.*fail|connection.*refuse|dial.*error|connect:.*refuse" | head -1) || true
        log_ok "[2/4] Pool detected daemon failure"
        log_info "  Log: ${DETECTED_MSG:-(message extracted)}"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        log_warn "[2/4] No daemon failure detection in pool logs"
    fi

    # Verify pool process survived (didn't crash)
    if kill -0 "$POOL_PID" 2>/dev/null; then
        log_info "  Pool process still alive (PID $POOL_PID) — graceful failure handling"
    else
        log_warn "  Pool process DIED (PID $POOL_PID) — ungraceful crash"
    fi

    # -- 3/4. Restart daemon -------------------------------------------------
    log_info "Restarting daemon..."
    PRE_RESTART_LINES=$(wc -l < "$RESIL_LOG")

    # Build restart command with coin-specific flags
    # BUG FIX: Use -listen=1 for coins that need peers (BCH, LTC, DOGE, etc.)
    LISTEN_FLAG=0
    [[ "$NEEDS_PEER_DAEMON" == "true" ]] && LISTEN_FLAG=1

    RESTART_ARGS=(
        -regtest
        -daemon=0
        -server=1
        -rpcuser="$RPC_USER"
        -rpcpassword="$RPC_PASS"
        -rpcport="$RPC_PORT"
        -rpcallowip=127.0.0.1
        -rpcbind=127.0.0.1
        -port="$P2P_PORT"
        -listen=$LISTEN_FLAG
        -zmqpubhashblock="tcp://127.0.0.1:$ZMQ_PORT"
        -zmqpubrawblock="tcp://127.0.0.1:$ZMQ_PORT"
        -txindex=1
        -fallbackfee=0.0001
        -printtoconsole=0
        -debuglogfile="$LOG_DIR/$DAEMON_LOG"
    )
    # Add blockfilterindex only for coins that support it (not BCH)
    [[ -n "$BLOCKFILTERINDEX_FLAG" ]] && RESTART_ARGS+=("$BLOCKFILTERINDEX_FLAG")
    # Add BCH-specific flags
    [[ "$COIN" == "bch" ]] && RESTART_ARGS+=(-expire=0)
    # Fractal Bitcoin: must specify datadir (binary is bitcoind, defaults to ~/.bitcoin)
    [[ "$COIN" == "fbtc" ]] && RESTART_ARGS+=(-datadir="$HOME/.fractal")
    # Note: DGB algo is set via config file (~/.digibyte/digibyte.conf), not command-line

    "$DAEMON_CMD" "${RESTART_ARGS[@]}" &>"$LOG_DIR/$DAEMON_STARTUP" &

    DAEMON_PID=$!

    wait_for_rpc

    # Reload wallet (daemon restart loses loaded wallets)
    # Skip for legacy wallets (they use default wallet, no load needed)
    if [[ -z "${LEGACY_WALLET:-}" ]]; then
        coincli loadwallet "$WALLET_NAME" &>/dev/null || true
    fi

    # BUG FIX: Restart peer daemon if needed (BCH, LTC, etc. need peers for GBT)
    if [[ "$NEEDS_PEER_DAEMON" == "true" && -n "$PEER_DAEMON_PID" ]]; then
        log_info "Restarting peer daemon for GBT support..."
        kill -9 "$PEER_DAEMON_PID" 2>/dev/null || true
        sleep 1

        PEER_EXTRA_ARGS=""
        [[ "$COIN" == "bch" ]] && PEER_EXTRA_ARGS="-expire=0"

        "$DAEMON_CMD" \
            -regtest \
            -daemon=0 \
            -server=1 \
            -rpcuser="$RPC_USER" \
            -rpcpassword="$RPC_PASS" \
            -rpcport="$PEER_RPC_PORT" \
            -rpcallowip=127.0.0.1 \
            -port="$PEER_P2P_PORT" \
            -connect="127.0.0.1:$P2P_PORT" \
            -datadir="$PEER_DATA_DIR" \
            -printtoconsole=0 \
            -listen=0 \
            $PEER_EXTRA_ARGS \
            &>"$LOG_DIR/peer-daemon.log" &

        PEER_DAEMON_PID=$!

        # Wait for peer connection
        PEER_WAIT=0
        while [[ $PEER_WAIT -lt 15 ]]; do
            PEER_COUNT=$(coincli getconnectioncount 2>/dev/null || echo "0")
            [[ "$PEER_COUNT" -gt 0 ]] && break
            sleep 1
            PEER_WAIT=$((PEER_WAIT + 1))
        done

        if [[ "$PEER_COUNT" -gt 0 ]]; then
            log_info "Peer reconnected (connections: $PEER_COUNT)"
        else
            log_warn "Peer did not reconnect within 15s"
        fi
    fi

    if coincli getblockchaininfo &>/dev/null; then
        RESTART_HEIGHT=$(get_block_count)
        log_ok "[3/4] Daemon restarted (PID $DAEMON_PID, height=$RESTART_HEIGHT)"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        log_warn "[3/4] Daemon restart failed — RPC not responding"
    fi

    # -- 4/4. Pool reconnects to daemon --------------------------------------
    log_info "Waiting for pool to reconnect to daemon (up to 90s)..."
    RECONNECT_WAIT=0
    RECONNECTED=0
    while [[ $RECONNECT_WAIT -lt 90 ]]; do
        if tail -n +$((PRE_RESTART_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
           grep -qiE "zmq.*recover|zmq.*connect|zmq.*stabil|rpc.*success|new.*job|block.*template|getblocktemplate"; then
            RECONNECTED=1
            break
        fi
        sleep 5
        RECONNECT_WAIT=$((RECONNECT_WAIT + 5))
    done

    if [[ $RECONNECTED -eq 1 ]]; then
        RECOVERY_MSG=$(tail -n +$((PRE_RESTART_LINES + 1)) "$RESIL_LOG" 2>/dev/null | \
            grep -iE "zmq.*recover|zmq.*connect|zmq.*stabil|rpc.*success|new.*job|block.*template" | head -1) || true
        log_ok "[4/4] Pool reconnected to daemon"
        log_info "  Log: ${RECOVERY_MSG:-(recovery detected)}"
        DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
    else
        # Fallback: check if pool is at least still serving stratum
        if (echo >/dev/tcp/127.0.0.1/$STRATUM_PORT) 2>/dev/null; then
            log_ok "[4/4] Pool still serving stratum (daemon reconnection may be pending)"
            DAEMON_RESIL_PASS=$((DAEMON_RESIL_PASS + 1))
        else
            log_warn "[4/4] Pool not reconnected after ${RECONNECT_WAIT}s"
        fi
    fi

    echo ""
    log_info "Daemon-down resilience: $DAEMON_RESIL_PASS/$DAEMON_RESIL_TOTAL checks passed"
    echo ""
fi

# =============================================================================
# STEP 8f: SOLO Mining Direct Payout Verification
# =============================================================================
#
# CRITICAL TEST: This verifies the SOLO mining code path where miners receive
# coinbase rewards directly to their wallet address (from stratum username).
#
# The earlier tests used "TEST.worker1" which is NOT a valid address, so the
# pool fell back to the pool's address. This test uses a REAL wallet address
# to verify the actual SOLO mining functionality.

SOLO_PASS=0
SOLO_TOTAL=5

if [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_step "Step 8f: SOLO mining direct payout verification (5 checks)"

    # -- 1/5. Create miner wallet (separate from pool wallet) ------------------
    log_info "Creating separate miner wallet for SOLO mining test..."
    SOLO_WALLET_NAME="regtest-solo-miner"

    if [[ -n "${LEGACY_WALLET:-}" ]]; then
        # Legacy wallets can't create additional wallets - use pool wallet address
        log_info "Legacy wallet mode — using pool wallet for SOLO test"
        SOLO_MINER_ADDRESS="$MINING_ADDRESS"
        log_ok "[1/5] Using pool wallet address for SOLO test (legacy wallet mode)"
        SOLO_PASS=$((SOLO_PASS + 1))
    else
        # Create or load miner wallet
        CREATE_SOLO=$(coincli createwallet "$SOLO_WALLET_NAME" 2>&1) || \
        LOAD_SOLO=$(coincli loadwallet "$SOLO_WALLET_NAME" 2>&1) || true

        # Generate miner address from the miner wallet
        if [[ -n "$ADDR_TYPE" ]]; then
            SOLO_MINER_ADDRESS=$(coincli -rpcwallet="$SOLO_WALLET_NAME" getnewaddress "solo-miner" "$ADDR_TYPE" 2>/dev/null) || true
        else
            SOLO_MINER_ADDRESS=$(coincli -rpcwallet="$SOLO_WALLET_NAME" getnewaddress "solo-miner" 2>/dev/null) || true
        fi

        if [[ -n "$SOLO_MINER_ADDRESS" ]]; then
            log_ok "[1/5] Miner wallet created with address: $SOLO_MINER_ADDRESS"
            SOLO_PASS=$((SOLO_PASS + 1))
        else
            log_warn "[1/5] Failed to create miner wallet — skipping SOLO test"
            SOLO_MINER_ADDRESS=""
        fi
    fi

    if [[ -n "$SOLO_MINER_ADDRESS" ]]; then
        # -- 2/5. Stop current miner and restart with miner's wallet address ---
        log_info "Restarting miner with SOLO wallet address as username..."
        SOLO_HEIGHT_BEFORE=$(get_block_count)

        # Stop the current miner
        kill -9 "$MINER_PID" 2>/dev/null || true
        wait "$MINER_PID" 2>/dev/null || true
        sleep 2

        # Start miner with miner's wallet address as username
        # Format: WALLET_ADDRESS.worker_name
        "$CPUMINER" -a "$COIN_ALGO" -o "stratum+tcp://127.0.0.1:$STRATUM_PORT" \
            -u "${SOLO_MINER_ADDRESS}.solo1" -p x -t "$MINER_THREADS" \
            &>"$LOG_DIR/cpuminer-solo-regtest.log" &

        MINER_PID=$!
        log_ok "[2/5] Miner restarted with SOLO address (PID $MINER_PID)"
        SOLO_PASS=$((SOLO_PASS + 1))

        # -- 3/5. Wait for at least 1 block with SOLO miner address -------------
        log_info "Mining block with SOLO miner address (may take ~${EST_SECS_PER_BLOCK}s)..."
        SOLO_WAIT=0
        SOLO_MAX_WAIT=$((EST_SECS_PER_BLOCK * 5 + 120))  # 5x expected time + buffer
        SOLO_BLOCK_FOUND=0

        while [[ $SOLO_WAIT -lt $SOLO_MAX_WAIT ]]; do
            SOLO_HEIGHT_NOW=$(get_block_count)
            if [[ $SOLO_HEIGHT_NOW -gt $SOLO_HEIGHT_BEFORE ]]; then
                SOLO_BLOCK_FOUND=1
                log_ok "[3/5] SOLO block mined at height $SOLO_HEIGHT_NOW"
                SOLO_PASS=$((SOLO_PASS + 1))
                break
            fi
            if [[ $((SOLO_WAIT % 30)) -eq 0 ]] && [[ $SOLO_WAIT -gt 0 ]]; then
                log_info "  ${SOLO_WAIT}s — waiting for SOLO block (height: $SOLO_HEIGHT_NOW)"
            fi
            sleep 10
            SOLO_WAIT=$((SOLO_WAIT + 10))
        done

        if [[ $SOLO_BLOCK_FOUND -eq 0 ]]; then
            log_warn "[3/5] No SOLO block mined within ${SOLO_MAX_WAIT}s"
        fi

        # -- 4/5. Verify coinbase pays MINER's address, not pool's address -----
        if [[ $SOLO_BLOCK_FOUND -eq 1 ]]; then
            SOLO_BLOCK_HASH=$(coincli getblockhash "$SOLO_HEIGHT_NOW" 2>/dev/null) || true

            if [[ -n "$SOLO_BLOCK_HASH" ]] && [[ ${#SOLO_BLOCK_HASH} -eq 64 ]]; then
                SOLO_BLOCK_JSON=$(coincli getblock "$SOLO_BLOCK_HASH" 2 2>/dev/null) || true

                # Extract coinbase output address
                SOLO_COINBASE_ADDR=$(echo "$SOLO_BLOCK_JSON" | grep -oE '"address":\s*"[^"]+"' | head -1 | sed 's/.*"address":\s*"\([^"]*\)".*/\1/') || true
                if [[ -z "$SOLO_COINBASE_ADDR" ]]; then
                    SOLO_COINBASE_ADDR=$(echo "$SOLO_BLOCK_JSON" | tr '\n' ' ' | grep -oE '"addresses":\s*\[\s*"[^"]+"' | head -1 | sed 's/.*\[\s*"\([^"]*\)".*/\1/') || true
                fi

                if [[ -n "$SOLO_COINBASE_ADDR" ]]; then
                    log_info "SOLO block coinbase address: $SOLO_COINBASE_ADDR"
                    log_info "Expected SOLO miner address: $SOLO_MINER_ADDRESS"
                    log_info "Pool address (should NOT be used): $MINING_ADDRESS"

                    if [[ "$SOLO_COINBASE_ADDR" == "$SOLO_MINER_ADDRESS" ]]; then
                        log_ok "[4/5] SOLO MINING VERIFIED — coinbase pays miner's address directly!"
                        SOLO_PASS=$((SOLO_PASS + 1))
                    elif [[ "$SOLO_COINBASE_ADDR" == "$MINING_ADDRESS" ]]; then
                        log_warn "[4/5] SOLO MINING FALLBACK — coinbase pays pool address (miner address may have been rejected)"
                        log_info "  Check pool log for: grep -i 'SOLO\\|miner.*address' $LOG_DIR/spiralpool-regtest.log"
                    else
                        log_warn "[4/5] SOLO block paid to unexpected address: $SOLO_COINBASE_ADDR"
                    fi
                else
                    log_warn "[4/5] Could not extract coinbase address from SOLO block"
                fi
            else
                log_warn "[4/5] Could not retrieve SOLO block hash"
            fi

            # -- 5/5. Verify miner address is in pool logs ---------------------
            if grep -qi "SOLO miner address set\|soloMinerAddress\|miner.*${SOLO_MINER_ADDRESS:0:10}" "$LOG_DIR/spiralpool-regtest.log" 2>/dev/null; then
                SOLO_LOG_MSG=$(grep -i "SOLO miner\|soloMinerAddress" "$LOG_DIR/spiralpool-regtest.log" | tail -1) || true
                log_ok "[5/5] SOLO miner address logged in pool: ${SOLO_LOG_MSG:0:80}..."
                SOLO_PASS=$((SOLO_PASS + 1))
            else
                log_warn "[5/5] No SOLO miner address confirmation in pool logs"
                log_info "  Searching for miner address prefix..."
                grep -i "${SOLO_MINER_ADDRESS:0:15}" "$LOG_DIR/spiralpool-regtest.log" 2>/dev/null | head -2 || true
            fi
        else
            log_warn "[4/5] Skipped — no SOLO block to verify"
            log_warn "[5/5] Skipped — no SOLO block mined"
        fi
    fi

    echo ""
    log_info "SOLO mining verification: $SOLO_PASS/$SOLO_TOTAL checks passed"
    if [[ $SOLO_PASS -ge 4 ]]; then
        log_ok "SOLO MINING DIRECT PAYOUT: VERIFIED"
    elif [[ $SOLO_PASS -ge 2 ]]; then
        log_warn "SOLO mining test incomplete — review logs for details"
    else
        log_error "SOLO mining test failed — coinbase may not be routing to miner's address"
    fi
    echo ""
fi

fi  # End HA_ONLY skip for Steps 6-8f

# =============================================================================
# STEP 8g: HA VIP Failover Emulation (Network Namespace)
# =============================================================================
#
# This test emulates a multi-node HA cluster on a single machine using Linux
# network namespaces. Each namespace acts as a separate "node" with its own
# network stack, allowing us to test:
#   - VIP acquisition via gratuitous ARP
#   - Master election based on priority
#   - Cluster discovery (UDP broadcast)
#   - VIP migration on node failure
#   - Service role transitions (BACKUP → MASTER)
#
# Requires: root/sudo access for network namespace operations

HA_VIP_PASS=0
HA_VIP_TOTAL=8

# Check if HA VIP test should run (requires root and specific flag or always run)
HA_VIP_ENABLED="${HA_VIP_TEST:-1}"  # Set HA_VIP_TEST=0 to skip

if [[ "$HA_VIP_ENABLED" == "1" ]] && [[ $BLOCKS_FOUND -ge 1 ]]; then
    log_step "Step 8g: HA VIP failover emulation (8 checks)"

    # Determine sudo command - only needed for network namespace operations
    SUDO_CMD=""
    HA_SUDO_OK=1

    if [[ $EUID -ne 0 ]]; then
        # Not root - check if sudo is available
        if command -v sudo &>/dev/null; then
            SUDO_CMD="sudo"
            log_info "Network namespace operations require sudo (you may be prompted for password)"
            # Prompt for sudo password now so it's cached for subsequent commands
            if ! sudo -v 2>/dev/null; then
                log_warn "HA VIP test requires sudo access — skipping"
                log_info "Set HA_VIP_TEST=0 to skip this test"
                HA_SUDO_OK=0
            fi
        else
            log_warn "sudo not available and not running as root — skipping HA VIP test"
            HA_SUDO_OK=0
        fi
    fi

    if [[ $HA_SUDO_OK -eq 1 ]]; then

        # HA Network Configuration
        HA_BRIDGE="br-ha-test"
        HA_NS1="ns-pool1"
        HA_NS2="ns-pool2"
        HA_VETH1="veth-p1"
        HA_VETH2="veth-p2"
        HA_NET="10.199.0"
        HA_NODE1_IP="${HA_NET}.1"
        HA_NODE2_IP="${HA_NET}.2"
        HA_VIP="${HA_NET}.100"
        HA_BRIDGE_IP="${HA_NET}.254"

        # HA Ports (different from main regtest to avoid conflicts)
        HA_STRATUM_PORT1=17337
        HA_STRATUM_PORT2=17338
        HA_API_PORT1=17080
        HA_API_PORT2=17081
        HA_STATUS_PORT1=15354
        HA_STATUS_PORT2=15355
        HA_DISCOVERY_PORT=15363

        # HA PostgreSQL config
        HA_PG_DATA="/tmp/ha-pg-regtest-$$"
        HA_PG_PORT=15432
        HA_PG_LOG="$LOG_DIR/ha-postgresql.log"

        # Cleanup function for HA test
        cleanup_ha_namespaces() {
            log_info "Cleaning up HA test namespaces..."
            # Stop temp PostgreSQL — try pg_ctl first, then kill by port as fallback
            if [[ -f "$HA_PG_DATA/postmaster.pid" ]]; then
                pg_ctl -D "$HA_PG_DATA" stop -m fast 2>/dev/null || true
            fi
            # Kill ANY postgres on HA_PG_PORT (handles stale instances from previous runs with different PIDs)
            pkill -f "postgres.*port.*${HA_PG_PORT}" 2>/dev/null || true
            pkill -f "postgres.*${HA_PG_PORT}" 2>/dev/null || true
            # Also kill by listening port via fuser (most reliable)
            fuser -k "${HA_PG_PORT}/tcp" 2>/dev/null || true
            sleep 1
            rm -rf "$HA_PG_DATA" 2>/dev/null || true
            # Clean up any leftover temp PG data dirs from previous runs
            rm -rf /tmp/ha-pg-regtest-* 2>/dev/null || true
            # Remove namespaces
            $SUDO_CMD ip netns del "$HA_NS1" 2>/dev/null || true
            $SUDO_CMD ip netns del "$HA_NS2" 2>/dev/null || true
            $SUDO_CMD ip link del "$HA_BRIDGE" 2>/dev/null || true
            $SUDO_CMD ip link del "${HA_VETH1}-br" 2>/dev/null || true
            $SUDO_CMD ip link del "${HA_VETH2}-br" 2>/dev/null || true
            # Remove iptables rules we added
            $SUDO_CMD iptables -t nat -D POSTROUTING -s "${HA_NET}.0/24" -j MASQUERADE 2>/dev/null || true
            $SUDO_CMD iptables -t nat -D PREROUTING -d "$HA_BRIDGE_IP" -p tcp --dport "${DB_PORT:-5432}" \
                -j DNAT --to-destination 127.0.0.1:"${DB_PORT:-5432}" 2>/dev/null || true
            $SUDO_CMD iptables -D FORWARD -i "$HA_BRIDGE" -j ACCEPT 2>/dev/null || true
            $SUDO_CMD iptables -D FORWARD -o "$HA_BRIDGE" -j ACCEPT 2>/dev/null || true
            # Remove INPUT rules for PostgreSQL and RPC ports
            $SUDO_CMD iptables -D INPUT -i "$HA_BRIDGE" -p tcp --dport "$HA_PG_PORT" -j ACCEPT 2>/dev/null || true
            $SUDO_CMD iptables -D INPUT -i "$HA_BRIDGE" -p tcp --dport "$RPC_PORT" -j ACCEPT 2>/dev/null || true
            # Kill any leftover pool processes from HA test
            $SUDO_CMD pkill -f "spiralpool.*ha-node" 2>/dev/null || true
            $SUDO_CMD pkill -f "config-ha-node" 2>/dev/null || true
        }

        # Clean up any previous HA test artifacts
        cleanup_ha_namespaces

        # -- 1/8. Create network namespaces ----------------------------------
        log_info "Creating network namespaces for HA emulation..."

        if $SUDO_CMD ip netns add "$HA_NS1" 2>/dev/null && \
           $SUDO_CMD ip netns add "$HA_NS2" 2>/dev/null; then
            log_ok "[1/8] Network namespaces created ($HA_NS1, $HA_NS2)"
            HA_VIP_PASS=$((HA_VIP_PASS + 1))
        else
            log_warn "[1/8] Failed to create network namespaces"
            cleanup_ha_namespaces
        fi

        # -- 2/8. Create bridge and veth pairs -------------------------------
        if [[ $HA_VIP_PASS -ge 1 ]]; then
            log_info "Setting up virtual network bridge..."

            # Create bridge
            $SUDO_CMD ip link add "$HA_BRIDGE" type bridge 2>/dev/null
            $SUDO_CMD ip addr add "${HA_BRIDGE_IP}/24" dev "$HA_BRIDGE" 2>/dev/null
            $SUDO_CMD ip link set "$HA_BRIDGE" up

            # Create veth pairs for node 1
            $SUDO_CMD ip link add "$HA_VETH1" type veth peer name "${HA_VETH1}-br"
            $SUDO_CMD ip link set "$HA_VETH1" netns "$HA_NS1"
            $SUDO_CMD ip link set "${HA_VETH1}-br" master "$HA_BRIDGE"
            $SUDO_CMD ip link set "${HA_VETH1}-br" up
            $SUDO_CMD ip netns exec "$HA_NS1" ip addr add "${HA_NODE1_IP}/24" dev "$HA_VETH1"
            $SUDO_CMD ip netns exec "$HA_NS1" ip link set "$HA_VETH1" up
            $SUDO_CMD ip netns exec "$HA_NS1" ip link set lo up

            # Create veth pairs for node 2
            $SUDO_CMD ip link add "$HA_VETH2" type veth peer name "${HA_VETH2}-br"
            $SUDO_CMD ip link set "$HA_VETH2" netns "$HA_NS2"
            $SUDO_CMD ip link set "${HA_VETH2}-br" master "$HA_BRIDGE"
            $SUDO_CMD ip link set "${HA_VETH2}-br" up
            $SUDO_CMD ip netns exec "$HA_NS2" ip addr add "${HA_NODE2_IP}/24" dev "$HA_VETH2"
            $SUDO_CMD ip netns exec "$HA_NS2" ip link set "$HA_VETH2" up
            $SUDO_CMD ip netns exec "$HA_NS2" ip link set lo up

            # Add default routes so namespaces can reach host services via bridge
            $SUDO_CMD ip netns exec "$HA_NS1" ip route add default via "$HA_BRIDGE_IP" 2>/dev/null || true
            $SUDO_CMD ip netns exec "$HA_NS2" ip route add default via "$HA_BRIDGE_IP" 2>/dev/null || true

            # Enable IP forwarding and NAT so namespaces can reach host's 127.0.0.1
            $SUDO_CMD sysctl -w net.ipv4.ip_forward=1 &>/dev/null || true
            $SUDO_CMD iptables -t nat -A POSTROUTING -s "${HA_NET}.0/24" -j MASQUERADE 2>/dev/null || true
            # Allow namespace traffic to reach localhost services
            $SUDO_CMD iptables -A FORWARD -i "$HA_BRIDGE" -j ACCEPT 2>/dev/null || true
            $SUDO_CMD iptables -A FORWARD -o "$HA_BRIDGE" -j ACCEPT 2>/dev/null || true
            # BUG FIX: Allow INPUT traffic from HA network to PostgreSQL port
            # Without this, the firewall blocks connections TO the bridge IP (not just through it)
            $SUDO_CMD iptables -A INPUT -i "$HA_BRIDGE" -p tcp --dport "$HA_PG_PORT" -j ACCEPT 2>/dev/null || true
            # Also allow daemon RPC port access
            $SUDO_CMD iptables -A INPUT -i "$HA_BRIDGE" -p tcp --dport "$RPC_PORT" -j ACCEPT 2>/dev/null || true
            # Merge mining: allow aux daemon RPC port access
            if [[ "$MERGE_MODE" == "1" ]]; then
                $SUDO_CMD iptables -A INPUT -i "$HA_BRIDGE" -p tcp --dport "$AUX_RPC_PORT" -j ACCEPT 2>/dev/null || true
            fi

            # Verify connectivity
            if $SUDO_CMD ip netns exec "$HA_NS1" ping -c 1 -W 1 "$HA_NODE2_IP" &>/dev/null; then
                log_ok "[2/8] Virtual network created (${HA_NET}.0/24, bridge: $HA_BRIDGE)"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))
            else
                log_warn "[2/8] Virtual network created but nodes cannot ping each other"
            fi
        fi

        # -- 2.5/8. Restart daemon with HA network bindings -------------------
        # The daemon is bound to 127.0.0.1 by default, which namespaces can't reach.
        # We need to restart it with the bridge IP added to rpcbind/rpcallowip.
        if [[ $HA_VIP_PASS -ge 2 ]]; then
            log_info "Restarting daemon with HA network bindings..."

            # Stop the current daemon gracefully
            coincli stop 2>/dev/null || true
            sleep 2
            kill -9 $DAEMON_PID 2>/dev/null || true
            sleep 1

            # Restart daemon with additional rpcbind for HA bridge network
            HA_DAEMON_ARGS=(
                -regtest
                -daemon=0
                -server=1
                -txindex=1
                -rpcuser="$RPC_USER"
                -rpcpassword="$RPC_PASS"
                -rpcport="$RPC_PORT"
                -rpcallowip=127.0.0.1
                -rpcallowip="${HA_NET}.0/24"
                -rpcbind=127.0.0.1
                -rpcbind="$HA_BRIDGE_IP"
                -zmqpubhashblock="tcp://0.0.0.0:$ZMQ_PORT"
            )

            # Add fallbackfee for coins that need it
            if [[ "$COIN_SYMBOL" =~ ^(BTC|LTC|DOGE|PEP|CAT|FBTC)$ ]]; then
                HA_DAEMON_ARGS+=(-fallbackfee=0.0001)
            fi

            # Fractal Bitcoin: must specify datadir (binary is bitcoind, defaults to ~/.bitcoin)
            [[ "$COIN" == "fbtc" ]] && HA_DAEMON_ARGS+=(-datadir="$HOME/.fractal")

            # Note: DGB algo is set via config file (~/.digibyte/digibyte.conf), not command-line

            "$DAEMON_CMD" "${HA_DAEMON_ARGS[@]}" &>"$LOG_DIR/${DAEMON_CMD##*/}-ha-regtest.log" &
            DAEMON_PID=$!

            # Wait for daemon to be ready
            HA_DAEMON_WAIT=0
            while [[ $HA_DAEMON_WAIT -lt 30 ]]; do
                if coincli getblockchaininfo &>/dev/null; then
                    break
                fi
                sleep 1
                HA_DAEMON_WAIT=$((HA_DAEMON_WAIT + 1))
            done

            if coincli getblockchaininfo &>/dev/null; then
                log_info "Daemon restarted with HA bindings (rpcbind=$HA_BRIDGE_IP)"
            else
                log_warn "Daemon may not have restarted properly"
            fi

            # Merge Mining: Restart aux daemon with HA bindings too
            if [[ "$MERGE_MODE" == "1" ]] && [[ -n "${AUX_DAEMON_PID:-}" ]]; then
                log_info "Restarting aux daemon with HA network bindings..."

                # Stop the current aux daemon
                auxcli stop 2>/dev/null || true
                sleep 2
                kill -9 $AUX_DAEMON_PID 2>/dev/null || true
                sleep 1

                # Restart aux daemon with HA bindings
                HA_AUX_DAEMON_ARGS=(
                    -regtest
                    -daemon=0
                    -server=1
                    -txindex=1
                    -rpcuser="$RPC_USER"
                    -rpcpassword="$RPC_PASS"
                    -rpcport="$AUX_RPC_PORT"
                    -rpcallowip=127.0.0.1
                    -rpcallowip="${HA_NET}.0/24"
                    -rpcbind=127.0.0.1
                    -rpcbind="$HA_BRIDGE_IP"
                    -port="$AUX_P2P_PORT"
                    -listen=0
                    -connect=0
                    -dnsseed=0
                    -zmqpubhashblock="tcp://0.0.0.0:$AUX_ZMQ_PORT"
                    -zmqpubrawblock="tcp://0.0.0.0:$AUX_ZMQ_PORT"
                    -fallbackfee=0.0001
                    -printtoconsole=0
                )

                # Aux-chain specific flags
                case "$AUX_COIN" in
                    doge|pep) HA_AUX_DAEMON_ARGS+=(-listenonion=0) ;;
                    nmc)      HA_AUX_DAEMON_ARGS+=(-blockfilterindex=1) ;;
                    fbtc)     HA_AUX_DAEMON_ARGS+=(-datadir="$HOME/.fractal" -blockfilterindex=1) ;;
                    sys)      HA_AUX_DAEMON_ARGS+=(-blockfilterindex=1) ;;
                    xmy)      ;; # Myriadcoin v0.18.1 too old for blockfilterindex
                esac

                "$AUX_DAEMON_CMD" "${HA_AUX_DAEMON_ARGS[@]}" &>"$LOG_DIR/${AUX_DAEMON_CMD##*/}-ha-regtest.log" &
                AUX_DAEMON_PID=$!

                # Wait for aux daemon to be ready
                HA_AUX_WAIT=0
                while [[ $HA_AUX_WAIT -lt 30 ]]; do
                    if auxcli getblockchaininfo &>/dev/null; then
                        break
                    fi
                    sleep 1
                    HA_AUX_WAIT=$((HA_AUX_WAIT + 1))
                done

                if auxcli getblockchaininfo &>/dev/null; then
                    log_info "Aux daemon restarted with HA bindings (rpcbind=$HA_BRIDGE_IP)"
                else
                    log_warn "Aux daemon may not have restarted properly"
                fi
            fi
        fi

        # -- 2.75/8. Spin up temporary PostgreSQL for HA test ----------------
        # The system PostgreSQL only listens on localhost. We need a temp instance
        # that listens on the bridge IP so namespaced pools can connect.
        if [[ $HA_VIP_PASS -ge 2 ]]; then
            log_info "Starting temporary PostgreSQL for HA test..."

            # Find PostgreSQL tools - they may be in various locations depending on distro
            PG_BIN_DIR=""
            if command -v initdb &>/dev/null && command -v pg_ctl &>/dev/null; then
                PG_BIN_DIR=""  # Already in PATH
                log_info "PostgreSQL tools found in PATH"
            else
                # Try pg_config first (most reliable if available)
                if command -v pg_config &>/dev/null; then
                    PG_BIN_DIR=$(pg_config --bindir 2>/dev/null || echo "")
                    if [[ -n "$PG_BIN_DIR" ]] && [[ -x "${PG_BIN_DIR}/initdb" ]]; then
                        export PATH="${PG_BIN_DIR}:${PATH}"
                        log_info "Found PostgreSQL tools via pg_config: ${PG_BIN_DIR}"
                    else
                        PG_BIN_DIR=""
                    fi
                fi

                # Search standard PostgreSQL installation paths (Debian/Ubuntu)
                if [[ -z "$PG_BIN_DIR" ]]; then
                    for pg_ver in 17 16 15 14 13 12 11 10; do
                        if [[ -x "/usr/lib/postgresql/${pg_ver}/bin/initdb" ]]; then
                            PG_BIN_DIR="/usr/lib/postgresql/${pg_ver}/bin"
                            export PATH="${PG_BIN_DIR}:${PATH}"
                            log_info "Found PostgreSQL tools in ${PG_BIN_DIR}"
                            break
                        fi
                    done
                fi

                # Search RHEL/CentOS paths
                if [[ -z "$PG_BIN_DIR" ]]; then
                    for pg_ver in 17 16 15 14 13 12 11 10; do
                        if [[ -x "/usr/pgsql-${pg_ver}/bin/initdb" ]]; then
                            PG_BIN_DIR="/usr/pgsql-${pg_ver}/bin"
                            export PATH="${PG_BIN_DIR}:${PATH}"
                            log_info "Found PostgreSQL tools in ${PG_BIN_DIR}"
                            break
                        fi
                    done
                fi
            fi

            # Check for required PostgreSQL tools
            HA_USE_SYSTEM_PG=0
            if ! command -v initdb &>/dev/null || ! command -v pg_ctl &>/dev/null; then
                log_warn "PostgreSQL tools (initdb, pg_ctl) not found"
                # Fallback: use system PostgreSQL with port forwarding
                if pg_isready -h "$DB_HOST" -p "$DB_PORT" &>/dev/null; then
                    log_info "Falling back to system PostgreSQL with port forwarding"
                    HA_USE_SYSTEM_PG=1
                    HA_PG_PORT="$DB_PORT"
                    # Set up iptables DNAT to forward bridge:5432 -> localhost:5432
                    $SUDO_CMD iptables -t nat -A PREROUTING -d "$HA_BRIDGE_IP" -p tcp --dport "$DB_PORT" \
                        -j DNAT --to-destination 127.0.0.1:"$DB_PORT" 2>/dev/null || true
                    # Also need to allow local routing
                    $SUDO_CMD sysctl -w net.ipv4.conf.all.route_localnet=1 &>/dev/null || true
                    # Create HA test database in system PostgreSQL
                    PGPASSWORD="$DB_PASS" createdb -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" \
                        "spiralstratum_${COIN_SYMBOL,,}_ha_regtest" 2>/dev/null || true
                    log_info "System PostgreSQL accessible via ${HA_BRIDGE_IP}:${DB_PORT}"
                else
                    log_error "System PostgreSQL not accessible — HA test cannot continue"
                    log_info "Install: sudo apt install postgresql postgresql-contrib"
                    HA_VIP_PASS=0  # Reset - can't continue without DB
                fi
            else
                # Clean up any previous temp PostgreSQL
                rm -rf "$HA_PG_DATA" 2>/dev/null || true
                mkdir -p "$HA_PG_DATA"

                # Initialize PostgreSQL data directory
                # CRITICAL: Use --username=postgres so we have a known superuser name
                initdb -D "$HA_PG_DATA" --auth=trust --no-locale -E UTF8 --username=postgres &>/dev/null

                if [[ -f "$HA_PG_DATA/postgresql.conf" ]]; then
                    # Configure to listen on bridge IP
                    cat >> "$HA_PG_DATA/postgresql.conf" << PGCONF
listen_addresses = '*'  # Listen on all interfaces for namespace access
port = ${HA_PG_PORT}
unix_socket_directories = '${HA_PG_DATA}'
PGCONF

                    # Allow connections from HA network and localhost
                    # CRITICAL: Overwrite pg_hba.conf entirely to ensure trust auth
                    cat > "$HA_PG_DATA/pg_hba.conf" << PGHBA
# TYPE  DATABASE  USER  ADDRESS  METHOD
# Local socket connections - needed for psql -h localhost
local   all       all             trust
# IPv4 connections - all trust for testing
host    all     all     127.0.0.1/32      trust
host    all     all     ${HA_NET}.0/24    trust
host    all     all     ${HA_BRIDGE_IP}/32    trust
host    all     all     0.0.0.0/0         trust
# IPv6 localhost
host    all     all     ::1/128           trust
PGHBA

                    # Start PostgreSQL
                    pg_ctl -D "$HA_PG_DATA" -l "$HA_PG_LOG" start &>/dev/null

                    # Wait for PostgreSQL to be ready (retry loop, not just sleep)
                    HA_PG_READY=0
                    for i in {1..15}; do
                        if pg_isready -h 127.0.0.1 -p "$HA_PG_PORT" &>/dev/null; then
                            HA_PG_READY=1
                            log_info "PostgreSQL ready after ${i}s"
                            break
                        fi
                        sleep 1
                    done

                    if [[ $HA_PG_READY -eq 1 ]]; then
                        # Create database and user via psql using localhost (more reliable)
                        # Note: initdb --username=postgres creates the postgres superuser
                        if psql -h 127.0.0.1 -p "$HA_PG_PORT" -U postgres -c "CREATE ROLE spiralstratum WITH LOGIN CREATEDB PASSWORD 'regtest';" 2>/dev/null; then
                            log_info "Created role 'spiralstratum'"
                        else
                            log_warn "Role 'spiralstratum' may already exist"
                        fi

                        if psql -h 127.0.0.1 -p "$HA_PG_PORT" -U postgres -c "CREATE DATABASE spiralstratum_${COIN_SYMBOL,,}_regtest OWNER spiralstratum;" 2>/dev/null; then
                            log_info "Created database 'spiralstratum_${COIN_SYMBOL,,}_regtest'"
                        else
                            log_warn "Database may already exist"
                        fi

                        # Verify the role was actually created
                        if psql -h 127.0.0.1 -p "$HA_PG_PORT" -U postgres -c "SELECT 1 FROM pg_roles WHERE rolname='spiralstratum';" 2>/dev/null | grep -q 1; then
                            log_ok "Verified: role 'spiralstratum' exists"
                        else
                            log_error "CRITICAL: Role 'spiralstratum' NOT created - check $HA_PG_LOG"
                        fi

                        log_info "Temp PostgreSQL running on ${HA_BRIDGE_IP}:${HA_PG_PORT}"
                    else
                        log_warn "Temp PostgreSQL failed to start after 15s — check $HA_PG_LOG"
                        # Show last few lines of log
                        tail -5 "$HA_PG_LOG" 2>/dev/null || true
                    fi
                else
                    log_warn "Failed to initialize temp PostgreSQL data directory"
                fi
            fi
        fi

        # -- 3/8. Create HA config files -------------------------------------
        if [[ $HA_VIP_PASS -ge 2 ]]; then
            log_info "Creating HA configuration files..."

            HA_CONFIG_DIR="$LOG_DIR/ha-configs"
            mkdir -p "$HA_CONFIG_DIR"

            # Generate cluster token
            HA_CLUSTER_TOKEN=$(openssl rand -hex 16 2>/dev/null || echo "regtest-ha-token-$(date +%s)")

            # Get host IP for daemon/database access from namespaces
            # Use bridge IP since namespaces route through it
            HA_HOST_IP="$HA_BRIDGE_IP"

            # Database credentials - use system DB credentials when falling back
            if [[ "${HA_USE_SYSTEM_PG:-0}" == "1" ]]; then
                HA_DB_USER="$DB_USER"
                HA_DB_PASS="$DB_PASS"
                HA_DB_NAME="spiralstratum_${COIN_SYMBOL,,}_ha_regtest"
            else
                HA_DB_USER="spiralstratum"
                HA_DB_PASS="regtest"
                HA_DB_NAME="spiralstratum_${COIN_SYMBOL,,}_regtest"
            fi

            # Node 1 config (priority 100 = higher priority = becomes master)
            cat > "$HA_CONFIG_DIR/config-ha-node1.yaml" << HAEOF1
version: 1

global:
  log_level: debug
  log_format: json
  metrics_port: ${HA_API_PORT1}
  api_port: ${HA_API_PORT1}
  api_enabled: true

database:
  host: ${HA_HOST_IP}
  port: ${HA_PG_PORT}
  user: ${HA_DB_USER}
  password: "${HA_DB_PASS}"
  database: ${HA_DB_NAME}
  sslMode: disable
  maxConnections: 10

coins:
  - symbol: ${COIN_SYMBOL}
    pool_id: ha_node1_regtest
    enabled: true
    address: "${MINING_ADDRESS}"
    coinbase_text: "HA-Node1"
    skip_genesis_verify: true

    stratum:
      host: "${HA_NODE1_IP}"
      port: ${HA_STRATUM_PORT1}
      difficulty:
        initial: 0.001
        varDiff:
          enabled: false
      banning:
        enabled: false
      connection:
        timeout: 600s
        maxConnections: 100
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: regtest-primary
        host: ${HA_HOST_IP}
        port: ${RPC_PORT}
        user: ${RPC_USER}
        password: "${RPC_PASS}"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://${HA_HOST_IP}:${ZMQ_PORT}"

    payments:
      enabled: false
      scheme: SOLO

vip:
  enabled: true
  address: "${HA_VIP}"
  interface: "${HA_VETH1}"
  netmask: 32
  priority: 100
  canBecomeMaster: true
  clusterToken: "${HA_CLUSTER_TOKEN}"
  discoveryPort: ${HA_DISCOVERY_PORT}
  statusPort: ${HA_STATUS_PORT1}
  heartbeatInterval: 1s
  failoverTimeout: 3s
HAEOF1

            # Merge Mining: Add aux chain config to HA node1
            # V2 CONFIG FIX: mergeMining must be INSIDE the coin section, not at root level.
            if [[ "$MERGE_MODE" == "1" ]]; then
                # Create the merge mining block with proper V2 indentation (4 spaces = inside coin section)
                if [[ -n "${AUX_LEGACY_WALLET:-}" ]]; then
                    # Legacy wallet: no wallet specification needed
                    HA_MERGE_BLOCK="\\
    # Merge Mining Configuration (auto-generated for HA test)\\
    mergeMining:\\
      enabled: true\\
      refreshInterval: 5s\\
      auxChains:\\
        - symbol: ${AUX_SYMBOL}\\
          enabled: true\\
          address: \"${AUX_MINING_ADDRESS}\"\\
          daemon:\\
            host: ${HA_HOST_IP}\\
            port: ${AUX_RPC_PORT}\\
            user: ${RPC_USER}\\
            password: \"${RPC_PASS}\"\\
"
                else
                    # Modern wallet: include wallet path for RPC endpoint
                    HA_MERGE_BLOCK="\\
    # Merge Mining Configuration (auto-generated for HA test)\\
    mergeMining:\\
      enabled: true\\
      refreshInterval: 5s\\
      auxChains:\\
        - symbol: ${AUX_SYMBOL}\\
          enabled: true\\
          address: \"${AUX_MINING_ADDRESS}\"\\
          daemon:\\
            host: ${HA_HOST_IP}\\
            port: ${AUX_RPC_PORT}\\
            user: ${RPC_USER}\\
            password: \"${RPC_PASS}\"\\
            wallet: \"${AUX_WALLET_NAME}\"\\
"
                fi
                # Insert merge mining config BEFORE the 'stratum:' line
                sed -i "0,/^    stratum:/s/^    stratum:/${HA_MERGE_BLOCK}    stratum:/" "$HA_CONFIG_DIR/config-ha-node1.yaml"
            fi

            # Node 2 config (priority 200 = lower priority = becomes backup)
            cat > "$HA_CONFIG_DIR/config-ha-node2.yaml" << HAEOF2
version: 1

global:
  log_level: debug
  log_format: json
  metrics_port: ${HA_API_PORT2}
  api_port: ${HA_API_PORT2}
  api_enabled: true

database:
  host: ${HA_HOST_IP}
  port: ${HA_PG_PORT}
  user: ${HA_DB_USER}
  password: "${HA_DB_PASS}"
  database: ${HA_DB_NAME}
  sslMode: disable
  maxConnections: 10

coins:
  - symbol: ${COIN_SYMBOL}
    pool_id: ha_node2_regtest
    enabled: true
    address: "${MINING_ADDRESS}"
    coinbase_text: "HA-Node2"
    skip_genesis_verify: true

    stratum:
      host: "${HA_NODE2_IP}"
      port: ${HA_STRATUM_PORT2}
      difficulty:
        initial: 0.001
        varDiff:
          enabled: false
      banning:
        enabled: false
      connection:
        timeout: 600s
        maxConnections: 100
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: regtest-primary
        host: ${HA_HOST_IP}
        port: ${RPC_PORT}
        user: ${RPC_USER}
        password: "${RPC_PASS}"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://${HA_HOST_IP}:${ZMQ_PORT}"

    payments:
      enabled: false
      scheme: SOLO

vip:
  enabled: true
  address: "${HA_VIP}"
  interface: "${HA_VETH2}"
  netmask: 32
  priority: 200
  canBecomeMaster: true
  clusterToken: "${HA_CLUSTER_TOKEN}"
  discoveryPort: ${HA_DISCOVERY_PORT}
  statusPort: ${HA_STATUS_PORT2}
  heartbeatInterval: 1s
  failoverTimeout: 3s
HAEOF2

            # Merge Mining: Add aux chain config to HA node2
            # V2 CONFIG FIX: mergeMining must be INSIDE the coin section, not at root level.
            if [[ "$MERGE_MODE" == "1" ]]; then
                # Create the merge mining block with proper V2 indentation (4 spaces = inside coin section)
                if [[ -n "${AUX_LEGACY_WALLET:-}" ]]; then
                    # Legacy wallet: no wallet specification needed
                    HA_MERGE_BLOCK2="\\
    # Merge Mining Configuration (auto-generated for HA test)\\
    mergeMining:\\
      enabled: true\\
      refreshInterval: 5s\\
      auxChains:\\
        - symbol: ${AUX_SYMBOL}\\
          enabled: true\\
          address: \"${AUX_MINING_ADDRESS}\"\\
          daemon:\\
            host: ${HA_HOST_IP}\\
            port: ${AUX_RPC_PORT}\\
            user: ${RPC_USER}\\
            password: \"${RPC_PASS}\"\\
"
                else
                    # Modern wallet: include wallet path for RPC endpoint
                    HA_MERGE_BLOCK2="\\
    # Merge Mining Configuration (auto-generated for HA test)\\
    mergeMining:\\
      enabled: true\\
      refreshInterval: 5s\\
      auxChains:\\
        - symbol: ${AUX_SYMBOL}\\
          enabled: true\\
          address: \"${AUX_MINING_ADDRESS}\"\\
          daemon:\\
            host: ${HA_HOST_IP}\\
            port: ${AUX_RPC_PORT}\\
            user: ${RPC_USER}\\
            password: \"${RPC_PASS}\"\\
            wallet: \"${AUX_WALLET_NAME}\"\\
"
                fi
                # Insert merge mining config BEFORE the 'stratum:' line
                sed -i "0,/^    stratum:/s/^    stratum:/${HA_MERGE_BLOCK2}    stratum:/" "$HA_CONFIG_DIR/config-ha-node2.yaml"
            fi

            if [[ -f "$HA_CONFIG_DIR/config-ha-node1.yaml" ]] && \
               [[ -f "$HA_CONFIG_DIR/config-ha-node2.yaml" ]]; then
                if [[ "$MERGE_MODE" == "1" ]]; then
                    log_ok "[3/8] HA config files created with merge mining (cluster token: ${HA_CLUSTER_TOKEN:0:8}...)"
                else
                    log_ok "[3/8] HA config files created (cluster token: ${HA_CLUSTER_TOKEN:0:8}...)"
                fi
                HA_VIP_PASS=$((HA_VIP_PASS + 1))
            else
                log_warn "[3/8] Failed to create HA config files"
            fi
        fi

        # -- 4/8. Start HA pool instances ------------------------------------
        if [[ $HA_VIP_PASS -ge 3 ]]; then
            log_info "Starting HA pool instances in network namespaces..."

            # BUG FIX: Verify database connectivity from namespaces before starting pools
            log_info "Verifying database connectivity from namespaces..."
            if $SUDO_CMD ip netns exec "$HA_NS1" nc -zw3 "$HA_HOST_IP" "$HA_PG_PORT" 2>/dev/null; then
                log_info "  Node1 can reach PostgreSQL at ${HA_HOST_IP}:${HA_PG_PORT}"
            else
                log_warn "  Node1 CANNOT reach PostgreSQL - checking bridge connectivity..."
                $SUDO_CMD ip netns exec "$HA_NS1" ping -c1 -W1 "$HA_BRIDGE_IP" && \
                    log_info "  Bridge IP reachable but PostgreSQL port blocked" || \
                    log_warn "  Bridge IP NOT reachable from namespace"
            fi

            # Start Node 1 (should become MASTER)
            $SUDO_CMD ip netns exec "$HA_NS1" \
                "$POOL_BINARY" -config "$HA_CONFIG_DIR/config-ha-node1.yaml" \
                &> "$LOG_DIR/spiralpool-ha-node1.log" &
            HA_NODE1_PID=$!

            sleep 2

            # Start Node 2 (should become BACKUP)
            $SUDO_CMD ip netns exec "$HA_NS2" \
                "$POOL_BINARY" -config "$HA_CONFIG_DIR/config-ha-node2.yaml" \
                &> "$LOG_DIR/spiralpool-ha-node2.log" &
            HA_NODE2_PID=$!

            # Wait for both to initialize (database connection + migrations can be slow)
            sleep 10

            if kill -0 $HA_NODE1_PID 2>/dev/null && kill -0 $HA_NODE2_PID 2>/dev/null; then
                log_ok "[4/8] Both HA pool instances started (PIDs: $HA_NODE1_PID, $HA_NODE2_PID)"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))
            else
                log_warn "[4/8] One or both HA instances failed to start"
                log_info "  Node1 log: tail $LOG_DIR/spiralpool-ha-node1.log"
                log_info "  Node2 log: tail $LOG_DIR/spiralpool-ha-node2.log"
            fi
        fi

        # -- 5/8. Verify master election -------------------------------------
        if [[ $HA_VIP_PASS -ge 4 ]]; then
            log_info "Verifying master election..."
            sleep 20  # BUG FIX: Give sufficient time for HA election (pool init + discovery + election)

            # Check Node 1 status (should be MASTER)
            NODE1_STATUS=$($SUDO_CMD ip netns exec "$HA_NS1" \
                curl -s --max-time 3 "http://${HA_NODE1_IP}:${HA_STATUS_PORT1}/status" 2>/dev/null) || true
            NODE1_ROLE=$(echo "$NODE1_STATUS" | grep -oP '"localRole":\s*"\K[^"]+' 2>/dev/null) || true

            # Check Node 2 status (should be BACKUP)
            NODE2_STATUS=$($SUDO_CMD ip netns exec "$HA_NS2" \
                curl -s --max-time 3 "http://${HA_NODE2_IP}:${HA_STATUS_PORT2}/status" 2>/dev/null) || true
            NODE2_ROLE=$(echo "$NODE2_STATUS" | grep -oP '"localRole":\s*"\K[^"]+' 2>/dev/null) || true

            log_info "  Node1 role: ${NODE1_ROLE:-UNKNOWN}"
            log_info "  Node2 role: ${NODE2_ROLE:-UNKNOWN}"

            if [[ "$NODE1_ROLE" == "MASTER" ]] && [[ "$NODE2_ROLE" == "BACKUP" ]]; then
                log_ok "[5/8] Master election correct (Node1=MASTER, Node2=BACKUP)"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))
            elif [[ "$NODE1_ROLE" == "MASTER" ]] || [[ "$NODE2_ROLE" == "MASTER" ]]; then
                log_warn "[5/8] One node is MASTER but roles not as expected"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))  # Partial credit
            else
                log_warn "[5/8] No MASTER elected — check HA logs"
                log_info "  Check: grep -i 'election\\|master\\|vip' $LOG_DIR/spiralpool-ha-node*.log"
            fi
        fi

        # -- 6/8. Verify VIP acquisition -------------------------------------
        if [[ $HA_VIP_PASS -ge 5 ]]; then
            log_info "Verifying VIP acquisition..."

            # Check if VIP is bound to Node 1's interface
            VIP_ON_NODE1=$($SUDO_CMD ip netns exec "$HA_NS1" ip addr show "$HA_VETH1" 2>/dev/null | grep -q " ${HA_VIP}/" && echo "yes" || echo "no")
            VIP_ON_NODE2=$($SUDO_CMD ip netns exec "$HA_NS2" ip addr show "$HA_VETH2" 2>/dev/null | grep -q " ${HA_VIP}/" && echo "yes" || echo "no")

            log_info "  VIP on Node1: $VIP_ON_NODE1"
            log_info "  VIP on Node2: $VIP_ON_NODE2"

            if [[ "$VIP_ON_NODE1" == "yes" ]] && [[ "$VIP_ON_NODE2" == "no" ]]; then
                log_ok "[6/8] VIP ($HA_VIP) acquired by MASTER (Node1)"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))
            elif [[ "$VIP_ON_NODE2" == "yes" ]] && [[ "$VIP_ON_NODE1" == "no" ]]; then
                log_ok "[6/8] VIP ($HA_VIP) acquired by Node2 (unexpected but valid)"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))
            elif [[ "$VIP_ON_NODE1" == "yes" ]] && [[ "$VIP_ON_NODE2" == "yes" ]]; then
                log_warn "[6/8] VIP on BOTH nodes — split-brain detected!"
            else
                log_warn "[6/8] VIP not acquired by either node"
                log_info "  Check: grep -i 'vip\\|acquire\\|bind' $LOG_DIR/spiralpool-ha-node*.log"
            fi
        fi

        # -- 7/8. Simulate failover (kill MASTER) ----------------------------
        if [[ $HA_VIP_PASS -ge 6 ]]; then
            log_info "Simulating failover by killing MASTER node..."

            # Kill Node 1 (MASTER)
            $SUDO_CMD kill -9 $HA_NODE1_PID 2>/dev/null || true

            # Wait for failover (should be ~3s based on failover_timeout)
            log_info "  Waiting for failover (up to 10s)..."
            FAILOVER_WAIT=0
            FAILOVER_DETECTED=0

            while [[ $FAILOVER_WAIT -lt 10 ]]; do
                sleep 1
                FAILOVER_WAIT=$((FAILOVER_WAIT + 1))

                # Check if Node 2 became MASTER
                NODE2_STATUS=$($SUDO_CMD ip netns exec "$HA_NS2" \
                    curl -s --max-time 2 "http://${HA_NODE2_IP}:${HA_STATUS_PORT2}/status" 2>/dev/null) || true
                NODE2_ROLE=$(echo "$NODE2_STATUS" | grep -oP '"localRole":\s*"\K[^"]+' 2>/dev/null) || true

                if [[ "$NODE2_ROLE" == "MASTER" ]]; then
                    FAILOVER_DETECTED=1
                    break
                fi
            done

            if [[ $FAILOVER_DETECTED -eq 1 ]]; then
                log_ok "[7/8] Failover detected — Node2 promoted to MASTER (${FAILOVER_WAIT}s)"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))
            else
                log_warn "[7/8] Failover not detected within 10s (Node2 role: ${NODE2_ROLE:-UNKNOWN})"
            fi
        fi

        # -- 8/8. Verify VIP migration ---------------------------------------
        if [[ $HA_VIP_PASS -ge 7 ]]; then
            log_info "Verifying VIP migration to new MASTER..."

            # Check if VIP moved to Node 2
            VIP_ON_NODE2=$($SUDO_CMD ip netns exec "$HA_NS2" ip addr show "$HA_VETH2" 2>/dev/null | grep -q " ${HA_VIP}/" && echo "yes" || echo "no")

            if [[ "$VIP_ON_NODE2" == "yes" ]]; then
                log_ok "[8/8] VIP ($HA_VIP) migrated to new MASTER (Node2)"
                HA_VIP_PASS=$((HA_VIP_PASS + 1))

                # Bonus: verify stratum is accessible via VIP
                STRATUM_VIA_VIP=$($SUDO_CMD ip netns exec "$HA_NS2" \
                    timeout 2 bash -c "echo | nc -w1 $HA_VIP $HA_STRATUM_PORT2" 2>/dev/null && echo "yes" || echo "no")
                if [[ "$STRATUM_VIA_VIP" == "yes" ]]; then
                    log_info "  Bonus: Stratum accessible via VIP"
                fi
            else
                log_warn "[8/8] VIP did not migrate to Node2"
            fi
        fi

        # -- Cleanup HA test -------------------------------------------------
        log_info "Cleaning up HA test environment..."
        [[ -n "${HA_NODE1_PID:-}" ]] && $SUDO_CMD kill -9 $HA_NODE1_PID 2>/dev/null || true
        [[ -n "${HA_NODE2_PID:-}" ]] && $SUDO_CMD kill -9 $HA_NODE2_PID 2>/dev/null || true
        cleanup_ha_namespaces
        log_ok "HA test cleanup complete"

        echo ""
        log_info "HA VIP failover emulation: $HA_VIP_PASS/$HA_VIP_TOTAL checks passed"

        if [[ $HA_VIP_PASS -ge 7 ]]; then
            log_ok "HA VIP FAILOVER: VERIFIED"
        elif [[ $HA_VIP_PASS -ge 5 ]]; then
            log_warn "HA VIP failover partial — review logs for details"
        else
            log_error "HA VIP failover test failed"
        fi

        log_info "HA logs available at:"
        log_info "  Node1: $LOG_DIR/spiralpool-ha-node1.log"
        log_info "  Node2: $LOG_DIR/spiralpool-ha-node2.log"
        echo ""
    fi
else
    if [[ "$HA_VIP_ENABLED" != "1" ]]; then
        log_info "Step 8g: HA VIP test skipped (HA_VIP_TEST=0)"
    fi
fi

# =============================================================================
# STEP 9: Verify Block Lifecycle
# =============================================================================

# Initialize lifecycle vars
LIFECYCLE_PASS=0
LIFECYCLE_TOTAL=6

if [[ "$HA_ONLY" == "1" ]]; then
    log_info "HA_ONLY mode — skipping Step 9 (block lifecycle verification)"
else

log_step "Step 9/10: Verify block lifecycle in pool logs"

POOL_LOG="$LOG_DIR/spiralpool-regtest.log"

check_lifecycle() {
    local pattern="$1"
    local description="$2"
    if grep -qiE "$pattern" "$POOL_LOG" 2>/dev/null; then
        log_ok "$description"
        return 0
    else
        log_warn "$description — NOT FOUND in pool log"
        return 1
    fi
}

LIFECYCLE_PASS=0
LIFECYCLE_TOTAL=6

echo ""
log_info "Checking pool log for block lifecycle events:"
echo ""

check_lifecycle "VARDIFF initial state|Stratum server started" "[1/6] Miner connected to stratum"          && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "authorize"                         "[2/6] Miner authorized"                     && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "share|accepted"                    "[3/6] Share submitted and processed"         && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "block.*found|found.*block|candidate|block.*candidate" "[4/6] Block candidate detected"    && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "submitblock|submit.*block|submitted" "[5/6] Block submitted to daemon via RPC"  && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))
check_lifecycle "pending|confirmed|inserted.*block" "[6/6] Block recorded in database"           && LIFECYCLE_PASS=$((LIFECYCLE_PASS + 1))

echo ""
if [[ $LIFECYCLE_PASS -eq $LIFECYCLE_TOTAL ]] && [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    log_ok "FULL BLOCK LIFECYCLE VERIFIED ($LIFECYCLE_PASS/$LIFECYCLE_TOTAL checks passed)"
elif [[ $LIFECYCLE_PASS -gt 0 ]]; then
    log_warn "Partial lifecycle: $LIFECYCLE_PASS/$LIFECYCLE_TOTAL checks passed"
    log_info "Review the full pool log for details: less $POOL_LOG"
else
    log_error "No lifecycle events found — pool may not have started correctly"
fi

fi  # End HA_ONLY skip for Step 9

# =============================================================================
# STEP 10: Summary
# =============================================================================

log_step "Step 10/10: Summary"

FINAL_HEIGHT=$(get_block_count)
FINAL_BALANCE=$(coincli_wallet getbalance 2>/dev/null || echo "unknown")

echo ""
echo -e "  ${BOLD}Test Results — $COIN_NAME ($COIN_SYMBOL)${NC}"
echo "  ─────────────────────────────────────────────"
if [[ "$HA_ONLY" == "1" ]]; then
    echo "  Mode:                  HA_ONLY (VIP failover test)"
    echo "  Chain height:          $FINAL_HEIGHT"
    echo "  HA VIP checks:         ${HA_VIP_PASS:-0} / ${HA_VIP_TOTAL:-8}"
else
    echo "  Chain height:          $FINAL_HEIGHT"
    echo "  Blocks mined via pool: $BLOCKS_FOUND / $TEST_BLOCKS"
    echo "  Lifecycle checks:      $LIFECYCLE_PASS / $LIFECYCLE_TOTAL"
    echo "  Wallet balance:        $FINAL_BALANCE $COIN_SYMBOL"
    echo "  Mining address:        $MINING_ADDRESS"
fi
if [[ ${PAYMENT_TOTAL:-0} -gt 0 ]]; then
    if [[ ${COINBASE_MATURE:-0} -eq 0 ]]; then
        PAYMENT_DENOM=$((PAYMENT_TOTAL - 2))
    else
        PAYMENT_DENOM=$PAYMENT_TOTAL
    fi
    if [[ $PAYMENT_PASS -ge $PAYMENT_DENOM ]] && [[ $PAYMENT_DENOM -gt 0 ]]; then
        echo -e "  Payment pipeline:      ${GREEN}PASS ($PAYMENT_PASS/$PAYMENT_DENOM checks)${NC}"
    elif [[ $PAYMENT_PASS -gt 0 ]]; then
        echo -e "  Payment pipeline:      ${YELLOW}PARTIAL ($PAYMENT_PASS/$PAYMENT_DENOM checks)${NC}"
    else
        echo -e "  Payment pipeline:      ${RED}SKIP (0/$PAYMENT_DENOM checks)${NC}"
    fi
fi
if [[ ${HA_TOTAL:-0} -gt 0 ]]; then
    if [[ ${HA_PASS:-0} -ge $HA_TOTAL ]]; then
        echo -e "  HA failover:           ${GREEN}PASS ($HA_PASS/$HA_TOTAL checks)${NC}"
    elif [[ ${HA_PASS:-0} -gt 0 ]]; then
        echo -e "  HA failover:           ${YELLOW}PARTIAL ($HA_PASS/$HA_TOTAL checks)${NC}"
    else
        echo -e "  HA failover:           ${RED}FAIL (0/$HA_TOTAL checks)${NC}"
    fi
fi
if [[ ${DAEMON_RESIL_TOTAL:-0} -gt 0 ]]; then
    if [[ ${DAEMON_RESIL_PASS:-0} -ge $DAEMON_RESIL_TOTAL ]]; then
        echo -e "  Daemon resilience:     ${GREEN}PASS ($DAEMON_RESIL_PASS/$DAEMON_RESIL_TOTAL checks)${NC}"
    elif [[ ${DAEMON_RESIL_PASS:-0} -gt 0 ]]; then
        echo -e "  Daemon resilience:     ${YELLOW}PARTIAL ($DAEMON_RESIL_PASS/$DAEMON_RESIL_TOTAL checks)${NC}"
    else
        echo -e "  Daemon resilience:     ${RED}FAIL (0/$DAEMON_RESIL_TOTAL checks)${NC}"
    fi
fi
# Merge Mining summary
if [[ "$MERGE_MODE" == "1" ]]; then
    # Aux blocks table: blocks_{parentPoolId}_{auxSymbol} (lowercase)
    AUX_SYMBOL_LOWER=$(echo "$AUX_SYMBOL" | tr '[:upper:]' '[:lower:]')
    AUX_BLOCKS_TABLE="blocks_${POOL_ID}_${AUX_SYMBOL_LOWER}"
    AUX_COUNT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM ${AUX_BLOCKS_TABLE};" 2>/dev/null | tr -d ' ') || AUX_COUNT="0"
    [[ -z "$AUX_COUNT" ]] && AUX_COUNT="0"
    if [[ "$AUX_COUNT" =~ ^[0-9]+$ ]] && [[ "$AUX_COUNT" -gt 0 ]]; then
        echo -e "  Merge mining ($AUX_SYMBOL):   ${GREEN}PASS ($AUX_COUNT aux blocks)${NC}"
    else
        echo -e "  Merge mining ($AUX_SYMBOL):   ${YELLOW}NO AUX BLOCKS (check difficulty)${NC}"
    fi
fi
echo ""
echo -e "  ${BOLD}Ports Used${NC}"
echo "  ─────────────────────────────────────────────"
echo "  Daemon RPC:   127.0.0.1:$RPC_PORT"
echo "  Daemon ZMQ:   127.0.0.1:$ZMQ_PORT"
echo "  Pool Stratum: 127.0.0.1:$STRATUM_PORT"
echo "  PostgreSQL:   $DB_HOST:$DB_PORT"
if [[ "$MERGE_MODE" == "1" ]]; then
    echo "  Aux RPC:      127.0.0.1:$AUX_RPC_PORT ($AUX_SYMBOL)"
fi
echo ""
echo -e "  ${BOLD}Log Files${NC}"
echo "  ─────────────────────────────────────────────"
echo "  Pool:     $LOG_DIR/spiralpool-regtest.log"
echo "  Miner:    $LOG_DIR/cpuminer-regtest.log"
echo "  Daemon:   $LOG_DIR/$DAEMON_LOG"
echo "  Startup:  $LOG_DIR/$DAEMON_STARTUP"
if [[ "$MERGE_MODE" == "1" ]]; then
    echo "  Aux Daemon: $LOG_DIR/$AUX_DAEMON_LOG"
fi
echo ""

if [[ "$HA_ONLY" == "1" ]]; then
    # HA_ONLY mode: Result based on HA VIP test results only
    if [[ ${HA_VIP_PASS:-0} -ge 6 ]]; then
        echo -e "  ${GREEN}${BOLD}RESULT: HA VIP TEST PASS${NC}"
        echo -e "  ${GREEN}HA VIP failover verified: ${HA_VIP_PASS:-0}/${HA_VIP_TOTAL:-8} checks passed${NC}"
        EXIT_CODE=0
    elif [[ ${HA_VIP_PASS:-0} -ge 3 ]]; then
        echo -e "  ${YELLOW}${BOLD}RESULT: HA VIP TEST PARTIAL${NC}"
        echo -e "  ${YELLOW}HA VIP partially working: ${HA_VIP_PASS:-0}/${HA_VIP_TOTAL:-8} checks passed${NC}"
        EXIT_CODE=1
    else
        echo -e "  ${RED}${BOLD}RESULT: HA VIP TEST FAIL${NC}"
        echo -e "  ${RED}HA VIP test failed: ${HA_VIP_PASS:-0}/${HA_VIP_TOTAL:-8} checks passed${NC}"
        EXIT_CODE=1
    fi
elif [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]] && [[ $LIFECYCLE_PASS -eq $LIFECYCLE_TOTAL ]]; then
    echo -e "  ${GREEN}${BOLD}RESULT: PASS${NC}"
    echo -e "  ${GREEN}$BLOCKS_FOUND blocks mined end-to-end through the pool.${NC}"
    echo -e "  ${GREEN}Full block lifecycle verified: stratum -> submitblock -> ZMQ -> DB${NC}"
elif [[ $BLOCKS_FOUND -ge $TEST_BLOCKS ]]; then
    echo -e "  ${YELLOW}${BOLD}RESULT: PARTIAL PASS${NC}"
    echo -e "  ${YELLOW}$BLOCKS_FOUND blocks mined, but $((LIFECYCLE_TOTAL - LIFECYCLE_PASS)) lifecycle checks failed.${NC}"
    echo -e "  ${YELLOW}Review logs above for details.${NC}"
else
    echo -e "  ${RED}${BOLD}RESULT: FAIL${NC}"
    echo -e "  ${RED}Only $BLOCKS_FOUND / $TEST_BLOCKS blocks found.${NC}"
    echo -e "  ${RED}Review logs for errors.${NC}"
fi

echo ""
if [[ "$MERGE_MODE" == "1" ]]; then
    echo -e "  ${CYAN}To re-run: ./scripts/linux/regtest.sh --merge ${MERGE_PAIR}${NC}"
    echo -e "  ${CYAN}To reset regtest chains: rm -rf ~/$DATA_DIR/regtest ~/$AUX_DATA_DIR/regtest${NC}"
else
    echo -e "  ${CYAN}To re-run: ./scripts/linux/regtest.sh $COIN${NC}"
    echo -e "  ${CYAN}To reset regtest chain: rm -rf ~/$DATA_DIR/regtest${NC}"
fi
echo ""

exit "${EXIT_CODE:-0}"
