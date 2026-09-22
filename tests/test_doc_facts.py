# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""The countable facts the documentation states are recomputed from the tree.

Cross-reference checking proves a link resolves. It cannot prove a *number* is
still true, and these numbers rot silently: nothing reads them except a person
deciding whether to trust the document around them.

They have rotted repeatedly. `docs/reference/SENTINEL.md` described
`SpiralSentinel.py` as ~20,700 lines when it was 23,763 -- three thousand lines
of drift, in a figure that a previous release had already corrected once, from
~19,500 to ~20,700. `install.sh` was described as ~36,500 lines at 41,850. The
test-suite totals in `TESTING.md` were 240 files and 3,500+ functions against
285 and 3,889.

Figures written with a leading `~` are approximations and are checked to within
5%, which passes normal growth and fails the kind of drift above. Figures
written exactly are checked exactly.

Adding a test file or a router pattern will fail this suite. That is the point:
update the number in the document in the same commit.

Run: python -m pytest tests/test_doc_facts.py -v
"""
import io
import os
import re

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))


def _read(rel):
    with io.open(os.path.join(ROOT, rel), encoding="utf-8", errors="replace") as fh:
        return fh.read()


def _lines(rel):
    return len(_read(rel).split("\n")) - 1


def _stated(rel, pattern):
    m = re.search(pattern, _read(rel))
    assert m, "%s no longer contains the claim matched by %r -- if the wording " \
              "changed, update this test rather than deleting the check" % (rel, pattern)
    return int(m.group(1).replace(",", ""))


def _approx(stated, actual, what):
    drift = abs(stated - actual) / float(actual)
    assert drift <= 0.05, (
        "%s: the documentation says ~%s, the tree has %s (%.1f%% out)"
        % (what, format(stated, ","), format(actual, ","), drift * 100))


def _exact(stated, actual, what):
    assert stated == actual, (
        "%s: the documentation says %s, the tree has %s"
        % (what, format(stated, ","), format(actual, ",")))


def _go_test_files():
    n = 0
    for base, dirs, files in os.walk(os.path.join(ROOT, "src")):
        dirs[:] = [d for d in dirs if d not in ("node_modules", "__pycache__")]
        n += sum(1 for f in files if f.endswith("_test.go"))
    return n


def _python_test_files():
    return len([f for f in os.listdir(os.path.join(ROOT, "tests"))
                if f.startswith("test_") and f.endswith(".py")])


def _go_funcs(prefixes):
    pat = re.compile(r"^func (?:%s)[A-Za-z0-9_]*\(" % "|".join(prefixes), re.M)
    n = 0
    for base, dirs, files in os.walk(os.path.join(ROOT, "src")):
        dirs[:] = [d for d in dirs if d not in ("node_modules", "__pycache__")]
        for f in files:
            if f.endswith("_test.go"):
                n += len(pat.findall(
                    io.open(os.path.join(base, f), encoding="utf-8", errors="replace").read()))
    return n


def _router_patterns():
    """Entries in the user-agent table in spiralrouter.go's `patterns := []struct`."""
    text = _read("src/stratum/internal/stratum/spiralrouter.go").split("\n")
    start = next(i for i, l in enumerate(text) if l.strip().startswith("patterns := []struct"))
    # the table opens at `}{` and closes on the first line that is a lone tab-brace
    n, opened = 0, False
    for line in text[start:]:
        if not opened:
            opened = line.rstrip().endswith("}{")
            continue
        if line == "\t}":
            break
        if line.startswith("\t\t{"):
            n += 1
    assert n > 10, "the router pattern table did not parse; this check is not running"
    return n


def test_sentinel_source_line_count():
    _approx(_stated("docs/reference/SENTINEL.md",
                    r"SpiralSentinel\.py`\s*\(~([\d,]+) lines\)"),
            _lines("src/sentinel/SpiralSentinel.py"),
            "docs/reference/SENTINEL.md SpiralSentinel.py line count")


def test_installer_line_count():
    _approx(_stated("docs/setup/OPERATIONS.md",
                    r"self-contained script \(~([\d,]+) lines\)"),
            _lines("install.sh"),
            "docs/setup/OPERATIONS.md install.sh line count")


def test_go_test_file_count():
    _exact(_stated("docs/development/TESTING.md", r"Total Test Files\*\*:\s*([\d,]+) Go"),
           _go_test_files(), "docs/development/TESTING.md Go test files")


def test_python_test_file_count():
    _exact(_stated("docs/development/TESTING.md", r"\+\s*([\d,]+) Python"),
           _python_test_files(), "docs/development/TESTING.md Python test files")


def test_go_test_function_count():
    _exact(_stated("docs/development/TESTING.md", r"Total Test Functions\*\*:\s*([\d,]+) Go"),
           _go_funcs(["Test"]), "docs/development/TESTING.md Go Test* functions")


def test_go_test_function_count_including_fuzz_and_bench():
    _exact(_stated("docs/development/TESTING.md", r"\(([\d,]+) including `Fuzz\*`"),
           _go_funcs(["Test", "Fuzz", "Benchmark"]),
           "docs/development/TESTING.md Test*+Fuzz*+Benchmark* functions")


def test_safe_num_test_count():
    # the tests are methods on classes, so they are indented
    actual = len(re.findall(r"^\s*def test_", _read("tests/test_safe_num.py"), re.M))
    _exact(_stated("docs/development/TESTING.md", r"test_safe_num\.py`\s*—\s*([\d,]+) tests"),
           actual, "docs/development/TESTING.md test_safe_num.py test count")


def test_router_pattern_count_in_architecture():
    _exact(_stated("docs/architecture/ARCHITECTURE.md", r"([\d,]+) verified regex patterns"),
           _router_patterns(), "docs/architecture/ARCHITECTURE.md router patterns")


def test_router_pattern_count_in_operations():
    _exact(_stated("docs/setup/OPERATIONS.md", r"against ([\d,]+) verified regex patterns"),
           _router_patterns(), "docs/setup/OPERATIONS.md router patterns")


# ---------------------------------------------------------------------------
# Documented config defaults
#
# The counts above are one kind of claim. The larger kind is the 118-row
# "| key | type | default | description |" tables in SENTINEL.md, which tell an
# operator what a setting does and what it defaults to. Nothing compared them
# to DEFAULT_CONFIG, and they had drifted in two ways at once: twelve
# documented keys were absent from DEFAULT_CONFIG entirely (read with an inline
# default at one call site each), and `smtp_to` was documented as a list
# defaulting to `[]` when Sentinel reads it as a comma-separated string --
# following that table gives a JSON array, and `CONFIG.get("smtp_to",
# "").strip()` raises AttributeError at import, taking the daemon down.
# ---------------------------------------------------------------------------

DOC_DEFAULT_ROW = re.compile(
    r"^\| `([a-z0-9_]+)` \| (bool|int|float|string|str|list|dict) \| `([^`]*)` \|", re.M)


def _sentinel_module():
    import importlib.util
    import tempfile
    os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
    spec = importlib.util.spec_from_file_location(
        "spiral_sentinel_docfacts", os.path.join(ROOT, "src", "sentinel", "SpiralSentinel.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _documented_defaults():
    rows = DOC_DEFAULT_ROW.findall(_read("docs/reference/SENTINEL.md"))
    assert len(rows) > 100, "the config tables did not parse; this check is not running"
    return rows


def _literal(raw):
    import json
    raw = raw.strip()
    if raw in ("true", "false"):
        return raw == "true"
    try:
        return json.loads(raw)
    except ValueError:
        return raw


def test_documented_config_keys_exist():
    declared = _sentinel_module().DEFAULT_CONFIG
    missing = sorted({k for k, _, _ in _documented_defaults() if k not in declared})
    assert not missing, (
        "documented in docs/reference/SENTINEL.md with a default, but absent from "
        "DEFAULT_CONFIG, so the stated default is not declared anywhere: %s"
        % ", ".join(missing))


def test_documented_config_defaults_match_the_code():
    declared = _sentinel_module().DEFAULT_CONFIG
    wrong = []
    for key, _type, raw in _documented_defaults():
        if key not in declared:
            continue  # reported by the test above
        want, have = _literal(raw), declared[key]
        if isinstance(want, (int, float)) and isinstance(have, (int, float)) \
                and not isinstance(want, bool) and not isinstance(have, bool):
            if abs(float(want) - float(have)) < 1e-9:
                continue
        elif want == have and type(want) is type(have):
            continue
        wrong.append("%s: documented %r, code has %r" % (key, want, have))
    assert not wrong, (
        "docs/reference/SENTINEL.md states defaults the code does not use:\n  "
        + "\n  ".join(wrong))


def _dashboard_routes():
    """Distinct URL rules across the dashboard's Flask modules."""
    rules = set()
    for rel in ("src/dashboard/dashboard.py", "src/dashboard/explorer.py"):
        path = os.path.join(ROOT, rel)
        if not os.path.exists(path):
            continue
        for m in re.finditer(r"@(?:app|[a-z_]+_bp|bp)\.route\(\s*[\"']([^\"']+)", _read(rel)):
            rules.add(m.group(1))
    assert len(rules) > 50, "no Flask routes parsed; this check is not running"
    return len(rules)


def test_dashboard_route_count():
    """INDEX.md advertised ~155 routes against 165. Approximate, so 5%."""
    _approx(_stated("docs/INDEX.md", r"~([\d,]+) API routes"),
            _dashboard_routes(), "docs/INDEX.md dashboard API routes")


def test_theme_count():
    themes = len([f for f in os.listdir(os.path.join(ROOT, "src", "dashboard", "static", "themes"))
                  if f.endswith(".json")])
    for rel, pattern in (("docs/INDEX.md", r"([\d]+) themes"),
                         ("docs/reference/DASHBOARD.md", r"([\d]+) themes available"),
                         ("README.md", r"([\d]+) themes")):
        _exact(_stated(rel, pattern), themes, "%s theme count" % rel)
