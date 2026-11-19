package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/tag"
	"github.com/amasok/asagao/internal/readmodel"
)

// Tools that change sections and tags themselves (#33). A feature Asana refused with a
// 402 (the workspace's plan lacks it) is not offered.

type CreateSectionInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
	Name    string `json:"name" jsonschema:"name of the new section"`
	Before  string `json:"before,omitempty" jsonschema:"put it just before this section (gid or exact name); omit both before and after to put it last"`
	After   string `json:"after,omitempty" jsonschema:"put it just after this section (gid or exact name)"`
}

type DeleteSectionInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
	Section string `json:"section" jsonschema:"section gid or exact name; it must hold no tasks"`
}

type MoveSectionInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
	Section string `json:"section" jsonschema:"section gid or exact name"`
	Before  string `json:"before,omitempty" jsonschema:"move it just before this section (set before or after)"`
	After   string `json:"after,omitempty" jsonschema:"move it just after this section (set before or after)"`
}

type UpdateTagInput struct {
	Tag   string  `json:"tag" jsonschema:"tag gid or exact name"`
	Name  string  `json:"name,omitempty" jsonschema:"new name (omit to keep it)"`
	Color *string `json:"color,omitempty" jsonschema:"new color: none, or one of dark-pink dark-green dark-blue dark-red dark-teal dark-brown dark-orange dark-purple dark-warm-gray light-pink light-green light-blue light-red light-teal light-yellow light-orange light-purple light-warm-gray; the names Asana now answers (red, blue, ...) are taken too and sent as the first of these with that name (omit to keep it)"`
}

type DeleteTagInput struct {
	Tag string `json:"tag" jsonschema:"tag gid or exact name"`
}

func (s *Server) addSectionTagTools(srv *mcp.Server, write func(destructive bool) *mcp.ToolAnnotations) {
	ctx := context.Background()
	if (&app.Sections{Store: s.Store, Clock: s.Now}).Available(ctx) {
		mcp.AddTool(srv, &mcp.Tool{Name: "create_section", Annotations: write(false),
			Description: "Create a section (a list/column) in a project. Works offline: tasks can be moved to it right away " +
				"with move_task (use the returned section_gid)."}, s.createSection)
		mcp.AddTool(srv, &mcp.Tool{Name: "move_section", Annotations: write(false),
			Description: "Reorder the sections of a project: move a section just before or just after another one."}, s.moveSection)
		mcp.AddTool(srv, &mcp.Tool{Name: "delete_section", Annotations: write(true),
			Description: "Delete an empty section of a project (move its tasks elsewhere first). " +
				"It cannot be undone in Asana: ask the user before deleting."}, s.deleteSection)
	}
	if (&app.Tags{Store: s.Store, Clock: s.Now}).Available(ctx) {
		mcp.AddTool(srv, &mcp.Tool{Name: "update_tag", Annotations: write(true),
			Description: "Rename a tag and/or change its color (it changes on every task that has it)."}, s.updateTag)
		mcp.AddTool(srv, &mcp.Tool{Name: "delete_tag", Annotations: write(true),
			Description: "Delete a tag: it is removed from every task. It cannot be undone in Asana: " +
				"tell the user how many tasks have it (list_tags) and ask before deleting."}, s.deleteTag)
	}
}

func (s *Server) createSection(ctx context.Context, _ *mcp.CallToolRequest, in CreateSectionInput) (*mcp.CallToolResult, WriteOutput, error) {
	sections := app.Sections{Store: s.Store, Clock: s.Now}
	g, err := sections.Create(ctx, in.Project, in.Name, app.SectionPlace{Before: in.Before, After: in.After})
	if err != nil {
		return nil, WriteOutput{}, err
	}
	out := s.push(ctx, "")
	out.SectionGID = g
	if gid.IsLocal(g) {
		if real, err := s.Store.RealSectionGID(ctx, g); err == nil {
			out.SectionGID = real
		}
	}
	return nil, out, nil
}

func (s *Server) moveSection(ctx context.Context, _ *mcp.CallToolRequest, in MoveSectionInput) (*mcp.CallToolResult, WriteOutput, error) {
	sections := app.Sections{Store: s.Store, Clock: s.Now}
	if err := sections.Reorder(ctx, in.Project, in.Section, app.SectionPlace{Before: in.Before, After: in.After}); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

func (s *Server) deleteSection(ctx context.Context, _ *mcp.CallToolRequest, in DeleteSectionInput) (*mcp.CallToolResult, WriteOutput, error) {
	sections := app.Sections{Store: s.Store, Clock: s.Now}
	if err := sections.Delete(ctx, in.Project, in.Section); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

func (s *Server) updateTag(ctx context.Context, _ *mcp.CallToolRequest, in UpdateTagInput) (*mcp.CallToolResult, WriteOutput, error) {
	tags := app.Tags{Store: s.Store, Clock: s.Now}
	t, err := tags.Resolve(ctx, in.Tag)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	changes := map[string]any{}
	if strings.TrimSpace(in.Name) != "" {
		changes["name"] = in.Name
	}
	if in.Color != nil {
		c, err := tag.NormalizeColor(*in.Color)
		if err != nil {
			return nil, WriteOutput{}, err
		}
		changes["color"] = c
	}
	if len(changes) == 0 {
		return nil, WriteOutput{}, fmt.Errorf("name か color を指定してください")
	}
	if err := tags.UpdateHere(ctx, t.GID, changes); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

func (s *Server) deleteTag(ctx context.Context, _ *mcp.CallToolRequest, in DeleteTagInput) (*mcp.CallToolResult, WriteOutput, error) {
	tags := app.Tags{Store: s.Store, Clock: s.Now}
	if err := tags.Delete(ctx, in.Tag); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

type MySectionsOutput struct {
	Sections []readmodel.SectionView `json:"sections"`
	Note     string                  `json:"note,omitempty"`
}

func (s *Server) listMySections(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, MySectionsOutput, error) {
	secs, err := (&app.Sections{Store: s.Store, Clock: s.Now}).MySections(ctx, "")
	if err != nil {
		return nil, MySectionsOutput{}, err
	}
	out := MySectionsOutput{Sections: secs}
	if len(secs) == 0 {
		out.Note = "the sections of My Tasks are not known (not synced yet, or not available on this plan)"
	}
	return nil, out, nil
}
