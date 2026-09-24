#!/usr/bin/env python3
"""tatitok quota-alert — a desktop notification when a reported limit
crosses its threshold.

A systemd user timer runs this every 5 minutes. One run:
  - GET <hub_url>/api/v1/limits on loopback (2 s timeout). Hub down, a
    non-200 or a body that is not the limits shape prints one line to
    stderr and exits 0.
  - for every provider window: threshold = thresholds[provider][label] if
    set, else default_threshold. usedPercent >= threshold alerts, unless
    the state file already holds this "provider|label" with the same
    resetAt (within 60 s: claude.ai's resetAt moves by milliseconds between
    polls). The state maps "provider|label" -> the resetAt last alerted,
    so a window alerts once per crossing and re-arms when its resetAt
    changes. Nothing is sent on the way down.
  - an alert is `notify-send -- tatitok "<provider> <label> <pct>% — resets
    <local HH:MM>"`. notify-send missing or failing prints the same line
    to stderr and exits 0. The alert still counts as sent (state updated).
    Provider and label are shown display-safe (display_safe: non-printable
    characters dropped, cut to 24 / 48 characters); state keys and
    thresholds use them unchanged.
No network call leaves the machine: the hub URL must be loopback.

Config <config>/tatitok/alerts.json (keep it 0600), <config> =
$XDG_CONFIG_HOME, else ~/.config:
  {"hub_url": "http://127.0.0.1:8284", "default_threshold": 80,
   "thresholds": {"claude": {"Fable 7d": 70}}}
hub_url and thresholds are optional. A missing or invalid config prints
one line to stderr and exits 1.
State <data>/tatitok/quota-alert/state.json, <data> = $XDG_DATA_HOME,
else ~/.local/share. A damaged state file is treated as empty.

  --install    write the systemd user service + 5-minute timer (both carry
               a marker line), daemon-reload, enable --now the timer;
               refuses when either unit exists without the marker, or
               when a value it would write into the service
               (XDG_CONFIG_HOME, XDG_DATA_HOME, the script path) holds a
               CR or LF
  --uninstall  disable --now the timer and remove the two units (only if
               they carry the marker)
"""

import datetime as _dt
import http.client
import json
import math
import os
import re
import stat
import subprocess
import sys
import tempfile
import urllib.parse

# The same loopback rule as the agy hook and the browser extension: http,
# 127.0.0.1 or localhost, optional decimal port, nothing else.
DEFAULT_HUB_URL = "http://127.0.0.1:8284"
HUB_URL_RE = re.compile(r"^http://(127\.0\.0\.1|localhost)(:\d{1,5})?$")
LIMITS_PATH = "/api/v1/limits"
FETCH_TIMEOUT_S = 2
# Read bound: a snapshot is a handful of windows; the hub caps POSTs at 64 KiB.
MAX_BYTES = 1 << 20
NOTIFY_TIMEOUT_S = 5
# claude.ai reports the same reset instant with a different millisecond part
# on each poll (seen 2026-09-24: ...600613 then ...600629). resetAt values this
# close are one window; a new window resets at least an hour later.
RESET_TOLERANCE_MS = 60_000
# Display caps, in characters, for hub text shown in an alert or on stderr.
PROVIDER_CAP = 24
LABEL_CAP = 48

CONFIG_FILE = "alerts.json"
CONFIG_KEYS = ("hub_url", "default_threshold", "thresholds")

UNIT_NAME = "tatitok-quota-alert"
# Pinned into the service when set at install time (see service_unit).
UNIT_ENV = ("XDG_CONFIG_HOME", "XDG_DATA_HOME")
UNIT_DIR = os.path.expanduser("~/.config/systemd/user")
MARKER = "# managed-by: quota_alert.py --install"


class ConfigError(Exception):
    """alerts.json is missing or invalid; the message is the stderr line."""


class HubError(Exception):
    """The hub could not be read; the message is the stderr line."""


# ---- paths --------------------------------------------------------------

def config_path():
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    return os.path.join(base, "tatitok", CONFIG_FILE)


def state_path():
    base = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    return os.path.join(base, "tatitok", "quota-alert", "state.json")


# ---- config -------------------------------------------------------------

def _is_threshold(v):
    return (isinstance(v, (int, float)) and not isinstance(v, bool)
            and math.isfinite(v) and v > 0)


def load_config(path):
    """Read and validate alerts.json. Returns {"hub_url", "default_threshold",
    "thresholds"}; raises ConfigError."""
    try:
        with open(path, encoding="utf-8") as fh:
            cfg = json.load(fh)
    except FileNotFoundError:
        raise ConfigError("no config at %s" % path) from None
    except (OSError, ValueError) as exc:
        raise ConfigError("%s: %s" % (path, exc)) from None
    if not isinstance(cfg, dict):
        raise ConfigError("%s: not a JSON object" % path)
    unknown = sorted(set(cfg) - set(CONFIG_KEYS))
    if unknown:
        raise ConfigError("%s: unknown key(s) %s (supported: %s)"
                          % (path, ", ".join(unknown), ", ".join(CONFIG_KEYS)))
    hub = cfg.get("hub_url", DEFAULT_HUB_URL)
    if not isinstance(hub, str) or not HUB_URL_RE.fullmatch(hub):
        raise ConfigError("%s: hub_url must be a loopback origin like %s"
                          % (path, DEFAULT_HUB_URL))
    default = cfg.get("default_threshold")
    if not _is_threshold(default):
        raise ConfigError("%s: default_threshold must be a number > 0" % path)
    thresholds = cfg.get("thresholds", {})
    if not isinstance(thresholds, dict):
        raise ConfigError("%s: thresholds must be an object" % path)
    for provider, labels in thresholds.items():
        if not isinstance(labels, dict):
            raise ConfigError("%s: thresholds.%s must be an object" % (path, provider))
        for label, v in labels.items():
            if not _is_threshold(v):
                raise ConfigError("%s: thresholds.%s.%s must be a number > 0"
                                  % (path, provider, label))
    return {"hub_url": hub, "default_threshold": default, "thresholds": thresholds}


# ---- hub ----------------------------------------------------------------

def fetch_limits(hub_url, timeout=FETCH_TIMEOUT_S):
    """GET /api/v1/limits and return its "providers" object. http.client
    (not urllib) so no proxy setting can route the request off the box."""
    u = urllib.parse.urlsplit(hub_url)
    conn = http.client.HTTPConnection(u.hostname, u.port or 80, timeout=timeout)
    try:
        conn.request("GET", LIMITS_PATH)
        with conn.getresponse() as resp:
            status = resp.status
            body = resp.read(MAX_BYTES + 1)
    except (OSError, http.client.HTTPException) as exc:
        raise HubError("GET %s%s: %s" % (hub_url, LIMITS_PATH, exc)) from None
    finally:
        conn.close()
    if status != 200:
        raise HubError("GET %s%s: HTTP %d" % (hub_url, LIMITS_PATH, status))
    if len(body) > MAX_BYTES:
        raise HubError("GET %s%s: body exceeds %d bytes" % (hub_url, LIMITS_PATH, MAX_BYTES))
    try:
        doc = json.loads(body.decode("utf-8"))
    except ValueError as exc:
        raise HubError("GET %s%s: bad JSON (%s)" % (hub_url, LIMITS_PATH, type(exc).__name__)) from None
    providers = doc.get("providers") if isinstance(doc, dict) else None
    if not isinstance(providers, dict):
        raise HubError("GET %s%s: no providers object" % (hub_url, LIMITS_PATH))
    return providers


def windows_of(providers):
    """(provider, label, usedPercent, resetAt) for every well-formed window,
    in the hub's order. A provider with windows: null has none."""
    for provider, bucket in providers.items():
        wins = bucket.get("windows") if isinstance(bucket, dict) else None
        for w in wins or ():
            if not isinstance(w, dict):
                continue
            label, pct, reset = w.get("label"), w.get("usedPercent"), w.get("resetAt")
            if (isinstance(label, str) and label
                    and isinstance(pct, (int, float)) and not isinstance(pct, bool)
                    and isinstance(reset, int) and not isinstance(reset, bool)):
                yield provider, label, pct, reset


# ---- alerts -------------------------------------------------------------

def threshold_for(cfg, provider, label):
    return cfg["thresholds"].get(provider, {}).get(label, cfg["default_threshold"])


def same_window(alerted, reset):
    """True when reset is the resetAt already alerted (within the tolerance)."""
    return alerted is not None and abs(reset - alerted) < RESET_TOLERANCE_MS


def due_alerts(providers, cfg, state):
    """Windows at or over their threshold whose resetAt differs from the one
    last alerted. Pure: returns [(key, provider, label, pct, resetAt)]."""
    out = []
    for provider, label, pct, reset in windows_of(providers):
        key = "%s|%s" % (provider, label)
        if pct >= threshold_for(cfg, provider, label) and not same_window(state.get(key), reset):
            out.append((key, provider, label, pct, reset))
    return out


def display_safe(text, cap):
    """text as it may be shown: every character str.isprintable() rejects
    (C0/C1 controls such as ESC, CR and LF, format characters, separators
    other than the space) dropped, then cut to cap characters. Hub text
    reaches a notification and stderr; the hub bounds it too."""
    return "".join(c for c in text if c.isprintable())[:cap]


def alert_body(provider, label, pct, reset_at):
    """One line; provider and label display-safe, the reset time in this
    machine's local zone."""
    try:
        when = _dt.datetime.fromtimestamp(reset_at / 1000).strftime("%H:%M") if reset_at > 0 else "unknown"
    except (OverflowError, OSError, ValueError):
        when = "unknown"
    return "%s %s %d%% — resets %s" % (display_safe(provider, PROVIDER_CAP),
                                       display_safe(label, LABEL_CAP), round(pct), when)


def notify(body, run=subprocess.run):
    """notify-send the alert; on any failure print it to stderr instead.
    "--" ends notify-send's options, so a body starting with "-" stays the
    body. Never raises."""
    try:
        run(["notify-send", "--", "tatitok", body], check=True, timeout=NOTIFY_TIMEOUT_S,
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return True
    except (OSError, subprocess.SubprocessError) as exc:
        sys.stderr.write("tatitok-quota-alert: notify-send failed (%s): %s\n"
                         % (type(exc).__name__, body))
        return False


# ---- state --------------------------------------------------------------

def load_state(path):
    """"provider|label" -> resetAt. A missing or damaged file is empty."""
    try:
        with open(path, encoding="utf-8") as fh:
            st = json.load(fh)
    except (OSError, ValueError):
        return {}
    if not isinstance(st, dict):
        return {}
    return {k: v for k, v in st.items()
            if isinstance(k, str) and isinstance(v, int) and not isinstance(v, bool)}


def save_state(path, state):
    """Atomic: a 0600 temp file in the 0700 state dir, then os.replace."""
    sdir = os.path.dirname(path)
    os.makedirs(sdir, mode=stat.S_IRWXU, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=sdir, prefix=".state.", suffix=".tmp")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump(state, fh, separators=(",", ":"), sort_keys=True)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


# ---- one run ------------------------------------------------------------

def run_once(cfg_path=None, st_path=None, fetch=fetch_limits, send=notify):
    """One timer run. Returns the exit code."""
    try:
        cfg = load_config(cfg_path or config_path())
    except ConfigError as exc:
        sys.stderr.write("tatitok-quota-alert: %s\n" % exc)
        return 1
    try:
        providers = fetch(cfg["hub_url"])
    except HubError as exc:
        sys.stderr.write("tatitok-quota-alert: %s\n" % exc)
        return 0
    st_path = st_path or state_path()
    state = load_state(st_path)
    due = due_alerts(providers, cfg, state)
    for key, provider, label, pct, reset in due:
        send(alert_body(provider, label, pct, reset))
        state[key] = reset
    if due:
        save_state(st_path, state)
    return 0


# ---- install / uninstall ------------------------------------------------

def _unit_quote(s):
    """Quote a value for a systemd unit line: % → %%, and double-quote it
    (with C escapes) when it holds whitespace, quotes or backslashes."""
    s = s.replace("%", "%%")
    if any(c.isspace() or c in "\"'\\" for c in s):
        s = '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'
    return s


def unit_env():
    """[(name, value)] of the UNIT_ENV variables set now: what service_unit
    pins as Environment= lines."""
    return [(var, os.environ[var]) for var in UNIT_ENV if os.environ.get(var)]


def service_unit(script):
    lines = [
        MARKER,
        "[Unit]",
        "Description=tatitok: desktop alert when a reported usage limit crosses its threshold",
        "",
        "[Service]",
        "Type=oneshot",
        # notify-send talks to the desktop session bus; %t is the user
        # runtime dir, so this is the systemd user default bus address.
        "Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=%t/bus",
    ]
    for var, val in unit_env():
        # The config and state paths follow these; the user manager may
        # not carry them, so the service pins the install-time value.
        lines.append("Environment=" + _unit_quote(var + "=" + val))
    lines.append("ExecStart=" + _unit_quote(script))
    return "\n".join(lines) + "\n"


def timer_unit():
    return "\n".join([
        MARKER,
        "[Unit]",
        "Description=tatitok: check reported usage limits every 5 minutes",
        "",
        "[Timer]",
        "OnCalendar=*:0/5",
        "Persistent=true",
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
    # _unit_quote cannot carry a CR or LF: it would split the unit line.
    for name, val in unit_env() + [("script path", script)]:
        if "\r" in val or "\n" in val:
            raise SystemExit("refusing: %s %r holds a CR or LF, which a systemd unit line "
                             "cannot carry" % (name, val))
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
    print("installed %s\n          %s\ntimer enabled (every 5 min)" % (service, timer))
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
    return run_once()


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv))
    except subprocess.CalledProcessError as exc:
        sys.stderr.write("tatitok-quota-alert: %s\n" % exc)
        sys.exit(1)
