# tatitok cc-statusline

Claude Code status line, the agy hook's twin in the other direction: Claude
Code runs `cc_statusline.py` as its `statusLine` command, the script asks the
running hub, and it prints one line:

    today 1.2M tok · $14.20 api-eq | claude 5h 63% · 7d 57% · Fable 7d 71%

- Left, verified: today's tokens (input + output + cache write + cache read,
  the dashboard's total) and API-equivalent cost, summed from
  `GET /api/v1/stats/daily?from=D&to=D&timezone=Z`, the rows the dashboard's
  range totals sum. D is today in the local zone Z, which is `$TZ` if set, else
  the zone the `/etc/localtime` symlink points at, else UTC, and is passed as
  `timezone=` the way the dashboard passes it.
- Right, reported: the `claude` provider's windows from `GET /api/v1/limits`,
  in the order the hub serves them (the companion extension's numbers).

Claude Code pipes a JSON object on stdin (model, workspace, cost,
context_window, rate_limits and so on). The script reads it to EOF with the agy
hook's bounded read and discards it. Nothing from stdin is used or stored.

Budget: 200 ms end to end. Both GETs start at once, each capped at 80 ms. If
the hub is down or a reply misses the budget, the line holds the part that
arrived, else `tatitok: hub down`. The script always exits 0.

- Install: `python3 extension/cc-statusline/cc_statusline.py --install`
  sets `"statusLine": {"type": "command", "command": "python3 <abs path>"}` in
  `~/.claude/settings.json`. The original is first copied to
  `settings.json.pre-tatitok-statusline` (0600). The key is spliced in as text,
  so every other key keeps its bytes. Install refuses when a different
  statusLine is already set or when the backup already exists. Installing
  the same command twice changes nothing.
- Uninstall: same script with `--uninstall`. It removes the key only when its
  command is exactly this script's, and keeps the backup.
- Custom hub address: set `TATITOK_HUB_URL` (default `http://127.0.0.1:8284`).
  Only `http://127.0.0.1[:port]` or `http://localhost[:port]` is accepted;
  anything else falls back to the default.
- Test: `python3 -W error::ResourceWarning -m unittest extension/cc-statusline/test_cc_statusline.py`
