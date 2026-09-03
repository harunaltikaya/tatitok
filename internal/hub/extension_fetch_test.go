package hub

// The companion extension's outbound-fetch confinement boundary.
//
// Chrome host_permissions are ORIGIN-scoped: the path in a match pattern is
// required syntactically but ignored for the grant, so the manifest's
// "https://claude.ai/*" actually grants all of claude.ai — a path-narrowed
// pattern like "https://claude.ai/api/*" would imply a boundary Chrome does
// not enforce. The real boundary — the only thing that confines WHICH paths the
// extension touches — is the set of URLs the code actually fetches. This test
// pins that set to an allowlist, the way web's TestDistNoExternalOrigins pins
// the served bundle to "no external origins". It is the client-side companion to
// the loopback gate in middleware.go: the hub accepts only loopback callers; the
// extension calls only these provider APIs plus the loopback ingest.
//
// Two checks:
//
//	A) every fetch() target — resolving the one const-composed URL (INGEST_URL)
//	   and tolerating ${...} interpolation in the path — must match an allowed
//	   host + path prefix;
//	B) every absolute URL literal anywhere in the JS must sit on an allowed host.
//	   This host-level dragnet also covers any non-fetch request mechanism
//	   (XHR, sendBeacon, importScripts, WebSocket) a future change might add;
//	   a new mechanism that needs path-level confinement would extend check A.
//	C) the hub origin is runtime-configurable (rules.hubUrl, options page).
//	   Check A resolves the `${await hubUrl()}` interpolation to the
//	   DEFAULT_HUB_URL const; what confines the RUNTIME value is storage.js's
//	   HUB_URL_RE, so this check compiles that very regex and pins it to
//	   loopback origins only (127.0.0.1 / localhost, optional port, no path).

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const extensionDir = "../../extension/tatitok-limits"

// allowEntry is one permitted fetch target: scheme + host[:port] + a path
// prefix. A fetched URL is allowed iff it matches all three.
type allowEntry struct{ scheme, host, pathPrefix string }

var fetchAllowlist = []allowEntry{
	{"https", "claude.ai", "/api/organizations"},        // org discovery + .../<org>/usage
	{"https", "chatgpt.com", "/api/auth/session"},       // session → bearer token
	{"https", "chatgpt.com", "/backend-api/wham/usage"}, // usage
	{"http", "127.0.0.1:8284", "/api/v1/limits"},        // loopback display-only ingest
}

// allowedHosts is the host-level projection of the allowlist, used by the
// dragnet (check B). Hosts carry the port where the scheme makes it explicit.
var allowedHosts = map[string]bool{
	"claude.ai":      true,
	"chatgpt.com":    true,
	"127.0.0.1:8284": true,
	"127.0.0.1":      true, // loopback origins named in comments/validation text
	"localhost":      true,
}

var (
	// const NAME = "..." / '...'  and  const NAME = `...` (\x60 == backtick).
	constStr  = regexp.MustCompile(`const\s+(\w+)\s*=\s*["']([^"']*)["']`)
	constTmpl = regexp.MustCompile(`const\s+(\w+)\s*=\s*\x60([^\x60]*)\x60`)
	// fetch( first argument: `template` | "double" | 'single' | identifier.
	fetchArg = regexp.MustCompile(`\bfetch\s*\(\s*(?:\x60([^\x60]*)\x60|"([^"]*)"|'([^']*)'|([A-Za-z_$][\w$]*))`)
	// ${NAME} — interpolation of a bare const identifier (resolved).
	interpIdent = regexp.MustCompile(`\$\{(\w+)\}`)
	// ${...} — any remaining (dynamic) interpolation, e.g. ${encodeURIComponent(x)}.
	interpAny = regexp.MustCompile(`\$\{[^}]*\}`)
	// Absolute URL literal → capture host[:port] up to the first path/quote/${.
	absURLHost = regexp.MustCompile(`https?://([A-Za-z0-9.:_-]+)`)
	// The runtime hub origin call inside a fetch template (check C pins the
	// validator that confines its value).
	hubCall = regexp.MustCompile(`\$\{await hubUrl\(\)\}`)
	// HUB_URL_RE literal in storage.js: `export const HUB_URL_RE = /.../;`
	hubURLRe = regexp.MustCompile(`HUB_URL_RE\s*=\s*/(.*)/;`)
)

func TestExtensionFetchAllowlist(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(extensionDir, "*.js"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no *.js under %s — the extension's fetch confinement is unguarded; "+
			"if the extension moved, repoint this test at it", extensionDir)
	}

	// Collect every file's source and a map of simple string/template consts
	// (across files; const names are unique enough that one map is fine).
	consts := map[string]string{}
	sources := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		sources[f] = src
		for _, m := range constStr.FindAllStringSubmatch(src, -1) {
			consts[m[1]] = m[2]
		}
		for _, m := range constTmpl.FindAllStringSubmatch(src, -1) {
			consts[m[1]] = m[2]
		}
	}
	if _, ok := sources[filepath.Join(extensionDir, "sw.js")]; !ok {
		t.Fatalf("sw.js (the fetching file) not found among %v — it moved; update this test", files)
	}

	// Resolve ${NAME} references between consts so a composed URL like
	// INGEST_URL = `${DASHBOARD_URL_PREFIX}/api/v1/limits` becomes concrete.
	// Iterate to a fixpoint; bounded so a reference cycle can't hang.
	for i := 0; i < 10; i++ {
		changed := false
		for k, v := range consts {
			if nv := resolveConsts(v, consts); nv != v {
				consts[k] = nv
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// ---- Check A: every fetch() target is on an allowed host + path prefix ----
	sawFetch := false
	for f, src := range sources {
		for _, m := range fetchArg.FindAllStringSubmatch(src, -1) {
			sawFetch = true
			literal := firstNonEmpty(m[1], m[2], m[3]) // `template` / "double" / 'single'
			ident := m[4]

			var target string
			if ident != "" {
				v, ok := consts[ident]
				if !ok {
					t.Errorf("%s: fetch(%s) — non-literal target that is not a resolvable const; "+
						"make the URL a literal or const so confinement is statically checkable", base(f), ident)
					continue
				}
				target = v
			} else {
				target = literal
			}
			// The configurable hub origin resolves to its default (check C
			// confines the runtime value); then resolve const interpolation
			// and blank out dynamic ${...} segments.
			target = hubCall.ReplaceAllLiteralString(target, "${DEFAULT_HUB_URL}")
			target = placeholderInterp(resolveConsts(target, consts))

			u, err := url.Parse(target)
			if err != nil || u.Scheme == "" || u.Host == "" {
				t.Errorf("%s: fetch target %q is not an absolute URL after resolution", base(f), target)
				continue
			}
			if !allowedTarget(u) {
				t.Errorf("%s: fetch target %s://%s%s is outside the allowlist", base(f), u.Scheme, u.Host, u.Path)
			}
		}
	}
	if !sawFetch {
		t.Fatal("no fetch() calls found in the extension JS — the parser is not seeing the requests it must confine")
	}
	// The hub call must actually have been seen and resolved through the
	// default const, or check A would be waving through a target it never
	// parsed.
	if !hubCall.MatchString(sources[filepath.Join(extensionDir, "sw.js")]) {
		t.Fatal("sw.js: expected the ingest fetch to interpolate ${await hubUrl()}; the fetch shape changed — update this test")
	}
	if _, ok := consts["DEFAULT_HUB_URL"]; !ok {
		t.Fatal("storage.js: DEFAULT_HUB_URL const not found — check A cannot resolve the hub origin")
	}

	// ---- Check C: the runtime hub-origin validator admits loopback only ------
	checkHubURLValidator(t, sources[filepath.Join(extensionDir, "storage.js")])

	// ---- Check B: every absolute URL literal sits on an allowed host ---------
	for f, src := range sources {
		for _, m := range absURLHost.FindAllStringSubmatch(src, -1) {
			if !allowedHosts[m[1]] {
				t.Errorf("%s: absolute URL on disallowed host %q — outside the extension's allowlist", base(f), m[1])
			}
		}
	}
}

// checkHubURLValidator (check C) compiles storage.js's HUB_URL_RE in Go
// (the pattern is RE2-compatible by construction) and pins it to loopback
// origins: anything the options page would accept must be one.
func checkHubURLValidator(t *testing.T, storageSrc string) {
	t.Helper()
	m := hubURLRe.FindStringSubmatch(storageSrc)
	if m == nil {
		t.Fatal("storage.js: HUB_URL_RE not found — the hub-URL validator moved; update this test")
	}
	// JS escapes "/" inside a regex literal; Go does not need that.
	re, err := regexp.Compile(strings.ReplaceAll(m[1], `\/`, "/"))
	if err != nil {
		t.Fatalf("storage.js: HUB_URL_RE does not compile as RE2: %v", err)
	}
	for _, ok := range []string{"http://127.0.0.1:8284", "http://127.0.0.1", "http://localhost:9000", "http://localhost"} {
		if !re.MatchString(ok) {
			t.Errorf("HUB_URL_RE rejects loopback origin %q", ok)
		}
	}
	for _, bad := range []string{
		"https://127.0.0.1:8284", "http://127.0.0.1:8284/", "http://127.0.0.1:8284/api",
		"http://127.0.0.1.evil.example", "http://localhost.evil.example", "http://evil.example",
		"http://10.0.0.5:8284", "http://[::1]:8284", "http://127.0.0.1:8284?x=1",
		"http://user@127.0.0.1:8284", "ftp://127.0.0.1", " http://127.0.0.1:8284",
	} {
		if re.MatchString(bad) {
			t.Errorf("HUB_URL_RE admits non-loopback or non-origin value %q", bad)
		}
	}
}

// resolveConsts substitutes ${NAME} for a known const value, leaving unknown
// (dynamic) interpolations intact for placeholderInterp to blank out.
func resolveConsts(s string, consts map[string]string) string {
	return interpIdent.ReplaceAllStringFunc(s, func(ref string) string {
		name := interpIdent.FindStringSubmatch(ref)[1]
		if v, ok := consts[name]; ok {
			return v
		}
		return ref
	})
}

// placeholderInterp blanks any remaining ${...} (dynamic) interpolation to a
// single inert path segment, so the static prefix of the URL stays checkable.
func placeholderInterp(s string) string { return interpAny.ReplaceAllString(s, "_") }

func allowedTarget(u *url.URL) bool {
	for _, e := range fetchAllowlist {
		if u.Scheme == e.scheme && u.Host == e.host && pathUnder(u.Path, e.pathPrefix) {
			return true
		}
	}
	return false
}

// pathUnder reports whether p is the prefix path itself or a segment beneath it
// (so /api/organizations does not match /api/organizationsX).
func pathUnder(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func base(p string) string { return filepath.Base(p) }
