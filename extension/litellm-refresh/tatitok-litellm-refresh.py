#!/usr/bin/env python3
"""tatitok litellm-refresh — daily copy of LiteLLM's price file for the hub.

The tatitok binary makes no network calls. This companion is the one piece
that does, and only when run: it downloads LiteLLM's model price file (the
same upstream file the vendored snapshot comes from) to
<config>/tatitok/litellm-live.json, where the hub reads it as an ADD-ONLY
pricing layer — only models the vendored snapshot lacks are priced from it.
<config> is $XDG_CONFIG_HOME, else ~/.config (the hub's rule for prices.json).

Default (no flag): one refresh.
  - GET the source (30 s timeout); the body must be a JSON object with at
    least 1000 keys, else it is discarded;
  - a body whose sha256 equals the one recorded in litellm-live.meta.json
    (and on disk) exits 0 and touches nothing;
  - otherwise litellm-live.json is replaced atomically, then
    litellm-live.meta.json (fetched_at, sha256, bytes, source_url).
    Files 0600, directory 0700.
Any failure prints one line to stderr and exits 1: existing files stay
untouched and no temporary file is left behind. Content is never printed.

  --install    write the systemd user service + daily timer (both carry a
               marker line), daemon-reload, enable --now the timer; refuses
               when either unit exists without the marker
  --uninstall  disable --now the timer and remove the two units (only if
               they carry the marker)
"""

import datetime as _dt
import hashlib
import json
import os
import stat
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

# The upstream file internal/pricing/snapshot_meta.json records as the
# vendored snapshot's source_url.
SOURCE_URL = ("https://raw.githubusercontent.com/BerriAI/litellm/main/"
              "model_prices_and_context_window.json")
FETCH_TIMEOUT_S = 30
MIN_KEYS = 1000
# Read bound: the file is a few MiB; anything past this is not a price list.
MAX_BYTES = 64 * 1024 * 1024

LIVE_FILE = "litellm-live.json"
META_FILE = "litellm-live.meta.json"

UNIT_NAME = "tatitok-litellm-refresh"
UNIT_DIR = os.path.expanduser("~/.config/systemd/user")
MARKER = "# managed-by: tatitok-litellm-refresh.py --install"


class RefreshError(Exception):
    """One refresh failed; the message is the single stderr line."""


# ---- paths --------------------------------------------------------------

def config_dir():
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    return os.path.join(base, "tatitok")


# ---- refresh ------------------------------------------------------------

def _reject_constant(name):
    # NaN / Infinity are not JSON: the hub's parser would reject the file.
    raise ValueError("non-standard JSON constant %s" % name)


def fetch(url, urlopen=None, timeout=FETCH_TIMEOUT_S):
    """GET url and return the body bytes (bounded by MAX_BYTES)."""
    urlopen = urlopen or urllib.request.urlopen
    req = urllib.request.Request(url, headers={"User-Agent": "tatitok-litellm-refresh"})
    try:
        with urlopen(req, timeout=timeout) as resp:
            status = getattr(resp, "status", 200)
            if status != 200:
                raise RefreshError("GET %s: HTTP %d" % (url, status))
            body = resp.read(MAX_BYTES + 1)
    except RefreshError:
        raise
    except Exception as exc:  # noqa: BLE001 — every network failure is one line
        if isinstance(exc, urllib.error.HTTPError):
            exc.close()  # an HTTPError holds the open response
        raise RefreshError("GET %s: %s" % (url, exc)) from None
    if len(body) > MAX_BYTES:
        raise RefreshError("GET %s: body exceeds %d bytes" % (url, MAX_BYTES))
    return body


def validate(body):
    """The body must be UTF-8 JSON, an object, with at least MIN_KEYS keys.
    Returns the key count."""
    try:
        obj = json.loads(body.decode("utf-8"), parse_constant=_reject_constant)
    except (UnicodeDecodeError, ValueError) as exc:
        raise RefreshError("body is not valid JSON (%s)" % type(exc).__name__) from None
    if not isinstance(obj, dict):
        raise RefreshError("body is not a JSON object")
    if len(obj) < MIN_KEYS:
        raise RefreshError("body has %d keys, fewer than %d" % (len(obj), MIN_KEYS))
    return len(obj)


def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


def recorded_sha(cdir):
    """The sha256 in the meta file, but only when litellm-live.json on disk
    still hashes to it — a missing or damaged copy is refreshed, not kept."""
    try:
        with open(os.path.join(cdir, META_FILE), encoding="utf-8") as fh:
            meta = json.load(fh)
        sha = meta.get("sha256") if isinstance(meta, dict) else None
        with open(os.path.join(cdir, LIVE_FILE), "rb") as fh:
            on_disk = sha256_hex(fh.read())
    except (OSError, ValueError):
        return None
    return sha if sha == on_disk else None


def _write_tmp(cdir, data):
    """Write data to a fresh 0600 temp file in cdir; returns its path."""
    fd, tmp = tempfile.mkstemp(dir=cdir, prefix=".litellm-live.", suffix=".tmp")
    try:
        os.fchmod(fd, stat.S_IRUSR | stat.S_IWUSR)
        with os.fdopen(fd, "wb") as fh:
            fh.write(data)
            fh.flush()
            os.fsync(fh.fileno())
    except BaseException:
        _unlink_quiet(tmp)
        raise
    return tmp


def _unlink_quiet(path):
    try:
        os.unlink(path)
    except OSError:
        pass


def rfc3339_utc(t):
    return t.astimezone(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def refresh(cdir=None, url=SOURCE_URL, urlopen=None, now=None):
    """One refresh. Returns (changed, keys, sha256); raises RefreshError.

    Both files are written to temp files first and only then renamed into
    place (live file, then meta), so a failure before the renames leaves the
    existing pair untouched and removes the temp files."""
    cdir = cdir or config_dir()
    body = fetch(url, urlopen=urlopen)
    keys = validate(body)
    sha = sha256_hex(body)
    if recorded_sha(cdir) == sha:
        return False, keys, sha

    now = now or _dt.datetime.now(_dt.timezone.utc)
    meta = {
        "fetched_at": rfc3339_utc(now),
        "sha256": sha,
        "bytes": len(body),
        "source_url": url,
    }
    meta_bytes = (json.dumps(meta, indent=2) + "\n").encode()
    tmps = []
    try:
        os.makedirs(cdir, mode=stat.S_IRWXU, exist_ok=True)
        os.chmod(cdir, stat.S_IRWXU)
        tmps.append(_write_tmp(cdir, body))
        tmps.append(_write_tmp(cdir, meta_bytes))
        os.replace(tmps[0], os.path.join(cdir, LIVE_FILE))
        os.replace(tmps[1], os.path.join(cdir, META_FILE))
    except OSError as exc:
        raise RefreshError("write %s: %s" % (cdir, exc)) from None
    finally:
        for t in tmps:
            _unlink_quiet(t)  # already renamed → nothing left to remove
    return True, keys, sha


def run_refresh(**kw):
    try:
        changed, keys, sha = refresh(**kw)
    except RefreshError as exc:
        sys.stderr.write("tatitok-litellm-refresh: %s\n" % exc)
        return 1
    print("%s %s (%d keys, sha256 %s)"
          % ("refreshed" if changed else "unchanged", LIVE_FILE, keys, sha[:12]))
    return 0


# ---- install / uninstall ------------------------------------------------

def _unit_quote(s):
    """Quote a value for a systemd unit line: % → %%, and double-quote it
    (with C escapes) when it holds whitespace, quotes or backslashes."""
    s = s.replace("%", "%%")
    if any(c.isspace() or c in "\"'\\" for c in s):
        s = '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'
    return s


def service_unit(script):
    lines = [
        MARKER,
        "[Unit]",
        "Description=tatitok: refresh LiteLLM's price file (add-only live pricing layer)",
        "",
        "[Service]",
        "Type=oneshot",
    ]
    xdg = os.environ.get("XDG_CONFIG_HOME")
    if xdg:
        # The hub reads <XDG_CONFIG_HOME>/tatitok; the user manager may not
        # carry the variable, so the service pins the install-time value.
        lines.append("Environment=" + _unit_quote("XDG_CONFIG_HOME=" + xdg))
    lines.append("ExecStart=" + _unit_quote(script))
    return "\n".join(lines) + "\n"


def timer_unit():
    return "\n".join([
        MARKER,
        "[Unit]",
        "Description=tatitok: daily LiteLLM price refresh",
        "",
        "[Timer]",
        "OnCalendar=daily",
        "Persistent=true",
        "RandomizedDelaySec=1h",
        "",
        "[Install]",
        "WantedBy=timers.target",
    ]) + "\n"


def unit_paths(unit_dir):
    return (os.path.join(unit_dir, UNIT_NAME + ".service"),
            os.path.join(unit_dir, UNIT_NAME + ".timer"))


def is_ours(path):
    """True when the unit file carries the marker line."""
    try:
        with open(path, encoding="utf-8") as fh:
            return any(line.rstrip("\n") == MARKER for line in fh)
    except (OSError, UnicodeDecodeError):
        return False


def _systemctl(*args):
    subprocess.run(["systemctl", "--user", *args], check=True)


def install(unit_dir=UNIT_DIR, script=None, systemctl=_systemctl):
    script = script or os.path.abspath(__file__)
    if not os.access(script, os.X_OK):
        raise SystemExit("refusing: %s is not executable (chmod +x it first)" % script)
    service, timer = unit_paths(unit_dir)
    for path in (service, timer):
        if os.path.exists(path) and not is_ours(path):
            raise SystemExit("refusing: %s exists and is not ours (no marker line)" % path)
    os.makedirs(unit_dir, exist_ok=True)
    for path, text in ((service, service_unit(script)), (timer, timer_unit())):
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
    systemctl("daemon-reload")
    systemctl("enable", "--now", UNIT_NAME + ".timer")
    print("installed %s\n          %s\ntimer enabled (daily); run the script once for a first refresh"
          % (service, timer))
    return 0


def uninstall(unit_dir=UNIT_DIR, systemctl=_systemctl):
    """Disable the timer and remove both units — each only if it carries the
    marker; a foreign unit is named and left alone (exit 1)."""
    service, timer = unit_paths(unit_dir)
    foreign = [p for p in (service, timer) if os.path.exists(p) and not is_ours(p)]
    for p in foreign:
        print("refusing: %s is not ours (no marker line), leaving it alone" % p)
    ours = [p for p in (service, timer) if os.path.exists(p) and is_ours(p)]
    if not ours:
        if not foreign:
            print("no %s units in %s; nothing to do" % (UNIT_NAME, unit_dir))
        return 1 if foreign else 0
    if timer in ours:
        systemctl("disable", "--now", UNIT_NAME + ".timer")
    for p in ours:
        os.unlink(p)
        print("removed %s" % p)
    systemctl("daemon-reload")
    return 1 if foreign else 0


def main(argv):
    if len(argv) > 1 and argv[1] == "--install":
        return install()
    if len(argv) > 1 and argv[1] == "--uninstall":
        return uninstall()
    if len(argv) > 1:
        sys.stderr.write("usage: %s [--install | --uninstall]\n" % os.path.basename(argv[0]))
        return 2
    return run_refresh()


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv))
    except subprocess.CalledProcessError as exc:
        sys.stderr.write("tatitok-litellm-refresh: %s\n" % exc)
        sys.exit(1)
