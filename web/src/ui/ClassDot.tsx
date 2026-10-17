import { useEffect, useRef, useState, type CSSProperties } from "react";

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

// The breathing pulse (the live SSE dot): a live-colored disc on the dot's
// ::after grows 5 px past the dot and fades out. Transform and opacity
// only, on its own layer, so Chrome runs it on the compositor; the old
// box-shadow keyframes restyled the page every frame (profile 10-17). The
// component runs it with the Web Animations API, not a CSS animation: a CSS
// animation fires animationiteration, and React's root listener for it
// woke the main thread every cycle. index.css's prefers-reduced-motion rule
// reaches CSS animations only, not this one, so the component checks the
// preference itself: under reduce the pulse never starts and the disc is
// not rendered (no tt-dot-pulse class), leaving the plain dot. Read once
// per mount; a later change of the setting is not followed.
if (typeof document !== "undefined" && !document.getElementById("tt-dot-css")) {
  const el = document.createElement("style");
  el.id = "tt-dot-css";
  el.textContent =
    ".tt-dot-pulse{position:relative}" +
    '.tt-dot-pulse::after{content:"";position:absolute;inset:0;border-radius:inherit;background:var(--color-live);will-change:transform,opacity}';
  document.head.appendChild(el);
}

// var(--ease-out) (tokens/spacing.css); WAAPI easing takes no var().
const EASE_OUT = "cubic-bezier(0.2, 0.7, 0.3, 1)";

const reducedMotion = () =>
  typeof window !== "undefined" &&
  typeof window.matchMedia === "function" &&
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

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
  const ref = useRef<HTMLSpanElement>(null);
  const [still] = useState(reducedMotion);
  const pulsing = pulse && !still;
  useEffect(() => {
    const el = ref.current;
    if (!pulsing || !el) return;
    // The disc's peak scale: the dot plus 5 px on each side. The easing
    // sits on each keyframe, as animation-timing-function did.
    const peak = (size + 10) / size;
    const anim = el.animate(
      [
        { offset: 0, transform: "scale(1)", opacity: 1, easing: EASE_OUT },
        { offset: 0.7, transform: `scale(${peak})`, opacity: 0, easing: EASE_OUT },
        { offset: 1, transform: "scale(1)", opacity: 0 },
      ],
      { duration: 1800, iterations: Infinity, pseudoElement: "::after" },
    );
    return () => anim.cancel();
  }, [pulsing, size]);
  return (
    <span
      ref={ref}
      title={title}
      className={pulsing ? `tt-dot-pulse ${className}` : className}
      style={{
        display: "inline-block",
        width: size,
        height: size,
        borderRadius: "999px",
        background: TONE[tone],
        flex: "0 0 auto",
        ...style,
      }}
    />
  );
}
