// Package limits is tatitok's display-only store for provider-REPORTED usage
// limits (M9). It is deliberately FENCED: it imports only the standard library
// and is imported by nothing in the verified data path (event store, pricing,
// rollups, parity). The numbers it holds are read off the providers' own pages
// by the companion browser extension and POSTed here over loopback; they are
// shown on the dashboard and NEVER mixed with tatitok's counted token/cost
// data — they never enter the store, pricing, rollups or the parity pipeline.
//
// Storage is in-memory and last-write-wins: the latest POSTed snapshot replaces
// the previous one. It is intentionally NOT persisted — empty after a restart
// until the extension's next poll. That is correct for a freshness-stamped live
// mirror and keeps the fence trivial (no schema, no migration, no disk).
package limits

import (
	"fmt"
	"math"
	"sync"
)

// Window is one reported usage window for a provider — e.g. Claude's
// "All models" / "Sonnet" weekly windows, or Codex's "5h" / "Weekly". The
// shape mirrors the companion extension's normalized output verbatim.
type Window struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"usedPercent"` // percent; finite and >= 0 (may exceed 100 if the provider reports over-cap)
	ResetAt     int64   `json:"resetAt"`     // epoch milliseconds (0 = unknown)
}

// Provider is the latest reported snapshot for one provider key.
type Provider struct {
	FetchedAt int64    `json:"fetchedAt"` // epoch ms the extension polled this provider
	Windows   []Window `json:"windows"`
}

// Snapshot is the whole normalized payload the extension POSTs: a provider key
// ("claude", "codex") -> its latest reported limits.
type Snapshot map[string]Provider

// Validate rejects a malformed snapshot cleanly (the handler turns a non-nil
// error into a 400). It guards the display invariants the dashboard relies on:
// a present provider key, finite and non-negative percentages, and non-negative
// epoch timestamps. A usedPercent ABOVE 100 is accepted and stored verbatim — a
// provider may report over-cap, and the stored number stays truthful (the
// frontend clamps the rendered bar to 100%). It deliberately does NOT constrain
// WHICH providers or window labels may appear — new providers/windows pass
// through unchanged (forward-compatible with the extension evolving).
func (s Snapshot) Validate() error {
	for key, p := range s {
		if key == "" {
			return fmt.Errorf("limits: empty provider key")
		}
		if p.FetchedAt < 0 {
			return fmt.Errorf("limits: provider %q has negative fetchedAt %d", key, p.FetchedAt)
		}
		for i, w := range p.Windows {
			if w.Label == "" {
				return fmt.Errorf("limits: provider %q window %d has empty label", key, i)
			}
			if math.IsNaN(w.UsedPercent) || math.IsInf(w.UsedPercent, 0) {
				return fmt.Errorf("limits: provider %q window %q usedPercent is not a finite number", key, w.Label)
			}
			if w.UsedPercent < 0 {
				return fmt.Errorf("limits: provider %q window %q has negative usedPercent %v", key, w.Label, w.UsedPercent)
			}
			if w.ResetAt < 0 {
				return fmt.Errorf("limits: provider %q window %q has negative resetAt %d", key, w.Label, w.ResetAt)
			}
		}
	}
	return nil
}

// Store holds the latest reported snapshot, safe for one concurrent POST
// (writer) and many GET (readers). Last-write-wins: Set replaces the whole
// snapshot, never merges.
type Store struct {
	mu   sync.RWMutex
	snap Snapshot // nil until the first Set
}

// NewStore returns an empty store — no snapshot until the first POST.
func NewStore() *Store { return &Store{} }

// Set replaces the stored snapshot (last-write-wins). The Store takes ownership
// of snap; callers must not mutate it afterwards.
func (s *Store) Set(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
}

// Get returns the latest snapshot, or nil if none has been stored yet. The
// returned map is read-only — Set always installs a fresh map, so the returned
// reference is never mutated in place.
func (s *Store) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}
