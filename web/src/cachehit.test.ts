// cacheHitRows: the cache hit-rate panel's rows — per harness, summed
// over the range's days, plus a total from the same rows.

import { test } from "node:test";
import assert from "node:assert/strict";
import { cacheHitRows } from "./aggregate.ts";
import type { DailyByRow } from "./api.ts";

function row(date: string, key: string, input: number, output: number, cacheWrite: number, cacheRead: number): DailyByRow {
  return {
    date, key,
    inputTokens: input, outputTokens: output, cacheCreationTokens: cacheWrite, cacheReadTokens: cacheRead,
    reasoningTokens: 0, costUSDMicro: 0, costAPIEquivMicro: 0, unpricedEvents: 0, ratedEvents: 1,
  };
}

test("cacheHitRows: per-harness rate over the range, a zero denominator, the total", () => {
  const { rows, total } = cacheHitRows([
    row("2026-09-23", "claude-code", 60, 10, 7, 150),
    row("2026-09-24", "claude-code", 40, 5, 3, 50),
    // Events, but no input and no cache-read: the rate is undefined.
    row("2026-09-24", "codex", 0, 40, 5, 0),
  ]);
  assert.deepEqual(rows, [
    // 200 ÷ (100 + 200); output and cache-write never enter the ratio.
    { key: "claude-code", raw: "claude-code", cacheRead: 200, input: 100, hitRate: "66.7%" },
    { key: "codex", raw: "codex", cacheRead: 0, input: 0, hitRate: "—" },
  ]);
  // Summed from the same rows, rate on the sums.
  assert.deepEqual(total, { key: "total", raw: "", cacheRead: 200, input: 100, hitRate: "66.7%" });
});

test("cacheHitRows: the total's rate is on the summed counts, not a mean of rates", () => {
  const { rows, total } = cacheHitRows([
    row("2026-09-24", "pi", 100, 0, 0, 0),
    row("2026-09-24", "opencode", 100, 0, 0, 300),
  ]);
  assert.deepEqual(rows.map((r) => [r.raw, r.hitRate]), [["opencode", "75.0%"], ["pi", "0.0%"]]);
  assert.equal(total.hitRate, "60.0%"); // 300 ÷ 500, not (75 + 0) ÷ 2
});

test("cacheHitRows: no rows in range → no harness rows, total reads —", () => {
  const { rows, total } = cacheHitRows([]);
  assert.deepEqual(rows, []);
  assert.equal(total.hitRate, "—");
});
