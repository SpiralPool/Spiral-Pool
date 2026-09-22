# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Sentinel's use of the automation modules: restart routing, miner lookup,
settings reload, auto-restart overrides and the per-cycle rule run.

Run: python -m pytest tests/test_sentinel_automation.py -v
"""
import importlib.util
import json
import os
import sys
import tempfile
from datetime import timedelta

import pytest

os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
SENTINEL_DIR = os.path.join(os.path.dirname(__file__), "..", "src", "sentinel")
SENTINEL_PATH = os.path.join(SENTINEL_DIR, "SpiralSentinel.py")
# Run as a script, Sentinel finds its sibling modules in its own directory.
sys.path.insert(0, SENTINEL_DIR)
_spec = importlib.util.spec_from_file_location("spiral_sentinel_automation", SENTINEL_PATH)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

IP = "192.168.1.50"


@pytest.fixture(autouse=True)
def isolated(monkeypatch, tmp_path):
    assert sentinel._AUTOMATION_AVAILABLE, "miner_automation / miner_control failed to import"
    monkeypatch.setattr(sentinel, "AUTOMATION_FILE", tmp_path / "automation.json")
    monkeypatch.setattr(sentinel, "DEVICE_CREDENTIALS_FILE", tmp_path / "device_credentials.json")
    monkeypatch.setattr(sentinel, "AUTOMATION_STATE_FILE", tmp_path / "automation_state.json")
    monkeypatch.setattr(sentinel, "_automation_cache", {"mtime": None, "settings": None})
    monkeypatch.setattr(sentinel, "_credentials_cache", {"mtime": None, "credentials": {}})
    monkeypatch.setattr(sentinel, "_automation_runner", None)
    monkeypatch.setattr(sentinel, "_miner_ip_lookup", {})
    monkeypatch.setattr(sentinel, "_miner_type_lookup", {})
    monkeypatch.setattr(sentinel, "MINERS", {})
    return tmp_path


def write(path, data):
    path.write_text(json.dumps(data), encoding="utf-8")
    # mtime granularity can hide a rewrite within the same tick
    stamp = path.stat().st_mtime + 1
    os.utime(path, (stamp, stamp))


# ── restart routing ──────────────────────────────────────────────────────────

def test_driver_families_restart_through_miner_control_with_credentials(monkeypatch):
    write(sentinel.DEVICE_CREDENTIALS_FILE, {IP: {"username": "root", "password": "s3cret"}})
    sent = []
    monkeypatch.setattr(sentinel.miner_control, "restart", lambda device: sent.append(device))
    assert sentinel.restart_miner("braiins", IP) is True
    assert sent == [{"ip": IP, "type": "braiins", "model": "", "port": 4028, "username": "root", "password": "s3cret"}]


def test_driver_failure_falls_back_to_the_older_restart(monkeypatch):
    def refuse(device):
        raise sentinel.miner_control.ControlError("no reply")
    monkeypatch.setattr(sentinel.miner_control, "restart", refuse)
    monkeypatch.setattr(sentinel, "restart_axeos", lambda ip: True)
    assert sentinel.restart_miner("avalon", IP) is True


def test_other_families_keep_their_existing_restart(monkeypatch):
    def unexpected(device):
        raise AssertionError("AxeOS restarts must not go through miner_control")
    monkeypatch.setattr(sentinel.miner_control, "restart", unexpected)
    monkeypatch.setattr(sentinel, "restart_axeos", lambda ip: True)
    assert sentinel.restart_miner("nerdqaxe", IP) is True


# ── lookup ───────────────────────────────────────────────────────────────────

def test_find_miner_follows_the_ip_when_polling_renamed_the_miner(monkeypatch):
    monkeypatch.setattr(sentinel, "MINERS", {"whatsminer": [{"ip": IP, "name": IP, "port": 4028}]})
    monkeypatch.setattr(sentinel, "_miner_ip_lookup", {"rig-7": IP})
    monkeypatch.setattr(sentinel, "_miner_type_lookup", {"rig-7": "whatsminer"})
    assert sentinel.find_miner("rig-7") == (IP, "whatsminer", 4028)
    assert sentinel.find_miner(IP) == (IP, "whatsminer", 4028)
    assert sentinel.find_miner("nobody") == (None, None, None)


# ── settings ─────────────────────────────────────────────────────────────────

def test_settings_reload_and_a_corrupt_file_keeps_the_last_good_ones():
    assert sentinel.auto_restart_settings() == (sentinel.AUTO_RESTART, sentinel.AUTO_RESTART_MIN, sentinel.AUTO_RESTART_COOL, 0)

    write(sentinel.AUTOMATION_FILE, {"auto_restart": {"enabled": True, "offline_minutes": 7,
                                                      "cooldown_minutes": 12, "low_hashrate_minutes": 30}})
    assert sentinel.auto_restart_settings() == (True, 7, 720, 30)

    sentinel.AUTOMATION_FILE.write_text("{broken", encoding="utf-8")
    stamp = sentinel.AUTOMATION_FILE.stat().st_mtime + 5
    os.utime(sentinel.AUTOMATION_FILE, (stamp, stamp))
    assert sentinel.auto_restart_settings() == (True, 7, 720, 30)


def test_auto_restart_respects_the_per_miner_opt_out(monkeypatch):
    monkeypatch.setattr(sentinel, "_miner_ip_lookup", {"rig": IP})
    assert sentinel.auto_restart_allowed("rig") is True
    write(sentinel.AUTOMATION_FILE, {"devices": {IP: {"model": "", "auto_restart": False}}})
    assert sentinel.auto_restart_allowed("rig") is False


# ── per-cycle rule run ───────────────────────────────────────────────────────

def sleep_rule_now():
    now = sentinel.local_now()
    start, end = now - timedelta(minutes=1), now + timedelta(hours=2)
    return {"id": "night", "name": "Night", "enabled": True, "devices": [IP], "days": list(range(7)),
            "start": start.strftime("%H:%M"), "end": end.strftime("%H:%M"), "action": "sleep"}


def test_run_automation_sleeps_the_miner_persists_it_and_suppresses_checks(monkeypatch):
    monkeypatch.setattr(sentinel, "MINERS", {"braiins": [{"ip": IP, "name": "rig", "port": 4028}]})
    monkeypatch.setattr(sentinel, "_miner_ip_lookup", {"rig": IP})
    write(sentinel.AUTOMATION_FILE, {"rules": [sleep_rule_now()]})
    slept = []
    monkeypatch.setattr(sentinel.miner_control, "sleep", lambda device: slept.append(device["ip"]))

    sentinel.run_automation({"rig": "online"}, state=None)

    assert slept == [IP]
    assert sentinel.automation_asleep("rig") and not sentinel.auto_restart_allowed("rig")
    saved = json.loads(sentinel.AUTOMATION_STATE_FILE.read_text(encoding="utf-8"))
    assert saved["asleep"] == {IP: "night"}

    # A restarted Sentinel reads the saved state.
    monkeypatch.setattr(sentinel, "_automation_runner", None)
    assert sentinel.automation_asleep("rig")


def test_run_automation_alerts_when_an_action_fails(monkeypatch):
    monkeypatch.setattr(sentinel, "MINERS", {"braiins": [{"ip": IP, "name": "rig", "port": 4028}]})
    monkeypatch.setattr(sentinel, "_miner_ip_lookup", {"rig": IP})
    write(sentinel.AUTOMATION_FILE, {"rules": [sleep_rule_now()]})

    def refuse(device):
        raise sentinel.miner_control.ControlError("login failed: HTTP 401 `x`")
    monkeypatch.setattr(sentinel.miner_control, "sleep", refuse)
    alerts = []
    monkeypatch.setattr(sentinel, "send_alert", lambda kind, embed, state=None, miner_name=None: alerts.append((kind, embed, miner_name)))

    sentinel.run_automation({"rig": "online"}, state=None)

    assert [(kind, name) for kind, _, name in alerts] == [("automation_failed", IP)]
    assert "`x`" not in json.dumps(alerts[0][1])
    assert not sentinel.automation_asleep("rig")


def test_restart_embed_names_the_reason():
    embed = sentinel.create_restart_embed("rig", 25, True, reason="Low hashrate")
    assert "Low hashrate" in json.dumps(embed)


def test_monitor_loop_consults_automation():
    with open(SENTINEL_PATH, encoding="utf-8") as f:
        source = f.read()
    for snippet in [
        "run_automation(miner_status, state)",
        'if st == "offline" and automation_asleep(name):',
        "not is_esp32 and not automation_asleep(name)",
        '== "esp32miner" or automation_asleep(name)',
        "_ar_low_min > 0",
    ]:
        assert snippet in source, snippet
