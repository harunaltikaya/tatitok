package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sort"
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

// Daily returns per-day token sums bucketed in tz, oldest day first,
// with per-model and per-harness breakdowns. A non-empty harness
// restricts the report to that harness (`stats --harness`). Aggregation
// runs in SQL: one GROUP BY (day, harness, model) pass with the timezone
// rule applied via tatitok_day; Go only assembles the per-day rows from
// the (few) group rows. tz must be resolvable by its name (IANA, "UTC"
// or "Local").
func (s *Store) Daily(ctx context.Context, tz *time.Location, harness string) ([]DailyRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tatitok_day(ts, ?1) AS day,
			COALESCE(harness, '') AS h, model,
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(COALESCE(tokens_reasoning, 0)),
			SUM(COALESCE(cost_usd_micro, 0)),
			SUM(COALESCE(cost_api_equiv_micro, 0)),
			SUM(cost_usd_micro IS NULL)
		FROM usage_events
		WHERE ?2 = '' OR harness = ?2
		GROUP BY day, h, model
		ORDER BY day, h, model`, tz.String(), harness)
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
// --by harness|provider|model|project` (M3 Task 4).
type DailyByRow struct {
	Date string `json:"date"`
	Key  string `json:"key"`
	TokenSums
	CostSums
}

// dailyByDims maps the --by dimension name to its NULL-safe column
// expression. model is the RAW model string (consistent with every
// other stats surface); family-level slicing is deferred — the rollup
// table carries model_family for it.
var dailyByDims = map[string]string{
	"harness":  "COALESCE(harness, '')",
	"provider": "provider",
	"model":    "model",
	"project":  "COALESCE(project, '')",
}

// DailyBy returns per-day sums broken down by one dimension, oldest day
// first, keys ordered within the day. Totals across a day's keys equal
// the Daily row for that day (tested).
func (s *Store) DailyBy(ctx context.Context, tz *time.Location, dim, harness string) ([]DailyByRow, error) {
	expr, ok := dailyByDims[dim]
	if !ok {
		return nil, fmt.Errorf("unknown --by dimension %q (supported: harness, provider, model, project)", dim)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tatitok_day(ts, ?1) AS day,
			`+expr+` AS key,
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read),
			SUM(COALESCE(tokens_reasoning, 0)),
			SUM(COALESCE(cost_usd_micro, 0)),
			SUM(COALESCE(cost_api_equiv_micro, 0)),
			SUM(cost_usd_micro IS NULL)
		FROM usage_events
		WHERE ?2 = '' OR harness = ?2
		GROUP BY day, key
		ORDER BY day, key`, tz.String(), harness)
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
			&r.CostAPIEquivMicro, &r.UnpricedEvents); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
