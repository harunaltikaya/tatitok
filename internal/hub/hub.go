// Package hub is the long-running serve process (M4): it owns the HTTP
// server now and, in later M4 tasks, the watchers, ingest scheduling and
// the SSE stream. The hub is *a* writer, not *the* writer — every write
// goes through the same store APIs as the CLI, and a concurrent CLI
// ingest against the same database must stay safe (the M3.1 guarantees:
// _txlock=immediate transactions + busy_timeout).
//
// Network posture: the no-runtime-network rule (CLAUDE.md hard rule 7)
// governs OUTBOUND fetches; serving INBOUND on loopback does not violate
// it. The default bind is loopback-only; any other interface requires an
// explicit --addr and draws a startup warning — there is no auth or TLS
// in this milestone.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/harunaltikaya/tatitok/internal/limits"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// DefaultAddr is the loopback-only default bind. Port 8284 avoids the
// ports common local AI / dev tools claim (8000 vLLM/uvicorn, 8080
// llama.cpp, 3000 node dev servers, 5173 vite, 11434 ollama, 1234
// LM Studio, 7860 gradio, 8888 jupyter). A bind failure is fatal with a
// message naming --addr — the hub never falls back to another port
// silently (milestone-4 ground rule).
const DefaultAddr = "127.0.0.1:8284"

// Config is everything the hub needs to start. Precedence is decided by
// the caller (cmd/tatitok: flag > built-in default; --db's default
// honors XDG_DATA_HOME like every other command).
type Config struct {
	DBPath string
	Addr   string
	// WatchTargets are the (adapter, source) pairs the watch layer
	// tails; empty means no watchers (tests, bare hub).
	WatchTargets []WatchTarget
	// Overrides is the user's price-override file (nil = none) —
	// reported at startup, applied by watch ingest.
	Overrides *pricing.Overrides
	// Debounce coalesces rapid changes into one ingest pass
	// (0 = DefaultDebounce); PollInterval drives the polling sources
	// (0 = DefaultPollInterval); Heartbeat is the SSE keep-alive
	// cadence (0 = DefaultHeartbeat).
	Debounce     time.Duration
	PollInterval time.Duration
	Heartbeat    time.Duration
}

// Hub is a started serve process.
type Hub struct {
	cfg Config
	st  *store.Store
	ln  net.Listener
	srv *http.Server
	w   *watcher // nil when no watch targets

	// lim is the display-only reported-usage-limits store (M9): fenced from
	// the event store/pricing/rollups/parity — see internal/limits.
	lim *limits.Store

	// health facts, fixed at Start (M4 Task 2).
	version  string
	snapshot string // pinned price snapshot id
	dbHash   string // sha256 of the absolute DB path — never the path
	started  time.Time

	// live stream (M4 Task 3).
	bcast      *broadcaster
	streamStop chan struct{} // closed at Shutdown so SSE handlers release the drain

	done     chan struct{} // closed when the serve loop returns
	serveErr error         // read only after done is closed
}

// buildVersion: the module version when stamped, else the VCS revision
// (dev builds), else "dev".
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		return rev + dirty
	}
	return "dev"
}

// Start opens the store, binds the listener and begins serving. It
// returns once the hub is up; the caller waits on Done/odd signals and
// calls Shutdown.
func Start(cfg Config) (*Hub, error) {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w — pass --addr HOST:PORT to choose another address (the hub never falls back to a different port silently)", cfg.Addr, err)
	}
	if warn := nonLoopbackWarning(ln.Addr()); warn != "" {
		slog.Warn(warn, "addr", ln.Addr().String())
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		_ = ln.Close()
		return nil, err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	snapshot, err := pricing.SnapshotVersion()
	if err != nil {
		_ = ln.Close()
		_ = st.Close()
		return nil, err
	}

	h := &Hub{
		cfg:        cfg,
		st:         st,
		ln:         ln,
		version:    buildVersion(),
		snapshot:   snapshot,
		dbHash:     dbPathHash(cfg.DBPath),
		started:    time.Now(),
		bcast:      newBroadcaster(),
		streamStop: make(chan struct{}),
		done:       make(chan struct{}),
		lim:        limits.NewStore(),
	}
	mux := http.NewServeMux()
	h.registerDashboard(mux)
	h.registerAPI(mux)
	// Display-only reported usage limits (M9): the handler lives in package
	// limits (stdlib-only) and is handed only the *Store — structurally fenced
	// from the event store / pricing / rollups / parity.
	limits.RegisterHTTP(mux, h.lim)
	h.srv = &http.Server{Handler: mux}
	go func() {
		err := h.srv.Serve(h.ln)
		if !errors.Is(err, http.ErrServerClosed) {
			h.serveErr = err
		}
		close(h.done)
	}()

	roots := make([]string, len(cfg.WatchTargets))
	for i, t := range cfg.WatchTargets {
		roots[i] = t.Source.Harness + ":" + t.Source.Root
	}
	slog.Info("hub started",
		"addr", ln.Addr().String(),
		"db", cfg.DBPath,
		"watch_roots", roots,
		"overrides", cfg.Overrides.Len(),
		"reference_models", cfg.Overrides.References())

	if len(cfg.WatchTargets) > 0 {
		// Watch ingest replaces rows on every live pass by design — the
		// per-event AS-4 line moves into the per-pass summary for this
		// handle (counted, not spammed; CLI ingest keeps per-event lines).
		st.QuietReplacements()
		debounce, poll := cfg.Debounce, cfg.PollInterval
		if debounce <= 0 {
			debounce = DefaultDebounce
		}
		if poll <= 0 {
			poll = DefaultPollInterval
		}
		h.w = startWatcher(st, cfg.Overrides, cfg.WatchTargets, debounce, poll, h.publishPass)
		slog.Info("watchers started", "targets", len(cfg.WatchTargets),
			"debounce", debounce.String(), "poll_interval", poll.String())
	}
	return h, nil
}

// Addr is the actually-bound address (resolves ":0" test listeners).
func (h *Hub) Addr() string { return h.ln.Addr().String() }

// Done is closed when the serve loop exits — on Shutdown or on a serve
// failure; Err distinguishes.
func (h *Hub) Done() <-chan struct{} { return h.done }

// Err reports why the serve loop exited; valid after Done is closed,
// nil for a clean Shutdown.
func (h *Hub) Err() error { return h.serveErr }

// Shutdown drains the hub in dependency order: watchers stop first and
// the in-flight ingest pass completes (hard-aborted only if ctx expires
// — its open per-file transaction rolls back), then the HTTP server
// drains, then the store closes.
func (h *Hub) Shutdown(ctx context.Context) error {
	if h.w != nil {
		h.w.Stop(ctx)
	}
	close(h.streamStop) // release long-lived SSE handlers before the drain
	srvErr := h.srv.Shutdown(ctx)
	<-h.done
	return errors.Join(srvErr, h.serveErr, h.st.Close())
}

// publishPass fans an ingest-pass summary out to SSE clients.
func (h *Hub) publishPass(p passSummary) { h.bcast.publish("ingest_pass", p) }

// nonLoopbackWarning returns the startup warning for a bind that is
// reachable beyond this machine, "" for loopback. Binding non-loopback
// is allowed only because the address was explicitly configured —
// DefaultAddr is loopback and there is no auth or TLS.
func nonLoopbackWarning(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return "serving on a non-loopback interface: tatitok has no auth or TLS — anyone who can reach this address can read your usage data"
	}
	return ""
}
