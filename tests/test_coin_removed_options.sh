#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# A coin upgrade must not leave the coin down.
#
# Bitcoin Cash Node 29.2.0 removed -excessiveblocksize and refuses to start
# while a config sets it -- and every Spiral Pool BCH config set it. A binary
# swap alone would have stopped BCH mining. Two defences, both tested here:
#
#   migrate_removed_options  comments out options the target version rejects,
#                            after a backup, before the new binary starts
#   _daemon_stays_up /       catches a daemon that dies after `systemctl start`
#   _rollback_dead_start     already returned 0, for whatever a future release
#                            breaks that the table does not know about yet,
#                            and puts the old binary back
#
# The same edit is made where coin-upgrade.sh is not involved: the Docker BCH
# entrypoint (configs kept on a volume) and pool-mode.sh's BCH install. Their
# sed expressions are lifted from the shipped files and run against the same
# fixture, so the three cannot drift apart unnoticed.
#
# Usage: bash tests/test_coin_removed_options.sh
# Exit:  0 all pass, 1 otherwise
# =============================================================================

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CU="$ROOT/coin-upgrade.sh"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'
RUN=0; PASSED=0; FAILED=0
log_test() { echo -e "${CYAN}[TEST]${NC} $1"; }
pass() { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { RUN=$((RUN+1)); FAILED=$((FAILED+1)); echo -e "  ${RED}FAIL${NC}: $1"; [[ -n "${2:-}" ]] && echo -e "    $2"; }
eq() { if [[ "$1" == "$2" ]]; then pass "$3"; else fail "$3" "got [$1], want [$2]"; fi; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Real function bodies from the shipped script, under the options it runs with.
LIB="$TMP/lib.sh"
{
    echo 'set -euo pipefail'
    sed -n '/^declare -A COIN_TARGET=(/,/^)/p' "$CU"
    sed -n '/^declare -A COIN_REMOVED_OPTIONS=(/,/^)/p' "$CU"
    for f in migrate_removed_options _daemon_stays_up _rollback_dead_start; do
        sed -n "/^${f}() {/,/^}/p" "$CU"
    done
} > "$LIB"
# shellcheck disable=SC1090
source "$LIB"
set +e   # the assertions below read non-zero returns on purpose

log_info() { :; }; log_success() { :; }; log_warn() { :; }; log_error() { :; }
POOL_USER="$(id -un)"
BACKUP_ROOT="$TMP/backups"
declare -A COIN_CONF=([BCH]="$TMP/bch/bitcoin.conf" [DGB]="$TMP/dgb/digibyte.conf")
mkdir -p "$TMP/bch" "$TMP/dgb"

FIXTURE='server=1
blockmaxsize=32000000
excessiveblocksize=32000000
  excessiveblocksize = 32000000
main.excessiveblocksize=32000000
-excessiveblocksize=32000000
noexcessiveblocksize=0
# excessiveblocksize=32000000
excessiveblocksizefoo=1
myexcessiveblocksize=1'

# Lines the daemon would read as the removed option, and the ones it would not.
LIVE=('excessiveblocksize=32000000' '  excessiveblocksize = 32000000'
      'main.excessiveblocksize=32000000' '-excessiveblocksize=32000000'
      'noexcessiveblocksize=0')
KEPT=('server=1' 'blockmaxsize=32000000' '# excessiveblocksize=32000000'
      'excessiveblocksizefoo=1' 'myexcessiveblocksize=1')

# Every line the daemon would read as the option is commented; nothing else moves.
check_migrated() { # <file> <label>
    local file="$1" label="$2" line ok=1
    for line in "${LIVE[@]}"; do
        grep -qxF -- "$line" "$file" && { ok=0; fail "$label: '$line' is no longer live"; }
    done
    [[ $ok -eq 1 ]] && pass "$label: every form the daemon reads is commented out"
    ok=1
    for line in "${KEPT[@]}"; do
        grep -qxF -- "$line" "$file" || { ok=0; fail "$label: '$line' is left alone"; }
    done
    [[ $ok -eq 1 ]] && pass "$label: other options, existing comments and look-alike names are untouched"
}

log_test "BCH 29.2.0 target lists excessiveblocksize as removed"
if [[ " ${COIN_REMOVED_OPTIONS[BCH]:-} " == *" excessiveblocksize "* ]]; then
    pass "COIN_REMOVED_OPTIONS[BCH] names excessiveblocksize"
else
    fail "COIN_REMOVED_OPTIONS[BCH] names excessiveblocksize" "got [${COIN_REMOVED_OPTIONS[BCH]:-}]"
fi

log_test "migrate_removed_options comments out the option, after a backup"
printf '%s\n' "$FIXTURE" > "${COIN_CONF[BCH]}"
migrate_removed_options BCH; rc=$?
eq "$rc" "0" "it succeeds"
check_migrated "${COIN_CONF[BCH]}" "coin-upgrade.sh"
bak=$(ls "$BACKUP_ROOT"/bch-config/*.bak 2>/dev/null | head -1)
if [[ -n "$bak" ]] && [[ "$(cat "$bak")" == "$FIXTURE" ]]; then
    pass "the backup is the untouched original"
else
    fail "the backup is the untouched original" "backup: [${bak:-none}]"
fi

log_test "a second run changes nothing"
before=$(cat "${COIN_CONF[BCH]}")
migrate_removed_options BCH; rc=$?
eq "$rc" "0" "it succeeds"
eq "$(cat "${COIN_CONF[BCH]}")" "$before" "the config is unchanged"
eq "$(ls "$BACKUP_ROOT"/bch-config/*.bak | wc -l | tr -d ' ')" "1" "no second backup is written"

log_test "a coin with nothing removed is never touched"
printf 'excessiveblocksize=1\n' > "${COIN_CONF[DGB]}"
migrate_removed_options DGB; rc=$?
eq "$rc" "0" "it succeeds"
eq "$(cat "${COIN_CONF[DGB]}")" "excessiveblocksize=1" "the config is unchanged"
if [[ -d "$BACKUP_ROOT/dgb-config" ]]; then fail "no backup is taken" "found $BACKUP_ROOT/dgb-config"; else pass "no backup is taken"; fi

log_test "no backup, no edit"
printf '%s\n' "$FIXTURE" > "${COIN_CONF[BCH]}"
BACKUP_ROOT="/proc/nonexistent-cannot-mkdir"
migrate_removed_options BCH; rc=$?
BACKUP_ROOT="$TMP/backups"
eq "$rc" "1" "it refuses, so the upgrade stops before touching the binary"
eq "$(cat "${COIN_CONF[BCH]}")" "$FIXTURE" "the config is left exactly as it was"

log_test "the Docker entrypoint and pool-mode.sh make the same edit"
# The Docker expression sits in a double-quoted string inside an echo; the
# pool-mode one in single quotes. Both are pulled out of the shipped file.
docker_expr=$(grep -oE '"s/\^\[\[:space:\]\][^"]*excessiveblocksize[^"]*"' "$ROOT/docker/Dockerfile.bitcoincash" | head -1 | tr -d '"')
poolmode_expr=$(grep -oE "'s/\^\[\[:space:\]\][^']*excessiveblocksize[^']*'" "$ROOT/scripts/linux/pool-mode.sh" | head -1 | tr -d "'")
for pair in "Docker entrypoint|$docker_expr" "pool-mode.sh|$poolmode_expr"; do
    label="${pair%%|*}"; expr="${pair#*|}"
    if [[ -z "$expr" ]]; then
        fail "$label carries the excessiveblocksize edit" "no sed expression found"
        continue
    fi
    printf '%s\n' "$FIXTURE" > "$TMP/copy.conf"
    sed -i -E "$expr" "$TMP/copy.conf"
    check_migrated "$TMP/copy.conf" "$label"
    before=$(cat "$TMP/copy.conf"); sed -i -E "$expr" "$TMP/copy.conf"
    eq "$(cat "$TMP/copy.conf")" "$before" "$label: a second start changes nothing"
done

log_test "no BCH config Spiral Pool writes sets the option"
if grep -qE '^[[:space:]]*excessiveblocksize[[:space:]]*=' "$ROOT/docker/config/bitcoincash.conf.template"; then
    fail "the Docker BCH template does not set it"
else
    pass "the Docker BCH template does not set it"
fi
# install.sh writes BCH and BCH2 configs; BCH2 is separate software that still
# accepts the option. Every remaining line must sit in a BCH2 block.
bad=$(awk '/^[[:space:]]*excessiveblocksize[[:space:]]*=/ { if (!(ctx ~ /BCH2|bitcoincashII/)) print NR } { if ($0 ~ /BCH2|bitcoincashII|BCH_|bitcoind-bch|=== BCH-SPECIFIC|Mining \(BCH/) ctx=$0 }' "$ROOT/install.sh")
eq "$bad" "" "install.sh sets it only in BCH2 configs"

log_test "_daemon_stays_up: a start that does not last is caught"
sleep() { :; }
SVC_STATE=active; SVC_RESTARTS=0
systemctl() {   # systemctl show -p <Prop> --value <svc>
    case "$3" in
        ActiveState) echo "$SVC_STATE" ;;
        NRestarts)   echo "$SVC_RESTARTS" ;;
    esac
}
_daemon_stays_up bitcoind-bch 0; eq "$?" "0" "active with no restarts is up"
SVC_STATE=activating; _daemon_stays_up bitcoind-bch 0; eq "$?" "1" "auto-restarting (activating) is not up"
SVC_STATE=failed;     _daemon_stays_up bitcoind-bch 0; eq "$?" "1" "failed is not up"
SVC_STATE=active; SVC_RESTARTS=2
_daemon_stays_up bitcoind-bch 0; eq "$?" "1" "active again after systemd restarted it is not up"
SVC_RESTARTS=""
_daemon_stays_up bitcoind-bch ""; eq "$?" "0" "no NRestarts (older systemd) falls back to the state alone"
unset -f systemctl sleep

log_test "_rollback_dead_start puts the previous binary back"
ROLLED=""; MAINT=""
sudo() { "$@"; }
journalctl() { echo "Error reading configuration file: Invalid configuration value excessiveblocksize"; }
systemctl() { :; }
rollback_coin() { ROLLED="$1 $2"; }
disable_maintenance() { MAINT=off; }
_rollback_dead_start BCH bitcoind-bch.service /backups/bch-1 >/dev/null 2>&1
eq "$ROLLED" "BCH /backups/bch-1" "rollback_coin restores the backup taken before the swap"
eq "$MAINT" "off" "maintenance mode is released"

echo ""
echo "==========================================================="
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "==========================================================="
[[ $FAILED -eq 0 ]] || exit 1
exit 0
