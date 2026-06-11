// Package codex ingests Codex CLI rollout session logs
// (<CODEX_HOME>/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl).
//
// ccusage (pinned, `ccusage codex`) is the parity referee (CLAUDE.md hard
// rule 2). Counting rules verified empirically against the pinned capture
// over the gx10 fixture set (exact, per day and per model — see
// docs/format-notes.md "Codex"):
//
//   - one billable event per event_msg payload of type "token_count"
//     whose info is non-null, valued from info.last_token_usage (the
//     per-request figures; total_token_usage is the session-cumulative
//     view of the same data);
//   - inputTokens EXCLUDES the cached portion: input = input_tokens -
//     cached_input_tokens, cacheRead = cached_input_tokens; codex has no
//     cache-write concept (always 0); output_tokens INCLUDES
//     reasoning_output_tokens, which is also reported separately;
//   - the model is NOT on the usage record: it is the latest
//     turn_context.model seen in the file (stateful carry-forward; the
//     fixture set contains zero token_count events before the first
//     turn_context);
//   - ccusage applies NO dedup across codex records (summing every
//     token_count event matches exactly), so every event gets a
//     per-occurrence fallback ID — re-ingest stays idempotent and
//     distinct occurrences never collapse.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
)

// AdapterVersion is bumped whenever format handling changes.
const AdapterVersion = 1

const harnessName = "codex"

type Adapter struct{}

func (Adapter) Name() string { return harnessName }

func (Adapter) Version() int { return AdapterVersion }

// Detect returns the sessions root. CODEX_HOME overrides (the directory
// CONTAINING sessions/ — no comma lists, matching codex and ccusage);
// otherwise ~/.codex/sessions is probed.
func (Adapter) Detect(env adapters.Probe) ([]adapters.Source, error) {
	base := filepath.Join(env.HomeDir, ".codex")
	if v := strings.TrimSpace(env.Getenv("CODEX_HOME")); v != "" {
		base = v
	}
	root := filepath.Join(base, "sessions")
	st, err := os.Stat(root)
	switch {
	case err == nil && st.IsDir():
		return []adapters.Source{{
			Harness: harnessName, Root: root, Machine: env.Machine,
		}}, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		slog.Warn("cannot probe candidate log root",
			"adapter", harnessName, "root", root, "error", err)
	}
	return nil, nil
}

// record is the typed view of one rollout line; the full line is
// preserved separately through core.SanitizeRaw, so unknown fields
// survive into the raw column even though this struct ignores them.
type record struct {
	Timestamp string  `json:"timestamp"`
	Type      string  `json:"type"`
	Payload   payload `json:"payload"`
}

type payload struct {
	// discriminator for event_msg payloads ("token_count", ...)
	Type string `json:"type"`
	// session_meta fields
	ID            string `json:"id"`
	CLIVersion    string `json:"cli_version"`
	ModelProvider string `json:"model_provider"`
	// turn_context fields (cwd also on session_meta)
	Cwd   string `json:"cwd"`
	Model string `json:"model"`
	// token_count payload
	Info *tokenInfo `json:"info"`
}

type tokenInfo struct {
	LastTokenUsage *tokenUsage `json:"last_token_usage"`
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// Backfill parses every rollout file under src.Root (sessions/YYYY/MM/DD
// date tree) and emits one event per token_count record, in size-bounded
// batches (contract v2). Files are processed in path order — the date
// tree makes that chronological; with no cross-record dedup the order
// has no semantic effect.
func (Adapter) Backfill(ctx context.Context, src adapters.Source, sink adapters.Sink) error {
	files, skipped, err := listRolloutFiles(src.Root)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		slog.Warn("skipping unreadable sessions subtree",
			"adapter", harnessName, "path", s.path, "error", s.err)
		if err := sink.FileStart(s.path); err != nil {
			return err
		}
		if err := sink.FileDone(adapters.FileResult{
			Path: s.path, ReadError: s.err.Error(),
		}); err != nil {
			return err
		}
	}
	sort.Strings(files)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sink.FileStart(f); err != nil {
			return err
		}
		res, readErr, sinkErr := backfillFile(src, f, sink)
		if sinkErr != nil {
			return sinkErr
		}
		if readErr != nil {
			slog.Warn("skipping unreadable rollout file",
				"adapter", harnessName, "file", f, "error", readErr)
			res = adapters.FileResult{Path: f, ReadError: readErr.Error()}
		}
		if err := sink.FileDone(res); err != nil {
			return err
		}
	}
	return nil
}

type skippedSource struct {
	path string
	err  error
}

// listRolloutFiles walks the date tree. An unreadable directory is
// reported as a skipped source (its files cannot be enumerated) without
// aborting the rest of the walk; an unreadable root fails the backfill.
func listRolloutFiles(root string) ([]string, []skippedSource, error) {
	var files []string
	var skipped []skippedSource
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return fmt.Errorf("read log root %s: %w", root, err)
			}
			skipped = append(skipped, skippedSource{path: path, err: err})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, skipped, nil
}

// readLine reads one full line of any length (rollout lines reach many
// megabytes; bufio.Scanner's token limit is not safe here).
func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return line, err
	}
	if err == nil {
		line = line[:len(line)-1]
	} else if len(line) == 0 {
		return nil, io.EOF
	}
	return line, err
}

// backfillFile streams one rollout file's billable events to the sink.
// The two error returns are distinct on purpose: readErr means the FILE
// could not be (fully) read — Backfill reports it as a skipped source;
// sinkErr must cancel the whole backfill unchanged.
func backfillFile(src adapters.Source, path string, sink adapters.Sink) (res adapters.FileResult, readErr, sinkErr error) {
	f, err := os.Open(path)
	if err != nil {
		return res, err, nil
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return res, err, nil
	}

	res = adapters.FileResult{
		Path:  path,
		MTime: st.ModTime().UTC(),
		Size:  st.Size(),
	}
	fileRel := sourceRelPath(src.Root, path)
	sessionFromName := sessionIDFromFilename(filepath.Base(path))

	// per-file carried state (stateful parsing: usage records carry no
	// model/cwd/provider of their own)
	var model, cwd, provider, cliVersion, sessionID string

	var batch []core.Event
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := sink.EmitBatch(path, batch)
		res.Events += len(batch)
		batch = nil // the sink may retain the slice; never reuse it
		return err
	}
	r := bufio.NewReaderSize(f, 256*1024)
	for lineIdx := 0; ; lineIdx++ {
		line, lineErr := readLine(r)
		if lineErr != nil && !errors.Is(lineErr, io.EOF) {
			return res, lineErr, nil
		}
		if len(line) > 0 {
			res.LineCount++
			var rec record
			perr := json.Unmarshal(line, &rec)
			switch {
			case perr != nil && errors.Is(lineErr, io.EOF):
				// Unterminated final line that does not parse: a write in
				// progress, not a malformed line (same classification as
				// claude-code; the next backfill clears it).
				res.IncompleteTail = true
				slog.Info("incomplete tail line",
					"adapter", harnessName, "file", path, "line", lineIdx+1)
			case perr != nil:
				res.ParseErrors++
				slog.Warn("malformed log line",
					"adapter", harnessName, "file", path,
					"line", lineIdx+1, "error", perr)
			default:
				switch rec.Type {
				case "session_meta":
					if rec.Payload.ID != "" {
						sessionID = rec.Payload.ID
					}
					if rec.Payload.Cwd != "" {
						cwd = rec.Payload.Cwd
					}
					if rec.Payload.ModelProvider != "" {
						provider = rec.Payload.ModelProvider
					}
					if rec.Payload.CLIVersion != "" {
						cliVersion = rec.Payload.CLIVersion
					}
				case "turn_context":
					if rec.Payload.Model != "" {
						model = rec.Payload.Model
					}
					if rec.Payload.Cwd != "" {
						cwd = rec.Payload.Cwd
					}
				case "event_msg":
					if rec.Payload.Type != "token_count" ||
						rec.Payload.Info == nil ||
						rec.Payload.Info.LastTokenUsage == nil {
						break // non-usage event, or info null: not billable
					}
					ev, perr := buildEvent(line, &rec, src, fileRel, lineIdx,
						model, cwd, provider, cliVersion,
						sessionID, sessionFromName)
					if perr != nil {
						res.ParseErrors++
						slog.Warn("malformed token_count record",
							"adapter", harnessName, "file", path,
							"line", lineIdx+1, "error", perr)
						break
					}
					batch = append(batch, ev)
					if len(batch) == adapters.BatchSize {
						if err := flush(); err != nil {
							return res, nil, err
						}
					}
				}
			}
		}
		if lineErr != nil {
			break
		}
	}
	if err := flush(); err != nil {
		return res, nil, err
	}
	return res, nil, nil
}

func buildEvent(line []byte, rec *record, src adapters.Source,
	fileRel string, lineIdx int,
	model, cwd, provider, cliVersion, sessionID, sessionFromName string) (core.Event, error) {
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return core.Event{}, fmt.Errorf("timestamp %q: %w", rec.Timestamp, err)
	}
	raw, err := core.SanitizeRaw(line)
	if err != nil {
		return core.Event{}, err
	}

	if provider == "" {
		provider = "openai" // codex default unless session_meta says otherwise
	}
	if sessionID == "" {
		sessionID = sessionFromName
	}
	u := rec.Payload.Info.LastTokenUsage
	reasoning := u.ReasoningOutputTokens

	meta := map[string]any{}
	if cliVersion != "" {
		meta["client_version"] = cliVersion
	}
	if len(meta) == 0 {
		meta = nil
	}

	return core.Event{
		// token_count records carry no native event id → per-occurrence
		// fallback (ccusage applies no dedup; see package comment).
		ID:          core.FallbackID(harnessName, fileRel, lineIdx, rec.Timestamp),
		TS:          ts.UTC(),
		Machine:     src.Machine,
		SourceKind:  core.SourceKindHarnessLog,
		Harness:     harnessName,
		Provider:    provider,
		Model:       model,
		ModelFamily: model, // unknown models pass through raw until the mapping milestone
		Project:     cwd,
		SessionID:   sessionID,
		// input EXCLUDES the cached portion (ccusage's codex rule);
		// output INCLUDES reasoning, which is also reported separately
		TokensInput:      u.InputTokens - u.CachedInputTokens,
		TokensOutput:     u.OutputTokens,
		TokensCacheWrite: 0, // codex has no cache-write concept
		TokensCacheRead:  u.CachedInputTokens,
		TokensReasoning:  &reasoning,
		Accuracy:         core.AccuracyExact,
		Meta:             meta,
		Raw:              raw,
	}, nil
}

// sourceRelPath is the file's path relative to the sessions root,
// '/'-joined (date dirs + basename) — the fallback-ID file component, so
// equal filenames under different date dirs cannot collide while IDs stay
// stable when the log root moves between machines.
func sourceRelPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

// sessionIDFromFilename extracts the session uuid embedded at the end of
// the rollout filename (rollout-<ts>-<uuid>.jsonl); empty when absent.
func sessionIDFromFilename(name string) string {
	stem := strings.TrimSuffix(name, ".jsonl")
	if len(stem) < 36 {
		return ""
	}
	tail := stem[len(stem)-36:]
	for i, c := range tail {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return ""
			}
		default:
			isHex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
			if !isHex {
				return ""
			}
		}
	}
	return tail
}
