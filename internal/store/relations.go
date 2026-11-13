package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Followers, extra projects, duplicates and uploads. Like the other local edits they are
// applied to the store at once and queued in the outbox.
const (
	OpAddFollower      = "add_follower"    // payload: user, user_name
	OpRemoveFollower   = "remove_follower" // payload: user, user_name
	OpAddProject       = "add_project"     // payload: project, project_name, section
	OpRemoveProject    = "remove_project"  // payload: project, project_name, section (to restore)
	OpDuplicateTask    = "duplicate_task"  // task_gid: the local copy; payload: source, name
	OpUploadAttachment = "upload_attachment"
)

// opposite returns the op kind that cancels kind.
func opposite(kind string) string {
	return map[string]string{OpAddFollower: OpRemoveFollower, OpRemoveFollower: OpAddFollower,
		OpAddProject: OpRemoveProject, OpRemoveProject: OpAddProject}[kind]
}

// relationKey is the payload field identifying the related object.
func relationKey(kind string) string {
	if kind == OpAddProject || kind == OpRemoveProject {
		return "project"
	}
	return "user"
}

// queueRelation applies a follower/project change and queues it; doing the opposite of a
// pending change cancels it instead.
func (s *Store) queueRelation(ctx context.Context, kind, taskGID string, payload map[string]any, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", taskGID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("task %s: %w", taskGID, ErrNotFound)
		}
		op := Op{Kind: kind, TaskGID: taskGID, Payload: payload, State: OpPending, CreatedAt: formatTime(now)}
		if err := applyRelation(ctx, tx, op, false); err != nil {
			return err
		}
		key := relationKey(kind)
		res, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE kind = ? AND task_gid = ? AND state = 'pending'
			AND json_extract(payload, '$.`+key+`') = ?`, opposite(kind), taskGID, payload[key])
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		if IsLocalGID(taskGID) && (kind == OpAddProject || kind == OpRemoveProject) {
			return errors.New("まだ送信されていないタスクのプロジェクトは、送信後に変更してください")
		}
		return saveOp(ctx, tx, &op)
	})
}

// applyRelation writes (or, with undo, reverts) a follower/project change locally.
func applyRelation(ctx context.Context, tx *sql.Tx, op Op, undo bool) error {
	add := op.Kind == OpAddFollower || op.Kind == OpAddProject
	if undo {
		add = !add
	}
	var q string
	var args []any
	switch op.Kind {
	case OpAddFollower, OpRemoveFollower:
		user, _ := op.Payload["user"].(string)
		q, args = "DELETE FROM task_followers WHERE task_gid = ? AND user_gid = ?", []any{op.TaskGID, user}
		if add {
			q = "INSERT OR IGNORE INTO task_followers(task_gid, user_gid) VALUES(?, ?)"
		}
	default:
		project, _ := op.Payload["project"].(string)
		section, _ := op.Payload["section"].(string)
		q, args = "DELETE FROM task_memberships WHERE task_gid = ? AND project_gid = ?", []any{op.TaskGID, project}
		if add {
			q, args = "INSERT OR IGNORE INTO task_memberships(task_gid, project_gid, section_gid) VALUES(?, ?, ?)", []any{op.TaskGID, project, section}
		}
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// reapplyRelationOps keeps unsent follower/project changes after a pull.
func reapplyRelationOps(ctx context.Context, tx *sql.Tx, taskGID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM outbox WHERE task_gid = ? AND kind IN (?, ?, ?, ?) AND state <> 'failed' ORDER BY id`,
		taskGID, OpAddFollower, OpRemoveFollower, OpAddProject, OpRemoveProject)
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
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := applyRelation(ctx, tx, *op, false); err != nil {
			return err
		}
	}
	return nil
}

// SetFollowerLocal makes a user follow (or stop following) a task.
func (s *Store) SetFollowerLocal(ctx context.Context, taskGID, userGID string, follow bool, now time.Time) error {
	var name string
	_ = s.db.QueryRowContext(ctx, "SELECT name FROM users WHERE gid = ?", userGID).Scan(&name)
	kind := OpAddFollower
	if !follow {
		kind = OpRemoveFollower
	}
	return s.queueRelation(ctx, kind, taskGID, map[string]any{"user": userGID, "user_name": name}, now)
}

// SetProjectLocal adds a task to a project (into section, or the first section) or removes it.
func (s *Store) SetProjectLocal(ctx context.Context, taskGID, projectGID, sectionGID string, add bool, now time.Time) error {
	var name string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM projects WHERE gid = ?", projectGID).Scan(&name); err != nil {
		return fmt.Errorf("project %s: %w", projectGID, ErrNotFound)
	}
	kind := OpAddProject
	if add {
		if sectionGID == "" {
			var err error
			if sectionGID, err = s.FirstSection(ctx, projectGID); err != nil {
				return err
			}
		}
	} else {
		kind = OpRemoveProject
		_ = s.db.QueryRowContext(ctx, "SELECT section_gid FROM task_memberships WHERE task_gid = ? AND project_gid = ?", taskGID, projectGID).Scan(&sectionGID)
	}
	return s.queueRelation(ctx, kind, taskGID, map[string]any{"project": projectGID, "project_name": name, "section": sectionGID}, now)
}

// TaskFollowers returns who follows a task.
func (s *Store) TaskFollowers(ctx context.Context, taskGID string) ([]UserRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.user_gid, COALESCE(u.name, '') FROM task_followers f
		LEFT JOIN users u ON u.gid = f.user_gid WHERE f.task_gid = ? ORDER BY u.name`, taskGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRef{}
	for rows.Next() {
		var u UserRef
		if err := rows.Scan(&u.GID, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DuplicateTaskLocal makes a local copy of a task (name, description, due date, assignee,
// projects, tags) and queues a duplicate in Asana, which also copies subtasks,
// attachments and followers.
func (s *Store) DuplicateTaskLocal(ctx context.Context, gid, name string, now time.Time) (string, error) {
	if IsLocalGID(gid) {
		return "", errors.New("まだ送信されていないタスクは複製できません（送信後に複製してください）")
	}
	copyGID := LocalPrefix + randomID()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var orig string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = ?", gid).Scan(&orig); err != nil {
			return fmt.Errorf("task %s: %w", gid, ErrNotFound)
		}
		if strings.TrimSpace(name) == "" {
			name = orig + " のコピー"
		}
		ts := formatTime(now)
		for _, q := range []string{
			`INSERT INTO tasks(gid, workspace_gid, name, notes, html_notes, due_on, due_at, start_on, assignee_gid, parent_gid, created_at, modified_at)
				SELECT ?, workspace_gid, ?, notes, html_notes, due_on, due_at, start_on, assignee_gid, parent_gid, ?, ? FROM tasks WHERE gid = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, copyGID, name, ts, ts, gid); err != nil {
				return err
			}
		}
		for _, q := range []string{
			"INSERT INTO task_memberships(task_gid, project_gid, section_gid) SELECT ?, project_gid, section_gid FROM task_memberships WHERE task_gid = ?",
			"INSERT INTO task_tags(task_gid, tag_gid) SELECT ?, tag_gid FROM task_tags WHERE task_gid = ?",
		} {
			if _, err := tx.ExecContext(ctx, q, copyGID, gid); err != nil {
				return err
			}
		}
		return saveOp(ctx, tx, &Op{Kind: OpDuplicateTask, TaskGID: copyGID, State: OpPending, CreatedAt: ts,
			Payload: map[string]any{"source": gid, "name": name}})
	})
	return copyGID, err
}

// UploadAttachmentLocal copies a file into the cache and queues its upload to a task.
// It shows up as an attachment right away (and can be viewed offline).
func (s *Store) UploadAttachmentLocal(ctx context.Context, taskGID, srcPath, cacheDir string, now time.Time) (string, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s はフォルダです", srcPath)
	}
	if info.Size() > 100<<20 {
		return "", errors.New("100MB を超えるファイルは添付できません")
	}
	gid := LocalPrefix + randomID()
	name := filepath.Base(srcPath)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(cacheDir, strings.TrimPrefix(gid, LocalPrefix)+"-"+strings.Map(func(r rune) rune {
		if strings.ContainsRune(" \t/\\:()<>[]", r) {
			return '_'
		}
		return r
	}, name))
	if err := copyFile(srcPath, dst); err != nil {
		return "", err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		ts := formatTime(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments(gid, parent_gid, name, resource_subtype, host, size, created_at, local_path)
			VALUES(?, ?, ?, 'asana', 'asana', ?, ?, ?)`, gid, taskGID, name, info.Size(), ts, dst); err != nil {
			return fmt.Errorf("task %s: %w", taskGID, err)
		}
		return saveOp(ctx, tx, &Op{Kind: OpUploadAttachment, TaskGID: taskGID, State: OpPending, CreatedAt: ts,
			Payload: map[string]any{"attachment": gid, "name": name, "path": dst}})
	})
	return gid, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ReplaceLocalAttachment records the Asana gid of an uploaded file.
func (s *Store) ReplaceLocalAttachment(ctx context.Context, local, real, viewURL string) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return replaceLocalAttachment(ctx, tx, local, real, viewURL) })
}

func replaceLocalAttachment(ctx context.Context, tx *sql.Tx, local, real, viewURL string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE attachments SET gid = ?, view_url = ? WHERE gid = ?", real, viewURL, local); err != nil {
		return err
	}
	return recordResult(ctx, tx, ResultAttachment, local, real)
}
