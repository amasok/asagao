//go:build unix

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// /dev/zero, a FIFO and the like report size 0, so they passed the 100MB check and were
// then copied without end. Only regular files are accepted now.
func TestUploadRefusesNonRegularFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cache := t.TempDir()
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/dev/zero", fifo} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		done := make(chan error, 1)
		go func() {
			_, err := s.UploadAttachmentLocal(ctx, "t1", path, cache, time.Now())
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "通常のファイルではありません") {
				t.Errorf("%s: err = %v, want refused as not a regular file", path, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: upload did not return (it is reading without a limit)", path)
		}
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Errorf("nothing should be copied: %v", entries)
	}
	if n := rowCount(t, s, "outbox"); n != 0 {
		t.Errorf("nothing should be queued, outbox has %d", n)
	}
}
