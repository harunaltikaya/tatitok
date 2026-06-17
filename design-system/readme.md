# tatitok — design system

**tatitok** is an open-source, **local-first** dashboard for tracking AI token usage and
cost. It unifies usage across coding harnesses (Claude Code, Codex, OpenCode), API
providers (Anthropic, OpenAI, DeepSeek), and self-hosted local models — then surfaces how
much **API-equivalent value** you extract from flat-rate subscriptions versus what you
actually pay. It is a single-page dashboard, **dark-mode-first**, served from a Go binary
and built with React, Vite, Tailwind, and ECharts.

This repository is the **design system** for that product: the colors, type, fonts, brand
assets, reusable React primitives, and a full dashboard UI kit. A consuming project links
`styles.css` and pulls components from the compiled bundle.

> The emotional center of the product is one number: **value extracted** — the
> API-equivalent dollars your usage would have cost, shown against what you actually paid.
> When that number is large and green, the design has done its job.

---

## Sources

This system was built by reading the product's real source. If you have access, explore
these to design with higher fidelity:

- **GitHub:** `harunaltikaya/tatitok` (private) — <https://github.com/harunaltikaya/tatitok>
  - `web/src/App.tsx` — the dashboard shell, range/timezone header, stat cards, panel grid
  - `web/src/components/{Plans,Breakdown,FacetRail,Panel,Chart}.tsx` — the real components
  - `web/src/api.ts` — number formatting (`usd`, `compactTokens`) and the data model
- **Brand assets provided:** `tatitok-logo.png` (mark + wordmark), `tatitok-logo-jost.png`
  (the owner's note pinning the wordmark to **Jost**).

The shipping app uses Tailwind's `zinc` neutrals with `sky`/`emerald`/`amber` accents. This
design system **refines** that into an intentional, restrained palette (economic-class
hues, sentence case, hairline borders) — the direction the product is moving toward, not a
literal screenshot of today's build.

---

## Content fundamentals

How tatitok writes.

- **Voice:** precise, understated, developer-native. Quietly proud of the savings, **never
  loud or salesy**. It states facts and trusts the reader to be impressed.
- **Casing:** **sentence case everywhere.** Never ALL-CAPS labels, never title-case
  headings. Section labels read `today`, `range totals`, `by harness`, `value extracted`.
- **Person:** addresses the reader as **you** ("what did it cost *you*"); the product
  refers to itself by name (`tatitok`), lowercase, never "we".
- **Numbers — one format, everywhere.** This is a load-bearing rule:
  - Tokens: compact with fixed precision — `1.18B`, `141.46M`, `4.5k`, `820`.
  - Money: `$908.49`; sub-cent totals keep digits rather than rounding to `$0.00`
    (`$0.0034`) — honesty over tidiness.
  - Multipliers: `4.5×` (the value-extracted ratio).
  - All numbers are **tabular** (`font-variant-numeric: tabular-nums`) so columns align.
- **Honesty markers, not decoration.** A trailing `*` means "this figure is a floor — some
  events carry no resolvable price." `≈` precedes an API-equivalent estimate. Accuracy is a
  visible dimension: events are `exact`, `derived`, or `estimated` — never blended silently.
- **Empty states teach.** "no data yet", "no active window — the next event opens one",
  "hub unreachable" — they say what to do, calmly.
- **Emoji:** effectively none in the UI. (The PRD uses one 🙂 in prose about locales; the
  product surface stays text-only.)
- **Examples of real copy:** `local AI usage` · `API-equivalent` · `resets in 2h 14m` ·
  `served from rollups` · `click to filter` · `restore the default panel order and sizes`.

---

## Visual foundations

The aesthetic: **calm, modern, information-dense without clutter** — deliberately the
opposite of a default Grafana dashboard. "Grafana power, Linear polish."

- **Surfaces & backgrounds.** Flat, solid fills. App background is near-black
  (`--bg-app` `#09090b`); cards sit one step up (`--surface-card` `#161618`). **No
  gradients, no glow, no images** behind data. Light mode flips to warm off-white
  (`#fafaf9`) cards on white.
- **Borders.** The signature detail: **thin `0.5px` hairlines** at low contrast
  (`--border-hairline`, ~9% white on dark). Structure comes from borders, not shadows.
- **Elevation.** Essentially flat. There are **no drop shadows on cards.** The only shadow
  in the system is a soft `--shadow-overlay` reserved for true floating layers (dialogs,
  menus, tooltips).
- **Corner radii.** Soft but not pill-y: controls and cards `--radius-md` (10px), panels
  `--radius-lg` (14px), chips and dots fully rounded.
- **Color = meaning, never decoration.** Neutral gray carries all structure, text, and
  chart axes. The **only categorical hues** encode the *economic class* of usage:
  - **violet** — subscription / plan-included
  - **amber** — metered API
  - **sky blue** — local / self-hosted
  - **green** — free
  No rainbow multi-series palettes. An entity keeps its color across every chart.
- **The positive number.** `value extracted` / savings render in **`--color-positive`**
  (`#34d399`) — a confident green so the savings feel good at a glance. Warnings escalate
  amber → red; over-cap and ingest errors are a soft red (`#f0726f`).
- **Typography.** One family — **Jost** (geometric, airy) — in **two weights only**:
  regular (400) and medium (500). Large, *quiet* hero numbers (regular weight, never bold-
  shouting) paired with small, muted labels. Generous whitespace.
- **Charts (ECharts).** Donut (cost by class), stacked bar/area (daily tokens & cost),
  hour×weekday heatmap (when you work), treemap (where spend concentrates). Transparent
  backgrounds, `#27272a` split lines, `#a1a1aa` 11px axis text, class colors for series.
- **Motion.** Quick and calm: `--dur-base` 170ms on a soft `--ease-out`. Fades and small
  position shifts — **no bounce, no spring, no decorative looping.** Respects
  `prefers-reduced-motion`.
- **Hover / press.** Hover lifts a surface by a faint white wash (`--surface-hover`, ~4%);
  press deepens it (`--surface-active`, ~7%). Interactive facet/filter selection uses the
  brand green at low key (`--accent-soft`), not a class hue. No scale/transform on press.
- **Transparency & blur.** Used sparingly — soft class-tinted fills for chips/rows
  (`color-mix` ~14%); no backdrop blur, no frosted glass.
- **Layout.** A fixed left **facet rail** (`--rail-width` 224px), a fluid main column of
  stat cards and reorderable chart panels, capped around `--content-max` 1320px. Dense but
  legible; whitespace does the separating.

---

## Iconography

- **Approach:** tatitok is icon-*light*. The shipping app uses almost no icons — it leans
  on **typography, numbers, and color dots** instead. The categorical *class dot* (a small
  filled circle in the economic-class hue) is the most important "icon" in the system; see
  the `ClassDot` component.
- **Unicode as icons.** The app uses a few characters as functional glyphs rather than an
  icon font: `→` (range separator), `×` (remove chip), `*` (unpriced-floor marker), `·`
  (meta separator), `⤢` / `✕` (panel fullscreen/close), `−` / `+` (resize). Keep these.
- **Status dots.** A small filled circle encodes liveness (green = SSE connected, gray =
  disconnected) and economic class — never an icon glyph.
- **No emoji** on the product surface.
- **When you need a real icon set:** the codebase ships none, so this system standardizes on
  **[Lucide](https://lucide.dev)** (thin, rounded, 1.5–2px stroke — it matches Jost's airy
  geometry and the hairline-border aesthetic). Load from CDN and keep strokes at
  `currentColor`. **This is a substitution** — flagged for the owner; swap if the product
  adopts a different set.
- **Brand mark:** `assets/tatitok-mark.svg` — the `[ • ]` viewfinder bracket with the brand-
  green dot, redrawn faithfully from the provided logo as a themeable SVG (brackets use
  `currentColor`, dot is `#1c9d74`). `assets/favicon.svg` is the dark-tile version.
  `assets/tatitok-logo-dark.png` is the original full lockup.

---

## What's in here (index)

| Path | What |
|---|---|
| `styles.css` | **The entry point** consumers link. `@import` manifest only. |
| `tokens/colors.css` | Neutral scale, economic-class hues, status, semantic + light-mode aliases. |
| `tokens/typography.css` | Jost, two weights, type scale, numeric helpers. |
| `tokens/spacing.css` | 4px grid, radii, hairline borders, motion, layout knobs. |
| `tokens/fonts.css` | `@font-face` for the self-hosted Jost variable file. |
| `tokens/base.css` | Reset + element defaults (dark canvas, tabular figures). |
| `fonts/jost-var.woff2` | Self-hosted Jost (variable, 100–900; system uses 400 & 500). |
| `assets/` | Brand mark SVG, favicon, original logo lockup. |
| `components/` | Reusable React primitives (buttons, forms, feedback, layout, navigation). |
| `ui_kits/dashboard/` | Full click-through recreation of the tatitok dashboard. |
| `templates/dashboard/` | A copy-and-edit dashboard scaffold (loads the system via `ds-base.js`). |
| `guidelines/` | Foundation specimen cards (Type, Colors, Spacing, Brand). |
| `SKILL.md` | Agent-Skills manifest for using this system in Claude Code. |

### Components
`Button`, `IconButton` · `Select`, `Switch` · `Badge`, `ClassDot`, `MeterBar` ·
`Card`, `Stat` · `Tabs`, `FilterChip`.

### UI kit
`ui_kits/dashboard` — the Overview with hero value, today / range stat cards, plan-window
meters, stacked daily charts, cost-by-class donut, and a click-to-filter breakdown.

---

## Notes & substitutions

- **Font:** Jost is self-hosted from Google Fonts (the owner's chosen face). If you have an
  official licensed/hinted build, drop it into `fonts/` and update `tokens/fonts.css`.
- **Icons:** Lucide is a substitution (the codebase ships no icon set). Flag for the owner.
- The brand mark SVG was redrawn from the provided PNG; if an official vector exists, prefer
  it.
