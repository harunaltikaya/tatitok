// Breakdown panel: totals per key (harness / provider / model) over the
// selected range. Cost honesty rules (PRD §9 / M3 rulings, carried to
// pixels): paid cost is prominent; a $0 row with a stored
// API-equivalent shows the equivalent as secondary text; any key with
// unpriced events gets the CLI's asterisk with a tooltip.

import type { DailyByRow } from "../api";
import { compactTokens, totalTokens, usd } from "../api";
import { displayValue } from "../filters";

export interface KeyTotals {
  key: string; // display form ("(none)" for the empty value)
  raw: string; // the stored value — what a click filters on
  tokens: number;
  costMicro: number;
  equivMicro: number;
  unpriced: number;
}

export function sumByKey(rows: DailyByRow[]): KeyTotals[] {
  const acc = new Map<string, KeyTotals>();
  for (const r of rows) {
    const t = acc.get(r.key) ?? {
      key: displayValue(r.key), raw: r.key,
      tokens: 0, costMicro: 0, equivMicro: 0, unpriced: 0,
    };
    t.tokens += totalTokens(r);
    t.costMicro += r.costUSDMicro;
    t.equivMicro += r.costAPIEquivMicro;
    t.unpriced += r.unpricedEvents;
    acc.set(r.key, t);
  }
  return [...acc.values()].sort((a, b) => b.costMicro - a.costMicro || b.tokens - a.tokens);
}

const unpricedTip = (n: number) =>
  `${n} event${n === 1 ? "" : "s"} in this range carry no resolvable price — the cost shown is a floor, not a total (same convention as the CLI's asterisk).`;

// Breakdown renders the table only — the surrounding panel chrome
// (border, title, layout controls) is the Panel's job (M6 Task 4), so
// this drops the old <section>/<h2> wrapper and is reused unchanged
// inside the panel grid.
export default function Breakdown({
  totals,
  bases,
  onSelect,
  active,
}: {
  totals: KeyTotals[];
  bases?: Map<string, string[]>; // model → distinct pricing bases (legend data)
  // onSelect: table rows are facet filters (M5 Task 4) — a click
  // toggles the row's RAW value in the global filter state.
  onSelect?: (raw: string) => void;
  active?: string[]; // currently filtered raw values for this facet
}) {
  return (
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
            <tr
              key={t.key}
              className={`border-t border-zinc-800/60 ${onSelect ? "cursor-pointer hover:bg-zinc-800/40" : ""} ${active?.includes(t.raw) ? "bg-sky-950/40" : ""}`}
              onClick={onSelect ? () => onSelect(t.raw) : undefined}
              title={onSelect ? "click to filter" : undefined}
            >
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
  );
}
