package hub

// API v1 tests (M4 Task 2): ordinary tests, not parity gates — every
// numeric payload is asserted equal to the direct store query on the
// same fixture-seeded database (all three harnesses).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
	"github.com/harunaltikaya/tatitok/internal/adapters/opencode"
	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// seedHub starts a hub (no watchers) on a DB freshly ingested from all
// three harness fixtures.
func seedHub(t *testing.T) *Hub {
	t.Helper()
	return seedHubWith(t, nil, nil)
}

// seedHubWith is seedHub with a price-override config applied to both
// ingest and the hub, plus optional extra events inserted after the
// fixture ingest (priced by the same overrides).
func seedHubWith(t *testing.T, ov *pricing.Overrides, extra []core.Event) *Hub {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "seeded.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ccRoot, _ := filepath.Abs(ccFixtureRoot)
	cxRoot, _ := filepath.Abs(codexFixtureRoot)
	ocFixture, _ := filepath.Abs(opencodeFixtureDir)
	ocRoot := filepath.Join(t.TempDir(), "opencode")
	if _, err := opencode.BuildFixtureDB(ocFixture, filepath.Join(ocRoot, "opencode.db")); err != nil {
		t.Fatalf("reconstruct fixture db: %v", err)
	}
	for _, in := range []struct {
		a   adapters.Adapter
		src adapters.Source
	}{
		{claudecode.Adapter{}, adapters.Source{Harness: "claude-code", Root: ccRoot, Machine: "gx10"}},
		{codex.Adapter{}, adapters.Source{Harness: "codex", Root: cxRoot, Machine: "gx10"}},
		{opencode.Adapter{}, adapters.Source{Harness: "opencode", Root: ocRoot, Machine: "gx10"}},
	} {
		if _, err := adapters.IngestBackfill(ctx, st, in.a, []adapters.Source{in.src}, ov); err != nil {
			t.Fatalf("%s seed: %v", in.src.Harness, err)
		}
	}
	if len(extra) > 0 {
		for i := range extra {
			if err := pricing.Apply(&extra[i], ov); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.InsertBatch(ctx, extra, store.SourceInfo{
			Harness: extra[0].Harness, Machine: "gx10", Path: "synthetic-now.jsonl",
			MTime: time.Now(), Size: 1, LineCount: len(extra), AdapterVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	h, err := Start(Config{DBPath: dbPath, Addr: "127.0.0.1:0", Overrides: ov})
	if err != nil {
		t.Fatalf("hub start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return h
}

func get(t *testing.T, h *Hub, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get("http://" + h.Addr() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET %s: Content-Type %q, want application/json", path, ct)
	}
	return resp.StatusCode, b
}

func getOK(t *testing.T, h *Hub, path string, into any) {
	t.Helper()
	status, b := get(t, h, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, status, b)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("GET %s: bad JSON: %v\n%s", path, err, b)
	}
}

// assertErrEnvelope: every failure shape is the one envelope.
func assertErrEnvelope(t *testing.T, h *Hub, path string, wantStatus int) {
	t.Helper()
	status, b := get(t, h, path)
	if status != wantStatus {
		t.Errorf("GET %s = %d, want %d (%s)", path, status, wantStatus, b)
	}
	var e apiError
	if err := json.Unmarshal(b, &e); err != nil || e.Error.Code == "" || e.Error.Message == "" {
		t.Errorf("GET %s: not the error envelope: %s", path, b)
	}
}

func TestAPIHealth(t *testing.T) {
	h := seedHub(t)
	var got struct {
		Version       string `json:"version"`
		PriceSnapshot string `json:"price_snapshot"`
		Overrides     int    `json:"overrides"`
		DBHash        string `json:"db_hash"`
		StartedAt     string `json:"started_at"`
		Uptime        *int64 `json:"uptime_seconds"`
	}
	getOK(t, h, "/api/v1/health", &got)
	if got.Version == "" || got.PriceSnapshot == "" || got.Uptime == nil {
		t.Errorf("health missing fields: %+v", got)
	}
	if got.DBHash != dbPathHash(h.cfg.DBPath) || len(got.DBHash) != 16 {
		t.Errorf("db_hash %q, want 16-hex hash of the path", got.DBHash)
	}
	// The hash must not leak the path or any of its segments.
	if strings.Contains(got.DBHash, "/") || strings.Contains(got.DBHash, "tmp") {
		t.Errorf("db_hash looks like a path: %q", got.DBHash)
	}
	if _, err := time.Parse(time.RFC3339, got.StartedAt); err != nil {
		t.Errorf("started_at %q: %v", got.StartedAt, err)
	}
}

func TestAPIStatsDaily(t *testing.T) {
	h := seedHub(t)
	ctx := context.Background()

	var got struct {
		Grain string            `json:"grain"`
		TZ    string            `json:"tz"`
		Daily []json.RawMessage `json:"daily"`
	}
	getOK(t, h, "/api/v1/stats/daily", &got)
	if got.Grain != "day" || got.TZ != "UTC" {
		t.Errorf("payload must declare grain/tz: got %q/%q", got.Grain, got.TZ)
	}
	want, err := h.st.DailyFromRollups(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got.Daily)
	var wantRaw []json.RawMessage
	_ = json.Unmarshal(wantJSON, &wantRaw)
	wj, _ := json.Marshal(wantRaw)
	if string(gotJSON) != string(wj) {
		t.Errorf("daily payload != DailyFromRollups\ngot  %s\nwant %s", gotJSON, wj)
	}
	if len(want) < 2 {
		t.Fatalf("fixture corpus spans %d days — range test needs 2+", len(want))
	}

	// from/to filtering: drop the first and last day.
	from, to := want[1].Date, want[len(want)-2].Date
	if from > to {
		from, to = want[1].Date, want[1].Date
	}
	var ranged struct {
		Daily []store.DailyRow `json:"daily"`
	}
	getOK(t, h, "/api/v1/stats/daily?from="+from+"&to="+to, &ranged)
	for _, row := range ranged.Daily {
		if row.Date < from || row.Date > to {
			t.Errorf("row %s outside [%s, %s]", row.Date, from, to)
		}
	}
	wantN := 0
	for _, row := range want {
		if row.Date >= from && row.Date <= to {
			wantN++
		}
	}
	if len(ranged.Daily) != wantN {
		t.Errorf("ranged daily has %d rows, want %d", len(ranged.Daily), wantN)
	}
}

func TestAPIStatsDailyBy(t *testing.T) {
	h := seedHub(t)
	for _, by := range []string{"harness", "provider", "model", "project"} {
		var got struct {
			By      string             `json:"by"`
			DailyBy []store.DailyByRow `json:"daily_by"`
		}
		getOK(t, h, "/api/v1/stats/daily?by="+by, &got)
		want, err := h.st.DailyBy(context.Background(), time.UTC, by, "")
		if err != nil {
			t.Fatal(err)
		}
		gj, _ := json.Marshal(got.DailyBy)
		wj, _ := json.Marshal(want)
		if got.By != by || string(gj) != string(wj) {
			t.Errorf("by=%s payload != DailyBy", by)
		}
	}
}

func TestAPITotals(t *testing.T) {
	h := seedHub(t)
	var got struct {
		Window string `json:"window"`
		Days   int    `json:"days"`
		Totals struct {
			store.TokenSums
			store.CostSums
		} `json:"totals"`
	}
	getOK(t, h, "/api/v1/totals", &got)
	want, err := h.st.DailyFromRollups(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var input, cost int64
	for _, row := range want {
		input += row.Input
		cost += row.CostUSDMicro
	}
	if got.Window != "all" || got.Days != len(want) || got.Totals.Input != input || got.Totals.CostUSDMicro != cost {
		t.Errorf("totals: got %+v; want days=%d input=%d cost=%d", got, len(want), input, cost)
	}

	// The fixture corpus is historical: a 1d window over it must be empty
	// (and prove the window arithmetic runs).
	var today struct {
		Days   int `json:"days"`
		Totals struct{ store.TokenSums }
	}
	getOK(t, h, "/api/v1/totals?window=today", &today)
	if today.Days != 0 || today.Totals.Input != 0 {
		t.Errorf("window=today over historical fixtures: %+v, want empty", today)
	}
}

func TestAPIMetaModels(t *testing.T) {
	h := seedHub(t)
	var got struct {
		Models []store.ModelInfo `json:"models"`
	}
	getOK(t, h, "/api/v1/meta/models", &got)
	want, err := h.st.ModelInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got.Models)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("meta/models != ModelInventory\ngot  %s\nwant %s", gj, wj)
	}
	if len(got.Models) == 0 {
		t.Error("fixture-seeded inventory is empty")
	}
	for _, m := range got.Models {
		if m.Basis == "" || m.Model == "" {
			t.Errorf("inventory row missing fields: %+v", m)
		}
	}
}

// TestAPIMethodNotAllowed (Codex M4 finding 5; HEAD ruling, M5 Codex
// round): a known path with a wrong method answers with the JSON
// envelope and "Allow: GET, HEAD" — and HEAD is NOT a wrong method.
// Per RFC 9110 §9.3.2 it is GET without the response body; the mux's
// GET patterns match it by design, so HEAD answers 200 with the GET's
// headers and an empty body. The contract states this; this test pins
// both halves permanently.
func TestAPIMethodNotAllowed(t *testing.T) {
	h := seedHub(t)
	paths := []string{
		"/api/v1/health", "/api/v1/stats/daily", "/api/v1/totals",
		"/api/v1/meta/models", "/api/v1/plans", "/api/v1/stream",
	}
	for _, p := range paths {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequest(method, "http://"+h.Addr()+p, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, p, resp.StatusCode)
			}
			if allow := resp.Header.Get("Allow"); allow != "GET, HEAD" {
				t.Errorf("%s %s: Allow = %q, want \"GET, HEAD\"", method, p, allow)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s: Content-Type %q, want the JSON envelope", method, p, ct)
			}
			var e apiError
			if err := json.Unmarshal(b, &e); err != nil || e.Error.Code != "method_not_allowed" {
				t.Errorf("%s %s: not the error envelope: %s", method, p, b)
			}
		}

		// HEAD: 200, the GET's headers, empty body (net/http strips it;
		// the stream endpoint answers with its SSE headers and is closed
		// by the client like any other consumer).
		req, err := http.NewRequest(http.MethodHead, "http://"+h.Addr()+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD %s = %d, want 200", p, resp.StatusCode)
		}
		if len(b) != 0 {
			t.Errorf("HEAD %s carried a body (%d bytes)", p, len(b))
		}
		wantCT := "application/json"
		if p == "/api/v1/stream" {
			wantCT = "text/event-stream"
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, wantCT) {
			t.Errorf("HEAD %s: Content-Type %q, want %s (the GET's headers)", p, ct, wantCT)
		}
	}
}

// TestAPIPlans (M5 Task 2): the window meter + value panel payload over
// a plan-seeded database — fixture history plus one synthetic event at
// now, so the current window is live. Every number is asserted equal to
// the direct store query + the same window math (the api_test
// discipline).
func TestAPIPlans(t *testing.T) {
	ovPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(ovPath, []byte(`{
		"plans": [{
			"name": "claude-max",
			"matchers": [{"harness": "claude-code"}],
			"window": "5h",
			"weekly_cap_equiv_usd": "120.00",
			"monthly_price_usd": "200.00"
		}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(ovPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	fresh := core.Event{
		ID: core.EventID("claude-code", "plans-now-msg", "plans-now-req"),
		TS: now, Machine: "gx10", SourceKind: core.SourceKindHarnessLog,
		Harness: "claude-code", Provider: "anthropic",
		Model: "claude-fable-5", ModelFamily: "claude-fable-5",
		TokensInput: 1000, TokensOutput: 100, Accuracy: core.AccuracyExact,
	}
	h := seedHubWith(t, ov, []core.Event{fresh})

	var got struct {
		TZ    string `json:"tz"`
		Plans []struct {
			Name                string `json:"name"`
			WindowSeconds       int64  `json:"window_seconds"`
			WindowStart         string `json:"window_start"`
			WeeklyCapEquivMicro *int64 `json:"weekly_cap_equiv_micro"`
			MonthlyPriceMicro   *int64 `json:"monthly_price_micro"`
			CurrentWindow       *struct {
				Start          time.Time `json:"start"`
				End            time.Time `json:"end"`
				Events         int64     `json:"events"`
				Input          int64     `json:"input"`
				EquivMicro     int64     `json:"cost_api_equiv_micro"`
				SecondsToReset int64     `json:"seconds_to_reset"`
			} `json:"current_window"`
			WeekRaw      json.RawMessage `json:"week"`
			MonthRaw     json.RawMessage `json:"month"`
			WindowsTotal int             `json:"windows_total"`
		} `json:"plans"`
		UnmatchedPlanEvents int64 `json:"unmatched_plan_events"`
	}
	getOK(t, h, "/api/v1/plans", &got)
	if len(got.Plans) != 1 || got.TZ != "UTC" || got.UnmatchedPlanEvents != 0 {
		t.Fatalf("plans payload shape: %+v", got)
	}
	p := got.Plans[0]
	if p.Name != "claude-max" || p.WindowSeconds != 5*3600 || p.WindowStart != "floored" ||
		p.WeeklyCapEquivMicro == nil || *p.WeeklyCapEquivMicro != 120_000_000 ||
		p.MonthlyPriceMicro == nil || *p.MonthlyPriceMicro != 200_000_000 {
		t.Fatalf("plan declaration not echoed: %+v", p)
	}

	// Cross-check against the direct store query + the same window math.
	rows, err := h.st.PlanIncludedEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no plan_included rows after plan-seeded ingest")
	}
	we := make([]pricing.WindowEvent, len(rows))
	for i, e := range rows {
		we[i] = pricing.WindowEvent{TS: e.TS, Input: e.Input, EquivMicro: e.EquivMicro, Unpriced: e.Unpriced}
	}
	windows := pricing.PlanWindows(we, 5*time.Hour, pricing.AnchorFloored)
	if p.WindowsTotal != len(windows) {
		t.Errorf("windows_total = %d, want %d", p.WindowsTotal, len(windows))
	}

	// The synthetic at-now event keeps a window open: the meter ticks.
	if p.CurrentWindow == nil {
		t.Fatal("current_window is null despite an event at now")
	}
	cur, ok := pricing.CurrentWindow(windows, time.Now().UTC())
	if !ok {
		t.Fatal("direct window math has no current window but the API does")
	}
	if !p.CurrentWindow.Start.Equal(cur.Start) || !p.CurrentWindow.End.Equal(cur.End) ||
		p.CurrentWindow.Events != cur.Events || p.CurrentWindow.Input != cur.Input ||
		p.CurrentWindow.EquivMicro != cur.EquivMicro {
		t.Errorf("current_window %+v != direct math %+v", p.CurrentWindow, cur)
	}
	if p.CurrentWindow.SecondsToReset <= 0 || p.CurrentWindow.SecondsToReset > 5*3600 {
		t.Errorf("seconds_to_reset = %d, want within (0, 18000]", p.CurrentWindow.SecondsToReset)
	}
	// 1000×$10 + 100×$50 per Mtok = 15000 micro — the fresh event's
	// equivalent is in the current window.
	if p.CurrentWindow.EquivMicro < 15_000 {
		t.Errorf("current window equivalent %d lacks the fresh event's 15000", p.CurrentWindow.EquivMicro)
	}

	// Week and month sums match the direct computation.
	var week, month planPeriodUsage
	if err := json.Unmarshal(p.WeekRaw, &week); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(p.MonthRaw, &month); err != nil {
		t.Fatal(err)
	}
	wantWeek := sumPeriod(rows, time.Now().UTC().Add(-7*24*time.Hour))
	nowUTC := time.Now().UTC()
	wantMonth := sumPeriod(rows, time.Date(nowUTC.Year(), nowUTC.Month(), 1, 0, 0, 0, 0, time.UTC))
	if week.Events != wantWeek.Events || week.EquivMicro != wantWeek.EquivMicro {
		t.Errorf("week = %+v, want %+v", week, wantWeek)
	}
	if month.Events != wantMonth.Events || month.EquivMicro != wantMonth.EquivMicro {
		t.Errorf("month = %+v, want %+v", month, wantMonth)
	}

	// No plans declared: empty payload, not an error.
	plain := seedHub(t)
	var none struct {
		Plans               []json.RawMessage `json:"plans"`
		UnmatchedPlanEvents int64             `json:"unmatched_plan_events"`
	}
	getOK(t, plain, "/api/v1/plans", &none)
	if len(none.Plans) != 0 || none.UnmatchedPlanEvents != 0 {
		t.Fatalf("plan-less hub: %+v", none)
	}
}

func TestAPIErrors(t *testing.T) {
	h := seedHub(t)
	cases := []struct {
		path   string
		status int
	}{
		{"/api/v1/stats/daily?frm=2026-01-01", http.StatusBadRequest},                // unknown param rejected
		{"/api/v1/stats/daily?from=01-01-2026", http.StatusBadRequest},               // bad date
		{"/api/v1/stats/daily?from=2026-02-02&to=2026-01-01", http.StatusBadRequest}, // empty range
		{"/api/v1/stats/daily?by=banana", http.StatusBadRequest},                     // unknown dimension
		{"/api/v1/totals?window=yesterday", http.StatusBadRequest},                   // bad window
		{"/api/v1/totals?window=9999d", http.StatusBadRequest},                       // window too large
		{"/api/v1/health?verbose=1", http.StatusBadRequest},                          // no params on health
		{"/api/v1/nope", http.StatusNotFound},                                        // unknown API path is JSON
		{fmt.Sprintf("/api/v1/stats/daily/%s", "extra"), http.StatusNotFound},
	}
	for _, c := range cases {
		assertErrEnvelope(t, h, c.path, c.status)
	}
}
