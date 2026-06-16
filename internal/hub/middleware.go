package hub

// Serving-surface access control. tatitok is local-only: the listener binds a
// loopback address (Start refuses anything else) and this middleware is the
// single gate in front of the WHOLE handler tree — dashboard/static, every
// /api route, the SSE stream, and both /api/v1/limits GET and POST — rejecting
// any request whose TCP peer is not loopback. It is defense-in-depth on top of
// the bind: even a future misconfiguration, or a handler mounted by mistake,
// can never answer a non-loopback peer. Remote access is expected over a tunnel
// (ssh -L / Tailscale), which terminates locally and arrives as a loopback peer.

import (
	"net"
	"net/http"
)

// loopbackOnly wraps next so only loopback peers reach it; everything else gets
// a 403. The original ResponseWriter is passed through UNCHANGED, so streaming
// handlers (SSE) keep their http.Flusher / http.ResponseController.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackPeer(r.RemoteAddr) {
			http.Error(w, "tatitok serves loopback callers only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackPeer reports whether a request RemoteAddr ("ip:port") is a loopback
// address. It uses net.IP.IsLoopback — which covers 127.0.0.0/8 AND ::1, the
// latter mattering on macOS where localhost often resolves to ::1 — never a
// "127." string match. It FAILS CLOSED: a RemoteAddr that does not split into
// host:port with a parseable IP is treated as non-loopback.
func loopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackBind reports whether the bound listener address is loopback. An
// unspecified bind (0.0.0.0 / ::) reaches every interface and is NOT loopback,
// so Start refuses it. Uses net.IP.IsLoopback for the same 127/8 + ::1 reasons
// as loopbackPeer.
func loopbackBind(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
