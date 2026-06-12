package parity

// `stats --daily --by <dim>` consistency (M3 Task 4, the M2 Task 5
// pattern): for every dimension, each day's breakdown rows must sum to
// exactly the Daily row's totals — tokens, reasoning and cost columns
// alike — over the combined three-harness fixture database.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/store"
)

func TestDailyByBreakdownConsistency(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "combined.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	ingestAllFixtures(t, st)

	daily, err := st.Daily(ctx, time.UTC, store.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	wantDays := map[string]store.DailyRow{}
	for _, d := range daily {
		wantDays[d.Date] = d
	}

	for _, dim := range []string{"harness", "provider", "model", "project"} {
		rows, err := st.DailyBy(ctx, time.UTC, dim, store.Filters{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatalf("--by %s returned no rows", dim)
		}
		type sums struct {
			tokens store.TokenSums
			costs  store.CostSums
		}
		got := map[string]*sums{}
		for _, r := range rows {
			s := got[r.Date]
			if s == nil {
				s = &sums{}
				got[r.Date] = s
			}
			s.tokens.Input += r.Input
			s.tokens.Output += r.Output
			s.tokens.CacheWrite += r.CacheWrite
			s.tokens.CacheRead += r.CacheRead
			s.costs.Reasoning += r.Reasoning
			s.costs.CostUSDMicro += r.CostUSDMicro
			s.costs.CostAPIEquivMicro += r.CostAPIEquivMicro
			s.costs.UnpricedEvents += r.UnpricedEvents
		}
		if len(got) != len(wantDays) {
			t.Fatalf("--by %s: %d days, want %d", dim, len(got), len(wantDays))
		}
		for date, s := range got {
			want := wantDays[date]
			if s.tokens != want.TokenSums {
				t.Errorf("--by %s %s: token sums %+v != daily totals %+v",
					dim, date, s.tokens, want.TokenSums)
			}
			if s.costs != want.CostSums {
				t.Errorf("--by %s %s: cost sums %+v != daily totals %+v",
					dim, date, s.costs, want.CostSums)
			}
		}
	}

	// --harness restriction composes with --by.
	rows, err := st.DailyBy(ctx, time.UTC, "provider", store.Filters{Harness: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Key != "openai" {
			t.Fatalf("--harness codex --by provider leaked %q", r.Key)
		}
	}

	// Unknown dimension is a loud error.
	if _, err := st.DailyBy(ctx, time.UTC, "nonsense", store.Filters{}); err == nil {
		t.Fatal("unknown --by dimension accepted")
	}
}
