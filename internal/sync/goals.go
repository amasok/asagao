package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

// Goals, portfolios and time periods (#7). They are not part of a sync: they are fetched
// when shown (like a project's overview) and kept in the cache to be shown offline. A 402
// (the workspace's plan lacks them) is recorded as readmodel.KeyPlanUnavailable and hides
// them for readmodel.PlanRecheckAfter; they are tried again after it. Any other failure
// keeps what is cached and is returned.

// goalPlanOff reports whether feature is hidden by a recent 402.
func (s *Syncer) goalPlanOff(ctx context.Context, feature string) (bool, error) {
	v, err := s.Store.SyncState(ctx, readmodel.KeyPlanUnavailable+feature)
	if err != nil {
		return false, err
	}
	return readmodel.PlanOffFor(v, s.clock()), nil
}

// goalPlanRefused records a 402 for feature when err is one, and says whether it was.
func (s *Syncer) goalPlanRefused(ctx context.Context, feature string, err error) (bool, error) {
	if !model.IsPaymentRequired(err) {
		return false, nil
	}
	return true, s.Store.SetSyncState(ctx, readmodel.KeyPlanUnavailable+feature, s.clock().UTC().Format(time.RFC3339))
}

// goalPlanGranted forgets an earlier 402 for feature (Asana answered).
func (s *Syncer) goalPlanGranted(ctx context.Context, feature string) error {
	v, err := s.Store.SyncState(ctx, readmodel.KeyPlanUnavailable+feature)
	if err != nil || v == "" {
		return err
	}
	return s.Store.SetSyncState(ctx, readmodel.KeyPlanUnavailable+feature, "")
}

// FetchGoals refreshes the goals of the default workspace and its time periods. It does
// nothing while a 402 hides goals; a new 402 hides them (nil is returned: the read says
// that the plan lacks them).
func (s *Syncer) FetchGoals(ctx context.Context) error {
	if s.API == nil {
		return ErrOffline
	}
	if off, err := s.goalPlanOff(ctx, readmodel.PlanGoals); err != nil || off {
		return err
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return err
	}
	goals, err := s.API.Goals(ctx, ws)
	if refused, serr := s.goalPlanRefused(ctx, readmodel.PlanGoals, err); refused || serr != nil {
		return serr
	}
	if err != nil {
		return err
	}
	if err := s.goalPlanGranted(ctx, readmodel.PlanGoals); err != nil {
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
	if err := s.Store.SetSyncState(ctx, readmodel.KeyGoals+ws, s.clock().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return fail
}

// FetchGoal refreshes one goal (with its rich notes) and what supports it. A goal Asana
// no longer has is forgotten (the error is returned).
func (s *Syncer) FetchGoal(ctx context.Context, gid string) error {
	if s.API == nil {
		return ErrOffline
	}
	if off, err := s.goalPlanOff(ctx, readmodel.PlanGoals); err != nil || off {
		return err
	}
	g, err := s.API.Goal(ctx, gid)
	if refused, serr := s.goalPlanRefused(ctx, readmodel.PlanGoals, err); refused || serr != nil {
		return serr
	}
	if isNotFound(err) {
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
	if err := s.goalPlanGranted(ctx, readmodel.PlanGoals); err != nil {
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
	if off, err := s.goalPlanOff(ctx, readmodel.PlanPortfolios); err != nil || off {
		return err
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return err
	}
	ps, err := s.API.Portfolios(ctx, ws)
	if refused, serr := s.goalPlanRefused(ctx, readmodel.PlanPortfolios, err); refused || serr != nil {
		return serr
	}
	if err != nil {
		return err
	}
	if err := s.goalPlanGranted(ctx, readmodel.PlanPortfolios); err != nil {
		return err
	}
	if err := s.Store.ReplacePortfolios(ctx, ws, ps); err != nil {
		return err
	}
	return s.Store.SetSyncState(ctx, readmodel.KeyPortfolios+ws, s.clock().UTC().Format(time.RFC3339))
}

// FetchPortfolio refreshes one portfolio and its items. A portfolio Asana no longer has
// is forgotten (the error is returned).
func (s *Syncer) FetchPortfolio(ctx context.Context, gid string) error {
	if s.API == nil {
		return ErrOffline
	}
	if off, err := s.goalPlanOff(ctx, readmodel.PlanPortfolios); err != nil || off {
		return err
	}
	p, err := s.API.Portfolio(ctx, gid)
	if refused, serr := s.goalPlanRefused(ctx, readmodel.PlanPortfolios, err); refused || serr != nil {
		return serr
	}
	if isNotFound(err) {
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
	if err := s.goalPlanGranted(ctx, readmodel.PlanPortfolios); err != nil {
		return err
	}
	return s.Store.SavePortfolio(ctx, *p, items, s.clock())
}
