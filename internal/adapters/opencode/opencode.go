// Package opencode ingests OpenCode's message store. Current OpenCode
// keeps one row per message in a SQLite database
// (<data-dir>/opencode/opencode.db, message table: id, session_id,
// time_created, time_updated, data JSON) — the old per-message JSON file
// layout is gone (see docs/format-notes.md "OpenCode").
//
// ccusage (pinned, `ccusage opencode`) is the parity referee (CLAUDE.md
// hard rule 2). Counting rules verified empirically against the pinned
// capture over the gx10 fixture set (exact per day, per model list and
// all four sums):
//
//   - one billable event per assistant-role message whose data carries a
//     tokens object with a NONZERO grand total (input + output +
//     reasoning + cache.read + cache.write); zero-token messages
//     (aborted/errored turns) are skipped entirely — they contribute
//     neither tokens nor a modelsUsed entry;
//   - tokens map directly: input, output, cache.read → cache-read,
//     cache.write → cache-write, reasoning reported separately;
//   - provider and model are explicit on every message (providerID /
//     modelID) and pass through VERBATIM — local vLLM providers appear
//     and are never normalized (milestone-2 rule);
//   - days bucket by the row's time_created (epoch milliseconds; equal to
//     data.time.created on every fixture row) in local time.
//
// The message id is the table's primary key — the native idempotency
// key. Event IDs hash (harness, message id, session id): both native,
// stable and unique per message, so re-ingest is idempotent and distinct
// messages never collapse.
//
// Message rows are MUTABLE while a turn is in flight: OpenCode rewrites
// the row's data blob until the message finishes, marked by
// data.time.completed appearing (verified empirically on the live store,
// 2026-06-11: every assistant row without time.completed carries zero
// tokens, and no row changes after time.completed — see
// docs/format-notes.md "Message rows are mutable"). A snapshot taken
// mid-turn could therefore hand us a partial row; it is still emitted
// when its tokens are nonzero (ccusage counts it at the same snapshot —
// parity), and the store's replacement semantics update the stored event
// when a later ingest reads the finalized row (same event ID, changed
// payload; replacements are counted and logged, never silent).
package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
)

// AdapterVersion is bumped whenever format handling changes.
const AdapterVersion = 1

const harnessName = "opencode"

type Adapter struct{}

func (Adapter) Name() string { return harnessName }

func (Adapter) Version() int { return AdapterVersion }

// Detect returns the data dir holding opencode.db. XDG_DATA_HOME
// overrides (OpenCode itself is XDG-aware even though ccusage's discovery
// is HOME-anchored); the default is ~/.local/share/opencode.
func (Adapter) Detect(env adapters.Probe) ([]adapters.Source, error) {
	base := filepath.Join(env.HomeDir, ".local", "share")
	if v := strings.TrimSpace(env.Getenv("XDG_DATA_HOME")); v != "" {
		base = v
	}
	root := filepath.Join(base, "opencode")
	st, err := os.Stat(filepath.Join(root, "opencode.db"))
	switch {
	case err == nil && !st.IsDir():
		return []adapters.Source{{
			Harness: harnessName, Root: root, Machine: env.Machine,
		}}, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		slog.Warn("cannot probe candidate message store",
			"adapter", harnessName, "root", root, "error", err)
	}
	return nil, nil
}

// row mirrors one message-table row; data stays raw for sanitization.
type row struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	TimeCreated int64           `json:"time_created"`
	TimeUpdated int64           `json:"time_updated"`
	Data        json.RawMessage `json:"data"`
}

// messageData is the typed view of the data blob; the full row is
// preserved separately through core.SanitizeRaw, so unknown fields
// survive into the raw column even though this struct ignores them.
type messageData struct {
	Role       string  `json:"role"`
	ProviderID string  `json:"providerID"`
	ModelID    string  `json:"modelID"`
	Tokens     *tokens `json:"tokens"`
	Path       *struct {
		Cwd string `json:"cwd"`
	} `json:"path"`
}

type tokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

// Backfill reads every message row (ordered by time_created, id — a
// deterministic order; there is no cross-row dedup so order has no
// semantic effect) and emits billable events in size-bounded batches
// (contract v2). The whole database is ONE source "file" for
// bookkeeping: per-row JSON failures are contained as parse errors; a
// database-level failure reports the store as a skipped source.
func (Adapter) Backfill(ctx context.Context, src adapters.Source, sink adapters.Sink) error {
	dbPath := filepath.Join(src.Root, "opencode.db")
	if err := sink.FileStart(dbPath); err != nil {
		return err
	}
	res, readErr, sinkErr := backfillDB(ctx, src, dbPath, sink)
	if sinkErr != nil {
		return sinkErr
	}
	if readErr != nil {
		slog.Warn("skipping unreadable message store",
			"adapter", harnessName, "db", dbPath, "error", readErr)
		res = adapters.FileResult{Path: dbPath, ReadError: readErr.Error()}
	}
	return sink.FileDone(res)
}

func backfillDB(ctx context.Context, src adapters.Source, dbPath string, sink adapters.Sink) (res adapters.FileResult, readErr, sinkErr error) {
	st, err := os.Stat(dbPath)
	if err != nil {
		return res, err, nil
	}
	res = adapters.FileResult{
		Path:  dbPath,
		MTime: st.ModTime().UTC(),
		Size:  st.Size(),
	}

	// Read-only: the adapter must never mutate the live store (hard
	// rule 7 spirit); busy_timeout guards against OpenCode's own writers.
	db, err := sql.Open("sqlite", fmt.Sprintf(
		"file:%s?mode=ro&_pragma=busy_timeout(5000)", dbPath))
	if err != nil {
		return res, err, nil
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, `SELECT id, session_id,
			time_created, time_updated, data
		FROM message ORDER BY time_created, id`)
	if err != nil {
		return res, err, nil
	}
	defer func() { _ = rows.Close() }()

	var batch []core.Event
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := sink.EmitBatch(dbPath, batch)
		res.Events += len(batch)
		batch = nil // the sink may retain the slice; never reuse it
		return err
	}
	for rows.Next() {
		var r row
		var data string
		if err := rows.Scan(&r.ID, &r.SessionID,
			&r.TimeCreated, &r.TimeUpdated, &data); err != nil {
			// A scan failure is a database-level read error: fail the
			// store so the next backfill retries it whole.
			return res, err, nil
		}
		r.Data = json.RawMessage(data)
		res.LineCount++
		ev, ok, perr := buildEvent(&r, src)
		switch {
		case perr != nil:
			res.ParseErrors++
			slog.Warn("malformed message row",
				"adapter", harnessName, "db", dbPath,
				"row", r.ID, "error", perr)
		case ok:
			batch = append(batch, ev)
			if len(batch) == adapters.BatchSize {
				if err := flush(); err != nil {
					return res, nil, err
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return res, err, nil
	}
	if err := flush(); err != nil {
		return res, nil, err
	}
	return res, nil, nil
}

// buildEvent returns (event, true, nil) for a billable message, (zero,
// false, nil) for a valid non-billable row (user role, no tokens, or a
// zero-token aborted turn — ccusage skips those), and an error for a row
// whose data blob does not parse.
func buildEvent(r *row, src adapters.Source) (core.Event, bool, error) {
	var data messageData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		return core.Event{}, false, err
	}
	if data.Role != "assistant" || data.Tokens == nil {
		return core.Event{}, false, nil
	}
	u := data.Tokens
	if u.Input+u.Output+u.Reasoning+u.Cache.Read+u.Cache.Write == 0 {
		return core.Event{}, false, nil // aborted/errored turn; ccusage skips it
	}

	rowJSON, err := json.Marshal(r)
	if err != nil {
		return core.Event{}, false, err
	}
	raw, err := core.SanitizeRaw(rowJSON)
	if err != nil {
		return core.Event{}, false, err
	}

	project := ""
	if data.Path != nil {
		project = data.Path.Cwd
	}
	reasoning := u.Reasoning

	return core.Event{
		// message id is the table PK; session id is its parent — both
		// native and stable (see package comment).
		ID:          core.EventID(harnessName, r.ID, r.SessionID),
		TS:          time.UnixMilli(r.TimeCreated).UTC(),
		Machine:     src.Machine,
		SourceKind:  core.SourceKindHarnessLog,
		Harness:     harnessName,
		Provider:    data.ProviderID, // verbatim — never normalized
		Model:       data.ModelID,
		ModelFamily: data.ModelID, // unknown models pass through raw until the mapping milestone
		Project:     project,
		SessionID:   r.SessionID,
		TokensInput: u.Input, TokensOutput: u.Output,
		TokensCacheWrite: u.Cache.Write,
		TokensCacheRead:  u.Cache.Read,
		TokensReasoning:  &reasoning,
		Accuracy:         core.AccuracyExact,
		Raw:              raw,
	}, true, nil
}
