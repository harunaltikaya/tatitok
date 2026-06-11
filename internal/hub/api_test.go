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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
	"github.com/harunaltikaya/tatitok/internal/adapters/opencode"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// seedHub starts a hub (no watchers) on a DB freshly ingested from all
// three harness fixtures.
func seedHub(t *testing.T) *Hub {
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
		if _, err := adapters.IngestBackfill(ctx, st, in.a, []adapters.Source{in.src}, nil); err != nil {
			t.Fatalf("%s seed: %v", in.src.Harness, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	h, err := Start(Config{DBPath: dbPath, Addr: "127.0.0.1:0"})
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

// TestAPIMethodNotAllowed (Codex M4 finding 5): a known path with the
// wrong method answers with the JSON envelope and an Allow header — the
// api.go contract, not the mux's text/plain default.
func TestAPIMethodNotAllowed(t *testing.T) {
	h := seedHub(t)
	paths := []string{
		"/api/v1/health", "/api/v1/stats/daily", "/api/v1/totals",
		"/api/v1/meta/models", "/api/v1/stream",
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
			if allow := resp.Header.Get("Allow"); allow != "GET" {
				t.Errorf("%s %s: Allow = %q, want GET", method, p, allow)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s: Content-Type %q, want the JSON envelope", method, p, ct)
			}
			var e apiError
			if err := json.Unmarshal(b, &e); err != nil || e.Error.Code != "method_not_allowed" {
				t.Errorf("%s %s: not the error envelope: %s", method, p, b)
			}
		}
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
