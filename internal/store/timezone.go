package store

// Timezone-correct serving (M6 Task 2). UTC stays the storage, rollup
// and parity truth; presentation becomes local. A local day whose hours
// map to whole UTC hours (whole-hour offset at that date, DST resolved
// per day via tzdata) is served from the hourly rollups — byte-equal to
// event aggregation by construction; fractional-hour offsets (+05:30,
// +05:45, half-hour DST) and basis-filtered queries fall back to exact
// event aggregation. The chosen path is returned so the caller (API,
// CLI) can declare it — the M5 honesty rule, extended to time.

import (
	"context"
	"database/sql"
	"time"
)

// DailyServed returns per-local-day rows in tz on the fastest correct
// serving path, and the path taken ("rollup" | "events"):
//
//   - a basis or session filter → events (the rollup grain lacks both)
//   - tz == UTC → rollup_daily (the established UTC server; M3)
//   - a whole-hour-offset zone over the stored span → rollup_hourly
//     (DST-aware; M6 Task 1's grain)
//   - a fractional-offset or otherwise unservable zone → events
//
// The rollup_hourly path is byte-equal to the events path by
// construction: when every local-day boundary lands on a whole UTC hour,
// each hour bucket lies entirely within one local day, so grouping
// buckets by tatitok_day(hour_start) equals grouping events by
// tatitok_day(ts). The hard-stop-1 drive compares the dashboard to
// `stats --daily --timezone <zone> --json` on exactly this guarantee.
func (s *Store) DailyServed(ctx context.Context, tz *time.Location, f Filters) ([]DailyRow, string, error) {
	if !f.RollupServable() {
		rows, err := s.Daily(ctx, tz, f)
		return rows, "events", err
	}
	if tz.String() == "UTC" {
		rows, err := s.DailyFromRollups(ctx, f)
		return rows, "rollup", err
	}
	servable, err := s.hourlyServable(ctx, tz)
	if err != nil {
		return nil, "", err
	}
	if servable {
		rows, err := s.dailyFromHourly(ctx, tz, f)
		return rows, "rollup", err
	}
	rows, err := s.Daily(ctx, tz, f)
	return rows, "events", err
}

// dailyFromHourly serves per-local-day rows from rollup_hourly. Each
// hour bucket maps to the local day of its start instant via tatitok_day
// — correct only when tz is whole-hour-offset across the range (the
// caller's gate), where the whole UTC hour lies within one local day, so
// this equals Daily(ctx, tz, f) byte-equal. hour_utc is "YYYY-MM-DDTHH";
// appending ":00:00Z" forms the RFC3339 instant tatitok_day parses. The
// column list and assembly match DailyFromRollups exactly, so the three
// daily paths assemble identically.
func (s *Store) dailyFromHourly(ctx context.Context, tz *time.Location, f Filters) ([]DailyRow, error) {
	pred, fargs := f.rollupPredicate()
	rows, err := s.db.QueryContext(ctx, `SELECT
			tatitok_day(hour_utc || ':00:00Z', ?) AS day,
			harness AS h, model,
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(tokens_reasoning), SUM(cost_usd_micro),
			SUM(cost_api_equiv_micro), SUM(events_unpriced)
		FROM rollup_hourly
		WHERE events > 0`+pred+`
		GROUP BY day, h, model
		ORDER BY day, h, model`, append([]any{tz.String()}, fargs...)...)
	if err != nil {
		return nil, err
	}
	return assembleDaily(rows)
}

// hourlyServable reports whether the hourly rollups can serve local days
// in tz byte-equal to events — i.e. every local-day boundary across the
// stored events' span lands on a whole UTC hour. Evaluated over the data
// span (MIN/MAX ts) so one payload gets one honest `source`; an empty
// table is trivially servable (both paths return []).
func (s *Store) hourlyServable(ctx context.Context, tz *time.Location) (bool, error) {
	var lo, hi sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(ts), MAX(ts) FROM usage_events`).Scan(&lo, &hi); err != nil {
		return false, err
	}
	if !lo.Valid || !hi.Valid {
		return true, nil
	}
	loT, err := time.Parse(time.RFC3339Nano, lo.String)
	if err != nil {
		return false, err
	}
	hiT, err := time.Parse(time.RFC3339Nano, hi.String)
	if err != nil {
		return false, err
	}
	return wholeHourZone(tz, loT, hiT), nil
}

// wholeHourZone reports whether every local-calendar-day boundary in
// [lo, hi] lands on a whole UTC hour in tz. Only a local-day boundary can
// split a UTC-hour bucket across two local days, so this is precisely the
// hourly-serving condition (M6 Task 2). It tests the resolved boundary
// INSTANT's UTC placement — minutes and seconds zero — NOT merely the
// offset (M6 Codex F3): at a shifted or skipped midnight (e.g.
// Pacific/Apia, Asia/Colombo transitions) time.Date(…00:00…) normalizes
// to an instant that may carry a whole-hour OFFSET while sitting
// mid-hour; the offset test would wrongly approve the hourly path there.
//
// DST is handled naturally: a boundary that stays on a whole UTC hour
// across an offset switch (-05:00/-04:00, +01:00/+02:00) remains
// servable; a fractional offset (+05:30, +05:45, half-hour DST) or a
// mid-hour boundary disqualifies the whole range. The walk steps one
// local day at a time on a NOON anchor (noon is never skipped by DST, so
// stepping never drifts), reconstructing each day's 00:00 boundary fresh.
func wholeHourZone(tz *time.Location, lo, hi time.Time) bool {
	loLocal, hiLocal := lo.In(tz), hi.In(tz)
	noon := time.Date(loLocal.Year(), loLocal.Month(), loLocal.Day(), 12, 0, 0, 0, tz)
	// One day past hi's local day, so hi's day's closing boundary is checked.
	end := time.Date(hiLocal.Year(), hiLocal.Month(), hiLocal.Day(), 12, 0, 0, 0, tz).AddDate(0, 0, 1)
	for !noon.After(end) {
		y, m, d := noon.Date()
		u := time.Date(y, m, d, 0, 0, 0, 0, tz).UTC()
		if u.Minute() != 0 || u.Second() != 0 || u.Nanosecond() != 0 {
			return false
		}
		noon = noon.AddDate(0, 0, 1)
	}
	return true
}
