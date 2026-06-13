import { useEffect, useState } from "react";
import { usd, compactTokens, type PlanStatus, type PlanWindowUsage } from "../api";
import Card from "../ui/Card";
import Stat from "../ui/Stat";
import MeterBar from "../ui/MeterBar";
import Badge from "../ui/Badge";

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
    <Card
      title={
        <span>
          {plan.name}
          <span className="ml-2" style={{ color: "var(--text-faint)" }}>
            · {durationLabel(plan.window_seconds)} windows
          </span>
        </span>
      }
    >
      {w ? (
        <>
          <Stat
            size="lg"
            value={
              <>
                {usd(w.cost_api_equiv_micro)}
                <span className="ml-1 text-sm text-tertiary">API-equiv</span>
              </>
            }
            unpriced={w.events_unpriced > 0}
            unpricedTitle={`${w.events_unpriced} events in this window carry no resolvable rates — the equivalent is a floor.`}
            sub={`${compactTokens(windowTokens(w))} tokens · ${w.events} events`}
          />
          <div className="mt-1 text-xs text-faint" title={`window ${w.start} → ${w.end} (UTC)`}>
            {remainingLabel(w.end, nowMs)}
          </div>
        </>
      ) : (
        <div className="text-sm text-tertiary">no active window — the next event opens one</div>
      )}

      {weekPct !== null && (
        <div className="mt-3.5">
          <div className="mb-1.5 flex justify-between text-xs text-tertiary tabular-nums">
            <span>week</span>
            <span>
              {usd(plan.week.cost_api_equiv_micro)} of {usd(plan.weekly_cap_equiv_micro!)} cap
            </span>
          </div>
          <MeterBar value={plan.week.cost_api_equiv_micro} max={plan.weekly_cap_equiv_micro!} />
        </div>
      )}

      {plan.monthly_price_micro !== null && (
        <div
          className="mt-3.5 flex flex-wrap items-baseline gap-1.5 border-t-[0.5px] border-hairline pt-3 text-sm tabular-nums"
          title="API-equivalent value of plan-covered usage this UTC calendar month vs. the declared subscription price."
        >
          <span style={{ color: "var(--text-positive)", fontWeight: "var(--weight-medium)" }}>
            {usd(plan.month.cost_api_equiv_micro)}
          </span>
          <span className="text-tertiary">extracted vs</span>
          <span className="text-primary">{usd(plan.monthly_price_micro)}/mo</span>
          {plan.monthly_price_micro > 0 && (
            <Badge tone="positive">
              {(plan.month.cost_api_equiv_micro / plan.monthly_price_micro).toFixed(1)}×
            </Badge>
          )}
        </div>
      )}
    </Card>
  );
}
