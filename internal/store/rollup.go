package store

// Rollup queries and the explicit rebuild (M3 Task 3, PRD §9.2). The
// rollup_daily table itself is maintained by the migration-9 triggers
// inside every write transaction; see store.go.

import (
	"context"
	"fmt"
)

// DailyFromRollups is the rollup-served Daily: identical output to
// Daily(ctx, UTC, f) by construction — same grouping, same assembly —
// but reading the pre-aggregated table. UTC ONLY: rollup days are UTC
// buckets and cannot serve other timezones exactly (the recorded M3
// decision; hourly grain is deferred). A basis filter is a loud error:
// the rollup grain lacks basis — callers check f.RollupServable() and
// fall back to the exact event path (M5 Task 3).
func (s *Store) DailyFromRollups(ctx context.Context, f Filters) ([]DailyRow, error) {
	if !f.RollupServable() {
		return nil, fmt.Errorf("rollups cannot serve a basis filter — aggregate events (Daily) instead")
	}
	pred, args := f.rollupPredicate()
	rows, err := s.db.QueryContext(ctx, `SELECT day_utc AS day,
			harness AS h, model,
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(tokens_reasoning), SUM(cost_usd_micro),
			SUM(cost_api_equiv_micro), SUM(events_unpriced)
		FROM rollup_daily
		WHERE events > 0`+pred+`
		GROUP BY day, h, model
		ORDER BY day, h, model`, args...)
	if err != nil {
		return nil, err
	}
	return assembleDaily(rows)
}

// RollupCounts reports both rollup grains' row counts and the event
// count — diagnostics and the recompute-rollups plan listing.
func (s *Store) RollupCounts(ctx context.Context) (dailyRows, hourlyRows, events int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM rollup_daily),
			(SELECT COUNT(*) FROM rollup_hourly),
			(SELECT COUNT(*) FROM usage_events)`).Scan(&dailyRows, &hourlyRows, &events)
	return dailyRows, hourlyRows, events, err
}

// RecomputeRollups rebuilds BOTH rollup grains (day and hour) from
// usage_events in one transaction — the explicit full-recompute path
// (AS-4). The triggers keep both tables correct incrementally; the
// rebuild exists to normalize the advisory version columns and to
// recover from anything unforeseen. After it commits, hourly sums to
// daily byte-equal AND the version columns agree (both are MAX() over
// the same events) — the state the hard-stop-0 ceremony verifies.
func (s *Store) RecomputeRollups(ctx context.Context) (dailyRows, hourlyRows int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	for _, stmt := range []string{
		`DELETE FROM rollup_daily`, rollupRebuildSQL,
		`DELETE FROM rollup_hourly`, rollupHourlyRebuildSQL,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM rollup_daily),
			(SELECT COUNT(*) FROM rollup_hourly)`).Scan(&dailyRows, &hourlyRows); err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	return dailyRows, hourlyRows, tx.Commit()
}

// ConservationRow is one (bucket, dimension combination) where two
// sources disagree on an additive measure, or where one carries a row
// the other lacks. Check names the comparison; Side names the source the
// unmatched row was found in. Bucket is a UTC day for the day-grain
// checks and a UTC hour ("YYYY-MM-DDTHH") for the hourly↔events check.
// The conservation law (M6 Task 1) is defined over the additive measures
// ONLY; the advisory version columns (map_version, snapshot_version —
// MAX semantics, M3.1 ruling) are excluded, exact only after
// recompute --rollups.
type ConservationRow struct {
	Check       string `json:"check"` // daily_vs_events | hourly_vs_events | daily_vs_hourly
	Side        string `json:"side"`
	Bucket      string `json:"bucket"`
	Machine     string `json:"machine"`
	Harness     string `json:"harness"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	ModelFamily string `json:"model_family"`
	Project     string `json:"project"`
	Events      int64  `json:"events"`
	Input       int64  `json:"tokens_input"`
	Output      int64  `json:"tokens_output"`
	CacheWrite  int64  `json:"tokens_cache_write"`
	CacheRead   int64  `json:"tokens_cache_read"`
	Reasoning   int64  `json:"tokens_reasoning"`
	CostMicro   int64  `json:"cost_usd_micro"`
	EquivMicro  int64  `json:"cost_api_equiv_micro"`
	Unpriced    int64  `json:"events_unpriced"`
}

// conservationMeasures are the seven dimensions and nine additive
// measures of the conservation tuple (the leading bucket column is
// supplied per source). Version columns are excluded — advisory (M3.1).
const conservationMeasures = `machine, harness, provider, model, model_family,
	project, events, tokens_input, tokens_output, tokens_cache_write,
	tokens_cache_read, tokens_reasoning, cost_usd_micro,
	cost_api_equiv_micro, events_unpriced`

// eventsAgg projects usage_events into the conservation tuple at the
// given bucket width (10 = UTC day, 13 = UTC hour) — the SAME mapping
// rollupRebuildSQL uses, so it is the GROUND TRUTH a correct rollup must
// equal exactly.
func eventsAgg(bucketLen int) string {
	return fmt.Sprintf(`SELECT substr(ts, 1, %d), machine, COALESCE(harness, ''),
			provider, model, model_family, COALESCE(project, ''),
			COUNT(*), SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(COALESCE(tokens_reasoning, 0)), SUM(COALESCE(cost_usd_micro, 0)),
			SUM(COALESCE(cost_api_equiv_micro, 0)), SUM(cost_usd_micro IS NULL)
		FROM usage_events GROUP BY 1, 2, 3, 4, 5, 6, 7`, bucketLen)
}

// VerifyRollupConservation checks the conservation law of the rollup
// grains against the EVENTS — the ground truth, not merely against each
// other (M6 Codex F2: two grains sharing an identical error would pass a
// purely grain↔grain check). Three comparisons run, each a symmetric
// EXCEPT over the additive-measure tuple for every dimension
// combination:
//
//   - daily_vs_events:  rollup_daily  == events bucketed by UTC day
//   - hourly_vs_events: rollup_hourly == events bucketed by UTC hour
//   - daily_vs_hourly:  rollup_daily  == the hourly rows summed to day
//
// The grain↔events checks establish correctness; the grain↔grain check
// is kept (Codex ruling). It returns every row present in one source but
// not byte-identically in the other — an empty result means "clean" =
// "matches the events". Runnable against the live DB (`doctor
// --rollups`); the hard-stop-0 ceremony is "this returns nothing across
// full history".
func (s *Store) VerifyRollupConservation(ctx context.Context) ([]ConservationRow, error) {
	const hourlyAsDaily = `SELECT substr(hour_utc, 1, 10), machine, harness,
			provider, model, model_family, project,
			SUM(events), SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(tokens_reasoning), SUM(cost_usd_micro),
			SUM(cost_api_equiv_micro), SUM(events_unpriced)
		FROM rollup_hourly GROUP BY 1, 2, 3, 4, 5, 6, 7`
	checks := []struct{ name, left, leftSide, right, rightSide string }{
		{"daily_vs_events", `SELECT day_utc, ` + conservationMeasures + ` FROM rollup_daily`, "daily", eventsAgg(10), "events"},
		{"hourly_vs_events", `SELECT hour_utc, ` + conservationMeasures + ` FROM rollup_hourly`, "hourly", eventsAgg(13), "events"},
		{"daily_vs_hourly", `SELECT day_utc, ` + conservationMeasures + ` FROM rollup_daily`, "daily", hourlyAsDaily, "hourly"},
	}
	var out []ConservationRow
	for _, c := range checks {
		got, err := s.conservationDiff(ctx, c.name, c.left, c.leftSide, c.right, c.rightSide)
		if err != nil {
			return nil, fmt.Errorf("conservation check %s: %w", c.name, err)
		}
		out = append(out, got...)
	}
	return out, nil
}

// conservationDiff runs a symmetric EXCEPT between two conservation-tuple
// SELECTs and returns the rows present in one side but not the other,
// tagged with the check name and the side they came from.
func (s *Store) conservationDiff(ctx context.Context, check, left, leftSide, right, rightSide string) ([]ConservationRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ?1 AS s, t.* FROM (`+left+` EXCEPT `+right+`) t
		 UNION ALL
		 SELECT ?2 AS s, t.* FROM (`+right+` EXCEPT `+left+`) t
		 ORDER BY 2, 3, 4, 5, 6, 7, 8`, leftSide, rightSide)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConservationRow
	for rows.Next() {
		r := ConservationRow{Check: check}
		if err := rows.Scan(&r.Side, &r.Bucket, &r.Machine, &r.Harness,
			&r.Provider, &r.Model, &r.ModelFamily, &r.Project, &r.Events,
			&r.Input, &r.Output, &r.CacheWrite, &r.CacheRead, &r.Reasoning,
			&r.CostMicro, &r.EquivMicro, &r.Unpriced); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
