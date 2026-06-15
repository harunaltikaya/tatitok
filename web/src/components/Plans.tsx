import { usd, reportedFor, type PlanStatus, type LimitsSnapshot } from "../api";
import Card from "../ui/Card";
import MeterBar from "../ui/MeterBar";
import Badge from "../ui/Badge";

// PlanCard (M5 Task 2; M9 chunk 3): one card per owner-declared plan. The card
// now LEADS with the REPORTED provider usage limits (M9) — the provider's own
// percentages + reset times, read off their page by the companion browser
// extension and fed to the hub's display-only /api/v1/limits. Those are
// display-only and never tatitok's verified numbers — the "· usage limits"
// subtitle and the per-card "as of <time>, local" freshness line already
// convey that these are live external figures, and they are kept visually
// distinct from the value-extraction line below the divider (API-equivalent
// extracted vs. the declared subscription price — tatitok's own computed
// value, unchanged).
//
// The computed 5h window meter (M5) still exists in the API and store; it is
// intentionally NOT shown in this card — the reported limits are the better,
// authoritative signal. The empty state — no limits reported for this provider
// yet, OR a provider that reports no windows (its windows arrive as null) —
// keeps the card fully functional: the value-extraction renders exactly as
// before, with a quiet "waiting for companion extension" placeholder where the
// meters go.

// resetLabel / fetchedLabel format the reported epoch-ms timestamps in the
// viewer's LOCAL time. These are absolute instants reported by the provider —
// unrelated to tatitok's day-bucketing timezone, so plain local time is right.
function resetLabel(epochMs: number): string {
  return new Date(epochMs).toLocaleString([], {
    month: "short", day: "numeric", hour: "numeric", minute: "2-digit",
  });
}
function fetchedLabel(epochMs: number): string {
  if (!epochMs) return "unknown";
  return new Date(epochMs).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

export default function PlanCard({ plan, limits }: { plan: PlanStatus; limits?: LimitsSnapshot }) {
  // reportedFor maps plan → provider bucket and normalizes its windows (null →
  // [] — see api.ts), so a provider with no windows shows the empty-state rather
  // than crashing on .length. fetchedAt is 0 (→ "unknown") when there's no bucket.
  const { windows, fetchedAt } = reportedFor(plan.name, limits);

  // Weekly cap meter, only when the owner declared a cap (M5). Computed, kept as
  // a separate progress bar; it does not render for plans without a declared cap.
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
            · usage limits
          </span>
        </span>
      }
    >
      {/* Reported usage limits (M9): the provider's OWN numbers, from the
          companion extension via the hub's display-only endpoint. Whatever
          windows the provider reports are rendered (count not hardcoded — a
          future ChatGPT message-count meter slots in here). The bar clamps the
          width and auto-escalates amber ≥90% / red ≥100%; an over-cap value
          still reads. A provider with no windows (windows: null on the wire)
          yields [] from reportedFor and falls through to the empty-state. */}
      {windows.length > 0 ? (
        <div className="space-y-2.5">
          {windows.map((win, i) => (
            <div key={`${win.label}-${i}`}>
              <div className="mb-1 flex justify-between text-xs text-tertiary tabular-nums">
                <span>{win.label}</span>
                <span>{Math.round(win.usedPercent)}% used</span>
              </div>
              <MeterBar value={win.usedPercent} max={100} />
              <div className="mt-1 text-[11px] text-faint tabular-nums">
                {win.resetAt ? `Resets ${resetLabel(win.resetAt)}` : "reset time unknown"}
              </div>
            </div>
          ))}
          <div className="pt-0.5 text-[11px] text-faint tabular-nums">
            as of {fetchedLabel(fetchedAt)}, local
          </div>
        </div>
      ) : (
        <div className="text-sm text-tertiary">usage limits — waiting for companion extension</div>
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
