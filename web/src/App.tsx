import { useEffect, useMemo, useRef, useState } from "react";
import type { EChartsOption } from "echarts";
import {
  fetchDaily,
  fetchDailyBy,
  fetchHealth,
  fetchModels,
  fetchTotals,
  usd,
  compactTokens,
  totalTokens,
  utcDaysAgo,
  utcToday,
  type DailyByRow,
  type DailyRow,
  type Health,
  type ModelInfo,
  type Totals,
} from "./api";
import { useStream } from "./useStream";
import Chart from "./components/Chart";
import Breakdown, { sumByKey } from "./components/Breakdown";

const presets = [
  { label: "7d", days: 7 },
  { label: "30d", days: 30 },
  { label: "90d", days: 90 },
  { label: "all", days: 0 },
] as const;

const axisText = { color: "#a1a1aa", fontSize: 11 };

function stackedByProvider(
  rows: DailyByRow[],
  value: (r: DailyByRow) => number,
  fmt: (v: number) => string,
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
      valueFormatter: (v) => fmt(Number(v ?? 0)),
    },
    xAxis: { type: "category", data: days, axisLabel: axisText },
    yAxis: { type: "value", axisLabel: { ...axisText, formatter: (v: number) => fmt(v) }, splitLine: { lineStyle: { color: "#27272a" } } },
    series: providers.map((p) => ({
      name: p === "" ? "(none)" : p,
      type: "bar",
      stack: "total",
      emphasis: { focus: "series" },
      data: days.map((d) => byCell.get(`${d}|${p}`) ?? 0),
    })),
  };
}

export default function App() {
  const [from, setFrom] = useState(utcDaysAgo(29));
  const [to, setTo] = useState(utcToday());
  const [daily, setDaily] = useState<DailyRow[]>([]);
  const [byProvider, setByProvider] = useState<DailyByRow[]>([]);
  const [byHarness, setByHarness] = useState<DailyByRow[]>([]);
  const [byModel, setByModel] = useState<DailyByRow[]>([]);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [today, setToday] = useState<Totals | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const stream = useStream();
  const lastRangeFetch = useRef(0);

  const loadRange = (f: string, t: string) => {
    Promise.all([
      fetchDaily(f, t),
      fetchDailyBy("provider", f, t),
      fetchDailyBy("harness", f, t),
      fetchDailyBy("model", f, t),
    ])
      .then(([d, p, h, m]) => {
        setDaily(d.daily ?? []);
        setByProvider(p.daily_by ?? []);
        setByHarness(h.daily_by ?? []);
        setByModel(m.daily_by ?? []);
        setErr(null);
      })
      .catch((e) => setErr(String(e)));
  };

  useEffect(() => {
    loadRange(from, to);
    fetchModels().then((m) => setModels(m.models ?? [])).catch(() => {});
    fetchHealth().then(setHealth).catch(() => {});
  }, [from, to]);

  // Live updates: every pass refreshes the today panel; the range
  // refetches only when a touched day falls inside it (or when the
  // stream says we lost events / reconnected: touchedDays empty).
  useEffect(() => {
    fetchTotals("today").then((t) => setToday(t.totals)).catch(() => {});
    if (stream.bump === 0 || stream.bump === lastRangeFetch.current) return;
    const touched = stream.touchedDays;
    const inRange = touched.length === 0 || touched.some((d) => d >= from && d <= to);
    if (inRange) {
      lastRangeFetch.current = stream.bump;
      loadRange(from, to);
    }
  }, [stream.bump]); // eslint-disable-line react-hooks/exhaustive-deps

  const costChart = useMemo(
    () => stackedByProvider(byProvider, (r) => r.costUSDMicro / 1e6, (v) => `$${v.toFixed(2)}`),
    [byProvider],
  );
  const tokenChart = useMemo(
    () => stackedByProvider(byProvider, totalTokens, compactTokens),
    [byProvider],
  );

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

  return (
    <div className="min-h-screen bg-zinc-950 px-6 py-5 text-zinc-100">
      <header className="mb-5 flex flex-wrap items-center gap-4">
        <h1 className="text-xl font-bold tracking-tight">
          tatitok
          <span className="ml-2 text-sm font-normal text-zinc-500">local AI usage</span>
        </h1>
        <div className="ml-auto flex items-center gap-2 text-sm">
          {presets.map((p) => (
            <button
              key={p.label}
              className="rounded-lg border border-zinc-800 px-2.5 py-1 text-zinc-300 hover:bg-zinc-800"
              onClick={() => {
                setFrom(p.days === 0 ? "1970-01-01" : utcDaysAgo(p.days - 1));
                setTo(utcToday());
              }}
            >
              {p.label}
            </button>
          ))}
          <input
            type="date"
            id="range-from"
            name="range-from"
            aria-label="range start (UTC day)"
            value={from}
            onChange={(e) => e.target.value && setFrom(e.target.value)}
            className="rounded-lg border border-zinc-800 bg-zinc-900 px-2 py-1 text-zinc-300"
          />
          <span className="text-zinc-600">→</span>
          <input
            type="date"
            id="range-to"
            name="range-to"
            aria-label="range end (UTC day)"
            value={to}
            onChange={(e) => e.target.value && setTo(e.target.value)}
            className="rounded-lg border border-zinc-800 bg-zinc-900 px-2 py-1 text-zinc-300"
          />
          <span className="cursor-help text-xs text-zinc-500" title="Rollup-backed days are UTC buckets — same numbers as `tatitok stats --daily --timezone UTC`. Exact local-timezone serving stays in the CLI this milestone.">
            days are UTC
          </span>
        </div>
      </header>

      {err && (
        <div className="mb-4 rounded-lg border border-red-900 bg-red-950/40 px-3 py-2 text-sm text-red-300">
          {err}
        </div>
      )}

      <section className="mb-5 grid grid-cols-1 gap-4 md:grid-cols-3">
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <h2 className="text-sm font-semibold uppercase tracking-wider text-zinc-400">
            today
            <span
              className={`ml-2 inline-block h-2 w-2 rounded-full ${stream.connected ? "bg-emerald-400" : "bg-zinc-600"}`}
              title={stream.connected ? "live — SSE connected" : "stream disconnected (EventSource will retry; data refetches on reconnect)"}
            />
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
          {stream.lastPass && (
            <div className="mt-2 text-xs text-zinc-500">
              last pass #{stream.lastPass.pass}:{" "}
              {stream.lastPass.harnesses
                .map((h) => `${h.harness} +${h.new}${h.replaced ? ` ~${h.replaced}` : ""}`)
                .join(", ")}
            </div>
          )}
        </div>
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4 md:col-span-2">
          <h2 className="text-sm font-semibold uppercase tracking-wider text-zinc-400">range totals</h2>
          <div className="mt-2 flex flex-wrap gap-6 tabular-nums">
            <div>
              <div className="text-2xl font-bold">
                {usd(daily.reduce((n, r) => n + r.costUSDMicro, 0))}
                {rangeUnpriced > 0 && (
                  <span className="cursor-help text-base text-amber-400" title={`${rangeUnpriced} events in range carry no resolvable price — cost is a floor (the CLI's asterisk).`}>*</span>
                )}
              </div>
              <div className="text-xs text-zinc-500">cost ({from} → {to})</div>
            </div>
            <div>
              <div className="text-2xl font-bold">{compactTokens(daily.reduce((n, r) => n + totalTokens(r), 0))}</div>
              <div className="text-xs text-zinc-500">tokens</div>
            </div>
            <div>
              <div className="text-2xl font-bold">{compactTokens(daily.reduce((n, r) => n + r.reasoningTokens, 0))}</div>
              <div className="text-xs text-zinc-500">reasoning</div>
            </div>
            <div>
              <div className="text-2xl font-bold">{daily.length}</div>
              <div className="text-xs text-zinc-500">active days</div>
            </div>
          </div>
        </div>
      </section>

      <section className="mb-5 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <h2 className="mb-2 text-sm font-semibold uppercase tracking-wider text-zinc-400">daily cost (by provider)</h2>
          <Chart option={costChart} />
        </div>
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <h2 className="mb-2 text-sm font-semibold uppercase tracking-wider text-zinc-400">daily tokens (by provider)</h2>
          <Chart option={tokenChart} />
        </div>
      </section>

      <section className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Breakdown title="by harness" totals={sumByKey(byHarness)} />
        <Breakdown title="by provider" totals={sumByKey(byProvider)} />
        <Breakdown title="by model" totals={sumByKey(byModel)} bases={modelBases} />
      </section>

      <footer className="mt-6 text-xs text-zinc-600">
        {health
          ? `tatitok ${health.version} · snapshot ${health.price_snapshot} · ${health.overrides} overrides / ${health.reference_models} reference models · db ${health.db_hash} · up ${Math.floor(health.uptime_seconds / 60)}m`
          : "hub unreachable"}
      </footer>
    </div>
  );
}
