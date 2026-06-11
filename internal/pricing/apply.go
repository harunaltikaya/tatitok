package pricing

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// priceDetail is the price_rates column payload (FR-9.5): the rates the
// event was BILLED at, plus — for free-basis events — the
// API-equivalent rates and how they were derived ("model" exact key or
// "family" fallback, flagged per the owner's ruling).
type priceDetail struct {
	Rates
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
	q, err := Resolve(e.Provider, e.Model, e.ModelFamily, ov)
	if err != nil {
		return err
	}

	in, out, cw, cr := PricedTokens(e.Harness,
		e.TokensInput, e.TokensOutput, e.TokensCacheWrite, e.TokensCacheRead,
		e.TokensReasoning)

	// Free interception: a source-reported $0 beats snapshot pricing and
	// unknown — but never an explicit override or the local-provider rule.
	e.CostAPIEquivMicro = nil
	if q.Snapshot != "override" && q.Basis != BasisLocal {
		if src, present := sourceCostMicro(e.Meta); present && src == 0 {
			q.Basis, q.Rates = BasisFree, &Rates{}
			detail := priceDetail{Rates: Rates{}}
			if equiv, derivedFrom, ok := EquivalentRates(e.Model, e.ModelFamily); ok {
				ev := equiv.CostMicroUSD(in, out, cw, 0, cr)
				e.CostAPIEquivMicro = &ev
				detail.EquivRates = &equiv
				detail.EquivSource = derivedFrom
			}
			return stamp(e, q, 0, detail)
		}
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
	cost := q.Rates.CostMicroUSD(in, out, cw, cw1h, cr)
	return stamp(e, q, cost, priceDetail{Rates: *q.Rates})
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

// sourceCostMicro reads the source-reported cost out of event meta
// (meta.source_cost — exact decimal text from the adapter, float64
// after a database meta round-trip).
func sourceCostMicro(meta map[string]any) (int64, bool) {
	v, present := meta["source_cost"]
	if !present {
		return 0, false
	}
	switch n := v.(type) {
	case json.Number:
		micro, err := USDToMicro(n.String())
		if err != nil {
			return 0, false
		}
		return micro, true
	case string:
		micro, err := USDToMicro(n)
		if err != nil {
			return 0, false
		}
		return micro, true
	case float64:
		r := new(big.Rat).SetFloat64(n)
		if r == nil || r.Sign() < 0 {
			return 0, false
		}
		r.Mul(r, new(big.Rat).SetInt64(1_000_000))
		num := new(big.Int).Lsh(r.Num(), 1)
		num.Add(num, r.Denom())
		den := new(big.Int).Lsh(r.Denom(), 1)
		q := num.Div(num, den)
		if !q.IsInt64() {
			return 0, false
		}
		return q.Int64(), true
	}
	return 0, false
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
