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

	"github.com/harunaltikaya/tatitok/internal/adapters"
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
	// WatchRoots are the log roots detected at startup. Task 0 logs
	// them; the Task 1 watchers will consume them.
	WatchRoots []adapters.Source
	// Overrides is the user's price-override file (nil = none) —
	// reported at startup, used by ingest from Task 1 on.
	Overrides *pricing.Overrides
}

// Hub is a started serve process.
type Hub struct {
	cfg Config
	st  *store.Store
	ln  net.Listener
	srv *http.Server

	done     chan struct{} // closed when the serve loop returns
	serveErr error         // read only after done is closed
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "tatitok hub running — API and dashboard arrive in later M4 tasks")
	})

	h := &Hub{
		cfg:  cfg,
		st:   st,
		ln:   ln,
		srv:  &http.Server{Handler: mux},
		done: make(chan struct{}),
	}
	go func() {
		err := h.srv.Serve(h.ln)
		if !errors.Is(err, http.ErrServerClosed) {
			h.serveErr = err
		}
		close(h.done)
	}()

	roots := make([]string, len(cfg.WatchRoots))
	for i, s := range cfg.WatchRoots {
		roots[i] = s.Harness + ":" + s.Root
	}
	slog.Info("hub started",
		"addr", ln.Addr().String(),
		"db", cfg.DBPath,
		"watch_roots", roots,
		"overrides", cfg.Overrides.Len(),
		"reference_models", cfg.Overrides.References())
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

// Shutdown drains the hub in dependency order: HTTP server first
// (in-flight requests complete within ctx), then the store closes.
// Task 1 inserts "watchers stopped, in-flight ingest batch completes"
// ahead of the drain.
func (h *Hub) Shutdown(ctx context.Context) error {
	srvErr := h.srv.Shutdown(ctx)
	<-h.done
	return errors.Join(srvErr, h.serveErr, h.st.Close())
}

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
