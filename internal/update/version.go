// Package update downloads a release of Tether Agent, checks it, and replaces the binary.
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is a parsed MAJOR.MINOR.PATCH[-pre] version.
type semver struct {
	nums [3]int
	pre  string
}

// IsDev returns true for the version of a binary that -ldflags did not set.
func IsDev(v string) bool {
	return v == "" || v == "dev"
}

// Compare returns -1 if a is older than b, 0 if they are equal, and 1 if a is newer.
// A version without a pre-release is newer than the same version with one.
func Compare(a, b string) (int, error) {
	va, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range va.nums {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] < vb.nums[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case va.pre == vb.pre:
		return 0, nil
	case va.pre == "":
		return 1, nil
	case vb.pre == "":
		return -1, nil
	}
	return strings.Compare(va.pre, vb.pre), nil
}

// parseVersion accepts an optional "v" prefix and an optional "-<pre>" suffix.
func parseVersion(v string) (semver, error) {
	bad := fmt.Errorf("bad version %s: want MAJOR.MINOR.PATCH", v)
	core, pre, hasPre := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	if hasPre && pre == "" {
		return semver{}, bad
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, bad
	}
	var s semver
	for i, p := range parts {
		// Atoi accepts a sign, so check for digits first.
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return semver{}, bad
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, bad
		}
		s.nums[i] = n
	}
	s.pre = pre
	return s, nil
}
