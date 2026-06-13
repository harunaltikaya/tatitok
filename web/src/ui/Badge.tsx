import type { HTMLAttributes, ReactNode } from "react";

// Badge — small pill. Encodes accuracy class (exact / derived / estimated),
// cost-basis tags (`tag`), or status (positive / danger). Sentence-case
// content. Faithful port of the DS Badge.

const CSS = `
.tt-badge{
  display:inline-flex; align-items:center; gap:5px;
  height:20px; padding:0 8px; border-radius:var(--radius-pill);
  font-family:var(--font-sans); font-size:11px; font-weight:var(--weight-medium);
  line-height:1; white-space:nowrap; border:0.5px solid transparent;
  font-variant-numeric:tabular-nums;
}
.tt-badge--neutral{ background:var(--surface-inset); border-color:var(--border-hairline); color:var(--text-secondary); }
.tt-badge--exact{ background:var(--class-free-soft); border-color:transparent; color:var(--class-free); }
.tt-badge--derived{ background:var(--class-subscription-soft); border-color:transparent; color:var(--class-subscription); }
.tt-badge--estimated{ background:var(--class-metered-soft); border-color:transparent; color:var(--class-metered); }
.tt-badge--positive{ background:color-mix(in oklab, var(--color-positive) 14%, transparent); color:var(--color-positive); }
.tt-badge--danger{ background:color-mix(in oklab, var(--color-danger) 14%, transparent); color:var(--color-danger); }
.tt-badge--tag{ background:var(--surface-inset); color:var(--text-tertiary); font-variant-numeric:tabular-nums; border-radius:var(--radius-xs); height:18px; padding:0 6px; font-weight:var(--weight-regular); }
`;

if (typeof document !== "undefined" && !document.getElementById("tt-badge-css")) {
  const el = document.createElement("style");
  el.id = "tt-badge-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

export type BadgeTone = "neutral" | "exact" | "derived" | "estimated" | "positive" | "danger" | "tag";

export default function Badge({
  tone = "neutral",
  dot = false,
  className = "",
  children,
  ...rest
}: {
  tone?: BadgeTone;
  dot?: boolean;
  className?: string;
  children?: ReactNode;
} & HTMLAttributes<HTMLSpanElement>) {
  const cls = ["tt-badge", `tt-badge--${tone}`, className].filter(Boolean).join(" ");
  const dotColor = (
    { exact: "var(--class-free)", derived: "var(--class-subscription)", estimated: "var(--class-metered)" } as Record<
      string,
      string
    >
  )[tone];
  return (
    <span className={cls} {...rest}>
      {dot && dotColor && (
        <span style={{ width: 5, height: 5, borderRadius: "999px", background: dotColor }} />
      )}
      {children}
    </span>
  );
}
