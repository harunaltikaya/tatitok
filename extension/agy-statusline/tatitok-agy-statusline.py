#!/usr/bin/env python3
"""tatitok agy statusLine hook — usage log + quota feed.

agy (Antigravity CLI) runs this on every status refresh (~1/s) with ONE JSON
object on stdin (no trailing newline). The hook does exactly two things:

  1. appends the object (minus "email") to <data>/tatitok/agy/statusline.jsonl
     whenever the conversation's token totals changed since the last append;
  2. at most every 90 s, POSTs the four agy quota windows to the tatitok hub's
     display-only limits endpoint (/api/v1/limits, provider key "agy").

It ALWAYS prints "tatitok" and exits 0 — a broken hook must never break agy's
status line. stdlib only; the status-line path stays under 200 ms: the POST
runs in a detached child (double fork, stdio closed) with a 0.15 s cap, so a
stopped or stalled hub never delays the parent.

  --install    add the statusLine command to agy's settings.json (backs it up)
  --uninstall  remove the statusLine key again (only if it is ours; backup kept)
"""

import time

# t0: the hook's one clock. Every budget below is measured from here, taken
# before anything else is imported so the interpreter's own startup is the
# only time not under our control.
T0 = time.monotonic()

import datetime as _dt  # noqa: E402
import fcntl  # noqa: E402
import json  # noqa: E402
import os  # noqa: E402
import re  # noqa: E402
import select  # noqa: E402
import signal  # noqa: E402
import stat  # noqa: E402
import sys  # noqa: E402
import urllib.request  # noqa: E402

# The hub's display-only ingest endpoint. TATITOK_HUB_URL overrides the
# default origin, but ONLY a loopback origin is accepted — the same rule as
# the browser extension's HUB_URL_RE (http, 127.0.0.1 or localhost, optional
# decimal port, nothing else). Anything else falls back to the default and is
# never contacted: quota data must not leave the machine.
DEFAULT_HUB_URL = "http://127.0.0.1:8284"
HUB_URL_RE = re.compile(r"^http://(127\.0\.0\.1|localhost)(:\d{1,5})?$")
INGEST_PATH = "/api/v1/limits"
POST_MIN_INTERVAL_S = 90
POST_TIMEOUT_S = 0.15  # connect + read, in the detached child

# stdin bound: a status object is a few KiB; anything past this is not agy.
# The reader never holds more than limit + 1 bytes (per-iteration read size
# is capped to the remaining capacity).
STDIN_LIMIT = 256 * 1024

# One end-to-end budget, measured from T0: stdin may be read until 100 ms
# (agy writes the object in one go, so the normal path never waits); every
# later step checks the remaining budget, and a SIGALRM at 190 ms ends the
# hook wherever it is — it prints "tatitok" and exits 0 regardless. The log
# line is appended in ONE os.write so an interrupt can never leave a partial
# line behind.
STDIN_DEADLINE_S = 0.100
HOOK_DEADLINE_S = 0.190


def elapsed():
    return time.monotonic() - T0


def remaining(deadline_s=HOOK_DEADLINE_S):
    return deadline_s - elapsed()


class Budget(Exception):
    """The 190 ms hook budget is spent; the caller prints "tatitok" and exits."""


def check_budget():
    if remaining() <= 0:
        raise Budget()

QUOTA_WINDOWS = ("gemini-5h", "gemini-weekly", "3p-5h", "3p-weekly")

SETTINGS_PATH = os.path.expanduser("~/.gemini/antigravity-cli/settings.json")
BACKUP_SUFFIX = ".pre-tatitok"


# ---- paths --------------------------------------------------------------

def data_dir():
    base = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    return os.path.join(base, "tatitok", "agy")


def hub_url(value=None):
    """The hub origin: TATITOK_HUB_URL if it is a loopback origin, else the
    default. `value` overrides the environment lookup (tests)."""
    if value is None:
        value = os.environ.get("TATITOK_HUB_URL", "")
    if isinstance(value, str) and HUB_URL_RE.fullmatch(value):
        return value
    return DEFAULT_HUB_URL


def ingest_url(value=None):
    return hub_url(value) + INGEST_PATH


def _open_private(path, mode):
    """Open with 0600 on create (files); the dir is made 0700 by caller."""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | (os.O_APPEND if "a" in mode else os.O_TRUNC),
                 stat.S_IRUSR | stat.S_IWUSR)
    return os.fdopen(fd, mode)


# ---- core ---------------------------------------------------------------

def now_ms():
    return int(_dt.datetime.now(_dt.timezone.utc).timestamp() * 1000)


def rfc3339_ms(ms):
    t = _dt.datetime.fromtimestamp(ms / 1000.0, _dt.timezone.utc)
    return t.strftime("%Y-%m-%dT%H:%M:%S.") + "%03dZ" % (ms % 1000)


def parse_reset_ms(window, now):
    """reset_time (protobuf Timestamp → RFC3339 string) → epoch ms; falls
    back to now + reset_in_seconds; 0 when neither is usable."""
    rt = window.get("reset_time")
    if isinstance(rt, str) and rt:
        s = rt.strip()
        if s.endswith("Z") or s.endswith("z"):
            s = s[:-1] + "+00:00"
        try:
            # fromisoformat (3.11+) accepts fractional seconds and offsets.
            t = _dt.datetime.fromisoformat(s)
            if t.tzinfo is None:
                t = t.replace(tzinfo=_dt.timezone.utc)
            return int(t.timestamp() * 1000)
        except ValueError:
            pass
    ris = window.get("reset_in_seconds")
    if isinstance(ris, (int, float)) and ris >= 0:
        return int(now + ris * 1000)
    return 0


def quota_payload(quota, now):
    """Build the /api/v1/limits body for the "agy" provider, or None when no
    usable window exists. usedPercent = (1 - remaining_fraction) * 100."""
    if not isinstance(quota, dict):
        return None
    windows = []
    for label in QUOTA_WINDOWS:
        w = quota.get(label)
        if not isinstance(w, dict):
            continue
        rf = w.get("remaining_fraction")
        if not isinstance(rf, (int, float)) or isinstance(rf, bool):
            continue
        used = (1.0 - float(rf)) * 100.0
        if used < 0:
            used = 0.0
        windows.append({"label": label, "usedPercent": used,
                        "resetAt": parse_reset_ms(w, now)})
    if not windows:
        return None
    return {"agy": {"fetchedAt": now, "windows": windows}}


class _Deadline(Exception):
    pass


def _deadline_hit(signum, frame):
    raise _Deadline()


def post_quota(payload, url=None, timeout=POST_TIMEOUT_S):
    """POST the payload synchronously; True on 2xx, False on any failure
    (never raises). The WHOLE request (connect + send + read) is capped at
    `timeout` seconds of wall time: the socket timeout alone is per
    operation, so a wall-clock alarm enforces the total."""
    prev = None
    try:
        body = json.dumps(payload, separators=(",", ":")).encode()
        req = urllib.request.Request(url or ingest_url(), data=body, method="POST",
                                     headers={"Content-Type": "application/json"})
        try:
            prev = signal.signal(signal.SIGALRM, _deadline_hit)
            signal.setitimer(signal.ITIMER_REAL, timeout)
        except (ValueError, OSError, AttributeError):
            prev = None  # not the main thread / no itimer: socket timeout only
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return 200 <= resp.status < 300
    except Exception:  # noqa: BLE001 — failure (incl. the deadline) is silent by contract
        return False
    finally:
        if prev is not None:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, prev)


def post_quota_detached(payload, state_path, now, lock_fd=None):
    """Deliver the POST from a detached grandchild (double fork, new session,
    stdio on /dev/null) so the status-line parent never waits on the
    network. On 2xx the grandchild records last_post_at=now in the state
    file itself, under the state lock (it drops the inherited lock fd first
    and re-opens state.lock, so it never shares the parent's lock). Returns
    None: delivery was handed off, outcome unknown to the caller."""
    try:
        pid = os.fork()
    except OSError:
        return None
    if pid > 0:
        # Parent: reap the intermediate child (it exits at once) and go on.
        try:
            os.waitpid(pid, 0)
        except OSError:
            pass
        return None
    # Intermediate child: detach and fork the worker, then exit.
    try:
        os.setsid()
        if os.fork() > 0:
            os._exit(0)
        if lock_fd is not None:
            os.close(lock_fd)  # the parent's lock is the parent's
        devnull = os.open(os.devnull, os.O_RDWR)
        for fd in (0, 1, 2):
            os.dup2(devnull, fd)
        if devnull > 2:
            os.close(devnull)
        if post_quota(payload):
            with state_lock(os.path.dirname(state_path), block=True) as held:
                if held:
                    state = load_state(state_path)
                    state["last_post_at"] = now
                    save_state(state_path, state)
    except Exception:  # noqa: BLE001
        pass
    finally:
        os._exit(0)


class state_lock:
    """flock on <dir>/state.lock. Non-blocking by default: `held` is False
    when another refresh is mid-flight (the caller then skips this refresh —
    the next one does the work). block=True waits (the grandchild's
    last_post_at write)."""

    def __init__(self, ddir, block=False):
        self.path = os.path.join(ddir, "state.lock")
        self.block = block
        self.fd = None

    def __enter__(self):
        self.fd = os.open(self.path, os.O_RDWR | os.O_CREAT, stat.S_IRUSR | stat.S_IWUSR)
        try:
            fcntl.flock(self.fd, fcntl.LOCK_EX | (0 if self.block else fcntl.LOCK_NB))
        except (BlockingIOError, InterruptedError, OSError):
            os.close(self.fd)
            self.fd = None
            return False
        return True

    def __exit__(self, *exc):
        if self.fd is not None:
            try:
                fcntl.flock(self.fd, fcntl.LOCK_UN)
            finally:
                os.close(self.fd)
            self.fd = None
        return False


def _is_num(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def _valid_totals(v):
    return (isinstance(v, list) and len(v) == 2
            and all(isinstance(x, int) and not isinstance(x, bool) for x in v))


def load_state(path):
    """Load state.json, validating EVERY field; any corrupt, missing-shape or
    wrong-typed value discards the whole state (fresh start), so a bad file
    can never wedge logging or POSTs."""
    try:
        with open(path, encoding="utf-8") as fh:
            st = json.load(fh)
    except Exception:  # noqa: BLE001
        return {}
    if not isinstance(st, dict):
        return {}
    out = {}
    lt = st.get("last_totals", {})
    if not isinstance(lt, dict):
        return {}
    for k, v in lt.items():
        if not isinstance(k, str) or not _valid_totals(v):
            return {}
    out["last_totals"] = dict(lt)
    for key in ("last_post_at", "last_attempt_at"):
        if key in st:
            if not _is_num(st[key]):
                return {}
            out[key] = st[key]
    return out


def save_state(path, state):
    tmp = path + ".tmp"
    with _open_private(tmp, "w") as fh:
        json.dump(state, fh, separators=(",", ":"))
    os.replace(tmp, path)


def append_line(path, data):
    """Append data (a complete line) with ONE os.write on an O_APPEND fd, so
    a budget interrupt can only fall before or after the line, never inside
    it. A short write would leave a partial line; treat it as a failure."""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, stat.S_IRUSR | stat.S_IWUSR)
    try:
        n = os.write(fd, data)
        if n != len(data):
            raise OSError("short write to %s (%d of %d bytes)" % (path, n, len(data)))
    finally:
        os.close(fd)


def totals_of(obj):
    cw = obj.get("context_window")
    if not isinstance(cw, dict):
        return [0, 0]
    return [int(cw.get("total_input_tokens") or 0),
            int(cw.get("total_output_tokens") or 0)]


class Result:
    """process() outcome. busy=True means another refresh held the state
    lock: nothing was logged or posted this time (the next refresh will)."""

    __slots__ = ("appended", "posted", "busy")

    def __init__(self, appended=False, posted=False, busy=False):
        self.appended, self.posted, self.busy = appended, posted, busy


def process(obj, ddir, now=None, poster=None):
    """Run the hook logic on one parsed status object. `poster(payload)`
    returning True/False is synchronous (tests); the default detaches the
    POST and reports None — the child records success itself. The state
    lock is held from the state load through the eligibility check, the
    last_attempt_at stamp and the fork handoff (or the synchronous poster
    call), so two overlapping refreshes can never both pass the throttle."""
    if now is None:
        now = now_ms()
    obj = dict(obj)
    obj.pop("email", None)  # never reaches disk or the network

    os.makedirs(ddir, mode=stat.S_IRWXU, exist_ok=True)
    os.chmod(ddir, stat.S_IRWXU)
    state_path = os.path.join(ddir, "state.json")

    check_budget()
    lock = state_lock(ddir)
    with lock as held:
        if not held:
            return Result(busy=True)  # another refresh is mid-flight; skip
        state = load_state(state_path)
        dirty = False

        appended = False
        conv = obj.get("conversation_id")
        if isinstance(conv, str) and conv:
            totals = totals_of(obj)
            last = state.setdefault("last_totals", {})
            if last.get(conv) != totals:
                rec = dict(obj)
                rec["logged_at"] = rfc3339_ms(now)
                line = (json.dumps(rec, separators=(",", ":"), ensure_ascii=False) + "\n").encode()
                check_budget()  # never start a write we may not finish
                append_line(os.path.join(ddir, "statusline.jsonl"), line)
                last[conv] = totals
                appended = dirty = True
        check_budget()

        # Both successes and attempts are throttled to the interval: a hub
        # that is down is retried every 90 s, not every status refresh;
        # last_post_at itself only ever moves on a 2xx.
        posted = False
        payload = quota_payload(obj.get("quota"), now)
        since = max(state.get("last_post_at") or 0, state.get("last_attempt_at") or 0)
        if payload is not None and now - since >= POST_MIN_INTERVAL_S * 1000:
            state["last_attempt_at"] = now
            save_state(state_path, state)  # stamped under the lock, before the handoff
            if poster is None:
                posted = post_quota_detached(payload, state_path, now, lock.fd)
            else:
                posted = poster(payload)
                if posted:
                    state = load_state(state_path)
                    state["last_post_at"] = now
                    save_state(state_path, state)
            return Result(appended, posted)

        if dirty:
            save_state(state_path, state)
        return Result(appended, posted)


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
            break  # deadline: parse what we have
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


def read_stdin_bounded(stream=None, limit=STDIN_LIMIT, deadline_s=STDIN_DEADLINE_S):
    """stdin as bytes (None when over the limit). A stream without a file
    descriptor (tests) is read directly, with the same exact bound."""
    stream = stream or sys.stdin.buffer
    try:
        fd = stream.fileno()
    except (AttributeError, OSError, ValueError):
        data = stream.read(limit + 1)
        return None if data is None or len(data) > limit else data
    data, _ = read_bounded(fd, limit, deadline_s)
    return data


def _budget_alarm(signum, frame):
    raise Budget()


def run_hook(stdin_bytes):
    """Parse + process under the hook budget: a SIGALRM at HOOK_DEADLINE_S
    (from T0) raises Budget wherever the parent is, on top of the explicit
    checks; either way the hook prints "tatitok" and exits 0."""
    armed = False
    try:
        rem = remaining()
        if rem <= 0:
            raise Budget()
        try:
            signal.signal(signal.SIGALRM, _budget_alarm)
            signal.setitimer(signal.ITIMER_REAL, rem)
            armed = True
        except (ValueError, OSError, AttributeError):
            pass  # not the main thread: explicit checks only
        if stdin_bytes is None:
            raise ValueError("stdin exceeds the size bound")
        obj = json.loads(stdin_bytes)
        if not isinstance(obj, dict):
            raise ValueError("status object is not a JSON object")
        process(obj, data_dir())
    except Exception:  # noqa: BLE001 — never crash agy's status line (Budget included)
        pass
    finally:
        if armed:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, signal.SIG_DFL)
    sys.stdout.write("tatitok")
    sys.stdout.flush()
    return 0


# ---- install / uninstall ------------------------------------------------

def install(settings_path=SETTINGS_PATH, script=None):
    script = script or os.path.abspath(__file__)
    backup = settings_path + BACKUP_SUFFIX
    if os.path.exists(backup):
        raise SystemExit("refusing: backup already exists at %s" % backup)
    with open(settings_path, encoding="utf-8") as fh:
        raw = fh.read()
    settings = json.loads(raw)
    if "statusLine" in settings:
        raise SystemExit("refusing: settings.json already has a statusLine key")
    with open(backup, "w", encoding="utf-8") as fh:
        fh.write(raw)
    settings["statusLine"] = {"command": script}
    with open(settings_path, "w", encoding="utf-8") as fh:
        json.dump(settings, fh, indent=2)
        fh.write("\n")
    print("installed statusLine -> %s\nbackup: %s" % (script, backup))


def uninstall(settings_path=SETTINGS_PATH, script=None):
    """Remove statusLine ONLY when its command is exactly this script; any
    other value is printed and left untouched (exit 1)."""
    script = script or os.path.abspath(__file__)
    with open(settings_path, encoding="utf-8") as fh:
        settings = json.load(fh)
    cur = settings.get("statusLine")
    if cur is None:
        print("no statusLine key in %s; nothing to do" % settings_path)
        return 0
    if not (isinstance(cur, dict) and cur.get("command") == script):
        print("refusing: statusLine in %s is not this hook, leaving it alone:\n  %s"
              % (settings_path, json.dumps(cur)))
        return 1
    del settings["statusLine"]
    with open(settings_path, "w", encoding="utf-8") as fh:
        json.dump(settings, fh, indent=2)
        fh.write("\n")
    print("removed statusLine from %s (backup %s kept)"
          % (settings_path, settings_path + BACKUP_SUFFIX))
    return 0


def main(argv):
    if len(argv) > 1 and argv[1] == "--install":
        install()
        return 0
    if len(argv) > 1 and argv[1] == "--uninstall":
        return uninstall()
    return run_hook(read_stdin_bounded())


if __name__ == "__main__":
    sys.exit(main(sys.argv))
