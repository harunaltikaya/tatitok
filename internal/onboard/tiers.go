// Package onboard authors the "plans" section of the user's price-override
// file (prices.json) from what tatitok can detect plus what the user
// declares — the backend of zero-config subscription onboarding.
//
// It is deliberately NARROW: it READS the codex/claude-code adapters'
// discovery (to detect the Codex tier and the Claude subscription signal)
// and REUSES pricing.OverridesPath / pricing.LoadOverrides to locate and
// validate the file it writes. It never touches pricing.Apply, the cost
// columns, the store, or the rollups — repricing under the freshly-written
// plans is the explicit, separate `tatitok recompute --pricing` step.
package onboard

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

// tier_prices_snapshot.json is the pinned, embedded default for consumer
// subscription monthly list prices — the litellm-snapshot pattern (a JSON
// with version + _source), so a fresh install ships the defaults with no
// external file. Prices are DEFAULTS, not assertions (see the file's _doc).
//
//go:embed tier_prices_snapshot.json
var tierSnapshotJSON []byte

type tierSnapshotFile struct {
	Version string                            `json:"version"`
	Tiers   map[string]map[string]json.Number `json:"tiers"`
}

// TierPrices is the loaded tier→price snapshot. Lookups are keyed by the
// provider snapshot key ("anthropic", "openai") and the tier name.
type TierPrices struct {
	Version string
	tiers   map[string]map[string]json.Number
}

// LoadTierPrices parses the embedded snapshot.
func LoadTierPrices() (*TierPrices, error) {
	var f tierSnapshotFile
	if err := json.Unmarshal(tierSnapshotJSON, &f); err != nil {
		return nil, fmt.Errorf("onboard: tier_prices_snapshot.json: %w", err)
	}
	if f.Version == "" {
		return nil, fmt.Errorf("onboard: tier_prices_snapshot.json has no version")
	}
	if len(f.Tiers) == 0 {
		return nil, fmt.Errorf("onboard: tier_prices_snapshot.json has no tiers")
	}
	return &TierPrices{Version: f.Version, tiers: f.Tiers}, nil
}

// Price returns the monthly list price (decimal USD, verbatim from the
// snapshot) for (provider, tier). ok is false for an unknown provider/tier.
func (t *TierPrices) Price(provider, tier string) (json.Number, bool) {
	m, ok := t.tiers[provider]
	if !ok {
		return "", false
	}
	p, ok := m[tier]
	return p, ok
}

// Tiers lists the known tier names for a provider snapshot key, sorted.
func (t *TierPrices) Tiers(provider string) []string {
	m, ok := t.tiers[provider]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
