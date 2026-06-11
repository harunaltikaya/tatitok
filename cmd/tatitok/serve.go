package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/hub"
)

// shutdownGrace bounds the drain on SIGINT/SIGTERM: in-flight HTTP
// requests (and, from Task 1, the in-flight ingest batch) get this long
// to complete before the process gives up and exits.
const shutdownGrace = 10 * time.Second

// cmdServe runs the hub until SIGINT/SIGTERM. Config precedence: flag >
// built-in default. --db defaults from XDG_DATA_HOME exactly like every
// other command; --addr defaults to hub.DefaultAddr (loopback-only —
// binding anything else is an explicit choice and draws a warning).
// There are no environment variables or config files for serve in M4.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", defaultDBPath(), "database path")
	addr := fs.String("addr", hub.DefaultAddr, "listen address (HOST:PORT; default is loopback-only)")
	_ = fs.Parse(args)

	probe := realProbe()
	overrides, err := loadPriceOverrides(probe)
	if err != nil {
		return err
	}
	// Detect watch roots up front so the startup line reports them; the
	// Task 1 watchers will consume the same list. No roots is not an
	// error for a server — sessions may appear after it starts.
	var roots []adapters.Source
	for _, a := range allAdapters {
		srcs, err := a.Detect(probe)
		if err != nil {
			return err
		}
		roots = append(roots, srcs...)
	}

	h, err := hub.Start(hub.Config{
		DBPath: *dbPath, Addr: *addr,
		WatchRoots: roots, Overrides: overrides,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
		stop() // restore default signal handling: a second ^C kills immediately
		slog.Info("signal received — shutting down", "grace", shutdownGrace.String())
	case <-h.Done():
		// Serve loop died on its own (listener failure) — shut down what
		// remains and surface the cause.
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := h.Shutdown(sctx); err != nil {
		return err
	}
	slog.Info("hub stopped")
	return nil
}
