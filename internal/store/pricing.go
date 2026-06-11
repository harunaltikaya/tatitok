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
	ByBasis     map[string]int64 // post-run basis distribution of repriced rows
}

// RecomputePricing re-derives cost columns for ALL events. Token columns
// and every other payload field are untouched by construction.
func (s *Store) RecomputePricing(ctx context.Context, ov *pricing.Overrides) (PricingResult, error) {
	res := PricingResult{ByBasis: map[string]int64{}}

	type update struct {
		id       string
		cost     any
		basis    string
		snapshot string
		rates    any
		equiv    any
		costEq   bool
	}
	var updates []update

	// Read pass first (collected, then applied — no UPDATE under an open
	// cursor on the same connection).
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(harness,''),
			provider, model, model_family,
			tokens_input, tokens_output, tokens_cache_write, tokens_cache_read,
			tokens_reasoning, COALESCE(meta,''),
			cost_usd_micro, COALESCE(cost_basis,''), COALESCE(price_snapshot,''),
			COALESCE(price_rates,''), cost_api_equiv_micro
		FROM usage_events`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id, harness, provider, model, family, meta, oldBasis, oldSnap, oldRates string
		var in, out, cw, cr int64
		var reasoning sql.NullInt64
		var oldCost, oldEquiv sql.NullInt64
		if err := rows.Scan(&id, &harness, &provider, &model, &family,
			&in, &out, &cw, &cr, &reasoning, &meta, &oldCost, &oldBasis, &oldSnap,
			&oldRates, &oldEquiv); err != nil {
			_ = rows.Close()
			return res, err
		}
		e := core.Event{
			ID: id, Harness: harness, Provider: provider, Model: model,
			ModelFamily: family, TokensInput: in, TokensOutput: out,
			TokensCacheWrite: cw, TokensCacheRead: cr,
		}
		if reasoning.Valid {
			v := reasoning.Int64
			e.TokensReasoning = &v
		}
		if meta != "" {
			// meta feeds pricing detail (cache-write TTL split); a decode
			// failure only loses that refinement, never the row.
			_ = json.Unmarshal([]byte(meta), &e.Meta)
		}
		if err := pricing.Apply(&e, ov); err != nil {
			_ = rows.Close()
			return res, fmt.Errorf("price %s: %w", id, err)
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
		u := update{id: id, basis: e.CostBasis, snapshot: e.PriceSnapshot, costEq: costEq}
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
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return res, err
	}
	_ = rows.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE usage_events SET
		cost_usd_micro=?2, cost_basis=?3, price_snapshot=?4, price_rates=?5,
		cost_api_equiv_micro=?6
		WHERE id=?1`)
	if err != nil {
		_ = tx.Rollback()
		return res, err
	}
	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.id, u.cost,
			nullStr(u.basis), nullStr(u.snapshot), u.rates, u.equiv); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return res, fmt.Errorf("reprice %s: %w", u.id, err)
		}
		res.Repriced++
		if !u.costEq {
			res.CostChanged++
		}
		res.ByBasis[u.basis]++
	}
	_ = stmt.Close()
	return res, tx.Commit()
}
