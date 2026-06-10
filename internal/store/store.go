// Package store owns the SQLite database: open/migrate, idempotent event
// inserts, the ingested-sources bookkeeping table, and the M1 queries.
// Pure-Go driver (modernc.org/sqlite) — the binary must build CGO_ENABLED=0.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// FileTx is one source file's ingest transaction: events arrive in
// size-bounded batches (bounded memory — rows go to SQLite, not Go
// slices), and the file commits atomically together with its sources row.
// Rollback discards everything, so a file that fails mid-read leaves no
// partial events behind.
type FileTx struct {
	tx       *sql.Tx
	stmt     *sql.Stmt
	inserted int
	done     bool
}

// BeginFile opens the transaction for one source file's events.
func (s *Store) BeginFile(ctx context.Context) (*FileTx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO usage_events
		(id, ts, machine, source_kind, harness, provider, model, model_family,
		 project, session_id, request_id,
		 tokens_input, tokens_output, tokens_cache_write, tokens_cache_read,
		 tokens_reasoning, accuracy, meta, raw, adapter_version)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return &FileTx{tx: tx, stmt: stmt}, nil
}

// InsertEvents validates and inserts one batch into the open transaction.
// INSERT OR IGNORE on the primary key makes re-ingest idempotent. An error
// leaves the transaction unusable; the caller must Rollback.
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
		res, err := f.stmt.ExecContext(ctx,
			e.ID, e.TS.UTC().Format(time.RFC3339Nano), e.Machine, e.SourceKind,
			nullStr(e.Harness), e.Provider, e.Model, e.ModelFamily,
			nullStr(e.Project), nullStr(e.SessionID), nullStr(e.RequestID),
			e.TokensInput, e.TokensOutput, e.TokensCacheWrite, e.TokensCacheRead,
			e.TokensReasoning, string(e.Accuracy), meta, raw,
			nullVersion(adapterVersion))
		if err != nil {
			return fmt.Errorf("insert %s: %w", e.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		f.inserted += int(n)
	}
	return nil
}

// Commit writes the file's sources row and commits the transaction,
// returning the number of newly inserted (non-duplicate) events.
func (f *FileTx) Commit(ctx context.Context, src SourceInfo) (int, error) {
	f.done = true
	_ = f.stmt.Close()
	if err := recordSource(ctx, f.tx, src); err != nil {
		_ = f.tx.Rollback()
		return 0, err
	}
	if err := f.tx.Commit(); err != nil {
		return 0, err
	}
	return f.inserted, nil
}

// Rollback discards the file's events (skipped source / cancelled run).
func (f *FileTx) Rollback() error {
	if f.done {
		return nil
	}
	f.done = true
	_ = f.stmt.Close()
	return f.tx.Rollback()
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
// Returns the number of newly inserted (non-duplicate) events.
func (s *Store) InsertBatch(ctx context.Context, events []core.Event, src SourceInfo) (int, error) {
	f, err := s.BeginFile(ctx)
	if err != nil {
		return 0, err
	}
	if err := f.InsertEvents(ctx, events, src.AdapterVersion); err != nil {
		_ = f.Rollback()
		return 0, fmt.Errorf("%s: %w", src.Path, err)
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
