import { Fragment } from "react";
import { compactTokens, type ActivityBucket } from "../api";
import { activityGrid } from "../aggregate";

// Activity heatmap (M8 1L → mockup-matched): weekday rows × hour columns,
// GitHub-contribution-style intensity for "when you're active." Intensity is
// TOKEN VOLUME (the mockup's "tokens · hour × weekday"); the endpoint also
// returns event counts, so a metric toggle is a cheap later add. Colour: a zero
// cell is pale white, any usage a clear GREEN shade (light → dark forest) —
// green reads as activity, white strictly as "no consumption" (its own legended
// panel, so green here doesn't break "colour = class" elsewhere). Flat DS: each
// cell a discrete flat step — no gradients, no shadows.

const WD_LABELS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
// Go's time.Weekday is Sunday=0; display Monday-first for readability.
const WD_ORDER = [1, 2, 3, 4, 5, 6, 0];
const HOUR_LABELS = new Set([0, 6, 12, 18, 23]);
// A zero cell is PALE WHITE; ANY usage is a clear GREEN shade. The green ramp
// is green-to-green (light green floor → dark forest peak), NEVER mixed over
// white, so low/mid usage stays obviously green instead of washing out; white
// is reserved strictly for zero. sqrt scaling spreads light→dark across the
// range, so a quiet hour still reads clearly green and busy hours go dark.
const PALE_WHITE = "var(--zinc-100)";
const LIGHT_GREEN = "#86efac"; // floor: an obvious light green (not a white-green)
const DARK_GREEN = "#166534"; // peak: saturated dark forest green
const greenShade = (t: number) => `color-mix(in oklab, ${DARK_GREEN} ${Math.round(t * 100)}%, ${LIGHT_GREEN})`;
const cellColor = (v: number, max: number) => (v <= 0 ? PALE_WHITE : greenShade(Math.sqrt(v / max)));
// less (pale white, empty) → light → dark green (more) — mirrors the cells.
const LEGEND = [PALE_WHITE, greenShade(0), greenShade(0.34), greenShade(0.67), greenShade(1)];

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
              return (
                <div
                  key={`${wd}-${h}`}
                  title={`${WD_LABELS[i]} ${String(h).padStart(2, "0")}:00 · ${compactTokens(v)} tokens`}
                  style={{ aspectRatio: "1", borderRadius: 2, background: cellColor(v, max) }}
                />
              );
            })}
          </Fragment>
        ))}
      </div>
      {/* less → more legend (GitHub-style), the same green ramp the cells use */}
      <div className="mt-2 flex items-center justify-end gap-1 text-[10px] text-faint">
        <span>less</span>
        {LEGEND.map((c, i) => (
          <span key={i} aria-hidden="true" style={{ display: "inline-block", width: 11, height: 11, borderRadius: 2, background: c }} />
        ))}
        <span>more</span>
      </div>
    </div>
  );
}
