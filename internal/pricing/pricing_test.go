package pricing

// Engine unit tests. Tiny synthetic inputs are fine here: this tests
// pure money math and resolution policy, not adapter parsing. Rate
// expectations for real models come from the committed snapshot itself
// (anthropic's published prices), so a snapshot refresh that changes
// them fails loudly and goes through the refresh ceremony.

import (
	"encoding/json"
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
	r := Rates{Input: 3_000_000, Output: 15_000_000} // $3 / $15 per Mtok
	// 1000 input + 100 output = 3000 + 1500 micro = 4500 micro-USD
	if got := r.CostMicroUSD(1000, 100, 0, 0); got != 4500 {
		t.Errorf("cost = %d, want 4500", got)
	}
	// One input token at $3/Mtok = 3 micro-USD exactly.
	if got := r.CostMicroUSD(1, 0, 0, 0); got != 3 {
		t.Errorf("cost = %d, want 3", got)
	}
	// Rounding: 1 token at 0.4 micro rounds to 0; at 0.5 micro rounds to 1.
	if got := (Rates{Input: 400_000}).CostMicroUSD(1, 0, 0, 0); got != 0 {
		t.Errorf("0.4 micro rounded to %d, want 0", got)
	}
	if got := (Rates{Input: 500_000}).CostMicroUSD(1, 0, 0, 0); got != 1 {
		t.Errorf("0.5 micro rounded to %d, want 1", got)
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
		CacheWrite: 12_500_000, CacheRead: 1_000_000}
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
		{"opencode", "deepseek-v4-flash-free", "deepseek-v4-flash", BasisFree, true},
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

	// Raw model hit.
	q, err := Resolve("deepseek", "deepseek-v4-flash", "deepseek-v4-flash", ov)
	if err != nil {
		t.Fatal(err)
	}
	want := Rates{Input: 280_000, Output: 420_000, CacheRead: 28_000}
	if q.Basis != BasisAPIPrice || q.Snapshot != "override" || *q.Rates != want {
		t.Fatalf("override quote: %+v rates %+v, want api_price/override/%+v", q, *q.Rates, want)
	}
	// Family hit: the -free variant folds into family deepseek-v4-flash,
	// but an override beats even the free-suffix rule ONLY via its keys —
	// here the variant's FAMILY matches the override.
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
