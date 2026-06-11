package web

// TestDistNoExternalOrigins is the milestone-4 "no external requests"
// build check: the served bundle must reference no external origins —
// no CDN scripts, no webfont fetches, no analytics. It runs against the
// build output, so it skips on a checkout that has not run `make web`
// (CI without node); `make web` runs it right after every bundle build.

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// fetchableOrigin matches URL uses that cause a network fetch when they
// reach a browser. XML namespace identifiers (xmlns="http://www.w3.org/...")
// are protocol identifiers, never fetched, and echarts/react legitimately
// carry them — they are excluded by the negative check below.
var fetchableOrigin = regexp.MustCompile(`(?i)(src=["']https?://|href=["']https?://|url\(["']?https?://|fetch\(["']https?://|import\(["']https?://|new WebSocket\(["'](ws|http)s?://|EventSource\(["']https?://)`)

// bareOrigin finds any absolute URL at all; every hit must be a known
// inert pattern (namespace identifiers, license pointers in code).
var bareOrigin = regexp.MustCompile(`https?://[a-zA-Z0-9.-]+`)

var inertOrigins = map[string]bool{
	"http://www.w3.org":          true, // XML/SVG namespace identifiers
	"https://reactjs.org":        true, // react error-decoder URL in error MESSAGES (printed, not fetched)
	"https://react.dev":          true,
	"https://echarts.apache.org": true, // echarts docs pointers in error messages
	"https://github.com":         true, // license/source pointers in comments
	"https://registry.npmjs.org": true,
	"https://tailwindcss.com":    true, // tailwind's /*! ... */ license banner in the CSS
}

func TestDistNoExternalOrigins(t *testing.T) {
	if _, err := fs.Stat(Dist, "dist/index.html"); err != nil {
		t.Skip("web bundle not built (run `make web`); the check runs as part of every web build")
	}
	err := fs.WalkDir(Dist, "dist", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(Dist, path)
		if err != nil {
			return err
		}
		content := string(b)
		if m := fetchableOrigin.FindString(content); m != "" {
			t.Errorf("%s: fetchable external reference %q — the dashboard must make no external requests", path, m)
		}
		for _, u := range bareOrigin.FindAllString(content, -1) {
			ok := false
			for inert := range inertOrigins {
				if strings.HasPrefix(u, inert) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s: unexpected absolute URL %q — add to inertOrigins only if it is provably never fetched", path, u)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
