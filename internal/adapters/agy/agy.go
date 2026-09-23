// Package agy ingests the Antigravity CLI (agy) usage log written by the
// tatitok statusLine hook (extension/agy-statusline/):
// $XDG_DATA_HOME/tatitok/agy/statusline.jsonl, one JSON status object per
// line, appended only when a conversation's running totals changed.
//
// agy itself keeps no per-call usage on disk; the hook's status objects
// carry each conversation's RUNNING totals (context_window.
// total_input_tokens / total_output_tokens). So this adapter derives
// events from the totals, never copies a per-call figure:
//
//   - per conversation_id, in file order, one event per line where either
//     total INCREASED: tokens_input = Δtotal_in, tokens_output =
//     Δtotal_out (the first line of a conversation is a delta from 0);
//   - a line where a total DECREASED (agy /compact or a reset) resets the
//     baseline to that line's totals and emits nothing; the next event of
//     the conversation carries meta.baseline_reset=true;
//   - a same-totals line (the hook dedupes, but tolerate it) emits nothing;
//   - event ID = EventID(agy, conversation_id, "<resets>/<total_in>/
//     <total_out>"), where resets counts the conversation's decreases so
//     far (its reset epoch): totals reached again after a reset get a new
//     ID, while appends and malformed lines (which never advance resets)
//     leave every earlier ID unchanged;
//   - cache write/read are 0 — the status object's current_usage is the
//     LAST call of a possibly multi-call turn and cannot be summed
//     honestly, so it rides along verbatim in meta instead;
//   - accuracy "derived" (running-total deltas, not per-call figures);
//   - harness "agy", provider "google" (the seller of every agy model,
//     Claude/GPT-OSS ones included), model = a slug of the display label
//     (Slug — table-free; effort/thinking parentheticals go to meta).
//
// The hook's "quota" object is limits data already fed to the hub
// separately: it is dropped from raw and meta, and any "email" key is
// stripped again defensively (the hook already removes it). A malformed
// line is a parse error, never an abort — a short-written trailing line
// is possible by design.
package agy

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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/core"
)

// AdapterVersion is bumped whenever format handling changes.
// v2: reset epoch in the event ID (all event IDs changed).
const AdapterVersion = 2

const (
	harnessName  = "agy"
	providerName = "google"
	logFileName  = "statusline.jsonl"
)

type Adapter struct{}

func (Adapter) Name() string { return harnessName }

func (Adapter) Version() int { return AdapterVersion }

// Detect returns the hook's data dir ($XDG_DATA_HOME/tatitok/agy, default
// ~/.local/share/tatitok/agy) when it exists; the log file inside it is
// the single source.
func (Adapter) Detect(env adapters.Probe) ([]adapters.Source, error) {
	base := strings.TrimSpace(env.Getenv("XDG_DATA_HOME"))
	if base == "" {
		base = filepath.Join(env.HomeDir, ".local", "share")
	}
	root := filepath.Join(base, "tatitok", "agy")
	st, err := os.Stat(root)
	switch {
	case err == nil && st.IsDir():
		return []adapters.Source{{Harness: harnessName, Root: root, Machine: env.Machine}}, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		slog.Warn("cannot probe candidate log root",
			"adapter", harnessName, "root", root, "error", err)
	}
	return nil, nil
}

// Backfill ingests the one log file under src.Root. A root without the
// file (hook installed, nothing logged yet) is simply empty.
func (Adapter) Backfill(ctx context.Context, src adapters.Source, sink adapters.Sink) error {
	path := filepath.Join(src.Root, logFileName)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return ingestFile(ctx, src, path, sink)
}

// BackfillFile is the watcher's incremental path: whole-file re-read,
// replacement semantics (deterministic IDs) verbatim.
func (Adapter) BackfillFile(ctx context.Context, src adapters.Source, path string, sink adapters.Sink) error {
	return ingestFile(ctx, src, path, sink)
}

// WatchSpec: the log file itself, by name, anywhere under the root.
func (Adapter) WatchSpec(adapters.Source) adapters.WatchSpec {
	return adapters.WatchSpec{Match: matchLog}
}

func matchLog(path string) string {
	if filepath.Base(path) == logFileName {
		return path
	}
	return ""
}

func ingestFile(ctx context.Context, src adapters.Source, path string, sink adapters.Sink) error {
	if err := sink.FileStart(path); err != nil {
		return err
	}
	res, readErr, sinkErr := backfillFile(ctx, src, path, sink)
	if sinkErr != nil {
		return sinkErr
	}
	if readErr != nil {
		slog.Warn("skipping unreadable log file",
			"adapter", harnessName, "file", path, "error", readErr)
		res = adapters.FileResult{Path: path, ReadError: readErr.Error()}
	}
	return sink.FileDone(res)
}

// record is the typed view of one status line; the full line (minus
// quota/email) is preserved separately through core.SanitizeRaw.
type record struct {
	ConversationID string `json:"conversation_id"`
	LoggedAt       string `json:"logged_at"`
	Cwd            string `json:"cwd"`
	Model          *struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Effort      string `json:"effort"`
	} `json:"model"`
	ContextWindow *struct {
		TotalInput   *int64          `json:"total_input_tokens"`
		TotalOutput  *int64          `json:"total_output_tokens"`
		CurrentUsage json.RawMessage `json:"current_usage"`
	} `json:"context_window"`
}

// baseline is the per-conversation running-total state within one file.
type baseline struct {
	in, out int64
	reset   bool // a decrease was seen since the last emitted event
	resets  int  // decreases seen so far: the reset epoch in the event ID
}

// readLine reads one full line of any length.
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

const cancelCheckInterval = 1000

// backfillFile streams the file's derived events to the sink in batches
// of at most adapters.BatchSize (I/O semantics mirror the pi adapter).
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
	res = adapters.FileResult{Path: path, MTime: st.ModTime().UTC(), Size: st.Size()}

	state := map[string]*baseline{}
	var batch []core.Event
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := sink.EmitBatch(path, batch)
		res.Events += len(batch)
		batch = nil
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
			ev, ok, perr := parseLine(line, src, state)
			switch {
			case perr != nil && errors.Is(lineErr, io.EOF):
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

// parseLine returns (event, true, nil) when the line advances a
// conversation's totals, (zero, false, nil) for a valid line that emits
// nothing (first zero line, same totals, a decrease → baseline reset),
// and an error for a malformed line. state is updated in place.
func parseLine(line []byte, src adapters.Source, state map[string]*baseline) (core.Event, bool, error) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return core.Event{}, false, err
	}
	if rec.ConversationID == "" {
		return core.Event{}, false, errors.New("missing conversation_id")
	}
	if rec.ContextWindow == nil || rec.ContextWindow.TotalInput == nil || rec.ContextWindow.TotalOutput == nil {
		return core.Event{}, false, errors.New("missing context_window totals")
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.LoggedAt)
	if err != nil {
		return core.Event{}, false, fmt.Errorf("logged_at %q: %w", rec.LoggedAt, err)
	}
	in, out := *rec.ContextWindow.TotalInput, *rec.ContextWindow.TotalOutput

	b := state[rec.ConversationID]
	if b == nil {
		b = &baseline{}
		state[rec.ConversationID] = b
	}
	if in < b.in || out < b.out {
		// /compact or reset: new baseline, nothing billable on this line.
		b.in, b.out, b.reset = in, out, true
		b.resets++
		return core.Event{}, false, nil
	}
	if in == b.in && out == b.out {
		return core.Event{}, false, nil // same totals: nothing new
	}
	dIn, dOut := in-b.in, out-b.out
	wasReset := b.reset
	b.in, b.out, b.reset = in, out, false

	label := ""
	if rec.Model != nil {
		label = rec.Model.DisplayName
		if label == "" {
			label = rec.Model.ID
		}
	}
	model, effort, thinking := Slug(label)
	if effort == "" && rec.Model != nil {
		effort = strings.ToLower(strings.TrimSpace(rec.Model.Effort))
	}

	meta := map[string]any{"label": label}
	if effort != "" {
		meta["effort"] = effort
	}
	if thinking {
		meta["thinking"] = true
	}
	if wasReset {
		meta["baseline_reset"] = true
	}
	if cu := rec.ContextWindow.CurrentUsage; len(cu) > 0 && string(cu) != "null" {
		var usage map[string]any
		if err := json.Unmarshal(cu, &usage); err == nil {
			stripKeys(usage, droppedKeys)
			meta["current_usage"] = usage
		}
	}

	raw, err := sanitizeLine(line)
	if err != nil {
		return core.Event{}, false, err
	}

	return core.Event{
		ID: core.EventID(harnessName, rec.ConversationID,
			strconv.Itoa(b.resets)+"/"+strconv.FormatInt(in, 10)+"/"+strconv.FormatInt(out, 10)),
		TS:          ts.UTC(),
		Machine:     src.Machine,
		SourceKind:  core.SourceKindHarnessLog,
		Harness:     harnessName,
		Provider:    providerName,
		Model:       model,
		ModelFamily: model,
		Project:     rec.Cwd,
		SessionID:   rec.ConversationID,
		TokensInput: dIn, TokensOutput: dOut,
		Accuracy: core.AccuracyDerived,
		Meta:     meta,
		Raw:      raw,
	}, true, nil
}

// sanitizeLine drops the quota object and any email key from the status
// object, then runs the shared sanitizer.
func sanitizeLine(line []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(string(line)))
	dec.UseNumber()
	var node any
	if err := dec.Decode(&node); err != nil {
		return nil, err
	}
	stripKeys(node, droppedKeys)
	b, err := json.Marshal(node)
	if err != nil {
		return nil, err
	}
	return core.SanitizeRaw(b)
}

// droppedKeys never reach raw or meta: quota is limits data the hook
// already POSTs to the hub; email is stripped again defensively.
var droppedKeys = map[string]bool{"quota": true, "email": true}

func stripKeys(node any, drop map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			if drop[k] {
				delete(v, k)
				continue
			}
			stripKeys(val, drop)
		}
	case []any:
		for _, val := range v {
			stripKeys(val, drop)
		}
	}
}

var (
	parenRe    = regexp.MustCompile(`\(([^)]*)\)`)
	nonSlugRe  = regexp.MustCompile(`[^a-z0-9.]+`)
	multiDash  = regexp.MustCompile(`-+`)
	effortSet  = map[string]bool{"low": true, "medium": true, "high": true}
	thinkingRe = regexp.MustCompile(`^thinking$`)
)

// Slug turns agy's display label into a canonical model id with no
// lookup table: parentheticals naming an effort ("(Low|Medium|High)") or
// "(Thinking)" are lifted out (returned as effort / thinking), any other
// parenthetical stays in the slug; the rest is lowercased, runs of
// anything but [a-z0-9.] become "-", and — for claude labels only — dots
// become dashes to match the litellm keys. "Gemini 3.8 Flash (Low)" →
// gemini-3.8-flash/low; "Claude Sonnet 4.6" → claude-sonnet-4-6;
// "GPT-OSS 120B" → gpt-oss-120b. Unknown labels still get a slug.
func Slug(label string) (model, effort string, thinking bool) {
	var keep []string
	rest := parenRe.ReplaceAllStringFunc(label, func(m string) string {
		inner := strings.ToLower(strings.TrimSpace(m[1 : len(m)-1]))
		switch {
		case effortSet[inner]:
			effort = inner
		case thinkingRe.MatchString(inner):
			thinking = true
		default:
			keep = append(keep, inner)
		}
		return " "
	})
	s := strings.ToLower(strings.Join(append([]string{rest}, keep...), " "))
	s = nonSlugRe.ReplaceAllString(s, "-")
	s = strings.Trim(multiDash.ReplaceAllString(s, "-"), "-.")
	if strings.HasPrefix(s, "claude") {
		s = strings.ReplaceAll(s, ".", "-")
	}
	return s, effort, thinking
}
