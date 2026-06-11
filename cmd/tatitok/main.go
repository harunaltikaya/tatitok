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

	// Embed the IANA zone database: day bucketing (--timezone) must work
	// on hosts without tzdata — Windows has none, minimal containers strip
	// it. The embedded copy is the fallback; a host database still wins.
	_ "time/tzdata"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
	"github.com/harunaltikaya/tatitok/internal/adapters/opencode"
	"github.com/harunaltikaya/tatitok/internal/modelmap"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const usageText = `tatitok — local-first AI token usage tracker

Usage:
  tatitok ingest --backfill [--db PATH] [--source claude-code|codex|opencode]
  tatitok stats  --daily [--by harness|provider|model|project] | --session
                 [--json] [--db PATH] [--timezone TZ] [--harness NAME]
  tatitok doctor --scan-content [--db PATH] [LITERAL...]
  tatitok doctor --provenance [--db PATH] [--json]
  tatitok doctor --pricing [--db PATH]
  tatitok recompute --provenance [--dry-run] [--db PATH] [--source NAME]
  tatitok recompute --model-map  [--dry-run] [--db PATH]
  tatitok recompute --pricing    [--dry-run] [--db PATH]

ingest with no --source runs every detected adapter and reports per
source. stats buckets days in the local timezone by default (ccusage's
rule); pass --timezone for like-for-like comparisons across machines;
--harness restricts the report, and daily JSON output carries a
per-harness breakdown.
doctor --scan-content re-checks every stored record against the
sanitizer invariants; extra LITERAL arguments are also grepped for and
must not appear anywhere in stored raw/meta.
doctor --provenance lists stored row counts by adapter@version.
doctor --pricing reconciles our computed costs against source-reported
costs (opencode store-and-compare) — a report, never a correction.
recompute --provenance re-reads the source files through the current
adapters and fills NULL adapter_version/source-link columns on stored
events — after verifying each stored payload is identical to the
re-parse (differences are reported, never altered). recompute
--model-map re-normalizes every stored model_family under the current
model map — the ONLY operation that ever changes a historical
model_family (raw model stays immutable). recompute --pricing
re-derives every cost column under the current price snapshot +
overrides — the ONLY operation that ever changes a historical cost.
All are explicit and logged, never a side effect (PRD AS-4); --dry-run
prints the plan and changes nothing.`

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
	case "recompute":
		err = cmdRecompute(args[1:])
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

// allAdapters is the registry; ingest with no --source runs every one.
var allAdapters = []adapters.Adapter{
	claudecode.Adapter{}, codex.Adapter{}, opencode.Adapter{},
}

func adapterFor(name string) (adapters.Adapter, error) {
	for _, a := range allAdapters {
		if a.Name() == name {
			return a, nil
		}
	}
	return nil, fmt.Errorf("unknown --source %q (supported: claude-code, codex, opencode)", name)
}

func realProbe() adapters.Probe {
	home, _ := os.UserHomeDir()
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return adapters.Probe{Getenv: os.Getenv, HomeDir: home, Machine: host}
}

// loadPriceOverrides reads the user's price-override file (FR-9.2);
// missing file = no overrides, malformed file = hard error.
func loadPriceOverrides(probe adapters.Probe) (*pricing.Overrides, error) {
	path := pricing.OverridesPath(probe.Getenv, probe.HomeDir)
	ov, err := pricing.LoadOverrides(path)
	if err != nil {
		return nil, err
	}
	if ov.Len() > 0 {
		slog.Info("price overrides loaded", "path", path, "models", ov.Len())
	}
	return ov, nil
}

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	backfill := fs.Bool("backfill", false, "ingest full history from detected log roots")
	dbPath := fs.String("db", defaultDBPath(), "database path")
	source := fs.String("source", "", "adapter to ingest from (default: every detected adapter)")
	_ = fs.Parse(args)
	if !*backfill {
		return fmt.Errorf("ingest currently requires --backfill (live tail is a later milestone)")
	}

	// No --source: run every registered adapter over whatever it detects;
	// adapters with nothing to ingest are reported, not errors.
	selected := allAdapters
	if *source != "" {
		a, err := adapterFor(*source)
		if err != nil {
			return err
		}
		selected = []adapters.Adapter{a}
	}

	st, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	probe := realProbe()
	overrides, err := loadPriceOverrides(probe)
	if err != nil {
		return err
	}
	ctx := context.Background()
	ingested, skippedTotal := 0, 0
	for _, a := range selected {
		srcs, err := a.Detect(probe)
		if err != nil {
			return err
		}
		if len(srcs) == 0 {
			if *source != "" {
				return fmt.Errorf("no %s log roots found", a.Name())
			}
			fmt.Printf("%-12s no log roots detected — skipped\n", a.Name()+":")
			continue
		}
		for _, s := range srcs {
			slog.Info("ingesting", "adapter", a.Name(), "root", s.Root)
		}
		sum, err := adapters.IngestBackfill(ctx, st, a, srcs, overrides)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		ingested++
		fmt.Printf("%-12s ingested %d files (%d lines): %d events emitted, %d new rows, %d parse errors\n",
			a.Name()+":", sum.Files, sum.Lines, sum.Emitted, sum.Inserted, sum.ParseErrors)
		if sum.Replaced > 0 {
			// AS-4: a stored number changed because the source row itself
			// changed (mutable store finalized an in-flight turn) — say so.
			fmt.Printf("%-12s %d events replaced (source rows changed since last ingest)\n",
				a.Name()+":", sum.Replaced)
		}
		if sum.EmptyModel > 0 {
			fmt.Printf("%-12s %d events carry no model (usage before the first turn_context; see doctor --provenance)\n",
				a.Name()+":", sum.EmptyModel)
		}
		if sum.Skipped > 0 {
			fmt.Printf("%-12s WARNING: %d sources skipped (unreadable) — totals are incomplete\n",
				a.Name()+":", sum.Skipped)
			skippedTotal += sum.Skipped
		}
	}
	if ingested == 0 {
		return fmt.Errorf("no log roots found for any adapter (claude-code, codex, opencode)")
	}
	if skippedTotal > 0 {
		// Distinct from parse errors and from exit 0: the run finished,
		// but unreadable sources mean the DB is missing history (they are
		// recorded in the sources table with their read error).
		return exitError{code: 3, msg: fmt.Sprintf(
			"ingest complete with %d skipped sources (see warnings above)", skippedTotal)}
	}
	return nil
}

func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	daily := fs.Bool("daily", false, "per-day token sums")
	session := fs.Bool("session", false, "per-session token sums")
	by := fs.String("by", "", "break the daily report down by one dimension (harness, provider, model, project)")
	asJSON := fs.Bool("json", false, "JSON output")
	dbPath := fs.String("db", defaultDBPath(), "database path")
	tzName := fs.String("timezone", "local", "IANA timezone for day bucketing")
	harness := fs.String("harness", "", "restrict to one harness (claude-code, codex, opencode)")
	_ = fs.Parse(args)
	if *daily == *session {
		return fmt.Errorf("pass exactly one of --daily or --session")
	}
	if *by != "" && !*daily {
		return fmt.Errorf("--by applies to --daily only")
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
	switch {
	case *daily && *by != "":
		rows, err := st.DailyBy(ctx, tz, *by, *harness)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(map[string]any{"by": *by, "daily_by": rows})
		}
		printDailyByTable(*by, rows)
		return nil
	case *daily:
		// UTC daily is served from the pre-aggregated rollup table —
		// byte-equal to direct aggregation by construction (property
		// tested); other timezones aggregate events exactly (rollup days
		// are UTC buckets; M3 decision).
		var rows []store.DailyRow
		if tz.String() == "UTC" {
			rows, err = st.DailyFromRollups(ctx, *harness)
		} else {
			rows, err = st.Daily(ctx, tz, *harness)
		}
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(map[string]any{"daily": rows})
		}
		printDailyTable(rows)
		return nil
	}
	rows, err := st.Sessions(ctx, tz, *harness)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(map[string]any{"sessions": rows})
	}
	printSessionTable(rows)
	return nil
}

// cmdRecompute is the explicit recompute entrypoint (PRD AS-4: historical
// numbers never change as a side effect; recompute is a command, logged).
// M3 Task 0 ships --provenance; --model-map and --rollups join in later
// M3 tasks.
func cmdRecompute(args []string) error {
	fs := flag.NewFlagSet("recompute", flag.ExitOnError)
	provenance := fs.Bool("provenance", false, "fill NULL adapter_version/source-link columns from the source files")
	modelMap := fs.Bool("model-map", false, "re-normalize stored model_family under the current model map")
	prices := fs.Bool("pricing", false, "re-derive cost columns under the current price snapshot + overrides")
	rollups := fs.Bool("rollups", false, "rebuild rollup_daily from the event table")
	dryRun := fs.Bool("dry-run", false, "print the plan and change nothing")
	dbPath := fs.String("db", defaultDBPath(), "database path")
	source := fs.String("source", "", "restrict to one adapter (claude-code, codex, opencode)")
	_ = fs.Parse(args)
	modes := 0
	for _, m := range []bool{*provenance, *modelMap, *prices, *rollups} {
		if m {
			modes++
		}
	}
	if modes != 1 {
		return fmt.Errorf("pass exactly one of --provenance, --model-map, --pricing or --rollups")
	}
	if *modelMap {
		return cmdRecomputeModelMap(*dbPath, *dryRun)
	}
	if *prices {
		return cmdRecomputePricing(*dbPath, *dryRun)
	}
	if *rollups {
		return cmdRecomputeRollups(*dbPath, *dryRun)
	}

	selected := allAdapters
	if *source != "" {
		a, err := adapterFor(*source)
		if err != nil {
			return err
		}
		selected = []adapters.Adapter{a}
	}

	st, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	// Plan listing first, in every mode.
	gaps, err := st.LineageGaps(ctx)
	if err != nil {
		return err
	}
	work := printRecomputePlan(gaps, *source)
	if !work {
		fmt.Println("provenance complete — nothing to recompute")
		return nil
	}
	if *dryRun {
		fmt.Println("\ndry run — no changes made")
		return nil
	}

	needy, err := st.NullProvenanceIDs(ctx)
	if err != nil {
		return err
	}
	slog.Info("recompute --provenance starting", "db", *dbPath,
		"events_missing_provenance", len(needy))

	// Only adapters whose harness actually has gaps re-read their logs.
	hasWork := map[string]bool{}
	for _, g := range gaps {
		if !g.Empty() {
			hasWork[g.Harness] = true
		}
	}

	probe := realProbe()
	var sum adapters.RecomputeSummary
	ran := 0
	for _, a := range selected {
		if !hasWork[a.Name()] {
			fmt.Printf("%-12s no provenance gaps — not re-read\n", a.Name()+":")
			continue
		}
		srcs, err := a.Detect(probe)
		if err != nil {
			return err
		}
		if len(srcs) == 0 {
			fmt.Printf("%-12s no log roots detected — skipped\n", a.Name()+":")
			continue
		}
		for _, s := range srcs {
			slog.Info("recompute re-reading", "adapter", a.Name(), "root", s.Root)
		}
		if err := adapters.RecomputeProvenance(ctx, st, a, srcs, needy, &sum); err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		ran++
	}
	if ran == 0 {
		return fmt.Errorf("no log roots found for any selected adapter — nothing re-read")
	}
	return printRecomputeResults(st, ctx, sum, needy, *source)
}

// cmdRecomputeModelMap is the explicit model_family re-normalization
// path (M3 Task 1) — the only operation that ever changes a historical
// model_family. Raw model strings are untouched by construction.
func cmdRecomputeModelMap(dbPath string, dryRun bool) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	plan, err := st.PlanModelMap(ctx, modelmap.Version())
	if err != nil {
		return err
	}
	fmt.Printf("model map version: %d\n", plan.CurrentVersion)
	fmt.Printf("events: %s — not on current map version: %s, model_family values that would change: %s\n",
		formatTokens(plan.Events), formatTokens(plan.Stale), formatTokens(plan.FamilyChanges))
	if plan.Stale == 0 && plan.FamilyChanges == 0 {
		fmt.Println("model_family is current — nothing to recompute")
		return nil
	}
	if dryRun {
		fmt.Println("\ndry run — no changes made")
		return nil
	}

	slog.Info("recompute --model-map starting", "db", dbPath,
		"map_version", plan.CurrentVersion, "stale_events", plan.Stale,
		"family_changes", plan.FamilyChanges)
	restamped, changed, err := st.RecomputeModelMap(ctx, modelmap.Version())
	if err != nil {
		return err
	}
	slog.Info("recompute --model-map complete",
		"events_restamped", restamped, "model_family_changed", changed)
	fmt.Printf("\nre-stamped %s events under map version %d; %s model_family values changed\n",
		formatTokens(restamped), plan.CurrentVersion, formatTokens(changed))
	return nil
}

// cmdRecomputePricing re-derives the cost columns for the whole history
// under the current snapshot + overrides (FR-9.5 explicit recompute).
func cmdRecomputePricing(dbPath string, dryRun bool) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	version, err := pricing.SnapshotVersion()
	if err != nil {
		return err
	}
	plan, err := st.PlanPricing(ctx, version)
	if err != nil {
		return err
	}
	fmt.Printf("price snapshot: %s\n", plan.SnapshotVersion)
	fmt.Printf("events: %s — never priced: %s, on current snapshot/override: %s, on another snapshot: %s\n",
		formatTokens(plan.Events), formatTokens(plan.Unpriced),
		formatTokens(plan.OnCurrent), formatTokens(plan.OnOther))
	if dryRun {
		fmt.Println("\ndry run — no changes made (note: rows on the current snapshot may still be re-stamped if the override file changed)")
		return nil
	}

	overrides, err := loadPriceOverrides(realProbe())
	if err != nil {
		return err
	}
	slog.Info("recompute --pricing starting", "db", dbPath,
		"snapshot", version, "overrides", overrides.Len())
	res, err := st.RecomputePricing(ctx, overrides)
	if err != nil {
		return err
	}
	slog.Info("recompute --pricing complete", "repriced", res.Repriced,
		"cost_changed", res.CostChanged)
	fmt.Printf("\nrepriced %s events (%s cost values changed) under %s\n",
		formatTokens(res.Repriced), formatTokens(res.CostChanged), version)
	for basis, n := range res.ByBasis {
		fmt.Printf("  %-14s %s\n", basis, formatTokens(n))
	}
	return nil
}

// cmdRecomputeRollups rebuilds the rollup table from events (explicit;
// the triggers keep it correct incrementally — this normalizes version
// columns and recovers from anything unforeseen).
func cmdRecomputeRollups(dbPath string, dryRun bool) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	rollupRows, events, err := st.RollupCounts(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("rollup_daily: %s rows over %s events — full rebuild from the event table\n",
		formatTokens(rollupRows), formatTokens(events))
	if dryRun {
		fmt.Println("\ndry run — no changes made")
		return nil
	}
	slog.Info("recompute --rollups starting", "db", dbPath,
		"rollup_rows", rollupRows, "events", events)
	rows, err := st.RecomputeRollups(ctx)
	if err != nil {
		return err
	}
	slog.Info("recompute --rollups complete", "rollup_rows", rows)
	fmt.Printf("\nrebuilt rollup_daily: %s rows\n", formatTokens(rows))
	return nil
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	scan := fs.Bool("scan-content", false, "verify no prompt/response text is stored")
	provenance := fs.Bool("provenance", false, "list row counts by adapter@version")
	prices := fs.Bool("pricing", false, "reconcile our computed costs against source-reported costs (opencode)")
	asJSON := fs.Bool("json", false, "JSON output (with --provenance)")
	dbPath := fs.String("db", defaultDBPath(), "database path")
	_ = fs.Parse(args)
	modes := 0
	for _, m := range []bool{*scan, *provenance, *prices} {
		if m {
			modes++
		}
	}
	if modes != 1 {
		return fmt.Errorf("pass exactly one of --scan-content, --provenance or --pricing")
	}
	st, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if *provenance {
		return doctorProvenance(context.Background(), st, *asJSON)
	}
	if *prices {
		return doctorPricing(context.Background(), st)
	}
	return doctorScanContent(context.Background(), st, fs.Args())
}
