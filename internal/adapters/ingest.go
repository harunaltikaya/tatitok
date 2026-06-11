package adapters

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/modelmap"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// IngestSummary aggregates one backfill run.
type IngestSummary struct {
	Files    int
	Lines    int
	Emitted  int // billable events emitted by the adapter
	Inserted int // new rows (duplicates collapse on the deterministic ID)
	// Replaced counts stored events whose source row changed since the
	// previous ingest and were updated to mirror it (mutable stores —
	// OpenCode rewrites a message row while the turn is in flight). The
	// correction mirrors source truth; it is reported, never silent (AS-4).
	Replaced int
	// EmptyModel counts committed events that carry no model — legal
	// (codex token_count records before the first turn_context) but a
	// health signal worth surfacing; also queryable via doctor.
	EmptyModel  int
	ParseErrors int
	// Skipped counts sources (files or project dirs) the adapter could
	// not read — recorded in the sources table with their read error,
	// reported distinctly from parse errors. Skipped > 0 means the run
	// completed but the DB does not reflect the full log history.
	Skipped int
	// TouchedDays are the UTC days whose rollup rows this run's inserts
	// and replacements touched, sorted (M4 Task 3 — the SSE stream
	// tells dashboards which days to refetch).
	TouchedDays []string
}

// mergeTouched unions newly touched days into the summary, keeping the
// slice sorted and unique (per-pass day counts are tiny).
func (s *IngestSummary) mergeTouched(days []string) {
	for _, d := range days {
		i := sort.SearchStrings(s.TouchedDays, d)
		if i < len(s.TouchedDays) && s.TouchedDays[i] == d {
			continue
		}
		s.TouchedDays = append(s.TouchedDays, "")
		copy(s.TouchedDays[i+1:], s.TouchedDays[i:])
		s.TouchedDays[i] = d
	}
}

// storeSink implements Sink over one Store: one SQLite transaction per
// source file, batches inserted as they arrive (bounded memory), commit
// at FileDone. A FileDone carrying a ReadError rolls the file's events
// back — a skipped source contributes bookkeeping, never partial data.
// Any error returned from a Sink method cancels the adapter's backfill
// per the contract.
//
// The sink enforces the bracketing contract (M2.1 hardening): every
// batch and FileDone must fall inside a declared FileStart/FileDone
// bracket, a path closes exactly once per run, and FileDone.Events must
// match the delivered batch total.
type storeSink struct {
	ctx       context.Context
	st        *store.Store
	harness   string
	machine   string
	version   int
	overrides *pricing.Overrides
	sum       *IngestSummary

	cur           *store.FileTx
	started       string          // path declared by FileStart; "" when no file is open
	curSourceID   string          // stable lineage ID of the open file (core.SourceID)
	curEmitted    int             // events inserted for the open file (counted into the summary only on commit)
	curEmptyModel int             // empty-model events for the open file (health, counted on commit)
	closed        map[string]bool // paths already finished this run
}

func (k *storeSink) FileStart(path string) error {
	if k.started != "" {
		return fmt.Errorf("adapter contract violation: FileStart for %s while %s is still open (missing FileDone)",
			path, k.started)
	}
	if k.closed[path] {
		return fmt.Errorf("adapter contract violation: duplicate path %s in one backfill run", path)
	}
	k.started = path
	k.curSourceID = core.SourceID(k.harness, path)
	return nil
}

func (k *storeSink) EmitBatch(path string, events []core.Event) error {
	if len(events) > BatchSize {
		return fmt.Errorf("adapter contract violation: batch of %d events exceeds BatchSize %d (%s)",
			len(events), BatchSize, path)
	}
	if k.started != path {
		if k.closed[path] {
			return fmt.Errorf("adapter contract violation: batch for %s after its FileDone", path)
		}
		return fmt.Errorf("adapter contract violation: batch for %s without FileStart (open file: %q)",
			path, k.started)
	}
	if k.cur == nil {
		tx, err := k.st.BeginFile(k.ctx)
		if err != nil {
			return err
		}
		k.cur = tx
	}
	// Derivation point (M3 Tasks 1+2): adapters emit model_family = model
	// verbatim and no cost fields; the ingest layer derives the family
	// through the versioned map and prices the event under the pinned
	// snapshot + user overrides. Unknown models pass through unchanged
	// and unpriceable events stay basis `unknown` with NULL cost.
	for i := range events {
		events[i].ModelFamily = modelmap.Family(events[i].Model)
		if err := pricing.Apply(&events[i], k.overrides); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := k.cur.InsertEvents(k.ctx, events, store.Provenance{
		AdapterVersion: k.version,
		SourceID:       k.curSourceID,
		MapVersion:     modelmap.Version(),
	}); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	k.curEmitted += len(events)
	for i := range events {
		if events[i].Model == "" {
			k.curEmptyModel++
		}
	}
	return nil
}

func (k *storeSink) FileDone(res FileResult) error {
	if k.started != res.Path {
		if k.closed[res.Path] {
			return fmt.Errorf("adapter contract violation: second FileDone for %s", res.Path)
		}
		return fmt.Errorf("adapter contract violation: FileDone for %s without FileStart (open file: %q)",
			res.Path, k.started)
	}
	// A skipped source (ReadError) may follow partial batches the ingest
	// layer discards; the Events count contract only holds for files read
	// to completion.
	if res.ReadError == "" && res.Events != k.curEmitted {
		return fmt.Errorf("adapter contract violation: %s declares %d events but delivered %d",
			res.Path, res.Events, k.curEmitted)
	}
	info := store.SourceInfo{
		Path: res.Path, Harness: k.harness, Machine: k.machine,
		MTime: res.MTime, Size: res.Size, LineCount: res.LineCount,
		ParseErrors: res.ParseErrors, ReadError: res.ReadError,
		IncompleteTail: res.IncompleteTail, AdapterVersion: k.version,
	}
	k.closed[res.Path] = true
	if res.ReadError != "" {
		// Skipped source: discard any events already inserted for the
		// file (the summary never counted them) so the next backfill
		// retries it whole, but persist the read error — the sources
		// table must reflect what is missing.
		if k.cur != nil {
			if err := k.cur.Rollback(); err != nil {
				return err
			}
			k.cur = nil
		}
		k.started, k.curEmitted, k.curEmptyModel = "", 0, 0
		if err := k.st.RecordSource(k.ctx, info); err != nil {
			return err
		}
		k.sum.Skipped++
		k.sum.Lines += res.LineCount
		return nil
	}
	if k.cur == nil {
		// Zero-event file: bookkeeping row only.
		k.started = ""
		if err := k.st.RecordSource(k.ctx, info); err != nil {
			return err
		}
	} else {
		stats, err := k.cur.Commit(k.ctx, info)
		emitted, emptyModel := k.curEmitted, k.curEmptyModel
		k.cur, k.started, k.curEmitted, k.curEmptyModel = nil, "", 0, 0
		if err != nil {
			return err
		}
		k.sum.Inserted += stats.Inserted
		k.sum.Replaced += stats.Replaced
		k.sum.Emitted += emitted
		k.sum.EmptyModel += emptyModel
		k.sum.mergeTouched(stats.TouchedDays)
	}
	k.sum.Files++
	k.sum.Lines += res.LineCount
	k.sum.ParseErrors += res.ParseErrors
	return nil
}

// IngestBackfill runs adapter backfill over the given sources and writes
// the events into st via a per-file-transaction sink. ov is the user's
// price-override file (nil = none; tests pass nil so they never read the
// real config). On error the open file transaction is rolled back; the
// summary reflects only completed files.
// IngestFiles is the watcher's incremental ingest (M4 Task 1): exactly
// the given files of src go through the same per-file-transaction sink
// as a backfill, so replacement semantics apply verbatim. Paths are
// deduplicated and sorted for a deterministic pass order. Like
// IngestBackfill, an error aborts the pass with the open file rolled
// back; completed files stay committed (idempotent — the next pass or a
// backfill reconciles).
func IngestFiles(ctx context.Context, st *store.Store, a Adapter, src Source, paths []string, ov *pricing.Overrides) (IngestSummary, error) {
	var sum IngestSummary
	sink := &storeSink{
		ctx: ctx, st: st, harness: src.Harness, machine: src.Machine,
		version: a.Version(), overrides: ov, sum: &sum,
		closed: map[string]bool{},
	}
	for _, p := range dedupSorted(paths) {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		err := a.BackfillFile(ctx, src, p, sink)
		if sink.cur != nil {
			_ = sink.cur.Rollback()
			sink.cur, sink.started, sink.curEmitted, sink.curEmptyModel = nil, "", 0, 0
		}
		if err == nil && sink.started != "" {
			err = fmt.Errorf("adapter contract violation: BackfillFile returned with %s still open (missing FileDone)",
				sink.started)
		}
		if err != nil {
			return sum, err
		}
	}
	return sum, nil
}

func dedupSorted(paths []string) []string {
	out := append([]string(nil), paths...)
	sort.Strings(out)
	return slices.Compact(out)
}

func IngestBackfill(ctx context.Context, st *store.Store, a Adapter, srcs []Source, ov *pricing.Overrides) (IngestSummary, error) {
	var sum IngestSummary
	for _, src := range srcs {
		sink := &storeSink{
			ctx: ctx, st: st, harness: src.Harness, machine: src.Machine,
			version: a.Version(), overrides: ov, sum: &sum,
			closed: map[string]bool{},
		}
		err := a.Backfill(ctx, src, sink)
		if sink.cur != nil {
			// A file was left open: either the backfill errored mid-file
			// or the adapter broke the bracketing contract.
			_ = sink.cur.Rollback()
		}
		if err == nil && sink.started != "" {
			err = fmt.Errorf("adapter contract violation: backfill returned with %s still open (missing FileDone)",
				sink.started)
		}
		if err != nil {
			return sum, err
		}
	}
	return sum, nil
}
