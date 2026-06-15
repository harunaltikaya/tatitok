package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/limits"
)

// TestLimitsEndpointWired confirms the display-only limits endpoints are
// actually mounted on the hub's mux by Start (via limits.RegisterHTTP) and
// reachable alongside the /api/ catch-all: GET starts clean-empty, a loopback
// POST stores (204), and the next GET round-trips it. The exhaustive handler
// behaviour (loopback gate, validation, parsing, methods) is unit-tested in
// package limits — this is only the wiring guard.
func TestLimitsEndpointWired(t *testing.T) {
	h := startHub(t, filepath.Join(t.TempDir(), "hub.db"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	}()
	base := "http://" + h.Addr() + "/api/v1/limits"

	// GET before any POST → 200 with a present, empty providers object.
	resp, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET (empty) = %d, want 200", resp.StatusCode)
	}
	var empty struct {
		Providers limits.Snapshot `json:"providers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&empty)
	_ = resp.Body.Close()
	if empty.Providers == nil || len(empty.Providers) != 0 {
		t.Errorf("empty GET providers = %v, want a present empty object", empty.Providers)
	}

	// A local POST arrives over loopback (the hub binds 127.0.0.1) → 204.
	body := `{"claude":{"fetchedAt":1700000000000,"windows":[{"label":"All models","usedPercent":42,"resetAt":1700000500000}]}}`
	resp, err = http.Post(base, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST = %d, want 204", resp.StatusCode)
	}

	// GET after POST → the stored snapshot.
	resp, err = http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Providers limits.Snapshot `json:"providers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	_ = resp.Body.Close()
	if len(got.Providers) != 1 || got.Providers["claude"].Windows[0].UsedPercent != 42 {
		t.Errorf("after POST, GET providers = %+v, want claude All models @42", got.Providers)
	}
}
