package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/harunaltikaya/tatitok/internal/onboard"
	"github.com/harunaltikaya/tatitok/internal/pricing"
)

const onboardHelp = `tatitok onboard — declare your subscription plan(s) so plan-covered usage
bills $0 and shows its API-equivalent value. It writes ONLY the "plans"
section of prices.json (merging, never clobbering); repricing stored events
is the explicit follow-up step it prints.

Usage:
  tatitok onboard [--claude TIER] [--codex TIER]
                  [--claude-price USD] [--codex-price USD] [--dry-run]

Tiers:
  Claude (anthropic, card "claude-max"):   free | pro | max_5x | max_20x | metered
  Codex  (openai,    card "chatgpt-plus"): free | plus | pro | metered
  metered → no plan written; that harness stays api_price (per-token billing).

Detection (read-only):
  Codex/ChatGPT tier is AUTO-DETECTED from the Codex CLI log
  (payload.rate_limits.plan_type) and pre-fills --codex.
  Claude tier is NOT detectable (service_tier is the API serving class, not
  your subscription) — you must choose it; tatitok never guesses it.

Prices default to the published consumer list price for the tier (tatitok's
embedded tier-prices snapshot) and are overridable with --claude-price /
--codex-price, or editable in prices.json afterward.

With both tier flags set, onboard is non-interactive; otherwise it prompts.
After writing, run:
  tatitok recompute --pricing && tatitok doctor --pricing`

func cmdOnboard(args []string) error {
	fs := flag.NewFlagSet("onboard", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, onboardHelp) }
	claudeTier := fs.String("claude", "", "Claude tier: free|pro|max_5x|max_20x|metered")
	codexTier := fs.String("codex", "", "Codex/ChatGPT tier: free|plus|pro|metered")
	claudePrice := fs.String("claude-price", "", "override Claude monthly price (USD)")
	codexPrice := fs.String("codex-price", "", "override Codex monthly price (USD)")
	dryRun := fs.Bool("dry-run", false, "print the plan entries and target path; write nothing")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil // fs.Usage already printed onboardHelp
		}
		return err
	}

	snap, err := onboard.LoadTierPrices()
	if err != nil {
		return err
	}
	probe := realProbe()
	path := pricing.OverridesPath(probe.Getenv, probe.HomeDir)

	// 1) Detection — print findings.
	det := onboard.Detect(probe)
	fmt.Println("Detection (read-only):")
	fmt.Printf("  Codex/ChatGPT: %s\n", detLine(det.Codex))
	fmt.Printf("  Claude:        %s\n", detLine(det.Claude))
	fmt.Println()

	// 2) Resolve tiers: flag > detection (codex only) > interactive prompt.
	in := bufio.NewReader(os.Stdin)
	codexChoices := strings.Join(onboard.TierChoices("codex", snap), ", ")
	claudeChoices := strings.Join(onboard.TierChoices("claude", snap), ", ")

	// Codex is detection-aware AND Pro-split-aware ($100 vs $200 → a choice).
	ct, ctNote, err := resolveCodexTier(*codexTier, det.Codex.DetectedTier, snap, codexChoices, in)
	if err != nil {
		return err
	}
	// Claude is never auto-detected — flag or prompt.
	clt, err := resolveTier("claude", *claudeTier, "", claudeChoices, in)
	if err != nil {
		return err
	}

	// 3) Build plan entries.
	now := time.Now().UTC().Format("2006-01-02")
	choices := []onboard.PlanChoice{
		{ProviderArg: "claude", Tier: clt, PriceUSD: *claudePrice},
		{ProviderArg: "codex", Tier: ct, PriceUSD: *codexPrice, TierNote: ctNote},
	}
	var entries []onboard.PlanEntryOut
	fmt.Println("Plan resolution:")
	for _, c := range choices {
		e, err := onboard.ResolveEntry(c, snap, now)
		if err != nil {
			return err
		}
		if e == nil {
			fmt.Printf("  %-6s tier=%-8s → metered: no plan entry (stays api_price, billed at list rates)\n", c.ProviderArg, c.Tier)
			continue
		}
		entries = append(entries, *e)
		fmt.Printf("  %-6s tier=%-8s → plan %q  %s\n", c.ProviderArg, c.Tier, e.Name, priceLabel(*e))
	}
	fmt.Println()

	if len(entries) == 0 {
		fmt.Println("Both providers metered — no plan entries to write; prices.json left unchanged.")
		return nil
	}

	// Show exactly what will be written (provenance included).
	pretty, _ := json.MarshalIndent(entries, "  ", "  ")
	fmt.Printf("Plan entries for %s:\n  %s\n\n", path, pretty)

	if *dryRun {
		fmt.Println("--dry-run: nothing written.")
		return nil
	}

	// 4) Merge into prices.json (non-destructive, backed up, atomic).
	res, err := onboard.MergePlans(path, entries)
	if err != nil {
		return err
	}
	if res.Created {
		fmt.Printf("Created %s\n", res.Path)
	} else {
		fmt.Printf("Updated %s (backup: %s)\n", res.Path, res.BackupPath)
	}
	if len(res.Added) > 0 {
		fmt.Printf("  added:    %s\n", strings.Join(res.Added, ", "))
	}
	if len(res.Replaced) > 0 {
		fmt.Printf("  replaced: %s\n", strings.Join(res.Replaced, ", "))
	}

	// 5) Next steps (the explicit reprice + reconcile; never a side effect).
	fmt.Println()
	fmt.Println("Next steps (reprice stored events under the new plans):")
	fmt.Println("  tatitok recompute --pricing")
	fmt.Println("  tatitok doctor --pricing")
	return nil
}

func detLine(p onboard.ProviderDetection) string {
	t := p.DetectedTier
	if t == "" {
		t = "(none)"
	}
	return fmt.Sprintf("tier=%s · %s · signal: %s", t, p.Source, p.SubscriptionSignal)
}

func priceLabel(e onboard.PlanEntryOut) string {
	if e.MonthlyPriceUSD == "" {
		return "$0/mo (included)"
	}
	return "$" + e.MonthlyPriceUSD + "/mo"
}

// resolveTier picks a tier: an explicit flag wins; otherwise a detected tier
// pre-fills (codex), and a TTY is prompted. With no flag, no detection and no
// TTY, it errors rather than hang — the friend gets a clear "pass --<arg>".
func resolveTier(arg, flagVal, detected, choices string, in *bufio.Reader) (string, error) {
	if v := strings.TrimSpace(flagVal); v != "" {
		return v, nil
	}
	if !stdinIsTTY() {
		if detected != "" {
			return detected, nil // non-interactive: accept the detected tier
		}
		return "", fmt.Errorf("%s tier not provided and not auto-detected — pass --%s (one of: %s)", arg, arg, choices)
	}
	def := detected
	suffix := ""
	if def != "" {
		suffix = fmt.Sprintf(" [%s detected, Enter to accept]", def)
	}
	fmt.Printf("  %s tier (%s)%s: ", arg, choices, suffix)
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	if line == "" {
		return "", fmt.Errorf("%s tier is required", arg)
	}
	return line, nil
}

// resolveCodexTier resolves the Codex tier with detection + Pro-split
// awareness. A flag wins; an unambiguous detection (plus/go) pre-fills; an
// ambiguous Pro ($100/$200) or unknown plan_type needs an explicit choice
// (prompted on a TTY, errored otherwise). Returns the tier and its
// provenance note for the entry's _doc.
func resolveCodexTier(flag, detectedRaw string, snap *onboard.TierPrices, choices string, in *bufio.Reader) (tier, note string, err error) {
	cc := onboard.ResolveCodexChoice(flag, detectedRaw, snap)
	if !cc.NeedChoice {
		return cc.Tier, cc.TierNote, nil
	}
	// A choice is owed (ambiguous/unknown/none and no flag).
	hint := ""
	if cc.Resolution.Note != "" {
		hint = " — " + cc.Resolution.Note
	}
	if !stdinIsTTY() {
		return "", "", fmt.Errorf("codex tier needs an explicit choice%s — pass --codex (one of: %s)", hint, choices)
	}
	fmt.Printf("  codex tier (%s)%s: ", choices, hint)
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", fmt.Errorf("codex tier is required")
	}
	cc2 := onboard.ResolveCodexChoice(line, detectedRaw, snap)
	return cc2.Tier, cc2.TierNote, nil
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}
