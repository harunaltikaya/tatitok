package limits

import (
	"math"
	"sync"
	"testing"
)

func sample() Snapshot {
	return Snapshot{
		"claude": {FetchedAt: 1_700_000_000_000, Windows: []Window{
			{Label: "All models", UsedPercent: 42, ResetAt: 1_700_000_500_000},
			{Label: "Sonnet", UsedPercent: 7.5, ResetAt: 1_700_001_000_000},
		}},
		"codex": {FetchedAt: 1_700_000_000_001, Windows: []Window{
			{Label: "5h", UsedPercent: 90, ResetAt: 1_700_000_300_000},
			{Label: "Weekly", UsedPercent: 0, ResetAt: 0},
		}},
	}
}

func TestStoreEmpty(t *testing.T) {
	s := NewStore()
	if got := s.Get(); got != nil {
		t.Fatalf("fresh store Get() = %v, want nil (nothing posted yet)", got)
	}
}

func TestStoreSetGet(t *testing.T) {
	s := NewStore()
	snap := sample()
	s.Set(snap)
	got := s.Get()
	if len(got) != 2 {
		t.Fatalf("Get() has %d providers, want 2", len(got))
	}
	if got["codex"].Windows[0].UsedPercent != 90 {
		t.Errorf("codex 5h usedPercent = %v, want 90", got["codex"].Windows[0].UsedPercent)
	}
}

func TestStoreMergesPerProviderKey(t *testing.T) {
	s := NewStore()
	s.Set(sample())
	// A POST from one feeder (the agy statusLine hook) carries only "agy":
	// it must be added WITHOUT removing claude/codex.
	s.Set(Snapshot{"agy": {FetchedAt: 9, Windows: []Window{{Label: "gemini-5h", UsedPercent: 12, ResetAt: 5}}}})
	got := s.Get()
	if len(got) != 3 {
		t.Fatalf("after agy write got %d providers, want 3 (claude, codex, agy): %+v", len(got), got)
	}
	if got["codex"].Windows[0].UsedPercent != 90 || got["claude"].Windows[0].UsedPercent != 42 {
		t.Errorf("claude/codex changed by a write that omitted them: %+v", got)
	}
	if got["agy"].Windows[0].UsedPercent != 12 {
		t.Errorf("agy = %+v, want gemini-5h@12%%", got["agy"])
	}
	// And vice versa: the extension's claude+codex snapshot must not remove
	// agy; within a key the write is last-write-wins.
	s.Set(Snapshot{"claude": {FetchedAt: 10, Windows: []Window{{Label: "All models", UsedPercent: 55, ResetAt: 6}}}})
	got = s.Get()
	if _, ok := got["agy"]; !ok {
		t.Error("agy dropped by a claude-only write")
	}
	if got["claude"].Windows[0].UsedPercent != 55 || len(got["claude"].Windows) != 1 {
		t.Errorf("claude not replaced within its key: %+v", got["claude"])
	}
}

// TestStoreConcurrent exercises the RWMutex under the suite's -race runs:
// concurrent writers and readers must not data-race.
func TestStoreConcurrent(t *testing.T) {
	s := NewStore()
	s.Set(sample())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.Set(sample()) }()
		go func() { defer wg.Done(); _ = s.Get() }()
	}
	wg.Wait()
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
		ok   bool
	}{
		{"valid two providers", sample(), true},
		{"empty snapshot", Snapshot{}, true},
		{"provider with no windows", Snapshot{"claude": {FetchedAt: 1}}, true},
		{"boundary 0 and 100", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: 0}, {Label: "b", UsedPercent: 100}}}}, true},
		{"resetAt zero allowed", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: 1, ResetAt: 0}}}}, true},
		// Over-cap is accepted and stored verbatim — a provider may report >100;
		// the stored number stays truthful, the frontend clamps the bar.
		{"percent just over 100 kept", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: 100.01}}}}, true},
		{"percent far over 100 kept", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: 250}}}}, true},

		{"empty provider key", Snapshot{"": {FetchedAt: 1}}, false},
		{"negative fetchedAt", Snapshot{"x": {FetchedAt: -1}}, false},
		{"empty window label", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "", UsedPercent: 1}}}}, false},
		{"percent NaN", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: math.NaN()}}}}, false},
		{"percent +Inf", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: math.Inf(1)}}}}, false},
		{"percent negative", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: -0.01}}}}, false},
		{"negative resetAt", Snapshot{"x": {FetchedAt: 1, Windows: []Window{{Label: "a", UsedPercent: 1, ResetAt: -5}}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.snap.Validate()
			if c.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !c.ok && err == nil {
				t.Errorf("Validate() = nil, want an error")
			}
		})
	}
}
