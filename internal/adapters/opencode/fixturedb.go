package opencode

// Fixture tooling: the committed opencode fixtures are TEXT (sanitized
// per-session row JSONL + the message-table DDL) — no binary lives in
// git. Tests and the parity gate reconstruct the database from that text
// with this helper; scripts/harvest_fixtures.py builds the identical
// database the same way when it captures the ccusage expectations, so
// the committed expectations match the reconstruction by construction.

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuildFixtureDB reconstructs <dbPath> from a fixture dir holding
// schema.sql and messages/*.jsonl. Returns the number of rows inserted.
func BuildFixtureDB(fixtureDir, dbPath string) (int, error) {
	ddl, err := os.ReadFile(filepath.Join(fixtureDir, "schema.sql"))
	if err != nil {
		return 0, err
	}
	files, err := filepath.Glob(filepath.Join(fixtureDir, "messages", "*.jsonl"))
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("no fixture message files under %s", fixtureDir)
	}
	sort.Strings(files)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return 0, err
	}
	// This database is an ephemeral test/parity fixture — durability is
	// irrelevant, so disable fsync (synchronous=off) and keep the rollback
	// journal in memory. Without this, modernc.org/sqlite fsyncs on every
	// commit; together with the single-transaction batching below it turns
	// a per-row fsync storm (pathologically slow on CI's slow disks — a
	// 10-minute hub-suite timeout on the arm runner) into a near-free build.
	// Pragmas live on the DSN so they apply to every pooled connection.
	db, err := sql.Open("sqlite",
		"file:"+dbPath+"?_pragma=synchronous(off)&_pragma=journal_mode(memory)")
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()

	// schema.sql carries leading "-- " comment lines plus the DDL.
	var stmts []string
	for _, line := range strings.Split(string(ddl), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			stmts = append(stmts, line)
		}
	}
	if _, err := db.Exec(strings.Join(stmts, "\n")); err != nil {
		return 0, fmt.Errorf("apply schema: %w", err)
	}

	// One transaction for all rows: a single commit instead of one
	// auto-commit per INSERT (each its own fsync, were fsync enabled). Row
	// content and order are unchanged, so the reconstructed message table —
	// and the parity expectations computed from it — stay identical.
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT INTO message VALUES (?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}

	inserted := 0
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			_ = tx.Rollback()
			return inserted, err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
		for sc.Scan() {
			var r row
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				_ = fh.Close()
				_ = tx.Rollback()
				return inserted, fmt.Errorf("%s: %w", f, err)
			}
			if _, err := stmt.Exec(
				r.ID, r.SessionID, r.TimeCreated, r.TimeUpdated,
				string(compactJSON(r.Data))); err != nil {
				_ = fh.Close()
				_ = tx.Rollback()
				return inserted, fmt.Errorf("%s: %w", f, err)
			}
			inserted++
		}
		err = sc.Err()
		_ = fh.Close()
		if err != nil {
			_ = tx.Rollback()
			return inserted, err
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return inserted, err
	}
	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// compactJSON normalizes the data blob to the compact form the harvest
// inserted (the JSONL field is already compact; this keeps the guarantee
// even if a fixture file was ever reformatted). Key order is preserved.
func compactJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}
