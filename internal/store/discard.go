package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Changes to things deleted in Asana are dropped automatically (Asana answered 404), and
// recorded so the front ends can tell the user once what was dropped:
//
//	for _, d := range PendingDiscards(ctx) { show(d) }; MarkDiscardsNotified(ctx, ids)

// Discard is a local change dropped because its target was deleted in Asana.
type Discard struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	TaskGID     string `json:"task_gid,omitempty"`
	TaskName    string `json:"task_name,omitempty"`
	Summary     string `json:"summary"` // what the change was, e.g. 「名前を「…」に変更」
	Reason      string `json:"reason,omitempty"`
	DiscardedAt string `json:"discarded_at"`
	// Payload is the dropped change as it was queued (e.g. the full comment text, the new
	// notes, the path of the attachment's local copy, which is kept), to recover it.
	Payload map[string]any `json:"payload,omitempty"`
}

// DiscardDeletedTask drops a task that no longer exists in Asana: every queued change to
// it or to its subtasks (including subtasks created offline), and its rows in the store.
// Each dropped change is recorded (see PendingDiscards).
func (s *Store) DiscardDeletedTask(ctx context.Context, taskGID, reason string, now time.Time) ([]Discard, error) {
	var out []Discard
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `WITH RECURSIVE sub(gid) AS (
				SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
			SELECT o.id FROM outbox o WHERE o.task_gid IN (SELECT gid FROM sub) ORDER BY o.id`, taskGID)
		if err != nil {
			return err
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return err
		}
		var parent string
		_ = tx.QueryRowContext(ctx, "SELECT parent_gid FROM tasks WHERE gid = ?", taskGID).Scan(&parent)
		for _, id := range ids {
			op, err := getOpTx(ctx, tx, id)
			if err != nil {
				return err
			}
			d, err := recordDiscard(ctx, tx, op, reason, now)
			if err != nil {
				return err
			}
			out = append(out, d)
			if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sub(gid) AS (
				SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
			DELETE FROM task_dependencies WHERE task_gid IN (SELECT gid FROM sub) OR dependency_gid IN (SELECT gid FROM sub)`, taskGID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sub(gid) AS (
				SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
			DELETE FROM tasks WHERE gid IN (SELECT gid FROM sub)`, taskGID); err != nil {
			return err
		}
		if parent != "" && IsLocalGID(taskGID) { // a local subtask was counted by its parent
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET num_subtasks = max(0, num_subtasks - 1) WHERE gid = ?", parent)
		}
		return err
	})
	return out, err
}

// DiscardGoneOp drops one entry whose target other than its task (a section, project,
// tag, ...) was deleted in Asana, undoing its local change like DiscardOp. It may be
// "sending" (Push calls it while handling the entry).
func (s *Store) DiscardGoneOp(ctx context.Context, id int64, reason string, now time.Time) (Discard, error) {
	var d Discard
	err := s.tx(ctx, func(tx *sql.Tx) error {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if d, err = recordDiscard(ctx, tx, op, reason, now); err != nil {
			return err
		}
		return discardOpTx(ctx, tx, op)
	})
	return d, err
}

// PendingDiscards returns the dropped changes not shown to the user yet, oldest first.
func (s *Store) PendingDiscards(ctx context.Context) ([]Discard, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, task_gid, task_name, summary, reason, discarded_at, payload
		FROM discarded_changes WHERE notified = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Discard{}
	for rows.Next() {
		var d Discard
		var payload string
		if err := rows.Scan(&d.ID, &d.Kind, &d.TaskGID, &d.TaskName, &d.Summary, &d.Reason, &d.DiscardedAt, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &d.Payload); err != nil {
			return nil, fmt.Errorf("discarded change %d: %w", d.ID, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDiscardsNotified records that the user was told about these dropped changes.
func (s *Store) MarkDiscardsNotified(ctx context.Context, ids []int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE discarded_changes SET notified = 1 WHERE id = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

func recordDiscard(ctx context.Context, tx *sql.Tx, op *Op, reason string, now time.Time) (Discard, error) {
	var name string
	_ = tx.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = ?", op.TaskGID).Scan(&name)
	if name == "" {
		name, _ = op.Payload["name"].(string)
	}
	d := Discard{Kind: op.Kind, TaskGID: op.TaskGID, TaskName: name, Summary: DescribeChange(*op), Reason: reason,
		DiscardedAt: formatTime(now), Payload: op.Payload}
	payload, err := json.Marshal(nonNil(op.Payload))
	if err != nil {
		return d, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO discarded_changes(kind, task_gid, task_name, summary, reason, discarded_at, payload)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, d.Kind, d.TaskGID, d.TaskName, d.Summary, d.Reason, d.DiscardedAt, string(payload))
	if err != nil {
		return d, err
	}
	d.ID, err = res.LastInsertId()
	return d, err
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
