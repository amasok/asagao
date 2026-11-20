package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

func init() {
	registerUndo(OpUpdateBrief, undoBriefOp)
}

// OpUpdateBrief creates, changes or deletes a project's brief (outbox.KindUpdateProjectBrief).
const OpUpdateBrief = outbox.KindUpdateProjectBrief

// BriefEdit is readmodel.BriefEdit.
type BriefEdit = readmodel.BriefEdit

// cachedBrief is a project's brief as the cache has it.
type cachedBrief struct {
	state, title, text, html string
}

func readBrief(ctx context.Context, tx *sql.Tx, projectGID string) (cachedBrief, error) {
	var b cachedBrief
	err := tx.QueryRowContext(ctx, `SELECT brief_state, brief_title, brief_text, brief_html FROM project_overviews
		WHERE project_gid = ?`, projectGID).Scan(&b.state, &b.title, &b.text, &b.html)
	if errors.Is(err, sql.ErrNoRows) {
		return cachedBrief{}, nil
	}
	return b, err
}

// writeBrief stores a brief in the cache (state BriefNone or "" clear it).
func writeBrief(ctx context.Context, tx *sql.Tx, projectGID string, b cachedBrief) error {
	if b.state != BriefAvailable {
		b.title, b.text, b.html = "", "", ""
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO project_overviews(project_gid) VALUES(?)", projectGID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE project_overviews SET brief_state = ?, brief_title = ?, brief_text = ?,
		brief_html = ? WHERE project_gid = ?`, b.state, b.title, b.text, b.html, projectGID)
	return err
}

// UpdateProjectBriefLocal applies a change to a project's brief in the cache and queues it
// for Asana. Consecutive changes of the same brief are one entry, whose base stays the
// brief as it was before the first (also while it is in conflict: the user resolves it
// once); a change back to that brief removes the entry, and so does deleting a brief that
// was only made here. A brief Asana does not give (BriefUnavailable) cannot be changed.
func (s *Store) UpdateProjectBriefLocal(ctx context.Context, projectGID string, e BriefEdit, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE gid = ?)", projectGID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("project %s: %w", projectGID, ErrNotFound)
		}
		cur, err := readBrief(ctx, tx, projectGID)
		if err != nil {
			return err
		}
		if cur.state == BriefUnavailable {
			return ErrBriefUnavailable
		}
		if e.Delete && cur.state != BriefAvailable {
			return fmt.Errorf("ブリーフ: %w", ErrNotFound)
		}
		op, err := findBriefOp(ctx, tx, projectGID)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpUpdateBrief, TaskGID: projectGID, Payload: map[string]any{},
				Base:  map[string]any{"state": cur.state, "title": cur.title, "text": cur.text, "html_text": cur.html},
				State: OpPending, CreatedAt: formatTime(now)}
		}
		next := cachedBrief{state: BriefNone}
		baseState := briefStr(op.Base, "state")
		switch {
		case e.Delete:
			op.Payload = map[string]any{"delete": true}
		case baseState != BriefAvailable: // a new brief: all of it
			op.Payload = map[string]any{"title": e.Title, "html_text": e.HTMLText, "text": e.Text}
		default: // only what differs from the base, so that a change of the other part in
			// Asana is kept (and is no conflict)
			op.Payload = map[string]any{}
			if e.Title != briefStr(op.Base, "title") {
				op.Payload["title"] = e.Title
			}
			if e.HTMLText != briefStr(op.Base, "html_text") {
				op.Payload["html_text"], op.Payload["text"] = e.HTMLText, e.Text
			}
		}
		if !e.Delete {
			next = cachedBrief{state: BriefAvailable, title: e.Title, text: e.Text, html: e.HTMLText}
		}
		if err := writeBrief(ctx, tx, projectGID, next); err != nil {
			return err
		}
		reverted := e.Delete && baseState != BriefAvailable || // made here and deleted again
			len(op.Payload) == 0 // changed back to the base
		if reverted { // nothing left to send: the brief as it was
			if err := undoBriefOp(ctx, tx, op, outbox.UpdateProjectBrief{}); err != nil {
				return err
			}
			if op.ID != 0 {
				_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
				return err
			}
			return nil
		}
		return saveOp(ctx, tx, op)
	})
}

func briefStr(m map[string]any, k string) string { v, _ := m[k].(string); return v }

// ErrBriefUnavailable: Asana does not give the project's brief (its plan, or the preview
// API refused it), so it cannot be changed here.
var ErrBriefUnavailable = errors.New("このプロジェクトのブリーフは Asana から取得できないため、変更できません")

// findBriefOp returns the brief entry of a project that a new change folds into: one
// waiting to be sent or in conflict (not one being sent, nor a failed one).
func findBriefOp(ctx context.Context, tx *sql.Tx, projectGID string) (*Op, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM outbox WHERE kind = ? AND task_gid = ? AND state IN ('pending', 'conflict')
		ORDER BY id DESC LIMIT 1`, OpUpdateBrief, projectGID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getOpTx(ctx, tx, id)
}

// reapplyBriefEdits keeps the unsent brief changes of a project after its brief was stored
// from Asana: the cache shows the brief as it will be once they are sent.
func reapplyBriefEdits(ctx context.Context, tx *sql.Tx, projectGID string) error {
	return reapplyBriefEditsBut(ctx, tx, projectGID, 0)
}

// reapplyBriefEditsBut is reapplyBriefEdits without entry except (one being discarded).
func reapplyBriefEditsBut(ctx context.Context, tx *sql.Tx, projectGID string, except int64) error {
	var payload string
	err := tx.QueryRowContext(ctx, `SELECT payload FROM outbox WHERE kind = ? AND task_gid = ? AND state <> 'failed'
		AND id <> ? ORDER BY id DESC LIMIT 1`, OpUpdateBrief, projectGID, except).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var p map[string]any
	if err := termsafe.Unmarshal([]byte(payload), &p); err != nil {
		return err
	}
	cur, err := readBrief(ctx, tx, projectGID)
	if err != nil {
		return err
	}
	return writeBrief(ctx, tx, projectGID, briefWith(cur, p))
}

// briefWith is brief cur with an entry's payload applied: deleted, or its title and body
// where the payload has them (a change of only one keeps the other as cur has it).
func briefWith(cur cachedBrief, p map[string]any) cachedBrief {
	if p["delete"] == true {
		return cachedBrief{state: BriefNone}
	}
	b := cur
	b.state = BriefAvailable
	if v, ok := p["title"].(string); ok {
		b.title = v
	}
	if v, ok := p["html_text"].(string); ok {
		b.html, b.text = v, briefStr(p, "text")
	}
	return b
}

// undoBriefOp puts back the brief as it was before a discarded entry (or, for one in
// conflict, as Asana has it), with the project's
// other brief changes still queued (a later one made while it was failed or being sent)
// applied over it.
func undoBriefOp(ctx context.Context, tx *sql.Tx, op *Op, _ outbox.UpdateProjectBrief) error {
	was := op.Base
	if op.State == OpConflict && briefStr(op.Conflict, "state") != "" {
		// Asana's brief, as the conflict found it: newer than the base, and what the next
		// change must be made from (found on the real Asana, #60)
		was = op.Conflict
	}
	if err := writeBrief(ctx, tx, op.TaskGID, cachedBrief{state: briefStr(was, "state"), title: briefStr(was, "title"),
		text: briefStr(was, "text"), html: briefStr(was, "html_text")}); err != nil {
		return err
	}
	return reapplyBriefEditsBut(ctx, tx, op.TaskGID, op.ID)
}

// CompleteBriefOp removes a sent brief entry and stores the brief Asana now has (nil: the
// project has none), unless another change of it is queued.
func (s *Store) CompleteBriefOp(ctx context.Context, id int64, projectGID string, brief *model.ProjectBrief) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id); err != nil {
			return err
		}
		b := cachedBrief{state: BriefNone}
		if brief != nil {
			b = cachedBrief{state: BriefAvailable, title: brief.Title, text: brief.Text, html: brief.HTMLText}
		}
		if err := writeBrief(ctx, tx, projectGID, b); err != nil {
			return err
		}
		return reapplyBriefEdits(ctx, tx, projectGID)
	})
}

// keepLocalBrief readies a brief change in conflict for "keep mine" (before its base
// becomes Asana's brief): a change of part of a brief that Asana deleted meanwhile makes
// the whole brief again, as it is shown here.
func keepLocalBrief(ctx context.Context, tx *sql.Tx, op *Op) error {
	if op.Payload["delete"] == true || briefStr(op.Conflict, "state") == BriefAvailable {
		return nil
	}
	cur, err := readBrief(ctx, tx, op.TaskGID)
	if err != nil {
		return err
	}
	if cur.state == BriefAvailable {
		op.Payload = map[string]any{"title": cur.title, "html_text": cur.html, "text": cur.text}
	}
	return nil
}
