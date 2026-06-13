import React from "react";

/**
 * MeterBar — a thin horizontal progress meter for plan-window usage against a
 * cap. Auto-escalates to amber near the cap and red over it; otherwise renders
 * in the positive green. Flat track, no glow.
 */
export function MeterBar({ value, max, tone, height = 6, showLabel = false, format, style }) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  const auto = pct >= 100 ? "danger" : pct >= 90 ? "warning" : "positive";
  const t = tone || auto;
  const fill = {
    positive: "var(--color-positive)",
    warning: "var(--color-warning)",
    danger: "var(--color-danger)",
    subscription: "var(--class-subscription)",
    metered: "var(--class-metered)",
    local: "var(--class-local)",
    free: "var(--class-free)",
  }[t] || "var(--color-positive)";
  const fmt = format || ((v) => String(v));
  return (
    <div style={style}>
      {showLabel && (
        <div
          style={{
            display: "flex",
            justifyContent: "space-between",
            fontSize: "var(--text-xs)",
            color: "var(--text-tertiary)",
            marginBottom: 6,
            fontVariantNumeric: "tabular-nums",
          }}
        >
          <span>{fmt(value)}</span>
          <span>{fmt(max)}</span>
        </div>
      )}
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
