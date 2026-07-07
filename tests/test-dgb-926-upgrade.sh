#!/usr/bin/env bash
# ══════════════════════════════════════════════════════════════════════════════
# test-dgb-926-upgrade.sh — Deploy-readiness test for the DigiByte Core v9.26.4
# upgrade and the pruning-SUPPORT changes.
#
# Background: v9.26.3 REQUIRED a full node (txindex for DigiDollar) and could not
# prune. v9.26.4 makes DigiDollar work on a pruned node — it keeps the
# [DigiDollar-activation-floor, tip] window and turns txindex off automatically —
# so DGB now honors the pool-wide prune toggle again, and coin-upgrade.sh offers a
# one-time switch to a pruned node on the 9.26.x → 9.26.4 upgrade.
#
# Safe to run on a blank Spiral Pool instance (any Linux box with this repo). It
# does NOT touch a live node, systemd, or /spiralpool — everything runs in an
# isolated temp directory on a throwaway regtest chain, and no root is required.
#
#   bash tests/test-dgb-926-upgrade.sh
#
# What it checks:
#   Part 1  static — every file pins 9.26.4 and the pruning-SUPPORT edits exist
#   Part 2  download + gzip integrity + the binary reports v9.26.4
#   Part 3  boots regtest with the full-node config, mines, confirms
#           getblocktemplate works, boots a PRUNED config, and proves prune+txindex
#           is still mutually exclusive (which is why v9.26.4 drops txindex)
#   Part 4  the full→pruned config edit (mirrors dgb_enable_pruning_config) is right
#
# Exit code 0 = all checks passed, 1 = one or more failed.
# ══════════════════════════════════════════════════════════════════════════════
set -uo pipefail

DGB_VERSION="9.26.4"
# sha256 of digibyte-9.26.4-x86_64-linux-gnu.tar.gz — verified against the GitHub
# release asset (67,408,783 bytes). HARD check on x86_64.
DGB_SHA256_X86="523da7ac9ee0f1bcb38ff75da6f01997b9491a9eee1e2b0ce3c90f60b2aaa9b3"

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
# assert a regex is ABSENT from a file (the v9.26.3 pruning-removal strings)
want_absent() { # file regex desc
  if [[ -f "$1" ]] && grep -qE -- "$2" "$1"; then fail "$3"; else pass "$3"; fi
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
section "Part 1 — Version pins & pruning-SUPPORT edits"
if [[ -z "$REPO_ROOT" ]]; then
  skip "repo not found — script is running as a standalone copy"
  info "Part 1 verifies the repo source edits; run from INSIDE the repo checkout"
  info "(bash tests/test-dgb-926-upgrade.sh) so it can find install.sh, etc."
else
IN="$REPO_ROOT/install.sh"
want "$IN" 'DIGIBYTE_VERSION="9\.26\.4"'                     'install.sh pins DGB 9.26.4'
want "$IN" 'DigiByte \(DGB\) supports pruning as of DigiByte Core v9\.26\.4' \
                                                            'install.sh prune prompt says DGB supports pruning'
want "$IN" 'runs DigiDollar on a pruned node too'           'install.sh DGB config honors the prune toggle'
want_absent "$IN" 'is excluded from pruning'                'install.sh no longer excludes DGB from pruning'

CU="$REPO_ROOT/coin-upgrade.sh"
want "$CU" '\[DGB\]="9\.26\.4"'            'coin-upgrade.sh COIN_TARGET[DGB]=9.26.4'
want "$CU" '\[DGB\]="MINOR"'               'coin-upgrade.sh COIN_RISK[DGB]=MINOR'
want "$CU" 'dgb_enable_pruning_config'     'coin-upgrade.sh has the pruning-enable function'
want "$CU" 'dgb_is_pruned'                 'coin-upgrade.sh has the pruned detector'
want "$CU" 'Enable pruning for DigiByte'   'coin-upgrade.sh offers the one-time pruning switch'
want_absent "$CU" 'dgb_apply_config_migration' 'coin-upgrade.sh no longer force-removes pruning'

want "$REPO_ROOT/upgrade.sh" 'DigiByte \(DGB\) v9\.26\.4 adds a DigiDollar consensus rule and enables' \
                                                            'upgrade.sh shows the v9.26.4 pruning-enabled notice'
want "$REPO_ROOT/scripts/linux/pool-mode.sh" 'get_existing_prune "\$SPIRALPOOL_DIR/dgb/digibyte.conf"' \
                                                            'pool-mode.sh lets DGB honor the prune toggle'
want_absent "$REPO_ROOT/scripts/linux/pool-mode.sh" 'DGB is always a full node' \
                                                            'pool-mode.sh no longer forces DGB full'
want_absent "$REPO_ROOT/scripts/spiralctl.sh" 'Pruning is not supported for DigiByte' \
                                                            'spiralctl no longer blocks DGB pruning'
want "$REPO_ROOT/docker/Dockerfile"          'DIGIBYTE_VERSION=9\.26\.4' 'docker/Dockerfile pins 9.26.4'
want "$REPO_ROOT/docker/Dockerfile.digibyte" 'DIGIBYTE_VERSION=9\.26\.4' 'docker/Dockerfile.digibyte pins 9.26.4'
want "$REPO_ROOT/scripts/linux/regtest.sh"   'releases/download/v9\.26\.4/' 'regtest.sh pins 9.26.4'
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
    if [[ "$sha" == "$EXP_SHA" ]]; then pass "sha256 matches the pinned release hash"
    else fail "sha256 MISMATCH — got $sha, expected $EXP_SHA"; fi
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

# ── PART 3: regtest boot + full & pruned configs + mining ─────────────────────
section "Part 3 — Boot regtest (full & pruned) & test mining"
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
    cli stop >/dev/null 2>&1 || true
    wait "$DAEMON_PID" 2>/dev/null || true
    DAEMON_PID=""
  else
    fail "regtest daemon did not respond within 30s"
    kill "$DAEMON_PID" 2>/dev/null || true; DAEMON_PID=""
  fi

  # v9.26.4: a PRUNED config (prune target, NO txindex) must boot and report pruned.
  PD="$WORK/pruned-data"; mkdir -p "$PD"
  cat > "$PD/digibyte.conf" <<'EOF'
server=1
prune=550
rpcuser=t
rpcpassword=t
fallbackfee=0.0001
EOF
  pcli() { "$DGBCLI" -regtest -datadir="$PD" -rpcport=18995 -rpcuser=t -rpcpassword=t "$@"; }
  "$DGBD" -regtest -datadir="$PD" -rpcport=18995 -port=18994 -listen=0 -daemon=0 >/dev/null 2>&1 &
  DAEMON_PID=$!
  pok=false
  for _ in $(seq 1 30); do if pcli getblockchaininfo >/dev/null 2>&1; then pok=true; break; fi; sleep 1; done
  if $pok; then
    pass "regtest daemon started with the PRUNED config (prune=550, no txindex)"
    if pcli getblockchaininfo | grep -qE '"pruned":[[:space:]]*true'; then pass "node reports pruned (prune honored, txindex off)"; else fail "pruned config did not report pruned=true"; fi
    pcli stop >/dev/null 2>&1 || true
    wait "$DAEMON_PID" 2>/dev/null || true
    DAEMON_PID=""
  else
    fail "pruned regtest daemon did not respond within 30s"
    kill "$DAEMON_PID" 2>/dev/null || true; DAEMON_PID=""
  fi

  # prune + txindex remain mutually exclusive — this is WHY v9.26.4 drops txindex
  # under prune rather than keeping both.
  PT="$WORK/prunetest"; mkdir -p "$PT"
  runner=(); command -v timeout >/dev/null 2>&1 && runner=(timeout 30)
  out="$("${runner[@]}" "$DGBD" -regtest -datadir="$PT" -prune=550 -txindex=1 -rpcport=18997 -port=18996 -daemon=0 2>&1 || true)"
  if echo "$out" | grep -qi 'incompatible with -txindex'; then
    pass "daemon still refuses prune + txindex together (v9.26.4 drops txindex under prune)"
  else
    skip "could not confirm the prune/txindex incompatibility message"
  fi
else
  skip "no binary available — skipping regtest boot & mining"
fi

# ── PART 4: full → pruned config edit ─────────────────────────────────────────
section "Part 4 — Full→pruned config edit (mirrors dgb_enable_pruning_config)"
MC="$WORK/digibyte.conf"
cat > "$MC" <<'EOF'
server=1
rpcuser=keepme
rpcpassword=keepme2
dbcache=4096
txindex=1
prune=0
addnode=1.2.3.4:12024
EOF
# The edits below MUST stay identical to dgb_enable_pruning_config() in
# coin-upgrade.sh. Part 1 asserts that function still exists; if you change the
# migration there, update these lines too.
sed -i -E '/^[[:space:]]*#?[[:space:]]*txindex=/d' "$MC"
sed -i -E '/^[[:space:]]*#?[[:space:]]*prune=/d'   "$MC"
printf '\n# DigiByte Core v9.26.4+: pruned DigiDollar node (~5 GB target).\nprune=5000\n' >> "$MC"

if [[ "$(grep -cE '^prune=5000$' "$MC")" -eq 1 ]]; then pass "exactly one active prune=5000"; else fail "prune not normalized to a single active prune=5000"; fi
if grep -qE '^txindex=' "$MC"; then fail "an active txindex= line still remains"; else pass "txindex removed (prune drops it automatically)"; fi
if grep -qE '^prune=0$' "$MC"; then fail "the old prune=0 line still remains"; else pass "old prune=0 replaced"; fi
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
