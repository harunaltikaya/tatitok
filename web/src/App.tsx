import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { EChartsOption } from "echarts";
import {
  fetchDaily,
  fetchDailyBy,
  fetchFacets,
  fetchHealth,
  fetchModels,
  fetchPlans,
  fetchLimits,
  fetchActivity,
  fetchOnboardDetect,
  usd,
  compactTokens,
  totalTokens,
  freshCachedSplit,
  browserTZ,
  availableTZs,
  todayInTZ,
  tzOffsetLabel,
  type ActivityBucket,
  type DailyByRow,
  type DailyRow,
  type FacetValue,
  type Health,
  type ModelInfo,
  type LimitsSnapshot,
  type PlanStatus,
  type OnboardDetect,
  type OnboardApplyResult,
} from "./api";
import {
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
import { presets, presetRange, initialRange, activePreset } from "./range";
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
import { sumByKey, rollupRows, mergeFamilies, chartCells, sortTotals, brandColorFor, countUnpriced, OTHERS_KEY, HOME_TOP_N } from "./aggregate";
import PlanCard from "./components/Plans";
import Heatmap from "./components/Heatmap";
import MeterBar from "./ui/MeterBar";
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
import Onboarding from "./components/Onboarding";
import { shouldOfferOnboarding } from "./onboarding";

// Suppresses the first-run auto-open after the user skips (browser-only, like
// the layout/theme). The header "plans" button always re-opens regardless.
const ONBOARD_DISMISS_KEY = "tatitok.onboard.dismissed";

// Range presets + default live in range.ts (pure, tested).

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

// providerColor (M8 1F) is the PROVIDER-channel resolver: Anthropic's brand
// clay takes precedence, every other provider falls back to its per-entity hue.
// Applied only where the dimension IS provider (the by-provider charts, and the
// home chart/donut/table when group-by=provider), so the brand colour never
// leaks into the model or harness groupings. Option B: class colours and other
// entities are untouched — only the provider channel gains the brand.
function providerColor(key: string): string {
  return brandColorFor(key) ?? seriesColor(key);
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
  // colorFor resolves a series' colour by key (M8 1F): the by-provider charts
  // pass providerColor (brand-aware), everyone else gets per-entity seriesColor.
  colorFor: (key: string) => string = seriesColor,
  // openDay (M8 1J): the in-progress day's date (today in the selected tz), or
  // undefined when today isn't in range. That bar renders provisional — faded,
  // value UNCHANGED — so a partial day doesn't read as a real dip or spike.
  openDay?: string,
): EChartsOption {
  const days = [...new Set(rows.map((r) => r.date))].sort();
  const providers = [...new Set(rows.map((r) => r.key))].sort();
  // Cells SUM rows sharing a (date, key): collapsed families / folded "others"
  // arrive as several same-day rows (home), so the bar shows their sum, not the
  // last one (chart≠donut fix — see chartCells).
  const byCell = chartCells(rows, value);
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
          `<div style="font-weight:500">${day}${day === openDay ? ' <span style="font-weight:400;color:var(--text-tertiary)">· partial (today so far)</span>' : ""}</div>`,
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
      itemStyle: { color: colorFor(p) },
      emphasis: { focus: "series" },
      data: days.map((d) => {
        const v = byCell.get(`${d}|${p}`) ?? 0;
        // In-progress day → faded (provisional); value unchanged (M8 1J).
        return d === openDay ? { value: v, itemStyle: { color: colorFor(p), opacity: 0.5 } } : v;
      }),
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
// them, so it keeps the design-token vars.
function valueDonut(rows: DailyByRow[], colorFor: (key: string) => string = seriesColor): EChartsOption {
  const byKey = new Map<string, number>();
  for (const r of rows) byKey.set(r.key, (byKey.get(r.key) ?? 0) + r.costAPIEquivMicro / 1e6);
  const data = [...byKey.entries()]
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([k, v]) => ({ name: displayValue(k), value: v, itemStyle: { color: colorFor(k) } }));
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
  // tz-offset clarity label next to the selector (M8 1I) — display only.
  const tzOffset = useMemo(() => tzOffsetLabel(tz), [tz]);
  const [filters, setFilters] = useState<FilterState>(initial.filters);
  // Default range (no from/to in the URL): the last 7 days in the selected
  // zone — today−6d → today — so the 7d preset reads as active; an explicit
  // URL range wins (range.ts, tested).
  const initialRangeValue = useMemo(() => initialRange(initial.from, initial.to, initialTZ), [initial, initialTZ]);
  const [from, setFrom] = useState(initialRangeValue.from);
  const [to, setTo] = useState(initialRangeValue.to);
  // Page (M8 1A): home | detail. Shareable view state → URL (owner ruling),
  // restored by popstate/refresh like filters/range/tz. Default home.
  const [view, setView] = useState<View>(initial.view);
  // Group-by (M8 1B): the home overview's aggregation dimension. Shareable →
  // URL like view; default model. Drives the home chart/donut/table only.
  const [groupBy, setGroupBy] = useState<GroupBy>(initial.groupBy);
  // Table sort (M8 1D): shared by the home + detail breakdown tables.
  // Shareable → URL; default equiv-desc (never actual cost).
  const [sort, setSort] = useState<Sort>(initial.sort);
  const [daily, setDaily] = useState<DailyRow[]>([]);
  const [byProvider, setByProvider] = useState<DailyByRow[]>([]);
  const [byHarness, setByHarness] = useState<DailyByRow[]>([]);
  const [byModel, setByModel] = useState<DailyByRow[]>([]);
  const [activity, setActivity] = useState<ActivityBucket[]>([]);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [facets, setFacets] = useState<Record<string, FacetValue[]>>({});
  const [plans, setPlans] = useState<PlanStatus[]>([]);
  // Reported usage limits (M9): display-only, fenced. Fetched on its OWN path
  // (below), never in loadRange's Promise.all — a failed/empty /api/v1/limits
  // yields the card empty-state, never a broken dashboard.
  const [limits, setLimits] = useState<LimitsSnapshot>({});
  const [health, setHealth] = useState<Health | null>(null);
  // Onboarding (Stage 2): the detect payload + whether the confirm panel is open.
  const [onboardDetect, setOnboardDetect] = useState<OnboardDetect | null>(null);
  const [showOnboard, setShowOnboard] = useState(false);
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
      fetchActivity(f, t, q, z),
    ])
      .then(([d, p, h, m, a]) => {
        if (gen !== rangeGen.current) return; // superseded by a newer request
        setDaily(d.daily ?? []);
        setByProvider(p.daily_by ?? []);
        setByHarness(h.daily_by ?? []);
        setByModel(m.daily_by ?? []);
        setActivity(a.buckets ?? []);
        setSource(d.source);
        setErr(null);
      })
      .catch((e) => {
        if (gen === rangeGen.current) setErr(String(e));
      });
  };

  useEffect(() => {
    loadRange(from, to, fq, tz);
    fetchModels().then((m) => setModels(m.models ?? [])).catch(() => {});
    fetchFacets().then((f) => setFacets(f.facets ?? {})).catch(() => {});
    fetchHealth().then(setHealth).catch(() => {});
  }, [from, to, fq, tz]);

  // Live updates: every pass refreshes the plan window meters and the facet
  // counts; the range refetches only when a touched day falls inside it (or
  // when the stream says we lost events / reconnected: touchedDays empty).
  useEffect(() => {
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

  // Reported usage limits (M9): display-only and INDEPENDENT — its own fetch
  // with its own error handling, deliberately NOT joined into loadRange's
  // Promise.all (a failed secondary fetch must never error the whole dashboard).
  // Refreshes on a ~60s interval to track the companion extension's polls; a
  // failure or empty result just leaves the cards in their waiting state.
  useEffect(() => {
    let alive = true;
    const load = () =>
      fetchLimits()
        .then((r) => {
          if (alive) setLimits(r.providers ?? {});
        })
        .catch(() => {
          /* hub limits unavailable — cards render the empty-state, page is fine */
        });
    load();
    const id = setInterval(load, 60_000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  // Onboarding (Stage 2): detect on mount; auto-offer the panel on FIRST RUN
  // (usage present, no plans declared) unless the user previously skipped. The
  // header "plans" button (below) always re-opens it.
  useEffect(() => {
    fetchOnboardDetect()
      .then((d) => {
        setOnboardDetect(d);
        const skipped =
          typeof localStorage !== "undefined" && localStorage.getItem(ONBOARD_DISMISS_KEY) === "1";
        if (shouldOfferOnboarding(d) && !skipped) setShowOnboard(true);
      })
      .catch(() => {
        /* hub /detect unavailable — no panel; dashboard is unaffected */
      });
  }, []);

  // Always re-openable (settings entry): a fresh detect, then open.
  const openOnboard = () =>
    fetchOnboardDetect()
      .then((d) => {
        setOnboardDetect(d);
        setShowOnboard(true);
      })
      .catch(() => {});
  const closeOnboard = () => {
    setShowOnboard(false);
    try {
      localStorage.setItem(ONBOARD_DISMISS_KEY, "1");
    } catch {
      /* private mode — first-run will just offer again next load */
    }
  };
  // After apply, the server already wrote prices.json + repriced; refetch so the
  // value populates without a manual reload.
  const onboardApplied = (r: OnboardApplyResult) => {
    setShowOnboard(false);
    // An all-metered apply writes no plan (added + replaced empty), so has_plans
    // stays false and the first-run trigger would re-open the panel next load.
    // The user made an explicit choice — record the dismiss so we don't nag.
    if (r.added.length === 0 && r.replaced.length === 0) {
      try {
        localStorage.setItem(ONBOARD_DISMISS_KEY, "1");
      } catch {
        /* private mode — first-run may offer again next load */
      }
    }
    fetchPlans().then((p) => setPlans(p.plans ?? [])).catch(() => {});
    loadRange(from, to, fq, tz);
    fetchOnboardDetect().then(setOnboardDetect).catch(() => {});
  };

  // The owner's stop-1 chart direction: API-EQUIVALENT cost is the
  // primary daily chart; actual out-of-pocket cost gets its own chart —
  // both truths always visible, no toggle (post-plans, actual-only is
  // honest but nearly empty).
  // Day-total + by-model tooltip on the tokens and API-equivalent charts
  // (M6 Task 3); the actual-cost chart gets the day total only (post-plans
  // it is near-empty, so a model breakdown of ~$0 adds nothing). byModel
  // is the same filtered/timezoned set the bars use.
  // These three are the detail charts — always by provider. The vllm family is
  // collapsed into one "vllm" series (M8 1H: mergeFamilies sums the members,
  // conserved) so the stacked charts lose the vllm-* wall; the by-provider
  // table below keeps every vllm-* row as the full drill-down. NO top-N here —
  // every provider family stays. The series take the brand-aware providerColor
  // resolver (M8 1F: Anthropic clay, others hashed). The by-model tooltip stays
  // on the raw byModel rows (models carry no family).
  // The in-progress day to flag as provisional across every daily chart (M8
  // 1J): today's date in the selected tz. When today isn't in the visible
  // range no bar matches and nothing is flagged. Display marker only — the
  // partial day's value stays exactly as served.
  const openDay = todayInTZ(tz);
  // Which preset button is lit: the one whose span equals the current range
  // today in tz; a custom or stale range lights none (range.ts).
  const activeRangePreset = useMemo(() => activePreset(from, to, tz), [from, to, tz]);
  const providerSeries = useMemo(() => mergeFamilies(byProvider), [byProvider]);
  const equivChart = useMemo(
    () => dailyStackedChart(providerSeries, (r) => r.costAPIEquivMicro / 1e6, (v) => `$${v.toFixed(2)}`,
      { rows: byModel, value: (r) => r.costAPIEquivMicro / 1e6 }, providerColor, openDay),
    [providerSeries, byModel, openDay],
  );
  const actualChart = useMemo(
    () => dailyStackedChart(providerSeries, (r) => r.costUSDMicro / 1e6, (v) => `$${v.toFixed(2)}`, undefined, providerColor, openDay),
    [providerSeries, openDay],
  );
  const tokenChart = useMemo(
    () => dailyStackedChart(providerSeries, totalTokens, compactTokens,
      { rows: byModel, value: totalTokens }, providerColor, openDay),
    [providerSeries, byModel, openDay],
  );
  // Click-to-filter on a chart series → filter that provider, but skip the
  // collapsed "vllm" series: it's a family aggregate, not one provider value
  // (filtering provider="vllm" would match nothing). Real providers still
  // filter; the full table is the way to filter an individual vllm-* member.
  const onProviderSeries = (seriesName: string) => {
    const raw = rawValue(seriesName);
    if (!(facets.provider ?? []).some((fv) => fv.value === raw)) return;
    toggle("provider", raw);
  };

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
  // Token split (M8 1K): the range's tokens broken into cached (cache-read) vs
  // fresh — display only, fresh + cached = total (the total is unchanged).
  // rangeTokenSums sums the four served token columns; freshCachedSplit derives
  // the binary; cacheTitle/cachedPct are the shared readout bits.
  const rangeTokenSums = useMemo(
    () =>
      daily.reduce(
        (a, r) => ({
          inputTokens: a.inputTokens + r.inputTokens,
          outputTokens: a.outputTokens + r.outputTokens,
          cacheCreationTokens: a.cacheCreationTokens + r.cacheCreationTokens,
          cacheReadTokens: a.cacheReadTokens + r.cacheReadTokens,
        }),
        { inputTokens: 0, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0 },
      ),
    [daily],
  );
  const tokenSplit = freshCachedSplit(rangeTokenSums);
  // Models in range with no price at all (footer honesty count; the by-model
  // table marks the same rows "unpriced").
  const unpricedModels = useMemo(() => countUnpriced(sumByKey(byModel)), [byModel]);
  const cachedPct = Math.round(tokenSplit.cachedShare * 100);
  // The caveat (M8 1N) is honesty copy only — the number is exactly what 1K's
  // freshCachedSplit computes from served cache-read tokens; local engine-side
  // prefix-cache reuse simply isn't in those logs, so we say so rather than infer it.
  const cacheTitle = `${tokenSplit.cached.toLocaleString()} tokens read from the prompt cache (cached); ${tokenSplit.fresh.toLocaleString()} freshly processed (fresh = input + output + cache writes); ${tokenSplit.total.toLocaleString()} total. Cached counts cache-read tokens reported in the logs; local engine-side prefix-cache reuse (e.g. vLLM) isn't reported there, so local models may read lower than their actual reuse.`;

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
  // homeColor (M8 1F): the brand-aware resolver only when the home channel IS
  // provider; for model/harness it stays plain per-entity, so the brand colour
  // never reaches a non-provider grouping.
  const homeColor = groupBy === "provider" ? providerColor : seriesColor;
  const homeChart = useMemo(
    () => dailyStackedChart(homeRollup, (r) => r.costAPIEquivMicro / 1e6, (v) => `$${v.toFixed(2)}`, undefined, homeColor, openDay),
    [homeRollup, homeColor, openDay],
  );
  const homeDonut = useMemo(() => valueDonut(homeRollup, homeColor), [homeRollup, homeColor]);
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
      <Breakdown totals={sortTotals(sumByKey(byProvider), sort)} sort={sort} onSort={onSort} rowColor={providerColor} onSelect={(raw) => toggle("provider", raw)} active={filters.provider} />
    ),
    "break-model": (
      <Breakdown totals={sortTotals(sumByKey(byModel), sort)} sort={sort} onSort={onSort} bases={modelBases} onSelect={(raw) => toggle("model", raw)} active={filters.model} />
    ),
  };

  return (
    <div className="mx-auto min-h-screen max-w-[var(--content-max)] bg-app px-6 py-5 text-primary">
      {showOnboard && onboardDetect && (
        <Onboarding detect={onboardDetect} onApplied={onboardApplied} onClose={closeOnboard} />
      )}
      <header className="mb-5 flex flex-wrap items-center gap-4">
        <div className="flex items-center gap-3">
          <span className="text-primary"><Mark size={26} /></span>
          <span className="text-[21px] font-medium tracking-[-0.01em]">tatitok</span>
          <span className="text-sm text-faint">local AI usage</span>
          {/* Live SSE signal (M9 chunk 3): relocated here from the removed detail
              "today" card so the live / disconnected indicator survives. */}
          <ClassDot
            tone={stream.connected ? "live" : "neutral"}
            pulse={stream.connected}
            title={stream.connected ? "live — SSE connected" : "stream disconnected (EventSource will retry; data refetches on reconnect)"}
          />
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
          <Button size="sm" variant="subtle" onClick={openOnboard} title="declare your subscription plans">
            plans
          </Button>
        </nav>
        <div className="ml-auto flex flex-wrap items-center gap-2 text-sm">
          {presets.map((p) => (
            <Button
              key={p.label}
              size="sm"
              active={activeRangePreset === p.label}
              aria-pressed={activeRangePreset === p.label}
              onClick={() => {
                const r = presetRange(p.label, tz);
                setFrom(r.from);
                setTo(r.to);
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
            {tzOffset && <span className="tabular-nums text-faint">{tzOffset}</span>}
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
        <FacetRail facets={facets} filters={filters} onToggle={toggle} />

        <main className="min-w-0 flex-1">
          {view === "home" ? (
            // Home (M8 1A): the calm overview — value-extracted hero, the
            // primary daily API-equivalent chart, a by-provider value donut,
            // and one ranked by-provider table. Every figure is the SAME
            // served, filtered, timezoned data the detail page uses; this is
            // re-presentation only, no counting change.
            <div className="space-y-4">
              {/* Reported-limit cards (M9 chunk 3): the claude-max / chatgpt-plus
                  PlanCards, same as detail, ABOVE the value-extracted hero. The
                  hero stays — it carries the cached% + aggregate × that live
                  nowhere else. plans is already fetched on home (no new wiring). */}
              {plans.length > 0 && (
                <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                  {plans.map((p) => (
                    <PlanCard key={p.name} plan={p} limits={limits} />
                  ))}
                </div>
              )}
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
                {/* Token fresh-vs-cached split (M8 1K): the cache-efficiency
                    headline — cached (prompt cache) vs fresh, summing to total.
                    Display only; the total is unchanged. */}
                <div className="mt-2 text-xs text-faint tabular-nums" title={cacheTitle} style={{ cursor: "help" }}>
                  {compactTokens(tokenSplit.total)} tokens · {cachedPct}% cached
                </div>
                {tokenSplit.total > 0 && (
                  <MeterBar value={tokenSplit.cached} max={tokenSplit.total} tone="positive" height={4} style={{ marginTop: 6 }} />
                )}
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

              {/* Activity heatmap (M8 1L; mockup-matched 1M): weekday × hour
                  "when you're active", over the same filtered/timezoned range.
                  The right-aligned label names the metric (tokens). */}
              <Card title="when you're active" actions={<span className="text-xs text-faint">tokens · hour × weekday</span>}>
                <Heatmap buckets={activity} />
              </Card>

              <Card title={`by ${groupBy}`}>
                {/* bases → ClassDots on model rows (M8 1E); rowColor → the
                    provider-channel swatch with Anthropic's brand (M8 1F). Each
                    is scoped to its dimension, so harness rows get neither. */}
                <Breakdown
                  totals={homeTable}
                  sort={sort}
                  onSort={onSort}
                  onSelect={onGroupSelect}
                  active={filters[groupBy]}
                  bases={groupBy === "model" ? modelBases : undefined}
                  rowColor={groupBy === "provider" ? providerColor : undefined}
                />
              </Card>
            </div>
          ) : (
            // Detail (M8 1A; M9 chunk 3): the full breakdown — the reported-limit
            // PlanCards + range totals, then the reorderable panel grid. The
            // "today" card was removed (M9); its live SSE dot moved to the header,
            // and range totals read the shared range memos.
            <>
              <section className="mb-5 grid grid-cols-1 gap-4 md:grid-cols-2">
                {plans.map((p) => (
                  <PlanCard key={p.name} plan={p} limits={limits} />
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
                    <Stat
                      value={compactTokens(tokenSplit.total)}
                      sub={<span title={cacheTitle} style={{ cursor: "help" }}>tokens · {cachedPct}% cached</span>}
                    />
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
              ? `tatitok ${health.version} · snapshot ${health.price_snapshot} · ${health.overrides} overrides / ${health.reference_models} reference models · ${unpricedModels} unpriced · db ${health.db_hash} · up ${Math.floor(health.uptime_seconds / 60)}m`
              : "hub unreachable"}
          </footer>
        </main>
      </div>
    </div>
  );
}
