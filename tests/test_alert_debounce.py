"""Proof tests for the v2.5.3 Sentinel alert false-positive / debounce fixes.

Covers the new cross-references, confirmation counters, and debounce helpers added to
SpiralSentinel.py so the false-positive fixes can't silently regress:
  - compute_pool_side_reject_pct()  — pool-side reject cross-reference (zombie/rejection gate)
  - _debounce_ha_scalar()           — VIP/state change debounce
  - MonitorState.track_chronic_issue — per-(miner,type) hourly count throttle
  - MonitorState.check_price_crash   — two-sample confirmation
  - handle_coin_health_alerts        — consecutive-failure streak + suppressed-retry

Run: python -m pytest tests/test_alert_debounce.py -v
"""
import importlib.util
import os
import tempfile

# Import SpiralSentinel as a module from its file path (it is a script, not a package).
# A throwaway install dir keeps its config/logging bootstrap off the real filesystem.
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)


def _state():
    """A bare MonitorState without running __init__ (which does file I/O)."""
    return sentinel.MonitorState.__new__(sentinel.MonitorState)


class Clock:
    """Controllable replacement for time.time() during a test."""
    def __init__(self, start=1_000_000.0):
        self.t = start
        self._orig = sentinel.time.time

    def __enter__(self):
        sentinel.time.time = lambda: self.t
        return self

    def __exit__(self, *a):
        sentinel.time.time = self._orig


class TestComputePoolSideRejectPct:
    f = staticmethod(lambda *a, **k: sentinel.compute_pool_side_reject_pct(*a, **k))

    def test_none_or_empty_metrics(self):
        assert self.f(None) is None
        assert self.f({}) is None

    def test_low_volume_returns_none(self):
        # <=100 pool shares is too little to compute a reliable rate
        assert self.f({"stratum_shares_accepted_total": 50,
                       "stratum_shares_rejected_total": 5}) is None

    def test_normal_rate(self):
        # NeonCleaver's real pool-side figure (~2.7%) — well below the 5% confirm threshold
        pct = self.f({"stratum_shares_accepted_total": 9730,
                      "stratum_shares_rejected_total": 270})
        assert pct is not None and 2.5 < pct < 2.9
        assert pct < sentinel.POOL_REJECT_CONFIRM_PCT

    def test_stale_shares_excluded(self):
        # All rejects are stale -> true pool-side reject is 0%
        pct = self.f({"stratum_shares_accepted_total": 9000,
                      "stratum_shares_rejected_total": 1000,
                      'stratum_shares_rejected_total{reason="stale"}': 1000})
        assert pct == 0.0


class TestDebounceHaScalar:
    d = staticmethod(lambda *a, **k: sentinel._debounce_ha_scalar(*a, **k))

    def test_seeds_baseline_on_first_observation(self):
        # Regression: a None baseline must seed, or VIP/state changes are never detectable.
        conf, pend, base = self.d(None, None, "VIP-A", 1000.0, 90, "VIP")
        assert conf is None and pend is None and base == "VIP-A"

    def test_blip_within_window_suppressed(self):
        conf, pend, base = self.d(None, "A", "B", 1000.0, 90, "VIP")
        assert pend and base == "A"
        conf, pend, base = self.d(pend, base, "A", 1030.0, 90, "VIP")  # reverted in 30s
        assert conf is None and pend is None and base == "A"

    def test_sustained_change_confirms(self):
        conf, pend, base = self.d(None, "A", "B", 2000.0, 90, "state")
        conf2, pend2, base2 = self.d(pend, base, "B", 2100.0, 90, "state")  # held 100s >= 90
        assert conf2 == ("A", "B") and pend2 is None and base2 == "B"

    def test_third_value_retargets_and_resets_timer(self):
        conf, pend, base = self.d(None, "A", "B", 3000.0, 90, "state")
        conf3, pend3, base3 = self.d(pend, base, "C", 3050.0, 90, "state")
        assert conf3 is None and pend3["new"] == "C" and pend3["detected_at"] == 3050.0 and base3 == "A"


class TestChronicThrottle:
    def test_continuous_detection_counts_hourly(self):
        st = _state(); st.chronic_issues = {}
        sentinel.CHRONIC_COUNT_MIN_INTERVAL = 3600
        with Clock() as clk:
            for _ in range(12):  # 12 cycles, 2 min apart = 24 min
                st.track_chronic_issue("M", "zombie_miner"); clk.t += 120
            assert st.chronic_issues["M:zombie_miner"]["count"] == 1
            clk.t += 3601
            st.track_chronic_issue("M", "zombie_miner")
            assert st.chronic_issues["M:zombie_miner"]["count"] == 2

    def test_miners_tracked_independently(self):
        st = _state(); st.chronic_issues = {}
        with Clock():
            for nm in ("A", "B", "C"):
                st.track_chronic_issue(nm, "zombie_miner")
            assert all(st.chronic_issues[f"{nm}:zombie_miner"]["count"] == 1 for nm in ("A", "B", "C"))

    def test_two_hour_gap_resets_episode(self):
        st = _state(); st.chronic_issues = {}
        with Clock() as clk:
            st.track_chronic_issue("D", "degradation")
            clk.t += 7201  # >2h with no recurrence
            st.track_chronic_issue("D", "degradation")
            assert st.chronic_issues["D:degradation"]["count"] == 1

    def test_meta_alert_fires_at_threshold(self):
        st = _state(); st.chronic_issues = {}
        sentinel.CHRONIC_COUNT_MIN_INTERVAL = 3600
        with Clock() as clk:
            for _ in range(5):
                st.track_chronic_issue("E", "zombie_miner"); clk.t += 3601
            is_chronic, info = st.check_chronic_issues("E")
            assert is_chronic and info["count"] == 5


class TestPriceCrashTwoSample:
    def _mk(self, now, baseline_usd, confirm_usd, current_usd):
        st = _state()
        st.price_history = {"dgb": [
            {"ts": now - 7300, "usd": baseline_usd},
            {"ts": now - 3600, "usd": confirm_usd},
            {"ts": now,        "usd": current_usd},
        ]}
        st.price_crash_last_alert = {}
        return st

    def test_single_dip_does_not_fire(self, monkeypatch):
        monkeypatch.setattr(sentinel, "get_enabled_coins", lambda: [{"symbol": "dgb"}])
        monkeypatch.setattr(sentinel, "PRICE_CRASH_PCT", 15)
        with Clock() as clk:
            st = self._mk(clk.t, baseline_usd=1.0, confirm_usd=1.0, current_usd=0.5)
            assert st.check_price_crash() == []

    def test_sustained_crash_fires(self, monkeypatch):
        monkeypatch.setattr(sentinel, "get_enabled_coins", lambda: [{"symbol": "dgb"}])
        monkeypatch.setattr(sentinel, "PRICE_CRASH_PCT", 15)
        with Clock() as clk:
            st = self._mk(clk.t, baseline_usd=1.0, confirm_usd=0.5, current_usd=0.5)
            crashes = st.check_price_crash()
            assert len(crashes) == 1 and crashes[0]["coin"] == "DGB"


class TestCoinNodeDownStreak:
    def _wire(self, monkeypatch, sent, suppressed):
        monkeypatch.setattr(sentinel, "send_alert",
                            lambda a, e, s=None, **k: (False if suppressed[0] else (sent.append(a) or True)))
        monkeypatch.setattr(sentinel, "create_coin_node_down_embed", lambda s, n: {})
        monkeypatch.setattr(sentinel, "create_coin_node_recovered_embed", lambda s, n: {})
        monkeypatch.setattr(sentinel, "get_coin_name", lambda s: s)
        monkeypatch.setattr(sentinel, "COIN_NODE_DOWN_CONFIRM", 2)
        sentinel._coin_health_state = {}
        sentinel._coin_node_down_streak = {}
        sentinel._coin_node_down_alerted = {}

    @staticmethod
    def _down(): return {"X": {"symbol": "X", "node_up": False, "issues": [{"type": "node_down"}]}}

    @staticmethod
    def _err(): return {"X": {"symbol": "X", "node_up": False, "issues": [{"type": "node_error"}]}}

    @staticmethod
    def _up(): return {"X": {"symbol": "X", "node_up": True, "issues": []}}

    def test_requires_two_failures(self, monkeypatch):
        sent, supp = [], [False]
        self._wire(monkeypatch, sent, supp)
        sentinel.handle_coin_health_alerts(self._down(), None)
        assert sent == []
        sentinel.handle_coin_health_alerts(self._down(), None)
        assert sent == ["coin_node_down"]

    def test_transient_blip_suppressed(self, monkeypatch):
        sent, supp = [], [False]
        self._wire(monkeypatch, sent, supp)
        sentinel.handle_coin_health_alerts(self._down(), None)  # one blip
        sentinel.handle_coin_health_alerts(self._up(), None)    # recovered
        assert sent == []

    def test_error_then_down_still_fires(self, monkeypatch):
        sent, supp = [], [False]
        self._wire(monkeypatch, sent, supp)
        sentinel.handle_coin_health_alerts(self._err(), None)
        sentinel.handle_coin_health_alerts(self._err(), None)
        sentinel.handle_coin_health_alerts(self._down(), None)
        assert sent == ["coin_node_down"]

    def test_suppressed_attempt_is_retried(self, monkeypatch):
        sent, supp = [], [False]
        self._wire(monkeypatch, sent, supp)
        sentinel.handle_coin_health_alerts(self._down(), None)  # streak 1
        supp[0] = True
        sentinel.handle_coin_health_alerts(self._down(), None)  # streak 2, send suppressed
        assert sent == [] and not sentinel._coin_node_down_alerted.get("X")
        supp[0] = False
        sentinel.handle_coin_health_alerts(self._down(), None)  # retry -> fires
        assert sent == ["coin_node_down"]
