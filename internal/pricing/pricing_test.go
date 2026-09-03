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
	"time"

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
	q, err := Resolve("anthropic", "claude-fable-5", "claude-fable-5", time.Time{}, nil)
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
		// Widened local rule: sglang / robotlab prefixes, "-local" suffix,
		// case-insensitive; a bare name that merely CONTAINS one of them
		// (or "-local" inside the name) does not match.
		{"sglang", "qwen38-fp8", "qwen38-fp8", BasisLocal, true},
		{"sglang-dflash2", "qwen38-fp8", "qwen38-fp8", BasisLocal, true},
		{"robotlab-qwen38-dflash2-low", "qwen38-r0b0tlab", "qwen38-r0b0tlab", BasisLocal, true},
		{"aeon-qwen36-35b-heretic-local", "aeon-qwen36-deep", "aeon-qwen36-deep", BasisLocal, true},
		{"VLLM-Flash-Next", "qwen3.8-flash-next", "qwen3.8-flash-next", BasisLocal, true},
		{"Laguna-W4A4-LOCAL", "laguna", "laguna", BasisLocal, true},
		{"myvllm", "qwen", "qwen", BasisUnknown, false},
		{"local-proxy", "qwen", "qwen", BasisUnknown, false},
		{"fp8-qwen38-dflash2-low", "qwen38", "qwen38", BasisUnknown, false},
		// Owner ruling: a "-free" NAME alone never means free — without a
		// source-reported $0 (Apply-level), resolution proceeds normally
		// and this model is simply absent from the snapshot.
		{"opencode", "deepseek-v4-flash-free", "deepseek-v4-flash", BasisUnknown, false},
		{"deepseek", "deepseek-v4-flash", "deepseek-v4-flash", BasisUnknown, false}, // absent from snapshot — honest unknown
		{"openai", "", "", BasisUnknown, false},                                     // codex pre-turn_context
		{"openai", "gpt-5.5", "gpt-5.5", BasisAPIPrice, false},
	}
	for _, c := range cases {
		q, err := Resolve(c.provider, c.model, c.family, time.Time{}, nil)
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
	q, err := Resolve("deepseek", "deepseek-v4-flash", "deepseek-v4-flash", time.Time{}, ov)
	if err != nil {
		t.Fatal(err)
	}
	want := Rates{Input: 280_000, Output: 420_000, CacheRead: 28_000}
	if q.Basis != BasisAPIPrice || q.Snapshot != "override" || *q.Rates != want {
		t.Fatalf("override quote: %+v rates %+v, want api_price/override/%+v", q, *q.Rates, want)
	}
	// Family hit: the -free variant's FAMILY matches the override key.
	q, err = Resolve("opencode", "deepseek-v4-flash-free", "deepseek-v4-flash", time.Time{}, ov)
	if err != nil {
		t.Fatal(err)
	}
	if q.Snapshot != "override" {
		t.Fatalf("family override not applied: %+v", q)
	}
	// free:true entry beats the local-provider rule with basis free.
	q, err = Resolve("vllm-delegate", "gx10", "gx10", time.Time{}, ov)
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
	q, err = Resolve("anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", time.Time{}, ov)
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

// FR-9.3 / M3.1 finding 6: the config-gated local cloud-equivalent.
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
	r, derived, ok := EquivalentRates("deepseek-v4-flash-free", "deepseek-v4-flash", time.Time{}, ov)
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
	r, derived, ok = EquivalentRates("claude-sonnet-4-6", "claude-sonnet-4-6", time.Time{}, ov)
	if !ok || derived != "model+override" || r.CacheWrite1h != 6_000_000 || r.Input != 3_000_000 {
		t.Fatalf("layered equiv: ok=%v derived=%q rates=%+v", ok, derived, r)
	}

	// No overrides: pure snapshot behavior is unchanged.
	r, derived, ok = EquivalentRates("claude-sonnet-4-6", "claude-sonnet-4-6", time.Time{}, nil)
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
	r, suffix, found, err := ReferenceRates("deepseek-v4-flash", time.Time{}, ov)
	if err != nil || !found || suffix != "+override" || r.Input != 140_000 {
		t.Fatalf("override-defined reference target: found=%v suffix=%q rates=%+v err=%v", found, suffix, r, err)
	}
	// Snapshot-defined target: no patch involvement reported.
	if _, suffix, found, err := ReferenceRates("claude-fable-5", time.Time{}, ov); err != nil || !found || suffix != "" {
		t.Fatalf("snapshot reference target: found=%v suffix=%q err=%v", found, suffix, err)
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
	// M4 Codex finding 4: the override-defined yardstick is
	// distinguishable from a snapshot rate in the stored derivation.
	if detail.EquivSource != "reference:deepseek-v4-flash+override" {
		t.Fatalf("reference provenance: %+v, want reference:deepseek-v4-flash+override", detail)
	}

	// Family-matched local key records +family too.
	fam := core.Event{ID: "lf", Harness: "opencode", Provider: "vllm",
		Model: "qwen3.6-27b-nvfp4-recipe", ModelFamily: "qwen3.6-27b",
		TokensInput: 1_000_000}
	if err := Apply(&fam, ov); err != nil {
		t.Fatal(err)
	}
	var famDetail struct {
		EquivSource string `json:"equiv_source"`
	}
	if err := json.Unmarshal(fam.PriceRates, &famDetail); err != nil {
		t.Fatal(err)
	}
	if famDetail.EquivSource != "reference:deepseek-v4-flash+family+override" {
		t.Fatalf("family-matched reference provenance: %+v, want reference:deepseek-v4-flash+family+override", famDetail)
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

	// A whitespace-only reason is not a reason either (Codex M4
	// finding 6) — and padded keys must match after trimming.
	if err := os.WriteFile(path, []byte(`{
		"explained_divergences": [{"provider": "p", "model": "m", "reason": "   \t  "}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("whitespace-only divergence reason accepted")
	}
	ov = loadOverridesJSON(t, `{
		"explained_divergences": [
			{"provider": " deepseek ", "model": " deepseek-v4-pro ", "reason": "  ruled  "}
		]
	}`)
	reason, ok = ov.ExplainedDivergence("deepseek", "deepseek-v4-pro")
	if !ok || reason != "ruled" {
		t.Fatalf("trimmed divergence entry not matched: %q %v", reason, ok)
	}
}

// M5 Task 1 addendum (hard stop 0 finding): unknown keys in the
// override file are loud load errors. The live ceremony's first run —
// a stale binary silently ignoring the regimes key, repricing nothing,
// and reporting OUT OF TOLERANCE at doctor — is the failure mode this
// pins shut: a declaration the binary cannot honor must fail at load,
// never lie at doctor. "_doc" is the one sanctioned notes key.
func TestOverridesUnknownKeysAreLoadErrors(t *testing.T) {
	bad := map[string]string{
		"unknown top-level key": `{"prices": {}, "plans": {}}`,
		"unknown key in a price entry": `{"prices": {"m": {
			"input_usd_per_mtok": "1.00", "regims": []}}}`,
		"regimes key misspelled (the live-ceremony shape)": `{"prices": {"m": {
			"input_usd_per_mtok": "1.00",
			"regime": [{"from": "2026-01-01", "input_usd_per_mtok": "2.00"}]}}}`,
		"unknown key inside a regime": `{"prices": {"m": {
			"input_usd_per_mtok": "1.00",
			"regimes": [{"from": "2026-01-01", "input_usd_per_mtok": "2.00", "label": "old"}]}}}`,
		"unknown key in a divergence entry": `{"explained_divergences": [
			{"provider": "p", "model": "m", "reason": "ruled", "severity": "low"}]}`,
		"trailing data after the object": `{"prices": {}} {"prices": {}}`,
	}
	dir := t.TempDir()
	for name, body := range bad {
		path := filepath.Join(dir, "prices.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOverrides(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// The sanctioned notes key loads at every level — the live file's
	// shape (top-level _doc + full entry/regime/divergence structure).
	ov := loadOverridesJSON(t, `{
		"_doc": "owner notes",
		"prices": {
			"deepseek-v4-pro": {
				"_doc": "per-entry note",
				"input_usd_per_mtok": "0.435",
				"regimes": [{
					"_doc": "pre-cut regime note",
					"from": "2026-05-02", "until": "2026-05-20",
					"input_usd_per_mtok": "1.74"
				}]
			},
			"x-free": {"free": true}
		},
		"reference_models": {"q": "deepseek-v4-pro"},
		"explained_divergences": [
			{"_doc": "note", "provider": "p", "model": "m", "reason": "ruled"}
		]
	}`)
	if ov.Len() != 2 || ov.References() != 1 || ov.Divergences() != 1 {
		t.Fatalf("documented file misparsed: len=%d refs=%d div=%d",
			ov.Len(), ov.References(), ov.Divergences())
	}
}

// regimeOverrides is the M5 Task 1 shape: a dated regime (the deepseek
// pre-cut rates pattern) beside the entry's top-level current/default
// rates. Synthetic CONFIG, not a log fixture.
const regimeOverrides = `{
	"prices": {
		"deepseek-v4-pro": {
			"input_usd_per_mtok": "0.435",
			"output_usd_per_mtok": "0.87",
			"cache_read_usd_per_mtok": "0.003625",
			"regimes": [
				{
					"from": "2025-09-01T00:00:00Z",
					"until": "2026-05-25T00:00:00Z",
					"input_usd_per_mtok": "1.74",
					"output_usd_per_mtok": "3.48",
					"cache_read_usd_per_mtok": "0.145"
				}
			]
		}
	}
}`

// M5 Task 1: the override schema learns time. A model entry may carry a
// `regimes` list ({from, until?, rates…} in UTC); a malformed regime is
// a load error, never a silently-undated rate.
func TestRegimeParsing(t *testing.T) {
	ov := loadOverridesJSON(t, regimeOverrides)
	if ov.Len() != 1 {
		t.Fatalf("Len = %d, want 1", ov.Len())
	}

	// Date-only boundaries parse as UTC midnight.
	ov = loadOverridesJSON(t, `{
		"prices": {"m": {
			"input_usd_per_mtok": "1.00",
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01",
				"input_usd_per_mtok": "2.00"}]
		}}
	}`)
	q, err := Resolve("p", "m", "m", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ov)
	if err != nil {
		t.Fatal(err)
	}
	if q.Rates.Input != 2_000_000 || q.Snapshot != "override+regime:2026-01-01T00:00:00Z" {
		t.Fatalf("date-only from: %+v %q", *q.Rates, q.Snapshot)
	}

	// Every malformed shape is a loud load error.
	bad := map[string]string{
		"missing from": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [{"until": "2026-02-01", "input_usd_per_mtok": "2.00"}]}}}`,
		"unparseable from": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [{"from": "last tuesday", "input_usd_per_mtok": "2.00"}]}}}`,
		"until not after from": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [{"from": "2026-02-01", "until": "2026-02-01", "input_usd_per_mtok": "2.00"}]}}}`,
		"rates-less regime": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01"}]}}}`,
		"free inside a regime": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01", "free": true}]}}}`,
		"regimes on a free entry": `{"prices": {"m": {"free": true,
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01", "input_usd_per_mtok": "2.00"}]}}}`,
		"overlapping regimes": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [
				{"from": "2026-01-01", "until": "2026-03-01", "input_usd_per_mtok": "2.00"},
				{"from": "2026-02-01", "until": "2026-04-01", "input_usd_per_mtok": "3.00"}]}}}`,
		"open-ended regime shadowing a later one": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [
				{"from": "2026-01-01", "input_usd_per_mtok": "2.00"},
				{"from": "2026-02-01", "until": "2026-04-01", "input_usd_per_mtok": "3.00"}]}}}`,
		"bad regime price": `{"prices": {"m": {"input_usd_per_mtok": "1.00",
			"regimes": [{"from": "2026-01-01", "input_usd_per_mtok": "cheap"}]}}}`,
	}
	dir := t.TempDir()
	for name, body := range bad {
		path := filepath.Join(dir, "prices.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOverrides(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Resolution picks the regime containing the event timestamp ([from,
// until) — half-open); events outside any dated regime use the
// top-level default, and the provenance names the regime that priced
// the event.
func TestRegimeResolution(t *testing.T) {
	ov := loadOverridesJSON(t, regimeOverrides)
	oldRates := Rates{Input: 1_740_000, Output: 3_480_000, CacheRead: 145_000}
	curRates := Rates{Input: 435_000, Output: 870_000, CacheRead: 3_625}
	from := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		ts   time.Time
		want Rates
		snap string
	}{
		{"inside regime", time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			oldRates, "override+regime:2025-09-01T00:00:00Z"},
		{"at from (inclusive)", from, oldRates, "override+regime:2025-09-01T00:00:00Z"},
		{"at until (exclusive)", until, curRates, "override"},
		{"after regime", time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC), curRates, "override"},
		{"before regime", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), curRates, "override"},
		{"zero timestamp", time.Time{}, curRates, "override"},
	}
	for _, c := range cases {
		q, err := Resolve("deepseek", "deepseek-v4-pro", "deepseek-v4-pro", c.ts, ov)
		if err != nil {
			t.Fatal(err)
		}
		if q.Basis != BasisAPIPrice || *q.Rates != c.want || q.Snapshot != c.snap {
			t.Errorf("%s: rates %+v snap %q, want %+v %q", c.name, *q.Rates, q.Snapshot, c.want, c.snap)
		}
	}

	// Family-key matches carry the regime exactly like raw-model matches.
	q, err := Resolve("deepseek", "deepseek-v4-pro-beta", "deepseek-v4-pro",
		time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), ov)
	if err != nil {
		t.Fatal(err)
	}
	if *q.Rates != oldRates || q.Snapshot != "override+regime:2025-09-01T00:00:00Z" {
		t.Fatalf("family-key regime: %+v %q", *q.Rates, q.Snapshot)
	}

	// A regime patch is a SIBLING of the default patch: both layer over
	// the same snapshot base, so a partial regime entry inherits the
	// SNAPSHOT's components — not the default patch's.
	ov = loadOverridesJSON(t, `{
		"prices": {"claude-sonnet-4-6": {
			"cache_write_1h_usd_per_mtok": "6.00",
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01",
				"input_usd_per_mtok": "6.00"}]
		}}
	}`)
	q, err = Resolve("anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6",
		time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), ov)
	if err != nil {
		t.Fatal(err)
	}
	want := Rates{Input: 6_000_000, Output: 15_000_000,
		CacheWrite: 3_750_000, CacheRead: 300_000} // 1h rate NOT inherited from the default patch
	if *q.Rates != want {
		t.Fatalf("regime over snapshot base: %+v, want %+v", *q.Rates, want)
	}
	// Outside the regime the default patch applies as before.
	q, err = Resolve("anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6",
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), ov)
	if err != nil {
		t.Fatal(err)
	}
	if q.Rates.Input != 3_000_000 || q.Rates.CacheWrite1h != 6_000_000 {
		t.Fatalf("default patch after regime: %+v", *q.Rates)
	}
}

// Apply stamps the regime into the stored provenance, so a repriced
// history remains auditable per event (which regime priced it).
func TestApplyRegimeProvenance(t *testing.T) {
	ov := loadOverridesJSON(t, regimeOverrides)
	old := core.Event{ID: "rg1", Harness: "opencode", Provider: "deepseek",
		Model: "deepseek-v4-pro", ModelFamily: "deepseek-v4-pro",
		TS:          time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		TokensInput: 1_000_000, TokensOutput: 1_000_000}
	if err := Apply(&old, ov); err != nil {
		t.Fatal(err)
	}
	// 1 Mtok in at $1.74 + 1 Mtok out at $3.48 = $5.22.
	if old.CostUSDMicro == nil || *old.CostUSDMicro != 5_220_000 ||
		old.PriceSnapshot != "override+regime:2025-09-01T00:00:00Z" {
		t.Fatalf("regime-priced event: cost=%v snap=%q", old.CostUSDMicro, old.PriceSnapshot)
	}
	var detail struct {
		Rates
	}
	if err := json.Unmarshal(old.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Input != 1_740_000 {
		t.Fatalf("stored rates are not the regime's: %+v", detail.Rates)
	}

	cur := old
	cur.ID, cur.TS = "rg2", time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	if err := Apply(&cur, ov); err != nil {
		t.Fatal(err)
	}
	// 1 Mtok in at $0.435 + 1 Mtok out at $0.87 = $1.305.
	if cur.CostUSDMicro == nil || *cur.CostUSDMicro != 1_305_000 || cur.PriceSnapshot != "override" {
		t.Fatalf("default-priced event: cost=%v snap=%q", cur.CostUSDMicro, cur.PriceSnapshot)
	}
}

// M5 Task 2: owner-declared plans. tatitok never guesses plan
// membership — the `plans` section names each subscription, its
// matchers, its window, and optionally a weekly cap and monthly price.
func TestPlanParsing(t *testing.T) {
	ov := loadOverridesJSON(t, `{
		"plans": [
			{
				"_doc": "the harness subscription",
				"name": "claude-max",
				"matchers": [
					{"harness": "claude-code"},
					{"_doc": "review bot", "harness": "opencode", "provider": "anthropic", "model": "claude-sonnet-4-6"}
				],
				"window": "5h",
				"weekly_cap_equiv_usd": "120.00",
				"monthly_price_usd": "200.00"
			},
			{
				"name": "codex-sub",
				"matchers": [{"harness": "codex"}],
				"window": "5h"
			}
		]
	}`)
	plans := ov.Plans()
	if len(plans) != 2 {
		t.Fatalf("Plans = %d, want 2", len(plans))
	}
	p := plans[0]
	if p.Name != "claude-max" || p.Window != 5*time.Hour || len(p.Matchers) != 2 {
		t.Fatalf("plan parsed wrong: %+v", p)
	}
	if p.WeeklyCapEquivMicro == nil || *p.WeeklyCapEquivMicro != 120_000_000 ||
		p.MonthlyPriceMicro == nil || *p.MonthlyPriceMicro != 200_000_000 {
		t.Fatalf("plan money parsed wrong: cap=%v price=%v", p.WeeklyCapEquivMicro, p.MonthlyPriceMicro)
	}
	if plans[1].WeeklyCapEquivMicro != nil || plans[1].MonthlyPriceMicro != nil {
		t.Fatalf("absent money fields must stay nil: %+v", plans[1])
	}
	// Anchoring (M5 stop-1 finding): floored is the default; exact is the
	// OpenAI behavior, declared per plan.
	if p.WindowStart != AnchorFloored || plans[1].WindowStart != AnchorFloored {
		t.Fatalf("default anchor: %q / %q, want floored", p.WindowStart, plans[1].WindowStart)
	}
	ov = loadOverridesJSON(t, `{
		"plans": [{"name": "p", "matchers": [{"harness": "codex"}],
			"window": "5h", "window_start": "exact"}]
	}`)
	if ov.Plans()[0].WindowStart != AnchorExact {
		t.Fatalf("exact anchor not parsed: %+v", ov.Plans()[0])
	}
	// Non-whole-hour durations are fine under EXACT anchoring (no floor
	// → no overlap), and sub-hour floored keeps its documented exception
	// (the floor is skipped).
	ov = loadOverridesJSON(t, `{
		"plans": [
			{"name": "a", "matchers": [{"harness": "x"}], "window": "90m", "window_start": "exact"},
			{"name": "b", "matchers": [{"harness": "y"}], "window": "30m"}
		]
	}`)
	if len(ov.Plans()) != 2 {
		t.Fatalf("exact 90m / floored 30m rejected: %+v", ov.Plans())
	}

	bad := map[string]string{
		"missing name":   `{"plans": [{"matchers": [{"harness": "h"}], "window": "5h"}]}`,
		"duplicate name": `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "5h"}, {"name": "p", "matchers": [{"harness": "x"}], "window": "5h"}]}`,
		"no matchers":    `{"plans": [{"name": "p", "window": "5h"}]}`,
		"empty matcher (would cover everything)": `{"plans": [{"name": "p", "matchers": [{}], "window": "5h"}]}`,
		"missing window":      `{"plans": [{"name": "p", "matchers": [{"harness": "h"}]}]}`,
		"unparseable window":  `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "5 hours"}]}`,
		"non-positive window": `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "-5h"}]}`,
		"bad weekly cap":      `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "5h", "weekly_cap_equiv_usd": "lots"}]}`,
		"zero monthly price":  `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "5h", "monthly_price_usd": "0"}]}`,
		"unknown plan key":    `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "5h", "cap": "1"}]}`,
		"unknown matcher key": `{"plans": [{"name": "p", "matchers": [{"harnes": "h"}], "window": "5h"}]}`,
		"invalid window_start": `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "5h", "window_start": "rounded"}]}`,
		// Codex M5 round, finding 3 (MED): hour-floored starts overlap
		// for non-whole-hour durations ≥ 1h (90m: [09:00,10:30) then
		// [10:00,11:30)) — rejected at load, by validation not new math.
		"non-whole-hour floored window": `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "90m"}]}`,
		"non-whole-hour floored window (explicit)": `{"plans": [{"name": "p", "matchers": [{"harness": "h"}], "window": "1h30m", "window_start": "floored"}]}`,
	}
	dir := t.TempDir()
	for name, body := range bad {
		path := filepath.Join(dir, "prices.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOverrides(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Matcher semantics: AND within a matcher (every present field must
// match), OR across the list; the model field matches raw model or
// family; the first declared plan covering the event wins.
func TestPlanFor(t *testing.T) {
	ov := loadOverridesJSON(t, `{
		"plans": [
			{"name": "first", "matchers": [
				{"harness": "claude-code"},
				{"harness": "opencode", "model": "review-bot-large"}
			], "window": "5h"},
			{"name": "second", "matchers": [{"harness": "opencode"}], "window": "5h"}
		]
	}`)
	cases := []struct {
		harness, provider, model, family string
		want                             string
		none                             bool
	}{
		{"claude-code", "anthropic", "claude-fable-5", "claude-fable-5", "first", false},
		// AND within: harness alone is not enough for the second matcher…
		{"opencode", "x", "other-model", "other-model", "second", false},
		// …but harness+model matches "first" before "second" (declared order).
		{"opencode", "x", "review-bot-large", "review-bot-large", "first", false},
		// model field matches the FAMILY too.
		{"opencode", "x", "review-bot-large-v2", "review-bot-large", "first", false},
		{"codex", "openai", "gpt-5.5", "gpt-5.5", "", true},
	}
	for _, c := range cases {
		p, ok := ov.PlanFor(c.harness, c.provider, c.model, c.family)
		if ok == c.none || (!c.none && p.Name != c.want) {
			t.Errorf("PlanFor(%s,%s,%s,%s) = %v/%v, want %q (none=%v)",
				c.harness, c.provider, c.model, c.family, p.Name, ok, c.want, c.none)
		}
	}
	if _, ok := (*Overrides)(nil).PlanFor("h", "p", "m", "f"); ok {
		t.Fatal("nil overrides matched a plan")
	}
}

// M5 Task 2: basis plan_included — actual cost $0, the API-equivalent
// ALWAYS computed and stored when rates resolve (the M3 free-basis
// discipline applied to plans), provenance distinct from free and
// local (price_rates.plan names the covering plan; equiv_source
// "billing" says the equivalent is exactly what billing resolution
// would have charged).
func TestApplyPlanIncluded(t *testing.T) {
	ov := loadOverridesJSON(t, `{
		"prices": {
			"deepseek-v4-pro": {
				"input_usd_per_mtok": "0.435",
				"regimes": [{"from": "2025-09-01", "until": "2026-05-25",
					"input_usd_per_mtok": "1.74"}]
			},
			"gx10": {"free": true}
		},
		"plans": [{"name": "claude-max", "matchers": [
			{"harness": "claude-code"},
			{"harness": "opencode"}
		], "window": "5h", "monthly_price_usd": "200.00"}]
	}`)

	// Snapshot-priced model under the plan: $0 billed, equivalent is the
	// full would-have-cost INCLUDING the cache-write TTL split.
	e := core.Event{ID: "p1", Harness: "claude-code", Provider: "anthropic",
		Model: "claude-fable-5", ModelFamily: "claude-fable-5",
		TS:          time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
		TokensInput: 1000, TokensOutput: 100, TokensCacheWrite: 200, TokensCacheRead: 5000,
		Meta: map[string]any{"cache_creation": map[string]any{
			"ephemeral_5m_input_tokens": float64(50),
			"ephemeral_1h_input_tokens": float64(150),
		}}}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	if e.CostBasis != "plan_included" || e.CostUSDMicro == nil || *e.CostUSDMicro != 0 {
		t.Fatalf("plan basis: %+v", e)
	}
	// 22500 flat (TestApplyEndToEnd) − 2500 flat cache + 625 + 3000 split.
	if e.CostAPIEquivMicro == nil || *e.CostAPIEquivMicro != 23625 {
		t.Fatalf("plan equivalent = %v, want 23625 micro", e.CostAPIEquivMicro)
	}
	var detail struct {
		Plan        string `json:"plan"`
		EquivSource string `json:"equiv_source"`
		EquivRates  *Rates `json:"equiv_rates"`
		Input       int64  `json:"input"`
	}
	if err := json.Unmarshal(e.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Plan != "claude-max" || detail.EquivSource != "billing" ||
		detail.EquivRates == nil || detail.Input != 0 {
		t.Fatalf("plan provenance: %+v (billed rates must be zero, equiv recorded)", detail)
	}
	ver, _ := SnapshotVersion()
	if e.PriceSnapshot != ver {
		t.Fatalf("plan event snapshot = %q, want %q (pins what priced the equivalent)", e.PriceSnapshot, ver)
	}

	// Unknown-model usage under the plan (the auto-review mass): basis
	// plan_included, $0, equivalent honestly NULL until rates exist.
	u := core.Event{ID: "p2", Harness: "opencode", Provider: "mystery",
		Model: "auto-review-xl", ModelFamily: "auto-review-xl",
		TS: time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC), TokensInput: 500}
	if err := Apply(&u, ov); err != nil {
		t.Fatal(err)
	}
	if u.CostBasis != "plan_included" || u.CostUSDMicro == nil || *u.CostUSDMicro != 0 ||
		u.CostAPIEquivMicro != nil {
		t.Fatalf("unknown model under plan: %+v equiv=%v", u, u.CostAPIEquivMicro)
	}

	// Regime-priced model under the plan: the equivalent is dated.
	r := core.Event{ID: "p3", Harness: "opencode", Provider: "deepseek",
		Model: "deepseek-v4-pro", ModelFamily: "deepseek-v4-pro",
		TS: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), TokensInput: 1_000_000}
	if err := Apply(&r, ov); err != nil {
		t.Fatal(err)
	}
	if r.CostBasis != "plan_included" || r.CostAPIEquivMicro == nil || *r.CostAPIEquivMicro != 1_740_000 {
		t.Fatalf("regime equivalent under plan: basis=%s equiv=%v", r.CostBasis, r.CostAPIEquivMicro)
	}
	if r.PriceSnapshot != "override+regime:2025-09-01T00:00:00Z" {
		t.Fatalf("regime provenance under plan: %q", r.PriceSnapshot)
	}

	// Source-reported $0 LOSES to the plan: plan_included is the more
	// honest basis when the owner declared coverage.
	s := core.Event{ID: "p4", Harness: "opencode", Provider: "anthropic",
		Model: "claude-fable-5", ModelFamily: "claude-fable-5",
		TS:          time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
		TokensInput: 1000,
		Meta:        map[string]any{"source_cost": json.Number("0")}}
	if err := Apply(&s, ov); err != nil {
		t.Fatal(err)
	}
	if s.CostBasis != "plan_included" {
		t.Fatalf("source-$0 beat the plan: %+v", s)
	}

	// free:true (per-model owner word) BEATS the plan.
	f := core.Event{ID: "p5", Harness: "opencode", Provider: "openrouter",
		Model: "gx10", ModelFamily: "gx10",
		TS: time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC), TokensInput: 100}
	if err := Apply(&f, ov); err != nil {
		t.Fatal(err)
	}
	if f.CostBasis != "free" {
		t.Fatalf("plan beat the per-model free declaration: %+v", f)
	}

	// Local providers BEAT the plan: your own metal is never a
	// subscription.
	l := core.Event{ID: "p6", Harness: "opencode", Provider: "vllm",
		Model: "qwen3.6-27b", ModelFamily: "qwen3.6-27b",
		TS: time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC), TokensInput: 100}
	if err := Apply(&l, ov); err != nil {
		t.Fatal(err)
	}
	if l.CostBasis != "local" {
		t.Fatalf("plan beat the local-provider rule: %+v", l)
	}

	// Codex M5 round, finding 2 (MED): a PATCHED local model resolves
	// api_price (the owner's rates beat local zeroing for BILLING — M3
	// rule) but it is still the owner's metal: the local-provider rule
	// is evaluated before plan matching UNCONDITIONALLY, patch or no
	// patch — a plan never captures a vllm* event.
	pl := loadOverridesJSON(t, `{
		"prices": {"qwen3.6-27b": {"input_usd_per_mtok": "0.15"}},
		"plans": [{"name": "trap", "matchers": [{"harness": "opencode"}], "window": "5h"}]
	}`)
	pe := core.Event{ID: "p7", Harness: "opencode", Provider: "vllm",
		Model: "qwen3.6-27b", ModelFamily: "qwen3.6-27b",
		TS: time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC), TokensInput: 1_000_000}
	if err := Apply(&pe, pl); err != nil {
		t.Fatal(err)
	}
	if pe.CostBasis != "api_price" || pe.CostUSDMicro == nil || *pe.CostUSDMicro != 150_000 {
		t.Fatalf("patched local model under a matching plan: basis=%s cost=%v, want api_price at the patch (never plan_included)", pe.CostBasis, pe.CostUSDMicro)
	}
}

// Codex M5 round, finding 1 (HIGH): a regime-only override on a model
// the snapshot cannot price must never price outside-regime events at
// $0 api_price. Two layers: (b) load-time — an entry whose pricing
// would be unresolvable outside its regimes is a config error naming
// the cure; (a) runtime — where validation's reach ends (the snapshot
// prices the key only under SOME providers), an effective patch with
// no rates over a snapshot miss falls through to normal resolution:
// unpriced, never $0.
func TestRegimeOnlyOverrideCannotPriceZero(t *testing.T) {
	// (b) snapshot-absent key with only regimes: load error.
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{
		"prices": {"no-such-model-anywhere": {
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01",
				"input_usd_per_mtok": "1.00"}]
		}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("regime-only entry on a snapshot-absent model accepted — outside-regime events would price $0")
	}

	// Top-level rates (a default regime) cure it.
	loadOverridesJSON(t, `{
		"prices": {"no-such-model-anywhere": {
			"input_usd_per_mtok": "2.00",
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01",
				"input_usd_per_mtok": "1.00"}]
		}}
	}`)

	// A provider-prefixed snapshot key also cures it at load — the
	// snapshot CAN price nova-lite-v1 (amazon-nova/nova-lite-v1; the
	// bare key is absent, pinned here so a snapshot refresh that adds
	// it fails this test loudly instead of silently weakening it).
	ov := loadOverridesJSON(t, `{
		"prices": {"nova-lite-v1": {
			"regimes": [{"from": "2026-01-01", "until": "2026-02-01",
				"input_usd_per_mtok": "1.00"}]
		}}
	}`)
	if _, ok := snapRates["nova-lite-v1"]; ok {
		t.Fatal("snapshot now prices bare nova-lite-v1 — pick a new prefixed-only key for this test")
	}

	// (a) runtime, inside the regime: regime rates, dated provenance.
	in := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	out := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	q, err := Resolve("someprovider", "nova-lite-v1", "nova-lite-v1", in, ov)
	if err != nil {
		t.Fatal(err)
	}
	if q.Basis != BasisAPIPrice || q.Rates.Input != 1_000_000 ||
		q.Snapshot != "override+regime:2026-01-01T00:00:00Z" {
		t.Fatalf("inside regime: %+v %q", q.Rates, q.Snapshot)
	}
	// Outside the regime under a provider the snapshot cannot price:
	// UNPRICED — the literal finding (was: $0 api_price "override").
	q, err = Resolve("someprovider", "nova-lite-v1", "nova-lite-v1", out, ov)
	if err != nil {
		t.Fatal(err)
	}
	if q.Basis != BasisUnknown || q.Rates != nil {
		t.Fatalf("outside regime, snapshot miss: %+v — must be unpriced, never $0", q)
	}
	e := core.Event{ID: "f1", Harness: "opencode", Provider: "someprovider",
		Model: "nova-lite-v1", ModelFamily: "nova-lite-v1",
		TS: out, TokensInput: 1000}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	if e.CostBasis != "unknown" || e.CostUSDMicro != nil {
		t.Fatalf("outside-regime event stamped: basis=%s cost=%v, want unknown/NULL", e.CostBasis, e.CostUSDMicro)
	}
	// Outside the regime under the provider the snapshot DOES price:
	// the snapshot's rates and the snapshot's provenance — the override
	// contributed nothing to this event.
	q, err = Resolve("amazon-nova", "nova-lite-v1", "nova-lite-v1", out, ov)
	if err != nil {
		t.Fatal(err)
	}
	ver, _ := SnapshotVersion()
	if q.Basis != BasisAPIPrice || q.Rates == nil || q.Rates.IsZero() || q.Snapshot != ver {
		t.Fatalf("outside regime, snapshot hit: %+v %q, want snapshot rates + snapshot provenance", q.Rates, q.Snapshot)
	}
}

// An explicit override beats source-$0 free interception (M3.1 design)
// — and a DATED regime is an explicit override too: its provenance
// reads "override+regime:<from>", not bare "override", and the guard
// must treat both alike. Regression pin for the M5 Task 2 round.
func TestApplyRegimeBeatsSourceZero(t *testing.T) {
	ov := loadOverridesJSON(t, regimeOverrides)
	e := core.Event{ID: "rz", Harness: "opencode", Provider: "deepseek",
		Model: "deepseek-v4-pro", ModelFamily: "deepseek-v4-pro",
		TS:          time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		TokensInput: 1_000_000,
		Meta:        map[string]any{"source_cost": json.Number("0")}}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	if e.CostBasis != "api_price" || e.CostUSDMicro == nil || *e.CostUSDMicro != 1_740_000 {
		t.Fatalf("regime-priced event lost to source-$0 interception: basis=%s cost=%v",
			e.CostBasis, e.CostUSDMicro)
	}
}

// Equivalent and reference derivations are regime-aware too: a
// would-have-cost answer is dated by the event it answers for, and the
// stored derivation says which regime served it.
func TestRegimeAwareEquivalentsAndReferences(t *testing.T) {
	ov := loadOverridesJSON(t, `{
		"prices": {
			"deepseek-v4-flash": {
				"input_usd_per_mtok": "0.14",
				"output_usd_per_mtok": "0.28",
				"regimes": [{"from": "2025-09-01", "until": "2026-05-25",
					"input_usd_per_mtok": "0.28", "output_usd_per_mtok": "0.56"}]
			}
		},
		"reference_models": {"qwen3.6-27b": "deepseek-v4-flash"}
	}`)
	inRegime := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)

	r, derived, ok := EquivalentRates("deepseek-v4-flash-free", "deepseek-v4-flash", inRegime, ov)
	if !ok || derived != "family+override+regime:2025-09-01T00:00:00Z" || r.Input != 280_000 {
		t.Fatalf("regime equiv: ok=%v derived=%q rates=%+v", ok, derived, r)
	}
	r, derived, ok = EquivalentRates("deepseek-v4-flash-free", "deepseek-v4-flash", after, ov)
	if !ok || derived != "family+override" || r.Input != 140_000 {
		t.Fatalf("post-regime equiv: ok=%v derived=%q rates=%+v", ok, derived, r)
	}

	r, suffix, found, err := ReferenceRates("deepseek-v4-flash", inRegime, ov)
	if err != nil || !found || suffix != "+override+regime:2025-09-01T00:00:00Z" || r.Input != 280_000 {
		t.Fatalf("regime reference: found=%v suffix=%q rates=%+v err=%v", found, suffix, r, err)
	}

	e := core.Event{ID: "lr", Harness: "opencode", Provider: "vllm",
		Model: "qwen3.6-27b", ModelFamily: "qwen3.6-27b",
		TS:          inRegime,
		TokensInput: 1_000_000, TokensOutput: 1_000_000}
	if err := Apply(&e, ov); err != nil {
		t.Fatal(err)
	}
	// 1 Mtok in at $0.28 + 1 Mtok out at $0.56 = $0.84 at the dated regime.
	if e.CostAPIEquivMicro == nil || *e.CostAPIEquivMicro != 840_000 {
		t.Fatalf("regime reference equivalent = %v, want 840000 micro", e.CostAPIEquivMicro)
	}
	var detail struct {
		EquivSource string `json:"equiv_source"`
	}
	if err := json.Unmarshal(e.PriceRates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EquivSource != "reference:deepseek-v4-flash+override+regime:2025-09-01T00:00:00Z" {
		t.Fatalf("reference regime provenance: %q", detail.EquivSource)
	}
}
