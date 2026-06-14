// Activity-grid test (M8 chunk 1L), Node's built-in runner. The live heatmap
// is exercised in the chunk-L drive; this pins the pure assembly in
// aggregate.ts (activityGrid): the served ≤168 buckets densify into a 7×24
// matrix (missing cells = 0), and Σ cells = Σ buckets (conservation — the grid
// is just a re-presentation of the served counts, no recount).

import { test } from "node:test";
import assert from "node:assert/strict";
import { activityGrid } from "./aggregate.ts";
import type { ActivityBucket } from "./api.ts";

test("activityGrid: buckets → dense 7×24, missing = 0, Σ cells = Σ buckets", () => {
  const buckets: ActivityBucket[] = [
    { weekday: 1, hour: 9, events: 5, tokens: 500 }, // Mon 09:00
    { weekday: 1, hour: 20, events: 3, tokens: 300 }, // Mon 20:00
    { weekday: 0, hour: 0, events: 2, tokens: 200 }, // Sun 00:00
  ];
  const grid = activityGrid(buckets, (b) => b.events);

  // Dense 7×24.
  assert.equal(grid.length, 7);
  assert.ok(grid.every((row) => row.length === 24));
  // Buckets land in their (weekday, hour) cell.
  assert.equal(grid[1][9], 5);
  assert.equal(grid[1][20], 3);
  assert.equal(grid[0][0], 2);
  // Absent buckets are 0.
  assert.equal(grid[3][12], 0);
  assert.equal(grid[1][0], 0);

  // Conservation: Σ cells = Σ buckets (events), and likewise for tokens.
  const cellSum = grid.flat().reduce((s, v) => s + v, 0);
  assert.equal(cellSum, buckets.reduce((s, b) => s + b.events, 0));
  const tokenGrid = activityGrid(buckets, (b) => b.tokens);
  assert.equal(tokenGrid.flat().reduce((s, v) => s + v, 0), buckets.reduce((s, b) => s + b.tokens, 0));
});
