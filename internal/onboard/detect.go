package onboard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
)

// ProviderDetection is what onboarding could learn about one provider from
// data already on disk — never a guess. DetectedTier is "" when the tier is
// not derivable (always so for Claude). SubscriptionSignal is a weak
// sub-vs-metered hint used only to pre-bias an interactive default; it never
// sets the tier.
type ProviderDetection struct {
	Provider           string `json:"provider"`
	DetectedTier       string `json:"detected_tier"`
	Source             string `json:"source"`
	SubscriptionSignal string `json:"subscription_signal"`
}

// Detection is the per-provider report.
type Detection struct {
	Codex  ProviderDetection `json:"codex"`  // OpenAI / ChatGPT (Codex CLI)
	Claude ProviderDetection `json:"claude"` // Anthropic (Claude Code)
	Google ProviderDetection `json:"google"` // Google AI (agy / Antigravity CLI)
}

// For returns the detection for a provider arg (Templates key).
func (d Detection) For(arg string) ProviderDetection {
	switch arg {
	case "codex":
		return d.Codex
	case "google":
		return d.Google
	default:
		return d.Claude
	}
}

// Detect runs all read-only detection against the probe's environment.
func Detect(probe adapters.Probe) Detection {
	var d Detection

	// Codex / OpenAI — the tier IS detectable from the Codex CLI log.
	tier, source, err := DetectCodexTier(probe)
	d.Codex = ProviderDetection{Provider: "openai", DetectedTier: tier, Source: source}
	switch {
	case err != nil:
		d.Codex.Source = "codex detection error: " + err.Error()
		d.Codex.SubscriptionSignal = "unknown (detection error)"
	case tier != "":
		d.Codex.SubscriptionSignal = fmt.Sprintf("codex plan_type=%q present (a paid ChatGPT subscription)", tier)
	default:
		d.Codex.SubscriptionSignal = "no codex plan_type found"
	}

	// Claude / Anthropic — the tier is NOT derivable. Report only a weak
	// subscription signal (log presence); never infer the tier.
	d.Claude = ProviderDetection{
		Provider:     "anthropic",
		DetectedTier: "",
		Source:       "not derivable — Claude logs carry only usage.service_tier (the API serving class), not the subscription tier",
	}
	if claudeLogsPresent(probe) {
		d.Claude.SubscriptionSignal = "claude-code logs present (active Claude user; tier still unknown — choose one)"
	} else {
		d.Claude.SubscriptionSignal = "no claude-code logs found"
	}

	// Google / agy — the tier is always the user's declaration. The agy
	// adapter ingests usage from the statusLine hook's log; agy's status
	// object also names a plan_tier (a display label), but detection does
	// not read it, so nothing here is inferred.
	d.Google = ProviderDetection{
		Provider:           "google",
		DetectedTier:       "",
		Source:             "not auto-detected — the agy adapter reads usage, not your plan; declare your Google AI tier",
		SubscriptionSignal: "unknown (nothing read)",
	}
	return d
}

// DetectCodexTier locates the Codex logs via the codex adapter's own
// discovery and reads the most recent session's
// payload.rate_limits.plan_type. It is read-only and changes no adapter:
// the codex adapter intentionally ignores rate_limits, so onboarding parses
// the field directly off the rollout line. Returns ("", reason, nil) when no
// plan_type is present (e.g. logged out, or a Codex build that omits it).
func DetectCodexTier(probe adapters.Probe) (tier, source string, err error) {
	srcs, derr := codex.Adapter{}.Detect(probe)
	if derr != nil {
		return "", "", derr
	}
	if len(srcs) == 0 {
		return "", "no codex sessions directory ($CODEX_HOME/sessions or ~/.codex/sessions)", nil
	}
	root := srcs[0].Root
	files, ferr := recentRolloutFiles(root, 50)
	if ferr != nil {
		return "", "", ferr
	}
	for _, f := range files {
		if pt, ok := planTypeInFile(f); ok {
			return pt, fmt.Sprintf("codex rollout rate_limits.plan_type (%s)", filepath.Base(f)), nil
		}
	}
	return "", fmt.Sprintf("no rate_limits.plan_type in %d recent rollout file(s) under %s", len(files), root), nil
}

// recentRolloutFiles returns up to limit *.jsonl rollout files under root,
// NEWEST FIRST: the sessions/YYYY/MM/DD date tree makes lexical path order
// chronological, so reverse-sorting puts the freshest session first.
func recentRolloutFiles(root string, limit int) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			return nil // best-effort: skip an unreadable subtree
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

// planTypeInFile returns the first non-empty rate_limits.plan_type in a
// rollout file (rate_limits rides token_count lines, which appear early and
// often, so the first match is cheap). An unreadable file yields no match.
func planTypeInFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	// Rollout lines reach many MB — read whole lines, never a bounded scanner.
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 {
			if pt, ok := planTypeFromLine(line); ok {
				return pt, true
			}
		}
		if rerr != nil {
			return "", false
		}
	}
}

// planTypeFromLine extracts payload.rate_limits.plan_type from one rollout
// line. Pure and total: a parse failure, a missing rate_limits, or a null /
// empty plan_type all yield ("", false).
func planTypeFromLine(line []byte) (string, bool) {
	var rec struct {
		Payload struct {
			RateLimits *struct {
				PlanType string `json:"plan_type"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return "", false
	}
	if rec.Payload.RateLimits == nil {
		return "", false
	}
	pt := strings.TrimSpace(rec.Payload.RateLimits.PlanType)
	if pt == "" {
		return "", false
	}
	return pt, true
}

// claudeLogsPresent reports whether the claude-code adapter finds any log
// root — a weak "this user runs Claude Code" signal. It does NOT distinguish
// subscription from metered API use, and never implies a tier.
func claudeLogsPresent(probe adapters.Probe) bool {
	srcs, err := claudecode.Adapter{}.Detect(probe)
	return err == nil && len(srcs) > 0
}
