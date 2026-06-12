package pricing

// Plan usage windows (M5 Task 2): rolling windows derived from event
// timestamps, duration from the owner's plan declaration. This is
// tatitok-native and informational — explicitly NOT a ccusage-blocks
// parity surface; no golden ceremony applies.
//
// The partition is greedy and deterministic: a window OPENS at the
// first event not covered by the previous window and spans [start,
// start+duration); events inside join it; the first event at or past
// the end opens the next window.
//
// Anchoring is PROVIDER-DEPENDENT (M5 stop-1 finding, live-verified):
// Anthropic floors window starts to the UTC hour; OpenAI anchors at
// the exact first-request time. Each plan declares its anchor
// (window_start: floored|exact, floored default). Floored skips the
// floor for sub-hour durations — flooring could otherwise close a
// window before its own opening event — and REQUIRES whole-hour
// durations at or above 1h (load-validated; Codex M5 round, finding 3:
// a floored 90m window would overlap its successor).

import (
	"sort"
	"time"
)

// WindowAnchor selects how a plan's windows anchor their start.
type WindowAnchor string

const (
	// AnchorFloored floors the opening event's timestamp to the UTC hour
	// (Anthropic's observed reset behavior).
	AnchorFloored WindowAnchor = "floored"
	// AnchorExact anchors at the opening event's exact timestamp
	// (OpenAI's observed reset behavior).
	AnchorExact WindowAnchor = "exact"
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

// windowStart anchors the opening event's timestamp per the plan's
// declaration. The floor is skipped for sub-hour durations, where it
// could place the window's end before the event itself.
func windowStart(ts time.Time, dur time.Duration, anchor WindowAnchor) time.Time {
	if anchor == AnchorExact || dur < time.Hour {
		return ts
	}
	return ts.Truncate(time.Hour)
}

// PlanWindows partitions events into rolling windows of dur, anchored
// per the plan's declaration. Events are sorted by timestamp if not
// already ascending; the returned windows are ascending and
// non-overlapping.
func PlanWindows(events []WindowEvent, dur time.Duration, anchor WindowAnchor) []WindowUsage {
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
			start := windowStart(ts, dur, anchor)
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
