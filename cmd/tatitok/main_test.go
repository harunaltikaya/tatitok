package main

import (
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
