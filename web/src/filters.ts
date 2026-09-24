// Global facet filter state (M5 Task 4, the owner's Qlik-style
// direction): everything is a facet, every value is a filter. ONE
// state drives every chart, table and total; composition is OR within
// a facet, AND across facets — matching the API semantics exactly
// (the params below are the API's own). State lives in the URL
// (shareable, bookmarkable, survives refresh) and nowhere else —
// saved views are out of scope this milestone.

export type FacetDim = "harness" | "provider" | "model" | "project" | "basis";

export const facetDims: FacetDim[] = ["harness", "provider", "model", "project", "basis"];

// View is the page dimension (M8 chunk 1A): the minimal "home" overview vs
// the full "detail" breakdown. It is SHAREABLE view state — which page you
// are looking at — so it rides the URL beside filters/range/tz (M8 owner
// ruling), not localStorage. Default home.
export type View = "home" | "detail";

export function parseView(v: string | null): View {
  return v === "detail" ? "detail" : "home";
}

// GroupBy is the home overview's aggregation dimension (M8 chunk 1B): the
// primary chart, the value donut and the ranked table all re-aggregate by
// it. Shareable view state → URL beside view/filters/range/tz (owner ruling).
// Default model (1C's family + top-N rollup tames the by-model long tail);
// null/unknown → model.
export type GroupBy = "harness" | "provider" | "model";

export function parseGroupBy(v: string | null): GroupBy {
  return v === "harness" || v === "provider" ? v : "model";
}

// Sort is the table ranking (M8 chunk 1D): a metric key + direction, shared by
// the home summary table and the detail breakdown tables. The default ranks by
// API-equivalent value DESCENDING — never actual cost, which is $0 for
// everything plan_included/local/free, so ranking by it is meaningless.
// Shareable view state → URL like view/groupBy. null/unknown → the default.
export type SortKey = "equiv" | "tokens";
export type SortDir = "desc" | "asc";
export interface Sort {
  key: SortKey;
  dir: SortDir;
}
export const DEFAULT_SORT: Sort = { key: "equiv", dir: "desc" };

export function parseSort(key: string | null, dir: string | null): Sort {
  return {
    key: key === "tokens" ? "tokens" : "equiv",
    dir: dir === "asc" ? "asc" : "desc",
  };
}

export type FilterState = Record<FacetDim, string[]>;

export function emptyFilters(): FilterState {
  return { harness: [], provider: [], model: [], project: [], basis: [] };
}

export function countActive(f: FilterState): number {
  return facetDims.reduce((n, d) => n + f[d].length, 0);
}

// displayValue renders the empty-string value (events without a
// harness/project) readably; rawValue inverts it for filtering.
export function displayValue(v: string): string {
  return v === "" ? "(none)" : v;
}

// projectLabel prints a project by name: the last path segment of the raw
// value ("(none)" for ""). Trailing slashes are ignored ("/a/b/" → "b"); a
// value without "/" (a claude-code folder name, "~") and an all-slash value
// print unchanged. Display only — filters keep the raw path.
export function projectLabel(raw: string): string {
  if (raw === "") return "(none)";
  const trimmed = raw.replace(/\/+$/, "");
  if (trimmed === "") return raw;
  return trimmed.slice(trimmed.lastIndexOf("/") + 1);
}

export function rawValue(display: string): string {
  return display === "(none)" ? "" : display;
}

export function hasValue(f: FilterState, dim: FacetDim, value: string): boolean {
  return f[dim].includes(value);
}

// toggleValue returns a NEW state with value added to / removed from
// the dimension (click-to-filter everywhere shares this).
export function toggleValue(f: FilterState, dim: FacetDim, value: string): FilterState {
  const cur = f[dim];
  const next = cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value];
  return { ...f, [dim]: next };
}

export function removeValue(f: FilterState, dim: FacetDim, value: string): FilterState {
  return { ...f, [dim]: f[dim].filter((v) => v !== value) };
}

// filterQuery renders the state as repeated API query params
// ("&harness=a&harness=b…"), empty string when unconstrained.
export function filterQuery(f: FilterState): string {
  const p = new URLSearchParams();
  for (const dim of facetDims) {
    for (const v of f[dim]) p.append(dim, v);
  }
  const s = p.toString();
  return s === "" ? "" : `&${s}`;
}

// URL round-trip: the page (view), group-by, sort, filters, the day range AND
// the timezone are the URL's query string; popstate/refresh restore them
// exactly (M6 Task 2 adds tz; M8 1A adds view, 1B adds groupBy, 1D adds sort —
// all shareable, like every other filter). Layout is deliberately NOT here
// (M6 Task 4): URLs share what you are looking at, not how your panels are
// arranged.
export function filtersFromURL(search: string): { filters: FilterState; from: string | null; to: string | null; tz: string | null; view: View; groupBy: GroupBy; sort: Sort } {
  const p = new URLSearchParams(search);
  const filters = emptyFilters();
  for (const dim of facetDims) filters[dim] = p.getAll(dim);
  return { filters, from: p.get("from"), to: p.get("to"), tz: p.get("tz"), view: parseView(p.get("view")), groupBy: parseGroupBy(p.get("groupBy")), sort: parseSort(p.get("sort"), p.get("dir")) };
}

export function filtersToURL(f: FilterState, from: string, to: string, tz: string, view: View, groupBy: GroupBy, sort: Sort): string {
  const p = new URLSearchParams();
  p.set("from", from);
  p.set("to", to);
  p.set("tz", tz);
  // Defaults (home, model, equiv-desc) stay out of the URL, so the common case
  // is the shortest link.
  if (view === "detail") p.set("view", "detail");
  if (groupBy !== "model") p.set("groupBy", groupBy);
  if (sort.key !== "equiv") p.set("sort", sort.key);
  if (sort.dir !== "desc") p.set("dir", sort.dir);
  for (const dim of facetDims) {
    for (const v of f[dim]) p.append(dim, v);
  }
  return `?${p.toString()}`;
}
