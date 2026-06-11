package store

// Explicit pricing recompute (M3 Task 2, FR-9.5 / AS-4): re-derive the
// four cost columns for every stored event under the CURRENT embedded
// snapshot + override file. The only path that ever changes a historical
// cost — ingest prices new rows, re-ingest never re-prices unchanged
// rows (cost columns are derived, outside the replacement predicate).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/pricing"
)

// PricingPlan is the `recompute --pricing` plan listing.
type PricingPlan struct {
	SnapshotVersion string `json:"snapshot_version"`
	Events          int64  `json:"events"`
	Unpriced        int64  `json:"events_never_priced"`
	OnCurrent       int64  `json:"events_on_current_snapshot"`
	OnOther         int64  `json:"events_on_other_snapshot"`
}

// PlanPricing reports the pricing provenance of the stored events.
func (s *Store) PlanPricing(ctx context.Context, snapshotVersion string) (PricingPlan, error) {
	p := PricingPlan{SnapshotVersion: snapshotVersion}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(price_snapshot IS NULL), 0),
			COALESCE(SUM(price_snapshot = ?1 OR price_snapshot = 'override'), 0)
		FROM usage_events`, snapshotVersion).
		Scan(&p.Events, &p.Unpriced, &p.OnCurrent)
	p.OnOther = p.Events - p.Unpriced - p.OnCurrent
	return p, err
}

// PricingReconRow is one (provider, model) group of the opencode
// store-and-compare lane: OUR computed cost vs the source-reported cost
// kept in meta.source_cost, both in integer micro-USD.
type PricingReconRow struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Basis is the group's cost_basis (uniform under current rules).
	Basis  string `json:"cost_basis"`
	Events int64  `json:"events"`
	// Unpriced counts events whose OUR cost is NULL (snapshot gap) —
	// reported as coverage findings, not tolerance violations.
	Unpriced    int64 `json:"events_unpriced"`
	OursMicro   int64 `json:"ours_usd_micro"`
	SourceMicro int64 `json:"source_usd_micro"`
	// EquivMicro sums the stored API-equivalent values (free basis).
	EquivMicro int64 `json:"api_equiv_usd_micro"`
}

// PricingReconciliation aggregates ours-vs-source costs per
// (provider, model) over every event carrying meta.source_cost.
// usdToMicro converts the source's decimal cost text exactly (the
// caller passes pricing.USDToMicro — the store stays float-free).
func (s *Store) PricingReconciliation(ctx context.Context, usdToMicro func(text string) (int64, error)) ([]PricingReconRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, model,
			COALESCE(cost_basis, ''), cost_usd_micro, cost_api_equiv_micro, meta
		FROM usage_events
		WHERE meta IS NOT NULL AND instr(meta, '"source_cost"') > 0
		ORDER BY provider, model`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []PricingReconRow
	for rows.Next() {
		var provider, model, basis, meta string
		var ours, equiv sql.NullInt64
		if err := rows.Scan(&provider, &model, &basis, &ours, &equiv, &meta); err != nil {
			return nil, err
		}
		var m struct {
			SourceCost json.Number `json:"source_cost"`
		}
		dec := json.NewDecoder(strings.NewReader(meta))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil || m.SourceCost == "" {
			continue // no parseable source cost on this row
		}
		src, err := usdToMicro(m.SourceCost.String())
		if err != nil {
			return nil, fmt.Errorf("source_cost %q: %w", m.SourceCost, err)
		}
		if n := len(out); n == 0 || out[n-1].Provider != provider || out[n-1].Model != model {
			out = append(out, PricingReconRow{Provider: provider, Model: model})
		}
		r := &out[len(out)-1]
		r.Events++
		r.SourceMicro += src
		if basis != "" {
			r.Basis = basis
		}
		if equiv.Valid {
			r.EquivMicro += equiv.Int64
		}
		if ours.Valid {
			r.OursMicro += ours.Int64
		} else {
			r.Unpriced++
		}
	}
	return out, rows.Err()
}

// PricingResult summarizes one pricing recompute.
type PricingResult struct {
	Repriced    int64            // rows whose cost columns were (re)stamped
	CostChanged int64            // of those, rows whose cost_usd_micro value actually changed
	Conflicts   int64            // concurrent replacements detected (skipped, then swept)
	ByBasis     map[string]int64 // post-run basis distribution of repriced rows
}

// pricingUpdate is one row's repricing: the new derived values, plus the
// pricing-relevant payload AS READ — the write re-verifies that payload
// is still in place (the --provenance verify-before-stamp pattern), so a
// concurrent ingest replacement can never be clobbered with a cost
// derived from the payload it replaced.
type pricingUpdate struct {
	id                       string
	harness, meta, reasoning any // NULL-able payload as read (nil = NULL)
	provider, model, family  string
	in, out, cw, cr          int64
	cost, equiv, rates       any
	basis, snapshot          string
	costEq                   bool
}

// pricingWriteBatch bounds each write transaction: batches keep the
// database available to concurrent writers during a long recompute.
const pricingWriteBatch = 500

// maxPricingSweeps bounds the conflict sweep; a database replaced faster
// than it can be swept is an error, never a silent partial recompute.
const maxPricingSweeps = 5

// RecomputePricing re-derives cost columns for ALL events. Token columns
// and every other payload field are untouched by construction.
//
// Concurrency: the read pass holds no lock, so a row can be replaced by
// a concurrent ingest between read and write. Each write happens in a
// per-batch immediate transaction and re-verifies the pricing-relevant
// payload before stamping; a changed row is skipped, logged and swept —
// re-read and re-priced from its current payload — so an exit code 0
// always means every cost column is consistent with the payload beside it.
func (s *Store) RecomputePricing(ctx context.Context, ov *pricing.Overrides) (PricingResult, error) {
	res := PricingResult{ByBasis: map[string]int64{}}
	var only map[string]bool // nil = all events; else the sweep work set
	for sweep := 0; ; sweep++ {
		updates, err := s.collectPricingUpdates(ctx, ov, only)
		if err != nil {
			return res, err
		}
		conflicts, err := s.applyPricingUpdates(ctx, updates, &res)
		if err != nil {
			return res, err
		}
		if len(conflicts) == 0 {
			return res, nil
		}
		res.Conflicts += int64(len(conflicts))
		if sweep+1 >= maxPricingSweeps {
			return res, fmt.Errorf(
				"recompute --pricing: %d rows still being replaced concurrently after %d sweeps — re-run when ingest is quiet",
				len(conflicts), maxPricingSweeps)
		}
		slog.Warn("recompute --pricing: rows replaced concurrently — sweeping them from their current payload",
			"rows", len(conflicts), "sweep", sweep+1)
		only = conflicts
	}
}

// collectPricingUpdates is the read pass: price every event (restricted
// to `only` when sweeping) from its CURRENT payload and keep that payload
// for the write-time re-verification. Rows already priced identically
// produce no update.
func (s *Store) collectPricingUpdates(ctx context.Context, ov *pricing.Overrides, only map[string]bool) ([]pricingUpdate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, harness,
			provider, model, model_family,
			tokens_input, tokens_output, tokens_cache_write, tokens_cache_read,
			tokens_reasoning, meta,
			cost_usd_micro, COALESCE(cost_basis,''), COALESCE(price_snapshot,''),
			COALESCE(price_rates,''), cost_api_equiv_micro
		FROM usage_events`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var updates []pricingUpdate
	for rows.Next() {
		var id, provider, model, family, oldBasis, oldSnap, oldRates string
		var harness, meta sql.NullString
		var in, out, cw, cr int64
		var reasoning, oldCost, oldEquiv sql.NullInt64
		if err := rows.Scan(&id, &harness, &provider, &model, &family,
			&in, &out, &cw, &cr, &reasoning, &meta, &oldCost, &oldBasis, &oldSnap,
			&oldRates, &oldEquiv); err != nil {
			return nil, err
		}
		if only != nil && !only[id] {
			continue
		}
		e := core.Event{
			ID: id, Harness: harness.String, Provider: provider, Model: model,
			ModelFamily: family, TokensInput: in, TokensOutput: out,
			TokensCacheWrite: cw, TokensCacheRead: cr,
		}
		if reasoning.Valid {
			v := reasoning.Int64
			e.TokensReasoning = &v
		}
		if meta.Valid {
			// meta feeds pricing detail (cache-write TTL split); a decode
			// failure only loses that refinement, never the row.
			_ = json.Unmarshal([]byte(meta.String), &e.Meta)
		}
		if err := pricing.Apply(&e, ov); err != nil {
			return nil, fmt.Errorf("price %s: %w", id, err)
		}
		newRates := string(e.PriceRates)
		costEq := (e.CostUSDMicro == nil) == !oldCost.Valid &&
			(e.CostUSDMicro == nil || *e.CostUSDMicro == oldCost.Int64)
		equivEq := (e.CostAPIEquivMicro == nil) == !oldEquiv.Valid &&
			(e.CostAPIEquivMicro == nil || *e.CostAPIEquivMicro == oldEquiv.Int64)
		if costEq && equivEq && e.CostBasis == oldBasis &&
			e.PriceSnapshot == oldSnap && newRates == oldRates {
			continue // already priced identically — nothing to write
		}
		u := pricingUpdate{
			id: id, provider: provider, model: model, family: family,
			in: in, out: out, cw: cw, cr: cr,
			harness: nullable(harness), meta: nullable(meta),
			reasoning: nullableInt(reasoning),
			basis:     e.CostBasis, snapshot: e.PriceSnapshot, costEq: costEq,
		}
		if e.CostUSDMicro != nil {
			u.cost = *e.CostUSDMicro
		}
		if e.CostAPIEquivMicro != nil {
			u.equiv = *e.CostAPIEquivMicro
		}
		if newRates != "" {
			u.rates = newRates
		}
		updates = append(updates, u)
	}
	return updates, rows.Err()
}

// applyPricingUpdates is the write pass: per-batch immediate transactions,
// each UPDATE re-verifying (NULL-safe IS) that the pricing-relevant
// payload it was derived from is still in place. A zero-row UPDATE means
// a concurrent replacement landed since the read — that id is returned
// for the sweep, never written.
func (s *Store) applyPricingUpdates(ctx context.Context, updates []pricingUpdate, res *PricingResult) (map[string]bool, error) {
	conflicts := map[string]bool{}
	for start := 0; start < len(updates); start += pricingWriteBatch {
		end := start + pricingWriteBatch
		if end > len(updates) {
			end = len(updates)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		stmt, err := tx.PrepareContext(ctx, `UPDATE usage_events SET
			cost_usd_micro=?2, cost_basis=?3, price_snapshot=?4, price_rates=?5,
			cost_api_equiv_micro=?6
			WHERE id=?1
			  AND harness IS ?7 AND provider IS ?8 AND model IS ?9
			  AND model_family IS ?10
			  AND tokens_input IS ?11 AND tokens_output IS ?12
			  AND tokens_cache_write IS ?13 AND tokens_cache_read IS ?14
			  AND tokens_reasoning IS ?15 AND meta IS ?16`)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		for _, u := range updates[start:end] {
			r, err := stmt.ExecContext(ctx, u.id, u.cost,
				nullStr(u.basis), nullStr(u.snapshot), u.rates, u.equiv,
				u.harness, u.provider, u.model, u.family,
				u.in, u.out, u.cw, u.cr, u.reasoning, u.meta)
			if err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return nil, fmt.Errorf("reprice %s: %w", u.id, err)
			}
			n, err := r.RowsAffected()
			if err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return nil, err
			}
			if n == 0 {
				conflicts[u.id] = true
				slog.Warn("recompute --pricing: row replaced since read — skipped, will sweep",
					"id", u.id)
				continue
			}
			res.Repriced++
			if !u.costEq {
				res.CostChanged++
			}
			res.ByBasis[u.basis]++
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return conflicts, nil
}

func nullable(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func nullableInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
