package parity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const fixtureRoot = "../../testdata/fixtures/claude-code"

func ingestInto(t *testing.T, srcs []adapters.Source) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "parity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sum, err := adapters.IngestBackfill(context.Background(), s, claudecode.Adapter{}, srcs)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	t.Logf("ingested %d files, %d events emitted, %d rows", sum.Files, sum.Emitted, sum.Inserted)
	return s
}

// TestCCUsageDailyParity is the CI conformance gate: one subtest per
// machine fixture dir (gx10 today, the MacBook set post-M1). Exact match
// on the four per-day token sums, in the timezone the expectations were
// captured in (expected/META.json).
func TestCCUsageDailyParity(t *testing.T) {
	machines, err := filepath.Glob(filepath.Join(fixtureRoot, "*", "expected", "ccusage-daily.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) == 0 {
		t.Fatalf("no fixture expectation sets under %s", fixtureRoot)
	}
	for _, expectedPath := range machines {
		machineDir := filepath.Dir(filepath.Dir(expectedPath))
		t.Run(filepath.Base(machineDir), func(t *testing.T) {
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

			root, err := filepath.Abs(filepath.Join(machineDir, "projects"))
			if err != nil {
				t.Fatal(err)
			}
			s := ingestInto(t, []adapters.Source{{
				Harness: "claude-code", Root: root, Machine: filepath.Base(machineDir),
			}})
			got, err := s.Daily(context.Background(), tz)
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
	got, err := s.Daily(context.Background(), time.Local)
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
