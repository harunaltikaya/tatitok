package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

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

// DailyRow is one local-time day. Date is YYYY-MM-DD in the query
// timezone — ccusage buckets days in local time, so parity comparisons
// must use the timezone recorded in expected/META.json.
type DailyRow struct {
	Date string `json:"date"`
	TokenSums
	ModelsUsed      []string    `json:"modelsUsed"`
	ModelBreakdowns []ModelSums `json:"modelBreakdowns"`
}

// SessionRow is one harness session.
type SessionRow struct {
	SessionID string `json:"sessionId"`
	Project   string `json:"projectPath,omitempty"`
	TokenSums
	ModelsUsed   []string `json:"modelsUsed"`
	LastActivity string   `json:"lastActivity"` // YYYY-MM-DD in query tz
}

// scanRow is the per-event projection both aggregations consume. M1 scans
// events instead of maintaining rollups (fixture scale; full history must
// still answer in < 5 s, which a single indexed scan does).
type scanRow struct {
	ts      time.Time
	model   string
	session string
	project string
	sums    TokenSums
}

func (s *Store) scanEvents(ctx context.Context) ([]scanRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, model,
		COALESCE(session_id,''), COALESCE(project,''),
		tokens_input, tokens_output, tokens_cache_write, tokens_cache_read
		FROM usage_events ORDER BY ts`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []scanRow
	for rows.Next() {
		var r scanRow
		var ts string
		if err := rows.Scan(&ts, &r.model, &r.session, &r.project,
			&r.sums.Input, &r.sums.Output, &r.sums.CacheWrite, &r.sums.CacheRead); err != nil {
			return nil, err
		}
		r.ts, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Daily returns per-day token sums bucketed in tz, oldest day first.
func (s *Store) Daily(ctx context.Context, tz *time.Location) ([]DailyRow, error) {
	events, err := s.scanEvents(ctx)
	if err != nil {
		return nil, err
	}
	days := map[string]*DailyRow{}
	models := map[string]map[string]*ModelSums{}
	for _, e := range events {
		date := e.ts.In(tz).Format("2006-01-02")
		d := days[date]
		if d == nil {
			d = &DailyRow{Date: date}
			days[date] = d
			models[date] = map[string]*ModelSums{}
		}
		d.add(e.sums)
		m := models[date][e.model]
		if m == nil {
			m = &ModelSums{Model: e.model}
			models[date][e.model] = m
		}
		m.add(e.sums)
	}
	out := make([]DailyRow, 0, len(days))
	for date, d := range days {
		for _, m := range models[date] {
			d.ModelBreakdowns = append(d.ModelBreakdowns, *m)
			d.ModelsUsed = append(d.ModelsUsed, m.Model)
		}
		sort.Slice(d.ModelBreakdowns, func(i, j int) bool {
			return d.ModelBreakdowns[i].Model < d.ModelBreakdowns[j].Model
		})
		sort.Strings(d.ModelsUsed)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

// Sessions returns per-session token sums, most recent activity last.
func (s *Store) Sessions(ctx context.Context, tz *time.Location) ([]SessionRow, error) {
	events, err := s.scanEvents(ctx)
	if err != nil {
		return nil, err
	}
	sessions := map[string]*SessionRow{}
	models := map[string]map[string]bool{}
	last := map[string]time.Time{}
	for _, e := range events {
		sr := sessions[e.session]
		if sr == nil {
			sr = &SessionRow{SessionID: e.session, Project: e.project}
			sessions[e.session] = sr
			models[e.session] = map[string]bool{}
		}
		sr.add(e.sums)
		models[e.session][e.model] = true
		if e.ts.After(last[e.session]) {
			last[e.session] = e.ts
			sr.LastActivity = e.ts.In(tz).Format("2006-01-02")
		}
	}
	out := make([]SessionRow, 0, len(sessions))
	for id, sr := range sessions {
		for m := range models[id] {
			sr.ModelsUsed = append(sr.ModelsUsed, m)
		}
		sort.Strings(sr.ModelsUsed)
		out = append(out, *sr)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivity != out[j].LastActivity {
			return out[i].LastActivity < out[j].LastActivity
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
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
