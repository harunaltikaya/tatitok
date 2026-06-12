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

// RollupCounts reports the table's size for plans and diagnostics.
func (s *Store) RollupCounts(ctx context.Context) (rollupRows, events int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM rollup_daily),
			(SELECT COUNT(*) FROM usage_events)`).Scan(&rollupRows, &events)
	return rollupRows, events, err
}

// RecomputeRollups rebuilds rollup_daily from usage_events in one
// transaction — the explicit full-recompute path (AS-4). The triggers
// keep the table correct incrementally; the rebuild exists to normalize
// version columns and to recover from anything unforeseen.
func (s *Store) RecomputeRollups(ctx context.Context) (rows int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rollup_daily`); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, rollupRebuildSQL); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rollup_daily`).Scan(&rows); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	return rows, tx.Commit()
}
