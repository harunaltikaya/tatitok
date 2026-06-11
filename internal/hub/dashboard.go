package hub

// Dashboard serving (M4 Task 4): the Vite bundle embedded in web.Dist
// is served at / — fully self-contained, no external origins (web's
// TestDistNoExternalOrigins is the build check). A binary built without
// the bundle (bare `go build`, no node) serves an explainer instead;
// `make build` always builds the bundle first.

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/harunaltikaya/tatitok/web"
)

func (h *Hub) registerDashboard(mux *http.ServeMux) {
	dist, err := fs.Sub(web.Dist, "dist")
	if err == nil {
		if _, statErr := fs.Stat(dist, "index.html"); statErr == nil {
			// http.FileServerFS serves index.html for "/" and the
			// hashed assets under /assets/ (GET/HEAD only by its own
			// rules). Registered method-less so the method-less /api/
			// catch-all stays the more specific pattern and wins.
			mux.Handle("/", http.FileServerFS(dist))
			return
		}
	}
	slog.Warn("dashboard bundle not embedded in this binary — / serves an explainer (build with `make build`)")
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("tatitok hub running — API at /api/v1.\n" +
			"The dashboard bundle is not embedded in this binary: build with `make build` (requires node/npm, versions pinned in web/.nvmrc).\n"))
	})
}
