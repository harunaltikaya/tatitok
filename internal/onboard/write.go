package onboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// PlanMatcherOut / PlanEntryOut are the WRITE-side shapes for prices.json
// plan entries. They emit ONLY keys the strict loader
// (pricing.LoadOverrides, DisallowUnknownFields) accepts, so the provenance
// audit trail rides the one sanctioned notes key, "_doc". omitempty keeps
// sparse entries clean (no empty matcher fields, no $0 price field).
type PlanMatcherOut struct {
	Harness  string `json:"harness,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type PlanEntryOut struct {
	Doc             string           `json:"_doc,omitempty"`
	Name            string           `json:"name"`
	Label           string           `json:"label,omitempty"`
	Matchers        []PlanMatcherOut `json:"matchers"`
	Window          string           `json:"window"`
	WindowStart     string           `json:"window_start,omitempty"`
	MonthlyPriceUSD string           `json:"monthly_price_usd,omitempty"`
}

// MergeResult reports what MergePlans did, for the CLI summary.
type MergeResult struct {
	Path       string
	BackupPath string // "" when no prior file existed
	Created    bool
	Added      []string // plan names appended
	Replaced   []string // plan names updated in place (idempotent re-run)
	Removed    []string // plan names deleted (metered: ensure no entry)
}

// MergePlans merges entries into the prices.json at path WITHOUT clobbering:
// every existing top-level key (prices, reference_models,
// explained_divergences, _doc) is preserved verbatim, and plans are merged
// BY NAME — an entry whose name already exists is replaced in place, others
// are appended. The prior file is backed up and the new file installed
// atomically (temp + rename in the same dir); the merged result is verified
// to LOAD via pricing.LoadOverrides before it can replace anything.
//
// remove names plans to delete (the metered path: "ensure no entry for this
// harness"). A name in both entries and remove keeps the entry — adding wins.
func MergePlans(path string, entries []PlanEntryOut, remove ...string) (MergeResult, error) {
	res := MergeResult{Path: path}

	// 1) Read + sanity-check the existing file. Never merge into a file we
	//    cannot already load — that would risk overwriting a precious config
	//    we only half-understood.
	var top map[string]json.RawMessage
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if _, lerr := pricing.LoadOverrides(path); lerr != nil {
			return res, fmt.Errorf("refusing to merge: existing %s does not load cleanly (%w) — fix it first; nothing changed", path, lerr)
		}
		if uerr := json.Unmarshal(existing, &top); uerr != nil {
			return res, fmt.Errorf("existing %s is not a JSON object: %w", path, uerr)
		}
	case os.IsNotExist(err):
		res.Created = true
	default:
		return res, fmt.Errorf("read %s: %w", path, err)
	}
	if top == nil {
		top = map[string]json.RawMessage{}
	}

	// 2) Existing plans (kept verbatim) → merge by name.
	var plans []json.RawMessage
	if raw, ok := top["plans"]; ok {
		if uerr := json.Unmarshal(raw, &plans); uerr != nil {
			return res, fmt.Errorf("existing %s: \"plans\" is not an array: %w", path, uerr)
		}
	}
	nameAt := make(map[string]int, len(plans))
	for i, p := range plans {
		var nm struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(p, &nm)
		if nm.Name != "" {
			nameAt[nm.Name] = i
		}
	}
	for _, e := range entries {
		b, merr := json.Marshal(e)
		if merr != nil {
			return res, merr
		}
		if idx, ok := nameAt[e.Name]; ok {
			plans[idx] = b
			res.Replaced = append(res.Replaced, e.Name)
		} else {
			nameAt[e.Name] = len(plans)
			plans = append(plans, b)
			res.Added = append(res.Added, e.Name)
		}
	}

	// Removals (metered): drop named plans — but never one we just added (a
	// name in both entries and remove keeps the entry; adding wins).
	if len(remove) > 0 {
		keep := map[string]bool{}
		for _, e := range entries {
			keep[e.Name] = true
		}
		rm := map[string]bool{}
		for _, n := range remove {
			if !keep[n] {
				rm[n] = true
			}
		}
		if len(rm) > 0 {
			out := make([]json.RawMessage, 0, len(plans))
			for _, p := range plans {
				var nm struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(p, &nm)
				if rm[nm.Name] {
					res.Removed = append(res.Removed, nm.Name)
					continue
				}
				out = append(out, p)
			}
			plans = out
		}
	}
	plansRaw, err := json.Marshal(plans)
	if err != nil {
		return res, err
	}
	top["plans"] = plansRaw

	// 3) Render: marshal compact (map keys sort deterministically), then
	//    pretty-print. json.Indent only reflows whitespace — it never
	//    reorders or alters any value, so existing content is byte-preserved
	//    modulo indentation.
	compact, err := json.Marshal(top)
	if err != nil {
		return res, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		return res, err
	}
	pretty.WriteByte('\n')

	// 4) Write atomically via a temp file in the same dir, and verify it
	//    LOADS (the whole merged file — existing overrides + new plans)
	//    before it can replace the original.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	tmp, err := os.CreateTemp(dir, ".prices-*.json.tmp")
	if err != nil {
		return res, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // harmless no-op after a successful rename
	if _, err := tmp.Write(pretty.Bytes()); err != nil {
		_ = tmp.Close()
		return res, err
	}
	if err := tmp.Close(); err != nil {
		return res, err
	}
	if _, err := pricing.LoadOverrides(tmpName); err != nil {
		return res, fmt.Errorf("internal: generated prices.json failed to load (%w) — original left untouched", err)
	}

	// 5) Back up the prior file (if any), then atomically swap in the new one.
	if !res.Created {
		res.BackupPath = path + ".bak"
		if err := os.WriteFile(res.BackupPath, existing, 0o644); err != nil {
			return res, fmt.Errorf("back up %s: %w", path, err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return res, fmt.Errorf("install %s: %w", path, err)
	}
	return res, nil
}
