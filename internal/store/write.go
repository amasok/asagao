package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

func (s *Store) UpsertUser(ctx context.Context, u model.User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(gid, name, email) VALUES(?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET name = excluded.name, email = excluded.email`,
		u.GID, u.Name, u.Email)
	return err
}

func (s *Store) UpsertWorkspaces(ctx context.Context, ws []model.Workspace) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, w := range ws {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(gid, name, is_organization) VALUES(?, ?, ?)
				ON CONFLICT(gid) DO UPDATE SET name = excluded.name, is_organization = excluded.is_organization`,
				w.GID, w.Name, w.IsOrganization); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpsertProjects stores projects of a workspace. meGID marks the projects the user is a member of.
func (s *Store) UpsertProjects(ctx context.Context, workspaceGID string, ps []model.Project, meGID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, p := range ps {
			isMember := slices.ContainsFunc(p.Members, func(r model.Ref) bool { return r.GID == meGID })
			if p.Members != nil {
				if _, err := tx.ExecContext(ctx, "DELETE FROM project_members WHERE project_gid = ?", p.GID); err != nil {
					return err
				}
				for _, u := range p.Members {
					if _, err := tx.ExecContext(ctx, `INSERT INTO users(gid, name) VALUES(?, ?)
						ON CONFLICT(gid) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE users.name END`, u.GID, u.Name); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO project_members(project_gid, user_gid) VALUES(?, ?)", p.GID, u.GID); err != nil {
						return err
					}
				}
			}
			var teamGID, teamName string
			if p.Team != nil {
				teamGID, teamName = p.Team.GID, p.Team.Name
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO projects
				(gid, workspace_gid, name, archived, color, notes, team_gid, team_name, is_member, permalink_url, modified_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(gid) DO UPDATE SET
					workspace_gid = excluded.workspace_gid, name = excluded.name, archived = excluded.archived,
					color = excluded.color, notes = excluded.notes, team_gid = excluded.team_gid,
					team_name = excluded.team_name, is_member = excluded.is_member,
					permalink_url = excluded.permalink_url, modified_at = excluded.modified_at`,
				p.GID, workspaceGID, p.Name, p.Archived, p.Color, p.Notes, teamGID, teamName, isMember,
				p.PermalinkURL, formatTime(p.ModifiedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceSections replaces all sections of a project, keeping Asana's order.
func (s *Store) ReplaceSections(ctx context.Context, projectGID string, secs []model.Section) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sections WHERE project_gid = ?", projectGID); err != nil {
			return err
		}
		for i, sec := range secs {
			if _, err := tx.ExecContext(ctx, "INSERT INTO sections(gid, project_gid, name, position) VALUES(?, ?, ?, ?)",
				sec.GID, projectGID, sec.Name, i); err != nil {
				return err
			}
		}
		return reapplySectionRenames(ctx, tx)
	})
}

// Writing many tasks (a pull of a large project) holds the write lock for a while, and
// another process's write (TUI, MCP server) waits for it for at most busy_timeout, then
// fails as busy (#55). UpsertTasks writes them in transactions of about writeSlice at most
// and, between two of them, leaves the lock free for writeYield: longer than SQLite's busy
// handler sleeps between two tries (100ms at most), so a writer that waits gets it.
var (
	writeSlice = busyTimeout / 10
	writeYield = 150 * time.Millisecond
)

// SetTaskWriteSlicesForTest makes UpsertTasks write in transactions of about slice (0: one
// task each) with yield between them, until the returned function restores them. It is for
// tests of other packages that need a few tasks written in several transactions.
func SetTaskWriteSlicesForTest(slice, yield time.Duration) (restore func()) {
	oldSlice, oldYield := writeSlice, writeYield
	writeSlice, writeYield = slice, yield
	return func() { writeSlice, writeYield = oldSlice, oldYield }
}

// UpsertTasks stores tasks with their memberships, tags and assignee.
//
// Many tasks are written in several transactions (see writeSlice): if it fails or ctx is
// cancelled, the tasks before the failure are stored and the rest are not. Each task is
// stored whole or not at all, and storing the same tasks again gives the same result, so a
// pull that failed is simply done again; what a sync records of its progress (events
// tokens, the last sync, pruning by what it fetched) waits until all tasks are written.
func (s *Store) UpsertTasks(ctx context.Context, tasks []model.Task) error {
	for len(tasks) > 0 {
		err := s.tx(ctx, func(tx *sql.Tx) error {
			start := time.Now()
			for i, t := range tasks {
				if i > 0 && time.Since(start) >= writeSlice {
					tasks = tasks[i:]
					return nil
				}
				if err := upsertTaskTx(ctx, tx, t); err != nil {
					return err
				}
			}
			tasks = nil
			return nil
		})
		if err != nil {
			return err
		}
		if len(tasks) > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(writeYield):
			}
		}
	}
	return nil
}

// upsertTaskTx stores one task and reapplies its unsent local edits, which win over what
// the server returned until they are pushed.
func upsertTaskTx(ctx context.Context, tx *sql.Tx, t model.Task) error {
	if err := upsertTask(ctx, tx, t); err != nil {
		return err
	}
	if err := reapplyUpdates(ctx, tx, t.GID); err != nil {
		return err
	}
	if err := reapplyTagOps(ctx, tx, t.GID); err != nil {
		return err
	}
	if err := reapplyRelationOps(ctx, tx, t.GID); err != nil {
		return err
	}
	if err := reapplyCustomFieldOps(ctx, tx, t.GID); err != nil {
		return err
	}
	return reapplyDependencyOps(ctx, tx, t.GID)
}

func upsertTask(ctx context.Context, tx *sql.Tx, t model.Task) error {
	var assignee, workspace, parent string
	if t.Assignee != nil {
		assignee = t.Assignee.GID
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(gid, name) VALUES(?, ?)
			ON CONFLICT(gid) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE users.name END`,
			t.Assignee.GID, t.Assignee.Name); err != nil {
			return err
		}
	}
	if t.Workspace != nil {
		workspace = t.Workspace.GID
	}
	if t.Parent != nil {
		parent = t.Parent.GID
	}
	subtype := t.ResourceSubtype
	if subtype == "" {
		subtype = "default_task"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
		(gid, workspace_gid, name, notes, resource_subtype, completed, completed_at, assignee_gid,
		 due_on, due_at, start_on, parent_gid, num_subtasks, permalink_url, created_at, modified_at, html_notes)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET
			workspace_gid = excluded.workspace_gid, name = excluded.name, notes = excluded.notes,
			html_notes = excluded.html_notes,
			resource_subtype = excluded.resource_subtype, completed = excluded.completed,
			completed_at = excluded.completed_at, assignee_gid = excluded.assignee_gid,
			due_on = excluded.due_on, due_at = excluded.due_at, start_on = excluded.start_on,
			parent_gid = excluded.parent_gid, num_subtasks = excluded.num_subtasks,
			permalink_url = excluded.permalink_url, created_at = excluded.created_at,
			modified_at = excluded.modified_at`,
		t.GID, workspace, t.Name, t.Notes, subtype, t.Completed, formatTimePtr(t.CompletedAt), assignee,
		t.DueOn, formatTimePtr(t.DueAt), t.StartOn, parent, t.NumSubtasks, t.PermalinkURL,
		formatTime(t.CreatedAt), formatTime(t.ModifiedAt), t.HTMLNotes); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM task_memberships WHERE task_gid = ?", t.GID); err != nil {
		return err
	}
	for _, m := range t.Memberships {
		if m.Project == nil {
			continue
		}
		var section string
		if m.Section != nil {
			section = m.Section.GID
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO task_memberships(task_gid, project_gid, section_gid) VALUES(?, ?, ?)",
			t.GID, m.Project.GID, section); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET liked = ?, num_likes = ? WHERE gid = ?", t.Liked, t.NumLikes, t.GID); err != nil {
		return err
	}
	if !t.Partial { // fetched without the optional fields: keep what is known
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET approval_status = ? WHERE gid = ?", t.ApprovalStatus, t.GID); err != nil {
			return err
		}
	}
	if err := upsertTaskDependencies(ctx, tx, t); err != nil {
		return err
	}
	if t.Followers != nil {
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_followers WHERE task_gid = ?", t.GID); err != nil {
			return err
		}
		for _, f := range t.Followers {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(gid, name) VALUES(?, ?)
				ON CONFLICT(gid) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE users.name END`,
				f.GID, f.Name); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO task_followers(task_gid, user_gid) VALUES(?, ?)", t.GID, f.GID); err != nil {
				return err
			}
		}
	}
	if err := upsertTaskCustomFields(ctx, tx, t); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_tags WHERE task_gid = ?", t.GID); err != nil {
		return err
	}
	for _, tag := range t.Tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tags(gid, name) VALUES(?, ?)
			ON CONFLICT(gid) DO UPDATE SET name = excluded.name`, tag.GID, tag.Name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO task_tags(task_gid, tag_gid) VALUES(?, ?)", t.GID, tag.GID); err != nil {
			return err
		}
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// ReplaceStories replaces the stories (comments and activity) of a task and records the fetch
// time. fetchedAt is when the fetch started: the list may be older than what was stored
// meanwhile, so these stay (#40):
//
//   - comments posted offline ("local:" gids), until they are sent;
//   - stories created at or after fetchedAt that the list does not have: a comment sent
//     (ReplaceLocalStory) while the list was being read. The next fetch drops them if
//     they were deleted in Asana after all.
func (s *Store) ReplaceStories(ctx context.Context, taskGID string, stories []model.Story, fetchedAt time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM stories WHERE task_gid = ? AND gid NOT LIKE 'local:%'
			AND NOT (created_at <> '' AND created_at >= ?)`, taskGID, formatTime(fetchedAt)); err != nil {
			return err
		}
		for _, st := range stories {
			var byGID, byName string
			if st.CreatedBy != nil {
				byGID, byName = st.CreatedBy.GID, st.CreatedBy.Name
			}
			// A story kept above may be in the list too (an upsert keeps the full-text index right).
			if _, err := tx.ExecContext(ctx, `INSERT INTO stories
				(gid, task_gid, type, resource_subtype, text, created_by_gid, created_by_name, created_at, html_text)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(gid) DO UPDATE SET task_gid = excluded.task_gid, type = excluded.type,
					resource_subtype = excluded.resource_subtype, text = excluded.text, created_by_gid = excluded.created_by_gid,
					created_by_name = excluded.created_by_name, created_at = excluded.created_at, html_text = excluded.html_text`,
				st.GID, taskGID, st.Type, st.ResourceSubtype, st.Text, byGID, byName, formatTime(st.CreatedAt), st.HTMLText); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO task_details(task_gid, fetched_at) VALUES(?, ?)
			ON CONFLICT(task_gid) DO UPDATE SET fetched_at = excluded.fetched_at`, taskGID, formatTime(fetchedAt))
		return err
	})
}

// PruneTasks deletes tasks that a full sync did not return (deleted in Asana, moved out of
// synced projects, or completed before the retention window). Subtasks of kept tasks are kept.
// The WHERE clauses below are task.MayPrune and task.RememberRef, in one DELETE
// (TestPruneTasksFollowsTaskRules checks that they agree).
func (s *Store) PruneTasks(ctx context.Context, keep map[string]bool) (int64, error) {
	var deleted int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS keep_tasks(gid TEXT PRIMARY KEY)"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM keep_tasks"); err != nil {
			return err
		}
		for gid := range keep {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO keep_tasks(gid) VALUES(?)", gid); err != nil {
				return err
			}
		}
		// Tasks linked by a dependency stay known by name and state (task_refs).
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO task_refs(gid, name, completed, resource_subtype)
			SELECT gid, name, completed, resource_subtype FROM tasks
			WHERE gid NOT IN (SELECT gid FROM keep_tasks) AND gid NOT IN (SELECT task_gid FROM outbox)
			  AND (gid IN (SELECT dependency_gid FROM task_dependencies) OR gid IN (SELECT task_gid FROM task_dependencies))`); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM tasks
			WHERE gid NOT IN (SELECT gid FROM keep_tasks)
			  AND gid NOT IN (SELECT task_gid FROM outbox)
			  AND (parent_gid = '' OR parent_gid NOT IN (SELECT gid FROM keep_tasks))`)
		if err != nil {
			return err
		}
		if deleted, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("prune tasks: %w", err)
		}
		_, err = tx.ExecContext(ctx, "DROP TABLE keep_tasks")
		return err
	})
	return deleted, err
}

// DeleteGoneTasks removes tasks Asana says are deleted or no longer visible to us, with
// their subtasks. Like PruneTasks, a task with queued changes is kept (sending them finds
// out it is gone, see sync.dropGone), and tasks linked by a dependency stay known by name.
func (s *Store) DeleteGoneTasks(ctx context.Context, gids []string) (int64, error) {
	if len(gids) == 0 {
		return 0, nil
	}
	b, err := json.Marshal(gids)
	if err != nil {
		return 0, err
	}
	const sub = `WITH RECURSIVE sub(gid) AS (
			SELECT value FROM json_each(?1) UNION SELECT t.gid FROM tasks t JOIN sub ON t.parent_gid = sub.gid) `
	var deleted int64
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sub+`INSERT OR REPLACE INTO task_refs(gid, name, completed, resource_subtype)
			SELECT gid, name, completed, resource_subtype FROM tasks
			WHERE gid IN (SELECT gid FROM sub) AND gid NOT IN (SELECT task_gid FROM outbox)
			  AND (gid IN (SELECT dependency_gid FROM task_dependencies) OR gid IN (SELECT task_gid FROM task_dependencies))`, string(b)); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, sub+`DELETE FROM tasks
			WHERE gid IN (SELECT gid FROM sub) AND gid NOT IN (SELECT task_gid FROM outbox)`, string(b))
		if err != nil {
			return err
		}
		if deleted, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("delete gone tasks: %w", err)
		}
		return nil
	})
	return deleted, err
}

// Prefixes of the sync_state keys kept per project (prefix + project gid). They are
// deleted with the project (see PruneProjects), so they do not pile up.
const (
	// KeyCustomFields + project gid: when the project's custom fields were last fetched.
	KeyCustomFields = readmodel.KeyCustomFields
	// KeyCollapsed + scope: the TUI's collapsed groups of a list ("my", "inbox", or a project gid).
	KeyCollapsed = readmodel.KeyCollapsed
	// KeyEvents + project gid: the sync token for the project's changes (GET /events).
	KeyEvents = readmodel.KeyEvents
)

// projectKeys lists, for each per-project key prefix, the SQL condition that tells a
// project's key from other keys with the same prefix ({gid} is the part after the prefix).
var projectKeys = []struct{ prefix, isProject string }{
	{KeyCustomFields, "1"},
	{KeyEvents, "1"},
	{KeyCollapsed, "{gid} GLOB '[0-9]*'"}, // Asana gids are digits; "my" and "inbox" are not projects
}

// PruneProjects deletes projects (and their sections) of a workspace that are no longer returned.
func (s *Store) PruneProjects(ctx context.Context, workspaceGID string, keep []model.Project) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		return pruneProjects(ctx, tx, workspaceGID, keep)
	})
}

func pruneProjects(ctx context.Context, tx *sql.Tx, workspaceGID string, keep []model.Project) error {
	rows, err := tx.QueryContext(ctx, "SELECT gid FROM projects WHERE workspace_gid = ?", workspaceGID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var gid string
		if err := rows.Scan(&gid); err != nil {
			rows.Close()
			return err
		}
		if !slices.ContainsFunc(keep, func(p model.Project) bool { return p.GID == gid }) {
			stale = append(stale, gid)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, gid := range stale {
		for _, q := range []string{
			"DELETE FROM sections WHERE project_gid = ?",
			"DELETE FROM project_members WHERE project_gid = ?",
			"DELETE FROM status_updates WHERE project_gid = ?",
			"DELETE FROM project_custom_fields WHERE project_gid = ?",
			"DELETE FROM task_memberships WHERE project_gid = ?",
			"DELETE FROM projects WHERE gid = ?",
		} {
			if _, err := tx.ExecContext(ctx, q, gid); err != nil {
				return err
			}
		}
	}
	return pruneProjectKeys(ctx, tx)
}

// pruneProjectKeys deletes the per-project sync_state keys of projects no longer in the
// store, including those left behind by versions that did not delete them (the custom
// fields are fetched again, and a list's collapsed groups reset, if the project comes back).
func pruneProjectKeys(ctx context.Context, tx *sql.Tx) error {
	for _, k := range projectKeys {
		gidPart := "substr(key, " + strconv.Itoa(len(k.prefix)+1) + ")"
		cond := strings.ReplaceAll(k.isProject, "{gid}", gidPart)
		if _, err := tx.ExecContext(ctx, `DELETE FROM sync_state
			WHERE substr(key, 1, ?) = ? AND `+cond+` AND `+gidPart+` NOT IN (SELECT gid FROM projects)`,
			len(k.prefix), k.prefix); err != nil {
			return err
		}
	}
	return nil
}

// PruneWorkspaces deletes the workspaces (with their projects and the projects' keys) that
// are not in keep, the workspaces Asana returned. An empty keep is ignored: an account
// always has a workspace, so it would only mean a broken answer.
func (s *Store) PruneWorkspaces(ctx context.Context, keep []model.Workspace) error {
	if len(keep) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT gid FROM workspaces")
		if err != nil {
			return err
		}
		var stale []string
		for rows.Next() {
			var gid string
			if err := rows.Scan(&gid); err != nil {
				rows.Close()
				return err
			}
			if !slices.ContainsFunc(keep, func(w model.Workspace) bool { return w.GID == gid }) {
				stale = append(stale, gid)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, gid := range stale {
			if err := pruneProjects(ctx, tx, gid, nil); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM workspaces WHERE gid = ?", gid); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpsertUsers stores workspace members.
func (s *Store) UpsertUsers(ctx context.Context, users []model.User) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, u := range users {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(gid, name, email) VALUES(?, ?, ?)
				ON CONFLICT(gid) DO UPDATE SET name = excluded.name,
					email = CASE WHEN excluded.email <> '' THEN excluded.email ELSE users.email END`,
				u.GID, u.Name, u.Email); err != nil {
				return err
			}
		}
		return nil
	})
}
