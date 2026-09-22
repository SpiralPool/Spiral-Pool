"""Proof tests for the orphan-alert timestamp fix.

The orphan embed printed found_at (an ISO-8601 UTC string from the pool API) verbatim
while rendering orphaned_at in the configured display timezone, so every alert showed
the block orphaned hours BEFORE it was found. fmt_display_time() now normalizes both.

The fraction-width cases are the ones worth keeping: `created` is a Go time.Time whose
JSON encoding trims trailing zeros, so the API emits 1-6 fractional digits, while
datetime.fromisoformat before Python 3.11 accepts only 3 or 6. A naive implementation
passes on a modern interpreter and silently falls back to the raw UTC string on the
3.9/3.10 that a bare-metal install may be running.

Run: python -m pytest tests/test_orphan_alert_timestamps.py -v
"""
import importlib.util
import os
import re
import tempfile
from datetime import datetime, timezone

import pytest

# Import SpiralSentinel as a module from its file path (it is a script, not a package).
# A throwaway install dir keeps its config/logging bootstrap off the real filesystem.
os.environ.setdefault("SPIRALPOOL_INSTALL_DIR", tempfile.mkdtemp())
_spec = importlib.util.spec_from_file_location(
    "spiral_sentinel",
    os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py"),
)
sentinel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sentinel)

# The real DGB orphan this fix came from: found 23:32:07 UTC, orphaned 21m42s later.
FOUND_UTC = "2026-09-18T23:32:07.750926Z"
ORPHANED_EPOCH = 1789775629.4179347
EXPECT_FOUND_EDT = "2026-09-18 19:32:07 EDT"
EXPECT_ORPHANED_EDT = "2026-09-18 19:53:49 EDT"


@pytest.fixture(autouse=True)
def display_tz_new_york():
    """Pin the display timezone so expected strings are stable."""
    prev = sentinel.CONFIG.get("display_timezone")
    sentinel.CONFIG["display_timezone"] = "America/New_York"
    yield
    if prev is None:
        sentinel.CONFIG.pop("display_timezone", None)
    else:
        sentinel.CONFIG["display_timezone"] = prev


class StrictISO(datetime):
    """Simulates Python 3.9/3.10: fromisoformat takes only 3 or 6 fractional digits.

    Returns an instance of itself so the helper's isinstance(dt, datetime) check —
    which resolves `datetime` from the module globals we patch — still holds.
    """
    _real = staticmethod(datetime.fromisoformat)

    @classmethod
    def fromisoformat(cls, s):
        m = re.search(r"\.(\d+)", s)
        if m and len(m.group(1)) not in (3, 6):
            raise ValueError(f"Invalid isoformat string: {s!r}  [simulated <=3.10]")
        d = cls._real(s)
        return cls(d.year, d.month, d.day, d.hour, d.minute,
                   d.second, d.microsecond, d.tzinfo)


class TestTheRegression:
    """The defect as it appeared in the field."""

    def test_found_is_before_orphaned(self):
        found = sentinel.fmt_display_time(FOUND_UTC)
        orphaned = sentinel.fmt_display_time(
            datetime.fromtimestamp(ORPHANED_EPOCH, tz=sentinel.get_display_tz()))
        assert found == EXPECT_FOUND_EDT
        assert orphaned == EXPECT_ORPHANED_EDT
        assert found < orphaned, "found_at must sort before orphaned_at"

    def test_both_carry_the_zone(self):
        # The ambiguity was the bug; every rendered time names its zone.
        assert sentinel.fmt_display_time(FOUND_UTC).endswith(" EDT")

    def test_embed_renders_them_in_order(self):
        """End-to-end through the embed that actually shipped the bug."""
        embed = sentinel.create_block_orphaned_embed(
            block_height=24235495,
            coin_symbol="DGB",
            found_at=FOUND_UTC,
            orphaned_at=datetime.fromtimestamp(ORPHANED_EPOCH, tz=sentinel.get_display_tz()),
            block_reward=250.72839494,
        )
        blob = str(embed)
        assert EXPECT_FOUND_EDT in blob
        assert EXPECT_ORPHANED_EDT in blob
        assert blob.index(EXPECT_FOUND_EDT) < blob.index(EXPECT_ORPHANED_EDT)
        # The raw UTC string must not survive into the alert.
        assert FOUND_UTC not in blob


class TestGoFractionWidths:
    """Go's time.Time JSON encoding trims trailing zeros -> 1 to 6 fractional digits."""

    WIDTHS = ["", ".7", ".75", ".750", ".7509", ".75092", ".750926"]

    @pytest.mark.parametrize("frac", WIDTHS)
    def test_every_width_converts(self, frac):
        assert sentinel.fmt_display_time(
            f"2026-09-18T23:32:07{frac}Z") == EXPECT_FOUND_EDT

    @pytest.mark.parametrize("frac", WIDTHS)
    def test_every_width_converts_on_python_3_9(self, monkeypatch, frac):
        """The case a modern interpreter hides: fromisoformat restricted to 3 or 6 digits.

        Without the six-digit normalization this falls back to the raw UTC string for
        1-, 2-, 4- and 5-digit fractions, reinstating the original defect.
        """
        monkeypatch.setattr(sentinel, "datetime", StrictISO)
        assert sentinel.fmt_display_time(
            f"2026-09-18T23:32:07{frac}Z") == EXPECT_FOUND_EDT


class TestAcceptedShapes:
    @pytest.mark.parametrize("value", [
        "2026-09-18T23:32:07.750926Z",       # API form
        "2026-09-18T23:32:07.750926+00:00",  # explicit UTC offset
        "2026-09-18T19:32:07.750926-04:00",  # already local
        "2026-09-18 23:32:07",               # space separator, no offset -> UTC
        "2026-09-18t23:32:07z",              # ISO-8601 allows lowercase
        "  2026-09-18T23:32:07Z  ",          # surrounding whitespace
    ])
    def test_string_forms(self, value):
        assert sentinel.fmt_display_time(value) == EXPECT_FOUND_EDT

    def test_naive_datetime_is_treated_as_utc(self):
        assert sentinel.fmt_display_time(
            datetime(2026, 9, 18, 23, 32, 7)) == EXPECT_FOUND_EDT

    def test_aware_datetime_is_converted(self):
        assert sentinel.fmt_display_time(
            datetime(2026, 9, 18, 23, 32, 7, tzinfo=timezone.utc)) == EXPECT_FOUND_EDT

    @pytest.mark.parametrize("value", [ORPHANED_EPOCH, int(ORPHANED_EPOCH)])
    def test_unix_timestamps(self, value):
        assert sentinel.fmt_display_time(value) == EXPECT_ORPHANED_EDT


class TestNeverRaises:
    """This runs inside an alert path: a bad timestamp must degrade, not throw."""

    @pytest.mark.parametrize("value", [
        "not a timestamp", "", "2026-13-45T99:99:99Z",
        None, [], {}, object(),
        1e20, -1e20, float("nan"), float("inf"),
    ])
    def test_malformed_input_returns_a_string(self, value):
        assert isinstance(sentinel.fmt_display_time(value), str)

    def test_unparseable_string_is_returned_unchanged(self):
        assert sentinel.fmt_display_time("not a timestamp") == "not a timestamp"

    @pytest.mark.parametrize("value", [True, False])
    def test_bool_is_not_read_as_an_epoch(self, value):
        # bool is a subclass of int; True would otherwise render as 1970-01-01.
        assert sentinel.fmt_display_time(value) == str(value)


class TestTimezoneIsHonoured:
    def test_utc_display_tz(self):
        sentinel.CONFIG["display_timezone"] = "UTC"
        assert sentinel.fmt_display_time(FOUND_UTC) == "2026-09-18 23:32:07 UTC"

    def test_unknown_tz_falls_back_without_raising(self):
        sentinel.CONFIG["display_timezone"] = "Not/AZone"
        assert sentinel.fmt_display_time(FOUND_UTC) == EXPECT_FOUND_EDT
