// Default day range = last 7 days in the selected zone; explicit URL wins;
// preset detection (the "lit" button) — pure helpers, no .tsx.
import { test } from "node:test";
import assert from "node:assert/strict";
import { defaultRange, initialRange, activePreset, presetRange, daysAgo, ALL_FROM } from "./range.ts";

const now = new Date("2026-09-03T11:30:00Z"); // 14:30 in Europe/Istanbul, 04:30 in America/Los_Angeles

test("defaultRange: today−6d → today in the selected timezone", () => {
  assert.deepEqual(defaultRange("UTC", now), { from: "2026-08-28", to: "2026-09-03" });
  assert.deepEqual(defaultRange("Europe/Istanbul", now), { from: "2026-08-28", to: "2026-09-03" });
  // 01:30 UTC is still the previous day in Los Angeles: the range shifts.
  const early = new Date("2026-09-03T01:30:00Z");
  assert.deepEqual(defaultRange("America/Los_Angeles", early), { from: "2026-08-27", to: "2026-09-02" });
  assert.deepEqual(defaultRange("UTC", early), { from: "2026-08-28", to: "2026-09-03" });
});

test("initialRange: URL values win, missing ones default", () => {
  assert.deepEqual(initialRange("2026-06-11", "2026-06-17", "UTC", now), { from: "2026-06-11", to: "2026-06-17" });
  assert.deepEqual(initialRange(null, null, "UTC", now), { from: "2026-08-28", to: "2026-09-03" });
  assert.deepEqual(initialRange("2026-06-11", null, "UTC", now), { from: "2026-06-11", to: "2026-09-03" });
});

test("activePreset: the default lights 7d; presets round-trip; custom is null", () => {
  const d = defaultRange("UTC", now);
  assert.equal(activePreset(d.from, d.to, "UTC", now), "7d");
  for (const label of ["7d", "30d", "90d", "all"] as const) {
    const r = presetRange(label, "UTC", now);
    assert.equal(activePreset(r.from, r.to, "UTC", now), label);
  }
  assert.equal(activePreset("2026-06-11", "2026-06-17", "UTC", now), null); // a stale June range
  assert.equal(activePreset("2026-08-28", "2026-09-02", "UTC", now), null); // 7 days not ending today
});

test("presetRange: spans are inclusive day counts; all opens at 1970", () => {
  assert.deepEqual(presetRange("30d", "UTC", now), { from: "2026-08-05", to: "2026-09-03" });
  assert.deepEqual(presetRange("90d", "UTC", now), { from: "2026-06-06", to: "2026-09-03" });
  assert.deepEqual(presetRange("all", "UTC", now), { from: ALL_FROM, to: "2026-09-03" });
  assert.equal(daysAgo("UTC", 0, now), "2026-09-03");
});
