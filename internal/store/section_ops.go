package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// Section operations of a project (outbox.KindCreateSection, KindDeleteSection,
// KindInsertSection).
const (
	OpCreateSection = outbox.KindCreateSection
	OpDeleteSection = outbox.KindDeleteSection
	OpInsertSection = outbox.KindInsertSection
)

// ResultSection maps a section created offline to the section Asana created.
const ResultSection = readmodel.ResultSection

// ErrSectionNotEmpty is returned when deleting a section that still holds tasks (Asana
// deletes only empty sections).
var ErrSectionNotEmpty = errors.New("セクションにタスクが残っています。先にタスクを別のセクションへ移してください")

// ErrLastSection is returned when deleting the only section of a project (Asana keeps
// at least one).
var ErrLastSection = errors.New("プロジェクトの最後のセクションは削除できません")

// sectionOrder returns the gids of a project's sections in order.
func sectionOrder(ctx context.Context, tx *sql.Tx, projectGID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT gid FROM sections WHERE project_gid = ? ORDER BY position, rowid", projectGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// writeSectionOrder numbers a project's sections in the order of gids.
func writeSectionOrder(ctx context.Context, tx *sql.Tx, gids []string) error {
	for i, g := range gids {
		if _, err := tx.ExecContext(ctx, "UPDATE sections SET position = ? WHERE gid = ?", i, g); err != nil {
			return err
		}
	}
	return nil
}

// placeSection puts gid into order just before before or just after after (neither: last).
// An anchor that is not in order (deleted since) puts it last.
func placeSection(order []string, gid, before, after string) []string {
	order = slices.DeleteFunc(slices.Clone(order), func(g string) bool { return g == gid })
	at := len(order)
	if i := slices.Index(order, before); before != "" && i >= 0 {
		at = i
	} else if i := slices.Index(order, after); after != "" && i >= 0 {
		at = i + 1
	}
	return slices.Insert(order, at, gid)
}

// sectionAnchor checks that anchor ("" for none) is a section of projectGID other than self.
func sectionAnchor(ctx context.Context, tx *sql.Tx, projectGID, anchor, self string) error {
	if anchor == "" {
		return nil
	}
	if anchor == self {
		return errors.New("セクションを自分自身の前後には移せません")
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sections WHERE gid = ? AND project_gid = ?", anchor, projectGID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("section %s: %w", anchor, ErrNotFound)
	}
	return nil
}

// CreateSectionLocal creates a section in a project with a temporary gid and queues its
// creation. At most one of before / after (a section gid of the project) places it; with
// neither it goes last. Tasks can be moved to it right away: the moves are sent after it.
func (s *Store) CreateSectionLocal(ctx context.Context, projectGID, name, before, after string, now time.Time) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("セクション名が空です")
	}
	if before != "" && after != "" {
		return "", errors.New("before と after は片方だけ指定してください")
	}
	gid := newLocalGID()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE gid = ?", projectGID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("project %s: %w", projectGID, ErrNotFound)
		}
		for _, a := range []string{before, after} {
			if err := sectionAnchor(ctx, tx, projectGID, a, ""); err != nil {
				return err
			}
		}
		order, err := sectionOrder(ctx, tx, projectGID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sections(gid, project_gid, name, position) VALUES(?, ?, ?, ?)",
			gid, projectGID, name, len(order)); err != nil {
			return err
		}
		if err := writeSectionOrder(ctx, tx, placeSection(order, gid, before, after)); err != nil {
			return err
		}
		payload := map[string]any{"project": projectGID, "name": name}
		if before != "" {
			payload["before"] = before
		}
		if after != "" {
			payload["after"] = after
		}
		return saveOp(ctx, tx, &Op{Kind: OpCreateSection, TaskGID: gid, Payload: payload, State: OpPending, CreatedAt: formatTime(now)})
	})
	if err != nil {
		return "", err
	}
	return gid, nil
}

// MoveSectionLocal moves a section just before or just after another section of its
// project (exactly one of before / after) and queues the move.
func (s *Store) MoveSectionLocal(ctx context.Context, sectionGID, before, after string, now time.Time) error {
	if (before == "") == (after == "") {
		return errors.New("移動先 (before か after) を 1 つ指定してください")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var projectGID, name string
		err := tx.QueryRowContext(ctx, "SELECT project_gid, name FROM sections WHERE gid = ?", sectionGID).Scan(&projectGID, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("section %s: %w", sectionGID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		for _, a := range []string{before, after} {
			if err := sectionAnchor(ctx, tx, projectGID, a, sectionGID); err != nil {
				return err
			}
		}
		order, err := sectionOrder(ctx, tx, projectGID)
		if err != nil {
			return err
		}
		next := placeSection(order, sectionGID, before, after)
		if slices.Equal(order, next) {
			return nil // already there
		}
		if err := writeSectionOrder(ctx, tx, next); err != nil {
			return err
		}
		payload := map[string]any{"project": projectGID, "name": name}
		if before != "" {
			payload["before"] = before
		} else {
			payload["after"] = after
		}
		// Every move is kept, in order: a later move may be placed relative to this one.
		return saveOp(ctx, tx, &Op{Kind: OpInsertSection, TaskGID: sectionGID, Payload: payload,
			Base: map[string]any{"position": slices.Index(order, sectionGID)}, State: OpPending, CreatedAt: formatTime(now)})
	})
}

// DeleteSectionLocal deletes an empty section and queues the deletion. A section created
// offline whose creation was not sent is simply forgotten with its queued changes.
func (s *Store) DeleteSectionLocal(ctx context.Context, sectionGID string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var projectGID, name string
		var position int
		err := tx.QueryRowContext(ctx, "SELECT project_gid, name, position FROM sections WHERE gid = ?", sectionGID).Scan(&projectGID, &name, &position)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("section %s: %w", sectionGID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		var tasks, sections int
		// Tasks whose delete is queued do not count: their deletes are sent first (earlier
		// in the queue), and the section's waits for them (sync.Push).
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM task_memberships WHERE project_gid = ? AND section_gid = ? AND task_gid NOT IN "+deletedTasks,
			projectGID, sectionGID).Scan(&tasks); err != nil {
			return err
		}
		if tasks > 0 {
			return fmt.Errorf("セクション「%s」(%d 件): %w", name, tasks, ErrSectionNotEmpty)
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sections WHERE project_gid = ?", projectGID).Scan(&sections); err != nil {
			return err
		}
		if sections <= 1 {
			return ErrLastSection
		}
		if IsLocalGID(sectionGID) {
			create, err := findOpAnyState(ctx, tx, OpCreateSection, sectionGID)
			if err != nil {
				return err
			}
			if create != nil && (create.SentAt == "" || (create.State == OpFailed && !create.Verify)) {
				// Never reached Asana: forget it and everything queued for it.
				return forgetLocalSection(ctx, tx, sectionGID)
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM sections WHERE gid = ?", sectionGID); err != nil {
			return err
		}
		// Its unsent rename and moves stay queued (sent before the delete), so that
		// discarding the delete leaves them as they were.
		return saveOp(ctx, tx, &Op{Kind: OpDeleteSection, TaskGID: sectionGID, State: OpPending, CreatedAt: formatTime(now),
			Payload: map[string]any{"project": projectGID, "name": name, "position": position}})
	})
}

// forgetLocalSection removes a section that was never created in Asana and the changes
// queued for it. Moves of tasks to it and sections placed next to it are undone; a task
// created (or added to a project) into it stays, in no section (Asana puts it in the
// project's first one).
func forgetLocalSection(ctx context.Context, tx *sql.Tx, gid string) error {
	ops, err := opsReferringToSection(ctx, tx, gid)
	if err != nil {
		return err
	}
	for i := len(ops) - 1; i >= 0; i-- { // undo the latest first
		op := &ops[i]
		switch op.Kind {
		case OpCreateTask, OpAddProject:
			delete(op.Payload, "section")
			if err := saveOp(ctx, tx, op); err != nil {
				return err
			}
		case OpCreateSection: // another new section placed next to it: it goes last instead
			delete(op.Payload, "before")
			delete(op.Payload, "after")
			if err := saveOp(ctx, tx, op); err != nil {
				return err
			}
		default:
			if err := discardOpTx(ctx, tx, op); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{
		"DELETE FROM outbox WHERE task_gid = ?",
		"UPDATE task_memberships SET section_gid = '', position = NULL WHERE section_gid = ?",
		"DELETE FROM sections WHERE gid = ?",
	} {
		if _, err := tx.ExecContext(ctx, q, gid); err != nil {
			return err
		}
	}
	return nil
}

// opsReferringToSection returns the entries (other than the section's own) whose payload
// names section gid: moves of tasks to it, sections placed next to it.
func opsReferringToSection(ctx context.Context, tx *sql.Tx, gid string) ([]Op, error) {
	var conds []string
	var args []any
	for _, k := range outbox.SectionRefKeys {
		conds = append(conds, "json_extract(payload, '$."+k+"') = ?")
		args = append(args, gid)
	}
	args = append(args, gid)
	rows, err := tx.QueryContext(ctx, "SELECT id FROM outbox WHERE ("+strings.Join(conds, " OR ")+") AND task_gid <> ? ORDER BY id", args...)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var ops []Op
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		ops = append(ops, *op)
	}
	return ops, nil
}

// CompleteCreateSection records that Asana created the section of entry id with gid real:
// the local gid is replaced everywhere (the section, the tasks in it, the queued changes
// that name it) and the entry is removed.
func (s *Store) CompleteCreateSection(ctx context.Context, id int64, local, real string) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalSectionGID(ctx, tx, local, real) })
}

func replaceLocalSectionGID(ctx context.Context, tx *sql.Tx, local, real string) error {
	var position int
	_ = tx.QueryRowContext(ctx, "SELECT position FROM sections WHERE gid = ?", local).Scan(&position)
	quoted := func(g string) string { return `"` + g + `"` }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM sections WHERE gid = ? AND EXISTS (SELECT 1 FROM sections WHERE gid = ?)", []any{real, local}}, // a sync already pulled it
		{"UPDATE sections SET gid = ? WHERE gid = ?", []any{real, local}},
		{"UPDATE task_memberships SET section_gid = ? WHERE section_gid = ?", []any{real, local}},
		{"UPDATE outbox SET task_gid = ? WHERE task_gid = ?", []any{real, local}},
		// Local gids are random and only ever stored as whole JSON strings.
		{"UPDATE outbox SET payload = replace(payload, ?, ?), base = replace(base, ?, ?) WHERE instr(payload, ?) > 0 OR instr(base, ?) > 0",
			[]any{quoted(local), quoted(real), quoted(local), quoted(real), quoted(local), quoted(local)}},
	} {
		if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	return recordResult(ctx, tx, ResultSection, local, real)
}

// RealSectionGID returns the Asana gid of a section created offline once it was created
// (or gid itself).
func (s *Store) RealSectionGID(ctx context.Context, gid string) (string, error) {
	if !IsLocalGID(gid) {
		return gid, nil
	}
	v, err := s.SyncState(ctx, ResultSection+gid)
	if err != nil || v == "" {
		return gid, err
	}
	return v, nil
}

// SectionCreateQueued reports whether the creation of the local section gid is still in
// the outbox (pending, being verified or failed): what names it waits for it.
func (s *Store) SectionCreateQueued(ctx context.Context, gid string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE kind = ? AND task_gid = ?", OpCreateSection, gid).Scan(&n)
	return n > 0, err
}

// reapplySectionOps keeps the unsent section changes of a project after a sync replaced
// its sections: sections created offline are put back, deleted ones removed again, moves
// redone (in the order they were made), and renames kept.
func reapplySectionOps(ctx context.Context, tx *sql.Tx, projectGID string) error {
	// A failed create is kept too: the section (and the tasks moved to it) stay visible
	// until the user discards it.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM outbox WHERE kind IN (?1, ?2, ?3) AND (state <> 'failed' OR kind = ?1)
		AND json_extract(payload, '$.project') = ?4 ORDER BY id`, OpCreateSection, OpDeleteSection, OpInsertSection, projectGID)
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
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := applySectionOp(ctx, tx, *op); err != nil {
			return err
		}
	}
	return reapplySectionRenames(ctx, tx)
}

// applySectionOp redoes one unsent section change on the local sections.
func applySectionOp(ctx context.Context, tx *sql.Tx, op Op) error {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	project := str("project")
	switch op.Kind {
	case OpDeleteSection:
		_, err := tx.ExecContext(ctx, "DELETE FROM sections WHERE gid = ?", op.TaskGID)
		return err
	case OpCreateSection:
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sections(gid, project_gid, name, position) VALUES(?, ?, ?, 1 << 30)",
			op.TaskGID, project, str("name")); err != nil {
			return err
		}
	}
	order, err := sectionOrder(ctx, tx, project)
	if err != nil || !slices.Contains(order, op.TaskGID) {
		return err
	}
	return writeSectionOrder(ctx, tx, placeSection(order, op.TaskGID, str("before"), str("after")))
}

// undoSectionOp undoes the local change of a section entry that is discarded.
func undoSectionOp(ctx context.Context, tx *sql.Tx, op *Op) error {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	switch op.Kind {
	case OpCreateSection:
		// The section goes, with what was queued for it (it would fail without it).
		return forgetLocalSection(ctx, tx, op.TaskGID)
	case OpDeleteSection:
		pos, _ := op.Payload["position"].(float64)
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sections(gid, project_gid, name, position) VALUES(?, ?, ?, 1 << 30)",
			op.TaskGID, str("project"), str("name")); err != nil {
			return err
		}
		return putSectionAt(ctx, tx, str("project"), op.TaskGID, int(pos))
	case OpInsertSection:
		if pos, ok := op.Base["position"].(float64); ok {
			return putSectionAt(ctx, tx, str("project"), op.TaskGID, int(pos))
		}
	}
	return nil
}

// putSectionAt moves section gid to index at of its project's sections.
func putSectionAt(ctx context.Context, tx *sql.Tx, project, gid string, at int) error {
	order, err := sectionOrder(ctx, tx, project)
	if err != nil || !slices.Contains(order, gid) {
		return err
	}
	order = slices.DeleteFunc(order, func(g string) bool { return g == gid })
	at = max(0, min(at, len(order)))
	return writeSectionOrder(ctx, tx, slices.Insert(order, at, gid))
}

// findOpAnyState returns the latest entry of kind for gid, whatever its state (nil if none).
func findOpAnyState(ctx context.Context, tx *sql.Tx, kind, gid string) (*Op, error) {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM outbox WHERE kind = ? AND task_gid = ? ORDER BY id DESC LIMIT 1", kind, gid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getOpTx(ctx, tx, id)
}

// SetOpPayload sets one key of an entry's payload (e.g. what sending it found out first).
func (s *Store) SetOpPayload(ctx context.Context, id int64, key string, value any) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		op.Payload[key] = value
		return saveOp(ctx, tx, op)
	})
}
