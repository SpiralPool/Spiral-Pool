#!/usr/bin/env python3
"""Differential fuzz for Spiral Pool's config readers.

Compares the four shell functions that READ and then REWRITE coin configs
against an independent model of Bitcoin Core's parsing rules. The model is
written from Core's documented behaviour, NOT transliterated from the awk under
test -- a transliteration would agree with a shared bug and prove nothing:

  common/config.cpp   GetConfigOptions : truncate each line at the first '#', trim
  common/args.cpp     InterpretBool    : empty => true, else atoi(value) != 0
  common/args.cpp     InterpretValue   : a leading "no" negates; a double
                                         negative flips back ("-nofoo=0" => true)
  common/settings.cpp MergeSettings    : the network section beats the top level;
                                         within a scope the FIRST assignment wins
  dogecoin 1.14 / pepecoin 1.1         : boost config_file_iterator turns
                                         "[main]" into a key PREFIX, so sectioned
                                         keys are unreachable; top level only

WHY THIS MATTERS ON LINUX SPECIFICALLY: Ubuntu and Debian install mawk as
/usr/bin/awk. This was developed against gawk. Every parser here is awk, so a
mawk/gawk difference would silently change how 15 coins' configs are read --
and a wrong answer on `prune` deletes block data irreversibly, while a wrong
answer on `txindex` is a fatal startup error on the pre-0.17 bases (DOGE, PEP).

Usage:
    python3 fuzz_config_readers.py /path/to/repo [cases] [seed]

    python3 fuzz_config_readers.py .                 # 1500 cases, default seed
    python3 fuzz_config_readers.py . 4000            # more cases
    python3 fuzz_config_readers.py . 1500 987654     # a different seed

Exit status is 0 only if every reader matches the model on every case.
Expected on a good tree: all four counters 0.
"""
import os
import random
import shutil
import subprocess
import sys
import tempfile

if len(sys.argv) < 2:
    sys.exit(__doc__)
ROOT = os.path.abspath(sys.argv[1])
N = int(sys.argv[2]) if len(sys.argv) > 2 else 1500
SEED = int(sys.argv[3]) if len(sys.argv) > 3 else 20260815
random.seed(SEED)

POOL_MODE = os.path.join(ROOT, "scripts", "linux", "pool-mode.sh")
SPIRALCTL = os.path.join(ROOT, "scripts", "spiralctl.sh")
for f in (POOL_MODE, SPIRALCTL):
    if not os.path.isfile(f):
        sys.exit(f"not found: {f}\nIs {ROOT} the repo root?")


# ----------------------------------------------------------------- reference
def atoi(v):
    v = v.lstrip()
    i, sign = 0, 1
    if i < len(v) and v[i] in "+-":
        sign = -1 if v[i] == "-" else 1
        i += 1
    j = i
    while j < len(v) and v[j].isdigit():
        j += 1
    return sign * int(v[i:j]) if j > i else 0


def core_parse(text, key, sections_supported):
    section, top, main = "", None, None
    for line in text.split("\n"):
        h = line.find("#")
        if h >= 0:
            line = line[:h]
        line = line.strip()
        if not line:
            continue
        if line.startswith("["):
            section = line[1:].split("]")[0]
            continue
        if "=" not in line:
            continue
        k, _, v = line.partition("=")
        k, v = k.strip(), v.strip()
        if k == "no" + key:
            neg = True
        elif k == key:
            neg = False
        else:
            continue
        if sections_supported:
            if section == "main" and main is None:
                main = (v, neg)
            elif section == "" and top is None:
                top = (v, neg)
        elif section == "" and top is None:
            top = (v, neg)
    return main if main is not None else top


def ref_bool(text, key, sections):
    r = core_parse(text, key, sections)
    if r is None:
        return None
    v, neg = r
    b = 1 if (v == "" or atoi(v) != 0) else 0
    return (1 - b) if neg else b


def ref_prune(text, sections):
    r = core_parse(text, "prune", sections)
    if r is None:
        return None
    v, neg = r
    if neg:
        # A negated option becomes a bool; SettingToInt maps false->0, true->1.
        return 0 if (v == "" or atoi(v) != 0) else 1
    return max(atoi(v), 0)


# ----------------------------------------------------------------- generator
VALUES = ["0", "1", "", "true", "false", "01", "2", "-1", "+5000", "5000",
          "5000M", "abc", "007", "00", "1abc", " 1 "]
KEYS = ["txindex", "notxindex", "prune", "noprune"]
NOISE = ["server=1", "rpcuser=x", "dbcache=450", "nodebuglogfile=1",
         "noconnect=1", "txindexfoo=1", "notxindexfoo=1", "prunexyz=9",
         "# txindex=1", "listen=1", "maxconnections=64"]
SECTS = ["[main]", "[test]", "[regtest]"]


def gen():
    out = []
    for _ in range(random.randint(0, 3)):
        out.append(random.choice(NOISE))
    for _ in range(random.randint(1, 4)):
        if random.random() < 0.25:
            out.append(random.choice(NOISE))
            continue
        k, v = random.choice(KEYS), random.choice(VALUES)
        sp = random.choice(["", " ", "  "])
        lead = random.choice(["", " ", "  "])
        tail = random.choice(["", "  ", "  # note", "#note"])
        out.append(f"{lead}{k}{sp}={sp}{v}{tail}")
        if random.random() < 0.25:
            out.append(random.choice(SECTS))
    if random.random() < 0.15:
        out.insert(0, random.choice(SECTS))
    return "\n".join(out) + ("\n" if random.random() < 0.9 else "")


def extract(path, fn):
    """Pull a shell function body out, honouring its indentation."""
    lines = open(path, encoding="utf-8", newline="").read().split("\n")
    for i, l in enumerate(lines):
        if l.lstrip().startswith(fn + "() {"):
            indent = l[:len(l) - len(l.lstrip())]
            body = [l]
            for j in range(i + 1, len(lines)):
                body.append(lines[j])
                if lines[j] == indent + "}":
                    return "\n".join(body)
    sys.exit(f"could not extract {fn}() from {path}")


WORK = tempfile.mkdtemp(prefix="spiral-fuzz-")
try:
    cases = []
    for n in range(N):
        doge = (n % 4 == 0)          # exercise the no-sections base too
        name = "dogecoin.conf" if doge else "bitcoin.conf"
        d = os.path.join(WORK, f"c{n}")
        os.makedirs(d)
        text = gen()
        with open(os.path.join(d, name), "w", encoding="utf-8", newline="") as fh:
            fh.write(text)
        cases.append((n, f"c{n}/{name}", text, not doge))

    FNS = "\n".join([
        extract(POOL_MODE, "get_existing_txindex"),
        extract(POOL_MODE, "get_existing_prune"),
        extract(SPIRALCTL, "_conf_bool"),
    ])

    runner = os.path.join(WORK, "run.sh")
    with open(runner, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(
            'set -u\n: "${BASE:?}"\nSPIRALPOOL_DIR=/nonexistent\nYELLOW=""; NC=""\n'
            + FNS + "\n"
            'while IFS="|" read -r n rel; do\n'
            '  conf="$BASE/$rel"\n'
            '  [ -f "$conf" ] || { printf "%s|MISSING||\\n" "$n"; continue; }\n'
            '  get_existing_txindex "$conf" 0\n'
            '  get_existing_prune "$conf"\n'
            '  b=$(_conf_bool "$conf" txindex)\n'
            '  printf "%s|%s|%s|%s\\n" "$n" "$EXISTING_TXINDEX" "$EXISTING_PRUNE" "$b"\n'
            'done\n')

    # BYTES, not text=True: text mode would translate "\n" to os.linesep and the
    # paths would reach the shell with a trailing CR.
    stdin = ("\n".join(f"{n}|{rel}" for n, rel, _, _ in cases) + "\n").encode()
    res = subprocess.run(["bash", runner], input=stdin, capture_output=True,
                         cwd=WORK, env={**os.environ, "BASE": WORK})
    out = res.stdout.decode("utf-8", "replace")
    err = res.stderr.decode("utf-8", "replace")
    if res.returncode != 0:
        sys.exit("harness failed: " + err[:600])

    got = {}
    for line in out.strip().split("\n"):
        p = line.split("|")
        if len(p) == 4:
            got[int(p[0])] = (p[1], p[2], p[3])

    missing = sum(1 for v in got.values() if v[0] == "MISSING")
    if missing:
        sys.exit(f"{missing} configs unreadable by the harness — the run is invalid")
    if len(got) != len(cases):
        sys.exit(f"only {len(got)} of {len(cases)} cases returned — the run is invalid")

    d_txi = d_pru = d_bool = d_agree = 0
    ex = []
    for n, rel, text, sections in cases:
        g_txi, g_pru, g_bool = got[n]
        rb = ref_bool(text, "txindex", sections)
        want_txi = "" if rb is None else ("txindex=1" if rb else "txindex=0")
        if g_txi != want_txi:
            d_txi += 1
            if len(ex) < 8:
                ex.append(("get_existing_txindex", text, g_txi, want_txi))
        rp = ref_prune(text, sections)
        want_pru = "0" if rp is None else str(rp)
        if g_pru != want_pru:
            d_pru += 1
            if len(ex) < 8:
                ex.append(("get_existing_prune", text, g_pru, want_pru))
        want_b = "" if rb is None else str(rb)
        if g_bool != want_b:
            d_bool += 1
            if len(ex) < 8:
                ex.append(("_conf_bool", text, g_bool, want_b))
        if {"1": "txindex=1", "0": "txindex=0", "": ""}.get(g_bool, "?") != g_txi:
            d_agree += 1
            if len(ex) < 8:
                ex.append(("READERS DISAGREE", text, f"bool={g_bool}", f"txi={g_txi}"))

    awkver = subprocess.run(["bash", "-c", "awk -W version 2>&1 | head -1 || awk --version 2>&1 | head -1"],
                            capture_output=True, text=True).stdout.strip()
    print(f"awk      : {awkver}")
    print(f"seed={SEED}  cases={len(got)}  (1 in 4 uses the no-sections dogecoin base)")
    print(f"  get_existing_txindex  vs Core model : {d_txi}")
    print(f"  get_existing_prune    vs Core model : {d_pru}")
    print(f"  _conf_bool            vs Core model : {d_bool}")
    print(f"  the two readers vs EACH OTHER       : {d_agree}")
    for kind, text, g, w in ex:
        print(f"\n  [{kind}] got={g!r} want={w!r}")
        print("    " + text.replace("\n", "\n    ").rstrip())

    sys.exit(1 if (d_txi or d_pru or d_bool or d_agree) else 0)
finally:
    shutil.rmtree(WORK, ignore_errors=True)
