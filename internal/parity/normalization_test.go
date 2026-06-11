package parity

// Ingest-time model normalization over the real opencode fixture set
// (M3 Task 1): the fixture models include a billing-tier variant and a
// local serving recipe, so the normalized families diverge from the raw
// models exactly where the seed says so — and the raw model column stays
// verbatim.

import (
	"context"
	"testing"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/opencode"
	"github.com/harunaltikaya/tatitok/internal/modelmap"
)

func TestIngestNormalizesModelFamilies(t *testing.T) {
	ctx := context.Background()
	root := opencodeRoot(t, "../../testdata/fixtures/opencode/gx10")
	st := ingestIntoWith(t, opencode.Adapter{}, []adapters.Source{{
		Harness: "opencode", Root: root, Machine: "gx10",
	}})

	rows, err := st.DB().QueryContext(ctx, `SELECT model, model_family,
		COALESCE(map_version, 0) FROM usage_events
		GROUP BY model, model_family, map_version ORDER BY model`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var model, family string
		var mv int64
		if err := rows.Scan(&model, &family, &mv); err != nil {
			t.Fatal(err)
		}
		if prev, dup := got[model]; dup {
			t.Errorf("model %q has two families (%q, %q) — normalization must be deterministic",
				model, prev, family)
		}
		got[model] = family
		if mv != int64(modelmap.Version()) {
			t.Errorf("model %q stamped map_version %d, want %d", model, mv, modelmap.Version())
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for model, family := range got {
		if want := modelmap.Family(model); family != want {
			t.Errorf("model %q normalized to %q, want %q", model, family, want)
		}
	}
	// The interesting divergences must actually be exercised by the
	// fixture set, not vacuously absent.
	if got["deepseek-v4-flash-free"] != "deepseek-v4-flash" {
		t.Errorf("billing-tier fold missing: %q", got["deepseek-v4-flash-free"])
	}
	if got["qwen3.6-35b-nvfp4-tecnigmaai"] != "qwen3.6-35b" {
		t.Errorf("recipe strip missing: %q", got["qwen3.6-35b-nvfp4-tecnigmaai"])
	}
}
