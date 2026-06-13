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

// ConservationRow is one (UTC day, dimension combination) where
// rollup_daily and the sum of its rollup_hourly rows disagree on an
// additive measure, or where one grain carries a row the other lacks.
// The conservation law (M6 Task 1) is defined over the additive measures
// ONLY; the advisory version columns (map_version, snapshot_version —
// MAX semantics, M3.1 ruling) are excluded, exact only after
// recompute --rollups. Side names the grain the row was found in.
type ConservationRow struct {
	Side        string `json:"side"` // "daily" | "hourly"
	Day         string `json:"day"`
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

// conservationCols are the conservation tuple — day, the seven
// dimensions, and the nine additive measures (version columns excluded).
const conservationCols = `day_utc, machine, harness, provider, model,
	model_family, project, events, tokens_input, tokens_output,
	tokens_cache_write, tokens_cache_read, tokens_reasoning,
	cost_usd_micro, cost_api_equiv_micro, events_unpriced`

// VerifyRollupConservation checks the conservation law of the grain:
// every rollup_daily row equals the sum of its rollup_hourly rows
// (grouped back to the UTC day via substr(hour_utc,1,10)) byte-equal on
// the additive measures, for every dimension combination. It returns
// every row present in one grain but not byte-identically in the other —
// an empty result means the two grains agree exactly. Runnable against
// the live DB (`doctor --rollups`); the hard-stop-0 conservation
// ceremony is "this returns nothing across full history".
func (s *Store) VerifyRollupConservation(ctx context.Context) ([]ConservationRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH hourly_as_daily AS (
			SELECT substr(hour_utc, 1, 10) AS day_utc, machine, harness,
				provider, model, model_family, project,
				SUM(events) AS events,
				SUM(tokens_input) AS tokens_input,
				SUM(tokens_output) AS tokens_output,
				SUM(tokens_cache_write) AS tokens_cache_write,
				SUM(tokens_cache_read) AS tokens_cache_read,
				SUM(tokens_reasoning) AS tokens_reasoning,
				SUM(cost_usd_micro) AS cost_usd_micro,
				SUM(cost_api_equiv_micro) AS cost_api_equiv_micro,
				SUM(events_unpriced) AS events_unpriced
			FROM rollup_hourly GROUP BY 1, 2, 3, 4, 5, 6, 7
		)
		SELECT 'daily' AS side, t.* FROM (
			SELECT `+conservationCols+` FROM rollup_daily
			EXCEPT
			SELECT `+conservationCols+` FROM hourly_as_daily
		) t
		UNION ALL
		SELECT 'hourly' AS side, t.* FROM (
			SELECT `+conservationCols+` FROM hourly_as_daily
			EXCEPT
			SELECT `+conservationCols+` FROM rollup_daily
		) t
		ORDER BY 2, 3, 4, 5, 6, 7, 8`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConservationRow
	for rows.Next() {
		var r ConservationRow
		if err := rows.Scan(&r.Side, &r.Day, &r.Machine, &r.Harness,
			&r.Provider, &r.Model, &r.ModelFamily, &r.Project, &r.Events,
			&r.Input, &r.Output, &r.CacheWrite, &r.CacheRead, &r.Reasoning,
			&r.CostMicro, &r.EquivMicro, &r.Unpriced); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
