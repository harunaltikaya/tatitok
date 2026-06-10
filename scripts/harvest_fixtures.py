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
  3. Sanitizes every line: content-bearing fields (message text, thinking,
     tool_use input, tool_result content, summaries, attachments, ...) are
     replaced with "<stripped len=N sha256=FIRST12HEX>" placeholders; all
     structure, ids, timestamps, models, usage objects and unknown fields are
     preserved. $HOME in paths becomes "~", the path-encoded home form
     ("-home-<user>") becomes "-home-user", and the bare username is redacted
     in all kept strings AND object keys (some records key maps by file
     path). Malformed source lines are replaced with
     a single-key placeholder object so line counts stay identical.
  4. Captures ccusage expectations (`claude daily` and `claude session`,
     --json --offline — the `claude` subcommand scopes multi-agent ccusage
     v20+ to Claude Code logs only) twice:
       - fixture-scoped: against a temp fake projects/ tree containing the
         ORIGINAL (unsanitized) content of only the selected files, laid out
         under the SAME sanitized dir/file names as the committed fixtures.
         Written to expected/ccusage-daily.json + ccusage-session.json.
         This is the set CI parity tests load.
       - full-history: against the real config dir(s). Written to
         expected/ccusage-daily-full.json + ccusage-session-full.json.
         Used by `make parity-full` on the owner's machine.
     The ccusage version is resolved once, pinned in expected/META.json, and
     reused on every future run.

Placeholder format (the Go sanitizer in internal/core must match exactly):
    <stripped len=N sha256=H>
  where N is the UTF-8 byte length of the original string and H is the first
  12 lowercase hex chars of sha256 of those bytes. Non-string content values
  (e.g. tool_use input objects) are serialized with
  json.dumps(v, separators=(",", ":"), ensure_ascii=False) first.
"""

import argparse
import datetime
import hashlib
import json
import os
import platform
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

# Keys whose string values are known-safe metadata: never stripped (paths are
# home-redacted). Everything else longer than MAX_FREE_LEN is stripped.
SAFE_KEYS = {
    "id",
    "uuid",
    "parentUuid",
    "leafUuid",
    "sessionId",
    "requestId",
    "request_id",
    "message_id",
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
}

MAX_FREE_LEN = 80  # unknown string fields longer than this get stripped

HOME = str(Path.home())
# project dir names encode the cwd with "/" -> "-", so $HOME appears as e.g.
# "-home-alice" or "-Users-alice" at the start of the dir name; that encoded
# form also shows up INSIDE path strings and object keys in the logs
ENCODED_HOME = HOME.replace("/", "-")
USERNAME = Path.home().name


def placeholder(value):
    if not isinstance(value, str):
        value = json.dumps(value, separators=(",", ":"), ensure_ascii=False)
    raw = value.encode("utf-8")
    digest = hashlib.sha256(raw).hexdigest()[:12]
    return "<stripped len=%d sha256=%s>" % (len(raw), digest)


def redact_home(s):
    s = s.replace(HOME, "~")
    s = s.replace(ENCODED_HOME, "-home-user")
    # bare username belt-and-braces; skip very short usernames that would
    # mangle unrelated text
    if len(USERNAME) >= 4:
        s = s.replace(USERNAME, "user")
    return s


def sanitize_key(k):
    """Object keys in Claude Code records can be file paths (e.g. the
    readFileState / file-backup maps are keyed by absolute path)."""
    s = redact_home(k)
    if looks_safe_short(s):
        return s
    if s.startswith(("/", "~", ".")) and "\n" not in s and len(s) <= 300:
        return s
    return placeholder(k)


def looks_safe_short(s):
    return len(s.encode("utf-8")) <= MAX_FREE_LEN


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
        if key in SAFE_KEYS:
            return redact_home(node)
        s = redact_home(node)
        if looks_safe_short(s):
            return s
        # long unknown string: keep only if it is clearly a path
        if s.startswith(("/", "~")) and "\n" not in s and len(s) <= 300:
            return s
        return placeholder(node)
    return node


def sanitize_record(obj):
    return sanitize_value(obj)


def sanitize_project_dirname(name):
    if name.startswith(ENCODED_HOME):
        return "-home-user" + name[len(ENCODED_HOME):]
    return name


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
                print("  ! skipping %s: %s" % (f, exc), file=sys.stderr)
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


def write_fixture(stat, out_root):
    proj = sanitize_project_dirname(stat.project_dir)
    dest_dir = out_root / "projects" / proj
    dest_dir.mkdir(parents=True, exist_ok=True)
    dest = dest_dir / stat.path.name
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
        raise RuntimeError("line count drifted for %s: %d -> %d"
                           % (stat.path, stat.lines, written))
    return str(rel)


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
    env = dict(os.environ, CLAUDE_CONFIG_DIR=config_dir)
    out = subprocess.run(cmd, capture_output=True, text=True, env=env,
                         timeout=1800)
    if out.returncode != 0:
        raise RuntimeError("%s failed (%d):\n%s"
                           % (" ".join(cmd), out.returncode, out.stderr[-2000:]))
    return json.loads(out.stdout), cmd


def snapshot_selected(selected, snap_root):
    """Copy each selected file ONCE into a temp fake config tree and rescan.

    Claude Code appends to live session logs while we run; sanitizing and
    expectation-capturing from the same frozen snapshot is the only way to
    keep the committed fixtures and the fixture-scoped ccusage expectations
    consistent with each other. Returns fresh FileStats pointing at the
    snapshot copies (criteria and original source path carried over).
    """
    frozen = []
    for stat in selected:
        d = snap_root / "projects" / sanitize_project_dirname(stat.project_dir)
        d.mkdir(parents=True, exist_ok=True)
        dest = d / stat.path.name
        shutil.copyfile(stat.path, dest)
        fresh = FileStat(dest, stat.project_dir)
        fresh.scan()
        fresh.criteria = stat.criteria
        fresh.source = stat.path
        frozen.append(fresh)
    return frozen


def capture_expectations(version, snap_root, project_dirs, expected_dir):
    expected_dir.mkdir(parents=True, exist_ok=True)
    commands = {}

    # fixture-scoped set (CI): the frozen snapshot tree is already laid out
    # as a fake config root (projects/<sanitized-dir>/<session>.jsonl)
    for sub, fname in (("daily", "ccusage-daily.json"),
                       ("session", "ccusage-session.json")):
        data, cmd = run_ccusage(version, sub, str(snap_root))
        (expected_dir / fname).write_text(
            json.dumps(data, indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8")
        commands[fname] = {
            "command": cmd,
            "CLAUDE_CONFIG_DIR": "<frozen fixture snapshot tree>",
            "scope": "fixture",
        }
        print("  wrote expected/%s" % fname)

    # full-history set (local parity): the real config roots
    roots = ",".join(str(p.parent) for p in project_dirs)
    for sub, fname in (("daily", "ccusage-daily-full.json"),
                       ("session", "ccusage-session-full.json")):
        data, cmd = run_ccusage(version, sub, roots)
        (expected_dir / fname).write_text(
            json.dumps(data, indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8")
        commands[fname] = {
            "command": cmd,
            "CLAUDE_CONFIG_DIR": redact_home(roots),
            "scope": "full-history",
        }
        print("  wrote expected/%s" % fname)
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

    project_dirs = discover_project_dirs(args.config_dir)
    if not project_dirs:
        print("error: no Claude Code projects dir found "
              "(checked $CLAUDE_CONFIG_DIR, ~/.claude, ~/.config/claude)",
              file=sys.stderr)
        return 1
    print("project dirs: %s" % ", ".join(redact_home(str(p))
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
    # fixtures + fixture-scoped expectations must come from identical bytes
    selected = snapshot_selected(selected, snap_root)

    manifest_files = []
    for stat in selected:
        rel = write_fixture(stat, out_root)
        print("  %-60s %s" % (rel, ",".join(stat.criteria)))
        manifest_files.append({
            "source": redact_home(str(stat.source)),
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
        "project_dirs": [redact_home(str(p)) for p in project_dirs],
        "files": manifest_files,
    }
    (out_root / "MANIFEST.json").write_text(
        json.dumps(manifest, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8")
    print("wrote MANIFEST.json")

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
        commands = capture_expectations(version, snap_root, project_dirs,
                                        expected_dir)
    finally:
        snap_ctx.cleanup()
    meta = {
        "ccusage_version": version,
        "captured_at": datetime.datetime.now(datetime.timezone.utc)
                       .isoformat(timespec="seconds"),
        "timezone": local_timezone(),
        "machine_label": args.label,
        "commands": commands,
        "note": "ccusage-daily.json / ccusage-session.json are scoped to the "
                "harvested fixture files (CI parity set); the -full variants "
                "cover the machine's complete history (make parity-full).",
    }
    meta_path.write_text(json.dumps(meta, indent=2, ensure_ascii=False) + "\n",
                         encoding="utf-8")
    print("wrote expected/META.json (ccusage %s, %.0fs)"
          % (version, time.time() - t0))
    print("done — review the fixtures for leaked content, then commit.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
