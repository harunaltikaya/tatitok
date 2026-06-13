// Pure tooltip-data helpers (M6 Task 3). The chart bars AND these
// tooltip numbers are fed by the SAME filtered, timezoned daily_by
// queries (App.loadRange fetches by-provider and by-model together with
// one fq + tz + range), so they agree by construction, not coincidence.
// value() selects the measure the chart shows (tokens, API-equivalent
// dollars, …); the breakdown uses the same selector, so a tooltip can
// never report a different number than its chart. Kept dependency-free
// (type-only import) so Node's built-in test runner exercises it.

import type { DailyByRow } from "./api";

// dayTotal sums one day's value across the (already filtered) rows — the
// tooltip's "total" line, equal to the stacked bar's height.
export function dayTotal(rows: DailyByRow[], day: string, value: (r: DailyByRow) => number): number {
  let total = 0;
  for (const r of rows) {
    if (r.date === day) total += value(r);
  }
  return total;
}

export interface BreakdownEntry {
  key: string;
  value: number;
}

// topModelsAtDay returns the day's per-model values descending, the top
// n kept and the remainder folded into a single "other" entry — the
// tokens / API-equivalent tooltip's by-model section. Zero-valued models
// are dropped. Because rows are the filter-respecting daily_by set, a
// model filter narrows this to exactly the filtered models.
export function topModelsAtDay(
  rows: DailyByRow[],
  day: string,
  value: (r: DailyByRow) => number,
  n = 5,
): BreakdownEntry[] {
  const by = new Map<string, number>();
  for (const r of rows) {
    if (r.date !== day) continue;
    by.set(r.key, (by.get(r.key) ?? 0) + value(r));
  }
  const entries = [...by.entries()]
    .map(([key, v]) => ({ key, value: v }))
    .filter((e) => e.value !== 0)
    .sort((a, b) => b.value - a.value);
  if (entries.length <= n) return entries;
  const top = entries.slice(0, n);
  const other = entries.slice(n).reduce((s, e) => s + e.value, 0);
  if (other !== 0) top.push({ key: "other", value: other });
  return top;
}
