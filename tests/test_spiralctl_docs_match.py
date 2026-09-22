# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""What the documentation and the usage text offer is what the code accepts.

Three drifts, each of which hands an operator something that does not work:

  * `docs/reference/spiralctl-reference.md` documents a `spiralctl <command>`
    the dispatcher has no branch for, so it exits "Unknown command".
  * `spiralctl config get|set` prints an "Available keys" list. A key on that
    list with no case branch is rejected as unknown; a key with a branch but
    not on the list cannot be found. `missing_payout_days` and
    `missing_payout_max_days` were in the second state -- implemented,
    accepted, and mentioned in no usage text or document.
  * `install.sh` writes a key into the generated Sentinel config that
    `DEFAULT_CONFIG` does not declare. Seven were in that state, including
    `alerts_enabled`, the master switch for every alert. Behaviour was correct
    -- each reader passes an inline default -- but the structure that is
    supposed to hold the defaults did not mention them, so neither did the
    generated documentation of it.

Each extractor asserts it parsed something. A regex that silently matches
nothing turns a guard into a decoration, and three of these did exactly that
on the first attempt: keying the dispatcher on `cmd_` reported the six
commands that exec the Go binary or call a shell function as missing, and
sweeping the whole script for two-space-indented echoes pulled in every other
subcommand's usage text.

Run: python -m pytest tests/test_spiralctl_docs_match.py -v
"""
import io
import os
import re

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
SPIRALCTL = "scripts/spiralctl.sh"
REFERENCE = "docs/reference/spiralctl-reference.md"


def _read(rel):
    with io.open(os.path.join(ROOT, rel), encoding="utf-8", errors="replace") as fh:
        return fh.read()


def _slice(rel, start_marker, end_marker, after=None):
    text = _read(rel)
    base = text.index(after) if after else 0
    start = text.index(start_marker, base)
    return text[start:text.index(end_marker, start)]


def _dispatch_commands():
    """Every command main() routes, however it is handled.

    `mining|pool|external|gdpr-delete` exec the Go binary and `help`/`version`
    call a shell function, so keying on `cmd_` misses six real commands.
    """
    body = _slice(SPIRALCTL, 'case "$command" in', "\n    esac", after="main() {")
    out = set()
    for m in re.finditer(r"^\s{8}([a-z][a-z0-9|_-]*)\)", body, re.M):
        out.update(m.group(1).split("|"))
    assert len(out) > 20, "the dispatch table did not parse; this check is not running"
    return out


def _documented_commands():
    out = set(re.findall(r"^#{2,4}\s+spiralctl\s+([a-z][a-z0-9_-]*)", _read(REFERENCE), re.M))
    assert len(out) > 10, "no command headings parsed; this check is not running"
    return out


def _config_body():
    """cmd_config's own body, ending at its closing brace in column 0.

    Slicing to the next cmd_config_* function instead spans ~2,900 lines and
    swallows several unrelated functions, whose clear), discord) and
    telegram) branches then read as undocumented config keys.
    """
    text = _read(SPIRALCTL)
    start = text.index("cmd_config() {")
    return text[start:text.index("\n}\n", start)]


def _config_usage_keys():
    """Keys advertised by the "Available keys:"/"Keys:" lists inside cmd_config."""
    out = set()
    for block in re.finditer(
            r'echo "(?:Available k|K)eys:"\n((?:\s*echo "  [^"]*"\n)+)', _config_body()):
        out.update(re.findall(r'echo "  ([a-z_]+)', block.group(1)))
    assert out, "no 'Available keys' list parsed; this check is not running"
    return out


def _config_handled_keys():
    """The primary name of each get/set case branch.

    A branch reads `telegram_token|telegram_bot_token)`: the first label is the
    advertised name, the rest are accepted spellings.
    """
    out = set()
    for m in re.finditer(r"^\s{16}([a-z_][a-z_|]*)\)\s*$", _config_body(), re.M):
        out.add(m.group(1).split("|")[0])
    assert len(out) > 4, "no config key branches parsed; this check is not running"
    return out


def _installer_sentinel_keys():
    """Keys install.sh writes into the Sentinel config specifically.

    Scoped to heredocs redirected at $SENTINEL_CONFIG. The installer also
    writes a miner-scan JSON whose keys (`miners`, `by_type`, `last_scan`)
    have nothing to do with Sentinel's defaults.
    """
    out = set()
    for m in re.finditer(r'"\$SENTINEL_CONFIG"[^\n]*<<\s*\'?EOF\'?\n(.*?)\nEOF\n',
                         _read("install.sh"), re.S):
        out.update(re.findall(r'^\s{2}"([a-z0-9_]+)"\s*:', m.group(1), re.M))
    assert len(out) > 20, "the Sentinel config heredocs did not parse; this check is not running"
    return out


def _default_config_keys():
    m = re.search(r"DEFAULT_CONFIG\s*=\s*\{(.*?)\n\}",
                  _read("src/sentinel/SpiralSentinel.py"), re.S)
    assert m, "DEFAULT_CONFIG did not parse; this check is not running"
    out = set(re.findall(r'^\s*"([a-z0-9_]+)"\s*:', m.group(1), re.M))
    assert len(out) > 50, "DEFAULT_CONFIG parsed too few keys; this check is not running"
    return out


def test_every_documented_command_is_dispatched():
    missing = sorted(_documented_commands() - _dispatch_commands())
    assert not missing, (
        "documented in %s but the dispatcher has no branch, so they exit "
        "'Unknown command': %s" % (REFERENCE, ", ".join(missing)))


def test_advertised_config_keys_are_handled():
    missing = sorted(_config_usage_keys() - _config_handled_keys())
    assert not missing, (
        "listed under 'Available keys' but `spiralctl config set` rejects them "
        "as unknown: %s" % ", ".join(missing))


def test_handled_config_keys_are_advertised():
    hidden = sorted(_config_handled_keys() - _config_usage_keys())
    assert not hidden, (
        "`spiralctl config` accepts these but no usage list mentions them, so "
        "nobody can find them: %s" % ", ".join(hidden))


def test_installer_keys_have_a_declared_default():
    undeclared = sorted(_installer_sentinel_keys() - _default_config_keys())
    assert not undeclared, (
        "install.sh writes these into the Sentinel config but DEFAULT_CONFIG "
        "does not declare them, so the defaults exist only at the call sites: %s"
        % ", ".join(undeclared))
