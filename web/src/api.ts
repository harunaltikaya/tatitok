// Typed client for the hub's /api/v1 (M4 Task 2 shapes). All paths are
// relative: the bundle is served by the hub itself, same origin, no
// external requests ever.

export interface TokenSums {
  inputTokens: number;
  outputTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
}

export interface CostSums {
  reasoningTokens: number;
  costUSDMicro: number;
  costAPIEquivMicro: number;
  unpricedEvents: number;
}

export interface DailyRow extends TokenSums, CostSums {
  date: string; // YYYY-MM-DD in the query timezone (UTC by default)
}

export interface DailyByRow extends TokenSums, CostSums {
  date: string;
  key: string;
}

export interface ModelInfo {
  provider: string;
  model: string;
  modelFamily: string;
  costBasis: string;
  events: number;
}

export interface Totals extends TokenSums, CostSums {}

export interface Health {
  version: string;
  price_snapshot: string;
  overrides: number;
  reference_models: number;
  db_hash: string;
  started_at: string;
  uptime_seconds: number;
}

// Plan window meter + value panel payload (M5 Task 2). Windows are
// tatitok-native rolling windows over STAMPED plan_included events —
// informational, not a parity surface; the API computes them, the UI
// only renders.
export interface PlanWindowUsage {
  start: string;
  end: string;
  events: number;
  input: number;
  output: number;
  cache_write: number;
  cache_read: number;
  cost_api_equiv_micro: number;
  events_unpriced: number;
  seconds_to_reset: number;
}

export interface PlanPeriod {
  from: string;
  events: number;
  cost_api_equiv_micro: number;
  events_unpriced: number;
}

export interface PlanStatus {
  name: string;
  window_seconds: number;
  weekly_cap_equiv_micro: number | null;
  monthly_price_micro: number | null;
  current_window: PlanWindowUsage | null;
  week: PlanPeriod;
  month: PlanPeriod;
  windows_total: number;
}

export interface PlansPayload {
  now: string;
  plans: PlanStatus[];
  unmatched_plan_events: number;
}

export interface HarnessPass {
  harness: string;
  files: number;
  events: number;
  new: number;
  replaced: number;
  parseErrors: number;
}

export interface IngestPass {
  pass: number;
  at: string;
  harnesses: HarnessPass[];
  touched_days: string[];
}

async function getJSON<T>(path: string): Promise<T> {
  const resp = await fetch(path);
  if (!resp.ok) {
    let msg = `${resp.status}`;
    try {
      const e = await resp.json();
      msg = e?.error?.message ?? msg;
    } catch {
      /* not the envelope — keep the status */
    }
    throw new Error(`GET ${path}: ${msg}`);
  }
  return resp.json() as Promise<T>;
}

// The fq argument is the global facet filter state rendered as
// repeated query params (filters.ts filterQuery) — every data fetch
// obeys the one state (M5 Task 4). tz is the IANA timezone the days
// bucket in (M6 Task 2); the SERVER resolves it against the binary's
// embedded tzdata and declares it back. source declares the serving
// path ("rollup"|"events" — the API's honesty field).
export function fetchDaily(from: string, to: string, fq = "", tz = "UTC"): Promise<{ tz: string; source: string; daily: DailyRow[] }> {
  return getJSON(`/api/v1/stats/daily?from=${from}&to=${to}&timezone=${encodeURIComponent(tz)}${fq}`);
}

export function fetchDailyBy(
  by: "harness" | "provider" | "model" | "project",
  from: string,
  to: string,
  fq = "",
  tz = "UTC",
): Promise<{ tz: string; source: string; daily_by: DailyByRow[] }> {
  return getJSON(`/api/v1/stats/daily?by=${by}&from=${from}&to=${to}&timezone=${encodeURIComponent(tz)}${fq}`);
}

export function fetchTotals(window: string, fq = "", tz = "UTC"): Promise<{ tz: string; days: number; totals: Totals }> {
  return getJSON(`/api/v1/totals?window=${window}&timezone=${encodeURIComponent(tz)}${fq}`);
}

export function fetchModels(): Promise<{ models: ModelInfo[] }> {
  return getJSON(`/api/v1/meta/models`);
}

export interface FacetValue {
  value: string;
  events: number;
}

export function fetchFacets(): Promise<{ facets: Record<string, FacetValue[]> }> {
  return getJSON(`/api/v1/meta/facets`);
}

export function fetchHealth(): Promise<Health> {
  return getJSON(`/api/v1/health`);
}

export function fetchPlans(): Promise<PlansPayload> {
  return getJSON(`/api/v1/plans`);
}

// usd renders integer micro-USD; sub-cent totals keep enough digits to
// stay honest instead of rounding to $0.00.
export function usd(micro: number): string {
  const dollars = micro / 1_000_000;
  if (micro !== 0 && Math.abs(dollars) < 0.01) {
    return `$${dollars.toFixed(6).replace(/0+$/, "")}`;
  }
  return `$${dollars.toFixed(2)}`;
}

export function compactTokens(n: number): string {
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(2)}B`;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return `${n}`;
}

export function totalTokens(t: TokenSums): number {
  return t.inputTokens + t.outputTokens + t.cacheCreationTokens + t.cacheReadTokens;
}

// Timezone helpers for the range picker (M6 Task 2). The browser only
// detects the IANA NAME (Intl) and picks default range bounds; the
// SERVER does the authoritative day bucketing against its embedded
// tzdata — the browser's own zone math never decides the data.
export function browserTZ(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

// availableTZs is the IANA list for the selector — the full supported
// set where the browser exposes it, always including UTC and the
// detected zone, sorted.
export function availableTZs(): string[] {
  let list: string[] = [];
  try {
    const sv = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    if (typeof sv === "function") list = sv("timeZone");
  } catch {
    /* older engine — fall back to UTC + browser zone only */
  }
  const set = new Set<string>(list);
  set.add("UTC");
  set.add(browserTZ());
  return [...set].sort();
}

// dayInTZ formats an instant as its YYYY-MM-DD calendar day in tz
// (en-CA renders ISO order). todayInTZ / daysAgoInTZ drive the range
// presets so "today" and "last N days" mean the viewer's local days.
export function dayInTZ(date: Date, tz: string): string {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: tz, year: "numeric", month: "2-digit", day: "2-digit",
  }).format(date);
}

export function todayInTZ(tz: string): string {
  return dayInTZ(new Date(), tz);
}

export function daysAgoInTZ(tz: string, n: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - n);
  return dayInTZ(d, tz);
}

// tzOffsetLabel renders a zone's UTC offset for the selector annotation (M8 1I)
// — e.g. "UTC+3", "UTC+0", "UTC-4", "UTC+5:30". A CLARITY label only: the server
// does the authoritative day-bucketing tz math, this never enters any
// computation. DST is not modelled — one representative offset (at `at`,
// default now) is shown. Intl's shortOffset yields "GMT±H[:MM]"; normalize to
// "UTC±H[:MM]" (drop a leading zero and a ":00"); "" on an engine that can't.
export function tzOffsetLabel(tz: string, at: Date = new Date()): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", { timeZone: tz, timeZoneName: "shortOffset" }).formatToParts(at);
    const name = parts.find((p) => p.type === "timeZoneName")?.value ?? "";
    const m = name.match(/GMT([+-])(\d{1,2})(?::(\d{2}))?/);
    if (!m) return name === "GMT" ? "UTC+0" : "";
    const mins = m[3] && m[3] !== "00" ? `:${m[3]}` : "";
    return `UTC${m[1]}${Number(m[2])}${mins}`;
  } catch {
    return "";
  }
}
