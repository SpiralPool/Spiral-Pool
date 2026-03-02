#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ============================================================================
# Spiral Dash - Universal Startup Script
# Automatically handles virtual environment setup on any Linux/macOS system
# ============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VENV_DIR="$SCRIPT_DIR/venv"
REQUIREMENTS="$SCRIPT_DIR/requirements.txt"
DASHBOARD="$SCRIPT_DIR/dashboard.py"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

print_banner() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "               ${GREEN}SPIRAL DASHBOARD${NC} - Mining Pool Web Interface"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

log_info() {
    echo -e "${GREEN}[+]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[!]${NC} $1"
}

log_error() {
    echo -e "${RED}[✗]${NC} $1"
}

# Find Python 3
find_python() {
    for cmd in python3 python; do
        if command -v "$cmd" &> /dev/null; then
            version=$("$cmd" --version 2>&1 | grep -oP '\d+\.\d+' | head -1)
            major=$(echo "$version" | cut -d. -f1)
            minor=$(echo "$version" | cut -d. -f2)
            if [ "$major" -ge 3 ] && [ "$minor" -ge 8 ]; then
                echo "$cmd"
                return 0
            fi
        fi
    done
    return 1
}

# Check for required system packages on Debian/Ubuntu
check_debian_deps() {
    if [ -f /etc/debian_version ]; then
        # Get Python version for version-specific venv package
        PY_VERSION=$($PYTHON --version 2>&1 | grep -oP '\d+\.\d+')
        VENV_PKG="python${PY_VERSION}-venv"

        if ! dpkg -l | grep -q "$VENV_PKG"; then
            log_warn "$VENV_PKG not installed."
            log_info "Run: sudo apt install $VENV_PKG"
            log_info "Then re-run this script."
            exit 1
        fi
    fi
}

# Main setup
setup_environment() {
    log_info "Checking Python installation..."

    PYTHON=$(find_python)
    if [ -z "$PYTHON" ]; then
        log_error "Python 3.8+ is required but not found."
        log_error "Please install Python 3.8 or later."
        exit 1
    fi

    log_info "Found Python: $($PYTHON --version)"

    # Check for venv module
    if ! $PYTHON -c "import venv" 2>/dev/null; then
        log_warn "Python venv module not available."
        check_debian_deps
    fi

    # Create virtual environment if it doesn't exist or is broken
    if [ ! -f "$VENV_DIR/bin/pip" ]; then
        # Clean up broken venv if exists
        if [ -d "$VENV_DIR" ]; then
            log_warn "Removing broken virtual environment..."
            rm -rf "$VENV_DIR"
        fi

        log_info "Creating virtual environment..."
        $PYTHON -m venv "$VENV_DIR"

        log_info "Upgrading pip..."
        "$VENV_DIR/bin/pip" install --upgrade pip --quiet

        log_info "Installing dependencies..."
        "$VENV_DIR/bin/pip" install -r "$REQUIREMENTS" --quiet

        log_info "Setup complete!"
        echo ""
    # Check if flask is installed
    elif ! "$VENV_DIR/bin/python" -c "import flask" 2>/dev/null; then
        log_info "Installing missing dependencies..."
        "$VENV_DIR/bin/pip" install -r "$REQUIREMENTS" --quiet
        log_info "Dependencies installed!"
        echo ""
    fi
}

# Run the dashboard
run_dashboard() {
    if [ ! -f "$DASHBOARD" ]; then
        log_error "dashboard.py not found at $DASHBOARD"
        exit 1
    fi

    log_info "Starting Spiral Dash on http://localhost:1618"
    echo ""

    # Run with gunicorn for production
    if [ -f "$VENV_DIR/bin/gunicorn" ]; then
        "$VENV_DIR/bin/gunicorn" --bind 0.0.0.0:1618 --workers 1 --threads 4 --access-logfile - --error-logfile - dashboard:app
    else
        # Fallback to Flask if gunicorn not available
        "$VENV_DIR/bin/python" "$DASHBOARD"
    fi
}

# Main
print_banner
setup_environment
run_dashboard
