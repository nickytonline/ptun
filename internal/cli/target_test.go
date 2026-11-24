package cli

import "testing"

func TestParseTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
		err      bool
	}{
		{"8081", "localhost:8081", false},
		{"http://localhost:8081", "localhost:8081", false},
		{"http://localhost:8081/path", "localhost:8081", false},
		{"localhost:3000", "localhost:3000", false},
		{"http://example.test", "example.test:80", false},
		{"https://example.test", "example.test:443", false},
		{"http://[::1]:9000", "[::1]:9000", false},
		{"0", "", true},
		{"70000", "", true},
		{"ftp://x:21", "", true},
		{"", "", true},
		{"http://:80", "", true},
	}
	for _, tc := range tests {
		got, err := ParseTarget(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("ParseTarget(%q) = %q, %v; want %q err=%v", tc.in, got, err, tc.want, tc.err)
		}
	}
}
