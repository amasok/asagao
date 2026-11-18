package asana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Fields requested for each resource (keep responses small; see requirements 8 章).
var (
	userFields    = []string{"name", "email", "workspaces.name"}
	projectFields = []string{"name", "archived", "color", "notes", "html_notes", "owner.name", "start_on", "due_on", "workspace", "team.name", "members.name", "permalink_url", "created_at", "modified_at"}
	sectionFields = []string{"name", "project"}
	TaskFields    = slices.Concat([]string{
		"name", "notes", "html_notes", "resource_subtype", "completed", "completed_at",
		"assignee.name", "due_on", "due_at", "start_on", "parent.name",
		"memberships.project.name", "memberships.section.name", "tags.name",
		"num_subtasks", "workspace", "permalink_url", "created_at", "modified_at",
		"followers.name", "liked", "num_likes", "created_by",
		"approval_status", "dependencies", "dependents", "assignee_section.name",
	}, customFieldValueFields)
)

func fields(f []string) url.Values {
	return url.Values{"opt_fields": {strings.Join(f, ",")}}
}

func (c *Client) Me(ctx context.Context) (*User, error) {
	var u User
	return &u, c.get(ctx, "/users/me", fields(userFields), &u)
}

func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	return list[Workspace](ctx, c, "/workspaces", fields([]string{"name", "is_organization"}))
}

func (c *Client) Projects(ctx context.Context, workspaceGID string, archived bool) ([]Project, error) {
	q := fields(projectFields)
	q.Set("workspace", workspaceGID)
	if !archived {
		q.Set("archived", "false")
	}
	return list[Project](ctx, c, "/projects", q)
}

func (c *Client) Sections(ctx context.Context, projectGID string) ([]Section, error) {
	return list[Section](ctx, c, "/projects/"+url.PathEscape(projectGID)+"/sections", fields(sectionFields))
}

func (c *Client) Tasks(ctx context.Context, tq TaskQuery) ([]Task, error) {
	return readTasks(c, false, func(f []string) ([]Task, error) { return c.tasks(ctx, tq, f) })
}

func (c *Client) tasks(ctx context.Context, tq TaskQuery, f []string) ([]Task, error) {
	q := fields(f)
	if tq.Project != "" {
		q.Set("project", tq.Project)
	} else {
		q.Set("assignee", tq.Assignee)
		q.Set("workspace", tq.Workspace)
	}
	if !tq.CompletedSince.IsZero() {
		q.Set("completed_since", tq.CompletedSince.UTC().Format(time.RFC3339))
	}
	if !tq.ModifiedSince.IsZero() {
		q.Set("modified_since", tq.ModifiedSince.UTC().Format(time.RFC3339))
	}
	return list[Task](ctx, c, "/tasks", q)
}

func (c *Client) Subtasks(ctx context.Context, taskGID string) ([]Task, error) {
	return readTasks(c, false, func(f []string) ([]Task, error) {
		return list[Task](ctx, c, "/tasks/"+url.PathEscape(taskGID)+"/subtasks", fields(f))
	})
}

var storyFields = []string{"type", "resource_subtype", "text", "html_text", "created_by.name", "created_at",
	"reaction_summary.emoji_base", "reaction_summary.variant", "reaction_summary.count", "reaction_summary.reacted"}

func (c *Client) Task(ctx context.Context, gid string) (*Task, error) {
	return c.task(ctx, gid, false)
}

func (c *Client) task(ctx context.Context, gid string, detail bool) (*Task, error) {
	ts, err := readTasks(c, detail, func(f []string) ([]Task, error) {
		var t Task
		return []Task{t}, c.get(ctx, "/tasks/"+url.PathEscape(gid), fields(f), &t)
	})
	if err != nil {
		return nil, err
	}
	return &ts[0], nil
}

// TaskDetail is Task plus the choices of the task's enum custom fields (for showing and
// editing one task; a project's choices come from its settings during sync).
func (c *Client) TaskDetail(ctx context.Context, gid string) (*Task, error) {
	return c.task(ctx, gid, true)
}

func (c *Client) Stories(ctx context.Context, taskGID string) ([]Story, error) {
	return list[Story](ctx, c, "/tasks/"+url.PathEscape(taskGID)+"/stories", fields(storyFields))
}

// --- writes ---

// UpdateTask sends changed fields (PUT /tasks/{gid}) and returns the updated task.
func (c *Client) UpdateTask(ctx context.Context, gid string, fields map[string]any) (*Task, error) {
	var t Task
	_, err := c.do(ctx, http.MethodPut, "/tasks/"+url.PathEscape(gid), fieldsQuery(c.taskFields(false)), fields, &t)
	t.Partial = c.plainTasks.Load()
	return &t, err
}

// CreateTask creates a task (POST /tasks).
func (c *Client) CreateTask(ctx context.Context, fields map[string]any) (*Task, error) {
	var t Task
	_, err := c.do(ctx, http.MethodPost, "/tasks", fieldsQuery(c.taskFields(false)), fields, &t)
	t.Partial = c.plainTasks.Load()
	return &t, err
}

// AddTaskToSection moves a task into a section (POST /sections/{gid}/addTask).
func (c *Client) AddTaskToSection(ctx context.Context, sectionGID, taskGID string) error {
	return c.InsertTaskInSection(ctx, sectionGID, taskGID, Position{})
}

// withPosition adds pos to a request body (insert_before / insert_after).
func withPosition(body map[string]any, p Position) map[string]any {
	if p.Before != "" {
		body["insert_before"] = p.Before
	}
	if p.After != "" {
		body["insert_after"] = p.After
	}
	return body
}

// InsertTaskInSection moves a task into a section at pos (POST /sections/{gid}/addTask
// with insert_before / insert_after); within its own section, it only reorders it.
func (c *Client) InsertTaskInSection(ctx context.Context, sectionGID, taskGID string, pos Position) error {
	_, err := c.do(ctx, http.MethodPost, "/sections/"+url.PathEscape(sectionGID)+"/addTask", nil, withPosition(map[string]any{"task": taskGID}, pos), nil)
	return err
}

// DeleteTask deletes a task, with its subtasks (DELETE /tasks/{gid}). Asana keeps it in
// the trash for a while.
func (c *Client) DeleteTask(ctx context.Context, taskGID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/tasks/"+url.PathEscape(taskGID), nil, nil, nil)
	return err
}

// SetParent makes a task a subtask of parentGID at pos among its subtasks, or a top-level
// task when parentGID is "" (POST /tasks/{gid}/setParent).
func (c *Client) SetParent(ctx context.Context, taskGID, parentGID string, pos Position) error {
	var parent any // null: no parent
	if parentGID != "" {
		parent = parentGID
	}
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/setParent", nil, withPosition(map[string]any{"parent": parent}, pos), nil)
	return err
}

// AddComment posts a comment (POST /tasks/{gid}/stories). If htmlText (Asana rich text) is
// set it is sent instead of text.
func (c *Client) AddComment(ctx context.Context, taskGID, text, htmlText string) (*Story, error) {
	var s Story
	body := map[string]any{"text": text}
	if htmlText != "" {
		body = map[string]any{"html_text": htmlText}
	}
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/stories", fieldsQuery(storyFields), body, &s)
	return &s, err
}

// UpdateStory changes the text of a comment (PUT /stories/{gid}). If htmlText (Asana rich
// text) is set it is sent instead of text (Asana takes only one of them).
func (c *Client) UpdateStory(ctx context.Context, gid, text, htmlText string) (*Story, error) {
	var s Story
	body := map[string]any{"text": text}
	if htmlText != "" {
		body = map[string]any{"html_text": htmlText}
	}
	_, err := c.do(ctx, http.MethodPut, "/stories/"+url.PathEscape(gid), fieldsQuery(storyFields), body, &s)
	return &s, err
}

// DeleteStory deletes a comment (DELETE /stories/{gid}); Asana lets users delete only the
// stories they created.
func (c *Client) DeleteStory(ctx context.Context, gid string) error {
	_, err := c.do(ctx, http.MethodDelete, "/stories/"+url.PathEscape(gid), nil, nil, nil)
	return err
}

// DeleteAttachment deletes an attachment (DELETE /attachments/{gid}).
func (c *Client) DeleteAttachment(ctx context.Context, gid string) error {
	_, err := c.do(ctx, http.MethodDelete, "/attachments/"+url.PathEscape(gid), nil, nil, nil)
	return err
}

func fieldsQuery(f []string) url.Values { return fields(f) }

// --- sections ---

func (c *Client) Section(ctx context.Context, gid string) (*Section, error) {
	var s Section
	return &s, c.get(ctx, "/sections/"+url.PathEscape(gid), fields(sectionFields), &s)
}

// UpdateSection renames a section (PUT /sections/{gid}).
func (c *Client) UpdateSection(ctx context.Context, gid, name string) (*Section, error) {
	var s Section
	_, err := c.do(ctx, http.MethodPut, "/sections/"+url.PathEscape(gid), fields(sectionFields), map[string]any{"name": name}, &s)
	return &s, err
}

// CreateSection creates a section in a project (POST /projects/{gid}/sections). At most
// one of before / after (a section gid) places it; with neither it goes last.
func (c *Client) CreateSection(ctx context.Context, projectGID, name, before, after string) (*Section, error) {
	body := map[string]any{"name": name}
	if before != "" {
		body["insert_before"] = before
	}
	if after != "" {
		body["insert_after"] = after
	}
	var s Section
	_, err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectGID)+"/sections", fields(sectionFields), body, &s)
	return &s, err
}

// DeleteSection deletes an empty section (DELETE /sections/{gid}).
func (c *Client) DeleteSection(ctx context.Context, gid string) error {
	_, err := c.do(ctx, http.MethodDelete, "/sections/"+url.PathEscape(gid), nil, nil, nil)
	return err
}

// InsertSection moves a section of a project just before or just after another one
// (POST /projects/{gid}/sections/insert). Exactly one of before / after is set.
func (c *Client) InsertSection(ctx context.Context, projectGID, sectionGID, before, after string) error {
	body := map[string]any{"section": sectionGID}
	if before != "" {
		body["before_section"] = before
	}
	if after != "" {
		body["after_section"] = after
	}
	_, err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectGID)+"/sections/insert", nil, body, nil)
	return err
}

// --- attachments ---

var attachmentFields = []string{"name", "resource_subtype", "host", "download_url", "view_url", "permanent_url", "size", "created_at", "parent"}

// Attachments lists the attachments of a task (GET /attachments?parent=).
func (c *Client) Attachments(ctx context.Context, parentGID string) ([]Attachment, error) {
	q := fields(attachmentFields)
	q.Set("parent", parentGID)
	return list[Attachment](ctx, c, "/attachments", q)
}

// Attachment returns one attachment with a fresh download_url.
func (c *Client) Attachment(ctx context.Context, gid string) (*Attachment, error) {
	var a Attachment
	return &a, c.get(ctx, "/attachments/"+url.PathEscape(gid), fields(attachmentFields), &a)
}

// Users lists the members of a workspace (for choosing an assignee).
func (c *Client) Users(ctx context.Context, workspaceGID string) ([]User, error) {
	q := fields([]string{"name", "email"})
	q.Set("workspace", workspaceGID)
	return list[User](ctx, c, "/users", q)
}

// CreateSubtask creates a subtask under parentGID (POST /tasks/{gid}/subtasks).
func (c *Client) CreateSubtask(ctx context.Context, parentGID string, fields map[string]any) (*Task, error) {
	var t Task
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(parentGID)+"/subtasks", fieldsQuery(c.taskFields(false)), fields, &t)
	t.Partial = c.plainTasks.Load()
	return &t, err
}

// --- tags ---

var tagFields = []string{"name", "color"}

// Tags lists the tags of a workspace.
func (c *Client) Tags(ctx context.Context, workspaceGID string) ([]Tag, error) {
	q := fields(tagFields)
	q.Set("workspace", workspaceGID)
	return list[Tag](ctx, c, "/tags", q)
}

// Tag returns one tag (GET /tags/{gid}).
func (c *Client) Tag(ctx context.Context, gid string) (*Tag, error) {
	var t Tag
	return &t, c.get(ctx, "/tags/"+url.PathEscape(gid), fields(tagFields), &t)
}

// UpdateTag changes a tag's name and/or color (PUT /tags/{gid}). changes holds "name"
// and/or "color" (a color name, or nil for none).
func (c *Client) UpdateTag(ctx context.Context, gid string, changes map[string]any) (*Tag, error) {
	var t Tag
	_, err := c.do(ctx, http.MethodPut, "/tags/"+url.PathEscape(gid), fields(tagFields), changes, &t)
	return &t, err
}

// DeleteTag deletes a tag (DELETE /tags/{gid}). Asana removes it from every task.
func (c *Client) DeleteTag(ctx context.Context, gid string) error {
	_, err := c.do(ctx, http.MethodDelete, "/tags/"+url.PathEscape(gid), nil, nil, nil)
	return err
}

// UserTaskList returns the gid of a user's My Tasks list in a workspace
// (GET /users/{gid}/user_task_list). userGID may be "me".
func (c *Client) UserTaskList(ctx context.Context, userGID, workspaceGID string) (*Ref, error) {
	q := fields([]string{"name"})
	q.Set("workspace", workspaceGID)
	var r Ref
	return &r, c.get(ctx, "/users/"+url.PathEscape(userGID)+"/user_task_list", q, &r)
}

// CreateTag creates a tag in a workspace (POST /tags).
func (c *Client) CreateTag(ctx context.Context, workspaceGID, name string) (*Ref, error) {
	var t Ref
	_, err := c.do(ctx, http.MethodPost, "/tags", fields([]string{"name"}), map[string]any{"name": name, "workspace": workspaceGID}, &t)
	return &t, err
}

// AddTag / RemoveTag change a task's tags (POST /tasks/{gid}/addTag, /removeTag).
func (c *Client) AddTag(ctx context.Context, taskGID, tagGID string) error {
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/addTag", nil, map[string]any{"tag": tagGID}, nil)
	return err
}

func (c *Client) RemoveTag(ctx context.Context, taskGID, tagGID string) error {
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/removeTag", nil, map[string]any{"tag": tagGID}, nil)
	return err
}

// --- followers, projects, duplicate, upload ---

// AddFollowers / RemoveFollowers change who follows a task.
func (c *Client) AddFollowers(ctx context.Context, taskGID string, users []string) error {
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/addFollowers", nil, map[string]any{"followers": users}, nil)
	return err
}

func (c *Client) RemoveFollowers(ctx context.Context, taskGID string, users []string) error {
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/removeFollowers", nil, map[string]any{"followers": users}, nil)
	return err
}

// AddProject adds a task to a project (optionally a section); RemoveProject removes it.
func (c *Client) AddProject(ctx context.Context, taskGID, projectGID, sectionGID string) error {
	body := map[string]any{"project": projectGID}
	if sectionGID != "" {
		body["section"] = sectionGID
	}
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/addProject", nil, body, nil)
	return err
}

func (c *Client) RemoveProject(ctx context.Context, taskGID, projectGID string) error {
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/removeProject", nil, map[string]any{"project": projectGID}, nil)
	return err
}

// DuplicateIncludes is what a duplicate copies (everything Asana supports on the free plan).
const DuplicateIncludes = "notes,assignee,subtasks,attachments,tags,followers,projects,dates,parent"

// DuplicateTask starts duplicating a task and returns the new task (the copy of
// subtasks etc. continues in the background).
func (c *Client) DuplicateTask(ctx context.Context, taskGID, name string) (*Ref, error) {
	var job struct {
		NewTask *Ref `json:"new_task"`
	}
	q := url.Values{"opt_fields": {"new_task.name"}}
	if _, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/duplicate", q,
		map[string]any{"name": name, "include": DuplicateIncludes}, &job); err != nil {
		return nil, err
	}
	if job.NewTask == nil {
		return nil, fmt.Errorf("asana: duplicate returned no new task")
	}
	return job.NewTask, nil
}

// UploadAttachment uploads a file to a task (multipart POST /attachments).
func (c *Client) UploadAttachment(ctx context.Context, parentGID, fileName string, data []byte) (*Attachment, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("parent", parentGID); err != nil {
		return nil, err
	}
	part, err := w.CreateFormFile("file", fileName)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	q := fields(attachmentFields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/attachments?"+q.Encode(), &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	c.observeDate(resp.Header, time.Now())
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, c.newError(resp.StatusCode, b)
	}
	var env struct {
		Data Attachment `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// --- projects and status updates ---

var statusFields = []string{"status_type", "title", "text", "html_text", "author.name", "created_at"}

// StatusUpdates lists a project's status updates, newest first.
func (c *Client) StatusUpdates(ctx context.Context, projectGID string) ([]StatusUpdate, error) {
	q := fields(statusFields)
	q.Set("parent", projectGID)
	return list[StatusUpdate](ctx, c, "/status_updates", q)
}

// CreateStatusUpdate posts a status update on a project.
func (c *Client) CreateStatusUpdate(ctx context.Context, projectGID, statusType, title, text, htmlText string) (*StatusUpdate, error) {
	// Asana takes only one of text and html_text ("400 Must supply only one of text or
	// html_text"), like comments.
	body := map[string]any{"parent": projectGID, "status_type": statusType, "title": title, "text": text}
	if strings.TrimSpace(htmlText) != "" { // blank HTML would lose the text
		delete(body, "text")
		body["html_text"] = htmlText
	}
	var s StatusUpdate
	_, err := c.do(ctx, http.MethodPost, "/status_updates", fields(statusFields), body, &s)
	return &s, err
}

// CreateProject creates a project in a workspace (team is needed for organizations).
func (c *Client) CreateProject(ctx context.Context, workspaceGID, teamGID, name string) (*Project, error) {
	body := map[string]any{"name": name, "workspace": workspaceGID}
	if teamGID != "" {
		body["team"] = teamGID
	}
	var p Project
	_, err := c.do(ctx, http.MethodPost, "/projects", fields(projectFields), body, &p)
	return &p, err
}

// SetProjectArchived archives or unarchives a project.
func (c *Client) SetProjectArchived(ctx context.Context, projectGID string, archived bool) (*Project, error) {
	var p Project
	_, err := c.do(ctx, http.MethodPut, "/projects/"+url.PathEscape(projectGID), fields(projectFields), map[string]any{"archived": archived}, &p)
	return &p, err
}

// AddMembers / RemoveMembers change a project's members.
func (c *Client) AddMembers(ctx context.Context, projectGID string, users []string) (*Project, error) {
	var p Project
	_, err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectGID)+"/addMembers", fields(projectFields), map[string]any{"members": strings.Join(users, ",")}, &p)
	return &p, err
}

func (c *Client) RemoveMembers(ctx context.Context, projectGID string, users []string) (*Project, error) {
	var p Project
	_, err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectGID)+"/removeMembers", fields(projectFields), map[string]any{"members": strings.Join(users, ",")}, &p)
	return &p, err
}

// Project returns one project, with a reference to its brief (if it has one).
func (c *Client) Project(ctx context.Context, gid string) (*Project, error) {
	var p Project
	return &p, c.get(ctx, "/projects/"+url.PathEscape(gid), fields(append(slices.Clone(projectFields), "project_brief")), &p)
}

// UpdateProject changes a project's fields (notes or html_notes, owner, start_on, due_on,
// color; nil clears owner and the dates).
func (c *Client) UpdateProject(ctx context.Context, gid string, changes map[string]any) (*Project, error) {
	var p Project
	_, err := c.do(ctx, http.MethodPut, "/projects/"+url.PathEscape(gid), fields(projectFields), changes, &p)
	return &p, err
}

// TaskCounts returns how many tasks a project has and how many are completed.
func (c *Client) TaskCounts(ctx context.Context, projectGID string) (*TaskCounts, error) {
	var n TaskCounts
	return &n, c.get(ctx, "/projects/"+url.PathEscape(projectGID)+"/task_counts",
		fields([]string{"num_tasks", "num_completed_tasks", "num_incomplete_tasks", "num_milestones", "num_completed_milestones"}), &n)
}

// ProjectBrief returns a project brief (a preview API: Asana may answer 402 or 403).
func (c *Client) ProjectBrief(ctx context.Context, briefGID string) (*ProjectBrief, error) {
	var b ProjectBrief
	return &b, c.get(ctx, "/project_briefs/"+url.PathEscape(briefGID), fields(briefFields), &b)
}

var briefFields = []string{"title", "text", "html_text"}

// CreateProjectBrief gives a project a brief (POST /projects/{gid}/project_briefs). The
// body is html_text (a <body> document), never text with it: Asana takes one of the two.
// A project has at most one brief; Asana refuses a second.
func (c *Client) CreateProjectBrief(ctx context.Context, projectGID, title, htmlText string) (*ProjectBrief, error) {
	var b ProjectBrief
	_, err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectGID)+"/project_briefs", fields(briefFields),
		map[string]any{"title": title, "html_text": htmlText}, &b)
	return &b, err
}

// UpdateProjectBrief changes a brief's title and/or body (PUT /project_briefs/{gid}):
// changes has "title", "html_text" or both; what it does not have is left as it is.
func (c *Client) UpdateProjectBrief(ctx context.Context, briefGID string, changes map[string]any) (*ProjectBrief, error) {
	var b ProjectBrief
	_, err := c.do(ctx, http.MethodPut, "/project_briefs/"+url.PathEscape(briefGID), fields(briefFields), changes, &b)
	return &b, err
}

// DeleteProjectBrief deletes a brief (DELETE /project_briefs/{gid}).
func (c *Client) DeleteProjectBrief(ctx context.Context, briefGID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/project_briefs/"+url.PathEscape(briefGID), nil, nil, nil)
	return err
}

// MyTeams lists the teams of an organization the logged-in user is a member of.
func (c *Client) MyTeams(ctx context.Context, workspaceGID string) ([]Team, error) {
	return list[Team](ctx, c, "/users/me/teams", url.Values{"opt_fields": {"name"}, "organization": {workspaceGID}})
}

// FavoriteProjects lists the logged-in user's favorite projects of a workspace, in the
// order the user arranged them in Asana.
func (c *Client) FavoriteProjects(ctx context.Context, workspaceGID string) ([]Ref, error) {
	return list[Ref](ctx, c, "/users/me/favorites", url.Values{"opt_fields": {"name"}, "resource_type": {"project"}, "workspace": {workspaceGID}})
}
