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
		direct, err := st.Daily(ctx, time.UTC, harness)
		if err != nil {
			t.Fatal(err)
		}
		rolled, err := st.DailyFromRollups(ctx, harness)
		if err != nil {
			t.Fatal(err)
		}
		dj, _ := json.Marshal(direct)
		rj, _ := json.Marshal(rolled)
		if string(dj) != string(rj) {
			t.Fatalf("%s (harness %q): rollup-served daily != direct aggregation", when, harness)
		}
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

	rows, err := st.RecomputeRollups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("rebuild produced an empty rollup table")
	}
	assertRollupDaily(t, st, "after recompute --rollups")
}
