package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
)

func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 10*time.Second)
}

type ssePacket struct {
	event string
	data  []byte
}

// readPacket parses one SSE event (terminated by a blank line).
func readPacket(br *bufio.Reader) (ssePacket, error) {
	var p ssePacket
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return p, err
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "" && p.event != "":
			return p, nil
		case strings.HasPrefix(line, "event: "):
			p.event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			p.data = []byte(line[len("data: "):])
		}
	}
}

func openStream(t *testing.T, h *Hub) (*bufio.Reader, func()) {
	t.Helper()
	resp, err := http.Get("http://" + h.Addr() + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream Content-Type %q", ct)
	}
	return bufio.NewReader(resp.Body), func() { _ = resp.Body.Close() }
}

// TestSSEIngestPass: a live watch pass arrives on the stream with
// per-harness counts and the rollup-touched days.
func TestSSEIngestPass(t *testing.T) {
	files := fixtureSessionFiles(t)
	whole := files[len(files)-1] // biggest file: guaranteed billable events

	root := t.TempDir()
	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}
	h := startWatchHub(t, []WatchTarget{{Adapter: claudecode.Adapter{}, Source: src}},
		150*time.Millisecond, time.Hour)

	br, closeStream := openStream(t, h)
	defer closeStream()

	hello, err := readPacket(br)
	if err != nil || hello.event != "hello" {
		t.Fatalf("first packet = %q (%v), want hello", hello.event, err)
	}
	var helloData struct {
		Reconnect string `json:"reconnect"`
	}
	if err := json.Unmarshal(hello.data, &helloData); err != nil || helloData.Reconnect != "refetch" {
		t.Errorf("hello must document reconnect=refetch: %s", hello.data)
	}

	// A new session file appears — the pass it triggers must stream.
	dir := filepath.Join(root, whole.project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, whole.name), whole.content, 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no ingest_pass event arrived")
		}
		p, err := readPacket(br)
		if err != nil {
			t.Fatalf("stream read: %v", err)
		}
		if p.event != "ingest_pass" {
			continue // heartbeats etc.
		}
		var pass passSummary
		if err := json.Unmarshal(p.data, &pass); err != nil {
			t.Fatalf("ingest_pass payload: %v\n%s", err, p.data)
		}
		if len(pass.Harnesses) != 1 || pass.Harnesses[0].Harness != "claude-code" {
			t.Fatalf("pass harnesses: %+v", pass.Harnesses)
		}
		if pass.Harnesses[0].New == 0 {
			t.Errorf("pass reports no new events for a fresh fixture file: %+v", pass.Harnesses[0])
		}
		if len(pass.TouchedDays) == 0 {
			t.Error("pass carries no rollup-touched days")
		}
		for _, d := range pass.TouchedDays {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				t.Errorf("touched day %q is not a UTC date", d)
			}
		}
		return
	}
}

// TestSSEHeartbeat: keep-alives flow on an otherwise idle stream.
func TestSSEHeartbeat(t *testing.T) {
	h, err := Start(Config{
		DBPath: filepath.Join(t.TempDir(), "hub.db"), Addr: "127.0.0.1:0",
		Heartbeat: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := contextWithTimeout(t)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	br, closeStream := openStream(t, h)
	defer closeStream()
	if p, err := readPacket(br); err != nil || p.event != "hello" {
		t.Fatalf("want hello first, got %q (%v)", p.event, err)
	}
	p, err := readPacket(br)
	if err != nil || p.event != "heartbeat" {
		t.Fatalf("want heartbeat on idle stream, got %q (%v)", p.event, err)
	}
}

// TestSSEShutdownWithStuckClient is the Codex M4 finding-2 adversarial
// reproduction, kept permanently: a client that opened the stream and
// then stopped reading fills its socket buffers; the handler blocks in
// Write. Shutdown must still complete within its grace — the stop
// watcher slams the write deadline, the per-write deadline bounds the
// re-arm race, and the handler releases before the server drain.
func TestSSEShutdownWithStuckClient(t *testing.T) {
	h, err := Start(Config{
		DBPath: filepath.Join(t.TempDir(), "hub.db"), Addr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}

	br, closeStream := openStream(t, h)
	defer closeStream()
	if p, err := readPacket(br); err != nil || p.event != "hello" {
		t.Fatalf("want hello, got %q (%v)", p.event, err)
	}

	// Stop reading entirely and flood until the handler is blocked in a
	// socket write (the payload total far exceeds kernel buffers).
	payload := strings.Repeat("y", 4096)
	for i := 0; i < 20_000; i++ {
		h.bcast.publish("ingest_pass", map[string]any{"i": i, "pad": payload})
	}
	time.Sleep(100 * time.Millisecond) // let the handler hit the blocked write

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	start := time.Now()
	err = h.Shutdown(ctx)
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("shutdown overran its grace with a stuck stream client (took %v)", elapsed)
	}
	if err != nil {
		t.Fatalf("shutdown: %v (took %v)", err, elapsed)
	}
	// Strictly under the grace: the deadline math says ≤ sseWriteTimeout
	// plus scheduling noise, not "just under 8s by luck".
	if elapsed > sseWriteTimeout+2*time.Second {
		t.Fatalf("shutdown took %v with a stuck client — handler did not observe stop promptly", elapsed)
	}
}

// TestSSEBackpressure: a client that stops reading must never block
// publish (the ingest path), loses events beyond its buffer, and is
// told it is stale once it drains again.
func TestSSEBackpressure(t *testing.T) {
	h, err := Start(Config{
		DBPath: filepath.Join(t.TempDir(), "hub.db"), Addr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := contextWithTimeout(t)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	br, closeStream := openStream(t, h)
	defer closeStream()
	if p, err := readPacket(br); err != nil || p.event != "hello" {
		t.Fatalf("want hello, got %q (%v)", p.event, err)
	}

	// Do NOT read. Publish a flood big enough to fill the client buffer,
	// the handler's blocked write and the kernel socket buffers. The
	// publisher (= the ingest path) must come back immediately.
	const flood = 50_000
	payload := strings.Repeat("x", 256)
	start := time.Now()
	for i := 0; i < flood; i++ {
		h.bcast.publish("ingest_pass", map[string]any{"i": i, "pad": payload})
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("publishing %d events to a stuck client took %v — ingest was blocked", flood, elapsed)
	}

	// Resume reading: a stale event must arrive (events were dropped).
	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no stale event after the flood")
		}
		p, err := readPacket(br)
		if err != nil {
			t.Fatalf("stream read: %v", err)
		}
		if p.event != "stale" {
			continue
		}
		var s struct {
			Dropped int64 `json:"dropped"`
		}
		if err := json.Unmarshal(p.data, &s); err != nil || s.Dropped == 0 {
			t.Fatalf("stale payload: %s (%v)", p.data, err)
		}
		return
	}
}
