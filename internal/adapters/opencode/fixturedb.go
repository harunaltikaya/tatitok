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
	db, err := sql.Open("sqlite", "file:"+dbPath)
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

	inserted := 0
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return inserted, err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
		for sc.Scan() {
			var r row
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				_ = fh.Close()
				return inserted, fmt.Errorf("%s: %w", f, err)
			}
			if _, err := db.Exec(
				`INSERT INTO message VALUES (?,?,?,?,?)`,
				r.ID, r.SessionID, r.TimeCreated, r.TimeUpdated,
				string(compactJSON(r.Data))); err != nil {
				_ = fh.Close()
				return inserted, fmt.Errorf("%s: %w", f, err)
			}
			inserted++
		}
		err = sc.Err()
		_ = fh.Close()
		if err != nil {
			return inserted, err
		}
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
