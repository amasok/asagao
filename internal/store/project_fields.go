package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/project"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// OpUpdateProject changes a project's fields (outbox.KindUpdateProject).
const OpUpdateProject = outbox.KindUpdateProject

// ProjectOverview is readmodel.ProjectOverview.
type ProjectOverview = readmodel.ProjectOverview

// TeamView is readmodel.TeamView.
type TeamView = readmodel.TeamView

// projectColumns maps the fields of an update_project payload to the projects columns.
var projectColumns = map[string]string{"notes": "notes", "html_notes": "html_notes", "owner": "owner_gid",
	"start_on": "start_on", "due_on": "due_on", "color": "color"}

// UpdateProjectLocal applies field changes to a project and queues them for Asana (see
// project.Fields). Consecutive edits of the same project are one outbox entry
// (outbox.Fold); an edit that reverts every field to its original value removes it.
// A color Asana's GET answers (red) is kept and sent as its written name (dark-red).
func (s *Store) UpdateProjectLocal(ctx context.Context, gid string, changes map[string]any, now time.Time) error {
	changes, err := project.NormalizeFields(changes)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		cur, err := currentProjectFields(ctx, tx, gid)
		if err != nil {
			return err
		}
		start, due := cur["start_on"].(string), cur["due_on"].(string)
		if v, ok := changes["start_on"].(string); ok {
			start = v
		}
		if v, ok := changes["due_on"].(string); ok {
			due = v
		}
		if err := project.CheckDates(start, due); err != nil {
			return err
		}
		op, err := findOp(ctx, tx, OpUpdateProject, gid)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpUpdateProject, TaskGID: gid, Payload: map[string]any{}, Base: map[string]any{},
				State: OpPending, CreatedAt: formatTime(now)}
		}
		folded, deleteOp := outbox.Fold(*op, cur, changes)
		if err := applyProjectFields(ctx, tx, gid, changes); err != nil {
			return err
		}
		if deleteOp {
			if folded.ID != 0 {
				_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", folded.ID)
				return err
			}
			return nil
		}
		return saveOp(ctx, tx, &folded)
	})
}

// currentProjectFields returns a project's local values of the fields an edit may change.
func currentProjectFields(ctx context.Context, tx *sql.Tx, gid string) (map[string]any, error) {
	var notes, html, owner, start, due, color string
	err := tx.QueryRowContext(ctx, "SELECT notes, html_notes, owner_gid, start_on, due_on, color FROM projects WHERE gid = ?", gid).
		Scan(&notes, &html, &owner, &start, &due, &color)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("project %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"notes": notes, "html_notes": html, "owner": owner, "start_on": start, "due_on": due, "color": color}, nil
}

// applyProjectFields writes field values (an update_project payload or base) to a project.
func applyProjectFields(ctx context.Context, tx *sql.Tx, gid string, fields map[string]any) error {
	for k, v := range fields {
		col, ok := projectColumns[k]
		if !ok {
			continue
		}
		s, _ := v.(string)
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET "+col+" = ? WHERE gid = ?", s, gid); err != nil {
			return err
		}
	}
	return nil
}

// reapplyProjectUpdates keeps unsent project edits after a sync stored the server's
// values.
func reapplyProjectUpdates(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT task_gid, payload FROM outbox WHERE kind = ? AND state <> 'failed' ORDER BY id", OpUpdateProject)
	if err != nil {
		return err
	}
	type edit struct {
		gid     string
		payload map[string]any
	}
	var edits []edit
	for rows.Next() {
		var e edit
		var payload string
		if err := rows.Scan(&e.gid, &payload); err != nil {
			rows.Close()
			return err
		}
		if err := termsafe.Unmarshal([]byte(payload), &e.payload); err != nil {
			rows.Close()
			return err
		}
		edits = append(edits, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range edits {
		if err := applyProjectFields(ctx, tx, e.gid, e.payload); err != nil {
			return err
		}
	}
	return nil
}

// --- teams and favorites (#38) ---

// ReplaceTeams stores the teams of a workspace the user is a member of.
func (s *Store) ReplaceTeams(ctx context.Context, workspaceGID string, teams []model.Team) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM teams WHERE workspace_gid = ?", workspaceGID); err != nil {
			return err
		}
		for _, t := range teams {
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO teams(gid, workspace_gid, name) VALUES(?, ?, ?)", t.GID, workspaceGID, t.Name); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceFavoriteProjects stores the user's favorite projects of a workspace, in order.
func (s *Store) ReplaceFavoriteProjects(ctx context.Context, workspaceGID string, favorites []model.Ref) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM project_favorites WHERE workspace_gid = ?", workspaceGID); err != nil {
			return err
		}
		for i, f := range favorites {
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO project_favorites(project_gid, workspace_gid, position) VALUES(?, ?, ?)",
				f.GID, workspaceGID, i); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListTeams returns the teams a new project can be made in: the teams I am in, or (before
// they were fetched, or in a workspace without them) the teams of the cached projects.
// Only the default workspace's.
func (s *Store) ListTeams(ctx context.Context) ([]TeamView, error) {
	ws, err := s.DefaultWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	teams, err := s.queryTeams(ctx, "SELECT gid, name FROM teams WHERE workspace_gid = ? ORDER BY name, gid", ws)
	if err != nil || len(teams) > 0 {
		return teams, err
	}
	return s.queryTeams(ctx, `SELECT team_gid, MAX(team_name) FROM projects
		WHERE workspace_gid = ? AND team_gid <> '' GROUP BY team_gid ORDER BY MAX(team_name), team_gid`, ws)
}

func (s *Store) queryTeams(ctx context.Context, q string, args ...any) ([]TeamView, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamView{}
	for rows.Next() {
		var t TeamView
		if err := rows.Scan(&t.GID, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListProjectsInTeam is ListProjects narrowed to one team, given by gid or (partial)
// name (see ResolveTeam; "" for every team).
func (s *Store) ListProjectsInTeam(ctx context.Context, memberOnly bool, team string) ([]ProjectView, error) {
	if team == "" {
		return s.ListProjects(ctx, memberOnly)
	}
	gid, err := s.ResolveTeam(ctx, team)
	if err != nil {
		return nil, err
	}
	ps, err := s.ListProjects(ctx, memberOnly)
	if err != nil {
		return nil, err
	}
	out := []ProjectView{}
	for _, p := range ps {
		if p.TeamGID == gid {
			out = append(out, p)
		}
	}
	return out, nil
}

// TeamForNewProject picks the team of a new project: team (a gid or name, see
// ResolveTeam) if given, else the only team of ListTeams, else none (a workspace without
// teams). With several teams and none given it is an error that names them (ErrAmbiguous).
func (s *Store) TeamForNewProject(ctx context.Context, team string) (string, error) {
	if strings.TrimSpace(team) != "" {
		return s.ResolveTeam(ctx, team)
	}
	teams, err := s.ListTeams(ctx)
	if err != nil {
		return "", err
	}
	switch len(teams) {
	case 0:
		return "", nil
	case 1:
		return teams[0].GID, nil
	}
	names := make([]string, len(teams))
	for i, t := range teams {
		names[i] = t.Name
	}
	return "", fmt.Errorf("チームを指定してください（%s）: %w", strings.Join(names, "、"), ErrAmbiguous)
}

// ResolveTeam finds a team by gid, exact name (case-insensitive) or unique partial name,
// among the teams of ListTeams and of the cached projects.
func (s *Store) ResolveTeam(ctx context.Context, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("team: %w", ErrNotFound)
	}
	teams, err := s.queryTeams(ctx, `SELECT gid, name FROM teams
		UNION SELECT team_gid, MAX(team_name) FROM projects WHERE team_gid <> '' AND team_gid NOT IN (SELECT gid FROM teams) GROUP BY team_gid`)
	if err != nil {
		return "", err
	}
	var exact, partial []TeamView
	for _, t := range teams {
		switch {
		case t.GID == arg:
			return t.GID, nil
		case strings.EqualFold(t.Name, arg):
			exact = append(exact, t)
		case strings.Contains(strings.ToLower(t.Name), strings.ToLower(arg)):
			partial = append(partial, t)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("team %q: %w", arg, ErrNotFound)
	case 1:
		return matches[0].GID, nil
	}
	names := make([]string, len(matches))
	for i, t := range matches {
		names[i] = t.Name
	}
	return "", fmt.Errorf("team %q matches %s: %w", arg, strings.Join(names, ", "), ErrAmbiguous)
}

// --- the project overview (#39) ---

// Brief states of project_overviews.brief_state (readmodel's).
const (
	BriefNone        = readmodel.BriefNone
	BriefUnavailable = readmodel.BriefUnavailable
	BriefAvailable   = readmodel.BriefAvailable
)

// SaveProjectOverview records what the overview fetched: the task counts (nil: not
// fetched, keep the cached ones) and the brief (briefState says whether there is one; ""
// keeps the cached brief).
func (s *Store) SaveProjectOverview(ctx context.Context, projectGID string, counts *model.TaskCounts, briefState string, brief *model.ProjectBrief, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO project_overviews(project_gid) VALUES(?)", projectGID); err != nil {
			return err
		}
		if counts != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE project_overviews SET num_tasks = ?, num_completed_tasks = ?,
				num_milestones = ?, num_completed_milestones = ? WHERE project_gid = ?`,
				counts.NumTasks, counts.NumCompletedTasks, counts.NumMilestones, counts.NumCompletedMiles, projectGID); err != nil {
				return err
			}
		}
		if briefState != "" {
			var title, text, html string
			if brief != nil && briefState == BriefAvailable {
				title, text, html = brief.Title, brief.Text, brief.HTMLText
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_overviews SET brief_state = ?, brief_title = ?, brief_text = ?,
				brief_html = ? WHERE project_gid = ?`, briefState, title, text, html, projectGID); err != nil {
				return err
			}
			if briefState != BriefUnavailable { // unsent changes stay until they are sent (#60)
				if err := reapplyBriefEdits(ctx, tx, projectGID); err != nil {
					return err
				}
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE project_overviews SET fetched_at = ? WHERE project_gid = ?", formatTime(now), projectGID)
		return err
	})
}

// ProjectOverview returns a project's overview from the cache.
func (s *Store) ProjectOverview(ctx context.Context, gid string) (*ProjectOverview, error) {
	var o ProjectOverview
	var ownerGID, ownerName, briefState, briefTitle, briefText, briefHTML, fetchedAt string
	var tasks, completed, milestones, completedMilestones sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT p.gid, p.name, p.team_name, p.color, p.notes, p.html_notes, p.owner_gid,
			COALESCE(u.name, ''), p.start_on, p.due_on, p.permalink_url, p.archived,
			EXISTS(SELECT 1 FROM project_favorites f WHERE f.project_gid = p.gid),
			EXISTS(SELECT 1 FROM outbox WHERE kind = ? AND task_gid = p.gid),
			EXISTS(SELECT 1 FROM outbox WHERE kind = ? AND task_gid = p.gid),
			o.num_tasks, o.num_completed_tasks, o.num_milestones, o.num_completed_milestones,
			COALESCE(o.brief_state, ''), COALESCE(o.brief_title, ''), COALESCE(o.brief_text, ''), COALESCE(o.brief_html, ''),
			COALESCE(o.fetched_at, '')
		FROM projects p LEFT JOIN users u ON u.gid = p.owner_gid AND p.owner_gid <> ''
		LEFT JOIN project_overviews o ON o.project_gid = p.gid WHERE p.gid = ?`, OpUpdateProject, OpUpdateBrief, gid).
		Scan(&o.GID, &o.Name, &o.TeamName, &o.Color, &o.Notes, &o.HTMLNotes, &ownerGID, &ownerName, &o.StartOn, &o.DueOn,
			&o.PermalinkURL, &o.Archived, &o.Favorite, &o.Pending, &o.BriefPending, &tasks, &completed, &milestones, &completedMilestones,
			&briefState, &briefTitle, &briefText, &briefHTML, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("project %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if ownerGID != "" {
		o.Owner = &UserRef{GID: ownerGID, Name: ownerName}
	}
	if tasks.Valid {
		o.Counts = &readmodel.TaskCounts{Tasks: int(tasks.Int64), CompletedTasks: int(completed.Int64),
			Milestones: int(milestones.Int64), CompletedMilestones: int(completedMilestones.Int64)}
	}
	o.BriefState = briefState
	if briefState == BriefAvailable {
		o.Brief = &readmodel.ProjectBrief{Title: briefTitle, Text: briefText, HTMLText: briefHTML}
	}
	o.FetchedAt = fetchedAt
	updates, err := s.StatusUpdates(ctx, gid)
	if err != nil {
		return nil, err
	}
	if len(updates) > 0 {
		o.Status = &updates[0]
	}
	return &o, nil
}
