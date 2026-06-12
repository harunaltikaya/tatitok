package parity

// M5 Task 2 over the real claude-code fixture set: a plan covering the
// harness flips every event to basis plan_included with $0 billed and
// the full would-have-cost equivalent stored, and the window math
// partitions the fixture's real timestamps into well-formed rolling
// windows. The window COUNT is pinned as a golden over the committed
// fixtures (recomputed only when fixtures change); everything else is
// asserted as invariants — this is tatitok-native window math, NOT a
// ccusage-blocks parity surface.

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

func TestPlanIncludedWindowsFixtures(t *testing.T) {
	set := paritySets[0]
	if set.harness != "claude-code" {
		t.Fatalf("parity set order changed: %q", set.harness)
	}

	ovPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(ovPath, []byte(`{
		"plans": [{
			"name": "claude-max",
			"matchers": [{"harness": "claude-code"}],
			"window": "5h",
			"monthly_price_usd": "200.00"
		}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(ovPath)
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "plan.db"))
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

	// Every claude-code event is plan-covered: $0 billed, basis
	// plan_included, plan name in the provenance.
	var total, planRows, zeroCost int64
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(cost_basis = 'plan_included'), 0),
			COALESCE(SUM(cost_usd_micro = 0), 0)
		FROM usage_events`).Scan(&total, &planRows, &zeroCost); err != nil {
		t.Fatal(err)
	}
	if total == 0 || planRows != total || zeroCost != total {
		t.Fatalf("plan coverage: %d events, %d plan_included, %d zero-cost", total, planRows, zeroCost)
	}
	var rates []byte
	if err := st.DB().QueryRowContext(ctx,
		`SELECT price_rates FROM usage_events LIMIT 1`).Scan(&rates); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Plan        string `json:"plan"`
		EquivSource string `json:"equiv_source"`
	}
	if err := json.Unmarshal(rates, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Plan != "claude-max" || detail.EquivSource != "billing" {
		t.Fatalf("plan provenance: %+v", detail)
	}
	// Anthropic models price from the snapshot — the equivalent must be
	// present and positive wherever any tokens flowed.
	var unpriced int64
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events
		WHERE cost_api_equiv_micro IS NULL`).Scan(&unpriced); err != nil {
		t.Fatal(err)
	}
	if unpriced != 0 {
		t.Fatalf("%d plan events without an equivalent (snapshot gap in the fixture set?)", unpriced)
	}

	// Window math over the fixture's real timestamps.
	events, err := st.PlanIncludedEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(events)) != total {
		t.Fatalf("PlanIncludedEvents = %d rows, want %d", len(events), total)
	}
	we := make([]pricing.WindowEvent, len(events))
	for i, e := range events {
		we[i] = pricing.WindowEvent{TS: e.TS, Input: e.Input, Output: e.Output,
			CacheWrite: e.CacheWrite, CacheRead: e.CacheRead,
			EquivMicro: e.EquivMicro, Unpriced: e.Unpriced}
	}
	windows := pricing.PlanWindows(we, 5*time.Hour)

	// Invariants: ascending, non-overlapping, hour-floored starts, 5h
	// spans, every event accounted for, equivalents conserved.
	var wEvents, wEquiv int64
	for i, w := range windows {
		if w.Start.Minute() != 0 || w.Start.Second() != 0 || w.Start.Nanosecond() != 0 {
			t.Errorf("window %d start %s not hour-floored", i, w.Start)
		}
		if !w.End.Equal(w.Start.Add(5 * time.Hour)) {
			t.Errorf("window %d span %s..%s is not 5h", i, w.Start, w.End)
		}
		if i > 0 && windows[i-1].End.After(w.Start) {
			t.Errorf("windows %d and %d overlap", i-1, i)
		}
		if w.Events == 0 {
			t.Errorf("window %d is empty — windows only open on events", i)
		}
		wEvents += w.Events
		wEquiv += w.EquivMicro
	}
	if wEvents != total {
		t.Fatalf("windows hold %d events, fixture has %d", wEvents, total)
	}
	var dbEquiv int64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_api_equiv_micro),0) FROM usage_events`).Scan(&dbEquiv); err != nil {
		t.Fatal(err)
	}
	if wEquiv != dbEquiv {
		t.Fatalf("windows hold %d micro equivalent, store has %d", wEquiv, dbEquiv)
	}

	// Golden pin over the committed fixture set: the partition itself.
	// Recompute only when the fixture set changes.
	// 7 windows over the gx10 fixture span (first opens 2026-06-07T22:00Z,
	// last 2026-06-10T14:00Z).
	const wantWindows = 7
	if len(windows) != wantWindows {
		t.Fatalf("fixture partition: %d windows (golden pin says %d) — first window %s, last %s",
			len(windows), wantWindows,
			windows[0].Start.Format(time.RFC3339), windows[len(windows)-1].Start.Format(time.RFC3339))
	}
}
