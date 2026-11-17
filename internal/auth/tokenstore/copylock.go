package tokenstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// copyLockWait is how long lockCopy waits for another process holding the lock (copying a
// login to 1Password takes a few `op` calls, about a second each).
var copyLockWait = 30 * time.Second

// errLockHeld is what tryLock returns while another process (or goroutine) holds the lock.
var errLockHeld = errors.New("lock held")

// lockCopy takes the lock file at path, waiting up to copyLockWait (or until ctx ends), so
// that only one process at a time copies a login of the former name or saves a new one. The
// lock is released by unlock, or when the process exits.
func lockCopy(ctx context.Context, path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(copyLockWait)
	for {
		err := tryLock(f)
		if err == nil {
			return func() { _ = unlockFile(f); f.Close() }, nil
		}
		if !errors.Is(err, errLockHeld) || time.Now().After(deadline) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
