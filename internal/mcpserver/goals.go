package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// Goals, portfolios and time periods (#7): read only. They are fetched from Asana when
// asked for (online) and read from the cache. A feature Asana refused with a 402 (the
// workspace's plan lacks it) is not offered.

type ListGoalsInput struct {
	Period string `json:"period,omitempty" jsonschema:"narrow to a time period: its name (e.g. Q1 FY26) or gid, which includes the periods within it (a year's quarters), or current for the periods that include today; omit for all goals"`
}

type GoalsOutput struct {
	readmodel.GoalList
	Note string `json:"note,omitempty"`
}

type GoalInput struct {
	Goal string `json:"goal" jsonschema:"goal gid or name"`
}

type GoalOutput struct {
	Goal readmodel.GoalDetail `json:"goal"`
	// Markdown is the goal's description as Markdown.
	Markdown string `json:"markdown"`
	Note     string `json:"note,omitempty"`
}

type PortfoliosOutput struct {
	Portfolios []readmodel.PortfolioView `json:"portfolios"`
	Note       string                    `json:"note,omitempty"`
}

type PortfolioInput struct {
	Portfolio string `json:"portfolio" jsonschema:"portfolio gid or name"`
}

type PortfolioOutput struct {
	Portfolio readmodel.PortfolioDetail `json:"portfolio"`
	Note      string                    `json:"note,omitempty"`
}

func (s *Server) goals() *app.Goals { return s.App.Goals(s.Now) }

func (s *Server) addGoalTools(srv *mcp.Server) {
	ctx := context.Background()
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	if s.goals().Available(ctx) {
		mcp.AddTool(srv, &mcp.Tool{Name: "list_goals", Annotations: readOnly,
			Description: "List the workspace's goals: status (green/yellow/red while open; achieved, partial, missed or dropped once closed), " +
				"progress (metric), time period, owner. Also lists the time periods (fiscal years, halves, quarters) to narrow by. " +
				"A metric of kind percentage keeps its values as fractions (target 1 is 100%)."}, s.listGoals)
		mcp.AddTool(srv, &mcp.Tool{Name: "get_goal", Annotations: readOnly,
			Description: "Get a goal: its description (Markdown), status, progress, time period, owner, " +
				"and what supports it (subgoals, projects, portfolios, tasks). " +
				"A metric of kind percentage keeps its values as fractions (target 1 is 100%)."}, s.getGoal)
	}
	if s.goals().PortfoliosAvailable(ctx) {
		mcp.AddTool(srv, &mcp.Tool{Name: "list_portfolios", Annotations: readOnly,
			Description: "List my portfolios (Asana's API shows only the portfolios I own) with their latest status."}, s.listPortfolios)
		mcp.AddTool(srv, &mcp.Tool{Name: "get_portfolio", Annotations: readOnly,
			Description: "Get a portfolio: the projects and portfolios in it, each with its latest status, owner and due date. " +
				"Projects can then be read with get_project."}, s.getPortfolio)
	}
}

// refresh runs a fetch from Asana when online, and says what failed ("" for nothing).
func (s *Server) refresh(ctx context.Context, f func(*sync.Syncer) error) string {
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return ""
	}
	if err := f(syncer); err != nil {
		return "from the local cache: " + err.Error()
	}
	return ""
}

func (s *Server) listGoals(ctx context.Context, _ *mcp.CallToolRequest, in ListGoalsInput) (*mcp.CallToolResult, GoalsOutput, error) {
	var out GoalsOutput
	out.Note = s.refresh(ctx, func(sy *sync.Syncer) error { return sy.FetchGoals(ctx) })
	l, err := s.goals().List(ctx, in.Period)
	if err != nil {
		return nil, out, err
	}
	out.GoalList = *l
	if len(l.Goals) == 0 && l.FetchedAt == "" && out.Note == "" {
		out.Note = "goals were not fetched yet (Asana could not be reached)"
	}
	return nil, out, nil
}

// fetchOne refreshes one goal or portfolio named arg: from the cache's name, or after
// fetching the list when it is not cached, or as a gid.
func (s *Server) fetchOne(ctx context.Context, arg string, resolve func(context.Context, string) (string, error),
	list func(*sync.Syncer) error, one func(*sync.Syncer, string) error) string {
	return s.refresh(ctx, func(sy *sync.Syncer) error {
		var fail error
		gid, err := resolve(ctx, arg)
		if errors.Is(err, readmodel.ErrNotFound) {
			fail = list(sy)
			if gid, err = resolve(ctx, arg); errors.Is(err, readmodel.ErrNotFound) && isDigits(arg) {
				gid, err = arg, nil
			}
		}
		if err != nil {
			return fail
		}
		if err := one(sy, gid); err != nil {
			return err
		}
		return fail
	})
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func (s *Server) getGoal(ctx context.Context, _ *mcp.CallToolRequest, in GoalInput) (*mcp.CallToolResult, GoalOutput, error) {
	var out GoalOutput
	goals := s.goals()
	out.Note = s.fetchOne(ctx, in.Goal, goals.ResolveGoal,
		func(sy *sync.Syncer) error { return sy.FetchGoals(ctx) },
		func(sy *sync.Syncer, gid string) error { return sy.FetchGoal(ctx, gid) })
	g, err := goals.Get(ctx, in.Goal)
	if err != nil {
		if out.Note != "" && errors.Is(err, readmodel.ErrNotFound) {
			return nil, out, fmt.Errorf("%w (%s)", err, out.Note)
		}
		return nil, out, err
	}
	out.Goal = *g
	out.Markdown = editableMarkdown(g.HTMLNotes, g.Notes)
	out.Goal.HTMLNotes = ""
	return nil, out, nil
}

func (s *Server) listPortfolios(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, PortfoliosOutput, error) {
	var out PortfoliosOutput
	out.Note = s.refresh(ctx, func(sy *sync.Syncer) error { return sy.FetchPortfolios(ctx) })
	ps, err := s.goals().Portfolios(ctx)
	if err != nil {
		return nil, out, err
	}
	out.Portfolios = ps
	return nil, out, nil
}

func (s *Server) getPortfolio(ctx context.Context, _ *mcp.CallToolRequest, in PortfolioInput) (*mcp.CallToolResult, PortfolioOutput, error) {
	var out PortfolioOutput
	goals := s.goals()
	out.Note = s.fetchOne(ctx, in.Portfolio, goals.ResolvePortfolio,
		func(sy *sync.Syncer) error { return sy.FetchPortfolios(ctx) },
		func(sy *sync.Syncer, gid string) error { return sy.FetchPortfolio(ctx, gid) })
	p, err := goals.Portfolio(ctx, in.Portfolio)
	if err != nil {
		if out.Note != "" && errors.Is(err, readmodel.ErrNotFound) {
			return nil, out, fmt.Errorf("%w (%s)", err, out.Note)
		}
		return nil, out, err
	}
	out.Portfolio = *p
	return nil, out, nil
}
