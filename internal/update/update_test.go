package update

import "testing"

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v0.1.1", "v0.1.0", true},
	}
	for _, tt := range tests {
		got := isNewer(tt.latest, tt.current)
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"v1.2.3", true},
		{"1.2.3", true},
		{"dev", false},
		{"v1.2", false},
		{"", false},
	}
	for _, tt := range tests {
		got := parseVersion(tt.input)
		if (got != nil) != tt.valid {
			t.Errorf("parseVersion(%q) valid=%v, want %v", tt.input, got != nil, tt.valid)
		}
	}
}
