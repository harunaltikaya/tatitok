package limits

// HTTP boundary for the display-only limits feature. This file lives in
// package limits ON PURPOSE: it imports only the standard library (net/http,
// net, encoding/json, io, errors), so the ingest path is STRUCTURALLY incapable
// of importing the verified packages (store / pricing / parity / modelmap) —
// the fence is by construction, not convention. The hub owns the *Store and
// calls RegisterHTTP in Start; the handler is handed ONLY a *Store, never the
// event store.
//
// The JSON write / error helpers are duplicated from the hub's api.go rather
// than shared — sharing would require importing the hub (an import cycle) or a
// helper package, either of which would breach the stdlib-only fence. The error
// envelope shape is kept identical so the API surface stays uniform.

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
)

// maxBody caps the POST body — a normalized snapshot is a handful of windows
// per provider; anything larger is malformed or hostile.
const maxBody = 64 << 10 // 64 KiB

// RegisterHTTP mounts the display-only limits endpoints on mux against st. The
// routes are registered EXPLICITLY (the hub's get() helper is GET/HEAD-only and
// would 405 a POST); the method-specific patterns win over the hub's "/api/"
// catch-all for this exact path, and the method-less pattern returns a JSON 405
// (Allow: GET, HEAD, POST) for any other method.
func RegisterHTTP(mux *http.ServeMux, st *Store) {
	h := &handler{st: st}
	mux.HandleFunc("GET /api/v1/limits", h.get)
	mux.HandleFunc("POST /api/v1/limits", h.post)
	mux.HandleFunc("/api/v1/limits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET, HEAD, POST")
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed",
			r.Method+" is not supported on /api/v1/limits (only GET, HEAD and POST)")
	})
}

// handler carries ONLY the display-only store — never the event store.
type handler struct{ st *Store }

// get returns the latest snapshot under "providers". An absent snapshot
// (nothing POSTed since start) is a clean empty object, NOT an error — the
// dashboard renders its "waiting for companion extension" state.
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) > 0 {
		writeErr(w, http.StatusBadRequest, "bad_param", "GET /api/v1/limits takes no query parameters")
		return
	}
	snap := h.st.Get()
	if snap == nil {
		snap = Snapshot{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": snap})
}

// post ingests a normalized snapshot from the companion extension. Loopback
// only (403 otherwise). The body is size-capped and decoded as a SINGLE JSON
// value: trailing data is rejected, a literal null is rejected (it must not
// clear the stored snapshot), and the payload must validate. Any failure is a
// 400 that leaves the stored snapshot untouched. Success replaces it
// (last-write-wins) and returns 204.
func (h *handler) post(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		writeErr(w, http.StatusForbidden, "forbidden",
			"POST /api/v1/limits accepts loopback callers only")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	var snap Snapshot
	if err := dec.Decode(&snap); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid limits payload: "+err.Error())
		return
	}
	if snap == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "limits payload must be an object, not null")
		return
	}
	// Exactly one JSON value: a second Decode must hit EOF. Anything else
	// (a trailing value or garbage) is malformed.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad_request", "unexpected trailing data after the JSON body")
		return
	}
	if err := snap.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	h.st.Set(snap)
	w.WriteHeader(http.StatusNoContent)
}

// isLoopback reports whether a request RemoteAddr ("ip:port") is a loopback
// address. It FAILS CLOSED: a RemoteAddr that does not split into host:port is
// treated as non-loopback (no fallback to parsing the raw string), so only a
// well-formed loopback host:port is ever admitted.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- JSON helpers, duplicated from hub/api.go to keep this package stdlib-only ---

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var e apiError
	e.Error.Code, e.Error.Message = code, msg
	writeJSON(w, status, e)
}
