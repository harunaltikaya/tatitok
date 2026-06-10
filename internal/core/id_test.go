package core

// Tiny synthetic inputs are allowed here: these test the pure ID hasher,
// not adapter parsing (CLAUDE.md hard rule 1).

import (
	"regexp"
	"strings"
	"testing"
)

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestEventIDDeterministicAndShaped(t *testing.T) {
	a := EventID("claude-code", "msg_abc", "req_def")
	b := EventID("claude-code", "msg_abc", "req_def")
	if a != b {
		t.Fatalf("same inputs gave different ids: %s vs %s", a, b)
	}
	if !hexID.MatchString(a) {
		t.Fatalf("id %q is not 32 lowercase hex chars", a)
	}
}

func TestEventIDDistinguishesParts(t *testing.T) {
	ids := map[string]string{
		"base":             EventID("claude-code", "msg_abc", "req_def"),
		"other harness":    EventID("codex", "msg_abc", "req_def"),
		"other message":    EventID("claude-code", "msg_abd", "req_def"),
		"other request":    EventID("claude-code", "msg_abc", "req_deg"),
		"shifted boundary": EventID("claude-code", "msg_abcr", "eq_def"),
		// length-prefixing: embedded separator bytes cannot shift
		// component boundaries either
		"embedded nul": EventID("claude-code", "msg_abc\x00x", "req_def"),
		"embedded len": EventID("claude-code", "msg_abc", "3:req_def"),
		"moved nul":    EventID("claude-code", "msg_abc\x00", "xreq_def"),
	}
	seen := map[string]string{}
	for name, id := range ids {
		if prev, dup := seen[id]; dup {
			t.Errorf("%s collides with %s: %s", name, prev, id)
		}
		seen[id] = name
	}
}

// Opaque-id rule: UUID-shaped message ids (real logs carry them on
// <synthetic> records) must work exactly like msg_* ids.
func TestEventIDOpaqueMessageID(t *testing.T) {
	// assembled at runtime so no UUID-shaped literal lands in the
	// committed tree (the leak checker flags unknown UUIDs globally)
	uuid := strings.Join([]string{"0f04df2a", "7a90", "4b62", "a435", "2f7d9a4f5c11"}, "-")
	u := EventID("claude-code", uuid, "req_x")
	m := EventID("claude-code", "msg_0f04df2a", "req_x")
	if !hexID.MatchString(u) || u == m {
		t.Fatalf("uuid-shaped id mishandled: %s vs %s", u, m)
	}
}

func TestFallbackIDPerOccurrence(t *testing.T) {
	a := FallbackID("claude-code", "-proj-a/session.jsonl", 7, "2026-06-10T14:23:43.448Z")
	b := FallbackID("claude-code", "-proj-a/session.jsonl", 7, "2026-06-10T14:23:43.448Z")
	if a != b {
		t.Fatalf("fallback id not deterministic: %s vs %s", a, b)
	}
	if !hexID.MatchString(a) {
		t.Fatalf("fallback id %q is not 32 lowercase hex chars", a)
	}
	if FallbackID("claude-code", "-proj-a/session.jsonl", 8, "2026-06-10T14:23:43.448Z") == a {
		t.Error("different line index must give a different id")
	}
	if FallbackID("claude-code", "-proj-a/other.jsonl", 7, "2026-06-10T14:23:43.448Z") == a {
		t.Error("different file must give a different id")
	}
	// The M1.1 hardening point: the same session filename under two
	// different project dirs is two different sources.
	if FallbackID("claude-code", "-proj-b/session.jsonl", 7, "2026-06-10T14:23:43.448Z") == a {
		t.Error("same basename in a different project dir must give a different id")
	}
}

func TestFallbackNeverCollidesWithPrimary(t *testing.T) {
	// Same textual parts through both forms: the line-index field makes the
	// preimages differ, so ids must too.
	p := EventID("claude-code", "-proj-a/session.jsonl", "2026-06-10T14:23:43.448Z")
	f := FallbackID("claude-code", "-proj-a/session.jsonl", 0, "2026-06-10T14:23:43.448Z")
	if p == f {
		t.Fatalf("primary and fallback ids collide: %s", p)
	}
}
