#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# =============================================================================
# Spiral Stratum — Bitcoin Cash II (BCH2) Regtest Integration Test
# =============================================================================
#
# WHAT THIS DOES:
#   Tests the FULL block lifecycle end-to-end against a real Bitcoin Cash II
#   daemon running in regtest mode (private local blockchain, difficulty=1).
#
#   BCH2 specifics:
#   - Daemon: bitcoincashiid (lowercase symlink) / bitcoincashIId (binary)
#   - CLI:    bitcoincashii-cli (lowercase symlink)
#   - CashAddr format: getnewaddress "pool-bch2" "legacy" returns bitcoincashii:q...
#   - BCH consensus: SIGHASH_FORKID, ASERT DAA, no SegWit
#   - Config: config/regtest/config-bch2-regtest.yaml
#
# PREREQUISITES:
#   1. Bitcoin Cash II Core installed (bitcoincashiid + bitcoincashii-cli in PATH)
#      - Build from: https://github.com/BitcoincashII/bitcoincashII-core
#      - v27.0.2: bitcoincashII-v27.0.2-linux-x86_64.tar.gz
#   2. cpuminer with SHA256d support (minerd / cpuminer-multi)
#   3. PostgreSQL running locally (install.sh sets this up)
#   4. Pool binary built
#
# USAGE:
#   chmod +x scripts/linux/regtest-bch2.sh
#   ./scripts/linux/regtest-bch2.sh
#
#   # Custom binary paths:
#   BITCOINCASHIID=/opt/spiralpool/bch2/bin/bitcoincashIId \
#   BITCOINCASHIICLI=/opt/spiralpool/bch2/bin/bitcoincashII-cli \
#     ./scripts/linux/regtest-bch2.sh
#
# This is a convenience wrapper that delegates to regtest.sh bch2.
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Export BCH2-specific binary overrides if set
if [[ -n "${BITCOINCASHIID:-}" ]]; then
    export BITCOINCASHIID
fi
if [[ -n "${BITCOINCASHIICLI:-}" ]]; then
    export BITCOINCASHIICLI
fi

# Delegate to the generic regtest runner
exec "$SCRIPT_DIR/regtest.sh" bch2 "$@"
