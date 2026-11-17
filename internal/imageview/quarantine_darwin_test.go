package imageview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMarkQuarantine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.pdf")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markQuarantine(p); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, err := unix.Getxattr(p, quarantineAttr, buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "0081;") || !strings.Contains(string(buf[:n]), ";asagao;") {
		t.Fatalf("quarantine attribute = %q, %v", buf[:n], err)
	}
	// An existing attribute (e.g. approved by the user) is kept.
	if err := unix.Setxattr(p, quarantineAttr, []byte("00c1;0;Safari;"), 0); err != nil {
		t.Fatal(err)
	}
	if err := markQuarantine(p); err != nil {
		t.Fatal(err)
	}
	n, _ = unix.Getxattr(p, quarantineAttr, buf)
	if string(buf[:n]) != "00c1;0;Safari;" {
		t.Errorf("existing attribute overwritten: %q", buf[:n])
	}
}
