#!/usr/bin/env python3
"""Leak checker for the committed tree (owner's machine only).

Enforces the owner ruling: ZERO real identifiers of any kind anywhere in the
committed tree, no exemptions. The -full expectation files and the secret
alias map are gitignored local artifacts and therefore outside the committed
set; the checker fails hard if either ever becomes stageable.

Scans every file git would commit (`git ls-files --cached --others
--exclude-standard`) with two kinds of checks:

GLOBAL substring checks (every file, zero tolerance, no exemptions):
  - the harvest salt, $HOME, encoded home (-home-<user>), the username;
  - every REAL identifier from PROJECT_MAP.local.json's id map, plus its
    first-8-hex prefix (catches truncated mentions like `abcdef12….jsonl`);
  - every real source path from the map;
  - unknown identifiers: every UUID-shaped or msg_/req_/toolu_-shaped string
    must be a known pseudonym (a VALUE of the id map) — anything else is
    treated as a possibly-real identifier and flagged.

TOKEN checks (word-boundary; underscore counts as a word character) for
real project slugs / path segments / path-valued keys from the map. The map
picked up the repo's own public path tokens (testdata, claude-code, the
machine label, doc paths, ...) because the live harvest session's log
references paths inside this repo, so:
  - project slugs match anywhere as standalone tokens (a project name in
    prose is still a leak);
  - path segments / path keys must additionally touch a '/' — they only
    leak in path context; bare English words like "tools" in prose or JSON
    field names are not path leaks;
  - fixture *.jsonl files get NO exemptions — every map token must have
    been aliased by the sanitizer;
  - in all other files, tokens equal to a committed file's path or any of
    its components are exempt (public by construction: the repo cannot
    avoid naming its own directories and docs).

Findings print as file:line + category with the matched value REDACTED
(first 4 chars + length) so the checker's output can be shared safely;
--show-values prints them in full. Exit 0 = clean, 1 = findings, 2 = cannot
run (no git / no map).

Usage:
    python scripts/check_fixture_leaks.py [--show-values]
"""

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

UUID_RE = re.compile(
    r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
    r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
PREFIX_ID_RE = re.compile(r"\b(?:msg|req|toolu)_[A-Za-z0-9]{10,}\b")

# binary-ish files we never scan for text leaks
SKIP_SUFFIXES = {".png", ".jpg", ".jpeg", ".gif", ".ico", ".db", ".sqlite"}


def redact(value):
    return "%s…(len=%d)" % (value[:4], len(value))


def token_re(literal):
    """Match the literal as a standalone token: no word character on either
    side (hyphens inside the literal itself are fine)."""
    return re.compile(r"(?<![A-Za-z0-9_])" + re.escape(literal)
                      + r"(?![A-Za-z0-9_])", re.IGNORECASE)


def committed_files(repo):
    out = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard"],
        cwd=repo, capture_output=True, text=True, check=True)
    return [Path(p) for p in out.stdout.splitlines() if p]


def load_map(map_path, home, username):
    m = json.loads(map_path.read_text(encoding="utf-8"))

    globals_ = {}  # real string -> category; substring-matched everywhere
    def g(value, cat):
        if isinstance(value, str) and len(value) >= 4:
            globals_.setdefault(value, cat)

    g(m["salt"], "harvest-salt")
    g(str(home), "home-path")
    g(str(home).replace("/", "-"), "encoded-home")
    if len(username) >= 4:
        g(username, "username")
    for k in m.get("ids", {}):
        g(k, "real-id")
    for k in m.get("sources", {}):
        g(k, "real-source-path")

    tokens = {}  # real token -> (category, compiled regex)
    def t(value, cat):
        if isinstance(value, str) and len(value) >= 3:
            tokens.setdefault(value, (cat, token_re(value)))

    for k in m.get("projects", {}):
        t(k, "real-project-slug")
    for k in m.get("segments", {}):
        t(k, "real-path-segment")
    for k in m.get("keys", {}):
        t(k, "real-path-key")

    # 8-hex prefixes of real UUIDs catch truncated doc mentions; skip any
    # that collide with a pseudonym's hex (astronomically unlikely)
    pseudonyms = {v.lower() for v in m.get("ids", {}).values()}
    prefixes = set()
    for k in m.get("ids", {}):
        if UUID_RE.fullmatch(k):
            p = k[:8].lower()
            if not any(p in ps for ps in pseudonyms):
                prefixes.add(p)
    return globals_, tokens, prefixes, pseudonyms


def scan(repo, rel, globals_, tokens, prefixes, pseudonyms, public_tokens,
         findings):
    path = repo / rel
    if path.suffix.lower() in SKIP_SUFFIXES:
        return
    is_fixture_log = str(rel).startswith("testdata/") and rel.suffix == ".jsonl"
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        findings.append((str(rel), 0, "unreadable", str(exc)))
        return
    for lineno, line in enumerate(text.splitlines(), 1):
        low = line.lower()
        for lit, cat in globals_.items():
            if lit in line or lit.lower() in low:
                findings.append((str(rel), lineno, cat, lit))
        for lit, (cat, rx) in tokens.items():
            if not is_fixture_log and lit.lower() in public_tokens:
                continue  # names a committed path: public by construction
            for m in rx.finditer(line):
                # segments/keys only leak in path context: the match must
                # touch a '/' (slugs leak anywhere, even in prose)
                if cat != "real-project-slug":
                    before = line[m.start() - 1] if m.start() > 0 else ""
                    after = line[m.end()] if m.end() < len(line) else ""
                    if before != "/" and after != "/" and "/" not in m.group():
                        continue
                findings.append((str(rel), lineno, cat, lit))
                break
        for p in prefixes:
            if p in low:
                findings.append((str(rel), lineno, "real-id-prefix", p))
        for match in UUID_RE.findall(line):
            if match.lower() not in pseudonyms:
                findings.append((str(rel), lineno, "unknown-uuid", match))
        for match in PREFIX_ID_RE.findall(line):
            if match not in pseudonyms and match.lower() not in pseudonyms:
                findings.append((str(rel), lineno, "unknown-prefixed-id",
                                 match))


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--map",
                    default="testdata/fixtures/claude-code/"
                            "PROJECT_MAP.local.json",
                    help="path to the secret alias map (never committed)")
    ap.add_argument("--show-values", action="store_true",
                    help="print matched values in full (default: redacted)")
    args = ap.parse_args()

    repo_out = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                              capture_output=True, text=True)
    if repo_out.returncode != 0:
        print("error: not inside a git repository", file=sys.stderr)
        return 2
    repo = Path(repo_out.stdout.strip())

    map_path = Path(args.map)
    if not map_path.is_file():
        print("error: alias map not found: %s" % map_path, file=sys.stderr)
        return 2

    globals_, tokens, prefixes, pseudonyms = load_map(
        map_path, Path.home(), Path.home().name)
    files = committed_files(repo)

    # the local-only artifacts must never appear in the committed set
    for rel in files:
        s = str(rel)
        if s.endswith("-full.json") and "/expected/" in s:
            print("FATAL: local-only -full expectation file is stageable: %s"
                  % s)
            return 1
        if s.endswith(".local.json"):
            print("FATAL: local-only secret map is stageable: %s" % s)
            return 1

    public_tokens = {repo.name.lower()}
    for rel in files:
        public_tokens.add(str(rel).lower())
        public_tokens.update(part.lower() for part in rel.parts)

    findings = []
    for rel in files:
        scan(repo, rel, globals_, tokens, prefixes, pseudonyms,
             public_tokens, findings)

    by_cat = {}
    for rel, lineno, cat, value in findings:
        by_cat[cat] = by_cat.get(cat, 0) + 1
        shown = value if args.show_values else redact(value)
        print("LEAK %-20s %s:%d  %s" % (cat, rel, lineno, shown))

    print("scanned %d committed files: %d global literals, %d map tokens "
          "(%d public-by-path exempt outside fixture logs), %d id prefixes, "
          "%d known pseudonyms"
          % (len(files), len(globals_), len(tokens),
             len([t for t in tokens if t.lower() in public_tokens]),
             len(prefixes), len(pseudonyms)))
    if findings:
        print("FINDINGS: %d total — %s" % (
            len(findings),
            ", ".join("%s=%d" % kv for kv in sorted(by_cat.items()))))
        return 1
    print("CLEAN: zero real identifiers in the committed tree "
          "(id check global, no -full exemption)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
