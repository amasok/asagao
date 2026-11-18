package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/amasok/asagao/internal/readmodel"
)

// Goals, portfolios and time periods (#7), read from the local cache: those of the
// default workspace, the one they are fetched from. The entry points
// refresh them from Asana first when online (sync.FetchGoals and the like). A recent 402
// (the workspace's plan lacks them) makes these return ErrPlanUnavailable, and Available
// false, so they are not offered.

// GoalStore is the part of the local cache that goals and portfolios read.
type GoalStore interface {
	SyncState(ctx context.Context, key string) (string, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	ListGoals(ctx context.Context, workspaceGID string) ([]readmodel.GoalView, error)
	GoalDetail(ctx context.Context, gid string) (*readmodel.GoalDetail, error)
	ListTimePeriods(ctx context.Context, workspaceGID string) ([]readmodel.TimePeriodView, error)
	ResolveGoal(ctx context.Context, arg string) (string, error)
	ListPortfolios(ctx context.Context, workspaceGID string) ([]readmodel.PortfolioView, error)
	PortfolioDetail(ctx context.Context, gid string) (*readmodel.PortfolioDetail, error)
	ResolvePortfolio(ctx context.Context, arg string) (string, error)
}

// Goals reads goals and portfolios.
type Goals struct {
	Store GoalStore
	Clock Clock
}

// PeriodCurrent narrows a goal list to the periods that include today.
const PeriodCurrent = "current"

func (g *Goals) now() string {
	return g.Clock().Format("2006-01-02")
}

// plan returns ErrPlanUnavailable while a recent 402 hides feature in the default
// workspace (the one goals and portfolios are fetched from).
func (g *Goals) plan(ctx context.Context, feature string) error {
	ws, _ := g.Store.DefaultWorkspace(ctx) // none yet: nothing was refused
	return planOffered(ctx, g.Store, feature, ws, g.Clock())
}

// Available reports whether goals can be offered.
func (g *Goals) Available(ctx context.Context) bool { return g.plan(ctx, readmodel.PlanGoals) == nil }

// PortfoliosAvailable reports whether portfolios can be offered.
func (g *Goals) PortfoliosAvailable(ctx context.Context) bool {
	return g.plan(ctx, readmodel.PlanPortfolios) == nil
}

// List returns the cached goals, narrowed to a period: "" for all, PeriodCurrent for the
// periods that include today, or a period's name or gid (its goals and those of the
// periods within it, e.g. a year's quarters).
func (g *Goals) List(ctx context.Context, period string) (*readmodel.GoalList, error) {
	if err := g.plan(ctx, readmodel.PlanGoals); err != nil {
		return nil, err
	}
	ws, _ := g.Store.DefaultWorkspace(ctx) // none yet: nothing is cached
	goals, err := g.Store.ListGoals(ctx, ws)
	if err != nil {
		return nil, err
	}
	periods, err := g.Store.ListTimePeriods(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := &readmodel.GoalList{Goals: goals, Periods: periods}
	if ws != "" {
		if out.FetchedAt, err = g.Store.SyncState(ctx, readmodel.KeyGoals+ws); err != nil {
			return nil, err
		}
	}
	period = strings.TrimSpace(period)
	if period == "" {
		return out, nil
	}
	var keep func(p *readmodel.TimePeriodView) bool
	if period == PeriodCurrent {
		today := g.now()
		keep = func(p *readmodel.TimePeriodView) bool { return p != nil && p.Contains(today) }
	} else {
		sel, err := findPeriod(periods, goals, period)
		if err != nil {
			return nil, err
		}
		out.Period = sel
		keep = func(p *readmodel.TimePeriodView) bool {
			if p == nil {
				return false
			}
			if p.GID == sel.GID {
				return true
			}
			return sel.StartOn != "" && sel.EndOn != "" && p.StartOn != "" && p.EndOn != "" &&
				sel.StartOn <= p.StartOn && p.EndOn <= sel.EndOn
		}
	}
	out.Goals = []readmodel.GoalView{}
	for _, gl := range goals {
		if keep(gl.TimePeriod) {
			out.Goals = append(out.Goals, gl)
		}
	}
	return out, nil
}

// findPeriod finds the period arg names (its gid or name, ignoring case) among the cached
// periods and those the goals carry.
func findPeriod(periods []readmodel.TimePeriodView, goals []readmodel.GoalView, arg string) (*readmodel.TimePeriodView, error) {
	all := append([]readmodel.TimePeriodView(nil), periods...)
	for _, gl := range goals {
		if gl.TimePeriod != nil {
			all = append(all, *gl.TimePeriod)
		}
	}
	for _, p := range all {
		if p.GID == arg || strings.EqualFold(p.DisplayName, arg) {
			return &p, nil
		}
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range all {
		if !seen[p.DisplayName] && p.DisplayName != "" {
			seen[p.DisplayName] = true
			names = append(names, p.DisplayName)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("期間 %q はありません（期間がまだ取得されていません）", arg)
	}
	return nil, fmt.Errorf("期間 %q はありません（%s、または %s）", arg, strings.Join(names, "・"), PeriodCurrent)
}

// Get returns a cached goal (a gid, its name, or a unique part of it) and what supports it.
func (g *Goals) Get(ctx context.Context, arg string) (*readmodel.GoalDetail, error) {
	if err := g.plan(ctx, readmodel.PlanGoals); err != nil {
		return nil, err
	}
	gid, err := g.Store.ResolveGoal(ctx, arg)
	if err != nil {
		return nil, err
	}
	return g.Store.GoalDetail(ctx, gid)
}

// ResolveGoal returns the gid of the cached goal arg names.
func (g *Goals) ResolveGoal(ctx context.Context, arg string) (string, error) {
	return g.Store.ResolveGoal(ctx, arg)
}

// Portfolios returns my cached portfolios.
func (g *Goals) Portfolios(ctx context.Context) ([]readmodel.PortfolioView, error) {
	if err := g.plan(ctx, readmodel.PlanPortfolios); err != nil {
		return nil, err
	}
	ws, _ := g.Store.DefaultWorkspace(ctx)
	return g.Store.ListPortfolios(ctx, ws)
}

// PortfoliosFetchedAt says when my portfolios were last fetched ("" never).
func (g *Goals) PortfoliosFetchedAt(ctx context.Context) (string, error) {
	ws, err := g.Store.DefaultWorkspace(ctx)
	if err != nil {
		return "", nil
	}
	return g.Store.SyncState(ctx, readmodel.KeyPortfolios+ws)
}

// Portfolio returns a cached portfolio (a gid, its name, or a unique part of the name of
// one of mine) and its items.
func (g *Goals) Portfolio(ctx context.Context, arg string) (*readmodel.PortfolioDetail, error) {
	if err := g.plan(ctx, readmodel.PlanPortfolios); err != nil {
		return nil, err
	}
	gid, err := g.Store.ResolvePortfolio(ctx, arg)
	if err != nil {
		return nil, err
	}
	return g.Store.PortfolioDetail(ctx, gid)
}

// ResolvePortfolio returns the gid of the cached portfolio arg names.
func (g *Goals) ResolvePortfolio(ctx context.Context, arg string) (string, error) {
	return g.Store.ResolvePortfolio(ctx, arg)
}
