package pricing

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// priceDetail is the price_rates column payload (FR-9.5): the rates the
// event was BILLED at, plus — for free-basis events — the
// API-equivalent rates and how they were derived ("model" exact key or
// "family" fallback, flagged per the owner's ruling).
//
// FreeSource distinguishes the two ways basis `free` arises (M3.1
// finding 5, owner ruling): "override" — the owner DECLARED the model
// free in the price-override file (free:true, owner's word, no source
// evidence needed) — vs "source" — the source itself reported exactly
// $0 for the event. Same basis, different provenance, visible per event.
type priceDetail struct {
	Rates
	FreeSource  string `json:"free_source,omitempty"`
	EquivRates  *Rates `json:"equiv_rates,omitempty"`
	EquivSource string `json:"equiv_source,omitempty"`
}

// Apply prices one event in place: resolves rates (override → local →
// free → snapshot → unknown), maps the stored token columns to price
// components per harness, and stamps the derived cost fields.
//
// Basis `free` (owner ruling 2026-06-11): ONLY when the source itself
// reported a cost that is explicitly present and exactly $0
// (meta.source_cost — opencode store rows). Never inferred from a
// "-free" model name; an absent source cost leaves normal resolution in
// charge. Free events bill $0 and ALWAYS carry the API-equivalent
// computation when the snapshot can price the model — exact model key
// first, then base-family key flagged "family" — mirroring the
// local-basis design (FR-9.3).
//
// Adapters never call this — the ingest layer (and explicit pricing
// recompute) do.
func Apply(e *core.Event, ov *Overrides) error {
	q, err := Resolve(e.Provider, e.Model, e.ModelFamily, e.TS, ov)
	if err != nil {
		return err
	}

	in, out, cw, cr := PricedTokens(e.Harness,
		e.TokensInput, e.TokensOutput, e.TokensCacheWrite, e.TokensCacheRead,
		e.TokensReasoning)

	// Free interception: a source-reported $0 beats snapshot pricing and
	// unknown — but never an explicit override (including a dated regime,
	// whose provenance reads "override+regime:<from>") or the
	// local-provider rule. The zero test is on the UNROUNDED source value
	// (M3.1 finding 5): a tiny-but-real cost like $4e-7 rounds to 0
	// micro-USD and must NOT be misclassified as free.
	e.CostAPIEquivMicro = nil
	if !strings.HasPrefix(q.Snapshot, "override") && q.Basis != BasisLocal {
		if src, present := sourceCostRat(e.Meta); present && src.Sign() == 0 {
			q.Basis, q.Rates = BasisFree, &Rates{}
			detail := priceDetail{Rates: Rates{}, FreeSource: "source"}
			if equiv, derivedFrom, ok := EquivalentRates(e.Model, e.ModelFamily, e.TS, ov); ok {
				ev, err := equiv.CostMicroUSD(in, out, cw, 0, cr)
				if err != nil {
					return fmt.Errorf("%s: %w", e.ID, err)
				}
				e.CostAPIEquivMicro = &ev
				detail.EquivRates = &equiv
				detail.EquivSource = derivedFrom
			}
			return stamp(e, q, 0, detail)
		}
	}

	// Local cloud-equivalent (PRD FR-9.3, M3.1 finding 6): local usage
	// bills 0 as always, but when the owner configured a reference model
	// (prices.json reference_models — the config gate, default off) the
	// event ALWAYS carries what this usage would have cost there,
	// mirroring the free-basis equivalent design.
	if q.Basis == BasisLocal {
		detail := priceDetail{Rates: *q.Rates}
		if ref, viaFamily, ok := ov.Reference(e.Model, e.ModelFamily); ok {
			rr, suffix, found, err := ReferenceRates(ref, e.TS, ov)
			if err != nil {
				return err
			}
			if found { // guaranteed by the LoadOverrides validation
				ev, err := rr.CostMicroUSD(in, out, cw, 0, cr)
				if err != nil {
					return fmt.Errorf("%s: %w", e.ID, err)
				}
				e.CostAPIEquivMicro = &ev
				detail.EquivRates = &rr
				// Full derivation per event (M4 Codex round, finding 4),
				// matching the equivalents' convention: +family when the
				// local mapping matched via the family key, +override (and
				// +regime:<from>, M5 Task 1) when an override patch shaped
				// the target's rates — an override-defined yardstick reads
				// differently from a snapshot rate.
				src := "reference:" + ref
				if viaFamily {
					src += "+family"
				}
				src += suffix
				detail.EquivSource = src
			}
		}
		return stamp(e, q, 0, detail)
	}

	if q.Rates == nil {
		e.CostBasis = string(q.Basis)
		e.PriceSnapshot = q.Snapshot
		e.CostUSDMicro = nil
		e.PriceRates = nil
		return nil
	}

	// Cache-write TTL split (claude-code meta.cache_creation): 5m and 1h
	// writes bill at different rates. Used only when the split is present,
	// sums to the stored cache-write count, and the snapshot knows a 1h
	// rate — anything else prices flat at the 5m rate.
	var cw1h int64
	if five, oneH, ok := cacheWriteSplit(e.Meta, cw); ok && q.Rates.CacheWrite1h > 0 {
		cw, cw1h = five, oneH
	}
	cost, err := q.Rates.CostMicroUSD(in, out, cw, cw1h, cr)
	if err != nil {
		return fmt.Errorf("%s: %w", e.ID, err)
	}
	detail := priceDetail{Rates: *q.Rates}
	if q.Basis == BasisFree {
		// Owner-declared free (override file free:true): kept as its own
		// basis source, distinct from source-reported $0 (M3.1 ruling).
		// M4 Task 5 (closing the recorded known gap): this path now
		// carries the API-equivalent like every other free path — for
		// deepseek-v4-flash-free that resolves through the family's
		// override patch ("family+override"), where it used to store NULL
		// for both missing pieces at once.
		detail.FreeSource = "override"
		if equiv, derivedFrom, ok := EquivalentRates(e.Model, e.ModelFamily, e.TS, ov); ok {
			ev, err := equiv.CostMicroUSD(in, out, cw, 0, cr)
			if err != nil {
				return fmt.Errorf("%s: %w", e.ID, err)
			}
			e.CostAPIEquivMicro = &ev
			detail.EquivRates = &equiv
			detail.EquivSource = derivedFrom
		}
	}
	return stamp(e, q, cost, detail)
}

func stamp(e *core.Event, q Quote, cost int64, detail priceDetail) error {
	e.CostBasis = string(q.Basis)
	e.PriceSnapshot = q.Snapshot
	e.CostUSDMicro = &cost
	rates, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal rates for %s: %w", e.ID, err)
	}
	e.PriceRates = rates
	return nil
}

// sourceCostRat reads the source-reported cost out of event meta
// (meta.source_cost — exact decimal text from the adapter, float64
// after a database meta round-trip) as an EXACT rational, no rounding:
// the free-basis rule compares it to zero, and rounding first would
// misclassify sub-micro real costs as $0 (M3.1 finding 5). Negative or
// unparseable values read as absent — normal resolution stays in charge.
func sourceCostRat(meta map[string]any) (*big.Rat, bool) {
	v, present := meta["source_cost"]
	if !present {
		return nil, false
	}
	var r *big.Rat
	switch n := v.(type) {
	case json.Number:
		r, _ = new(big.Rat).SetString(n.String())
	case string:
		r, _ = new(big.Rat).SetString(n)
	case float64:
		r = new(big.Rat).SetFloat64(n)
	}
	if r == nil || r.Sign() < 0 {
		return nil, false
	}
	return r, true
}

// cacheWriteSplit extracts the per-TTL cache-write counts from
// meta.cache_creation (claude-code: ephemeral_5m/1h input tokens).
// Valid only when both parse and sum to the stored cache-write total.
func cacheWriteSplit(meta map[string]any, total int64) (five, oneH int64, ok bool) {
	cc, _ := meta["cache_creation"].(map[string]any)
	if cc == nil {
		return 0, 0, false
	}
	five, ok5 := metaInt(cc["ephemeral_5m_input_tokens"])
	oneH, ok1 := metaInt(cc["ephemeral_1h_input_tokens"])
	if !ok5 || !ok1 || five < 0 || oneH < 0 || five+oneH != total {
		return 0, 0, false
	}
	return five, oneH, true
}

// metaInt reads an integral token count out of decoded JSON (float64
// from encoding/json, json.Number from UseNumber decoders).
func metaInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		i := int64(n)
		return i, float64(i) == n
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
