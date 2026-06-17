package adapters

// RecomputeProvenance implements `tatitok recompute --provenance` (M3
// Task 0, AS-4): re-read source files through the CURRENT adapters
// and fill the NULL provenance columns (adapter_version, source_id) of
// already-stored events. It is explicit, logged, and never a side
// effect of ingest. Nothing is ingested and no payload is ever altered:
// a re-parsed event stamps a stored row only when its deterministic ID
// matches AND the stored payload verifies identical to the re-parse;
// differing rows are reported as mismatches and left alone.
//
// Verification mirrors the ingest fold exactly: an ID can occur on
// several source lines with growing payloads (claude-code streams the
// same message id across consecutive lines; mutable stores finalize
// rows), and ingest resolves that last-wins via the replacement path.
// So each occurrence is verified read-only and only the LAST occurrence
// of an ID across the whole run decides — stamps are written at the end,
// once the final verdict per ID is known.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// maxMismatchDetails caps the per-event detail list in the summary; the
// full set is still counted and logged.
const maxMismatchDetails = 20

// RecomputeSummary aggregates one recompute run.
type RecomputeSummary struct {
	Files            int // files read to completion
	FilesNotIngested int // visited files with no sources row — never ingested; their events cannot be source-linked
	FilesSkipped     int // read errors: their occurrences are dropped, retried next run
	Lines            int
	Stamped          int // events whose NULL provenance was filled (payload verified identical first)
	StampedNoSource  int // of Stamped: adapter_version only — the file has no sources row to link to
	Mismatched       int // stored payload differs from the final re-parse — reported, NEVER altered or stamped
	SourceMachines   int // sources rows whose NULL machine was filled
	// Mismatches holds the first maxMismatchDetails "id (path)" details.
	Mismatches []string
}

// occState is the latest verification verdict for one needy ID — the
// last occurrence wins, like the ingest replacement path.
type occState struct {
	matched bool
	path    string
}

// recomputeSink consumes one source's backfill without ingesting it:
// read-only verification per occurrence into the run-level state.
type recomputeSink struct {
	ctx      context.Context
	st       *store.Store
	harness  string
	machine  string
	needy    map[string]string
	verifier *store.Verifier
	sum      *RecomputeSummary

	// run-level (shared across this adapter's sources via the run struct)
	state       map[string]occState
	srcIDByPath map[string]string // "" = file unknown to the sources table

	path string // open file; "" between files
}

func (k *recomputeSink) FileStart(path string) error {
	if k.path != "" {
		return fmt.Errorf("adapter contract violation: FileStart for %s while %s is still open", path, k.path)
	}
	k.path = path
	srcID, machineNull, found, err := k.st.SourceIDForPath(k.ctx, path)
	if err != nil {
		return err
	}
	if !found {
		// Never-ingested file: its events are not in the DB, so there is
		// nothing to stamp — and no sources row to link to.
		k.srcIDByPath[path] = ""
		k.sum.FilesNotIngested++
		return nil
	}
	k.srcIDByPath[path] = srcID
	if machineNull && k.machine != "" {
		stamped, err := k.st.StampSourceMachine(k.ctx, path, k.machine)
		if err != nil {
			return err
		}
		if stamped {
			k.sum.SourceMachines++
		}
	}
	return nil
}

func (k *recomputeSink) EmitBatch(path string, events []core.Event) error {
	if k.path != path {
		return fmt.Errorf("adapter contract violation: batch for %s (open file: %q)", path, k.path)
	}
	for i := range events {
		e := &events[i]
		if _, ok := k.needy[e.ID]; !ok {
			continue // already provenanced, or never ingested — not recompute's business
		}
		outcome, err := k.verifier.Verify(k.ctx, e)
		if err != nil {
			return err
		}
		if outcome == store.VerifyMissing {
			// In the needy set but gone from the DB mid-run — should not
			// happen (nothing deletes events); surfaced, not fatal.
			slog.Warn("recompute: event vanished from the database mid-run",
				"id", e.ID, "harness", k.harness, "file", path)
			continue
		}
		k.state[e.ID] = occState{matched: outcome == store.VerifyIdentical, path: path}
	}
	return nil
}

func (k *recomputeSink) FileDone(res FileResult) error {
	if k.path != res.Path {
		return fmt.Errorf("adapter contract violation: FileDone for %s (open file: %q)", res.Path, k.path)
	}
	k.path = ""
	k.sum.Lines += res.LineCount
	if res.ReadError != "" {
		// Partially read file: its last occurrence may never have been
		// reached, so any verdict it produced is unreliable — drop it (the
		// ids stay in the work set and are reported as leftovers).
		for id, st := range k.state {
			if st.path == res.Path {
				delete(k.state, id)
			}
		}
		k.sum.FilesSkipped++
		slog.Warn("recompute: source skipped (unreadable) — its events keep NULL provenance",
			"file", res.Path, "error", res.ReadError)
		return nil
	}
	k.sum.Files++
	return nil
}

// RecomputeProvenance re-reads every file under the adapter's sources,
// verifies stored events against the re-parse, and fills NULL provenance
// on the verified ones. needy is the shared work set from
// Store.NullProvenanceIDs; stamped ids are removed, so the caller can
// report leftovers after all adapters ran.
func RecomputeProvenance(ctx context.Context, st *store.Store, a Adapter, srcs []Source, needy map[string]string, sum *RecomputeSummary) error {
	verifier, err := st.NewVerifier(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = verifier.Close() }()

	state := map[string]occState{}
	srcIDByPath := map[string]string{}
	for _, src := range srcs {
		sink := &recomputeSink{
			ctx: ctx, st: st, harness: src.Harness, machine: src.Machine,
			needy: needy, verifier: verifier, sum: sum,
			state: state, srcIDByPath: srcIDByPath,
		}
		if err := a.Backfill(ctx, src, sink); err != nil {
			return err
		}
		if sink.path != "" {
			return fmt.Errorf("adapter contract violation: backfill returned with %s still open", sink.path)
		}
	}

	// Final verdicts: stamp the matched, report the rest.
	var stamps []store.ProvenanceStamp
	noSource := 0
	for id, occ := range state {
		if !occ.matched {
			sum.Mismatched++
			if len(sum.Mismatches) < maxMismatchDetails {
				sum.Mismatches = append(sum.Mismatches, fmt.Sprintf("%s (%s)", id, occ.path))
			}
			slog.Warn("recompute: stored payload differs from current re-parse — not altered, not stamped",
				"id", id, "harness", a.Name(), "file", occ.path)
			continue
		}
		srcID := srcIDByPath[occ.path]
		if srcID == "" {
			noSource++
		}
		stamps = append(stamps, store.ProvenanceStamp{ID: id, SourceID: srcID})
	}
	if err := st.StampProvenance(ctx, a.Version(), stamps); err != nil {
		return err
	}
	for _, s := range stamps {
		delete(needy, s.ID)
	}
	sum.Stamped += len(stamps)
	sum.StampedNoSource += noSource
	return nil
}
