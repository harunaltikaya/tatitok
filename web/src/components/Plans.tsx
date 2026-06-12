import { useEffect, useState } from "react";
import { usd, compactTokens, type PlanStatus, type PlanWindowUsage } from "../api";

// Plan cards (M5 Task 2): one card per owner-declared plan — the
// rolling-window meter (current usage, time to reset, weekly cap when
// declared) and, when a monthly price is declared, the value panel:
// API-equivalent extracted vs. subscription outlay. The server computes
// windows from stamped events; this component only renders and ticks
// the countdown between SSE-driven refetches.

function windowTokens(w: PlanWindowUsage): number {
  return w.input + w.output + w.cache_write + w.cache_read;
}

function remainingLabel(end: string, nowMs: number): string {
  const ms = Date.parse(end) - nowMs;
  if (ms <= 0) return "resetting…";
  const m = Math.floor(ms / 60_000);
  const h = Math.floor(m / 60);
  return h > 0 ? `resets in ${h}h ${m % 60}m` : `resets in ${m}m`;
}

function durationLabel(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0 && m > 0) return `${h}h${m}m`;
  if (h > 0) return `${h}h`;
  return `${m}m`;
}

export default function PlanCard({ plan }: { plan: PlanStatus }) {
  // Countdown tick between refetches — display only, no data motion.
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 30_000);
    return () => clearInterval(id);
  }, []);

  const w = plan.current_window;
  const weekPct =
    plan.weekly_cap_equiv_micro && plan.weekly_cap_equiv_micro > 0
      ? Math.min(100, (plan.week.cost_api_equiv_micro / plan.weekly_cap_equiv_micro) * 100)
      : null;

  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
      <h2 className="text-sm font-semibold uppercase tracking-wider text-zinc-400">
        {plan.name}
        <span className="ml-2 font-normal normal-case text-zinc-600">
          {durationLabel(plan.window_seconds)} windows
        </span>
      </h2>

      {w ? (
        <>
          <div className="mt-2 text-2xl font-bold tabular-nums">
            {usd(w.cost_api_equiv_micro)}
            <span className="ml-1 text-sm font-normal text-zinc-500">API-equiv</span>
            {w.events_unpriced > 0 && (
              <span
                className="cursor-help text-base text-amber-400"
                title={`${w.events_unpriced} events in this window carry no resolvable rates — the equivalent is a floor.`}
              >
                *
              </span>
            )}
          </div>
          <div className="text-sm text-zinc-400 tabular-nums">
            {compactTokens(windowTokens(w))} tokens · {w.events} events
          </div>
          <div className="mt-1 text-xs text-zinc-500" title={`window ${w.start} → ${w.end} (UTC)`}>
            {remainingLabel(w.end, nowMs)}
          </div>
        </>
      ) : (
        <div className="mt-2 text-sm text-zinc-500">no active window — the next event opens one</div>
      )}

      {weekPct !== null && (
        <div className="mt-3">
          <div className="flex justify-between text-xs text-zinc-500 tabular-nums">
            <span>week</span>
            <span>
              {usd(plan.week.cost_api_equiv_micro)} of {usd(plan.weekly_cap_equiv_micro!)} cap
            </span>
          </div>
          <div className="mt-1 h-1.5 rounded bg-zinc-800">
            <div
              className={`h-1.5 rounded ${weekPct >= 90 ? "bg-amber-400" : "bg-emerald-500"}`}
              style={{ width: `${weekPct}%` }}
            />
          </div>
        </div>
      )}

      {plan.monthly_price_micro !== null && (
        <div
          className="mt-3 border-t border-zinc-800 pt-2 text-sm tabular-nums"
          title="API-equivalent value of plan-covered usage this UTC calendar month vs. the declared subscription price."
        >
          <span className="font-semibold">{usd(plan.month.cost_api_equiv_micro)}</span>
          <span className="text-zinc-500"> extracted vs </span>
          <span className="font-semibold">{usd(plan.monthly_price_micro)}</span>
          <span className="text-zinc-500">/mo</span>
          {plan.monthly_price_micro > 0 && (
            <span className="ml-2 text-xs text-zinc-500">
              {(plan.month.cost_api_equiv_micro / plan.monthly_price_micro).toFixed(1)}×
            </span>
          )}
        </div>
      )}
    </div>
  );
}
