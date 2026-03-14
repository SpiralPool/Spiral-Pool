#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# wait-for-node.sh - Wait for blockchain node RPC to be ready before starting stratum
#
# This script is called by systemd ExecStartPre to ensure the blockchain daemon
# is ready to accept RPC connections before stratum attempts to connect.
#
# V2 multi-coin mode: Waits for ALL enabled coins' primary nodes to be ready.
#
# Usage: wait-for-node.sh <config_file> [max_wait_seconds]
#
# Exit codes:
#   0 - Node(s) ready
#   1 - Timeout waiting for node
#   2 - Configuration error

set -e

CONFIG_FILE="${1:-/spiralpool/config/config.yaml}"
MAX_WAIT="${2:-1800}"  # Default: 30 minutes (1800 seconds)
RETRY_INTERVAL=10      # Check every 10 seconds
LOCK_FILE="/run/spiralpool/spiralstratum-config.lock"
LOCK_FD=200

# Acquire exclusive lock on config file to prevent race conditions
# Uses flock for atomic file operations
acquire_config_lock() {
    mkdir -p "$(dirname "$LOCK_FILE")" 2>/dev/null || true
    # Fallback: if /run/spiralpool couldn't be created (ProtectSystem=strict without
    # RuntimeDirectory, or insufficient permissions), use /tmp instead.
    # PrivateTmp=yes gives each systemd service its own /tmp, so locking still works.
    if [[ ! -d "$(dirname "$LOCK_FILE")" ]]; then
        LOCK_FILE="/tmp/spiralstratum-config.lock"
    fi
    exec 200>"$LOCK_FILE"
    if ! flock -w 30 $LOCK_FD 2>/dev/null; then
        echo "  ⚠ Could not acquire config lock (timeout after 30s)"
        return 1
    fi
    return 0
}

# Release config lock
release_config_lock() {
    flock -u $LOCK_FD 2>/dev/null || true
}

# Extract a field value from YAML for a specific coin section
# More robust than inline AWK - handles various YAML formats
# Usage: get_coin_field <config_file> <symbol> <field_name>
# Returns: field value or empty string
get_coin_field() {
    local config_file="$1"
    local symbol="$2"
    local field="$3"

    # Use a more robust multi-line AWK pattern that handles:
    # - Quoted and unquoted values
    # - Various indentation levels
    # - Both 'symbol: "X"' and 'symbol: X' formats
    awk -v sym="$symbol" -v fld="$field" '
        BEGIN { in_coins=0; in_target_coin=0; indent=0 }

        # Track when we enter coins: array
        /^coins:/ { in_coins=1; next }

        # Exit coins array on new top-level key
        in_coins && /^[a-zA-Z]/ && !/^[[:space:]]/ { in_coins=0 }

        # Not in coins array, skip
        !in_coins { next }

        # New coin entry (starts with -)
        /^[[:space:]]*-[[:space:]]/ {
            # Check if this line also has symbol
            if (match($0, /symbol:[[:space:]]*["'"'"']?([^"'"'"',]+)["'"'"']?/, arr) ||
                match($0, /symbol:[[:space:]]*([^[:space:],]+)/, arr)) {
                gsub(/["'"'"']/, "", arr[1])
                in_target_coin = (toupper(arr[1]) == toupper(sym))
            } else {
                in_target_coin = 0
            }
            # Remember indent level for this coin block
            match($0, /^[[:space:]]*/)
            indent = RLENGTH
            next
        }

        # Symbol on its own line
        in_coins && /^[[:space:]]+symbol:/ {
            match($0, /symbol:[[:space:]]*["'"'"']?([^"'"'"']+)["'"'"']?/)
            val = substr($0, RSTART)
            gsub(/.*symbol:[[:space:]]*["'"'"']?/, "", val)
            gsub(/["'"'"'].*/, "", val)
            gsub(/[[:space:]]+$/, "", val)
            in_target_coin = (toupper(val) == toupper(sym))
            next
        }

        # Found target field in target coin
        in_target_coin {
            # Build regex for the field
            pattern = "^[[:space:]]+" fld ":"
            if ($0 ~ pattern) {
                val = $0
                gsub(/.*:[[:space:]]*["'"'"']?/, "", val)
                gsub(/["'"'"'][[:space:]]*$/, "", val)
                gsub(/[[:space:]]+$/, "", val)
                print val
                exit
            }
        }

        # Check if we exited the coin block (less indentation or new coin)
        in_target_coin && /^[[:space:]]*-[[:space:]]/ { in_target_coin=0 }
    ' "$config_file" 2>/dev/null
}

# Safely update a coin field in the config file
# Uses file locking to prevent race conditions
# Usage: update_coin_field <config_file> <symbol> <field> <new_value>
update_coin_field() {
    local config_file="$1"
    local symbol="$2"
    local field="$3"
    local new_value="$4"
    local tmp_file
    tmp_file=$(mktemp "${config_file}.XXXXXX") || return 1

    # Acquire lock before modifying
    if ! acquire_config_lock; then
        rm -f "$tmp_file"
        return 1
    fi

    # Use AWK to update the specific field for the specific coin
    awk -v sym="$symbol" -v fld="$field" -v newval="$new_value" '
        BEGIN { in_coins=0; in_target_coin=0 }

        /^coins:/ { in_coins=1 }
        in_coins && /^[a-zA-Z]/ && !/^[[:space:]]/ { in_coins=0 }

        # New coin entry - check if its our target
        in_coins && /^[[:space:]]*-[[:space:]]/ {
            in_target_coin = 0
        }

        # Check for symbol match
        in_coins && /symbol:/ {
            val = $0
            gsub(/.*symbol:[[:space:]]*["'"'"']?/, "", val)
            gsub(/["'"'"'].*/, "", val)
            gsub(/[[:space:]]+$/, "", val)
            if (toupper(val) == toupper(sym)) {
                in_target_coin = 1
            }
        }

        # Update the target field
        in_target_coin {
            pattern = fld ":"
            if (index($0, pattern) > 0) {
                # Preserve indentation
                match($0, /^[[:space:]]*/)
                indent = substr($0, 1, RLENGTH)
                $0 = indent fld ": \"" newval "\""
                in_target_coin = 0  # Only update first match
            }
        }

        { print }
    ' "$config_file" > "$tmp_file" 2>/dev/null

    local result=1
    if [ -s "$tmp_file" ]; then
        # Atomic move
        if mv "$tmp_file" "$config_file" 2>/dev/null; then
            result=0
        fi
    fi

    # Cleanup on failure
    [ -f "$tmp_file" ] && rm -f "$tmp_file" 2>/dev/null

    release_config_lock
    return $result
}

# Get RPC credentials from a coin's config file
# Usage: get_rpc_creds <symbol>
# Returns: "user password" or empty if not found
get_rpc_creds() {
    local symbol="$1"
    local conf_file=""
    local user="" pass=""
    local base_dir="/spiralpool"

    # Map symbol to directory and possible config file names
    # Tries multiple common config file naming conventions
    case "$symbol" in
        DGB|DGB-SCRYPT)
            # DigiByte (both SHA256d and Scrypt use same node)
            for f in "$base_dir/dgb/digibyte.conf" "$base_dir/digibyte/digibyte.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        BC2)
            # Bitcoin II - uses bitcoinii.conf (note: lowercase ii)
            for f in "$base_dir/bc2/bitcoinii.conf" "$base_dir/bc2/bitcoin.conf" "$base_dir/bitcoinii/bitcoinii.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        BTC)
            # Bitcoin Core
            for f in "$base_dir/btc/bitcoin.conf" "$base_dir/bitcoin/bitcoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        BCH)
            # Bitcoin Cash
            for f in "$base_dir/bch/bitcoin.conf" "$base_dir/bitcoincash/bitcoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        LTC)
            # Litecoin
            for f in "$base_dir/ltc/litecoin.conf" "$base_dir/litecoin/litecoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        DOGE)
            # Dogecoin
            for f in "$base_dir/doge/dogecoin.conf" "$base_dir/dogecoin/dogecoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        PEP)
            # PepeCoin
            for f in "$base_dir/pep/pepecoin.conf" "$base_dir/pepecoin/pepecoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        CAT)
            # Catcoin
            for f in "$base_dir/cat/catcoin.conf" "$base_dir/catcoin/catcoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        # Merge mining aux chains (SHA-256d compatible)
        NMC)
            # Namecoin
            for f in "$base_dir/nmc/namecoin.conf" "$base_dir/namecoin/namecoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        SYS)
            # Syscoin
            for f in "$base_dir/sys/syscoin.conf" "$base_dir/syscoin/syscoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        XMY)
            # Myriadcoin
            for f in "$base_dir/xmy/myriadcoin.conf" "$base_dir/myriadcoin/myriadcoin.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        FBTC)
            # Fractal Bitcoin
            for f in "$base_dir/fbtc/fractal.conf" "$base_dir/fractal/fractal.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        QBX)
            # Q-BitX (Post-Quantum Bitcoin)
            for f in "$base_dir/qbx/qbitx.conf" "$base_dir/qbitx/qbitx.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
        *)
            # Try to find config file by symbol name (lowercase)
            local sym_lower=$(echo "$symbol" | tr '[:upper:]' '[:lower:]')
            for f in "$base_dir/$sym_lower/$sym_lower.conf" "$base_dir/$sym_lower/*.conf"; do
                [ -f "$f" ] && { conf_file="$f"; break; }
            done
            ;;
    esac

    if [ -n "$conf_file" ] && [ -f "$conf_file" ]; then
        user=$(grep -E '^rpcuser=' "$conf_file" 2>/dev/null | cut -d= -f2)
        pass=$(grep -E '^rpcpassword=' "$conf_file" 2>/dev/null | cut -d= -f2)
    fi

    echo "$user $pass"
}

# Extract a field value from aux chain YAML section
# Usage: get_auxchain_field <config_file> <symbol> <field_name>
# Returns: field value or empty string
get_auxchain_field() {
    local config_file="$1"
    local symbol="$2"
    local field="$3"

    awk -v sym="$symbol" -v fld="$field" '
        BEGIN { in_mm=0; in_aux=0; in_target=0 }

        # Track when we enter mergeMining section
        /^[[:space:]]*mergeMining:/ { in_mm=1; next }

        # Exit on new top-level section (no leading whitespace)
        in_mm && /^[a-zA-Z]/ && !/^[[:space:]]/ { in_mm=0 }

        !in_mm { next }

        # Track auxChains array
        /auxChains:/ { in_aux=1; next }

        # New aux chain entry (starts with -)
        in_aux && /^[[:space:]]*-[[:space:]]/ {
            in_target = 0
        }

        # Check for symbol match in aux chain
        in_aux && /symbol:/ {
            val = $0
            gsub(/.*symbol:[[:space:]]*["'"'"']?/, "", val)
            gsub(/["'"'"'].*/, "", val)
            gsub(/[[:space:]]+$/, "", val)
            if (toupper(val) == toupper(sym)) {
                in_target = 1
            }
        }

        # Found target field in target aux chain
        in_target {
            pattern = fld ":"
            if (index($0, pattern) > 0) {
                val = $0
                gsub(/.*:[[:space:]]*["'"'"']?/, "", val)
                gsub(/["'"'"'][[:space:]]*$/, "", val)
                gsub(/[[:space:]]+$/, "", val)
                print val
                exit
            }
        }
    ' "$config_file" 2>/dev/null
}

# Update an aux chain field in the config file
# Usage: update_auxchain_field <config_file> <symbol> <field> <new_value>
update_auxchain_field() {
    local config_file="$1"
    local symbol="$2"
    local field="$3"
    local new_value="$4"
    local tmp_file
    tmp_file=$(mktemp "${config_file}.XXXXXX") || return 1

    if ! acquire_config_lock; then
        rm -f "$tmp_file"
        return 1
    fi

    awk -v sym="$symbol" -v fld="$field" -v newval="$new_value" '
        BEGIN { in_mm=0; in_aux=0; in_target=0 }

        /^[[:space:]]*mergeMining:/ { in_mm=1 }
        in_mm && /^[a-zA-Z]/ && !/^[[:space:]]/ { in_mm=0 }

        in_mm && /auxChains:/ { in_aux=1 }
        in_aux && /^[[:space:]]*-[[:space:]]/ { in_target=0 }

        in_aux && /symbol:/ {
            val = $0
            gsub(/.*symbol:[[:space:]]*["'"'"']?/, "", val)
            gsub(/["'"'"'].*/, "", val)
            gsub(/[[:space:]]+$/, "", val)
            if (toupper(val) == toupper(sym)) {
                in_target = 1
            }
        }

        in_target {
            pattern = fld ":"
            if (index($0, pattern) > 0) {
                match($0, /^[[:space:]]*/)
                indent = substr($0, 1, RLENGTH)
                $0 = indent fld ": \"" newval "\""
                in_target = 0
            }
        }

        { print }
    ' "$config_file" > "$tmp_file" 2>/dev/null

    local result=1
    if [ -s "$tmp_file" ]; then
        if mv "$tmp_file" "$config_file" 2>/dev/null; then
            result=0
        fi
    fi

    [ -f "$tmp_file" ] && rm -f "$tmp_file" 2>/dev/null

    release_config_lock
    return $result
}

# Extract merge mining aux chains from V2 config
# Returns one line per enabled aux chain: "SYMBOL host port user pass"
extract_v2_auxchains() {
    local config="$1"

    # Parse with awk to get aux chains from mergeMining.auxChains
    local aux_raw
    aux_raw=$(awk '
    BEGIN { in_mm=0; in_aux=0; in_chain=0; in_daemon=0; symbol=""; enabled=0; port="" }

    /^[[:space:]]*mergeMining:/ { in_mm=1; next }
    in_mm && /^[a-zA-Z]/ && !/^[[:space:]]/ { in_mm=0; in_aux=0 }
    !in_mm { next }

    /auxChains:/ { in_aux=1; next }

    in_aux && /^[[:space:]]*-[[:space:]]/ {
        if (enabled && port != "") print symbol, port
        symbol=""; enabled=0; port=""; in_chain=1; in_daemon=0
        next
    }

    in_chain && /symbol:/ {
        gsub(/.*symbol:[[:space:]]*["'"'"']?/, "")
        gsub(/["'"'"'].*/, "")
        symbol = $0
    }

    in_chain && /enabled:[[:space:]]*true/ { enabled=1 }
    in_chain && /daemon:/ { in_daemon=1 }
    in_daemon && /port:/ {
        gsub(/.*port:[[:space:]]*/, "")
        gsub(/[[:space:]].*/, "")
        port = $0
    }

    END { if (enabled && port != "") print symbol, port }
    ' "$config")

    # For each aux chain, get RPC credentials from the node's config file
    echo "$aux_raw" | while read -r symbol port; do
        [ -z "$symbol" ] && continue
        local creds
        creds=$(get_rpc_creds "$symbol")
        read -r user pass <<< "$creds"
        echo "$symbol 127.0.0.1 $port $user $pass"
    done
}

# Ensure wallet and address for an aux chain
# Args: $1 = aux chain symbol, $2 = config file
ensure_auxchain_wallet_and_address() {
    local symbol="$1"
    local config_file="$2"

    # Check if address is already configured
    local current_address
    current_address=$(get_auxchain_field "$config_file" "$symbol" "address")

    if [ -n "$current_address" ] && [ "$current_address" != '""' ] && [ "$current_address" != "PENDING_GENERATION" ]; then
        echo "    ✓ Aux chain $symbol address configured: $current_address"
        return 0
    fi

    # HA backup: try spiralpool-wallet first — it has HA address pull logic (API → SSH → local).
    # This prevents generating a divergent local address when primary's address is available.
    if command -v spiralpool-wallet >/dev/null 2>&1; then
        echo "    → Aux chain $symbol needs address — trying spiralpool-wallet (HA-aware)..."
        if spiralpool-wallet --coin "${symbol,,}" --auto 2>/dev/null; then
            # Re-check if address was written to config
            current_address=$(get_auxchain_field "$config_file" "$symbol" "address")
            if [ -n "$current_address" ] && [ "$current_address" != '""' ] && [ "$current_address" != "PENDING_GENERATION" ]; then
                echo "    ✓ Aux chain $symbol address set via spiralpool-wallet: $current_address"
                return 0
            fi
        fi
        echo "    → spiralpool-wallet did not resolve $symbol — falling back to local generation..."
    fi

    echo "    → Aux chain $symbol needs address generation..."

    local cli_path=$(get_cli_path "$symbol")
    local conf_path=$(get_conf_path "$symbol")

    if [ ! -x "$cli_path" ]; then
        echo "    ⚠ CLI not found for $symbol: $cli_path"
        return 1
    fi

    if [ ! -f "$conf_path" ]; then
        echo "    ⚠ Node config not found for $symbol: $conf_path"
        return 1
    fi

    local wallet_name="pool-${symbol,,}"

    local wallets
    wallets=$("$cli_path" -conf="$conf_path" listwallets 2>&1) || wallets="[]"
    local wallets_clean
    wallets_clean=$(echo "$wallets" | tr -d '[:space:]')

    if echo "$wallets_clean" | grep -q "\"$wallet_name\""; then
        echo "    ✓ Wallet '$wallet_name' already loaded"
    else
        echo "    → Attempting to load wallet '$wallet_name'..."
        local load_result
        load_result=$("$cli_path" -conf="$conf_path" loadwallet "$wallet_name" 2>&1) || true

        if echo "$load_result" | grep -q '"name"'; then
            echo "    ✓ Wallet '$wallet_name' loaded"
        elif echo "$load_result" | grep -qi "already loaded"; then
            echo "    ✓ Wallet '$wallet_name' was already loaded"
        elif echo "$load_result" | grep -qi "not found\|does not exist"; then
            echo "    → Creating wallet '$wallet_name'..."
            local create_result
            create_result=$("$cli_path" -conf="$conf_path" createwallet "$wallet_name" 2>&1) || true

            if echo "$create_result" | grep -q '"name"'; then
                echo "    ✓ Wallet '$wallet_name' created for aux chain $symbol"
                local wallet_dir=$(get_wallet_dir "$symbol")
                echo ""
                echo "    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "    IMPORTANT: Aux Chain Wallet Created - Please Back Up!"
                echo "    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "    Wallet location: $wallet_dir/$wallet_name"
                echo "    Coin: $symbol (Merge Mining Aux Chain)"
                echo ""
            else
                echo "    ✗ Failed to create wallet: $create_result"
                return 1
            fi
        else
            echo "    ⚠ Wallet load returned: $load_result"
        fi
    fi

    # Generate address
    local new_address
    local use_addr_type=true

    case "$symbol" in
        NMC|SYS)
            # These support bech32
            new_address=$("$cli_path" -conf="$conf_path" getnewaddress "$wallet_name" "bech32" 2>&1) || true
            ;;
        XMY|FBTC)
            # These use legacy addresses
            use_addr_type=false
            new_address=$("$cli_path" -conf="$conf_path" getnewaddress "$wallet_name" 2>&1) || true
            ;;
        QBX)
            # Q-BitX uses post-quantum "pq" address type
            new_address=$("$cli_path" -conf="$conf_path" getnewaddress "" "pq" 2>&1) || true
            ;;
        *)
            use_addr_type=false
            new_address=$("$cli_path" -conf="$conf_path" getnewaddress "$wallet_name" 2>&1) || true
            ;;
    esac

    if [ -n "$new_address" ] && [[ "$new_address" =~ ^[a-zA-Z0-9]{20,90}$ ]]; then
        echo "    ✓ Generated aux chain address: $new_address"

        if update_auxchain_field "$config_file" "$symbol" "address" "$new_address"; then
            echo "    ✓ Updated config with new aux chain address for $symbol"
        else
            echo "    ⚠ Could not write to config. Please add address manually for $symbol:"
            echo "      address: \"$new_address\""
        fi
    else
        echo "    ✗ Failed to generate address for aux chain $symbol: $new_address"
        return 1
    fi

    return 0
}

# Extract ALL enabled coins' primary node info from V2 config
# Supports:
#   1. Full format: coins with 'nodes:' array containing host/port/user/pass
#   2. Simple format: coins with 'daemon:' containing just port (reads creds from node conf)
# Returns one line per enabled coin: "SYMBOL host port user pass"
extract_v2_nodes() {
    local config="$1"

    # Parse with awk to get symbol and daemon port for each enabled coin
    local coins_raw
    coins_raw=$(awk '
    BEGIN { in_coins=0; in_coin=0; in_daemon=0; in_stratum=0; symbol=""; enabled=0; port="" }

    /^coins:/ { in_coins=1; next }
    /^[a-z]+:/ && !/^coins:/ && !/^-/ { in_coins=0; in_coin=0 }
    !in_coins { next }

    /^-[[:space:]]+symbol:/ {
        if (enabled && port != "") print symbol, port
        symbol=$NF; gsub(/["'\'']/, "", symbol)
        enabled=0; port=""; in_daemon=0; in_stratum=0; in_coin=1
        next
    }

    in_coin && /^[[:space:]]+enabled:[[:space:]]*true/ { enabled=1 }
    in_coin && /^[[:space:]]+daemon:/ { in_daemon=1; in_stratum=0 }
    in_coin && /^[[:space:]]+stratum:/ { in_stratum=1; in_daemon=0 }
    in_daemon && /^[[:space:]]+port:/ { port=$NF; gsub(/["'\'']/, "", port) }

    END { if (enabled && port != "") print symbol, port }
    ' "$config")

    # For each coin, get RPC credentials from the node's config file
    echo "$coins_raw" | while read -r symbol port; do
        [ -z "$symbol" ] && continue
        local creds
        creds=$(get_rpc_creds "$symbol")
        read -r user pass <<< "$creds"
        echo "$symbol 127.0.0.1 $port $user $pass"
    done
}

# Extract daemon connection info from config (V1 single-coin format)
extract_v1_daemon() {
    local config="$1"
    local host="" port="" user="" pass=""

    # Try V1 format (top-level daemon: section)
    host=$(grep -A10 '^daemon:' "$config" 2>/dev/null | grep 'host:' | head -1 | awk '{print $2}' | tr -d '"')
    port=$(grep -A10 '^daemon:' "$config" 2>/dev/null | grep 'port:' | head -1 | awk '{print $2}' | tr -d '"')
    user=$(grep -A10 '^daemon:' "$config" 2>/dev/null | grep 'user:' | head -1 | awk '{print $2}' | tr -d '"')
    pass=$(grep -A10 '^daemon:' "$config" 2>/dev/null | grep 'password:' | head -1 | awk '{print $2}' | tr -d '"')

    if [ -n "$host" ] && [ -n "$port" ]; then
        echo "V1 $host $port $user $pass"
    fi
}

# Check if RPC is responding AND blockchain is synced
check_rpc() {
    local host="$1"
    local port="$2"
    local user="$3"
    local pass="$4"

    # RPC call to getblockchaininfo
    local response
    response=$(curl -sf --max-time 10 \
        -u "$user:$pass" \
        -X POST \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"1.0","id":"wait","method":"getblockchaininfo","params":[]}' \
        "http://${host}:${port}/" 2>/dev/null) || return 1

    # Check if we got a valid response (has "result" field)
    if ! echo "$response" | grep -q '"result"'; then
        return 1
    fi

    # Check if blockchain sync is complete (initialblockdownload must be false)
    local ibd
    ibd=$(echo "$response" | grep -o '"initialblockdownload":[^,}]*' | cut -d: -f2 | tr -d ' ')
    if [ "$ibd" = "true" ]; then
        # Still syncing — extract progress for logging
        local progress
        progress=$(echo "$response" | grep -o '"verificationprogress":[^,}]*' | cut -d: -f2)
        if [ -n "$progress" ]; then
            local pct
            pct=$(echo "$progress" | awk '{printf "%.2f", $1 * 100}')
            echo "  Blockchain syncing: ${pct}% complete (initialblockdownload=true)" >&2
        fi
        return 1
    fi

    return 0
}

# Get the CLI binary path for a coin
# NOTE: DGB/BTC/BCH/BC2 store binaries in their data dir ($coin/bin/)
#       All others use separate binary dirs ($coin-bin/bin/) per install.sh
get_cli_path() {
    local symbol="$1"
    local base_dir="/spiralpool"

    case "$symbol" in
        # Coins with binaries in data directory
        DGB|DGB-SCRYPT)
            echo "$base_dir/dgb/bin/digibyte-cli"
            ;;
        BTC)
            echo "$base_dir/btc/bin/bitcoin-cli"
            ;;
        BCH)
            echo "$base_dir/bch/bin/bitcoin-cli"
            ;;
        BC2)
            echo "$base_dir/bc2/bin/bitcoinII-cli"
            ;;
        # Coins with separate binary directories (*-bin)
        LTC)
            echo "$base_dir/ltc-bin/bin/litecoin-cli"
            ;;
        DOGE)
            echo "$base_dir/doge-bin/bin/dogecoin-cli"
            ;;
        PEP)
            echo "$base_dir/pep-bin/bin/pepecoin-cli"
            ;;
        CAT)
            echo "$base_dir/cat-bin/bin/catcoin-cli"
            ;;
        # Merge mining aux chains (separate binary directories)
        NMC)
            echo "$base_dir/nmc-bin/bin/namecoin-cli"
            ;;
        SYS)
            echo "$base_dir/sys-bin/bin/syscoin-cli"
            ;;
        XMY)
            echo "$base_dir/xmy-bin/bin/myriadcoin-cli"
            ;;
        FBTC)
            # Fractal release ships bitcoin-cli; fractal-cli is a symlink in /usr/local/bin only
            echo "$base_dir/fbtc-bin/bin/bitcoin-cli"
            ;;
        QBX)
            echo "$base_dir/qbx-bin/qbitx-cli"
            ;;
    esac
}

# Get the wallet data directory for a coin (for user notification)
get_wallet_dir() {
    local symbol="$1"
    local base_dir="/spiralpool"

    case "$symbol" in
        DGB|DGB-SCRYPT) echo "$base_dir/dgb/wallets" ;;
        BC2) echo "$base_dir/bc2/wallets" ;;
        BTC) echo "$base_dir/btc/wallets" ;;
        BCH) echo "$base_dir/bch/wallets" ;;
        LTC) echo "$base_dir/ltc/wallets" ;;
        DOGE) echo "$base_dir/doge/wallets" ;;
        PEP) echo "$base_dir/pep/wallets" ;;
        CAT) echo "$base_dir/cat/wallets" ;;
        NMC) echo "$base_dir/nmc/wallets" ;;
        SYS) echo "$base_dir/sys/wallets" ;;
        XMY) echo "$base_dir/xmy/wallets" ;;
        FBTC) echo "$base_dir/fbtc/wallets" ;;
        QBX) echo "$base_dir/qbx/wallets" ;;
        *) echo "$base_dir/${symbol,,}/wallets" ;;
    esac
}

# Get the config file path for a coin
get_conf_path() {
    local symbol="$1"
    local base_dir="/spiralpool"

    case "$symbol" in
        DGB|DGB-SCRYPT)
            echo "$base_dir/dgb/digibyte.conf"
            ;;
        BC2)
            echo "$base_dir/bc2/bitcoinii.conf"
            ;;
        BTC)
            echo "$base_dir/btc/bitcoin.conf"
            ;;
        BCH)
            echo "$base_dir/bch/bitcoin.conf"
            ;;
        LTC)
            echo "$base_dir/ltc/litecoin.conf"
            ;;
        DOGE)
            echo "$base_dir/doge/dogecoin.conf"
            ;;
        PEP)
            echo "$base_dir/pep/pepecoin.conf"
            ;;
        CAT)
            echo "$base_dir/cat/catcoin.conf"
            ;;
        # Merge mining aux chains (SHA-256d)
        NMC)
            echo "$base_dir/nmc/namecoin.conf"
            ;;
        SYS)
            echo "$base_dir/sys/syscoin.conf"
            ;;
        XMY)
            echo "$base_dir/xmy/myriadcoin.conf"
            ;;
        FBTC)
            echo "$base_dir/fbtc/fractal.conf"
            ;;
        QBX)
            echo "$base_dir/qbx/qbitx.conf"
            ;;
    esac
}

# Check if address is configured, only create wallet if address generation is needed
# Args: $1 = coin symbol, $2 = config file, $3 = mode ("v1" or "v2")
ensure_wallet_and_address() {
    local symbol="$1"
    local config_file="$2"
    local mode="${3:-v1}"

    # FIRST: Check if address is already configured in stratum config
    # If user provided an address, we don't need to touch wallets at all
    local current_address
    if [ "$mode" = "v2" ]; then
        current_address=$(get_coin_field "$config_file" "$symbol" "address")
    else
        current_address=$(grep -E '^\s*address:' "$config_file" 2>/dev/null | head -1 | sed 's/.*address:\s*["'\'']\?\([^"'\'']*\)["'\'']\?.*/\1/' | tr -d ' ')
    fi

    # If address is configured and valid, we're done - no wallet needed
    if [ -n "$current_address" ] && [ "$current_address" != '""' ] && [ "$current_address" != "PENDING_GENERATION" ]; then
        echo "  ✓ Pool address configured: $current_address"
        return 0
    fi

    # HA backup: try spiralpool-wallet first — it has HA address pull logic (API → SSH → local).
    # This prevents generating a divergent local address when primary's address is available.
    if command -v spiralpool-wallet >/dev/null 2>&1; then
        echo "  → No address configured — trying spiralpool-wallet (HA-aware)..."
        if spiralpool-wallet --coin "${symbol,,}" --auto 2>/dev/null; then
            # Re-check if address was written to config
            if [ "$mode" = "v2" ]; then
                current_address=$(get_coin_field "$config_file" "$symbol" "address")
            else
                current_address=$(grep -E '^\s*address:' "$config_file" 2>/dev/null | head -1 | sed 's/.*address:\s*["'\'']\?\([^"'\'']*\)["'\'']\?.*/\1/' | tr -d ' ')
            fi
            if [ -n "$current_address" ] && [ "$current_address" != '""' ] && [ "$current_address" != "PENDING_GENERATION" ]; then
                echo "  ✓ Address set via spiralpool-wallet: $current_address"
                return 0
            fi
        fi
        echo "  → spiralpool-wallet did not resolve $symbol — falling back to local generation..."
    fi

    # No address configured - need to generate one, which requires wallet setup
    echo "  → No pool address configured, will generate one..."

    local cli_path=$(get_cli_path "$symbol")
    local conf_path=$(get_conf_path "$symbol")

    # Check if CLI exists
    if [ ! -x "$cli_path" ]; then
        echo "  ⚠ CLI not found for $symbol: $cli_path"
        echo "  ⚠ Cannot auto-generate address. Please configure address manually in config."
        return 1
    fi

    # Check if conf exists
    if [ ! -f "$conf_path" ]; then
        echo "  ⚠ Node config not found for $symbol: $conf_path"
        echo "  ⚠ Cannot auto-generate address. Please configure address manually in config."
        return 1
    fi

    # Need a wallet to generate address - try to load or create one
    # Use coin-specific wallet name to match install.sh convention (e.g., pool-bc2, pool-btc)
    local wallet_name="pool-${symbol,,}"

    local wallets
    wallets=$("$cli_path" -conf="$conf_path" listwallets 2>&1) || wallets="[]"
    local wallets_clean
    wallets_clean=$(echo "$wallets" | tr -d '[:space:]')

    # Check if coin-specific wallet is already loaded
    if echo "$wallets_clean" | grep -q "\"$wallet_name\""; then
        echo "  ✓ Wallet '$wallet_name' already loaded for $symbol"
    else
        # Wallet not loaded - try to load it first (it may exist on disk from previous run)
        echo "  → Wallet not loaded, attempting to load '$wallet_name'..."
        local load_result
        load_result=$("$cli_path" -conf="$conf_path" loadwallet "$wallet_name" 2>&1) || true

        if echo "$load_result" | grep -q '"name"'; then
            echo "  ✓ Wallet '$wallet_name' loaded for $symbol"
        elif echo "$load_result" | grep -qi "already loaded"; then
            echo "  ✓ Wallet '$wallet_name' was already loaded for $symbol"
        elif echo "$load_result" | grep -qi "not found\|does not exist"; then
            # Wallet doesn't exist - create it
            echo "  → Wallet does not exist, creating '$wallet_name'..."
            local create_result
            create_result=$("$cli_path" -conf="$conf_path" createwallet "$wallet_name" 2>&1) || true

            if echo "$create_result" | grep -q '"name"'; then
                echo "  ✓ Wallet '$wallet_name' created for $symbol"
                local wallet_dir=$(get_wallet_dir "$symbol")
                echo ""
                echo "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "  IMPORTANT: Wallet Created - Please Back Up!"
                echo "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "  Wallet location: $wallet_dir/$wallet_name"
                echo ""
                echo "  This wallet contains your pool's private keys."
                echo "  Back up the wallet directory to prevent loss of funds!"
                echo ""
            else
                echo "  ✗ Failed to create wallet for $symbol: $create_result"
                echo ""
                echo "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "  WALLET GENERATION FAILED - Manual Action Required"
                echo "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "  The automatic wallet generation for $symbol failed."
                echo ""
                echo "  Please generate a wallet address externally using:"
                echo "    1. The coin's official wallet (desktop or mobile)"
                echo "    2. A hardware wallet (Ledger, Trezor)"
                echo "    3. The daemon CLI: $(get_cli_path $symbol) getnewaddress"
                echo ""
                echo "  Then update the config file with the address:"
                echo "    $config_file"
                echo ""
                return 1
            fi
        else
            # Some other error - log it but don't fail (wallet might still work)
            echo "  ⚠ Wallet load returned: $load_result"
            echo "  → Continuing anyway (wallet may still be usable)"
        fi
    fi

    # DGB-SCRYPT special case: reuse DGB address if available
    if [ "$mode" = "v2" ] && [ "$symbol" = "DGB-SCRYPT" ]; then
        local dgb_address
        dgb_address=$(get_coin_field "$config_file" "DGB" "address")
        if [ -n "$dgb_address" ] && [ "$dgb_address" != "PENDING_GENERATION" ] && [ "$dgb_address" != '""' ]; then
            echo "  → DGB-SCRYPT uses same blockchain as DGB, reusing DGB address"
            if update_coin_field "$config_file" "DGB-SCRYPT" "address" "$dgb_address"; then
                echo "  ✓ Updated config with DGB address for DGB-SCRYPT: $dgb_address"
            else
                echo "  ⚠ Could not update config. Please set DGB-SCRYPT address to: $dgb_address"
            fi
            return 0
        fi
    fi

    # Generate new address with appropriate type for each coin
    # Address type parameter support:
    #   BTC, LTC, DGB, NMC, SYS - support "bech32" (SegWit native)
    #   BCH - NO address_type param (no SegWit, uses CashAddr legacy)
    #   DOGE - NO address_type param (no SegWit, legacy D... addresses)
    #   BC2 - NO address_type param (older Bitcoin fork)
    #   XMY, FBTC - may or may not support, try legacy for safety
    #   QBX - uses post-quantum "pq" address type (handled in separate case above)
    local new_address
    local addr_type=""
    local use_addr_type=true

    case "$symbol" in
        BTC|LTC|DGB|DGB-SCRYPT|NMC|SYS)
            # Modern Bitcoin-derived daemons with SegWit support
            # NMC: Namecoin supports SegWit (nc1q... addresses)
            # SYS: Syscoin supports SegWit (sys1q... addresses)
            addr_type="bech32"
            ;;
        BCH|DOGE|BC2|PEP|CAT|XMY|FBTC|QBX)
            # These daemons do NOT support standard address_type parameter:
            # - BCH: No SegWit (rejected it)
            # - DOGE: No SegWit implemented
            # - BC2: Older Bitcoin fork
            # - PEP/CAT: Legacy Scrypt coins without SegWit
            # - XMY: Myriad uses legacy addresses (M...)
            # - FBTC: Fractal Bitcoin - use legacy for compatibility
            # - QBX: Uses "pq" post-quantum address type (handled separately)
            use_addr_type=false
            ;;
        *)
            # Unknown coin - try without address type for safety
            use_addr_type=false
            ;;
    esac

    if [ "$use_addr_type" = true ] && [ -n "$addr_type" ]; then
        new_address=$("$cli_path" -conf="$conf_path" getnewaddress "$wallet_name" "$addr_type" 2>&1) || true
    else
        # Daemons without address_type support: getnewaddress [label]
        new_address=$("$cli_path" -conf="$conf_path" getnewaddress "$wallet_name" 2>&1) || true
    fi

    if [ -n "$new_address" ] && [[ "$new_address" =~ ^[a-zA-Z0-9]{20,90}$ ]]; then
        echo "  ✓ Generated address: $new_address"

        # Update the config file based on mode
        if [ "$mode" = "v2" ]; then
            # V2: Use safe helper function with file locking
            if update_coin_field "$config_file" "$symbol" "address" "$new_address"; then
                echo "  ✓ Updated $config_file with new address for $symbol"
            else
                echo "  ⚠ Could not write to config. Please add address manually for $symbol:"
                echo "    address: \"$new_address\""
            fi
        else
            # V1: Replace first address field under pool: section
            # Handle various formats: address: "", address: "PENDING_GENERATION", address: "placeholder..."
            local updated=false

            # Try to replace any address value (empty, placeholder, or PENDING)
            if grep -q '^\s*address:\s*"' "$config_file" 2>/dev/null; then
                # Replace the first occurrence of address: "..." with new address
                if sed -i '0,/^\(\s*address:\s*\)"[^"]*"/s//\1"'"$new_address"'"/' "$config_file" 2>/dev/null; then
                    # Verify the change was made
                    if grep -q "address:.*\"$new_address\"" "$config_file" 2>/dev/null; then
                        echo "  ✓ Updated $config_file with new address"
                        updated=true
                    fi
                fi
            fi

            if [ "$updated" = false ]; then
                echo "  ⚠ Could not auto-update config. Please add address manually:"
                echo "    address: \"$new_address\""
                echo ""
                echo "  You can run this command to update the config:"
                echo "    sed -i 's|address: \"[^\"]*\"|address: \"$new_address\"|' $config_file"
            fi
        fi
    else
        echo "  ✗ Failed to generate address for $symbol: $new_address"
        return 1
    fi

    return 0
}

# Extract coin symbol from V1 config
extract_v1_coin() {
    local config="$1"
    # Get coin from pool.coin field
    grep -A5 '^pool:' "$config" 2>/dev/null | grep 'coin:' | head -1 | awk '{print $2}' | tr -d '"' | tr -d "'"
}

# Check all nodes and return status
# Returns: "all_ready", "some_ready", or "none_ready"
check_all_nodes() {
    local ready_count=0
    local total_count=0

    while IFS=' ' read -r symbol host port user pass; do
        [ -z "$symbol" ] && continue
        total_count=$((total_count + 1))
        if check_rpc "$host" "$port" "$user" "$pass"; then
            echo "  ✓ $symbol node ready ($host:$port)"
            ready_count=$((ready_count + 1))
        else
            echo "  ✗ $symbol node not ready ($host:$port)"
        fi
    done

    if [ "$ready_count" -eq "$total_count" ] && [ "$total_count" -gt 0 ]; then
        echo "all_ready"
    elif [ "$ready_count" -gt 0 ]; then
        echo "some_ready"
    else
        echo "none_ready"
    fi
}

# Process all wallets for V2 config (main coins + aux chains)
# This function is called after all nodes are ready
process_v2_wallets() {
    local config_file="$1"
    local nodes="$2"

    echo "Checking wallets and addresses for all coins..."
    echo "$nodes" | while IFS=' ' read -r symbol host port user pass; do
        [ -z "$symbol" ] && continue
        echo "  Coin: $symbol"
        # Wallet failures are non-fatal — stratum should still start if addresses are already configured
        ensure_wallet_and_address "$symbol" "$config_file" "v2" || echo "  WARNING: Wallet setup failed for $symbol (non-fatal, address may already be configured)"
    done

    # Process merge mining aux chains
    local aux_nodes
    aux_nodes=$(extract_v2_auxchains "$config_file")
    if [ -n "$aux_nodes" ]; then
        echo ""
        echo "Checking wallets and addresses for merge mining aux chains..."
        echo "$aux_nodes" | while IFS=' ' read -r symbol host port user pass; do
            [ -z "$symbol" ] && continue
            echo "  Aux Chain: $symbol"
            ensure_auxchain_wallet_and_address "$symbol" "$config_file" || echo "  WARNING: Aux wallet setup failed for $symbol (non-fatal)"
        done
    fi
}

# Process wallets for V1 config (main coin + aux chains)
# V1 has merge mining at top level, not nested under coin
process_v1_wallets() {
    local coin_symbol="$1"
    local config_file="$2"

    echo "Checking wallet and address..."
    # Wallet failure for main coin is non-fatal — stratum logs its own address check
    ensure_wallet_and_address "$coin_symbol" "$config_file" || echo "  WARNING: Wallet setup failed for $coin_symbol (non-fatal)"

    # Check for merge mining aux chains in V1 config (top-level mergeMining:)
    local aux_nodes
    aux_nodes=$(extract_v2_auxchains "$config_file")
    if [ -n "$aux_nodes" ]; then
        echo ""
        echo "Checking wallets and addresses for merge mining aux chains..."
        echo "$aux_nodes" | while IFS=' ' read -r symbol host port user pass; do
            [ -z "$symbol" ] && continue
            echo "  Aux Chain: $symbol"
            ensure_auxchain_wallet_and_address "$symbol" "$config_file" || echo "  WARNING: Aux wallet setup failed for $symbol (non-fatal)"
        done
    fi
}

# Main
echo "Waiting for blockchain node RPC to be ready..."
echo "Config: $CONFIG_FILE"
echo "Max wait: ${MAX_WAIT}s"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "ERROR: Config file not found: $CONFIG_FILE"
    exit 2
fi

# Detect V1 vs V2 config format
# IMPORTANT: Check V2 first - if a config has both 'coins:' array AND top-level 'daemon:',
# we should use V2 mode as it's more specific (multi-coin takes precedence)
V2_NODES=$(extract_v2_nodes "$CONFIG_FILE")
V1_INFO=$(extract_v1_daemon "$CONFIG_FILE")

if [ -n "$V2_NODES" ]; then
    # V2 mode: multi-coin (takes precedence over V1 if both exist)
    echo "Detected V2 (multi-coin) configuration"
    echo "Enabled coins:"
    echo "$V2_NODES" | while IFS=' ' read -r symbol host port user pass; do
        echo "  - $symbol ($host:$port)"
    done

    # Quick check - all nodes already up?
    echo ""
    echo "Checking node status..."
    result=$(echo "$V2_NODES" | check_all_nodes | tail -1)
    if [ "$result" = "all_ready" ]; then
        echo "All nodes are ready! (hot restart)"
        echo ""
        process_v2_wallets "$CONFIG_FILE" "$V2_NODES"
        exit 0
    fi

    # Quick checks (5 seconds)
    echo ""
    echo "Nodes not immediately available, performing quick checks..."
    for i in 1 2 3 4 5; do
        sleep 1
        result=$(echo "$V2_NODES" | check_all_nodes | tail -1)
        if [ "$result" = "all_ready" ]; then
            echo "All nodes are ready! (waited ${i}s)"
            echo ""
            process_v2_wallets "$CONFIG_FILE" "$V2_NODES"
            exit 0
        fi
    done

    # Slow polling
    echo ""
    echo "Nodes still starting, switching to ${RETRY_INTERVAL}s polling..."
    elapsed=5
    while [ $elapsed -lt $MAX_WAIT ]; do
        echo ""
        echo "Checking nodes... (${elapsed}s / ${MAX_WAIT}s)"
        result=$(echo "$V2_NODES" | check_all_nodes | tail -1)
        if [ "$result" = "all_ready" ]; then
            echo "All nodes are ready! (waited ${elapsed}s)"
            echo ""
            process_v2_wallets "$CONFIG_FILE" "$V2_NODES"
            exit 0
        fi
        sleep $RETRY_INTERVAL
        elapsed=$((elapsed + RETRY_INTERVAL))
    done

    echo "ERROR: Timeout waiting for all nodes after ${MAX_WAIT}s"
    exit 1

elif [ -n "$V1_INFO" ]; then
    # V1 mode: single coin (fallback if no V2 coins array found)
    echo "Detected V1 (single-coin) configuration"
    read -r _ HOST PORT USER PASS <<< "$V1_INFO"
    echo "Node: $HOST:$PORT"

    # Get the coin symbol for wallet setup
    COIN_RAW=$(extract_v1_coin "$CONFIG_FILE")
    # Normalize coin name to symbol
    case "${COIN_RAW,,}" in
        digibyte|dgb) COIN_SYMBOL="DGB" ;;
        digibyte-scrypt|dgb-scrypt) COIN_SYMBOL="DGB-SCRYPT" ;;
        bitcoin|btc) COIN_SYMBOL="BTC" ;;
        bitcoincash|bitcoin-cash|bch) COIN_SYMBOL="BCH" ;;
        bitcoinii|bitcoin-ii|bitcoin2|bc2) COIN_SYMBOL="BC2" ;;
        litecoin|ltc) COIN_SYMBOL="LTC" ;;
        dogecoin|doge) COIN_SYMBOL="DOGE" ;;
        pepecoin|pep) COIN_SYMBOL="PEP" ;;
        catcoin|cat) COIN_SYMBOL="CAT" ;;
        namecoin|nmc) COIN_SYMBOL="NMC" ;;
        syscoin|sys) COIN_SYMBOL="SYS" ;;
        myriadcoin|myriad|xmy) COIN_SYMBOL="XMY" ;;
        fractalbitcoin|fractal|fbtc) COIN_SYMBOL="FBTC" ;;
        qbitx|q-bitx|qbx) COIN_SYMBOL="QBX" ;;
        *) COIN_SYMBOL="${COIN_RAW^^}" ;;
    esac
    echo "Coin: $COIN_SYMBOL"

    # Quick check first
    if check_rpc "$HOST" "$PORT" "$USER" "$PASS"; then
        echo "Node RPC is already ready! (hot restart)"
        echo ""
        process_v1_wallets "$COIN_SYMBOL" "$CONFIG_FILE"
        exit 0
    fi

    # Quick checks
    echo "Node not immediately available, performing quick checks..."
    for i in 1 2 3 4 5; do
        sleep 1
        if check_rpc "$HOST" "$PORT" "$USER" "$PASS"; then
            echo "Node RPC is ready! (waited ${i}s)"
            echo ""
            process_v1_wallets "$COIN_SYMBOL" "$CONFIG_FILE"
            exit 0
        fi
    done

    # Slow polling
    echo "Node still starting, switching to ${RETRY_INTERVAL}s polling..."
    elapsed=5
    while [ $elapsed -lt $MAX_WAIT ]; do
        if check_rpc "$HOST" "$PORT" "$USER" "$PASS"; then
            echo "Node RPC is ready! (waited ${elapsed}s)"
            echo ""
            process_v1_wallets "$COIN_SYMBOL" "$CONFIG_FILE"
            exit 0
        fi
        echo "Node not ready yet... (${elapsed}s / ${MAX_WAIT}s)"
        sleep $RETRY_INTERVAL
        elapsed=$((elapsed + RETRY_INTERVAL))
    done

    echo "ERROR: Timeout waiting for node RPC after ${MAX_WAIT}s"
    exit 1

else
    echo "ERROR: Could not extract daemon/node info from config"
    echo "Config must have either:"
    echo "  - V1 format: top-level 'daemon:' section"
    echo "  - V2 format: 'coins:' array with 'daemon:' or 'nodes:' sections"
    exit 2
fi
