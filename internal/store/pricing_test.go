package store

// Pricing store behavior (M3 Task 2). Synthetic events are fine here:
// this tests SQL/derivation plumbing, not adapter parsing.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// Cost columns are derived, not payload: a re-insert with identical
// payload but different cost fields writes nothing; a genuine payload
// change re-stamps them.
func TestCostChangeIsNotAReplacement(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	e := event("m1", "r1", "claude-fable-5", "s1", ts, TokenSums{Input: 1000})
	cost := int64(10_000)
	e.CostUSDMicro, e.CostBasis, e.PriceSnapshot = &cost, "api_price", "snap-1"
	if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
		t.Fatal(err)
	}

	e2 := e
	newCost := int64(99_999)
	e2.CostUSDMicro, e2.PriceSnapshot = &newCost, "snap-2"
	stats, err := s.InsertBatch(ctx, []core.Event{e2}, testSource(1))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Inserted != 0 || stats.Replaced != 0 {
		t.Fatalf("cost-only difference triggered a write: %+v", stats)
	}
	var got int64
	var snap string
	if err := s.db.QueryRowContext(ctx, `SELECT cost_usd_micro, price_snapshot
		FROM usage_events WHERE id = ?`, e.ID).Scan(&got, &snap); err != nil {
		t.Fatal(err)
	}
	if got != 10_000 || snap != "snap-1" {
		t.Fatalf("stored cost changed without recompute: %d @ %s", got, snap)
	}
}

// RecomputePricing re-derives every cost column under the current
// snapshot — the only path that changes a historical cost. Tokens are
// untouched by construction; a second run is a no-op.
func TestRecomputePricing(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	priced := eventH("claude-code", "m1", "r1", "claude-fable-5", "s1", ts,
		TokenSums{Input: 1000, Output: 100})
	unknown := eventH("opencode", "m2", "r2", "deepseek-v4-flash", "s1",
		ts.Add(time.Minute), TokenSums{Input: 50})
	unknown.Provider = "deepseek"
	local := eventH("opencode", "m3", "r3", "qwen3.6-27b", "s1",
		ts.Add(2*time.Minute), TokenSums{Output: 7})
	local.Provider = "vllm"
	if _, err := s.InsertBatch(ctx, []core.Event{priced, unknown, local},
		testSource(3)); err != nil {
		t.Fatal(err)
	}
	// Pre-pricing database state (what migration 7 leaves behind).
	if _, err := s.db.ExecContext(ctx, `UPDATE usage_events SET
		cost_usd_micro=NULL, cost_basis=NULL, price_snapshot=NULL, price_rates=NULL`); err != nil {
		t.Fatal(err)
	}

	version, err := pricing.SnapshotVersion()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanPricing(ctx, version)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Events != 3 || plan.Unpriced != 3 {
		t.Fatalf("plan: %+v, want 3 events all unpriced", plan)
	}

	res, err := s.RecomputePricing(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repriced != 3 {
		t.Fatalf("repriced %d, want 3", res.Repriced)
	}
	type want struct {
		basis    string
		costNull bool
		cost     int64
	}
	wants := map[string]want{
		// 1000×$10 + 100×$50 per Mtok = 10000 + 5000 micro
		priced.ID:  {"api_price", false, 15_000},
		unknown.ID: {"unknown", true, 0},
		local.ID:   {"local", false, 0},
	}
	for id, w := range wants {
		var cost sql.NullInt64
		var basis, snap string
		if err := s.db.QueryRowContext(ctx, `SELECT cost_usd_micro,
			COALESCE(cost_basis,''), COALESCE(price_snapshot,'')
			FROM usage_events WHERE id = ?`, id).Scan(&cost, &basis, &snap); err != nil {
			t.Fatal(err)
		}
		if basis != w.basis || cost.Valid == w.costNull || (cost.Valid && cost.Int64 != w.cost) {
			t.Errorf("%s: basis=%s cost=%v, want %s/%d (null=%v)",
				id, basis, cost, w.basis, w.cost, w.costNull)
		}
		if snap != version {
			t.Errorf("%s: price_snapshot %q, want %q", id, snap, version)
		}
	}

	// Idempotent.
	res, err = s.RecomputePricing(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repriced != 0 {
		t.Fatalf("second run repriced %d, want 0", res.Repriced)
	}
}
