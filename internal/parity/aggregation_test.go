package parity

// M2 Task 2: the SQL-side aggregation replaced the legacy full-scan-in-Go
// path after TestSQLAggregationByteIdenticalToLegacy (see git history,
// commit "feat: stats aggregation moves into SQL") asserted byte-equal
// JSON over the fixture DB in UTC, Europe/Istanbul, America/Los_Angeles
// and Asia/Kathmandu. The ccusage parity gate and the store tests now pin
// the SQL path's behavior directly; this file keeps the stats budget.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
)

// The < 5 s full-history stats budget (with headroom): the fixture DB is
// small, so this is a smoke ceiling, not a benchmark — the SQL path must
// answer well under the budget.
func TestSQLAggregationBudget(t *testing.T) {
	root, err := filepath.Abs(filepath.Join(fixtureRoot, "gx10", "projects"))
	if err != nil {
		t.Fatal(err)
	}
	s := ingestInto(t, []adapters.Source{{
		Harness: "claude-code", Root: root, Machine: "gx10",
	}})
	start := time.Now()
	if _, err := s.Daily(context.Background(), time.UTC); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sessions(context.Background(), time.UTC); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("stats aggregation took %v, budget is 5s", d)
	}
}
