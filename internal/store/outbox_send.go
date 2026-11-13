package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/amasok/asana-tui/internal/asana"
)

// Sending outbox entries (used by sync.Push).
//
// An entry goes pending → sending (BeginSend) → removed (Complete*) once Asana accepted it.
// Each Complete* method updates the local tables with Asana's response and removes the
// entry in one transaction, so a crash can never leave a sent entry queued (which would
// send it again) or a removed entry whose local gids were not replaced.
//
// An entry still "sending" when Push starts was interrupted (the process died while it was
// being sent): Asana may or may not have received it. RecoverInterruptedOps queues it again
// if sending it twice is harmless, and otherwise marks it failed for the user to check.

// OpSending is the state of an entry while Push is sending it.
const OpSending = "sending"

// ErrOpSending is returned when an entry that is being sent is retried or discarded.
var ErrOpSending = errors.New("この変更は送信中です。送信が終わってからやり直してください")

// UnknownResultMessage is the last_error of an entry that may or may not have reached Asana.
const UnknownResultMessage = "送信中に中断したため、Asana に届いたか分かりません。Asana で確認して、届いていなければ再送、届いていれば破棄してください"

// SentNotSavedMessage prefixes the last_error of an entry Asana accepted but whose result
// could not be saved locally. Retrying it would send it again.
const SentNotSavedMessage = "Asana には送信済みですが、ローカルへの反映に失敗しました（再送すると重複します。破棄して同期してください）: "

// BeginSend marks a pending entry as being sent. It returns false if the entry is no
// longer pending (sent, discarded or resolved by someone else in the meantime).
func (s *Store) BeginSend(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'sending', attempts = attempts + 1 WHERE id = ? AND state = 'pending'", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseOp puts an entry that was not sent (e.g. we are offline) back in the queue.
func (s *Store) ReleaseOp(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'pending' WHERE id = ? AND state = 'sending'", id)
	return err
}

// MarkOpUnknown marks an entry that may or may not have reached Asana, so it is not sent
// again without the user's confirmation.
func (s *Store) MarkOpUnknown(ctx context.Context, id int64) error {
	return s.MarkOpFailed(ctx, id, errors.New(UnknownResultMessage))
}

// SafeToResend reports whether sending op twice has the same effect as sending it once.
// Creating things (tasks, comments, attachments, status updates, tags) is not.
func (s *Store) SafeToResend(ctx context.Context, op Op) (bool, error) {
	return safeToResend(ctx, s.db, op)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func safeToResend(ctx context.Context, q queryer, op Op) (bool, error) {
	switch op.Kind {
	case OpCreateTask, OpDuplicateTask, OpAddComment, OpUploadAttachment, OpCreateStatus:
		return false, nil
	case OpAddTag:
		tag, _ := op.Payload["tag"].(string)
		if !IsLocalGID(tag) {
			return true, nil
		}
		// The tag is created first; once that is recorded only the (harmless) addTag remains.
		var real string
		err := q.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", "tag:"+tag).Scan(&real)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && real != "", err
	}
	return true, nil
}

// RecoverInterruptedOps handles entries left "sending" by a process that died while
// sending them. It must only be called while holding the push lock (see TryLockPush).
func (s *Store) RecoverInterruptedOps(ctx context.Context) (requeued, unknown int, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM outbox WHERE state = 'sending' ORDER BY id")
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			op, err := getOpTx(ctx, tx, id)
			if err != nil {
				return err
			}
			safe, err := safeToResend(ctx, tx, *op)
			if err != nil {
				return err
			}
			if safe {
				_, err = tx.ExecContext(ctx, "UPDATE outbox SET state = 'pending' WHERE id = ?", id)
				requeued++
			} else {
				_, err = tx.ExecContext(ctx, "UPDATE outbox SET state = 'failed', last_error = ? WHERE id = ?", UnknownResultMessage, id)
				unknown++
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return requeued, unknown, err
}

// completeOp runs fn and removes entry id in one transaction.
func (s *Store) completeOp(ctx context.Context, id int64, fn func(*sql.Tx) error) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id)
		return err
	})
}

// MoveTarget is where a task created in Asana still has to be moved.
type MoveTarget struct {
	Project, Section string
	// From is the section Asana put the task in (restored if the move is discarded).
	From string
}

// CompleteCreate records that Asana created the task of entry id (create_task or
// duplicate_task) with gid real: the local gid is replaced everywhere and the entry is
// removed. If move is set, the entry becomes a pending move_task instead (keeping its
// place in the queue), so moving the task to its section is sent and retried like any move.
func (s *Store) CompleteCreate(ctx context.Context, id int64, local, real string, move *MoveTarget) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := replaceLocalTaskGID(ctx, tx, local, real); err != nil {
			return err
		}
		if move == nil || move.Section == "" {
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id)
			return err
		}
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		var name string
		_ = tx.QueryRowContext(ctx, "SELECT name FROM sections WHERE gid = ?", move.Section).Scan(&name)
		op.Kind, op.TaskGID, op.State, op.LastError = OpMoveTask, real, OpPending, ""
		op.Payload = map[string]any{"project": move.Project, "section": move.Section, "section_name": name}
		op.Base = map[string]any{"section": move.From}
		if _, err := tx.ExecContext(ctx, "UPDATE outbox SET kind = ?, task_gid = ? WHERE id = ?", op.Kind, op.TaskGID, id); err != nil {
			return err
		}
		return saveOp(ctx, tx, op)
	})
}

// CompleteComment records the comment Asana created for entry id.
func (s *Store) CompleteComment(ctx context.Context, id int64, localGID, taskGID string, st asana.Story) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalStory(ctx, tx, localGID, taskGID, st) })
}

// CompleteUpload records the attachment Asana created for entry id.
func (s *Store) CompleteUpload(ctx context.Context, id int64, local, real, viewURL string) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalAttachment(ctx, tx, local, real, viewURL) })
}

// CompleteStatus records the status update Asana created for entry id.
func (s *Store) CompleteStatus(ctx context.Context, id int64, local string, u asana.StatusUpdate) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalStatus(ctx, tx, local, u) })
}

// ensureNotSending refuses to change an entry that Push is sending.
func ensureNotSending(op *Op) error {
	if op.State == OpSending {
		return fmt.Errorf("outbox entry %d: %w", op.ID, ErrOpSending)
	}
	return nil
}
