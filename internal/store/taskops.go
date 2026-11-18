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

// Position is where a task goes among its siblings (model.Position).
type Position = model.Position

// Kinds of the task operations of #32 (outbox's).
const (
	OpDeleteTask = outbox.KindDeleteTask
	OpSetParent  = outbox.KindSetParent
)

// deletedTasks lists the gids of the tasks whose delete is queued, and of their subtasks
// (Asana deletes them with it): they are hidden from the lists until the delete is sent
// (and removed), or shown again when it is discarded.
const deletedTasks = `(WITH RECURSIVE gone(gid) AS (
	SELECT task_gid FROM outbox WHERE kind = 'delete_task'
	UNION SELECT c.gid FROM tasks c JOIN gone ON c.parent_gid = gone.gid)
	SELECT gid FROM gone)`

// DeletePlan is readmodel.DeletePlan.
type DeletePlan = readmodel.DeletePlan

// PlanDeleteTask tells what DeleteTaskLocal would do with task gid, without changing
// anything (for a confirmation, or a dry run).
func (s *Store) PlanDeleteTask(ctx context.Context, gid string) (DeletePlan, error) {
	var p DeletePlan
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = planDelete(ctx, tx, gid)
		return err
	})
	return p, err
}

func planDelete(ctx context.Context, tx *sql.Tx, gid string) (DeletePlan, error) {
	p := DeletePlan{GID: gid, Local: IsLocalGID(gid)}
	err := tx.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = ?", gid).Scan(&p.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("task %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return p, err
	}
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE sub(gid) AS (
			SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
		SELECT (SELECT count(*) - 1 FROM sub),
			(SELECT count(*) FROM outbox WHERE task_gid IN (SELECT gid FROM sub) AND kind <> 'delete_task')`, gid).
		Scan(&p.Subtasks, &p.Queued)
	return p, err
}

// DeleteTaskLocal deletes task gid and its subtasks. A task never sent to Asana is removed
// at once, with every change queued for it. Otherwise the delete is queued: the task is
// hidden (see deletedTasks) until Push sends it, and discarding the entry brings it back
// with the changes queued for it. Deleting a task whose delete is queued does nothing.
func (s *Store) DeleteTaskLocal(ctx context.Context, gid string, now time.Time) (DeletePlan, error) {
	var p DeletePlan
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if p, err = planDelete(ctx, tx, gid); err != nil {
			return err
		}
		if p.Local {
			return deleteLocalTask(ctx, tx, gid)
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE kind = ? AND task_gid = ?", OpDeleteTask, gid).Scan(&n); err != nil || n > 0 {
			return err
		}
		// Subtasks made offline go now: their creates would only be deleted again.
		rows, err := tx.QueryContext(ctx, `WITH RECURSIVE sub(gid) AS (
				SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
			SELECT gid FROM sub WHERE gid LIKE 'local:%'`, gid)
		if err != nil {
			return err
		}
		locals, err := scanStrings(rows)
		if err != nil {
			return err
		}
		for _, l := range locals {
			if err := deleteLocalTask(ctx, tx, l); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		return saveOp(ctx, tx, &Op{Kind: OpDeleteTask, TaskGID: gid, State: OpPending, CreatedAt: formatTime(now),
			Payload: map[string]any{"name": p.Name}})
	})
	return p, err
}

// deleteLocalTask removes a task created offline (and not sent) as discarding its create
// does. A create being sent cannot be taken back.
func deleteLocalTask(ctx context.Context, tx *sql.Tx, gid string) error {
	var id int64
	var kind, state string
	err := tx.QueryRowContext(ctx, "SELECT id, kind, state FROM outbox WHERE task_gid = ? AND kind IN (?, ?) ORDER BY id LIMIT 1",
		gid, OpCreateTask, OpDuplicateTask).Scan(&id, &kind, &state)
	if errors.Is(err, sql.ErrNoRows) {
		// sent meanwhile (the gid is replaced), or already gone
		return fmt.Errorf("task %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if state == OpSending {
		return fmt.Errorf("outbox entry %d: %w", id, ErrOpSending)
	}
	return discardLocalTask(ctx, tx, gid, kind == OpCreateTask)
}

// CompleteDelete records that Asana deleted the task of entry id: the task, its subtasks
// and every change still queued for them are removed.
func (s *Store) CompleteDelete(ctx context.Context, id int64, gid string) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return removeTaskTree(ctx, tx, gid, nil) })
}

// removeTaskTree removes task gid, its subtasks, their dependencies and the changes queued
// for them; each entry is passed to dropped first (if set).
func removeTaskTree(ctx context.Context, tx *sql.Tx, gid string, dropped func(*Op) error) error {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE sub(gid) AS (
			SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
		SELECT o.id FROM outbox o WHERE o.task_gid IN (SELECT gid FROM sub) ORDER BY o.id`, gid)
	if err != nil {
		return err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return err
	}
	var parent string
	_ = tx.QueryRowContext(ctx, "SELECT parent_gid FROM tasks WHERE gid = ?", gid).Scan(&parent)
	for _, id := range ids {
		if dropped != nil {
			op, err := getOpTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := dropped(op); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`WITH RECURSIVE sub(gid) AS (SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
		DELETE FROM task_dependencies WHERE task_gid IN (SELECT gid FROM sub) OR dependency_gid IN (SELECT gid FROM sub)`,
		`WITH RECURSIVE sub(gid) AS (SELECT ? UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid)
		DELETE FROM tasks WHERE gid IN (SELECT gid FROM sub)`,
	} {
		if _, err := tx.ExecContext(ctx, q, gid); err != nil {
			return err
		}
	}
	if parent != "" {
		_, err = tx.ExecContext(ctx, "UPDATE tasks SET num_subtasks = max(0, num_subtasks - 1) WHERE gid = ?", parent)
	}
	return err
}

// SetParentLocal makes task gid a subtask of parent ("" for a top-level task), at pos
// among the parent's subtasks (gids of subtasks, optional), and queues it. Consecutive
// changes of a task are one entry; changing back to the parent it had removes it.
func (s *Store) SetParentLocal(ctx context.Context, gid, parent string, pos Position, now time.Time) error {
	if gid == parent {
		return errors.New("タスクを自分自身のサブタスクにはできません")
	}
	if parent == "" && !pos.IsZero() {
		return errors.New("親タスクを外すときは、位置 (前・後) を指定できません")
	}
	if pos.Before != "" && pos.After != "" {
		return errors.New("前と後は、どちらか一方だけ指定してください")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := refuseDeleted(ctx, tx, gid, parent, pos.Before, pos.After); err != nil {
			return err
		}
		var current string
		err := tx.QueryRowContext(ctx, "SELECT parent_gid FROM tasks WHERE gid = ?", gid).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %s: %w", gid, ErrNotFound)
		}
		if err != nil {
			return err
		}
		var parentName string
		if parent != "" {
			err := tx.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = ?", parent).Scan(&parentName)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task %s: %w", parent, ErrNotFound)
			}
			if err != nil {
				return err
			}
			// not under itself: walk up from the new parent
			var n int
			if err := tx.QueryRowContext(ctx, `WITH RECURSIVE up(gid) AS (
					SELECT ? UNION SELECT t.parent_gid FROM tasks t JOIN up ON t.gid = up.gid WHERE t.parent_gid <> '')
				SELECT count(*) FROM up WHERE gid = ?`, parent, gid).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errors.New("タスクを自分のサブタスクの下には移せません")
			}
		}
		for _, anchor := range []string{pos.Before, pos.After} {
			if anchor == "" {
				continue
			}
			var p string
			err := tx.QueryRowContext(ctx, "SELECT parent_gid FROM tasks WHERE gid = ?", anchor).Scan(&p)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task %s: %w", anchor, ErrNotFound)
			}
			if err != nil {
				return err
			}
			if p != parent || anchor == gid {
				return fmt.Errorf("位置の目印 %s は、新しい親のほかのサブタスクを指定してください", anchor)
			}
		}
		if current == parent && pos.IsZero() {
			return nil
		}
		op, err := findOp(ctx, tx, OpSetParent, gid)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpSetParent, TaskGID: gid, Payload: map[string]any{}, Base: map[string]any{"parent": current},
				State: OpPending, CreatedAt: formatTime(now)}
		}
		if err := setParent(ctx, tx, gid, parent); err != nil {
			return err
		}
		if op.Base["parent"] == parent && pos.IsZero() { // back where it started
			if op.ID == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		if !pos.IsZero() && op.ID != 0 {
			// A place is next to a task that may have moved since: go after those moves.
			if err := requeue(ctx, tx, op, now); err != nil {
				return err
			}
		}
		op.Payload = map[string]any{"parent": parent, "parent_name": parentName}
		if pos.Before != "" {
			op.Payload["insert_before"] = pos.Before
		}
		if pos.After != "" {
			op.Payload["insert_after"] = pos.After
		}
		return saveOp(ctx, tx, op)
	})
}

// setParent moves a task under parent ("" none) locally, keeping the parents' subtask
// counts right.
func setParent(ctx context.Context, tx *sql.Tx, gid, parent string) error {
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT parent_gid FROM tasks WHERE gid = ?", gid).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // gone meanwhile
		}
		return err
	}
	if current == parent {
		return nil
	}
	for _, q := range []struct {
		sql string
		arg string
	}{
		{"UPDATE tasks SET num_subtasks = max(0, num_subtasks - 1) WHERE gid = ?", current},
		{"UPDATE tasks SET num_subtasks = num_subtasks + 1 WHERE gid = ?", parent},
	} {
		if q.arg == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, q.sql, q.arg); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE tasks SET parent_gid = ? WHERE gid = ?", parent, gid)
	return err
}

// reapplySetParent keeps an unsent parent change of a task after a pull.
func reapplySetParent(ctx context.Context, tx *sql.Tx, gid string) error {
	var parent sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT json_extract(payload, '$.parent') FROM outbox
		WHERE kind = ? AND task_gid = ? AND state <> 'failed' ORDER BY id DESC LIMIT 1`, OpSetParent, gid).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Only the parent: the parents' subtask counts are the local ones (SetParentLocal
	// counted the change) until a pull of the parent itself brings Asana's.
	_, err = tx.ExecContext(ctx, "UPDATE tasks SET parent_gid = ? WHERE gid = ?", parent.String, gid)
	return err
}

// refuseDeleted refuses to use tasks whose delete is queued (or that go with one): Asana
// would delete a task moved under them, and a place next to them does not last.
func refuseDeleted(ctx context.Context, tx *sql.Tx, gids ...string) error {
	for _, g := range gids {
		if g == "" {
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+deletedTasks+" WHERE gid = ?", g).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("タスク %s は削除を送る予定です (取り消すには outbox の削除を取り消してください)", g)
		}
	}
	return nil
}

// requeue takes entry op out of its place in the queue: saving it adds it at the end
// (with its base), after the changes queued since.
func requeue(ctx context.Context, tx *sql.Tx, op *Op, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID); err != nil {
		return err
	}
	op.ID, op.CreatedAt = 0, formatTime(now)
	return nil
}

// undoSetParentsTo discards the parent changes that put a task under gid, which is going
// away (a task never sent, discarded or dropped): the tasks go back to their parents.
func undoSetParentsTo(ctx context.Context, tx *sql.Tx, gid string) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM outbox WHERE kind = ? AND json_extract(payload, '$.parent') = ? ORDER BY id", OpSetParent, gid)
	if err != nil {
		return err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := discardOpTx(ctx, tx, op); err != nil {
			return err
		}
	}
	return nil
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ResolveMoveNear resolves where moving task taskGID next to task anchor puts it: the
// project as ResolveMove picks it, and the section anchor is in there.
func (s *Store) ResolveMoveNear(ctx context.Context, taskGID, project, anchor string) (projectGID, sectionGID string, err error) {
	if projectGID, err = s.resolveMoveProject(ctx, taskGID, project); err != nil {
		return "", "", err
	}
	err = s.db.QueryRowContext(ctx, "SELECT section_gid FROM task_memberships WHERE task_gid = ? AND project_gid = ?", anchor, projectGID).Scan(&sectionGID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && sectionGID == "") {
		return "", "", fmt.Errorf("位置の目印 %s は、このプロジェクトのセクションに入っていません", anchor)
	}
	return projectGID, sectionGID, err
}
