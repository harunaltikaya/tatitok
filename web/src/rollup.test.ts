// Rollup conservation tests (M8 chunk 1C), Node's built-in runner. The live
// home rollup (chart/donut/table/filter-pane) is exercised in the chunk-C
// drive; this pins the pure machinery in aggregate.ts: family collapse folds
// vllm-* into one bucket equal to the sum of its members; top-N + "others"
// conserves the grand total for every dimension; the filter-pane top-N + others
// conserves the event count. Rollup is display-only — it relabels keys, never
// re-counts.

import { test } from "node:test";
import assert from "node:assert/strict";
import { sumByKey, collapseFamilies, familyOf, rollupRows, topFacets, OTHERS_KEY } from "./aggregate.ts";
import type { DailyByRow, FacetValue } from "./api.ts";

// row builds a DailyByRow with a key and a couple of measures; unused token
// components stay 0 so the totals are easy to reason about.
function row(date: string, key: string, equiv: number, tokens: number, cost = 0): DailyByRow {
  return {
    date, key,
    inputTokens: tokens, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0,
    reasoningTokens: 0, costUSDMicro: cost, costAPIEquivMicro: equiv, unpricedEvents: 0,
  };
}

const grand = (rows: DailyByRow[]) => {
  const t = sumByKey(rows);
  return {
    tokens: t.reduce((n, k) => n + k.tokens, 0),
    cost: t.reduce((n, k) => n + k.costMicro, 0),
    equiv: t.reduce((n, k) => n + k.equivMicro, 0),
  };
};

test("rollup: family collapse + top-N/others conserve, for every dimension", () => {
  // familyOf folds only the declared prefixes; model names are untouched.
  assert.equal(familyOf("vllm"), "vllm");
  assert.equal(familyOf("vllm-0.6.3"), "vllm");
  assert.equal(familyOf("anthropic"), "anthropic");
  assert.equal(familyOf("claude-sonnet-4-6"), "claude-sonnet-4-6");

  // Family collapse: the "vllm" bucket equals the sum of its members, and
  // collapsing conserves the grand total.
  const providerRows = [
    row("2026-06-01", "vllm-0.6.3", 100, 1_000),
    row("2026-06-01", "vllm-0.7.0", 200, 2_000),
    row("2026-06-01", "anthropic", 500, 5_000, 5_000),
    row("2026-06-01", "openai", 400, 4_000),
    row("2026-06-01", "deepseek", 300, 3_000),
    row("2026-06-02", "vllm-0.7.0", 50, 500),
    row("2026-06-02", "google", 50, 500),
  ];
  const collapsed = sumByKey(collapseFamilies(providerRows));
  const vllm = collapsed.find((k) => k.raw === "vllm")!;
  assert.equal(vllm.equivMicro, 100 + 200 + 50);
  assert.equal(vllm.tokens, 1_000 + 2_000 + 500);
  assert.deepEqual(grand(collapseFamilies(providerRows)), grand(providerRows));

  // top-N + others = grand total (and families fold first).
  const rolled = rollupRows(providerRows, 2);
  assert.deepEqual(grand(rolled), grand(providerRows));
  const keys = new Set(rolled.map((r) => r.key));
  assert.ok(keys.size <= 3); // top-2 + others
  assert.ok(keys.has(OTHERS_KEY));
  assert.ok(!keys.has("google")); // a folded member never survives as its own key

  // Conservation for EVERY dimension: the same atoms bucketed three ways.
  const atoms = [
    { date: "2026-06-01", harness: "claude-code", provider: "anthropic", model: "claude-sonnet-4-6", equiv: 2_000, tokens: 110 },
    { date: "2026-06-01", harness: "codex", provider: "openai", model: "gpt-5", equiv: 3_000, tokens: 220 },
    { date: "2026-06-02", harness: "claude-code", provider: "vllm-0.7.0", model: "qwen3", equiv: 500, tokens: 330 },
    { date: "2026-06-02", harness: "opencode", provider: "vllm-0.6.3", model: "llama-4", equiv: 700, tokens: 440 },
    { date: "2026-06-02", harness: "codex", provider: "deepseek", model: "deepseek-v4", equiv: 900, tokens: 550 },
  ];
  for (const dim of ["harness", "provider", "model"] as const) {
    const rows = atoms.map((a) => row(a.date, a[dim], a.equiv, a.tokens));
    assert.deepEqual(grand(rollupRows(rows, 2)), grand(rows));
  }

  // Filter-pane top-N + others conserves the event count.
  const facets: FacetValue[] = [
    { value: "a", events: 50 }, { value: "b", events: 40 }, { value: "c", events: 30 },
    { value: "d", events: 20 }, { value: "e", events: 10 }, { value: "f", events: 5 },
  ];
  const tf = topFacets(facets, 3);
  assert.equal(tf.shown.length, 3);
  assert.equal(tf.othersValues, 3);
  const totalEvents = facets.reduce((n, v) => n + v.events, 0);
  assert.equal(tf.shown.reduce((n, v) => n + v.events, 0) + tf.othersEvents, totalEvents);
});
