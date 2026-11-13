package store

import (
	"errors"
	"os"
)

// ErrPushBusy is returned by TryLockPush when another process (or goroutine) is sending
// the outbox.
var ErrPushBusy = errors.New("別のプロセスが未送信の変更を送信中です")

// TryLockPush takes the push lock: a lock file next to the database, so only one process
// (TUI, CLI, MCP server) sends the outbox at a time. It does not wait: if the lock is held
// it returns ErrPushBusy. The lock is released by unlock, or when the process exits.
func (s *Store) TryLockPush() (unlock func(), err error) {
	f, err := os.OpenFile(s.path+".push.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := tryLockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = unlockFile(f)
		f.Close()
	}, nil
}
