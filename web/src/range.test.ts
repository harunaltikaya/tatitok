// Default day range = last 7 days in the selected zone; explicit URL wins;
// preset detection (the "lit" button) — pure helpers, no .tsx.
import { test } from "node:test";
import assert from "node:assert/strict";
import { defaultRange, initialRange, activePreset, presetRange, daysAgo, shiftDay, precedingRange, ALL_FROM } from "./range.ts";
import { filtersFromURL } from "./filters.ts";

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

// precedingRange: the period compare's range — same calendar-day count,
// ending the day before `from`.
test("precedingRange: a 7d range across the 2026 US spring-forward, one day, a month start", () => {
  const tz = "America/Los_Angeles";
  const calendarDays = (from: string, to: string) =>
    (Date.UTC(+to.slice(0, 4), +to.slice(5, 7) - 1, +to.slice(8)) - Date.UTC(+from.slice(0, 4), +from.slice(5, 7) - 1, +from.slice(8))) / 864e5 + 1;
  // The 7d preset at 00:30 PDT on 2026-03-09 spans the switch (Sunday
  // 03-08, 02:00 PST → 03:00 PDT); so does the range after it, whose
  // preceding period is the DST week itself.
  const r = presetRange("7d", tz, new Date("2026-03-09T07:30:00Z"));
  assert.deepEqual(r, { from: "2026-03-03", to: "2026-03-09" });
  assert.deepEqual(precedingRange(r.from, r.to, tz), { from: "2026-02-24", to: "2026-03-02" });
  assert.deepEqual(precedingRange("2026-03-10", "2026-03-16", tz), r);
  // Every hour of the DST week: 7 calendar days, ending the day before from.
  for (let h = 0; h < 24 * 9; h++) {
    const t = new Date(Date.UTC(2026, 2, 6, h, 30));
    const cur = presetRange("7d", tz, t);
    const p = precedingRange(cur.from, cur.to, tz);
    assert.equal(calendarDays(p.from, p.to), 7, t.toISOString());
    assert.equal(p.to, shiftDay(cur.from, -1));
  }
  // One day → the day before.
  assert.deepEqual(precedingRange("2026-09-24", "2026-09-24", tz), { from: "2026-09-23", to: "2026-09-23" });
  // A range crossing a month start (Feb has 28 days in 2026), and a 30d
  // range whose preceding period crosses one.
  assert.deepEqual(precedingRange("2026-02-26", "2026-03-04", "UTC"), { from: "2026-02-19", to: "2026-02-25" });
  assert.deepEqual(precedingRange("2026-09-05", "2026-10-04", "UTC"), { from: "2026-08-06", to: "2026-09-04" });
  assert.equal(calendarDays("2026-08-06", "2026-09-04"), 30);
});

// Review 0924g MED: loadRange calls precedingRange synchronously, outside
// the fetch error handler, so a hand-edited URL range must yield null (no
// compare request) instead of throwing from shiftDay; the main fetch's 400
// then reaches the normal error UI.
test("precedingRange: null for invalid or pre-1970 bounds, never throws", () => {
  const to = "2026-09-24";
  assert.equal(precedingRange("bad", to, "UTC"), null);
  assert.equal(precedingRange("", to, "UTC"), null);
  assert.equal(precedingRange(ALL_FROM, to, "UTC"), null); // the "all" preset
  assert.equal(precedingRange("1969-12-31", to, "UTC"), null);
  assert.equal(precedingRange("2026-09-18", "bad", "UTC"), null);
  // Date.parse accepts these, shiftDay would not.
  assert.equal(precedingRange("2026-09", to, "UTC"), null);
  assert.equal(precedingRange("2026", to, "UTC"), null);
  // A valid range is unchanged; the day after ALL_FROM still compares.
  assert.deepEqual(precedingRange("2026-09-18", to, "UTC"), { from: "2026-09-11", to: "2026-09-17" });
  assert.deepEqual(precedingRange("1970-01-02", "1970-01-02", "UTC"), { from: "1970-01-01", to: "1970-01-01" });
  const all = presetRange("all", "UTC", now);
  assert.equal(precedingRange(all.from, all.to, "UTC"), null);
});

// Codex's path: the URL through the same parse the app uses
// (filtersFromURL → initialRange), then precedingRange.
test("precedingRange: ?from=bad and an empty from, parsed like the app", () => {
  for (const search of ["?from=bad&to=2026-09-24", "?from=&to=2026-09-24"]) {
    const u = filtersFromURL(search);
    const r = initialRange(u.from, u.to, "UTC", now);
    assert.equal(r.to, "2026-09-24", search);
    assert.doesNotThrow(() => precedingRange(r.from, r.to, "UTC"), search);
    assert.equal(precedingRange(r.from, r.to, "UTC"), null, search);
  }
});
