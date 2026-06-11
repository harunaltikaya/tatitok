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

STRUCTURAL checks (always run, even without the map): the local-only
artifacts (-full expectation files under expected/, *.local.json secret
maps) must never be stageable.

SHAPE checks (always run, even without the map — they need no secrets):
every committed testdata/ JSON/JSONL file is parsed and its sanitizer-output
invariants verified:
  - in every path-shaped string value, EVERY segment after the project
    segment (a project-<6hex> alias or an encoded "-..." dirname, incl.
    bare "-home-user") must be a sanitizer token: d-<6hex>[.ext],
    p-<8hex>[.ext], project-<6hex>, a pseudonymized id, or another encoded
    dirname — a literal segment there is a leak (audit class 1);
  - in schema-defined path-keyed maps (readFileState, trackedFileBackups),
    EVERY key must be a p-<8hex>[.ext] token (audit class 2).

MAP-INDEPENDENT content scans (every file, run in BOTH modes — they need
no secrets): credential patterns (private-key blocks, AWS/GitHub/
Anthropic/OpenAI/Slack tokens, JWTs), real home paths (/home/<anything
except the sanitizer's "user" alias>, /Users/<anything> — macOS homes are
never legitimate here), and email addresses.

When the alias map is absent (CI checkout: the map is a gitignored local
artifact), the checker still runs the structural, shape and
map-independent content checks and skips ONLY the map-dependent scans
(real ids/tokens/pseudonym sets), stating so explicitly. On the owner's
machine the map is expected to exist, so the full scan always runs there.

Findings print as file:line + category with the matched value REDACTED
(first 4 chars + length) so the checker's output can be shared safely;
--show-values prints them in full. Exit 0 = clean, 1 = findings, 2 = cannot
run (no git).

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
# prefixed-id shapes come from the shared sanitizer rule-spec (one source
# of truth): every such id in a committed file must be a known pseudonym
_RULES = json.loads(
    (Path(__file__).resolve().parent.parent
     / "internal" / "core" / "sanitize_rules.json").read_text(encoding="utf-8"))

# The checker derives its scan patterns from the rule-spec and is built
# for exactly ONE spec_version: scanning with semantics it was not written
# against could wave a leak through while looking green. Mismatch = cannot
# run (exit 2), never a silent best-effort scan.
BUILT_FOR_SPEC_VERSION = 1
if _RULES["spec_version"] != BUILT_FOR_SPEC_VERSION:
    print("error: check_fixture_leaks.py is built for sanitize_rules.json "
          "spec_version %d but the repo's spec is %r — update this "
          "checker's scans for the new rule-spec semantics before trusting "
          "its verdict" % (BUILT_FOR_SPEC_VERSION, _RULES["spec_version"]),
          file=sys.stderr)
    sys.exit(2)

PREFIX_ID_RE = re.compile(
    r"\b(?:%s)_[A-Za-z0-9]{10,}\b"
    % "|".join(_RULES["id_shapes"]["prefix_id_prefixes"]))

# Sanitizer contract vectors are SYNTHETIC by design (fabricated content is
# explicitly allowed there — the sanitizer is a pure function). Raw vectors
# deliberately violate sanitizer-output shape (they are pre-sanitization
# inputs) and use made-up ids, so this directory is exempt from the SHAPE
# checks and the unknown-id heuristics ONLY. Everything else still runs on
# it: global real-id/salt/home literals, map tokens, secrets, emails.
VECTOR_PREFIX = "testdata/sanitizer-vectors/"

# Go module manifests: public registry paths only — exempt from the
# aliased-segment token scan (still scanned for global literals/ids)
MODULE_MANIFESTS = {"go.mod", "go.sum"}

# binary-ish files we never scan for text leaks
SKIP_SUFFIXES = {".png", ".jpg", ".jpeg", ".gif", ".ico", ".db", ".sqlite"}

# --- map-independent content scans (map-free; run on CI too) -----------------

# Credential shapes that are leaks no matter whose they are. Each literal
# regex is written so it cannot match its own source text here.
SECRET_PATTERNS = [
    ("private-key-block", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")),
    ("aws-access-key-id", re.compile(r"\bAKIA[0-9A-Z]{16}\b")),
    ("github-token", re.compile(r"\bgh[pousr]_[A-Za-z0-9]{20,}\b")),
    ("anthropic-api-key", re.compile(r"\bsk-ant-[A-Za-z0-9_-]{8,}\b")),
    ("openai-api-key", re.compile(r"\bsk-[A-Za-z0-9]{24,}\b")),
    ("slack-token", re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{10,}\b")),
    ("jwt", re.compile(
        r"\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}\b")),
]
# The sanitizer aliases the owner's home to /home/user; any other /home/*
# is a real username, and /Users/* (macOS) is never aliased at all.
HOME_PATH_RE = re.compile(r"/home/(?!user(?![A-Za-z0-9._+-]))[A-Za-z0-9._+-]+")
MACOS_HOME_RE = re.compile(r"/Users/[A-Za-z0-9._+-]+")
EMAIL_RE = re.compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b")


def scan_map_free(repo, rel, findings):
    """Content scans that need no alias map: secrets, real home paths,
    emails. Run on every committed file in both modes."""
    path = repo / rel
    if path.suffix.lower() in SKIP_SUFFIXES:
        return
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        findings.append((str(rel), 0, "unreadable", str(exc)))
        return
    for lineno, line in enumerate(text.splitlines(), 1):
        for cat, rx in SECRET_PATTERNS:
            for m in rx.finditer(line):
                findings.append((str(rel), lineno, "secret-" + cat, m.group()))
        for m in HOME_PATH_RE.finditer(line):
            findings.append((str(rel), lineno, "real-home-path", m.group()))
        for m in MACOS_HOME_RE.finditer(line):
            findings.append((str(rel), lineno, "macos-home-path", m.group()))
        for m in EMAIL_RE.finditer(line):
            findings.append((str(rel), lineno, "email-address", m.group()))


# --- sanitizer-output shape invariants (map-free; run on CI too) -------------

# maps that are path-keyed BY SCHEMA — from the shared sanitizer rule-spec
PATH_KEYED_MAPS = set(_RULES["harvest"]["path_keyed_maps"])

P_TOKEN_RE = re.compile(r"p-[0-9a-f]{8}(?:\.[A-Za-z0-9]{1,5})?")
PROJECT_TOKEN_RE = re.compile(r"project-[0-9a-f]{6}")
# what the sanitizer may emit AFTER the project segment: d-/p- tokens,
# nested project aliases, pseudonymized ids (UUID or msg_/req_/toolu_,
# optionally with a file extension), or another encoded "-..." dirname
POST_PROJECT_SEGMENT_RE = re.compile(
    r"(?:d-[0-9a-f]{6}|p-[0-9a-f]{8}|project-[0-9a-f]{6}"
    r"|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
    r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
    r"|(?:msg|req|toolu)_[A-Za-z0-9]+)"
    r"(?:\.[A-Za-z0-9]{1,5})?")


def is_pathlike(s):
    """Mirror of the harvester's heuristic for path-shaped string values."""
    if s.startswith(("/", "~")) or (s.startswith(".") and "/" in s):
        return True
    return "/" in s and " " not in s and "\n" not in s and "\t" not in s


def check_sanitized_path(s, rel, lineno, findings):
    project_seen = False
    for seg in s.split("/"):
        if project_seen:
            if not POST_PROJECT_SEGMENT_RE.fullmatch(seg) \
                    and not seg.startswith("-"):
                findings.append((rel, lineno, "literal-post-project-segment",
                                 "%s (in %s)" % (seg, s)))
        elif PROJECT_TOKEN_RE.fullmatch(seg) or seg.startswith("-"):
            project_seen = True


def shape_walk(node, rel, lineno, findings, key=None):
    if isinstance(node, dict):
        if key in PATH_KEYED_MAPS:
            for k in node:
                if not P_TOKEN_RE.fullmatch(k):
                    findings.append((rel, lineno, "non-token-path-map-key", k))
        for k, v in node.items():
            shape_walk(v, rel, lineno, findings, k)
    elif isinstance(node, list):
        for v in node:
            shape_walk(v, rel, lineno, findings, key)
    elif isinstance(node, str) and is_pathlike(node):
        check_sanitized_path(node, rel, lineno, findings)


def shape_scan(repo, rel, findings):
    try:
        text = (repo / rel).read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        findings.append((str(rel), 0, "unreadable", str(exc)))
        return
    if rel.suffix == ".jsonl":
        for lineno, line in enumerate(text.splitlines(), 1):
            if not line.strip():
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                findings.append((str(rel), lineno, "unparseable-fixture-line",
                                 line[:40]))
                continue
            shape_walk(obj, str(rel), lineno, findings)
    else:
        try:
            obj = json.loads(text)
        except json.JSONDecodeError as exc:
            findings.append((str(rel), 0, "unparseable-json", str(exc)))
            return
        shape_walk(obj, str(rel), 0, findings)


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


def load_maps(map_paths, home, username):
    """Merge every per-source secret alias map (each fixture source root —
    claude-code, codex, opencode — keeps its own PROJECT_MAP.local.json
    with its own salt)."""
    globals_ = {}  # real string -> category; substring-matched everywhere
    tokens = {}    # real token -> (category, compiled regex)
    ids = {}       # merged real id -> pseudonym

    def g(value, cat):
        if isinstance(value, str) and len(value) >= 4:
            globals_.setdefault(value, cat)

    # Names the rule-spec declares STRUCTURAL (public tool/XDG dirs, date
    # segments) can appear in the alias maps too, when the same name shows
    # up after a project segment and gets aliased there. Their literal
    # occurrences are public by spec; the SHAPE check still proves every
    # post-project segment in committed fixtures is a sanitizer token.
    structural = (set(_RULES["harvest"]["path_segment_allowlist"])
                  | set(_RULES["harvest"]["owner_approved_segments"]))
    structural_res = [re.compile(p) for p in
                      _RULES["harvest"]["path_segment_allowlist_regexes"]]

    def t(value, cat):
        if not isinstance(value, str) or len(value) < 3:
            return
        if value in structural or any(rx.match(value) for rx in structural_res):
            return
        tokens.setdefault(value, (cat, token_re(value)))

    g(str(home), "home-path")
    g(str(home).replace("/", "-"), "encoded-home")
    if len(username) >= 4:
        g(username, "username")

    for map_path in map_paths:
        m = json.loads(map_path.read_text(encoding="utf-8"))
        g(m["salt"], "harvest-salt")
        for k in m.get("ids", {}):
            g(k, "real-id")
        for k in m.get("sources", {}):
            g(k, "real-source-path")
        for k in m.get("projects", {}):
            t(k, "real-project-slug")
        for k in m.get("segments", {}):
            t(k, "real-path-segment")
        for k in m.get("keys", {}):
            t(k, "real-path-key")
        ids.update(m.get("ids", {}))

    # 8-hex prefixes of real UUIDs catch truncated doc mentions; skip any
    # that collide with a pseudonym's hex (astronomically unlikely). The
    # match demands non-hex neighbors: an all-digit prefix is otherwise a
    # guaranteed false positive inside epoch-millisecond timestamps
    # (found by the opencode harvest — a claude uuid prefix inside
    # time_created), while a truncated mention like "abcd1234.jsonl"
    # still matches.
    pseudonyms = {v.lower() for v in ids.values()}
    prefixes = {}
    for k in ids:
        if UUID_RE.fullmatch(k):
            p = k[:8].lower()
            if not any(p in ps for ps in pseudonyms):
                prefixes[p] = re.compile(
                    r"(?<![0-9a-fA-F])" + p + r"(?![0-9a-fA-F])")
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
            if rel.name in MODULE_MANIFESTS:
                # go.mod/go.sum hold public registry module paths;
                # common words there collide with aliased segments but
                # cannot leak the owner's filesystem. Global literals,
                # id prefixes and UUIDs are still checked on these files.
                continue
            for m in rx.finditer(line):
                # segment and key tokens only leak in path context: the
                # match must touch a slash (slugs leak anywhere, even in
                # prose)
                if cat != "real-project-slug":
                    before = line[m.start() - 1] if m.start() > 0 else ""
                    after = line[m.end()] if m.end() < len(line) else ""
                    if before != "/" and after != "/" and "/" not in m.group():
                        continue
                findings.append((str(rel), lineno, cat, lit))
                break
        for p, prx in prefixes.items():
            if p in low and prx.search(low):
                findings.append((str(rel), lineno, "real-id-prefix", p))
        if str(rel).startswith(VECTOR_PREFIX):
            continue  # synthetic vector ids are exempt from the unknown-id
            # heuristics only; all literal/token/secret scans above ran
        for match in UUID_RE.findall(line):
            if match.lower() not in pseudonyms:
                findings.append((str(rel), lineno, "unknown-uuid", match))
        for match in PREFIX_ID_RE.findall(line):
            if match not in pseudonyms and match.lower() not in pseudonyms:
                findings.append((str(rel), lineno, "unknown-prefixed-id",
                                 match))


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--maps-glob",
                    default="testdata/fixtures/*/PROJECT_MAP.local.json",
                    help="glob for the per-source secret alias maps "
                         "(never committed)")
    ap.add_argument("--show-values", action="store_true",
                    help="print matched values in full (default: redacted)")
    args = ap.parse_args()

    repo_out = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                              capture_output=True, text=True)
    if repo_out.returncode != 0:
        print("error: not inside a git repository", file=sys.stderr)
        return 2
    repo = Path(repo_out.stdout.strip())

    files = committed_files(repo)

    # STRUCTURAL: the local-only artifacts must never appear in the
    # committed set. Runs unconditionally, with or without the map.
    for rel in files:
        s = str(rel)
        if s.endswith("-full.json") and "/expected/" in s:
            print("FATAL: local-only -full expectation file is stageable: %s"
                  % s)
            return 1
        if s.endswith(".local.json"):
            print("FATAL: local-only secret map is stageable: %s" % s)
            return 1

    # SHAPE: sanitizer-output invariants on every committed testdata/
    # JSON/JSONL file. Map-free, so it runs on CI too.
    findings = []
    shaped = [rel for rel in files
              if str(rel).startswith("testdata/")
              and not str(rel).startswith(VECTOR_PREFIX)
              and rel.suffix in (".json", ".jsonl")]
    for rel in shaped:
        shape_scan(repo, rel, findings)

    # MAP-INDEPENDENT content scans (secrets, real home paths, emails) on
    # every committed file. Map-free, so they run on CI too.
    for rel in files:
        scan_map_free(repo, rel, findings)

    def report(scope):
        by_cat = {}
        for rel, lineno, cat, value in findings:
            by_cat[cat] = by_cat.get(cat, 0) + 1
            shown = value if args.show_values else redact(value)
            print("LEAK %-28s %s:%d  %s" % (cat, rel, lineno, shown))
        print(scope)
        if findings:
            print("FINDINGS: %d total — %s" % (
                len(findings),
                ", ".join("%s=%d" % kv for kv in sorted(by_cat.items()))))
            return 1
        print("CLEAN: zero real identifiers in the committed tree "
              "(id check global, no -full exemption)")
        return 0

    map_paths = sorted(repo.glob(args.maps_glob))
    if not map_paths:
        return report(
            "structural checks passed, %d testdata files shape-checked, and "
            "map-independent content scans (secret patterns, /home/<not "
            "user>, /Users/*, emails) ran across %d committed files; no "
            "alias maps found (%s) — ONLY map-dependent scans (real ids/"
            "tokens/pseudonyms) SKIPPED (expected on CI; the maps are "
            "local-only artifacts)" % (len(shaped), len(files), args.maps_glob))

    globals_, tokens, prefixes, pseudonyms = load_maps(
        map_paths, Path.home(), Path.home().name)

    # Public by construction: committed file paths/components, the repo
    # name, and the harness/tool names this product exists to support —
    # the docs cannot avoid writing "ccusage opencode" or naming
    # testdata/fixtures/<harness>/ paths. Fixture .jsonl files still get
    # NO exemption: these names must be aliased inside fixture logs.
    public_tokens = {repo.name.lower(),
                     "claude", "claude-code", "codex", "opencode", "ccusage"}
    for rel in files:
        public_tokens.add(str(rel).lower())
        public_tokens.update(part.lower() for part in rel.parts)

    for rel in files:
        scan(repo, rel, globals_, tokens, prefixes, pseudonyms,
             public_tokens, findings)

    return report(
        "scanned %d committed files (%d shape-checked): %d global literals, "
        "%d map tokens (%d public-by-path exempt outside fixture logs), "
        "%d id prefixes, %d known pseudonyms"
        % (len(files), len(shaped), len(globals_), len(tokens),
           len([t for t in tokens if t.lower() in public_tokens]),
           len(prefixes), len(pseudonyms)))


if __name__ == "__main__":
    sys.exit(main())
