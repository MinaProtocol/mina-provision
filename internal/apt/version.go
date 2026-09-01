package apt

import (
	"strconv"
	"strings"
)

// CompareVersions orders two Debian version strings the way dpkg does, and
// returns a negative number, zero, or a positive number as a sorts before,
// equal to, or after b.
//
// This is not a convenience: Mina package versions such as
// "3.4.0-bd0fe9e" and "3.4.0-stop-2967b39" do not order the same way under
// string comparison as they do under Debian rules, and picking "the latest"
// by string order or by upload order silently selects the wrong package. The
// algorithm below is dpkg's verrevcmp, which is the only definition of
// "latest" the repository itself uses.
//
// A version is [epoch:]upstream[-revision]. The epoch is compared as an
// integer, then the upstream part, then the revision.
func CompareVersions(a, b string) int {
	ae, au, ar := splitVersion(a)
	be, bu, br := splitVersion(b)

	if ae != be {
		if ae < be {
			return -1
		}
		return 1
	}
	if c := compareFragment(au, bu); c != 0 {
		return c
	}
	return compareFragment(ar, br)
}

// splitVersion breaks a version into its epoch, upstream and revision parts.
// A missing epoch is 0 and a missing revision is the empty string, which is
// what dpkg compares against.
func splitVersion(v string) (epoch int, upstream, revision string) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, ":"); i >= 0 {
		if n, err := strconv.Atoi(v[:i]); err == nil {
			epoch = n
			v = v[i+1:]
		}
	}
	// The revision is what follows the *last* hyphen, so an upstream version
	// that itself contains hyphens stays intact.
	if i := strings.LastIndex(v, "-"); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

// order gives a character its dpkg sort weight. Digits are handled separately
// by the caller, the tilde sorts before everything including the end of the
// string (so "1.0~rc1" precedes "1.0"), letters sort before all other
// characters, and everything else sorts after them.
func order(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return 0
	case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		return int(c)
	case c == '~':
		return -1
	case c == 0:
		return 0
	default:
		return int(c) + 256
	}
}

// compareFragment compares one part of a version (upstream or revision) by
// alternating between non-digit runs, compared character by character with
// order(), and digit runs, compared numerically with leading zeros ignored.
func compareFragment(a, b string) int {
	at := func(s string, i int) byte {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }

	i, j := 0, 0
	for i < len(a) || j < len(b) {
		firstDiff := 0

		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			ac, bc := order(at(a, i)), order(at(b, j))
			if ac != bc {
				return ac - bc
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		for i < len(a) && isDigit(a[i]) && j < len(b) && isDigit(b[j]) {
			if firstDiff == 0 {
				firstDiff = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		// A longer run of digits is a larger number.
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if firstDiff != 0 {
			return firstDiff
		}
	}
	return 0
}
