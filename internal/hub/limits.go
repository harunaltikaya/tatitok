package hub

// Display-only usage-limits ingest (M9 chunk 1). This is a FENCED, additive
// surface: the reported limits the companion extension POSTs here over loopback
// are shown on the dashboard and never enter the event store, pricing, rollups
// or the parity pipeline. The store lives in internal/limits (stdlib-only); the
// only coupling to tatitok is these two HTTP handlers and one Hub field (lim).
//
// The endpoints are registered EXPLICITLY (not through registerAPI's get()
// helper, which is GET/HEAD-only and would 405 a POST). POST is loopback-only;
// GET is the dashboard's read path and returns a clean EMPTY result before the
// first poll, so the frontend empty-state works without an error.

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/harunaltikaya/tatitok/internal/limits"
)

// maxLimitsBody caps the POST body — a normalized snapshot is a handful of
// windows per provider; anything larger is malformed or hostile.
const maxLimitsBody = 64 << 10 // 64 KiB

// registerLimits mounts the display-only limits endpoints. The method-specific
// patterns win over registerAPI's "/api/" catch-all for this exact path; the
// method-less pattern returns the JSON 405 (Allow: GET, HEAD, POST) for any
// other method — the same contract as the get() helper, but admitting POST.
func (h *Hub) registerLimits(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/limits", h.apiLimitsGet)
	mux.HandleFunc("POST /api/v1/limits", h.apiLimitsPost)
	mux.HandleFunc("/api/v1/limits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET, HEAD, POST")
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed",
			r.Method+" is not supported on /api/v1/limits (only GET, HEAD and POST)")
	})
}

// apiLimitsGet returns the latest reported snapshot under "providers". An
// absent snapshot (nothing POSTed since start) is a clean empty object, NOT an
// error — the dashboard renders its "waiting for companion extension" state.
func (h *Hub) apiLimitsGet(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_param", err.Error())
		return
	}
	snap := h.lim.Get()
	if snap == nil {
		snap = limits.Snapshot{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": snap})
}

// apiLimitsPost ingests a normalized snapshot from the companion extension.
// Loopback-only (defense in depth — the hub binds loopback by default): a
// non-loopback caller gets 403. The body is size-capped, decoded and
// validated; a malformed payload is a 400. Success replaces the stored
// snapshot (last-write-wins) and returns 204.
func (h *Hub) apiLimitsPost(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		writeErr(w, http.StatusForbidden, "forbidden",
			"POST /api/v1/limits accepts loopback callers only")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLimitsBody)
	var snap limits.Snapshot
	if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid limits payload: "+err.Error())
		return
	}
	if err := snap.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	h.lim.Set(snap)
	w.WriteHeader(http.StatusNoContent)
}

// isLoopback reports whether a request RemoteAddr ("ip:port") is a loopback
// address — the same posture as nonLoopbackWarning, applied per request. A
// RemoteAddr that doesn't parse is treated as non-loopback (fail closed).
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
