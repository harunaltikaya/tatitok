package codex

// All parsing tests run against the committed, owner-harvested fixture set
// (hard rule 1: no fabricated log lines). The numeric expectations below
// are measured properties of that set and change only on re-harvest.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const fixtureBase = "../../../testdata/fixtures/codex/gx10"

// Measured from the committed gx10 fixture set: 1,946 token_count events
// with non-null info across 12,104 lines (12 of the 1,958 token_count
// records carry info:null and are not billable). Codex has no native
// event ids → no dedup; every occurrence is unique.
const (
	wantEmitted = 1946
	wantUnique  = 1946
)

func fixtureSource(t *testing.T) adapters.Source {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(fixtureBase, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture root missing (harvest + commit first): %v", err)
	}
	return adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}
}

// collectSink mirrors the claudecode test sink: collects everything and
// fails the test on contract-v2 bracketing violations.
type collectSink struct {
	t       *testing.T
	events  []core.Event
	byFile  map[string][]core.Event
	results []adapters.FileResult
	done    map[string]bool
	open    string // file declared by FileStart but no FileDone yet
}

func newCollectSink(t *testing.T) *collectSink {
	return &collectSink{t: t,
		byFile: map[string][]core.Event{}, done: map[string]bool{}}
}

func (c *collectSink) FileStart(path string) error {
	c.t.Helper()
	if c.done[path] {
		c.t.Fatalf("duplicate path %s in one backfill run", path)
	}
	if c.open != "" {
		c.t.Fatalf("FileStart for %s while %s is still open", path, c.open)
	}
	c.open = path
	return nil
}

func (c *collectSink) EmitBatch(path string, events []core.Event) error {
	c.t.Helper()
	if len(events) == 0 || len(events) > adapters.BatchSize {
		c.t.Fatalf("batch of %d events for %s (BatchSize %d)",
			len(events), path, adapters.BatchSize)
	}
	if c.open != path {
		c.t.Fatalf("batch for %s outside its FileStart/FileDone bracket (open: %q)",
			path, c.open)
	}
	c.events = append(c.events, events...)
	c.byFile[path] = append(c.byFile[path], events...)
	return nil
}

func (c *collectSink) FileDone(res adapters.FileResult) error {
	c.t.Helper()
	if c.open != res.Path {
		c.t.Fatalf("FileDone for %s outside its FileStart bracket (open: %q)",
			res.Path, c.open)
	}
	if got := len(c.byFile[res.Path]); res.ReadError == "" && got != res.Events {
		c.t.Fatalf("%s: FileDone.Events=%d but %d events emitted",
			res.Path, res.Events, got)
	}
	c.done[res.Path] = true
	c.open = ""
	c.results = append(c.results, res)
	return nil
}

func backfillAll(t *testing.T) *collectSink {
	t.Helper()
	sink := newCollectSink(t)
	if err := (Adapter{}).Backfill(context.Background(), fixtureSource(t), sink); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if sink.open != "" {
		t.Fatalf("backfill returned with %s still open", sink.open)
	}
	return sink
}

func TestDetect(t *testing.T) {
	probe := func(env map[string]string, home string) adapters.Probe {
		return adapters.Probe{
			Getenv:  func(k string) string { return env[k] },
			HomeDir: home,
			Machine: "gx10",
		}
	}

	t.Run("codex_home override wins", func(t *testing.T) {
		abs, err := filepath.Abs(fixtureBase)
		if err != nil {
			t.Fatal(err)
		}
		srcs, err := (Adapter{}).Detect(probe(map[string]string{"CODEX_HOME": abs}, "/nonexistent"))
		if err != nil {
			t.Fatal(err)
		}
		want := []adapters.Source{{Harness: harnessName,
			Root: filepath.Join(abs, "sessions"), Machine: "gx10"}}
		if !reflect.DeepEqual(srcs, want) {
			t.Fatalf("got %+v, want %+v", srcs, want)
		}
	})

	t.Run("default home", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".codex", "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		srcs, err := (Adapter{}).Detect(probe(nil, home))
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 1 || srcs[0].Root != filepath.Join(home, ".codex", "sessions") {
			t.Fatalf("got %+v", srcs)
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
	sink := backfillAll(t)

	ids := map[string]bool{}
	for _, e := range sink.events {
		ids[e.ID] = true
	}
	if len(sink.events) != wantEmitted {
		t.Errorf("emitted %d billable events, want %d", len(sink.events), wantEmitted)
	}
	if len(ids) != wantUnique {
		t.Errorf("got %d unique ids, want %d", len(ids), wantUnique)
	}

	for i := range sink.events {
		e := &sink.events[i]
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		if e.Accuracy != core.AccuracyExact {
			t.Fatalf("event %s accuracy %q, want exact", e.ID, e.Accuracy)
		}
		if e.Harness != harnessName || e.Machine != "gx10" {
			t.Fatalf("bad identity fields: %+v", e)
		}
		if e.Provider == "" || e.Model == "" {
			t.Fatalf("missing provider/model on %s (provider=%q model=%q)",
				e.ID, e.Provider, e.Model)
		}
		if e.SessionID == "" || len(e.Raw) == 0 {
			t.Fatalf("missing session/raw on %s", e.ID)
		}
		// codex reports reasoning tokens separately on every usage record
		if e.TokensReasoning == nil {
			t.Fatalf("missing reasoning tokens on %s", e.ID)
		}
		if e.TokensCacheWrite != 0 {
			t.Fatalf("codex event %s has cache-write tokens", e.ID)
		}
		if e.TokensInput < 0 {
			t.Fatalf("negative input tokens on %s (cached > input?)", e.ID)
		}
	}

	totalEvents := 0
	for _, res := range sink.results {
		if res.Path == "" || res.LineCount == 0 {
			t.Fatalf("missing file provenance: %+v", res)
		}
		if res.ParseErrors != 0 {
			t.Fatalf("fixture parse errors in %s: %d", res.Path, res.ParseErrors)
		}
		if res.ReadError != "" || res.IncompleteTail {
			t.Fatalf("fixture file reported unhealthy: %+v", res)
		}
		totalEvents += res.Events
	}
	if totalEvents != wantEmitted {
		t.Errorf("FileResult.Events sums to %d, want %d", totalEvents, wantEmitted)
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
			[]adapters.Source{fixtureSource(t)}, nil)
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
	daily1, err := s.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}

	if again := ingest(); again.Inserted != 0 {
		t.Fatalf("re-ingest inserted %d new rows, want 0", again.Inserted)
	}
	daily2, err := s.Daily(ctx, time.UTC, "")
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

// goldenCeremony is the mandatory procedure for changing the frozen
// golden file; it is printed wherever someone could be tempted to skip it.
const goldenCeremony = `regenerating expected/events.json requires the full ceremony:
  1. bump codex.AdapterVersion (format handling changed by definition)
  2. TATITOK_UPDATE_GOLDEN=1 TATITOK_CONFIRM_PARITY=1 go test ./internal/adapters/codex -run TestGoldenEvents
  3. re-run the fixture parity gate: go test ./internal/parity (must stay EXACT)
  4. owner re-runs 'make parity-full-codex' against live logs before the commit counts as verified
commit the golden diff together with the AdapterVersion bump and note the parity re-verification in the message`

// Golden test: fixtures in → exact expected normalized events out.
// expected/events.json was generated from the first verified-parity run
// and is FROZEN: a missing file fails the test, and regeneration demands
// an explicit two-flag confirmation of the ceremony above.
func TestGoldenEvents(t *testing.T) {
	golden := filepath.Join(fixtureBase, "expected", "events.json")

	sink := backfillAll(t)
	var normalized []core.Event
	seen := map[string]bool{}
	for _, e := range sink.events {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		normalized = append(normalized, e)
	}
	got, err := json.MarshalIndent(normalized, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	if os.Getenv("TATITOK_UPDATE_GOLDEN") == "1" {
		if os.Getenv("TATITOK_CONFIRM_PARITY") != "1" {
			t.Fatalf("TATITOK_UPDATE_GOLDEN=1 refused without TATITOK_CONFIRM_PARITY=1\n%s",
				goldenCeremony)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d events)\n%s", golden, len(normalized), goldenCeremony)
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		// A missing golden file FAILS — skipping would let the whole
		// normalization contract go unchecked while looking green.
		t.Fatalf("frozen golden file unreadable: %v\n%s", err, goldenCeremony)
	}
	if string(got) != string(want) {
		t.Fatalf("normalized events diverge from frozen golden file %s\n%s",
			golden, goldenCeremony)
	}
}

// cancelOnFirstStartSink cancels the run's context as soon as a file is
// declared, so the cancellation lands while that file is mid-parse.
type cancelOnFirstStartSink struct {
	*collectSink
	cancel context.CancelFunc
}

func (c *cancelOnFirstStartSink) FileStart(path string) error {
	c.cancel()
	return c.collectSink.FileStart(path)
}

// In-file cancellation (M2.1 item 5): a context canceled while a file is
// being parsed stops the parse loop at the next checkpoint — the file
// never completes (no FileDone), nothing is emitted after the cancel,
// and the cancellation error propagates unchanged.
func TestBackfillCanceledMidFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newCollectSink(t)
	err := (Adapter{}).Backfill(ctx, fixtureSource(t),
		&cancelOnFirstStartSink{collectSink: sink, cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(sink.results) != 0 || len(sink.events) != 0 {
		t.Fatalf("work continued after cancellation: %d results, %d events",
			len(sink.results), len(sink.events))
	}
}
