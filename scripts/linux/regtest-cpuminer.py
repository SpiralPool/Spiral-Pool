#!/usr/bin/env python3

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

"""
Minimal stratum CPU miner for regtest testing.

Sends user-agent "esp32/lottery" so Spiral Router classifies it as lottery class
(minDiff=0.0001, initialDiff=0.001), allowing CPU mining at regtest difficulty=1.

Usage:
    python3 regtest-cpuminer.py [host:port] [worker] [threads]
    python3 regtest-cpuminer.py 127.0.0.1:16333 TEST.worker1 4
"""

import hashlib
import json
import multiprocessing
import os
import random
import socket
import struct
import sys
import threading
import time

# Stratum connection
HOST = "127.0.0.1"
PORT = 16333
WORKER = "TEST.worker1"
PASSWORD = "x"
USER_AGENT = "esp32/lottery"  # Triggers lottery class in Spiral Router
THREADS = multiprocessing.cpu_count()

# Parse args
if len(sys.argv) > 1:
    parts = sys.argv[1].split(":")
    HOST = parts[0]
    if len(parts) > 1:
        PORT = int(parts[1])
if len(sys.argv) > 2:
    WORKER = sys.argv[2]
if len(sys.argv) > 3:
    THREADS = int(sys.argv[3])

# Shared state
current_job = None
job_lock = threading.Lock()
shares_accepted = 0
shares_rejected = 0
extranonce1 = ""
extranonce2_size = 4
target = None
sock = None
sock_lock = threading.Lock()
running = True


def sha256d(data):
    """Double SHA-256 hash."""
    return hashlib.sha256(hashlib.sha256(data).digest()).digest()


def send_json(obj):
    """Send JSON-RPC message."""
    global sock
    msg = json.dumps(obj) + "\n"
    with sock_lock:
        try:
            sock.sendall(msg.encode())
        except Exception as e:
            print(f"[ERROR] Send failed: {e}")


def recv_line():
    """Receive a single line from socket."""
    global sock
    buf = b""
    while running:
        try:
            chunk = sock.recv(1)
            if not chunk:
                return None
            if chunk == b"\n":
                return buf.decode().strip()
            buf += chunk
        except socket.timeout:
            continue
        except Exception:
            return None
    return None


def handle_notify(params):
    """Handle mining.notify job."""
    global current_job
    job = {
        "id": params[0],
        "prevhash": params[1],
        "coinb1": params[2],
        "coinb2": params[3],
        "merkle_branch": params[4],
        "version": params[5],
        "nbits": params[6],
        "ntime": params[7],
        "clean": params[8],
    }
    with job_lock:
        current_job = job
    if job["clean"]:
        print(f"[JOB] New job {job['id']} (clean)")
    else:
        print(f"[JOB] New job {job['id']}")


def set_difficulty(diff):
    """Handle mining.set_difficulty."""
    global target
    # target = 2^256 / (diff * 2^32) = 2^224 / diff
    # But for stratum, difficulty 1 means target = 0x00000000ffff... (diff1 target)
    # target = diff1_target / diff
    diff1 = 0x00000000FFFF0000000000000000000000000000000000000000000000000000
    if diff > 0:
        t = int(diff1 / diff)
        target = t.to_bytes(32, "big")
    print(f"[DIFF] Difficulty set to {diff}")


def build_header(job, extranonce2_hex, nonce):
    """Build 80-byte block header."""
    # Build coinbase
    coinbase = bytes.fromhex(job["coinb1"]) + bytes.fromhex(extranonce1) + bytes.fromhex(extranonce2_hex) + bytes.fromhex(job["coinb2"])
    coinbase_hash = sha256d(coinbase)

    # Build merkle root
    merkle_root = coinbase_hash
    for branch in job["merkle_branch"]:
        merkle_root = sha256d(merkle_root + bytes.fromhex(branch))

    # Build header (80 bytes, little-endian)
    header = b""
    header += bytes.fromhex(job["version"])[::-1]  # version (LE)

    # Prevhash: stratum sends each 4-byte group with bytes reversed.
    # Un-swap each 4-byte group to get the real header byte order.
    # Must match pool's validator.go:553-574 (headerPrevHash swap).
    prevhash_stratum = bytes.fromhex(job["prevhash"])
    prevhash_header = bytearray(32)
    for i in range(0, 32, 4):
        prevhash_header[i]   = prevhash_stratum[i+3]
        prevhash_header[i+1] = prevhash_stratum[i+2]
        prevhash_header[i+2] = prevhash_stratum[i+1]
        prevhash_header[i+3] = prevhash_stratum[i]
    header += bytes(prevhash_header)

    header += merkle_root  # merkle root
    header += bytes.fromhex(job["ntime"])[::-1]  # ntime (LE)
    header += bytes.fromhex(job["nbits"])[::-1]  # nbits (LE)
    header += struct.pack("<I", nonce)  # nonce (LE)
    return header


def check_hash(header_bytes):
    """Check if hash meets target."""
    h = sha256d(header_bytes)
    # Compare as big-endian (reverse the hash)
    h_rev = h[::-1]
    return h_rev < target if target else False


def miner_thread(thread_id):
    """Mining thread."""
    global shares_accepted, current_job, running
    hashes = 0
    start = time.time()
    last_report = start

    print(f"[MINER] Thread {thread_id} started")

    while running:
        with job_lock:
            job = current_job

        if job is None or target is None:
            time.sleep(0.1)
            continue

        # Random extranonce2 for this attempt
        en2 = random.randint(0, (1 << (extranonce2_size * 8)) - 1)
        en2_hex = en2.to_bytes(extranonce2_size, "big").hex()

        # Scan nonce range
        nonce_start = random.randint(0, 0xFFFFFFFF - 65536)
        for nonce in range(nonce_start, min(nonce_start + 65536, 0xFFFFFFFF)):
            if not running:
                return

            # Check if job changed
            with job_lock:
                if current_job and current_job["id"] != job["id"]:
                    break

            hashes += 1
            header = build_header(job, en2_hex, nonce)
            if check_hash(header):
                # Found a share!
                # Pool expects nonce as big-endian VALUE hex (like C's %08x).
                # Pool's validator.go:597-607 does hex.Decode then reverseBytes
                # to get LE for header. So "ff9c0c5e" → decode [ff,9c,0c,5e] → reverse [5e,0c,9c,ff].
                # Our header already has struct.pack("<I", 0xff9c0c5e) = [5e,0c,9c,ff]. Match.
                nonce_hex = format(nonce, '08x')
                submit = {
                    "id": random.randint(100, 99999),
                    "method": "mining.submit",
                    "params": [WORKER, job["id"], en2_hex, job["ntime"], nonce_hex],
                }
                send_json(submit)
                print(f"[SHARE] Thread {thread_id} found share! nonce={nonce_hex}")

            # Report hashrate
            now = time.time()
            if now - last_report > 60:
                elapsed = now - start
                rate = hashes / elapsed / 1000
                print(f"[HASH] Thread {thread_id}: {rate:.1f} kH/s ({hashes} hashes)")
                last_report = now


def receiver_thread():
    """Receive and handle stratum messages."""
    global shares_accepted, shares_rejected, extranonce1, extranonce2_size, running

    while running:
        line = recv_line()
        if line is None:
            print("[ERROR] Connection lost")
            running = False
            return

        if not line:
            continue

        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue

        # Handle responses
        if "id" in msg and msg["id"] is not None and "result" in msg:
            msg_id = msg["id"]
            if msg_id == 1:  # subscribe response
                if msg.get("result"):
                    extranonce1 = msg["result"][1]
                    extranonce2_size = msg["result"][2]
                    print(f"[SUB] Subscribed. extranonce1={extranonce1}, en2_size={extranonce2_size}")
            elif msg_id == 2:  # authorize response
                if msg.get("result"):
                    print(f"[AUTH] Authorized as {WORKER}")
                else:
                    print(f"[AUTH] Authorization FAILED: {msg.get('error')}")
                    running = False
            elif msg_id >= 100:  # submit response
                if msg.get("result"):
                    shares_accepted += 1
                    print(f"[ACCEPTED] Share accepted ({shares_accepted} total)")
                else:
                    shares_rejected += 1
                    print(f"[REJECTED] Share rejected: {msg.get('error')} ({shares_rejected} total)")

        # Handle notifications
        if "method" in msg:
            method = msg["method"]
            params = msg.get("params", [])
            if method == "mining.notify":
                handle_notify(params)
            elif method == "mining.set_difficulty":
                set_difficulty(params[0])


def main():
    global sock, running

    print(f"[START] Regtest CPU miner — {THREADS} threads")
    print(f"[START] Pool: {HOST}:{PORT}")
    print(f"[START] Worker: {WORKER}")
    print(f"[START] User-Agent: {USER_AGENT}")
    print()

    # Connect
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(5)
    try:
        sock.connect((HOST, PORT))
    except Exception as e:
        print(f"[ERROR] Connection failed: {e}")
        return

    sock.settimeout(30)
    print(f"[CONN] Connected to {HOST}:{PORT}")

    # Subscribe with lottery user-agent
    send_json({
        "id": 1,
        "method": "mining.subscribe",
        "params": [USER_AGENT],
    })

    # Start receiver
    rx = threading.Thread(target=receiver_thread, daemon=True)
    rx.start()

    # Wait for subscribe response
    time.sleep(1)

    # Authorize
    send_json({
        "id": 2,
        "method": "mining.authorize",
        "params": [WORKER, PASSWORD],
    })

    # Wait for auth + first job
    time.sleep(2)

    if not running:
        return

    # Start miner threads
    miners = []
    for i in range(THREADS):
        t = threading.Thread(target=miner_thread, args=(i,), daemon=True)
        t.start()
        miners.append(t)

    # Wait
    try:
        while running:
            time.sleep(1)
    except KeyboardInterrupt:
        print("\n[STOP] Shutting down...")
        running = False

    print(f"[DONE] Accepted: {shares_accepted}, Rejected: {shares_rejected}")
    sock.close()


if __name__ == "__main__":
    main()
