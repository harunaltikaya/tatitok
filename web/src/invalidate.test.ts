// Live-invalidation tests (M6 Codex F1), Node's built-in runner.

import { test } from "node:test";
import assert from "node:assert/strict";
import { localDaysForUTCDay, touchedInRange } from "./invalidate.ts";

test("UTC day maps to both local days it overlaps (Tokyo +09)", () => {
  // 2026-06-12 UTC: 00:00Z = 09:00 local 06-12; 23:59:59.999Z = 08:59 local 06-13.
  assert.deepEqual(localDaysForUTCDay("2026-06-12", "Asia/Tokyo"), ["2026-06-12", "2026-06-13"]);
});

test("UTC day maps to a single local day in UTC", () => {
  assert.deepEqual(localDaysForUTCDay("2026-06-12", "UTC"), ["2026-06-12"]);
});

// The F1 boundary case: a Tokyo viewer looking only at 2026-06-13; a UTC
// touched-day 2026-06-12 (which holds the 22:30Z event = local 06-13)
// must invalidate that view. The old direct string compare did not.
test("touchedInRange invalidates a next-day local view across the UTC boundary", () => {
  assert.equal(touchedInRange(["2026-06-12"], "Asia/Tokyo", "2026-06-13", "2026-06-13"), true);
});

test("touchedInRange: no overlap → no refetch", () => {
  assert.equal(touchedInRange(["2026-06-01"], "Asia/Tokyo", "2026-06-13", "2026-06-13"), false);
});

test("touchedInRange: empty touched (reconnect / lost events) → refetch", () => {
  assert.equal(touchedInRange([], "UTC", "2026-06-01", "2026-06-30"), true);
});
