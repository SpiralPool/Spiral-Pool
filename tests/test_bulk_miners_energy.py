# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Bulk miner actions and the energy report (v3.1.0).

Bulk actions go through miner_control (the drivers Sentinel's automation uses),
only for miners configured on the dashboard, and record hand-slept miners in
manual_sleep.json — which Sentinel reads, so a miner put to sleep on purpose is
neither alerted on nor auto-restarted. The energy report integrates the measured
watts series per day instead of projecting today's watts over 24 hours.

Run: python -m pytest tests/test_bulk_miners_energy.py -v
"""
import collections
import os
import sys
import time

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))
sys.path.insert(0, os.path.join(ROOT, "src", "sentinel"))
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", os.path.join(ROOT, "config"))

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}

import miner_automation  # noqa: E402


# ── manual sleep record (shared with Sentinel) ──────────────────────────────

def test_manual_sleep_roundtrip(tmp_path):
    d = str(tmp_path)
    assert miner_automation.read_manual_sleep(d) == {}
    miner_automation.set_manual_sleep(d, ["192.168.1.10", "192.168.1.11", "not-an-ip"], True, by="t", now=100)
    asleep = miner_automation.read_manual_sleep(d)
    assert set(asleep) == {"192.168.1.10", "192.168.1.11"}
    assert asleep["192.168.1.10"]["since"] == 100
    miner_automation.set_manual_sleep(d, ["192.168.1.10"], False)
    assert set(miner_automation.read_manual_sleep(d)) == {"192.168.1.11"}


def test_manual_sleep_tolerates_garbage(tmp_path):
    (tmp_path / miner_automation.MANUAL_SLEEP_FILE).write_text("{not json")
    assert miner_automation.read_manual_sleep(str(tmp_path)) == {}


# ── energy integration ──────────────────────────────────────────────────────

def _series(start, seconds, step, watts):
    return [{"time": start + i, "value": watts} for i in range(0, seconds + 1, step)]


def test_energy_integrates_watts_over_time():
    import dashboard
    start = time.mktime((2026, 11, 10, 1, 0, 0, 0, 0, -1))
    days = dashboard.energy_daily_from_series(_series(start, 3600, 60, 1000))
    day = days["2026-11-10"]
    assert day["kwh"] == pytest.approx(1.0, abs=1e-6)       # 1000 W for one hour
    assert day["avg_watts"] == pytest.approx(1000.0)
    assert day["hours_measured"] == pytest.approx(1.0)


def test_energy_skips_gaps_instead_of_inventing_energy():
    import dashboard
    start = time.mktime((2026, 11, 10, 1, 0, 0, 0, 0, -1))
    pts = _series(start, 600, 60, 2000) + _series(start + 600 + 3600, 600, 60, 2000)  # 1 h outage
    day = dashboard.energy_daily_from_series(pts)["2026-11-10"]
    assert day["hours_measured"] == pytest.approx(20 / 60, abs=0.01)   # rounded to 2 dp
    assert day["kwh"] == pytest.approx(2000 * (20 / 60) / 1000, abs=1e-3)


# ── endpoints ───────────────────────────────────────────────────────────────

@pytest.fixture()
def dash(tmp_path, monkeypatch):
    import dashboard
    dashboard.AUTH_ENABLED = False
    dashboard.app.config["TESTING"] = True
    install = tmp_path / "spiralpool"
    (install / "data").mkdir(parents=True)
    monkeypatch.setenv("SPIRALPOOL_INSTALL_DIR", str(install))
    monkeypatch.setattr(dashboard, "CONFIG_DIR", tmp_path)
    monkeypatch.setattr(dashboard, "check_rate_limit", lambda *a, **k: True)
    monkeypatch.setattr(dashboard, "_automation_device_types",
                        lambda: {"192.168.1.10": "axeos", "192.168.1.11": "axeos"})
    dashboard._data_dir = install / "data"
    return dashboard


class FakeControl:
    class ControlError(Exception):
        pass

    def __init__(self, fail_ip=None):
        self.calls = []
        self.fail_ip = fail_ip

    def device_from_files(self, ip, data_dir, miner_type=None, model=None, port=None):
        return {"ip": ip, "type": miner_type}

    def _op(self, name, device, **kw):
        self.calls.append((name, device["ip"], kw))
        if device["ip"] == self.fail_ip:
            raise self.ControlError("no answer")

    def restart(self, d): self._op("restart", d)
    def sleep(self, d): self._op("sleep", d)
    def wake(self, d): self._op("wake", d)
    def set_power(self, d, level=None, watts=None): self._op("power", d, level=level, watts=watts)


def test_bulk_sleep_records_manual_sleep(dash, monkeypatch):
    fake = FakeControl(fail_ip="192.168.1.11")
    monkeypatch.setattr(dash, "miner_control", fake)
    r = dash.app.test_client().post("/api/fleet/batch/control", environ_base=LOCAL,
                                    json={"action": "sleep", "ips": ["192.168.1.10", "192.168.1.11"]})
    body = r.get_json()
    assert r.status_code == 200 and body["successful"] == 1 and body["failed"] == 1
    # Only the miner that accepted is recorded as hand-slept.
    assert set(miner_automation.read_manual_sleep(str(dash._data_dir))) == {"192.168.1.10"}

    fake.fail_ip = None
    dash.app.test_client().post("/api/fleet/batch/control", environ_base=LOCAL,
                                json={"action": "wake", "ips": ["192.168.1.10"]})
    assert miner_automation.read_manual_sleep(str(dash._data_dir)) == {}


def test_bulk_power_level_and_watts(dash, monkeypatch):
    fake = FakeControl()
    monkeypatch.setattr(dash, "miner_control", fake)
    c = dash.app.test_client()
    assert c.post("/api/fleet/batch/control", environ_base=LOCAL,
                  json={"action": "power", "ips": ["192.168.1.10"], "level": "low"}).status_code == 200
    assert c.post("/api/fleet/batch/control", environ_base=LOCAL,
                  json={"action": "power", "ips": ["192.168.1.10"], "watts": 3000}).status_code == 200
    assert ("power", "192.168.1.10", {"level": "low", "watts": None}) in fake.calls
    assert ("power", "192.168.1.10", {"level": None, "watts": 3000}) in fake.calls
    assert c.post("/api/fleet/batch/control", environ_base=LOCAL,
                  json={"action": "power", "ips": ["192.168.1.10"], "level": "turbo"}).status_code == 400


def test_bulk_rejects_unconfigured_and_bad_input(dash, monkeypatch):
    fake = FakeControl()
    monkeypatch.setattr(dash, "miner_control", fake)
    c = dash.app.test_client()
    assert c.post("/api/fleet/batch/control", environ_base=LOCAL,
                  json={"action": "restart", "ips": ["10.0.0.99"]}).status_code == 400
    assert c.post("/api/fleet/batch/control", environ_base=LOCAL,
                  json={"action": "format-disk", "ips": ["192.168.1.10"]}).status_code == 400
    assert c.post("/api/fleet/batch/control", environ_base=LOCAL,
                  json={"action": "restart", "ips": []}).status_code == 400
    assert fake.calls == [], "nothing sent to any miner on rejected input"


def test_energy_export_csv_and_json(dash, monkeypatch):
    start = time.mktime((2026, 11, 10, 1, 0, 0, 0, 0, -1))
    monkeypatch.setitem(dash.historical_data, "power_watts",
                        collections.deque(_series(start, 7200, 60, 1500), maxlen=10080))
    monkeypatch.setattr(dash, "load_config", lambda: {"power_cost": {"rate_per_kwh": 0.10, "currency": "USD"}})
    c = dash.app.test_client()

    j = c.get("/api/export/energy?period=all&format=json", environ_base=LOCAL).get_json()
    assert j["success"] and len(j["days"]) == 1
    assert j["days"][0]["kwh"] == pytest.approx(3.0)       # 1.5 kW x 2 h
    assert j["total_cost"] == pytest.approx(0.30)

    csv = c.get("/api/export/energy?period=all", environ_base=LOCAL)
    assert csv.status_code == 200 and csv.mimetype == "text/csv"
    lines = csv.get_data(as_text=True).strip().splitlines()
    assert lines[0].startswith("date,kwh,avg_watts,peak_watts,hours_measured,rate_per_kwh,cost_USD")
    assert lines[1].startswith("2026-11-10,3.0,")
    assert lines[-1].startswith("TOTAL,3.0,")


def test_energy_history_persists_beyond_live_series(dash, monkeypatch):
    start = time.mktime((2026, 11, 10, 1, 0, 0, 0, 0, -1))
    monkeypatch.setitem(dash.historical_data, "power_watts",
                        collections.deque(_series(start, 3600, 60, 1000), maxlen=10080))
    monkeypatch.setattr(dash, "load_config", lambda: {"power_cost": {"rate_per_kwh": 0.10}})
    dash.update_energy_history(force=True)
    # The raw series rolls over; the stored daily total remains.
    monkeypatch.setitem(dash.historical_data, "power_watts", collections.deque(maxlen=10080))
    j = dash.app.test_client().get("/api/export/energy?period=all&format=json", environ_base=LOCAL).get_json()
    assert [d["date"] for d in j["days"]] == ["2026-11-10"]
    assert j["days"][0]["kwh"] == pytest.approx(1.0)


def test_bulk_restart_uses_direct_api_for_axeos(dash, monkeypatch):
    """miner_control has no AxeOS restart; bulk restart must not fail every Bitaxe."""
    fake = FakeControl()
    fake.family_for = lambda t: "axeos"
    fake.RESTART_FAMILIES = frozenset({"avalon", "antminer"})
    monkeypatch.setattr(dash, "miner_control", fake)
    direct = []
    monkeypatch.setattr(dash, "_restart_device_direct", lambda ip, t, name="": (direct.append((ip, t)) or (True, "sent")))
    r = dash.app.test_client().post("/api/fleet/batch/control", environ_base=LOCAL,
                                    json={"action": "restart", "ips": ["192.168.1.10"]}).get_json()
    assert r["successful"] == 1
    assert direct == [("192.168.1.10", "axeos")] and fake.calls == []

    fake.family_for = lambda t: "avalon"
    r = dash.app.test_client().post("/api/fleet/batch/control", environ_base=LOCAL,
                                    json={"action": "restart", "ips": ["192.168.1.11"]}).get_json()
    assert r["successful"] == 1 and fake.calls == [("restart", "192.168.1.11", {})]
