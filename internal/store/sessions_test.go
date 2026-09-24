package store

// Sessions panel query (SessionsInRange) and the session filter.
// Synthetic events are fine here: this tests SQL grouping, ranging and
// predicate plumbing, not adapter parsing.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func seedSessionEvents(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	mk := func(harness, msgID, session, project string, ts time.Time, in, equiv int64) {
		e := eventH(harness, msgID, "r-"+msgID, "m", session, ts, TokenSums{Input: in, Output: 1, CacheWrite: 2, CacheRead: 3})
		e.Project = project
		cost := int64(0)
		e.CostUSDMicro, e.CostBasis, e.PriceSnapshot = &cost, "plan_included", "test"
		e.CostAPIEquivMicro = &equiv
		if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
			t.Fatal(err)
		}
	}
	day := func(d, h, m int) time.Time { return time.Date(2026, 6, d, h, m, 0, 0, time.UTC) }
	// s1 spans 06-09..06-11 UTC and changes project mid-session.
	mk("claude-code", "a1", "s1", "/p/early", day(9, 12, 0), 10, 100)
	mk("claude-code", "a2", "s1", "/p/late", day(10, 9, 0), 20, 200)
	mk("claude-code", "a3", "s1", "/p/late", day(11, 12, 0), 40, 400)
	// s2 (codex) on 06-10; 22:30 UTC is 06-11 01:30 in Europe/Istanbul.
	mk("codex", "b1", "s2", "/p/x", day(10, 8, 0), 1, 300)
	mk("codex", "b2", "s2", "/p/x", day(10, 22, 30), 2, 300)
	// s3 ties s2 on value inside 06-10 and starts later.
	mk("claude-code", "c1", "s3", "/p/y", day(10, 15, 0), 4, 200)
	// No session id: groups under "".
	mk("pi", "d1", "", "", day(10, 16, 0), 8, 50)
	return s
}

func TestSessionsInRange(t *testing.T) {
	s := seedSessionEvents(t)
	ctx := context.Background()
	ids := func(rows []SessionSummary) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.Harness + "/" + r.Session
		}
		return out
	}

	// Range 06-10 (UTC): only in-range events count, so s1 is its a2 alone.
	rows, total, err := s.SessionsInRange(ctx, time.UTC, "2026-06-10", "2026-06-10", Filters{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	// s2 600, then s1 and s3 at 200 (s1 starts first), then the "" session.
	want := []string{"codex/s2", "claude-code/s1", "claude-code/s3", "pi/"}
	if got := ids(rows); len(got) != len(want) || total != 4 {
		t.Fatalf("06-10: got %v total %d, want %v total 4", got, total, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("06-10 order: got %v, want %v", got, want)
			}
		}
	}
	s1 := rows[1]
	if s1.Events != 1 || s1.Input != 20 || s1.Output != 1 || s1.CacheWrite != 2 ||
		s1.CacheRead != 3 || s1.CostAPIEquivMicro != 200 || s1.Project != "/p/late" ||
		s1.FirstTS != "2026-06-10T09:00:00Z" || s1.LastTS != "2026-06-10T09:00:00Z" {
		t.Errorf("s1 in 06-10 = %+v, want its a2 event only", s1)
	}
	s2 := rows[0]
	if s2.Events != 2 || s2.Input != 3 || s2.CostAPIEquivMicro != 600 ||
		s2.FirstTS != "2026-06-10T08:00:00Z" || s2.LastTS != "2026-06-10T22:30:00Z" {
		t.Errorf("s2 in 06-10 = %+v", s2)
	}
	if rows[3].Session != "" || rows[3].Project != "" || rows[3].Input != 8 {
		t.Errorf(`"" session row = %+v`, rows[3])
	}

	// Whole range: s1 sums all three events; its project is its first.
	rows, _, err = s.SessionsInRange(ctx, time.UTC, "", "", Filters{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Session != "s1" || rows[0].Events != 3 || rows[0].Input != 70 ||
		rows[0].CostAPIEquivMicro != 700 || rows[0].Project != "/p/early" ||
		rows[0].FirstTS != "2026-06-09T12:00:00Z" || rows[0].LastTS != "2026-06-11T12:00:00Z" {
		t.Errorf("s1 over all days = %+v", rows[0])
	}

	// Zone bound: 06-11 in Istanbul (+03:00) takes s2's 22:30 UTC event
	// and s1's a3; in UTC it holds a3 alone.
	ist, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err = s.SessionsInRange(ctx, ist, "2026-06-11", "2026-06-11", Filters{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rows); total != 2 || got[0] != "claude-code/s1" || got[1] != "codex/s2" ||
		rows[1].Events != 1 || rows[1].Input != 2 {
		t.Errorf("Istanbul 06-11: %v %+v, want s1 then s2's 22:30 event", got, rows)
	}
	rows, total, err = s.SessionsInRange(ctx, time.UTC, "2026-06-11", "2026-06-11", Filters{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || rows[0].Session != "s1" {
		t.Errorf("UTC 06-11: %v, want s1 alone", ids(rows))
	}

	// A filter restricts the events before grouping.
	rows, total, err = s.SessionsInRange(ctx, time.UTC, "2026-06-10", "2026-06-10",
		Filters{Harness: []string{"claude-code"}}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rows); total != 2 || got[0] != "claude-code/s1" || got[1] != "claude-code/s3" {
		t.Errorf("harness filter: %v total %d", got, total)
	}

	// The session filter, "" included.
	rows, total, err = s.SessionsInRange(ctx, time.UTC, "", "", Filters{Session: []string{"s2", ""}}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rows); total != 2 || got[0] != "codex/s2" || got[1] != "pi/" {
		t.Errorf("session filter: %v total %d", got, total)
	}

	// The cap cuts rows after sorting; total counts them all.
	rows, total, err = s.SessionsInRange(ctx, time.UTC, "", "", Filters{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rows); total != 4 || len(rows) != 2 || got[0] != "claude-code/s1" || got[1] != "codex/s2" {
		t.Errorf("cap 2: %v total %d, want [s1 s2] total 4", got, total)
	}

	// Empty range: [] (not nil) and total 0.
	rows, total, err = s.SessionsInRange(ctx, time.UTC, "2026-07-01", "2026-07-01", Filters{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 || total != 0 {
		t.Errorf("empty range: %v total %d", rows, total)
	}
}

// TestSessionFilter: the session filter is an events-path dimension —
// not rollup-servable, served from events by DailyServed, and a session's
// daily totals equal its SessionsInRange row.
func TestSessionFilter(t *testing.T) {
	s := seedSessionEvents(t)
	ctx := context.Background()
	f := Filters{Session: []string{"s1"}}
	if f.RollupServable() || f.IsZero() {
		t.Fatal("a session filter must be non-zero and not rollup-servable")
	}
	if _, err := s.DailyFromRollups(ctx, f); err == nil {
		t.Fatal("rollups served a session filter")
	}
	for _, tz := range []string{"UTC", "Europe/Istanbul", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			t.Fatal(err)
		}
		rows, source, err := s.DailyServed(ctx, loc, f)
		if err != nil {
			t.Fatal(err)
		}
		if source != "events" {
			t.Errorf("%s: session filter served from %q, want events", tz, source)
		}
		want, err := s.Daily(ctx, loc, f)
		if err != nil {
			t.Fatal(err)
		}
		gj, _ := json.Marshal(rows)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			t.Errorf("%s: DailyServed(session) != Daily(session)", tz)
		}
	}
	rows, err := s.Daily(ctx, time.UTC, f)
	if err != nil {
		t.Fatal(err)
	}
	var sum TokenSums
	var equiv int64
	for _, r := range rows {
		sum.add(r.TokenSums)
		equiv += r.CostAPIEquivMicro
	}
	sess, _, err := s.SessionsInRange(ctx, time.UTC, "", "", f, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess) != 1 || sess[0].TokenSums != sum || sess[0].CostAPIEquivMicro != equiv {
		t.Errorf("session row %+v != daily sums %+v / %d", sess, sum, equiv)
	}
	// Daily by harness under the session filter holds the one harness.
	by, err := s.DailyBy(ctx, time.UTC, "harness", f)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range by {
		if r.Key != "claude-code" {
			t.Errorf("by-harness under session s1 has key %q", r.Key)
		}
	}
}
