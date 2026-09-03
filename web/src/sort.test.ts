// Table-sort tests (M8 chunk 1D), Node's built-in runner. The clickable
// headers + live reorder are exercised in the chunk-D drive; this pins the
// pure machinery: parseSort validation, the sort serialize→parse round-trip
// (default equiv-desc stays out of the URL, like view/groupBy), and the
// reorder — sortTotals ranks by the chosen metric while aggregates ("others"
// and collapsed family buckets) stay pinned to the bottom in either direction.
// Sort is reorder only: no number changes, conservation untouched.

import { test } from "node:test";
import assert from "node:assert/strict";
import { parseSort, filtersToURL, filtersFromURL, emptyFilters, DEFAULT_SORT } from "./filters.ts";
import { sortTotals, OTHERS_KEY, LOCAL_KEY, type KeyTotals } from "./aggregate.ts";

function kt(key: string, equiv: number, tokens: number, cost = 0): KeyTotals {
  return { key, raw: key, tokens, costMicro: cost, equivMicro: equiv, unpriced: 0 };
}

test("sort: parseSort round-trip, default equiv-desc omitted, reorder keeps aggregates last", () => {
  // parseSort accepts the metric × direction matrix; null/unknown → the default.
  assert.deepEqual(parseSort("equiv", "desc"), { key: "equiv", dir: "desc" });
  assert.deepEqual(parseSort("tokens", "asc"), { key: "tokens", dir: "asc" });
  assert.deepEqual(parseSort("equiv", "asc"), { key: "equiv", dir: "asc" });
  assert.deepEqual(parseSort("tokens", "desc"), { key: "tokens", dir: "desc" });
  assert.deepEqual(parseSort(null, null), DEFAULT_SORT);
  assert.deepEqual(parseSort("bogus", "bogus"), DEFAULT_SORT);

  const f = emptyFilters();
  const from = "2026-06-01";
  const to = "2026-06-14";
  const tz = "UTC";

  // Default equiv-desc stays OUT of the URL (byte-identical with the default
  // view + group-by), so the common link is the shortest.
  const defURL = filtersToURL(f, from, to, tz, "home", "model", DEFAULT_SORT);
  assert.equal(defURL, `?from=${from}&to=${to}&tz=${tz}`);
  assert.ok(!defURL.includes("sort=") && !defURL.includes("dir="));

  // A non-default key+dir writes both and survives serialize→parse.
  const tokAsc = filtersToURL(f, from, to, tz, "home", "model", { key: "tokens", dir: "asc" });
  assert.ok(tokAsc.includes("sort=tokens") && tokAsc.includes("dir=asc"));
  assert.deepEqual(filtersFromURL(tokAsc).sort, { key: "tokens", dir: "asc" });

  // The default key (equiv) with a non-default dir writes only dir.
  const equivAsc = filtersToURL(f, from, to, tz, "home", "model", { key: "equiv", dir: "asc" });
  assert.ok(!equivAsc.includes("sort=") && equivAsc.includes("dir=asc"));
  assert.deepEqual(filtersFromURL(equivAsc).sort, { key: "equiv", dir: "asc" });

  // Reorder: real entities rank by the metric; the collapsed family bucket
  // ("local") sits below them; "others" is always dead last — both directions.
  const rows = [
    kt("anthropic", 500, 5_000),
    kt("openai", 900, 1_000),
    kt(OTHERS_KEY, 9_999, 9_999), // an aggregate, even if huge → stays last
    kt(LOCAL_KEY, 700, 8_000), // collapsed family → below real entities
    kt("deepseek", 300, 2_000),
  ];
  const keysOf = (s: KeyTotals[]) => s.map((t) => t.key);
  assert.deepEqual(
    keysOf(sortTotals(rows, { key: "equiv", dir: "desc" })),
    ["openai", "anthropic", "deepseek", LOCAL_KEY, OTHERS_KEY],
  );
  assert.deepEqual(
    keysOf(sortTotals(rows, { key: "equiv", dir: "asc" })),
    ["deepseek", "anthropic", "openai", LOCAL_KEY, OTHERS_KEY],
  );
  // tokens key ranks by tokens; aggregates still pinned last.
  assert.deepEqual(
    keysOf(sortTotals(rows, { key: "tokens", dir: "desc" })),
    ["anthropic", "deepseek", "openai", LOCAL_KEY, OTHERS_KEY],
  );
});
