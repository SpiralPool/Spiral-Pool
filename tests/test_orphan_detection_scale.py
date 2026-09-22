# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""check_for_orphans must survive a multi-coin pool.

Two defects, both invisible on a single-coin install and both silent:

1. `block.get("coin", get_primary_coin() or "UNKNOWN")` evaluates its default
   eagerly, so get_primary_coin() runs once per block even though "coin" is
   always present on a multi-coin pool. That call reaches auto_detect_pool_coin(),
   an HTTP GET with timeout=10 which one branch of get_enabled_coins never
   caches -- so a slow or dead pool API stalls the monitor loop for minutes
   inside a single orphan check.

2. known_block_statuses is one dict shared by every pool, pruned to the newest
   100 entries at the end of EVERY call. Three coins returning 50 blocks each
   overflow it within one cycle, so each pool evicts the pool before it. The
   evicted blocks look new next cycle, take the `continue` path, and their
   pending -> orphaned transition is never compared. Pruning drops the
   oldest-tracked entries first -- the long-pending blocks likeliest to orphan.

Run: python -m pytest tests/test_orphan_detection_scale.py -v
"""
import importlib.util
import os
import tempfile

os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel_orphan_scale",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

COINS = ["DGB", "LTC", "DOGE"]
PER_POOL = 50


class _Clock:
    """Stand-in for the `time` module inside Sentinel only.

    first_seen ordering decides what the prune keeps, and Windows time.time()
    has ~15ms resolution -- a whole batch lands on one value and the order
    becomes arbitrary. Patching the real time module instead would reach every
    library in the process, including socket timeouts; this replaces only the
    name Sentinel resolves, and delegates everything else to the real module.
    """

    def __init__(self):
        self._t = 1_000_000.0

    def time(self):
        self._t += 1.0
        return self._t

    def __getattr__(self, name):
        import time as _real
        return getattr(_real, name)


def _state(monkeypatch, coin_calls=None):
    st = sentinel.MonitorState.__new__(sentinel.MonitorState)
    st.known_block_statuses = {}
    st.orphan_alerts_sent = set()
    monkeypatch.setattr(sentinel, "time", _Clock())
    # No test may reach the real get_primary_coin(): it can issue an HTTP GET
    # with a 10s timeout per call, which is the defect under test here and would
    # hang the suite rather than fail it. Tests that care count the calls.
    calls = coin_calls if coin_calls is not None else []
    monkeypatch.setattr(sentinel, "get_primary_coin",
                        lambda: (calls.append(1), "DGB")[1])
    return st


def _blocks(coin, status_of_first="confirmed"):
    return [{"hash": f"{coin}-hash-{i:03d}",
             "status": status_of_first if i == 0 else "confirmed",
             "blockHeight": 1000 + i,
             "coin": coin,
             "created": "2026-09-18T23:32:07.750926Z",
             "reward": 250.0}
            for i in range(PER_POOL)]


def _serve(monkeypatch, table):
    monkeypatch.setattr(sentinel, "fetch_pool_blocks",
                        lambda limit=50, pool_id=None: table.get(pool_id, []))


def test_a_present_coin_field_costs_no_pool_api_calls(monkeypatch):
    """The pool API must not be consulted once per block.

    Every block here carries "coin", so the fallback value is never used. A call
    to get_primary_coin() is therefore pure waste -- and it is not cheap waste:
    it can reach an HTTP GET with a 10 second timeout.
    """
    calls = []
    st = _state(monkeypatch, coin_calls=calls)
    _serve(monkeypatch, {"dgb_pool": _blocks("DGB")})

    st.check_for_orphans(pool_id="dgb_pool")
    assert calls == [], (
        f"get_primary_coin() ran {len(calls)} times for {PER_POOL} blocks that "
        f"all carry a coin field")


def test_the_fallback_still_works_when_coin_is_absent(monkeypatch):
    """Removing the eager call must not remove the fallback itself."""
    st = _state(monkeypatch)
    blocks = _blocks("DGB")
    for b in blocks:
        del b["coin"]
    _serve(monkeypatch, {"solo": blocks})
    monkeypatch.setattr(sentinel, "get_primary_coin", lambda: "DGB")

    st.check_for_orphans(pool_id="solo")
    coins = {e["coin"] for e in st.known_block_statuses.values()}
    assert coins == {"DGB"}, f"fallback did not supply the coin: {coins}"


def test_fallback_survives_an_unreachable_pool_api(monkeypatch):
    """get_primary_coin() returning None must degrade, not crash."""
    st = _state(monkeypatch)
    blocks = _blocks("DGB")
    for b in blocks:
        del b["coin"]
    _serve(monkeypatch, {"solo": blocks})
    monkeypatch.setattr(sentinel, "get_primary_coin", lambda: None)

    st.check_for_orphans(pool_id="solo")
    coins = {e["coin"] for e in st.known_block_statuses.values()}
    assert coins == {"UNKNOWN"}, coins


def test_three_coins_do_not_evict_each_other(monkeypatch):
    """The tracking dict must not be emptied of a pool by its siblings.

    3 x 50 blocks overflows the 100-entry cap inside a single cycle, so the
    first pool checked is evicted by the last. Next cycle its blocks read as
    never-seen and orphan detection for that coin is dead.
    """
    st = _state(monkeypatch)
    _serve(monkeypatch, {f"{c.lower()}_pool": _blocks(c) for c in COINS})

    for c in COINS:
        st.check_for_orphans(pool_id=f"{c.lower()}_pool")

    retained = {c: sum(1 for h in st.known_block_statuses if h.startswith(c)) for c in COINS}
    assert all(retained[c] == PER_POOL for c in COINS), (
        f"pools evicted one another within one cycle: {retained}")


def test_an_orphan_on_the_first_coin_is_still_detected(monkeypatch):
    """The end-to-end miss: the whole point of the method.

    Cycle 1 records every block as pending. Cycle 2 flips the first pool's
    block to orphaned. If that pool was evicted, the block reads as new, takes
    the `continue`, and the transition is never compared -- silently, with no
    error anywhere.
    """
    st = _state(monkeypatch)
    table = {f"{c.lower()}_pool": _blocks(c, status_of_first="pending") for c in COINS}
    _serve(monkeypatch, table)
    for c in COINS:
        st.check_for_orphans(pool_id=f"{c.lower()}_pool")

    # The first coin checked orphans a block.
    table["dgb_pool"][0]["status"] = "orphaned"
    alerts = []
    for c in COINS:
        alerts += st.check_for_orphans(pool_id=f"{c.lower()}_pool")

    assert [a["coin"] for a in alerts] == ["DGB"], (
        f"orphan on the first-checked pool was missed; alerts={alerts}")


def test_tracking_stays_bounded(monkeypatch):
    """The cap exists for a reason: memory must not grow without limit."""
    st = _state(monkeypatch)
    for cycle in range(6):
        table = {}
        for c in COINS:
            table[f"{c.lower()}_pool"] = [
                {"hash": f"{c}-c{cycle}-{i:03d}", "status": "confirmed",
                 "blockHeight": i, "coin": c, "created": "", "reward": 1.0}
                for i in range(PER_POOL)]
        _serve(monkeypatch, table)
        for c in COINS:
            st.check_for_orphans(pool_id=f"{c.lower()}_pool")

    assert len(st.known_block_statuses) <= 100 * len(COINS), (
        f"tracking grew unbounded: {len(st.known_block_statuses)} entries")
