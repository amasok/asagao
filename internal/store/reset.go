package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

// ErrUnsentChanges is returned by Reset (without force) when local changes have not been
// sent to Asana yet; Reset then deletes nothing.
var ErrUnsentChanges = errors.New("Asana に送っていない変更があります")

// Reset deletes all local data: tasks, projects, comments, attachments, the outbox, the
// record of dropped changes and sync state including the account (KeyMeGID), so the store
// can be used with another Asana account. It is one transaction, taken under the push
// lock (ErrPushBusy if another process is sending).
//
// unsent is the number of outbox entries not in Asana (pending, sending, failed or
// conflict). If there are any and force is false, nothing is deleted and the error is
// ErrUnsentChanges; with force they are deleted too.
//
// Files cached for attachments (downloaded, or copied for an upload) are deleted after
// the data, best effort: they belong to the old account.
func (s *Store) Reset(ctx context.Context, force bool) (unsent int, err error) {
	unlock, err := s.TryLockPush()
	if err != nil {
		return 0, err
	}
	defer unlock()
	var files []string
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox").Scan(&unsent); err != nil {
			return err
		}
		if unsent > 0 && !force {
			return ErrUnsentChanges
		}
		rows, err := tx.QueryContext(ctx, `SELECT local_path FROM attachments WHERE local_path <> ''
			UNION SELECT json_extract(payload, '$.path') FROM outbox WHERE kind = ? AND json_extract(payload, '$.path') <> ''`, OpUploadAttachment)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			files = append(files, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// Children first; deleting tasks and stories also clears the full-text indexes
		// (triggers).
		for _, table := range []string{
			"task_memberships", "task_tags", "task_followers", "task_details", "attachments", "stories",
			"tasks", "sections", "project_members", "status_updates", "projects", "tags", "users",
			"workspaces", "outbox", "discarded_changes", "sync_state",
		} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return unsent, err
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
	return unsent, nil
}
