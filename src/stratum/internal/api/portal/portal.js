// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Spiral Pool miner portal. Every value from the API is written with
// textContent, never parsed as HTML.
(function () {
  "use strict";

  var form = document.getElementById("lookup");
  var input = document.getElementById("address");
  var button = form.querySelector("button");
  var statusEl = document.getElementById("status");
  var results = document.getElementById("results");

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function setStatus(text, isError) {
    statusEl.textContent = text;
    statusEl.className = isError ? "error" : "";
  }

  function when(iso) {
    if (!iso) return "never";
    var d = new Date(iso);
    if (isNaN(d.getTime()) || d.getFullYear() < 2000) return "never";
    return d.toLocaleString();
  }

  function stat(label, value) {
    var box = el("div", "stat");
    box.appendChild(el("span", "label", label));
    box.appendChild(el("span", "value", value));
    return box;
  }

  function table(headers, rows) {
    var wrap = el("div", "table-wrap");
    var t = el("table");
    var head = el("tr");
    headers.forEach(function (h) { head.appendChild(el("th", null, h)); });
    t.appendChild(head);
    rows.forEach(function (cells) {
      var tr = el("tr");
      cells.forEach(function (c) {
        var td = el("td", c.className || null, c.text);
        tr.appendChild(td);
      });
      t.appendChild(tr);
    });
    wrap.appendChild(t);
    return wrap;
  }

  function renderCoin(c) {
    var box = el("article", "coin");

    var head = el("div", "coin-head");
    head.appendChild(el("h2", null, c.coin));
    head.appendChild(el("span", "offline", c.algorithm));
    box.appendChild(head);

    var stats = el("div", "stats");
    stats.appendChild(stat("Hashrate (24h)", c.hashrate24hFormatted || "0"));
    stats.appendChild(stat("Shares (24h)", c.shares24h));
    stats.appendChild(stat("Last share", when(c.lastShare)));
    stats.appendChild(stat("Blocks found", c.blocks.length));
    box.appendChild(stats);

    box.appendChild(el("h3", null, "Workers (last 15 minutes)"));
    if (c.workers.length === 0) {
      box.appendChild(el("p", "empty", "No shares in the last 15 minutes."));
    } else {
      box.appendChild(table(["Worker", "Hashrate", "Last share", "Status"], c.workers.map(function (w) {
        return [
          { text: w.worker },
          { text: w.hashrateFormatted },
          { text: when(w.lastShare) },
          { text: w.connected ? "online" : "idle", className: w.connected ? "online" : "offline" }
        ];
      })));
    }

    box.appendChild(el("h3", null, "Blocks"));
    if (c.blocks.length === 0) {
      box.appendChild(el("p", "empty", "No blocks found by this address."));
    } else {
      box.appendChild(table(["Height", "Status", "Reward", "Worker", "Found", "Hash"], c.blocks.map(function (b) {
        var confirm = b.status === "pending" ? " (" + Math.round((b.confirmationProgress || 0) * 100) + "%)" : "";
        return [
          { text: b.height },
          { text: b.status + confirm, className: "status-" + b.status },
          { text: b.reward },
          { text: b.worker || "" },
          { text: when(b.created) },
          { text: b.hash, className: "mono" }
        ];
      })));
    }

    return box;
  }

  function lookup(address) {
    results.textContent = "";
    setStatus("Looking up…", false);
    button.disabled = true;

    fetch("/api/portal/" + encodeURIComponent(address), { headers: { "Accept": "application/json" } })
      .then(function (res) {
        if (res.status === 400) throw new Error("That does not look like a wallet address.");
        if (res.status === 429) throw new Error("Too many lookups. Wait a moment and try again.");
        if (!res.ok) throw new Error("The pool could not answer right now (HTTP " + res.status + ").");
        return res.json();
      })
      .then(function (data) {
        if (!data.coins || data.coins.length === 0) {
          setStatus("No recent shares or blocks for this address.", false);
          return;
        }
        setStatus("Updated " + when(data.generatedAt) + ".", false);
        data.coins.forEach(function (c) { results.appendChild(renderCoin(c)); });
      })
      .catch(function (err) {
        setStatus(err.message || "Lookup failed.", true);
      })
      .then(function () {
        button.disabled = false;
      });
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var address = input.value.trim();
    if (!address) return;
    history.replaceState(null, "", "#" + encodeURIComponent(address));
    lookup(address);
  });

  if (location.hash.length > 1) {
    try {
      input.value = decodeURIComponent(location.hash.slice(1));
      lookup(input.value.trim());
    } catch (e) {
      // Ignore a malformed fragment.
    }
  }
})();
