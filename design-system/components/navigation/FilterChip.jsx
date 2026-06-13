import React from "react";

const CSS = `
.tt-chip{
  display:inline-flex; align-items:center; gap:6px; height:24px; padding:0 6px 0 9px;
  font-family:var(--font-sans); font-size:13px; font-weight:var(--weight-regular);
  color:var(--text-primary); background:var(--accent-soft);
  border:0.5px solid var(--accent-border); border-radius:var(--radius-pill);
  cursor:pointer; white-space:nowrap;
  transition:background var(--dur-fast) var(--ease-out);
}
.tt-chip:hover{ background:color-mix(in oklab, var(--accent) 26%, transparent); }
.tt-chip:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-chip__dim{ font-size:12px; color:var(--accent); }
.tt-chip__x{ display:inline-flex; color:var(--text-tertiary); font-size:13px; }
.tt-chip:hover .tt-chip__x{ color:var(--text-primary); }
`;

if (typeof document !== "undefined" && !document.getElementById("tt-chip-css")) {
  const el = document.createElement("style");
  el.id = "tt-chip-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * FilterChip — a removable active-facet chip ("provider: anthropic ×"). Click
 * removes the filter. Uses the neutral brand-green accent, not a class hue.
 */
export function FilterChip({ dim, value, onRemove, className = "", ...rest }) {
  return (
    <button className={`tt-chip ${className}`} onClick={onRemove} title={dim ? `remove ${dim} filter` : "remove filter"} {...rest}>
      {dim && <span className="tt-chip__dim">{dim}:</span>}
      <span>{value}</span>
      <span className="tt-chip__x" aria-hidden>×</span>
    </button>
  );
}
