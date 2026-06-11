package adapters

import (
	"context"
	"fmt"

	"github.com/harunaltikaya/tatitok/internal/core"
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
	Replaced    int
	ParseErrors int
	// Skipped counts sources (files or project dirs) the adapter could
	// not read — recorded in the sources table with their read error,
	// reported distinctly from parse errors. Skipped > 0 means the run
	// completed but the DB does not reflect the full log history.
	Skipped int
}

// storeSink implements Sink over one Store: one SQLite transaction per
// source file, batches inserted as they arrive (bounded memory), commit
// at FileDone. A FileDone carrying a ReadError rolls the file's events
// back — a skipped source contributes bookkeeping, never partial data.
// Any error returned from a Sink method cancels the adapter's backfill
// per the contract.
type storeSink struct {
	ctx     context.Context
	st      *store.Store
	harness string
	version int
	sum     *IngestSummary

	cur        *store.FileTx
	curPath    string
	curEmitted int // events inserted for the open file (counted into the summary only on commit)
}

func (k *storeSink) EmitBatch(path string, events []core.Event) error {
	if len(events) > BatchSize {
		return fmt.Errorf("adapter contract violation: batch of %d events exceeds BatchSize %d (%s)",
			len(events), BatchSize, path)
	}
	if k.cur != nil && k.curPath != path {
		return fmt.Errorf("adapter contract violation: batch for %s while %s is still open (missing FileDone)",
			path, k.curPath)
	}
	if k.cur == nil {
		tx, err := k.st.BeginFile(k.ctx)
		if err != nil {
			return err
		}
		k.cur, k.curPath = tx, path
	}
	if err := k.cur.InsertEvents(k.ctx, events, k.version); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	k.curEmitted += len(events)
	return nil
}

func (k *storeSink) FileDone(res FileResult) error {
	if k.cur != nil && k.curPath != res.Path {
		return fmt.Errorf("adapter contract violation: FileDone for %s while %s is still open",
			res.Path, k.curPath)
	}
	info := store.SourceInfo{
		Path: res.Path, Harness: k.harness, MTime: res.MTime,
		Size: res.Size, LineCount: res.LineCount,
		ParseErrors: res.ParseErrors, ReadError: res.ReadError,
		IncompleteTail: res.IncompleteTail, AdapterVersion: k.version,
	}
	if res.ReadError != "" {
		// Skipped source: discard any events already inserted for the
		// file (the summary never counted them) so the next backfill
		// retries it whole, but persist the read error — the sources
		// table must reflect what is missing.
		if k.cur != nil {
			if err := k.cur.Rollback(); err != nil {
				return err
			}
			k.cur, k.curPath, k.curEmitted = nil, "", 0
		}
		if err := k.st.RecordSource(k.ctx, info); err != nil {
			return err
		}
		k.sum.Skipped++
		k.sum.Lines += res.LineCount
		return nil
	}
	if k.cur == nil {
		// Zero-event file: bookkeeping row only.
		if err := k.st.RecordSource(k.ctx, info); err != nil {
			return err
		}
	} else {
		stats, err := k.cur.Commit(k.ctx, info)
		emitted := k.curEmitted
		k.cur, k.curPath, k.curEmitted = nil, "", 0
		if err != nil {
			return err
		}
		k.sum.Inserted += stats.Inserted
		k.sum.Replaced += stats.Replaced
		k.sum.Emitted += emitted
	}
	k.sum.Files++
	k.sum.Lines += res.LineCount
	k.sum.ParseErrors += res.ParseErrors
	return nil
}

// IngestBackfill runs adapter backfill over the given sources and writes
// the events into st via a per-file-transaction sink. On error the open
// file transaction is rolled back; the summary reflects only completed
// files.
func IngestBackfill(ctx context.Context, st *store.Store, a Adapter, srcs []Source) (IngestSummary, error) {
	var sum IngestSummary
	for _, src := range srcs {
		sink := &storeSink{
			ctx: ctx, st: st, harness: src.Harness,
			version: a.Version(), sum: &sum,
		}
		err := a.Backfill(ctx, src, sink)
		if sink.cur != nil {
			// A file was left open: either the backfill errored mid-file
			// or the adapter broke the one-FileDone-per-file contract.
			_ = sink.cur.Rollback()
			if err == nil {
				err = fmt.Errorf("adapter contract violation: backfill returned with %s still open (missing FileDone)",
					sink.curPath)
			}
		}
		if err != nil {
			return sum, err
		}
	}
	return sum, nil
}
