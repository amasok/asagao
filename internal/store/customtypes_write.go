package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// Setting a custom task type and its status locally (#59): checked against the statuses
// of the cache, and what a task can be given.

// customStatusType returns the type of a status ("" if it is not known).
// enabled: it can still be chosen.
func customStatusType(ctx context.Context, q dbQuerier, status string) (typ string, enabled bool, err error) {
	err = q.QueryRowContext(ctx, "SELECT custom_type_gid, enabled FROM custom_type_status_options WHERE gid = ?", status).Scan(&typ, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return typ, enabled, err
}

// offersCustomType reports whether a project of task gid offers the custom type typ.
func offersCustomType(ctx context.Context, q dbQuerier, gid, typ string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM project_custom_types pct
		JOIN task_memberships tm ON tm.project_gid = pct.project_gid
		WHERE tm.task_gid = ? AND pct.custom_type_gid = ?`, gid, typ).Scan(&n)
	return n > 0, err
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

// customTypeChanges checks the custom type fields of a local edit of task gid (cur: its
// fields now) and returns the changes to apply: a new type without a status gets the
// type's first status (defaultCustomStatus), so that Asana and the cache agree on it. A
// new type must be one the task's projects offer, and a new status one that can still be
// chosen (the task's own status, here or in Asana, stays allowed).
func customTypeChanges(ctx context.Context, tx *sql.Tx, gid string, cur, changes map[string]any) (map[string]any, error) {
	str := func(k string) string { v, _ := cur[k].(string); return v }
	if typ, ok := changes["custom_type"].(string); ok && typ != str("custom_type") {
		offered, err := offersCustomType(ctx, tx, gid, typ)
		if err != nil {
			return nil, err
		}
		if !offered {
			return nil, fmt.Errorf("カスタムタイプ %q は、このタスクのプロジェクトにありません（同期すると最新の一覧を取ります）", typ)
		}
	}
	// The status the task has in Asana (an unsent edit's base) stays allowed too.
	own := map[string]bool{str("custom_type_status_option"): true}
	if op, err := findUpdateOp(ctx, tx, gid); err != nil {
		return nil, err
	} else if op != nil {
		if v, ok := op.Base["custom_type_status_option"].(string); ok {
			own[v] = true
		}
	}
	var lookupErr error
	c, err := task.CheckCustomType(str("resource_subtype"), str("custom_type"), str("custom_type_status_option"), changes,
		func(status string) string {
			typ, enabled, err := customStatusType(ctx, tx, status)
			if err != nil {
				lookupErr = err
			}
			if !enabled && !own[status] {
				return "" // can no longer be chosen: refused as not of the type
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

// customTypeKeys are the update_task fields of a custom type change.
var customTypeKeys = []string{"custom_type", "custom_type_status_option"}

// WithdrawCustomType takes the custom type change out of entry id (an update_task Asana
// refused, or answered without applying it: the API may not take custom types), so that
// the rest of the change is not held up by it. The custom type and status (and
// resource_subtype "custom") are recorded as a dropped change with reason (PendingDiscards),
// and the task's local type, custom type, status and completed go back to what they were.
// applied: Asana applied the rest already (it answered 200), so the entry is done;
// otherwise it is put back in the queue. An entry with nothing else is removed. rest is
// the entry to send again (nil when there is none).
func (s *Store) WithdrawCustomType(ctx context.Context, id int64, reason string, applied bool, now time.Time) (rest *Op, d Discard, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		withdrawn := Op{ID: op.ID, Kind: OpUpdateTask, TaskGID: op.TaskGID, Base: map[string]any{}}
		var w outbox.UpdateTask // what goes: the custom type and status, and the type "custom"
		if err := outbox.EditPayload(op, func(p *outbox.UpdateTask) {
			w.CustomType, p.CustomType = p.CustomType, nil
			w.CustomTypeStatusOption, p.CustomTypeStatusOption = p.CustomTypeStatusOption, nil
			if outbox.Str(p.ResourceSubtype) == task.SubtypeCustom {
				w.ResourceSubtype, p.ResourceSubtype = p.ResourceSubtype, nil
			}
		}); err != nil {
			return err
		}
		if err := outbox.SetPayload(&withdrawn, w); err != nil {
			return err
		}
		for k := range withdrawn.Payload {
			withdrawn.Base[k] = op.Base[k]
			delete(op.Conflict, k)
		}
		if len(withdrawn.Payload) == 0 { // nothing of a custom type: leave the entry as it is
			rest = op
			_, err := tx.ExecContext(ctx, "UPDATE outbox SET state = 'pending' WHERE id = ? AND state = 'sending'", id)
			return err
		}
		if d, err = s.recordDiscard(ctx, tx, &withdrawn, reason, now); err != nil {
			return err
		}
		// Back to the base: the kind of task, its custom type and status, and completed
		// unless the entry changes it itself (a status completes the task locally).
		restore := map[string]any{}
		for _, k := range append(slices.Clone(customTypeKeys), "resource_subtype") {
			if v, ok := op.Base[k]; ok {
				if _, still := op.Payload[k]; !still {
					restore[k] = v
				}
			}
		}
		_, sendsCompleted := op.Payload["completed"]
		if v, ok := op.Base["completed"]; ok && !sendsCompleted {
			restore["completed"] = v
		}
		if err := restoreCustomType(ctx, tx, op.TaskGID, restore); err != nil {
			return err
		}
		for k := range restore {
			if _, still := op.Payload[k]; !still {
				delete(op.Base, k)
			}
		}
		if applied || len(op.Payload) == 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id); err != nil {
				return err
			}
		} else {
			op.State = OpPending
			if err := saveOp(ctx, tx, op); err != nil {
				return err
			}
			rest = op
		}
		return reapplyUpdates(ctx, tx, op.TaskGID) // a later edit stays shown
	})
	return rest, d, err
}

// restoreCustomType sets a task's type fields to values of a base, as they were stored.
func restoreCustomType(ctx context.Context, tx *sql.Tx, gid string, base map[string]any) error {
	columns := map[string]string{"resource_subtype": "resource_subtype", "custom_type": "custom_type_gid",
		"custom_type_status_option": "custom_type_status_gid"}
	for k, col := range columns {
		if v, ok := base[k]; ok {
			s, _ := v.(string)
			if k == "resource_subtype" && s == "" {
				s = "default_task"
			}
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET "+col+" = ? WHERE gid = ?", s, gid); err != nil {
				return err
			}
		}
	}
	if v, ok := base["completed"].(bool); ok {
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET completed = ? WHERE gid = ?", v, gid); err != nil {
			return err
		}
	}
	return nil
}
