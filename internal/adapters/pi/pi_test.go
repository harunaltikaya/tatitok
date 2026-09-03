package pi

// Tests run against the committed, redacted fixture (one real session,
// content replaced by placeholders — hard rule 1: no fabricated lines).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
)

const fixtureBase = "../../../testdata/fixtures/pi/gx10"

// Measured from the committed fixture: 9 records, 2 assistant messages
// with usage (one text-only, one with tool calls).
const wantEmitted = 2

func fixtureSource(t *testing.T) adapters.Source {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(fixtureBase, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture root missing: %v", err)
	}
	return adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}
}

type collectSink struct {
	t       *testing.T
	events  []core.Event
	results []adapters.FileResult
	open    string
}

func (c *collectSink) FileStart(path string) error {
	if c.open != "" {
		c.t.Fatalf("FileStart for %s while %s is still open", path, c.open)
	}
	c.open = path
	return nil
}

func (c *collectSink) EmitBatch(path string, events []core.Event) error {
	if len(events) == 0 || len(events) > adapters.BatchSize || c.open != path {
		c.t.Fatalf("bad batch of %d for %s (open %q)", len(events), path, c.open)
	}
	c.events = append(c.events, events...)
	return nil
}

func (c *collectSink) FileDone(res adapters.FileResult) error {
	if c.open != res.Path {
		c.t.Fatalf("FileDone for %s outside its bracket (open %q)", res.Path, c.open)
	}
	c.open = ""
	c.results = append(c.results, res)
	return nil
}

func TestDetect(t *testing.T) {
	abs, _ := filepath.Abs(fixtureBase)
	probe := func(env map[string]string, home string) adapters.Probe {
		return adapters.Probe{
			Getenv:  func(k string) string { return env[k] },
			HomeDir: home,
			Machine: "gx10",
		}
	}

	t.Run("agent dir override", func(t *testing.T) {
		srcs, err := (Adapter{}).Detect(probe(map[string]string{"PI_CODING_AGENT_DIR": abs}, "/nonexistent"))
		if err != nil {
			t.Fatal(err)
		}
		want := []adapters.Source{{Harness: harnessName, Root: filepath.Join(abs, "sessions"), Machine: "gx10"}}
		if !reflect.DeepEqual(srcs, want) {
			t.Fatalf("got %+v, want %+v", srcs, want)
		}
	})

	t.Run("session dir override wins", func(t *testing.T) {
		dir := filepath.Join(abs, "sessions")
		srcs, err := (Adapter{}).Detect(probe(map[string]string{
			"PI_CODING_AGENT_DIR": "/nonexistent", "PI_CODING_AGENT_SESSION_DIR": dir}, "/nonexistent"))
		if err != nil || len(srcs) != 1 || srcs[0].Root != dir {
			t.Fatalf("got %+v, %v", srcs, err)
		}
	})

	t.Run("default", func(t *testing.T) {
		home := t.TempDir()
		root := filepath.Join(home, ".pi", "agent", "sessions")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		srcs, err := (Adapter{}).Detect(probe(nil, home))
		if err != nil || len(srcs) != 1 || srcs[0].Root != root {
			t.Fatalf("got %+v, %v", srcs, err)
		}
	})

	t.Run("nothing found", func(t *testing.T) {
		srcs, err := (Adapter{}).Detect(probe(nil, t.TempDir()))
		if err != nil || len(srcs) != 0 {
			t.Fatalf("got %+v, %v", srcs, err)
		}
	})
}

func TestBackfillFixture(t *testing.T) {
	sink := &collectSink{t: t}
	if err := (Adapter{}).Backfill(context.Background(), fixtureSource(t), sink); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(sink.events) != wantEmitted {
		t.Fatalf("emitted %d events, want %d", len(sink.events), wantEmitted)
	}
	if len(sink.results) != 1 || sink.results[0].ParseErrors != 0 ||
		sink.results[0].ReadError != "" || sink.results[0].IncompleteTail ||
		sink.results[0].Events != wantEmitted || sink.results[0].LineCount != 9 {
		t.Fatalf("unhealthy file result: %+v", sink.results)
	}

	// First assistant turn (text-only) — values read straight from the
	// fixture line.
	e := sink.events[0]
	if e.TokensInput != 7092 || e.TokensOutput != 70 ||
		e.TokensCacheRead != 0 || e.TokensCacheWrite != 0 {
		t.Fatalf("token fields: %+v", e)
	}
	if e.TokensReasoning == nil || *e.TokensReasoning != 23 {
		t.Fatalf("reasoning: %v", e.TokensReasoning)
	}
	if e.Provider != "vllm-flash-next-blazux" || e.Model != "qwen3.8-flash-next" ||
		e.ModelFamily != e.Model {
		t.Fatalf("provider/model: %q %q", e.Provider, e.Model)
	}
	if e.RequestID != "chatcmpl-b4245d52c9b528e3" {
		t.Fatalf("request id: %q", e.RequestID)
	}
	if e.Meta["api"] != "openai-completions" {
		t.Fatalf("meta: %v", e.Meta)
	}
	if e.SessionID != "d3247fed-1592-cb48-a24d-768d195bd181" ||
		e.Project != "/home/user/project-a23e25" {
		t.Fatalf("session/project: %q %q", e.SessionID, e.Project)
	}
	if e.TS.Format("2006-01-02T15:04:05.000Z") != "2026-08-28T01:08:28.816Z" {
		t.Fatalf("ts: %v", e.TS)
	}
	if e.ID != core.EventID(harnessName, "f05ab431", e.SessionID) {
		t.Fatalf("id: %q", e.ID)
	}

	// Second turn carries tool calls; its reasoning is present too.
	if e2 := sink.events[1]; e2.TokensInput != 7370 || e2.TokensOutput != 269 ||
		e2.TokensReasoning == nil || *e2.TokensReasoning != 150 {
		t.Fatalf("second turn: %+v", e2)
	}

	for i := range sink.events {
		e := &sink.events[i]
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		if e.Accuracy != core.AccuracyExact || e.Harness != harnessName || e.Machine != "gx10" {
			t.Fatalf("identity fields: %+v", e)
		}
		if findings, err := core.CheckRawSanitized(e.Raw); err != nil || len(findings) != 0 {
			t.Fatalf("raw carries content: %v %v", findings, err)
		}
		// No text/thinking/tool-argument value survives as anything but
		// a placeholder.
		var raw map[string]any
		if err := json.Unmarshal(e.Raw, &raw); err != nil {
			t.Fatal(err)
		}
		walk(t, raw, "")
	}
}

func walk(t *testing.T, node any, key string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			walk(t, val, k)
		}
	case []any:
		for _, val := range v {
			walk(t, val, key)
		}
	case string:
		if (key == "text" || key == "thinking" || key == "arguments") && !core.IsPlaceholder(v) {
			t.Fatalf("content under %q leaked into raw: %q", key, v)
		}
	}
}
