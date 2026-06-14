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
import type { DailyByRow, FacetValue, ActivityBucket } from "./api.ts";
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

// mergeFamilies collapses families AND sums the rows that then share
// (date, key), so each family is ONE row per day (its members summed). Unlike
// rollupRows it does NOT truncate to top-N — every family stays — and unlike
// bare collapseFamilies it merges the duplicate (date, key) rows the relabel
// creates, so a chart that keys cells by (date, key) shows the family SUM
// rather than one arbitrary member. The detail by-provider charts feed this
// (M8 1H): the vllm-* wall folds into one "vllm" series = Σ its members
// (conserved), every other family intact. Pure — the input rows (the full
// table's data) are never mutated.
export function mergeFamilies(rows: DailyByRow[]): DailyByRow[] {
  const merged = new Map<string, DailyByRow>(); // `${date}|${family}` → summed row
  for (const r of collapseFamilies(rows)) {
    const id = `${r.date}|${r.key}`;
    const cur = merged.get(id);
    if (!cur) {
      merged.set(id, { ...r });
      continue;
    }
    cur.inputTokens += r.inputTokens;
    cur.outputTokens += r.outputTokens;
    cur.cacheCreationTokens += r.cacheCreationTokens;
    cur.cacheReadTokens += r.cacheReadTokens;
    cur.reasoningTokens += r.reasoningTokens;
    cur.costUSDMicro += r.costUSDMicro;
    cur.costAPIEquivMicro += r.costAPIEquivMicro;
    cur.unpricedEvents += r.unpricedEvents;
  }
  return [...merged.values()];
}

// chartCells builds the stacked daily chart's (date, key) → value map. It SUMS
// rows that share a (date, key) cell: a collapsed family ("vllm") or a folded
// "others" arrives as SEVERAL rows on the same day after rollupRows/
// mergeFamilies relabel their keys, so the bar must show the SUM of its
// members — not whichever row was written last (the chart≠donut bug). Pure, so
// the chart builder and a per-cell test share it; the donut already summed per
// key (valueDonut), this brings the daily bars in line.
export function chartCells(rows: DailyByRow[], value: (r: DailyByRow) => number): Map<string, number> {
  const byCell = new Map<string, number>();
  for (const r of rows) {
    const k = `${r.date}|${r.key}`;
    byCell.set(k, (byCell.get(k) ?? 0) + value(r));
  }
  return byCell;
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

// railItems is the facet rail's display structure (M8 1G): the SAME family
// collapse the main view uses (familyOf) folds vllm-* into one "vllm" group,
// then — for long dims — the tail past top-N folds into an "others" group. Both
// are expandable GROUPS (the rail renders the members when open). A pure
// regroup/relabel of the served facet counts: a family group's count is the sum
// of its members, and top-N + others = the dimension total (conserved — the
// rail shows no number the inventory didn't). Group headers are expand toggles
// only; the leaves (top-N entries, family members, others-tail) each filter by
// their exact value.
export type RailItem =
  | { kind: "leaf"; value: string; events: number }
  | { kind: "family" | "others"; key: string; label: string; events: number; members: { value: string; events: number }[] };

export function railItems(values: FacetValue[], rolled: boolean, topN: number): RailItem[] {
  // 1. Group by family (vllm-* → "vllm"), preserving first-seen order.
  const byFam = new Map<string, FacetValue[]>();
  const order: string[] = [];
  for (const v of values) {
    const fam = familyOf(v.value);
    const g = byFam.get(fam);
    if (g) g.push(v);
    else {
      byFam.set(fam, [v]);
      order.push(fam);
    }
  }
  // 2. A family with >1 member (or a lone member relabelled to the family) is a
  //    collapsible group; everything else is a plain leaf (no-op collapse).
  let items: RailItem[] = order.map((fam) => {
    const members = byFam.get(fam)!;
    const events = members.reduce((s, m) => s + m.events, 0);
    const collapsible = members.length > 1 || members[0].value !== fam;
    return collapsible
      ? { kind: "family", key: fam, label: fam, events, members: members.map((m) => ({ value: m.value, events: m.events })) }
      : { kind: "leaf", value: members[0].value, events };
  });
  // 3. Biggest first (stable tiebreak on the display key).
  const keyOf = (i: RailItem) => (i.kind === "leaf" ? i.value : i.key);
  items.sort((a, b) => b.events - a.events || (keyOf(a) < keyOf(b) ? -1 : keyOf(a) > keyOf(b) ? 1 : 0));
  // 4. Long dims fold the tail past top-N into one "others" group; its members
  //    are the tail flattened to leaves (each still filters by its exact value).
  if (rolled && items.length > topN) {
    const tail = items.slice(topN);
    const members = tail.flatMap((i) => (i.kind === "leaf" ? [{ value: i.value, events: i.events }] : i.members));
    const events = tail.reduce((s, i) => s + i.events, 0);
    items = [...items.slice(0, topN), { kind: "others", key: "__others__", label: "others", events, members }];
  }
  return items;
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

// --- M8 1E: economic class from the served basis -----------------------------
// Frontend-only: the class is DERIVED from the per-model cost basis already
// served at /api/v1/meta/models (ModelInfo.costBasis — the served form of
// core/event.go's api_price | plan_included | local | free | unknown). No new
// served field, no inference; "none" is shown as a neutral dot, never faked
// into a class colour.

export type EconClass = "subscription" | "metered" | "local" | "free" | "none";

export function basisToClass(basis: string): EconClass {
  switch (basis) {
    case "api_price":
      return "metered";
    case "plan_included":
      return "subscription";
    case "local":
      return "local";
    case "free":
      return "free";
    default: // "unknown", "", or anything this build doesn't recognise
      return "none";
  }
}

// dotClassesFor returns the distinct economic classes to mark a row with: a
// model row carries its served basis (usually one, occasionally several across
// providers) mapped to class; an aggregate row ("others", or any key with no
// served basis) carries NONE — a mixed bucket has no single class. bases is the
// model→basis map App derives from the served inventory; absent → no dots, so
// only model-keyed tables (which pass it) ever show ClassDots.
export function dotClassesFor(key: string, bases?: Map<string, string[]>): EconClass[] {
  const list = bases?.get(key);
  if (!list || list.length === 0) return [];
  const seen = new Set<EconClass>();
  const out: EconClass[] = [];
  for (const basis of list) {
    const c = basisToClass(basis);
    if (!seen.has(c)) {
      seen.add(c);
      out.push(c);
    }
  }
  return out;
}

// --- M8 1F: provider brand colour --------------------------------------------
// The contained exception to "colour = class": on the PROVIDER channel a
// provider may take its brand colour instead of a per-entity hashed hue. Values
// are concrete hexes (ECharts itemStyle can't read CSS vars) mirroring the DS
// --brand-* tokens. Anthropic's CLAY end (#cc785c) was chosen to clear both
// metered-amber (#f5b547) and danger coral-red (#f0726f) — a brighter orange
// would collide with amber. Extensible: OpenAI green etc. land later, each
// tuned against the class palette then.
const BRAND_COLORS: Record<string, string> = {
  anthropic: "#cc785c",
};

// brandColorFor returns a provider's brand hex, or null to fall back to the
// per-entity colour. The CALLER applies this only on the provider dimension, so
// the brand never leaks into the model or harness groupings.
export function brandColorFor(provider: string): string | null {
  return BRAND_COLORS[provider] ?? null;
}

// --- M8 1L: activity heatmap grid -------------------------------------------

// activityGrid densifies the served ≤168 buckets into a 7×24 matrix of the
// chosen metric: grid[weekday][hour] (weekday Go-style 0=Sunday..6=Saturday;
// the renderer reorders Monday-first), 0 where a bucket is absent. A pure
// densify — Σ grid = Σ buckets (conserved), no recount.
export function activityGrid(buckets: ActivityBucket[], metric: (b: ActivityBucket) => number): number[][] {
  const grid = Array.from({ length: 7 }, () => new Array<number>(24).fill(0));
  for (const b of buckets) {
    if (b.weekday >= 0 && b.weekday < 7 && b.hour >= 0 && b.hour < 24) {
      grid[b.weekday][b.hour] += metric(b);
    }
  }
  return grid;
}
