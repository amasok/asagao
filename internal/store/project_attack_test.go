package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

// Team names, project notes and the brief from Asana carry no terminal control characters.
func TestAttackProjectStringsAreSafe(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "tmx", Name: "悪\x1b]0;pwn\x07チーム"}}))
	teams, err := s.ListTeams(ctx)
	must(t, err)
	for _, tm := range teams {
		if strings.ContainsAny(tm.Name, "\x1b\x07") {
			t.Errorf("team name %q", tm.Name)
		}
	}
	must(t, s.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p9", Name: "x", Members: []asana.Ref{{GID: "me"}},
		Notes: "a\x1b[2Jb", Owner: &asana.Ref{GID: "u", Name: "o\x1b[31m"}}}, "me"))
	o, err := s.ProjectOverview(ctx, "p9")
	must(t, err)
	if strings.ContainsRune(o.Notes, 0x1b) || strings.ContainsRune(o.Owner.Name, 0x1b) {
		t.Errorf("overview %+v", o)
	}
	must(t, s.SaveProjectOverview(ctx, "p9", nil, "available", &asana.ProjectBrief{Title: "t\x1b[1m", Text: "x\x07y", HTMLText: "<body>\x1b</body>"}, time.Now()))
	o, _ = s.ProjectOverview(ctx, "p9")
	if o.Brief == nil || strings.ContainsAny(o.Brief.Title+o.Brief.Text+o.Brief.HTMLText, "\x1b\x07") {
		t.Errorf("brief %+v", o.Brief)
	}
}

// Replacing teams or favorites with an empty list in one workspace leaves the others.
func TestAttackReplaceKeepsOtherWorkspaces(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "a", Name: "A"}}))
	must(t, s.ReplaceTeams(ctx, "w1", nil))
	teams, _ := s.ListTeams(ctx)
	seen := map[string]bool{}
	for _, tm := range teams {
		seen[tm.GID] = true
	}
	if seen["a"] {
		t.Fatalf("teams %+v", teams)
	}
	must(t, s.ReplaceFavoriteProjects(ctx, "w1", []asana.Ref{{GID: "p1"}, {GID: "p1"}, {GID: "zz"}}))
}

// Team lookup: a gid beats names, exact beats partial, same names / LIKE-wildcards are not special.
func TestAttackResolveTeamBoundaries(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "t1", Name: "開発"}, {GID: "t2", Name: "開発部"}, {GID: "t3", Name: "開発"}, {GID: "t4", Name: "営%業"}}))
	if _, err := s.ResolveTeam(ctx, "開発"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("duplicate exact names: %v", err)
	}
	if g, err := s.ResolveTeam(ctx, "t3"); err != nil || g != "t3" {
		t.Errorf("gid: %q %v", g, err)
	}
	if g, err := s.ResolveTeam(ctx, "開発部"); err != nil || g != "t2" {
		t.Errorf("exact: %q %v", g, err)
	}
	for _, bad := range []string{"_", "   ", "'; DROP TABLE teams;--"} {
		if g, err := s.ResolveTeam(ctx, bad); err == nil {
			t.Errorf("%q resolved to %q", bad, g)
		}
	}
	if _, err := s.ResolveTeam(ctx, "_"); err == nil {
		t.Error("_ matched")
	}
	if _, err := s.ListTeams(ctx); err != nil {
		t.Fatal(err)
	}
}

// start/due combinations through UpdateProjectLocal (start counts from the cache when only due changes).
func TestAttackProjectDateCombinations(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t) // p1: due 2026-12-31, no start
	ok := func(c map[string]any) {
		t.Helper()
		if err := s.UpdateProjectLocal(ctx, "p1", c, time.Now()); err != nil {
			t.Errorf("%v rejected: %v", c, err)
		}
	}
	bad := func(c map[string]any) {
		t.Helper()
		if err := s.UpdateProjectLocal(ctx, "p1", c, time.Now()); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
	bad(map[string]any{"start_on": "2027-01-01"})                        // after due
	ok(map[string]any{"start_on": "2026-12-31"})                         // same day
	bad(map[string]any{"due_on": "2026-12-30"})                          // due before cached start
	bad(map[string]any{"due_on": ""})                                    // start without due
	ok(map[string]any{"start_on": "", "due_on": ""})                     // both cleared
	bad(map[string]any{"start_on": "2026-01-01"})                        // start with no due
	bad(map[string]any{"due_on": "2026/12/31"})                          // format
	bad(map[string]any{"due_on": "2026-02-30"})                          // not a date
	bad(map[string]any{"due_on": 20261231})                              // not a string
	bad(map[string]any{"color": "light-brown"})                          // not an Asana color
	ok(map[string]any{"start_on": "2026-01-01", "due_on": "2026-02-01"}) // both together
}

// A partial name that fits two teams is ambiguous; one that fits one resolves, case-insensitively.
func TestAttackResolveTeamPartial(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "a", Name: "Dev Ops"}, {GID: "b", Name: "Dev Web"}, {GID: "c", Name: "Sales"}}))
	if _, err := s.ResolveTeam(ctx, "dev"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("dev: %v", err)
	}
	if g, err := s.ResolveTeam(ctx, "WEB"); err != nil || g != "b" {
		t.Errorf("WEB: %q %v", g, err)
	}
	if _, err := s.ResolveTeam(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nope: %v", err)
	}
}
