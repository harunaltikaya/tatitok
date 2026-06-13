/* tatitok dashboard UI kit — screens (window.TTScreens). */
const { useState, useRef, useEffect } = React;
const { Card: C2, Stat: S2, Badge: B2, ClassDot: CD2, Select: Sel2, Tabs: T2 } = window.TatitokDesignSystem_b39a28;
const { EChart, ClassLegend, FacetRail: Rail, Breakdown, PlanCard } = window.TTParts;
const TT = window.TT;
const TTC = window.TTCharts;

// ---- Overview -------------------------------------------------------------
function Overview({ filters, onToggle }) {
  const t = TT.totals;
  const [donut] = useState(() => TTC.donutByClass());
  const [equivChart] = useState(() => TTC.dailyStacked("equiv"));
  const [tokenChart] = useState(() => TTC.dailyStacked("tokens"));
  const modelFilter = filters.model || [];
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
      {/* stat strip */}
      <div style={{ display: "grid", gridTemplateColumns: "1.35fr 1fr 1.75fr", gap: "var(--space-4)" }}>
        <C2 title="value extracted">
          <S2 value={TT.usd(t.valueExtracted)} positive size="hero"
            sub={`${t.ratio}× your ${TT.usd(t.monthlyPlanOutlay)}/mo in plans`} />
          <div style={{ marginTop: 10, fontSize: 12, color: "var(--text-faint)" }}>
            API-equivalent value of plan-included usage
          </div>
        </C2>
        <C2 title={<span style={{ display: "inline-flex", alignItems: "center", gap: 7 }}>today <CD2 tone="live" size={8} pulse /></span>}>
          <S2 value={TT.usd(t.todayActual)} size="lg" unpriced unpricedTitle={`${t.todayUnpriced} events today carry no resolvable price — cost is a floor.`}
            sub={`${TT.compactTokens(t.todayTokens)} tokens`} />
          <div style={{ marginTop: 8, fontSize: 12, color: "var(--text-tertiary)", fontVariantNumeric: "tabular-nums" }}>≈ {TT.usd(t.todayEquiv)} API-equiv</div>
        </C2>
        <C2 title="range totals · last 30 days">
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr 1fr", gap: "var(--space-4) var(--space-5)", marginTop: 2 }}>
            <S2 value={TT.usd(t.rangeEquiv)} sub="API-equivalent" />
            <S2 value={TT.usd(t.rangeActual)} sub="actual cost" />
            <S2 value={TT.compactTokens(t.rangeTokens)} sub="tokens" />
            <S2 value={String(t.activeDays)} sub="active days" />
          </div>
        </C2>
      </div>

      {/* plan windows */}
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "var(--space-4)" }}>
        {TT.plans.map((p) => <PlanCard key={p.name} plan={p} />)}
      </div>

      {/* charts row 1 */}
      <div style={{ display: "grid", gridTemplateColumns: "1.75fr 1fr", gap: "var(--space-4)" }}>
        <C2 title="daily API-equivalent value" actions={<ClassLegend active={null} />}>
          <EChart option={equivChart} height={236} />
        </C2>
        <C2 title="value by class">
          <div style={{ position: "relative" }}>
            <EChart option={donut} height={188} />
            <div style={{ position: "absolute", inset: 0, display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center", pointerEvents: "none" }}>
              <div style={{ fontSize: 24, fontWeight: 400, color: "var(--text-primary)", letterSpacing: "-0.02em", fontVariantNumeric: "tabular-nums" }}>{TT.usd(t.rangeEquiv)}</div>
              <div style={{ fontSize: 11, color: "var(--text-tertiary)" }}>total value</div>
            </div>
          </div>
          <div style={{ marginTop: 12 }}><ClassLegend active={null} /></div>
        </C2>
      </div>

      {/* charts row 2 */}
      <div style={{ display: "grid", gridTemplateColumns: "1.75fr 1fr", gap: "var(--space-4)" }}>
        <C2 title="usage by model" actions={<span style={{ fontSize: 12, color: "var(--text-faint)" }}>click a row to filter</span>}>
          <Breakdown rows={TT.models} active={modelFilter} onSelect={(k) => onToggle("model", k)} />
        </C2>
        <C2 title="daily tokens">
          <EChart option={tokenChart} height={236} />
        </C2>
      </div>
    </div>
  );
}

// ---- Explore --------------------------------------------------------------
function Explore({ filters, onToggle }) {
  const [by, setBy] = useState("model");
  const [heat] = useState(() => TTC.activityHeatmap());
  const [tree] = useState(() => TTC.valueTreemap());
  const dimRows = {
    model: TT.models,
    provider: TT.providers.map((p) => ({ key: p.key, cls: p.cls, basis: p.cls === "subscription" ? "plan_included" : p.cls === "metered" ? "api_price" : "local_energy", tokens: p.events * 9000, equiv: p.events * 0.012, actual: p.cls === "metered" ? p.events * 0.011 : p.events * 0.0002 })),
    harness: TT.harnesses.map((h) => ({ key: h.key, cls: h.cls, basis: h.cls === "subscription" ? "plan_included" : h.cls === "metered" ? "api_price" : "local_energy", tokens: h.events * 9000, equiv: h.events * 0.013, actual: h.cls === "metered" ? h.events * 0.012 : h.events * 0.0002 })),
  };
  const rows = dimRows[by];
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
        <span style={{ fontSize: 13, color: "var(--text-tertiary)" }}>primary dimension</span>
        <Sel2 value={by} onChange={(e) => setBy(e.target.value)} options={["model", "provider", "harness"]} aria-label="group by" />
        <span style={{ marginLeft: "auto", fontSize: 12, color: "var(--text-faint)" }}>export current slice · CSV / JSON</span>
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1.4fr 1fr", gap: "var(--space-4)" }}>
        <C2 title={`by ${by}`}>
          <Breakdown rows={rows} active={filters[by] || []} onSelect={(k) => onToggle(by, k)} />
        </C2>
        <C2 title="when you work · hour × weekday">
          <EChart option={heat} height={232} />
        </C2>
      </div>
      <C2 title="where the value concentrates · API-equivalent by model">
        <EChart option={tree} height={240} />
      </C2>
    </div>
  );
}

// ---- Live -----------------------------------------------------------------
const SAMPLE_EVENTS = [
  { harness: "claude-code", model: "claude-fable-5", cls: "subscription", tok: 18420, cost: 0 },
  { harness: "codex", model: "gpt-5-codex", cls: "metered", tok: 9260, cost: 0.41 },
  { harness: "vllm-proxy", model: "qwen-3.5-35b-a3b", cls: "local", tok: 24110, cost: 0.002 },
  { harness: "opencode", model: "deepseek-v3.2", cls: "metered", tok: 6180, cost: 0.18 },
  { harness: "claude-code", model: "claude-haiku-4.5", cls: "subscription", tok: 4120, cost: 0 },
  { harness: "web-claude", model: "claude-fable-5", cls: "subscription", tok: 7740, cost: 0 },
];
function Live() {
  const [feed, setFeed] = useState(() =>
    Array.from({ length: 7 }, (_, i) => ({ ...SAMPLE_EVENTS[i % SAMPLE_EVENTS.length], id: i, t: new Date(Date.now() - i * 4200) })));
  const idRef = useRef(100);
  useEffect(() => {
    const iv = setInterval(() => {
      const e = SAMPLE_EVENTS[Math.floor(Math.random() * SAMPLE_EVENTS.length)];
      setFeed((f) => [{ ...e, id: idRef.current++, t: new Date() }, ...f].slice(0, 12));
    }, 2600);
    return () => clearInterval(iv);
  }, []);
  const hhmmss = (d) => d.toTimeString().slice(0, 8);
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: "var(--space-4)" }}>
        <C2 title={<span style={{ display: "inline-flex", alignItems: "center", gap: 7 }}>burn rate <CD2 tone="live" size={8} pulse /></span>}>
          <S2 value="1.42M" sub="tokens / min · 5-min EMA" size="lg" />
        </C2>
        <C2 title="spend rate">
          <S2 value="$3.18" sub="per hour · actual" size="lg" />
        </C2>
        <C2 title="plan-window projection">
          <S2 value="17:42" sub="Claude Max limit at this rate" size="lg" />
        </C2>
      </div>
      <C2 title="live event stream" actions={<span style={{ fontSize: 12, color: "var(--text-faint)" }}>last {feed.length} events</span>}>
        <div style={{ display: "flex", flexDirection: "column" }}>
          {feed.map((e) => (
            <div key={e.id} style={{ display: "grid", gridTemplateColumns: "84px 1fr 1fr 92px 78px", alignItems: "center", gap: 10,
              padding: "8px 0", borderTop: "0.5px solid var(--border-hairline)", fontSize: 13, fontVariantNumeric: "tabular-nums" }}>
              <span className="mono" style={{ color: "var(--text-faint)", fontSize: 12 }}>{hhmmss(e.t)}</span>
              <span style={{ display: "inline-flex", alignItems: "center", gap: 7, color: "var(--text-secondary)" }}><CD2 tone={e.cls} size={7} />{e.harness}</span>
              <span className="mono" style={{ color: "var(--text-tertiary)", fontSize: 12 }}>{e.model}</span>
              <span style={{ textAlign: "right", color: "var(--text-secondary)" }}>{TT.compactTokens(e.tok)}</span>
              <span style={{ textAlign: "right", color: e.cost === 0 ? "var(--text-faint)" : "var(--text-primary)" }}>{e.cost === 0 ? "plan" : TT.usd(e.cost)}</span>
            </div>
          ))}
        </div>
      </C2>
    </div>
  );
}

window.TTScreens = { Overview, Explore, Live };
