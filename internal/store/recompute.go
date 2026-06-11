package store

// Store support for `tatitok recompute --provenance` (M3 Task 0):
// querying what is missing provenance, verification probes, and the
// final provenance stamps. Recompute re-stamps provenance columns ONLY —
// adapter_version and source lineage. It never alters a payload: a
// stored row whose payload differs from the current re-parse is
// reported, not corrected (AS-4 — historical numbers never change
// silently).

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// LineageGapRow is one harness's provenance health: how many events and
// sources still miss adapter_version, source link or machine.
type LineageGapRow struct {
	Harness          string `json:"harness"`
	Events           int64  `json:"events"`
	EventsNoVersion  int64  `json:"events_missing_adapter_version"`
	EventsNoSource   int64  `json:"events_missing_source_link"`
	Sources          int64  `json:"source_files"`
	SourcesNoMachine int64  `json:"sources_missing_machine"`
}

// Empty reports whether the row needs no recompute work.
func (r LineageGapRow) Empty() bool {
	return r.EventsNoVersion == 0 && r.EventsNoSource == 0 && r.SourcesNoMachine == 0
}

// LineageGaps aggregates provenance gaps per harness — the recompute
// plan listing.
func (s *Store) LineageGaps(ctx context.Context) ([]LineageGapRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT harness,
			SUM(events), SUM(ev_nov), SUM(ev_nos), SUM(files), SUM(f_nom)
		FROM (
			SELECT COALESCE(harness,'') AS harness, COUNT(*) AS events,
			       COALESCE(SUM(adapter_version IS NULL),0) AS ev_nov,
			       COALESCE(SUM(source_id IS NULL),0) AS ev_nos,
			       0 AS files, 0 AS f_nom
			FROM usage_events GROUP BY 1
			UNION ALL
			SELECT harness, 0, 0, 0, COUNT(*),
			       COALESCE(SUM(machine IS NULL),0)
			FROM sources GROUP BY 1
		)
		GROUP BY harness ORDER BY harness`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LineageGapRow
	for rows.Next() {
		var r LineageGapRow
		if err := rows.Scan(&r.Harness, &r.Events, &r.EventsNoVersion,
			&r.EventsNoSource, &r.Sources, &r.SourcesNoMachine); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NullProvenanceIDs returns id → harness for every stored event missing
// adapter_version or a source link — the recompute work set.
func (s *Store) NullProvenanceIDs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(harness,'')
		FROM usage_events
		WHERE adapter_version IS NULL OR source_id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}

// VerifyOutcome is one re-parsed occurrence's verification result.
type VerifyOutcome int

const (
	// VerifyMissing: no stored row with this ID.
	VerifyMissing VerifyOutcome = iota
	// VerifyDiffers: a stored row exists but its payload differs from this
	// re-parsed occurrence.
	VerifyDiffers
	// VerifyIdentical: the stored payload matches this occurrence exactly.
	VerifyIdentical
)

// Verifier is a prepared read-only probe comparing a re-parsed event
// against the stored row with the same ID. The predicate is the
// replacement path's NULL-safe payload difference test, reused verbatim
// as a SELECT — "identical" means exactly what idempotent re-ingest
// means.
type Verifier struct{ stmt *sql.Stmt }

// NewVerifier prepares the probe; callers must Close it.
func (s *Store) NewVerifier(ctx context.Context) (*Verifier, error) {
	// model_family (?8) is deliberately absent: it is derived via the
	// model map, not payload — a stored row normalized under an older map
	// still verifies (recompute --model-map is the explicit catch-up).
	stmt, err := s.db.PrepareContext(ctx, `SELECT (
			ts IS NOT ?2 OR machine IS NOT ?3 OR source_kind IS NOT ?4 OR
			harness IS NOT ?5 OR provider IS NOT ?6 OR model IS NOT ?7 OR
			project IS NOT ?9 OR
			session_id IS NOT ?10 OR request_id IS NOT ?11 OR
			tokens_input IS NOT ?12 OR tokens_output IS NOT ?13 OR
			tokens_cache_write IS NOT ?14 OR tokens_cache_read IS NOT ?15 OR
			tokens_reasoning IS NOT ?16 OR accuracy IS NOT ?17 OR
			meta IS NOT ?18 OR raw IS NOT ?19
		) FROM usage_events WHERE id = ?1`)
	if err != nil {
		return nil, err
	}
	return &Verifier{stmt: stmt}, nil
}

// Verify compares e against the stored row with e.ID.
func (v *Verifier) Verify(ctx context.Context, e *core.Event) (VerifyOutcome, error) {
	args, err := eventArgs(e)
	if err != nil {
		return VerifyMissing, err
	}
	var differs bool
	switch err := v.stmt.QueryRowContext(ctx, args...).Scan(&differs); {
	case err == sql.ErrNoRows:
		return VerifyMissing, nil
	case err != nil:
		return VerifyMissing, fmt.Errorf("verify %s: %w", e.ID, err)
	}
	if differs {
		return VerifyDiffers, nil
	}
	return VerifyIdentical, nil
}

// Close releases the prepared probe.
func (v *Verifier) Close() error { return v.stmt.Close() }

// SourceIDForPath finds the recorded sources row for path, returning its
// stable source_id and whether its machine column is still NULL.
func (s *Store) SourceIDForPath(ctx context.Context, path string) (sourceID string, machineNull, found bool, err error) {
	var machine sql.NullString
	switch err := s.db.QueryRowContext(ctx,
		`SELECT source_id, machine FROM sources WHERE path = ?`, path).
		Scan(&sourceID, &machine); {
	case err == sql.ErrNoRows:
		return "", false, false, nil
	case err != nil:
		return "", false, false, err
	}
	return sourceID, !machine.Valid, true, nil
}

// StampSourceMachine fills a NULL machine on the path's sources row.
func (s *Store) StampSourceMachine(ctx context.Context, path, machine string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sources SET machine = ?2 WHERE path = ?1 AND machine IS NULL`,
		path, machine)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ProvenanceStamp is one verified event's provenance fill: which source
// row it came from (SourceID may be empty when the file is unknown to
// the sources table — then only adapter_version is stamped).
type ProvenanceStamp struct {
	ID       string
	SourceID string
}

// ModelMapPlan is the `recompute --model-map` plan: how many events sit
// on an older (or NULL) map_version and how many model_family values
// would actually change under the current map.
type ModelMapPlan struct {
	CurrentVersion int   `json:"current_map_version"`
	Events         int64 `json:"events"`
	Stale          int64 `json:"events_not_on_current_version"`
	FamilyChanges  int64 `json:"model_family_changes"`
}

// currentFamilyExpr resolves a row's model through the model_map table
// with verbatim passthrough — the same rule modelmap.Family applies.
const currentFamilyExpr = `COALESCE(
	(SELECT m.model_family FROM model_map m WHERE m.model = usage_events.model),
	usage_events.model)`

// PlanModelMap reports what RecomputeModelMap would do.
func (s *Store) PlanModelMap(ctx context.Context, currentVersion int) (ModelMapPlan, error) {
	p := ModelMapPlan{CurrentVersion: currentVersion}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(map_version IS NOT ?1), 0),
			COALESCE(SUM(model_family IS NOT `+currentFamilyExpr+`), 0)
		FROM usage_events`, currentVersion).
		Scan(&p.Events, &p.Stale, &p.FamilyChanges)
	return p, err
}

// RecomputeModelMap re-normalizes every stored event's model_family under
// the current model map and stamps map_version — the ONLY path that ever
// changes a historical model_family (explicit, logged; AS-4). The raw
// model column is untouched by construction.
//
// model_family feeds price resolution (family-key snapshot and override
// lookups), so every event whose family changed is REPRICED before this
// recompute exits, inside the same immediate transaction — the invariant
// is that any recompute exiting 0 leaves the derived columns mutually
// consistent, whatever order the recomputes run in (M3.1 finding 2).
// Returns how many rows were re-stamped, how many family values actually
// changed, and how many of those were repriced.
func (s *Store) RecomputeModelMap(ctx context.Context, currentVersion int, ov *pricing.Overrides) (restamped, changed, repriced int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	fail := func(err error) (int64, int64, int64, error) {
		_ = tx.Rollback()
		return 0, 0, 0, err
	}
	// The ids whose family is about to change — the repricing work set.
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM usage_events WHERE model_family IS NOT `+currentFamilyExpr)
	if err != nil {
		return fail(fmt.Errorf("plan family changes: %w", err))
	}
	changedIDs := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fail(err)
		}
		changedIDs[id] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fail(err)
	}
	_ = rows.Close()

	res, err := tx.ExecContext(ctx, `UPDATE usage_events SET
			model_family = `+currentFamilyExpr+`
		WHERE model_family IS NOT `+currentFamilyExpr, // counts real changes
	)
	if err != nil {
		return fail(fmt.Errorf("recompute model_family: %w", err))
	}
	if changed, err = res.RowsAffected(); err != nil {
		return fail(err)
	}
	res, err = tx.ExecContext(ctx, `UPDATE usage_events SET map_version = ?1
		WHERE map_version IS NOT ?1`, currentVersion)
	if err != nil {
		return fail(fmt.Errorf("stamp map_version: %w", err))
	}
	if restamped, err = res.RowsAffected(); err != nil {
		return fail(err)
	}

	// Reprice the changed-family rows from their post-update state. Same
	// transaction: the immediate lock means no replacement can interleave,
	// so a zero-row reprice here is an internal error, not a conflict.
	if len(changedIDs) > 0 {
		updates, err := collectPricingUpdates(ctx, tx, ov, changedIDs)
		if err != nil {
			return fail(err)
		}
		stmt, err := tx.PrepareContext(ctx, repriceSQL)
		if err != nil {
			return fail(err)
		}
		for _, u := range updates {
			r, err := stmt.ExecContext(ctx, u.args()...)
			if err != nil {
				_ = stmt.Close()
				return fail(fmt.Errorf("reprice %s: %w", u.id, err))
			}
			n, err := r.RowsAffected()
			if err != nil {
				_ = stmt.Close()
				return fail(err)
			}
			if n == 0 {
				_ = stmt.Close()
				return fail(fmt.Errorf("reprice %s: row changed inside the transaction (driver invariant broken)", u.id))
			}
			repriced++
		}
		_ = stmt.Close()
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return restamped, changed, repriced, nil
}

// StampProvenance fills NULL provenance columns on the given verified
// events in one transaction. Existing non-NULL provenance is never
// overwritten, and payloads are untouched by construction.
func (s *Store) StampProvenance(ctx context.Context, version int, stamps []ProvenanceStamp) error {
	if len(stamps) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE usage_events SET
			adapter_version = COALESCE(adapter_version, ?2),
			source_id = COALESCE(source_id, ?3)
		WHERE id = ?1`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, st := range stamps {
		if _, err := stmt.ExecContext(ctx, st.ID,
			nullVersion(version), nullStr(st.SourceID)); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("stamp %s: %w", st.ID, err)
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}
