#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Upstream release detection in coin-upgrade.sh.
#
# COIN_TARGET is a static table shipped with each Spiral Pool release, so asking
# only "is this daemon at its target?" answers "yes" forever no matter what a
# coin's developers publish afterwards. The upstream check closes that, and it
# has to work for projects that do NOT publish on GitHub as well as those that
# do: Bitcoin's binaries come from bitcoincore.org, not from a GitHub release.
#
# Every test stubs _http_get. Nothing touches the network -- release feeds
# change without warning, rate-limit, and go down, none of which should ever
# turn this suite red.
#
# The rule the assertions encode: an unrecognised shape produces SILENCE, never
# a guess. A missed release costs an upgrade cycle; a fabricated one sends an
# operator to reindex a consensus-critical daemon for nothing.
#
# Usage: bash tests/test_coin_upstream.sh
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

# Load the real bodies of the functions under test, so this exercises shipped
# code rather than a copy that can drift from it.
LIB="$TMP/lib.sh"
{
    # The same options coin-upgrade.sh runs under. Testing these functions under
    # plain `set -u` would hide every set -e landmine in them: an unguarded
    # non-zero status that is harmless in the test shell aborts the real script.
    echo 'set -euo pipefail'
    sed -n '/^ALL_COINS=(/p' "$CU"
    sed -n '/^declare -A COIN_TARGET=(/,/^)/p' "$CU"
    sed -n '/^declare -A COIN_UPSTREAM=(/,/^)/p' "$CU"
    echo "VERSION_CACHE_DIR=\"$TMP/cache\""
    echo 'UPSTREAM_CACHE_TTL=86400'
    for f in _norm4 _ver_matches _ver_gt _is_stable_version _tag_to_version \
             _upstream_gh _upstream_idx _upstream_latest _coin_present_for_upstream \
             _collect_upstream list_upstream check_upstream _print_unreachable; do
        sed -n "/^${f}() {/,/^}/p" "$CU"
    done
    echo 'UPSTREAM_ROWS=(); UPSTREAM_UNREACHABLE=()'
    echo 'get_installed_version() { echo "0.0.1"; }'
    # HTTP_FIXTURE, not "page" or "body": bash is dynamically scoped, and the
    # functions under test declare local page / local body, which would shadow a
    # fixture of that name and hand the stub an empty string instead.
    echo 'HTTP_FIXTURE=""; HTTP_FAIL=0'
    echo '_http_get() { [[ "$HTTP_FAIL" == "1" ]] && return 1; printf "%s" "$HTTP_FIXTURE"; }'
} > "$LIB"

# shellcheck disable=SC1090
source "$LIB"

fresh() { rm -rf "$TMP/cache"; HTTP_FAIL=0; }
gh_json() { printf '{"tag_name": "%s", "prerelease": false}' "$1"; }

log_test "a stable release is a plain dotted number of two to four parts"
for good in 31.1 31.1.0 0.21.5.8 9.26.5; do
    if _is_stable_version "$good"; then pass "accepts $good"; else fail "accepts $good"; fi
done
for bad in 31 1.2.3.4.5 20260101 31.1.0-rc1; do
    if _is_stable_version "$bad"; then
        fail "rejects $bad" "a shape it cannot compare must not be treated as a version"
    else
        pass "rejects $bad"
    fi
done

log_test "the tag prefixes the fifteen coins really use"
eq "$(_tag_to_version v31.1)"         "31.1"     "v31.1 becomes 31.1"
eq "$(_tag_to_version version31.1.3)" "31.1.3"   "version31.1.3 becomes 31.1.3 (Bitcoin Silver)"
eq "$(_tag_to_version nc31.1)"        "31.1"     "nc31.1 becomes 31.1 (Namecoin)"
eq "$(_tag_to_version v0.18.1.0)"     "0.18.1.0" "a four-part tag survives"

log_test "GitHub: release candidates are rejected, stable releases are not"
for tag in v29.2.0rc1 v0.21.5-rc1 31.0rc2 v1.0.0-beta v2.0.0-alpha.1 v31.1-pre v31.1.0-dev nightly-20260101 v31.1.0+build5; do
    fresh; HTTP_FIXTURE="$(gh_json "$tag")"
    if got=$(_upstream_gh "x/y"); then fail "rejects $tag" "accepted it as [$got]"; else pass "rejects $tag"; fi
done
fresh; HTTP_FIXTURE="$(gh_json v31.1)"
eq "$(_upstream_gh x/y)" "31.1" "accepts the stable tag v31.1"

log_test "a project that does not publish on GitHub is still checked"
fresh; HTTP_FIXTURE="bitcoin-core-29.1/ bitcoin-core-31.1/ bitcoin-core-30.3/"
eq "$(_upstream_idx 'https://x|bitcoin-core-')" "31.1" "picks the newest entry, not the last listed"

fresh; HTTP_FIXTURE="bitcoin-core-9.99/ bitcoin-core-10.0/"
eq "$(_upstream_idx 'https://x|bitcoin-core-')" "10.0" "10.0 beats 9.99 (integer, not string, compare)"

# The one that bit: a greedy match stops at the first non-digit, so an RC
# directory laundered itself into a stable-looking version number.
fresh; HTTP_FIXTURE="bitcoin-core-31.1/ bitcoin-core-32.0rc1/ bitcoin-core-32.0-rc2/"
eq "$(_upstream_idx 'https://x|bitcoin-core-')" "31.1" "an RC directory is not truncated into a stable version"

fresh; HTTP_FIXTURE="bitcoin-core-32.0rc1/"
if got=$(_upstream_idx 'https://x|bitcoin-core-'); then fail "a page of only RCs reports nothing" "returned [$got]"; else pass "a page of only RCs reports nothing"; fi

fresh; HTTP_FIXTURE=""
if got=$(_upstream_idx 'https://x|bitcoin-core-'); then fail "an empty page reports nothing" "returned [$got]"; else pass "an empty page reports nothing"; fi

fresh; HTTP_FIXTURE="no versions here at all"
if got=$(_upstream_idx 'https://x|bitcoin-core-'); then fail "an unparseable page reports nothing" "returned [$got]"; else pass "an unparseable page reports nothing"; fi

log_test "a malformed or unusable source degrades quietly"
fresh; HTTP_FIXTURE="bitcoin-core-31.1/"
# Assign inside `if`, not as a bare statement: under set -e a bare assignment
# from a command substitution that exits non-zero kills the script before $?
# can be read, which is exactly how this suite aborted when it was first run
# under the shell options coin-upgrade.sh actually uses.
if err="$(_upstream_idx 'https://example.com/bin/' 2>&1 >/dev/null)"; then
    fail "a source with no prefix separator reports nothing" "it returned a version"
else
    pass "a source with no prefix separator reports nothing"
fi
if [[ -n "$err" ]]; then
    fail "a malformed source prints no error" "stderr: $err"
else
    pass "a malformed source prints no error"
fi

# The cache is written by root (this script) and by the pool user (Sentinel).
# An unwritable cache must degrade to "ask every time", never to "no answer".
fresh; HTTP_FIXTURE="$(gh_json v0.21.5.8)"
OLD_CACHE="$VERSION_CACHE_DIR"
VERSION_CACHE_DIR="/proc/nonexistent-cannot-mkdir"
eq "$(_upstream_latest LTC)" "0.21.5.8" "an unwritable cache still returns the answer"
VERSION_CACHE_DIR="$OLD_CACHE"

log_test "each coin is asked in the way its project publishes"
fresh; HTTP_FIXTURE="bitcoin-core-31.1/"
eq "$(_upstream_latest BTC)" "31.1" "BTC resolves through the non-GitHub index"
fresh; HTTP_FIXTURE="$(gh_json v0.21.5.8)"
eq "$(_upstream_latest LTC)" "0.21.5.8" "LTC resolves through the GitHub API"

log_test "an answer is cached, and a failure is not cached as an answer"
fresh; HTTP_FIXTURE="$(gh_json v0.21.5.8)"
_upstream_latest LTC >/dev/null
HTTP_FAIL=1
eq "$(_upstream_latest LTC)" "0.21.5.8" "a cached answer survives the feed going away"
fresh; HTTP_FAIL=1
if got=$(_upstream_latest LTC); then fail "a failed fetch is not cached as an answer" "returned [$got]"; else pass "a failed fetch is not cached as an answer"; fi

log_test "a cache entry is re-validated on the way out"
# The writer validates, but the file outlives the process that wrote it: it is
# state on disk owned by the pool user. A malformed entry must be ignored and
# refetched, not handed on as a release. (A well-formed but untrue value cannot
# be detected by shape -- its defences are the validated atomic write and the
# directory ownership, not this check.)
fresh; mkdir -p "$VERSION_CACHE_DIR/upstream"
printf 'not-a-version' > "$VERSION_CACHE_DIR/upstream/LTC.latest"
HTTP_FAIL=1
if got=$(_upstream_latest LTC); then
    fail "garbage in the cache is not served as a version" "returned [$got]"
else
    pass "garbage in the cache is not served as a version"
fi

fresh; mkdir -p "$VERSION_CACHE_DIR/upstream"
printf '31.1rc1' > "$VERSION_CACHE_DIR/upstream/LTC.latest"
HTTP_FAIL=1
if got=$(_upstream_latest LTC); then
    fail "a release candidate in the cache is not served" "returned [$got]"
else
    pass "a release candidate in the cache is not served"
fi

# And a good entry is still served from cache without a fetch.
fresh; mkdir -p "$VERSION_CACHE_DIR/upstream"
printf '0.22.0' > "$VERSION_CACHE_DIR/upstream/LTC.latest"
HTTP_FAIL=1
eq "$(_upstream_latest LTC)" "0.22.0" "a valid cache entry is served without a fetch"

log_test "could-not-ask is never reported as nothing-new"
fresh; HTTP_FAIL=1
out="$(check_upstream)"
if grep -q "Could not check" <<< "$out"; then
    pass "a total outage says so explicitly"
else
    fail "a total outage says so explicitly" "$(head -3 <<< "$out")"
fi
if grep -q "No coin daemon has a newer stable release" <<< "$out"; then
    fail "a total outage does not claim everything is current" "it printed the all-clear while unable to reach any feed"
else
    pass "a total outage does not claim everything is current"
fi

log_test "a release is reported only when it is actually newer than the target"
fresh; HTTP_FIXTURE="$(gh_json v0.21.5.8)"
if list_upstream | grep -q "^LTC "; then fail "a target already at upstream reports nothing"; else pass "a target already at upstream reports nothing"; fi
fresh; HTTP_FIXTURE="$(gh_json v0.21.5.6)"
if list_upstream | grep -q "^LTC "; then fail "an older upstream release reports nothing"; else pass "an older upstream release reports nothing"; fi
fresh; HTTP_FIXTURE="$(gh_json v0.22.0)"
eq "$(list_upstream | grep '^LTC ' || true)" "LTC 0.21.5.8 0.22.0" "a newer release is reported with both versions"

log_test "the machine-readable mode distinguishes a dead feed from no news"
# check_upstream says "Could not check" to a human, but list_upstream -- the mode
# Sentinel actually consumes -- used to print only the rows it found. A feed that
# never answered left it completely silent, identical to a feed that answered
# with nothing newer, so a check broken for weeks looked exactly like a healthy
# one. The script still exits 0 in that case, so the exit code cannot carry it.
fresh; HTTP_FAIL=1
out="$(list_upstream)"
if grep -q "^unreachable " <<< "$out"; then
    pass "a feed that did not answer is named in the machine-readable output"
else
    fail "a feed that did not answer is named in the machine-readable output" "got [$out]"
fi
if grep -qE "^[A-Z0-9_]+ [0-9]" <<< "$out"; then
    fail "an unreachable feed is not emitted as a release row" "$out"
else
    pass "an unreachable feed is not emitted as a release row"
fi

# The two must stay tellable apart in the same run: a real row is three fields,
# an unreachable marker is two and starts with a word no coin symbol can be.
fresh; HTTP_FIXTURE="$(gh_json v0.22.0)"
out="$(list_upstream)"
# Scoped to LTC on purpose: the fixture is GitHub-shaped, so BTC -- whose source
# is an HTTP directory index, not GitHub -- genuinely cannot resolve from it and
# is correctly marked unreachable in the same run. That is the feature working,
# and a blanket "no markers at all" assertion would call it a failure.
eq "$(grep -c '^unreachable LTC$' <<< "$out" || true)" "0" "a reachable feed emits no unreachable marker for that coin"
if grep -q '^unreachable BTC$' <<< "$out"; then
    pass "a coin whose source cannot parse the response is marked unreachable, not silently dropped"
else
    fail "a coin whose source cannot parse the response is marked unreachable, not silently dropped" "$out"
fi
eq "$(grep '^LTC ' <<< "$out" || true)" "LTC 0.21.5.8 0.22.0" "and the release row is unchanged by the addition"

log_test "which coins the check asks about, where no daemon binary can be seen"
# The Sentinel container ships this script but none of the fifteen daemons, so
# get_installed_version answers "not_installed" for every coin and the whole
# check goes silent — indistinguishable from a healthy run with no news. That
# is the failure this override exists to remove, and these assertions are what
# stop it coming back.
#
# The harness stubs get_installed_version to a version, so "binary present" is
# the default here; a stub returning not_installed stands in for the container.
fresh
unset SPIRALPOOL_INSTALLED_COINS
if _coin_present_for_upstream DGB; then
    pass "with no override, a coin whose binary answers is present"
else
    fail "with no override, a coin whose binary answers is present"
fi

get_installed_version() { echo "not_installed"; }
if _coin_present_for_upstream DGB; then
    fail "with no override, a coin with no binary is absent"
else
    pass "with no override, a coin with no binary is absent"
fi

# From here the binary is invisible — the container's situation exactly.
SPIRALPOOL_INSTALLED_COINS="DGB,BTC"
if _coin_present_for_upstream DGB; then
    pass "a named coin is present even with no binary to ask"
else
    fail "a named coin is present even with no binary to ask"
fi
if _coin_present_for_upstream LTC; then
    fail "a coin the override does not name stays absent"
else
    pass "a coin the override does not name stays absent"
fi

# Compose profiles are lowercase (dgb, btc); the table is uppercase.
SPIRALPOOL_INSTALLED_COINS="dgb btc"
if _coin_present_for_upstream DGB; then
    pass "whitespace-separated and lowercase are both accepted"
else
    fail "whitespace-separated and lowercase are both accepted"
fi

# `--profile multi` and `--profile dgb-scrypt` are legitimate profile names
# that are not tickers. They must be ignored, not matched to something.
SPIRALPOOL_INSTALLED_COINS="multi,dgb-scrypt"
_unknown_matched=0
for _c in "${ALL_COINS[@]}"; do
    _coin_present_for_upstream "$_c" && _unknown_matched=1
done
eq "$_unknown_matched" "0" "profile names that are not tickers match no coin"

# An empty value must mean "no override", not "no coins" — otherwise a compose
# file with the variable defined but unset would switch the check off on a
# bare-metal host that had been working.
SPIRALPOOL_INSTALLED_COINS=""
get_installed_version() { echo "9.26.5"; }
if _coin_present_for_upstream DGB; then
    pass "an empty override falls back to asking the binary"
else
    fail "an empty override falls back to asking the binary"
fi

# End to end: the whole check runs off the override with no binary anywhere,
# which is what a Docker pool will actually do.
fresh
get_installed_version() { echo "not_installed"; }
SPIRALPOOL_INSTALLED_COINS="LTC"
HTTP_FIXTURE="$(gh_json v0.22.0)"
out="$(list_upstream)"
eq "$out" "LTC 0.21.5.8 0.22.0" "with no binaries at all, the named coin is still checked and reported"

SPIRALPOOL_INSTALLED_COINS="DOGE"
out="$(list_upstream)"
if grep -q '^LTC ' <<< "$out"; then
    fail "a coin outside the override is not reported" "$out"
else
    pass "a coin outside the override is not reported"
fi

unset SPIRALPOOL_INSTALLED_COINS
get_installed_version() { echo "0.0.1"; }

echo ""
echo "==========================================================="
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "==========================================================="
[[ $FAILED -eq 0 ]] || exit 1
exit 0
