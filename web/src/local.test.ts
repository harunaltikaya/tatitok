// Local-provider grouping (feat: group local providers), Node's built-in
// runner. Pins the pure machinery in aggregate.ts: localProviders derives the
// "local" family from the SERVED inventory's costBasis (never from the name),
// a provider with any non-local basis stays out, and the family collapse /
// merge / rail regroup all fold that set — and only that set — into ONE
// "local" bucket, conserved.

import { test } from "node:test";
import assert from "node:assert/strict";
import { localProviders, familyOf, mergeFamilies, rollupRows, railItems, sumByKey, LOCAL_KEY, FILTER_TOP_N } from "./aggregate.ts";
import type { DailyByRow, ModelInfo } from "./api.ts";

function info(provider: string, model: string, costBasis: string, events = 1): ModelInfo {
  return { provider, model, modelFamily: model, costBasis, events };
}

function row(date: string, key: string, equiv: number, tokens: number): DailyByRow {
  return {
    date, key,
    inputTokens: tokens, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0,
    reasoningTokens: 0, costUSDMicro: 0, costAPIEquivMicro: equiv, unpricedEvents: 0, ratedEvents: 0,
  };
}

test("localProviders: basis-driven, name-blind, mixed providers excluded", () => {
  const models: ModelInfo[] = [
    info("anthropic", "claude-opus-5", "plan_included"),
    info("sglang-qwen38", "qwen3.8", "local"),
    info("robotlab-qwen38-dflash2-low", "qwen3.8", "local"),
    info("laguna-w4a4-kvcal-local", "qwen3.6", "local"),
    info("vllm", "qwen", "local"),
    info("vllm", "qwen3.6-27b", "local"), // two models, both local → still local
    info("ds4", "deepseek-v4-flash", "api_price"), // a real API behind a short name
    info("ds4", "deepseek-v4-flash-0731", "free"),
    info("fp8-qwen38-dflash2-low", "qwen3.8", "unknown"), // local-looking name, unknown basis → out
    info("zz-proxytest-nothink", "qwen38", "free"),
    info("mixed-box", "a", "local"),
    info("mixed-box", "b", "api_price"), // any non-local basis → not purely local
  ];
  const locals = localProviders(models);
  assert.deepEqual(
    [...locals].sort(),
    ["laguna-w4a4-kvcal-local", "robotlab-qwen38-dflash2-low", "sglang-qwen38", "vllm"],
  );
  assert.equal(localProviders([]).size, 0);

  // familyOf folds exactly the set; no set → identity even for local names.
  assert.equal(familyOf("sglang-qwen38", locals), LOCAL_KEY);
  assert.equal(familyOf("ds4", locals), "ds4");
  assert.equal(familyOf("fp8-qwen38-dflash2-low", locals), "fp8-qwen38-dflash2-low");
  assert.equal(familyOf("sglang-qwen38"), "sglang-qwen38");
});

test("local family: charts, home rollup and rail fold to ONE bucket, conserved", () => {
  const locals = new Set(["sglang-qwen38", "robotlab-low", "vllm"]);
  const rows = [
    row("2026-09-01", "anthropic", 500, 5000),
    row("2026-09-01", "sglang-qwen38", 10, 100),
    row("2026-09-01", "robotlab-low", 7, 70),
    row("2026-09-01", "vllm", 3, 30),
    row("2026-09-01", "ds4", 20, 200),
    row("2026-09-02", "vllm", 5, 50),
    row("2026-09-02", "openai", 40, 400),
  ];
  const sumEquiv = (rs: DailyByRow[]) => rs.reduce((s, r) => s + r.costAPIEquivMicro, 0);

  // Detail charts (mergeFamilies): one "local" series per day = Σ members; the
  // legend loses every local name and keeps every cloud provider.
  const series = mergeFamilies(rows, locals);
  const legend = [...new Set(series.map((r) => r.key))].sort();
  assert.deepEqual(legend, ["anthropic", "ds4", LOCAL_KEY, "openai"]);
  assert.equal(series.find((r) => r.date === "2026-09-01" && r.key === LOCAL_KEY)!.costAPIEquivMicro, 10 + 7 + 3);
  assert.equal(series.find((r) => r.date === "2026-09-02" && r.key === LOCAL_KEY)!.costAPIEquivMicro, 5);
  assert.equal(sumEquiv(series), sumEquiv(rows));

  // Home rollup on the provider channel: the family folds first, then top-N.
  const rolled = sumByKey(rollupRows(rows, 5, locals));
  assert.equal(rolled.find((k) => k.raw === LOCAL_KEY)!.equivMicro, 10 + 7 + 3 + 5);
  assert.equal(rolled.reduce((s, k) => s + k.equivMicro, 0), sumEquiv(rows));
  // A non-provider channel passes no set: nothing folds.
  assert.ok(!rollupRows(rows, 5).some((r) => r.key === LOCAL_KEY));

  // Rail: one collapsible "local" group with the member count; cloud leaves.
  const items = railItems(
    [
      { value: "anthropic", events: 100 },
      { value: "sglang-qwen38", events: 10 },
      { value: "robotlab-low", events: 7 },
      { value: "vllm", events: 8 },
      { value: "ds4", events: 20 },
    ],
    false, FILTER_TOP_N, locals,
  );
  const groups = items.filter((i) => i.kind !== "leaf");
  assert.equal(groups.length, 1);
  assert.equal(groups[0].key, LOCAL_KEY);
  assert.equal(groups[0].events, 10 + 7 + 8);
  assert.equal(groups[0].members.length, 3);
  assert.deepEqual(items.filter((i) => i.kind === "leaf").map((i) => i.value).sort(), ["anthropic", "ds4"]);
});
