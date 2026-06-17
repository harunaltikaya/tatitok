package hub

// Onboarding endpoints (Stage 2): thin HTTP wrappers over internal/onboard's
// Stage 1 logic — detection, ResolveEntry, the prices.json merge-writer — plus
// the explicit recompute. No pricing logic lives here. Loopback-only like the
// rest of the hub (loopbackOnly gates the whole tree); /apply is the explicit
// user action that lets the hub rewrite history (otherwise CLI-only).

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/onboard"
	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// hubProbe mirrors cmd/tatitok's realProbe so detection + path resolution
// match the CLI exactly (CLAUDE_CONFIG_DIR, CODEX_HOME, XDG_CONFIG_HOME).
func (h *Hub) hubProbe() adapters.Probe {
	home, _ := os.UserHomeDir()
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return adapters.Probe{Getenv: os.Getenv, HomeDir: home, Machine: host}
}

type onbTier struct {
	Tier     string `json:"tier"`
	PriceUSD string `json:"price_usd"` // snapshot list default
}

type onbCurrent struct {
	Declared          bool   `json:"declared"`
	MonthlyPriceMicro *int64 `json:"monthly_price_micro"`
	WindowSeconds     int64  `json:"window_seconds"`
	WindowStart       string `json:"window_start"`
}

type onbCard struct {
	ProviderArg        string     `json:"provider_arg"` // "claude" / "codex"
	PlanName           string     `json:"plan_name"`    // card label
	Provider           string     `json:"provider"`     // "anthropic" / "openai"
	DetectedTier       string     `json:"detected_tier"`
	DetectedRaw        string     `json:"detected_raw"`
	Ambiguous          bool       `json:"ambiguous"`
	AmbiguousOptions   []string   `json:"ambiguous_options"`
	DetectionSource    string     `json:"detection_source"`
	SubscriptionSignal string     `json:"subscription_signal"`
	Note               string     `json:"note"`            // ambiguity/explanation
	WindowsPresent     bool       `json:"windows_present"` // live limits signal
	MeteredAvailable   bool       `json:"metered_available"`
	Tiers              []onbTier  `json:"tiers"`
	Current            onbCurrent `json:"current"`
}

// apiOnboardDetect reports, per card, what is detected, what is selectable
// (tiers + list prices), the live subscription signal, and the currently
// declared plan — everything the confirm panel needs to render and pre-fill.
func (h *Hub) apiOnboardDetect(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	snap, err := onboard.LoadTierPrices()
	if err != nil {
		slog.Error("onboard: tier snapshot", "err", err)
		writeErr(w, http.StatusInternalServerError, "config_error", "tier-price snapshot unavailable")
		return
	}
	probe := h.hubProbe()
	det := onboard.Detect(probe)
	ov := h.overrides()
	declared := map[string]pricing.Plan{}
	for _, p := range ov.Plans() {
		declared[p.Name] = p
	}
	lim := h.lim.Get()

	cards := make([]onbCard, 0, 2)
	for _, arg := range onboard.ProviderArgs() {
		tpl := onboard.Templates[arg]
		pd := det.Claude
		if arg == "codex" {
			pd = det.Codex
		}
		card := onbCard{
			ProviderArg:        arg,
			PlanName:           tpl.PlanName,
			Provider:           tpl.SnapshotKey,
			DetectionSource:    pd.Source,
			SubscriptionSignal: pd.SubscriptionSignal,
			MeteredAvailable:   true,
			AmbiguousOptions:   []string{},
			Tiers:              []onbTier{},
		}
		for _, tier := range snap.Tiers(tpl.SnapshotKey) {
			price, _ := snap.Price(tpl.SnapshotKey, tier)
			card.Tiers = append(card.Tiers, onbTier{Tier: tier, PriceUSD: price.String()})
		}
		// Codex tier is detectable (with the Pro-split ambiguity); Claude is not.
		if arg == "codex" {
			res := onboard.ResolveCodexPlanType(pd.DetectedTier, snap)
			card.DetectedTier = res.Tier
			card.DetectedRaw = res.Raw
			card.Ambiguous = res.Ambiguous
			if len(res.Options) > 0 {
				card.AmbiguousOptions = res.Options
			}
			card.Note = res.Note
		} else {
			card.Note = pd.Source // Claude: the "not derivable" explanation
		}
		// Live limits/windows presence (internal/limits) — a sub-vs-metered
		// hint only; never sets the tier. Keys match the extension/providerForPlan.
		if p, ok := lim[arg]; ok && len(p.Windows) > 0 {
			card.WindowsPresent = true
		}
		if p, ok := declared[tpl.PlanName]; ok {
			card.Current = onbCurrent{
				Declared:          true,
				MonthlyPriceMicro: p.MonthlyPriceMicro,
				WindowSeconds:     int64(p.Window.Seconds()),
				WindowStart:       string(p.WindowStart),
			}
		}
		cards = append(cards, card)
	}

	count, err := h.st.CountEvents(r.Context())
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot_version": snap.Version,
		"has_usage":        count > 0,
		"has_plans":        len(ov.Plans()) > 0,
		"cards":            cards,
	})
}

type applyCard struct {
	ProviderArg string `json:"provider_arg"`
	Tier        string `json:"tier"`      // tier name, or "metered"
	PriceUSD    string `json:"price_usd"` // optional override; "" → snapshot default
}

type applyBody struct {
	Cards []applyCard `json:"cards"`
}

// rejectCSRF guards a state-changing endpoint against being driven cross-site
// by a browser. The loopback bind and loopbackOnly gate do NOT stop CSRF: a
// malicious page can make the browser POST to 127.0.0.1 and the TCP peer is
// still loopback. Two checks (the extension ingest endpoint, internal/limits,
// has the equivalent content-type gate but not the Sec-Fetch-Site one):
//
//   - Content-Type must be application/json. A cross-origin page can only send
//     a CORS "simple" content-type (text/plain or a form type) WITHOUT a
//     preflight; requiring application/json forces a preflight, and the
//     method-less /apply route answers OPTIONS with a 405 that carries NO
//     Access-Control-Allow-* headers (the hub sets none anywhere — writeJSON
//     emits only Content-Type + nosniff), so the browser never sends the real
//     POST.
//   - Sec-Fetch-Site, when the browser sends it, must be same-origin or none
//     (a direct navigation). cross-site/same-site is refused outright — defense
//     in depth above the content-type gate. Non-browser callers (curl, the Go
//     test client) omit the header and are unaffected.
//
// It returns true when the request was rejected (a response is already written).
func rejectCSRF(w http.ResponseWriter, r *http.Request) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"this endpoint requires Content-Type: application/json")
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeErr(w, http.StatusForbidden, "forbidden",
			"cross-site request rejected")
		return true
	}
	return false
}

// apiOnboardApply builds plan entries via Stage 1's ResolveEntry, merges them
// into prices.json via write.go (backup + atomic + validate + provenance),
// removes entries for metered cards, reloads the overrides, and reprices the
// whole history (the explicit, user-triggered recompute). It returns the merge
// result, the reprice counts, and the resulting full-DB basis summary.
func (h *Hub) apiOnboardApply(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	// CSRF defense: /apply writes prices.json and reprices the whole history, so
	// a browser must not be drivable cross-site into it (see rejectCSRF).
	if rejectCSRF(w, r) {
		return
	}
	var body applyBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_body", "request body is not a valid apply payload: "+err.Error())
		return
	}
	// Exactly one JSON value: a second decode must hit EOF, rejecting trailing
	// data (matches pricing.LoadOverrides and the limits ingest endpoint).
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad_body", "unexpected trailing data after the JSON body")
		return
	}
	if len(body.Cards) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_body", "no cards in apply payload")
		return
	}

	// Serialize applies: each is a write + full reprice.
	h.applyMu.Lock()
	defer h.applyMu.Unlock()

	snap, err := onboard.LoadTierPrices()
	if err != nil {
		slog.Error("onboard: tier snapshot", "err", err)
		writeErr(w, http.StatusInternalServerError, "config_error", "tier-price snapshot unavailable")
		return
	}
	probe := h.hubProbe()
	det := onboard.Detect(probe)
	path := pricing.OverridesPath(probe.Getenv, probe.HomeDir)
	now := time.Now().UTC().Format("2006-01-02")

	var entries []onboard.PlanEntryOut
	var remove []string
	for _, c := range body.Cards {
		tpl, ok := onboard.Templates[c.ProviderArg]
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_body", "unknown provider_arg "+c.ProviderArg)
			return
		}
		// Only the literal "metered" removes a plan. An empty/missing tier is a
		// malformed card — reject it rather than silently deleting the plan.
		if strings.TrimSpace(c.Tier) == "" {
			writeErr(w, http.StatusBadRequest, "bad_body",
				"card for "+c.ProviderArg+` has an empty tier (choose a tier, or "metered" to keep per-token billing)`)
			return
		}
		if strings.TrimSpace(c.Tier) == onboard.MeteredTier {
			remove = append(remove, tpl.PlanName) // metered → ensure no entry
			continue
		}
		choice := onboard.PlanChoice{ProviderArg: c.ProviderArg, Tier: c.Tier, PriceUSD: c.PriceUSD}
		if c.ProviderArg == "codex" {
			// Compute provenance server-side from detection (never client-trusted).
			cc := onboard.ResolveCodexChoice(c.Tier, det.Codex.DetectedTier, snap)
			choice.TierNote = cc.TierNote
		}
		e, err := onboard.ResolveEntry(choice, snap, now)
		if err != nil {
			// Safe: ResolveEntry errors carry no filesystem path.
			writeErr(w, http.StatusBadRequest, "bad_tier", err.Error())
			return
		}
		entries = append(entries, *e)
	}

	var mres onboard.MergeResult
	if len(entries) > 0 || len(remove) > 0 {
		mres, err = onboard.MergePlans(path, entries, remove...)
		if err != nil {
			// The error may embed the config path — log it, return generic.
			slog.Error("onboard: merge prices.json", "err", err)
			writeErr(w, http.StatusInternalServerError, "write_failed", "could not write prices.json (see server log)")
			return
		}
	}

	// Reload overrides from disk and swap them into the hub, then reprice the
	// whole history under them (the explicit user-triggered recompute).
	ov, err := pricing.LoadOverrides(path)
	if err != nil {
		slog.Error("onboard: reload overrides", "err", err)
		writeErr(w, http.StatusInternalServerError, "reload_failed", "wrote prices.json but could not reload it (see server log)")
		return
	}
	h.setOverrides(ov)

	pres, err := h.st.RecomputePricing(r.Context(), ov)
	if err != nil {
		storeError(w, r, err)
		return
	}
	facets, err := h.st.Facets(r.Context())
	if err != nil {
		storeError(w, r, err)
		return
	}

	slog.Info("onboard apply", "added", mres.Added, "replaced", mres.Replaced,
		"removed", mres.Removed, "repriced", pres.Repriced, "cost_changed", pres.CostChanged)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"added":        names(mres.Added),
		"replaced":     names(mres.Replaced),
		"removed":      names(mres.Removed),
		"created":      mres.Created,
		"repriced":     pres.Repriced,
		"cost_changed": pres.CostChanged,
		"by_basis":     facets["basis"],
	})
}

func names(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
