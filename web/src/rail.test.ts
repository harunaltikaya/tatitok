// Facet-rail structure tests (M8 chunk 1G), Node's built-in runner. The live
// expand/collapse + localStorage density are exercised in the chunk-G drive;
// this pins the pure machinery in aggregate.ts (railItems): family collapse
// folds the local-basis providers into one "local" group whose count is the
// sum of its members (conservation; membership = the passed set, decided from
// served basis, never the name), non-family dims stay plain leaves, and the long dims fold the
// tail past top-N into an "others" group whose members are the tail. Rendering
// then just reveals a group's members when expanded — display-only, no recount.

import { test } from "node:test";
import assert from "node:assert/strict";
import { railItems, FILTER_TOP_N, LOCAL_KEY } from "./aggregate.ts";
import type { FacetValue } from "./api.ts";

const sumEvents = (items: { events: number }[]) => items.reduce((s, i) => s + i.events, 0);

test("rail: family collapse (conserved) + expandable groups, non-family dims unchanged", () => {
  // A provider-like facet: three local-basis providers of unrelated names +
  // two singletons. provider is NOT a long dim, so no top-N here.
  const providers: FacetValue[] = [
    { value: "anthropic", events: 100 },
    { value: "openai", events: 200 },
    { value: "vllm", events: 5 },
    { value: "sglang-qwen38", events: 10 },
    { value: "laguna-local", events: 7 },
  ];
  const locals = new Set(["vllm", "sglang-qwen38", "laguna-local"]);
  const items = railItems(providers, false, FILTER_TOP_N, locals);

  // ONE collapsed "local" family group (not one per name prefix); its count is
  // the sum of its members.
  const groups = items.filter((i) => i.kind !== "leaf");
  assert.equal(groups.length, 1);
  const local = groups[0];
  assert.equal(local.kind, "family");
  assert.equal(local.key, LOCAL_KEY);
  assert.equal(local.events, 5 + 10 + 7); // conservation: family = Σ members
  assert.deepEqual(
    local.members.map((m) => m.value).sort(),
    ["laguna-local", "sglang-qwen38", "vllm"],
  );
  // Each member is clickable by exact value inside the group, never a
  // separate top-level row.
  assert.ok(!items.some((i) => i.kind === "leaf" && locals.has(i.value)));
  // Without the set (a non-provider dim) nothing collapses.
  assert.ok(railItems(providers, false, FILTER_TOP_N).every((i) => i.kind === "leaf"));

  // Singletons stay leaves; the whole rail conserves the total event count.
  const leaves = items.filter((i) => i.kind === "leaf");
  assert.deepEqual(leaves.map((l) => l.value).sort(), ["anthropic", "openai"]);
  assert.equal(sumEvents(items), 100 + 200 + 5 + 10 + 7);

  // A non-family dim collapses nothing: every entry is a plain leaf, set intact.
  const harness: FacetValue[] = [
    { value: "claude-code", events: 50 },
    { value: "codex", events: 30 },
    { value: "opencode", events: 20 },
  ];
  const hItems = railItems(harness, false, FILTER_TOP_N);
  assert.ok(hItems.every((i) => i.kind === "leaf"));
  assert.deepEqual(hItems.map((i) => i.value).sort(), ["claude-code", "codex", "opencode"]);
  assert.equal(sumEvents(hItems), 100);

  // A long (rolled) dim folds the tail past top-N into ONE "others" group whose
  // members are the tail (each still filters by its exact value) — expanding it
  // reveals them. Conserves the dimension total.
  const models: FacetValue[] = Array.from({ length: FILTER_TOP_N + 4 }, (_, i) => ({ value: `m${i}`, events: 100 - i }));
  const mItems = railItems(models, true, FILTER_TOP_N);
  assert.equal(mItems.length, FILTER_TOP_N + 1); // top-N leaves + one others group
  const others = mItems.find((i) => i.kind === "others");
  assert.ok(others, "expected an others group on the long dim");
  assert.equal(others.members.length, 4); // the folded tail
  assert.equal(sumEvents(mItems), sumEvents(models)); // top-N + others = total
});
