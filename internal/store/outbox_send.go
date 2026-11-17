package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// Sending outbox entries (used by sync.Push).
//
// An entry goes pending → sending (BeginSend) → removed (Complete*) once Asana accepted it.
// Each Complete* method updates the local tables with Asana's response and removes the
// entry in one transaction, so a crash can never leave a sent entry queued (which would
// send it again) or a removed entry whose local gids were not replaced.
//
// Sometimes we cannot tell whether Asana applied an entry: a 5xx answer, a dropped
// connection, or a process that died while the entry was "sending". Sending it again is
// harmless for most entries, but would duplicate a create (task, comment, attachment,
// status update, tag). Such an entry goes back to pending with verify set: the next Push
// first looks in Asana for what it would have created since sent_at, and either records
// that (no resend) or sends it again. Only when Asana cannot tell (e.g. several candidates)
// is the entry marked failed for the user.

// OpSending is the state of an entry while Push is sending it (outbox.StateSending).
const OpSending = outbox.StateSending

// ErrOpSending is returned when an entry that is being sent is retried or discarded (an
// alias of readmodel.ErrOpSending, so that internal/app can match it).
var ErrOpSending = readmodel.ErrOpSending

// AmbiguousMessage is the last_error of an entry that may have reached Asana, when Asana
// has several candidates for what it created.
const AmbiguousMessage = "Asana に届いたか自動で確かめられませんでした（同じ内容のものが複数あります）。Asana で確認して、届いていなければ再送、届いていれば破棄してください"

// BeginSend marks a pending entry as being sent at now. It returns false if the entry is no
// longer pending (sent, discarded or resolved by someone else in the meantime). An entry
// being verified keeps its first sent_at, so the check looks back far enough.
func (s *Store) BeginSend(ctx context.Context, id int64, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE outbox SET state = 'sending',
		sent_at = CASE WHEN verify = 1 AND sent_at <> '' THEN sent_at ELSE ? END
		WHERE id = ? AND state = 'pending'`, formatTime(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CountAttempt records that entry id is about to be sent (attempts counts requests that
// may have reached Asana; see sync.maxSendAttempts).
func (s *Store) CountAttempt(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET attempts = attempts + 1, last_sent_at = ? WHERE id = ?", formatTime(now), id)
	return err
}

// ReleaseNotSent puts back an entry whose request never left (no connection): the attempt
// counted for it is taken back.
func (s *Store) ReleaseNotSent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'pending', attempts = max(0, attempts - 1) WHERE id = ? AND state = 'sending'", id)
	return err
}

// ReleaseOp puts an entry that was not sent (e.g. we are offline) back in the queue.
func (s *Store) ReleaseOp(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'pending' WHERE id = ? AND state = 'sending'", id)
	return err
}

// MarkOpVerify puts back in the queue an entry that may or may not have reached Asana: the
// next push checks Asana before sending it again. note (may be empty) is shown as its
// last_error meanwhile.
func (s *Store) MarkOpVerify(ctx context.Context, id int64, note string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'pending', verify = 1, last_error = ? WHERE id = ?", note, id)
	return err
}

// ClearVerify records that Asana does not have what entry id would create, so it is sent
// again (and gets a new sent_at).
func (s *Store) ClearVerify(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET verify = 0, last_error = '' WHERE id = ?", id)
	return err
}

// SafeToResend reports whether sending op twice has the same effect as sending it once.
// Creating things (tasks, comments, attachments, status updates, tags) is not.
func (s *Store) SafeToResend(ctx context.Context, op Op) (bool, error) {
	return safeToResend(ctx, s.db, op)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// safeToResend is outbox.SafeToResend, looking up whether a local tag was created.
func safeToResend(ctx context.Context, q queryer, op Op) (bool, error) {
	return outbox.SafeToResend(op, func(tag string) (bool, error) {
		var real string
		err := q.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", ResultTag+tag).Scan(&real)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && real != "", err
	})
}

// RecoverInterruptedOps handles entries left "sending" by a process that died while
// sending them: they are queued again, to be verified first if resending is not harmless.
// It must only be called while holding the push lock (see TryLockPush).
func (s *Store) RecoverInterruptedOps(ctx context.Context) (requeued, verify int, err error) {
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
				_, err = tx.ExecContext(ctx, "UPDATE outbox SET state = 'pending', verify = 1 WHERE id = ?", id)
				verify++
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return requeued, verify, err
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

// MoveTarget is where a task created in Asana still has to be moved (outbox.MoveTarget).
type MoveTarget = outbox.MoveTarget

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
		// A new change: not verified, and its own attempts.
		if _, err := tx.ExecContext(ctx, "UPDATE outbox SET kind = ?, task_gid = ?, verify = 0, attempts = 0 WHERE id = ?", op.Kind, op.TaskGID, id); err != nil {
			return err
		}
		return saveOp(ctx, tx, op)
	})
}

// CompleteComment records the comment Asana created for entry id.
func (s *Store) CompleteComment(ctx context.Context, id int64, localGID, taskGID string, st model.Story) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalStory(ctx, tx, localGID, taskGID, st) })
}

// CompleteUpload records the attachment Asana created for entry id.
func (s *Store) CompleteUpload(ctx context.Context, id int64, local, real, viewURL string) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalAttachment(ctx, tx, local, real, viewURL) })
}

// CompleteStatus records the status update Asana created for entry id.
func (s *Store) CompleteStatus(ctx context.Context, id int64, local string, u model.StatusUpdate) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalStatus(ctx, tx, local, u) })
}

// ensureNotSending refuses to change an entry that Push is sending.
func ensureNotSending(op *Op) error {
	if op.State == OpSending {
		return fmt.Errorf("outbox entry %d: %w", op.ID, ErrOpSending)
	}
	return nil
}
