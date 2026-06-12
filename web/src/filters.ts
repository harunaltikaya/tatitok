// Global facet filter state (M5 Task 4, the owner's Qlik-style
// direction): everything is a facet, every value is a filter. ONE
// state drives every chart, table and total; composition is OR within
// a facet, AND across facets — matching the API semantics exactly
// (the params below are the API's own). State lives in the URL
// (shareable, bookmarkable, survives refresh) and nowhere else —
// saved views are out of scope this milestone.

export type FacetDim = "harness" | "provider" | "model" | "project" | "basis";

export const facetDims: FacetDim[] = ["harness", "provider", "model", "project", "basis"];

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

// URL round-trip: filters (and the day range) are the URL's query
// string; popstate/refresh restore them exactly.
export function filtersFromURL(search: string): { filters: FilterState; from: string | null; to: string | null } {
  const p = new URLSearchParams(search);
  const filters = emptyFilters();
  for (const dim of facetDims) filters[dim] = p.getAll(dim);
  return { filters, from: p.get("from"), to: p.get("to") };
}

export function filtersToURL(f: FilterState, from: string, to: string): string {
  const p = new URLSearchParams();
  p.set("from", from);
  p.set("to", to);
  for (const dim of facetDims) {
    for (const v of f[dim]) p.append(dim, v);
  }
  return `?${p.toString()}`;
}
