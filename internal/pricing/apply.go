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
	cost := q.Rates.CostMicroUSD(in, out, cw, cr)
	e.CostUSDMicro = &cost
	rates, err := json.Marshal(q.Rates)
	if err != nil {
		return fmt.Errorf("marshal rates for %s: %w", e.ID, err)
	}
	e.PriceRates = rates
	return nil
}
