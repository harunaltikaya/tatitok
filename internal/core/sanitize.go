package core

// SanitizeRaw is the Go twin of scripts/harvest_fixtures.py's sanitizer:
// it prepares a source record for the Event.Raw column by replacing every
// content-bearing field with a <stripped len=N sha256=FIRST12HEX>
// placeholder while preserving structure, ids, timestamps, models, usage
// objects and unknown fields (hard rules 6 and 8).
//
// Differences from the harvest script are deliberate: the script also
// pseudonymizes ids and aliases paths because its output is PUBLISHED as
// committed fixtures; the DB is local-first, so ids and paths stay
// verbatim and the content hash is unsalted. The content rules — which
// keys are stripped, the >80-byte conservative cutoff, the placeholder
// format — must stay in lockstep with the script (mirror its CONTENT_KEYS
// / SAFE_KEYS when new free-text fields are discovered in real logs).

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// contentKeys: values that are content by definition — always stripped,
// any length. Mirror of the harvest script's CONTENT_KEYS.
var contentKeys = map[string]bool{
	"text": true, "thinking": true, "summary": true, "input": true,
	"attachments": true, "toolUseResult": true, "signature": true,
	"prompt": true, "stdout": true, "stderr": true,
	// short content-bearing fields that slip under the 80-byte rule
	"aiTitle": true, "lastPrompt": true, "subject": true, "description": true,
}

// safeKeys: string values that are known-safe metadata at any length.
// Mirror of the harvest script's SAFE_KEYS.
var safeKeys = map[string]bool{
	"id": true, "uuid": true, "parentUuid": true, "leafUuid": true,
	"sessionId": true, "requestId": true, "request_id": true,
	"message_id": true, "messageId": true, "promptId": true,
	"timestamp": true, "type": true, "subtype": true, "role": true,
	"model": true, "version": true, "cwd": true, "gitBranch": true,
	"userType": true, "name": true, "tool_use_id": true, "toolUseID": true,
	"stop_reason": true, "stopReason": true, "stop_sequence": true,
	"service_tier": true, "slug": true, "entrypoint": true,
	"permissionMode": true, "promptSource": true,
}

// maxFreeLen: unknown string fields longer than this are stripped.
const maxFreeLen = 80

var (
	placeholderRe = regexp.MustCompile(`^<stripped len=\d+ sha256=[0-9a-f]{12}>$`)
	uuidRe        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	prefixIDRe    = regexp.MustCompile(`^(?:msg|req|toolu)_[A-Za-z0-9]+$`)
)

// Placeholder returns the stripped-content marker for v: a string is
// hashed as its UTF-8 bytes, anything else as its compact JSON encoding.
func Placeholder(v any) string {
	var raw []byte
	if s, ok := v.(string); ok {
		raw = []byte(s)
	} else {
		b, err := json.Marshal(v)
		if err != nil {
			b = []byte(fmt.Sprintf("%v", v))
		}
		raw = b
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("<stripped len=%d sha256=%x>", len(raw), digest[:6])
}

// IsPlaceholder reports whether s is already a stripped-content marker.
// Placeholders pass through SanitizeRaw unchanged, which makes the
// sanitizer idempotent (and keeps fixture placeholders verbatim).
func IsPlaceholder(s string) bool { return placeholderRe.MatchString(s) }

func isPathlike(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return true
	}
	if strings.HasPrefix(s, ".") && strings.Contains(s, "/") {
		return true
	}
	return strings.Contains(s, "/") &&
		!strings.ContainsAny(s, " \n\t")
}

func isIDShaped(s string) bool {
	return uuidRe.MatchString(s) || prefixIDRe.MatchString(s)
}

// SanitizeRaw parses one source JSON record and returns its sanitized
// form. Numbers round-trip verbatim (json.Number); object keys come back
// sorted (Go map marshaling), which is a deliberate determinism property
// for golden tests. A parse failure is returned as an error — the caller
// contains it per source file, never aborts the run.
func SanitizeRaw(line []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var node any
	if err := dec.Decode(&node); err != nil {
		return nil, fmt.Errorf("sanitize: %w", err)
	}
	return json.Marshal(sanitizeValue(node, ""))
}

func sanitizeValue(node any, key string) any {
	// Already-stripped markers stay verbatim, even under content keys.
	if s, ok := node.(string); ok && IsPlaceholder(s) {
		return s
	}
	if contentKeys[key] {
		return Placeholder(node)
	}
	// "content" is special: a string is stripped, but the message.content
	// block array keeps its structure and is recursed into.
	if key == "content" {
		if s, ok := node.(string); ok {
			return Placeholder(s)
		}
	}
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = sanitizeValue(val, k)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = sanitizeValue(val, key)
		}
		return out
	case string:
		if safeKeys[key] || isIDShaped(v) || isPathlike(v) ||
			len(v) <= maxFreeLen {
			return v
		}
		return Placeholder(v)
	default: // json.Number, bool, nil
		return node
	}
}
