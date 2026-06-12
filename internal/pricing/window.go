package pricing

// Plan usage windows (M5 Task 2): rolling windows derived from event
// timestamps, duration from the owner's plan declaration. This is
// tatitok-native and informational — explicitly NOT a ccusage-blocks
// parity surface; no golden ceremony applies.
//
// The partition is greedy and deterministic: a window OPENS at the
// first event not covered by the previous window, with its start
// floored to the UTC hour (the provider-observed reset behavior;
// sub-hour windows skip the floor — flooring could otherwise close a
// window before its own opening event). The window spans [start,
// start+duration); events inside join it; the first event at or past
// the end opens the next window.

import (
	"sort"
	"time"
)

// WindowEvent is one plan-covered event's contribution to window math.
type WindowEvent struct {
	TS                                   time.Time
	Input, Output, CacheWrite, CacheRead int64
	// EquivMicro is the stored API-equivalent (0 when absent); Unpriced
	// marks absence so the meter can stay honest about coverage.
	EquivMicro int64
	Unpriced   bool
}

// WindowUsage is one materialized window: its bounds and the usage that
// landed inside.
type WindowUsage struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`

	Events     int64 `json:"events"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheWrite int64 `json:"cache_write"`
	CacheRead  int64 `json:"cache_read"`
	EquivMicro int64 `json:"cost_api_equiv_micro"`
	Unpriced   int64 `json:"events_unpriced"`
}

// windowStart floors the opening event's timestamp to the UTC hour —
// except for sub-hour durations, where the floor could place the
// window's end before the event itself.
func windowStart(ts time.Time, dur time.Duration) time.Time {
	if dur < time.Hour {
		return ts
	}
	return ts.Truncate(time.Hour)
}

// PlanWindows partitions events into rolling windows of dur. Events are
// sorted by timestamp if not already ascending; the returned windows
// are ascending and non-overlapping.
func PlanWindows(events []WindowEvent, dur time.Duration) []WindowUsage {
	if len(events) == 0 || dur <= 0 {
		return nil
	}
	if !sort.SliceIsSorted(events, func(i, j int) bool { return events[i].TS.Before(events[j].TS) }) {
		sorted := make([]WindowEvent, len(events))
		copy(sorted, events)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })
		events = sorted
	}
	var out []WindowUsage
	for _, e := range events {
		ts := e.TS.UTC()
		if len(out) == 0 || !ts.Before(out[len(out)-1].End) {
			start := windowStart(ts, dur)
			out = append(out, WindowUsage{Start: start, End: start.Add(dur)})
		}
		w := &out[len(out)-1]
		w.Events++
		w.Input += e.Input
		w.Output += e.Output
		w.CacheWrite += e.CacheWrite
		w.CacheRead += e.CacheRead
		w.EquivMicro += e.EquivMicro
		if e.Unpriced {
			w.Unpriced++
		}
	}
	return out
}

// CurrentWindow returns the window containing now, if the last window
// is still open. A plan with no window in progress has nothing to
// meter — the next event will open one.
func CurrentWindow(windows []WindowUsage, now time.Time) (WindowUsage, bool) {
	if n := len(windows); n > 0 {
		w := windows[n-1]
		if !now.Before(w.Start) && now.Before(w.End) {
			return w, true
		}
	}
	return WindowUsage{}, false
}
