// Detail by-provider chart-series tests (M8 chunk 1H), Node's built-in runner.
// The live stacked charts are exercised in the chunk-H drive; this pins the
// pure machinery in aggregate.ts (mergeFamilies): the vllm-* wall folds into
// ONE "vllm" series per day whose value is the SUM of its members
// (conservation), every other provider family stays (no top-N drop, no
// "others"), and the input rows (the full table's data) are left untouched —
// so a collapsed chart sits beside a full drill-down table. Display-only.

import { test } from "node:test";
import assert from "node:assert/strict";
import { mergeFamilies } from "./aggregate.ts";
import type { DailyByRow } from "./api.ts";

// row builds a DailyByRow with a key + a couple of measures (others 0).
function row(date: string, key: string, equiv: number, tokens: number, cost = 0): DailyByRow {
  return {
    date, key,
    inputTokens: tokens, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0,
    reasoningTokens: 0, costUSDMicro: cost, costAPIEquivMicro: equiv, unpricedEvents: 0,
  };
}

const sumEquiv = (rs: DailyByRow[]) => rs.reduce((s, r) => s + r.costAPIEquivMicro, 0);
const sumTokens = (rs: DailyByRow[]) => rs.reduce((s, r) => s + r.inputTokens, 0);

test("1H: detail by-provider chart series collapse vllm (conserved), other families intact", () => {
  const providerRows = [
    row("2026-06-01", "anthropic", 500, 5000),
    row("2026-06-01", "openai", 400, 4000),
    row("2026-06-01", "vllm-35b", 10, 100),
    row("2026-06-01", "vllm-delegate", 7, 70),
    row("2026-06-01", "vllm", 3, 30), // the bare vllm provider is a member
    row("2026-06-02", "deepseek", 300, 3000),
    row("2026-06-02", "vllm-35b", 5, 50),
  ];
  const series = mergeFamilies(providerRows);

  // vllm-* fold into ONE "vllm" series per day = the SUM of its members.
  const vllm0601 = series.find((r) => r.date === "2026-06-01" && r.key === "vllm");
  assert.ok(vllm0601);
  assert.equal(vllm0601.costAPIEquivMicro, 10 + 7 + 3);
  assert.equal(vllm0601.inputTokens, 100 + 70 + 30);
  const vllm0602 = series.find((r) => r.date === "2026-06-02" && r.key === "vllm");
  assert.ok(vllm0602);
  assert.equal(vllm0602.costAPIEquivMicro, 5);
  // Exactly one "vllm" row per day (members merged, not stacked separately),
  // and no vllm-* variant survives as its own series.
  assert.equal(series.filter((r) => r.key === "vllm").length, 2);
  assert.ok(!series.some((r) => r.key.startsWith("vllm-")));

  // Every non-vllm provider family stays — NO top-N drop, NO "others" bucket.
  const keys = new Set(series.map((r) => r.key));
  for (const p of ["anthropic", "openai", "deepseek", "vllm"]) assert.ok(keys.has(p), `missing ${p}`);
  assert.ok(!keys.has("others"));

  // Conservation: the collapsed series totals equal the input totals.
  assert.equal(sumEquiv(series), sumEquiv(providerRows));
  assert.equal(sumTokens(series), sumTokens(providerRows));

  // Pure: the input (the detail TABLE's data) is untouched — every vllm-* row
  // remains for the full drill-down beside the collapsed chart.
  assert.equal(providerRows.length, 7);
  assert.deepEqual(
    providerRows.filter((r) => r.key.startsWith("vllm")).map((r) => r.key).sort(),
    ["vllm", "vllm-35b", "vllm-35b", "vllm-delegate"],
  );
});
