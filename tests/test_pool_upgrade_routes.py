# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Dashboard "Upgrade Spiral Pool" endpoints (v3.1.0).

The old check parsed upgrade.sh --check's JSON as prose ("update available" in
the text) and so always answered "up to date"; the old apply ran upgrade.sh as a
child of the dashboard, inside its ProtectSystem=strict sandbox and its cgroup,
which upgrade.sh then stopped. These pin the replacements: JSON in, the root-owned
launcher (no arguments) out, and a status endpoint the page can follow.

Run: python -m pytest tests/test_pool_upgrade_routes.py -v
"""
import json
import os
import subprocess
import sys
import types

import pytest

ROOT = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, os.path.join(ROOT, "src", "dashboard"))
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", os.path.join(ROOT, "config"))

LOCAL = {"REMOTE_ADDR": "127.0.0.1"}


@pytest.fixture()
def dash(tmp_path, monkeypatch):
    import dashboard
    dashboard.AUTH_ENABLED = False
    dashboard.app.config["TESTING"] = True
    install = tmp_path / "spiralpool"
    (install / "scripts").mkdir(parents=True)
    (install / "scripts" / "upgrade.sh").write_text("#!/bin/bash\n")
    (install / "VERSION").write_text("3.1.0\n")
    monkeypatch.setenv("SPIRALPOOL_INSTALL_DIR", str(install))
    monkeypatch.setattr(dashboard, "POOL_UPGRADE_LOG", str(tmp_path / "upgrade.log"))
    monkeypatch.setattr(dashboard, "check_rate_limit", lambda *a, **k: True)
    dashboard._pool_upgrade_check_cache.update(at=0.0, data=None)
    dashboard.ha_status_cache["enabled"] = False
    dashboard.ha_status_cache["local_role"] = "UNKNOWN"
    dashboard._install = install
    return dashboard


class FakeRun:
    """Records subprocess.run calls; answers by the first matching rule."""

    def __init__(self, rules):
        self.rules = rules
        self.calls = []

    def __call__(self, cmd, **kwargs):
        self.calls.append(list(cmd))
        for match, out in self.rules:
            if match(cmd):
                rc, stdout, stderr = out
                return types.SimpleNamespace(returncode=rc, stdout=stdout, stderr=stderr)
        return types.SimpleNamespace(returncode=0, stdout="ActiveState=inactive\nResult=\n", stderr="")


def _is_show(cmd):
    return cmd[:2] == ["systemctl", "show"]


def test_check_reads_upgrade_sh_json(dash, monkeypatch):
    payload = {"current_version": "3.1.0", "latest_version": "3.2.0", "update_available": True,
               "release_url": "https://example.invalid/r", "release_published_at": "2026-12-01T00:00:00Z"}
    fake = FakeRun([(lambda c: "--check" in c, (0, json.dumps(payload), ""))])
    monkeypatch.setattr(subprocess, "run", fake)
    client = dash.app.test_client()

    r = client.get("/api/system/pool-upgrade/check", environ_base=LOCAL).get_json()
    assert r["success"] and r["update_available"] is True
    assert r["latest_version"] == "3.2.0" and r["current_version"] == "3.1.0"
    # No sudo: --check needs no root, and runs the root-owned copy.
    assert fake.calls[0][0] == "/bin/bash" and fake.calls[0][1].endswith(os.path.join("scripts", "upgrade.sh"))

    client.get("/api/system/pool-upgrade/check", environ_base=LOCAL)
    assert len(fake.calls) == 1, "second check within 15 min should be served from cache"
    client.get("/api/system/pool-upgrade/check?refresh=1", environ_base=LOCAL)
    assert len(fake.calls) == 2, "?refresh=1 bypasses the cache"


def test_check_up_to_date(dash, monkeypatch):
    payload = {"current_version": "3.1.0", "latest_version": "3.1.0", "update_available": False}
    monkeypatch.setattr(subprocess, "run", FakeRun([(lambda c: "--check" in c, (0, json.dumps(payload), ""))]))
    r = dash.app.test_client().get("/api/system/pool-upgrade/check", environ_base=LOCAL).get_json()
    assert r["success"] and r["update_available"] is False


def test_apply_without_launcher_explains(dash, monkeypatch):
    monkeypatch.setattr(subprocess, "run", FakeRun([]))
    res = dash.app.test_client().post("/api/system/pool-upgrade/apply", environ_base=LOCAL)
    assert res.status_code == 501
    assert "spiralctl upgrade" in res.get_json()["error"]


def test_apply_runs_only_the_launcher(dash, monkeypatch):
    launcher = dash._install / "scripts" / "pool-upgrade-launch.sh"
    launcher.write_text("#!/bin/bash\n")
    fake = FakeRun([(lambda c: c[:2] == ["sudo", "-n"], (0, "started spiralpool-upgrade\n", ""))])
    monkeypatch.setattr(subprocess, "run", fake)
    res = dash.app.test_client().post("/api/system/pool-upgrade/apply", environ_base=LOCAL)
    assert res.status_code == 202, res.get_json()
    sudo_calls = [c for c in fake.calls if c and c[0] == "sudo"]
    assert sudo_calls == [["sudo", "-n", str(launcher)]], "launcher only, no arguments"


def test_apply_refuses_ha_backup(dash, monkeypatch):
    (dash._install / "scripts" / "pool-upgrade-launch.sh").write_text("#!/bin/bash\n")
    fake = FakeRun([])
    monkeypatch.setattr(subprocess, "run", fake)
    dash.ha_status_cache["enabled"] = True
    dash.ha_status_cache["local_role"] = "BACKUP"
    res = dash.app.test_client().post("/api/system/pool-upgrade/apply", environ_base=LOCAL)
    assert res.status_code == 409
    assert not [c for c in fake.calls if c and c[0] == "sudo"]


def test_apply_refuses_when_already_running(dash, monkeypatch):
    (dash._install / "scripts" / "pool-upgrade-launch.sh").write_text("#!/bin/bash\n")
    fake = FakeRun([(_is_show, (0, "ActiveState=active\nResult=success\n", ""))])
    monkeypatch.setattr(subprocess, "run", fake)
    res = dash.app.test_client().post("/api/system/pool-upgrade/apply", environ_base=LOCAL)
    assert res.status_code == 409
    assert not [c for c in fake.calls if c and c[0] == "sudo"]


def test_status_running_and_finished(dash, monkeypatch, tmp_path):
    log = tmp_path / "upgrade.log"
    log.write_text("=== Spiral Pool upgrade started at x ===\n\x1b[0;32mStopping services\x1b[0m\n")
    monkeypatch.setattr(subprocess, "run", FakeRun([(_is_show, (0, "ActiveState=active\n", ""))]))
    client = dash.app.test_client()
    s = client.get("/api/system/pool-upgrade/status", environ_base=LOCAL).get_json()
    assert s["running"] is True and s["finished"] is None
    assert "Stopping services" in s["log"] and "\x1b" not in s["log"], "ANSI stripped"

    with open(log, "a") as f:
        f.write("=== Spiral Pool upgrade finished (exit 0) at y ===\n")
    monkeypatch.setattr(subprocess, "run", FakeRun([(_is_show, (0, "ActiveState=inactive\n", ""))]))
    s = client.get("/api/system/pool-upgrade/status", environ_base=LOCAL).get_json()
    assert s["running"] is False and s["finished"] == {"exit_code": 0, "ok": True}
    assert s["version"] == "3.1.0"


def test_status_reports_failure(dash, monkeypatch, tmp_path):
    (tmp_path / "upgrade.log").write_text("boom\n=== Spiral Pool upgrade finished (exit 3) at y ===\n")
    monkeypatch.setattr(subprocess, "run", FakeRun([]))
    s = dash.app.test_client().get("/api/system/pool-upgrade/status", environ_base=LOCAL).get_json()
    assert s["finished"] == {"exit_code": 3, "ok": False}
