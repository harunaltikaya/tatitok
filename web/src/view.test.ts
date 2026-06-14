// View URL-state tests (M8 chunk 1A), Node's built-in runner. The header
// nav wiring + the live home/detail swap are exercised in the chunk-A
// drive; this pins the pure machinery: parseView validation and the view's
// serialize→parse round-trip through the shared URL helpers — and guards the
// invariant that a default-home link stays byte-identical to the pre-1A URL
// (no view= param), so existing bookmarks keep working unchanged.

import { test } from "node:test";
import assert from "node:assert/strict";
import { parseView, filtersToURL, filtersFromURL, emptyFilters, DEFAULT_SORT } from "./filters.ts";

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

  // Default home (+ default-model group-by) stays OUT of the URL —
  // byte-identical to the pre-1A serialization (from/to/tz only), so existing
  // home bookmarks are unchanged.
  const homeURL = filtersToURL(f, from, to, tz, "home", "model", DEFAULT_SORT);
  assert.equal(homeURL, `?from=${from}&to=${to}&tz=${tz}`);
  assert.ok(!homeURL.includes("view="));

  // Detail writes the param.
  const detailURL = filtersToURL(f, from, to, tz, "detail", "model", DEFAULT_SORT);
  assert.ok(detailURL.includes("view=detail"));

  // Both survive serialize → parse (the refresh / popstate path).
  assert.equal(filtersFromURL(homeURL).view, "home");
  assert.equal(filtersFromURL(detailURL).view, "detail");
});

test("view toggle preserves active filters in the URL", () => {
  // Permanent regression guard: the home/detail switch flips ONLY `view` and
  // keeps EVERY active facet filter. The nav drives this via setView →
  // filtersToURL(filters, …), so a future change to the nav or filtersToURL
  // that dropped a facet param on the switch would fail here. Asserted on the
  // pure round-trip both the URL effect and popstate use.
  const f = {
    ...emptyFilters(),
    harness: ["claude-code"],
    provider: ["anthropic"],
    model: ["claude-sonnet-4-6", "claude-opus-4-8"], // multi-value within a dim
    project: ["tatitok"],
    basis: ["plan_included"],
  };
  const from = "2026-06-01";
  const to = "2026-06-14";
  const tz = "UTC";

  const homeURL = filtersToURL(f, from, to, tz, "home", "model", DEFAULT_SORT);
  const detailURL = filtersToURL(f, from, to, tz, "detail", "model", DEFAULT_SORT);

  // Only `view` differs across the toggle.
  assert.ok(!homeURL.includes("view="));
  assert.ok(detailURL.includes("view=detail"));

  // Every facet param survives the round-trip on BOTH pages — including detail,
  // where the reported bug had them dropped.
  assert.deepEqual(filtersFromURL(homeURL).filters, f);
  assert.deepEqual(filtersFromURL(detailURL).filters, f);
  // …and the switch keeps the SAME filter set (nothing dropped home→detail).
  assert.deepEqual(filtersFromURL(detailURL).filters, filtersFromURL(homeURL).filters);
});
