# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""Every cross-reference in the documentation points at something that exists.

Three kinds of reference rot, all of which had shipped:

  * A link to a file that moved or was never there.
  * A `#anchor` whose heading was retitled. `docs/setup/DOCKER_GUIDE.md` sent
    readers to `#wsl2-native-path-installsh-in-wsl2` long after that heading
    became "WSL2 Native Path (Experimental - not recommended for production)".
  * A "TERMS.md Section N" citation naming a section that does not exist.
    Five documents cited `TERMS.md` Section 5D for the SimpleSwap disclosure.
    The disclosure is 5C, and the headings ran 5A, 5B, 5C, 5E -- there was no
    5D at all, in a legal document, for a feature involving a third-party
    exchange.

The section check deliberately reads **shell and Python sources too, not only
markdown**. `install.sh` prints "See WARNINGS.md and TERMS.md Section 5E." to
the operator during install and carried the same error; a markdown-only sweep
finds every other copy and leaves that one, which is the copy an operator
actually reads.

None of this is caught by anything else: the syntax is valid, the files parse,
and a renamed heading leaves the link looking perfectly well-formed.

A line carrying the marker "(renumbered to" is a deliberately preserved
historical reference -- a shipped release's changelog entry recording the name
a section had at the time -- and is exempt. Rewriting those would make the
changelog lie about what that release did.

Run: python -m pytest tests/test_doc_crossrefs.py -v
"""
import io
import os
import re

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

# Files whose "<FILE>.md Section N" citations are checked alongside the docs.
SOURCE_GLOBS = ("install.sh", "install-windows.ps1", "upgrade.sh", "coin-upgrade.sh")
SOURCE_DIRS = ("scripts", "src")
SOURCE_EXTS = (".sh", ".py", ".ps1")

HISTORICAL = "(renumbered to"


SKIP_DIRS = (".git", "node_modules", "__pycache__", ".pytest_cache", "venv", ".venv")


def _markdown_files():
    """Every markdown file in the repository, not only docs/ and the root.

    `.github/SUPPORT.md` links into `docs/` with relative paths, and
    `docker/tls/README.md` sits outside both trees. A sweep scoped to the
    documentation directories leaves those to rot unobserved.
    """
    out = []
    for base, dirs, files in os.walk(ROOT):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for f in sorted(files):
            if f.endswith(".md"):
                out.append(os.path.relpath(os.path.join(base, f), ROOT).replace("\\", "/"))
    return sorted(out)


def _source_files():
    out = [f for f in SOURCE_GLOBS if os.path.exists(os.path.join(ROOT, f))]
    for d in SOURCE_DIRS:
        for base, dirs, files in os.walk(os.path.join(ROOT, d)):
            dirs[:] = [x for x in dirs if x not in ("node_modules", "__pycache__")]
            for f in sorted(files):
                if f.endswith(SOURCE_EXTS):
                    out.append(os.path.relpath(os.path.join(base, f), ROOT).replace("\\", "/"))
    return out


def _read(rel):
    with io.open(os.path.join(ROOT, rel), encoding="utf-8", errors="replace") as fh:
        return fh.read()


def _slug(title):
    """GitHub's heading slugger.

    Lowercase, drop everything that is not a word character, space or hyphen,
    then spaces to hyphens. Consecutive hyphens are NOT collapsed: "Network &
    Pool Alerts" becomes "network--pool-alerts", because removing the "&"
    leaves two spaces behind. Collapsing them reports a dozen working links as
    broken, which is how this function was wrong the first time.
    """
    return re.sub(r"[^\w\s-]", "", title.lower()).strip().replace(" ", "-")


def _outside_fences(text):
    """Yield (line_number, line) for lines outside ``` fenced blocks."""
    infence = False
    for i, line in enumerate(text.split("\n"), 1):
        if line.strip().startswith("```"):
            infence = not infence
            continue
        if not infence:
            yield i, line


def _headings(rel):
    """(anchor slugs, {section number: title}) for one markdown file."""
    anchors, numbers, seen = set(), {}, {}
    for _, line in _outside_fences(_read(rel)):
        m = re.match(r"^#{1,6}\s+(.+?)\s*$", line)
        if not m:
            continue
        title = m.group(1)
        slug = _slug(title)
        # GitHub disambiguates a repeated heading with -1, -2, ...
        if slug in seen:
            seen[slug] += 1
            anchors.add("%s-%d" % (slug, seen[slug]))
        else:
            seen[slug] = 0
        anchors.add(slug)
        n = re.match(r"^(\d+[A-Z]?)\.\s+(.+)$", title)
        if n:
            numbers[n.group(1)] = n.group(2)
    return anchors, numbers


MD = _markdown_files()
HEADINGS = {rel: _headings(rel) for rel in MD}
BY_BASENAME = {}
for _rel in MD:
    BY_BASENAME.setdefault(os.path.basename(_rel), _rel)

LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")
CITATION = re.compile(r"([A-Za-z0-9_./-]+\.md)[^\n]{0,80}?(?:[Ss]ection|§)\s*(\d+[A-Z]?)")


def test_link_targets_exist():
    bad = []
    for rel in MD:
        base = os.path.dirname(rel)
        for num, line in _outside_fences(_read(rel)):
            for target in LINK.findall(line):
                if target.startswith(("http://", "https://", "mailto:", "#")):
                    continue
                path = target.split("#", 1)[0]
                if not path:
                    continue
                resolved = os.path.normpath(os.path.join(ROOT, base, path))
                if not os.path.exists(resolved):
                    bad.append("%s:%d -> %s" % (rel, num, target))
    assert not bad, "link targets that do not exist:\n  " + "\n  ".join(bad)


def test_anchors_resolve():
    bad = []
    for rel in MD:
        base = os.path.dirname(rel)
        for num, line in _outside_fences(_read(rel)):
            for target in LINK.findall(line):
                if target.startswith(("http://", "https://", "mailto:")):
                    continue
                path, _, frag = target.partition("#")
                if not frag:
                    continue
                if not path:
                    tgt = rel
                else:
                    tgt = os.path.relpath(
                        os.path.normpath(os.path.join(ROOT, base, path)), ROOT
                    ).replace("\\", "/")
                    if tgt not in HEADINGS:
                        continue  # a non-markdown target; nothing to resolve against
                if frag.lower() not in HEADINGS[tgt][0]:
                    bad.append("%s:%d -> %s" % (rel, num, target))
    assert not bad, "anchors with no matching heading:\n  " + "\n  ".join(bad)


def test_section_citations_resolve():
    bad = []
    for rel in MD + _source_files():
        for num, line in enumerate(_read(rel).split("\n"), 1):
            if HISTORICAL in line:
                continue
            for fname, section in CITATION.findall(line):
                target = BY_BASENAME.get(os.path.basename(fname))
                if not target:
                    continue
                numbers = HEADINGS[target][1]
                if numbers and section not in numbers:
                    bad.append(
                        "%s:%d cites %s section %s (it has: %s)"
                        % (rel, num, os.path.basename(fname), section,
                           ", ".join(sorted(numbers)))
                    )
    assert not bad, "citations of sections that do not exist:\n  " + "\n  ".join(bad)
