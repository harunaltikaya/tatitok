package parity

// M2 Task 2 gate: the SQL-side stats aggregation must be byte-identical
// to the legacy full-scan-in-Go path over the fixture DB before the
// legacy path may be deleted. JSON bytes are compared because that is
// exactly what `stats --json` emits.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/store"
)

func TestSQLAggregationByteIdenticalToLegacy(t *testing.T) {
	root, err := filepath.Abs(filepath.Join(fixtureRoot, "gx10", "projects"))
	if err != nil {
		t.Fatal(err)
	}
	s := ingestInto(t, []adapters.Source{{
		Harness: "claude-code", Root: root, Machine: "gx10",
	}})
	ctx := context.Background()

	asJSON := func(v any, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// Several timezones incl. UTC, the capture zone, a western zone and a
	// non-whole-hour offset — day boundaries must bucket identically.
	for _, tzName := range []string{
		"UTC", "Europe/Istanbul", "America/Los_Angeles", "Asia/Kathmandu",
	} {
		tz, err := time.LoadLocation(tzName)
		if err != nil {
			t.Skipf("tzdata unavailable: %v", err)
		}
		t.Run(tzName, func(t *testing.T) {
			gotDaily := asJSON(s.Daily(ctx, tz))
			wantDaily := asJSON(s.DailyLegacy(ctx, tz))
			if string(gotDaily) != string(wantDaily) {
				t.Errorf("Daily diverges from legacy in %s:\nsql:    %.2000s\nlegacy: %.2000s",
					tzName, gotDaily, wantDaily)
			}
			gotSessions := asJSON(s.Sessions(ctx, tz))
			wantSessions := asJSON(s.SessionsLegacy(ctx, tz))
			if string(gotSessions) != string(wantSessions) {
				t.Errorf("Sessions diverges from legacy in %s:\nsql:    %.2000s\nlegacy: %.2000s",
					tzName, gotSessions, wantSessions)
			}
		})
	}
}

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

var _ = store.TokenSums{} // keep the store import if assertions change
