package pricing

// User price overrides (FR-9.2): a local file LAYERED OVER the embedded
// snapshot — each entry is a partial patch, so a single-field entry
// (e.g. only the 1h cache-write rate the upstream snapshot is missing)
// keeps the snapshot's other components. Rates are decimal USD per
// MILLION tokens (the unit people quote prices in), parsed exactly into
// integer micro-USD — a malformed price is an error, never a silent
// zero.
//
// File: $XDG_CONFIG_HOME/tatitok/prices.json (default ~/.config/...):
//
//	{
//	  "prices": {
//	    "claude-sonnet-4-6": { "cache_write_1h_usd_per_mtok": "6.00" },
//	    "deepseek-v4-flash": {
//	      "input_usd_per_mtok": "0.28",
//	      "output_usd_per_mtok": "0.42",
//	      "cache_read_usd_per_mtok": "0.028"
//	    },
//	    "some-free-routing": { "free": true }
//	  },
//	  "reference_models": {
//	    "qwen3.6-27b": "claude-fable-5"
//	  }
//	}
//
// Keys match raw model first, then model_family (see Apply/Resolve).
//
// "reference_models" is the local cloud-equivalent gate (PRD FR-9.3,
// default off): it maps a LOCAL model (raw model or family key) to the
// snapshot model whose prices answer "what would this usage have cost
// on X". When configured, local-basis events bill 0 as always but carry
// cost_api_equiv_micro computed at the reference model's rates, with the
// derivation recorded per event (price_rates equiv_source
// "reference:<model>"). A reference model absent from the snapshot is a
// load error — failing loudly beats silently-missing equivalents.
//
// "free": true is the OWNER-DECLARED free basis: the model bills $0 by
// the owner's word, no source evidence needed, and it beats every other
// resolution rule (including local-provider zeroing). It is recorded
// distinctly from source-reported $0 free events: price_rates carries
// free_source "override" vs "source" (price_snapshot likewise reads
// "override"), so the two origins stay distinguishable per event.

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

// overridePatch is one parsed entry: nil fields were absent and fall
// back to the snapshot-resolved component.
type overridePatch struct {
	free                                               bool
	input, output, cacheWrite, cacheWrite1h, cacheRead *int64
}

// apply layers the patch over base.
func (p overridePatch) apply(base Rates) Rates {
	if p.input != nil {
		base.Input = *p.input
	}
	if p.output != nil {
		base.Output = *p.output
	}
	if p.cacheWrite != nil {
		base.CacheWrite = *p.cacheWrite
	}
	if p.cacheWrite1h != nil {
		base.CacheWrite1h = *p.cacheWrite1h
	}
	if p.cacheRead != nil {
		base.CacheRead = *p.cacheRead
	}
	return base
}

// Overrides is the parsed override file; the zero value (or nil) means
// no overrides.
type Overrides struct {
	patches map[string]overridePatch
	refs    map[string]string // local model/family → reference snapshot model
	path    string
}

// Path returns the file the overrides were read from ("" when none).
func (o *Overrides) Path() string {
	if o == nil {
		return ""
	}
	return o.path
}

// Len reports how many models the override file covers.
func (o *Overrides) Len() int {
	if o == nil {
		return 0
	}
	return len(o.patches)
}

func (o *Overrides) lookup(keys ...string) (overridePatch, bool) {
	if o == nil {
		return overridePatch{}, false
	}
	for _, k := range keys {
		if p, ok := o.patches[k]; ok {
			return p, true
		}
	}
	return overridePatch{}, false
}

// References reports how many local models have a reference mapping.
func (o *Overrides) References() int {
	if o == nil {
		return 0
	}
	return len(o.refs)
}

// Reference resolves the configured cloud-reference model for a local
// model — raw model first, then family, mirroring the price lookup.
func (o *Overrides) Reference(model, family string) (string, bool) {
	if o == nil {
		return "", false
	}
	for _, k := range []string{model, family} {
		if ref, ok := o.refs[k]; ok {
			return ref, true
		}
	}
	return "", false
}

type overrideEntry struct {
	Free         bool        `json:"free"`
	Input        json.Number `json:"input_usd_per_mtok"`
	Output       json.Number `json:"output_usd_per_mtok"`
	CacheWrite   json.Number `json:"cache_write_usd_per_mtok"`
	CacheWrite1h json.Number `json:"cache_write_1h_usd_per_mtok"`
	CacheRead    json.Number `json:"cache_read_usd_per_mtok"`
}

type overrideFile struct {
	Prices          map[string]overrideEntry `json:"prices"`
	ReferenceModels map[string]string        `json:"reference_models"`
}

// OverridesPath resolves the override file location from the
// environment (XDG_CONFIG_HOME, falling back to ~/.config).
func OverridesPath(getenv func(string) string, home string) string {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "tatitok", "prices.json")
}

// LoadOverrides reads the override file at path. A missing file is no
// overrides; a malformed file is an error (prices are inputs — failing
// loudly beats pricing under silently-dropped overrides).
func LoadOverrides(path string) (*Overrides, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("price overrides %s: %w", path, err)
	}
	var f overrideFile
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("price overrides %s: %w", path, err)
	}
	ov := &Overrides{patches: make(map[string]overridePatch, len(f.Prices)), path: path}
	for model, e := range f.Prices {
		if model == "" {
			return nil, fmt.Errorf("price overrides %s: empty model key", path)
		}
		p := overridePatch{free: e.Free}
		for _, c := range []struct {
			n   json.Number
			dst **int64
		}{
			{e.Input, &p.input}, {e.Output, &p.output},
			{e.CacheWrite, &p.cacheWrite}, {e.CacheWrite1h, &p.cacheWrite1h},
			{e.CacheRead, &p.cacheRead},
		} {
			if c.n == "" {
				continue
			}
			v, err := usdPerMtokToMicro(c.n)
			if err != nil {
				return nil, fmt.Errorf("price overrides %s: model %q: %w", path, model, err)
			}
			*c.dst = &v
		}
		ov.patches[model] = p
	}
	for local, ref := range f.ReferenceModels {
		if local == "" || ref == "" {
			return nil, fmt.Errorf("price overrides %s: empty reference_models entry (%q: %q)", path, local, ref)
		}
		_, found, err := ReferenceRates(ref)
		if err != nil {
			return nil, fmt.Errorf("price overrides %s: %w", path, err)
		}
		if !found {
			return nil, fmt.Errorf("price overrides %s: reference model %q for %q is not in the price snapshot", path, ref, local)
		}
		if ov.refs == nil {
			ov.refs = make(map[string]string, len(f.ReferenceModels))
		}
		ov.refs[local] = ref
	}
	return ov, nil
}

// usdPerMtokToMicro converts decimal USD-per-Mtok to integer micro-USD
// per Mtok, exactly: value × 1e6.
func usdPerMtokToMicro(n json.Number) (int64, error) {
	if n == "" {
		return 0, nil
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return 0, fmt.Errorf("unparseable price %q", n)
	}
	r.Mul(r, new(big.Rat).SetInt64(1_000_000))
	if !r.IsInt() {
		return 0, fmt.Errorf("price %q is finer than micro-USD per Mtok", n)
	}
	if !r.Num().IsInt64() {
		return 0, fmt.Errorf("price %q out of range", n)
	}
	return r.Num().Int64(), nil
}
