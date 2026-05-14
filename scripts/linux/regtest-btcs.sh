#!/usr/bin/env bash

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# =============================================================================
# Spiral Stratum — Bitcoin Silver (BTCS) Regtest Integration Test
# =============================================================================
#
# WHAT THIS DOES:
#   Tests the FULL block lifecycle end-to-end against a real Bitcoin Silver
#   daemon running in regtest mode (private local blockchain, difficulty=1).
#
#   BTCS specifics:
#   - Daemon: bitcoinsilverd
#   - CLI:    bitcoinsilver-cli
#   - Address: getnewaddress "pool-btcs" "bech32" returns bs1q...
#   - BTC-style consensus: SegWit+Taproot from block 0
#   - Block time: 300s (5 minutes) — faster than BTC/BCH/BCH2
#   - Coinbase maturity: 200 blocks (vs Bitcoin's 100)
#   - Config: config/regtest/config-btcs-regtest.yaml
#
# PREREQUISITES:
#   1. Bitcoin Silver installed (build from source — no binary releases)
#      - Source: https://github.com/bitcoin-silver/core (commit ff5c3c3d)
#      - install.sh builds this automatically via install_bitcoinsilver()
#   2. cpuminer with SHA256d support (minerd / cpuminer-multi)
#   3. PostgreSQL running locally (install.sh sets this up)
#   4. Pool binary built
#
# IMPORTANT: BTCS coinbase maturity is 200 blocks (not 100 like Bitcoin).
#   The pool's payment processor uses the coin's CoinbaseMaturity() value
#   automatically — no manual config needed.
#
# USAGE:
#   chmod +x scripts/linux/regtest-btcs.sh
#   ./scripts/linux/regtest-btcs.sh
#
#   # Custom binary paths:
#   BITCOINSILVERD=/usr/local/bin/bitcoinsilverd \
#   BITCOINSILVERCLI=/usr/local/bin/bitcoinsilver-cli \
#     ./scripts/linux/regtest-btcs.sh
#
# This is a convenience wrapper that delegates to regtest.sh btcs.
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Export BTCS-specific binary overrides if set
if [[ -n "${BITCOINSILVERD:-}" ]]; then
    export BITCOINSILVERD
fi
if [[ -n "${BITCOINSILVERCLI:-}" ]]; then
    export BITCOINSILVERCLI
fi

# Delegate to the generic regtest runner
exec "$SCRIPT_DIR/regtest.sh" btcs "$@"
