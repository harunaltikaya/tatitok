// Token fresh-vs-cached split test (M8 chunk 1K), Node's built-in runner. The
// live readout sits next to the token totals (range totals + home value card);
// this pins the pure helper: cached = the served cache-read total, fresh =
// everything else, and fresh + cached = total (conservation). Display-only —
// the total is never changed, just split into two parts that sum back to it.

import { test } from "node:test";
import assert from "node:assert/strict";
import { freshCachedSplit } from "./api.ts";
import type { TokenSums } from "./api.ts";

function ts(input: number, output: number, cacheCreation: number, cacheRead: number): TokenSums {
  return { inputTokens: input, outputTokens: output, cacheCreationTokens: cacheCreation, cacheReadTokens: cacheRead };
}

test("freshCachedSplit: fresh + cached = total, cached = cache-read, no-cache → all fresh", () => {
  // total 2000, of which 1820 read from cache.
  const s = freshCachedSplit(ts(100, 50, 30, 1820));
  assert.equal(s.total, 2000);
  assert.equal(s.cached, 1820); // cached = the served cache-read total
  assert.equal(s.fresh, 180); // 100 + 50 + 30
  assert.equal(s.fresh + s.cached, s.total); // conservation
  assert.equal(s.cachedShare, 1820 / 2000);

  // No cache reads → everything fresh, zero cached.
  const z = freshCachedSplit(ts(100, 50, 30, 0));
  assert.equal(z.cached, 0);
  assert.equal(z.fresh, z.total);
  assert.equal(z.fresh, 180);
  assert.equal(z.cachedShare, 0);

  // Empty → no divide-by-zero.
  const e = freshCachedSplit(ts(0, 0, 0, 0));
  assert.equal(e.total, 0);
  assert.equal(e.cachedShare, 0);
});
