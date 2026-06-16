package hub

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoopbackBind pins which bound listener addresses are accepted (loopback)
// vs refused (everything else, including the unspecified all-interfaces bind).
func TestLoopbackBind(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.5", true}, // all of 127.0.0.0/8 is loopback
		{"::1", true},       // macOS often resolves localhost to ::1
		{"192.168.1.5", false},
		{"0.0.0.0", false}, // unspecified = all interfaces, not loopback
		{"::", false},
	}
	for _, c := range cases {
		addr := &net.TCPAddr{IP: net.ParseIP(c.ip), Port: 8284}
		if got := loopbackBind(addr); got != c.want {
			t.Errorf("loopbackBind(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestLoopbackPeer mirrors the limits-package gate: IsLoopback covers 127/8 and
// ::1, and a RemoteAddr without a parseable host:port fails closed.
func TestLoopbackPeer(t *testing.T) {
	cases := []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:12345", true},
		{"127.0.0.5:80", true},
		{"[::1]:443", true},
		{"192.168.1.5:80", false},
		{"203.0.113.7:9999", false},
		{"0.0.0.0:1", false},
		// Fail closed: no parseable host:port (incl. a bare IP without a port).
		{"127.0.0.1", false},
		{"::1", false},
		{"garbage", false},
		{"", false},
	}
	for _, c := range cases {
		if got := loopbackPeer(c.remote); got != c.want {
			t.Errorf("loopbackPeer(%q) = %v, want %v", c.remote, got, c.want)
		}
	}
}

// TestLoopbackOnly: the middleware passes loopback peers through to next and
// 403s everything else before next runs.
func TestLoopbackOnly(t *testing.T) {
	var reached bool
	h := loopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// Loopback peer → reaches next, 200.
	reached = false
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !reached || rec.Code != http.StatusOK {
		t.Errorf("loopback peer: reached=%v code=%d, want true/200", reached, rec.Code)
	}

	// Non-loopback peer → 403, next never runs.
	reached = false
	req = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "203.0.113.7:9999" // TEST-NET-3, not loopback
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached {
		t.Error("non-loopback peer reached the wrapped handler — must be blocked")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-loopback peer code = %d, want 403", rec.Code)
	}
}

// TestStartRefusesNonLoopbackBind: binding an all-interfaces address succeeds at
// the OS level but must be refused by Start with a tunnel-pointer error — and no
// hub is returned. (0.0.0.0:0 binds an ephemeral port on all interfaces; the
// refusal closes it immediately, before the store is opened.)
func TestStartRefusesNonLoopbackBind(t *testing.T) {
	h, err := Start(Config{DBPath: filepath.Join(t.TempDir(), "x.db"), Addr: "0.0.0.0:0"})
	if err == nil {
		if h != nil {
			ctx, cancel := contextWithTimeout(t)
			defer cancel()
			_ = h.Shutdown(ctx)
		}
		t.Fatal("Start on 0.0.0.0 succeeded, want refusal")
	}
	if h != nil {
		t.Error("Start returned a hub alongside the refusal error — want nil")
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Errorf("refusal error = %q, want it to mention non-loopback", err.Error())
	}
}
