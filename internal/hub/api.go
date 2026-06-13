package hub

// API v1 (M4 Task 2): the `stats --json` shapes promoted to versioned
// endpoints under /api/v1/. Rollup-backed endpoints serve UTC days and
// say so in the payload ("grain":"day","tz":"UTC") — exact non-UTC
// serving from events stays CLI-only this milestone. Errors use one
// JSON envelope; unknown query parameters are rejected, not ignored.
// These are ordinary tests' endpoints, not parity surfaces: responses
// are asserted equal to direct store queries.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const dayFormat = "2006-01-02"

// apiError is the one error envelope every /api/v1 failure uses.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var e apiError
	e.Error.Code, e.Error.Message = code, msg
	writeJSON(w, status, e)
}

// checkParams rejects any query parameter outside the allowlist —
// a typo like ?form= must fail loudly, not silently return everything.
func checkParams(r *http.Request, allowed ...string) error {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range r.URL.Query() {
		if !ok[k] {
			return fmt.Errorf("unknown query parameter %q (supported: %s)", k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// registerAPI mounts /api/v1 on the hub's mux. Every endpoint is
// GET-only — and that includes HEAD (owner ruling, M5 Codex round):
// per RFC 9110 §9.3.2 HEAD is GET without the response body, the
// mux's "GET " patterns match it deliberately, and net/http strips
// the body — so HEAD answers 200 with the GET's headers and an empty
// body. It was never in the "wrong methods" set. A known path with
// any OTHER method gets the JSON envelope with "Allow: GET, HEAD"
// (M4 Codex round, finding 5 — the mux's built-in 405 is text/plain,
// off-contract), and everything else under /api/ is a JSON 404
// (never the dashboard's HTML).
func (h *Hub) registerAPI(mux *http.ServeMux) {
	get := func(path string, fn http.HandlerFunc) {
		mux.HandleFunc("GET "+path, fn)
		// Method-less pattern: more specific than "/api/", less than
		// "GET path" (which also takes HEAD) — exactly the wrong-method
		// case.
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", "GET, HEAD")
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed",
				r.Method+" is not supported on "+path+" (only GET and HEAD)")
		})
	}
	get("/api/v1/health", h.apiHealth)
	get("/api/v1/stats/daily", h.apiStatsDaily)
	get("/api/v1/totals", h.apiTotals)
	get("/api/v1/meta/models", h.apiMetaModels)
	get("/api/v1/meta/facets", h.apiMetaFacets)
	get("/api/v1/plans", h.apiPlans)
	get("/api/v1/stream", h.apiStream)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not_found",
			"unknown API path "+r.URL.Path+" (this hub serves /api/v1)")
	})
}

// apiHealth: version, price snapshot id, override counts, DB path HASH
// (never the path itself — it may embed a private home directory),
// uptime.
func (h *Hub) apiHealth(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":          h.version,
		"price_snapshot":   h.snapshot,
		"overrides":        h.cfg.Overrides.Len(),
		"reference_models": h.cfg.Overrides.References(),
		"db_hash":          h.dbHash,
		"started_at":       h.started.UTC().Format(time.RFC3339),
		"uptime_seconds":   int64(time.Since(h.started).Seconds()),
	})
}

// dbPathHash: a stable identifier for "which database is this hub on"
// without disclosing the filesystem path.
func dbPathHash(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:8])
}

// parseDayRange validates optional from/to (YYYY-MM-DD, inclusive).
func parseDayRange(r *http.Request) (from, to string, err error) {
	for _, p := range []struct {
		name string
		dst  *string
	}{{"from", &from}, {"to", &to}} {
		v := r.URL.Query().Get(p.name)
		if v == "" {
			continue
		}
		if _, perr := time.Parse(dayFormat, v); perr != nil {
			return "", "", fmt.Errorf("%s: want YYYY-MM-DD, got %q", p.name, v)
		}
		*p.dst = v
	}
	if from != "" && to != "" && from > to {
		return "", "", fmt.Errorf("empty range: from %s is after to %s", from, to)
	}
	return from, to, nil
}

func inRange(day, from, to string) bool {
	return (from == "" || day >= from) && (to == "" || day <= to)
}

// parseTimezone reads the optional timezone= parameter (IANA name),
// defaulting to UTC — the API's day-bucketing zone, declared in every
// payload alongside `source` (M6 Task 2). An unresolvable name is a loud
// bad_param, like any other invalid value. Resolution uses the binary's
// embedded tzdata (cmd/tatitok imports time/tzdata) — never
// /etc/timezone, the standing trap.
func parseTimezone(r *http.Request) (*time.Location, error) {
	name := r.URL.Query().Get("timezone")
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("timezone: %q is not a known IANA timezone name", name)
	}
	return loc, nil
}

// filterParamNames are the M5 Task 3 facet filter query parameters —
// repeatable (OR within a dimension, AND across dimensions), matching
// the dashboard semantics exactly. Unknown VALUES are not errors (an
// empty result, declared via the echoed filters); unknown parameter
// NAMES stay rejected (the M4 rule, enforced by checkParams).
var filterParamNames = []string{"harness", "provider", "model", "project", "basis"}

func parseFilters(r *http.Request) store.Filters {
	q := r.URL.Query()
	return store.Filters{
		Harness:  q["harness"],
		Provider: q["provider"],
		Model:    q["model"],
		Project:  q["project"],
		Basis:    q["basis"],
	}
}

// apiStatsDaily mirrors the CLI exactly: plain daily is rollup-backed
// (UTC rollup grain, M3) unless a filter the rollup grain cannot serve
// (basis) forces exact event aggregation; ?by= always aggregates
// events. The serving path is declared in the payload ("source":
// "rollup"|"events") — honesty about the path survives into the
// response.
func (h *Hub) apiStatsDaily(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r, append([]string{"from", "to", "by", "timezone"}, filterParamNames...)...); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	from, to, err := parseDayRange(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	tz, err := parseTimezone(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	ctx := r.Context()
	f := parseFilters(r)
	base := map[string]any{"grain": "day", "tz": tz.String()}
	if !f.IsZero() {
		base["filters"] = f
	}

	if by := r.URL.Query().Get("by"); by != "" {
		rows, err := h.st.DailyBy(ctx, tz, by, f)
		if err != nil {
			if strings.Contains(err.Error(), "unknown --by dimension") {
				writeErr(w, http.StatusBadRequest, "bad_param",
					fmt.Sprintf("by: %q (supported: harness, provider, model, project)", by))
				return
			}
			writeErr(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		filtered := make([]store.DailyByRow, 0, len(rows))
		for _, row := range rows {
			if inRange(row.Date, from, to) {
				filtered = append(filtered, row)
			}
		}
		base["by"] = by
		base["source"] = "events"
		base["daily_by"] = filtered
		writeJSON(w, http.StatusOK, base)
		return
	}

	rows, source, err := h.st.DailyServed(ctx, tz, f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	base["source"] = source
	filtered := make([]store.DailyRow, 0, len(rows))
	for _, row := range rows {
		if inRange(row.Date, from, to) {
			filtered = append(filtered, row)
		}
	}
	base["daily"] = filtered
	writeJSON(w, http.StatusOK, base)
}

var windowRe = regexp.MustCompile(`^([1-9][0-9]{0,2})d$`)

// apiTotals is the convenience aggregate over the daily rows —
// rollup-backed unless a basis filter forces the exact event path
// (declared as "source"). window: "all" (default), "today", or "Nd"
// (last N UTC days including today, N ≤ 365).
func (h *Hub) apiTotals(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r, append([]string{"window", "timezone"}, filterParamNames...)...); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	tz, err := parseTimezone(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "all"
	}
	// The window's reference "today" and lookback are in the requested
	// zone — the rows bucket in tz, so the bounds must too.
	today := time.Now().In(tz).Format(dayFormat)
	from := ""
	switch window {
	case "all":
	case "today":
		from = today
	default:
		m := windowRe.FindStringSubmatch(window)
		if m == nil {
			writeErr(w, http.StatusBadRequest, "bad_param",
				fmt.Sprintf("window: want all, today or Nd (N ≤ 365), got %q", window))
			return
		}
		n, _ := strconv.Atoi(m[1])
		if n > 365 {
			writeErr(w, http.StatusBadRequest, "bad_param",
				fmt.Sprintf("window: want all, today or Nd (N ≤ 365), got %q", window))
			return
		}
		from = time.Now().In(tz).AddDate(0, 0, -(n - 1)).Format(dayFormat)
	}

	f := parseFilters(r)
	rows, source, err := h.st.DailyServed(r.Context(), tz, f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var sums struct {
		store.TokenSums
		store.CostSums
	}
	days := 0
	for _, row := range rows {
		if !inRange(row.Date, from, today) {
			continue
		}
		days++
		sums.Input += row.Input
		sums.Output += row.Output
		sums.CacheWrite += row.CacheWrite
		sums.CacheRead += row.CacheRead
		sums.Reasoning += row.Reasoning
		sums.CostUSDMicro += row.CostUSDMicro
		sums.CostAPIEquivMicro += row.CostAPIEquivMicro
		sums.UnpricedEvents += row.UnpricedEvents
	}
	payload := map[string]any{
		"grain": "day", "tz": tz.String(), "source": source,
		"window": window, "from": from, "to": today,
		"days": days, "totals": sums,
	}
	if !f.IsZero() {
		payload["filters"] = f
	}
	writeJSON(w, http.StatusOK, payload)
}

// planPeriodUsage sums one fixed lookback (rolling week / calendar
// month to date) of a plan's stamped usage.
type planPeriodUsage struct {
	From       string `json:"from"`
	Events     int64  `json:"events"`
	EquivMicro int64  `json:"cost_api_equiv_micro"`
	Unpriced   int64  `json:"events_unpriced"`
}

func sumPeriod(rows []store.PlanEventRow, from time.Time) planPeriodUsage {
	u := planPeriodUsage{From: from.Format(time.RFC3339)}
	for _, r := range rows {
		if r.TS.Before(from) {
			continue
		}
		u.Events++
		u.EquivMicro += r.EquivMicro
		if r.Unpriced {
			u.Unpriced++
		}
	}
	return u
}

// apiPlans (M5 Task 2): the window meter + value panel source. Windows
// are computed from STAMPED plan_included events — one source of truth,
// what Apply/recompute wrote — partitioned by each declared plan's
// window duration. week is the rolling last 7×24h; month is the UTC
// calendar month to date (the value panel compares its equivalent
// against the declared monthly price). Events stamped with a plan name
// no longer declared are counted in unmatched_plan_events — config
// drift is reported, never papered over.
func (h *Hub) apiPlans(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	plans := h.cfg.Overrides.Plans()
	rows, err := h.st.PlanIncludedEvents(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	now := time.Now().UTC()
	weekFrom := now.Add(-7 * 24 * time.Hour)
	monthFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	declared := map[string]bool{}
	for _, p := range plans {
		declared[p.Name] = true
	}
	byPlan := map[string][]store.PlanEventRow{}
	var unmatched int64
	for _, row := range rows {
		if declared[row.Plan] {
			byPlan[row.Plan] = append(byPlan[row.Plan], row)
		} else {
			unmatched++
		}
	}

	type currentWindow struct {
		pricing.WindowUsage
		SecondsToReset int64 `json:"seconds_to_reset"`
	}
	type planPayload struct {
		Name                string          `json:"name"`
		WindowSeconds       int64           `json:"window_seconds"`
		WindowStart         string          `json:"window_start"`
		WeeklyCapEquivMicro *int64          `json:"weekly_cap_equiv_micro"`
		MonthlyPriceMicro   *int64          `json:"monthly_price_micro"`
		CurrentWindow       *currentWindow  `json:"current_window"`
		Week                planPeriodUsage `json:"week"`
		Month               planPeriodUsage `json:"month"`
		WindowsTotal        int             `json:"windows_total"`
	}
	out := make([]planPayload, 0, len(plans))
	for _, p := range plans {
		evs := byPlan[p.Name]
		we := make([]pricing.WindowEvent, len(evs))
		for i, e := range evs {
			we[i] = pricing.WindowEvent{TS: e.TS, Input: e.Input, Output: e.Output,
				CacheWrite: e.CacheWrite, CacheRead: e.CacheRead,
				EquivMicro: e.EquivMicro, Unpriced: e.Unpriced}
		}
		windows := pricing.PlanWindows(we, p.Window, p.WindowStart)
		pp := planPayload{
			Name:                p.Name,
			WindowSeconds:       int64(p.Window.Seconds()),
			WindowStart:         string(p.WindowStart),
			WeeklyCapEquivMicro: p.WeeklyCapEquivMicro,
			MonthlyPriceMicro:   p.MonthlyPriceMicro,
			Week:                sumPeriod(evs, weekFrom),
			Month:               sumPeriod(evs, monthFrom),
			WindowsTotal:        len(windows),
		}
		if cur, ok := pricing.CurrentWindow(windows, now); ok {
			pp.CurrentWindow = &currentWindow{WindowUsage: cur,
				SecondsToReset: int64(cur.End.Sub(now).Seconds())}
		}
		out = append(out, pp)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tz": "UTC", "now": now.Format(time.RFC3339),
		"plans": out, "unmatched_plan_events": unmatched,
	})
}

// apiMetaFacets (M5 Task 3): every filterable dimension's stored values
// with event counts — the dashboard facet rail's source. Project values
// render locally only (localhost serving; leakcheck guards the tree).
func (h *Hub) apiMetaFacets(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	facets, err := h.st.Facets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	for k, v := range facets {
		if v == nil {
			facets[k] = []store.FacetValue{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"facets": facets})
}

// apiMetaModels: the model → family → pricing basis inventory that
// powers dashboard legends.
func (h *Hub) apiMetaModels(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	inv, err := h.st.ModelInventory(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if inv == nil {
		inv = []store.ModelInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": inv})
}
