// Package store owns the SQLite database: open/migrate, idempotent event
// inserts, the ingested-sources bookkeeping table, and the M1 queries.
// Pure-Go driver (modernc.org/sqlite) — the binary must build CGO_ENABLED=0.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// Store wraps the SQLite handle after migration.
type Store struct {
	db *sql.DB
}

// migrations run in order inside one transaction each; schema_version
// records the last applied index + 1. Append-only — never edit an entry
// that has shipped.
var migrations = []string{
	`CREATE TABLE usage_events (
		id                 TEXT PRIMARY KEY,
		ts                 TEXT NOT NULL,            -- RFC3339 UTC
		machine            TEXT NOT NULL,
		source_kind        TEXT NOT NULL,
		harness            TEXT,
		provider           TEXT NOT NULL,
		model              TEXT NOT NULL,
		model_family       TEXT NOT NULL,
		project            TEXT,
		session_id         TEXT,
		request_id         TEXT,
		tokens_input       INTEGER NOT NULL,
		tokens_output      INTEGER NOT NULL,
		tokens_cache_write INTEGER NOT NULL,
		tokens_cache_read  INTEGER NOT NULL,
		tokens_reasoning   INTEGER,
		accuracy           TEXT NOT NULL CHECK (accuracy IN ('exact','derived','estimated')),
		meta               TEXT,                     -- JSON
		raw                TEXT                      -- sanitized JSON
	);
	CREATE INDEX idx_events_ts          ON usage_events (ts);
	CREATE INDEX idx_events_harness_ts  ON usage_events (harness, ts);
	CREATE INDEX idx_events_model_ts    ON usage_events (model, ts);
	CREATE INDEX idx_events_project_ts  ON usage_events (project, ts);

	CREATE TABLE sources (
		path        TEXT PRIMARY KEY,                -- absolute source file path
		harness     TEXT NOT NULL,
		mtime       TEXT NOT NULL,                   -- RFC3339 UTC
		size        INTEGER NOT NULL,
		line_count  INTEGER NOT NULL,
		ingested_at TEXT NOT NULL,                   -- RFC3339 UTC
		parse_errors INTEGER NOT NULL DEFAULT 0
	);`,
	// read_error: why this source was skipped (NULL = read fully); a
	// later successful ingest of the same path clears it via the upsert.
	`ALTER TABLE sources ADD COLUMN read_error TEXT;`,
	// incomplete_tail: the file's final line was unterminated and did not
	// parse (write in progress, not a parse error); cleared by the next
	// backfill of the same path via the upsert.
	`ALTER TABLE sources ADD COLUMN incomplete_tail INTEGER NOT NULL DEFAULT 0;`,
	// AdapterVersion provenance (M2 Task 0): which adapter format-handling
	// version produced each stored event and each ingested source file.
	// The adapter NAME is the existing harness column on both tables. NULL
	// on rows ingested before this migration.
	`ALTER TABLE usage_events ADD COLUMN adapter_version INTEGER;
	ALTER TABLE sources ADD COLUMN adapter_version INTEGER;`,
}

// Open opens (creating if needed) the database at path, enables WAL, and
// applies pending migrations.
func Open(path string) (*Store, error) {
	// _pragma values apply per connection; busy_timeout guards WAL writers.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func migrate(db *sql.DB) error {
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	var version int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`DELETE FROM schema_version`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

// SourceInfo describes one ingested file for the sources table.
type SourceInfo struct {
	Path        string
	Harness     string
	MTime       time.Time
	Size        int64
	LineCount   int
	ParseErrors int
	// ReadError non-empty marks a skipped source (could not be read);
	// stored so `sources` always reflects what the DB is missing.
	ReadError string
	// IncompleteTail: unterminated, unparseable final line (write in
	// progress); cleared by the next ingest of the same path.
	IncompleteTail bool
	// AdapterVersion is the format-handling version of the adapter that
	// parsed this file (provenance); stamped onto every event row inserted
	// through the file's transaction and onto the sources row.
	AdapterVersion int
}

// InsertStats reports one file transaction's effect on usage_events.
type InsertStats struct {
	Inserted int // new rows (duplicates collapse on the deterministic ID)
	Replaced int // existing rows updated because their source row changed
}

// FileTx is one source file's ingest transaction: events arrive in
// size-bounded batches (bounded memory — rows go to SQLite, not Go
// slices), and the file commits atomically together with its sources row.
// Rollback discards everything, so a file that fails mid-read leaves no
// partial events behind.
type FileTx struct {
	tx    *sql.Tx
	ins   *sql.Stmt
	upd   *sql.Stmt
	stats InsertStats
	done  bool
}

// BeginFile opens the transaction for one source file's events.
func (s *Store) BeginFile(ctx context.Context) (*FileTx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO usage_events
		(id, ts, machine, source_kind, harness, provider, model, model_family,
		 project, session_id, request_id,
		 tokens_input, tokens_output, tokens_cache_write, tokens_cache_read,
		 tokens_reasoning, accuracy, meta, raw, adapter_version)
		VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17,?18,?19,?20)`)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	// Replacement path: some stores mutate rows in place (OpenCode updates
	// a message row while the turn is in flight), so an event ID can come
	// back with a different payload. The DB mirrors the latest source read:
	// on conflict, update iff the payload differs (NULL-safe IS NOT).
	// adapter_version is provenance, not payload — it is stamped when a
	// replacement happens but never triggers one by itself. Replacements
	// are counted and logged, never silent (PRD AS-4).
	upd, err := tx.PrepareContext(ctx, `UPDATE usage_events SET
		ts=?2, machine=?3, source_kind=?4, harness=?5, provider=?6, model=?7,
		model_family=?8, project=?9, session_id=?10, request_id=?11,
		tokens_input=?12, tokens_output=?13, tokens_cache_write=?14,
		tokens_cache_read=?15, tokens_reasoning=?16, accuracy=?17, meta=?18,
		raw=?19, adapter_version=?20
		WHERE id=?1 AND (
			ts IS NOT ?2 OR machine IS NOT ?3 OR source_kind IS NOT ?4 OR
			harness IS NOT ?5 OR provider IS NOT ?6 OR model IS NOT ?7 OR
			model_family IS NOT ?8 OR project IS NOT ?9 OR
			session_id IS NOT ?10 OR request_id IS NOT ?11 OR
			tokens_input IS NOT ?12 OR tokens_output IS NOT ?13 OR
			tokens_cache_write IS NOT ?14 OR tokens_cache_read IS NOT ?15 OR
			tokens_reasoning IS NOT ?16 OR accuracy IS NOT ?17 OR
			meta IS NOT ?18 OR raw IS NOT ?19
		)`)
	if err != nil {
		_ = ins.Close()
		_ = tx.Rollback()
		return nil, err
	}
	return &FileTx{tx: tx, ins: ins, upd: upd}, nil
}

// InsertEvents validates and inserts one batch into the open transaction.
// The deterministic ID is the primary key: a duplicate with an identical
// payload is a no-op (idempotent re-ingest); a duplicate whose payload
// differs replaces the stored row to mirror the source (see BeginFile).
// An error leaves the transaction unusable; the caller must Rollback.
func (f *FileTx) InsertEvents(ctx context.Context, events []core.Event, adapterVersion int) error {
	for i := range events {
		e := &events[i]
		if err := e.Validate(); err != nil {
			return err
		}
		var meta any
		if e.Meta != nil {
			b, err := json.Marshal(e.Meta)
			if err != nil {
				return fmt.Errorf("marshal meta for %s: %w", e.ID, err)
			}
			meta = string(b)
		}
		var raw any
		if len(e.Raw) > 0 {
			raw = string(e.Raw)
		}
		args := []any{
			e.ID, e.TS.UTC().Format(time.RFC3339Nano), e.Machine, e.SourceKind,
			nullStr(e.Harness), e.Provider, e.Model, e.ModelFamily,
			nullStr(e.Project), nullStr(e.SessionID), nullStr(e.RequestID),
			e.TokensInput, e.TokensOutput, e.TokensCacheWrite, e.TokensCacheRead,
			e.TokensReasoning, string(e.Accuracy), meta, raw,
			nullVersion(adapterVersion),
		}
		res, err := f.ins.ExecContext(ctx, args...)
		if err != nil {
			return fmt.Errorf("insert %s: %w", e.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			f.stats.Inserted++
			continue
		}
		res, err = f.upd.ExecContext(ctx, args...)
		if err != nil {
			return fmt.Errorf("replace %s: %w", e.ID, err)
		}
		if n, err = res.RowsAffected(); err != nil {
			return err
		}
		if n == 1 {
			f.stats.Replaced++
			slog.Info("replaced stored event: source row changed since last ingest",
				"id", e.ID, "harness", e.Harness, "ts", e.TS.UTC())
		}
	}
	return nil
}

// Commit writes the file's sources row and commits the transaction,
// returning the insert/replace counts.
func (f *FileTx) Commit(ctx context.Context, src SourceInfo) (InsertStats, error) {
	f.done = true
	f.closeStmts()
	if err := recordSource(ctx, f.tx, src); err != nil {
		_ = f.tx.Rollback()
		return InsertStats{}, err
	}
	if err := f.tx.Commit(); err != nil {
		return InsertStats{}, err
	}
	return f.stats, nil
}

// Rollback discards the file's events (skipped source / cancelled run).
func (f *FileTx) Rollback() error {
	if f.done {
		return nil
	}
	f.done = true
	f.closeStmts()
	return f.tx.Rollback()
}

func (f *FileTx) closeStmts() {
	_ = f.ins.Close()
	_ = f.upd.Close()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func recordSource(ctx context.Context, db execer, src SourceInfo) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO sources
		(path, harness, mtime, size, line_count, ingested_at, parse_errors,
		 read_error, incomplete_tail, adapter_version)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
			harness=excluded.harness, mtime=excluded.mtime, size=excluded.size,
			line_count=excluded.line_count, ingested_at=excluded.ingested_at,
			parse_errors=excluded.parse_errors, read_error=excluded.read_error,
			incomplete_tail=excluded.incomplete_tail,
			adapter_version=excluded.adapter_version`,
		src.Path, src.Harness, src.MTime.UTC().Format(time.RFC3339Nano),
		src.Size, src.LineCount, time.Now().UTC().Format(time.RFC3339Nano),
		src.ParseErrors, nullStr(src.ReadError), src.IncompleteTail,
		nullVersion(src.AdapterVersion)); err != nil {
		return fmt.Errorf("record source %s: %w", src.Path, err)
	}
	return nil
}

// RecordSource upserts a sources row outside any file transaction — used
// for skipped sources (read errors) and zero-event files.
func (s *Store) RecordSource(ctx context.Context, src SourceInfo) error {
	return recordSource(ctx, s.db, src)
}

// InsertBatch writes one file's events and its sources row in a single
// transaction (convenience wrapper over BeginFile/InsertEvents/Commit).
func (s *Store) InsertBatch(ctx context.Context, events []core.Event, src SourceInfo) (InsertStats, error) {
	f, err := s.BeginFile(ctx)
	if err != nil {
		return InsertStats{}, err
	}
	if err := f.InsertEvents(ctx, events, src.AdapterVersion); err != nil {
		_ = f.Rollback()
		return InsertStats{}, fmt.Errorf("%s: %w", src.Path, err)
	}
	return f.Commit(ctx, src)
}

func nullVersion(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
