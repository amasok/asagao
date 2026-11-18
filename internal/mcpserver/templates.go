package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/sync"
)

// Templates (#2): listing task and project templates (read from Asana, not the cache:
// they are not cached), making a task or a project from one, and duplicating a project.
// Making something needs Asana to be reachable, like create_project.

type ListTaskTemplatesInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
}

type ListProjectTemplatesInput struct {
	Team string `json:"team,omitempty" jsonschema:"team gid or name to list only its templates (omit for the whole workspace)"`
}

type TaskTemplatesOutput struct {
	Templates []model.TaskTemplate `json:"templates"`
	Note      string               `json:"note,omitempty"`
}

type ProjectTemplatesOutput struct {
	Templates []model.ProjectTemplate `json:"templates"`
	Note      string                  `json:"note,omitempty"`
}

type TaskFromTemplateInput struct {
	Project  string `json:"project" jsonschema:"project gid or name (the one the template belongs to)"`
	Template string `json:"template" jsonschema:"task template gid or name (from list_task_templates)"`
	Name     string `json:"name,omitempty" jsonschema:"name of the new task (omit for the template's name)"`
}

type ProjectFromTemplateInput struct {
	Template string            `json:"template" jsonschema:"project template gid or name (from list_project_templates)"`
	Name     string            `json:"name" jsonschema:"name of the new project"`
	Team     string            `json:"team,omitempty" jsonschema:"team gid or name (omit for the template's team)"`
	Public   bool              `json:"public,omitempty" jsonschema:"make the project public to its team"`
	Dates    map[string]string `json:"dates,omitempty" jsonschema:"values YYYY-MM-DD for the template's requested_dates, keyed by their gid or name; dates not given are today"`
	Roles    map[string]string `json:"roles,omitempty" jsonschema:"users (\"me\", a name or gid) for the template's requested_roles, keyed by their gid or name"`
}

type DuplicateProjectInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
	Name    string `json:"name,omitempty" jsonschema:"name of the copy (omit for \"<name> のコピー\")"`
	Team    string `json:"team,omitempty" jsonschema:"team gid or name of the copy (omit for the original's team)"`
}

type MadeOutput struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
	// Pending: Asana was still filling it in (subtasks, tasks); the rest arrives with the
	// next sync.
	Pending bool   `json:"pending,omitempty"`
	Note    string `json:"note,omitempty"`
}

const noTemplatesNote = "no templates: the plan may lack them, or none were made. duplicate_project can copy an existing project instead"

func (s *Server) addTemplateReadTools(srv *mcp.Server) {
	live := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_task_templates", Annotations: live,
		Description: "List the task templates of a project (read from Asana; needs Asana to be reachable)."}, s.listTaskTemplates)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_project_templates", Annotations: live,
		Description: "List the project templates of the workspace or a team, with the dates and roles each asks for (read from Asana; needs Asana to be reachable)."}, s.listProjectTemplates)
}

func (s *Server) addTemplateWriteTools(srv *mcp.Server, write func(bool) *mcp.ToolAnnotations) {
	mcp.AddTool(srv, &mcp.Tool{Name: "create_task_from_template", Annotations: write(false),
		Description: "Create a task (with its subtasks) from a task template of a project (needs Asana to be reachable)."}, s.createTaskFromTemplate)
	mcp.AddTool(srv, &mcp.Tool{Name: "create_project_from_template", Annotations: write(false),
		Description: "Create a project (with its sections and tasks) from a project template (needs Asana to be reachable). " +
			"Give values for the template's requested dates (others are today) and roles."}, s.createProjectFromTemplate)
	mcp.AddTool(srv, &mcp.Tool{Name: "duplicate_project", Annotations: write(false),
		Description: "Duplicate a project with its tasks, subtasks, members, dates, followers, tags and attachments (needs Asana to be reachable). " +
			"Works without templates (any plan)."}, s.duplicateProject)
}

func (s *Server) onlineSyncer(ctx context.Context, what string) (*sync.Syncer, error) {
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return nil, fmt.Errorf("Asana に接続できないため%s: %w", what, err)
	}
	return syncer, nil
}

func (s *Server) listTaskTemplates(ctx context.Context, _ *mcp.CallToolRequest, in ListTaskTemplatesInput) (*mcp.CallToolResult, TaskTemplatesOutput, error) {
	gid, err := s.query().ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, TaskTemplatesOutput{}, err
	}
	syncer, err := s.onlineSyncer(ctx, "取れません")
	if err != nil {
		return nil, TaskTemplatesOutput{}, err
	}
	list, err := syncer.TaskTemplates(ctx, gid)
	if err != nil {
		return nil, TaskTemplatesOutput{}, err
	}
	out := TaskTemplatesOutput{Templates: list}
	if len(list) == 0 {
		out.Templates, out.Note = []model.TaskTemplate{}, noTemplatesNote
	}
	return nil, out, nil
}

func (s *Server) listProjectTemplates(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectTemplatesInput) (*mcp.CallToolResult, ProjectTemplatesOutput, error) {
	team := ""
	if strings.TrimSpace(in.Team) != "" {
		var err error
		if team, err = s.query().ResolveTeam(ctx, in.Team); err != nil {
			return nil, ProjectTemplatesOutput{}, fmt.Errorf("team: %w", err)
		}
	}
	syncer, err := s.onlineSyncer(ctx, "取れません")
	if err != nil {
		return nil, ProjectTemplatesOutput{}, err
	}
	list, err := syncer.ProjectTemplates(ctx, team)
	if err != nil {
		return nil, ProjectTemplatesOutput{}, err
	}
	out := ProjectTemplatesOutput{Templates: list}
	if len(list) == 0 {
		out.Templates, out.Note = []model.ProjectTemplate{}, noTemplatesNote
	}
	return nil, out, nil
}

func madeOutput(made *sync.Made, err error) (*mcp.CallToolResult, MadeOutput, error) {
	if made == nil {
		return nil, MadeOutput{}, err
	}
	out := MadeOutput{GID: made.GID, Name: made.Name, Pending: made.Pending}
	if err != nil { // made, but not stored or the job failed: say so with what was made
		out.Note = err.Error()
	} else if made.Pending {
		out.Note = "Asana is still filling it in; the rest arrives with the next sync"
	}
	return nil, out, nil
}

func (s *Server) createTaskFromTemplate(ctx context.Context, _ *mcp.CallToolRequest, in TaskFromTemplateInput) (*mcp.CallToolResult, MadeOutput, error) {
	gid, err := s.query().ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, MadeOutput{}, err
	}
	syncer, err := s.onlineSyncer(ctx, "作れません")
	if err != nil {
		return nil, MadeOutput{}, err
	}
	list, err := syncer.TaskTemplates(ctx, gid)
	if err != nil {
		return nil, MadeOutput{}, err
	}
	tmpl, err := sync.PickTemplate(list, in.Template,
		func(t model.TaskTemplate) string { return t.GID }, func(t model.TaskTemplate) string { return t.Name })
	if err != nil {
		return nil, MadeOutput{}, err
	}
	return madeOutput(syncer.InstantiateTask(ctx, tmpl, in.Name))
}

func (s *Server) createProjectFromTemplate(ctx context.Context, _ *mcp.CallToolRequest, in ProjectFromTemplateInput) (*mcp.CallToolResult, MadeOutput, error) {
	pin := sync.ProjectInput{Name: in.Name, Public: in.Public, Dates: in.Dates}
	if strings.TrimSpace(in.Team) != "" {
		var err error
		if pin.TeamGID, err = s.query().ResolveTeam(ctx, in.Team); err != nil {
			return nil, MadeOutput{}, fmt.Errorf("team: %w", err)
		}
	}
	if len(in.Roles) > 0 {
		pin.Roles = map[string]string{}
		for role, user := range in.Roles {
			gid, err := s.query().ResolveUser(ctx, user)
			if err != nil || gid == "" {
				if err == nil {
					err = fmt.Errorf("ユーザーを指定してください")
				}
				return nil, MadeOutput{}, fmt.Errorf("roles[%s]: %w", role, err)
			}
			pin.Roles[role] = gid
		}
	}
	syncer, err := s.onlineSyncer(ctx, "作れません")
	if err != nil {
		return nil, MadeOutput{}, err
	}
	list, err := syncer.ProjectTemplates(ctx, "")
	if err != nil {
		return nil, MadeOutput{}, err
	}
	tmpl, err := sync.PickTemplate(list, in.Template,
		func(t model.ProjectTemplate) string { return t.GID }, func(t model.ProjectTemplate) string { return t.Name })
	if err != nil {
		return nil, MadeOutput{}, err
	}
	return madeOutput(syncer.InstantiateProject(ctx, tmpl, pin))
}

func (s *Server) duplicateProject(ctx context.Context, _ *mcp.CallToolRequest, in DuplicateProjectInput) (*mcp.CallToolResult, MadeOutput, error) {
	gid, err := s.query().ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, MadeOutput{}, err
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		o, err := s.query().ProjectOverview(ctx, gid)
		if err != nil {
			return nil, MadeOutput{}, err
		}
		name = o.Name + " のコピー"
	}
	team := ""
	if strings.TrimSpace(in.Team) != "" {
		if team, err = s.query().ResolveTeam(ctx, in.Team); err != nil {
			return nil, MadeOutput{}, fmt.Errorf("team: %w", err)
		}
	}
	syncer, err := s.onlineSyncer(ctx, "複製できません")
	if err != nil {
		return nil, MadeOutput{}, err
	}
	return madeOutput(syncer.DuplicateProject(ctx, gid, name, team))
}
