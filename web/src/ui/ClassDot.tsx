import type { CSSProperties } from "react";

// ClassDot — the small filled circle that encodes the economic class of
// usage (subscription / metered / local / free), liveness, or status. The
// design system's most important "icon": color carries the meaning, not a
// glyph. Faithful port of the DS ClassDot.

export type DotTone =
  | "subscription"
  | "metered"
  | "local"
  | "free"
  | "positive"
  | "neutral"
  | "live"
  | "danger";

const TONE: Record<DotTone, string> = {
  subscription: "var(--class-subscription)",
  metered: "var(--class-metered)",
  local: "var(--class-local)",
  free: "var(--class-free)",
  positive: "var(--color-positive)",
  neutral: "var(--zinc-500)",
  live: "var(--color-live)",
  danger: "var(--color-danger)",
};

// The breathing pulse (the live SSE dot). prefers-reduced-motion is honored
// globally in index.css, which neutralizes this animation's duration.
if (typeof document !== "undefined" && !document.getElementById("tt-dot-css")) {
  const el = document.createElement("style");
  el.id = "tt-dot-css";
  el.textContent =
    "@keyframes tt-dot-pulse{0%{box-shadow:0 0 0 0 var(--color-live)}70%{box-shadow:0 0 0 5px transparent}100%{box-shadow:0 0 0 0 transparent}}";
  document.head.appendChild(el);
}

export default function ClassDot({
  tone = "neutral",
  size = 8,
  pulse = false,
  title,
  className = "",
  style,
}: {
  tone?: DotTone;
  size?: number;
  pulse?: boolean;
  title?: string;
  className?: string;
  style?: CSSProperties;
}) {
  return (
    <span
      title={title}
      className={className}
      style={{
        display: "inline-block",
        width: size,
        height: size,
        borderRadius: "999px",
        background: TONE[tone],
        flex: "0 0 auto",
        animation: pulse ? "tt-dot-pulse 1.8s var(--ease-out) infinite" : undefined,
        ...style,
      }}
    />
  );
}
