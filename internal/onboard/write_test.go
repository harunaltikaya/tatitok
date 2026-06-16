package onboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/pricing"
)

const now = "2026-06-17"

// existingPrices is a realistic starting prices.json: two model overrides
// (the codex-auto-review synthetic-label stand-in priced $0, plus a
// claude-sonnet 1h cache-write patch) and a top-level _doc — the content the
// merge must preserve.
const existingPrices = `{
  "_doc": "owner's existing overrides — must survive onboarding",
  "prices": {
    "codex-auto-review": {
      "_doc": "synthetic codex harness label, not a real model — priced $0",
      "input_usd_per_mtok": "0",
      "output_usd_per_mtok": "0"
    },
    "claude-sonnet-4-6": { "cache_write_1h_usd_per_mtok": "6.00" }
  }
}`

func entriesFor(t *testing.T, snap *TierPrices, choices ...PlanChoice) []PlanEntryOut {
	t.Helper()
	var out []PlanEntryOut
	for _, c := range choices {
		e, err := ResolveEntry(c, snap, now)
		if err != nil {
			t.Fatalf("ResolveEntry(%+v): %v", c, err)
		}
		if e != nil {
			out = append(out, *e)
		}
	}
	return out
}

func loadSnap(t *testing.T) *TierPrices {
	t.Helper()
	snap, err := LoadTierPrices()
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestMergeAddsPlansPreservingOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.json")
	if err := os.WriteFile(path, []byte(existingPrices), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := loadSnap(t)

	entries := entriesFor(t, snap,
		PlanChoice{ProviderArg: "claude", Tier: "max_20x"},
		PlanChoice{ProviderArg: "codex", Tier: "plus", Detected: true},
	)
	res, err := MergePlans(path, entries)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.BackupPath == "" {
		t.Fatalf("expected an update with a backup, got %+v", res)
	}

	// Backup must equal the original bytes exactly.
	bak, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(bak) != existingPrices {
		t.Fatal("backup is not a verbatim copy of the original")
	}

	// The merged file must LOAD, with existing overrides intact AND new plans.
	ov, err := pricing.LoadOverrides(path)
	if err != nil {
		t.Fatalf("merged file does not load: %v", err)
	}
	if ov.Len() != 2 {
		t.Fatalf("model overrides changed: Len()=%d, want 2 (codex-auto-review, claude-sonnet-4-6)", ov.Len())
	}
	plans := ov.Plans()
	byName := map[string]pricing.Plan{}
	for _, p := range plans {
		byName[p.Name] = p
	}
	cm, ok := byName["claude-max"]
	if !ok || cm.MonthlyPriceMicro == nil || *cm.MonthlyPriceMicro != 200_000_000 {
		t.Fatalf("claude-max plan wrong: %+v (ok=%v)", cm, ok)
	}
	cp, ok := byName["chatgpt-plus"]
	if !ok || cp.MonthlyPriceMicro == nil || *cp.MonthlyPriceMicro != 20_000_000 {
		t.Fatalf("chatgpt-plus plan wrong: %+v (ok=%v)", cp, ok)
	}
	// Matchers + window reuse the live shape.
	if len(cm.Matchers) != 1 || cm.Matchers[0].Harness != "claude-code" {
		t.Fatalf("claude-max matcher wrong: %+v", cm.Matchers)
	}
	if len(cp.Matchers) != 1 || cp.Matchers[0].Harness != "codex" {
		t.Fatalf("chatgpt-plus matcher wrong: %+v", cp.Matchers)
	}

	// Raw inspection: the model overrides survived verbatim, and provenance
	// _doc rides each plan entry.
	var top struct {
		Prices map[string]json.RawMessage `json:"prices"`
		Plans  []struct {
			Doc  string `json:"_doc"`
			Name string `json:"name"`
		} `json:"plans"`
	}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top.Prices["codex-auto-review"]; !ok {
		t.Fatal("codex-auto-review stand-in was clobbered")
	}
	if _, ok := top.Prices["claude-sonnet-4-6"]; !ok {
		t.Fatal("claude-sonnet-4-6 override was clobbered")
	}
	for _, p := range top.Plans {
		if !strings.Contains(p.Doc, "tier=") || !strings.Contains(p.Doc, "tatitok onboard") {
			t.Fatalf("plan %q missing provenance _doc: %q", p.Name, p.Doc)
		}
	}

	// Idempotent re-run: replace, never duplicate.
	res2, err := MergePlans(path, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Added) != 0 || len(res2.Replaced) != 2 {
		t.Fatalf("re-run not idempotent: %+v", res2)
	}
	ov2, _ := pricing.LoadOverrides(path)
	if len(ov2.Plans()) != 2 {
		t.Fatalf("re-run duplicated plans: %d", len(ov2.Plans()))
	}
}

func TestMergeCreatesWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	snap := loadSnap(t)
	entries := entriesFor(t, snap,
		PlanChoice{ProviderArg: "claude", Tier: "max_5x"},
		PlanChoice{ProviderArg: "codex", Tier: "pro_200"},
	)
	res, err := MergePlans(path, entries)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.BackupPath != "" {
		t.Fatalf("expected fresh create with no backup, got %+v", res)
	}
	ov, err := pricing.LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Plans()) != 2 {
		t.Fatalf("created file has %d plans, want 2", len(ov.Plans()))
	}
}

// TestMeteredWritesNoEntry: a metered choice yields no plan entry; only the
// other provider's plan is written.
func TestMeteredWritesNoEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	snap := loadSnap(t)
	entries := entriesFor(t, snap,
		PlanChoice{ProviderArg: "claude", Tier: MeteredTier}, // → no entry
		PlanChoice{ProviderArg: "codex", Tier: "plus", Detected: true},
	)
	if len(entries) != 1 {
		t.Fatalf("metered claude should drop to 1 entry, got %d", len(entries))
	}
	if _, err := MergePlans(path, entries); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ov.Plans() {
		if p.Name == "claude-max" {
			t.Fatal("metered Claude wrote a claude-max plan")
		}
	}
	if len(ov.Plans()) != 1 || ov.Plans()[0].Name != "chatgpt-plus" {
		t.Fatalf("want only chatgpt-plus, got %+v", ov.Plans())
	}
}

// TestResolveEntryProvenanceAndPrice checks the tier/price provenance honesty
// trail and the free / override price handling.
func TestResolveEntryProvenanceAndPrice(t *testing.T) {
	snap := loadSnap(t)

	// codex plus, confirming detection → "detected", snapshot price $20.
	e, err := ResolveEntry(PlanChoice{ProviderArg: "codex", Tier: "plus", Detected: true}, snap, now)
	if err != nil || e == nil {
		t.Fatalf("codex plus: %v", err)
	}
	if e.MonthlyPriceUSD != "20" {
		t.Errorf("codex plus price = %q, want 20", e.MonthlyPriceUSD)
	}
	if !strings.Contains(e.Doc, "detected from codex") || !strings.Contains(e.Doc, "published list default") {
		t.Errorf("codex provenance off: %q", e.Doc)
	}

	// claude max_20x, user-declared → "user-declared", $200.
	e, _ = ResolveEntry(PlanChoice{ProviderArg: "claude", Tier: "max_20x"}, snap, now)
	if e.MonthlyPriceUSD != "200" || !strings.Contains(e.Doc, "user-declared") {
		t.Errorf("claude max_20x off: price=%q doc=%q", e.MonthlyPriceUSD, e.Doc)
	}

	// free tier → NO monthly_price_usd (loader rejects $0), doc says included.
	e, _ = ResolveEntry(PlanChoice{ProviderArg: "claude", Tier: "free"}, snap, now)
	if e.MonthlyPriceUSD != "" || !strings.Contains(e.Doc, "included") {
		t.Errorf("claude free should omit price: price=%q doc=%q", e.MonthlyPriceUSD, e.Doc)
	}

	// explicit price override → "user-edited".
	e, _ = ResolveEntry(PlanChoice{ProviderArg: "claude", Tier: "max_20x", PriceUSD: "175"}, snap, now)
	if e.MonthlyPriceUSD != "175" || !strings.Contains(e.Doc, "user-edited") {
		t.Errorf("price override off: price=%q doc=%q", e.MonthlyPriceUSD, e.Doc)
	}
}
