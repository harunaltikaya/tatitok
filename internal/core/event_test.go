package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAccuracyValid(t *testing.T) {
	for _, a := range []Accuracy{AccuracyExact, AccuracyDerived, AccuracyEstimated} {
		if !a.Valid() {
			t.Errorf("%q should be valid", a)
		}
	}
	for _, a := range []Accuracy{"", "Exact", "guessed"} {
		if a.Valid() {
			t.Errorf("%q should be invalid", a)
		}
	}
}

func validEvent() Event {
	return Event{
		ID:         EventID("claude-code", "msg_abc", "req_def"),
		TS:         time.Date(2026, 6, 10, 14, 23, 43, 0, time.UTC),
		Machine:    "test",
		SourceKind: SourceKindHarnessLog,
		Harness:    "claude-code",
		Provider:   "anthropic",
		Model:      "claude-fable-5",
		Accuracy:   AccuracyExact,
	}
}

func TestEventValidate(t *testing.T) {
	e := validEvent()
	if err := e.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}

	// Model and Provider MAY be empty: codex token_count records before
	// the first turn_context genuinely carry no model — counted as an
	// ingest health signal, never rejected (M2.1 item 9).
	noModel := validEvent()
	noModel.Model, noModel.ModelFamily, noModel.Provider = "", "", ""
	if err := noModel.Validate(); err != nil {
		t.Fatalf("empty model/provider must stay legal: %v", err)
	}

	noID := validEvent()
	noID.ID = ""
	noKind := validEvent()
	noKind.SourceKind = ""
	noHarness := validEvent()
	noHarness.Harness = ""
	zeroTS := validEvent()
	zeroTS.TS = time.Time{}
	localTS := validEvent()
	localTS.TS = localTS.TS.In(time.FixedZone("X", 3*3600))
	badAcc := validEvent()
	badAcc.Accuracy = "approximate"
	negIn := validEvent()
	negIn.TokensInput = -1
	negOut := validEvent()
	negOut.TokensOutput = -2
	negCW := validEvent()
	negCW.TokensCacheWrite = -3
	negCR := validEvent()
	negCR.TokensCacheRead = -4
	minusFive := int64(-5)
	negReason := validEvent()
	negReason.TokensReasoning = &minusFive

	for name, e := range map[string]Event{
		"empty id": noID, "empty source_kind": noKind,
		"empty harness": noHarness, "zero ts": zeroTS,
		"non-utc ts": localTS, "bad accuracy": badAcc,
		"negative input": negIn, "negative output": negOut,
		"negative cache write": negCW, "negative cache read": negCR,
		"negative reasoning": negReason,
	} {
		if err := e.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// Hard rule 6 enforcement: Validate is the boundary every event crosses
// on insert, so a Raw that still carries content must be an error there.
func TestEventValidateRejectsUnsanitizedRaw(t *testing.T) {
	long := strings.Repeat("x", maxFreeLen+1)
	bad := map[string]json.RawMessage{
		"content key holds text": json.RawMessage(
			`{"message":{"content":"the actual user prompt text"}}`),
		"long free text":   json.RawMessage(`{"surprise":"` + long + `"}`),
		"raw not json":     json.RawMessage(`{"truncated":`),
		"text under input": json.RawMessage(`{"input":"rm -rf the secret"}`),
	}
	for name, raw := range bad {
		e := validEvent()
		e.Raw = raw
		if err := e.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}

	ok := validEvent()
	ok.Raw = json.RawMessage(
		`{"message":{"content":"<stripped len=27 sha256=0123456789ab>"},"type":"assistant"}`)
	if err := ok.Validate(); err != nil {
		t.Errorf("sanitized raw rejected: %v", err)
	}
}
