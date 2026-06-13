/* tatitok dashboard UI kit — shared parts (window.TTParts).
   Composes the design-system primitives; never re-implements them. */
const { useEffect, useRef, useState } = React;
const DS = window.TatitokDesignSystem_b39a28;
const { Button, IconButton, Card, Stat, Badge, ClassDot, MeterBar, Select, Switch, Tabs, FilterChip } = DS;
const { usd, compactTokens, CLASS, classKeys } = window.TT;

// ---- ECharts wrapper (ResizeObserver-driven init, per the real app) -------
function EChart({ option, height = 240 }) {
  const el = React.useRef(null);
  const chart = React.useRef(null);
  const latest = React.useRef(option);
  latest.current = option;
  React.useEffect(() => {
    const node = el.current;
    if (!node) return;
    let timer = 0, tries = 0;
    const tryInit = () => {
      if (chart.current) return;
      tries++;
      if (node.clientWidth === 0 || node.clientHeight === 0) {
        if (tries < 60) timer = setTimeout(tryInit, 50);
        return;
      }
      chart.current = window.echarts.init(node, null, { renderer: "svg" });
      chart.current.setOption(latest.current, { notMerge: true });
    };
    tryInit();
    const ro = new ResizeObserver(() => {
      if (!chart.current) tryInit();
      else chart.current.resize();
    });
    ro.observe(node);
    return () => { clearTimeout(timer); ro.disconnect(); chart.current && chart.current.dispose(); chart.current = null; };
  }, []);
  React.useEffect(() => { if (chart.current) chart.current.setOption(option, { notMerge: true }); }, [option]);
  return <div ref={el} style={{ height, width: "100%" }} />;
}

// ---- brand mark (inline so currentColor themes) ---------------------------
function Mark({ size = 26 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 120 120" fill="none" aria-hidden="true" style={{ display: "block" }}>
      <g stroke="currentColor" strokeWidth="12" strokeLinecap="round" strokeLinejoin="round">
        <path d="M50 16 H18 V104 H50" /><path d="M70 16 H102 V104 H70" />
      </g>
      <circle cx="60" cy="60" r="14" fill="#1c9d74" />
    </svg>
  );
}

// ---- class legend ---------------------------------------------------------
function ClassLegend({ active, onToggle }) {
  return (
    <div style={{ display: "flex", gap: 16, flexWrap: "wrap" }}>
      {classKeys.map((c) => (
        <button key={c} onClick={() => onToggle && onToggle(c)}
          style={{ display: "inline-flex", alignItems: "center", gap: 7, background: "none", border: "none",
            cursor: onToggle ? "pointer" : "default", padding: 0, fontFamily: "var(--font-sans)", fontSize: 13,
            color: active && !active.includes(c) ? "var(--text-faint)" : "var(--text-secondary)" }}>
          <ClassDot tone={c} /> {CLASS[c].label}
        </button>
      ))}
    </div>
  );
}

// ---- header ---------------------------------------------------------------
const PRESETS = ["7d", "30d", "90d", "all"];
function Header({ range, onRange, tz, onTz, theme, onTheme, live }) {
  return (
    <header style={{ display: "flex", alignItems: "center", gap: "var(--space-4)", flexWrap: "wrap", marginBottom: "var(--space-5)" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 11 }}>
        <span style={{ color: "var(--text-primary)" }}><Mark size={26} /></span>
        <span style={{ fontSize: 21, fontWeight: 500, letterSpacing: "-0.01em", color: "var(--text-primary)" }}>tatitok</span>
        <span style={{ fontSize: 13, color: "var(--text-faint)" }}>local AI usage</span>
      </div>
      <div style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
        {PRESETS.map((p) => (
          <Button key={p} size="sm" active={range === p} onClick={() => onRange(p)}>{p}</Button>
        ))}
        <span style={{ display: "inline-flex", alignItems: "center", gap: 6, marginLeft: 4 }}>
          <span style={{ fontSize: 12, color: "var(--text-tertiary)" }}>days in</span>
          <Select value={tz} onChange={(e) => onTz(e.target.value)}
            options={["UTC", "Europe/Istanbul", "America/New_York", "Asia/Tokyo"]} aria-label="timezone" />
        </span>
        <Switch checked={theme === "light"} onChange={onTheme} aria-label="light mode" />
      </div>
    </header>
  );
}

// ---- facet rail -----------------------------------------------------------
function FacetSection({ dim, values, active, onToggle }) {
  return (
    <Card padding={12} style={{ borderRadius: "var(--radius-md)" }}>
      <div style={{ fontSize: 12, fontWeight: 500, color: "var(--text-tertiary)", marginBottom: 8 }}>{dim}</div>
      <div style={{ display: "flex", flexDirection: "column", gap: 1 }}>
        {values.map((v) => {
          const sel = active.includes(v.key);
          return (
            <button key={v.key} onClick={() => onToggle(dim, v.key)}
              style={{ display: "flex", alignItems: "center", gap: 7, width: "100%", textAlign: "left",
                padding: "3px 7px", borderRadius: "var(--radius-sm)", border: "none", cursor: "pointer",
                fontFamily: "var(--font-sans)", fontSize: 13,
                background: sel ? "var(--accent-soft)" : "transparent",
                color: sel ? "var(--accent)" : "var(--text-secondary)" }}
              onMouseEnter={(e) => { if (!sel) e.currentTarget.style.background = "var(--surface-hover)"; }}
              onMouseLeave={(e) => { if (!sel) e.currentTarget.style.background = "transparent"; }}>
              {v.cls ? <ClassDot tone={v.cls} size={7} /> : <span style={{ width: 7 }} />}
              <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{v.key}</span>
              <span style={{ fontSize: 11, color: "var(--text-faint)", fontVariantNumeric: "tabular-nums" }}>{compactTokens(v.events)}</span>
            </button>
          );
        })}
      </div>
    </Card>
  );
}

function FacetRail({ facets, filters, onToggle }) {
  return (
    <aside style={{ width: "var(--rail-width)", flex: "0 0 auto", display: "flex", flexDirection: "column", gap: "var(--space-3)" }}>
      {Object.keys(facets).map((dim) => (
        <FacetSection key={dim} dim={dim} values={facets[dim]} active={filters[dim] || []} onToggle={onToggle} />
      ))}
    </aside>
  );
}

// ---- breakdown table ------------------------------------------------------
function Breakdown({ rows, active, onSelect }) {
  return (
    <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 14 }}>
      <thead>
        <tr style={{ textAlign: "left", color: "var(--text-tertiary)", fontSize: 12 }}>
          <th style={{ fontWeight: 400, padding: "0 0 8px" }}>key</th>
          <th style={{ fontWeight: 400, textAlign: "right", padding: "0 0 8px" }}>tokens</th>
          <th style={{ fontWeight: 400, textAlign: "right", padding: "0 0 8px" }}>API-equiv</th>
          <th style={{ fontWeight: 400, textAlign: "right", padding: "0 0 8px" }}>actual</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((m) => {
          const sel = active.includes(m.key);
          return (
            <tr key={m.key} onClick={() => onSelect(m.key)}
              style={{ cursor: "pointer", borderTop: "0.5px solid var(--border-hairline)",
                background: sel ? "var(--accent-soft)" : "transparent" }}
              onMouseEnter={(e) => { if (!sel) e.currentTarget.style.background = "var(--surface-hover)"; }}
              onMouseLeave={(e) => { if (!sel) e.currentTarget.style.background = "transparent"; }}>
              <td style={{ padding: "9px 0" }}>
                <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
                  <ClassDot tone={m.cls} size={7} />
                  <span style={{ color: "var(--text-primary)" }}>{m.key}</span>
                  <Badge tone="tag">{m.basis}</Badge>
                </span>
              </td>
              <td style={{ textAlign: "right", color: "var(--text-secondary)", fontVariantNumeric: "tabular-nums" }}>{compactTokens(m.tokens)}</td>
              <td style={{ textAlign: "right", color: "var(--text-primary)", fontVariantNumeric: "tabular-nums" }}>{usd(m.equiv)}</td>
              <td style={{ textAlign: "right", color: m.actual === 0 ? "var(--text-faint)" : "var(--text-secondary)", fontVariantNumeric: "tabular-nums" }}>{m.actual === 0 ? "—" : usd(m.actual)}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

// ---- plan card ------------------------------------------------------------
function PlanCard({ plan }) {
  return (
    <Card title={<span>{plan.name} <span style={{ color: "var(--text-faint)", fontWeight: 400 }}>· {plan.windowLabel} windows</span></span>}>
      {plan.windowEquiv !== null ? (
        <React.Fragment>
          <Stat value={usd(plan.windowEquiv)} sub={`${compactTokens(plan.windowTokens)} tokens · ${plan.windowEvents} events`} size="lg" />
          <div style={{ marginTop: 6, fontSize: 12, color: "var(--text-faint)" }}>resets in {plan.resetsIn}</div>
        </React.Fragment>
      ) : (
        <div style={{ fontSize: 14, color: "var(--text-tertiary)", padding: "6px 0" }}>no active window — the next event opens one</div>
      )}
      {plan.weeklyCap && (
        <div style={{ marginTop: 14 }}>
          <div style={{ display: "flex", justifyContent: "space-between", fontSize: 12, color: "var(--text-tertiary)", marginBottom: 7, fontVariantNumeric: "tabular-nums" }}>
            <span>week</span><span>{usd(plan.weekEquiv)} of {usd(plan.weeklyCap)} cap</span>
          </div>
          <MeterBar value={plan.weekEquiv} max={plan.weeklyCap} />
        </div>
      )}
      <div style={{ marginTop: 14, paddingTop: 12, borderTop: "0.5px solid var(--border-hairline)", fontSize: 14, fontVariantNumeric: "tabular-nums", display: "flex", alignItems: "baseline", gap: 6, flexWrap: "wrap" }}>
        <span style={{ color: "var(--text-positive)", fontWeight: 500 }}>{usd(plan.monthEquiv)}</span>
        <span style={{ color: "var(--text-tertiary)" }}>extracted vs</span>
        <span style={{ color: "var(--text-primary)" }}>{usd(plan.monthlyPrice)}/mo</span>
        <Badge tone="positive">{(plan.monthEquiv / plan.monthlyPrice).toFixed(1)}×</Badge>
      </div>
    </Card>
  );
}

window.TTParts = { EChart, Mark, ClassLegend, Header, FacetRail, Breakdown, PlanCard };
