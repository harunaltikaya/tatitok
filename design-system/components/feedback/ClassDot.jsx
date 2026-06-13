import React from "react";

const CLASS_VARS = {
  subscription: "var(--class-subscription)",
  metered: "var(--class-metered)",
  local: "var(--class-local)",
  free: "var(--class-free)",
  positive: "var(--color-positive)",
  neutral: "var(--zinc-500)",
  live: "var(--color-live)",
  danger: "var(--color-danger)",
};

/**
 * ClassDot — the small filled circle that encodes the economic class of usage
 * (subscription / metered / local / free), liveness, or status. The system's
 * most important "icon": color carries the meaning, not a glyph.
 */
export function ClassDot({ tone = "neutral", size = 8, pulse = false, style, ...rest }) {
  const color = CLASS_VARS[tone] || tone;
  return (
    <span
      style={{
        display: "inline-block",
        width: size,
        height: size,
        borderRadius: "var(--radius-pill)",
        background: color,
        flex: "0 0 auto",
        boxShadow: pulse ? `0 0 0 0 ${color}` : undefined,
        animation: pulse ? "tt-dot-pulse 1.8s var(--ease-out) infinite" : undefined,
        ...style,
      }}
      {...rest}
    />
  );
}

if (typeof document !== "undefined" && !document.getElementById("tt-dot-css")) {
  const el = document.createElement("style");
  el.id = "tt-dot-css";
  el.textContent =
    "@keyframes tt-dot-pulse{0%{box-shadow:0 0 0 0 var(--color-live)}70%{box-shadow:0 0 0 5px transparent}100%{box-shadow:0 0 0 0 transparent}}";
  document.head.appendChild(el);
}
