//go:build unix

package repoctx

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A FIFO is refused without blocking on it.
func TestLoadRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	if _, err := Load(fifo); err == nil || !strings.Contains(err.Error(), "通常のファイル") {
		t.Errorf("fifo: %v", err)
	}
}
