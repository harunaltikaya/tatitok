package hub

// Watcher tests drive real fixture content (never fabricated lines —
// CLAUDE.md hard rule 1) through temp watch roots: files are copied or
// appended in parts, and the watched store must converge to exactly
// what a CLI backfill of the same root produces, with rollups byte-equal
// to direct aggregation throughout.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/opencode"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const opencodeFixtureDir = "../../testdata/fixtures/opencode/gx10"

// fixtureSessionFiles returns the claude-code machine fixture's session
// files as (relative project dir, file name, content), smallest first.
func fixtureSessionFiles(t *testing.T) []fixtureFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(ccFixtureRoot, "*", "*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no claude-code fixture session files: %v", err)
	}
	var out []fixtureFile
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fixtureFile{
			project: filepath.Base(filepath.Dir(p)),
			name:    filepath.Base(p),
			content: b,
		})
	}
	// Smallest first so tests can pick cheap files deterministically.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j].content) < len(out[j-1].content); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

type fixtureFile struct {
	project string
	name    string
	content []byte
}

// splitLines cuts content at a line boundary roughly in half; both
// halves are real fixture lines (the first ends with a newline).
func splitLines(t *testing.T, content []byte) (head, tail []byte) {
	t.Helper()
	cut := bytes.Index(content[len(content)/2:], []byte("\n"))
	if cut < 0 {
		t.Fatal("fixture file too small to split")
	}
	cut += len(content)/2 + 1
	return content[:cut], content[cut:]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// assertConverged: the watched store equals a fresh CLI backfill of the
// same root (count + daily aggregation), and its rollup-served daily is
// byte-equal to direct aggregation (the property the soak generalizes).
func assertConverged(t *testing.T, got *store.Store, a adapters.Adapter, src adapters.Source) {
	t.Helper()
	ctx := context.Background()
	ref, err := store.Open(filepath.Join(t.TempDir(), "ref.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.Close() }()
	if _, err := adapters.IngestBackfill(ctx, ref, a, []adapters.Source{src}, nil); err != nil {
		t.Fatalf("reference backfill: %v", err)
	}
	gotN, _ := got.CountEvents(ctx)
	wantN, _ := ref.CountEvents(ctx)
	if gotN != wantN {
		t.Errorf("watched store has %d events, reference backfill %d", gotN, wantN)
	}
	gd, err := got.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	rd, err := ref.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(gd)
	rj, _ := json.Marshal(rd)
	if string(gj) != string(rj) {
		t.Errorf("watched daily != reference daily\ngot  %s\nwant %s", gj, rj)
	}
	assertRollupsConsistent(t, got)
}

func assertRollupsConsistent(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	direct, err := st.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := st.DailyFromRollups(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	dj, _ := json.Marshal(direct)
	rj, _ := json.Marshal(rolled)
	if string(dj) != string(rj) {
		t.Error("rollup-served daily != direct aggregation after watch ingest")
	}
}

func startWatchHub(t *testing.T, targets []WatchTarget, debounce, poll time.Duration) *Hub {
	t.Helper()
	h, err := Start(Config{
		DBPath: filepath.Join(t.TempDir(), "hub.db"), Addr: "127.0.0.1:0",
		WatchTargets: targets, Debounce: debounce, PollInterval: poll,
	})
	if err != nil {
		t.Fatalf("hub start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return h
}

func eventCount(t *testing.T, st *store.Store) int64 {
	t.Helper()
	n, err := st.CountEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestWatchNotifyIncremental: the fsnotify path end to end — catch-up
// of a half-written file at start, append detection, and a project
// directory created while watching (with a file inside).
func TestWatchNotifyIncremental(t *testing.T) {
	files := fixtureSessionFiles(t)
	if len(files) < 2 {
		t.Skip("need at least two claude-code fixture session files")
	}
	grow, whole := files[len(files)-1], files[0] // biggest split-grows, smallest arrives whole

	root := t.TempDir()
	head, tail := splitLines(t, grow.content)
	growDir := filepath.Join(root, grow.project)
	if err := os.MkdirAll(growDir, 0o755); err != nil {
		t.Fatal(err)
	}
	growPath := filepath.Join(growDir, grow.name)
	if err := os.WriteFile(growPath, head, 0o644); err != nil {
		t.Fatal(err)
	}

	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}
	h := startWatchHub(t, []WatchTarget{{Adapter: claudecode.Adapter{}, Source: src}},
		150*time.Millisecond, time.Hour)

	// Catch-up: the half file ingests without any fsnotify event.
	waitFor(t, "catch-up pass over the pre-existing half file", func() bool {
		return h.w.passes.Load() >= 1
	})

	// Append the rest — fsnotify Write.
	f, err := os.OpenFile(growPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(tail); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// New project dir created while watching, file lands inside.
	newDir := filepath.Join(root, whole.project+"-live")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, whole.name), whole.content, 0o644); err != nil {
		t.Fatal(err)
	}

	want := int64(-1)
	waitFor(t, "watched store to converge with the reference backfill", func() bool {
		if want < 0 {
			ref, err := store.Open(filepath.Join(t.TempDir(), "count-ref.db"))
			if err != nil {
				return false
			}
			defer func() { _ = ref.Close() }()
			if _, err := adapters.IngestBackfill(context.Background(), ref, claudecode.Adapter{}, []adapters.Source{src}, nil); err != nil {
				return false
			}
			want = eventCount(t, ref)
		}
		return eventCount(t, h.st) == want
	})
	assertConverged(t, h.st, claudecode.Adapter{}, src)
}

// pollOnlyAdapter forces the polling path for a notify-capable adapter
// — the automatic-fallback machinery under test without breaking
// fsnotify on purpose.
type pollOnlyAdapter struct{ claudecode.Adapter }

func (a pollOnlyAdapter) WatchSpec(src adapters.Source) adapters.WatchSpec {
	spec := a.Adapter.WatchSpec(src)
	spec.PollOnly = true
	return spec
}

// TestWatchPollingFallback: same convergence as the notify test, but
// every change is discovered by the walk-and-stat poller.
func TestWatchPollingFallback(t *testing.T) {
	files := fixtureSessionFiles(t)
	grow := files[len(files)-1]

	root := t.TempDir()
	head, tail := splitLines(t, grow.content)
	dir := filepath.Join(root, grow.project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, grow.name)
	if err := os.WriteFile(path, head, 0o644); err != nil {
		t.Fatal(err)
	}

	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}
	h := startWatchHub(t, []WatchTarget{{Adapter: pollOnlyAdapter{}, Source: src}},
		100*time.Millisecond, 100*time.Millisecond)

	waitFor(t, "catch-up pass over the pre-existing half file", func() bool {
		return h.w.passes.Load() >= 1
	})
	p0 := h.w.passes.Load()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(tail); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	waitFor(t, "poller to pick up the append", func() bool {
		return h.w.passes.Load() > p0
	})
	assertConverged(t, h.st, pollOnlyAdapter{}, src)
}

// TestWatchOpencodePoll: the store-watching strategy — catch-up ingest
// of a reconstructed fixture database, then re-detection when the
// database is atomically replaced, with an idempotent result.
func TestWatchOpencodePoll(t *testing.T) {
	fixtureDir, err := filepath.Abs(opencodeFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "opencode")
	dbPath := filepath.Join(root, "opencode.db")
	if _, err := opencode.BuildFixtureDB(fixtureDir, dbPath); err != nil {
		t.Fatalf("reconstruct fixture db: %v", err)
	}

	src := adapters.Source{Harness: "opencode", Root: root, Machine: "gx10"}
	h := startWatchHub(t, []WatchTarget{{Adapter: opencode.Adapter{}, Source: src}},
		100*time.Millisecond, 100*time.Millisecond)

	waitFor(t, "catch-up ingest of the message store", func() bool {
		return eventCount(t, h.st) > 0
	})
	n := eventCount(t, h.st)
	passes := h.w.passes.Load()

	// Replace the database atomically (build aside, rename over) — the
	// poller must re-detect via mtime and re-ingest idempotently.
	aside := filepath.Join(t.TempDir(), "rebuilt.db")
	if _, err := opencode.BuildFixtureDB(fixtureDir, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(aside, dbPath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "re-detection pass after database replacement", func() bool {
		return h.w.passes.Load() > passes
	})
	if got := eventCount(t, h.st); got != n {
		t.Errorf("re-ingest changed the store: %d events, was %d (must be idempotent)", got, n)
	}
	assertConverged(t, h.st, opencode.Adapter{}, src)
}

// TestWatchRescanCoversDroppedNotify is the Codex M4 finding-3
// reproduction, kept permanently: an fsnotify watcher that silently
// delivers nothing (the dropped-buffered-event scenario — here the
// notifyDisabled test hook, which does NOT flip targets to polling)
// must not lose changes until restart. The periodic safety-net rescan
// stats notify targets every rescanTicks-th poll tick and picks the
// change up within a bounded window.
func TestWatchRescanCoversDroppedNotify(t *testing.T) {
	files := fixtureSessionFiles(t)
	whole := files[0]

	root := t.TempDir()
	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}

	st, err := store.Open(filepath.Join(t.TempDir(), "rescan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	w := startWatcher(st, nil, []WatchTarget{{Adapter: claudecode.Adapter{}, Source: src}},
		100*time.Millisecond, 100*time.Millisecond, nil,
		func(w *watcher) { w.notifyDisabled = true; w.rescanTicks = 2 })
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.Stop(ctx)
	}()

	// Change appears AFTER the startup catch-up scan: only the rescan
	// can see it (fsnotify is "delivering nothing", polling is off for
	// notify-mode targets except the rescan).
	time.Sleep(150 * time.Millisecond)
	dir := filepath.Join(root, whole.project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, whole.name), whole.content, 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "safety-net rescan to ingest the change fsnotify never delivered", func() bool {
		n, err := st.CountEvents(context.Background())
		return err == nil && n > 0
	})
	assertConverged(t, st, claudecode.Adapter{}, src)
}

// TestWatchNotifyBrokenFlipsToPolling: runtime fsnotify failure parity
// with setup failure (Codex M4 finding 3) — a broken watcher degrades
// every notify source to polling, and changes keep flowing.
func TestWatchNotifyBrokenFlipsToPolling(t *testing.T) {
	files := fixtureSessionFiles(t)
	whole := files[0]

	root := t.TempDir()
	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}
	st, err := store.Open(filepath.Join(t.TempDir(), "broken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	w := startWatcher(st, nil, []WatchTarget{{Adapter: claudecode.Adapter{}, Source: src}},
		100*time.Millisecond, 100*time.Millisecond, nil,
		func(w *watcher) { w.notifyDisabled = true; w.rescanTicks = 1 << 30 }) // rescan effectively off
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.Stop(ctx)
	}()

	if w.targets[0].polled.Load() {
		t.Fatal("notify target polled before any failure")
	}
	w.notifyBroken("test: simulated runtime fsnotify failure")
	if !w.targets[0].polled.Load() {
		t.Fatal("notifyBroken did not flip the notify target to polling")
	}

	// With the target on the polling path, a new file must ingest even
	// though fsnotify delivers nothing and the rescan is disabled.
	dir := filepath.Join(root, whole.project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, whole.name), whole.content, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "polling fallback to ingest after notify breakage", func() bool {
		n, err := st.CountEvents(context.Background())
		return err == nil && n > 0
	})
	assertConverged(t, st, claudecode.Adapter{}, src)
}

// TestWatchDebounceCoalescing: a burst of rapid appends to one file
// becomes exactly one ingest pass.
func TestWatchDebounceCoalescing(t *testing.T) {
	files := fixtureSessionFiles(t)
	grow := files[0] // smallest: the whole burst must fit inside one window
	lines := bytes.SplitAfter(grow.content, []byte("\n"))

	root := t.TempDir()
	dir := filepath.Join(root, grow.project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}
	h := startWatchHub(t, []WatchTarget{{Adapter: claudecode.Adapter{}, Source: src}},
		700*time.Millisecond, time.Hour)
	time.Sleep(100 * time.Millisecond) // let the (empty) catch-up scan finish

	path := filepath.Join(dir, grow.name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range lines { // every append is an fsnotify event
		if len(ln) == 0 {
			continue
		}
		if _, err := f.Write(ln); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	_ = f.Close()

	waitFor(t, "the single debounced pass", func() bool {
		return h.w.passes.Load() >= 1
	})
	time.Sleep(900 * time.Millisecond) // a second window: nothing else may fire
	if got := h.w.passes.Load(); got != 1 {
		t.Errorf("burst of %d appends ran %d ingest passes, want exactly 1 (debounce)", len(lines), got)
	}
	assertConverged(t, h.st, claudecode.Adapter{}, src)
}
