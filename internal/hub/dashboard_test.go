package hub

// Dashboard serving tests (M4 Task 4). They exercise whatever bundle is
// embedded in the test binary and skip cleanly on a checkout that has
// not built one — the bundle content itself is covered by web's
// TestDistNoExternalOrigins after every `make web`.

import (
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harunaltikaya/tatitok/web"
)

func bundleBuilt(t *testing.T) bool {
	t.Helper()
	_, err := fs.Stat(web.Dist, "dist/index.html")
	return err == nil
}

func TestDashboardServed(t *testing.T) {
	if !bundleBuilt(t) {
		t.Skip("web bundle not built (run `make web`)")
	}
	h := startHub(t, filepath.Join(t.TempDir(), "hub.db"))
	t.Cleanup(func() {
		ctx, cancel := contextWithTimeout(t)
		defer cancel()
		_ = h.Shutdown(ctx)
	})

	resp, err := http.Get("http://" + h.Addr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `<div id="root">`) {
		t.Fatalf("GET / = %d, want the dashboard index (body %.120q)", resp.StatusCode, body)
	}

	// Every embedded asset must be served with a sensible type.
	err = fs.WalkDir(web.Dist, "dist/assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		url := "http://" + h.Addr() + strings.TrimPrefix(path, "dist")
		resp, err := http.Get(url)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", url, resp.StatusCode)
		}
		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.HasSuffix(path, ".js") && !strings.Contains(ct, "javascript"):
			t.Errorf("%s served as %q", path, ct)
		case strings.HasSuffix(path, ".css") && !strings.Contains(ct, "css"):
			t.Errorf("%s served as %q", path, ct)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The API still wins routing over the file server.
	resp, err = http.Get("http://" + h.Addr() + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("/api/v1/health served as %q after dashboard mount", ct)
	}
}
