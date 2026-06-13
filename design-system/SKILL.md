---
name: tatitok-design
description: Use this skill to generate well-branded interfaces and assets for tatitok, either for production or throwaway prototypes/mocks/etc. Contains essential design guidelines, colors, type, fonts, assets, and UI kit components for prototyping.
user-invocable: true
---

Read the README.md file within this skill, and explore the other available files.

If creating visual artifacts (slides, mocks, throwaway prototypes, etc), copy assets out and
create static HTML files for the user to view. If working on production code, you can copy
assets and read the rules here to become an expert in designing with this brand.

If the user invokes this skill without any other guidance, ask them what they want to build
or design, ask some questions, and act as an expert designer who outputs HTML artifacts _or_
production code, depending on the need.

## Fast orientation
- **What tatitok is:** a local-first dashboard that tracks AI token usage and cost across
  coding harnesses, API providers, and local models — and surfaces the API-equivalent
  *value extracted* from flat-rate plans versus what you actually pay.
- **The look:** calm, dense, dark-first. Flat surfaces, 0.5px hairline borders, soft radii,
  no shadows/gradients/glow. Jost, two weights. Sentence case. One number format
  (1.18B, $908.49, 4.5×). Color encodes the *economic class* of usage only:
  violet = subscription, amber = metered API, sky blue = local, green = free. The positive
  `value extracted` number is the emotional center, in success green.

## Files
- `readme.md` — the full design guide: content fundamentals, visual foundations,
  iconography, and an index of everything. **Read this first.**
- `styles.css` — link this one file; it `@import`s all tokens + fonts.
- `tokens/` — colors, typography, spacing, fonts, base reset (CSS custom properties).
- `fonts/` — self-hosted Jost (variable woff2).
- `assets/` — brand mark SVG, favicon, original logo lockup.
- `components/` — React primitives (Button, IconButton, Select, Switch, Badge, ClassDot,
  MeterBar, Card, Stat, Tabs, FilterChip). Each has a `.d.ts` contract and `.prompt.md`.
- `ui_kits/dashboard/` — a full click-through recreation of the dashboard; copy it as a
  starting point for new screens.
- `guidelines/` — foundation specimen cards (open any in a browser).

## Working rules
- Link `styles.css` and use the CSS custom properties — never hard-code hex values that a
  token already covers.
- Sentence case everywhere. Two font weights only (400/500). Keep the one number format.
- Use class hues only to mean the economic class; keep everything else neutral gray.
- Charts are ECharts — prefer the SVG renderer with `animation: false` for crisp,
  capturable output.
- Icons: the brand ships none; this system standardizes on Lucide (a flagged substitution).
