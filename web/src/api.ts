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
  date: string; // YYYY-MM-DD (UTC)
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

export function fetchDaily(from: string, to: string): Promise<{ daily: DailyRow[] }> {
  return getJSON(`/api/v1/stats/daily?from=${from}&to=${to}`);
}

export function fetchDailyBy(
  by: "harness" | "provider" | "model" | "project",
  from: string,
  to: string,
): Promise<{ daily_by: DailyByRow[] }> {
  return getJSON(`/api/v1/stats/daily?by=${by}&from=${from}&to=${to}`);
}

export function fetchTotals(window: string): Promise<{ days: number; totals: Totals }> {
  return getJSON(`/api/v1/totals?window=${window}`);
}

export function fetchModels(): Promise<{ models: ModelInfo[] }> {
  return getJSON(`/api/v1/meta/models`);
}

export function fetchHealth(): Promise<Health> {
  return getJSON(`/api/v1/health`);
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

// UTC day arithmetic for the range picker — days are UTC everywhere in
// the rollup-backed API and the UI says so.
export function utcToday(): string {
  return new Date().toISOString().slice(0, 10);
}

export function utcDaysAgo(n: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - n);
  return d.toISOString().slice(0, 10);
}
