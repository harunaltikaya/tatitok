// Facet-rail structure tests (M8 chunk 1G), Node's built-in runner. The live
// expand/collapse + localStorage density are exercised in the chunk-G drive;
// this pins the pure machinery in aggregate.ts (railItems): family collapse
// folds vllm-* into one "vllm" group whose count is the sum of its members
// (conservation), non-family dims stay plain leaves, and the long dims fold the
// tail past top-N into an "others" group whose members are the tail. Rendering
// then just reveals a group's members when expanded — display-only, no recount.

import { test } from "node:test";
import assert from "node:assert/strict";
import { railItems, FILTER_TOP_N } from "./aggregate.ts";
import type { FacetValue } from "./api.ts";

const sumEvents = (items: { events: number }[]) => items.reduce((s, i) => s + i.events, 0);

test("rail: family collapse (conserved) + expandable groups, non-family dims unchanged", () => {
  // A provider-like facet: a vllm family (incl. the bare "vllm" member) + two
  // singletons. provider is NOT a long dim, so no top-N here.
  const providers: FacetValue[] = [
    { value: "anthropic", events: 100 },
    { value: "openai", events: 200 },
    { value: "vllm", events: 5 },
    { value: "vllm-35b", events: 10 },
    { value: "vllm-delegate", events: 7 },
  ];
  const items = railItems(providers, false, FILTER_TOP_N);

  // One collapsed "vllm" family group; its count is the sum of its members.
  const vllm = items.find((i) => i.kind !== "leaf" && i.key === "vllm");
  assert.ok(vllm, "expected a collapsed vllm family group");
  assert.equal(vllm.kind, "family");
  assert.equal(vllm.events, 5 + 10 + 7); // conservation: family = Σ members
  assert.deepEqual(
    vllm.members.map((m) => m.value).sort(),
    ["vllm", "vllm-35b", "vllm-delegate"],
  );
  // The bare "vllm" provider is a member (clickable by exact value), not a
  // separate top-level row.
  assert.ok(vllm.members.some((m) => m.value === "vllm"));
  assert.ok(!items.some((i) => i.kind === "leaf" && i.value === "vllm"));

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
