package claudecode

// All parsing tests run against the committed, owner-harvested fixture set
// (hard rule 1: no fabricated log lines). The numeric expectations below
// are measured properties of that set and change only on re-harvest.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const fixtureBase = "../../../testdata/fixtures/claude-code/gx10"

// Measured from the committed gx10 fixture set: 1323 assistant records
// with usage, minus 2 <synthetic> ones; 721 duplicate (message id,
// request id) occurrences collapse to 600 unique events.
const (
	wantEmitted = 1321
	wantUnique  = 600
)

func fixtureSource(t *testing.T) adapters.Source {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(fixtureBase, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture root missing (harvest + commit first): %v", err)
	}
	return adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}
}

func backfillAll(t *testing.T) []adapters.Event {
	t.Helper()
	var events []adapters.Event
	if err := (Adapter{}).Backfill(fixtureSource(t), func(e adapters.Event) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	return events
}

func TestDetect(t *testing.T) {
	abs, _ := filepath.Abs(fixtureBase)
	probe := func(env map[string]string, home string) adapters.Probe {
		return adapters.Probe{
			Getenv:  func(k string) string { return env[k] },
			HomeDir: home,
			Machine: "gx10",
		}
	}

	t.Run("env override wins", func(t *testing.T) {
		srcs, err := (Adapter{}).Detect(probe(map[string]string{"CLAUDE_CONFIG_DIR": abs}, "/nonexistent"))
		if err != nil {
			t.Fatal(err)
		}
		want := []adapters.Source{{Harness: harnessName, Root: filepath.Join(abs, "projects"), Machine: "gx10"}}
		if !reflect.DeepEqual(srcs, want) {
			t.Fatalf("got %+v, want %+v", srcs, want)
		}
	})

	t.Run("comma-separated override", func(t *testing.T) {
		srcs, err := (Adapter{}).Detect(probe(map[string]string{
			"CLAUDE_CONFIG_DIR": abs + ", /nonexistent-dir"}, "/nonexistent"))
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 1 || srcs[0].Root != filepath.Join(abs, "projects") {
			t.Fatalf("got %+v", srcs)
		}
	})

	t.Run("legacy and xdg defaults", func(t *testing.T) {
		home := t.TempDir()
		for _, d := range []string{".claude", filepath.Join(".config", "claude")} {
			if err := os.MkdirAll(filepath.Join(home, d, "projects"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		srcs, err := (Adapter{}).Detect(probe(nil, home))
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 2 {
			t.Fatalf("want both default roots, got %+v", srcs)
		}
	})

	t.Run("nothing found", func(t *testing.T) {
		srcs, err := (Adapter{}).Detect(probe(nil, t.TempDir()))
		if err != nil || len(srcs) != 0 {
			t.Fatalf("got %+v, %v", srcs, err)
		}
	})
}

func TestBackfillFixtures(t *testing.T) {
	events := backfillAll(t)

	var billable []adapters.Event
	ids := map[string]bool{}
	for _, e := range events {
		if e.ID == "" { // zero-event file marker
			continue
		}
		billable = append(billable, e)
		ids[e.ID] = true
	}
	if len(billable) != wantEmitted {
		t.Errorf("emitted %d billable events, want %d", len(billable), wantEmitted)
	}
	if len(ids) != wantUnique {
		t.Errorf("got %d unique ids, want %d", len(ids), wantUnique)
	}

	for _, e := range billable {
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		if e.Accuracy != core.AccuracyExact {
			t.Fatalf("event %s accuracy %q, want exact", e.ID, e.Accuracy)
		}
		if e.Model == "<synthetic>" {
			t.Fatalf("synthetic record emitted: %s", e.ID)
		}
		if e.Provider != "anthropic" || e.Harness != harnessName || e.Machine != "gx10" {
			t.Fatalf("bad identity fields: %+v", e.Event)
		}
		if e.Project == "" || e.SessionID == "" || len(e.Raw) == 0 {
			t.Fatalf("missing project/session/raw on %s", e.ID)
		}
		if e.File.Path == "" || e.File.LineCount == 0 {
			t.Fatalf("missing file provenance on %s", e.ID)
		}
		if e.File.ParseErrors != 0 {
			t.Fatalf("fixture parse errors in %s: %d", e.File.Path, e.File.ParseErrors)
		}
	}
}

// Hard rule 5: re-ingesting the same files twice must yield identical DB
// contents.
func TestReingestIdempotent(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	ingest := func() adapters.IngestSummary {
		t.Helper()
		sum, err := adapters.IngestBackfill(ctx, s, Adapter{},
			[]adapters.Source{fixtureSource(t)})
		if err != nil {
			t.Fatalf("ingest: %v", err)
		}
		return sum
	}

	first := ingest()
	if first.Inserted != wantUnique {
		t.Fatalf("first ingest inserted %d, want %d", first.Inserted, wantUnique)
	}
	if first.Emitted != wantEmitted {
		t.Fatalf("first ingest emitted %d, want %d", first.Emitted, wantEmitted)
	}
	if first.ParseErrors != 0 {
		t.Fatalf("parse errors on fixtures: %d", first.ParseErrors)
	}
	daily1, err := s.Daily(ctx, time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	if again := ingest(); again.Inserted != 0 {
		t.Fatalf("re-ingest inserted %d new rows, want 0", again.Inserted)
	}
	daily2, err := s.Daily(ctx, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(daily1, daily2) {
		t.Fatal("daily stats changed after re-ingest")
	}
	total, err := s.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != wantUnique {
		t.Fatalf("row count %d after re-ingest, want %d", total, wantUnique)
	}
}

// Golden test: fixtures in → exact expected normalized events out.
// expected/events.json is generated ONCE from the first verified-parity
// run (milestone-1 Task 4), then frozen. Regenerate explicitly with:
//
//	TATITOK_UPDATE_GOLDEN=1 go test ./internal/adapters/claudecode -run TestGoldenEvents
func TestGoldenEvents(t *testing.T) {
	golden := filepath.Join(fixtureBase, "expected", "events.json")

	events := backfillAll(t)
	var normalized []core.Event
	seen := map[string]bool{}
	for _, e := range events {
		if e.ID == "" || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		normalized = append(normalized, e.Event)
	}
	got, err := json.MarshalIndent(normalized, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	if os.Getenv("TATITOK_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d events)", golden, len(normalized))
		return
	}

	want, err := os.ReadFile(golden)
	if os.IsNotExist(err) {
		// TODO(milestone-1 Task 6): generate expected/events.json from the
		// first verified-parity run, commit it, and let this test enforce it.
		t.Skip("expected/events.json not generated yet (waiting for first verified-parity run)")
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("normalized events diverge from frozen golden file %s "+
			"(adapter behavior changed — bump AdapterVersion and regenerate "+
			"ONLY after parity re-verifies)", golden)
	}
}
