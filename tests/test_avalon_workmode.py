# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Avalon power profiles send the workmode syntax each firmware accepts.

Nano 3S and Avalon Q firmware takes ascset|0,workmode,set,<n> with
0=Low/Eco, 1=Mid/Standard, 2=High/Super. Older A10-era firmware takes
ascset|0,workmode,<n>.

Run: python -m pytest tests/test_avalon_workmode.py -v
"""
import os
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..', 'src', 'dashboard'))

import dashboard  # noqa: E402


@pytest.fixture
def ascset_calls(monkeypatch):
    calls = []

    def fake_cgminer_command(ip, port, command, parameter=None, timeout=None):
        calls.append((command, parameter))
        return {"STATUS": [{"STATUS": "I", "Msg": "ok"}]}

    monkeypatch.setattr(dashboard, "cgminer_command", fake_cgminer_command)
    return calls


@pytest.mark.parametrize("model,profile,expected", [
    ("nano3s", "efficiency", "0,workmode,set,0"),
    ("nano3s", "balanced", "0,workmode,set,1"),
    ("nano3s", "high", "0,workmode,set,2"),
    ("avalon_q", "efficiency", "0,workmode,set,0"),
    ("Avalon_Q", "high", "0,workmode,set,2"),
])
def test_home_series_uses_workmode_set(ascset_calls, model, profile, expected):
    result = dashboard.apply_avalon_profile("192.168.1.14", profile, model)
    assert result["success"] is True
    # Canaan documents only the workmode for these, so nothing else is written.
    assert ascset_calls == [("ascset", expected)]


def test_older_models_keep_plain_workmode(ascset_calls):
    result = dashboard.apply_avalon_profile("192.168.1.20", "high", "a1566")
    assert result["success"] is True
    assert ascset_calls[0] == ("ascset", "0,workmode,1")
    assert ("ascset", "0,freq,650") in ascset_calls
