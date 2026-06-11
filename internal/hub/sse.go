package hub

// SSE live stream (M4 Task 3): GET /api/v1/stream emits ingest-pass
// summaries (counts by harness, replaced/new), the rollup-touched UTC
// days, and a heartbeat every ~15s.
//
// Reconnect semantics — the simpler option, chosen and documented per
// the milestone: there is NO Last-Event-ID replay buffer. Events carry
// increasing ids, but a client that reconnects (or receives the `stale`
// event below) simply refetches the REST endpoints; the stream's only
// job is "something changed, here is roughly what".
//
// Backpressure: a slow client must never block ingest. publish() does a
// non-blocking send into each client's bounded buffer; on overflow the
// event is dropped and the client marked stale — the next event it does
// receive is preceded by `event: stale` telling it to refetch.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultHeartbeat is the SSE keep-alive cadence.
const DefaultHeartbeat = 15 * time.Second

// sseBuffer is each client's pending-event capacity; overflow drops
// events and marks the client stale (it refetches).
const sseBuffer = 16

// sseWriteTimeout bounds every single write to a stream client (M4
// Codex round, finding 2): a client that stops reading fills its socket
// buffers and would otherwise block the handler in Write indefinitely —
// past srv.Shutdown's grace. With the per-write deadline (plus the stop
// watcher that shortens it to "now" at shutdown) a handler outlives
// streamStop by at most this long, strictly under the shutdown grace.
const sseWriteTimeout = 2 * time.Second

type sseEvent struct {
	id   int64
	name string
	data []byte
}

type sseClient struct {
	ch      chan sseEvent
	dropped atomic.Int64
}

type broadcaster struct {
	mu      sync.Mutex
	clients map[*sseClient]struct{}
	lastID  atomic.Int64
}

func newBroadcaster() *broadcaster {
	return &broadcaster{clients: map[*sseClient]struct{}{}}
}

func (b *broadcaster) add(c *sseClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clients[c] = struct{}{}
}

func (b *broadcaster) remove(c *sseClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.clients, c)
}

// publish fans v out to every connected client WITHOUT ever blocking:
// the caller is the ingest path.
func (b *broadcaster) publish(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return // a marshal failure must never break ingest
	}
	ev := sseEvent{id: b.lastID.Add(1), name: name, data: data}
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		select {
		case c.ch <- ev:
		default:
			c.dropped.Add(1)
		}
	}
}

// passSummary is the ingest_pass event payload.
type passSummary struct {
	Pass        int64                `json:"pass"`
	At          string               `json:"at"` // RFC3339 UTC
	Harnesses   []harnessPassSummary `json:"harnesses"`
	TouchedDays []string             `json:"touched_days"` // UTC days whose rollups changed
}

type harnessPassSummary struct {
	Harness     string `json:"harness"`
	Files       int    `json:"files"`
	Events      int    `json:"events"`
	New         int    `json:"new"`
	Replaced    int    `json:"replaced"`
	ParseErrors int    `json:"parseErrors"`
}

// apiStream is the SSE handler. It exits when the client goes away or
// the hub shuts down (streamStop) — long-lived responses must not hold
// the server drain open.
func (h *Hub) apiStream(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, http.StatusInternalServerError, "no_stream", "response writer cannot stream")
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	c := &sseClient{ch: make(chan sseEvent, sseBuffer)}
	h.bcast.add(c)
	defer h.bcast.remove(c)

	// Stop watcher (finding 2): a write blocked on a stuck client's full
	// socket must observe shutdown. Closing streamStop (or the client
	// vanishing) slams the write deadline to "now", aborting an in-flight
	// Write immediately; the per-write deadline below bounds the race
	// where one more write re-arms after the slam.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-h.streamStop:
		case <-r.Context().Done():
		case <-watchDone:
			return
		}
		_ = rc.SetWriteDeadline(time.Now())
	}()

	write := func(ev sseEvent) bool {
		select {
		case <-h.streamStop:
			return false // never re-arm the deadline after stop
		default:
		}
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.id, ev.name, ev.data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	// hello documents the protocol to the client: no replay — refetch
	// REST on reconnect or on `stale`.
	hello, _ := json.Marshal(map[string]string{"reconnect": "refetch", "version": h.version})
	if !write(sseEvent{id: h.bcast.lastID.Load(), name: "hello", data: hello}) {
		return
	}

	heartbeat := h.cfg.Heartbeat
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.streamStop:
			return
		case ev := <-c.ch:
			if n := c.dropped.Swap(0); n > 0 {
				stale, _ := json.Marshal(map[string]int64{"dropped": n})
				if !write(sseEvent{id: ev.id, name: "stale", data: stale}) {
					return
				}
			}
			if !write(ev) {
				return
			}
		case <-tick.C:
			beat, _ := json.Marshal(map[string]string{"at": time.Now().UTC().Format(time.RFC3339)})
			if !write(sseEvent{id: h.bcast.lastID.Load(), name: "heartbeat", data: beat}) {
				return
			}
		}
	}
}
