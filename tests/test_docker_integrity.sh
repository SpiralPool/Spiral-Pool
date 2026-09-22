#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# =============================================================================
# Spiral Pool — Docker deployment integrity
# =============================================================================
# The Docker deployment had no automated coverage of any kind. It is also the
# deployment nobody here runs day to day, which is exactly the combination that
# lets it rot quietly: a version pin drifts, an environment variable is renamed,
# a service is added without being wired in, and nothing says so until someone
# tries to bring the stack up.
#
# Everything here is STATIC. No Docker daemon, no image build, no network — so
# it runs on any machine, including the ones that will never run the containers.
# That is a deliberate trade: it cannot tell you the stack works, only that the
# files agree with each other and with the installer. The build itself still
# needs a host with Docker.
#
# The defect that prompted it: docker-compose.yml passed sixteen
# <SYMBOL>_WALLET_ADDRESS variables to the sentinel container and Sentinel read
# three of them. An operator setting BTC_POOL_ADDRESS got a container that
# dutifully forwarded it and a monitor that ignored it — no error, no warning,
# just a wallet nobody was watching.
#
# Run: bash tests/test_docker_integrity.sh
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE="$ROOT/docker/docker-compose.yml"
SENTINEL_PY="$ROOT/src/sentinel/SpiralSentinel.py"
INSTALL="$ROOT/install.sh"

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; NC='\033[0m'
RUN=0; PASSED=0; FAILED=0

log_test() { echo -e "${CYAN}[CHECK]${NC} $1"; RUN=$((RUN + 1)); }
pass()     { echo -e "  ${GREEN}PASS${NC}: $1"; PASSED=$((PASSED + 1)); }
fail()     { echo -e "  ${RED}FAIL${NC}: $1"; [[ -n "${2:-}" ]] && echo -e "$2"; FAILED=$((FAILED + 1)); }
skip()     { echo -e "  ${YELLOW}SKIP${NC}: $1"; RUN=$((RUN - 1)); }

if ! python -c "import yaml" 2>/dev/null; then
    echo "PyYAML is required for these checks (pip install pyyaml)"
    exit 1
fi

# ─────────────────────────────────────────────────────────────────────────────
echo ""
log_test "docker-compose.yml is valid YAML and every service is well formed"
# A compose file that does not parse fails at `docker compose up`, on the host,
# in front of whoever is deploying. Catching it here costs nothing.
_yaml=$(python - "$COMPOSE" <<'PY' 2>&1
import sys, yaml
try:
    doc = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    print("PARSE ERROR: %s" % e); sys.exit(1)
services = doc.get("services") or {}
if not services:
    print("no services found"); sys.exit(1)
bad = [n for n, s in services.items() if not isinstance(s, dict)]
if bad:
    print("malformed services: %s" % ", ".join(bad)); sys.exit(1)
print("OK %d" % len(services))
PY
)
if [[ "$_yaml" == OK* ]]; then
    pass "compose parses, ${_yaml#OK } services defined"
else
    fail "docker-compose.yml did not parse cleanly" "        $_yaml"
fi

# ─────────────────────────────────────────────────────────────────────────────
echo ""
log_test "every depends_on names a service that exists"
# A typo here is only reported when the stack is brought up, and then only for
# the profile that happens to include the broken service.
_deps=$(python - "$COMPOSE" <<'PY' 2>&1
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
services = doc.get("services") or {}
problems = []
for name, svc in services.items():
    dep = svc.get("depends_on") or {}
    targets = dep.keys() if isinstance(dep, dict) else dep
    for t in targets:
        if t not in services:
            problems.append("%s depends on %s, which is not a service" % (name, t))
print("\n".join(problems) if problems else "OK")
PY
)
if [[ "$_deps" == "OK" ]]; then
    pass "every depends_on target resolves to a defined service"
else
    fail "a service depends on something that does not exist" "        ${_deps//$'\n'/$'\n'        }"
fi

# ─────────────────────────────────────────────────────────────────────────────
echo ""
log_test "every variable the sentinel container is given is one Sentinel reads"
# This is the check the suite exists for. Passing a variable costs nothing and
# looks like support for it; the container starts either way. The only symptom
# of a name Sentinel does not read is that the feature quietly does nothing.
#
# Sentinel reads a variable in one of three ways: the env_overrides table, a
# direct os.environ.get(), or — since per-coin wallet addresses are resolved
# from the coins list by symbol — as <SYMBOL>_WALLET_ADDRESS for a coin in the
# config. A fourth source counts too: Sentinel runs coin-upgrade.sh as a
# subprocess, which inherits this environment, so a variable that script reads
# is genuinely in use even though no Python line mentions it.
_env=$(python - "$COMPOSE" "$SENTINEL_PY" "$ROOT/coin-upgrade.sh" <<'PY' 2>&1
import re, sys, yaml

compose, sentinel_src = sys.argv[1], sys.argv[2]
doc = yaml.safe_load(open(compose, encoding="utf-8"))
svc = (doc.get("services") or {}).get("sentinel")
if not svc:
    print("no sentinel service in docker-compose.yml"); sys.exit(1)

passed = []
for entry in svc.get("environment") or []:
    if isinstance(entry, str) and "=" in entry:
        passed.append(entry.split("=", 1)[0].strip())

src = open(sentinel_src, encoding="utf-8").read()
explicit = set(re.findall(r'["\']([A-Z][A-Z0-9_]{2,})["\']\s*:', src))
explicit |= set(re.findall(r'os\.environ\.get\(\s*["\']([A-Z][A-Z0-9_]{2,})["\']', src))

# Variables read by coin-upgrade.sh, which Sentinel runs as a subprocess with
# this environment inherited.
if len(sys.argv) > 3:
    shell = open(sys.argv[3], encoding="utf-8", errors="replace").read()
    explicit |= set(re.findall(r'\$\{([A-Z][A-Z0-9_]{2,})[:}]', shell))

# Per-coin wallet addresses are built as f"{SYMBOL}_WALLET_ADDRESS".
by_symbol = bool(re.search(r'_WALLET_ADDRESS"\)', src)) or bool(
    re.search(r'\{_sym\}_WALLET_ADDRESS', src))
symbols = set()
for m in re.finditer(r'"symbol":\s*"([A-Z0-9\-]+)"', src):
    symbols.add(m.group(1).replace("-", "_").upper())

unread = []
for name in passed:
    if name in explicit:
        continue
    if by_symbol and name.endswith("_WALLET_ADDRESS") and name[:-len("_WALLET_ADDRESS")] in symbols:
        continue
    unread.append(name)

print("\n".join(sorted(unread)) if unread else "OK %d" % len(passed))
PY
)
if [[ "$_env" == OK* ]]; then
    pass "all ${_env#OK } variables passed to the sentinel container are read by Sentinel"
else
    fail "the sentinel container is passed variables Sentinel never reads" \
         "        ${_env//$'\n'/$'\n'        }"
fi

# ─────────────────────────────────────────────────────────────────────────────
echo ""
log_test "coin daemon versions in Docker match the versions install.sh pins"
# The two deployments install the same coins from the same upstreams. When they
# disagree, a Docker pool and a bare-metal pool mine different daemon versions
# from one release — and coin-upgrade.sh's targets, which follow install.sh,
# then describe a version the containers do not run.
_vers=$(python - "$ROOT" <<'PY' 2>&1
import glob, os, re, sys
root = sys.argv[1]
installer = open(os.path.join(root, "install.sh"), encoding="utf-8", errors="replace").read()

# ARG <NAME>_VERSION=x in a Dockerfile should match <NAME>_VERSION="x" in install.sh.
problems, checked = [], 0
for path in sorted(glob.glob(os.path.join(root, "docker", "Dockerfile.*"))):
    text = open(path, encoding="utf-8", errors="replace").read()
    for name, ver in re.findall(r'^ARG\s+([A-Z0-9_]+_VERSION)=([0-9][0-9A-Za-z.\-]*)', text, re.M):
        m = re.search(r'^%s="?([0-9][0-9A-Za-z.\-]*)"?' % re.escape(name), installer, re.M)
        if not m:
            continue            # Docker-only pin; nothing in install.sh to compare against
        checked += 1
        if m.group(1) != ver:
            problems.append("%s: Docker %s, install.sh %s  (%s)"
                            % (name, ver, m.group(1), os.path.basename(path)))
print("\n".join(problems) if problems else "OK %d" % checked)
PY
)
if [[ "$_vers" == OK* ]]; then
    pass "${_vers#OK } coin versions agree between the Dockerfiles and install.sh"
else
    fail "a coin is pinned to different versions in Docker and install.sh" \
         "        ${_vers//$'\n'/$'\n'        }"
fi

# ─────────────────────────────────────────────────────────────────────────────
echo ""
log_test "the sentinel container can actually run the upstream release check"
# Four things have to line up, and three of them fail silently. The script has
# to be in the image at the path Sentinel resolves; the image needs an HTTP
# client, or every release feed reports unreachable; the service has to be told
# which coins to ask about, because it cannot see the daemons to find out; and
# the script has to honour being told. Miss any one and a Docker pool learns
# about no coin release, ever, in the same silence a healthy check produces.
_dockerfile="$ROOT/docker/Dockerfile.sentinel"
_up_problems=""
grep -qE '^COPY .*coin-upgrade\.sh /spiralpool/scripts/coin-upgrade\.sh' "$_dockerfile" \
    || _up_problems+="        Dockerfile.sentinel does not copy coin-upgrade.sh to the path Sentinel resolves"$'\n'
grep -qE 'apt-get install.*(curl|wget)' "$_dockerfile" \
    || _up_problems+="        the image installs neither curl nor wget, so every release feed is unreachable"$'\n'
grep -q 'SPIRALPOOL_INSTALLED_COINS' "$COMPOSE" \
    || _up_problems+="        docker-compose.yml never sets SPIRALPOOL_INSTALLED_COINS on the sentinel service"$'\n'
grep -aq '_coin_present_for_upstream()' "$ROOT/coin-upgrade.sh" \
    || _up_problems+="        coin-upgrade.sh no longer honours SPIRALPOOL_INSTALLED_COINS"$'\n'
if [[ -z "$_up_problems" ]]; then
    pass "the script is shipped, has an HTTP client, is told its coins, and honours being told"
else
    fail "a Docker pool would never hear about a coin release" "$_up_problems"
fi

echo ""
log_test "the container cannot use the half of coin-upgrade.sh that would not work"
# Shipping the script is safe only because --list-upstream exits before
# check_root and never touches a daemon. The APPLY half stops a systemd unit
# and swaps a binary on disk, neither of which exists here; under Docker a coin
# is upgraded by rebuilding its image. The read-only modes must stay ahead of
# the root check, or the container's calls start failing on privilege instead.
_list_line=$(grep -an -- '--list-upstream)' "$ROOT/coin-upgrade.sh" | head -1 | cut -d: -f1)
_root_line=$(grep -an '^check_root$\|^[[:space:]]*check_root$' "$ROOT/coin-upgrade.sh" | head -1 | cut -d: -f1)
if [[ -n "$_list_line" && -n "$_root_line" && "$_list_line" -lt "$_root_line" ]]; then
    pass "--list-upstream returns (line ${_list_line}) before check_root (line ${_root_line})"
else
    fail "the read-only upstream mode no longer exits ahead of check_root" \
         "        --list-upstream at [${_list_line:-not found}], check_root at [${_root_line:-not found}]"
fi

echo ""
log_test "a check that could not run is still treated as unknown, not as good news"
# Belt and braces for everything above: if the script is missing, unreadable or
# broken in a future image, Sentinel must not read the resulting silence as
# "nothing newer", and must not count it as a failing feed either — that would
# raise the version-check-failing alert about a check that never ran.
if grep -q "upstream_ok = upstream is not None" "$SENTINEL_PY" \
   && grep -q "if upstream_ok:" "$SENTINEL_PY"; then
    pass "a check that could not run stays unknown, and raises nothing"
else
    fail "the upstream_ok guard is gone — a Docker Sentinel would alert on a check it can never run"
fi

# ─────────────────────────────────────────────────────────────────────────────
echo ""
log_test "every coin daemon service reserves a healthcheck and a restart policy"
# A coin daemon that dies in a container and is not restarted takes the pool's
# templates with it. Compose will not tell you a service lacks either.
_health=$(python - "$COMPOSE" <<'PY' 2>&1
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
services = doc.get("services") or {}
problems = []
for name, svc in services.items():
    # Only the long-running pieces; one-shot helpers are exempt.
    if not (svc.get("build") or svc.get("image")):
        continue
    if not svc.get("restart"):
        problems.append("%s has no restart policy" % name)
print("\n".join(problems) if problems else "OK %d" % len(services))
PY
)
if [[ "$_health" == OK* ]]; then
    pass "all ${_health#OK } services declare a restart policy"
else
    fail "a long-running service would not come back after a crash" \
         "        ${_health//$'\n'/$'\n'        }"
fi

# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "═══════════════════════════════════════════════════════════"
echo -e "  Run: ${RUN}   ${GREEN}Passed: ${PASSED}${NC}   ${RED}Failed: ${FAILED}${NC}"
echo "═══════════════════════════════════════════════════════════"
[[ $FAILED -eq 0 ]] || exit 1
exit 0
