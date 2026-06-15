package limits

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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
		// Fail closed: a RemoteAddr without a parseable host:port is rejected,
		// including a bare loopback IP with no port.
		{"127.0.0.1", false},
		{"::1", false},
		{"garbage", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.remote); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.remote, got, c.want)
		}
	}
}

const sampleJSON = `{
  "claude": {"fetchedAt": 1700000000000, "windows": [
    {"label": "All models", "usedPercent": 42, "resetAt": 1700000500000},
    {"label": "Sonnet", "usedPercent": 7.5, "resetAt": 1700001000000}
  ]},
  "codex": {"fetchedAt": 1700000000001, "windows": [
    {"label": "5h", "usedPercent": 90, "resetAt": 1700000300000}
  ]}
}`

// limitsServer mounts the routes on a loopback httptest server (so POSTs arrive
// over loopback) and returns the backing store for assertions.
func limitsServer(t *testing.T) (*Store, *httptest.Server) {
	t.Helper()
	st := NewStore()
	mux := http.NewServeMux()
	RegisterHTTP(mux, st)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return st, srv
}

// postLoopback drives handler.post directly with a loopback RemoteAddr — used
// where we want to assert the store was (or was not) mutated.
func postLoopback(t *testing.T, st *Store, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := &handler{st: st}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/limits", bytes.NewReader([]byte(body)))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.post(rec, req)
	return rec
}

func TestLimitsGetEmptyIsCleanNotError(t *testing.T) {
	_, srv := limitsServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/limits")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET (empty) = %d, want 200 (empty is not an error)", resp.StatusCode)
	}
	var got struct {
		Providers Snapshot `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Providers == nil {
		t.Error(`"providers" decoded to nil — want a present (empty) object for the frontend empty-state`)
	}
	if len(got.Providers) != 0 {
		t.Errorf("empty store returned %d providers, want 0", len(got.Providers))
	}
}

func TestLimitsRoundTrip(t *testing.T) {
	_, srv := limitsServer(t)

	resp, err := http.Post(srv.URL+"/api/v1/limits", "application/json", strings.NewReader(sampleJSON))
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
		Providers Snapshot `json:"providers"`
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
	st := NewStore()
	h := &handler{st: st}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/limits", strings.NewReader(sampleJSON))
	req.RemoteAddr = "203.0.113.7:9999" // TEST-NET-3, not loopback
	rec := httptest.NewRecorder()

	h.post(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback POST = %d, want 403", rec.Code)
	}
	if st.Get() != nil {
		t.Error("non-loopback POST mutated the store — it must be rejected before Set")
	}
}

func TestLimitsPostMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"wrong shape (provider is a string)", `{"claude": "nope"}`},
		{"negative percent", `{"claude":{"fetchedAt":1,"windows":[{"label":"a","usedPercent":-3}]}}`},
		{"empty window label", `{"claude":{"fetchedAt":1,"windows":[{"label":"","usedPercent":3}]}}`},
		{"negative resetAt", `{"claude":{"fetchedAt":1,"windows":[{"label":"a","usedPercent":3,"resetAt":-9}]}}`},
		{"not json", `{not json`},
		{"null body", `null`},
		{"trailing second object", `{"claude":{"fetchedAt":1,"windows":[]}} {}`},
		{"trailing garbage", `{"claude":{"fetchedAt":1,"windows":[]}} oops`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := NewStore()
			rec := postLoopback(t, st, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("malformed POST = %d, want 400", rec.Code)
			}
			if st.Get() != nil {
				t.Error("malformed POST mutated the store")
			}
		})
	}
}

// TestLimitsPostNullDoesNotClear pins the harden-parsing rule: a JSON null body
// is a 400 and must NOT clear an already-stored snapshot.
func TestLimitsPostNullDoesNotClear(t *testing.T) {
	st := NewStore()
	st.Set(sample())

	rec := postLoopback(t, st, `null`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("null POST = %d, want 400", rec.Code)
	}
	if got := st.Get(); !reflect.DeepEqual(got, sample()) {
		t.Errorf("null POST changed the stored snapshot:\ngot  %+v\nwant %+v", got, sample())
	}
}

// TestLimitsPostOver100RoundTrips: a provider-reported over-cap percent is
// stored verbatim, not rejected (Validate loosened; the frontend clamps later).
func TestLimitsPostOver100RoundTrips(t *testing.T) {
	st := NewStore()
	rec := postLoopback(t, st, `{"codex":{"fetchedAt":1,"windows":[{"label":"5h","usedPercent":150,"resetAt":0}]}}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("over-100 POST = %d, want 204 (over-cap is stored truthfully)", rec.Code)
	}
	if got := st.Get()["codex"].Windows[0].UsedPercent; got != 150 {
		t.Errorf("stored usedPercent = %v, want 150 (verbatim)", got)
	}
}

func TestLimitsWrongMethod(t *testing.T) {
	_, srv := limitsServer(t)
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
