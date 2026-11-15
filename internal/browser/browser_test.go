package browser

import (
	"strings"
	"testing"
)

// Only https URLs reach `open` / xdg-open (nothing is started for the refused ones).
func TestOpenRefusesNonHTTPS(t *testing.T) {
	for _, u := range []string{
		"file:///Applications/Calculator.app", "/etc/passwd", "http://app.asana.com/0/1/2",
		"x-apple.systempreferences:com.apple.preference.security", "javascript:alert(1)", "https://", "",
	} {
		if err := Open(u); err == nil || !strings.Contains(err.Error(), "https 以外") {
			t.Errorf("Open(%q) = %v", u, err)
		}
	}
}
