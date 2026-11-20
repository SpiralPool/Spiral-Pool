# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Pool topology endpoint (v3.1.0).

/api/topology joins config.yaml (coins, stratum ports, nodes in failover order,
merge-mined aux chains), node reachability, connected miners (matched to a coin by
the stratum port in their pool URL) and the HA cluster — without ever passing the
HA cluster token or pool addresses to the browser.

Run: python -m pytest tests/test_topology.py -v
"""
import os
import sys
import types

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", os.path.join(ROOT, "config"))

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}

CONFIG = """
coins:
  - symbol: DGB
    pool_id: dgb_sha256_1
    enabled: true
    stratum: {port: 3333, port_tls: 3335}
    nodes:
      - {id: backup-1, host: 10.0.0.2, port: 14022, priority: 1}
      - {id: primary, host: 127.0.0.1, port: 14022, priority: 0}
  - symbol: LTC
    pool_id: ltc_1
    enabled: true
    stratum: {port: 7333}
    nodes:
      - {id: primary, host: 127.0.0.1, port: 9332, priority: 0}
    mergeMining:
      enabled: true
      auxChains:
        - {symbol: DOGE, enabled: true, address: Dxxx}
        - {symbol: PEP, enabled: false, address: Pxxx}
  - symbol: BTC
    pool_id: btc_1
    enabled: false
    stratum: {port: 4333}
"""


@pytest.fixture()
def dash(tmp_path, monkeypatch):
    import dashboard
    dashboard.AUTH_ENABLED = False
    dashboard.app.config["TESTING"] = True
    cfg = tmp_path / "config.yaml"
    cfg.write_text(CONFIG)
    monkeypatch.setattr(dashboard, "POOL_CONFIG_PATH", str(cfg))
    monkeypatch.setattr(dashboard, "fetch_all_nodes_health",
                        lambda: {"DGB": {"status": "online", "blocks": 23000000, "sync_progress": 100.0, "connections": 12}})
    monkeypatch.setattr(dashboard, "_tcp_reachable", lambda host, port, timeout=1.0: host != "10.0.0.2")
    monkeypatch.setitem(dashboard.miner_cache, "miners", {
        "rig1": {"ip": "192.168.1.10", "type": "axeos", "online": True, "hashrate_ths": 1.2,
                 "pool_url": "stratum+tcp://192.168.1.5:3333"},
        "rig2": {"ip": "192.168.1.11", "type": "avalon", "online": False, "hashrate_ths": 0,
                 "pool_url": "stratum+tcp://192.168.1.5:7333"},
        "rig3": {"ip": "192.168.1.12", "type": "axeos", "online": True, "hashrate_ths": 0.5,
                 "pool_url": "stratum+tcp://public.example:9999"},
    })

    def fake_get(url, timeout=None):
        assert url.endswith(":5354/status")
        return types.SimpleNamespace(ok=True, json=lambda: {
            "enabled": True, "state": "running", "vip": "192.168.1.200", "localId": "n1", "masterId": "n1",
            "localRole": "MASTER", "clusterToken": "SECRET-TOKEN", "failoverCount": 2,
            "nodes": [{"id": "n1", "host": "192.168.1.5", "role": "MASTER", "isHealthy": True,
                       "poolAddresses": {"DGB": "Dsecret"}},
                      {"id": "n2", "host": "192.168.1.6", "role": "BACKUP", "isHealthy": False}]})
    monkeypatch.setattr(dashboard.requests, "get", fake_get)
    dashboard._topology_cache.update(at=0.0, data=None)
    return dashboard


def test_topology_shape(dash):
    res = dash.app.test_client().get("/api/topology", environ_base=LOCAL)
    assert res.status_code == 200
    t = res.get_json()

    assert [c["symbol"] for c in t["coins"]] == ["DGB", "LTC"], "disabled BTC left out"
    dgb, ltc = t["coins"]
    assert dgb["ports"] == [3333, 3335]
    assert [n["id"] for n in dgb["nodes"]] == ["primary", "backup-1"], "failover order by priority"
    assert dgb["nodes"][0]["reachable"] is True and dgb["nodes"][0]["blocks"] == 23000000
    assert dgb["nodes"][1]["reachable"] is False
    assert ltc["aux_chains"] == ["DOGE"], "disabled aux chain left out"

    by_name = {m["name"]: m for m in t["miners"]}
    assert by_name["rig1"]["coin"] == "DGB" and by_name["rig2"]["coin"] == "LTC"
    assert by_name["rig3"]["coin"] is None, "miner on an unknown port is not invented a coin"

    assert t["ha"]["enabled"] and t["ha"]["vip"] == "192.168.1.200" and len(t["ha"]["nodes"]) == 2
    body = res.get_data(as_text=True)
    assert "SECRET-TOKEN" not in body and "Dsecret" not in body, "no token or pool addresses leak"


def test_topology_page_renders(dash):
    res = dash.app.test_client().get("/topology", environ_base=LOCAL)
    assert res.status_code == 200 and b"topology.js" in res.data
