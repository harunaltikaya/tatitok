// Daily-chart cell-aggregation test (M8 1H fix), Node's built-in runner. This
// is the test that was missing: the existing suites asserted aggregate totals
// (table/donut) but never a per-(date, key) chart cell, which is why the bug
// hid — dailyStackedChart OVERWROTE each cell, so a collapsed family ("vllm")
// or folded "others" (several same-day rows after rollupRows/mergeFamilies)
// showed only the last member, not the sum. chartCells now accumulates; this
// pins that, plus the chart == donut invariant per key. Display-only.

import { test } from "node:test";
import assert from "node:assert/strict";
import { chartCells } from "./aggregate.ts";
import type { DailyByRow } from "./api.ts";

function row(date: string, key: string, equiv: number): DailyByRow {
  return {
    date, key,
    inputTokens: 0, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0,
    reasoningTokens: 0, costUSDMicro: 0, costAPIEquivMicro: equiv, unpricedEvents: 0,
  };
}

test("chart cells SUM duplicate (date,key) rows (collapsed/folded series); chart == donut per key", () => {
  const value = (r: DailyByRow) => r.costAPIEquivMicro;
  // Two rows share (06-01, "vllm") — as after collapsing vllm-* — and two share
  // (06-01, "others") — as after folding the tail. The cell must be their SUM,
  // not the last-written row (the overwrite bug this fixes).
  const rows = [
    row("2026-06-01", "anthropic", 500),
    row("2026-06-01", "vllm", 10),
    row("2026-06-01", "vllm", 7),
    row("2026-06-01", "others", 4),
    row("2026-06-01", "others", 9),
    row("2026-06-02", "vllm", 5),
  ];
  const cells = chartCells(rows, value);
  assert.equal(cells.get("2026-06-01|vllm"), 10 + 7); // 17, not 7 (last)
  assert.equal(cells.get("2026-06-01|others"), 4 + 9); // 13, not 9 (last)
  assert.equal(cells.get("2026-06-01|anthropic"), 500);
  assert.equal(cells.get("2026-06-02|vllm"), 5);

  // chart == donut: a key's bars summed over days equal Σ value over that key's
  // rows (what the donut shows) — the invariant the chart≠donut finding exposed.
  for (const k of new Set(rows.map((r) => r.key))) {
    const chartTotal = [...cells.entries()]
      .filter(([id]) => id.split("|")[1] === k)
      .reduce((s, [, v]) => s + v, 0);
    const donutTotal = rows.filter((r) => r.key === k).reduce((s, r) => s + value(r), 0);
    assert.equal(chartTotal, donutTotal, `chart != donut for ${k}`);
  }
});
