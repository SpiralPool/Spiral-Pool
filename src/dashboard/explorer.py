# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Block explorer queries against the pool's own coin nodes.

Built to work on pruned nodes:

- Block headers exist for every height even when block data is pruned, so any
  height or block hash can be looked up. Full block bodies, and the
  transactions in them, are available only inside the node's retained window.
- A pruned node has no transaction index, so a transaction is found only if it
  is in the mempool or the caller names the block that contains it.
- Address lookups use ``scantxoutset``, which reads the UTXO set and works
  pruned or not, but scans the whole set and can take minutes.

Every function takes an ``rpc(method, params=None, timeout=None)`` callable
that returns the RPC result, or None when the call fails.
"""

import re

HEX64 = re.compile(r"^[0-9a-fA-F]{64}$")

# An optional CashAddr prefix, then base58/bech32/CashAddr characters only:
# nothing that could extend the scantxoutset "addr(...)" descriptor.
ADDRESS = re.compile(r"^(?:[a-z]{1,20}:)?[A-Za-z0-9]{20,90}$")

MAX_HEIGHT = 10 ** 9
RECENT_BLOCKS = 10
MAX_TXIDS = 1000
MAX_TX_IO = 200
MAX_UTXOS = 500
SCAN_TIMEOUT = 300  # seconds; scantxoutset walks the whole UTXO set


class ExplorerError(Exception):
    """A lookup that cannot be answered, with the HTTP status to report."""

    def __init__(self, status, message):
        super().__init__(message)
        self.status = status
        self.message = message


def _header_summary(header, found_hashes):
    return {
        "height": header.get("height"),
        "hash": header.get("hash"),
        "time": header.get("time"),
        "nTx": header.get("nTx"),
        "difficulty": header.get("difficulty"),
        "confirmations": header.get("confirmations"),
        "previousBlockHash": header.get("previousblockhash"),
        "nextBlockHash": header.get("nextblockhash"),
        "foundByPool": header.get("hash") in found_hashes,
    }


def _getblock(rpc, block_hash):
    # Daemons based on Bitcoin Core 0.14 (e.g. Dogecoin 1.14) take a boolean
    # "verbose" where later ones take an integer verbosity.
    return rpc("getblock", [block_hash, 1]) or rpc("getblock", [block_hash, True])


def _getrawtransaction(rpc, txid, block_hash=None):
    # The block-hash argument only exists from Bitcoin Core 0.16, and older
    # daemons want an integer "verbose".
    if block_hash:
        tx = rpc("getrawtransaction", [txid, True, block_hash])
        if tx:
            return tx
    return rpc("getrawtransaction", [txid, True]) or rpc("getrawtransaction", [txid, 1])


def _tx_summary(tx):
    vins = []
    for vin in (tx.get("vin") or [])[:MAX_TX_IO]:
        if "coinbase" in vin:
            vins.append({"coinbase": True})
        else:
            vins.append({"txid": vin.get("txid"), "vout": vin.get("vout")})

    vouts = []
    for vout in (tx.get("vout") or [])[:MAX_TX_IO]:
        spk = vout.get("scriptPubKey") or {}
        address = spk.get("address")
        if not address and spk.get("addresses"):
            address = spk["addresses"][0]
        vouts.append({
            "n": vout.get("n"),
            "value": vout.get("value"),
            "address": address,
            "type": spk.get("type"),
        })

    return {
        "txid": tx.get("txid"),
        "size": tx.get("size"),
        "vsize": tx.get("vsize"),
        "confirmations": tx.get("confirmations"),
        "blockHash": tx.get("blockhash"),
        "time": tx.get("time"),
        "vin": vins,
        "vout": vouts,
        "vinTruncated": len(tx.get("vin") or []) > MAX_TX_IO,
        "voutTruncated": len(tx.get("vout") or []) > MAX_TX_IO,
    }


def chain_summary(rpc, found_hashes=frozenset(), recent=RECENT_BLOCKS):
    """The node's chain state, mempool and most recent block headers."""
    info = rpc("getblockchaininfo")
    if not info:
        raise ExplorerError(503, "The node did not answer. It may be starting, syncing, or not configured.")

    tip = int(info.get("blocks", 0))
    pruned = bool(info.get("pruned", False))
    summary = {
        "chain": info.get("chain", ""),
        "blocks": tip,
        "headers": int(info.get("headers", 0)),
        "bestBlockHash": info.get("bestblockhash", ""),
        "pruned": pruned,
        "pruneHeight": int(info.get("pruneheight", 0)) if pruned else 0,
        "sizeOnDiskBytes": int(info.get("size_on_disk", 0)),
        "mempool": None,
        "recentBlocks": [],
    }

    mempool = rpc("getmempoolinfo")
    if mempool:
        summary["mempool"] = {"size": int(mempool.get("size", 0)), "bytes": int(mempool.get("bytes", 0))}

    for height in range(tip, max(tip - recent, -1), -1):
        block_hash = rpc("getblockhash", [height])
        header = rpc("getblockheader", [block_hash]) if block_hash else None
        if not header:
            break
        summary["recentBlocks"].append(_header_summary(header, found_hashes))

    return summary


def block_detail(rpc, block_id, found_hashes=frozenset()):
    """A block by height or hash: its header always, its body when the node still has it."""
    block_id = str(block_id or "").strip()
    if HEX64.match(block_id):
        block_hash = block_id.lower()
    elif block_id.isdigit() and len(block_id) <= 10 and int(block_id) <= MAX_HEIGHT:
        block_hash = rpc("getblockhash", [int(block_id)])
        if not block_hash:
            raise ExplorerError(404, f"No block at height {block_id} on this node's chain.")
    else:
        raise ExplorerError(400, "Enter a block height or a 64-character block hash.")

    header = rpc("getblockheader", [block_hash])
    if not header:
        raise ExplorerError(404, "This node does not know that block.")

    detail = _header_summary(header, found_hashes)
    detail.update({
        "version": header.get("version"),
        "bits": header.get("bits"),
        "nonce": header.get("nonce"),
        "merkleRoot": header.get("merkleroot"),
        "bodyAvailable": False,
        "bodyNote": "",
        "size": None,
        "weight": None,
        "txids": [],
        "txidsTruncated": False,
        "coinbase": None,
    })

    block = _getblock(rpc, block_hash)
    if not block:
        info = rpc("getblockchaininfo") or {}
        prune_height = int(info.get("pruneheight", 0)) if info.get("pruned") else 0
        if prune_height and (header.get("height") or 0) < prune_height:
            detail["bodyNote"] = (
                f"This block's data has been pruned. The node keeps blocks from height "
                f"{prune_height} onward, so only the header is available."
            )
        else:
            detail["bodyNote"] = "The node did not return this block's data."
        return detail

    txids = block.get("tx") or []
    detail["bodyAvailable"] = True
    detail["size"] = block.get("size")
    detail["weight"] = block.get("weight")
    detail["txids"] = txids[:MAX_TXIDS]
    detail["txidsTruncated"] = len(txids) > MAX_TXIDS
    if txids:
        coinbase = _getrawtransaction(rpc, txids[0], block_hash)
        if coinbase:
            detail["coinbase"] = _tx_summary(coinbase)
    return detail


def tx_detail(rpc, txid, block_hash=None):
    """A transaction from the mempool, or from a named block the node still has."""
    txid = str(txid or "").strip()
    if not HEX64.match(txid):
        raise ExplorerError(400, "Enter a 64-character transaction ID.")
    if block_hash:
        block_hash = str(block_hash).strip()
        if not HEX64.match(block_hash):
            raise ExplorerError(400, "The block hash must be 64 hexadecimal characters.")
        block_hash = block_hash.lower()

    tx = _getrawtransaction(rpc, txid.lower(), block_hash)
    if tx:
        return {"inMempool": not tx.get("blockhash"), "tx": _tx_summary(tx)}

    if not block_hash:
        raise ExplorerError(
            404,
            "Transaction not found. Without a transaction index (pruned nodes have none) the node "
            "can only find a transaction in its mempool or in a block you name, so add the block hash.",
        )
    raise ExplorerError(
        404,
        "Transaction not found in that block. If the block is older than the node's pruned "
        "window, its data is gone.",
    )


def address_utxos(rpc, address):
    """Unspent outputs paying an address, from a full UTXO-set scan."""
    address = str(address or "").strip()
    if not ADDRESS.match(address):
        raise ExplorerError(400, "That does not look like an address.")

    result = rpc("scantxoutset", ["start", [f"addr({address})"]], timeout=SCAN_TIMEOUT)
    if not result:
        raise ExplorerError(
            502,
            "The node could not scan for this address. The address may not belong to this coin, "
            "the daemon may not support scantxoutset, or another scan may be running.",
        )
    if result.get("success") is False:
        raise ExplorerError(502, "The node's UTXO scan did not complete.")

    unspents = result.get("unspents") or []
    return {
        "address": address,
        "height": result.get("height"),
        "totalAmount": result.get("total_amount"),
        "utxoCount": len(unspents),
        "utxos": [
            {"txid": u.get("txid"), "vout": u.get("vout"), "amount": u.get("amount"), "height": u.get("height")}
            for u in unspents[:MAX_UTXOS]
        ],
        "truncated": len(unspents) > MAX_UTXOS,
    }
