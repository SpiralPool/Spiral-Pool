# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Miner automation: scheduled sleep windows and power modes, per-device settings
and auto-restart overrides.

Sentinel runs the rules and the dashboard edits them, so this module holds only
the shared logic: the rule format, validation, what each miner family supports,
and the runner that decides when to act. Talking to miners is miner_control.py's
job.

automation.json, in the shared data directory:

    {
      "version": 1,
      "rules": [{"id", "name", "enabled", "devices": [ip, ...],
                 "days": [0-6, Monday = 0], "start": "HH:MM", "end": "HH:MM",
                 "action": "sleep" | "power",
                 "level": "low" | "normal" | "high",   # power, on miners with modes
                 "watts": int}],                       # power, on miners with a power target
      "devices": {ip: {"model": "nano3s" | "avalon_q" | "mini3" | "", "auto_restart": bool}},
      "auto_restart": {"enabled": bool, "offline_minutes": int,
                       "cooldown_minutes": int, "low_hashrate_minutes": int}
    }

Times are in Sentinel's display timezone. A window whose end is earlier than its
start runs past midnight and belongs to the day it starts.
"""
import copy
import ipaddress
import json
import os
import re
import tempfile

AUTOMATION_FILE = "automation.json"
CREDENTIALS_FILE = "device_credentials.json"

LEVELS = ("low", "normal", "high")
MAX_RULES = 200
MAX_RULE_DEVICES = 500
MIN_WATTS, MAX_WATTS = 100, 20000
RETRY_SECONDS = 300

# Avalon sleep and work modes are confirmed only for these home models.
AVALON_HOME_MODELS = {"nano3s": "Avalon Nano 3S", "avalon_q": "Avalon Q", "mini3": "Avalon Mini 3"}

# Sentinel and dashboard miner types, by control family.
FAMILY_BY_TYPE = {
    "axeos": "axeos", "bitaxe": "axeos", "nmaxe": "axeos", "nerdaxe": "axeos",
    "qaxe": "axeos", "qaxeplus": "axeos", "hammer": "axeos",
    "luckyminer": "axeos", "jingleminer": "axeos", "zyber": "axeos",
    "nerdqaxe": "nerdqaxe", "nerdoctaxe": "nerdqaxe",
    "avalon": "avalon", "canaan": "avalon",
    "antminer": "antminer", "antminer_scrypt": "antminer",
    "braiins": "braiins", "luxos": "luxos", "vnish": "vnish",
    "whatsminer": "whatsminer", "elphapex": "elphapex", "goldshell": "goldshell",
}

# "credentials": the firmware's API needs a login, which the operator can store.
_FAMILIES = {
    "axeos": {"label": "Bitaxe / AxeOS", "sleep": True, "levels": (), "watts": False, "credentials": False},
    "nerdqaxe": {"label": "NerdQAxe", "sleep": False, "levels": ("low", "normal"), "watts": False, "credentials": False},
    # No sleep: Canaan's home-miner firmware has no standby command. A Nano 3S
    # (MM319, 25061101) refused softoff/softon and lists none in "ascset 0,help".
    "avalon": {"label": "Avalon", "sleep": False, "levels": LEVELS, "watts": False, "credentials": False},
    "antminer": {"label": "Antminer (stock firmware)", "sleep": True, "levels": ("low", "normal"), "watts": False, "credentials": True},
    "braiins": {"label": "Braiins OS", "sleep": True, "levels": (), "watts": True, "credentials": True},
    "luxos": {"label": "LuxOS", "sleep": True, "levels": (), "watts": True, "credentials": False},
    "vnish": {"label": "Vnish", "sleep": True, "levels": (), "watts": True, "credentials": True},
    "whatsminer": {"label": "Whatsminer", "sleep": True, "levels": LEVELS, "watts": False, "credentials": True},
    "elphapex": {"label": "Elphapex", "sleep": False, "levels": (), "watts": False, "credentials": True},
    "goldshell": {"label": "Goldshell", "sleep": False, "levels": (), "watts": False, "credentials": False},
}

_RULE_ID = re.compile(r"^[A-Za-z0-9_-]{1,40}$")
_HHMM = re.compile(r"^([01]?[0-9]|2[0-3]):([0-5][0-9])$")
_IPV4 = re.compile(r"^\d{1,3}(\.\d{1,3}){3}$")
_PROFILE_LEVEL = {"efficiency": "low", "balanced": "normal", "high": "high"}


def family_for(miner_type):
    return FAMILY_BY_TYPE.get(str(miner_type or "").lower())


def capabilities_for(miner_type, model=""):
    """What automation can do with a miner: {family, label, sleep, levels, watts}.

    Avalons get work modes only once the operator names a home model.
    """
    family = family_for(miner_type)
    if family is None:
        return {"family": None, "label": str(miner_type or "unknown"), "sleep": False, "levels": (), "watts": False,
                "credentials": False}
    caps = dict(_FAMILIES[family], family=family)
    if family == "avalon" and model not in AVALON_HOME_MODELS:
        caps.update(levels=())
    return caps


def rule_supported(rule, caps):
    if rule["action"] == "sleep":
        return caps["sleep"]
    if "watts" in rule:
        return caps["watts"]
    return rule.get("level") in caps["levels"]


def valid_device_ip(ip):
    """Private LAN IPv4 only, the same range Sentinel will connect to."""
    if not isinstance(ip, str) or not _IPV4.match(ip):
        return False
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        return False
    return addr.is_private and not (addr.is_loopback or addr.is_link_local or addr.is_multicast or addr.is_reserved)


def default_automation():
    return {"version": 1, "rules": [], "devices": {}}


def _normalize_hhmm(value):
    m = _HHMM.match(value) if isinstance(value, str) else None
    return f"{int(m.group(1)):02d}:{m.group(2)}" if m else None


def _int_in(value, low, high):
    return type(value) is int and low <= value <= high


def validate_automation(data, device_types=None):
    """Return (clean settings, list of error strings).

    device_types maps IP to miner type. When it is given, a rule that asks a miner
    for something it cannot do is an error.
    """
    errors = []
    if not isinstance(data, dict):
        return default_automation(), ["Automation settings must be a JSON object."]
    clean = default_automation()

    devices = data.get("devices", {})
    if not isinstance(devices, dict):
        errors.append("devices must be an object keyed by IP.")
        devices = {}
    for ip, settings in devices.items():
        if not valid_device_ip(ip) or not isinstance(settings, dict):
            errors.append(f"Device {ip!r}: not a private LAN IPv4 address with settings.")
            continue
        model = settings.get("model", "")
        if model not in AVALON_HOME_MODELS and model != "":
            errors.append(f"Device {ip}: unknown Avalon model {model!r}.")
            continue
        auto_restart = settings.get("auto_restart", True)
        if not isinstance(auto_restart, bool):
            errors.append(f"Device {ip}: auto_restart must be true or false.")
            continue
        clean["devices"][ip] = {"model": model, "auto_restart": auto_restart}

    rules = data.get("rules", [])
    if not isinstance(rules, list):
        errors.append("rules must be a list.")
        rules = []
    if len(rules) > MAX_RULES:
        errors.append(f"At most {MAX_RULES} rules are allowed.")
        rules = rules[:MAX_RULES]
    seen_ids = set()
    for index, rule in enumerate(rules):
        where = f"Rule {index + 1}"
        if not isinstance(rule, dict):
            errors.append(f"{where}: must be an object.")
            continue
        rule_errors = []
        rule_id = rule.get("id")
        if not isinstance(rule_id, str) or not _RULE_ID.match(rule_id) or rule_id in seen_ids:
            rule_errors.append("needs a unique id of letters, digits, '-' or '_'.")
        name = rule.get("name", "")
        if not isinstance(name, str) or len(name) > 64 or any(ord(c) < 32 or ord(c) == 127 for c in name):
            rule_errors.append("name must be at most 64 printable characters.")
            name = ""
        where = f"Rule {name.strip() or index + 1!r}" if name.strip() else where
        enabled = rule.get("enabled", True)
        if not isinstance(enabled, bool):
            rule_errors.append("enabled must be true or false.")
        ips = rule.get("devices")
        if not isinstance(ips, list) or not ips or len(ips) > MAX_RULE_DEVICES or not all(valid_device_ip(ip) for ip in ips):
            rule_errors.append("devices must list 1 to 500 private LAN IPv4 addresses.")
            ips = []
        days = rule.get("days")
        if not isinstance(days, list) or not days or not all(_int_in(d, 0, 6) for d in days):
            rule_errors.append("days must list weekdays 0 (Monday) to 6 (Sunday).")
            days = []
        start, end = _normalize_hhmm(rule.get("start")), _normalize_hhmm(rule.get("end"))
        if start is None or end is None:
            rule_errors.append("start and end must be HH:MM times.")
        elif start == end:
            rule_errors.append("start and end must differ.")

        action = rule.get("action")
        cleaned = {"id": rule_id, "name": name.strip(), "enabled": enabled, "devices": sorted(set(ips)),
                   "days": sorted(set(days)), "start": start, "end": end, "action": action}
        has_level, has_watts = "level" in rule, "watts" in rule
        if action == "sleep":
            if has_level or has_watts:
                rule_errors.append("a sleep rule takes no level or watts.")
        elif action == "power":
            if has_level == has_watts:
                rule_errors.append("a power rule needs either a level or watts, not both.")
            elif has_level and rule["level"] not in LEVELS:
                rule_errors.append("level must be low, normal or high.")
            elif has_watts and not _int_in(rule["watts"], MIN_WATTS, MAX_WATTS):
                rule_errors.append(f"watts must be a whole number from {MIN_WATTS} to {MAX_WATTS}.")
            elif has_level:
                cleaned["level"] = rule["level"]
            else:
                cleaned["watts"] = rule["watts"]
        else:
            rule_errors.append("action must be sleep or power.")

        if not rule_errors and device_types is not None:
            for ip in cleaned["devices"]:
                miner_type = device_types.get(ip)
                if miner_type is None:
                    rule_errors.append(f"{ip} is not a configured miner.")
                    continue
                model = clean["devices"].get(ip, {}).get("model", "")
                caps = capabilities_for(miner_type, model)
                if not rule_supported(cleaned, caps):
                    hint = " Set its Avalon model first." if caps["family"] == "avalon" and not model else ""
                    rule_errors.append(f"{ip} ({caps['label']}) cannot {_describe_action(cleaned)}.{hint}")

        if rule_errors:
            errors.extend(f"{where}: {e}" for e in rule_errors)
            continue
        seen_ids.add(rule_id)
        clean["rules"].append(cleaned)

    if "auto_restart" in data:
        ar = data["auto_restart"]
        limits = {"offline_minutes": (1, 1440), "cooldown_minutes": (5, 1440), "low_hashrate_minutes": (0, 1440)}
        if not isinstance(ar, dict) or not isinstance(ar.get("enabled"), bool) \
                or not all(_int_in(ar.get(k), lo, hi) for k, (lo, hi) in limits.items()):
            errors.append("auto_restart needs enabled (true/false), offline_minutes 1-1440, "
                          "cooldown_minutes 5-1440 and low_hashrate_minutes 0-1440 (0 = off).")
        else:
            clean["auto_restart"] = {"enabled": ar["enabled"], **{k: ar[k] for k in limits}}

    return clean, errors


def _describe_action(rule):
    if rule["action"] == "sleep":
        return "sleep"
    if "watts" in rule:
        return f"take a {rule['watts']} W power target"
    return f"switch to {rule['level']} power mode"


def _minutes(hhmm):
    hours, minutes = hhmm.split(":")
    return int(hours) * 60 + int(minutes)


def rule_active(rule, now):
    """Whether an enabled rule's window contains now (a local datetime)."""
    if not rule.get("enabled"):
        return False
    start, end = _minutes(rule["start"]), _minutes(rule["end"])
    current = now.hour * 60 + now.minute
    weekday = now.weekday()
    if start < end:
        return weekday in rule["days"] and start <= current < end
    if current >= start:
        return weekday in rule["days"]
    if current < end:
        return (weekday - 1) % 7 in rule["days"]
    return False


def desired_state(rules, ip, now):
    """(first active sleep rule, first active power rule) for a miner, or None each."""
    sleep_rule = power_rule = None
    for rule in rules:
        if ip not in rule["devices"] or not rule_active(rule, now):
            continue
        if rule["action"] == "sleep" and sleep_rule is None:
            sleep_rule = rule
        elif rule["action"] == "power" and power_rule is None:
            power_rule = rule
    return sleep_rule, power_rule


def low_power_active(automation, now):
    """Whether any miner is inside a sleep or low-power window right now."""
    return any(rule_active(r, now) and (r["action"] == "sleep" or r.get("level") == "low")
               for r in automation.get("rules", []))


def power_key(rule):
    return f"watts:{rule['watts']}" if "watts" in rule else f"level:{rule['level']}"


class AutomationRunner:
    """Brings scheduled miners to the state their rules ask for, once per cycle.

    control provides sleep(device), wake(device) and set_power(device, level, watts),
    raising on failure. state is what the runner has done ({"asleep": {ip: rule id},
    "power": {ip: power key}}) and is safe to persist, so a restarted Sentinel still
    wakes the miners it put to sleep.
    """

    def __init__(self, control, state=None):
        self.control = control
        self.state = {"asleep": {}, "power": {}}
        if isinstance(state, dict):
            for key in self.state:
                if isinstance(state.get(key), dict):
                    self.state[key] = dict(state[key])
        self._retry_after = {}
        self._online = {}

    def is_asleep(self, ip):
        return ip in self.state["asleep"]

    def tick(self, automation, devices, now, online=None):
        """Act on every scheduled miner. Returns [{ip, action, ok, error}].

        devices maps IP to a device dict with at least "type". online optionally maps
        IP to whether the miner is up; one that comes back gets its power mode
        re-applied, since a reboot resets it.
        """
        rules = automation.get("rules", [])
        settings = automation.get("devices", {})
        events = []
        ts = now.timestamp()
        scheduled = {ip for rule in rules if rule.get("enabled") for ip in rule["devices"]}

        for ip in sorted(scheduled | set(self.state["asleep"])):
            device = devices.get(ip)
            if device is None:
                # No longer in the fleet: forget it rather than command a reused address.
                self.state["asleep"].pop(ip, None)
                self.state["power"].pop(ip, None)
                continue
            sleep_rule, power_rule = desired_state(rules, ip, now)
            caps = capabilities_for(device.get("type"), settings.get(ip, {}).get("model", ""))

            is_online = None if online is None else online.get(ip)
            came_back = is_online is True and self._online.get(ip) is False
            if is_online is not None:
                self._online[ip] = is_online

            if sleep_rule is not None and caps["sleep"]:
                if ip not in self.state["asleep"] and self._attempt(events, ip, "sleep", ts, lambda: self.control.sleep(device)):
                    self.state["asleep"][ip] = sleep_rule["id"]
                continue
            if ip in self.state["asleep"]:
                if self._attempt(events, ip, "wake", ts, lambda: self.control.wake(device)):
                    del self.state["asleep"][ip]
                    self.state["power"].pop(ip, None)
                continue

            if came_back or power_rule is None:
                self.state["power"].pop(ip, None)
            if power_rule is None or not rule_supported(power_rule, caps):
                continue
            key = power_key(power_rule)
            if self.state["power"].get(ip) != key and self._attempt(
                    events, ip, "power " + key, ts,
                    lambda: self.control.set_power(device, level=power_rule.get("level"), watts=power_rule.get("watts"))):
                self.state["power"][ip] = key
        return events

    def _attempt(self, events, ip, action, ts, fn):
        key = (ip, action)
        if self._retry_after.get(key, 0) > ts:
            return False
        try:
            fn()
        except Exception as exc:  # one miner's failure must not stop the rest of the fleet
            self._retry_after[key] = ts + RETRY_SECONDS
            events.append({"ip": ip, "action": action, "ok": False, "error": str(exc)})
            return False
        self._retry_after.pop(key, None)
        events.append({"ip": ip, "action": action, "ok": True, "error": ""})
        return True


def migrate_avalon_schedules(schedules, automation):
    """Move Nano 3S and Avalon Q schedules from the dashboard's Avalon scheduler into
    automation rules. Returns (new automation settings, migrated IPs).

    Each old rule becomes an every-day power rule: efficiency → low, balanced →
    normal, high → high. Rule order is kept, so the first matching rule still wins.
    Other Avalon models are left on the old scheduler.
    """
    out = copy.deepcopy(automation)
    existing_ids = {r.get("id") for r in out.get("rules", [])}
    migrated = []
    for ip, schedule in sorted(schedules.items()):
        if not isinstance(schedule, dict) or schedule.get("model") not in ("nano3s", "avalon_q") or not valid_device_ip(ip):
            continue
        model = schedule["model"]
        for index, old in enumerate(schedule.get("rules") or []):
            if not isinstance(old, dict):
                continue
            level = _PROFILE_LEVEL.get(old.get("profile", "balanced"))
            start, end = _normalize_hhmm(old.get("start")), _normalize_hhmm(old.get("end"))
            rule_id = f"avalon-{ip.replace('.', '-')}-{index}"
            if level is None or start is None or end is None or start == end or rule_id in existing_ids:
                continue
            out["rules"].append({
                "id": rule_id, "name": f"{AVALON_HOME_MODELS[model]} {ip} {old.get('profile', 'balanced')}",
                "enabled": bool(schedule.get("enabled")), "devices": [ip], "days": list(range(7)),
                "start": start, "end": end, "action": "power", "level": level,
            })
            existing_ids.add(rule_id)
        out["devices"].setdefault(ip, {"model": "", "auto_restart": True})["model"] = model
        migrated.append(ip)
    return out, migrated


def read_json(path):
    """The parsed file, or None if it does not exist.

    Raises ValueError when the file exists but cannot be read or parsed, so a bad
    write is never mistaken for "no rules".
    """
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise ValueError(f"{path}: {exc}") from None


def write_json_atomic(path, data, mode=0o600):
    directory = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".automation-", suffix=".tmp")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(data, f, indent=2, sort_keys=True)
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
