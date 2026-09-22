# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""The SimpleSwap link in sats_surge alerts has its own off switch.

NOSEC.md and OPERATIONS.md both promise the swap link can be turned off
independently of the alert that carries it. Before this, the only way to stop
seeing a third-party exchange link was to mute `sats_surge` entirely and lose
the surge notification with it.

These tests pin both halves: the link still ships by default (no behaviour
change for existing installs, whose config files predate the key), and setting
`simpleswap_enabled: false` removes the link while the surge alert itself keeps
firing.

Run: python -m pytest tests/test_simpleswap_toggle.py -v
"""
import datetime
import importlib.util
import os
import tempfile

os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel_simpleswap",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)


def _surge_info():
    return {
        "coin": "DGB",
        "current_sats": 62,
        "baseline_sats": 40,
        "change_pct": 55.0,
        "baseline_date": datetime.datetime(2026, 9, 14),
        "lookback_days": 7,
    }


def _field_names(embed):
    return [f["name"] for f in embed.get("fields", [])]


def _swap_fields(embed):
    return [f for f in embed.get("fields", []) if "SimpleSwap" in f["name"]]


def test_default_config_ships_the_key_enabled():
    """Existing installs keep the link — the default is on."""
    assert sentinel.DEFAULT_CONFIG["simpleswap_enabled"] is True


def test_link_present_when_enabled(monkeypatch):
    monkeypatch.setitem(sentinel.CONFIG, "simpleswap_enabled", True)
    embed = sentinel.create_sats_surge_embed(_surge_info())
    swap = _swap_fields(embed)
    assert len(swap) == 1, f"expected one SimpleSwap field, got {_field_names(embed)}"
    assert "simpleswap.io/exchange?from=dgb&to=btc" in swap[0]["value"]


def test_link_present_when_key_absent(monkeypatch):
    """A config file written before the key existed must behave as before."""
    cfg = dict(sentinel.CONFIG)
    cfg.pop("simpleswap_enabled", None)
    monkeypatch.setattr(sentinel, "CONFIG", cfg)
    assert len(_swap_fields(sentinel.create_sats_surge_embed(_surge_info()))) == 1


def test_link_suppressed_when_disabled(monkeypatch):
    monkeypatch.setitem(sentinel.CONFIG, "simpleswap_enabled", False)
    embed = sentinel.create_sats_surge_embed(_surge_info())
    assert _swap_fields(embed) == [], f"swap link survived the off switch: {_field_names(embed)}"


def test_surge_alert_still_fires_with_link_off(monkeypatch):
    """Turning the link off must not silence the surge alert itself."""
    monkeypatch.setitem(sentinel.CONFIG, "simpleswap_enabled", False)
    embed = sentinel.create_sats_surge_embed(_surge_info())
    assert "DGB" in embed["description"]
    assert "SAT VALUE UP" in embed["description"]
    assert any("Recommendation" in n for n in _field_names(embed))
