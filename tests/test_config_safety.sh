#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool — Daemon Config Safety Tests
# =============================================================================
# Pins the behaviour of every place that READS or REWRITES an operator's
# bitcoin.conf. These functions run unattended as root against files the
# operator may have hand-edited, so a parsing mistake here does not produce a
# stack trace — it produces a daemon that will not start, or a node that has
# irreversibly deleted its own block data.
#
# Bitcoin Core's config format is the reason these tests exist. Three of its
# rules were each violated by a plain '^key=' grep:
#   - Options under a [main]/[test]/[regtest] header apply ONLY to that network
#   - Whitespace is legal around the key and the '='
#   - When a key is assigned twice, the FIRST assignment wins
#
# Regressions pinned here, all observed in v2.7.0 development:
#   1. get_existing_prune returned every match, and the value is interpolated
#      into the generated config as "prune=$EXISTING_PRUNE" — so two prune
#      lines emitted a bare "1000" line with no '=', which is a fatal parse
#      error the daemon does not come back from.
#   2. The same regex missed "prune = 5000" and rewrote a PRUNED node to
#      prune=0, which Core refuses to start against a pruned datadir.
#   3. It also copied a [test]-scoped prune to the top level, enabling
#      irreversible block deletion on a full mainnet node.
#   4. rightsize_daemon_resources' multi-line value made [[ -gt ]] throw an
#      arithmetic error, silently skipping the RAM cap while reporting OK.
#   5. ensure_daemon_peer_config appended mainnet peers into a trailing [test]
#      section, then matched its own misplaced line forever after.
#   6. pool-mode config backups were keyed on basename alone, so btc, bch and
#      xec (all "bitcoin.conf") overwrote each other.
#   7. dgb_enable_pruning_config edited the config even when its backup failed,
#      and was not section aware.
#
# These tests source the REAL functions out of the REAL scripts rather than
# reimplementing them, so they fail if the shipping code changes.
#
# Usage: bash tests/test_config_safety.sh
#        bash tests/test_config_safety.sh --verbose
#
# Exit codes:
#   0  All tests passed
#   1  One or more tests failed
# =============================================================================

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
POOL_MODE="$PROJECT_ROOT/scripts/linux/pool-mode.sh"
UPGRADE="$PROJECT_ROOT/upgrade.sh"
COIN_UPGRADE="$PROJECT_ROOT/coin-upgrade.sh"

TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
VERBOSE="${1:-}"

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

log_test() { echo -e "${CYAN}[TEST]${NC} $1"; }

pass() {
    TESTS_RUN=$((TESTS_RUN + 1)); TESTS_PASSED=$((TESTS_PASSED + 1))
    echo -e "  ${GREEN}PASS${NC}: $1"
}

fail() {
    TESTS_RUN=$((TESTS_RUN + 1)); TESTS_FAILED=$((TESTS_FAILED + 1))
    echo -e "  ${RED}FAIL${NC}: $1"
    [[ -n "$2" ]] && echo -e "        $2"
}

assert_eq() {
    local expected="$1" actual="$2" desc="$3"
    if [[ "$expected" == "$actual" ]]; then
        pass "$desc"
    else
        fail "$desc" "expected [${expected}] got [${actual}]"
    fi
}

# -----------------------------------------------------------------------------
# Extract a function verbatim from a shipping script.
#
# Matches the closing brace at the SAME indentation as the declaration, so
# nested helpers (which are indented inside their parent) extract correctly.
# -----------------------------------------------------------------------------
extract_fn() {
    local file="$1" name="$2"
    awk -v fn="$name" '
        !inside {
            if ($0 ~ "^[[:space:]]*" fn "\\(\\) \\{") {
                match($0, /^[[:space:]]*/)
                indent = substr($0, 1, RLENGTH)
                inside = 1
                print
            }
            next
        }
        { print }
        $0 == indent "}" { exit }
    ' "$file"
}

require_fn() {
    local body="$1" name="$2"
    if [[ -z "$body" ]]; then
        echo -e "${RED}FATAL${NC}: could not extract ${name}() — the function was renamed or removed."
        exit 1
    fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# =============================================================================
# A. get_existing_prune  (scripts/linux/pool-mode.sh)
#
# Its result is interpolated straight into the generated config as
# "prune=$EXISTING_PRUNE", so anything other than a single integer is a config
# the daemon cannot parse.
# =============================================================================
log_test "get_existing_prune — real function from pool-mode.sh"

GEP_BODY="$(extract_fn "$POOL_MODE" get_existing_prune)"
require_fn "$GEP_BODY" get_existing_prune

# $2 selects the pool-wide PRUNE_ENABLED setting: "true" builds a coins.env,
# anything else points SPIRALPOOL_DIR at a nonexistent root so the function
# takes its "false" default.
#
# This parameter exists because every assertion here used to run against a
# nonexistent root, so global_prune was ALWAYS "false" and the whole
# PRUNE_ENABLED=true branch was untested. That is the only branch in which a
# wrong answer is destructive — it is where an unparseable or absent prune value
# could inherit the pool-wide 5000 and start deleting blocks on an archival
# node — and it passed the suite by never being reached.
run_get_existing_prune() {
    local conf="$1" global="${2:-false}" root
    if [[ "$global" == "true" ]]; then
        root="$WORK/prune-root"
        mkdir -p "$root/config"
        printf 'PRUNE_ENABLED="true"\n' > "$root/config/coins.env"
    else
        root="$WORK/nonexistent-root"
    fi
    bash -c '
        set -u
        SPIRALPOOL_DIR="'"$root"'"
        YELLOW=""; NC=""
        '"$GEP_BODY"'
        get_existing_prune "'"$conf"'"
        printf "%s" "$EXISTING_PRUNE"
    '
}

# 1. Duplicate key. The old code returned "5000\n1000", and the generated
#    config then carried a bare "1000" line with no '=' — fatal to the daemon.
cat > "$WORK/dup.conf" <<'EOF'
chain=main
prune=5000

[test]
prune=1000
EOF
out="$(run_get_existing_prune "$WORK/dup.conf")"
assert_eq "5000" "$out" "duplicate prune= takes the first, top-level value only"

# The consequence, asserted directly: every emitted line must be key=value.
printf 'prune=%s\n' "$out" > "$WORK/emitted.conf"
bad="$(grep -vc '=' "$WORK/emitted.conf" || true)"
assert_eq "0" "$bad" "generated prune line always contains '=' (daemon can parse it)"

# 2. Core-legal whitespace. Missing this rewrote a PRUNED node to prune=0,
#    which Core refuses to start against a pruned datadir without a resync.
printf 'chain=main\nprune = 5000\n' > "$WORK/ws.conf"
out="$(run_get_existing_prune "$WORK/ws.conf")"
assert_eq "5000" "$out" "whitespace around '=' is tolerated (pruned node stays pruned)"

# 3. Section scoping. A [test] prune must NOT be promoted to mainnet — doing so
#    enables irreversible block deletion on a full archival node.
printf 'chain=main\ntxindex=1\n\n[test]\nprune=1000\n' > "$WORK/sect.conf"
out="$(run_get_existing_prune "$WORK/sect.conf")"
assert_eq "0" "$out" "[test]-scoped prune is not applied to mainnet"

# 4. CRLF must not leak a \r into the value.
printf 'chain=main\r\nprune=5000\r\n' > "$WORK/crlf.conf"
out="$(run_get_existing_prune "$WORK/crlf.conf")"
assert_eq "5000" "$out" "CRLF config yields a clean integer (no trailing CR)"

# 5. Non-numeric junk is ignored rather than propagated into the config.
printf 'chain=main\nprune=abc\n' > "$WORK/junk.conf"
out="$(run_get_existing_prune "$WORK/junk.conf")"
assert_eq "0" "$out" "non-integer prune value is ignored, not propagated"

# =============================================================================
# B. _conf_int  (upgrade.sh, inside rightsize_daemon_resources)
#
# A multi-line value made [[ -gt ]] throw an arithmetic error, so the RAM cap
# was skipped while the run reported "All daemon resource limits OK".
# =============================================================================
log_test "_conf_int — real helper from upgrade.sh rightsize_daemon_resources"

CI_BODY="$(extract_fn "$UPGRADE" _conf_int)"
require_fn "$CI_BODY" _conf_int

run_conf_int() {
    local conf="$1" key="$2"
    bash -c '
        set -u
        conf_path="'"$conf"'"
        '"$CI_BODY"'
        _conf_int "'"$key"'"
    '
}

# 6. The exact shape that silently disabled the cap: same key at top level and
#    under [main]. Must yield ONE value so the comparison can run.
cat > "$WORK/rs.conf" <<'EOF'
chain=main
dbcache=8192
maxconnections=256

[main]
dbcache=8192
maxconnections=256
EOF
out="$(run_conf_int "$WORK/rs.conf" dbcache)"
assert_eq "8192" "$out" "duplicate dbcache across sections yields a single value"

# 7. Prove the arithmetic comparison the caller performs actually works now.
#    Under the old code this emitted "arithmetic syntax error" and evaluated
#    false, so an 8 GB dbcache sailed past a 4 GB cap.
cmp_out="$(bash -c '
    set -u
    conf_path="'"$WORK"'/rs.conf"
    '"$CI_BODY"'
    cur=$(_conf_int dbcache); [[ -n "$cur" ]] || cur=0
    if [[ "$cur" -gt 4096 ]]; then echo "CAPPED"; else echo "NOT_CAPPED"; fi
' 2>"$WORK/cmp.err")"
assert_eq "CAPPED" "$cmp_out" "over-limit dbcache is detected (cap is not silently skipped)"
assert_eq "" "$(cat "$WORK/cmp.err")" "comparison emits no arithmetic error on stderr"

# 8. A value that exists only under [test] must not be read as a mainnet value.
printf 'chain=main\nserver=1\n\n[test]\ndbcache=16384\n' > "$WORK/rs2.conf"
out="$(run_conf_int "$WORK/rs2.conf" dbcache)"
assert_eq "" "$out" "[test]-scoped dbcache is not read as a mainnet setting"

# =============================================================================
# C. _toplevel / _add_toplevel  (upgrade.sh, inside ensure_daemon_peer_config)
#
# These write MAINNET peer settings. A plain >> put them under a trailing
# [test] header, where mainnet never sees them — and the guard then matched
# its own misplaced line and reported "up to date" forever.
# =============================================================================
log_test "_add_toplevel — real helpers from upgrade.sh ensure_daemon_peer_config"

TL_BODY="$(extract_fn "$UPGRADE" _mainnet_scope)"
AT_BODY="$(extract_fn "$UPGRADE" _add_toplevel)"
require_fn "$TL_BODY" _mainnet_scope
require_fn "$AT_BODY" _add_toplevel

printf 'chain=main\nserver=1\n\n[test]\nrpcport=18332\n' > "$WORK/peer.conf"
bash -c '
    set -u
    conf_path="'"$WORK"'/peer.conf"
    '"$TL_BODY"'
    '"$AT_BODY"'
    _add_toplevel "forcednsseed=1"
    _add_toplevel "addnode=1.2.3.4:8333"
' >/dev/null

# 9. The settings must land ABOVE the [test] header.
hdr_line=$(grep -n '^\[test\]' "$WORK/peer.conf" | cut -d: -f1)
peer_line=$(grep -n '^addnode=1.2.3.4:8333' "$WORK/peer.conf" | cut -d: -f1)
if [[ -n "$hdr_line" && -n "$peer_line" && "$peer_line" -lt "$hdr_line" ]]; then
    pass "mainnet peers are inserted before the [test] header, not appended after it"
else
    fail "mainnet peers are inserted before the [test] header, not appended after it" \
         "[test] at line ${hdr_line:-none}, addnode at line ${peer_line:-none}"
fi

# 10. The operator's [test] section must survive intact.
assert_eq "rpcport=18332" "$(tail -1 "$WORK/peer.conf")" \
    "the existing [test] section is preserved below the insertion"

# 11. Idempotent: the guard must see the line it wrote, so a second run is a
#     no-op rather than a duplicate.
before="$(md5sum < "$WORK/peer.conf")"
bash -c '
    set -u
    conf_path="'"$WORK"'/peer.conf"
    '"$TL_BODY"'
    '"$AT_BODY"'
    if ! _mainnet_scope | grep -q "^[[:space:]]*forcednsseed[[:space:]]*=[[:space:]]*1"; then
        _add_toplevel "forcednsseed=1"
    fi
' >/dev/null
assert_eq "$before" "$(md5sum < "$WORK/peer.conf")" "re-running is a no-op (no duplicate entries)"

# 12. A config with no sections at all still works.
printf 'chain=main\n' > "$WORK/nosect.conf"
bash -c '
    set -u
    conf_path="'"$WORK"'/nosect.conf"
    '"$TL_BODY"'
    '"$AT_BODY"'
    _add_toplevel "forcednsseed=1"
' >/dev/null
assert_eq "forcednsseed=1" "$(tail -1 "$WORK/nosect.conf")" \
    "a section-less config still receives the setting"

# =============================================================================
# D. _backup_coin_conf  (scripts/linux/pool-mode.sh)
#
# btc, bch and xec all use a file named "bitcoin.conf". Keyed on basename
# alone, one coin's backup silently overwrote another's.
# =============================================================================
log_test "_backup_coin_conf — real function from pool-mode.sh"

BK_BODY="$(extract_fn "$POOL_MODE" _backup_coin_conf)"
require_fn "$BK_BODY" _backup_coin_conf

mkdir -p "$WORK/sp/btc" "$WORK/sp/bch"
echo "rpcpassword=btc-secret" > "$WORK/sp/btc/bitcoin.conf"
echo "rpcpassword=bch-secret" > "$WORK/sp/bch/bitcoin.conf"

bash -c '
    set -u
    SPIRALPOOL_DIR="'"$WORK"'/sp"
    GREEN=""; RED=""; NC=""
    '"$BK_BODY"'
    _backup_coin_conf "$SPIRALPOOL_DIR/btc/bitcoin.conf"
    _backup_coin_conf "$SPIRALPOOL_DIR/bch/bitcoin.conf"
' >/dev/null

count=$(find "$WORK/sp/backups/pool-mode-configs" -type f 2>/dev/null | wc -l | tr -d ' ')
assert_eq "2" "$count" "two coins named bitcoin.conf produce two distinct backups"

# 13. And the contents must not have been cross-clobbered.
if grep -rq 'btc-secret' "$WORK/sp/backups/pool-mode-configs" 2>/dev/null \
   && grep -rq 'bch-secret' "$WORK/sp/backups/pool-mode-configs" 2>/dev/null; then
    pass "each coin's config content is preserved in its own backup"
else
    fail "each coin's config content is preserved in its own backup" \
         "one backup overwrote the other"
fi

# 14. Backups hold rpcpassword in plaintext and must never be replicated by
#     ha-replicate.sh. Assert the exclude that protects them still exists.
if grep -q '\-\-exclude="\*\.bak-\*"' "$PROJECT_ROOT/scripts/linux/ha-replicate.sh"; then
    pass "ha-replicate.sh still excludes *.bak-* from replication"
else
    fail "ha-replicate.sh still excludes *.bak-* from replication" \
         "credential-bearing backups would be copied to the HA peer"
fi

# =============================================================================
# E. dgb_enable_pruning_config  (coin-upgrade.sh)
#
# The only config-mutating path in coin-upgrade.sh. It deletes lines and then
# removes the transaction index, so proceeding without a backup, or applying a
# non-section-aware edit, is not recoverable.
# =============================================================================
log_test "dgb_enable_pruning_config — real function from coin-upgrade.sh"

DGB_BODY="$(extract_fn "$COIN_UPGRADE" dgb_enable_pruning_config)"
require_fn "$DGB_BODY" dgb_enable_pruning_config

dgb_harness() {
    local conf="$1" force_cp_fail="$2"
    bash -c '
        set -u
        log_info() { :; }; log_warn() { :; }; log_error() { :; }
        log_success() { :; }; CYAN=""; NC=""; DIM=""
        get_data_dir() { echo "'"$WORK"'/dgbdata"; }
        POOL_USER="$(id -un)"
        BACKUP_ROOT="'"$WORK"'/backups"
        declare -A COIN_CONF=( [DGB]="'"$conf"'" )
        if [[ "'"$force_cp_fail"'" == "yes" ]]; then cp() { return 1; }; fi
        '"$DGB_BODY"'
        dgb_enable_pruning_config
        echo "SPIRAL_RC=$?"
    ' 2>/dev/null | grep '^SPIRAL_RC=' | sed 's/^SPIRAL_RC=/RC=/'
}

# 15. Section-bearing config must be handed back UNCHANGED, not half-edited.
cat > "$WORK/dgb-sect.conf" <<'EOF'
server=1
txindex=1
rpcpassword=secret

[test]
rpcport=14023
EOF
before="$(md5sum < "$WORK/dgb-sect.conf")"
rc="$(dgb_harness "$WORK/dgb-sect.conf" no)"
assert_eq "RC=0" "$rc" "section-bearing config is declined without erroring the upgrade"
assert_eq "$before" "$(md5sum < "$WORK/dgb-sect.conf")" \
    "section-bearing config is left byte-identical (txindex not stripped)"

# 16. Backup failure must abort BEFORE any edit. Previously the warning was
#     logged and the deletes ran anyway, with no copy on disk.
printf 'server=1\ntxindex=1\nrpcpassword=secret\n' > "$WORK/dgb-flat.conf"
before="$(md5sum < "$WORK/dgb-flat.conf")"
rc="$(dgb_harness "$WORK/dgb-flat.conf" yes)"
assert_eq "RC=1" "$rc" "a failed backup aborts with a non-zero status"
assert_eq "$before" "$(md5sum < "$WORK/dgb-flat.conf")" \
    "a failed backup leaves the config untouched (fails closed)"

# 17. The happy path still works: flat config, backup succeeds, pruning applied.
mkdir -p "$WORK/dgbdata/indexes/txindex"
rc="$(dgb_harness "$WORK/dgb-flat.conf" no)"
assert_eq "RC=0" "$rc" "flat config: pruning is applied successfully"
if grep -q '^prune=5000' "$WORK/dgb-flat.conf" && ! grep -q '^txindex=' "$WORK/dgb-flat.conf"; then
    pass "flat config: prune=5000 added and txindex removed"
else
    fail "flat config: prune=5000 added and txindex removed" \
         "$(cat "$WORK/dgb-flat.conf")"
fi
if grep -q 'rpcpassword=secret' "$WORK/dgb-flat.conf"; then
    pass "unrelated settings (rpcpassword) survive the edit"
else
    fail "unrelated settings (rpcpassword) survive the edit" "rpcpassword was lost"
fi

# 18. The backup is credential-bearing; it must not be world/group readable.
bakfile="$(find "$WORK/backups" -name '*.bak' -type f 2>/dev/null | head -1)"
if [[ -n "$bakfile" ]]; then
    # Does chmod actually take effect here? MSYS/Windows silently ignores it,
    # which would otherwise report a false failure on a correct code path.
    probe="$WORK/chmod-probe"; : > "$probe"; chmod 600 "$probe" 2>/dev/null
    probe_mode="$(stat -c '%a' "$probe" 2>/dev/null || echo unknown)"
    mode="$(stat -c '%a' "$bakfile" 2>/dev/null || echo unknown)"
    if [[ "$probe_mode" != "600" ]]; then
        echo -e "  ${CYAN}SKIP${NC}: config backup mode (this filesystem ignores chmod; probe gave ${probe_mode})"
    else
        assert_eq "600" "$mode" "config backup is mode 600 (holds rpcpassword)"
    fi
else
    fail "a backup file was created before the edit" "no .bak found under $WORK/backups"
fi

# =============================================================================
# F. The WRITE path in rightsize_daemon_resources.
#
# Sections A-E only ever exercised the READ helpers. A round of review found
# that the whole suite stayed green while the write actively corrupted configs:
# `sed -i "0,/^[[:space:]]*key[[:space:]]*=/s//key=NEW/"` reuses the address
# regex for the empty `//`, and that regex stops at `=` without consuming the
# value — so the new value was PREPENDED to the old digits and compounded on
# every run. These assertions look at the resulting file bytes.
# =============================================================================
log_test "rightsize_daemon_resources — the substitutions that rewrite the file"

RS_BODY="$(extract_fn "$UPGRADE" rightsize_daemon_resources)"
require_fn "$RS_BODY" rightsize_daemon_resources

# Drive the REAL writer out of the shipping function, so this tests the code
# that ships rather than a copy of it.
SET_BODY="$(extract_fn "$UPGRADE" _conf_set_int)"
require_fn "$SET_BODY" _conf_set_int

rs_sed() {
    local conf="$1" key="$2" val="$3"
    bash -c '
        set -u
        conf_path="'"$conf"'"
        '"$SET_BODY"'
        _conf_set_int "'"$key"'" "'"$val"'"
    '
}

printf 'dbcache=16384\nmaxconnections=256\n' > "$WORK/rsw.conf"
rs_sed "$WORK/rsw.conf" dbcache 4096 >/dev/null 2>&1
assert_eq "dbcache=4096" "$(grep '^dbcache' "$WORK/rsw.conf")" \
    "dbcache is REPLACED, not prepended to the old value"

# 3 runs: the compounding failure produced 4096→409616384→4096409616384…
rs_sed "$WORK/rsw.conf" dbcache 4096 >/dev/null 2>&1
rs_sed "$WORK/rsw.conf" dbcache 4096 >/dev/null 2>&1
assert_eq "dbcache=4096" "$(grep '^dbcache' "$WORK/rsw.conf")" \
    "repeated runs do not compound the value"

# Whitespace form must not leave the old value stranded after the new one.
printf '  dbcache = 16384\n' > "$WORK/rsw2.conf"
rs_sed "$WORK/rsw2.conf" dbcache 4096 >/dev/null 2>&1
if grep -q '16384' "$WORK/rsw2.conf"; then
    fail "whitespace form is fully replaced" "old value survived: $(cat "$WORK/rsw2.conf")"
else
    pass "whitespace form is fully replaced"
fi

# Only the mainnet value may be rewritten; a [test] value is out of scope.
printf 'dbcache=16384\n\n[test]\ndbcache=512\n' > "$WORK/rsw3.conf"
rs_sed "$WORK/rsw3.conf" dbcache 4096 >/dev/null 2>&1
assert_eq "512" "$(sed -n '/^\[test\]/,$p' "$WORK/rsw3.conf" | grep -oP '^dbcache=\K.*')" \
    "a [test]-scoped value is left untouched"

# =============================================================================
# G. [main] is mainnet.
#
# The section filter originally stopped at the FIRST header of any kind, which
# treated an explicit [main] block as out of scope — so a cap or a peer setting
# written there was invisible and the run reported success.
# =============================================================================
log_test "section scoping — [main] counts as mainnet, [test] does not"

printf 'rpcuser=spiral\n\n[main]\ndbcache=16384\n' > "$WORK/sc1.conf"
assert_eq "16384" "$(run_conf_int "$WORK/sc1.conf" dbcache)" \
    "a value under [main] IS read as a mainnet setting"

printf 'rpcuser=spiral\n\n[regtest]\ndbcache=16384\n' > "$WORK/sc2.conf"
assert_eq "" "$(run_conf_int "$WORK/sc2.conf" dbcache)" \
    "a value under [regtest] is NOT read as a mainnet setting"

# [main] BEATS top-level, regardless of file order. Bitcoin Core's
# doc/bitcoin-conf.md: "Network specific options take precedence over
# non-network specific options. If multiple values for the same option are found
# with the same precedence, the first one is generally chosen."
#
# This assertion previously demanded the OPPOSITE (first-in-file wins) and so
# would have defended the bug against a correct fix. Both error directions are
# destructive: reading 0 for a pruned node regenerates a config Core refuses to
# start, and reading a prune target for an archival node begins irreversible
# block deletion.
printf 'chain=main\nprune=5000\n\n[main]\nprune=1000\n' > "$WORK/sc3.conf"
assert_eq "1000" "$(run_get_existing_prune "$WORK/sc3.conf")" \
    "[main] wins over top-level even when top-level comes first"

printf 'chain=main\n\n[main]\nprune=1000\n\nprune=5000\n' > "$WORK/sc3b.conf"
assert_eq "1000" "$(run_get_existing_prune "$WORK/sc3b.conf")" \
    "[main] wins over top-level even when [main] comes first"

# The destructive pair, stated as the operator would experience it.
printf 'chain=main\nprune=0\n\n[main]\nprune=5000\n' > "$WORK/sc3c.conf"
assert_eq "5000" "$(run_get_existing_prune "$WORK/sc3c.conf")" \
    "a pruned node is not regenerated as prune=0 (Core would refuse to start)"

printf 'chain=main\nprune=5000\n\n[main]\nprune=0\n' > "$WORK/sc3d.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/sc3d.conf")" \
    "an archival node is not regenerated as pruned (irreversible deletion)"

printf 'chain=main\n\n[main]\nprune=5000\n' > "$WORK/sc4.conf"
assert_eq "5000" "$(run_get_existing_prune "$WORK/sc4.conf")" \
    "a prune set only under [main] is preserved (pruned node stays pruned)"

# =============================================================================
# H. Commented-out peers must be re-added.
#
# The duplicate check used an unanchored `grep -qF`, so "#addnode=1.2.3.4:8333"
# matched "addnode=1.2.3.4:8333" and suppressed the add permanently — the
# opposite of what the code's own comment claimed.
# =============================================================================
log_test "peer duplicate check — anchored to whole lines"

printf 'chain=main\n#addnode=1.2.3.4:8333\n' > "$WORK/peer2.conf"
present=$(bash -c '
    conf_path="'"$WORK"'/peer2.conf"
    '"$(extract_fn "$UPGRADE" _mainnet_scope)"'
    _mainnet_scope | sed "s/^[[:space:]]*//" | grep -qxF -- "addnode=1.2.3.4:8333" && echo dup || echo fresh
')
assert_eq "fresh" "$present" "a commented-out peer does not count as already present"

printf 'chain=main\naddnode=1.2.3.4:8333\n' > "$WORK/peer3.conf"
present=$(bash -c '
    conf_path="'"$WORK"'/peer3.conf"
    '"$(extract_fn "$UPGRADE" _mainnet_scope)"'
    _mainnet_scope | sed "s/^[[:space:]]*//" | grep -qxF -- "addnode=1.2.3.4:8333" && echo dup || echo fresh
')
assert_eq "dup" "$present" "a real peer entry IS detected as already present"

# =============================================================================
# I. Findings from adversarial review. Each of these was a live defect.
# =============================================================================
log_test "adversarial cases"

# The WRITE must target the line Core actually READS. With a [main] value
# present, capping the top-level one leaves the effective value uncapped while
# logging success — the OOM this cap exists to prevent, self-concealing.
printf 'dbcache=16384\n\n[main]\ndbcache=8192\n' > "$WORK/adv1b.conf"
rs_sed "$WORK/adv1b.conf" dbcache 4096 >/dev/null 2>&1
assert_eq "4096" "$(run_conf_int "$WORK/adv1b.conf" dbcache)" \
    "the EFFECTIVE ([main]) value is capped, not the shadowed top-level one"
assert_eq "dbcache=16384" "$(sed -n '1p' "$WORK/adv1b.conf")" \
    "the shadowed top-level line is left untouched"

# The cap READ is mainnet-scoped; the WRITE must be too. With [test] before
# [main], a whole-file "first match" rewrote the TESTNET value, left mainnet
# uncapped, and still logged the cap as applied.
printf '[test]\ndbcache=300\n\n[main]\ndbcache=16384\n' > "$WORK/adv1.conf"
rs_sed "$WORK/adv1.conf" dbcache 4096 >/dev/null 2>&1
assert_eq "300" "$(sed -n '/^\[test\]/,/^\[main\]/p' "$WORK/adv1.conf" | grep -oP '^dbcache=\K.*')" \
    "a [test] value is not rewritten when [test] precedes [main]"
assert_eq "4096" "$(sed -n '/^\[main\]/,$p' "$WORK/adv1.conf" | grep -oP '^dbcache=\K.*')" \
    "the [main] value IS capped when [test] precedes it"

# Core parses prune with atoi semantics, so these all mean 5000. Rejecting them
# wrote prune=0 onto an already-pruned node, which Core refuses to start.
for v in "5000M" "5000MB" "+5000"; do
    printf 'chain=main\nprune=%s\n' "$v" > "$WORK/adv2.conf"
    assert_eq "5000" "$(run_get_existing_prune "$WORK/adv2.conf")" \
        "prune=${v} is read as 5000, the way Core reads it"
done

# First ASSIGNMENT wins, not first parseable line. Taking the first parseable
# line turned an archival node into a pruning one.
printf 'chain=main\nprune=\nprune=5000\n' > "$WORK/adv3.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/adv3.conf")" \
    "an empty first assignment wins, as in Core (archival node stays archival)"

# The generators compare this as a STRING against "0" to decide whether to emit
# txindex, so "00" silently dropped the index from a full node.
printf 'chain=main\nprune=00\n' > "$WORK/adv4.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/adv4.conf")" \
    "prune=00 normalises to 0 so txindex is still emitted"

# Appending to a config whose last line lacks a newline concatenated onto it,
# turning rpcpassword=SECRET into rpcpassword=SECRETaddnode=... — the daemon
# still starts, but every getblocktemplate then fails.
printf 'server=1\nrpcpassword=SUPERSECRET' > "$WORK/adv5.conf"
bash -c '
    set -u
    conf_path="'"$WORK"'/adv5.conf"
    '"$AT_BODY"'
    _add_toplevel "addnode=1.2.3.4:8333"
' >/dev/null 2>&1
if grep -q '^rpcpassword=SUPERSECRET$' "$WORK/adv5.conf"; then
    pass "appending to a file with no trailing newline preserves the last line"
else
    fail "appending to a file with no trailing newline preserves the last line" \
         "got: $(tail -2 "$WORK/adv5.conf" | tr '\n' '|')"
fi

# A temp file left by an interrupted rewrite holds rpcpassword, so its name must
# match ha-replicate.sh's excludes or it replicates to the HA peer.
if grep -q 'bak-tmp' "$UPGRADE"; then
    pass "rewrite temp file is named to match the HA replication excludes"
else
    fail "rewrite temp file is named to match the HA replication excludes" \
         "a plain .tmp name matches none of ha-replicate.sh's excludes"
fi

# =============================================================================
# PRUNE_ENABLED=true — the branch where a wrong answer destroys data
# =============================================================================
# Everything above runs with the pool-wide default of "false", where
# EXISTING_PRUNE starts at 0 and a parse failure is harmless. With the pool-wide
# setting ON it starts at 5000, so any input the function fails to read is
# written to the config as prune=5000 — and on a node that has been archival all
# along, the daemon then deletes block data irreversibly on next start. The rule
# under test: an EXISTING config is authoritative; only a coin with no config at
# all may take the pool-wide default.
log_test "get_existing_prune — PRUNE_ENABLED=true must not override an existing config"

printf 'server=1\ntxindex=1\nrpcport=8332\n' > "$WORK/g_noprune.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/g_noprune.conf" true)" \
    "existing config with NO prune line stays archival (Core's default), not 5000"

printf 'chain=main\nprune=\n' > "$WORK/g_empty.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/g_empty.conf" true)" \
    "empty prune= reads as 0 (atoi), not the pool-wide 5000"

printf 'chain=main\nprune=abc\n' > "$WORK/g_junk.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/g_junk.conf" true)" \
    "unparseable prune= reads as 0 (atoi), not the pool-wide 5000"

printf 'chain=main\nprune=1000\n' > "$WORK/g_keep.conf"
assert_eq "1000" "$(run_get_existing_prune "$WORK/g_keep.conf" true)" \
    "an explicit prune target is preserved, not raised to the pool-wide 5000"

printf 'chain=main\nprune=0\n' > "$WORK/g_zero.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/g_zero.conf" true)" \
    "an explicit prune=0 keeps the node archival"

printf '[test]\nprune=5000\n' > "$WORK/g_sect.conf"
assert_eq "0" "$(run_get_existing_prune "$WORK/g_sect.conf" true)" \
    "a prune under [test] is not copied up to mainnet"

# The one case that SHOULD follow the pool-wide setting: a coin with no config.
assert_eq "5000" "$(run_get_existing_prune "$WORK/g_absent.conf" true)" \
    "a coin with no config yet does take the pool-wide prune target"

# =============================================================================
echo ""
echo "═══════════════════════════════════════════════════════════"
echo -e "  Run: ${TESTS_RUN}   ${GREEN}Passed: ${TESTS_PASSED}${NC}   ${RED}Failed: ${TESTS_FAILED}${NC}"
echo "═══════════════════════════════════════════════════════════"
[[ $TESTS_FAILED -eq 0 ]] || exit 1
exit 0
