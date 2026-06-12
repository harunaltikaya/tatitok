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
		{TS: at(13, 59), CacheRead: 7, EquivMicro: 1},   // still inside [09:00, 14:00)
		{TS: at(14, 0), Input: 1, EquivMicro: 2},        // at end — opens [14:00, 19:00)
		{TS: at(23, 30), Input: 9, Unpriced: true},      // gap — opens [23:00, 04:00)
	}
	w := PlanWindows(events, dur)
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
	w2 := PlanWindows(shuffled, dur)
	if len(w2) != 3 || w2[0] != w[0] || w2[1] != w[1] || w2[2] != w[2] {
		t.Fatalf("unsorted input changed the partition: %+v", w2)
	}

	// Empty input and non-positive duration: no windows.
	if PlanWindows(nil, dur) != nil || PlanWindows(events, 0) != nil {
		t.Fatal("degenerate inputs produced windows")
	}
}

// Sub-hour windows skip the hour floor — flooring would end the window
// before its own opening event.
func TestPlanWindowsSubHour(t *testing.T) {
	w := PlanWindows([]WindowEvent{{TS: at(12, 45)}}, 30*time.Minute)
	if len(w) != 1 || !w[0].Start.Equal(at(12, 45)) || !w[0].End.Equal(time.Date(2026, 6, 10, 13, 15, 0, 0, time.UTC)) {
		t.Fatalf("sub-hour window: %+v", w)
	}
}

func TestCurrentWindow(t *testing.T) {
	w := PlanWindows([]WindowEvent{{TS: at(9, 30)}}, 5*time.Hour) // [09:00, 14:00)
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
