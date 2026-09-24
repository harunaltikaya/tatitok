// compareLabel: the "prev" line under the value and token cards.

import { test } from "node:test";
import assert from "node:assert/strict";
import { compareLabel, compactTokens, usd } from "./api.ts";

test("compareLabel: prev in the card's format, signed whole percent", () => {
  assert.equal(compareLabel(12_500_000, 10_000_000, usd), "prev $10.00 (+25%)");
  assert.equal(compareLabel(7_500_000, 10_000_000, usd), "prev $10.00 (-25%)");
  assert.equal(compareLabel(0, 10_000_000, usd), "prev $10.00 (-100%)");
  assert.equal(compareLabel(1_500_000, 1_000_000, compactTokens), "prev 1.00M (+50%)");
  // (current − prev) ÷ prev rounds: 2/3 → 67, and a change under half a
  // percent reads 0 without a sign.
  assert.equal(compareLabel(5, 3, String), "prev 3 (+67%)");
  assert.equal(compareLabel(996, 1000, String), "prev 1000 (0%)");
});

test("compareLabel: no percent when prev is 0", () => {
  assert.equal(compareLabel(10_000_000, 0, usd), "prev $0.00");
  assert.equal(compareLabel(0, 0, compactTokens), "prev 0");
});
