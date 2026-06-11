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

// vectorCeremony is the mandatory procedure for changing the frozen
// sanitizer vectors; it is printed wherever someone could be tempted to
// skip it (mirrors the adapters' golden ceremony).
const vectorCeremony = `regenerating sanitizer vector expectations requires the full ceremony:
  1. a vector regen means sanitizer output changed by definition — identify the
     rule-spec / implementation change that caused it; if the shared semantics
     changed, bump spec_version in sanitize_rules.json (every consumer pins it)
  2. TATITOK_UPDATE_VECTORS=1 TATITOK_CONFIRM_VECTORS=1 go test ./internal/core -run TestSanitizerContractVectors
     (the Python side regenerates its vectors with
      harvest_fixtures.py --update-vectors --confirm-vector-ceremony)
  3. python3 scripts/harvest_fixtures.py --check-vectors must pass afterwards —
     rule-spec lockstep: both implementations byte-identical on the new bytes
  4. make test must stay green: sanitized raw feeds the frozen adapter goldens,
     so a vector change can cascade into the golden ceremony
commit the vector diff together with the causing change and note the ceremony in the message`

// TestSanitizerContractVectors runs every contract vector through the Go
// sanitizer and demands byte-equality with the committed expected output.
// Regeneration demands the explicit two-flag confirmation of the ceremony
// above — never regenerate to make a red test green.
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
				if os.Getenv("TATITOK_CONFIRM_VECTORS") != "1" {
					t.Fatalf("TATITOK_UPDATE_VECTORS=1 refused without TATITOK_CONFIRM_VECTORS=1\n%s",
						vectorCeremony)
				}
				if err := os.WriteFile(expPath, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s\n%s", expPath, vectorCeremony)
				return
			}
			want, err := os.ReadFile(expPath)
			if err != nil {
				// A missing expected file FAILS — skipping would let the
				// cross-implementation contract go unchecked while green.
				t.Fatalf("frozen vector expectation unreadable: %v\n%s", err, vectorCeremony)
			}
			if string(got)+"\n" != string(want) {
				t.Fatalf("sanitizer output diverges from frozen vector %s:\ngot:  %s\nwant: %s\n%s",
					filepath.Base(expPath), got, strings.TrimSuffix(string(want), "\n"),
					vectorCeremony)
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
