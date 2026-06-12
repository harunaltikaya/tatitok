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
	"time"
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
	// CacheWrite1h is the 1-hour-TTL cache-write rate (anthropic bills 5m
	// and 1h writes differently). Zero when the snapshot has none; used
	// only when the event carries the per-TTL split in
	// meta.cache_creation.
	CacheWrite1h int64 `json:"cache_write_1h,omitempty"`
}

// IsZero reports whether every component rate is zero.
func (r Rates) IsZero() bool {
	return r.Input == 0 && r.Output == 0 && r.CacheWrite == 0 &&
		r.CacheWrite1h == 0 && r.CacheRead == 0
}

// CostMicroUSD prices the token components exactly: every
// tokens × (micro-USD per Mtok) product joins the sum as a big.Rat —
// arbitrary precision through the whole expression, so an extreme
// override rate fails the SINGLE final range check instead of wrapping
// int64 somewhere mid-sum (M3.1 finding 7) — and ONE half-up rounding
// converts to micro-USD. cacheWrite1h is the 1h-TTL portion of cache
// writes (0 when the split is unknown — then cacheWrite carries the
// full count at the 5m rate).
func (r Rates) CostMicroUSD(input, output, cacheWrite, cacheWrite1h, cacheRead int64) (int64, error) {
	sum := new(big.Rat)
	term := func(tokens, rate int64) {
		if tokens == 0 || rate == 0 {
			return
		}
		product := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(rate))
		sum.Add(sum, new(big.Rat).SetFrac(product, big.NewInt(1_000_000)))
	}
	term(input, r.Input)
	term(output, r.Output)
	term(cacheWrite, r.CacheWrite)
	term(cacheWrite1h, r.CacheWrite1h)
	term(cacheRead, r.CacheRead)
	return ratMicroHalfUp(sum, "event cost")
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
	Input        json.Number `json:"input_cost_per_token"`
	Output       json.Number `json:"output_cost_per_token"`
	CacheWrite   json.Number `json:"cache_creation_input_token_cost"`
	CacheWrite1h json.Number `json:"cache_creation_input_token_cost_above_1hr"`
	CacheRead    json.Number `json:"cache_read_input_token_cost"`
}

var (
	loadOnce    sync.Once
	loadErr     error
	snapVersion string
	snapRates   map[string]Rates
)

// ratMicroHalfUp rounds a non-negative rational micro-USD amount half up
// to int64 — the shared money-math exit: one rounding, one range check.
func ratMicroHalfUp(r *big.Rat, what string) (int64, error) {
	// round(num/den) half up, exactly: (2*num + den) / (2*den)
	num := new(big.Int).Lsh(r.Num(), 1)
	num.Add(num, r.Denom())
	den := new(big.Int).Lsh(r.Denom(), 1)
	q := num.Div(num, den)
	if !q.IsInt64() {
		return 0, fmt.Errorf("%s out of int64 micro-USD range", what)
	}
	return q.Int64(), nil
}

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
	return ratMicroHalfUp(r, fmt.Sprintf("price %q", n))
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
		if r.CacheWrite1h, err = usdPerTokenToMicroPerMtok(e.CacheWrite1h); err != nil {
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

// snapshotCanPriceKey reports whether the embedded snapshot could
// resolve rates for an override key under SOME provider: the bare key
// itself, or any provider-prefixed form (snapshotLookup tries
// provider/model and provider/family with the EVENT's provider, which
// load-time validation cannot know). Codex M5 round, finding 1: the
// load-time half of the regime-only guard.
func snapshotCanPriceKey(key string) (bool, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return false, loadErr
	}
	if _, ok := snapRates[key]; ok {
		return true, nil
	}
	suffix := "/" + key
	for k := range snapRates {
		if strings.HasSuffix(k, suffix) {
			return true, nil
		}
	}
	return false, nil
}

// snapshotLookup resolves rates from the embedded snapshot: model,
// provider/model, family, provider/family — first hit wins.
func snapshotLookup(provider, model, family string) (Rates, bool) {
	for _, key := range []string{model, provider + "/" + model, family, provider + "/" + family} {
		if r, ok := snapRates[key]; ok {
			return r, true
		}
	}
	return Rates{}, false
}

// Resolve picks the price for one event. Resolution order (documented in
// the milestone report):
//  1. user override file — raw model first, then family; each entry is a
//     PARTIAL patch layered over the snapshot resolution (owner's word
//     beats everything, including local-provider zeroing). With dated
//     regimes (M5 Task 1), ts picks the regime: the one containing the
//     event timestamp, else the entry's top-level default — provenance
//     "override+regime:<from>" vs plain "override".
//  2. local provider (vllm*) → basis local, cost 0
//  3. snapshot — model, provider/model, family, provider/family
//  4. unknown (cost stays NULL; never guessed)
//
// Basis `free` is NOT resolved here: per the owner's ruling it requires
// a source-reported cost of exactly $0, which only Apply can see (it
// lives in event meta) — never inferred from a "-free" model name.
func Resolve(provider, model, family string, ts time.Time, ov *Overrides) (Quote, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return Quote{}, loadErr
	}
	if p, ok := ov.lookup(model, family); ok {
		if p.free {
			return Quote{Basis: BasisFree, Rates: &Rates{}, Snapshot: "override"}, nil
		}
		// Codex M5 round, finding 1 (HIGH): an effective patch with no
		// rate fields — a regime-only entry outside its regimes —
		// contributes nothing at this timestamp, so normal resolution
		// decides: the snapshot with its truthful provenance where it
		// can price, UNKNOWN where it cannot. The old unconditional
		// return priced the snapshot-miss case at $0 api_price. Load
		// validation rejects entries that would ALWAYS land here on a
		// snapshot miss; this guard covers the remainder (the snapshot
		// prices the key only under some providers).
		if eff, suffix := p.patchAt(ts); eff.hasRates() {
			base, _ := snapshotLookup(provider, model, family)
			r := eff.apply(base)
			return Quote{Basis: BasisAPIPrice, Rates: &r, Snapshot: "override" + suffix}, nil
		}
	}
	if isLocalProvider(provider) {
		return Quote{Basis: BasisLocal, Rates: &Rates{}, Snapshot: snapVersion}, nil
	}
	if r, ok := snapshotLookup(provider, model, family); ok {
		return Quote{Basis: BasisAPIPrice, Rates: &r, Snapshot: snapVersion}, nil
	}
	return Quote{Basis: BasisUnknown, Snapshot: snapVersion}, nil
}

// EquivalentRates resolves the API-equivalent rates for a free-basis
// event (owner ruling, mirroring FR-9.3): the exact model key first,
// then the base-family key — the latter flagged "family" so the
// derivation is visible per event. M4 Task 5: resolution goes THROUGH
// the override patches (snapshot-only was a recorded known gap) — a key
// the snapshot lacks but the override file prices resolves, and a patch
// over a snapshot entry layers exactly like billing resolution. When a
// patch contributed, the derivation says so ("+override"). M5 Task 1:
// the would-have-cost answer is dated by the event it answers for — ts
// picks the patch's regime, recorded as "+regime:<from>". free:true
// entries never serve as rate sources (see ratesPatch).
func EquivalentRates(model, family string, ts time.Time, ov *Overrides) (Rates, string, bool) {
	loadOnce.Do(load)
	if loadErr != nil {
		return Rates{}, "", false
	}
	keys := [][2]string{{model, "model"}}
	if family != model {
		keys = append(keys, [2]string{family, "family"})
	}
	for _, k := range keys {
		base, snapOK := snapRates[k[0]]
		patch, regimeTag, patchOK := ov.ratesPatch(k[0], ts)
		switch {
		case patchOK:
			return patch.apply(base), k[1] + "+override" + regimeTag, true
		case snapOK:
			return base, k[1], true
		}
	}
	return Rates{}, "", false
}

// ReferenceRates resolves a user-chosen reference model for the
// cloud-equivalent "would have cost" path (FR-9.3) — no basis logic.
// M4 Task 5: the target may be snapshot-defined, override-defined, or
// an override patch layered over a snapshot entry (free:true entries
// excluded — they declare billing, not prices). suffix is the
// derivation provenance the caller appends to "reference:<model>": ""
// when the snapshot alone served, "+override" when an override patch
// shaped the rates (M4 Codex round, finding 4), plus "+regime:<from>"
// when ts fell in a dated regime (M5 Task 1).
func ReferenceRates(model string, ts time.Time, ov *Overrides) (r Rates, suffix string, found bool, err error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return Rates{}, "", false, loadErr
	}
	base, snapOK := snapRates[model]
	if patch, regimeTag, ok := ov.ratesPatch(model, ts); ok {
		return patch.apply(base), "+override" + regimeTag, true, nil
	}
	return base, "", snapOK, nil
}

// USDToMicro converts a decimal USD amount (e.g. an opencode
// source-reported cost) to integer micro-USD, exactly via big.Rat with
// half-up rounding at the sub-micro digits float sources carry.
func USDToMicro(text string) (int64, error) {
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, fmt.Errorf("unparseable USD amount %q", text)
	}
	if r.Sign() < 0 {
		return 0, fmt.Errorf("negative USD amount %q", text)
	}
	r.Mul(r, new(big.Rat).SetInt64(1_000_000))
	return ratMicroHalfUp(r, fmt.Sprintf("USD amount %q", text))
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
