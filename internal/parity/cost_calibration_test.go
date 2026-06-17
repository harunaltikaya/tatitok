package parity

// Cost calibration vs ccusage (M3 Task 2, AS-6 cost lane): per-day cost
// sums compared against the committed expectations — captured offline
// with ccusage's bundled prices — with a DOCUMENTED tolerance, never
// zero-tolerance (pricing snapshot versions legitimately differ; tokens
// stay zero-tolerance in their own gate).
//
// Measured baselines (snapshot litellm-2026-06-11-3ceb8bff vs ccusage
// 20.0.9):
//
//   - codex: exact within per-event rounding — max |Δ| $0.00004/day.
//   - claude-code: exact within rounding except 2026-06-10, −$0.258734
//     (0.41%): the bare claude-sonnet-4-6 snapshot key lacks the
//     1h-TTL cache-write rate (above_1hr) that ccusage's bundled
//     snapshot has; that day's sonnet cache writes are all 1h-TTL.
//     Tolerance max(0.5%, $0.01) absorbs exactly this class of residual.
//   - opencode: ccusage's totalCost is BLENDED — the source-reported
//     cost, except when it is zero and the model resolves in its price
//     DB, then a computed price (measured: every day equals the source
//     sum except 2026-05-02, which adds ccusage's computed gpt-5-nano
//     $0.007942 where the source recorded $0). The lane reproduces that
//     blend with OUR computed costs; ours-vs-source deltas live in
//     doctor --pricing, not here.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

type expectedCostDay struct {
	Date      string      `json:"date"`
	TotalCost json.Number `json:"totalCost"` // claude-code, opencode
	CostUSD   json.Number `json:"costUSD"`   // codex
}

func loadExpectedCosts(t *testing.T, base string) (map[string]int64, *time.Location) {
	t.Helper()
	var meta struct {
		Timezone struct {
			IANA string `json:"iana"`
		} `json:"timezone"`
	}
	mb, err := os.ReadFile(filepath.Join(base, "expected", "META.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mb, &meta); err != nil {
		t.Fatal(err)
	}
	tz, err := time.LoadLocation(meta.Timezone.IANA)
	if err != nil {
		t.Fatal(err)
	}
	var exp struct {
		Daily []expectedCostDay `json:"daily"`
	}
	eb, err := os.ReadFile(filepath.Join(base, "expected", "ccusage-daily.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(eb, &exp); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, d := range exp.Daily {
		n := d.TotalCost
		if n == "" {
			n = d.CostUSD
		}
		micro, err := pricing.USDToMicro(n.String())
		if err != nil {
			t.Fatalf("%s: expected cost %q: %v", d.Date, n, err)
		}
		out[d.Date] = micro
	}
	return out, tz
}

func ourDailyCosts(t *testing.T, st *store.Store, tz *time.Location, expr string) map[string]int64 {
	t.Helper()
	rows, err := st.DB().QueryContext(context.Background(),
		`SELECT tatitok_day(ts, ?), COALESCE(SUM(`+expr+`), 0)
		 FROM usage_events GROUP BY 1`, tz.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var day string
		var micro int64
		if err := rows.Scan(&day, &micro); err != nil {
			t.Fatal(err)
		}
		out[day] = micro
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// computed-cost lanes: our cost_usd_micro sums vs ccusage, with the
// documented per-harness tolerance.
func TestCostCalibrationComputed(t *testing.T) {
	cases := []struct {
		set       paritySet
		tolerance func(ccusage int64) int64
		note      string
	}{
		{paritySets[0], func(cc int64) int64 { // claude-code
			tol := abs64(cc) / 200 // 0.5%
			if tol < 10_000 {      // $0.01
				tol = 10_000
			}
			return tol
		}, "max(0.5%, $0.01) — absorbs snapshot-version residuals (sonnet 1h cache rate)"},
		{paritySets[1], func(int64) int64 { return 1_000 }, // codex: $0.001/day
			"$0.001/day — same snapshot lineage, per-event rounding only"},
	}
	for _, c := range cases {
		t.Run(c.set.harness, func(t *testing.T) {
			base := filepath.Join(c.set.fixtureBase, "gx10")
			want, tz := loadExpectedCosts(t, base)
			st := ingestIntoWith(t, c.set.adapter, []adapters.Source{{
				Harness: c.set.harness, Root: c.set.root(t, base), Machine: "gx10",
			}})
			got := ourDailyCosts(t, st, tz, "cost_usd_micro")
			t.Logf("tolerance: %s", c.note)
			for day, cc := range want {
				delta := got[day] - cc
				t.Logf("%s ours=%s ccusage=%s delta=%s", day,
					microUSD(got[day]), microUSD(cc), microUSD(delta))
				if abs64(delta) > c.tolerance(cc) {
					t.Errorf("%s: |%s| exceeds tolerance %s — investigate rates/component mapping before touching expectations",
						day, microUSD(delta), microUSD(c.tolerance(cc)))
				}
			}
		})
	}
}

// opencode blended lane (see the package comment): per event, the
// source-reported cost unless it is zero and WE priced the event — then
// our computed cost stands in for ccusage's. Tolerance max(0.5%, $0.001)
// per day absorbs snapshot drift on the computed stand-ins.
func TestCostCalibrationOpencodeBlended(t *testing.T) {
	set := paritySets[2]
	base := filepath.Join(set.fixtureBase, "gx10")
	want, tz := loadExpectedCosts(t, base)
	st := ingestIntoWith(t, set.adapter, []adapters.Source{{
		Harness: set.harness, Root: set.root(t, base), Machine: "gx10",
	}})

	recon, err := st.PricingReconciliation(context.Background(), pricing.USDToMicro)
	if err != nil {
		t.Fatal(err)
	}
	if len(recon) == 0 {
		t.Fatal("no source costs stored — meta.source_cost extraction broken")
	}

	// Per-event blend: source cost, or — when the source recorded zero —
	// our stored API-equivalent (free basis, owner ruling) standing in
	// for ccusage's computed value.
	got := map[string]int64{}
	rows, err := st.DB().QueryContext(context.Background(), `SELECT
			tatitok_day(ts, ?), meta,
			COALESCE(cost_api_equiv_micro, cost_usd_micro, 0)
		FROM usage_events
		WHERE meta IS NOT NULL AND instr(meta, '"source_cost"') > 0`, tz.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var day, meta string
		var ours int64
		if err := rows.Scan(&day, &meta, &ours); err != nil {
			t.Fatal(err)
		}
		var m struct {
			SourceCost json.Number `json:"source_cost"`
		}
		dec := json.NewDecoder(strings.NewReader(meta))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil || m.SourceCost == "" {
			continue
		}
		micro, err := pricing.USDToMicro(m.SourceCost.String())
		if err != nil {
			t.Fatal(err)
		}
		if micro == 0 {
			micro = ours
		}
		got[day] += micro
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for day, cc := range want {
		delta := got[day] - cc
		tol := abs64(cc) / 200
		if tol < 1_000 {
			tol = 1_000
		}
		t.Logf("%s blended=%s ccusage=%s delta=%s", day,
			microUSD(got[day]), microUSD(cc), microUSD(delta))
		if abs64(delta) > tol {
			t.Errorf("%s: blended sum diverges from ccusage by %s (tolerance %s) — the blend model or a rate broke",
				day, microUSD(delta), microUSD(tol))
		}
	}
}

func microUSD(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	return fmt.Sprintf("%s$%d.%06d", sign, micro/1_000_000, micro%1_000_000)
}
