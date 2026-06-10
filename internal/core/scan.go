package core

import (
	"encoding/json"
	"fmt"
)

// CheckRawSanitized verifies the sanitizer invariants on one stored raw
// record (the `doctor --scan-content` core): every content-key value must
// be a placeholder, and no unknown free-text string longer than the
// sanitizer cutoff may survive. Returns one human-readable finding per
// violation; empty means clean.
func CheckRawSanitized(raw []byte) ([]string, error) {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, fmt.Errorf("raw not JSON: %w", err)
	}
	var findings []string
	checkSanitizedValue(node, "", &findings)
	return findings, nil
}

func checkSanitizedValue(node any, key string, findings *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			checkSanitizedValue(val, k, findings)
		}
	case []any:
		for _, val := range v {
			checkSanitizedValue(val, key, findings)
		}
	case string:
		if IsPlaceholder(v) {
			return
		}
		if contentKeys[key] || stringContentKeys[key] {
			*findings = append(*findings,
				fmt.Sprintf("content key %q holds non-placeholder text (len=%d)", key, len(v)))
			return
		}
		if len(v) > maxFreeLen && !safeKeys[key] && !isPathlike(v) && !isIDShaped(v) {
			*findings = append(*findings,
				fmt.Sprintf("long free text under key %q (len=%d)", key, len(v)))
		}
	}
}
