# tatitok-agy-statusline

agy (Antigravity CLI) statusLine hook, a companion like `tatitok-limits`: on every
status refresh it appends the status object (minus `email`) to
`$XDG_DATA_HOME/tatitok/agy/statusline.jsonl` when the conversation's token
totals changed, and every 90 s POSTs the four agy quota windows to the running
hub's display-only `/api/v1/limits` (provider `agy`). Always prints `tatitok`.

- Install: `python3 extension/agy-statusline/tatitok-agy-statusline.py --install`
  (backs up `~/.gemini/antigravity-cli/settings.json` to `settings.json.pre-tatitok`).
- Uninstall: same script with `--uninstall` (removes the key, keeps the backup).
- Custom hub address: set `TATITOK_HUB_URL` (default `http://127.0.0.1:8284`).
- Test: `python3 -m unittest extension/agy-statusline/test_statusline.py`
