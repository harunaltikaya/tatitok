package store

// Verify/stamp semantics (M3 Task 0). Synthetic events are fine here:
// this tests the store's SQL behavior, not adapter parsing.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/core"
)

func TestVerifyAndStampProvenance(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	e := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 10, Output: 20})
	src := SourceInfo{Path: "/tmp/test.jsonl", Harness: "claude-code",
		Machine: "gx10", MTime: time.Now(), Size: 1, LineCount: 1, AdapterVersion: 2}
	if _, err := s.InsertBatch(ctx, []core.Event{e}, src); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-provenance database (sources rows keep their
	// source_id — migration 5 backfills those totally — but machine and
	// the event's provenance are NULL).
	if _, err := s.db.ExecContext(ctx,
		`UPDATE usage_events SET adapter_version = NULL, source_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sources SET machine = NULL`); err != nil {
		t.Fatal(err)
	}

	srcID, machineNull, found, err := s.SourceIDForPath(ctx, "/tmp/test.jsonl")
	if err != nil || !found || !machineNull {
		t.Fatalf("SourceIDForPath: id=%q machineNull=%v found=%v err=%v",
			srcID, machineNull, found, err)
	}
	if want := core.SourceID("claude-code", "/tmp/test.jsonl"); srcID != want {
		t.Fatalf("SourceIDForPath id %q, want %q", srcID, want)
	}
	if _, _, found, err := s.SourceIDForPath(ctx, "/nowhere.jsonl"); err != nil || found {
		t.Fatalf("SourceIDForPath for unknown path: found=%v err=%v", found, err)
	}
	if stamped, err := s.StampSourceMachine(ctx, "/tmp/test.jsonl", "gx10"); err != nil || !stamped {
		t.Fatalf("StampSourceMachine: stamped=%v err=%v", stamped, err)
	}
	// Second stamp is a no-op (machine no longer NULL).
	if stamped, err := s.StampSourceMachine(ctx, "/tmp/test.jsonl", "other"); err != nil || stamped {
		t.Fatalf("StampSourceMachine re-stamp: stamped=%v err=%v, want no-op", stamped, err)
	}

	v, err := s.NewVerifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()

	if got, err := v.Verify(ctx, &e); err != nil || got != VerifyIdentical {
		t.Fatalf("identical payload: %v err=%v, want VerifyIdentical", got, err)
	}
	tampered := e
	tampered.TokensInput = 99
	if got, err := v.Verify(ctx, &tampered); err != nil || got != VerifyDiffers {
		t.Fatalf("differing payload: %v err=%v, want VerifyDiffers", got, err)
	}
	ghost := event("ghost", "r9", "model-a", "s1", ts, TokenSums{})
	if got, err := v.Verify(ctx, &ghost); err != nil || got != VerifyMissing {
		t.Fatalf("unknown id: %v err=%v, want VerifyMissing", got, err)
	}

	if err := s.StampProvenance(ctx, 3,
		[]ProvenanceStamp{{ID: e.ID, SourceID: srcID}}); err != nil {
		t.Fatal(err)
	}
	var av int64
	var sid, machine string
	if err := s.db.QueryRowContext(ctx, `SELECT adapter_version, source_id
		FROM usage_events WHERE id = ?`, e.ID).Scan(&av, &sid); err != nil {
		t.Fatal(err)
	}
	if av != 3 || sid != srcID {
		t.Fatalf("stamped row: adapter_version=%d source_id=%q, want 3/%q", av, sid, srcID)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT machine FROM sources`).Scan(&machine); err != nil {
		t.Fatal(err)
	}
	if machine != "gx10" {
		t.Fatalf("sources.machine = %q, want gx10 (re-stamp must not overwrite)", machine)
	}

	// Stamping never overwrites existing provenance.
	if err := s.StampProvenance(ctx, 9,
		[]ProvenanceStamp{{ID: e.ID, SourceID: "bogus"}}); err == nil {
		var av2 int64
		var sid2 string
		if err := s.db.QueryRowContext(ctx, `SELECT adapter_version, source_id
			FROM usage_events WHERE id = ?`, e.ID).Scan(&av2, &sid2); err != nil {
			t.Fatal(err)
		}
		if av2 != 3 || sid2 != srcID {
			t.Fatalf("re-stamp overwrote provenance: %d/%q", av2, sid2)
		}
	}
}

// A stamp with an empty SourceID fills adapter_version only — the
// source_id column stays NULL (file unknown to the sources table).
func TestStampProvenanceWithoutSourceLink(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	e := event("m1", "r1", "model-a", "s1", ts, TokenSums{Input: 1})
	if _, err := s.InsertBatch(ctx, []core.Event{e}, testSource(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE usage_events SET adapter_version = NULL, source_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := s.StampProvenance(ctx, 3,
		[]ProvenanceStamp{{ID: e.ID, SourceID: ""}}); err != nil {
		t.Fatal(err)
	}
	var av int64
	var sid sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT adapter_version, source_id
		FROM usage_events WHERE id = ?`, e.ID).Scan(&av, &sid); err != nil {
		t.Fatal(err)
	}
	if av != 3 || sid.Valid {
		t.Fatalf("got adapter_version=%d source_id=%v, want 3/NULL", av, sid)
	}
}
