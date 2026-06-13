package hub

// Canonical IANA zone validation (M6 Codex F4). time.LoadLocation is too
// permissive to use as an IANA check: besides "Local" it also resolves
// host-magic names that exist as files in a host zoneinfo directory but
// are not zones — "localtime", "posixrules" — so "LoadLocation succeeded"
// does not mean "is IANA". We validate timezone= by MEMBERSHIP in the
// canonical set instead (an allowlist also stays correct as new magic
// names appear), then resolve the membership-checked name.

import (
	_ "embed"
	"strings"
)

// ianaZonesRaw is the entry-name list of the Go toolchain's embedded
// zoneinfo (time/tzdata) — every real zone and link the binary can
// resolve (UTC, Asia/Kolkata, its alias Asia/Calcutta, Etc/*, GMT, …),
// and NONE of the host-magic non-zones. Regenerate from
// $GOROOT/lib/time/zoneinfo.zip when the toolchain's tzdata bumps:
//
//	python3 -c 'import zipfile;print("\n".join(sorted(n for n in \
//	  zipfile.ZipFile(Z).namelist() if not n.endswith("/"))))' > iana_zones.txt
//
// Zone names are stable and new ones are rare, so a slightly stale list
// only over-rejects a brand-new zone (clear error, easily regenerated) —
// it never accepts a magic name.
//
//go:embed iana_zones.txt
var ianaZonesRaw string

var ianaZones = func() map[string]struct{} {
	names := strings.Split(strings.TrimSpace(ianaZonesRaw), "\n")
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		if n != "" {
			m[n] = struct{}{}
		}
	}
	return m
}()

// isIANAZone reports whether name is in the canonical IANA zone set —
// the allowlist timezone= is validated against.
func isIANAZone(name string) bool {
	_, ok := ianaZones[name]
	return ok
}
