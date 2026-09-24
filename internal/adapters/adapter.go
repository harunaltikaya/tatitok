// Package adapters defines the contract every harness adapter implements
// (the Adapter contract, redesigned in M2 Task 0). One package per
// harness lives below this one.
//
// Contract v2 — bounded, error-aware batch emission:
//
//   - Backfill pushes events through a Sink in size-bounded batches (at
//     most BatchSize events per EmitBatch call), so memory stays bounded
//     regardless of file size.
//   - Each source file is bracketed by exactly ONE FileStart and exactly
//     ONE FileDone carrying its completion result; every EmitBatch for a
//     file falls between the two, files never interleave, and a path
//     appears at most once per backfill run. Zero-event and unreadable
//     (skipped) files are bracketed too — the sources bookkeeping must
//     see every file. FileDone.Events must equal the file's delivered
//     batch total. The ingest layer rejects any deviation as a contract
//     violation (M2.1 hardening).
//   - Both Sink methods return errors. A non-nil return (e.g. a DB
//     failure in the ingest layer) CANCELS the backfill: the adapter must
//     stop all remaining work and return that error unchanged — no
//     parse-and-discard. Adapters also honor ctx cancellation between
//     batches and files.
//   - A file whose FileDone carries a ReadError is a SKIPPED source: the
//     ingest layer discards any events already emitted for it (its
//     transaction rolls back) so the next backfill retries the whole file.
package adapters

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// BatchSize is the maximum number of events an adapter may pass to a
// single EmitBatch call.
const BatchSize = 500

// Probe carries the environment an adapter inspects during Detect.
// Tests inject fakes; the CLI fills it from the real process environment.
type Probe struct {
	// Getenv looks up environment overrides (e.g. CLAUDE_CONFIG_DIR).
	Getenv func(key string) string
	// HomeDir is the user's home directory.
	HomeDir string
	// Machine is the collecting machine's label (hostname or alias);
	// copied onto every Source and from there onto every Event.
	Machine string
}

// Source is one detected log root for a harness.
type Source struct {
	Harness string
	// Root is the directory holding the harness's session logs (for
	// claude-code: a projects/ directory).
	Root string
	// Machine label propagated from the Probe.
	Machine string
}

// FileResult is one source file's completion result — the per-file health
// record the ingest layer writes to the sources table. Malformed lines
// increment ParseErrors and are logged; they never abort the file (hard
// rule 8).
type FileResult struct {
	Path      string
	MTime     time.Time
	Size      int64
	LineCount int
	// Events is the number of billable events emitted for this file
	// (the sum of its EmitBatch lengths).
	Events      int
	ParseErrors int
	// ReadError, when non-empty, marks a SKIPPED source: the file or
	// directory at Path could not be (fully) read. Any events already
	// emitted for the file are discarded by the ingest layer so the next
	// backfill retries the whole file. Distinct from ParseErrors, which
	// are contained per line.
	ReadError string
	// IncompleteTail: the file's final line is unterminated (no trailing
	// newline) and does not parse — almost certainly a write in progress,
	// so it is NOT a parse error. The next backfill of the file clears it.
	IncompleteTail bool
}

// Sink receives a backfill's output. Implementations are provided by the
// ingest layer (and by tests); see the package comment for the call
// sequence and cancellation semantics.
type Sink interface {
	// FileStart declares the file at path before any of its batches —
	// exactly one per file per run, zero-event and skipped files
	// included. A non-nil error cancels the backfill.
	FileStart(path string) error
	// EmitBatch delivers up to BatchSize billable events parsed from the
	// file at path. A non-nil error cancels the backfill.
	EmitBatch(path string, events []core.Event) error
	// FileDone reports a file's completion result after all its batches.
	// A non-nil error cancels the backfill.
	FileDone(res FileResult) error
}

// WatchSpec describes how the hub watches one Source for live changes
// (M4 Task 1 — the live-tail milestone the contract reserved Watch for).
// The hub owns the machinery (fsnotify, polling, debounce, scheduling);
// the adapter owns the layout knowledge: which paths matter and what to
// ingest when one of them changes.
type WatchSpec struct {
	// PollOnly marks fsnotify unsuitable for this source. The opencode
	// store is a live SQLite database written by another process —
	// polling is the primary mechanism by design (verdict and the WAL
	// empirical basis verified on the live store).
	PollOnly bool
	// PollPaths lists the exact files the poller stats for a PollOnly
	// source, so no tree walk is needed (opencode: the database and its
	// -wal — in WAL mode the main file's mtime only moves at
	// checkpoint). Empty means the poller walks Root applying Match —
	// the automatic fallback mode for notify sources.
	PollPaths []string
	// Match maps a changed filesystem path to the path BackfillFile
	// should ingest, or "" when the change is irrelevant. claude-code
	// and codex map a session/rollout .jsonl to itself; opencode maps
	// the database and its -wal to the database.
	Match func(path string) string
}

// SkippedDir is a directory ListFiles could not read.
type SkippedDir struct {
	Path string
	Err  error
}

// ListFiles is the file discovery the hub watcher and backfill share, so
// both list the same files: every file under root, at any depth
// (claude-code's <folder>/<session>/subagents/ included), that match
// accepts, in lexical order. A directory below root that cannot be read
// is returned in skipped; an unreadable root is an error.
func ListFiles(root string, match func(path string) string) (files []string, skipped []SkippedDir, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			skipped = append(skipped, SkippedDir{Path: path, Err: err})
			return nil
		}
		if !d.IsDir() && match(path) != "" {
			files = append(files, path)
		}
		return nil
	})
	return files, skipped, err
}

// Adapter is the shared contract.
type Adapter interface {
	Name() string // "claude-code"
	// Version is the adapter's format-handling version (the package's
	// AdapterVersion constant, bumped on format-handling changes). It is
	// persisted per event and per source file as provenance.
	Version() int
	// Detect finds log roots, respecting env overrides and probing both
	// legacy and XDG paths.
	Detect(env Probe) ([]Source, error)
	// Backfill parses every log file under src and pushes the normalized
	// events through sink per the contract v2 semantics above.
	Backfill(ctx context.Context, src Source, sink Sink) error
	// WatchSpec describes how the hub watches src (M4 Task 1).
	WatchSpec(src Source) WatchSpec
	// BackfillFile ingests exactly ONE file of src through sink, with
	// the same bracketing, error containment and replacement semantics
	// Backfill applies to that file. The hub watcher calls it per
	// changed file: the whole file is re-read — deliberately no offset
	// tracking, so a pass is idempotent by construction — and re-emitted
	// rows hit the deterministic-ID replacement path, last occurrence
	// wins (M4 Task 1).
	BackfillFile(ctx context.Context, src Source, path string, sink Sink) error
}
