package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// Resetting the store from the TUI or CLI: unsent changes (and the record of dropped
// changes, which Reset clears) are written to a JSON file first, so nothing the user
// wrote is lost without a copy.

// ResetBackup is the JSON file written before a reset.
type ResetBackup struct {
	CreatedAt string    `json:"created_at"`
	Account   string    `json:"account"` // the Asana user gid the data was synced with
	Outbox    []Op      `json:"outbox"`
	Discarded []Discard `json:"discarded"`
}

// ResetResult says what ResetWithBackup did (readmodel.ResetResult).
type ResetResult = readmodel.ResetResult

// UnsentCount is the number of outbox entries not in Asana (any state).
func (s *Store) UnsentCount(ctx context.Context) (int, error) {
	n, _, err := s.UnsentState(ctx)
	return n, err
}

// UnsentState is the number of unsent changes and the id of the newest one (0 if none):
// what a user confirms before a reset (see ResetUpTo).
func (s *Store) UnsentState(ctx context.Context) (n int, newest int64, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT count(*), COALESCE(max(id), 0) FROM outbox").Scan(&n, &newest)
	return n, newest, err
}

// ResetWithBackup resets the store (see ResetUpTo). upTo is the newest unsent change the
// user agreed to delete (0: none, AnyUnsent: whatever there is); if a newer one exists,
// nothing is deleted and the error is ErrUnsentChanges (with their number), so the user
// can be asked again. Before deleting anything it writes the unsent changes and dropped
// changes to a new file in backupDir; if that fails nothing is deleted.
func (s *Store) ResetWithBackup(ctx context.Context, backupDir string, upTo int64, now time.Time) (ResetResult, error) {
	var res ResetResult
	ops, err := s.ListOps(ctx)
	if err != nil {
		return res, err
	}
	for _, op := range ops {
		if upTo != AnyUnsent && op.ID > upTo {
			res.Unsent = len(ops)
			return res, fmt.Errorf("%d 件: %w", len(ops), ErrUnsentChanges)
		}
	}
	discarded, err := s.RecentDiscards(ctx, -1)
	if err != nil {
		return res, err
	}
	if len(ops)+len(discarded) > 0 {
		account, _ := s.SyncState(ctx, keyMeGID)
		path, err := writeResetBackup(backupDir, ResetBackup{CreatedAt: formatTime(now), Account: account, Outbox: ops, Discarded: discarded}, now)
		if err != nil {
			return res, fmt.Errorf("バックアップを書き出せなかったため、初期化をやめました: %w", err)
		}
		res.BackupPath = path
	}
	res.Unsent, err = s.ResetUpTo(ctx, upTo, now)
	if errors.Is(err, ErrUnsentChanges) { // queued after we looked (another process)
		return res, fmt.Errorf("%d 件: %w", res.Unsent, err)
	}
	return res, err
}

func writeResetBackup(dir string, b ResetBackup, now time.Time) (string, error) {
	if dir == "" {
		return "", errors.New("バックアップ先がありません")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "reset-"+now.Local().Format("20060102-150405")+"-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return filepath.Abs(f.Name())
}
