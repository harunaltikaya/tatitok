// Command tatitok is a local-first AI token usage tracker.
// Milestone 1: ingest Claude Code logs into SQLite and report daily stats.
//
// Stdlib flag instead of cobra: three subcommands with a handful of flags
// each don't justify the dependency in M1 (CLAUDE.md leaves the call open).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const usageText = `tatitok — local-first AI token usage tracker

Usage:
  tatitok ingest --backfill [--db PATH] [--source claude-code]
  tatitok stats  --daily|--session [--json] [--db PATH] [--timezone TZ]
  tatitok doctor --scan-content [--db PATH] [LITERAL...]

stats buckets days in the local timezone by default (ccusage's rule);
pass --timezone for like-for-like comparisons across machines.
doctor --scan-content re-checks every stored record against the
sanitizer invariants; extra LITERAL arguments are also grepped for and
must not appear anywhere in stored raw/meta.`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usageText)
		return 2
	}
	var err error
	switch args[0] {
	case "ingest":
		err = cmdIngest(args[1:])
	case "stats":
		err = cmdStats(args[1:])
	case "doctor":
		err = cmdDoctor(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "tatitok: unknown command %q\n\n%s\n", args[0], usageText)
		return 2
	}
	if err != nil {
		var ec exitError
		if errAs(err, &ec) {
			fmt.Fprintln(os.Stderr, "tatitok:", ec.msg)
			return ec.code
		}
		fmt.Fprintln(os.Stderr, "tatitok:", err)
		return 1
	}
	return 0
}

// exitError carries a non-default exit code (doctor findings → 1 vs
// operational failure).
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

func errAs(err error, target *exitError) bool {
	e, ok := err.(exitError)
	if ok {
		*target = e
	}
	return ok
}

func defaultDBPath() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "tatitok", "tatitok.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "tatitok.db"
	}
	return filepath.Join(home, ".local", "share", "tatitok", "tatitok.db")
}

func openStore(path string) (*store.Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return store.Open(path)
}

// registry of available adapters; M1 ships exactly one.
func adapterFor(name string) (adapters.Adapter, error) {
	switch name {
	case "claude-code":
		return claudecode.Adapter{}, nil
	default:
		return nil, fmt.Errorf("unknown --source %q (M1 supports: claude-code)", name)
	}
}

func realProbe() adapters.Probe {
	home, _ := os.UserHomeDir()
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return adapters.Probe{Getenv: os.Getenv, HomeDir: home, Machine: host}
}

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	backfill := fs.Bool("backfill", false, "ingest full history from detected log roots")
	dbPath := fs.String("db", defaultDBPath(), "database path")
	source := fs.String("source", "claude-code", "adapter to ingest from")
	_ = fs.Parse(args)
	if !*backfill {
		return fmt.Errorf("ingest currently requires --backfill (live tail is a later milestone)")
	}

	a, err := adapterFor(*source)
	if err != nil {
		return err
	}
	srcs, err := a.Detect(realProbe())
	if err != nil {
		return err
	}
	if len(srcs) == 0 {
		return fmt.Errorf("no %s log roots found (set CLAUDE_CONFIG_DIR to override)", a.Name())
	}
	st, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	for _, s := range srcs {
		slog.Info("ingesting", "adapter", a.Name(), "root", s.Root)
	}
	sum, err := adapters.IngestBackfill(context.Background(), st, a, srcs)
	if err != nil {
		return err
	}
	fmt.Printf("ingested %d files (%d lines): %d events emitted, %d new rows, %d parse errors\n",
		sum.Files, sum.Lines, sum.Emitted, sum.Inserted, sum.ParseErrors)
	if sum.Skipped > 0 {
		// Distinct from parse errors and from exit 0: the run finished,
		// but unreadable sources mean the DB is missing history (they are
		// recorded in the sources table with their read error).
		fmt.Printf("WARNING: %d sources skipped (unreadable) — totals are incomplete\n",
			sum.Skipped)
		return exitError{code: 3, msg: fmt.Sprintf(
			"ingest complete with %d skipped sources (see warnings above)", sum.Skipped)}
	}
	return nil
}

func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	daily := fs.Bool("daily", false, "per-day token sums")
	session := fs.Bool("session", false, "per-session token sums")
	asJSON := fs.Bool("json", false, "JSON output")
	dbPath := fs.String("db", defaultDBPath(), "database path")
	tzName := fs.String("timezone", "local", "IANA timezone for day bucketing")
	_ = fs.Parse(args)
	if *daily == *session {
		return fmt.Errorf("pass exactly one of --daily or --session")
	}

	tz := time.Local
	if *tzName != "local" {
		var err error
		if tz, err = time.LoadLocation(*tzName); err != nil {
			return fmt.Errorf("timezone %q: %w", *tzName, err)
		}
	}
	st, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if *daily {
		rows, err := st.Daily(ctx, tz)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(map[string]any{"daily": rows})
		}
		printDailyTable(rows)
		return nil
	}
	rows, err := st.Sessions(ctx, tz)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(map[string]any{"sessions": rows})
	}
	printSessionTable(rows)
	return nil
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	scan := fs.Bool("scan-content", false, "verify no prompt/response text is stored")
	dbPath := fs.String("db", defaultDBPath(), "database path")
	_ = fs.Parse(args)
	if !*scan {
		return fmt.Errorf("doctor currently supports only --scan-content")
	}
	st, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	return doctorScanContent(context.Background(), st, fs.Args())
}
