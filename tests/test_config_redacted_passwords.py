# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""POST /api/config keeps stored device passwords when a client saves back the
***REDACTED*** placeholder that GET /api/config returns in their place.

Run: python -m pytest tests/test_config_redacted_passwords.py -v
"""
import copy
import os
import sys

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}
S21, S19, NEW = "192.168.1.30", "192.168.1.31", "192.168.1.40"
STORED = {"first_run": False, "devices": {
    "antminer": [{"name": "S21", "ip": S21, "port": 4028, "password": "hunter2"}],
    "vnish": [{"name": "S19", "ip": S19, "password": "vnish-pass"}],
}}


@pytest.fixture
def dash(monkeypatch):
    import dashboard
    saved = {}
    monkeypatch.setattr(dashboard, "AUTH_ENABLED", False)
    monkeypatch.setattr(dashboard, "load_config", lambda: copy.deepcopy(STORED))
    monkeypatch.setattr(dashboard, "save_config", lambda cfg: saved.update(copy.deepcopy(cfg)))
    monkeypatch.setattr(dashboard, "sync_miners_to_sentinel", lambda: (0, []))
    monkeypatch.setattr(dashboard, "record_activity", lambda *a, **k: None)
    monkeypatch.setattr(dashboard, "check_rate_limit", lambda *a, **k: True)
    return dashboard.app.test_client(), saved


def test_saving_the_devices_get_returned_keeps_the_stored_passwords(dash):
    client, saved = dash
    shown = client.get("/api/config", environ_base=LOCAL).get_json()
    assert shown["devices"]["antminer"][0]["password"] == "***REDACTED***"

    res = client.post("/api/config", json={"devices": shown["devices"]}, environ_base=LOCAL)
    assert res.status_code == 200, res.get_json()
    assert saved["devices"]["antminer"][0]["password"] == "hunter2"
    assert saved["devices"]["vnish"][0]["password"] == "vnish-pass"


def test_a_new_password_replaces_the_stored_one(dash):
    client, saved = dash
    devices = copy.deepcopy(STORED["devices"])
    devices["antminer"][0]["password"] = "changed"
    devices["vnish"][0]["password"] = "***REDACTED***"

    assert client.post("/api/config", json={"devices": devices}, environ_base=LOCAL).status_code == 200
    assert saved["devices"]["antminer"][0]["password"] == "changed"
    assert saved["devices"]["vnish"][0]["password"] == "vnish-pass"


def test_the_placeholder_is_never_stored_as_a_password(dash):
    client, saved = dash
    devices = copy.deepcopy(STORED["devices"])
    devices["antminer"].append({"name": "new", "ip": NEW, "password": "***REDACTED***"})
    # The same IP under another device type has no stored password to restore.
    devices["vnish"].append({"name": "moved", "ip": S21, "password": "***REDACTED***"})

    assert client.post("/api/config", json={"devices": devices}, environ_base=LOCAL).status_code == 200
    assert "***REDACTED***" not in str(saved)
    assert "password" not in saved["devices"]["antminer"][1]
    assert "password" not in saved["devices"]["vnish"][1]
