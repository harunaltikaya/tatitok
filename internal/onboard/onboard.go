package onboard

import (
	"fmt"
	"strings"

	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// MeteredTier is the pseudo-tier meaning "I pay per token": no plan entry is
// written, so that harness keeps basis api_price (billed at list rates).
const MeteredTier = "metered"

// ProviderTemplate is the FIXED per-provider plan shape onboarding reuses
// verbatim — the live owner-declared matchers, window and plan name.
// Only the TIER (hence the price and the display label) varies per user;
// the plan Name stays fixed whatever the tier.
type ProviderTemplate struct {
	// Arg is the CLI selector ("claude" / "codex").
	Arg string
	// SnapshotKey indexes the tier-price snapshot ("anthropic" / "openai").
	SnapshotKey string
	// PlanName is the plan id, fixed regardless of tier (the card shows the
	// tier's label when one was written).
	PlanName string
	Matcher  PlanMatcherOut
	Window   string
	// WindowStart anchors the rolling window (Anthropic floors to the UTC
	// hour, OpenAI anchors at the exact first request).
	WindowStart string
	// Detectable reports whether the tier can be auto-detected (codex yes,
	// claude no).
	Detectable bool
}

// Templates are the supported subscription cards, keyed by CLI arg.
var Templates = map[string]ProviderTemplate{
	"claude": {
		Arg: "claude", SnapshotKey: "anthropic", PlanName: "claude-max",
		Matcher: PlanMatcherOut{Harness: "claude-code"},
		Window:  "5h", WindowStart: "floored", Detectable: false,
	},
	"codex": {
		Arg: "codex", SnapshotKey: "openai", PlanName: "chatgpt-plus",
		Matcher: PlanMatcherOut{Harness: "codex"},
		Window:  "5h", WindowStart: "exact", Detectable: true,
	},
	// Google AI Pro (agy / Antigravity CLI): 5h + weekly windows like
	// claude-max; the tier is user-declared (agy's status line names a
	// plan_tier, but onboarding detection does not read it).
	// WindowStart "floored" is the default, unverified for Google.
	"google": {
		Arg: "google", SnapshotKey: "google", PlanName: "google-ai-pro",
		Matcher: PlanMatcherOut{Harness: "agy"},
		Window:  "5h", WindowStart: "floored", Detectable: false,
	},
}

// ProviderArgs returns the CLI provider selectors in a stable order.
func ProviderArgs() []string { return []string{"claude", "codex", "google"} }

// TierChoices lists the selectable tiers for a provider arg (snapshot tiers
// plus "metered"), for help text and validation.
func TierChoices(arg string, snap *TierPrices) []string {
	tpl, ok := Templates[arg]
	if !ok {
		return []string{MeteredTier}
	}
	return append(snap.Tiers(tpl.SnapshotKey), MeteredTier)
}

// PlanChoice is one provider's resolved decision: the tier, an optional
// price override, and provenance about where the tier came from.
type PlanChoice struct {
	ProviderArg string
	Tier        string
	PriceUSD    string // "" → use the snapshot default
	Detected    bool   // tier confirms an unambiguous detection (codex)
	// TierNote, when set, is the exact tier-provenance phrase recorded in
	// the entry's _doc — used for the ambiguous Codex-Pro sub-tier choice
	// ("$100 vs $200 not in logs, user-selected"). Empty → derived from
	// Detected/Detectable (Stage 1 behavior).
	TierNote string
}

// ResolveEntry turns a choice into the plan entry to write, or (nil, nil) for
// a metered choice (no entry). It resolves the price (override or snapshot
// default), validates it as USD, and records an honest provenance _doc.
func ResolveEntry(c PlanChoice, snap *TierPrices, now string) (*PlanEntryOut, error) {
	tpl, ok := Templates[c.ProviderArg]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (want one of: %s)", c.ProviderArg, strings.Join(ProviderArgs(), ", "))
	}
	tier := strings.TrimSpace(c.Tier)
	if tier == "" {
		return nil, fmt.Errorf("%s: no tier chosen", c.ProviderArg)
	}
	if tier == MeteredTier {
		return nil, nil // metered → no plan entry; stays api_price
	}

	// Price: explicit override, else the published list default.
	price := strings.TrimSpace(c.PriceUSD)
	fromSnapshot := false
	if price == "" {
		p, ok := snap.Price(tpl.SnapshotKey, tier)
		if !ok {
			return nil, fmt.Errorf("%s: unknown tier %q (choose one of: %s)",
				c.ProviderArg, tier, strings.Join(TierChoices(c.ProviderArg, snap), ", "))
		}
		price = p.String()
		fromSnapshot = true
	}
	// Validate with the SAME exact parser the loader uses for plan prices.
	micro, err := pricing.USDToMicro(price)
	if err != nil {
		return nil, fmt.Errorf("%s: price %q is not a valid USD amount: %w", c.ProviderArg, price, err)
	}

	e := &PlanEntryOut{
		Name:        tpl.PlanName,
		Matchers:    []PlanMatcherOut{tpl.Matcher},
		Window:      tpl.Window,
		WindowStart: tpl.WindowStart,
	}
	// The loader requires monthly_price_usd > 0, so a $0 tier (free) writes a
	// plan with NO price field — usage is still plan_included ($0, with the
	// API-equivalent carried), which is exactly right for a free plan.
	if micro > 0 {
		e.MonthlyPriceUSD = price
	}
	// Display label for the card (the plan Name stays fixed per provider); a
	// tier the snapshot does not label writes none.
	if l, ok := snap.Label(tpl.SnapshotKey, tier); ok {
		e.Label = l
	}

	// Tier provenance: an explicit TierNote wins (the ambiguous-Pro case);
	// otherwise derive it from detection (Stage 1 behavior).
	tierNote := strings.TrimSpace(c.TierNote)
	if tierNote == "" {
		switch {
		case c.Detected:
			tierNote = "detected from codex rate_limits.plan_type, user-confirmed"
		case tpl.Detectable:
			tierNote = "user-declared (overriding codex detection)"
		case tpl.Arg == "google":
			tierNote = "user-declared (Google AI tier is not auto-detectable)"
		default:
			tierNote = "user-declared (Claude tier is not auto-detectable)"
		}
	}
	e.Doc = provenance(tpl, tier, price, micro, tierNote, fromSnapshot, snap.Version, now)
	return e, nil
}

// provenance builds the per-entry _doc audit trail: where the tier came from
// (tierNote, pre-resolved by the caller) and where the price came from
// (published default vs user-edited) — the honesty trail the spec requires.
func provenance(tpl ProviderTemplate, tier, price string, micro int64, tierNote string, fromSnapshot bool, snapVer, now string) string {
	var priceNote string
	switch {
	case micro == 0:
		priceNote = "$0/mo — included, no monthly_price_usd written"
	case fromSnapshot:
		priceNote = fmt.Sprintf("$%s/mo (published list default, tatitok tier-prices snapshot %s)", price, snapVer)
	default:
		priceNote = fmt.Sprintf("$%s/mo (user-edited)", price)
	}

	return fmt.Sprintf(
		"authored by `tatitok onboard` %s — tier=%s (%s); price=%s; matcher+window reuse the live %s card. Defaults are overridable: edit this entry and run `tatitok recompute --pricing`.",
		now, tier, tierNote, priceNote, tpl.PlanName)
}

// CodexResolution interprets a detected Codex plan_type against the snapshot.
// Some plan_types map 1:1 to a priced tier (plus, go); "pro" is two price
// points ($100/$200) reported identically in the log (verified 2026-06-17:
// no rate_limits field distinguishes them) — so it is Ambiguous and must be a
// user choice, the Codex analog of Claude Max 5×/20×. tatitok never guesses it.
type CodexResolution struct {
	Raw       string   `json:"raw"`       // plan_type as logged ("" = none detected)
	Tier      string   `json:"tier"`      // snapshot tier to pre-fill; "" when ambiguous/unknown/none
	Ambiguous bool     `json:"ambiguous"` // raw maps to >1 priced tier (pro → pro_100/pro_200)
	Options   []string `json:"options"`   // candidate tiers when ambiguous
	Note      string   `json:"note"`
}

// codexPlanTypeAliases maps a logged plan_type that is not a snapshot key to
// the one tier it names. "prolite" is the $100 Pro (first seen in Codex logs
// 2026-09-22); "pro" stays ambiguous (see below).
var codexPlanTypeAliases = map[string]string{"prolite": "pro_100"}

// ResolveCodexPlanType maps a raw plan_type to onboarding guidance.
func ResolveCodexPlanType(raw string, snap *TierPrices) CodexResolution {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CodexResolution{Note: "no plan_type detected in the Codex logs"}
	}
	// Known, irreducible ambiguity: ChatGPT Pro is $100 and $200, both logged
	// as plan_type="pro" with no distinguishing field — a user choice.
	if raw == "pro" {
		return CodexResolution{
			Raw:       raw,
			Ambiguous: true,
			Options:   []string{"pro_100", "pro_200"},
			Note:      "ChatGPT Pro is two price points ($100 and $200), indistinguishable in the Codex log — choose yours",
		}
	}
	// A logged alias that names one priced tier pre-fills that tier.
	if tier, ok := codexPlanTypeAliases[raw]; ok {
		if _, ok := snap.Price("openai", tier); ok {
			return CodexResolution{Raw: raw, Tier: tier, Note: fmt.Sprintf("detected from codex rate_limits.plan_type (%q = %s)", raw, tier)}
		}
	}
	// Exact snapshot key (plus, go, free, or any future addition) pre-fills.
	if _, ok := snap.Price("openai", raw); ok {
		return CodexResolution{Raw: raw, Tier: raw, Note: "detected from codex rate_limits.plan_type"}
	}
	// Unknown plan_type — never guess; let the user pick.
	return CodexResolution{Raw: raw, Note: fmt.Sprintf("plan_type %q has no known price tier — choose one", raw)}
}

// CodexChoice is the resolved Codex tier decision for CLI/API: an explicit
// flag wins; an unambiguous detection pre-fills; an ambiguous/absent/unknown
// detection needs an explicit choice (NeedChoice). TierNote carries the
// provenance to record. flag/detectedRaw are trimmed by the callee.
type CodexChoice struct {
	Tier       string
	TierNote   string
	NeedChoice bool
	Resolution CodexResolution
}

// ResolveCodexChoice combines an explicit flag with the detected plan_type.
func ResolveCodexChoice(flag, detectedRaw string, snap *TierPrices) CodexChoice {
	res := ResolveCodexPlanType(detectedRaw, snap)
	flag = strings.TrimSpace(flag)
	if flag != "" {
		// Explicit choice — record HOW it relates to detection (honesty trail).
		var note string
		switch {
		case flag == MeteredTier:
			note = "" // metered → no entry; note unused
		case res.Tier != "" && flag == res.Tier:
			note = "detected from codex rate_limits.plan_type, user-confirmed"
		case res.Ambiguous && contains(res.Options, flag):
			note = fmt.Sprintf("sub-tier chosen for ambiguous codex Pro (plan_type=%q; $100 vs $200 not distinguishable in logs), user-selected", res.Raw)
		case res.Raw != "":
			note = fmt.Sprintf("user-declared (codex detected plan_type=%q)", res.Raw)
		default:
			note = "user-declared (overriding codex detection)"
		}
		return CodexChoice{Tier: flag, TierNote: note, Resolution: res}
	}
	// No flag: an unambiguous detection pre-fills; otherwise a choice is owed.
	if res.Tier != "" {
		return CodexChoice{Tier: res.Tier, TierNote: "detected from codex rate_limits.plan_type", Resolution: res}
	}
	return CodexChoice{NeedChoice: true, Resolution: res}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
