// Live-invalidation mapping (M6 Codex F1). The SSE stream reports
// touched days in UTC (the store buckets writes by UTC day), but the
// dashboard's visible range is in the viewer's timezone. Comparing them
// directly is wrong: a 22:30Z event is the NEXT local day in Tokyo, so a
// UTC touched-day must invalidate whichever LOCAL day(s) it overlaps.
// These pure helpers do that mapping (pinned by Node's test runner).

// Explicit .ts extension: this is a runtime import (not type-only), and
// invalidate.ts is exercised by Node's test runner, which resolves real
// module specifiers — Vite and tsc (allowImportingTsExtensions) accept
// it too.
import { dayInTZ } from "./api.ts";

// localDaysForUTCDay maps a UTC day (YYYY-MM-DD) to the local calendar
// day(s) it overlaps in tz — the local days of its first and last
// instants. A 24h UTC window spans at most two consecutive local days
// (offsets are within ±14h), so this is 1 or 2 days.
export function localDaysForUTCDay(utcDay: string, tz: string): string[] {
  const start = dayInTZ(new Date(utcDay + "T00:00:00Z"), tz);
  const end = dayInTZ(new Date(utcDay + "T23:59:59.999Z"), tz);
  return start === end ? [start] : [start, end];
}

// touchedInRange reports whether any UTC touched-day overlaps the visible
// [from, to] local-day range once mapped into tz. Empty touched (the
// stream lost events / reconnected) returns true — refetch to be safe,
// matching the prior behavior.
export function touchedInRange(
  touchedUTCDays: string[],
  tz: string,
  from: string,
  to: string,
): boolean {
  if (touchedUTCDays.length === 0) return true;
  for (const d of touchedUTCDays) {
    for (const local of localDaysForUTCDay(d, tz)) {
      if (local >= from && local <= to) return true;
    }
  }
  return false;
}
