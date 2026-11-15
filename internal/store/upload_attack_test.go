//go:build unix

package store

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The file is checked (Stat), then opened. If it is replaced by a FIFO in between,
// opening it must not wait forever for a writer.
func TestCopyFileDoesNotBlockOnSwappedFIFO(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "a.pdf")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(regular)
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "swapped.pdf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := copyFile(fifo, filepath.Join(dir, "out"), want, MaxUploadSize)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO swapped in after the check was copied")
		}
	case <-time.After(3 * time.Second):
		// unblock the goroutine before failing
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		t.Fatal("copyFile blocked opening a FIFO swapped in after the check")
	}
}

// Exactly the limit is accepted; one byte more is refused (sparse files, so the test is cheap).
func TestUploadSizeBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	for _, tc := range []struct {
		size int64
		ok   bool
	}{{MaxUploadSize, true}, {MaxUploadSize + 1, false}} {
		path := filepath.Join(dir, "f.bin")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(tc.size); err != nil {
			t.Fatal(err)
		}
		f.Close()
		cache := t.TempDir()
		_, err = s.UploadAttachmentLocal(ctx, "t1", path, cache, time.Now())
		if (err == nil) != tc.ok {
			t.Errorf("size %d: err = %v, want ok=%v", tc.size, err, tc.ok)
		}
		if !tc.ok {
			if entries, _ := os.ReadDir(cache); len(entries) != 0 {
				t.Errorf("size %d: refused file left in the cache", tc.size)
			}
		}
	}
}
