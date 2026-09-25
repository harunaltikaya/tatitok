package parity

// Metered API-equivalent over the real fixture sets (owner ruling
// 2026-09-25): on basis api_price the API-equivalent IS the billed
// cost, for a new event and after recompute --pricing alike; local rows
// with no reference model stay NULL, and a plan_included event keeps
// the equivalent the billing path computes for the same tokens.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

func TestAPIPriceEquivalentFixtures(t *testing.T) {
	set := paritySets[2]
	if set.harness != "opencode" {
		t.Fatalf("parity set order changed: %q", set.harness)
	}
	ctx := context.Background()
	machineDir := filepath.Join(set.fixtureBase, "gx10")
	st := ingestIntoWith(t, set.adapter, []adapters.Source{{
		Harness: set.harness, Root: set.root(t, machineDir), Machine: "gx10",
	}})

	check := func(stage string) int64 {
		t.Helper()
		var metered, equal, local, localEquiv int64
		if err := st.DB().QueryRowContext(ctx, `SELECT
				COALESCE(SUM(cost_basis = 'api_price'), 0),
				COALESCE(SUM(cost_basis = 'api_price' AND cost_usd_micro IS NOT NULL
					AND cost_api_equiv_micro = cost_usd_micro), 0),
				COALESCE(SUM(cost_basis = 'local'), 0),
				COALESCE(SUM(cost_basis = 'local' AND cost_api_equiv_micro IS NOT NULL), 0)
			FROM usage_events`).Scan(&metered, &equal, &local, &localEquiv); err != nil {
			t.Fatal(err)
		}
		if metered == 0 || local == 0 {
			t.Fatalf("%s: fixture coverage missing: %d api_price rows, %d local rows", stage, metered, local)
		}
		if equal != metered {
			t.Errorf("%s: %d of %d api_price rows carry equivalent == cost", stage, equal, metered)
		}
		// No reference_models configured: the local equivalent stays NULL.
		if localEquiv != 0 {
			t.Errorf("%s: %d of %d local rows carry an equivalent", stage, localEquiv, local)
		}
		return metered
	}
	metered := check("ingest")

	// A database priced before the ruling: recompute --pricing restamps
	// exactly the metered rows and nothing else.
	if _, err := st.DB().ExecContext(ctx, `UPDATE usage_events
		SET cost_api_equiv_micro = NULL WHERE cost_basis = 'api_price'`); err != nil {
		t.Fatal(err)
	}
	res, err := st.RecomputePricing(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repriced != metered {
		t.Errorf("recompute repriced %d rows, want the %d api_price rows", res.Repriced, metered)
	}
	check("recompute")
}

func TestPlanEquivalentUnchangedFixtures(t *testing.T) {
	set := paritySets[0]
	if set.harness != "claude-code" {
		t.Fatalf("parity set order changed: %q", set.harness)
	}
	ctx := context.Background()
	machineDir := filepath.Join(set.fixtureBase, "gx10")
	src := []adapters.Source{{Harness: set.harness, Root: set.root(t, machineDir), Machine: "gx10"}}

	// Same fixture twice: metered (no plan) and plan-covered.
	metered := ingestIntoWith(t, set.adapter, src)
	ovPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(ovPath, []byte(`{
		"plans": [{"name": "claude-max", "matchers": [{"harness": "claude-code"}], "window": "5h"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(ovPath)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := store.Open(filepath.Join(t.TempDir(), "plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = planned.Close() }()
	if _, err := adapters.IngestBackfill(ctx, planned, set.adapter, src, ov); err != nil {
		t.Fatal(err)
	}

	type priced struct {
		basis       string
		cost, equiv sql.NullInt64
	}
	load := func(st *store.Store) map[string]priced {
		t.Helper()
		rows, err := st.DB().QueryContext(ctx,
			`SELECT id, cost_basis, cost_usd_micro, cost_api_equiv_micro FROM usage_events`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		out := map[string]priced{}
		for rows.Next() {
			var id string
			var p priced
			if err := rows.Scan(&id, &p.basis, &p.cost, &p.equiv); err != nil {
				t.Fatal(err)
			}
			out[id] = p
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	m, p := load(metered), load(planned)
	if len(m) != wantClaudeRows || len(p) != wantClaudeRows {
		t.Fatalf("rows: metered %d, planned %d, want %d", len(m), len(p), wantClaudeRows)
	}
	for id, pe := range p {
		me := m[id]
		if me.basis != "api_price" || !me.cost.Valid || me.equiv != me.cost {
			t.Fatalf("%s metered: basis %q cost %+v equiv %+v, want api_price with equiv == cost",
				id, me.basis, me.cost, me.equiv)
		}
		// The plan row bills $0 and keeps the equivalent computed at the
		// billing path's rates — the metered row's cost.
		if pe.basis != "plan_included" || pe.cost != (sql.NullInt64{Valid: true}) ||
			pe.equiv != me.cost {
			t.Fatalf("%s plan: basis %q cost %+v equiv %+v, want plan_included $0 with equiv %d",
				id, pe.basis, pe.cost, pe.equiv, me.cost.Int64)
		}
	}
}
