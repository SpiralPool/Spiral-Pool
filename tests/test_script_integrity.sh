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
BTCS|BTCS_VERSION|bitcoinsilver|
LTC|LITECOIN_VERSION|litecoin|litecoin
DOGE|DOGECOIN_VERSION|dogecoin|dogecoin
PEP|PEPECOIN_VERSION|pepecoin|pepecoin
CAT|CATCOIN_VERSION|catcoin|
NMC|NAMECOIN_VERSION|namecoin|namecoin
SYS|SYSCOIN_VERSION|syscoin|syscoin
XMY|MYRIAD_VERSION|myriadcoin|myriadcoin
XEC|ECASH_VERSION|ecash|bitcoin-abc
FBTC|FBTC_VERSION|fractalbitcoin|fractald
COINS

if [[ -z "$vdrift" ]]; then
    pass "every coin pins the same version in install.sh, docker, coin-upgrade and the harness"
else
    fail "a coin's version differs between files — one of them ships or tests the wrong daemon" "$vdrift"
fi

# =============================================================================
# GUARD 3e — pinned download checksums agree
# =============================================================================
# Coins that publish no signed checksum file are verified against a SHA256 pinned
# in three places: install.sh, docker/Dockerfile.<coin> and coin-upgrade.sh
# COIN_SHA256. A version bump that updates the pin in only one of them makes the
# others refuse the download, or keeps verifying the old file.
log_test "download checksum pins agree across install.sh, docker and coin-upgrade"

sdrift=""
# coin|install.sh variable|Dockerfile suffix
while IFS='|' read -r coin var df; do
    [[ -n "$coin" ]] || continue
    is=$(grep -oE "${var}=\"[0-9a-f]{64}\"" "$ROOT/install.sh" | head -1 | grep -oE '[0-9a-f]{64}')
    ds=$(grep -oE '^ARG SHA256=[0-9a-f]{64}' "$ROOT/docker/Dockerfile.${df}" 2>/dev/null | head -1 | cut -d= -f2)
    cs=$(grep -aoE "\[${coin}\]=\"[0-9a-f]{64}\"" "$ROOT/coin-upgrade.sh" | head -1 | grep -oE '[0-9a-f]{64}')
    if [[ -z "$is" || -z "$ds" || -z "$cs" ]]; then
        sdrift+="        ${coin}: missing pin (install.sh=${is:-none} docker=${ds:-none} coin-upgrade=${cs:-none})"$'\n'
    elif [[ "$is" != "$ds" || "$is" != "$cs" ]]; then
        sdrift+="        ${coin}: install.sh=${is} docker=${ds} coin-upgrade=${cs}"$'\n'
    fi
done <<'PINS'
BC2|BC2_SHA256|bitcoinii
BTCS|BTCS_SHA256|bitcoinsilver
FBTC|FBTC_SHA256|fractalbitcoin
PINS

if [[ -z "$sdrift" ]]; then
    pass "BC2, BTCS and FBTC pin the same download checksum in install.sh, docker and coin-upgrade"
else
    fail "a download checksum pin is missing or differs between files" "$sdrift"
fi

# =============================================================================
# GUARD 3f — verify_sha256 fails closed
# =============================================================================
# The pins above are worth nothing if the helper that checks them can pass a file
# it did not hash. Run the real function from install.sh against a known file.
log_test "install.sh verify_sha256 refuses a file whose hash is not the pin"

vfn=$(sed -n '/^verify_sha256() {/,/^}/p' "$ROOT/install.sh")
if [[ -z "$vfn" ]]; then
    fail "verify_sha256 is extractable from install.sh" "        renamed or removed"
else
    vtmp=$(mktemp -d)
    printf 'spiral' > "$vtmp/f"
    vgood=$(sha256sum "$vtmp/f" | awk '{print $1}')
    vres=$(
        eval "$vfn"
        log_error() { :; }; log_success() { :; }
        verify_sha256 "$vtmp/f" "$vgood" "match" && printf 'match-ok'
        verify_sha256 "$vtmp/f" "0000000000000000000000000000000000000000000000000000000000000000" "mismatch" || printf ' mismatch-refused'
        verify_sha256 "$vtmp/missing" "$vgood" "missing" || printf ' missing-refused'
    )
    if [[ "$vres" == "match-ok mismatch-refused missing-refused" ]]; then
        pass "verify_sha256 accepts the pinned hash and refuses a mismatch or an unreadable file"
    else
        fail "verify_sha256 does not fail closed" "        got [$vres]"
    fi
    rm -rf "$vtmp"
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

# =============================================================================
# GUARD 6 — no generated daemon config can be written with an empty rpcpassword
#
# A blank rpcpassword makes the daemon ignore password auth and fall back to
# its cookie file, so the pool's own RPC calls are rejected at runtime with
# "incorrect password attempt" while the install itself looks clean. Every
# heredoc that writes rpcpassword=$<COIN>_RPC_PASSWORD must be preceded by an
# ensure_rpc_password <COIN> call for that same coin.
# =============================================================================
log_test "generated configs cannot carry an empty rpcpassword"
rpcg=$(awk '
    /^    ensure_rpc_password / { g=$2; gl=NR }
    /^rpcpassword=\$[A-Z0-9_]+_RPC_PASSWORD/ {
        v=$0; sub(/^rpcpassword=\$/,"",v); sub(/_RPC_PASSWORD.*$/,"",v)
        if (g != v || NR-gl >= 60) print "        line "NR": writes "v" but nearest guard is "(g==""?"(none)":g)
    }
' "$ROOT/install.sh")
rpcn=$(grep -cE '^rpcpassword=\$[A-Z0-9_]+_RPC_PASSWORD' "$ROOT/install.sh")
if [[ -n "$rpcg" ]]; then
    fail "every generated config guards its rpcpassword" "$rpcg"
elif [[ "$rpcn" -eq 0 ]]; then
    fail "every generated config guards its rpcpassword" "        found no rpcpassword= sites - pattern moved?"
else
    pass "all $rpcn generated configs guard rpcpassword against an empty value"
fi

# =============================================================================
# GUARD 7 — every coin offered in solo mode has a wallet-setup arm
#
# The solo path dispatches on "case $SOLO_COIN in". A coin the menu can assign
# to SOLO_COIN but that has no arm in the wallet case gets no address prompt
# and no RPC password, and the install only fails much later when the daemon
# refuses the pool's RPC calls. BTCS and BCH2 both shipped that way.
# =============================================================================
log_test "every solo-selectable coin has a wallet setup arm"
solo_sel=$(grep -oE 'SOLO_COIN="[A-Z0-9_]+"' "$ROOT/install.sh" \
    | sed 's/SOLO_COIN="//; s/"//' | sort -u)
solo_arms=$(awk '
    # Each arm holds a nested "case $wallet_choice in", so the block has to be
    # tracked by depth or it closes at the first inner esac.
    /case[[:space:]]+"\$SOLO_COIN"[[:space:]]+in/ && !insolo { insolo=1; depth=1; block=""; arms=""; next }
    insolo {
        block = block $0
        if ($0 ~ /[[:space:]]*case[[:space:]].*[[:space:]]in[[:space:]]*$/) { depth++ }
        else if ($0 ~ /^[[:space:]]*esac/) {
            depth--
            if (depth == 0) { if (block ~ /Wallet Address/) printf "%s", arms; insolo=0 }
            next
        }
        if (depth == 1 && match($0, /^[[:space:]]+([A-Z0-9_]+)\)/, m)) arms = arms m[1] "\n"
    }
' "$ROOT/install.sh" | sort -u)
solo_missing=""
for _c in $solo_sel; do
    grep -qx "$_c" <<< "$solo_arms" || solo_missing+="        $_c is selectable as a solo coin but has no wallet arm"$'\n'
done
if [[ -z "$solo_sel" ]]; then
    fail "every solo-selectable coin has a wallet arm" "        found no SOLO_COIN assignments - pattern moved?"
elif [[ -n "$solo_missing" ]]; then
    fail "every solo-selectable coin has a wallet arm" "$solo_missing"
else
    pass "all $(wc -w <<< "$solo_sel") solo-selectable coins have a wallet setup arm"
fi

# =============================================================================
# GUARD 8 — every installable coin has an arm in the spiralpool-wallet generator
#
# Choosing "Generate one for me" only stores PENDING_GENERATION; the address is
# created later by spiralpool-wallet, and the stratum refuses to start until it
# is. A coin with no arm in that script's dispatch cannot get an address at all,
# and nothing fails until well after the install. The coin list comes from
# FN_COIN, which GUARD 1 has already verified against install.sh.
# =============================================================================
log_test "every installable coin can generate a wallet"
wallet_arms=$(sed -n "/spiralpool-wallet > \/dev\/null << 'WALLETEOF'/,/^WALLETEOF/p" "$ROOT/install.sh" | awk '
    # The coin dispatch is the top-level case on $COIN whose arms set CLI=.
    # Arms hold nested cases, so track depth.
    !insw && /^case[[:space:]]+"\$COIN"[[:space:]]+in/ { insw=1; depth=1; arm=""; out=""; hascli=0; next }
    insw {
        if ($0 ~ /[[:space:]]case[[:space:]].*[[:space:]]in[[:space:]]*$/ || $0 ~ /^case[[:space:]]/) { depth++; next }
        if ($0 ~ /^[[:space:]]*esac/) {
            depth--
            if (depth == 0) { if (hascli) printf "%s", out; insw=0; exit }
            next
        }
        if (depth == 1 && match($0, /^[[:space:]]+([a-z0-9|-]+)\)/, m)) { arm = m[1] }
        if (depth == 1 && arm != "" && $0 ~ /^[[:space:]]+CLI=/) { out = out arm "\n"; hascli=1; arm="" }
    }
' | tr '|' '\n' | sort -u)
wallet_missing=""
for _t in $(printf '%s\n' "${FN_COIN[@]}" | sort -u); do
    grep -qx "${_t,,}" <<< "$wallet_arms" || wallet_missing+="        ${_t}: no arm in spiralpool-wallet, so it can never generate an address"$'\n'
done
if [[ -z "$wallet_arms" ]]; then
    fail "every installable coin can generate a wallet" "        found no coin dispatch in spiralpool-wallet - pattern moved?"
elif [[ -n "$wallet_missing" ]]; then
    fail "every installable coin can generate a wallet" "$wallet_missing"
else
    pass "all ${#FN_COIN[@]} installable coins have a spiralpool-wallet generator arm"
fi

# =============================================================================
# GUARD 9 — the installer's own code never writes to $LOG_FILE
#
# install.sh has no log file. LOG_FILE is set only inside the scripts it
# generates (health monitor, sync monitor, backup, ...). A "| tee -a $LOG_FILE"
# in the installer itself runs tee with an empty name, printing
# "tee: '': No such file or directory" to the operator, as the wallet
# generation step in start_services did right before the sync screen.
# =============================================================================
log_test "installer code does not use the generated scripts' LOG_FILE"
logfile_uses=$(awk '
    hd == "" && match($0, /<< *-?'"'"'?"?([A-Z_][A-Z_0-9]*)'"'"'?"?/, m) { hd = m[1]; next }
    hd != "" { if ($0 ~ "^[[:space:]]*" hd "$") hd = ""; next }
    /^[[:space:]]*#/ { next }
    /\$\{?LOG_FILE/ { printf "        install.sh:%d: %s\n", NR, $0 }
' "$ROOT/install.sh")
if [[ -n "$logfile_uses" ]]; then
    fail "installer code does not use the generated scripts' LOG_FILE" "$logfile_uses"
else
    pass "no \$LOG_FILE use outside generated scripts"
fi

# =============================================================================
# GUARD 10 — the sync status shown before the live view lists every coin
#
# show_all_coins_sync_status prints the box the operator sees right before the
# sync screen. A coin missing from it prints an empty box for a solo install of
# that coin, which is what BCH2 and BTCS did.
# =============================================================================
log_test "sync status box lists every installable coin"
status_fn=$(sed -n '/^show_all_coins_sync_status() {/,/^}/p' "$ROOT/install.sh")
status_missing=""
for _t in $(printf '%s\n' "${FN_COIN[@]}" | sort -u); do
    grep -q "ENABLE_${_t}\" == \"true\"" <<< "$status_fn" || status_missing+="        ${_t}: not shown by show_all_coins_sync_status"$'\n'
done
if [[ -z "$status_fn" ]]; then
    fail "sync status box lists every installable coin" "        show_all_coins_sync_status not found - renamed?"
elif [[ -n "$status_missing" ]]; then
    fail "sync status box lists every installable coin" "$status_missing"
else
    pass "all ${#FN_COIN[@]} installable coins appear in the sync status box"
fi

# =============================================================================
# GUARD 11 — the dashboard starts at boot without waiting on stratum
#
# Seen on the second test install after a reboot: stratum stays "activating"
# while wait-for-node.sh waits for the chain, so a dashboard ordered
# After=spiralstratum.service stayed down for minutes, and the health monitor,
# started before the dashboard, blocked on restarting it. Both dashboard units
# (install.sh's and upgrade.sh's template) must also disable gunicorn's control
# socket, whose default under $HOME fails with ProtectHome=yes, and must not
# carry "\-" escapes, which systemd warns about on every reload.
# =============================================================================
log_test "dashboard unit starts independently and cleanly"
dash_unit_install=$(sed -n '/sudo tee \/etc\/systemd\/system\/spiraldash.service > \/dev\/null << EOF/,/^EOF$/p' "$ROOT/install.sh")
dash_unit_tpl=$(cat "$ROOT/scripts/linux/systemd/spiraldash.service" 2>/dev/null)
start_fn=$(sed -n '/^start_services() {/,/^}/p' "$ROOT/install.sh")
dash_problems=""
[[ -z "$dash_unit_install" ]] && dash_problems+="        spiraldash unit not found in install.sh - pattern moved?"$'\n'
[[ -z "$dash_unit_tpl" ]] && dash_problems+="        scripts/linux/systemd/spiraldash.service not found"$'\n'
for _unit_name in install tpl; do
    _var="dash_unit_${_unit_name}"
    _unit="${!_var}"
    [[ -z "$_unit" ]] && continue
    grep -E '^(After|Requires|Wants|BindsTo)=.*spiralstratum' <<< "$_unit" > /dev/null && dash_problems+="        ${_unit_name}: dashboard is ordered after spiralstratum"$'\n'
    grep -E '^ExecStart=.*gunicorn' <<< "$_unit" | grep -q -- '--no-control-socket' || dash_problems+="        ${_unit_name}: gunicorn control socket not disabled"$'\n'
    grep -qF -- '\-' <<< "$_unit" && dash_problems+="        ${_unit_name}: \"\\-\" escape in the unit file"$'\n'
done
_dash_line=$(grep -n 'start_service_if_exists "spiraldash"$' <<< "$start_fn" | tail -1 | cut -d: -f1)
_health_line=$(grep -n 'systemctl start spiralpool-health' <<< "$start_fn" | head -1 | cut -d: -f1)
if [[ -z "$_dash_line" || -z "$_health_line" ]]; then
    dash_problems+="        start_services: dashboard or health monitor start not found"$'\n'
elif (( _health_line < _dash_line )); then
    dash_problems+="        start_services: health monitor starts before the dashboard"$'\n'
fi
if [[ -n "$dash_problems" ]]; then
    fail "dashboard unit starts independently and cleanly" "$dash_problems"
else
    pass "dashboard units have no stratum ordering, no control socket, no \\- escapes; health monitor starts last"
fi

# =============================================================================
# GUARD 12 — the regtest harness cannot harm a real pool on the same machine
#
# install.sh's pool logs in to PostgreSQL as spiralstratum with a generated
# password, and runs as /spiralpool/bin/spiralstratum -config .... The regtest
# scripts used that same role, resetting its password to "spiralstratum" when
# they could not log in, and killed "spiralpool.*config" by name, which matches
# the real pool. Both would take down a pool the harness was run next to.
# =============================================================================
log_test "regtest harness is isolated from a real pool"
regtest_problems=""
for _rt in "$ROOT"/scripts/linux/regtest.sh "$ROOT"/scripts/linux/regtest-bc2.sh "$ROOT"/scripts/linux/regtest-dgb.sh; do
    _rn=$(basename "$_rt")
    grep -qE 'DB_USER="\$\{DB_USER:-spiralstratum\}"' "$_rt" && regtest_problems+="        ${_rn}: default DB_USER is the production role"$'\n'
    _broad=$(grep -nE 'pkill[^#]*"spiralpool\.\*-?config"' "$_rt" || true)
    [[ -n "$_broad" ]] && regtest_problems+="        ${_rn}: pkill pattern matches the real pool: ${_broad}"$'\n'
done
_rt_configs=("$ROOT"/config/regtest/config-*-regtest.yaml)
[[ -f "${_rt_configs[0]}" ]] || regtest_problems+="        no config/regtest/config-*-regtest.yaml found"$'\n'
for _rc in "${_rt_configs[@]}"; do
    [[ -f "$_rc" ]] || continue
    grep -qE '^[[:space:]]+user:[[:space:]]*"?spiralstratum"?[[:space:]]*$' "$_rc" && regtest_problems+="        $(basename "$_rc"): database user is the production role"$'\n'
done
if [[ -n "$regtest_problems" ]]; then
    fail "regtest harness is isolated from a real pool" "$regtest_problems"
else
    pass "regtest uses its own database login and only kills its own pool binary"
fi

log_test "regtest pool ports match their configs and avoid the coins' own RPC ports"
port_problems=""
_rt_sh="$ROOT/scripts/linux/regtest.sh"
# Every port the harness assumes must equal the one the config makes the pool or
# the daemon bind. PEP had two of these wrong at once: a port remap reached
# regtest.sh but only half reached config-pep-regtest.yaml, leaving the RPC port
# at 18570 and the API port at 14026. The RPC half failed loudly — the pool could
# not reach the daemon — but the API half was silent, because the check accepted
# any HTTP status from 200 to 499 and there was nothing listening to give one.
# XEC was worse: its api_port was 14022, DigiByte's standard RPC port, so on any
# host also running a DGB node the check read that daemon's 405 as a healthy pool
# API. All four pairs are compared here, not just the one that bit.
while read -r _coin _hrpc _hstr _hapi _hmet; do
    _cfg="$ROOT/config/regtest/config-${_coin//-scrypt/_scrypt}-regtest.yaml"
    if [[ ! -f "$_cfg" ]]; then
        port_problems+="        ${_coin}: no regtest config at $(basename "$_cfg")"$'\n'
        continue
    fi
    # rpc lives under nodes:, stratum under stratum:, the other two are global.
    read -r _crpc _cstr _capi _cmet < <(awk '
        /^  metrics_port:/ {m=$2} /^  api_port:/ {a=$2}
        /^    stratum:/ {s=1} /^      port:/ {if (s==1) {st=$2; s=0}}
        /^    nodes:/   {n=1} /^        port:/ {if (n==1) {rp=$2; n=0}}
        END {printf "%s %s %s %s\n", rp, st, a, m}' "$_cfg")
    for _pair in "rpc:${_hrpc}:${_crpc}" "stratum:${_hstr}:${_cstr}" \
                 "api:${_hapi}:${_capi}" "metrics:${_hmet}:${_cmet}"; do
        _what="${_pair%%:*}"; _rest="${_pair#*:}"
        _h="${_rest%%:*}"; _c="${_rest#*:}"
        [[ -z "$_h" ]] && continue          # harness does not define this one
        if [[ "$_h" != "$_c" ]]; then
            port_problems+="        ${_coin}: harness ${_what} ${_h}, config ${_c:-none}"$'\n'
        fi
    done
done < <(awk '
    /^        [a-z0-9_|-]+\)$/ { if (c!="" && rpc!="") print c, rpc, st, api, met; c=$0; gsub(/[ )]/,"",c); rpc=st=api=met="" }
    { if (match($0,/RPC_PORT_DEF=[0-9]+/))     rpc=substr($0,RSTART+13,RLENGTH-13)
      if (match($0,/STRATUM_PORT_DEF=[0-9]+/)) st =substr($0,RSTART+17,RLENGTH-17)
      if (match($0,/[^2]API_PORT_DEF=[0-9]+/)) api=substr($0,RSTART+14,RLENGTH-14)
      if (match($0,/METRICS_PORT_DEF=[0-9]+/)) met=substr($0,RSTART+17,RLENGTH-17) }
    END { if (c!="" && rpc!="") print c, rpc, st, api, met }' "$_rt_sh")

# No pool-side regtest port may sit on a port a coin daemon listens on by default.
_manifest="$ROOT/config/coins.manifest.yaml"
if [[ -f "$_manifest" ]]; then
    _std=$(grep -oE 'rpc_port: [0-9]+' "$_manifest" | awk '{print $2}' | sort -u)
    _rtp=$(grep -oE '(API_PORT_DEF|METRICS_PORT_DEF|STRATUM_PORT_DEF|STRATUM_V2_PORT_DEF|HA_API|HA_STRATUM|HA_METRICS)=[0-9]+' "$_rt_sh" | cut -d= -f2 | sort -u)
    for _p in $_rtp; do
        if printf '%s\n' "$_std" | grep -qx "$_p"; then
            port_problems+="        regtest pool port ${_p} is a coin daemon's standard RPC port"$'\n'
        fi
    done
fi

if [[ -n "$port_problems" ]]; then
    fail "regtest pool ports match their configs and avoid the coins' own RPC ports" "$port_problems"
else
    pass "every coin's harness rpc/stratum/api/metrics ports match its config, and no pool port sits on a coin's RPC port"
fi


log_test "every alert Sentinel raises can be muted from the CLI"
# spiralctl rejects an unlisted alert name outright with "Unknown alert type",
# so an alert added to Sentinel and not to _alerts_groups() cannot be muted at
# all. That is how coin_upgrade_available shipped unmutable: the two lists are
# maintained by hand, in different files and different languages.
#
# Names are read from the three per-type tables every alert has to appear in —
# the get_alert_cooldowns defaults, ALERT_BYPASS_QUIET, and the digest type_info
# registry — and NOT from send_alert() call sites, because several sites pass the
# name in a variable and a grep for send_alert("literal") misses precisely those.
# The first version of this guard did exactly that and passed on the bug it was
# written to catch.
alert_problems=""
_sentinel_py="$ROOT/src/sentinel/SpiralSentinel.py"
_ctl_sh="$ROOT/scripts/spiralctl.sh"

# Deliberately not offered by the CLI:
#   block_found       - documented in spiralctl.sh; it can never be muted.
#   startup_summary   - startup notification, exempt from ALERTS_ENABLED too.
_alert_exempt="block_found startup_summary"

if [[ -f "$_sentinel_py" && -f "$_ctl_sh" ]]; then
    _known=$(sed -n '/^_alerts_groups() {/,/^}/p' "$_ctl_sh" \
             | grep '|' | sed 's/^[^|]*|//' | tr ' ' '\n' \
             | grep -E '^[a-z0-9_]+$' | sort -u)
    while read -r _name; do
        [[ -z "$_name" ]] && continue
        case " $_alert_exempt " in *" $_name "*) continue ;; esac
        # send_alert() strips infra_/pool_ before consulting disabled_alerts, so
        # the bare name also mutes the Prometheus and Go-bridged variants. Try the
        # name as written first: pool_hashrate_drop is a canonical name itself.
        printf '%s\n' "$_known" | grep -qx "$_name" && continue
        # send_alert strips ONE prefix and breaks, so this must too.
        _bare="$_name"
        for _pfx in infra_ pool_; do
            if [[ "$_name" == "$_pfx"* ]]; then _bare="${_name#$_pfx}"; break; fi
        done
        if [[ "$_bare" != "$_name" ]] && printf '%s\n' "$_known" | grep -qx "$_bare"; then
            continue
        fi
        alert_problems+="        ${_name}: raised by Sentinel, rejected by 'spiralctl alerts disable'"$'\n'
    done < <({ awk '/^def get_alert_cooldowns/,/^ALERT_COOLDOWNS = /' "$_sentinel_py"
               awk '/^ALERT_BYPASS_QUIET = \{/,/^\}/' "$_sentinel_py"
               awk '/^[[:space:]]*type_info = \{/,/^[[:space:]]*\}$/' "$_sentinel_py"
             } | grep -oE '^[[:space:]]*"[a-z0-9_]+":' | tr -d ' ":' | sort -u)

    if [[ -n "$alert_problems" ]]; then
        fail "every alert Sentinel raises can be muted with 'spiralctl alerts disable'" "$alert_problems"
    else
        pass "every alert Sentinel raises is a name 'spiralctl alerts disable' accepts"
    fi
fi
log_test "every upgradable coin has an upstream release source"
# COIN_TARGET says what we install; COIN_UPSTREAM says where to look for
# anything newer. A coin in the first and not the second is never checked for
# new releases and says nothing about it — the same silent-by-omission failure
# as an alert missing from the spiralctl list, one table further along.
upstream_problems=""
_cu="$ROOT/coin-upgrade.sh"
if [[ -f "$_cu" ]]; then
    _targets=$(sed -n '/^declare -A COIN_TARGET=(/,/^)/p' "$_cu"                | grep -oE '^[[:space:]]*\[[A-Z0-9_]+\]' | tr -d ' []' | sort -u)
    _sources=$(sed -n '/^declare -A COIN_UPSTREAM=(/,/^)/p' "$_cu"                | grep -oE '^[[:space:]]*\[[A-Z0-9_]+\]' | tr -d ' []' | sort -u)
    for _c in $_targets; do
        printf '%s
' "$_sources" | grep -qx "$_c" ||             upstream_problems+="        ${_c}: has a target but no upstream release source"$'
'
    done
    for _c in $_sources; do
        printf '%s
' "$_targets" | grep -qx "$_c" ||             upstream_problems+="        ${_c}: has an upstream source but no target"$'
'
    done
    if [[ -n "$upstream_problems" ]]; then
        fail "every coin with a target has an upstream release source" "$upstream_problems"
    else
        pass "every coin with an upgrade target also has a source to check for newer releases"
    fi
fi

log_test "every celebrated block can be withdrawn when it orphans"
# Two halves that only work together. A celebration is stopped by withdrawing
# the block it stands for, so every trigger has to record its block hash and
# every orphan has to withdraw one. Wire only one half and the feature is inert
# in a way nothing complains about: celebrations still start, orphans still
# alert, and the lights simply never go out. That is how it was first written.
celebrate_problems=""
_sent_py="$ROOT/src/sentinel/SpiralSentinel.py"
if [[ -f "$_sent_py" ]]; then
    # Calls, not the definition: the def line carries "block_hash=None".
    _trig_total=$(grep -c "^[[:space:]]*trigger_block_celebration(" "$_sent_py" || true)
    _trig_hash=$(grep -c "^[[:space:]]*trigger_block_celebration(.*block_hash=" "$_sent_py" || true)
    if [[ "$_trig_total" -ne "$_trig_hash" ]]; then
        celebrate_problems+="        $(( _trig_total - _trig_hash )) of ${_trig_total} celebration trigger(s) do not record a block hash"$'
'
    fi

    _orphan_alerts=$(grep -c 'send_alert("block_orphaned"' "$_sent_py" || true)
    _orphan_stops=$(grep -c "stop_block_celebration(orphan" "$_sent_py" || true)
    if [[ "$_orphan_alerts" -ne "$_orphan_stops" ]]; then
        celebrate_problems+="        ${_orphan_alerts} orphan alert site(s) but ${_orphan_stops} withdraw the block from the celebration"$'
'
    fi

    if [[ -n "$celebrate_problems" ]]; then
        fail "every celebration records its block and every orphan withdraws one" "$celebrate_problems"
    else
        pass "all ${_trig_total} celebration triggers record a block hash, and all ${_orphan_alerts} orphan sites withdraw one"
    fi
fi

log_test "the toolchain installed and the toolchain built against are the same"
# upgrade.sh does not keep its own copy of these: it greps GO_VERSION and
# POSTGRES_VERSION back out of install.sh, so install.sh is the single source
# of truth. The Docker build is not wired that way — docker/Dockerfile carries
# its own ARG defaults, and until now nothing checked the two agree. That is
# the exact shape of the Litecoin drift this release fixed, where the installer
# fetched one version and the tests booted another, so every green result
# validated software nobody would actually receive. The postgres image tags are
# checked against the same major for the same reason: HA replication only works
# between two servers of the same major, and one of them is a container.
toolchain_problems=""
_dockerfile="$ROOT/docker/Dockerfile"
if [[ -f "$ROOT/install.sh" && -f "$_dockerfile" ]]; then
    _pg_major=""
    for _v in GO_VERSION POSTGRES_VERSION; do
        _inst=$(grep -oE "^${_v}=\"[^\"]+\"" "$ROOT/install.sh" | head -1 | cut -d'"' -f2)
        _dock=$(grep -oE "^ARG ${_v}=[^[:space:]]+" "$_dockerfile" | head -1 | cut -d'=' -f2)
        if [[ -z "$_inst" ]]; then
            toolchain_problems+="        ${_v}: not found in install.sh"$'\n'
        elif [[ -z "$_dock" ]]; then
            toolchain_problems+="        ${_v}: no ARG ${_v} in docker/Dockerfile"$'\n'
        elif [[ "$_inst" != "$_dock" ]]; then
            toolchain_problems+="        ${_v}: install.sh says ${_inst}, docker/Dockerfile says ${_dock}"$'\n'
        else
            [[ "$_v" == "POSTGRES_VERSION" ]] && _pg_major="$_inst"
        fi
    done

    # Every pinned postgres image must be that same major.
    if [[ -n "$_pg_major" ]]; then
        while IFS= read -r _img; do
            [[ -n "$_img" ]] || continue
            _tag="${_img#*postgres:}"
            [[ "${_tag%%.*}" == "$_pg_major" ]] ||                 toolchain_problems+="        postgres image ${_tag} is not major ${_pg_major}"$'\n'
        done < <(grep -rhoE "postgres:[0-9][^[:space:]\"']*" "$ROOT/docker/" 2>/dev/null | sort -u)
    fi

    if [[ -n "$toolchain_problems" ]]; then
        fail "install.sh, docker/Dockerfile and the postgres images pin the same versions" "$toolchain_problems"
    else
        pass "install.sh and Docker agree on Go, and every postgres image is major ${_pg_major}"
    fi
fi

echo ""
log_test "every fallback peer a script can write is one the installer also ships"
# Three scripts write coin configs and each keeps its own copy of the peer list:
# install.sh writes from scratch, upgrade.sh tops up an existing config, and
# pool-mode.sh rewrites one when the operator switches coins. A peer added to
# one and not the others means a fresh install, an upgraded install and a
# mode-switched install end up with different sets -- the drift that already
# cost this project once over the Litecoin version. install.sh is allowed to be
# a superset (it ships peers for coins the others only DNS-seed), but no other
# script may write a peer the installer has never heard of.
#
# What this CANNOT catch: a port wrong in every copy at once. Every BCH peer
# shipped on :8433 -- Spiral Pool's own remapped listen port -- rather than
# :8333, which is what other nodes actually run. All three files agreed, so no
# static check saw it; 0 of 15 answered until they were probed against the live
# network. What it DOES catch is the half-fix: correcting two copies and missing
# the third, which is exactly how pool-mode.sh was left behind.
peer_problems=""
_ins="$ROOT/install.sh"
if [[ -f "$_ins" ]]; then
    _ins_peers=$(grep -aoE '^addnode=[0-9.]+:[0-9]+' "$_ins" | sed 's/^addnode=//' | sort -u)
    _peers_checked=0
    for _other in "$ROOT/upgrade.sh" "$ROOT/scripts/linux/pool-mode.sh"; do
        [[ -f "$_other" ]] || continue
        # upgrade.sh keeps its peers in COIN_PEERS and also shows an example
        # peer inside a comment; pool-mode.sh writes its peers as plain config
        # lines in a heredoc.
        case "$_other" in
            */upgrade.sh) _other_peers=$(sed -n '/COIN_PEERS=(/,/^    )/p' "$_other" | grep -aoE 'addnode=[0-9.]+:[0-9]+') ;;
            *)            _other_peers=$(grep -aoE '^addnode=[0-9.]+:[0-9]+' "$_other") ;;
        esac
        _other_peers=$(printf '%s\n' "$_other_peers" | sed 's/^addnode=//' | sort -u)
        _peers_checked=$(( _peers_checked + $(printf '%s\n' "$_other_peers" | grep -c . || true) ))
        _missing=$(comm -23 <(printf '%s\n' "$_other_peers") <(printf '%s\n' "$_ins_peers"))
        while read -r _p; do
            [[ -z "$_p" ]] && continue
            peer_problems+="        ${_p}: written by $(basename "$_other"), unknown to install.sh"$'\n'
        done <<< "$_missing"
    done
    if [[ -n "$peer_problems" ]]; then
        fail "no script writes a peer the installer does not ship" "$peer_problems"
    else
        pass "all ${_peers_checked} fallback peers upgrade.sh and pool-mode.sh can write are shipped by install.sh too"
    fi
fi

# Which Sentinel config file is in force depends on the sandbox rather than on
# the disk: under ProtectHome=yes the daemon cannot use its home directory and
# falls back to the install directory, while a tool the operator runs finds the
# home copy first. Each tool resolving it independently is how `config validate`
# came to report on a file the service had never read, and how `config set`
# could write to one. The daemon publishes the path it actually opened; this
# guards against anyone going back to guessing it.
log_test "sentinel config path comes from the running service, not a guess"
sentinel_path_problems=""

if [[ -f "$ROOT/src/sentinel/SpiralSentinel.py" ]]; then
    grep -q "^def publish_config_path" "$ROOT/src/sentinel/SpiralSentinel.py" \
        || sentinel_path_problems+="        SpiralSentinel.py no longer defines publish_config_path()"$'\n'
    # It has to be the daemon that publishes. A --status, --test or --reload run
    # resolves the path as the invoking user, normally the home copy, and
    # publishing that would aim every other tool at the file the service is not
    # using — the exact fault this mechanism exists to remove.
    grep -q "^        publish_config_path()" "$ROOT/src/sentinel/SpiralSentinel.py" \
        || sentinel_path_problems+="        publish_config_path() is not called from the daemon branch"$'\n'
fi

if [[ -f "$ROOT/scripts/spiralctl.sh" ]]; then
    grep -q "_sentinel_config_path()" "$ROOT/scripts/spiralctl.sh" \
        || sentinel_path_problems+="        spiralctl.sh no longer defines _sentinel_config_path()"$'\n'
    grep -q "sentinel-config-path" "$ROOT/scripts/spiralctl.sh" \
        || sentinel_path_problems+="        spiralctl.sh does not consult the path the daemon publishes"$'\n'
    # One literal home path is expected: the resolver's own fallback, for a pool
    # whose Sentinel has not started since the upgrade. More than one means a
    # caller has gone back to building the path itself.
    _sentinel_literals=$(grep -c '\.spiralsentinel/config\.json' "$ROOT/scripts/spiralctl.sh" || true)
    if [[ "$_sentinel_literals" -gt 1 ]]; then
        sentinel_path_problems+="        spiralctl.sh builds the config path literally in ${_sentinel_literals} places; only _sentinel_config_path should"$'\n'
    fi
fi

if [[ -n "$sentinel_path_problems" ]]; then
    fail "every tool takes the sentinel config path from the running service" "$sentinel_path_problems"
else
    pass "sentinel config path is resolved once, from the path the daemon publishes"
fi

echo "═══════════════════════════════════════════════════════════"
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "═══════════════════════════════════════════════════════════"
[[ $FAILED -eq 0 ]] || exit 1
exit 0
