package agy

// Tests run against the committed, redacted fixture (real hook lines,
// cwd/ids aliased; see testdata/fixtures/agy/gx10/MANIFEST.json for the
// lines that were reordered, repeated, truncated or number-edited to
// cover the cases the young real log has not produced yet).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
)

const fixtureBase = "../../../testdata/fixtures/agy/gx10"

const (
	convA = "10385f9e-9a43-1516-4b28-f4c0817f30aa" // 2 lines: 0/0, then 19790/12
	convB = "9080b85b-47fe-5bc8-0b4e-c789c449222a" // the rest
)

func fixtureSource(t *testing.T) adapters.Source {
	t.Helper()
	root, err := filepath.Abs(fixtureBase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, logFileName)); err != nil {
		t.Fatalf("fixture missing: %v", err)
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
	probe := func(env map[string]string, home string) adapters.Probe {
		return adapters.Probe{Getenv: func(k string) string { return env[k] }, HomeDir: home, Machine: "gx10"}
	}
	t.Run("XDG_DATA_HOME", func(t *testing.T) {
		xdg := t.TempDir()
		root := filepath.Join(xdg, "tatitok", "agy")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		srcs, err := (Adapter{}).Detect(probe(map[string]string{"XDG_DATA_HOME": xdg}, "/nonexistent"))
		if err != nil || len(srcs) != 1 || srcs[0].Root != root || srcs[0].Harness != harnessName || srcs[0].Machine != "gx10" {
			t.Fatalf("got %+v, %v", srcs, err)
		}
	})
	t.Run("default under home", func(t *testing.T) {
		home := t.TempDir()
		root := filepath.Join(home, ".local", "share", "tatitok", "agy")
		if err := os.MkdirAll(root, 0o700); err != nil {
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
	t.Run("empty root: hook installed, nothing logged", func(t *testing.T) {
		sink := &collectSink{t: t}
		if err := (Adapter{}).Backfill(context.Background(), adapters.Source{Root: t.TempDir()}, sink); err != nil || len(sink.results) != 0 {
			t.Fatalf("got %+v, %v", sink.results, err)
		}
	})
	if matchLog("/x/tatitok/agy/statusline.jsonl") == "" || matchLog("/x/tatitok/agy/state.json") != "" {
		t.Fatal("watch match")
	}
}

func TestBackfillFixture(t *testing.T) {
	sink := &collectSink{t: t}
	if err := (Adapter{}).Backfill(context.Background(), fixtureSource(t), sink); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// 9 lines: A 0/0 (nothing), B first (event), A second (event), B
	// increase (event), B repeated (nothing), B decrease (reset, nothing),
	// then three B increases.
	if len(sink.results) != 1 {
		t.Fatalf("results: %+v", sink.results)
	}
	res := sink.results[0]
	if res.LineCount != 9 || res.ParseErrors != 0 || res.IncompleteTail || res.ReadError != "" || res.Events != 6 {
		t.Fatalf("file result: %+v", res)
	}
	if len(sink.events) != 6 {
		t.Fatalf("emitted %d events", len(sink.events))
	}
	type want struct {
		conv    string
		in, out int64
		reset   bool
	}
	wants := []want{
		{convB, 22840, 102, false}, // first line of B: a delta from 0
		{convA, 19790, 12, false},  // A's baseline is independent of B's
		{convB, 194, 73, false},    // 23034-22840 / 175-102
		{convB, 16912, 261, true},  // after the 12000/100 reset: 28912-12000 / 361-100
		{convB, 2825, 78, false},
		{convB, 2748, 269, false},
	}
	for i, w := range wants {
		e := sink.events[i]
		if e.SessionID != w.conv || e.TokensInput != w.in || e.TokensOutput != w.out {
			t.Fatalf("event %d: got %s %d/%d, want %s %d/%d", i, e.SessionID, e.TokensInput, e.TokensOutput, w.conv, w.in, w.out)
		}
		if _, ok := e.Meta["baseline_reset"]; ok != w.reset {
			t.Fatalf("event %d: baseline_reset=%v, want %v", i, ok, w.reset)
		}
		if e.TokensCacheRead != 0 || e.TokensCacheWrite != 0 || e.TokensReasoning != nil {
			t.Fatalf("event %d: cache/reasoning must be 0/nil: %+v", i, e)
		}
	}

	// Identity + mapping, read from the first event.
	e := sink.events[0]
	if e.Harness != harnessName || e.Provider != "google" || e.Model != "gemini-3.8-flash" || e.ModelFamily != e.Model {
		t.Fatalf("identity: %+v", e)
	}
	if e.Meta["effort"] != "low" || e.Meta["label"] != "Gemini 3.8 Flash (Low)" || e.Meta["thinking"] != nil {
		t.Fatalf("meta: %v", e.Meta)
	}
	cu, _ := e.Meta["current_usage"].(map[string]any)
	if cu == nil || cu["input_tokens"] != float64(16781) || cu["output_tokens"] != float64(102) {
		t.Fatalf("current_usage: %v", e.Meta["current_usage"])
	}
	if e.Project != "/home/user/project-e7766d" || e.RequestID != "" || e.Accuracy != core.AccuracyDerived || e.Machine != "gx10" {
		t.Fatalf("project/accuracy: %+v", e)
	}
	if e.TS.Format("2006-01-02T15:04:05.000Z") != "2026-09-03T15:56:03.527Z" {
		t.Fatalf("ts: %v", e.TS)
	}
	if e.ID != core.EventID(harnessName, convB, "22840/102") {
		t.Fatalf("id: %q", e.ID)
	}
	// Re-ingesting yields the same IDs (deterministic from the totals).
	again := &collectSink{t: t}
	if err := (Adapter{}).BackfillFile(context.Background(), fixtureSource(t), filepath.Join(fixtureSource(t).Root, logFileName), again); err != nil {
		t.Fatal(err)
	}
	for i := range sink.events {
		if again.events[i].ID != sink.events[i].ID {
			t.Fatalf("id drift at %d", i)
		}
	}

	for i := range sink.events {
		e := &sink.events[i]
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event %d: %v", i, err)
		}
		if findings, err := core.CheckRawSanitized(e.Raw); err != nil || len(findings) != 0 {
			t.Fatalf("raw carries content: %v %v", findings, err)
		}
		// No quota (limits data, fed separately) and no email anywhere in
		// raw or meta.
		var raw map[string]any
		if err := json.Unmarshal(e.Raw, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["quota"]; ok {
			t.Fatalf("quota leaked into raw: %v", raw)
		}
		for _, blob := range [][]byte{e.Raw, mustJSON(t, e.Meta)} {
			if s := strings.ToLower(string(blob)); strings.Contains(s, "quota") || strings.Contains(s, "email") || strings.Contains(s, "@") {
				t.Fatalf("event %d carries quota/email: %s", i, blob)
			}
		}
		if raw["context_window"] == nil || raw["model"] == nil || raw["logged_at"] == nil {
			t.Fatalf("raw lost structure: %v", raw)
		}
	}
}

// A short-written line (the hook's by-design failure: a partial line the
// hook terminates with "\n") is a parse error contained to that line —
// never an abort, events before and after it intact. Built at run time
// from a byte prefix of a fixture line (committed fixtures must parse).
func TestShortWrittenLine(t *testing.T) {
	src := fixtureSource(t)
	data, err := os.ReadFile(filepath.Join(src.Root, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 9 {
		t.Fatalf("fixture has %d lines", len(lines))
	}
	short := lines[6][:57] // a prefix of the 28912/361 line
	withShort := append(append(append([]string{}, lines[:6]...), short), lines[6:]...)

	root := t.TempDir()
	path := filepath.Join(root, logFileName)
	if err := os.WriteFile(path, []byte(strings.Join(withShort, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{t: t}
	if err := (Adapter{}).Backfill(context.Background(), adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}, sink); err != nil {
		t.Fatal(err)
	}
	res := sink.results[0]
	if res.LineCount != 10 || res.ParseErrors != 1 || res.IncompleteTail || res.Events != 6 || len(sink.events) != 6 {
		t.Fatalf("short line not contained: %+v (%d events)", res, len(sink.events))
	}
	// The event after the short line still reads the reset baseline.
	if e := sink.events[3]; e.TokensInput != 16912 || e.TokensOutput != 261 || e.Meta["baseline_reset"] != true {
		t.Fatalf("event after short line: %+v", e)
	}

	// The same prefix as an UNTERMINATED final line is a write in
	// progress: incomplete tail, not a parse error.
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"+short), 0o600); err != nil {
		t.Fatal(err)
	}
	sink = &collectSink{t: t}
	if err := (Adapter{}).Backfill(context.Background(), adapters.Source{Harness: harnessName, Root: root, Machine: "gx10"}, sink); err != nil {
		t.Fatal(err)
	}
	if res := sink.results[0]; res.LineCount != 10 || res.ParseErrors != 0 || !res.IncompleteTail || res.Events != 6 {
		t.Fatalf("tail: %+v", res)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEmailStrippedDefensively(t *testing.T) {
	line := []byte(`{"conversation_id":"c1","logged_at":"2026-09-03T10:00:00.000Z","cwd":"/p","email":"someone@example.com","model":{"display_name":"GPT-OSS 120B"},"context_window":{"total_input_tokens":10,"total_output_tokens":2,"current_usage":{"email":"x@y.z","input_tokens":10}},"quota":{"3p-5h":{"remaining_fraction":1}}}`)
	ev, ok, err := parseLine(line, adapters.Source{Machine: "m"}, map[string]*baseline{})
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	for _, blob := range [][]byte{ev.Raw, mustJSON(t, ev.Meta)} {
		if s := string(blob); strings.Contains(s, "email") || strings.Contains(s, "@") || strings.Contains(s, "quota") {
			t.Fatalf("leak: %s", s)
		}
	}
	if ev.Model != "gpt-oss-120b" || ev.Provider != "google" {
		t.Fatalf("model: %+v", ev)
	}
}

func TestSlug(t *testing.T) {
	cases := []struct {
		label, model, effort string
		thinking             bool
	}{
		{"Gemini 3.8 Flash (Low)", "gemini-3.8-flash", "low", false},
		{"Gemini 3.8 Flash", "gemini-3.8-flash", "", false},
		{"Gemini 3.8 Pro (High) (Thinking)", "gemini-3.8-pro", "high", true},
		{"Gemini 3.8 Pro (Thinking)", "gemini-3.8-pro", "", true},
		{"Claude Sonnet 4.6", "claude-sonnet-4-6", "", false},
		{"Claude Opus 4.8 (Medium)", "claude-opus-4-8", "medium", false},
		{"GPT-OSS 120B", "gpt-oss-120b", "", false},
		{"  Some  New Model (Preview) ", "some-new-model-preview", "", false}, // unknown label still slugs
		{"", "", "", false},
	}
	for _, c := range cases {
		m, e, th := Slug(c.label)
		if m != c.model || e != c.effort || th != c.thinking {
			t.Errorf("Slug(%q) = %q/%q/%v, want %q/%q/%v", c.label, m, e, th, c.model, c.effort, c.thinking)
		}
	}
}
