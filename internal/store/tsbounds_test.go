package store

// tsBounds is a pure pre-filter: it must never exclude an event whose
// LOCAL day is in range, for any zone offset, and its text comparison
// must agree with the stored RFC3339Nano UTC ts format (fractional
// seconds included).

import (
	"context"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func TestTsBoundsText(t *testing.T) {
	where, args := tsBounds("2026-10-09", "2026-10-15")
	if where != " AND ts >= ? AND ts < ?" {
		t.Fatalf("where = %q", where)
	}
	if len(args) != 2 || args[0] != "2026-10-08" || args[1] != "2026-10-17" {
		t.Fatalf("args = %v, want [2026-10-08 2026-10-17]", args)
	}
	// Text order against the stored format: a fractional ts on the lower
	// bound's day is INSIDE (a "T…" suffix sorts after the bare date),
	// and the last instant before the upper bound is inside too.
	for _, ts := range []string{"2026-10-08T00:00:00.123Z", "2026-10-08T00:00:00Z", "2026-10-16T23:59:59.999999999Z"} {
		if ts < "2026-10-08" || ts >= "2026-10-17" {
			t.Fatalf("%s falls outside the text bounds", ts)
		}
	}
	if w, a := tsBounds("", ""); w != "" || len(a) != 0 {
		t.Fatalf("open range: %q %v", w, a)
	}
	if w, a := tsBounds("garbage", "2026-10-15"); w != " AND ts < ?" || len(a) != 1 {
		t.Fatalf("bad from: %q %v", w, a)
	}
}

// Extreme offsets: an event that is local day D in UTC+14 (Kiritimati)
// sits at D-1 10:00Z; in UTC-12 (Etc/GMT+12) at D+1 11:59Z. Both must
// still be counted for from=to=D by the bounded queries, and excluded
// for the neighbouring days — exactly as the unbounded predicate decides.
func TestTsBoundsNeverExcludesLocalDayMembers(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	kir, _ := time.LoadLocation("Pacific/Kiritimati")
	west, _ := time.LoadLocation("Etc/GMT+12")
	early := event("m1", "r1", "model-a", "s1", time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC), TokenSums{Input: 1})  // 06-12 00:00 +14
	late := event("m2", "r2", "model-a", "s2", time.Date(2026, 6, 13, 11, 59, 0, 0, time.UTC), TokenSums{Input: 10}) // 06-12 23:59 -12
	if _, err := s.InsertBatch(ctx, []core.Event{early, late}, testSource(2)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		tz        *time.Location
		from, to  string
		wantInput int64
	}{
		{kir, "2026-06-12", "2026-06-12", 1}, {kir, "2026-06-11", "2026-06-11", 0}, {kir, "2026-06-13", "2026-06-14", 10},
		{west, "2026-06-12", "2026-06-12", 10}, {west, "2026-06-13", "2026-06-13", 0}, {west, "2026-06-10", "2026-06-11", 1},
	}
	for _, c := range cases {
		rows, err := s.DailyBy(ctx, c.tz, "harness", c.from, c.to, Filters{})
		if err != nil {
			t.Fatal(err)
		}
		var got int64
		for _, r := range rows {
			if r.Date >= c.from && r.Date <= c.to { // the caller's exact range filter
				got += r.Input
			}
		}
		if got != c.wantInput {
			t.Errorf("DailyBy %s [%s,%s]: input %d, want %d", c.tz, c.from, c.to, got, c.wantInput)
		}
		buckets, err := s.Activity(ctx, c.tz, c.from, c.to, Filters{})
		if err != nil {
			t.Fatal(err)
		}
		got = 0
		for _, b := range buckets {
			got += b.Tokens
		}
		if got != c.wantInput {
			t.Errorf("Activity %s [%s,%s]: tokens %d, want %d", c.tz, c.from, c.to, got, c.wantInput)
		}
		sess, _, err := s.SessionsInRange(ctx, c.tz, c.from, c.to, Filters{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		got = 0
		for _, ss := range sess {
			got += ss.Input
		}
		if got != c.wantInput {
			t.Errorf("Sessions %s [%s,%s]: input %d, want %d", c.tz, c.from, c.to, got, c.wantInput)
		}
	}
}
