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
  // Events in the group that resolved SOME rate (a cost, or a plan event's
  // API-equivalent). 0 across a key = the key has no price at all: its $0 is
  // unknown, not zero — the table says "unpriced" (isUnpriced, aggregate.ts).
  ratedEvents: number;
}

// One (weekday, hour) cell of the activity heatmap (M8 1L). weekday is Go's
// time.Weekday (0=Sunday..6=Saturday); hour is 0..23 (local hour in the query
// tz). events = count, tokens = total volume — the frontend picks the metric.
export interface ActivityBucket {
  weekday: number;
  hour: number;
  events: number;
  tokens: number;
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
  label: string; // owner-editable card title; "" → show name
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
  by: "harness" | "provider" | "model" | "project" | "machine",
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

// fetchActivity (M8 1L): the weekday×hour buckets for the heatmap — the SAME
// filtered/timezoned range every other fetch obeys (one global state).
export function fetchActivity(from: string, to: string, fq = "", tz = "UTC"): Promise<{ tz: string; buckets: ActivityBucket[] }> {
  return getJSON(`/api/v1/stats/activity?from=${from}&to=${to}&timezone=${encodeURIComponent(tz)}${fq}`);
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

// Reported usage limits (M9): the display-only, FENCED data the companion
// browser extension POSTs to the hub, served back at GET /api/v1/limits. It is
// NEVER mixed with tatitok's verified token/cost numbers — it has its own fetch
// path, its own error handling, and renders raw (none of the usd/token
// formatters above touch it). A provider key is absent until first polled.
export interface LimitWindow {
  label: string;
  usedPercent: number; // 0..100, but a provider may report over-cap (>100); the bar clamps
  resetAt: number; // epoch ms the window resets (0 = unknown)
}
export interface ProviderLimits {
  fetchedAt: number; // epoch ms the extension last polled this provider
  // null when the provider reported no windows: the backend validly accepts an
  // empty windows slice and Go marshals nil as JSON null. Consumers MUST treat
  // null/missing as "no windows" (use reportedFor) — never call .length on it.
  windows: LimitWindow[] | null;
}
export type LimitsSnapshot = Record<string, ProviderLimits>; // keyed "claude" | "codex" | …

export function fetchLimits(): Promise<{ providers: LimitsSnapshot }> {
  return getJSON(`/api/v1/limits`);
}

// Ingest health: one row per watch target, so a broken log format does not
// look like a quiet day. Times are RFC3339 UTC or null. last_ingest_at lives
// in the serve process: null until the source's first pass since the serve
// started. last_event_at is per harness (every target of a harness shows the
// same). An empty list means the hub runs without watchers.
export interface SourceHealth {
  harness: string;
  root: string; // home shown as "~"
  watch: string; // "fsnotify" | "polling"
  last_ingest_at: string | null;
  parse_errors_last_pass: number; // 0 when no pass yet
  last_event_at: string | null;
}
export interface Sources {
  now: string;
  sources: SourceHealth[];
}

export function fetchSources(): Promise<Sources> {
  return getJSON(`/api/v1/sources`);
}

// Onboarding (Stage 2): thin wrappers over the hub's /api/onboard/* — detection
// + the apply (write prices.json + reprice). Backend reuses Stage 1's logic.
export interface OnboardTier {
  tier: string;
  price_usd: string; // published list default (decimal USD)
}
export interface OnboardCurrent {
  declared: boolean;
  monthly_price_micro: number | null;
  window_seconds: number;
  window_start: string;
}
export interface OnboardCard {
  provider_arg: string; // "claude" | "codex"
  plan_name: string; // card label, e.g. "claude-max"
  provider: string; // "anthropic" | "openai"
  detected_tier: string; // "" when not detected / ambiguous (Claude always "")
  detected_raw: string; // raw plan_type as logged ("pro")
  ambiguous: boolean; // Codex Pro $100/$200 — a choice, never guessed
  ambiguous_options: string[];
  detection_source: string;
  subscription_signal: string;
  note: string;
  windows_present: boolean; // live limits signal (sub-vs-metered hint only)
  metered_available: boolean;
  tiers: OnboardTier[];
  current: OnboardCurrent;
}
export interface OnboardDetect {
  snapshot_version: string;
  has_usage: boolean;
  has_plans: boolean;
  cards: OnboardCard[];
}
export interface OnboardApplyCard {
  provider_arg: string;
  tier: string; // tier name, or "metered"
  price_usd?: string; // override; omit for the list default
}
export interface OnboardApplyResult {
  ok: boolean;
  added: string[];
  replaced: string[];
  removed: string[];
  created: boolean;
  repriced: number;
  cost_changed: number;
  by_basis: { value: string; events: number }[];
}

export function fetchOnboardDetect(): Promise<OnboardDetect> {
  return getJSON(`/api/onboard/detect`);
}

export async function applyOnboard(cards: OnboardApplyCard[]): Promise<OnboardApplyResult> {
  const resp = await fetch(`/api/onboard/apply`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ cards }),
  });
  if (!resp.ok) {
    let msg = `${resp.status}`;
    try {
      const e = await resp.json();
      msg = e?.error?.message ?? msg;
    } catch {
      /* not the envelope — keep the status */
    }
    throw new Error(`apply: ${msg}`);
  }
  return resp.json() as Promise<OnboardApplyResult>;
}

// providerForPlan maps an owner-declared plan to the reported-limits provider
// key the extension uses ("claude" off claude.ai, "codex" off chatgpt.com).
// PlanStatus carries no provider, so derive it from the declared name
// (claude-max → claude, chatgpt-plus → codex). Matching is conservative —
// "chatgpt"/"openai"/"codex" for codex, "claude"/"anthropic" for claude — so a
// bare token like "gpt" can't mis-map a future owner-named plan. No match →
// null → the card's empty-state. (A provider field on PlanStatus is the robust
// fix, deferred past M9.)
export function providerForPlan(name: string): string | null {
  const n = name.toLowerCase();
  if (n.includes("claude") || n.includes("anthropic")) return "claude";
  if (n.includes("chatgpt") || n.includes("openai") || n.includes("codex")) return "codex";
  // Google AI Pro via agy (the statusLine hook posts provider "agy").
  if (n.includes("google") || n.includes("gemini") || n.includes("antigravity") || n.includes("ai-pro")) return "agy";
  return null;
}

// windowLabel renders a reported window's label for the card. The agy hook
// posts agy's own quota keys (gemini-5h, gemini-weekly, 3p-5h, 3p-weekly —
// "3p" = third-party models, i.e. Claude/GPT inside agy); every other
// provider's labels pass through verbatim. One place, so the component stays
// label-agnostic.
const agyWindowLabels: Record<string, string> = {
  "gemini-5h": "Gemini 5h",
  "gemini-weekly": "Gemini weekly",
  "3p-5h": "Claude+GPT 5h",
  "3p-weekly": "Claude+GPT weekly",
};
export function windowLabel(provider: string | null, label: string): string {
  if (provider === "agy") return agyWindowLabels[label] ?? label;
  return label;
}

// reportedFor resolves the reported windows + freshness to render for a plan. It
// hardens against the wire reality that a provider with no windows arrives as
// "windows": null (Go marshals a nil slice as null): a missing bucket OR
// null/absent windows both yield [] — so the UI never calls .length on a
// possibly-null value and cleanly falls through to the empty-state. fetchedAt is
// 0 (→ "unknown") when there is no bucket.
export function reportedFor(
  planName: string,
  limits: LimitsSnapshot | undefined,
): { windows: LimitWindow[]; fetchedAt: number; provider: string | null } {
  const provider = providerForPlan(planName);
  const bucket = provider && limits ? limits[provider] : undefined;
  return { windows: bucket?.windows ?? [], fetchedAt: bucket?.fetchedAt ?? 0, provider };
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

// freshCachedSplit (M8 1K) breaks a token total into "cached" (cache-read —
// served from the prompt cache) vs "fresh" (everything else: input + output +
// cache-creation/writes). A pure display split of the already-served counts:
// fresh + cached = total (conserved); it never changes the total. cachedShare
// is the cached fraction (0 when there are no tokens, so no divide-by-zero).
export function freshCachedSplit(t: TokenSums): { fresh: number; cached: number; total: number; cachedShare: number } {
  const total = totalTokens(t);
  const cached = t.cacheReadTokens;
  return { fresh: total - cached, cached, total, cachedShare: total > 0 ? cached / total : 0 };
}

// compareLabel: the period-compare line under a range card's number —
// "prev <value> (+N%)", the value in the card's own format, N rounded to a
// whole percent. No percent when prev is 0: the change has no base.
export function compareLabel(current: number, prev: number, fmt: (n: number) => string): string {
  if (prev === 0) return `prev ${fmt(prev)}`;
  const pct = Math.round(((current - prev) / prev) * 100);
  return `prev ${fmt(prev)} (${pct > 0 ? "+" : ""}${pct}%)`;
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
// (en-CA renders ISO order). todayInTZ / range.ts daysAgo drive the range
// presets so "today" and "last N days" mean the viewer's local days.
export function dayInTZ(date: Date, tz: string): string {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: tz, year: "numeric", month: "2-digit", day: "2-digit",
  }).format(date);
}

export function todayInTZ(tz: string): string {
  return dayInTZ(new Date(), tz);
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
