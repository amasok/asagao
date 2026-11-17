package gid

import "testing"

func TestIsLocal(t *testing.T) {
	for _, tc := range []struct {
		gid  string
		want bool
	}{
		{"local:0123456789abcdef", true},
		{"local:", true},
		{"local:local:x", true},
		{"1201234567890", false},
		{"", false},
		{"Local:abc", false},      // the prefix is case sensitive
		{" local:abc", false},     // not trimmed
		{"xlocal:abc", false},     // only at the start
		{"local", false},          // without the colon
		{"1200:local:abc", false}, // in the middle
	} {
		if got := IsLocal(tc.gid); got != tc.want {
			t.Errorf("IsLocal(%q) = %v, want %v", tc.gid, got, tc.want)
		}
	}
}

func TestNewLocal(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"0123456789abcdef", "local:0123456789abcdef"},
		{"", "local:"},
	} {
		got := NewLocal(tc.id)
		if got != tc.want {
			t.Errorf("NewLocal(%q) = %q, want %q", tc.id, got, tc.want)
		}
		if !IsLocal(got) {
			t.Errorf("IsLocal(NewLocal(%q)) = false", tc.id)
		}
	}
}
