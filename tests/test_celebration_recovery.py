"""Sentinel side of the v3.0.3 Avalon power-cycle celebration fix.

Two things Sentinel now does, both pinned here:

  1. recover_block_celebration() runs `block-celebrate.sh --recover` when the
     script's durable record of lit miners exists - at startup (a power cut) and
     at most every 5 minutes after (a miner that was rebooting when its restore
     was due). It must cost nothing when there is no record, and must not spawn
     a process every monitoring cycle while one exists.

  2. A block discovered at startup is celebrated only for what is left of its
     window (found_at + duration), not for a fresh full duration. Before, a
     restart re-lit the miners for hours over a block found long before.

The shell side (record, recovery, retries, clocks) is covered by
tests/test_celebration_power_cycle.sh.

Run: python -m pytest tests/test_celebration_recovery.py -v
"""
import importlib.util
import os
import tempfile

import pytest

os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

FOUND = "2026-11-15T12:00:00Z"
FOUND_TS = 1794744000.0  # FOUND as epoch seconds


# ── celebration_seconds_remaining ────────────────────────────────────────────
class TestRemainingWindow:
    def test_just_found_gets_the_full_window(self):
        assert sentinel.celebration_seconds_remaining(7200, FOUND, now=FOUND_TS) == 7200

    def test_partly_elapsed_window(self):
        assert sentinel.celebration_seconds_remaining(7200, FOUND, now=FOUND_TS + 1800) == 5400

    def test_restart_after_the_deadline_gets_nothing(self):
        assert sentinel.celebration_seconds_remaining(7200, FOUND, now=FOUND_TS + 7200) == 0
        assert sentinel.celebration_seconds_remaining(7200, FOUND, now=FOUND_TS + 5 * 3600) == 0

    def test_restart_just_before_the_deadline(self):
        assert sentinel.celebration_seconds_remaining(7200, FOUND, now=FOUND_TS + 7195) == 5

    def test_found_in_the_future_counts_as_just_found(self):
        # Pool and Sentinel clocks disagreeing must not EXTEND the window.
        assert sentinel.celebration_seconds_remaining(7200, FOUND, now=FOUND_TS - 600) == 7200

    @pytest.mark.parametrize("ts", [
        "2026-11-15T12:00:00Z",
        "2026-11-15T12:00:00.5Z",              # Go trims trailing zeros: 1..9 digits
        "2026-11-15T12:00:00.123456789Z",
        "2026-11-15T12:00:00+00:00",
        "2026-11-15T07:00:00-05:00",
        "2026-11-15T12:00:00",                 # naive: pool API is UTC
        1794744000,
        1794744000.0,
    ])
    def test_timestamp_formats(self, ts):
        assert sentinel.celebration_seconds_remaining(7200, ts, now=FOUND_TS + 60) in (7140, 7139)

    @pytest.mark.parametrize("bad", [None, "", "not a date", "2026-13-45T99:00:00Z", True, 0, -5, [], {}])
    def test_unknown_found_time_means_unknown(self, bad):
        # None tells the caller to fall back to the full duration, as before 3.0.3.
        assert sentinel.celebration_seconds_remaining(7200, bad, now=FOUND_TS) is None


# ── trigger_block_celebration(found_at=...) ──────────────────────────────────
@pytest.fixture
def linux_host(monkeypatch, tmp_path):
    """A Linux host with the script and a config.yaml, capturing every Popen."""
    import platform
    import subprocess
    install = tmp_path / "spiralpool"
    (install / "scripts").mkdir(parents=True)
    (install / "config").mkdir()
    (install / "data").mkdir()
    script = install / "scripts" / "block-celebrate.sh"
    script.write_text("#!/bin/bash\n")
    (install / "config" / "config.yaml").write_text("celebration:\n  enabled: true\n  duration_hours: 2\n")
    monkeypatch.setattr(sentinel, "INSTALL_DIR", install)
    monkeypatch.setattr(sentinel, "CELEBRATION_SCRIPT", str(script))
    monkeypatch.setattr(sentinel, "CELEBRATION_STATE_FILE", str(install / "data" / "celebrate.state"))
    monkeypatch.setattr(sentinel, "_last_celebration_recovery", None)
    monkeypatch.setattr(sentinel, "is_quiet_hours", lambda: False)
    monkeypatch.setattr(platform, "system", lambda: "Linux")
    calls = []
    monkeypatch.setattr(subprocess, "Popen", lambda cmd, **kw: calls.append(list(cmd)))
    return install, calls


def _duration_of(cmd):
    return int(cmd[cmd.index("--duration") + 1])


class TestTriggerWithFoundAt:
    def test_without_found_at_the_full_duration_is_used(self, linux_host):
        _, calls = linux_host
        sentinel.trigger_block_celebration({}, block_hash="h1")
        assert len(calls) == 1 and _duration_of(calls[0]) == 7200
        assert calls[0][-2:] == ["--block", "h1"]

    def test_block_found_30m_before_restart_gets_90m(self, linux_host, monkeypatch):
        _, calls = linux_host
        monkeypatch.setattr(sentinel.time, "time", lambda: FOUND_TS + 1800)
        sentinel.trigger_block_celebration({}, block_hash="h1", found_at=FOUND)
        assert len(calls) == 1 and _duration_of(calls[0]) == 5400

    def test_block_whose_window_ended_before_restart_is_not_lit(self, linux_host, monkeypatch):
        _, calls = linux_host
        monkeypatch.setattr(sentinel.time, "time", lambda: FOUND_TS + 3 * 3600)
        sentinel.trigger_block_celebration({}, block_hash="h1", found_at=FOUND)
        assert calls == []

    def test_unparseable_found_at_falls_back_to_full_duration(self, linux_host):
        _, calls = linux_host
        sentinel.trigger_block_celebration({}, block_hash="h1", found_at="garbage")
        assert len(calls) == 1 and _duration_of(calls[0]) == 7200


# ── recover_block_celebration ────────────────────────────────────────────────
class TestRecoveryHook:
    def test_no_record_no_process(self, linux_host):
        _, calls = linux_host
        assert sentinel.recover_block_celebration() is False
        assert calls == []

    def test_record_present_runs_recover(self, linux_host):
        install, calls = linux_host
        (install / "data" / "celebrate.state").write_text("format=1\n")
        assert sentinel.recover_block_celebration() is True
        assert calls == [[str(install / "scripts" / "block-celebrate.sh"), "--recover"]]

    def test_first_call_runs_even_right_after_boot(self, linux_host, monkeypatch):
        # time.monotonic() is small just after boot - exactly when a power-cut
        # recovery is needed. "Never run" must not be confused with "ran at 0".
        install, calls = linux_host
        (install / "data" / "celebrate.state").write_text("format=1\n")
        monkeypatch.setattr(sentinel.time, "monotonic", lambda: 12.0)
        assert sentinel.recover_block_celebration() is True

    def test_rate_limited_while_the_record_persists(self, linux_host, monkeypatch):
        install, calls = linux_host
        (install / "data" / "celebrate.state").write_text("format=1\n")
        clock = {"t": 1000.0}
        monkeypatch.setattr(sentinel.time, "monotonic", lambda: clock["t"])
        assert sentinel.recover_block_celebration() is True
        for dt in (30, 120, 299):
            clock["t"] = 1000.0 + dt
            assert sentinel.recover_block_celebration() is False
        clock["t"] = 1000.0 + 300
        assert sentinel.recover_block_celebration() is True
        assert len(calls) == 2

    def test_not_linux_does_nothing(self, linux_host, monkeypatch):
        import platform
        install, calls = linux_host
        (install / "data" / "celebrate.state").write_text("format=1\n")
        monkeypatch.setattr(platform, "system", lambda: "Windows")
        assert sentinel.recover_block_celebration() is False
        assert calls == []

    def test_spawn_failure_never_raises(self, linux_host, monkeypatch):
        import subprocess
        install, _ = linux_host
        (install / "data" / "celebrate.state").write_text("format=1\n")

        def boom(*a, **k):
            raise OSError("exec format error")
        monkeypatch.setattr(subprocess, "Popen", boom)
        assert sentinel.recover_block_celebration() is False

    def test_state_path_matches_the_script_default(self):
        # The two halves only work together if they agree on where the record is.
        script = os.path.join(os.path.dirname(__file__), "..", "scripts", "linux", "block-celebrate.sh")
        with open(script, encoding="utf-8") as f:
            src = f.read()
        assert 'CELEBRATE_STATE_FILE:-/spiralpool/data/celebrate.state' in src
        assert sentinel.CELEBRATION_STATE_FILE.replace("\\", "/").endswith("data/celebrate.state")
