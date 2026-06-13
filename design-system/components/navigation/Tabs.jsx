import React from "react";

const CSS = `
.tt-tabs{ display:inline-flex; align-items:center; gap:2px; }
.tt-tab{
  display:inline-flex; align-items:center; gap:7px; height:32px; padding:0 12px;
  font-family:var(--font-sans); font-size:14px; font-weight:var(--weight-medium);
  color:var(--text-tertiary); background:transparent; border:none; cursor:pointer;
  border-radius:var(--radius-md); line-height:1;
  transition:background var(--dur-fast) var(--ease-out), color var(--dur-fast);
}
.tt-tab:hover{ color:var(--text-primary); background:var(--surface-hover); }
.tt-tab.is-active{ color:var(--text-primary); background:var(--surface-active); }
.tt-tab:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
`;

if (typeof document !== "undefined" && !document.getElementById("tt-tabs-css")) {
  const el = document.createElement("style");
  el.id = "tt-tabs-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Tabs — quiet top-level page switcher (overview / explore / live /
 * collectors). Sentence case labels; the active tab uses a faint surface wash,
 * not an underline or accent bar.
 */
export function Tabs({ tabs = [], value, onChange, className = "", style }) {
  return (
    <div className={`tt-tabs ${className}`} role="tablist" style={style}>
      {tabs.map((t) => {
        const tab = typeof t === "string" ? { value: t, label: t } : t;
        const active = tab.value === value;
        return (
          <button
            key={tab.value}
            role="tab"
            aria-selected={active}
            className={`tt-tab ${active ? "is-active" : ""}`}
            onClick={() => onChange && onChange(tab.value)}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}
