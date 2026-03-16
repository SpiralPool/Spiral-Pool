#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool Miner Scanner - Standalone version
# Discover and manage mining devices on your network
#
# Usage:
#   ./rescan-miners.sh                    - Scan local subnet for miners
#   ./rescan-miners.sh 192.168.1.0/24     - Scan specific subnet
#   ./rescan-miners.sh --reset            - Delete database and rescan (removes duplicates)
#   ./rescan-miners.sh --clear            - Just delete the database (no scan)
#   ./rescan-miners.sh --list             - List all known miners
#   ./rescan-miners.sh --add 192.168.1.50 - Manually add a miner
#   ./rescan-miners.sh --remove 192.168.1.50 - Remove a miner
#   ./rescan-miners.sh --nickname 192.168.1.50 'My BitAxe' - Set nickname
#

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
MAGENTA='\033[0;35m'
DIM='\033[2m'
NC='\033[0m'

# Standard pool service account (matches install.sh)
POOL_USER="spiraluser"

# Shared location - accessible by both admin user and dashboard service
MINER_DB="/spiralpool/data/miners.json"
DASHBOARD_CONFIG="$HOME/.spiralpool/dashboard_config.json"

# Miner detection ports
AXEOS_PORT=80       # BitAxe, NerdQAxe, Hammer, Goldshell (HTTP API)
CGMINER_PORT=4028   # Antminer, Whatsminer, Avalon, Innosilicon, Elphapex

show_banner() {
    echo ""
    echo -e "${CYAN}╔════════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║${NC}                                                                    ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                  ${WHITE}SPIRAL POOL MINER SCANNER${NC}                         ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}             Discover mining devices on your network                ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                    ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}   ${DIM}26 device types: SHA-256d + Scrypt (AxeOS, CGMiner, HTTP)${NC}        ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                    ${CYAN}║${NC}"
    echo -e "${CYAN}╚════════════════════════════════════════════════════════════════════╝${NC}"
    echo ""
}

show_usage() {
    echo ""
    echo -e "${WHITE}Usage:${NC}"
    echo "  $0                         - Scan local subnet for miners"
    echo "  $0 192.168.1.0/24          - Scan specific subnet"
    echo "  $0 --reset                 - Delete database and rescan (fix duplicates)"
    echo "  $0 --clear                 - Delete database only (no scan)"
    echo "  $0 --list                  - List all known miners"
    echo "  $0 --add 192.168.1.50      - Manually add a miner"
    echo "  $0 --remove 192.168.1.50   - Remove a miner"
    echo "  $0 --nickname 192.168.1.50 'My BitAxe' - Set nickname"
    echo ""
    echo -e "${WHITE}Database Location:${NC}"
    echo "  $MINER_DB"
    echo ""
}

get_local_subnet() {
    # WSL2: auto-detection only sees the WSL NAT adapter, not the real Windows LAN.
    # Prompt the user to enter their subnet manually.
    if grep -qi "microsoft\|wsl" /proc/version 2>/dev/null; then
        echo -e "${YELLOW}WSL2 detected.${NC} Auto-detection cannot see your Windows LAN subnet." >&2
        echo -e "" >&2
        echo -e "${WHITE}Before scanning, make sure Windows Firewall allows outbound connections from WSL2:${NC}" >&2
        echo -e "  1. Open ${WHITE}Windows Defender Firewall${NC} > ${WHITE}Advanced Settings${NC}" >&2
        echo -e "  2. Go to ${WHITE}Outbound Rules${NC} > ${WHITE}New Rule${NC}" >&2
        echo -e "  3. Select ${WHITE}Port${NC}, then allow TCP ports ${WHITE}80, 4028, 4029, 8080${NC}" >&2
        echo -e "  4. Apply to ${WHITE}All profiles${NC} and save" >&2
        echo -e "" >&2
        echo -e "Enter your subnet to scan (e.g. ${WHITE}192.168.1.0/24${NC}): " >&2
        local user_subnet
        read -r user_subnet </dev/tty
        if [[ "$user_subnet" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}/[0-9]{1,2}$ ]]; then
            echo "$user_subnet"
        else
            echo -e "${RED}Invalid subnet format. Expected format: 192.168.1.0/24${NC}" >&2
            echo ""
        fi
        return
    fi

    # Get the local subnet (e.g., 192.168.1.0/24)
    local ip=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)192\.168\.\d+\.\d+' | head -1)
    if [[ -z "$ip" ]]; then
        ip=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)10\.\d+\.\d+\.\d+' | head -1)
    fi
    if [[ -z "$ip" ]]; then
        ip=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)172\.(1[6-9]|2[0-9]|3[0-1])\.\d+\.\d+' | head -1)
    fi
    # Fallback for systems without ip command (macOS)
    if [[ -z "$ip" ]]; then
        ip=$(ifconfig 2>/dev/null | grep -oE '192\.168\.[0-9]+\.[0-9]+' | head -1)
    fi
    if [[ -z "$ip" ]]; then
        ip=$(ifconfig 2>/dev/null | grep -oE '10\.[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    fi
    if [[ -n "$ip" ]]; then
        echo "${ip%.*}.0/24"
    else
        echo ""
    fi
}

init_database() {
    local db_dir
    db_dir="$(dirname "$MINER_DB")"

    # Create directory with sudo if needed (owned by spiraluser for dashboard access)
    if [[ ! -d "$db_dir" ]]; then
        if ! mkdir -p "$db_dir" 2>/dev/null; then
            sudo mkdir -p "$db_dir"
            sudo chown "$POOL_USER:$POOL_USER" "$db_dir" 2>/dev/null || sudo chown "$USER:$USER" "$db_dir"
            sudo chmod 775 "$db_dir"
        fi
    fi

    # Initialize database file if it doesn't exist
    if [[ ! -f "$MINER_DB" ]]; then
        local init_json='{
  "miners": {},
  "by_type": {
    "nmaxe": [],
    "nerdqaxe": [],
    "axeos": [],
    "qaxe": [],
    "qaxeplus": [],
    "luckyminer": [],
    "jingleminer": [],
    "zyber": [],
    "esp32miner": [],
    "avalon": [],
    "antminer": [],
    "antminer_scrypt": [],
    "whatsminer": [],
    "innosilicon": [],
    "goldshell": [],
    "hammer": [],
    "futurebit": [],
    "canaan": [],
    "ebang": [],
    "gekkoscience": [],
    "ipollo": [],
    "epic": [],
    "elphapex": [],
    "braiins": [],
    "vnish": [],
    "luxos": []
  },
  "last_scan": null
}'
        if ! echo "$init_json" > "$MINER_DB" 2>/dev/null; then
            echo "$init_json" | sudo tee "$MINER_DB" > /dev/null
            sudo chown "$POOL_USER:$POOL_USER" "$MINER_DB" 2>/dev/null || sudo chown "$USER:$USER" "$MINER_DB"
            sudo chmod 664 "$MINER_DB"
        fi
    fi
}

clear_database() {
    echo -e "${YELLOW}Clearing miner database...${NC}"

    if [[ -f "$MINER_DB" ]]; then
        rm -f "$MINER_DB"
        echo -e "  ${GREEN}✓${NC} Removed: $MINER_DB"
    else
        echo -e "  ${DIM}Database not found (already clean)${NC}"
    fi

    echo -e "${GREEN}Database cleared!${NC}"
    echo ""
}

detect_miner_type() {
    local ip=$1
    local timeout=2

    # Try CGMiner API FIRST (port 4028) - used by Antminer, Whatsminer, Avalon, Innosilicon, etc.
    # Check this before HTTP because some CGMiner devices also respond on port 80
    local cgminer_response=$(printf '{"command":"version"}' | timeout $timeout nc -w $timeout $ip $CGMINER_PORT 2>/dev/null | tr -d '\0')

    if [[ -n "$cgminer_response" ]] && echo "$cgminer_response" | grep -q "STATUS"; then
        # Canaan AvalonMiner A12/A13/A14 series (check BEFORE generic Avalon)
        if echo "$cgminer_response" | grep -qi "canaan\|AvalonMiner"; then
            echo "canaan"
            return 0
        # Avalon Nano 3 identifies as "Avalon Nano3s" or similar
        elif echo "$cgminer_response" | grep -qi "avalon\|Nano3"; then
            echo "avalon"
            return 0
        # FutureBit Apollo uses BFGMiner (CGMiner-compatible)
        elif echo "$cgminer_response" | grep -qi "futurebit\|apollo\|bfgminer"; then
            echo "futurebit"
            return 0
        # Antminer L-series (Scrypt) - check for L3, L7, L9
        elif echo "$cgminer_response" | grep -qiE "antminer.*(L[0-9]|scrypt)|bitmain.*L[0-9]"; then
            echo "antminer_scrypt"
            return 0
        elif echo "$cgminer_response" | grep -qi "antminer\|bitmain"; then
            echo "antminer"
            return 0
        elif echo "$cgminer_response" | grep -qi "whatsminer\|microbt"; then
            echo "whatsminer"
            return 0
        elif echo "$cgminer_response" | grep -qi "innosilicon"; then
            echo "innosilicon"
            return 0
        elif echo "$cgminer_response" | grep -qi "goldshell"; then
            echo "goldshell"
            return 0
        elif echo "$cgminer_response" | grep -qi "ebang\|ebit"; then
            echo "ebang"
            return 0
        elif echo "$cgminer_response" | grep -qi "gekkoscience\|compac\|newpac"; then
            echo "gekkoscience"
            return 0
        elif echo "$cgminer_response" | grep -qi "ipollo"; then
            echo "ipollo"
            return 0
        elif echo "$cgminer_response" | grep -qi "elphapex\|DG1\|DG.Home"; then
            echo "elphapex"
            return 0
        else
            # Generic CGMiner device
            echo "avalon"
            return 0
        fi
    fi

    # Try AxeOS/BitAxe HTTP API (port 80) - only if CGMiner didn't match
    local axeos_response=$(curl -s --connect-timeout $timeout --max-time $timeout "http://${ip}/api/system/info" 2>/dev/null)
    if [[ -n "$axeos_response" ]]; then
        if echo "$axeos_response" | grep -q '"hostname"\|"ASICModel"\|"hashRate"'; then
            # NerdOctaxe (check FIRST - contains "axe" substring)
            if echo "$axeos_response" | grep -qi "nerdoctaxe\|octaxe"; then
                echo "nerdqaxe"
                return 0
            # NerdQAxe/NerdAxe variants
            elif echo "$axeos_response" | grep -qi "nerdqaxe\|nerdaxe\|nerd-qaxe\|nerd-axe"; then
                echo "nerdqaxe"
                return 0
            # QAxe/QAxe+ (must NOT contain "nerd")
            elif echo "$axeos_response" | grep -qi "qaxe" && ! echo "$axeos_response" | grep -qi "nerd"; then
                if echo "$axeos_response" | grep -qi "qaxe+\|qaxeplus\|qaxe-plus"; then
                    echo "qaxeplus"
                    return 0
                else
                    echo "qaxe"
                    return 0
                fi
            # Lucky Miner LV06/LV07/LV08
            elif echo "$axeos_response" | grep -qi "lucky\|lv06\|lv07\|lv08"; then
                echo "luckyminer"
                return 0
            # Jingle Miner BTC Solo Pro/Lite
            elif echo "$axeos_response" | grep -qi "jingle\|jingleminer\|btc.solo"; then
                echo "jingleminer"
                return 0
            # Zyber TinyChipHub
            elif echo "$axeos_response" | grep -qi "zyber\|tinychip"; then
                echo "zyber"
                return 0
            # Hammer/Heatbit (Scrypt miners)
            elif echo "$axeos_response" | grep -qi "hammer\|heatbit\|plebsource"; then
                echo "hammer"
                return 0
            # ESP32 Miner
            elif echo "$axeos_response" | grep -qi "esp32miner\|nerd-miner\|nerd_miner"; then
                echo "esp32miner"
                return 0
            # NMaxe/BitAxe (generic AxeOS family)
            elif echo "$axeos_response" | grep -qi "nmaxe\|bitaxe\|supra\|ultra\|gamma\|hex"; then
                echo "nmaxe"
                return 0
            else
                # Generic AxeOS device
                echo "axeos"
                return 0
            fi
        fi
    fi

    # Try Goldshell HTTP REST API (port 80, different endpoint than AxeOS)
    # Goldshell Mini DOGE/LT5/KD6 use /mcb/cgminer?cgminercmd=summary
    local goldshell_response=$(curl -s --connect-timeout $timeout --max-time $timeout "http://${ip}/mcb/cgminer?cgminercmd=summary" 2>/dev/null)
    if [[ -n "$goldshell_response" ]] && echo "$goldshell_response" | grep -q '"SUMMARY"\|"STATUS"'; then
        echo "goldshell"
        return 0
    fi

    # Try ePIC BlockMiner HTTP REST API on port 4028 (NOT CGMiner TCP — ePIC speaks HTTP)
    local epic_response=$(curl -s --connect-timeout $timeout --max-time $timeout -u root:letmein "http://${ip}:4028/summary" 2>/dev/null)
    if [[ -n "$epic_response" ]] && echo "$epic_response" | grep -qi "Mining\|BlockMiner\|ePIC"; then
        echo "epic"
        return 0
    fi

    # Try CGMiner stats as fallback (some miners only respond to stats, not version)
    local cgminer_stats=$(printf '{"command":"stats"}' | timeout $timeout nc -w $timeout $ip $CGMINER_PORT 2>/dev/null | tr -d '\0')
    if [[ -n "$cgminer_stats" ]] && echo "$cgminer_stats" | grep -q "STATUS"; then
        if echo "$cgminer_stats" | grep -qi "canaan\|AvalonMiner"; then
            echo "canaan"
            return 0
        elif echo "$cgminer_stats" | grep -qi "avalon\|Nano"; then
            echo "avalon"
            return 0
        elif echo "$cgminer_stats" | grep -qiE "antminer.*(L[0-9]|scrypt)|bitmain.*L[0-9]"; then
            echo "antminer_scrypt"
            return 0
        elif echo "$cgminer_stats" | grep -qi "antminer\|bitmain"; then
            echo "antminer"
            return 0
        elif echo "$cgminer_stats" | grep -qi "whatsminer\|microbt"; then
            echo "whatsminer"
            return 0
        elif echo "$cgminer_stats" | grep -qi "innosilicon"; then
            echo "innosilicon"
            return 0
        elif echo "$cgminer_stats" | grep -qi "goldshell"; then
            echo "goldshell"
            return 0
        elif echo "$cgminer_stats" | grep -qi "futurebit\|apollo\|bfgminer"; then
            echo "futurebit"
            return 0
        elif echo "$cgminer_stats" | grep -qi "ebang\|ebit"; then
            echo "ebang"
            return 0
        elif echo "$cgminer_stats" | grep -qi "gekkoscience\|compac\|newpac"; then
            echo "gekkoscience"
            return 0
        elif echo "$cgminer_stats" | grep -qi "ipollo"; then
            echo "ipollo"
            return 0
        elif echo "$cgminer_stats" | grep -qi "elphapex\|DG1\|DG.Home"; then
            echo "elphapex"
            return 0
        else
            # Generic CGMiner device
            echo "avalon"
            return 0
        fi
    fi

    echo ""
    return 1
}

get_miner_info() {
    local ip=$1
    local mtype=$2
    local timeout=2

    case "$mtype" in
        nmaxe|nerdqaxe|axeos|hammer|qaxe|qaxeplus|luckyminer|jingleminer|zyber|esp32miner)
            # AxeOS HTTP API
            local info=$(curl -s --connect-timeout $timeout --max-time $timeout "http://${ip}/api/system/info" 2>/dev/null)
            if [[ -n "$info" ]]; then
                local hostname=$(echo "$info" | python3 -c "import sys,json; print(json.load(sys.stdin).get('hostname',''))" 2>/dev/null)
                local hashrate=$(echo "$info" | python3 -c "import sys,json; print(json.load(sys.stdin).get('hashRate',0))" 2>/dev/null)
                echo "${hostname:-Unknown}|${hashrate:-0} GH/s"
            fi
            ;;
        goldshell)
            # Goldshell HTTP REST API
            local info=$(curl -s --connect-timeout $timeout --max-time $timeout "http://${ip}/mcb/cgminer?cgminercmd=devs" 2>/dev/null)
            if [[ -n "$info" ]]; then
                local hashrate=$(echo "$info" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    devs = data.get('data', {}).get('DEVS', [])
    total_mhs = sum(float(d.get('MHS 5s', 0)) for d in devs)
    if total_mhs > 1000:
        print(f'{total_mhs/1000:.2f} GH/s')
    else:
        print(f'{total_mhs:.2f} MH/s')
except:
    print('Active')
" 2>/dev/null)
                echo "Goldshell|${hashrate:-Active}"
            else
                echo "Goldshell|Offline"
            fi
            ;;
        epic)
            # ePIC BlockMiner HTTP REST on port 4028
            local epic_info=$(curl -s --connect-timeout $timeout --max-time $timeout -u root:letmein "http://${ip}:4028/summary" 2>/dev/null)
            if [[ -n "$epic_info" ]]; then
                local hashrate=$(echo "$epic_info" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    mining = data.get('Mining', {})
    ghs = float(mining.get('Speed(GHS)', 0) or 0)
    if ghs > 1000:
        print(f'{ghs/1000:.2f} TH/s')
    else:
        print(f'{ghs:.2f} GH/s')
except:
    print('Active')
" 2>/dev/null)
                echo "ePIC BlockMiner|${hashrate:-Active}"
            else
                echo "ePIC BlockMiner|Offline"
            fi
            ;;
        antminer|whatsminer|avalon|innosilicon|antminer_scrypt|futurebit|ebang|canaan|gekkoscience|ipollo|elphapex|luxos|vnish|braiins)
            # CGMiner API (port 4028)
            local summary=$(printf '{"command":"summary"}' | timeout $timeout nc -w $timeout $ip $CGMINER_PORT 2>/dev/null | tr -d '\0')
            if [[ -n "$summary" ]]; then
                local mhs=$(echo "$summary" | python3 -c "
import sys, json, re
try:
    data = sys.stdin.read().replace('\x00', '')
    data = re.sub(r'^[^{]*', '', data)
    idx = data.rfind('}')
    if idx >= 0:
        data = data[:idx+1]
    j = json.loads(data)
    s = j.get('SUMMARY', [{}])[0]
    ghs = s.get('GHS av', 0) or s.get('GHS 5s', 0)
    mhs = s.get('MHS av', 0) or s.get('MHS 5s', 0)
    if ghs and float(ghs) > 0:
        g = float(ghs)
        print(f'{g/1000:.2f} TH/s' if g >= 1000 else f'{g:.2f} GH/s')
    elif mhs and float(mhs) > 0:
        g = float(mhs) / 1000
        print(f'{g/1000:.2f} TH/s' if g >= 1000 else f'{g:.2f} GH/s')
    else:
        print('Active')
except:
    print('Active')
" 2>/dev/null)
                local version=$(printf '{"command":"version"}' | timeout $timeout nc -w $timeout $ip $CGMINER_PORT 2>/dev/null | tr -d '\0')
                local device_name=$(echo "$version" | python3 -c "
import sys, json, re
try:
    data = sys.stdin.read().replace('\x00', '')
    data = re.sub(r'^[^{]*', '', data)
    idx = data.rfind('}')
    if idx >= 0:
        data = data[:idx+1]
    j = json.loads(data)
    v = j.get('VERSION', [{}])[0]
    name = v.get('Type', '') or v.get('PROD', '') or v.get('Miner', '') or v.get('MODEL', '') or v.get('CGMiner', 'CGMiner')
    print(name[:20] if name else 'CGMiner')
except:
    print('CGMiner')
" 2>/dev/null)
                echo "${device_name:-CGMiner}|${mhs:-Active}"
            else
                echo "CGMiner Device|Offline"
            fi
            ;;
    esac
}

add_miner() {
    local ip=$1
    local mtype=$2
    local nickname=$3

    init_database

    python3 -c "
import json, sys
from datetime import datetime

db_file = sys.argv[1]
ip = sys.argv[2]
mtype = sys.argv[3]
nickname = sys.argv[4] if len(sys.argv) > 4 else ''

try:
    with open(db_file, 'r') as f:
        db = json.load(f)
except:
    db = {'miners': {}, 'by_type': {'nmaxe':[],'nerdqaxe':[],'axeos':[],'qaxe':[],'qaxeplus':[],'luckyminer':[],'jingleminer':[],'zyber':[],'esp32miner':[],'avalon':[],'antminer':[],'antminer_scrypt':[],'whatsminer':[],'innosilicon':[],'goldshell':[],'hammer':[],'futurebit':[],'canaan':[],'ebang':[],'gekkoscience':[],'ipollo':[],'epic':[],'elphapex':[],'braiins':[],'vnish':[],'luxos':[]}}

if ip not in db.get('miners', {}):
    db.setdefault('miners', {})[ip] = {}

db['miners'][ip]['type'] = mtype
db['miners'][ip]['added'] = datetime.utcnow().isoformat() + 'Z'
if nickname:
    db['miners'][ip]['nickname'] = nickname

if mtype not in db.get('by_type', {}):
    db.setdefault('by_type', {})[mtype] = []
if ip not in db['by_type'][mtype]:
    db['by_type'][mtype].append(ip)

import tempfile, os
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(db_file), suffix='.tmp')
try:
    with os.fdopen(fd, 'w') as f:
        json.dump(db, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.rename(tmp, db_file)
except:
    os.unlink(tmp)
    raise

print(f'Added {ip} as {mtype}')
" "$MINER_DB" "$ip" "$mtype" "$nickname"
}

remove_miner() {
    local ip=$1

    python3 -c "
import json, sys

db_file = sys.argv[1]
ip = sys.argv[2]

try:
    with open(db_file, 'r') as f:
        db = json.load(f)
except:
    print('No miner database found')
    exit(1)

if ip in db.get('miners', {}):
    del db['miners'][ip]
    print(f'Removed {ip} from miners')
else:
    print(f'{ip} not found in database')
    exit(1)

for mtype, ips in db.get('by_type', {}).items():
    if ip in ips:
        ips.remove(ip)

import tempfile, os
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(db_file), suffix='.tmp')
try:
    with os.fdopen(fd, 'w') as f:
        json.dump(db, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.rename(tmp, db_file)
except:
    os.unlink(tmp)
    raise
" "$MINER_DB" "$ip"
}

set_nickname() {
    local ip=$1
    local nickname=$2

    python3 -c "
import json, sys

db_file = sys.argv[1]
ip = sys.argv[2]
nickname = sys.argv[3]

try:
    with open(db_file, 'r') as f:
        db = json.load(f)
except:
    print('No miner database found. Run the scanner first.')
    exit(1)

if ip not in db.get('miners', {}):
    print(f'Miner {ip} not in database. Add it first with --add {ip}')
    exit(1)

db['miners'][ip]['nickname'] = nickname

import tempfile, os
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(db_file), suffix='.tmp')
try:
    with os.fdopen(fd, 'w') as f:
        json.dump(db, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.rename(tmp, db_file)
except:
    os.unlink(tmp)
    raise

print(f'Set nickname for {ip}: {nickname}')
" "$MINER_DB" "$ip" "$nickname"
}

list_miners() {
    echo ""
    echo -e "${WHITE}Known Miners:${NC}"
    echo ""

    if [[ ! -f "$MINER_DB" ]]; then
        echo -e "  ${DIM}No miners configured. Run: $0${NC}"
        return
    fi

    python3 -c "
import json, sys

db_file = sys.argv[1]
try:
    with open(db_file, 'r') as f:
        db = json.load(f)
except:
    print('  No miner database found')
    exit(0)

miners = db.get('miners', {})
if not miners:
    print('  No miners in database. Run the scanner first.')
    exit(0)

type_names = {
    'nmaxe': 'BitAxe',
    'nerdqaxe': 'NerdQAxe',
    'axeos': 'AxeOS',
    'qaxe': 'QAxe',
    'qaxeplus': 'QAxe+',
    'luckyminer': 'LuckyMiner',
    'jingleminer': 'JingleMiner',
    'zyber': 'Zyber',
    'esp32miner': 'ESP32',
    'antminer': 'Antminer',
    'antminer_scrypt': 'Antminer-L',
    'whatsminer': 'Whatsminer',
    'avalon': 'Avalon',
    'canaan': 'Canaan',
    'innosilicon': 'Innosilicon',
    'goldshell': 'Goldshell',
    'hammer': 'Hammer',
    'futurebit': 'FutureBit',
    'ebang': 'Ebang',
    'gekkoscience': 'GekkoScience',
    'ipollo': 'iPollo',
    'epic': 'ePIC',
    'elphapex': 'Elphapex',
    'braiins': 'BraiinsOS',
    'vnish': 'Vnish',
    'luxos': 'LuxOS'
}

print(f\"  {'IP Address':<18} {'Type':<16} {'Nickname'}\")
print(f\"  {'-'*18} {'-'*16} {'-'*20}\")

for ip, info in sorted(miners.items()):
    mtype = info.get('type', 'unknown')
    nickname = info.get('nickname', '-')
    type_name = type_names.get(mtype, mtype)
    print(f'  {ip:<18} {type_name:<16} {nickname}')

print(f'\n  Total: {len(miners)} miners')
last_scan = db.get('last_scan', 'Never')
print(f'  Last scan: {last_scan}')
" "$MINER_DB"
    echo ""
}

scan_network() {
    local subnet=$1

    if [[ -z "$subnet" ]]; then
        subnet=$(get_local_subnet)
    fi

    if [[ -z "$subnet" ]]; then
        echo -e "${RED}Could not detect local subnet. Please specify: $0 192.168.1.0/24${NC}"
        exit 1
    fi

    echo -e "${WHITE}Scanning:${NC} $subnet"
    echo -e "${DIM}Fast parallel scan in progress...${NC}"
    echo ""

    init_database

    local base_ip="${subnet%.*}"

    # SECURITY: Set restrictive umask for temp directory creation
    local old_umask
    old_umask=$(umask)
    umask 077
    local tmpdir=$(mktemp -d) || { umask "$old_umask"; echo -e "${RED}Failed to create temp directory${NC}"; exit 1; }
    umask "$old_umask"

    local active_file="$tmpdir/active_ips"
    local found_file="$tmpdir/found_miners"
    touch "$active_file" "$found_file"

    # Cleanup on exit (use function to safely defer $tmpdir expansion)
    _cleanup_tmpdir() { rm -rf "$tmpdir"; }
    trap _cleanup_tmpdir EXIT

    # PHASE 1: Ultra-fast parallel port scan
    echo -e "  ${CYAN}Phase 1:${NC} Port scanning (parallel)..."

    # Use high concurrency - all 254 IPs at once for maximum speed
    # LAN scans are I/O bound, not CPU bound
    for i in $(seq 1 254); do
        ip="${base_ip}.$i"
        # Check all common miner ports: 80 (AxeOS/Goldshell), 4028 (CGMiner/ePIC), 4029, 8080
        (timeout 1 nc -z -w 1 "$ip" 80 2>/dev/null || \
         timeout 1 nc -z -w 1 "$ip" 4028 2>/dev/null || \
         timeout 1 nc -z -w 1 "$ip" 4029 2>/dev/null || \
         timeout 1 nc -z -w 1 "$ip" 8080 2>/dev/null) && \
            echo "$ip" >> "$active_file" &
    done

    # Wait for all scans to complete
    wait

    local active_count=$(wc -l < "$active_file" 2>/dev/null || echo 0)
    echo -e "  ${GREEN}✓${NC} Found ${WHITE}$active_count${NC} responsive hosts"

    if [[ $active_count -eq 0 ]]; then
        echo -e "\n${WHITE}Scan Complete!${NC}"
        echo -e "  Found: ${GREEN}0${NC} miners"
        echo ""
        return
    fi

    # PHASE 2: Identify miners on responsive hosts (PARALLEL)
    echo -e "  ${CYAN}Phase 2:${NC} Identifying miners (parallel)..."

    # Parallel miner identification - write results to temp file
    local results_file="$tmpdir/miner_results"
    touch "$results_file"

    # Export functions and variables for subshells
    export CGMINER_PORT GREEN WHITE CYAN DIM NC
    export -f detect_miner_type get_miner_info 2>/dev/null || true

    # Process miners in parallel batches
    local id_jobs=0
    local max_id_jobs=20  # 20 concurrent identifications

    identify_miner() {
        local ip=$1
        local results_file=$2
        local timeout=2

        # Inline detect_miner_type for subshell (full detection with all 26 types)
        local cgminer_response=$(printf '{"command":"version"}' | timeout $timeout nc -w $timeout $ip 4028 2>/dev/null | tr -d '\0')
        local mtype=""

        if [[ -n "$cgminer_response" ]] && echo "$cgminer_response" | grep -q "STATUS"; then
            if echo "$cgminer_response" | grep -qi "canaan\|AvalonMiner"; then
                mtype="canaan"
            elif echo "$cgminer_response" | grep -qi "avalon\|Nano3"; then
                mtype="avalon"
            elif echo "$cgminer_response" | grep -qi "futurebit\|apollo\|bfgminer"; then
                mtype="futurebit"
            elif echo "$cgminer_response" | grep -qiE "antminer.*(L[0-9]|scrypt)|bitmain.*L[0-9]"; then
                mtype="antminer_scrypt"
            elif echo "$cgminer_response" | grep -qi "antminer\|bitmain"; then
                mtype="antminer"
            elif echo "$cgminer_response" | grep -qi "whatsminer\|microbt"; then
                mtype="whatsminer"
            elif echo "$cgminer_response" | grep -qi "innosilicon"; then
                mtype="innosilicon"
            elif echo "$cgminer_response" | grep -qi "goldshell"; then
                mtype="goldshell"
            elif echo "$cgminer_response" | grep -qi "ebang\|ebit"; then
                mtype="ebang"
            elif echo "$cgminer_response" | grep -qi "gekkoscience\|compac\|newpac"; then
                mtype="gekkoscience"
            elif echo "$cgminer_response" | grep -qi "ipollo"; then
                mtype="ipollo"
            elif echo "$cgminer_response" | grep -qi "elphapex\|DG1\|DG.Home"; then
                mtype="elphapex"
            else
                mtype="avalon"
            fi
        fi

        # Try AxeOS if no CGMiner response
        if [[ -z "$mtype" ]]; then
            local axeos_response=$(curl -s --connect-timeout $timeout --max-time $timeout "http://${ip}/api/system/info" 2>/dev/null)
            if [[ -n "$axeos_response" ]] && echo "$axeos_response" | grep -q '"hostname"\|"ASICModel"\|"hashRate"'; then
                if echo "$axeos_response" | grep -qi "nerdoctaxe\|nerdqaxe\|nerdaxe\|nerd-qaxe\|nerd-axe"; then
                    mtype="nerdqaxe"
                elif echo "$axeos_response" | grep -qi "qaxe" && ! echo "$axeos_response" | grep -qi "nerd"; then
                    if echo "$axeos_response" | grep -qi "qaxe+\|qaxeplus\|qaxe-plus"; then
                        mtype="qaxeplus"
                    else
                        mtype="qaxe"
                    fi
                elif echo "$axeos_response" | grep -qi "lucky\|lv06\|lv07\|lv08"; then
                    mtype="luckyminer"
                elif echo "$axeos_response" | grep -qi "jingle\|jingleminer\|btc.solo"; then
                    mtype="jingleminer"
                elif echo "$axeos_response" | grep -qi "zyber\|tinychip"; then
                    mtype="zyber"
                elif echo "$axeos_response" | grep -qi "hammer\|heatbit\|plebsource"; then
                    mtype="hammer"
                elif echo "$axeos_response" | grep -qi "esp32miner\|nerd-miner\|nerd_miner"; then
                    mtype="esp32miner"
                elif echo "$axeos_response" | grep -qi "nmaxe\|bitaxe\|supra\|ultra\|gamma\|hex"; then
                    mtype="nmaxe"
                else
                    mtype="axeos"
                fi
            fi
        fi

        # Try Goldshell HTTP REST (different endpoint than AxeOS)
        if [[ -z "$mtype" ]]; then
            local goldshell_response=$(curl -s --connect-timeout $timeout --max-time $timeout "http://${ip}/mcb/cgminer?cgminercmd=summary" 2>/dev/null)
            if [[ -n "$goldshell_response" ]] && echo "$goldshell_response" | grep -q '"SUMMARY"\|"STATUS"'; then
                mtype="goldshell"
            fi
        fi

        # Try ePIC HTTP REST on port 4028
        if [[ -z "$mtype" ]]; then
            local epic_response=$(curl -s --connect-timeout $timeout --max-time $timeout -u root:letmein "http://${ip}:4028/summary" 2>/dev/null)
            if [[ -n "$epic_response" ]] && echo "$epic_response" | grep -qi "Mining\|BlockMiner\|ePIC"; then
                mtype="epic"
            fi
        fi

        if [[ -n "$mtype" ]]; then
            echo "$ip|$mtype" >> "$results_file"
        fi
    }
    export -f identify_miner

    # Spawn parallel identification jobs
    while IFS= read -r ip; do
        identify_miner "$ip" "$results_file" &
        id_jobs=$((id_jobs + 1))

        if [[ $id_jobs -ge $max_id_jobs ]]; then
            wait -n 2>/dev/null || wait
            id_jobs=$((id_jobs - 1))
        fi
    done < "$active_file"

    # Wait for all identification jobs
    wait

    # Process results
    local found=0
    local cgminer_miners=()  # Track CGMiner-based miners (need nicknames)

    while IFS='|' read -r ip mtype; do
        if [[ -n "$ip" ]] && [[ -n "$mtype" ]]; then
            local info=$(get_miner_info "$ip" "$mtype")
            echo -e "  ${GREEN}✓${NC} Found: ${WHITE}$ip${NC} - ${CYAN}$mtype${NC} ${DIM}($info)${NC}"
            add_miner "$ip" "$mtype" ""
            found=$((found + 1))

            # Track CGMiner-based miners (they don't expose hostname via API)
            case "$mtype" in
                avalon|antminer|antminer_scrypt|whatsminer|innosilicon|canaan|futurebit|ebang|gekkoscience|ipollo|elphapex)
                    cgminer_miners+=("$ip|$mtype")
                    ;;
            esac
        fi
    done < "$results_file"

    echo ""
    echo -e "${WHITE}Scan Complete!${NC}"
    echo -e "  Found: ${GREEN}$found${NC} miners"
    echo ""

    # Show nickname suggestion for CGMiner-based miners
    if [[ ${#cgminer_miners[@]} -gt 0 ]]; then
        echo -e "${YELLOW}═══════════════════════════════════════════════════════════════${NC}"
        echo -e "${YELLOW}  SET NICKNAMES FOR YOUR MINERS${NC}"
        echo -e "${YELLOW}═══════════════════════════════════════════════════════════════${NC}"
        echo ""
        echo -e "  ${DIM}ASIC miners (Antminer, Whatsminer, Avalon, etc.) don't expose${NC}"
        echo -e "  ${DIM}their hostname via CGMiner API. Set nicknames for easier ID:${NC}"
        echo ""
        for entry in "${cgminer_miners[@]}"; do
            local miner_ip="${entry%%|*}"
            local miner_type="${entry##*|}"
            echo -e "    ${WHITE}$0 --nickname $miner_ip 'My-${miner_type^}-01'${NC}"
        done
        echo ""
    elif [[ $found -gt 0 ]]; then
        echo -e "${YELLOW}Tip:${NC} Set nicknames with: $0 --nickname <IP> 'Friendly Name'"
        echo ""
    fi

    # Note about ESP32 miners
    echo -e "${DIM}────────────────────────────────────────────────────────────────${NC}"
    echo -e "${CYAN}Note:${NC} Some ESP32-based miners may not be auto-detected due to limited"
    echo -e "      network APIs. Add them manually with:"
    echo -e "      ${WHITE}$0 --add <IP>${NC} and select 'esp32miner' type"
    echo -e ""
    echo -e "${CYAN}Note:${NC} Scans are additive — old miners are NOT automatically removed."
    echo -e "      To remove a miner that is no longer on the network:"
    echo -e "      ${WHITE}$0 --remove <IP>${NC}"
    echo -e "      To clear all miners and start fresh: ${WHITE}$0 --reset${NC}"
    echo -e "${DIM}────────────────────────────────────────────────────────────────${NC}"
    echo ""

    # Update last scan time
    python3 -c "
import json, sys, tempfile, os
from datetime import datetime
try:
    db_file = sys.argv[1]
    with open(db_file, 'r') as f:
        db = json.load(f)
    db['last_scan'] = datetime.utcnow().isoformat() + 'Z'
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(db_file), suffix='.tmp')
    with os.fdopen(fd, 'w') as f:
        json.dump(db, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.rename(tmp, db_file)
except:
    pass
" "$MINER_DB"
}

# Main
show_banner

case "${1:-}" in
    "-h"|"--help"|"help")
        show_usage
        ;;
    "--reset"|"-R"|"reset")
        echo -e "${YELLOW}This will delete your miner database and rescan the network.${NC}"
        echo -e "${YELLOW}All nicknames will be lost!${NC}"
        echo ""
        read -p "Continue? (y/N): " confirm
        if [[ "$confirm" =~ ^[Yy]$ ]]; then
            clear_database
            scan_network "${2:-}"
        else
            echo "Cancelled."
        fi
        ;;
    "--clear"|"-C"|"clear")
        echo -e "${YELLOW}This will delete your miner database.${NC}"
        echo -e "${YELLOW}All miners and nicknames will be removed!${NC}"
        echo ""
        read -p "Continue? (y/N): " confirm
        if [[ "$confirm" =~ ^[Yy]$ ]]; then
            clear_database
        else
            echo "Cancelled."
        fi
        ;;
    "--list"|"-l"|"list")
        list_miners
        ;;
    "--add"|"-a"|"add")
        if [[ -z "$2" ]]; then
            echo -e "${RED}Please specify IP address${NC}"
            exit 1
        fi
        if ! [[ "$2" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            echo -e "${RED}Invalid IP address format: $2${NC}"
            exit 1
        fi
        mtype=$(detect_miner_type "$2")
        if [[ -z "$mtype" ]]; then
            echo -e "${YELLOW}Could not auto-detect miner type. Please specify:${NC}"
            echo ""
            echo -e "  ${GREEN}AxeOS HTTP API devices:${NC}"
            echo "   1) nmaxe (BitAxe/NMaxe/AxeOS)"
            echo "   2) nerdqaxe (NerdQAxe++/NerdAxe/NerdOctaxe)"
            echo "   3) luckyminer (Lucky Miner LV06/LV07/LV08)"
            echo "   4) jingleminer (Jingle Miner BTC Solo Pro/Lite)"
            echo "   5) zyber (Zyber 8G/8GP/8S TinyChipHub)"
            echo "   6) hammer (PlebSource Hammer Miner — Scrypt)"
            echo "   7) esp32miner (ESP32 Miners)"
            echo ""
            echo -e "  ${GREEN}CGMiner API devices (port 4028):${NC}"
            echo "   8) antminer (Bitmain S19/S21/T21 — SHA-256d)"
            echo "   9) antminer_scrypt (Bitmain L3+/L7/L9 — Scrypt)"
            echo "  10) whatsminer (MicroBT M30/M50/M60)"
            echo "  11) avalon (Avalon Nano 2/3/3S)"
            echo "  12) canaan (Canaan AvalonMiner A12-A16)"
            echo "  13) innosilicon (Innosilicon A10/A11/T3)"
            echo "  14) futurebit (FutureBit Apollo/Apollo II)"
            echo "  15) goldshell (Goldshell KD6/LT5/Mini DOGE — Scrypt)"
            echo "  16) ebang (Ebang Ebit E9-E12+)"
            echo "  17) gekkoscience (GekkoScience Compac F/NewPac/R606)"
            echo "  18) ipollo (iPollo V1/V1 Mini/G1)"
            echo "  19) epic (ePIC BlockMiner)"
            echo "  20) elphapex (Elphapex DG1/DG Home — Scrypt)"
            echo ""
            echo -e "  ${GREEN}Custom firmware (manual config):${NC}"
            echo "  21) braiins (BraiinsOS/BOS+ on Antminers)"
            echo "  22) vnish (Vnish firmware on Antminers)"
            echo "  23) luxos (LuxOS firmware on Antminers)"
            echo ""
            read -p "Select (1-23): " choice
            case "$choice" in
                1) mtype="nmaxe" ;;
                2) mtype="nerdqaxe" ;;
                3) mtype="luckyminer" ;;
                4) mtype="jingleminer" ;;
                5) mtype="zyber" ;;
                6) mtype="hammer" ;;
                7) mtype="esp32miner" ;;
                8) mtype="antminer" ;;
                9) mtype="antminer_scrypt" ;;
                10) mtype="whatsminer" ;;
                11) mtype="avalon" ;;
                12) mtype="canaan" ;;
                13) mtype="innosilicon" ;;
                14) mtype="futurebit" ;;
                15) mtype="goldshell" ;;
                16) mtype="ebang" ;;
                17) mtype="gekkoscience" ;;
                18) mtype="ipollo" ;;
                19) mtype="epic" ;;
                20) mtype="elphapex" ;;
                21) mtype="braiins" ;;
                22) mtype="vnish" ;;
                23) mtype="luxos" ;;
                *) echo "Invalid choice"; exit 1 ;;
            esac
        fi
        # For ESP32 miners, prompt for worker name (required for pool stats tracking)
        nickname="${3:-}"
        if [[ "$mtype" == "esp32miner" && -z "$nickname" ]]; then
            echo ""
            echo -e "${YELLOW}ESP32 miners have no HTTP API - stats come from the pool.${NC}"
            echo -e "Enter the ${WHITE}worker name${NC} this device uses when mining."
            echo -e "${DIM}(This is the name after your wallet address, e.g., ADDRESS.${WHITE}MyESP32${NC}${DIM})${NC}"
            read -p "Worker name: " nickname
            if [[ -z "$nickname" ]]; then
                echo -e "${YELLOW}Warning: Without a worker name, pool stats won't be available.${NC}"
            fi
        fi
        add_miner "$2" "$mtype" "$nickname"
        ;;
    "--remove"|"-r"|"remove")
        if [[ -z "$2" ]]; then
            echo -e "${RED}Please specify IP address${NC}"
            exit 1
        fi
        remove_miner "$2"
        ;;
    "--nickname"|"-n"|"nickname")
        if [[ -z "$2" ]] || [[ -z "$3" ]]; then
            echo -e "${RED}Usage: $0 --nickname <IP> 'Nickname'${NC}"
            exit 1
        fi
        set_nickname "$2" "$3"
        ;;
    "")
        # No args - do a scan
        scan_network ""
        ;;
    *)
        # Assume it's a subnet to scan
        if [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(/[0-9]+)?$ ]]; then
            scan_network "$1"
        else
            echo -e "${RED}Unknown option: $1${NC}"
            show_usage
            exit 1
        fi
        ;;
esac
