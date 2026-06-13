import React from "react";

const CSS = `
.tt-btn{
  display:inline-flex; align-items:center; justify-content:center; gap:8px;
  font-family:var(--font-sans); font-weight:var(--weight-medium); line-height:1;
  border-radius:var(--radius-md); border:0.5px solid transparent;
  cursor:pointer; white-space:nowrap; user-select:none;
  font-variant-numeric:tabular-nums;
  transition:background var(--dur-fast) var(--ease-out),
             border-color var(--dur-fast) var(--ease-out),
             color var(--dur-fast) var(--ease-out);
}
.tt-btn:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-btn--sm{ height:28px; padding:0 10px; font-size:13px; }
.tt-btn--md{ height:34px; padding:0 14px; font-size:14px; }
.tt-btn--default{ background:var(--surface-card); border-color:var(--border-hairline); color:var(--text-primary); }
.tt-btn--default:hover{ background:var(--surface-raised); border-color:var(--border-strong); }
.tt-btn--subtle{ background:transparent; color:var(--text-secondary); }
.tt-btn--subtle:hover{ background:var(--surface-hover); color:var(--text-primary); }
.tt-btn--primary{ background:var(--accent); color:var(--text-on-accent); }
.tt-btn--primary:hover{ filter:brightness(1.06); }
.tt-btn.is-active{ background:var(--accent-soft); border-color:var(--accent-border); color:var(--accent); }
.tt-btn:disabled{ opacity:.45; cursor:not-allowed; }
`;

if (typeof document !== "undefined" && !document.getElementById("tt-btn-css")) {
  const el = document.createElement("style");
  el.id = "tt-btn-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Button — the system's text button. Sentence case labels only.
 */
export function Button({
  variant = "default",
  size = "md",
  active = false,
  className = "",
  children,
  ...rest
}) {
  const cls = [
    "tt-btn",
    `tt-btn--${size}`,
    `tt-btn--${variant}`,
    active ? "is-active" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button className={cls} {...rest}>
      {children}
    </button>
  );
}
