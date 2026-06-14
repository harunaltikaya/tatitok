package store

// Activity heatmap aggregation (M8 1L): a read-only VIEW bucketing events by
// (weekday, hour) in the query timezone. Synthetic events are fine here — this
// tests SQL bucketing + predicate plumbing, not adapter parsing. Aggregating
// from usage_events (not rollup_hourly) is exact for fractional-offset zones
// (Asia/Kolkata +5:30), which this test locks.

import (
	"context"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func TestActivity(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ist, err := time.LoadLocation("Asia/Kolkata") // +5:30, no DST
	if err != nil {
		t.Fatal(err)
	}

	insert := func(msgID, harness, provider string, ts time.Time, tok int64) {
		e := eventH(harness, msgID, "r-"+msgID, "m-"+msgID, "s1", ts, TokenSums{Input: tok})
		e.Provider = provider
		if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
			t.Fatal(err)
		}
	}
	// e1, e2 share IST hour 1; tFrac lands in IST hour 2 (fractional split);
	// e3 (codex, carrying reasoning) lands in IST hour 8.
	t1 := time.Date(2026, 6, 10, 20, 0, 0, 0, time.UTC)     // IST 2026-06-11 01:30 → hour 1
	t2 := time.Date(2026, 6, 10, 20, 15, 0, 0, time.UTC)    // IST 2026-06-11 01:45 → hour 1
	tFrac := time.Date(2026, 6, 10, 20, 45, 0, 0, time.UTC) // IST 2026-06-11 02:15 → hour 2
	t3 := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)      // IST 2026-06-11 08:30 → hour 8
	insert("e1", "claude-code", "anthropic", t1, 10)
	insert("e2", "claude-code", "anthropic", t2, 20)
	insert("eF", "claude-code", "anthropic", tFrac, 5)

	// e3 is a codex event carrying reasoning. codex's output already INCLUDES
	// reasoning (adapters/codex/codex.go), so the dashboard's canonical total
	// (web/src/api.ts totalTokens) sums only input+output+cache-write+cache-read
	// and EXCLUDES tokens_reasoning. Activity must match: a 5-field sum here
	// would double-count e3's 100 reasoning tokens (4 → 104).
	e3 := eventH("codex", "e3", "r-e3", "m-e3", "s1", t3, TokenSums{Input: 4})
	e3.Provider = "openai"
	reasoning := int64(100)
	e3.TokensReasoning = &reasoning
	if _, err := s.InsertBatch(ctx, []core.Event{e3}, testSource(1)); err != nil {
		t.Fatal(err)
	}

	get := func(bs []ActivityBucket, wd, hr int) (ActivityBucket, bool) {
		for _, b := range bs {
			if b.Weekday == wd && b.Hour == hr {
				return b, true
			}
		}
		return ActivityBucket{}, false
	}
	sum := func(bs []ActivityBucket) (evs, toks int64) {
		for _, b := range bs {
			evs += b.Events
			toks += b.Tokens
		}
		return
	}

	// Fractional-offset bucketing: events land in their IST LOCAL (weekday,
	// hour), NOT the UTC hour — the reason Activity aggregates from events.
	wd1, hr1 := int(t1.In(ist).Weekday()), t1.In(ist).Hour() // hr1 == 1, not 20
	if hr1 == t1.UTC().Hour() {
		t.Fatal("setup: pick a ts whose IST hour differs from its UTC hour")
	}
	buckets, err := s.Activity(ctx, ist, "", "", Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := get(buckets, wd1, hr1); !ok || b.Events != 2 || b.Tokens != 30 {
		t.Errorf("IST (%d,%d) = %+v, want 2 events / 30 tokens", wd1, hr1, b)
	}
	if _, ok := get(buckets, wd1, t1.UTC().Hour()); ok {
		t.Errorf("found a bucket at UTC hour %d — events were not bucketed in IST", t1.UTC().Hour())
	}

	// Fractional split lock: tFrac (20:45 UTC) lands in IST hour 2 under the
	// real +05:30 offset (02:15), but in hour 1 (01:45) under an incorrect
	// whole-hour +05:00. A distinct hour-2 bucket — with hour 1 still holding
	// only e1+e2 (asserted above) — proves the half-hour offset is honored.
	wdF, hrF := int(tFrac.In(ist).Weekday()), tFrac.In(ist).Hour() // hrF == 2
	if hrF != 2 {
		t.Fatalf("setup: tFrac IST hour = %d, want 2", hrF)
	}
	if b, ok := get(buckets, wdF, hrF); !ok || b.Events != 1 || b.Tokens != 5 {
		t.Errorf("IST fractional (%d,%d) = %+v, want 1 event / 5 tokens", wdF, hrF, b)
	}

	// Reasoning-exclusion lock: e3 (codex) has 4 input + 100 reasoning tokens.
	// Its bucket must report the 4-field canonical total (4), NOT 104 — the old
	// 5-field sum double-counted reasoning. Fails on the old code, passes here.
	wd3, hr3 := int(t3.In(ist).Weekday()), t3.In(ist).Hour()
	if b, ok := get(buckets, wd3, hr3); !ok || b.Events != 1 || b.Tokens != 4 {
		t.Errorf("codex bucket IST (%d,%d) = %+v, want 1 event / 4 tokens (reasoning 100 EXCLUDED)", wd3, hr3, b)
	}

	// Conservation: Σ buckets = total events (4) and total tokens (39 — the
	// 4-field canonical sum; e3's 100 reasoning tokens are EXCLUDED).
	if evs, toks := sum(buckets); evs != 4 || toks != 39 {
		t.Errorf("conservation: Σ buckets = %d events / %d tokens, want 4 / 39", evs, toks)
	}

	// eventsPredicate narrows the grid: provider=anthropic → e1, e2, tFrac
	// (the codex/openai e3 drops out).
	f, err := s.Activity(ctx, ist, "", "", Filters{Provider: []string{"anthropic"}})
	if err != nil {
		t.Fatal(err)
	}
	if evs, toks := sum(f); evs != 3 || toks != 35 {
		t.Errorf("filtered Σ = %d events / %d tokens, want 3 / 35", evs, toks)
	}

	// Range bounds the scan (all four are on IST 2026-06-11).
	if in, err := s.Activity(ctx, ist, "2026-06-11", "2026-06-11", Filters{}); err != nil {
		t.Fatal(err)
	} else if evs, _ := sum(in); evs != 4 {
		t.Errorf("in-range Σ = %d events, want 4", evs)
	}
	if out, err := s.Activity(ctx, ist, "2026-06-12", "2026-06-12", Filters{}); err != nil {
		t.Fatal(err)
	} else if len(out) != 0 {
		t.Errorf("out-of-range returned %d buckets, want 0", len(out))
	}

	// UTC (whole-hour) bucketing: t1 → UTC hour 20 (the unshifted case).
	if u, err := s.Activity(ctx, time.UTC, "", "", Filters{}); err != nil {
		t.Fatal(err)
	} else if _, ok := get(u, int(t1.Weekday()), 20); !ok {
		t.Error("UTC: no bucket at hour 20 (t1's UTC hour)")
	}
}
