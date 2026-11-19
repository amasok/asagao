package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/amasok/asagao/internal/termsafe"
)

// The order of tasks as Asana has it (#51).
//
// task_memberships.position is a task's place in its section, tasks.subtask_position its
// place among its parent's subtasks; only their order counts, within one section or one
// parent. They come from Asana's listings (SetProjectTaskOrder, SetSectionTaskOrder,
// SetSubtaskOrder), and a local move puts the task where Asana will (placeInSection,
// placeSubtask), so the cache shows the new order before it is sent. NULL is not known: a
// task stored before #51, moved to another section or parent, or created here. Such tasks
// come after the known ones, in the order used before (listOrder, subtaskOrder).

// listOrder is the order of ListTasks without a known position (and before #51).
const listOrder = "t.completed, t.due_on = '', t.due_on, t.due_at IS NULL, t.due_at, t.name"

// subtaskOrder is the order of a task's subtasks without a known position (and before #51).
const subtaskOrder = "t.created_at, t.gid"

// SetProjectTaskOrder records the order of a project's tasks as a full listing of it gives
// them (GET /tasks?project=, in the project's order). Tasks of the project not listed lose
// their place. Unsent moves of the project are put back where they go.
func (s *Store) SetProjectTaskOrder(ctx context.Context, projectGID string, gids []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE task_memberships SET position = NULL WHERE project_gid = ?", projectGID); err != nil {
			return err
		}
		for i, g := range gids {
			if _, err := tx.ExecContext(ctx, "UPDATE task_memberships SET position = ? WHERE task_gid = ? AND project_gid = ?",
				i, g, projectGID); err != nil {
				return err
			}
		}
		return reapplyMoves(ctx, tx, projectGID)
	})
}

// SetSectionTaskOrder records the order of a section's tasks as Asana lists them (GET
// /sections/{gid}/tasks). Tasks of the section not listed lose their place; a listed task
// the cache has in another section is left alone. Unsent moves of the section's project
// are put back where they go. An unknown section is ignored.
func (s *Store) SetSectionTaskOrder(ctx context.Context, sectionGID string, gids []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var project string
		err := tx.QueryRowContext(ctx, "SELECT project_gid FROM sections WHERE gid = ?", sectionGID).Scan(&project)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE task_memberships SET position = NULL WHERE section_gid = ? AND project_gid = ?",
			sectionGID, project); err != nil {
			return err
		}
		for i, g := range gids {
			if _, err := tx.ExecContext(ctx, "UPDATE task_memberships SET position = ? WHERE task_gid = ? AND section_gid = ? AND project_gid = ?",
				i, g, sectionGID, project); err != nil {
				return err
			}
		}
		return reapplyMoves(ctx, tx, project)
	})
}

// SetSubtaskOrder records the order of a task's subtasks as Asana lists them (GET
// /tasks/{gid}/subtasks). Subtasks not listed lose their place; a listed task the cache
// has under another parent is left alone. Unsent parent changes into it are put back
// where they go.
func (s *Store) SetSubtaskOrder(ctx context.Context, parentGID string, gids []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET subtask_position = NULL WHERE parent_gid = ?", parentGID); err != nil {
			return err
		}
		for i, g := range gids {
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET subtask_position = ? WHERE gid = ? AND parent_gid = ?",
				i, g, parentGID); err != nil {
				return err
			}
		}
		return reapplyParentMoves(ctx, tx, parentGID)
	})
}

// placeInSection puts a task, which the cache has in section (of project), where a move
// there puts it in Asana: next to pos.Before / pos.After, else at the top of the section
// when it came from another one (POST /sections/{gid}/addTask inserts at the top).
// Otherwise its place stays.
func placeInSection(ctx context.Context, tx *sql.Tx, gid, project, section string, pos Position, fromOther bool) error {
	switch {
	case pos.Before != "":
		return reorder(ctx, tx, sectionList(project, section), gid, pos.Before, false)
	case pos.After != "":
		return reorder(ctx, tx, sectionList(project, section), gid, pos.After, true)
	case fromOther:
		return reorder(ctx, tx, sectionList(project, section), gid, "", false)
	}
	return nil
}

// placeSubtask puts a subtask next to pos.Before / pos.After among its parent's subtasks,
// as POST /tasks/{gid}/setParent does. Without a position its place stays.
func placeSubtask(ctx context.Context, tx *sql.Tx, gid, parent string, pos Position) error {
	switch {
	case pos.Before != "":
		return reorder(ctx, tx, subtaskList(parent), gid, pos.Before, false)
	case pos.After != "":
		return reorder(ctx, tx, subtaskList(parent), gid, pos.After, true)
	}
	return nil
}

// orderedList is a group of tasks that has an order: how to read it in order, and how to
// write a task's place in it.
type orderedList struct {
	query  string
	args   []any
	update string // args: position, task gid, then args
}

func sectionList(project, section string) orderedList {
	return orderedList{
		query: `SELECT m.task_gid FROM task_memberships m JOIN tasks t ON t.gid = m.task_gid
			WHERE m.project_gid = ? AND m.section_gid = ? ORDER BY m.position IS NULL, m.position, ` + listOrder,
		args:   []any{project, section},
		update: "UPDATE task_memberships SET position = ? WHERE task_gid = ? AND project_gid = ? AND section_gid = ?",
	}
}

func subtaskList(parent string) orderedList {
	return orderedList{
		query: `SELECT t.gid FROM tasks t WHERE t.parent_gid = ?
			ORDER BY t.subtask_position IS NULL, t.subtask_position, ` + subtaskOrder,
		args:   []any{parent},
		update: "UPDATE tasks SET subtask_position = ? WHERE gid = ? AND parent_gid = ?",
	}
}

// reorder moves gid in l just before (or after) anchor, or to the top when anchor is "",
// and numbers the list again. If gid or anchor is not in the list, nothing changes.
func reorder(ctx context.Context, tx *sql.Tx, l orderedList, gid, anchor string, after bool) error {
	rows, err := tx.QueryContext(ctx, l.query, l.args...)
	if err != nil {
		return err
	}
	list, err := scanStrings(rows)
	if err != nil {
		return err
	}
	i := slices.Index(list, gid)
	if i < 0 || anchor == gid {
		return nil
	}
	list = slices.Delete(list, i, i+1)
	at := 0
	if anchor != "" {
		if at = slices.Index(list, anchor); at < 0 {
			return nil
		}
		if after {
			at++
		}
	}
	list = slices.Insert(list, at, gid)
	for i, g := range list {
		if _, err := tx.ExecContext(ctx, l.update, append([]any{i, g}, l.args...)...); err != nil {
			return err
		}
	}
	return nil
}

// payloadPosition is the place an outbox payload asks for (insert_before / insert_after).
func payloadPosition(p map[string]any) Position {
	before, _ := p["insert_before"].(string)
	after, _ := p["insert_after"].(string)
	return Position{Before: before, After: after}
}

// reapplyMoves puts the tasks with an unsent move in project where the moves put them,
// after a listing of Asana's order (which does not have them yet).
func reapplyMoves(ctx context.Context, tx *sql.Tx, project string) error {
	rows, err := tx.QueryContext(ctx, `SELECT task_gid, payload, base FROM outbox
		WHERE kind = ? AND state <> 'failed' AND json_extract(payload, '$.project') = ? ORDER BY id`, OpMoveTask, project)
	if err != nil {
		return err
	}
	type move struct {
		gid           string
		payload, base map[string]any
	}
	var moves []move
	for rows.Next() {
		var m move
		var payload, base sql.NullString
		if err := rows.Scan(&m.gid, &payload, &base); err != nil {
			rows.Close()
			return err
		}
		if err := termsafe.Unmarshal([]byte(payload.String), &m.payload); err != nil {
			rows.Close()
			return err
		}
		if base.Valid && base.String != "" {
			if err := termsafe.Unmarshal([]byte(base.String), &m.base); err != nil {
				rows.Close()
				return err
			}
		}
		moves = append(moves, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range moves {
		if err := reapplyMove(ctx, tx, m.gid, m.payload, m.base); err != nil {
			return err
		}
	}
	return nil
}

// reapplyMove applies an unsent move of a task again: its section, and its place there.
func reapplyMove(ctx context.Context, tx *sql.Tx, gid string, payload, base map[string]any) error {
	project, _ := payload["project"].(string)
	section, _ := payload["section"].(string)
	if err := setSection(ctx, tx, gid, project, section); err != nil {
		return err
	}
	from, _ := base["section"].(string)
	return placeInSection(ctx, tx, gid, project, section, payloadPosition(payload), from != section)
}

// reapplyParentMoves puts the subtasks with an unsent, placed parent change into parent
// where the changes put them, after a listing of Asana's order.
func reapplyParentMoves(ctx context.Context, tx *sql.Tx, parent string) error {
	rows, err := tx.QueryContext(ctx, `SELECT task_gid, payload FROM outbox
		WHERE kind = ? AND state <> 'failed' AND json_extract(payload, '$.parent') = ? ORDER BY id`, OpSetParent, parent)
	if err != nil {
		return err
	}
	type move struct {
		gid     string
		payload map[string]any
	}
	var moves []move
	for rows.Next() {
		var m move
		var payload string
		if err := rows.Scan(&m.gid, &payload); err != nil {
			rows.Close()
			return err
		}
		if err := termsafe.Unmarshal([]byte(payload), &m.payload); err != nil {
			rows.Close()
			return err
		}
		moves = append(moves, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range moves {
		if err := placeSubtask(ctx, tx, m.gid, parent, payloadPosition(m.payload)); err != nil {
			return err
		}
	}
	return nil
}

// sectionPosition is a task's place in its section of project (NULL if not known).
func sectionPosition(ctx context.Context, tx *sql.Tx, gid, project string) (sql.NullInt64, error) {
	var p sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT position FROM task_memberships WHERE task_gid = ? AND project_gid = ?", gid, project).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	return p, err
}

// basePosition is the place an outbox entry's base recorded ("position"), if any.
func basePosition(base map[string]any) (sql.NullInt64, bool) {
	v, ok := base["position"]
	if !ok {
		return sql.NullInt64{}, false
	}
	f, ok := v.(float64)
	if !ok {
		return sql.NullInt64{}, true // recorded as not known
	}
	return sql.NullInt64{Int64: int64(f), Valid: true}, true
}

// positionValue is p as an outbox base value: its number, or nil if not known.
func positionValue(p sql.NullInt64) any {
	if !p.Valid {
		return nil
	}
	return p.Int64
}
