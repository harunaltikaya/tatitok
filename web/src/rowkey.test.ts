// Breakdown rows are keyed by the raw value (Review 0924e): two
// directories that share a last folder name print the same label but stay
// two rows, each with its own totals, through sortTotals in either order.

import { test } from "node:test";
import assert from "node:assert/strict";
import { sumByKey, sortTotals } from "./aggregate.ts";
import { projectLabel, type Sort } from "./filters.ts";
import type { DailyByRow } from "./api.ts";

function row(date: string, key: string, equiv: number, tokens: number): DailyByRow {
  return {
    date, key,
    inputTokens: tokens, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0,
    reasoningTokens: 0, costUSDMicro: 0, costAPIEquivMicro: equiv, unpricedEvents: 0,
    ratedEvents: 1,
  };
}

test("same-label project rows stay distinct by raw through sortTotals", () => {
  const rows = [
    row("2026-09-23", "/a/repo", 300, 30),
    row("2026-09-24", "/a/repo", 100, 10),
    row("2026-09-24", "/b/repo", 200, 20),
    row("2026-09-24", "/c/other", 50, 5),
  ];
  // As the by-project panel builds its rows: aggregate by raw, print by name.
  const labeled = sumByKey(rows).map((t) => ({ ...t, key: projectLabel(t.raw) }));
  const want = new Map([
    ["/a/repo", { equiv: 400, tokens: 40 }],
    ["/b/repo", { equiv: 200, tokens: 20 }],
    ["/c/other", { equiv: 50, tokens: 5 }],
  ]);
  for (const key of ["equiv", "tokens"] as const) {
    for (const dir of ["desc", "asc"] as const) {
      const sort: Sort = { key, dir };
      const out = sortTotals(labeled, sort);
      assert.equal(out.length, 3, `${key} ${dir}: three rows`);
      assert.equal(new Set(out.map((t) => t.raw)).size, 3, `${key} ${dir}: raw values (the row keys) are distinct`);
      assert.deepEqual(out.filter((t) => t.key === "repo").map((t) => t.raw).sort(), ["/a/repo", "/b/repo"]);
      const order = dir === "desc" ? ["/a/repo", "/b/repo", "/c/other"] : ["/c/other", "/b/repo", "/a/repo"];
      assert.deepEqual(out.map((t) => t.raw), order, `${key} ${dir}: order`);
      for (const t of out) {
        const w = want.get(t.raw)!;
        assert.equal(t.equivMicro, w.equiv, `${t.raw} equiv`);
        assert.equal(t.tokens, w.tokens, `${t.raw} tokens`);
      }
    }
  }
});
