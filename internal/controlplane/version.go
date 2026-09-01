package controlplane

import (
	"strconv"
	"strings"
)

// versionBelow reports whether data-plane version v is older than the
// operator-set minimum. Both sides accept an optional leading "v" and a
// pre-release suffix after "-" (ignored: "1.2.0-rc1" compares as 1.2.0 —
// good enough for an ADVISORY channel; a release process that needs
// pre-release ordering can encode it in the numeric parts). Comparison is
// numeric per dot-separated part, missing parts read as 0.
//
// An unparseable v — "", "dev", a git hash — is BELOW any minimum: the
// operator set a fleet minimum precisely to smoke out builds of unknown
// provenance, and advice is the only consequence. An unparseable minimum
// disables nothing loudly; it simply judges every plane stale, which the
// operator notices immediately on the dataplanes view.
func versionBelow(v, min string) bool {
	vp, ok := versionParts(v)
	if !ok {
		return true
	}
	mp, _ := versionParts(min)
	for i := 0; i < len(vp) || i < len(mp); i++ {
		var a, b int
		if i < len(vp) {
			a = vp[i]
		}
		if i < len(mp) {
			b = mp[i]
		}
		if a != b {
			return a < b
		}
	}
	return false
}

func versionParts(s string) ([]int, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return nil, false
	}
	fields := strings.Split(s, ".")
	parts := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, false
		}
		parts[i] = n
	}
	return parts, true
}
