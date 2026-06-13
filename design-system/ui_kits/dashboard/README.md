# tatitok dashboard — UI kit

A high-fidelity, click-through recreation of the tatitok dashboard, built on the design
system's tokens and components. It is a **recreation of the real product** (see
`web/src/App.tsx` and `web/src/components/*` in `harunaltikaya/tatitok`), elevated to the
intended aesthetic: economic-class color, hairline borders, quiet Jost numbers, sentence
case.

## Run it
Open `index.html`. It loads React + Babel + ECharts + the design-system bundle, then mounts
the app. All data is mock (`data.js`) — deterministic so the view is stable.

## Screens
- **overview** — the hero `value extracted` number, today / range-totals stat cards, two
  plan-window cards (rolling-window meter + value-vs-price ratio), the daily
  API-equivalent stacked bar, a `value by class` donut, the `usage by model` breakdown
  table, and daily tokens.
- **explore** — pivot breakdown by model / provider / harness, an hour×weekday activity
  heatmap, and a treemap of where API-equivalent value concentrates.
- **live** — burn-rate / spend-rate / plan-projection tiles and a ticking live event stream.

## Interactions
Tabs switch screens · range presets (7d/30d/90d/all) · timezone select · **light/dark
toggle** (the header switch) · click a facet in the rail or a breakdown row to add a filter
chip · remove chips or `clear all` · hover any chart for a tooltip · the live stream ticks.

## Files
| File | Role |
|---|---|
| `index.html` | Shell: loads deps + the three `text/babel` screens, mounts `App`. |
| `data.js` | Mock data (`window.TT`) + the canonical number format (`usd`, `compactTokens`). |
| `charts.js` | ECharts option builders (`window.TTCharts`): donut, stacked bars, heatmap, treemap. |
| `parts.jsx` | Shared parts (`window.TTParts`): `EChart`, brand `Mark`, `Header`, `FacetRail`, `Breakdown`, `PlanCard`, `ClassLegend`. |
| `screens.jsx` | `Overview`, `Explore`, `Live` (`window.TTScreens`). |
| `app.jsx` | App shell: page/range/tz/theme/filter state, tabs, chips, mount. |

## Notes
- Charts use the **SVG renderer** with `animation: false` — crisp, instantly-painted, and
  cleanly capturable for PDF/PPTX export. It also matches the brand's calm, low-motion
  stance.
- Each `text/babel` script has its own scope; shared pieces are published on `window.*`.
- The kit composes the design-system primitives (`Button`, `Card`, `Stat`, `Badge`,
  `ClassDot`, `MeterBar`, `Select`, `Switch`, `Tabs`, `FilterChip`) — it never re-implements
  them.
