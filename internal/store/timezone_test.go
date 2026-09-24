package store

// Timezone-correct serving tests (M6 Task 2). Synthetic core.Event
// values are fine — this exercises the store's path selection and SQL
// bucketing, not adapter parsing. tzdata is
// registered for the test binary by store_test.go's blank import.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func mustTime(t *testing.T, rfc string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, rfc)
	if err != nil {
		t.Fatalf("parse %q: %v", rfc, err)
	}
	return ts
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %q: %v (embedded tzdata missing?)", name, err)
	}
	return loc
}

// tzTestEvents seeds events deliberately straddling UTC-day boundaries
// for a +09 zone (so local bucketing actually shifts days) and spanning
// the America/New_York spring-forward day 2026-03-08 (a 23-hour local
// day across the DST transition).
func tzTestEvents(t *testing.T, s *Store) {
	t.Helper()
	cost := func(e core.Event, micro int64) core.Event {
		e.CostUSDMicro = &micro
		e.CostBasis, e.PriceSnapshot = "api_price", "snap-test"
		return e
	}
	evs := []core.Event{
		// Near a UTC-day boundary: 22:30Z is 06-13 07:30 in Tokyo (+09).
		cost(event("a", "1", "model-a", "s1", mustTime(t, "2026-06-12T22:30:00Z"), TokenSums{Input: 100, Output: 10, CacheRead: 5}), 1200),
		cost(event("b", "2", "model-a", "s1", mustTime(t, "2026-06-12T10:00:00Z"), TokenSums{Input: 50, Output: 5}), 600),
		event("c", "3", "model-b", "s1", mustTime(t, "2026-06-13T01:00:00Z"), TokenSums{Input: 7, Output: 1}), // unpriced
		// America/New_York DST spring-forward day (2026-03-08, EST→EDT).
		cost(eventH("codex", "d", "4", "model-a", "s2", mustTime(t, "2026-03-08T06:30:00Z"), TokenSums{Input: 3, Output: 9}), 90),
		cost(eventH("codex", "e", "5", "model-a", "s2", mustTime(t, "2026-03-08T23:00:00Z"), TokenSums{Input: 11, Output: 2}), 110),
		// 03-09 03:30Z is still NY 03-08 23:30 (EDT, -04) — the day's tail.
		cost(eventH("codex", "f", "6", "model-b", "s2", mustTime(t, "2026-03-09T03:30:00Z"), TokenSums{Input: 4, Output: 6}), 40),
	}
	if _, err := s.InsertBatch(context.Background(), evs, testSource(len(evs))); err != nil {
		t.Fatal(err)
	}
}

func TestWholeHourZone(t *testing.T) {
	lo := mustTime(t, "2026-01-01T00:00:00Z")
	hi := mustTime(t, "2026-12-31T23:59:59Z")
	cases := []struct {
		zone string
		want bool
	}{
		{"UTC", true},
		{"Asia/Tokyo", true},           // +09:00, no DST
		{"Europe/Istanbul", true},      // +03:00, no DST since 2016
		{"America/New_York", true},     // -05:00 / -04:00, whole-hour DST
		{"Europe/Berlin", true},        // +01:00 / +02:00, whole-hour DST
		{"Pacific/Apia", true},         // +13:00 date-line, whole-hour
		{"Asia/Kolkata", false},        // +05:30, fractional
		{"Asia/Kathmandu", false},      // +05:45, fractional
		{"Australia/Lord_Howe", false}, // +10:30 / +11:00, half-hour DST
	}
	for _, c := range cases {
		if got := wholeHourZone(mustLoad(t, c.zone), lo, hi); got != c.want {
			t.Errorf("wholeHourZone(%s) = %v, want %v", c.zone, got, c.want)
		}
	}
}

// F3: servability is decided by where the local-day BOUNDARY lands in
// UTC, not by the offset at a (possibly normalized) midnight. A
// whole-hour DST zone stays servable across both transition directions
// (the boundary remains on a UTC hour); a fractional zone is unservable
// even over a single day.
func TestWholeHourZoneBoundaryPlacement(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	spring := []struct{ lo, hi string }{
		{"2026-03-07T00:00:00Z", "2026-03-09T23:59:59Z"}, // around spring-forward 03-08
		{"2026-10-31T00:00:00Z", "2026-11-02T23:59:59Z"}, // around fall-back 11-01
	}
	for _, s := range spring {
		if !wholeHourZone(ny, mustTime(t, s.lo), mustTime(t, s.hi)) {
			t.Errorf("America/New_York DST span %s..%s should stay servable (boundary on a UTC hour)", s.lo, s.hi)
		}
	}
	// A fractional zone: even one day is unservable (boundary at :30).
	if wholeHourZone(mustLoad(t, "Asia/Kolkata"), mustTime(t, "2026-06-12T00:00:00Z"), mustTime(t, "2026-06-12T23:59:59Z")) {
		t.Error("Asia/Kolkata single day should be unservable (boundary at :30 past a UTC hour)")
	}

	// Shifted-midnight case (M6 Codex F3 coverage): Asia/Colombo moved its
	// clocks AT midnight in 1996 (the +05:30↔+06:30 changes), so the
	// local-day boundary lands mid-UTC-hour — the boundary-placement check
	// must route that span to events.
	if wholeHourZone(mustLoad(t, "Asia/Colombo"), mustTime(t, "1996-05-01T00:00:00Z"), mustTime(t, "1996-12-31T23:59:59Z")) {
		t.Error("Asia/Colombo 1996 (midnight shift) must be unservable → events")
	}
}

// F3 coverage through the serving path: a shifted-midnight zone routes a
// query to exact events (not the hourly rollups).
func TestDailyServedShiftedMidnightUsesEvents(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	e := event("c1", "r1", "model-a", "s1", mustTime(t, "1996-06-15T12:00:00Z"), TokenSums{Input: 5, Output: 1})
	if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
		t.Fatal(err)
	}
	_, src, err := s.DailyServed(ctx, mustLoad(t, "Asia/Colombo"), Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if src != "events" {
		t.Errorf("Asia/Colombo (shifted midnight) served from %q, want events", src)
	}
}

func TestDailyServedPathSelection(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	tzTestEvents(t, s)

	eq := func(when string, served []DailyRow, source, wantSource string, direct []DailyRow) {
		t.Helper()
		if source != wantSource {
			t.Errorf("%s: source = %q, want %q", when, source, wantSource)
		}
		sj, _ := json.Marshal(served)
		dj, _ := json.Marshal(direct)
		if string(sj) != string(dj) {
			t.Fatalf("%s: served != direct event aggregation\nserved: %s\ndirect: %s", when, sj, dj)
		}
	}

	// UTC → rollup_daily.
	rows, src, err := s.DailyServed(ctx, time.UTC, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	direct, _ := s.Daily(ctx, time.UTC, Filters{})
	eq("UTC", rows, src, "rollup", direct)

	// Whole-hour offset (Tokyo +09) → rollup_hourly, byte-equal to events.
	tokyo := mustLoad(t, "Asia/Tokyo")
	rows, src, err = s.DailyServed(ctx, tokyo, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	direct, _ = s.Daily(ctx, tokyo, Filters{})
	eq("Asia/Tokyo", rows, src, "rollup", direct)

	// Fractional offset (Kolkata +05:30) → events.
	kolkata := mustLoad(t, "Asia/Kolkata")
	rows, src, err = s.DailyServed(ctx, kolkata, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	direct, _ = s.Daily(ctx, kolkata, Filters{})
	eq("Asia/Kolkata", rows, src, "events", direct)

	// A basis filter forces events even for a whole-hour zone.
	rows, src, err = s.DailyServed(ctx, tokyo, Filters{Basis: []string{"api_price"}})
	if err != nil {
		t.Fatal(err)
	}
	direct, _ = s.Daily(ctx, tokyo, Filters{Basis: []string{"api_price"}})
	eq("Asia/Tokyo+basis", rows, src, "events", direct)
}

// The DST transition day (NY 2026-03-08, a 23-hour local day) is served
// from the hourly rollups byte-equal to event aggregation — the spec's
// DST case. The rollup path is also distinct from the UTC buckets here,
// so this is not a UTC-in-disguise check.
func TestDailyServedDSTByteEqual(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	tzTestEvents(t, s)
	ny := mustLoad(t, "America/New_York")

	served, src, err := s.DailyServed(ctx, ny, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if src != "rollup" {
		t.Fatalf("NY source = %q, want rollup (whole-hour DST zone)", src)
	}
	direct, err := s.Daily(ctx, ny, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	sj, _ := json.Marshal(served)
	dj, _ := json.Marshal(direct)
	if string(sj) != string(dj) {
		t.Fatalf("NY DST day: hourly-served != events\nserved: %s\ndirect: %s", sj, dj)
	}
	// The DST day must actually exist in the result (the three codex
	// events all bucket to NY local 2026-03-08).
	var found *DailyRow
	for i := range served {
		if served[i].Date == "2026-03-08" {
			found = &served[i]
		}
	}
	if found == nil {
		t.Fatal("NY local day 2026-03-08 missing from served rows")
	}
	if found.Input != 18 || found.Output != 17 { // 3+11+4, 9+2+6
		t.Errorf("NY 2026-03-08 sums = in %d/out %d, want 18/17", found.Input, found.Output)
	}
}

// Grand totals are invariant across timezones: every event is counted
// once regardless of how days bucket. The per-day rows differ; the sum
// over a range covering all events does not.
func TestDailyServedTotalsZoneInvariant(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	tzTestEvents(t, s)

	grand := func(rows []DailyRow) (in, out, cost, equiv, unpriced int64) {
		for _, r := range rows {
			in += r.Input
			out += r.Output
			cost += r.CostUSDMicro
			equiv += r.CostAPIEquivMicro
			unpriced += r.UnpricedEvents
		}
		return
	}
	var want [5]int64
	for i, zone := range []string{"UTC", "Asia/Tokyo", "America/New_York", "Asia/Kolkata", "Asia/Kathmandu"} {
		rows, _, err := s.DailyServed(ctx, mustLoad(t, zone), Filters{})
		if err != nil {
			t.Fatal(err)
		}
		in, out, cost, equiv, unpriced := grand(rows)
		got := [5]int64{in, out, cost, equiv, unpriced}
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Errorf("%s grand totals %v != UTC %v", zone, got, want)
		}
	}
}
