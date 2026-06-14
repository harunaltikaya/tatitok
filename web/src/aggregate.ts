// Client-side range aggregation (M5/M6, extracted to a pure module at M8
// 1B). sumByKey folds the served per-(day, key) rows into one ranked total
// per key over the whole range — the breakdown tables' source, and (1B) the
// group-by-driven home chart/donut/table all read it. Kept framework-free so
// Node's test runner can pin its conservation property (the per-key totals
// sum to the same grand total regardless of which dimension keys the rows).
// Display-only: it sums already-served numbers, it never re-counts.

// Explicit .ts extensions: this module has runtime (value) imports and is
// loaded directly by Node's test runner (groupby.test.ts), whose ESM resolver
// needs the extension — unlike the bundle, where Vite resolves extensionless.
// tsconfig allowImportingTsExtensions makes tsc accept them.
import type { DailyByRow, FacetValue } from "./api.ts";
import { totalTokens } from "./api.ts";
import { displayValue, type Sort } from "./filters.ts";

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
  // A deterministic, value-based default order (API-equiv desc) — display
  // ranking is sortTotals' job (M8 1D); this is never actual-cost ordering.
  return [...acc.values()].sort((a, b) => b.equivMicro - a.equivMicro || b.tokens - a.tokens);
}

// --- M8 1C: home-only rollup (family collapse + top-N/others) ----------------
// These are DISPLAY aggregations layered over the same served daily_by rows —
// they relabel/fold keys, never re-count, so every total is conserved (proven
// in rollup.test.ts). The detail page keeps the full per-entity breakdown; only
// the home overview rolls up.

// HOME_TOP_N drives the home chart/donut/table (top-5 + others); FILTER_TOP_N
// the left-pane model/project lists (top-10 + others, owner ruling).
export const HOME_TOP_N = 5;
export const FILTER_TOP_N = 10;

// OTHERS_KEY is the synthetic bucket the remainder folds into. It is NOT a real
// facet value — the home click-to-filter guards against it (and collapsed
// families) so a rolled-up bucket never applies a bogus single-value filter.
export const OTHERS_KEY = "others";

// FAMILY_PREFIXES collapse related providers to one family bucket on the home
// overview (vllm-0.6, vllm-0.7 → "vllm"). Conservative: only a declared prefix
// as a whole token or "<prefix>-…" collapses, so model names like
// "claude-sonnet-4-6" are never split.
const FAMILY_PREFIXES = ["vllm"];

export function familyOf(key: string): string {
  for (const p of FAMILY_PREFIXES) {
    if (key === p || key.startsWith(p + "-")) return p;
  }
  return key;
}

// collapseFamilies relabels each row to its family (display-only; preserves
// every number → the family bucket equals the sum of its members).
export function collapseFamilies(rows: DailyByRow[]): DailyByRow[] {
  return rows.map((r) => {
    const fam = familyOf(r.key);
    return fam === r.key ? r : { ...r, key: fam };
  });
}

// rollupRows collapses families, then keeps the top-N keys by API-equivalent
// value (the home's primary metric; tokens then key name as tiebreaks, for a
// stable pick) and folds the rest into OTHERS_KEY. A pure relabel, so
// top-N + others = grand total for every measure. Feed the result to the same
// dailyStackedChart / valueDonut / sumByKey the detail page uses — the rollup
// is the only change between the two presentations.
export function rollupRows(rows: DailyByRow[], topN: number): DailyByRow[] {
  const collapsed = collapseFamilies(rows);
  const equiv = new Map<string, number>();
  const tokens = new Map<string, number>();
  for (const r of collapsed) {
    equiv.set(r.key, (equiv.get(r.key) ?? 0) + r.costAPIEquivMicro);
    tokens.set(r.key, (tokens.get(r.key) ?? 0) + totalTokens(r));
  }
  if (equiv.size <= topN) return collapsed; // nothing to fold
  const ranked = [...equiv.keys()].sort((a, b) =>
    (equiv.get(b)! - equiv.get(a)!) ||
    (tokens.get(b)! - tokens.get(a)!) ||
    (a < b ? -1 : 1));
  const top = new Set(ranked.slice(0, topN));
  return collapsed.map((r) => (top.has(r.key) ? r : { ...r, key: OTHERS_KEY }));
}

// topFacets is the left-pane analog (M8 1C): a dimension's values by event
// count desc, top n kept, the remainder summarized as a single non-interactive
// "others" tally (how many values folded + their total events). shown events +
// othersEvents = the dimension's total events (conserved).
export function topFacets(values: FacetValue[], n: number): { shown: FacetValue[]; othersValues: number; othersEvents: number } {
  const sorted = [...values].sort((a, b) => b.events - a.events || (a.value < b.value ? -1 : 1));
  const shown = sorted.slice(0, n);
  const rest = sorted.slice(n);
  return { shown, othersValues: rest.length, othersEvents: rest.reduce((s, v) => s + v.events, 0) };
}

// --- M8 1D: table sort -------------------------------------------------------

// bucketRank tiers rows for the table sort: real entities (0) rank by the
// metric; collapsed family buckets (1) sit below them; "others" (2) is always
// dead last. So an aggregate never out-ranks a real entity, and the remainder
// stays at the bottom regardless of sort direction.
function bucketRank(key: string): number {
  if (key === OTHERS_KEY) return 2;
  if (FAMILY_PREFIXES.includes(key)) return 1;
  return 0;
}

// sortTotals ranks the breakdown rows for display (M8 1D): by the chosen metric
// (API-equiv or tokens) and direction, with aggregates pinned to the bottom
// (bucketRank), key name as a stable tiebreak. A pure reorder — no number
// changes, conservation untouched. The home and detail tables share one Sort
// (URL state); the default is equiv-desc, never actual cost (which is $0 for
// plan/local/free, so ranking by it is meaningless).
export function sortTotals(totals: KeyTotals[], sort: Sort): KeyTotals[] {
  const metric = sort.key === "tokens" ? (t: KeyTotals) => t.tokens : (t: KeyTotals) => t.equivMicro;
  const sign = sort.dir === "asc" ? 1 : -1;
  return [...totals].sort((a, b) => {
    const ra = bucketRank(a.raw);
    const rb = bucketRank(b.raw);
    if (ra !== rb) return ra - rb;
    return sign * (metric(a) - metric(b)) || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);
  });
}
