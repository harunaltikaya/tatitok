package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// --- shapes the tests decode (subset of the handler payloads) ---

type dTier struct {
	Tier     string `json:"tier"`
	PriceUSD string `json:"price_usd"`
}

type dCard struct {
	ProviderArg      string   `json:"provider_arg"`
	PlanName         string   `json:"plan_name"`
	Provider         string   `json:"provider"`
	DetectedTier     string   `json:"detected_tier"`
	DetectedRaw      string   `json:"detected_raw"`
	Ambiguous        bool     `json:"ambiguous"`
	AmbiguousOptions []string `json:"ambiguous_options"`
	MeteredAvailable bool     `json:"metered_available"`
	WindowsPresent   bool     `json:"windows_present"`
	Tiers            []dTier  `json:"tiers"`
	Current          struct {
		Declared          bool   `json:"declared"`
		MonthlyPriceMicro *int64 `json:"monthly_price_micro"`
	} `json:"current"`
}

func (c dCard) price(tier string) (string, bool) {
	for _, t := range c.Tiers {
		if t.Tier == tier {
			return t.PriceUSD, true
		}
	}
	return "", false
}

type detectResp struct {
	SnapshotVersion string  `json:"snapshot_version"`
	HasUsage        bool    `json:"has_usage"`
	HasPlans        bool    `json:"has_plans"`
	Cards           []dCard `json:"cards"`
}

func (d detectResp) card(t *testing.T, arg string) dCard {
	t.Helper()
	for _, c := range d.Cards {
		if c.ProviderArg == arg {
			return c
		}
	}
	t.Fatalf("no %q card in detect response", arg)
	return dCard{}
}

type applyResp struct {
	OK       bool     `json:"ok"`
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Repriced int64    `json:"repriced"`
	ByBasis  []struct {
		Value  string `json:"value"`
		Events int64  `json:"events"`
	} `json:"by_basis"`
}

func (a applyResp) basis(name string) int64 {
	for _, kv := range a.ByBasis {
		if kv.Value == name {
			return kv.Events
		}
	}
	return 0
}

func postJSON(t *testing.T, h *Hub, path, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post("http://"+h.Addr()+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestOnboardDetect: from a fixture-seeded, no-plans DB, /detect reports the
// detected Codex tier (plus, from the fixtures), the selectable tiers + list
// prices, and no current plan.
func TestOnboardDetect(t *testing.T) {
	h := seedHubWith(t, nil, nil)
	var d detectResp
	getOK(t, h, "/api/onboard/detect", &d)

	if d.SnapshotVersion != "tier-prices-2026-06-17.2" {
		t.Errorf("snapshot_version = %q", d.SnapshotVersion)
	}
	if !d.HasUsage || d.HasPlans {
		t.Errorf("has_usage=%v has_plans=%v, want true/false", d.HasUsage, d.HasPlans)
	}
	if len(d.Cards) != 2 {
		t.Fatalf("want 2 cards, got %d", len(d.Cards))
	}

	codex := d.card(t, "codex")
	if codex.DetectedTier != "plus" || codex.Ambiguous {
		t.Errorf("codex detected = %q ambiguous=%v, want plus/false", codex.DetectedTier, codex.Ambiguous)
	}
	if p, ok := codex.price("pro_200"); !ok || p != "200" {
		t.Errorf("codex pro_200 price = %q (ok=%v), want 200", p, ok)
	}
	if p, ok := codex.price("go"); !ok || p != "8" {
		t.Errorf("codex go price = %q, want 8", p)
	}
	if _, ok := codex.price("pro"); ok {
		t.Error("codex still offers the old ambiguous \"pro\" tier")
	}
	if codex.Current.Declared {
		t.Error("codex should have no current plan in a no-plans DB")
	}

	claude := d.card(t, "claude")
	if claude.DetectedTier != "" {
		t.Errorf("claude detected = %q, want empty (never derivable)", claude.DetectedTier)
	}
	if p, ok := claude.price("max_20x"); !ok || p != "200" {
		t.Errorf("claude max_20x price = %q, want 200", p)
	}
}

// TestOnboardApplyAndReprice: /apply writes both entries, reprices to
// plan_included, and /detect then shows the declared plans.
func TestOnboardApplyAndReprice(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	h := seedHubWith(t, nil, nil)

	status, b := postJSON(t, h, "/api/onboard/apply",
		`{"cards":[{"provider_arg":"claude","tier":"max_20x"},{"provider_arg":"codex","tier":"plus"}]}`)
	if status != http.StatusOK {
		t.Fatalf("apply status %d: %s", status, b)
	}
	var ar applyResp
	if err := json.Unmarshal(b, &ar); err != nil {
		t.Fatal(err)
	}
	if !ar.OK || ar.Repriced == 0 {
		t.Fatalf("apply result: %+v", ar)
	}
	if ar.basis("plan_included") == 0 {
		t.Fatalf("no plan_included events after apply: %+v", ar.ByBasis)
	}

	ov, err := pricing.LoadOverrides(filepath.Join(xdg, "tatitok", "prices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Plans()) != 2 {
		t.Fatalf("prices.json has %d plans, want 2", len(ov.Plans()))
	}

	var d detectResp
	getOK(t, h, "/api/onboard/detect", &d)
	if !d.HasPlans {
		t.Error("has_plans should be true after apply")
	}
	cl := d.card(t, "claude")
	if !cl.Current.Declared || cl.Current.MonthlyPriceMicro == nil || *cl.Current.MonthlyPriceMicro != 200_000_000 {
		t.Errorf("claude current plan wrong: %+v", cl.Current)
	}
}

// TestOnboardApplyMeteredWritesNone: a metered card writes no entry.
func TestOnboardApplyMeteredWritesNone(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	h := seedHubWith(t, nil, nil)

	status, b := postJSON(t, h, "/api/onboard/apply",
		`{"cards":[{"provider_arg":"claude","tier":"metered"},{"provider_arg":"codex","tier":"plus"}]}`)
	if status != http.StatusOK {
		t.Fatalf("apply status %d: %s", status, b)
	}
	ov, err := pricing.LoadOverrides(filepath.Join(xdg, "tatitok", "prices.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ov.Plans() {
		if p.Name == "claude-max" {
			t.Fatal("metered claude wrote a claude-max plan")
		}
	}
	if len(ov.Plans()) != 1 || ov.Plans()[0].Name != "chatgpt-plus" {
		t.Fatalf("want only chatgpt-plus, got %+v", ov.Plans())
	}
}

// TestOnboardDetectProAmbiguous: when the Codex log reports plan_type "pro"
// (two indistinguishable price points), /detect surfaces the $100/$200 choice
// and does NOT pre-fill a tier. Uses a synthetic Codex rollout for DETECTION
// only (single-field read; not an adapter parity test) via CODEX_HOME.
func TestOnboardDetectProAmbiguous(t *testing.T) {
	codexHome := t.TempDir()
	day := filepath.Join(codexHome, "sessions", "2026", "06", "16")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-06-16T12:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","plan_type":"pro","primary":{"used_percent":1.0,"window_minutes":300,"resets_at":1}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(day, "rollout-2026-06-16T12-00-00-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	h := seedHubWith(t, nil, nil)
	var d detectResp
	getOK(t, h, "/api/onboard/detect", &d)
	codex := d.card(t, "codex")
	if !codex.Ambiguous || codex.DetectedTier != "" {
		t.Fatalf("codex pro should be ambiguous with no pre-filled tier: %+v", codex)
	}
	if codex.DetectedRaw != "pro" {
		t.Errorf("detected_raw = %q, want pro", codex.DetectedRaw)
	}
	if len(codex.AmbiguousOptions) != 2 || codex.AmbiguousOptions[0] != "pro_100" || codex.AmbiguousOptions[1] != "pro_200" {
		t.Errorf("ambiguous_options = %v, want [pro_100 pro_200]", codex.AmbiguousOptions)
	}
}
