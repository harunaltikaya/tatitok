package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/harunaltikaya/tatitok/internal/hub"
)

// shutdownGrace bounds the drain on SIGINT/SIGTERM: in-flight HTTP
// requests (and, from Task 1, the in-flight ingest batch) get this long
// to complete before the process gives up and exits.
const shutdownGrace = 10 * time.Second

// cmdServe runs the hub until SIGINT/SIGTERM. Config precedence: flag >
// built-in default. --db defaults from XDG_DATA_HOME exactly like every
// other command; --addr defaults to hub.DefaultAddr (loopback-only — a
// non-loopback bind is refused, tatitok is local-only with no auth/TLS).
// There are no environment variables or config files for serve in M4.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", defaultDBPath(), "database path")
	addr := fs.String("addr", hub.DefaultAddr, "listen address (HOST:PORT; must be loopback — a non-loopback bind is refused)")
	debounce := fs.Duration("debounce", hub.DefaultDebounce, "coalesce window: rapid log changes become one ingest pass")
	pollEvery := fs.Duration("poll-interval", hub.DefaultPollInterval, "polling interval (opencode store; fsnotify fallback)")
	install := fs.Bool("install", false, "write, enable and start the systemd user service "+serviceName+" (runs this binary's serve)")
	uninstall := fs.Bool("uninstall", false, "stop, disable and remove the systemd user service "+serviceName)
	_ = fs.Parse(args)

	if *install || *uninstall {
		if fs.NFlag() != 1 || fs.NArg() != 0 {
			return errors.New("serve --install and --uninstall take no other flags or arguments")
		}
		unitDir, err := serviceUnitDir(os.Getenv)
		if err != nil {
			return err
		}
		if *uninstall {
			return uninstallService(unitDir, systemctlUser, os.Stdout)
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return installService(unitDir, exe, serveLogPath(), os.Getenv, systemctlUser, os.Stdout)
	}

	probe := realProbe()
	overrides, err := loadPriceOverrides(probe)
	if err != nil {
		return err
	}
	// The live layer re-reads litellm-live.json on change (stat at most
	// once a minute), so a running hub prices new models without restart.
	useLivePrices(probe)
	// Detect watch targets up front; the startup line reports the roots.
	// No roots is not an error for a server — sessions may appear after
	// it starts (and a later serve restart picks the harness up).
	var targets []hub.WatchTarget
	for _, a := range allAdapters {
		srcs, err := a.Detect(probe)
		if err != nil {
			return err
		}
		for _, s := range srcs {
			targets = append(targets, hub.WatchTarget{Adapter: a, Source: s})
		}
	}

	h, err := hub.Start(hub.Config{
		DBPath: *dbPath, Addr: *addr,
		WatchTargets: targets, Overrides: overrides,
		Debounce: *debounce, PollInterval: *pollEvery,
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
