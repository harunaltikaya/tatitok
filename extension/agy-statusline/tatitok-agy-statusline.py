#!/usr/bin/env python3
"""tatitok agy statusLine hook — usage log + quota feed.

agy (Antigravity CLI) runs this on every status refresh (~1/s) with ONE JSON
object on stdin (no trailing newline). The hook does exactly two things:

  1. appends the object (minus "email") to <data>/tatitok/agy/statusline.jsonl
     whenever the conversation's token totals changed since the last append;
  2. at most every 90 s, POSTs the four agy quota windows to the tatitok hub's
     display-only limits endpoint (/api/v1/limits, provider key "agy").

It ALWAYS prints "tatitok" and exits 0 — a broken hook must never break agy's
status line. stdlib only; budget <200 ms.

  --install    add the statusLine command to agy's settings.json (backs it up)
  --uninstall  remove the statusLine key again (backup is kept)
"""

import datetime as _dt
import json
import os
import stat
import sys
import urllib.request

# The hub's display-only ingest endpoint (loopback only).
# TODO(addr): make configurable if the owner runs the hub on a custom --addr
# (kept in step with extension/tatitok-limits/sw.js INGEST_URL).
INGEST_URL = "http://127.0.0.1:8284/api/v1/limits"
POST_MIN_INTERVAL_S = 90
POST_TIMEOUT_S = 1.0

QUOTA_WINDOWS = ("gemini-5h", "gemini-weekly", "3p-5h", "3p-weekly")

SETTINGS_PATH = os.path.expanduser("~/.gemini/antigravity-cli/settings.json")
BACKUP_SUFFIX = ".pre-tatitok"


# ---- paths --------------------------------------------------------------

def data_dir():
    base = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    return os.path.join(base, "tatitok", "agy")


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


def post_quota(payload, url=INGEST_URL, timeout=POST_TIMEOUT_S):
    """POST the payload; True on 2xx, False on any failure (never raises)."""
    try:
        body = json.dumps(payload, separators=(",", ":")).encode()
        req = urllib.request.Request(url, data=body, method="POST",
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return 200 <= resp.status < 300
    except Exception:  # noqa: BLE001 — failure is silent by contract
        return False


def load_state(path):
    try:
        with open(path, encoding="utf-8") as fh:
            st = json.load(fh)
        if isinstance(st, dict):
            return st
    except Exception:  # noqa: BLE001
        pass
    return {}


def save_state(path, state):
    tmp = path + ".tmp"
    with _open_private(tmp, "w") as fh:
        json.dump(state, fh, separators=(",", ":"))
    os.replace(tmp, path)


def totals_of(obj):
    cw = obj.get("context_window")
    if not isinstance(cw, dict):
        return [0, 0]
    return [int(cw.get("total_input_tokens") or 0),
            int(cw.get("total_output_tokens") or 0)]


def process(obj, ddir, now=None, poster=post_quota):
    """Run the hook logic on one parsed status object. Returns
    (appended: bool, posted: bool). Raises nothing to the caller's benefit —
    the caller wraps it."""
    if now is None:
        now = now_ms()
    obj = dict(obj)
    obj.pop("email", None)  # never reaches disk or the network

    os.makedirs(ddir, mode=stat.S_IRWXU, exist_ok=True)
    os.chmod(ddir, stat.S_IRWXU)
    state_path = os.path.join(ddir, "state.json")
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
            with _open_private(os.path.join(ddir, "statusline.jsonl"), "a") as fh:
                fh.write(json.dumps(rec, separators=(",", ":"), ensure_ascii=False) + "\n")
            last[conv] = totals
            appended = dirty = True

    posted = False
    payload = quota_payload(obj.get("quota"), now)
    last_post = state.get("last_post_at") or 0
    if payload is not None and now - last_post >= POST_MIN_INTERVAL_S * 1000:
        if poster(payload):
            state["last_post_at"] = now
            posted = dirty = True

    if dirty:
        save_state(state_path, state)
    return appended, posted


def run_hook(stdin_text):
    try:
        obj = json.loads(stdin_text)
        if not isinstance(obj, dict):
            raise ValueError("status object is not a JSON object")
        process(obj, data_dir())
    except Exception:  # noqa: BLE001 — never crash agy's status line
        pass
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


def uninstall(settings_path=SETTINGS_PATH):
    with open(settings_path, encoding="utf-8") as fh:
        settings = json.load(fh)
    if settings.pop("statusLine", None) is None:
        print("no statusLine key in %s; nothing to do" % settings_path)
        return
    with open(settings_path, "w", encoding="utf-8") as fh:
        json.dump(settings, fh, indent=2)
        fh.write("\n")
    print("removed statusLine from %s (backup %s kept)"
          % (settings_path, settings_path + BACKUP_SUFFIX))


def main(argv):
    if len(argv) > 1 and argv[1] == "--install":
        install()
        return 0
    if len(argv) > 1 and argv[1] == "--uninstall":
        uninstall()
        return 0
    return run_hook(sys.stdin.read())


if __name__ == "__main__":
    sys.exit(main(sys.argv))
