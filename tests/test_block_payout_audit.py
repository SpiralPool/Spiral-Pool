# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Sentinel checks that a found block's coinbase actually paid this pool.

A solo pool finds a block every few weeks or months, and that block is the
entire product. Nothing verified where its coinbase paid. An address edited in
config that a running stratum never picked up, a coin configured with another
coin's address, a wallet reloaded under a different label — each gives a pool
that mines perfectly, alerts nothing, and pays somebody else. It is invisible
because it can only show on the rarest event the pool has, and by then the
block is already on the chain and cannot be taken back.

The second thing these pin is restraint. Per-worker payout and a multi-port
wallet map both mean a block legitimately pays an address other than the coin's
configured one. An audit that cried wolf on those would be muted long before it
ever caught a real misdirected block, which would leave the pool worse off than
having no audit at all.

Run: python -m pytest tests/test_block_payout_audit.py -v
"""
import importlib.util
import os
import tempfile

import pytest

_INSTALL_DIR = tempfile.mkdtemp()
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", _INSTALL_DIR)
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel_payout",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

OURS = "DTE2HHLNh1TNJzPaZb5uf6fkxxqYiLuGLB"
THEIRS = "DFpN6QqFfUm3gKwBLBjRqYQ9PUhKVpTWEp"
BLOCK = "a" * 64


def _state(statuses=None):
    st = sentinel.MonitorState.__new__(sentinel.MonitorState)
    st.last_alerts = {}
    st.known_block_statuses = statuses if statuses is not None else {}
    return st


def _entry(coin="DGB", height=24240943):
    return {"status": "confirmed", "height": height, "coin": coin,
            "reward": 250.72839494, "first_seen": 0, "pool": "dgb_sha256_1"}


def _coinbase(outputs, address_key="address"):
    """A getblock verbosity-2 reply whose first tx is the coinbase."""
    vout = []
    for n, (addr, value) in enumerate(outputs):
        spk = {}
        if addr is not None:
            spk[address_key] = addr if address_key == "address" else [addr]
        vout.append({"n": n, "value": value, "scriptPubKey": spk})
    return {"height": 24240943, "tx": [{"vout": vout}]}


@pytest.fixture(autouse=True)
def base_config(monkeypatch):
    """One DGB coin paying OURS, audit on, no per-worker payout."""
    monkeypatch.setitem(sentinel.CONFIG, "block_payout_audit_enabled", True)
    monkeypatch.setitem(sentinel.CONFIG, "coins", [
        {"symbol": "DGB", "wallet_address": OURS, "rpc_port": 14022},
    ])
    monkeypatch.setitem(sentinel.CONFIG, "wallet_address", "")
    monkeypatch.setattr(sentinel, "get_primary_coin", lambda: "DGB")
    yield


def _capture_alerts(monkeypatch):
    # The audit tests are about the coinbase, not the switch; the switch has
    # its own tests below, which need the real function.
    monkeypatch.setattr(sentinel, "_per_worker_payout_configured", lambda: False)
    sent = []
    monkeypatch.setattr(sentinel, "send_alert",
                        lambda t, e, s=None, **kw: (sent.append((t, e)), True)[1])
    return sent


def _rpc(monkeypatch, reply, record=None):
    def fake(host, port, method, params=None, timeout=10, auth=None):
        if record is not None:
            record.append((method, params))
        return reply
    monkeypatch.setattr(sentinel, "_rpc_call", fake)


# ───────────────────────── the audit itself ─────────────────────────

def test_a_block_that_paid_us_raises_nothing(monkeypatch):
    sent = _capture_alerts(monkeypatch)
    _rpc(monkeypatch, _coinbase([(OURS, 250.72839494), (None, 0.0)]))
    statuses = {BLOCK: _entry()}

    sentinel.check_block_payouts(_state(statuses))

    assert sent == []
    assert statuses[BLOCK]["payout_audited"] is True


def test_a_block_that_paid_someone_else_alerts(monkeypatch):
    """The whole point: the block is on the chain and the money went elsewhere."""
    sent = _capture_alerts(monkeypatch)
    _rpc(monkeypatch, _coinbase([(THEIRS, 250.72839494), (None, 0.0)]))

    sentinel.check_block_payouts(_state({BLOCK: _entry()}))

    assert len(sent) == 1
    alert_type, embed = sent[0]
    assert alert_type == "block_payout_mismatch"
    body = str(embed)
    assert THEIRS in body, "the alert must name what was actually paid"
    assert OURS in body, "and what was expected, so the verdict can be checked"


def test_a_node_that_could_not_be_read_is_not_a_mismatch(monkeypatch):
    """None means 'could not ask', which must never read as 'paid nobody'."""
    sent = _capture_alerts(monkeypatch)
    _rpc(monkeypatch, None)
    statuses = {BLOCK: _entry()}

    sentinel.check_block_payouts(_state(statuses))

    assert sent == []
    assert "payout_audited" not in statuses[BLOCK], (
        "an unreadable block must stay unaudited so the check retries")


def test_zero_value_outputs_are_commitments_not_payments(monkeypatch):
    """SegWit and the DigiDollar oracle bundle both ride in zero-value outputs."""
    _rpc(monkeypatch, _coinbase([(OURS, 250.0), (None, 0.0), (None, 0.0)]))

    audited = sentinel.audit_block_coinbase("DGB", BLOCK)

    assert audited["commitment_outputs"] == 2
    assert audited["paid"] == {OURS: 250.0}
    assert audited["total"] == 250.0


def test_both_daemon_address_shapes_are_read(monkeypatch):
    """Core 22+ reports "address"; older daemons report "addresses". We run both."""
    _rpc(monkeypatch, _coinbase([(OURS, 250.0)], address_key="addresses"))
    assert sentinel.audit_block_coinbase("DGB", BLOCK)["paid"] == {OURS: 250.0}

    _rpc(monkeypatch, _coinbase([(OURS, 250.0)], address_key="address"))
    assert sentinel.audit_block_coinbase("DGB", BLOCK)["paid"] == {OURS: 250.0}


def test_getblock_is_actually_permitted(monkeypatch):
    """_rpc_call drops any method not on its whitelist, silently returning None,
    which this audit would read as 'node unreadable' forever."""
    assert "getblock" in sentinel._RPC_ALLOWED_METHODS


# ───────────────────────── restraint ─────────────────────────

def test_per_worker_payout_skips_the_audit_entirely(monkeypatch):
    """A worker legitimately paid at its own address is not a misdirected block."""
    sent = _capture_alerts(monkeypatch)
    calls = []
    _rpc(monkeypatch, _coinbase([(THEIRS, 250.0)]), record=calls)
    monkeypatch.setattr(sentinel, "_per_worker_payout_configured", lambda: True)

    sentinel.check_block_payouts(_state({BLOCK: _entry()}))

    assert sent == []
    assert calls == [], "the node should not even be asked"


def test_an_unconfigured_address_is_not_a_finding(monkeypatch):
    """A pool half-way through setup must not be told its blocks are misdirected."""
    sent = _capture_alerts(monkeypatch)
    monkeypatch.setitem(sentinel.CONFIG, "coins", [
        {"symbol": "DGB", "wallet_address": "YOUR_DGB_ADDRESS", "rpc_port": 14022},
    ])
    _rpc(monkeypatch, _coinbase([(THEIRS, 250.0)]))

    sentinel.check_block_payouts(_state({BLOCK: _entry()}))

    assert sent == []


def test_a_block_is_audited_once(monkeypatch):
    """Re-reading every known block each cycle is load for no new information."""
    _capture_alerts(monkeypatch)
    calls = []
    _rpc(monkeypatch, _coinbase([(OURS, 250.0)]), record=calls)
    statuses = {BLOCK: _entry()}
    state = _state(statuses)

    sentinel.check_block_payouts(state)
    sentinel.check_block_payouts(state)

    assert len(calls) == 1


def test_the_audit_can_be_switched_off(monkeypatch):
    sent = _capture_alerts(monkeypatch)
    calls = []
    _rpc(monkeypatch, _coinbase([(THEIRS, 250.0)]), record=calls)
    monkeypatch.setitem(sentinel.CONFIG, "block_payout_audit_enabled", False)

    sentinel.check_block_payouts(_state({BLOCK: _entry()}))

    assert sent == [] and calls == []


def test_one_bad_block_does_not_mute_the_next(monkeypatch):
    """The cooldown is keyed per block hash. Two misdirected blocks are two
    findings — the second is not a repeat of the first."""
    sent = _capture_alerts(monkeypatch)
    _rpc(monkeypatch, _coinbase([(THEIRS, 250.0)]))
    other = "b" * 64

    state = _state({BLOCK: _entry(), other: _entry(height=24240944)})
    sentinel.check_block_payouts(state)

    assert len(sent) == 2


# ───────────────── reading the stratum's own switch ─────────────────

def _write_stratum_config(text):
    cfg_dir = os.path.join(_INSTALL_DIR, "config")
    os.makedirs(cfg_dir, exist_ok=True)
    with open(os.path.join(cfg_dir, "config.yaml"), "w", encoding="utf-8") as f:
        f.write(text)


def test_payout_from_worker_name_is_detected(monkeypatch):
    monkeypatch.setattr(sentinel, "INSTALL_DIR", sentinel.Path(_INSTALL_DIR))
    _write_stratum_config("stratum:\n  payout_from_worker_name: true\n")
    assert sentinel._per_worker_payout_configured() is True


def test_a_commented_out_switch_is_not_detected(monkeypatch):
    """A commented example must not disable the audit for everyone."""
    monkeypatch.setattr(sentinel, "INSTALL_DIR", sentinel.Path(_INSTALL_DIR))
    _write_stratum_config("stratum:\n  # payout_from_worker_name: true\n")
    assert sentinel._per_worker_payout_configured() is False


def test_a_wallet_map_also_suspends_the_audit(monkeypatch):
    monkeypatch.setattr(sentinel, "INSTALL_DIR", sentinel.Path(_INSTALL_DIR))
    _write_stratum_config("multi_port:\n  wallet_map:\n    rig1:\n      DGB: addr\n")
    assert sentinel._per_worker_payout_configured() is True


def test_a_plain_config_leaves_the_audit_running(monkeypatch):
    monkeypatch.setattr(sentinel, "INSTALL_DIR", sentinel.Path(_INSTALL_DIR))
    _write_stratum_config("stratum:\n  port: 4334\n")
    assert sentinel._per_worker_payout_configured() is False


def test_a_missing_config_leaves_the_audit_running(monkeypatch):
    monkeypatch.setattr(sentinel, "INSTALL_DIR",
                        sentinel.Path(os.path.join(_INSTALL_DIR, "nope")))
    assert sentinel._per_worker_payout_configured() is False
