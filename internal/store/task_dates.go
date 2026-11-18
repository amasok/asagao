package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// A task's start date (start_on) needs a due date and does not come after it (Asana's
// rules, task.CheckDates), and a workspace whose plan lacks start dates (a recent 402,
// readmodel.PlanStartOn) is not given one. Both are checked before an edit is queued, so
// an entry Asana would refuse is never made.

// checkTaskDates checks the dates of task gid after changes (cur: its values now). It
// does nothing when the edit changes neither date.
func checkTaskDates(ctx context.Context, tx *sql.Tx, gid string, cur, changes map[string]any, now time.Time) error {
	newStart, startSet := changes["start_on"].(string)
	newDue, dueSet := changes["due_on"].(string)
	if !startSet && !dueSet {
		return nil
	}
	start, _ := cur["start_on"].(string)
	due, _ := cur["due_on"].(string)
	var err error
	if startSet {
		start = newStart
	}
	if dueSet {
		due = newDue
	} else if due == "" {
		if due, err = dueAtDay(ctx, tx, gid); err != nil {
			return err
		}
	}
	if err = task.CheckDates(start, due, dueSet && newDue == ""); err != nil {
		return err
	}
	if startSet && newStart != "" {
		ws, err := taskWorkspace(ctx, tx, gid)
		if err != nil {
			return err
		}
		return startOnOffered(ctx, tx, ws, now)
	}
	return nil
}

// dueAtDay is the day (local time) of a task's due time, for a task due at a time whose
// due_on is empty ("" if it has none).
func dueAtDay(ctx context.Context, q dbQuerier, gid string) (string, error) {
	var at string
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(due_at, '') FROM tasks WHERE gid = ?", gid).Scan(&at); err != nil {
		return "", err
	}
	if at == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return "", nil
	}
	return t.Local().Format(time.DateOnly), nil
}

// startOnOffered returns a *readmodel.PlanUnavailableError while a recent 402 hides start
// dates in workspace ws.
func startOnOffered(ctx context.Context, q dbQuerier, ws string, now time.Time) error {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", readmodel.PlanKey(readmodel.PlanStartOn, ws)).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if readmodel.PlanOffFor(v, now) {
		return &readmodel.PlanUnavailableError{Feature: readmodel.PlanStartOn}
	}
	return nil
}

// taskWorkspace is TaskWorkspace within q.
func taskWorkspace(ctx context.Context, q dbQuerier, taskGID string) (string, error) {
	var ws string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(t.workspace_gid, ''),
		(SELECT p.workspace_gid FROM task_memberships m JOIN projects p ON p.gid = m.project_gid
			WHERE m.task_gid = t.gid ORDER BY m.rowid LIMIT 1), '')
		FROM tasks t WHERE t.gid = ?`, taskGID).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("task %s: %w", taskGID, ErrNotFound)
	}
	return ws, err
}
