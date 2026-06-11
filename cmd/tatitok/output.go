package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/harunaltikaya/tatitok/internal/core"
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
	fmt.Printf("%-12s %15s %15s %15s %15s  %s\n",
		"DATE", "INPUT", "OUTPUT", "CACHE WRITE", "CACHE READ", "MODELS")
	var total store.TokenSums
	for _, r := range rows {
		fmt.Printf("%-12s %15s %15s %15s %15s  %s\n",
			r.Date, formatTokens(r.Input), formatTokens(r.Output),
			formatTokens(r.CacheWrite), formatTokens(r.CacheRead),
			strings.Join(r.ModelsUsed, ", "))
		total.Input += r.Input
		total.Output += r.Output
		total.CacheWrite += r.CacheWrite
		total.CacheRead += r.CacheRead
	}
	fmt.Printf("%-12s %15s %15s %15s %15s\n",
		"TOTAL", formatTokens(total.Input), formatTokens(total.Output),
		formatTokens(total.CacheWrite), formatTokens(total.CacheRead))
}

func printSessionTable(rows []store.SessionRow) {
	if len(rows) == 0 {
		fmt.Println("no events ingested yet — run: tatitok ingest --backfill")
		return
	}
	fmt.Printf("%-12s %-38s %-12s %12s %12s %14s %14s  %s\n",
		"HARNESS", "SESSION", "LAST", "INPUT", "OUTPUT", "CACHE WRITE", "CACHE READ", "PROJECT")
	for _, r := range rows {
		fmt.Printf("%-12s %-38s %-12s %12s %12s %14s %14s  %s\n",
			r.Harness, r.SessionID, r.LastActivity, formatTokens(r.Input),
			formatTokens(r.Output), formatTokens(r.CacheWrite),
			formatTokens(r.CacheRead), r.Project)
	}
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
