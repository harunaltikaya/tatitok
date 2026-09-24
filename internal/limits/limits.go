// Package limits is tatitok's display-only store for provider-REPORTED usage
// limits (M9). It is deliberately FENCED: it imports only the standard library
// and is imported by nothing in the verified data path (event store, pricing,
// rollups, parity). The numbers it holds are read off the providers' own pages
// by the companion browser extension and POSTed here over loopback; they are
// shown on the dashboard and NEVER mixed with tatitok's counted token/cost
// data — they never enter the store, pricing, rollups or the parity pipeline.
//
// Storage is in-memory and merges PER PROVIDER KEY: a POST carrying "agy"
// updates only "agy" and leaves "claude"/"codex" as they were (each feeder —
// the browser extension, the agy statusLine hook — posts only the providers it
// knows). Within one key it is last-write-wins. It is intentionally NOT persisted — empty after a restart
// until the extension's next poll. That is correct for a freshness-stamped live
// mirror and keeps the fence trivial (no schema, no migration, no disk).
package limits

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
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

// maxText bounds a provider key or window label, in characters (runes). Real
// names are short ("5h", "Fable 7d", "iguana_necktie (cloud credit)"); the
// consumers print them on a desktop notification or a terminal status line.
const maxText = 64

// checkText says why s is not a displayable name: blank after trimming, longer
// than maxText characters, or holding a rune unicode.IsPrint rejects (C0/C1
// controls, format characters, line/paragraph separators; the ASCII space is
// printable). nil when s is fine.
func checkText(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("is empty")
	}
	if n := utf8.RuneCountInString(s); n > maxText {
		return fmt.Errorf("is %d characters (max %d)", n, maxText)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("holds non-printable character %U", r)
		}
	}
	return nil
}

// Validate rejects a malformed snapshot cleanly (the handler turns a non-nil
// error into a 400). It guards the display invariants the dashboard relies on:
// provider keys and window labels that are non-blank, at most maxText
// characters and printable (checkText), finite and non-negative percentages,
// and non-negative epoch timestamps. A usedPercent ABOVE 100 is accepted and
// stored verbatim — a provider may report over-cap, and the stored number stays
// truthful (the frontend clamps the rendered bar to 100%). It deliberately does
// NOT constrain WHICH providers or window labels may appear — new
// providers/windows pass through unchanged (forward-compatible with the
// extension evolving).
func (s Snapshot) Validate() error {
	for key, p := range s {
		if err := checkText(key); err != nil {
			return fmt.Errorf("limits: provider key %q %v", key, err)
		}
		if p.FetchedAt < 0 {
			return fmt.Errorf("limits: provider %q has negative fetchedAt %d", key, p.FetchedAt)
		}
		for i, w := range p.Windows {
			if err := checkText(w.Label); err != nil {
				return fmt.Errorf("limits: provider %q window %d label %q %v", key, i, w.Label, err)
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

// Store holds the latest reported snapshot, safe for concurrent POSTs
// (writers) and many GET (readers). Set merges per provider key; a key is
// last-write-wins, keys absent from a write are kept.
type Store struct {
	mu   sync.RWMutex
	snap Snapshot // nil until the first Set
}

// NewStore returns an empty store — no snapshot until the first POST.
func NewStore() *Store { return &Store{} }

// Set merges snap into the stored snapshot per provider key: every key in snap
// replaces the stored entry for that key; keys not in snap are kept. The
// stored map is never mutated in place — a fresh map is installed on every
// Set, so references handed out by Get stay stable.
func (s *Store) Set(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := make(Snapshot, len(s.snap)+len(snap))
	for k, p := range s.snap {
		merged[k] = p
	}
	for k, p := range snap {
		merged[k] = p
	}
	s.snap = merged
}

// Get returns the latest snapshot, or nil if none has been stored yet. The
// returned map is read-only — Set always installs a fresh map, so the returned
// reference is never mutated in place.
func (s *Store) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}
