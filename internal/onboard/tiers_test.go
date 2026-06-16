package onboard

import "testing"

func TestLoadTierPrices(t *testing.T) {
	snap, err := LoadTierPrices()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version == "" {
		t.Fatal("empty version")
	}

	// The spec-seeded published list prices.
	for _, c := range []struct {
		provider, tier, want string
	}{
		{"anthropic", "free", "0"},
		{"anthropic", "pro", "20"},
		{"anthropic", "max_5x", "100"},
		{"anthropic", "max_20x", "200"},
		{"openai", "free", "0"},
		{"openai", "go", "8"},
		{"openai", "plus", "20"},
		{"openai", "pro_100", "100"},
		{"openai", "pro_200", "200"},
	} {
		got, ok := snap.Price(c.provider, c.tier)
		if !ok {
			t.Errorf("Price(%q,%q): not found", c.provider, c.tier)
			continue
		}
		if got.String() != c.want {
			t.Errorf("Price(%q,%q) = %s, want %s", c.provider, c.tier, got, c.want)
		}
	}

	if _, ok := snap.Price("anthropic", "nonsuch"); ok {
		t.Error("unknown tier resolved")
	}
	if _, ok := snap.Price("nonsuch", "pro"); ok {
		t.Error("unknown provider resolved")
	}
}

func TestTierChoicesIncludeMetered(t *testing.T) {
	snap, err := LoadTierPrices()
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range ProviderArgs() {
		choices := TierChoices(arg, snap)
		var hasMetered bool
		for _, c := range choices {
			if c == MeteredTier {
				hasMetered = true
			}
		}
		if !hasMetered {
			t.Errorf("%s choices %v missing %q", arg, choices, MeteredTier)
		}
	}
}
