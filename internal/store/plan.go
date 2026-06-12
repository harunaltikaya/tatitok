package store

// Plan-window source rows (M5 Task 2). The meter reads STAMPED basis,
// not live matcher evaluation — one source of truth: what Apply (and
// recompute) wrote. Config drift (a renamed plan, a matcher change
// before the recompute) is therefore visible, never papered over.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PlanEventRow is one plan_included event's contribution to window
// math: timestamp, covering-plan name (from the stored provenance),
// raw token columns and the stored API-equivalent.
type PlanEventRow struct {
	TS                                   time.Time
	Plan                                 string
	Input, Output, CacheWrite, CacheRead int64
	EquivMicro                           int64
	Unpriced                             bool
}

// PlanIncludedEvents returns every plan_included event ascending by
// timestamp. Full history by design: window anchoring is
// history-dependent — a query horizon could mis-anchor the current
// window across an idle gap.
func (s *Store) PlanIncludedEvents(ctx context.Context) ([]PlanEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, COALESCE(price_rates, ''),
			tokens_input, tokens_output, tokens_cache_write, tokens_cache_read,
			cost_api_equiv_micro
		FROM usage_events WHERE cost_basis = 'plan_included' ORDER BY ts`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PlanEventRow
	for rows.Next() {
		var ts, rates string
		var r PlanEventRow
		var equiv sql.NullInt64
		if err := rows.Scan(&ts, &rates, &r.Input, &r.Output, &r.CacheWrite,
			&r.CacheRead, &equiv); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil, fmt.Errorf("plan_included event: unparseable ts %q: %w", ts, err)
		}
		r.TS = t.UTC()
		var detail struct {
			Plan string `json:"plan"`
		}
		// A decode failure only loses the plan attribution, never the row.
		_ = json.Unmarshal([]byte(rates), &detail)
		r.Plan = detail.Plan
		if equiv.Valid {
			r.EquivMicro = equiv.Int64
		} else {
			r.Unpriced = true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
