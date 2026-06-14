// tz-offset label test (M8 chunk 1I), Node's built-in runner. The live label
// next to the timezone selector is a clarity annotation only (never used in any
// computation — the server does the authoritative day-bucketing); this pins the
// pure formatter: an IANA zone → its "UTC±H[:MM]" offset. A fixed date is passed
// so the DST-observing zone is deterministic; the others are DST-free year-round.

import { test } from "node:test";
import assert from "node:assert/strict";
import { tzOffsetLabel } from "./api.ts";

test("tzOffsetLabel renders a zone's UTC offset", () => {
  const at = new Date("2026-06-14T12:00:00Z");
  assert.equal(tzOffsetLabel("Europe/Istanbul", at), "UTC+3"); // permanent +3 since 2016
  assert.equal(tzOffsetLabel("UTC", at), "UTC+0");
  assert.equal(tzOffsetLabel("Asia/Kolkata", at), "UTC+5:30"); // half-hour zone, no DST
  assert.equal(tzOffsetLabel("America/New_York", at), "UTC-4"); // EDT in June (negative offset)
});
