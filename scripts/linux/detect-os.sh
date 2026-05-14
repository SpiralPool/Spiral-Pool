#!/usr/bin/env bash
# OS detection and abstraction layer for Spiral Pool.
# MUST be sourced, not executed directly.
[[ "${BASH_SOURCE[0]}" == "${0}" ]] && {
    echo "ERROR: detect-os.sh must be sourced, not executed directly." >&2
    echo "Usage: source \"\$(dirname \"\$0\")/detect-os.sh\"" >&2
    exit 1
}

# Already sourced guard
[[ -n "${_DETECT_OS_LOADED:-}" ]] && return 0
_DETECT_OS_LOADED=1

# ---------------------------------------------------------------------------
# Internal: read a single field from /etc/os-release without sourcing it.
# Avoids polluting the caller's variable space with all OS-release variables.
# ---------------------------------------------------------------------------
_os_field() {
    local key="$1"
    grep -oP "^${key}=\K.*" /etc/os-release 2>/dev/null | tr -d '"'
}

# ---------------------------------------------------------------------------
# Populate exported OS variables
# ---------------------------------------------------------------------------
if [[ ! -f /etc/os-release ]]; then
    OS_ID=""
    OS_VERSION=""
    OS_CODENAME=""
    OS_PRETTY_NAME="Unknown (no /etc/os-release)"
    DOCKER_DISTRO="unknown"
    UNATTENDED_UPGRADES_EXTRA_ORIGINS=""
else
    OS_ID="$(_os_field ID)"
    OS_VERSION="$(_os_field VERSION_ID)"
    OS_PRETTY_NAME="$(_os_field PRETTY_NAME)"

    # Prefer VERSION_CODENAME (present on Debian 13 and Ubuntu 22.04+).
    # Fall back to UBUNTU_CODENAME for older Ubuntu (20.04 and earlier used that field).
    OS_CODENAME="$(_os_field VERSION_CODENAME)"
    if [[ -z "$OS_CODENAME" ]]; then
        OS_CODENAME="$(_os_field UBUNTU_CODENAME)"
    fi

    # DOCKER_DISTRO is used in apt repo URLs: linux/ubuntu vs linux/debian
    case "$OS_ID" in
        ubuntu)  DOCKER_DISTRO="ubuntu" ;;
        debian)  DOCKER_DISTRO="debian" ;;
        *)       DOCKER_DISTRO="$OS_ID" ;;
    esac

    # ESM/Ubuntu Pro apt origins — set only on Ubuntu, empty on Debian.
    # Used by setup_auto_updates() so Debian installs don't get Ubuntu-specific
    # origins that would confuse operators or produce log noise.
    if [[ "$OS_ID" == "ubuntu" ]]; then
        UNATTENDED_UPGRADES_EXTRA_ORIGINS='        "${distro_id}ESMApps:${distro_codename}-apps-security";
        "${distro_id}ESM:${distro_codename}-infra-security";'
    else
        UNATTENDED_UPGRADES_EXTRA_ORIGINS=""
    fi
fi

export OS_ID OS_VERSION OS_CODENAME OS_PRETTY_NAME DOCKER_DISTRO UNATTENDED_UPGRADES_EXTRA_ORIGINS

# ---------------------------------------------------------------------------
# Helper predicates
# ---------------------------------------------------------------------------
is_ubuntu()    { [[ "$OS_ID" == "ubuntu" ]]; }
is_debian()    { [[ "$OS_ID" == "debian" ]]; }
is_debian_13() { [[ "$OS_ID" == "debian" && "$OS_VERSION" == "13" ]]; }

# ---------------------------------------------------------------------------
# Gate: exit 1 with a clear message if the OS is not supported.
# Supported: Ubuntu 24.04, Ubuntu 26.04, Debian 13 (Trixie)
# ---------------------------------------------------------------------------
require_supported_os() {
    if is_ubuntu; then
        case "$OS_VERSION" in
            24.04|26.04) return 0 ;;
            *)
                echo "ERROR: Ubuntu ${OS_VERSION} is not supported." >&2
                echo "       Supported Ubuntu versions: 24.04 LTS, 26.04 LTS" >&2
                exit 1
                ;;
        esac
    elif is_debian_13; then
        return 0
    elif is_debian; then
        echo "ERROR: Debian ${OS_VERSION} is not supported." >&2
        echo "       Supported Debian versions: 13 (Trixie)" >&2
        exit 1
    else
        echo "ERROR: Unsupported operating system: ${OS_PRETTY_NAME:-$OS_ID}" >&2
        echo "       Supported platforms: Ubuntu 24.04 LTS, Ubuntu 26.04 LTS, Debian 13 (Trixie)" >&2
        exit 1
    fi
}
