# tatitok

A local-first dashboard that shows the API-equivalent value of your Claude Code, Codex, and OpenCode usage versus what you actually pay — all on your own machine.

![tatitok dashboard home — a large green "value extracted" headline reading $1103.62 API-equivalent next to $0.08 actually paid, two usage-limit cards, a daily API-equivalent bar chart, and a value-by-harness donut](docs/images/dashboard.png)

*The home view: $1103.62 of usage priced at API rates that actually cost $0.08 out of pocket, with the daily breakdown and value-by-harness split below.*

## What it does

tatitok reads the logs your coding agents already write to disk, prices that usage at each provider's published API rates, and puts two numbers next to each other: what the same tokens would have cost on the metered API, and what you actually pay out of pocket on your subscription.

What makes it different is the honesty stance. Everything runs on loopback (`127.0.0.1`) — nothing ever leaves your machine and there's no telemetry. And it never guesses your subscription: you declare your own plan and prices, because a wrong guess would fake the exact number the tool exists to make honest.

The home view leads with the number you came for, then fills in the texture below it — when you tend to be active, and which harness the value comes from.

![Lower on the home view — a "when you're active" heatmap by hour and weekday, beside a value-by-harness donut and its table](docs/images/home-activity.png)

*Further down the home page: an activity heatmap by hour and weekday, and value broken out by harness.*

## Quickstart

You'll need **Go 1.25+** and **Node 24** (the repo pins `24.15.0` in `web/.nvmrc`; if you use nvm, `nvm use` in the repo picks it up). The build compiles the Go binary and bundles the dashboard into it. There's no CGO and no database to install — SQLite is embedded.

```sh
git clone https://github.com/harunaltikaya/tatitok.git
cd tatitok
make build            # builds the dashboard + the Go binary into ./dist/tatitok
./dist/tatitok serve  # starts the local hub on 127.0.0.1:8284
```

Then open **http://127.0.0.1:8284** in your browser.

On first run, `serve` scans the agent logs already on your machine and ingests your history automatically — there's no separate import step. Leave it running: it watches for new activity and updates the dashboard live. Stop it with Ctrl-C.

## Onboarding — set up your plans

The first time you open the dashboard with usage present, tatitok pops up a panel titled **"set up your plans."** This is how it learns what you actually pay, so it can show plan-covered usage as $0 out of pocket with its API-equivalent value beside it.

![The "set up your plans" panel — a claude-max card reading "choose your plan — not auto-detectable", a chatgpt-plus card reading "detected: Plus", editable monthly-price fields, and skip / apply buttons](docs/images/onboarding.png)

*Codex is detected and pre-filled ("detected: Plus"); Claude can't be, so the panel asks ("choose your plan — not auto-detectable"). Every price is an editable list default.*

There's one card per provider. The flow:

1. **It pre-fills what it can detect.** Your ChatGPT/Codex tier (Plus, Pro, …) is read straight from your Codex logs and shown already selected, marked with a `✓`.

2. **It asks for what it can't.** Your Claude tier — Free, Pro, Max 5×, or Max 20× — isn't recorded in any log, so the Claude card reads *"choose your plan — not auto-detectable"* and waits for you to pick. Same story for ChatGPT **Pro $100 vs $200**: both look identical in the logs (*"Codex Pro detected — pick $100 or $200"*), so you choose.

3. **Prices are pre-filled at published list rates.** Each paid tier shows its monthly price in an editable box. If your real bill differs — tax, annual billing, a promo — just edit it.

4. **Pay per token instead?** Click **metered** on a provider's card. That writes no plan; that harness stays billed at API rates — the right answer if you don't have a subscription there.

5. **Hit "apply."** tatitok reprices your stored usage under the plans you declared. Plan-covered usage now reads $0 out of pocket with its API-equivalent value next to it, and the dashboard refreshes.

You can dismiss the panel with **"skip for now"** and reopen it anytime from the **plans** button in the header.

### Why it asks instead of guessing

This is the whole idea, so it's worth saying plainly. Your Claude subscription tier genuinely is not in any log tatitok can read (the API's `service_tier` field is a serving class, not your plan), and ChatGPT Pro $100 and Pro $200 are indistinguishable in the Codex logs. tatitok *could* guess — but the point of the tool is to tell you a true number, and a guessed plan would silently fake the one figure you came here to trust. So it detects what's real, asks for what isn't, and shows every price as an editable default. Nothing is invented.

> Prefer the terminal? `tatitok onboard` does the same thing from the CLI: it auto-detects your Codex tier, prompts for the rest, and prints the `tatitok recompute --pricing` step that reprices your stored events. Run `tatitok onboard --help` for the flags.

## The browser extension (optional)

The dashboard's core is built only from numbers your local logs actually contain. Some things aren't in any local file — like your live **usage-limit windows** on Claude and ChatGPT ("X% of your weekly limit used"); only the provider's own web session knows those. The companion extension, in `extension/tatitok-limits/`, fills that gap: it reads those limit numbers from your logged-in claude.ai / chatgpt.com sessions and posts them to your local hub, which renders them as cards on the dashboard.

![Two usage-limit cards — claude-max with 5h, 7d and Sonnet windows showing $1190.67 extracted vs $200.00/mo (6.0×), and chatgpt-plus with 5h and Weekly windows showing $264.09 vs $20.00/mo (13.2×)](docs/images/limit-cards.png)

*The cards the extension feeds: each window's usage, plus how much value you've pulled from the plan against its monthly price (6.0× and 13.2× here).*

Setup is manual for now — it isn't in the Chrome Web Store yet:

1. Open `chrome://extensions`.
2. Turn on **Developer mode** (top-right toggle).
3. Click **Load unpacked** and select the `extension/tatitok-limits/` folder.
4. The first time it reaches the hub, Chrome shows a **Local Network Access** prompt — allow it, so the extension can talk to `127.0.0.1`.

There's no build step; it's plain unpacked files. See [`extension/tatitok-limits/README.md`](extension/tatitok-limits/README.md) for details and options.

## How it works / what's tracked

tatitok reads three coding-agent harnesses from the standard locations they already write to:

- **Claude Code** — session logs under `~/.claude`
- **Codex** — session logs under `~/.codex`
- **OpenCode** — its local database under `~/.local/share/opencode`

It takes the provider-reported token counts verbatim (no tokenizers, no estimating), stores them in a local SQLite file, and prices them against a pinned snapshot of published rates.

Click into a harness — or any chart — for the detail view: totals for the range you're looking at, then the day-by-day API-equivalent and actual-cost charts.

![Detail view, upper area — the usage-limit cards, a range-totals row reading $1103.62 API-equivalent, $0.08 actual, 1.19B tokens and 7 active days, and daily API-equivalent and daily actual-cost charts](docs/images/detail-totals.png)

*The detail view: totals for the selected range, with daily API-equivalent and daily actual-cost charts underneath.*

Scroll down and the same range breaks out by harness, provider, and model, one row at a time.

![Detail view, lower area — a daily-tokens chart above breakdown tables by harness, provider and model, each row pairing tokens with $0.00 actual and the API-equivalent value](docs/images/detail-breakdown.png)

*Each row pairs the tokens with what you actually paid ($0.00 on a covered plan) and the API-equivalent value beside it.*

The charts aren't static — any panel expands to fullscreen, and hovering a day breaks it down by provider and model.

![A chart panel expanded to fullscreen, with a hover tooltip breaking one day down by provider and model](docs/images/chart-fullscreen.png)

*A panel expanded to fullscreen; hovering a day splits it by provider and model.*

Privacy, restated plainly: it binds to loopback only (a non-loopback bind is refused), makes no network calls of its own, and sends no telemetry — ever. Your prompts and responses are never stored, only token counts and metadata.

## Honest scope (this is a v1)

A few things to know going in:

- **You build it from source.** No prebuilt binaries yet — clone and `make build`.
- **The extension is unpacked.** Load-unpacked plus a Local Network Access grant, as above. Not in the Web Store yet.
- **Metered-only users see $0 API-equivalent.** If you have no subscription at all on a provider (pure pay-per-token), the API-equivalent value for that provider currently shows $0. The numbers are correct for subscription users; closing this gap is on the list.

<details>
<summary><b>More screenshots — full pages at a glance</b></summary>

<br>

The text is small at this zoom — click either image to open it at full resolution.

[![The entire home page at a glance, zoomed out](docs/images/home-fullscreen.png)](docs/images/home-fullscreen.png)

*The full home page.*

[![The entire detail page at a glance, zoomed out](docs/images/detail-fullscreen.png)](docs/images/detail-fullscreen.png)

*The full detail page.*

</details>

## License

[MIT](LICENSE) © 2026 Harun Altikaya.
