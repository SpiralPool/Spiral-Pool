// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Spiral Dashboard block explorer. All node data is written with textContent,
// never parsed as HTML.
(function () {
  "use strict";

  var coinSelect = document.getElementById("explorer-coin");
  var form = document.getElementById("explorer-search");
  var queryInput = document.getElementById("explorer-query");
  var statusEl = document.getElementById("explorer-status");
  var summaryEl = document.getElementById("explorer-summary");
  var resultEl = document.getElementById("explorer-result");

  var HEX64 = /^[0-9a-fA-F]{64}$/;

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function setStatus(text, isError) {
    statusEl.textContent = text || "";
    statusEl.className = isError ? "error" : "";
  }

  function when(unix) {
    if (!unix) return "";
    return new Date(unix * 1000).toLocaleString();
  }

  function fact(label, value) {
    var box = el("div", "explorer-fact");
    box.appendChild(el("span", "label", label));
    if (value instanceof Node) {
      var wrap = el("span", "value");
      wrap.appendChild(value);
      box.appendChild(wrap);
    } else {
      box.appendChild(el("span", "value", value === undefined || value === null ? "" : value));
    }
    return box;
  }

  function link(text, onClick) {
    var b = el("button", "explorer-link", text);
    b.type = "button";
    b.addEventListener("click", onClick);
    return b;
  }

  function table(headers, rows) {
    var wrap = el("div", "explorer-table-wrap");
    var t = el("table", "explorer-table");
    var head = el("tr");
    headers.forEach(function (h) { head.appendChild(el("th", null, h)); });
    t.appendChild(head);
    rows.forEach(function (cells) {
      var tr = el("tr");
      cells.forEach(function (c) {
        var td = el("td");
        if (c instanceof Node) td.appendChild(c); else td.textContent = c === undefined || c === null ? "" : String(c);
        tr.appendChild(td);
      });
      t.appendChild(tr);
    });
    wrap.appendChild(t);
    return wrap;
  }

  function api(path) {
    return fetch("/api/explorer/" + encodeURIComponent(coinSelect.value) + path, {
      headers: { "Accept": "application/json" },
      credentials: "same-origin"
    }).then(function (res) {
      if (res.status === 401) {
        window.location.href = "/login?next=/explorer";
        throw new Error("Login required.");
      }
      return res.json().catch(function () { return {}; }).then(function (body) {
        if (!res.ok) {
          var err = new Error(body.error || ("Request failed (HTTP " + res.status + ")."));
          err.status = res.status;
          throw err;
        }
        return body;
      });
    });
  }

  function card(title) {
    var c = el("article", "explorer-card");
    c.appendChild(el("h2", null, title));
    return c;
  }

  function renderSummary(s) {
    summaryEl.textContent = "";
    var c = card(coinSelect.value + " node");
    var facts = el("div", "explorer-facts");
    facts.appendChild(fact("Chain", s.chain));
    facts.appendChild(fact("Blocks / headers", s.blocks + " / " + s.headers));
    facts.appendChild(fact("Pruned", s.pruned ? "yes, block data from height " + s.pruneHeight : "no"));
    facts.appendChild(fact("Size on disk", (s.sizeOnDiskBytes / 1073741824).toFixed(2) + " GB"));
    if (s.mempool) facts.appendChild(fact("Mempool", s.mempool.size + " tx, " + (s.mempool.bytes / 1048576).toFixed(2) + " MB"));
    c.appendChild(facts);

    c.appendChild(el("h3", null, "Recent blocks"));
    c.appendChild(table(["Height", "Time", "Tx", "Found by this pool", "Hash"], s.recentBlocks.map(function (b) {
      return [
        link(b.height, function () { showBlock(String(b.height)); }),
        when(b.time),
        b.nTx,
        b.foundByPool ? el("span", "explorer-found", "yes") : "",
        b.hash
      ];
    })));
    summaryEl.appendChild(c);
  }

  function renderBlock(b) {
    resultEl.textContent = "";
    var c = card("Block " + b.height);
    var facts = el("div", "explorer-facts");
    facts.appendChild(fact("Hash", b.hash));
    facts.appendChild(fact("Time", when(b.time)));
    facts.appendChild(fact("Confirmations", b.confirmations));
    facts.appendChild(fact("Transactions", b.nTx));
    facts.appendChild(fact("Found by this pool", b.foundByPool ? "yes" : "no"));
    facts.appendChild(fact("Difficulty", b.difficulty));
    facts.appendChild(fact("Merkle root", b.merkleRoot));
    if (b.previousBlockHash) facts.appendChild(fact("Previous", link(b.previousBlockHash, function () { showBlock(b.previousBlockHash); })));
    if (b.nextBlockHash) facts.appendChild(fact("Next", link(b.nextBlockHash, function () { showBlock(b.nextBlockHash); })));
    c.appendChild(facts);

    if (!b.bodyAvailable) {
      c.appendChild(el("p", "explorer-note", b.bodyNote));
      resultEl.appendChild(c);
      return;
    }

    if (b.coinbase) {
      c.appendChild(el("h3", null, "Coinbase outputs"));
      c.appendChild(table(["#", "Value", "Address", "Type"], b.coinbase.vout.map(function (o) {
        return [o.n, o.value, o.address || "", o.type || ""];
      })));
    }

    c.appendChild(el("h3", null, "Transactions" + (b.txidsTruncated ? " (first " + b.txids.length + ")" : "")));
    c.appendChild(table(["#", "Transaction ID"], b.txids.map(function (txid, i) {
      return [i, link(txid, function () { showTx(txid, b.hash); })];
    })));
    resultEl.appendChild(c);
  }

  function renderTx(t) {
    resultEl.textContent = "";
    var tx = t.tx;
    var c = card("Transaction");
    var facts = el("div", "explorer-facts");
    facts.appendChild(fact("Transaction ID", tx.txid));
    facts.appendChild(fact("Status", t.inMempool ? "in mempool" : (tx.confirmations || 0) + " confirmations"));
    if (tx.blockHash) facts.appendChild(fact("Block", link(tx.blockHash, function () { showBlock(tx.blockHash); })));
    facts.appendChild(fact("Size", tx.vsize ? tx.vsize + " vB" : tx.size + " B"));
    c.appendChild(facts);

    c.appendChild(el("h3", null, "Inputs" + (tx.vinTruncated ? " (truncated)" : "")));
    c.appendChild(table(["Spends"], tx.vin.map(function (i) {
      return [i.coinbase ? "coinbase" : link(i.txid + ":" + i.vout, function () { showTx(i.txid, null); })];
    })));
    c.appendChild(el("h3", null, "Outputs" + (tx.voutTruncated ? " (truncated)" : "")));
    c.appendChild(table(["#", "Value", "Address", "Type"], tx.vout.map(function (o) {
      return [o.n, o.value, o.address || "", o.type || ""];
    })));
    resultEl.appendChild(c);
  }

  function renderAddress(a) {
    resultEl.textContent = "";
    var c = card("Address");
    var facts = el("div", "explorer-facts");
    facts.appendChild(fact("Address", a.address));
    facts.appendChild(fact("Balance (unspent)", a.totalAmount));
    facts.appendChild(fact("Unspent outputs", a.utxoCount));
    facts.appendChild(fact("Scanned at height", a.height));
    c.appendChild(facts);
    c.appendChild(el("h3", null, "Unspent outputs" + (a.truncated ? " (first " + a.utxos.length + ")" : "")));
    c.appendChild(table(["Amount", "Height", "Output"], a.utxos.map(function (u) {
      return [u.amount, u.height, u.txid + ":" + u.vout];
    })));
    resultEl.appendChild(c);
  }

  function run(label, promise, render) {
    setStatus(label, false);
    return promise.then(function (data) { setStatus("", false); render(data); })
      .catch(function (err) { setStatus(err.message, true); throw err; });
  }

  function showBlock(id) {
    return run("Loading block…", api("/block/" + encodeURIComponent(id)), renderBlock);
  }

  function showTx(txid, blockHash) {
    var q = blockHash ? "?block=" + encodeURIComponent(blockHash) : "";
    return run("Loading transaction…", api("/tx/" + encodeURIComponent(txid) + q), renderTx);
  }

  function showAddress(address) {
    return run("Scanning the UTXO set for this address. This can take a few minutes…",
      api("/address/" + encodeURIComponent(address)), renderAddress);
  }

  function loadSummary() {
    summaryEl.textContent = "";
    resultEl.textContent = "";
    return run("Loading node…", api("/summary"), renderSummary).catch(function () {});
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var q = queryInput.value.trim();
    if (!q) return;
    if (/^\d+$/.test(q)) {
      showBlock(q).catch(function () {});
    } else if (HEX64.test(q)) {
      // A 64-character ID is ambiguous: try it as a block, then as a transaction.
      showBlock(q).catch(function (err) {
        if (err.status === 404) showTx(q, null).catch(function () {});
      });
    } else {
      showAddress(q).catch(function () {});
    }
  });

  coinSelect.addEventListener("change", loadSummary);

  fetch("/api/explorer/coins", { credentials: "same-origin", headers: { "Accept": "application/json" } })
    .then(function (res) {
      if (res.status === 401) { window.location.href = "/login?next=/explorer"; throw new Error("Login required."); }
      return res.json();
    })
    .then(function (data) {
      (data.coins || []).forEach(function (c) {
        var opt = el("option", null, c.symbol + " — " + c.name);
        opt.value = c.symbol;
        coinSelect.appendChild(opt);
      });
      if (coinSelect.options.length === 0) {
        setStatus("No coins are enabled on this pool.", true);
        return;
      }
      loadSummary();
    })
    .catch(function (err) { setStatus(err.message, true); });
})();
