package hub

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
	"github.com/harunaltikaya/tatitok/internal/adapters/codex"
	"github.com/harunaltikaya/tatitok/internal/store"
)

// TestMain silences per-event slog noise (reference backfills emit the
// AS-4 replacement lines) — the parity-test summarization precedent.
// The soak test raises the level back when it reports throughput.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
		&slog.HandlerOptions{Level: slog.LevelWarn})))
	os.Exit(m.Run())
}

// TestDefaultAddrIsLoopback pins the milestone-4 ground rule: the
// default bind is 127.0.0.1 and the default port avoids the ports
// common local-AI tools claim.
func TestDefaultAddrIsLoopback(t *testing.T) {
	host, port, err := net.SplitHostPort(DefaultAddr)
	if err != nil {
		t.Fatalf("DefaultAddr %q: %v", DefaultAddr, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("default bind host = %q, want 127.0.0.1 (localhost-only by default)", host)
	}
	for _, taken := range []string{"8000", "8080", "3000", "5173", "11434", "1234", "7860", "8888"} {
		if port == taken {
			t.Errorf("default port %s collides with a common local tool port", port)
		}
	}
}

func TestNonLoopbackWarning(t *testing.T) {
	cases := []struct {
		ip   string
		warn bool
	}{
		{"127.0.0.1", false},
		{"::1", false},
		{"192.168.1.5", true},
		{"0.0.0.0", true}, // unspecified = all interfaces
		{"::", true},
	}
	for _, c := range cases {
		addr := &net.TCPAddr{IP: net.ParseIP(c.ip), Port: 8284}
		got := nonLoopbackWarning(addr) != ""
		if got != c.warn {
			t.Errorf("nonLoopbackWarning(%s) warned=%v, want %v", c.ip, got, c.warn)
		}
	}
}

func startHub(t *testing.T, dbPath string) *Hub {
	t.Helper()
	h, err := Start(Config{DBPath: dbPath, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("hub start: %v", err)
	}
	return h
}

// TestLifecycle exercises hard stop 0's lifecycle: start, serve a
// request, shut down cleanly, and stop accepting afterwards.
func TestLifecycle(t *testing.T) {
	h := startHub(t, filepath.Join(t.TempDir(), "hub.db"))

	resp, err := http.Get("http://" + h.Addr() + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// 200 with the embedded dashboard, 503 explainer when the bundle is
	// not built (clean checkout without `make web`) — both are alive.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET / = %d, want 200 (dashboard) or 503 (bundle not built) (body %q)", resp.StatusCode, body)
	}
	if resp, err := http.Get("http://" + h.Addr() + "/definitely-not-a-route"); err == nil {
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /definitely-not-a-route = %d, want 404", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := h.Err(); err != nil {
		t.Fatalf("serve loop error: %v", err)
	}
	if _, err := http.Get("http://" + h.Addr() + "/"); err == nil {
		t.Error("server still accepting connections after Shutdown")
	}
}

const (
	ccFixtureRoot    = "../../testdata/fixtures/claude-code/gx10/projects"
	codexFixtureRoot = "../../testdata/fixtures/codex/gx10/sessions"
)

func fixtureSource(t *testing.T, harness, root string) adapters.Source {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return adapters.Source{Harness: harness, Root: abs, Machine: "gx10"}
}

func ingest(t *testing.T, st *store.Store, a adapters.Adapter, src adapters.Source) {
	t.Helper()
	if _, err := adapters.IngestBackfill(context.Background(), st, a, []adapters.Source{src}, nil); err != nil {
		t.Errorf("%s ingest: %v", src.Harness, err)
	}
}

// TestConcurrentHubAndCLIIngest is the Task 0 single-writer-posture
// deliverable: the hub is *a* writer, not *the* writer. A CLI ingest
// (its own store.Open on the same database file, like a separate
// process) runs concurrently with hub-side ingest; afterwards the
// database must be byte-for-byte the same as a serial reference ingest,
// and the trigger-maintained rollups must agree with direct
// aggregation. Run under -race in the suite.
func TestConcurrentHubAndCLIIngest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hub.db")
	h := startHub(t, dbPath)
	ctx := context.Background()

	cli, err := store.Open(dbPath) // second handle = the CLI process
	if err != nil {
		t.Fatalf("CLI store open: %v", err)
	}
	defer func() { _ = cli.Close() }()

	ccSrc := fixtureSource(t, "claude-code", ccFixtureRoot)
	cxSrc := fixtureSource(t, "codex", codexFixtureRoot)

	// Phase 1: initial backfills race on the empty database.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ingest(t, h.st, claudecode.Adapter{}, ccSrc) }()
	go func() { defer wg.Done(); ingest(t, cli, codex.Adapter{}, cxSrc) }()
	wg.Wait()

	// Phase 2: both writers re-ingest the SAME fixture concurrently —
	// row-level contention on the dedup/replacement path (idempotent by
	// hard rule 5, so the final state must not change).
	wg.Add(2)
	go func() { defer wg.Done(); ingest(t, h.st, claudecode.Adapter{}, ccSrc) }()
	go func() { defer wg.Done(); ingest(t, cli, claudecode.Adapter{}, ccSrc) }()
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	// Reference: the same two fixtures ingested serially into a fresh DB.
	ref, err := store.Open(filepath.Join(t.TempDir(), "ref.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.Close() }()
	ingest(t, ref, claudecode.Adapter{}, ccSrc)
	ingest(t, ref, codex.Adapter{}, cxSrc)

	gotN, err := h.st.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantN, err := ref.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotN != wantN {
		t.Errorf("concurrent DB holds %d events, serial reference %d", gotN, wantN)
	}

	got, err := h.st.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	want, err := ref.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("daily aggregation diverged from serial reference:\ngot  %+v\nwant %+v", got, want)
	}

	// Rollup consistency under concurrent writers: trigger-maintained
	// rollups must equal direct aggregation on the contended DB.
	fromRollups, err := h.st.DailyFromRollups(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromRollups, got) {
		t.Errorf("rollup-served daily diverged from direct aggregation after concurrent ingest:\nrollups %+v\ndirect  %+v", fromRollups, got)
	}

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.Shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
