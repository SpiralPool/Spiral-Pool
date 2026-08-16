"""Proof tests for the v2.7.0 BTC chain-identity alert in SpiralSentinel.

On 2026-08-08 Bitcoin split at block 961,632 over BIP-110 ("RDTS"). A node on the
minority chain looks completely healthy — RPC responds, sync reads 100%, hashrate
is normal — and the only symptom is that blocks never arrive. check_chain_identity
is the alert that catches it, so these tests pin the behaviour that makes it
trustworthy rather than noisy:

  - fires on BIP-110 enforcement, on a block 961,632 mismatch, and on a stale tip
  - stays SILENT when the daemon is unreachable or still syncing below the split
    height (unknown is not a wrong-chain verdict, and crying wolf here would
    train operators to ignore the one alert that matters)
  - never sends the wrong-chain text for a node that is merely stalled on the
    CORRECT chain — wrong diagnosis and wrong remedy
  - is off-mainnet aware, so regtest/testnet never trip it
  - only starts its cooldown when the alert was actually delivered, while still
    throttling the journal line so a suppressed alert cannot flood the log
  - can be disabled by operators who deliberately mine a non-majority chain

Run: python -m pytest tests/test_chain_identity.py -v
"""
import importlib.util
import json
import logging
import os
import tempfile

os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

MAJORITY = sentinel.BITCOIN_MAJORITY_BLOCK_961632
RDTS = sentinel.BITCOIN_RDTS_BLOCK_961632
SPLIT = sentinel.BITCOIN_SPLIT_HEIGHT


def _state():
    """A bare MonitorState with just the alert bookkeeping the check touches."""
    s = sentinel.MonitorState.__new__(sentinel.MonitorState)
    s.last_alerts = {}
    return s


class _Harness:
    """Drives check_chain_identity with a scripted daemon."""

    def __init__(self, monkeypatch, *, chain="main", blocks=SPLIT + 900,
                 block_hash=MAJORITY, deployments=None, tip_age_secs=600,
                 reachable=True, delivered=True, stamps_before_send=False):
        self.sent = []
        self.delivered = delivered
        self.stamps_before_send = stamps_before_send
        now = 1_755_000_000
        self.now = now

        def fake_rpc(host, port, method, params=None, timeout=10, auth=None):
            if not reachable:
                return None
            if method == "getblockchaininfo":
                return {"chain": chain, "blocks": blocks,
                        "mediantime": now - tip_age_secs}
            if method == "getdeploymentinfo":
                return {"deployments": deployments or {}}
            if method == "getblockhash":
                return block_hash
            return None

        def fake_send(alert_type, embed, state=None, miner_name=None):
            self.sent.append((alert_type, embed))
            # The real send_alert keeps its OWN rate limiter, keyed on the bare
            # alert_type, and stamps it BEFORE attempting delivery — so the
            # stamp survives a transport failure. Reproduce that when asked,
            # because it is the interaction that silently defeated the retry.
            if self.stamps_before_send and state is not None:
                state.last_alerts[alert_type] = self.now
            return self.delivered

        monkeypatch.setattr(sentinel, "_rpc_call", fake_rpc)
        monkeypatch.setattr(sentinel, "send_alert", fake_send)
        monkeypatch.setattr(sentinel, "get_coin_by_symbol",
                            lambda sym: {"symbol": "BTC", "rpc_port": 8332, "enabled": True})
        monkeypatch.setattr(sentinel, "time", type("T", (), {"time": staticmethod(lambda: now)}))

    @property
    def fired(self):
        return bool(self.sent)

    @property
    def calls(self):
        return len(self.sent)

    @property
    def embed(self):
        return self.sent[0][1] if self.sent else None


class TestFires:
    """Conditions that must produce an alert."""

    def test_rdts_enforcement_detected(self, monkeypatch):
        h = _Harness(monkeypatch, deployments={"reduced_data": {}, "segwit": {}})
        sentinel.check_chain_identity(_state())
        assert h.fired, "BIP-110 enforcement must alert"
        assert "wrong chain" in h.embed["title"].lower()

    def test_block_hash_is_the_rdts_chain(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash=RDTS)
        sentinel.check_chain_identity(_state())
        assert h.fired
        # The alert should name the chain, not just say "not the right one".
        assert "BIP-110" in h.embed["description"]

    def test_block_hash_matches_no_known_chain(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash="00" * 32)
        sentinel.check_chain_identity(_state())
        assert h.fired


class TestStaysSilent:
    """Conditions that must NOT alert. Each of these firing would be a false
    positive on the release's headline alert."""

    def test_healthy_majority_chain_node(self, monkeypatch):
        h = _Harness(monkeypatch)
        sentinel.check_chain_identity(_state())
        assert not h.fired

    def test_daemon_unreachable(self, monkeypatch):
        h = _Harness(monkeypatch, reachable=False)
        sentinel.check_chain_identity(_state())
        assert not h.fired, "unreachable is unknown, not wrong-chain"

    def test_still_syncing_below_split_height(self, monkeypatch):
        h = _Harness(monkeypatch, blocks=SPLIT - 5000, block_hash=None)
        sentinel.check_chain_identity(_state())
        assert not h.fired, "a node that cannot reach the split height is unknown"

    def test_non_dict_rpc_reply_does_not_raise(self, monkeypatch):
        """A raise here would abort every later check in the monitor cycle."""
        monkeypatch.setattr(sentinel, "_rpc_call",
                            lambda *a, **k: "unexpected string")
        monkeypatch.setattr(sentinel, "get_coin_by_symbol",
                            lambda sym: {"symbol": "BTC", "rpc_port": 8332})
        sentinel.check_chain_identity(_state())  # must simply return

    def test_regtest_and_testnet_are_exempt(self, monkeypatch):
        """Block 961,632 is a mainnet constant; off mainnet it does not exist."""
        for chain in ("regtest", "test", "signet"):
            h = _Harness(monkeypatch, chain=chain, blocks=101,
                         deployments={"reduced_data": {}})
            sentinel.check_chain_identity(_state())
            assert not h.fired, f"{chain} must not trip the mainnet split check"

    def test_disabled_by_config(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash=RDTS)
        monkeypatch.setitem(sentinel.CONFIG, "chain_identity_enabled", False)
        sentinel.check_chain_identity(_state())
        assert not h.fired, "operators who choose a minority chain must be able to mute this"


class TestStaleTipIsADifferentProblem:
    """A stalled node on the CORRECT chain must not be told its blocks are
    worthless and to migrate its daemon."""

    def test_stale_tip_alerts_with_its_own_wording(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash=MAJORITY, tip_age_secs=6 * 3600)
        sentinel.check_chain_identity(_state())
        assert h.fired
        desc = h.embed["description"]
        assert "not the chain-split problem" in desc.lower()
        assert "coin-upgrade.sh" not in desc, "wrong remedy for a stalled node"
        assert "worthless" not in desc.lower()

    def test_fresh_tip_on_majority_chain_is_silent(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash=MAJORITY, tip_age_secs=600)
        sentinel.check_chain_identity(_state())
        assert not h.fired


class TestTransportFailureRetry:
    def test_transport_failure_does_not_consume_the_cooldown(self, monkeypatch):
        """A Discord outage must not buy 6 hours of silence.

        send_alert stamps its own rate limiter, keyed on the bare alert type,
        before the transport runs. Left alone, that stamp makes every retry die
        inside send_alert's limiter even after the transport recovers — so one
        transient failure silences the release's headline alert for a full
        cooldown. check_chain_identity has to undo the premature stamp.
        """
        h = _Harness(monkeypatch, block_hash=RDTS, delivered=False,
                     stamps_before_send=True)
        st = _state()
        sentinel.check_chain_identity(st)

        assert h.fired, "delivery was attempted"
        assert "chain_identity:BTC" not in st.last_alerts, \
            "a failed delivery must not start the alert cooldown"
        assert "chain_identity" not in st.last_alerts, \
            "send_alert's premature stamp must be undone so the retry can proceed"

    def test_a_legitimate_prior_cooldown_is_preserved(self, monkeypatch):
        """Undoing that stamp must not clobber a real cooldown.

        send_alert also returns False when it is genuinely rate-limited by an
        earlier SUCCESSFUL send. That stamp has to survive, or the alert would
        escape its own cooldown and repeat.
        """
        h = _Harness(monkeypatch, block_hash=RDTS, delivered=False)
        st = _state()
        earlier = h.now - 60
        st.last_alerts["chain_identity"] = earlier
        sentinel.check_chain_identity(st)

        assert st.last_alerts.get("chain_identity") == earlier, \
            "a cooldown from an earlier successful send must be left intact"

    def test_delivery_success_still_starts_the_cooldown(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash=RDTS, delivered=True,
                     stamps_before_send=True)
        st = _state()
        sentinel.check_chain_identity(st)
        assert st.last_alerts.get("chain_identity:BTC") == h.now


class TestBelowSplitEnforcement:
    def test_enforcing_below_split_does_not_claim_an_unreadable_block(self, monkeypatch):
        """A node enforcing RDTS that has not reached 961,632 yet.

        The verdict is conclusive and worth alerting on, but there is no block
        961,632 to read — and the alert must not imply the check failed, nor
        state as present fact something the node has not reached.
        """
        h = _Harness(monkeypatch, blocks=SPLIT - 5000,
                     deployments={"reduced_data": {"type": "bip9"}})
        st = _state()
        sentinel.check_chain_identity(st)

        assert h.fired, "enforcement is conclusive even below the split height"
        body = json.dumps(h.embed)
        assert "unreadable" not in body, \
            "must not render an unreadable hash for a block it cannot have"
        assert "will follow" in body, \
            "wording must be future-tense below the split height"


class TestLogThrottle:
    def test_suppressed_alert_does_not_log_every_cycle(self, monkeypatch, caplog):
        """A suppressed alert is retried every cycle by design. The journal line
        must not be, or a quiet-hours window produces thousands of ERROR lines."""
        h = _Harness(monkeypatch, block_hash=RDTS, delivered=False)
        st = _state()
        with caplog.at_level(logging.ERROR):
            for _ in range(25):
                sentinel.check_chain_identity(st)
        assert h.calls == 25, "send_alert still retried on every cycle"
        errors = [r for r in caplog.records if "CHAIN IDENTITY" in r.getMessage()]
        assert len(errors) == 1, f"expected 1 throttled log line, got {len(errors)}"


class TestCooldown:
    def test_cooldown_only_starts_when_the_alert_was_delivered(self, monkeypatch):
        """send_alert suppresses on several paths. Stamping the latch on a
        suppressed alert buys hours of silence for a message nobody received."""
        h = _Harness(monkeypatch, block_hash=RDTS, delivered=False)
        st = _state()
        sentinel.check_chain_identity(st)
        assert h.fired, "send_alert was called"
        # The journal-throttle latch is a separate key and IS stamped
        # unconditionally; only the alert cooldown must stay unset so the
        # suppressed alert is retried on the next cycle.
        assert "chain_identity:BTC" not in st.last_alerts,             "suppressed, so no alert cooldown may be recorded"

    def test_cooldown_recorded_on_delivery(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash=RDTS, delivered=True)
        st = _state()
        sentinel.check_chain_identity(st)
        assert st.last_alerts.get("chain_identity:BTC"), "delivered alert must start the cooldown"

    def test_second_call_within_cooldown_is_suppressed(self, monkeypatch):
        h = _Harness(monkeypatch, block_hash=RDTS)
        st = _state()
        sentinel.check_chain_identity(st)
        assert len(h.sent) == 1
        sentinel.check_chain_identity(st)
        assert len(h.sent) == 1, "cooldown must hold within the window"


class TestRegistration:
    """The alert must be wired into the notification machinery, not just defined."""

    def test_bypasses_quiet_hours(self):
        assert sentinel.ALERT_BYPASS_QUIET.get("chain_identity") is True

    def test_not_batched(self):
        assert "chain_identity" in sentinel.IMMEDIATE_ALERT_TYPES

    def test_cooldown_default_present_in_both_registries(self):
        """DEFAULT_CONFIG covers fresh installs; get_alert_cooldowns covers
        upgraded ones whose saved config replaced the sub-dict wholesale."""
        assert sentinel.DEFAULT_CONFIG["alert_cooldowns"]["chain_identity"] == 21600
        assert sentinel.get_alert_cooldowns()["chain_identity"] == 21600

    def test_rpc_methods_are_whitelisted(self):
        """_rpc_call rejects anything not on the allow-list, which would make
        the check silently return None forever."""
        assert "getblockhash" in sentinel._RPC_ALLOWED_METHODS
        assert "getdeploymentinfo" in sentinel._RPC_ALLOWED_METHODS

    def test_block_hashes_are_well_formed_and_distinct(self):
        assert len(MAJORITY) == 64 and len(RDTS) == 64
        assert MAJORITY != RDTS
        assert all(c in "0123456789abcdef" for c in MAJORITY + RDTS)
