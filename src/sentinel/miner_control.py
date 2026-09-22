# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Remote control of miners: sleep and wake, power modes, and restarts for the
families Sentinel's own restart code cannot reach.

Every function takes a device dict {"ip", "type", "model", "username",
"password", "port"} and raises ControlError when the miner cannot be reached or
refuses. Which family supports what is in miner_automation.capabilities_for.

Protocols, from vendor API documentation where it exists and otherwise from
independent public descriptions (see docs/reference/MINER_SUPPORT.md). Run against
real hardware: Avalon work modes and reboot (Nano 3S), and NerdQAxe power normal
(NerdQAxe++ firmware v1.1.0, which has no eco preset, so power low is refused).
The rest have not:

  Bitaxe / AxeOS  HTTP POST /api/system/pause and /resume (ESP-Miner 2.14.0+).
  NerdQAxe        HTTP GET /api/system/asic for the eco and default frequency and
                  voltage, PATCH /api/system, then POST /api/system/restart.
  Avalon          cgminer ascset on TCP 4028: 0,reboot,0; 0,workmode,set,<0-2>
                  (Nano 3S, Q, Mini 3). No sleep: the home firmware has no standby.
  Antminer        HTTP digest auth. GET /cgi-bin/reboot.cgi. Modes by reading
                  get_miner_conf.cgi and writing set_miner_conf.cgi with miner-mode
                  0 (normal), 1 (sleep) or 3 (low power).
  Braiins OS      REST /api/v1: auth/login, actions/reboot|pause|resume,
                  performance/power-target.
  LuxOS           TCP 4028 with a logon session: rebootdevice, curtail sleep|wakeup,
                  profileset <watts>W.
  Vnish           REST /api/v1: unlock, system/reboot, mining/pause|resume,
                  settings miner.overclock.preset.
  Whatsminer      API v3 on TCP 4433: length-prefixed JSON; set.system.reboot,
                  set.miner.service stop|start, set.miner.power_mode low|normal|high.
                  The API must be enabled in WhatsMinerTool first.
  Elphapex        HTTP digest auth, GET /cgi-bin/reboot.cgi.
"""
import base64
import hashlib
import ipaddress
import json
import os
import socket
import struct
import sys
import time

import requests
from requests.auth import HTTPDigestAuth

from miner_automation import (AUTOMATION_FILE, CREDENTIALS_FILE, capabilities_for, default_automation,
                              family_for, read_json, validate_automation)

TIMEOUT = 10
CGMINER_PORT = 4028
WHATSMINER_PORT = 4433
MAX_RESPONSE = 1 << 20


class ControlError(Exception):
    """A miner could not be controlled."""


def _host(device):
    ip = str(device.get("ip", "")).strip()
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        raise ControlError(f"invalid IP address {ip!r}") from None
    if addr.version != 4 or not addr.is_private or addr.is_loopback or addr.is_link_local \
            or addr.is_multicast or addr.is_reserved:
        raise ControlError(f"{ip} is not a private LAN address")
    return ip


def _http(device, method, path, body=None, headers=None, auth=None):
    port = int(device.get("http_port") or 80)
    url = f"http://{_host(device)}:{port}{path}"
    try:
        resp = requests.request(method, url, json=body, headers=headers, auth=auth, timeout=TIMEOUT)
    except requests.RequestException as exc:
        raise ControlError(f"{method} {path} failed: {exc.__class__.__name__}") from None
    if resp.status_code in (401, 403):
        raise ControlError(f"{method} {path}: HTTP {resp.status_code}; check the device credentials")
    if not 200 <= resp.status_code < 300:
        raise ControlError(f"{method} {path}: HTTP {resp.status_code}")
    return resp


def _json_object(resp, what):
    try:
        data = resp.json()
    except ValueError:
        data = None
    if not isinstance(data, (dict, list)):
        raise ControlError(f"{what}: unreadable response")
    return data


def _positive_number(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0


# ── cgminer-style TCP API (Avalon, LuxOS) ────────────────────────────────────

def _cgminer(device, command, parameter=None):
    request = {"command": command}
    if parameter is not None:
        request["parameter"] = parameter
    port = int(device.get("port") or CGMINER_PORT)
    data = b""
    try:
        with socket.create_connection((_host(device), port), timeout=TIMEOUT) as sock:
            sock.sendall(json.dumps(request).encode())
            while b"\x00" not in data and len(data) < MAX_RESPONSE:
                chunk = sock.recv(4096)
                if not chunk:
                    break
                data += chunk
    except OSError as exc:
        raise ControlError(f"{command} failed: {exc}") from None
    text = data.split(b"\x00", 1)[0].decode("utf-8", "replace")
    start = text.find("{")
    try:
        reply, _ = json.JSONDecoder().raw_decode(text[start:]) if start >= 0 else (None, 0)
    except ValueError:
        reply = None
    if not isinstance(reply, dict):
        raise ControlError(f"{command}: unreadable reply")
    return reply


def _cgminer_checked(device, command, parameter=None):
    reply = _cgminer(device, command, parameter)
    status = reply.get("STATUS")
    status = status[0] if isinstance(status, list) and status else status
    if not isinstance(status, dict) or status.get("STATUS") not in ("S", "I"):
        message = status.get("Msg") if isinstance(status, dict) else "no status"
        raise ControlError(f"{command} refused: {message}")
    return reply


# ── Bitaxe / AxeOS ───────────────────────────────────────────────────────────

def _axeos_sleep(device):
    _http(device, "POST", "/api/system/pause")


def _axeos_wake(device):
    _http(device, "POST", "/api/system/resume")


# ── NerdQAxe ─────────────────────────────────────────────────────────────────

def _nerdqaxe_power(device, level, watts):
    asic = _json_object(_http(device, "GET", "/api/system/asic"), "ASIC settings")
    prefix = {"low": "eco", "normal": "default"}[level]
    frequency, voltage = asic.get(prefix + "Frequency"), asic.get(prefix + "Voltage")
    if not _positive_number(frequency) or not _positive_number(voltage):
        raise ControlError(f"device reports no {prefix} frequency and voltage")
    # With OTP enabled on the device this PATCH is refused with 401.
    _http(device, "PATCH", "/api/system", body={"frequency": frequency, "coreVoltage": voltage})
    _http(device, "POST", "/api/system/restart")


# ── Avalon ───────────────────────────────────────────────────────────────────

_AVALON_WORKMODE = {"low": 0, "normal": 1, "high": 2}


def _avalon_restart(device):
    _cgminer_checked(device, "ascset", "0,reboot,0")


def _avalon_power(device, level, watts):
    _cgminer_checked(device, "ascset", f"0,workmode,set,{_AVALON_WORKMODE[level]}")


# ── Antminer (stock firmware) and Elphapex ───────────────────────────────────

def _digest(device):
    return HTTPDigestAuth(device.get("username") or "root", device.get("password") or "root")


def _cgi_reboot(device):
    _http(device, "GET", "/cgi-bin/reboot.cgi", auth=_digest(device))


def _antminer_mode(device, mode):
    auth = _digest(device)
    conf = _json_object(_http(device, "GET", "/cgi-bin/get_miner_conf.cgi", auth=auth), "miner config")
    pools = conf.get("pools") if isinstance(conf, dict) else None
    if not isinstance(pools, list) or not any(isinstance(p, dict) for p in pools):
        # The write replaces the whole config; without the pools it would erase them.
        raise ControlError("miner config has no pools; not writing it back")
    body = {
        "bitmain-fan-ctrl": conf.get("bitmain-fan-ctrl", False),
        "bitmain-fan-pwm": str(conf.get("bitmain-fan-pwm", "100")),
        "freq-level": str(conf.get("bitmain-freq-level", "100")),
        "miner-mode": mode,
        "pools": [{"url": p.get("url", ""), "user": p.get("user", ""), "pass": p.get("pass", "")}
                  for p in pools if isinstance(p, dict)],
    }
    _http(device, "POST", "/cgi-bin/set_miner_conf.cgi", body=body, auth=auth)


# ── Braiins OS ───────────────────────────────────────────────────────────────

def _braiins_headers(device):
    resp = _http(device, "POST", "/api/v1/auth/login",
                 body={"username": device.get("username") or "root", "password": device.get("password") or ""})
    login = _json_object(resp, "login")
    token = login.get("token") if isinstance(login, dict) else None
    if not token:
        raise ControlError("login returned no token")
    return {"Authorization": f"Bearer {token}"}


def _braiins_action(device, action):
    _http(device, "PUT", f"/api/v1/actions/{action}", headers=_braiins_headers(device))


def _braiins_power(device, level, watts):
    _http(device, "PUT", "/api/v1/performance/power-target", body={"watt": watts}, headers=_braiins_headers(device))


# ── LuxOS ────────────────────────────────────────────────────────────────────

def _luxos(device, commands):
    """Run (command, parameter) pairs in one logon session, always logging off."""
    session = _cgminer_checked(device, "logon").get("SESSION")
    session_id = session[0].get("SessionID") if isinstance(session, list) and session and isinstance(session[0], dict) else None
    if not session_id:
        raise ControlError("logon returned no session; another LuxOS session may be open")
    try:
        for command, parameter in commands:
            _cgminer_checked(device, command, f"{session_id},{parameter}" if parameter else session_id)
    finally:
        try:
            _cgminer(device, "logoff", session_id)
        except ControlError:
            pass


def _luxos_power(device, level, watts):
    # Same sequence as the dashboard's LuxOS profile control: the autotuner is
    # paused while the profile changes.
    _luxos(device, [("atmset", "enabled=false"), ("profileset", f"{watts}W"), ("atmset", "enabled=true")])


# ── Vnish ────────────────────────────────────────────────────────────────────

def _vnish_token(device):
    unlock = _json_object(_http(device, "POST", "/api/v1/unlock", body={"pw": device.get("password") or "admin"}), "unlock")
    token = unlock.get("token") if isinstance(unlock, dict) else None
    if not token:
        raise ControlError("unlock returned no token")
    return token


def _vnish_restart(device):
    _http(device, "POST", "/api/v1/system/reboot", headers={"Authorization": f"Bearer {_vnish_token(device)}"})


def _vnish_mining(device, action):
    _http(device, "POST", f"/api/v1/mining/{action}", headers={"Authorization": _vnish_token(device)})


def _vnish_power(device, level, watts):
    headers = {"Authorization": _vnish_token(device)}
    presets = _json_object(_http(device, "GET", "/api/v1/autotune/presets", headers=headers), "presets")
    if isinstance(presets, dict):
        presets = presets.get("presets", [])
    names = sorted({str(p.get("name")) for p in presets if isinstance(p, dict) and p.get("name") is not None})
    if str(watts) not in names:
        raise ControlError(f"no {watts} W preset on this miner; presets: {', '.join(names) or 'none'}")
    _http(device, "POST", "/api/v1/settings", body={"miner": {"overclock": {"preset": str(watts)}}}, headers=headers)


# ── Whatsminer (API v3) ──────────────────────────────────────────────────────

def _recv_exact(sock, size):
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise ControlError("connection closed mid-reply")
        data += chunk
    return data


def _whatsminer(device, cmd, param=None, privileged=True):
    request = {"cmd": cmd}
    if param is not None:
        request["param"] = param
    if privileged:
        info = _whatsminer(device, "get.device.info", "salt", privileged=False).get("msg")
        salt = info.get("salt") if isinstance(info, dict) else None
        if not salt:
            raise ControlError("device returned no salt; is the API enabled in WhatsMinerTool?")
        ts = int(time.time())
        password = device.get("password") or "super"
        digest = hashlib.sha256(f"{cmd}{password}{salt}{ts}".encode()).digest()
        request.update(ts=ts, account=device.get("username") or "super",
                       token=base64.b64encode(digest).decode()[:8])
    payload = json.dumps(request).encode()
    port = int(device.get("api_port") or WHATSMINER_PORT)
    try:
        with socket.create_connection((_host(device), port), timeout=TIMEOUT) as sock:
            sock.sendall(struct.pack("<I", len(payload)) + payload)
            (length,) = struct.unpack("<I", _recv_exact(sock, 4))
            if length > MAX_RESPONSE:
                raise ControlError(f"{cmd}: reply too large")
            reply = json.loads(_recv_exact(sock, length))
    except OSError as exc:
        raise ControlError(f"{cmd} failed: {exc}") from None
    except ValueError:
        raise ControlError(f"{cmd}: unreadable reply") from None
    if not isinstance(reply, dict) or reply.get("code") != 0:
        message = reply.get("msg") if isinstance(reply, dict) else reply
        raise ControlError(f"{cmd} refused: {message}")
    return reply


# ── Dispatch ─────────────────────────────────────────────────────────────────

_DRIVERS = {
    "axeos": {"sleep": _axeos_sleep, "wake": _axeos_wake},
    "nerdqaxe": {"power": _nerdqaxe_power},
    "avalon": {"restart": _avalon_restart, "power": _avalon_power},
    "antminer": {
        "restart": _cgi_reboot,
        "sleep": lambda d: _antminer_mode(d, 1),
        "wake": lambda d: _antminer_mode(d, 0),
        "power": lambda d, level, watts: _antminer_mode(d, {"low": 3, "normal": 0}[level]),
    },
    "braiins": {
        "restart": lambda d: _braiins_action(d, "reboot"),
        "sleep": lambda d: _braiins_action(d, "pause"),
        "wake": lambda d: _braiins_action(d, "resume"),
        "power": _braiins_power,
    },
    "luxos": {
        "restart": lambda d: _luxos(d, [("rebootdevice", None)]),
        "sleep": lambda d: _luxos(d, [("curtail", "sleep")]),
        "wake": lambda d: _luxos(d, [("curtail", "wakeup")]),
        "power": _luxos_power,
    },
    "vnish": {
        "restart": _vnish_restart,
        "sleep": lambda d: _vnish_mining(d, "pause"),
        "wake": lambda d: _vnish_mining(d, "resume"),
        "power": _vnish_power,
    },
    "whatsminer": {
        "restart": lambda d: _whatsminer(d, "set.system.reboot"),
        "sleep": lambda d: _whatsminer(d, "set.miner.service", "stop"),
        "wake": lambda d: _whatsminer(d, "set.miner.service", "start"),
        "power": lambda d, level, watts: _whatsminer(d, "set.miner.power_mode", level),
    },
    "elphapex": {"restart": _cgi_reboot},
}

# Families whose restarts go through this module; Sentinel restarts the rest itself.
RESTART_FAMILIES = frozenset(family for family, ops in _DRIVERS.items() if "restart" in ops)


def _label(device):
    return capabilities_for(device.get("type"), device.get("model", ""))["label"]


def restart(device):
    driver = _DRIVERS.get(family_for(device.get("type")), {}).get("restart")
    if driver is None:
        raise ControlError(f"{_label(device)} has no remote restart here")
    driver(device)


def _sleep_or_wake(device, op):
    caps = capabilities_for(device.get("type"), device.get("model", ""))
    if not caps["sleep"]:
        raise ControlError(f"{caps['label']} does not support sleep")
    _DRIVERS[caps["family"]][op](device)


def sleep(device):
    _sleep_or_wake(device, "sleep")


def wake(device):
    _sleep_or_wake(device, "wake")


def set_power(device, level=None, watts=None):
    caps = capabilities_for(device.get("type"), device.get("model", ""))
    if watts is not None:
        if not caps["watts"] or type(watts) is not int:
            raise ControlError(f"{caps['label']} has no power target")
    elif level not in caps["levels"]:
        hint = "; set its Avalon model first" if caps["family"] == "avalon" else ""
        raise ControlError(f"{caps['label']} has no {level!r} power mode{hint}")
    _DRIVERS[caps["family"]]["power"](device, level, watts)


# ── Command line ─────────────────────────────────────────────────────────────
# `spiralctl miner control` runs this module so an operator can send one command
# to one miner and see what it answered, with the device built as Sentinel builds
# it. That is the only way to try these drivers on real hardware without waiting
# for an automation window.

def device_from_files(ip, data_dir, miner_type=None, model=None, port=None):
    """The device Sentinel would control for ip, read from its shared data directory.

    Type and port come from miners.json, the Avalon model from automation.json and
    the login from device_credentials.json. Any argument given overrides the file.
    Raises ControlError when the type is unknown, ValueError when a file is unreadable.
    """
    miners = read_json(os.path.join(data_dir, "miners.json")) or {}
    for listed_type, items in (miners.get("by_type") or {}).items():
        for item in items if isinstance(items, list) else ():
            item_ip = item.get("ip") if isinstance(item, dict) else item
            if item_ip == ip:
                miner_type = miner_type or listed_type
                if port is None and isinstance(item, dict) and item.get("port"):
                    port = item["port"]
    miner_type = miner_type or ((miners.get("miners") or {}).get(ip) or {}).get("type")
    if not miner_type:
        raise ControlError(f"{ip} is not in miners.json; pass --type")

    if model is None:
        raw = read_json(os.path.join(data_dir, AUTOMATION_FILE))
        settings, _ = validate_automation(raw if raw is not None else default_automation())
        model = settings["devices"].get(ip, {}).get("model", "")

    device = {"ip": ip, "type": miner_type, "model": model}
    if port:
        device["port"] = port
    creds = (read_json(os.path.join(data_dir, CREDENTIALS_FILE)) or {}).get(ip)
    for key in ("username", "password"):
        if isinstance(creds, dict) and isinstance(creds.get(key), str) and creds[key]:
            device[key] = creds[key]
    return device


def main(argv=None):
    import argparse

    parser = argparse.ArgumentParser(
        prog="spiralctl miner control",
        description="Send one command to one miner, as Sentinel's automation would.",
        epilog="power takes low, normal or high, or a number of watts (Braiins OS, LuxOS, Vnish). "
               "OK means the miner accepted the command; check the miner to confirm it took effect.")
    parser.add_argument("ip")
    parser.add_argument("action", choices=("info", "sleep", "wake", "restart", "power"))
    parser.add_argument("value", nargs="?")
    parser.add_argument("--data-dir", default=os.path.join(os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool"), "data"))
    parser.add_argument("--type", dest="miner_type", help="miner type, when the miner is not in miners.json")
    parser.add_argument("--model", help="Avalon model: nano3s, avalon_q or mini3")
    parser.add_argument("--port", type=int, help="cgminer API port (default 4028)")
    args = parser.parse_args(argv)
    if args.action == "power" and args.value is None:
        parser.error("power needs low, normal, high or a number of watts")
    if args.action != "power" and args.value is not None:
        parser.error(f"{args.action} takes no value")

    try:
        device = device_from_files(args.ip, args.data_dir, args.miner_type, args.model, args.port)
    except (ControlError, ValueError) as exc:
        print(f"FAILED: {exc}")
        return 1
    caps = capabilities_for(device["type"], device["model"])
    name = f"{caps['label']} at {args.ip}"

    if args.action == "info":
        print(f"{name} (type {device['type']}{', model ' + device['model'] if device['model'] else ''})")
        print(f"  sleep / wake:  {'yes' if caps['sleep'] else 'no'}")
        print(f"  power modes:   {', '.join(caps['levels']) or 'none'}")
        print(f"  power target:  {'watts' if caps['watts'] else 'no'}")
        print(f"  restart:       {'yes' if caps['family'] in RESTART_FAMILIES else 'not through this command'}")
        print(f"  stored login:  {'yes' if 'username' in device or 'password' in device else 'no (firmware default)'}")
        if caps["family"] == "avalon" and not caps["levels"]:
            print("  set its Avalon model on the Automation page, or pass --model, to enable power modes")
        return 0

    try:
        if args.action == "power":
            if args.value.isdigit():
                set_power(device, watts=int(args.value))
            else:
                set_power(device, level=args.value)
        else:
            {"sleep": sleep, "wake": wake, "restart": restart}[args.action](device)
    except ControlError as exc:
        print(f"FAILED: {name}: {exc}")
        return 1
    print(f"OK: {name} accepted {args.action}{' ' + args.value if args.value else ''}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
