package claudecode

// All parsing tests run against the committed, owner-harvested fixture set
// (hard rule 1: no fabricated log lines). The numeric expectations below
// are measured properties of that set and change only on re-harvest.

import (
	"bytes"
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

// collectSink is a contract-v2 test sink: it collects everything and
// fails the test on any sequencing violation (oversized batch, batches
// interleaving across files, a batch after its file's FileDone, or a
// second FileDone for the same path).
type collectSink struct {
	t       *testing.T
	events  []core.Event // billable events in emission order
	byFile  map[string][]core.Event
	results []adapters.FileResult
	done    map[string]bool
	open    string // file with batches emitted but no FileDone yet
}

func newCollectSink(t *testing.T) *collectSink {
	return &collectSink{t: t,
		byFile: map[string][]core.Event{}, done: map[string]bool{}}
}

func (c *collectSink) EmitBatch(path string, events []core.Event) error {
	c.t.Helper()
	if len(events) == 0 || len(events) > adapters.BatchSize {
		c.t.Fatalf("batch of %d events for %s (BatchSize %d)",
			len(events), path, adapters.BatchSize)
	}
	if c.done[path] {
		c.t.Fatalf("batch for %s after its FileDone", path)
	}
	if c.open != "" && c.open != path {
		c.t.Fatalf("batch for %s while %s is still open", path, c.open)
	}
	c.open = path
	c.events = append(c.events, events...)
	c.byFile[path] = append(c.byFile[path], events...)
	return nil
}

func (c *collectSink) FileDone(res adapters.FileResult) error {
	c.t.Helper()
	if c.done[res.Path] {
		c.t.Fatalf("second FileDone for %s", res.Path)
	}
	if c.open != "" && c.open != res.Path {
		c.t.Fatalf("FileDone for %s while %s is still open", res.Path, c.open)
	}
	// A skipped source (ReadError) may follow partial batches that the
	// ingest layer would discard; the Events count contract only holds
	// for files read to completion.
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
		if e.Model == "<synthetic>" {
			t.Fatalf("synthetic record emitted: %s", e.ID)
		}
		if e.Provider != "anthropic" || e.Harness != harnessName || e.Machine != "gx10" {
			t.Fatalf("bad identity fields: %+v", e)
		}
		if e.Project == "" || e.SessionID == "" || len(e.Raw) == 0 {
			t.Fatalf("missing project/session/raw on %s", e.ID)
		}
	}

	// Per-file health: every fixture file reports clean, with provenance.
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

// I/O failure honesty: unreadable project dirs and session files are
// counted as skipped sources, persisted to the sources table with their
// read error, and never silently dropped. Uses COPIES of a real fixture
// file with permissions removed — no log lines are fabricated.
func TestSkippedSourcesSurfaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based test cannot run as root")
	}
	fixtures, err := filepath.Glob(filepath.Join(fixtureBase, "projects", "*", "*.jsonl"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixture session files: %v", err)
	}
	realLog, err := os.ReadFile(fixtures[0])
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "projects")
	mk := func(project, name string, perm os.FileMode) string {
		p := filepath.Join(root, project, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, realLog, perm); err != nil {
			t.Fatal(err)
		}
		return p
	}
	readable := mk("-project-ok", "a.jsonl", 0o644)
	unreadableFile := mk("-project-locked", "b.jsonl", 0o000)
	unreadableDir := filepath.Join(root, "-project-dark")
	if err := os.MkdirAll(unreadableDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // let TempDir cleanup succeed
		_ = os.Chmod(unreadableDir, 0o755)
		_ = os.Chmod(unreadableFile, 0o644)
	})

	src := adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}
	sink := newCollectSink(t)
	if err := (Adapter{}).Backfill(context.Background(), src, sink); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	skipped := map[string]bool{}
	billableFiles := map[string]bool{}
	for _, res := range sink.results {
		if res.ReadError != "" {
			skipped[res.Path] = true
		} else if res.Events > 0 {
			billableFiles[res.Path] = true
		}
	}
	if !skipped[unreadableFile] || !skipped[unreadableDir] || len(skipped) != 2 {
		t.Fatalf("skipped markers wrong: %v", skipped)
	}
	if !billableFiles[readable] || len(billableFiles) != 1 {
		t.Fatalf("readable file not ingested normally: %v", billableFiles)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	sum, err := adapters.IngestBackfill(ctx, s, Adapter{}, []adapters.Source{src})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if sum.Skipped != 2 || sum.Files != 1 {
		t.Fatalf("summary: skipped=%d files=%d, want 2/1", sum.Skipped, sum.Files)
	}

	readError := func(path string) (string, bool) {
		var re *string
		err := s.DB().QueryRow(`SELECT read_error FROM sources WHERE path = ?`, path).Scan(&re)
		if err != nil {
			t.Fatalf("sources row for %s: %v", path, err)
		}
		if re == nil {
			return "", false
		}
		return *re, true
	}
	if re, ok := readError(unreadableFile); !ok || re == "" {
		t.Fatalf("unreadable file has no persisted read_error")
	}
	if re, ok := readError(unreadableDir); !ok || re == "" {
		t.Fatalf("unreadable dir has no persisted read_error")
	}
	if _, ok := readError(readable); ok {
		t.Fatalf("readable file has a read_error")
	}

	// Once readable again, the next backfill ingests the file and clears
	// its read_error via the sources upsert.
	if err := os.Chmod(unreadableFile, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err = adapters.IngestBackfill(ctx, s, Adapter{}, []adapters.Source{src})
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if sum.Skipped != 1 { // the dir is still unreadable
		t.Fatalf("re-ingest skipped=%d, want 1", sum.Skipped)
	}
	if _, ok := readError(unreadableFile); ok {
		t.Fatalf("read_error not cleared after successful re-ingest")
	}
}

// Partial-tail classification: an unterminated final line that fails to
// parse is a write in progress — incomplete_tail bookkeeping, not a parse
// error — and the next backfill of the completed file clears it. Uses a
// byte-level truncation of a real fixture file (no fabricated log lines).
func TestIncompleteTailClassification(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join(fixtureBase, "projects", "*", "*.jsonl"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixture session files: %v", err)
	}
	full, err := os.ReadFile(fixtures[0])
	if err != nil {
		t.Fatal(err)
	}
	trimmed := bytes.TrimRight(full, "\n")
	lastStart := bytes.LastIndexByte(trimmed, '\n') + 1
	cut := lastStart + min(40, (len(trimmed)-lastStart)/2)
	frag := trimmed[:cut]
	if json.Valid(frag[lastStart:]) {
		t.Fatalf("truncated tail unexpectedly parses — adjust the cut")
	}

	root := filepath.Join(t.TempDir(), "projects")
	path := filepath.Join(root, "-project-tail", "a.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, frag, 0o644); err != nil {
		t.Fatal(err)
	}
	src := adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}

	tailSink := newCollectSink(t)
	if err := (Adapter{}).Backfill(context.Background(), src, tailSink); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(tailSink.results) != 1 {
		t.Fatalf("want 1 file result, got %d", len(tailSink.results))
	}
	res := tailSink.results[0]
	if !res.IncompleteTail {
		t.Error("unterminated unparseable tail not flagged as IncompleteTail")
	}
	if res.ParseErrors != 0 {
		t.Errorf("tail counted as parse error (ParseErrors=%d)", res.ParseErrors)
	}
	if res.ReadError != "" {
		t.Errorf("tail treated as read error: %s", res.ReadError)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	tailFlag := func() int {
		var n int
		if err := s.DB().QueryRow(
			`SELECT incomplete_tail FROM sources WHERE path = ?`, path).Scan(&n); err != nil {
			t.Fatalf("sources row: %v", err)
		}
		return n
	}
	if _, err := adapters.IngestBackfill(ctx, s, Adapter{}, []adapters.Source{src}); err != nil {
		t.Fatal(err)
	}
	if tailFlag() != 1 {
		t.Fatal("incomplete_tail not persisted")
	}

	// The "write" completes; the next backfill clears the flag.
	if err := os.WriteFile(path, full, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := adapters.IngestBackfill(ctx, s, Adapter{}, []adapters.Source{src})
	if err != nil {
		t.Fatal(err)
	}
	if tailFlag() != 0 {
		t.Fatal("incomplete_tail not cleared by next backfill")
	}
	if sum.ParseErrors != 0 {
		t.Fatalf("completed file has parse errors: %d", sum.ParseErrors)
	}
}

// goldenCeremony is the mandatory procedure for changing the frozen
// golden file; it is printed wherever someone could be tempted to skip it.
const goldenCeremony = `regenerating expected/events.json requires the full ceremony:
  1. bump claudecode.AdapterVersion (format handling changed by definition)
  2. TATITOK_UPDATE_GOLDEN=1 TATITOK_CONFIRM_PARITY=1 go test ./internal/adapters/claudecode -run TestGoldenEvents
  3. re-run the fixture parity gate: go test ./internal/parity (must stay EXACT)
  4. owner re-runs 'make parity-full' against live logs before the commit counts as verified
commit the golden diff together with the AdapterVersion bump and note the parity re-verification in the message`

// Golden test: fixtures in → exact expected normalized events out.
// expected/events.json was generated from the first verified-parity run
// (milestone-1 Task 4) and is FROZEN: a missing file fails the test, and
// regeneration demands an explicit two-flag confirmation of the ceremony
// above — golden updates are never casual.
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
