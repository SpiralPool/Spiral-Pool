# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""BraiinsOS REST API integration (issue #20) against a mock miner.

The mock follows the Braiins OS Public REST API spec
(https://developer.braiins-os.com/latest/openapi.json, v1.8.0) and the firmware
behaviour reported in issue #20 (BraiinsOS 26.09.1-plus on an S19K Pro):

  * every call but login needs `Authorization: <token>`; anything else, a
    `Bearer <token>` included, gets 401 "Missing or invalid authentication token"
  * GET /api/v1/pools/ answers a bare list of pool groups (the spec's own example)
  * POST /api/v1/pools/ answers 201, action endpoints answer a bare boolean

Run: python -m pytest tests/test_braiins_api.py -v
"""
import copy
import importlib.util
import json
import os
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))
SENTINEL_DIR = os.path.join(ROOT, "src", "sentinel")
sys.path.insert(0, SENTINEL_DIR)

import dashboard  # noqa: E402

_spec = importlib.util.spec_from_file_location("spiral_sentinel_braiins", os.path.join(SENTINEL_DIR, "SpiralSentinel.py"))
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}

# Verbatim from the spec's GET /api/v1/pools/ 200 example.
SPEC_POOLS = [{"name": "High Performance", "pools": [{"active": True, "alive": True, "enabled": True, "stats": {"accepted_shares": 1000, "best_share": 1200000, "best_share_str": "1200000", "generated_work": 5000000, "last_difficulty": 800000, "last_share_time": {"nanos": 255441271, "seconds": 1741260910}, "rejected_shares": 10, "stale_shares": 5}, "uid": "pool1", "url": "stratum+tcp://pool.example.com:3333", "user": "miner1"}], "strategy": {"quota": {"value": 70}}, "uid": "group1"}]

STATS = {"miner_stats": {"nominal_hashrate": {"gigahash_per_second": 104000.0},
                         "real_hashrate": {"last_5m": {"gigahash_per_second": 103500.0}},
                         "found_blocks": 0, "best_share": 98765, "best_share_str": "98765"},
         "power_stats": {"approximated_consumption": {"watt": 2760}, "efficiency": {"joule_per_terahash": 26.7}},
         "pool_stats": {"accepted_shares": 4321, "rejected_shares": 7, "stale_shares": 2,
                        "last_difficulty": 0, "best_share": 0, "generated_work": 0, "best_share_str": "0"}}
COOLING = {"highest_temperature": {"location": 0, "temperature": {"degree_c": 71.5}},
           "fans": [{"position": 0, "rpm": 4200, "target_speed_ratio": 0.62},
                    {"position": 1, "rpm": 4320, "target_speed_ratio": 0.64}]}
DETAILS = {"miner_identity": {"name": "Antminer S19K Pro", "miner_model": "Antminer S19K Pro"},
           "bos_version": {"current": "26.09.1-plus"}, "hostname": "s19k",
           "bosminer_uptime_s": 86400, "system_uptime_s": 90000}


class MockBraiins:
    """A BraiinsOS miner on loopback. Tokens are issued per login; reboot() drops them."""

    def __init__(self, password="", pools="list", stats=None, cooling=None, legacy_login=False):
        self.password, self.pools_mode, self.legacy_login = password, pools, legacy_login
        self.stats = copy.deepcopy(STATS if stats is None else stats)
        self.cooling = copy.deepcopy(COOLING if cooling is None else cooling)
        self.tokens, self.requests, self._n = set(), [], 0
        mock = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, code, body=None, headers=None):
                raw = b"" if body is None else json.dumps(body).encode()
                self.send_response(code)
                for k, v in (headers or {}).items():
                    self.send_header(k, v)
                if body is not None:
                    self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def _handle(self, method):
                n = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(n)) if n else None
                auth = self.headers.get("Authorization")
                mock.requests.append({"method": method, "path": self.path, "auth": auth, "json": body})
                if (method, self.path) == ("POST", "/api/v1/auth/login"):
                    if (body or {}).get("password") != mock.password:
                        return self._send(401, {"message": "Invalid credentials"})
                    mock._n += 1
                    token = f"tok{mock._n}"
                    mock.tokens.add(token)
                    if mock.legacy_login:
                        return self._send(200, {}, headers={"authorization": token})
                    return self._send(200, {"token": token, "timeout_s": 3600})
                if auth not in mock.tokens:
                    return self._send(401, {"message": "Missing or invalid authentication token"})
                route = (method, self.path)
                if route == ("GET", "/api/v1/miner/stats"):
                    return self._send(200, mock.stats)
                if route == ("GET", "/api/v1/cooling/state"):
                    return self._send(200, mock.cooling)
                if route == ("GET", "/api/v1/miner/details"):
                    return self._send(200, DETAILS)
                if route == ("GET", "/api/v1/pools/"):
                    if mock.pools_mode == "error":
                        return self._send(500, {"message": "internal error"})
                    groups = [] if mock.pools_mode == "empty" else SPEC_POOLS
                    return self._send(200, {"pool_groups": groups} if mock.pools_mode == "legacy" else groups)
                if route == ("POST", "/api/v1/pools/"):
                    return self._send(201, dict(body or {}, uid="new-group"))
                if method == "PUT" and self.path.startswith("/api/v1/pools/"):
                    return self._send(200, dict(body or {}, uid=self.path.rsplit("/", 1)[-1]))
                if route == ("PUT", "/api/v1/actions/reboot"):
                    return self._send(204)
                if method == "PUT" and self.path.startswith("/api/v1/actions/"):
                    return self._send(200, True)
                return self._send(404, {"message": "not found"})

            def do_GET(self):
                self._handle("GET")

            def do_POST(self):
                self._handle("POST")

            def do_PUT(self):
                self._handle("PUT")

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.ip = f"127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def reboot(self):
        self.tokens.clear()

    def calls(self, method=None):
        return [r for r in self.requests if r["path"] != "/api/v1/auth/login" and (method is None or r["method"] == method)]

    def close(self):
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def miner(monkeypatch, tmp_path):
    """Factory for mock miners; the miners live on loopback, so the SSRF filters are opened."""
    monkeypatch.setenv("SPIRALPOOL_INSTALL_DIR", str(tmp_path))
    monkeypatch.setattr(dashboard, "validate_miner_ip", lambda ip: True)
    monkeypatch.setattr(dashboard, "load_config", lambda *a, **k: {"devices": {}})
    monkeypatch.setattr(dashboard, "AUTH_ENABLED", False)
    monkeypatch.setattr(sentinel, "validate_miner_ip", lambda ip: True)
    monkeypatch.setattr(sentinel, "DEVICE_CREDENTIALS_FILE", tmp_path / "data" / "device_credentials.json")
    sentinel._credentials_cache.update(mtime=None, credentials={})
    dashboard._braiins_sessions.clear()
    sentinel._braiins_sessions.clear()
    made = []

    def make(**kwargs):
        m = MockBraiins(**kwargs)
        made.append(m)
        return m
    yield make
    for m in made:
        m.close()


def _store_credentials(tmp_path, ip, username, password):
    data_dir = tmp_path / "data"
    data_dir.mkdir(exist_ok=True)
    (data_dir / "device_credentials.json").write_text(json.dumps({ip: {"username": username, "password": password}}))


# ── Dashboard monitoring ─────────────────────────────────────────────────────

@pytest.mark.parametrize("pools", ["list", "legacy"])
def test_dashboard_reads_the_miner(miner, pools):
    m = miner(pools=pools)
    r = dashboard.fetch_braiins(m.ip, timeout=5)

    assert r["online"] is True, r
    assert r["hashrate_ths"] == pytest.approx(103.5)  # measured real_hashrate, not nominal
    assert r["power_watts"] == 2760 and r["efficiency"] == pytest.approx(26.7)
    assert r["temps"] == {"chip": 71.5, "board": 71.5}
    assert r["fan_speed"] == 63
    assert (r["accepted"], r["rejected"]) == (4321, 9)
    assert r["best_diff"] == "98765"
    assert r["pool_url"] == "stratum+tcp://pool.example.com:3333"
    assert (r["model"], r["version"], r["uptime"]) == ("Antminer S19K Pro", "26.09.1-plus", 86400)


def test_dashboard_never_sends_a_bearer_token(miner):
    m = miner()
    dashboard.fetch_braiins(m.ip, timeout=5)
    assert m.calls() and all(c["auth"] == "tok1" for c in m.calls())


def test_dashboard_recovers_when_the_miner_reboots(miner):
    m = miner()
    assert dashboard.fetch_braiins(m.ip, timeout=5)["online"] is True
    m.reboot()  # the cached token is now stale
    r = dashboard.fetch_braiins(m.ip, timeout=5)
    assert r["online"] is True and r["model"] == "Antminer S19K Pro", r
    assert m.tokens == {"tok2"}


def test_dashboard_reads_a_token_from_the_legacy_login_header(miner):
    m = miner(legacy_login=True)
    assert dashboard.fetch_braiins(m.ip, timeout=5)["online"] is True


def test_dashboard_survives_null_objects(miner):
    stats = copy.deepcopy(STATS)
    stats["power_stats"] = None
    stats["miner_stats"]["real_hashrate"] = None
    m = miner(stats=stats, cooling={"highest_temperature": None, "fans": [{"rpm": 4200}]})
    r = dashboard.fetch_braiins(m.ip, timeout=5)

    assert r["online"] is True, r
    assert r["hashrate_ths"] == pytest.approx(104.0)  # no measurement: nominal
    assert r["power_watts"] == 0 and r["temps"]["chip"] == 0
    assert r["fan_speed"] == 70  # RPM fallback when no target_speed_ratio


def test_a_paused_miner_reports_its_measured_zero(miner):
    stats = copy.deepcopy(STATS)
    stats["miner_stats"]["real_hashrate"]["last_5m"]["gigahash_per_second"] = 0.0
    m = miner(stats=stats)
    assert dashboard.fetch_braiins(m.ip, timeout=5)["hashrate_ths"] == 0
    assert sentinel.fetch_braiins(m.ip, timeout=5) is None  # offline, like a stalled CGMiner


def test_wrong_credentials_are_reported_not_retried_forever(miner):
    m = miner(password="s3cret")
    r = dashboard.fetch_braiins(m.ip, timeout=5)
    assert r["online"] is False and "401" in r["error"]
    assert m.calls() == []


def test_automation_page_credentials_reach_monitoring_and_device_test(miner, tmp_path):
    m = miner(password="s3cret")
    _store_credentials(tmp_path, m.ip, "admin", "s3cret")
    assert dashboard._braiins_credentials(m.ip) == ("admin", "s3cret")

    res = dashboard.app.test_client().post("/api/device/test", json={"type": "braiins", "ip": m.ip}, environ_base=LOCAL)
    assert res.status_code == 200 and res.get_json()["success"] is True, res.get_json()


def test_action_endpoints_answering_a_boolean_are_wrapped(miner):
    m = miner()
    assert dashboard.braiins_api_call(m.ip, "/actions/pause", method="PUT", timeout=5) == {"success": True, "result": True}
    assert dashboard.braiins_api_call(m.ip, "/actions/reboot", method="PUT", timeout=5) == {"success": True}


# ── Dashboard pool configuration ─────────────────────────────────────────────

def _batch_pool(m):
    res = dashboard.app.test_client().post(
        "/api/fleet/batch/pool", environ_base=LOCAL,
        json={"pool_url": "192.168.1.10", "pool_port": 3333,
              "targets": [{"ip": m.ip, "type": "braiins", "name": "s19k"}]})
    assert res.status_code == 200, res.get_json()
    return res.get_json()["results"][0]


@pytest.mark.parametrize("pools", ["list", "legacy"])
def test_pool_update_replaces_the_first_group_with_the_spec_payload(miner, pools):
    m = miner(pools=pools)
    result = _batch_pool(m)

    assert result["success"] is True, result
    writes = m.calls("PUT")
    assert [w["path"] for w in writes] == ["/api/v1/pools/group1"]
    body = writes[0]["json"]
    assert body["load_balance_strategy"] == {"quota": {"value": 1}} and "quota" not in body
    assert body["pools"] == [{"url": "stratum+tcp://192.168.1.10:3333", "user": "s19k", "password": "x"}]


def test_pool_create_on_a_miner_without_groups_reports_success(miner):
    m = miner(pools="empty")
    result = _batch_pool(m)
    assert result["success"] is True, result  # POST answers 201
    assert [w["path"] for w in m.calls("POST")] == ["/api/v1/pools/"]


def test_pool_config_never_creates_a_group_when_the_pools_cannot_be_read(miner):
    m = miner(pools="error")
    result = _batch_pool(m)
    assert result["success"] is False and "Could not read the current pools" in result["message"]
    assert m.calls("POST") == [] and m.calls("PUT") == []


# ── Settings save keeps the login ────────────────────────────────────────────

def test_saving_settings_keeps_a_braiins_login(monkeypatch):
    stored = {"first_run": False, "devices": {"braiins": [
        {"name": "s19k", "ip": "192.168.1.60", "username": "admin", "password": "s3cret"}]}}
    saved = {}
    monkeypatch.setattr(dashboard, "AUTH_ENABLED", False)
    monkeypatch.setattr(dashboard, "load_config", lambda: copy.deepcopy(stored))
    monkeypatch.setattr(dashboard, "save_config", lambda cfg: saved.update(copy.deepcopy(cfg)))
    monkeypatch.setattr(dashboard, "sync_miners_to_sentinel", lambda: (0, []))
    monkeypatch.setattr(dashboard, "record_activity", lambda *a, **k: None)
    monkeypatch.setattr(dashboard, "check_rate_limit", lambda *a, **k: True)

    # What the Settings page sends: name and IP only.
    res = dashboard.app.test_client().post(
        "/api/config", environ_base=LOCAL, json={"devices": {"braiins": [{"name": "s19k", "ip": "192.168.1.60"}]}})
    assert res.status_code == 200, res.get_json()
    assert saved["devices"]["braiins"][0] == {"name": "s19k", "ip": "192.168.1.60", "username": "admin", "password": "s3cret"}


# ── Sentinel ─────────────────────────────────────────────────────────────────

@pytest.mark.parametrize("pools", ["list", "legacy"])
def test_sentinel_reads_the_miner(miner, pools):
    m = miner(pools=pools)
    r = sentinel.fetch_braiins(m.ip, timeout=5)

    assert r is not None
    assert r["hashrate_ghs"] == 103500.0
    assert (r["power_watts"], r["temps"], r["fans"]) == (2760, {"chip": 71.5}, [4200, 4320])
    assert (r["accepted"], r["rejected"], r["stale"], r["uptime"]) == (4321, 7, 2, 86400)
    assert (r["pool_url"], r["stratum_user"], r["worker_name"]) == ("stratum+tcp://pool.example.com:3333", "miner1", "miner1")
    assert all(c["auth"] == "tok1" for c in m.calls())


def test_sentinel_recovers_when_the_miner_reboots(miner):
    m = miner()
    assert sentinel.fetch_braiins(m.ip, timeout=5) is not None
    m.reboot()
    assert sentinel.fetch_braiins(m.ip, timeout=5) is not None
    assert m.tokens == {"tok2"}


def test_sentinel_survives_null_objects(miner):
    stats = copy.deepcopy(STATS)
    stats["power_stats"] = None
    m = miner(stats=stats, cooling={"highest_temperature": None, "fans": []})
    r = sentinel.fetch_braiins(m.ip, timeout=5)
    assert r is not None and r["hashrate_ghs"] == 103500.0 and r["power_watts"] == 0


def test_sentinel_uses_the_automation_page_login(miner, tmp_path):
    m = miner(password="s3cret")
    _store_credentials(tmp_path, m.ip, "admin", "s3cret")
    entry = {"ip": m.ip, "name": "s19k"}
    assert sentinel._braiins_login(entry) == ("admin", "s3cret")
    assert sentinel.fetch_braiins(m.ip, *sentinel._braiins_login(entry), timeout=5) is not None
    assert sentinel._braiins_login({"ip": "192.168.1.99", "username": "u", "password": "p"}) == ("u", "p")
