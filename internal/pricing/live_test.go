package pricing

// Live-layer tests: synthetic litellm-live.json files in a temp dir.
// The layer is process-wide, so every test turns it off again.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// liveOnly is a key the vendored snapshot lacks (asserted per test).
const liveOnly = "tatitok-live-test-model"

type liveEntry map[string]any

// writeLive writes a litellm-live.json holding entries as its prices
// into dir and returns its path.
func writeLive(t *testing.T, dir string, entries map[string]liveEntry, fetchedAt string) string {
	t.Helper()
	prices, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return writeLivePrices(t, dir, prices, fetchedAt)
}

// writeLivePrices wraps prices verbatim the way the companion does.
func writeLivePrices(t *testing.T, dir string, prices []byte, fetchedAt string) string {
	t.Helper()
	head, err := json.Marshal(map[string]any{
		"fetched_at": fetchedAt, "source_url": "https://example.invalid/prices.json",
		"sha256": "not-checked-by-the-hub", "bytes": len(prices),
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := append(append(head[:len(head)-1], `, "prices": `...), prices...)
	return writeLiveFile(t, dir, append(doc, "}\n"...))
}

// writeLiveFile writes content verbatim as dir's litellm-live.json.
func writeLiveFile(t *testing.T, dir string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, LiveFile)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// warnCounter is a slog handler that counts WARN records.
type warnCounter struct{ n int }

func (w *warnCounter) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnCounter) WithAttrs([]slog.Attr) slog.Handler       { return w }
func (w *warnCounter) WithGroup(string) slog.Handler            { return w }
func (w *warnCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		w.n++
	}
	return nil
}

// countWarns routes the default logger to a warnCounter until cleanup.
func countWarns(t *testing.T) *warnCounter {
	t.Helper()
	prev := slog.Default()
	w := &warnCounter{}
	slog.SetDefault(slog.New(w))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return w
}

// useLiveForTest turns the layer on for path with a steppable clock and
// turns it off (real clock) at cleanup.
func useLiveForTest(t *testing.T, path string) *time.Time {
	t.Helper()
	clock := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	liveNow = func() time.Time { return clock }
	t.Cleanup(func() {
		UseLive("")
		liveNow = time.Now
	})
	UseLive(path)
	return &clock
}

func resolve(t *testing.T, provider, model, family string, ov *Overrides) Quote {
	t.Helper()
	q, err := Resolve(provider, model, family, time.Time{}, ov)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func assertSnapshotLacks(t *testing.T, key string) {
	t.Helper()
	loadOnce.Do(load)
	if snapKeys[key] {
		t.Fatalf("test key %q is in the vendored snapshot — pick another", key)
	}
}

var newModelRates = liveEntry{
	"input_cost_per_token": 2e-06, "output_cost_per_token": 8e-06,
	"cache_read_input_token_cost": 2e-07,
}

func TestLiveNoFileIsSnapshotOnly(t *testing.T) {
	assertSnapshotLacks(t, liveOnly)
	base := resolve(t, "anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", nil)
	useLiveForTest(t, filepath.Join(t.TempDir(), LiveFile)) // nothing there
	if q := resolve(t, "anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", nil); q.Snapshot != base.Snapshot || *q.Rates != *base.Rates {
		t.Fatalf("snapshot key with no live file: %+v, want %+v", q, base)
	}
	if q := resolve(t, "acme", liveOnly, liveOnly, nil); q.Basis != BasisUnknown || q.Rates != nil {
		t.Fatalf("snapshot miss with no live file: %+v, want unknown", q)
	}
}

func TestLiveNewKeyIsPricedWithLiveProvenance(t *testing.T) {
	assertSnapshotLacks(t, liveOnly)
	path := writeLive(t, t.TempDir(), map[string]liveEntry{liveOnly: newModelRates}, "2026-09-24T03:04:05Z")
	useLiveForTest(t, path)

	q := resolve(t, "acme", liveOnly, liveOnly, nil)
	want := Rates{Input: 2_000_000, Output: 8_000_000, CacheRead: 200_000}
	if q.Basis != BasisAPIPrice || q.Rates == nil || *q.Rates != want {
		t.Fatalf("live key: %+v, want api_price %+v", q, want)
	}
	if q.Snapshot != "litellm-live-2026-09-24" {
		t.Fatalf("provenance %q, want litellm-live-2026-09-24", q.Snapshot)
	}
	// The key chain is the snapshot's: provider/model and family resolve too.
	if q := resolve(t, "acme", "tatitok-live-test-model-0924", liveOnly, nil); q.Basis != BasisAPIPrice {
		t.Fatalf("live key via family: %+v", q)
	}

	// Through Apply: cost, price_snapshot and price_rates as usual.
	e := core.Event{ID: "e1", Harness: "opencode", Provider: "acme", Model: liveOnly,
		ModelFamily: liveOnly, TokensInput: 1_000_000, TokensOutput: 1_000_000}
	if err := Apply(&e, nil); err != nil {
		t.Fatal(err)
	}
	if e.CostUSDMicro == nil || *e.CostUSDMicro != 10_000_000 || e.CostBasis != "api_price" ||
		e.PriceSnapshot != "litellm-live-2026-09-24" {
		t.Fatalf("Apply: cost %v basis %s snapshot %s", e.CostUSDMicro, e.CostBasis, e.PriceSnapshot)
	}
	if !strings.Contains(string(e.PriceRates), `"input":2000000`) {
		t.Fatalf("price_rates %s lacks the live rates", e.PriceRates)
	}
}

func TestLiveNeverTouchesSnapshotKeys(t *testing.T) {
	base := resolve(t, "anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", nil)
	cheap := liveEntry{"input_cost_per_token": 1e-09, "output_cost_per_token": 1e-09}
	path := writeLive(t, t.TempDir(), map[string]liveEntry{
		"claude-sonnet-4-6": cheap, // a snapshot key with a different live rate
		liveOnly:            cheap, // a live-only raw model whose family the snapshot prices
	}, "2026-09-24T03:04:05Z")
	useLiveForTest(t, path)

	if q := resolve(t, "anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", nil); q.Snapshot != base.Snapshot || *q.Rates != *base.Rates {
		t.Fatalf("snapshot key: %+v, want the snapshot's %+v", *q.Rates, *base.Rates)
	}
	// The snapshot prices this event through its family; the live file's
	// exact raw-model key must not win (the layer runs only on a full miss).
	if q := resolve(t, "anthropic", liveOnly, "claude-sonnet-4-6", nil); q.Snapshot != base.Snapshot || *q.Rates != *base.Rates {
		t.Fatalf("snapshot family hit: %+v, want the snapshot's %+v", q, *base.Rates)
	}
}

func TestLiveMalformedIsIgnored(t *testing.T) {
	assertSnapshotLacks(t, liveOnly)
	good := `{"` + liveOnly + `": {"input_cost_per_token": 2e-06}}`
	cases := map[string]func(dir string){
		"not json": func(dir string) {
			writeLiveFile(t, dir, []byte(`{"fetched_at": "2026-09-24T03:04:05Z", "prices": {oops`))
		},
		"not an object": func(dir string) {
			writeLiveFile(t, dir, []byte(`["`+liveOnly+`"]`))
		},
		"null": func(dir string) {
			writeLiveFile(t, dir, []byte(`null`))
		},
		"no prices": func(dir string) {
			writeLiveFile(t, dir, []byte(`{"fetched_at": "2026-09-24T03:04:05Z"}`))
		},
		"prices null": func(dir string) {
			writeLivePrices(t, dir, []byte(`null`), "2026-09-24T03:04:05Z")
		},
		"prices an array": func(dir string) {
			writeLivePrices(t, dir, []byte(`[`+good+`]`), "2026-09-24T03:04:05Z")
		},
		"prices a string": func(dir string) {
			writeLivePrices(t, dir, []byte(`"`+liveOnly+`"`), "2026-09-24T03:04:05Z")
		},
		"bad fetched_at": func(dir string) {
			writeLivePrices(t, dir, []byte(good), "yesterday")
		},
		// The earlier two-file format: the bare upstream object.
		"old format": func(dir string) {
			writeLiveFile(t, dir, []byte(good))
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeLive(t, dir, map[string]liveEntry{liveOnly: newModelRates}, "2026-09-24T03:04:05Z")
			spoil(dir)
			warns := countWarns(t)
			clock := useLiveForTest(t, path)
			if q := resolve(t, "acme", liveOnly, liveOnly, nil); q.Basis != BasisUnknown {
				t.Fatalf("%s: %+v, want unknown (layer empty)", name, q)
			}
			if q := resolve(t, "anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", nil); q.Basis != BasisAPIPrice {
				t.Fatalf("%s: snapshot pricing broke: %+v", name, q)
			}
			// The unchanged bad file is not re-read, so it warns once.
			*clock = clock.Add(2 * liveCheckEvery)
			resolve(t, "acme", liveOnly, liveOnly, nil)
			if warns.n != 1 {
				t.Fatalf("%s: %d WARNs, want 1", name, warns.n)
			}
		})
	}
	// One unusable entry skips that entry, not the layer.
	path := writeLive(t, t.TempDir(), map[string]liveEntry{
		liveOnly:                     newModelRates,
		"tatitok-live-test-negative": {"input_cost_per_token": -1e-06},
	}, "2026-09-24T03:04:05Z")
	useLiveForTest(t, path)
	if q := resolve(t, "acme", liveOnly, liveOnly, nil); q.Basis != BasisAPIPrice {
		t.Fatalf("good entry beside a bad one: %+v", q)
	}
	if q := resolve(t, "acme", "tatitok-live-test-negative", "tatitok-live-test-negative", nil); q.Basis != BasisUnknown {
		t.Fatalf("negative price entry: %+v, want unknown", q)
	}
}

func TestLiveReloadsOnChange(t *testing.T) {
	assertSnapshotLacks(t, liveOnly)
	dir := t.TempDir()
	path := writeLive(t, dir, map[string]liveEntry{liveOnly: newModelRates}, "2026-09-24T03:04:05Z")
	clock := useLiveForTest(t, path)

	writeLive(t, dir, map[string]liveEntry{liveOnly: {
		"input_cost_per_token": 3e-06, "output_cost_per_token": 9e-06,
	}}, "2026-09-25T03:04:05Z")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	// Within the minute: no stat, the old layer answers.
	*clock = clock.Add(30 * time.Second)
	if q := resolve(t, "acme", liveOnly, liveOnly, nil); q.Rates.Input != 2_000_000 || q.Snapshot != "litellm-live-2026-09-24" {
		t.Fatalf("before the check interval: %+v", q)
	}
	// Past it: the changed mtime triggers a reload.
	*clock = clock.Add(31 * time.Second)
	if q := resolve(t, "acme", liveOnly, liveOnly, nil); q.Rates.Input != 3_000_000 || q.Snapshot != "litellm-live-2026-09-25" {
		t.Fatalf("after the change: %+v, want input 3000000 under litellm-live-2026-09-25", q)
	}

	// A file that disappears empties the layer at the next check.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	if q := resolve(t, "acme", liveOnly, liveOnly, nil); q.Basis != BasisUnknown {
		t.Fatalf("after removal: %+v, want unknown", q)
	}
}

// The earlier format's meta file is no longer read: a mismatched one
// beside a good file changes nothing.
func TestLiveIgnoresStaleMetaFile(t *testing.T) {
	assertSnapshotLacks(t, liveOnly)
	dir := t.TempDir()
	path := writeLive(t, dir, map[string]liveEntry{liveOnly: newModelRates}, "2026-09-24T03:04:05Z")
	stale := []byte(`{"fetched_at":"2020-01-01T00:00:00Z","sha256":"00","bytes":1}`)
	if err := os.WriteFile(filepath.Join(dir, "litellm-live.meta.json"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	useLiveForTest(t, path)
	if q := resolve(t, "acme", liveOnly, liveOnly, nil); q.Basis != BasisAPIPrice || q.Snapshot != "litellm-live-2026-09-24" {
		t.Fatalf("beside a stale meta file: %+v", q)
	}
}

func TestLiveOverridesAndLocalStillWin(t *testing.T) {
	assertSnapshotLacks(t, liveOnly)
	path := writeLive(t, t.TempDir(), map[string]liveEntry{liveOnly: newModelRates}, "2026-09-24T03:04:05Z")
	useLiveForTest(t, path)

	ov := loadOverridesJSON(t, `{"prices": {"`+liveOnly+`": {
		"input_usd_per_mtok": "0.50", "output_usd_per_mtok": "1.50"}}}`)
	q := resolve(t, "acme", liveOnly, liveOnly, ov)
	if q.Snapshot != "override" || q.Rates.Input != 500_000 || q.Rates.Output != 1_500_000 {
		t.Fatalf("override over a live key: %+v, want the override's rates", q)
	}
	if q := resolve(t, "vllm", liveOnly, liveOnly, nil); q.Basis != BasisLocal {
		t.Fatalf("local provider over a live key: %+v, want local", q)
	}
}
