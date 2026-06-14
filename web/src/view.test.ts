// View URL-state tests (M8 chunk 1A), Node's built-in runner. The header
// nav wiring + the live home/detail swap are exercised in the chunk-A
// drive; this pins the pure machinery: parseView validation and the view's
// serialize→parse round-trip through the shared URL helpers — and guards the
// invariant that a default-home link stays byte-identical to the pre-1A URL
// (no view= param), so existing bookmarks keep working unchanged.

import { test } from "node:test";
import assert from "node:assert/strict";
import { parseView, filtersToURL, filtersFromURL, emptyFilters } from "./filters.ts";

test("view URL-state: parseView, home omits view=, detail writes it, survives a round-trip", () => {
  // parseView accepts the two known pages and defaults everything else home.
  assert.equal(parseView("home"), "home");
  assert.equal(parseView("detail"), "detail");
  assert.equal(parseView(null), "home");
  assert.equal(parseView("bogus"), "home");

  const f = emptyFilters();
  const from = "2026-06-01";
  const to = "2026-06-14";
  const tz = "UTC";

  // Default home stays OUT of the URL — byte-identical to the pre-1A
  // serialization (from/to/tz only), so existing home bookmarks are unchanged.
  const homeURL = filtersToURL(f, from, to, tz, "home");
  assert.equal(homeURL, `?from=${from}&to=${to}&tz=${tz}`);
  assert.ok(!homeURL.includes("view="));

  // Detail writes the param.
  const detailURL = filtersToURL(f, from, to, tz, "detail");
  assert.ok(detailURL.includes("view=detail"));

  // Both survive serialize → parse (the refresh / popstate path).
  assert.equal(filtersFromURL(homeURL).view, "home");
  assert.equal(filtersFromURL(detailURL).view, "detail");
});
