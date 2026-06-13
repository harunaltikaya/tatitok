import type { CSSProperties } from "react";

// MeterBar — a thin horizontal progress meter for plan-window usage against
// a cap. Auto-escalates to amber near the cap and red over it; otherwise
// the positive green. Flat track (--surface-inset), no glow. Faithful port
// of the DS MeterBar.

type MeterTone = "positive" | "warning" | "danger" | "subscription" | "metered" | "local" | "free";

const FILL: Record<MeterTone, string> = {
  positive: "var(--color-positive)",
  warning: "var(--color-warning)",
  danger: "var(--color-danger)",
  subscription: "var(--class-subscription)",
  metered: "var(--class-metered)",
  local: "var(--class-local)",
  free: "var(--class-free)",
};

export default function MeterBar({
  value,
  max,
  tone,
  height = 6,
  style,
}: {
  value: number;
  max: number;
  tone?: MeterTone;
  height?: number;
  style?: CSSProperties;
}) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  const auto: MeterTone = pct >= 100 ? "danger" : pct >= 90 ? "warning" : "positive";
  const fill = FILL[tone ?? auto];
  return (
    <div style={style}>
      <div
        style={{
          height,
          borderRadius: "var(--radius-pill)",
          background: "var(--surface-inset)",
          overflow: "hidden",
        }}
      >
        <div
          style={{
            height: "100%",
            width: `${pct}%`,
            background: fill,
            borderRadius: "var(--radius-pill)",
            transition: "width var(--dur-slow) var(--ease-out)",
          }}
        />
      </div>
    </div>
  );
}
