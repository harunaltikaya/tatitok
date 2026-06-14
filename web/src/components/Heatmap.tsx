import { Fragment } from "react";
import { compactTokens, type ActivityBucket } from "../api";
import { activityGrid } from "../aggregate";

// Activity heatmap (M8 1L): weekday rows × hour columns, GitHub-style intensity
// for "when you're active." Intensity defaults to event count (frequency); the
// endpoint also returns tokens, so a metric toggle is a cheap later add. The
// ramp is ONE neutral hue (empty = base surface, rising via a lightness mix) —
// deliberately not a class/brand/danger colour, so "colour = class" stays
// intact; flat DS, each cell a discrete flat step (no gradients, no shadows).

const WD_LABELS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
// Go's time.Weekday is Sunday=0; display Monday-first for readability.
const WD_ORDER = [1, 2, 3, 4, 5, 6, 0];
const HOUR_LABELS = new Set([0, 6, 12, 18]);

export default function Heatmap({ buckets }: { buckets: ActivityBucket[] }) {
  const grid = activityGrid(buckets, (b) => b.events);
  const max = Math.max(1, ...grid.flat());
  return (
    <div style={{ display: "grid", gridTemplateColumns: "2.2rem repeat(24, 1fr)", gap: 2, alignItems: "center" }}>
      {/* hour-label header row (sparse, to avoid clutter) */}
      <div />
      {Array.from({ length: 24 }, (_, h) => (
        <div key={`hl-${h}`} className="text-center text-[10px] tabular-nums text-faint">
          {HOUR_LABELS.has(h) ? h : ""}
        </div>
      ))}
      {/* weekday rows */}
      {WD_ORDER.map((wd, i) => (
        <Fragment key={wd}>
          <div className="pr-1 text-right text-[11px] text-tertiary">{WD_LABELS[i]}</div>
          {Array.from({ length: 24 }, (_, h) => {
            const v = grid[wd][h];
            // A nonzero cell shows at least 12% so a quiet hour is still visible.
            const pct = v === 0 ? 0 : Math.max(12, Math.round((v / max) * 100));
            return (
              <div
                key={`${wd}-${h}`}
                title={`${WD_LABELS[i]} ${String(h).padStart(2, "0")}:00 · ${compactTokens(v)} events`}
                style={{
                  aspectRatio: "1",
                  borderRadius: 2,
                  background:
                    v === 0 ? "var(--surface-inset)" : `color-mix(in oklab, var(--zinc-300) ${pct}%, var(--surface-inset))`,
                }}
              />
            );
          })}
        </Fragment>
      ))}
    </div>
  );
}
