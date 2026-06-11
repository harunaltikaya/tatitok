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
	direct, err := s.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := s.DailyFromRollups(ctx, "")
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
			if _, _, err := s.RecomputeModelMap(ctx, modelmap.Version()); err != nil {
				t.Fatal(err)
			}
			assertRollupConsistent(t, s, "after recompute --model-map")
			if _, err := s.RecomputePricing(ctx, nil); err != nil {
				t.Fatal(err)
			}
			assertRollupConsistent(t, s, "after recompute --pricing")

			// The explicit rebuild produces the same table.
			if _, err := s.RecomputeRollups(ctx); err != nil {
				t.Fatal(err)
			}
			assertRollupConsistent(t, s, "after recompute --rollups")
		})
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
