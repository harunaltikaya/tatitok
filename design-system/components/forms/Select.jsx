import React from "react";

const CSS = `
.tt-select{ position:relative; display:inline-flex; align-items:center; }
.tt-select > select{
  appearance:none; -webkit-appearance:none;
  font-family:var(--font-sans); font-size:13px; font-weight:var(--weight-regular);
  color:var(--text-primary); background:var(--surface-card);
  border:0.5px solid var(--border-hairline); border-radius:var(--radius-md);
  height:30px; padding:0 28px 0 10px; cursor:pointer; line-height:1;
  transition:border-color var(--dur-fast) var(--ease-out), background var(--dur-fast);
}
.tt-select > select:hover{ border-color:var(--border-strong); }
.tt-select > select:focus-visible{ outline:2px solid var(--border-focus); outline-offset:1px; }
.tt-select__chev{
  position:absolute; right:9px; pointer-events:none; color:var(--text-tertiary);
  font-size:10px; line-height:1;
}
`;

if (typeof document !== "undefined" && !document.getElementById("tt-select-css")) {
  const el = document.createElement("style");
  el.id = "tt-select-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Select — a thin wrapper over a native <select> with the system's hairline
 * border and a quiet chevron. Used for the timezone picker and group-by.
 */
export function Select({ value, onChange, options = [], "aria-label": ariaLabel, className = "", style, children }) {
  return (
    <span className={`tt-select ${className}`} style={style}>
      <select value={value} onChange={onChange} aria-label={ariaLabel}>
        {children ||
          options.map((o) => {
            const opt = typeof o === "string" ? { value: o, label: o } : o;
            return (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            );
          })}
      </select>
      <span className="tt-select__chev" aria-hidden>▾</span>
    </span>
  );
}
