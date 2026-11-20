// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Spiral Dashboard pool topology page (v3.1.0). Draws /api/topology as an SVG:
//   miners (by coin)  →  this pool server  →  coin pools  →  coin nodes (failover order)
// with the HA cluster above the pool server when HA is enabled. Every value from
// the server is written with textContent, never parsed as markup.
(function () {
  "use strict";

  var SVG_NS = "http://www.w3.org/2000/svg";
  var REFRESH_MS = 30000;
  var MAX_MINERS_PER_GROUP = 6;

  // Column x positions and widths (viewBox units).
  var COL = { miners: 10, pool: 300, coins: 520, nodes: 790 };
  var W = { miners: 240, pool: 175, coins: 215, nodes: 225 };
  var VIEW_W = COL.nodes + W.nodes + 10;
  var ROW_GAP = 18;
  var NODE_H = 54, NODE_GAP = 8, MINER_ROW = 17;

  var svg = document.getElementById("topology-svg");
  var statusEl = document.getElementById("topology-status");
  var summaryEl = document.getElementById("topology-summary");
  var refreshBtn = document.getElementById("topology-refresh");

  function node(tag, attrs, text) {
    var n = document.createElementNS(SVG_NS, tag);
    Object.keys(attrs || {}).forEach(function (k) { n.setAttribute(k, attrs[k]); });
    if (text !== undefined && text !== null) n.textContent = String(text);
    return n;
  }

  function clip(s, n) {
    s = String(s == null ? "" : s);
    return s.length > n ? s.slice(0, n - 1) + "…" : s;
  }

  function fmtHash(ths) {
    var v = Number(ths) || 0;
    if (v >= 1000) return (v / 1000).toFixed(2) + " PH/s";
    if (v >= 1) return v.toFixed(2) + " TH/s";
    return (v * 1000).toFixed(1) + " GH/s";
  }

  function box(x, y, w, h, cls, title) {
    var r = node("rect", { x: x, y: y, width: w, height: h, rx: 6, ry: 6, "class": "topo-box " + cls });
    if (title) r.appendChild(node("title", {}, title));
    svg.appendChild(r);
    return r;
  }

  function text(x, y, s, cls) {
    svg.appendChild(node("text", { x: x, y: y, "class": cls || "topo-sub" }, s));
  }

  function dot(x, y, state) {
    svg.appendChild(node("circle", { cx: x, cy: y, r: 4.5, "class": "topo-dot " + state }));
  }

  // Smooth left-to-right connector between two points.
  function link(x1, y1, x2, y2, cls) {
    var mx = (x1 + x2) / 2;
    svg.appendChild(node("path", {
      d: "M" + x1 + "," + y1 + " C" + mx + "," + y1 + " " + mx + "," + y2 + " " + x2 + "," + y2,
      "class": "topo-link " + (cls || "")
    }));
  }

  function nodeState(n) {
    if (n.reachable === false) return "bad";
    var st = String(n.status || "").toLowerCase();
    if (st && /offline|error|down|unreach|stopped/.test(st)) return "bad";
    if (typeof n.sync_progress === "number" && n.sync_progress < 99.9 && n.sync_progress > 0) return "warn";
    if (/sync/.test(st) && !/synced/.test(st)) return "warn";
    if (n.reachable === true) return "ok";
    return "muted";
  }

  function render(data) {
    while (svg.firstChild) svg.removeChild(svg.firstChild);

    var coins = data.coins || [];
    var miners = data.miners || [];
    var ha = data.ha || { enabled: false };

    // Miner groups per coin, plus one for miners we could not place.
    var groups = {};
    coins.forEach(function (c) { groups[c.symbol] = []; });
    var unplaced = [];
    miners.forEach(function (m) {
      if (m.coin && groups[m.coin]) groups[m.coin].push(m); else unplaced.push(m);
    });

    var y = 12;

    // HA cluster across the top.
    var haBottom = null;
    if (ha.enabled) {
      var haNodes = ha.nodes || [];
      var haH = 46 + Math.max(1, Math.ceil(haNodes.length / 3)) * 20;
      var haX = COL.pool - 10, haW = VIEW_W - haX - 10;
      box(haX, y, haW, haH, "ha", "High-availability cluster");
      text(haX + 12, y + 18, "HA cluster" + (ha.vip ? "  ·  VIP " + ha.vip : "") +
        (ha.state ? "  ·  " + ha.state : ""), "topo-title");
      text(haX + 12, y + 34, "this node: " + (ha.local_role || "?") +
        (ha.failover_count ? "  ·  failovers: " + ha.failover_count : ""), "topo-small");
      haNodes.forEach(function (n, i) {
        var cx = haX + 16 + (i % 3) * (haW / 3), cy = y + 52 + Math.floor(i / 3) * 20;
        dot(cx, cy - 4, n.healthy ? "ok" : "bad");
        text(cx + 10, cy, clip((n.role || "?") + "  " + (n.host || n.id || ""), 34),
          n.id === ha.local_id ? "topo-title" : "topo-sub");
      });
      haBottom = y + haH;
      y += haH + 26;
    }

    // Lay out one row block per coin (and one for unplaced miners).
    var blocks = [];
    coins.forEach(function (c) {
      var g = groups[c.symbol] || [];
      var minerH = g.length ? 28 + Math.min(g.length, MAX_MINERS_PER_GROUP) * MINER_ROW + (g.length > MAX_MINERS_PER_GROUP ? MINER_ROW : 0) : 0;
      var nodesH = Math.max(1, c.nodes.length) * (NODE_H + NODE_GAP) - NODE_GAP;
      var coinH = 58 + (c.aux_chains.length ? 16 : 0);
      blocks.push({ coin: c, miners: g, y: 0, h: Math.max(minerH, nodesH, coinH), minerH: minerH, nodesH: nodesH, coinH: coinH });
    });
    if (unplaced.length) {
      var uh = 28 + Math.min(unplaced.length, MAX_MINERS_PER_GROUP) * MINER_ROW + (unplaced.length > MAX_MINERS_PER_GROUP ? MINER_ROW : 0);
      blocks.push({ coin: null, miners: unplaced, y: 0, h: uh, minerH: uh, nodesH: 0, coinH: 0 });
    }
    blocks.forEach(function (b) { b.y = y; y += b.h + ROW_GAP; });
    var contentTop = blocks.length ? blocks[0].y : y;
    var contentBottom = blocks.length ? y - ROW_GAP : y + 80;

    // Pool server, vertically centred on the coin rows.
    var poolH = 92;
    var poolY = Math.max(contentTop, (contentTop + contentBottom) / 2 - poolH / 2);
    var pool = data.pool || {};
    box(COL.pool, poolY, W.pool, poolH, "pool", "This Spiral Pool server");
    text(COL.pool + 12, poolY + 22, "Spiral Pool", "topo-title");
    text(COL.pool + 12, poolY + 40, clip(pool.hostname || "", 24), "topo-sub");
    text(COL.pool + 12, poolY + 56, "v" + (pool.version || "?"), "topo-small");
    dot(COL.pool + 16, poolY + 72, pool.stratum === "active" ? "ok" : "bad");
    text(COL.pool + 26, poolY + 76, "stratum " + (pool.stratum || "?"), "topo-small");
    var poolMidY = poolY + poolH / 2;
    if (haBottom !== null) link(COL.pool + W.pool / 2, haBottom, COL.pool + W.pool / 2, poolY, "ha");

    blocks.forEach(function (b) {
      // Miner group (left).
      if (b.miners.length) {
        var gy = b.y + (b.h - b.minerH) / 2;
        var online = b.miners.filter(function (m) { return m.online; }).length;
        var hr = b.miners.reduce(function (s, m) { return s + (Number(m.hashrate_ths) || 0); }, 0);
        box(COL.miners, gy, W.miners, b.minerH, "", "Miners");
        text(COL.miners + 10, gy + 18, (b.coin ? b.coin.symbol + " miners" : "Other miners") +
          "  " + online + "/" + b.miners.length + "  ·  " + fmtHash(hr), "topo-title");
        b.miners.slice(0, MAX_MINERS_PER_GROUP).forEach(function (m, i) {
          var ry = gy + 36 + i * MINER_ROW;
          dot(COL.miners + 14, ry - 4, m.online ? "ok" : "bad");
          text(COL.miners + 24, ry, clip(m.name, 20), "topo-sub");
          text(COL.miners + W.miners - 10 - 78, ry, m.online ? fmtHash(m.hashrate_ths) : "offline", "topo-small");
        });
        if (b.miners.length > MAX_MINERS_PER_GROUP) {
          text(COL.miners + 24, gy + 36 + MAX_MINERS_PER_GROUP * MINER_ROW, "+ " + (b.miners.length - MAX_MINERS_PER_GROUP) + " more", "topo-small");
        }
        link(COL.miners + W.miners, gy + b.minerH / 2, COL.pool, poolMidY);
      }
      if (!b.coin) return;

      // Coin pool (middle).
      var c = b.coin;
      var cy = b.y + (b.h - b.coinH) / 2;
      box(COL.coins, cy, W.coins, b.coinH, "coin", c.pool_id || c.symbol);
      text(COL.coins + 12, cy + 22, c.symbol, "topo-coin-symbol");
      text(COL.coins + 12, cy + 40, c.ports.length ? "stratum :" + c.ports.join(" :") : "no stratum port", "topo-sub");
      text(COL.coins + 12, cy + 54, clip(c.pool_id, 30), "topo-small");
      if (c.aux_chains.length) text(COL.coins + 12, cy + 70, "+ merge: " + c.aux_chains.join(", "), "topo-badge");
      link(COL.pool + W.pool, poolMidY, COL.coins, cy + b.coinH / 2);

      // Nodes (right), in failover order.
      var ny0 = b.y + (b.h - b.nodesH) / 2;
      if (!c.nodes.length) {
        text(COL.nodes, ny0 + 20, "no node configured", "topo-small");
        return;
      }
      c.nodes.forEach(function (n, i) {
        var ny = ny0 + i * (NODE_H + NODE_GAP);
        var state = nodeState(n);
        box(COL.nodes, ny, W.nodes, NODE_H, "node " + state, (i === 0 ? "Primary" : "Backup") + " node " + n.id);
        dot(COL.nodes + 14, ny + 15, state);
        var role = i === 0 ? "primary" : "backup " + i;
        text(COL.nodes + 24, ny + 19, clip(n.id && n.id !== role && n.id.replace(/-/g, " ") !== role ? role + " · " + n.id : role, 26), "topo-title");
        text(COL.nodes + 12, ny + 35, clip(n.host + ":" + (n.port || "?"), 30), "topo-sub");
        var detail = [];
        if (n.reachable === false) detail.push("unreachable");
        if (typeof n.blocks === "number") detail.push("height " + n.blocks);
        if (typeof n.sync_progress === "number" && n.sync_progress < 99.9) detail.push(n.sync_progress.toFixed(1) + "% synced");
        if (typeof n.connections === "number") detail.push(n.connections + " peers");
        if (detail.length) text(COL.nodes + 12, ny + 48, clip(detail.join(" · "), 34), "topo-small");
        link(COL.coins + W.coins, cy + b.coinH / 2, COL.nodes, ny + NODE_H / 2,
          (i === 0 ? "" : "backup") + (state === "bad" ? " bad" : ""));
      });
    });

    if (!coins.length && !miners.length) {
      text(COL.pool, poolY + poolH + 30, "No enabled coins found in config.yaml.", "topo-sub");
    }

    var height = Math.max(contentBottom, poolY + poolH) + 16;
    svg.setAttribute("viewBox", "0 0 " + VIEW_W + " " + height);

    // Summary line.
    var nodes = coins.reduce(function (s, c) { return s + c.nodes.length; }, 0);
    var down = coins.reduce(function (s, c) {
      return s + c.nodes.filter(function (n) { return n.reachable === false; }).length;
    }, 0);
    var online = miners.filter(function (m) { return m.online; }).length;
    summaryEl.textContent = coins.length + " coin" + (coins.length === 1 ? "" : "s") +
      " · " + nodes + " node" + (nodes === 1 ? "" : "s") + (down ? " (" + down + " unreachable)" : "") +
      " · " + online + "/" + miners.length + " miners online" +
      (ha.enabled ? " · HA " + (ha.local_role || "") : "");
  }

  function load(force) {
    refreshBtn.disabled = true;
    fetch("/api/topology" + (force ? "?refresh=1" : ""), { credentials: "same-origin", cache: "no-store" })
      .then(function (r) {
        if (r.status === 401 || r.status === 403) { window.location.href = "/login"; throw new Error("login"); }
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (data) {
        render(data);
        statusEl.className = "topology-status";
        statusEl.textContent = "Updated " + new Date().toLocaleTimeString();
      })
      .catch(function (e) {
        if (e.message === "login") return;
        statusEl.className = "topology-status error";
        statusEl.textContent = "Could not load the topology: " + e.message;
      })
      .then(function () { refreshBtn.disabled = false; });
  }

  refreshBtn.addEventListener("click", function () { load(true); });
  load(false);
  setInterval(function () { if (!document.hidden) load(false); }, REFRESH_MS);
})();
