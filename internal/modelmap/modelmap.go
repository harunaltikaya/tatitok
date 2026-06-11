// Package modelmap is the versioned model-normalization table (M3 Task
// 1, PRD §9.1 model_family): raw model string → model_family, embedded
// as seed.json and mirrored into the store's model_map table. The raw
// model column is immutable forever; only the derived model_family uses
// this map. Unknown models pass through verbatim — never guessed.
package modelmap

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed seed.json
var seedJSON []byte

type seed struct {
	MapVersion int               `json:"map_version"`
	Families   map[string]string `json:"families"`
}

var current seed

func init() {
	if err := json.Unmarshal(seedJSON, &current); err != nil {
		panic(fmt.Sprintf("modelmap: seed.json: %v", err))
	}
	if current.MapVersion < 1 {
		panic("modelmap: seed.json map_version must be >= 1")
	}
	for model, family := range current.Families {
		if model == "" || family == "" {
			panic(fmt.Sprintf("modelmap: seed.json has empty model or family (%q -> %q)", model, family))
		}
	}
}

// Version is the embedded seed's map_version. Any change to the families
// table bumps it; ingest stamps it per event, and historical rows only
// catch up via `tatitok recompute --model-map`.
func Version() int { return current.MapVersion }

// Family normalizes one raw model string. Unknown models (including the
// empty codex pre-turn_context model and live-only strings the seed has
// not reviewed yet) pass through verbatim.
func Family(model string) string {
	if f, ok := current.Families[model]; ok {
		return f
	}
	return model
}

// Entry is one mapping row, for mirroring into the model_map table.
type Entry struct{ Model, Family string }

// Entries returns the seed's mappings sorted by model.
func Entries() []Entry {
	out := make([]Entry, 0, len(current.Families))
	for m, f := range current.Families {
		out = append(out, Entry{Model: m, Family: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
