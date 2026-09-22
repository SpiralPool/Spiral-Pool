# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Every dashboard GET route renders for a logged-in operator.

Testing had only ever reached the login page, so faults behind it stayed hidden:
/api/stratum/connect answered 500 for everyone because it re-entered an
auth-gated view through a fresh request context, and /api/combined — the
endpoint the whole UI polls — raised AttributeError whenever no coin was
configured yet.

A 500 here always means an unhandled exception: dashboard.py registers a blanket
@app.errorhandler(Exception) that turns any escape into {"error": "Internal
server error"}, so a 500 is never a deliberate answer. 502 and 503 are, and they
are what an unreachable pool API or a not-ready probe is supposed to return.

Run: python -m pytest tests/test_dashboard_routes_authenticated.py -v
"""
import os
import sys

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", os.path.join(ROOT, "config"))

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}

# Stand-ins for path arguments. A route whose argument is not here is skipped
# rather than guessed at, so this never asserts against a fabricated URL.
SAMPLE = {
    "coin": "dgb", "pool_id": "dgb_sha256_1", "id": "dgb_sha256_1",
    "symbol": "dgb", "address": "DTE2HHLNh1TNJzPaZb5uf6fkxxqYiLuGLB",
    "height": "1", "hash": "0" * 64, "txid": "0" * 64,
    "ip": "192.168.1.30", "name": "rig1", "worker": "rig1",
    "miner": "rig1", "page": "1",
}


@pytest.fixture(scope="module")
def client():
    import dashboard
    dashboard.AUTH_ENABLED = False          # a logged-in operator on loopback
    dashboard.app.config["TESTING"] = True
    return dashboard.app.test_client()


def _concrete_paths():
    import dashboard
    for rule in sorted(dashboard.app.url_map.iter_rules(), key=str):
        if "GET" not in rule.methods or rule.endpoint == "static":
            continue
        path = str(rule)
        if any(arg not in SAMPLE for arg in rule.arguments):
            continue
        for arg in rule.arguments:
            for conv in ("", "int:", "string:", "path:", "float:"):
                path = path.replace("<%s%s>" % (conv, arg), SAMPLE[arg])
        if "<" in path:
            continue
        yield path


def test_no_authenticated_route_raises(client):
    failures = []
    for path in _concrete_paths():
        res = client.get(path, environ_base=LOCAL)
        # 5xx other than 502/503 means something escaped to the blanket handler.
        if res.status_code >= 500 and res.status_code not in (502, 503):
            failures.append("%s -> %d" % (path, res.status_code))
    assert not failures, "routes raised behind login:\n  " + "\n  ".join(failures)


def test_stratum_connect_does_not_reenter_auth(client):
    """It calls get_stratum_address, which is itself auth-gated.

    Done inside a fresh app.test_request_context() that view saw no session and
    no X-API-Key, so its decorator returned a redirect to /login — and
    get_json() on a redirect is None.
    """
    res = client.get("/api/stratum/connect", environ_base=LOCAL)
    assert res.status_code != 500, res.get_data(as_text=True)


def test_combined_survives_no_configured_coin(client):
    """The coin lookup helpers must treat a missing symbol as 'no data'.

    fetch_block_reward already guards its later fallbacks with "if primary_coin";
    coin_rpc and fetch_live_block_reward did not, and called .upper() on None.
    """
    import dashboard
    assert dashboard.coin_rpc(None, "getblockchaininfo") is None
    assert dashboard.fetch_live_block_reward(None).get("block_reward") == 0
    assert client.get("/api/combined", environ_base=LOCAL).status_code != 500
