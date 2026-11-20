package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/richtext"
)

// A project's brief (#60): get_project_brief reads it as editable Markdown, whose tables
// and embeds are asana-html blocks to keep as they are; update_project_brief creates,
// replaces or deletes it through the outbox.

func (s *Server) addBriefTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "get_project_brief", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Get a project's brief (its overview page): the title and the body as Markdown. Tables and embeds " +
			"(external media, milestones, goals) are fenced ```asana-html blocks holding Asana's HTML: keep them as they " +
			"are when the body is given back to update_project_brief (the text in a table's cells may be edited there). " +
			"state is none (no brief), available, unavailable (Asana does not give it here) or empty (not fetched)."}, s.getBrief)
}

func (s *Server) addBriefWriteTools(srv *mcp.Server, write func(destructive bool) *mcp.ToolAnnotations) {
	mcp.AddTool(srv, &mcp.Tool{Name: "update_project_brief", Annotations: write(true),
		Description: "Create or change a project's brief (title and/or the whole Markdown body, as get_project_brief gives it), " +
			"or delete it (delete: true; ask the user first, it cannot be undone once sent). Queued, sent now if Asana is " +
			"reachable; a brief changed in Asana meanwhile becomes a conflict (sync_status.outbox, resolve_outbox)."}, s.updateBrief)
}

type BriefInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
}

type BriefDetailOutput struct {
	ProjectGID string `json:"project_gid"`
	State      string `json:"state"`
	Title      string `json:"title,omitempty"`
	Markdown   string `json:"markdown,omitempty"`
	// Pending: a change to the brief is queued and not sent yet.
	Pending bool   `json:"pending,omitempty"`
	Note    string `json:"note,omitempty"`
}

func (s *Server) briefs() *app.Briefs {
	return s.App.Briefs(s.Now)
}

func (s *Server) getBrief(ctx context.Context, _ *mcp.CallToolRequest, in BriefInput) (*mcp.CallToolResult, BriefDetailOutput, error) {
	var out BriefDetailOutput
	gid, err := s.query().ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, out, err
	}
	if syncer, err := s.getSyncer(ctx); err == nil {
		if err := syncer.FetchProjectOverview(ctx, gid); err != nil {
			out.Note = "from the local cache: " + err.Error()
		}
	}
	o, err := s.query().ProjectOverview(ctx, gid)
	if err != nil {
		return nil, out, err
	}
	out.ProjectGID, out.State, out.Pending = gid, o.BriefState, o.BriefPending
	if o.Brief != nil {
		out.Title, out.Markdown = o.Brief.Title, briefMarkdown(o.Brief.HTMLText, o.Brief.Text)
	}
	return nil, out, nil
}

// briefMarkdown is a brief's body as editable Markdown (asana-html blocks kept).
func briefMarkdown(htmlText, text string) string {
	if htmlText != "" {
		return richtext.BriefToEditableMarkdown(htmlText, nil)
	}
	return editableMarkdown("", text)
}

type UpdateBriefInput struct {
	Project  string  `json:"project" jsonschema:"project gid or name"`
	Title    *string `json:"title,omitempty" jsonschema:"the brief's title"`
	Markdown *string `json:"markdown,omitempty" jsonschema:"the whole body in Markdown (replaces it); keep the asana-html blocks of get_project_brief"`
	Delete   bool    `json:"delete,omitempty" jsonschema:"true deletes the brief (ask the user first)"`
}

func (s *Server) updateBrief(ctx context.Context, _ *mcp.CallToolRequest, in UpdateBriefInput) (*mcp.CallToolResult, ProjectWriteOutput, error) {
	briefs := s.briefs()
	if in.Delete {
		if in.Title != nil || in.Markdown != nil {
			return nil, ProjectWriteOutput{}, errors.New("delete cannot be given with title or markdown")
		}
		gid, err := briefs.Delete(ctx, in.Project)
		if err != nil {
			return nil, ProjectWriteOutput{}, err
		}
		w := s.push(ctx, "")
		return nil, ProjectWriteOutput{ProjectGID: gid, Sent: &w.Sent, Note: w.Note}, nil
	}
	if in.Title == nil && in.Markdown == nil {
		return nil, ProjectWriteOutput{}, errors.New("nothing to update: pass title, markdown or delete")
	}
	gid, changed, err := briefs.Save(ctx, in.Project, app.BriefInput{Title: in.Title, Markdown: in.Markdown})
	if err != nil {
		var fe *app.FieldError
		if errors.As(err, &fe) {
			err = errors.New("markdown: " + fe.Err.Error())
		}
		return nil, ProjectWriteOutput{}, err
	}
	if !changed {
		return nil, ProjectWriteOutput{ProjectGID: gid, Note: "no change"}, nil
	}
	w := s.push(ctx, "")
	return nil, ProjectWriteOutput{ProjectGID: gid, Sent: &w.Sent, Note: w.Note}, nil
}
