# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Vnish web API integration against a mock miner built from Vnish's own OpenAPI
document (api-doc.json, "xminer-api", as served by the firmware; checked for
1.2.6 and 1.3.4):

  * POST /api/v1/unlock {"pw"} -> {"token"}; a wrong password is 403
  * protected endpoints (system/*, mining/*, settings, autotune/presets) declare
    bearerAuth: `Authorization: Bearer <token>`
  * /summary, /info and /model are readable without auth
  * MinerSummary: power_consumption is watts, power_usage is deprecated and
    "Same as power_efficiency"; chip_temp/pcb_temp are {min, max}; hr_* are in
    the unit /model reports as hr_measure ("GH/s" or "MH/s")

auth="raw" stands in for older firmware that accepts only the bare token.

Run: python -m pytest tests/test_vnish_api.py -v
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
import miner_control as mc  # noqa: E402

_spec = importlib.util.spec_from_file_location("spiral_sentinel_vnish", os.path.join(SENTINEL_DIR, "SpiralSentinel.py"))
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}

SUMMARY = {"miner": {
    "miner_type": "Antminer S19 XP", "power_consumption": 3250, "power_usage": 30,
    "power_efficiency": 21.4, "hr_realtime": 110000.0, "hr_average": 109000.0, "hr_nominal": 140000.0,
    "chip_temp": {"min": 61, "max": 72}, "pcb_temp": {"min": 50, "max": 60},
    "chains": [{"id": 1, "chip_temp": {"min": 61, "max": 72}, "pcb_temp": {"min": 50, "max": 60}}],
    "cooling": {"fan_duty": 55, "fan_num": 4, "fans": [{"id": 0, "rpm": 4800, "status": "ok", "max_rpm": 6000}], "settings": {}},
    "pools": [{"id": 0, "url": "stratum+tcp://backup:3333", "user": "addr.backup", "status": "working"},
              {"id": 1, "url": "stratum+tcp://192.168.1.10:3333", "user": "addr.s19xp", "status": "active"}],
    "best_share": 123456789, "found_blocks": 1,
}}


class MockVnish:
    """auth: "bearer" (documented form only), "raw" (older firmware) or "either"."""

    def __init__(self, password="admin", auth="bearer", summary=None, hr_measure="GH/s"):
        self.password, self.auth, self.hr_measure = password, auth, hr_measure
        self.summary = copy.deepcopy(SUMMARY if summary is None else summary)
        self.tokens, self.requests, self._n = set(), [], 0
        mock = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, code, body=None):
                raw = b"" if body is None else json.dumps(body).encode()
                self.send_response(code)
                if body is not None:
                    self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def _authorized(self, header):
                if not header:
                    return False
                if header.startswith("Bearer "):
                    return mock.auth in ("bearer", "either") and header[7:] in mock.tokens
                return mock.auth in ("raw", "either") and header in mock.tokens

            def _handle(self, method):
                n = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(n)) if n else None
                auth = self.headers.get("Authorization")
                mock.requests.append({"method": method, "path": self.path, "auth": auth, "json": body})
                route = (method, self.path)
                if route == ("POST", "/api/v1/unlock"):
                    if (body or {}).get("pw") != mock.password:
                        return self._send(403, {"err": "Wrong password"})
                    mock._n += 1
                    mock.tokens.add(f"vt{mock._n}")
                    return self._send(200, {"token": f"vt{mock._n}"})
                # Public reads: anonymous is fine, a bad token is not
                if method == "GET" and self.path in ("/api/v1/summary", "/api/v1/info", "/api/v1/model"):
                    if auth and not self._authorized(auth):
                        return self._send(401, {"err": "Unauthorized"})
                    if self.path == "/api/v1/summary":
                        return self._send(200, mock.summary)
                    if self.path == "/api/v1/info":
                        return self._send(200, {"fw_name": "Vnish", "fw_version": "1.3.4", "build_name": "Vnish 1.3.4", "build_uuid": "0123456789abcdef"})
                    return self._send(200, {"model": "S19 XP", "hr_measure": mock.hr_measure})
                if not self._authorized(auth):
                    return self._send(401, {"err": "Unauthorized"})
                if route == ("GET", "/api/v1/autotune/presets"):
                    return self._send(200, [{"name": "3200", "pretty": "3200 watt ~ 120 TH", "status": "tuned"}])
                if method == "POST" and self.path in ("/api/v1/settings", "/api/v1/system/reboot") or self.path.startswith("/api/v1/mining/"):
                    return self._send(200, {})
                return self._send(404, {"err": "not found"})

            def do_GET(self):
                self._handle("GET")

            def do_POST(self):
                self._handle("POST")

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        self.ip = f"127.0.0.1:{self.port}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def reboot(self):
        self.tokens.clear()

    def calls(self, path):
        return [r for r in self.requests if r["path"] == path]

    def close(self):
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def miner(monkeypatch, tmp_path):
    """Mock Vnish miners on loopback; the CGMiner port is absent, so the web API is what is read."""
    monkeypatch.setenv("SPIRALPOOL_INSTALL_DIR", str(tmp_path))
    monkeypatch.setattr(dashboard, "validate_miner_ip", lambda ip: True)
    monkeypatch.setattr(dashboard, "load_config", lambda *a, **k: {"devices": {}})
    monkeypatch.setattr(dashboard, "AUTH_ENABLED", False)
    monkeypatch.setattr(dashboard, "cgminer_command", lambda *a, **k: {})
    monkeypatch.setattr(sentinel, "validate_miner_ip", lambda ip: True)
    monkeypatch.setattr(sentinel, "_cgminer", lambda *a, **k: None)
    monkeypatch.setattr(sentinel, "_cgminer_get_worker", lambda *a, **k: ("", "", ""))
    monkeypatch.setattr(sentinel, "DEVICE_CREDENTIALS_FILE", tmp_path / "data" / "device_credentials.json")
    monkeypatch.setattr(mc, "_host", lambda device: "127.0.0.1")
    sentinel._credentials_cache.update(mtime=None, credentials={})
    dashboard._vnish_sessions.clear()
    sentinel._vnish_sessions.clear()
    made = []

    def make(**kwargs):
        m = MockVnish(**kwargs)
        made.append(m)
        return m
    yield make
    for m in made:
        m.close()


def _store_password(tmp_path, ip, password):
    (tmp_path / "data").mkdir(exist_ok=True)
    (tmp_path / "data" / "device_credentials.json").write_text(json.dumps({ip: {"username": "", "password": password}}))


# ── Dashboard monitoring ─────────────────────────────────────────────────────

def test_dashboard_reads_the_documented_fields(miner):
    m = miner()
    r = dashboard.fetch_vnish(m.ip, timeout=5)

    assert r["online"] is True, r
    assert r["power_watts"] == 3250  # power_consumption, not power_usage (J/TH)
    assert r["hashrate_ths"] == pytest.approx(110.0)  # hr_realtime in GH/s
    assert r["temps"] == {"chip": 72, "board": 60}
    assert r["fan_speed"] == 55
    assert r["best_diff"] == "123456789"
    assert r["pool_url"] == "stratum+tcp://192.168.1.10:3333"
    assert (r["model"], r["version"]) == ("Antminer S19 XP", "1.3.4")


def test_dashboard_scrypt_hashrate_uses_hr_measure(miner):
    summary = copy.deepcopy(SUMMARY)
    summary["miner"]["hr_realtime"] = 9500.0
    m = miner(summary=summary, hr_measure="MH/s")
    assert dashboard.fetch_vnish(m.ip, timeout=5)["hashrate_ths"] == pytest.approx(0.0095)


def test_dashboard_older_firmware_power_usage_still_reads_as_watts(miner):
    summary = copy.deepcopy(SUMMARY)
    del summary["miner"]["power_consumption"]
    summary["miner"]["power_usage"] = 3100
    m = miner(summary=summary)
    assert dashboard.fetch_vnish(m.ip, timeout=5)["power_watts"] == 3100


def test_dashboard_monitoring_survives_a_wrong_password(miner):
    m = miner(password="s3cret")
    r = dashboard.fetch_vnish(m.ip, password="admin", timeout=5)
    assert r["online"] is True and r["power_watts"] == 3250, r


def test_device_test_uses_the_automation_page_password(miner, tmp_path):
    m = miner(password="s3cret")
    _store_password(tmp_path, m.ip, "s3cret")
    res = dashboard.app.test_client().post("/api/device/test", json={"type": "vnish", "ip": m.ip}, environ_base=LOCAL)
    assert res.status_code == 200 and res.get_json()["success"] is True, res.get_json()
    assert m.calls("/api/v1/unlock")[-1]["json"] == {"pw": "s3cret"}


# ── Dashboard control routes ─────────────────────────────────────────────────

def _fan(m):
    return dashboard.app.test_client().post("/api/miner/vnish/fan", environ_base=LOCAL,
                                            json={"ip": m.ip, "fan_speed": 70, "auto_fan": False}).get_json()


def test_fan_control_uses_the_stored_password_and_bearer(miner, tmp_path):
    m = miner(password="s3cret")
    _store_password(tmp_path, m.ip, "s3cret")
    assert _fan(m)["success"] is True
    post = m.calls("/api/v1/settings")[-1]
    assert post["auth"] == "Bearer vt1"
    assert post["json"] == {"miner": {"cooling": {"mode": {"name": "manual", "param": 70}, "fan_min_duty": 70, "fan_min_count": 1}}}


def test_older_firmware_gets_the_bare_token_and_it_is_remembered(miner):
    m = miner(auth="raw")
    assert _fan(m)["success"] is True
    assert [c["auth"] for c in m.calls("/api/v1/settings")] == ["Bearer vt1", "vt1"]
    assert _fan(m)["success"] is True
    assert m.calls("/api/v1/settings")[-1]["auth"] == "vt1"


def test_control_recovers_when_the_miner_reboots(miner):
    m = miner()
    assert _fan(m)["success"] is True
    m.reboot()
    assert _fan(m)["success"] is True
    assert m.tokens == {"vt2"}


def test_presets_route_handles_the_array_response(miner):
    m = miner()
    res = dashboard.app.test_client().post("/api/miner/vnish/presets", json={"ip": m.ip}, environ_base=LOCAL).get_json()
    assert res["success"] is True and res["presets"][0]["name"] == "3200", res


def test_wrong_password_is_reported_for_control(miner):
    m = miner(password="s3cret")
    res = _fan(m)
    assert res["success"] is False and "403" in res["error"]


# ── Sentinel ─────────────────────────────────────────────────────────────────

def test_sentinel_reads_the_documented_fields(miner):
    m = miner()
    r = sentinel.fetch_vnish(m.ip, timeout=5)
    assert r is not None
    assert r["hashrate_ghs"] == 110000.0
    assert r["power_watts"] == 3250
    assert r["temps"] == {"chip": 72, "board": 60}
    assert (r["pool_url"], r["stratum_user"], r["worker_name"]) == ("stratum+tcp://192.168.1.10:3333", "addr.s19xp", "s19xp")
    assert all(c["auth"] == "Bearer vt1" for c in m.calls("/api/v1/summary"))


def test_sentinel_scrypt_unit(miner):
    summary = copy.deepcopy(SUMMARY)
    summary["miner"]["hr_realtime"] = 9500.0
    m = miner(summary=summary, hr_measure="MH/s")
    assert sentinel.fetch_vnish(m.ip, timeout=5)["hashrate_ghs"] == pytest.approx(9.5)


def test_sentinel_survives_a_wrong_password_and_a_stale_token(miner):
    m = miner(password="s3cret")
    assert sentinel.fetch_vnish(m.ip, password="admin", timeout=5) is not None
    sentinel._vnish_sessions[m.ip] = {"token": "dead", "expires": 9e18}
    assert sentinel.fetch_vnish(m.ip, password="admin", timeout=5) is not None


def test_sentinel_uses_the_automation_page_password(miner, tmp_path):
    m = miner(password="s3cret")
    _store_password(tmp_path, m.ip, "s3cret")
    assert sentinel._vnish_login({"ip": m.ip}) == "s3cret"
    assert sentinel._vnish_login({"ip": "192.168.1.99", "password": "pw"}) == "pw"
    assert sentinel._vnish_login({"ip": "192.168.1.98"}) == "admin"


# ── Sentinel miner control ───────────────────────────────────────────────────

@pytest.mark.parametrize("auth,expected", [("bearer", ["Bearer vt1"]), ("raw", ["Bearer vt1", "vt1"])])
def test_miner_control_sends_bearer_and_falls_back_for_older_firmware(miner, auth, expected):
    m = miner(auth=auth)
    mc.sleep({"ip": "127.0.0.1", "type": "vnish", "http_port": m.port, "password": "admin"})
    assert [c["auth"] for c in m.calls("/api/v1/mining/pause")] == expected
