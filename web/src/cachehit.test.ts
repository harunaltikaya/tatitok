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
    // claude-code's input holds only the uncached tail; its cache
    // writes are most of the rest of the prompt.
    row("2026-09-23", "claude-code", 6, 10, 300, 900),
    row("2026-09-24", "claude-code", 4, 5, 100, 600),
    // codex writes no cache: its rate is what it was without cache-write.
    row("2026-09-24", "codex", 300, 40, 0, 700),
    // Events, but no input, cache-write or cache-read: the rate is undefined.
    row("2026-09-24", "pi", 0, 40, 0, 0),
  ]);
  assert.deepEqual(rows, [
    // 1500 ÷ (10 + 400 + 1500); it was 99.3% with cache-write left out.
    { key: "claude-code", raw: "claude-code", cacheRead: 1500, input: 10, cacheWrite: 400, hitRate: "78.5%" },
    // 700 ÷ (300 + 0 + 700), unchanged.
    { key: "codex", raw: "codex", cacheRead: 700, input: 300, cacheWrite: 0, hitRate: "70.0%" },
    { key: "pi", raw: "pi", cacheRead: 0, input: 0, cacheWrite: 0, hitRate: "—" },
  ]);
  // Summed from the same rows, rate on the sums: 2200 ÷ (310 + 400 + 2200).
  assert.deepEqual(total, { key: "total", raw: "", cacheRead: 2200, input: 310, cacheWrite: 400, hitRate: "75.6%" });
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
