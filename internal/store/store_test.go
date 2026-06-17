package store

// Synthetic core.Event values are fine here: this tests the store's SQL
// behavior (pure infrastructure), not adapter parsing — no log lines are
// fabricated.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	// The timezone matrix below must never skip for missing host tzdata
	// (the shipped binary embeds the zone database the same way).
	_ "time/tzdata"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func eventH(harness, msgID, reqID, model, session string, ts time.Time, sums TokenSums) core.Event {
	return core.Event{
		ID:          core.EventID(harness, msgID, reqID),
		TS:          ts.UTC(),
		Machine:     "test",
		SourceKind:  core.SourceKindHarnessLog,
		Harness:     harness,
		Provider:    "anthropic",
		Model:       model,
		ModelFamily: model,
		SessionID:   session,
		RequestID:   reqID,
		TokensInput: sums.Input, TokensOutput: sums.Output,
		TokensCacheWrite: sums.CacheWrite, TokensCacheRead: sums.CacheRead,
		Accuracy: core.AccuracyExact,
	}
}

func event(msgID, reqID, model, session string, ts time.Time, sums TokenSums) core.Event {
	return eventH("claude-code", msgID, reqID, model, session, ts, sums)
}

func testSource(n int) SourceInfo {
	return SourceInfo{Path: "/tmp/test.jsonl", Harness: "claude-code",
		MTime: time.Now(), Size: 1, LineCount: n}
}

func TestInsertBatchIdempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	batch := []core.Event{
		event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10, Output: 20, CacheWrite: 30, CacheRead: 40}),
		event("m2", "r2", "model-a", "s1", ts.Add(time.Minute), TokenSums{Input: 1, Output: 2}),
	}

	stats, err := s.InsertBatch(ctx, batch, testSource(2))
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if stats.Inserted != 2 || stats.Replaced != 0 {
		t.Fatalf("first insert: got %+v, want 2 inserted, 0 replaced", stats)
	}

	stats, err = s.InsertBatch(ctx, batch, testSource(2))
	if err != nil {
		t.Fatalf("re-insert: %v", err)
	}
	if stats.Inserted != 0 || stats.Replaced != 0 {
		t.Fatalf("re-insert: got %+v, want 0 inserted, 0 replaced", stats)
	}

	total, err := s.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("count after re-ingest: got %d, want 2", total)
	}
}

// F1 (M6 Codex): a replacement that moves an event across a UTC day
// boundary must report BOTH the old and new day in TouchedDays — the
// rollup triggers subtract from the old day's bucket and add to the new,
// so the live refresh must be able to invalidate either visible day.
func TestReplacementCrossingDayReportsBothDays(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	d10 := time.Date(2026, 6, 10, 23, 30, 0, 0, time.UTC)
	first := event("m1", "r1", "model-a", "s1", d10, TokenSums{Input: 10, Output: 1})
	if st, err := s.InsertBatch(ctx, []core.Event{first}, testSource(1)); err != nil {
		t.Fatal(err)
	} else if len(st.TouchedDays) != 1 || st.TouchedDays[0] != "2026-06-10" {
		t.Fatalf("first ingest touched %v, want [2026-06-10]", st.TouchedDays)
	}
	// Same id (msg/req unchanged), ts moved to the next UTC day → replace.
	moved := event("m1", "r1", "model-a", "s1", d10.Add(time.Hour), TokenSums{Input: 10, Output: 1})
	st, err := s.InsertBatch(ctx, []core.Event{moved}, testSource(1))
	if err != nil {
		t.Fatal(err)
	}
	if st.Replaced != 1 {
		t.Fatalf("want 1 replaced, got %+v", st)
	}
	got := map[string]bool{}
	for _, d := range st.TouchedDays {
		got[d] = true
	}
	if !got["2026-06-10"] || !got["2026-06-11"] {
		t.Fatalf("cross-boundary replacement touched %v, want both 2026-06-10 and 2026-06-11", st.TouchedDays)
	}
}

// Mutable-store semantics: re-ingesting an event whose deterministic ID
// already exists but whose payload changed (OpenCode finalizing an
// in-flight message row) replaces the stored row to mirror the source;
// an identical payload stays a no-op, and a provenance-only difference
// (adapter_version) never triggers a replacement.
func TestInsertBatchReplacesChangedPayload(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	partial := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10, Output: 3})

	src := testSource(1)
	src.AdapterVersion = 1
	if stats, err := s.InsertBatch(ctx, []core.Event{partial}, src); err != nil || stats.Inserted != 1 {
		t.Fatalf("first insert: %+v, %v", stats, err)
	}

	// Same deterministic ID, finalized token counts.
	final := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10, Output: 42})
	stats, err := s.InsertBatch(ctx, []core.Event{final}, src)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if stats.Inserted != 0 || stats.Replaced != 1 {
		t.Fatalf("replace: got %+v, want 0 inserted, 1 replaced", stats)
	}
	days, err := s.Daily(ctx, time.UTC, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Output != 42 {
		t.Fatalf("stored payload not replaced: %+v", days)
	}
	total, err := s.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("replacement duplicated the row: %d", total)
	}

	// Identical payload re-ingested → idempotent no-op.
	if stats, err = s.InsertBatch(ctx, []core.Event{final}, src); err != nil ||
		stats.Inserted != 0 || stats.Replaced != 0 {
		t.Fatalf("identical re-ingest: %+v, %v", stats, err)
	}

	// adapter_version alone is provenance, never a payload change.
	src.AdapterVersion = 2
	if stats, err = s.InsertBatch(ctx, []core.Event{final}, src); err != nil ||
		stats.Inserted != 0 || stats.Replaced != 0 {
		t.Fatalf("provenance-only re-ingest: %+v, %v", stats, err)
	}
}

func TestInsertBatchRejectsInvalid(t *testing.T) {
	s := openTemp(t)
	bad := event("m1", "r1", "model-a", "s1", time.Now(), TokenSums{})
	bad.Accuracy = "nope"
	if _, err := s.InsertBatch(context.Background(), []core.Event{bad}, testSource(1)); err == nil {
		t.Fatal("expected validation error")
	}
}

// Hard rule 6 at the store boundary: an event whose Raw still carries
// content-bearing text must never reach the DB.
func TestInsertBatchRejectsUnsanitizedRaw(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	bad := event("m1", "r1", "model-a", "s1", time.Now().UTC(), TokenSums{})
	bad.Raw = []byte(`{"message":{"content":"verbatim user prompt text"}}`)
	if _, err := s.InsertBatch(ctx, []core.Event{bad}, testSource(1)); err == nil {
		t.Fatal("expected sanitizer-invariant error")
	}
	n, err := s.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("unsanitized event persisted: %d rows", n)
	}
}

// Day bucketing happens in the query timezone, not UTC: 22:30Z on June 9 is
// already June 10 in Europe/Istanbul (+03).
func TestDailyTimezoneBucketing(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ist, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatalf("tzdata embedded via time/tzdata, must resolve: %v", err)
	}
	batch := []core.Event{
		event("m1", "r1", "model-a", "s1",
			time.Date(2026, 6, 9, 22, 30, 0, 0, time.UTC), TokenSums{Input: 5}),
		event("m2", "r2", "model-b", "s1",
			time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC), TokenSums{Output: 7}),
		event("m3", "r3", "model-a", "s2",
			time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC), TokenSums{CacheRead: 9}),
	}
	if _, err := s.InsertBatch(ctx, batch, testSource(3)); err != nil {
		t.Fatal(err)
	}

	days, err := s.Daily(ctx, ist, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2: %+v", len(days), days)
	}
	if days[0].Date != "2026-06-09" || days[0].CacheRead != 9 {
		t.Errorf("day 0 wrong: %+v", days[0])
	}
	if days[1].Date != "2026-06-10" || days[1].Input != 5 || days[1].Output != 7 {
		t.Errorf("day 1 wrong: %+v", days[1])
	}
	if len(days[1].ModelBreakdowns) != 2 || days[1].ModelBreakdowns[0].Model != "model-a" {
		t.Errorf("day 1 breakdowns wrong: %+v", days[1].ModelBreakdowns)
	}

	// Same data in UTC buckets differently.
	utcDays, err := s.Daily(ctx, time.UTC, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(utcDays) != 2 || utcDays[0].Input != 5 || utcDays[0].CacheRead != 9 {
		t.Errorf("utc bucketing wrong: %+v", utcDays)
	}
}

// SQL day bucketing (tatitok_day) across the 4-zone matrix with
// DST-boundary and odd-offset cases (M2.1 item 8). Expected dates are
// computed BY HAND from the IANA rules, not via the same Go call the
// function uses: US DST 2026 starts Mar 8 10:00Z (PST→PDT) and ends
// Nov 1 09:00Z (PDT→PST); Istanbul is fixed +03 (no DST since 2016);
// Kathmandu is fixed +05:45.
func TestDailyTimezoneMatrixDST(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	batch := []core.Event{
		// LA spring-forward bracket: both sides of Mar 8 10:00Z
		event("m1", "r1", "model-a", "s1",
			time.Date(2026, 3, 8, 9, 30, 0, 0, time.UTC), TokenSums{Input: 1}),
		event("m2", "r2", "model-a", "s1",
			time.Date(2026, 3, 8, 10, 30, 0, 0, time.UTC), TokenSums{Input: 2}),
		// LA fall-back bracket: 06:30Z is 23:30 PDT Oct 31; 09:30Z is 01:30 PST Nov 1
		event("m3", "r3", "model-a", "s1",
			time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), TokenSums{Input: 4}),
		event("m4", "r4", "model-a", "s1",
			time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC), TokenSums{Input: 8}),
		// Kathmandu's +05:45 crosses midnight at 18:15Z
		event("m5", "r5", "model-a", "s1",
			time.Date(2026, 6, 9, 18, 20, 0, 0, time.UTC), TokenSums{Input: 16}),
		// Istanbul's +03 crosses midnight at 21:00Z
		event("m6", "r6", "model-a", "s1",
			time.Date(2026, 6, 9, 21, 30, 0, 0, time.UTC), TokenSums{Input: 32}),
	}
	if _, err := s.InsertBatch(ctx, batch, testSource(6)); err != nil {
		t.Fatal(err)
	}

	want := map[string]map[string]int64{
		"UTC": {
			"2026-03-08": 3, "2026-06-09": 48, "2026-11-01": 12},
		"Europe/Istanbul": {
			"2026-03-08": 3, "2026-06-09": 16, "2026-06-10": 32, "2026-11-01": 12},
		"America/Los_Angeles": {
			"2026-03-08": 3, "2026-06-09": 48, "2026-10-31": 4, "2026-11-01": 8},
		"Asia/Kathmandu": {
			"2026-03-08": 3, "2026-06-10": 48, "2026-11-01": 12},
	}
	for zone, days := range want {
		t.Run(zone, func(t *testing.T) {
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatalf("tzdata embedded via time/tzdata, must resolve: %v", err)
			}
			got, err := s.Daily(ctx, loc, Filters{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(days) {
				t.Fatalf("got %d days, want %d: %+v", len(got), len(days), got)
			}
			for _, d := range got {
				if days[d.Date] != d.Input {
					t.Errorf("%s: input %d, want %d", d.Date, d.Input, days[d.Date])
				}
			}
		})
	}
}

func TestSessions(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	batch := []core.Event{
		event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 1}),
		event("m2", "r2", "model-b", "s1", ts.Add(time.Hour), TokenSums{Output: 2}),
		event("m3", "r3", "model-a", "s2", ts.Add(2*time.Hour), TokenSums{CacheWrite: 3}),
	}
	if _, err := s.InsertBatch(ctx, batch, testSource(3)); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.Sessions(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].SessionID != "s1" || sessions[0].Input != 1 || sessions[0].Output != 2 {
		t.Errorf("s1 wrong: %+v", sessions[0])
	}
	if sessions[0].Harness != "claude-code" {
		t.Errorf("s1 harness wrong: %+v", sessions[0])
	}
	if len(sessions[0].ModelsUsed) != 2 {
		t.Errorf("s1 models wrong: %+v", sessions[0].ModelsUsed)
	}
}

// Session identity is (machine, harness, session_id): native ids can
// collide across harnesses, and the empty session id is common to
// several — they must never merge into one row (M2.1 review item 3;
// machine completed the key in M3 Task 0).
func TestSessionsCrossHarnessCollision(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	batch := []core.Event{
		// same session id under two harnesses
		eventH("claude-code", "m1", "r1", "model-a", "shared", ts, TokenSums{Input: 1}),
		eventH("codex", "m2", "r2", "model-b", "shared", ts.Add(time.Minute), TokenSums{Output: 2}),
		// empty session id under two harnesses
		eventH("claude-code", "m3", "r3", "model-a", "", ts.Add(2*time.Minute), TokenSums{CacheWrite: 3}),
		eventH("opencode", "m4", "r4", "model-c", "", ts.Add(3*time.Minute), TokenSums{CacheRead: 4}),
	}
	if _, err := s.InsertBatch(ctx, batch, testSource(4)); err != nil {
		t.Fatal(err)
	}

	sessions, err := s.Sessions(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 4 {
		t.Fatalf("got %d sessions, want 4 (cross-harness ids must not merge): %+v",
			len(sessions), sessions)
	}
	bySession := map[sessionKey]SessionRow{}
	for _, r := range sessions {
		bySession[sessionKey{r.Machine, r.Harness, r.SessionID}] = r
	}
	if r := bySession[sessionKey{"test", "claude-code", "shared"}]; r.Input != 1 || r.Output != 0 {
		t.Errorf("claude-code/shared absorbed foreign tokens: %+v", r)
	}
	if r := bySession[sessionKey{"test", "codex", "shared"}]; r.Output != 2 || r.Input != 0 {
		t.Errorf("codex/shared absorbed foreign tokens: %+v", r)
	}
	if r := bySession[sessionKey{"test", "claude-code", ""}]; r.CacheWrite != 3 || r.CacheRead != 0 {
		t.Errorf("claude-code/<empty> absorbed foreign tokens: %+v", r)
	}
	if r := bySession[sessionKey{"test", "opencode", ""}]; r.CacheRead != 4 || r.CacheWrite != 0 {
		t.Errorf("opencode/<empty> absorbed foreign tokens: %+v", r)
	}

	// --harness restriction still keys correctly
	only, err := s.Sessions(ctx, time.UTC, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Harness != "codex" || only[0].Output != 2 {
		t.Fatalf("harness-restricted sessions wrong: %+v", only)
	}
}

// The same (harness, session_id) on two machines is two sessions — the
// composite identity's machine component (M3 Task 0).
func TestSessionsCrossMachineCollision(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	a := eventH("claude-code", "m1", "r1", "model-a", "shared", ts, TokenSums{Input: 1})
	b := eventH("claude-code", "m2", "r2", "model-a", "shared", ts.Add(time.Minute), TokenSums{Output: 2})
	b.Machine = "mac"
	if _, err := s.InsertBatch(ctx, []core.Event{a, b}, testSource(2)); err != nil {
		t.Fatal(err)
	}

	sessions, err := s.Sessions(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2 (cross-machine ids must not merge): %+v",
			len(sessions), sessions)
	}
	bySession := map[sessionKey]SessionRow{}
	for _, r := range sessions {
		bySession[sessionKey{r.Machine, r.Harness, r.SessionID}] = r
	}
	if r := bySession[sessionKey{"test", "claude-code", "shared"}]; r.Input != 1 || r.Output != 0 {
		t.Errorf("test/claude-code/shared absorbed foreign tokens: %+v", r)
	}
	if r := bySession[sessionKey{"mac", "claude-code", "shared"}]; r.Output != 2 || r.Input != 0 {
		t.Errorf("mac/claude-code/shared absorbed foreign tokens: %+v", r)
	}
}

// Two processes opening the same database at once must both succeed and
// migrate it exactly once (M3.1 finding 3): the schema_version read
// happens inside BEGIN IMMEDIATE, so concurrent migrators serialize on
// the write lock instead of both reading a stale version and racing to
// apply the same migration ("table already exists").
func TestOpenConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	const openers = 8
	errs := make(chan error, openers)
	for i := 0; i < openers; i++ {
		go func() {
			s, err := Open(path)
			if err == nil {
				_ = s.Close()
			}
			errs <- err
		}()
	}
	for i := 0; i < openers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent open: %v", err)
		}
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var rows, version int
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(version),0)
		FROM schema_version`).Scan(&rows, &version); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || version != len(migrations) {
		t.Fatalf("schema_version after concurrent opens: %d rows @ v%d, want 1 @ v%d",
			rows, version, len(migrations))
	}
}

func TestMigrateIsIdempotentAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	if _, err := s.InsertBatch(context.Background(),
		[]core.Event{event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 1})},
		testSource(1)); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	n, err := s2.CountEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("data lost across reopen: %d", n)
	}
}

func TestScanRawForContent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	e := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 1})
	e.Raw = []byte(`{"message":{"content":"<stripped len=42 sha256=abcdef123456>"}}`)
	if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
		t.Fatal(err)
	}
	hits, err := s.ScanRawForContent(ctx, "stripped len=42")
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("expected to find placeholder, got %d hits", hits)
	}
	hits, err = s.ScanRawForContent(ctx, "the actual secret prompt")
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("unexpected content hit: %d", hits)
	}
}
