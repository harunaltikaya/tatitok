package hub

// The M4 Task 1 soak test — answers the M3 report's open question 4
// (watcher × rollups): a watcher ingests while a replay script appends
// realistic content (the committed real fixture corpus, chunked line by
// line) into watched roots; afterwards the rollup property (byte-equal
// vs direct aggregation) must hold and throughput is reported.
//
// Gated: TATITOK_SOAK=1 enables it. TATITOK_SOAK_DB names a COPY of the
// live database to start from (owner-run flavor; the test copies it
// again and never touches the given file). Without it the soak starts
// from an empty store.

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const (
	soakChunkLines = 50
	soakChunkPause = 10 * time.Millisecond
)

// soakCorpus lists one harness's fixture files as (relative path under
// the watch root, content).
func soakCorpus(t *testing.T, fixtureRoot string) map[string][]byte {
	t.Helper()
	abs, err := filepath.Abs(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return err
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = b
		return nil
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("soak corpus %s: %v (%d files)", fixtureRoot, err, len(out))
	}
	return out
}

// replay appends one corpus into root in line chunks — the "realistic
// appends" script: files grow incrementally, directories appear as
// sessions start, several files interleave per harness.
func replay(t *testing.T, root string, corpus map[string][]byte) {
	t.Helper()
	for rel, content := range corpus {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Error(err)
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Error(err)
			return
		}
		rest := content
		for len(rest) > 0 {
			chunk := rest
			for i, n := 0, 0; i < len(chunk); i++ {
				if chunk[i] == '\n' {
					if n++; n == soakChunkLines {
						chunk = chunk[:i+1]
						break
					}
				}
			}
			if _, err := f.Write(chunk); err != nil {
				t.Error(err)
				_ = f.Close()
				return
			}
			rest = rest[len(chunk):]
			time.Sleep(soakChunkPause)
		}
		_ = f.Close()
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherSoak(t *testing.T) {
	if os.Getenv("TATITOK_SOAK") != "1" {
		t.Skip("soak test: set TATITOK_SOAK=1 (optionally TATITOK_SOAK_DB=copy-of-live-db)")
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil))) // soak wants the pass lines visible

	dbPath := filepath.Join(t.TempDir(), "soak.db")
	refPath := filepath.Join(t.TempDir(), "soak-ref.db")
	if from := os.Getenv("TATITOK_SOAK_DB"); from != "" {
		copyFile(t, from, dbPath)
		copyFile(t, from, refPath)
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(from + suffix); err == nil {
				copyFile(t, from+suffix, dbPath+suffix)
				copyFile(t, from+suffix, refPath+suffix)
			}
		}
	}

	ccCorpus := soakCorpus(t, "../../testdata/fixtures/claude-code/gx10/projects")
	cxCorpus := soakCorpus(t, "../../testdata/fixtures/codex/gx10/sessions")
	ccRoot := filepath.Join(t.TempDir(), "projects")
	cxRoot := filepath.Join(t.TempDir(), "sessions")
	for _, r := range []string{ccRoot, cxRoot} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ccSrc := adapters.Source{Harness: "claude-code", Root: ccRoot, Machine: "gx10"}
	cxSrc := adapters.Source{Harness: "codex", Root: cxRoot, Machine: "gx10"}

	h, err := Start(Config{
		DBPath: dbPath, Addr: "127.0.0.1:0",
		WatchTargets: []WatchTarget{
			{Adapter: claudecode.Adapter{}, Source: ccSrc},
			{Adapter: codex.Adapter{}, Source: cxSrc},
		},
		Debounce: time.Second, PollInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("hub start: %v", err)
	}

	ctx := context.Background()
	baseline, err := h.st.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); replay(t, ccRoot, ccCorpus) }()
	go func() { defer wg.Done(); replay(t, cxRoot, cxCorpus) }()
	wg.Wait()
	replayDone := time.Since(start)

	// Expected end state: the starting DB plus a CLI backfill of both
	// fully-replayed roots.
	ref, err := store.Open(refPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.Close() }()
	if _, err := adapters.IngestBackfill(ctx, ref, claudecode.Adapter{}, []adapters.Source{ccSrc}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := adapters.IngestBackfill(ctx, ref, codex.Adapter{}, []adapters.Source{cxSrc}, nil); err != nil {
		t.Fatal(err)
	}
	want, err := ref.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(120 * time.Second)
	for {
		got, err := h.st.CountEvents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("soak did not converge: %d events, want %d", got, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
	converged := time.Since(start)

	// The rollup property under watcher load — the question this soak
	// answers (M3 report open question 4).
	assertRollupsConsistent(t, h.st)
	for _, harness := range []string{"claude-code", "codex", "opencode"} {
		direct, err := h.st.Daily(ctx, time.UTC, harness)
		if err != nil {
			t.Fatal(err)
		}
		rolled, err := h.st.DailyFromRollups(ctx, harness)
		if err != nil {
			t.Fatal(err)
		}
		dj, rj := mustJSON(t, direct), mustJSON(t, rolled)
		if dj != rj {
			t.Errorf("harness %s: rollup-served daily != direct aggregation after soak", harness)
		}
	}

	lines := 0
	for _, c := range [2]map[string][]byte{ccCorpus, cxCorpus} {
		for _, b := range c {
			for _, ch := range b {
				if ch == '\n' {
					lines++
				}
			}
		}
	}
	t.Logf("SOAK: %d log lines replayed in %v (chunks of %d lines / %v); %d new events; converged %v after replay start; %d ingest passes; %.0f events/sec over the soak window",
		lines, replayDone.Round(time.Millisecond), soakChunkLines, soakChunkPause,
		want-baseline, converged.Round(time.Millisecond), h.w.passes.Load(),
		float64(want-baseline)/converged.Seconds())

	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := h.Shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
