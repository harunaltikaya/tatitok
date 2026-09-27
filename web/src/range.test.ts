// Default day range = last 7 days in the selected zone; explicit URL wins;
// preset detection (the "lit" button) — pure helpers, no .tsx.
import { test } from "node:test";
import assert from "node:assert/strict";
import { defaultRange, initialRange, activePreset, presetRange, daysAgo, shiftDay, precedingRange, parsePreset, rangeFromURL, zonedRangeFromURL, rangeInZone, presets, ALL_FROM } from "./range.ts";
import { filtersFromURL, filtersToURL, emptyFilters, DEFAULT_SORT } from "./filters.ts";

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

// Round G: a preset is stored by name. A 7d click writes range=7d and no
// from/to, and opening that URL resolves the dates at open time, so a tab
// reopened the next zone day shows the new day.
test("range param: a preset writes range=<label> and no from/to; a calendar pick writes dates", () => {
  const f = emptyFilters();
  const r = presetRange("7d", "Europe/Istanbul", now);
  assert.equal(filtersToURL(f, r.from, r.to, "Europe/Istanbul", "home", "model", DEFAULT_SORT, "7d"), "?range=7d&tz=Europe%2FIstanbul");
  const all = presetRange("all", "UTC", now);
  assert.equal(filtersToURL(f, all.from, all.to, "UTC", "detail", "model", DEFAULT_SORT, "all"), "?range=all&tz=UTC&view=detail");
  // No preset (the calendar, or a from/to link): dates, as before.
  assert.equal(filtersToURL(f, "2026-06-11", "2026-06-17", "UTC", "home", "model", DEFAULT_SORT, null), "?from=2026-06-11&to=2026-06-17&tz=UTC");
  assert.equal(filtersToURL(f, "2026-06-11", "2026-06-17", "UTC", "home", "model", DEFAULT_SORT), "?from=2026-06-11&to=2026-06-17&tz=UTC");
  // Other params ride along unchanged.
  const withFilter = { ...f, harness: ["codex"] };
  assert.equal(filtersToURL(withFilter, r.from, r.to, "UTC", "home", "harness", DEFAULT_SORT, "30d"), "?range=30d&tz=UTC&groupBy=harness&harness=codex");
});

test("range param: parse resolves at open time; unknown or absent is the 7d default", () => {
  assert.equal(parsePreset("7d"), "7d");
  assert.equal(parsePreset("all"), "all");
  assert.equal(parsePreset("8d"), null);
  assert.equal(parsePreset(""), null);
  assert.equal(parsePreset(null), null);
  const open = (search: string, at: Date) => {
    const u = filtersFromURL(search);
    return rangeFromURL(u.from, u.to, u.range, "Europe/Istanbul", at);
  };
  // Clicked late on 09-27, reopened just after midnight on 09-28 (+03).
  const url = filtersToURL(emptyFilters(), "2026-09-21", "2026-09-27", "Europe/Istanbul", "home", "model", DEFAULT_SORT, "7d");
  assert.deepEqual(open(url, new Date("2026-09-27T20:30:00Z")), { from: "2026-09-21", to: "2026-09-27", preset: "7d" });
  assert.deepEqual(open(url, new Date("2026-09-27T21:30:00Z")), { from: "2026-09-22", to: "2026-09-28", preset: "7d" });
  assert.deepEqual(open("?range=30d&tz=Europe%2FIstanbul", now), { ...presetRange("30d", "Europe/Istanbul", now), preset: "30d" });
  assert.deepEqual(open("?range=all", now), { from: ALL_FROM, to: "2026-09-03", preset: "all" });
  // Unknown value: ignored, the default applies. No range at all: the default.
  const def = { ...defaultRange("Europe/Istanbul", now), preset: "7d" };
  assert.deepEqual(open("?range=8d", now), def);
  assert.deepEqual(open("?range=", now), def);
  assert.deepEqual(open("?tz=Europe%2FIstanbul", now), def);
  assert.deepEqual(open("", now), def);
});

// Precedence: explicit from/to win over range=. The dates are the absolute
// statement and every link written before round G carries only them; the
// app never writes both, so both appear only in a hand-edited link.
test("range param: from/to win over range= when both are present", () => {
  const open = (search: string) => {
    const u = filtersFromURL(search);
    return rangeFromURL(u.from, u.to, u.range, "UTC", now);
  };
  assert.deepEqual(open("?from=2026-06-11&to=2026-06-17&tz=UTC"), { from: "2026-06-11", to: "2026-06-17", preset: null });
  assert.deepEqual(open("?range=30d&from=2026-06-11&to=2026-06-17"), { from: "2026-06-11", to: "2026-06-17", preset: null });
  // One date present still wins; the missing one falls back as before.
  assert.deepEqual(open("?range=30d&from=2026-06-11"), { from: "2026-06-11", to: "2026-09-03", preset: null });
  // An empty from is still a from (served as no start date, soul §6).
  assert.deepEqual(open("?range=30d&from=&to=2026-09-24"), { from: "", to: "2026-09-24", preset: null });
});

// Review 0928a F2. App's zone handler calls rangeInZone and its popstate
// handler calls zonedRangeFromURL; the transitions themselves need a React
// renderer this harness does not have, so the helpers are tested. Rule: at
// any instant, the URL and the screen name the same dates.
const late = new Date("2026-09-27T21:30:00Z"); // 00:30 on 09-28 in Istanbul, 21:30 on 09-27 in UTC

// reopen: the dates a fresh open of the URL shows at the same instant.
const reopen = (url: string, browserZone: string, at: Date) => zonedRangeFromURL(filtersFromURL(url), browserZone, at);

test("zone change: an active preset is recomputed in the new zone; calendar dates stay", () => {
  const f = emptyFilters();
  const ist = presetRange("7d", "Europe/Istanbul", late);
  assert.deepEqual(ist, { from: "2026-09-22", to: "2026-09-28" });
  // Istanbul -> UTC with 7d active: the screen moves to UTC's 7 days ...
  const r = rangeInZone("7d", ist.from, ist.to, "UTC", late);
  assert.deepEqual(r, { from: "2026-09-21", to: "2026-09-27" });
  // ... which is what ?range=7d&tz=UTC opens on at that instant.
  const url = filtersToURL(f, r.from, r.to, "UTC", "home", "model", DEFAULT_SORT, "7d");
  assert.equal(url, "?range=7d&tz=UTC");
  assert.deepEqual(reopen(url, "Europe/Istanbul", late), { tz: "UTC", ...r, preset: "7d" });
  // A calendar range keeps its dates in any zone, and its URL carries them.
  assert.deepEqual(rangeInZone(null, "2026-09-01", "2026-09-05", "UTC", late), { from: "2026-09-01", to: "2026-09-05" });
  // Every preset, every zone pair: the screen after the change equals a
  // reopen of the URL written after it.
  const zones = ["Europe/Istanbul", "UTC", "America/Los_Angeles", "Asia/Kolkata", "Pacific/Kiritimati"];
  for (const p of presets) {
    for (const z1 of zones) {
      for (const z2 of zones) {
        const before = presetRange(p.label, z1, late);
        const after = rangeInZone(p.label, before.from, before.to, z2, late);
        const u = filtersToURL(f, after.from, after.to, z2, "home", "model", DEFAULT_SORT, p.label);
        assert.deepEqual(reopen(u, z1, late), { tz: z2, ...after, preset: p.label }, `${p.label} ${z1} -> ${z2}`);
      }
    }
  }
});

test("popstate: the zone is resolved first (URL tz, else the browser's) and the dates in that zone", () => {
  // No tz in the URL, browser in UTC, Istanbul selected before: tz and dates
  // both come from UTC, and the URL written back reopens on the same dates.
  const r = zonedRangeFromURL(filtersFromURL("?range=7d"), "UTC", late);
  assert.deepEqual(r, { tz: "UTC", from: "2026-09-21", to: "2026-09-27", preset: "7d" });
  const back = filtersToURL(emptyFilters(), r.from, r.to, r.tz, "home", "model", DEFAULT_SORT, r.preset);
  assert.deepEqual(reopen(back, "Asia/Tokyo", late), r);
  // A tz in the URL wins over the browser zone.
  assert.deepEqual(zonedRangeFromURL(filtersFromURL("?range=7d&tz=Europe%2FIstanbul"), "UTC", late), {
    tz: "Europe/Istanbul",
    from: "2026-09-22",
    to: "2026-09-28",
    preset: "7d",
  });
  // Dates in the URL are kept; the zone still follows the same rule.
  assert.deepEqual(zonedRangeFromURL(filtersFromURL("?from=2026-09-01&to=2026-09-05"), "UTC", late), {
    tz: "UTC",
    from: "2026-09-01",
    to: "2026-09-05",
    preset: null,
  });
});
