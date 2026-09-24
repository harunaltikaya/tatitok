package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// tatitok_day(ts, tz) is a deterministic custom SQL function: the local
// calendar day (YYYY-MM-DD) of an RFC3339 UTC timestamp in the given IANA
// timezone. Registering it lets day bucketing — a query/display concern,
// hard rule 9 — run INSIDE the SQL aggregation with the exact same rule
// the Go side used, instead of loading every event into Go.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("tatitok_day", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ts, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_day: ts is %T, want TEXT", args[0])
			}
			tzName, ok := args[1].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_day: tz is %T, want TEXT", args[1])
			}
			loc, err := lookupLocation(tzName)
			if err != nil {
				return nil, err
			}
			t, err := time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				return nil, fmt.Errorf("tatitok_day: %w", err)
			}
			return t.In(loc).Format("2006-01-02"), nil
		})
	// tatitok_weekday(ts, tz) / tatitok_hour(ts, tz) bucket a UTC timestamp by
	// its LOCAL weekday (0=Sunday..6=Saturday, Go's time.Weekday) and
	// hour-of-day (0..23) in tz — the activity heatmap's buckets (M8 1L), the
	// same tz rule tatitok_day uses, run inside SQL. Aggregating from events
	// (not rollup_hourly) keeps this exact for fractional offsets (+05:30 etc.),
	// where a UTC hour straddles two local hours.
	sqlite.MustRegisterDeterministicScalarFunction("tatitok_weekday", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ts, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_weekday: ts is %T, want TEXT", args[0])
			}
			tzName, ok := args[1].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_weekday: tz is %T, want TEXT", args[1])
			}
			loc, err := lookupLocation(tzName)
			if err != nil {
				return nil, err
			}
			t, err := time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				return nil, fmt.Errorf("tatitok_weekday: %w", err)
			}
			return int64(t.In(loc).Weekday()), nil
		})
	sqlite.MustRegisterDeterministicScalarFunction("tatitok_hour", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ts, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_hour: ts is %T, want TEXT", args[0])
			}
			tzName, ok := args[1].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_hour: tz is %T, want TEXT", args[1])
			}
			loc, err := lookupLocation(tzName)
			if err != nil {
				return nil, err
			}
			t, err := time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				return nil, fmt.Errorf("tatitok_hour: %w", err)
			}
			return int64(t.In(loc).Hour()), nil
		})
	// tatitok_source_id(harness, path) exposes core.SourceID to SQL so the
	// source-lineage migration can backfill the sources table with the
	// exact same ID the Go ingest path stamps.
	sqlite.MustRegisterDeterministicScalarFunction("tatitok_source_id", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			harness, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_source_id: harness is %T, want TEXT", args[0])
			}
			path, ok := args[1].(string)
			if !ok {
				return nil, fmt.Errorf("tatitok_source_id: path is %T, want TEXT", args[1])
			}
			return core.SourceID(harness, path), nil
		})
}

var locCache sync.Map // tz name -> *time.Location

func lookupLocation(name string) (*time.Location, error) {
	if loc, ok := locCache.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("tatitok_day: %w", err)
	}
	locCache.Store(name, loc)
	return loc, nil
}

// TokenSums are the four parity-relevant token counters — the
// zero-tolerance comparison struct, deliberately unchanged by M3 (cost
// and reasoning live in CostSums).
type TokenSums struct {
	Input      int64 `json:"inputTokens"`
	Output     int64 `json:"outputTokens"`
	CacheWrite int64 `json:"cacheCreationTokens"`
	CacheRead  int64 `json:"cacheReadTokens"`
}

func (t *TokenSums) add(o TokenSums) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheWrite += o.CacheWrite
	t.CacheRead += o.CacheRead
}

// CostSums are the M3 Task 4 additions: reasoning tokens
// (codex/opencode report them; others sum 0) and integer micro-USD cost
// columns (rendered to dollars only at the CLI edge). UnpricedEvents
// keeps cost sums honest — a nonzero value means the cost column is a
// floor, not a total.
type CostSums struct {
	Reasoning         int64 `json:"reasoningTokens"`
	CostUSDMicro      int64 `json:"costUSDMicro"`
	CostAPIEquivMicro int64 `json:"costAPIEquivMicro"`
	UnpricedEvents    int64 `json:"unpricedEvents"`
}

func (c *CostSums) addCost(o CostSums) {
	c.Reasoning += o.Reasoning
	c.CostUSDMicro += o.CostUSDMicro
	c.CostAPIEquivMicro += o.CostAPIEquivMicro
	c.UnpricedEvents += o.UnpricedEvents
}

// ModelSums is a per-model breakdown within a day or session.
type ModelSums struct {
	Model string `json:"modelName"`
	TokenSums
	CostSums
}

// HarnessSums is a per-harness breakdown within a day (M2 Task 5: one DB
// holds claude-code, codex and opencode side by side).
type HarnessSums struct {
	Harness string `json:"harness"`
	TokenSums
	CostSums
}

// DailyRow is one local-time day. Date is YYYY-MM-DD in the query
// timezone — ccusage buckets days in local time, so parity comparisons
// must use the timezone recorded in expected/META.json.
type DailyRow struct {
	Date string `json:"date"`
	TokenSums
	CostSums
	ModelsUsed        []string      `json:"modelsUsed"`
	ModelBreakdowns   []ModelSums   `json:"modelBreakdowns"`
	HarnessBreakdowns []HarnessSums `json:"harnessBreakdowns"`
}

// SessionRow is one harness session. Identity is the composite
// (machine, harness, session_id) — M3 Task 0: session ids are native per
// harness and CAN collide across harnesses and across machines (and the
// empty id is common to several), so neither session_id alone nor
// (harness, session_id) ever keys a row.
type SessionRow struct {
	Machine   string `json:"machine"`
	Harness   string `json:"harness"`
	SessionID string `json:"sessionId"`
	Project   string `json:"projectPath,omitempty"`
	TokenSums
	CostSums
	ModelsUsed   []string `json:"modelsUsed"`
	LastActivity string   `json:"lastActivity"` // YYYY-MM-DD in query tz
}

// Filters is the M5 Task 3 facet filter set: values OR within a
// dimension, dimensions AND across — the dashboard semantics, shared by
// API and CLI so dashboard claims stay CLI-verifiable. Empty slices
// constrain nothing. Model matches the RAW model column (consistent
// with every stats surface); Basis matches stored cost_basis with NULL
// reading as 'unknown'; Session matches the raw session_id with NULL
// reading as the empty id. Unknown values are not errors — they match
// nothing; declaring the empty result is the caller's job.
type Filters struct {
	Harness  []string `json:"harness,omitempty"`
	Provider []string `json:"provider,omitempty"`
	Model    []string `json:"model,omitempty"`
	Project  []string `json:"project,omitempty"`
	Basis    []string `json:"basis,omitempty"`
	Session  []string `json:"session,omitempty"`
}

// IsZero reports an unconstrained filter set.
func (f Filters) IsZero() bool {
	return len(f.Harness) == 0 && len(f.Provider) == 0 && len(f.Model) == 0 &&
		len(f.Project) == 0 && len(f.Basis) == 0 && len(f.Session) == 0
}

// RollupServable reports whether the UTC rollup grain carries every
// constrained dimension. Basis and session are the ones it lacks —
// such queries fall back to exact event aggregation (declared in the
// API payload as "source": "events").
func (f Filters) RollupServable() bool { return len(f.Basis) == 0 && len(f.Session) == 0 }

// predicate renders the filter as an "AND col IN (…)" SQL fragment
// (empty when unconstrained) with its args, over the given per-dimension
// column expressions.
func (f Filters) predicate(harness, provider, model, project, basis, session string) (string, []any) {
	var sb strings.Builder
	var args []any
	for _, d := range []struct {
		col  string
		vals []string
	}{
		{harness, f.Harness}, {provider, f.Provider}, {model, f.Model},
		{project, f.Project}, {basis, f.Basis}, {session, f.Session},
	} {
		if len(d.vals) == 0 {
			continue
		}
		sb.WriteString(" AND " + d.col + " IN (")
		for i := range d.vals {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('?')
			args = append(args, d.vals[i])
		}
		sb.WriteByte(')')
	}
	return sb.String(), args
}

// eventsPredicate filters the usage_events table (NULL-safe exprs).
func (f Filters) eventsPredicate() (string, []any) {
	return f.predicate("COALESCE(harness, '')", "provider", "model",
		"COALESCE(project, '')", "COALESCE(cost_basis, 'unknown')",
		"COALESCE(session_id, '')")
}

// rollupPredicate filters rollup_daily (columns are NOT NULL there);
// only valid when RollupServable.
func (f Filters) rollupPredicate() (string, []any) {
	return f.predicate("harness", "provider", "model", "project", "", "")
}

// sumAPIEquiv is the events path's API-equivalent sum. Daily and
// SessionsInRange both use it, so a session's value is summed exactly
// like a day's.
const sumAPIEquiv = "SUM(COALESCE(cost_api_equiv_micro, 0))"

// tsMillis is ts in one fixed width (millisecond precision), so MIN and
// MAX over it are chronological. ts itself is RFC3339Nano with trailing
// zeros trimmed, and text order puts "…:00.5Z" before "…:00Z".
const tsMillis = "strftime('%Y-%m-%dT%H:%M:%fZ', ts)"

// Daily returns per-day token sums bucketed in tz, oldest day first,
// with per-model and per-harness breakdowns, restricted by f (M5
// Task 3: OR within a dimension, AND across). Aggregation runs in SQL:
// one GROUP BY (day, harness, model) pass with the timezone rule
// applied via tatitok_day; Go only assembles the per-day rows from the
// (few) group rows. tz must be resolvable by its name (IANA, "UTC" or
// "Local").
func (s *Store) Daily(ctx context.Context, tz *time.Location, f Filters) ([]DailyRow, error) {
	pred, fargs := f.eventsPredicate()
	rows, err := s.db.QueryContext(ctx, `SELECT tatitok_day(ts, ?) AS day,
			COALESCE(harness, '') AS h, model,
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(COALESCE(tokens_reasoning, 0)),
			SUM(COALESCE(cost_usd_micro, 0)),
			`+sumAPIEquiv+`,
			SUM(cost_usd_micro IS NULL)
		FROM usage_events
		WHERE 1=1`+pred+`
		GROUP BY day, h, model
		ORDER BY day, h, model`, append([]any{tz.String()}, fargs...)...)
	if err != nil {
		return nil, err
	}
	return assembleDaily(rows)
}

// assembleDaily turns ordered (day, harness, model, sums) group rows
// into DailyRows — shared by the event-direct path (Daily) and the
// rollup-served path (DailyFromRollups), so the two are byte-equal by
// construction wherever their groupings agree.
func assembleDaily(rows *sql.Rows) ([]DailyRow, error) {
	defer func() { _ = rows.Close() }()
	var out []DailyRow
	models := map[string]*ModelSums{} // per current day
	flushModels := func() {
		if len(out) == 0 {
			return
		}
		d := &out[len(out)-1]
		names := make([]string, 0, len(models))
		for name := range models {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			d.ModelBreakdowns = append(d.ModelBreakdowns, *models[name])
			d.ModelsUsed = append(d.ModelsUsed, name)
		}
		models = map[string]*ModelSums{}
	}
	for rows.Next() {
		var day, h string
		var sums TokenSums
		var costs CostSums
		var model string
		if err := rows.Scan(&day, &h, &model,
			&sums.Input, &sums.Output, &sums.CacheWrite, &sums.CacheRead,
			&costs.Reasoning, &costs.CostUSDMicro,
			&costs.CostAPIEquivMicro, &costs.UnpricedEvents); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Date != day {
			flushModels()
			out = append(out, DailyRow{Date: day})
		}
		d := &out[len(out)-1]
		d.add(sums)
		d.addCost(costs)
		// rows arrive ordered by harness within the day: extend or append
		if n := len(d.HarnessBreakdowns); n > 0 && d.HarnessBreakdowns[n-1].Harness == h {
			d.HarnessBreakdowns[n-1].add(sums)
			d.HarnessBreakdowns[n-1].addCost(costs)
		} else {
			d.HarnessBreakdowns = append(d.HarnessBreakdowns,
				HarnessSums{Harness: h, TokenSums: sums, CostSums: costs})
		}
		// models merge across harnesses (a model may appear in several)
		m := models[model]
		if m == nil {
			m = &ModelSums{Model: model}
			models[model] = m
		}
		m.add(sums)
		m.addCost(costs)
	}
	flushModels()
	return out, rows.Err()
}

// Sessions returns per-session token sums, most recent activity last.
// Rows key on (machine, harness, session_id) — see SessionRow. A
// non-empty harness restricts the report (`stats --harness`).
// Aggregation runs in SQL: a GROUP BY (machine, harness, session, model)
// pass for sums, models and last-activity day (MAX over tatitok_day is
// sound — day strings are fixed-width, so lexicographic max is
// chronological max), plus one GROUP BY (machine, harness, session) pass
// whose bare project column SQLite takes from the MIN(ts) row (the
// session's first event, matching the legacy scan order).
func (s *Store) Sessions(ctx context.Context, tz *time.Location, harness string) ([]SessionRow, error) {
	first, err := s.sessionFirstProjects(ctx, harness)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT machine,
			COALESCE(harness, '') AS h,
			COALESCE(session_id, '') AS sid,
			model, MAX(tatitok_day(ts, ?1)),
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(COALESCE(tokens_reasoning, 0)),
			SUM(COALESCE(cost_usd_micro, 0)),
			SUM(COALESCE(cost_api_equiv_micro, 0)),
			SUM(cost_usd_micro IS NULL)
		FROM usage_events
		WHERE ?2 = '' OR harness = ?2
		GROUP BY machine, h, sid, model
		ORDER BY machine, h, sid, model`, tz.String(), harness)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SessionRow
	for rows.Next() {
		var m, h, sid, model, last string
		var sums TokenSums
		var costs CostSums
		if err := rows.Scan(&m, &h, &sid, &model, &last,
			&sums.Input, &sums.Output, &sums.CacheWrite, &sums.CacheRead,
			&costs.Reasoning, &costs.CostUSDMicro,
			&costs.CostAPIEquivMicro, &costs.UnpricedEvents); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Machine != m ||
			out[n-1].Harness != h || out[n-1].SessionID != sid {
			out = append(out, SessionRow{Machine: m, Harness: h, SessionID: sid,
				Project: first[sessionKey{m, h, sid}]})
		}
		sr := &out[len(out)-1]
		sr.add(sums)
		sr.addCost(costs)
		sr.ModelsUsed = append(sr.ModelsUsed, model)
		if last > sr.LastActivity {
			sr.LastActivity = last
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivity != out[j].LastActivity {
			return out[i].LastActivity < out[j].LastActivity
		}
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		if out[i].Harness != out[j].Harness {
			return out[i].Harness < out[j].Harness
		}
		return out[i].Machine < out[j].Machine
	})
	return out, nil
}

// DailyByRow is one (day, dimension value) group of `stats --daily
// --by harness|provider|model|project|machine` (M3 Task 4).
type DailyByRow struct {
	Date string `json:"date"`
	Key  string `json:"key"`
	TokenSums
	CostSums
	// RatedEvents counts the group's events that resolved SOME rate: a cost
	// (api_price, or the $0 of local/free — priced by design) or, for
	// plan_included, an API-equivalent. A key whose RatedEvents is 0 has no
	// price at all — its $0 is unknown, not zero (the UI says "unpriced").
	RatedEvents int64 `json:"ratedEvents"`
}

// dailyByDims maps the --by dimension name to its NULL-safe column
// expression. model is the RAW model string (consistent with every
// other stats surface); family-level slicing is deferred — the rollup
// table carries model_family for it. machine is NOT NULL on events, so
// no COALESCE is needed (like provider/model).
var dailyByDims = map[string]string{
	"harness":  "COALESCE(harness, '')",
	"provider": "provider",
	"model":    "model",
	"project":  "COALESCE(project, '')",
	"machine":  "machine",
}

// DailyBy returns per-day sums broken down by one dimension, oldest day
// first, keys ordered within the day, restricted by f. Totals across a
// day's keys equal the Daily row for that day (tested).
func (s *Store) DailyBy(ctx context.Context, tz *time.Location, dim string, f Filters) ([]DailyByRow, error) {
	expr, ok := dailyByDims[dim]
	if !ok {
		return nil, fmt.Errorf("unknown --by dimension %q (supported: harness, provider, model, project, machine)", dim)
	}
	pred, fargs := f.eventsPredicate()
	rows, err := s.db.QueryContext(ctx, `SELECT tatitok_day(ts, ?) AS day,
			`+expr+` AS key,
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(COALESCE(tokens_reasoning, 0)),
			SUM(COALESCE(cost_usd_micro, 0)),
			SUM(COALESCE(cost_api_equiv_micro, 0)),
			SUM(cost_usd_micro IS NULL),
			SUM(cost_usd_micro IS NOT NULL
				AND NOT (cost_basis = 'plan_included' AND cost_api_equiv_micro IS NULL))
		FROM usage_events
		WHERE 1=1`+pred+`
		GROUP BY day, key
		ORDER BY day, key`, append([]any{tz.String()}, fargs...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DailyByRow
	for rows.Next() {
		var r DailyByRow
		if err := rows.Scan(&r.Date, &r.Key,
			&r.Input, &r.Output, &r.CacheWrite, &r.CacheRead,
			&r.Reasoning, &r.CostUSDMicro,
			&r.CostAPIEquivMicro, &r.UnpricedEvents, &r.RatedEvents); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActivityBucket is one (weekday, hour) cell of the activity heatmap (M8 1L):
// the event count and token volume that fall in that LOCAL weekday × hour-of-
// day in the query timezone. Weekday is Go's time.Weekday (0=Sunday..6=
// Saturday); Hour is 0..23.
type ActivityBucket struct {
	Weekday int   `json:"weekday"`
	Hour    int   `json:"hour"`
	Events  int64 `json:"events"`
	Tokens  int64 `json:"tokens"`
}

// Activity returns the weekday × hour activity buckets over [from, to] in tz,
// restricted by f — a new VIEW of the same events the daily path serves. It
// aggregates from usage_events (exact for every zone, fractional offsets
// included — a UTC hour can't be split into a local hour under +05:30), bucketed
// by (weekday, hour) in tz via tatitok_weekday/tatitok_hour, ranged with the
// same tatitok_day the daily path uses, and filtered with the same
// eventsPredicate. Tokens is the canonical 4-field total the dashboard reports
// (input+output+cache-write+cache-read) — reasoning is EXCLUDED, matching
// web/src/api.ts totalTokens (codex's output already includes reasoning, so
// summing tokens_reasoning again would double-count); Events the count.
// Read-only aggregation: no counting change. Empty buckets are simply absent
// (≤168 rows).
func (s *Store) Activity(ctx context.Context, tz *time.Location, from, to string, f Filters) ([]ActivityBucket, error) {
	tzName := tz.String()
	args := []any{tzName, tzName} // tatitok_weekday, tatitok_hour
	where := ""
	if from != "" {
		where += " AND tatitok_day(ts, ?) >= ?"
		args = append(args, tzName, from)
	}
	if to != "" {
		where += " AND tatitok_day(ts, ?) <= ?"
		args = append(args, tzName, to)
	}
	pred, fargs := f.eventsPredicate()
	where += pred
	args = append(args, fargs...)
	rows, err := s.db.QueryContext(ctx, `SELECT
			tatitok_weekday(ts, ?) AS wd, tatitok_hour(ts, ?) AS hr,
			COUNT(*) AS events,
			SUM(tokens_input + tokens_output + tokens_cache_write
				+ tokens_cache_read) AS tokens
		FROM usage_events
		WHERE 1=1`+where+`
		GROUP BY wd, hr
		ORDER BY wd, hr`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ActivityBucket
	for rows.Next() {
		var b ActivityBucket
		if err := rows.Scan(&b.Weekday, &b.Hour, &b.Events, &b.Tokens); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SessionSummary is one session's events inside a day range (the
// dashboard's sessions panel). Session and Project are raw ("" when the
// harness left them empty); FirstTS/LastTS are the first and last event
// in the range, UTC RFC3339. Only in-range events count, so a session
// that spans a range boundary shows its in-range part.
type SessionSummary struct {
	Session string `json:"session"`
	Harness string `json:"harness"`
	Project string `json:"project"`
	FirstTS string `json:"firstTs"`
	LastTS  string `json:"lastTs"`
	Events  int64  `json:"events"`
	TokenSums
	CostAPIEquivMicro int64 `json:"costAPIEquivMicro"`
}

// SessionsInRange returns the sessions with at least one event whose
// local day in tz lies in [from, to] (either bound may be ""), restricted
// by f, sorted by API-equivalent descending, then first event, and cut to
// limit rows. total is the session count before the cut. A session is
// the composite (machine, harness, session_id), as in Sessions. Its
// project is the one of its first in-range event. First and last are
// instants to the millisecond (tsMillis), not text order. The range uses
// tatitok_day, like Activity; the value sums with sumAPIEquiv, like Daily.
func (s *Store) SessionsInRange(ctx context.Context, tz *time.Location, from, to string, f Filters, limit int) ([]SessionSummary, int, error) {
	tzName := tz.String()
	var args []any
	where := ""
	if from != "" {
		where += " AND tatitok_day(ts, ?) >= ?"
		args = append(args, tzName, from)
	}
	if to != "" {
		where += " AND tatitok_day(ts, ?) <= ?"
		args = append(args, tzName, to)
	}
	pred, fargs := f.eventsPredicate()
	where += pred
	args = append(args, fargs...)
	// One group per (session, project): the earliest instant per project
	// picks the session's first project below, without SQLite's
	// bare-column rule (ambiguous when MIN and MAX share a query).
	rows, err := s.db.QueryContext(ctx, `SELECT machine,
			COALESCE(harness, '') AS h,
			COALESCE(session_id, '') AS sid,
			COALESCE(project, '') AS p,
			MIN(`+tsMillis+`), MAX(`+tsMillis+`), COUNT(*),
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			`+sumAPIEquiv+`
		FROM usage_events
		WHERE 1=1`+where+`
		GROUP BY machine, h, sid, p
		ORDER BY machine, h, sid, p`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	type acc struct {
		SessionSummary
		first, last time.Time
	}
	byKey := map[sessionKey]*acc{}
	var all []*acc
	for rows.Next() {
		var m, h, sid, p, lo, hi string
		var events, equiv int64
		var sums TokenSums
		if err := rows.Scan(&m, &h, &sid, &p, &lo, &hi, &events,
			&sums.Input, &sums.Output, &sums.CacheWrite, &sums.CacheRead, &equiv); err != nil {
			return nil, 0, err
		}
		loT, err := time.Parse(time.RFC3339Nano, lo)
		if err != nil {
			return nil, 0, err
		}
		hiT, err := time.Parse(time.RFC3339Nano, hi)
		if err != nil {
			return nil, 0, err
		}
		k := sessionKey{m, h, sid}
		a := byKey[k]
		if a == nil {
			a = &acc{SessionSummary: SessionSummary{Session: sid, Harness: h, Project: p}, first: loT, last: hiT}
			byKey[k] = a
			all = append(all, a)
		} else {
			if loT.Before(a.first) || (loT.Equal(a.first) && p < a.Project) {
				a.first, a.Project = loT, p
			}
			if hiT.After(a.last) {
				a.last = hiT
			}
		}
		a.Events += events
		a.add(sums)
		a.CostAPIEquivMicro += equiv
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Stable over the ORDER BY above, so a full tie keeps machine order.
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.CostAPIEquivMicro != b.CostAPIEquivMicro {
			return a.CostAPIEquivMicro > b.CostAPIEquivMicro
		}
		if !a.first.Equal(b.first) {
			return a.first.Before(b.first)
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Harness < b.Harness
	})
	total := len(all)
	if total > limit {
		all = all[:limit]
	}
	out := make([]SessionSummary, len(all))
	for i, a := range all {
		a.FirstTS = a.first.UTC().Format(time.RFC3339)
		a.LastTS = a.last.UTC().Format(time.RFC3339)
		out[i] = a.SessionSummary
	}
	return out, total, nil
}

type sessionKey struct{ machine, harness, sid string }

// sessionFirstProjects maps each (machine, harness, session) to the
// project of its earliest event. SQLite's bare-column-with-MIN semantics
// pin project to the MIN(ts) row.
func (s *Store) sessionFirstProjects(ctx context.Context, harness string) (map[sessionKey]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT machine,
			COALESCE(harness, ''),
			COALESCE(session_id, ''),
			COALESCE(project, ''), MIN(ts)
		FROM usage_events
		WHERE ?1 = '' OR harness = ?1
		GROUP BY 1, 2, 3`, harness)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[sessionKey]string{}
	for rows.Next() {
		var m, h, sid, project, minTS string
		if err := rows.Scan(&m, &h, &sid, &project, &minTS); err != nil {
			return nil, err
		}
		out[sessionKey{m, h, sid}] = project
	}
	return out, rows.Err()
}

// ProvenanceRow is one adapter@version slice of the DB: how many event
// rows and ingested source files that adapter version produced. A nil
// AdapterVersion marks rows ingested before provenance existed
// (pre-migration-4 databases).
type ProvenanceRow struct {
	Harness        string `json:"harness"`
	AdapterVersion *int64 `json:"adapter_version"`
	Events         int64  `json:"events"`
	SourceFiles    int64  `json:"source_files"`
}

// Provenance aggregates row counts by adapter@version across both the
// events and sources tables (`doctor --provenance`) — the queryable basis
// for future recompute decisions.
func (s *Store) Provenance(ctx context.Context) ([]ProvenanceRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT harness, adapter_version,
			SUM(events), SUM(files)
		FROM (
			SELECT COALESCE(harness,'') AS harness, adapter_version,
			       COUNT(*) AS events, 0 AS files
			FROM usage_events GROUP BY 1, 2
			UNION ALL
			SELECT harness, adapter_version, 0, COUNT(*)
			FROM sources GROUP BY 1, 2
		)
		GROUP BY harness, adapter_version
		ORDER BY harness, adapter_version`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ProvenanceRow
	for rows.Next() {
		var r ProvenanceRow
		if err := rows.Scan(&r.Harness, &r.AdapterVersion, &r.Events, &r.SourceFiles); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ModelInfo is one row of the model inventory (M4 Task 2,
// /api/v1/meta/models — dashboard legends): the distinct
// (provider, model, family, basis) combinations actually stored, with
// event counts. Basis 'unknown' covers NULL (pre-pricing rows).
type ModelInfo struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Family   string `json:"modelFamily"`
	Basis    string `json:"costBasis"`
	Events   int64  `json:"events"`
}

// FacetValue is one stored value of a filterable dimension, with its
// event count (M5 Task 3 — /api/v1/meta/facets, the dashboard rail).
type FacetValue struct {
	Value  string `json:"value"`
	Events int64  `json:"events"`
}

// Facets enumerates every filterable dimension's stored values with
// event counts. Keys match the filter parameter names; values are
// ordered for stable output.
func (s *Store) Facets(ctx context.Context) (map[string][]FacetValue, error) {
	dims := []struct{ name, expr string }{
		{"harness", "COALESCE(harness, '')"},
		{"provider", "provider"},
		{"model", "model"},
		{"project", "COALESCE(project, '')"},
		{"basis", "COALESCE(cost_basis, 'unknown')"},
	}
	out := make(map[string][]FacetValue, len(dims))
	for _, d := range dims {
		rows, err := s.db.QueryContext(ctx, `SELECT `+d.expr+` AS v, COUNT(*)
			FROM usage_events GROUP BY v ORDER BY v`)
		if err != nil {
			return nil, err
		}
		var vals []FacetValue
		for rows.Next() {
			var fv FacetValue
			if err := rows.Scan(&fv.Value, &fv.Events); err != nil {
				_ = rows.Close()
				return nil, err
			}
			vals = append(vals, fv)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		out[d.name] = vals
	}
	return out, nil
}

// ModelInventory lists every stored (provider, model, family, basis)
// combination, ordered for stable output.
func (s *Store) ModelInventory(ctx context.Context) ([]ModelInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, model, model_family,
			COALESCE(cost_basis, 'unknown'), COUNT(*)
		FROM usage_events
		GROUP BY provider, model, model_family, COALESCE(cost_basis, 'unknown')
		ORDER BY provider, model, model_family, 4`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ModelInfo
	for rows.Next() {
		var m ModelInfo
		if err := rows.Scan(&m.Provider, &m.Model, &m.Family, &m.Basis, &m.Events); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SourceState is the (mtime, size) recorded for one source file at its
// last ingest — the watcher's catch-up baseline (M4 Task 1): at serve
// start, files whose stat still matches are not re-read.
type SourceState struct {
	MTime time.Time
	Size  int64
}

// SourceStates returns the recorded state of every known source file,
// keyed by absolute path.
func (s *Store) SourceStates(ctx context.Context) (map[string]SourceState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, mtime, size FROM sources`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]SourceState{}
	for rows.Next() {
		var path, mtime string
		var size int64
		if err := rows.Scan(&path, &mtime, &size); err != nil {
			return nil, err
		}
		ts, err := time.Parse(time.RFC3339Nano, mtime)
		if err != nil {
			// A row this handle cannot parse just means "re-read that
			// file" — never abort the watcher over bookkeeping.
			continue
		}
		out[path] = SourceState{MTime: ts, Size: size}
	}
	return out, rows.Err()
}

// LastEventByHarness returns the newest stored event time per harness
// (one grouped query on idx_events_harness_ts; events without a harness
// are left out).
func (s *Store) LastEventByHarness(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT harness, MAX(ts) FROM usage_events
		WHERE harness IS NOT NULL GROUP BY harness`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]time.Time{}
	for rows.Next() {
		var harness, ts string
		if err := rows.Scan(&harness, &ts); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, err
		}
		out[harness] = t
	}
	return out, rows.Err()
}

// CountEvents returns the total number of stored events (test/diagnostic
// helper).
func (s *Store) CountEvents(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&n)
	return n, err
}

// CountEmptyModel returns how many stored events carry no model — legal
// (codex usage before the first turn_context) but a health signal
// (`doctor --provenance` surfaces it; pricing will need these visible).
func (s *Store) CountEmptyModel(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_events WHERE model = ''`).Scan(&n)
	return n, err
}

// ScanRawForContent reports how many stored raw/meta blobs contain the
// given literal — `doctor --scan-content` uses it to prove no prompt or
// response text ever reaches the DB.
func (s *Store) ScanRawForContent(ctx context.Context, literal string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events
		WHERE instr(COALESCE(raw,''), ?) > 0 OR instr(COALESCE(meta,''), ?) > 0`,
		literal, literal).Scan(&n)
	return n, err
}

// ForEachRaw streams every event's stored raw and meta blobs (either may
// be nil) to fn; fn returning an error stops the scan. Diagnostic use
// (doctor --scan-content).
func (s *Store) ForEachRaw(ctx context.Context, fn func(id string, raw, meta []byte) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, raw, meta FROM usage_events`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var raw, meta sql.NullString
		if err := rows.Scan(&id, &raw, &meta); err != nil {
			return err
		}
		var rb, mb []byte
		if raw.Valid {
			rb = []byte(raw.String)
		}
		if meta.Valid {
			mb = []byte(meta.String)
		}
		if err := fn(id, rb, mb); err != nil {
			return err
		}
	}
	return rows.Err()
}

// DB exposes the handle for diagnostic commands; not for general use.
func (s *Store) DB() *sql.DB { return s.db }
