package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

// Dependencies: a task waits for its dependencies; its dependents wait for it. Each link is
// one row of task_dependencies (task_gid waits for dependency_gid), so both directions
// read the same rows. A change is queued on the waiting task (add_dependency /
// remove_dependency), whichever side the user changed it from; doing the opposite of an
// unsent change cancels it.

const (
	OpAddDependency    = "add_dependency"    // payload: dependency, dependency_name
	OpRemoveDependency = "remove_dependency" // payload: dependency, dependency_name

	// MaxDependencies is Asana's limit of dependencies and dependents of one task, combined.
	MaxDependencies = 30
)

// DependencyView is a task linked to another by a dependency.
type DependencyView struct {
	GID             string `json:"gid"`
	Name            string `json:"name"`
	Completed       bool   `json:"completed"`
	ResourceSubtype string `json:"resource_subtype,omitempty"`
	// Pending: the link was added here and not sent yet.
	Pending bool `json:"pending,omitempty"`
}

// upsertTaskDependencies replaces a task's links (each direction only if the response had it).
func upsertTaskDependencies(ctx context.Context, tx *sql.Tx, t asana.Task) error {
	type side struct {
		refs []asana.Ref
		del  string
		pair func(other string) (task, dependency string)
	}
	for _, sd := range []side{
		{t.Dependencies, "DELETE FROM task_dependencies WHERE task_gid = ?", func(o string) (string, string) { return t.GID, o }},
		{t.Dependents, "DELETE FROM task_dependencies WHERE dependency_gid = ?", func(o string) (string, string) { return o, t.GID }},
	} {
		if sd.refs == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, sd.del, t.GID); err != nil {
			return err
		}
		for _, r := range sd.refs {
			task, dep := sd.pair(r.GID)
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO task_dependencies(task_gid, dependency_gid) VALUES(?, ?)", task, dep); err != nil {
				return err
			}
			if r.Name != "" {
				if err := upsertTaskRef(ctx, tx, asana.TaskRef{GID: r.GID, Name: r.Name}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func upsertTaskRef(ctx context.Context, tx *sql.Tx, r asana.TaskRef) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_refs(gid, name, completed, resource_subtype) VALUES(?, ?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET name = excluded.name, completed = excluded.completed, resource_subtype = excluded.resource_subtype`,
		r.GID, r.Name, r.Completed, r.ResourceSubtype)
	return err
}

// ReplaceDependencies stores a task's dependencies and dependents as listed by Asana (with
// the names of tasks that may not be cached), keeping unsent changes.
func (s *Store) ReplaceDependencies(ctx context.Context, taskGID string, deps, dependents []asana.TaskRef) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		t := asana.Task{GID: taskGID, Dependencies: []asana.Ref{}, Dependents: []asana.Ref{}}
		for _, list := range []struct {
			refs []asana.TaskRef
			dst  *[]asana.Ref
		}{{deps, &t.Dependencies}, {dependents, &t.Dependents}} {
			for _, r := range list.refs {
				*list.dst = append(*list.dst, asana.Ref{GID: r.GID})
				if err := upsertTaskRef(ctx, tx, r); err != nil {
					return err
				}
			}
		}
		if err := upsertTaskDependencies(ctx, tx, t); err != nil {
			return err
		}
		return reapplyDependencyOps(ctx, tx, taskGID)
	})
}

// applyDependency writes (or, with undo, reverts) a dependency change locally.
func applyDependency(ctx context.Context, tx *sql.Tx, op Op, undo bool) error {
	dep, _ := op.Payload["dependency"].(string)
	q := "INSERT OR IGNORE INTO task_dependencies(task_gid, dependency_gid) VALUES(?, ?)"
	if (op.Kind == OpRemoveDependency) != undo {
		q = "DELETE FROM task_dependencies WHERE task_gid = ? AND dependency_gid = ?"
	}
	_, err := tx.ExecContext(ctx, q, op.TaskGID, dep)
	return err
}

// reapplyDependencyOps keeps unsent dependency changes of a task (either side) after a pull.
func reapplyDependencyOps(ctx context.Context, tx *sql.Tx, gid string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM outbox WHERE kind IN (?, ?) AND state <> 'failed'
		AND (task_gid = ? OR json_extract(payload, '$.dependency') = ?) ORDER BY id`, OpAddDependency, OpRemoveDependency, gid, gid)
	if err != nil {
		return err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := applyDependency(ctx, tx, *op, false); err != nil {
			return err
		}
	}
	return nil
}

// taskName returns the name of a cached or referenced task ("" if unknown).
func taskName(ctx context.Context, q dbQuerier, gid string) string {
	var name string
	_ = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT name FROM tasks WHERE gid = ?1), (SELECT name FROM task_refs WHERE gid = ?1), '')`, gid).Scan(&name)
	return name
}

// SetDependencyLocal makes taskGID wait for dependencyGID (add) or stop waiting, and
// queues the change. Use it with the arguments swapped to change a task's dependents.
// Adding a link that exists (or removing one that does not) does nothing.
func (s *Store) SetDependencyLocal(ctx context.Context, taskGID, dependencyGID string, add bool, now time.Time) error {
	if taskGID == dependencyGID {
		return errors.New("タスクは自分自身を待てません")
	}
	if IsLocalGID(taskGID) || IsLocalGID(dependencyGID) {
		return errors.New("まだ送信されていないタスクの依存関係は、送信後に設定してください")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, gid := range []string{taskGID, dependencyGID} {
			if taskName(ctx, tx, gid) == "" {
				var n int
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", gid).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return fmt.Errorf("task %s: %w", gid, ErrNotFound)
				}
			}
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM task_dependencies WHERE task_gid = ? AND dependency_gid = ?",
			taskGID, dependencyGID).Scan(&exists); err != nil {
			return err
		}
		if (exists > 0) == add {
			return nil
		}
		if add {
			if err := checkNewDependency(ctx, tx, taskGID, dependencyGID); err != nil {
				return err
			}
		}
		kind, opposite := OpAddDependency, OpRemoveDependency
		if !add {
			kind, opposite = opposite, kind
		}
		op := Op{Kind: kind, TaskGID: taskGID, State: OpPending, CreatedAt: formatTime(now),
			Payload: map[string]any{"dependency": dependencyGID, "dependency_name": taskName(ctx, tx, dependencyGID)}}
		if err := applyDependency(ctx, tx, op, false); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE kind = ? AND task_gid = ? AND state = 'pending'
			AND json_extract(payload, '$.dependency') = ?`, opposite, taskGID, dependencyGID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("cancel unsent %s: %w", opposite, err)
		}
		if n > 0 {
			return nil // cancelled an unsent change
		}
		return saveOp(ctx, tx, &op)
	})
}

// checkNewDependency refuses a link that would exceed Asana's limit or make a cycle.
func checkNewDependency(ctx context.Context, tx *sql.Tx, taskGID, dependencyGID string) error {
	for _, gid := range []string{taskGID, dependencyGID} {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM task_dependencies WHERE task_gid = ?1 OR dependency_gid = ?1", gid).Scan(&n); err != nil {
			return err
		}
		if n >= MaxDependencies {
			return fmt.Errorf("「%s」の依存関係は上限（待つ・待たれる合わせて %d 件）に達しています", taskName(ctx, tx, gid), MaxDependencies)
		}
	}
	// A cycle: dependencyGID already waits (directly or not) for taskGID.
	var cycle int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE waits(gid) AS (
			SELECT dependency_gid FROM task_dependencies WHERE task_gid = ?1
			UNION SELECT d.dependency_gid FROM task_dependencies d JOIN waits w ON d.task_gid = w.gid)
		SELECT count(*) FROM waits WHERE gid = ?2`, dependencyGID, taskGID).Scan(&cycle); err != nil {
		return err
	}
	if cycle > 0 {
		return fmt.Errorf("「%s」はすでに「%s」を待っているため、循環します", taskName(ctx, tx, dependencyGID), taskName(ctx, tx, taskGID))
	}
	return nil
}

// taskDependencies returns the tasks gid waits for (dependents: the tasks waiting for gid).
func (s *Store) taskDependencies(ctx context.Context, gid string, dependents bool) ([]DependencyView, error) {
	self, other := "d.task_gid", "d.dependency_gid"
	waiting, blocker := "d.task_gid", "d.dependency_gid"
	if dependents {
		self, other = other, self
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+other+`,
			COALESCE(t.name, r.name, ''), COALESCE(t.completed, r.completed, 0), COALESCE(t.resource_subtype, r.resource_subtype, ''),
			EXISTS (SELECT 1 FROM outbox o WHERE o.kind = ? AND o.task_gid = `+waiting+`
				AND json_extract(o.payload, '$.dependency') = `+blocker+`)
		FROM task_dependencies d LEFT JOIN tasks t ON t.gid = `+other+` LEFT JOIN task_refs r ON r.gid = `+other+`
		WHERE `+self+` = ? ORDER BY 3, 2`, OpAddDependency, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DependencyView{}
	for rows.Next() {
		var v DependencyView
		if err := rows.Scan(&v.GID, &v.Name, &v.Completed, &v.ResourceSubtype, &v.Pending); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ResolveTaskGID checks that gid is a task in the store (or one referenced by a cached task).
func (s *Store) ResolveTaskGID(ctx context.Context, gid string) (string, error) {
	real, err := s.RealGID(ctx, gid)
	if err != nil {
		return "", err
	}
	if taskName(ctx, s.db, real) == "" {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", real).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			if IsLocalGID(gid) {
				// sent long ago: its mapping was pruned (see PruneResults)
				return "", fmt.Errorf("task %s: %w（オフラインで作ったタスクの一時的な gid は、送信から %d 日ほどで使えなくなります。Asana の gid を使ってください）",
					gid, ErrNotFound, int(ResultGrace.Hours()/24))
			}
			return "", fmt.Errorf("task %s: %w", gid, ErrNotFound)
		}
	}
	return real, nil
}
