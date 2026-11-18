package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

// Control characters from Asana do not reach the screen through the JSON columns (metric,
// time period) or the plain ones.
func TestGoalsAreTermsafe(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	esc := "\x1b]0;pwn\x07"
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "名" + esc, Owner: &model.Ref{GID: "u1", Name: "人" + esc},
		Metric:     &model.GoalMetric{CurrentDisplayValue: "4" + esc, CurrencyCode: "J" + esc},
		TimePeriod: &model.TimePeriod{GID: "tp", DisplayName: "Q" + esc}}}))
	must(t, s.SaveGoal(ctx, model.Goal{GID: "g1", Name: "名", HTMLNotes: "<body>" + esc + "</body>"},
		[]model.GoalRelationship{{SupportingResource: &model.SupportingWork{GID: "p1", ResourceType: "project", Name: "営" + esc}}}, testNow))
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "pf", Name: "P" + esc,
		CurrentStatusUpdate: &model.StatusBrief{StatusType: "on_track", Title: "T" + esc}}}))
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "名" + esc, Owner: &model.Ref{GID: "u1", Name: "人" + esc},
		Metric:     &model.GoalMetric{CurrentDisplayValue: "4" + esc, CurrencyCode: "J" + esc},
		TimePeriod: &model.TimePeriod{GID: "tp", DisplayName: "Q" + esc}}}))
	g, err := s.GoalDetail(ctx, "g1")
	must(t, err)
	ps, err := s.ListPortfolios(ctx, "w1")
	must(t, err)
	for _, v := range []string{g.Name, g.Owner.Name, g.Metric.Display, g.Metric.Currency, g.TimePeriod.DisplayName, g.HTMLNotes,
		g.Supporting[0].Name, ps[0].Name, ps[0].Status.Title} {
		if strings.ContainsAny(v, "\x1b\x07") {
			t.Errorf("control characters in %q", v)
		}
	}
}

// Names resolve by gid, exact name, or a unique part; a part that matches several says so.
func TestResolveGoalAndPortfolio(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "売上 100%"}, {GID: "g2", Name: "売上_新規"}}))
	for arg, want := range map[string]string{"g1": "g1", "売上 100%": "g1", "100%": "g1", "_新": "g2"} {
		if got, err := s.ResolveGoal(ctx, arg); err != nil || got != want {
			t.Errorf("%q: %q, %v", arg, got, err)
		}
	}
	if _, err := s.ResolveGoal(ctx, "売上"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := s.ResolveGoal(ctx, "%"); err != nil {
		t.Errorf("%% is literal: %v", err)
	}
	if _, err := s.ResolveGoal(ctx, "なし"); !errors.Is(err, ErrNotFound) {
		t.Errorf("none: %v", err)
	}
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "pf1", Name: "全社"}}))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "pf1", Name: "全社"}, []model.PortfolioItem{{GID: "pf2", ResourceType: "portfolio", Name: "全社の下"}}, testNow))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "pf2", Name: "全社の下"}, nil, testNow))
	if got, err := s.ResolvePortfolio(ctx, "全社"); err != nil || got != "pf1" {
		t.Errorf("exact name of mine: %q, %v", got, err)
	}
	if got, err := s.ResolvePortfolio(ctx, "pf2"); err != nil || got != "pf2" {
		t.Errorf("gid of a nested one: %q, %v", got, err)
	}
	for _, blank := range []string{"", " ", "\t"} {
		if g, err := s.ResolveGoal(ctx, blank); !errors.Is(err, ErrNotFound) {
			t.Errorf("blank %q names %q, %v", blank, g, err)
		}
		if p, err := s.ResolvePortfolio(ctx, blank); !errors.Is(err, ErrNotFound) {
			t.Errorf("blank portfolio %q names %q, %v", blank, p, err)
		}
	}
}

// Nested portfolios are kept while a portfolio of mine holds them, and go with it; my
// list is only mine, in Asana's order.
func TestPortfoliosPrune(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "b", Name: "B"}, {GID: "a", Name: "A"}}))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "a"}, []model.PortfolioItem{{GID: "n", ResourceType: "portfolio", Name: "N"}, {GID: "p1", ResourceType: "project"}}, testNow))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "n", Name: "N"}, []model.PortfolioItem{{GID: "nn", ResourceType: "portfolio"}}, testNow))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "nn", Name: "NN"}, []model.PortfolioItem{{GID: "p2", ResourceType: "project"}}, testNow))
	ps, err := s.ListPortfolios(ctx, "w1")
	must(t, err)
	if len(ps) != 2 || ps[0].GID != "b" || ps[1].GID != "a" {
		t.Fatalf("mine = %+v", ps)
	}
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "a", Name: "A"}}))
	if _, err := s.PortfolioDetail(ctx, "nn"); err != nil {
		t.Fatalf("a portfolio nested twice is gone: %v", err)
	}
	if _, err := s.PortfolioDetail(ctx, "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b kept: %v", err)
	}
	must(t, s.ReplacePortfolios(ctx, "w1", nil))
	var n int
	must(t, s.db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM portfolios) + (SELECT count(*) FROM portfolio_items)").Scan(&n))
	if n != 0 {
		t.Fatalf("%d rows kept after no portfolio is mine", n)
	}
}

// A portfolio fetched by itself does not lose its place or the workspace.
func TestSavePortfolioKeepsItsPlace(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "a", Name: "A"}, {GID: "b", Name: "B"}}))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "a", Name: "A2"}, nil, testNow))
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "a", Name: "A2"}, {GID: "b", Name: "B"}}))
	ps, err := s.ListPortfolios(ctx, "w1")
	must(t, err)
	if len(ps) != 2 || ps[0].Name != "A2" {
		t.Fatalf("mine = %+v", ps)
	}
}

// A goal fetched by itself before any list goes after the listed ones; removing the
// workspace or resetting removes goals, periods and portfolios.
func TestGoalsGoWithTheirWorkspaceAndReset(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "一"}}))
	must(t, s.SaveGoal(ctx, model.Goal{GID: "g9", Name: "九", Workspace: &model.Ref{GID: "w1"}}, nil, testNow))
	gs, err := s.ListGoals(ctx, "w1")
	must(t, err)
	if len(gs) != 2 || gs[1].GID != "g9" {
		t.Fatalf("goals = %+v", gs)
	}
	must(t, s.ReplaceTimePeriods(ctx, "w1", []model.TimePeriod{{GID: "tp"}}))
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "pf"}}))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "pf"}, []model.PortfolioItem{{GID: "p1", ResourceType: "project"}}, testNow))
	must(t, s.SetSyncState(ctx, readmodel.KeyGoals+"w1", "x"))
	must(t, s.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanGoals, "w1"), "x"))
	must(t, s.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanGoals, "w11"), "kept"))
	must(t, s.UpsertWorkspaces(ctx, []model.Workspace{{GID: "w2", Name: "別"}}))
	must(t, s.PruneWorkspaces(ctx, []model.Workspace{{GID: "w2"}}))
	count := func() int {
		var n int
		must(t, s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM goals) + (SELECT count(*) FROM goal_supports) +
			(SELECT count(*) FROM time_periods) + (SELECT count(*) FROM portfolios) + (SELECT count(*) FROM portfolio_items)`).Scan(&n))
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("%d rows of a pruned workspace", n)
	}
	for key, want := range map[string]string{readmodel.KeyGoals + "w1": "", readmodel.PlanKey(readmodel.PlanGoals, "w1"): "",
		readmodel.PlanKey(readmodel.PlanGoals, "w11"): "kept"} {
		if v, _ := s.SyncState(ctx, key); v != want {
			t.Fatalf("%s after pruning w1: %q", key, v)
		}
	}
	must(t, s.ReplaceGoals(ctx, "w2", []model.Goal{{GID: "g1"}}))
	must(t, s.SaveGoal(ctx, model.Goal{GID: "g1"}, []model.GoalRelationship{{SupportingResource: &model.SupportingWork{GID: "x"}}}, testNow))
	must(t, s.ReplaceTimePeriods(ctx, "w2", []model.TimePeriod{{GID: "tp"}}))
	must(t, s.ReplacePortfolios(ctx, "w2", []model.Portfolio{{GID: "pf"}}))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "pf"}, []model.PortfolioItem{{GID: "p1"}}, testNow))
	if _, err := s.ResetWithBackup(ctx, t.TempDir(), AnyUnsent, testNow); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("%d rows after a reset", n)
	}
}
