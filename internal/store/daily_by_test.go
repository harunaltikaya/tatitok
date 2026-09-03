package store

// Machine dimension for stats --daily --by machine (M3 Task 4 extension):
// verifies that the machine column groups events correctly, that the
// dimension is wired through the store API, and that an unknown dimension
// still produces a loud error naming the supported set.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func TestDailyByMachine(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	// Two events on the same day but different machines.
	a := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10})
	a.Machine = "gx10"
	b := event("m2", "r2", "model-b", "s1", ts.Add(time.Minute), TokenSums{Input: 20, Output: 5})
	b.Machine = "gx11"
	if _, err := s.InsertBatch(ctx, []core.Event{a, b}, testSource(2)); err != nil {
		t.Fatal(err)
	}

	rows, err := s.DailyBy(ctx, time.UTC, "machine", Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("DailyBy machine: got %d rows, want 2 (one per machine): %+v", len(rows), rows)
	}
	// Keys arrive ordered by day then key (SQL ORDER BY day, key).
	if rows[0].Key != "gx10" || rows[0].Input != 10 {
		t.Errorf("row[0] = key=%q input=%d, want key=gx10 input=10", rows[0].Key, rows[0].Input)
	}
	if rows[1].Key != "gx11" || rows[1].Input != 20 || rows[1].Output != 5 {
		t.Errorf("row[1] = key=%q input=%d output=%d, want key=gx11 input=20 output=5",
			rows[1].Key, rows[1].Input, rows[1].Output)
	}
	// The day must be the UTC date of the event.
	if rows[0].Date != "2026-06-10" {
		t.Errorf("row[0].Date = %q, want 2026-06-10", rows[0].Date)
	}
}

func TestDailyByMachineFilterInteraction(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	a := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10})
	a.Machine = "gx10"
	a.Provider = "anthropic"
	b := event("m2", "r2", "model-b", "s1", ts, TokenSums{Input: 20})
	b.Machine = "gx11"
	b.Provider = "openai"
	c := event("m3", "r3", "model-a", "s1", ts, TokenSums{Input: 30})
	c.Machine = "gx10"
	c.Provider = "anthropic"
	if _, err := s.InsertBatch(ctx, []core.Event{a, b, c}, testSource(3)); err != nil {
		t.Fatal(err)
	}

	// Filtering by harness (claude-code, the default in eventH) restricts to
	// the events whose harness matches, while --by machine still groups by
	// machine within that filter.
	rows, err := s.DailyBy(ctx, time.UTC, "machine", Filters{Harness: []string{"claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	// All three events are claude-code (eventH default), so all three appear.
	// gx10 has m1 (10) + m3 (30) = 40; gx11 has m2 (20).
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	byKey := make(map[string]int64)
	for _, r := range rows {
		byKey[r.Key] = r.Input
	}
	if byKey["gx10"] != 40 || byKey["gx11"] != 20 {
		t.Errorf("machine breakdown = %v, want map[gx10:40 gx11:20]", byKey)
	}
}

func TestDailyByUnknownDimensionMentionsMachine(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	e := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 1})
	if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
		t.Fatal(err)
	}
	_, err := s.DailyBy(ctx, time.UTC, "nonexistent", Filters{})
	if err == nil {
		t.Fatal("unknown dimension did not error")
	}
	if !strings.Contains(err.Error(), "machine") {
		t.Errorf("error %q does not mention machine in the supported list", err.Error())
	}
}
