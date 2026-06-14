// Economic-class tests (M8 chunk 1E), Node's built-in runner. The live
// ClassDots are exercised in the chunk-E drive; this pins the pure machinery in
// aggregate.ts: basisToClass maps each served basis to its class (unknown →
// none), and dotClassesFor decides which dots a row gets — a model row carries
// its class(es), an aggregate row ("others") carries none. Frontend-only,
// derived from already-served data: no number changes.

import { test } from "node:test";
import assert from "node:assert/strict";
import { basisToClass, dotClassesFor } from "./aggregate.ts";

test("class: basisToClass maps served basis → class; dots only on rows with a known basis", () => {
  // Every served basis maps to its economic class; unknown/unrecognised → none.
  assert.equal(basisToClass("api_price"), "metered");
  assert.equal(basisToClass("plan_included"), "subscription");
  assert.equal(basisToClass("local"), "local");
  assert.equal(basisToClass("free"), "free");
  assert.equal(basisToClass("unknown"), "none");
  assert.equal(basisToClass(""), "none");
  assert.equal(basisToClass("a-future-basis"), "none");

  const bases = new Map<string, string[]>([
    ["claude-sonnet-4-6", ["plan_included"]],
    ["gpt-5", ["api_price"]],
    ["qwen3", ["local"]],
    ["mixed-model", ["plan_included", "api_price"]],
    ["odd-model", ["unknown"]],
  ]);

  // A model row shows the dot for its class.
  assert.deepEqual(dotClassesFor("claude-sonnet-4-6", bases), ["subscription"]);
  assert.deepEqual(dotClassesFor("gpt-5", bases), ["metered"]);
  assert.deepEqual(dotClassesFor("qwen3", bases), ["local"]);
  // A model spanning bases shows one dot per distinct class (deduped, in order).
  assert.deepEqual(dotClassesFor("mixed-model", bases), ["subscription", "metered"]);
  // Unknown basis → a single "none" (neutral) dot, never a class colour.
  assert.deepEqual(dotClassesFor("odd-model", bases), ["none"]);
  // Aggregate rows ("others") and anything without served basis → no dot.
  assert.deepEqual(dotClassesFor("others", bases), []);
  assert.deepEqual(dotClassesFor("anything", undefined), []);
});
