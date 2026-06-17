package parity

// Local cloud-equivalent over the real opencode fixture set (FR-9.3,
// M3.1 finding 6): with a reference_models mapping configured, every
// local-basis (vllm*) event carries cost_api_equiv_micro at the
// reference model's rates; unmapped local models and the unconfigured
// default carry none. The expected sums are recomputed here from the
// stats token sums and the pinned snapshot rates (claude-fable-5:
// $10/$50/$1 per Mtok — whole micro-USD per token, and the fixture's
// vllm rows have zero cache writes, so per-event rounding cannot split
// a day sum).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

func TestLocalReferenceEquivalentFixtures(t *testing.T) {
	set := paritySets[2]
	if set.harness != "opencode" {
		t.Fatalf("parity set order changed: %q", set.harness)
	}

	ovPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(ovPath, []byte(`{
		"reference_models": {
			"qwen3.6-27b": "claude-fable-5",
			"qwen3.6-35b": "claude-fable-5"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(ovPath)
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "ref.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	machineDir := filepath.Join(set.fixtureBase, "gx10")
	sum, err := adapters.IngestBackfill(ctx, st, set.adapter, []adapters.Source{{
		Harness: set.harness, Root: set.root(t, machineDir), Machine: "gx10",
	}}, ov)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Inserted != wantOpencodeRows {
		t.Fatalf("ingested %d rows, want %d", sum.Inserted, wantOpencodeRows)
	}

	rows, err := st.DailyBy(ctx, time.UTC, "model", store.Filters{Harness: []string{"opencode"}})
	if err != nil {
		t.Fatal(err)
	}
	const inRate, outRate, readRate = 10, 50, 1 // claude-fable-5, micro-USD per token
	var mapped, family int
	for _, r := range rows {
		switch r.Key {
		case "qwen3.6-27b", "qwen3.6-35b-nvfp4-tecnigmaai":
			// Direct mapping and the family fallback (nvfp4 recipe →
			// family qwen3.6-35b). OpenCode reasoning bills as output.
			want := inRate*r.Input + outRate*(r.Output+r.Reasoning) + readRate*r.CacheRead
			if r.CacheWrite != 0 {
				t.Fatalf("%s %s: fixture grew cache writes — exact day-sum oracle no longer valid", r.Date, r.Key)
			}
			if r.CostAPIEquivMicro != want {
				t.Errorf("%s %s: equiv %d micro, want %d", r.Date, r.Key, r.CostAPIEquivMicro, want)
			}
			if r.CostUSDMicro != 0 {
				t.Errorf("%s %s: local usage billed %d micro, want 0", r.Date, r.Key, r.CostUSDMicro)
			}
			if r.Key == "qwen3.6-27b" {
				mapped++
			} else {
				family++
			}
		case "gx10", "qwen3.6-35b-a3b":
			// Local but unmapped: no equivalent — the gate is per model.
			if r.CostAPIEquivMicro != 0 {
				t.Errorf("%s %s: unmapped local model carries equiv %d", r.Date, r.Key, r.CostAPIEquivMicro)
			}
		}
	}
	if mapped == 0 || family == 0 {
		t.Fatalf("fixture coverage missing: model-mapped days %d, family-mapped days %d", mapped, family)
	}
}
