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
	"slices"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/commitref"
	"github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

func init() {
	registerUndo(OpCreateTask, func(ctx context.Context, tx *sql.Tx, op *Op, _ outbox.CreateTask) error {
		return discardLocalTask(ctx, tx, op.TaskGID, true)
	})
	registerUndo(OpDuplicateTask, func(ctx context.Context, tx *sql.Tx, op *Op, _ outbox.DuplicateTask) error {
		return discardLocalTask(ctx, tx, op.TaskGID, false)
	})
	// Restore the values from before the local edit.
	registerUndo(OpUpdateTask, func(ctx context.Context, tx *sql.Tx, op *Op, _ outbox.UpdateTask) error {
		return applyFields(ctx, tx, op.TaskGID, op.Base, time.Time{})
	})
	registerUndo(OpMoveTask, func(ctx context.Context, tx *sql.Tx, op *Op, p outbox.MoveTask) error {
		project := p.Project
		section, _ := op.Base["section"].(string)
		if err := setSection(ctx, tx, op.TaskGID, project, section); err != nil {
			return err
		}
		return restorePosition(ctx, tx, op.TaskGID, project, op.Base)
	})
	// The task stays in the store, hidden while its delete is queued: dropping the entry
	// shows it again.
	registerUndo(OpDeleteTask, nothingToUndo[outbox.DeleteTask])
}

// Offline editing: every write is applied to the local tables immediately and recorded in
// the outbox. The sync engine sends outbox entries to Asana in order (see sync.Push).
//
// update_task payloads use these keys (values are what Asana accepts):
//
//	completed bool, name string, notes string, due_on "YYYY-MM-DD" or "", assignee gid or ""
//
// "" means "clear" for due_on and assignee.

// The kinds and states are aliases of domain/outbox's.
const (
	OpUpdateTask = outbox.KindUpdateTask
	OpCreateTask = outbox.KindCreateTask
	OpAddComment = outbox.KindAddComment
	OpMoveTask   = outbox.KindMoveTask

	OpPending  = outbox.StatePending
	OpConflict = outbox.StateConflict
	OpFailed   = outbox.StateFailed

	// LocalPrefix marks gids of tasks/comments created offline and not yet sent (an alias
	// of gid.Prefix, kept while the callers move to domain/gid).
	LocalPrefix = gid.Prefix
)

// IsLocalGID reports whether gid belongs to a task or comment not yet sent to Asana (see
// gid.IsLocal, which it calls).
func IsLocalGID(g string) bool { return gid.IsLocal(g) }

// Op is an outbox entry (an alias of outbox.Op).
type Op = outbox.Op

// Kind is the kind of an outbox entry (an alias of outbox.Kind).
type Kind = outbox.Kind

// NewTask is the input for CreateTaskLocal (an alias of task.NewTask; see TagEdit).
type NewTask = task.NewTask

// --- local writes ---

// UpdateTaskLocal applies field changes to a task and queues them for Asana.
// Consecutive edits of the same task are merged into one outbox entry (outbox.Fold); an
// edit that reverts every field to its original value removes the entry.
func (s *Store) UpdateTaskLocal(ctx context.Context, gid string, changes map[string]any, now time.Time) error {
	if err := task.ValidateFields(changes); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error { return updateTaskLocal(ctx, tx, gid, changes, now) })
}

// updateTaskLocal is UpdateTaskLocal within tx; changes are validated already.
func updateTaskLocal(ctx context.Context, tx *sql.Tx, gid string, changes map[string]any, now time.Time) error {
	cur, modifiedAt, err := currentFields(ctx, tx, gid)
	if err != nil {
		return err
	}
	subtype, _ := cur["resource_subtype"].(string)
	if err := task.CheckApproval(subtype, changes); err != nil {
		return err
	}
	if err := checkTaskDates(ctx, tx, gid, cur, changes, now); err != nil {
		return err
	}
	if changes, err = customTypeChanges(ctx, tx, gid, cur, changes); err != nil {
		return err
	}
	if IsLocalGID(gid) {
		// Not on the server yet: fold the change into the pending create.
		op, err := findUnsentCreate(ctx, tx, gid)
		if err != nil {
			return err
		}
		// A custom type is set after the create (#59: a create may not take one), so it and
		// every later edit of the task join the update entry, which is sent after it.
		var update *Op
		if op != nil && !task.TouchesCustomType(changes) {
			if update, err = findUpdateOp(ctx, tx, gid); err != nil {
				return err
			}
		}
		if op != nil && update == nil && !task.TouchesCustomType(changes) {
			maps.Copy(op.Payload, changes)
			if err := saveOp(ctx, tx, op); err != nil {
				return err
			}
			return applyFields(ctx, tx, gid, changes, now)
		}
		// The create is being sent, was sent (it may be waiting to be verified, which
		// looks for what was sent), or failed, or the task is a local duplicate: queue
		// the edit as an update, sent once the task exists.
		if err := ensureCreateQueued(ctx, tx, gid); err != nil {
			return err
		}
	}

	// Join the unsent (or conflicted) entry, so its base stays the server value; an
	// entry being sent is left alone and the edit gets a new one.
	op, err := findUpdateOp(ctx, tx, gid)
	if err != nil {
		return err
	}
	if op == nil {
		op = &Op{Kind: OpUpdateTask, TaskGID: gid, Payload: map[string]any{}, Base: map[string]any{},
			BaseModifiedAt: modifiedAt, State: OpPending, CreatedAt: formatTime(now)}
	}
	folded, deleteOp := outbox.Fold(*op, cur, changes)
	if err := applyFields(ctx, tx, gid, changes, now); err != nil {
		return err
	}
	if deleteOp {
		if folded.ID != 0 {
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", folded.ID)
			return err
		}
		return nil
	}
	return saveOp(ctx, tx, &folded)
}

// MoveTaskLocal moves a task to another section of a project it belongs to, and queues it.
// Moves are sent as-is (the last move wins); they are not conflict-checked.
func (s *Store) MoveTaskLocal(ctx context.Context, gid, projectGID, sectionGID string, now time.Time) error {
	return s.MoveTaskAtLocal(ctx, gid, projectGID, sectionGID, Position{}, now)
}

// MoveTaskAtLocal moves a task as MoveTaskLocal, to pos in the section: just before or
// after another task of it (pos.Before / pos.After); with a position, a move within the
// task's own section is queued too. The cache puts the task where Asana will (#51): there,
// else at the top of a section it comes from another one to.
func (s *Store) MoveTaskAtLocal(ctx context.Context, gid, projectGID, sectionGID string, pos Position, now time.Time) error {
	if pos.Before != "" && pos.After != "" {
		return errors.New("前と後は、どちらか一方だけ指定してください")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := refuseDeleted(ctx, tx, gid, pos.Before, pos.After); err != nil {
			return err
		}
		for _, anchor := range []string{pos.Before, pos.After} {
			if anchor == "" {
				continue
			}
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM task_memberships WHERE task_gid = ? AND project_gid = ? AND section_gid = ?",
				anchor, projectGID, sectionGID).Scan(&n); err != nil {
				return err
			}
			if n == 0 || anchor == gid {
				return fmt.Errorf("位置の目印 %s は、移動先のセクションにあるほかのタスクを指定してください", anchor)
			}
		}
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
		if current == sectionGID && pos.IsZero() {
			return nil
		}
		position, err := sectionPosition(ctx, tx, gid, projectGID)
		if err != nil {
			return err
		}
		place := map[string]any{} // where it was, to give back
		if err := recordPlace(ctx, tx, place, sectionList(projectGID, current), gid, position); err != nil {
			return err
		}
		if err := setSection(ctx, tx, gid, projectGID, sectionGID); err != nil {
			return err
		}

		if IsLocalGID(gid) {
			op, err := findUnsentCreate(ctx, tx, gid)
			if err != nil {
				return err
			}
			if op != nil && pos.IsZero() {
				// created there: Asana's place for it is not known until it is fetched
				op.Payload["section"] = sectionGID
				return saveOp(ctx, tx, op)
			}
			// The create was (or is being) sent, or failed: queue a move, sent once the task exists.
			if err := ensureCreateQueued(ctx, tx, gid); err != nil {
				return err
			}
		}

		op, err := findMoveOp(ctx, tx, gid, projectGID)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpMoveTask, TaskGID: gid, Payload: map[string]any{"project": projectGID},
				Base: place, State: OpPending, CreatedAt: formatTime(now)}
			op.Base["section"] = current
		}
		if op.Base["section"] == sectionGID && pos.IsZero() { // moved back where it started
			if err := restorePosition(ctx, tx, gid, projectGID, op.Base); err != nil {
				return err
			}
			if op.ID == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		if err := placeInSection(ctx, tx, gid, projectGID, sectionGID, pos, current != sectionGID); err != nil {
			return err
		}
		if !pos.IsZero() && op.ID != 0 {
			// A place is next to a task that may have moved since: go after those moves.
			if err := requeue(ctx, tx, op, now); err != nil {
				return err
			}
		}
		op.Payload["section"] = sectionGID
		op.Payload["section_name"] = sectionName
		delete(op.Payload, "insert_before") // the last move says where
		delete(op.Payload, "insert_after")
		if pos.Before != "" {
			op.Payload["insert_before"] = pos.Before
		}
		if pos.After != "" {
			op.Payload["insert_after"] = pos.After
		}
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

// setSection moves a task to another section of a project locally. Its place in the old
// section means nothing in the new one: it is not known until placed (placeInSection).
func setSection(ctx context.Context, tx *sql.Tx, gid, projectGID, sectionGID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE task_memberships SET section_gid = ?1,
		position = CASE WHEN section_gid = ?1 THEN position END WHERE task_gid = ?2 AND project_gid = ?3`, sectionGID, gid, projectGID)
	return err
}

// restorePosition gives a task back its place in its section of project that base (of a
// move it no longer makes) recorded, when it recorded one.
func restorePosition(ctx context.Context, tx *sql.Tx, gid, projectGID string, base map[string]any) error {
	var section string
	err := tx.QueryRowContext(ctx, "SELECT section_gid FROM task_memberships WHERE task_gid = ? AND project_gid = ?", gid, projectGID).Scan(&section)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return restorePlace(ctx, tx, base, sectionList(projectGID, section), gid, func(p sql.NullInt64) error {
		_, err := tx.ExecContext(ctx, "UPDATE task_memberships SET position = ? WHERE task_gid = ? AND project_gid = ?", p, gid, projectGID)
		return err
	})
}

// CreateTaskLocal inserts a task with a temporary "local:" gid and queues its creation.
func (s *Store) CreateTaskLocal(ctx context.Context, nt NewTask, now time.Time) (string, error) {
	if strings.TrimSpace(nt.Name) == "" {
		return "", errors.New("タスク名が空です")
	}
	if nt.ParentGID != "" {
		// its own workspace, else its project's (as TaskWorkspace: the 402 of a start date
		// is recorded and looked up by it)
		ws, err := taskWorkspace(ctx, s.db, nt.ParentGID)
		if errors.Is(err, ErrNotFound) {
			return "", fmt.Errorf("parent task %s: %w", nt.ParentGID, ErrNotFound)
		} else if err != nil {
			return "", err
		}
		nt.WorkspaceGID = ws
		nt.ProjectGID, nt.SectionGID = "", "" // subtasks are not in projects
	}
	if nt.WorkspaceGID == "" && nt.ProjectGID != "" {
		if err := s.db.QueryRowContext(ctx, "SELECT workspace_gid FROM projects WHERE gid = ?", nt.ProjectGID).Scan(&nt.WorkspaceGID); err != nil {
			return "", notFound(err, "project "+nt.ProjectGID)
		}
	}
	if nt.WorkspaceGID == "" && nt.ParentGID == "" { // subtasks inherit it from the parent in Asana
		return "", errors.New("ワークスペースが決まりません（--project または --workspace を指定してください）")
	}
	if err := task.CheckDates(nt.StartOn, nt.DueOn, false); err != nil {
		return "", err
	}
	if nt.StartOn != "" {
		if _, err := time.Parse(time.DateOnly, nt.StartOn); err != nil {
			return "", fmt.Errorf("start_on は YYYY-MM-DD で書いてください: %q", nt.StartOn)
		}
		if err := startOnOffered(ctx, s.db, nt.WorkspaceGID, now); err != nil {
			return "", err
		}
	}
	gid := newLocalGID()
	payload := map[string]any{"workspace": nt.WorkspaceGID, "name": nt.Name}
	if nt.ParentGID != "" {
		payload["parent"] = nt.ParentGID
	}
	if nt.ProjectGID != "" {
		payload["project"] = nt.ProjectGID
	}
	if nt.SectionGID != "" {
		payload["section"] = nt.SectionGID
	}
	if nt.Notes != "" {
		payload["notes"] = nt.Notes
	}
	if nt.HTMLNotes != "" {
		payload["html_notes"] = nt.HTMLNotes
	}
	if nt.DueOn != "" {
		payload["due_on"] = nt.DueOn
	}
	if nt.StartOn != "" {
		payload["start_on"] = nt.StartOn
	}
	if nt.AssigneeGID != "" {
		payload["assignee"] = nt.AssigneeGID
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ts := formatTime(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
			(gid, workspace_gid, name, notes, html_notes, due_on, start_on, assignee_gid, parent_gid, created_at, modified_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			gid, nt.WorkspaceGID, nt.Name, nt.Notes, nt.HTMLNotes, nt.DueOn, nt.StartOn, nt.AssigneeGID, nt.ParentGID, ts, ts); err != nil {
			return err
		}
		if nt.ParentGID != "" {
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET num_subtasks = num_subtasks + 1 WHERE gid = ?", nt.ParentGID); err != nil {
				return err
			}
		}
		if nt.ProjectGID != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_memberships(task_gid, project_gid, section_gid) VALUES(?, ?, ?)",
				gid, nt.ProjectGID, nt.SectionGID); err != nil {
				return err
			}
			// Working in a project opens it (#54): the syncs keep it, and the new task, up to
			// date (a full sync would otherwise remove the task once it was sent).
			if err := wantProject(ctx, tx, nt.ProjectGID); err != nil {
				return err
			}
		}
		return saveOp(ctx, tx, &Op{Kind: OpCreateTask, TaskGID: gid, Payload: payload, State: OpPending, CreatedAt: ts})
	})
	return gid, err
}

// AddCommentLocal adds a plain-text comment with a temporary gid and queues it.
func (s *Store) AddCommentLocal(ctx context.Context, taskGID, text string, author UserRef, now time.Time) (string, error) {
	return s.AddRichCommentLocal(ctx, taskGID, text, "", author, now)
}

// AddRichCommentLocal adds a comment; htmlText (Asana rich text) is sent instead of text if set.
func (s *Store) AddRichCommentLocal(ctx context.Context, taskGID, text, htmlText string, author UserRef, now time.Time) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("コメントが空です")
	}
	gid := newLocalGID()
	err := s.tx(ctx, func(tx *sql.Tx) error { return addCommentLocal(ctx, tx, gid, taskGID, text, htmlText, author, now) })
	return gid, err
}

// addCommentLocal is AddRichCommentLocal within tx, for the comment's local gid.
func addCommentLocal(ctx context.Context, tx *sql.Tx, gid, taskGID, text, htmlText string, author UserRef, now time.Time) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", taskGID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("task %s: %w", taskGID, ErrNotFound)
	}
	ts := formatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO stories
		(gid, task_gid, type, resource_subtype, text, created_by_gid, created_by_name, created_at, html_text)
		VALUES(?, ?, 'comment', 'comment_added', ?, ?, ?, ?, ?)`,
		gid, taskGID, text, author.GID, author.Name, ts, htmlText); err != nil {
		return err
	}
	payload := map[string]any{"text": text, "story_gid": gid}
	if htmlText != "" {
		payload["html_text"] = htmlText
	}
	return saveOp(ctx, tx, &Op{Kind: OpAddComment, TaskGID: taskGID, Payload: payload, State: OpPending, CreatedAt: ts})
}

// commentOncePrefix starts the sync_state keys that record the markers AddCommentOnceLocal
// added: "comment-once:<task gid>\x00<marker>".
const commentOncePrefix = "comment-once:"

// storyMarked reports whether a comment of taskGID carries marker (commitref.Marked).
func storyMarked(ctx context.Context, tx *sql.Tx, taskGID, marker string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT text, COALESCE(html_text, '') FROM stories
		WHERE task_gid = ?1 AND (instr(text, ?2) > 0 OR instr(html_text, ?2) > 0)`, taskGID, marker)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var text, html string
		if err := rows.Scan(&text, &html); err != nil {
			return false, err
		}
		if commitref.Marked(text, html, marker) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// AddCommentOnceLocal adds a comment to taskGID, as AddRichCommentLocal, unless it added
// one with marker before (kept in sync_state, which refreshing the comments does not
// touch) or one of the task's comments (sent or queued) carries marker (commitref.Marked); changes, when not empty, are applied to
// the task first, as UpdateTaskLocal. All of it is one transaction, which takes the write
// lock at BEGIN: two processes adding the same marker at once (a git hook and a scan of the
// same commit, #8) add it once. added is false, and nothing is queued, when the marker is
// there already.
func (s *Store) AddCommentOnceLocal(ctx context.Context, taskGID, marker string, changes map[string]any, text, htmlText string, author UserRef, now time.Time) (gid string, added bool, err error) {
	if strings.TrimSpace(text) == "" {
		return "", false, errors.New("コメントが空です")
	}
	if !commitref.Marked(text, htmlText, marker) {
		return "", false, errors.New("コメントに印がありません")
	}
	if len(changes) > 0 {
		if err := task.ValidateFields(changes); err != nil {
			return "", false, err
		}
	}
	gid = newLocalGID()
	err = s.tx(ctx, func(tx *sql.Tx) error {
		// The record is what counts on this computer: a refresh that read the comments
		// before another process sent ours replaces them without it (ReplaceStories).
		key := commentOncePrefix + taskGID + "\x00" + marker
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sync_state WHERE key = ?", key).Scan(&n); err != nil || n > 0 {
			return err
		}
		record := func() error {
			_, err := tx.ExecContext(ctx, "INSERT INTO sync_state(key, value) VALUES(?, ?)", key, formatTime(now))
			return err
		}
		marked, err := storyMarked(ctx, tx, taskGID, marker)
		if err != nil {
			return err
		}
		if marked { // made elsewhere (another computer), or before the record was kept
			return record()
		}
		if err := record(); err != nil {
			return err
		}
		if len(changes) > 0 {
			if err := updateTaskLocal(ctx, tx, taskGID, changes, now); err != nil {
				return err
			}
		}
		added = true
		return addCommentLocal(ctx, tx, gid, taskGID, text, htmlText, author, now)
	})
	if err != nil || !added {
		return "", false, err
	}
	return gid, true, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.kind, o.task_gid, COALESCE(t.name, sec.name, pr.name, CASE WHEN o.kind = 'mark_notification' THEN json_extract(o.payload, '$.name') END, ''), o.payload, o.base,
		o.base_modified_at, o.state, o.conflict, o.last_error, o.attempts, o.created_at, o.sent_at, o.last_sent_at, o.verify
		FROM outbox o LEFT JOIN tasks t ON t.gid = o.task_gid LEFT JOIN sections sec ON sec.gid = o.task_gid
		LEFT JOIN projects pr ON pr.gid = o.task_gid `+where+` ORDER BY o.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ops := []Op{}
	for rows.Next() {
		var op Op
		var payload, base, conflict string
		if err := rows.Scan(&op.ID, &op.Kind, &op.TaskGID, &op.TaskName, &payload, &base, &op.BaseModifiedAt,
			&op.State, &conflict, &op.LastError, &op.Attempts, &op.CreatedAt, &op.SentAt, &op.LastSentAt, &op.Verify); err != nil {
			return nil, err
		}
		if err := unmarshalOp(&op, payload, base, conflict); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// OutboxCounts returns the number of entries per state. Entries being sent count as pending
// (they have not reached Asana yet).
func (s *Store) OutboxCounts(ctx context.Context) (pending, conflict, failed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(state IN ('pending', 'sending')), 0), COALESCE(SUM(state = 'conflict'), 0), COALESCE(SUM(state = 'failed'), 0)
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
	_, err = s.db.ExecContext(ctx, "UPDATE outbox SET state = 'conflict', conflict = ? WHERE id = ?", string(b), id)
	return err
}

func (s *Store) MarkOpFailed(ctx context.Context, id int64, cause error) error {
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'failed', last_error = ? WHERE id = ?", cause.Error(), id)
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
		if err := ensureNotSending(op); err != nil {
			return err
		}
		if op.Kind == OpUpdateBrief {
			if err := keepLocalBrief(ctx, tx, op); err != nil {
				return err
			}
		}
		maps.Copy(op.Base, op.Conflict)
		op.Conflict, op.State, op.LastError = map[string]any{}, OpPending, ""
		if op.Kind == OpUpdateBrief { // shown: Asana's brief with this change over it
			cur, err := readBrief(ctx, tx, op.TaskGID)
			if err != nil {
				return err
			}
			base := cachedBrief{state: briefStr(op.Base, "state"), title: briefStr(op.Base, "title"),
				text: briefStr(op.Base, "text"), html: briefStr(op.Base, "html_text")}
			if cur.state != BriefUnavailable {
				if err := writeBrief(ctx, tx, op.TaskGID, briefWith(base, op.Payload)); err != nil {
					return err
				}
			}
		}
		return saveOp(ctx, tx, op)
	})
}

// RetryOp puts a failed entry back in the queue.
// An entry being sent is refused with ErrOpSending.
func (s *Store) RetryOp(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ensureNotSending(op); err != nil {
			return err
		}
		// The user asks to send it again as it is: no check in Asana first.
		// A fresh start: no check in Asana first, and the attempts count from zero again.
		_, err = tx.ExecContext(ctx, "UPDATE outbox SET state = 'pending', last_error = '', verify = 0, attempts = 0 WHERE id = ?", id)
		return err
	})
}

// DiscardOp drops a local change. The task keeps its local values until the next sync
// (or FetchDetails) brings the server values back; offline-created tasks/comments are removed.
func (s *Store) DiscardOp(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ensureNotSending(op); err != nil {
			return err
		}
		return discardOpTx(ctx, tx, op)
	})
}

// discardOpTx drops entry op and undoes its local change (see undoers).
func discardOpTx(ctx context.Context, tx *sql.Tx, op *Op) error {
	if undo, ok := undoers[op.Kind]; ok { // an entry of a newer version: only dropped
		if err := undo(ctx, tx, op); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
	return err
}

// discardLocalTask removes a task that was never sent, its local subtasks, and every
// change queued for them. counted is whether the parent's num_subtasks includes it
// (CreateTaskLocal counts subtasks; a local duplicate does not).
func discardLocalTask(ctx context.Context, tx *sql.Tx, gid string, counted bool) error {
	if err := undoSetParentsTo(ctx, tx, gid); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT gid FROM tasks WHERE parent_gid = ? AND gid LIKE 'local:%'", gid)
	if err != nil {
		return err
	}
	var children []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		children = append(children, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range children {
		if err := discardLocalTask(ctx, tx, c, true); err != nil {
			return err
		}
	}
	var parent string
	_ = tx.QueryRowContext(ctx, "SELECT parent_gid FROM tasks WHERE gid = ?", gid).Scan(&parent)
	for _, q := range []string{"DELETE FROM outbox WHERE task_gid = ?", "DELETE FROM tasks WHERE gid = ?"} {
		if _, err := tx.ExecContext(ctx, q, gid); err != nil {
			return err
		}
	}
	if parent != "" && counted {
		_, err := tx.ExecContext(ctx, "UPDATE tasks SET num_subtasks = max(0, num_subtasks - 1) WHERE gid = ?", parent)
		return err
	}
	return nil
}

// ReplaceLocalTaskGID swaps a temporary gid for the real one after the task was created in Asana.
func (s *Store) ReplaceLocalTaskGID(ctx context.Context, local, real string) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return replaceLocalTaskGID(ctx, tx, local, real) })
}

func replaceLocalTaskGID(ctx context.Context, tx *sql.Tx, local, real string) error {
	// Children reference tasks(gid); check the constraints at commit, after all rows moved.
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return err
	}
	// Every table that refers to a task gid must be listed here (see schema.go).
	for _, q := range []string{
		"DELETE FROM tasks WHERE gid = ?2", // in case a sync already pulled it
		"UPDATE tasks SET gid = ?2 WHERE gid = ?1",
		"UPDATE tasks SET parent_gid = ?2 WHERE parent_gid = ?1",
		"UPDATE task_memberships SET task_gid = ?2 WHERE task_gid = ?1",
		"UPDATE task_tags SET task_gid = ?2 WHERE task_gid = ?1",
		"UPDATE task_followers SET task_gid = ?2 WHERE task_gid = ?1",
		"DELETE FROM task_custom_fields WHERE task_gid = ?2",
		"UPDATE task_custom_fields SET task_gid = ?2 WHERE task_gid = ?1",
		"UPDATE OR REPLACE task_dependencies SET task_gid = ?2 WHERE task_gid = ?1",
		"UPDATE OR REPLACE task_dependencies SET dependency_gid = ?2 WHERE dependency_gid = ?1",
		"UPDATE task_details SET task_gid = ?2 WHERE task_gid = ?1",
		"UPDATE stories SET task_gid = ?2 WHERE task_gid = ?1",
		"UPDATE time_entries SET task_gid = ?2 WHERE task_gid = ?1",
		"UPDATE attachments SET parent_gid = ?2 WHERE parent_gid = ?1",
		"UPDATE outbox SET task_gid = ?2 WHERE task_gid = ?1",
	} {
		if _, err := tx.ExecContext(ctx, q, local, real); err != nil {
			return err
		}
	}
	// Remember the mapping so callers holding the local gid can find the task.
	return recordResult(ctx, tx, resultPrefixTask, local, real)
}

// RealGID returns the Asana gid for a task created offline, or gid itself if it is not
// a local gid or has not been sent yet.
func (s *Store) RealGID(ctx context.Context, gid string) (string, error) {
	if !IsLocalGID(gid) {
		return gid, nil
	}
	v, err := s.SyncState(ctx, resultPrefixTask+gid)
	if err != nil || v == "" {
		return gid, err
	}
	return v, nil
}

// ReplaceLocalStory swaps an offline comment for the one Asana created.
func (s *Store) ReplaceLocalStory(ctx context.Context, localGID, taskGID string, st model.Story) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return replaceLocalStory(ctx, tx, localGID, taskGID, st) })
}

func replaceLocalStory(ctx context.Context, tx *sql.Tx, localGID, taskGID string, st model.Story) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM stories WHERE gid = ? OR gid = ?", localGID, st.GID); err != nil {
		return err
	}
	var byGID, byName string
	if st.CreatedBy != nil {
		byGID, byName = st.CreatedBy.GID, st.CreatedBy.Name
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO stories
		(gid, task_gid, type, resource_subtype, text, created_by_gid, created_by_name, created_at, html_text, reactions)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.GID, taskGID, st.Type, st.ResourceSubtype, st.Text, byGID, byName, formatTime(st.CreatedAt), st.HTMLText,
		reactionsJSON(st.ReactionSummary)); err != nil {
		return err
	}
	if err := recordResult(ctx, tx, resultPrefixStory, localGID, st.GID); err != nil {
		return err
	}
	return reapplyStoryOps(ctx, tx, taskGID) // edits or a delete queued while it was being sent
}

func recordResult(ctx context.Context, tx *sql.Tx, prefix, local, real string) error {
	if local == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO sync_state(key, value) VALUES (?, ?)", prefix+local, real)
	return err
}

// RecordedResults returns the gids already recorded as the result of some local entry
// (for kind, one of readmodel.Result*), so they are not taken for another one.
func (s *Store) RecordedResults(ctx context.Context, kind readmodel.ResultKind) (map[string]bool, error) {
	prefix, err := resultPrefix(kind)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT value FROM sync_state WHERE substr(key, 1, ?) = ?", len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// --- helpers ---

// Task types (resource_subtype) and approval statuses that can be set.
var (
	TaskSubtypes     = task.Subtypes
	ApprovalStatuses = task.ApprovalStatuses
)

// TaskFieldValues extracts update_task fields from an API task (for conflict checks;
// model's).
var TaskFieldValues = model.TaskFieldValues

func currentFields(ctx context.Context, tx *sql.Tx, gid string) (map[string]any, string, error) {
	var completed, liked bool
	var name, notes, htmlNotes, dueOn, startOn, assignee, modifiedAt, subtype, approval, customType, customStatus string
	err := tx.QueryRowContext(ctx, `SELECT completed, name, notes, html_notes, due_on, start_on, assignee_gid, modified_at, liked,
		resource_subtype, approval_status, custom_type_gid, custom_type_status_gid FROM tasks WHERE gid = ?`, gid).
		Scan(&completed, &name, &notes, &htmlNotes, &dueOn, &startOn, &assignee, &modifiedAt, &liked, &subtype, &approval, &customType, &customStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("task %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return nil, "", err
	}
	return map[string]any{"completed": completed, "name": name, "notes": notes, "html_notes": htmlNotes, "due_on": dueOn, "start_on": startOn, "assignee": assignee, "liked": liked,
		"resource_subtype": subtype, "approval_status": approval,
		"custom_type": customType, "custom_type_status_option": customStatus}, modifiedAt, nil
}

// applyFields writes update_task fields to the tasks table.
func applyFields(ctx context.Context, tx *sql.Tx, gid string, fields map[string]any, now time.Time) error {
	// "notes" clears the rich text, so apply "html_notes" after it.
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if (a == "html_notes") != (b == "html_notes") {
			if a == "html_notes" {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})
	for _, k := range keys {
		v := fields[k]
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
		case "notes":
			// the rich text no longer matches; show the plain notes until the next fetch
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET notes = ?, html_notes = '' WHERE gid = ?", v, gid)
		case "html_notes":
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET html_notes = ? WHERE gid = ?", v, gid)
		case "liked":
			like, _ := v.(bool)
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET num_likes = max(0, num_likes + CASE WHEN ? = liked THEN 0 WHEN ? THEN 1 ELSE -1 END),
				liked = ? WHERE gid = ?`, like, like, like, gid)
		case "name", "due_on", "start_on":
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET "+k+" = ? WHERE gid = ?", v, gid)
		case "resource_subtype":
			// A task of another kind has no custom type (applied after custom_type: sorted).
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET resource_subtype = ?1,
				custom_type_gid = CASE WHEN ?1 = 'custom' THEN custom_type_gid ELSE '' END,
				custom_type_status_gid = CASE WHEN ?1 = 'custom' THEN custom_type_status_gid ELSE '' END WHERE gid = ?2`, v, gid)
		case "custom_type":
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET custom_type_gid = ?1,
				custom_type_status_gid = CASE WHEN custom_type_gid = ?1 THEN custom_type_status_gid ELSE '' END WHERE gid = ?2`, v, gid)
		case "custom_type_status_option":
			// A status that completes the task (completion_state) is done in Asana, as an
			// approval is (applied after custom_type: sorted). An unknown state leaves it.
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET custom_type_status_gid = ?1,
				completed = COALESCE((SELECT CASE lower(completion_state) WHEN 'complete' THEN 1 WHEN 'incomplete' THEN 0 END
					FROM custom_type_status_options WHERE gid = ?1), completed) WHERE gid = ?2`, v, gid)
		case "approval_status":
			// Asana completes an approval task once it is decided, and reopens it when pending.
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET approval_status = ?1,
				completed = CASE WHEN ?1 = 'pending' THEN 0 WHEN ?1 <> '' THEN 1 ELSE completed END WHERE gid = ?2`, v, gid)
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
	rows, err := tx.QueryContext(ctx, `SELECT kind, payload, base FROM outbox
		WHERE task_gid = ? AND kind IN (?, ?) AND state <> 'failed' ORDER BY id`, gid, OpUpdateTask, OpMoveTask)
	if err != nil {
		return err
	}
	type entry struct {
		kind          Kind
		payload, base map[string]any
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var p, b string
		if err := rows.Scan(&e.kind, &p, &b); err != nil {
			rows.Close()
			return err
		}
		if err := termsafe.Unmarshal([]byte(p), &e.payload); err != nil {
			rows.Close()
			return err
		}
		if e.kind == OpMoveTask && b != "" {
			if err := termsafe.Unmarshal([]byte(b), &e.base); err != nil {
				rows.Close()
				return err
			}
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
			err = reapplyMove(ctx, tx, gid, e.payload, e.base)
		} else {
			err = applyFields(ctx, tx, gid, e.payload, time.Time{})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func findOp(ctx context.Context, tx *sql.Tx, kind Kind, gid string) (*Op, error) {
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

// findUnsentCreate returns the pending create of a local task if it was never sent, so
// edits can be folded into it. Once sent, its payload must stay what was sent: verifying
// it looks for exactly that in Asana.
func findUnsentCreate(ctx context.Context, tx *sql.Tx, gid string) (*Op, error) {
	op, err := findOp(ctx, tx, OpCreateTask, gid)
	if err != nil || op == nil {
		return nil, err
	}
	if op.Verify || op.SentAt != "" || op.Attempts > 0 {
		return nil, nil
	}
	return op, nil
}

// CreateQueued reports whether a task with a local gid still has its create (or duplicate)
// in the outbox, in any state.
func (s *Store) CreateQueued(ctx context.Context, gid string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE task_gid = ? AND kind IN (?, ?)",
		gid, OpCreateTask, OpDuplicateTask).Scan(&n)
	return n > 0, err
}

// ensureCreateQueued checks that a task with a local gid still has its create (or
// duplicate) in the outbox, in any state. Changes queued for it keep the local gid, which
// replaceLocalTaskGID rewrites once Asana has created the task; until then sync.Push skips
// them (they wait, pending), and discarding the create discards them too.
func ensureCreateQueued(ctx context.Context, tx *sql.Tx, gid string) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE task_gid = ? AND kind IN (?, ?)",
		gid, OpCreateTask, OpDuplicateTask).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("task %s: create not found in the outbox: %w", gid, ErrNotFound)
	}
	return nil
}

// findUpdateOp returns the pending or conflicted update_task entry of a task.
func findUpdateOp(ctx context.Context, tx *sql.Tx, gid string) (*Op, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM outbox WHERE kind = ? AND task_gid = ? AND state IN ('pending', 'conflict')
		ORDER BY id DESC LIMIT 1`, OpUpdateTask, gid).Scan(&id)
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
	err := tx.QueryRowContext(ctx, `SELECT id, kind, task_gid, payload, base, base_modified_at, state, conflict, last_error, attempts, created_at,
		sent_at, last_sent_at, verify FROM outbox WHERE id = ?`, id).Scan(&op.ID, &op.Kind, &op.TaskGID, &payload, &base, &op.BaseModifiedAt,
		&op.State, &conflict, &op.LastError, &op.Attempts, &op.CreatedAt, &op.SentAt, &op.LastSentAt, &op.Verify)
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
			if err := termsafe.Unmarshal([]byte(f.src), f.dst); err != nil {
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

func sameValue(a, b any) bool { return outbox.SameValue(a, b) }

// newLocalGID makes a new local gid from a random id (gid.NewLocal).
func newLocalGID() string { return gid.NewLocal(randomID()) }

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
