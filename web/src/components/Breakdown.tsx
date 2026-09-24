// Breakdown panel: totals per key (harness / provider / model) over the
// selected range. Cost honesty rules (M3 rulings, carried to
// pixels): paid cost is prominent; a $0 row with a stored
// API-equivalent shows the equivalent as secondary text; any key with
// unpriced events gets the CLI's asterisk with a tooltip.

import { compactTokens, usd } from "../api";
import { dotClassesFor, isUnpriced, type KeyTotals, type EconClass } from "../aggregate";
import type { Sort, SortKey } from "../filters";
import ClassDot, { type DotTone } from "../ui/ClassDot";

// CLASS_DOT maps the derived economic class to the DS dot tone + an accessible
// label (M8 1E). "none" (unknown basis) is a neutral dot, never a class colour,
// so the table never fakes a class it doesn't have.
const CLASS_DOT: Record<EconClass, { tone: DotTone; label: string }> = {
  subscription: { tone: "subscription", label: "Subscription" },
  metered: { tone: "metered", label: "Metered" },
  local: { tone: "local", label: "Local" },
  free: { tone: "free", label: "Free" },
  none: { tone: "neutral", label: "Unknown" },
};

// KeyTotals + sumByKey moved to ../aggregate (M8 1B) so the pure aggregation
// is unit-testable without pulling this JSX component into Node's test runner.
// The actual row ordering is sortTotals (../aggregate) applied by the caller;
// this component only RENDERS the order and surfaces the sort affordance.

const unpricedTip = (n: number) =>
  `${n} event${n === 1 ? "" : "s"} in this range carry no resolvable price — the cost shown is a floor, not a total (same convention as the CLI's asterisk).`;

const noPriceTip =
  "no price is known for this model (not in the price snapshot, no override) — its cost and API-equivalent are unknown, not $0";

// SortHeader is a clickable numeric column header (M8 1D): clicking sorts by
// its metric and toggles direction; the arrow marks the active column. The
// "cost" column sorts by API-EQUIVALENT value — actual cost is $0 for
// plan/local/free, so it is never a useful ranking.
function SortHeader({ label, col, sort, onSort, title }: {
  label: string;
  col: SortKey;
  sort: Sort;
  onSort: (key: SortKey) => void;
  title: string;
}) {
  const active = sort.key === col;
  return (
    <button
      type="button"
      onClick={() => onSort(col)}
      title={title}
      className="font-normal tabular-nums hover:text-secondary"
      style={{ color: active ? "var(--text-secondary)" : "inherit", cursor: "pointer" }}
      aria-sort={active ? (sort.dir === "desc" ? "descending" : "ascending") : "none"}
    >
      {label}
      {active && <span aria-hidden="true">{sort.dir === "desc" ? " ▼" : " ▲"}</span>}
    </button>
  );
}

// Breakdown renders the table only — the surrounding panel chrome
// (border, title, layout controls) is the Panel's job (M6 Task 4), so
// this drops the old <section>/<h2> wrapper and is reused unchanged
// inside the panel grid.
export default function Breakdown({
  totals,
  bases,
  rowColor,
  sort,
  onSort,
  onSelect,
  active,
}: {
  totals: KeyTotals[];
  // bases (model → served cost bases): present only on model-keyed tables; it
  // drives the per-row ClassDots (M8 1E). Absent → no dots (non-model rows).
  bases?: Map<string, string[]>;
  // rowColor (M8 1F): present only on the provider-channel table — a leading
  // colour swatch matching the chart/donut (Anthropic's brand, others hashed),
  // so the three by-provider surfaces read consistently. Absent → no swatch.
  rowColor?: (raw: string) => string;
  // sort / onSort (M8 1D): the shared table sort state and the click handler;
  // the caller has already ordered `totals` via sortTotals, so this only draws
  // the header arrows and reports clicks.
  sort: Sort;
  onSort: (key: SortKey) => void;
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
          <th className="pb-2 text-right font-normal">
            <SortHeader label="tokens" col="tokens" sort={sort} onSort={onSort} title="sort by tokens" />
          </th>
          <th className="pb-2 text-right font-normal">
            <SortHeader
              label="cost"
              col="equiv"
              sort={sort}
              onSort={onSort}
              title="sort by API-equivalent value (actual cost is $0 for plan/local/free, so it is never the ranking)"
            />
          </th>
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
            <td className="max-w-48 truncate py-1.5 pr-2 text-primary" title={t.raw || t.key}>
              {/* Provider-channel swatch (M8 1F): a leading colour chip tying
                  the row to its chart/donut colour (Anthropic brand, others
                  hashed). Decorative — the provider NAME carries the meaning. */}
              {rowColor && (
                <span
                  aria-hidden="true"
                  className="mr-1.5 inline-block align-middle"
                  style={{ width: 8, height: 8, borderRadius: "999px", background: rowColor(t.raw) }}
                />
              )}
              {t.key}
              {/* ClassDots (M8 1E): economic class derived from the served
                  basis, only on model rows (bases passed) — never on aggregate
                  rows ("others"), which have no single class. */}
              {dotClassesFor(t.raw, bases).map((c) => (
                <ClassDot key={c} tone={CLASS_DOT[c].tone} size={7} title={CLASS_DOT[c].label} className="ml-1.5 align-middle" />
              ))}
            </td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{compactTokens(t.tokens)}</td>
            <td className="py-1.5 text-right tabular-nums">
              {isUnpriced(t) ? (
                /* Honesty: a model with no price at all shows "unpriced", never
                   a $0.00 that reads as free. */
                <span className="cursor-help text-tertiary" title={noPriceTip}>unpriced</span>
              ) : (
                <span className="text-primary">{usd(t.costMicro)}</span>
              )}
              {!isUnpriced(t) && t.unpriced > 0 && (
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
