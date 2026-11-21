package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/timetrack"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

func init() {
	registerUndo(OpAddTimeEntry, undoTimeEntryOp)
	registerUndo(OpUpdateTimeEntry, undoTimeEntryOp)
	registerUndo(OpDeleteTimeEntry, undoTimeEntryOp)
}

// Time tracking entries of tasks (#6). As every other write, a change is applied to
// time_entries at once and queued in the outbox, the way comments are (comment_edits.go):
//
//   - add_time_entry holds the new entry's local gid in payload "entry"; an edit or delete
//     of it rewrites or drops the add while it has never been sent, and otherwise waits
//     (sync.Push) until Asana has created it (the result recorded under "time:");
//   - refreshing a task's entries from Asana (ReplaceTimeEntries) applies the queued edits
//     and deletes again, so a list read before they were sent does not undo them.

const (
	OpAddTimeEntry    = outbox.KindAddTimeEntry
	OpUpdateTimeEntry = outbox.KindUpdateTimeEntry
	OpDeleteTimeEntry = outbox.KindDeleteTimeEntry
)

// timeEntryColumns are the columns of time_entries after gid, as kept in a
// delete_time_entry's base.
var timeEntryColumns = []string{"task_gid", "duration_minutes", "entered_on", "assignee_gid", "assignee_name",
	"created_by_gid", "created_by_name", "project_name", "categories", "added_at"}

type timeEntryRow struct {
	GID, TaskGID                         string
	Minutes                              int
	EnteredOn, AssigneeGID, AssigneeName string
	ByGID, ByName, Project, Categories   string
	AddedAt                              string
}

func (r timeEntryRow) base() map[string]any {
	return map[string]any{"task_gid": r.TaskGID, "duration_minutes": r.Minutes, "entered_on": r.EnteredOn,
		"assignee_gid": r.AssigneeGID, "assignee_name": r.AssigneeName, "created_by_gid": r.ByGID,
		"created_by_name": r.ByName, "project_name": r.Project, "categories": r.Categories, "added_at": r.AddedAt}
}

func getTimeEntryRow(ctx context.Context, tx *sql.Tx, gid string) (*timeEntryRow, error) {
	var r timeEntryRow
	err := tx.QueryRowContext(ctx, `SELECT gid, task_gid, duration_minutes, entered_on, assignee_gid, assignee_name,
		created_by_gid, created_by_name, project_name, categories, added_at FROM time_entries WHERE gid = ?`, gid).
		Scan(&r.GID, &r.TaskGID, &r.Minutes, &r.EnteredOn, &r.AssigneeGID, &r.AssigneeName, &r.ByGID, &r.ByName,
			&r.Project, &r.Categories, &r.AddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("time entry %s: %w", gid, ErrNotFound)
	}
	return &r, err
}

// validTimeEntry checks the values an entry is recorded with.
func validTimeEntry(minutes int, enteredOn string) error {
	if minutes < 1 || minutes > timetrack.MaxMinutes {
		return fmt.Errorf("時間は 1 分から %s までです", timetrack.Format(timetrack.MaxMinutes))
	}
	if _, err := time.Parse(time.DateOnly, enteredOn); err != nil {
		return fmt.Errorf("日付 %q は YYYY-MM-DD で書いてください", enteredOn)
	}
	return nil
}

// AddTimeEntryLocal records minutes on taskGID for the day enteredOn ("YYYY-MM-DD") as
// me, with a local gid, and queues it. It returns the local gid.
func (s *Store) AddTimeEntryLocal(ctx context.Context, taskGID string, minutes int, enteredOn string, me UserRef, now time.Time) (string, error) {
	if err := validTimeEntry(minutes, enteredOn); err != nil {
		return "", err
	}
	gid := newLocalGID()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", taskGID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("task %s: %w", taskGID, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO time_entries
			(gid, task_gid, duration_minutes, entered_on, assignee_gid, assignee_name, created_by_gid, created_by_name)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, gid, taskGID, minutes, enteredOn, me.GID, me.Name, me.GID, me.Name); err != nil {
			return err
		}
		return savePayload(ctx, tx, &Op{Kind: OpAddTimeEntry, TaskGID: taskGID, State: OpPending, CreatedAt: formatTime(now)},
			outbox.TimeEntry{Entry: gid, DurationMinutes: minutes, EnteredOn: enteredOn})
	})
	return gid, err
}

// UpdateTimeEntryLocal changes the minutes and the day of an entry and queues it. An entry
// not sent yet is sent with the new values instead.
func (s *Store) UpdateTimeEntryLocal(ctx context.Context, gid string, minutes int, enteredOn string, now time.Time) error {
	if err := validTimeEntry(minutes, enteredOn); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		row, err := getTimeEntryRow(ctx, tx, gid)
		if err != nil {
			return err
		}
		if row.Minutes == minutes && row.EnteredOn == enteredOn {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE time_entries SET duration_minutes = ?, entered_on = ? WHERE gid = ?",
			minutes, enteredOn, gid); err != nil {
			return err
		}
		if IsLocalGID(gid) {
			create, err := unsentCreate(ctx, tx, OpAddTimeEntry, gid)
			if err != nil {
				return err
			}
			if create != nil { // not sent yet: send the new values instead
				if err := outbox.EditPayload(create, func(p *outbox.TimeEntry) { p.DurationMinutes, p.EnteredOn = minutes, enteredOn }); err != nil {
					return err
				}
				return saveOp(ctx, tx, create)
			}
			if err := ensureStoryCreateQueued(ctx, tx, OpAddTimeEntry, gid); err != nil {
				return err
			}
		}
		// Join the edit not sent yet, so its base stays the values before the first edit.
		op, err := findStoryOp(ctx, tx, OpUpdateTimeEntry, gid, "'pending', 'failed'")
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpUpdateTimeEntry, TaskGID: row.TaskGID, State: OpPending, CreatedAt: formatTime(now),
				Base: map[string]any{"duration_minutes": row.Minutes, "entered_on": row.EnteredOn}}
		}
		if intValue(op.Base["duration_minutes"]) == minutes && op.Base["entered_on"] == enteredOn { // back to what Asana has
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		op.State, op.LastError = OpPending, ""
		return savePayload(ctx, tx, op, outbox.TimeEntry{Entry: gid, DurationMinutes: minutes, EnteredOn: enteredOn})
	})
}

// DeleteTimeEntryLocal deletes an entry and queues the delete (an entry never sent is just
// dropped).
func (s *Store) DeleteTimeEntryLocal(ctx context.Context, gid string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		row, err := getTimeEntryRow(ctx, tx, gid)
		if err != nil {
			return err
		}
		if IsLocalGID(gid) {
			create, err := unsentCreate(ctx, tx, OpAddTimeEntry, gid)
			if err != nil {
				return err
			}
			if create != nil { // never sent: forget it (with the edits queued for it)
				return discardOpTx(ctx, tx, create)
			}
			if err := ensureStoryCreateQueued(ctx, tx, OpAddTimeEntry, gid); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM time_entries WHERE gid = ?", gid); err != nil {
			return err
		}
		return savePayload(ctx, tx, &Op{Kind: OpDeleteTimeEntry, TaskGID: row.TaskGID, Base: row.base(), State: OpPending, CreatedAt: formatTime(now)},
			outbox.TimeEntry{Entry: gid, DurationMinutes: row.Minutes, EnteredOn: row.EnteredOn})
	})
}

// TaskWorkspace returns the workspace of a task: its own, else the one of its first
// project ("" if neither is known).
func (s *Store) TaskWorkspace(ctx context.Context, taskGID string) (string, error) {
	return taskWorkspace(ctx, s.db, taskGID)
}

// WorkspaceGIDs returns the gids of the workspaces in the cache: the ones listed, and the
// ones its projects and tasks are in.
func (s *Store) WorkspaceGIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT gid FROM workspaces UNION SELECT workspace_gid FROM projects WHERE workspace_gid <> ''
		UNION SELECT workspace_gid FROM tasks WHERE workspace_gid <> '' ORDER BY 1`)
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

// TimeEntryTask returns the task an entry belongs to (ErrNotFound if it is not in the cache).
func (s *Store) TimeEntryTask(ctx context.Context, gid string) (string, error) {
	var task string
	err := s.db.QueryRowContext(ctx, "SELECT task_gid FROM time_entries WHERE gid = ?", gid).Scan(&task)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("time entry %s: %w", gid, ErrNotFound)
	}
	return task, err
}

// TimeEntries returns a task's time tracking entries in the cache, newest day first.
func (s *Store) TimeEntries(ctx context.Context, taskGID string) (readmodel.TimeEntries, error) {
	out := readmodel.TimeEntries{TaskGID: taskGID, Entries: []readmodel.TimeEntryView{}}
	changed, err := resolvedRefs(ctx, s.db, resultPrefixTimeEntry, "kind IN (?, ?)", OpUpdateTimeEntry, OpDeleteTimeEntry)
	if err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT gid, duration_minutes, entered_on, assignee_gid, assignee_name,
		created_by_gid, created_by_name, project_name, categories, gid LIKE 'local:%'
		FROM time_entries WHERE task_gid = ? ORDER BY entered_on DESC, gid DESC`, taskGID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e readmodel.TimeEntryView
		var aGID, aName, bGID, bName, cats string
		if err := rows.Scan(&e.GID, &e.DurationMinutes, &e.EnteredOn, &aGID, &aName, &bGID, &bName, &e.Project, &cats, &e.Pending); err != nil {
			return out, err
		}
		e.Pending = e.Pending || changed[e.GID]
		e.TaskGID, e.Duration = taskGID, timetrack.Format(e.DurationMinutes)
		if aGID != "" || aName != "" {
			e.Assignee = &UserRef{GID: aGID, Name: aName}
		}
		if bGID != "" || bName != "" {
			e.CreatedBy = &UserRef{GID: bGID, Name: bName}
		}
		if cats != "" {
			_ = termsafe.Unmarshal([]byte(cats), &e.Categories) // a broken value only loses the names
		}
		out.Entries = append(out.Entries, e)
		out.TotalMinutes += e.DurationMinutes
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Total = timetrack.Format(out.TotalMinutes)
	out.FetchedAt, err = s.SyncState(ctx, keyTimeEntries+taskGID)
	return out, err
}

// insertTimeEntry writes an entry as Asana returned it (replacing a row with its gid).
func insertTimeEntry(ctx context.Context, tx *sql.Tx, taskGID string, e model.TimeEntry, addedAt string) error {
	ref := func(r *model.Ref) (string, string) {
		if r == nil {
			return "", ""
		}
		return r.GID, r.Name
	}
	aGID, aName := ref(e.Assignee)
	bGID, bName := ref(e.CreatedBy)
	_, project := ref(e.AttributableTo)
	cats := ""
	if len(e.Categories) > 0 {
		names := make([]string, len(e.Categories))
		for i, c := range e.Categories {
			names[i] = c.Name
		}
		b, err := json.Marshal(names)
		if err != nil {
			return err
		}
		cats = string(b)
	}
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO time_entries
		(gid, task_gid, duration_minutes, entered_on, assignee_gid, assignee_name, created_by_gid, created_by_name,
		project_name, categories, added_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GID, taskGID, e.DurationMinutes, e.EnteredOn, aGID, aName, bGID, bName, project, cats, addedAt)
	return err
}

// ReplaceTimeEntries replaces a task's entries by what Asana listed (read at fetchedAt) and
// records the fetch. Kept are the entries not sent yet (local gids) and the ones Asana
// created at or after fetchedAt (a list read before an add was recorded does not have it).
// The queued edits and deletes are applied again.
func (s *Store) ReplaceTimeEntries(ctx context.Context, taskGID string, entries []model.TimeEntry, fetchedAt time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", taskGID).Scan(&exists); err != nil || exists == 0 {
			return err // a task no longer in the cache: nothing to keep them for
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM time_entries WHERE task_gid = ? AND gid NOT LIKE 'local:%'
			AND NOT (added_at <> '' AND added_at >= ?)`, taskGID, formatTime(fetchedAt)); err != nil {
			return err
		}
		for _, e := range entries {
			if err := insertTimeEntry(ctx, tx, taskGID, e, ""); err != nil {
				return err
			}
		}
		if err := reapplyTimeEntryOps(ctx, tx, taskGID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO sync_state(key, value) VALUES(?, ?)",
			keyTimeEntries+taskGID, formatTime(fetchedAt))
		return err
	})
}

// CompleteTimeEntry records the entry Asana created for entry id (an add_time_entry of the
// local entry localGID): the local row becomes Asana's.
func (s *Store) CompleteTimeEntry(ctx context.Context, id int64, localGID, taskGID string, e model.TimeEntry) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM time_entries WHERE gid = ? OR gid = ?", localGID, e.GID); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", taskGID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			if err := insertTimeEntry(ctx, tx, taskGID, e, formatTime(e.CreatedAt)); err != nil {
				return err
			}
		}
		if err := recordResult(ctx, tx, resultPrefixTimeEntry, localGID, e.GID); err != nil {
			return err
		}
		return reapplyTimeEntryOps(ctx, tx, taskGID) // edits or a delete queued while it was being sent
	})
}

// ApplyTimeEntry records the minutes and day Asana returned after an edit of an entry.
func (s *Store) ApplyTimeEntry(ctx context.Context, e model.TimeEntry) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var task string
		err := tx.QueryRowContext(ctx, "SELECT task_gid FROM time_entries WHERE gid = ?", e.GID).Scan(&task)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE time_entries SET duration_minutes = ?, entered_on = ? WHERE gid = ?",
			e.DurationMinutes, e.EnteredOn, e.GID); err != nil {
			return err
		}
		return reapplyTimeEntryOps(ctx, tx, task) // a later edit queued meanwhile
	})
}

// reapplyTimeEntryOps applies the queued edits and deletes of a task's entries again
// (after they were replaced by what Asana returned). A failed one is not: Asana's values
// are shown, as for a failed task update.
func reapplyTimeEntryOps(ctx context.Context, tx *sql.Tx, taskGID string) error {
	// In the order they were made; the entry they name is the one Asana created for it, for a
	// local gid already sent (resolveResult). One whose payload does not read entirely
	// (held) is applied as far as it reads.
	ops, err := opsWhere(ctx, tx, nil, "kind IN (?, ?) AND state <> 'failed'", OpUpdateTimeEntry, OpDeleteTimeEntry)
	if err != nil {
		return err
	}
	for _, op := range ops {
		p, _ := outbox.BestEffortPayload[outbox.TimeEntry](op)
		if p.Entry == "" {
			continue
		}
		gid, err := resolveResult(ctx, tx, resultPrefixTimeEntry, p.Entry)
		if err != nil {
			return err
		}
		if op.Kind == OpDeleteTimeEntry {
			_, err = tx.ExecContext(ctx, "DELETE FROM time_entries WHERE task_gid = ? AND gid = ?", taskGID, gid)
		} else {
			err = updateReadFields(ctx, tx, "time_entries", op, map[string]any{"duration_minutes": p.DurationMinutes, "entered_on": p.EnteredOn}, taskGID, gid)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// undoTimeEntryOp puts back what a discarded add, edit or delete of an entry changed locally.
func undoTimeEntryOp(ctx context.Context, tx *sql.Tx, op *Op, p outbox.TimeEntry) error {
	local := p.Entry
	if op.Kind == OpAddTimeEntry {
		if local == "" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM time_entries WHERE gid = ?", local); err != nil {
			return err
		}
		return dropChangesOf(ctx, tx, local, OpUpdateTimeEntry, OpDeleteTimeEntry)
	}
	gid, err := resolveResult(ctx, tx, resultPrefixTimeEntry, local)
	if err != nil {
		return err
	}
	if op.Kind == OpUpdateTimeEntry {
		if _, ok := op.Base["duration_minutes"]; !ok {
			return nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE time_entries SET duration_minutes = ?, entered_on = ? WHERE gid = ?",
			intValue(op.Base["duration_minutes"]), op.Base["entered_on"], gid)
		return err
	}
	task, _ := op.Base["task_gid"].(string)
	if task == "" {
		return nil
	}
	args := []any{gid}
	for _, c := range timeEntryColumns {
		v := op.Base[c]
		if c == "duration_minutes" {
			v = intValue(v)
		} else if _, ok := v.(string); !ok {
			v = ""
		}
		args = append(args, v)
	}
	args = append(args, task)
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO time_entries(gid, task_gid, duration_minutes, entered_on,
		assignee_gid, assignee_name, created_by_gid, created_by_name, project_name, categories, added_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM tasks WHERE gid = ?)`, args...)
	return err
}

// intValue reads a whole number from an outbox payload or base (JSON numbers are float64).
func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
