package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/amasok/asagao/internal/readmodel"
)

// Which projects' tasks are cached (#54). A sync fetches every project of the workspace,
// but the tasks only of the projects that were opened (or are favorites, see
// sync.Syncer.Run); the others are listed only.

const (
	// KeyProjectTasks + project gid: how far the project's tasks are cached (readmodel.Tasks*).
	KeyProjectTasks = readmodel.KeyProjectTasks
	// KeyMyTasks + workspace gid: how far My Tasks of the workspace is fetched.
	KeyMyTasks = readmodel.KeyMyTasks
)

// fetchedSQL is true for a row of projects whose tasks are cached: their incomplete tasks
// at least were fetched, or the project has an events token (it was synced before #54,
// when every member project was).
const fetchedSQL = `(EXISTS(SELECT 1 FROM sync_state ps WHERE ps.key = '` + KeyProjectTasks + `' || projects.gid
		AND ps.value IN ('` + readmodel.TasksOpen + `', '` + readmodel.TasksAll + `'))
	OR EXISTS(SELECT 1 FROM sync_state pe WHERE pe.key = '` + KeyEvents + `' || projects.gid AND pe.value <> ''))`

// ProjectTaskStates returns, for every project whose tasks a sync fetches, how far they are
// cached (readmodel.TasksWanted, TasksOpen or TasksAll): those with a state, those with an
// events token (TasksAll unless they have a state: synced before #54), and the favorites
// (TasksWanted unless they have a state).
func (s *Store) ProjectTaskStates(ctx context.Context) (map[string]string, error) {
	states := map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT substr(key, ?), value FROM sync_state WHERE substr(key, 1, ?) = ? AND value <> ''`,
		len(KeyProjectTasks)+1, len(KeyProjectTasks), KeyProjectTasks)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var gid, v string
		if err := rows.Scan(&gid, &v); err != nil {
			rows.Close()
			return nil, err
		}
		states[gid] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	add := func(q string, state string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var gid string
			if err := rows.Scan(&gid); err != nil {
				return err
			}
			if _, ok := states[gid]; !ok {
				states[gid] = state
			}
		}
		return rows.Err()
	}
	if err := add(`SELECT substr(key, ?) FROM sync_state WHERE substr(key, 1, ?) = ? AND value <> ''`, readmodel.TasksAll,
		len(KeyEvents)+1, len(KeyEvents), KeyEvents); err != nil {
		return nil, err
	}
	if err := add(`SELECT project_gid FROM project_favorites`, readmodel.TasksWanted); err != nil {
		return nil, err
	}
	return states, nil
}

// ProjectTaskState is how far a project's tasks are cached: "" (listed only), or one of
// readmodel.TasksWanted, TasksOpen and TasksAll. A project with an events token and no
// state was synced before #54: TasksAll.
func (s *Store) ProjectTaskState(ctx context.Context, projectGID string) (string, error) {
	v, err := s.SyncState(ctx, KeyProjectTasks+projectGID)
	if err != nil || v != "" {
		return v, err
	}
	token, err := s.SyncState(ctx, KeyEvents+projectGID)
	if err != nil || token == "" {
		return "", err
	}
	return readmodel.TasksAll, nil
}

// WantProject records that a project was opened: from now on, a sync fetches its tasks
// (and the next sync fetches them the first time, if the open could not). It returns the
// project's state (TasksWanted if it was only listed).
func (s *Store) WantProject(ctx context.Context, projectGID string) (string, error) {
	st, err := s.ProjectTaskState(ctx, projectGID)
	if err != nil || st != "" {
		return st, err
	}
	if err := wantProject(ctx, s.db, projectGID); err != nil {
		return "", err
	}
	return s.ProjectTaskState(ctx, projectGID)
}

// wantProject records a project as TasksWanted unless it has a state or an events token
// (another process may have fetched it meanwhile).
func wantProject(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectGID string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO sync_state(key, value) SELECT ?, ?
		WHERE NOT EXISTS(SELECT 1 FROM sync_state WHERE key = ? AND value <> '')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value WHERE sync_state.value = ''`,
		KeyProjectTasks+projectGID, readmodel.TasksWanted, KeyEvents+projectGID)
	return err
}

// CountUnfetchedProjects counts the projects (not archived; only those I am a member of,
// if memberOnly) whose tasks are not cached: what a search of the cache cannot find.
func (s *Store) CountUnfetchedProjects(ctx context.Context, memberOnly bool) (int, error) {
	q := `SELECT COUNT(*) FROM projects WHERE archived = 0 AND NOT ` + fetchedSQL
	if memberOnly {
		q += " AND is_member = 1"
	}
	var n int
	err := s.db.QueryRowContext(ctx, q).Scan(&n)
	return n, err
}

// SyncStatesWithPrefix returns the sync_state values whose key starts with prefix, by the
// rest of the key.
func (s *Store) SyncStatesWithPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT substr(key, `+strconv.Itoa(len(prefix)+1)+`), value FROM sync_state WHERE substr(key, 1, ?) = ?`,
		len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ProjectRef returns a cached project's name and workspace (ErrNotFound if it is not cached).
func (s *Store) ProjectRef(ctx context.Context, gid string) (readmodel.ProjectRef, error) {
	r := readmodel.ProjectRef{GID: gid}
	err := s.db.QueryRowContext(ctx, "SELECT name, workspace_gid FROM projects WHERE gid = ?", gid).Scan(&r.Name, &r.WorkspaceGID)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}
