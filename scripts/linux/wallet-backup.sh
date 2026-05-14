#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# WALLET BACKUP
# Creates a clean wallet.dat backup for every enabled coin using the
# backupwallet RPC (safe for live daemons, works for all wallet types).
#
# Usage: sudo bash wallet-backup.sh

set -euo pipefail

INSTALL_DIR="/spiralpool"
POOL_USER="spiraluser"
BACKUP_DIR="$INSTALL_DIR/backups"
CONFIG="$INSTALL_DIR/config/config.yaml"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
SERVER_IP=$(hostname -I | awk '{print $1}')

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
WHITE='\033[1;37m'
CYAN='\033[0;36m'
NC='\033[0m'

if [[ $EUID -ne 0 ]]; then
    echo -e "${RED}Run as root: sudo bash $0${NC}"
    exit 1
fi

if [[ ! -f "$CONFIG" ]]; then
    echo -e "${RED}No config found at $CONFIG — is Spiral Pool installed?${NC}"
    exit 1
fi

mkdir -p "$BACKUP_DIR"
chown "${POOL_USER}:${POOL_USER}" "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

echo ""
echo -e "${RED}╔══════════════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${RED}║${NC}  ${WHITE}⚠  EMERGENCY WALLET BACKUP — SPIRAL POOL${NC}                              ${RED}║${NC}"
echo -e "${RED}╚══════════════════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "  This script creates a clean ${WHITE}wallet.dat${NC} backup for every enabled coin."
echo -e "  ${YELLOW}SCP every file listed below off this server before doing anything else.${NC}"
echo ""

backed_up=()
failed=()

backup_coin() {
    local coin="$1"      # e.g. dgb
    local cli="$2"       # full CLI command with -conf and -rpcwallet
    local wallet="pool-${coin}"
    local out_file="$BACKUP_DIR/wallet-${coin}-emergency-${TIMESTAMP}.dat"

    echo -ne "  Backing up ${WHITE}${coin^^}${NC}... "

    local result
    result=$(sudo -u "$POOL_USER" $cli backupwallet "$out_file" 2>&1) || true

    if echo "$result" | grep -qi "error\|unknown\|refused\|couldn't connect"; then
        # Daemon may not be running or backupwallet unsupported — try listdescriptors
        local desc_out
        desc_out=$(sudo -u "$POOL_USER" $cli listdescriptors true 2>&1) || true
        if echo "$desc_out" | grep -q '"descriptors"'; then
            out_file="$BACKUP_DIR/wallet-${coin}-emergency-${TIMESTAMP}.dump"
            printf '%s\n' "$desc_out" | sudo -u "$POOL_USER" tee "$out_file" > /dev/null
            chmod 600 "$out_file"
            echo -e "${YELLOW}✓ descriptor dump${NC} (backupwallet unavailable)"
            backed_up+=("$out_file")
        else
            echo -e "${RED}✗ FAILED${NC} — daemon not running or wallet inaccessible"
            echo -e "    ${YELLOW}Manual: sudo systemctl start ${coin}d && re-run this script${NC}"
            failed+=("$coin")
        fi
    else
        chmod 600 "$out_file"
        echo -e "${GREEN}✓ wallet.dat${NC}"
        backed_up+=("$out_file")
    fi
}

# Read enabled coins from config.yaml
ENABLE_DGB=false; ENABLE_BTC=false; ENABLE_BCH=false; ENABLE_BCH2=false
ENABLE_BC2=false; ENABLE_BTCS=false; ENABLE_NMC=false; ENABLE_SYS=false
ENABLE_XMY=false; ENABLE_FBTC=false; ENABLE_QBX=false; ENABLE_LTC=false
ENABLE_DOGE=false; ENABLE_PEP=false; ENABLE_XEC=false
# CAT is intentionally excluded: Catcoin uses an operator-provided external address,
# not a pool-managed wallet — there is no pool-cat wallet to back up.

while IFS= read -r line; do
    case "$line" in
        *"coin: digibyte"*|*"coin: \"digibyte\""*)       ENABLE_DGB=true ;;
        *"coin: bitcoin"*|*"coin: \"bitcoin\""*)          ENABLE_BTC=true ;;
        *"coin: bitcoincash"*|*"coin: \"bitcoincash\""*)  ENABLE_BCH=true ;;
        *"coin: bitcoincashii"*)                           ENABLE_BCH2=true ;;
        *"coin: bitcoinii"*)                               ENABLE_BC2=true ;;
        *"coin: bitcoinsilver"*)                           ENABLE_BTCS=true ;;
        *"coin: namecoin"*)                                ENABLE_NMC=true ;;
        *"coin: syscoin"*)                                 ENABLE_SYS=true ;;
        *"coin: myriadcoin"*)                              ENABLE_XMY=true ;;
        *"coin: fractalbitcoin"*|*"coin: fractal"*)        ENABLE_FBTC=true ;;
        *"coin: qbitx"*)                                   ENABLE_QBX=true ;;
        *"coin: litecoin"*)                                ENABLE_LTC=true ;;
        *"coin: dogecoin"*)                                ENABLE_DOGE=true ;;
        *"coin: pepecoin"*)                                ENABLE_PEP=true ;;
        *"coin: ecash"*|*"coin: xec"*)                     ENABLE_XEC=true ;;
    esac
done < "$CONFIG"

[[ "$ENABLE_DGB"  == true ]] && backup_coin "dgb"  "digibyte-cli -conf=$INSTALL_DIR/dgb/digibyte.conf -rpcwallet=pool-dgb"
[[ "$ENABLE_BTC"  == true ]] && backup_coin "btc"  "bitcoin-cli -conf=$INSTALL_DIR/btc/bitcoin.conf -rpcwallet=pool-btc"
[[ "$ENABLE_BCH"  == true ]] && backup_coin "bch"  "bitcoin-cli -conf=$INSTALL_DIR/bch/bitcoin.conf -rpcwallet=pool-bch"
[[ "$ENABLE_BCH2" == true ]] && backup_coin "bch2" "bitcoincashII-cli -conf=$INSTALL_DIR/bch2/bitcoincashii.conf -rpcwallet=pool-bch2"
[[ "$ENABLE_BC2"  == true ]] && backup_coin "bc2"  "bitcoinii-cli -conf=$INSTALL_DIR/bc2/bitcoinii.conf -rpcwallet=pool-bc2"
[[ "$ENABLE_BTCS" == true ]] && backup_coin "btcs" "bitcoinsilver-cli -conf=$INSTALL_DIR/btcs/bitcoinsilver.conf -rpcwallet=pool-btcs"
[[ "$ENABLE_NMC"  == true ]] && backup_coin "nmc"  "namecoin-cli -conf=$INSTALL_DIR/nmc/namecoin.conf -rpcwallet=pool-nmc"
[[ "$ENABLE_SYS"  == true ]] && backup_coin "sys"  "syscoin-cli -conf=$INSTALL_DIR/sys/syscoin.conf -rpcwallet=pool-sys"
[[ "$ENABLE_XMY"  == true ]] && backup_coin "xmy"  "myriadcoin-cli -conf=$INSTALL_DIR/xmy/myriadcoin.conf -rpcwallet=pool-xmy"
[[ "$ENABLE_FBTC" == true ]] && backup_coin "fbtc" "fractal-cli -conf=$INSTALL_DIR/fbtc/fractal.conf -rpcwallet=pool-fbtc"
[[ "$ENABLE_QBX"  == true ]] && backup_coin "qbx"  "qbitx-cli -conf=$INSTALL_DIR/qbx/qbitx.conf -rpcwallet=pool-qbx"
[[ "$ENABLE_LTC"  == true ]] && backup_coin "ltc"  "litecoin-cli -conf=$INSTALL_DIR/ltc/litecoin.conf -rpcwallet=pool-ltc"
[[ "$ENABLE_DOGE" == true ]] && backup_coin "doge" "dogecoin-cli -conf=$INSTALL_DIR/doge/dogecoin.conf -rpcwallet=pool-doge"
[[ "$ENABLE_PEP"  == true ]] && backup_coin "pep"  "pepecoin-cli -conf=$INSTALL_DIR/pep/pepecoin.conf -rpcwallet=pool-pep"
[[ "$ENABLE_XEC"  == true ]] && backup_coin "xec"  "ecash-cli -conf=$INSTALL_DIR/xec/bitcoin.conf"

echo ""

if [[ ${#backed_up[@]} -gt 0 ]]; then
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${WHITE}  SCP THESE FILES OFF THE SERVER NOW${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════${NC}"
    echo ""
    for f in "${backed_up[@]}"; do
        echo -e "  ${GREEN}scp ${POOL_USER}@${SERVER_IP}:${f} ./${NC}"
    done
    echo ""
    echo -e "  ${YELLOW}Store backups in at least two offline locations.${NC}"
    echo -e "  ${WHITE}To restore: stop daemon, replace wallet.dat, restart daemon.${NC}"
    echo ""
fi

if [[ ${#failed[@]} -gt 0 ]]; then
    echo -e "${RED}══════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${WHITE}  FAILED — MANUAL ACTION REQUIRED${NC}"
    echo -e "${RED}══════════════════════════════════════════════════════════════════════════${NC}"
    echo ""
    for coin in "${failed[@]}"; do
        echo -e "  ${RED}✗ ${coin^^}${NC} — start the daemon and re-run this script:"
        echo -e "    ${WHITE}sudo systemctl start ${coin}d${NC}"
    done
    echo ""
fi
