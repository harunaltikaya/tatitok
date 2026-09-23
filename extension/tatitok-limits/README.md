# tatitok-limits (M9 companion extension)

Standalone MV3 browser extension for tatitok **milestone 9** — surfaces external
provider **usage limits** that tatitok's verified token core structurally can't
produce on its own. It lives in the repo at `extension/tatitok-limits/` but ships
no Go, is not part of the tatitok build (not in `web/dist`, not seen by the
no-external-origins bundle check), and touches no verified source.
M9 numbers never enter tatitok's store/API/CLI/parity.

> **Built so far:** the two REPORTED-limit pollers (the Claude org id discovered
> per-user from the session, never hardcoded), a POST that feeds each snapshot to
> the tatitok hub's display-only ingest endpoint — the hub renders the limits in
> its own dashboard cards, so the corner overlay has been **retired** — and a
> minimal options page (the optional Claude org-ID override). NOT yet built: the
> ChatGPT message counter.

## What it does now

A service worker (`sw.js`) polls two provider usage endpoints on a gentle,
dashboard-gated `chrome.alarms` timer and writes normalized windows to
`chrome.storage.local`:

- **Claude** — cookie session, `GET /api/organizations/<org>/usage`. Every
  non-null `{ utilization, resets_at }` bucket becomes a window. `<org>` is the
  user's own org id, **discovered at runtime** from `GET /api/organizations`
  (same session) and cached — never hardcoded; an optional override lives in the
  options page, and the Claude poll is skipped (not crashed) when no org is known.
- **Codex** — bearer token from `…/api/auth/session`, then
  `GET /backend-api/wham/usage`. `primary_window` → `5h`, `secondary_window` → `Weekly`.

After each poll cycle the worker POSTs the **whole** snapshot (both providers,
last-good each, null providers omitted) in one request to the hub's display-only
ingest endpoint, `POST /api/v1/limits` on the configured hub URL (default
`http://127.0.0.1:8284`). The stored shape is the hub's JSON contract verbatim,
so no translation is needed. The hub merges per provider key (since `175102f`,
so other feeders such as the agy statusLine hook coexist); the extension still
sends claude and codex in one body. The POST is best-effort: if the hub is down
or rejects it, the worker warns and carries on, re-sending next cycle.

tatitok renders these limits natively now — inside its claude-max / chatgpt-plus
cards, fed by the hub's `GET /api/v1/limits`. (Earlier builds drew a fixed-position
corner overlay from a content script; that `overlay.js` has been retired now that
the native cards exist, so the extension no longer injects anything into the page.)

The `chrome.storage.local` shape is documented at the top of `storage.js`.

## Load & test (unpacked)

1. `chrome://extensions` → enable **Developer mode** → **Load unpacked** → this folder.
2. Open the tatitok dashboard at the configured hub URL (default
   `http://127.0.0.1:8284`) in a tab.
3. Open the service worker console: the extension card → **service worker** → Console.
4. Click the toolbar button — **poll now** (bypasses the dashboard gate).
5. Confirm both providers filled:
   `chrome.storage.local.get("limits").then(console.log)` in the SW console.
6. Confirm the hub received the snapshot:
   `curl -s http://127.0.0.1:8284/api/v1/limits` → `{"providers":{"claude":{…},"codex":{…}}}`
   with both providers' windows + `fetchedAt`. (The SW console also logs
   `hub ingest POST ok (204) [claude, codex]`.)

On the timer, polling runs **only while a tab on the configured hub URL (default
`http://127.0.0.1:8284`) is open** (otherwise it logs
`dashboard closed, skipping.`). Each provider polls in its own
try/catch; on failure it logs and keeps the last good snapshot.

After a poll lands, tatitok's own dashboard cards (claude-max / chatgpt-plus)
populate with the Claude + Codex meters from the hub feed — there is no longer a
separate extension overlay.

The Claude org id is discovered automatically on the first poll (then cached). If
you have multiple Claude orgs and the wrong one is picked — or discovery can't
reach the org list — open the extension's **options** (chrome://extensions →
Details → Extension options) and set your Claude organization ID; clearing it
re-enables auto-discovery.

The same options page has a **tatitok hub URL** field, for when `tatitok serve`
runs on a non-default `--addr`. It defaults to `http://127.0.0.1:8284` (blank
restores it) and accepts loopback origins only: `http://127.0.0.1[:port]` or
`http://localhost[:port]`, no path. Anything else is refused on save. Both the
ingest POST and the "dashboard open" polling gate use it.

## Permissions

`storage`, `alarms`, and host access to `claude.ai`, `chatgpt.com`, and the
loopback origins `http://127.0.0.1/*` and `http://localhost/*` (any port, for the
hub URL option). Nothing else.

## Known TODOs (later chunks)

- ChatGPT message-cap rules + send counter (a third reported meter).
- Options page: add the poll-interval + the ChatGPT message-cap rules (the
  Claude org-ID override already ships there).
