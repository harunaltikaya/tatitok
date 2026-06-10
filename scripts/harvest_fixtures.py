#!/usr/bin/env python3
"""Harvest sanitized Claude Code log fixtures + ccusage expectations.

Runs on the OWNER's machine (never CI). Python 3.9+, stdlib only.

Usage:
    python scripts/harvest_fixtures.py --out testdata/fixtures/claude-code
    python scripts/harvest_fixtures.py --out testdata/fixtures/claude-code --label gx10

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
       and expected/*.json projectPath alike; path segments after the
       project segment become d-<6hex>[.ext]; path-valued object KEYS
       (readFileState / trackedFileBackups maps) are replaced whole with
       p-<8hex>[.ext];
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

Placeholder format (the Go sanitizer in internal/core must match):
    <stripped len=N sha256=H>
  N = UTF-8 byte length of the original string; H = first 12 lowercase hex
  chars of sha256(salt_utf8 + original_utf8). Non-string content values are
  serialized with json.dumps(v, separators=(",", ":"), ensure_ascii=False)
  first.
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
import subprocess
import sys
import tempfile
import time
from pathlib import Path

# --- sanitization rules -----------------------------------------------------

# Keys whose values are content by definition: always stripped, any length.
# "content" is special-cased: recursed into when it is a list/dict (the
# message.content array structure is preserved), stripped when it is a string.
CONTENT_KEYS = {
    "text",
    "thinking",
    "summary",
    "input",
    "attachments",
    "toolUseResult",
    "signature",  # thinking-block crypto signature: long, content-adjacent
    "prompt",
    "stdout",
    "stderr",
    # short content-bearing fields found in real logs that slip under the
    # 80-char conservative rule: AI-generated session titles, the user's
    # last prompt, task-reminder subjects/descriptions
    "aiTitle",
    "lastPrompt",
    "subject",
    "description",
}

# Keys whose string values are known-safe metadata. Id-shaped values are
# pseudonymized and path-shaped values are aliased regardless of this list;
# everything else longer than MAX_FREE_LEN is stripped.
SAFE_KEYS = {
    "id",
    "uuid",
    "parentUuid",
    "leafUuid",
    "sessionId",
    "requestId",
    "request_id",
    "message_id",
    "messageId",
    "promptId",
    "timestamp",
    "type",
    "subtype",
    "role",
    "model",
    "version",
    "cwd",
    "gitBranch",
    "userType",
    "name",
    "tool_use_id",
    "toolUseID",
    "stop_reason",
    "stopReason",
    "stop_sequence",
    "service_tier",
    "slug",
    "entrypoint",
    "permissionMode",
    "promptSource",
}

MAX_FREE_LEN = 80  # unknown string fields longer than this get stripped

HOME = str(Path.home())
# project dir names encode the cwd with "/" -> "-", so $HOME appears as e.g.
# "-home-alice" or "-Users-alice" at the start of the dir name; that encoded
# form also shows up INSIDE path strings and object keys in the logs
ENCODED_HOME = HOME.replace("/", "-")
USERNAME = Path.home().name

# secret per-harvest salt + alias maps; set/loaded in main() from
# <out>/PROJECT_MAP.local.json so re-harvests produce stable names
SALT = ""
PROJECT_ALIASES = {}  # real project slug -> "project-XXXXXX"
SEGMENT_ALIASES = {}  # real path segment -> "d-XXXXXX[.ext]"
KEY_TOKENS = {}       # real path-valued object key -> "p-XXXXXXXX[.ext]"
ID_MAP = {}           # real identifier -> pseudonymized identifier

UUID_RE = re.compile(
    r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
    r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
PREFIX_ID_RE = re.compile(r"^(msg|req|toolu)_([A-Za-z0-9]+)$")

# structural path segments that carry no project identity
PATH_SEGMENT_ALLOWLIST = {
    "", "~", "home", "user", "tmp", "var", "opt", "usr", "etc",
    "Projects", "projects", ".claude", ".config", "claude",
    "memory", "plans", "todos", "sessions",
}

GIT_BRANCH_ALLOWLIST = {"", "main", "master", "develop", "HEAD"}


def salted(s):
    return hashlib.sha256((SALT + s).encode("utf-8")).hexdigest()


def placeholder(value):
    if not isinstance(value, str):
        value = json.dumps(value, separators=(",", ":"), ensure_ascii=False)
    raw = value.encode("utf-8")
    digest = hashlib.sha256(SALT.encode("utf-8") + raw).hexdigest()[:12]
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
    """Alias a path-shaped string value segment by segment."""
    s = redact_home_text(s)
    out, project_seen = [], False
    for seg in s.split("/"):
        if seg in PATH_SEGMENT_ALLOWLIST or seg == "-home-user":
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


def sanitize_key(k):
    """Object keys in Claude Code records can be file paths (e.g. the
    readFileState / trackedFileBackups maps are keyed by file path). Those
    are parser-skipped noise: replace the ENTIRE key with a stable token
    preserving only the extension."""
    is_filename = ("." in k and " " not in k
                   and not k.replace(".", "").isdigit())  # ".gitignore" etc.
    if "/" in k or k.startswith(("/", "~")) or is_filename:
        tok = KEY_TOKENS.get(k)
        if tok is None:
            _, ext = split_ext(k)
            tok = "p-" + salted(k)[:8] + ext
            KEY_TOKENS[k] = tok
        return tok
    s = redact_home_text(k)
    if looks_safe_short(s):
        return s
    return placeholder(k)


def sanitize_value(node, key=None):
    """Recursively sanitize a decoded JSON value. Returns the sanitized value.

    Content keys are stripped whole regardless of value type (an entire
    tool_use input object becomes one placeholder). "content" is special:
    a string is stripped, but a list/dict (the message.content block array)
    keeps its structure and is recursed into.
    """
    if key in CONTENT_KEYS:
        return placeholder(node)
    if key == "content" and isinstance(node, str):
        return placeholder(node)
    if isinstance(node, dict):
        return {sanitize_key(k): sanitize_value(v, k) for k, v in node.items()}
    if isinstance(node, list):
        return [sanitize_value(v, key) for v in node]
    if isinstance(node, str):
        pid = pseudo_id(node)
        if pid is not None:
            return pid
        if is_pathlike(node):
            return alias_path_value(node)
        if key == "gitBranch":
            if node in GIT_BRANCH_ALLOWLIST:
                return node
            return "branch-" + salted(node)[:6]
        s = redact_home_text(node)
        if key in SAFE_KEYS:
            return s
        if looks_safe_short(s):
            return s
        return placeholder(node)
    return node


def sanitize_record(obj):
    return sanitize_value(obj)


# --- file scanning -----------------------------------------------------------


class FileStat:
    def __init__(self, path, project_dir):
        self.path = path
        self.source = path  # original location (snapshot copies override this)
        self.project_dir = project_dir
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
                if not isinstance(obj, dict):
                    continue
                ts = obj.get("timestamp")
                if isinstance(ts, str):
                    if self.first_ts is None:
                        self.first_ts = ts
                    self.last_ts = ts
                ver = obj.get("version")
                if isinstance(ver, str):
                    self.versions.add(ver)
                msg = obj.get("message")
                if obj.get("type") == "assistant" and isinstance(msg, dict):
                    usage = msg.get("usage")
                    if isinstance(usage, dict):
                        self.usage_msgs += 1
                        model = msg.get("model")
                        if isinstance(model, str):
                            self.models.add(model)
                        for k in ("cache_creation_input_tokens",
                                  "cache_read_input_tokens"):
                            v = usage.get(k)
                            if isinstance(v, (int, float)):
                                self.cache_tokens += int(v)


def discover_project_dirs(overrides):
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


def scan_all(project_dirs):
    stats = []
    for pd in project_dirs:
        for f in sorted(pd.glob("*/*.jsonl")):
            st = FileStat(f, f.parent.name)
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


def fixture_name(stat):
    """Aliased project dir + pseudonymized session filename."""
    proj = alias_encoded_dirname(stat.project_dir)
    stem = stat.path.stem
    new_stem = pseudo_id(stem) or stem
    return proj, new_stem + stat.path.suffix


def write_fixture(stat, out_root):
    proj, fname = fixture_name(stat)
    dest_dir = out_root / "projects" / proj
    dest_dir.mkdir(parents=True, exist_ok=True)
    dest = dest_dir / fname
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
        d = snap_root / "projects" / stat.project_dir
        d.mkdir(parents=True, exist_ok=True)
        dest = d / stat.path.name
        shutil.copyfile(stat.path, dest)
        fresh = FileStat(dest, stat.project_dir)
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


def run_ccusage(version, subcommand, config_dir):
    # ccusage >= v20 is multi-agent (codex, opencode, ...) and mixes every
    # detected agent's usage into the bare `daily`/`session` commands; the
    # `claude` subcommand scopes the report to Claude Code logs only
    cmd = (["npx", "-y", "ccusage@%s" % version, "claude"] + subcommand.split()
           + ["--json", "--offline"])
    env = dict(os.environ, CLAUDE_CONFIG_DIR=str(config_dir))
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


def capture_expectations(version, fixture_root, snap_root, project_dirs,
                         expected_dir):
    expected_dir.mkdir(parents=True, exist_ok=True)
    # fail BEFORE the expensive ccusage runs if the local-only -full pair
    # could be committed (they keep real session/message ids)
    for fname in FULL_EXPECTATION_FILES:
        assert_unstageable(expected_dir / fname,
                           "full-history expectations keep real ids — "
                           "local-only by owner ruling")
    commands = {}

    # fixture-scoped set (CI): captured from the SANITIZED tree itself, so
    # the committed expectations match the committed fixtures by construction
    fixture_daily = None
    for sub, fname in (("daily", "ccusage-daily.json"),
                       ("session", "ccusage-session.json")):
        data, cmd = run_ccusage(version, sub, fixture_root)
        if sub == "daily":
            fixture_daily = data
        (expected_dir / fname).write_text(
            json.dumps(data, indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8")
        commands[fname] = {
            "command": cmd,
            "CLAUDE_CONFIG_DIR": "<the sanitized fixture tree>",
            "scope": "fixture",
        }
        print("  wrote expected/%s" % fname)

    # self-check: ccusage on the frozen ORIGINAL snapshot must produce the
    # exact same four token totals as on the sanitized tree — proves the
    # sanitizer preserved all billing-relevant data (usage, ids for dedup,
    # timestamps)
    orig_daily, _ = run_ccusage(version, "daily", snap_root)
    got = {k: fixture_daily["totals"][k] for k in TOTAL_KEYS}
    want = {k: orig_daily["totals"][k] for k in TOTAL_KEYS}
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
    roots = ",".join(str(p.parent) for p in project_dirs)
    for sub, fname in zip(("daily", "session"), FULL_EXPECTATION_FILES):
        data, cmd = run_ccusage(version, sub, roots)
        dest = expected_dir / fname
        dest.write_text(
            json.dumps(redact_json(data), indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8")
        assert_unstageable(dest,
                           "full-history expectations keep real ids — "
                           "local-only by owner ruling")
        commands[fname] = {
            "command": cmd,
            "CLAUDE_CONFIG_DIR": redact_home_text(roots),
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


# --- main ---------------------------------------------------------------------


def main():
    ap = argparse.ArgumentParser(
        description="Harvest sanitized Claude Code fixtures + ccusage "
                    "expectations (owner's machine only).")
    ap.add_argument("--out", required=True,
                    help="output root, e.g. testdata/fixtures/claude-code")
    ap.add_argument("--label", default=None,
                    help="machine label (e.g. macbook, gx10); fixtures and "
                         "expectations go under <out>/<label>/ so harvests "
                         "from multiple machines don't overwrite each other")
    ap.add_argument("--config-dir", action="append", default=None,
                    metavar="DIR/projects",
                    help="explicit projects dir(s); overrides discovery")
    ap.add_argument("--files", type=int, default=12,
                    help="target number of fixture files (8-15, default 12)")
    ap.add_argument("--ccusage-version", default=None,
                    help="pin/override the ccusage version")
    ap.add_argument("--no-expectations", action="store_true",
                    help="skip the ccusage expectation capture (fixtures only)")
    args = ap.parse_args()

    if not 8 <= args.files <= 15:
        ap.error("--files must be between 8 and 15")

    out_root = Path(args.out)
    if args.label:
        out_root = out_root / args.label
    expected_dir = out_root / "expected"

    map_path = Path(args.out) / "PROJECT_MAP.local.json"
    map_path.parent.mkdir(parents=True, exist_ok=True)
    assert_unstageable(map_path,
                       "the map contains the salt and real project names")
    load_or_create_map(map_path)

    project_dirs = discover_project_dirs(args.config_dir)
    if not project_dirs:
        print("error: no Claude Code projects dir found "
              "(checked $CLAUDE_CONFIG_DIR, ~/.claude, ~/.config/claude)",
              file=sys.stderr)
        return 1
    print("project dirs: %s" % ", ".join(redact_home_text(str(p))
                                         for p in project_dirs))

    print("scanning session files ...")
    stats = scan_all(project_dirs)
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
        rel = write_fixture(stat, out_root)
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
        "machine_label": args.label,
        "platform": platform.platform(),
        "claude_code_versions_seen": sorted(versions),
        "project_dirs": [redact_home_text(str(p)) for p in project_dirs],
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
        commands = capture_expectations(version, out_root, snap_root,
                                        project_dirs, expected_dir)
    finally:
        snap_ctx.cleanup()
    meta = {
        "ccusage_version": version,
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
