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
//
// "regimes" (M5 Task 1, effective-dated rates): a model entry may carry
// a list of {from, until?, rates…} objects — boundaries in UTC (RFC 3339
// or YYYY-MM-DD = midnight UTC), intervals half-open [from, until), no
// overlaps. Resolution picks the regime containing the event timestamp;
// events outside any dated regime use the entry's top-level rates (the
// current/default regime). Each regime is a SIBLING of the default
// patch: it layers over the same snapshot base, never over the default's
// fields. Provenance records which regime priced the event
// ("override+regime:<from>"). The snapshot-pinning principle is
// unchanged — regimes are an owner-declared override feature, not
// reconstructed historical snapshots. free is a billing rule, not a
// price: it cannot be dated (neither inside a regime nor beside one).
//
// "plans" (M5 Task 2): owner-declared subscriptions. Usage covered by a
// plan bills $0 with basis plan_included and ALWAYS carries the
// API-equivalent when rates resolve — the M3 free-basis discipline
// applied to plans, provenance distinct from free and local
// (price_rates.plan names the covering plan). Each plan declares a
// name, matchers (harness and/or provider, optionally model — AND
// within a matcher, OR across the list, model matching raw model or
// family), a rolling window duration, a window anchor (window_start:
// floored|exact, floored default — provider-dependent, see window.go),
// and optionally a weekly cap (in API-equivalent USD — tatitok's one
// cross-model yardstick) and a monthly price. The window meter models
// LOCAL usage only: a provider's limit is account-level (shared pools,
// other devices, other accounts), so the meter is informational, never
// the authoritative counter. tatitok NEVER guesses plan membership.
// Precedence:
// per-model free:true and the local-provider rule beat plans (your own
// metal is never a subscription); plans beat everything else,
// including source-reported $0 and rate patches — patches define
// RATES, which keep pricing the equivalent. First declared plan
// covering an event wins.
//
// "explained_divergences" (M4 Task 5, the dead-check ruling): a list of
// {provider, model, reason} entries naming store-and-compare groups
// whose divergence from source-reported costs is UNDERSTOOD and ruled
// expected (the gpt-5-nano precedent; first entry: the deepseek-v4-pro
// price-cut regime break). `doctor --pricing` reports matching
// out-of-tolerance groups informationally at exit 0 instead of failing
// — a permanently failing check is a dead check. The stored costs are
// untouched either way; this only declassifies the report finding.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// overridePatch is one parsed entry: nil fields were absent and fall
// back to the snapshot-resolved component.
type overridePatch struct {
	free                                               bool
	input, output, cacheWrite, cacheWrite1h, cacheRead *int64
	regimes                                            []regime
}

// regime is one effective-dated rate period (M5 Task 1): patch applies
// to events with from ≤ ts < until (zero until = open-ended).
type regime struct {
	from, until time.Time
	patch       overridePatch
}

// patchAt picks the effective patch for an event at ts: the dated
// regime containing ts, else the entry itself (the current/default
// regime). suffix is the provenance tag joined onto "override"
// ("" for the default, "+regime:<from>" for a dated regime).
func (p overridePatch) patchAt(ts time.Time) (overridePatch, string) {
	for _, r := range p.regimes {
		if ts.Before(r.from) {
			continue
		}
		if !r.until.IsZero() && !ts.Before(r.until) {
			continue
		}
		return r.patch, "+regime:" + r.from.Format(time.RFC3339)
	}
	return p, ""
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
	refs    map[string]string // local model/family → reference model (snapshot or override-defined)
	// divergences: (provider, model) → reason, from explained_divergences.
	divergences map[[2]string]string
	plans       []Plan
	path        string
}

// Plan is one owner-declared subscription (M5 Task 2).
type Plan struct {
	Name     string
	Matchers []PlanMatcher
	// Window is the plan's rolling usage-window duration.
	Window time.Duration
	// WindowStart is the window-anchoring mode (M5 stop-1 finding:
	// provider-dependent — Anthropic floors to the UTC hour, OpenAI
	// anchors at the exact first request). Default AnchorFloored.
	WindowStart WindowAnchor
	// WeeklyCapEquivMicro is the declared weekly cap in API-equivalent
	// micro-USD (nil = no cap declared); MonthlyPriceMicro the
	// subscription's monthly price in micro-USD (nil = not declared).
	WeeklyCapEquivMicro *int64
	MonthlyPriceMicro   *int64
}

// PlanMatcher matches events by harness/provider/model; empty fields
// are wildcards, every present field must match (AND), and Model
// matches the raw model or the family.
type PlanMatcher struct {
	Harness, Provider, Model string
}

func (m PlanMatcher) matches(harness, provider, model, family string) bool {
	if m.Harness != "" && m.Harness != harness {
		return false
	}
	if m.Provider != "" && m.Provider != provider {
		return false
	}
	if m.Model != "" && m.Model != model && m.Model != family {
		return false
	}
	return true
}

// PlanFor returns the first declared plan covering the event
// coordinates. Plan precedence against free:true and local lives in
// Apply, not here.
func (o *Overrides) PlanFor(harness, provider, model, family string) (Plan, bool) {
	if o == nil {
		return Plan{}, false
	}
	for _, p := range o.plans {
		for _, m := range p.Matchers {
			if m.matches(harness, provider, model, family) {
				return p, true
			}
		}
	}
	return Plan{}, false
}

// Plans returns the declared plans in declaration order.
func (o *Overrides) Plans() []Plan {
	if o == nil {
		return nil
	}
	return o.plans
}

// hasRates reports whether the patch carries at least one rate field —
// a free:true-only entry declares billing, not prices, and is never a
// rate source for equivalents or reference targets.
func (p overridePatch) hasRates() bool {
	return p.input != nil || p.output != nil || p.cacheWrite != nil ||
		p.cacheWrite1h != nil || p.cacheRead != nil
}

// ratesPatch returns the patch effective at ts for key when it can
// serve as a RATE source: present, not owner-declared free, and
// carrying at least one rate field. The free exclusion is deliberate —
// free means "bills $0", which is a billing rule, not a price;
// equivalents derived from it would be silently meaningless zeros.
// suffix is the regime provenance tag from patchAt ("" for the default).
func (o *Overrides) ratesPatch(key string, ts time.Time) (overridePatch, string, bool) {
	if o == nil {
		return overridePatch{}, "", false
	}
	p, ok := o.patches[key]
	if !ok || p.free {
		return overridePatch{}, "", false
	}
	eff, suffix := p.patchAt(ts)
	if !eff.hasRates() {
		return overridePatch{}, "", false
	}
	return eff, suffix, true
}

// ExplainedDivergence reports the owner-recorded reason a
// (provider, model) store-and-compare group is expected to diverge, if
// one was declared.
func (o *Overrides) ExplainedDivergence(provider, model string) (string, bool) {
	if o == nil {
		return "", false
	}
	reason, ok := o.divergences[[2]string{provider, model}]
	return reason, ok
}

// Divergences reports how many explained-divergence entries are loaded.
func (o *Overrides) Divergences() int {
	if o == nil {
		return 0
	}
	return len(o.divergences)
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
// viaFamily reports that the FAMILY key matched (the raw model had no
// mapping of its own) — recorded in the stored derivation exactly like
// the equivalents' "family" flag (M4 Codex round, finding 4).
func (o *Overrides) Reference(model, family string) (ref string, viaFamily bool, ok bool) {
	if o == nil {
		return "", false, false
	}
	if ref, ok := o.refs[model]; ok {
		return ref, false, true
	}
	if family != model {
		if ref, ok := o.refs[family]; ok {
			return ref, true, true
		}
	}
	return "", false, false
}

// rateFields are the shared per-entry price fields — the top-level
// (default-regime) form and each dated regime carry the same set.
// Doc is the sanctioned notes key (see the strict-parsing rule at
// LoadOverrides); its content is never interpreted.
type rateFields struct {
	Doc          json.RawMessage `json:"_doc"`
	Free         bool            `json:"free"`
	Input        json.Number     `json:"input_usd_per_mtok"`
	Output       json.Number     `json:"output_usd_per_mtok"`
	CacheWrite   json.Number     `json:"cache_write_usd_per_mtok"`
	CacheWrite1h json.Number     `json:"cache_write_1h_usd_per_mtok"`
	CacheRead    json.Number     `json:"cache_read_usd_per_mtok"`
}

// patch parses the rate fields into an overridePatch (free carried,
// regimes left to the caller).
func (rf rateFields) patch() (overridePatch, error) {
	p := overridePatch{free: rf.Free}
	for _, c := range []struct {
		n   json.Number
		dst **int64
	}{
		{rf.Input, &p.input}, {rf.Output, &p.output},
		{rf.CacheWrite, &p.cacheWrite}, {rf.CacheWrite1h, &p.cacheWrite1h},
		{rf.CacheRead, &p.cacheRead},
	} {
		if c.n == "" {
			continue
		}
		v, err := usdPerMtokToMicro(c.n)
		if err != nil {
			return overridePatch{}, err
		}
		*c.dst = &v
	}
	return p, nil
}

type overrideEntry struct {
	rateFields
	Regimes []regimeEntry `json:"regimes"`
}

type regimeEntry struct {
	rateFields
	From  string `json:"from"`
	Until string `json:"until"`
}

// parseUTCBoundary reads a regime boundary: RFC 3339 (normalized to
// UTC) or a bare YYYY-MM-DD date (midnight UTC).
func parseUTCBoundary(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("boundary %q is neither YYYY-MM-DD nor RFC 3339", s)
	}
	return t.UTC(), nil
}

type divergenceEntry struct {
	Doc      json.RawMessage `json:"_doc"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Reason   string          `json:"reason"`
}

type planMatcherEntry struct {
	Doc      json.RawMessage `json:"_doc"`
	Harness  string          `json:"harness"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
}

type planEntry struct {
	Doc               json.RawMessage    `json:"_doc"`
	Name              string             `json:"name"`
	Matchers          []planMatcherEntry `json:"matchers"`
	Window            string             `json:"window"`
	WindowStart       string             `json:"window_start"`
	WeeklyCapEquivUSD json.Number        `json:"weekly_cap_equiv_usd"`
	MonthlyPriceUSD   json.Number        `json:"monthly_price_usd"`
}

type overrideFile struct {
	Doc                  json.RawMessage          `json:"_doc"`
	Prices               map[string]overrideEntry `json:"prices"`
	ReferenceModels      map[string]string        `json:"reference_models"`
	ExplainedDivergences []divergenceEntry        `json:"explained_divergences"`
	Plans                []planEntry              `json:"plans"`
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
//
// Parsing is STRICT (M5 hard stop 0 finding): an unknown key anywhere
// in the file is a load error, never silently ignored — the live
// ceremony's first run had a stale binary load a regime-bearing config
// without error, reprice nothing, and report OUT OF TOLERANCE at
// doctor. A declaration the binary cannot honor must fail at load,
// never lie at doctor. "_doc" is the one sanctioned notes key, allowed
// at every level and never interpreted.
func LoadOverrides(path string) (*Overrides, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("price overrides %s: %w", path, err)
	}
	var f overrideFile
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return nil, fmt.Errorf("price overrides %s: %w — this binary cannot honor that declaration, so it refuses to price under it (notes go in \"_doc\"; if the key is from a newer tatitok, rebuild first)", path, err)
		}
		return nil, fmt.Errorf("price overrides %s: %w", path, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("price overrides %s: trailing data after the override object", path)
	}
	ov := &Overrides{patches: make(map[string]overridePatch, len(f.Prices)), path: path}
	for model, e := range f.Prices {
		if model == "" {
			return nil, fmt.Errorf("price overrides %s: empty model key", path)
		}
		p, err := e.patch()
		if err != nil {
			return nil, fmt.Errorf("price overrides %s: model %q: %w", path, model, err)
		}
		if p.free && len(e.Regimes) > 0 {
			return nil, fmt.Errorf("price overrides %s: model %q: free is a billing rule, not a price — it cannot carry dated regimes", path, model)
		}
		for i, re := range e.Regimes {
			rp, err := re.patch()
			if err != nil {
				return nil, fmt.Errorf("price overrides %s: model %q regimes[%d]: %w", path, model, i, err)
			}
			if rp.free {
				return nil, fmt.Errorf("price overrides %s: model %q regimes[%d]: free cannot be dated", path, model, i)
			}
			if !rp.hasRates() {
				return nil, fmt.Errorf("price overrides %s: model %q regimes[%d]: a regime without rates prices nothing", path, model, i)
			}
			if re.From == "" {
				return nil, fmt.Errorf("price overrides %s: model %q regimes[%d]: from is required", path, model, i)
			}
			from, err := parseUTCBoundary(re.From)
			if err != nil {
				return nil, fmt.Errorf("price overrides %s: model %q regimes[%d]: from: %w", path, model, i, err)
			}
			var until time.Time
			if re.Until != "" {
				if until, err = parseUTCBoundary(re.Until); err != nil {
					return nil, fmt.Errorf("price overrides %s: model %q regimes[%d]: until: %w", path, model, i, err)
				}
				if !until.After(from) {
					return nil, fmt.Errorf("price overrides %s: model %q regimes[%d]: until %s is not after from %s", path, model, i, re.Until, re.From)
				}
			}
			p.regimes = append(p.regimes, regime{from: from, until: until, patch: rp})
		}
		sort.Slice(p.regimes, func(a, b int) bool {
			return p.regimes[a].from.Before(p.regimes[b].from)
		})
		for i := 1; i < len(p.regimes); i++ {
			prev, cur := p.regimes[i-1], p.regimes[i]
			if prev.until.IsZero() || prev.until.After(cur.from) {
				return nil, fmt.Errorf("price overrides %s: model %q: regimes starting %s and %s overlap — one timestamp must price one way",
					path, model, prev.from.Format(time.RFC3339), cur.from.Format(time.RFC3339))
			}
		}
		// Codex M5 round, finding 1 (HIGH): an entry with ONLY dated
		// regimes and no default rates leaves outside-regime events to
		// the snapshot — if the snapshot cannot price the key under any
		// provider, those events would be unpriceable (and used to price
		// $0). A declaration that cannot be honored fails at load, per
		// the strict-load philosophy.
		if len(p.regimes) > 0 && !p.hasRates() {
			ok, err := snapshotCanPriceKey(model)
			if err != nil {
				return nil, fmt.Errorf("price overrides %s: %w", path, err)
			}
			if !ok {
				return nil, fmt.Errorf("price overrides %s: model %q: only dated regimes price it and the snapshot cannot — events outside the regimes would be unpriceable; add top-level rates (the current/default regime) or remove the entry", path, model)
			}
		}
		ov.patches[model] = p
	}
	for local, ref := range f.ReferenceModels {
		if local == "" || ref == "" {
			return nil, fmt.Errorf("price overrides %s: empty reference_models entry (%q: %q)", path, local, ref)
		}
		// M4 Task 5 (removing the snapshot-only limitation): a reference
		// target resolves through the override patches parsed above, so
		// a model the snapshot lacks but this very file prices (e.g.
		// deepseek-v4-flash) is a valid target. A free:true-only entry is
		// NOT — it declares billing, not prices. The zero timestamp checks
		// the DEFAULT regime: a target must be priced undated to qualify.
		_, _, found, err := ReferenceRates(ref, time.Time{}, ov)
		if err != nil {
			return nil, fmt.Errorf("price overrides %s: %w", path, err)
		}
		if !found {
			return nil, fmt.Errorf("price overrides %s: reference model %q for %q is neither in the price snapshot nor priced by this file's overrides (free:true does not price a model)", path, ref, local)
		}
		if ov.refs == nil {
			ov.refs = make(map[string]string, len(f.ReferenceModels))
		}
		ov.refs[local] = ref
	}
	for i, d := range f.ExplainedDivergences {
		// Trimmed validation (M4 Codex round, finding 6): a
		// whitespace-only reason is not a reason, and padded
		// provider/model keys would silently never match a
		// store-and-compare group.
		provider := strings.TrimSpace(d.Provider)
		model := strings.TrimSpace(d.Model)
		reason := strings.TrimSpace(d.Reason)
		if provider == "" || model == "" || reason == "" {
			return nil, fmt.Errorf("price overrides %s: explained_divergences[%d] needs provider, model and reason — a divergence without a written reason is not explained", path, i)
		}
		if ov.divergences == nil {
			ov.divergences = make(map[[2]string]string, len(f.ExplainedDivergences))
		}
		ov.divergences[[2]string{provider, model}] = reason
	}
	names := map[string]bool{}
	for i, pe := range f.Plans {
		name := strings.TrimSpace(pe.Name)
		if name == "" {
			return nil, fmt.Errorf("price overrides %s: plans[%d]: name is required", path, i)
		}
		if names[name] {
			return nil, fmt.Errorf("price overrides %s: plans[%d]: duplicate plan name %q", path, i, name)
		}
		names[name] = true
		if len(pe.Matchers) == 0 {
			return nil, fmt.Errorf("price overrides %s: plan %q: at least one matcher is required — tatitok never guesses plan membership", path, name)
		}
		p := Plan{Name: name}
		for j, me := range pe.Matchers {
			m := PlanMatcher{
				Harness:  strings.TrimSpace(me.Harness),
				Provider: strings.TrimSpace(me.Provider),
				Model:    strings.TrimSpace(me.Model),
			}
			if m.Harness == "" && m.Provider == "" && m.Model == "" {
				return nil, fmt.Errorf("price overrides %s: plan %q matchers[%d]: an empty matcher would cover everything — declare harness, provider and/or model", path, name, j)
			}
			p.Matchers = append(p.Matchers, m)
		}
		if pe.Window == "" {
			return nil, fmt.Errorf("price overrides %s: plan %q: window duration is required (e.g. \"5h\")", path, name)
		}
		dur, err := time.ParseDuration(pe.Window)
		if err != nil || dur <= 0 {
			return nil, fmt.Errorf("price overrides %s: plan %q: window %q is not a positive Go duration", path, name, pe.Window)
		}
		p.Window = dur
		switch WindowAnchor(pe.WindowStart) {
		case "", AnchorFloored:
			p.WindowStart = AnchorFloored
		case AnchorExact:
			p.WindowStart = AnchorExact
		default:
			return nil, fmt.Errorf("price overrides %s: plan %q: window_start %q is not \"floored\" or \"exact\"", path, name, pe.WindowStart)
		}
		// Codex M5 round, finding 3 (MED): hour-floored starts OVERLAP
		// for non-whole-hour durations ≥ 1h (90m: [09:00,10:30) then
		// [10:00,11:30)) — rejected here, by validation not new math.
		// Sub-hour floored keeps its documented exception (the floor is
		// skipped); exact anchoring never floors, so any duration works.
		if p.WindowStart == AnchorFloored && p.Window >= time.Hour && p.Window%time.Hour != 0 {
			return nil, fmt.Errorf("price overrides %s: plan %q: window %s with window_start \"floored\" must be a whole number of hours (hour-floored starts would overlap) — use a whole-hour window or window_start \"exact\"", path, name, pe.Window)
		}
		for _, c := range []struct {
			n    json.Number
			what string
			dst  **int64
		}{
			{pe.WeeklyCapEquivUSD, "weekly_cap_equiv_usd", &p.WeeklyCapEquivMicro},
			{pe.MonthlyPriceUSD, "monthly_price_usd", &p.MonthlyPriceMicro},
		} {
			if c.n == "" {
				continue
			}
			v, err := USDToMicro(c.n.String())
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("price overrides %s: plan %q: %s %q is not a positive USD amount", path, name, c.what, c.n)
			}
			*c.dst = &v
		}
		ov.plans = append(ov.plans, p)
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
