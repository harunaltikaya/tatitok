// Package core defines the unified usage-event model shared by every
// adapter and the store: the Event struct (the M1 subset), the accuracy
// classes, and the deterministic event ID.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Accuracy classifies how a token figure was obtained.
// It is assigned per event at ingest by the adapter and is immutable (AS-1).
type Accuracy string

const (
	// AccuracyExact: token figures reported by the serving provider, read
	// verbatim from an authoritative record. We perform no counting.
	AccuracyExact Accuracy = "exact"
	// AccuracyDerived: computed deterministically from exact data via a
	// documented rule (interpretation, but no tokenization).
	AccuracyDerived Accuracy = "derived"
	// AccuracyEstimated: reconstructed via local tokenization + calibrated
	// overhead constants; always carries a confidence percentage.
	AccuracyEstimated Accuracy = "estimated"
)

// Valid reports whether a is one of the three defined classes.
func (a Accuracy) Valid() bool {
	switch a {
	case AccuracyExact, AccuracyDerived, AccuracyEstimated:
		return true
	}
	return false
}

// SourceKind values. M1 only ingests harness logs.
const (
	SourceKindHarnessLog = "harness_log"
)

// Event is one LLM interaction (message/request), normalized across
// sources. The subset needed for M1; later milestones add cost,
// confidence and latency fields.
type Event struct {
	// ID is the deterministic idempotency key — see EventID / FallbackID.
	ID string `json:"id"`
	// TS is the event time (message time, not ingest time), always UTC.
	TS time.Time `json:"ts"`
	// Machine is the hostname/alias of the collecting machine.
	Machine    string `json:"machine"`
	SourceKind string `json:"source_kind"`
	Harness    string `json:"harness,omitempty"`
	Provider   string `json:"provider"`
	// Model is the raw model id exactly as reported by the source.
	Model string `json:"model"`
	// ModelFamily mirrors Model until the mapping-table milestone —
	// unknown models pass through raw, never guessed.
	ModelFamily string `json:"model_family"`
	Project     string `json:"project,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`

	TokensInput      int64 `json:"tokens_input"`
	TokensOutput     int64 `json:"tokens_output"`
	TokensCacheWrite int64 `json:"tokens_cache_write"`
	TokensCacheRead  int64 `json:"tokens_cache_read"`
	// TokensReasoning is nil when the source does not report
	// thinking/reasoning tokens separately (Claude Code does not).
	TokensReasoning *int64 `json:"tokens_reasoning,omitempty"`

	// Cost fields (M3 Task 2) — derived at ingest by the pricing
	// engine, never by adapters (adapter-emitted events leave them empty,
	// so adapter goldens are cost-free). Integer micro-USD; CostUSDMicro
	// is nil when the event could not be priced (basis `unknown`).
	CostUSDMicro *int64 `json:"cost_usd_micro,omitempty"`
	// CostBasis: api_price | plan_included | local | free | unknown.
	CostBasis string `json:"cost_basis,omitempty"`
	// PriceSnapshot is the price-snapshot version (or "override") that
	// priced this event (FR-9.5).
	PriceSnapshot string `json:"price_snapshot,omitempty"`
	// PriceRates is the JSON-encoded unit rates used, integer micro-USD
	// per million tokens per component (FR-9.5); for free-basis events it
	// additionally carries the API-equivalent rates and their derivation
	// ("model" or "family").
	PriceRates json.RawMessage `json:"price_rates,omitempty"`
	// CostAPIEquivMicro is the computed API-equivalent value of a
	// free-basis event (owner ruling, mirroring FR-9.3) — what the same
	// tokens would have cost at the model's API price. Nil unless basis
	// is free and the model (or its family) resolves in the snapshot.
	CostAPIEquivMicro *int64 `json:"cost_api_equiv_micro,omitempty"`

	Accuracy Accuracy `json:"accuracy"`
	// Meta holds source-specific extras (cwd, branch, client version,
	// per-TTL cache detail, …).
	Meta map[string]any `json:"meta,omitempty"`
	// Raw is the SANITIZED source record: original structure with every
	// content field replaced by <stripped len=N sha256=…> placeholders.
	// Prompt or response text must never end up here.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Validate checks the invariants every adapter must uphold before an
// event reaches the store. The store calls it on every insert, so it is
// the enforcement boundary for hard rule 6: an event whose Raw still
// carries content-bearing text (non-placeholder strings under content
// keys, or over-long free text) must never be persisted.
//
// Identity fields (id, source kind, harness) and non-negative token
// counters are required; Model and Provider MAY be empty — codex
// token_count records before the first turn_context genuinely carry no
// model — and the ingest layer counts such events as a health signal
// instead of rejecting real usage.
func (e *Event) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("event has empty id")
	}
	if e.SourceKind == "" {
		return fmt.Errorf("event %s has empty source_kind", e.ID)
	}
	if e.Harness == "" {
		return fmt.Errorf("event %s has empty harness", e.ID)
	}
	if e.TS.IsZero() {
		return fmt.Errorf("event %s has zero timestamp", e.ID)
	}
	if e.TS.Location() != time.UTC {
		return fmt.Errorf("event %s timestamp not UTC", e.ID)
	}
	if !e.Accuracy.Valid() {
		return fmt.Errorf("event %s has invalid accuracy %q", e.ID, e.Accuracy)
	}
	if e.TokensInput < 0 || e.TokensOutput < 0 ||
		e.TokensCacheWrite < 0 || e.TokensCacheRead < 0 {
		return fmt.Errorf("event %s has negative token counters (%d/%d/%d/%d)",
			e.ID, e.TokensInput, e.TokensOutput,
			e.TokensCacheWrite, e.TokensCacheRead)
	}
	if e.TokensReasoning != nil && *e.TokensReasoning < 0 {
		return fmt.Errorf("event %s has negative reasoning tokens (%d)",
			e.ID, *e.TokensReasoning)
	}
	if e.CostUSDMicro != nil && *e.CostUSDMicro < 0 {
		return fmt.Errorf("event %s has negative cost (%d micro-USD)", e.ID, *e.CostUSDMicro)
	}
	switch e.CostBasis {
	case "", "api_price", "plan_included", "local", "free", "unknown":
	default:
		return fmt.Errorf("event %s has invalid cost_basis %q", e.ID, e.CostBasis)
	}
	if len(e.Raw) > 0 {
		findings, err := CheckRawSanitized(e.Raw)
		if err != nil {
			return fmt.Errorf("event %s raw: %w", e.ID, err)
		}
		if len(findings) > 0 {
			return fmt.Errorf("event %s raw violates sanitizer invariants: %s",
				e.ID, strings.Join(findings, "; "))
		}
	}
	return nil
}
