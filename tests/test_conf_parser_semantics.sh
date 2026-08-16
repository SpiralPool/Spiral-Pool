#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool — Config Parser Semantics
# =============================================================================
# Several helpers in this repo read a coin's config file and then REWRITE it.
# Any disagreement between how they read a value and how the daemon reads the
# same value silently CHANGES that value — and for prune and txindex, a change
# in either direction is destructive:
#
#   txindex  Bitcoin Core records whether the transaction index was built as a
#            flag in the block-index DB and compares it to -txindex at startup.
#            On pre-0.17 bases (dogecoin 1.14.9, pepecoin 1.1.0) a mismatch in
#            EITHER direction is fatal:
#              "You need to rebuild the database using -reindex-chainstate to
#               change -txindex"
#
#   prune    Reporting a prune target for an archival node starts irreversible
#            block deletion. Reporting 0 for a pruned node writes a config the
#            daemon refuses to start from.
#
# So these helpers must replicate the daemon's parser, not approximate it. The
# rules, from the sources the helpers' own comments cite:
#
#   GetConfigOptions()  truncates each line at the FIRST '#', then trims.
#   InterpretBool()     if (strValue.empty()) return true;
#                       return (atoi(strValue) != 0);
#   precedence          [main] beats top-level; first assignment wins within a
#                       scope (doc/bitcoin-conf.md).
#   dogecoin/pepecoin   parse with boost's config_file_iterator, where "[main]"
#                       becomes a key PREFIX ("main.txindex") that nothing
#                       reads — so only top-level assignments exist.
#
# atoi, NOT a word match, is the subtle one and is what this suite was written
# for: "true" parses to 0 and is therefore FALSE to the daemon, while "01", "2"
# and "-1" are all true. A `case "$v" in 1|true)` shipped and inverted five
# input classes.
#
# Usage: bash tests/test_conf_parser_semantics.sh
# Exit:  0 all pass, 1 otherwise
# =============================================================================

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; NC='\033[0m'
RUN=0; PASSED=0; FAILED=0
section() { echo ""; echo -e "${CYAN}── $1${NC}"; }
chk() { # <desc> <got> <want>
    RUN=$((RUN+1))
    if [[ "$2" == "$3" ]]; then
        PASSED=$((PASSED+1)); echo -e "  ${GREEN}PASS${NC}  $1"
    else
        FAILED=$((FAILED+1)); echo -e "  ${RED}FAIL${NC}  $1 — got [$2] want [$3]"
    fi
}

# Extract a function body by name, honouring its indentation so the closing
# brace of a NESTED function does not end the extraction early.
extract_fn() { # <file> <fn>
    awk -v fn="$2" '
        !inside { if ($0 ~ "^[[:space:]]*" fn "\\(\\) \\{") {
            match($0,/^[[:space:]]*/); indent=substr($0,1,RLENGTH); inside=1; print } next }
        { print }
        $0 == indent "}" { exit }
    ' "$1"
}

W=$(mktemp -d); trap 'rm -rf "$W"' EXIT

TXI="$(extract_fn "$ROOT/scripts/linux/pool-mode.sh" get_existing_txindex)"
PRU="$(extract_fn "$ROOT/scripts/linux/pool-mode.sh" get_existing_prune)"
EFF="$(extract_fn "$ROOT/scripts/spiralctl.sh" _conf_effective)"
ADD="$(extract_fn "$ROOT/scripts/spiralctl.sh" _conf_add_toplevel)"
BOO="$(extract_fn "$ROOT/scripts/spiralctl.sh" _conf_bool)"

for pair in "get_existing_txindex:$TXI" "get_existing_prune:$PRU" \
            "_conf_effective:$EFF" "_conf_add_toplevel:$ADD" "_conf_bool:$BOO"; do
    if [[ -z "${pair#*:}" ]]; then
        echo -e "  ${RED}FATAL${NC}: could not extract ${pair%%:*} — renamed or removed"
        exit 1
    fi
done

# =============================================================================
section "get_existing_txindex — must resolve exactly as InterpretBool does"
# =============================================================================
txi() { # <conf body> [conf name] -> emitted line
    local name="${2:-bitcoin.conf}"
    printf '%s' "$1" > "$W/$name"
    bash -c "set -u; $TXI; get_existing_txindex '$W/$name' 0; printf '%s' \"\$EXISTING_TXINDEX\""
}

# The five classes that a `case "$v" in 1|true)` got backwards.
chk 'txindex=true  -> atoi("true")==0, so FALSE'  "$(txi 'txindex=true
')"   "txindex=0"
chk 'txindex=false -> atoi("false")==0, so FALSE' "$(txi 'txindex=false
')"   "txindex=0"
chk 'txindex=      -> empty value is TRUE'        "$(txi 'txindex=
')"   "txindex=1"
chk 'txindex=01    -> atoi 1, TRUE'               "$(txi 'txindex=01
')"   "txindex=1"
chk 'txindex=2     -> atoi 2, TRUE'               "$(txi 'txindex=2
')"   "txindex=1"
chk 'txindex=-1    -> atoi -1 != 0, TRUE'         "$(txi 'txindex=-1
')"   "txindex=1"
chk 'txindex=1#c   -> truncated at #, TRUE'       "$(txi 'txindex=1#c
')"   "txindex=1"
# The case above does NOT actually prove the truncation: without it, atoi("1#c")
# is still 1 and the answer is unchanged. Only an EMPTY value followed by a
# comment separates the two — truncated it is "" (Core: TRUE), untruncated it is
# "#c" (atoi 0: FALSE). A mutation campaign removed the truncation and every
# suite still passed until these two cases existed.
chk 'txindex=#c    -> empty value, TRUE'          "$(txi 'txindex=#c
')"   "txindex=1"
chk 'txindex= # n  -> empty value, TRUE'          "$(txi 'txindex= # n
')"   "txindex=1"
chk 'txindex=abc   -> atoi 0, FALSE'              "$(txi 'txindex=abc
')"   "txindex=0"

# Core's "no" negation prefix. notxindex=0 is the dangerous one: it means
# txindex ON, and reading it as absent emits nothing — the fatal mismatch on a
# pre-0.17 base whose index IS built.
chk 'notxindex=1   -> txindex OFF'                "$(txi 'notxindex=1
')"   "txindex=0"
chk 'notxindex=0   -> txindex ON'                 "$(txi 'notxindex=0
')"   "txindex=1"

# The straightforward ones must not have regressed.
chk 'txindex=1'                                   "$(txi 'txindex=1
')"   "txindex=1"
chk 'txindex=0'                                   "$(txi 'txindex=0
')"   "txindex=0"
chk 'absent -> emit nothing (DB flag is false)'   "$(txi 'server=1
')"   ""
chk '#txindex=1 is a comment, not a setting'      "$(txi '#txindex=1
')"   ""
chk 'whitespace around ='                         "$(txi '  txindex = 1
')" "txindex=1"
chk 'CRLF config'                                 "$(printf 'txindex=1\r\n' > "$W/crlf.conf"; bash -c "set -u; $TXI; get_existing_txindex '$W/crlf.conf' 0; printf '%s' \"\$EXISTING_TXINDEX\"")" "txindex=1"

section "  section precedence"
chk '[main] beats top-level'                      "$(txi 'txindex=0

[main]
txindex=1
')" "txindex=1"
chk 'first wins within a scope'                   "$(txi 'txindex=1
txindex=0
')" "txindex=1"
chk '[test] does not apply to mainnet'            "$(txi '[test]
txindex=1
')" ""
chk 'dogecoin: no sections, [main] ignored'       "$(txi 'txindex=0

[main]
txindex=1
' dogecoin.conf)" "txindex=0"
chk 'pepecoin: no sections, [main] ignored'       "$(txi 'txindex=0

[main]
txindex=1
' pepecoin.conf)" "txindex=0"

section "  pruning overrides everything (Core rejects txindex + prune>0)"
printf 'txindex=1\n' > "$W/p.conf"
chk 'pruned node never carries txindex' \
    "$(bash -c "set -u; $TXI; get_existing_txindex '$W/p.conf' 5000; printf '%s' \"\$EXISTING_TXINDEX\"")" ""
chk 'new coin, full node -> txindex=1' \
    "$(bash -c "set -u; $TXI; get_existing_txindex '$W/nope.conf' 0; printf '%s' \"\$EXISTING_TXINDEX\"")" "txindex=1"
chk 'new coin, pruned -> nothing' \
    "$(bash -c "set -u; $TXI; get_existing_txindex '$W/nope.conf' 5000; printf '%s' \"\$EXISTING_TXINDEX\"")" ""

# =============================================================================
section "get_existing_prune — an existing config is authoritative"
# =============================================================================
# With PRUNE_ENABLED=true, an unparseable or absent prune value must NOT inherit
# the pool-wide target: the daemon reads all of these as 0 (archival), so
# writing prune=5000 would delete blocks the operator asked to keep.
mkdir -p "$W/config"
printf 'PRUNE_ENABLED="true"\n' > "$W/config/coins.env"
pru() {
    printf '%s' "$1" > "$W/bitcoin.conf"
    bash -c "set -u; SPIRALPOOL_DIR='$W'; YELLOW=''; NC=''; $PRU; get_existing_prune '$W/bitcoin.conf'; printf '%s' \"\$EXISTING_PRUNE\""
}
chk 'prune=     -> archival (0), not the global 5000' "$(pru 'prune=
')" "0"
chk 'prune=abc  -> archival (0), not the global 5000' "$(pru 'prune=abc
')" "0"
chk 'no prune line -> archival (0)'                   "$(pru 'server=1
')" "0"
chk 'prune=1000 -> preserved'                         "$(pru 'prune=1000
')" "1000"
chk 'prune=0 stays 0'                                 "$(pru 'prune=0
')" "0"
chk 'prune=5000M -> atoi 5000'                        "$(pru 'prune=5000M
')" "5000"
chk 'no config at all -> pool-wide default applies' \
    "$(bash -c "set -u; SPIRALPOOL_DIR='$W'; YELLOW=''; NC=''; $PRU; get_existing_prune '$W/absent.conf'; printf '%s' \"\$EXISTING_PRUNE\"")" "5000"

section "  the no- negation prefix (Core InterpretValue)"
# A negated option resolves to a BOOL, and a double negative flips it back, so
# SettingToInt gives 0 or 1 — never the value's digits:
#   noprune=1 -> 0        noprune=0     -> 1
#   noprune=  -> 0        noprune=false -> 1  (atoi("false") == 0)
# Before this was handled, `noprune` was not recognised at all and the NEXT
# prune= line was taken as the first assignment — a different value than the
# daemon reads, on the setting that deletes block data.
chk 'noprune=1     -> prune 0'                "$(pru 'noprune=1
')" "0"
chk 'noprune=0     -> prune 1 (double neg)'   "$(pru 'noprune=0
')" "1"
chk 'noprune=      -> prune 0'                "$(pru 'noprune=
')" "0"
chk 'noprune=false -> prune 1 (atoi)'         "$(pru 'noprune=false
')" "1"
chk 'noprune wins as the FIRST assignment'    "$(pru 'noprune=1
prune=5000
')" "0"
chk 'a later noprune does not win'            "$(pru 'prune=5000
noprune=1
')" "5000"
chk 'noprune under [test] is not mainnet'     "$(pru '[test]
noprune=0
')" "0"
chk 'prunexyz is not prune'                   "$(pru 'prunexyz=9
')" "0"
chk 'noprunexyz is not noprune'               "$(pru 'noprunexyz=9
')" "0"

# =============================================================================
section "upgrade.sh _conf_int — atoi semantics, not a bare-integer match"
# =============================================================================
# Core parses these with atoi, so "dbcache=+16384" is 16384 to the daemon.
# Returning empty made the caller treat the key as absent and skip the OOM cap
# entirely — silently, on the setting that exists to stop a multi-coin OOM.
# This existed as a fix with no test: a mutation campaign reintroduced the bug
# and every suite still passed.
CI="$(extract_fn "$ROOT/upgrade.sh" _conf_int)"
if [[ -z "$CI" ]]; then
    chk '_conf_int is extractable' "missing" "present"
else
    ci() { printf '%s' "$1" > "$W/i.conf"; bash -c "conf_path='$W/i.conf'; $CI; _conf_int '$2'"; }
    chk 'dbcache=16384'                "$(ci 'dbcache=16384
' dbcache)" "16384"
    chk 'dbcache=+16384 -> atoi 16384' "$(ci 'dbcache=+16384
' dbcache)" "16384"
    chk 'dbcache=16384M -> atoi 16384' "$(ci 'dbcache=16384M
' dbcache)" "16384"
    chk 'dbcache=abc -> empty'         "$(ci 'dbcache=abc
' dbcache)" ""
    chk 'absent key -> empty'          "$(ci 'server=1
' dbcache)" ""
    chk '[main] beats top-level'       "$(ci 'dbcache=1

[main]
dbcache=16384
' dbcache)" "16384"
fi

# =============================================================================
section "spiralctl _conf_effective — same rules, shared implementation"
# =============================================================================
eff() { printf '%s' "$1" > "$W/e.conf"; bash -c "$EFF; _conf_effective '$W/e.conf' '$2'"; }
chk 'reads a plain value'                "$(eff 'prune=5000
' prune)" "5000"
chk 'truncates at #'                     "$(eff 'prune=5000#note
' prune)" "5000"
chk '[main] beats top-level'             "$(eff 'prune=0

[main]
prune=5000
' prune)" "5000"
chk 'ignores [test]'                     "$(eff '[test]
prune=5000
' prune)" ""
chk 'absent key prints nothing'          "$(eff 'server=1
' prune)" ""
chk 'prefix key does not match'          "$(eff 'prunexyz=9
' prune)" ""

# =============================================================================
section "spiralctl _conf_bool — an empty value is TRUE, not absent"
# =============================================================================
# _conf_effective cannot distinguish "key absent" from "key present, empty
# value", and the two mean opposite things: InterpretBool reads an empty value
# as TRUE. Reading `txindex=` as off let `spiralctl coin prune` enable pruning
# without commenting txindex out, producing a config Core refuses to start from.
bol() { printf '%s' "$1" > "$W/b.conf"; bash -c "$BOO; _conf_bool '$W/b.conf' '$2'"; }
chk 'txindex=      -> 1 (empty is true)'   "$(bol 'txindex=
' txindex)" "1"
chk 'txindex=1     -> 1'                   "$(bol 'txindex=1
' txindex)" "1"
chk 'txindex=0     -> 0'                   "$(bol 'txindex=0
' txindex)" "0"
chk 'txindex=true  -> 0 (atoi)'            "$(bol 'txindex=true
' txindex)" "0"
chk 'txindex=#c    -> 1 (empty after #)'   "$(bol 'txindex=#c
' txindex)" "1"
chk 'txindex=2     -> 1 (atoi)'            "$(bol 'txindex=2
' txindex)" "1"
chk 'genuinely absent -> empty'            "$(bol 'server=1
' txindex)" ""
chk 'commented out -> empty'               "$(bol '#txindex=1
' txindex)" ""
chk '[test] scope ignored'                 "$(bol '[test]
txindex=1
' txindex)" ""
chk '[main] beats top-level'               "$(bol 'txindex=0

[main]
txindex=1
' txindex)" "1"
chk 'notxindex=0   -> 1 (negation: ON)'    "$(bol 'notxindex=0
' txindex)" "1"
chk 'notxindex=1   -> 0 (negation: OFF)'   "$(bol 'notxindex=1
' txindex)" "0"
chk 'nodebuglogfile is NOT a txindex key'  "$(bol 'nodebuglogfile=1
' txindex)" ""
chk 'notxindexfoo is NOT a txindex key'    "$(bol 'notxindexfoo=1
' txindex)" ""

# =============================================================================
section "the two boolean readers must agree — differential"
# =============================================================================
# _conf_bool (spiralctl) and get_existing_txindex (pool-mode) are documented as
# implementing the same rules, and they decide the same thing: whether a node
# has the transaction index on. They shipped disagreeing on five inputs, all
# `no`-prefixed, because only one of them handled the negation — and where they
# disagree, `spiralctl coin prune` writes prune=5000 beside a live txindex and
# the daemon refuses to start.
#
# This comparison cannot pass vacuously: gutting EITHER function makes the two
# diverge on the inputs where the other still returns a value. The assertions
# above that check for "" can each be satisfied by a broken implementation; this
# one cannot.
for _case in 'txindex=1' 'txindex=0' 'txindex=' 'txindex=true' 'txindex=false' \
             'txindex=01' 'txindex=2' 'txindex=-1' 'txindex=abc' 'txindex=1#c' \
             'notxindex=0' 'notxindex=1' 'notxindex=' 'nodebuglogfile=1' \
             'notxindexfoo=1' 'txindexfoo=1' 'server=1' '#txindex=1' \
             '  txindex = 1  '; do
    printf '%s\n' "$_case" > "$W/diff.conf"
    _b=$(bash -c "$BOO; _conf_bool '$W/diff.conf' txindex")
    _g=$(bash -c "set -u; $TXI; get_existing_txindex '$W/diff.conf' 0; printf '%s' \"\$EXISTING_TXINDEX\"")
    case "$_b" in
        1)  _want="txindex=1" ;;
        0)  _want="txindex=0" ;;
        *)  _want="" ;;
    esac
    chk "agree on [${_case}]" "$_g" "$_want"
done

# =============================================================================
section "spiralctl _conf_add_toplevel — must not land inside a section"
# =============================================================================
add() { # <body> -> effective prune after inserting prune=5000
    printf '%s' "$1" > "$W/a.conf"
    bash -c "$ADD; $EFF; _conf_add_toplevel '$W/a.conf' 'prune=5000' && _conf_effective '$W/a.conf' prune"
}
chk 'file with no sections: appended'         "$(add 'server=1
')" "5000"
chk 'file ending in [test]: inserted ABOVE it' "$(add 'server=1

[test]
rpcport=18332
')" "5000"
chk 'file starting with [main]: inserted above' "$(add '[main]
server=1
')" "5000"

printf 'rpcpassword=SECRET' > "$W/nonl.conf"   # no trailing newline
bash -c "$ADD; _conf_add_toplevel '$W/nonl.conf' 'prune=5000'" >/dev/null
chk 'no trailing newline: does not corrupt the last line' \
    "$(grep -c '^rpcpassword=SECRET$' "$W/nonl.conf")" "1"
chk 'no trailing newline: the new line is its own line' \
    "$(grep -c '^prune=5000$' "$W/nonl.conf")" "1"
chk 'leaves no orphan temp file holding rpcpassword' \
    "$(find "$W" -name '*.bak-tmp.*' | wc -l | tr -d ' ')" "0"

# =============================================================================
section "check_multi_disk_layout — a guard must fail CLOSED"
# =============================================================================
# Reading CHAIN_MOUNT_POINT is the guard's only input. If the read returns empty
# the guard concludes "single disk, proceed" — so every shape coins.env can
# legitimately take must parse, or a multi-disk host resyncs every coin.
MDL="$(extract_fn "$ROOT/scripts/linux/pool-mode.sh" check_multi_disk_layout)"
READER=$(awk '/mount_point=\$\(tr -d/,/^    .\)$/' <<< "$MDL")
if [[ -z "$READER" ]]; then
    RUN=$((RUN+1)); FAILED=$((FAILED+1))
    echo -e "  ${RED}FAIL${NC}  could not extract the CHAIN_MOUNT_POINT reader"
else
    mdl() { printf '%s' "$1" > "$W/config/ce.env"; bash -c "coins_env='$W/config/ce.env'; $READER; printf '%s' \"\$mount_point\""; }
    chk 'plain quoted (what install.sh writes)' "$(mdl 'CHAIN_MOUNT_POINT="/mnt/chains"
')" "/mnt/chains"
    chk 'unquoted'                    "$(mdl 'CHAIN_MOUNT_POINT=/mnt/chains
')" "/mnt/chains"
    chk 'single-quoted'               "$(mdl "CHAIN_MOUNT_POINT='/mnt/chains'
")" "/mnt/chains"
    chk 'export prefix'               "$(mdl 'export CHAIN_MOUNT_POINT="/mnt/chains"
')" "/mnt/chains"
    chk 'leading whitespace'          "$(mdl '   CHAIN_MOUNT_POINT="/mnt/chains"
')" "/mnt/chains"
    chk 'spaces around ='             "$(mdl 'CHAIN_MOUNT_POINT = "/mnt/chains"
')" "/mnt/chains"
    chk 'CRLF'                        "$(printf 'CHAIN_MOUNT_POINT="/mnt/chains"\r\n' > "$W/config/ce.env"; bash -c "coins_env='$W/config/ce.env'; $READER; printf '%s' \"\$mount_point\"")" "/mnt/chains"
    chk 'trailing comment'            "$(mdl 'CHAIN_MOUNT_POINT=/mnt/chains # set by installer
')" "/mnt/chains"
    chk 'last assignment wins (file is sourced)' "$(mdl 'CHAIN_MOUNT_POINT=/old
CHAIN_MOUNT_POINT=/mnt/chains
')" "/mnt/chains"
    chk 'genuinely absent -> empty'   "$(mdl 'PRUNE_ENABLED=true
')" ""
fi

echo ""
echo "═══════════════════════════════════════════════════════════"
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "═══════════════════════════════════════════════════════════"
[[ $FAILED -eq 0 ]] || exit 1
exit 0
