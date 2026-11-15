package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// copyFile reads at most the limit, even if the file grew after it was checked.
func TestCopyFileStopsAtLimit(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(src)
	if _, err := copyFile(src, dst, info, 10); !errors.Is(err, errTooLarge) {
		t.Fatalf("err = %v, want errTooLarge", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("partial copy should be removed: %v", err)
	}
	if n, err := copyFile(src, dst, info, 16); err != nil || n != 16 {
		t.Fatalf("copy within the limit: n=%d err=%v", n, err)
	}
}

// A file swapped for another one between the check and the copy is refused.
func TestCopyFileRefusesSwappedFile(t *testing.T) {
	dir := t.TempDir()
	src, other := filepath.Join(dir, "src"), filepath.Join(dir, "other")
	_ = os.WriteFile(src, []byte("a"), 0o600)
	_ = os.WriteFile(other, []byte("b"), 0o600)
	info, _ := os.Stat(other)
	if _, err := copyFile(src, filepath.Join(dir, "dst"), info, 10); err == nil {
		t.Fatal("a different file than the one checked must be refused")
	}
}
