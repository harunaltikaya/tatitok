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
import type { DailyByRow, FacetValue, ActivityBucket, ModelInfo, SessionRow } from "./api.ts";
import { totalTokens, zoneTimeLabel } from "./api.ts";
import { displayValue, projectLabel, sessionLabel, type Sort } from "./filters.ts";

export interface KeyTotals {
  key: string; // display form ("(none)" for the empty value)
  raw: string; // the stored value — what a click filters on
  tokens: number;
  costMicro: number;
  equivMicro: number;
  unpriced: number;
  rated: number; // events that resolved a rate (see DailyByRow.ratedEvents)
}

export function sumByKey(rows: DailyByRow[]): KeyTotals[] {
  const acc = new Map<string, KeyTotals>();
  for (const r of rows) {
    const t = acc.get(r.key) ?? {
      key: displayValue(r.key), raw: r.key,
      tokens: 0, costMicro: 0, equivMicro: 0, unpriced: 0, rated: 0,
    };
    t.tokens += totalTokens(r);
    t.costMicro += r.costUSDMicro;
    t.equivMicro += r.costAPIEquivMicro;
    t.unpriced += r.unpricedEvents;
    t.rated += r.ratedEvents ?? 0;
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

// LOCAL_KEY is the one family bucket: every provider whose served events are
// all cost_basis "local" (the box's own vllm-*/sglang-*/robotlab-*/*-local
// endpoints) folds into it on the provider channel. It replaced the vllm-*
// name-prefix family: the basis is DECIDED FROM THE SERVED DATA (the
// /api/v1/meta/models inventory carries costBasis per (provider, model)), so
// no name matching happens in the frontend — a cloud provider never lands
// here by spelling, and a local endpoint named anything at all does.
export const LOCAL_KEY = "local";

// Family membership: the set of keys to fold into LOCAL_KEY on ONE dimension.
// The caller derives it with localProviders() / localModels() and passes the
// matching set on the provider / model channel — the harness channel gets
// none, and with no set every key is its own family (identity).
export type Locals = ReadonlySet<string>;

// localKeys: the values of one inventory dimension (keyOf: provider or model
// name) EVERY served (provider, model) row of which has costBasis "local". A
// value with any other basis (api_price, free, plan_included, unknown, …) is
// not purely local and stays its own entry — a mixed bucket would misstate
// its non-local part. The ONE grouping rule; the per-dimension helpers below
// only pick the key. Pure over the inventory.
export function localKeys(models: ModelInfo[], keyOf: (m: ModelInfo) => string): Set<string> {
  const bases = new Map<string, Set<string>>();
  for (const m of models) {
    const key = keyOf(m);
    let b = bases.get(key);
    if (!b) {
      b = new Set();
      bases.set(key, b);
    }
    b.add(m.costBasis);
  }
  const out = new Set<string>();
  for (const [key, b] of bases) {
    if (b.size === 1 && b.has("local")) out.add(key);
  }
  return out;
}

// localProviders / localModels: the provider and model channels' local sets.
export function localProviders(models: ModelInfo[]): Set<string> {
  return localKeys(models, (m) => m.provider);
}

export function localModels(models: ModelInfo[]): Set<string> {
  return localKeys(models, (m) => m.model);
}

export function familyOf(key: string, locals?: Locals): string {
  return locals?.has(key) ? LOCAL_KEY : key;
}

// collapseFamilies relabels each row to its family (display-only; preserves
// every number → the family bucket equals the sum of its members).
export function collapseFamilies(rows: DailyByRow[], locals?: Locals): DailyByRow[] {
  return rows.map((r) => {
    const fam = familyOf(r.key, locals);
    return fam === r.key ? r : { ...r, key: fam };
  });
}

// mergeFamilies collapses families AND sums the rows that then share
// (date, key), so each family is ONE row per day (its members summed). Unlike
// rollupRows it does NOT truncate to top-N — every family stays — and unlike
// bare collapseFamilies it merges the duplicate (date, key) rows the relabel
// creates, so a chart that keys cells by (date, key) shows the family SUM
// rather than one arbitrary member. The detail by-provider charts feed this
// (M8 1H): the local-basis wall folds into one "local" series = Σ its
// members (conserved), every cloud provider intact. Pure — the input rows
// (the full table's data) are never mutated.
export function mergeFamilies(rows: DailyByRow[], locals?: Locals): DailyByRow[] {
  const merged = new Map<string, DailyByRow>(); // `${date}|${family}` → summed row
  for (const r of collapseFamilies(rows, locals)) {
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
    cur.ratedEvents = (cur.ratedEvents ?? 0) + (r.ratedEvents ?? 0);
  }
  return [...merged.values()];
}

// --- unpriced keys (honesty) --------------------------------------------------
// A key none of whose events resolved a rate has NO price: its $0.00 is
// unknown, not zero. isUnpriced marks such rows (the table shows "unpriced"
// instead of a dollar figure); countUnpriced feeds the footer's "N unpriced".
// A key with any rated event keeps its figures (a partial gap is the asterisk).
export function isUnpriced(t: KeyTotals): boolean {
  return t.tokens > 0 && t.rated === 0 && t.costMicro === 0 && t.equivMicro === 0;
}

export function countUnpriced(totals: KeyTotals[]): number {
  return totals.filter(isUnpriced).length;
}

// chartCells builds the stacked daily chart's (date, key) → value map. It SUMS
// rows that share a (date, key) cell: a collapsed family ("local") or a folded
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
export function rollupRows(rows: DailyByRow[], topN: number, locals?: Locals): DailyByRow[] {
  const collapsed = collapseFamilies(rows, locals);
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
// collapse the main view uses (familyOf) folds the local-basis providers into
// one "local" group (locals — passed on the provider dim only), then — for long dims — the tail past top-N folds into an "others" group. Both
// are expandable GROUPS (the rail renders the members when open). A pure
// regroup/relabel of the served facet counts: a family group's count is the sum
// of its members, and top-N + others = the dimension total (conserved — the
// rail shows no number the inventory didn't). Group headers are expand toggles
// only; the leaves (top-N entries, family members, others-tail) each filter by
// their exact value.
export type RailItem =
  | { kind: "leaf"; value: string; events: number }
  | { kind: "family" | "others"; key: string; label: string; events: number; members: { value: string; events: number }[] };

export function railItems(values: FacetValue[], rolled: boolean, topN: number, locals?: Locals): RailItem[] {
  // 1. Group by family (local-basis providers → "local"), preserving
  //    first-seen order.
  const byFam = new Map<string, FacetValue[]>();
  const order: string[] = [];
  for (const v of values) {
    const fam = familyOf(v.value, locals);
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
  if (key === LOCAL_KEY) return 1;
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

// --- cache hit rate per harness ----------------------------------------------

export interface CacheHitRow {
  key: string; // display form ("(none)" for the empty value)
  raw: string;
  cacheRead: number;
  input: number;
  hitRate: string; // "12.3%", or "—" when input + cache-read is 0
}

// hitRateLabel: cache-read ÷ (input + cache-read), one decimal.
function hitRateLabel(cacheRead: number, input: number): string {
  const denom = input + cacheRead;
  return denom === 0 ? "—" : `${((cacheRead / denom) * 100).toFixed(1)}%`;
}

// cacheHitRows: one row per harness present in the served by-harness rows
// (a row exists only for a harness with events that day), largest
// input + cache-read first, plus a total summed from the same rows. The
// total's rate is computed on the summed counts, not averaged.
export function cacheHitRows(rows: DailyByRow[]): { rows: CacheHitRow[]; total: CacheHitRow } {
  const acc = new Map<string, { cacheRead: number; input: number }>();
  let cacheRead = 0;
  let input = 0;
  for (const r of rows) {
    const t = acc.get(r.key) ?? { cacheRead: 0, input: 0 };
    t.cacheRead += r.cacheReadTokens;
    t.input += r.inputTokens;
    acc.set(r.key, t);
    cacheRead += r.cacheReadTokens;
    input += r.inputTokens;
  }
  const out = [...acc.entries()]
    .map(([raw, t]) => ({ key: displayValue(raw), raw, ...t, hitRate: hitRateLabel(t.cacheRead, t.input) }))
    .sort((a, b) => b.input + b.cacheRead - (a.input + a.cacheRead) || (a.raw < b.raw ? -1 : a.raw > b.raw ? 1 : 0));
  return { rows: out, total: { key: "total", raw: "", cacheRead, input, hitRate: hitRateLabel(cacheRead, input) } };
}

// SessionLine is one sessions-panel row, shaped for display.
export interface SessionLine {
  raw: string; // the session id a click filters on
  label: string; // first 8 characters, "(none)" for ""
  harness: string;
  project: string; // folder name (projectLabel)
  projectRaw: string;
  start: string; // firstTs as "MM-DD HH:mm" in tz
  events: number;
  tokens: number; // the four-field total
  equivMicro: number;
}

// sessionLines shapes the served rows in their served order.
export function sessionLines(rows: SessionRow[], tz: string): SessionLine[] {
  return rows.map((r) => ({
    raw: r.session,
    label: sessionLabel(r.session),
    harness: displayValue(r.harness),
    project: projectLabel(r.project),
    projectRaw: r.project,
    start: zoneTimeLabel(r.firstTs, tz),
    events: r.events,
    tokens: totalTokens(r),
    equivMicro: r.costAPIEquivMicro,
  }));
}
