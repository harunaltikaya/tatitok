package onboard

import (
	"path/filepath"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/adapters"
)

// TestPlanTypeFromLine exercises the pure rollout-line parser with tiny
// synthetic JSON, which is fine for a pure-function unit test. The
// directory-discovery path is covered against the real fixture
// in TestDetectCodexTier below.
func TestPlanTypeFromLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{"plus", `{"payload":{"type":"token_count","rate_limits":{"plan_type":"plus"}}}`, "plus", true},
		{"pro", `{"payload":{"type":"token_count","rate_limits":{"plan_type":"pro"}}}`, "pro", true},
		{"null", `{"payload":{"rate_limits":{"plan_type":null}}}`, "", false},
		{"empty", `{"payload":{"rate_limits":{"plan_type":""}}}`, "", false},
		{"no_rate_limits", `{"payload":{"type":"token_count","info":null}}`, "", false},
		{"no_payload", `{"timestamp":"2026-01-01T00:00:00Z"}`, "", false},
		{"not_json", `not json at all`, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := planTypeFromLine([]byte(c.line))
			if got != c.want || ok != c.ok {
				t.Fatalf("planTypeFromLine(%s) = (%q,%v), want (%q,%v)", c.line, got, ok, c.want, c.ok)
			}
		})
	}
}

func probeWithCodexHome(t *testing.T, codexHome string) adapters.Probe {
	t.Helper()
	return adapters.Probe{
		HomeDir: t.TempDir(), // keep ~/.codex out of the picture
		Machine: "test",
		Getenv: func(k string) string {
			if k == "CODEX_HOME" {
				return codexHome
			}
			return ""
		},
	}
}

// TestDetectCodexTier proves the discovery path on the REAL committed gx10
// rollout fixtures (plan_type "plus"), and the absent path on an empty home.
func TestDetectCodexTier(t *testing.T) {
	// gx10 fixtures live two levels up; CODEX_HOME is the dir CONTAINING sessions/.
	gx10, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "codex", "gx10"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("plus_from_real_fixture", func(t *testing.T) {
		tier, source, err := DetectCodexTier(probeWithCodexHome(t, gx10))
		if err != nil {
			t.Fatal(err)
		}
		if tier != "plus" {
			t.Fatalf("tier = %q (source %q), want \"plus\"", tier, source)
		}
	})

	t.Run("absent_when_no_sessions", func(t *testing.T) {
		// CODEX_HOME points at a dir with no sessions/ subtree.
		tier, _, err := DetectCodexTier(probeWithCodexHome(t, t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if tier != "" {
			t.Fatalf("tier = %q, want \"\" (no logs → not detectable)", tier)
		}
	})
}

// TestDetectReportsClaudeUnknown: Claude tier is never inferred.
func TestDetectReportsClaudeUnknown(t *testing.T) {
	det := Detect(probeWithCodexHome(t, t.TempDir()))
	if det.Claude.DetectedTier != "" {
		t.Fatalf("Claude DetectedTier = %q, want \"\" (never derivable)", det.Claude.DetectedTier)
	}
	if det.Claude.Source == "" || det.Claude.SubscriptionSignal == "" {
		t.Fatalf("Claude detection missing source/signal: %+v", det.Claude)
	}
}
