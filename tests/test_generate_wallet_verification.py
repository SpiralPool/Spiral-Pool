# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Dashboard "Generate wallet": the node must confirm a generated address is
valid and owned by its wallet before the address is written to config.yaml.

Run: python -m pytest tests/test_generate_wallet_verification.py -v
"""
import os
import sys

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}
ADDRESS = "bs1q" + "a" * 38
CONFIG = 'coins:\n- symbol: BTCS\n  enabled: true\n  address: PENDING_GENERATION\n'


@pytest.fixture
def dash(monkeypatch, tmp_path):
    import dashboard
    config = tmp_path / "config.yaml"
    config.write_text(CONFIG)
    monkeypatch.setattr(dashboard, "AUTH_ENABLED", False)
    monkeypatch.setattr(dashboard, "POOL_CONFIG_PATH", str(config))
    monkeypatch.setattr(dashboard, "check_rate_limit", lambda ip, endpoint: True)
    monkeypatch.setitem(dashboard.MULTI_COIN_NODES, "BTCS", {**dashboard.MULTI_COIN_NODES["BTCS"], "enabled": True})
    return dashboard, config


def node(monkeypatch, dashboard, replies):
    """Fake coin_rpc: replies maps (method, wallet) to the RPC result; missing means an RPC error."""
    calls = []

    def coin_rpc(symbol, method, params=None, wallet=None, timeout=10):
        calls.append((method, wallet))
        return replies.get((method, wallet))
    monkeypatch.setattr(dashboard, "coin_rpc", coin_rpc)
    return calls


def generate(dashboard):
    res = dashboard.app.test_client().post("/api/nodes/BTCS/generate-wallet", environ_base=LOCAL)
    return res.status_code, res.get_json()


def test_valid_owned_address_is_saved(dash, monkeypatch):
    dashboard, config = dash
    node(monkeypatch, dashboard, {
        ("getnewaddress", "pool-btcs"): ADDRESS,
        ("validateaddress", None): {"isvalid": True, "address": ADDRESS},
        ("getaddressinfo", "pool-btcs"): {"ismine": True},
    })
    status, body = generate(dashboard)
    assert status == 200 and body["success"] is True
    assert ADDRESS in config.read_text()


def test_address_the_node_calls_invalid_is_not_saved(dash, monkeypatch):
    dashboard, config = dash
    node(monkeypatch, dashboard, {
        ("getnewaddress", "pool-btcs"): ADDRESS,
        ("validateaddress", None): {"isvalid": False},
        ("getaddressinfo", "pool-btcs"): {"ismine": True},
    })
    status, body = generate(dashboard)
    assert status == 500 and body["success"] is False and "valid" in body["error"]
    assert "PENDING_GENERATION" in config.read_text()


def test_unreachable_validation_is_not_saved(dash, monkeypatch):
    dashboard, config = dash
    node(monkeypatch, dashboard, {("getnewaddress", "pool-btcs"): ADDRESS})
    status, body = generate(dashboard)
    assert status == 500 and body["success"] is False
    assert "PENDING_GENERATION" in config.read_text()


def test_address_from_another_wallet_is_not_saved(dash, monkeypatch):
    dashboard, config = dash
    node(monkeypatch, dashboard, {
        ("getnewaddress", "pool-btcs"): ADDRESS,
        ("validateaddress", None): {"isvalid": True},
        ("getaddressinfo", "pool-btcs"): {"ismine": False},
    })
    status, body = generate(dashboard)
    assert status == 500 and "belongs" in body["error"]
    assert "PENDING_GENERATION" in config.read_text()


def test_unknown_ownership_is_not_saved(dash, monkeypatch):
    dashboard, config = dash
    node(monkeypatch, dashboard, {
        ("getnewaddress", "pool-btcs"): ADDRESS,
        ("validateaddress", None): {"isvalid": True},
    })
    status, body = generate(dashboard)
    assert status == 500 and "belongs" in body["error"]
    assert "PENDING_GENERATION" in config.read_text()


def test_old_daemon_ownership_from_validateaddress(dash, monkeypatch):
    dashboard, config = dash
    calls = node(monkeypatch, dashboard, {
        ("getnewaddress", "pool-btcs"): ADDRESS,
        ("validateaddress", None): {"isvalid": True, "ismine": True},
    })
    status, body = generate(dashboard)
    assert status == 200 and body["success"] is True
    assert ("getaddressinfo", "pool-btcs") in calls and ("getaddressinfo", None) in calls
    assert ADDRESS in config.read_text()


def test_default_wallet_fallback_checks_the_default_wallet(dash, monkeypatch):
    dashboard, config = dash
    node(monkeypatch, dashboard, {
        ("getnewaddress", None): ADDRESS,
        ("validateaddress", None): {"isvalid": True},
        ("getaddressinfo", None): {"ismine": True},
    })
    status, body = generate(dashboard)
    assert status == 200 and body["success"] is True
    assert ADDRESS in config.read_text()
