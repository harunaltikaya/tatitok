# tatitok quota-alert

A desktop notification when a reported usage limit crosses its threshold.
A systemd user timer runs `quota_alert.py` every 5 minutes. Each run reads the
running hub's `GET /api/v1/limits` over loopback (the same numbers the plan
cards show) and alerts once per window per crossing:

    tatitok
    claude Fable 7d 71% — resets 14:00

- A window alerts when `usedPercent` reaches its threshold. The state file
  (`~/.local/share/tatitok/quota-alert/state.json`, under `$XDG_DATA_HOME` if
  set) remembers the `resetAt` it alerted for, so the same window stays quiet
  until the provider reports a new `resetAt`. Values within 60 s count as the
  same (claude.ai's `resetAt` moves by milliseconds between polls). Nothing is
  sent on the way down. Limitation: two successive windows of one provider and
  label whose `resetAt` values lie under 60 s apart count as one window, so the
  second does not alert; the 5h and 7d windows reset hours apart, and the hub
  does not enforce a minimum.
- The reset time is shown in this machine's local zone.
- Hub down, or `notify-send` missing or failing, prints one line to stderr
  (the journal) and exits 0. There are no network calls: the hub URL must be
  loopback.

## Config

`~/.config/tatitok/alerts.json` (under `$XDG_CONFIG_HOME` if set), mode 0600:

    {
      "hub_url": "http://127.0.0.1:8284",
      "default_threshold": 80,
      "thresholds": { "claude": { "Fable 7d": 70 } }
    }

`hub_url` (default `http://127.0.0.1:8284`) must be `http://127.0.0.1[:port]`
or `http://localhost[:port]`. `thresholds` is optional and keyed by provider
(`claude`, `codex`, `agy`) and then by window label as the cards show it. A
missing or invalid config makes the run exit 1 with one line on stderr.

## Install

    python3 extension/quota-alert/quota_alert.py --install

This writes `~/.config/systemd/user/tatitok-quota-alert.service` and
`tatitok-quota-alert.timer` (both carry a marker line), runs daemon-reload and
enables the timer (`OnCalendar=*:0/5`, `Persistent=true`). It refuses to
overwrite units without the marker. `--uninstall` disables the timer and
removes only marked units.

notify-send needs the desktop session bus. The service sets
`Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=%t/bus`, the systemd user
default (`%t` is `$XDG_RUNTIME_DIR`). On a box with no desktop session the
notification goes nowhere and the alert is only in the journal:

    journalctl --user -u tatitok-quota-alert.service

## Test

    python3 -W error::ResourceWarning -m unittest extension/quota-alert/test_quota_alert.py
