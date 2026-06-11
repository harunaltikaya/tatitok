package parity

// M2 Task 5 — cross-source integration over ONE database holding all
// three harnesses' fixture sets (claude-code 600 + codex 1,946 +
// opencode 834 unique events).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const (
	wantClaudeRows   = 600
	wantCodexRows    = 1946
	wantOpencodeRows = 834
	wantCombinedRows = wantClaudeRows + wantCodexRows + wantOpencodeRows
)

// ingestAllFixtures runs every parity set's gx10 fixtures into st once,
// returning the total inserted rows.
func ingestAllFixtures(t *testing.T, st *store.Store) int {
	t.Helper()
	inserted := 0
	for _, set := range paritySets {
		machineDir := filepath.Join(set.fixtureBase, "gx10")
		sum, err := adapters.IngestBackfill(context.Background(), st, set.adapter,
			[]adapters.Source{{
				Harness: set.harness, Root: set.root(t, machineDir), Machine: "gx10",
			}})
		if err != nil {
			t.Fatalf("%s: ingest: %v", set.harness, err)
		}
		if sum.ParseErrors != 0 || sum.Skipped != 0 {
			t.Fatalf("%s: unhealthy fixture ingest: %+v", set.harness, sum)
		}
		inserted += sum.Inserted
	}
	return inserted
}

// One DB ingests all three sources; re-ingesting EVERYTHING a second time
// inserts 0 rows, and no cross-adapter collision eats an event (the
// combined count is exactly the sum of the per-adapter counts).
func TestCombinedDBIdempotency(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "combined.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if n := ingestAllFixtures(t, st); n != wantCombinedRows {
		t.Fatalf("first combined ingest inserted %d rows, want %d", n, wantCombinedRows)
	}
	daily1, err := st.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}

	if n := ingestAllFixtures(t, st); n != 0 {
		t.Fatalf("second combined ingest inserted %d rows, want 0", n)
	}
	total, err := st.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != wantCombinedRows {
		t.Fatalf("combined row count %d, want %d", total, wantCombinedRows)
	}
	daily2, err := st.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(daily1) != len(daily2) {
		t.Fatal("daily stats changed after combined re-ingest")
	}
	for i := range daily1 {
		if daily1[i].Date != daily2[i].Date || daily1[i].TokenSums != daily2[i].TokenSums {
			t.Fatalf("day %s changed after re-ingest", daily1[i].Date)
		}
	}
}

// Per-harness breakdown: every day's totals must equal the sum of its
// harness breakdowns, and the --harness filtered report must equal that
// harness's breakdown rows exactly.
func TestHarnessBreakdownConsistency(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "combined.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	ingestAllFixtures(t, st)

	all, err := st.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no combined daily rows")
	}
	seenHarnesses := map[string]bool{}
	for _, d := range all {
		var sum store.TokenSums
		for _, h := range d.HarnessBreakdowns {
			sum.Input += h.Input
			sum.Output += h.Output
			sum.CacheWrite += h.CacheWrite
			sum.CacheRead += h.CacheRead
			seenHarnesses[h.Harness] = true
		}
		if sum != d.TokenSums {
			t.Fatalf("%s: harness breakdowns sum to %+v, day totals %+v",
				d.Date, sum, d.TokenSums)
		}
	}
	for _, h := range []string{"claude-code", "codex", "opencode"} {
		if !seenHarnesses[h] {
			t.Fatalf("harness %s missing from breakdowns (got %v)", h, seenHarnesses)
		}
	}

	// filtered report == that harness's breakdown slice
	for h := range seenHarnesses {
		filtered, err := st.Daily(ctx, time.UTC, h)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]store.TokenSums{}
		for _, d := range all {
			for _, hb := range d.HarnessBreakdowns {
				if hb.Harness == h {
					want[d.Date] = hb.TokenSums
				}
			}
		}
		if len(filtered) != len(want) {
			t.Fatalf("--harness %s: %d days, want %d", h, len(filtered), len(want))
		}
		for _, d := range filtered {
			if d.TokenSums != want[d.Date] {
				t.Fatalf("--harness %s %s: %+v, want %+v", h, d.Date, d.TokenSums, want[d.Date])
			}
			if len(d.HarnessBreakdowns) != 1 || d.HarnessBreakdowns[0].Harness != h {
				t.Fatalf("--harness %s %s: breakdown not singular: %+v", h, d.Date, d.HarnessBreakdowns)
			}
		}
	}
}

// Cross-adapter ID isolation: identical-looking native records from
// different harnesses must never produce the same event ID — the harness
// is a hash component of both ID forms.
func TestCrossAdapterIDIsolation(t *testing.T) {
	harnesses := []string{"claude-code", "codex", "opencode"}
	seen := map[string]string{}
	for _, h := range harnesses {
		id := core.EventID(h, "msg_same", "req_same")
		if prev, dup := seen[id]; dup {
			t.Fatalf("EventID collision between %s and %s", prev, h)
		}
		seen[id] = h
		fid := core.FallbackID(h, "same/file.jsonl", 7, "2026-06-11T00:00:00Z")
		if prev, dup := seen[fid]; dup {
			t.Fatalf("FallbackID collision between %s and %s", prev, h)
		}
		seen[fid] = h
	}
}

// `doctor --scan-content` semantics over the combined DB: every stored
// raw and meta blob from all three adapters passes the sanitizer
// invariants.
func TestCombinedDBScanContentClean(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "combined.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ingestAllFixtures(t, st)

	events, findings := 0, 0
	err = st.ForEachRaw(context.Background(), func(id string, raw, meta []byte) error {
		events++
		for _, blob := range [][]byte{raw, meta} {
			if len(blob) == 0 {
				continue
			}
			fs, err := core.CheckRawSanitized(blob)
			if err != nil {
				t.Errorf("%s: %v", id, err)
				findings++
				continue
			}
			for _, f := range fs {
				t.Errorf("%s: %s", id, f)
				findings++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if events != wantCombinedRows {
		t.Fatalf("scanned %d events, want %d", events, wantCombinedRows)
	}
	if findings != 0 {
		t.Fatalf("scan-content found %d violations in the combined DB", findings)
	}
}
