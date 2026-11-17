package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// searchLockWait is how long a change of searches.toml waits for another one (another
// `asagao searches save`, say) to finish. A change takes milliseconds.
var searchLockWait = 10 * time.Second

// errLockHeld is what tryLockFile returns while another process (or goroutine) holds it.
var errLockHeld = errors.New("lock held")

// lockSearches takes the lock file of the saved searches at path, waiting up to
// searchLockWait, so that one change at a time reads and rewrites the file. The lock is
// released by unlock, or when the process exits.
func lockSearches(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(searchLockWait)
	for {
		err := tryLockFile(f)
		if err == nil {
			return func() { _ = unlockFile(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, errLockHeld) {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%s: another change of the saved searches is in progress", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
