package hub

// API v1 tests (M4 Task 2): ordinary tests, not parity gates — every
// numeric payload is asserted equal to the direct store query on the
// same fixture-seeded database (all three harnesses).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
	"github.com/harunaltikaya/tatitok/internal/adapters/opencode"
	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// seedHub starts a hub (no watchers) on a DB freshly ingested from all
// three harness fixtures.
func seedHub(t *testing.T) *Hub {
	t.Helper()
	return seedHubWith(t, nil, nil)
}

// seedHubWith is seedHub with a price-override config applied to both
// ingest and the hub, plus optional extra events inserted after the
// fixture ingest (priced by the same overrides).
func seedHubWith(t *testing.T, ov *pricing.Overrides, extra []core.Event) *Hub {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "seeded.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ccRoot, _ := filepath.Abs(ccFixtureRoot)
	cxRoot, _ := filepath.Abs(codexFixtureRoot)
	ocFixture, _ := filepath.Abs(opencodeFixtureDir)
	ocRoot := filepath.Join(t.TempDir(), "opencode")
	if _, err := opencode.BuildFixtureDB(ocFixture, filepath.Join(ocRoot, "opencode.db")); err != nil {
		t.Fatalf("reconstruct fixture db: %v", err)
	}
	for _, in := range []struct {
		a   adapters.Adapter
		src adapters.Source
	}{
		{claudecode.Adapter{}, adapters.Source{Harness: "claude-code", Root: ccRoot, Machine: "gx10"}},
		{codex.Adapter{}, adapters.Source{Harness: "codex", Root: cxRoot, Machine: "gx10"}},
		{opencode.Adapter{}, adapters.Source{Harness: "opencode", Root: ocRoot, Machine: "gx10"}},
	} {
		if _, err := adapters.IngestBackfill(ctx, st, in.a, []adapters.Source{in.src}, ov); err != nil {
			t.Fatalf("%s seed: %v", in.src.Harness, err)
		}
	}
	if len(extra) > 0 {
		for i := range extra {
			if err := pricing.Apply(&extra[i], ov); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.InsertBatch(ctx, extra, store.SourceInfo{
			Harness: extra[0].Harness, Machine: "gx10", Path: "synthetic-now.jsonl",
			MTime: time.Now(), Size: 1, LineCount: len(extra), AdapterVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	h, err := Start(Config{DBPath: dbPath, Addr: "127.0.0.1:0", Overrides: ov})
	if err != nil {
		t.Fatalf("hub start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return h
}

func get(t *testing.T, h *Hub, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get("http://" + h.Addr() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET %s: Content-Type %q, want application/json", path, ct)
	}
	return resp.StatusCode, b
}

func getOK(t *testing.T, h *Hub, path string, into any) {
	t.Helper()
	status, b := get(t, h, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, status, b)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("GET %s: bad JSON: %v\n%s", path, err, b)
	}
}

// assertErrEnvelope: every failure shape is the one envelope. A GET's 400
// is always a bad query parameter, so its code must be bad_param.
func assertErrEnvelope(t *testing.T, h *Hub, path string, wantStatus int) {
	t.Helper()
	status, b := get(t, h, path)
	if status != wantStatus {
		t.Errorf("GET %s = %d, want %d (%s)", path, status, wantStatus, b)
	}
	var e apiError
	if err := json.Unmarshal(b, &e); err != nil || e.Error.Code == "" || e.Error.Message == "" {
		t.Errorf("GET %s: not the error envelope: %s", path, b)
	}
	if wantStatus == http.StatusBadRequest && e.Error.Code != "bad_param" {
		t.Errorf("GET %s: error code %q, want bad_param", path, e.Error.Code)
	}
}

func TestAPIHealth(t *testing.T) {
	h := seedHub(t)
	var got struct {
		Version       string `json:"version"`
		PriceSnapshot string `json:"price_snapshot"`
		Overrides     int    `json:"overrides"`
		DBHash        string `json:"db_hash"`
		StartedAt     string `json:"started_at"`
		Uptime        *int64 `json:"uptime_seconds"`
	}
	getOK(t, h, "/api/v1/health", &got)
	if got.Version == "" || got.PriceSnapshot == "" || got.Uptime == nil {
		t.Errorf("health missing fields: %+v", got)
	}
	if got.DBHash != dbPathHash(h.cfg.DBPath) || len(got.DBHash) != 16 {
		t.Errorf("db_hash %q, want 16-hex hash of the path", got.DBHash)
	}
	// The hash must not leak the path or any of its segments.
	if strings.Contains(got.DBHash, "/") || strings.Contains(got.DBHash, "tmp") {
		t.Errorf("db_hash looks like a path: %q", got.DBHash)
	}
	if _, err := time.Parse(time.RFC3339, got.StartedAt); err != nil {
		t.Errorf("started_at %q: %v", got.StartedAt, err)
	}
}

func TestAPIStatsDaily(t *testing.T) {
	h := seedHub(t)
	ctx := context.Background()

	var got struct {
		Grain string            `json:"grain"`
		TZ    string            `json:"tz"`
		Daily []json.RawMessage `json:"daily"`
	}
	getOK(t, h, "/api/v1/stats/daily", &got)
	if got.Grain != "day" || got.TZ != "UTC" {
		t.Errorf("payload must declare grain/tz: got %q/%q", got.Grain, got.TZ)
	}
	want, err := h.st.DailyFromRollups(ctx, store.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got.Daily)
	var wantRaw []json.RawMessage
	_ = json.Unmarshal(wantJSON, &wantRaw)
	wj, _ := json.Marshal(wantRaw)
	if string(gotJSON) != string(wj) {
		t.Errorf("daily payload != DailyFromRollups\ngot  %s\nwant %s", gotJSON, wj)
	}
	if len(want) < 2 {
		t.Fatalf("fixture corpus spans %d days — range test needs 2+", len(want))
	}

	// from/to filtering: drop the first and last day.
	from, to := want[1].Date, want[len(want)-2].Date
	if from > to {
		from, to = want[1].Date, want[1].Date
	}
	var ranged struct {
		Daily []store.DailyRow `json:"daily"`
	}
	getOK(t, h, "/api/v1/stats/daily?from="+from+"&to="+to, &ranged)
	for _, row := range ranged.Daily {
		if row.Date < from || row.Date > to {
			t.Errorf("row %s outside [%s, %s]", row.Date, from, to)
		}
	}
	wantN := 0
	for _, row := range want {
		if row.Date >= from && row.Date <= to {
			wantN++
		}
	}
	if len(ranged.Daily) != wantN {
		t.Errorf("ranged daily has %d rows, want %d", len(ranged.Daily), wantN)
	}
}

// TestAPITimezone (M6 Task 2): the timezone= parameter buckets days in
// the requested zone and declares it in the payload; whole-hour-offset
// zones serve from the hourly rollups, fractional zones from events —
// each declared in `source`, and the served rows equal exact event
// aggregation in that zone byte-equal (the rollup fast-path is correct,
// not merely present). Invalid IANA names are rejected loudly. UTC
// stays the default (asserted in TestAPIStatsDaily).
func TestAPITimezone(t *testing.T) {
	h := seedHub(t)
	ctx := context.Background()

	check := func(zone, wantSource string) {
		t.Helper()
		var got struct {
			TZ     string           `json:"tz"`
			Source string           `json:"source"`
			Daily  []store.DailyRow `json:"daily"`
		}
		getOK(t, h, "/api/v1/stats/daily?timezone="+zone, &got)
		if got.TZ != zone {
			t.Errorf("%s: payload tz = %q, want %q", zone, got.TZ, zone)
		}
		if got.Source != wantSource {
			t.Errorf("%s: source = %q, want %q", zone, got.Source, wantSource)
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		// Ground truth is exact event aggregation in the zone; the served
		// path (hourly rollups for whole-hour zones) must equal it.
		want, err := h.st.Daily(ctx, loc, store.Filters{})
		if err != nil {
			t.Fatal(err)
		}
		if len(want) == 0 {
			t.Fatalf("%s: fixture corpus produced no rows", zone)
		}
		gj, _ := json.Marshal(got.Daily)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			t.Errorf("%s: %s-served daily != exact event aggregation\ngot  %s\nwant %s",
				zone, wantSource, gj, wj)
		}
	}

	check("Asia/Tokyo", "rollup")       // +09:00, whole-hour
	check("America/New_York", "rollup") // whole-hour DST
	check("Asia/Kolkata", "events")     // +05:30, fractional

	// Totals declare the zone and serving path too.
	var totals struct {
		TZ     string `json:"tz"`
		Source string `json:"source"`
	}
	getOK(t, h, "/api/v1/totals?timezone=Asia/Tokyo", &totals)
	if totals.TZ != "Asia/Tokyo" || totals.Source != "rollup" {
		t.Errorf("totals timezone: tz=%q source=%q, want Asia/Tokyo/rollup", totals.TZ, totals.Source)
	}

	// Invalid IANA names are rejected loudly, on both endpoints.
	assertErrEnvelope(t, h, "/api/v1/stats/daily?timezone=Not/AZone", http.StatusBadRequest)
	assertErrEnvelope(t, h, "/api/v1/totals?timezone=Mars/Phobos", http.StatusBadRequest)
	// Host-magic names are host-dependent (the /etc/timezone trap) and
	// rejected by IANA membership (M6 Codex F4) — time.LoadLocation would
	// resolve "localtime"/"posixrules" from a host zoneinfo dir, so the
	// validation is an allowlist, not "LoadLocation succeeded". A declared
	// payload must name a real IANA zone.
	for _, magic := range []string{"Local", "localtime", "posixrules"} {
		assertErrEnvelope(t, h, "/api/v1/stats/daily?timezone="+magic, http.StatusBadRequest)
		assertErrEnvelope(t, h, "/api/v1/totals?timezone="+magic, http.StatusBadRequest)
	}
}

func TestAPIStatsDailyBy(t *testing.T) {
	h := seedHub(t)
	for _, by := range []string{"harness", "provider", "model", "project", "machine"} {
		var got struct {
			By      string             `json:"by"`
			DailyBy []store.DailyByRow `json:"daily_by"`
		}
		getOK(t, h, "/api/v1/stats/daily?by="+by, &got)
		want, err := h.st.DailyBy(context.Background(), time.UTC, by, store.Filters{})
		if err != nil {
			t.Fatal(err)
		}
		gj, _ := json.Marshal(got.DailyBy)
		wj, _ := json.Marshal(want)
		if got.By != by || string(gj) != string(wj) {
			t.Errorf("by=%s payload != DailyBy", by)
		}
	}
}

// TestAPIFilters (M5 Task 3): repeatable facet filter params — OR
// within a dimension, AND across — on stats/daily and totals; every
// filtered payload equals the equivalent direct store query; the
// serving path is declared ("source": "rollup"|"events"); unknown
// VALUES are empty results with the filters echoed, never errors;
// unknown parameter NAMES stay rejected (the M4 rule).
func TestAPIFilters(t *testing.T) {
	h := seedHub(t)
	ctx := context.Background()

	type dailyPayload struct {
		Source  string             `json:"source"`
		Filters *store.Filters     `json:"filters"`
		Daily   []store.DailyRow   `json:"daily"`
		DailyBy []store.DailyByRow `json:"daily_by"`
	}

	// Rollup-servable filter: source rollup, equals the direct query.
	var got dailyPayload
	getOK(t, h, "/api/v1/stats/daily?harness=claude-code&harness=codex", &got)
	if got.Source != "rollup" || got.Filters == nil || len(got.Filters.Harness) != 2 {
		t.Fatalf("rollup-servable filter: source=%q filters=%+v", got.Source, got.Filters)
	}
	want, err := h.st.DailyFromRollups(ctx, store.Filters{Harness: []string{"claude-code", "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got.Daily)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("filtered daily != direct store query")
	}
	if len(got.Daily) == 0 {
		t.Fatal("fixture-backed filter returned nothing")
	}

	// Basis filter: rollups cannot serve — events path, declared.
	got = dailyPayload{}
	getOK(t, h, "/api/v1/stats/daily?basis=api_price", &got)
	if got.Source != "events" {
		t.Fatalf("basis filter source = %q, want events", got.Source)
	}
	wantEv, err := h.st.Daily(ctx, time.UTC, store.Filters{Basis: []string{"api_price"}})
	if err != nil {
		t.Fatal(err)
	}
	gj, _ = json.Marshal(got.Daily)
	wj, _ = json.Marshal(wantEv)
	if string(gj) != string(wj) {
		t.Errorf("basis-filtered daily != direct store query")
	}

	// by= with a filter: events path, equals the direct query.
	got = dailyPayload{}
	getOK(t, h, "/api/v1/stats/daily?by=model&provider=anthropic", &got)
	if got.Source != "events" {
		t.Fatalf("by+filter source = %q, want events", got.Source)
	}
	wantBy, err := h.st.DailyBy(ctx, time.UTC, "model", store.Filters{Provider: []string{"anthropic"}})
	if err != nil {
		t.Fatal(err)
	}
	gj, _ = json.Marshal(got.DailyBy)
	wj, _ = json.Marshal(wantBy)
	if string(gj) != string(wj) {
		t.Errorf("by+filtered daily_by != direct store query")
	}

	// Unknown VALUE: empty result, declared via the echoed filters — not
	// an error.
	got = dailyPayload{}
	getOK(t, h, "/api/v1/stats/daily?provider=no-such-provider", &got)
	if len(got.Daily) != 0 || got.Daily == nil || got.Filters == nil ||
		len(got.Filters.Provider) != 1 {
		t.Errorf("unknown filter value: daily=%v filters=%+v (want declared empty result)", got.Daily, got.Filters)
	}

	// Totals with filters: equals summing the same filtered rows; source
	// declared on both paths.
	var totals struct {
		Source string `json:"source"`
		Totals struct {
			store.TokenSums
			store.CostSums
		} `json:"totals"`
	}
	getOK(t, h, "/api/v1/totals?harness=opencode", &totals)
	if totals.Source != "rollup" {
		t.Errorf("filtered totals source = %q, want rollup", totals.Source)
	}
	wantRows, err := h.st.DailyFromRollups(ctx, store.Filters{Harness: []string{"opencode"}})
	if err != nil {
		t.Fatal(err)
	}
	var wantIn int64
	for _, r := range wantRows {
		wantIn += r.Input
	}
	if totals.Totals.Input != wantIn || wantIn == 0 {
		t.Errorf("filtered totals input = %d, want %d", totals.Totals.Input, wantIn)
	}
	getOK(t, h, "/api/v1/totals?basis=local", &totals)
	if totals.Source != "events" {
		t.Errorf("basis-filtered totals source = %q, want events", totals.Source)
	}

	// Unknown parameter NAMES remain rejected.
	assertErrEnvelope(t, h, "/api/v1/stats/daily?models=x", http.StatusBadRequest)
	assertErrEnvelope(t, h, "/api/v1/totals?basis_=x", http.StatusBadRequest)
}

// TestAPIStatsSessions: the sessions payload equals the direct store
// query for its params; a session filter forces the events path on the
// daily endpoint and its totals equal the session's row; bad params 400.
func TestAPIStatsSessions(t *testing.T) {
	h := seedHub(t)
	ctx := context.Background()

	type sessionsPayload struct {
		TZ       string                 `json:"tz"`
		Source   string                 `json:"source"`
		Total    int                    `json:"total"`
		Limit    int                    `json:"limit"`
		Filters  *store.Filters         `json:"filters"`
		Sessions []store.SessionSummary `json:"sessions"`
	}
	check := func(query string, tz *time.Location, from, to string, f store.Filters) sessionsPayload {
		t.Helper()
		var got sessionsPayload
		getOK(t, h, "/api/v1/stats/sessions"+query, &got)
		if got.Source != "events" || got.Limit != 200 || got.TZ != tz.String() {
			t.Errorf("%s: source=%q limit=%d tz=%q", query, got.Source, got.Limit, got.TZ)
		}
		want, total, err := h.st.SessionsInRange(ctx, tz, from, to, f, 200)
		if err != nil {
			t.Fatal(err)
		}
		gj, _ := json.Marshal(got.Sessions)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) || got.Total != total {
			t.Errorf("%s: payload != SessionsInRange\ngot  %s (total %d)\nwant %s (total %d)",
				query, gj, got.Total, wj, total)
		}
		return got
	}

	all := check("", time.UTC, "", "", store.Filters{})
	if len(all.Sessions) < 2 || all.Filters != nil {
		t.Fatalf("fixture corpus gave %d sessions (filters %+v)", len(all.Sessions), all.Filters)
	}
	for _, key := range []string{`"session"`, `"harness"`, `"project"`, `"firstTs"`, `"lastTs"`,
		`"events"`, `"inputTokens"`, `"outputTokens"`, `"cacheCreationTokens"`,
		`"cacheReadTokens"`, `"costAPIEquivMicro"`} {
		if _, b := get(t, h, "/api/v1/stats/sessions"); !strings.Contains(string(b), key) {
			t.Errorf("sessions payload lacks %s", key)
		}
	}

	// Range, zone and a filter reach the store call.
	top := all.Sessions[0]
	day := top.FirstTS[:10]
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	check("?from="+day+"&to="+day+"&timezone=Asia/Tokyo", tokyo, day, day, store.Filters{})
	got := check("?harness="+top.Harness, time.UTC, "", "", store.Filters{Harness: []string{top.Harness}})
	if got.Filters == nil || len(got.Filters.Harness) != 1 {
		t.Errorf("filters not echoed: %+v", got.Filters)
	}
	for _, s := range got.Sessions {
		if s.Harness != top.Harness {
			t.Errorf("harness filter let %q through", s.Harness)
		}
	}

	// The session filter on the sessions endpoint itself.
	got = check("?session="+top.Session, time.UTC, "", "", store.Filters{Session: []string{top.Session}})
	if got.Total != 1 || got.Sessions[0].Session != top.Session {
		t.Errorf("session filter: %+v", got.Sessions)
	}

	// The session filter forces "events" on the daily endpoint (UTC, where
	// plain daily is rollup-served) and its totals equal the session's row.
	var daily struct {
		Source  string           `json:"source"`
		Filters *store.Filters   `json:"filters"`
		Daily   []store.DailyRow `json:"daily"`
	}
	getOK(t, h, "/api/v1/stats/daily?session="+top.Session, &daily)
	if daily.Source != "events" || daily.Filters == nil || len(daily.Filters.Session) != 1 {
		t.Fatalf("daily with session: source=%q filters=%+v", daily.Source, daily.Filters)
	}
	var sum store.TokenSums
	var equiv int64
	for _, r := range daily.Daily {
		sum.Input += r.Input
		sum.Output += r.Output
		sum.CacheWrite += r.CacheWrite
		sum.CacheRead += r.CacheRead
		equiv += r.CostAPIEquivMicro
	}
	if sum != top.TokenSums || equiv != top.CostAPIEquivMicro {
		t.Errorf("daily under session %s = %+v / %d, want the row's %+v / %d",
			top.Session, sum, equiv, top.TokenSums, top.CostAPIEquivMicro)
	}
	var by struct {
		Source  string             `json:"source"`
		DailyBy []store.DailyByRow `json:"daily_by"`
	}
	getOK(t, h, "/api/v1/stats/daily?by=harness&session="+top.Session, &by)
	if by.Source != "events" {
		t.Errorf("daily_by with session: source=%q", by.Source)
	}
	sum, equiv = store.TokenSums{}, 0
	for _, r := range by.DailyBy {
		sum.Input += r.Input
		sum.Output += r.Output
		sum.CacheWrite += r.CacheWrite
		sum.CacheRead += r.CacheRead
		equiv += r.CostAPIEquivMicro
	}
	if sum != top.TokenSums || equiv != top.CostAPIEquivMicro {
		t.Errorf("daily_by under session %s = %+v / %d, want the row's %+v / %d",
			top.Session, sum, equiv, top.TokenSums, top.CostAPIEquivMicro)
	}
	var totals struct {
		Source string `json:"source"`
		Totals struct {
			store.TokenSums
			store.CostSums
		} `json:"totals"`
	}
	getOK(t, h, "/api/v1/totals?session="+top.Session, &totals)
	if totals.Source != "events" {
		t.Errorf("totals with session: source=%q", totals.Source)
	}
	if totals.Totals.TokenSums != top.TokenSums || totals.Totals.CostAPIEquivMicro != top.CostAPIEquivMicro {
		t.Errorf("totals under session %s = %+v / %d, want the row's %+v / %d",
			top.Session, totals.Totals.TokenSums, totals.Totals.CostAPIEquivMicro,
			top.TokenSums, top.CostAPIEquivMicro)
	}
	// Activity is always the events path; under the session filter its
	// buckets sum to the session's row.
	var act struct {
		Source  string                 `json:"source"`
		Buckets []store.ActivityBucket `json:"buckets"`
	}
	getOK(t, h, "/api/v1/stats/activity", &act)
	if act.Source != "events" {
		t.Errorf("activity: source=%q, want events", act.Source)
	}
	getOK(t, h, "/api/v1/stats/activity?session="+top.Session, &act)
	var actEvents, actTokens int64
	for _, b := range act.Buckets {
		actEvents += b.Events
		actTokens += b.Tokens
	}
	if act.Source != "events" || actEvents != top.Events ||
		actTokens != top.Input+top.Output+top.CacheWrite+top.CacheRead {
		t.Errorf("activity under session %s: source=%q events=%d tokens=%d, want events, the row's %d / %d",
			top.Session, act.Source, actEvents, actTokens, top.Events,
			top.Input+top.Output+top.CacheWrite+top.CacheRead)
	}

	// 400 bad_param, like the daily endpoint.
	for _, p := range []string{
		"/api/v1/stats/sessions?from=2026-02-02&to=2026-01-01",
		"/api/v1/stats/sessions?from=01-01-2026",
		"/api/v1/stats/sessions?timezone=Not/AZone",
		"/api/v1/stats/sessions?by=harness",
		"/api/v1/stats/sessions?limit=5",
	} {
		assertErrEnvelope(t, h, p, http.StatusBadRequest)
	}
	// The session bound on every endpoint that reads filters: 64 printable
	// runes pass (160 bytes here, so runes are counted, not bytes); 65
	// runes or a control character are 400 bad_param.
	fits := strings.Repeat("aş界🙂", 16)
	if n := utf8.RuneCountInString(fits); n != 64 {
		t.Fatalf("fits has %d runes", n)
	}
	for _, ep := range []string{
		"/api/v1/stats/daily?", "/api/v1/stats/daily?by=harness&", "/api/v1/totals?",
		"/api/v1/stats/activity?", "/api/v1/stats/sessions?",
	} {
		for _, bad := range []string{fits + "a", "a\x01b", "a\u0085b"} {
			assertErrEnvelope(t, h, ep+"session="+url.QueryEscape(bad), http.StatusBadRequest)
		}
		var ok map[string]any
		getOK(t, h, ep+"session="+url.QueryEscape(fits), &ok)
	}
	for _, p := range []string{
		"/api/v1/stats/daily?session=a%0Ab",
		"/api/v1/totals?session=a%7Fb",
	} {
		assertErrEnvelope(t, h, p, http.StatusBadRequest)
	}
	// "" is fine.
	getOK(t, h, "/api/v1/stats/daily?session=", &daily)
	if daily.Source != "events" {
		t.Errorf(`daily with session="": source=%q`, daily.Source)
	}
}

// TestAPIMetaFacets (M5 Task 3): the facet rail's source — every
// filterable dimension enumerated with event counts, equal to the
// direct store query.
func TestAPIMetaFacets(t *testing.T) {
	h := seedHub(t)
	var got struct {
		Facets map[string][]store.FacetValue `json:"facets"`
	}
	getOK(t, h, "/api/v1/meta/facets", &got)
	want, err := h.st.Facets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Facets) != 5 {
		t.Fatalf("facets payload has %d dimensions, want 5: %v", len(got.Facets), got.Facets)
	}
	total, err := h.st.CountEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for dim, vals := range want {
		gj, _ := json.Marshal(got.Facets[dim])
		wj, _ := json.Marshal(vals)
		if string(gj) != string(wj) {
			t.Errorf("facet %s != direct store query", dim)
		}
		var n int64
		for _, v := range vals {
			n += v.Events
		}
		if n != total {
			t.Errorf("facet %s counts sum to %d, want %d", dim, n, total)
		}
	}
	assertErrEnvelope(t, h, "/api/v1/meta/facets?x=1", http.StatusBadRequest)
}

func TestAPITotals(t *testing.T) {
	h := seedHub(t)
	var got struct {
		Window string `json:"window"`
		Days   int    `json:"days"`
		Totals struct {
			store.TokenSums
			store.CostSums
		} `json:"totals"`
	}
	getOK(t, h, "/api/v1/totals", &got)
	want, err := h.st.DailyFromRollups(context.Background(), store.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	var input, cost int64
	for _, row := range want {
		input += row.Input
		cost += row.CostUSDMicro
	}
	if got.Window != "all" || got.Days != len(want) || got.Totals.Input != input || got.Totals.CostUSDMicro != cost {
		t.Errorf("totals: got %+v; want days=%d input=%d cost=%d", got, len(want), input, cost)
	}

	// The fixture corpus is historical: a 1d window over it must be empty
	// (and prove the window arithmetic runs).
	var today struct {
		Days   int `json:"days"`
		Totals struct{ store.TokenSums }
	}
	getOK(t, h, "/api/v1/totals?window=today", &today)
	if today.Days != 0 || today.Totals.Input != 0 {
		t.Errorf("window=today over historical fixtures: %+v, want empty", today)
	}
}

func TestAPIMetaModels(t *testing.T) {
	h := seedHub(t)
	var got struct {
		Models []store.ModelInfo `json:"models"`
	}
	getOK(t, h, "/api/v1/meta/models", &got)
	want, err := h.st.ModelInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got.Models)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("meta/models != ModelInventory\ngot  %s\nwant %s", gj, wj)
	}
	if len(got.Models) == 0 {
		t.Error("fixture-seeded inventory is empty")
	}
	for _, m := range got.Models {
		if m.Basis == "" || m.Model == "" {
			t.Errorf("inventory row missing fields: %+v", m)
		}
	}
}

// TestAPIMethodNotAllowed (Codex M4 finding 5; HEAD ruling, M5 Codex
// round): a known path with a wrong method answers with the JSON
// envelope and "Allow: GET, HEAD" — and HEAD is NOT a wrong method.
// Per RFC 9110 §9.3.2 it is GET without the response body; the mux's
// GET patterns match it by design, so HEAD answers 200 with the GET's
// headers and an empty body. The contract states this; this test pins
// both halves permanently.
func TestAPIMethodNotAllowed(t *testing.T) {
	h := seedHub(t)
	// /api/v1/stream must stay LAST: its HEAD leaves the SSE handler
	// running on the keep-alive connection, so the next request on that
	// connection hangs until the test timeout. New paths go before it.
	paths := []string{
		"/api/v1/health", "/api/v1/stats/daily", "/api/v1/totals",
		"/api/v1/meta/models", "/api/v1/meta/facets", "/api/v1/plans",
		"/api/v1/sources", "/api/v1/stats/sessions", "/api/v1/stream",
	}
	if last := paths[len(paths)-1]; last != "/api/v1/stream" {
		t.Fatalf("last path is %s, want /api/v1/stream: its HEAD leaves the SSE handler "+
			"on the keep-alive connection, so any request after it hangs until the test timeout", last)
	}
	for _, p := range paths {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequest(method, "http://"+h.Addr()+p, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, p, resp.StatusCode)
			}
			if allow := resp.Header.Get("Allow"); allow != "GET, HEAD" {
				t.Errorf("%s %s: Allow = %q, want \"GET, HEAD\"", method, p, allow)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s: Content-Type %q, want the JSON envelope", method, p, ct)
			}
			var e apiError
			if err := json.Unmarshal(b, &e); err != nil || e.Error.Code != "method_not_allowed" {
				t.Errorf("%s %s: not the error envelope: %s", method, p, b)
			}
		}

		// HEAD: 200, the GET's headers, empty body (net/http strips it;
		// the stream endpoint answers with its SSE headers and is closed
		// by the client like any other consumer).
		req, err := http.NewRequest(http.MethodHead, "http://"+h.Addr()+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD %s = %d, want 200", p, resp.StatusCode)
		}
		if len(b) != 0 {
			t.Errorf("HEAD %s carried a body (%d bytes)", p, len(b))
		}
		wantCT := "application/json"
		if p == "/api/v1/stream" {
			wantCT = "text/event-stream"
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, wantCT) {
			t.Errorf("HEAD %s: Content-Type %q, want %s (the GET's headers)", p, ct, wantCT)
		}
	}
}

// TestAPIPlans (M5 Task 2): the window meter + value panel payload over
// a plan-seeded database — fixture history plus one synthetic event at
// now, so the current window is live. Every number is asserted equal to
// the direct store query + the same window math (the api_test
// discipline).
func TestAPIPlans(t *testing.T) {
	ovPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(ovPath, []byte(`{
		"plans": [{
			"name": "claude-max",
			"label": "Claude Max 5x",
			"matchers": [{"harness": "claude-code"}],
			"window": "5h",
			"weekly_cap_equiv_usd": "120.00",
			"monthly_price_usd": "200.00"
		}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err := pricing.LoadOverrides(ovPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	fresh := core.Event{
		ID: core.EventID("claude-code", "plans-now-msg", "plans-now-req"),
		TS: now, Machine: "gx10", SourceKind: core.SourceKindHarnessLog,
		Harness: "claude-code", Provider: "anthropic",
		Model: "claude-fable-5", ModelFamily: "claude-fable-5",
		TokensInput: 1000, TokensOutput: 100, Accuracy: core.AccuracyExact,
	}
	h := seedHubWith(t, ov, []core.Event{fresh})

	var got struct {
		TZ    string `json:"tz"`
		Plans []struct {
			Name                string `json:"name"`
			Label               string `json:"label"`
			WindowSeconds       int64  `json:"window_seconds"`
			WindowStart         string `json:"window_start"`
			WeeklyCapEquivMicro *int64 `json:"weekly_cap_equiv_micro"`
			MonthlyPriceMicro   *int64 `json:"monthly_price_micro"`
			CurrentWindow       *struct {
				Start          time.Time `json:"start"`
				End            time.Time `json:"end"`
				Events         int64     `json:"events"`
				Input          int64     `json:"input"`
				EquivMicro     int64     `json:"cost_api_equiv_micro"`
				SecondsToReset int64     `json:"seconds_to_reset"`
			} `json:"current_window"`
			WeekRaw      json.RawMessage `json:"week"`
			MonthRaw     json.RawMessage `json:"month"`
			WindowsTotal int             `json:"windows_total"`
		} `json:"plans"`
		UnmatchedPlanEvents int64 `json:"unmatched_plan_events"`
	}
	getOK(t, h, "/api/v1/plans", &got)
	if len(got.Plans) != 1 || got.TZ != "UTC" || got.UnmatchedPlanEvents != 0 {
		t.Fatalf("plans payload shape: %+v", got)
	}
	p := got.Plans[0]
	if p.Name != "claude-max" || p.Label != "Claude Max 5x" || p.WindowSeconds != 5*3600 || p.WindowStart != "floored" ||
		p.WeeklyCapEquivMicro == nil || *p.WeeklyCapEquivMicro != 120_000_000 ||
		p.MonthlyPriceMicro == nil || *p.MonthlyPriceMicro != 200_000_000 {
		t.Fatalf("plan declaration not echoed: %+v", p)
	}

	// Cross-check against the direct store query + the same window math.
	rows, err := h.st.PlanIncludedEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no plan_included rows after plan-seeded ingest")
	}
	we := make([]pricing.WindowEvent, len(rows))
	for i, e := range rows {
		we[i] = pricing.WindowEvent{TS: e.TS, Input: e.Input, EquivMicro: e.EquivMicro, Unpriced: e.Unpriced}
	}
	windows := pricing.PlanWindows(we, 5*time.Hour, pricing.AnchorFloored)
	if p.WindowsTotal != len(windows) {
		t.Errorf("windows_total = %d, want %d", p.WindowsTotal, len(windows))
	}

	// The synthetic at-now event keeps a window open: the meter ticks.
	if p.CurrentWindow == nil {
		t.Fatal("current_window is null despite an event at now")
	}
	cur, ok := pricing.CurrentWindow(windows, time.Now().UTC())
	if !ok {
		t.Fatal("direct window math has no current window but the API does")
	}
	if !p.CurrentWindow.Start.Equal(cur.Start) || !p.CurrentWindow.End.Equal(cur.End) ||
		p.CurrentWindow.Events != cur.Events || p.CurrentWindow.Input != cur.Input ||
		p.CurrentWindow.EquivMicro != cur.EquivMicro {
		t.Errorf("current_window %+v != direct math %+v", p.CurrentWindow, cur)
	}
	if p.CurrentWindow.SecondsToReset <= 0 || p.CurrentWindow.SecondsToReset > 5*3600 {
		t.Errorf("seconds_to_reset = %d, want within (0, 18000]", p.CurrentWindow.SecondsToReset)
	}
	// 1000×$10 + 100×$50 per Mtok = 15000 micro — the fresh event's
	// equivalent is in the current window.
	if p.CurrentWindow.EquivMicro < 15_000 {
		t.Errorf("current window equivalent %d lacks the fresh event's 15000", p.CurrentWindow.EquivMicro)
	}

	// Week and month sums match the direct computation.
	var week, month planPeriodUsage
	if err := json.Unmarshal(p.WeekRaw, &week); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(p.MonthRaw, &month); err != nil {
		t.Fatal(err)
	}
	wantWeek := sumPeriod(rows, time.Now().UTC().Add(-7*24*time.Hour))
	nowUTC := time.Now().UTC()
	wantMonth := sumPeriod(rows, time.Date(nowUTC.Year(), nowUTC.Month(), 1, 0, 0, 0, 0, time.UTC))
	if week.Events != wantWeek.Events || week.EquivMicro != wantWeek.EquivMicro {
		t.Errorf("week = %+v, want %+v", week, wantWeek)
	}
	if month.Events != wantMonth.Events || month.EquivMicro != wantMonth.EquivMicro {
		t.Errorf("month = %+v, want %+v", month, wantMonth)
	}

	// No plans declared: empty payload, not an error.
	plain := seedHub(t)
	var none struct {
		Plans               []json.RawMessage `json:"plans"`
		UnmatchedPlanEvents int64             `json:"unmatched_plan_events"`
	}
	getOK(t, plain, "/api/v1/plans", &none)
	if len(none.Plans) != 0 || none.UnmatchedPlanEvents != 0 {
		t.Fatalf("plan-less hub: %+v", none)
	}

	// A plan without a label still carries the key, as "" (the card
	// falls back to the name).
	unlabeledPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(unlabeledPath, []byte(`{
		"plans": [{"name": "chatgpt-plus", "matchers": [{"harness": "codex"}], "window": "5h"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	uov, err := pricing.LoadOverrides(unlabeledPath)
	if err != nil {
		t.Fatal(err)
	}
	var unlabeled struct {
		Plans []map[string]json.RawMessage `json:"plans"`
	}
	getOK(t, seedHubWith(t, uov, nil), "/api/v1/plans", &unlabeled)
	if len(unlabeled.Plans) != 1 {
		t.Fatalf("unlabeled hub: %d plans, want 1", len(unlabeled.Plans))
	}
	if got := string(unlabeled.Plans[0]["label"]); got != `""` {
		t.Fatalf("unlabeled plan: label = %s, want \"\"", got)
	}
}

func TestAPIErrors(t *testing.T) {
	h := seedHub(t)
	cases := []struct {
		path   string
		status int
	}{
		{"/api/v1/stats/daily?frm=2026-01-01", http.StatusBadRequest},                // unknown param rejected
		{"/api/v1/stats/daily?from=01-01-2026", http.StatusBadRequest},               // bad date
		{"/api/v1/stats/daily?from=2026-02-02&to=2026-01-01", http.StatusBadRequest}, // empty range
		{"/api/v1/stats/daily?by=banana", http.StatusBadRequest},                     // unknown dimension
		{"/api/v1/totals?window=yesterday", http.StatusBadRequest},                   // bad window
		{"/api/v1/totals?window=9999d", http.StatusBadRequest},                       // window too large
		{"/api/v1/health?verbose=1", http.StatusBadRequest},                          // no params on health
		{"/api/v1/nope", http.StatusNotFound},                                        // unknown API path is JSON
		{fmt.Sprintf("/api/v1/stats/daily/%s", "extra"), http.StatusNotFound},
	}
	for _, c := range cases {
		assertErrEnvelope(t, h, c.path, c.status)
	}
}

// TestAPISources: the ingest-health payload — exact keys, "~" for the
// home directory, null times for targets never ingested, and
// last_event_at per harness on every target of that harness.
func TestAPISources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	whole := fixtureSessionFiles(t)[0]
	fed := filepath.Join(home, "fed")
	if err := os.MkdirAll(filepath.Join(fed, whole.project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fed, whole.project, whole.name), whole.content, 0o644); err != nil {
		t.Fatal(err)
	}
	idle := filepath.Join(home, "idle")
	codexRoot := filepath.Join(home, "codex")
	for _, d := range []string{idle, codexRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := startWatchHub(t, []WatchTarget{
		{Adapter: claudecode.Adapter{}, Source: adapters.Source{Harness: "claude-code", Root: fed, Machine: "gx10"}},
		{Adapter: pollOnlyAdapter{}, Source: adapters.Source{Harness: "claude-code", Root: idle, Machine: "gx10"}},
		{Adapter: codex.Adapter{}, Source: adapters.Source{Harness: "codex", Root: codexRoot, Machine: "gx10"}},
	}, 100*time.Millisecond, time.Hour)
	waitFor(t, "catch-up pass over the fed root", func() bool { return h.w.passes.Load() >= 1 })

	var got struct {
		Now     string           `json:"now"`
		Sources []map[string]any `json:"sources"`
	}
	getOK(t, h, "/api/v1/sources", &got)
	if _, err := time.Parse(time.RFC3339, got.Now); err != nil {
		t.Errorf("now %q: %v", got.Now, err)
	}
	if len(got.Sources) != 3 {
		t.Fatalf("%d sources, want 3 in registration order: %v", len(got.Sources), got.Sources)
	}
	keys := []string{"harness", "root", "watch", "last_ingest_at", "parse_errors_last_pass", "last_event_at"}
	for i, s := range got.Sources {
		if len(s) != len(keys) {
			t.Errorf("source %d has keys %v, want exactly %v", i, s, keys)
		}
		for _, k := range keys {
			if _, ok := s[k]; !ok {
				t.Errorf("source %d missing %q", i, k)
			}
		}
	}
	last, err := h.st.LastEventByHarness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantLast := last["claude-code"].UTC().Format(time.RFC3339)

	fedRow, idleRow, cxRow := got.Sources[0], got.Sources[1], got.Sources[2]
	if fedRow["root"] != "~/fed" || fedRow["watch"] != "fsnotify" || fedRow["parse_errors_last_pass"] != float64(0) {
		t.Errorf("fed row = %v", fedRow)
	}
	if s, ok := fedRow["last_ingest_at"].(string); !ok {
		t.Errorf("fed last_ingest_at = %v, want a time", fedRow["last_ingest_at"])
	} else if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("fed last_ingest_at %q: %v", s, err)
	}
	if fedRow["last_event_at"] != wantLast {
		t.Errorf("fed last_event_at = %v, want %s", fedRow["last_event_at"], wantLast)
	}
	if idleRow["root"] != "~/idle" || idleRow["watch"] != "polling" || idleRow["last_ingest_at"] != nil {
		t.Errorf("idle row = %v, want ~/idle, polling, last_ingest_at null", idleRow)
	}
	if idleRow["last_event_at"] != wantLast {
		t.Errorf("idle last_event_at = %v, want the harness's %s", idleRow["last_event_at"], wantLast)
	}
	if cxRow["harness"] != "codex" || cxRow["last_ingest_at"] != nil || cxRow["last_event_at"] != nil {
		t.Errorf("codex row = %v, want both times null", cxRow)
	}
}

// TestAPISourcesNoWatcher: a hub without watch targets serves [].
func TestAPISourcesNoWatcher(t *testing.T) {
	h := seedHub(t)
	status, b := get(t, h, "/api/v1/sources")
	if status != http.StatusOK || !strings.Contains(string(b), `"sources":[]`) {
		t.Errorf("GET /api/v1/sources = %d %s, want 200 with \"sources\":[]", status, b)
	}
	assertErrEnvelope(t, h, "/api/v1/sources?x=1", http.StatusBadRequest)
}

func TestTildeHome(t *testing.T) {
	for _, c := range []struct{ path, home, want string }{
		{"/home/user", "/home/user", "~"},
		{"/home/user/.claude/projects", "/home/user", "~/.claude/projects"},
		{"/srv/userx/logs", "/srv/user", "/srv/userx/logs"}, // sibling, not a child
		{"/srv/logs", "/home/user", "/srv/logs"},
		{"/home/user/logs", "", "/home/user/logs"},
	} {
		if got := tildeHome(c.path, c.home); got != c.want {
			t.Errorf("tildeHome(%q, %q) = %q, want %q", c.path, c.home, got, c.want)
		}
	}
}
