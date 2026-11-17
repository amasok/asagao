//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Stopped by a signal (Ctrl-C), a dry run removes its copy before the process ends.
func TestDryRunRemovesCopyOnSignal(t *testing.T) {
	exited := make(chan struct{})
	old := exitOnSignal
	exitOnSignal = func() { close(exited) }
	t.Cleanup(func() { exitOnSignal = old })

	dir := t.TempDir()
	cp := filepath.Join(dir, dryRunPrefix+"x")
	if err := os.MkdirAll(filepath.Join(cp, "store.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	unwatch := removeOnSignal(cp)
	defer unwatch()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the signal was not handled")
	}
	if _, err := os.Stat(cp); !os.IsNotExist(err) {
		t.Errorf("the copy is still there: %v", err)
	}
}

// Once the dry run is over, a signal is no longer taken by it.
func TestDryRunUnwatch(t *testing.T) {
	called := make(chan struct{}, 1)
	old := exitOnSignal
	exitOnSignal = func() { called <- struct{}{} }
	t.Cleanup(func() { exitOnSignal = old })
	cp := t.TempDir()
	unwatch := removeOnSignal(cp)
	unwatch()
	unwatch() // twice is fine
	select {
	case <-called:
		t.Fatal("exited")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(cp); err != nil {
		t.Errorf("removed after unwatch: %v", err)
	}
}
