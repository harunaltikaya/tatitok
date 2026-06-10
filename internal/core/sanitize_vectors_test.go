package core

// Sanitizer contract vectors (M2 Task 1): SYNTHETIC raw records — the one
// place fabricated inputs are explicitly allowed, because the sanitizer is
// a pure function — with frozen expected outputs. The Go sanitizer and the
// Python harvest sanitizer must both reproduce the expected bytes exactly,
// which holds the two implementations of the shared rule-spec
// (sanitize_rules.json) in lockstep.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const vectorsDir = "../../testdata/sanitizer-vectors"

// TestSanitizerContractVectors runs every contract vector through the Go
// sanitizer and demands byte-equality with the committed expected output.
// TATITOK_UPDATE_VECTORS=1 regenerates the expected files — review the
// diff deliberately and re-run scripts/harvest_fixtures.py --check-vectors
// before committing (both implementations must agree on the new bytes).
func TestSanitizerContractVectors(t *testing.T) {
	raws, err := filepath.Glob(filepath.Join(vectorsDir, "contract", "*.raw.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raws) == 0 {
		t.Fatalf("no contract vectors under %s", vectorsDir)
	}
	for _, rawPath := range raws {
		name := strings.TrimSuffix(filepath.Base(rawPath), ".raw.json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(rawPath)
			if err != nil {
				t.Fatal(err)
			}
			got, err := SanitizeRaw(raw)
			if err != nil {
				t.Fatalf("SanitizeRaw: %v", err)
			}

			// Sanitized output must always pass the invariant scanner.
			findings, err := CheckRawSanitized(got)
			if err != nil {
				t.Fatalf("CheckRawSanitized: %v", err)
			}
			if len(findings) > 0 {
				t.Fatalf("sanitized vector fails the invariant scan: %v", findings)
			}

			expPath := strings.TrimSuffix(rawPath, ".raw.json") + ".expected.json"
			if os.Getenv("TATITOK_UPDATE_VECTORS") == "1" {
				if err := os.WriteFile(expPath, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s", expPath)
				return
			}
			want, err := os.ReadFile(expPath)
			if err != nil {
				t.Fatalf("missing expected vector (generate via TATITOK_UPDATE_VECTORS=1): %v", err)
			}
			if string(got)+"\n" != string(want) {
				t.Fatalf("sanitizer output diverges from frozen vector %s:\ngot:  %s\nwant: %s",
					filepath.Base(expPath), got, strings.TrimSuffix(string(want), "\n"))
			}
		})
	}
}

// TestSanitizerCrossImplementation runs the Python sanitizer over the SAME
// vectors (contract mode + the harvest-mode vectors with the pinned salt)
// via harvest_fixtures.py --check-vectors. Both implementations asserting
// byte-equality against the same committed files proves they are
// byte-equal to each other. Skips when python3 is unavailable (the Go-side
// check above still ran); the harvest workflow always runs the Python side
// (the script self-tests before touching real logs).
func TestSanitizerCrossImplementation(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available — Go-side vector check still enforced")
	}
	script, err := filepath.Abs("../../scripts/harvest_fixtures.py")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, script, "--check-vectors").CombinedOutput()
	if err != nil {
		t.Fatalf("python sanitizer diverges from the committed vectors "+
			"(rule-spec lockstep broken):\n%s", out)
	}
	t.Logf("%s", strings.TrimSpace(string(out)))
}
