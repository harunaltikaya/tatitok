// Package adapters defines the contract every harness adapter implements
// (CLAUDE.md "Adapter contract", redesigned in M2 Task 0). One package per
// harness lives below this one.
//
// Contract v2 — bounded, error-aware batch emission:
//
//   - Backfill pushes events through a Sink in size-bounded batches (at
//     most BatchSize events per EmitBatch call), so memory stays bounded
//     regardless of file size.
//   - Each source file finishes with exactly ONE FileDone carrying its
//     completion result; every EmitBatch for a file precedes its FileDone,
//     and files never interleave. A file with no billable events still
//     gets a FileDone — the sources bookkeeping must see every file.
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
	// EmitBatch delivers up to BatchSize billable events parsed from the
	// file at path. A non-nil error cancels the backfill.
	EmitBatch(path string, events []core.Event) error
	// FileDone reports a file's completion result after all its batches.
	// A non-nil error cancels the backfill.
	FileDone(res FileResult) error
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
	// Watch(...) — NOT until the milestone that asks for live tail.
}
