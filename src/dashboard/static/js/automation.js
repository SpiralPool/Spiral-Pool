// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Spiral Dashboard miner automation page. Every value from the server is written
// with textContent, never parsed as HTML.
(function () {
  "use strict";

  var DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
  var LEVEL_NAMES = { low: "low power", normal: "normal", high: "high" };

  var statusEl = document.getElementById("automation-status");
  var errorsEl = document.getElementById("automation-errors");
  var tzEl = document.getElementById("automation-timezone");
  var rulesEl = document.getElementById("automation-rules");
  var editorEl = document.getElementById("automation-editor");
  var minersEl = document.getElementById("automation-miners");
  var restartEl = document.getElementById("automation-restart");
  var saveBtn = document.getElementById("automation-save");

  var data = null;     // last GET /api/automation response
  var settings = null; // working copy, sent by Save changes

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function input(type, value) {
    var node = el("input");
    node.type = type;
    if (value !== undefined && value !== null) node.value = String(value);
    return node;
  }

  function button(text, onClick, className) {
    var b = el("button", className || "automation-button", text);
    b.type = "button";
    b.addEventListener("click", onClick);
    return b;
  }

  function checkLabel(box, text) {
    var label = el("label");
    label.appendChild(box);
    label.appendChild(document.createTextNode(" " + text));
    return label;
  }

  function setStatus(text, isError) {
    statusEl.textContent = text || "";
    statusEl.className = isError ? "error" : "";
  }

  function showErrors(list) {
    errorsEl.textContent = "";
    (list || []).forEach(function (message) { errorsEl.appendChild(el("li", null, message)); });
    errorsEl.hidden = !list || list.length === 0;
  }

  function markDirty(message) {
    saveBtn.disabled = false;
    setStatus(message || "Unsaved changes.", false);
  }

  function api(method, path, body) {
    var options = { method: method, credentials: "same-origin", headers: { "Accept": "application/json" } };
    if (body !== undefined) {
      options.headers["Content-Type"] = "application/json";
      options.body = JSON.stringify(body);
    }
    return fetch(path, options).then(function (res) {
      if (res.status === 401) {
        window.location.href = "/login?next=/automation";
        throw new Error("Login required.");
      }
      return res.json().catch(function () { return {}; }).then(function (payload) {
        if (!res.ok) {
          var err = new Error(payload.error || (payload.errors ? "Not saved. Fix the problems listed below." : "Request failed (HTTP " + res.status + ")."));
          err.errors = payload.errors;
          throw err;
        }
        return payload;
      });
    });
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

  function minerByIp(ip) {
    for (var i = 0; i < data.miners.length; i++) {
      if (data.miners[i].ip === ip) return data.miners[i];
    }
    return null;
  }

  function deviceSettings(ip) {
    if (!settings.devices[ip]) settings.devices[ip] = { model: "", auto_restart: true };
    return settings.devices[ip];
  }

  function capabilityText(m) {
    var parts = [];
    if (m.sleep) parts.push("sleep");
    if (m.levels.length) parts.push("power modes: " + m.levels.map(function (l) { return LEVEL_NAMES[l]; }).join(", "));
    if (m.watts) parts.push("power target in watts");
    if (m.family === "avalon" && !m.model) parts.push("set the model for power modes");
    return parts.length ? parts.join("; ") : "auto-restart only";
  }

  function newRuleId() {
    return "r" + Date.now().toString(36) + Math.floor(Math.random() * 1e6).toString(36);
  }

  // ── schedules ──────────────────────────────────────────────────────────────

  function describeAction(rule) {
    if (rule.action === "sleep") return "Sleep";
    if (rule.watts !== undefined) return "Power target " + rule.watts + " W";
    return "Power mode: " + LEVEL_NAMES[rule.level];
  }

  function describeDays(days) {
    if (days.length === 7) return "Every day";
    return days.map(function (d) { return DAYS[d]; }).join(", ");
  }

  function renderRules() {
    rulesEl.textContent = "";
    if (!settings.rules.length) {
      rulesEl.appendChild(el("p", "automation-muted", "No schedules yet."));
    } else {
      rulesEl.appendChild(table(["On", "Name", "Action", "When", "Miners", ""], settings.rules.map(function (rule) {
        var on = input("checkbox");
        on.checked = rule.enabled;
        on.addEventListener("change", function () { rule.enabled = on.checked; markDirty(); });
        var miners = rule.devices.map(function (ip) {
          var m = minerByIp(ip);
          return m ? m.name : ip;
        }).join(", ");
        var actions = el("div", "automation-actions");
        actions.appendChild(button("Edit", function () { openEditor(rule); }, "automation-link"));
        actions.appendChild(button("Delete", function () {
          settings.rules = settings.rules.filter(function (r) { return r !== rule; });
          markDirty();
          renderRules();
        }, "automation-link"));
        return [on, rule.name || "(unnamed)", describeAction(rule),
                describeDays(rule.days) + ", " + rule.start + " to " + rule.end, miners, actions];
      })));
    }
    rulesEl.appendChild(button("Add schedule", function () { openEditor(null); }));
  }

  function closeEditor() {
    editorEl.textContent = "";
    editorEl.hidden = true;
  }

  function openEditor(rule) {
    var draft = rule || { name: "", enabled: true, devices: [], days: [0, 1, 2, 3, 4, 5, 6],
                          start: "22:00", end: "06:00", action: "sleep" };
    editorEl.textContent = "";
    editorEl.hidden = false;
    editorEl.appendChild(el("h2", null, rule ? "Edit schedule" : "New schedule"));

    var form = el("div", "automation-form");
    function field(label, control, wide) {
      var row = el("label", wide ? "automation-field wide" : "automation-field");
      row.appendChild(el("span", null, label));
      row.appendChild(control);
      form.appendChild(row);
      return row;
    }

    var name = input("text", draft.name);
    name.maxLength = 64;
    field("Name", name);
    var start = input("time", draft.start);
    field("Start", start);
    var end = input("time", draft.end);
    field("End", end);

    var action = el("select");
    action.appendChild(new Option("Sleep", "sleep"));
    action.appendChild(new Option("Power mode or target", "power"));
    action.value = draft.action;
    field("Action", action);

    var power = el("select");
    ["low", "normal", "high"].forEach(function (level) {
      power.appendChild(new Option("Power mode: " + LEVEL_NAMES[level], level));
    });
    power.appendChild(new Option("Power target in watts", "watts"));
    power.value = draft.watts !== undefined ? "watts" : (draft.level || "low");
    var powerRow = field("Power", power);
    var watts = input("number", draft.watts !== undefined ? draft.watts : "");
    watts.min = "100";
    watts.max = "20000";
    watts.step = "1";
    var wattsRow = field("Watts", watts);

    var days = el("div", "automation-checks");
    var dayBoxes = DAYS.map(function (day, index) {
      var box = input("checkbox");
      box.checked = draft.days.indexOf(index) >= 0;
      days.appendChild(checkLabel(box, day));
      return box;
    });
    field("Days", days, true);

    var miners = el("div", "automation-checks column");
    var minerBoxes = data.miners.map(function (m) {
      var box = input("checkbox", m.ip);
      box.checked = draft.devices.indexOf(m.ip) >= 0;
      miners.appendChild(checkLabel(box, m.name + " (" + m.label + ": " + capabilityText(m) + ")"));
      return box;
    });
    if (!data.miners.length) miners.appendChild(el("span", "automation-muted", "No miners configured."));
    field("Miners", miners, true);

    function syncVisibility() {
      powerRow.hidden = action.value !== "power";
      wattsRow.hidden = action.value !== "power" || power.value !== "watts";
    }
    action.addEventListener("change", syncVisibility);
    power.addEventListener("change", syncVisibility);
    syncVisibility();

    editorEl.appendChild(form);
    editorEl.appendChild(el("p", "automation-muted",
      "A window whose end is earlier than its start runs past midnight and belongs to the day it starts."));

    var buttons = el("div", "automation-actions");
    buttons.appendChild(button(rule ? "Update schedule" : "Add schedule", function () {
      var out = {
        id: rule ? rule.id : newRuleId(),
        name: name.value.trim(),
        enabled: draft.enabled,
        devices: minerBoxes.filter(function (b) { return b.checked; }).map(function (b) { return b.value; }),
        days: dayBoxes.map(function (b, i) { return b.checked ? i : -1; }).filter(function (i) { return i >= 0; }),
        start: start.value,
        end: end.value,
        action: action.value
      };
      if (out.action === "power") {
        if (power.value === "watts") out.watts = parseInt(watts.value, 10);
        else out.level = power.value;
      }
      if (rule) settings.rules[settings.rules.indexOf(rule)] = out;
      else settings.rules.push(out);
      closeEditor();
      markDirty("Unsaved changes. Save to send them to Sentinel.");
      renderRules();
    }));
    buttons.appendChild(button("Cancel", closeEditor, "automation-link"));
    editorEl.appendChild(buttons);
  }

  // ── miners ─────────────────────────────────────────────────────────────────

  function credentialsCell(m) {
    var box = el("div");
    if (!m.credentials) {
      box.appendChild(el("span", "automation-muted", "not needed"));
      return box;
    }
    box.appendChild(el("span", m.has_credentials ? "automation-ok" : "automation-muted",
                       m.has_credentials ? "saved " : "firmware default "));

    var form = el("div", "automation-credential-form");
    form.hidden = true;
    var user = input("text");
    user.placeholder = "username";
    user.maxLength = 128;
    user.autocomplete = "off";
    var pass = input("password");
    pass.placeholder = "password";
    pass.maxLength = 128;
    pass.autocomplete = "new-password";
    form.appendChild(user);
    form.appendChild(pass);

    function done(saved, message) {
      m.has_credentials = saved;
      renderMiners();
      setStatus(message, false);
    }
    form.appendChild(button("Save", function () {
      api("PUT", "/api/automation/credentials/" + encodeURIComponent(m.ip), { username: user.value, password: pass.value })
        .then(function () { done(true, "Credentials saved for " + m.name + "."); })
        .catch(function (err) { setStatus(err.message, true); });
    }, "automation-link"));

    box.appendChild(button(m.has_credentials ? "Change" : "Set", function () { form.hidden = !form.hidden; }, "automation-link"));
    if (m.has_credentials) {
      box.appendChild(button("Clear", function () {
        api("DELETE", "/api/automation/credentials/" + encodeURIComponent(m.ip))
          .then(function () { done(false, "Credentials cleared for " + m.name + "."); })
          .catch(function (err) { setStatus(err.message, true); });
      }, "automation-link"));
    }
    box.appendChild(form);
    return box;
  }

  function renderMiners() {
    minersEl.textContent = "";
    if (!data.miners.length) {
      minersEl.appendChild(el("p", "automation-muted", "No miners are configured on the dashboard yet."));
      return;
    }
    minersEl.appendChild(table(["Miner", "Type", "Automation can", "Avalon model", "Auto-restart", "Credentials"],
      data.miners.map(function (m) {
        var ds = deviceSettings(m.ip);
        var name = el("div");
        name.appendChild(el("div", null, m.name));
        name.appendChild(el("div", "automation-muted", m.ip));

        var model = "";
        if (m.family === "avalon") {
          model = el("select");
          model.appendChild(new Option("Other Avalon", ""));
          Object.keys(data.avalon_models).forEach(function (key) {
            model.appendChild(new Option(data.avalon_models[key], key));
          });
          model.value = ds.model || "";
          model.addEventListener("change", function () {
            ds.model = model.value;
            markDirty("Save to apply the Avalon model.");
          });
        }

        var autoRestart = input("checkbox");
        autoRestart.checked = ds.auto_restart !== false;
        autoRestart.addEventListener("change", function () {
          ds.auto_restart = autoRestart.checked;
          markDirty();
        });
        return [name, m.label, capabilityText(m), model, autoRestart, credentialsCell(m)];
      })));
  }

  // ── auto-restart ───────────────────────────────────────────────────────────

  function renderRestart() {
    restartEl.textContent = "";
    var values = settings.auto_restart || data.auto_restart_defaults;

    var useDefaults = input("checkbox");
    useDefaults.checked = !settings.auto_restart;
    restartEl.appendChild(checkLabel(useDefaults, "Use Sentinel's configured settings"));

    var form = el("div", "automation-form");
    function numberField(label, value, min) {
      var box = input("number", value);
      box.min = String(min);
      box.max = "1440";
      box.step = "1";
      var row = el("label", "automation-field");
      row.appendChild(el("span", null, label));
      row.appendChild(box);
      form.appendChild(row);
      return box;
    }
    var enabled = input("checkbox");
    enabled.checked = values.enabled;
    var enabledRow = el("label", "automation-field");
    enabledRow.appendChild(el("span", null, "Auto-restart"));
    enabledRow.appendChild(checkLabel(enabled, "enabled"));
    form.appendChild(enabledRow);
    var offline = numberField("Offline for (minutes)", values.offline_minutes, 1);
    var cooldown = numberField("Between attempts (minutes)", values.cooldown_minutes, 5);
    var low = numberField("Below expected hashrate for (minutes, 0 = never)", values.low_hashrate_minutes, 0);
    restartEl.appendChild(form);

    var controls = [enabled, offline, cooldown, low];
    function apply(changed) {
      controls.forEach(function (c) { c.disabled = useDefaults.checked; });
      if (!changed) return;
      if (useDefaults.checked) {
        delete settings.auto_restart;
      } else {
        settings.auto_restart = {
          enabled: enabled.checked,
          offline_minutes: parseInt(offline.value, 10),
          cooldown_minutes: parseInt(cooldown.value, 10),
          low_hashrate_minutes: parseInt(low.value, 10)
        };
      }
      markDirty();
    }
    useDefaults.addEventListener("change", function () { apply(true); });
    controls.forEach(function (c) { c.addEventListener("change", function () { apply(true); }); });
    apply(false);
  }

  // ── load and save ──────────────────────────────────────────────────────────

  function load() {
    return api("GET", "/api/automation").then(function (payload) {
      data = payload;
      settings = JSON.parse(JSON.stringify(payload.settings));
      tzEl.textContent = payload.timezone;
      closeEditor();
      renderRules();
      renderMiners();
      renderRestart();
      saveBtn.disabled = true;
    });
  }

  saveBtn.addEventListener("click", function () {
    var body = { version: 1, rules: settings.rules, devices: settings.devices };
    if (settings.auto_restart) body.auto_restart = settings.auto_restart;
    showErrors([]);
    setStatus("Saving…", false);
    api("PUT", "/api/automation", body)
      .then(load)
      .then(function () { setStatus("Saved. Sentinel applies the changes on its next check.", false); })
      .catch(function (err) {
        setStatus(err.message, true);
        showErrors(err.errors);
      });
  });

  load().then(function () { setStatus("", false); }).catch(function (err) { setStatus(err.message, true); });
})();
