// "unpriced" honesty marking: a model none of whose events resolved a rate
// has no price — its $0.00 is unknown, not zero (pure helpers, node --test).
import { test } from "node:test";
import assert from "node:assert/strict";
import { sumByKey, isUnpriced, countUnpriced, mergeFamilies } from "./aggregate.ts";
import type { DailyByRow } from "./api.ts";

const row = (key: string, over: Partial<DailyByRow> = {}): DailyByRow => ({
  date: "2026-09-01", key,
  inputTokens: 100, outputTokens: 10, cacheCreationTokens: 0, cacheReadTokens: 0,
  reasoningTokens: 0, costUSDMicro: 0, costAPIEquivMicro: 0, unpricedEvents: 0, ratedEvents: 0,
  ...over,
});

test("isUnpriced: no rated events and no figures → unpriced; any rate → priced", () => {
  const t = sumByKey([row("claude-opus-5"), row("claude-opus-5", { date: "2026-09-02" })]);
  assert.equal(t.length, 1);
  assert.equal(t[0].rated, 0);
  assert.equal(isUnpriced(t[0]), true);

  const plan = sumByKey([row("claude-fable-5", { ratedEvents: 1, costAPIEquivMicro: 5_000_000 })]);
  assert.equal(isUnpriced(plan[0]), false); // plan_included with an equivalent
  const local = sumByKey([row("qwen3.6-27b", { ratedEvents: 1 })]);
  assert.equal(isUnpriced(local[0]), false); // local: priced at $0 by design
  const partial = sumByKey([row("m", { ratedEvents: 1, unpricedEvents: 3, costUSDMicro: 10 })]);
  assert.equal(isUnpriced(partial[0]), false); // partial gap keeps the asterisk path
});

test("isUnpriced: a mix across days counts as priced when any day rated", () => {
  const t = sumByKey([row("m"), row("m", { date: "2026-09-02", ratedEvents: 2, costAPIEquivMicro: 1 })]);
  assert.equal(t[0].rated, 2);
  assert.equal(isUnpriced(t[0]), false);
});

test("countUnpriced: counts only rows with no price; zero-token rows never count", () => {
  const t = sumByKey([
    row("a"), row("b", { ratedEvents: 1 }), row("c"),
    row("d", { inputTokens: 0, outputTokens: 0 }),
  ]);
  assert.equal(countUnpriced(t), 2);
});

test("mergeFamilies carries ratedEvents through the collapse", () => {
  const merged = mergeFamilies([
    row("vllm-a", { ratedEvents: 1 }), row("vllm-b", { ratedEvents: 2 }),
  ], new Set(["vllm-a", "vllm-b"]));
  const total = merged.reduce((n, r) => n + (r.ratedEvents ?? 0), 0);
  assert.equal(total, 3);
});

test("ratedEvents absent on the wire (older hub) counts as 0, never NaN", () => {
  const r = row("m");
  delete (r as Partial<DailyByRow>).ratedEvents;
  const t = sumByKey([r]);
  assert.equal(t[0].rated, 0);
  assert.equal(isUnpriced(t[0]), true);
});
