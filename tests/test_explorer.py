# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Block explorer: node queries (explorer.py) and the dashboard routes.

Run: python -m pytest tests/test_explorer.py -v
"""
import json
import os
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..', 'src', 'dashboard'))

import explorer  # noqa: E402

TIP_HASH = "a" * 64
PREV_HASH = "b" * 64
OLD_HASH = "c" * 64
COINBASE_TXID = "d" * 64
OTHER_TXID = "e" * 64


class FakeRPC:
    """Answers RPC calls from a table keyed by (method, params) or by method alone."""

    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __call__(self, method, params=None, timeout=None):
        self.calls.append((method, params, timeout))
        key = (method, json.dumps(params))
        if key in self.responses:
            return self.responses[key]
        return self.responses.get(method)


def header(height, block_hash, prev=None, nxt=None):
    return {
        "height": height, "hash": block_hash, "time": 1789500000 + height, "nTx": 2,
        "difficulty": 1.5, "confirmations": 3, "version": 536870912, "bits": "1d00ffff",
        "nonce": 42, "merkleroot": "f" * 64, "previousblockhash": prev, "nextblockhash": nxt,
    }


def node(pruned=False, prune_height=0, tip=101):
    return {
        ("getblockchaininfo", "null"): {
            "chain": "main", "blocks": tip, "headers": tip, "bestblockhash": TIP_HASH,
            "pruned": pruned, "pruneheight": prune_height, "size_on_disk": 5 * 1024 ** 3,
        },
    }


# ── chain_summary ────────────────────────────────────────────────────────────

def test_summary_lists_recent_blocks_and_marks_pool_blocks():
    responses = node(pruned=True, prune_height=90)
    responses.update({
        ("getmempoolinfo", "null"): {"size": 12, "bytes": 4096},
        ("getblockhash", "[101]"): TIP_HASH,
        ("getblockhash", "[100]"): PREV_HASH,
        ("getblockheader", json.dumps([TIP_HASH])): header(101, TIP_HASH, PREV_HASH),
        ("getblockheader", json.dumps([PREV_HASH])): header(100, PREV_HASH),
    })
    summary = explorer.chain_summary(FakeRPC(responses), found_hashes={PREV_HASH}, recent=2)

    assert summary["pruned"] is True and summary["pruneHeight"] == 90
    assert summary["mempool"] == {"size": 12, "bytes": 4096}
    assert [b["height"] for b in summary["recentBlocks"]] == [101, 100]
    assert [b["foundByPool"] for b in summary["recentBlocks"]] == [False, True]


def test_summary_node_down_is_503():
    with pytest.raises(explorer.ExplorerError) as err:
        explorer.chain_summary(FakeRPC({}))
    assert err.value.status == 503


# ── block_detail ─────────────────────────────────────────────────────────────

def test_block_by_height_returns_body_and_coinbase_payout():
    responses = node()
    responses.update({
        ("getblockhash", "[101]"): TIP_HASH,
        ("getblockheader", json.dumps([TIP_HASH])): header(101, TIP_HASH, PREV_HASH),
        ("getblock", json.dumps([TIP_HASH, 1])): {"tx": [COINBASE_TXID, OTHER_TXID], "size": 400, "weight": 1600},
        ("getrawtransaction", json.dumps([COINBASE_TXID, True, TIP_HASH])): {
            "txid": COINBASE_TXID, "blockhash": TIP_HASH, "confirmations": 3,
            "vin": [{"coinbase": "03aabbcc"}],
            "vout": [{"n": 0, "value": 3.125, "scriptPubKey": {"address": "bc1qminer", "type": "witness_v0_keyhash"}}],
        },
    })
    rpc = FakeRPC(responses)
    block = explorer.block_detail(rpc, "101", found_hashes={TIP_HASH})

    assert block["bodyAvailable"] is True and block["foundByPool"] is True
    assert block["txids"] == [COINBASE_TXID, OTHER_TXID]
    assert block["coinbase"]["vin"] == [{"coinbase": True}]
    assert block["coinbase"]["vout"][0]["address"] == "bc1qminer"


def test_pruned_block_returns_header_and_explains():
    responses = node(pruned=True, prune_height=90)
    responses.update({
        ("getblockheader", json.dumps([OLD_HASH])): header(12, OLD_HASH),
        # getblock fails for pruned data (no entry in the table)
    })
    block = explorer.block_detail(FakeRPC(responses), OLD_HASH.upper())

    assert block["height"] == 12 and block["hash"] == OLD_HASH
    assert block["bodyAvailable"] is False
    assert "pruned" in block["bodyNote"] and "90" in block["bodyNote"]
    assert block["txids"] == []


def test_block_falls_back_to_boolean_verbose():
    # Bitcoin Core 0.14-based daemons (Dogecoin 1.14) reject an integer verbosity.
    responses = node()
    responses.update({
        ("getblockheader", json.dumps([TIP_HASH])): header(101, TIP_HASH),
        ("getblock", json.dumps([TIP_HASH, True])): {"tx": [COINBASE_TXID]},
        ("getrawtransaction", json.dumps([COINBASE_TXID, 1])): {"txid": COINBASE_TXID, "vin": [], "vout": []},
    })
    block = explorer.block_detail(FakeRPC(responses), TIP_HASH)
    assert block["bodyAvailable"] is True
    assert block["coinbase"]["txid"] == COINBASE_TXID


@pytest.mark.parametrize("block_id,status", [
    ("", 400), ("12a", 400), ("-5", 400), ("1" * 11, 400), ("abc" * 21 + "x", 400),
    ("99999", 404),
])
def test_block_bad_or_unknown_id(block_id, status):
    with pytest.raises(explorer.ExplorerError) as err:
        explorer.block_detail(FakeRPC(node()), block_id)
    assert err.value.status == status


# ── tx_detail ────────────────────────────────────────────────────────────────

def test_tx_in_mempool():
    rpc = FakeRPC({("getrawtransaction", json.dumps([OTHER_TXID, True])): {"txid": OTHER_TXID, "vin": [], "vout": []}})
    result = explorer.tx_detail(rpc, OTHER_TXID)
    assert result["inMempool"] is True


def test_tx_not_found_without_block_explains_pruned_limit():
    with pytest.raises(explorer.ExplorerError) as err:
        explorer.tx_detail(FakeRPC({}), OTHER_TXID)
    assert err.value.status == 404
    assert "block hash" in err.value.message


def test_tx_with_block_hash_passes_it_to_the_node():
    rpc = FakeRPC({("getrawtransaction", json.dumps([OTHER_TXID, True, TIP_HASH])): {
        "txid": OTHER_TXID, "blockhash": TIP_HASH, "confirmations": 1, "vin": [], "vout": []}})
    result = explorer.tx_detail(rpc, OTHER_TXID, block_hash=TIP_HASH)
    assert result["inMempool"] is False
    assert rpc.calls[0] == ("getrawtransaction", [OTHER_TXID, True, TIP_HASH], None)


@pytest.mark.parametrize("txid,block", [("nothex", None), (OTHER_TXID, "nothex")])
def test_tx_bad_input(txid, block):
    rpc = FakeRPC({})
    with pytest.raises(explorer.ExplorerError) as err:
        explorer.tx_detail(rpc, txid, block)
    assert err.value.status == 400
    assert rpc.calls == []


# ── address_utxos ────────────────────────────────────────────────────────────

def test_address_scan_uses_addr_descriptor_and_long_timeout():
    address = "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a"
    rpc = FakeRPC({"scantxoutset": {
        "success": True, "height": 101, "total_amount": 6.25,
        "unspents": [{"txid": COINBASE_TXID, "vout": 0, "amount": 6.25, "height": 90}],
    }})
    result = explorer.address_utxos(rpc, address)

    assert rpc.calls == [("scantxoutset", ["start", [f"addr({address})"]], explorer.SCAN_TIMEOUT)]
    assert result["totalAmount"] == 6.25 and result["utxoCount"] == 1


@pytest.mark.parametrize("address", [
    "", "short", "bc1qaaaaaaaaaaaaaaaaaaaaaaa)#checksum", "addr(bc1q)", "raw(00)",
    "bc1q aaaaaaaaaaaaaaaaaaaaaa", "combo(bc1qaaaaaaaaaaaaaaaaaaaaaaaa)",
])
def test_address_descriptor_injection_refused(address):
    rpc = FakeRPC({"scantxoutset": {"success": True, "unspents": []}})
    with pytest.raises(explorer.ExplorerError) as err:
        explorer.address_utxos(rpc, address)
    assert err.value.status == 400
    assert rpc.calls == []


def test_address_scan_failure_is_502():
    with pytest.raises(explorer.ExplorerError) as err:
        explorer.address_utxos(FakeRPC({}), "bc1qaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
    assert err.value.status == 502


# ── dashboard routes ─────────────────────────────────────────────────────────

@pytest.fixture
def dash(monkeypatch):
    import dashboard
    monkeypatch.setattr(dashboard, "_explorer_pool_block_hashes", lambda symbol: {PREV_HASH})
    return dashboard


def test_routes_require_login(dash):
    client = dash.app.test_client()
    for path in ["/api/explorer/coins", "/api/explorer/DGB/summary", "/api/explorer/DGB/address/bc1qaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]:
        res = client.get(path, environ_base={"REMOTE_ADDR": "203.0.113.9"})
        assert res.status_code == 401, path


def test_summary_route_queries_the_coin_node(dash, monkeypatch):
    responses = node(tip=100)
    responses.update({
        ("getblockhash", "[100]"): PREV_HASH,
        ("getblockheader", json.dumps([PREV_HASH])): header(100, PREV_HASH),
    })
    fake = FakeRPC(responses)
    queried = []

    def coin_rpc(symbol, method, params=None, wallet=None, timeout=10):
        queried.append(symbol)
        return fake(method, params, timeout)

    monkeypatch.setattr(dash, "AUTH_ENABLED", False)
    monkeypatch.setattr(dash, "coin_rpc", coin_rpc)
    client = dash.app.test_client()

    res = client.get("/api/explorer/dgb/summary", environ_base={"REMOTE_ADDR": "127.0.0.1"})
    assert res.status_code == 200
    body = res.get_json()
    assert body["recentBlocks"][0]["foundByPool"] is True
    assert set(queried) == {"DGB"}

    assert client.get("/api/explorer/NOPE/summary", environ_base={"REMOTE_ADDR": "127.0.0.1"}).status_code == 404
    bad = client.get("/api/explorer/DGB/block/notablock", environ_base={"REMOTE_ADDR": "127.0.0.1"})
    assert bad.status_code == 400 and "error" in bad.get_json()


def test_second_address_scan_is_refused_while_one_runs(dash, monkeypatch):
    monkeypatch.setattr(dash, "AUTH_ENABLED", False)
    lock = dash._explorer_scan_locks["DGB"]
    assert lock.acquire(blocking=False)
    try:
        res = dash.app.test_client().get(
            "/api/explorer/DGB/address/bc1qaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            environ_base={"REMOTE_ADDR": "127.0.0.1"},
        )
        assert res.status_code == 409
    finally:
        lock.release()


def test_explorer_script_never_parses_node_data_as_html():
    path = os.path.join(os.path.dirname(__file__), '..', 'src', 'dashboard', 'static', 'js', 'explorer.js')
    with open(path, encoding="utf-8") as f:
        js = f.read()
    for sink in ["innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("]:
        assert sink not in js, sink
