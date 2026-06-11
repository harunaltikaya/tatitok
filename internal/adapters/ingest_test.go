package adapters

// Contract-v2 ingest mechanics: cancellation, per-file rollback, and
// provenance stamping. Synthetic core.Event values are fine here — this
// tests the ingest layer's transaction/cancellation behavior (pure
// infrastructure), not adapter parsing; no log lines are fabricated
// (CLAUDE.md hard rule 1).

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/store"
)

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func synthEvent(i int) core.Event {
	return core.Event{
		ID:          core.EventID("fake", "m"+strconv.Itoa(i), "r"+strconv.Itoa(i)),
		TS:          time.Date(2026, 6, 10, 12, 0, i, 0, time.UTC),
		Machine:     "test",
		SourceKind:  core.SourceKindHarnessLog,
		Harness:     "fake",
		Provider:    "test",
		Model:       "model-x",
		ModelFamily: "model-x",
		TokensInput: 1, TokensOutput: 1,
		Accuracy: core.AccuracyExact,
	}
}

// fakeFile drives one file's emission through the sink.
type fakeFile struct {
	path      string
	batches   [][]core.Event
	readError string // FileDone carries this after the batches
}

// fakeAdapter emits its files in order and records how far it got, so
// tests can assert that a sink error cancelled the remaining work.
type fakeAdapter struct {
	files        []fakeFile
	batchesSent  int
	filesStarted int
}

func (*fakeAdapter) Name() string { return "fake" }
func (*fakeAdapter) Version() int { return 7 }
func (*fakeAdapter) Detect(Probe) ([]Source, error) {
	return []Source{{Harness: "fake", Root: "/nowhere", Machine: "test"}}, nil
}

func (a *fakeAdapter) Backfill(ctx context.Context, src Source, sink Sink) error {
	for _, f := range a.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.filesStarted++
		if err := sink.FileStart(f.path); err != nil {
			return err
		}
		events := 0
		for _, b := range f.batches {
			if err := sink.EmitBatch(f.path, b); err != nil {
				return err
			}
			a.batchesSent++
			events += len(b)
		}
		res := FileResult{
			Path: f.path, MTime: time.Now().UTC(), Size: 1,
			LineCount: events, Events: events, ReadError: f.readError,
		}
		if err := sink.FileDone(res); err != nil {
			return err
		}
	}
	return nil
}

func TestIngestSinkErrorCancelsBackfill(t *testing.T) {
	s := openTemp(t)
	bad := synthEvent(99)
	bad.Accuracy = "nope" // fails validation at insert → EmitBatch error
	a := &fakeAdapter{files: []fakeFile{
		{path: "/fake/ok.jsonl", batches: [][]core.Event{{synthEvent(1), synthEvent(2)}}},
		{path: "/fake/poison.jsonl", batches: [][]core.Event{{bad}, {synthEvent(3)}}},
		{path: "/fake/never.jsonl", batches: [][]core.Event{{synthEvent(4)}}},
	}}

	sum, err := IngestBackfill(context.Background(), s, a, []Source{{Harness: "fake"}})
	if err == nil {
		t.Fatal("want insert error to propagate, got nil")
	}
	// The adapter must have stopped at the failing batch: file 3 never
	// started, the second batch of file 2 never sent.
	if a.filesStarted != 2 || a.batchesSent != 1 {
		t.Fatalf("remaining work not cancelled: filesStarted=%d batchesSent=%d",
			a.filesStarted, a.batchesSent)
	}
	// File 1 committed; the poison file's transaction rolled back.
	n, err2 := s.CountEvents(context.Background())
	if err2 != nil {
		t.Fatal(err2)
	}
	if n != 2 {
		t.Fatalf("row count %d, want 2 (only the committed file)", n)
	}
	if sum.Files != 1 || sum.Inserted != 2 || sum.Emitted != 2 {
		t.Fatalf("summary counts discarded work: %+v", sum)
	}
}

func TestIngestContextCancellation(t *testing.T) {
	s := openTemp(t)
	a := &fakeAdapter{files: []fakeFile{
		{path: "/fake/a.jsonl", batches: [][]core.Event{{synthEvent(1)}}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := IngestBackfill(ctx, s, a, []Source{{Harness: "fake"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if a.filesStarted != 0 {
		t.Fatalf("adapter did work under a cancelled context: %d files", a.filesStarted)
	}
}

// A skipped source (FileDone with ReadError) discards its already-emitted
// events — the next backfill retries the whole file — while the sources
// table records what is missing.
func TestIngestReadErrorDiscardsPartialEvents(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := &fakeAdapter{files: []fakeFile{
		{path: "/fake/partial.jsonl",
			batches:   [][]core.Event{{synthEvent(1), synthEvent(2)}},
			readError: "disk on fire"},
		{path: "/fake/ok.jsonl", batches: [][]core.Event{{synthEvent(3)}}},
	}}

	sum, err := IngestBackfill(ctx, s, a, []Source{{Harness: "fake"}})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if sum.Skipped != 1 || sum.Files != 1 || sum.Inserted != 1 || sum.Emitted != 1 {
		t.Fatalf("summary wrong: %+v", sum)
	}
	n, err := s.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("partial events persisted: %d rows, want 1", n)
	}
	var readErr *string
	if err := s.DB().QueryRow(`SELECT read_error FROM sources WHERE path = ?`,
		"/fake/partial.jsonl").Scan(&readErr); err != nil {
		t.Fatalf("sources row: %v", err)
	}
	if readErr == nil || *readErr != "disk on fire" {
		t.Fatalf("read_error not persisted: %v", readErr)
	}
}

// A file with zero billable events still lands in the sources table — no
// silent success, every file is accounted for.
func TestIngestZeroEventFileRecorded(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := &fakeAdapter{files: []fakeFile{{path: "/fake/empty.jsonl"}}}
	sum, err := IngestBackfill(ctx, s, a, []Source{{Harness: "fake"}})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if sum.Files != 1 {
		t.Fatalf("zero-event file not counted: %+v", sum)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sources WHERE path = ?`,
		"/fake/empty.jsonl").Scan(&count); err != nil || count != 1 {
		t.Fatalf("zero-event file missing from sources: count=%d err=%v", count, err)
	}
}

// An adapter that returns without closing a file (no FileDone) is a
// contract violation, not a success.
func TestIngestMissingFileDoneIsAnError(t *testing.T) {
	s := openTemp(t)
	a := &noDoneAdapter{}
	_, err := IngestBackfill(context.Background(), s, a, []Source{{Harness: "fake"}})
	if err == nil {
		t.Fatal("want contract-violation error, got nil")
	}
	n, _ := s.CountEvents(context.Background())
	if n != 0 {
		t.Fatalf("unclosed file's events persisted: %d", n)
	}
}

type noDoneAdapter struct{ fakeAdapter }

func (a *noDoneAdapter) Backfill(ctx context.Context, src Source, sink Sink) error {
	if err := sink.FileStart("/fake/open.jsonl"); err != nil {
		return err
	}
	return sink.EmitBatch("/fake/open.jsonl", []core.Event{synthEvent(1)})
}

// scriptAdapter drives an arbitrary call sequence against the sink, for
// contract-violation tests.
type scriptAdapter struct {
	fakeAdapter
	run func(sink Sink) error
}

func (a *scriptAdapter) Backfill(_ context.Context, _ Source, sink Sink) error {
	return a.run(sink)
}

// The ingest sink rejects every bracketing violation (M2.1 hardening):
// nothing lands in the DB and the run fails loudly.
func TestIngestContractViolationsRejected(t *testing.T) {
	done := func(s Sink, path string, events int) error {
		return s.FileDone(FileResult{Path: path, MTime: time.Now().UTC(),
			Size: 1, LineCount: events, Events: events})
	}
	cases := []struct {
		name string
		run  func(s Sink) error
	}{
		{"batch without FileStart", func(s Sink) error {
			return s.EmitBatch("/fake/a", []core.Event{synthEvent(1)})
		}},
		{"FileDone without FileStart", func(s Sink) error {
			return done(s, "/fake/a", 0)
		}},
		{"duplicate FileStart for open file", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			return s.FileStart("/fake/a")
		}},
		{"FileStart while another file open", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			return s.FileStart("/fake/b")
		}},
		{"duplicate path across brackets", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			if err := done(s, "/fake/a", 0); err != nil {
				return err
			}
			return s.FileStart("/fake/a")
		}},
		{"batch after FileDone", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			if err := done(s, "/fake/a", 0); err != nil {
				return err
			}
			return s.EmitBatch("/fake/a", []core.Event{synthEvent(1)})
		}},
		{"second FileDone", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			if err := done(s, "/fake/a", 0); err != nil {
				return err
			}
			return done(s, "/fake/a", 0)
		}},
		{"batch for foreign file", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			return s.EmitBatch("/fake/b", []core.Event{synthEvent(1)})
		}},
		{"FileDone for foreign file", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			return done(s, "/fake/b", 0)
		}},
		{"Events overdeclared", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			if err := s.EmitBatch("/fake/a", []core.Event{synthEvent(1)}); err != nil {
				return err
			}
			return done(s, "/fake/a", 2)
		}},
		{"Events underdeclared", func(s Sink) error {
			if err := s.FileStart("/fake/a"); err != nil {
				return err
			}
			if err := s.EmitBatch("/fake/a", []core.Event{synthEvent(1)}); err != nil {
				return err
			}
			return done(s, "/fake/a", 0)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			_, err := IngestBackfill(context.Background(), s,
				&scriptAdapter{run: tc.run}, []Source{{Harness: "fake"}})
			if err == nil {
				t.Fatal("want contract-violation error, got nil")
			}
			if !strings.Contains(err.Error(), "contract violation") {
				t.Fatalf("want a contract-violation error, got: %v", err)
			}
			n, err2 := s.CountEvents(context.Background())
			if err2 != nil {
				t.Fatal(err2)
			}
			if n != 0 {
				t.Fatalf("violating run persisted %d events", n)
			}
		})
	}
}

// Empty-model events are LEGAL (codex usage before the first
// turn_context) but counted as a health signal and queryable via doctor
// (M2.1 item 9).
func TestIngestCountsEmptyModelEvents(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	noModel := synthEvent(2)
	noModel.Model, noModel.ModelFamily, noModel.Provider = "", "", ""
	a := &fakeAdapter{files: []fakeFile{
		{path: "/fake/a.jsonl", batches: [][]core.Event{{synthEvent(1), noModel}}},
	}}
	sum, err := IngestBackfill(ctx, s, a, []Source{{Harness: "fake"}})
	if err != nil {
		t.Fatalf("empty-model event rejected: %v", err)
	}
	if sum.Inserted != 2 || sum.EmptyModel != 1 {
		t.Fatalf("summary wrong: %+v", sum)
	}
	n, err := s.CountEmptyModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("doctor count = %d, want 1", n)
	}
}

// Provenance stamping: every event row and sources row carries the
// adapter's version.
func TestIngestStampsAdapterVersion(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := &fakeAdapter{files: []fakeFile{
		{path: "/fake/a.jsonl", batches: [][]core.Event{{synthEvent(1), synthEvent(2)}}},
	}}
	if _, err := IngestBackfill(ctx, s, a, []Source{{Harness: "fake"}}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM usage_events WHERE adapter_version IS NOT 7`,
		`SELECT COUNT(*) FROM sources WHERE adapter_version IS NOT 7`,
	} {
		var n int
		if err := s.DB().QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%d rows missing adapter_version stamp (%s)", n, q)
		}
	}
	rows, err := s.Provenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Harness != "fake" ||
		rows[0].AdapterVersion == nil || *rows[0].AdapterVersion != 7 ||
		rows[0].Events != 2 || rows[0].SourceFiles != 1 {
		t.Fatalf("provenance wrong: %+v", rows)
	}
}
