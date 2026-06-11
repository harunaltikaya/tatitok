package hub

// The watch layer (M4 Task 1): fsnotify where the filesystem delivers
// events, polling where it does not (opencode's live SQLite store, or
// any source whose fsnotify setup fails — the fallback engages
// automatically and is logged). Changes debounce into ingest passes;
// each pass goes through adapters.IngestFiles — the same store APIs and
// replacement semantics as CLI ingest, no new write paths. Replacements
// are counted into one summary line per harness per pass, never logged
// per event here (store.QuietReplacements; M3.1 summarization
// precedent).

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/harunaltikaya/tatitok/internal/adapters"
	"github.com/harunaltikaya/tatitok/internal/pricing"
	"github.com/harunaltikaya/tatitok/internal/store"
)

const (
	// DefaultDebounce coalesces rapid successive changes into one
	// ingest pass (milestone default ~1–2s).
	DefaultDebounce = 1500 * time.Millisecond
	// DefaultPollInterval drives the polling sources (opencode primary;
	// fsnotify fallback for the others).
	DefaultPollInterval = 5 * time.Second
)

// WatchTarget pairs an adapter with one of its detected sources.
type WatchTarget struct {
	Adapter adapters.Adapter
	Source  adapters.Source
}

// fileEvent is one Match-mapped change notice headed for the debouncer.
type fileEvent struct {
	target *watchTarget
	path   string // the ingest unit (WatchSpec.Match output)
}

type watchTarget struct {
	adapter adapters.Adapter
	src     adapters.Source
	spec    adapters.WatchSpec
	// polled: this target is on the polling path — either by spec
	// (PollOnly) or because fsnotify setup failed (automatic fallback).
	polled atomic.Bool
}

type watcher struct {
	st        *store.Store
	overrides *pricing.Overrides
	debounce  time.Duration
	pollEvery time.Duration
	targets   []*watchTarget

	events chan fileEvent
	passes atomic.Int64 // completed ingest passes (tests; SSE in Task 3)

	cancelLoop context.CancelFunc // stops watching and the debounce loop
	cancelPass context.CancelFunc // hard-aborts an in-flight pass (Stop timeout)
	wg         sync.WaitGroup
	done       chan struct{}
}

// startWatcher wires the goroutines: one fsnotify loop for all notify
// targets, one poller for polling targets (which also runs the startup
// catch-up scan for everyone), one debounce/ingest loop.
func startWatcher(st *store.Store, ov *pricing.Overrides, targets []WatchTarget, debounce, pollEvery time.Duration) *watcher {
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	passCtx, cancelPass := context.WithCancel(context.Background())
	w := &watcher{
		st: st, overrides: ov,
		debounce: debounce, pollEvery: pollEvery,
		events:     make(chan fileEvent, 1024),
		cancelLoop: cancelLoop, cancelPass: cancelPass,
		done: make(chan struct{}),
	}
	for _, t := range targets {
		w.targets = append(w.targets, &watchTarget{
			adapter: t.Adapter, src: t.Source,
			spec: t.Adapter.WatchSpec(t.Source),
		})
	}
	for _, t := range w.targets {
		if t.spec.PollOnly {
			t.polled.Store(true)
		}
	}

	w.wg.Add(1)
	go func() { defer w.wg.Done(); w.notifyLoop(loopCtx) }()
	w.wg.Add(1)
	go func() { defer w.wg.Done(); w.pollLoop(loopCtx) }()
	w.wg.Add(1)
	go func() { defer w.wg.Done(); w.debounceLoop(loopCtx, passCtx) }()
	go func() { w.wg.Wait(); close(w.done) }()
	return w
}

// Stop ends watching and waits for an in-flight ingest pass to complete
// (the shutdown contract: watchers stopped, in-flight batch completes).
// If ctx expires first, the pass is hard-aborted — its open per-file
// transaction rolls back, committed files stay; the next pass or a
// backfill reconciles (idempotent).
func (w *watcher) Stop(ctx context.Context) {
	w.cancelLoop()
	select {
	case <-w.done:
	case <-ctx.Done():
		slog.Warn("shutdown grace expired — aborting in-flight ingest pass (per-file transaction rolls back)")
		w.cancelPass()
		<-w.done
	}
	w.cancelPass()
}

// emit hands a change notice to the debouncer without ever blocking the
// watch loops: when the buffer is full the notice is dropped — safe,
// because every dropped change is re-detected (appends keep changing
// the file; polling re-stats it) and ingest is idempotent.
func (w *watcher) emit(ctx context.Context, t *watchTarget, path string) {
	ev := fileEvent{target: t, path: path}
	select {
	case w.events <- ev:
	case <-ctx.Done():
	default:
		slog.Warn("watch event buffer full — change notice dropped (will be re-detected)",
			"harness", t.src.Harness, "path", path)
	}
}

// ---- fsnotify ----

// notifyLoop owns one fsnotify.Watcher for every non-polling target.
// Directories are watched recursively (fsnotify itself is not
// recursive): the root tree is added at start, directories created
// later are added on their Create event and scanned for content that
// landed before the watch took effect. Any setup failure flips the
// target to polling — the automatic fallback the milestone requires.
func (w *watcher) notifyLoop(ctx context.Context) {
	dirTarget := map[string]*watchTarget{}
	var notify []*watchTarget
	for _, t := range w.targets {
		if !t.polled.Load() {
			notify = append(notify, t)
		}
	}
	if len(notify) == 0 {
		return
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("fsnotify unavailable — all watch sources fall back to polling",
			"error", err, "interval", w.pollEvery.String())
		for _, t := range notify {
			t.polled.Store(true)
		}
		return
	}
	defer func() { _ = fsw.Close() }()

	addTree := func(t *watchTarget, root string) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil //nolint:nilerr // unreadable subtrees are backfill's problem; watch what we can
			}
			if err := fsw.Add(path); err != nil {
				return err
			}
			dirTarget[path] = t
			return nil
		})
	}
	for _, t := range notify {
		if err := addTree(t, t.src.Root); err != nil {
			slog.Warn("fsnotify setup failed — source falls back to polling",
				"harness", t.src.Harness, "root", t.src.Root,
				"error", err, "interval", w.pollEvery.String())
			t.polled.Store(true)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			slog.Warn("fsnotify error", "error", err)
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			t := dirTarget[filepath.Dir(ev.Name)]
			if t == nil || t.polled.Load() {
				continue
			}
			if ev.Op.Has(fsnotify.Create) {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					// New directory (a project dir, a codex day dir):
					// watch it, then scan for files created before the
					// watch took effect.
					if err := addTree(t, ev.Name); err != nil {
						slog.Warn("fsnotify add failed — source falls back to polling",
							"harness", t.src.Harness, "dir", ev.Name, "error", err)
						t.polled.Store(true)
						continue
					}
					w.scanExisting(ctx, t, ev.Name)
					continue
				}
			}
			if !ev.Op.Has(fsnotify.Create) && !ev.Op.Has(fsnotify.Write) {
				continue
			}
			if unit := t.spec.Match(ev.Name); unit != "" {
				w.emit(ctx, t, unit)
			}
		}
	}
}

// scanExisting emits every matching file already under dir (used right
// after a directory watch is added, closing the create-then-write race).
func (w *watcher) scanExisting(ctx context.Context, t *watchTarget, dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best effort; polling and backfill are the safety nets
		}
		if unit := t.spec.Match(path); unit != "" {
			w.emit(ctx, t, unit)
		}
		return nil
	})
}

// ---- polling ----

// pollState is one file's last observed stat.
type pollState struct {
	mtime time.Time
	size  int64
}

// pollLoop stats candidate files on a fixed interval for every polling
// target, and — once, at startup — for ALL targets: the catch-up scan.
// The baseline comes from the sources table (mtime/size recorded at the
// last ingest), so files unchanged since then are not re-read; anything
// new or different is emitted and ingested through the normal pass.
func (w *watcher) pollLoop(ctx context.Context) {
	seen := map[string]pollState{}
	if base, err := w.st.SourceStates(ctx); err == nil {
		for path, s := range base {
			seen[path] = pollState{mtime: s.MTime, size: s.Size}
		}
	} else if ctx.Err() == nil {
		slog.Warn("catch-up baseline unavailable — re-reading watched files once", "error", err)
	}

	scan := func(t *watchTarget) {
		for _, path := range w.candidates(t) {
			st, err := os.Stat(path)
			if err != nil {
				continue // gone or unreadable; backfill bookkeeping owns that story
			}
			cur := pollState{mtime: st.ModTime().UTC(), size: st.Size()}
			if prev, ok := seen[path]; ok && prev.size == cur.size && prev.mtime.Equal(cur.mtime) {
				continue
			}
			seen[path] = cur
			if unit := t.spec.Match(path); unit != "" {
				w.emit(ctx, t, unit)
			}
		}
	}

	// Catch-up: one scan over every target, polling or not — sessions
	// written while the hub was down ingest within the first debounce
	// window of serve start.
	for _, t := range w.targets {
		scan(t)
	}

	tick := time.NewTicker(w.pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, t := range w.targets {
				if t.polled.Load() {
					scan(t)
				}
			}
		}
	}
}

// candidates lists the files a polling scan stats for one target:
// the spec's explicit list (opencode: db + -wal, no tree walk), or a
// walk of the root filtered through Match.
func (w *watcher) candidates(t *watchTarget) []string {
	if len(t.spec.PollPaths) > 0 {
		return t.spec.PollPaths
	}
	var out []string
	_ = filepath.WalkDir(t.src.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // unreadable subtrees are backfill's problem
		}
		if t.spec.Match(path) != "" {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// ---- debounce + ingest ----

// debounceLoop collects change notices and runs ingest passes: the
// first notice arms a timer; everything arriving within the window
// joins the same pass (bounded latency = one window). Passes run
// synchronously here, so shutdown after the loop exits means no pass is
// in flight.
func (w *watcher) debounceLoop(loopCtx, passCtx context.Context) {
	dirty := map[*watchTarget]map[string]struct{}{}
	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-loopCtx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case ev := <-w.events:
			if dirty[ev.target] == nil {
				dirty[ev.target] = map[string]struct{}{}
			}
			dirty[ev.target][ev.path] = struct{}{}
			if fire == nil {
				timer = time.NewTimer(w.debounce)
				fire = timer.C
			}
		case <-fire:
			fire, timer = nil, nil
			batch := dirty
			dirty = map[*watchTarget]map[string]struct{}{}
			w.runPass(passCtx, batch)
		}
	}
}

// runPass ingests one debounced batch, target by target in registration
// order, and emits ONE summary line per harness (replacements counted
// here, never per event — AS-4 stays reported, the log stays readable).
// A failing target is logged and does not stop the others; the watcher
// keeps running.
func (w *watcher) runPass(ctx context.Context, batch map[*watchTarget]map[string]struct{}) {
	for _, t := range w.targets {
		files := batch[t]
		if len(files) == 0 {
			continue
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		start := time.Now()
		sum, err := adapters.IngestFiles(ctx, w.st, t.adapter, t.src, paths, w.overrides)
		if err != nil {
			slog.Error("watch ingest pass failed — will retry on next change",
				"harness", t.src.Harness, "files", len(paths), "error", err)
			continue
		}
		slog.Info("watch ingest pass",
			"harness", t.src.Harness, "files", sum.Files,
			"lines", sum.Lines, "events", sum.Emitted,
			"new", sum.Inserted, "replaced", sum.Replaced,
			"empty_model", sum.EmptyModel, "parse_errors", sum.ParseErrors,
			"skipped", sum.Skipped,
			"duration_ms", time.Since(start).Milliseconds())
	}
	w.passes.Add(1)
}
