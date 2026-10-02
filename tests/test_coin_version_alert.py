# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Sentinel warns when a coin daemon falls behind its target version.

upgrade.sh prints a pending-upgrade table once, at the end of a pool upgrade, to
the console. Nothing mentioned it again — so a consensus release landing months
later was invisible. COIN_RISK records that a BC2 node below 31.1.0 "is not
following BC2 mainnet" and that a Litecoin pool on 0.21.5.6 or older "can produce
blocks upgraded nodes reject"; both keep finding blocks while it happens.

Version comparison is deliberately NOT reimplemented here or in Sentinel —
coin-upgrade.sh --list owns it. These tests pin the reporting layer: that the
list is parsed, that silence and "all current" stay distinct, and that a newly
released upgrade is not swallowed by a cooldown opened for an unrelated one.

Run: python -m pytest tests/test_coin_version_alert.py -v
"""
import importlib.util
import os
import tempfile

os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel_coinver",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)


def _state():
    st = sentinel.MonitorState.__new__(sentinel.MonitorState)
    st.last_alerts = {}
    st.upstream_check_failures = {}
    return st


def _fake_list(monkeypatch, stdout, returncode=0):
    """Stand in for `coin-upgrade.sh --list` without running it."""
    class R:
        pass
    r = R()
    r.returncode = returncode
    r.stdout = stdout
    monkeypatch.setattr(sentinel, "INSTALL_DIR", sentinel.Path(os.environ["SPIRALPOOL_INSTALL_DIR"]))
    script = sentinel.INSTALL_DIR / "scripts" / "coin-upgrade.sh"
    script.parent.mkdir(parents=True, exist_ok=True)
    script.write_text("#!/bin/bash\n")
    import subprocess
    monkeypatch.setattr(subprocess, "run", lambda *a, **k: r)
    return r


def _fake_modes(monkeypatch, by_mode, returncode=0):
    """Answer `--list` and `--list-upstream` differently in one run."""
    monkeypatch.setattr(sentinel, "INSTALL_DIR", sentinel.Path(os.environ["SPIRALPOOL_INSTALL_DIR"]))
    script = sentinel.INSTALL_DIR / "scripts" / "coin-upgrade.sh"
    script.parent.mkdir(parents=True, exist_ok=True)
    script.write_text("#!/bin/bash\n")

    class R:
        pass

    def run(cmd, *a, **k):
        r = R()
        mode = cmd[-1]
        r.returncode = returncode
        r.stdout = by_mode.get(mode, "")
        return r

    import subprocess
    monkeypatch.setattr(subprocess, "run", run)


def _capture_alerts(monkeypatch):
    sent = []
    monkeypatch.setattr(sentinel, "send_alert",
                        lambda key, embed, state: sent.append((key, embed)) or True)
    return sent


LIST_MAJOR = "BC2 29.1.0 31.1.0 MAJOR\nLTC 0.21.5.6 0.21.5.8 MAJOR\n"


def test_parses_the_machine_readable_list(monkeypatch):
    _fake_list(monkeypatch, LIST_MAJOR)
    pending = sentinel.get_pending_coin_upgrades()
    assert [p["coin"] for p in pending] == ["BC2", "LTC"]
    assert pending[0] == {"coin": "BC2", "installed": "29.1.0",
                          "target": "31.1.0", "risk": "MAJOR"}


def test_unknown_is_not_all_current(monkeypatch):
    """A failed or missing script must return None, never an empty list.

    [] means "checked, nothing pending" and is reported as such; None means the
    check could not run. Collapsing them would let a broken script read as a
    clean bill of health forever.
    """
    _fake_list(monkeypatch, "", returncode=1)
    assert sentinel.get_pending_coin_upgrades() is None


def test_major_upgrade_alerts_once_then_holds(monkeypatch):
    _fake_list(monkeypatch, LIST_MAJOR)
    sent = _capture_alerts(monkeypatch)
    st = _state()

    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert len(sent) == 1, "a MAJOR pending upgrade must alert"

    # Same set again inside the cooldown: silent.
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert len(sent) == 1, "re-alerted for an unchanged set inside the cooldown"


def test_a_new_release_is_not_swallowed_by_an_open_cooldown(monkeypatch):
    """The cooldown is keyed on the pending set, not the bare alert type."""
    _fake_list(monkeypatch, "LTC 0.21.5.6 0.21.5.8 MAJOR\n")
    sent = _capture_alerts(monkeypatch)
    st = _state()
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert len(sent) == 1

    # A different daemon falls behind while the first cooldown is still open.
    _fake_list(monkeypatch, "LTC 0.21.5.6 0.21.5.8 MAJOR\nBC2 29.1.0 31.1.0 MAJOR\n")
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert len(sent) == 2, "a newly pending daemon was hidden by an open cooldown"


def test_all_current_says_nothing(monkeypatch):
    _fake_list(monkeypatch, "")
    sent = _capture_alerts(monkeypatch)
    st = _state()
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert sent == []


def test_embed_names_the_risk_and_the_command(monkeypatch):
    embed = sentinel.create_coin_upgrade_embed([
        {"coin": "BC2", "installed": "29.1.0", "target": "31.1.0", "risk": "MAJOR"},
    ])
    body = str(embed)
    assert "BC2" in body and "31.1.0" in body
    assert "coin-upgrade.sh" in body, "the operator must be told how to act"


def test_routine_updates_are_not_dressed_as_consensus_risk(monkeypatch):
    embed = sentinel.create_coin_upgrade_embed([
        {"coin": "BCH", "installed": "29.0.0", "target": "29.1.0", "risk": "PATCH"},
    ])
    body = str(embed)
    assert "UPGRADE REQUIRED" not in body


def test_an_unreadable_version_is_not_reported_as_an_upgrade(monkeypatch):
    """A daemon whose version could not be read is not a daemon behind target.

    get_installed_version() in coin-upgrade.sh returns the literal strings
    "unknown" (binary ran, printed no parseable version) and "error" (binary
    produced no output). list_upgrades filters only "not_installed", so those
    two reach --list as the INSTALLED column: `BTC unknown 31.1 MAJOR`. Passed
    through, they render as a red "COIN DAEMON UPGRADE REQUIRED" claiming the
    node "can follow the wrong chain" — about a node that may well be current.
    That is a fabricated consensus verdict built on a failed read, and it is the
    fastest way to teach an operator to ignore this alert.
    """
    _fake_list(monkeypatch, "BTC unknown 31.1 MAJOR\nDGB error 9.26.5 MINOR\n"
                            "LTC 0.21.5.6 0.21.5.8 MAJOR\n")
    pending = sentinel.get_pending_coin_upgrades()
    assert [p["coin"] for p in pending] == ["LTC"], (
        "an unreadable version must not be reported as a pending upgrade")


def test_only_unreadable_versions_is_not_all_current(monkeypatch):
    """Dropping every row must not leave "[] — all current" as the verdict.

    [] is reported to the caller as "checked, nothing pending". If the only rows
    --list produced were unreadable, nothing was established at all, so the
    answer is None (unknown) — the same as a script that would not run.
    """
    _fake_list(monkeypatch, "BTC unknown 31.1 MAJOR\n")
    assert sentinel.get_pending_coin_upgrades() is None


def test_a_version_with_a_build_suffix_still_counts(monkeypatch):
    """The filter must not reject real versions.

    Knots builds report 29.3.knots20260210 and the suffix is deliberately kept
    so a Knots build never compares equal to a Core release — exactly the case
    this alert exists to catch. Rejecting it would silence the one daemon that
    matters most.
    """
    _fake_list(monkeypatch, "BTC 29.3.knots20260210 31.1 MAJOR\n")
    pending = sentinel.get_pending_coin_upgrades()
    assert [p["installed"] for p in pending] == ["29.3.knots20260210"]


def test_a_new_upstream_release_is_reported_even_when_nothing_is_behind(monkeypatch):
    """The gap that made this necessary.

    COIN_TARGET is a static table shipped with each Spiral Pool release. Asking
    only "is this daemon at its target?" answers "yes" forever, no matter what
    upstream publishes afterwards — so a consensus release landing months after
    this version shipped produced no notice at all until the pool was upgraded.
    """
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": "LTC 0.21.5.8 0.22.0\n"})
    sent = _capture_alerts(monkeypatch)
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(_state())
    assert len(sent) == 1, "a newer upstream release must raise the alert on its own"
    body = str(sent[0][1])
    assert "LTC" in body and "0.22.0" in body


def test_upstream_news_is_not_dressed_as_an_upgrade_required(monkeypatch):
    """Not behind the target, so not the red "can follow the wrong chain" alert.

    That verdict is about a daemon below a target Spiral Pool has reviewed.
    Upstream news has not been reviewed, so it gets its own wording.
    """
    embed = sentinel.create_coin_upgrade_embed(
        [], [{"coin": "LTC", "target": "0.21.5.8", "upstream": "0.22.0"}])
    body = str(embed)
    assert "UPGRADE REQUIRED" not in body


def test_upstream_news_never_claims_there_is_nothing_to_do(monkeypatch):
    """The alert that went out for DigiByte 9.26.6.

    It said "nothing is behind and there is nothing to do right now" about a
    consensus release every mining node had to install before block 24,490,000.
    The check compares version numbers and never reads release notes, so it has
    no grounds to call any release routine.
    """
    embed = sentinel.create_coin_upgrade_embed(
        [], [{"coin": "DGB", "target": "9.26.5", "upstream": "9.26.6"}])
    desc = embed["description"]
    assert "nothing to do" not in desc.lower()
    assert "consensus" in desc, "it must say the release may be a required consensus upgrade"
    assert embed["color"] != sentinel.COLORS.get("blue"), "blue reads as informational"


def test_upstream_news_gives_the_commands_in_order(monkeypatch):
    """--coin installs only the target, so upgrade.sh has to come first.

    The operator needs the commands that actually get the release onto the node:
    a Spiral Pool upgrade that moves the target, then the daemon upgrade, with
    the real ticker rather than a placeholder.
    """
    embed = sentinel.create_coin_upgrade_embed(
        [], [{"coin": "DGB", "target": "9.26.5", "upstream": "9.26.6"}])
    desc = embed["description"]
    pool = desc.find("sudo /spiralpool/upgrade.sh")
    coin = desc.find("sudo /spiralpool/scripts/coin-upgrade.sh --coin DGB")
    assert pool != -1 and coin != -1, "both commands must be given"
    assert pool < coin, "the Spiral Pool upgrade must come before the daemon upgrade"


def test_pending_upgrades_name_the_real_ticker(monkeypatch):
    """A <TICKER> placeholder is a command that fails when pasted."""
    embed = sentinel.create_coin_upgrade_embed([
        {"coin": "DGB", "installed": "9.26.5", "target": "9.26.6", "risk": "MAJOR"},
        {"coin": "LTC", "installed": "0.21.5.6", "target": "0.21.5.8", "risk": "MAJOR"},
    ])
    desc = embed["description"]
    assert "<TICKER>" not in desc
    assert "coin-upgrade.sh --coin DGB" in desc and "coin-upgrade.sh --coin LTC" in desc


def test_upstream_and_pending_are_both_reported(monkeypatch):
    _fake_modes(monkeypatch, {"--list": "BC2 29.1.0 31.1.0 MAJOR\n",
                              "--list-upstream": "LTC 0.21.5.8 0.22.0\n"})
    sent = _capture_alerts(monkeypatch)
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(_state())
    body = str(sent[0][1])
    assert "BC2" in body and "LTC" in body
    assert "UPGRADE REQUIRED" in body, "a MAJOR pending upgrade still sets the tone"


def test_a_new_upstream_release_reopens_the_cooldown(monkeypatch):
    """Same reasoning as the pending set: the signature covers upstream too."""
    st = _state()
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": "LTC 0.21.5.8 0.22.0\n"})
    sent = _capture_alerts(monkeypatch)
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert len(sent) == 1

    # Same release again, inside the cooldown: silence.
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert len(sent) == 1

    # A newer one appears: must not be swallowed.
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": "LTC 0.21.5.8 0.23.0\n"})
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(st)
    assert len(sent) == 2, "a newer upstream release was swallowed by an open cooldown"


def test_no_network_is_silence_not_all_current(monkeypatch):
    """A failed upstream check must not be reported, and must not alert."""
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": ""}, returncode=1)
    assert sentinel.get_upstream_coin_releases()[0] is None
    sent = _capture_alerts(monkeypatch)
    sentinel._last_coin_version_check = 0
    sentinel.check_coin_versions(_state())
    assert sent == []


def test_a_feed_that_did_not_answer_is_not_nothing_to_report(monkeypatch):
    """`unreachable X` must parse as "could not check X", not as a release row.

    The script exits 0 when some feeds answer and others do not, so without this
    a partial failure reaches Sentinel as an empty list -- indistinguishable from
    a working check that genuinely found nothing newer.
    """
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": "unreachable DGB\nunreachable LTC\n"})
    rows, unreachable = sentinel.get_upstream_coin_releases()
    assert rows == [], "an unreachable marker must not be read as a release"
    assert unreachable == ["DGB", "LTC"]


def test_unreachable_feeds_are_reported_alongside_real_rows(monkeypatch):
    _fake_modes(monkeypatch, {"--list": "",
                              "--list-upstream": "LTC 0.21.5.8 0.23.0\nunreachable DGB\n"})
    rows, unreachable = sentinel.get_upstream_coin_releases()
    assert [r["coin"] for r in rows] == ["LTC"]
    assert unreachable == ["DGB"]


def test_only_unreachable_feeds_raises_no_alert_but_is_logged(monkeypatch, caplog):
    """Nothing newer plus a dead feed is not an alert, but must not pass silently.

    Alerting every six hours because a feed hiccuped would train the operator to
    ignore this. Saying nothing at all is how a check that has been broken for
    weeks goes unnoticed. So: no alert, one log line.
    """
    import logging
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": "unreachable DGB\n"})
    sent = _capture_alerts(monkeypatch)
    sentinel._last_coin_version_check = 0
    with caplog.at_level(logging.WARNING):
        sentinel.check_coin_versions(_state())
    assert sent == [], "a dead feed must not raise the coin-upgrade alert"
    assert any("no release feed answered for DGB" in r.message for r in caplog.records), \
        "a dead feed must leave a trace in the log"


def test_the_embed_names_the_feeds_it_could_not_check(monkeypatch):
    embed = sentinel.create_coin_upgrade_embed(
        [{"coin": "LTC", "installed": "0.21.5.6", "target": "0.21.5.8", "risk": "MAJOR"}],
        [], ["DGB", "NMC"])
    desc = embed["description"]
    assert "Could not check" in desc
    assert "`DGB`" in desc and "`NMC`" in desc
    assert "not** a statement" in desc, "it must refuse to imply those coins are current"


def _run_upstream(monkeypatch, st, out, times=1):
    """Drive check_coin_versions `times` times with a fixed --list-upstream answer."""
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": out})
    for _ in range(times):
        sentinel._last_coin_version_check = 0
        sentinel.check_coin_versions(st)


def test_one_failed_check_does_not_alert(monkeypatch):
    """A single blip is not an outage. Alerting here trains the operator to mute."""
    st = _state()
    sent = _capture_alerts(monkeypatch)
    _run_upstream(monkeypatch, st, "unreachable DGB\n", times=1)
    assert sent == []
    assert st.upstream_check_failures == {"DGB": 1}


def test_a_feed_dead_past_the_threshold_alerts(monkeypatch):
    st = _state()
    sent = _capture_alerts(monkeypatch)
    threshold = sentinel.CONFIG.get("coin_version_check_fail_threshold", 4)
    _run_upstream(monkeypatch, st, "unreachable DGB\n", times=threshold)
    assert [k for k, _ in sent] == ["coin_version_check_failing"], \
        "a feed dead for the whole threshold must say so"
    desc = sent[0][1]["description"]
    assert "not** a statement" in desc, "it must not imply the daemon is current"
    assert "`DGB`" in desc


def test_a_feed_that_comes_back_resets_the_counter(monkeypatch):
    """Recovery must clear the count, or a flaky feed eventually alerts anyway."""
    st = _state()
    sent = _capture_alerts(monkeypatch)
    threshold = sentinel.CONFIG.get("coin_version_check_fail_threshold", 4)
    _run_upstream(monkeypatch, st, "unreachable DGB\n", times=threshold - 1)
    assert sent == []
    _run_upstream(monkeypatch, st, "", times=1)          # answered, nothing newer
    assert st.upstream_check_failures == {}
    _run_upstream(monkeypatch, st, "unreachable DGB\n", times=threshold - 1)
    assert sent == [], "the counter did not reset when the feed recovered"


def test_a_script_that_cannot_run_does_not_count_against_any_feed(monkeypatch):
    """None means the question was never asked -- that is not a feed failure."""
    st = _state()
    sent = _capture_alerts(monkeypatch)
    threshold = sentinel.CONFIG.get("coin_version_check_fail_threshold", 4)
    _fake_modes(monkeypatch, {"--list": "", "--list-upstream": ""}, returncode=1)
    for _ in range(threshold + 2):
        sentinel._last_coin_version_check = 0
        sentinel.check_coin_versions(st)
    assert st.upstream_check_failures == {}
    assert sent == []


def test_the_failing_alert_survives_a_restart(monkeypatch):
    """The counter is persisted, or a flapping Sentinel never reaches the threshold."""
    assert "upstream_check_failures" in sentinel.MonitorState._PERSIST_KEYS


def test_both_coin_embeds_go_through_the_shared_builder():
    """Hand-building an embed dict silently skips three things _embed() does.

    It truncates to Discord's limits -- an over-length description is a silent
    400, not an error anyone sees -- appends the hostname so an operator running
    more than one box knows which pool is speaking, and stamps the embed. The
    failing-check embed was first written as a literal dict and had none of them.
    `timestamp` is the cheap proof it went through the builder.
    """
    failing = sentinel.create_version_check_failing_embed(["DGB"], 4, 4)
    upgrade = sentinel.create_coin_upgrade_embed(
        [{"coin": "LTC", "installed": "0.21.5.6", "target": "0.21.5.8", "risk": "MAJOR"}])
    for name, e in (("failing-check", failing), ("coin-upgrade", upgrade)):
        assert "timestamp" in e, f"{name} embed did not go through _embed()"
        assert e["footer"]["text"], f"{name} embed has no footer"
        assert len(e["description"]) <= 4096, f"{name} description exceeds the Discord limit"
        assert len(e["title"]) <= 256, f"{name} title exceeds the Discord limit"


def test_the_failing_embed_stays_within_limits_for_every_coin():
    """Fifteen coin names is the worst case; it must still fit."""
    coins = ["BTC", "BCH", "BCH2", "BC2", "BTCS", "DGB", "LTC", "DOGE",
             "PEP", "CAT", "NMC", "SYS", "XMY", "FBTC", "XEC"]
    e = sentinel.create_version_check_failing_embed(coins, 12, 4)
    assert len(e["description"]) <= 4096
    assert all(f"`{c}`" in e["description"] for c in coins), "a coin was dropped from the list"
