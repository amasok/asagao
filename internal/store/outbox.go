package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

// Offline editing: every write is applied to the local tables immediately and recorded in
// the outbox. The sync engine sends outbox entries to Asana in order (see sync.Push).
//
// update_task payloads use these keys (values are what Asana accepts):
//
//	completed bool, name string, notes string, due_on "YYYY-MM-DD" or "", assignee gid or ""
//
// "" means "clear" for due_on and assignee.

const (
	OpUpdateTask = "update_task"
	OpCreateTask = "create_task"
	OpAddComment = "add_comment"
	OpMoveTask   = "move_task" // payload: project, section, section_name; base: section

	OpPending  = "pending"
	OpConflict = "conflict"
	OpFailed   = "failed"

	// LocalPrefix marks gids of tasks/comments created offline and not yet sent.
	LocalPrefix = "local:"
)

// IsLocalGID reports whether gid belongs to a task or comment not yet sent to Asana.
func IsLocalGID(gid string) bool { return strings.HasPrefix(gid, LocalPrefix) }

type Op struct {
	ID             int64          `json:"id"`
	Kind           string         `json:"kind"`
	TaskGID        string         `json:"task_gid"`
	TaskName       string         `json:"task_name"`
	Payload        map[string]any `json:"payload"`
	Base           map[string]any `json:"base,omitempty"`
	BaseModifiedAt string         `json:"base_modified_at,omitempty"`
	State          string         `json:"state"`
	Conflict       map[string]any `json:"conflict,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
	Attempts       int            `json:"attempts"`
	CreatedAt      string         `json:"created_at"`
}

// NewTask is the input for CreateTaskLocal.
type NewTask struct {
	WorkspaceGID string
	ProjectGID   string
	SectionGID   string
	Name         string
	Notes        string
	DueOn        string
	AssigneeGID  string
}

// --- local writes ---

// UpdateTaskLocal applies field changes to a task and queues them for Asana.
// Consecutive edits of the same task are merged into one outbox entry; an edit that
// reverts every field to its original value removes the entry.
func (s *Store) UpdateTaskLocal(ctx context.Context, gid string, changes map[string]any, now time.Time) error {
	if err := validateFields(changes); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		cur, modifiedAt, err := currentFields(ctx, tx, gid)
		if err != nil {
			return err
		}
		if IsLocalGID(gid) {
			// Not on the server yet: fold the change into the pending create.
			op, err := findOp(ctx, tx, OpCreateTask, gid)
			if err != nil {
				return err
			}
			if op == nil {
				return fmt.Errorf("task %s: pending create not found", gid)
			}
			maps.Copy(op.Payload, changes)
			if err := saveOp(ctx, tx, op); err != nil {
				return err
			}
			return applyFields(ctx, tx, gid, changes, now)
		}

		op, err := findOp(ctx, tx, OpUpdateTask, gid)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpUpdateTask, TaskGID: gid, Payload: map[string]any{}, Base: map[string]any{},
				BaseModifiedAt: modifiedAt, State: OpPending, CreatedAt: formatTime(now)}
		}
		for k, v := range changes {
			if _, ok := op.Base[k]; !ok {
				op.Base[k] = cur[k]
			}
			if sameValue(op.Base[k], v) {
				delete(op.Payload, k) // back to the original value
			} else {
				op.Payload[k] = v
			}
		}
		if err := applyFields(ctx, tx, gid, changes, now); err != nil {
			return err
		}
		if len(op.Payload) == 0 {
			if op.ID != 0 {
				_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
				return err
			}
			return nil
		}
		return saveOp(ctx, tx, op)
	})
}

// MoveTaskLocal moves a task to another section of a project it belongs to, and queues it.
// Moves are sent as-is (the last move wins); they are not conflict-checked.
func (s *Store) MoveTaskLocal(ctx context.Context, gid, projectGID, sectionGID string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var sectionName string
		err := tx.QueryRowContext(ctx, "SELECT name FROM sections WHERE gid = ? AND project_gid = ?", sectionGID, projectGID).Scan(&sectionName)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("section %s is not in project %s: %w", sectionGID, projectGID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		var current string
		err = tx.QueryRowContext(ctx, "SELECT section_gid FROM task_memberships WHERE task_gid = ? AND project_gid = ?", gid, projectGID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("タスク %s はこのプロジェクトに入っていません", gid)
		}
		if err != nil {
			return err
		}
		if current == sectionGID {
			return nil
		}
		if err := setSection(ctx, tx, gid, projectGID, sectionGID); err != nil {
			return err
		}

		if IsLocalGID(gid) {
			op, err := findOp(ctx, tx, OpCreateTask, gid)
			if err != nil || op == nil {
				return fmt.Errorf("task %s: pending create not found", gid)
			}
			op.Payload["section"] = sectionGID
			return saveOp(ctx, tx, op)
		}

		op, err := findMoveOp(ctx, tx, gid, projectGID)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpMoveTask, TaskGID: gid, Payload: map[string]any{"project": projectGID},
				Base: map[string]any{"section": current}, State: OpPending, CreatedAt: formatTime(now)}
		}
		if op.Base["section"] == sectionGID { // moved back where it started
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		op.Payload["section"] = sectionGID
		op.Payload["section_name"] = sectionName
		return saveOp(ctx, tx, op)
	})
}

func findMoveOp(ctx context.Context, tx *sql.Tx, gid, projectGID string) (*Op, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM outbox WHERE kind = ? AND task_gid = ? AND state = 'pending' ORDER BY id DESC", OpMoveTask, gid)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if op.Payload["project"] == projectGID {
			return op, nil
		}
	}
	return nil, rows.Err()
}

func setSection(ctx context.Context, tx *sql.Tx, gid, projectGID, sectionGID string) error {
	_, err := tx.ExecContext(ctx, "UPDATE task_memberships SET section_gid = ? WHERE task_gid = ? AND project_gid = ?", sectionGID, gid, projectGID)
	return err
}

// CreateTaskLocal inserts a task with a temporary "local:" gid and queues its creation.
func (s *Store) CreateTaskLocal(ctx context.Context, nt NewTask, now time.Time) (string, error) {
	if strings.TrimSpace(nt.Name) == "" {
		return "", errors.New("タスク名が空です")
	}
	if nt.WorkspaceGID == "" && nt.ProjectGID != "" {
		if err := s.db.QueryRowContext(ctx, "SELECT workspace_gid FROM projects WHERE gid = ?", nt.ProjectGID).Scan(&nt.WorkspaceGID); err != nil {
			return "", fmt.Errorf("project %s: %w", nt.ProjectGID, ErrNotFound)
		}
	}
	if nt.WorkspaceGID == "" {
		return "", errors.New("ワークスペースが決まりません（--project または --workspace を指定してください）")
	}
	gid := LocalPrefix + randomID()
	payload := map[string]any{"workspace": nt.WorkspaceGID, "name": nt.Name}
	if nt.ProjectGID != "" {
		payload["project"] = nt.ProjectGID
	}
	if nt.SectionGID != "" {
		payload["section"] = nt.SectionGID
	}
	if nt.Notes != "" {
		payload["notes"] = nt.Notes
	}
	if nt.DueOn != "" {
		payload["due_on"] = nt.DueOn
	}
	if nt.AssigneeGID != "" {
		payload["assignee"] = nt.AssigneeGID
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ts := formatTime(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
			(gid, workspace_gid, name, notes, due_on, assignee_gid, created_at, modified_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			gid, nt.WorkspaceGID, nt.Name, nt.Notes, nt.DueOn, nt.AssigneeGID, ts, ts); err != nil {
			return err
		}
		if nt.ProjectGID != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_memberships(task_gid, project_gid, section_gid) VALUES(?, ?, ?)",
				gid, nt.ProjectGID, nt.SectionGID); err != nil {
				return err
			}
		}
		return saveOp(ctx, tx, &Op{Kind: OpCreateTask, TaskGID: gid, Payload: payload, State: OpPending, CreatedAt: ts})
	})
	return gid, err
}

// AddCommentLocal adds a comment with a temporary gid and queues it.
func (s *Store) AddCommentLocal(ctx context.Context, taskGID, text string, author UserRef, now time.Time) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("コメントが空です")
	}
	gid := LocalPrefix + randomID()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", taskGID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("task %s: %w", taskGID, ErrNotFound)
		}
		ts := formatTime(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO stories
			(gid, task_gid, type, resource_subtype, text, created_by_gid, created_by_name, created_at)
			VALUES(?, ?, 'comment', 'comment_added', ?, ?, ?, ?)`,
			gid, taskGID, text, author.GID, author.Name, ts); err != nil {
			return err
		}
		return saveOp(ctx, tx, &Op{Kind: OpAddComment, TaskGID: taskGID,
			Payload: map[string]any{"text": text, "story_gid": gid}, State: OpPending, CreatedAt: ts})
	})
	return gid, err
}

// --- outbox access for the sync engine ---

func (s *Store) PendingOps(ctx context.Context) ([]Op, error) {
	return s.queryOps(ctx, "WHERE o.state = ?", OpPending)
}

func (s *Store) ListOps(ctx context.Context) ([]Op, error) { return s.queryOps(ctx, "") }

func (s *Store) GetOp(ctx context.Context, id int64) (*Op, error) {
	ops, err := s.queryOps(ctx, "WHERE o.id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("outbox entry %d: %w", id, ErrNotFound)
	}
	return &ops[0], nil
}

func (s *Store) queryOps(ctx context.Context, where string, args ...any) ([]Op, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.kind, o.task_gid, COALESCE(t.name, ''), o.payload, o.base,
		o.base_modified_at, o.state, o.conflict, o.last_error, o.attempts, o.created_at
		FROM outbox o LEFT JOIN tasks t ON t.gid = o.task_gid `+where+` ORDER BY o.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ops := []Op{}
	for rows.Next() {
		var op Op
		var payload, base, conflict string
		if err := rows.Scan(&op.ID, &op.Kind, &op.TaskGID, &op.TaskName, &payload, &base, &op.BaseModifiedAt,
			&op.State, &conflict, &op.LastError, &op.Attempts, &op.CreatedAt); err != nil {
			return nil, err
		}
		if err := unmarshalOp(&op, payload, base, conflict); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// OutboxCounts returns the number of entries per state.
func (s *Store) OutboxCounts(ctx context.Context) (pending, conflict, failed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(state = 'pending'), 0), COALESCE(SUM(state = 'conflict'), 0), COALESCE(SUM(state = 'failed'), 0)
		FROM outbox`).Scan(&pending, &conflict, &failed)
	return
}

// CompleteOp removes a sent entry.
func (s *Store) CompleteOp(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id)
	return err
}

func (s *Store) MarkOpConflict(ctx context.Context, id int64, server map[string]any) error {
	b, err := json.Marshal(server)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE outbox SET state = 'conflict', conflict = ?, attempts = attempts + 1 WHERE id = ?", string(b), id)
	return err
}

func (s *Store) MarkOpFailed(ctx context.Context, id int64, cause error) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'failed', last_error = ?, attempts = attempts + 1 WHERE id = ?", cause.Error(), id)
	return err
}

// ResolveKeepLocal accepts the server's current values as the new base, so the local
// values are sent on the next push (overwriting the other change).
func (s *Store) ResolveKeepLocal(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		maps.Copy(op.Base, op.Conflict)
		op.Conflict, op.State, op.LastError = map[string]any{}, OpPending, ""
		return saveOp(ctx, tx, op)
	})
}

// RetryOp puts a failed entry back in the queue.
func (s *Store) RetryOp(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'pending', last_error = '' WHERE id = ?", id)
	return err
}

// DiscardOp drops a local change. The task keeps its local values until the next sync
// (or FetchDetails) brings the server values back; offline-created tasks/comments are removed.
func (s *Store) DiscardOp(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		switch op.Kind {
		case OpCreateTask:
			if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE task_gid = ?", op.TaskGID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE gid = ?", op.TaskGID); err != nil {
				return err
			}
		case OpAddComment:
			if gid, _ := op.Payload["story_gid"].(string); gid != "" {
				if _, err := tx.ExecContext(ctx, "DELETE FROM stories WHERE gid = ?", gid); err != nil {
					return err
				}
			}
		case OpMoveTask:
			project, _ := op.Payload["project"].(string)
			section, _ := op.Base["section"].(string)
			if err := setSection(ctx, tx, op.TaskGID, project, section); err != nil {
				return err
			}
		case OpUpdateTask:
			// Restore the values from before the local edit.
			if err := applyFields(ctx, tx, op.TaskGID, op.Base, time.Time{}); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id)
		return err
	})
}

// ReplaceLocalTaskGID swaps a temporary gid for the real one after the task was created in Asana.
func (s *Store) ReplaceLocalTaskGID(ctx context.Context, local, real string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		// Children reference tasks(gid); check the constraints at commit, after all rows moved.
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
			return err
		}
		for _, q := range []string{
			"DELETE FROM tasks WHERE gid = ?2", // in case a sync already pulled it
			"UPDATE tasks SET gid = ?2 WHERE gid = ?1",
			"UPDATE tasks SET parent_gid = ?2 WHERE parent_gid = ?1",
			"UPDATE task_memberships SET task_gid = ?2 WHERE task_gid = ?1",
			"UPDATE task_tags SET task_gid = ?2 WHERE task_gid = ?1",
			"UPDATE stories SET task_gid = ?2 WHERE task_gid = ?1",
			"UPDATE outbox SET task_gid = ?2 WHERE task_gid = ?1",
			// Remember the mapping so callers holding the local gid can find the task.
			"INSERT OR REPLACE INTO sync_state(key, value) VALUES ('gid:' || ?1, ?2)",
		} {
			if _, err := tx.ExecContext(ctx, q, local, real); err != nil {
				return err
			}
		}
		return nil
	})
}

// RealGID returns the Asana gid for a task created offline, or gid itself if it is not
// a local gid or has not been sent yet.
func (s *Store) RealGID(ctx context.Context, gid string) (string, error) {
	if !IsLocalGID(gid) {
		return gid, nil
	}
	v, err := s.SyncState(ctx, "gid:"+gid)
	if err != nil || v == "" {
		return gid, err
	}
	return v, nil
}

// ReplaceLocalStory swaps an offline comment for the one Asana created.
func (s *Store) ReplaceLocalStory(ctx context.Context, localGID, taskGID string, st asana.Story) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM stories WHERE gid = ? OR gid = ?", localGID, st.GID); err != nil {
			return err
		}
		var byGID, byName string
		if st.CreatedBy != nil {
			byGID, byName = st.CreatedBy.GID, st.CreatedBy.Name
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO stories
			(gid, task_gid, type, resource_subtype, text, created_by_gid, created_by_name, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			st.GID, taskGID, st.Type, st.ResourceSubtype, st.Text, byGID, byName, formatTime(st.CreatedAt))
		return err
	})
}

// --- helpers ---

var updatableFields = map[string]bool{"completed": true, "name": true, "notes": true, "due_on": true, "assignee": true}

func validateFields(changes map[string]any) error {
	if len(changes) == 0 {
		return errors.New("変更がありません")
	}
	for k, v := range changes {
		if !updatableFields[k] {
			return fmt.Errorf("unsupported field %q", k)
		}
		switch k {
		case "completed":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("completed must be bool")
			}
		default:
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s must be string", k)
			}
		}
	}
	if name, ok := changes["name"].(string); ok && strings.TrimSpace(name) == "" {
		return errors.New("タスク名が空です")
	}
	return nil
}

// TaskFieldValues extracts update_task fields from an API task (for conflict checks).
func TaskFieldValues(t *asana.Task) map[string]any {
	assignee := ""
	if t.Assignee != nil {
		assignee = t.Assignee.GID
	}
	return map[string]any{"completed": t.Completed, "name": t.Name, "notes": t.Notes, "due_on": t.DueOn, "assignee": assignee}
}

func currentFields(ctx context.Context, tx *sql.Tx, gid string) (map[string]any, string, error) {
	var completed bool
	var name, notes, dueOn, assignee, modifiedAt string
	err := tx.QueryRowContext(ctx, "SELECT completed, name, notes, due_on, assignee_gid, modified_at FROM tasks WHERE gid = ?", gid).
		Scan(&completed, &name, &notes, &dueOn, &assignee, &modifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("task %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return nil, "", err
	}
	return map[string]any{"completed": completed, "name": name, "notes": notes, "due_on": dueOn, "assignee": assignee}, modifiedAt, nil
}

// applyFields writes update_task fields to the tasks table.
func applyFields(ctx context.Context, tx *sql.Tx, gid string, fields map[string]any, now time.Time) error {
	for k, v := range fields {
		var err error
		switch k {
		case "completed":
			done, _ := v.(bool)
			var at any
			if done && !now.IsZero() {
				at = formatTime(now)
			}
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET completed = ?, completed_at = CASE WHEN ? THEN COALESCE(?, completed_at) ELSE NULL END WHERE gid = ?",
				done, done, at, gid)
		case "name", "notes", "due_on":
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET "+k+" = ? WHERE gid = ?", v, gid)
		case "assignee":
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET assignee_gid = ? WHERE gid = ?", v, gid)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// reapplyUpdates re-applies unsent update_task payloads for a task (after a pull overwrote it).
func reapplyUpdates(ctx context.Context, tx *sql.Tx, gid string) error {
	rows, err := tx.QueryContext(ctx, `SELECT kind, payload FROM outbox
		WHERE task_gid = ? AND kind IN (?, ?) AND state <> 'failed' ORDER BY id`, gid, OpUpdateTask, OpMoveTask)
	if err != nil {
		return err
	}
	type entry struct {
		kind    string
		payload map[string]any
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var p string
		if err := rows.Scan(&e.kind, &p); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(p), &e.payload); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range entries {
		var err error
		if e.kind == OpMoveTask {
			project, _ := e.payload["project"].(string)
			section, _ := e.payload["section"].(string)
			err = setSection(ctx, tx, gid, project, section)
		} else {
			err = applyFields(ctx, tx, gid, e.payload, time.Time{})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func findOp(ctx context.Context, tx *sql.Tx, kind, gid string) (*Op, error) {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM outbox WHERE kind = ? AND task_gid = ? AND state = 'pending' ORDER BY id DESC LIMIT 1", kind, gid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getOpTx(ctx, tx, id)
}

func getOpTx(ctx context.Context, tx *sql.Tx, id int64) (*Op, error) {
	var op Op
	var payload, base, conflict string
	err := tx.QueryRowContext(ctx, `SELECT id, kind, task_gid, payload, base, base_modified_at, state, conflict, last_error, attempts, created_at
		FROM outbox WHERE id = ?`, id).Scan(&op.ID, &op.Kind, &op.TaskGID, &payload, &base, &op.BaseModifiedAt,
		&op.State, &conflict, &op.LastError, &op.Attempts, &op.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("outbox entry %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &op, unmarshalOp(&op, payload, base, conflict)
}

func saveOp(ctx context.Context, tx *sql.Tx, op *Op) error {
	payload, err := json.Marshal(op.Payload)
	if err != nil {
		return err
	}
	base, err := json.Marshal(nonNil(op.Base))
	if err != nil {
		return err
	}
	conflict, err := json.Marshal(nonNil(op.Conflict))
	if err != nil {
		return err
	}
	if op.ID == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, op.Kind, op.TaskGID, string(payload), string(base), op.BaseModifiedAt, op.State, string(conflict), op.CreatedAt)
		if err != nil {
			return err
		}
		op.ID, err = res.LastInsertId()
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE outbox SET payload = ?, base = ?, base_modified_at = ?, state = ?, conflict = ?, last_error = ?
		WHERE id = ?`, string(payload), string(base), op.BaseModifiedAt, op.State, string(conflict), op.LastError, op.ID)
	return err
}

func unmarshalOp(op *Op, payload, base, conflict string) error {
	for _, f := range []struct {
		src string
		dst *map[string]any
	}{{payload, &op.Payload}, {base, &op.Base}, {conflict, &op.Conflict}} {
		*f.dst = map[string]any{}
		if f.src != "" {
			if err := json.Unmarshal([]byte(f.src), f.dst); err != nil {
				return fmt.Errorf("outbox %d: %w", op.ID, err)
			}
		}
	}
	return nil
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func sameValue(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
