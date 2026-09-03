// Package pi ingests pi-coding-agent session logs
// (<agent-dir>/sessions/<encoded-cwd>/<timestamp>_<session-id>.jsonl).
//
// The layout mirrors Claude Code's projects tree — one directory per
// working directory, one JSONL file per session — so this adapter mirrors
// the claudecode adapter structurally. There is no external parity
// referee for pi; the counting rule is the canonical four-field one:
//
//   - one billable event per record of type "message" whose message has
//     role "assistant" and a usage object carrying input and output;
//   - usage.input/output/cacheRead/cacheWrite map to the four token
//     fields; usage.reasoning is reported separately (tokens_reasoning).
//     pi's own usage.totalTokens (input + output only) and usage.cost are
//     ignored — totals are computed from the four fields, never copied;
//   - provider and model pass through VERBATIM. pi's providers are user
//     labels for local endpoints (all 127.0.0.1 on the owner's box); the
//     pricing layer's provider rule decides the cost basis, exactly as it
//     does for opencode's local providers — the adapter invents nothing.
package pi

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
const AdapterVersion = 1

const harnessName = "pi"

type Adapter struct{}

func (Adapter) Name() string { return harnessName }

func (Adapter) Version() int { return AdapterVersion }

// Detect returns the sessions dir to ingest. pi honors
// PI_CODING_AGENT_DIR (the whole agent dir; sessions live under it) and
// PI_CODING_AGENT_SESSION_DIR (the sessions dir itself) — both read from
// pi's dist/config.js; the default is ~/.pi/agent/sessions.
func (Adapter) Detect(env adapters.Probe) ([]adapters.Source, error) {
	root := filepath.Join(env.HomeDir, ".pi", "agent", "sessions")
	if v := strings.TrimSpace(env.Getenv("PI_CODING_AGENT_DIR")); v != "" {
		root = filepath.Join(v, "sessions")
	}
	if v := strings.TrimSpace(env.Getenv("PI_CODING_AGENT_SESSION_DIR")); v != "" {
		root = v
	}
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

// record is the typed view of one log line; the full line is preserved
// separately through core.SanitizeRaw, so unknown fields survive into
// the raw column even though this struct ignores them.
type record struct {
	Type      string   `json:"type"`
	ID        string   `json:"id"`
	Timestamp string   `json:"timestamp"`
	Cwd       string   `json:"cwd"` // session record only
	Message   *message `json:"message"`
}

type message struct {
	Role       string `json:"role"`
	API        string `json:"api"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Usage      *usage `json:"usage"`
	ResponseID string `json:"responseId"`
}

type usage struct {
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  int64  `json:"cacheRead"`
	CacheWrite int64  `json:"cacheWrite"`
	Reasoning  *int64 `json:"reasoning"`
}

// session is the per-file state read from the leading session record:
// the native session id and the working directory (the project).
type session struct {
	id  string
	cwd string
}

// Backfill parses every session file under src.Root and emits one event
// per billable assistant message, in size-bounded batches (contract v2).
// Files are processed in order of their earliest record timestamp. I/O
// failure semantics are the claudecode adapter's verbatim: unreadable
// dirs/files report a skipped source; a sink error cancels the backfill.
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
		if err := ingestFile(ctx, src, f, sink); err != nil {
			return err
		}
	}
	return nil
}

// ingestFile is one file's bracketed slice of a backfill — shared by
// Backfill and BackfillFile so the two paths cannot drift.
func ingestFile(ctx context.Context, src adapters.Source, f string, sink adapters.Sink) error {
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
	return sink.FileDone(res)
}

// BackfillFile is the watcher's incremental path: one session file,
// whole-file re-read, replacement semantics verbatim.
func (Adapter) BackfillFile(ctx context.Context, src adapters.Source, path string, sink adapters.Sink) error {
	return ingestFile(ctx, src, path, sink)
}

// WatchSpec: session JSONL files under sessions/<encoded-cwd>/ — fsnotify
// on the directory tree, any .jsonl create/append re-ingests that file.
// New cwd directories are picked up by the watch layer itself: it adds
// every directory created under the root and scans it (same mechanism
// that covers claude-code's project dirs).
func (Adapter) WatchSpec(adapters.Source) adapters.WatchSpec {
	return adapters.WatchSpec{Match: matchJSONL}
}

func matchJSONL(path string) string {
	if strings.HasSuffix(path, ".jsonl") {
		return path
	}
	return ""
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
// in them. Files without any timestamp sort last; ties break on path.
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

// readLine reads one full line of any length (tool results reach many
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

// cancelCheckInterval is how many lines a parse loop reads between ctx
// checks.
const cancelCheckInterval = 1000

// backfillFile streams one session file's billable events to the sink in
// batches of at most adapters.BatchSize. readErr means the FILE could not
// be (fully) read; sinkErr (also carrying ctx cancellation) cancels the
// whole backfill unchanged.
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
	// <timestamp>_<session-id>.jsonl — the id after the underscore is the
	// fallback when the leading session record is missing.
	sess := session{cwd: project}
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if i := strings.LastIndex(base, "_"); i >= 0 {
		sess.id = base[i+1:]
	}

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
			return res, lineErr, nil
		}
		if len(line) > 0 {
			res.LineCount++
			ev, ok, perr := parseLine(line, src, path, lineIdx, project, &sess)
			switch {
			case perr != nil && errors.Is(lineErr, io.EOF):
				// Unterminated final line that does not parse: a write in
				// progress, not a malformed line.
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
// for a malformed line. A "session" record updates sess (id, cwd) and is
// itself non-billable.
//
// Billable = type "message", message.role "assistant", usage carrying
// input and output. Everything else (session/model_change/
// thinking_level_change/custom/compaction records, user and toolResult
// messages, assistant messages without usage) is skipped.
func parseLine(line []byte, src adapters.Source, path string, lineIdx int,
	project string, sess *session) (core.Event, bool, error) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return core.Event{}, false, err
	}
	if rec.Type == "session" {
		if rec.ID != "" {
			sess.id = rec.ID
		}
		if rec.Cwd != "" {
			sess.cwd = rec.Cwd
		}
		return core.Event{}, false, nil
	}
	if rec.Type != "message" || rec.Message == nil ||
		rec.Message.Role != "assistant" || rec.Message.Usage == nil {
		return core.Event{}, false, nil
	}
	u := rec.Message.Usage
	if u.Input == nil || u.Output == nil {
		return core.Event{}, false, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return core.Event{}, false, fmt.Errorf("timestamp %q: %w", rec.Timestamp, err)
	}

	// The record id is only unique within a session, so the session id is
	// the second native component (core.EventID's requestID slot — both
	// must be non-empty). Either missing → per-occurrence fallback keyed
	// by the source-relative path.
	var id string
	if rec.ID != "" && sess.id != "" {
		id = core.EventID(harnessName, rec.ID, sess.id)
	} else {
		fileRel := project + "/" + filepath.Base(path)
		id = core.FallbackID(harnessName, fileRel, lineIdx, rec.Timestamp)
	}

	raw, err := core.SanitizeRaw(line)
	if err != nil {
		return core.Event{}, false, err
	}

	var meta map[string]any
	if rec.Message.API != "" {
		meta = map[string]any{"api": rec.Message.API}
	}

	return core.Event{
		ID:          id,
		TS:          ts.UTC(),
		Machine:     src.Machine,
		SourceKind:  core.SourceKindHarnessLog,
		Harness:     harnessName,
		Provider:    rec.Message.Provider, // verbatim — never normalized
		Model:       rec.Message.Model,
		ModelFamily: rec.Message.Model,
		Project:     sess.cwd,
		SessionID:   sess.id,
		RequestID:   rec.Message.ResponseID,
		TokensInput: *u.Input, TokensOutput: *u.Output,
		TokensCacheWrite: u.CacheWrite,
		TokensCacheRead:  u.CacheRead,
		TokensReasoning:  u.Reasoning,
		Accuracy:         core.AccuracyExact,
		Meta:             meta,
		Raw:              raw,
	}, true, nil
}
