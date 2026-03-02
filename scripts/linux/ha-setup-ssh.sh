#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Spiral Pool HA SSH Key Setup Script
# Version: 1.0.0
# License: BSD-3-Clause
#
# PURPOSE:
#   Automatically configure SSH key-based authentication for HA replication
#
# USAGE:
#   ./ha-setup-ssh.sh --mode [primary|standby] --peer <IP>
#
# MODES:
#   primary  - Setup SSH keys on primary node (for standby to pull from)
#   standby  - Setup SSH keys on standby node (to pull from primary)
#
# WHAT IT DOES:
#   1. Generates SSH keys for spiraluser (blockchain replication)
#   2. Configures sudoers on primary to allow spiraluser to run rsync as postgres
#   3. Copies spiraluser public key to peer node (with confirmation)
#   4. Tests SSH connections and sudo access
#   5. Optionally restricts SSH keys to rsync-only commands
#
# SECURITY NOTE:
#   Uses sudoers rule to allow spiraluser to run rsync as postgres.
#   This is more secure than enabling postgres user SSH login or using root SSH.
#
# AUTHOR: Spiral Pool HA Infrastructure Team
#

set -euo pipefail

# ============================================================================
# CONFIGURATION
# ============================================================================

SCRIPT_VERSION="1.0.0"
SCRIPT_NAME="$(basename "$0")"
POOL_USER="${POOL_USER:-spiraluser}"

# Validate POOL_USER — used in SSH commands, sudoers, file paths
if [[ ! "$POOL_USER" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
    echo "ERROR: Invalid POOL_USER '${POOL_USER}' — must be a valid Unix username" >&2
    exit 1
fi

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ============================================================================
# LOGGING
# ============================================================================

log() {
    local level="$1"
    shift
    local message="$*"

    case "$level" in
        INFO)
            echo -e "${BLUE}[INFO]${NC} $message" >&2
            ;;
        WARN)
            echo -e "${YELLOW}[WARN]${NC} $message" >&2
            ;;
        ERROR)
            echo -e "${RED}[ERROR]${NC} $message" >&2
            ;;
        SUCCESS)
            echo -e "${GREEN}[SUCCESS]${NC} $message" >&2
            ;;
    esac
}

die() {
    log ERROR "$@"
    exit 1
}

# ============================================================================
# SSH KEY FUNCTIONS
# ============================================================================

generate_spiraluser_ssh_key() {
    log INFO "Checking spiraluser SSH keys..."

    local ssh_dir="/home/${POOL_USER}/.ssh"
    local key_file="${ssh_dir}/id_ed25519"

    # Ensure .ssh directory exists
    if [[ ! -d "$ssh_dir" ]]; then
        sudo -u "$POOL_USER" mkdir -p "$ssh_dir"
    fi
    # Always ensure correct ownership/permissions (fixes issues from previous runs)
    sudo chown "${POOL_USER}:${POOL_USER}" "$ssh_dir"
    sudo chmod 700 "$ssh_dir"

    # Check if key exists (as root to avoid permission issues with existing files)
    if [[ -f "$key_file" ]]; then
        # Fix ownership on existing key files
        sudo chown "${POOL_USER}:${POOL_USER}" "$key_file"
        sudo chmod 600 "$key_file"
        [[ -f "${key_file}.pub" ]] && sudo chown "${POOL_USER}:${POOL_USER}" "${key_file}.pub" && sudo chmod 644 "${key_file}.pub"
        log INFO "spiraluser SSH key already exists: $key_file"
        log WARN "Skipping key generation (use existing key)."
        return 0
    fi

    log INFO "Generating SSH key for ${POOL_USER}..."

    # Generate SSH key
    sudo -u "$POOL_USER" ssh-keygen -t ed25519 \
        -C "spiralpool-ha-blockchain" \
        -f "$key_file" \
        -N ""

    log SUCCESS "SSH key generated for ${POOL_USER}: ${key_file}"
}

configure_postgres_sudo_on_primary() {
    local primary_ip="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "Configuring sudo access for postgres on PRIMARY node"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    log INFO "This will configure sudoers on ${primary_ip} to allow:"
    log INFO "  ${POOL_USER} ALL=(postgres) NOPASSWD: /usr/bin/rsync"
    log INFO ""
    log WARN "You will need root access to ${primary_ip} to configure sudoers."
    echo -e "${YELLOW}▶${NC} Configure postgres sudo on ${primary_ip}? (yes/no)"
    read -r response

    if [[ "$response" != "yes" ]]; then
        log WARN "Skipping sudoers configuration."
        log WARN "You must manually configure this on ${primary_ip} for PostgreSQL replication to work."
        return 1
    fi

    # Create sudoers rule on primary
    log INFO "Creating sudoers rule on ${primary_ip}..."

    local sudoers_content="${POOL_USER} ALL=(postgres) NOPASSWD: /usr/bin/rsync"
    local sudoers_file="/etc/sudoers.d/spiralpool-ha-postgres"

    # Execute on primary node
    # BUG FIX (M5): Add BatchMode + ConnectTimeout to prevent indefinite hang
    # if root SSH is disabled (common hardening practice).
    if echo "${sudoers_content}" | ssh -o BatchMode=yes -o ConnectTimeout=10 "root@${primary_ip}" "cat > '${sudoers_file}' && chmod 0440 '${sudoers_file}' && visudo -c"; then
        log SUCCESS "Sudoers rule configured on ${primary_ip}"
        log INFO "Rule added: ${sudoers_file}"
        log INFO "Content: ${sudoers_content}"
        return 0
    else
        log ERROR "Failed to configure sudoers on ${primary_ip}"
        log ERROR ""
        log ERROR "Manual configuration required on PRIMARY node (${primary_ip}):"
        log ERROR "  sudo visudo -f /etc/sudoers.d/spiralpool-ha-postgres"
        log ERROR ""
        log ERROR "Add this line:"
        log ERROR "  ${sudoers_content}"
        log ERROR ""
        log ERROR "Then validate:"
        log ERROR "  sudo visudo -c"
        return 1
    fi
}

copy_spiraluser_key_to_peer() {
    local peer_ip="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "Copying spiraluser SSH key to ${POOL_USER}@${peer_ip}"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    local key_file="/home/${POOL_USER}/.ssh/id_ed25519.pub"

    if [[ ! -f "$key_file" ]]; then
        die "spiraluser public key not found: $key_file"
    fi

    log WARN "You will be prompted for ${POOL_USER}'s password on ${peer_ip}."
    log WARN "If ${POOL_USER} password is not set, configure it first on ${peer_ip}:"
    log WARN "  ssh root@${peer_ip} \"passwd ${POOL_USER}\""

    # Copy SSH key
    if sudo -u "$POOL_USER" ssh-copy-id -i "$key_file" "${POOL_USER}@${peer_ip}"; then
        log SUCCESS "spiraluser SSH key copied to ${peer_ip}"
    else
        log ERROR "Failed to copy spiraluser SSH key to ${peer_ip}"
        log ERROR "Possible reasons:"
        log ERROR "  1. ${POOL_USER} password not set on ${peer_ip}"
        log ERROR "  2. SSH service not running on ${peer_ip}"
        log ERROR "  3. Firewall blocking SSH port 22"
        return 1
    fi
}


test_spiraluser_ssh() {
    local peer_ip="$1"

    log INFO "Testing spiraluser SSH connection to ${peer_ip}..."

    if sudo -u "$POOL_USER" ssh -o BatchMode=yes -o ConnectTimeout=5 "${POOL_USER}@${peer_ip}" "echo 'SSH OK'" &>/dev/null; then
        log SUCCESS "spiraluser SSH connection successful (no password required)"
        return 0
    else
        log ERROR "spiraluser SSH connection failed"
        log ERROR "Debug: sudo -u $POOL_USER ssh ${POOL_USER}@${peer_ip} \"echo SSH OK\""
        return 1
    fi
}

test_postgres_sudo() {
    local peer_ip="$1"

    log INFO "Testing postgres sudo access on ${peer_ip}..."

    if sudo -u "$POOL_USER" ssh -o BatchMode=yes -o ConnectTimeout=5 "${POOL_USER}@${peer_ip}" "sudo -u postgres -n true" &>/dev/null; then
        log SUCCESS "postgres sudo access verified (spiraluser can run commands as postgres)"

        # Additional test: verify rsync can be executed
        if sudo -u "$POOL_USER" ssh -o BatchMode=yes -o ConnectTimeout=5 "${POOL_USER}@${peer_ip}" "sudo -u postgres -n rsync --version" &>/dev/null; then
            log SUCCESS "postgres rsync sudo access verified"
            return 0
        else
            log ERROR "postgres sudo access works, but rsync execution failed"
            log ERROR "Check sudoers rule on ${peer_ip}: ${POOL_USER} ALL=(postgres) NOPASSWD: /usr/bin/rsync"
            return 1
        fi
    else
        log ERROR "postgres sudo access test failed"
        log ERROR "Debug: sudo -u $POOL_USER ssh ${POOL_USER}@${peer_ip} 'sudo -u postgres -n true'"
        log ERROR ""
        log ERROR "Ensure sudoers is configured on ${peer_ip}:"
        log ERROR "  sudo visudo -f /etc/sudoers.d/spiralpool-ha-postgres"
        log ERROR "  ${POOL_USER} ALL=(postgres) NOPASSWD: /usr/bin/rsync"
        return 1
    fi
}

restrict_ssh_keys() {
    # NOTE: The from="${PEER_IP}" restriction limits SSH access to a SINGLE source IP.
    # For 3+ node clusters with multiple standbys, omit --restrict or use unrestricted keys.
    # The automated SSH mesh in install.sh setup_ha_ssh_keys() uses unrestricted keys.
    local mode="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "OPTIONAL: Restrict SSH Keys to rsync-only Commands"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    if [[ "$mode" != "primary" ]]; then
        log INFO "Key restriction only applies to PRIMARY node."
        return 0
    fi

    log WARN "This will restrict SSH keys to ONLY allow rsync commands."
    log WARN "This improves security but prevents manual SSH login using these keys."
    echo -e "${YELLOW}▶${NC} Restrict SSH keys to rsync-only? (yes/no)"
    read -r response

    if [[ "$response" != "yes" ]]; then
        log INFO "Skipping SSH key restriction."
        return 0
    fi

    # Restrict spiraluser SSH key
    log INFO "Restricting spiraluser SSH key..."
    local spiraluser_auth_keys="/home/${POOL_USER}/.ssh/authorized_keys"

    if [[ -f "$spiraluser_auth_keys" ]]; then
        # Backup original
        sudo cp "$spiraluser_auth_keys" "${spiraluser_auth_keys}.backup"

        # Add command restriction to last key (most recent)
        local last_key
        last_key=$(sudo tail -n 1 "$spiraluser_auth_keys")

        if [[ "$last_key" =~ ^ssh- ]] || [[ "$last_key" =~ ^ecdsa- ]] || [[ "$last_key" =~ ^ed25519- ]]; then
            # Remove last key
            sudo sed -i '$ d' "$spiraluser_auth_keys"

            # Add restricted key
            printf '%s\n' "command=\"/usr/bin/rsync --server --sender -logDtprze.iLsfxC . /spiralpool/\",from=\"${PEER_IP}\",restrict ${last_key}" | sudo tee -a "$spiraluser_auth_keys" > /dev/null

            log SUCCESS "spiraluser SSH key restricted to rsync (blockchain data only)"
        else
            log WARN "Could not restrict spiraluser key (unexpected format)"
        fi
    else
        log WARN "spiraluser authorized_keys not found: $spiraluser_auth_keys"
    fi

    log INFO "Backup created:"
    log INFO "  ${spiraluser_auth_keys}.backup"
    echo ""
    log INFO "NOTE: PostgreSQL replication uses sudo (not SSH keys), so no postgres key restriction needed."
}

# ============================================================================
# MAIN WORKFLOW
# ============================================================================

setup_primary() {
    local peer_ip="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "PRIMARY NODE SSH SETUP"
    log INFO "This node: PRIMARY (source for replication)"
    log INFO "Peer node: ${peer_ip} (STANDBY)"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    log INFO "Primary node setup:"
    log INFO "  1. No SSH key generation needed (standby pulls from primary)"
    log INFO "  2. Optionally restrict incoming SSH keys (after standby setup)"
    log INFO ""
    log INFO "NEXT STEPS:"
    log INFO "  1. Run this script on STANDBY node: ./ha-setup-ssh.sh --mode standby --peer $(hostname -I | awk '{print $1}')"
    log INFO "  2. After standby setup completes, optionally run: ./ha-setup-ssh.sh --mode primary --peer ${peer_ip} --restrict"
    log INFO ""

    echo -e "${YELLOW}▶${NC} Do you want to restrict incoming SSH keys now? (yes/no)"
    echo -e "   ${BLUE}(Recommended after standby is configured)${NC}"
    read -r response

    if [[ "$response" == "yes" ]]; then
        restrict_ssh_keys "primary"
    else
        log INFO "Skipping SSH key restriction. You can run it later with --restrict flag."
    fi
}

setup_standby() {
    local peer_ip="$1"

    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log INFO "STANDBY NODE SSH SETUP"
    log INFO "This node: STANDBY (pulls replication from primary)"
    log INFO "Peer node: ${peer_ip} (PRIMARY)"
    log INFO "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    # Step 1: Generate SSH keys for spiraluser
    generate_spiraluser_ssh_key

    # Step 2: Copy spiraluser key to primary
    echo ""
    log INFO "Step 2: Copy spiraluser SSH key to primary node..."
    if ! copy_spiraluser_key_to_peer "$peer_ip"; then
        die "Failed to copy spiraluser key. Fix the issue and re-run this script."
    fi

    # Step 3: Configure postgres sudo on primary
    echo ""
    log INFO "Step 3: Configure postgres sudo access on primary..."
    if ! configure_postgres_sudo_on_primary "$peer_ip"; then
        log WARN "Postgres sudo configuration skipped or failed."
        log WARN "PostgreSQL replication will NOT work until sudo is configured on ${peer_ip}."
    fi

    # Step 4: Test connections
    echo ""
    log INFO "Step 4: Testing SSH and sudo access..."
    test_spiraluser_ssh "$peer_ip" || die "spiraluser SSH test failed"
    test_postgres_sudo "$peer_ip" || log WARN "postgres sudo test failed (configure manually on ${peer_ip})"

    # Success
    echo ""
    log SUCCESS "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log SUCCESS "SSH SETUP COMPLETE!"
    log SUCCESS "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log SUCCESS "Standby node can now replicate from primary using:"
    log SUCCESS "  sudo ./scripts/linux/ha-replicate.sh --mode blockchain --source ${peer_ip} --execute"
    echo ""
    if ! test_postgres_sudo "$peer_ip" &>/dev/null; then
        log WARN "PostgreSQL replication NOT ready (postgres sudo not configured)."
        log WARN "Configure sudo on PRIMARY (${peer_ip}), then test with:"
        log WARN "  sudo ./scripts/linux/ha-replicate.sh --mode postgres --source ${peer_ip} --dry-run"
    else
        log SUCCESS "PostgreSQL replication ready! Test with:"
        log SUCCESS "  sudo ./scripts/linux/ha-replicate.sh --mode postgres --source ${peer_ip} --dry-run"
    fi
}

# ============================================================================
# USAGE
# ============================================================================

usage() {
    cat <<EOF
Spiral Pool HA SSH Setup Script v${SCRIPT_VERSION}

USAGE:
    $SCRIPT_NAME --mode <MODE> --peer <IP> [OPTIONS]

MODES:
    primary     Setup on primary node (configure incoming SSH key restrictions)
    standby     Setup on standby node (generate keys, copy to primary)

REQUIRED ARGUMENTS:
    --peer <IP>     IP address of peer node (primary if standby, standby if primary)

OPTIONS:
    --restrict      Restrict SSH keys to rsync-only commands (primary mode only)
    --help          Show this help message

EXAMPLES:
    # On standby node: Generate keys and copy to primary
    $SCRIPT_NAME --mode standby --peer 192.168.1.10

    # On primary node: Restrict incoming SSH keys (after standby setup)
    $SCRIPT_NAME --mode primary --peer 192.168.1.11 --restrict

PREREQUISITES:
    - Spiral Pool installed on both nodes
    - SSH service running on both nodes
    - Firewall allows SSH port 22 between nodes
    - ${POOL_USER} user exists on both nodes
    - Root login enabled on primary (for sudoers configuration)

For more information, see: docs/reference/REFERENCE.md
EOF
}

# ============================================================================
# ARGUMENT PARSING
# ============================================================================

MODE=""
PEER_IP=""
RESTRICT=false

if [[ $# -eq 0 ]]; then
    usage
    exit 0
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        --mode)
            MODE="$2"
            shift 2
            ;;
        --peer)
            PEER_IP="$2"
            shift 2
            ;;
        --restrict)
            RESTRICT=true
            shift
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            die "Unknown argument: $1. Use --help for usage information."
            ;;
    esac
done

# Validate arguments
if [[ -z "$MODE" ]]; then
    die "Missing required argument: --mode. Use --help for usage information."
fi

if [[ -z "$PEER_IP" ]]; then
    die "Missing required argument: --peer. Use --help for usage information."
fi

# Validate PEER_IP is a real IP address (prevents SSH option injection)
if [[ ! "$PEER_IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "Invalid peer IP: $PEER_IP. Must be a valid IPv4 address."
fi

if [[ ! "$MODE" =~ ^(primary|standby)$ ]]; then
    die "Invalid mode: $MODE. Must be one of: primary, standby"
fi

# Check root
if [[ $EUID -ne 0 ]]; then
    die "This script must be run as root (sudo)"
fi

# Execute
case "$MODE" in
    primary)
        if [[ "$RESTRICT" == "true" ]]; then
            restrict_ssh_keys "primary"
        else
            setup_primary "$PEER_IP"
        fi
        ;;
    standby)
        setup_standby "$PEER_IP"
        ;;
esac

exit 0
