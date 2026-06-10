package adapters

import (
	"context"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// IngestSummary aggregates one backfill run.
type IngestSummary struct {
	Files       int
	Lines       int
	Emitted     int // billable events emitted by the adapter
	Inserted    int // new rows (duplicates collapse via INSERT OR IGNORE)
	ParseErrors int
	// Skipped counts sources (files or project dirs) the adapter could
	// not read — recorded in the sources table with their read error,
	// reported distinctly from parse errors. Skipped > 0 means the run
	// completed but the DB does not reflect the full log history.
	Skipped int
}

// IngestBackfill runs adapter backfill over the given sources and writes
// the events into st, one transaction per source file (Backfill emits a
// file's events contiguously; an event with an empty ID is a zero-event
// file marker carrying only bookkeeping).
func IngestBackfill(ctx context.Context, st *store.Store, a Adapter, srcs []Source) (IngestSummary, error) {
	var sum IngestSummary
	for _, src := range srcs {
		var batch []core.Event
		var cur FileResult
		var emitErr error
		flush := func() {
			if cur.Path == "" || emitErr != nil {
				return
			}
			n, err := st.InsertBatch(ctx, batch, store.SourceInfo{
				Path: cur.Path, Harness: src.Harness, MTime: cur.MTime,
				Size: cur.Size, LineCount: cur.LineCount,
				ParseErrors: cur.ParseErrors, ReadError: cur.ReadError,
				IncompleteTail: cur.IncompleteTail,
			})
			if err != nil {
				emitErr = err
				return
			}
			if cur.ReadError != "" {
				sum.Skipped++
			} else {
				sum.Files++
			}
			sum.Lines += cur.LineCount
			sum.Inserted += n
			sum.ParseErrors += cur.ParseErrors
			batch = nil
		}
		err := a.Backfill(src, func(e Event) {
			if emitErr != nil {
				return
			}
			if e.File.Path != cur.Path {
				flush()
				cur = e.File
				batch = nil
			}
			if e.ID != "" {
				batch = append(batch, e.Event)
				sum.Emitted++
			}
		})
		flush()
		if emitErr != nil {
			return sum, emitErr
		}
		if err != nil {
			return sum, err
		}
	}
	return sum, nil
}
