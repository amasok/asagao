package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// Setting a custom task type and its status locally (#59): checked against the statuses
// of the cache, and what a task can be given.

// customStatusType returns the type of a status ("" if it is not known).
func customStatusType(ctx context.Context, q dbQuerier, status string) (string, error) {
	var typ string
	err := q.QueryRowContext(ctx, "SELECT custom_type_gid FROM custom_type_status_options WHERE gid = ?", status).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return typ, err
}

// defaultCustomStatus is the status a task gets when it is given the type typ without
// one: the first status that can be chosen and does not complete the task, else the first
// that can be chosen ("" if the statuses of the type are not known).
func defaultCustomStatus(ctx context.Context, q dbQuerier, typ string) (string, error) {
	var gid string
	err := q.QueryRowContext(ctx, `SELECT gid FROM custom_type_status_options WHERE custom_type_gid = ? AND enabled
		ORDER BY lower(completion_state) = 'complete', position, gid LIMIT 1`, typ).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return gid, err
}

// customTypeChanges checks the custom type fields of a local edit of a task (cur: its
// fields now) and returns the changes to apply: a new type without a status gets the
// type's first status (defaultCustomStatus), so that Asana and the cache agree on it.
func customTypeChanges(ctx context.Context, tx *sql.Tx, cur, changes map[string]any) (map[string]any, error) {
	str := func(k string) string { v, _ := cur[k].(string); return v }
	var lookupErr error
	c, err := task.CheckCustomType(str("resource_subtype"), str("custom_type"), str("custom_type_status_option"), changes,
		func(status string) string {
			typ, err := customStatusType(ctx, tx, status)
			if err != nil {
				lookupErr = err
			}
			return typ
		})
	if lookupErr != nil {
		return nil, lookupErr
	}
	if err != nil {
		return nil, err
	}
	if _, ok := changes["custom_type"]; !ok || c.Status != "" {
		return changes, nil
	}
	status, err := defaultCustomStatus(ctx, tx, c.Type)
	if err != nil || status == "" {
		return changes, err
	}
	changes = maps.Clone(changes)
	changes["custom_type_status_option"] = status
	return changes, nil
}

// TaskKind returns a task's type (resource_subtype), its custom type and status, and the
// custom types it can be given: those its projects offer, and its own type if they do not.
func (s *Store) TaskKind(ctx context.Context, gid string) (readmodel.TaskKind, error) {
	var k readmodel.TaskKind
	var typ, typeName, status string
	err := s.db.QueryRowContext(ctx, `SELECT t.resource_subtype, t.custom_type_gid, COALESCE(ct.name, ''), t.custom_type_status_gid
		FROM tasks t LEFT JOIN custom_types ct ON ct.gid = t.custom_type_gid WHERE t.gid = ?`, gid).Scan(&k.Subtype, &typ, &typeName, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return k, fmt.Errorf("task %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return k, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT project_gid FROM task_memberships WHERE task_gid = ? ORDER BY rowid", gid)
	if err != nil {
		return k, err
	}
	var projects []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return k, err
		}
		projects = append(projects, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return k, err
	}
	if k.Choices, err = s.CustomTypes(ctx, projects); err != nil {
		return k, err
	}
	if k.Subtype != task.SubtypeCustom || typ == "" {
		return k, nil
	}
	k.Type = &readmodel.CustomTypeRef{GID: typ, Name: typeName}
	if !slices.ContainsFunc(k.Choices, func(c readmodel.CustomTypeView) bool { return c.GID == typ }) {
		own := readmodel.CustomTypeView{GID: typ, Name: typeName}
		if own.Statuses, err = s.customStatuses(ctx, typ); err != nil {
			return k, err
		}
		k.Choices = append(k.Choices, own)
	}
	if status != "" { // also one that can no longer be chosen
		v := readmodel.CustomTypeStatusView{GID: status}
		var state string
		err := s.db.QueryRowContext(ctx, "SELECT name, color, completion_state FROM custom_type_status_options WHERE gid = ?", status).
			Scan(&v.Name, &v.Color, &state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return k, err
		}
		v.Complete = model.CustomTypeStatusOption{CompletionState: state}.IsComplete()
		k.Status = &v
	}
	return k, nil
}
