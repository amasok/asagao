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
)

func init() {
	registerUndo(OpAddTag, func(ctx context.Context, tx *sql.Tx, op *Op, p outbox.AddTag) error {
		return undoTaskTag(ctx, tx, op, p.Tag)
	})
	registerUndo(OpRemoveTag, func(ctx context.Context, tx *sql.Tx, op *Op, p outbox.RemoveTag) error {
		return undoTaskTag(ctx, tx, op, p.Tag)
	})
}

// undoTaskTag takes a tag added locally off the task again, or puts one removed back.
func undoTaskTag(ctx context.Context, tx *sql.Tx, op *Op, tag string) error {
	q := "DELETE FROM task_tags WHERE task_gid = ? AND tag_gid = ?"
	if op.Kind == OpRemoveTag {
		// A task gone from the cache has nothing to put back (#87).
		q = "INSERT OR IGNORE INTO task_tags(task_gid, tag_gid) SELECT ?1, ?2 WHERE EXISTS (SELECT 1 FROM tasks WHERE gid = ?1)"
	}
	_, err := tx.ExecContext(ctx, q, op.TaskGID, tag)
	return err
}

// Tag operations (outbox.KindAddTag, outbox.KindRemoveTag).
const (
	OpAddTag    = outbox.KindAddTag
	OpRemoveTag = outbox.KindRemoveTag
)

func tagWorkspaceGID(t model.Tag) string {
	if t.Workspace != nil {
		return t.Workspace.GID
	}
	return ""
}

// UpsertTags stores the tags of a workspace (so unused tags can be picked too).
func (s *Store) UpsertTags(ctx context.Context, tags []model.Tag) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, t := range tags {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tags(gid, name, color, workspace_gid) VALUES(?, ?, ?, ?)
				ON CONFLICT(gid) DO UPDATE SET name = excluded.name, color = excluded.color,
					workspace_gid = CASE WHEN excluded.workspace_gid <> '' THEN excluded.workspace_gid ELSE tags.workspace_gid END`,
				t.GID, t.Name, t.Color, tagWorkspaceGID(t)); err != nil {
				return err
			}
		}
		return reapplyTagEdits(ctx, tx)
	})
}

// TaskTags returns the gids and names of a task's tags.
func (s *Store) TaskTags(ctx context.Context, taskGID string) ([]model.Ref, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.gid, g.name FROM task_tags tt JOIN tags g ON g.gid = tt.tag_gid
		WHERE tt.task_gid = ? ORDER BY g.name`, taskGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Ref
	for rows.Next() {
		var r model.Ref
		if err := rows.Scan(&r.GID, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddTagLocal tags a task. With tagGID "" a new tag named tagName is created (offline too).
func (s *Store) AddTagLocal(ctx context.Context, taskGID, tagGID, tagName string, now time.Time) (string, error) {
	tagName = strings.TrimSpace(tagName)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var workspace string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(workspace_gid, ''),
			(SELECT p.workspace_gid FROM task_memberships m JOIN projects p ON p.gid = m.project_gid
				WHERE m.task_gid = tasks.gid ORDER BY m.rowid LIMIT 1), '')
			FROM tasks WHERE gid = ?`, taskGID).Scan(&workspace); err != nil {
			return notFound(err, "task "+taskGID)
		}
		if tagGID == "" {
			if tagName == "" {
				return errors.New("タグ名が空です")
			}
			// reuse an existing tag with that name
			err := tx.QueryRowContext(ctx, "SELECT gid FROM tags WHERE name = ?", tagName).Scan(&tagGID)
			if errors.Is(err, sql.ErrNoRows) {
				tagGID = newLocalGID()
				if _, err := tx.ExecContext(ctx, "INSERT INTO tags(gid, name, workspace_gid) VALUES(?, ?, ?)", tagGID, tagName, workspace); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		} else if err := tx.QueryRowContext(ctx, "SELECT name FROM tags WHERE gid = ?", tagGID).Scan(&tagName); err != nil {
			return notFound(err, "tag "+tagGID)
		}
		if workspace != "" {
			if _, err := tx.ExecContext(ctx, "UPDATE tags SET workspace_gid = ? WHERE gid = ? AND workspace_gid = ''", workspace, tagGID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO task_tags(task_gid, tag_gid) VALUES(?, ?)", taskGID, tagGID); err != nil {
			return err
		}
		// undoing a pending removal cancels it
		if n, err := cancelUnsentTag(ctx, tx, OpRemoveTag, taskGID, tagGID); err != nil {
			return fmt.Errorf("cancel unsent %s: %w", OpRemoveTag, err)
		} else if n > 0 {
			return nil
		}
		return savePayload(ctx, tx, &Op{Kind: OpAddTag, TaskGID: taskGID, State: OpPending, CreatedAt: formatTime(now)},
			outbox.AddTag{Tag: tagGID, TagName: tagName, Workspace: workspace})
	})
	return tagGID, err
}

// RemoveTagLocal untags a task.
func (s *Store) RemoveTagLocal(ctx context.Context, taskGID, tagGID string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var name string
		_ = tx.QueryRowContext(ctx, "SELECT name FROM tags WHERE gid = ?", tagGID).Scan(&name)
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_tags WHERE task_gid = ? AND tag_gid = ?", taskGID, tagGID); err != nil {
			return err
		}
		if n, err := cancelUnsentTag(ctx, tx, OpAddTag, taskGID, tagGID); err != nil {
			return fmt.Errorf("cancel unsent %s: %w", OpAddTag, err)
		} else if n > 0 {
			return nil // it was never sent
		}
		return savePayload(ctx, tx, &Op{Kind: OpRemoveTag, TaskGID: taskGID, State: OpPending, CreatedAt: formatTime(now)},
			outbox.RemoveTag{Tag: tagGID, TagName: name})
	})
}

// RealTagGID returns the Asana gid of a tag created offline (or gid itself).
func (s *Store) RealTagGID(ctx context.Context, gid string) (string, error) {
	if !IsLocalGID(gid) {
		return gid, nil
	}
	v, err := s.SyncState(ctx, resultPrefixTag+gid)
	if err != nil || v == "" {
		return gid, err
	}
	return v, nil
}

// ReplaceLocalTagGID records the real gid of a tag created offline.
func (s *Store) ReplaceLocalTagGID(ctx context.Context, local, real string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			"UPDATE tags SET workspace_gid = COALESCE((SELECT NULLIF(workspace_gid, '') FROM tags WHERE gid = ?2), workspace_gid) WHERE gid = ?1 AND workspace_gid = ''",
			"DELETE FROM tags WHERE gid = ?2 AND EXISTS (SELECT 1 FROM tags WHERE gid = ?1)",
			"UPDATE tags SET gid = ?2 WHERE gid = ?1",
			"UPDATE OR IGNORE task_tags SET tag_gid = ?2 WHERE tag_gid = ?1",
			"DELETE FROM task_tags WHERE tag_gid = ?1",
		} {
			if _, err := tx.ExecContext(ctx, q, local, real); err != nil {
				return err
			}
		}
		return recordResult(ctx, tx, resultPrefixTag, local, real)
	})
}

// cancelUnsentTag removes the pending entries of kind (add_tag or remove_tag) of task
// taskGID for tag tagGID, and returns how many it removed.
func cancelUnsentTag(ctx context.Context, tx *sql.Tx, kind Kind, taskGID, tagGID string) (int, error) {
	ops, err := opsWhere(ctx, tx, func(op Op) bool { return tagGID != "" && tagOf(op) == tagGID },
		"kind = ? AND task_gid = ? AND state = 'pending'", kind, taskGID)
	if err != nil {
		return 0, err
	}
	return deleteOps(ctx, tx, ops)
}

// tagOf returns the tag an add_tag or remove_tag puts on or takes off, as far as its
// payload reads ("" for another kind).
func tagOf(op Op) string {
	switch op.Kind {
	case OpAddTag:
		p, _ := outbox.BestEffortPayload[outbox.AddTag](op)
		return p.Tag
	case OpRemoveTag:
		p, _ := outbox.BestEffortPayload[outbox.RemoveTag](op)
		return p.Tag
	}
	return ""
}

// reapplyTagOps applies the unsent tag changes of a task again. One whose tag does not
// read (held) has nothing to apply.
func reapplyTagOps(ctx context.Context, tx *sql.Tx, taskGID string) error {
	ops, err := opsWhere(ctx, tx, nil, "task_gid = ? AND kind IN (?, ?) AND state <> 'failed'", taskGID, OpAddTag, OpRemoveTag)
	if err != nil {
		return err
	}
	for _, op := range ops {
		tag := tagOf(op)
		if tag == "" {
			continue
		}
		q := "INSERT OR IGNORE INTO task_tags(task_gid, tag_gid) VALUES(?, ?)"
		if op.Kind == OpRemoveTag {
			q = "DELETE FROM task_tags WHERE task_gid = ? AND tag_gid = ?"
		}
		if _, err := tx.ExecContext(ctx, q, taskGID, tag); err != nil {
			return err
		}
	}
	return nil
}

// AddTags caches tags found in Asana by name (typeahead, Issue #36) that the cache does not
// have yet. A tag already cached is left as it is (the found record may lack its color),
// and a tag deleted here but not yet in Asana stays deleted. It returns how many were added.
func (s *Store) AddTags(ctx context.Context, tags []model.Tag) (int, error) {
	added := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var gids []string
		for _, t := range tags {
			if t.GID == "" {
				continue
			}
			ws := tagWorkspaceGID(t)
			res, err := tx.ExecContext(ctx, `INSERT INTO tags(gid, name, color, workspace_gid) VALUES(?, ?, ?, ?)
				ON CONFLICT(gid) DO NOTHING`, t.GID, t.Name, t.Color, ws)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n > 0 {
				gids = append(gids, t.GID)
			} else if ws != "" {
				if _, err := tx.ExecContext(ctx, "UPDATE tags SET workspace_gid = ? WHERE gid = ? AND workspace_gid = ''", ws, t.GID); err != nil {
					return err
				}
			}
		}
		if len(gids) == 0 {
			return nil
		}
		if err := reapplyTagEdits(ctx, tx, gids...); err != nil {
			return err
		}
		// a tag deleted here (not yet in Asana) was deleted again: it was not added
		args := make([]any, len(gids))
		for i, g := range gids {
			args[i] = g
		}
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tags WHERE gid IN ("+
			strings.TrimSuffix(strings.Repeat("?,", len(gids)), ",")+")", args...).Scan(&added)
	})
	return added, err
}
