// Tooltip-data tests (M6 Task 3), run by Node's built-in test runner
// with native TypeScript type-stripping — no test-runner dependency.
// `node --test` (the web build's `npm test`) discovers this file.

import { test } from "node:test";
import assert from "node:assert/strict";
import { dayTotal, topModelsAtDay } from "./tooltip.ts";
import type { DailyByRow } from "./api.ts";

function row(date: string, key: string, equiv: number, tokensIn = 0): DailyByRow {
  return {
    date, key,
    inputTokens: tokensIn, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0,
    reasoningTokens: 0, costUSDMicro: 0, costAPIEquivMicro: equiv, unpricedEvents: 0,
  };
}
const equiv = (r: DailyByRow) => r.costAPIEquivMicro;

test("dayTotal sums only the hovered day", () => {
  const rows = [
    row("2026-06-12", "claude-fable-5", 100),
    row("2026-06-12", "gpt-5.5", 50),
    row("2026-06-11", "claude-fable-5", 999), // other day — ignored
  ];
  assert.equal(dayTotal(rows, "2026-06-12", equiv), 150);
});

test("topModelsAtDay keeps top n and folds the rest into 'other'", () => {
  const rows: DailyByRow[] = [];
  for (let i = 0; i < 8; i++) rows.push(row("2026-06-12", `m${i}`, (8 - i) * 10)); // 80,70,…,10
  const top = topModelsAtDay(rows, "2026-06-12", equiv, 5);
  assert.deepEqual(top.map((e) => e.key), ["m0", "m1", "m2", "m3", "m4", "other"]);
  assert.equal(top[5].value, 30 + 20 + 10); // m5+m6+m7
  // The breakdown conserves the day total.
  assert.equal(top.reduce((s, e) => s + e.value, 0), dayTotal(rows, "2026-06-12", equiv));
});

// The filtered case: when the global filter restricts to one model, App
// passes the already-filtered daily_by rows here, so the tooltip shows
// exactly that model and its total equals the filtered day total — the
// tooltip cannot disagree with the chart's filter state.
test("a model filter is reflected: only the filtered model appears, total matches", () => {
  const filtered = [row("2026-06-12", "gpt-5.5", 42)];
  assert.deepEqual(topModelsAtDay(filtered, "2026-06-12", equiv), [{ key: "gpt-5.5", value: 42 }]);
  assert.equal(dayTotal(filtered, "2026-06-12", equiv), 42);
});
