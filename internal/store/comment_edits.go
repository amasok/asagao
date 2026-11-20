package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

func init() {
	registerUndo(OpAddComment, func(ctx context.Context, tx *sql.Tx, op *Op) error {
		gid, _ := op.Payload["story_gid"].(string)
		if gid == "" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM stories WHERE gid = ?", gid); err != nil {
			return err
		}
		return dropChangesOf(ctx, tx, "story", gid, OpUpdateStory, OpDeleteStory)
	})
	registerUndo(OpUpdateStory, undoStoryOp)
	registerUndo(OpDeleteStory, undoStoryOp)
	registerUndo(OpUploadAttachment, func(ctx context.Context, tx *sql.Tx, op *Op) error {
		gid, _ := op.Payload["attachment"].(string)
		if _, err := tx.ExecContext(ctx, "DELETE FROM attachments WHERE gid = ?", gid); err != nil {
			return err
		}
		return dropChangesOf(ctx, tx, "attachment", gid, OpDeleteAttachment)
	})
	registerUndo(OpDeleteAttachment, undoAttachmentDelete)
}

// Editing and deleting the user's comments, and deleting attachments (#34). As every other
// write, the change is applied to the local tables at once and queued in the outbox:
//
//   - update_story / delete_story / delete_attachment hold the comment's or attachment's
//     gid in payload ("story" / "attachment") and the task in task_gid;
//   - a comment or attachment not sent yet (a "local:" gid) is changed in its own create
//     while that has never been sent: an edit rewrites the add_comment, a delete discards
//     the add_comment or the upload_attachment. Once the create is being sent (or checked)
//     its payload must stay what was sent, so the change is queued with the local gid and
//     waits (sync.Push) until Asana has created it (the result recorded under "story:" /
//     "att:");
//   - refreshing a task's comments or attachments from Asana (ReplaceStories,
//     ReplaceAttachments) applies the queued edits and deletes again, so a list read
//     before they were sent does not bring the old text or the deleted item back.

const (
	OpUpdateStory      = outbox.KindUpdateStory
	OpDeleteStory      = outbox.KindDeleteStory
	OpDeleteAttachment = outbox.KindDeleteAttachment
)

// ErrNotOwnComment is returned when a comment that the logged-in user did not write is
// edited or deleted (Asana lets users change only their own comments).
var ErrNotOwnComment = errors.New("自分のコメントだけを編集・削除できます")

// storyRow is a stored comment, as kept in a delete_story's base to put it back.
type storyRow struct {
	GID, TaskGID, Type, Subtype, Text, HTMLText, ByGID, ByName, CreatedAt, Reactions string
}

func (r storyRow) base() map[string]any {
	return map[string]any{"task": r.TaskGID, "type": r.Type, "resource_subtype": r.Subtype, "text": r.Text,
		"html_text": r.HTMLText, "created_by_gid": r.ByGID, "created_by_name": r.ByName, "created_at": r.CreatedAt,
		"reactions": r.Reactions}
}

func getStoryRow(ctx context.Context, tx *sql.Tx, gid string) (*storyRow, error) {
	var r storyRow
	err := tx.QueryRowContext(ctx, `SELECT gid, task_gid, type, resource_subtype, text, html_text, created_by_gid,
		created_by_name, created_at, reactions FROM stories WHERE gid = ?`, gid).
		Scan(&r.GID, &r.TaskGID, &r.Type, &r.Subtype, &r.Text, &r.HTMLText, &r.ByGID, &r.ByName, &r.CreatedAt, &r.Reactions)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("comment %s: %w", gid, ErrNotFound)
	}
	return &r, err
}

// ownComment returns the comment gid if the logged-in user (me) wrote it.
func ownComment(ctx context.Context, tx *sql.Tx, gid, me string) (*storyRow, error) {
	r, err := getStoryRow(ctx, tx, gid)
	if err != nil {
		return nil, err
	}
	if r.Type != "comment" {
		return nil, fmt.Errorf("%s はコメントではありません", gid)
	}
	if me == "" || r.ByGID != me {
		return nil, ErrNotOwnComment
	}
	return r, nil
}

// unsentCreate returns the create (kind) of a comment or attachment with a local gid, as
// long as it was never sent: until then it can be rewritten or dropped.
func unsentCreate(ctx context.Context, tx *sql.Tx, kind Kind, key, gid string) (*Op, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM outbox WHERE kind = ? AND json_extract(payload, '$.'||?) = ?
		AND state IN ('pending', 'failed') AND verify = 0 AND sent_at = '' AND attempts = 0`, kind, key, gid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getOpTx(ctx, tx, id)
}

// EditCommentLocal changes the text of one of the user's comments (htmlText, Asana rich
// text, is sent instead of text if set) and queues it.
func (s *Store) EditCommentLocal(ctx context.Context, storyGID, text, htmlText string, now time.Time) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("コメントが空です")
	}
	me, err := s.MeGID(ctx)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		row, err := ownComment(ctx, tx, storyGID, me)
		if err != nil {
			return err
		}
		if row.Text == text && row.HTMLText == htmlText {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE stories SET text = ?, html_text = ? WHERE gid = ?", text, htmlText, storyGID); err != nil {
			return err
		}
		if IsLocalGID(storyGID) {
			create, err := unsentCreate(ctx, tx, OpAddComment, "story_gid", storyGID)
			if err != nil {
				return err
			}
			if create != nil { // not sent yet: send the new text instead
				create.Payload["text"] = text
				delete(create.Payload, "html_text")
				if htmlText != "" {
					create.Payload["html_text"] = htmlText
				}
				return saveOp(ctx, tx, create)
			}
			if err := ensureStoryCreateQueued(ctx, tx, OpAddComment, "story_gid", storyGID); err != nil {
				return err
			}
		}
		// Join the edit not sent yet, so its base stays the text before the first edit.
		op, err := findStoryOp(ctx, tx, OpUpdateStory, "story", storyGID, "'pending', 'failed'")
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpUpdateStory, TaskGID: row.TaskGID, State: OpPending, CreatedAt: formatTime(now),
				Base: map[string]any{"text": row.Text, "html_text": row.HTMLText}}
		}
		if op.Base["text"] == text && op.Base["html_text"] == htmlText { // back to what Asana has
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		op.Payload = map[string]any{"story": storyGID, "text": text, "html_text": htmlText}
		op.State, op.LastError = OpPending, ""
		return saveOp(ctx, tx, op)
	})
}

// DeleteCommentLocal deletes one of the user's comments and queues the delete.
func (s *Store) DeleteCommentLocal(ctx context.Context, storyGID string, now time.Time) error {
	me, err := s.MeGID(ctx)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		row, err := ownComment(ctx, tx, storyGID, me)
		if err != nil {
			return err
		}
		if IsLocalGID(storyGID) {
			create, err := unsentCreate(ctx, tx, OpAddComment, "story_gid", storyGID)
			if err != nil {
				return err
			}
			if create != nil { // never sent: forget it (with the edits queued for it)
				return discardOpTx(ctx, tx, create)
			}
			if err := ensureStoryCreateQueued(ctx, tx, OpAddComment, "story_gid", storyGID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM stories WHERE gid = ?", storyGID); err != nil {
			return err
		}
		payload := map[string]any{"story": storyGID, "text": row.Text}
		return saveOp(ctx, tx, &Op{Kind: OpDeleteStory, TaskGID: row.TaskGID, Payload: payload, Base: row.base(),
			State: OpPending, CreatedAt: formatTime(now)})
	})
}

// DeleteAttachmentLocal deletes an attachment of a task and queues the delete.
//
// The local file of the attachment (downloaded, or the copy of a file not uploaded yet) is
// removed with it: a file the user deleted must not stay on the computer. Discarding the
// delete puts the attachment back without it (it is downloaded again when opened). Only
// the copy that an upload being sent (or checked) may still need stays, until the delete
// is sent (payload "path", see sync.Push).
func (s *Store) DeleteAttachmentLocal(ctx context.Context, gid string, now time.Time) error {
	var remove string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row, err := getAttachmentRow(ctx, tx, gid)
		if err != nil {
			return err
		}
		path, _ := row["local_path"].(string)
		payload := map[string]any{"attachment": gid, "name": row["name"]}
		if IsLocalGID(gid) {
			create, err := unsentCreate(ctx, tx, OpUploadAttachment, "attachment", gid)
			if err != nil {
				return err
			}
			if create != nil { // never uploaded: forget it, and its copy
				remove, _ = create.Payload["path"].(string)
				return discardOpTx(ctx, tx, create)
			}
			if err := ensureStoryCreateQueued(ctx, tx, OpUploadAttachment, "attachment", gid); err != nil {
				return err
			}
			if path != "" { // the upload may be sent again from it
				payload["path"] = path
				path = ""
			}
		}
		if path != "" {
			remove, row["local_path"] = path, ""
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM attachments WHERE gid = ?", gid); err != nil {
			return err
		}
		return saveOp(ctx, tx, &Op{Kind: OpDeleteAttachment, TaskGID: row["parent_gid"].(string), Payload: payload, Base: row,
			State: OpPending, CreatedAt: formatTime(now)})
	})
	if err == nil && remove != "" {
		if rerr := s.RemoveAttachmentFile(remove); rerr != nil {
			return fmt.Errorf("添付は削除しましたが、ローカルのファイル %s を消せませんでした: %w", remove, rerr)
		}
	}
	return err
}

// attachmentColumns are the attachments columns kept in a delete_attachment's base.
var attachmentColumns = []string{"parent_gid", "name", "resource_subtype", "host", "view_url", "permanent_url", "size", "created_at", "local_path"}

func getAttachmentRow(ctx context.Context, tx *sql.Tx, gid string) (map[string]any, error) {
	var parent, name, subtype, host, viewURL, permURL, createdAt, path string
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT parent_gid, name, resource_subtype, host, view_url, permanent_url, size,
		created_at, local_path FROM attachments WHERE gid = ?`, gid).
		Scan(&parent, &name, &subtype, &host, &viewURL, &permURL, &size, &createdAt, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("attachment %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"parent_gid": parent, "name": name, "resource_subtype": subtype, "host": host,
		"view_url": viewURL, "permanent_url": permURL, "size": size, "created_at": createdAt, "local_path": path}, nil
}

// ensureStoryCreateQueued checks that a comment or attachment with a local gid still has
// its create in the outbox (in any state): a change queued for it waits for that.
func ensureStoryCreateQueued(ctx context.Context, tx *sql.Tx, kind Kind, key, gid string) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE kind = ? AND json_extract(payload, '$.'||?) = ?",
		kind, key, gid).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", gid, ErrNotFound)
	}
	return nil
}

// findStoryOp returns the newest entry of kind whose payload key is gid, in one of states
// (a SQL list).
func findStoryOp(ctx context.Context, tx *sql.Tx, kind Kind, key, gid, states string) (*Op, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM outbox WHERE kind = ? AND json_extract(payload, '$.'||?) = ?
		AND state IN (`+states+`) ORDER BY id DESC LIMIT 1`, kind, key, gid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getOpTx(ctx, tx, id)
}

// resolvedSQL is the gid in payload key of an outbox row "o", as Asana knows it: a local
// gid is replaced by what Asana created for it (recorded under prefix), if recorded.
func resolvedSQL(key, prefix string) string {
	return `COALESCE((SELECT value FROM sync_state WHERE key = '` + prefix + `' || json_extract(o.payload, '$.` + key + `')),
		json_extract(o.payload, '$.` + key + `'))`
}

// reapplyStoryOps applies the queued edits and deletes of a task's comments to its stories
// again (after they were replaced by what Asana returned). A failed one is not: Asana's
// version is shown, as for a failed task update.
func reapplyStoryOps(ctx context.Context, tx *sql.Tx, taskGID string) error {
	gid := resolvedSQL("story", resultPrefixStory)
	if _, err := tx.ExecContext(ctx, `UPDATE stories SET
		text = (SELECT json_extract(o.payload, '$.text') FROM outbox o WHERE o.kind = ?1 AND o.state <> 'failed' AND `+gid+` = stories.gid ORDER BY o.id DESC LIMIT 1),
		html_text = (SELECT json_extract(o.payload, '$.html_text') FROM outbox o WHERE o.kind = ?1 AND o.state <> 'failed' AND `+gid+` = stories.gid ORDER BY o.id DESC LIMIT 1)
		WHERE task_gid = ?2 AND gid IN (SELECT `+gid+` FROM outbox o WHERE o.kind = ?1 AND o.state <> 'failed')`, OpUpdateStory, taskGID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM stories WHERE task_gid = ? AND gid IN (SELECT `+gid+` FROM outbox o WHERE o.kind = ? AND o.state <> 'failed')`,
		taskGID, OpDeleteStory)
	return err
}

// reapplyAttachmentDeletes removes again a task's attachments whose delete is queued.
func reapplyAttachmentDeletes(ctx context.Context, tx *sql.Tx, taskGID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM attachments WHERE parent_gid = ? AND gid IN
		(SELECT `+resolvedSQL("attachment", resultPrefixAttachment)+` FROM outbox o WHERE o.kind = ? AND o.state <> 'failed')`, taskGID, OpDeleteAttachment)
	return err
}

// resolveResult returns what Asana created for a local gid (recorded under prefix), or
// gid itself.
func resolveResult(ctx context.Context, tx *sql.Tx, prefix, gid string) (string, error) {
	if !IsLocalGID(gid) {
		return gid, nil
	}
	var real string
	err := tx.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", prefix+gid).Scan(&real)
	if errors.Is(err, sql.ErrNoRows) {
		return gid, nil
	}
	return real, err
}

// undoStoryOp puts back what a discarded update_story or delete_story changed locally.
func undoStoryOp(ctx context.Context, tx *sql.Tx, op *Op) error {
	gid, _ := op.Payload["story"].(string)
	gid, err := resolveResult(ctx, tx, resultPrefixStory, gid)
	if err != nil {
		return err
	}
	str := func(k string) string { v, _ := op.Base[k].(string); return v }
	if op.Kind == OpUpdateStory {
		if _, ok := op.Base["text"]; !ok {
			return nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE stories SET text = ?, html_text = ? WHERE gid = ?", str("text"), str("html_text"), gid)
		return err
	}
	if str("task") == "" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO stories
		(gid, task_gid, type, resource_subtype, text, html_text, created_by_gid, created_by_name, created_at, reactions)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM tasks WHERE gid = ?)`,
		gid, str("task"), str("type"), str("resource_subtype"), str("text"), str("html_text"), str("created_by_gid"),
		str("created_by_name"), str("created_at"), str("reactions"), str("task"))
	return err
}

// undoAttachmentDelete puts back an attachment whose delete is discarded.
func undoAttachmentDelete(ctx context.Context, tx *sql.Tx, op *Op) error {
	gid, _ := op.Payload["attachment"].(string)
	gid, err := resolveResult(ctx, tx, resultPrefixAttachment, gid)
	if err != nil {
		return err
	}
	parent, _ := op.Base["parent_gid"].(string)
	if parent == "" {
		return nil
	}
	args := []any{gid}
	for _, c := range attachmentColumns {
		v := op.Base[c]
		if c == "size" {
			f, _ := v.(float64) // JSON numbers
			v = int64(f)
		} else if _, ok := v.(string); !ok {
			v = ""
		}
		args = append(args, v)
	}
	args = append(args, parent)
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO attachments(gid, `+strings.Join(attachmentColumns, ", ")+`)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM tasks WHERE gid = ?)`, args...)
	return err
}

// dropChangesOf removes the queued changes (kinds) to a comment or attachment whose create
// is discarded: there is nothing left in Asana for them to change.
func dropChangesOf(ctx context.Context, tx *sql.Tx, key, gid string, kinds ...Kind) error {
	if gid == "" {
		return nil
	}
	for _, k := range kinds {
		if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE kind = ? AND json_extract(payload, '$.'||?) = ? AND state <> 'sending'",
			k, key, gid); err != nil {
			return err
		}
	}
	return nil
}

// LocalResult returns, for a comment, attachment or time entry with a local gid, what Asana created for
// it (kind readmodel.ResultStory, ResultAttachment or ResultTimeEntry) and whether its create is still in the
// outbox (waiting: a change to it waits for that). real is "" if none is recorded.
func (s *Store) LocalResult(ctx context.Context, kind readmodel.ResultKind, gid string) (real string, waiting bool, err error) {
	prefix, err := resultPrefix(kind)
	if err != nil {
		return "", false, err
	}
	if !IsLocalGID(gid) {
		return gid, false, nil
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM sync_state WHERE key = ?), '')", prefix+gid).Scan(&real); err != nil {
		return "", false, err
	}
	if real != "" {
		return real, false, nil
	}
	opKind, key := OpAddComment, "story_gid"
	switch kind {
	case readmodel.ResultAttachment:
		opKind, key = OpUploadAttachment, "attachment"
	case readmodel.ResultTimeEntry:
		opKind, key = OpAddTimeEntry, "entry"
	}
	var n int
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE kind = ? AND json_extract(payload, '$.'||?) = ?", opKind, key, gid).Scan(&n)
	return "", n > 0, err
}

// ApplyStory records the comment Asana returned after an edit (its text as Asana keeps it).
func (s *Store) ApplyStory(ctx context.Context, gid, text, htmlText string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE stories SET text = ?, html_text = ? WHERE gid = ?", text, htmlText, gid); err != nil {
			return err
		}
		var task string
		err := tx.QueryRowContext(ctx, "SELECT task_gid FROM stories WHERE gid = ?", gid).Scan(&task)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return reapplyStoryOps(ctx, tx, task) // a later edit queued meanwhile
	})
}

// reactionsJSON is the reactions column for a story's reaction_summary ("" for none).
func reactionsJSON(rs []model.Reaction) string {
	if len(rs) == 0 {
		return ""
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return ""
	}
	return string(b)
}
