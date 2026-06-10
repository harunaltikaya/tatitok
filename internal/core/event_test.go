package core

import (
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

	noID := validEvent()
	noID.ID = ""
	zeroTS := validEvent()
	zeroTS.TS = time.Time{}
	localTS := validEvent()
	localTS.TS = localTS.TS.In(time.FixedZone("X", 3*3600))
	badAcc := validEvent()
	badAcc.Accuracy = "approximate"

	for name, e := range map[string]Event{
		"empty id": noID, "zero ts": zeroTS,
		"non-utc ts": localTS, "bad accuracy": badAcc,
	} {
		if err := e.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
