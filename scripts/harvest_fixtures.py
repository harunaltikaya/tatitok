#!/usr/bin/env python3
"""Harvest sanitized Claude Code log fixtures + ccusage expectations.

Runs on the OWNER's machine (never CI). Python 3.9+, stdlib only.

Usage:
    python scripts/harvest_fixtures.py --out testdata/fixtures/claude-code
    python scripts/harvest_fixtures.py --out testdata/fixtures/claude-code --label gx10
    python scripts/harvest_fixtures.py --check-vectors   # sanitizer contract test
    python scripts/harvest_fixtures.py --update-vectors  # regenerate expected vectors

The sanitization rule tables come from internal/core/sanitize_rules.json —
the ONE machine-readable spec shared with the Go DB sanitizer. The harvest
flow self-tests against testdata/sanitizer-vectors/ before touching real
logs.

What it does:
  1. Discovers Claude Code project dirs ($CLAUDE_CONFIG_DIR/projects if set,
     else ~/.claude/projects and ~/.config/claude/projects — all that exist).
  2. Selects a representative sample of session JSONL files (~8-15): the
     longest session, a short one, the most cache-heavy, one with mid-session
     model switches, the oldest, the newest, the 2-3 most recent, plus a
     time-spread fill. Every selected file is copied IN FULL (then sanitized).
  3. Sanitizes every line:
     - content-bearing fields (message text, thinking, tool_use input,
       tool_result content, summaries, attachments, aiTitle, lastPrompt, ...)
       become "<stripped len=N sha256=H>" placeholders where H is the first
       12 hex chars of sha256(SALT + content) — salted so short strings can't
       be guess-confirmed;
     - project identity is aliased uniformly, NO exceptions: every
       project-identifying path segment becomes project-<6hex of
       sha256(salt+slug)>, in fixture dir names, cwd values, MANIFEST paths
       and expected/*.json projectPath alike; EVERY path segment after the
       project segment becomes d-<6hex>[.ext] (the structural allowlist
       applies only before the project segment); path-valued object KEYS
       are replaced whole with p-<8hex>[.ext] — unconditionally for the
       schema-defined path-keyed maps (readFileState, trackedFileBackups),
       by path-likeness heuristic elsewhere;
     - identifiers (UUIDs, msg_*, req_*, toolu_*) are pseudonymized via
       salted hash with shape preserved — the same real id maps to the same
       fake id everywhere, so dedup and parentUuid chains survive; fixture
       filenames follow the new session ids;
     - $HOME -> "~", encoded home ("-home-<user>") -> "-home-user", bare
       username redacted, in values and keys; timestamps, usage objects,
       models, structure and unknown fields are preserved untouched;
     - malformed source lines become a single-key placeholder object so line
       counts stay identical.
     The salt + all real->alias maps go to <out>/PROJECT_MAP.local.json
     (gitignored; never committed, never printed).
  4. Captures ccusage expectations (`claude daily` and `claude session`,
     --json --offline — the `claude` subcommand scopes multi-agent ccusage
     v20+ to Claude Code logs only) twice:
       - fixture-scoped: from the SANITIZED fixture tree itself, so the
         committed expectations match the committed fixtures by
         construction. Written to expected/ccusage-daily.json +
         ccusage-session.json (the set CI parity tests load). As a
         self-check, ccusage is also run on the frozen ORIGINAL snapshot
         and the four token totals must match the sanitized-tree capture
         exactly, proving sanitization preserved billing-relevant data.
       - full-history: against the real config dir(s), post-processed with
         username/project redaction only (same alias map; ids untouched).
         Written to expected/ccusage-daily-full.json +
         ccusage-session-full.json — LOCAL-ONLY point-in-time references,
         gitignored and never committed (they keep real ids; owner ruling:
         zero real identifiers in the committed tree). `make parity-full`
         recaptures from live logs at comparison time instead of reading
         these files.
     The ccusage version is resolved once, pinned in expected/META.json, and
     reused on every future run.

Placeholder format (spec: internal/core/sanitize_rules.json "placeholder"):
    <stripped len=N sha256=H>
  N = UTF-8 byte length of the original string; H = first 12 lowercase hex
  chars of sha256(salt_utf8 + original_utf8). Non-string content values are
  first serialized to canonical JSON (sorted keys, compact separators,
  raw UTF-8) so the hash matches the Go sanitizer byte-for-byte.
"""

import argparse
import datetime
import hashlib
import json
import os
import platform
import re
import secrets
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
from pathlib import Path

# --- sanitization rules (loaded from the shared machine-readable spec) -------
#
# The content rules live in ONE spec consumed by both this script and the
# Go DB sanitizer (internal/core/sanitize.go embeds the same file). This
# script must never carry its own copy of a rule table; harvest-only
# behaviors (id pseudonymization, path aliasing, salting) use the spec's
# "harvest" section. Contract vectors in testdata/sanitizer-vectors/ hold
# the two implementations byte-equal (--check-vectors; the harvest flow
# self-tests against them before touching real logs).

REPO_ROOT = Path(__file__).resolve().parent.parent
RULES_PATH = REPO_ROOT / "internal" / "core" / "sanitize_rules.json"
RULES = json.loads(RULES_PATH.read_text(encoding="utf-8"))
if RULES["spec_version"] != 1:
    raise SystemExit("unsupported sanitize_rules.json spec_version %r"
                     % RULES["spec_version"])

# Keys whose values are content by definition: always stripped, any length.
CONTENT_KEYS = set(RULES["content_keys"])
# Keys ("content") whose STRING values are stripped but whose list/dict
# values keep their structure and are recursed into.
STRING_CONTENT_KEYS = set(RULES["string_content_keys"])
# Keys whose string values are known-safe metadata. Id-shaped values are
# pseudonymized and path-shaped values are aliased regardless of this list;
# everything else longer than MAX_FREE_LEN is stripped.
SAFE_KEYS = set(RULES["safe_keys"])
MAX_FREE_LEN = RULES["max_free_len"]  # UTF-8 bytes

PLACEHOLDER_RE = re.compile(RULES["placeholder"]["regex"])
HASH_HEX_CHARS = RULES["placeholder"]["hash_hex_chars"]

UUID_RE = re.compile(RULES["id_shapes"]["uuid_regex"])
PREFIX_ID_RE = re.compile("^(%s)_(%s)$" % (
    "|".join(RULES["id_shapes"]["prefix_id_prefixes"]),
    RULES["id_shapes"]["prefix_id_body_regex"]))

# harvest-only tables (the DB sanitizer ignores these)
PATH_KEYED_MAPS = set(RULES["harvest"]["path_keyed_maps"])
GIT_BRANCH_KEYS = set(RULES["harvest"]["git_branch_keys"])
GIT_BRANCH_ALLOWLIST = set(RULES["harvest"]["git_branch_allowlist"])
# structural names plus owner-approved exceptions (distinct spec
# categories — see the spec's decision rule — identical sanitizer effect)
PATH_SEGMENT_ALLOWLIST = (set(RULES["harvest"]["path_segment_allowlist"])
                          | set(RULES["harvest"]["owner_approved_segments"]))
PATH_SEGMENT_ALLOWLIST_RES = [
    re.compile(p) for p in RULES["harvest"]["path_segment_allowlist_regexes"]]
VERBATIM_VALUE_KEYS = set(RULES["harvest"]["verbatim_value_keys"])


def is_structural_segment(seg):
    return seg in PATH_SEGMENT_ALLOWLIST or any(
        rx.match(seg) for rx in PATH_SEGMENT_ALLOWLIST_RES)

VECTORS_DIR = REPO_ROOT / "testdata" / "sanitizer-vectors"

HOME = ""
ENCODED_HOME = ""
USERNAME = ""


def set_identity(home):
    """Set the home-redaction identity. Real harvests use the actual home
    dir; the vector self-test pins /home/user so committed vectors are
    machine-independent. Project dir names encode the cwd with "/" -> "-",
    so $HOME appears as e.g. "-home-alice" at the start of the dir name;
    that encoded form also shows up INSIDE path strings and object keys."""
    global HOME, ENCODED_HOME, USERNAME
    HOME = home
    ENCODED_HOME = home.replace("/", "-")
    USERNAME = Path(home).name


set_identity(str(Path.home()))

# secret per-harvest salt + alias maps; set/loaded in main() from
# <out>/PROJECT_MAP.local.json so re-harvests produce stable names
SALT = ""
PROJECT_ALIASES = {}  # real project slug -> "project-XXXXXX"
SEGMENT_ALIASES = {}  # real path segment -> "d-XXXXXX[.ext]"
KEY_TOKENS = {}       # real path-valued object key -> "p-XXXXXXXX[.ext]"
ID_MAP = {}           # real identifier -> pseudonymized identifier


def salted(s):
    return hashlib.sha256((SALT + s).encode("utf-8")).hexdigest()


def placeholder(value):
    if not isinstance(value, str):
        # canonical JSON per the spec's nonstring_encoding: sorted keys,
        # compact, raw UTF-8 — must hash byte-identically to the Go side
        value = json.dumps(value, separators=(",", ":"),
                           ensure_ascii=False, sort_keys=True)
    raw = value.encode("utf-8")
    digest = hashlib.sha256(
        SALT.encode("utf-8") + raw).hexdigest()[:HASH_HEX_CHARS]
    return "<stripped len=%d sha256=%s>" % (len(raw), digest)


def redact_home_text(s):
    s = s.replace(HOME, "~")
    s = s.replace(ENCODED_HOME, "-home-user")
    # bare username belt-and-braces; skip very short usernames that would
    # mangle unrelated text
    if len(USERNAME) >= 4:
        s = s.replace(USERNAME, "user")
    return s


def split_ext(seg):
    i = seg.rfind(".")
    if 0 < i < len(seg) - 1 and len(seg) - i <= 6:
        return seg[:i], seg[i:]
    return seg, ""


def alias_project(slug):
    a = PROJECT_ALIASES.get(slug)
    if a is None:
        a = "project-" + salted(slug)[:6]
        PROJECT_ALIASES[slug] = a
    return a


def alias_dirsegment(seg):
    a = SEGMENT_ALIASES.get(seg)
    if a is None:
        _, ext = split_ext(seg)
        a = "d-" + salted(seg)[:6] + ext
        SEGMENT_ALIASES[seg] = a
    return a


def pseudo_id(value):
    """Stable pseudonym for UUIDs and msg_/req_/toolu_ ids; None otherwise."""
    cached = ID_MAP.get(value)
    if cached is not None:
        return cached
    if UUID_RE.match(value):
        d = salted(value)
        out = "%s-%s-%s-%s-%s" % (d[0:8], d[8:12], d[12:16], d[16:20], d[20:32])
    else:
        m = PREFIX_ID_RE.match(value)
        if m is None:
            return None
        prefix, body = m.groups()
        d = salted(value)
        while len(d) < len(body):
            d += salted(d)
        out = "%s_%s" % (prefix, d[:len(body)])
    ID_MAP[value] = out
    return out


def alias_encoded_dirname(name):
    """Alias '-home-user-Projects-<slug>'-style encoded cwd dir names."""
    name = redact_home_text(name)
    if name == "-home-user":
        return name
    for prefix in ("-home-user-Projects-", "-home-user-", "-tmp-"):
        if name.startswith(prefix) and len(name) > len(prefix):
            return prefix + alias_project(name[len(prefix):])
    return "-" + alias_project(name.lstrip("-"))


def is_pathlike(s):
    # relative paths too ("Projects/<slug>/...", "docs/x.md" — seen in
    # displayPath and similar fields); whitespace-free slash strings are
    # treated as paths, which over-aliases the odd mime-type-ish value but
    # never leaks a project name
    if s.startswith(("/", "~")) or (s.startswith(".") and "/" in s):
        return True
    return "/" in s and " " not in s and "\n" not in s and "\t" not in s


def alias_path_value(s):
    """Alias a path-shaped string value segment by segment.

    Owner ruling: EVERY segment after the project segment must be a
    sanitizer token (d-XXXXXX[.ext], or a pseudonymized id for session
    filenames). The structural allowlist therefore only applies BEFORE the
    project segment; encoded cwd dirnames (incl. bare "-home-user" for
    sessions run in $HOME) count as the project segment.
    """
    s = redact_home_text(s)
    out, project_seen = [], False
    for seg in s.split("/"):
        if not project_seen and is_structural_segment(seg):
            out.append(seg)
            continue
        if seg.startswith("-"):
            out.append(alias_encoded_dirname(seg))
            project_seen = True
            continue
        base, ext = split_ext(seg)
        pid = pseudo_id(base)
        if pid is not None:
            out.append(pid + ext)
            continue
        if not project_seen:
            out.append(alias_project(seg))
            project_seen = True
        else:
            out.append(alias_dirsegment(seg))
    return "/".join(out)


def looks_safe_short(s):
    return len(s.encode("utf-8")) <= MAX_FREE_LEN


# PATH_KEYED_MAPS (spec): maps that are path-keyed BY SCHEMA — every key
# is tokenized unconditionally, no path-likeness heuristics (audit caught
# extensionless keys like "Makefile" / "LICENSE" slipping through).


def key_token(k):
    """Replace an ENTIRE path-valued object key with a stable token,
    preserving only the extension."""
    tok = KEY_TOKENS.get(k)
    if tok is None:
        _, ext = split_ext(k)
        tok = "p-" + salted(k)[:8] + ext
        KEY_TOKENS[k] = tok
    return tok


def sanitize_key(k):
    """Object keys in Claude Code records can be file paths (keys of maps
    NOT in PATH_KEYED_MAPS, which are tokenized wholesale elsewhere). Those
    are parser-skipped noise: replace the ENTIRE key with a stable token
    preserving only the extension."""
    is_filename = ("." in k and " " not in k
                   and not k.replace(".", "").isdigit())  # ".gitignore" etc.
    if "/" in k or k.startswith(("/", "~")) or is_filename:
        return key_token(k)
    s = redact_home_text(k)
    if looks_safe_short(s):
        return s
    return placeholder(k)


def sanitize_value(node, key=None, harvest=True):
    """Recursively sanitize a decoded JSON value. Returns the sanitized value.

    The CONTENT rules (placeholder passthrough, content keys stripped whole
    regardless of value type, string-content keys like "content" stripped
    only when strings, safe keys / id shapes / path shapes / short strings
    kept, the rest placeholdered) are the shared spec semantics — identical
    to the Go DB sanitizer and held byte-equal by the contract vectors.

    harvest=True layers the publication-only behaviors on top: id
    pseudonymization, path/segment/key aliasing, gitBranch aliasing and
    home redaction. harvest=False ("contract mode") is the pure shared
    semantics, used by --check-vectors to compare against Go.
    """
    # Already-stripped markers stay verbatim, even under content keys —
    # the sanitizer is idempotent and fixture placeholders survive.
    if isinstance(node, str) and PLACEHOLDER_RE.match(node):
        return node
    if key in CONTENT_KEYS:
        # numbers/booleans/null cannot carry content; generic key names
        # collide across formats (opencode tokens.input is a NUMBER under
        # claude-code's tool-input key name) — see the spec doc
        if node is None or isinstance(node, (bool, int, float)):
            return node
        return placeholder(node)
    if key in STRING_CONTENT_KEYS and isinstance(node, str):
        return placeholder(node)
    if isinstance(node, dict):
        if harvest and key in PATH_KEYED_MAPS:
            return {key_token(k): sanitize_value(v, k, harvest)
                    for k, v in node.items()}
        if harvest:
            return {sanitize_key(k): sanitize_value(v, k, harvest)
                    for k, v in node.items()}
        # contract mode keeps object keys verbatim (the DB is local-first)
        return {k: sanitize_value(v, k, harvest) for k, v in node.items()}
    if isinstance(node, list):
        return [sanitize_value(v, key, harvest) for v in node]
    if isinstance(node, str):
        if harvest:
            if key in VERBATIM_VALUE_KEYS:
                # closed-vocabulary metadata (IANA timezones) — slash-shaped
                # but never project identity; aliasing would poison the map
                # with public names like Europe/Istanbul
                return node
            pid = pseudo_id(node)
            if pid is not None:
                return pid
            redacted = redact_home_text(node)
            if redacted == "-home-user" or \
                    redacted.startswith(("-home-user-", "-tmp-")):
                # a bare encoded cwd dirname as a VALUE (no slashes, so the
                # pathlike branch never sees it) carries project identity —
                # alias it like the dir name it is, not like free text
                # (gap found by harvest vector 02; redact_json already
                # handled this case for the -full expectation files)
                return alias_encoded_dirname(node)
            if key in GIT_BRANCH_KEYS:
                if node in GIT_BRANCH_ALLOWLIST:
                    return node
                if not is_pathlike(node):
                    return "branch-" + salted(node)[:6]
            if is_pathlike(node):
                return alias_path_value(node)
            s = redact_home_text(node)
            if key in SAFE_KEYS:
                return s
            if looks_safe_short(s):
                return s
            return placeholder(node)
        # contract mode mirrors Go: ids and paths pass through verbatim
        if (key in SAFE_KEYS or pseudo_id_shape(node) or is_pathlike(node)
                or looks_safe_short(node)):
            return node
        return placeholder(node)
    return node


def pseudo_id_shape(value):
    """True when value is id-shaped (UUID or msg_/req_/toolu_) — contract
    mode keeps these verbatim, mirroring the Go sanitizer."""
    return bool(UUID_RE.match(value) or PREFIX_ID_RE.match(value))


def sanitize_record(obj):
    return sanitize_value(obj)


# --- sources ------------------------------------------------------------------
#
# Each harvest source describes one agent's log store: discovery, per-line
# stat scanning (for selection), the snapshot/fixture tree layout, and the
# ccusage agent subcommand + env var used for expectations. The sanitizer
# itself is shared — codex-specific CONTENT fields live in the rule-spec,
# never here (milestone-2 Task 3 rule).

class FileStat:
    """Stats for one source log file. snap_rel is the file's path inside
    the snapshot/fixture trees with ORIGINAL names (e.g.
    projects/<dir>/<file>.jsonl or sessions/YYYY/MM/DD/<file>.jsonl);
    fixture naming is source-specific."""

    def __init__(self, path, snap_rel, scan_line):
        self.path = path
        self.source = path  # original location (snapshot copies override this)
        self.snap_rel = Path(snap_rel)
        self.scan_line = scan_line
        self.mtime = path.stat().st_mtime
        self.size = path.stat().st_size
        self.lines = 0
        self.malformed = 0
        self.usage_msgs = 0
        self.models = set()
        self.cache_tokens = 0
        self.first_ts = None
        self.last_ts = None
        self.versions = set()
        self.criteria = []

    def scan(self):
        with open(self.path, "r", encoding="utf-8", errors="replace") as fh:
            for line in fh:
                if not line.strip():
                    continue
                self.lines += 1
                try:
                    obj = json.loads(line)
                except json.JSONDecodeError:
                    self.malformed += 1
                    continue
                if isinstance(obj, dict):
                    self.scan_line(self, obj)


def scan_line_claude(st, obj):
    ts = obj.get("timestamp")
    if isinstance(ts, str):
        if st.first_ts is None:
            st.first_ts = ts
        st.last_ts = ts
    ver = obj.get("version")
    if isinstance(ver, str):
        st.versions.add(ver)
    msg = obj.get("message")
    if obj.get("type") == "assistant" and isinstance(msg, dict):
        usage = msg.get("usage")
        if isinstance(usage, dict):
            st.usage_msgs += 1
            model = msg.get("model")
            if isinstance(model, str):
                st.models.add(model)
            for k in ("cache_creation_input_tokens",
                      "cache_read_input_tokens"):
                v = usage.get(k)
                if isinstance(v, (int, float)):
                    st.cache_tokens += int(v)


def scan_line_codex(st, obj):
    """Codex rollout records: top-level {timestamp, type, payload}. Usage
    rides on event_msg token_count payloads (payload.info); the model on
    turn_context; the CLI version on session_meta."""
    ts = obj.get("timestamp")
    if isinstance(ts, str):
        if st.first_ts is None:
            st.first_ts = ts
        st.last_ts = ts
    pl = obj.get("payload")
    if not isinstance(pl, dict):
        return
    t = obj.get("type")
    if t == "session_meta":
        ver = pl.get("cli_version")
        if isinstance(ver, str):
            st.versions.add(ver)
    elif t == "turn_context":
        model = pl.get("model")
        if isinstance(model, str):
            st.models.add(model)
    elif pl.get("type") == "token_count":
        info = pl.get("info")
        if isinstance(info, dict):
            st.usage_msgs += 1
            last = info.get("last_token_usage")
            if isinstance(last, dict):
                v = last.get("cached_input_tokens")
                if isinstance(v, (int, float)):
                    st.cache_tokens += int(v)


def discover_claude(overrides):
    if overrides:
        candidates = [Path(p).expanduser() for p in overrides]
    else:
        candidates = []
        env = os.environ.get("CLAUDE_CONFIG_DIR")
        if env:
            for part in env.split(","):
                candidates.append(Path(part.strip()).expanduser() / "projects")
        candidates.append(Path.home() / ".claude" / "projects")
        candidates.append(Path.home() / ".config" / "claude" / "projects")
    seen, found = set(), []
    for c in candidates:
        rc = c.resolve()
        if rc in seen:
            continue
        seen.add(rc)
        if c.is_dir():
            found.append(c)
    return found


def discover_codex(overrides):
    if overrides:
        candidates = [Path(p).expanduser() for p in overrides]
    else:
        env = os.environ.get("CODEX_HOME")
        base = Path(env).expanduser() if env else Path.home() / ".codex"
        candidates = [base / "sessions"]
    return [c for c in candidates if c.is_dir()]


def discover_opencode(overrides):
    """OpenCode stores per-message JSON rows in a SQLite db (current
    format — the old storage/ directory-of-JSON layout is gone):
    $XDG_DATA_HOME/opencode/opencode.db. Overrides are dirs containing
    opencode.db."""
    if overrides:
        candidates = [Path(p).expanduser() for p in overrides]
    else:
        xdg = os.environ.get("XDG_DATA_HOME")
        base = Path(xdg).expanduser() if xdg else Path.home() / ".local" / "share"
        candidates = [base / "opencode"]
    return [c for c in candidates if (c / "opencode.db").is_file()]


def scan_all_claude(roots):
    stats = []
    for pd in roots:
        for f in sorted(pd.glob("*/*.jsonl")):
            st = FileStat(f, Path("projects") / f.parent.name / f.name,
                          scan_line_claude)
            try:
                st.scan()
            except OSError as exc:
                print("  ! skipping a session file: %s" % exc, file=sys.stderr)
                continue
            if st.lines > 0:
                stats.append(st)
    return stats


def scan_all_codex(roots):
    stats = []
    for root in roots:
        for f in sorted(root.rglob("*.jsonl")):
            st = FileStat(f, Path("sessions") / f.relative_to(root),
                          scan_line_codex)
            try:
                st.scan()
            except OSError as exc:
                print("  ! skipping a session file: %s" % exc, file=sys.stderr)
                continue
            if st.lines > 0:
                stats.append(st)
    return stats


# --- selection ---------------------------------------------------------------


def tag(stat, criterion, picked):
    if stat is None:
        return
    if stat.path not in picked:
        picked[stat.path] = stat
    picked[stat.path].criteria.append(criterion)


def select(stats, target):
    picked = {}
    with_usage = [s for s in stats if s.usage_msgs > 0]
    pool = with_usage or stats

    tag(max(pool, key=lambda s: s.lines, default=None), "long-session", picked)
    shorts = [s for s in with_usage if s.lines >= 2]
    tag(min(shorts or pool, key=lambda s: s.lines, default=None),
        "short-session", picked)
    cachey = [s for s in pool if s.cache_tokens > 0]
    tag(max(cachey, key=lambda s: s.cache_tokens, default=None),
        "cache-heavy", picked)
    switchy = [s for s in pool if len(s.models) >= 2]
    tag(max(switchy, key=lambda s: s.usage_msgs, default=None),
        "model-switch", picked)
    tag(min(pool, key=lambda s: s.first_ts or "9999", default=None),
        "oldest", picked)
    tag(max(pool, key=lambda s: s.last_ts or "", default=None),
        "newest", picked)
    for s in sorted(pool, key=lambda s: s.mtime, reverse=True)[:3]:
        tag(s, "recent-full", picked)

    # fill with a deterministic time-spread sample up to the target count
    remaining = [s for s in sorted(pool, key=lambda s: s.mtime)
                 if s.path not in picked]
    need = max(0, min(target, len(pool)) - len(picked))
    if need and remaining:
        step = max(1, len(remaining) // need)
        for s in remaining[::step][:need]:
            tag(s, "fill-spread", picked)
    return sorted(picked.values(), key=lambda s: s.mtime)


# --- fixture writing ---------------------------------------------------------


UUID_TAIL_RE = re.compile(
    r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
    r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")


def fixture_rel_claude(stat):
    """Aliased project dir + pseudonymized session filename."""
    proj = alias_encoded_dirname(stat.snap_rel.parts[1])
    stem = stat.path.stem
    new_stem = pseudo_id(stem) or stem
    return Path("projects") / proj / (new_stem + stat.path.suffix)


def fixture_rel_codex(stat):
    """Same sessions/YYYY/MM/DD tree (ccusage codex needs it); the session
    uuid embedded at the end of the rollout filename is pseudonymized with
    the SAME map as the in-record ids, so file/record identity stays
    consistent. The rollout timestamp prefix is preserved (timestamps are
    never redacted)."""
    stem = stat.path.stem
    m = UUID_TAIL_RE.search(stem)
    if m:
        stem = stem[:m.start()] + pseudo_id(m.group(0))
    return stat.snap_rel.parent / (stem + stat.path.suffix)


def write_fixture(stat, out_root, rel):
    dest = out_root / rel
    dest.parent.mkdir(parents=True, exist_ok=True)
    written = 0
    with open(stat.path, "r", encoding="utf-8", errors="replace") as src, \
            open(dest, "w", encoding="utf-8") as out:
        for line in src:
            if not line.strip():
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                out.write(json.dumps(
                    {"_tatitok_malformed_source_line": placeholder(line)},
                    separators=(",", ":"), ensure_ascii=False) + "\n")
                written += 1
                continue
            out.write(json.dumps(sanitize_record(obj),
                                 separators=(",", ":"),
                                 ensure_ascii=False) + "\n")
            written += 1
    rel = dest.relative_to(out_root)
    if written != stat.lines:
        raise RuntimeError("line count drifted: %d -> %d (%s)"
                           % (stat.lines, written, rel))
    return str(rel)


def snapshot_selected(selected, snap_root):
    """Copy each selected file ONCE into a temp tree and rescan.

    Claude Code appends to live session logs while we run; sanitizing and
    expectation-verifying from the same frozen snapshot is the only way to
    keep everything consistent. The snapshot keeps ORIGINAL names/content
    (it is temp-only, never committed) — it exists to verify that ccusage
    totals on originals == totals on the sanitized tree.
    """
    frozen = []
    for stat in selected:
        dest = snap_root / stat.snap_rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(stat.path, dest)
        fresh = FileStat(dest, stat.snap_rel, stat.scan_line)
        fresh.scan()
        fresh.criteria = stat.criteria
        fresh.source = stat.path
        frozen.append(fresh)
    return frozen


# --- ccusage expectations ----------------------------------------------------


def resolve_ccusage_version(meta_path, override):
    if override:
        return override
    if meta_path.is_file():
        with open(meta_path, "r", encoding="utf-8") as fh:
            meta = json.load(fh)
        ver = meta.get("ccusage_version")
        if ver:
            print("  pinned ccusage version from META.json: %s" % ver)
            return ver
    out = subprocess.run(["npm", "view", "ccusage", "version"],
                         capture_output=True, text=True, timeout=120)
    if out.returncode != 0:
        raise RuntimeError("npm view ccusage version failed: %s" % out.stderr)
    ver = out.stdout.strip()
    print("  resolved latest ccusage version: %s (now pinned)" % ver)
    return ver


def run_ccusage(version, agent, subcommand, env_overrides):
    # ccusage >= v20 is multi-agent (codex, opencode, ...) and mixes every
    # detected agent's usage into the bare `daily`/`session` commands; the
    # agent subcommand scopes the report to one log store, pointed at via
    # the source's env overrides (a dict — opencode discovery is
    # HOME-anchored and needs more than one variable).
    cmd = (["npx", "-y", "ccusage@%s" % version, agent] + subcommand.split()
           + ["--json", "--offline"])
    env = dict(os.environ)
    env.update({k: str(v) for k, v in env_overrides.items()})
    out = subprocess.run(cmd, capture_output=True, text=True, env=env,
                         timeout=1800)
    if out.returncode != 0:
        raise RuntimeError("%s failed (%d):\n%s"
                           % (" ".join(cmd), out.returncode, out.stderr[-2000:]))
    return json.loads(out.stdout), cmd


def redact_json(node):
    """Username/project redaction for the full-history expectation files:
    redact home/username in every string and apply the SAME project alias
    map to encoded project dir names (e.g. session rows' projectPath).
    Identifiers and timestamps are left untouched in the -full set."""
    if isinstance(node, dict):
        return {redact_home_text(k): redact_json(v) for k, v in node.items()}
    if isinstance(node, list):
        return [redact_json(v) for v in node]
    if isinstance(node, str):
        if node.startswith("-"):
            return alias_encoded_dirname(node)
        if is_pathlike(node):
            return alias_path_value(node)
        return redact_home_text(node)
    return node


TOTAL_KEYS = ("inputTokens", "outputTokens",
              "cacheCreationTokens", "cacheReadTokens")


FULL_EXPECTATION_FILES = ("ccusage-daily-full.json",
                          "ccusage-session-full.json")


# --- opencode (sqlite-backed source) -------------------------------------------


class SessionStat:
    """Per-session stats over opencode message rows — quacks like FileStat
    for select(): lines/usage_msgs/models/cache_tokens/first_ts/last_ts/
    mtime/criteria/path."""

    def __init__(self, session_id):
        self.session_id = session_id
        self.path = session_id  # unique selection key
        self.lines = 0          # message rows
        self.malformed = 0
        self.usage_msgs = 0
        self.models = set()
        self.providers = set()
        self.cache_tokens = 0
        self.first_ts = None
        self.last_ts = None
        self.mtime = 0
        self.criteria = []


def epoch_ms_iso(ms):
    return datetime.datetime.fromtimestamp(
        ms / 1000.0, datetime.timezone.utc).isoformat(timespec="milliseconds")


def build_opencode_db(home_dir, ddl, rows):
    """Construct an opencode.db an unmodified ccusage can read, at the
    HOME-anchored location ccusage discovers
    (<home>/.local/share/opencode/opencode.db — XDG_DATA_HOME is ignored;
    verified by strace + empty-store probes). ONLY the message table:
    empirically sufficient for daily AND session reports under proper
    isolation. rows: (id, session_id, time_created, time_updated,
    data_json_str)."""
    d = Path(home_dir) / ".local" / "share" / "opencode"
    d.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(d / "opencode.db")
    con.execute(ddl)
    con.executemany("INSERT INTO message VALUES (?,?,?,?,?)", rows)
    con.commit()
    con.close()


def harvest_opencode(args, src, out_root, expected_dir, map_path):
    """The opencode pipeline: the unit of selection is a SESSION (rows of
    the message table grouped by session_id), the committed fixture is a
    per-session JSONL of sanitized rows plus the message-table DDL — no
    binary in git; the db ccusage and the adapter read is CONSTRUCTED from
    that text (here for expectation capture, in Go tests at test time)."""
    roots = discover_opencode(args.config_dir)
    if not roots:
        print("error: no opencode data dir found (set $XDG_DATA_HOME or "
              "--config-dir)", file=sys.stderr)
        return 1
    db_path = roots[0] / "opencode.db"
    print("log roots: %s" % redact_home_text(str(db_path)))

    con = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True)
    ddl = con.execute(
        "SELECT sql FROM sqlite_master WHERE name='message'").fetchone()[0]
    # freeze NOW: one consistent read of every row (the live db grows
    # while we run; fixtures + self-check must describe identical rows)
    all_rows = con.execute(
        "SELECT id, session_id, time_created, time_updated, data "
        "FROM message ORDER BY time_created, id").fetchall()
    con.close()

    stats = {}
    for rid, sid, tc, tu, data in all_rows:
        st = stats.get(sid)
        if st is None:
            st = stats[sid] = SessionStat(sid)
        st.lines += 1
        st.mtime = max(st.mtime, tu / 1000.0)
        iso = epoch_ms_iso(tc)
        if st.first_ts is None or iso < st.first_ts:
            st.first_ts = iso
        if st.last_ts is None or iso > st.last_ts:
            st.last_ts = iso
        try:
            obj = json.loads(data)
        except json.JSONDecodeError:
            st.malformed += 1
            continue
        if not isinstance(obj, dict):
            continue
        if obj.get("role") == "assistant":
            tokens = obj.get("tokens")
            if isinstance(tokens, dict):
                st.usage_msgs += 1
                model = obj.get("modelID")
                if isinstance(model, str):
                    st.models.add(model)
                prov = obj.get("providerID")
                if isinstance(prov, str):
                    st.providers.add(prov)
                cache = tokens.get("cache")
                if isinstance(cache, dict):
                    for k in ("read", "write"):
                        v = cache.get(k)
                        if isinstance(v, (int, float)):
                            st.cache_tokens += int(v)

    if not stats:
        print("error: no message rows found", file=sys.stderr)
        return 1
    print("scanning sessions ...")
    print("  %d sessions, %d with usage messages"
          % (len(stats), sum(1 for s in stats.values() if s.usage_msgs)))

    selected = select(list(stats.values()), args.files)
    sel_ids = {s.session_id for s in selected}
    print("selected %d fixture sessions:" % len(selected))

    out_root.mkdir(parents=True, exist_ok=True)
    (out_root / "schema.sql").write_text(
        "-- message-table DDL captured verbatim from the live opencode.db\n"
        "-- (public drizzle-generated structure; the ONLY table ccusage\n"
        "-- opencode needs — verified empirically). Go tests reconstruct\n"
        "-- the fixture db from this DDL + messages/*.jsonl.\n"
        + ddl + ";\n", encoding="utf-8")

    manifest_sessions, sources = [], {}
    fixture_rows = []  # sanitized, for the expectation-capture db
    for st in selected:
        sid_alias = pseudo_id(st.session_id) or st.session_id
        rel = Path("messages") / (sid_alias + ".jsonl")
        dest = out_root / rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        written = 0
        with open(dest, "w", encoding="utf-8") as out:
            for rid, sid, tc, tu, data in all_rows:
                if sid != st.session_id:
                    continue
                try:
                    obj = json.loads(data)
                    sanitized = sanitize_record(obj)
                except json.JSONDecodeError:
                    sanitized = {"_tatitok_malformed_source_data":
                                 placeholder(data)}
                data_str = json.dumps(sanitized, separators=(",", ":"),
                                      ensure_ascii=False)
                row = {
                    "id": pseudo_id(rid) or rid,
                    "session_id": pseudo_id(sid) or sid,
                    "time_created": tc,
                    "time_updated": tu,
                    "data": json.loads(data_str),
                }
                out.write(json.dumps(row, separators=(",", ":"),
                                     ensure_ascii=False) + "\n")
                fixture_rows.append((row["id"], row["session_id"],
                                     tc, tu, data_str))
                written += 1
        if written != st.lines:
            raise RuntimeError("row count drifted: %d -> %d (%s)"
                               % (st.lines, written, rel))
        print("  %-60s %s" % (rel, ",".join(st.criteria)))
        sources[st.session_id] = str(rel)
        manifest_sessions.append({
            "session": pseudo_id(st.session_id) or st.session_id,
            "fixture": str(rel),
            "criteria": st.criteria,
            "messages": st.lines,
            "malformed_data": st.malformed,
            "usage_messages": st.usage_msgs,
            "models": sorted(st.models),
            "providers": sorted(st.providers),
            "first_timestamp": st.first_ts,
            "last_timestamp": st.last_ts,
        })

    manifest = {
        "harvest_date": datetime.datetime.now(datetime.timezone.utc)
                        .isoformat(timespec="seconds"),
        "source": args.source,
        "machine_label": args.label,
        "platform": platform.platform(),
        "source_format": "sqlite message table (opencode.db); fixtures are "
                         "sanitized per-session row JSONL + schema.sql, the "
                         "db is reconstructed from them",
        "log_roots": [redact_home_text(str(p)) for p in roots],
        "sessions": manifest_sessions,
    }
    (out_root / "MANIFEST.json").write_text(
        json.dumps(manifest, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8")
    print("wrote MANIFEST.json")

    write_map(map_path, sources)
    print("wrote %s (SECRET, gitignored)" % map_path.name)

    if args.no_expectations:
        print("skipping ccusage expectations (--no-expectations)")
        return 0
    if shutil.which("npx") is None or shutil.which("npm") is None:
        print("error: npx/npm not found", file=sys.stderr)
        return 1

    print("capturing ccusage expectations ...")
    meta_path = expected_dir / "META.json"
    version = resolve_ccusage_version(meta_path, args.ccusage_version)
    t0 = time.time()
    with tempfile.TemporaryDirectory(prefix="tatitok-oc-fix-") as fix_xdg, \
            tempfile.TemporaryDirectory(prefix="tatitok-oc-snap-") as snap_xdg:
        # fixture db from the SANITIZED rows; snapshot db from the frozen
        # ORIGINAL rows of the same sessions
        build_opencode_db(fix_xdg, ddl, fixture_rows)
        build_opencode_db(snap_xdg, ddl,
                          [r for r in all_rows if r[1] in sel_ids])
        commands = capture_expectations(src, version, fix_xdg, snap_xdg,
                                        roots, expected_dir)
    meta = {
        "ccusage_version": version,
        "source": args.source,
        "captured_at": datetime.datetime.now(datetime.timezone.utc)
                       .isoformat(timespec="seconds"),
        "timezone": local_timezone(),
        "machine_label": args.label,
        "commands": commands,
        "note": "fixture expectations captured from a db RECONSTRUCTED from "
                "the committed sanitized rows (messages/*.jsonl + "
                "schema.sql) — no binary fixture exists. The -full variants "
                "are LOCAL-ONLY and gitignored; make parity-full-opencode "
                "recaptures from the live db at comparison time.",
    }
    meta_path.write_text(json.dumps(meta, indent=2, ensure_ascii=False) + "\n",
                         encoding="utf-8")
    print("wrote expected/META.json (ccusage %s, %.0fs)"
          % (version, time.time() - t0))
    print("done — review the fixtures for leaked content, then commit.")
    return 0


# Per-source harvest configuration. total_keys: the totals the
# sanitized-vs-original self-check must match exactly (codex reports
# reasoning/total token columns too — held to the same standard).
SOURCES = {
    "claude-code": {
        "agent": "claude",
        "env_var": "CLAUDE_CONFIG_DIR",
        "discover": discover_claude,
        "scan_all": scan_all_claude,
        "fixture_rel": fixture_rel_claude,
        "versions_key": "claude_code_versions_seen",
        "total_keys": TOTAL_KEYS,
        # ccusage env overrides for a capture target
        "capture_env": lambda v: {"CLAUDE_CONFIG_DIR": str(v)},
        # ccusage env value covering the FULL history (all roots)
        "full_env": lambda roots: ",".join(str(p.parent) for p in roots),
    },
    "codex": {
        "agent": "codex",
        "env_var": "CODEX_HOME",
        "discover": discover_codex,
        "scan_all": scan_all_codex,
        "fixture_rel": fixture_rel_codex,
        "versions_key": "codex_cli_versions_seen",
        "total_keys": TOTAL_KEYS + ("reasoningOutputTokens", "totalTokens"),
        "capture_env": lambda v: {"CODEX_HOME": str(v)},
        # CODEX_HOME is the dir CONTAINING sessions/ (no list support)
        "full_env": lambda roots: str(roots[0].parent),
    },
    "opencode": {
        "agent": "opencode",
        "env_var": "HOME",
        "custom_harvest": harvest_opencode,  # sqlite-backed pipeline
        "total_keys": TOTAL_KEYS + ("totalTokens",),
        # ccusage opencode discovery is HOME-anchored
        # ($HOME/.local/share/opencode/opencode.db; XDG_DATA_HOME is
        # IGNORED — verified by strace + empty-store probes). Override
        # HOME to the capture target (db at
        # <target>/.local/share/opencode/opencode.db); pin the npm cache
        # so npx still finds the pinned ccusage; set XDG_DATA_HOME
        # consistently in case a future ccusage becomes XDG-aware.
        "capture_env": lambda v: {
            "HOME": str(v),
            "XDG_DATA_HOME": str(Path(v) / ".local" / "share"),
            "npm_config_cache": str(Path.home() / ".npm"),
        },
        # full history = the real home (live db)
        "full_env": lambda roots: str(roots[0].parents[2]),
    },
}


def capture_expectations(src, version, fixture_root, snap_root, roots,
                         expected_dir):
    expected_dir.mkdir(parents=True, exist_ok=True)
    # fail BEFORE the expensive ccusage runs if the local-only -full pair
    # could be committed (they keep real session/message ids)
    for fname in FULL_EXPECTATION_FILES:
        assert_unstageable(expected_dir / fname,
                           "full-history expectations keep real ids — "
                           "local-only by owner ruling")
    commands = {}
    agent, env_var = src["agent"], src["env_var"]

    # fixture-scoped set (CI): captured from the SANITIZED tree itself, so
    # the committed expectations match the committed fixtures by construction
    fixture_daily = None
    for sub, fname in (("daily", "ccusage-daily.json"),
                       ("session", "ccusage-session.json")):
        data, cmd = run_ccusage(version, agent, sub,
                                src["capture_env"](fixture_root))
        if sub == "daily":
            fixture_daily = data
        (expected_dir / fname).write_text(
            json.dumps(data, indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8")
        commands[fname] = {
            "command": cmd,
            env_var: "<the sanitized fixture tree>",
            "scope": "fixture",
        }
        print("  wrote expected/%s" % fname)

    # self-check: ccusage on the frozen ORIGINAL snapshot must produce the
    # exact same token totals as on the sanitized tree — proves the
    # sanitizer preserved all billing-relevant data (usage, ids for dedup,
    # timestamps)
    orig_daily, _ = run_ccusage(version, agent, "daily",
                                src["capture_env"](snap_root))
    got = {k: fixture_daily["totals"][k] for k in src["total_keys"]}
    want = {k: orig_daily["totals"][k] for k in src["total_keys"]}
    if got != want:
        raise RuntimeError(
            "sanitized-tree ccusage totals diverge from original snapshot: "
            "sanitized=%r original=%r — sanitizer broke billing-relevant "
            "data, DO NOT commit" % (got, want))
    print("  self-check OK: sanitized-tree totals == original-snapshot totals")

    # full-history set: LOCAL-ONLY point-in-time reference (gitignored —
    # it keeps real session/message ids; owner ruling: zero real identifiers
    # in the committed tree). `make parity-full` recaptures from live logs
    # at comparison time and never reads these files.
    full_env = src["full_env"](roots)
    for sub, fname in zip(("daily", "session"), FULL_EXPECTATION_FILES):
        data, cmd = run_ccusage(version, agent, sub,
                                src["capture_env"](full_env))
        dest = expected_dir / fname
        dest.write_text(
            json.dumps(redact_json(data), indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8")
        assert_unstageable(dest,
                           "full-history expectations keep real ids — "
                           "local-only by owner ruling")
        commands[fname] = {
            "command": cmd,
            env_var: redact_home_text(str(full_env)),
            "scope": "full-history (LOCAL-ONLY, gitignored)",
        }
        print("  wrote expected/%s (local-only, gitignored)" % fname)
    return commands


def local_timezone():
    now = datetime.datetime.now().astimezone()
    name = None
    # /etc/localtime is what Node/ICU (and hence ccusage date bucketing)
    # actually follows; /etc/timezone can be stale (seen: Etc/UTC vs an
    # /etc/localtime symlink to Europe/Istanbul)
    if Path("/etc/localtime").is_symlink():
        target = os.readlink("/etc/localtime")
        if "zoneinfo/" in target:
            name = target.split("zoneinfo/")[-1]
    if name is None and Path("/etc/timezone").is_file():
        name = Path("/etc/timezone").read_text(encoding="utf-8").strip()
    return {
        "iana": name or os.environ.get("TZ"),
        "abbreviation": now.tzname(),
        "utc_offset": now.strftime("%z"),
    }


# --- secret map file ----------------------------------------------------------


def load_or_create_map(map_path):
    global SALT
    if map_path.is_file():
        data = json.loads(map_path.read_text(encoding="utf-8"))
        SALT = data["salt"]
        PROJECT_ALIASES.update(data.get("projects", {}))
        SEGMENT_ALIASES.update(data.get("segments", {}))
        KEY_TOKENS.update(data.get("keys", {}))
        ID_MAP.update(data.get("ids", {}))
        print("loaded existing salt + alias maps from %s" % map_path.name)
    else:
        SALT = secrets.token_hex(16)
        print("generated new harvest salt (kept only in %s)" % map_path.name)


def write_map(map_path, sources):
    map_path.write_text(json.dumps({
        "_warning": "SECRET — real project/id mapping + salt. Gitignored; "
                    "never commit, never share.",
        "salt": SALT,
        "projects": PROJECT_ALIASES,
        "segments": SEGMENT_ALIASES,
        "keys": KEY_TOKENS,
        "ids": ID_MAP,
        "sources": sources,
    }, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def assert_unstageable(path, why):
    """Owner ruling: local-only files (the secret map, the -full expectation
    pair) must be impossible to commit. Two independent checks: the path must
    be gitignored (check-ignore), and if it already exists on disk an explicit
    `git add -n` must refuse to stage it."""
    if shutil.which("git") is None:
        return
    cwd = str(path.parent.resolve())
    r = subprocess.run(["git", "check-ignore", "-q", path.name], cwd=cwd)
    if r.returncode != 0:
        raise RuntimeError(
            "%s is NOT gitignored — fix .gitignore before harvesting (%s)"
            % (path, why))
    if path.is_file():
        r = subprocess.run(["git", "add", "-n", "--", path.name],
                           cwd=cwd, capture_output=True, text=True)
        if r.returncode == 0:
            raise RuntimeError(
                "git add -n would stage %s despite the ignore rule — fix "
                ".gitignore before harvesting (%s)" % (path, why))


# --- sanitizer contract vectors ------------------------------------------------


def canonical_json(node):
    """Serialize exactly like Go's json.Marshal (the contract-vector
    encoding): sorted object keys, compact separators, raw UTF-8 — except
    the HTML characters and U+2028/U+2029, which Go escapes inside JSON
    strings (a global replace is safe: those characters can only occur
    inside string literals in serialized JSON)."""
    s = json.dumps(node, separators=(",", ":"), ensure_ascii=False,
                   sort_keys=True)
    return (s.replace("&", "\\u0026").replace("<", "\\u003c")
             .replace(">", "\\u003e")
             .replace("\u2028", "\\u2028").replace("\u2029", "\\u2029"))


def check_vectors(update=False):
    """Verify (or with update=True regenerate) the sanitizer vectors in
    testdata/sanitizer-vectors/. Returns (failures, checked_count).

    contract/ vectors run the pure shared semantics (harvest=False, empty
    salt) and must match their *.expected.json byte-for-byte; the Go test
    (internal/core/sanitize_vectors_test.go) asserts the same files, which
    holds the two implementations byte-equal. harvest/ vectors run the full
    harvest sanitizer with the committed pinned salt, the /home/user
    identity and fresh alias maps per vector, freezing the publication
    rules (pseudonymization, aliasing, path-keyed maps).

    The vectors are SYNTHETIC by design (explicitly allowed: the sanitizer
    is a pure function) — never harvested from real logs."""
    global SALT
    failures = []
    checked = 0

    def run_dir(subdir, fn):
        nonlocal checked
        raws = sorted((VECTORS_DIR / subdir).glob("*.raw.json"))
        if not raws:
            failures.append("%s/: no *.raw.json vectors found" % subdir)
        for raw_path in raws:
            exp_path = raw_path.with_name(
                raw_path.name[:-len(".raw.json")] + ".expected.json")
            try:
                node = json.loads(raw_path.read_text(encoding="utf-8"))
            except json.JSONDecodeError as exc:
                failures.append("%s: unparseable raw vector: %s"
                                % (raw_path.name, exc))
                continue
            got = fn(node) + "\n"
            checked += 1
            if update:
                exp_path.write_text(got, encoding="utf-8")
                continue
            if not exp_path.is_file():
                failures.append("%s: missing expected file %s"
                                % (raw_path.name, exp_path.name))
                continue
            if got != exp_path.read_text(encoding="utf-8"):
                failures.append(
                    "%s: sanitizer output diverges from committed %s — the "
                    "implementation no longer matches the frozen rule "
                    "behavior" % (raw_path.name, exp_path.name))

    saved_salt = SALT
    try:
        SALT = ""  # contract mode is unsalted, like the DB sanitizer
        run_dir("contract", lambda node: canonical_json(
            sanitize_value(node, None, harvest=False)))

        salt_file = VECTORS_DIR / "harvest" / "SALT"
        if not salt_file.is_file():
            failures.append("harvest/SALT missing — the pinned vector salt "
                            "must be committed for deterministic outputs")
        else:
            SALT = salt_file.read_text(encoding="utf-8").strip()
            set_identity("/home/user")  # machine-independent vectors

            def harvest_fn(node):
                for d in (PROJECT_ALIASES, SEGMENT_ALIASES,
                          KEY_TOKENS, ID_MAP):
                    d.clear()
                return json.dumps(sanitize_value(node, None, harvest=True),
                                  separators=(",", ":"), ensure_ascii=False)

            run_dir("harvest", harvest_fn)
    finally:
        SALT = saved_salt
        set_identity(str(Path.home()))
        for d in (PROJECT_ALIASES, SEGMENT_ALIASES, KEY_TOKENS, ID_MAP):
            d.clear()
    return failures, checked


def self_test_or_die():
    """The harvest flow refuses to touch real logs unless the sanitizer
    passes every committed vector."""
    failures, checked = check_vectors(update=False)
    if failures:
        for f in failures:
            print("VECTOR FAIL: %s" % f, file=sys.stderr)
        print("error: sanitizer self-test failed (%d vectors) — fix the "
              "sanitizer/spec before harvesting" % checked, file=sys.stderr)
        sys.exit(1)
    print("sanitizer self-test OK: %d vectors byte-identical" % checked)


# --- main ---------------------------------------------------------------------


def main():
    ap = argparse.ArgumentParser(
        description="Harvest sanitized Claude Code fixtures + ccusage "
                    "expectations (owner's machine only).")
    ap.add_argument("--out",
                    help="output root, e.g. testdata/fixtures/claude-code "
                         "(required unless --check-vectors/--update-vectors)")
    ap.add_argument("--check-vectors", action="store_true",
                    help="run the sanitizer contract vectors and exit "
                         "(no log access; safe anywhere)")
    ap.add_argument("--update-vectors", action="store_true",
                    help="REGENERATE the expected vector outputs from the "
                         "current sanitizer, then exit — review the diff "
                         "deliberately before committing")
    ap.add_argument("--label", default=None,
                    help="machine label (e.g. macbook, gx10); fixtures and "
                         "expectations go under <out>/<label>/ so harvests "
                         "from multiple machines don't overwrite each other")
    ap.add_argument("--source", default="claude-code",
                    choices=sorted(SOURCES),
                    help="which agent's logs to harvest (default claude-code)")
    ap.add_argument("--config-dir", action="append", default=None,
                    metavar="DIR",
                    help="explicit log root dir(s) (claude-code: a projects "
                         "dir; codex: a sessions dir); overrides discovery")
    ap.add_argument("--files", type=int, default=12,
                    help="target number of fixture files (8-15, default 12)")
    ap.add_argument("--ccusage-version", default=None,
                    help="pin/override the ccusage version")
    ap.add_argument("--no-expectations", action="store_true",
                    help="skip the ccusage expectation capture (fixtures only)")
    args = ap.parse_args()

    if args.check_vectors or args.update_vectors:
        failures, checked = check_vectors(update=args.update_vectors)
        if failures:
            for f in failures:
                print("VECTOR FAIL: %s" % f, file=sys.stderr)
            return 1
        print("sanitizer vectors %s: %d vectors OK"
              % ("regenerated" if args.update_vectors else "verified",
                 checked))
        return 0

    if not args.out:
        ap.error("--out is required when harvesting")
    if not 8 <= args.files <= 15:
        ap.error("--files must be between 8 and 15")

    # hard gate: the sanitizer must pass every committed vector before the
    # script is allowed anywhere near real logs
    self_test_or_die()

    out_root = Path(args.out)
    if args.label:
        out_root = out_root / args.label
    expected_dir = out_root / "expected"

    map_path = Path(args.out) / "PROJECT_MAP.local.json"
    map_path.parent.mkdir(parents=True, exist_ok=True)
    assert_unstageable(map_path,
                       "the map contains the salt and real project names")
    load_or_create_map(map_path)

    src = SOURCES[args.source]
    if "custom_harvest" in src:
        return src["custom_harvest"](args, src, out_root, expected_dir,
                                     map_path)

    roots = src["discover"](args.config_dir)
    if not roots:
        print("error: no %s log roots found (set $%s to override)"
              % (args.source, src["env_var"]), file=sys.stderr)
        return 1
    print("log roots: %s" % ", ".join(redact_home_text(str(p))
                                      for p in roots))

    print("scanning session files ...")
    stats = src["scan_all"](roots)
    if not stats:
        print("error: no session JSONL files found", file=sys.stderr)
        return 1
    print("  %d files, %d with usage messages"
          % (len(stats), sum(1 for s in stats if s.usage_msgs)))

    selected = select(stats, args.files)
    print("selected %d fixture files:" % len(selected))

    out_root.mkdir(parents=True, exist_ok=True)
    snap_ctx = tempfile.TemporaryDirectory(prefix="tatitok-harvest-snap-")
    snap_root = Path(snap_ctx.name)
    # freeze the selected files: live session logs grow while we run, and the
    # fixtures + expectation self-check must come from identical bytes
    selected = snapshot_selected(selected, snap_root)

    manifest_files, sources = [], {}
    for stat in selected:
        rel = write_fixture(stat, out_root, src["fixture_rel"](stat))
        print("  %-72s %s" % (rel, ",".join(stat.criteria)))
        sources[str(stat.source)] = rel
        manifest_files.append({
            "source": alias_path_value(str(stat.source)),
            "fixture": rel,
            "criteria": stat.criteria,
            "lines": stat.lines,
            "malformed_lines": stat.malformed,
            "usage_messages": stat.usage_msgs,
            "models": sorted(stat.models),
            "first_timestamp": stat.first_ts,
            "last_timestamp": stat.last_ts,
            "source_bytes": stat.size,
        })

    versions = set()
    for s in selected:
        versions.update(s.versions)
    manifest = {
        "harvest_date": datetime.datetime.now(datetime.timezone.utc)
                        .isoformat(timespec="seconds"),
        "source": args.source,
        "machine_label": args.label,
        "platform": platform.platform(),
        src["versions_key"]: sorted(versions),
        "log_roots": [redact_home_text(str(p)) for p in roots],
        "files": manifest_files,
    }
    (out_root / "MANIFEST.json").write_text(
        json.dumps(manifest, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8")
    print("wrote MANIFEST.json")

    write_map(map_path, sources)
    print("wrote %s (SECRET, gitignored)" % map_path.name)

    if args.no_expectations:
        print("skipping ccusage expectations (--no-expectations)")
        snap_ctx.cleanup()
        return 0

    if shutil.which("npx") is None or shutil.which("npm") is None:
        print("error: npx/npm not found — install Node, or re-run with "
              "--no-expectations and capture expectations separately",
              file=sys.stderr)
        snap_ctx.cleanup()
        return 1

    print("capturing ccusage expectations ...")
    meta_path = expected_dir / "META.json"
    version = resolve_ccusage_version(meta_path, args.ccusage_version)
    t0 = time.time()
    try:
        commands = capture_expectations(src, version, out_root, snap_root,
                                        roots, expected_dir)
    finally:
        snap_ctx.cleanup()
    meta = {
        "ccusage_version": version,
        "source": args.source,
        "captured_at": datetime.datetime.now(datetime.timezone.utc)
                       .isoformat(timespec="seconds"),
        "timezone": local_timezone(),
        "machine_label": args.label,
        "commands": commands,
        "note": "ccusage-daily.json / ccusage-session.json are captured from "
                "the sanitized fixture tree (CI parity set). The -full "
                "variants cover the machine's complete history but are "
                "LOCAL-ONLY point-in-time references: gitignored, never "
                "committed (they keep real ids). make parity-full recaptures "
                "from live logs at comparison time and never reads a "
                "committed -full file.",
    }
    meta_path.write_text(json.dumps(meta, indent=2, ensure_ascii=False) + "\n",
                         encoding="utf-8")
    print("wrote expected/META.json (ccusage %s, %.0fs)"
          % (version, time.time() - t0))
    print("done — review the fixtures for leaked content, then commit.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
