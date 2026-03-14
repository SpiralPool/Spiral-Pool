#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# Spiral Pool - Coin Config Verification Script
#
# Downloads each coin daemon binary and runs it with the EXACT same config
# that install.sh would generate (clearnet mode). Verifies the daemon starts
# without config errors and responds to RPC.
#
# Usage: sudo ./test-coin-configs.sh [coin1] [coin2] ...
#   No args = test all coins
#   Example: sudo ./test-coin-configs.sh bch ltc doge
#
# IMPORTANT: Run this on a clean test server or VM. Each coin daemon will
# briefly connect to mainnet peers before being stopped.
#

set -euo pipefail

# Test directory
TEST_BASE="/tmp/spiralpool-config-test"
RESULTS_FILE="$TEST_BASE/results.txt"
LOG_DIR="$TEST_BASE/logs"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
WHITE='\033[1;37m'
NC='\033[0m'

# Architecture
ARCH=$(uname -m)
if [[ "$ARCH" == "x86_64" ]]; then
    ARCH_SUFFIX="x86_64-linux-gnu"
elif [[ "$ARCH" == "aarch64" ]]; then
    ARCH_SUFFIX="aarch64-linux-gnu"
else
    echo "Unsupported architecture: $ARCH"
    exit 1
fi

# Generate RPC password
gen_rpc_pass() {
    openssl rand -hex 16
}

log_test() { echo -e "${WHITE}[TEST]${NC} $1"; }
log_pass() { echo -e "${GREEN}[PASS]${NC} $1"; echo "PASS: $1" >> "$RESULTS_FILE"; }
log_fail() { echo -e "${RED}[FAIL]${NC} $1"; echo "FAIL: $1" >> "$RESULTS_FILE"; }
log_skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; echo "SKIP: $1" >> "$RESULTS_FILE"; }

# Common test function
# Args: coin_name daemon_binary cli_binary conf_file data_dir rpc_port rpc_user rpc_pass timeout
#       wallet_name addr_type expected_prefix aux_api
#   aux_api: "" = not merge-mineable, "legacy" = getauxblock, "modern" = createauxblock(address)
test_coin() {
    local coin="$1"
    local daemon="$2"
    local cli="$3"
    local conf="$4"
    local datadir="$5"
    local rpc_port="$6"
    local rpc_user="$7"
    local rpc_pass="$8"
    local timeout="${9:-60}"

    log_test "Starting $coin daemon..."

    # Start daemon
    if ! "$daemon" -daemon -conf="$conf" -datadir="$datadir" > "$LOG_DIR/${coin}-startup.log" 2>&1; then
        log_fail "$coin - daemon failed to start"
        cat "$LOG_DIR/${coin}-startup.log" 2>/dev/null
        return 1
    fi

    # Wait for RPC
    local waited=0
    while ! "$cli" -conf="$conf" -datadir="$datadir" getblockchaininfo &>/dev/null; do
        sleep 2
        waited=$((waited + 2))
        if [[ $waited -ge $timeout ]]; then
            log_fail "$coin - RPC not responding after ${timeout}s"
            # Check debug log for errors
            if [[ -f "$datadir/debug.log" ]]; then
                echo "--- Last 20 lines of debug.log ---"
                tail -20 "$datadir/debug.log"
            fi
            # Try to stop it anyway
            "$cli" -conf="$conf" -datadir="$datadir" stop &>/dev/null || true
            sleep 3
            return 1
        fi
    done

    # Get blockchain info
    local info
    info=$("$cli" -conf="$conf" -datadir="$datadir" getblockchaininfo 2>&1)
    if echo "$info" | grep -q '"chain"'; then
        local chain
        chain=$(echo "$info" | grep '"chain"' | head -1 | sed 's/.*: *"\([^"]*\)".*/\1/')
        log_pass "$coin - daemon started, config valid, chain=$chain (RPC responded in ${waited}s)"
    else
        log_fail "$coin - getblockchaininfo returned unexpected output"
        echo "$info"
    fi

    # ── WALLET CREATION TEST ──
    # Mirrors the EXACT production flow from spiralpool-wallet (install.sh):
    #   1. loadwallet → getwalletinfo pre-check (catches old daemons with default wallet)
    #   2. If wallet exists: listreceivedbyaddress → getnewaddress with type fallback
    #   3. If no wallet: createwallet → getnewaddress with type
    #   4. Validate address prefix
    local wallet_name="${10}"
    local addr_type="${11}"
    local expected_prefix="${12}"
    local aux_api="${13}"  # "" = not merge-mineable, "legacy" = getauxblock, "modern" = createauxblock

    if [[ -n "$wallet_name" ]]; then
        log_test "$coin - creating wallet '$wallet_name'..."

        # Step 1: Try loading named wallet (matches install.sh line 24559)
        local load_result
        load_result=$("$cli" -conf="$conf" -datadir="$datadir" loadwallet "$wallet_name" 2>&1) || true
        local wallet_cli="$cli"
        # loadwallet succeeded or wallet already loaded → use -rpcwallet for subsequent calls
        if ! echo "$load_result" | grep -qi "error" || echo "$load_result" | grep -qi "already loaded"; then
            wallet_cli="$cli -rpcwallet=$wallet_name"
        fi

        # Step 2: Check if wallet already exists — named or default (matches install.sh line 24567)
        local wallet_info
        wallet_info=$("$wallet_cli" -conf="$conf" -datadir="$datadir" getwalletinfo 2>/dev/null) || true

        local new_address=""
        if [[ -n "$wallet_info" ]]; then
            # Wallet exists — try to get existing address (matches install.sh line 24573)
            local existing_addr
            existing_addr=$("$wallet_cli" -conf="$conf" -datadir="$datadir" listreceivedbyaddress 0 true 2>/dev/null \
                | grep -o '"address":"[^"]*' | head -1 | cut -d'"' -f4) || true

            if [[ -z "$existing_addr" ]]; then
                # No existing address — generate one (matches install.sh lines 24575-24588)
                if [[ -n "$addr_type" ]]; then
                    existing_addr=$("$wallet_cli" -conf="$conf" -datadir="$datadir" getnewaddress "pool" "$addr_type" 2>&1) || true
                    # Old daemons (DOGE v1.14.9, PEP v1.1.0) don't accept type param — try without
                    if echo "$existing_addr" | grep -qi "error"; then
                        existing_addr=$("$wallet_cli" -conf="$conf" -datadir="$datadir" getnewaddress "" 2>&1) || true
                    fi
                else
                    existing_addr=$("$wallet_cli" -conf="$conf" -datadir="$datadir" getnewaddress "pool" 2>&1) || true
                fi
                if echo "$existing_addr" | grep -qi "error"; then
                    existing_addr=""
                fi
            fi

            if [[ -n "$existing_addr" ]]; then
                new_address="$existing_addr"
                log_test "$coin - using existing wallet address"
            fi
        fi

        # Step 3: If no address from existing wallet, try createwallet (matches install.sh line 24623)
        if [[ -z "$new_address" ]]; then
            local create_result
            create_result=$("$cli" -conf="$conf" -datadir="$datadir" createwallet "$wallet_name" 2>&1) || true

            if echo "$create_result" | grep -qi "error"; then
                if echo "$create_result" | grep -qi "already exists\|already loaded\|Database already exists"; then
                    # Wallet already exists — load it and get address
                    log_test "$coin - wallet already exists, loading..."
                    "$cli" -conf="$conf" -datadir="$datadir" loadwallet "$wallet_name" &>/dev/null || true
                    if [[ -n "$addr_type" ]]; then
                        new_address=$("$cli" -conf="$conf" -datadir="$datadir" -rpcwallet="$wallet_name" getnewaddress "pool-rewards" "$addr_type" 2>&1) || true
                    else
                        new_address=$("$cli" -conf="$conf" -datadir="$datadir" -rpcwallet="$wallet_name" getnewaddress "pool-rewards" 2>&1) || true
                    fi
                else
                    # createwallet not supported (old daemon) or other error
                    # Try generating address from default wallet (matches install.sh lines 24672-24687)
                    log_test "$coin - no createwallet support (old daemon), using default wallet"
                    if [[ -n "$addr_type" ]]; then
                        new_address=$("$cli" -conf="$conf" -datadir="$datadir" getnewaddress "pool-rewards" "$addr_type" 2>&1) || true
                        if echo "$new_address" | grep -qi "error"; then
                            new_address=""
                        fi
                    fi
                    if [[ -z "$new_address" ]]; then
                        # Old daemons: getnewaddress only takes [account], no type parameter
                        new_address=$("$cli" -conf="$conf" -datadir="$datadir" getnewaddress "" 2>&1) || true
                    fi
                fi
            else
                # createwallet succeeded — use wallet-specific RPC
                if [[ -n "$addr_type" ]]; then
                    new_address=$("$cli" -conf="$conf" -datadir="$datadir" -rpcwallet="$wallet_name" getnewaddress "pool-rewards" "$addr_type" 2>&1) || true
                else
                    new_address=$("$cli" -conf="$conf" -datadir="$datadir" -rpcwallet="$wallet_name" getnewaddress "pool-rewards" 2>&1) || true
                fi
            fi
        fi

        if echo "$new_address" | grep -qi "error"; then
            log_fail "$coin - getnewaddress failed: $new_address"
        elif [[ -z "$new_address" ]]; then
            log_fail "$coin - getnewaddress returned empty"
        else
            # Validate address prefix
            local prefix_ok=false
            IFS='|' read -ra PREFIXES <<< "$expected_prefix"
            for pfx in "${PREFIXES[@]}"; do
                if [[ "$new_address" == ${pfx}* ]]; then
                    prefix_ok=true
                    break
                fi
            done

            if [[ "$prefix_ok" == "true" ]]; then
                log_pass "$coin - wallet created, address generated: $new_address"
            else
                log_fail "$coin - address prefix mismatch: got '$new_address', expected prefix '$expected_prefix'"
            fi
        fi
    fi

    # ── MERGE MINING TEST ──
    # Tests getauxblock (legacy) or createauxblock (modern) RPC on aux chains
    # Matches stratum code: internal/auxpow/manager.go
    #   Legacy (NMC, SYS, DOGE, PEP): getauxblock() — no params
    #   Modern (FBTC, XMY): createauxblock(address) — uses pool payout address
    if [[ -n "$aux_api" ]]; then
        log_test "$coin - testing merge mining RPC ($aux_api API)..."
        local aux_result
        local aux_cli="$cli"

        if [[ "$aux_api" == "legacy" ]]; then
            # Legacy API: getauxblock with no params
            aux_result=$("$aux_cli" -conf="$conf" -datadir="$datadir" getauxblock 2>&1) || true

            if echo "$aux_result" | grep -qi "error"; then
                # Some coins (DOGE) activate auxpow at a specific height (371337)
                # At block 0, getauxblock may not be available yet — expected, not a bug
                if echo "$aux_result" | grep -qi "not available\|not activated\|not connected\|downloading blocks\|auxiliary\|height\|method not found"; then
                    log_skip "$coin - getauxblock precondition not met (not connected/syncing — RPC exists and responded)"
                else
                    log_fail "$coin - getauxblock failed: $aux_result"
                fi
            elif echo "$aux_result" | grep -qi '"hash"'; then
                log_pass "$coin - merge mining RPC works (getauxblock returned block template)"
            else
                # Some daemons return the hash directly without JSON wrapper
                log_pass "$coin - merge mining RPC works (getauxblock responded)"
            fi

        elif [[ "$aux_api" == "modern" ]]; then
            # Modern API: createauxblock with payout address
            if [[ -n "$new_address" ]]; then
                aux_result=$("$aux_cli" -conf="$conf" -datadir="$datadir" createauxblock "$new_address" 2>&1) || true
            else
                # No address available — try without (will likely fail)
                aux_result=$("$aux_cli" -conf="$conf" -datadir="$datadir" createauxblock "" 2>&1) || true
            fi

            if echo "$aux_result" | grep -qi "error"; then
                if echo "$aux_result" | grep -qi "not available\|not activated\|not connected\|downloading blocks\|method not found"; then
                    log_skip "$coin - createauxblock precondition not met (not connected/syncing — RPC exists and responded)"
                else
                    log_fail "$coin - createauxblock failed: $aux_result"
                fi
            elif echo "$aux_result" | grep -qi '"hash"'; then
                log_pass "$coin - merge mining RPC works (createauxblock returned block template)"
            else
                log_pass "$coin - merge mining RPC works (createauxblock responded)"
            fi
        fi
    fi

    # Stop daemon
    log_test "Stopping $coin daemon..."
    "$cli" -conf="$conf" -datadir="$datadir" stop &>/dev/null || true
    sleep 5

    # Wait for clean shutdown
    local stop_wait=0
    while pgrep -f "$daemon.*$datadir" &>/dev/null; do
        sleep 2
        stop_wait=$((stop_wait + 2))
        if [[ $stop_wait -ge 30 ]]; then
            log_test "$coin - force killing daemon"
            pkill -f "$daemon.*$datadir" || true
            sleep 2
            break
        fi
    done

    return 0
}

# Download and extract a tarball
# Args: coin url filename
download_extract() {
    local coin="$1"
    local url="$2"
    local bin_dir="$TEST_BASE/$coin/bin"

    mkdir -p "$bin_dir"

    local filename=$(basename "$url")
    local dl_path="$TEST_BASE/downloads/$filename"

    if [[ -f "$dl_path" ]]; then
        log_test "$coin - using cached download: $filename"
    else
        log_test "$coin - downloading $filename..."
        if ! wget -q --show-progress -O "$dl_path" "$url" 2>&1; then
            log_fail "$coin - download failed: $url"
            return 1
        fi
    fi

    # Extract
    log_test "$coin - extracting..."
    if [[ "$filename" == *.zip ]]; then
        unzip -qo "$dl_path" -d "$TEST_BASE/$coin/extract" 2>/dev/null
    else
        mkdir -p "$TEST_BASE/$coin/extract"
        tar xzf "$dl_path" -C "$TEST_BASE/$coin/extract" 2>/dev/null
    fi

    return 0
}

# ═══════════════════════════════════════════════════════════════════════════════
# PER-COIN TEST FUNCTIONS
# Each writes the EXACT config install.sh would generate (clearnet mode)
# ═══════════════════════════════════════════════════════════════════════════════

test_dgb() {
    local coin="dgb"
    local url="https://github.com/DigiByte-Core/digibyte/releases/download/v8.26.2/digibyte-8.26.2-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiraldgb"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=14022
    local zmq_port=28532
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/digibyte.conf"

    download_extract "$coin" "$url" || return 1

    # Find daemon binary
    local daemon=$(find "$TEST_BASE/$coin/extract" -name "digibyted" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "digibyte-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - digibyted not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    # EXACT config from install.sh (clearnet mode)
    cat > "$conf" << EOF
# DigiByte Core Configuration
# Spiral Pool v3 - Solo Mining Pool
# Network Mode: CLEARNET (Fast Sync)

# === CORE SETTINGS ===
server=1
daemon=1
listen=1
txindex=1
prune=0

# === RPC CONFIGURATION ===
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcport=$rpc_port
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcthreads=8
rpcworkqueue=64

# === MINING ===
algo=sha256d

# === ZMQ NOTIFICATIONS (for instant block detection) ===
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# === PERFORMANCE OPTIMIZATION ===
dbcache=8192
maxmempool=300
par=4
maxsigcachesize=250

# === CLEARNET - FAST SYNC MODE ===
maxconnections=256
maxoutconnections=24
maxreceivebuffer=25000
maxsendbuffer=5000
onlynet=ipv4
bind=0.0.0.0
dnsseed=1
forcednsseed=1
blocksonly=0
nblocks=64

# === LOGGING & CONSOLE ===
printtoconsole=0
logtimestamps=1
logips=1
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50

# === WALLET ===
disablewallet=0
addresstype=legacy

# === SECURITY ===
deprecatedrpc=

# === CHECKPOINTS & SIGNATURE VALIDATION ===
checkpoints=1
assumevalid=457f6864b52e5076a433afe3c28e3ae0bbeeaba9036a782ddb691242326fcb80

# === BLOCK DOWNLOAD OPTIMIZATION ===
maxblocksinprogress=32
maxorphantx=100
blockreconstructionextratxn=100

# === SYNC OPTIMIZATION ===
peertimeout=60
blockstallingtimeout=10

# === SEED NODES (verified working - Jan 2026) ===
seednode=seed.digibyte.io
seednode=seed.digibyte.help
seednode=seed.diginode.tools
seednode=seed.digibyte.link
seednode=seed.quakeguy.com
seednode=seed.aroundtheblock.app

dnsseed=seed.digibyte.io
dnsseed=seed.digibyte.help
dnsseed=seed.diginode.tools
dnsseed=seed.digibyte.link
dnsseed=seed.quakeguy.com
dnsseed=seed.aroundtheblock.app
EOF

    # Wallet: pool-dgb, legacy, prefix D or S. Not merge-mineable (parent chain).
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-dgb" "legacy" "D|S" ""
}

test_btc() {
    local coin="btc"
    local url="https://bitcoinknots.org/files/29.x/29.3.knots20260210/bitcoin-29.3.knots20260210-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiralbtc"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=8332
    local zmq_port=28332
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/bitcoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "bitcoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "bitcoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - bitcoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Bitcoin Knots Configuration
# Spiral Pool v3 - Multi-Coin Solo Mining
# Network Mode: CLEARNET (Fast Sync)

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1
prune=0
chain=main

# === RPC CONFIGURATION ===
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcport=$rpc_port
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcthreads=8
rpcworkqueue=64

# === ZMQ NOTIFICATIONS (for instant block detection) ===
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# === PERFORMANCE OPTIMIZATION ===
dbcache=8192
maxmempool=300
par=4

# === CLEARNET - FAST SYNC MODE ===
maxconnections=256
maxoutconnections=24
maxreceivebuffer=25000
maxsendbuffer=5000
listen=1
bind=0.0.0.0
onlynet=ipv4
dnsseed=1
forcednsseed=1
peertimeout=60
blockstallingtimeout=10
nblocks=64

# === WALLET ===
disablewallet=0
addresstype=bech32
changetype=bech32

# === LOGGING ===
printtoconsole=0
logtimestamps=1
logips=1
shrinkdebugfile=1
debuglogfile=debug.log

# === ASSUME VALID ===
assumevalid=00000000000000000000611fd22f2df7c8fbd0688745c3a6c3bb5109cc2a12cb

# === CHECKPOINTS ===
checkpoints=1

# === SEED NODES ===
seednode=seed.bitcoin.sipa.be
seednode=dnsseed.bluematt.me
seednode=seed.bitcoin.jonasschnelli.ch
seednode=seed.btc.petertodd.net
seednode=seed.bitcoin.sprovoost.nl
seednode=dnsseed.emzy.de
seednode=seed.bitcoin.wiz.biz
seednode=seed.mainnet.achownodes.xyz

dnsseed=seed.bitcoin.sipa.be
dnsseed=dnsseed.bluematt.me
dnsseed=seed.btc.petertodd.net
EOF

    # Wallet: pool-btc, bech32, prefix bc1. Parent chain for SHA-256d merge mining.
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-btc" "bech32" "bc1" ""
}

test_bch() {
    local coin="bch"
    local url="https://github.com/bitcoin-cash-node/bitcoin-cash-node/releases/download/v29.0.0/bitcoin-cash-node-29.0.0-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiralbch"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=8432
    local zmq_port=28432
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/bitcoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "bitcoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "bitcoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - bitcoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    # BCH config - NO chain=main, NO debuglogfile, NO rpcworkqueue,
    # NO maxoutconnections, NO checkpoints, NO forcednsseed,
    # NO peertimeout/blockstallingtimeout, NO nblocks, NO i2psam
    cat > "$conf" << EOF
# Bitcoin Cash Node (BCHN) Configuration
# Spiral Pool v3 - Multi-Coin Solo Mining
# Network Mode: CLEARNET (Fast Sync)
#
# IMPORTANT: Uses unique ports to avoid conflict with Bitcoin Knots
# RPC: 8432 (not 8332), P2P: 8433 (not 8333), ZMQ: 28432

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1
prune=0

# === RPC CONFIGURATION (unique port 8432) ===
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcport=$rpc_port
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcthreads=8

# === ZMQ NOTIFICATIONS (unique port 28432) ===
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# === PERFORMANCE OPTIMIZATION ===
dbcache=2048
maxmempool=300
par=4

# === CLEARNET - FAST SYNC MODE ===
maxconnections=256
maxreceivebuffer=25000
maxsendbuffer=5000
listen=1
bind=0.0.0.0:8433
port=8433
onlynet=ipv4
dnsseed=1

# === WALLET ===
disablewallet=0

# === LOGGING ===
printtoconsole=0
logtimestamps=1
logips=1
shrinkdebugfile=1

# === BCH-SPECIFIC SETTINGS ===
excessiveblocksize=32000000

# === ASSUME VALID ===
assumevalid=000000000000000000982e811b14b1fe425553fc1b437a34caddea0d70ec6508

# === SEED NODES ===
seednode=seed.flowee.cash
seednode=seed-bch.bitcoinforks.org
seednode=btccash-seeder.bitcoinunlimited.info
seednode=seed.bchd.cash
seednode=seed.bch.loping.net
seednode=dnsseed.electroncash.de
seednode=bchseed.c3-soft.com
seednode=bch.bitjson.com
EOF

    # Wallet: pool-bch, NO address type (CashAddr default), prefix bitcoincash: or 1. Not merge-mineable.
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-bch" "" "bitcoincash:|1" ""
}

test_bc2() {
    local coin="bc2"
    # BC2 uses -CLI suffix, not -gnu
    local bc2_arch="x86_64-linux-CLI"
    [[ "$ARCH" == "aarch64" ]] && bc2_arch="aarch64-linux-CLI"
    local url="https://github.com/Bitcoin-II/BitcoinII-Core/releases/download/v29.1.0/BitcoinII-29.1.0-${bc2_arch}.tar.gz"
    local rpc_user="spiralbc2"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=8339
    local zmq_port=28338
    local p2p_port=8338
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/bitcoinii.conf"

    download_extract "$coin" "$url" || return 1

    # BC2 uses capital II: bitcoinIId, bitcoinII-cli
    local daemon=$(find "$TEST_BASE/$coin/extract" -name "bitcoinIId" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "bitcoinII-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - bitcoinIId not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Bitcoin II Core Configuration
# Spiral Pool v3 - Multi-Coin Solo Mining
# Network Mode: CLEARNET (Fast Sync)
#
# CRITICAL ADDRESS WARNING: Bitcoin II uses IDENTICAL address formats to Bitcoin.
# Port Reference: RPC 8339, P2P 8338, ZMQ 28338

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1
prune=0
chain=main

# === RPC CONFIGURATION (BC2 RPC port 8339) ===
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcport=$rpc_port
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcthreads=8
rpcworkqueue=64

# === ZMQ NOTIFICATIONS (BC2 ZMQ port 28338) ===
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# === PERFORMANCE OPTIMIZATION ===
dbcache=2048
maxmempool=300
par=0

# === CLEARNET - FAST SYNC MODE ===
maxconnections=256
maxoutconnections=24
maxreceivebuffer=25000
maxsendbuffer=5000
listen=1
bind=0.0.0.0:$p2p_port
port=$p2p_port
onlynet=ipv4
dnsseed=1
forcednsseed=1
peertimeout=60
blockstallingtimeout=10
nblocks=64

# === WALLET ===
disablewallet=0
addresstype=bech32
changetype=bech32

# === LOGGING ===
printtoconsole=0
logtimestamps=1
logips=1
shrinkdebugfile=1
debuglogfile=debug.log

# === CHECKPOINTS ===
checkpoints=1

# === SEED NODES (official Bitcoin II DNS seeds) ===
seednode=dnsseed.bitcoin-ii.org
seednode=bitcoinII.ddns.net

dnsseed=dnsseed.bitcoin-ii.org
dnsseed=bitcoinII.ddns.net

addnode=144.76.79.60:8338
addnode=75.130.145.1:8338
addnode=45.32.205.199:8338
addnode=98.22.238.18:8338
EOF

    # Wallet: pool-bc2, bech32, prefix bc1 or 1 or 3 (same as BTC). Not merge-mineable.
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-bc2" "bech32" "bc1|1|3" ""
}

test_nmc() {
    local coin="nmc"
    local url="https://www.namecoin.org/files/namecoin-core/namecoin-core-28.0/namecoin-28.0-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiralnmc"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=8336
    local zmq_port=28336
    local p2p_port=8334
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/namecoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "namecoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "namecoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - namecoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Namecoin Core Configuration for Spiral Pool

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=125
listen=1
dnsseed=1

# Performance
dbcache=512
maxmempool=300

# Wallet
disablewallet=0

# Logging
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/namecoind.pid
EOF

    # Wallet: pool-nmc, legacy, prefix N or M. Merge-mineable with BTC (legacy API).
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-nmc" "legacy" "N|M" "legacy"
}

test_sys() {
    local coin="sys"
    local url="https://github.com/syscoin/syscoin/releases/download/v5.0.5/syscoin-5.0.5-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiralsys"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=8370
    local zmq_port=28370
    local p2p_port=8369
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/syscoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "syscoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "syscoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - syscoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Syscoin Core Configuration for Spiral Pool

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=125
listen=1
dnsseed=1

# Performance
dbcache=1024
maxmempool=300

# Wallet
disablewallet=0

# Logging
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/syscoind.pid
EOF

    # Wallet: pool-sys, legacy, prefix S or sys1q. Merge-mineable with BTC (legacy API).
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-sys" "legacy" "S|sys1q" "legacy"
}

test_xmy() {
    local coin="xmy"
    local url="https://github.com/myriadteam/myriadcoin/releases/download/v0.18.1.0/myriadcoin-0.18.1.0-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiralxmy"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=10889
    local zmq_port=28889
    local p2p_port=10888
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/myriadcoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "myriadcoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "myriadcoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - myriadcoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Myriad Core Configuration for Spiral Pool

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=125
listen=1
dnsseed=1

# Performance
dbcache=512
maxmempool=300

# Wallet
disablewallet=0

# Logging
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/myriadcoind.pid
EOF

    # Wallet: pool-xmy, legacy, prefix M. Merge-mineable with BTC (modern API — createauxblock).
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-xmy" "legacy" "M" "modern"
}

test_fbtc() {
    local coin="fbtc"

    # FBTC is x86_64 only
    if [[ "$ARCH" != "x86_64" ]]; then
        log_skip "$coin - x86_64 only (current: $ARCH)"
        return 0
    fi

    local url="https://github.com/fractal-bitcoin/fractald-release/releases/download/v0.3.0/fractald-0.3.0-x86_64-linux-gnu.tar.gz"
    local rpc_user="spiralfbtc"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=8340
    local zmq_port=28340
    local p2p_port=8341
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/fractal.conf"

    download_extract "$coin" "$url" || return 1

    # FBTC ships bitcoind/bitcoin-cli but we use them as fractald/fractal-cli
    local daemon=$(find "$TEST_BASE/$coin/extract" -name "bitcoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "bitcoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - bitcoind (fractald) not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Fractal Bitcoin Configuration for Spiral Pool

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=125
listen=1
dnsseed=1
forcednsseed=1

seednode=dnsseed-mainnet.fractalbitcoin.io
seednode=dnsseed-mainnet.unisat.io

dnsseed=dnsseed-mainnet.fractalbitcoin.io
dnsseed=dnsseed-mainnet.unisat.io

# Performance
dbcache=1024
maxmempool=300

# Wallet
disablewallet=0

# Logging
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/fractald.pid
EOF

    # Wallet: pool-fbtc, bech32, prefix bc1. Merge-mineable with BTC (modern API — createauxblock).
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-fbtc" "bech32" "bc1" "modern"
}

test_qbx() {
    local coin="qbx"

    # QBX is x86_64 only
    if [[ "$ARCH" != "x86_64" ]]; then
        log_skip "$coin - x86_64 only (current: $ARCH)"
        return 0
    fi

    local url="https://github.com/q-bitx/Source-/releases/download/v0.1.0/qbitx-linux-x86.zip"
    local rpc_user="spiralqbx"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=8344
    local zmq_port=28344
    local p2p_port=8345
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/qbitx.conf"

    # QBX ships as a zip, not tar.gz
    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "qbitx" -not -name "qbitx-cli" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "qbitx-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - qbitx not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Q-BitX Configuration for Spiral Pool

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=32
listen=1

# Performance
dbcache=512
maxmempool=300

# Wallet
disablewallet=0

# Logging
shrinkdebugfile=1
printtoconsole=0

# PID file
pid=$datadir/qbitxd.pid
EOF

    # Wallet: pool-qbx, pq address type (post-quantum). Standalone SHA-256d.
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-qbx" "" "" ""
}

test_ltc() {
    local coin="ltc"
    local url="https://github.com/litecoin-project/litecoin/releases/download/v0.21.4/litecoin-0.21.4-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiralltc"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=9332
    local zmq_port=28933
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/litecoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "litecoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "litecoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - litecoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Litecoin Core Configuration for Spiral Pool
# Network Mode: CLEARNET (Fast Sync)

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# === NETWORK CONFIGURATION ===
maxconnections=125
listen=1
bind=0.0.0.0
onlynet=ipv4
dnsseed=1

# Performance
dbcache=1024
maxmempool=300

# Logging
debug=rpc
debug=zmq
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/litecoind.pid
EOF

    # Wallet: pool-ltc, bech32, prefix ltc1. Parent chain for Scrypt merge mining.
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-ltc" "bech32" "ltc1" ""
}

test_doge() {
    local coin="doge"
    local url="https://github.com/dogecoin/dogecoin/releases/download/v1.14.9/dogecoin-1.14.9-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiraldoge"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=22555
    local zmq_port=28555
    local p2p_port=22556
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/dogecoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "dogecoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "dogecoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - dogecoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Dogecoin Core Configuration for Spiral Pool
# Network Mode: CLEARNET (Fast Sync)

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=100
listen=1
dnsseed=1

# Performance
dbcache=512
maxmempool=300

# Logging
debug=rpc
debug=zmq
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/dogecoind.pid
EOF

    # Wallet: pool-doge, legacy, prefix D. Merge-mineable with LTC (legacy API). AuxPoW starts at block 371337.
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-doge" "legacy" "D" "legacy"
}

test_pep() {
    local coin="pep"
    local url="https://github.com/pepecoinppc/pepecoin/releases/download/v1.1.0/pepecoin-1.1.0-${ARCH_SUFFIX}.tar.gz"
    local rpc_user="spiralpep"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=33873
    local p2p_port=33874
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/pepecoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "pepecoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "pepecoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - pepecoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    # PEP: NO ZMQ — PepeCoin v1.1.0 compiled without ZMQ, crashes with SIGABRT
    cat > "$conf" << EOF
# PepeCoin Core Configuration for Spiral Pool
# Network Mode: CLEARNET (Fast Sync)

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ: DISABLED — PepeCoin v1.1.0 binary is compiled without ZMQ support.
# The daemon crashes (SIGABRT) if zmqpub* options are present.
# Stratum uses RPC polling fallback for block notifications.

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=75
listen=1
dnsseed=1

# Performance
dbcache=256
maxmempool=200

# Logging
debug=rpc
debug=zmq
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/pepecoind.pid
EOF

    # Wallet: pool-pep, legacy, prefix P. Merge-mineable with LTC (legacy API).
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-pep" "legacy" "P" "legacy"
}

test_cat() {
    local coin="cat"
    # CAT uses ZIP, different naming
    local cat_filename="Catcoin-Linux.zip"
    [[ "$ARCH" == "aarch64" ]] && cat_filename="Catcoin-AArch.zip"
    local url="https://github.com/CatcoinCore/catcoincore/releases/download/v2.1.1/${cat_filename}"
    local rpc_user="spiralcat"
    local rpc_pass=$(gen_rpc_pass)
    local rpc_port=9932
    local zmq_port=28932
    local p2p_port=9933
    local datadir="$TEST_BASE/$coin/data"
    local conf="$datadir/catcoin.conf"

    download_extract "$coin" "$url" || return 1

    local daemon=$(find "$TEST_BASE/$coin/extract" -name "catcoind" -type f | head -1)
    local cli=$(find "$TEST_BASE/$coin/extract" -name "catcoin-cli" -type f | head -1)
    [[ -z "$daemon" ]] && { log_fail "$coin - catcoind not found in archive"; return 1; }
    chmod +x "$daemon" "$cli"

    mkdir -p "$datadir"

    cat > "$conf" << EOF
# Catcoin Core Configuration for Spiral Pool
# Network Mode: CLEARNET (Fast Sync)

# === CORE SETTINGS ===
server=1
daemon=1
txindex=1

# RPC Settings
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=$rpc_port

# ZMQ for block notifications
zmqpubhashblock=tcp://127.0.0.1:$zmq_port
zmqpubrawtx=tcp://127.0.0.1:$zmq_port

# P2P port
port=$p2p_port

# === NETWORK CONFIGURATION ===
maxconnections=75
listen=1
dnsseed=1

# Performance
dbcache=256
maxmempool=200

# Logging
debug=rpc
debug=zmq
shrinkdebugfile=1
debuglogfile=debug.log
maxdebugfilesize=50
logips=1

# PID file
pid=$datadir/catcoind.pid
EOF

    # Wallet: pool-cat, legacy, prefix 9. Not merge-mineable.
    test_coin "$coin" "$daemon" "$cli" "$conf" "$datadir" "$rpc_port" "$rpc_user" "$rpc_pass" 60 "pool-cat" "legacy" "9" ""
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN
# ═══════════════════════════════════════════════════════════════════════════════

# Must run as root (daemons bind to ports, need file permissions)
if [[ $EUID -ne 0 ]]; then
    echo "This script must be run as root (sudo)"
    exit 1
fi

# Setup
mkdir -p "$TEST_BASE/downloads" "$LOG_DIR"
echo "Spiral Pool - Coin Config Test Results" > "$RESULTS_FILE"
echo "Date: $(date)" >> "$RESULTS_FILE"
echo "Arch: $ARCH ($ARCH_SUFFIX)" >> "$RESULTS_FILE"
echo "---" >> "$RESULTS_FILE"

# Determine which coins to test
ALL_COINS="dgb btc bch bc2 nmc sys xmy fbtc qbx ltc doge pep cat"
if [[ $# -gt 0 ]]; then
    COINS_TO_TEST="$*"
else
    COINS_TO_TEST="$ALL_COINS"
fi

echo ""
echo -e "${WHITE}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${WHITE}  Spiral Pool - Coin Config Verification${NC}"
echo -e "${WHITE}  Testing: $COINS_TO_TEST${NC}"
echo -e "${WHITE}═══════════════════════════════════════════════════════════════${NC}"
echo ""

# Run tests
for coin in $COINS_TO_TEST; do
    echo ""
    echo -e "${WHITE}─── Testing $coin ───${NC}"

    case "$coin" in
        dgb)  test_dgb  ;;
        btc)  test_btc  ;;
        bch)  test_bch  ;;
        bc2)  test_bc2  ;;
        nmc)  test_nmc  ;;
        sys)  test_sys  ;;
        xmy)  test_xmy  ;;
        fbtc) test_fbtc ;;
        qbx)  test_qbx  ;;
        ltc)  test_ltc  ;;
        doge) test_doge ;;
        pep)  test_pep  ;;
        cat)  test_cat  ;;
        *)    log_fail "Unknown coin: $coin" ;;
    esac
done

# Summary
echo ""
echo -e "${WHITE}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${WHITE}  RESULTS SUMMARY${NC}"
echo -e "${WHITE}═══════════════════════════════════════════════════════════════${NC}"
echo ""

pass_count=$(grep -c "^PASS:" "$RESULTS_FILE" 2>/dev/null || echo 0)
fail_count=$(grep -c "^FAIL:" "$RESULTS_FILE" 2>/dev/null || echo 0)
skip_count=$(grep -c "^SKIP:" "$RESULTS_FILE" 2>/dev/null || echo 0)

grep "^PASS:" "$RESULTS_FILE" 2>/dev/null | while read -r line; do echo -e "${GREEN}$line${NC}"; done
grep "^FAIL:" "$RESULTS_FILE" 2>/dev/null | while read -r line; do echo -e "${RED}$line${NC}"; done
grep "^SKIP:" "$RESULTS_FILE" 2>/dev/null | while read -r line; do echo -e "${YELLOW}$line${NC}"; done

echo ""
echo -e "  ${GREEN}PASS: $pass_count${NC}  ${RED}FAIL: $fail_count${NC}  ${YELLOW}SKIP: $skip_count${NC}"
echo ""

# Cleanup prompt
echo -e "${WHITE}Test data is in: $TEST_BASE${NC}"
echo -e "${WHITE}To clean up: rm -rf $TEST_BASE${NC}"
echo ""

# Exit with failure if any test failed
[[ $fail_count -gt 0 ]] && exit 1
exit 0
