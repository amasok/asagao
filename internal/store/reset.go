package store

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// ErrUnsentChanges is readmodel.ErrUnsentChanges.
var ErrUnsentChanges = readmodel.ErrUnsentChanges

// ResetReason is readmodel.ResetReason.
const ResetReason = readmodel.ResetReason

// Reset deletes all local data: tasks, projects, comments, attachments, the outbox, the
// record of dropped changes and sync state including the account (KeyMeGID), so the store
// can be used with another Asana account. It is one transaction, taken under the push
// lock (ErrPushBusy if another process is sending).
//
// unsent is the number of outbox entries not in Asana (pending, sending, failed or
// conflict). If there are any and force is false, nothing is deleted and the error is
// ErrUnsentChanges; with force they are deleted too.
//
// Unsent changes deleted with force are kept as dropped changes (PendingDiscards, with
// their payload), like changes to things deleted in Asana.
//
// Files cached for attachments (downloaded, or copied for an upload) are deleted after
// the data, best effort: they belong to the old account. The push lock file stays: another
// process may hold it, and a new file would let two processes push at once.
//
// Other processes with the store open (TUI, MCP server) see the empty store on their next
// read; their writes to tasks that no longer exist fail with ErrNotFound. A sync in
// another process holds the push lock for its whole run, so Reset cannot happen between
// its push and pull (which would bring old data back): Reset returns ErrPushBusy instead.
func (s *Store) Reset(ctx context.Context, force bool, now time.Time) (unsent int, err error) {
	upTo := int64(0) // no unsent change may be deleted
	if force {
		upTo = AnyUnsent
	}
	return s.ResetUpTo(ctx, upTo, now)
}

// AnyUnsent lets ResetUpTo delete every unsent change, however many there are.
const AnyUnsent = readmodel.AnyUnsent

// ResetUpTo is Reset for a reset the user confirmed after seeing the unsent changes: it
// deletes them only if none was queued after outbox entry upTo (the newest one shown;
// 0 = there were none). Otherwise nothing is deleted and the error is ErrUnsentChanges,
// so the user can be asked again. AnyUnsent deletes them all (Reset with force).
func (s *Store) ResetUpTo(ctx context.Context, upTo int64, now time.Time) (unsent int, err error) {
	unlock, err := s.TryLockPush()
	if err != nil {
		return 0, err
	}
	defer unlock()
	var files []string
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var maxID int64
		if err := tx.QueryRowContext(ctx, "SELECT count(*), COALESCE(max(id), 0) FROM outbox").Scan(&unsent, &maxID); err != nil {
			return err
		}
		if upTo != AnyUnsent && maxID > upTo {
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
		// Unsent changes deleted with force are recorded (with what the user wrote) as
		// dropped changes, so the front ends tell the user once and the text can be copied.
		// Older records belong to the old data and go.
		if _, err := tx.ExecContext(ctx, "DELETE FROM discarded_changes"); err != nil {
			return err
		}
		idRows, err := tx.QueryContext(ctx, "SELECT id FROM outbox ORDER BY id")
		if err != nil {
			return err
		}
		ids, err := scanIDs(idRows)
		if err != nil {
			return err
		}
		for _, id := range ids {
			op, err := getOpTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := recordDiscard(ctx, tx, op, ResetReason, now); err != nil {
				return err
			}
		}
		// Children first; deleting tasks and stories also clears the full-text indexes
		// (triggers).
		for _, table := range []string{
			"task_memberships", "task_tags", "task_followers", "task_details", "attachments", "stories",
			"task_custom_fields", "project_custom_fields", "custom_fields", "task_dependencies", "task_refs",
			"tasks", "sections", "project_members", "status_updates", "projects", "tags", "users",
			"workspaces", "outbox", "sync_state",
			"notifications",
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
