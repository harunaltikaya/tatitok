package parity

// `recompute --provenance` over the real claude-code fixture set (M3
// Task 0): a database stripped back to pre-provenance state must come
// back fully stamped — with the stored numbers untouched (PRD AS-4).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/adapters/claudecode"
)

func TestRecomputeProvenanceFixtures(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.Abs(filepath.Join(fixtureRoot, "gx10", "projects"))
	if err != nil {
		t.Fatal(err)
	}
	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}
	st := ingestInto(t, []adapters.Source{src})

	dailyBefore, err := st.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-provenance database: events lose adapter_version and
	// source link, sources lose machine (their source_id stays — migration
	// 5 backfills it totally).
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE usage_events SET adapter_version = NULL, source_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE sources SET machine = NULL`); err != nil {
		t.Fatal(err)
	}

	needy, err := st.NullProvenanceIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(needy) != wantClaudeRows {
		t.Fatalf("needy set: %d, want %d", len(needy), wantClaudeRows)
	}

	var sum adapters.RecomputeSummary
	if err := adapters.RecomputeProvenance(ctx, st, claudecode.Adapter{},
		[]adapters.Source{src}, needy, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Stamped != wantClaudeRows || sum.Mismatched != 0 ||
		sum.StampedNoSource != 0 || sum.FilesSkipped != 0 {
		t.Fatalf("recompute summary: %+v, want %d stamped and nothing else", sum, wantClaudeRows)
	}
	if len(needy) != 0 {
		t.Fatalf("%d events left in the work set", len(needy))
	}
	if sum.SourceMachines == 0 {
		t.Fatal("no sources row regained its machine")
	}

	// Post-run: no gaps anywhere, FK intact.
	gaps, err := st.LineageGaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gaps {
		if !g.Empty() {
			t.Fatalf("gaps remain after recompute: %+v", g)
		}
	}
	fk, err := st.DB().QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fk.Close() }()
	if fk.Next() {
		t.Fatal("foreign_key_check reported violations after recompute")
	}

	// AS-4: the recompute changed provenance columns only — every daily
	// number is identical.
	dailyAfter, err := st.Daily(ctx, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(dailyBefore) != len(dailyAfter) {
		t.Fatal("daily stats changed shape after recompute")
	}
	for i := range dailyBefore {
		if dailyBefore[i].Date != dailyAfter[i].Date ||
			dailyBefore[i].TokenSums != dailyAfter[i].TokenSums {
			t.Fatalf("day %s changed after recompute", dailyBefore[i].Date)
		}
	}

	// Idempotence: a second run finds nothing to stamp.
	needy2, err := st.NullProvenanceIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(needy2) != 0 {
		t.Fatalf("second needy set not empty: %d", len(needy2))
	}
	var sum2 adapters.RecomputeSummary
	if err := adapters.RecomputeProvenance(ctx, st, claudecode.Adapter{},
		[]adapters.Source{src}, needy2, &sum2); err != nil {
		t.Fatal(err)
	}
	if sum2.Stamped != 0 || sum2.Mismatched != 0 {
		t.Fatalf("second run stamped %d / mismatched %d, want 0/0", sum2.Stamped, sum2.Mismatched)
	}
}

// A stored event whose numbers were altered after ingest must be flagged
// and left exactly as found — recompute never "fixes" history (AS-4).
func TestRecomputeReportsTamperedRowUntouched(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.Abs(filepath.Join(fixtureRoot, "gx10", "projects"))
	if err != nil {
		t.Fatal(err)
	}
	src := adapters.Source{Harness: "claude-code", Root: root, Machine: "gx10"}
	st := ingestInto(t, []adapters.Source{src})

	var id string
	var tokens int64
	if err := st.DB().QueryRowContext(ctx, `SELECT id, tokens_input
		FROM usage_events ORDER BY id LIMIT 1`).Scan(&id, &tokens); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE usage_events SET
		adapter_version = NULL, source_id = NULL, tokens_input = tokens_input + 7
		WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	needy, err := st.NullProvenanceIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(needy) != 1 {
		t.Fatalf("needy set: %d, want 1", len(needy))
	}
	var sum adapters.RecomputeSummary
	if err := adapters.RecomputeProvenance(ctx, st, claudecode.Adapter{},
		[]adapters.Source{src}, needy, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Mismatched != 1 || sum.Stamped != 0 {
		t.Fatalf("summary: %+v, want exactly 1 mismatch and 0 stamps", sum)
	}
	if len(sum.Mismatches) != 1 {
		t.Fatalf("mismatch details: %v", sum.Mismatches)
	}

	var gotTokens int64
	var av, sid sql.NullString
	if err := st.DB().QueryRowContext(ctx, `SELECT tokens_input,
		adapter_version, source_id FROM usage_events WHERE id = ?`, id).
		Scan(&gotTokens, &av, &sid); err != nil {
		t.Fatal(err)
	}
	if gotTokens != tokens+7 || av.Valid || sid.Valid {
		t.Fatalf("tampered row was touched: tokens=%d (want %d) av=%v sid=%v",
			gotTokens, tokens+7, av, sid)
	}
}
