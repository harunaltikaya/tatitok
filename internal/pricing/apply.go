package pricing

import (
	"encoding/json"
	"fmt"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// Apply prices one event in place: resolves rates (override → local →
// free → snapshot → unknown), maps the stored token columns to price
// components per harness, and stamps the four derived cost fields.
// Adapters never call this — the ingest layer (and explicit pricing
// recompute) do.
func Apply(e *core.Event, ov *Overrides) error {
	q, err := Resolve(e.Provider, e.Model, e.ModelFamily, ov)
	if err != nil {
		return err
	}
	e.CostBasis = string(q.Basis)
	e.PriceSnapshot = q.Snapshot
	if q.Rates == nil {
		e.CostUSDMicro = nil
		e.PriceRates = nil
		return nil
	}
	in, out, cw, cr := PricedTokens(e.Harness,
		e.TokensInput, e.TokensOutput, e.TokensCacheWrite, e.TokensCacheRead,
		e.TokensReasoning)
	// Cache-write TTL split (claude-code meta.cache_creation): 5m and 1h
	// writes bill at different rates. Used only when the split is present,
	// sums to the stored cache-write count, and the snapshot knows a 1h
	// rate — anything else prices flat at the 5m rate.
	var cw1h int64
	if five, oneH, ok := cacheWriteSplit(e.Meta, cw); ok && q.Rates.CacheWrite1h > 0 {
		cw, cw1h = five, oneH
	}
	cost := q.Rates.CostMicroUSD(in, out, cw, cw1h, cr)
	e.CostUSDMicro = &cost
	rates, err := json.Marshal(q.Rates)
	if err != nil {
		return fmt.Errorf("marshal rates for %s: %w", e.ID, err)
	}
	e.PriceRates = rates
	return nil
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
