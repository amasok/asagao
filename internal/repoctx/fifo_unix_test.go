//go:build unix

package repoctx

import (
	"os"
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

type statInfo struct {
	os.FileInfo
	st *syscall.Stat_t
}

func (s statInfo) Sys() any { return s.st }

// Someone else's file is refused (it could be in a shared folder such as /tmp).
func TestOwnedByMe(t *testing.T) {
	me := uint32(os.Getuid())
	if !ownedByMe(statInfo{st: &syscall.Stat_t{Uid: me}}) || ownedByMe(statInfo{st: &syscall.Stat_t{Uid: me + 1}}) {
		t.Error("ownedByMe")
	}
}
