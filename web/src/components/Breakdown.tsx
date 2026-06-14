// Breakdown panel: totals per key (harness / provider / model) over the
// selected range. Cost honesty rules (PRD §9 / M3 rulings, carried to
// pixels): paid cost is prominent; a $0 row with a stored
// API-equivalent shows the equivalent as secondary text; any key with
// unpriced events gets the CLI's asterisk with a tooltip.

import { compactTokens, usd } from "../api";
import type { KeyTotals } from "../aggregate";
import Badge from "../ui/Badge";

// KeyTotals + sumByKey moved to ../aggregate (M8 1B) so the pure aggregation
// is unit-testable without pulling this JSX component into Node's test runner.

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
        <tr className="text-left text-xs text-tertiary">
          <th className="pb-2 font-normal">key</th>
          <th className="pb-2 text-right font-normal">tokens</th>
          <th className="pb-2 text-right font-normal">cost</th>
        </tr>
      </thead>
      <tbody>
        {totals.length === 0 && (
          <tr>
            <td colSpan={3} className="py-2 text-tertiary">
              no data in range
            </td>
          </tr>
        )}
        {totals.map((t) => (
          <tr
            key={t.key}
            className={`border-t-[0.5px] border-hairline ${onSelect ? "cursor-pointer hover:bg-[var(--surface-hover)]" : ""}`}
            style={active?.includes(t.raw) ? { background: "var(--accent-soft)" } : undefined}
            onClick={onSelect ? () => onSelect(t.raw) : undefined}
            title={onSelect ? "click to filter" : undefined}
          >
            <td className="max-w-48 truncate py-1.5 pr-2 text-primary" title={t.key}>
              {t.key}
              {bases?.get(t.key)?.map((b) => (
                <Badge key={b} tone="tag" className="ml-1.5">
                  {b}
                </Badge>
              ))}
            </td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{compactTokens(t.tokens)}</td>
            <td className="py-1.5 text-right tabular-nums">
              <span className="text-primary">{usd(t.costMicro)}</span>
              {t.unpriced > 0 && (
                <span className="cursor-help text-warning" title={unpricedTip(t.unpriced)}>
                  *
                </span>
              )}
              {t.costMicro === 0 && t.equivMicro > 0 && (
                <div className="text-xs text-tertiary">≈ {usd(t.equivMicro)} API-equiv</div>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
