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
} from "./filters";
import { dayTotal, topModelsAtDay } from "./tooltip";
import {
  LAYOUT_KEY,
  defaultLayout,
  loadLayout,
  serializeLayout,
  type Layout,
} from "./layout";
import { useStream } from "./useStream";
import Chart from "./components/Chart";
import Breakdown, { sumByKey } from "./components/Breakdown";
import PlanCard from "./components/Plans";
import FacetRail from "./components/FacetRail";
import PanelGrid from "./components/PanelGrid";

const presets = [
  { label: "7d", days: 7 },
  { label: "30d", days: 30 },
  { label: "90d", days: 90 },
  { label: "all", days: 0 },
] as const;

const axisText = { color: "#a1a1aa", fontSize: 11 };

// dailyProviderChart is the stacked-by-provider daily bar chart. Its
// tooltip (M6 Task 3) shows a day-total line above the per-provider
// breakdown; passing modelBreakdown adds a by-model section (top 5 +
// "other") for the tokens and API-equivalent charts. Every tooltip
// number is computed from the SAME filtered, timezoned daily_by rows the
// bars render, so the tooltip cannot disagree with its chart.
function dailyProviderChart(
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
    legend: { textStyle: axisText, top: 0 },
    grid: { left: 56, right: 12, top: 32, bottom: 24 },
    tooltip: {
      trigger: "axis",
      formatter: (params: unknown) => {
        const arr = (Array.isArray(params) ? params : [params]) as Array<{
          axisValue?: string; seriesName?: string; value?: number | null; marker?: string;
        }>;
        if (arr.length === 0) return "";
        const day = String(arr[0]?.axisValue ?? "");
        const parts = [
          `<div style="font-weight:600">${day}</div>`,
          `<div>total <b>${fmt(dayTotal(rows, day, value))}</b></div>`,
        ];
        const seriesLines = arr
          .filter((p) => Number(p.value ?? 0) !== 0)
          .map((p) => `${p.marker ?? ""}${p.seriesName ?? ""} <b>${fmt(Number(p.value ?? 0))}</b>`);
        if (seriesLines.length > 0) parts.push(seriesLines.join("<br/>"));
        if (modelBreakdown) {
          const models = topModelsAtDay(modelBreakdown.rows, day, modelBreakdown.value);
          if (models.length > 0) {
            parts.push(`<div style="margin-top:4px;color:#a1a1aa">by model</div>`);
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
      emphasis: { focus: "series" },
      data: days.map((d) => byCell.get(`${d}|${p}`) ?? 0),
    })),
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
  const stream = useStream();
  const lastRangeFetch = useRef(0);

  const fq = useMemo(() => filterQuery(filters), [filters]);

  // URL sync (replaceState — every click is not a history entry) and
  // back/forward restore. tz round-trips alongside filters and range.
  useEffect(() => {
    const url = filtersToURL(filters, from, to, tz);
    if (window.location.search !== url) {
      window.history.replaceState(null, "", url);
    }
  }, [filters, from, to, tz]);
  useEffect(() => {
    const onPop = () => {
      const s = filtersFromURL(window.location.search);
      setFilters(s.filters);
      if (s.from) setFrom(s.from);
      if (s.to) setTo(s.to);
      if (s.tz) setTz(s.tz);
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
    Promise.all([
      fetchDaily(f, t, q, z),
      fetchDailyBy("provider", f, t, q, z),
      fetchDailyBy("harness", f, t, q, z),
      fetchDailyBy("model", f, t, q, z),
    ])
      .then(([d, p, h, m]) => {
        setDaily(d.daily ?? []);
        setByProvider(p.daily_by ?? []);
        setByHarness(h.daily_by ?? []);
        setByModel(m.daily_by ?? []);
        setSource(d.source);
        setErr(null);
      })
      .catch((e) => setErr(String(e)));
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
    const touched = stream.touchedDays;
    const inRange = touched.length === 0 || touched.some((d) => d >= from && d <= to);
    if (inRange) {
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
    () => dailyProviderChart(byProvider, (r) => r.costAPIEquivMicro / 1e6, (v) => `$${v.toFixed(2)}`,
      { rows: byModel, value: (r) => r.costAPIEquivMicro / 1e6 }),
    [byProvider, byModel],
  );
  const actualChart = useMemo(
    () => dailyProviderChart(byProvider, (r) => r.costUSDMicro / 1e6, (v) => `$${v.toFixed(2)}`),
    [byProvider],
  );
  const tokenChart = useMemo(
    () => dailyProviderChart(byProvider, totalTokens, compactTokens,
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
      <Breakdown totals={sumByKey(byHarness)} onSelect={(raw) => toggle("harness", raw)} active={filters.harness} />
    ),
    "break-provider": (
      <Breakdown totals={sumByKey(byProvider)} onSelect={(raw) => toggle("provider", raw)} active={filters.provider} />
    ),
    "break-model": (
      <Breakdown totals={sumByKey(byModel)} bases={modelBases} onSelect={(raw) => toggle("model", raw)} active={filters.model} />
    ),
  };

  return (
    <div className="min-h-screen bg-zinc-950 px-6 py-5 text-zinc-100">
      <header className="mb-5 flex flex-wrap items-center gap-4">
        <h1 className="text-xl font-bold tracking-tight">
          tatitok
          <span className="ml-2 text-sm font-normal text-zinc-500">local AI usage</span>
        </h1>
        <div className="ml-auto flex flex-wrap items-center gap-2 text-sm">
          {presets.map((p) => (
            <button
              key={p.label}
              className="rounded-lg border border-zinc-800 px-2.5 py-1 text-zinc-300 hover:bg-zinc-800"
              onClick={() => {
                setFrom(p.days === 0 ? "1970-01-01" : daysAgoInTZ(tz, p.days - 1));
                setTo(todayInTZ(tz));
              }}
            >
              {p.label}
            </button>
          ))}
          <input
            type="date"
            id="range-from"
            name="range-from"
            aria-label={`range start (${tz} day)`}
            value={from}
            onChange={(e) => e.target.value && setFrom(e.target.value)}
            className="rounded-lg border border-zinc-800 bg-zinc-900 px-2 py-1 text-zinc-300"
          />
          <span className="text-zinc-600">→</span>
          <input
            type="date"
            id="range-to"
            name="range-to"
            aria-label={`range end (${tz} day)`}
            value={to}
            onChange={(e) => e.target.value && setTo(e.target.value)}
            className="rounded-lg border border-zinc-800 bg-zinc-900 px-2 py-1 text-zinc-300"
          />
          {/* Days bucket in the selected zone (M6 Task 2). UTC stays the
              storage/parity truth; the server resolves this IANA name
              against its embedded tzdata and declares it + the serving
              path back. The path badge makes the hard-stop-1 drive
              glanceable: rollup for whole-hour zones, events for
              fractional offsets. */}
          <label className="flex items-center gap-1 text-xs text-zinc-500">
            <span className="uppercase tracking-wider">days in</span>
            <select
              id="timezone"
              name="timezone"
              aria-label="timezone for day bucketing"
              value={tz}
              onChange={(e) => setTz(e.target.value)}
              className="max-w-[12rem] rounded-lg border border-zinc-800 bg-zinc-900 px-2 py-1 text-zinc-300"
            >
              {tzOptions.map((z) => (
                <option key={z} value={z}>{z}</option>
              ))}
            </select>
          </label>
          {source && (
            <span
              className="cursor-help text-[10px] uppercase tracking-wider text-zinc-600"
              title={
                source === "rollup"
                  ? `served from rollups — ${tz} is a whole-hour offset, so local days map to whole UTC hours`
                  : `served from exact events — ${tz} is a fractional offset (or a basis filter is active), so the UTC-hour rollups cannot serve it`
              }
            >
              · {source}
            </span>
          )}
        </div>
      </header>

      {err && (
        <div className="mb-4 rounded-lg border border-red-900 bg-red-950/40 px-3 py-2 text-sm text-red-300">
          {err}
        </div>
      )}

      {chips.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          <span className="text-xs uppercase tracking-wider text-zinc-500">filters</span>
          {chips.map((c) => (
            <button
              key={`${c.dim}|${c.value}`}
              className="flex items-center gap-1 rounded-full border border-sky-900 bg-sky-950/60 px-2.5 py-0.5 text-sky-300 hover:bg-sky-900/60"
              onClick={() => setFilters((f) => removeValue(f, c.dim, c.value))}
              title={`remove ${c.dim} filter`}
            >
              <span className="text-xs text-sky-500">{c.dim}:</span>
              {displayValue(c.value)}
              <span aria-hidden>×</span>
            </button>
          ))}
          <button
            className="text-xs text-zinc-500 underline hover:text-zinc-300"
            onClick={() => setFilters(emptyFilters())}
          >
            clear all
          </button>
        </div>
      )}

      <div className="flex gap-4">
        <FacetRail facets={facets} filters={filters} onToggle={toggle} />

        <main className="min-w-0 flex-1">
          <section className="mb-5 grid grid-cols-1 gap-4 md:grid-cols-3">
            <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
              <h2 className="text-sm font-semibold uppercase tracking-wider text-zinc-400">
                today
                <span
                  className={`ml-2 inline-block h-2 w-2 rounded-full ${stream.connected ? "bg-emerald-400" : "bg-zinc-600"}`}
                  title={stream.connected ? "live — SSE connected" : "stream disconnected (EventSource will retry; data refetches on reconnect)"}
                />
                {countActive(filters) > 0 && (
                  <span className="ml-2 text-[10px] font-normal normal-case text-sky-400">filtered</span>
                )}
              </h2>
              <div className="mt-2 text-2xl font-bold tabular-nums">
                {today ? usd(today.costUSDMicro) : "—"}
                {today && today.unpricedEvents > 0 && (
                  <span className="cursor-help text-base text-amber-400" title={`${today.unpricedEvents} events today carry no resolvable price — cost is a floor.`}>*</span>
                )}
              </div>
              <div className="text-sm text-zinc-400 tabular-nums">
                {today ? `${compactTokens(totalTokens(today))} tokens` : "no data yet"}
              </div>
              {today && today.costAPIEquivMicro > 0 && (
                <div className="text-xs text-zinc-500 tabular-nums">≈ {usd(today.costAPIEquivMicro)} API-equiv</div>
              )}
              {stream.lastPass && (
                <div className="mt-2 text-xs text-zinc-500">
                  last pass #{stream.lastPass.pass}:{" "}
                  {stream.lastPass.harnesses
                    .map((h) => `${h.harness} +${h.new}${h.replaced ? ` ~${h.replaced}` : ""}`)
                    .join(", ")}
                </div>
              )}
            </div>
            {plans.map((p) => (
              <PlanCard key={p.name} plan={p} />
            ))}
            <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4 md:col-span-2">
              <h2 className="text-sm font-semibold uppercase tracking-wider text-zinc-400">range totals</h2>
              <div className="mt-2 flex flex-wrap gap-6 tabular-nums">
                <div>
                  <div className="text-2xl font-bold">
                    {usd(daily.reduce((n, r) => n + r.costAPIEquivMicro, 0))}
                  </div>
                  <div className="text-xs text-zinc-500">API-equivalent ({from} → {to})</div>
                </div>
                <div>
                  <div className="text-2xl font-bold">
                    {usd(daily.reduce((n, r) => n + r.costUSDMicro, 0))}
                    {rangeUnpriced > 0 && (
                      <span className="cursor-help text-base text-amber-400" title={`${rangeUnpriced} events in range carry no resolvable price — cost is a floor (the CLI's asterisk).`}>*</span>
                    )}
                  </div>
                  <div className="text-xs text-zinc-500">actual cost</div>
                </div>
                <div>
                  <div className="text-2xl font-bold">{compactTokens(daily.reduce((n, r) => n + totalTokens(r), 0))}</div>
                  <div className="text-xs text-zinc-500">tokens</div>
                </div>
                <div>
                  <div className="text-2xl font-bold">{daily.length}</div>
                  <div className="text-xs text-zinc-500">active days</div>
                </div>
              </div>
            </div>
          </section>

          {/* Panel grid (M6 Task 4): charts and breakdowns become
              reorderable, resizable, fullscreen-able panels. Layout is
              browser-local (never the URL); a reset restores defaults.
              Filter interactions inside the panels survive every layout
              state — the same elements are reframed, never remounted. */}
          <div className="mb-2 flex items-center gap-2 text-xs text-zinc-500">
            <span className="uppercase tracking-wider">panels</span>
            <span className="hidden text-zinc-600 sm:inline">drag header to reorder · −/+ to resize · ⤢ fullscreen (Esc)</span>
            <button
              className="ml-auto rounded-lg border border-zinc-800 px-2 py-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
              onClick={() => setLayout(defaultLayout())}
              title="restore the default panel order and sizes"
            >
              reset layout
            </button>
          </div>
          <PanelGrid
            layout={layout}
            content={panelContent}
            fullscreen={fullscreen}
            onLayout={setLayout}
            onFullscreen={setFullscreen}
          />

          <footer className="mt-6 text-xs text-zinc-600">
            {health
              ? `tatitok ${health.version} · snapshot ${health.price_snapshot} · ${health.overrides} overrides / ${health.reference_models} reference models · db ${health.db_hash} · up ${Math.floor(health.uptime_seconds / 60)}m`
              : "hub unreachable"}
          </footer>
        </main>
      </div>
    </div>
  );
}
