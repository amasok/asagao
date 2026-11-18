package store

import (
	"context"
	"errors"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
)

// An empty or blank argument names nothing; it must not pick the only goal by matching everything.
func TestAttackResolveEmptyArg(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "only"}}))
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "pf1", Name: "only"}}))
	for _, arg := range []string{"", " "} {
		if gid, err := s.ResolveGoal(ctx, arg); err == nil {
			t.Errorf("goal %q resolved to %q", arg, gid)
		}
		if gid, err := s.ResolvePortfolio(ctx, arg); err == nil {
			t.Errorf("portfolio %q resolved to %q", arg, gid)
		}
	}
}

// Hostile arguments are plain text: no SQL, no LIKE wildcards, no backslash escapes.
func TestAttackResolveHostileArgs(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "a"}, {GID: "g2", Name: "b"}}))
	for _, arg := range []string{"' OR 1=1 --", `\`, `\%`, "_", "%%", "'; DROP TABLE goals; --"} {
		gid, err := s.ResolveGoal(ctx, arg)
		if err == nil {
			t.Errorf("%q resolved to %q", arg, gid)
		}
		if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrAmbiguous) {
			t.Errorf("%q: %v", arg, err)
		}
	}
	if l, err := s.ListGoals(ctx, "w1"); err != nil || len(l) != 2 {
		t.Fatalf("goals after hostile args: %v, %v", l, err)
	}
}

// A goal fetched on its own (not listed) keeps its place and does not vanish nor duplicate on a list.
func TestAttackSaveGoalThenList(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "a"}, {GID: "g2", Name: "b"}}))
	must(t, s.SaveGoal(ctx, model.Goal{GID: "g3", Name: "c", Workspace: &model.Ref{GID: "w1"}}, nil, testNow))
	l, err := s.ListGoals(ctx, "w1")
	must(t, err)
	if len(l) != 3 || l[0].GID != "g1" || l[1].GID != "g2" || l[2].GID != "g3" {
		t.Fatalf("order = %+v", l)
	}
	// Saving the same goal twice with fewer supports drops the old ones.
	rel := func(gid string) model.GoalRelationship {
		return model.GoalRelationship{SupportingResource: &model.SupportingWork{GID: gid, ResourceType: "project", Name: gid}}
	}
	must(t, s.SaveGoal(ctx, model.Goal{GID: "g1", Name: "a"}, []model.GoalRelationship{rel("p1"), rel("p2")}, testNow))
	must(t, s.SaveGoal(ctx, model.Goal{GID: "g1", Name: "a"}, []model.GoalRelationship{rel("p2")}, testNow))
	g, err := s.GoalDetail(ctx, "g1")
	must(t, err)
	if len(g.Supporting) != 1 || g.Supporting[0].GID != "p2" {
		t.Fatalf("supports = %+v", g.Supporting)
	}
	// A relationship without a resource does not panic.
	must(t, s.SaveGoal(ctx, model.Goal{GID: "g1", Name: "a"}, []model.GoalRelationship{{}}, testNow))
}

// Replacing the list of one workspace leaves the goals of another alone.
func TestAttackReplaceGoalsOtherWorkspace(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "a"}}))
	must(t, s.ReplaceGoals(ctx, "w2", []model.Goal{{GID: "h1", Name: "b"}}))
	must(t, s.ReplaceGoals(ctx, "w1", nil))
	l, err := s.ListGoals(ctx, "w2")
	must(t, err)
	if len(l) != 1 || l[0].GID != "h1" {
		t.Fatalf("%+v", l)
	}
}

func pfItems(gids ...string) []model.PortfolioItem {
	var out []model.PortfolioItem
	for _, g := range gids {
		out = append(out, model.PortfolioItem{GID: g, ResourceType: "portfolio", Name: g})
	}
	return out
}

func portfolioGIDs(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	rows, err := s.db.Query("SELECT gid FROM portfolios")
	must(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var g string
		must(t, rows.Scan(&g))
		out[g] = true
	}
	return out
}

// Portfolios nested under mine are kept, however deep, even when they form a cycle; once
// I stop owning the top one, the whole chain goes (cycle included) without hanging.
func TestAttackPortfolioNestingAndCycle(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "top", Name: "top"}}))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "top", Name: "top"}, pfItems("a"), testNow))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "a", Name: "a"}, pfItems("b"), testNow))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "b", Name: "b"}, pfItems("a", "top"), testNow)) // cycle a<->b, and back to top
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "top", Name: "top"}}))
	if got := portfolioGIDs(t, s); !got["top"] || !got["a"] || !got["b"] {
		t.Fatalf("nested kept: %v", got)
	}
	if p, err := s.PortfolioDetail(ctx, "b"); err != nil || len(p.Items) != 2 {
		t.Fatalf("b items kept: %+v, %v", p, err)
	}
	must(t, s.ReplacePortfolios(ctx, "w1", nil))
	if got := portfolioGIDs(t, s); len(got) != 0 {
		t.Fatalf("chain survived: %v", got)
	}
	var n int
	must(t, s.db.QueryRow("SELECT count(*) FROM portfolio_items").Scan(&n))
	if n != 0 {
		t.Fatalf("%d items left", n)
	}
}

// A portfolio that holds itself, and one held by two parents (only one parent kept).
func TestAttackPortfolioSelfAndSharedChild(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "m1", Name: "m1"}, {GID: "m2", Name: "m2"}}))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "m1", Name: "m1"}, pfItems("m1", "c"), testNow))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "m2", Name: "m2"}, pfItems("c"), testNow))
	must(t, s.SavePortfolio(ctx, model.Portfolio{GID: "c", Name: "c"}, pfItems(), testNow))
	must(t, s.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "m2", Name: "m2"}}))
	got := portfolioGIDs(t, s)
	if got["m1"] || !got["m2"] || !got["c"] {
		t.Fatalf("got %v", got)
	}
}
