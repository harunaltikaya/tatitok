// Layout-model tests (M6 Task 4), run by Node's built-in test runner.
// The interaction wiring (drag, resize handle, fullscreen) lives in the
// React components and is exercised in the hard-stop-1 drive; this pins
// the pure machinery: persistence round-trip, forward-compatible merge,
// clamping, reorder.

import { test } from "node:test";
import assert from "node:assert/strict";
import {
  GRID_COLS,
  PANELS,
  clampSpan,
  defaultLayout,
  loadLayout,
  reorder,
  serializeLayout,
  setSpan,
} from "./layout.ts";

test("defaultLayout mirrors the canonical panel set", () => {
  const l = defaultLayout();
  assert.deepEqual(l.map((p) => p.id), PANELS.map((p) => p.id));
  assert.deepEqual(l.map((p) => p.span), PANELS.map((p) => p.defaultSpan));
});

test("clampSpan keeps spans in [1, GRID_COLS] and survives garbage", () => {
  assert.equal(clampSpan(0), 1);
  assert.equal(clampSpan(GRID_COLS + 5), GRID_COLS);
  assert.equal(clampSpan(2), 2);
  assert.equal(clampSpan("nope"), 1);
  assert.equal(clampSpan(undefined), 1);
});

test("loadLayout round-trips a serialized layout", () => {
  const saved = setSpan(reorder(defaultLayout(), "break-model", "chart-equiv"), "chart-equiv", 4);
  const back = loadLayout(serializeLayout(saved));
  assert.deepEqual(back, saved);
});

test("loadLayout falls back to default on missing/corrupt input", () => {
  assert.deepEqual(loadLayout(null), defaultLayout());
  assert.deepEqual(loadLayout("not json"), defaultLayout());
  assert.deepEqual(loadLayout("{}"), defaultLayout()); // not an array
});

test("loadLayout is forward-compatible: drops unknown ids, appends new panels, clamps spans", () => {
  // A save from an older/newer build: one stale panel, one known panel
  // with an out-of-range span, and the rest of the canonical set missing.
  const raw = JSON.stringify([
    { id: "gone-panel", span: 2 },
    { id: "chart-tokens", span: 99 },
  ]);
  const merged = loadLayout(raw);
  // stale id dropped; chart-tokens kept first with span clamped; every
  // other canonical panel appended in default order.
  assert.equal(merged[0].id, "chart-tokens");
  assert.equal(merged[0].span, GRID_COLS);
  const ids = merged.map((p) => p.id);
  assert.ok(!ids.includes("gone-panel"));
  for (const p of PANELS) assert.ok(ids.includes(p.id), `${p.id} must be present`);
  assert.equal(merged.length, PANELS.length);
});

test("reorder moves a panel to the target position; no-ops on unknown ids", () => {
  const l = defaultLayout();
  const moved = reorder(l, "break-model", "chart-equiv");
  assert.equal(moved[0].id, "break-model");
  assert.equal(moved.length, l.length);
  assert.deepEqual(reorder(l, "nope", "chart-equiv"), l);
});
