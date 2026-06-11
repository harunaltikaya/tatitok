// Breakdown panel: totals per key (harness / provider / model) over the
// selected range. Cost honesty rules (PRD §9 / M3 rulings, carried to
// pixels): paid cost is prominent; a $0 row with a stored
// API-equivalent shows the equivalent as secondary text; any key with
// unpriced events gets the CLI's asterisk with a tooltip.

import type { DailyByRow } from "../api";
import { compactTokens, totalTokens, usd } from "../api";

export interface KeyTotals {
  key: string;
  tokens: number;
  costMicro: number;
  equivMicro: number;
  unpriced: number;
}

export function sumByKey(rows: DailyByRow[]): KeyTotals[] {
  const acc = new Map<string, KeyTotals>();
  for (const r of rows) {
    const k = r.key === "" ? "(none)" : r.key;
    const t = acc.get(k) ?? { key: k, tokens: 0, costMicro: 0, equivMicro: 0, unpriced: 0 };
    t.tokens += totalTokens(r);
    t.costMicro += r.costUSDMicro;
    t.equivMicro += r.costAPIEquivMicro;
    t.unpriced += r.unpricedEvents;
    acc.set(k, t);
  }
  return [...acc.values()].sort((a, b) => b.costMicro - a.costMicro || b.tokens - a.tokens);
}

const unpricedTip = (n: number) =>
  `${n} event${n === 1 ? "" : "s"} in this range carry no resolvable price — the cost shown is a floor, not a total (same convention as the CLI's asterisk).`;

export default function Breakdown({
  title,
  totals,
  bases,
}: {
  title: string;
  totals: KeyTotals[];
  bases?: Map<string, string[]>; // model → distinct pricing bases (legend data)
}) {
  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-zinc-400">{title}</h2>
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-zinc-500">
            <th className="pb-1 font-normal">key</th>
            <th className="pb-1 text-right font-normal">tokens</th>
            <th className="pb-1 text-right font-normal">cost</th>
          </tr>
        </thead>
        <tbody>
          {totals.length === 0 && (
            <tr>
              <td colSpan={3} className="py-2 text-zinc-500">
                no data in range
              </td>
            </tr>
          )}
          {totals.map((t) => (
            <tr key={t.key} className="border-t border-zinc-800/60">
              <td className="max-w-48 truncate py-1.5 pr-2 text-zinc-200" title={t.key}>
                {t.key}
                {bases?.get(t.key)?.map((b) => (
                  <span
                    key={b}
                    className="ml-1.5 rounded bg-zinc-800 px-1 py-0.5 text-[10px] text-zinc-400"
                  >
                    {b}
                  </span>
                ))}
              </td>
              <td className="py-1.5 text-right tabular-nums text-zinc-300">{compactTokens(t.tokens)}</td>
              <td className="py-1.5 text-right tabular-nums">
                <span className="text-zinc-100">{usd(t.costMicro)}</span>
                {t.unpriced > 0 && (
                  <span className="cursor-help text-amber-400" title={unpricedTip(t.unpriced)}>
                    *
                  </span>
                )}
                {t.costMicro === 0 && t.equivMicro > 0 && (
                  <div className="text-xs text-zinc-500">≈ {usd(t.equivMicro)} API-equiv</div>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
