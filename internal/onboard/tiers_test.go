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

	// The spec-seeded published list prices, and each tier's card label.
	cases := []struct {
		provider, tier, want, label string
	}{
		{"anthropic", "free", "0", "Claude Free"},
		{"anthropic", "pro", "20", "Claude Pro"},
		{"anthropic", "max_5x", "100", "Claude Max 5x"},
		{"anthropic", "max_20x", "200", "Claude Max 20x"},
		{"openai", "free", "0", "ChatGPT Free"},
		{"openai", "go", "8", "ChatGPT Go"},
		{"openai", "plus", "20", "ChatGPT Plus"},
		{"openai", "pro_100", "100", "ChatGPT Pro $100"},
		{"openai", "pro_200", "200", "ChatGPT Pro $200"},
		{"google", "ai_pro", "20", "Google AI Pro"},
	}
	for _, c := range cases {
		got, ok := snap.Price(c.provider, c.tier)
		if !ok {
			t.Errorf("Price(%q,%q): not found", c.provider, c.tier)
			continue
		}
		if got.String() != c.want {
			t.Errorf("Price(%q,%q) = %s, want %s", c.provider, c.tier, got, c.want)
		}
		if l, ok := snap.Label(c.provider, c.tier); !ok || l != c.label {
			t.Errorf("Label(%q,%q) = %q (ok=%v), want %q", c.provider, c.tier, l, ok, c.label)
		}
	}

	// tier_labels mirrors tiers exactly: no priced tier without a label,
	// no label without a priced tier.
	for provider, m := range snap.tiers {
		for tier := range m {
			if _, ok := snap.Label(provider, tier); !ok {
				t.Errorf("priced tier %s/%s has no label", provider, tier)
			}
		}
	}
	for provider, m := range snap.labels {
		for tier := range m {
			if _, ok := snap.Price(provider, tier); !ok {
				t.Errorf("label for %s/%s has no priced tier", provider, tier)
			}
		}
	}

	if _, ok := snap.Price("anthropic", "nonsuch"); ok {
		t.Error("unknown tier resolved")
	}
	if _, ok := snap.Price("nonsuch", "pro"); ok {
		t.Error("unknown provider resolved")
	}
	if _, ok := snap.Label("anthropic", "nonsuch"); ok {
		t.Error("unknown tier labeled")
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
