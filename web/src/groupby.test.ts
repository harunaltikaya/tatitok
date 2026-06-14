// Group-by URL-state + conservation tests (M8 chunk 1B), Node's built-in
// runner. The segmented-control wiring + the live re-aggregation are
// exercised in the chunk-B drive; this pins the pure machinery: parseGroupBy
// validation, the groupBy serialize→parse round-trip (default provider stays
// out of the URL, like default home), and the conservation invariant — the
// SAME atomic contributions bucketed by harness vs provider vs model sum, via
// sumByKey, to the same grand total (group-by is display-only, never a recount).

import { test } from "node:test";
import assert from "node:assert/strict";
import { parseGroupBy, filtersToURL, filtersFromURL, emptyFilters, DEFAULT_SORT } from "./filters.ts";
import { sumByKey } from "./aggregate.ts";
import type { DailyByRow } from "./api.ts";

test("groupBy: URL round-trip (default provider omitted) and grand-total conservation", () => {
  // parseGroupBy accepts the three dimensions and defaults everything else.
  assert.equal(parseGroupBy("harness"), "harness");
  assert.equal(parseGroupBy("provider"), "provider");
  assert.equal(parseGroupBy("model"), "model");
  assert.equal(parseGroupBy(null), "model");
  assert.equal(parseGroupBy("bogus"), "model");

  const f = emptyFilters();
  const from = "2026-06-01";
  const to = "2026-06-14";
  const tz = "UTC";

  // Default model stays OUT of the URL (like default home), so the common
  // link is the shortest.
  const modelURL = filtersToURL(f, from, to, tz, "home", "model", DEFAULT_SORT);
  assert.equal(modelURL, `?from=${from}&to=${to}&tz=${tz}`);
  assert.ok(!modelURL.includes("groupBy="));
  assert.equal(filtersFromURL(modelURL).groupBy, "model");

  // The non-default dimensions write the param and survive serialize→parse.
  for (const g of ["harness", "provider"] as const) {
    const url = filtersToURL(f, from, to, tz, "home", g, DEFAULT_SORT);
    assert.ok(url.includes(`groupBy=${g}`));
    assert.equal(filtersFromURL(url).groupBy, g);
  }

  // Conservation: four atomic daily contributions, each tagged with all three
  // dimension keys. Bucketing the same atoms by a different key must not
  // change the grand total — sumByKey only re-buckets already-served numbers.
  const atoms = [
    { date: "2026-06-01", harness: "claude-code", provider: "anthropic", model: "claude-sonnet-4-6", input: 100, output: 10, cw: 0, cr: 5, cost: 1_000, equiv: 2_000, unp: 0 },
    { date: "2026-06-01", harness: "codex", provider: "openai", model: "gpt-5", input: 200, output: 20, cw: 4, cr: 0, cost: 0, equiv: 3_000, unp: 1 },
    { date: "2026-06-02", harness: "claude-code", provider: "anthropic", model: "claude-opus-4-8", input: 300, output: 30, cw: 1, cr: 9, cost: 5_000, equiv: 5_000, unp: 0 },
    { date: "2026-06-02", harness: "codex", provider: "openai", model: "gpt-5", input: 400, output: 40, cw: 0, cr: 2, cost: 0, equiv: 7_000, unp: 0 },
  ];
  const rowsBy = (dim: "harness" | "provider" | "model"): DailyByRow[] =>
    atoms.map((a) => ({
      date: a.date, key: a[dim],
      inputTokens: a.input, outputTokens: a.output,
      cacheCreationTokens: a.cw, cacheReadTokens: a.cr,
      reasoningTokens: 0, costUSDMicro: a.cost,
      costAPIEquivMicro: a.equiv, unpricedEvents: a.unp,
    }));
  const grand = (rows: DailyByRow[]) => {
    const t = sumByKey(rows);
    return {
      tokens: t.reduce((n, k) => n + k.tokens, 0),
      cost: t.reduce((n, k) => n + k.costMicro, 0),
      equiv: t.reduce((n, k) => n + k.equivMicro, 0),
      unpriced: t.reduce((n, k) => n + k.unpriced, 0),
    };
  };
  const byHarness = grand(rowsBy("harness"));
  assert.deepEqual(byHarness, grand(rowsBy("provider")));
  assert.deepEqual(byHarness, grand(rowsBy("model")));
  // …and it equals the raw sum of the atoms (not a coincidental zero).
  const tot = atoms.reduce((n, a) => n + a.input + a.output + a.cw + a.cr, 0);
  assert.equal(byHarness.tokens, tot);
});
