package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

func init() {
	registerUndo(OpMoveMySection, func(ctx context.Context, tx *sql.Tx, op *Op) error {
		section, _ := op.Base["section"].(string)
		_, err := tx.ExecContext(ctx, "UPDATE tasks SET assignee_section_gid = ? WHERE gid = ?", section, op.TaskGID)
		return err
	})
}

// The sections of the user's My Tasks list (#35): the list's sections per workspace, and
// the section each of my tasks is in there (tasks.assignee_section_gid).

// OpMoveMySection moves a task to another section of My Tasks (outbox.KindMoveMySection).
const OpMoveMySection = outbox.KindMoveMySection

// ReplaceMyTaskSections replaces the sections of my My Tasks list in a workspace, in
// Asana's order. Sections only known from tasks (assignee_section) are kept after them.
func (s *Store) ReplaceMyTaskSections(ctx context.Context, workspaceGID string, secs []model.Section) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		// The sections my tasks are in stay even when the list did not return them, with
		// the name the tasks told (after the list's).
		if _, err := tx.ExecContext(ctx, `DELETE FROM my_task_sections WHERE workspace_gid = ? AND gid NOT IN (
			SELECT t.assignee_section_gid FROM tasks t WHERE t.workspace_gid = ?
			AND t.assignee_gid = (SELECT value FROM sync_state WHERE key = ?))`, workspaceGID, workspaceGID, keyMeGID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE my_task_sections SET position = ? WHERE workspace_gid = ?", len(secs), workspaceGID); err != nil {
			return err
		}
		for i, sec := range secs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO my_task_sections(gid, workspace_gid, name, position) VALUES(?, ?, ?, ?)
				ON CONFLICT(gid) DO UPDATE SET workspace_gid = excluded.workspace_gid, name = excluded.name, position = excluded.position`,
				sec.GID, workspaceGID, sec.Name, i); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListMyTaskSections returns the sections of my My Tasks list in a workspace ("" for the
// default workspace), in order.
func (s *Store) ListMyTaskSections(ctx context.Context, workspaceGID string) ([]readmodel.SectionView, error) {
	if workspaceGID == "" {
		var err error
		if workspaceGID, err = s.DefaultWorkspace(ctx); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT gid, name FROM my_task_sections WHERE workspace_gid = ? AND name <> '' ORDER BY position, rowid", workspaceGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []readmodel.SectionView{}
	for rows.Next() {
		var v readmodel.SectionView
		if err := rows.Scan(&v.GID, &v.Name); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ResolveMySection finds a section of my My Tasks list in the workspace of task taskGID,
// by gid or exact name.
func (s *Store) ResolveMySection(ctx context.Context, taskGID, arg string) (string, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, `SELECT ms.gid FROM my_task_sections ms JOIN tasks t ON t.workspace_gid = ms.workspace_gid
		WHERE t.gid = ? AND (ms.gid = ? OR ms.name = ?) AND ms.name <> '' ORDER BY ms.gid = ? DESC LIMIT 1`,
		taskGID, strings.TrimSpace(arg), strings.TrimSpace(arg), strings.TrimSpace(arg)).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("マイタスクのセクション %q: %w", arg, ErrNotFound)
	}
	return gid, err
}

// ErrNotMine is returned when moving a task that is not assigned to me within My Tasks.
var ErrNotMine = errors.New("自分が担当のタスクだけ、マイタスクのセクションを移せます")

// MoveMySectionLocal moves task gid (assigned to me) to section sectionGID of my My Tasks
// list and queues the move. Moving it back cancels it.
func (s *Store) MoveMySectionLocal(ctx context.Context, gid, sectionGID string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := refuseDeleted(ctx, tx, gid); err != nil {
			return err
		}
		var workspace, assignee, current, me string
		err := tx.QueryRowContext(ctx, "SELECT workspace_gid, assignee_gid, assignee_section_gid FROM tasks WHERE gid = ?", gid).
			Scan(&workspace, &assignee, &current)
		if err != nil {
			return notFound(err, "task "+gid)
		}
		_ = tx.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", keyMeGID).Scan(&me)
		if me == "" || assignee != me {
			return ErrNotMine
		}
		var name string
		err = tx.QueryRowContext(ctx, "SELECT name FROM my_task_sections WHERE gid = ? AND workspace_gid = ?", sectionGID, workspace).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("マイタスクのセクション %s: %w", sectionGID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if current == sectionGID {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET assignee_section_gid = ? WHERE gid = ?", sectionGID, gid); err != nil {
			return err
		}
		op, err := findOp(ctx, tx, OpMoveMySection, gid)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpMoveMySection, TaskGID: gid, Base: map[string]any{"section": current},
				State: OpPending, CreatedAt: formatTime(now)}
		}
		if op.Base["section"] == sectionGID { // moved back
			if op.ID == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		op.Payload = map[string]any{"section": sectionGID, "section_name": name, "workspace": workspace}
		return saveOp(ctx, tx, op)
	})
}

// upsertAssigneeSection records the My Tasks section of a task Asana returned (when the
// response included it), keeping an unsent local move.
func upsertAssigneeSection(ctx context.Context, tx *sql.Tx, t model.Task, workspace string) error {
	if !t.AssigneeSection.Set {
		return nil
	}
	gid := t.AssigneeSection.GID()
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET assignee_section_gid = ? WHERE gid = ?", gid, t.GID); err != nil {
		return err
	}
	var me string
	_ = tx.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", keyMeGID).Scan(&me)
	if gid != "" && workspace != "" && t.Assignee != nil && t.Assignee.GID == me {
		// A section of my list only known from the task (its name, if the list was not read).
		if _, err := tx.ExecContext(ctx, `INSERT INTO my_task_sections(gid, workspace_gid, name, position) VALUES(?, ?, ?, 1 << 30)
			ON CONFLICT(gid) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE my_task_sections.name END`,
			gid, workspace, t.AssigneeSection.Ref.Name); err != nil {
			return err
		}
	}
	var payload string
	err := tx.QueryRowContext(ctx, "SELECT json_extract(payload, '$.section') FROM outbox WHERE kind = ? AND task_gid = ? AND state <> 'failed' ORDER BY id DESC LIMIT 1",
		OpMoveMySection, t.GID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE tasks SET assignee_section_gid = ? WHERE gid = ?", payload, t.GID)
	return err
}
