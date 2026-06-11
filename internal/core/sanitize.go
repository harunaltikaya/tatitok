package core

// SanitizeRaw is the Go twin of scripts/harvest_fixtures.py's sanitizer:
// it prepares a source record for the Event.Raw column by replacing every
// content-bearing field with a <stripped len=N sha256=FIRST12HEX>
// placeholder while preserving structure, ids, timestamps, models, usage
// objects and unknown fields (hard rules 6 and 8).
//
// The content rules — which keys are stripped, the conservative byte
// cutoff, the placeholder format — come from ONE machine-readable spec,
// sanitize_rules.json, embedded here and read by the harvest script; the
// two implementations are held byte-equal over the contract vectors in
// testdata/sanitizer-vectors/ (see sanitize_vectors_test.go).
//
// Differences from the harvest script are deliberate and OUTSIDE the
// shared spec tables: the script also pseudonymizes ids and aliases paths
// because its output is PUBLISHED as committed fixtures, and it salts the
// placeholder hash; the DB is local-first, so ids and paths stay verbatim
// and the content hash is unsalted.

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// builtForSpecVersion is the sanitize_rules.json spec_version this
// implementation was written against (asserted at init; the harvest
// script and the leak checker pin their own copies).
const builtForSpecVersion = 1

//go:embed sanitize_rules.json
var sanitizeRulesJSON []byte

// SanitizeRulesJSON exposes the embedded rule-spec bytes (diagnostics and
// tests; the Python harvest script reads the same file from the repo).
func SanitizeRulesJSON() []byte { return sanitizeRulesJSON }

type sanitizeRules struct {
	SpecVersion       int      `json:"spec_version"`
	ContentKeys       []string `json:"content_keys"`
	StringContentKeys []string `json:"string_content_keys"`
	SafeKeys          []string `json:"safe_keys"`
	MaxFreeLen        int      `json:"max_free_len"`
	Placeholder       struct {
		Format       string `json:"format"`
		HashHexChars int    `json:"hash_hex_chars"`
		Regex        string `json:"regex"`
	} `json:"placeholder"`
	IDShapes struct {
		UUIDRegex        string   `json:"uuid_regex"`
		PrefixIDPrefixes []string `json:"prefix_id_prefixes"`
		PrefixIDBody     string   `json:"prefix_id_body_regex"`
	} `json:"id_shapes"`
}

// Rule tables, populated from the embedded spec at init. contentKeys
// strips any value type; stringContentKeys strips strings only (list/
// dict values keep structure and are recursed into); safeKeys passes
// strings at any length; unknown strings longer than maxFreeLen bytes
// are stripped.
var (
	contentKeys       map[string]bool
	stringContentKeys map[string]bool
	safeKeys          map[string]bool
	maxFreeLen        int
	hashHexChars      int

	placeholderRe *regexp.Regexp
	uuidRe        *regexp.Regexp
	prefixIDRe    *regexp.Regexp
)

func init() {
	var r sanitizeRules
	if err := json.Unmarshal(sanitizeRulesJSON, &r); err != nil {
		panic(fmt.Sprintf("sanitize_rules.json: %v", err))
	}
	// Built for exactly ONE spec_version: sanitizing under semantics this
	// code was not written against could leak content while looking green.
	if r.SpecVersion != builtForSpecVersion {
		panic(fmt.Sprintf("sanitize_rules.json: this sanitizer is built for spec_version %d but the embedded spec is %d — update sanitize.go for the new rule-spec semantics",
			builtForSpecVersion, r.SpecVersion))
	}
	toSet := func(keys []string) map[string]bool {
		m := make(map[string]bool, len(keys))
		for _, k := range keys {
			m[k] = true
		}
		return m
	}
	contentKeys = toSet(r.ContentKeys)
	stringContentKeys = toSet(r.StringContentKeys)
	safeKeys = toSet(r.SafeKeys)
	maxFreeLen = r.MaxFreeLen
	hashHexChars = r.Placeholder.HashHexChars
	placeholderRe = regexp.MustCompile(r.Placeholder.Regex)
	uuidRe = regexp.MustCompile(r.IDShapes.UUIDRegex)
	prefixIDRe = regexp.MustCompile(
		"^(?:" + strings.Join(r.IDShapes.PrefixIDPrefixes, "|") + ")_" +
			r.IDShapes.PrefixIDBody + "$")
	if maxFreeLen <= 0 || hashHexChars <= 0 || hashHexChars%2 != 0 {
		panic("sanitize_rules.json: bad max_free_len or hash_hex_chars")
	}
}

// Placeholder returns the stripped-content marker for v: a string is
// hashed as its UTF-8 bytes, anything else as its canonical JSON encoding
// per the spec (sorted keys, compact, no HTML escaping). The DB-side hash
// is unsalted (the harvest sanitizer salts; see the spec doc).
func Placeholder(v any) string {
	var raw []byte
	if s, ok := v.(string); ok {
		raw = []byte(s)
	} else {
		b, err := canonicalJSON(v)
		if err != nil {
			b = []byte(fmt.Sprintf("%v", v))
		}
		raw = b
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("<stripped len=%d sha256=%x>", len(raw), digest[:hashHexChars/2])
}

// canonicalJSON encodes v per the spec's nonstring_encoding: sorted object
// keys (json.Marshal's map order), compact, raw UTF-8, no HTML escaping.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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
		switch node.(type) {
		case json.Number, bool, nil:
			// numbers/booleans/null cannot carry content; generic key
			// names collide across formats (opencode tokens.input is a
			// NUMBER under claude-code's tool-input key name) — see the
			// spec's content_keys_doc
			return node
		default:
			return Placeholder(node)
		}
	}
	// string-content keys ("content") strip a string, but a list/dict (the
	// message.content block array) keeps its structure and is recursed into.
	if stringContentKeys[key] {
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
