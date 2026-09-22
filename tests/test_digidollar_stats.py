# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""DigiDollar stats: correct numbers, and never at the cost of block production.

The safety property is the first test and the reason the rest exist. DigiByte
force-disables the DigiDollar stats index on a pruned node (init.cpp: "-prune
set -> setting -digidollarstatsindex=0"), and getdigidollarstats then falls back
to ForceFlushStateToDisk() plus a full UTXO scan while holding cs_main. On a
mining node that stalls getblocktemplate for the length of the scan. Spiral Pool
offers DGB pruning, so a panel that simply called the RPC would cost blocks on
exactly the pools that opted into pruning to save disk.

Run: python -m pytest tests/test_digidollar_stats.py -v
"""
import os
import sys

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", os.path.join(ROOT, "config"))

import dashboard  # noqa: E402


# A full, healthy answer in the shape DigiByte v9.26.5 documents for
# getdigidollarstats: supply in cents, oracle price in micro-USD.
HEALTHY = {
    "health_percentage": 152.5,
    "health_status": "healthy",
    "total_collateral_dgb": 1234567.0,
    "total_dd_supply": 456789,          # cents -> $4,567.89
    "oracle_price_cents": 1,
    "oracle_price_micro_usd": 12345,    # -> $0.012345
    "oracle_available": True,
    "oracle_status": "available",
    "minting_restricted_reason": "none",
    "is_emergency": False,
    "system_collateral_ratio": 152.5,
    "total_collateral_locked": 123456700000000,
    "active_positions": 42,
    "oracle_price_age": 3,
    "dca_tier": {"min_collateral": 150, "max_collateral": 200,
                 "multiplier": 1.0, "status": "normal"},
    "err_tier": {"ratio": 1.0, "burn_multiplier": 1.0, "description": "no emergency"},
}


class FakeRPC:
    """Records every call, answers from a table, raises for error entries."""

    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __call__(self, symbol, method, params=None, wallet=None, timeout=10, raise_error=False):
        self.calls.append(method)
        answer = self.responses.get(method)
        if isinstance(answer, Exception):
            if raise_error:
                raise answer
            return None
        return answer

    @property
    def methods(self):
        return list(self.calls)


@pytest.fixture(autouse=True)
def clean_cache():
    """Each test starts with an empty cache, so none can pass on another's answer."""
    dashboard._digidollar_cache["at"] = 0.0
    dashboard._digidollar_cache["payload"] = None
    yield
    dashboard._digidollar_cache["at"] = 0.0
    dashboard._digidollar_cache["payload"] = None


def install(monkeypatch, responses, enabled=("DGB",)):
    rpc = FakeRPC(responses)
    monkeypatch.setattr(dashboard, "coin_rpc", rpc)
    monkeypatch.setattr(dashboard, "get_enabled_coins", lambda: {"enabled": list(enabled)})
    return rpc


# ───────────────────────── the safety property ─────────────────────────

# Every DigiDollar RPC that routes through GetDigiDollarRpcTotals or
# GetDigiDollarRpcSystemHealth, both of which fall back to a UTXO-set scan
# under cs_main when the stats index is absent. Read from DigiByte v9.26.5's
# src/rpc/digidollar.cpp; getdcamultiplier and calculatecollateralrequirement
# reach it indirectly, which a first reading of the file missed.
UTXO_SCANNING_RPCS = (
    "getdigidollarstats",
    "getprotectionstatus",
    "getdcamultiplier",
    "calculatecollateralrequirement",
)

ORACLE = {
    "price_micro_usd": 12345,
    "price_cents": 1,
    "price_usd": 0.012345,
    "last_update_height": 23999997,
    "last_update_time": 1789600000,
    "validity_blocks": 40,
    "is_stale": False,
    "oracle_count": 5,
    "status": "active",
    "24h_high": 2, "24h_low": 1, "volatility": 3.5,
}

DEPLOYMENT = {
    "enabled": True,
    "type": "buried",
    "status": "active",
    "activation_height": 23869440,
    "musig2_session": {"epoch": 12, "state": "complete", "nonce_count": 5,
                       "partial_sig_count": 5, "creation_height": 23999990},
}

PRUNED_NODE = {
    "getblockchaininfo": {"pruned": True, "blocks": 24000000},
    "getoracleprice": ORACLE,
    "getdigidollardeploymentinfo": DEPLOYMENT,
    # Present and must go untouched — each would scan the UTXO set here.
    "getdigidollarstats": HEALTHY,
    "getprotectionstatus": {"whatever": 1},
    "getdcamultiplier": {"multiplier": 1.0},
    "calculatecollateralrequirement": {"required": 1},
}


def test_a_pruned_node_is_never_asked_anything_that_scans_the_utxo_set(monkeypatch):
    """The whole point. None of the four expensive RPCs may be sent."""
    rpc = install(monkeypatch, PRUNED_NODE)

    dashboard.digidollar_stats()

    for method in UTXO_SCANNING_RPCS:
        assert method not in rpc.methods, (
            "a pruned node was asked %s -- it rescans the whole UTXO set under "
            "cs_main and would stall block template production" % method)


def test_a_pruned_node_still_reports_what_it_can_answer_cheaply(monkeypatch):
    """Unavailable was over-broad: six of the read-only RPCs cost the node nothing."""
    rpc = install(monkeypatch, PRUNED_NODE)

    result = dashboard.digidollar_stats()

    assert result["available"] is True
    assert result["partial"] is True
    assert result["partial_reason"] == "pruned"
    assert "getoracleprice" in rpc.methods
    assert result["oracle"]["price_usd"] == pytest.approx(0.012345)
    assert result["oracle"]["oracle_count"] == 5
    assert result["oracle"]["is_stale"] is False
    # 24,000,000 tip minus a price set at 23,999,997.
    assert result["oracle"]["price_age_blocks"] == 3
    assert result["deployment"]["enabled"] is True
    assert result["deployment"]["activation_height"] == 23869440
    assert result["deployment"]["musig2_state"] == "complete"


def test_a_pruned_node_reports_no_aggregate_it_cannot_compute(monkeypatch):
    """Absent, not zero. A zero supply would read as a collapsed stablecoin."""
    install(monkeypatch, PRUNED_NODE)

    result = dashboard.digidollar_stats()

    for absent in ("health_percentage", "dd_supply_usd", "total_collateral_dgb",
                   "active_positions", "dca_tier", "err_tier"):
        assert absent not in result, "%s cannot be known on a pruned node" % absent


def test_a_stale_oracle_is_reported_as_stale(monkeypatch):
    """The figure a mining operator actually needs: a stale oracle means blocks
    carry no price bundle, which is a block-production fact, not a market one."""
    install(monkeypatch, dict(PRUNED_NODE,
                              getoracleprice=dict(ORACLE, is_stale=True, status="warning")))

    result = dashboard.digidollar_stats()

    assert result["oracle"]["is_stale"] is True
    assert result["oracle"]["available"] is False
    assert result["oracle"]["status"] == "warning"


def test_a_pruned_node_with_deployment_info_missing_still_reports_the_oracle(monkeypatch):
    """getdigidollardeploymentinfo is a bonus; losing it must not lose the panel."""
    install(monkeypatch, dict(PRUNED_NODE, getdigidollardeploymentinfo=None))

    result = dashboard.digidollar_stats()

    assert result["available"] is True
    assert result["oracle"]["price_usd"] == pytest.approx(0.012345)
    assert result["deployment"]["status"] == "unknown"


def test_a_full_node_is_marked_as_not_partial(monkeypatch):
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": HEALTHY,
    })

    assert dashboard.digidollar_stats()["partial"] is False


def test_an_unreachable_node_is_not_asked_either(monkeypatch):
    """No chain info means no verdict on pruning, so the stats RPC stays unsent."""
    rpc = install(monkeypatch, {"getblockchaininfo": None, "getdigidollarstats": HEALTHY})

    result = dashboard.digidollar_stats()

    assert "getdigidollarstats" not in rpc.methods
    assert result["available"] is False
    assert result["reason"] == "offline"


def test_no_digibyte_node_means_no_rpc_at_all(monkeypatch):
    rpc = install(monkeypatch, {"getblockchaininfo": {"pruned": False}}, enabled=("BTC", "LTC"))

    result = dashboard.digidollar_stats()

    assert rpc.methods == []
    assert result["available"] is False
    assert result["reason"] == "no_node"


# ───────────────────────── the numbers ─────────────────────────

def test_a_full_node_reports_the_networks_health(monkeypatch):
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False, "blocks": 24000000},
        "getdigidollarstats": HEALTHY,
    })

    result = dashboard.digidollar_stats()

    assert result["available"] is True
    assert result["coin"] == "DGB"
    assert result["health_percentage"] == 152.5
    assert result["health_status"] == "healthy"
    assert result["is_emergency"] is False
    assert result["active_positions"] == 42
    assert result["total_collateral_dgb"] == 1234567.0


def test_supply_is_converted_from_cents_and_price_from_micro_usd(monkeypatch):
    """The two unit conversions the RPC's own documentation specifies."""
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": HEALTHY,
    })

    result = dashboard.digidollar_stats()

    assert result["dd_supply_usd"] == pytest.approx(4567.89)
    assert result["oracle"]["price_usd"] == pytest.approx(0.012345)
    assert result["oracle"]["available"] is True
    assert result["oracle"]["price_age_blocks"] == 3


def test_tier_objects_survive_and_a_missing_one_is_none(monkeypatch):
    stats = dict(HEALTHY)
    stats.pop("err_tier")
    install(monkeypatch, {"getblockchaininfo": {"pruned": False}, "getdigidollarstats": stats})

    result = dashboard.digidollar_stats()

    assert result["dca_tier"]["multiplier"] == 1.0
    assert result["err_tier"] is None


def test_garbage_numbers_do_not_raise(monkeypatch):
    """A field the node reports as null or a string must not 500 the panel."""
    stats = dict(HEALTHY, health_percentage=None, total_dd_supply="n/a",
                 oracle_price_micro_usd=None, active_positions=None)
    install(monkeypatch, {"getblockchaininfo": {"pruned": False}, "getdigidollarstats": stats})

    result = dashboard.digidollar_stats()

    assert result["available"] is True
    assert result["health_percentage"] == 0.0
    assert result["dd_supply_usd"] == 0.0
    assert result["oracle"]["price_usd"] == 0.0
    assert result["active_positions"] == 0


def test_an_emergency_is_reported_as_one(monkeypatch):
    stats = dict(HEALTHY, health_percentage=87.0, health_status="emergency",
                 is_emergency=True, minting_restricted_reason="err_active")
    install(monkeypatch, {"getblockchaininfo": {"pruned": False}, "getdigidollarstats": stats})

    result = dashboard.digidollar_stats()

    assert result["is_emergency"] is True
    assert result["health_status"] == "emergency"
    assert result["minting_restricted_reason"] == "err_active"


# ───────────────────────── the failure modes stay distinct ─────────────────────────

def test_not_active_is_not_the_same_as_broken(monkeypatch):
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": dashboard.CoinRPCError(
            "DigiDollar is not yet active on this blockchain"),
    })

    result = dashboard.digidollar_stats()

    assert result["available"] is False
    assert result["reason"] == "inactive"


def test_a_syncing_index_says_so(monkeypatch):
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": dashboard.CoinRPCError(
            "DigiDollar stats index is syncing. Current height: 23000000"),
    })

    result = dashboard.digidollar_stats()

    assert result["available"] is False
    assert result["reason"] == "syncing"
    assert "23000000" in result["detail"]


def test_an_unrecognised_rpc_error_is_carried_through_verbatim(monkeypatch):
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": dashboard.CoinRPCError("Method not found"),
    })

    result = dashboard.digidollar_stats()

    assert result["available"] is False
    assert result["reason"] == "error"
    assert result["detail"] == "Method not found"


# ───────────────────────── caching ─────────────────────────

def test_a_second_read_inside_the_ttl_does_not_touch_the_node(monkeypatch):
    rpc = install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": HEALTHY,
    })

    first = dashboard.digidollar_stats()
    calls_after_first = len(rpc.methods)
    second = dashboard.digidollar_stats()

    assert len(rpc.methods) == calls_after_first
    assert second is first


def test_force_refresh_reaches_the_node_again(monkeypatch):
    rpc = install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": HEALTHY,
    })

    dashboard.digidollar_stats()
    calls_after_first = len(rpc.methods)
    dashboard.digidollar_stats(force=True)

    assert len(rpc.methods) > calls_after_first


def test_a_pruned_verdict_is_cached_too(monkeypatch):
    """Re-probing a pruned node every poll would be pointless load."""
    rpc = install(monkeypatch, {"getblockchaininfo": {"pruned": True}})

    dashboard.digidollar_stats()
    dashboard.digidollar_stats()

    assert rpc.methods.count("getblockchaininfo") == 1


# ───────────────────────── node selection ─────────────────────────

def test_a_scrypt_only_pool_still_finds_the_chain(monkeypatch):
    """DGB and DGB-SCRYPT are one blockchain, so either node can answer."""
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": HEALTHY,
    }, enabled=("DGB-SCRYPT",))

    result = dashboard.digidollar_stats()

    assert result["available"] is True
    assert result["coin"] == "DGB-SCRYPT"


def test_dgb_is_preferred_when_both_are_enabled(monkeypatch):
    install(monkeypatch, {
        "getblockchaininfo": {"pruned": False},
        "getdigidollarstats": HEALTHY,
    }, enabled=("DGB-SCRYPT", "DGB"))

    assert dashboard.digidollar_stats()["coin"] == "DGB"


# ───────────────────────── the coin_rpc change itself ─────────────────────────

def test_coin_rpc_still_returns_none_by_default(monkeypatch):
    """raise_error is opt-in: every existing caller must be unaffected."""
    class Response:
        status_code = 200

        @staticmethod
        def json():
            return {"error": {"code": -1, "message": "boom"}}

    monkeypatch.setitem(dashboard.MULTI_COIN_NODES["DGB"], "rpc_user", "u")
    monkeypatch.setitem(dashboard.MULTI_COIN_NODES["DGB"], "rpc_password", "p")
    monkeypatch.setattr(dashboard.requests, "post", lambda *a, **k: Response())

    assert dashboard.coin_rpc("DGB", "anything") is None
    with pytest.raises(dashboard.CoinRPCError) as caught:
        dashboard.coin_rpc("DGB", "anything", raise_error=True)
    assert "boom" in str(caught.value)
