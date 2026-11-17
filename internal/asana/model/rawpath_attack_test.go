package model

import (
	"errors"
	"testing"
)

// Paths that could point outside the API, or smuggle something past the checks.
func TestCheckRawPathAttack(t *testing.T) {
	bad := []string{
		"", "x", "//evil/x", "///x", "/\\evil", "/a/..", "/a/../", "/..", "/./", "/a/./b",
		"/%2e%2e", "/%2E%2e/x", "/%2e/x", "/.%2e/x", "/%2e%2E%2f", "/a%2f..%2fb", "/a%2F%2e%2e%2Fb",
		"/a%5Cb", "/a%5cb", "/a%23b", "/a%3fb", "/a%00", "/a%0d%0aHost:%20evil", "/a%0a",
		"/a\tb", "/a\nb", "/a\rb", "/a b", "/a\x7f", "/a#frag", "/a?x=1#frag", "/a%", "/a%g1", "/a%2",
		"/a?x=\n", "/a?x= y", "/a?x=\\y", "/a?x=a\\",
	}
	for _, p := range bad {
		if err := CheckRawPath(p); !errors.Is(err, ErrRawPath) {
			t.Errorf("CheckRawPath(%q) = %v, want ErrRawPath", p, err)
		}
	}
	ok := []string{
		"/", "/users/me", "/tasks/1?opt_fields=name,notes", "/a/b..c/d", "/a/..b", "/a/b..", "/a/.b",
		"/search?text=../..", "/search?text=%2e%2e%2f", "/tasks/%E3%81%82", "/a?x=a%20b&y=%23",
		"/a?u=http://evil.example/",
	}
	for _, p := range ok {
		if err := CheckRawPath(p); err != nil {
			t.Errorf("CheckRawPath(%q) = %v, want nil", p, err)
		}
	}
}
