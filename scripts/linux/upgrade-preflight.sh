#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# Spiral Pool upgrade preflight — READ-ONLY dry run of an upgrade (since v3.1.0)
#
# Usage:  sudo bash scripts/linux/upgrade-preflight.sh [release folder] [--no-build]
#         (exit status: 0 all passed, 2 warnings only, 1 at least one FAIL)
#         sudo spiralctl upgrade --from <release folder> --dry-run
#
# The release folder defaults to the one this script is in. Checks what
# `sudo ./upgrade.sh --local --full` from that folder would do to this server,
# and whether it would go through, WITHOUT changing anything:
#   - the release folder, installed version, disk, services, HA role
#   - the sudoers migration (rewritten into a temp file and checked with visudo)
#   - script ownership changes in /spiralpool/scripts
#   - the HA key-exchange handler move (HA primaries)
#   - builds the release's stratum into /tmp and loads YOUR config.yaml with it,
#     reporting any merge-mining entry the release would switch off
#   - shell syntax of every script in the release
# Writes only: a temp dir under /tmp (removed on exit) and Go's module download
# cache (which the real upgrade fills anyway). Nothing under /spiralpool or /etc.

set -u
REL=""
NO_BUILD=false
for arg in "$@"; do
    case "$arg" in
        --no-build) NO_BUILD=true ;;
        -h|--help)  sed -n '2,/^$/p' "$0"; exit 0 ;;
        *)          REL="$arg" ;;
    esac
done
# Default: the release folder this script ships in (scripts/linux/ → ../..)
[[ -z "$REL" ]] && REL="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/../.." && pwd)"
INSTALL_DIR=/spiralpool

RED=$'\033[0;31m'; GRN=$'\033[0;32m'; YEL=$'\033[1;33m'; CYN=$'\033[0;36m'; DIM=$'\033[2m'; NC=$'\033[0m'
PASS=0; WARN=0; FAIL=0
ok()   { echo "  ${GRN}PASS${NC}  $*"; PASS=$((PASS + 1)); }
warn() { echo "  ${YEL}WARN${NC}  $*"; WARN=$((WARN + 1)); }
bad()  { echo "  ${RED}FAIL${NC}  $*"; FAIL=$((FAIL + 1)); }
info() { echo "        ${DIM}$*${NC}"; }
hdr()  { echo; echo "${CYN}── $* ──${NC}"; }

if [[ $EUID -ne 0 ]]; then echo "Run with sudo: sudo bash $0 [release folder]"; exit 1; fi
if [[ ! -d "$REL" ]]; then
    echo "Usage: sudo bash $0 [release folder] [--no-build]"; exit 1
fi
REL="$(cd "$REL" && pwd)"
TMP="$(mktemp -d /tmp/spiral-preflight.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

echo "Spiral Pool upgrade preflight — read-only. Nothing on this server is changed."

# ─────────────────────────────────────────────────────────────────────────────
hdr "Release folder and versions"
rel_ver=$(tr -d '[:space:]' < "$REL/VERSION" 2>/dev/null)
inst_ver=$(tr -d '[:space:]' < "$INSTALL_DIR/VERSION" 2>/dev/null)
if [[ -z "$rel_ver" ]]; then
    bad "no VERSION file in $REL — is this a Spiral Pool release folder?"
else
    ok "release folder is $rel_ver ($REL)"
fi
for f in upgrade.sh install.sh scripts/linux/pool-upgrade-launch.sh scripts/linux/pool-mode.sh src/stratum/go.mod; do
    [[ -f "$REL/$f" ]] || bad "release folder is missing $f"
done
[[ -n "$inst_ver" ]] && info "installed version: $inst_ver" || warn "no $INSTALL_DIR/VERSION — is Spiral Pool installed here?"
if [[ -n "$inst_ver" && -n "$rel_ver" ]]; then
    if [[ "$inst_ver" == "$rel_ver" ]]; then
        warn "this server already runs $rel_ver — the upgrade would reinstall the same version"
    elif [[ "$(printf '%s\n' "$inst_ver" "$rel_ver" | sort -V | tail -1)" == "$inst_ver" ]]; then
        bad "the release ($rel_ver) is OLDER than this server ($inst_ver) — that would be a downgrade"
    else
        ok "upgrade $inst_ver → $rel_ver"
    fi
fi
if grep -q $'\r' "$REL/upgrade.sh" 2>/dev/null; then
    warn "release files have Windows line endings — upgrade.sh fixes this itself when it starts"
fi
[[ "$(uname -m)" == "x86_64" ]] && ok "architecture x86_64" || bad "architecture $(uname -m) — Spiral Pool supports x86_64 only"

# ─────────────────────────────────────────────────────────────────────────────
hdr "Disk and memory"
avail_mb=$(df -Pm "$INSTALL_DIR" 2>/dev/null | awk 'NR==2{print $4}')
if [[ -n "$avail_mb" ]]; then
    if (( avail_mb < 1024 )); then bad "only ${avail_mb} MB free on $INSTALL_DIR (backup + build need ~1 GB)"
    elif (( avail_mb < 3072 )); then warn "${avail_mb} MB free on $INSTALL_DIR — enough, but tight"
    else ok "${avail_mb} MB free on $INSTALL_DIR"; fi
fi
tmp_mb=$(df -Pm /tmp 2>/dev/null | awk 'NR==2{print $4}')
[[ -n "$tmp_mb" ]] && (( tmp_mb < 1024 )) && warn "/tmp has only ${tmp_mb} MB free (the stratum build uses it)"
db_kb=$(du -sk /var/lib/postgresql 2>/dev/null | awk '{print $1}')
pg_free_kb=$(df -Pk /var/lib/postgresql 2>/dev/null | awk 'NR==2{print $4}')
if [[ -n "$db_kb" && -n "$pg_free_kb" ]]; then
    (( pg_free_kb < db_kb + 262144 )) && warn "PostgreSQL volume is tight for the pre-upgrade database dump" \
        || ok "room for the pre-upgrade database dump ($((db_kb / 1024)) MB database)"
fi
mem_mb=$(free -m 2>/dev/null | awk '/^Mem:/{print $7}')
[[ -n "$mem_mb" ]] && info "available memory: ${mem_mb} MB"

# ─────────────────────────────────────────────────────────────────────────────
hdr "Services right now"
for svc in spiralstratum spiraldash spiralsentinel spiralpool-health postgresql patroni; do
    systemctl list-unit-files "$svc.service" &>/dev/null || continue
    if ! systemctl is-enabled --quiet "$svc" 2>/dev/null; then
        # What upgrade.sh (3.1.0+) does with a disabled pool service: restarts it if it
        # was running; enables Sentinel if a notification channel is configured;
        # otherwise leaves it as it is. Nothing here changes it.
        if [[ -f "/etc/systemd/system/$svc.service" ]] && [[ "$svc" == spiral* ]]; then
            st=$(systemctl is-active "$svc" 2>/dev/null)
            notify_cfg=false
            if [[ "$svc" == "spiralsentinel" ]]; then
                # Use the release's own check, so the dry run predicts exactly what
                # upgrade.sh will decide (it reads every place Sentinel reads).
                pf_fn=$(sed -n '/^sentinel_notifications_configured() {/,/^}/p' "$REL/upgrade.sh" 2>/dev/null)
                if [[ -n "$pf_fn" ]] && eval "$pf_fn" 2>/dev/null; then
                    POOL_USER=$(stat -c '%U' "$INSTALL_DIR" 2>/dev/null || echo spiraluser)
                    sentinel_notifications_configured && notify_cfg=true
                fi
            fi
            if $notify_cfg; then
                info "$svc is not enabled ($st) — notifications are configured, so the upgrade will enable and start it"
            elif [[ "$st" == "active" ]]; then
                info "$svc is running but not set to start at boot (started by hand) — the upgrade starts it again afterwards; to have it survive a reboot: sudo systemctl enable $svc"
            else
                info "$svc is disabled and stopped — the upgrade leaves it that way"
            fi
        fi
        continue
    fi
    st=$(systemctl is-active "$svc" 2>/dev/null)
    case "$st" in
        active) ok "$svc is running" ;;
        failed) warn "$svc is FAILED now — fix or note it before upgrading, so you can tell what the upgrade changed" ;;
        *)      warn "$svc is $st" ;;
    esac
done
for d in digibyted bitcoind bitcoind-bch bitcoincashIId bitcoiniid bitcoinsilverd litecoind dogecoind pepecoind catcoind namecoind syscoind myriadcoind fractald ecashd; do
    systemctl is-enabled --quiet "$d" 2>/dev/null || continue
    st=$(systemctl is-active "$d" 2>/dev/null)
    [[ "$st" == "active" ]] && info "$d running" || warn "coin daemon $d is $st"
done

# ─────────────────────────────────────────────────────────────────────────────
hdr "High availability"
if [[ -f /etc/keepalived/keepalived.conf ]] && grep -q SPIRALPOOL /etc/keepalived/keepalived.conf 2>/dev/null; then
    vip=$(grep -oP '^\s+\K[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(?=/)' /etc/keepalived/keepalived.conf | head -1)
    if [[ -n "$vip" ]] && ip addr show 2>/dev/null | grep -qF " ${vip}/"; then
        info "HA is configured and this node holds the VIP ($vip) — it is the MASTER"
        warn "HA: upgrade every node before adding a new backup node"
    else
        info "HA is configured and this node does NOT hold the VIP — it is a BACKUP"
        info "upgrade.sh restarts only stratum + health here; dashboard/Sentinel stay with the HA watcher"
    fi
else
    ok "HA not configured (standalone)"
fi
kx_files=$(grep -l "ha-key-exchange" /etc/sudoers.d/* 2>/dev/null)
if [[ -n "$kx_files" ]]; then
    awk '/<< .HANDLER_EOF.$/{f=1; next} /^HANDLER_EOF$/{f=0} f' "$REL/scripts/linux/pool-mode.sh" > "$TMP/kx.sh"
    if head -1 "$TMP/kx.sh" | grep -q '^#!/bin/bash' && grep -q 'HA Key Exchange Handler' "$TMP/kx.sh" && bash -n "$TMP/kx.sh"; then
        ok "HA key-exchange handler can be regenerated from the release ($(wc -l < "$TMP/kx.sh") lines)"
    else
        bad "could not extract the HA key-exchange handler from the release pool-mode.sh"
    fi
    for f in $kx_files; do
        sed "s|/spiralpool/bin/ha-key-exchange\.sh|${INSTALL_DIR}/scripts/ha-key-exchange.sh|g" "$f" > "$TMP/kx.sudoers"
        if visudo -cf "$TMP/kx.sudoers" >/dev/null 2>&1; then
            ok "$(basename "$f"): rewritten entry passes visudo"
            diff <(cat "$f") "$TMP/kx.sudoers" | grep '^>' | sed 's/^> /        will become: /'
        else
            bad "$(basename "$f"): rewritten entry FAILS visudo — the upgrade would leave it unchanged"
        fi
    done
    [[ -f "$INSTALL_DIR/bin/ha-key-exchange.sh" ]] && info "will remove the old $INSTALL_DIR/bin/ha-key-exchange.sh"
fi

# ─────────────────────────────────────────────────────────────────────────────
hdr "sudoers migration (simulated with visudo)"
DS=/etc/sudoers.d/spiralpool-dashboard
if [[ -f "$DS" ]]; then
    sed -e "s|NOPASSWD: ${INSTALL_DIR}/upgrade\.sh |NOPASSWD: ${INSTALL_DIR}/scripts/upgrade.sh |" \
        -e "s|NOPASSWD: ${INSTALL_DIR}/upgrade\.sh\$|NOPASSWD: ${INSTALL_DIR}/scripts/upgrade.sh *|" \
        -e "s|NOPASSWD: ${INSTALL_DIR}/bin/daemon-reindex\.sh|NOPASSWD: ${INSTALL_DIR}/scripts/daemon-reindex.sh|" \
        "$DS" > "$TMP/dash.sudoers"
    POOL_USER=$(stat -c '%U' "$INSTALL_DIR" 2>/dev/null || echo spiraluser)
    grep -q "scripts/upgrade\.sh" "$TMP/dash.sudoers" || \
        printf '\n%s ALL=(ALL) NOPASSWD: %s/scripts/upgrade.sh *\n' "$POOL_USER" "$INSTALL_DIR" >> "$TMP/dash.sudoers"
    grep -q "scripts/pool-upgrade-launch\.sh" "$TMP/dash.sudoers" || \
        printf '\n%s ALL=(root) NOPASSWD: %s/scripts/pool-upgrade-launch.sh\n' "$POOL_USER" "$INSTALL_DIR" >> "$TMP/dash.sudoers"
    if visudo -cf "$TMP/dash.sudoers" >/dev/null 2>&1; then
        ok "migrated spiralpool-dashboard passes visudo"
    else
        bad "migrated spiralpool-dashboard FAILS visudo — the upgrade would leave the file unchanged (dashboard Upgrade button would not work)"
    fi
    changes=$(diff "$DS" "$TMP/dash.sudoers" | grep '^[<>]' | grep -v '^> *$')
    if [[ -n "$changes" ]]; then
        echo "$changes" | sed -e 's/^< /        - /' -e 's/^> /        + /'
    else
        info "no changes needed (already migrated)"
    fi
    leftovers=$(grep -nE "NOPASSWD: ${INSTALL_DIR}/(upgrade\.sh|bin/)" "$TMP/dash.sudoers")
    [[ -z "$leftovers" ]] && ok "after migration no sudo rule points into a pool-user-owned directory" \
        || bad "still pointing outside $INSTALL_DIR/scripts after migration: $leftovers"
else
    warn "$DS not found — the dashboard has no service-control rights (upgrade.sh would create the file)"
fi

# ─────────────────────────────────────────────────────────────────────────────
hdr "Ownership in $INSTALL_DIR/scripts (will be made root-owned, not writable by others)"
if [[ -d "$INSTALL_DIR/scripts" ]]; then
    [[ "$(stat -c %U "$INSTALL_DIR/scripts")" == "root" ]] && ok "$INSTALL_DIR/scripts directory is root-owned" \
        || warn "$INSTALL_DIR/scripts directory is owned by $(stat -c %U "$INSTALL_DIR/scripts") — will become root"
    n=0
    while IFS= read -r f; do
        info "will change: $(stat -c '%U:%G %A' "$f")  $(basename "$f")"
        n=$((n + 1))
    done < <(find "$INSTALL_DIR/scripts" -maxdepth 1 -type f \( ! -user root -o -perm /022 \) 2>/dev/null)
    (( n == 0 )) && ok "every script is already root-owned" || ok "$n file(s) will be re-owned to root (they still run as before)"
fi
[[ -f "$INSTALL_DIR/upgrade.sh" ]] && info "$INSTALL_DIR/upgrade.sh is $(stat -c '%U:%G' "$INSTALL_DIR/upgrade.sh") — will become root:root"
[[ -f "$INSTALL_DIR/bin/daemon-reindex.sh" ]] && info "$INSTALL_DIR/bin/daemon-reindex.sh will move to $INSTALL_DIR/scripts/"

# ─────────────────────────────────────────────────────────────────────────────
hdr "systemd features the dashboard upgrade launcher uses"
command -v systemd-run >/dev/null && ok "systemd-run present" || bad "systemd-run not found"
sdv=$(systemctl --version 2>/dev/null | awk 'NR==1{print $2}')
[[ -n "$sdv" ]] && (( sdv >= 240 )) && ok "systemd $sdv (append: log output needs ≥ 240)" || warn "systemd ${sdv:-?} — the launcher's log file needs ≥ 240"

# ─────────────────────────────────────────────────────────────────────────────
hdr "Sentinel update settings"
for sc in "$INSTALL_DIR/config/sentinel/config.json" /home/*/.spiralsentinel/config.json; do
    [[ -f "$sc" ]] || continue
    mode=$(grep -oP '"auto_update_mode"\s*:\s*"\K[^"]+' "$sc" 2>/dev/null)
    [[ "$mode" == "auto" ]] && warn "$sc has auto_update_mode=auto — Sentinel cannot run upgrades under its sandbox and will notify instead; use 'spiralctl update configure' for unattended upgrades" \
        || info "$sc: auto_update_mode=${mode:-notify (default)} — first notice + 48h/72h/1-week reminders"
    break
done

# ─────────────────────────────────────────────────────────────────────────────
hdr "Build the release stratum and load YOUR config.yaml with it"
CFG="$INSTALL_DIR/config/config.yaml"
if $NO_BUILD; then
    info "skipped (--no-build)"
else
    GO=""
    [[ -x /usr/local/go/bin/go ]] && GO=/usr/local/go/bin/go || GO=$(command -v go 2>/dev/null)
    need_go=$(grep -oP '^GO_VERSION="\K[^"]+' "$REL/install.sh" | head -1)
    if [[ -z "$GO" ]]; then
        warn "Go not found — upgrade.sh installs Go ${need_go} itself; skipping the build check"
    else
        have_go=$("$GO" version | grep -oP 'go\K[0-9.]+')
        info "Go $have_go at $GO (release pins $need_go; upgrade.sh upgrades Go if needed)"
        mkdir -p "$TMP/pf"
        cat > "$TMP/pf/main.go" <<'GOEOF'
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spiralpool/stratum/internal/config"
)

func main() {
	path := os.Args[1]
	cfg, err := config.LoadV2(path)
	if err == nil {
		fmt.Printf("V2_OK coins=%d\n", len(cfg.Coins))
		for _, c := range cfg.Coins {
			merge := "-"
			if c.MergeMining.Enabled {
				var on []string
				for _, a := range c.MergeMining.AuxChains {
					if a.Enabled {
						on = append(on, a.Symbol)
					}
				}
				merge = strings.Join(on, ",")
			}
			fmt.Printf("COIN %s enabled=%v merge=%s\n", c.Symbol, c.Enabled, merge)
		}
		for _, w := range cfg.MergeMiningWarnings {
			fmt.Printf("MERGE_WARNING %s\n", w)
		}
		return
	}
	fmt.Printf("V2_FAIL %v\n", err)
	if _, err1 := config.Load(path); err1 == nil {
		fmt.Println("V1_OK")
		return
	} else {
		fmt.Printf("V1_FAIL %v\n", err1)
		os.Exit(2)
	}
}
GOEOF
        printf '{"Replace":{"%s":"%s"}}' "$REL/src/stratum/cmd/zzpreflight/main.go" "$TMP/pf/main.go" > "$TMP/pf/overlay.json"
        info "building (this can take a few minutes the first time)…"
        if ( cd "$REL/src/stratum" && "$GO" build -overlay "$TMP/pf/overlay.json" -o "$TMP/pf/check" ./cmd/zzpreflight ) > "$TMP/pf/build.log" 2>&1 \
           && ( cd "$REL/src/stratum" && "$GO" build -o "$TMP/pf/spiralpool" ./cmd/spiralpool ) >> "$TMP/pf/build.log" 2>&1; then
            ok "the release stratum builds on this server ($("$TMP/pf/spiralpool" --version 2>&1 | head -1))"
            if [[ -f "$CFG" ]]; then
                out=$(SPIRALPOOL_INSTALL_DIR="$INSTALL_DIR" "$TMP/pf/check" "$CFG" 2>&1)
                if grep -q '^V2_OK' <<< "$out"; then
                    ok "config.yaml loads with the release ($(grep -c '^COIN' <<< "$out") coin entries)"
                    grep '^COIN' <<< "$out" | sed 's/^COIN /        /'
                    if grep -q '^MERGE_WARNING' <<< "$out"; then
                        grep '^MERGE_WARNING' <<< "$out" | sed 's/^MERGE_WARNING /  '"$YEL"'WARN'"$NC"'  the release will switch this off: /'
                        WARN=$((WARN + $(grep -c '^MERGE_WARNING' <<< "$out")))
                    else
                        ok "no merge-mining entry would be switched off"
                    fi
                elif grep -q '^V1_OK' <<< "$out"; then
                    ok "config.yaml loads with the release as a V1 (single-coin) config"
                else
                    bad "the release cannot load config.yaml:"
                    sed 's/^/        /' <<< "$out"
                fi
            else
                warn "$CFG not found"
            fi
        else
            bad "the release stratum did not build on this server — last lines:"
            tail -15 "$TMP/pf/build.log" | sed 's/^/        /'
        fi
    fi
fi

# ─────────────────────────────────────────────────────────────────────────────
hdr "Shell syntax of the release scripts"
nerr=0
while IFS= read -r f; do
    if ! bash -n "$f" 2>"$TMP/syn.err"; then bad "syntax error in ${f#$REL/}: $(head -1 "$TMP/syn.err")"; nerr=$((nerr + 1)); fi
done < <(find "$REL" -name '*.sh' -not -path '*/node_modules/*' 2>/dev/null)
(( nerr == 0 )) && ok "all release shell scripts parse"

# ─────────────────────────────────────────────────────────────────────────────
echo
echo "${CYN}════════════════════════════════════════════════════════════${NC}"
echo "  ${GRN}PASS $PASS${NC}   ${YEL}WARN $WARN${NC}   ${RED}FAIL $FAIL${NC}"
if (( FAIL > 0 )); then
    echo "  ${RED}Fix the FAIL items before upgrading.${NC}"
elif [[ -z "${SPIRAL_PREFLIGHT_FROM_UPGRADE:-}" ]]; then
    echo "  Ready. Upgrade with:  cd $REL && sudo ./upgrade.sh --local --full"
    echo "  (upgrade.sh takes a backup first; roll back with: sudo ./upgrade.sh --rollback <backup>)"
fi
echo "${CYN}════════════════════════════════════════════════════════════${NC}"
# Exit status: 0 = all passed, 2 = warnings only, 1 = at least one FAIL.
(( FAIL > 0 )) && exit 1
(( WARN > 0 )) && exit 2
exit 0
