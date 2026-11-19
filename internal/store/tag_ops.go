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
	"github.com/amasok/asagao/internal/domain/tag"
	"github.com/amasok/asagao/internal/termsafe"
)

// Operations on tags themselves (outbox.KindUpdateTag, outbox.KindDeleteTag).
const (
	OpUpdateTag = outbox.KindUpdateTag
	OpDeleteTag = outbox.KindDeleteTag
)

// ResolveTag finds a tag by gid or exact name. Several tags with that name are ambiguous.
func (s *Store) ResolveTag(ctx context.Context, arg string) (string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT gid FROM tags WHERE gid = ?1 OR name = ?1 ORDER BY gid = ?1 DESC", strings.TrimSpace(arg))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var gids []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return "", err
		}
		if g == arg { // an exact gid wins
			return g, nil
		}
		gids = append(gids, g)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(gids) {
	case 0:
		return "", fmt.Errorf("tag %q: %w", arg, ErrNotFound)
	case 1:
		return gids[0], nil
	}
	return "", fmt.Errorf("tag %q matches %s (gid で指定してください): %w", arg, strings.Join(gids, ", "), ErrAmbiguous)
}

// UpdateTagLocal renames a tag and/or changes its color, and queues the change. changes
// holds "name" (not empty) and/or "color" (one of tag.Colors, or "" for none).
func (s *Store) UpdateTagLocal(ctx context.Context, tagGID string, changes map[string]any, now time.Time) error {
	clean := map[string]any{}
	for k, v := range changes {
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("tag %s: %s must be a string", tagGID, k)
		}
		switch k {
		case "name":
			if str = strings.TrimSpace(str); str == "" {
				return errors.New("タグ名が空です")
			}
		case "color":
			var err error
			if str, err = tag.NormalizeColor(str); err != nil {
				return err
			}
		default:
			return fmt.Errorf("tag %s: unknown change %q", tagGID, k)
		}
		clean[k] = str
	}
	if len(clean) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		current := map[string]any{}
		var name, color string
		err := tx.QueryRowContext(ctx, "SELECT name, color FROM tags WHERE gid = ?", tagGID).Scan(&name, &color)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tag %s: %w", tagGID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		current["name"], current["color"] = name, color
		op, err := findOp(ctx, tx, OpUpdateTag, tagGID)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpUpdateTag, TaskGID: tagGID, Payload: map[string]any{}, Base: map[string]any{},
				State: OpPending, CreatedAt: formatTime(now)}
		}
		keepSameColor(clean, op.Base, current)
		changed := false
		for k, v := range clean {
			if current[k] == v {
				continue
			}
			changed = true
			if _, ok := op.Base[k]; !ok {
				op.Base[k] = current[k]
			}
			op.Payload[k] = v
			if _, err := tx.ExecContext(ctx, "UPDATE tags SET "+k+" = ? WHERE gid = ?", v, tagGID); err != nil {
				return err
			}
		}
		if !changed {
			return nil
		}
		for k, v := range op.Payload { // changed back: nothing to send for it
			if op.Base[k] == v {
				delete(op.Payload, k)
				delete(op.Base, k)
			}
		}
		if len(op.Payload) == 0 {
			if op.ID == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		return saveOp(ctx, tx, op)
	})
}

// DeleteTagLocal deletes a tag (from every task too) and queues the deletion. A tag
// created offline that never reached Asana is simply forgotten, with the changes queued
// for it.
func (s *Store) DeleteTagLocal(ctx context.Context, tagGID string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var name, color, workspace string
		err := tx.QueryRowContext(ctx, "SELECT name, color, workspace_gid FROM tags WHERE gid = ?", tagGID).Scan(&name, &color, &workspace)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tag %s: %w", tagGID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if workspace == "" {
			if workspace, err = tagWorkspaceFromTasks(ctx, tx, tagGID); err != nil {
				return err
			}
		}
		tasks, err := taggedTasks(ctx, tx, tagGID)
		if err != nil {
			return err
		}
		// The tag's queued changes that were never sent need not be: it is going away.
		var unsure int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE kind IN (?, ?, ?)
			AND (task_gid = ?4 OR json_extract(payload, '$.tag') = ?4) AND (state <> 'pending' OR verify = 1)`,
			OpAddTag, OpRemoveTag, OpUpdateTag, tagGID).Scan(&unsure); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE kind IN (?, ?, ?)
			AND (task_gid = ?4 OR json_extract(payload, '$.tag') = ?4) AND state = 'pending' AND verify = 0`,
			OpAddTag, OpRemoveTag, OpUpdateTag, tagGID); err != nil {
			return err
		}
		for _, q := range []string{"DELETE FROM task_tags WHERE tag_gid = ?", "DELETE FROM tags WHERE gid = ?"} {
			if _, err := tx.ExecContext(ctx, q, tagGID); err != nil {
				return err
			}
		}
		if IsLocalGID(tagGID) && unsure == 0 {
			var real string
			_ = tx.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", ResultTag+tagGID).Scan(&real)
			if real == "" {
				return nil // Asana never had it
			}
			tagGID = real
		}
		return saveOp(ctx, tx, &Op{Kind: OpDeleteTag, TaskGID: tagGID, State: OpPending, CreatedAt: formatTime(now),
			Payload: map[string]any{"name": name, "color": color, "workspace": workspace, "tasks": tasks}})
	})
}

func taggedTasks(ctx context.Context, tx *sql.Tx, tagGID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT task_gid FROM task_tags WHERE tag_gid = ? ORDER BY task_gid", tagGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		tasks = append(tasks, g)
	}
	return tasks, rows.Err()
}

// ApplyTag records the server's name, color and workspace for a tag (after a push).
func (s *Store) ApplyTag(ctx context.Context, t model.Tag) error {
	ws := tagWorkspaceGID(t)
	_, err := s.db.ExecContext(ctx, `UPDATE tags SET name = ?, color = ?,
		workspace_gid = CASE WHEN ? <> '' THEN ? ELSE workspace_gid END WHERE gid = ?`,
		t.Name, t.Color, ws, ws, t.GID)
	return err
}

// reapplyTagEdits keeps the unsent changes to tags after a sync stored Asana's tags: a
// renamed or recolored tag keeps its new name and color, a deleted one stays deleted.
// With gids, only those tags are looked at.
func reapplyTagEdits(ctx context.Context, tx *sql.Tx, gids ...string) error {
	q := "SELECT kind, task_gid, payload FROM outbox WHERE kind IN (?, ?) AND state <> 'failed'"
	args := []any{OpUpdateTag, OpDeleteTag}
	if len(gids) > 0 {
		q += " AND task_gid IN (" + strings.TrimSuffix(strings.Repeat("?,", len(gids)), ",") + ")"
		for _, g := range gids {
			args = append(args, g)
		}
	}
	rows, err := tx.QueryContext(ctx, q+" ORDER BY id", args...)
	if err != nil {
		return err
	}
	type edit struct {
		kind, gid string
		payload   map[string]any
	}
	var edits []edit
	for rows.Next() {
		var e edit
		var payload string
		if err := rows.Scan(&e.kind, &e.gid, &payload); err != nil {
			rows.Close()
			return err
		}
		if err := termsafe.Unmarshal([]byte(payload), &e.payload); err != nil {
			rows.Close()
			return err
		}
		edits = append(edits, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range edits {
		if e.kind == OpDeleteTag {
			for _, q := range []string{"DELETE FROM task_tags WHERE tag_gid = ?", "DELETE FROM tags WHERE gid = ?"} {
				if _, err := tx.ExecContext(ctx, q, e.gid); err != nil {
					return err
				}
			}
			continue
		}
		for _, k := range []string{"name", "color"} {
			if v, ok := e.payload[k].(string); ok {
				if _, err := tx.ExecContext(ctx, "UPDATE tags SET "+k+" = ? WHERE gid = ?", v, e.gid); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// undoTagOp undoes the local change of a tag entry that is discarded.
func undoTagOp(ctx context.Context, tx *sql.Tx, op *Op) error {
	switch op.Kind {
	case OpUpdateTag:
		for _, k := range []string{"name", "color"} {
			if v, ok := op.Base[k].(string); ok {
				if _, err := tx.ExecContext(ctx, "UPDATE tags SET "+k+" = ? WHERE gid = ?", v, op.TaskGID); err != nil {
					return err
				}
			}
		}
	case OpDeleteTag:
		name, _ := op.Payload["name"].(string)
		color, _ := op.Payload["color"].(string)
		workspace, _ := op.Payload["workspace"].(string)
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO tags(gid, name, color, workspace_gid) VALUES(?, ?, ?, ?)", op.TaskGID, name, color, workspace); err != nil {
			return err
		}
		tasks, _ := op.Payload["tasks"].([]any)
		for _, t := range tasks {
			g, _ := t.(string)
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO task_tags(task_gid, tag_gid) SELECT gid, ? FROM tasks WHERE gid = ?", op.TaskGID, g); err != nil {
				return err
			}
		}
	}
	return nil
}

// TagCreateQueued reports whether a tag created offline still has an add_tag (which
// creates it in Asana) in the outbox, in any state.
func (s *Store) TagCreateQueued(ctx context.Context, tag string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE kind = ? AND json_extract(payload, '$.tag') = ?", OpAddTag, tag).Scan(&n)
	return n > 0, err
}
