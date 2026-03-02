#!/bin/bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#
# Update all spiralpool-* commands from the fixed install.sh
# Usage: sudo bash update-commands.sh [path-to-install.sh]
#

set -euo pipefail

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

SRC="${1:-./install.sh}"

if [[ ! -f "$SRC" ]]; then
    echo -e "${RED}Error: install.sh not found at: $SRC${NC}"
    echo "Usage: sudo bash update-commands.sh [path-to-install.sh]"
    exit 1
fi

# SECURITY: Reject symlinks (prevents redirection to malicious files)
if [[ -L "$SRC" ]]; then
    echo -e "${RED}Error: Source file is a symlink — refusing for security${NC}"
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    echo -e "${RED}Error: Must run as root (sudo)${NC}"
    echo "Usage: sudo bash update-commands.sh"
    exit 1
fi

extract_and_install() {
    local cmd="$1"
    local marker="$2"
    local target="/usr/local/bin/${cmd}"

    # Extract content between the heredoc markers
    # Find: sudo tee .../cmd ... << 'MARKER' ... content ... MARKER
    local content
    content=$(sed -n "/sudo tee.*${cmd}.*${marker}/,/^${marker}\$/{
        /sudo tee.*${marker}/d
        /^${marker}\$/d
        p
    }" "$SRC")

    if [[ -z "$content" ]]; then
        echo -e "  ${YELLOW}[SKIP]${NC} ${cmd} — not found in install.sh"
        return 1
    fi

    # SECURITY: Atomic write — prevents partially-written executables on interruption
    local temp_target
    temp_target=$(mktemp "${target}.XXXXXX")
    printf '%s\n' "$content" > "$temp_target"
    chmod 755 "$temp_target"
    mv -f "$temp_target" "$target"
    echo -e "  ${GREEN}[OK]${NC}   ${cmd}"
}

echo ""
echo -e "${GREEN}╔════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║          UPDATING SPIRALPOOL COMMANDS                          ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo "  Source: $SRC"
echo ""

PASS=0
FAIL=0

for pair in \
    "spiralpool-status:STATUSEOF" \
    "spiralpool-logs:LOGSEOF" \
    "spiralpool-restart:RESTARTEOF" \
    "spiralpool-sync:SYNCSTATUSEOF" \
    "spiralpool-wallet:WALLETEOF" \
    "spiralpool-backup:BACKUPEOF" \
    "spiralpool-restore:RESTOREEOF" \
    "spiralpool-pause:PAUSEEOF" \
    "spiralpool-stats:STATSEOF" \
    "spiralpool-blocks:BLOCKSEOF" \
    "spiralpool-test:TESTEOF" \
    "spiralpool-config:CONFIGEOF" \
    "spiralpool-scan:SCANEOF" \
    "spiralpool-watch:WATCHEOF" \
    "spiralpool-export:EXPORTEOF"
do
    cmd="${pair%%:*}"
    marker="${pair##*:}"
    if extract_and_install "$cmd" "$marker"; then
        ((++PASS))
    else
        ((++FAIL))
    fi
done

echo ""
echo -e "  Updated: ${GREEN}${PASS}${NC}  Skipped: ${YELLOW}${FAIL}${NC}"
echo ""
if [[ $FAIL -eq 0 ]]; then
    echo -e "  ${GREEN}Done! All commands updated.${NC}"
else
    echo -e "  ${YELLOW}Done. Some commands were not found in install.sh.${NC}"
fi
echo ""
