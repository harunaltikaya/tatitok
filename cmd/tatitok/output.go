package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func formatTokens(n int64) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func printDailyTable(rows []store.DailyRow) {
	if len(rows) == 0 {
		fmt.Println("no events ingested yet — run: tatitok ingest --backfill")
		return
	}
	fmt.Printf("%-12s %15s %15s %15s %15s %12s %13s  %s\n",
		"DATE", "INPUT", "OUTPUT", "CACHE WRITE", "CACHE READ", "REASONING", "COST", "MODELS")
	var total store.TokenSums
	var totalCost, totalReasoning, totalUnpriced int64
	for _, r := range rows {
		fmt.Printf("%-12s %15s %15s %15s %15s %12s %13s  %s\n",
			r.Date, formatTokens(r.Input), formatTokens(r.Output),
			formatTokens(r.CacheWrite), formatTokens(r.CacheRead),
			formatTokens(r.Reasoning), formatCostCell(r.CostUSDMicro, r.UnpricedEvents),
			strings.Join(r.ModelsUsed, ", "))
		total.Input += r.Input
		total.Output += r.Output
		total.CacheWrite += r.CacheWrite
		total.CacheRead += r.CacheRead
		totalReasoning += r.Reasoning
		totalCost += r.CostUSDMicro
		totalUnpriced += r.UnpricedEvents
	}
	fmt.Printf("%-12s %15s %15s %15s %15s %12s %13s\n",
		"TOTAL", formatTokens(total.Input), formatTokens(total.Output),
		formatTokens(total.CacheWrite), formatTokens(total.CacheRead),
		formatTokens(totalReasoning), formatCostCell(totalCost, totalUnpriced))
	if totalUnpriced > 0 {
		fmt.Printf("(* %s events unpriced — cost is a floor; see doctor --pricing / recompute --pricing)\n",
			formatTokens(totalUnpriced))
	}
}

// formatCostCell renders micro-USD to dollars at the CLI edge; a '*'
// marks sums with unpriced events underneath (the cost is a floor).
func formatCostCell(micro, unpriced int64) string {
	cell := fmt.Sprintf("$%d.%02d", micro/1_000_000, (micro%1_000_000)/10_000)
	if unpriced > 0 {
		cell += "*"
	}
	return cell
}

func printDailyByTable(dim string, rows []store.DailyByRow) {
	if len(rows) == 0 {
		fmt.Println("no events ingested yet — run: tatitok ingest --backfill")
		return
	}
	fmt.Printf("%-12s %-34s %15s %15s %15s %15s %12s %13s\n",
		"DATE", strings.ToUpper(dim), "INPUT", "OUTPUT", "CACHE WRITE", "CACHE READ", "REASONING", "COST")
	for _, r := range rows {
		fmt.Printf("%-12s %-34s %15s %15s %15s %15s %12s %13s\n",
			r.Date, r.Key, formatTokens(r.Input), formatTokens(r.Output),
			formatTokens(r.CacheWrite), formatTokens(r.CacheRead),
			formatTokens(r.Reasoning), formatCostCell(r.CostUSDMicro, r.UnpricedEvents))
	}
}

func printSessionTable(rows []store.SessionRow) {
	if len(rows) == 0 {
		fmt.Println("no events ingested yet — run: tatitok ingest --backfill")
		return
	}
	fmt.Printf("%-12s %-12s %-38s %-12s %12s %12s %14s %14s %11s %12s  %s\n",
		"MACHINE", "HARNESS", "SESSION", "LAST", "INPUT", "OUTPUT", "CACHE WRITE", "CACHE READ", "REASONING", "COST", "PROJECT")
	for _, r := range rows {
		fmt.Printf("%-12s %-12s %-38s %-12s %12s %12s %14s %14s %11s %12s  %s\n",
			r.Machine, r.Harness, r.SessionID, r.LastActivity, formatTokens(r.Input),
			formatTokens(r.Output), formatTokens(r.CacheWrite),
			formatTokens(r.CacheRead), formatTokens(r.Reasoning),
			formatCostCell(r.CostUSDMicro, r.UnpricedEvents), r.Project)
	}
}

// printRecomputePlan renders the provenance-gap table (the recompute
// plan listing) and reports whether any selected harness has work.
func printRecomputePlan(gaps []store.LineageGapRow, sourceFilter string) bool {
	fmt.Printf("%-12s %12s %18s %16s %9s %16s\n", "HARNESS", "EVENTS",
		"MISSING VERSION", "MISSING SOURCE", "SOURCES", "MISSING MACHINE")
	work := false
	for _, g := range gaps {
		note := ""
		if sourceFilter != "" && g.Harness != sourceFilter {
			note = "  (not selected)"
		} else if !g.Empty() {
			work = true
		}
		fmt.Printf("%-12s %12s %18s %16s %9s %16s%s\n", g.Harness,
			formatTokens(g.Events), formatTokens(g.EventsNoVersion),
			formatTokens(g.EventsNoSource), formatTokens(g.Sources),
			formatTokens(g.SourcesNoMachine), note)
	}
	return work
}

// printRecomputeResults renders the run summary and the post-run state.
// Payload mismatches make the run exit non-zero: stored history disagrees
// with the current re-parse and a human must look (it is never altered).
func printRecomputeResults(st *store.Store, ctx context.Context, sum adapters.RecomputeSummary, leftover map[string]string, sourceFilter string) error {
	fmt.Printf("\nrecompute --provenance results:\n")
	fmt.Printf("  files re-read:               %s (%s lines)\n",
		formatTokens(int64(sum.Files)), formatTokens(int64(sum.Lines)))
	fmt.Printf("  events stamped:              %s (payload verified identical first)\n",
		formatTokens(int64(sum.Stamped)))
	if sum.StampedNoSource > 0 {
		fmt.Printf("  …without source link:        %s (file unknown to the sources table)\n",
			formatTokens(int64(sum.StampedNoSource)))
	}
	if sum.SourceMachines > 0 {
		fmt.Printf("  sources rows gained machine: %s\n", formatTokens(int64(sum.SourceMachines)))
	}
	if sum.FilesNotIngested > 0 {
		fmt.Printf("  files never ingested:        %s (recompute does not ingest — run: tatitok ingest --backfill)\n",
			formatTokens(int64(sum.FilesNotIngested)))
	}
	if sum.FilesSkipped > 0 {
		fmt.Printf("  files skipped (unreadable):  %s — their events keep NULL provenance\n",
			formatTokens(int64(sum.FilesSkipped)))
	}

	if len(leftover) > 0 {
		byHarness := map[string]int{}
		for _, h := range leftover {
			byHarness[h]++
		}
		for h, n := range byHarness {
			note := "source file deleted/rotated, or the current adapter no longer produces its ID"
			if sourceFilter != "" && h != sourceFilter {
				note = "harness not selected (--source)"
			}
			fmt.Printf("  WARNING %-12s %s events still missing provenance — %s\n",
				h, formatTokens(int64(n)), note)
		}
	}

	if sum.Mismatched > 0 {
		fmt.Printf("  MISMATCH: %s stored events differ from the current re-parse — NOT altered, NOT stamped:\n",
			formatTokens(int64(sum.Mismatched)))
		for _, m := range sum.Mismatches {
			fmt.Printf("    %s\n", m)
		}
		if sum.Mismatched > len(sum.Mismatches) {
			fmt.Printf("    … and %d more (see log)\n", sum.Mismatched-len(sum.Mismatches))
		}
		return exitError{code: 1, msg: fmt.Sprintf(
			"recompute: %d payload mismatches need investigation (stored history was not modified)",
			sum.Mismatched)}
	}

	gaps, err := st.LineageGaps(ctx)
	if err != nil {
		return err
	}
	fmt.Println("\npost-run state:")
	printRecomputePlan(gaps, sourceFilter)
	return nil
}

// doctorProvenance lists stored row counts by adapter@version — the
// queryable record of which format-handling version produced what, for
// future recompute decisions.
func doctorProvenance(ctx context.Context, st *store.Store, asJSON bool) error {
	rows, err := st.Provenance(ctx)
	if err != nil {
		return err
	}
	emptyModel, err := st.CountEmptyModel(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(map[string]any{
			"provenance":         rows,
			"empty_model_events": emptyModel,
		})
	}
	if len(rows) == 0 {
		fmt.Println("no events ingested yet — run: tatitok ingest --backfill")
		return nil
	}
	fmt.Printf("%-24s %15s %14s\n", "ADAPTER@VERSION", "EVENTS", "SOURCE FILES")
	for _, r := range rows {
		version := "pre-provenance"
		if r.AdapterVersion != nil {
			version = fmt.Sprintf("v%d", *r.AdapterVersion)
		}
		fmt.Printf("%-24s %15s %14s\n",
			fmt.Sprintf("%s@%s", r.Harness, version),
			formatTokens(r.Events), formatTokens(r.SourceFiles))
	}
	if emptyModel > 0 {
		// Legal but worth seeing: codex usage logged before the first
		// turn_context carries no model (pricing cannot attribute these).
		fmt.Printf("events with empty model: %s\n", formatTokens(emptyModel))
	}
	return nil
}

// pricingTolerance is the documented reconciliation tolerance: a
// (provider, model) group passes when |ours − source| is within 1% of
// the source total or within 1000 micro-USD ($0.001) absolute —
// whichever is larger. Small enough to catch a wrong rate or a wrong
// component mapping, large enough to absorb the per-event rounding the
// two implementations do independently.
func pricingTolerance(sourceMicro int64) int64 {
	tol := sourceMicro / 100
	if tol < 0 {
		tol = -tol
	}
	if tol < 1000 {
		tol = 1000
	}
	return tol
}

func formatMicroUSD(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	return fmt.Sprintf("%s$%d.%06d", sign, micro/1_000_000, micro%1_000_000)
}

// doctorPricing is the opencode store-and-compare lane (milestone-3
// Task 2): OUR computed cost vs the source-reported cost, per
// (provider, model). Deltas are a report, never an auto-correction;
// out-of-tolerance groups where both sides are priced → exit 1, unless
// the owner recorded the divergence as explained (M4 Task 5).
func doctorPricing(ctx context.Context, st *store.Store, ov *pricing.Overrides) error {
	rows, err := st.PricingReconciliation(ctx, pricing.USDToMicro)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no events carry a source-reported cost — ingest an opencode store first")
		return nil
	}
	fmt.Printf("%-22s %-30s %-10s %8s %14s %14s %14s  %s\n",
		"PROVIDER", "MODEL", "BASIS", "EVENTS", "OURS", "SOURCE", "DELTA", "VERDICT")
	violations, gaps, explained := 0, 0, 0
	for _, r := range rows {
		delta := r.OursMicro - r.SourceMicro
		verdict := "ok"
		switch {
		case r.Basis == "free":
			// Owner ruling 2026-06-11: free groups (source billed exactly
			// $0) are informational, never findings; the stored
			// API-equivalent is the comparison value of interest.
			verdict = fmt.Sprintf("free — API-equivalent %s stored", formatMicroUSD(r.EquivMicro))
		case r.Unpriced > 0:
			verdict = fmt.Sprintf("COVERAGE GAP — %d events unpriced (model missing from snapshot/overrides)", r.Unpriced)
			gaps++
		case delta > pricingTolerance(r.SourceMicro) || -delta > pricingTolerance(r.SourceMicro):
			// M4 Task 5 (the dead-check ruling, gpt-5-nano precedent): a
			// divergence the owner investigated and ruled expected reports
			// with its written reason at exit 0 — a permanently failing
			// check is a dead check. Anything unexplained still fails.
			if reason, ok := ov.ExplainedDivergence(r.Provider, r.Model); ok {
				verdict = "EXPLAINED DIVERGENCE — " + reason
				explained++
			} else {
				verdict = "OUT OF TOLERANCE (>1% and >$0.001)"
				violations++
			}
		}
		fmt.Printf("%-22s %-30s %-10s %8s %14s %14s %14s  %s\n",
			r.Provider, r.Model, r.Basis, formatTokens(r.Events),
			formatMicroUSD(r.OursMicro), formatMicroUSD(r.SourceMicro),
			formatMicroUSD(delta), verdict)
	}
	if explained > 0 {
		fmt.Printf("\n%d explained divergence(s) reported informationally (prices.json explained_divergences) — stored costs untouched; remove the entry to re-arm the check\n", explained)
	}
	if gaps > 0 {
		fmt.Printf("\n%d coverage gap(s): the source priced models our snapshot cannot — add override-file entries or refresh the snapshot, then run: tatitok recompute --pricing\n", gaps)
	}
	if violations > 0 {
		return exitError{code: 1, msg: fmt.Sprintf(
			"pricing reconciliation: %d (provider, model) group(s) out of tolerance — investigate rates/component mapping (stored costs were NOT modified)",
			violations)}
	}
	return nil
}

// doctorScanContent re-checks every stored raw/meta blob against the
// sanitizer invariants and greps for caller-supplied literals (e.g. known
// real content strings the owner checks privately). Findings → exit 1.
func doctorScanContent(ctx context.Context, st *store.Store, literals []string) error {
	events, findings := 0, 0
	report := func(id, what string) {
		findings++
		fmt.Printf("FINDING %s: %s\n", id, what)
	}
	err := st.ForEachRaw(ctx, func(id string, raw, meta []byte) error {
		events++
		for _, blob := range [][]byte{raw, meta} {
			if len(blob) == 0 {
				continue
			}
			fs, err := core.CheckRawSanitized(blob)
			if err != nil {
				report(id, err.Error())
				continue
			}
			for _, f := range fs {
				report(id, f)
			}
			for _, lit := range literals {
				if strings.Contains(string(blob), lit) {
					report(id, fmt.Sprintf("literal %q found in stored record", lit))
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if findings > 0 {
		return exitError{code: 1, msg: fmt.Sprintf(
			"scan-content: %d findings across %d events — content leaked into the DB",
			findings, events)}
	}
	fmt.Printf("scan-content: clean — %d events checked, no prompt/response text stored\n", events)
	return nil
}
