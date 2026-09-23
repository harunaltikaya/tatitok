package pricing

// The live layer: an ADD-ONLY price source BELOW the vendored snapshot.
// The companion script (extension/litellm-refresh) downloads LiteLLM's
// price file daily to $XDG_CONFIG_HOME/tatitok/litellm-live.json; the
// binary itself still makes no network calls — it only reads that file.
// The file is one JSON object, {"fetched_at", "source_url", "sha256",
// "bytes", "prices"}, where prices is the upstream object unchanged; the
// companion replaces it with a single atomic rename, so a reader sees
// either the old file or the new one, never half of either.
//
// Add-only, two ways: a key the vendored file carries (priced or not)
// is ignored whatever the live rate, and the layer is consulted only
// when the WHOLE snapshot lookup chain (model, provider/model, family,
// provider/family) misses — so an event the snapshot prices is never
// touched. Owner overrides and the local-provider rule keep their
// precedence over both. Events it prices carry price_snapshot
// "litellm-live-<date>", the UTC date of the file's fetched_at; the
// rates land in price_rates as usual. Only billing resolution consults
// it: free-basis equivalents and reference targets stay snapshot +
// overrides (reference targets are validated at load, which must not
// depend on a file that changes daily).
//
// A missing or unusable file (not JSON, no "prices" object, a bad
// fetched_at) leaves the layer empty with one WARN — never a startup
// failure. A running process picks up a new file without restart:
// resolution stats it at most once a minute and reloads when its mtime
// or size changed.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LiveFile is the companion's output, next to prices.json.
const LiveFile = "litellm-live.json"

// liveCheckEvery throttles the reload stat.
const liveCheckEvery = time.Minute

// liveNow is the reload throttle's clock (tests step it).
var liveNow = time.Now

// LivePath resolves litellm-live.json: the directory prices.json lives
// in (XDG_CONFIG_HOME, falling back to ~/.config).
func LivePath(getenv func(string) string, home string) string {
	return filepath.Join(filepath.Dir(OverridesPath(getenv, home)), LiveFile)
}

// liveDoc is the part of the file the hub reads; source_url, sha256 and
// bytes serve the companion's unchanged check and whoever reads the file.
type liveDoc struct {
	FetchedAt string          `json:"fetched_at"`
	Prices    json.RawMessage `json:"prices"`
}

// fileStamp is what the reload check compares: existence, size, mtime.
type fileStamp struct {
	exists bool
	size   int64
	mtime  int64 // UnixNano
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, size: fi.Size(), mtime: fi.ModTime().UnixNano()}
}

// liveLayer is the process-wide layer; mu guards every field (lookups
// run only on snapshot misses, so the lock is off the hot path).
type liveLayer struct {
	mu      sync.Mutex
	path    string // "" = layer off
	stamp   fileStamp
	checked time.Time
	rates   map[string]Rates // only keys the vendored snapshot lacks
	version string           // "litellm-live-<date>"
}

var live liveLayer

// UseLive turns the live layer on for path (litellm-live.json) and
// loads it now; "" turns it off. It never fails: an unusable file
// leaves the layer empty with one WARN.
func UseLive(path string) {
	loadOnce.Do(load)
	live.mu.Lock()
	defer live.mu.Unlock()
	live.path = path
	live.rates, live.version = nil, ""
	live.stamp = fileStamp{}
	if path == "" {
		return
	}
	live.reload(true)
}

// reload re-reads the file when forced, or — checked at most once per
// liveCheckEvery — when its stamp changed. Caller holds mu.
func (l *liveLayer) reload(force bool) {
	now := liveNow()
	if !force && now.Sub(l.checked) < liveCheckEvery {
		return
	}
	l.checked = now
	stamp := stampOf(l.path)
	if !force && stamp == l.stamp {
		return
	}
	l.stamp = stamp
	rates, version, skipped, err := readLive(l.path)
	if err != nil {
		l.rates, l.version = nil, ""
		slog.Warn("pricing: live price layer empty — snapshot only", "path", l.path, "err", err)
		return
	}
	l.rates, l.version = rates, version
	slog.Info("pricing: live price layer loaded", "path", l.path, "version", version,
		"added_models", len(rates), "unusable_entries", skipped)
}

// lookup resolves rates from the live layer with the snapshot's key
// chain; version is the provenance to stamp.
func (l *liveLayer) lookup(provider, model, family string) (r Rates, version string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return Rates{}, "", false
	}
	l.reload(false)
	for _, key := range []string{model, provider + "/" + model, family, provider + "/" + family} {
		if r, ok := l.rates[key]; ok {
			return r, l.version, true
		}
	}
	return Rates{}, "", false
}

// readLive loads the file: fetched_at dates the provenance, prices holds
// the upstream entries. Keys the vendored snapshot carries are dropped;
// so are entries with an unusable price (counted in skipped — one bad
// upstream entry never empties the layer).
func readLive(path string) (rates map[string]Rates, version string, skipped int, err error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, "", 0, err
	}
	var doc liveDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, "", 0, fmt.Errorf("%s: %w", LiveFile, err)
	}
	if len(doc.Prices) == 0 {
		return nil, "", 0, fmt.Errorf("%s: no \"prices\" object", LiveFile)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc.Prices, &raw); err != nil || raw == nil {
		return nil, "", 0, fmt.Errorf("%s: \"prices\" is not an object", LiveFile)
	}
	fetched, err := time.Parse(time.RFC3339, doc.FetchedAt)
	if err != nil {
		return nil, "", 0, fmt.Errorf("%s: fetched_at: %w", LiveFile, err)
	}
	rates = make(map[string]Rates)
	for key, entry := range raw {
		if snapKeys[key] {
			continue // add-only: the vendored snapshot decides this key
		}
		r, ok, err := parseEntry(key, entry)
		if err != nil {
			skipped++
			continue
		}
		if ok {
			rates[key] = r
		}
	}
	return rates, "litellm-live-" + fetched.UTC().Format("2006-01-02"), skipped, nil
}
