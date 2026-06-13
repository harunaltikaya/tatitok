import React from "react";

const CSS = `
.tt-switch{ display:inline-flex; align-items:center; gap:9px; cursor:pointer; user-select:none; }
.tt-switch input{ position:absolute; opacity:0; width:0; height:0; }
.tt-switch__track{
  position:relative; width:34px; height:20px; border-radius:var(--radius-pill);
  background:var(--surface-inset); border:0.5px solid var(--border-hairline);
  transition:background var(--dur-base) var(--ease-out), border-color var(--dur-base);
  flex:0 0 auto;
}
.tt-switch__thumb{
  position:absolute; top:2px; left:2px; width:15px; height:15px;
  border-radius:var(--radius-pill); background:var(--zinc-400);
  transition:transform var(--dur-base) var(--ease-out), background var(--dur-base);
}
.tt-switch input:checked + .tt-switch__track{ background:var(--accent); border-color:transparent; }
.tt-switch input:checked + .tt-switch__track .tt-switch__thumb{ transform:translateX(14px); background:var(--text-on-accent); }
.tt-switch input:focus-visible + .tt-switch__track{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-switch__label{ font-size:13px; color:var(--text-secondary); white-space:nowrap; }
`;

if (typeof document !== "undefined" && !document.getElementById("tt-switch-css")) {
  const el = document.createElement("style");
  el.id = "tt-switch-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Switch — a compact toggle for binary view options (include estimates,
 * dark/light). On uses the brand-green track.
 */
export function Switch({ checked = false, onChange, label, "aria-label": ariaLabel, className = "", style }) {
  return (
    <label className={`tt-switch ${className}`} style={style}>
      <input type="checkbox" checked={checked} onChange={onChange} aria-label={ariaLabel || (typeof label === "string" ? label : undefined)} />
      <span className="tt-switch__track">
        <span className="tt-switch__thumb" />
      </span>
      {label && <span className="tt-switch__label">{label}</span>}
    </label>
  );
}
