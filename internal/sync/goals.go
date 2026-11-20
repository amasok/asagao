package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/plan"
)

// Goals, portfolios and time periods (#7). They are not part of a sync: they are fetched
// when shown (like a project's overview) and kept in the cache to be shown offline. A 402
// (the workspace's plan lacks them) is recorded for the workspace and hides them there for
// plan.RecheckAfter (plan.go); they are tried again after it. Any other failure
// keeps what is cached and is returned.

// FetchGoals refreshes the goals of the default workspace and its time periods. It does
// nothing while a 402 hides goals; a new 402 hides them (nil is returned: the read says
// that the plan lacks them).
func (s *Syncer) FetchGoals(ctx context.Context) error {
	if s.API == nil {
		return ErrOffline
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return err
	}
	if off, err := s.planHidden(ctx, plan.Goals, ws); err != nil || off {
		return err
	}
	goals, err := s.API.Goals(ctx, ws)
	if refused, serr := s.recordPlanRefusal(ctx, plan.Goals, ws, err); refused || serr != nil {
		return serr
	}
	if err != nil {
		return err
	}
	if err := s.clearPlanRefusal(ctx, plan.Goals, ws); err != nil {
		return err
	}
	if err := s.Store.ReplaceGoals(ctx, ws, goals); err != nil {
		return err
	}
	// The periods only name the goals' periods (a goal carries its own): a failure keeps
	// the cached ones and is returned after the goals are stored.
	var fail error
	if periods, err := s.API.TimePeriods(ctx, ws); err == nil {
		if err := s.Store.ReplaceTimePeriods(ctx, ws, periods); err != nil {
			return err
		}
	} else if !model.IsPaymentRequired(err) {
		fail = err
	}
	if err := s.Store.SetGoalsFetchedAt(ctx, ws, s.clock()); err != nil {
		return err
	}
	return fail
}

// FetchGoal refreshes one goal (with its rich notes) and what supports it. A goal Asana
// no longer has, or no longer lets me see (it answers 403 "You do not have access to this
// goal." for both), is forgotten (the error is returned).
func (s *Syncer) FetchGoal(ctx context.Context, gid string) error {
	if s.API == nil {
		return ErrOffline
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return err
	}
	if off, err := s.planHidden(ctx, plan.Goals, ws); err != nil || off {
		return err
	}
	g, err := s.API.Goal(ctx, gid)
	if refused, serr := s.recordPlanRefusal(ctx, plan.Goals, ws, err); refused || serr != nil {
		return serr
	}
	if isNotFound(err) || isForbidden(err) {
		if derr := s.Store.DeleteGoal(ctx, gid); derr != nil {
			return derr
		}
		return err
	}
	if err != nil {
		return err
	}
	if g.GID == "" {
		g.GID = gid
	}
	supports, err := s.API.GoalRelationships(ctx, gid)
	if err != nil {
		return err
	}
	if err := s.clearPlanRefusal(ctx, plan.Goals, ws); err != nil {
		return err
	}
	return s.Store.SaveGoal(ctx, *g, supports, s.clock())
}

// FetchPortfolios refreshes my portfolios of the default workspace (Asana lists only the
// portfolios one owns).
func (s *Syncer) FetchPortfolios(ctx context.Context) error {
	if s.API == nil {
		return ErrOffline
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return err
	}
	if off, err := s.planHidden(ctx, plan.Portfolios, ws); err != nil || off {
		return err
	}
	ps, err := s.API.Portfolios(ctx, ws)
	if refused, serr := s.recordPlanRefusal(ctx, plan.Portfolios, ws, err); refused || serr != nil {
		return serr
	}
	if err != nil {
		return err
	}
	if err := s.clearPlanRefusal(ctx, plan.Portfolios, ws); err != nil {
		return err
	}
	if err := s.Store.ReplacePortfolios(ctx, ws, ps); err != nil {
		return err
	}
	return s.Store.SetPortfoliosFetchedAt(ctx, ws, s.clock())
}

// FetchPortfolio refreshes one portfolio and its items. A portfolio Asana no longer has,
// or no longer lets me see, is forgotten (the error is returned).
func (s *Syncer) FetchPortfolio(ctx context.Context, gid string) error {
	if s.API == nil {
		return ErrOffline
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return err
	}
	if off, err := s.planHidden(ctx, plan.Portfolios, ws); err != nil || off {
		return err
	}
	p, err := s.API.Portfolio(ctx, gid)
	if refused, serr := s.recordPlanRefusal(ctx, plan.Portfolios, ws, err); refused || serr != nil {
		return serr
	}
	if isNotFound(err) || isForbidden(err) {
		if derr := s.Store.DeletePortfolio(ctx, gid); derr != nil {
			return derr
		}
		return err
	}
	if err != nil {
		return err
	}
	if p.GID == "" {
		p.GID = gid
	}
	items, err := s.API.PortfolioItems(ctx, gid)
	if err != nil {
		return err
	}
	if err := s.clearPlanRefusal(ctx, plan.Portfolios, ws); err != nil {
		return err
	}
	return s.Store.SavePortfolio(ctx, *p, items, s.clock())
}
