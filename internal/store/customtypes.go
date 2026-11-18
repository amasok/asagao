package store

import (
	"context"
	"database/sql"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// Custom task types (#59). A task of one (resource_subtype "custom") keeps the gids of its
// type and status; their names are in custom_types and custom_type_status_options, from
// the task itself (a type no project of the cache offers is still named) and from the
// types each project offers (with the choices of statuses: their order, color, whether
// they can be chosen and whether they complete the task).

// upsertTaskCustomType stores the custom type and status of a task fetched with them. A
// custom type or status that came without its gid (Asana answers custom_type.name alone
// so) says nothing of which it is: the task keeps what the cache has.
func upsertTaskCustomType(ctx context.Context, tx *sql.Tx, t model.Task) error {
	if t.ResourceSubtype == task.SubtypeCustom && (noGID(t.CustomType) || noGID(t.CustomTypeStatusOption)) {
		return nil
	}
	var typ, status string
	if t.ResourceSubtype == task.SubtypeCustom && t.CustomType != nil && t.CustomType.GID != "" {
		typ = t.CustomType.GID
		if err := upsertCustomTypeName(ctx, tx, typ, t.CustomType.Name); err != nil {
			return err
		}
		if o := t.CustomTypeStatusOption; o != nil && o.GID != "" {
			status = o.GID
			if _, err := tx.ExecContext(ctx, `INSERT INTO custom_type_status_options(gid, custom_type_gid, name) VALUES(?, ?, ?)
				ON CONFLICT(gid) DO UPDATE SET custom_type_gid = excluded.custom_type_gid,
					name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE custom_type_status_options.name END`,
				status, typ, o.Name); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE tasks SET custom_type_gid = ?, custom_type_status_gid = ? WHERE gid = ?", typ, status, t.GID)
	return err
}

// noGID reports a reference that came without its gid.
func noGID(r *model.Ref) bool { return r != nil && r.GID == "" }

func upsertCustomTypeName(ctx context.Context, tx *sql.Tx, gid, name string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO custom_types(gid, name) VALUES(?, ?)
		ON CONFLICT(gid) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE custom_types.name END`, gid, name)
	return err
}

// ReplaceProjectCustomTypes stores the custom task types a project offers, with their
// statuses (nil: none, e.g. a plan without them).
func (s *Store) ReplaceProjectCustomTypes(ctx context.Context, projectGID string, types []model.CustomType) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM project_custom_types WHERE project_gid = ?", projectGID); err != nil {
			return err
		}
		for i, ct := range types {
			if ct.GID == "" {
				continue
			}
			if err := upsertCustomTypeName(ctx, tx, ct.GID, ct.Name); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO project_custom_types(project_gid, custom_type_gid, position) VALUES(?, ?, ?)",
				projectGID, ct.GID, i); err != nil {
				return err
			}
			// The list has every status of the type (disabled ones too): the others are gone.
			// A status a task still has keeps its row, so that its name is still shown, but
			// it can no longer be chosen.
			if _, err := tx.ExecContext(ctx, `DELETE FROM custom_type_status_options WHERE custom_type_gid = ?
				AND gid NOT IN (SELECT custom_type_status_gid FROM tasks WHERE custom_type_gid = ?)`, ct.GID, ct.GID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE custom_type_status_options SET enabled = 0 WHERE custom_type_gid = ?", ct.GID); err != nil {
				return err
			}
			for j, o := range ct.StatusOptions {
				if o.GID == "" {
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO custom_type_status_options(gid, custom_type_gid, position, name, color, enabled, completion_state)
					VALUES(?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(gid) DO UPDATE SET custom_type_gid = excluded.custom_type_gid, position = excluded.position,
						name = excluded.name, color = excluded.color, enabled = excluded.enabled, completion_state = excluded.completion_state`,
					o.GID, ct.GID, j, o.Name, o.Color, o.IsEnabled(), o.CompletionState); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// CustomTypes returns the custom task types the projects offer (each once, in the order of
// the projects and of each project's list), with their statuses that can be chosen.
func (s *Store) CustomTypes(ctx context.Context, projectGIDs []string) ([]readmodel.CustomTypeView, error) {
	var out []readmodel.CustomTypeView
	seen := map[string]bool{}
	for _, p := range projectGIDs {
		rows, err := s.db.QueryContext(ctx, `SELECT ct.gid, ct.name FROM project_custom_types pct
			JOIN custom_types ct ON ct.gid = pct.custom_type_gid WHERE pct.project_gid = ? ORDER BY pct.position`, p)
		if err != nil {
			return nil, err
		}
		var types []readmodel.CustomTypeView
		for rows.Next() {
			var v readmodel.CustomTypeView
			if err := rows.Scan(&v.GID, &v.Name); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[v.GID] {
				seen[v.GID] = true
				types = append(types, v)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for i := range types {
			if types[i].Statuses, err = s.customStatuses(ctx, types[i].GID); err != nil {
				return nil, err
			}
		}
		out = append(out, types...)
	}
	return out, nil
}

func (s *Store) customStatuses(ctx context.Context, typ string) ([]readmodel.CustomTypeStatusView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT gid, name, color, completion_state FROM custom_type_status_options
		WHERE custom_type_gid = ? AND enabled ORDER BY position, gid`, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []readmodel.CustomTypeStatusView
	for rows.Next() {
		var v readmodel.CustomTypeStatusView
		var state string
		if err := rows.Scan(&v.GID, &v.Name, &v.Color, &state); err != nil {
			return nil, err
		}
		v.Complete = model.CustomTypeStatusOption{CompletionState: state}.IsComplete()
		out = append(out, v)
	}
	return out, rows.Err()
}
