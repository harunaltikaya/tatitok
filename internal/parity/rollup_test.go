package parity

// Rollup consistency over the real fixture sets (M3 Task 3): the
// rollup-served stats path must be byte-equal to direct aggregation on
// the combined three-harness database, and stay equal through the
// explicit recomputes.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/modelmap"
	"github.com/harunaltikaya/tatitok/internal/store"
)

func assertRollupDaily(t *testing.T, st *store.Store, when string) {
	t.Helper()
	ctx := context.Background()
	for _, harness := range []string{"", "claude-code", "codex", "opencode"} {
		var f store.Filters
		if harness != "" {
			f.Harness = []string{harness}
		}
		direct, err := st.Daily(ctx, time.UTC, f)
		if err != nil {
			t.Fatal(err)
		}
		rolled, err := st.DailyFromRollups(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		dj, _ := json.Marshal(direct)
		rj, _ := json.Marshal(rolled)
		if string(dj) != string(rj) {
			t.Fatalf("%s (harness %q): rollup-served daily != direct aggregation", when, harness)
		}
	}
	// The conservation law of the grain (M6 Task 1), over the real
	// three-harness fixture corpus: every daily rollup row equals the
	// sum of its hourly rows, byte-equal on the additive measures.
	viol, err := st.VerifyRollupConservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(viol) != 0 {
		t.Fatalf("%s: %d rollup conservation violation(s): %+v", when, len(viol), viol)
	}
}

func TestRollupConsistencyFixtures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "combined.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	ingestAllFixtures(t, st)
	assertRollupDaily(t, st, "after combined ingest")

	// Idempotent re-ingest must not double rollups.
	ingestAllFixtures(t, st)
	assertRollupDaily(t, st, "after combined re-ingest")

	if _, _, _, err := st.RecomputeModelMap(ctx, modelmap.Version(), nil); err != nil {
		t.Fatal(err)
	}
	assertRollupDaily(t, st, "after recompute --model-map")

	if _, err := st.RecomputePricing(ctx, nil); err != nil {
		t.Fatal(err)
	}
	assertRollupDaily(t, st, "after recompute --pricing")

	dailyRows, hourlyRows, err := st.RecomputeRollups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dailyRows == 0 || hourlyRows == 0 {
		t.Fatalf("rebuild produced an empty rollup table (daily=%d, hourly=%d)", dailyRows, hourlyRows)
	}
	assertRollupDaily(t, st, "after recompute --rollups")
}
