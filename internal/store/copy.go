package store

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// CopyTo writes a consistent copy of the database to the new file dst (VACUUM INTO), for a
// dry run: a write made on the copy never reaches this database or its outbox. dst must not
// exist yet; it is created with 0600 permissions (the copy holds the same data).
func (s *Store) CopyTo(ctx context.Context, dst string) error {
	// Create the file first so that its permissions are ours, then let VACUUM INTO fill it:
	// it writes into an existing file only when that file is empty.
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return errors.Join(fmt.Errorf("copy the store: %w", err), os.Remove(dst))
	}
	return nil
}
