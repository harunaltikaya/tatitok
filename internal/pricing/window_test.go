package pricing

// Window math unit tests (M5 Task 2). Synthetic timestamps are fine:
// PlanWindows is a pure function — this is the sanctioned use. The
// fixture-anchored pin lives in the store tests, where real fixture
// ingest feeds the same math.

import (
	"testing"
	"time"
)

func at(h, m int) time.Time {
	return time.Date(2026, 6, 10, h, m, 0, 0, time.UTC)
}

func TestPlanWindowsPartition(t *testing.T) {
	dur := 5 * time.Hour
	events := []WindowEvent{
		{TS: at(9, 30), Input: 100, EquivMicro: 10},
		{TS: at(9, 45), Output: 50, EquivMicro: 5},
		{TS: at(13, 59), CacheRead: 7, EquivMicro: 1}, // still inside [09:00, 14:00)
		{TS: at(14, 0), Input: 1, EquivMicro: 2},      // at end — opens [14:00, 19:00)
		{TS: at(23, 30), Input: 9, Unpriced: true},    // gap — opens [23:00, 04:00)
	}
	w := PlanWindows(events, dur, AnchorFloored)
	if len(w) != 3 {
		t.Fatalf("windows = %d, want 3: %+v", len(w), w)
	}
	// Window starts floor to the UTC hour; [start, end) is half-open.
	if !w[0].Start.Equal(at(9, 0)) || !w[0].End.Equal(at(14, 0)) ||
		w[0].Events != 3 || w[0].Input != 100 || w[0].Output != 50 ||
		w[0].CacheRead != 7 || w[0].EquivMicro != 16 {
		t.Fatalf("window 0: %+v", w[0])
	}
	if !w[1].Start.Equal(at(14, 0)) || w[1].Events != 1 || w[1].EquivMicro != 2 {
		t.Fatalf("window 1: %+v", w[1])
	}
	if !w[2].Start.Equal(at(23, 0)) || w[2].Unpriced != 1 || w[2].EquivMicro != 0 {
		t.Fatalf("window 2: %+v", w[2])
	}

	// Unsorted input partitions identically (defensive sort).
	shuffled := []WindowEvent{events[3], events[0], events[4], events[2], events[1]}
	w2 := PlanWindows(shuffled, dur, AnchorFloored)
	if len(w2) != 3 || w2[0] != w[0] || w2[1] != w[1] || w2[2] != w[2] {
		t.Fatalf("unsorted input changed the partition: %+v", w2)
	}

	// Empty input and non-positive duration: no windows.
	if PlanWindows(nil, dur, AnchorFloored) != nil || PlanWindows(events, 0, AnchorFloored) != nil {
		t.Fatal("degenerate inputs produced windows")
	}
}

// Exact anchoring (M5 stop-1 finding, the OpenAI behavior): windows
// open at the first request's exact timestamp — an 18:33 first request
// resets at 23:33, not 23:00.
func TestPlanWindowsExactAnchor(t *testing.T) {
	events := []WindowEvent{
		{TS: at(18, 33), Input: 1},
		{TS: at(23, 32), Input: 2}, // still inside [18:33, 23:33)
		{TS: at(23, 33), Input: 3}, // at end — opens [23:33, 04:33)
	}
	w := PlanWindows(events, 5*time.Hour, AnchorExact)
	if len(w) != 2 {
		t.Fatalf("windows = %d, want 2: %+v", len(w), w)
	}
	if !w[0].Start.Equal(at(18, 33)) || !w[0].End.Equal(at(23, 33)) || w[0].Events != 2 {
		t.Fatalf("exact window 0: %+v", w[0])
	}
	if !w[1].Start.Equal(at(23, 33)) || w[1].Events != 1 {
		t.Fatalf("exact window 1: %+v", w[1])
	}
}

// Sub-hour windows skip the hour floor — flooring would end the window
// before its own opening event.
func TestPlanWindowsSubHour(t *testing.T) {
	w := PlanWindows([]WindowEvent{{TS: at(12, 45)}}, 30*time.Minute, AnchorFloored)
	if len(w) != 1 || !w[0].Start.Equal(at(12, 45)) || !w[0].End.Equal(time.Date(2026, 6, 10, 13, 15, 0, 0, time.UTC)) {
		t.Fatalf("sub-hour window: %+v", w)
	}
}

// Codex M5 round, finding 3 (MED): hour-floored starts overlap for
// non-whole-hour durations ≥ 1h (90m: [09:00,10:30) then [10:00,11:30)).
// Load validation rejects that combination; this pins the non-overlap
// invariant over the ACCEPTED space — whole-hour floored, any-duration
// exact, sub-hour floored (floor skipped) — on an adversarial event set
// (boundary-straddling, dense, gapped).
func TestPlanWindowsNeverOverlap(t *testing.T) {
	var events []WindowEvent
	for _, m := range []int{0, 7, 29, 30, 31, 50, 59, 89, 90, 91, 119, 150, 240, 600, 601, 1439} {
		events = append(events, WindowEvent{TS: at(0, 0).Add(time.Duration(m) * time.Minute)})
	}
	accepted := []struct {
		dur    time.Duration
		anchor WindowAnchor
	}{
		{time.Hour, AnchorFloored}, {2 * time.Hour, AnchorFloored},
		{5 * time.Hour, AnchorFloored}, {30 * time.Minute, AnchorFloored},
		{90 * time.Minute, AnchorExact}, {45 * time.Minute, AnchorExact},
		{5 * time.Hour, AnchorExact},
	}
	for _, c := range accepted {
		w := PlanWindows(events, c.dur, c.anchor)
		var covered int64
		for i, win := range w {
			if !win.End.Equal(win.Start.Add(c.dur)) {
				t.Errorf("%v/%s: window %d span wrong: %s..%s", c.dur, c.anchor, i, win.Start, win.End)
			}
			if i > 0 && w[i-1].End.After(win.Start) {
				t.Errorf("%v/%s: windows %d and %d OVERLAP (%s..%s then %s..%s)",
					c.dur, c.anchor, i-1, i, w[i-1].Start, w[i-1].End, win.Start, win.End)
			}
			covered += win.Events
		}
		if covered != int64(len(events)) {
			t.Errorf("%v/%s: %d of %d events covered", c.dur, c.anchor, covered, len(events))
		}
	}
}

func TestCurrentWindow(t *testing.T) {
	w := PlanWindows([]WindowEvent{{TS: at(9, 30)}}, 5*time.Hour, AnchorFloored) // [09:00, 14:00)
	if cur, ok := CurrentWindow(w, at(13, 59)); !ok || !cur.Start.Equal(at(9, 0)) {
		t.Fatalf("inside window not current: %+v %v", cur, ok)
	}
	if _, ok := CurrentWindow(w, at(14, 0)); ok {
		t.Fatal("expired window reported current (end is exclusive)")
	}
	if _, ok := CurrentWindow(nil, at(12, 0)); ok {
		t.Fatal("no windows but current reported")
	}
}
