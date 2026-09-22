# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Dashboard Automation page: API routes, credentials, the Avalon schedule
migration, celebration quiet hours and deployment of the shared module.

Run: python -m pytest tests/test_automation_routes.py -v
"""
import json
import os
import stat
import sys
from datetime import datetime, timedelta, timezone

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}
S21, NANO, DOGE = "192.168.1.30", "192.168.1.14", "192.168.1.40"
CONFIG = {"devices": {
    "antminer": [{"name": "S21 garage", "ip": S21, "port": 4028}],
    "avalon": [{"name": "Nano desk", "ip": NANO, "port": 4028}],
    "goldshell": [{"name": "Doge box", "ip": DOGE}],
}}


@pytest.fixture
def dash(monkeypatch, tmp_path):
    import dashboard
    monkeypatch.setattr(dashboard, "AUTH_ENABLED", False)
    monkeypatch.setattr(dashboard, "_automation_data_dir", lambda: tmp_path)
    monkeypatch.setattr(dashboard, "load_config", lambda: CONFIG)
    monkeypatch.setattr(dashboard, "_sentinel_config", lambda: {"display_timezone": "UTC", "auto_restart_min_offline": 25})
    return dashboard


def sleep_rule(**overrides):
    rule = {"id": "night", "name": "Night", "enabled": True, "devices": [S21], "days": list(range(7)),
            "start": "22:00", "end": "06:00", "action": "sleep"}
    rule.update(overrides)
    return rule


def test_routes_require_login():
    import dashboard
    client = dashboard.app.test_client()
    remote = {"REMOTE_ADDR": "203.0.113.9"}
    assert client.get("/api/automation", environ_base=remote).status_code == 401
    assert client.put("/api/automation", json={}, environ_base=remote).status_code == 401
    assert client.put(f"/api/automation/credentials/{S21}", json={"password": "x"}, environ_base=remote).status_code == 401


def test_get_lists_miners_and_never_returns_passwords(dash, tmp_path):
    (tmp_path / "device_credentials.json").write_text(json.dumps({S21: {"username": "root", "password": "hunter2"}}))
    (tmp_path / "automation.json").write_text(json.dumps({"devices": {NANO: {"model": "nano3s"}}}))

    res = dash.app.test_client().get("/api/automation", environ_base=LOCAL)
    assert res.status_code == 200
    assert "hunter2" not in res.get_data(as_text=True)
    miners = {m["ip"]: m for m in res.get_json()["miners"]}
    assert miners[S21]["has_credentials"] is True and miners[S21]["credentials"] is True
    assert miners[NANO]["sleep"] is False and miners[NANO]["levels"] == ["low", "normal", "high"]
    assert miners[DOGE]["sleep"] is False and miners[DOGE]["credentials"] is False
    body = res.get_json()
    assert body["auto_restart_defaults"]["offline_minutes"] == 25 and body["timezone"] == "UTC"


def test_put_saves_valid_settings(dash, tmp_path):
    client = dash.app.test_client()
    payload = {"rules": [sleep_rule()], "devices": {S21: {"model": "", "auto_restart": False}},
               "auto_restart": {"enabled": True, "offline_minutes": 15, "cooldown_minutes": 30, "low_hashrate_minutes": 45}}
    res = client.put("/api/automation", json=payload, environ_base=LOCAL)
    assert res.status_code == 200, res.get_json()

    saved = json.loads((tmp_path / "automation.json").read_text())
    assert saved["rules"][0]["id"] == "night" and saved["devices"][S21]["auto_restart"] is False
    assert saved["auto_restart"]["low_hashrate_minutes"] == 45
    assert client.get("/api/automation", environ_base=LOCAL).get_json()["settings"] == saved


def test_put_refuses_rules_a_miner_cannot_follow_and_keeps_the_file(dash, tmp_path):
    path = tmp_path / "automation.json"
    path.write_text(json.dumps({"rules": []}))
    res = dash.app.test_client().put("/api/automation", json={"rules": [sleep_rule(devices=[DOGE])]}, environ_base=LOCAL)
    assert res.status_code == 400
    assert any("Goldshell) cannot sleep" in e for e in res.get_json()["errors"])
    assert json.loads(path.read_text()) == {"rules": []}


def test_unreadable_automation_file_is_reported_not_replaced(dash, tmp_path):
    (tmp_path / "automation.json").write_text("{broken")
    res = dash.app.test_client().get("/api/automation", environ_base=LOCAL)
    assert res.status_code == 500 and "untouched" in res.get_json()["error"]


def test_credentials_are_write_only(dash, tmp_path):
    client = dash.app.test_client()
    url = f"/api/automation/credentials/{S21}"
    assert client.put(url, json={"username": "root"}, environ_base=LOCAL).status_code == 400
    assert client.put("/api/automation/credentials/192.168.1.99", json={"password": "x"}, environ_base=LOCAL).status_code == 404

    res = client.put(url, json={"username": "root", "password": "s3cret pass"}, environ_base=LOCAL)
    assert res.status_code == 200 and "s3cret" not in res.get_data(as_text=True)
    path = tmp_path / "device_credentials.json"
    assert json.loads(path.read_text()) == {S21: {"username": "root", "password": "s3cret pass"}}
    if os.name != "nt":
        assert stat.S_IMODE(path.stat().st_mode) == 0o600

    assert client.delete(url, environ_base=LOCAL).status_code == 200
    assert json.loads(path.read_text()) == {}


def test_old_scheduler_sends_home_avalon_models_to_the_automation_page(dash, monkeypatch):
    monkeypatch.setattr(dash, "save_avalon_schedules", lambda: None)
    res = dash.app.test_client().post(f"/api/avalon/schedules/{NANO}", environ_base=LOCAL,
                                      json={"enabled": True, "model": "avalon_q", "rules": []})
    assert res.get_json()["success"] is False and "Automation page" in res.get_json()["error"]


def test_migration_moves_home_models_keeps_others_and_backs_up(dash, monkeypatch, tmp_path):
    schedules = {
        NANO: {"enabled": True, "model": "nano3s", "rules": [{"start": "09:00", "end": "17:00", "profile": "efficiency"}]},
        "192.168.1.20": {"enabled": True, "model": "a1566", "rules": [{"start": "01:00", "end": "02:00", "profile": "high"}]},
    }
    config_dir = tmp_path / "dashboard"
    config_dir.mkdir()
    saves = []
    monkeypatch.setattr(dash, "avalon_schedules", schedules)
    monkeypatch.setattr(dash, "CONFIG_DIR", str(config_dir))
    monkeypatch.setattr(dash, "save_avalon_schedules", lambda: saves.append(dict(dash.avalon_schedules)))

    assert dash.migrate_avalon_schedules_to_automation() == [NANO]

    automation = json.loads((tmp_path / "automation.json").read_text())
    assert automation["rules"][0]["level"] == "low" and automation["devices"][NANO]["model"] == "nano3s"
    assert list(schedules) == ["192.168.1.20"] and saves == [schedules]
    backup = json.loads((config_dir / "avalon_schedules.json.pre-automation").read_text())
    assert NANO in backup
    assert dash.migrate_avalon_schedules_to_automation() == []


def test_celebrations_are_quiet_inside_a_low_power_window(dash, monkeypatch, tmp_path):
    now = datetime.now(timezone.utc)
    sentinel_dir = tmp_path / "install" / "config" / "sentinel"
    sentinel_dir.mkdir(parents=True)
    (sentinel_dir / "config.json").write_text(json.dumps({
        "quiet_hours_start": (now.hour + 2) % 24, "quiet_hours_end": (now.hour + 3) % 24, "display_timezone": "UTC"}))
    monkeypatch.setenv("SPIRALPOOL_INSTALL_DIR", str(tmp_path / "install"))
    monkeypatch.setattr(dash, "avalon_schedules", {})
    assert dash._is_celebration_quiet_hours() is False

    rule = sleep_rule(devices=[NANO], action="power", level="low",
                      start=(now - timedelta(hours=1)).strftime("%H:%M"), end=(now + timedelta(hours=1)).strftime("%H:%M"))
    (tmp_path / "automation.json").write_text(json.dumps({"rules": [rule]}))
    assert dash._is_celebration_quiet_hours() is True


def test_automation_script_never_parses_server_data_as_html():
    with open(os.path.join(ROOT, "src", "dashboard", "static", "js", "automation.js"), encoding="utf-8") as f:
        js = f.read()
    for sink in ["innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("]:
        assert sink not in js, sink


def test_every_install_path_deploys_the_automation_modules():
    def read(*parts):
        with open(os.path.join(ROOT, *parts), encoding="utf-8") as f:
            return f.read()
    assert "src/sentinel/miner_automation.py /app/miner_automation.py" in read("docker", "Dockerfile.dashboard")
    sentinel_image = read("docker", "Dockerfile.sentinel")
    assert "src/sentinel/miner_automation.py" in sentinel_image and "src/sentinel/miner_control.py" in sentinel_image
    install = read("install.sh")
    assert "for _automation_module in ha_manager.py miner_automation.py miner_control.py" in install
    assert 'src/sentinel/miner_automation.py" "$DASH_DIR/"' in install
    upgrade = read("upgrade.sh")
    assert "for _sentinel_module in ha_manager.py miner_automation.py miner_control.py" in upgrade
    assert '"$DASHBOARD_SOURCE/../sentinel/miner_automation.py" "$DASHBOARD_INSTALL/"' in upgrade
