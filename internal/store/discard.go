package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, task_gid, task_name, summary, reason, discarded_at
		FROM discarded_changes WHERE notified = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Discard{}
	for rows.Next() {
		var d Discard
		if err := rows.Scan(&d.ID, &d.Kind, &d.TaskGID, &d.TaskName, &d.Summary, &d.Reason, &d.DiscardedAt); err != nil {
			return nil, err
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
	d := Discard{Kind: op.Kind, TaskGID: op.TaskGID, TaskName: name, Summary: DescribeChange(*op), Reason: reason, DiscardedAt: formatTime(now)}
	res, err := tx.ExecContext(ctx, `INSERT INTO discarded_changes(kind, task_gid, task_name, summary, reason, discarded_at)
		VALUES(?, ?, ?, ?, ?, ?)`, d.Kind, d.TaskGID, d.TaskName, d.Summary, d.Reason, d.DiscardedAt)
	if err != nil {
		return d, err
	}
	d.ID, err = res.LastInsertId()
	return d, err
}

// DescribeChange says in a few words what an outbox entry changes.
func DescribeChange(op Op) string {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	switch op.Kind {
	case OpCreateTask:
		if str("parent") != "" {
			return fmt.Sprintf("サブタスク「%s」の作成", str("name"))
		}
		return fmt.Sprintf("タスク「%s」の作成", str("name"))
	case OpDuplicateTask:
		return fmt.Sprintf("複製「%s」の作成", str("name"))
	case OpAddComment:
		return fmt.Sprintf("コメント「%s」", clip(str("text"), 40))
	case OpMoveTask:
		return fmt.Sprintf("セクション「%s」への移動", str("section_name"))
	case OpAddTag:
		return fmt.Sprintf("タグ「%s」の追加", str("tag_name"))
	case OpRemoveTag:
		return "タグの削除"
	case OpAddFollower:
		return fmt.Sprintf("フォロワー %s の追加", str("user_name"))
	case OpRemoveFollower:
		return fmt.Sprintf("フォロワー %s の削除", str("user_name"))
	case OpAddProject:
		return fmt.Sprintf("プロジェクト「%s」への追加", str("project_name"))
	case OpRemoveProject:
		return fmt.Sprintf("プロジェクト「%s」からの削除", str("project_name"))
	case OpUploadAttachment:
		return fmt.Sprintf("添付「%s」", str("name"))
	case OpCreateStatus:
		return fmt.Sprintf("ステータス更新「%s」", str("title"))
	case OpRenameSection:
		return fmt.Sprintf("セクション名を「%s」に変更", str("name"))
	case OpUpdateTask:
		var parts []string
		for _, k := range []string{"name", "completed", "due_on", "assignee", "notes", "liked"} {
			v, ok := op.Payload[k]
			if !ok {
				continue
			}
			switch k {
			case "name":
				parts = append(parts, fmt.Sprintf("名前を「%s」に変更", v))
			case "completed":
				parts = append(parts, map[bool]string{true: "完了にする", false: "未完了に戻す"}[v == true])
			case "due_on":
				if v == "" {
					parts = append(parts, "期日を削除")
				} else {
					parts = append(parts, fmt.Sprintf("期日を %s に変更", v))
				}
			case "assignee":
				parts = append(parts, "担当者の変更")
			case "notes":
				parts = append(parts, "説明の変更")
			case "liked":
				parts = append(parts, "いいね")
			}
		}
		if len(parts) == 0 && op.Payload["html_notes"] != nil {
			parts = append(parts, "説明の変更")
		}
		return strings.Join(parts, "、")
	}
	return op.Kind
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
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
