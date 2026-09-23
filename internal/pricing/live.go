package pricing

// The live layer: an ADD-ONLY price source BELOW the vendored snapshot.
// The companion script (extension/litellm-refresh) downloads LiteLLM's
// price file daily to $XDG_CONFIG_HOME/tatitok/litellm-live.json (+
// litellm-live.meta.json); the binary itself still makes no network
// calls — it only reads that file.
//
// Add-only, two ways: a key the vendored file carries (priced or not)
// is ignored whatever the live rate, and the layer is consulted only
// when the WHOLE snapshot lookup chain (model, provider/model, family,
// provider/family) misses — so an event the snapshot prices is never
// touched. Owner overrides and the local-provider rule keep their
// precedence over both. Events it prices carry price_snapshot
// "litellm-live-<date>", the UTC date of the meta's fetched_at; the
// rates land in price_rates as usual. Only billing resolution consults
// it: free-basis equivalents and reference targets stay snapshot +
// overrides (reference targets are validated at load, which must not
// depend on a file that changes daily).
//
// A missing or malformed file or meta (including a meta whose sha256 or
// bytes do not match the file) leaves the layer empty with one WARN —
// never a startup failure. A running process picks up a new file
// without restart: resolution stats both files at most once a minute
// and reloads when the mtime or size of either changed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LiveFile and LiveMetaFile are the companion's outputs, next to
// prices.json.
const (
	LiveFile     = "litellm-live.json"
	LiveMetaFile = "litellm-live.meta.json"
)

// liveCheckEvery throttles the reload stat.
const liveCheckEvery = time.Minute

// liveNow is the reload throttle's clock (tests step it).
var liveNow = time.Now

// LivePath resolves litellm-live.json: the directory prices.json lives
// in (XDG_CONFIG_HOME, falling back to ~/.config).
func LivePath(getenv func(string) string, home string) string {
	return filepath.Join(filepath.Dir(OverridesPath(getenv, home)), LiveFile)
}

type liveMeta struct {
	FetchedAt string `json:"fetched_at"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	SourceURL string `json:"source_url"`
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
	mu       sync.Mutex
	path     string // "" = layer off
	metaPath string
	stamps   [2]fileStamp
	checked  time.Time
	rates    map[string]Rates // only keys the vendored snapshot lacks
	version  string           // "litellm-live-<date>"
}

var live liveLayer

// UseLive turns the live layer on for path (litellm-live.json; its meta
// sits beside it) and loads it now; "" turns it off. It never fails:
// an unusable file leaves the layer empty with one WARN.
func UseLive(path string) {
	loadOnce.Do(load)
	live.mu.Lock()
	defer live.mu.Unlock()
	live.path, live.metaPath = path, ""
	live.rates, live.version = nil, ""
	live.stamps = [2]fileStamp{}
	if path == "" {
		return
	}
	live.metaPath = filepath.Join(filepath.Dir(path), LiveMetaFile)
	live.reload(true)
}

// reload re-reads the pair when forced, or — checked at most once per
// liveCheckEvery — when either file's stamp changed. Caller holds mu.
func (l *liveLayer) reload(force bool) {
	now := liveNow()
	if !force && now.Sub(l.checked) < liveCheckEvery {
		return
	}
	l.checked = now
	stamps := [2]fileStamp{stampOf(l.path), stampOf(l.metaPath)}
	if !force && stamps == l.stamps {
		return
	}
	l.stamps = stamps
	rates, version, skipped, err := readLive(l.path, l.metaPath)
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

// readLive loads the file + meta pair. The meta must match the file
// (sha256 and bytes) so the provenance date belongs to exactly these
// rates. Keys the vendored snapshot carries are dropped; so are entries
// with an unusable price (counted in skipped — one bad upstream entry
// never empties the layer).
func readLive(path, metaPath string) (rates map[string]Rates, version string, skipped int, err error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, "", 0, err
	}
	metaBody, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, "", 0, err
	}
	var meta liveMeta
	if err := json.Unmarshal(metaBody, &meta); err != nil {
		return nil, "", 0, fmt.Errorf("%s: %w", LiveMetaFile, err)
	}
	fetched, err := time.Parse(time.RFC3339, meta.FetchedAt)
	if err != nil {
		return nil, "", 0, fmt.Errorf("%s: fetched_at: %w", LiveMetaFile, err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != meta.SHA256 || int64(len(body)) != meta.Bytes {
		return nil, "", 0, fmt.Errorf("%s does not match %s (sha256/bytes differ)", LiveMetaFile, LiveFile)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", 0, fmt.Errorf("%s: %w", LiveFile, err)
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
