package parity

// M4 Task 5 acceptance over the real opencode fixture set: with the
// owner's actual override shape — flash rates as a partial patch plus
// flash-free declared free:true — every deepseek-v4-flash-free row
// bills $0 AND carries a nonzero API-equivalent resolved through the
// family's override patch. Before this milestone these rows stored
// NULL equivalents for two reasons at once (override-declared free
// computed no equivalent; resolution was snapshot-only) — the recorded
// known gap from the M3.1 report.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

func TestFlashFreeOverrideEquivalentFixtures(t *testing.T) {
	set := paritySets[2]
	if set.harness != "opencode" {
		t.Fatalf("parity set order changed: %q", set.harness)
	}

	ovPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(ovPath, []byte(`{
		"prices": {
			"deepseek-v4-flash": {
				"input_usd_per_mtok": "0.14",
				"output_usd_per_mtok": "0.28",
				"cache_read_usd_per_mtok": "0.0028"
			},
			"deepseek-v4-flash-free": { "free": true }
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(ovPath)
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "flashfree.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	machineDir := filepath.Join(set.fixtureBase, "gx10")
	if _, err := adapters.IngestBackfill(ctx, st, set.adapter, []adapters.Source{{
		Harness: set.harness, Root: set.root(t, machineDir), Machine: "gx10",
	}}, ov); err != nil {
		t.Fatal(err)
	}

	rows, err := st.DailyBy(ctx, time.UTC, "model", store.Filters{Harness: []string{"opencode"}})
	if err != nil {
		t.Fatal(err)
	}
	days := 0
	for _, r := range rows {
		if r.Key != "deepseek-v4-flash-free" {
			continue
		}
		days++
		if r.CostUSDMicro != 0 {
			t.Errorf("%s: flash-free billed %d micro, want 0 (owner-declared free)", r.Date, r.CostUSDMicro)
		}
		if r.CostAPIEquivMicro <= 0 {
			t.Errorf("%s: flash-free equivalent %d micro, want > 0 (resolved through the family's override patch)", r.Date, r.CostAPIEquivMicro)
		}
	}
	if days == 0 {
		t.Fatal("fixture coverage missing: no deepseek-v4-flash-free day rows")
	}

	// Per-event provenance: free_source override + family+override equiv.
	var rates []byte
	row := st.DB().QueryRowContext(ctx, `SELECT price_rates FROM usage_events
		WHERE model = 'deepseek-v4-flash-free' AND price_rates IS NOT NULL LIMIT 1`)
	if err := row.Scan(&rates); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		FreeSource  string `json:"free_source"`
		EquivSource string `json:"equiv_source"`
	}
	if err := json.Unmarshal(rates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.FreeSource != "override" || detail.EquivSource != "family+override" {
		t.Errorf("per-event provenance: %+v, want free_source=override equiv_source=family+override", detail)
	}
}
