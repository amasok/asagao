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

	"github.com/amasok/asagao/internal/domain/outbox"
)

func init() {
	registerUndo(OpAddFollower, undoWith[outbox.Follower](applyRelation))
	registerUndo(OpRemoveFollower, undoWith[outbox.Follower](applyRelation))
	registerUndo(OpAddProject, undoWith[outbox.ProjectMembership](applyRelation))
	registerUndo(OpRemoveProject, undoWith[outbox.ProjectMembership](applyRelation))
}

// Followers, extra projects, duplicates and uploads. Like the other local edits they are
// applied to the store at once and queued in the outbox.
const (
	OpAddFollower      = outbox.KindAddFollower
	OpRemoveFollower   = outbox.KindRemoveFollower
	OpAddProject       = outbox.KindAddProject
	OpRemoveProject    = outbox.KindRemoveProject
	OpDuplicateTask    = outbox.KindDuplicateTask
	OpUploadAttachment = outbox.KindUploadAttachment
)

// opposite returns the op kind that cancels kind.
func opposite(kind Kind) Kind {
	return map[Kind]Kind{OpAddFollower: OpRemoveFollower, OpRemoveFollower: OpAddFollower,
		OpAddProject: OpRemoveProject, OpRemoveProject: OpAddProject}[kind]
}

// relationTarget returns the payload field identifying the related object of a follower
// or project change (payload p), and the object.
func relationTarget(p any) (key, gid string) {
	switch p := p.(type) {
	case outbox.ProjectMembership:
		return "project", p.Project
	case outbox.Follower:
		return "user", p.User
	}
	panic(fmt.Sprintf("store: %T is not the payload of a relation", p))
}

// queueRelation applies a follower/project change and queues it; doing the opposite of a
// pending change cancels it instead.
func (s *Store) queueRelation(ctx context.Context, kind Kind, taskGID string, p any, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		op := Op{Kind: kind, TaskGID: taskGID, State: OpPending, CreatedAt: formatTime(now)}
		if err := outbox.SetPayload(&op, p); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", taskGID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("task %s: %w", taskGID, ErrNotFound)
		}
		if err := applyRelation(ctx, tx, op, false); err != nil {
			return err
		}
		key, target := relationTarget(p)
		res, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE kind = ? AND task_gid = ? AND state = 'pending'
			AND json_extract(payload, '$.`+key+`') = ?`, opposite(kind), taskGID, target)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("cancel unsent %s: %w", opposite(kind), err)
		}
		if n > 0 {
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
		p, _ := outbox.BestEffortPayload[outbox.Follower](op)
		user := p.User
		q, args = "DELETE FROM task_followers WHERE task_gid = ? AND user_gid = ?", []any{op.TaskGID, user}
		if add {
			// A task gone from the cache has nothing to put back (#87).
			q, args = "INSERT OR IGNORE INTO task_followers(task_gid, user_gid) SELECT ?1, ?2 WHERE EXISTS (SELECT 1 FROM tasks WHERE gid = ?1)", []any{op.TaskGID, user}
		}
	default:
		p, _ := outbox.BestEffortPayload[outbox.ProjectMembership](op)
		project, section := p.Project, outbox.Str(p.Section)
		q, args = "DELETE FROM task_memberships WHERE task_gid = ? AND project_gid = ?", []any{op.TaskGID, project}
		if add {
			q, args = `INSERT OR IGNORE INTO task_memberships(task_gid, project_gid, section_gid)
				SELECT ?1, ?2, ?3 WHERE EXISTS (SELECT 1 FROM tasks WHERE gid = ?1)`, []any{op.TaskGID, project, section}
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
	return s.queueRelation(ctx, kind, taskGID, outbox.Follower{User: userGID, UserName: name}, now)
}

// SetProjectLocal adds a task to a project (into section, or the first section) or removes it.
func (s *Store) SetProjectLocal(ctx context.Context, taskGID, projectGID, sectionGID string, add bool, now time.Time) error {
	var name string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM projects WHERE gid = ?", projectGID).Scan(&name); err != nil {
		return notFound(err, "project "+projectGID)
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
	return s.queueRelation(ctx, kind, taskGID, outbox.ProjectMembership{Project: projectGID, ProjectName: name, Section: &sectionGID}, now)
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
	copyGID := newLocalGID()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var orig string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = ?", gid).Scan(&orig); err != nil {
			return notFound(err, "task "+gid)
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
		return savePayload(ctx, tx, &Op{Kind: OpDuplicateTask, TaskGID: copyGID, State: OpPending, CreatedAt: ts},
			outbox.DuplicateTask{Source: gid, Name: name})
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
	// Only regular files: /dev/zero, a FIFO or a socket has size 0 and would pass the
	// size check, then be read without end (and opening a FIFO waits for a writer).
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s は通常のファイルではありません", srcPath)
	}
	if info.Size() > MaxUploadSize {
		return "", errTooLarge
	}
	gid := newLocalGID()
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
	size, err := copyFile(srcPath, dst, info, MaxUploadSize)
	if err != nil {
		return "", err
	}
	committing := false // fn returned nil: an error from here on came from Commit
	defer func() {
		switch {
		case err == nil:
		case !committing:
			_ = os.Remove(dst) // nothing was written: nothing refers to the copy
		default:
			s.discardUnrecordedCopy(gid, dst)
		}
	}()
	err = s.tx(ctx, func(tx *sql.Tx) error {
		ts := formatTime(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments(gid, parent_gid, name, resource_subtype, host, size, created_at, local_path)
			VALUES(?, ?, ?, 'asana', 'asana', ?, ?, ?)`, gid, taskGID, name, size, ts, dst); err != nil {
			return fmt.Errorf("task %s: %w", taskGID, err)
		}
		if err := savePayload(ctx, tx, &Op{Kind: OpUploadAttachment, TaskGID: taskGID, State: OpPending, CreatedAt: ts},
			outbox.UploadAttachment{Attachment: gid, Name: name, Path: dst}); err != nil {
			return err
		}
		committing = true
		return nil
	})
	return gid, err
}

// discardUnrecordedCopy removes dst, the cached copy of the attachment gid, after the
// commit recording it returned an error, so no file is left that nothing refers to.
// The file stays when the row is there after all (the commit went through: the outbox
// op still needs it) or when that can't be checked: an unused file is better than an
// upload that has lost its file.
func (s *Store) discardUnrecordedCopy(gid, dst string) {
	var n int
	if err := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM attachments WHERE gid = ?`, gid).Scan(&n); err != nil || n != 0 {
		return
	}
	_ = os.Remove(dst)
}

// MaxUploadSize is the largest file that can be attached (Asana's limit).
const MaxUploadSize = 100 << 20

var errTooLarge = errors.New("100MB を超えるファイルは添付できません")

// copyFile copies the regular file src (statted as want) to dst, reading at most limit
// bytes: the file may grow, or be replaced, after it was checked.
func copyFile(src, dst string, want os.FileInfo, limit int64) (int64, error) {
	in, err := openNoBlock(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if got, err := in.Stat(); err != nil {
		return 0, err
	} else if !got.Mode().IsRegular() || !os.SameFile(got, want) {
		return 0, fmt.Errorf("%s が確認中に変わりました", src)
	}
	// O_EXCL: never write over (or, on failure, remove) a file that was already there.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if err == nil && n > limit {
		err = errTooLarge
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return 0, err
	}
	return n, nil
}

// ReplaceLocalAttachment records the Asana gid of an uploaded file.
func (s *Store) ReplaceLocalAttachment(ctx context.Context, local, real, viewURL string) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return replaceLocalAttachment(ctx, tx, local, real, viewURL) })
}

func replaceLocalAttachment(ctx context.Context, tx *sql.Tx, local, real, viewURL string) error {
	var parent string
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT parent_gid FROM attachments WHERE gid = ?), '')", local).Scan(&parent); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE attachments SET gid = ?, view_url = ? WHERE gid = ?", real, viewURL, local); err != nil {
		return err
	}
	if err := recordResult(ctx, tx, resultPrefixAttachment, local, real); err != nil {
		return err
	}
	return reapplyAttachmentDeletes(ctx, tx, parent)
}
