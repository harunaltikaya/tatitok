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

// TokenSums are the four parity-relevant token counters (cost is excluded
// in M1).
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

// ModelSums is a per-model breakdown within a day or session.
type ModelSums struct {
	Model string `json:"modelName"`
	TokenSums
}

// HarnessSums is a per-harness breakdown within a day (M2 Task 5: one DB
// holds claude-code, codex and opencode side by side).
type HarnessSums struct {
	Harness string `json:"harness"`
	TokenSums
}

// DailyRow is one local-time day. Date is YYYY-MM-DD in the query
// timezone — ccusage buckets days in local time, so parity comparisons
// must use the timezone recorded in expected/META.json.
type DailyRow struct {
	Date string `json:"date"`
	TokenSums
	ModelsUsed        []string      `json:"modelsUsed"`
	ModelBreakdowns   []ModelSums   `json:"modelBreakdowns"`
	HarnessBreakdowns []HarnessSums `json:"harnessBreakdowns"`
}

// SessionRow is one harness session. Identity is (harness, session_id):
// session ids are native per harness and CAN collide across harnesses
// (and the empty id is common to several), so session_id alone never
// keys a row. Full composite identity (machine included) lands with the
// M3 schema; this is the query-level rule.
type SessionRow struct {
	Harness   string `json:"harness"`
	SessionID string `json:"sessionId"`
	Project   string `json:"projectPath,omitempty"`
	TokenSums
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
			SUM(tokens_cache_write), SUM(tokens_cache_read)
		FROM usage_events
		WHERE ?2 = '' OR harness = ?2
		GROUP BY day, h, model
		ORDER BY day, h, model`, tz.String(), harness)
	if err != nil {
		return nil, err
	}
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
		var model string
		if err := rows.Scan(&day, &h, &model,
			&sums.Input, &sums.Output, &sums.CacheWrite, &sums.CacheRead); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Date != day {
			flushModels()
			out = append(out, DailyRow{Date: day})
		}
		d := &out[len(out)-1]
		d.add(sums)
		// rows arrive ordered by harness within the day: extend or append
		if n := len(d.HarnessBreakdowns); n > 0 && d.HarnessBreakdowns[n-1].Harness == h {
			d.HarnessBreakdowns[n-1].add(sums)
		} else {
			d.HarnessBreakdowns = append(d.HarnessBreakdowns,
				HarnessSums{Harness: h, TokenSums: sums})
		}
		// models merge across harnesses (a model may appear in several)
		m := models[model]
		if m == nil {
			m = &ModelSums{Model: model}
			models[model] = m
		}
		m.add(sums)
	}
	flushModels()
	return out, rows.Err()
}

// Sessions returns per-session token sums, most recent activity last.
// Rows key on (harness, session_id) — see SessionRow. A non-empty
// harness restricts the report (`stats --harness`). Aggregation runs in
// SQL: a GROUP BY (harness, session, model) pass for sums, models and
// last-activity day (MAX over tatitok_day is sound — day strings are
// fixed-width, so lexicographic max is chronological max), plus one
// GROUP BY (harness, session) pass whose bare project column SQLite
// takes from the MIN(ts) row (the session's first event, matching the
// legacy scan order).
func (s *Store) Sessions(ctx context.Context, tz *time.Location, harness string) ([]SessionRow, error) {
	first, err := s.sessionFirstProjects(ctx, harness)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(harness, '') AS h,
			COALESCE(session_id, '') AS sid,
			model, MAX(tatitok_day(ts, ?1)),
			SUM(tokens_input), SUM(tokens_output),
			SUM(tokens_cache_write), SUM(tokens_cache_read)
		FROM usage_events
		WHERE ?2 = '' OR harness = ?2
		GROUP BY h, sid, model
		ORDER BY h, sid, model`, tz.String(), harness)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SessionRow
	for rows.Next() {
		var h, sid, model, last string
		var sums TokenSums
		if err := rows.Scan(&h, &sid, &model, &last,
			&sums.Input, &sums.Output, &sums.CacheWrite, &sums.CacheRead); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Harness != h || out[n-1].SessionID != sid {
			out = append(out, SessionRow{Harness: h, SessionID: sid,
				Project: first[sessionKey{h, sid}]})
		}
		sr := &out[len(out)-1]
		sr.add(sums)
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
		return out[i].Harness < out[j].Harness
	})
	return out, nil
}

type sessionKey struct{ harness, sid string }

// sessionFirstProjects maps each (harness, session) to the project of
// its earliest event. SQLite's bare-column-with-MIN semantics pin
// project to the MIN(ts) row.
func (s *Store) sessionFirstProjects(ctx context.Context, harness string) (map[sessionKey]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(harness, ''),
			COALESCE(session_id, ''),
			COALESCE(project, ''), MIN(ts)
		FROM usage_events
		WHERE ?1 = '' OR harness = ?1
		GROUP BY 1, 2`, harness)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[sessionKey]string{}
	for rows.Next() {
		var h, sid, project, minTS string
		if err := rows.Scan(&h, &sid, &project, &minTS); err != nil {
			return nil, err
		}
		out[sessionKey{h, sid}] = project
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

// CountEvents returns the total number of stored events (test/diagnostic
// helper).
func (s *Store) CountEvents(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&n)
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
