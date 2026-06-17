package modelmap

import "testing"

// Exit criterion: the seed covers EVERY model string that
// appears in the committed fixtures. This list is the DB-visible
// inventory over all three fixture sets (claude-code, codex, opencode)
// plus <synthetic>, which the claude-code adapter excludes today but the
// map covers defensively.
var fixtureModels = []string{
	"claude-fable-5",
	"claude-opus-4-8",
	"claude-sonnet-4-6",
	"<synthetic>",
	"gpt-5.4",
	"gpt-5.5",
	"codex-auto-review",
	"deepseek-v4-flash",
	"deepseek-v4-flash-free",
	"deepseek-v4-pro",
	"gpt-5-nano",
	"qwen3.6-27b",
	"qwen3.6-35b-a3b",
	"qwen3.6-35b-nvfp4-tecnigmaai",
	"gx10",
}

func TestSeedCoversEveryFixtureModel(t *testing.T) {
	for _, m := range fixtureModels {
		if _, ok := current.Families[m]; !ok {
			t.Errorf("fixture model %q has no seed entry — the map must cover every committed fixture model", m)
		}
	}
}

func TestReviewedFamilies(t *testing.T) {
	want := map[string]string{
		"<synthetic>":                  "synthetic",         // covered defensively
		"deepseek-v4-flash-free":       "deepseek-v4-flash", // billing tier of the same model
		"qwen3.6-35b-nvfp4-tecnigmaai": "qwen3.6-35b",       // quant+org recipe stripped
		"qwen3.6-35b-a3b":              "qwen3.6-35b-a3b",   // distinct architecture, own family
		"gx10":                         "gx10",              // owner box alias, not derivable
	}
	for model, family := range want {
		if got := Family(model); got != family {
			t.Errorf("Family(%q) = %q, want %q", model, got, family)
		}
	}
}

// Unknown models pass through verbatim — never guessed.
// Covers the live-only shapes the owner reported: slash-named models and
// the empty codex pre-turn_context model.
func TestUnknownModelsPassThrough(t *testing.T) {
	for _, m := range []string{
		"nvidia/diffusiongemma-12b", // slash-named, live-only
		"",                          // codex usage before the first turn_context
		"some-future-model",
	} {
		if got := Family(m); got != m {
			t.Errorf("Family(%q) = %q, want verbatim passthrough", m, got)
		}
	}
}

func TestVersionAndEntries(t *testing.T) {
	if Version() < 1 {
		t.Fatalf("Version() = %d, want >= 1", Version())
	}
	entries := Entries()
	if len(entries) != len(current.Families) {
		t.Fatalf("Entries() returned %d rows, want %d", len(entries), len(current.Families))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Model >= entries[i].Model {
			t.Fatal("Entries() not sorted by model")
		}
	}
}
