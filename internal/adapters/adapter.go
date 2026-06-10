// Package adapters defines the contract every harness adapter implements
// (CLAUDE.md "Adapter contract"). One package per harness lives below
// this one; M1 ships claudecode only.
package adapters

import (
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

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

// FileResult describes one fully parsed log file — the bookkeeping the
// ingest layer writes to the sources table. Malformed lines increment
// ParseErrors and are logged; they never abort the file (hard rule 8).
type FileResult struct {
	Path        string
	MTime       time.Time
	Size        int64
	LineCount   int
	ParseErrors int
}

// Event pairs a normalized usage event with the provenance of the file
// it came from. Backfill emits one file's events contiguously, each
// carrying the identical, final FileResult — the ingest layer flushes a
// transaction whenever File.Path changes.
type Event struct {
	core.Event
	File FileResult
}

// Adapter is the shared contract (versioned via each package's
// AdapterVersion constant, bumped on format-handling changes).
type Adapter interface {
	Name() string // "claude-code"
	// Detect finds log roots, respecting env overrides and probing both
	// legacy and XDG paths.
	Detect(env Probe) ([]Source, error)
	Backfill(src Source, emit func(Event)) error
	// Watch(...) — NOT until the milestone that asks for live tail.
}
