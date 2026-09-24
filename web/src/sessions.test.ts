// sessionLines: the sessions panel's row shaping (label, folder name,
// start time in the zone, four-field total); chipValue: the filter chip
// text, session included; the session filter's URL round-trip.

import { test } from "node:test";
import assert from "node:assert/strict";
import { sessionLines } from "./aggregate.ts";
import type { SessionRow } from "./api.ts";
import { DEFAULT_SORT, chipValue, emptyFilters, filterQuery, filtersFromURL, filtersToURL } from "./filters.ts";

function row(session: string, harness: string, project: string, firstTs: string, equiv: number): SessionRow {
  return {
    session, harness, project, firstTs, lastTs: firstTs, events: 3,
    inputTokens: 1000, outputTokens: 200, cacheCreationTokens: 30, cacheReadTokens: 4,
    costAPIEquivMicro: equiv,
  };
}

// The leak checker's one synthetic UUID (scripts/check_fixture_leaks.py).
const ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";

test("sessionLines: label, folder name, zone time, totals, served order", () => {
  const rows = [
    row(ID, "claude-code", "/home/user/Projects/tatitok/", "2026-09-23T22:30:00Z", 2_500_000),
    row("", "", "", "2026-09-24T09:05:59Z", 0),
  ];
  const [a, b] = sessionLines(rows, "Europe/Istanbul");
  assert.deepEqual(a, {
    raw: ID,
    label: "aaaaaaaa",
    harness: "claude-code",
    project: "tatitok",
    projectRaw: "/home/user/Projects/tatitok/",
    start: "09-24 01:30", // 22:30Z is the next day at +03:00
    events: 3,
    tokens: 1234, // input + output + cache-write + cache-read
    equivMicro: 2_500_000,
  });
  assert.equal(b.raw, "");
  assert.equal(b.label, "(none)");
  assert.equal(b.harness, "(none)");
  assert.equal(b.project, "(none)");
  assert.equal(b.start, "09-24 12:05"); // minutes truncate, not round

  // The zone decides the start: UTC, and a fractional offset.
  assert.equal(sessionLines(rows, "UTC")[0].start, "09-23 22:30");
  assert.equal(sessionLines(rows, "Asia/Kolkata")[0].start, "09-24 04:00");
  // Midnight reads 00, not 24.
  assert.equal(sessionLines([row("x", "pi", "", "2026-09-23T21:00:00Z", 0)], "Europe/Istanbul")[0].start, "09-24 00:00");
  assert.deepEqual(sessionLines([], "UTC"), []);
});

test("chipValue: session by its first 8 characters, other dims as before", () => {
  assert.equal(chipValue("session", ID), "aaaaaaaa");
  assert.equal(chipValue("session", "ses_short"), "ses_shor");
  assert.equal(chipValue("session", ""), "(none)");
  assert.equal(chipValue("project", "/home/user/Projects/tatitok"), "tatitok");
  assert.equal(chipValue("harness", "codex"), "codex");
  assert.equal(chipValue("harness", ""), "(none)");
});

test("session filter rides the URL and every request's query", () => {
  const f = { ...emptyFilters(), harness: ["codex"], session: [ID] };
  assert.equal(filterQuery(f), `&harness=codex&session=${ID}`);
  const url = filtersToURL(f, "2026-09-18", "2026-09-24", "Europe/Istanbul", "detail", "model", DEFAULT_SORT);
  assert.ok(url.endsWith(`&session=${ID}`));
  assert.deepEqual(filtersFromURL(url).filters, f);
});
