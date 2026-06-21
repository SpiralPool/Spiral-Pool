#!/usr/bin/env python3
"""Reset corrupted per-miner hashrate baselines in Spiral Sentinel state.

The Sentinel learns a rolling "baseline" hashrate per miner and alerts when the
current reading drops far below it. A single glitched/units-misparsed reading can
inflate a baseline far above what the device can physically produce, after which
every healthy reading looks like a ~97% crash and the degradation alert fires
roughly once an hour forever (the baseline does not self-heal — it intentionally
stops adapting while a miner reads "degraded").

This tool clears the stored baseline for one or more miners so the Sentinel
re-learns a correct baseline from live readings on the next cycle.

IMPORTANT: stop the Sentinel before running this, otherwise the running process
holds state in memory and will overwrite state.json on its next save, undoing the
reset. Re-start the Sentinel afterwards.

Usage:
    # Show all current baselines (no changes made):
    python reset-hashrate-baseline.py

    # Reset specific miners by name (worker name as shown in the alert):
    python reset-hashrate-baseline.py <worker-name>

    # Reset every miner's baseline:
    python reset-hashrate-baseline.py --all
"""
import json
import os
import sys
import tempfile
from pathlib import Path

# Mirror SpiralSentinel.py's DATA_DIR resolution: primary ~/.spiralsentinel,
# fallback $SPIRALPOOL_INSTALL_DIR/config/sentinel (used when ProtectHome blocks home).
INSTALL_DIR = Path(os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool"))
CANDIDATES = [
    Path.home() / ".spiralsentinel" / "state.json",
    INSTALL_DIR / "config" / "sentinel" / "state.json",
]


def find_state_file():
    for p in CANDIDATES:
        if p.exists():
            return p
    return None


def save_atomic(path, data):
    """Write-to-temp-then-rename, matching MonitorState.save()."""
    temp_fd, temp_path = tempfile.mkstemp(suffix=".tmp", prefix="state_", dir=str(path.parent))
    try:
        with os.fdopen(temp_fd, "w") as f:
            json.dump(data, f)
            f.flush()
            os.fsync(f.fileno())
        os.replace(temp_path, path)
    except BaseException:
        try:
            os.unlink(temp_path)
        except OSError:
            pass
        raise


def main():
    args = sys.argv[1:]
    state_file = find_state_file()
    if state_file is None:
        print("ERROR: could not find state.json. Looked in:")
        for p in CANDIDATES:
            print(f"  {p}")
        print("Set SPIRALPOOL_INSTALL_DIR if the Sentinel uses the fallback location.")
        return 1

    with open(state_file) as f:
        state = json.load(f)

    baselines = state.get("miner_hashrate_baseline", {})
    if not baselines:
        print(f"No miner_hashrate_baseline data in {state_file} — nothing to reset.")
        return 0

    # No args: list current baselines so the operator can spot the corrupt one.
    if not args:
        print(f"Current baselines in {state_file}:\n")
        for name, b in sorted(baselines.items()):
            avg = b.get("avg", 0)
            print(f"  {name:<24} avg={avg:>12.0f} GH/s  samples={b.get('samples', 0)}")
        print("\nRe-run with a miner name to reset it, or --all to reset everything.")
        print("Stop the Sentinel first, then re-start it after the reset.")
        return 0

    if args == ["--all"]:
        targets = list(baselines.keys())
    else:
        targets = args
        missing = [t for t in targets if t not in baselines]
        if missing:
            print(f"ERROR: no baseline found for: {', '.join(missing)}")
            print("Known miners: " + ", ".join(sorted(baselines.keys())))
            return 1

    for name in targets:
        old = baselines.pop(name, None)
        if old is not None:
            print(f"Reset baseline for {name} (was avg={old.get('avg', 0):.0f} GH/s).")

    state["miner_hashrate_baseline"] = baselines
    save_atomic(state_file, state)
    print(f"\nSaved {state_file}. Re-start the Sentinel; it will relearn baselines from live readings.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
