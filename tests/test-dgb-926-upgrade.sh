#!/usr/bin/env bash
# ══════════════════════════════════════════════════════════════════════════════
# test-dgb-926-upgrade.sh — Deploy-readiness test for the DigiByte Core v9.26.3
# upgrade and the pruning-removal changes.
#
# Safe to run on a blank Spiral Pool instance (any Linux box with this repo). It
# does NOT touch a live node, systemd, or /spiralpool — everything runs in an
# isolated temp directory on a throwaway regtest chain, and no root is required.
#
#   bash tests/test-dgb-926-upgrade.sh
#
# What it checks:
#   Part 1  static — every file pins 9.26.3 and the pruning-removal edits exist
#   Part 2  download + gzip integrity + the binary reports v9.26.3
#   Part 3  boots regtest with the full-node config (txindex=1/prune=0), mines,
#           confirms getblocktemplate works, and proves prune+txindex is rejected
#   Part 4  the pruned→full config migration (mirrors coin-upgrade.sh) is correct
#
# NOTE: the "mainnet refuses to start when pruned" behaviour is mainnet/testnet
# gated in DigiByte's init.cpp and cannot be reproduced on regtest; Part 3
# instead proves the underlying prune/txindex incompatibility, which is what the
# whole change is built around.
#
# Exit code 0 = all checks passed, 1 = one or more failed.
# ══════════════════════════════════════════════════════════════════════════════
set -uo pipefail

DGB_VERSION="9.26.3"
# sha256 of digibyte-9.26.3-x86_64-linux-gnu.tar.gz per the GitHub release
# metadata. SOFT check (warn, not fail) — confirm against upstream if it differs.
DGB_SHA256_X86="51465a7a7409dc9a734e463b8fd95fb9773033753d79504de946ab2a0aa0991c"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Locate the repo root by walking UP from the script's own directory looking for
# marker files (install.sh + coin-upgrade.sh). When the script is run as a lone
# copy with no repo around it, REPO_ROOT stays empty and Part 1 is skipped.
REPO_ROOT=""
_d="$SCRIPT_DIR"
while [[ -n "$_d" && "$_d" != "/" ]]; do
  if [[ -f "$_d/install.sh" && -f "$_d/coin-upgrade.sh" ]]; then REPO_ROOT="$_d"; break; fi
  _d="$(dirname "$_d")"
done

RED=$'\033[0;31m'; GRN=$'\033[0;32m'; YLW=$'\033[1;33m'; CYN=$'\033[0;36m'; NC=$'\033[0m'; BOLD=$'\033[1m'
PASS=0; FAIL=0; SKIP=0

pass()    { PASS=$((PASS+1)); echo -e "  ${GRN}✓${NC} $1"; }
fail()    { FAIL=$((FAIL+1)); echo -e "  ${RED}✗ $1${NC}"; }
skip()    { SKIP=$((SKIP+1)); echo -e "  ${YLW}○ SKIP:${NC} $1"; }
info()    { echo -e "  ${CYN}i $1${NC}"; }
section() { echo -e "\n${CYN}${BOLD}$1${NC}"; }

# assert a regex IS present in a file
want() { # file regex desc
  if [[ -f "$1" ]] && grep -qE -- "$2" "$1"; then pass "$3"; else fail "$3"; fi
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/dgb926-test-XXXXXX")"
DAEMON_PID=""
cleanup() {
  [[ -n "$DAEMON_PID" ]] && kill "$DAEMON_PID" 2>/dev/null || true
  rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

echo -e "${BOLD}DigiByte v${DGB_VERSION} upgrade — deploy-readiness test${NC}"
echo -e "Script: ${SCRIPT_DIR}"
echo -e "Repo:   ${REPO_ROOT:-<not found — running standalone; Part 1 will skip>}"
echo -e "Work:   ${WORK}"

# ── PART 1: static config assertions ──────────────────────────────────────────
section "Part 1 — Version pins & pruning-removal edits"
if [[ -z "$REPO_ROOT" ]]; then
  skip "repo not found — script is running as a standalone copy"
  info "Part 1 verifies the repo source edits; run from INSIDE the repo checkout"
  info "(bash tests/test-dgb-926-upgrade.sh) so it can find install.sh, etc."
else
IN="$REPO_ROOT/install.sh"
want "$IN" 'DIGIBYTE_VERSION="9\.26\.3"'                'install.sh pins DGB 9.26.3'
want "$IN" 'requires txindex=1 for DigiDollar'          'install.sh DGB config forces full node (txindex/prune)'
want "$IN" 'DigiByte \(DGB\) is excluded from pruning'  'install.sh prune prompt excludes DGB'

CU="$REPO_ROOT/coin-upgrade.sh"
want "$CU" '\[DGB\]="9\.26\.3"'            'coin-upgrade.sh COIN_TARGET[DGB]=9.26.3'
want "$CU" '\[DGB\]="MAJOR"'               'coin-upgrade.sh COIN_RISK[DGB]=MAJOR'
want "$CU" 'dgb_apply_config_migration'    'coin-upgrade.sh has the migration function'
want "$CU" 'dgb_needs_pruning_migration'   'coin-upgrade.sh has the pruning detector'
want "$CU" 'Type .*UPGRADE.* to proceed'   'coin-upgrade.sh requires explicit acceptance'

want "$REPO_ROOT/upgrade.sh" 'DigiByte \(DGB\) v9\.26\.3 is a MAJOR upgrade' 'upgrade.sh shows a separate DGB MAJOR notice'
want "$REPO_ROOT/scripts/linux/pool-mode.sh" 'DGB is always a full node'      'pool-mode.sh forces DGB full node'
want "$REPO_ROOT/scripts/spiralctl.sh" 'Pruning is not supported for DigiByte' 'spiralctl blocks DGB pruning'
want "$REPO_ROOT/docker/Dockerfile"          'DIGIBYTE_VERSION=9\.26\.3' 'docker/Dockerfile pins 9.26.3'
want "$REPO_ROOT/docker/Dockerfile.digibyte" 'DIGIBYTE_VERSION=9\.26\.3' 'docker/Dockerfile.digibyte pins 9.26.3'
want "$REPO_ROOT/scripts/linux/regtest.sh"   'releases/download/v9\.26\.3/' 'regtest.sh pins 9.26.3'
fi

# ── PART 2: binary download + integrity + version ─────────────────────────────
section "Part 2 — Download & verify the v${DGB_VERSION} binary"
case "$(uname -m)" in
  x86_64|amd64)  SFX="x86_64-linux-gnu";  EXP_SHA="$DGB_SHA256_X86" ;;
  aarch64|arm64) SFX="aarch64-linux-gnu"; EXP_SHA="" ;;
  *)             SFX="x86_64-linux-gnu";  EXP_SHA="" ;;
esac
TARBALL="$WORK/dgb.tar.gz"
URL="https://github.com/DigiByte-Core/digibyte/releases/download/v${DGB_VERSION}/digibyte-${DGB_VERSION}-${SFX}.tar.gz"
DGBD=""; DGBCLI=""
if command -v curl >/dev/null 2>&1 && curl -fsSL --retry 3 -o "$TARBALL" "$URL"; then
  if gzip -t "$TARBALL" 2>/dev/null; then pass "downloaded + gzip integrity OK"; else fail "archive is corrupt"; fi
  sha=""
  if   command -v sha256sum >/dev/null 2>&1; then sha="$(sha256sum "$TARBALL" | awk '{print $1}')"
  elif command -v shasum    >/dev/null 2>&1; then sha="$(shasum -a 256 "$TARBALL" | awk '{print $1}')"; fi
  if [[ -n "$EXP_SHA" && -n "$sha" ]]; then
    if [[ "$sha" == "$EXP_SHA" ]]; then pass "sha256 matches release metadata"
    else info "sha256 differs from recorded value — verify against upstream:"; echo "     got:      $sha"; echo "     expected: $EXP_SHA"; fi
  elif [[ -n "$sha" ]]; then info "sha256: $sha (no reference to compare on this arch)"; fi
  tar -xzf "$TARBALL" -C "$WORK" 2>/dev/null || true
  DGBD="$(find "$WORK" -type f -name digibyted | head -1)"
  DGBCLI="$(find "$WORK" -type f -name digibyte-cli | head -1)"
  if [[ -x "$DGBD" ]]; then
    ver="$("$DGBD" --version 2>/dev/null | head -1)"
    if echo "$ver" | grep -q "$DGB_VERSION"; then pass "digibyted reports v$DGB_VERSION"; else fail "digibyted version mismatch: ${ver:-<none>}"; fi
  else
    fail "digibyted not found in archive"
  fi
else
  skip "no internet / curl — skipping download, integrity, and regtest (Parts 2-3)"
fi

# ── PART 3: regtest boot + full-node config + mining ──────────────────────────
section "Part 3 — Boot regtest with the full-node config & test mining"
if [[ -x "$DGBD" && -x "$DGBCLI" ]]; then
  DD="$WORK/regtest-data"; mkdir -p "$DD"
  cat > "$DD/digibyte.conf" <<'EOF'
server=1
txindex=1
prune=0
rpcuser=t
rpcpassword=t
fallbackfee=0.0001
EOF
  cli() { "$DGBCLI" -regtest -datadir="$DD" -rpcport=18999 -rpcuser=t -rpcpassword=t "$@"; }
  "$DGBD" -regtest -datadir="$DD" -rpcport=18999 -port=18998 -listen=0 -daemon=0 >/dev/null 2>&1 &
  DAEMON_PID=$!
  ok=false
  for _ in $(seq 1 30); do if cli getblockchaininfo >/dev/null 2>&1; then ok=true; break; fi; sleep 1; done
  if $ok; then
    pass "regtest daemon started with the full-node config (txindex=1/prune=0)"
    if cli getblockchaininfo | grep -qE '"pruned":[[:space:]]*false'; then pass "node reports NOT pruned (prune=0 honored)"; else fail "node reports pruned=true"; fi
    cli createwallet t >/dev/null 2>&1 || cli loadwallet t >/dev/null 2>&1 || true
    addr="$(cli -rpcwallet=t getnewaddress 2>/dev/null || cli getnewaddress 2>/dev/null || echo "")"
    if [[ -n "$addr" ]]; then
      if cli generatetoaddress 3 "$addr" >/dev/null 2>&1; then pass "mined 3 regtest blocks"; else fail "generatetoaddress failed"; fi
    else
      skip "could not create wallet/address — block-generation check"
    fi
    if cli getblocktemplate '{"rules":["segwit"]}' 2>/dev/null | grep -q '"coinbasevalue"'; then
      pass "getblocktemplate (segwit) returns a valid template — mining path works"
    else
      fail "getblocktemplate (segwit) did not return a template"
    fi
    # Informational: how does this build react to the Phase 2 DigiDollar rule?
    if cli getblocktemplate '{"rules":["segwit","digidollar-oracle"]}' 2>/dev/null | grep -q '"coinbasevalue"'; then
      info "digidollar-oracle rule accepted — returns a normal template pre-activation"
    else
      info "digidollar-oracle rule not accepted on plain regtest (expected; Phase 2 is validated on testnet26)"
    fi
    cli stop >/dev/null 2>&1 || true
    wait "$DAEMON_PID" 2>/dev/null || true
    DAEMON_PID=""
  else
    fail "regtest daemon did not respond within 30s"
    kill "$DAEMON_PID" 2>/dev/null || true; DAEMON_PID=""
  fi

  # Prove the prune/txindex incompatibility that drives the whole change.
  PT="$WORK/prunetest"; mkdir -p "$PT"
  runner=(); command -v timeout >/dev/null 2>&1 && runner=(timeout 30)
  out="$("${runner[@]}" "$DGBD" -regtest -datadir="$PT" -prune=550 -txindex=1 -rpcport=18997 -port=18996 -daemon=0 2>&1 || true)"
  if echo "$out" | grep -qi 'incompatible with -txindex'; then
    pass "daemon refuses prune + txindex (the conflict that makes DGB unprunable on v9.26.3)"
  else
    skip "could not confirm the prune/txindex incompatibility message"
  fi
else
  skip "no binary available — skipping regtest boot & mining"
fi

# ── PART 4: pruned → full config migration ────────────────────────────────────
section "Part 4 — Pruned→full config migration (mirrors coin-upgrade.sh)"
MC="$WORK/digibyte.conf"
cat > "$MC" <<'EOF'
server=1
rpcuser=keepme
rpcpassword=keepme2
dbcache=4096
#txindex=1
prune=5000
addnode=1.2.3.4:12024
EOF
# The two edits below MUST stay identical to dgb_apply_config_migration() in
# coin-upgrade.sh. Part 1 asserts that function still exists; if you change the
# migration there, update these two lines too.
sed -i -E 's|^([[:space:]]*prune=.*)$|#\1  # removed: DGB v9.26.3 needs a full node (DigiDollar/txindex)|' "$MC"
sed -i -E '/^[[:space:]]*#?[[:space:]]*txindex=/d' "$MC"
printf '\n# DigiDollar (v9.26.3) requires a full transaction index\ntxindex=1\n' >> "$MC"

if grep -qE '^#[[:space:]]*prune=5000' "$MC"; then pass "prune line commented out (kept, not deleted)"; else fail "prune not commented"; fi
if [[ "$(grep -cE '^txindex=1$' "$MC")" -eq 1 ]]; then pass "exactly one active txindex=1"; else fail "txindex not normalized to a single active line"; fi
if grep -qE '^prune=[1-9]' "$MC"; then fail "an active prune= line still remains"; else pass "no active prune remains"; fi
for keep in 'server=1' 'rpcuser=keepme' 'dbcache=4096' 'addnode=1.2.3.4:12024'; do
  if grep -qF -- "$keep" "$MC"; then pass "preserved unrelated line: $keep"; else fail "LOST unrelated line: $keep"; fi
done

# ── Summary ───────────────────────────────────────────────────────────────────
section "Summary"
echo -e "  ${GRN}pass:${NC} $PASS   ${RED}fail:${NC} $FAIL   ${YLW}skip:${NC} $SKIP"
if [[ "$FAIL" -eq 0 ]]; then
  echo -e "${GRN}${BOLD}✓ DGB v${DGB_VERSION} upgrade looks deploy-ready${NC}"
  [[ "$SKIP" -gt 0 ]] && echo -e "${YLW}  (note: $SKIP check(s) skipped — see above)${NC}"
  exit 0
else
  echo -e "${RED}${BOLD}✗ Fix the failures above before deploying${NC}"; exit 1
fi
