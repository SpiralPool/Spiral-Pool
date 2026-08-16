#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool — Coin Config Generation and Upgrade-Survival Tests
# =============================================================================
# tests/test-coin-configs.sh downloads each real daemon and starts it. That is
# the authoritative check, but it needs Linux, root, network, and several GB of
# downloads. This suite answers a narrower question without any of that:
#
#   1. Does each generator emit a config the daemon can actually parse?
#   2. Is that config STILL valid after an upgrade has rewritten it?
#
# Question 2 is the one that has bitten. The generators were reviewed carefully;
# the functions that mutate a config in place during `upgrade.sh` were not, and
# one of them was found prepending a new value onto the old one
# (dbcache=16384 -> dbcache=409616384) while logging that the cap had been
# applied. A config can be perfect at install time and unparseable two upgrades
# later, so both ends are checked here.
#
# The configs are rendered from the REAL heredocs in the shipping scripts, and
# mutated with the REAL sed commands, so this fails if either side changes.
#
# What is checked on every rendered config:
#   - every non-blank, non-comment line is key=value with a non-empty key
#   - no option is left with an empty value
#   - no unexpanded $VAR or ${VAR} survived into the file
#   - txindex=1 never coexists with prune>0 (fatal to Bitcoin Core at startup)
#   - no scalar option is assigned twice (list-valued options are exempt)
#
# Usage: bash tests/test_coin_config_upgrade.sh
# Exit:  0 all passed, 1 otherwise
# =============================================================================

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALL="$PROJECT_ROOT/install.sh"
POOL_MODE="$PROJECT_ROOT/scripts/linux/pool-mode.sh"
UPGRADE="$PROJECT_ROOT/upgrade.sh"

TESTS_RUN=0; TESTS_PASSED=0; TESTS_FAILED=0
RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; NC='\033[0m'

log_test() { echo -e "${CYAN}[TEST]${NC} $1"; }
pass() { TESTS_RUN=$((TESTS_RUN+1)); TESTS_PASSED=$((TESTS_PASSED+1)); echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { TESTS_RUN=$((TESTS_RUN+1)); TESTS_FAILED=$((TESTS_FAILED+1)); echo -e "  ${RED}FAIL${NC}: $1"; [[ -n "${2:-}" ]] && echo -e "        $2"; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Options Bitcoin Core (and its forks) accept more than once.
LIST_OPTS="addnode seednode connect onlynet rpcallowip rpcauth bind whitebind
           whitelist externalip zmqpubhashblock zmqpubhashtx zmqpubrawblock
           zmqpubrawtx debug uacomment onion"

# -----------------------------------------------------------------------------
# Extract the Nth `cat > "<something>" << EOF` heredoc body from a script.
# -----------------------------------------------------------------------------
# Matches both writer styles the scripts use: `cat > "path" << EOF` and
# `sudo tee "path" > /dev/null << EOF`. The bare-metal configs — the ones a
# non-Docker install actually runs on — use the tee form, so matching only `cat`
# silently skipped them.
extract_heredoc() {
    local file="$1" marker="$2"
    awk -v marker="$marker" '
        index($0, marker) && /<< *EOF/ { inside=1; next }
        inside && /^EOF$/ { exit }
        inside { print }
    ' "$file"
}

# Render a heredoc body with the shipping scripts variables stubbed.
render() {
    local body="$1" prune="$2" tor="$3"
    (
        set +u
        # Values are placeholders; what matters is the STRUCTURE they produce.
        SPIRALPOOL_DIR=/spiralpool; INSTALL_DIR=/spiralpool; CONFIG_DIR=/spiralpool/config
        BTC_DIR=/spiralpool/btc;  BTC_DATA=/spiralpool/btc
        DGB_DIR=/spiralpool/dgb;  DGB_DATA=/spiralpool/dgb
        LTC_DIR=/spiralpool/ltc;  LTC_DATA=/spiralpool/ltc
        rpc_user=spiraltest; rpc_pass=deadbeefcafe1234
        BTC_RPC_USER=spiralbtc; BTC_RPC_PASSWORD=deadbeefcafe1234
        DGB_RPC_USER=spiraldgb; DGB_RPC_PASSWORD=deadbeefcafe1234
        LTC_RPC_USER=spiralltc; LTC_RPC_PASSWORD=deadbeefcafe1234
        POOL_USER=spiralpool
        EXISTING_PRUNE="$prune"
        PRUNE_ENABLED=$([[ "$prune" != "0" ]] && echo true || echo false)
        if [[ "$prune" != "0" ]]; then PRUNE_CONF_TXINDEX=""; PRUNE_CONF_PRUNE="prune=$prune"
        else PRUNE_CONF_TXINDEX="txindex=1"; PRUNE_CONF_PRUNE="prune=0"; fi
        TOR_ENABLED="$tor"; BTC_TOR_ENABLED="$tor"; DGB_TOR_ENABLED="$tor"; LTC_TOR_ENABLED="$tor"
        BTC_NETWORK_CONFIG="# network"; DGB_NETWORK_CONFIG="# network"; LTC_NETWORK_CONFIG="# network"
        BTC_DBCACHE=4096; DGB_DBCACHE=2048; LTC_DBCACHE=1024
        BCH_DBCACHE=2048; BTCS_DBCACHE=1024
        BTC_MEM_HIGH=8G; BTC_MEM_MAX=12G
        BCH_RPC_USER=spiralbch; BCH_RPC_PASSWORD=deadbeefcafe1234
        BCH_TOR_ENABLED="$tor"; BCH_NETWORK_CONFIG="# network"
        BTCS_DATA=/spiralpool/btcs; BTCS_RPC_USER=spiralbtcs
        BTCS_RPC_PASSWORD=deadbeefcafe1234
        BTCS_RPC_PORT=10567; BTCS_P2P_PORT=10566; BTCS_ZMQ_PORT=28567
        # The DGB bare-metal heredoc uses generic names rather than DGB_-prefixed
        # ones for these three.
        RPC_PORT=14022; ZMQ_PORT=28532; NETWORK_CONFIG="# network"
        eval "cat <<SPIRALEOF
$body
SPIRALEOF"
    ) 2>/dev/null
}

# -----------------------------------------------------------------------------
# The validator. Every rule here is a way a daemon refuses to start.
# -----------------------------------------------------------------------------
# $3: "template" when ${PLACEHOLDER} tokens are expected rather than a defect.
# docker/config/bitcoin.conf.template is substituted by the container entrypoint
# at first start, so unexpanded tokens are the point of the file.
validate_config() {
    local file="$1" label="$2" kind="${3:-rendered}" problems=""

    # Strip comments and blanks; everything left must be an assignment.
    local bad
    bad=$(grep -vE '^[[:space:]]*(#|$)' "$file" | grep -vE '^[[:space:]]*\[' \
          | grep -vE '^[[:space:]]*[A-Za-z0-9_.]+[[:space:]]*=' || true)
    [[ -n "$bad" ]] && problems+="not key=value: $(echo "$bad" | tr '\n' '|') "

    # An option with no value. Core rejects some outright and misreads others.
    local empty
    empty=$(grep -E '^[[:space:]]*[A-Za-z0-9_.]+[[:space:]]*=[[:space:]]*$' "$file" || true)
    [[ -n "$empty" ]] && problems+="empty value: $(echo "$empty" | tr '\n' '|') "

    # A variable that never got substituted would be written literally.
    #
    # Skipped for templates: docker/config/bitcoin.conf.template is substituted
    # by the container entrypoint at first start, so ${RPC_USER} placeholders
    # are the point of the file. What matters there is that every placeholder is
    # brace-delimited — a bare $RPC_USER is not substituted by the entrypoint
    # and would reach the daemon verbatim.
    local unexpanded
    if [[ "$kind" == "template" ]]; then
        unexpanded=$(grep -nE '\$[A-Za-z_]' "$file" || true)
        [[ -n "$unexpanded" ]] && problems+="bare \$VAR in template, needs \${VAR}: $(echo "$unexpanded" | head -2 | tr '\n' '|') "
    else
        unexpanded=$(grep -nE '\$\{?[A-Za-z_]' "$file" || true)
        [[ -n "$unexpanded" ]] && problems+="unexpanded var: $(echo "$unexpanded" | head -2 | tr '\n' '|') "
    fi

    # Fatal at startup: "Prune mode is incompatible with -txindex."
    local pv
    pv=$(grep -oP '^[[:space:]]*prune[[:space:]]*=[[:space:]]*\K[0-9]+' "$file" | head -1 || true)
    if [[ -n "$pv" && "$pv" != "0" ]] && grep -qE '^[[:space:]]*txindex[[:space:]]*=[[:space:]]*1' "$file"; then
        problems+="prune=$pv together with txindex=1 (daemon will not start) "
    fi

    # A scalar assigned twice is at best confusing and at worst wrong, since
    # Core takes the FIRST occurrence in a config file, not the last.
    local dupes
    dupes=$(grep -oE '^[[:space:]]*[A-Za-z0-9_.]+[[:space:]]*=' "$file" \
            | tr -d '[:space:]=' | sort | uniq -d || true)
    local d
    for d in $dupes; do
        grep -qw -- "$d" <<< "$LIST_OPTS" || problems+="duplicate scalar option: $d "
    done

    if [[ -z "$problems" ]]; then
        pass "$label"
    else
        fail "$label" "$problems"
    fi
}

# =============================================================================
# 1. What the generators actually emit
# =============================================================================
log_test "generated configs are parseable"

declare -a CASES=(
    "$POOL_MODE|btc/bitcoin.conf|pool-mode BTC"
    "$POOL_MODE|dgb/digibyte.conf|pool-mode DGB"
    "$POOL_MODE|bch/bitcoin.conf|pool-mode BCH"
    "$POOL_MODE|ltc/litecoin.conf|pool-mode LTC"
    "$POOL_MODE|xec/bitcoin.conf|pool-mode XEC"
    "$INSTALL|CONFIG_DIR/bitcoin.conf|install.sh docker BTC"
    "$INSTALL|CONFIG_DIR/digibyte.conf|install.sh docker DGB"
    "$INSTALL|CONFIG_DIR/litecoin.conf|install.sh docker LTC"
    # Bare-metal writers. These are what a non-Docker install actually runs on,
    # and they use `sudo tee` rather than `cat >`.
    "$INSTALL|BTC_DATA/bitcoin.conf|install.sh bare-metal BTC"
    "$INSTALL|DGB_DIR/digibyte.conf|install.sh bare-metal DGB"
    "$INSTALL|BCH_DATA/bitcoin.conf|install.sh bare-metal BCH"
    "$INSTALL|BTCS_DATA/bitcoinsilver.conf|install.sh bare-metal BTCS"
)

for case in "${CASES[@]}"; do
    IFS='|' read -r src marker label <<< "$case"
    body="$(extract_heredoc "$src" "$marker")"
    if [[ -z "$body" ]]; then
        fail "$label: heredoc found" "marker '$marker' no longer matches — generator moved or renamed"
        continue
    fi
    for prune in 0 5000; do
        state=$([[ "$prune" == "0" ]] && echo "full" || echo "pruned")
        render "$body" "$prune" false > "$WORK/gen.conf"
        validate_config "$WORK/gen.conf" "$label ($state)"
    done
done

# The Docker template ships as a literal file, not a heredoc.
if [[ -f "$PROJECT_ROOT/docker/config/bitcoin.conf.template" ]]; then
    validate_config "$PROJECT_ROOT/docker/config/bitcoin.conf.template" "docker bitcoin.conf.template" template
fi

# =============================================================================
# 2. Do those configs survive an upgrade?
#
# upgrade.sh rewrites daemon configs in place on every run. A config that is
# valid at install time must still be valid afterwards — and after a SECOND
# upgrade, since the compounding failure only showed up on repeat runs.
# =============================================================================
log_test "configs remain valid after upgrade.sh rewrites them"

extract_fn() {
    awk -v fn="$2" '
        !inside { if ($0 ~ "^[[:space:]]*" fn "\\(\\) \\{") {
            match($0,/^[[:space:]]*/); indent=substr($0,1,RLENGTH); inside=1; print } next }
        { print }
        $0 == indent "}" { exit }
    ' "$1"
}

# Pull the real config mutators out of upgrade.sh.
SET_BODY="$(extract_fn "$UPGRADE" _conf_set_int)"
AT_BODY="$(extract_fn "$UPGRADE" _add_toplevel)"
MS_BODY="$(extract_fn "$UPGRADE" _mainnet_scope)"

if [[ -z "$SET_BODY" || -z "$AT_BODY" || -z "$MS_BODY" ]]; then
    fail "upgrade mutators extracted" "_conf_set_int/_add_toplevel/_mainnet_scope no longer match — update this test"
else
    for case in "${CASES[@]}"; do
        IFS='|' read -r src marker label <<< "$case"
        body="$(extract_heredoc "$src" "$marker")"
        [[ -z "$body" ]] && continue

        for prune in 0 5000; do
            state=$([[ "$prune" == "0" ]] && echo "full" || echo "pruned")
            render "$body" "$prune" false > "$WORK/up.conf"

            # Two upgrade runs, because the value-prepending failure only
            # became visible on the second.
            for _ in 1 2; do
                bash -c "
                    set -u
                    conf_path='$WORK/up.conf'
                    max_cache=4096
                    $SET_BODY
                    _conf_set_int dbcache 4096 || true
                    _conf_set_int maxconnections 64 || true
                    $MS_BODY
                    $AT_BODY
                    _mainnet_scope | sed 's/^[[:space:]]*//' | grep -qxF -- 'addnode=1.2.3.4:8333' \
                        || _add_toplevel 'addnode=1.2.3.4:8333'
                " >/dev/null 2>&1
            done

            validate_config "$WORK/up.conf" "$label ($state) after 2 upgrades"

            # The cap must have actually landed, not been prepended.
            cur=$(grep -oP '^[[:space:]]*dbcache[[:space:]]*=[[:space:]]*\K[0-9]+' "$WORK/up.conf" | head -1 || true)
            if [[ -n "$cur" && "$cur" -gt 4096 ]]; then
                fail "$label ($state): dbcache capped" "value is ${cur}, above the 4096 cap"
            fi
        done
    done
fi

# =============================================================================
# 3. The pruned/full pair is mutually exclusive in every generator.
#
# Core treats prune>0 with txindex=1 as fatal, and this pairing is produced by
# three different mechanisms across the scripts (PRUNE_CONF_* pair, a $(if …)
# conditional, and plain omission), so it is worth asserting directly.
# =============================================================================
log_test "prune and txindex are never both enabled"

for case in "${CASES[@]}"; do
    IFS='|' read -r src marker label <<< "$case"
    body="$(extract_heredoc "$src" "$marker")"
    [[ -z "$body" ]] && continue
    render "$body" 5000 false > "$WORK/p.conf"
    if grep -qE '^[[:space:]]*txindex[[:space:]]*=[[:space:]]*1' "$WORK/p.conf"; then
        fail "$label: pruned config omits txindex" "txindex=1 present with prune=5000"
    else
        pass "$label: pruned config omits txindex"
    fi
done

# =============================================================================
# coin-upgrade.sh: get_installed_version must never let the cache hide the binary
# =============================================================================
# This is the mechanism behind the whole v2.7.0 release, reproduced inside the
# remediation tool: the function returned config/coin-versions/<COIN>.ver
# unconditionally and never asked the daemon. With BTC.ver=31.1 and a Bitcoin
# Knots binary restored on disk — reachable by following the restore command
# coin-upgrade.sh itself prints on failure — `--check` reported "current" while
# the node kept following the BIP-110 minority chain.
#
# The opposite error is equally real: BTCS is pinned to a SOURCE COMMIT
# ("source-<40 hex>"), which a compiled binary cannot report from --version, so
# for that pin the cache IS the record and asking the binary would report
# "update available" forever, immediately after a successful upgrade.
#
# Both directions are asserted here because fixing either one alone re-breaks
# the other.
log_test "coin-upgrade.sh get_installed_version — binary vs version cache"

CUV="$PROJECT_ROOT/coin-upgrade.sh"
GIV_BODY="$(awk '/^get_installed_version\(\) \{/,/^\}/' "$CUV")"

if [[ -z "$GIV_BODY" ]]; then
    fail "get_installed_version is extractable" "renamed or removed from coin-upgrade.sh"
else
    GW="$(mktemp -d)"
    mkdir -p "$GW/cache" "$GW/bin"
    printf '#!/bin/bash\necho "Bitcoin Knots Daemon version v29.3.knots20260508"\n' > "$GW/bin/knots"
    printf '#!/bin/bash\necho "Bitcoin Core Daemon version v31.1.0"\n'             > "$GW/bin/core"
    printf '#!/bin/bash\necho "Bitcoin Silver Daemon version v1.0.2"\n'            > "$GW/bin/btcs"
    printf '#!/bin/bash\nexit 127\n'                                              > "$GW/bin/mute"
    chmod +x "$GW/bin/"*
    echo "31.1" > "$GW/cache/BTC.ver"
    echo "source-ff5c3c3d381fa3c783862768d5a2e4fbb50f0931" > "$GW/cache/BTCS.ver"

    printf '%s\n' "$GIV_BODY" > "$GW/fn.sh"
    cat > "$GW/run.sh" <<'GIVEOF'
declare -A COIN_TARGET=([BTC]="31.1" [BTCS]="source-ff5c3c3d381fa3c783862768d5a2e4fbb50f0931")
VERSION_CACHE_DIR="$GW/cache"
get_binary_path(){ echo "$BIN"; }
source "$GW/fn.sh"
get_installed_version "$COIN"
GIVEOF
    giv() { GW="$GW" BIN="$2" COIN="$1" bash "$GW/run.sh" 2>/dev/null; }
    # This suite uses pass/fail, not assert_eq. Using the wrong helper here
    # printed "command not found" four times and STILL reported 61/61 passing,
    # because an unknown command increments no counter — so define the
    # comparison in terms of the helpers the suite actually has.
    giv_eq() { # <want> <got> <label>
        if [[ "$1" == "$2" ]]; then pass "$3"; else fail "$3" "got [$2] want [$1]"; fi
    }

    giv_eq "29.3.knots20260508" "$(giv BTC "$GW/bin/knots")" \
        "a Knots binary is reported as Knots even when the cache says 31.1"
    giv_eq "31.1.0" "$(giv BTC "$GW/bin/core")" \
        "a Core binary is reported from the binary, not the cache"
    giv_eq "source-ff5c3c3d381fa3c783862768d5a2e4fbb50f0931" "$(giv BTCS "$GW/bin/btcs")" \
        "a source-commit pin still reads from the cache (no permanent 'update available')"
    giv_eq "31.1" "$(giv BTC "$GW/bin/mute")" \
        "the cache is still the fallback when the binary prints nothing"

    rm -rf "$GW"
fi

echo ""
echo "═══════════════════════════════════════════════════════════"
echo -e "  Run: ${TESTS_RUN}   ${GREEN}Passed: ${TESTS_PASSED}${NC}   ${RED}Failed: ${TESTS_FAILED}${NC}"
echo "═══════════════════════════════════════════════════════════"
[[ $TESTS_FAILED -eq 0 ]] || exit 1
exit 0
