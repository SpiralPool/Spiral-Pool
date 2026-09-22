# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Callers of GET /api/pools/{id}/miners send the pool admin key.

The pool API requires X-API-Key on the miner list whenever admin_api_key is
set, so Sentinel's zombie detection, its Telegram /miners command and
`spiralctl miners` / `spiralctl workers` must all send it.

Run: python -m pytest tests/test_pool_miners_auth.py -v
"""
import importlib.util
import os
import tempfile

ROOT = os.path.join(os.path.dirname(__file__), "..")
SENTINEL_PATH = os.path.join(ROOT, "src", "sentinel", "SpiralSentinel.py")
SPIRALCTL_PATH = os.path.join(ROOT, "scripts", "spiralctl.sh")

os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location("spiral_sentinel_miners_auth", SENTINEL_PATH)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)


def _capture_http(monkeypatch):
    seen = []

    def fake_http(url, timeout=10, headers=None, retries=2):
        seen.append((url, headers))
        return [{"miner": "DAddr.rig1", "hashrate": 1.0}]

    monkeypatch.setattr(sentinel, "_http", fake_http)
    return seen


def test_fetch_pool_miners_sends_admin_key(monkeypatch):
    seen = _capture_http(monkeypatch)
    monkeypatch.setitem(sentinel.CONFIG, "pool_admin_api_key", "secret-key")
    monkeypatch.setitem(sentinel.CONFIG, "pool_id", "dgb_sha256_1")

    miners, per_worker = sentinel.fetch_pool_miners()

    assert per_worker is True and "DAddr.rig1" in miners
    assert seen[0][0].endswith("/api/pools/dgb_sha256_1/miners")
    assert seen[0][1] == {"X-API-Key": "secret-key"}


def test_fetch_pool_miners_for_coin_sends_admin_key(monkeypatch):
    seen = _capture_http(monkeypatch)
    monkeypatch.setitem(sentinel.CONFIG, "pool_admin_api_key", "secret-key")

    assert "DAddr.rig1" in sentinel.fetch_pool_miners_for_coin({"pool_id": "btc_sha256_1"})
    assert seen[0][1] == {"X-API-Key": "secret-key"}


def test_no_key_configured_sends_no_header(monkeypatch):
    seen = _capture_http(monkeypatch)
    monkeypatch.setitem(sentinel.CONFIG, "pool_admin_api_key", "")

    sentinel.fetch_pool_miners_for_coin({"pool_id": "btc_sha256_1"})
    assert seen[0][1] == {}


def test_every_sentinel_miner_list_call_sends_the_key():
    with open(SENTINEL_PATH, encoding="utf-8") as f:
        lines = f.read().splitlines()
    calls = [i for i, line in enumerate(lines) if "/api/pools/" in line and '/miners"' in line]
    assert len(calls) >= 3, "expected the zombie check, per-coin fetch and Telegram /miners calls"
    for i in calls:
        assert any("_pool_admin_headers()" in line for line in lines[i:i + 3]), f"line {i + 1}"


def test_every_spiralctl_miner_list_call_sends_the_key():
    with open(SPIRALCTL_PATH, encoding="utf-8") as f:
        lines = [line for line in f.read().splitlines() if "curl" in line and '/miners"' in line]
    assert len(lines) >= 2, "expected spiralctl miners and spiralctl workers"
    for line in lines:
        assert '-H "X-API-Key: ${admin_key}"' in line, line
