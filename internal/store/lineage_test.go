package store

// Source-lineage tests (M3 Task 0, migration 5). Synthetic rows are fine
// here: this tests the store's SQL behavior (pure infrastructure), not
// adapter parsing — no log lines are fabricated.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

// buildV4DB creates a database frozen at schema version 4 (pre-lineage)
// and fills it with rows shaped like a real M2 database: sources and
// events that carry NO machine/source_id columns yet.
func buildV4DB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := migrateTo(db, 4); err != nil {
		t.Fatal(err)
	}

	srcs := []struct{ path, harness string }{
		// claude-code embeds the session id in the filename
		{"/logs/projects/p1/sess-aaa.jsonl", "claude-code"},
		{"/logs/projects/p2/sess-bbb.jsonl", "claude-code"},
		// codex rollout original + backup copy share one session id —
		// the path match is ambiguous on purpose
		{"/cx/sessions/2026/06/01/rollout-1-sess-ccc.jsonl", "codex"},
		{"/cx/sessions/2026/06/02/rollout-2-sess-ccc.jsonl", "codex"},
		// opencode is a single db file; session ids never appear in it
		{"/data/opencode/opencode.db", "opencode"},
	}
	for _, s := range srcs {
		if _, err := db.Exec(`INSERT INTO sources
			(path, harness, mtime, size, line_count, ingested_at, parse_errors)
			VALUES (?,?,?,1,1,?,0)`,
			s.path, s.harness,
			"2026-06-01T00:00:00Z", "2026-06-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}

	events := []struct{ id, harness, session, machine string }{
		{"e1", "claude-code", "sess-aaa", "gx10"}, // unambiguous → p1
		{"e2", "claude-code", "sess-bbb", "gx10"}, // unambiguous → p2
		{"e3", "claude-code", "sess-zzz", "gx10"}, // matches nothing → NULL
		{"e4", "codex", "sess-ccc", "gx10"},       // two paths match → NULL
		{"e5", "codex", "sess-ccc", "mac"},        // second machine → codex machine ambiguous
		{"e6", "opencode", "oc-1", "gx10"},        // single-source harness → opencode.db
	}
	for _, e := range events {
		if _, err := db.Exec(`INSERT INTO usage_events
			(id, ts, machine, source_kind, harness, provider, model,
			 model_family, session_id, tokens_input, tokens_output,
			 tokens_cache_write, tokens_cache_read, accuracy)
			VALUES (?, '2026-06-01T00:00:00Z', ?, 'harness_log', ?, 'p',
			        'm', 'm', ?, 1, 1, 0, 0, 'exact')`,
			e.id, e.machine, e.harness, e.session); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestMigration5LineageBackfill(t *testing.T) {
	path := buildV4DB(t)
	s, err := Open(path) // applies migration 5 over the v4 data
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	// Every sources row gets its deterministic source_id.
	rows, err := s.db.QueryContext(ctx, `SELECT path, harness, source_id,
		COALESCE(machine, '') FROM sources`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	wantMachine := map[string]string{
		"claude-code": "gx10", // single distinct machine
		"codex":       "",     // gx10 + mac → ambiguous, stays NULL
		"opencode":    "gx10",
	}
	srcCount := 0
	for rows.Next() {
		srcCount++
		var p, h, sid, machine string
		if err := rows.Scan(&p, &h, &sid, &machine); err != nil {
			t.Fatal(err)
		}
		if want := core.SourceID(h, p); sid != want {
			t.Errorf("%s: source_id %q, want %q", p, sid, want)
		}
		if machine != wantMachine[h] {
			t.Errorf("%s: machine %q, want %q", p, machine, wantMachine[h])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if srcCount != 5 {
		t.Fatalf("sources rows: %d, want 5", srcCount)
	}

	// Event links: unambiguous path matches and the single-source rule
	// resolve; ambiguous (codex copies) and unmatched stay NULL + counted.
	wantLink := map[string]string{
		"e1": core.SourceID("claude-code", "/logs/projects/p1/sess-aaa.jsonl"),
		"e2": core.SourceID("claude-code", "/logs/projects/p2/sess-bbb.jsonl"),
		"e3": "",
		"e4": "",
		"e5": "",
		"e6": core.SourceID("opencode", "/data/opencode/opencode.db"),
	}
	for id, want := range wantLink {
		var got sql.NullString
		if err := s.db.QueryRowContext(ctx,
			`SELECT source_id FROM usage_events WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got.String != want {
			t.Errorf("%s: source_id %q, want %q", id, got.String, want)
		}
	}

	// The new FK must hold over the backfilled data.
	fk, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fk.Close() }()
	if fk.Next() {
		t.Fatal("foreign_key_check reported violations after migration 5")
	}

	// Reopening must be a no-op (migration idempotence across reopen).
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after migration 5: %v", err)
	}
	_ = s2.Close()
}

// Ingest stamps lineage going forward: events carry the source_id of the
// file transaction they arrived in, and the sources row records machine
// and the same source_id.
func TestIngestStampsLineage(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	src := SourceInfo{Path: "/tmp/test.jsonl", Harness: "claude-code",
		Machine: "gx10", MTime: time.Now(), Size: 1, LineCount: 1,
		AdapterVersion: 2}
	if _, err := s.InsertBatch(ctx,
		[]core.Event{event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 1})},
		src); err != nil {
		t.Fatal(err)
	}

	want := core.SourceID("claude-code", "/tmp/test.jsonl")
	var eventSID, srcSID, machine string
	if err := s.db.QueryRowContext(ctx,
		`SELECT source_id FROM usage_events`).Scan(&eventSID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT source_id, machine FROM sources`).Scan(&srcSID, &machine); err != nil {
		t.Fatal(err)
	}
	if eventSID != want || srcSID != want {
		t.Errorf("source_id event=%q sources=%q, want %q", eventSID, srcSID, want)
	}
	if machine != "gx10" {
		t.Errorf("sources.machine = %q, want gx10", machine)
	}
}
