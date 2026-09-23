package onboard

import (
	"strings"
	"testing"
)

func TestSnapshotVersionBumped(t *testing.T) {
	snap := loadSnap(t)
	if snap.Version != "tier-prices-2026-09-24.2" {
		t.Fatalf("version = %q, want tier-prices-2026-09-24.2 (ChatGPT Pro labels, prolite alias)", snap.Version)
	}
	// The split: old openai "pro" is gone; pro_100/pro_200/go present.
	if _, ok := snap.Price("openai", "pro"); ok {
		t.Error("openai \"pro\" should be renamed away (now pro_100/pro_200)")
	}
}

func TestResolveCodexPlanType(t *testing.T) {
	snap := loadSnap(t)

	if r := ResolveCodexPlanType("plus", snap); r.Tier != "plus" || r.Ambiguous {
		t.Errorf("plus: %+v, want Tier=plus unambiguous", r)
	}
	if r := ResolveCodexPlanType("go", snap); r.Tier != "go" || r.Ambiguous {
		t.Errorf("go: %+v, want Tier=go unambiguous", r)
	}
	// The headline: "pro" is ambiguous ($100 vs $200), never guessed.
	r := ResolveCodexPlanType("pro", snap)
	if !r.Ambiguous || r.Tier != "" {
		t.Fatalf("pro: %+v, want Ambiguous with empty Tier", r)
	}
	if len(r.Options) != 2 || r.Options[0] != "pro_100" || r.Options[1] != "pro_200" {
		t.Fatalf("pro options = %v, want [pro_100 pro_200]", r.Options)
	}
	if r := ResolveCodexPlanType("", snap); r.Tier != "" || r.Ambiguous {
		t.Errorf("empty: %+v, want no tier", r)
	}
	if r := ResolveCodexPlanType("enterprise", snap); r.Tier != "" || r.Ambiguous {
		t.Errorf("unknown plan_type should not guess: %+v", r)
	}
}

func TestResolveCodexChoice(t *testing.T) {
	snap := loadSnap(t)

	// No flag, unambiguous detection → pre-fill, no choice owed.
	if c := ResolveCodexChoice("", "plus", snap); c.NeedChoice || c.Tier != "plus" {
		t.Errorf("detected plus: %+v, want Tier=plus NeedChoice=false", c)
	}
	// No flag, ambiguous detection → a choice IS owed (never guessed).
	if c := ResolveCodexChoice("", "pro", snap); !c.NeedChoice || c.Tier != "" {
		t.Errorf("detected pro w/o flag: %+v, want NeedChoice", c)
	}
	// No flag, no detection → choice owed.
	if c := ResolveCodexChoice("", "", snap); !c.NeedChoice {
		t.Errorf("no detection: %+v, want NeedChoice", c)
	}
	// Sub-tier flag resolving an ambiguous Pro → recorded as user-selected.
	c := ResolveCodexChoice("pro_200", "pro", snap)
	if c.Tier != "pro_200" || c.NeedChoice {
		t.Fatalf("pro_200 over ambiguous pro: %+v", c)
	}
	if !strings.Contains(c.TierNote, "sub-tier chosen for ambiguous codex Pro") {
		t.Fatalf("provenance note off: %q", c.TierNote)
	}
	// Flag confirming an unambiguous detection.
	if c := ResolveCodexChoice("plus", "plus", snap); !strings.Contains(c.TierNote, "user-confirmed") {
		t.Errorf("confirm note off: %q", c.TierNote)
	}
	// Metered → no note (no entry will be written).
	if c := ResolveCodexChoice(MeteredTier, "plus", snap); c.Tier != MeteredTier {
		t.Errorf("metered: %+v", c)
	}
}

// TestProSubTierEntryProvenance: choosing pro_200 for an ambiguous detection
// writes the $200 price AND records the sub-tier provenance in _doc.
func TestProSubTierEntryProvenance(t *testing.T) {
	snap := loadSnap(t)
	cc := ResolveCodexChoice("pro_200", "pro", snap)
	e, err := ResolveEntry(PlanChoice{
		ProviderArg: "codex", Tier: cc.Tier, TierNote: cc.TierNote,
	}, snap, now)
	if err != nil || e == nil {
		t.Fatalf("ResolveEntry: %v", err)
	}
	if e.MonthlyPriceUSD != "200" {
		t.Errorf("pro_200 price = %q, want 200", e.MonthlyPriceUSD)
	}
	if e.Name != "chatgpt-plus" || e.Label != "ChatGPT Pro" {
		t.Errorf("pro_200 name/label = %q/%q, want chatgpt-plus/ChatGPT Pro", e.Name, e.Label)
	}
	if !strings.Contains(e.Doc, "sub-tier chosen for ambiguous codex Pro") {
		t.Errorf("entry _doc missing Pro-split provenance: %q", e.Doc)
	}
}
