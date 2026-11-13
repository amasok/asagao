package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrNotFound is returned when a task or project is not in the local store.
var ErrNotFound = errors.New("not found in local store")

// ErrAmbiguous is returned when a project name matches more than one project.
var ErrAmbiguous = errors.New("ambiguous name")

const KeyMeGID = "me_gid"

// --- views (the JSON shapes returned by the CLI and MCP; keep them stable) ---

type UserRef struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type MembershipView struct {
	ProjectGID  string `json:"project_gid"`
	ProjectName string `json:"project_name"`
	SectionGID  string `json:"section_gid,omitempty"`
	SectionName string `json:"section_name,omitempty"`
}

type TaskSummary struct {
	GID             string           `json:"gid"`
	Name            string           `json:"name"`
	ResourceSubtype string           `json:"resource_subtype"`
	Completed       bool             `json:"completed"`
	CompletedAt     string           `json:"completed_at,omitempty"`
	DueOn           string           `json:"due_on,omitempty"`
	DueAt           string           `json:"due_at,omitempty"`
	StartOn         string           `json:"start_on,omitempty"`
	Assignee        *UserRef         `json:"assignee"`
	Memberships     []MembershipView `json:"memberships"`
	Tags            []string         `json:"tags"`
	ParentGID       string           `json:"parent_gid,omitempty"`
	ParentName      string           `json:"parent_name,omitempty"`
	NumSubtasks     int              `json:"num_subtasks"`
	PermalinkURL    string           `json:"permalink_url"`
	ModifiedAt      string           `json:"modified_at"`
	// Pending is true while local changes to this task have not been sent to Asana.
	Pending  bool `json:"pending"`
	Liked    bool `json:"liked"`
	NumLikes int  `json:"num_likes"`
}

type StoryView struct {
	GID             string   `json:"gid"`
	Type            string   `json:"type"`
	ResourceSubtype string   `json:"resource_subtype"`
	Text            string   `json:"text"`
	CreatedBy       *UserRef `json:"created_by"`
	CreatedAt       string   `json:"created_at"`
	Pending         bool     `json:"pending"`
	// HTMLText is the comment as Asana rich text (empty for comments posted offline).
	HTMLText string `json:"html_text,omitempty"`
}

type TaskDetail struct {
	TaskSummary
	Notes string `json:"notes"`
	// HTMLNotes is the description as Asana rich text (HTML subset); empty if unknown.
	HTMLNotes   string           `json:"html_notes,omitempty"`
	CreatedAt   string           `json:"created_at"`
	Parent      *TaskRef         `json:"parent"`
	Subtasks    []TaskSummary    `json:"subtasks"`
	Stories     []StoryView      `json:"stories"`
	Attachments []AttachmentView `json:"attachments"`
	Followers   []UserRef        `json:"followers"`
	// DetailsFetchedAt is when subtasks/stories were last fetched; empty if never.
	DetailsFetchedAt string `json:"details_fetched_at,omitempty"`
}

type TaskRef struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type SectionView struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type ProjectView struct {
	GID          string        `json:"gid"`
	Name         string        `json:"name"`
	WorkspaceGID string        `json:"workspace_gid"`
	TeamName     string        `json:"team_name,omitempty"`
	IsMember     bool          `json:"is_member"`
	Archived     bool          `json:"archived"`
	PermalinkURL string        `json:"permalink_url"`
	Sections     []SectionView `json:"sections"`
}

// --- tasks ---

// TaskFilter selects tasks from the store. Zero values mean "no filter".
type TaskFilter struct {
	WorkspaceGID string
	AssigneeGID  string
	ProjectGID   string
	SectionGID   string
	ParentGID    string
	// Completed: nil = both, true = completed only, false = incomplete only.
	Completed *bool
	DueBefore string // inclusive, YYYY-MM-DD
	DueAfter  string // inclusive, YYYY-MM-DD
	// ModifiedAfter keeps tasks modified at or after this time (RFC 3339).
	ModifiedAfter string
	// SortByModified orders by last modification, newest first (instead of due date).
	SortByModified bool
	Tag            string // tag name or gid
	// Query is a full-text search over task name, notes and comments.
	Query string
	Limit int
}

const taskColumns = `t.gid, t.name, t.resource_subtype, t.completed, COALESCE(t.completed_at, ''),
	t.due_on, COALESCE(t.due_at, ''), t.start_on, t.assignee_gid, COALESCE(u.name, ''),
	t.parent_gid, COALESCE((SELECT p.name FROM tasks p WHERE p.gid = t.parent_gid), ''),
	t.num_subtasks, t.permalink_url, t.modified_at,
	EXISTS (SELECT 1 FROM outbox o WHERE o.task_gid = t.gid), t.liked, t.num_likes`

func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]TaskSummary, error) {
	var where []string
	var args []any
	add := func(cond string, a ...any) { where = append(where, cond); args = append(args, a...) }

	if f.WorkspaceGID != "" {
		add("t.workspace_gid = ?", f.WorkspaceGID)
	}
	if f.AssigneeGID != "" {
		add("t.assignee_gid = ?", f.AssigneeGID)
	}
	if f.ProjectGID != "" {
		add("EXISTS (SELECT 1 FROM task_memberships m WHERE m.task_gid = t.gid AND m.project_gid = ?)", f.ProjectGID)
	}
	if f.SectionGID != "" {
		add("EXISTS (SELECT 1 FROM task_memberships m WHERE m.task_gid = t.gid AND m.section_gid = ?)", f.SectionGID)
	}
	if f.ParentGID != "" {
		add("t.parent_gid = ?", f.ParentGID)
	}
	if f.Completed != nil {
		add("t.completed = ?", *f.Completed)
	}
	if f.DueBefore != "" {
		add("t.due_on <> '' AND t.due_on <= ?", f.DueBefore)
	}
	if f.ModifiedAfter != "" {
		add("t.modified_at >= ?", f.ModifiedAfter)
	}
	if f.DueAfter != "" {
		add("t.due_on <> '' AND t.due_on >= ?", f.DueAfter)
	}
	if f.Tag != "" {
		add(`EXISTS (SELECT 1 FROM task_tags tt JOIN tags g ON g.gid = tt.tag_gid
			WHERE tt.task_gid = t.gid AND (g.gid = ? OR g.name = ?))`, f.Tag, f.Tag)
	}
	for _, term := range strings.Fields(f.Query) {
		cond, a := searchCondition(term)
		add(cond, a...)
	}

	q := "SELECT " + taskColumns + " FROM tasks t LEFT JOIN users u ON u.gid = t.assignee_gid"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.SortByModified {
		q += " ORDER BY t.modified_at DESC, t.name"
	} else {
		q += " ORDER BY t.completed, t.due_on = '', t.due_on, t.due_at IS NULL, t.due_at, t.name"
	}
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	return s.queryTasks(ctx, q, args...)
}

// searchCondition matches one search term against task name/notes and comment text.
// Terms of 3+ characters use the trigram FTS index; shorter ones (common in Japanese,
// e.g. "新居") use LIKE, which the trigram tokenizer cannot serve.
func searchCondition(term string) (string, []any) {
	if utf8.RuneCountInString(term) >= 3 {
		phrase := `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
		return `(t.rowid IN (SELECT rowid FROM tasks_fts WHERE tasks_fts MATCH ?)
			OR t.gid IN (SELECT s.task_gid FROM stories s WHERE s.rowid IN
				(SELECT rowid FROM stories_fts WHERE stories_fts MATCH ?)))`, []any{phrase, phrase}
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(term) + "%"
	return `(t.name LIKE ? ESCAPE '\' OR t.notes LIKE ? ESCAPE '\'
		OR EXISTS (SELECT 1 FROM stories s WHERE s.task_gid = t.gid AND s.text LIKE ? ESCAPE '\'))`,
		[]any{like, like, like}
}

func (s *Store) queryTasks(ctx context.Context, q string, args ...any) ([]TaskSummary, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []TaskSummary{}
	for rows.Next() {
		var t TaskSummary
		var assigneeGID, assigneeName string
		if err := rows.Scan(&t.GID, &t.Name, &t.ResourceSubtype, &t.Completed, &t.CompletedAt,
			&t.DueOn, &t.DueAt, &t.StartOn, &assigneeGID, &assigneeName,
			&t.ParentGID, &t.ParentName, &t.NumSubtasks, &t.PermalinkURL, &t.ModifiedAt, &t.Pending, &t.Liked, &t.NumLikes); err != nil {
			return nil, err
		}
		if assigneeGID != "" {
			t.Assignee = &UserRef{GID: assigneeGID, Name: assigneeName}
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, s.fillRelations(ctx, tasks)
}

// fillRelations loads memberships and tags for the given tasks in two queries.
func (s *Store) fillRelations(ctx context.Context, tasks []TaskSummary) error {
	if len(tasks) == 0 {
		return nil
	}
	idx := make(map[string]*TaskSummary, len(tasks))
	ids := make([]any, len(tasks))
	for i := range tasks {
		tasks[i].Memberships = []MembershipView{}
		tasks[i].Tags = []string{}
		idx[tasks[i].GID] = &tasks[i]
		ids[i] = tasks[i].GID
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"

	rows, err := s.db.QueryContext(ctx, `SELECT m.task_gid, m.project_gid, COALESCE(p.name, ''), m.section_gid, COALESCE(sec.name, '')
		FROM task_memberships m
		LEFT JOIN projects p ON p.gid = m.project_gid
		LEFT JOIN sections sec ON sec.gid = m.section_gid
		WHERE m.task_gid IN `+in+` ORDER BY p.name`, ids...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskGID string
		var m MembershipView
		if err := rows.Scan(&taskGID, &m.ProjectGID, &m.ProjectName, &m.SectionGID, &m.SectionName); err != nil {
			rows.Close()
			return err
		}
		idx[taskGID].Memberships = append(idx[taskGID].Memberships, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT tt.task_gid, g.name FROM task_tags tt JOIN tags g ON g.gid = tt.tag_gid
		WHERE tt.task_gid IN `+in+` ORDER BY g.name`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskGID, name string
		if err := rows.Scan(&taskGID, &name); err != nil {
			return err
		}
		idx[taskGID].Tags = append(idx[taskGID].Tags, name)
	}
	return rows.Err()
}

// GetTask returns a task with notes, subtasks and stories from the store.
func (s *Store) GetTask(ctx context.Context, gid string) (*TaskDetail, error) {
	tasks, err := s.queryTasks(ctx, "SELECT "+taskColumns+" FROM tasks t LEFT JOIN users u ON u.gid = t.assignee_gid WHERE t.gid = ?", gid)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("task %s: %w", gid, ErrNotFound)
	}
	d := &TaskDetail{TaskSummary: tasks[0], Subtasks: []TaskSummary{}, Stories: []StoryView{}}
	if d.Attachments, err = s.taskAttachments(ctx, gid); err != nil {
		return nil, err
	}
	if d.Followers, err = s.TaskFollowers(ctx, gid); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT t.notes, t.html_notes, t.created_at, COALESCE(d.fetched_at, '')
		FROM tasks t LEFT JOIN task_details d ON d.task_gid = t.gid WHERE t.gid = ?`, gid).
		Scan(&d.Notes, &d.HTMLNotes, &d.CreatedAt, &d.DetailsFetchedAt); err != nil {
		return nil, err
	}
	if d.ParentGID != "" {
		var name string
		err := s.db.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = ?", d.ParentGID).Scan(&name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		d.Parent = &TaskRef{GID: d.ParentGID, Name: name}
	}
	if d.Subtasks, err = s.queryTasks(ctx, "SELECT "+taskColumns+
		" FROM tasks t LEFT JOIN users u ON u.gid = t.assignee_gid WHERE t.parent_gid = ? ORDER BY t.created_at", gid); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT gid, type, resource_subtype, text, created_by_gid, created_by_name, created_at, html_text
		FROM stories WHERE task_gid = ? ORDER BY created_at`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st StoryView
		var byGID, byName string
		if err := rows.Scan(&st.GID, &st.Type, &st.ResourceSubtype, &st.Text, &byGID, &byName, &st.CreatedAt, &st.HTMLText); err != nil {
			return nil, err
		}
		if byGID != "" {
			st.CreatedBy = &UserRef{GID: byGID, Name: byName}
		}
		st.Pending = IsLocalGID(st.GID)
		d.Stories = append(d.Stories, st)
	}
	return d, rows.Err()
}

// --- projects ---

func (s *Store) ListProjects(ctx context.Context, memberOnly bool) ([]ProjectView, error) {
	q := `SELECT gid, name, workspace_gid, team_name, is_member, archived, permalink_url FROM projects`
	if memberOnly {
		q += " WHERE is_member = 1"
	}
	q += " ORDER BY name"
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	projects := []ProjectView{}
	for rows.Next() {
		var p ProjectView
		if err := rows.Scan(&p.GID, &p.Name, &p.WorkspaceGID, &p.TeamName, &p.IsMember, &p.Archived, &p.PermalinkURL); err != nil {
			rows.Close()
			return nil, err
		}
		p.Sections = []SectionView{}
		projects = append(projects, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range projects {
		secs, err := s.db.QueryContext(ctx, "SELECT gid, name FROM sections WHERE project_gid = ? ORDER BY position", projects[i].GID)
		if err != nil {
			return nil, err
		}
		for secs.Next() {
			var sec SectionView
			if err := secs.Scan(&sec.GID, &sec.Name); err != nil {
				secs.Close()
				return nil, err
			}
			projects[i].Sections = append(projects[i].Sections, sec)
		}
		secs.Close()
		if err := secs.Err(); err != nil {
			return nil, err
		}
	}
	return projects, nil
}

// ResolveProject finds a project by gid, exact name, or unique partial name.
func (s *Store) ResolveProject(ctx context.Context, arg string) (string, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, "SELECT gid FROM projects WHERE gid = ? OR name = ? LIMIT 1", arg, arg).Scan(&gid)
	if err == nil {
		return gid, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(arg) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT gid, name FROM projects WHERE name LIKE ? ESCAPE '\' LIMIT 5`, like)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var gids, names []string
	for rows.Next() {
		var g, n string
		if err := rows.Scan(&g, &n); err != nil {
			return "", err
		}
		gids, names = append(gids, g), append(names, n)
	}
	switch len(gids) {
	case 0:
		return "", fmt.Errorf("project %q: %w", arg, ErrNotFound)
	case 1:
		return gids[0], nil
	}
	return "", fmt.Errorf("project %q matches %s: %w", arg, strings.Join(names, ", "), ErrAmbiguous)
}

// ResolveSection finds a section of a project by gid or exact name.
func (s *Store) ResolveSection(ctx context.Context, projectGID, arg string) (string, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, "SELECT gid FROM sections WHERE project_gid = ? AND (gid = ? OR name = ?) LIMIT 1",
		projectGID, arg, arg).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("section %q: %w", arg, ErrNotFound)
	}
	return gid, err
}

// ResolveMove finds the project and section for moving a task. project may be empty when the
// task is in exactly one project; section is a gid or exact name.
func (s *Store) ResolveMove(ctx context.Context, taskGID, project, section string) (projectGID, sectionGID string, err error) {
	d, err := s.GetTask(ctx, taskGID)
	if err != nil {
		return "", "", err
	}
	switch {
	case project != "":
		if projectGID, err = s.ResolveProject(ctx, project); err != nil {
			return "", "", err
		}
	case len(d.Memberships) == 1:
		projectGID = d.Memberships[0].ProjectGID
	case len(d.Memberships) == 0:
		return "", "", fmt.Errorf("タスク %s はどのプロジェクトにも入っていません", taskGID)
	default:
		return "", "", fmt.Errorf("タスク %s は複数のプロジェクトに入っています。project を指定してください", taskGID)
	}
	sectionGID, err = s.ResolveSection(ctx, projectGID, section)
	return projectGID, sectionGID, err
}

// ListUsers returns known users (workspace members and people seen on tasks), me first.
func (s *Store) ListUsers(ctx context.Context) ([]UserRef, error) {
	me, _ := s.SyncState(ctx, KeyMeGID)
	rows, err := s.db.QueryContext(ctx, `SELECT gid, CASE WHEN name <> '' THEN name ELSE email END FROM users
		WHERE name <> '' OR email <> '' ORDER BY gid = ? DESC, name`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []UserRef{}
	for rows.Next() {
		var u UserRef
		if err := rows.Scan(&u.GID, &u.Name); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ResolveUser turns "me", "none"/"" (unassigned), a gid, or a unique (partial) name or
// email into a user gid ("" for unassigned).
func (s *Store) ResolveUser(ctx context.Context, arg string) (string, error) {
	switch arg {
	case "", "none":
		return "", nil
	case "me":
		return s.MeGID(ctx)
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(arg) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT gid, name FROM users
		WHERE gid = ? OR name = ? OR email = ? OR name LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\' LIMIT 5`,
		arg, arg, arg, like, like)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var gids, names []string
	for rows.Next() {
		var g, n string
		if err := rows.Scan(&g, &n); err != nil {
			return "", err
		}
		if g == arg || n == arg {
			return g, nil
		}
		gids, names = append(gids, g), append(names, n)
	}
	switch len(gids) {
	case 0:
		return "", fmt.Errorf("user %q: %w", arg, ErrNotFound)
	case 1:
		return gids[0], nil
	}
	return "", fmt.Errorf("user %q matches %s: %w", arg, strings.Join(names, ", "), ErrAmbiguous)
}

// TagCount is a tag with the number of incomplete tasks that have it.
type TagCount struct {
	GID   string `json:"gid"`
	Name  string `json:"name"`
	Open  int    `json:"open_tasks"`
	Total int    `json:"tasks"`
}

// ListTags returns the workspace tags (with how many tasks use them), ordered by name.
func (s *Store) ListTags(ctx context.Context) ([]TagCount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.gid, g.name, COALESCE(SUM(t.completed = 0), 0), COUNT(t.gid)
		FROM tags g LEFT JOIN task_tags tt ON tt.tag_gid = g.gid LEFT JOIN tasks t ON t.gid = tt.task_gid
		GROUP BY g.gid ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.GID, &t.Name, &t.Open, &t.Total); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// MeGID returns the logged-in user's gid recorded by the last sync.
// Me returns the logged-in user recorded by the last sync.
func (s *Store) Me(ctx context.Context) (UserRef, error) {
	gid, err := s.MeGID(ctx)
	if err != nil {
		return UserRef{}, err
	}
	u := UserRef{GID: gid}
	err = s.db.QueryRowContext(ctx, "SELECT name FROM users WHERE gid = ?", gid).Scan(&u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return u, err
}

// DefaultWorkspace returns the only (or first) workspace, for creating tasks outside a project.
func (s *Store) DefaultWorkspace(ctx context.Context) (string, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, "SELECT gid FROM workspaces ORDER BY name LIMIT 1").Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("ワークスペースがありません。先に `asana-tui sync` を実行してください")
	}
	return gid, err
}

// FirstSection returns the first section of a project ("" if it has none).
func (s *Store) FirstSection(ctx context.Context, projectGID string) (string, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, "SELECT gid FROM sections WHERE project_gid = ? ORDER BY position LIMIT 1", projectGID).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return gid, err
}

func (s *Store) MeGID(ctx context.Context) (string, error) {
	gid, err := s.SyncState(ctx, KeyMeGID)
	if err == nil && gid == "" {
		return "", errors.New("自分のユーザー情報がローカルストアにありません。`asana-tui sync` を実行してください")
	}
	return gid, err
}
