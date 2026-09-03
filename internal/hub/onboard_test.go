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

// postRaw POSTs with an explicit Content-Type and arbitrary extra headers — for
// exercising the CSRF guard (browser-only headers the Go client never sets).
func postRaw(t *testing.T, h *Hub, path, contentType, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+h.Addr()+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestOnboardApplyRejectsCSRF: /apply writes prices.json + reprices, so a
// browser must not be drivable cross-site into it. The loopback peer gate does
// NOT stop CSRF, so two header checks do: application/json is required (a
// CORS-simple content-type forces no preflight and is refused), and a
// cross-site/same-site Sec-Fetch-Site is refused outright. A rejected request
// must not write anything (finding #1).
func TestOnboardApplyRejectsCSRF(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	h := seedHubWith(t, nil, nil)
	pricesPath := filepath.Join(xdg, "tatitok", "prices.json")
	valid := `{"cards":[{"provider_arg":"claude","tier":"max_20x"}]}`

	// CORS-simple content-types a cross-origin page can send without a preflight.
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data", ""} {
		if status, b := postRaw(t, h, "/api/onboard/apply", ct, valid, nil); status != http.StatusUnsupportedMediaType {
			t.Errorf("apply with Content-Type %q = %d, want 415: %s", ct, status, b)
		}
	}
	// Browser cross-site / same-site signals are refused even with JSON.
	for _, site := range []string{"cross-site", "same-site"} {
		if status, b := postRaw(t, h, "/api/onboard/apply", "application/json", valid,
			map[string]string{"Sec-Fetch-Site": site}); status != http.StatusForbidden {
			t.Errorf("apply with Sec-Fetch-Site %q = %d, want 403: %s", site, status, b)
		}
	}
	// No rejected request may have written the plan file.
	if _, err := os.Stat(pricesPath); !os.IsNotExist(err) {
		t.Fatalf("a rejected CSRF request wrote prices.json (stat err=%v)", err)
	}

	// The dashboard's own same-origin JSON fetch still works.
	if status, b := postRaw(t, h, "/api/onboard/apply", "application/json", valid,
		map[string]string{"Sec-Fetch-Site": "same-origin"}); status != http.StatusOK {
		t.Fatalf("same-origin apply = %d, want 200: %s", status, b)
	}
	if _, err := os.Stat(pricesPath); err != nil {
		t.Fatalf("same-origin apply did not write prices.json: %v", err)
	}
}

// TestOnboardApplyRejectsBadBody: an empty tier is a malformed card (only the
// literal "metered" removes a plan — finding #4), and trailing data after the
// JSON object is rejected (finding #6). Neither writes a plan file.
func TestOnboardApplyRejectsBadBody(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	h := seedHubWith(t, nil, nil)

	if status, b := postJSON(t, h, "/api/onboard/apply",
		`{"cards":[{"provider_arg":"claude","tier":""}]}`); status != http.StatusBadRequest {
		t.Errorf("empty-tier apply = %d, want 400: %s", status, b)
	}
	if status, b := postJSON(t, h, "/api/onboard/apply",
		`{"cards":[{"provider_arg":"claude","tier":"max_20x"}]}{"trailing":true}`); status != http.StatusBadRequest {
		t.Errorf("trailing-data apply = %d, want 400: %s", status, b)
	}
	if _, err := os.Stat(filepath.Join(xdg, "tatitok", "prices.json")); !os.IsNotExist(err) {
		t.Fatalf("a rejected bad-body request wrote prices.json (stat err=%v)", err)
	}
}

// TestOnboardDetect: from a fixture-seeded, no-plans DB, /detect reports the
// detected Codex tier (plus, from the fixtures), the selectable tiers + list
// prices, and no current plan.
func TestOnboardDetect(t *testing.T) {
	// Hermetic Codex-tier detection: the /detect handler reads the real
	// environment via hubProbe(), so point CODEX_HOME at the committed,
	// sanitized gx10 rollout fixtures (plan_type "plus") rather than the
	// developer's ~/.codex. Without this the test passes only where real Codex
	// logs exist and fails `codex detected=""` on a clean CI runner. This is
	// the same committed fixture internal/onboard's TestDetectCodexTier uses.
	gx10, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "codex", "gx10"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", gx10)

	h := seedHubWith(t, nil, nil)
	var d detectResp
	getOK(t, h, "/api/onboard/detect", &d)

	if d.SnapshotVersion != "tier-prices-2026-09-03.1" {
		t.Errorf("snapshot_version = %q", d.SnapshotVersion)
	}
	if !d.HasUsage || d.HasPlans {
		t.Errorf("has_usage=%v has_plans=%v, want true/false", d.HasUsage, d.HasPlans)
	}
	if len(d.Cards) != 3 {
		t.Fatalf("want 3 cards (claude, codex, google), got %d", len(d.Cards))
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
	google := d.card(t, "google")
	if google.DetectedTier != "" || google.PlanName != "google-ai-pro" {
		t.Errorf("google card = %+v, want undetected google-ai-pro", google)
	}
	if p, ok := google.price("ai_pro"); !ok || p != "20" {
		t.Errorf("google ai_pro price = %q, want 20", p)
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
