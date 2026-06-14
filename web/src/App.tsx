import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { EChartsOption } from "echarts";
import {
  fetchDaily,
  fetchDailyBy,
  fetchFacets,
  fetchHealth,
  fetchModels,
  fetchPlans,
  fetchTotals,
  usd,
  compactTokens,
  totalTokens,
  browserTZ,
  availableTZs,
  todayInTZ,
  daysAgoInTZ,
  type DailyByRow,
  type DailyRow,
  type FacetValue,
  type Health,
  type ModelInfo,
  type PlanStatus,
  type Totals,
} from "./api";
import {
  countActive,
  displayValue,
  emptyFilters,
  facetDims,
  filterQuery,
  filtersFromURL,
  filtersToURL,
  rawValue,
  removeValue,
  toggleValue,
  type FacetDim,
  type FilterState,
  type GroupBy,
  type Sort,
  type SortKey,
  type View,
} from "./filters";
import { dayTotal, topModelsAtDay } from "./tooltip";
import { touchedInRange } from "./invalidate";
import {
  LAYOUT_KEY,
  defaultLayout,
  loadLayout,
  serializeLayout,
  type Layout,
} from "./layout";
import { useStream } from "./useStream";
import { THEMES, loadTheme, saveTheme, applyTheme } from "./theme";
import Chart from "./components/Chart";
import Breakdown from "./components/Breakdown";
import { sumByKey, rollupRows, sortTotals, OTHERS_KEY, HOME_TOP_N } from "./aggregate";
import PlanCard from "./components/Plans";
import FacetRail from "./components/FacetRail";
import PanelGrid from "./components/PanelGrid";
import Mark from "./ui/Mark";
import ClassDot from "./ui/ClassDot";
import Button from "./ui/Button";
import Select from "./ui/Select";
import FilterChip from "./ui/FilterChip";
import Badge from "./ui/Badge";
import Card from "./ui/Card";
import Stat from "./ui/Stat";

const presets = [
  { label: "7d", days: 7 },
  { label: "30d", days: 30 },
  { label: "90d", days: 90 },
  { label: "all", days: 0 },
] as const;

const axisText = { color: "#a1a1aa", fontSize: 11, fontFamily: "Jost, sans-serif" };

// Stable per-entity series colors (M7 Task 3). The design system forbids
// rainbow series palettes; instead an entity (provider/model/harness) keeps
// ONE calm hue across every chart, assigned by a stable hash of its key so
// the color is independent of which other entities are present in a stack.
// The palette is the four economic-class hues plus two muted tones (used
// only when a stack carries >4 providers). Concrete hexes — ECharts
// itemStyle.color does not resolve CSS vars, and the class hues are fixed
// across the dark tones anyway. (Full class-ENCODED coloring — color means
// the economic class — awaits the M8 class model + chart group-by; here the
// color only has to be calm, non-rainbow, and stable per entity.)
const SERIES_PALETTE = ["#a78bfa", "#38bdf8", "#f5b547", "#4ade80", "#6b7fd7", "#c98bb0"];
function seriesColor(key: string): string {
  // The rolled-up "others" bucket (M8 1C) is an aggregate, not an entity — a
  // muted grey keeps it from reading like a real provider/model/harness.
  if (key === OTHERS_KEY) return "#52525b";
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
  return SERIES_PALETTE[h % SERIES_PALETTE.length];
}

// dailyStackedChart is the stacked daily bar chart, stacked by whatever
// dimension keys the rows — provider on the detail charts; the group-by
// dimension on the home overview (M8 1B). Its tooltip (M6 Task 3) shows a
// day-total line above the per-series breakdown; passing modelBreakdown adds
// a by-model section (top 5 + "other"). Every tooltip number is computed from
// the SAME filtered, timezoned daily_by rows the bars render, so the tooltip
// cannot disagree with its chart.
function dailyStackedChart(
  rows: DailyByRow[],
  value: (r: DailyByRow) => number,
  fmt: (v: number) => string,
  modelBreakdown?: { rows: DailyByRow[]; value: (r: DailyByRow) => number },
): EChartsOption {
  const days = [...new Set(rows.map((r) => r.date))].sort();
  const providers = [...new Set(rows.map((r) => r.key))].sort();
  const byCell = new Map<string, number>();
  for (const r of rows) byCell.set(`${r.date}|${r.key}`, value(r));
  return {
    backgroundColor: "transparent",
    animation: false,
    legend: { textStyle: axisText, top: 0 },
    grid: { left: 56, right: 12, top: 32, bottom: 24 },
    tooltip: {
      trigger: "axis",
      backgroundColor: "var(--surface-raised)",
      borderColor: "var(--border-hairline)",
      borderWidth: 0.5,
      textStyle: { color: "var(--text-secondary)", fontSize: 12, fontFamily: "Jost, sans-serif" },
      extraCssText: "box-shadow: var(--shadow-overlay); border-radius: 10px; font-variant-numeric: tabular-nums;",
      formatter: (params: unknown) => {
        const arr = (Array.isArray(params) ? params : [params]) as Array<{
          axisValue?: string; seriesName?: string; value?: number | null; marker?: string;
        }>;
        if (arr.length === 0) return "";
        const day = String(arr[0]?.axisValue ?? "");
        const parts = [
          `<div style="font-weight:500">${day}</div>`,
          `<div>total <b>${fmt(dayTotal(rows, day, value))}</b></div>`,
        ];
        const seriesLines = arr
          .filter((p) => Number(p.value ?? 0) !== 0)
          .map((p) => `${p.marker ?? ""}${p.seriesName ?? ""} <b>${fmt(Number(p.value ?? 0))}</b>`);
        if (seriesLines.length > 0) parts.push(seriesLines.join("<br/>"));
        if (modelBreakdown) {
          const models = topModelsAtDay(modelBreakdown.rows, day, modelBreakdown.value);
          if (models.length > 0) {
            parts.push(`<div style="margin-top:4px;color:var(--text-secondary)">by model</div>`);
            parts.push(models.map((m) => `${displayValue(m.key)} <b>${fmt(m.value)}</b>`).join("<br/>"));
          }
        }
        return parts.join("");
      },
    },
    xAxis: { type: "category", data: days, axisLabel: axisText },
    yAxis: { type: "value", axisLabel: { ...axisText, formatter: (v: number) => fmt(v) }, splitLine: { lineStyle: { color: "#27272a" } } },
    series: providers.map((p) => ({
      name: displayValue(p),
      type: "bar",
      stack: "total",
      itemStyle: { color: seriesColor(p) },
      emphasis: { focus: "series" },
      data: days.map((d) => byCell.get(`${d}|${p}`) ?? 0),
    })),
  };
}

// valueDonut (M8 1A): the value donut on the home overview, sliced by
// whatever dimension keys the rows — provider by default, the group-by
// dimension at 1B. Same per-entity hue as the bars (seriesColor) over the
// SAME served rows — display-only re-presentation, no counting change. Value
// is API-equivalent (the primary value metric). itemStyle colors are concrete
// hexes because ECharts' SVG itemStyle does not resolve CSS vars (the reason
// dailyStackedChart hard-codes its palette); the HTML tooltip does resolve
// them, so it keeps the design-system vars.
function valueDonut(rows: DailyByRow[]): EChartsOption {
  const byKey = new Map<string, number>();
  for (const r of rows) byKey.set(r.key, (byKey.get(r.key) ?? 0) + r.costAPIEquivMicro / 1e6);
  const data = [...byKey.entries()]
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([k, v]) => ({ name: displayValue(k), value: v, itemStyle: { color: seriesColor(k) } }));
  return {
    backgroundColor: "transparent",
    animation: false,
    tooltip: {
      trigger: "item",
      backgroundColor: "var(--surface-raised)",
      borderColor: "var(--border-hairline)",
      borderWidth: 0.5,
      textStyle: { color: "var(--text-secondary)", fontSize: 12, fontFamily: "Jost, sans-serif" },
      extraCssText: "box-shadow: var(--shadow-overlay); border-radius: 10px; font-variant-numeric: tabular-nums;",
      formatter: (p: unknown) => {
        const it = p as { name?: string; value?: number; marker?: string; percent?: number };
        return `${it.marker ?? ""}${it.name ?? ""} <b>$${Number(it.value ?? 0).toFixed(2)}</b> · ${it.percent ?? 0}%`;
      },
    },
    series: [
      {
        type: "pie",
        radius: ["62%", "86%"],
        center: ["50%", "50%"],
        avoidLabelOverlap: false,
        padAngle: 2,
        itemStyle: { borderRadius: 4, borderColor: "#161618", borderWidth: 2 },
        label: { show: false },
        labelLine: { show: false },
        emphasis: { scale: true, scaleSize: 4 },
        data,
      },
    ],
  };
}

export default function App() {
  // Filter state and the day range live in the URL — shareable,
  // bookmarkable, survives refresh; no persistence beyond that (M5
  // Task 4). One state drives every chart, table and total.
  const initial = useMemo(() => filtersFromURL(window.location.search), []);
  // Timezone (M6 Task 2): the day-bucketing zone. Defaults to the
  // browser's IANA zone, overridable via the header selector, and lives
  // in the URL like every other filter. The server resolves the name
  // against its embedded tzdata and declares it back.
  const initialTZ = initial.tz ?? browserTZ();
  const [tz, setTz] = useState(initialTZ);
  const tzOptions = useMemo(() => availableTZs(), []);
  const [filters, setFilters] = useState<FilterState>(initial.filters);
  const [from, setFrom] = useState(initial.from ?? daysAgoInTZ(initialTZ, 29));
  const [to, setTo] = useState(initial.to ?? todayInTZ(initialTZ));
  // Page (M8 1A): home | detail. Shareable view state → URL (owner ruling),
  // restored by popstate/refresh like filters/range/tz. Default home.
  const [view, setView] = useState<View>(initial.view);
  // Group-by (M8 1B): the home overview's aggregation dimension. Shareable →
  // URL like view; default provider. Drives the home chart/donut/table only.
  const [groupBy, setGroupBy] = useState<GroupBy>(initial.groupBy);
  // Table sort (M8 1D): shared by the home + detail breakdown tables.
  // Shareable → URL; default equiv-desc (never actual cost).
  const [sort, setSort] = useState<Sort>(initial.sort);
  const [daily, setDaily] = useState<DailyRow[]>([]);
  const [byProvider, setByProvider] = useState<DailyByRow[]>([]);
  const [byHarness, setByHarness] = useState<DailyByRow[]>([]);
  const [byModel, setByModel] = useState<DailyByRow[]>([]);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [facets, setFacets] = useState<Record<string, FacetValue[]>>({});
  const [today, setToday] = useState<Totals | null>(null);
  const [plans, setPlans] = useState<PlanStatus[]>([]);
  const [health, setHealth] = useState<Health | null>(null);
  const [source, setSource] = useState(""); // serving path of the day query ("rollup"|"events")
  const [err, setErr] = useState<string | null>(null);
  // Panel layout (M6 Task 4) is LOCAL presentation state: it lives in the
  // browser only, never the URL. fullscreen is transient (never persisted).
  const [layout, setLayout] = useState<Layout>(() =>
    loadLayout(typeof localStorage !== "undefined" ? localStorage.getItem(LAYOUT_KEY) : null),
  );
  const [fullscreen, setFullscreen] = useState<string | null>(null);
  // Dark tone (M7 Task 2): LOCAL presentation state like the panel layout
  // — browser only, never the URL. Lazy-init from localStorage.
  const [theme, setTheme] = useState(loadTheme);
  const stream = useStream();
  const lastRangeFetch = useRef(0);
  // Request-generation guard (M6 Codex F5): each loadRange bumps this; a
  // response whose generation is no longer current is discarded, so a
  // slow earlier fetch can't overwrite newer filter/timezone/range state.
  const rangeGen = useRef(0);

  const fq = useMemo(() => filterQuery(filters), [filters]);

  // URL sync (replaceState — every click is not a history entry) and
  // back/forward restore. tz round-trips alongside filters and range.
  useEffect(() => {
    const url = filtersToURL(filters, from, to, tz, view, groupBy, sort);
    if (window.location.search !== url) {
      window.history.replaceState(null, "", url);
    }
  }, [filters, from, to, tz, view, groupBy, sort]);
  useEffect(() => {
    const onPop = () => {
      const s = filtersFromURL(window.location.search);
      setFilters(s.filters);
      if (s.from) setFrom(s.from);
      if (s.to) setTo(s.to);
      if (s.tz) setTz(s.tz);
      setView(s.view);
      setGroupBy(s.groupBy);
      setSort(s.sort);
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  // Layout persists to the browser ONLY — deliberately not the URL
  // (M6 Task 4: URLs share filters/range/timezone, not panel arrangement).
  useEffect(() => {
    try {
      localStorage.setItem(LAYOUT_KEY, serializeLayout(layout));
    } catch {
      /* private mode / storage disabled — layout just won't persist */
    }
  }, [layout]);
  // Dark tone persists to the browser ONLY (like the layout) and reflects
  // onto <html data-theme>, which the themes.css [data-theme] blocks
  // override the canvas + surface steps on.
  useEffect(() => {
    applyTheme(theme);
    saveTheme(theme);
  }, [theme]);
  // Escape exits fullscreen (transient — never persisted, never in URL).
  useEffect(() => {
    if (!fullscreen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setFullscreen(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [fullscreen]);

  const toggle = (dim: FacetDim, value: string) => setFilters((f) => toggleValue(f, dim, value));

  const loadRange = (f: string, t: string, q: string, z: string) => {
    const gen = ++rangeGen.current;
    Promise.all([
      fetchDaily(f, t, q, z),
      fetchDailyBy("provider", f, t, q, z),
      fetchDailyBy("harness", f, t, q, z),
      fetchDailyBy("model", f, t, q, z),
    ])
      .then(([d, p, h, m]) => {
        if (gen !== rangeGen.current) return; // superseded by a newer request
        setDaily(d.daily ?? []);
        setByProvider(p.daily_by ?? []);
        setByHarness(h.daily_by ?? []);
        setByModel(m.daily_by ?? []);
        setSource(d.source);
        setErr(null);
      })
      .catch((e) => {
        if (gen === rangeGen.current) setErr(String(e));
      });
  };

  useEffect(() => {
    loadRange(from, to, fq, tz);
    fetchTotals("today", fq, tz).then((t) => setToday(t.totals)).catch(() => {});
    fetchModels().then((m) => setModels(m.models ?? [])).catch(() => {});
    fetchFacets().then((f) => setFacets(f.facets ?? {})).catch(() => {});
    fetchHealth().then(setHealth).catch(() => {});
  }, [from, to, fq, tz]);

  // Live updates: every pass refreshes the today panel, the plan window
  // meters and the facet counts; the range refetches only when a
  // touched day falls inside it (or when the stream says we lost
  // events / reconnected: touchedDays empty).
  useEffect(() => {
    fetchTotals("today", fq, tz).then((t) => setToday(t.totals)).catch(() => {});
    fetchPlans().then((p) => setPlans(p.plans ?? [])).catch(() => {});
    if (stream.bump === 0 || stream.bump === lastRangeFetch.current) return;
    fetchFacets().then((f) => setFacets(f.facets ?? {})).catch(() => {});
    // SSE touched-days are UTC; the visible range is local — map before
    // comparing so a boundary event invalidates the right local day (F1).
    if (touchedInRange(stream.touchedDays, tz, from, to)) {
      lastRangeFetch.current = stream.bump;
      loadRange(from, to, fq, tz);
    }
  }, [stream.bump]); // eslint-disable-line react-hooks/exhaustive-deps

  // The owner's stop-1 chart direction: API-EQUIVALENT cost is the
  // primary daily chart; actual out-of-pocket cost gets its own chart —
  // both truths always visible, no toggle (post-plans, actual-only is
  // honest but nearly empty).
  // Day-total + by-model tooltip on the tokens and API-equivalent charts
  // (M6 Task 3); the actual-cost chart gets the day total only (post-plans
  // it is near-empty, so a model breakdown of ~$0 adds nothing). byModel
  // is the same filtered/timezoned set the bars use.
  const equivChart = useMemo(
    () => dailyStackedChart(byProvider, (r) => r.costAPIEquivMicro / 1e6, (v) => `$${v.toFixed(2)}`,
      { rows: byModel, value: (r) => r.costAPIEquivMicro / 1e6 }),
    [byProvider, byModel],
  );
  const actualChart = useMemo(
    () => dailyStackedChart(byProvider, (r) => r.costUSDMicro / 1e6, (v) => `$${v.toFixed(2)}`),
    [byProvider],
  );
  const tokenChart = useMemo(
    () => dailyStackedChart(byProvider, totalTokens, compactTokens,
      { rows: byModel, value: totalTokens }),
    [byProvider, byModel],
  );
  const onProviderSeries = (seriesName: string) => toggle("provider", rawValue(seriesName));

  const modelBases = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const info of models) {
      const cur = m.get(info.model) ?? [];
      if (!cur.includes(info.costBasis)) cur.push(info.costBasis);
      m.set(info.model, cur);
    }
    return m;
  }, [models]);

  const rangeUnpriced = useMemo(
    () => daily.reduce((n, r) => n + r.unpricedEvents, 0),
    [daily],
  );
  // Shared range aggregates (display-only sums over the served daily rows) —
  // used by both the home hero/donut-center and the detail range-totals card,
  // so the two pages state the same number by construction.
  const rangeEquiv = useMemo(() => daily.reduce((n, r) => n + r.costAPIEquivMicro, 0), [daily]);
  const rangeActual = useMemo(() => daily.reduce((n, r) => n + r.costUSDMicro, 0), [daily]);
  const rangeTokens = useMemo(() => daily.reduce((n, r) => n + totalTokens(r), 0), [daily]);

  // Group-by (M8 1B): the home overview re-aggregates by the chosen dimension
  // — same served rows, different key, so the grand total is invariant across
  // dimensions (conservation). The detail page keeps its dedicated
  // per-dimension panels, so groupBy drives home only. groupRows just selects
  // which already-fetched daily_by set the home chart/donut/table read.
  const groupRows = groupBy === "harness" ? byHarness : groupBy === "model" ? byModel : byProvider;
  // Rollup (M8 1C): collapse families (vllm-*) and fold all but the top-N into
  // an "others" bucket — a pure relabel of the same served rows (conserved).
  // Feeding the rolled rows to the same chart/donut/sumByKey yields a calm
  // top-N overview; the detail page keeps the full per-entity breakdown.
  const homeRollup = useMemo(() => rollupRows(groupRows, HOME_TOP_N), [groupRows]);
  const homeChart = useMemo(
    () => dailyStackedChart(homeRollup, (r) => r.costAPIEquivMicro / 1e6, (v) => `$${v.toFixed(2)}`),
    [homeRollup],
  );
  const homeDonut = useMemo(() => valueDonut(homeRollup), [homeRollup]);
  // The home table shares the global Sort (M8 1D); sortTotals ranks by the
  // chosen metric and pins aggregates ("others"/family) to the bottom.
  const homeTable = useMemo(() => sortTotals(sumByKey(homeRollup), sort), [homeRollup, sort]);
  // Click-to-filter only on REAL facet values: rolled-up buckets ("others",
  // collapsed families) are display aggregates, not single filter values, so a
  // click on one is ignored rather than applying a filter that matches nothing.
  const onGroupSelect = (raw: string) => {
    if (!(facets[groupBy] ?? []).some((fv) => fv.value === raw)) return;
    toggle(groupBy, raw);
  };
  const onGroupSeries = (name: string) => onGroupSelect(rawValue(name));
  // Sort toggle (M8 1D): clicking the active column flips direction; a new
  // column starts descending. Shared by every breakdown table; persists to URL.
  const onSort = (key: SortKey) =>
    setSort((s) => (s.key === key ? { key, dir: s.dir === "desc" ? "asc" : "desc" } : { key, dir: "desc" }));

  const chips = facetDims.flatMap((dim) => filters[dim].map((v) => ({ dim, value: v })));

  // Panel content keyed by panel id (M6 Task 4): the charts and tables,
  // each with its filter handlers intact. PanelGrid only positions and
  // frames these — the handlers (click-to-filter, legend interception,
  // row select) ride along into every layout state, fullscreen included.
  const panelContent: Record<string, ReactNode> = {
    "chart-equiv": <Chart option={equivChart} onSeriesClick={onProviderSeries} />,
    "chart-actual": <Chart option={actualChart} onSeriesClick={onProviderSeries} />,
    "chart-tokens": <Chart option={tokenChart} onSeriesClick={onProviderSeries} />,
    "break-harness": (
      <Breakdown totals={sortTotals(sumByKey(byHarness), sort)} sort={sort} onSort={onSort} onSelect={(raw) => toggle("harness", raw)} active={filters.harness} />
    ),
    "break-provider": (
      <Breakdown totals={sortTotals(sumByKey(byProvider), sort)} sort={sort} onSort={onSort} onSelect={(raw) => toggle("provider", raw)} active={filters.provider} />
    ),
    "break-model": (
      <Breakdown totals={sortTotals(sumByKey(byModel), sort)} sort={sort} onSort={onSort} bases={modelBases} onSelect={(raw) => toggle("model", raw)} active={filters.model} />
    ),
  };

  return (
    <div className="mx-auto min-h-screen max-w-[var(--content-max)] bg-app px-6 py-5 text-primary">
      <header className="mb-5 flex flex-wrap items-center gap-4">
        <div className="flex items-center gap-3">
          <span className="text-primary"><Mark size={26} /></span>
          <span className="text-[21px] font-medium tracking-[-0.01em]">tatitok</span>
          <span className="text-sm text-faint">local AI usage</span>
        </div>
        {/* Page nav (M8 1A): home overview vs full detail. Shareable → URL;
            the active page is the one global view state both pages share. */}
        <nav className="flex items-center gap-1" aria-label="page">
          <Button size="sm" variant="subtle" active={view === "home"} aria-current={view === "home" ? "page" : undefined} onClick={() => setView("home")}>
            home
          </Button>
          <Button size="sm" variant="subtle" active={view === "detail"} aria-current={view === "detail" ? "page" : undefined} onClick={() => setView("detail")}>
            detail
          </Button>
        </nav>
        <div className="ml-auto flex flex-wrap items-center gap-2 text-sm">
          {presets.map((p) => (
            <Button
              key={p.label}
              size="sm"
              onClick={() => {
                setFrom(p.days === 0 ? "1970-01-01" : daysAgoInTZ(tz, p.days - 1));
                setTo(todayInTZ(tz));
              }}
            >
              {p.label}
            </Button>
          ))}
          <input
            type="date"
            id="range-from"
            name="range-from"
            aria-label={`range start (${tz} day)`}
            value={from}
            onChange={(e) => e.target.value && setFrom(e.target.value)}
            className="h-[30px] rounded-[10px] border-[0.5px] border-hairline bg-card px-2 text-secondary"
          />
          <span className="text-faint">→</span>
          <input
            type="date"
            id="range-to"
            name="range-to"
            aria-label={`range end (${tz} day)`}
            value={to}
            onChange={(e) => e.target.value && setTo(e.target.value)}
            className="h-[30px] rounded-[10px] border-[0.5px] border-hairline bg-card px-2 text-secondary"
          />
          {/* Days bucket in the selected zone (M6 Task 2). UTC stays the
              storage/parity truth; the server resolves this IANA name
              against its embedded tzdata and declares it + the serving
              path back. The path badge makes the hard-stop-1 drive
              glanceable: rollup for whole-hour zones, events for
              fractional offsets. */}
          <label className="flex items-center gap-1.5 text-xs text-tertiary">
            <span>days in</span>
            <Select
              id="timezone"
              name="timezone"
              aria-label="timezone for day bucketing"
              value={tz}
              onChange={(e) => setTz(e.target.value)}
              options={tzOptions}
              style={{ maxWidth: "12rem" }}
            />
          </label>
          <label className="flex items-center gap-1.5 text-xs text-tertiary">
            <span>theme</span>
            <Select
              id="theme"
              name="theme"
              aria-label="dark theme tone"
              value={theme}
              onChange={(e) => setTheme(e.target.value)}
              options={THEMES.map((t) => ({ value: t.id, label: t.label }))}
            />
          </label>
          {source && (
            <Badge
              tone="tag"
              style={{ cursor: "help" }}
              title={
                source === "rollup"
                  ? `served from rollups — ${tz} is a whole-hour offset, so local days map to whole UTC hours`
                  : `served from exact events — ${tz} is a fractional offset (or a basis filter is active), so the UTC-hour rollups cannot serve it`
              }
            >
              · {source}
            </Badge>
          )}
        </div>
      </header>

      {err && (
        <div
          className="mb-4 rounded-[10px] border-[0.5px] px-3 py-2 text-sm"
          style={{
            color: "var(--color-danger)",
            borderColor: "color-mix(in oklab, var(--color-danger) 30%, transparent)",
            background: "color-mix(in oklab, var(--color-danger) 12%, transparent)",
          }}
        >
          {err}
        </div>
      )}

      {chips.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          <span className="text-xs text-tertiary">filters</span>
          {chips.map((c) => (
            <FilterChip
              key={`${c.dim}|${c.value}`}
              dim={c.dim}
              value={displayValue(c.value)}
              onRemove={() => setFilters((f) => removeValue(f, c.dim, c.value))}
            />
          ))}
          <Button variant="subtle" size="sm" onClick={() => setFilters(emptyFilters())}>
            clear all
          </Button>
        </div>
      )}

      <div className="flex gap-4">
        <FacetRail facets={facets} filters={filters} onToggle={toggle} rollup={view === "home"} />

        <main className="min-w-0 flex-1">
          {view === "home" ? (
            // Home (M8 1A): the calm overview — value-extracted hero, the
            // primary daily API-equivalent chart, a by-provider value donut,
            // and one ranked by-provider table. Every figure is the SAME
            // served, filtered, timezoned data the detail page uses; this is
            // re-presentation only, no counting change.
            <div className="space-y-4">
              <Card title="value extracted">
                <Stat
                  size="hero"
                  positive
                  value={usd(rangeEquiv)}
                  sub={`API-equivalent value of your usage · ${from} → ${to}`}
                />
                <div className="mt-2 text-xs text-faint tabular-nums">
                  vs {usd(rangeActual)} actual out-of-pocket
                  {rangeActual > 0 && rangeEquiv > 0 && ` · ${(rangeEquiv / rangeActual).toFixed(1)}× extracted`}
                </div>
              </Card>

              {/* Group-by (M8 1B): one segmented control drives the chart,
                  donut and table below; the choice is shareable → URL. */}
              <div className="flex items-center gap-2 text-xs text-tertiary">
                <span>group by</span>
                <div className="flex items-center gap-1" role="group" aria-label="group the overview by dimension">
                  {(["harness", "provider", "model"] as GroupBy[]).map((d) => (
                    <Button
                      key={d}
                      size="sm"
                      variant="subtle"
                      active={groupBy === d}
                      aria-pressed={groupBy === d}
                      onClick={() => setGroupBy(d)}
                    >
                      {d}
                    </Button>
                  ))}
                </div>
              </div>

              <div className="grid grid-cols-1 gap-4 lg:grid-cols-[1.6fr_1fr]">
                <Card title={`daily API-equivalent (by ${groupBy})`}>
                  <Chart option={homeChart} height={264} onSeriesClick={onGroupSeries} />
                </Card>
                <Card title={`value by ${groupBy}`}>
                  <div className="relative">
                    <Chart option={homeDonut} height={200} onSeriesClick={onGroupSeries} />
                    <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
                      <div className="text-2xl tabular-nums text-primary" style={{ letterSpacing: "-0.02em" }}>
                        {usd(rangeEquiv)}
                      </div>
                      <div className="text-[11px] text-tertiary">total value</div>
                    </div>
                  </div>
                </Card>
              </div>

              <Card title={`by ${groupBy}`}>
                {/* bases only when grouping by model → ClassDots on model rows
                    (M8 1E); provider/harness rows aren't single models. */}
                <Breakdown
                  totals={homeTable}
                  sort={sort}
                  onSort={onSort}
                  onSelect={onGroupSelect}
                  active={filters[groupBy]}
                  bases={groupBy === "model" ? modelBases : undefined}
                />
              </Card>
            </div>
          ) : (
            // Detail (M8 1A): the full breakdown App has always rendered —
            // today + plan meters + range totals, then the reorderable panel
            // grid. Unchanged from M7 beyond being rehomed under the view
            // switch (range totals now read the shared range memos).
            <>
              <section className="mb-5 grid grid-cols-1 gap-4 md:grid-cols-3">
                <Card
                  title={
                    <span className="inline-flex items-center gap-2">
                      today
                      <ClassDot
                        tone={stream.connected ? "live" : "neutral"}
                        pulse={stream.connected}
                        title={stream.connected ? "live — SSE connected" : "stream disconnected (EventSource will retry; data refetches on reconnect)"}
                      />
                      {countActive(filters) > 0 && (
                        <span className="text-[11px]" style={{ color: "var(--accent)" }}>filtered</span>
                      )}
                    </span>
                  }
                >
                  <Stat
                    size="lg"
                    value={today ? usd(today.costUSDMicro) : "—"}
                    unpriced={!!today && today.unpricedEvents > 0}
                    unpricedTitle={today ? `${today.unpricedEvents} events today carry no resolvable price — cost is a floor.` : undefined}
                    sub={today ? `${compactTokens(totalTokens(today))} tokens` : "no data yet"}
                  />
                  {today && today.costAPIEquivMicro > 0 && (
                    <div className="mt-1 text-xs text-tertiary tabular-nums">≈ {usd(today.costAPIEquivMicro)} API-equiv</div>
                  )}
                  {stream.lastPass && (
                    <div className="mt-2 text-xs text-faint tabular-nums">
                      last pass #{stream.lastPass.pass}:{" "}
                      {stream.lastPass.harnesses
                        .map((h) => `${h.harness} +${h.new}${h.replaced ? ` ~${h.replaced}` : ""}`)
                        .join(", ")}
                    </div>
                  )}
                </Card>
                {plans.map((p) => (
                  <PlanCard key={p.name} plan={p} />
                ))}
                <Card className="md:col-span-2" title="range totals">
                  <div className="flex flex-wrap gap-x-10 gap-y-4">
                    <Stat value={usd(rangeEquiv)} sub={`API-equivalent (${from} → ${to})`} />
                    <Stat
                      value={usd(rangeActual)}
                      unpriced={rangeUnpriced > 0}
                      unpricedTitle={`${rangeUnpriced} events in range carry no resolvable price — cost is a floor (the CLI's asterisk).`}
                      sub="actual cost"
                    />
                    <Stat value={compactTokens(rangeTokens)} sub="tokens" />
                    <Stat value={String(daily.length)} sub="active days" />
                  </div>
                </Card>
              </section>

              {/* Panel grid (M6 Task 4): charts and breakdowns become
                  reorderable, resizable, fullscreen-able panels. Layout is
                  browser-local (never the URL); a reset restores defaults.
                  Filter interactions inside the panels survive every layout
                  state — the same elements are reframed, never remounted. */}
              <div className="mb-2 flex items-center gap-2 text-xs text-tertiary">
                <span>panels</span>
                <span className="hidden text-faint sm:inline">drag header to reorder · −/+ to resize · ⤢ fullscreen (Esc)</span>
                <Button
                  className="ml-auto"
                  variant="subtle"
                  size="sm"
                  onClick={() => setLayout(defaultLayout())}
                  title="restore the default panel order and sizes"
                >
                  reset layout
                </Button>
              </div>
              <PanelGrid
                layout={layout}
                content={panelContent}
                fullscreen={fullscreen}
                onLayout={setLayout}
                onFullscreen={setFullscreen}
              />
            </>
          )}

          <footer className="mt-6 text-xs text-faint tabular-nums">
            {health
              ? `tatitok ${health.version} · snapshot ${health.price_snapshot} · ${health.overrides} overrides / ${health.reference_models} reference models · db ${health.db_hash} · up ${Math.floor(health.uptime_seconds / 60)}m`
              : "hub unreachable"}
          </footer>
        </main>
      </div>
    </div>
  );
}
