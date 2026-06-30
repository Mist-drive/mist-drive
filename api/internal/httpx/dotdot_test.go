package httpx

import "testing"

func TestHasDotDotSegment(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// Real parent-directory traversal — must be rejected.
		{"..", true},
		{"../etc/passwd", true},
		{"a/../b", true},
		{"a/b/..", true},
		{"foo/../../bar", true},
		{"/..", true},

		// Consecutive dots inside a legitimate name — must pass.
		{"archive..tar.gz", false},
		{"v1..2", false},
		{"photo..backup.jpg", false},
		{"a..b/c..d", false},
		{"...", false},
		{"a/b/c.txt", false},
		{"", false},
		{"normal/path/file.bin", false},
	}
	for _, tc := range cases {
		if got := hasDotDotSegment(tc.key); got != tc.want {
			t.Errorf("hasDotDotSegment(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
