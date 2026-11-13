package store

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

func (s *Store) UpsertUser(ctx context.Context, u asana.User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(gid, name, email) VALUES(?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET name = excluded.name, email = excluded.email`,
		u.GID, u.Name, u.Email)
	return err
}

func (s *Store) UpsertWorkspaces(ctx context.Context, ws []asana.Workspace) error {
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
func (s *Store) UpsertProjects(ctx context.Context, workspaceGID string, ps []asana.Project, meGID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, p := range ps {
			isMember := slices.ContainsFunc(p.Members, func(r asana.Ref) bool { return r.GID == meGID })
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
func (s *Store) ReplaceSections(ctx context.Context, projectGID string, secs []asana.Section) error {
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
		return nil
	})
}

// UpsertTasks stores tasks with their memberships, tags and assignee.
func (s *Store) UpsertTasks(ctx context.Context, tasks []asana.Task) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, t := range tasks {
			if err := upsertTask(ctx, tx, t); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertTask(ctx context.Context, tx *sql.Tx, t asana.Task) error {
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
		 due_on, due_at, start_on, parent_gid, num_subtasks, permalink_url, created_at, modified_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET
			workspace_gid = excluded.workspace_gid, name = excluded.name, notes = excluded.notes,
			resource_subtype = excluded.resource_subtype, completed = excluded.completed,
			completed_at = excluded.completed_at, assignee_gid = excluded.assignee_gid,
			due_on = excluded.due_on, due_at = excluded.due_at, start_on = excluded.start_on,
			parent_gid = excluded.parent_gid, num_subtasks = excluded.num_subtasks,
			permalink_url = excluded.permalink_url, created_at = excluded.created_at,
			modified_at = excluded.modified_at`,
		t.GID, workspace, t.Name, t.Notes, subtype, t.Completed, formatTimePtr(t.CompletedAt), assignee,
		t.DueOn, formatTimePtr(t.DueAt), t.StartOn, parent, t.NumSubtasks, t.PermalinkURL,
		formatTime(t.CreatedAt), formatTime(t.ModifiedAt)); err != nil {
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
