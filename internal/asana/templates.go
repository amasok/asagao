package asana

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/amasok/asagao/internal/asana/model"
)

// Templates (#2): listing task and project templates, making a task or a project from one,
// and duplicating a project. Making something returns an asynchronous job (Job), whose end
// is waited for with GET /jobs/{gid}.

type (
	TaskTemplate        = model.TaskTemplate
	ProjectTemplate     = model.ProjectTemplate
	TemplateDate        = model.TemplateDate
	Job                 = model.Job
	ProjectFromTemplate = model.ProjectFromTemplate
)

var (
	taskTemplateFields    = []string{"name", "project", "created_at"}
	projectTemplateFields = []string{"name", "description", "team.name", "public",
		"requested_dates.name", "requested_dates.description", "requested_roles.name"}
	jobFields = fields([]string{"status", "resource_subtype", "new_task.name", "new_project.name"})
)

// TaskTemplates lists the task templates of a project.
func (c *Client) TaskTemplates(ctx context.Context, projectGID string) ([]TaskTemplate, error) {
	q := fields(taskTemplateFields)
	q.Set("project", projectGID)
	return list[TaskTemplate](ctx, c, "/task_templates", q)
}

// ProjectTemplates lists the project templates of a team, or of a workspace when teamGID
// is "".
func (c *Client) ProjectTemplates(ctx context.Context, workspaceGID, teamGID string) ([]ProjectTemplate, error) {
	q := fields(projectTemplateFields)
	if teamGID != "" {
		q.Set("team", teamGID)
	} else {
		q.Set("workspace", workspaceGID)
	}
	return list[ProjectTemplate](ctx, c, "/project_templates", q)
}

// InstantiateTask starts making a task from a task template, named name ("" for the
// template's name).
func (c *Client) InstantiateTask(ctx context.Context, templateGID, name string) (*Job, error) {
	body := map[string]any{}
	if name != "" {
		body["name"] = name
	}
	return c.startJob(ctx, "/task_templates/"+url.PathEscape(templateGID)+"/instantiateTask", body)
}

// InstantiateProject starts making a project from a project template.
func (c *Client) InstantiateProject(ctx context.Context, templateGID string, in ProjectFromTemplate) (*Job, error) {
	body := map[string]any{"name": in.Name, "public": in.Public}
	if in.TeamGID != "" {
		body["team"] = in.TeamGID
	}
	if len(in.Dates) > 0 {
		body["requested_dates"] = gidValues(in.Dates)
	}
	if len(in.Roles) > 0 {
		body["requested_roles"] = gidValues(in.Roles)
	}
	return c.startJob(ctx, "/project_templates/"+url.PathEscape(templateGID)+"/instantiateProject", body)
}

// ProjectDuplicateIncludes is what a duplicated project copies: everything but the forms
// (a paid feature) and the other projects of its tasks (a copy is not added to them).
const ProjectDuplicateIncludes = "members,notes,task_notes,task_assignee,task_subtasks,task_attachments,task_dates,task_dependencies,task_followers,task_tags"

// DuplicateProject starts duplicating a project, named name, into team teamGID ("" for the
// team of the original).
func (c *Client) DuplicateProject(ctx context.Context, projectGID, name, teamGID string) (*Job, error) {
	body := map[string]any{"name": name, "include": ProjectDuplicateIncludes}
	if teamGID != "" {
		body["team"] = teamGID
	}
	return c.startJob(ctx, "/projects/"+url.PathEscape(projectGID)+"/duplicate", body)
}

// Job returns a job.
func (c *Client) Job(ctx context.Context, gid string) (*Job, error) {
	var j Job
	if err := c.get(ctx, "/jobs/"+url.PathEscape(gid), jobFields, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func (c *Client) startJob(ctx context.Context, path string, body map[string]any) (*Job, error) {
	var j Job
	if _, err := c.do(ctx, http.MethodPost, path, jobFields, body, &j); err != nil {
		return nil, err
	}
	if j.GID == "" && j.NewTask == nil && j.NewProject == nil {
		return nil, fmt.Errorf("asana: %s returned no job", path[strings.LastIndex(path, "/")+1:])
	}
	return &j, nil
}

// gidValues turns gid → value into [{gid, value}], in the order of the gids.
func gidValues(m map[string]string) []map[string]string {
	gids := make([]string, 0, len(m))
	for g := range m {
		gids = append(gids, g)
	}
	sort.Strings(gids)
	out := make([]map[string]string, 0, len(gids))
	for _, g := range gids {
		out = append(out, map[string]string{"gid": g, "value": m[g]})
	}
	return out
}
