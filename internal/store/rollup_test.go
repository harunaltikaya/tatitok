package store

// Rollup consistency property (M3 Task 3): rollup-table query results
// must equal direct event aggregation, byte-equal through the stats
// path, under randomized inserts, replacements and recomputes.
// Synthetic events are fine here: this tests SQL trigger behavior, not
// adapter parsing.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/modelmap"
)

// assertRollupConsistent checks both equality lanes: the assembled
// stats path (byte-equal JSON) and the raw column sums including the
// cost columns the stats path does not carry yet.
func assertRollupConsistent(t *testing.T, s *Store, when string) {
	t.Helper()
	ctx := context.Background()
	direct, err := s.Daily(ctx, time.UTC, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := s.DailyFromRollups(ctx, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	dj, _ := json.Marshal(direct)
	rj, _ := json.Marshal(rolled)
	if string(dj) != string(rj) {
		t.Fatalf("%s: rollup-served daily != direct aggregation\nrollup: %s\ndirect: %s",
			when, rj, dj)
	}

	var d, r struct {
		events, in, out, cw, cr, reasoning, cost, equiv, unpriced int64
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(tokens_input),0), COALESCE(SUM(tokens_output),0),
			COALESCE(SUM(tokens_cache_write),0), COALESCE(SUM(tokens_cache_read),0),
			COALESCE(SUM(COALESCE(tokens_reasoning,0)),0),
			COALESCE(SUM(COALESCE(cost_usd_micro,0)),0),
			COALESCE(SUM(COALESCE(cost_api_equiv_micro,0)),0),
			COALESCE(SUM(cost_usd_micro IS NULL),0)
		FROM usage_events`).Scan(&d.events, &d.in, &d.out, &d.cw, &d.cr,
		&d.reasoning, &d.cost, &d.equiv, &d.unpriced); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(events),0),
			COALESCE(SUM(tokens_input),0), COALESCE(SUM(tokens_output),0),
			COALESCE(SUM(tokens_cache_write),0), COALESCE(SUM(tokens_cache_read),0),
			COALESCE(SUM(tokens_reasoning),0),
			COALESCE(SUM(cost_usd_micro),0), COALESCE(SUM(cost_api_equiv_micro),0),
			COALESCE(SUM(events_unpriced),0)
		FROM rollup_daily`).Scan(&r.events, &r.in, &r.out, &r.cw, &r.cr,
		&r.reasoning, &r.cost, &r.equiv, &r.unpriced); err != nil {
		t.Fatal(err)
	}
	if d != r {
		t.Fatalf("%s: rollup sums diverged\nrollup: %+v\ndirect: %+v", when, r, d)
	}

	// Hour grain (M6 Task 1): the grand totals over rollup_hourly equal
	// the event grand totals too (same lane as the daily check above)…
	var hr struct {
		events, in, out, cw, cr, reasoning, cost, equiv, unpriced int64
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(events),0),
			COALESCE(SUM(tokens_input),0), COALESCE(SUM(tokens_output),0),
			COALESCE(SUM(tokens_cache_write),0), COALESCE(SUM(tokens_cache_read),0),
			COALESCE(SUM(tokens_reasoning),0),
			COALESCE(SUM(cost_usd_micro),0), COALESCE(SUM(cost_api_equiv_micro),0),
			COALESCE(SUM(events_unpriced),0)
		FROM rollup_hourly`).Scan(&hr.events, &hr.in, &hr.out, &hr.cw, &hr.cr,
		&hr.reasoning, &hr.cost, &hr.equiv, &hr.unpriced); err != nil {
		t.Fatal(err)
	}
	if d != hr {
		t.Fatalf("%s: hourly rollup sums diverged from events\nhourly: %+v\ndirect: %+v", when, hr, d)
	}
	// …and the conservation law holds: every daily row equals the sum of
	// its hourly rows, byte-equal on the additive measures.
	viol, err := s.VerifyRollupConservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(viol) != 0 {
		t.Fatalf("%s: %d rollup conservation violation(s): %+v", when, len(viol), viol)
	}
}

// Randomized subsets and orders of ingestion, with payload-changing
// replacements mixed in — the milestone's consistency property.
func TestRollupConsistencyRandomized(t *testing.T) {
	harnesses := []string{"claude-code", "codex", "opencode"}
	models := []string{"claude-fable-5", "gpt-5.5", "deepseek-v4-flash-free", "qwen3.6-27b", "unknown-x"}
	projects := []string{"", "/p/alpha", "/p/beta"}

	for seed := int64(1); seed <= 3; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := openTemp(t)
			ctx := context.Background()
			base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

			var pool []core.Event
			for i := 0; i < 120; i++ {
				m := models[rng.Intn(len(models))]
				e := eventH(harnesses[rng.Intn(len(harnesses))],
					fmt.Sprintf("m%d", i), fmt.Sprintf("r%d", i),
					m, fmt.Sprintf("s%d", rng.Intn(5)),
					base.Add(time.Duration(rng.Intn(96))*time.Hour),
					TokenSums{Input: int64(rng.Intn(1000)), Output: int64(rng.Intn(500)),
						CacheWrite: int64(rng.Intn(300)), CacheRead: int64(rng.Intn(2000))})
				e.ModelFamily = modelmap.Family(m)
				e.Project = projects[rng.Intn(len(projects))]
				if rng.Intn(3) > 0 { // some events stay unpriced (NULL cost)
					cost := int64(rng.Intn(50_000))
					e.CostUSDMicro = &cost
					e.CostBasis, e.PriceSnapshot = "api_price", "snap-test"
				}
				if rng.Intn(4) == 0 {
					reasoning := int64(rng.Intn(200))
					e.TokensReasoning = &reasoning
				}
				pool = append(pool, e)
			}
			// Random subset, random order.
			rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
			subset := pool[:60+rng.Intn(60)]
			for len(subset) > 0 {
				n := 1 + rng.Intn(20)
				if n > len(subset) {
					n = len(subset)
				}
				if _, err := s.InsertBatch(ctx, subset[:n], testSource(n)); err != nil {
					t.Fatal(err)
				}
				subset = subset[n:]
			}
			assertRollupConsistent(t, s, "after randomized ingest")

			// Replacements: re-insert some already-stored events with
			// changed payloads (mutable-store semantics).
			rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
			var mutated []core.Event
			for _, e := range pool[:20] {
				e.TokensOutput += int64(1 + rng.Intn(100))
				if rng.Intn(2) == 0 {
					e.TS = e.TS.Add(time.Duration(rng.Intn(48)) * time.Hour) // may move days
				}
				mutated = append(mutated, e)
			}
			if _, err := s.InsertBatch(ctx, mutated, testSource(len(mutated))); err != nil {
				t.Fatal(err)
			}
			assertRollupConsistent(t, s, "after replacements")

			// Explicit recomputes move rows between buckets; the triggers
			// must follow.
			if _, _, _, err := s.RecomputeModelMap(ctx, modelmap.Version(), nil); err != nil {
				t.Fatal(err)
			}
			assertRollupConsistent(t, s, "after recompute --model-map")
			if _, err := s.RecomputePricing(ctx, nil); err != nil {
				t.Fatal(err)
			}
			assertRollupConsistent(t, s, "after recompute --pricing")

			// The explicit rebuild produces the same tables (both grains).
			if _, _, err := s.RecomputeRollups(ctx); err != nil {
				t.Fatal(err)
			}
			assertRollupConsistent(t, s, "after recompute --rollups")
		})
	}
}

// Rollup version columns are ADVISORY with MAX semantics (M3.1 finding 4,
// owner ruling): the incremental triggers keep the MAX over contributing
// events — agreeing with the rebuild's MAX() — even when a lower-version
// event arrives later (out-of-order ingest of an older file).
func TestRollupVersionColumnsMaxSemantics(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	newer := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 1})
	newer.PriceSnapshot = "snap-b"
	src := testSource(1)
	src.MapVersion = 2
	if _, err := s.InsertBatch(ctx, []core.Event{newer}, src); err != nil {
		t.Fatal(err)
	}
	// Same bucket, LOWER versions, ingested later.
	older := event("m2", "r2", "model-a", "s1", ts.Add(time.Minute), TokenSums{Input: 1})
	older.PriceSnapshot = "snap-a"
	src.MapVersion = 1
	if _, err := s.InsertBatch(ctx, []core.Event{older}, src); err != nil {
		t.Fatal(err)
	}

	check := func(when string) {
		t.Helper()
		var mapVersion int64
		var snapshot string
		if err := s.db.QueryRowContext(ctx, `SELECT map_version, snapshot_version
			FROM rollup_daily`).Scan(&mapVersion, &snapshot); err != nil {
			t.Fatal(err)
		}
		if mapVersion != 2 || snapshot != "snap-b" {
			t.Fatalf("%s: rollup versions %d/%s, want MAX semantics 2/snap-b",
				when, mapVersion, snapshot)
		}
	}
	check("incremental (last-written would say 1/snap-a)")

	// The rebuild agrees — incremental and rebuild share MAX semantics.
	if _, _, err := s.RecomputeRollups(ctx); err != nil {
		t.Fatal(err)
	}
	check("after rebuild")
}

// VerifyRollupConservation must actually DETECT a divergence — a verify
// that can only ever return empty is a dead check. Corrupt one hourly
// bucket directly (bypassing the triggers) and confirm it is reported
// from both grains, then prove recompute --rollups heals it.
func TestVerifyRollupConservationDetectsSkew(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	batch := []core.Event{
		event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10, Output: 20}),
		event("m2", "r2", "model-a", "s1", ts.Add(2*time.Hour), TokenSums{Input: 5, Output: 7}),
	}
	if _, err := s.InsertBatch(ctx, batch, testSource(len(batch))); err != nil {
		t.Fatal(err)
	}
	if viol, err := s.VerifyRollupConservation(ctx); err != nil || len(viol) != 0 {
		t.Fatalf("clean DB: %d violation(s) (err %v), want 0", len(viol), err)
	}

	// Inflate one hourly bucket directly — no trigger fires on rollup_hourly.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE rollup_hourly SET tokens_input = tokens_input + 1000 WHERE hour_utc = ?`,
		ts.Format("2006-01-02T15")); err != nil {
		t.Fatal(err)
	}
	viol, err := s.VerifyRollupConservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(viol) == 0 {
		t.Fatal("conservation verify did not detect the injected skew")
	}

	// The rebuild restores both grains from the events — heals it.
	if _, _, err := s.RecomputeRollups(ctx); err != nil {
		t.Fatal(err)
	}
	if viol, err := s.VerifyRollupConservation(ctx); err != nil || len(viol) != 0 {
		t.Fatalf("after recompute --rollups: %d violation(s) (err %v), want 0", len(viol), err)
	}
}

// F2: a grain↔grain check alone would pass when BOTH grains share an
// identical error while the events stay correct. The verify must anchor
// to the events (ground truth): inject the same divergence into both
// grains so daily still sums to hourly, and confirm the grain↔events
// checks catch it while the sibling check stays clean.
func TestVerifyRollupConservationAnchorsToEvents(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	batch := []core.Event{
		event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10, Output: 20}),
		event("m2", "r2", "model-a", "s1", ts.Add(2*time.Hour), TokenSums{Input: 5, Output: 7}),
	}
	if _, err := s.InsertBatch(ctx, batch, testSource(len(batch))); err != nil {
		t.Fatal(err)
	}

	// Inflate the daily row AND one of its hourly rows by the same amount:
	// daily (1015) still equals the hourly sum (1010+5), so daily↔hourly
	// is clean — but both now disagree with the events (15).
	if _, err := s.db.ExecContext(ctx,
		`UPDATE rollup_daily SET tokens_input = tokens_input + 1000 WHERE day_utc = '2026-06-10'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE rollup_hourly SET tokens_input = tokens_input + 1000 WHERE hour_utc = '2026-06-10T12'`); err != nil {
		t.Fatal(err)
	}

	viol, err := s.VerifyRollupConservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]int{}
	for _, v := range viol {
		checks[v.Check]++
	}
	if checks["daily_vs_events"] == 0 {
		t.Error("grain↔events did not catch the daily divergence from the events (F2)")
	}
	if checks["hourly_vs_events"] == 0 {
		t.Error("grain↔events did not catch the hourly divergence from the events (F2)")
	}
	if checks["daily_vs_hourly"] != 0 {
		t.Errorf("daily↔hourly should stay clean here (both grains share the error), got %d", checks["daily_vs_hourly"])
	}
}

// Migration 11 backfills rollup_hourly for events that predate the hour
// grain — the same backfill discipline migration 9 applied to the day
// grain — and the backfilled grains conserve.
func TestMigration11BackfillsHourly(t *testing.T) {
	path := buildV4DB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var rows int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM rollup_hourly`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("migration 11 left rollup_hourly empty over a populated event table")
	}
	viol, err := s.VerifyRollupConservation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(viol) != 0 {
		t.Fatalf("after migration 11 backfill: %d conservation violation(s): %+v", len(viol), viol)
	}
}

// Migration 9 backfills rollups for events that predate the triggers.
func TestMigration9BackfillsRollups(t *testing.T) {
	path := buildV4DB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	assertRollupConsistent(t, s, "after migration backfill")
	var rows int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM rollup_daily`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("migration 9 left rollup_daily empty over a populated event table")
	}
}
