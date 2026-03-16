#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║                                                                            ║
# ║           SPIRAL POOL - PATRONI POSTGRESQL HA INSTALLER                    ║
# ║                                                                            ║
# ║   Installs Patroni for automatic PostgreSQL failover                       ║
# ║                                                                            ║
# ║   PREREQUISITES:                                                           ║
# ║   - Spiral Pool already installed with HA mode (ha-master or ha-backup)    ║
# ║   - PostgreSQL 18 running                                                  ║
# ║   - Root/sudo access                                                       ║
# ║                                                                            ║
# ║   ARCHITECTURE:                                                            ║
# ║   - etcd cluster for distributed consensus (leader election)               ║
# ║   - Patroni manages PostgreSQL lifecycle and promotion                     ║
# ║   - Pool's pg_is_in_recovery() check remains authoritative                 ║
# ║                                                                            ║
# ║   IMPORTANT: This script CONVERTS an existing HA setup to use Patroni.     ║
# ║   It does NOT change pool behavior - only adds automatic DB promotion.     ║
# ║                                                                            ║
# ╚════════════════════════════════════════════════════════════════════════════╝

set -euo pipefail

# ── etcd credentials ─────────────────────────────────────────────────────────
# SECURITY (etcd-auth): Load etcd root password if authentication is configured.
# Written by the installer to /spiralpool/config/etcd-auth.conf (mode 640,
# root-owned, spiralpool-group-readable). Sets ETCDCTL_USER so all etcdctl calls
# in this script authenticate automatically without per-call --user flags.
if [[ -f /spiralpool/config/etcd-auth.conf ]]; then
    # shellcheck source=/dev/null
    source /spiralpool/config/etcd-auth.conf
    [[ -n "${ETCD_ROOT_PASS:-}" ]] && export ETCDCTL_USER="root:${ETCD_ROOT_PASS}"
fi
export ETCDCTL_API=3

# ═══════════════════════════════════════════════════════════════════════════════
# CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="${INSTALL_DIR:-/spiralpool}"
POSTGRES_VERSION="${POSTGRES_VERSION:-18}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
NC='\033[0m'
DIM='\033[2m'

# ═══════════════════════════════════════════════════════════════════════════════
# HELPER FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════════════

log() {
    echo -e "${WHITE}[$(date '+%H:%M:%S')]${NC} $*"
}

log_success() {
    echo -e "${GREEN}✓${NC} $*"
}

log_error() {
    echo -e "${RED}✗${NC} $*" >&2
}

log_warn() {
    echo -e "${YELLOW}⚠${NC} $*"
}

check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root (sudo)"
        exit 1
    fi
}

# ═══════════════════════════════════════════════════════════════════════════════
# PREREQUISITE CHECKS
# ═══════════════════════════════════════════════════════════════════════════════

check_prerequisites() {
    log "Checking prerequisites..."

    # Check PostgreSQL is installed
    if ! command -v psql &>/dev/null; then
        log_error "PostgreSQL is not installed. Run install.sh first."
        exit 1
    fi

    # Check PostgreSQL version
    local pg_version
    pg_version=$(psql --version | grep -oP '\d+' | head -1)
    if [[ "$pg_version" -lt 14 ]]; then
        log_error "PostgreSQL 14+ required. Found version $pg_version"
        exit 1
    fi
    log_success "PostgreSQL $pg_version detected"

    # Check PostgreSQL is running
    if ! systemctl is-active --quiet postgresql; then
        log_error "PostgreSQL is not running"
        exit 1
    fi
    log_success "PostgreSQL is running"

    # Check if this is an HA installation
    if [[ ! -f "/etc/spiralpool/ha.env" ]]; then
        log_warn "HA configuration not detected. This script is designed for HA setups."
        read -p "Continue anyway? [y/N]: " confirm
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
            exit 0
        fi
    else
        log_success "HA configuration detected"
    fi

    # Check Python 3
    if ! command -v python3 &>/dev/null; then
        log_error "Python 3 is required for Patroni"
        exit 1
    fi
    log_success "Python 3 detected"
}

# ═══════════════════════════════════════════════════════════════════════════════
# ETCD INSTALLATION
# ═══════════════════════════════════════════════════════════════════════════════

install_etcd() {
    log "Installing etcd..."

    # Check if etcd is already installed
    if command -v etcd &>/dev/null; then
        log_success "etcd already installed"
        return 0
    fi

    # Install etcd from package manager
    # Ubuntu 24.04 split the 'etcd' package into 'etcd-server' + 'etcd-client'
    add-apt-repository -y universe > /dev/null 2>&1 || true
    apt-get update -qq
    apt-get install -y -qq etcd-server etcd-client

    log_success "etcd installed"
}

configure_etcd() {
    local node_name="$1"
    local node_ip="$2"
    local cluster_peers="$3"  # Format: "node1=http://ip1:2380,node2=http://ip2:2380,node3=http://ip3:2380"

    log "Configuring etcd..."

    # Create etcd configuration
    cat > /etc/default/etcd << EOF
# Spiral Pool etcd configuration
# Generated by install-patroni.sh on $(date)

ETCD_NAME="$node_name"
ETCD_DATA_DIR="/var/lib/etcd"
ETCD_LISTEN_CLIENT_URLS="http://${node_ip}:2379,http://127.0.0.1:2379"
ETCD_ADVERTISE_CLIENT_URLS="http://${node_ip}:2379"
ETCD_LISTEN_PEER_URLS="http://0.0.0.0:2380"
ETCD_INITIAL_ADVERTISE_PEER_URLS="http://${node_ip}:2380"
ETCD_INITIAL_CLUSTER="$cluster_peers"
ETCD_INITIAL_CLUSTER_STATE="new"
ETCD_INITIAL_CLUSTER_TOKEN="spiralpool-etcd-cluster"
EOF

    # Enable and start etcd
    systemctl enable etcd
    systemctl restart etcd

    # Wait for etcd to be ready (etcd 3.4+ outputs health to stderr, so use 2>&1)
    local retries=30
    while [[ $retries -gt 0 ]]; do
        if ETCDCTL_API=3 etcdctl --command-timeout=5s endpoint health 2>&1 | grep -q "is healthy"; then
            log_success "etcd is healthy"
            return 0
        fi
        sleep 1
        retries=$((retries - 1))
    done

    log_error "etcd failed to become healthy"
    return 1
}

# ═══════════════════════════════════════════════════════════════════════════════
# PATRONI INSTALLATION
# ═══════════════════════════════════════════════════════════════════════════════

install_patroni() {
    log "Installing Patroni..."

    # Install Patroni in a virtual environment (PEP 668 compliant for Ubuntu 24.04)
    local patroni_venv="/opt/patroni/venv"
    mkdir -p /opt/patroni
    python3 -m venv "$patroni_venv"
    "$patroni_venv/bin/pip" install --upgrade pip -q 2>/dev/null
    "$patroni_venv/bin/pip" install --quiet patroni[etcd] python-etcd psycopg2-binary

    # Symlink patroni and patronictl into /usr/local/bin for convenience
    ln -sf "$patroni_venv/bin/patroni" /usr/local/bin/patroni
    ln -sf "$patroni_venv/bin/patronictl" /usr/local/bin/patronictl

    # Create Patroni directories
    mkdir -p /etc/patroni
    mkdir -p /var/log/patroni

    log_success "Patroni installed"
}

configure_patroni() {
    local node_name="$1"
    local node_ip="$2"
    local etcd_hosts="$3"      # Format: "host1:2379,host2:2379,host3:2379"
    local db_password="$4"
    local repl_password="$5"

    log "Configuring Patroni..."

    # Derive or load Patroni REST API credentials.
    # If patroni-api.conf exists (written by main installer), reuse its credentials so
    # all cluster nodes share the same username/password.
    # Otherwise generate a fresh random password and write the file.
    local patroni_api_user="spiralpool"
    local patroni_api_pass=""
    if [[ -f /spiralpool/config/patroni-api.conf ]]; then
        # shellcheck source=/dev/null
        source /spiralpool/config/patroni-api.conf
        patroni_api_pass="${PATRONI_API_PASSWORD:-}"
    fi
    if [[ -z "${patroni_api_pass}" ]]; then
        patroni_api_pass=$(openssl rand -hex 16)
        mkdir -p /spiralpool/config
        tee /spiralpool/config/patroni-api.conf > /dev/null << PATRONIEOF
# Spiral Pool -- Patroni REST API credentials
# Generated by install-patroni.sh on $(date)
# Mode 640: root-owned, pool-user-group-readable (spiraluser can read via SSH)
PATRONI_API_USERNAME="${patroni_api_user}"
PATRONI_API_PASSWORD="${patroni_api_pass}"
PATRONIEOF
        local pool_user="${POOL_USER:-spiraluser}"
        chown root:"${pool_user}" /spiralpool/config/patroni-api.conf 2>/dev/null || chown root:root /spiralpool/config/patroni-api.conf
        chmod 640 /spiralpool/config/patroni-api.conf
        log_success "Patroni REST API credentials written to /spiralpool/config/patroni-api.conf"
    fi

    # Generate Patroni configuration
    cat > /etc/patroni/patroni.yml << EOF
# Spiral Pool Patroni Configuration
# Generated by install-patroni.sh on $(date)
#
# IMPORTANT: The pool's pg_is_in_recovery() check remains authoritative.
# Patroni handles promotion; the pool handles write routing.

scope: spiralpool-postgres
namespace: /spiralpool/
name: ${node_name}

restapi:
  listen: 0.0.0.0:8008
  connect_address: ${node_ip}:8008
  authentication:
    username: ${patroni_api_user}
    password: ${patroni_api_pass}

etcd3:
  hosts: ${etcd_hosts}
  username: root
  password: ${ETCD_ROOT_PASS}

bootstrap:
  dcs:
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    maximum_lag_on_failover: 1048576  # 1MB - tight for mining pool consistency
    failsafe_mode: true               # Keep primary running if etcd goes down (prevents unnecessary demotion)
    synchronous_mode: true            # CRITICAL: Designed to minimize data loss on failover
    synchronous_mode_strict: false    # Allow async if no sync replica available
    postgresql:
      use_pg_rewind: true
      use_slots: true
      remove_data_directory_on_diverged_timelines: true  # Auto-wipe replica on timeline divergence
      parameters:
        # Connection settings
        max_connections: 200
        # WAL settings for synchronous replication
        wal_level: replica
        hot_standby: 'on'
        max_wal_senders: 10
        max_replication_slots: 10
        wal_keep_size: '1GB'
        # Synchronous commit for data safety
        synchronous_commit: 'on'
        # Shared buffers (adjust based on available memory)
        shared_buffers: '1GB'
        effective_cache_size: '3GB'
        # Logging
        log_destination: 'stderr'
        logging_collector: 'off'
        log_statement: 'ddl'
        log_min_duration_statement: 1000

  initdb:
    - encoding: UTF8
    - data-checksums

  # NOTE: pg_hba uses /24 subnet to allow peer node connections.
  # For tighter security, replace with /32 entries for each specific node IP.
  pg_hba:
    - local all all peer
    - host replication replicator samenet scram-sha-256
    - host all all samenet scram-sha-256
    - host replication replicator 127.0.0.1/32 scram-sha-256
    - host all all 127.0.0.1/32 scram-sha-256

postgresql:
  listen: 0.0.0.0:5432
  connect_address: ${node_ip}:5432
  data_dir: /var/lib/postgresql/${POSTGRES_VERSION}/main
  bin_dir: /usr/lib/postgresql/${POSTGRES_VERSION}/bin
  config_dir: /etc/postgresql/${POSTGRES_VERSION}/main

  authentication:
    replication:
      username: replicator
      password: "${repl_password}"
    superuser:
      username: postgres
      password: "${db_password}"

  parameters:
    unix_socket_directories: '/var/run/postgresql'

tags:
  nofailover: false
  noloadbalance: false
  clonefrom: false
  nosync: false
EOF

    # Secure the configuration file
    chmod 600 /etc/patroni/patroni.yml
    chown postgres:postgres /etc/patroni/patroni.yml

    log_success "Patroni configured"
}

create_patroni_service() {
    log "Creating Patroni systemd service..."

    cat > /etc/systemd/system/patroni.service << 'EOF'
[Unit]
Description=Patroni PostgreSQL HA Manager
Documentation=https://patroni.readthedocs.io
After=network.target etcd.service
Requires=etcd.service

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=/opt/patroni/venv/bin/patroni /etc/patroni/patroni.yml
ExecReload=/bin/kill -HUP $MAINPID
KillMode=process
TimeoutSec=30
Restart=always
RestartSec=5

# Security hardening
NoNewPrivileges=false
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/etc/postgresql

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=patroni

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    log_success "Patroni service created"
}

# ═══════════════════════════════════════════════════════════════════════════════
# MIGRATION FROM STANDALONE POSTGRESQL
# ═══════════════════════════════════════════════════════════════════════════════

migrate_to_patroni() {
    log "Migrating PostgreSQL to Patroni management..."

    # Stop PostgreSQL service (Patroni will manage it)
    log "Stopping PostgreSQL service..."
    systemctl stop postgresql
    systemctl disable postgresql

    # Start Patroni
    log "Starting Patroni..."
    systemctl enable patroni
    if ! systemctl start patroni 2>&1; then
        log_error "Patroni failed to start. Rolling back to standalone PostgreSQL..."
        systemctl disable patroni 2>/dev/null || true
        systemctl enable postgresql
        systemctl start postgresql
        return 1
    fi

    # Wait for Patroni to initialize
    local retries=60
    while [[ $retries -gt 0 ]]; do
        if curl -sf http://localhost:8008/health &>/dev/null; then
            log_success "Patroni is healthy"
            return 0
        fi
        sleep 2
        retries=$((retries - 1))
    done

    log_error "Patroni failed to become healthy within 120 seconds."
    log_error "Rolling back to standalone PostgreSQL..."
    systemctl stop patroni 2>/dev/null || true
    systemctl disable patroni 2>/dev/null || true
    systemctl enable postgresql
    systemctl start postgresql
    return 1
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN INSTALLATION FLOW
# ═══════════════════════════════════════════════════════════════════════════════

print_banner() {
    echo ""
    echo -e "${CYAN}╔════════════════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║${NC}                                                                            ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}           ${WHITE}SPIRAL POOL - PATRONI HA INSTALLER${NC}                               ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                            ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}   ${DIM}Automatic PostgreSQL failover for mining pool high availability${NC}          ${CYAN}║${NC}"
    echo -e "${CYAN}║${NC}                                                                            ${CYAN}║${NC}"
    echo -e "${CYAN}╚════════════════════════════════════════════════════════════════════════════╝${NC}"
    echo ""
}

collect_configuration() {
    echo -e "${WHITE}Patroni Configuration${NC}"
    echo ""

    # Node name
    local default_name
    default_name="patroni-$(hostname -s 2>/dev/null | tr -dc 'A-Za-z0-9_-' || echo 'node')"
    read -p "Node name [$default_name]: " PATRONI_NODE_NAME
    PATRONI_NODE_NAME="${PATRONI_NODE_NAME:-$default_name}"
    # Validate node name (alphanumeric, hyphens, underscores, dots only)
    if [[ ! "$PATRONI_NODE_NAME" =~ ^[a-zA-Z0-9._-]+$ ]] || [[ ${#PATRONI_NODE_NAME} -gt 64 ]]; then
        log_error "Node name must be alphanumeric (with hyphens/underscores/dots), max 64 chars"
        exit 1
    fi

    # Node IP
    local default_ip
    default_ip=$(ip -4 addr show scope global | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | head -1)
    read -p "This node's IP [$default_ip]: " PATRONI_NODE_IP
    PATRONI_NODE_IP="${PATRONI_NODE_IP:-$default_ip}"
    # Validate IP address format and octet ranges
    if [[ ! "$PATRONI_NODE_IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
        log_error "Invalid IP address format: $PATRONI_NODE_IP"
        exit 1
    fi
    IFS='.' read -r _o1 _o2 _o3 _o4 <<< "$PATRONI_NODE_IP"
    if [[ "$_o1" -gt 255 || "$_o2" -gt 255 || "$_o3" -gt 255 || "$_o4" -gt 255 ]]; then
        log_error "Invalid IP address (octet out of range 0-255): $PATRONI_NODE_IP"
        exit 1
    fi

    # etcd cluster configuration
    echo ""
    echo -e "${WHITE}etcd Cluster Configuration${NC}"
    echo "For a 3-node HA setup, you need 3 etcd nodes."
    echo "Enter comma-separated list of all etcd node IPs (including this one)."
    echo ""
    read -p "etcd node IPs (e.g., 192.168.1.10,192.168.1.11,192.168.1.12): " ETCD_NODE_IPS

    if [[ -z "$ETCD_NODE_IPS" ]]; then
        ETCD_NODE_IPS="$PATRONI_NODE_IP"
    fi

    # Build etcd cluster peers string
    # IMPORTANT: Node names must match ETCD_NAME passed to configure_etcd (line 550).
    # The local node uses "etcd-${PATRONI_NODE_NAME}", peers use "etcd-nodeN".
    ETCD_CLUSTER_PEERS=""
    ETCD_HOSTS=""
    ETCD_LOCAL_NAME="etcd-${PATRONI_NODE_NAME}"
    local i=1
    IFS=',' read -ra IPS <<< "$ETCD_NODE_IPS"
    for ip in "${IPS[@]}"; do
        ip=$(echo "$ip" | tr -d ' ')
        # Validate each IP address format and octet ranges
        if [[ ! "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
            log_error "Invalid etcd node IP: $ip"
            exit 1
        fi
        IFS='.' read -r _o1 _o2 _o3 _o4 <<< "$ip"
        if [[ "$_o1" -gt 255 || "$_o2" -gt 255 || "$_o3" -gt 255 || "$_o4" -gt 255 ]]; then
            log_error "Invalid etcd node IP (octet out of range 0-255): $ip"
            exit 1
        fi
        if [[ -n "$ETCD_CLUSTER_PEERS" ]]; then
            ETCD_CLUSTER_PEERS+=","
            ETCD_HOSTS+=","
        fi
        # Use the configured node name for the local IP, generic names for peers
        if [[ "$ip" == "$PATRONI_NODE_IP" ]]; then
            ETCD_CLUSTER_PEERS+="${ETCD_LOCAL_NAME}=http://${ip}:2380"
        else
            ETCD_CLUSTER_PEERS+="etcd-node${i}=http://${ip}:2380"
        fi
        ETCD_HOSTS+="${ip}:2379"
        i=$((i + 1))
    done

    # Database passwords
    echo ""
    echo -e "${WHITE}Database Credentials${NC}"

    # Try to read from existing config (safe parsing — no source as root)
    if [[ -f "/etc/spiralpool/ha.env" ]] && [[ ! -L "/etc/spiralpool/ha.env" ]]; then
        while IFS='=' read -r key value; do
            # Skip comments and empty lines
            [[ "$key" =~ ^[[:space:]]*# ]] && continue
            [[ -z "$key" ]] && continue
            key=$(echo "$key" | tr -d '[:space:]')
            # Only extract known password variables
            case "$key" in
                DB_PASSWORD|REPLICATION_PASSWORD|REDIS_PASSWORD)
                    # Strip surrounding quotes
                    value=$(echo "$value" | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
                    declare -g "$key=$value"
                    ;;
                SPIRAL_REPLICATION_PASSWORD)
                    # install.sh writes this name — map to our internal variable
                    value=$(echo "$value" | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
                    REPLICATION_PASSWORD="$value"
                    ;;
            esac
        done < /etc/spiralpool/ha.env
    fi

    if [[ -z "${DB_PASSWORD:-}" ]]; then
        read -sp "PostgreSQL superuser password: " DB_PASSWORD
        echo ""
    else
        log "Using existing DB password from ha.env"
    fi

    if [[ -z "${REPLICATION_PASSWORD:-}" ]]; then
        read -sp "Replication user password: " REPLICATION_PASSWORD
        echo ""
    else
        log "Using existing replication password from ha.env"
    fi

    # Validate passwords don't contain YAML/shell-breaking characters
    # These go into patroni.yml inside double-quoted YAML strings
    for pw_name in DB_PASSWORD REPLICATION_PASSWORD; do
        local pw_val="${!pw_name}"
        if [[ -z "$pw_val" ]]; then
            log_error "${pw_name} cannot be empty"
            exit 1
        fi
        if [[ "$pw_val" =~ [\"\\\$\`] ]] || [[ "$pw_val" == *$'\n'* ]]; then
            log_error "${pw_name} contains unsafe characters (\", \\, \$, \`, newline). Please use alphanumeric + safe special chars."
            exit 1
        fi
    done

    echo ""
}

show_summary() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${WHITE}Configuration Summary${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  Node Name:       ${GREEN}$PATRONI_NODE_NAME${NC}"
    echo -e "  Node IP:         ${GREEN}$PATRONI_NODE_IP${NC}"
    echo -e "  etcd Cluster:    ${GREEN}$ETCD_CLUSTER_PEERS${NC}"
    echo -e "  etcd Hosts:      ${GREEN}$ETCD_HOSTS${NC}"
    echo ""
    echo -e "${YELLOW}WARNING: This will stop the standalone PostgreSQL service${NC}"
    echo -e "${YELLOW}         and migrate to Patroni management.${NC}"
    echo ""
    read -p "Proceed with installation? [y/N]: " confirm
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        log "Installation cancelled"
        exit 0
    fi
}

main() {
    print_banner
    check_root
    check_prerequisites
    collect_configuration
    show_summary

    echo ""
    log "Starting Patroni installation..."
    echo ""

    install_etcd
    configure_etcd "etcd-${PATRONI_NODE_NAME}" "$PATRONI_NODE_IP" "$ETCD_CLUSTER_PEERS"

    install_patroni
    configure_patroni "$PATRONI_NODE_NAME" "$PATRONI_NODE_IP" "$ETCD_HOSTS" "$DB_PASSWORD" "$REPLICATION_PASSWORD"
    create_patroni_service

    migrate_to_patroni

    echo ""
    echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${WHITE}Patroni Installation Complete!${NC}"
    echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  ${WHITE}Status Commands:${NC}"
    echo -e "    ${CYAN}patronictl -c /etc/patroni/patroni.yml list${NC}        - Show cluster status"
    echo -e "    ${CYAN}curl http://localhost:8008/leader${NC}                  - Check if this node is leader"
    echo -e "    ${CYAN}curl http://localhost:8008/health${NC}                  - Health check"
    echo ""
    echo -e "  ${WHITE}Failover Commands:${NC}"
    echo -e "    ${CYAN}patronictl -c /etc/patroni/patroni.yml switchover${NC}  - Planned switchover"
    echo -e "    ${CYAN}patronictl -c /etc/patroni/patroni.yml failover${NC}    - Force failover"
    echo ""
    echo -e "  ${WHITE}Log Commands:${NC}"
    echo -e "    ${CYAN}journalctl -u patroni -f${NC}                           - Follow Patroni logs"
    echo -e "    ${CYAN}journalctl -u etcd -f${NC}                              - Follow etcd logs"
    echo ""
    echo -e "  ${YELLOW}IMPORTANT: Run this script on ALL nodes in the HA cluster.${NC}"
    echo ""
}

main "$@"
