package store

// Facet filter semantics (M5 Task 3): OR within a dimension, AND
// across dimensions; unknown values match nothing; rollup-served
// filtered queries equal exact event aggregation wherever the rollup
// grain can serve. Synthetic events are fine here: this tests SQL
// predicate plumbing, not adapter parsing.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func seedFilterEvents(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	mk := func(harness, msgID, provider, model, project, basis string, in int64) {
		e := eventH(harness, msgID, "r-"+msgID, model, "s1", ts, TokenSums{Input: in})
		e.Provider = provider
		e.Project = project
		cost := int64(0)
		e.CostUSDMicro, e.CostBasis, e.PriceSnapshot = &cost, basis, "test"
		if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
			t.Fatal(err)
		}
	}
	mk("claude-code", "m1", "anthropic", "claude-fable-5", "proj-a", "plan_included", 1)
	mk("claude-code", "m2", "anthropic", "claude-sonnet-4-6", "proj-b", "plan_included", 2)
	mk("codex", "m3", "openai", "gpt-5.5", "proj-a", "plan_included", 4)
	mk("opencode", "m4", "deepseek", "deepseek-v4-pro", "proj-b", "api_price", 8)
	mk("opencode", "m5", "vllm", "qwen3.6-27b", "proj-a", "local", 16)
	return s
}

func dailyInput(t *testing.T, rows []DailyRow) int64 {
	t.Helper()
	var n int64
	for _, r := range rows {
		n += r.Input
	}
	return n
}

func TestFilterSemantics(t *testing.T) {
	s := seedFilterEvents(t)
	ctx := context.Background()
	cases := []struct {
		name string
		f    Filters
		want int64 // summed input tokens of matching events
	}{
		{"unconstrained", Filters{}, 31},
		{"single harness", Filters{Harness: []string{"claude-code"}}, 3},
		{"OR within dimension", Filters{Harness: []string{"claude-code", "codex"}}, 7},
		{"AND across dimensions", Filters{Harness: []string{"claude-code", "codex"}, Project: []string{"proj-a"}}, 5},
		{"basis filter", Filters{Basis: []string{"plan_included"}}, 7},
		{"basis OR", Filters{Basis: []string{"api_price", "local"}}, 24},
		{"model matches raw column", Filters{Model: []string{"gpt-5.5", "qwen3.6-27b"}}, 20},
		{"provider AND basis", Filters{Provider: []string{"deepseek", "vllm"}, Basis: []string{"local"}}, 16},
		{"unknown value matches nothing", Filters{Provider: []string{"no-such-provider"}}, 0},
		{"unknown value ORed with known", Filters{Provider: []string{"no-such-provider", "openai"}}, 4},
	}
	for _, c := range cases {
		rows, err := s.Daily(ctx, time.UTC, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if got := dailyInput(t, rows); got != c.want {
			t.Errorf("%s: input sum %d, want %d", c.name, got, c.want)
		}
		// Rollup-served equals exact event aggregation wherever the
		// grain can serve (basis is the one dimension it lacks).
		if c.f.RollupServable() {
			rolled, err := s.DailyFromRollups(ctx, c.f)
			if err != nil {
				t.Fatal(err)
			}
			dj, _ := json.Marshal(rows)
			rj, _ := json.Marshal(rolled)
			if string(dj) != string(rj) {
				t.Errorf("%s: rollup-served filtered daily != direct aggregation", c.name)
			}
		}
		// DailyBy under the same filter sums to the same totals.
		byRows, err := s.DailyBy(ctx, time.UTC, "harness", c.f)
		if err != nil {
			t.Fatal(err)
		}
		var bySum int64
		for _, r := range byRows {
			bySum += r.Input
		}
		if bySum != c.want {
			t.Errorf("%s: by-harness sum %d, want %d", c.name, bySum, c.want)
		}
	}

	// Basis filters cannot be rollup-served — loud error, never a wrong
	// answer.
	if _, err := s.DailyFromRollups(ctx, Filters{Basis: []string{"local"}}); err == nil {
		t.Fatal("rollups served a basis filter")
	}
}

func TestFacets(t *testing.T) {
	s := seedFilterEvents(t)
	facets, err := s.Facets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, dim := range []string{"harness", "provider", "model", "project", "basis"} {
		vals := facets[dim]
		if len(vals) == 0 {
			t.Errorf("facet %s is empty", dim)
			continue
		}
		// Counts over a dimension sum to the event total.
		var n int64
		for _, v := range vals {
			n += v.Events
		}
		if n != 5 {
			t.Errorf("facet %s counts sum to %d, want 5", dim, n)
		}
	}
	if len(facets["harness"]) != 3 || len(facets["basis"]) != 3 || len(facets["project"]) != 2 {
		t.Errorf("facet cardinalities wrong: harness=%d basis=%d project=%d",
			len(facets["harness"]), len(facets["basis"]), len(facets["project"]))
	}
}
