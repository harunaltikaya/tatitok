#!/usr/bin/env python3
"""tatitok Claude Code status line — one line from the hub.

The agy hook's twin in the other direction: Claude Code runs this as its
statusLine command, it asks the running hub, it prints ONE line:

  today 1.2M tok · $14.20 api-eq | claude 5h 63% · 7d 57% · Fable 7d 71%

  left  (verified) today's tokens (input + output + cache write + cache
        read, the dashboard's total) and API-equivalent cost for today in
        the local zone Z, summed from
        GET /api/v1/stats/daily?from=D&to=D&timezone=Z, the rows the
        dashboard's range totals sum. Z is $TZ if set (one leading ":"
        removed, as libc reads it), else the zone the /etc/localtime
        symlink points at, else UTC.
  right (reported) the "claude" provider's windows from GET
        /api/v1/limits, in the hub's order, "<label> <pct>%". Provider
        and labels are display-safe: non-printable characters dropped,
        cut to 24 / 48 characters.

Claude Code pipes one JSON object on stdin (model, workspace, cost,
context_window, rate_limits, ...). It is read to EOF with the agy hook's
bounded read and discarded: nothing from it is used or stored.

Budget 200 ms end to end. Both GETs start at once, each capped at 80 ms;
whatever has not arrived by then is left out. Hub down or over budget
prints the part that arrived, else "tatitok: hub down". Always exits 0.

  --install    set statusLine in ~/.claude/settings.json (backs it up)
  --uninstall  remove statusLine again (only if it is this command)
"""

import time

# t0: the one clock every budget below is measured from, taken before
# anything else is imported (the agy hook's rule).
T0 = time.monotonic()

import datetime as _dt  # noqa: E402
import http.client  # noqa: E402
import json  # noqa: E402
import os  # noqa: E402
import re  # noqa: E402
import select  # noqa: E402
import shlex  # noqa: E402
import stat  # noqa: E402
import sys  # noqa: E402
import tempfile  # noqa: E402
import threading  # noqa: E402
import urllib.parse  # noqa: E402
import zoneinfo  # noqa: E402

# TATITOK_HUB_URL overrides the default origin, but ONLY a loopback origin
# is accepted (the agy hook's rule); anything else falls back to the default.
DEFAULT_HUB_URL = "http://127.0.0.1:8284"
HUB_URL_RE = re.compile(r"^http://(127\.0\.0\.1|localhost)(?::(\d{1,5}))?$")
STATS_PATH = "/api/v1/stats/daily?from=%s&to=%s&timezone=%s"
LIMITS_PATH = "/api/v1/limits"
PROVIDER = "claude"
TOKEN_FIELDS = ("inputTokens", "outputTokens", "cacheCreationTokens", "cacheReadTokens")
HUB_DOWN = "tatitok: hub down"
# Display caps, in characters, for hub text on the line.
PROVIDER_CAP = 24
LABEL_CAP = 48

GET_TIMEOUT_S = 0.080
# Hard stop for the GETs, from T0; leaves the rest of the 200 ms for the
# interpreter's start and exit.
DEADLINE_S = 0.170
# Read bound for a hub reply: one day's rows or one snapshot.
MAX_BYTES = 1 << 20

# stdin, as in the agy hook: a status object is a few KiB; the read stops
# at limit + 1 bytes, EOF or the stdin deadline (from T0).
STDIN_LIMIT = 256 * 1024
STDIN_DEADLINE_S = 0.100

# The zone "today" is counted in when $TZ is unset or empty: the target of
# this symlink, relative to its zoneinfo dir. /etc/timezone is never read.
LOCALTIME = "/etc/localtime"
ZONE_RE = re.compile(r"(?:.*/)?zoneinfo/(.+)")

SETTINGS_PATH = os.path.expanduser("~/.claude/settings.json")
BACKUP_SUFFIX = ".pre-tatitok-statusline"
KEY = "statusLine"


def elapsed():
    return time.monotonic() - T0


# ---- hub ----------------------------------------------------------------

def hub_origin(value=None):
    """(host, port) of TATITOK_HUB_URL if it is a loopback origin, else of
    the default. `value` overrides the environment lookup (tests)."""
    if value is None:
        value = os.environ.get("TATITOK_HUB_URL", "")
    m = HUB_URL_RE.fullmatch(value) if isinstance(value, str) else None
    if m is None:
        m = HUB_URL_RE.fullmatch(DEFAULT_HUB_URL)
    return m.group(1), int(m.group(2) or 80)


def get_json(origin, path, timeout=GET_TIMEOUT_S):
    """GET path from the hub and decode it. http.client (not urllib) so no
    proxy setting can route the request off the box. Raises on any failure."""
    conn = http.client.HTTPConnection(origin[0], origin[1], timeout=timeout)
    try:
        conn.request("GET", path)
        with conn.getresponse() as resp:
            if resp.status != 200:
                raise OSError("HTTP %d" % resp.status)
            body = resp.read(MAX_BYTES + 1)
    finally:
        conn.close()
    if len(body) > MAX_BYTES:
        raise OSError("reply exceeds %d bytes" % MAX_BYTES)
    return json.loads(body.decode("utf-8"))


def start_fetches(fetchers):
    """Run each fetcher in its own daemon thread. Returns (threads, out):
    out[name] is filled when that fetcher returns; a failure leaves it out."""
    out = {}

    def work(name, fn):
        try:
            out[name] = fn()
        except Exception:  # noqa: BLE001 — a failed GET is a missing part
            pass

    threads = [threading.Thread(target=work, args=item, daemon=True) for item in fetchers.items()]
    for t in threads:
        t.start()
    return threads, out


def wait_fetches(threads, out, deadline):
    """Wait for the threads until the monotonic deadline; return what has
    arrived by then (a thread still running is abandoned)."""
    for t in threads:
        t.join(max(0.0, deadline - time.monotonic()))
    return dict(out)


# ---- today --------------------------------------------------------------

def local_zone(env=None, localtime=LOCALTIME):
    """$TZ if set and non-empty, else the /etc/localtime symlink's target
    relative to its zoneinfo dir, else "UTC". One leading ":" is removed
    from $TZ first (":Europe/Istanbul" is how libc also reads
    Europe/Istanbul); the result names the zone for both the date and
    timezone=. `env` and `localtime` override the real ones (tests)."""
    tz = (os.environ if env is None else env).get("TZ", "")
    if tz.startswith(":"):
        tz = tz[1:]
    if tz:
        return tz
    try:
        target = os.readlink(localtime)
    except OSError:
        return "UTC"
    m = ZONE_RE.fullmatch(target)
    return m.group(1) if m else "UTC"


def today_in(zone, now=None):
    """(zone, today's YYYY-MM-DD in it). An unknown zone name falls back to
    UTC with one stderr line. `now` (aware) overrides the clock (tests)."""
    try:
        tzinfo = zoneinfo.ZoneInfo(zone)
    except (KeyError, ValueError, OSError):
        sys.stderr.write("tatitok: unknown zone %r, using UTC\n" % zone)
        zone, tzinfo = "UTC", _dt.timezone.utc
    now = now or _dt.datetime.now(_dt.timezone.utc)
    return zone, now.astimezone(tzinfo).strftime("%Y-%m-%d")


def stats_path(zone, day):
    """The one-day stats/daily path, the zone passed as the dashboard passes
    it (timezone=, percent-encoded like encodeURIComponent)."""
    return STATS_PATH % (day, day, urllib.parse.quote(zone, safe=""))


# ---- the line -----------------------------------------------------------

def compact_tokens(n):
    for div, suffix in ((1_000_000_000, "B"), (1_000_000, "M"), (1_000, "k")):
        if n >= div:
            return "%.1f%s" % (n / div, suffix)
    return str(n)


def verified_part(doc):
    """"today <tok> tok · $<x> api-eq" from a stats/daily reply, or None."""
    rows = doc.get("daily") if isinstance(doc, dict) else None
    if not isinstance(rows, list):
        return None
    tokens = equiv = 0
    for r in rows:
        tokens += sum(int(r.get(k) or 0) for k in TOKEN_FIELDS)
        equiv += int(r.get("costAPIEquivMicro") or 0)
    return "today %s tok · $%.2f api-eq" % (compact_tokens(tokens), equiv / 1_000_000)


def display_safe(text, cap):
    """text as it may be printed: every character str.isprintable() rejects
    (C0/C1 controls such as ESC, CR and LF, format characters, separators
    other than the space) dropped, then cut to cap characters. Hub text
    reaches the terminal; the hub bounds it too. (quota_alert.py holds the
    same helper; the companions share no module.)"""
    return "".join(c for c in text if c.isprintable())[:cap]


def reported_part(doc):
    """"claude <label> <pct>% · ..." from a limits reply, or None when the
    provider has no windows. Provider and labels are display_safe; a label
    that is empty afterwards is left out."""
    providers = doc.get("providers") if isinstance(doc, dict) else None
    bucket = providers.get(PROVIDER) if isinstance(providers, dict) else None
    windows = bucket.get("windows") if isinstance(bucket, dict) else None
    items = []
    for w in windows or ():
        label, pct = w.get("label"), w.get("usedPercent")
        if isinstance(label, str) and isinstance(pct, (int, float)) and not isinstance(pct, bool):
            label = display_safe(label, LABEL_CAP)
            if label:
                items.append("%s %d%%" % (label, round(pct)))
    return "%s %s" % (display_safe(PROVIDER, PROVIDER_CAP), " · ".join(items)) if items else None


def compose(stats_doc, limits_doc):
    parts = []
    for build, doc in ((verified_part, stats_doc), (reported_part, limits_doc)):
        if doc is None:
            continue
        try:
            part = build(doc)
        except Exception:  # noqa: BLE001 — a malformed reply is a missing part
            part = None
        if part:
            parts.append(part)
    return " | ".join(parts) if parts else HUB_DOWN


# ---- stdin (the agy hook's bounded read) --------------------------------

def read_bounded(fd, limit=STDIN_LIMIT, deadline_s=STDIN_DEADLINE_S):
    """Read fd up to limit+1 bytes until EOF or the stdin deadline (measured
    from T0). Returns (data, retained): data is None when the input exceeds
    the limit; retained is how many bytes the reader held at that point —
    never more than limit + 1, because each read asks for at most the
    remaining capacity."""
    chunks, total = [], 0
    while True:
        rem = deadline_s - elapsed()
        if rem <= 0:
            break
        try:
            ready, _, _ = select.select([fd], [], [], rem)
        except (OSError, ValueError):
            break
        if not ready:
            break
        try:
            chunk = os.read(fd, min(65536, limit + 1 - total))
        except BlockingIOError:
            continue
        except OSError:
            break
        if not chunk:
            break  # EOF
        chunks.append(chunk)
        total += len(chunk)
        if total > limit:
            return None, total
    return b"".join(chunks), total


def drain_stdin(stream=None):
    """Read stdin to EOF (bounded) and drop it. Returns the byte count."""
    stream = stream or sys.stdin.buffer
    try:
        fd = stream.fileno()
    except (AttributeError, OSError, ValueError):
        data = stream.read(STDIN_LIMIT + 1)
        return len(data or b"")
    return read_bounded(fd)[1]


# ---- one run ------------------------------------------------------------

def status_line(get=get_json, origin=None, zone=None, now=None, stdin=None, deadline=None):
    """The line. `zone` and `now` override the local zone and the clock,
    `deadline` (monotonic) the 80 ms / T0 budget (tests)."""
    origin = origin or hub_origin()
    zone, day = today_in(local_zone() if zone is None else zone, now)
    path = stats_path(zone, day)
    started = time.monotonic()
    threads, out = start_fetches({
        "stats": lambda: get(origin, path),
        "limits": lambda: get(origin, LIMITS_PATH),
    })
    try:
        drain_stdin(stdin)
    except Exception:  # noqa: BLE001
        pass
    if deadline is None:
        deadline = min(started + GET_TIMEOUT_S, T0 + DEADLINE_S)
    got = wait_fetches(threads, out, deadline)
    return compose(got.get("stats"), got.get("limits"))


def run(**kw):
    try:
        line = status_line(**kw)
    except Exception:  # noqa: BLE001 — never break Claude Code's status line
        line = HUB_DOWN
    sys.stdout.write(line + "\n")
    sys.stdout.flush()
    return 0


# ---- install / uninstall ------------------------------------------------
#
# settings.json is edited as text: the statusLine member is spliced in after
# the last top-level member (or removed again), so every other byte of the
# file stays as it was. The result is re-parsed and compared before writing.

def command_for(script):
    return "python3 " + shlex.quote(script)


def _skip_ws(t, i):
    while i < len(t) and t[i] in " \t\r\n":
        i += 1
    return i


def _end_of_string(t, i):
    i += 1
    while t[i] != '"':
        i += 2 if t[i] == "\\" else 1
    return i + 1


def _end_of_value(t, i):
    if t[i] == '"':
        return _end_of_string(t, i)
    if t[i] in "{[":
        depth = 0
        while True:
            c = t[i]
            if c == '"':
                i = _end_of_string(t, i)
                continue
            if c in "{[":
                depth += 1
            elif c in "}]":
                depth -= 1
                if depth == 0:
                    return i + 1
            i += 1
    while i < len(t) and t[i] not in ",}] \t\r\n":
        i += 1
    return i


def top_level_members(t):
    """For text holding one valid JSON object: (open, close, members) with
    the brace indexes and [(key, start, end)] per member, start at the key's
    quote, end just past the value."""
    open_ = _skip_ws(t, 0)
    i = _skip_ws(t, open_ + 1)
    members = []
    while t[i] != "}":
        start = i
        i = _end_of_string(t, i)
        key = json.loads(t[start:i])
        i = _skip_ws(t, _skip_ws(t, i) + 1)  # past ":"
        end = _end_of_value(t, i)
        members.append((key, start, end))
        i = _skip_ws(t, end)
        if t[i] == ",":
            i = _skip_ws(t, i + 1)
    return open_, i, members


def with_member(t, key, value):
    """t with key: value appended as the last top-level member, laid out
    like the members around it."""
    open_, close, members = top_level_members(t)
    if not members:
        body = json.dumps(value, indent=2).replace("\n", "\n  ")
        return t[:open_] + "{\n  %s: %s\n}" % (json.dumps(key), body) + t[close + 1:]
    lead = t[open_ + 1:members[0][1]]
    if "\n" in lead:
        nl = "\r\n" if "\r\n" in lead else "\n"
        indent = lead.rsplit("\n", 1)[1]
        body = json.dumps(value, indent=len(indent) or 2).replace("\n", nl + indent)
        text = ",%s%s%s: %s" % (nl, indent, json.dumps(key), body)
    else:
        text = ", %s: %s" % (json.dumps(key), json.dumps(value))
    end = members[-1][2]
    return t[:end] + text + t[end:]


def without_member(t, key):
    """t with its one top-level member named key removed, together with the
    comma and white space that separated it."""
    open_, close, members = top_level_members(t)
    idx = [n for n, m in enumerate(members) if m[0] == key]
    if len(idx) != 1:
        raise ValueError("expected one %s member, found %d" % (key, len(idx)))
    k = idx[0]
    if k > 0:
        return t[:members[k - 1][2]] + t[members[k][2]:]
    if len(members) > 1:
        return t[:members[0][1]] + t[members[1][1]:]
    return t[:open_ + 1] + t[close:]


def _read_settings(path):
    with open(path, "rb") as fh:
        raw = fh.read()
    text = raw.decode("utf-8")
    settings = json.loads(text)
    if not isinstance(settings, dict):
        raise SystemExit("refusing: %s is not a JSON object" % path)
    return raw, text, settings


def _write_settings(path, text, expect):
    """Check that text parses to expect, then replace the file (atomic, same
    mode; a symlinked settings.json keeps its link)."""
    if json.loads(text) != expect:
        raise SystemExit("refusing: the edited %s would not parse as expected" % path)
    real = os.path.realpath(path)
    mode = stat.S_IMODE(os.stat(real).st_mode)
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(real), prefix=".settings.", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb") as fh:
            fh.write(text.encode("utf-8"))
        os.chmod(tmp, mode)
        os.replace(tmp, real)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def install(settings_path=SETTINGS_PATH, script=None):
    """Set statusLine to this script. Refuses a different statusLine command
    and an existing backup; an identical command is a no-op."""
    command = command_for(script or os.path.abspath(__file__))
    raw, text, settings = _read_settings(settings_path)
    cur = settings.get(KEY)
    if isinstance(cur, dict) and cur.get("command") == command:
        print("statusLine already runs %s; nothing to do" % command)
        return 0
    if KEY in settings:
        raise SystemExit("refusing: %s already has a statusLine, leaving it alone:\n  %s"
                         % (settings_path, json.dumps(cur)))
    backup = settings_path + BACKUP_SUFFIX
    try:
        fd = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, stat.S_IRUSR | stat.S_IWUSR)
    except FileExistsError:
        raise SystemExit("refusing: backup already exists at %s" % backup) from None
    with os.fdopen(fd, "wb") as fh:
        fh.write(raw)
    value = {"type": "command", "command": command}
    _write_settings(settings_path, with_member(text, KEY, value), dict(settings, **{KEY: value}))
    print("installed statusLine -> %s\nbackup: %s" % (command, backup))
    return 0


def uninstall(settings_path=SETTINGS_PATH, script=None):
    """Remove statusLine ONLY when its command is exactly this script's; any
    other value is printed and left untouched (exit 1)."""
    command = command_for(script or os.path.abspath(__file__))
    _, text, settings = _read_settings(settings_path)
    cur = settings.get(KEY)
    if cur is None and KEY not in settings:
        print("no statusLine key in %s; nothing to do" % settings_path)
        return 0
    if not (isinstance(cur, dict) and cur.get("command") == command):
        print("refusing: statusLine in %s is not this script, leaving it alone:\n  %s"
              % (settings_path, json.dumps(cur)))
        return 1
    expect = {k: v for k, v in settings.items() if k != KEY}
    _write_settings(settings_path, without_member(text, KEY), expect)
    print("removed statusLine from %s (backup %s kept)" % (settings_path, settings_path + BACKUP_SUFFIX))
    return 0


def main(argv):
    if len(argv) > 1 and argv[1] == "--install":
        return install()
    if len(argv) > 1 and argv[1] == "--uninstall":
        return uninstall()
    return run()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
