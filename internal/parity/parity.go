// Package parity implements the ccusage token-parity conformance gate
// (PRD AS-6, milestone-1 Task 6). Per-day input/output/cache-write/
// cache-read sums must match ccusage exactly — zero tolerance. A mismatch
// is a parsing/dedup bug to investigate, never something to waive by
// editing expectations.
package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/harunaltikaya/tatitok/internal/store"
)

// CCUsageDaily mirrors the subset of `ccusage claude daily --json` that
// M1 compares (cost is excluded).
type CCUsageDaily struct {
	Daily []CCUsageDay `json:"daily"`
}

type CCUsageDay struct {
	Date string `json:"date"`
	store.TokenSums
	// ReasoningOutputTokens joins the comparison when ccusage reports it
	// (codex daily output; M3 Task 4 decision: YES — it is
	// provider-reported and matched exactly on the fixture set, so it is
	// zero-tolerance like the other token columns). nil when the harness's
	// ccusage output has no such column (claude, opencode).
	ReasoningOutputTokens *int64 `json:"reasoningOutputTokens"`
}

// Meta is the capture record written by the harvest script next to the
// expectations (pinned ccusage version, capture timezone).
type Meta struct {
	CCUsageVersion string `json:"ccusage_version"`
	Timezone       struct {
		IANA string `json:"iana"`
	} `json:"timezone"`
}

func LoadMeta(path string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	if m.CCUsageVersion == "" || m.Timezone.IANA == "" {
		return m, fmt.Errorf("%s: missing ccusage_version or timezone", path)
	}
	return m, nil
}

func LoadExpectedDaily(path string) (CCUsageDaily, error) {
	var d CCUsageDaily
	b, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

func ParseDailyJSON(b []byte) (CCUsageDaily, error) {
	var d CCUsageDaily
	err := json.Unmarshal(b, &d)
	return d, err
}

// CompareDaily checks the four token sums per day for exact equality —
// plus reasoning tokens on days where ccusage reports the column (codex;
// same zero tolerance). The returned string is a per-day diff table,
// empty when parity holds.
func CompareDaily(got []store.DailyRow, want CCUsageDaily) string {
	type row struct {
		got           *store.TokenSums
		gotReasoning  int64
		want          *store.TokenSums
		wantReasoning *int64
	}
	days := map[string]*row{}
	for i := range got {
		days[got[i].Date] = &row{got: &got[i].TokenSums, gotReasoning: got[i].Reasoning}
	}
	for i := range want.Daily {
		d := days[want.Daily[i].Date]
		if d == nil {
			d = &row{}
			days[want.Daily[i].Date] = d
		}
		d.want = &want.Daily[i].TokenSums
		d.wantReasoning = want.Daily[i].ReasoningOutputTokens
	}

	var dates []string
	for d := range days {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	var b strings.Builder
	mismatches := 0
	for _, date := range dates {
		r := days[date]
		switch {
		case r.want == nil:
			mismatches++
			fmt.Fprintf(&b, "%s: day present in tatitok but absent from ccusage (got %+v)\n", date, *r.got)
		case r.got == nil:
			mismatches++
			fmt.Fprintf(&b, "%s: day present in ccusage but absent from tatitok (want %+v)\n", date, *r.want)
		case *r.got != *r.want ||
			(r.wantReasoning != nil && r.gotReasoning != *r.wantReasoning):
			mismatches++
			fmt.Fprintf(&b, "%s: MISMATCH\n", date)
			diffField(&b, "input", r.got.Input, r.want.Input)
			diffField(&b, "output", r.got.Output, r.want.Output)
			diffField(&b, "cache-write", r.got.CacheWrite, r.want.CacheWrite)
			diffField(&b, "cache-read", r.got.CacheRead, r.want.CacheRead)
			if r.wantReasoning != nil {
				diffField(&b, "reasoning", r.gotReasoning, *r.wantReasoning)
			}
		}
	}
	if mismatches == 0 {
		return ""
	}
	fmt.Fprintf(&b, "%d of %d days diverge\n", mismatches, len(dates))
	return b.String()
}

func diffField(b *strings.Builder, name string, got, want int64) {
	if got != want {
		fmt.Fprintf(b, "  %-12s got %14d  want %14d  delta %+d\n", name, got, want, got-want)
	}
}
