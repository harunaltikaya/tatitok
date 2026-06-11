// Package claudecode ingests Claude Code session logs
// (<config-dir>/projects/<encoded-cwd>/<session-id>.jsonl).
//
// ccusage is the parity referee for every rule here: which records count,
// how duplicates collapse, how days bucket. Deviating from ccusage's
// behavior is a bug by definition (CLAUDE.md hard rule 2).
package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// v2: length-prefixed ID hashing; fallback IDs keyed by project-relative
// path (M1.1 ID hardening — all event IDs changed).
const AdapterVersion = 2

const harnessName = "claude-code"

type Adapter struct{}

func (Adapter) Name() string { return harnessName }

func (Adapter) Version() int { return AdapterVersion }

// Detect returns the log roots to ingest. CLAUDE_CONFIG_DIR overrides
// (comma-separated list supported, matching Claude Code and ccusage);
// otherwise both the legacy (~/.claude) and XDG (~/.config/claude)
// locations are probed and all that exist are returned. Overlapping
// roots are safe: deterministic event IDs collapse duplicates at insert.
func (Adapter) Detect(env adapters.Probe) ([]adapters.Source, error) {
	var candidates []string
	if v := env.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		for _, dir := range strings.Split(v, ",") {
			if dir = strings.TrimSpace(dir); dir != "" {
				candidates = append(candidates, filepath.Join(dir, "projects"))
			}
		}
	} else {
		candidates = []string{
			filepath.Join(env.HomeDir, ".claude", "projects"),
			filepath.Join(env.HomeDir, ".config", "claude", "projects"),
		}
	}
	var sources []adapters.Source
	for _, root := range candidates {
		st, err := os.Stat(root)
		switch {
		case err == nil && st.IsDir():
			sources = append(sources, adapters.Source{
				Harness: harnessName, Root: root, Machine: env.Machine,
			})
		case err != nil && !errors.Is(err, os.ErrNotExist):
			// A root that exists but cannot be probed must not vanish
			// silently — the user would see "no log roots found" and
			// believe there is nothing to ingest.
			slog.Warn("cannot probe candidate log root",
				"adapter", harnessName, "root", root, "error", err)
		}
	}
	return sources, nil
}

// record is the typed view of one log line; the full line is preserved
// separately through core.SanitizeRaw, so unknown fields survive into
// the raw column even though this struct ignores them.
type record struct {
	Type              string   `json:"type"`
	Timestamp         string   `json:"timestamp"`
	RequestID         string   `json:"requestId"`
	SessionID         string   `json:"sessionId"`
	Cwd               string   `json:"cwd"`
	Version           string   `json:"version"`
	GitBranch         string   `json:"gitBranch"`
	IsApiErrorMessage bool     `json:"isApiErrorMessage"`
	Message           *message `json:"message"`
}

type message struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage *usage `json:"usage"`
}

type usage struct {
	// ccusage's schema requires input_tokens and output_tokens; a usage
	// object missing either is not a billable record.
	InputTokens              *int64          `json:"input_tokens"`
	OutputTokens             *int64          `json:"output_tokens"`
	CacheCreationInputTokens int64           `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64           `json:"cache_read_input_tokens"`
	CacheCreation            json.RawMessage `json:"cache_creation"`
}

// Backfill parses every session file under src.Root and emits one event
// per billable assistant message, in size-bounded batches (contract v2).
// Files are processed in order of their earliest record timestamp —
// ccusage's order, so when duplicated (message id, request id) pairs carry
// diverging fields, the same copy wins on both sides.
//
// I/O failures are honest, never silent: an unreadable project dir or
// session file, or a non-EOF read error mid-file, reports a skipped
// source (FileDone with ReadError) that the ingest layer counts and
// persists; the ingest layer discards the file's already-emitted events
// so the next run retries the whole file. A sink error cancels the
// remaining backfill and is returned unchanged.
func (Adapter) Backfill(ctx context.Context, src adapters.Source, sink adapters.Sink) error {
	files, skipped, err := listSessionFiles(src.Root)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		slog.Warn("skipping unreadable project dir",
			"adapter", harnessName, "dir", s.path, "error", s.err)
		if err := sink.FileStart(s.path); err != nil {
			return err
		}
		if err := sink.FileDone(adapters.FileResult{
			Path: s.path, ReadError: s.err.Error(),
		}); err != nil {
			return err
		}
	}
	sortByEarliestTimestamp(files)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sink.FileStart(f); err != nil {
			return err
		}
		res, readErr, sinkErr := backfillFile(ctx, src, f, sink)
		if sinkErr != nil {
			return sinkErr
		}
		if readErr != nil {
			slog.Warn("skipping unreadable session file",
				"adapter", harnessName, "file", f, "error", readErr)
			res = adapters.FileResult{Path: f, ReadError: readErr.Error()}
		}
		if err := sink.FileDone(res); err != nil {
			return err
		}
	}
	return nil
}

// skippedSource is a directory or file that could not be read.
type skippedSource struct {
	path string
	err  error
}

func listSessionFiles(root string) ([]string, []skippedSource, error) {
	projects, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("read log root %s: %w", root, err)
	}
	var files []string
	var skipped []skippedSource
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, p.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			skipped = append(skipped, skippedSource{path: dir, err: err})
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	return files, skipped, nil
}

// sortByEarliestTimestamp orders files by the first timestamp field found
// in them (ccusage's deterministic global-dedup order). Files without any
// timestamp sort last; ties break on path.
func sortByEarliestTimestamp(files []string) {
	keys := make(map[string]string, len(files))
	for _, f := range files {
		keys[f] = earliestTimestamp(f)
	}
	sort.Slice(files, func(i, j int) bool {
		if keys[files[i]] != keys[files[j]] {
			return keys[files[i]] < keys[files[j]]
		}
		return files[i] < files[j]
	})
}

func earliestTimestamp(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "\xff"
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			var rec struct {
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.Timestamp != "" {
				return rec.Timestamp
			}
		}
		if err != nil {
			return "\xff"
		}
	}
}

// readLine reads one full line of any length (session lines reach many
// megabytes in real logs; bufio.Scanner's token limit is not safe here).
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

// cancelCheckInterval is how many lines a parse loop reads between ctx
// checks: session files reach hundreds of megabytes, and honoring
// cancellation only at file boundaries could stall a shutdown for the
// whole file.
const cancelCheckInterval = 1000

// backfillFile streams one session file's billable events to the sink in
// batches of at most adapters.BatchSize, checking ctx every
// cancelCheckInterval lines. The error returns are distinct on purpose:
// readErr means the FILE could not be (fully) read — Backfill reports it
// as a skipped source; sinkErr (also carrying ctx cancellation) must
// cancel the whole backfill unchanged.
func backfillFile(ctx context.Context, src adapters.Source, path string, sink adapters.Sink) (res adapters.FileResult, readErr, sinkErr error) {
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
	project := filepath.Base(filepath.Dir(path))
	sessionFromName := strings.TrimSuffix(filepath.Base(path), ".jsonl")

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
		if lineIdx%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return res, nil, err
			}
		}
		line, lineErr := readLine(r)
		if lineErr != nil && !errors.Is(lineErr, io.EOF) {
			// Non-EOF read error: fail the whole file; Backfill records it
			// as a skipped source and the ingest layer discards the
			// already-emitted partial events.
			return res, lineErr, nil
		}
		if len(line) > 0 {
			res.LineCount++
			ev, ok, perr := parseLine(line, src, path, lineIdx, project, sessionFromName)
			switch {
			case perr != nil && errors.Is(lineErr, io.EOF):
				// Unterminated final line that does not parse: a write in
				// progress, not a malformed line — bookkeeping, not a
				// parse error. The next backfill clears it (the full tail
				// state machine waits for the watcher milestone).
				res.IncompleteTail = true
				slog.Info("incomplete tail line",
					"adapter", harnessName, "file", path, "line", lineIdx+1)
			case perr != nil:
				res.ParseErrors++
				slog.Warn("malformed log line",
					"adapter", harnessName, "file", path,
					"line", lineIdx+1, "error", perr)
			case ok:
				batch = append(batch, ev)
				if len(batch) == adapters.BatchSize {
					if err := flush(); err != nil {
						return res, nil, err
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

// parseLine returns (event, true, nil) for a billable assistant message,
// (zero, false, nil) for a valid but non-billable record, and an error
// for a malformed line.
//
// Billable = type "assistant" with a usage object carrying input_tokens
// and output_tokens. Skipped the way ccusage skips them: user messages,
// summaries and other non-assistant types; model "<synthetic>" (Claude
// Code's locally generated assistant notices — no API usage); and
// isApiErrorMessage entries.
func parseLine(line []byte, src adapters.Source, path string, lineIdx int,
	project, sessionFromName string) (core.Event, bool, error) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return core.Event{}, false, err
	}
	if rec.Type != "assistant" || rec.Message == nil || rec.Message.Usage == nil {
		return core.Event{}, false, nil
	}
	if rec.Message.Model == "<synthetic>" || rec.IsApiErrorMessage {
		return core.Event{}, false, nil
	}
	u := rec.Message.Usage
	if u.InputTokens == nil || u.OutputTokens == nil {
		return core.Event{}, false, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return core.Event{}, false, fmt.Errorf("timestamp %q: %w", rec.Timestamp, err)
	}

	// message.id is opaque — real logs carry UUID-shaped ids, not only
	// msg_*. Both native ids present → the ccusage dedup key; either
	// missing → per-occurrence fallback (ccusage does not dedup those),
	// keyed by the source-relative path (project dir + basename) so equal
	// filenames across project dirs cannot collide.
	var id string
	if rec.Message.ID != "" && rec.RequestID != "" {
		id = core.EventID(harnessName, rec.Message.ID, rec.RequestID)
	} else {
		fileRel := project + "/" + filepath.Base(path)
		id = core.FallbackID(harnessName, fileRel, lineIdx, rec.Timestamp)
	}

	raw, err := core.SanitizeRaw(line)
	if err != nil {
		return core.Event{}, false, err
	}

	meta := map[string]any{}
	if len(u.CacheCreation) > 0 {
		// per-TTL cache-write detail (ephemeral_5m/1h split)
		var cc any
		if err := json.Unmarshal(u.CacheCreation, &cc); err == nil {
			meta["cache_creation"] = cc
		}
	}
	if rec.Cwd != "" {
		meta["cwd"] = rec.Cwd
	}
	if rec.Version != "" {
		meta["client_version"] = rec.Version
	}
	if rec.GitBranch != "" {
		meta["git_branch"] = rec.GitBranch
	}
	if len(meta) == 0 {
		meta = nil
	}

	sessionID := rec.SessionID
	if sessionID == "" {
		sessionID = sessionFromName
	}

	return core.Event{
		ID:          id,
		TS:          ts.UTC(),
		Machine:     src.Machine,
		SourceKind:  core.SourceKindHarnessLog,
		Harness:     harnessName,
		Provider:    "anthropic",
		Model:       rec.Message.Model,
		ModelFamily: rec.Message.Model, // unknown models pass through raw until the mapping milestone
		Project:     project,
		SessionID:   sessionID,
		RequestID:   rec.RequestID,
		TokensInput: *u.InputTokens, TokensOutput: *u.OutputTokens,
		TokensCacheWrite: u.CacheCreationInputTokens,
		TokensCacheRead:  u.CacheReadInputTokens,
		Accuracy:         core.AccuracyExact,
		Meta:             meta,
		Raw:              raw,
	}, true, nil
}
