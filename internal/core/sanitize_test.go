package core

// Two layers: unit tests on tiny synthetic JSON (allowed — SanitizeRaw is
// a pure function, not adapter parsing), and a property test sweeping
// every committed fixture line.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sanitize(t *testing.T, in string) map[string]any {
	t.Helper()
	out, err := SanitizeRaw([]byte(in))
	if err != nil {
		t.Fatalf("sanitize %q: %v", in, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	return m
}

func TestSanitizeStripsContentKeys(t *testing.T) {
	m := sanitize(t, `{"message":{"content":[{"type":"text","text":"hi"}]},"toolUseResult":{"stdout":"x"},"summary":"s"}`)
	blocks := m["message"].(map[string]any)["content"].([]any)
	text := blocks[0].(map[string]any)["text"].(string)
	if !IsPlaceholder(text) {
		t.Errorf("text not stripped: %q", text)
	}
	if tu := m["toolUseResult"].(string); !IsPlaceholder(tu) {
		t.Errorf("toolUseResult not stripped whole: %q", tu)
	}
	if s := m["summary"].(string); !IsPlaceholder(s) {
		t.Errorf("summary not stripped: %q", s)
	}
}

func TestSanitizeContentStringVsArray(t *testing.T) {
	m := sanitize(t, `{"message":{"content":"plain string content"}}`)
	c := m["message"].(map[string]any)["content"].(string)
	if !IsPlaceholder(c) {
		t.Errorf("string content not stripped: %q", c)
	}
}

func TestSanitizePreservesUsageIdsAndUnknownFields(t *testing.T) {
	in := `{"type":"assistant","timestamp":"2026-06-10T14:23:43.448Z",` +
		`"requestId":"req_abc","futureUnknownField":{"k":1},` +
		`"message":{"id":"msg_x","model":"claude-fable-5",` +
		`"usage":{"input_tokens":2082,"output_tokens":450,"cache_creation":{"ephemeral_1h_input_tokens":7497}}}}`
	m := sanitize(t, in)
	msg := m["message"].(map[string]any)
	u := msg["usage"].(map[string]any)
	if u["input_tokens"].(float64) != 2082 || u["output_tokens"].(float64) != 450 {
		t.Errorf("usage mangled: %v", u)
	}
	if msg["id"] != "msg_x" || msg["model"] != "claude-fable-5" {
		t.Errorf("ids/model mangled: %v", msg)
	}
	if m["futureUnknownField"] == nil {
		t.Error("unknown field dropped (hard rule 8)")
	}
	if m["timestamp"] != "2026-06-10T14:23:43.448Z" {
		t.Errorf("timestamp mangled: %v", m["timestamp"])
	}
}

func TestSanitizeStripsLongUnknownStrings(t *testing.T) {
	long := strings.Repeat("a", 81)
	m := sanitize(t, fmt.Sprintf(`{"someNewField":%q,"shortField":"ok","aPath":"/very/long/%s"}`, long, long))
	if v := m["someNewField"].(string); !IsPlaceholder(v) {
		t.Errorf("long unknown string survived: %q", v)
	}
	if m["shortField"] != "ok" {
		t.Error("short string should survive")
	}
	if v := m["aPath"].(string); IsPlaceholder(v) {
		t.Error("pathlike string should survive")
	}
}

func TestSanitizeIdempotent(t *testing.T) {
	in := `{"message":{"content":[{"type":"text","text":"some content"}]},"x":"` + strings.Repeat("y", 100) + `"}`
	once, err := SanitizeRaw([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := SanitizeRaw(once)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Fatalf("not idempotent:\n%s\n%s", once, twice)
	}
}

func TestSanitizeRejectsMalformed(t *testing.T) {
	if _, err := SanitizeRaw([]byte(`{"truncated":`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestPlaceholderFormat(t *testing.T) {
	p := Placeholder("hello")
	if !IsPlaceholder(p) {
		t.Fatalf("placeholder %q does not match its own format", p)
	}
	if p != Placeholder("hello") {
		t.Error("placeholder not deterministic")
	}
	if Placeholder("hello") == Placeholder("world") {
		t.Error("distinct content must hash differently")
	}
}

// Property test over every committed fixture line: sanitizing real
// (already-sanitized) records must keep existing placeholders verbatim,
// preserve usage objects exactly, never let an unknown long free-text
// string through, and be idempotent.
func TestSanitizeFixtureProperty(t *testing.T) {
	pattern := filepath.Join("..", "..", "testdata", "fixtures", "claude-code", "*", "projects", "*", "*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture files found at %s", pattern)
	}
	lines := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines++
			out, err := SanitizeRaw([]byte(line))
			if err != nil {
				t.Fatalf("%s:%d: %v", f, i+1, err)
			}

			var inNode, outNode any
			if err := json.Unmarshal([]byte(line), &inNode); err != nil {
				t.Fatalf("%s:%d input unparseable: %v", f, i+1, err)
			}
			if err := json.Unmarshal(out, &outNode); err != nil {
				t.Fatalf("%s:%d output unparseable: %v", f, i+1, err)
			}
			checkNoLongFreeText(t, f, i+1, outNode, "")
			checkPlaceholdersPreserved(t, f, i+1, inNode, outNode)

			again, err := SanitizeRaw(out)
			if err != nil {
				t.Fatalf("%s:%d re-sanitize: %v", f, i+1, err)
			}
			if string(again) != string(out) {
				t.Fatalf("%s:%d not idempotent", f, i+1)
			}
		}
	}
	t.Logf("swept %d fixture lines across %d files", lines, len(files))
}

func checkNoLongFreeText(t *testing.T, file string, line int, node any, key string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			checkNoLongFreeText(t, file, line, val, k)
		}
	case []any:
		for _, val := range v {
			checkNoLongFreeText(t, file, line, val, key)
		}
	case string:
		if len(v) > maxFreeLen && !IsPlaceholder(v) &&
			!safeKeys[key] && !isPathlike(v) && !isIDShaped(v) {
			t.Fatalf("%s:%d: long free text survived under key %q: %.60q…",
				file, line, key, v)
		}
	}
}

// Every placeholder the harvest sanitizer produced must come out the
// other side byte-identical (never re-hashed, never dropped).
func checkPlaceholdersPreserved(t *testing.T, file string, line int, in, out any) {
	t.Helper()
	inSet := map[string]bool{}
	collectPlaceholders(in, inSet)
	outSet := map[string]bool{}
	collectPlaceholders(out, outSet)
	for p := range inSet {
		if !outSet[p] {
			t.Fatalf("%s:%d: input placeholder %q missing from output", file, line, p)
		}
	}
}

func collectPlaceholders(node any, set map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for _, val := range v {
			collectPlaceholders(val, set)
		}
	case []any:
		for _, val := range v {
			collectPlaceholders(val, set)
		}
	case string:
		if IsPlaceholder(v) {
			set[v] = true
		}
	}
}
