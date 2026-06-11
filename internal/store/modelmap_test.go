package store

// Model-normalization store behavior (M3 Task 1). Synthetic events are
// fine here: this tests SQL behavior, not adapter parsing.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/modelmap"
	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// Migration 6 over a v4 database: model_map table arrives seeded, events
// gain a NULL map_version, and model_family is NOT rewritten (historical
// values change only via recompute --model-map).
func TestMigration6ModelMapUpgrade(t *testing.T) {
	path := buildV4DB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	var rows, version int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(map_version) FROM model_map`).Scan(&rows, &version); err != nil {
		t.Fatal(err)
	}
	if rows != int64(len(modelmap.Entries())) || version != int64(modelmap.Version()) {
		t.Fatalf("model_map: %d rows @ v%d, want %d @ v%d",
			rows, version, len(modelmap.Entries()), modelmap.Version())
	}
	var stale, rewritten int64
	if err := s.db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(map_version IS NULL), 0),
			COALESCE(SUM(model_family IS NOT model), 0)
		FROM usage_events`).Scan(&stale, &rewritten); err != nil {
		t.Fatal(err)
	}
	if stale != 6 || rewritten != 0 {
		t.Fatalf("after migration: %d NULL map_version (want 6), %d rewritten families (want 0)",
			stale, rewritten)
	}
}

// A model_family / map_version difference alone never triggers a
// replacement: they are derived columns, and a map bump must not rewrite
// history through plain re-ingest.
func TestFamilyChangeIsNotAReplacement(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	e := event("m1", "r1", "deepseek-v4-flash-free", "s1", ts, TokenSums{Input: 1})
	e.ModelFamily = "deepseek-v4-flash" // normalized at first ingest
	src := testSource(1)
	src.MapVersion = 1
	if _, err := s.InsertBatch(ctx, []core.Event{e}, src); err != nil {
		t.Fatal(err)
	}

	// Same payload, different derived family (as if the map changed).
	e2 := e
	e2.ModelFamily = "deepseek-v4"
	src.MapVersion = 2
	stats, err := s.InsertBatch(ctx, []core.Event{e2}, src)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Inserted != 0 || stats.Replaced != 0 {
		t.Fatalf("derived-only difference triggered a write: %+v", stats)
	}
	var family string
	var mapVersion int64
	if err := s.db.QueryRowContext(ctx, `SELECT model_family, map_version
		FROM usage_events WHERE id = ?`, e.ID).Scan(&family, &mapVersion); err != nil {
		t.Fatal(err)
	}
	if family != "deepseek-v4-flash" || mapVersion != 1 {
		t.Fatalf("stored derived columns changed without recompute: %q @ v%d", family, mapVersion)
	}

	// A genuine payload change still replaces — and re-stamps the derived
	// columns under the current map.
	e3 := e2
	e3.TokensOutput = 7
	if stats, err = s.InsertBatch(ctx, []core.Event{e3}, src); err != nil {
		t.Fatal(err)
	}
	if stats.Replaced != 1 {
		t.Fatalf("genuine payload change did not replace: %+v", stats)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT model_family, map_version
		FROM usage_events WHERE id = ?`, e.ID).Scan(&family, &mapVersion); err != nil {
		t.Fatal(err)
	}
	if family != "deepseek-v4" || mapVersion != 2 {
		t.Fatalf("replacement did not re-stamp derived columns: %q @ v%d", family, mapVersion)
	}
}

// RecomputeModelMap is the only path that changes historical
// model_family: it normalizes through the model_map table, stamps
// map_version, never touches the raw model, and is idempotent.
func TestRecomputeModelMap(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	batch := []core.Event{
		event("m1", "r1", "deepseek-v4-flash-free", "s1", ts, TokenSums{Input: 1}),
		event("m2", "r2", "qwen3.6-35b-nvfp4-tecnigmaai", "s1", ts.Add(time.Minute), TokenSums{Output: 2}),
		event("m3", "r3", "unknown-model", "s1", ts.Add(2*time.Minute), TokenSums{CacheRead: 3}),
	}
	if _, err := s.InsertBatch(ctx, batch, testSource(3)); err != nil {
		t.Fatal(err)
	}
	// Pre-normalization state: family = model, no map_version (what a
	// pre-migration-6 database looks like).
	if _, err := s.db.ExecContext(ctx,
		`UPDATE usage_events SET model_family = model, map_version = NULL`); err != nil {
		t.Fatal(err)
	}

	plan, err := s.PlanModelMap(ctx, modelmap.Version())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Events != 3 || plan.Stale != 3 || plan.FamilyChanges != 2 {
		t.Fatalf("plan: %+v, want 3 events / 3 stale / 2 family changes", plan)
	}

	restamped, changed, repriced, err := s.RecomputeModelMap(ctx, modelmap.Version(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if restamped != 3 || changed != 2 || repriced != 2 {
		t.Fatalf("recompute: restamped=%d changed=%d repriced=%d, want 3/2/2",
			restamped, changed, repriced)
	}

	want := map[string]struct{ model, family string }{
		batch[0].ID: {"deepseek-v4-flash-free", "deepseek-v4-flash"},
		batch[1].ID: {"qwen3.6-35b-nvfp4-tecnigmaai", "qwen3.6-35b"},
		batch[2].ID: {"unknown-model", "unknown-model"}, // passthrough
	}
	for id, w := range want {
		var model, family string
		var mv sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT model, model_family,
			map_version FROM usage_events WHERE id = ?`, id).
			Scan(&model, &family, &mv); err != nil {
			t.Fatal(err)
		}
		if model != w.model {
			t.Errorf("%s: raw model changed to %q — must be immutable", id, model)
		}
		if family != w.family || !mv.Valid || mv.Int64 != int64(modelmap.Version()) {
			t.Errorf("%s: family=%q map_version=%v, want %q @ v%d",
				id, family, mv, w.family, modelmap.Version())
		}
	}

	// Idempotent: a second run finds nothing.
	plan, err = s.PlanModelMap(ctx, modelmap.Version())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stale != 0 || plan.FamilyChanges != 0 {
		t.Fatalf("second plan not empty: %+v", plan)
	}
}

// The ordering scenario from M3.1 finding 2: model_family feeds price
// resolution, so running recompute --pricing FIRST and --model-map
// SECOND used to exit 0 with costs still derived from the old family.
// --model-map now reprices the changed-family rows itself: whatever
// order the recomputes run in, exit 0 means the derived columns are
// mutually consistent.
func TestRecomputeModelMapRepricesChangedFamilies(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	// Price exists only under the FAMILY key, via the override file.
	ovPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(ovPath, []byte(`{
		"prices": {"deepseek-v4-flash": {
			"input_usd_per_mtok": "0.28", "output_usd_per_mtok": "0.42"
		}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(ovPath)
	if err != nil {
		t.Fatal(err)
	}

	moved := eventH("opencode", "m1", "r1", "deepseek-v4-flash-free", "s1", ts,
		TokenSums{Input: 1000, Output: 100})
	moved.Provider = "opencode"
	stays := eventH("opencode", "m2", "r2", "unknown-model", "s1",
		ts.Add(time.Minute), TokenSums{Input: 50})
	stays.Provider = "opencode"
	if _, err := s.InsertBatch(ctx, []core.Event{moved, stays}, testSource(2)); err != nil {
		t.Fatal(err)
	}
	// Stale-map state: family = raw model, never normalized.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE usage_events SET model_family = model, map_version = NULL,
		 cost_usd_micro=NULL, cost_basis=NULL, price_snapshot=NULL, price_rates=NULL`); err != nil {
		t.Fatal(err)
	}

	// Recompute order under test: pricing FIRST (stale family resolves
	// nothing — basis unknown), model-map SECOND.
	if _, err := s.RecomputePricing(ctx, ov); err != nil {
		t.Fatal(err)
	}
	var basis string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(cost_basis,'')
		FROM usage_events WHERE id = ?`, moved.ID).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if basis != "unknown" {
		t.Fatalf("precondition: stale family priced as %q, want unknown", basis)
	}

	_, changed, repriced, err := s.RecomputeModelMap(ctx, modelmap.Version(), ov)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || repriced != 1 {
		t.Fatalf("changed=%d repriced=%d, want 1/1", changed, repriced)
	}

	// The moved row is now consistent with its NEW family: override hit.
	var cost sql.NullInt64
	var snap string
	if err := s.db.QueryRowContext(ctx, `SELECT cost_usd_micro,
		COALESCE(cost_basis,''), COALESCE(price_snapshot,'')
		FROM usage_events WHERE id = ?`, moved.ID).Scan(&cost, &basis, &snap); err != nil {
		t.Fatal(err)
	}
	// 1000×$0.28 + 100×$0.42 per Mtok = 280 + 42 micro-USD.
	if basis != "api_price" || snap != "override" || !cost.Valid || cost.Int64 != 322 {
		t.Fatalf("changed-family row not repriced: basis=%s snap=%s cost=%v", basis, snap, cost)
	}

	// A follow-up recompute --pricing finds nothing left to do: the exit-0
	// invariant held without it.
	res, err := s.RecomputePricing(ctx, ov)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repriced != 0 {
		t.Fatalf("derived columns were left inconsistent: --pricing rewrote %d rows", res.Repriced)
	}
}
