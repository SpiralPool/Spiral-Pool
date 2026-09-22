# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Miner drivers (src/sentinel/miner_control.py) against mock devices.

The mocks are real HTTP and TCP servers on loopback: HTTP digest authentication
is verified the way a miner verifies it, cgminer replies are NUL-terminated JSON,
and Whatsminer frames are length-prefixed.

Run: python -m pytest tests/test_miner_control.py -v
"""
import base64
import hashlib
import json
import os
import re
import socket
import struct
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src", "sentinel"))

import miner_automation as ma  # noqa: E402
import miner_control as mc  # noqa: E402

REAL_HOST = mc._host
OK = {"STATUS": [{"STATUS": "S", "Msg": "ok"}]}
REALM, NONCE = "antMiner Configuration", "5f1e2d3c4b5a"


@pytest.fixture(autouse=True)
def loopback(monkeypatch):
    monkeypatch.setattr(mc, "_host", lambda device: "127.0.0.1")


def md5(text):
    return hashlib.md5(text.encode()).hexdigest()


class HTTPDevice:
    """routes: {(method, path): (status, body or fn(request) -> body)}."""

    def __init__(self, routes, digest=None):
        self.routes, self.digest = routes, digest
        self.requests, self.unauthorized = [], 0
        device = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def handle_request(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                if device.digest and not device.digest_ok(self.command, self.path, self.headers.get("Authorization", "")):
                    device.unauthorized += 1
                    self.send_response(401)
                    self.send_header("WWW-Authenticate", f'Digest realm="{REALM}", nonce="{NONCE}", qop="auth"')
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                request = {"method": self.command, "path": self.path,
                           "headers": {k.lower(): v for k, v in self.headers.items()},
                           "json": json.loads(raw) if raw else None}
                device.requests.append(request)
                status, body = device.routes.get((self.command, self.path), (404, {"error": "not found"}))
                data = json.dumps(body(request) if callable(body) else body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            do_GET = do_POST = do_PUT = do_PATCH = handle_request

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.port = self.server.server_address[1]

    def digest_ok(self, method, uri, header):
        if not header.startswith("Digest "):
            return False
        params = {k: a or b for k, a, b in re.findall(r'(\w+)=(?:"([^"]*)"|([^,\s]*))', header[7:])}
        user, password = self.digest
        ha1 = md5(f"{user}:{REALM}:{password}")
        ha2 = md5(f"{method}:{params.get('uri')}")
        expected = md5(f"{ha1}:{params.get('nonce')}:{params.get('nc')}:{params.get('cnonce')}:{params.get('qop')}:{ha2}")
        return params.get("username") == user and params.get("uri") == uri and params.get("response") == expected

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class TCPDevice:
    """framing "cgminer": JSON in, JSON + NUL out. framing "whatsminer": 4-byte
    little-endian length prefix both ways."""

    def __init__(self, reply, framing="cgminer"):
        self.reply, self.framing, self.requests = reply, framing, []
        self.sock = socket.socket()
        self.sock.bind(("127.0.0.1", 0))
        self.sock.listen(16)
        self.port = self.sock.getsockname()[1]
        threading.Thread(target=self.serve, daemon=True).start()

    def serve(self):
        while True:
            try:
                conn, _ = self.sock.accept()
            except OSError:
                return
            with conn:
                request = self.read(conn)
                if request is None:
                    continue
                self.requests.append(request)
                data = json.dumps(self.reply(request)).encode()
                if self.framing == "whatsminer":
                    conn.sendall(struct.pack("<I", len(data)) + data)
                else:
                    conn.sendall(data + b"\x00")

    def read(self, conn):
        if self.framing == "whatsminer":
            header = conn.recv(4)
            if len(header) < 4:
                return None
            (length,) = struct.unpack("<I", header)
            body = b""
            while len(body) < length:
                body += conn.recv(length - len(body))
            return json.loads(body)
        buf = b""
        while True:
            chunk = conn.recv(4096)
            if not chunk:
                return None
            buf += chunk
            try:
                return json.loads(buf)
            except ValueError:
                continue

    def close(self):
        self.sock.close()


@pytest.fixture
def http_device():
    made = []

    def make(routes, digest=None):
        made.append(HTTPDevice(routes, digest))
        return made[-1]
    yield make
    for d in made:
        d.close()


@pytest.fixture
def tcp_device():
    made = []

    def make(reply, framing="cgminer"):
        made.append(TCPDevice(reply, framing))
        return made[-1]
    yield make
    for d in made:
        d.close()


# ── safety ───────────────────────────────────────────────────────────────────

@pytest.mark.parametrize("ip", ["8.8.8.8", "127.0.0.1", "169.254.1.1", "not-an-ip", ""])
def test_only_private_lan_addresses_are_contacted(ip):
    with pytest.raises(mc.ControlError):
        REAL_HOST({"ip": ip})
    assert REAL_HOST({"ip": "192.168.1.5"}) == "192.168.1.5"


def test_capabilities_and_drivers_agree():
    for family in ma._FAMILIES:
        miner_type = next(t for t, f in ma.FAMILY_BY_TYPE.items() if f == family)
        caps = ma.capabilities_for(miner_type, "nano3s" if family == "avalon" else "")
        ops = mc._DRIVERS.get(family, {})
        assert caps["sleep"] == ("sleep" in ops and "wake" in ops), family
        assert bool(caps["levels"] or caps["watts"]) == ("power" in ops), family


def test_unsupported_actions_raise_without_contacting_the_miner(http_device):
    dev = http_device({})
    for call in (lambda d: mc.sleep(d), lambda d: mc.set_power(d, level="low"), lambda d: mc.restart(d)):
        with pytest.raises(mc.ControlError):
            call({"ip": "192.168.1.9", "type": "goldshell", "http_port": dev.port})
    with pytest.raises(mc.ControlError, match="Avalon does not support sleep"):
        mc.sleep({"ip": "192.168.1.9", "type": "avalon", "model": "nano3s"})
    with pytest.raises(mc.ControlError, match="set its Avalon model"):
        mc.set_power({"ip": "192.168.1.9", "type": "avalon"}, level="low")
    with pytest.raises(mc.ControlError):
        mc.set_power({"ip": "192.168.1.9", "type": "braiins"}, level="low")
    assert dev.requests == []


# ── Bitaxe / NerdQAxe ────────────────────────────────────────────────────────

def test_axeos_pause_and_resume(http_device):
    dev = http_device({("POST", "/api/system/pause"): (200, {}), ("POST", "/api/system/resume"): (200, {})})
    device = {"ip": "192.168.1.40", "type": "bitaxe", "http_port": dev.port}
    mc.sleep(device)
    mc.wake(device)
    assert [(r["method"], r["path"]) for r in dev.requests] == [("POST", "/api/system/pause"), ("POST", "/api/system/resume")]


def test_axeos_firmware_without_pause_reports_it(http_device):
    dev = http_device({})
    with pytest.raises(mc.ControlError, match="HTTP 404"):
        mc.sleep({"ip": "192.168.1.40", "type": "bitaxe", "http_port": dev.port})


ASIC = {"ecoFrequency": 400, "ecoVoltage": 1100, "defaultFrequency": 525, "defaultVoltage": 1150}


def test_nerdqaxe_low_power_writes_eco_values_then_restarts(http_device):
    dev = http_device({("GET", "/api/system/asic"): (200, ASIC), ("PATCH", "/api/system"): (200, {}),
                       ("POST", "/api/system/restart"): (200, {})})
    mc.set_power({"ip": "192.168.1.41", "type": "nerdqaxe", "http_port": dev.port}, level="low")
    assert [(r["method"], r["path"]) for r in dev.requests] == [
        ("GET", "/api/system/asic"), ("PATCH", "/api/system"), ("POST", "/api/system/restart")]
    assert dev.requests[1]["json"] == {"frequency": 400, "coreVoltage": 1100}


def test_nerdqaxe_with_otp_is_refused_and_not_restarted(http_device):
    dev = http_device({("GET", "/api/system/asic"): (200, ASIC), ("PATCH", "/api/system"): (401, {})})
    with pytest.raises(mc.ControlError, match="credentials"):
        mc.set_power({"ip": "192.168.1.41", "type": "nerdqaxe", "http_port": dev.port}, level="normal")
    assert not any(r["path"] == "/api/system/restart" for r in dev.requests)


# ── Avalon ───────────────────────────────────────────────────────────────────

def test_avalon_home_model_commands(tcp_device):
    dev = tcp_device(lambda request: OK)
    device = {"ip": "192.168.1.14", "type": "avalon", "model": "nano3s", "port": dev.port}
    mc.set_power(device, level="high")
    mc.restart(device)

    assert dev.requests == [{"command": "ascset", "parameter": "0,workmode,set,2"},
                            {"command": "ascset", "parameter": "0,reboot,0"}]


def test_avalon_cannot_sleep_and_is_never_sent_softoff(tcp_device):
    # A real Nano 3S (MM319) answered "Unknown option: softoff"; its firmware has no standby.
    dev = tcp_device(lambda request: OK)
    device = {"ip": "192.168.1.14", "type": "avalon", "model": "nano3s", "port": dev.port}
    for op in (mc.sleep, mc.wake):
        with pytest.raises(mc.ControlError, match="Avalon does not support sleep"):
            op(device)
    assert dev.requests == []


def test_avalon_refusal_is_an_error(tcp_device):
    dev = tcp_device(lambda request: {"STATUS": [{"STATUS": "E", "Msg": "Don't permit switch mode"}]})
    with pytest.raises(mc.ControlError, match="Don't permit switch mode"):
        mc.set_power({"ip": "192.168.1.14", "type": "avalon", "model": "avalon_q", "port": dev.port}, level="low")


def test_any_avalon_can_be_restarted(tcp_device):
    dev = tcp_device(lambda request: OK)
    mc.restart({"ip": "192.168.1.15", "type": "avalon", "port": dev.port})
    assert dev.requests == [{"command": "ascset", "parameter": "0,reboot,0"}]


# ── Antminer / Elphapex ──────────────────────────────────────────────────────

CONF = {"bitmain-fan-ctrl": False, "bitmain-fan-pwm": "100", "bitmain-freq-level": "100", "bitmain-work-mode": "0",
        "pools": [{"url": "stratum+tcp://192.168.1.2:3333", "user": "DAddr.rig1", "pass": "x", "extra": "dropped"}]}


def test_antminer_modes_keep_the_pools_and_use_digest_auth(http_device):
    dev = http_device({("GET", "/cgi-bin/get_miner_conf.cgi"): (200, CONF),
                       ("POST", "/cgi-bin/set_miner_conf.cgi"): (200, {"stats": "success"})},
                      digest=("root", "s3cret"))
    device = {"ip": "192.168.1.30", "type": "antminer", "http_port": dev.port, "password": "s3cret"}
    mc.sleep(device)
    mc.set_power(device, level="low")
    mc.wake(device)

    writes = [r["json"] for r in dev.requests if r["method"] == "POST"]
    assert [w["miner-mode"] for w in writes] == [1, 3, 0]
    assert writes[0]["pools"] == [{"url": "stratum+tcp://192.168.1.2:3333", "user": "DAddr.rig1", "pass": "x"}]
    assert dev.unauthorized > 0  # every call answered the digest challenge


def test_antminer_wrong_password_is_reported(http_device):
    dev = http_device({("GET", "/cgi-bin/reboot.cgi"): (200, {})}, digest=("root", "s3cret"))
    with pytest.raises(mc.ControlError, match="credentials"):
        mc.restart({"ip": "192.168.1.30", "type": "antminer", "http_port": dev.port, "password": "wrong"})
    assert dev.requests == []


def test_antminer_config_without_pools_is_not_written(http_device):
    dev = http_device({("GET", "/cgi-bin/get_miner_conf.cgi"): (200, {"pools": []}),
                       ("POST", "/cgi-bin/set_miner_conf.cgi"): (200, {})}, digest=("root", "root"))
    with pytest.raises(mc.ControlError, match="no pools"):
        mc.sleep({"ip": "192.168.1.30", "type": "antminer_scrypt", "http_port": dev.port})
    assert not any(r["method"] == "POST" for r in dev.requests)


def test_elphapex_restart(http_device):
    dev = http_device({("GET", "/cgi-bin/reboot.cgi"): (200, {})}, digest=("root", "root"))
    mc.restart({"ip": "192.168.1.31", "type": "elphapex", "http_port": dev.port})
    assert dev.requests[0]["path"] == "/cgi-bin/reboot.cgi"


# ── Braiins OS / Vnish ───────────────────────────────────────────────────────

def test_braiins_logs_in_and_sends_the_token(http_device):
    dev = http_device({("POST", "/api/v1/auth/login"): (200, {"token": "tok123", "timeout_s": 3600}),
                       ("PUT", "/api/v1/actions/pause"): (200, {}),
                       ("PUT", "/api/v1/performance/power-target"): (200, {"watt": 900})})
    device = {"ip": "192.168.1.50", "type": "braiins", "http_port": dev.port, "username": "root", "password": "pw"}
    mc.sleep(device)
    mc.set_power(device, watts=900)

    assert dev.requests[0]["json"] == {"username": "root", "password": "pw"}
    puts = [r for r in dev.requests if r["method"] == "PUT"]
    assert [r["path"] for r in puts] == ["/api/v1/actions/pause", "/api/v1/performance/power-target"]
    assert all(r["headers"]["authorization"] == "Bearer tok123" for r in puts)
    assert puts[1]["json"] == {"watt": 900}


def test_vnish_pause_and_preset_must_exist(http_device):
    dev = http_device({("POST", "/api/v1/unlock"): (200, {"token": "vtok"}),
                       ("POST", "/api/v1/mining/pause"): (200, {}),
                       ("GET", "/api/v1/autotune/presets"): (200, [{"name": "3200"}, {"name": "2800"}]),
                       ("POST", "/api/v1/settings"): (200, {})})
    device = {"ip": "192.168.1.51", "type": "vnish", "http_port": dev.port, "password": "admin"}
    mc.sleep(device)
    assert dev.requests[1]["headers"]["authorization"] == "vtok"

    with pytest.raises(mc.ControlError, match="presets: 2800, 3200"):
        mc.set_power(device, watts=3000)
    mc.set_power(device, watts=3200)
    assert dev.requests[-1]["json"] == {"miner": {"overclock": {"preset": "3200"}}}


# ── LuxOS ────────────────────────────────────────────────────────────────────

def test_luxos_uses_one_session_and_always_logs_off(tcp_device):
    def reply(request):
        if request["command"] == "logon":
            return {"STATUS": [{"STATUS": "S"}], "SESSION": [{"SessionID": "sid9"}]}
        if request["command"] == "curtail" and request["parameter"].endswith("wakeup"):
            return {"STATUS": [{"STATUS": "E", "Msg": "not sleeping"}]}
        return OK

    dev = tcp_device(reply)
    device = {"ip": "192.168.1.60", "type": "luxos", "port": dev.port}
    mc.sleep(device)
    with pytest.raises(mc.ControlError, match="not sleeping"):
        mc.wake(device)
    mc.set_power(device, watts=1200)

    sent = [(r["command"], r.get("parameter")) for r in dev.requests]
    assert sent == [
        ("logon", None), ("curtail", "sid9,sleep"), ("logoff", "sid9"),
        ("logon", None), ("curtail", "sid9,wakeup"), ("logoff", "sid9"),
        ("logon", None), ("atmset", "sid9,enabled=false"), ("profileset", "sid9,1200W"),
        ("atmset", "sid9,enabled=true"), ("logoff", "sid9"),
    ]


# ── Whatsminer ───────────────────────────────────────────────────────────────

def test_whatsminer_v3_token_and_commands(tcp_device):
    def reply(request):
        if request["cmd"] == "get.device.info":
            return {"code": 0, "msg": {"salt": "BQ5hoXV9"}}
        if request["cmd"] == "set.miner.service" and request["param"] == "start":
            return {"code": -2, "msg": "invalid token"}
        return {"code": 0, "msg": "ok"}

    dev = tcp_device(reply, framing="whatsminer")
    device = {"ip": "192.168.1.70", "type": "whatsminer", "api_port": dev.port, "password": "hunter2"}
    mc.set_power(device, level="low")
    with pytest.raises(mc.ControlError, match="invalid token"):
        mc.wake(device)

    command = dev.requests[1]
    assert command["cmd"] == "set.miner.power_mode" and command["param"] == "low" and command["account"] == "super"
    expected = base64.b64encode(hashlib.sha256(f"set.miner.power_mode" f"hunter2" f"BQ5hoXV9" f"{command['ts']}".encode()).digest()).decode()[:8]
    assert command["token"] == expected


# ── command line (spiralctl miner control) ───────────────────────────────────

ROOT = os.path.join(os.path.dirname(__file__), "..")
NANO = "192.168.1.14"


def data_dir(tmp_path, miners=None, automation=None, credentials=None):
    for name, data in (("miners.json", miners), (ma.AUTOMATION_FILE, automation), (ma.CREDENTIALS_FILE, credentials)):
        if data is not None:
            (tmp_path / name).write_text(json.dumps(data))
    return str(tmp_path)


def nano_dir(tmp_path, port=4029):
    return data_dir(
        tmp_path,
        miners={"miners": {NANO: {"type": "avalon"}}, "by_type": {"avalon": [{"ip": NANO, "port": port}]}},
        automation={"version": 1, "rules": [], "devices": {NANO: {"model": "nano3s"}}},
        credentials={NANO: {"username": "root", "password": "s3cret"}})


def test_device_is_built_from_the_files_sentinel_reads(tmp_path):
    assert mc.device_from_files(NANO, nano_dir(tmp_path)) == {
        "ip": NANO, "type": "avalon", "model": "nano3s", "port": 4029, "username": "root", "password": "s3cret"}


def test_arguments_override_the_files(tmp_path):
    d = data_dir(tmp_path, miners={"by_type": {"nerdqaxe": [NANO]}})
    assert mc.device_from_files(NANO, d, "avalon", "avalon_q", 4030) == {
        "ip": NANO, "type": "avalon", "model": "avalon_q", "port": 4030}
    assert mc.device_from_files(NANO, d)["type"] == "nerdqaxe"


def test_command_reaches_the_miner_and_never_prints_the_password(tmp_path, tcp_device, capsys):
    dev = tcp_device(lambda request: OK)
    assert mc.main(["--data-dir", nano_dir(tmp_path, port=dev.port), NANO, "power", "high"]) == 0
    assert dev.requests == [{"command": "ascset", "parameter": "0,workmode,set,2"}]
    out = capsys.readouterr().out
    assert out.startswith("OK: Avalon at 192.168.1.14 accepted power high") and "s3cret" not in out


def test_a_refusal_is_reported_as_failed(tmp_path, tcp_device, capsys):
    dev = tcp_device(lambda request: {"STATUS": [{"STATUS": "E", "Msg": "Don't permit switch mode"}]})
    assert mc.main(["--data-dir", nano_dir(tmp_path, port=dev.port), NANO, "power", "low"]) == 1
    assert "FAILED: Avalon at 192.168.1.14: ascset refused: Don't permit switch mode" in capsys.readouterr().out


def test_unknown_miner_and_unsupported_action_fail_without_contacting_it(tmp_path, capsys, monkeypatch):
    monkeypatch.setattr(mc, "_host", lambda device: pytest.fail("the miner must not be contacted"))
    empty = data_dir(tmp_path)
    assert mc.main(["--data-dir", empty, NANO, "sleep"]) == 1
    assert "not in miners.json; pass --type" in capsys.readouterr().out
    assert mc.main(["--data-dir", empty, "--type", "nerdqaxe", NANO, "sleep"]) == 1
    assert "NerdQAxe does not support sleep" in capsys.readouterr().out
    assert mc.main(["--data-dir", empty, "--type", "avalon", NANO, "power", "low"]) == 1
    assert "set its Avalon model first" in capsys.readouterr().out
    assert mc.main(["--data-dir", empty, "--type", "avalon", "--model", "nano3s", NANO, "sleep"]) == 1
    assert "Avalon does not support sleep" in capsys.readouterr().out


def test_power_takes_a_level_or_whole_watts(tmp_path, monkeypatch):
    calls = []
    monkeypatch.setattr(mc, "set_power", lambda device, level=None, watts=None: calls.append((level, watts)))
    d = data_dir(tmp_path)
    assert mc.main(["--data-dir", d, "--type", "braiins", NANO, "power", "1200"]) == 0
    assert mc.main(["--data-dir", d, "--type", "avalon", NANO, "power", "low"]) == 0
    assert calls == [(None, 1200), ("low", None)]


@pytest.mark.parametrize("argv", [[NANO, "power"], [NANO, "sleep", "low"], [NANO, "reboot"]])
def test_malformed_commands_are_usage_errors(tmp_path, argv):
    with pytest.raises(SystemExit) as exc:
        mc.main(["--data-dir", data_dir(tmp_path)] + argv)
    assert exc.value.code == 2


def test_info_runs_as_a_script_and_explains_a_missing_avalon_model(tmp_path):
    result = subprocess.run(
        [sys.executable, os.path.join(ROOT, "src", "sentinel", "miner_control.py"),
         "--data-dir", data_dir(tmp_path), "--type", "avalon", NANO, "info"],
        capture_output=True, text=True, timeout=30)
    assert result.returncode == 0, result.stderr
    assert result.stdout.startswith("Avalon at 192.168.1.14 (type avalon)")
    assert "set its Avalon model" in result.stdout

    # With the model given, the hint must not appear (it did on a real Nano 3S)
    result = subprocess.run(
        [sys.executable, os.path.join(ROOT, "src", "sentinel", "miner_control.py"),
         "--data-dir", data_dir(tmp_path), "--type", "avalon", "--model", "nano3s", NANO, "info"],
        capture_output=True, text=True, timeout=30)
    assert result.returncode == 0, result.stderr
    assert "power modes:   low, normal, high" in result.stdout
    assert "set its Avalon model" not in result.stdout


def test_spiralctl_runs_it_as_the_pool_user_against_the_shared_data_dir():
    with open(os.path.join(ROOT, "scripts", "spiralctl.sh"), encoding="utf-8") as f:
        script = f.read()
    arm = re.search(r"\n        control\)\n(.*?)\n            ;;", script, re.S)
    assert arm, "spiralctl miner has no control subcommand"
    assert 'sudo -u "$POOL_USER" python3 "$control_py" --data-dir "${INSTALL_DIR}/data" "$@"' in arm.group(1)
    assert 'control_py="${INSTALL_DIR}/bin/miner_control.py"' in arm.group(1)
