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
	"strings"

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
			// secureDashboard adds the document CSP / Referrer-Policy.
			mux.Handle("/", secureDashboard(http.FileServerFS(dist)))
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

// secureDashboard sets security headers on the dashboard DOCUMENT (the HTML at
// "/" or any .html path; hashed assets under /assets/ don't need a document
// CSP). The bundle is fully self-contained — web's TestDistNoExternalOrigins
// proves it loads nothing external — so default-src 'self' blocks nothing it
// needs and keeps a (hypothetically injected) script from reaching any external
// origin (connect/img/font all inherit 'self'). 'unsafe-inline' is required in
// exactly two places the bundle genuinely uses it: the inline theme-bootstrap
// script in index.html (applies the saved theme before first paint) and
// ECharts' tooltip HTML (inline style attributes). The SPA has no HTML-injection
// sink (no dangerouslySetInnerHTML; React auto-escapes), so inline execution has
// no untrusted entry point. A stricter script-src (nonce/hash, which needs the
// inline script externalized) is a follow-up. Referrer-Policy strips the
// (already same-origin) path from any outbound referer.
func secureDashboard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p == "/" || strings.HasSuffix(p, ".html") {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; base-uri 'self'; object-src 'none'; "+
					"frame-ancestors 'none'; style-src 'self' 'unsafe-inline'; "+
					"script-src 'self' 'unsafe-inline'")
			w.Header().Set("Referrer-Policy", "no-referrer")
		}
		next.ServeHTTP(w, r)
	})
}
