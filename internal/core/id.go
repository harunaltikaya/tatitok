package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Deterministic event IDs (milestone-1 Task 2; hardened in M1.1).
//
// Primary form: sha256 over the length-prefixed components (harness,
// native message id, request id), truncated to 16 bytes, lowercase hex
// (32 chars). Each component is encoded as "<decimal byte length>:" +
// bytes, so no choice of component contents — including embedded NUL or
// ':' bytes — can make two different component tuples produce the same
// preimage. Native ids are treated as opaque strings (real logs contain
// UUID-shaped message ids on <synthetic> records, so no msg_* shape may
// be assumed).
//
// The primary form is only used when BOTH native ids are present. This
// matches ccusage's dedup rule: it collapses duplicates by message id +
// request id, and applies no dedup at all when either id is missing. A
// record missing either id therefore gets the fallback ID, which is unique
// per physical occurrence (source-relative file path | line index | ts) —
// re-ingesting the same file stays idempotent, while distinct occurrences
// never collapse.

const idLen = 16 // bytes of sha256 kept; hex-encoded to 32 chars

func hashID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:idLen])
}

// EventID returns the deterministic ID for a record carrying both native
// ids. messageID and requestID must be non-empty; callers with a missing
// id must use FallbackID instead (see dedup rationale above).
func EventID(harness, messageID, requestID string) string {
	return hashID(harness, messageID, requestID)
}

// SourceID returns the deterministic ID of one ingested source file:
// sha256 over the length-prefixed components ("src", harness, absolute
// path), truncated like event IDs. The "src" domain prefix keeps source
// IDs out of the event-ID preimage space. Machine is deliberately NOT a
// component: the ID must stay stable across a hostname change (machine is
// its own column on the sources table), and within one database a path is
// already unique per harness.
func SourceID(harness, path string) string {
	return hashID("src", harness, path)
}

// FallbackID returns the deterministic ID for a record missing a native
// message id or request id: sha256 over the length-prefixed components
// (harness, fileRel, line index, ts), same truncation. fileRel is the
// SOURCE-RELATIVE path of the log file — for claude-code the project dir
// plus basename, '/'-joined — so identical session filenames in different
// project dirs can never collide, while the ID stays stable when the log
// root moves between machines. lineIndex is the 0-based line number
// within the source file; ts is the record's raw timestamp string.
func FallbackID(harness, fileRel string, lineIndex int, ts string) string {
	return hashID(harness, fileRel, strconv.Itoa(lineIndex), ts)
}
