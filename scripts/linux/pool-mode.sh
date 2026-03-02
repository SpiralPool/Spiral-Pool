#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║                    SPIRAL POOL - POOL MODE MANAGER                         ║
# ║                                                                            ║
# ║   Switch between Solo Mode (single coin) and Multi-Coin Mode, or add/      ║
# ║   remove coins from your pool configuration.                               ║
# ║                                                                            ║
# ║   Supported Coins: DGB, BTC, BCH, BC2, LTC, DOGE, DGB-SCRYPT,              ║
# ║                    PEP, CAT, NMC, SYS, XMY, FBTC                           ║
# ║                                                                            ║
# ║   Usage:                                                                   ║
# ║     ./pool-mode.sh                    # Interactive mode                   ║
# ║     ./pool-mode.sh --solo DGB         # Switch to solo DGB                 ║
# ║     ./pool-mode.sh --solo BTC         # Switch to solo BTC                 ║
# ║     ./pool-mode.sh --multi DGB,BTC    # Switch to multi with DGB+BTC       ║
# ║     ./pool-mode.sh --multi DGB,BC2    # Switch to multi with DGB+BC2       ║
# ║     ./pool-mode.sh --add BC2          # Add BC2 to current config          ║
# ║     ./pool-mode.sh --status           # Show current configuration         ║
# ║     ./pool-mode.sh --verify           # Verify & heal services/firewall    ║
# ║                                                                            ║
# ║   Spiral Pool Contributors                                                 ║
# ║                                                                            ║
# ╚════════════════════════════════════════════════════════════════════════════╝

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
NC='\033[0m' # No Color

# Configuration
SPIRALPOOL_DIR="/spiralpool"
CONFIG_FILE="$SPIRALPOOL_DIR/config/config.yaml"
HA_CONFIG_FILE="$SPIRALPOOL_DIR/config/ha.yaml"
HA_CLUSTER_FILE="$SPIRALPOOL_DIR/config/ha_cluster.conf"

# Standard user account for all Spiral Pool operations
# This is hardcoded - no customization allowed
POOL_USER="spiraluser"

# Detect system architecture for coin binary downloads
# dpkg returns "amd64" or "arm64"; fallback to amd64 if dpkg unavailable
SYSTEM_ARCH=$(dpkg --print-architecture 2>/dev/null || echo "amd64")
ARCH_SUFFIX="x86_64-linux-gnu"
if [[ "$SYSTEM_ARCH" == "arm64" ]]; then
    ARCH_SUFFIX="aarch64-linux-gnu"
fi

# Dedicated HA user for SSH operations (must be defined before use in HA_SSH_HOME)
HA_SSH_USER="${HA_SSH_USER:-spiralha}"
HA_SSH_KEY_COMMENT="spiralpool-ha-cluster"

# ═══════════════════════════════════════════════════════════════════════════════
# DATABASE MIGRATIONS (V1 -> V2 Multi-Coin)
# ═══════════════════════════════════════════════════════════════════════════════
# Database schema migrations are handled AUTOMATICALLY by the stratum binary.
# When the stratum starts, it:
#   1. Runs RunV2Migrations() - creates global V2 tables
#   2. Runs CreatePoolTablesV2() for each enabled coin - creates/upgrades pool tables
#
# The migration uses "ALTER TABLE ... ADD COLUMN IF NOT EXISTS" which is:
#   - Idempotent (safe to run multiple times)
#   - Works for fresh V2 installs (creates tables then adds coin column)
#   - Works for V1->V2 upgrades (adds coin column to existing tables)
#   - Works for V2->V2 upgrades (no-op, column already exists)
#
# NO MANUAL SQL COMMANDS ARE REQUIRED - just generate the config and start stratum.
# ═══════════════════════════════════════════════════════════════════════════════

# ═══════════════════════════════════════════════════════════════════════════════
# HA CLUSTER COMMUNICATION MODEL
# ═══════════════════════════════════════════════════════════════════════════════
# HA cluster communication is handled by the VIP manager in the stratum server.
# Communication uses:
#   - UDP port 5363: Cluster discovery and heartbeats (AES-256-GCM encrypted)
#   - HTTP port 5354: Status API (Bearer token authentication)
#   - Cluster token: Pre-shared secret for node authentication
#
# NO SSH IS REQUIRED for HA functionality. The VIP manager handles:
#   - Node discovery via UDP broadcast
#   - Master election based on priority
#   - VIP failover with gratuitous ARP
#   - Health monitoring via heartbeats
#
# PostgreSQL replication uses direct TCP with scram-sha-256 authentication.
#
# SECURITY:
#   - All cluster messages are AES-256-GCM encrypted
#   - HKDF key derivation from cluster token
#   - Constant-time token comparison (timing attack prevention)
#   - Rate limiting and IP blacklisting for brute-force protection

# ═══════════════════════════════════════════════════════════════════════════════
# HA CLUSTER SYNCHRONIZATION FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════════════

# Detect if HA (High Availability) is enabled
detect_ha_enabled() {
    HA_ENABLED=false
    HA_BACKUP_SERVERS=()

    # Check if HA config exists and is enabled
    if [ -f "$HA_CONFIG_FILE" ]; then
        if grep -qE "^\s*enabled:\s*(true|yes|1)" "$HA_CONFIG_FILE" 2>/dev/null; then
            HA_ENABLED=true
        fi
    fi

    # Check spiralctl status for VIP info
    if command -v spiralctl &>/dev/null; then
        if spiralctl status 2>/dev/null | grep -q "VIP:.*active\|HA:.*enabled"; then
            HA_ENABLED=true
        fi
    fi

    # Check for HA cluster configuration file with peer servers
    if [ -f "$HA_CLUSTER_FILE" ]; then
        while IFS= read -r line; do
            # Skip comments and empty lines
            [[ "$line" =~ ^#.*$ || -z "$line" ]] && continue
            # Extract backup server IPs/hostnames
            if [[ "$line" =~ ^backup[_-]?server[s]?[[:space:]]*[:=][[:space:]]*(.+)$ ]]; then
                IFS=',' read -ra servers <<< "${BASH_REMATCH[1]}"
                for server in "${servers[@]}"; do
                    server=$(echo "$server" | xargs)  # Trim whitespace
                    [ -n "$server" ] && HA_BACKUP_SERVERS+=("$server")
                done
            fi
        done < "$HA_CLUSTER_FILE"
    fi

    # Also check ha.yaml for peer nodes
    if [ -f "$HA_CONFIG_FILE" ]; then
        local peers=$(grep -A20 'peers:' "$HA_CONFIG_FILE" 2>/dev/null | grep -E '^\s+-\s*(host|address):' | grep -oP '(?<=:\s).+' | tr -d '"'"'")
        for peer in $peers; do
            [ -n "$peer" ] && [ "$peer" != "127.0.0.1" ] && [ "$peer" != "localhost" ] && HA_BACKUP_SERVERS+=("$peer")
        done
    fi

    # Remove duplicates
    if [ ${#HA_BACKUP_SERVERS[@]} -gt 0 ]; then
        HA_BACKUP_SERVERS=($(printf "%s\n" "${HA_BACKUP_SERVERS[@]}" | sort -u))
    fi
}

# ═══════════════════════════════════════════════════════════════════════════════
# HA SSH KEY MANAGEMENT FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════════════

# Home directory for the dedicated HA user
HA_SSH_HOME="/home/$HA_SSH_USER"

# Ensure the dedicated HA user exists and has proper SSH directory
ensure_ha_user_setup() {
    # Check if HA user exists
    if ! id "$HA_SSH_USER" &>/dev/null; then
        echo -e "${YELLOW}Creating dedicated HA account: $HA_SSH_USER${NC}"
        useradd -r -m -d "$HA_SSH_HOME" -s /bin/bash -c "Spiral Pool HA Cluster" "$HA_SSH_USER" 2>/dev/null || true

        # Set up sudoers for this account (minimal permissions for HA operations)
        local sudoers_file="/etc/sudoers.d/spiralpool-ha"
        if [ ! -f "$sudoers_file" ]; then
            echo -e "${CYAN}Configuring sudo permissions for $HA_SSH_USER...${NC}"
            tee "$sudoers_file" > /dev/null << SUDOERSEOF
# Spiral Pool HA Cluster - Minimal sudo permissions
# This account can ONLY manage pool services and deploy config files

# Service management
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start spiralstratum, /bin/systemctl stop spiralstratum, /bin/systemctl restart spiralstratum
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start keepalived, /bin/systemctl stop keepalived, /bin/systemctl restart keepalived
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start digibyted, /bin/systemctl stop digibyted, /bin/systemctl restart digibyted, /bin/systemctl enable digibyted, /bin/systemctl disable digibyted
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start bitcoind, /bin/systemctl stop bitcoind, /bin/systemctl restart bitcoind, /bin/systemctl enable bitcoind, /bin/systemctl disable bitcoind
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start bitcoind-bch, /bin/systemctl stop bitcoind-bch, /bin/systemctl restart bitcoind-bch, /bin/systemctl enable bitcoind-bch, /bin/systemctl disable bitcoind-bch
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start bitcoiniid, /bin/systemctl stop bitcoiniid, /bin/systemctl restart bitcoiniid, /bin/systemctl enable bitcoiniid, /bin/systemctl disable bitcoiniid
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start litecoind, /bin/systemctl stop litecoind, /bin/systemctl restart litecoind, /bin/systemctl enable litecoind, /bin/systemctl disable litecoind
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start dogecoind, /bin/systemctl stop dogecoind, /bin/systemctl restart dogecoind, /bin/systemctl enable dogecoind, /bin/systemctl disable dogecoind
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start pepecoind, /bin/systemctl stop pepecoind, /bin/systemctl restart pepecoind, /bin/systemctl enable pepecoind, /bin/systemctl disable pepecoind
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start catcoind, /bin/systemctl stop catcoind, /bin/systemctl restart catcoind, /bin/systemctl enable catcoind, /bin/systemctl disable catcoind
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start fractald, /bin/systemctl stop fractald, /bin/systemctl restart fractald, /bin/systemctl enable fractald, /bin/systemctl disable fractald
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start namecoind, /bin/systemctl stop namecoind, /bin/systemctl restart namecoind, /bin/systemctl enable namecoind, /bin/systemctl disable namecoind
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start syscoind, /bin/systemctl stop syscoind, /bin/systemctl restart syscoind, /bin/systemctl enable syscoind, /bin/systemctl disable syscoind
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/systemctl start myriadcoind, /bin/systemctl stop myriadcoind, /bin/systemctl restart myriadcoind, /bin/systemctl enable myriadcoind, /bin/systemctl disable myriadcoind

# Config deployment (temp file from HA user home to config directory only)
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/mv $HA_SSH_HOME/.sp-sync-config.tmp /spiralpool/config/config.yaml
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/mv $HA_SSH_HOME/.sp-sync-ha.tmp /spiralpool/config/ha.yaml
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/mv $HA_SSH_HOME/.sp-sync-cluster.tmp /spiralpool/config/ha_cluster.conf
$HA_SSH_USER ALL=(ALL) NOPASSWD: /bin/chown root\:root /spiralpool/config/*

# Blockchain CLI queries (read-only)
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/digibyte-cli -conf=/spiralpool/dgb/digibyte.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/bitcoin-cli -conf=/spiralpool/btc/bitcoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/bitcoin-cli-bch -conf=/spiralpool/bch/bitcoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/bitcoinii-cli -conf=/spiralpool/bc2/bitcoinii.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/litecoin-cli -conf=/spiralpool/ltc/litecoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/dogecoin-cli -conf=/spiralpool/doge/dogecoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/fractal-cli -conf=/spiralpool/fbtc/fractal.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/pepecoin-cli -conf=/spiralpool/pep/pepecoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/catcoin-cli -conf=/spiralpool/cat/catcoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/namecoin-cli -conf=/spiralpool/nmc/namecoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/syscoin-cli -conf=/spiralpool/sys/syscoin.conf getblockchaininfo
$HA_SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/myriadcoin-cli -conf=/spiralpool/xmy/myriadcoin.conf getblockchaininfo
SUDOERSEOF
            chmod 440 "$sudoers_file"
            chown root:root "$sudoers_file"
            # Validate syntax
            if ! visudo -c -f "$sudoers_file" > /dev/null 2>&1; then
                echo -e "${RED}Sudoers syntax error - removing invalid file${NC}"
                rm -f "$sudoers_file"
            fi
        fi
    fi

    # Ensure SSH directory exists with correct permissions
    local ssh_dir="$HA_SSH_HOME/.ssh"
    mkdir -p "$ssh_dir"
    chmod 700 "$ssh_dir"
    chown "$HA_SSH_USER:$HA_SSH_USER" "$ssh_dir"

    # Create authorized_keys if doesn't exist
    touch "$ssh_dir/authorized_keys"
    chmod 600 "$ssh_dir/authorized_keys"
    chown "$HA_SSH_USER:$HA_SSH_USER" "$ssh_dir/authorized_keys"

    return 0
}

# Generate SSH key for HA communication (if not exists)
# Keys are generated for the dedicated HA user, not root (principle of least privilege)
generate_ha_ssh_key() {
    ensure_ha_user_setup

    local key_file="$HA_SSH_HOME/.ssh/id_ed25519"

    if [ -f "$key_file" ]; then
        echo -e "${GREEN}✓ SSH key already exists for $HA_SSH_USER${NC}"
        return 0
    fi

    echo -e "${CYAN}Generating ED25519 SSH key for HA cluster communication...${NC}"

    # Generate key as the HA user
    sudo -u "$HA_SSH_USER" ssh-keygen -t ed25519 -f "$key_file" -N "" -C "${HA_SSH_KEY_COMMENT}-$(hostname)" 2>/dev/null

    if [ $? -ne 0 ]; then
        echo -e "${RED}Failed to generate SSH key.${NC}"
        return 1
    fi

    chmod 600 "$key_file"
    chmod 644 "${key_file}.pub"
    chown "$HA_SSH_USER:$HA_SSH_USER" "$key_file" "${key_file}.pub"

    echo -e "${GREEN}✓ SSH key generated for $HA_SSH_USER${NC}"
    return 0
}

# Get the local public key for distribution to peers
get_local_public_key() {
    local key_file="$HA_SSH_HOME/.ssh/id_ed25519.pub"

    if [ ! -f "$key_file" ]; then
        generate_ha_ssh_key
    fi

    if [ -f "$key_file" ]; then
        cat "$key_file"
    else
        echo ""
    fi
}

# ═══════════════════════════════════════════════════════════════════════════════
# BOOTSTRAP TOKEN SYSTEM - Zero-password key exchange
# ═══════════════════════════════════════════════════════════════════════════════
# This allows fully automated SSH key exchange without requiring password auth.
#
# Flow:
#   1. Primary: pool-mode.sh --ha-init  (generates token, starts listener)
#   2. Secondary: pool-mode.sh --ha-join <primary-ip> --token <token>
#   3. Keys exchanged via token-authenticated channel
#   4. Full mesh SSH works after initial exchange
#
# Token format: spiral-bootstrap-<32-hex-chars> OR cluster token (spiral-<64-hex-chars>)
# Token validity: 24 hours (allows time for full HA cluster setup)
# ═══════════════════════════════════════════════════════════════════════════════

BOOTSTRAP_PORT=19876
BOOTSTRAP_TOKEN_FILE="$SPIRALPOOL_DIR/config/.ha-bootstrap-token"
BOOTSTRAP_TOKEN_VALIDITY=86400  # 24 hours - allows time for full HA setup on both nodes

# Generate a secure bootstrap token
generate_bootstrap_token() {
    local token="spiral-bootstrap-$(openssl rand -hex 16 2>/dev/null || head -c 32 /dev/urandom | xxd -p | head -c 32)"
    local expires=$(($(date +%s) + BOOTSTRAP_TOKEN_VALIDITY))

    mkdir -p "$SPIRALPOOL_DIR/config"
    echo "$token:$expires" > "$BOOTSTRAP_TOKEN_FILE"
    chmod 600 "$BOOTSTRAP_TOKEN_FILE"

    echo "$token"
}

# Validate a bootstrap token or cluster token
validate_bootstrap_token() {
    local provided_token=$1

    # First check bootstrap token file
    if [ -f "$BOOTSTRAP_TOKEN_FILE" ]; then
        local stored_data=$(cat "$BOOTSTRAP_TOKEN_FILE" 2>/dev/null)
        local stored_token="${stored_data%%:*}"
        local expires="${stored_data##*:}"
        local now=$(date +%s)

        # Check if token matches and not expired
        if [ "$now" -le "$expires" ] 2>/dev/null && [ "$provided_token" = "$stored_token" ]; then
            return 0
        fi

        # If expired, clean up
        if [ "$now" -gt "$expires" ] 2>/dev/null; then
            rm -f "$BOOTSTRAP_TOKEN_FILE"
        fi
    fi

    # Also accept the cluster token from config.yaml (for easier HA setup)
    # The cluster token is already shared during installation
    if [ -f "$SPIRALPOOL_DIR/config/config.yaml" ]; then
        local cluster_token=$(grep -E '^\s+token:' "$SPIRALPOOL_DIR/config/config.yaml" 2>/dev/null | head -1 | sed 's/.*token:\s*["'"'"']\?\([^"'"'"']*\)["'"'"']\?.*/\1/' | tr -d '[:space:]')
        if [ -n "$cluster_token" ] && [ "$provided_token" = "$cluster_token" ]; then
            return 0
        fi
    fi

    return 1
}

# Invalidate/remove the bootstrap token
invalidate_bootstrap_token() {
    rm -f "$BOOTSTRAP_TOKEN_FILE" 2>/dev/null
}

# Start the bootstrap listener as a background service
# This creates a simple systemd service that handles key exchange requests
start_bootstrap_listener() {
    local token=$1
    local timeout=${2:-$BOOTSTRAP_TOKEN_VALIDITY}
    local local_ip=$(get_local_ip)

    # Create the bin directory with proper permissions
    # Root owns directory, but spiralpool-ha can execute scripts in it
    mkdir -p /spiralpool/bin
    chown root:spiralpool-ha /spiralpool/bin
    chmod 755 /spiralpool/bin

    # Create a key exchange handler script (owned by root, executed with sudo)
    local handler_script="/spiralpool/bin/ha-key-exchange.sh"

    cat > "$handler_script" << 'HANDLER_EOF'
#!/bin/bash
# HA Key Exchange Handler
# Called via: sudo /spiralpool/bin/ha-key-exchange.sh <token> <pubkey>
# This script needs root to write to authorized_keys and read config files

TOKEN="$1"
THEIR_PUBKEY="$2"
TOKEN_FILE="/spiralpool/config/.ha-bootstrap-token"
CONFIG_FILE="/spiralpool/config/config.yaml"
HA_SSH_HOME="/home/spiralpool-ha"

# Validate token
valid=false

# Check bootstrap token file
if [ -f "$TOKEN_FILE" ]; then
    stored_data=$(cat "$TOKEN_FILE" 2>/dev/null)
    stored_token="${stored_data%%:*}"
    expires="${stored_data##*:}"
    now=$(date +%s)
    if [ "$now" -le "$expires" ] 2>/dev/null && [ "$TOKEN" = "$stored_token" ]; then
        valid=true
    fi
fi

# Also check cluster token in config.yaml
if [ "$valid" = "false" ] && [ -f "$CONFIG_FILE" ]; then
    cluster_token=$(grep -E '^\s+token:' "$CONFIG_FILE" 2>/dev/null | head -1 | sed 's/.*token:\s*["'"'"']\?\([^"'"'"']*\)["'"'"']\?.*/\1/' | tr -d '[:space:]')
    if [ -n "$cluster_token" ] && [ "$TOKEN" = "$cluster_token" ]; then
        valid=true
    fi
fi

if [ "$valid" = "false" ]; then
    echo "DENIED:Invalid token"
    exit 1
fi

# Validate pubkey format
if [[ ! "$THEIR_PUBKEY" =~ ^ssh-(ed25519|rsa|ecdsa) ]]; then
    echo "DENIED:Invalid key format"
    exit 1
fi

# Ensure .ssh directory exists with correct permissions
mkdir -p "$HA_SSH_HOME/.ssh"
chmod 700 "$HA_SSH_HOME/.ssh"
chown spiralpool-ha:spiralpool-ha "$HA_SSH_HOME/.ssh"

# Add their key to authorized_keys if not already present
if ! grep -qF "$THEIR_PUBKEY" "$HA_SSH_HOME/.ssh/authorized_keys" 2>/dev/null; then
    echo "$THEIR_PUBKEY" >> "$HA_SSH_HOME/.ssh/authorized_keys"
fi
chmod 600 "$HA_SSH_HOME/.ssh/authorized_keys"
chown spiralpool-ha:spiralpool-ha "$HA_SSH_HOME/.ssh/authorized_keys"

# Return our pubkey
our_pubkey=$(cat "$HA_SSH_HOME/.ssh/id_ed25519.pub" 2>/dev/null)
echo "OK:$our_pubkey"
HANDLER_EOF

    chmod 755 "$handler_script"
    chown root:root "$handler_script"

    # Allow spiralpool-ha to run this specific script via sudo without password
    # Add sudoers entry if not already present
    local sudoers_file="/etc/sudoers.d/spiralpool-ha-exchange"
    if [ ! -f "$sudoers_file" ]; then
        echo "spiralpool-ha ALL=(root) NOPASSWD: /spiralpool/bin/ha-key-exchange.sh" > "$sudoers_file"
        chmod 440 "$sudoers_file"
    fi

    # Ensure spiralpool-ha's .ssh directory is properly set up
    mkdir -p "$HA_SSH_HOME/.ssh"
    chmod 700 "$HA_SSH_HOME/.ssh"
    touch "$HA_SSH_HOME/.ssh/authorized_keys"
    chmod 600 "$HA_SSH_HOME/.ssh/authorized_keys"
    chown -R spiralpool-ha:spiralpool-ha "$HA_SSH_HOME/.ssh"

    echo ""
    echo -e "${GREEN}✓ Key exchange handler installed${NC}"
    echo ""
    echo -e "${CYAN}The primary node is now ready for backup nodes to join.${NC}"
    echo ""
    echo "Backup nodes will connect via SSH and exchange keys automatically."
    echo ""
}

# Join cluster using bootstrap token (secondary node)
join_with_bootstrap_token() {
    local primary_ip=$1
    local token=$2

    # Validate inputs
    if ! validate_server_address "$primary_ip"; then
        echo -e "${RED}Error: Invalid primary node address${NC}"
        return 1
    fi

    # Accept both bootstrap tokens (spiral-bootstrap-xxx) and cluster tokens (spiral-xxx)
    if [[ ! "$token" =~ ^spiral-[a-f0-9]{32,64}$ ]]; then
        echo -e "${RED}Error: Invalid token format${NC}"
        echo "Expected format: spiral-<hex> (bootstrap or cluster token)"
        return 1
    fi

    # Ensure we have our own key
    ensure_ha_user_setup
    generate_ha_ssh_key

    local local_pubkey=$(get_local_public_key)
    if [ -z "$local_pubkey" ]; then
        echo -e "${RED}Error: Failed to get local public key${NC}"
        return 1
    fi

    local local_ip=$(get_local_ip)

    echo ""
    echo -e "${CYAN}Connecting to primary node $primary_ip...${NC}"

    # Check if primary is reachable
    if ! ping -c 1 -W 3 "$primary_ip" &>/dev/null; then
        echo -e "${RED}Error: Cannot reach $primary_ip${NC}"
        return 1
    fi

    # Connect via SSH and run the key exchange handler
    echo ""
    echo -e "${CYAN}Exchanging SSH keys with primary node...${NC}"

    # Try multiple methods for initial connection
    local response=""
    local ssh_exit=1

    # Method 1: Try key-based auth first (in case keys were manually exchanged)
    echo "  Trying key-based authentication..."
    response=$(ssh -o ConnectTimeout=5 -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
        "${HA_SSH_USER}@${primary_ip}" \
        "sudo /spiralpool/bin/ha-key-exchange.sh '$token' '$local_pubkey'" 2>/dev/null)
    ssh_exit=$?

    if [ $ssh_exit -ne 0 ]; then
        # Method 2: Try as current user with sudo (for admin access)
        echo "  Trying current user with sudo..."
        local current_user=$(whoami)
        response=$(ssh -o ConnectTimeout=5 -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
            "${current_user}@${primary_ip}" \
            "sudo /spiralpool/bin/ha-key-exchange.sh '$token' '$local_pubkey'" 2>/dev/null)
        ssh_exit=$?
    fi

    if [ $ssh_exit -ne 0 ]; then
        # Method 3: Interactive password prompt as current user
        echo ""
        echo -e "${YELLOW}Key-based authentication not yet available.${NC}"
        echo -e "${YELLOW}Please enter your password for SSH access to the primary node:${NC}"
        echo ""

        # Try interactive SSH as current user (who should have sudo on both machines)
        local current_user=$(whoami)
        response=$(ssh -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new \
            "${current_user}@${primary_ip}" \
            "sudo /spiralpool/bin/ha-key-exchange.sh '$token' '$local_pubkey'" 2>&1)
        ssh_exit=$?
    fi

    if [ $ssh_exit -ne 0 ]; then
        echo ""
        echo -e "${RED}Error: SSH connection failed${NC}"
        echo ""
        echo "Unable to connect to primary node. Please ensure:"
        echo "  1. Primary node has run: sudo pool-mode.sh --ha-init"
        echo "  2. You can SSH to $primary_ip from this machine"
        echo "  3. Your user account has sudo access on the primary"
        echo ""
        echo "Alternative: Manually exchange keys"
        echo "  On this node, get your public key:"
        echo "    cat /home/spiralpool-ha/.ssh/id_ed25519.pub"
        echo "  Add it to primary's authorized_keys:"
        echo "    sudo tee -a /home/spiralpool-ha/.ssh/authorized_keys"
        echo "  Then get primary's key and add it here the same way."
        return 1
    fi

    # Parse response
    if [[ "$response" =~ ^OK:(.+)$ ]]; then
        local their_pubkey="${BASH_REMATCH[1]}"

        # Validate and add their key
        if validate_public_key "$their_pubkey"; then
            add_authorized_key "$their_pubkey"
            echo -e "${GREEN}✓ Received and added primary node's public key${NC}"

            # Add to cluster config
            add_peer_to_cluster_config "$primary_ip"
            echo -e "${GREEN}✓ Added $primary_ip to cluster configuration${NC}"

            # Test SSH access (should work without password now)
            sleep 1
            if check_ssh_access "$primary_ip"; then
                echo -e "${GREEN}✓ SSH key authentication working!${NC}"

                # Now use SSH to get full cluster membership
                echo ""
                echo -e "${CYAN}Fetching cluster membership...${NC}"
                local cluster_members=$(sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${primary_ip}" "
                    if [ -f '$HA_CLUSTER_FILE' ]; then
                        grep -E '^backup_servers:' '$HA_CLUSTER_FILE' 2>/dev/null | sed 's/^backup_servers:\s*//'
                    fi
                " 2>/dev/null)

                if [ -n "$cluster_members" ]; then
                    IFS=',' read -ra members <<< "$cluster_members"
                    for member in "${members[@]}"; do
                        member=$(echo "$member" | xargs)
                        if [ -n "$member" ] && [ "$member" != "$local_ip" ] && [ "$member" != "$primary_ip" ]; then
                            add_peer_to_cluster_config "$member"
                            echo -e "  Added peer: $member"
                        fi
                    done
                fi

                # Tell the primary to add us to their cluster config
                echo ""
                echo -e "${CYAN}Registering with primary node...${NC}"
                sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${primary_ip}" "
                    # Add this node to cluster config
                    if [ -f '$HA_CLUSTER_FILE' ]; then
                        current_backups=\$(grep -E '^backup_servers:' '$HA_CLUSTER_FILE' 2>/dev/null | sed 's/^backup_servers:\s*//')
                        if ! echo \"\$current_backups\" | grep -q '$local_ip'; then
                            if [ -n \"\$current_backups\" ]; then
                                new_backups=\"\${current_backups},$local_ip\"
                            else
                                new_backups=\"$local_ip\"
                            fi
                            grep -v '^backup_servers:' '$HA_CLUSTER_FILE' > '${HA_CLUSTER_FILE}.tmp' 2>/dev/null || true; echo \"backup_servers: \$new_backups\" >> '${HA_CLUSTER_FILE}.tmp'; mv '${HA_CLUSTER_FILE}.tmp' '$HA_CLUSTER_FILE'
                        fi
                    fi
                " 2>/dev/null
                echo -e "${GREEN}✓ Registered with primary${NC}"

                echo ""
                echo -e "${GREEN}Successfully joined cluster!${NC}"
                echo ""
                echo "The cluster is now configured. Both nodes can communicate via SSH."
                echo ""
                return 0
            else
                echo -e "${YELLOW}Warning: SSH key test failed. Keys may need manual verification.${NC}"
                return 1
            fi
        else
            echo -e "${RED}Error: Received invalid public key from primary${NC}"
            return 1
        fi
    elif [[ "$response" =~ ^DENIED:(.+)$ ]]; then
        local reason="${BASH_REMATCH[1]}"
        echo -e "${RED}Error: Access denied by primary node${NC}"
        echo "Reason: $reason"
        echo ""
        echo "Common causes:"
        echo "  - Invalid or expired token"
        echo "  - Primary node hasn't run --ha-init yet"
        return 1
    else
        echo -e "${RED}Error: Unexpected response from primary node${NC}"
        echo "Response: $response"
        echo ""
        echo "Ensure the primary has run: sudo pool-mode.sh --ha-init"
        return 1
    fi
}

# Check if SSH key-based access to a remote server is available
# Returns: 0 = key auth works, 1 = no key auth (would need password), 2 = unreachable, 3 = invalid address
check_ssh_access() {
    local server=$1

    # SECURITY: Validate server address to prevent command injection
    if ! validate_server_address "$server"; then
        return 3  # Invalid address
    fi

    # First check if server is reachable at all
    if ! ping -c 1 -W 2 "$server" &>/dev/null; then
        return 2  # Unreachable
    fi

    # Try key-based auth as HA user (BatchMode=yes means it won't prompt for password)
    # Both sides use the same dedicated HA account: spiralpool-ha
    if sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes -o StrictHostKeyChecking=accept-new "${HA_SSH_USER}@${server}" "echo ok" &>/dev/null; then
        return 0  # Key auth works
    else
        return 1  # Key auth failed (would need password or key setup)
    fi
}

# Add a public key to the local authorized_keys for HA user
add_authorized_key() {
    local pubkey=$1
    local auth_keys="$HA_SSH_HOME/.ssh/authorized_keys"

    # SECURITY: Validate the public key format to prevent injection
    if ! validate_public_key "$pubkey"; then
        echo -e "${RED}Rejected invalid public key format${NC}" >&2
        return 1
    fi

    ensure_ha_user_setup

    # Check if key already exists
    if grep -qF "$pubkey" "$auth_keys" 2>/dev/null; then
        return 0  # Already exists
    fi

    # Add the key
    echo "$pubkey" >> "$auth_keys"
    chmod 600 "$auth_keys"
    chown "$HA_SSH_USER:$HA_SSH_USER" "$auth_keys"

    return 0
}

# Setup SSH key-based authentication to a remote server
# This is required for HA cluster communication
setup_ssh_keys() {
    local server=$1

    # SECURITY: Validate server address to prevent command injection
    if ! validate_server_address "$server"; then
        echo -e "${RED}Error: Invalid server address${NC}"
        return 1
    fi

    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  SSH KEY SETUP FOR HA CLUSTER - ${server}${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "${YELLOW}SSH key-based authentication is required for HA cluster communication.${NC}"
    echo ""
    echo -e "${GREEN}Security Model:${NC}"
    echo "  • A dedicated HA account '$HA_SSH_USER' is used on ALL servers"
    echo "  • Only PUBLIC keys are exchanged (safe to transmit)"
    echo "  • Private keys NEVER leave the server they were generated on"
    echo "  • Uses ED25519 encryption (modern, secure, fast)"
    echo ""

    # Ensure local key exists
    if ! generate_ha_ssh_key; then
        echo -e "${RED}Failed to ensure local SSH key exists.${NC}"
        return 1
    fi

    local local_pubkey=$(get_local_public_key)
    if [ -z "$local_pubkey" ]; then
        echo -e "${RED}Failed to get local public key.${NC}"
        return 1
    fi

    echo -e "${CYAN}Local public key:${NC}"
    echo -e "  ${BLUE}$local_pubkey${NC}"
    echo ""

    # Check if we already have access
    if check_ssh_access "$server"; then
        echo -e "${GREEN}✓ SSH key auth already working to $server${NC}"
        return 0
    fi

    echo -e "${CYAN}To set up bidirectional SSH access with ${server}:${NC}"
    echo ""
    echo -e "${GREEN}Option 1: Automatic exchange (requires temporary password auth)${NC}"
    echo "  You will be prompted for the password for ${HA_SSH_USER}@$server"
    echo "  This will:"
    echo "    1. Copy YOUR public key TO $server"
    echo "    2. Retrieve $server's public key and add it HERE"
    echo ""
    echo -e "${GREEN}Option 2: Manual exchange${NC}"
    echo "  1. Copy the public key above to $server:"
    echo "     Add to $HA_SSH_HOME/.ssh/authorized_keys on $server"
    echo "  2. Get $server's public key and add it here:"
    echo "     sudo pool-mode.sh --ha-add-key \"<their-public-key>\""
    echo ""

    read -p "Attempt automatic exchange? (requires ${HA_SSH_USER} password on $server) (y/N): " do_auto
    if [[ "$do_auto" =~ ^[Yy]$ ]]; then
        echo ""
        echo -e "${CYAN}Step 1: Copying local public key to $server...${NC}"
        echo -e "${YELLOW}Enter password for ${HA_SSH_USER}@$server when prompted${NC}"
        echo ""

        # Copy our key to remote server
        sudo -u "$HA_SSH_USER" ssh-copy-id -i "$HA_SSH_HOME/.ssh/id_ed25519.pub" "${HA_SSH_USER}@${server}" 2>/dev/null

        if [ $? -eq 0 ]; then
            echo -e "${GREEN}✓ Local public key copied to $server${NC}"
        else
            echo -e "${RED}Failed to copy key to $server.${NC}"
            echo ""
            echo -e "${YELLOW}Troubleshooting:${NC}"
            echo "  1. Ensure $HA_SSH_USER account exists on $server"
            echo "     (Run: sudo pool-mode.sh --ha-setup on $server first)"
            echo "  2. Ensure password auth is enabled on $server (temporarily)"
            echo "  3. Verify the password for $HA_SSH_USER on $server"
            return 1
        fi

        echo ""
        echo -e "${CYAN}Step 2: Retrieving $server's public key...${NC}"

        # Get remote server's public key
        local remote_pubkey=$(sudo -u "$HA_SSH_USER" ssh -o StrictHostKeyChecking=accept-new "${HA_SSH_USER}@${server}" "cat ~/.ssh/id_ed25519.pub 2>/dev/null || cat ~/.ssh/id_rsa.pub 2>/dev/null" 2>/dev/null)

        if [ -n "$remote_pubkey" ]; then
            add_authorized_key "$remote_pubkey"
            echo -e "${GREEN}✓ Added $server's public key to local authorized_keys${NC}"
        else
            echo -e "${YELLOW}⚠ Could not retrieve $server's public key.${NC}"
            echo "  $server may need to generate keys first."
            echo "  Run: sudo pool-mode.sh --ha-setup on $server"
        fi

        echo ""

        # Verify bidirectional access
        if check_ssh_access "$server"; then
            echo -e "${GREEN}✓ SSH key authentication verified to $server${NC}"
            return 0
        else
            echo -e "${YELLOW}⚠ Key auth not fully working yet.${NC}"
            echo "  You may need to run --ha-setup on $server as well."
            return 1
        fi
    else
        echo ""
        echo -e "${YELLOW}Manual setup required.${NC}"
        echo ""
        echo "1. On $server, add this key to $HA_SSH_HOME/.ssh/authorized_keys:"
        echo -e "   ${BLUE}$local_pubkey${NC}"
        echo ""
        echo "2. Then get $server's public key and run here:"
        echo "   sudo pool-mode.sh --ha-add-key \"<server-public-key>\""
        echo ""
        return 1
    fi
}

# Add an authorized key from command line (for manual setup)
add_ha_authorized_key_cli() {
    local pubkey=$1

    if [ -z "$pubkey" ]; then
        echo -e "${RED}Error: No public key provided${NC}"
        echo "Usage: pool-mode.sh --ha-add-key \"ssh-ed25519 AAAA... comment\""
        return 1
    fi

    # SECURITY: Use the validate_public_key function for comprehensive validation
    if ! validate_public_key "$pubkey"; then
        echo -e "${RED}Error: Invalid public key format${NC}"
        echo "Key should start with 'ssh-ed25519', 'ssh-rsa', or 'ssh-ecdsa'"
        echo "Key must not contain shell metacharacters"
        return 1
    fi

    ensure_ha_user_setup

    # add_authorized_key also validates, but we've pre-validated above
    if add_authorized_key "$pubkey"; then
        echo -e "${GREEN}✓ Public key added to authorized_keys${NC}"
        return 0
    else
        echo -e "${RED}Failed to add public key${NC}"
        return 1
    fi
}

# Configure sshd for key-based authentication (does NOT restart service)
configure_sshd_for_keys() {
    local sshd_config="/etc/ssh/sshd_config"
    local backup_config="/etc/ssh/sshd_config.backup.$(date +%Y%m%d_%H%M%S)"

    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  SSH SERVICE CONFIGURATION${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""

    # Backup current config
    cp "$sshd_config" "$backup_config"
    echo -e "${GREEN}✓ Backed up sshd_config to $backup_config${NC}"

    # Check current settings
    local pubkey_auth=$(grep -E "^PubkeyAuthentication" "$sshd_config" | awk '{print $2}')
    local password_auth=$(grep -E "^PasswordAuthentication" "$sshd_config" | awk '{print $2}')

    echo ""
    echo -e "${CYAN}Current SSH settings:${NC}"
    echo "  PubkeyAuthentication: ${pubkey_auth:-not set (default: yes)}"
    echo "  PasswordAuthentication: ${password_auth:-not set (default: yes)}"
    echo ""

    # Ensure PubkeyAuthentication is enabled
    if [ "$pubkey_auth" != "yes" ]; then
        if grep -qE "^#?PubkeyAuthentication" "$sshd_config"; then
            sed -i 's/^#*PubkeyAuthentication.*/PubkeyAuthentication yes/' "$sshd_config"
        else
            echo "PubkeyAuthentication yes" >> "$sshd_config"
        fi
        echo -e "${GREEN}✓ Enabled PubkeyAuthentication${NC}"
    else
        echo -e "${BLUE}○ PubkeyAuthentication already enabled${NC}"
    fi

    echo ""
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${YELLOW}  IMPORTANT: SSH SERVICE RESTART REQUIRED${NC}"
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "${RED}WARNING: Restarting SSH now may disconnect your current session!${NC}"
    echo ""
    echo "To apply SSH configuration changes, run:"
    echo -e "  ${BLUE}sudo systemctl restart sshd${NC}"
    echo ""
    echo "Before restarting, ensure you have:"
    echo "  1. Verified SSH key access works from another terminal"
    echo "  2. Console access or IPMI/ILO available as backup"
    echo ""

    # Ask about disabling password auth
    echo -e "${CYAN}Optional: Disable password authentication for extra security?${NC}"
    echo "This prevents brute-force password attacks but requires key auth."
    echo ""
    read -p "Disable password authentication? (y/N): " disable_pw
    if [[ "$disable_pw" =~ ^[Yy]$ ]]; then
        if grep -qE "^#?PasswordAuthentication" "$sshd_config"; then
            sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' "$sshd_config"
        else
            echo "PasswordAuthentication no" >> "$sshd_config"
        fi
        echo -e "${GREEN}✓ PasswordAuthentication set to 'no' (takes effect after restart)${NC}"
        echo ""
        echo -e "${RED}CRITICAL: Verify key access works BEFORE restarting sshd!${NC}"
    else
        echo -e "${BLUE}○ Password authentication remains enabled${NC}"
    fi

    echo ""
    return 0
}

# ═══════════════════════════════════════════════════════════════════════════════
# HA CLUSTER AUTO-DISCOVERY AND SELF-HEALING
# ═══════════════════════════════════════════════════════════════════════════════

# Validate that a string is a safe IP address or hostname
# Prevents command injection through malicious "server" addresses
# Returns 0 if valid, 1 if invalid
validate_server_address() {
    local addr=$1

    # Must not be empty
    if [ -z "$addr" ]; then
        return 1
    fi

    # Must not contain dangerous characters (shell metacharacters)
    # Only allow: alphanumeric, dots, hyphens, colons (for port notation)
    if [[ "$addr" =~ [^a-zA-Z0-9.:-] ]]; then
        echo -e "${RED}Invalid server address: contains illegal characters${NC}" >&2
        return 1
    fi

    # Must not start with a hyphen (could be interpreted as an option)
    if [[ "$addr" =~ ^- ]]; then
        echo -e "${RED}Invalid server address: cannot start with hyphen${NC}" >&2
        return 1
    fi

    # Must be reasonable length
    if [ ${#addr} -gt 253 ]; then
        echo -e "${RED}Invalid server address: too long${NC}" >&2
        return 1
    fi

    # IPv4 validation (if it looks like an IP)
    if [[ "$addr" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        # Validate each octet is 0-255
        local IFS='.'
        read -ra octets <<< "$addr"
        for octet in "${octets[@]}"; do
            if [ "$octet" -lt 0 ] || [ "$octet" -gt 255 ] 2>/dev/null; then
                echo -e "${RED}Invalid IPv4 address: octet out of range${NC}" >&2
                return 1
            fi
        done
    fi

    return 0
}

# Validate that a string is a safe SSH public key
# Returns 0 if valid, 1 if invalid
validate_public_key() {
    local key=$1

    # Must not be empty
    if [ -z "$key" ]; then
        return 1
    fi

    # Must start with a valid key type
    if [[ ! "$key" =~ ^ssh-(ed25519|rsa|ecdsa|dss)[[:space:]] ]]; then
        return 1
    fi

    # Must not contain shell metacharacters (except space which is expected)
    # Public keys are base64 encoded, so valid chars are: A-Za-z0-9+/= and space
    if [[ "$key" =~ [\;\|\&\$\`\(\)\{\}\[\]\<\>\!\~\*\?] ]]; then
        return 1
    fi

    return 0
}

# Get this node's IP address (best guess for cluster communication)
get_local_ip() {
    # Try to get the IP that would be used to reach the internet
    local ip=$(ip route get 1.1.1.1 2>/dev/null | grep -oP 'src \K[^ ]+')
    if [ -z "$ip" ]; then
        # Fallback: first non-loopback IP
        ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    fi
    echo "$ip"
}

# Get this node's hostname
get_local_hostname() {
    hostname -f 2>/dev/null || hostname
}

# Add a peer to the local cluster configuration
add_peer_to_cluster_config() {
    local new_peer=$1
    local local_ip=$(get_local_ip)

    # Validate the server address to prevent command injection
    if ! validate_server_address "$new_peer"; then
        echo -e "${RED}Rejected invalid peer address: $new_peer${NC}" >&2
        return 1
    fi

    # Don't add ourselves
    if [ "$new_peer" = "$local_ip" ] || [ "$new_peer" = "$(get_local_hostname)" ] || [ "$new_peer" = "127.0.0.1" ] || [ "$new_peer" = "localhost" ]; then
        return 0
    fi

    mkdir -p "$SPIRALPOOL_DIR/config"

    # Check if peer already in config
    if [ -f "$HA_CLUSTER_FILE" ]; then
        if grep -qF "$new_peer" "$HA_CLUSTER_FILE" 2>/dev/null; then
            return 0  # Already exists
        fi
    fi

    # Add peer to config
    if [ -f "$HA_CLUSTER_FILE" ]; then
        # Append to existing backup_servers line
        local current=$(grep -E "^backup_servers:" "$HA_CLUSTER_FILE" | sed 's/^backup_servers:\s*//')
        if [ -n "$current" ]; then
            sed -i "s|^backup_servers:.*|backup_servers: ${current}, ${new_peer}|" "$HA_CLUSTER_FILE"
        else
            echo "backup_servers: $new_peer" >> "$HA_CLUSTER_FILE"
        fi
    else
        echo "# HA Cluster Peers" > "$HA_CLUSTER_FILE"
        echo "# Auto-generated on $(date)" >> "$HA_CLUSTER_FILE"
        echo "backup_servers: $new_peer" >> "$HA_CLUSTER_FILE"
    fi

    return 0
}

# Remove a peer from the local cluster configuration
remove_peer_from_cluster_config() {
    local dead_peer=$1

    # Validate the server address to prevent command injection
    if ! validate_server_address "$dead_peer"; then
        echo -e "${RED}Rejected invalid peer address: $dead_peer${NC}" >&2
        return 1
    fi

    if [ ! -f "$HA_CLUSTER_FILE" ]; then
        return 0
    fi

    # Get current peers
    local current=$(grep -E "^backup_servers:" "$HA_CLUSTER_FILE" | sed 's/^backup_servers:\s*//')
    if [ -z "$current" ]; then
        return 0
    fi

    # Remove the dead peer and rebuild
    local new_list=""
    IFS=',' read -ra peers <<< "$current"
    for peer in "${peers[@]}"; do
        peer=$(echo "$peer" | xargs)  # Trim
        if [ "$peer" != "$dead_peer" ] && [ -n "$peer" ]; then
            if [ -z "$new_list" ]; then
                new_list="$peer"
            else
                new_list="$new_list, $peer"
            fi
        fi
    done

    if [ -n "$new_list" ]; then
        sed -i "s|^backup_servers:.*|backup_servers: $new_list|" "$HA_CLUSTER_FILE"
    else
        # No peers left, remove the line
        sed -i '/^backup_servers:/d' "$HA_CLUSTER_FILE"
    fi

    return 0
}

# Announce this node to an existing cluster (join request)
# When a new node wants to join, it contacts existing peers
announce_to_cluster() {
    local contact_peer=$1

    # SECURITY: Validate the contact peer address before any operations
    if ! validate_server_address "$contact_peer"; then
        echo -e "${RED}Error: Invalid contact peer address${NC}"
        return 1
    fi

    local local_ip=$(get_local_ip)
    local local_hostname=$(get_local_hostname)

    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  ANNOUNCING TO EXISTING CLUSTER${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "This node: ${GREEN}$local_ip${NC} (${local_hostname})"
    echo -e "Contact peer: ${BLUE}$contact_peer${NC}"
    echo ""

    # Ensure we have SSH keys set up
    ensure_ha_user_setup
    generate_ha_ssh_key

    local local_pubkey=$(get_local_public_key)

    # Check if we can reach the contact peer
    if ! ping -c 1 -W 2 "$contact_peer" &>/dev/null; then
        echo -e "${RED}Cannot reach $contact_peer${NC}"
        return 1
    fi

    # Check if SSH access already works
    if check_ssh_access "$contact_peer"; then
        echo -e "${GREEN}✓ Already have SSH access to $contact_peer${NC}"
    else
        echo -e "${YELLOW}SSH key exchange needed with $contact_peer${NC}"
        setup_ssh_keys "$contact_peer"

        if ! check_ssh_access "$contact_peer"; then
            echo -e "${RED}Failed to establish SSH access to $contact_peer${NC}"
            return 1
        fi
    fi

    echo ""
    echo -e "${CYAN}Requesting cluster membership from $contact_peer...${NC}"

    # Get the list of all cluster members from the contact peer
    local cluster_members=$(sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${contact_peer}" "
        if [ -f '$HA_CLUSTER_FILE' ]; then
            grep -E '^backup_servers:' '$HA_CLUSTER_FILE' 2>/dev/null | sed 's/^backup_servers:\s*//'
        fi
    " 2>/dev/null)

    echo ""

    # Add contact peer to our config
    add_peer_to_cluster_config "$contact_peer"
    echo -e "${GREEN}✓ Added $contact_peer to local cluster config${NC}"

    # Add any other cluster members
    if [ -n "$cluster_members" ]; then
        IFS=',' read -ra members <<< "$cluster_members"
        for member in "${members[@]}"; do
            member=$(echo "$member" | xargs)  # Trim
            if [ -n "$member" ] && [ "$member" != "$local_ip" ] && [ "$member" != "$local_hostname" ]; then
                add_peer_to_cluster_config "$member"
                echo -e "${GREEN}✓ Added $member to local cluster config${NC}"
            fi
        done
    fi

    # Now announce ourselves to all known peers
    echo ""
    echo -e "${CYAN}Announcing this node to all cluster members...${NC}"

    detect_ha_enabled  # Refresh the peer list

    for server in "${HA_BACKUP_SERVERS[@]}"; do
        echo -ne "  Announcing to $server... "

        if check_ssh_access "$server"; then
            # Tell the remote server to add us
            sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${server}" "
                # Add the new node to cluster config
                if [ -f '$HA_CLUSTER_FILE' ]; then
                    if ! grep -qF '$local_ip' '$HA_CLUSTER_FILE' 2>/dev/null; then
                        current=\$(grep -E '^backup_servers:' '$HA_CLUSTER_FILE' | sed 's/^backup_servers:\s*//')
                        if [ -n \"\$current\" ]; then
                            sudo grep -v '^backup_servers:' '$HA_CLUSTER_FILE' > ~/._sp_cluster.tmp 2>/dev/null || true; echo \"backup_servers: \${current}, $local_ip\" >> ~/._sp_cluster.tmp; sudo mv ~/._sp_cluster.tmp '$HA_CLUSTER_FILE'
                        else
                            echo 'backup_servers: $local_ip' | sudo tee -a '$HA_CLUSTER_FILE' > /dev/null
                        fi
                    fi
                else
                    echo '# HA Cluster Peers' | sudo tee '$HA_CLUSTER_FILE' > /dev/null
                    echo 'backup_servers: $local_ip' | sudo tee -a '$HA_CLUSTER_FILE' > /dev/null
                fi
            " 2>/dev/null

            # Add our public key to the remote server
            echo "$local_pubkey" | sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${server}" "
                mkdir -p ~/.ssh
                chmod 700 ~/.ssh
                touch ~/.ssh/authorized_keys
                chmod 600 ~/.ssh/authorized_keys
                if ! grep -qF '$local_pubkey' ~/.ssh/authorized_keys 2>/dev/null; then
                    echo '$local_pubkey' >> ~/.ssh/authorized_keys
                fi
            " 2>/dev/null

            echo -e "${GREEN}✓${NC}"
        else
            echo -e "${YELLOW}⚠ needs key setup${NC}"
        fi
    done

    # Distribute all keys across the cluster for full mesh
    echo ""
    echo -e "${CYAN}Establishing full mesh key distribution...${NC}"
    distribute_keys_to_cluster

    echo ""
    echo -e "${GREEN}Cluster join complete.${NC}"
    echo ""
    echo "This node is now part of the HA cluster."
    echo "Run 'pool-mode.sh --ha-status' to verify cluster status."
    echo ""

    return 0
}

# Propagate cluster membership changes to all nodes
# Call this after adding/removing a node to ensure all nodes have consistent config
propagate_cluster_membership() {
    detect_ha_enabled

    if [ ${#HA_BACKUP_SERVERS[@]} -eq 0 ]; then
        echo -e "${YELLOW}No HA peers to propagate to.${NC}"
        return 0
    fi

    local local_ip=$(get_local_ip)

    # Build the full cluster list (all peers + ourselves)
    local full_cluster="$local_ip"
    for server in "${HA_BACKUP_SERVERS[@]}"; do
        full_cluster="$full_cluster, $server"
    done

    echo ""
    echo -e "${CYAN}Propagating cluster membership to all nodes...${NC}"
    echo -e "Full cluster: ${GREEN}$full_cluster${NC}"
    echo ""

    for server in "${HA_BACKUP_SERVERS[@]}"; do
        echo -ne "  Updating $server... "

        if check_ssh_access "$server"; then
            # Update the remote server's cluster config
            # Each server's config should have all OTHER servers (not itself)
            local peers_for_remote=""
            for peer in "${HA_BACKUP_SERVERS[@]}"; do
                if [ "$peer" != "$server" ]; then
                    if [ -z "$peers_for_remote" ]; then
                        peers_for_remote="$peer"
                    else
                        peers_for_remote="$peers_for_remote, $peer"
                    fi
                fi
            done
            # Add ourselves to their list
            if [ -z "$peers_for_remote" ]; then
                peers_for_remote="$local_ip"
            else
                peers_for_remote="$peers_for_remote, $local_ip"
            fi

            sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${server}" "
                sudo mkdir -p '$SPIRALPOOL_DIR/config'
                if [ -f '$HA_CLUSTER_FILE' ]; then
                    sudo grep -v '^backup_servers:' '$HA_CLUSTER_FILE' > ~/._sp_cluster.tmp 2>/dev/null || true; echo 'backup_servers: $peers_for_remote' >> ~/._sp_cluster.tmp; sudo mv ~/._sp_cluster.tmp '$HA_CLUSTER_FILE'
                else
                    echo '# HA Cluster Peers' | sudo tee '$HA_CLUSTER_FILE' > /dev/null
                    echo '# Propagated from cluster on \$(date)' | sudo tee -a '$HA_CLUSTER_FILE' > /dev/null
                    echo 'backup_servers: $peers_for_remote' | sudo tee -a '$HA_CLUSTER_FILE' > /dev/null
                fi
            " 2>/dev/null
            echo -e "${GREEN}✓${NC}"
        else
            echo -e "${YELLOW}⚠ not accessible${NC}"
        fi
    done

    echo ""
    echo -e "${GREEN}Cluster membership propagated.${NC}"
    return 0
}

# Check cluster health and perform self-healing
# Detects failed nodes and updates cluster config accordingly
check_cluster_health() {
    detect_ha_enabled

    if [ ${#HA_BACKUP_SERVERS[@]} -eq 0 ]; then
        echo -e "${YELLOW}No HA peers configured.${NC}"
        return 0
    fi

    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  CLUSTER HEALTH CHECK${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""

    local healthy_nodes=()
    local failed_nodes=()
    local degraded_nodes=()

    for server in "${HA_BACKUP_SERVERS[@]}"; do
        echo -ne "  Checking $server... "

        # Check network reachability
        if ! ping -c 1 -W 2 "$server" &>/dev/null; then
            echo -e "${RED}✗ UNREACHABLE${NC}"
            failed_nodes+=("$server")
            continue
        fi

        # Check SSH access
        if ! check_ssh_access "$server"; then
            echo -e "${YELLOW}⚠ SSH key auth failed${NC}"
            degraded_nodes+=("$server")
            continue
        fi

        # Check if spiralstratum is running
        local stratum_status=$(sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${server}" "
            if systemctl is-active --quiet spiralstratum 2>/dev/null; then
                echo 'running'
            else
                echo 'stopped'
            fi
        " 2>/dev/null)

        if [ "$stratum_status" = "running" ]; then
            echo -e "${GREEN}✓ HEALTHY${NC}"
            healthy_nodes+=("$server")
        else
            echo -e "${YELLOW}⚠ stratum not running${NC}"
            degraded_nodes+=("$server")
        fi
    done

    echo ""
    echo -e "${CYAN}Cluster Summary:${NC}"
    echo -e "  ${GREEN}Healthy:${NC}  ${#healthy_nodes[@]} nodes"
    echo -e "  ${YELLOW}Degraded:${NC} ${#degraded_nodes[@]} nodes"
    echo -e "  ${RED}Failed:${NC}   ${#failed_nodes[@]} nodes"
    echo ""

    # Offer to heal degraded nodes
    if [ ${#degraded_nodes[@]} -gt 0 ]; then
        echo -e "${YELLOW}Degraded nodes may need attention:${NC}"
        for node in "${degraded_nodes[@]}"; do
            echo "  • $node"
        done
        echo ""
        read -p "Attempt to heal degraded nodes? (y/N): " do_heal
        if [[ "$do_heal" =~ ^[Yy]$ ]]; then
            for node in "${degraded_nodes[@]}"; do
                echo ""
                echo -e "${CYAN}Healing $node...${NC}"

                # Try to set up SSH keys if that's the issue
                if ! check_ssh_access "$node"; then
                    setup_ssh_keys "$node"
                fi

                # Try to restart stratum if reachable
                if check_ssh_access "$node"; then
                    echo -ne "  Restarting spiralstratum on $node... "
                    sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${node}" "
                        sudo systemctl restart spiralstratum 2>/dev/null
                    " 2>/dev/null
                    echo -e "${GREEN}✓${NC}"
                fi
            done
        fi
    fi

    # Offer to remove failed nodes
    if [ ${#failed_nodes[@]} -gt 0 ]; then
        echo ""
        echo -e "${RED}Failed nodes are unreachable:${NC}"
        for node in "${failed_nodes[@]}"; do
            echo "  • $node"
        done
        echo ""
        echo -e "${YELLOW}These nodes may be permanently down or have network issues.${NC}"
        read -p "Remove failed nodes from cluster config? (y/N): " do_remove
        if [[ "$do_remove" =~ ^[Yy]$ ]]; then
            for node in "${failed_nodes[@]}"; do
                remove_peer_from_cluster_config "$node"
                echo -e "${GREEN}✓ Removed $node from local cluster config${NC}"
            done

            # Propagate the change to healthy nodes
            echo ""
            echo -e "${CYAN}Propagating changes to healthy nodes...${NC}"
            propagate_cluster_membership
        fi
    fi

    return 0
}

# Distribute public keys to ALL peers in the cluster
# This ensures every node can SSH to every other node (mesh topology)
distribute_keys_to_cluster() {
    detect_ha_enabled

    if [ ${#HA_BACKUP_SERVERS[@]} -eq 0 ]; then
        echo -e "${YELLOW}No HA backup servers configured.${NC}"
        return 1
    fi

    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  CLUSTER-WIDE KEY DISTRIBUTION${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "${GREEN}How SSH key distribution works:${NC}"
    echo "  • Each server generates its OWN unique key pair"
    echo "  • Private keys NEVER leave their origin server"
    echo "  • Public keys are collected from ALL servers"
    echo "  • Each server's authorized_keys contains ALL peers' public keys"
    echo "  • Result: Any server can SSH to any other server (mesh topology)"
    echo ""

    # Ensure local key exists
    ensure_ha_user_setup
    generate_ha_ssh_key

    local local_pubkey=$(get_local_public_key)
    local local_hostname=$(hostname)

    echo -e "${CYAN}Step 1: Collecting public keys from all cluster nodes...${NC}"
    echo ""

    # Array to hold all public keys (including local)
    declare -A cluster_keys
    cluster_keys["$local_hostname"]="$local_pubkey"
    echo -e "  ${GREEN}✓${NC} Local ($local_hostname)"

    # Collect public keys from all peers
    for server in "${HA_BACKUP_SERVERS[@]}"; do
        echo -ne "  Checking $server... "

        # First check if we can already SSH to this server
        if check_ssh_access "$server"; then
            # Get their public key
            local remote_key=$(sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${server}" "cat ~/.ssh/id_ed25519.pub 2>/dev/null" 2>/dev/null)
            if [ -n "$remote_key" ]; then
                cluster_keys["$server"]="$remote_key"
                echo -e "${GREEN}✓ Key collected${NC}"
            else
                echo -e "${YELLOW}⚠ No key found (run --ha-setup on $server first)${NC}"
            fi
        else
            echo -e "${YELLOW}⚠ Cannot access yet (needs initial key exchange)${NC}"
        fi
    done

    echo ""
    echo -e "${CYAN}Step 2: Distributing all public keys to all nodes...${NC}"
    echo ""

    # Add all collected keys to local authorized_keys
    echo -ne "  Updating local authorized_keys... "
    for server in "${!cluster_keys[@]}"; do
        if [ "$server" != "$local_hostname" ]; then
            add_authorized_key "${cluster_keys[$server]}"
        fi
    done
    echo -e "${GREEN}✓${NC}"

    # Distribute keys to all accessible peers
    for server in "${HA_BACKUP_SERVERS[@]}"; do
        echo -ne "  Updating $server... "

        if check_ssh_access "$server"; then
            # Build the list of all keys to add
            local keys_to_add=""
            for key_server in "${!cluster_keys[@]}"; do
                if [ "$key_server" != "$server" ]; then
                    keys_to_add+="${cluster_keys[$key_server]}"$'\n'
                fi
            done

            # Send keys to remote server
            if [ -n "$keys_to_add" ]; then
                echo "$keys_to_add" | sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${server}" "
                    mkdir -p ~/.ssh
                    chmod 700 ~/.ssh
                    touch ~/.ssh/authorized_keys
                    chmod 600 ~/.ssh/authorized_keys
                    while IFS= read -r key; do
                        if [ -n \"\$key\" ] && ! grep -qF \"\$key\" ~/.ssh/authorized_keys 2>/dev/null; then
                            echo \"\$key\" >> ~/.ssh/authorized_keys
                        fi
                    done
                " 2>/dev/null
                echo -e "${GREEN}✓${NC}"
            else
                echo -e "${GREEN}✓ (no new keys)${NC}"
            fi
        else
            echo -e "${YELLOW}⚠ Not accessible${NC}"
        fi
    done

    echo ""
    echo -e "${GREEN}Key distribution complete.${NC}"
    echo ""
    echo "Each accessible node now has all cluster nodes' public keys."
    echo "Run this command on any nodes that weren't accessible."
    echo ""

    return 0
}

# Verify HA cluster SSH connectivity and offer to set up keys if needed
verify_ha_ssh_connectivity() {
    detect_ha_enabled

    if [ ${#HA_BACKUP_SERVERS[@]} -eq 0 ]; then
        echo -e "${YELLOW}No HA backup servers configured.${NC}"
        return 1
    fi

    echo ""
    echo -e "${CYAN}Verifying SSH connectivity to HA cluster peers...${NC}"
    echo ""

    # Ensure local setup is done first
    ensure_ha_user_setup
    generate_ha_ssh_key

    local all_ok=true
    local servers_needing_setup=()

    for server in "${HA_BACKUP_SERVERS[@]}"; do
        check_ssh_access "$server"
        local status=$?

        case $status in
            0)
                echo -e "  ${GREEN}✓${NC} $server - SSH key auth working"
                ;;
            1)
                echo -e "  ${YELLOW}⚠${NC} $server - SSH key auth not configured"
                servers_needing_setup+=("$server")
                all_ok=false
                ;;
            2)
                echo -e "  ${RED}✗${NC} $server - Unreachable"
                all_ok=false
                ;;
        esac
    done

    echo ""

    # Offer to set up SSH keys for servers that need it
    if [ ${#servers_needing_setup[@]} -gt 0 ]; then
        echo -e "${YELLOW}Some servers need SSH key setup for HA to work properly.${NC}"
        echo ""
        read -p "Would you like to set up SSH keys now? (y/N): " do_setup
        if [[ "$do_setup" =~ ^[Yy]$ ]]; then
            for server in "${servers_needing_setup[@]}"; do
                setup_ssh_keys "$server"
            done

            # After individual setup, try cluster-wide distribution
            echo ""
            echo -e "${CYAN}Attempting cluster-wide key distribution...${NC}"
            distribute_keys_to_cluster
        fi
    else
        # All servers accessible, ensure full mesh
        echo -e "${CYAN}All peers accessible. Ensuring full mesh key distribution...${NC}"
        distribute_keys_to_cluster
    fi

    if [ "$all_ok" = true ]; then
        echo -e "${GREEN}All HA cluster peers have SSH key authentication configured.${NC}"
        return 0
    else
        return 1
    fi
}

# Check if a remote server has a coin's blockchain synced
# Uses SSH to query the remote node (secure - no credential sharing)
check_remote_blockchain_sync() {
    local server=$1
    local coin=$2
    local rpc_port=""
    local cli_cmd=""
    local conf_path=""

    case $coin in
        DGB|DGB-SCRYPT)
            rpc_port=14022
            cli_cmd="digibyte-cli"
            conf_path="$SPIRALPOOL_DIR/dgb/digibyte.conf"
            conf_path_alt=""
            ;;
        BTC)
            rpc_port=8332
            cli_cmd="bitcoin-cli"
            conf_path="$SPIRALPOOL_DIR/btc/bitcoin.conf"
            conf_path_alt=""
            ;;
        BCH)
            rpc_port=8432
            cli_cmd="bitcoin-cli-bch"
            conf_path="$SPIRALPOOL_DIR/bch/bitcoin.conf"
            conf_path_alt=""
            ;;
        BC2)
            rpc_port=8339
            cli_cmd="bitcoinii-cli"
            conf_path="$SPIRALPOOL_DIR/bc2/bitcoinii.conf"
            conf_path_alt=""
            ;;
        LTC)
            rpc_port=9332
            cli_cmd="litecoin-cli"
            conf_path="$SPIRALPOOL_DIR/ltc/litecoin.conf"
            conf_path_alt=""
            ;;
        DOGE)
            rpc_port=22555
            cli_cmd="dogecoin-cli"
            conf_path="$SPIRALPOOL_DIR/doge/dogecoin.conf"
            conf_path_alt=""
            ;;
        PEP)
            rpc_port=33873
            cli_cmd="pepecoin-cli"
            conf_path="$SPIRALPOOL_DIR/pep/pepecoin.conf"
            conf_path_alt=""
            ;;
        CAT)
            rpc_port=9932
            cli_cmd="catcoin-cli"
            conf_path="$SPIRALPOOL_DIR/cat/catcoin.conf"
            conf_path_alt=""
            ;;
        NMC)
            rpc_port=8336
            cli_cmd="namecoin-cli"
            conf_path="$SPIRALPOOL_DIR/nmc/namecoin.conf"
            conf_path_alt=""
            ;;
        SYS)
            rpc_port=8370
            cli_cmd="syscoin-cli"
            conf_path="$SPIRALPOOL_DIR/sys/syscoin.conf"
            conf_path_alt=""
            ;;
        XMY)
            rpc_port=10889
            cli_cmd="myriadcoin-cli"
            conf_path="$SPIRALPOOL_DIR/xmy/myriadcoin.conf"
            conf_path_alt=""
            ;;
        FBTC)
            rpc_port=8340
            cli_cmd="fractal-cli"
            conf_path="$SPIRALPOOL_DIR/fbtc/fractal.conf"
            conf_path_alt=""
            ;;
    esac

    # Execute getblockchaininfo on the remote node via SSH as HA user
    # The remote node uses its own local credentials - we never see them
    # HA user has permission to run CLI commands via sudo
    local response=$(sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 -o BatchMode=yes "${HA_SSH_USER}@${server}" "
        if [ -x /usr/local/bin/$cli_cmd ]; then
            if [ -f '$conf_path' ]; then
                sudo /usr/local/bin/$cli_cmd -conf='$conf_path' getblockchaininfo 2>/dev/null
            elif [ -n '$conf_path_alt' ] && [ -f '$conf_path_alt' ]; then
                sudo /usr/local/bin/$cli_cmd -conf='$conf_path_alt' getblockchaininfo 2>/dev/null
            else
                echo 'NODE_NOT_INSTALLED'
            fi
        else
            echo 'NODE_NOT_INSTALLED'
        fi
    " 2>/dev/null)

    if [ -z "$response" ]; then
        return 1  # Cannot connect via SSH
    fi

    if [ "$response" = "NODE_NOT_INSTALLED" ]; then
        return 4  # Node not installed on remote
    fi

    # Check for RPC errors (node not running, etc)
    if echo "$response" | grep -qE "error|couldn't connect|Could not connect"; then
        return 1  # Node not running or RPC error
    fi

    # Check if synced (verificationprogress > 0.9999 or initialblockdownload = false)
    local progress=$(echo "$response" | grep -oP '"verificationprogress"\s*:\s*\K[0-9.]+' || echo "0")
    local ibd=$(echo "$response" | grep -oP '"initialblockdownload"\s*:\s*\K(true|false)' || echo "true")

    # Handle different output formats (JSON vs plain)
    if [ -z "$progress" ] || [ "$progress" = "0" ]; then
        # Try alternate parsing for non-JSON output
        progress=$(echo "$response" | grep -i "verificationprogress" | grep -oP '[0-9.]+' || echo "0")
    fi
    if [ -z "$ibd" ]; then
        ibd=$(echo "$response" | grep -i "initialblockdownload" | grep -oP '(true|false)' || echo "true")
    fi

    if [ "$ibd" = "false" ]; then
        return 0  # Synced
    elif (( $(echo "$progress > 0.9999" | bc -l 2>/dev/null || echo 0) )); then
        return 0  # Synced
    else
        return 3  # Still syncing
    fi
}

# Display HA cluster sync warning and instructions
warn_ha_cluster_sync() {
    local new_coins=("$@")

    detect_ha_enabled

    if [ "$HA_ENABLED" = false ]; then
        return 0  # No HA, no warning needed
    fi

    echo ""
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${YELLOW}  WARNING: HA CLUSTER SYNCHRONIZATION REQUIRED${NC}"
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "${CYAN}High Availability is ENABLED on this server.${NC}"
    echo -e "${CYAN}To ensure seamless failover, ALL HA servers must have:${NC}"
    echo ""
    echo -e "  ${GREEN}1.${NC} The SAME coin configuration (config.yaml)"
    echo -e "  ${GREEN}2.${NC} The SAME coins enabled/disabled"
    echo -e "  ${GREEN}3.${NC} Blockchain data FULLY SYNCED for each enabled coin"
    echo ""

    # List backup servers if known
    if [ ${#HA_BACKUP_SERVERS[@]} -gt 0 ]; then
        echo -e "${CYAN}Detected HA Cluster Peers:${NC}"
        for server in "${HA_BACKUP_SERVERS[@]}"; do
            echo -e "  • $server"
        done
        echo ""

        # Check blockchain sync status on remote servers
        echo -e "${CYAN}Checking blockchain sync status on HA peers...${NC}"
        echo ""

        local sync_issues=()

        for server in "${HA_BACKUP_SERVERS[@]}"; do
            echo -e "  ${BLUE}Server: $server${NC}"

            # Check if server is reachable
            if ! ping -c 1 -W 2 "$server" &>/dev/null; then
                echo -e "    ${RED}✗ Cannot reach server${NC}"
                sync_issues+=("$server: unreachable")
                continue
            fi

            for coin in "${new_coins[@]}"; do
                check_remote_blockchain_sync "$server" "$coin"
                local status=$?

                case $status in
                    0)
                        echo -e "    ${GREEN}✓ $coin: Blockchain synced${NC}"
                        ;;
                    1)
                        echo -e "    ${RED}✗ $coin: Node not running or RPC error${NC}"
                        sync_issues+=("$server: $coin node not running")
                        ;;
                    3)
                        echo -e "    ${YELLOW}⚠ $coin: Blockchain still syncing${NC}"
                        sync_issues+=("$server: $coin blockchain not synced")
                        ;;
                    4)
                        echo -e "    ${RED}✗ $coin: Node not installed${NC}"
                        sync_issues+=("$server: $coin node not installed - run pool-mode.sh on that server")
                        ;;
                    *)
                        echo -e "    ${YELLOW}? $coin: Unable to check status${NC}"
                        ;;
                esac
            done
            echo ""
        done

        # Show sync issues summary
        if [ ${#sync_issues[@]} -gt 0 ]; then
            echo -e "${RED}═══════════════════════════════════════════════════════════════${NC}"
            echo -e "${RED}  ⚠  HA SYNC ISSUES DETECTED${NC}"
            echo -e "${RED}═══════════════════════════════════════════════════════════════${NC}"
            echo ""
            echo -e "${YELLOW}The following issues may prevent proper failover:${NC}"
            for issue in "${sync_issues[@]}"; do
                echo -e "  ${RED}•${NC} $issue"
            done
            echo ""
            echo -e "${YELLOW}IMPORTANT: Failover to an unsynced node will cause mining${NC}"
            echo -e "${YELLOW}interruption until the blockchain catches up!${NC}"
            echo ""
        fi
    fi

    # Show manual sync instructions
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  TO SYNCHRONIZE HA BACKUP SERVER(S):${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "${GREEN}Option 1: Run pool-mode.sh on each backup server${NC}"
    echo "  SSH into each backup server and run the same command:"
    if [ ${#new_coins[@]} -eq 1 ]; then
        echo -e "    ${BLUE}sudo ./pool-mode.sh --solo ${new_coins[0]}${NC}"
    else
        local coins_str=$(IFS=,; echo "${new_coins[*]}")
        echo -e "    ${BLUE}sudo ./pool-mode.sh --multi ${coins_str}${NC}"
    fi
    echo ""
    echo -e "${GREEN}Option 2: Use spiralctl to sync cluster (if configured)${NC}"
    echo -e "    ${BLUE}sudo spiralctl ha sync-config${NC}"
    echo ""
    echo -e "${GREEN}Option 3: Manual config copy${NC}"
    echo "  1. Copy config.yaml to each backup server:"
    for server in "${HA_BACKUP_SERVERS[@]}"; do
        echo -e "     ${BLUE}scp $CONFIG_FILE root@${server}:$CONFIG_FILE${NC}"
    done
    echo "  2. Ensure blockchain nodes are running and synced on each server"
    echo "  3. Restart spiralstratum on each backup server:"
    echo -e "     ${BLUE}sudo systemctl restart spiralstratum${NC}"
    echo ""

    # Offer to sync now if servers are known
    if [ ${#HA_BACKUP_SERVERS[@]} -gt 0 ]; then
        echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
        read -p "Would you like to attempt automatic sync to HA peers now? (y/N): " do_sync
        if [[ "$do_sync" =~ ^[Yy]$ ]]; then
            sync_ha_cluster "${new_coins[@]}"
        fi
    fi

    echo ""
}

# Attempt to sync configuration and start nodes on HA peers
# NOTE: This does NOT sync credentials - each node keeps its own secure credentials
sync_ha_cluster() {
    local new_coins=("$@")

    detect_ha_enabled

    if [ ${#HA_BACKUP_SERVERS[@]} -eq 0 ]; then
        echo -e "${YELLOW}No HA backup servers configured to sync.${NC}"
        return 1
    fi

    echo ""
    echo -e "${CYAN}Attempting to sync HA cluster...${NC}"
    echo -e "${CYAN}(Credentials are NOT synced - each node uses its own secure credentials)${NC}"
    echo ""

    # First, verify SSH connectivity to all peers and offer to set up keys
    local servers_needing_keys=()
    for server in "${HA_BACKUP_SERVERS[@]}"; do
        check_ssh_access "$server"
        local status=$?
        if [ $status -eq 1 ]; then
            servers_needing_keys+=("$server")
        fi
    done

    # If any servers need SSH key setup, offer to set them up
    if [ ${#servers_needing_keys[@]} -gt 0 ]; then
        echo -e "${YELLOW}The following servers need SSH key authentication setup:${NC}"
        for server in "${servers_needing_keys[@]}"; do
            echo -e "  • $server"
        done
        echo ""
        echo -e "${CYAN}SSH key authentication is required for secure HA communication.${NC}"
        echo -e "${CYAN}This allows password-less, encrypted communication between nodes.${NC}"
        echo ""
        read -p "Set up SSH keys now? (Y/n): " do_setup
        if [[ ! "$do_setup" =~ ^[Nn]$ ]]; then
            for server in "${servers_needing_keys[@]}"; do
                setup_ssh_keys "$server"
            done
            echo ""
        else
            echo -e "${YELLOW}Skipping SSH key setup. Sync will fail for unconfigured servers.${NC}"
            echo ""
        fi
    fi

    for server in "${HA_BACKUP_SERVERS[@]}"; do
        echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
        echo -e "${BLUE}  Syncing to: $server${NC}"
        echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"

        # Check SSH connectivity
        check_ssh_access "$server"
        local ssh_status=$?
        if [ $ssh_status -ne 0 ]; then
            if [ $ssh_status -eq 1 ]; then
                echo -e "${RED}  ✗ SSH key authentication not configured for $server${NC}"
                echo -e "${YELLOW}    Run this command again and select 'Y' to set up SSH keys${NC}"
            else
                echo -e "${RED}  ✗ Cannot reach $server (server may be offline)${NC}"
            fi
            continue
        fi

        # Copy stratum config file (contains pool settings, NOT node RPC credentials)
        # Uses dedicated HA account for SSH, then sudo on remote for file placement
        echo -e "  ${CYAN}Copying stratum configuration...${NC}"
        if sudo -u "$HA_SSH_USER" scp -q "$CONFIG_FILE" "${HA_SSH_USER}@${server}:~/.sp-sync-config.tmp" 2>/dev/null && \
           sudo -u "$HA_SSH_USER" ssh "${HA_SSH_USER}@${server}" "[ ! -L ~/.sp-sync-config.tmp ] && sudo mv ~/.sp-sync-config.tmp $CONFIG_FILE && sudo chown root:root $CONFIG_FILE" 2>/dev/null; then
            echo -e "  ${GREEN}✓ Stratum config copied${NC}"
        else
            echo -e "  ${RED}✗ Failed to copy stratum config${NC}"
            echo -e "  ${YELLOW}  $HA_SSH_USER may need sudo permissions on $server${NC}"
            continue
        fi

        # Copy HA config if exists
        if [ -f "$HA_CONFIG_FILE" ]; then
            if sudo -u "$HA_SSH_USER" scp -q "$HA_CONFIG_FILE" "${HA_SSH_USER}@${server}:~/.sp-sync-ha.tmp" 2>/dev/null && \
               sudo -u "$HA_SSH_USER" ssh "${HA_SSH_USER}@${server}" "[ ! -L ~/.sp-sync-ha.tmp ] && sudo mv ~/.sp-sync-ha.tmp $HA_CONFIG_FILE && sudo chown root:root $HA_CONFIG_FILE" 2>/dev/null; then
                echo -e "  ${GREEN}✓ HA config copied${NC}"
            fi
        fi

        # Copy HA cluster file if exists
        if [ -f "$HA_CLUSTER_FILE" ]; then
            sudo -u "$HA_SSH_USER" scp -q "$HA_CLUSTER_FILE" "${HA_SSH_USER}@${server}:~/.sp-sync-cluster.tmp" 2>/dev/null && \
            sudo -u "$HA_SSH_USER" ssh "${HA_SSH_USER}@${server}" "[ ! -L ~/.sp-sync-cluster.tmp ] && sudo mv ~/.sp-sync-cluster.tmp $HA_CLUSTER_FILE && sudo chown root:root $HA_CLUSTER_FILE" 2>/dev/null
        fi

        # Check/start nodes for each enabled coin
        for coin in "${new_coins[@]}"; do
            local service=""

            case $coin in
                DGB) service="digibyted" ;;
                BTC) service="bitcoind" ;;
                BCH) service="bitcoind-bch" ;;
                BC2) service="bitcoiniid" ;;
                LTC) service="litecoind" ;;
                DOGE) service="dogecoind" ;;
                PEP) service="pepecoind" ;;
                CAT) service="catcoind" ;;
                NMC) service="namecoind" ;;
                SYS) service="syscoind" ;;
                XMY) service="myriadcoind" ;;
                FBTC) service="fractald" ;;
                DGB-SCRYPT) service="digibyted" ;;  # Shares node with DGB
            esac

            echo -e "  ${CYAN}Checking $coin node on $server...${NC}"

            # Check if service exists and start it (HA user uses sudo for systemctl)
            local result=$(sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 "${HA_SSH_USER}@${server}" "
                if systemctl list-unit-files | grep -q '$service'; then
                    sudo systemctl enable $service 2>/dev/null
                    if systemctl is-active --quiet $service; then
                        echo 'already_running'
                    else
                        sudo systemctl start $service 2>/dev/null
                        echo 'started'
                    fi
                else
                    echo 'not_installed'
                fi
            " 2>/dev/null)

            case "$result" in
                "already_running")
                    echo -e "  ${GREEN}✓ $coin node already running${NC}"
                    ;;
                "started")
                    echo -e "  ${GREEN}✓ $coin node started${NC}"
                    ;;
                "not_installed")
                    echo -e "  ${YELLOW}⚠ $coin node not installed on $server${NC}"
                    echo -e "    ${YELLOW}SSH to $server and run: sudo pool-mode.sh --add $coin${NC}"
                    ;;
                *)
                    echo -e "  ${YELLOW}? $coin status unknown${NC}"
                    ;;
            esac
        done

        # Stop nodes for coins NOT in the list (includes all SHA-256d and Scrypt coins)
        for coin in DGB BTC BCH BC2 LTC DOGE DGB-SCRYPT PEP CAT NMC SYS XMY FBTC; do
            local in_list=false
            for new_coin in "${new_coins[@]}"; do
                if [ "$coin" = "$new_coin" ]; then
                    in_list=true
                    break
                fi
            done

            if [ "$in_list" = false ]; then
                local service=""
                case $coin in
                    DGB) service="digibyted" ;;
                    BTC) service="bitcoind" ;;
                    BC2) service="bitcoiniid" ;;
                    BCH) service="bitcoind-bch" ;;
                    LTC) service="litecoind" ;;
                    DOGE) service="dogecoind" ;;
                    PEP) service="pepecoind" ;;
                    CAT) service="catcoind" ;;
                    NMC) service="namecoind" ;;
                    SYS) service="syscoind" ;;
                    XMY) service="myriadcoind" ;;
                    FBTC) service="fractald" ;;
                    DGB-SCRYPT) service="digibyted" ;;  # Shares node with DGB
                esac

                local result=$(sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 "${HA_SSH_USER}@${server}" "
                    if systemctl is-active --quiet $service 2>/dev/null; then
                        sudo systemctl stop $service 2>/dev/null
                        sudo systemctl disable $service 2>/dev/null
                        echo 'stopped'
                    fi
                " 2>/dev/null)

                if [ "$result" = "stopped" ]; then
                    echo -e "  ${YELLOW}○ $coin node stopped (not in config)${NC}"
                fi
            fi
        done

        # Restart stratum on remote
        echo -e "  ${CYAN}Restarting stratum on $server...${NC}"
        if sudo -u "$HA_SSH_USER" ssh -o ConnectTimeout=5 "${HA_SSH_USER}@${server}" "sudo systemctl restart spiralstratum" 2>/dev/null; then
            echo -e "  ${GREEN}✓ Stratum restarted${NC}"
        else
            echo -e "  ${YELLOW}⚠ Could not restart stratum (may need manual restart)${NC}"
        fi

        echo ""
    done

    echo -e "${GREEN}HA cluster sync attempt complete.${NC}"
    echo ""
    echo -e "${CYAN}Security Note:${NC}"
    echo -e "  Each node maintains its own RPC credentials locally."
    echo -e "  Health checks use SSH tunnels - credentials are never transmitted."
    echo ""
    echo -e "${YELLOW}If a node is missing coin software, SSH to that server and run:${NC}"
    echo -e "  ${BLUE}sudo pool-mode.sh --add <COIN>${NC}"
    echo ""
}

# ═══════════════════════════════════════════════════════════════════════════════
# HELPER FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════════════

print_banner() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${CYAN}  SPIRAL POOL - POOL MODE MANAGER${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  Switch between Solo and Multi-Coin mining modes"
    echo -e "  SHA-256d: DGB, BTC, BCH, BC2 | Scrypt: LTC, DOGE, DGB-SCRYPT, PEP, CAT"
    echo ""
}

check_root() {
    if [ "$EUID" -ne 0 ]; then
        echo -e "${RED}Error: Please run as root (sudo ./pool-mode.sh)${NC}"
        exit 1
    fi
}

check_installation() {
    if [ ! -d "$SPIRALPOOL_DIR" ]; then
        echo -e "${RED}Error: Spiral Pool not found at $SPIRALPOOL_DIR${NC}"
        echo "Please install Spiral Pool first using install.sh"
        exit 1
    fi
}

# Detect currently configured coins
detect_current_coins() {
    CURRENT_DGB=false
    CURRENT_BTC=false
    CURRENT_BCH=false
    CURRENT_BC2=false
    CURRENT_NMC=false
    CURRENT_SYS=false
    CURRENT_XMY=false
    CURRENT_FBTC=false
    CURRENT_LTC=false
    CURRENT_DOGE=false
    CURRENT_DGBSCRYPT=false
    CURRENT_PEP=false
    CURRENT_CAT=false
    CURRENT_COINS=()

    if [ -f "$CONFIG_FILE" ]; then
        if grep -qE "symbol:\s*[\"']?DGB[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_DGB=true
            CURRENT_COINS+=("DGB")
        fi
        if grep -qE "symbol:\s*[\"']?BTC[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_BTC=true
            CURRENT_COINS+=("BTC")
        fi
        if grep -qE "symbol:\s*[\"']?BCH[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_BCH=true
            CURRENT_COINS+=("BCH")
        fi
        if grep -qE "symbol:\s*[\"']?BC2[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_BC2=true
            CURRENT_COINS+=("BC2")
        fi
        if grep -qE "symbol:\s*[\"']?NMC[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_NMC=true
            CURRENT_COINS+=("NMC")
        fi
        if grep -qE "symbol:\s*[\"']?SYS[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_SYS=true
            CURRENT_COINS+=("SYS")
        fi
        if grep -qE "symbol:\s*[\"']?XMY[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_XMY=true
            CURRENT_COINS+=("XMY")
        fi
        if grep -qE "symbol:\s*[\"']?FBTC[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_FBTC=true
            CURRENT_COINS+=("FBTC")
        fi
        if grep -qE "symbol:\s*[\"']?LTC[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_LTC=true
            CURRENT_COINS+=("LTC")
        fi
        if grep -qE "symbol:\s*[\"']?DOGE[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_DOGE=true
            CURRENT_COINS+=("DOGE")
        fi
        if grep -qE "symbol:\s*[\"']?DGB-SCRYPT[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_DGBSCRYPT=true
            CURRENT_COINS+=("DGB-SCRYPT")
        fi
        if grep -qE "symbol:\s*[\"']?PEP[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_PEP=true
            CURRENT_COINS+=("PEP")
        fi
        if grep -qE "symbol:\s*[\"']?CAT[\"']?" "$CONFIG_FILE" 2>/dev/null; then
            CURRENT_CAT=true
            CURRENT_COINS+=("CAT")
        fi
    fi

    CURRENT_COUNT=${#CURRENT_COINS[@]}

    if [ "$CURRENT_COUNT" -eq 1 ]; then
        CURRENT_MODE="solo"
    elif [ "$CURRENT_COUNT" -gt 1 ]; then
        CURRENT_MODE="multi"
    else
        CURRENT_MODE="none"
    fi
}

# Check if a node is installed for a coin
check_node_installed() {
    local coin=$1
    case $coin in
        DGB)
            [ -x "/usr/local/bin/digibyted" ] || systemctl is-enabled digibyted &>/dev/null 2>&1
            ;;
        BTC)
            [ -x "/usr/local/bin/bitcoind" ] || systemctl is-enabled bitcoind &>/dev/null 2>&1
            ;;
        BCH)
            [ -x "/usr/local/bin/bitcoind-bch" ] || systemctl is-enabled bitcoind-bch &>/dev/null 2>&1
            ;;
        BC2)
            [ -x "/usr/local/bin/bitcoiniid" ] || systemctl is-enabled bitcoiniid &>/dev/null 2>&1
            ;;
        LTC)
            [ -x "/usr/local/bin/litecoind" ] || systemctl is-enabled litecoind &>/dev/null 2>&1
            ;;
        DOGE)
            [ -x "/usr/local/bin/dogecoind" ] || systemctl is-enabled dogecoind &>/dev/null 2>&1
            ;;
        PEP)
            [ -x "/usr/local/bin/pepecoind" ] || systemctl is-enabled pepecoind &>/dev/null 2>&1
            ;;
        CAT)
            [ -x "/usr/local/bin/catcoind" ] || systemctl is-enabled catcoind &>/dev/null 2>&1
            ;;
        NMC)
            [ -x "/usr/local/bin/namecoind" ] || systemctl is-enabled namecoind &>/dev/null 2>&1
            ;;
        SYS)
            [ -x "/usr/local/bin/syscoind" ] || systemctl is-enabled syscoind &>/dev/null 2>&1
            ;;
        XMY)
            [ -x "/usr/local/bin/myriadcoind" ] || systemctl is-enabled myriadcoind &>/dev/null 2>&1
            ;;
        FBTC)
            [ -x "/usr/local/bin/fractald" ] || systemctl is-enabled fractald &>/dev/null 2>&1
            ;;
        DGB-SCRYPT)
            # DGB-SCRYPT uses the same node as DGB (DigiByte Core)
            [ -x "/usr/local/bin/digibyted" ] || systemctl is-enabled digibyted &>/dev/null 2>&1
            ;;
    esac
}

# Print current status
print_status() {
    detect_current_coins

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  CURRENT CONFIGURATION${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""

    if [ "$CURRENT_MODE" = "none" ]; then
        echo -e "${YELLOW}  Mode: Not Configured${NC}"
        echo "  No coins are currently enabled."
    else
        if [ "$CURRENT_MODE" = "solo" ]; then
            echo -e "${BLUE}  Mode: Solo Mining${NC}"
        else
            echo -e "${MAGENTA}  Mode: Multi-Coin Mining${NC}"
        fi
        echo -e "  Active Coins: ${CYAN}${CURRENT_COINS[*]}${NC}"
    fi

    echo ""
    echo "  Node Status:"
    for coin in DGB BTC BCH BC2 LTC DOGE DGB-SCRYPT PEP CAT NMC SYS XMY FBTC; do
        if check_node_installed "$coin"; then
            echo -e "    $coin: ${GREEN}Installed${NC}"
        else
            echo -e "    $coin: ${YELLOW}Not Installed${NC}"
        fi
    done
    echo ""
}

# Validate coin symbol
validate_coin() {
    local coin=$1
    case $coin in
        DGB|BTC|BCH|BC2|LTC|DOGE|DGB-SCRYPT|PEP|CAT|NMC|SYS|XMY|FBTC) return 0 ;;
        *) return 1 ;;
    esac
}

# Get wallet address for a coin
# IMPORTANT: All display output goes to stderr (>&2), only the final address to stdout
get_wallet_address() {
    local coin=$1
    local address=""

    case $coin in
        DGB)
            echo -e "${CYAN}Enter your DigiByte wallet address:${NC}" >&2
            echo "(Addresses starting with D or S are valid)" >&2
            read -p "DGB Address: " address
            if [[ ! "$address" =~ ^[DS] ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid DigiByte address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        BTC)
            echo -e "${CYAN}Enter your Bitcoin wallet address:${NC}" >&2
            echo "(Native SegWit bc1... addresses recommended)" >&2
            read -p "BTC Address: " address
            if [[ ! "$address" =~ ^(1|3|bc1) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Bitcoin address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        BCH)
            echo -e "${CYAN}Enter your Bitcoin Cash wallet address:${NC}" >&2
            echo "(CashAddr format recommended, e.g., bitcoincash:q...)" >&2
            read -p "BCH Address: " address
            if [[ ! "$address" =~ ^(bitcoincash:|q|p|1|3) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Bitcoin Cash address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        BC2)
            echo "" >&2
            echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}" >&2
            echo -e "${RED}  CRITICAL WARNING - BITCOIN II (BC2) ADDRESSES${NC}" >&2
            echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}" >&2
            echo "" >&2
            echo -e "${RED}  BC2 addresses look IDENTICAL to Bitcoin addresses!${NC}" >&2
            echo -e "${RED}  (bc1q..., 1..., 3...)${NC}" >&2
            echo "" >&2
            echo -e "${RED}  You MUST generate your address using Bitcoin II Core.${NC}" >&2
            echo -e "${RED}  Using a Bitcoin address will result in LOST FUNDS!${NC}" >&2
            echo "" >&2
            echo -e "${CYAN}Enter your Bitcoin II (BC2) wallet address:${NC}" >&2
            echo "(Native SegWit bc1q... addresses recommended)" >&2
            echo "" >&2

            # Check if BC2 node is available for auto-generation
            local bc2_node_available=false
            if systemctl is-active bitcoiniid &>/dev/null || pgrep -x bitcoiniid &>/dev/null; then
                bc2_node_available=true
            fi

            if [ "$bc2_node_available" = true ]; then
                echo "Options:" >&2
                echo "  [1] Enter existing BC2 address" >&2
                echo "  [2] Auto-generate new BC2 address (BC2 node is running)" >&2
                read -p "Select option (1-2): " bc2_option
            else
                echo -e "${YELLOW}Note: BC2 node not running - auto-generate not available${NC}" >&2
                echo "" >&2
                bc2_option=1
            fi

            case $bc2_option in
                1)
                    read -p "BC2 Address: " address
                    if [[ ! "$address" =~ ^(1|3|bc1) ]]; then
                        echo -e "${YELLOW}Warning: Address doesn't look like a valid Bitcoin II address${NC}" >&2
                        read -p "Continue anyway? (y/N): " confirm
                        [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
                    fi
                    ;;
                2)
                    echo -e "${CYAN}Attempting to auto-generate BC2 address...${NC}" >&2
                    # Check if wallet exists, create if not
                    if ! bitcoinii-cli -conf=/spiralpool/bc2/bitcoinii.conf listwallets 2>/dev/null | grep -q "pool"; then
                        echo "Creating BC2 pool wallet..." >&2
                        bitcoinii-cli -conf=/spiralpool/bc2/bitcoinii.conf createwallet "pool" 2>/dev/null || true
                        # Ensure wallet is loaded after creation
                        bitcoinii-cli -conf=/spiralpool/bc2/bitcoinii.conf loadwallet "pool" 2>/dev/null || true
                    fi
                    # BC2 uses older Bitcoin codebase - no address_type parameter supported
                    address=$(bitcoinii-cli -conf=/spiralpool/bc2/bitcoinii.conf -rpcwallet=pool getnewaddress "pool" 2>/dev/null)
                    if [ -z "$address" ]; then
                        echo -e "${RED}Error: Failed to generate BC2 address. Check node status.${NC}" >&2
                        return 1
                    fi
                    echo -e "${GREEN}Generated BC2 address: $address${NC}" >&2
                    echo -e "${YELLOW}IMPORTANT: Back up your BC2 wallet immediately!${NC}" >&2
                    ;;
                *)
                    echo -e "${RED}Invalid option${NC}" >&2
                    return 1
                    ;;
            esac
            ;;
        LTC)
            echo -e "${CYAN}Enter your Litecoin wallet address:${NC}" >&2
            echo "(Native SegWit ltc1... addresses recommended)" >&2
            read -p "LTC Address: " address
            if [[ ! "$address" =~ ^(L|M|3|ltc1) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Litecoin address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        DOGE)
            echo -e "${CYAN}Enter your Dogecoin wallet address:${NC}" >&2
            echo "(Addresses starting with D are valid)" >&2
            read -p "DOGE Address: " address
            if [[ ! "$address" =~ ^(D|9|A) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Dogecoin address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        DGB-SCRYPT)
            echo -e "${CYAN}Enter your DigiByte wallet address for Scrypt mining:${NC}" >&2
            echo "(Same address as DGB - both algorithms pay to same wallet)" >&2
            echo "(Addresses starting with D or S are valid)" >&2
            read -p "DGB-SCRYPT Address: " address
            if [[ ! "$address" =~ ^[DS] ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid DigiByte address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        PEP)
            echo -e "${CYAN}Enter your PepeCoin wallet address:${NC}" >&2
            echo "(Addresses starting with P are valid)" >&2
            read -p "PEP Address: " address
            if [[ ! "$address" =~ ^P ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid PepeCoin address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        CAT)
            echo -e "${CYAN}Enter your Catcoin wallet address:${NC}" >&2
            echo "(P2PKH addresses start with 9 — version byte 21)" >&2
            read -p "CAT Address: " address
            if [[ ! "$address" =~ ^9 ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Catcoin address (must start with 9)${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        NMC)
            echo -e "${CYAN}Enter your Namecoin wallet address:${NC}" >&2
            echo "(Addresses starting with N or M, or nc1... bech32)" >&2
            read -p "NMC Address: " address
            if [[ ! "$address" =~ ^(N|M|nc1) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Namecoin address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        SYS)
            echo -e "${CYAN}Enter your Syscoin wallet address:${NC}" >&2
            echo "(Native SegWit sys1... addresses recommended)" >&2
            read -p "SYS Address: " address
            if [[ ! "$address" =~ ^(S|sys1|tsys1) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Syscoin address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        XMY)
            echo -e "${CYAN}Enter your Myriadcoin wallet address:${NC}" >&2
            echo "(Addresses starting with M, or my1... bech32)" >&2
            read -p "XMY Address: " address
            if [[ ! "$address" =~ ^(M|my1) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Myriadcoin address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
        FBTC)
            echo "" >&2
            echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}" >&2
            echo -e "${RED}  CRITICAL WARNING - FRACTAL BITCOIN (FBTC) ADDRESSES${NC}" >&2
            echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}" >&2
            echo "" >&2
            echo -e "${RED}  FBTC addresses look IDENTICAL to Bitcoin addresses!${NC}" >&2
            echo -e "${RED}  You MUST generate your address using the Fractal Bitcoin node.${NC}" >&2
            echo -e "${RED}  Using a Bitcoin address will result in LOST FUNDS!${NC}" >&2
            echo "" >&2
            echo -e "${CYAN}Enter your Fractal Bitcoin (FBTC) wallet address:${NC}" >&2
            read -p "FBTC Address: " address
            if [[ ! "$address" =~ ^(1|3|bc1) ]]; then
                echo -e "${YELLOW}Warning: Address doesn't look like a valid Fractal BTC address${NC}" >&2
                read -p "Continue anyway? (y/N): " confirm
                [[ ! "$confirm" =~ ^[Yy]$ ]] && return 1
            fi
            ;;
    esac

    echo "$address"
}

# Extract existing config values
extract_coin_config() {
    local coin=$1
    local backup_file=$2

    # Extract user, password, address for this coin from backup
    local section_start=$(grep -n "symbol:\s*[\"']?${coin}[\"']?" "$backup_file" 2>/dev/null | head -1 | cut -d: -f1)

    if [ -n "$section_start" ]; then
        # Get next 25 lines and extract values
        local section=$(sed -n "${section_start},$((section_start+25))p" "$backup_file")

        COIN_USER=$(echo "$section" | grep -Po '(?<=user:\s)["\047]?[^"\047\n]+' | head -1 | tr -d '"'"'")
        COIN_PASS=$(echo "$section" | grep -Po '(?<=password:\s)["\047]?[^"\047\n]+' | head -1 | tr -d '"'"'")
        COIN_ADDR=$(echo "$section" | grep -Po '(?<=address:\s)["\047]?[^"\047\n]+' | head -1 | tr -d '"'"'")

        return 0
    fi
    return 1
}

# Read RPC credentials from existing node config file
# This ensures pool-mode.sh uses the same password as the running node
read_node_rpc_credentials() {
    local coin=$1
    local conf_file=""

    # Determine the config file path for each coin
    case $coin in
        DGB)
            conf_file="$SPIRALPOOL_DIR/dgb/digibyte.conf"
            ;;
        BTC)
            conf_file="$SPIRALPOOL_DIR/btc/bitcoin.conf"
            ;;
        BCH)
            conf_file="$SPIRALPOOL_DIR/bch/bitcoin.conf"
            ;;
        BC2)
            conf_file="$SPIRALPOOL_DIR/bc2/bitcoinii.conf"
            ;;
        LTC)
            conf_file="$SPIRALPOOL_DIR/ltc/litecoin.conf"
            ;;
        DOGE)
            conf_file="$SPIRALPOOL_DIR/doge/dogecoin.conf"
            ;;
        DGB-SCRYPT)
            # DGB-SCRYPT uses the same node as DGB
            conf_file="$SPIRALPOOL_DIR/dgb/digibyte.conf"
            ;;
        PEP)
            conf_file="$SPIRALPOOL_DIR/pep/pepecoin.conf"
            ;;
        CAT)
            conf_file="$SPIRALPOOL_DIR/cat/catcoin.conf"
            ;;
        NMC)
            conf_file="$SPIRALPOOL_DIR/nmc/namecoin.conf"
            ;;
        SYS)
            conf_file="$SPIRALPOOL_DIR/sys/syscoin.conf"
            ;;
        XMY)
            conf_file="$SPIRALPOOL_DIR/xmy/myriadcoin.conf"
            ;;
        FBTC)
            conf_file="$SPIRALPOOL_DIR/fbtc/fractal.conf"
            ;;
    esac

    if [ -f "$conf_file" ]; then
        # Extract rpcuser and rpcpassword from node config
        NODE_RPC_USER=$(sudo cat "$conf_file" 2>/dev/null | grep -Po '^rpcuser=\K.*' | head -1)
        NODE_RPC_PASS=$(sudo cat "$conf_file" 2>/dev/null | grep -Po '^rpcpassword=\K.*' | head -1)

        if [ -n "$NODE_RPC_USER" ] && [ -n "$NODE_RPC_PASS" ]; then
            echo -e "${BLUE}Found existing RPC credentials for $coin in $conf_file${NC}" >&2
            return 0
        fi
    fi

    NODE_RPC_USER=""
    NODE_RPC_PASS=""
    return 1
}

# Generate coin configuration block for YAML
generate_coin_config() {
    local coin=$1
    local address=$2
    local rpc_user=$3
    local rpc_pass=$4

    case $coin in
        DGB)
            cat << EOF
  # DigiByte (DGB) - SHA256d
  - symbol: DGB
    pool_id: dgb_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 3333
      difficulty:
        initial: 5000
        varDiff:
          enabled: true
          minDiff: 0.0001
          maxDiff: 10000000
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 50
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 30s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 14022
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28532"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        BTC)
            cat << EOF
  # Bitcoin (BTC) - SHA256d
  - symbol: BTC
    pool_id: btc_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 4333
      difficulty:
        initial: 65536
        varDiff:
          enabled: true
          minDiff: 1024
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 120
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 5
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 8332
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28332"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        BCH)
            cat << EOF
  # Bitcoin Cash (BCH) - SHA256d
  - symbol: BCH
    pool_id: bch_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 5333
      difficulty:
        initial: 65536
        varDiff:
          enabled: true
          minDiff: 1024
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 120
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 5
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 8432
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28432"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        BC2)
            cat << EOF
  # Bitcoin II (BC2) - SHA256d
  # CRITICAL: BC2 addresses are IDENTICAL to Bitcoin addresses (bc1q..., 1..., 3...)
  # Verify you are using a BC2 wallet address, NOT a Bitcoin address!
  - symbol: BC2
    pool_id: bc2_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 6333
      difficulty:
        initial: 65536
        varDiff:
          enabled: true
          minDiff: 1
          maxDiff: 1000000000
          targetTime: 15
          retargetTime: 60
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 50
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 8339
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28338"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        LTC)
            cat << EOF
  # Litecoin (LTC) - Scrypt
  - symbol: LTC
    pool_id: ltc_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 7333
      difficulty:
        initial: 8
        varDiff:
          enabled: true
          minDiff: 1
          maxDiff: 65536
          targetTime: 10
          retargetTime: 60
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 50
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 30s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 9332
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28933"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        DOGE)
            cat << EOF
  # Dogecoin (DOGE) - Scrypt
  - symbol: DOGE
    pool_id: doge_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 8335
      difficulty:
        initial: 8
        varDiff:
          enabled: true
          minDiff: 1
          maxDiff: 65536
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 50
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 30s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 22555
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28555"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        DGB-SCRYPT)
            cat << EOF
  # DigiByte-Scrypt (DGB-SCRYPT) - Scrypt
  # Uses same node as DGB but mines with Scrypt algorithm
  - symbol: DGB-SCRYPT
    pool_id: dgb_scrypt_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 3336
      difficulty:
        initial: 8
        varDiff:
          enabled: true
          minDiff: 1
          maxDiff: 65536
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 50
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 15s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 14022
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28532"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        PEP)
            cat << EOF
  # PepeCoin (PEP) - Scrypt
  - symbol: PEP
    pool_id: pep_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 10335
      difficulty:
        initial: 8
        varDiff:
          enabled: true
          minDiff: 1
          maxDiff: 65536
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 50
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 30s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 33873
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: false  # PepeCoin v1.1.0 compiled without ZMQ support
          endpoint: "tcp://127.0.0.1:28873"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        CAT)
            cat << EOF
  # Catcoin (CAT) - Scrypt
  - symbol: CAT
    pool_id: cat_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 12335
      difficulty:
        initial: 8
        varDiff:
          enabled: true
          minDiff: 1
          maxDiff: 65536
          targetTime: 4
          retargetTime: 60
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 50
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 30s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 9932
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28932"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        NMC)
            cat << EOF
  # Namecoin (NMC) - SHA256d (AuxPoW - merge-mined with Bitcoin)
  - symbol: NMC
    pool_id: nmc_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 14335
      difficulty:
        initial: 65536
        varDiff:
          enabled: true
          minDiff: 1024
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 120
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 5
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 8336
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28336"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        SYS)
            cat << EOF
  # Syscoin (SYS) - SHA256d (AuxPoW - merge-mined with Bitcoin)
  - symbol: SYS
    pool_id: sys_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 15335
      difficulty:
        initial: 65536
        varDiff:
          enabled: true
          minDiff: 1024
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 120
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 5
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 8370
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28370"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        XMY)
            cat << EOF
  # Myriadcoin (XMY) - SHA256d (AuxPoW - merge-mined with Bitcoin)
  - symbol: XMY
    pool_id: xmy_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 17335
      difficulty:
        initial: 65536
        varDiff:
          enabled: true
          minDiff: 1024
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 120
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 5
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 10889
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28889"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
        FBTC)
            cat << EOF
  # Fractal Bitcoin (FBTC) - SHA256d (AuxPoW - merge-mined with Bitcoin)
  # CRITICAL: FBTC addresses are IDENTICAL to Bitcoin addresses (bc1q..., 1..., 3...)
  # Verify you are using an FBTC wallet address, NOT a Bitcoin address!
  - symbol: FBTC
    pool_id: fbtc_mainnet
    enabled: true
    address: "$address"
    coinbase_text: "Spiral Pool"

    stratum:
      port: 18335
      difficulty:
        initial: 65536
        varDiff:
          enabled: true
          minDiff: 1024
          maxDiff: 100000000
          targetTime: 10
          retargetTime: 120
          variancePercent: 30
      banning:
        enabled: true
        banDuration: 600s
        invalidSharesThreshold: 5
      connection:
        timeout: 600s
        maxConnections: 10000
      version_rolling:
        enabled: true
        mask: 536862720
      job_rebroadcast: 55s

    nodes:
      - id: primary
        host: 127.0.0.1
        port: 8340
        user: $rpc_user
        password: "$rpc_pass"
        priority: 0
        weight: 1
        zmq:
          enabled: true
          endpoint: "tcp://127.0.0.1:28340"

    payments:
      enabled: false
      scheme: SOLO

EOF
            ;;
    esac
}

# Install node for a coin if not present
install_node_if_needed() {
    local coin=$1

    if check_node_installed "$coin"; then
        echo -e "${GREEN}✓ $coin node already installed${NC}"
        return 0
    fi

    echo -e "${YELLOW}$coin node not installed. Installing...${NC}"

    case $coin in
        DGB)
            echo "Downloading DigiByte Core..."
            cd /tmp
            DGB_VERSION="8.26.2"
            DGB_FILENAME="digibyte-${DGB_VERSION}-${ARCH_SUFFIX}.tar.gz"

            if [ ! -f "$DGB_FILENAME" ]; then
                wget -q --show-progress --max-redirect=5 "https://github.com/DigiByte-Core/digibyte/releases/download/v${DGB_VERSION}/${DGB_FILENAME}"
            fi

            tar -xzf "$DGB_FILENAME"
            cp "digibyte-${DGB_VERSION}/bin/digibyted" /usr/local/bin/
            cp "digibyte-${DGB_VERSION}/bin/digibyte-cli" /usr/local/bin/
            echo -e "${GREEN}✓ DigiByte Core ${DGB_VERSION} installed${NC}"
            ;;

        BTC)
            echo "Detecting latest Bitcoin Knots version..."
            BITCOIN_KNOTS_FALLBACK="29.3.knots20260210"

            KNOTS_PAGE=$(curl -sL --connect-timeout 10 --max-time 30 "https://bitcoinknots.org/files/" 2>/dev/null)
            if [ -n "$KNOTS_PAGE" ]; then
                LATEST_MAJOR=$(echo "$KNOTS_PAGE" | grep -oP 'href="\K[0-9]+\.x(?=/")' | sort -t. -k1 -n | tail -1)
                if [ -n "$LATEST_MAJOR" ]; then
                    VERSION_PAGE=$(curl -sL --connect-timeout 10 --max-time 30 "https://bitcoinknots.org/files/${LATEST_MAJOR}/" 2>/dev/null)
                    if [ -n "$VERSION_PAGE" ]; then
                        BITCOIN_KNOTS_VERSION=$(echo "$VERSION_PAGE" | grep -oP 'href="\K[0-9]+\.[0-9]+\.knots[0-9]+(?=/")' | sort -V | tail -1)
                    fi
                fi
            fi

            if [ -z "$BITCOIN_KNOTS_VERSION" ]; then
                BITCOIN_KNOTS_VERSION="$BITCOIN_KNOTS_FALLBACK"
            fi

            KNOTS_MAJOR_VERSION="${BITCOIN_KNOTS_VERSION%%.*}.x"

            echo "Downloading Bitcoin Knots $BITCOIN_KNOTS_VERSION..."
            cd /tmp
            KNOTS_FILENAME="bitcoin-${BITCOIN_KNOTS_VERSION}-${ARCH_SUFFIX}.tar.gz"

            if [ ! -f "$KNOTS_FILENAME" ]; then
                wget -q --show-progress --max-redirect=5 "https://bitcoinknots.org/files/${KNOTS_MAJOR_VERSION}/${BITCOIN_KNOTS_VERSION}/${KNOTS_FILENAME}"
            fi

            tar -xzf "$KNOTS_FILENAME"
            EXTRACTED_DIR=$(ls -d bitcoin-*/ 2>/dev/null | head -1 | tr -d '/')

            cp "${EXTRACTED_DIR}/bin/bitcoind" /usr/local/bin/
            cp "${EXTRACTED_DIR}/bin/bitcoin-cli" /usr/local/bin/
            echo -e "${GREEN}✓ Bitcoin Knots $BITCOIN_KNOTS_VERSION installed${NC}"
            ;;

        BCH)
            echo "Downloading Bitcoin Cash Node..."
            cd /tmp
            BCH_VERSION="29.0.0"
            BCH_FILENAME="bitcoin-cash-node-${BCH_VERSION}-${ARCH_SUFFIX}.tar.gz"

            if [ ! -f "$BCH_FILENAME" ]; then
                wget -q --show-progress --max-redirect=5 "https://github.com/bitcoin-cash-node/bitcoin-cash-node/releases/download/v${BCH_VERSION}/${BCH_FILENAME}"
            fi

            tar -xzf "$BCH_FILENAME"
            cp "bitcoin-cash-node-${BCH_VERSION}/bin/bitcoind" /usr/local/bin/bitcoind-bch
            cp "bitcoin-cash-node-${BCH_VERSION}/bin/bitcoin-cli" /usr/local/bin/bitcoin-cli-bch
            echo -e "${GREEN}✓ Bitcoin Cash Node ${BCH_VERSION} installed${NC}"
            ;;

        BC2)
            echo "Downloading Bitcoin II Core v29.1.0..."
            cd /tmp
            BC2_VERSION="29.1.0"
            # BC2 uses -CLI suffix instead of -gnu, and extraction dir includes arch
            local BC2_ARCH_SUFFIX="x86_64-linux-CLI"
            if [[ "$SYSTEM_ARCH" == "arm64" ]]; then
                BC2_ARCH_SUFFIX="aarch64-linux-CLI"
            fi
            BC2_FILENAME="BitcoinII-${BC2_VERSION}-${BC2_ARCH_SUFFIX}.tar.gz"

            if [ ! -f "$BC2_FILENAME" ]; then
                wget -q --show-progress --max-redirect=5 "https://github.com/Bitcoin-II/BitcoinII-Core/releases/download/v${BC2_VERSION}/${BC2_FILENAME}"
            fi

            if [ ! -f "$BC2_FILENAME" ]; then
                echo -e "${RED}Error: Failed to download Bitcoin II Core${NC}"
                return 1
            fi

            tar -xzf "$BC2_FILENAME"

            # Bitcoin II binary names use capital "II": bitcoinIId and bitcoinII-cli
            # Extract to both /usr/local/bin (lowercase) and /spiralpool/bc2/bin (original case)
            # Service file will use the /spiralpool/bc2/bin path with original capitalization
            mkdir -p "$SPIRALPOOL_DIR/bc2/bin"
            cp "BitcoinII-${BC2_VERSION}-${BC2_ARCH_SUFFIX}/bitcoinIId" "$SPIRALPOOL_DIR/bc2/bin/"
            cp "BitcoinII-${BC2_VERSION}-${BC2_ARCH_SUFFIX}/bitcoinII-cli" "$SPIRALPOOL_DIR/bc2/bin/"
            chmod +x "$SPIRALPOOL_DIR/bc2/bin/bitcoinIId" "$SPIRALPOOL_DIR/bc2/bin/bitcoinII-cli"
            chown -R "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/bc2/bin"

            # Also create symlinks in /usr/local/bin for CLI convenience (lowercase names)
            ln -sf "$SPIRALPOOL_DIR/bc2/bin/bitcoinIId" /usr/local/bin/bitcoiniid
            ln -sf "$SPIRALPOOL_DIR/bc2/bin/bitcoinII-cli" /usr/local/bin/bitcoinii-cli

            echo -e "${GREEN}✓ Bitcoin II Core ${BC2_VERSION} installed${NC}"
            ;;

        LTC)
            echo "Downloading Litecoin Core..."
            cd /tmp
            LTC_VERSION="0.21.4"
            LTC_FILENAME="litecoin-${LTC_VERSION}-${ARCH_SUFFIX}.tar.gz"

            if [ ! -f "$LTC_FILENAME" ]; then
                wget -q --show-progress --max-redirect=5 "https://github.com/litecoin-project/litecoin/releases/download/v${LTC_VERSION}/${LTC_FILENAME}"
            fi

            tar -xzf "$LTC_FILENAME"
            cp "litecoin-${LTC_VERSION}/bin/litecoind" /usr/local/bin/
            cp "litecoin-${LTC_VERSION}/bin/litecoin-cli" /usr/local/bin/
            echo -e "${GREEN}✓ Litecoin Core ${LTC_VERSION} installed${NC}"
            ;;

        DOGE)
            echo "Downloading Dogecoin Core..."
            cd /tmp
            DOGE_VERSION="1.14.9"
            DOGE_FILENAME="dogecoin-${DOGE_VERSION}-${ARCH_SUFFIX}.tar.gz"

            if [ ! -f "$DOGE_FILENAME" ]; then
                wget -q --show-progress --max-redirect=5 "https://github.com/dogecoin/dogecoin/releases/download/v${DOGE_VERSION}/${DOGE_FILENAME}"
            fi

            tar -xzf "$DOGE_FILENAME"
            cp "dogecoin-${DOGE_VERSION}/bin/dogecoind" /usr/local/bin/
            cp "dogecoin-${DOGE_VERSION}/bin/dogecoin-cli" /usr/local/bin/
            echo -e "${GREEN}✓ Dogecoin Core ${DOGE_VERSION} installed${NC}"
            ;;

        DGB-SCRYPT)
            # DGB-SCRYPT uses the same node as DGB - just ensure DGB node is installed
            if ! check_node_installed "DGB"; then
                echo "DGB-SCRYPT requires DigiByte node. Installing DGB node..."
                install_node_if_needed "DGB"
            else
                echo -e "${GREEN}✓ DGB-SCRYPT uses existing DigiByte node${NC}"
            fi
            ;;
    esac
}

# Setup node configuration and service for a coin
setup_node() {
    local coin=$1
    local rpc_user=$2
    local rpc_pass=$3

    case $coin in
        DGB)
            mkdir -p "$SPIRALPOOL_DIR/dgb"
            chown -R "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/dgb"

            cat > "$SPIRALPOOL_DIR/dgb/digibyte.conf" << EOF
# DIGIBYTE CORE - SPIRAL POOL CONFIGURATION
listen=1
maxconnections=125
datadir=$SPIRALPOOL_DIR/dgb
server=1
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=14022
zmqpubhashblock=tcp://127.0.0.1:28532
zmqpubrawtx=tcp://127.0.0.1:28532
dbcache=1024
par=0
disablewallet=0
debuglogfile=$SPIRALPOOL_DIR/dgb/debug.log
printtoconsole=0
prune=0
EOF
            chown "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/dgb/digibyte.conf"
            chmod 600 "$SPIRALPOOL_DIR/dgb/digibyte.conf"

            cat > /etc/systemd/system/digibyted.service << EOF
[Unit]
Description=DigiByte Core Daemon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=forking
User=$POOL_USER
Group=$POOL_USER
ExecStart=/usr/local/bin/digibyted -daemon -conf=$SPIRALPOOL_DIR/dgb/digibyte.conf -pid=$SPIRALPOOL_DIR/dgb/digibyted.pid
ExecStop=/usr/local/bin/digibyte-cli -conf=$SPIRALPOOL_DIR/dgb/digibyte.conf stop
PIDFile=$SPIRALPOOL_DIR/dgb/digibyted.pid
Restart=always
RestartSec=30
TimeoutStartSec=infinity
TimeoutStopSec=600
# Note: WatchdogSec removed - blockchain daemons don't support sd_notify

# NOTE: Systemd security hardening intentionally omitted.
# Some blockchain daemons crash with SIGABRT under modern systemd
# hardening options (PrivateTmp, ProtectSystem, etc.)

[Install]
WantedBy=multi-user.target
EOF
            ufw allow 12024/tcp comment "DigiByte P2P" 2>/dev/null || true
            ufw allow 3333/tcp comment "DGB Stratum V1" 2>/dev/null || true
            ufw allow 3334/tcp comment "DGB Stratum V2" 2>/dev/null || true
            ;;

        BTC)
            mkdir -p "$SPIRALPOOL_DIR/btc"
            chown -R "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/btc"

            cat > "$SPIRALPOOL_DIR/btc/bitcoin.conf" << EOF
# BITCOIN KNOTS - SPIRAL POOL CONFIGURATION
chain=main
listen=1
maxconnections=125
datadir=$SPIRALPOOL_DIR/btc
server=1
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=8332
zmqpubhashblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28332
dbcache=8192
par=0
disablewallet=0
debuglogfile=$SPIRALPOOL_DIR/btc/debug.log
printtoconsole=0
prune=0
EOF
            chown "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/btc/bitcoin.conf"
            chmod 600 "$SPIRALPOOL_DIR/btc/bitcoin.conf"

            cat > /etc/systemd/system/bitcoind.service << EOF
[Unit]
Description=Bitcoin Knots Daemon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=forking
User=$POOL_USER
Group=$POOL_USER
ExecStart=/usr/local/bin/bitcoind -daemon -conf=$SPIRALPOOL_DIR/btc/bitcoin.conf -pid=$SPIRALPOOL_DIR/btc/bitcoind.pid
ExecStop=/usr/local/bin/bitcoin-cli -conf=$SPIRALPOOL_DIR/btc/bitcoin.conf stop
PIDFile=$SPIRALPOOL_DIR/btc/bitcoind.pid
Restart=always
RestartSec=30
TimeoutStartSec=infinity
TimeoutStopSec=600
# Note: WatchdogSec removed - blockchain daemons don't support sd_notify

# NOTE: Systemd security hardening intentionally omitted.
# Some blockchain daemons crash with SIGABRT under modern systemd
# hardening options (PrivateTmp, ProtectSystem, etc.)

[Install]
WantedBy=multi-user.target
EOF
            ufw allow 8333/tcp comment "Bitcoin P2P" 2>/dev/null || true
            ufw allow 4333/tcp comment "BTC Stratum V1" 2>/dev/null || true
            ufw allow 4334/tcp comment "BTC Stratum V2" 2>/dev/null || true
            ;;

        BCH)
            mkdir -p "$SPIRALPOOL_DIR/bch"
            chown -R "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/bch"

            cat > "$SPIRALPOOL_DIR/bch/bitcoin.conf" << EOF
# BITCOIN CASH NODE - SPIRAL POOL CONFIGURATION
listen=1
port=8433
maxconnections=125
datadir=$SPIRALPOOL_DIR/bch
server=1
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=8432
zmqpubhashblock=tcp://127.0.0.1:28432
zmqpubrawtx=tcp://127.0.0.1:28432
dbcache=2048
par=0
disablewallet=0
printtoconsole=0
prune=0
EOF
            chown "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/bch/bitcoin.conf"
            chmod 600 "$SPIRALPOOL_DIR/bch/bitcoin.conf"

            cat > /etc/systemd/system/bitcoind-bch.service << EOF
[Unit]
Description=Bitcoin Cash Node Daemon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=forking
User=$POOL_USER
Group=$POOL_USER
ExecStart=/usr/local/bin/bitcoind-bch -daemon -conf=$SPIRALPOOL_DIR/bch/bitcoin.conf -pid=$SPIRALPOOL_DIR/bch/bitcoind.pid
ExecStop=/usr/local/bin/bitcoin-cli-bch -conf=$SPIRALPOOL_DIR/bch/bitcoin.conf stop
PIDFile=$SPIRALPOOL_DIR/bch/bitcoind.pid
Restart=always
RestartSec=30
TimeoutStartSec=infinity
TimeoutStopSec=600
# Note: WatchdogSec removed - blockchain daemons don't support sd_notify

# NOTE: Systemd security hardening intentionally omitted.
# Some blockchain daemons crash with SIGABRT under modern systemd
# hardening options (PrivateTmp, ProtectSystem, etc.)

[Install]
WantedBy=multi-user.target
EOF
            ufw allow 8433/tcp comment "Bitcoin Cash P2P" 2>/dev/null || true
            ufw allow 5333/tcp comment "BCH Stratum V1" 2>/dev/null || true
            ufw allow 5334/tcp comment "BCH Stratum V2" 2>/dev/null || true
            ;;

        BC2)
            mkdir -p "$SPIRALPOOL_DIR/bc2"
            chown -R "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/bc2"

            cat > "$SPIRALPOOL_DIR/bc2/bitcoinii.conf" << EOF
# BITCOIN II CORE - SPIRAL POOL CONFIGURATION
# CRITICAL: BC2 addresses look IDENTICAL to Bitcoin (bc1q..., 1..., 3...)
# Genesis: 0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb
chain=main
listen=1
port=8338
bind=0.0.0.0:8338
maxconnections=125
datadir=$SPIRALPOOL_DIR/bc2
server=1
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=8339
zmqpubhashblock=tcp://127.0.0.1:28338
zmqpubrawtx=tcp://127.0.0.1:28338
dbcache=512
par=0
disablewallet=0
debuglogfile=$SPIRALPOOL_DIR/bc2/debug.log
printtoconsole=0
prune=0
EOF
            chown "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/bc2/bitcoinii.conf"
            chmod 600 "$SPIRALPOOL_DIR/bc2/bitcoinii.conf"

            # Note: Bitcoin II binaries use capital "II" (bitcoinIId, bitcoinII-cli)
            cat > /etc/systemd/system/bitcoiniid.service << EOF
[Unit]
Description=Bitcoin II Core Daemon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=forking
User=$POOL_USER
Group=$POOL_USER
# Bitcoin II uses capital "II" in binary names: bitcoinIId, bitcoinII-cli
ExecStart=$SPIRALPOOL_DIR/bc2/bin/bitcoinIId -daemon -conf=$SPIRALPOOL_DIR/bc2/bitcoinii.conf -datadir=$SPIRALPOOL_DIR/bc2
ExecStop=$SPIRALPOOL_DIR/bc2/bin/bitcoinII-cli -conf=$SPIRALPOOL_DIR/bc2/bitcoinii.conf -datadir=$SPIRALPOOL_DIR/bc2 stop
Restart=always
RestartSec=30
TimeoutStartSec=infinity
TimeoutStopSec=600
# Note: WatchdogSec removed - blockchain daemons don't support sd_notify

# NOTE: Systemd security hardening intentionally omitted.
# Some blockchain daemons crash with SIGABRT under modern systemd
# hardening options (PrivateTmp, ProtectSystem, etc.)

[Install]
WantedBy=multi-user.target
EOF
            ufw allow 8338/tcp comment "Bitcoin II P2P" 2>/dev/null || true
            ufw allow 6333/tcp comment "BC2 Stratum V1" 2>/dev/null || true
            ufw allow 6334/tcp comment "BC2 Stratum V2" 2>/dev/null || true
            ;;

        LTC)
            mkdir -p "$SPIRALPOOL_DIR/ltc"
            chown -R "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/ltc"

            cat > "$SPIRALPOOL_DIR/ltc/litecoin.conf" << EOF
# LITECOIN CORE - SPIRAL POOL CONFIGURATION
# Algorithm: Scrypt (N=1024, r=1, p=1)
# Genesis: 12a765e31ffd4059bada1e25190f6e98c99d9714d334efa41a195a7e7e04bfe2
listen=1
port=9333
maxconnections=125
datadir=$SPIRALPOOL_DIR/ltc
server=1
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=9332
zmqpubhashblock=tcp://127.0.0.1:28933
zmqpubrawtx=tcp://127.0.0.1:28933
dbcache=1024
par=0
disablewallet=0
debuglogfile=$SPIRALPOOL_DIR/ltc/debug.log
printtoconsole=0
prune=0
EOF
            chown "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/ltc/litecoin.conf"
            chmod 600 "$SPIRALPOOL_DIR/ltc/litecoin.conf"

            cat > /etc/systemd/system/litecoind.service << EOF
[Unit]
Description=Litecoin Core Daemon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=forking
User=$POOL_USER
Group=$POOL_USER
ExecStart=/usr/local/bin/litecoind -daemon -conf=$SPIRALPOOL_DIR/ltc/litecoin.conf -pid=$SPIRALPOOL_DIR/ltc/litecoind.pid
ExecStop=/usr/local/bin/litecoin-cli -conf=$SPIRALPOOL_DIR/ltc/litecoin.conf stop
PIDFile=$SPIRALPOOL_DIR/ltc/litecoind.pid
Restart=always
RestartSec=30
TimeoutStartSec=infinity
TimeoutStopSec=600
# Note: WatchdogSec removed - blockchain daemons don't support sd_notify

# NOTE: Systemd security hardening intentionally omitted.
# Some blockchain daemons crash with SIGABRT under modern systemd
# hardening options (PrivateTmp, ProtectSystem, etc.)

[Install]
WantedBy=multi-user.target
EOF
            ufw allow 9333/tcp comment "Litecoin P2P" 2>/dev/null || true
            ufw allow 7333/tcp comment "LTC Stratum V1" 2>/dev/null || true
            ufw allow 7334/tcp comment "LTC Stratum V2" 2>/dev/null || true
            ;;

        DOGE)
            mkdir -p "$SPIRALPOOL_DIR/doge"
            chown -R "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/doge"

            cat > "$SPIRALPOOL_DIR/doge/dogecoin.conf" << EOF
# DOGECOIN CORE - SPIRAL POOL CONFIGURATION
# Algorithm: Scrypt (N=1024, r=1, p=1)
# Genesis: 1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691
# AuxPoW enabled from block 371,337
listen=1
port=22556
maxconnections=125
datadir=$SPIRALPOOL_DIR/doge
server=1
rpcuser=$rpc_user
rpcpassword=$rpc_pass
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport=22555
zmqpubhashblock=tcp://127.0.0.1:28555
zmqpubrawtx=tcp://127.0.0.1:28555
dbcache=1024
par=0
disablewallet=0
debuglogfile=$SPIRALPOOL_DIR/doge/debug.log
printtoconsole=0
prune=0
EOF
            chown "$POOL_USER:$POOL_USER" "$SPIRALPOOL_DIR/doge/dogecoin.conf"
            chmod 600 "$SPIRALPOOL_DIR/doge/dogecoin.conf"

            cat > /etc/systemd/system/dogecoind.service << EOF
[Unit]
Description=Dogecoin Core Daemon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=forking
User=$POOL_USER
Group=$POOL_USER
ExecStart=/usr/local/bin/dogecoind -daemon -conf=$SPIRALPOOL_DIR/doge/dogecoin.conf -pid=$SPIRALPOOL_DIR/doge/dogecoind.pid
ExecStop=/usr/local/bin/dogecoin-cli -conf=$SPIRALPOOL_DIR/doge/dogecoin.conf stop
PIDFile=$SPIRALPOOL_DIR/doge/dogecoind.pid
Restart=always
RestartSec=30
TimeoutStartSec=infinity
TimeoutStopSec=600
# Note: WatchdogSec removed - blockchain daemons don't support sd_notify

# NOTE: Systemd security hardening intentionally omitted.
# Some blockchain daemons crash with SIGABRT under modern systemd
# hardening options (PrivateTmp, ProtectSystem, etc.)

[Install]
WantedBy=multi-user.target
EOF
            ufw allow 22556/tcp comment "Dogecoin P2P" 2>/dev/null || true
            ufw allow 8335/tcp comment "DOGE Stratum V1" 2>/dev/null || true
            ufw allow 8337/tcp comment "DOGE Stratum V2" 2>/dev/null || true
            ;;

        DGB-SCRYPT)
            # DGB-SCRYPT uses the same node configuration as DGB
            # Just ensure DGB node is setup and add firewall rules for DGB-SCRYPT stratum
            if [ ! -f "$SPIRALPOOL_DIR/dgb/digibyte.conf" ]; then
                echo "Setting up DGB node for DGB-SCRYPT..."
                setup_node "DGB" "$rpc_user" "$rpc_pass"
            fi
            # Add firewall rule for DGB-SCRYPT stratum port
            ufw allow 3336/tcp comment "DGB-SCRYPT Stratum V1" 2>/dev/null || true
            ufw allow 3337/tcp comment "DGB-SCRYPT Stratum V2" 2>/dev/null || true
            ;;
    esac

    systemctl daemon-reload
}

# Start a node service
start_node() {
    local coin=$1
    local service=""

    case $coin in
        DGB) service="digibyted" ;;
        BTC) service="bitcoind" ;;
        BCH) service="bitcoind-bch" ;;
        BC2) service="bitcoiniid" ;;
        LTC) service="litecoind" ;;
        DOGE) service="dogecoind" ;;
        PEP) service="pepecoind" ;;
        CAT) service="catcoind" ;;
        NMC) service="namecoind" ;;
        SYS) service="syscoind" ;;
        XMY) service="myriadcoind" ;;
        FBTC) service="fractald" ;;
        DGB-SCRYPT) service="digibyted" ;;  # Uses same node as DGB
    esac

    if systemctl is-active --quiet "$service" 2>/dev/null; then
        echo -e "${GREEN}✓ $coin node already running${NC}"
    else
        echo "Starting $coin node..."
        systemctl enable "$service" 2>/dev/null || true
        systemctl start "$service" 2>/dev/null || true
        echo -e "${GREEN}✓ $coin node started${NC}"
    fi
}

# Stop a node service, close firewall ports, and remove service file
stop_node() {
    local coin=$1
    local service=""
    local service_file=""

    case $coin in
        DGB)
            service="digibyted"
            service_file="/etc/systemd/system/digibyted.service"
            ;;
        BTC)
            service="bitcoind"
            service_file="/etc/systemd/system/bitcoind.service"
            ;;
        BCH)
            service="bitcoind-bch"
            service_file="/etc/systemd/system/bitcoind-bch.service"
            ;;
        BC2)
            service="bitcoiniid"
            service_file="/etc/systemd/system/bitcoiniid.service"
            ;;
        LTC)
            service="litecoind"
            service_file="/etc/systemd/system/litecoind.service"
            ;;
        DOGE)
            service="dogecoind"
            service_file="/etc/systemd/system/dogecoind.service"
            ;;
        PEP)
            service="pepecoind"
            service_file="/etc/systemd/system/pepecoind.service"
            ;;
        CAT)
            service="catcoind"
            service_file="/etc/systemd/system/catcoind.service"
            ;;
        NMC)
            service="namecoind"
            service_file="/etc/systemd/system/namecoind.service"
            ;;
        SYS)
            service="syscoind"
            service_file="/etc/systemd/system/syscoind.service"
            ;;
        XMY)
            service="myriadcoind"
            service_file="/etc/systemd/system/myriadcoind.service"
            ;;
        FBTC)
            service="fractald"
            service_file="/etc/systemd/system/fractald.service"
            ;;
        DGB-SCRYPT)
            # DGB-SCRYPT uses the same node as DGB - don't stop node, just close stratum ports
            service=""
            service_file=""
            ;;
    esac

    # Stop and disable service
    if systemctl is-active --quiet "$service" 2>/dev/null; then
        echo "Stopping $coin node..."
        systemctl stop "$service" 2>/dev/null || true
    fi

    if systemctl is-enabled --quiet "$service" 2>/dev/null; then
        systemctl disable "$service" 2>/dev/null || true
    fi

    # Remove service file
    if [ -f "$service_file" ]; then
        echo "Removing $coin service file..."
        rm -f "$service_file"
        systemctl daemon-reload
        echo -e "${YELLOW}✓ $coin service removed${NC}"
    fi

    # Close firewall ports for removed coin
    echo "Closing firewall ports for $coin..."
    case $coin in
        DGB)
            ufw delete allow 12024/tcp 2>/dev/null || true
            ufw delete allow 3333/tcp 2>/dev/null || true
            ufw delete allow 3334/tcp 2>/dev/null || true
            ;;
        BTC)
            ufw delete allow 8333/tcp 2>/dev/null || true
            ufw delete allow 4333/tcp 2>/dev/null || true
            ufw delete allow 4334/tcp 2>/dev/null || true
            ;;
        BCH)
            ufw delete allow 8433/tcp 2>/dev/null || true
            ufw delete allow 5333/tcp 2>/dev/null || true
            ufw delete allow 5334/tcp 2>/dev/null || true
            ;;
        BC2)
            ufw delete allow 8338/tcp 2>/dev/null || true
            ufw delete allow 6333/tcp 2>/dev/null || true
            ufw delete allow 6334/tcp 2>/dev/null || true
            ;;
        LTC)
            ufw delete allow 9333/tcp 2>/dev/null || true
            ufw delete allow 7333/tcp 2>/dev/null || true
            ufw delete allow 7334/tcp 2>/dev/null || true
            ;;
        DOGE)
            ufw delete allow 22556/tcp 2>/dev/null || true
            ufw delete allow 8335/tcp 2>/dev/null || true
            ufw delete allow 8337/tcp 2>/dev/null || true
            ;;
        PEP)
            ufw delete allow 33874/tcp 2>/dev/null || true
            ufw delete allow 10335/tcp 2>/dev/null || true
            ufw delete allow 10336/tcp 2>/dev/null || true
            ;;
        CAT)
            ufw delete allow 9933/tcp 2>/dev/null || true
            ufw delete allow 12335/tcp 2>/dev/null || true
            ufw delete allow 12336/tcp 2>/dev/null || true
            ;;
        NMC)
            ufw delete allow 8334/tcp 2>/dev/null || true
            ufw delete allow 14335/tcp 2>/dev/null || true
            ufw delete allow 14336/tcp 2>/dev/null || true
            ;;
        SYS)
            ufw delete allow 8369/tcp 2>/dev/null || true
            ufw delete allow 15335/tcp 2>/dev/null || true
            ufw delete allow 15336/tcp 2>/dev/null || true
            ;;
        XMY)
            ufw delete allow 10888/tcp 2>/dev/null || true
            ufw delete allow 17335/tcp 2>/dev/null || true
            ufw delete allow 17336/tcp 2>/dev/null || true
            ;;
        FBTC)
            ufw delete allow 8341/tcp 2>/dev/null || true
            ufw delete allow 18335/tcp 2>/dev/null || true
            ufw delete allow 18336/tcp 2>/dev/null || true
            ;;
        DGB-SCRYPT)
            # Only close DGB-SCRYPT stratum ports, not DGB node ports
            ufw delete allow 3336/tcp 2>/dev/null || true
            ufw delete allow 3337/tcp 2>/dev/null || true
            ;;
    esac
    echo -e "${YELLOW}✓ Firewall ports closed for $coin${NC}"
}

# Verify and heal service state - ensures services match config
verify_services() {
    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  SERVICE VERIFICATION${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""

    detect_current_coins
    local issues_found=0

    # Check each coin
    for coin in DGB BTC BCH BC2 LTC DOGE DGB-SCRYPT PEP CAT NMC SYS XMY FBTC; do
        local service=""
        local service_file=""
        local should_be_running=false

        case $coin in
            DGB)
                service="digibyted"
                service_file="/etc/systemd/system/digibyted.service"
                ;;
            BTC)
                service="bitcoind"
                service_file="/etc/systemd/system/bitcoind.service"
                ;;
            BCH)
                service="bitcoind-bch"
                service_file="/etc/systemd/system/bitcoind-bch.service"
                ;;
            BC2)
                service="bitcoiniid"
                service_file="/etc/systemd/system/bitcoiniid.service"
                ;;
            LTC)
                service="litecoind"
                service_file="/etc/systemd/system/litecoind.service"
                ;;
            DOGE)
                service="dogecoind"
                service_file="/etc/systemd/system/dogecoind.service"
                ;;
            PEP)
                service="pepecoind"
                service_file="/etc/systemd/system/pepecoind.service"
                ;;
            CAT)
                service="catcoind"
                service_file="/etc/systemd/system/catcoind.service"
                ;;
            NMC)
                service="namecoind"
                service_file="/etc/systemd/system/namecoind.service"
                ;;
            SYS)
                service="syscoind"
                service_file="/etc/systemd/system/syscoind.service"
                ;;
            XMY)
                service="myriadcoind"
                service_file="/etc/systemd/system/myriadcoind.service"
                ;;
            FBTC)
                service="fractald"
                service_file="/etc/systemd/system/fractald.service"
                ;;
            DGB-SCRYPT)
                # DGB-SCRYPT uses the same node as DGB
                service="digibyted"
                service_file="/etc/systemd/system/digibyted.service"
                ;;
        esac

        # Check if coin is in current config
        for current in "${CURRENT_COINS[@]}"; do
            if [ "$coin" = "$current" ]; then
                should_be_running=true
                break
            fi
        done

        local is_running=$(systemctl is-active "$service" 2>/dev/null || echo "inactive")
        local service_exists=$([ -f "$service_file" ] && echo "yes" || echo "no")

        if [ "$should_be_running" = true ]; then
            # Should be running
            if [ "$is_running" != "active" ]; then
                echo -e "  ${YELLOW}⚠ $coin: Should be running but is $is_running${NC}"
                issues_found=$((issues_found + 1))

                read -p "    Start $coin node? (y/N): " fix_start
                if [[ "$fix_start" =~ ^[Yy]$ ]]; then
                    start_node "$coin"
                fi
            else
                echo -e "  ${GREEN}✓ $coin: Running (correct)${NC}"
            fi
        else
            # Should NOT be running
            if [ "$is_running" = "active" ]; then
                echo -e "  ${YELLOW}⚠ $coin: Running but not in config${NC}"
                issues_found=$((issues_found + 1))

                read -p "    Stop $coin node? (y/N): " fix_stop
                if [[ "$fix_stop" =~ ^[Yy]$ ]]; then
                    stop_node "$coin"
                fi
            elif [ "$service_exists" = "yes" ]; then
                echo -e "  ${YELLOW}⚠ $coin: Service file exists but coin not in config${NC}"
                issues_found=$((issues_found + 1))

                read -p "    Remove $coin service file? (y/N): " fix_remove
                if [[ "$fix_remove" =~ ^[Yy]$ ]]; then
                    rm -f "$service_file"
                    systemctl daemon-reload
                    echo -e "  ${GREEN}✓ Removed orphaned service file${NC}"
                fi
            else
                echo -e "  ${BLUE}○ $coin: Not configured (correct)${NC}"
            fi
        fi
    done

    echo ""
    if [ $issues_found -eq 0 ]; then
        echo -e "${GREEN}All services are in correct state.${NC}"
    else
        echo -e "${YELLOW}Found $issues_found issue(s). Review above.${NC}"
    fi
    echo ""
}

# Verify firewall rules match config
verify_firewall() {
    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  FIREWALL VERIFICATION${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""

    detect_current_coins

    # Define ports for each coin
    declare -A COIN_PORTS
    COIN_PORTS[DGB]="12024 3333 3334"
    COIN_PORTS[BTC]="8333 4333 4334"
    COIN_PORTS[BCH]="8433 5333 5334"
    COIN_PORTS[BC2]="8338 6333 6334"
    COIN_PORTS[LTC]="9333 7333 7334"
    COIN_PORTS[DOGE]="22556 8335 8337"
    COIN_PORTS[DGB-SCRYPT]="3336 3337"
    COIN_PORTS[PEP]="33874 10335 10336"
    COIN_PORTS[CAT]="9933 12335 12336"
    # SHA-256d AuxPoW merge-mined coins
    COIN_PORTS[NMC]="8334 14335 14336"
    COIN_PORTS[SYS]="8369 15335 15336"
    COIN_PORTS[XMY]="10888 17335 17336"
    COIN_PORTS[FBTC]="8341 18335 18336"

    local issues_found=0

    for coin in DGB BTC BCH BC2 LTC DOGE DGB-SCRYPT PEP CAT NMC SYS XMY FBTC; do
        local should_be_open=false
        for current in "${CURRENT_COINS[@]}"; do
            if [ "$coin" = "$current" ]; then
                should_be_open=true
                break
            fi
        done

        local ports="${COIN_PORTS[$coin]}"
        for port in $ports; do
            local is_open=$(ufw status | grep -q "$port/tcp.*ALLOW" && echo "yes" || echo "no")

            if [ "$should_be_open" = true ] && [ "$is_open" = "no" ]; then
                echo -e "  ${YELLOW}⚠ Port $port ($coin): Should be open but is closed${NC}"
                issues_found=$((issues_found + 1))
            elif [ "$should_be_open" = false ] && [ "$is_open" = "yes" ]; then
                echo -e "  ${YELLOW}⚠ Port $port ($coin): Open but coin not in config${NC}"
                issues_found=$((issues_found + 1))
            fi
        done
    done

    echo ""
    if [ $issues_found -eq 0 ]; then
        echo -e "${GREEN}All firewall rules are correct.${NC}"
    else
        echo -e "${YELLOW}Found $issues_found firewall issue(s).${NC}"
        read -p "Auto-fix firewall rules? (y/N): " fix_fw
        if [[ "$fix_fw" =~ ^[Yy]$ ]]; then
            # Close all coin ports first
            for coin in DGB BTC BCH BC2 LTC DOGE DGB-SCRYPT PEP CAT NMC SYS XMY FBTC; do
                local ports="${COIN_PORTS[$coin]}"
                for port in $ports; do
                    ufw delete allow $port/tcp 2>/dev/null || true
                done
            done
            # Re-open only for configured coins
            for coin in "${CURRENT_COINS[@]}"; do
                local ports="${COIN_PORTS[$coin]}"
                for port in $ports; do
                    ufw allow $port/tcp 2>/dev/null || true
                done
            done
            echo -e "${GREEN}✓ Firewall rules corrected${NC}"
        fi
    fi
    echo ""
}

# Generate the full config file
generate_config() {
    local coins=("$@")
    local backup_file="$CONFIG_FILE.backup.$(date +%Y%m%d_%H%M%S)"

    # Backup existing config
    if [ -f "$CONFIG_FILE" ]; then
        cp "$CONFIG_FILE" "$backup_file"
        echo -e "${GREEN}✓ Backed up existing configuration${NC}"
    fi

    # Extract database settings from backup or use defaults
    local db_password=""
    local db_user="spiralstratum"
    local db_name="spiralstratum"
    if [ -f "$backup_file" ]; then
        # Extract database section values
        local in_db_section=false
        local line_count=0
        while IFS= read -r line; do
            # Check if we're entering database section
            if [[ "$line" =~ ^database: ]]; then
                in_db_section=true
                line_count=0
                continue
            fi
            # Check if we're leaving the database section (next top-level key)
            if [[ "$in_db_section" == true && "$line" =~ ^[a-z]+: && ! "$line" =~ ^[[:space:]] ]]; then
                in_db_section=false
            fi
            # Extract values if we're in database section
            if [[ "$in_db_section" == true ]]; then
                if [[ "$line" =~ ^[[:space:]]+password: ]]; then
                    db_password=$(echo "$line" | sed 's/.*password:[[:space:]]*//' | tr -d '"'"'")
                elif [[ "$line" =~ ^[[:space:]]+user: ]]; then
                    db_user=$(echo "$line" | sed 's/.*user:[[:space:]]*//' | tr -d '"'"'")
                elif [[ "$line" =~ ^[[:space:]]+database: ]]; then
                    db_name=$(echo "$line" | sed 's/.*database:[[:space:]]*//' | tr -d '"'"'")
                fi
            fi
            # Safety: don't search more than 15 lines after database:
            ((line_count++)) || true
            if [[ $line_count -gt 15 ]]; then
                in_db_section=false
            fi
        done < "$backup_file"
    fi
    # SECURITY: If no password found, generate a secure random one
    if [ -z "$db_password" ] || [ "$db_password" = "changeme" ]; then
        db_password=$(openssl rand -base64 24 | tr -d '/+=' | head -c 32)
        echo -e "${YELLOW}⚠ Generated new database password (update your PostgreSQL accordingly)${NC}"
    else
        echo -e "${GREEN}✓ Preserved existing database credentials${NC}"
    fi

    # Start config file
    cat > "$CONFIG_FILE" << EOF
# Spiral Pool v1.0 Configuration
# Generated by pool-mode.sh on $(date)
# Mode: $([ ${#coins[@]} -eq 1 ] && echo "Solo" || echo "Multi-Coin")
# Coins: ${coins[*]}

version: 2

global:
  log_level: info
  log_format: json
  metrics_port: 9100
  api_port: 4000
  api_enabled: true

coins:
EOF

    # Add each coin config
    for coin in "${coins[@]}"; do
        local address=""
        local rpc_user=""
        local rpc_pass=""

        # Try to extract existing config
        if [ -f "$backup_file" ] && extract_coin_config "$coin" "$backup_file"; then
            address="$COIN_ADDR"
            rpc_user="$COIN_USER"
            rpc_pass="$COIN_PASS"
        fi

        # If we don't have values, check if they were passed or need to be collected
        if [ -z "$address" ]; then
            if [ -n "${WALLET_ADDRESSES[$coin]}" ]; then
                address="${WALLET_ADDRESSES[$coin]}"
            fi
        fi

        # Try to read existing RPC credentials from node config file
        # This ensures we use the same password the running node is using
        if [ -z "$rpc_user" ] || [ -z "$rpc_pass" ]; then
            if read_node_rpc_credentials "$coin"; then
                [ -z "$rpc_user" ] && rpc_user="$NODE_RPC_USER"
                [ -z "$rpc_pass" ] && rpc_pass="$NODE_RPC_PASS"
            fi
        fi

        # Generate RPC username if still not set
        if [ -z "$rpc_user" ]; then
            rpc_user="spiral${coin,,}"
        fi

        # Generate secure random password only if no existing password found
        if [ -z "$rpc_pass" ]; then
            rpc_pass=$(openssl rand -hex 32)
        fi

        # Generate and append coin config
        generate_coin_config "$coin" "$address" "$rpc_user" "$rpc_pass" >> "$CONFIG_FILE"

        # Setup node if needed
        if ! check_node_installed "$coin"; then
            install_node_if_needed "$coin"
        fi
        setup_node "$coin" "$rpc_user" "$rpc_pass"
        start_node "$coin"
    done

    # Add database config (V2 format)
    cat >> "$CONFIG_FILE" << EOF

database:
  host: 127.0.0.1
  port: 5432
  user: $db_user
  password: "$db_password"
  database: $db_name
  maxConnections: 30
  batching:
    size: 1000
    interval: 5s
EOF

    chown "$POOL_USER:$POOL_USER" "$CONFIG_FILE"
    echo -e "${GREEN}✓ Configuration generated${NC}"
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN OPERATIONS
# ═══════════════════════════════════════════════════════════════════════════════

# Show change summary before making changes
show_change_summary() {
    local new_coins=("$@")

    detect_current_coins

    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  CHANGE SUMMARY${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""

    # Current state
    if [ "$CURRENT_MODE" = "none" ]; then
        echo -e "  Current Mode: ${YELLOW}Not Configured${NC}"
    elif [ "$CURRENT_MODE" = "solo" ]; then
        echo -e "  Current Mode: ${BLUE}Solo (${CURRENT_COINS[*]})${NC}"
    else
        echo -e "  Current Mode: ${MAGENTA}Multi-Coin (${CURRENT_COINS[*]})${NC}"
    fi

    # New state
    if [ ${#new_coins[@]} -eq 1 ]; then
        echo -e "  New Mode:     ${BLUE}Solo (${new_coins[*]})${NC}"
    else
        echo -e "  New Mode:     ${MAGENTA}Multi-Coin (${new_coins[*]})${NC}"
    fi

    echo ""

    # Show what will be started
    local to_start=()
    local to_stop=()
    local to_install=()

    for coin in "${new_coins[@]}"; do
        local found=false
        for current in "${CURRENT_COINS[@]}"; do
            if [ "$coin" = "$current" ]; then
                found=true
                break
            fi
        done
        if [ "$found" = false ]; then
            if check_node_installed "$coin"; then
                to_start+=("$coin")
            else
                to_install+=("$coin")
            fi
        fi
    done

    # Show what will be stopped
    for current in "${CURRENT_COINS[@]}"; do
        local found=false
        for coin in "${new_coins[@]}"; do
            if [ "$current" = "$coin" ]; then
                found=true
                break
            fi
        done
        if [ "$found" = false ]; then
            to_stop+=("$current")
        fi
    done

    if [ ${#to_install[@]} -gt 0 ]; then
        echo -e "  ${GREEN}Will INSTALL & START:${NC} ${to_install[*]}"
        echo -e "    ${YELLOW}(This will download node software and begin blockchain sync)${NC}"
    fi

    if [ ${#to_start[@]} -gt 0 ]; then
        echo -e "  ${GREEN}Will START:${NC} ${to_start[*]}"
    fi

    if [ ${#to_stop[@]} -gt 0 ]; then
        echo -e "  ${RED}Will STOP:${NC} ${to_stop[*]}"
        echo -e "    ${YELLOW}• Node service stopped and disabled${NC}"
        echo -e "    ${YELLOW}• Firewall ports closed (P2P + Stratum)${NC}"
        echo -e "    ${YELLOW}• Blockchain data preserved (can re-enable later)${NC}"
    fi

    # Show coins that stay the same
    local unchanged=()
    for coin in "${new_coins[@]}"; do
        for current in "${CURRENT_COINS[@]}"; do
            if [ "$coin" = "$current" ]; then
                unchanged+=("$coin")
                break
            fi
        done
    done

    if [ ${#unchanged[@]} -gt 0 ]; then
        echo -e "  ${BLUE}Unchanged:${NC} ${unchanged[*]}"
    fi

    echo ""

    # Disk space warning for installs
    if [ ${#to_install[@]} -gt 0 ]; then
        local total_space=0
        for coin in "${to_install[@]}"; do
            case $coin in
                BTC) total_space=$((total_space + 600)) ;;
                BCH) total_space=$((total_space + 250)) ;;
                DGB) total_space=$((total_space + 60)) ;;
            esac
        done
        echo -e "  ${YELLOW}⚠ Disk space required: ~${total_space}GB for blockchain data${NC}"
        echo ""
    fi

    read -p "Proceed with these changes? (y/N): " confirm
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        echo "Cancelled."
        exit 0
    fi
}

# Switch to solo mode
switch_to_solo() {
    local coin=$1

    if ! validate_coin "$coin"; then
        echo -e "${RED}Error: Invalid coin '$coin'. Supported: DGB, BTC, BCH, BC2, LTC, DOGE, DGB-SCRYPT, PEP, CAT, NMC, SYS, XMY, FBTC${NC}"
        exit 1
    fi

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  SWITCHING TO SOLO MODE: $coin${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"

    # Show change summary and confirm
    show_change_summary "$coin"

    echo ""

    # Get wallet address if needed
    detect_current_coins

    declare -A WALLET_ADDRESSES

    # Check if coin is already configured
    local need_address=true
    if [ -f "$CONFIG_FILE" ]; then
        local backup_file="$CONFIG_FILE"
        if extract_coin_config "$coin" "$backup_file" && [ -n "$COIN_ADDR" ]; then
            echo -e "${BLUE}Found existing wallet address for $coin${NC}"
            read -p "Use existing address ($COIN_ADDR)? (Y/n): " use_existing
            if [[ ! "$use_existing" =~ ^[Nn]$ ]]; then
                WALLET_ADDRESSES[$coin]="$COIN_ADDR"
                need_address=false
            fi
        fi
    fi

    if [ "$need_address" = true ]; then
        WALLET_ADDRESSES[$coin]=$(get_wallet_address "$coin")
        if [ -z "${WALLET_ADDRESSES[$coin]}" ]; then
            echo -e "${RED}Error: Wallet address is required${NC}"
            exit 1
        fi
    fi

    # Stop other nodes (includes all SHA-256d and Scrypt coins)
    for other in DGB BTC BCH BC2 NMC SYS XMY FBTC LTC DOGE DGB-SCRYPT PEP CAT; do
        if [ "$other" != "$coin" ]; then
            stop_node "$other"
        fi
    done

    # Generate config with just this coin
    generate_config "$coin"

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  SOLO MODE ENABLED: $coin${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "${YELLOW}Restart Spiral Pool to apply changes:${NC}"
    echo "  sudo systemctl restart spiralstratum"
    echo ""

    # Check for HA cluster and warn about synchronization
    warn_ha_cluster_sync "$coin"
}

# Switch to multi-coin mode
switch_to_multi() {
    local coins_str=$1
    IFS=',' read -ra coins <<< "$coins_str"

    # Validate all coins
    for coin in "${coins[@]}"; do
        if ! validate_coin "$coin"; then
            echo -e "${RED}Error: Invalid coin '$coin'. Supported: DGB, BTC, BCH, BC2, LTC, DOGE, DGB-SCRYPT, PEP, CAT, NMC, SYS, XMY, FBTC${NC}"
            exit 1
        fi
    done

    # Need at least 2 coins for multi mode
    if [ ${#coins[@]} -lt 2 ]; then
        echo -e "${RED}Error: Multi-coin mode requires at least 2 coins${NC}"
        exit 1
    fi

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  SWITCHING TO MULTI-COIN MODE: ${coins[*]}${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"

    # Show change summary and confirm
    show_change_summary "${coins[@]}"

    echo ""

    declare -A WALLET_ADDRESSES

    # Get wallet addresses for each coin
    for coin in "${coins[@]}"; do
        local need_address=true

        if [ -f "$CONFIG_FILE" ]; then
            if extract_coin_config "$coin" "$CONFIG_FILE" && [ -n "$COIN_ADDR" ]; then
                echo -e "${BLUE}Found existing wallet address for $coin${NC}"
                read -p "Use existing address ($COIN_ADDR)? (Y/n): " use_existing
                if [[ ! "$use_existing" =~ ^[Nn]$ ]]; then
                    WALLET_ADDRESSES[$coin]="$COIN_ADDR"
                    need_address=false
                fi
            fi
        fi

        if [ "$need_address" = true ]; then
            WALLET_ADDRESSES[$coin]=$(get_wallet_address "$coin")
            if [ -z "${WALLET_ADDRESSES[$coin]}" ]; then
                echo -e "${RED}Error: Wallet address is required for $coin${NC}"
                exit 1
            fi
        fi
    done

    # Stop nodes not in the list (includes all SHA-256d and Scrypt coins)
    for other in DGB BTC BCH BC2 NMC SYS XMY FBTC LTC DOGE DGB-SCRYPT PEP CAT; do
        local in_list=false
        for coin in "${coins[@]}"; do
            if [ "$other" = "$coin" ]; then
                in_list=true
                break
            fi
        done
        if [ "$in_list" = false ]; then
            stop_node "$other"
        fi
    done

    # Generate config with selected coins
    generate_config "${coins[@]}"

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  MULTI-COIN MODE ENABLED: ${coins[*]}${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "${YELLOW}Restart Spiral Pool to apply changes:${NC}"
    echo "  sudo systemctl restart spiralstratum"
    echo ""

    # Check for HA cluster and warn about synchronization
    warn_ha_cluster_sync "${coins[@]}"
}

# Add a coin to current configuration
add_coin() {
    local coin=$1

    if ! validate_coin "$coin"; then
        echo -e "${RED}Error: Invalid coin '$coin'. Supported: DGB, BTC, BCH, BC2, LTC, DOGE, DGB-SCRYPT, PEP, CAT, NMC, SYS, XMY, FBTC${NC}"
        exit 1
    fi

    detect_current_coins

    # Check if already configured
    for existing in "${CURRENT_COINS[@]}"; do
        if [ "$existing" = "$coin" ]; then
            echo -e "${YELLOW}$coin is already configured${NC}"
            exit 0
        fi
    done

    # Build new coins list
    local new_coins_list=("${CURRENT_COINS[@]}" "$coin")

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  ADDING COIN: $coin${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"

    # Show change summary and confirm
    show_change_summary "${new_coins_list[@]}"

    echo ""

    # Update CURRENT_COINS after confirmation
    CURRENT_COINS+=("$coin")

    declare -A WALLET_ADDRESSES

    # Get wallet for new coin
    WALLET_ADDRESSES[$coin]=$(get_wallet_address "$coin")
    if [ -z "${WALLET_ADDRESSES[$coin]}" ]; then
        echo -e "${RED}Error: Wallet address is required${NC}"
        exit 1
    fi

    # Generate config with all coins
    generate_config "${CURRENT_COINS[@]}"

    # Check if node is installed
    if ! check_node_installed "$coin"; then
        echo ""
        echo -e "${YELLOW}$coin node is not installed. Would you like to install it?${NC}"
        read -p "Install $coin node? (Y/n): " install_node
        if [[ ! "$install_node" =~ ^[Nn]$ ]]; then
            install_node_if_needed "$coin"
        fi
    fi

    # Set up and start the node service
    if check_node_installed "$coin"; then
        echo ""
        echo -e "${CYAN}Setting up $coin node service...${NC}"

        # Generate RPC credentials if not already set
        local rpc_user="${RPC_USER:-spiralpool}"
        local rpc_pass="${RPC_PASS:-$(openssl rand -hex 32)}"

        setup_node "$coin" "$rpc_user" "$rpc_pass"
        start_node "$coin"

        echo -e "${GREEN}✓ $coin node service created and started${NC}"
        echo -e "  ${CYAN}Service: Restart=always, RestartSec=30${NC}"
        echo -e "  ${CYAN}The service will automatically restart on failure${NC}"
    fi

    echo ""
    echo -e "${GREEN}✓ Added $coin to configuration${NC}"
    echo ""
    echo -e "${YELLOW}Restart Spiral Pool to apply changes:${NC}"
    echo "  sudo systemctl restart spiralstratum"
    echo ""

    # Check for HA cluster and warn about synchronization
    warn_ha_cluster_sync "${CURRENT_COINS[@]}"
}

# Remove a coin from current configuration
remove_coin() {
    local coin=$1

    if ! validate_coin "$coin"; then
        echo -e "${RED}Error: Invalid coin '$coin'. Supported: DGB, BTC, BCH, BC2, LTC, DOGE, DGB-SCRYPT, PEP, CAT, NMC, SYS, XMY, FBTC${NC}"
        exit 1
    fi

    detect_current_coins

    # Check if configured
    local found=false
    local new_coins=()
    for existing in "${CURRENT_COINS[@]}"; do
        if [ "$existing" = "$coin" ]; then
            found=true
        else
            new_coins+=("$existing")
        fi
    done

    if [ "$found" = false ]; then
        echo -e "${YELLOW}$coin is not currently configured${NC}"
        exit 0
    fi

    # Need at least 1 coin
    if [ ${#new_coins[@]} -lt 1 ]; then
        echo -e "${RED}Error: Cannot remove the last coin. At least 1 coin must be configured.${NC}"
        exit 1
    fi

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  REMOVING COIN: $coin${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"

    # Show change summary and confirm
    show_change_summary "${new_coins[@]}"

    echo ""

    # Stop the removed coin's node and remove service
    echo -e "${CYAN}Stopping and removing $coin services...${NC}"
    stop_node "$coin"

    # Verify service was actually removed
    local service=""
    local service_file=""
    local data_dir=""
    case $coin in
        DGB)
            service="digibyted"
            service_file="/etc/systemd/system/digibyted.service"
            data_dir="$SPIRALPOOL_DIR/dgb"
            ;;
        BTC)
            service="bitcoind"
            service_file="/etc/systemd/system/bitcoind.service"
            data_dir="$SPIRALPOOL_DIR/btc"
            ;;
        BCH)
            service="bitcoind-bch"
            service_file="/etc/systemd/system/bitcoind-bch.service"
            data_dir="$SPIRALPOOL_DIR/bch"
            ;;
        BC2)
            service="bitcoiniid"
            service_file="/etc/systemd/system/bitcoiniid.service"
            data_dir="$SPIRALPOOL_DIR/bc2"
            ;;
        LTC)
            service="litecoind"
            service_file="/etc/systemd/system/litecoind.service"
            data_dir="$SPIRALPOOL_DIR/ltc"
            ;;
        DOGE)
            service="dogecoind"
            service_file="/etc/systemd/system/dogecoind.service"
            data_dir="$SPIRALPOOL_DIR/doge"
            ;;
        PEP)
            service="pepecoind"
            service_file="/etc/systemd/system/pepecoind.service"
            data_dir="$SPIRALPOOL_DIR/pep"
            ;;
        CAT)
            service="catcoind"
            service_file="/etc/systemd/system/catcoind.service"
            data_dir="$SPIRALPOOL_DIR/cat"
            ;;
        NMC)
            service="namecoind"
            service_file="/etc/systemd/system/namecoind.service"
            data_dir="$SPIRALPOOL_DIR/nmc"
            ;;
        SYS)
            service="syscoind"
            service_file="/etc/systemd/system/syscoind.service"
            data_dir="$SPIRALPOOL_DIR/sys"
            ;;
        XMY)
            service="myriadcoind"
            service_file="/etc/systemd/system/myriadcoind.service"
            data_dir="$SPIRALPOOL_DIR/xmy"
            ;;
        FBTC)
            service="fractald"
            service_file="/etc/systemd/system/fractald.service"
            data_dir="$SPIRALPOOL_DIR/fbtc"
            ;;
        DGB-SCRYPT)
            # DGB-SCRYPT uses the same node as DGB - don't remove DGB node
            service=""
            service_file=""
            data_dir=""  # Don't offer to remove DGB data
            ;;
    esac

    # Double-check service is stopped and disabled
    if systemctl is-active --quiet "$service" 2>/dev/null; then
        echo -e "${YELLOW}Warning: Service still running, force stopping...${NC}"
        systemctl stop "$service" 2>/dev/null || true
        systemctl kill "$service" 2>/dev/null || true
    fi

    if systemctl is-enabled --quiet "$service" 2>/dev/null; then
        systemctl disable "$service" 2>/dev/null || true
    fi

    # Remove service file if still exists
    if [ -f "$service_file" ]; then
        rm -f "$service_file"
        systemctl daemon-reload
    fi

    echo -e "${GREEN}✓ $coin service stopped and removed${NC}"

    # Ask about data cleanup
    if [ -d "$data_dir" ]; then
        echo ""
        echo -e "${YELLOW}$coin data directory exists at: $data_dir${NC}"
        echo -e "${YELLOW}This may contain blockchain data and wallet files.${NC}"
        read -p "Remove $coin data directory? (y/N): " remove_data
        if [[ "$remove_data" =~ ^[Yy]$ ]]; then
            echo -e "${RED}WARNING: This will permanently delete all $coin data including wallets!${NC}"
            read -p "Type 'DELETE' to confirm: " confirm_delete
            if [ "$confirm_delete" = "DELETE" ]; then
                rm -rf "$data_dir"
                echo -e "${GREEN}✓ $coin data directory removed${NC}"
            else
                echo -e "${YELLOW}Data directory preserved${NC}"
            fi
        else
            echo -e "${CYAN}Data directory preserved at: $data_dir${NC}"
        fi
    fi

    # Generate config without the removed coin
    declare -A WALLET_ADDRESSES
    generate_config "${new_coins[@]}"

    echo ""
    echo -e "${GREEN}✓ Removed $coin from configuration${NC}"
    echo -e "  ${CYAN}Service: Stopped, disabled, and removed${NC}"
    echo -e "  ${CYAN}Firewall: Ports closed${NC}"
    echo ""
    echo -e "${YELLOW}Restart Spiral Pool to apply changes:${NC}"
    echo "  sudo systemctl restart spiralstratum"
    echo ""

    # Check for HA cluster and warn about synchronization
    warn_ha_cluster_sync "${new_coins[@]}"
}

# Interactive menu
interactive_menu() {
    print_banner
    print_status

    # Check HA status for menu display
    detect_ha_enabled
    local ha_label=""
    if [ "$HA_ENABLED" = true ]; then
        ha_label=" ${GREEN}[HA ENABLED]${NC}"
    fi

    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  SELECT AN ACTION${ha_label}${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo "  [1] Switch to Solo Mode (single coin)"
    echo "  [2] Switch to Multi-Coin Mode"
    echo "  [3] Add a coin to current configuration"
    echo "  [4] Remove a coin from current configuration"
    echo "  [5] Show current status"
    echo "  [6] Verify & Heal Services"
    echo -e "  [7] ${CYAN}HA Cluster Setup${NC} (SSH keys + peers)"
    if [ "$HA_ENABLED" = true ]; then
        echo -e "  [8] ${CYAN}HA Cluster Status${NC}"
        echo -e "  [9] ${CYAN}Sync HA Cluster${NC}"
        echo "  [0] Exit"
        echo ""
        read -p "Select option (1-9, 0): " choice
    else
        echo "  [8] Exit"
        echo ""
        read -p "Select option (1-8): " choice
    fi

    case $choice in
        1)
            echo ""
            echo "Select coin for Solo Mode:"
            echo "  === SHA-256d (ASIC) ==="
            echo "  [1]  DigiByte (DGB)"
            echo "  [2]  Bitcoin (BTC)"
            echo "  [3]  Bitcoin Cash (BCH)"
            echo "  [4]  Bitcoin II (BC2)"
            echo "  === SHA-256d AuxPoW (merge-mineable) ==="
            echo "  [5]  Namecoin (NMC)"
            echo "  [6]  Syscoin (SYS) — merge-mining only"
            echo "  [7]  Myriadcoin (XMY)"
            echo "  [8]  Fractal Bitcoin (FBTC)"
            echo "  === Scrypt (ASIC/GPU) ==="
            echo "  [9]  Litecoin (LTC)"
            echo "  [10] Dogecoin (DOGE)"
            echo "  [11] DigiByte-Scrypt (DGB-SCRYPT)"
            echo "  [12] PepeCoin (PEP)"
            echo "  [13] Catcoin (CAT)"
            read -p "Select coin (1-13): " coin_choice
            case $coin_choice in
                1) switch_to_solo "DGB" ;;
                2) switch_to_solo "BTC" ;;
                3) switch_to_solo "BCH" ;;
                4) switch_to_solo "BC2" ;;
                5) switch_to_solo "NMC" ;;
                6) echo -e "${YELLOW}⚠ SYS cannot solo mine (requires CbTx/quorum commitment). Use multi-coin mode with BTC + SYS.${NC}" ;;
                7) switch_to_solo "XMY" ;;
                8) switch_to_solo "FBTC" ;;
                9) switch_to_solo "LTC" ;;
                10) switch_to_solo "DOGE" ;;
                11) switch_to_solo "DGB-SCRYPT" ;;
                12) switch_to_solo "PEP" ;;
                13) switch_to_solo "CAT" ;;
                *) echo "Invalid selection" ;;
            esac
            ;;
        2)
            echo ""
            echo "Select coins for Multi-Coin Mode:"
            echo "  === SHA-256d Combinations ==="
            echo "  [1]  DGB + BTC"
            echo "  [2]  DGB + BCH"
            echo "  [3]  DGB + BC2"
            echo "  [4]  BTC + BCH"
            echo "  [5]  BTC + BC2"
            echo "  [6]  All SHA-256d (DGB + BTC + BCH + BC2)"
            echo "  === SHA-256d + AuxPoW ==="
            echo "  [7]  All SHA-256d + Aux (DGB + BTC + BCH + BC2 + NMC + SYS + XMY + FBTC)"
            echo "  === Scrypt Combinations ==="
            echo "  [8]  LTC + DOGE"
            echo "  [9]  DGB + DGB-SCRYPT (SHA256 + Scrypt on same node)"
            echo "  [10] All Scrypt (LTC + DOGE + DGB-SCRYPT + PEP + CAT)"
            echo "  === Mixed Algorithm ==="
            echo "  [11] DGB + LTC"
            echo "  [12] BTC + LTC"
            echo "  [13] All 13 coins"
            read -p "Select combination (1-13): " coin_choice
            case $coin_choice in
                1) switch_to_multi "DGB,BTC" ;;
                2) switch_to_multi "DGB,BCH" ;;
                3) switch_to_multi "DGB,BC2" ;;
                4) switch_to_multi "BTC,BCH" ;;
                5) switch_to_multi "BTC,BC2" ;;
                6) switch_to_multi "DGB,BTC,BCH,BC2" ;;
                7) switch_to_multi "DGB,BTC,BCH,BC2,NMC,SYS,XMY,FBTC" ;;
                8) switch_to_multi "LTC,DOGE" ;;
                9) switch_to_multi "DGB,DGB-SCRYPT" ;;
                10) switch_to_multi "LTC,DOGE,DGB-SCRYPT,PEP,CAT" ;;
                11) switch_to_multi "DGB,LTC" ;;
                12) switch_to_multi "BTC,LTC" ;;
                13) switch_to_multi "DGB,BTC,BCH,BC2,NMC,SYS,XMY,FBTC,LTC,DOGE,DGB-SCRYPT,PEP,CAT" ;;
                *) echo "Invalid selection" ;;
            esac
            ;;
        3)
            echo ""
            echo "Select coin to add:"
            echo "  === SHA-256d (ASIC) ==="
            echo "  [1]  DigiByte (DGB)"
            echo "  [2]  Bitcoin (BTC)"
            echo "  [3]  Bitcoin Cash (BCH)"
            echo "  [4]  Bitcoin II (BC2)"
            echo "  === SHA-256d AuxPoW (merge-mineable) ==="
            echo "  [5]  Namecoin (NMC)"
            echo "  [6]  Syscoin (SYS) — merge-mining only"
            echo "  [7]  Myriadcoin (XMY)"
            echo "  [8]  Fractal Bitcoin (FBTC)"
            echo "  === Scrypt (ASIC/GPU) ==="
            echo "  [9]  Litecoin (LTC)"
            echo "  [10] Dogecoin (DOGE)"
            echo "  [11] DigiByte-Scrypt (DGB-SCRYPT)"
            echo "  [12] PepeCoin (PEP)"
            echo "  [13] Catcoin (CAT)"
            read -p "Select coin (1-13): " coin_choice
            case $coin_choice in
                1) add_coin "DGB" ;;
                2) add_coin "BTC" ;;
                3) add_coin "BCH" ;;
                4) add_coin "BC2" ;;
                5) add_coin "NMC" ;;
                6) add_coin "SYS" ;;
                7) add_coin "XMY" ;;
                8) add_coin "FBTC" ;;
                9) add_coin "LTC" ;;
                10) add_coin "DOGE" ;;
                11) add_coin "DGB-SCRYPT" ;;
                12) add_coin "PEP" ;;
                13) add_coin "CAT" ;;
                *) echo "Invalid selection" ;;
            esac
            ;;
        4)
            echo ""
            echo "Select coin to remove:"
            echo "  === SHA-256d (ASIC) ==="
            echo "  [1]  DigiByte (DGB)"
            echo "  [2]  Bitcoin (BTC)"
            echo "  [3]  Bitcoin Cash (BCH)"
            echo "  [4]  Bitcoin II (BC2)"
            echo "  === SHA-256d AuxPoW (merge-mineable) ==="
            echo "  [5]  Namecoin (NMC)"
            echo "  [6]  Syscoin (SYS) — merge-mining only"
            echo "  [7]  Myriadcoin (XMY)"
            echo "  [8]  Fractal Bitcoin (FBTC)"
            echo "  === Scrypt (ASIC/GPU) ==="
            echo "  [9]  Litecoin (LTC)"
            echo "  [10] Dogecoin (DOGE)"
            echo "  [11] DigiByte-Scrypt (DGB-SCRYPT)"
            echo "  [12] PepeCoin (PEP)"
            echo "  [13] Catcoin (CAT)"
            read -p "Select coin (1-13): " coin_choice
            case $coin_choice in
                1) remove_coin "DGB" ;;
                2) remove_coin "BTC" ;;
                3) remove_coin "BCH" ;;
                4) remove_coin "BC2" ;;
                5) remove_coin "NMC" ;;
                6) remove_coin "SYS" ;;
                7) remove_coin "XMY" ;;
                8) remove_coin "FBTC" ;;
                9) remove_coin "LTC" ;;
                10) remove_coin "DOGE" ;;
                11) remove_coin "DGB-SCRYPT" ;;
                12) remove_coin "PEP" ;;
                13) remove_coin "CAT" ;;
                *) echo "Invalid selection" ;;
            esac
            ;;
        5)
            print_status
            ;;
        6)
            verify_services
            verify_firewall
            ;;
        7)
            # HA Cluster Setup (always available)
            echo ""
            echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
            echo -e "${CYAN}  HA CLUSTER SETUP${NC}"
            echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
            echo ""
            echo -e "${YELLOW}This will set up SSH key authentication for HA cluster communication.${NC}"
            echo ""
            echo -e "${GREEN}Security Model:${NC}"
            echo "  • SSH keys provide encrypted, passwordless authentication"
            echo "  • RPC credentials are NEVER shared between nodes"
            echo "  • Each node keeps its own credentials private"
            echo "  • Health checks execute locally on remote nodes via SSH"
            echo ""

            if [ ${#HA_BACKUP_SERVERS[@]} -eq 0 ]; then
                echo -e "${YELLOW}No HA peer servers configured yet.${NC}"
                echo ""
                echo "To configure HA peers, add them to one of these files:"
                echo "  • /spiralpool/config/ha_cluster.conf"
                echo "  • /spiralpool/config/ha.yaml"
                echo ""
                echo "Example ha_cluster.conf format:"
                echo "  backup_servers: 192.168.1.100, 192.168.1.101"
                echo ""
                read -p "Would you like to add HA peer servers now? (y/N): " add_peers
                if [[ "$add_peers" =~ ^[Yy]$ ]]; then
                    echo ""
                    echo "Enter peer server addresses (comma-separated):"
                    echo "Example: 192.168.1.100, 192.168.1.101"
                    read -p "Peer servers: " peer_input

                    if [ -n "$peer_input" ]; then
                        mkdir -p "$SPIRALPOOL_DIR/config"
                        echo "# HA Cluster Peers" > "$HA_CLUSTER_FILE"
                        echo "# Generated by pool-mode.sh on $(date)" >> "$HA_CLUSTER_FILE"
                        echo "backup_servers: $peer_input" >> "$HA_CLUSTER_FILE"
                        echo ""
                        echo -e "${GREEN}✓ HA cluster configuration saved to $HA_CLUSTER_FILE${NC}"

                        # Re-detect with new config
                        detect_ha_enabled
                    fi
                fi
            fi

            if [ ${#HA_BACKUP_SERVERS[@]} -gt 0 ]; then
                echo -e "${CYAN}Configured HA Peers:${NC}"
                for server in "${HA_BACKUP_SERVERS[@]}"; do
                    echo "  • $server"
                done
                echo ""

                # Verify and set up SSH connectivity
                verify_ha_ssh_connectivity

                echo ""
                echo -e "${GREEN}HA setup complete.${NC}"
                echo ""
                echo "Next steps:"
                echo "  1. Enable HA in /spiralpool/config/ha.yaml (set enabled: true)"
                echo "  2. Configure the same on peer servers"
                echo "  3. Run: sudo pool-mode.sh --ha-sync"
            else
                echo -e "${YELLOW}No HA peers configured. Setup cancelled.${NC}"
            fi
            echo ""
            ;;
        8)
            if [ "$HA_ENABLED" = true ]; then
                # HA Cluster Status
                echo ""
                echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
                echo -e "${CYAN}  HA CLUSTER STATUS${NC}"
                echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
                echo ""
                if [ ${#HA_BACKUP_SERVERS[@]} -gt 0 ]; then
                    echo -e "  ${CYAN}HA Cluster Peers:${NC}"
                    for server in "${HA_BACKUP_SERVERS[@]}"; do
                        if ping -c 1 -W 2 "$server" &>/dev/null; then
                            echo -e "    ${GREEN}●${NC} $server (reachable)"
                        else
                            echo -e "    ${RED}●${NC} $server (unreachable)"
                        fi
                    done
                    echo ""
                    echo -e "  ${CYAN}Blockchain Sync Status on Peers:${NC}"
                    for server in "${HA_BACKUP_SERVERS[@]}"; do
                        echo -e "  ${BLUE}$server:${NC}"
                        for coin in "${CURRENT_COINS[@]}"; do
                            check_remote_blockchain_sync "$server" "$coin"
                            local status=$?
                            case $status in
                                0) echo -e "    ${GREEN}✓${NC} $coin: Synced" ;;
                                1) echo -e "    ${RED}✗${NC} $coin: Node not running" ;;
                                3) echo -e "    ${YELLOW}⚠${NC} $coin: Still syncing" ;;
                                4) echo -e "    ${RED}✗${NC} $coin: Not installed" ;;
                                *) echo -e "    ${YELLOW}?${NC} $coin: Unknown" ;;
                            esac
                        done
                    done
                else
                    echo -e "  ${YELLOW}No HA peer servers configured.${NC}"
                fi
                echo ""
                echo -e "  ${CYAN}Security: Checks use SSH (credentials never shared)${NC}"
                echo ""
            else
                echo "Exiting..."
                exit 0
            fi
            ;;
        9)
            if [ "$HA_ENABLED" = true ]; then
                # Sync HA Cluster
                if [ ${#CURRENT_COINS[@]} -eq 0 ]; then
                    echo -e "${RED}No coins configured. Configure coins first.${NC}"
                else
                    echo -e "${CYAN}Synchronizing HA cluster...${NC}"
                    sync_ha_cluster "${CURRENT_COINS[@]}"
                fi
            else
                echo "Invalid selection"
            fi
            ;;
        0)
            if [ "$HA_ENABLED" = true ]; then
                echo "Exiting..."
                exit 0
            else
                echo "Invalid selection"
            fi
            ;;
        *)
            echo "Invalid selection"
            ;;
    esac
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN ENTRY POINT
# ═══════════════════════════════════════════════════════════════════════════════

check_root
check_installation

# Parse command line arguments
case "${1:-}" in
    --solo)
        if [ -z "$2" ]; then
            echo -e "${RED}Error: --solo requires a coin symbol (DGB, BTC, BCH, or BC2)${NC}"
            exit 1
        fi
        switch_to_solo "${2^^}"
        ;;
    --multi)
        if [ -z "$2" ]; then
            echo -e "${RED}Error: --multi requires comma-separated coin symbols (e.g., DGB,BTC)${NC}"
            exit 1
        fi
        switch_to_multi "${2^^}"
        ;;
    --add)
        if [ -z "$2" ]; then
            echo -e "${RED}Error: --add requires a coin symbol (DGB, BTC, BCH, or BC2)${NC}"
            exit 1
        fi
        add_coin "${2^^}"
        ;;
    --remove)
        if [ -z "$2" ]; then
            echo -e "${RED}Error: --remove requires a coin symbol (DGB, BTC, BCH, or BC2)${NC}"
            exit 1
        fi
        remove_coin "${2^^}"
        ;;
    --status)
        print_banner
        print_status
        ;;
    --verify)
        print_banner
        verify_services
        verify_firewall
        ;;
    --ha-status)
        print_banner
        echo ""
        echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
        echo -e "${CYAN}  HA CLUSTER STATUS${NC}"
        echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
        echo ""

        # Query the VIP manager status API
        local status_port=5354
        local status_response

        status_response=$(curl -s --connect-timeout 2 "http://127.0.0.1:${status_port}/status" 2>/dev/null)

        if [ -z "$status_response" ]; then
            echo -e "  HA Status: ${YELLOW}DISABLED or NOT RUNNING${NC}"
            echo ""
            echo "  The VIP manager is not responding on port $status_port."
            echo "  Either HA is not enabled or the stratum server is not running."
            echo ""
            echo "  To enable HA, configure /spiralpool/config/config.yaml:"
            echo "    ha:"
            echo "      enabled: true"
            echo "      vipAddress: 192.168.1.200"
            echo "      clusterToken: <your-token>"
            exit 0
        fi

        # Parse JSON response (basic parsing with grep/sed)
        local role=$(echo "$status_response" | grep -o '"role":"[^"]*"' | cut -d'"' -f4)
        local state=$(echo "$status_response" | grep -o '"state":"[^"]*"' | cut -d'"' -f4)
        local vip=$(echo "$status_response" | grep -o '"vipAddress":"[^"]*"' | cut -d'"' -f4)
        local has_vip=$(echo "$status_response" | grep -o '"hasVIP":[^,}]*' | cut -d':' -f2)
        local node_count=$(echo "$status_response" | grep -o '"nodeCount":[^,}]*' | cut -d':' -f2)

        echo -e "  HA Status:    ${GREEN}ENABLED${NC}"
        echo -e "  Role:         ${CYAN}$role${NC}"
        echo -e "  State:        ${CYAN}$state${NC}"
        echo -e "  VIP Address:  ${CYAN}$vip${NC}"
        if [ "$has_vip" = "true" ]; then
            echo -e "  VIP Owned:    ${GREEN}YES (this node holds the VIP)${NC}"
        else
            echo -e "  VIP Owned:    ${YELLOW}NO (another node holds the VIP)${NC}"
        fi
        echo -e "  Cluster Size: ${CYAN}$node_count nodes${NC}"
        echo ""
        echo -e "  ${CYAN}For detailed status: spiralctl vip status${NC}"
        echo ""
        ;;
    --ha-sync|--ha-setup|--ha-init|--ha-join|--ha-show-key|--ha-add-key|--ha-configure-sshd|--ha-propagate|--ha-health)
        # All SSH-based HA commands have been removed
        print_banner
        echo ""
        echo -e "${YELLOW}═══════════════════════════════════════════════════════════════${NC}"
        echo -e "${YELLOW}  COMMAND REMOVED${NC}"
        echo -e "${YELLOW}═══════════════════════════════════════════════════════════════${NC}"
        echo ""
        echo -e "This command has been removed. SSH is no longer required for HA."
        echo ""
        echo "HA cluster communication now uses encrypted UDP (AES-256-GCM) via the"
        echo "VIP manager. Configuration is done during installation or by editing"
        echo "/spiralpool/config/config.yaml directly."
        echo ""
        echo -e "${GREEN}To set up HA:${NC}"
        echo "  1. Install PRIMARY node with 'ha-master' mode"
        echo "  2. Copy the cluster token from the installation summary"
        echo "  3. Install BACKUP node with 'ha-backup' mode, paste the token"
        echo "  4. VIP failover works automatically"
        echo ""
        echo -e "${CYAN}Useful commands:${NC}"
        echo "  pool-mode.sh --ha-status    Check cluster status"
        echo "  spiralctl vip status        Detailed VIP status"
        echo "  spiralctl vip failover      Force failover to this node"
        echo ""
        exit 0
        ;;
    --ha-remove-node)
        print_banner
        if [ -z "$2" ]; then
            echo -e "${RED}Error: --ha-remove-node requires a node address${NC}"
            echo ""
            echo "Usage: $0 --ha-remove-node <node-ip>"
            echo ""
            echo "This removes a node from the cluster configuration file."
            echo "Run this when a node is permanently decommissioned."
            echo ""
            echo "Note: The VIP manager will automatically detect node failures."
            echo "This command is only needed to clean up the configuration."
            exit 1
        fi
        echo ""
        echo -e "${CYAN}Removing $2 from cluster config...${NC}"
        remove_peer_from_cluster_config "$2"
        echo -e "${GREEN}✓ Removed $2 from local cluster config${NC}"
        echo ""
        echo "Restart spiralstratum to apply changes: sudo systemctl restart spiralstratum"
        echo ""
        ;;
    --ha-add-node)
        print_banner
        if [ -z "$2" ]; then
            echo -e "${RED}Error: --ha-add-node requires a node address${NC}"
            echo ""
            echo "Usage: $0 --ha-add-node <node-ip>"
            echo ""
            echo "This adds a node to the local cluster configuration."
            echo ""
            echo "Note: For new nodes, it's easier to configure during installation."
            echo "The VIP manager will automatically discover nodes on the same network."
            exit 1
        fi
        echo ""
        echo -e "${CYAN}Adding $2 to cluster config...${NC}"
        add_peer_to_cluster_config "$2"
        echo -e "${GREEN}✓ Added $2 to local cluster config${NC}"
        echo ""
        echo -e "${YELLOW}Next steps:${NC}"
        echo "  1. Ensure the new node has the same cluster token in config.yaml"
        echo "  2. Restart spiralstratum: sudo systemctl restart spiralstratum"
        echo "  3. Check cluster status: pool-mode.sh --ha-status"
        echo ""
        ;;
    --help|-h)
        print_banner
        echo "Usage: $0 [OPTIONS]"
        echo ""
        echo "Options:"
        echo "  --solo COIN         Switch to solo mode with specified coin"
        echo "  --multi COINS       Switch to multi-coin mode (comma-separated)"
        echo "  --add COIN          Add a coin to current configuration"
        echo "  --remove COIN       Remove a coin from current configuration"
        echo "  --status            Show current configuration"
        echo "  --verify            Verify services and firewall match config (self-heal)"
        echo ""
        echo "HA Cluster Options:"
        echo "  --ha-status         Show HA cluster status (queries VIP manager API)"
        echo ""
        echo "  NOTE: HA cluster communication uses encrypted UDP (AES-256-GCM)."
        echo "        No SSH setup is required. Configure HA during installation or"
        echo "        edit /spiralpool/config/config.yaml directly."
        echo ""
        echo "HA Cluster Management:"
        echo "  --ha-add-node NODE  Add a node to cluster config (edit config.yaml)"
        echo "  --ha-remove-node N  Remove a node from cluster config"
        echo "  --ha-health         Check cluster health and offer self-healing"
        echo "  --ha-propagate      Propagate membership to all nodes"
        echo ""
        echo "  --help, -h          Show this help message"
        echo ""
        echo "Examples:"
        echo "  $0                          Interactive mode"
        echo "  $0 --solo DGB               Switch to solo DGB mining (SHA-256d)"
        echo "  $0 --solo LTC               Switch to solo LTC mining (Scrypt)"
        echo "  $0 --solo DOGE              Switch to solo DOGE mining (Scrypt)"
        echo "  $0 --multi DGB,BTC          Switch to multi-coin (SHA-256d)"
        echo "  $0 --multi LTC,DOGE         Switch to Scrypt coins only"
        echo "  $0 --multi DGB,LTC          Mixed algorithm (SHA-256d + Scrypt)"
        echo "  $0 --multi DGB,BTC,BCH,BC2,LTC,DOGE  Enable all six coins"
        echo "  $0 --add LTC                Add LTC to current config"
        echo "  $0 --remove BTC             Remove BTC from current config"
        echo "  $0 --verify                 Check services/firewall, fix if needed"
        echo ""
        echo "HA Cluster Examples:"
        echo "  $0 --ha-status          Check HA cluster status via VIP manager API"
        echo ""
        echo "HA Setup (during installation):"
        echo "  1. On PRIMARY node: Run installer with 'ha-master' mode"
        echo "  2. Copy the cluster token from the installation summary"
        echo "  3. On BACKUP node: Run installer with 'ha-backup' mode, paste token"
        echo "  4. VIP failover works automatically - no SSH required"
        echo ""
        echo "Manual HA Configuration:"
        echo "  Edit /spiralpool/config/config.yaml and set:"
        echo "    ha.enabled: true"
        echo "    ha.vipAddress: 192.168.1.200"
        echo "    ha.clusterToken: <your-token>"
        echo ""
        ;;
    "")
        interactive_menu
        ;;
    *)
        echo -e "${RED}Unknown option: $1${NC}"
        echo "Use --help for usage information"
        exit 1
        ;;
esac
