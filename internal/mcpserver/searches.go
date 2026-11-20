package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// The saved searches (searches.toml) are read here, not written: saving and removing them
// is the user's, from the CLI (asagao searches save / rm).

type SavedSearchesOutput struct {
	Searches []readmodel.SavedSearch `json:"searches"`
}

type RunSavedSearchInput struct {
	Name  string `json:"name" jsonschema:"the saved search's name (exactly, as list_saved_searches shows it)"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of tasks (default: the search's own limit, or 100; at most 500)"`
}

type RunSavedSearchOutput struct {
	Search readmodel.SavedSearch `json:"search"`
	TasksOutput
}

func (s *Server) searches() app.Searches { return app.Searches{File: s.Searches} }

func (s *Server) addSearchTools(srv *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_saved_searches", Annotations: readOnly,
		Description: "List the user's saved searches (name and conditions, the same as list_tasks/search_tasks arguments; " +
			"dates may be relative and are read on the day the search runs). The user saves them with `asagao searches save`."}, s.listSavedSearches)
	mcp.AddTool(srv, &mcp.Tool{Name: "run_saved_search", Annotations: readOnly,
		Description: "Run a saved search by name on the local cache. A search with a query runs like search_tasks " +
			"(everyone's tasks, completed ones too); one without runs like list_tasks (my incomplete tasks, everyone's in a project)."}, s.runSavedSearch)
}

func (s *Server) listSavedSearches(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, SavedSearchesOutput, error) {
	ss, err := s.searches().List()
	if ss == nil {
		ss = []readmodel.SavedSearch{}
	}
	return nil, SavedSearchesOutput{Searches: ss}, err
}

func (s *Server) runSavedSearch(ctx context.Context, _ *mcp.CallToolRequest, in RunSavedSearchInput) (*mcp.CallToolResult, RunSavedSearchOutput, error) {
	if in.Name == "" {
		return nil, RunSavedSearchOutput{}, errors.New("name is required")
	}
	x, err := s.searches().Get(in.Name)
	if err != nil {
		return nil, RunSavedSearchOutput{}, err
	}
	spec := x.Spec()
	if in.Limit > 0 {
		spec.Limit = in.Limit
	}
	spec.Limit = clampLimit(spec.Limit, 100)
	f, err := s.query().SavedSearchFilter(ctx, x.Name, spec, s.Now())
	if err != nil {
		return nil, RunSavedSearchOutput{}, err
	}
	tasks, err := s.query().ListTasks(ctx, f)
	return nil, RunSavedSearchOutput{Search: x, TasksOutput: TasksOutput{Tasks: tasks, Count: len(tasks)}}, err
}
