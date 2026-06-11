// Package pricing is the M3 pricing engine (PRD FR-9.1..9.5): a pinned,
// embedded snapshot of the LiteLLM price DB plus a user override file,
// resolved per event into integer micro-USD. No floats in storage or
// arithmetic: snapshot prices (USD per token, decimal) are converted
// EXACTLY to integer micro-USD per million tokens at load via big.Rat;
// no network calls at runtime or in tests (the snapshot refresh is an
// owner ceremony — see snapshot_meta.json).
//
// Costs are class `derived` (deterministic interpretation of exact
// token counts); the event's token accuracy class is untouched.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
)

//go:embed prices_snapshot.json
var snapshotJSON []byte

//go:embed snapshot_meta.json
var snapshotMetaJSON []byte

// Basis is the PRD §9.1 cost_basis enum, M3 form (the energy model is a
// later milestone, so `local` replaces `local_energy` for now).
type Basis string

const (
	BasisAPIPrice     Basis = "api_price"
	BasisPlanIncluded Basis = "plan_included" // not produced in M3 — owner ruling pending (hard stop 2)
	BasisLocal        Basis = "local"
	BasisFree         Basis = "free"
	BasisUnknown      Basis = "unknown"
)

// Rates are unit prices in integer micro-USD per MILLION tokens, one per
// price component. JSON tags define the per-event price_rates column
// format (FR-9.5: the unit prices used are stored with every priced
// event).
type Rates struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheWrite int64 `json:"cache_write"`
	CacheRead  int64 `json:"cache_read"`
}

// IsZero reports whether every component rate is zero.
func (r Rates) IsZero() bool {
	return r.Input == 0 && r.Output == 0 && r.CacheWrite == 0 && r.CacheRead == 0
}

// CostMicroUSD prices the four token components in integer arithmetic:
// the four products accumulate in micro-USD-per-Mtok × tokens, and ONE
// division at the end converts to micro-USD, rounding half up. tokens
// per component are int64; products stay far below int64 range.
func (r Rates) CostMicroUSD(input, output, cacheWrite, cacheRead int64) int64 {
	sum := input*r.Input + output*r.Output +
		cacheWrite*r.CacheWrite + cacheRead*r.CacheRead
	return (sum + 500_000) / 1_000_000
}

// Quote is one event's price resolution.
type Quote struct {
	Basis Basis
	// Rates is nil exactly when Basis is unknown.
	Rates *Rates
	// Snapshot records what priced the event: the snapshot_version, or
	// "override" when the user override file supplied the rates.
	Snapshot string
}

type snapshotMeta struct {
	SnapshotVersion string `json:"snapshot_version"`
}

// snapshotEntry holds the four LiteLLM price fields we consume; all
// other fields (tiered/flex/priority/batch variants, context windows)
// are deliberately ignored in M3 — standard-tier pricing, documented in
// the milestone report.
type snapshotEntry struct {
	Input      json.Number `json:"input_cost_per_token"`
	Output     json.Number `json:"output_cost_per_token"`
	CacheWrite json.Number `json:"cache_creation_input_token_cost"`
	CacheRead  json.Number `json:"cache_read_input_token_cost"`
}

var (
	loadOnce    sync.Once
	loadErr     error
	snapVersion string
	snapRates   map[string]Rates
)

// usdPerTokenToMicroPerMtok converts a decimal USD-per-token price to
// integer micro-USD per million tokens: value × 1e12, rounded half up
// to the nearest micro. The rounding absorbs float dirt in the source
// (the LiteLLM DB carries artifacts like 1.5000300000000002e-06); the
// resolution loss is bounded by 0.5 micro-USD per MILLION tokens —
// far below any real price granularity.
func usdPerTokenToMicroPerMtok(n json.Number) (int64, error) {
	if n == "" {
		return 0, nil
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return 0, fmt.Errorf("unparseable price %q", n)
	}
	if r.Sign() < 0 {
		return 0, fmt.Errorf("negative price %q", n)
	}
	r.Mul(r, new(big.Rat).SetInt64(1_000_000_000_000))
	// round(num/den) half up, exactly: (2*num + den) / (2*den)
	num := new(big.Int).Lsh(r.Num(), 1)
	num.Add(num, r.Denom())
	den := new(big.Int).Lsh(r.Denom(), 1)
	q := num.Div(num, den)
	if !q.IsInt64() {
		return 0, fmt.Errorf("price %q out of range", n)
	}
	return q.Int64(), nil
}

func load() {
	var meta snapshotMeta
	if loadErr = json.Unmarshal(snapshotMetaJSON, &meta); loadErr != nil {
		loadErr = fmt.Errorf("pricing: snapshot_meta.json: %w", loadErr)
		return
	}
	if meta.SnapshotVersion == "" {
		loadErr = fmt.Errorf("pricing: snapshot_meta.json has no snapshot_version")
		return
	}
	snapVersion = meta.SnapshotVersion

	var raw map[string]json.RawMessage
	if loadErr = json.Unmarshal(snapshotJSON, &raw); loadErr != nil {
		loadErr = fmt.Errorf("pricing: prices_snapshot.json: %w", loadErr)
		return
	}
	snapRates = make(map[string]Rates, len(raw))
	for key, body := range raw {
		if key == "sample_spec" { // LiteLLM's schema-documentation entry
			continue
		}
		var e snapshotEntry
		if err := json.Unmarshal(body, &e); err != nil {
			continue // entries with non-numeric price fields are not chat prices
		}
		var r Rates
		var err error
		if r.Input, err = usdPerTokenToMicroPerMtok(e.Input); err != nil {
			loadErr = fmt.Errorf("pricing: %s: %w", key, err)
			return
		}
		if r.Output, err = usdPerTokenToMicroPerMtok(e.Output); err != nil {
			loadErr = fmt.Errorf("pricing: %s: %w", key, err)
			return
		}
		if r.CacheWrite, err = usdPerTokenToMicroPerMtok(e.CacheWrite); err != nil {
			loadErr = fmt.Errorf("pricing: %s: %w", key, err)
			return
		}
		if r.CacheRead, err = usdPerTokenToMicroPerMtok(e.CacheRead); err != nil {
			loadErr = fmt.Errorf("pricing: %s: %w", key, err)
			return
		}
		if r.IsZero() {
			continue // no usable token prices (embedding/audio-only, etc.)
		}
		snapRates[key] = r
	}
}

// SnapshotVersion returns the embedded snapshot's version string.
func SnapshotVersion() (string, error) {
	loadOnce.Do(load)
	return snapVersion, loadErr
}

// isLocalProvider: vLLM serving (any owner recipe alias) prices as
// `local`, cost 0 in M3 — the energy model is a later milestone. Other
// local engines (ollama, llama.cpp) join when their adapters do.
func isLocalProvider(provider string) bool {
	return provider == "vllm" || strings.HasPrefix(provider, "vllm-")
}

// isFreeTier: gateway billing-tier suffixes that mean "this routing of
// the model is not billed". A billing rule on the model STRING, not a
// token heuristic.
func isFreeTier(model string) bool {
	return strings.HasSuffix(model, "-free") || strings.HasSuffix(model, ":free")
}

// Resolve picks the price for one event. Resolution order (documented in
// the milestone report):
//  1. user override file — raw model first, then family (owner's word
//     beats everything, including local-provider zeroing)
//  2. local provider (vllm*) → basis local, cost 0
//  3. free billing tier (-free/:free suffix) → basis free, cost 0
//  4. snapshot — model, provider/model, family, provider/family
//  5. unknown (cost stays NULL; never guessed)
func Resolve(provider, model, family string, ov *Overrides) (Quote, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return Quote{}, loadErr
	}
	if ov != nil {
		for _, key := range []string{model, family} {
			if r, ok := ov.rates[key]; ok {
				basis := BasisAPIPrice
				if r.IsZero() {
					basis = BasisFree
				}
				return Quote{Basis: basis, Rates: &r, Snapshot: "override"}, nil
			}
		}
	}
	if isLocalProvider(provider) {
		return Quote{Basis: BasisLocal, Rates: &Rates{}, Snapshot: snapVersion}, nil
	}
	if isFreeTier(model) {
		return Quote{Basis: BasisFree, Rates: &Rates{}, Snapshot: snapVersion}, nil
	}
	for _, key := range []string{model, provider + "/" + model, family, provider + "/" + family} {
		if r, ok := snapRates[key]; ok {
			return Quote{Basis: BasisAPIPrice, Rates: &r, Snapshot: snapVersion}, nil
		}
	}
	return Quote{Basis: BasisUnknown, Snapshot: snapVersion}, nil
}

// ReferenceRates resolves a user-chosen reference model for the
// cloud-equivalent "would have cost" path (FR-9.3) — snapshot only, no
// basis logic.
func ReferenceRates(model string) (Rates, bool, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return Rates{}, false, loadErr
	}
	r, ok := snapRates[model]
	return r, ok, nil
}

// PricedTokens maps an event's stored token columns to the four price
// components, per harness (the per-harness mapping table in the
// milestone report):
//
//   - claude-code: stored columns map 1:1 (input is uncached input;
//     cache writes priced at the 5m-TTL rate — the 1h-TTL split lives in
//     meta.cache_creation, a documented M3 approximation).
//   - codex: input already EXCLUDES cached tokens and output already
//     INCLUDES reasoning (format-notes); cache_write is always 0.
//   - opencode: output EXCLUDES reasoning (verified on fixture rows:
//     total = input+output+reasoning+cache), and reasoning bills as
//     output tokens — so reasoning joins the output component.
//
// Default for unknown harnesses: stored columns map 1:1, reasoning
// informational only.
func PricedTokens(harness string, input, output, cacheWrite, cacheRead int64, reasoning *int64) (in, out, cw, cr int64) {
	in, out, cw, cr = input, output, cacheWrite, cacheRead
	if harness == "opencode" && reasoning != nil {
		out += *reasoning
	}
	return in, out, cw, cr
}
