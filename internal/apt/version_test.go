package apt

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int // -1, 0 or 1
	}{
		{"3.4.0-bd0fe9e", "3.4.0-bd0fe9e", 0},
		{"3.3.0-8c0c2e6", "3.3.1-7b34378", -1},
		{"3.4.0-bd0fe9e", "3.3.1-7b34378", 1},
		{"3.10.0-abc", "3.9.0-abc", 1},              // numeric, not lexicographic
		{"3.4.0-3f038cb", "3.4.0-stop-2967b39", -1}, // digits sort before letters
		{"1.0~rc1", "1.0", -1},                      // tilde sorts before the empty string
		{"1.0~rc1", "1.0~rc2", -1},
		{"1:1.0", "2.0", 1},          // a higher epoch wins over a higher upstream version
		{"3.4.0", "3.4.0-1", -1},     // a revision sorts after none
		{"3.4.0-0001", "3.4.0-1", 0}, // leading zeros are ignored
	}
	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		if sign(got) != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
		if rev := CompareVersions(tt.b, tt.a); sign(rev) != -tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want sign %d (antisymmetry)", tt.b, tt.a, rev, -tt.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
