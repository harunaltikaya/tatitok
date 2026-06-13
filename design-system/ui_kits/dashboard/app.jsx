/* tatitok dashboard UI kit — app shell + mount. */
const { useState: useS } = React;
const DSx = window.TatitokDesignSystem_b39a28;
const { Tabs, FilterChip, Button } = DSx;
const { Header } = window.TTParts;
const RailX = window.TTParts.FacetRail;
const { Overview, Explore, Live } = window.TTScreens;
const TTx = window.TT;

const FACET_DIMS = ["harness", "provider", "model", "accuracy"];

function displayDim(d) { return d; }

function App() {
  const [page, setPage] = useS("overview");
  const [range, setRange] = useS("30d");
  const [tz, setTz] = useS("Europe/Istanbul");
  const [theme, setTheme] = useS("dark");
  const [filters, setFilters] = useS({}); // dim -> [values]

  function onTheme(e) {
    const t = e.target.checked ? "light" : "dark";
    setTheme(t);
    document.documentElement.setAttribute("data-theme", t === "light" ? "light" : "");
  }
  function toggle(dim, value) {
    setFilters((f) => {
      const cur = f[dim] || [];
      const next = cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value];
      const out = { ...f, [dim]: next };
      if (next.length === 0) delete out[dim];
      return out;
    });
  }
  const chips = Object.entries(filters).flatMap(([dim, vals]) => vals.map((v) => ({ dim, value: v })));

  const Screen = page === "overview" ? Overview : page === "explore" ? Explore : page === "live" ? Live : Overview;

  return (
    <div style={{ minHeight: "100vh", background: "var(--bg-app)", color: "var(--text-primary)", padding: "20px 24px" }}>
      <div style={{ maxWidth: "var(--content-max)", margin: "0 auto" }}>
        <Header range={range} onRange={setRange} tz={tz} onTz={setTz} theme={theme} onTheme={onTheme} />

        <div style={{ display: "flex", alignItems: "center", gap: 12, marginBottom: "var(--space-4)", flexWrap: "wrap" }}>
          <Tabs value={page} onChange={setPage} tabs={["overview", "explore", "live"]} />
          {page !== "live" && (
            <span style={{ marginLeft: "auto", fontSize: 12, color: "var(--text-faint)" }}>
              tatitok v0.6.2 · snapshot 2026-06-10 · db a3f9c1
            </span>
          )}
        </div>

        {chips.length > 0 && (
          <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: "var(--space-4)", flexWrap: "wrap" }}>
            <span style={{ fontSize: 12, color: "var(--text-tertiary)" }}>filters</span>
            {chips.map((c) => (
              <FilterChip key={c.dim + c.value} dim={c.dim} value={c.value} onRemove={() => toggle(c.dim, c.value)} />
            ))}
            <Button variant="subtle" size="sm" onClick={() => setFilters({})}>clear all</Button>
          </div>
        )}

        <div style={{ display: "flex", gap: "var(--space-4)", alignItems: "flex-start" }}>
          <RailX facets={TTx.facets} filters={filters} onToggle={toggle} />
          <main style={{ flex: 1, minWidth: 0 }}>
            <Screen filters={filters} onToggle={toggle} />
          </main>
        </div>
      </div>
    </div>
  );
}

ReactDOM.createRoot(document.getElementById("root")).render(<App />);
