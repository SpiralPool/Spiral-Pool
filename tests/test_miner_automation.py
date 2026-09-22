# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Automation rules: validation, schedule windows, the runner and the Avalon
schedule migration (src/sentinel/miner_automation.py).

Run: python -m pytest tests/test_miner_automation.py -v
"""
import os
import sys
from datetime import datetime, timedelta

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src", "sentinel"))

import miner_automation as ma  # noqa: E402

MON = datetime(2026, 9, 14)  # a Monday
IP = "192.168.1.14"


def at(day_offset, hhmm):
    hours, minutes = map(int, hhmm.split(":"))
    return MON + timedelta(days=day_offset, hours=hours, minutes=minutes)


def rule(**overrides):
    base = {"id": "r1", "name": "Night", "enabled": True, "devices": [IP], "days": list(range(7)),
            "start": "22:00", "end": "06:00", "action": "sleep"}
    base.update(overrides)
    return base


def test_monday_fixture_is_a_monday():
    assert MON.weekday() == 0


# ── windows ──────────────────────────────────────────────────────────────────

def test_daytime_window_is_start_inclusive_end_exclusive():
    r = rule(days=[0], start="09:00", end="17:00")
    assert ma.rule_active(r, at(0, "09:00"))
    assert ma.rule_active(r, at(0, "16:59"))
    assert not ma.rule_active(r, at(0, "17:00"))
    assert not ma.rule_active(r, at(1, "10:00"))


def test_overnight_window_belongs_to_the_day_it_starts():
    r = rule(days=[0], start="22:00", end="06:00")
    assert ma.rule_active(r, at(0, "23:00"))
    assert ma.rule_active(r, at(1, "05:59"))
    assert not ma.rule_active(r, at(1, "06:00"))
    assert not ma.rule_active(r, at(1, "22:30"))
    assert not ma.rule_active(r, at(0, "05:00"))  # that one belongs to Sunday


def test_disabled_rule_is_never_active():
    assert not ma.rule_active(rule(enabled=False), at(0, "23:00"))


def test_desired_state_takes_the_first_active_rule_of_each_action():
    rules = [
        rule(id="p1", action="power", level="low", start="00:00", end="23:59"),
        rule(id="p2", action="power", level="high", start="00:00", end="23:59"),
        rule(id="s1"),
    ]
    sleep_rule, power_rule = ma.desired_state(rules, IP, at(0, "23:00"))
    assert sleep_rule["id"] == "s1" and power_rule["id"] == "p1"
    assert ma.desired_state(rules, "192.168.1.99", at(0, "23:00")) == (None, None)


# ── validation ───────────────────────────────────────────────────────────────

def test_valid_settings_pass_and_are_normalized():
    data = {"rules": [rule(start="9:00", end="17:30", action="power", level="low", devices=[IP, IP])],
            "devices": {IP: {"model": "nano3s"}},
            "auto_restart": {"enabled": True, "offline_minutes": 20, "cooldown_minutes": 30, "low_hashrate_minutes": 0}}
    clean, errors = ma.validate_automation(data, device_types={IP: "avalon"})
    assert errors == []
    assert clean["rules"][0]["start"] == "09:00" and clean["rules"][0]["devices"] == [IP]
    assert clean["devices"][IP] == {"model": "nano3s", "auto_restart": True}
    assert clean["auto_restart"]["offline_minutes"] == 20


@pytest.mark.parametrize("overrides,fragment", [
    ({"start": "25:00"}, "HH:MM"),
    ({"start": "06:00"}, "must differ"),
    ({"devices": ["8.8.8.8"]}, "private LAN"),
    ({"devices": []}, "private LAN"),
    ({"days": [7]}, "weekdays"),
    ({"action": "power"}, "either a level or watts"),
    ({"action": "power", "level": "low", "watts": 900}, "either a level or watts"),
    ({"action": "power", "watts": True}, "whole number"),
    ({"action": "power", "level": "turbo"}, "low, normal or high"),
    ({"action": "sleep", "level": "low"}, "no level"),
    ({"action": "reboot"}, "sleep or power"),
    ({"id": "bad id!"}, "unique id"),
    ({"name": "x" * 65}, "64 printable"),
])
def test_invalid_rules_are_refused(overrides, fragment):
    clean, errors = ma.validate_automation({"rules": [rule(**overrides)]})
    assert clean["rules"] == []
    assert any(fragment in e for e in errors), errors


def test_duplicate_rule_ids_are_refused():
    _, errors = ma.validate_automation({"rules": [rule(), rule()]})
    assert any("unique id" in e for e in errors)


def test_rules_a_device_cannot_carry_out_are_refused():
    rules = [rule(id="a"), rule(id="b", devices=["192.168.1.20"], action="power", level="low"),
             rule(id="c", devices=["192.168.1.30"], action="power", watts=900)]
    types = {IP: "goldshell", "192.168.1.20": "avalon", "192.168.1.30": "antminer"}
    _, errors = ma.validate_automation({"rules": rules}, device_types=types)
    assert any("Goldshell) cannot sleep" in e for e in errors)
    assert any("Set its Avalon model first" in e for e in errors)
    assert any("cannot take a 900 W power target" in e for e in errors)
    _, errors = ma.validate_automation({"rules": [rule(devices=["192.168.1.77"])]}, device_types=types)
    assert any("not a configured miner" in e for e in errors)


def test_auto_restart_limits():
    _, errors = ma.validate_automation({"auto_restart": {"enabled": True, "offline_minutes": 0,
                                                          "cooldown_minutes": 30, "low_hashrate_minutes": 0}})
    assert errors
    _, errors = ma.validate_automation({"devices": {IP: {"model": "a1566"}}})
    assert any("unknown Avalon model" in e for e in errors)


def test_avalon_capabilities_depend_on_the_model():
    assert ma.capabilities_for("avalon")["levels"] == ()
    assert ma.capabilities_for("avalon", "avalon_q")["levels"] == ma.LEVELS
    assert ma.capabilities_for("avalon", "nano3s")["sleep"] is False
    assert ma.capabilities_for("mystery")["family"] is None


# ── runner ───────────────────────────────────────────────────────────────────

class FakeControl:
    def __init__(self):
        self.calls = []
        self.fail = set()

    def _do(self, action, device, detail=None):
        self.calls.append((action, device["ip"], detail))
        if action in self.fail:
            raise RuntimeError(f"{action} failed")

    def sleep(self, device):
        self._do("sleep", device)

    def wake(self, device):
        self._do("wake", device)

    def set_power(self, device, level=None, watts=None):
        self._do("power", device, level or watts)


DEVICES = {IP: {"ip": IP, "type": "avalon"}}
SETTINGS = {IP: {"model": "nano3s", "auto_restart": True}}
# Avalons cannot sleep, so the sleep tests use a Bitaxe
SLEEPER = {IP: {"ip": IP, "type": "bitaxe"}}


def automation(*rules):
    return {"rules": list(rules), "devices": SETTINGS}


def test_runner_sleeps_once_then_wakes_when_the_window_ends():
    control = FakeControl()
    runner = ma.AutomationRunner(control)
    auto = automation(rule())

    assert runner.tick(auto, SLEEPER, at(0, "21:59")) == []
    runner.tick(auto, SLEEPER, at(0, "22:00"))
    runner.tick(auto, SLEEPER, at(0, "22:05"))
    assert control.calls == [("sleep", IP, None)]
    assert runner.is_asleep(IP)

    events = runner.tick(auto, SLEEPER, at(1, "06:00"))
    assert control.calls[-1] == ("wake", IP, None)
    assert events == [{"ip": IP, "action": "wake", "ok": True, "error": ""}]
    assert not runner.is_asleep(IP)


def test_runner_backs_off_after_a_failure():
    control = FakeControl()
    control.fail.add("sleep")
    runner = ma.AutomationRunner(control)
    auto = automation(rule())

    events = runner.tick(auto, SLEEPER, at(0, "22:00"))
    assert events[0]["ok"] is False and "sleep failed" in events[0]["error"]
    runner.tick(auto, SLEEPER, at(0, "22:04"))
    assert len(control.calls) == 1
    control.fail.clear()
    runner.tick(auto, SLEEPER, at(0, "22:06"))
    assert len(control.calls) == 2 and runner.is_asleep(IP)


def test_power_mode_is_reapplied_after_the_miner_comes_back():
    control = FakeControl()
    runner = ma.AutomationRunner(control)
    auto = automation(rule(action="power", level="low", start="09:00", end="17:00"))

    runner.tick(auto, DEVICES, at(0, "10:00"), online={IP: True})
    runner.tick(auto, DEVICES, at(0, "10:01"), online={IP: True})
    runner.tick(auto, DEVICES, at(0, "10:02"), online={IP: False})
    assert control.calls == [("power", IP, "low")]
    runner.tick(auto, DEVICES, at(0, "10:03"), online={IP: True})
    assert control.calls == [("power", IP, "low"), ("power", IP, "low")]


def test_power_mode_is_forgotten_outside_its_window():
    control = FakeControl()
    runner = ma.AutomationRunner(control)
    auto = automation(rule(action="power", level="low", start="09:00", end="17:00"))

    runner.tick(auto, DEVICES, at(0, "10:00"))
    runner.tick(auto, DEVICES, at(0, "18:00"))
    runner.tick(auto, DEVICES, at(1, "10:00"))
    assert control.calls == [("power", IP, "low"), ("power", IP, "low")]


def test_disabling_the_rule_wakes_the_miner_and_state_survives_a_restart():
    control = FakeControl()
    runner = ma.AutomationRunner(control)
    runner.tick(automation(rule()), SLEEPER, at(0, "23:00"))

    restarted = ma.AutomationRunner(control, state=runner.state)
    assert restarted.is_asleep(IP)
    restarted.tick(automation(rule(enabled=False)), SLEEPER, at(0, "23:30"))
    assert control.calls[-1] == ("wake", IP, None)


def test_removed_miner_is_forgotten_without_commands():
    control = FakeControl()
    runner = ma.AutomationRunner(control, state={"asleep": {IP: "r1"}, "power": {}})
    runner.tick(automation(), {}, at(0, "12:00"))
    assert control.calls == [] and not runner.is_asleep(IP)


def test_unsupported_action_is_skipped():
    control = FakeControl()
    runner = ma.AutomationRunner(control)
    runner.tick(automation(rule()), {IP: {"ip": IP, "type": "goldshell"}}, at(0, "23:00"))
    assert control.calls == [] and not runner.is_asleep(IP)
    # An Avalon with its model set still has no sleep
    runner.tick(automation(rule()), DEVICES, at(0, "23:00"))
    assert control.calls == [] and not runner.is_asleep(IP)


def test_low_power_active():
    auto = automation(rule(action="power", level="low", start="09:00", end="17:00"))
    assert ma.low_power_active(auto, at(0, "10:00"))
    assert not ma.low_power_active(auto, at(0, "18:00"))


# ── migration ────────────────────────────────────────────────────────────────

def test_migrates_home_avalon_schedules_and_leaves_other_models():
    schedules = {
        IP: {"enabled": True, "model": "nano3s", "rules": [
            {"start": "9:00", "end": "17:00", "profile": "efficiency"},
            {"start": "17:00", "end": "09:00", "profile": "high"},
        ]},
        "192.168.1.20": {"enabled": True, "model": "a1566", "rules": [{"start": "01:00", "end": "02:00", "profile": "high"}]},
    }
    migrated_settings, migrated = ma.migrate_avalon_schedules(schedules, ma.default_automation())

    assert migrated == [IP]
    assert [(r["start"], r["end"], r["level"]) for r in migrated_settings["rules"]] == [
        ("09:00", "17:00", "low"), ("17:00", "09:00", "high")]
    assert migrated_settings["devices"][IP]["model"] == "nano3s"
    clean, errors = ma.validate_automation(migrated_settings, device_types={IP: "avalon"})
    assert errors == [] and len(clean["rules"]) == 2

    again, _ = ma.migrate_avalon_schedules(schedules, migrated_settings)
    assert len(again["rules"]) == 2


def test_json_files_round_trip_and_bad_content_is_an_error(tmp_path):
    path = tmp_path / "automation.json"
    assert ma.read_json(path) is None
    ma.write_json_atomic(path, {"rules": []})
    assert ma.read_json(path) == {"rules": []}
    path.write_text("{not json", encoding="utf-8")
    with pytest.raises(ValueError):
        ma.read_json(path)
