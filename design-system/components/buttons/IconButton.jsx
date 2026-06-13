import React from "react";

const CSS = `
.tt-iconbtn{
  display:inline-flex; align-items:center; justify-content:center;
  border-radius:var(--radius-sm); border:0.5px solid transparent;
  background:transparent; color:var(--text-tertiary); cursor:pointer;
  transition:background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.tt-iconbtn:hover{ background:var(--surface-hover); color:var(--text-primary); }
.tt-iconbtn:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-iconbtn--sm{ width:26px; height:26px; font-size:14px; }
.tt-iconbtn--md{ width:32px; height:32px; font-size:16px; }
.tt-iconbtn.is-active{ background:var(--accent-soft); color:var(--accent); }
.tt-iconbtn:disabled{ opacity:.4; cursor:not-allowed; }
`;

if (typeof document !== "undefined" && !document.getElementById("tt-iconbtn-css")) {
  const el = document.createElement("style");
  el.id = "tt-iconbtn-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * IconButton — square, quiet control for panel chrome (resize, fullscreen,
 * close) and toolbar glyphs. Pass a Unicode glyph or an SVG icon as children.
 */
export function IconButton({
  size = "md",
  active = false,
  label,
  className = "",
  children,
  ...rest
}) {
  const cls = [
    "tt-iconbtn",
    `tt-iconbtn--${size}`,
    active ? "is-active" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button className={cls} aria-label={label} title={label} {...rest}>
      {children}
    </button>
  );
}
