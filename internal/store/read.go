package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// ErrNotFound is readmodel.ErrNotFound.
var ErrNotFound = readmodel.ErrNotFound

// notFound reports a lookup's error: only a missing row (sql.ErrNoRows) is ErrNotFound;
// any other error (busy, I/O, a closed store, a cancelled context) is returned as it is,
// so that it is not shown as "not found" (#54). what names the thing looked up.
func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ErrAmbiguous is readmodel.ErrAmbiguous.
var ErrAmbiguous = readmodel.ErrAmbiguous

const KeyMeGID = readmodel.KeyMeGID

// --- views (the JSON shapes returned by the CLI and MCP; keep them stable) ---

// UserRef is readmodel.UserRef (an alias, so that internal/app can use it without
// importing the store).
type UserRef = readmodel.UserRef

// The views below are readmodel's too (aliases, stage c2 of migration-plan.md), so that
// the read ports of internal/app can use them without importing the store.
type (
	MembershipView = readmodel.MembershipView
	TaskSummary    = readmodel.TaskSummary
	StoryView      = readmodel.StoryView
	TaskDetail     = readmodel.TaskDetail
	TaskRef        = readmodel.TaskRef
	SectionView    = readmodel.SectionView
	ProjectView    = readmodel.ProjectView
)

// --- tasks ---

// TaskFilter is readmodel.TaskFilter.
type TaskFilter = readmodel.TaskFilter

const taskColumns = `t.gid, t.name, t.resource_subtype, t.completed, COALESCE(t.completed_at, ''),
	t.due_on, COALESCE(t.due_at, ''), t.start_on, t.assignee_gid, COALESCE(u.name, ''),
	t.parent_gid, COALESCE((SELECT p.name FROM tasks p WHERE p.gid = t.parent_gid), ''),
	t.num_subtasks, t.permalink_url, t.modified_at,
	EXISTS (SELECT 1 FROM outbox o WHERE o.task_gid = t.gid), t.liked, t.num_likes, t.approval_status,
	(SELECT count(*) FROM task_dependencies dp LEFT JOIN tasks bt ON bt.gid = dp.dependency_gid
		LEFT JOIN task_refs br ON br.gid = dp.dependency_gid
		WHERE dp.task_gid = t.gid AND COALESCE(bt.completed, br.completed) = 0),
	t.assignee_section_gid, COALESCE((SELECT ms.name FROM my_task_sections ms WHERE ms.gid = t.assignee_section_gid), ''),
	t.custom_type_gid, COALESCE((SELECT ct.name FROM custom_types ct WHERE ct.gid = t.custom_type_gid), ''),
	t.custom_type_status_gid, COALESCE(cs.name, ''), COALESCE(cs.color, ''), COALESCE(cs.completion_state, '')`

// customStatusJoin joins the status of a task's custom type, for taskColumns.
const customStatusJoin = " LEFT JOIN custom_type_status_options cs ON cs.gid = t.custom_type_status_gid"

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
	if f.Field != "" {
		cond, a := fieldCondition(f.Field, f.FieldValue)
		add(cond, a...)
	}
	for _, term := range strings.Fields(f.Query) {
		cond, a := searchCondition(term)
		add(cond, a...)
	}
	if f.GIDs != nil {
		if len(f.GIDs) == 0 {
			add("0")
		} else {
			a := make([]any, len(f.GIDs))
			for i, g := range f.GIDs {
				a[i] = g
			}
			add("t.gid IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.GIDs)), ",")+")", a...)
		}
	}

	add("t.gid NOT IN " + deletedTasks) // their delete is queued

	q := "SELECT " + taskColumns + " FROM tasks t LEFT JOIN users u ON u.gid = t.assignee_gid" + customStatusJoin
	// The tasks of a section, a project or a parent are in Asana's order (#51); those
	// whose place is not known come after, as before.
	var place, group string
	var orderArgs []any
	if !f.SortByModified {
		switch {
		case f.SectionGID != "":
			q += " LEFT JOIN task_memberships om ON om.task_gid = t.gid AND om.section_gid = ?"
			args = append([]any{f.SectionGID}, args...)
			place = "om.position"
		case f.ProjectGID != "":
			q += " LEFT JOIN task_memberships om ON om.task_gid = t.gid AND om.project_gid = ?" +
				" LEFT JOIN sections os ON os.gid = om.section_gid"
			args = append([]any{f.ProjectGID}, args...)
			// by section first, as soon as the project has a known place (a place counts
			// within its section only); otherwise all as before
			place, group = "om.position", `CASE WHEN EXISTS (SELECT 1 FROM task_memberships km
				WHERE km.project_gid = ? AND km.position IS NOT NULL) THEN os.position END, `
			orderArgs = []any{f.ProjectGID}
		case f.ParentGID != "":
			place = "t.subtask_position"
		}
	}
	q += " WHERE " + strings.Join(where, " AND ")
	switch {
	case f.SortByModified:
		q += " ORDER BY t.modified_at DESC, t.name"
	case place != "":
		q += " ORDER BY " + group + place + " IS NULL, " + place + ", " + listOrder
		args = append(args, orderArgs...)
	default:
		q += " ORDER BY " + listOrder
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
		var assigneeGID, assigneeName, mySection, mySectionName string
		var customType, customTypeName, customStatus, customStatusName, customStatusColor, customStatusState string
		if err := rows.Scan(&t.GID, &t.Name, &t.ResourceSubtype, &t.Completed, &t.CompletedAt,
			&t.DueOn, &t.DueAt, &t.StartOn, &assigneeGID, &assigneeName,
			&t.ParentGID, &t.ParentName, &t.NumSubtasks, &t.PermalinkURL, &t.ModifiedAt, &t.Pending, &t.Liked, &t.NumLikes,
			&t.ApprovalStatus, &t.OpenDependencies, &mySection, &mySectionName,
			&customType, &customTypeName, &customStatus, &customStatusName, &customStatusColor, &customStatusState); err != nil {
			return nil, err
		}
		if t.ResourceSubtype == task.SubtypeCustom && customType != "" {
			t.CustomType = &readmodel.CustomTypeRef{GID: customType, Name: customTypeName}
			if customStatus != "" {
				t.CustomTypeStatus = &readmodel.CustomTypeStatusView{GID: customStatus, Name: customStatusName, Color: customStatusColor,
					Complete: model.CustomTypeStatusOption{CompletionState: customStatusState}.IsComplete()}
			}
		}
		if mySection != "" && mySectionName != "" {
			t.MySection = &SectionView{GID: mySection, Name: mySectionName}
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

// fillRelations loads memberships and tags for the given tasks in two queries per batch.
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
	// in batches: SQLite limits the number of variables in a statement
	for len(ids) > 0 {
		n := min(len(ids), relationBatch)
		if err := s.fillRelationsBatch(ctx, idx, ids[:n]); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// relationBatch is how many tasks fillRelations loads per query (well below SQLite's
// limit on variables, which is 999 in older builds).
const relationBatch = 500

func (s *Store) fillRelationsBatch(ctx context.Context, idx map[string]*TaskSummary, ids []any) error {
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
	for rows.Next() {
		var taskGID, name string
		if err := rows.Scan(&taskGID, &name); err != nil {
			rows.Close()
			return err
		}
		idx[taskGID].Tags = append(idx[taskGID].Tags, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return s.fillCustomFields(ctx, idx, in, ids)
}

// GetTask returns a task with notes, subtasks and stories from the store.
func (s *Store) GetTask(ctx context.Context, gid string) (*TaskDetail, error) {
	tasks, err := s.queryTasks(ctx, "SELECT "+taskColumns+" FROM tasks t LEFT JOIN users u ON u.gid = t.assignee_gid"+customStatusJoin+" WHERE t.gid = ?", gid)
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
	if d.CustomFields, err = s.taskCustomFields(ctx, gid); err != nil {
		return nil, err
	}
	var projects []string
	for _, m := range d.Memberships {
		projects = append(projects, m.ProjectGID)
	}
	if d.CustomTypes, err = s.CustomTypes(ctx, projects); err != nil {
		return nil, err
	}
	if d.Dependencies, err = s.taskDependencies(ctx, gid, false); err != nil {
		return nil, err
	}
	if d.Dependents, err = s.taskDependencies(ctx, gid, true); err != nil {
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
		" FROM tasks t LEFT JOIN users u ON u.gid = t.assignee_gid"+customStatusJoin+" WHERE t.parent_gid = ? AND t.gid NOT IN "+deletedTasks+
		" ORDER BY t.subtask_position IS NULL, t.subtask_position, "+subtaskOrder, gid); err != nil {
		return nil, err
	}

	// A comment whose edit is not sent yet is pending too (#34); a failed edit is not
	// applied (reapplyStoryOps), so its comment shows Asana's text and is not.
	rows, err := s.db.QueryContext(ctx, `SELECT gid, type, resource_subtype, text, created_by_gid, created_by_name, created_at, html_text,
		reactions, EXISTS (SELECT 1 FROM outbox o WHERE o.kind = ? AND o.state <> 'failed' AND `+resolvedSQL("story", ResultStory)+` = stories.gid)
		FROM stories WHERE task_gid = ? ORDER BY created_at`, OpUpdateStory, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st StoryView
		var byGID, byName, reactions string
		var edited bool
		if err := rows.Scan(&st.GID, &st.Type, &st.ResourceSubtype, &st.Text, &byGID, &byName, &st.CreatedAt, &st.HTMLText,
			&reactions, &edited); err != nil {
			return nil, err
		}
		if byGID != "" {
			st.CreatedBy = &UserRef{GID: byGID, Name: byName}
		}
		if reactions != "" {
			// Emoji and names come from Asana: termsafe.Unmarshal drops control characters.
			if err := termsafe.Unmarshal([]byte(reactions), &st.Reactions); err != nil {
				return nil, fmt.Errorf("reactions of comment %s: %w", st.GID, err)
			}
		}
		st.Pending = IsLocalGID(st.GID) || edited
		d.Stories = append(d.Stories, st)
	}
	return d, rows.Err()
}

// --- projects ---

func (s *Store) ListProjects(ctx context.Context, memberOnly bool) ([]ProjectView, error) {
	// Favorites first, in the user's order; then by team (projects without a team last).
	q := `SELECT gid, name, projects.workspace_gid, team_gid, team_name, f.project_gid IS NOT NULL, is_member, archived, permalink_url,
		COALESCE((SELECT status_type FROM status_updates su WHERE su.project_gid = projects.gid ORDER BY created_at DESC LIMIT 1), ''),
		` + fetchedSQL + `
		FROM projects LEFT JOIN project_favorites f ON f.project_gid = projects.gid WHERE archived = 0`
	if memberOnly {
		q += " AND is_member = 1"
	}
	q += " ORDER BY f.project_gid IS NULL, f.position, team_name = '', team_name, team_gid, name"
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	projects := []ProjectView{}
	for rows.Next() {
		var p ProjectView
		var fetched bool
		if err := rows.Scan(&p.GID, &p.Name, &p.WorkspaceGID, &p.TeamGID, &p.TeamName, &p.Favorite, &p.IsMember, &p.Archived, &p.PermalinkURL, &p.Status, &fetched); err != nil {
			rows.Close()
			return nil, err
		}
		p.Sections = []SectionView{}
		p.Unfetched = !fetched
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
	if projectGID, err = s.resolveMoveProject(ctx, taskGID, project); err != nil {
		return "", "", err
	}
	sectionGID, err = s.ResolveSection(ctx, projectGID, section)
	return projectGID, sectionGID, err
}

// resolveMoveProject picks the project of task taskGID to move it in: project (a name or
// gid), or the only project the task is in.
func (s *Store) resolveMoveProject(ctx context.Context, taskGID, project string) (projectGID string, err error) {
	d, err := s.GetTask(ctx, taskGID)
	if err != nil {
		return "", err
	}
	switch {
	case project != "":
		return s.ResolveProject(ctx, project)
	case len(d.Memberships) == 1:
		return d.Memberships[0].ProjectGID, nil
	case len(d.Memberships) == 0:
		return "", fmt.Errorf("タスク %s はどのプロジェクトにも入っていません", taskGID)
	default:
		return "", fmt.Errorf("タスク %s は複数のプロジェクトに入っています。project を指定してください", taskGID)
	}
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

// UserContact is readmodel.UserContact.
type UserContact = readmodel.UserContact

// ListUserContacts returns known users with their name and email, me first.
func (s *Store) ListUserContacts(ctx context.Context) ([]UserContact, error) {
	me, _ := s.SyncState(ctx, KeyMeGID)
	rows, err := s.db.QueryContext(ctx, `SELECT gid, name, email FROM users
		WHERE name <> '' OR email <> '' ORDER BY gid = ? DESC, name`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []UserContact{}
	for rows.Next() {
		var u UserContact
		if err := rows.Scan(&u.GID, &u.Name, &u.Email); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ResolveUser turns "me", "none"/"" (unassigned), a gid, or a unique (partial) name or
// email into a user gid ("" for unassigned). A numeric gid of a user not in the store yet
// is passed through as is.
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
	switch {
	case len(gids) == 0 && isGID(arg):
		return arg, nil
	case len(gids) == 0:
		return "", fmt.Errorf("user %q: %w", arg, ErrNotFound)
	case len(gids) == 1:
		return gids[0], nil
	}
	return "", fmt.Errorf("user %q matches %s: %w", arg, strings.Join(names, ", "), ErrAmbiguous)
}

// TagCount is readmodel.TagCount.
type TagCount = readmodel.TagCount

// ListTags returns the workspace tags (with how many tasks use them), ordered by name.
func (s *Store) ListTags(ctx context.Context) ([]TagCount, error) {
	return s.queryTags(ctx, "")
}

// GetTag returns one tag of the cache (with how many tasks use it).
func (s *Store) GetTag(ctx context.Context, gid string) (TagCount, error) {
	tags, err := s.queryTags(ctx, "WHERE g.gid = ?", gid)
	if err != nil {
		return TagCount{}, err
	}
	if len(tags) == 0 {
		return TagCount{}, fmt.Errorf("tag %s: %w", gid, ErrNotFound)
	}
	return tags[0], nil
}

func (s *Store) queryTags(ctx context.Context, where string, args ...any) ([]TagCount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.gid, g.name, g.color, COALESCE(SUM(t.completed = 0), 0), COUNT(t.gid)
		FROM tags g LEFT JOIN task_tags tt ON tt.tag_gid = g.gid LEFT JOIN tasks t ON t.gid = tt.task_gid
		`+where+` GROUP BY g.gid ORDER BY g.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.GID, &t.Name, &t.Color, &t.Open, &t.Total); err != nil {
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
		return "", errors.New("ワークスペースがありません。先に `asagao sync` を実行してください")
	}
	return gid, err
}

// ProjectWorkspace returns the workspace of a cached project ("" if it is not cached).
func (s *Store) ProjectWorkspace(ctx context.Context, projectGID string) (string, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, "SELECT workspace_gid FROM projects WHERE gid = ?", projectGID).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
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
		return "", errors.New("自分のユーザー情報がローカルストアにありません。`asagao sync` を実行してください")
	}
	return gid, err
}

// isGID reports whether s looks like an Asana gid (digits only).
func isGID(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
