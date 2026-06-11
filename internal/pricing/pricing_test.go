package pricing

// Engine unit tests. Tiny synthetic inputs are fine here: this tests
// pure money math and resolution policy, not adapter parsing. Rate
// expectations for real models come from the committed snapshot itself
// (anthropic's published prices), so a snapshot refresh that changes
// them fails loudly and goes through the refresh ceremony.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func TestUSDPerTokenConversionExact(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"3e-06", 3_000_000},
		{"2.8e-07", 280_000},
		{"1e-05", 10_000_000},
		{"0.0", 0},
		{"1.25e-05", 12_500_000},
		// float dirt in the source rounds to the nearest micro/Mtok
		{"1.5000300000000002e-06", 1_500_030},
	}
	for _, c := range cases {
		got, err := usdPerTokenToMicroPerMtok(json.Number(c.in))
		if err != nil || got != c.want {
			t.Errorf("usdPerTokenToMicroPerMtok(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	if _, err := usdPerTokenToMicroPerMtok(json.Number("-1e-06")); err == nil {
		t.Error("negative price accepted")
	}
}

func TestCostMicroUSDRounding(t *testing.T) {
	cost := func(r Rates, in, out, cw, cw1h, cr int64) int64 {
		t.Helper()
		got, err := r.CostMicroUSD(in, out, cw, cw1h, cr)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	r := Rates{Input: 3_000_000, Output: 15_000_000} // $3 / $15 per Mtok
	// 1000 input + 100 output = 3000 + 1500 micro = 4500 micro-USD
	if got := cost(r, 1000, 100, 0, 0, 0); got != 4500 {
		t.Errorf("cost = %d, want 4500", got)
	}
	// One input token at $3/Mtok = 3 micro-USD exactly.
	if got := cost(r, 1, 0, 0, 0, 0); got != 3 {
		t.Errorf("cost = %d, want 3", got)
	}
	// Rounding: 1 token at 0.4 micro rounds to 0; at 0.5 micro rounds to 1.
	if got := cost(Rates{Input: 400_000}, 1, 0, 0, 0, 0); got != 0 {
		t.Errorf("0.4 micro rounded to %d, want 0", got)
	}
	if got := cost(Rates{Input: 500_000}, 1, 0, 0, 0, 0); got != 1 {
		t.Errorf("0.5 micro rounded to %d, want 1", got)
	}
	// 1h-TTL cache writes bill at their own rate.
	split := Rates{CacheWrite: 12_500_000, CacheWrite1h: 20_000_000}
	if got := cost(split, 0, 0, 1000, 2000, 0); got != 12_500+40_000 {
		t.Errorf("split cache write cost = %d, want 52500", got)
	}
}

// M3.1 finding 7: the sum is carried in big.Rat — an extreme (override)
// rate whose products would wrap int64 mid-sum instead computes exactly
// and fails the single final range check, loudly.
func TestCostMicroUSDOverflowIsAnError(t *testing.T) {
	huge := Rates{Input: math.MaxInt64, Output: math.MaxInt64}
	if _, err := huge.CostMicroUSD(math.MaxInt64, math.MaxInt64, 0, 0, 0); err == nil {
		t.Fatal("int64-wrapping cost accepted silently")
	}
	// Just inside range still computes exactly: MaxInt64 micro/Mtok on one
	// million tokens = MaxInt64 micro-USD.
	got, err := (Rates{Input: math.MaxInt64}).CostMicroUSD(1_000_000, 0, 0, 0, 0)
	if err != nil || got != math.MaxInt64 {
		t.Fatalf("boundary cost = %d, %v; want MaxInt64, nil", got, err)
	}
}

func TestResolveSnapshotEntry(t *testing.T) {
	q, err := Resolve("anthropic", "claude-fable-5", "claude-fable-5", nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Basis != BasisAPIPrice || q.Rates == nil {
		t.Fatalf("claude-fable-5: %+v, want api_price with rates", q)
	}
	want := Rates{Input: 10_000_000, Output: 50_000_000,
		CacheWrite: 12_500_000, CacheWrite1h: 20_000_000, CacheRead: 1_000_000}
	if *q.Rates != want {
		t.Fatalf("claude-fable-5 rates = %+v, want %+v (snapshot changed? run the refresh ceremony)", *q.Rates, want)
	}
	ver, err := SnapshotVersion()
	if err != nil || ver == "" || q.Snapshot != ver {
		t.Fatalf("snapshot version: %q vs quote %q (err %v)", ver, q.Snapshot, err)
	}
}

func TestResolveBases(t *testing.T) {
	cases := []struct {
		provider, model, family string
		want                    Basis
		zeroRates               bool
	}{
		{"vllm", "qwen3.6-27b", "qwen3.6-27b", BasisLocal, true},
		{"vllm-tecnigmaai-nvfp4", "qwen3.6-35b-nvfp4-tecnigmaai", "qwen3.6-35b", BasisLocal, true},
		{"vllm-delegate", "gx10", "gx10", BasisLocal, true},
		// Owner ruling: a "-free" NAME alone never means free — without a
		// source-reported $0 (Apply-level), resolution proceeds normally
		// and this model is simply absent from the snapshot.
		{"opencode", "deepseek-v4-flash-free", "deepseek-v4-flash", BasisUnknown, false},
		{"deepseek", "deepseek-v4-flash", "deepseek-v4-flash", BasisUnknown, false}, // absent from snapshot — honest unknown
		{"openai", "", "", BasisUnknown, false},                                     // codex pre-turn_context
		{"openai", "gpt-5.5", "gpt-5.5", BasisAPIPrice, false},
	}
	for _, c := range cases {
		q, err := Resolve(c.provider, c.model, c.family, nil)
		if err != nil {
			t.Fatal(err)
		}
		if q.Basis != c.want {
			t.Errorf("Resolve(%s, %s): basis %s, want %s", c.provider, c.model, q.Basis, c.want)
		}
		if c.zeroRates && (q.Rates == nil || !q.Rates.IsZero()) {
			t.Errorf("Resolve(%s, %s): rates %+v, want all-zero", c.provider, c.model, q.Rates)
		}
		if q.Basis == BasisUnknown && q.Rates != nil {
			t.Errorf("Resolve(%s, %s): unknown basis must carry nil rates", c.provider, c.model)
		}
	}
}

func TestOverridesResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.json")
	if err := os.WriteFile(path, []byte(`{
		"prices": {
			"deepseek-v4-flash": {
				"input_usd_per_mtok": "0.28",
				"output_usd_per_mtok": "0.42",
				"cache_read_usd_per_mtok": "0.028"
			},
			"gx10": { "free": true }
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Len() != 2 {
		t.Fatalf("Len = %d, want 2", ov.Len())
	}

	// Raw model hit (model absent from the snapshot: patch over zeros).
	q, err := Resolve("deepseek", "deepseek-v4-flash", "deepseek-v4-flash", ov)
	if err != nil {
		t.Fatal(err)
	}
	want := Rates{Input: 280_000, Output: 420_000, CacheRead: 28_000}
	if q.Basis != BasisAPIPrice || q.Snapshot != "override" || *q.Rates != want {
		t.Fatalf("override quote: %+v rates %+v, want api_price/override/%+v", q, *q.Rates, want)
	}
	// Family hit: the -free variant's FAMILY matches the override key.
	q, err = Resolve("opencode", "deepseek-v4-flash-free", "deepseek-v4-flash", ov)
	if err != nil {
		t.Fatal(err)
	}
	if q.Snapshot != "override" {
		t.Fatalf("family override not applied: %+v", q)
	}
	// free:true entry beats the local-provider rule with basis free.
	q, err = Resolve("vllm-delegate", "gx10", "gx10", ov)
	if err != nil {
		t.Fatal(err)
	}
	if q.Basis != BasisFree || !q.Rates.IsZero() {
		t.Fatalf("free override: %+v", q)
	}

	// Partial patch over a snapshot-resolved model: ONLY the specified
	// component changes (the owner's sonnet 1h-rate patch — a
	// whole-entry replacement here would zero $3/$15 base rates).
	if err := os.WriteFile(path, []byte(`{
		"prices": {"claude-sonnet-4-6": {"cache_write_1h_usd_per_mtok": "6.00"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err = LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	q, err = Resolve("anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", ov)
	if err != nil {
		t.Fatal(err)
	}
	patched := Rates{Input: 3_000_000, Output: 15_000_000,
		CacheWrite: 3_750_000, CacheWrite1h: 6_000_000, CacheRead: 300_000}
	if q.Snapshot != "override" || *q.Rates != patched {
		t.Fatalf("partial patch: %+v, want %+v", *q.Rates, patched)
	}

	// Missing file = nil overrides; malformed file = loud error.
	if ov, err := LoadOverrides(filepath.Join(dir, "absent.json")); err != nil || ov != nil {
		t.Fatalf("missing file: %v, %v", ov, err)
	}
	if err := os.WriteFile(path, []byte(`{"prices": {"x": {"input_usd_per_mtok": "not-a-number"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("malformed override file accepted")
	}
}

func TestPricedTokensPerHarness(t *testing.T) {
	reasoning := int64(50)
	// claude-code: 1:1, reasoning never reported.
	in, out, cw, cr := PricedTokens("claude-code", 10, 20, 30, 40, nil)
	if in != 10 || out != 20 || cw != 30 || cr != 40 {
		t.Errorf("claude-code mapping: %d %d %d %d", in, out, cw, cr)
	}
	// codex: output already includes reasoning — must NOT be added again.
	_, out, _, _ = PricedTokens("codex", 10, 20, 0, 40, &reasoning)
	if out != 20 {
		t.Errorf("codex output = %d, want 20 (reasoning already included)", out)
	}
	// opencode: output excludes reasoning — reasoning bills as output.
	_, out, _, _ = PricedTokens("opencode", 10, 20, 0, 40, &reasoning)
	if out != 70 {
		t.Errorf("opencode output = %d, want 70 (reasoning joins output)", out)
	}
}

func TestApplyEndToEnd(t *testing.T) {
	e := core.Event{
		ID: "x", Harness: "claude-code", Provider: "anthropic",
		Model: "claude-fable-5", ModelFamily: "claude-fable-5",
		TokensInput: 1000, TokensOutput: 100,
		TokensCacheWrite: 200, TokensCacheRead: 5000,
	}
	if err := Apply(&e, nil); err != nil {
		t.Fatal(err)
	}
	// 1000×$10 + 100×$50 + 200×$12.50 + 5000×$1 per Mtok
	// = 10000 + 5000 + 2500 + 5000 micro = 22500 micro-USD
	if e.CostUSDMicro == nil || *e.CostUSDMicro != 22500 {
		t.Fatalf("cost = %v, want 22500 micro-USD", e.CostUSDMicro)
	}
	if e.CostBasis != "api_price" || e.PriceSnapshot == "" || len(e.PriceRates) == 0 {
		t.Fatalf("derived fields incomplete: %+v", e)
	}

	// With the per-TTL split in meta, 1h writes bill at the 1h rate:
	// 200 cache-write tokens as 50×$12.50 + 150×$20 per Mtok
	// = 625 + 3000 micro, replacing the flat 200×$12.50 = 2500 micro.
	s := e
	s.Meta = map[string]any{"cache_creation": map[string]any{
		"ephemeral_5m_input_tokens": float64(50),
		"ephemeral_1h_input_tokens": float64(150),
	}}
	if err := Apply(&s, nil); err != nil {
		t.Fatal(err)
	}
	if *s.CostUSDMicro != 22500-2500+625+3000 {
		t.Fatalf("split cost = %d, want %d", *s.CostUSDMicro, 22500-2500+625+3000)
	}
	// A split that does not sum to the stored count falls back to flat.
	s.Meta = map[string]any{"cache_creation": map[string]any{
		"ephemeral_5m_input_tokens": float64(50),
		"ephemeral_1h_input_tokens": float64(9),
	}}
	if err := Apply(&s, nil); err != nil {
		t.Fatal(err)
	}
	if *s.CostUSDMicro != 22500 {
		t.Fatalf("inconsistent split must price flat: %d, want 22500", *s.CostUSDMicro)
	}

	// Unpriceable: NULL cost, basis unknown, no rates.
	u := core.Event{ID: "y", Harness: "opencode", Provider: "deepseek",
		Model: "deepseek-v4-flash", ModelFamily: "deepseek-v4-flash",
		TokensInput: 100}
	if err := Apply(&u, nil); err != nil {
		t.Fatal(err)
	}
	if u.CostUSDMicro != nil || u.CostBasis != "unknown" || u.PriceRates != nil {
		t.Fatalf("unknown pricing leaked values: %+v", u)
	}
}

// Owner ruling 2026-06-11: basis free ONLY on an explicitly-present
// source-reported cost of exactly $0; the API-equivalent value is
// computed and stored alongside, exact model key first, then
// base-family key flagged "family".
func TestApplyFreeBasis(t *testing.T) {
	// Source billed $0 and the model resolves: free + model-keyed equiv.
	free := core.Event{ID: "f1", Harness: "opencode", Provider: "opencode",
		Model: "gpt-5-nano", ModelFamily: "gpt-5-nano",
		TokensInput: 1_000_000, TokensOutput: 0,
		Meta: map[string]any{"source_cost": json.Number("0")}}
	if err := Apply(&free, nil); err != nil {
		t.Fatal(err)
	}
	if free.CostBasis != "free" || free.CostUSDMicro == nil || *free.CostUSDMicro != 0 {
		t.Fatalf("free basis: %+v", free)
	}
	if free.CostAPIEquivMicro == nil || *free.CostAPIEquivMicro <= 0 {
		t.Fatalf("API-equivalent missing for resolvable free model: %v", free.CostAPIEquivMicro)
	}
	var detail struct {
		FreeSource  string `json:"free_source"`
		EquivSource string `json:"equiv_source"`
		EquivRates  *Rates `json:"equiv_rates"`
	}
	if err := json.Unmarshal(free.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EquivSource != "model" || detail.EquivRates == nil {
		t.Fatalf("equiv derivation not recorded: %+v", detail)
	}
	if detail.FreeSource != "source" {
		t.Fatalf("source-reported free not flagged: %+v", detail)
	}

	// Family-derived equivalent: variant key absent, family resolves.
	fam := free
	fam.ID, fam.Model, fam.ModelFamily = "f2", "gpt-5.5-free-routing", "gpt-5.5"
	if err := Apply(&fam, nil); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fam.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if fam.CostBasis != "free" || detail.EquivSource != "family" {
		t.Fatalf("family-derived equiv not flagged: basis=%s detail=%+v", fam.CostBasis, detail)
	}

	// Unresolvable free model: still free, no equivalent.
	none := free
	none.ID, none.Model, none.ModelFamily = "f3", "deepseek-v4-flash-free", "deepseek-v4-flash"
	if err := Apply(&none, nil); err != nil {
		t.Fatal(err)
	}
	if none.CostBasis != "free" || none.CostAPIEquivMicro != nil {
		t.Fatalf("unresolvable free model: %+v", none)
	}

	// Source billed > $0: NOT free (normal resolution → unknown here).
	paid := free
	paid.ID, paid.Model, paid.ModelFamily = "f4", "deepseek-v4-pro", "deepseek-v4-pro"
	paid.Meta = map[string]any{"source_cost": json.Number("0.05")}
	if err := Apply(&paid, nil); err != nil {
		t.Fatal(err)
	}
	if paid.CostBasis != "unknown" || paid.CostUSDMicro != nil {
		t.Fatalf("paid source cost must not turn free: %+v", paid)
	}

	// Absent source cost: never free, even with a "-free" name.
	named := free
	named.ID, named.Model, named.ModelFamily = "f5", "deepseek-v4-flash-free", "deepseek-v4-flash"
	named.Meta = nil
	if err := Apply(&named, nil); err != nil {
		t.Fatal(err)
	}
	if named.CostBasis == "free" {
		t.Fatalf("-free name alone must never mean basis free: %+v", named)
	}

	// Local providers stay local even at source $0 (vllm rows record 0).
	local := free
	local.ID, local.Provider, local.Model, local.ModelFamily = "f6", "vllm", "qwen3.6-27b", "qwen3.6-27b"
	if err := Apply(&local, nil); err != nil {
		t.Fatal(err)
	}
	if local.CostBasis != "local" {
		t.Fatalf("local provider lost to free rule: %+v", local)
	}

	// A database meta round-trip turns the number into float64 — the
	// presence/zero detection must survive it.
	rt := free
	rt.ID = "f7"
	rt.Meta = map[string]any{"source_cost": float64(0)}
	if err := Apply(&rt, nil); err != nil {
		t.Fatal(err)
	}
	if rt.CostBasis != "free" {
		t.Fatalf("float64 zero source cost not detected: %+v", rt)
	}
}

// M3.1 finding 5: the free-basis zero test compares the UNROUNDED source
// value. A tiny-but-real source cost rounds to 0 micro-USD — it must
// price normally, never as free.
func TestApplySubMicroSourceCostIsNotFree(t *testing.T) {
	for name, cost := range map[string]any{
		"json.Number": json.Number("4e-7"),
		"string":      "0.0000001",
		"float64":     float64(4e-7),
	} {
		e := core.Event{ID: "t", Harness: "claude-code", Provider: "anthropic",
			Model: "claude-fable-5", ModelFamily: "claude-fable-5",
			TokensInput: 1000, TokensOutput: 100,
			Meta: map[string]any{"source_cost": cost}}
		if err := Apply(&e, nil); err != nil {
			t.Fatal(err)
		}
		if e.CostBasis != "api_price" || e.CostUSDMicro == nil || *e.CostUSDMicro != 15_000 {
			t.Errorf("%s: sub-micro source cost misclassified: basis=%s cost=%v",
				name, e.CostBasis, e.CostUSDMicro)
		}
	}
}

// PRD FR-9.3 / M3.1 finding 6: the config-gated local cloud-equivalent.
// reference_models maps a local model (or family) to a snapshot model;
// local-basis events then carry the "would have cost" value with
// per-event derivation provenance. Default off; an unresolvable
// reference model fails at load, never silently.
func TestApplyLocalReferenceEquivalent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{
		"reference_models": {"qwen3.6-27b": "claude-fable-5"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if ov.References() != 1 {
		t.Fatalf("References = %d, want 1", ov.References())
	}

	e := core.Event{ID: "l1", Harness: "opencode", Provider: "vllm",
		Model: "qwen3.6-27b", ModelFamily: "qwen3.6-27b",
		TokensInput: 1000, TokensOutput: 100,
		Meta: map[string]any{"source_cost": json.Number("0")}}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	if e.CostBasis != "local" || e.CostUSDMicro == nil || *e.CostUSDMicro != 0 {
		t.Fatalf("local basis lost: %+v", e)
	}
	// 1000×$10 + 100×$50 per Mtok at the fable-5 reference rates.
	if e.CostAPIEquivMicro == nil || *e.CostAPIEquivMicro != 15_000 {
		t.Fatalf("reference equivalent = %v, want 15000 micro", e.CostAPIEquivMicro)
	}
	var detail struct {
		EquivSource string `json:"equiv_source"`
		EquivRates  *Rates `json:"equiv_rates"`
	}
	if err := json.Unmarshal(e.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EquivSource != "reference:claude-fable-5" || detail.EquivRates == nil {
		t.Fatalf("reference derivation not recorded: %+v", detail)
	}

	// Unmapped local model: bills 0, carries no equivalent (default off).
	u := core.Event{ID: "l2", Harness: "opencode", Provider: "vllm-delegate",
		Model: "gx10", ModelFamily: "gx10", TokensInput: 1000}
	if err := Apply(&u, ov); err != nil {
		t.Fatal(err)
	}
	if u.CostBasis != "local" || u.CostAPIEquivMicro != nil {
		t.Fatalf("unmapped local model: %+v", u)
	}

	// A reference model the snapshot cannot price is a LOAD error.
	if err := os.WriteFile(path, []byte(`{
		"reference_models": {"qwen3.6-27b": "no-such-model-anywhere"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("unresolvable reference model accepted at load")
	}
}

// M3.1 finding 5 (owner ruling): free:true overrides stay an
// owner-declared basis, with provenance distinct from source-reported $0
// — price_rates carries free_source "override" vs "source".
func TestApplyFreeOverrideProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path,
		[]byte(`{"prices": {"gx10": {"free": true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	e := core.Event{ID: "o1", Harness: "opencode", Provider: "vllm-delegate",
		Model: "gx10", ModelFamily: "gx10", TokensInput: 100}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	if e.CostBasis != "free" || e.PriceSnapshot != "override" ||
		e.CostUSDMicro == nil || *e.CostUSDMicro != 0 {
		t.Fatalf("override-free basis wrong: %+v", e)
	}
	var detail struct {
		FreeSource string `json:"free_source"`
	}
	if err := json.Unmarshal(e.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.FreeSource != "override" {
		t.Fatalf("owner-declared free not flagged distinctly: %+v", detail)
	}
}

// loadOverridesJSON writes and parses an override file (helper for the
// M4 Task 5 tests below — synthetic CONFIG, not log fixtures).
func loadOverridesJSON(t *testing.T, body string) *Overrides {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	return ov
}

// M4 Task 5 (closing the recorded known gap): equivalent resolution
// goes through override patches, and the override-declared-free path
// carries the API-equivalent like every other free path.
func TestOverrideAwareEquivalents(t *testing.T) {
	ov := loadOverridesJSON(t, `{
		"prices": {
			"deepseek-v4-flash": {
				"input_usd_per_mtok": "0.14",
				"output_usd_per_mtok": "0.28",
				"cache_read_usd_per_mtok": "0.0028"
			},
			"deepseek-v4-flash-free": { "free": true }
		}
	}`)

	// Snapshot-absent key resolves through the family patch; the exact
	// model's free:true entry is NOT a rate source.
	r, derived, ok := EquivalentRates("deepseek-v4-flash-free", "deepseek-v4-flash", ov)
	want := Rates{Input: 140_000, Output: 280_000, CacheRead: 2_800}
	if !ok || derived != "family+override" || r != want {
		t.Fatalf("equiv through patch: ok=%v derived=%q rates=%+v, want family+override %+v", ok, derived, r, want)
	}

	// Apply end to end: owner-declared free bills $0 AND carries the
	// equivalent (this exact case used to store NULL for both reasons
	// at once — M3.1 recorded gap, flash-free symptom).
	e := core.Event{ID: "ff", Harness: "opencode", Provider: "deepseek",
		Model: "deepseek-v4-flash-free", ModelFamily: "deepseek-v4-flash",
		TokensInput: 1_000_000, TokensOutput: 1_000_000}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	if e.CostBasis != "free" || e.CostUSDMicro == nil || *e.CostUSDMicro != 0 {
		t.Fatalf("override-free billing: %+v", e)
	}
	// 1 Mtok in at $0.14 + 1 Mtok out at $0.28 = $0.42.
	if e.CostAPIEquivMicro == nil || *e.CostAPIEquivMicro != 420_000 {
		t.Fatalf("override-free equivalent = %v, want 420000 micro", e.CostAPIEquivMicro)
	}
	var detail struct {
		FreeSource  string `json:"free_source"`
		EquivSource string `json:"equiv_source"`
	}
	if err := json.Unmarshal(e.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.FreeSource != "override" || detail.EquivSource != "family+override" {
		t.Fatalf("provenance: %+v", detail)
	}

	// A patch over a snapshot entry layers for equivalents exactly like
	// billing resolution (the sonnet 1h-rate patch).
	ov = loadOverridesJSON(t, `{
		"prices": {"claude-sonnet-4-6": {"cache_write_1h_usd_per_mtok": "6.00"}}
	}`)
	r, derived, ok = EquivalentRates("claude-sonnet-4-6", "claude-sonnet-4-6", ov)
	if !ok || derived != "model+override" || r.CacheWrite1h != 6_000_000 || r.Input != 3_000_000 {
		t.Fatalf("layered equiv: ok=%v derived=%q rates=%+v", ok, derived, r)
	}

	// No overrides: pure snapshot behavior is unchanged.
	r, derived, ok = EquivalentRates("claude-sonnet-4-6", "claude-sonnet-4-6", nil)
	if !ok || derived != "model" || r.CacheWrite1h != 0 {
		t.Fatalf("snapshot-only equiv changed: ok=%v derived=%q rates=%+v", ok, derived, r)
	}
}

// M4 Task 5: reference targets may be override-defined (the snapshot-only
// validation was the recorded limitation keeping qwen pointed at a
// stand-in); free:true-only entries still do not qualify.
func TestOverrideDefinedReferenceTargets(t *testing.T) {
	ov := loadOverridesJSON(t, `{
		"prices": {
			"deepseek-v4-flash": {
				"input_usd_per_mtok": "0.14",
				"output_usd_per_mtok": "0.28"
			}
		},
		"reference_models": { "qwen3.6-27b": "deepseek-v4-flash" }
	}`)
	r, found, err := ReferenceRates("deepseek-v4-flash", ov)
	if err != nil || !found || r.Input != 140_000 {
		t.Fatalf("override-defined reference target: found=%v rates=%+v err=%v", found, r, err)
	}

	e := core.Event{ID: "lq", Harness: "opencode", Provider: "vllm",
		Model: "qwen3.6-27b", ModelFamily: "qwen3.6-27b",
		TokensInput: 1_000_000, TokensOutput: 1_000_000}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	if e.CostBasis != "local" || e.CostAPIEquivMicro == nil || *e.CostAPIEquivMicro != 420_000 {
		t.Fatalf("local event with override-defined reference: %+v equiv=%v", e, e.CostAPIEquivMicro)
	}
	var detail struct {
		EquivSource string `json:"equiv_source"`
	}
	if err := json.Unmarshal(e.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EquivSource != "reference:deepseek-v4-flash" {
		t.Fatalf("reference provenance: %+v", detail)
	}

	// free:true-only target: rejected at load (declares billing, not prices).
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{
		"prices": { "x-free": { "free": true } },
		"reference_models": { "q": "x-free" }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("free:true-only reference target accepted")
	}
	// Entirely unknown target: still rejected.
	if err := os.WriteFile(path, []byte(`{
		"reference_models": { "q": "no-such-model-anywhere" }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("unknown reference target accepted")
	}
}

// M4 Task 5: explained_divergences parsing and lookup (the dead-check
// ruling — doctor consumes these to report ruled store-and-compare
// failures informationally).
func TestExplainedDivergences(t *testing.T) {
	ov := loadOverridesJSON(t, `{
		"explained_divergences": [
			{"provider": "deepseek", "model": "deepseek-v4-pro",
			 "reason": "price-cut regime break, ruled 2026-06-11"}
		]
	}`)
	if ov.Divergences() != 1 {
		t.Fatalf("Divergences = %d, want 1", ov.Divergences())
	}
	reason, ok := ov.ExplainedDivergence("deepseek", "deepseek-v4-pro")
	if !ok || reason == "" {
		t.Fatalf("explained divergence not found: %q %v", reason, ok)
	}
	if _, ok := ov.ExplainedDivergence("deepseek", "deepseek-v4-flash"); ok {
		t.Fatal("unrelated model reported as explained")
	}
	if _, ok := (*Overrides)(nil).ExplainedDivergence("p", "m"); ok {
		t.Fatal("nil overrides reported an explanation")
	}

	// A divergence without a written reason is not explained: load error.
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{
		"explained_divergences": [{"provider": "p", "model": "m"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("reason-less divergence entry accepted")
	}
}
