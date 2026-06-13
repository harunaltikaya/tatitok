import type { CSSProperties, ReactNode } from "react";

// Stat — a quiet label over a large tabular number, with optional sub-line
// and a trailing honesty marker. The product's primary way of stating a
// figure; `positive` renders the value in the savings green (use it for
// value-extracted). Faithful port of the DS Stat.

export default function Stat({
  label,
  value,
  sub,
  positive = false,
  size = "md",
  unpriced = false,
  unpricedTitle,
  className = "",
  style,
}: {
  label?: ReactNode;
  value: ReactNode;
  sub?: ReactNode;
  positive?: boolean;
  size?: "hero" | "lg" | "md";
  unpriced?: boolean;
  unpricedTitle?: string;
  className?: string;
  style?: CSSProperties;
}) {
  const valSize =
    size === "hero" ? "var(--text-hero)" : size === "lg" ? "var(--text-2xl)" : "var(--text-xl)";
  return (
    <div className={className} style={style}>
      {label && (
        <div
          style={{
            fontSize: "var(--text-xs)",
            fontWeight: "var(--weight-medium)",
            letterSpacing: "var(--tracking-label)",
            color: "var(--text-tertiary)",
            marginBottom: 6,
          }}
        >
          {label}
        </div>
      )}
      <div
        style={{
          fontSize: valSize,
          fontWeight: "var(--weight-regular)",
          lineHeight: "var(--leading-tight)",
          letterSpacing: "var(--tracking-tight)",
          fontVariantNumeric: "tabular-nums",
          color: positive ? "var(--text-positive)" : "var(--text-primary)",
        }}
      >
        {value}
        {unpriced && (
          <span title={unpricedTitle} style={{ color: "var(--color-warning)", cursor: "help", marginLeft: 1 }}>
            *
          </span>
        )}
      </div>
      {sub && (
        <div style={{ marginTop: 4, fontSize: "var(--text-sm)", color: "var(--text-tertiary)", fontVariantNumeric: "tabular-nums" }}>
          {sub}
        </div>
      )}
    </div>
  );
}
