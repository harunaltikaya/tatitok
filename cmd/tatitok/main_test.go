package main

import (
	"fmt"
	"os"
	"testing"
)

// TestRunExitCodes is a table test over the argument-validation surface of
// run(): every case below fails (or exits) before any store.Open, so no
// database is ever touched — no temp DB, no defaultPath resolution that hits
// disk. The command tokens that bind exit codes are:
//
//	2   usage errors (no command, unknown command)
//	0   help
//	1   valid command, invalid required flags (ingest needs --backfill,
//	    stats needs exactly one of --daily/--session and --session accepts
//	    no --by, doctor and recompute need exactly one mode flag)
func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"help", []string{"--help"}, 0},
		{"unknown command", []string{"frobnicate"}, 2},
		{"ingest without --backfill", []string{"ingest"}, 1},
		{"stats with no --daily/--session", []string{"stats"}, 1},
		{"stats with both --daily/--session", []string{"stats", "--daily", "--session"}, 1},
		{"stats --session --by harness", []string{"stats", "--session", "--by", "harness"}, 1},
		{"doctor with no mode", []string{"doctor"}, 1},
		{"recompute with no mode", []string{"recompute"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got int
			quiet(func() { got = run(tt.args) })
			if got != tt.want {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

// TestErrAsUnwraps is a regression test for the exitError unwrapping fix.
// errAs must behave like the standard library's errors.As: an exitError
// wrapped in the chain (via fmt.Errorf %w or a custom wrapper) must still
// resolve to its exit code and message. The wrapped cases below FAIL on the
// old implementation, which used a single-step type assertion that only saw
// the outermost concrete type.
func TestErrAs(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantOK   bool
		wantCode int
		wantMsg  string
	}{
		{"nil", nil, false, 0, ""},
		{"direct value", exitError{code: 1, msg: "doctor found issues"}, true, 1, "doctor found issues"},
		{"single wrap", fmt.Errorf("op: %w", exitError{code: 3, msg: "skipped"}), true, 3, "skipped"},
		{"double wrap", fmt.Errorf("a: %w", fmt.Errorf("b: %w", exitError{code: 3, msg: "skipped"})), true, 3, "skipped"},
		{"no exitError in chain", fmt.Errorf("plain failure"), false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ec exitError
			got := errAs(tc.err, &ec)
			if got != tc.wantOK {
				t.Errorf("errAs(%v) = %v, want ok=%v", errStr(tc.err), got, tc.wantOK)
				return
			}
			if tc.wantOK && (ec.code != tc.wantCode || ec.msg != tc.wantMsg) {
				t.Errorf("errAs target = %+v, want {%d %q}", ec, tc.wantCode, tc.wantMsg)
			}
		})
	}
}

func errStr(e error) string {
	if e == nil {
		return "nil"
	}
	return e.Error()
}

// quiet redirects stdout/stderr to the null device for the duration of fn so
// usage text and error lines stay out of the test log.
func quiet(fn func()) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		panic(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = null, null
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = null.Close()
	}()
	fn()
}
