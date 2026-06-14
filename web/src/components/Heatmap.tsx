import { Fragment } from "react";
import { compactTokens, type ActivityBucket } from "../api";
import { activityGrid } from "../aggregate";

// Activity heatmap (M8 1L → mockup-matched at 1M): weekday rows × hour columns,
// GitHub-style intensity for "when you're active." Intensity is TOKEN VOLUME
// (the mockup's "tokens · hour × weekday"); the endpoint also returns event
// counts, so a metric toggle is a cheap later add. The ramp is the DS
// value/positive green (empty = base surface, rising via a lightness mix in
// oklab) — the heatmap is its own legended panel, so green reads as activity
// intensity and ties to the value-green theme (this overrides 1L's neutral
// ramp). Flat DS: each cell a discrete flat step — no gradients, no shadows.

const WD_LABELS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
// Go's time.Weekday is Sunday=0; display Monday-first for readability.
const WD_ORDER = [1, 2, 3, 4, 5, 6, 0];
const HOUR_LABELS = new Set([0, 6, 12, 18, 23]);
// ramp(pct): a green lightness mix over a PALE OFF-WHITE base — empty (0%) is a
// solid pale white (no green), climbing pale-white → light green → full
// value-green (#34d399) as consumption grows. zinc-100 is opaque, so a
// zero-consumption cell reads as a visible pale slot, not the dark panel.
const ramp = (pct: number) => `color-mix(in oklab, var(--color-positive) ${pct}%, var(--zinc-100))`;
// less (pale white, empty) → more (green) — mirrors the cell ramp.
const LEGEND_STEPS = [0, 25, 50, 75, 100];

export default function Heatmap({ buckets }: { buckets: ActivityBucket[] }) {
  const grid = activityGrid(buckets, (b) => b.tokens);
  const max = Math.max(1, ...grid.flat());
  return (
    <div>
      <div style={{ display: "grid", gridTemplateColumns: "2.2rem repeat(24, 1fr)", gap: 2, alignItems: "center" }}>
        {/* hour-label header row (sparse, to avoid clutter) */}
        <div />
        {Array.from({ length: 24 }, (_, h) => (
          <div key={`hl-${h}`} className="text-center text-[10px] tabular-nums text-faint">
            {HOUR_LABELS.has(h) ? `${h}:00` : ""}
          </div>
        ))}
        {/* weekday rows */}
        {WD_ORDER.map((wd, i) => (
          <Fragment key={wd}>
            <div className="pr-1 text-right text-[11px] text-tertiary">{WD_LABELS[i]}</div>
            {Array.from({ length: 24 }, (_, h) => {
              const v = grid[wd][h];
              // A nonzero cell shows at least 12% so a quiet hour stays visible.
              const pct = v === 0 ? 0 : Math.max(12, Math.round((v / max) * 100));
              return (
                <div
                  key={`${wd}-${h}`}
                  title={`${WD_LABELS[i]} ${String(h).padStart(2, "0")}:00 · ${compactTokens(v)} tokens`}
                  style={{ aspectRatio: "1", borderRadius: 2, background: ramp(pct) }}
                />
              );
            })}
          </Fragment>
        ))}
      </div>
      {/* less → more legend (GitHub-style), the same green ramp the cells use */}
      <div className="mt-2 flex items-center justify-end gap-1 text-[10px] text-faint">
        <span>less</span>
        {LEGEND_STEPS.map((p) => (
          <span key={p} aria-hidden="true" style={{ display: "inline-block", width: 11, height: 11, borderRadius: 2, background: ramp(p) }} />
        ))}
        <span>more</span>
      </div>
    </div>
  );
}
