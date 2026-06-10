package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Deterministic event IDs (milestone-1 Task 2).
//
// Primary form: sha256(harness | native message id | request id), truncated
// to 16 bytes, lowercase hex (32 chars). Fields are joined with a NUL byte
// so ("a","bc") and ("ab","c") cannot collide; native ids are treated as
// opaque strings (real logs contain UUID-shaped message ids on <synthetic>
// records, so no msg_* shape may be assumed).
//
// The primary form is only used when BOTH native ids are present. This
// matches ccusage's dedup rule: it collapses duplicates by message id +
// request id, and applies no dedup at all when either id is missing. A
// record missing either id therefore gets the fallback ID, which is unique
// per physical occurrence (file basename | line index | ts) — re-ingesting
// the same file stays idempotent, while distinct occurrences never collapse.

const idLen = 16 // bytes of sha256 kept; hex-encoded to 32 chars

func hashID(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
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

// FallbackID returns the deterministic ID for a record missing a native
// message id or request id: sha256(harness | file basename | line index |
// ts), same truncation. lineIndex is the 0-based line number within the
// source file; ts is the record's raw timestamp string.
func FallbackID(harness, fileBase string, lineIndex int, ts string) string {
	return hashID(harness, fileBase, strconv.Itoa(lineIndex), ts)
}
