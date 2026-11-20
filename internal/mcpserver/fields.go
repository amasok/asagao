package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Custom fields of projects (#37): adding a field of the workspace to a project, or
// removing it. Queued like any change, and sent now if Asana is reachable.

type UpdateProjectFieldsInput struct {
	Project string   `json:"project" jsonschema:"project gid or name"`
	Add     []string `json:"add,omitempty" jsonschema:"names or gids of the workspace's custom fields to add to the project (see get_project available_custom_fields)"`
	Remove  []string `json:"remove,omitempty" jsonschema:"names or gids of the project's custom fields to remove (the project's tasks lose the field and its values: ask the user first)"`
}

func (s *Server) addFieldTools(srv *mcp.Server, write func(destructive bool) *mcp.ToolAnnotations) {
	mcp.AddTool(srv, &mcp.Tool{Name: "update_project_custom_fields", Annotations: write(true),
		Description: "Add custom fields of the workspace to a project, or remove them from it (queued, sent now if Asana is reachable). " +
			"get_project with include_available_fields lists the fields that can be added. Removing takes the field and its values " +
			"off the project's tasks: ask the user before removing. To add an option to an enum field, use update_task with add_missing_options."},
		s.updateProjectFields)
}

func (s *Server) updateProjectFields(ctx context.Context, _ *mcp.CallToolRequest, in UpdateProjectFieldsInput) (*mcp.CallToolResult, ProjectWriteOutput, error) {
	if len(in.Add)+len(in.Remove) == 0 {
		return nil, ProjectWriteOutput{}, errors.New("nothing to update: pass add or remove")
	}
	gid, err := s.query().ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, ProjectWriteOutput{}, err
	}
	pf := s.App.ProjectFields(s.Now)
	if err := pf.Changeable(ctx, gid); err != nil {
		return nil, ProjectWriteOutput{}, err
	}
	// Resolve every name before changing anything.
	type change struct {
		gid string
		add bool
	}
	var changes []change
	for _, c := range []struct {
		names []string
		add   bool
		arg   string
	}{{in.Add, true, "add"}, {in.Remove, false, "remove"}} {
		for _, name := range c.names {
			f, err := pf.Store.ResolveProjectField(ctx, gid, name)
			if err != nil {
				return nil, ProjectWriteOutput{}, fmt.Errorf("%s: %w", c.arg, err)
			}
			if f.Attached == c.add {
				return nil, ProjectWriteOutput{}, fmt.Errorf("%s: the project %s the custom field %q", c.arg,
					map[bool]string{true: "already has", false: "does not have"}[c.add], f.Name)
			}
			changes = append(changes, change{f.GID, c.add})
		}
	}
	for _, c := range changes {
		if err := pf.SetHere(ctx, gid, c.gid, c.add); err != nil {
			return nil, ProjectWriteOutput{}, err
		}
	}
	w := s.push(ctx, "")
	return nil, ProjectWriteOutput{ProjectGID: gid, Sent: &w.Sent, Note: w.Note}, nil
}
