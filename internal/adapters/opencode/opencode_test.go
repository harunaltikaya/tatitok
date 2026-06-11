package opencode

// All parsing tests run against the committed, owner-harvested fixture
// set (hard rule 1: no fabricated log lines). The fixture database is
// reconstructed at test time from the committed sanitized text (see
// fixturedb.go); numeric expectations are measured properties of the set.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const fixtureBase = "../../../testdata/fixtures/opencode/gx10"

// Measured from the committed gx10 fixture set: 929 message rows, 848
// assistant messages with a tokens object, of which 14 are zero-token
// aborted turns ccusage skips → 834 billable. Message ids are the table
// PK — every event unique.
const (
	wantRows    = 929
	wantEmitted = 834
	wantUnique  = 834
)

// fixtureSource reconstructs the fixture db under a temp dir and returns
// a Source rooted at it.
func fixtureSource(t *testing.T) adapters.Source {
	t.Helper()
	dir, err := filepath.Abs(fixtureBase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixture dir missing (harvest + commit first): %v", err)
	}
	root := filepath.Join(t.TempDir(), "opencode")
	n, err := BuildFixtureDB(dir, filepath.Join(root, "opencode.db"))
	if err != nil {
		t.Fatalf("reconstruct fixture db: %v", err)
	}
	if n != wantRows {
		t.Fatalf("reconstructed %d rows, want %d", n, wantRows)
	}
	return adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}
}

// collectSink mirrors the other adapters' test sink: collects everything
// and fails the test on contract-v2 bracketing violations.
type collectSink struct {
	t       *testing.T
	events  []core.Event
	byFile  map[string][]core.Event
	results []adapters.FileResult
	done    map[string]bool
	open    string // file declared by FileStart but no FileDone yet
}

func newCollectSink(t *testing.T) *collectSink {
	return &collectSink{t: t,
		byFile: map[string][]core.Event{}, done: map[string]bool{}}
}

func (c *collectSink) FileStart(path string) error {
	c.t.Helper()
	if c.done[path] {
		c.t.Fatalf("duplicate path %s in one backfill run", path)
	}
	if c.open != "" {
		c.t.Fatalf("FileStart for %s while %s is still open", path, c.open)
	}
	c.open = path
	return nil
}

func (c *collectSink) EmitBatch(path string, events []core.Event) error {
	c.t.Helper()
	if len(events) == 0 || len(events) > adapters.BatchSize {
		c.t.Fatalf("batch of %d events for %s (BatchSize %d)",
			len(events), path, adapters.BatchSize)
	}
	if c.open != path {
		c.t.Fatalf("batch for %s outside its FileStart/FileDone bracket (open: %q)",
			path, c.open)
	}
	c.events = append(c.events, events...)
	c.byFile[path] = append(c.byFile[path], events...)
	return nil
}

func (c *collectSink) FileDone(res adapters.FileResult) error {
	c.t.Helper()
	if c.open != res.Path {
		c.t.Fatalf("FileDone for %s outside its FileStart bracket (open: %q)",
			res.Path, c.open)
	}
	if got := len(c.byFile[res.Path]); res.ReadError == "" && got != res.Events {
		c.t.Fatalf("%s: FileDone.Events=%d but %d events emitted",
			res.Path, res.Events, got)
	}
	c.done[res.Path] = true
	c.open = ""
	c.results = append(c.results, res)
	return nil
}

func backfillAll(t *testing.T) *collectSink {
	t.Helper()
	sink := newCollectSink(t)
	if err := (Adapter{}).Backfill(context.Background(), fixtureSource(t), sink); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if sink.open != "" {
		t.Fatalf("backfill returned with %s still open", sink.open)
	}
	return sink
}

func TestDetect(t *testing.T) {
	probe := func(env map[string]string, home string) adapters.Probe {
		return adapters.Probe{
			Getenv:  func(k string) string { return env[k] },
			HomeDir: home,
			Machine: "gx10",
		}
	}
	mkdb := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "opencode.db"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("xdg override wins", func(t *testing.T) {
		xdg := t.TempDir()
		mkdb(t, filepath.Join(xdg, "opencode"))
		srcs, err := (Adapter{}).Detect(probe(map[string]string{"XDG_DATA_HOME": xdg}, "/nonexistent"))
		if err != nil {
			t.Fatal(err)
		}
		want := []adapters.Source{{Harness: harnessName,
			Root: filepath.Join(xdg, "opencode"), Machine: "gx10"}}
		if !reflect.DeepEqual(srcs, want) {
			t.Fatalf("got %+v, want %+v", srcs, want)
		}
	})

	t.Run("default home", func(t *testing.T) {
		home := t.TempDir()
		mkdb(t, filepath.Join(home, ".local", "share", "opencode"))
		srcs, err := (Adapter{}).Detect(probe(nil, home))
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 1 || srcs[0].Root != filepath.Join(home, ".local", "share", "opencode") {
			t.Fatalf("got %+v", srcs)
		}
	})

	t.Run("nothing found", func(t *testing.T) {
		srcs, err := (Adapter{}).Detect(probe(nil, t.TempDir()))
		if err != nil || len(srcs) != 0 {
			t.Fatalf("got %+v, %v", srcs, err)
		}
	})
}

func TestBackfillFixtures(t *testing.T) {
	sink := backfillAll(t)

	ids := map[string]bool{}
	for _, e := range sink.events {
		ids[e.ID] = true
	}
	if len(sink.events) != wantEmitted {
		t.Errorf("emitted %d billable events, want %d", len(sink.events), wantEmitted)
	}
	if len(ids) != wantUnique {
		t.Errorf("got %d unique ids, want %d", len(ids), wantUnique)
	}

	for i := range sink.events {
		e := &sink.events[i]
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		if e.Accuracy != core.AccuracyExact {
			t.Fatalf("event %s accuracy %q, want exact", e.ID, e.Accuracy)
		}
		if e.Harness != harnessName || e.Machine != "gx10" {
			t.Fatalf("bad identity fields: %+v", e)
		}
		// provider+model are explicit on every opencode usage record and
		// pass through verbatim (local vLLM providers included)
		if e.Provider == "" || e.Model == "" {
			t.Fatalf("missing provider/model on %s (provider=%q model=%q)",
				e.ID, e.Provider, e.Model)
		}
		if e.SessionID == "" || len(e.Raw) == 0 {
			t.Fatalf("missing session/raw on %s", e.ID)
		}
		if e.TokensReasoning == nil {
			t.Fatalf("missing reasoning tokens on %s", e.ID)
		}
		sum := e.TokensInput + e.TokensOutput + *e.TokensReasoning +
			e.TokensCacheRead + e.TokensCacheWrite
		if sum == 0 {
			t.Fatalf("zero-token event emitted (ccusage skips those): %s", e.ID)
		}
	}

	if len(sink.results) != 1 {
		t.Fatalf("want 1 source result (the db), got %d", len(sink.results))
	}
	res := sink.results[0]
	if res.LineCount != wantRows {
		t.Errorf("scanned %d rows, want %d", res.LineCount, wantRows)
	}
	if res.ParseErrors != 0 || res.ReadError != "" {
		t.Fatalf("fixture db reported unhealthy: %+v", res)
	}
	if res.Events != wantEmitted {
		t.Errorf("FileResult.Events = %d, want %d", res.Events, wantEmitted)
	}
}

// Hard rule 5: re-ingesting the same store twice must yield identical DB
// contents.
func TestReingestIdempotent(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	src := fixtureSource(t)

	ingest := func() adapters.IngestSummary {
		t.Helper()
		sum, err := adapters.IngestBackfill(ctx, s, Adapter{},
			[]adapters.Source{src}, nil)
		if err != nil {
			t.Fatalf("ingest: %v", err)
		}
		return sum
	}

	first := ingest()
	if first.Inserted != wantUnique || first.Emitted != wantEmitted {
		t.Fatalf("first ingest inserted %d emitted %d, want %d/%d",
			first.Inserted, first.Emitted, wantUnique, wantEmitted)
	}
	if first.ParseErrors != 0 {
		t.Fatalf("parse errors on fixtures: %d", first.ParseErrors)
	}
	daily1, err := s.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}

	if again := ingest(); again.Inserted != 0 || again.Replaced != 0 {
		t.Fatalf("re-ingest inserted %d / replaced %d rows, want 0/0",
			again.Inserted, again.Replaced)
	}
	daily2, err := s.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(daily1, daily2) {
		t.Fatal("daily stats changed after re-ingest")
	}
	total, err := s.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != wantUnique {
		t.Fatalf("row count %d after re-ingest, want %d", total, wantUnique)
	}
}

// assertRollupMatchesDaily: rollup-served daily must equal direct
// aggregation at UTC (M3 Task 3 consistency property on the replacement
// path).
func assertRollupMatchesDaily(t *testing.T, s *store.Store, when string) {
	t.Helper()
	ctx := context.Background()
	direct, err := s.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := s.DailyFromRollups(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(direct, rolled) {
		t.Fatalf("%s: rollup-served daily diverged from direct aggregation", when)
	}
}

// M2.1 review item 1 (owner-directed test): OpenCode message rows are
// mutable while a turn is in flight, so a row ingested mid-turn must be
// superseded when a later ingest reads its finalized form. The
// "in-flight" variant is derived from one real fixture row by removing
// data.time.completed and shrinking tokens.output — the documented
// mid-turn state a live snapshot could capture (docs/format-notes.md
// "Message rows are mutable"); no log lines are fabricated from scratch.
func TestMutatedRowSupersededOnReingest(t *testing.T) {
	ctx := context.Background()

	// Reference truth: the pristine fixture set in its own store.
	ref, err := store.Open(filepath.Join(t.TempDir(), "ref.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.Close() }()
	if _, err := adapters.IngestBackfill(ctx, ref, Adapter{},
		[]adapters.Source{fixtureSource(t)}, nil); err != nil {
		t.Fatal(err)
	}
	wantDaily, err := ref.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite one fixture row into its in-flight form. Victim: first row in
	// backfill order that is billable, completed, and has output >= 2 (so
	// the halved partial value stays a nonzero integer and still emits).
	src := fixtureSource(t)
	dbPath := filepath.Join(src.Root, "opencode.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, time_created, time_updated, data FROM message ORDER BY time_created, id`)
	if err != nil {
		t.Fatal(err)
	}
	var victimID, origData string
	var victimCreated, origUpdated int64
	for rows.Next() {
		var id, data string
		var tc, tu int64
		if err := rows.Scan(&id, &tc, &tu, &data); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			t.Fatal(err)
		}
		tok, _ := d["tokens"].(map[string]any)
		tm, _ := d["time"].(map[string]any)
		if d["role"] != "assistant" || tok == nil || tm == nil {
			continue
		}
		if _, completed := tm["completed"]; !completed {
			continue
		}
		if out, _ := tok["output"].(float64); out >= 2 {
			victimID, victimCreated, origUpdated, origData = id, tc, tu, data
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if victimID == "" {
		t.Fatal("no completed billable fixture row with output >= 2")
	}

	var partial map[string]any
	if err := json.Unmarshal([]byte(origData), &partial); err != nil {
		t.Fatal(err)
	}
	delete(partial["time"].(map[string]any), "completed")
	tok := partial["tokens"].(map[string]any)
	tok["output"] = int64(tok["output"].(float64)) / 2
	partialJSON, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE message SET data = ?, time_updated = ? WHERE id = ?`,
		string(partialJSON), victimCreated, victimID); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ingest := func() adapters.IngestSummary {
		t.Helper()
		sum, err := adapters.IngestBackfill(ctx, s, Adapter{}, []adapters.Source{src}, nil)
		if err != nil {
			t.Fatalf("ingest: %v", err)
		}
		if sum.ParseErrors != 0 {
			t.Fatalf("parse errors: %d", sum.ParseErrors)
		}
		return sum
	}

	// Ingest the snapshot holding the in-flight row: still a full event set
	// (nonzero partial rows are billable — ccusage counts them at the same
	// snapshot), but the day sums diverge from the finalized truth.
	first := ingest()
	if first.Inserted != wantUnique || first.Replaced != 0 {
		t.Fatalf("first ingest: %d inserted / %d replaced, want %d/0",
			first.Inserted, first.Replaced, wantUnique)
	}
	partialDaily, err := s.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(partialDaily, wantDaily) {
		t.Fatal("partial row did not change the day sums — mutation ineffective")
	}
	assertRollupMatchesDaily(t, s, "after partial-row ingest")

	// The turn finishes: restore the finalized row and re-ingest. The
	// stored event must be replaced (same ID, new payload), not frozen.
	if _, err := db.ExecContext(ctx,
		`UPDATE message SET data = ?, time_updated = ? WHERE id = ?`,
		origData, origUpdated, victimID); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	second := ingest()
	if second.Inserted != 0 || second.Replaced != 1 {
		t.Fatalf("re-ingest after finalize: %d inserted / %d replaced, want 0/1",
			second.Inserted, second.Replaced)
	}
	gotDaily, err := s.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDaily, wantDaily) {
		t.Fatal("finalized row did not supersede the partial one")
	}
	// M3 Task 3: the replacement must correct the rollups too — the
	// mutated-row fixture path, end to end.
	assertRollupMatchesDaily(t, s, "after finalized-row replacement")
	total, err := s.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != wantUnique {
		t.Fatalf("row count %d after replacement, want %d", total, wantUnique)
	}

	// And the corrected state is stable: another ingest is a no-op.
	if third := ingest(); third.Inserted != 0 || third.Replaced != 0 {
		t.Fatalf("third ingest: %d inserted / %d replaced, want 0/0",
			third.Inserted, third.Replaced)
	}
}

// goldenCeremony is the mandatory procedure for changing the frozen
// golden file; it is printed wherever someone could be tempted to skip it.
const goldenCeremony = `regenerating expected/events.json requires the full ceremony:
  1. bump opencode.AdapterVersion (format handling changed by definition)
  2. TATITOK_UPDATE_GOLDEN=1 TATITOK_CONFIRM_PARITY=1 go test ./internal/adapters/opencode -run TestGoldenEvents
  3. re-run the fixture parity gate: go test ./internal/parity (must stay EXACT)
  4. owner re-runs 'make parity-full-opencode' against the live db before the commit counts as verified
commit the golden diff together with the AdapterVersion bump and note the parity re-verification in the message`

// Golden test: fixtures in → exact expected normalized events out.
// expected/events.json was generated from the first verified-parity run
// and is FROZEN: a missing file fails the test, and regeneration demands
// an explicit two-flag confirmation of the ceremony above.
func TestGoldenEvents(t *testing.T) {
	golden, err := filepath.Abs(filepath.Join(fixtureBase, "expected", "events.json"))
	if err != nil {
		t.Fatal(err)
	}

	sink := backfillAll(t)
	var normalized []core.Event
	seen := map[string]bool{}
	for _, e := range sink.events {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		normalized = append(normalized, e)
	}
	got, err := json.MarshalIndent(normalized, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	if os.Getenv("TATITOK_UPDATE_GOLDEN") == "1" {
		if os.Getenv("TATITOK_CONFIRM_PARITY") != "1" {
			t.Fatalf("TATITOK_UPDATE_GOLDEN=1 refused without TATITOK_CONFIRM_PARITY=1\n%s",
				goldenCeremony)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d events)\n%s", golden, len(normalized), goldenCeremony)
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		// A missing golden file FAILS — skipping would let the whole
		// normalization contract go unchecked while looking green.
		t.Fatalf("frozen golden file unreadable: %v\n%s", err, goldenCeremony)
	}
	if string(got) != string(want) {
		t.Fatalf("normalized events diverge from frozen golden file %s\n%s",
			golden, goldenCeremony)
	}
}
