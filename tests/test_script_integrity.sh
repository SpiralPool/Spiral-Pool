#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool — Script Integrity Guards
# =============================================================================
# These guards exist because of how bugs were actually introduced into this
# repo, not because of a style preference. Each check below corresponds to a
# real defect that shipped, passed `bash -n`, and was only caught by a later
# review pass.
#
#   1. CROSS-COIN VARIABLE LEAK
#      An edit meant for install_bitcoin() landed in install_digibyte(), where
#      $BTC_DIR is not defined ($BTC_DIR is `local` to install_bitcoin). The
#      line expanded to `sudo rm -f /bin/bitcoind /bin/bitcoin-cli` — running
#      as root, on a usrmerge distro, deleting the distro's own binaries.
#      Cause: the anchor text appears once per coin, and a first-match
#      replacement hit the wrong one.
#
#   2. QUOTED STRING BROKEN ACROSS LINES
#      `sed -e 's/<newline>$//'` — a literal newline inside the s/// program.
#      sed fails at runtime with "unterminated `s' command", 2>/dev/null hides
#      it, and the caller silently receives an empty value. This is VALID BASH
#      SYNTAX, so `bash -n` reports the file as clean. Two config parsers were
#      dead this way while their test suites passed.
#
#   3. UNDEFINED COLOUR/FORMAT VARIABLE
#      Message text referencing a variable that does not exist at that point
#      prints an empty string under `set -u`-less code, or aborts under it.
#
# The guards are deliberately cheap and specific. They are not a linter; they
# detect the three shapes that have actually cost us.
#
# Usage: bash tests/test_script_integrity.sh
# Exit:  0 all guards pass, 1 otherwise
# =============================================================================

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; NC='\033[0m'
RUN=0; PASSED=0; FAILED=0
log_test() { echo -e "${CYAN}[GUARD]${NC} $1"; }
pass() { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { RUN=$((RUN+1)); FAILED=$((FAILED+1)); echo -e "  ${RED}FAIL${NC}: $1"; [[ -n "${2:-}" ]] && echo -e "$2"; }

SHELL_FILES=(
    "$ROOT/install.sh"
    "$ROOT/upgrade.sh"
    "$ROOT/coin-upgrade.sh"
    "$ROOT/scripts/linux/pool-mode.sh"
    "$ROOT/scripts/linux/wait-for-node.sh"
    "$ROOT/scripts/spiralctl.sh"
)

# =============================================================================
# GUARD 1 — a coin's variables must not be referenced in another coin's function
# =============================================================================
log_test "cross-coin variable leaks in install.sh"

# install.sh names functions after the coin's full name but variables after its
# ticker, so the check needs the real mapping rather than a guess.
declare -A FN_COIN=(
    [install_bitcoin]=BTC          [install_digibyte]=DGB
    [install_bitcoincash]=BCH      [install_bitcoincashii]=BCH2
    [install_bitcoinii]=BC2        [install_bitcoinsilver]=BTCS
    [install_litecoin]=LTC         [install_dogecoin]=DOGE
    [install_pepecoin]=PEP         [install_catcoin]=CAT
    [install_namecoin]=NMC         [install_syscoin]=SYS
    [install_myriad]=XMY           [install_fbtc]=FBTC
    [install_ecash]=XEC
)

# install_* functions that are infrastructure, not coins.
NON_COIN_FNS="install_dashboard install_docker install_etcd install_go \
              install_patroni install_postgresql install_redis install_sentinel"

# Prefixes that are shared/global rather than owned by one coin. Derived by
# listing every *_DIR/*_DATA prefix in the file and removing the coin tickers.
SHARED_PREFIXES="INSTALL CONFIG POOL BACKUP SCRIPT CHAIN PRUNE TOR HA RPC
                 SPIRALPOOL WALLET BLOCKS CERT CHECKPOINT DASH DOCKER GOCACHE
                 GOPATH OUTPUT SENTINEL SRC TEMP"

# The map above is an ASSUMPTION about the file, and an unverified assumption is
# how the bug this guard exists to catch was introduced. Verify it: every mapped
# function must exist, and every install_* function must be either mapped or
# explicitly listed as non-coin. Otherwise the guard silently stops covering a
# coin as the file evolves.
map_stale=""
for fn in "${!FN_COIN[@]}"; do
    grep -qE "^${fn}\\(\\) \\{" "$ROOT/install.sh" || map_stale+="        mapped but missing: ${fn}()"$'\n'
done
while read -r fn; do
    [[ -n "$fn" ]] || continue
    [[ -n "${FN_COIN[$fn]:-}" ]] && continue
    grep -qw "$fn" <<< "$NON_COIN_FNS" && continue
    map_stale+="        unmapped install function: ${fn}() — add it to FN_COIN or NON_COIN_FNS"$'\n'
done < <(grep -oE '^install_[a-z0-9]+\(\)' "$ROOT/install.sh" | tr -d '()' | sort -u)

if [[ -z "$map_stale" ]]; then
    pass "the guard's own coin map matches install.sh"
else
    fail "the guard's coin map is stale — it would skip coins silently" "$map_stale"
fi


# ONE awk pass over install.sh, not a shell loop per line. The previous
# implementation forked grep+sed+sort for every line of every install_* body —
# roughly 24,000 processes — and never finished on a developer machine, so the
# guard against the bug class that actually shipped could not be run at all.
leaks=$(
    {
        for fn in "${!FN_COIN[@]}"; do printf 'MAP %s %s\n' "$fn" "${FN_COIN[$fn]}"; done
        for p in $SHARED_PREFIXES; do printf 'SHARED %s\n' "$p"; done
        printf 'FILE\n'
        cat "$ROOT/install.sh"
    } | awk '
        $1 == "MAP"    { own[$2] = $3; ticker[$3] = 1; next }
        $1 == "SHARED" { shared[$2] = 1; next }
        $1 == "FILE"   { body = 1; ln = 0; next }
        !body { next }
        { ln++ }
        # Track which coin function we are inside; a top-level } ends it.
        !cur && /^install_[a-z0-9]+\(\) \{/ {
            f = $1; sub(/\(\).*$/, "", f)
            if (f in own) { cur = f; mine = own[f] }
            next
        }
        cur && /^\}/ { cur = ""; next }
        !cur { next }
        {
            # Prose in a comment may mention another coin; only code counts.
            code = $0
            sub(/#.*$/, "", code)
            rest = code
            while (match(rest, /\$\{?[A-Z][A-Z0-9]*_(DIR|DATA)/)) {
                v = substr(rest, RSTART, RLENGTH)
                rest = substr(rest, RSTART + RLENGTH)
                sub(/^\$\{?/, "", v)
                owner = v; sub(/_.*$/, "", owner)
                if (owner in shared) continue
                if (owner == mine) continue
                # Another coin s *_DIR/_DATA is `local` to that coin s function,
                # so here it expands to EMPTY — and "rm -rf $EMPTY/bin" runs as
                # root against /bin. That is how this guard came to exist.
                if (owner in ticker)
                    printf "        %s() [line %d] references $%s (owned by %s)\n", cur, ln, v, owner
            }
        }
    '
)

if [[ -z "$leaks" ]]; then
    pass "no function references another coin's _DIR/_DATA variable"
else
    fail "a function references another coin's variable (expands to empty → destructive paths)" "$leaks"
fi

# =============================================================================
# GUARD 2 — no quoted string broken across lines inside a command argument
# =============================================================================
log_test "quoted strings spanning lines (invisible to bash -n)"

broken=""
for f in "${SHELL_FILES[@]}"; do
    [[ -f "$f" ]] || continue
    # Track shell quote state across the WHOLE file, character by character.
    #
    # Counting quotes per line does not work: `tr -d '"'"'"` and
    # `sed "s/'/''/g"` are ordinary, correct shell with an odd number of single
    # quotes on the line. That heuristic reported a dozen healthy lines and no
    # real ones. What actually matters is whether a single-quoted string OPENS
    # and CLOSES on different lines while serving as a program argument to a
    # tool whose program must be one line — which is exactly how
    #     sed -e 's/<newline>$//'
    # shipped: valid bash, `bash -n` clean, fails at runtime with
    # "unterminated `s' command", and 2>/dev/null hides it.
    #
    # Multi-line awk programs are legitimate and common here, so awk is not in
    # the command list.
    while IFS= read -r hit; do
        broken+="        ${f#$ROOT/}:${hit}"$'\n'
    done < <(awk '
        BEGIN { q = 0 }   # 0 = unquoted, 1 = inside single, 2 = inside double
        # Heredoc bodies are DATA, not shell. These files are mostly generated
        # config and systemd units, and an apostrophe in heredoc prose ("Core s
        # default") would otherwise open a phantom string and desync every line
        # after it. Skip the body, then resume at the terminator.
        inheredoc {
            t = $0; gsub(/^[[:space:]]+|[[:space:]]+$/, "", t)
            if (t == hd) inheredoc = 0
            next
        }
        {
            line = $0
            n = length(line)
            for (i = 1; i <= n; i++) {
                c = substr(line, i, 1)
                if (q == 0) {
                    if (c == "\\") { i++; continue }
                    if (c == "#" && (i == 1 || substr(line, i-1, 1) ~ /[[:space:]]/)) break
                    if (c == "\"") { q = 2; continue }
                    if (c == "'"'"'") {
                        q = 1; openln = NR
                        # Keep only the current PIPELINE SEGMENT before the quote.
                        # `tr -d '"'"'\015'"'"' ... | awk -v k=1 '"'"'` opens its long
                        # quote after awk, not after tr — judging by the whole
                        # line flagged every one of those as a broken sed.
                        pre = substr(line, 1, i - 1)
                        sub(/^.*[|;&(]/, "", pre)
                        opentxt = line; opencmd = pre
                        continue
                    }
                } else if (q == 2) {
                    if (c == "\\") { i++; continue }
                    if (c == "\"") { q = 0; continue }
                } else {
                    # Inside a single-quoted string nothing escapes; only '"'"' ends it.
                    if (c == "'"'"'") {
                        q = 0
                        if (openln != NR && opencmd ~ /^[[:space:]]*[A-Za-z_]*=?\$?\(?[[:space:]]*(sed|grep|tr|cut|perl)[[:space:]]/)
                            print openln": "substr(opentxt, 1, 100)
                        continue
                    }
                }
            }
            # A heredoc opener only counts outside quotes and outside comments,
            # which is why this runs after the scan rather than as its own rule.
            if (q == 0 && match(line, /<<-?[[:space:]]*'"'"'?"?[A-Za-z_][A-Za-z0-9_]*/)) {
                hd = substr(line, RSTART, RLENGTH)
                sub(/^<<-?[[:space:]]*'"'"'?"?/, "", hd)
                inheredoc = 1
            }
        }
        END { if (q == 1) print openln": UNTERMINATED single-quoted string: "substr(opentxt, 1, 100) }
    ' "$f")
done

if [[ -z "$broken" ]]; then
    pass "no sed/grep/tr/cut program argument spans a line break"
else
    fail "a quoted command argument spans lines — valid bash, fails at runtime" "$broken"
fi

# =============================================================================
# GUARD 3 — the config parsers must actually run, not merely parse
# =============================================================================
log_test "extracted config helpers execute and return a value"

extract_fn() {
    awk -v fn="$2" '
        !inside { if ($0 ~ "^[[:space:]]*" fn "\\(\\) \\{") {
            match($0,/^[[:space:]]*/); indent=substr($0,1,RLENGTH); inside=1; print } next }
        { print }
        $0 == indent "}" { exit }
    ' "$1"
}

W=$(mktemp -d); trap 'rm -rf "$W"' EXIT
printf 'chain=main\ndbcache=16384\nprune=5000\n' > "$W/probe.conf"

CI="$(extract_fn "$ROOT/upgrade.sh" _conf_int)"
if [[ -z "$CI" ]]; then
    fail "_conf_int is extractable" "renamed or removed"
else
    got=$(bash -c "set -u; conf_path='$W/probe.conf'; $CI; _conf_int dbcache" 2>/dev/null)
    if [[ "$got" == "16384" ]]; then
        pass "_conf_int runs and returns a value (not silently empty)"
    else
        fail "_conf_int returns a value" "        got [$got], expected [16384] — a runtime failure the syntax check cannot see"
    fi
fi

GP="$(extract_fn "$ROOT/scripts/linux/pool-mode.sh" get_existing_prune)"
if [[ -z "$GP" ]]; then
    fail "get_existing_prune is extractable" "renamed or removed"
else
    got=$(bash -c "set -u; SPIRALPOOL_DIR='$W/none'; $GP; get_existing_prune '$W/probe.conf'; printf '%s' \"\$EXISTING_PRUNE\"" 2>/dev/null)
    if [[ "$got" == "5000" ]]; then
        pass "get_existing_prune runs and returns a value"
    else
        fail "get_existing_prune returns a value" "        got [$got], expected [5000]"
    fi
fi

# =============================================================================
# GUARD 3b — a CLI passed to a QUOTED invocation must be a single word
# =============================================================================
# install.sh has two conventions for the same argument and they are not
# interchangeable:
#
#   check_blockchain_sync        INFO=$($cli -conf=…)      unquoted, word-splits
#   check_blockchain_daemon_health / is_daemon_synced
#                                $(timeout 30 "$cli" …)    QUOTED, one word only
#
# Passing "ecash-cli -rpcport=9004" to a quoted call makes the shell look for
# one executable with a space in its name: exit 127, read as "daemon not
# responding", and the monitor restarts a perfectly healthy daemon three times
# an hour, forever. That shipped because the name was copied from the unquoted
# call site without the calling convention.
log_test "multi-word CLI passed to a quoted invocation"

badcli=""
while IFS= read -r hit; do
    badcli+="        install.sh:${hit}"$'\n'
done < <(grep -nE '^[[:space:]]*(check_blockchain_daemon_health|is_daemon_synced)[[:space:]]+"[^"]*"[[:space:]]+"[^"]*[[:space:]][^"]*"' \
             "$ROOT/install.sh" | cut -c1-140)

if [[ -z "$badcli" ]]; then
    pass "no multi-word CLI reaches a quoted \"\$cli\" invocation"
else
    fail "a multi-word CLI reaches a quoted invocation (exit 127 → restart loop)" "$badcli"
fi

# =============================================================================
# GUARD 3c — a helper must not clobber its callers' loop counter
# =============================================================================
# download_with_retry declared six locals but not `url` or `attempt`, and five
# callers loop on a bare `attempt`. The callee left it at its own maximum, so a
# caller's `max_attempts=3` was really 1 and its retry branch was unreachable —
# in exactly the transient-network case the retries exist for.
#
# Executed, not grepped for `local`: run the REAL function inside a caller loop
# with wget and sleep stubbed to fail fast, and count how many iterations the
# caller actually gets. A mutation campaign proved nothing here caught this.
log_test "helper leaking its callers' loop counter"

DWR="$(extract_fn "$ROOT/install.sh" download_with_retry)"
if [[ -z "$DWR" ]]; then
    fail "download_with_retry is extractable" "renamed or removed"
else
    iters=$(bash -c '
        set -u
        log(){ :; }; log_success(){ :; }; log_warn(){ :; }; log_error(){ :; }
        wget(){ return 1; }          # always fail, exhaust the callee retries
        sleep(){ :; }                # no real delay
        '"$DWR"'
        max_attempts=3
        n=0
        for ((attempt=1; attempt<=max_attempts; attempt++)); do
            download_with_retry "'"$W"'/dl.bin" "http://example.invalid/x" >/dev/null 2>&1 || true
            n=$((n+1))
        done
        printf "%s" "$n"
    ' 2>/dev/null)
    if [[ "$iters" == "3" ]]; then
        pass "the caller's retry loop still runs all 3 attempts"
    else
        fail "the caller's retry loop runs all 3 attempts" \
             "        ran ${iters:-?} — download_with_retry is leaking \$attempt to its caller"
    fi
fi

# =============================================================================
# GUARD 3d — one coin version, four files, they must agree
# =============================================================================
# Each coin's version is written in four independent places:
#
#   install.sh                 what a bare-metal install actually fetches
#   docker/Dockerfile.<coin>   what the container image fetches
#   coin-upgrade.sh COIN_TARGET what we tell operators is current
#   tests/test-coin-configs.sh what the verification suite actually boots
#
# They drifted. LTC was the worst case: the installer fetched 0.21.5.4, the
# version cache was seeded 0.21.4, coin-upgrade called 0.21.5.6 current, and the
# test harness downloaded 0.21.4 — so every "PASS" for LTC validated software no
# operator would ever receive, while a fresh install shipped a node missing a
# consensus rule whose activation height had already passed.
log_test "coin version pins agree across install.sh, docker, coin-upgrade and the harness"

vdrift=""
# coin|install.sh var|Dockerfile suffix|harness archive prefix
while IFS='|' read -r coin var df arch; do
    [[ -n "$coin" ]] || continue
    iv=$(grep -oE "^[[:space:]]*(local[[:space:]]+)?${var}=\"?[0-9][0-9.]*" "$ROOT/install.sh" \
         | head -1 | grep -oE '[0-9][0-9.]*$')
    dv=$(grep -oE '^ARG [A-Z0-9_]*VERSION=[0-9][0-9.]*' "$ROOT/docker/Dockerfile.${df}" 2>/dev/null \
         | head -1 | cut -d= -f2)
    tv=$(grep -oE "\[${coin}\]=\"[0-9][0-9.]*\"" "$ROOT/coin-upgrade.sh" | head -1 | cut -d'"' -f2)
    hv=$(grep -oE "${arch}-[0-9][0-9.]*" "$ROOT/tests/test-coin-configs.sh" 2>/dev/null \
         | head -1 | sed "s/^${arch}-//")
    for pair in "docker:$dv" "target:$tv" "harness:$hv"; do
        other="${pair#*:}"
        [[ -z "$other" || -z "$iv" ]] && continue
        if [[ "$other" != "$iv" ]]; then
            vdrift+="        ${coin}: install.sh=${iv} but ${pair%%:*}=${other}"$'\n'
        fi
    done
done <<'COINS'
DGB|DIGIBYTE_VERSION|digibyte|digibyte
BTC|BITCOIN_CORE_VERSION|bitcoin|bitcoin
BCH|BCHN_VERSION|bitcoincash|bitcoin-cash-node
BCH2|BITCOINCASHII_VERSION|bitcoincashii|
BC2|BITCOINII_VERSION|bitcoinii|
LTC|LITECOIN_VERSION|litecoin|litecoin
DOGE|DOGECOIN_VERSION|dogecoin|dogecoin
PEP|PEPECOIN_VERSION|pepecoin|pepecoin
CAT|CATCOIN_VERSION|catcoin|
NMC|NAMECOIN_VERSION|namecoin|namecoin
SYS|SYSCOIN_VERSION|syscoin|syscoin
XMY|MYRIAD_VERSION|myriadcoin|myriadcoin
XEC|ECASH_VERSION|ecash|bitcoin-abc
COINS

if [[ -z "$vdrift" ]]; then
    pass "every coin pins the same version in install.sh, docker, coin-upgrade and the harness"
else
    fail "a coin's version differs between files — one of them ships or tests the wrong daemon" "$vdrift"
fi

# =============================================================================
# GUARD 4 — every shell file still parses
# =============================================================================
log_test "syntax"
syn=""
for f in "${SHELL_FILES[@]}"; do
    [[ -f "$f" ]] || continue
    bash -n "$f" 2>/dev/null || syn+="        ${f#$ROOT/}"$'\n'
done
[[ -z "$syn" ]] && pass "all shell files parse" || fail "shell files parse" "$syn"

# =============================================================================
# GUARD 5 — line endings must stay LF (.gitattributes mandates eol=lf)
# =============================================================================
log_test "line endings"
crlf=""
for f in "${SHELL_FILES[@]}"; do
    [[ -f "$f" ]] || continue
    grep -qU $'\r' "$f" 2>/dev/null && crlf+="        ${f#$ROOT/}"$'\n'
done
[[ -z "$crlf" ]] && pass "no CRLF in shell scripts" || fail "no CRLF in shell scripts" "$crlf"

echo ""
echo "═══════════════════════════════════════════════════════════"
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "═══════════════════════════════════════════════════════════"
[[ $FAILED -eq 0 ]] || exit 1
exit 0
