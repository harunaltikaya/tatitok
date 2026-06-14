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
import type { DailyByRow } from "./api.ts";
import { totalTokens } from "./api.ts";
import { displayValue } from "./filters.ts";

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
  return [...acc.values()].sort((a, b) => b.costMicro - a.costMicro || b.tokens - a.tokens);
}
