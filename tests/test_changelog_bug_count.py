# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""The release summary's bug count must match the entries beneath it.

The number is written in prose near the top of the v3.0.0 section and the
entries it counts are hundreds of lines below, so the two drift apart every
time anyone adds a fix. It has now been wrong twice: "thirty-six" when the
entries said forty, and "forty" when they said fifty-three. Nobody notices,
because nothing reads it except a person deciding whether to trust the rest of
the document.

Counting rule, which the guard and the prose have to agree on:
  every "- **" bullet under a "### Fixed" heading in the v3.0.0 section,
  EXCEPT
    - "Fixed - Sentinel auto-restart", which the sentence names separately
      ("Sentinel's auto-restart defect AND n pre-existing bugs"), and
    - "Fixed - regtest harness and release packaging", whose own opening line
      says these are defects in the harness rather than the pool and are "not
      counted among the pre-existing bugs above".

Run: python -m pytest tests/test_changelog_bug_count.py -v
"""
import io
import os
import re

WORDS = {
    "twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
    "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
}
UNITS = {"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
         "six": 6, "seven": 7, "eight": 8, "nine": 9}

EXCLUDED = ("Sentinel auto-restart", "regtest harness")


def _word_to_int(word):
    word = word.strip().lower()
    if word in WORDS:
        return WORDS[word]
    if "-" in word:
        tens, units = word.split("-", 1)
        if tens in WORDS and units in UNITS:
            return WORDS[tens] + UNITS[units]
    raise AssertionError("cannot read the number %r - extend WORDS/UNITS" % word)


def _changelog():
    path = os.path.join(os.path.dirname(__file__), "..", "CHANGELOG.md")
    return io.open(path, encoding="utf-8").read().split(chr(10))


def _v3_bounds(lines):
    start = next(i for i, l in enumerate(lines) if l.startswith("## [v3.0.0]"))
    end = next(i for i in range(start + 1, len(lines)) if lines[i].startswith("## ["))
    return start, end


def count_fixed_bullets():
    lines = _changelog()
    start, end = _v3_bounds(lines)
    total, i = 0, start
    while i < end:
        if lines[i].startswith("### "):
            heading, n, j = lines[i], 0, i + 1
            while j < end and not lines[j].startswith("### "):
                if lines[j].startswith("- **"):
                    n += 1
                j += 1
            if heading.startswith("### Fixed") and not any(x in heading for x in EXCLUDED):
                total += n
            i = j
            continue
        i += 1
    return total


def stated_count():
    lines = _changelog()
    start, end = _v3_bounds(lines)
    for l in lines[start:end]:
        m = re.search(r"auto-restart defect and ([a-z-]+) pre-existing bugs are fixed", l)
        if m:
            return _word_to_int(m.group(1)), m.group(1)
    raise AssertionError("the release summary no longer states a bug count")


def test_the_stated_count_matches_the_entries():
    stated, word = stated_count()
    actual = count_fixed_bullets()
    assert stated == actual, (
        "CHANGELOG says %r (%d) pre-existing bugs, but the Fixed sections hold %d entries"
        % (word, stated, actual))
