package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/limits"
)

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:12345", true},
		{"127.0.0.5:80", true},
		{"[::1]:443", true},
		{"192.168.1.5:80", false},
		{"10.0.0.1:1234", false},
		{"203.0.113.7:9999", false},
		{"0.0.0.0:1", false},
		{"garbage", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.remote); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.remote, got, c.want)
		}
	}
}

const sampleLimits = `{
  "claude": {"fetchedAt": 1700000000000, "windows": [
    {"label": "All models", "usedPercent": 42, "resetAt": 1700000500000},
    {"label": "Sonnet", "usedPercent": 7.5, "resetAt": 1700001000000}
  ]},
  "codex": {"fetchedAt": 1700000000001, "windows": [
    {"label": "5h", "usedPercent": 90, "resetAt": 1700000300000}
  ]}
}`

// limitsHub builds a Hub with only the display-only limits store wired (the
// handlers touch nothing else on Hub), plus a mux carrying just the limits
// routes — enough to exercise registration, methods and the loopback gate.
func limitsHub() (*Hub, *httptest.Server) {
	h := &Hub{lim: limits.NewStore()}
	mux := http.NewServeMux()
	h.registerLimits(mux)
	return h, httptest.NewServer(mux)
}

func TestLimitsGetEmptyIsCleanNotError(t *testing.T) {
	_, srv := limitsHub()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/limits")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET (empty) = %d, want 200 (empty is not an error)", resp.StatusCode)
	}
	var got struct {
		Providers limits.Snapshot `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Providers == nil {
		t.Error(`"providers" decoded to nil — want a present (empty) object so the frontend empty-state works`)
	}
	if len(got.Providers) != 0 {
		t.Errorf("empty store returned %d providers, want 0", len(got.Providers))
	}
}

func TestLimitsRoundTrip(t *testing.T) {
	_, srv := limitsHub()
	defer srv.Close()

	// httptest.Server binds 127.0.0.1, so this POST arrives over loopback.
	resp, err := http.Post(srv.URL+"/api/v1/limits", "application/json", strings.NewReader(sampleLimits))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST (loopback, valid) = %d, want 204 (body %q)", resp.StatusCode, body)
	}

	resp, err = http.Get(srv.URL + "/api/v1/limits")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got struct {
		Providers limits.Snapshot `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Providers) != 2 {
		t.Fatalf("round-tripped %d providers, want 2", len(got.Providers))
	}
	claude := got.Providers["claude"]
	if claude.FetchedAt != 1700000000000 || len(claude.Windows) != 2 {
		t.Errorf("claude round-trip = %+v, want fetchedAt 1700000000000 with 2 windows", claude)
	}
	if got.Providers["codex"].Windows[0].UsedPercent != 90 {
		t.Errorf("codex 5h usedPercent = %v, want 90", got.Providers["codex"].Windows[0].UsedPercent)
	}
}

func TestLimitsPostRejectsNonLoopback(t *testing.T) {
	// httptest.Server is always loopback, so drive the handler directly with a
	// crafted non-loopback RemoteAddr to exercise the 403 gate.
	h := &Hub{lim: limits.NewStore()}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/limits", strings.NewReader(sampleLimits))
	req.RemoteAddr = "203.0.113.7:9999" // TEST-NET-3, not loopback
	rec := httptest.NewRecorder()

	h.apiLimitsPost(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback POST = %d, want 403", rec.Code)
	}
	if h.lim.Get() != nil {
		t.Error("non-loopback POST mutated the store — it must be rejected before Set")
	}
}

func TestLimitsPostMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"wrong shape (provider is a string)", `{"claude": "nope"}`},
		{"percent out of range", `{"claude":{"fetchedAt":1,"windows":[{"label":"a","usedPercent":150}]}}`},
		{"negative percent", `{"claude":{"fetchedAt":1,"windows":[{"label":"a","usedPercent":-3}]}}`},
		{"empty window label", `{"claude":{"fetchedAt":1,"windows":[{"label":"","usedPercent":3}]}}`},
		{"negative resetAt", `{"claude":{"fetchedAt":1,"windows":[{"label":"a","usedPercent":3,"resetAt":-9}]}}`},
		{"not json", `{not json`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Hub{lim: limits.NewStore()}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/limits", bytes.NewReader([]byte(c.body)))
			req.RemoteAddr = "127.0.0.1:5555"
			rec := httptest.NewRecorder()

			h.apiLimitsPost(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("malformed POST = %d, want 400", rec.Code)
			}
			if h.lim.Get() != nil {
				t.Error("malformed POST mutated the store")
			}
		})
	}
}

func TestLimitsWrongMethod(t *testing.T) {
	_, srv := limitsHub()
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/limits", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "POST") || !strings.Contains(allow, "GET") {
		t.Errorf("Allow = %q, want it to list GET and POST", allow)
	}
}
