package parity

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
	"github.com/harunaltikaya/tatitok/internal/adapters/opencode"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// TestMain silences per-event slog noise in test output (the per-event
// AS-4 replacement lines stay in real ingest; tests summarize replaced
// counts per ingest instead — owner note, M3).
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
		&slog.HandlerOptions{Level: slog.LevelWarn})))
	os.Exit(m.Run())
}

const fixtureRoot = "../../testdata/fixtures/claude-code"

// paritySet describes one harness's fixture layout for the CI gate.
type paritySet struct {
	harness string
	// fixtures live under <fixtureBase>/<machine>/
	fixtureBase string
	adapter     adapters.Adapter
	// root returns the Source.Root for one machine fixture dir. The
	// opencode set reconstructs its database from the committed text
	// fixtures into a temp dir first.
	root func(t *testing.T, machineDir string) string
}

func subdirRoot(sub string) func(*testing.T, string) string {
	return func(t *testing.T, machineDir string) string {
		t.Helper()
		root, err := filepath.Abs(filepath.Join(machineDir, sub))
		if err != nil {
			t.Fatal(err)
		}
		return root
	}
}

func opencodeRoot(t *testing.T, machineDir string) string {
	t.Helper()
	dir, err := filepath.Abs(machineDir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "opencode")
	if _, err := opencode.BuildFixtureDB(dir, filepath.Join(root, "opencode.db")); err != nil {
		t.Fatalf("reconstruct fixture db: %v", err)
	}
	return root
}

var paritySets = []paritySet{
	{"claude-code", "../../testdata/fixtures/claude-code", claudecode.Adapter{}, subdirRoot("projects")},
	{"codex", "../../testdata/fixtures/codex", codex.Adapter{}, subdirRoot("sessions")},
	{"opencode", "../../testdata/fixtures/opencode", opencode.Adapter{}, opencodeRoot},
}

func ingestIntoWith(t *testing.T, a adapters.Adapter, srcs []adapters.Source) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "parity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sum, err := adapters.IngestBackfill(context.Background(), s, a, srcs, nil)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	t.Logf("ingested %d files, %d events emitted, %d rows, %d replaced (in-file streaming updates)",
		sum.Files, sum.Emitted, sum.Inserted, sum.Replaced)
	return s
}

func ingestInto(t *testing.T, srcs []adapters.Source) *store.Store {
	t.Helper()
	return ingestIntoWith(t, claudecode.Adapter{}, srcs)
}

// TestCCUsageDailyParity is the CI conformance gate: one subtest per
// harness and machine fixture dir. Exact match on the four per-day token
// sums, in the timezone the expectations were captured in
// (expected/META.json).
func TestCCUsageDailyParity(t *testing.T) {
	for _, set := range paritySets {
		machines, err := filepath.Glob(filepath.Join(set.fixtureBase, "*", "expected", "ccusage-daily.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(machines) == 0 {
			t.Fatalf("no fixture expectation sets under %s", set.fixtureBase)
		}
		for _, expectedPath := range machines {
			machineDir := filepath.Dir(filepath.Dir(expectedPath))
			name := fmt.Sprintf("%s-%s", set.harness, filepath.Base(machineDir))
			t.Run(name, func(t *testing.T) {
				meta, err := LoadMeta(filepath.Join(machineDir, "expected", "META.json"))
				if err != nil {
					t.Fatal(err)
				}
				tz, err := time.LoadLocation(meta.Timezone.IANA)
				if err != nil {
					t.Fatalf("META timezone: %v", err)
				}
				want, err := LoadExpectedDaily(expectedPath)
				if err != nil {
					t.Fatal(err)
				}

				s := ingestIntoWith(t, set.adapter, []adapters.Source{{
					Harness: set.harness, Root: set.root(t, machineDir),
					Machine: filepath.Base(machineDir),
				}})
				got, err := s.Daily(context.Background(), tz, "")
				if err != nil {
					t.Fatal(err)
				}

				if diff := CompareDaily(got, want); diff != "" {
					t.Fatalf("token parity broken vs ccusage %s — do NOT edit the "+
						"expected file; find the parsing/dedup bug:\n%s",
						meta.CCUsageVersion, diff)
				}
			})
		}
	}
}

// TestParityFull is the owner-run gate against the full real logs
// (`make parity-full`). It RECAPTURES ccusage output from the live logs
// at comparison time — pinned version from the fixture META.json — and
// never reads a committed or on-disk -full expectation file. Days bucket
// in the machine's local timezone on both sides (ccusage's rule).
func TestParityFull(t *testing.T) {
	if os.Getenv("TATITOK_PARITY_FULL") != "1" {
		t.Skip("owner-run full parity: make parity-full (needs live ~/.claude logs + npx)")
	}
	meta, err := LoadMeta(filepath.Join(fixtureRoot, "gx10", "expected", "META.json"))
	if err != nil {
		t.Fatal(err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	srcs, err := claudecode.Adapter{}.Detect(adapters.Probe{
		Getenv: os.Getenv, HomeDir: home, Machine: "parity-full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) == 0 {
		t.Fatal("no live claude-code log roots found")
	}
	for _, s := range srcs {
		t.Logf("live root: %s", s.Root)
	}

	// Recapture from the live logs, never from a stored -full file.
	cmd := exec.Command("npx", "-y", "ccusage@"+meta.CCUsageVersion,
		"claude", "daily", "--json", "--offline")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ccusage recapture failed: %v", err)
	}
	want, err := ParseDailyJSON(out)
	if err != nil {
		t.Fatalf("ccusage output: %v", err)
	}

	s := ingestInto(t, srcs)
	got, err := s.Daily(context.Background(), time.Local, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff := CompareDaily(got, want); diff != "" {
		t.Fatalf("full-history token parity broken vs ccusage %s:\n%s",
			meta.CCUsageVersion, diff)
	}
	t.Logf("full-history parity holds across %d days (ccusage %s)",
		len(want.Daily), meta.CCUsageVersion)
}

// TestParityFullCodex is the owner-run gate against the full real codex
// logs (`make parity-full-codex`). Same recapture discipline as
// TestParityFull: pinned ccusage version from the codex fixture META,
// live `ccusage codex daily` at comparison time, never an on-disk -full
// file.
func TestParityFullCodex(t *testing.T) {
	if os.Getenv("TATITOK_PARITY_FULL_CODEX") != "1" {
		t.Skip("owner-run full parity: make parity-full-codex (needs live ~/.codex logs + npx)")
	}
	meta, err := LoadMeta(filepath.Join("../../testdata/fixtures/codex", "gx10", "expected", "META.json"))
	if err != nil {
		t.Fatal(err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	srcs, err := codex.Adapter{}.Detect(adapters.Probe{
		Getenv: os.Getenv, HomeDir: home, Machine: "parity-full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) == 0 {
		t.Fatal("no live codex log roots found")
	}
	for _, s := range srcs {
		t.Logf("live root: %s", s.Root)
	}

	// Recapture from the live logs, never from a stored -full file.
	cmd := exec.Command("npx", "-y", "ccusage@"+meta.CCUsageVersion,
		"codex", "daily", "--json", "--offline")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ccusage recapture failed: %v", err)
	}
	want, err := ParseDailyJSON(out)
	if err != nil {
		t.Fatalf("ccusage output: %v", err)
	}

	s := ingestIntoWith(t, codex.Adapter{}, srcs)
	got, err := s.Daily(context.Background(), time.Local, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff := CompareDaily(got, want); diff != "" {
		t.Fatalf("full-history codex token parity broken vs ccusage %s:\n%s",
			meta.CCUsageVersion, diff)
	}
	t.Logf("full-history codex parity holds across %d days (ccusage %s)",
		len(want.Daily), meta.CCUsageVersion)
}

// TestParityFullOpencode is the owner-run gate against the full live
// opencode store (`make parity-full-opencode`). Same recapture
// discipline: pinned ccusage version from the opencode fixture META,
// recaptured at comparison time, never an on-disk -full file.
//
// Unlike the JSONL gates, the source is a SQLite database OpenCode may be
// writing to. The live store is therefore read exactly once, briefly —
// `VACUUM INTO` a temp snapshot — and BOTH sides compare against that
// snapshot: ccusage via the HOME-override isolation the harvest uses (its
// opencode discovery is HOME-anchored; XDG_DATA_HOME ignored but set
// consistently; npm cache pinned so npx still resolves the pinned
// version), the adapter via Detect against the snapshot home. No long
// cursor ever sits on the live db, and a mid-test OpenCode write cannot
// desync the two sides.
func TestParityFullOpencode(t *testing.T) {
	if os.Getenv("TATITOK_PARITY_FULL_OPENCODE") != "1" {
		t.Skip("owner-run full parity: make parity-full-opencode (needs the live opencode.db + npx)")
	}
	ctx := context.Background()
	meta, err := LoadMeta(filepath.Join("../../testdata/fixtures/opencode", "gx10", "expected", "META.json"))
	if err != nil {
		t.Fatal(err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	srcs, err := opencode.Adapter{}.Detect(adapters.Probe{
		Getenv: os.Getenv, HomeDir: home, Machine: "parity-full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) == 0 {
		t.Fatal("no live opencode message store found")
	}
	for _, s := range srcs {
		t.Logf("live root: %s", s.Root)
	}
	// M3 Task 5 containment: ccusage merges a legacy storage/ tree with
	// opencode.db; our adapter reads only the db. With a populated legacy
	// tree the comparison would diverge BY DESIGN — skip explicitly
	// instead of failing mysteriously. The merge is a fixture-gated task
	// the day a real legacy tree appears (MacBook candidate).
	if present, files := opencode.HasLegacyStorageTree(srcs[0].Root); present {
		t.Skipf("legacy OpenCode storage tree present (%d files under %s) — "+
			"multi-store merge unsupported (no real fixtures; fabrication forbidden); "+
			"parity vs ccusage would diverge by design. See docs/format-notes.md.",
			files, filepath.Join(srcs[0].Root, "storage"))
	}

	// Snapshot the live store ONCE; everything below reads the snapshot.
	snapHome := t.TempDir()
	snapDB := filepath.Join(snapHome, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(snapDB), 0o755); err != nil {
		t.Fatal(err)
	}
	live, err := sql.Open("sqlite", fmt.Sprintf(
		"file:%s?mode=ro&_pragma=busy_timeout(5000)",
		filepath.Join(srcs[0].Root, "opencode.db")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = live.ExecContext(ctx, `VACUUM INTO ?`, snapDB)
	_ = live.Close()
	if err != nil {
		t.Fatalf("snapshot live store: %v", err)
	}

	// Recapture from the snapshot, never from a stored -full file.
	cmd := exec.Command("npx", "-y", "ccusage@"+meta.CCUsageVersion,
		"opencode", "daily", "--json", "--offline")
	cmd.Env = overrideEnv(map[string]string{
		"HOME":             snapHome,
		"XDG_DATA_HOME":    filepath.Join(snapHome, ".local", "share"),
		"npm_config_cache": filepath.Join(home, ".npm"),
	})
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ccusage recapture failed: %v", err)
	}
	want, err := ParseDailyJSON(out)
	if err != nil {
		t.Fatalf("ccusage output: %v", err)
	}

	snapSrcs, err := opencode.Adapter{}.Detect(adapters.Probe{
		Getenv:  func(string) string { return "" },
		HomeDir: snapHome, Machine: "parity-full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapSrcs) == 0 {
		t.Fatal("snapshot store not detected")
	}
	s := ingestIntoWith(t, opencode.Adapter{}, snapSrcs)
	got, err := s.Daily(ctx, time.Local, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff := CompareDaily(got, want); diff != "" {
		t.Fatalf("full-history opencode token parity broken vs ccusage %s:\n%s",
			meta.CCUsageVersion, diff)
	}
	t.Logf("full-history opencode parity holds across %d days (ccusage %s)",
		len(want.Daily), meta.CCUsageVersion)
}

// overrideEnv returns the current environment with the given variables
// replaced (not appended — libc getenv takes the FIRST occurrence, so a
// duplicate HOME would silently win or lose by libc implementation).
func overrideEnv(overrides map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := overrides[k]; ok {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}
