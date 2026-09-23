// Default day range = last 7 days in the selected zone; explicit URL wins;
// preset detection (the "lit" button) — pure helpers, no .tsx.
import { test } from "node:test";
import assert from "node:assert/strict";
import { defaultRange, initialRange, activePreset, presetRange, daysAgo, shiftDay, ALL_FROM } from "./range.ts";

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

// The review-0903 MED: subtracting 24 h periods in UTC before zoning skipped a
// local calendar day across a spring-forward in a negative-offset zone. The US
// 2026 spring-forward is Sunday 2026-03-08 (02:00 PST → 03:00 PDT).
test("daysAgo: a 7d preset spans exactly 7 calendar days across the 2026 US spring-forward", () => {
  const tz = "America/Los_Angeles";
  const calendarDays = (from: string, to: string) =>
    (Date.UTC(+to.slice(0, 4), +to.slice(5, 7) - 1, +to.slice(8)) - Date.UTC(+from.slice(0, 4), +from.slice(5, 7) - 1, +from.slice(8))) / 864e5 + 1;
  // 00:30 PDT on Monday 2026-03-09 = 07:30Z; the old math gave from=03-02
  // (eight days, starting one day early) because 07:30Z − 6×24 h =
  // 03-03T07:30Z = 23:30 PST on 03-02.
  const now = new Date("2026-03-09T07:30:00Z");
  assert.equal(daysAgo(tz, 0, now), "2026-03-09");
  const r = presetRange("7d", tz, now);
  assert.deepEqual(r, { from: "2026-03-03", to: "2026-03-09" });
  assert.equal(calendarDays(r.from, r.to), 7);
  // Every hour of the DST week: the preset is 7 calendar days, the endpoints
  // are the zoned today and today−6, and the lit button round-trips.
  for (let h = 0; h < 24 * 9; h++) {
    const t = new Date(Date.UTC(2026, 2, 6, h, 30));
    const p = presetRange("7d", tz, t);
    assert.equal(calendarDays(p.from, p.to), 7, t.toISOString());
    assert.equal(p.to, daysAgo(tz, 0, t));
    assert.equal(daysAgo(tz, 6, t), shiftDay(p.to, -6));
    assert.equal(activePreset(p.from, p.to, tz, t), "7d");
  }
  // shiftDay is plain calendar arithmetic: month and year edges, both ways.
  assert.equal(shiftDay("2026-03-01", -1), "2026-02-28");
  assert.equal(shiftDay("2026-01-01", -1), "2025-12-31");
  assert.equal(shiftDay("2025-12-31", 1), "2026-01-01");
  assert.equal(shiftDay("2024-03-01", -1), "2024-02-29");
});
