# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Every place that tells an operator how much disk a coin needs says the same number.

Five surfaces publish a per-coin storage figure and they had drifted apart:

  * `install-windows.ps1` contradicted **itself** -- its `$CoinConfig` table
    said BCH2 20 GB and BTCS 15 GB while its own menu, printed on the screen
    the operator is looking at while choosing, said 15 GB and 8 GB.
  * `docs/setup/OPERATIONS.md` was alone in saying BC2 needs 5 GB, against
    10 GB everywhere else.
  * OPERATIONS and CLOUD_OPERATIONS said Myriad 6 GB, against 8 GB in the
    installer and the Windows guide.

Nothing catches this: each file is internally valid, and an operator reads
exactly one of them before provisioning a disk.

`install.sh` is deliberately NOT one of the surfaces checked here. It states
raw chain size and adds its own headroom to compute `REQUIRED_GB`, the
threshold its preflight actually enforces -- a different quantity from the
provisioning figure published everywhere else, and one validated by real
installs. Pulling it into this comparison would mean changing the number that
gates installation, which is not a documentation change.

To change a figure, change it in every source below; this test names the ones
that disagree.

Run: python -m pytest tests/test_coin_storage_consistency.py -v
"""
import io
import os
import re

import pytest

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

# Syscoin is published as "25 GB + NEVM state" in prose and plain "25 GB" in
# the installer field; the NEVM qualifier is the point of the sentence, so the
# comparison is on the leading number only.
COINS = ("DGB", "BTC", "BCH", "BCH2", "BC2", "BTCS", "NMC", "SYS",
         "XMY", "FBTC", "XEC", "LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT")

NAMES = {
    "DGB": "DigiByte", "BTC": "Bitcoin", "BCH": "Bitcoin Cash",
    "BCH2": "Bitcoin Cash II", "BC2": "Bitcoin II", "BTCS": "Bitcoin Silver",
    "NMC": "Namecoin", "SYS": "Syscoin", "XMY": "Myriad", "FBTC": "Fractal Bitcoin",
    "XEC": "eCash", "LTC": "Litecoin", "DOGE": "Dogecoin",
    "DGB-SCRYPT": "DigiByte (Scrypt)", "PEP": "PepeCoin", "CAT": "Catcoin",
}


def _read(rel):
    with io.open(os.path.join(ROOT, rel), encoding="utf-8", errors="replace") as fh:
        return fh.read()


def _ps1_coinconfig():
    """Storage="NN GB" inside the $CoinConfig hashtable, keyed by symbol."""
    out = {}
    for line in _read("install-windows.ps1").split("\n"):
        m = re.match(r'\s*"?([A-Z0-9-]+)"?\s*=\s*@\{.*Storage="(\d+) GB"', line)
        if m:
            out[m.group(1)] = int(m.group(2))
    return out


def _ps1_menu():
    """The '~NN GB' printed beside each coin in the interactive menu."""
    out, text = {}, _read("install-windows.ps1")
    for m in re.finditer(
        r'Write-Host "([A-Z0-9-]+)\s+- [^"]*"[^\n]*\n\s*Write-Host "[^"]*?~(\d+) GB', text):
        out.setdefault(m.group(1), int(m.group(2)))
    return out


def _ps1_help():
    """The '<SYM> (~NNGB)' list in the comment-based help block."""
    out = {}
    for m in re.finditer(r"([A-Z0-9-]+) \(~(\d+)GB\)", _read("install-windows.ps1")):
        out.setdefault(m.group(1), int(m.group(2)))
    return out


def _windows_guide():
    out = {}
    for line in _read("docs/setup/WINDOWS_GUIDE.md").split("\n"):
        m = re.match(r"\|\s*\d+\s*\|\s*([A-Z0-9-]+)\s*\|[^|]*\|[^|]*\|\s*~(\d+) GB", line)
        if m:
            out[m.group(1)] = int(m.group(2))
    return out


def _operations():
    out = {}
    for line in _read("docs/setup/OPERATIONS.md").split("\n"):
        m = re.match(r"\|\s*[^|]+\|\s*([A-Z0-9-]+)\s*\|\s*(\d+) GB", line)
        if m:
            out[m.group(1)] = int(m.group(2))
    return out


def _cloud():
    out = {}
    for line in _read("docs/setup/CLOUD_OPERATIONS.md").split("\n"):
        m = re.match(r"\|\s*[^|(]+\(([A-Z0-9-]+)\)\s*\|\s*~(\d+) GB", line)
        if m:
            out[m.group(1)] = int(m.group(2))
    return out


def _manifest():
    """config/coins.manifest.yaml, which calls itself the canonical source for
    coin definitions and now carries the figure with its provenance."""
    import yaml
    with io.open(os.path.join(ROOT, "config", "coins.manifest.yaml"),
                 encoding="utf-8") as fh:
        data = yaml.safe_load(fh)
    return {c["symbol"]: int(c["storage"]["chain_gb"]) for c in data["coins"]}


SOURCES = {
    "config/coins.manifest.yaml": _manifest,
    "install-windows.ps1 $CoinConfig": _ps1_coinconfig,
    "install-windows.ps1 menu": _ps1_menu,
    "install-windows.ps1 help": _ps1_help,
    "docs/setup/WINDOWS_GUIDE.md": _windows_guide,
    "docs/setup/OPERATIONS.md": _operations,
    "docs/setup/CLOUD_OPERATIONS.md": _cloud,
}


@pytest.mark.parametrize("symbol", COINS)
def test_storage_figure_agrees_across_sources(symbol):
    stated = {}
    for label, fn in SOURCES.items():
        value = fn().get(symbol)
        if value is not None:
            stated[label] = value
    assert stated, "no source publishes a storage figure for %s" % symbol
    distinct = set(stated.values())
    assert len(distinct) == 1, (
        "%s (%s) is published with conflicting storage figures:\n  %s"
        % (symbol, NAMES.get(symbol, symbol),
           "\n  ".join("%-34s %s GB" % (k, v) for k, v in sorted(stated.items())))
    )


def test_every_source_was_actually_parsed():
    """A regex that silently matches nothing turns this file into a no-op."""
    empty = [label for label, fn in SOURCES.items() if len(fn()) < 10]
    assert not empty, (
        "these sources parsed to fewer than 10 coins, so the comparison above "
        "is not really running: " + ", ".join(empty))


def test_every_coin_declares_where_its_figure_came_from():
    """A number with no provenance is indistinguishable from a guess.

    Eight of these chains publish no size statistic anywhere, and the figures
    for them are estimates. That has to be visible in the data rather than
    only in a paragraph of prose, so `measured: null` and `source: estimate`
    say so explicitly and this test refuses a coin that declares neither.
    """
    import yaml
    with io.open(os.path.join(ROOT, "config", "coins.manifest.yaml"),
                 encoding="utf-8") as fh:
        coins = yaml.safe_load(fh)["coins"]
    bad = []
    for c in coins:
        st = c.get("storage") or {}
        if "chain_gb" not in st or "measured" not in st or not st.get("source"):
            bad.append(c["symbol"])
    assert not bad, (
        "coins whose storage figure declares no provenance (need chain_gb, "
        "measured and source): " + ", ".join(bad))
