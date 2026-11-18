package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

// projectStore has workspace w1 with projects of two teams, one without a team, and one
// I am not a member of.
func projectStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	t.Cleanup(func() { s.Close() })
	must(t, s.SetSyncState(ctx, KeyMeGID, "me"))
	must(t, s.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "会社", IsOrganization: true}}))
	me := []asana.Ref{{GID: "me", Name: "私"}}
	must(t, s.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "p1", Name: "A 開発", Team: &asana.Ref{GID: "tm2", Name: "開発"}, Members: me,
			Notes: "説明", HTMLNotes: "<body>説明</body>", Owner: &asana.Ref{GID: "u1", Name: "花子"}, DueOn: "2026-12-31", Color: "dark-blue"},
		{GID: "p2", Name: "B 営業", Team: &asana.Ref{GID: "tm1", Name: "営業"}, Members: me},
		{GID: "p3", Name: "C 開発", Team: &asana.Ref{GID: "tm2", Name: "開発"}, Members: me},
		{GID: "p4", Name: "D 個人", Members: me},
		{GID: "p5", Name: "E 他人", Team: &asana.Ref{GID: "tm1", Name: "営業"}},
	}, "me"))
	return s
}

func projectGIDs(t *testing.T, s *Store, memberOnly bool, team string) string {
	t.Helper()
	ps, err := s.ListProjectsInTeam(context.Background(), memberOnly, team)
	must(t, err)
	var g []string
	for _, p := range ps {
		g = append(g, p.GID)
	}
	return strings.Join(g, ",")
}

// #38: favorites first (in my order), then by team name, projects without a team last.
func TestListProjectsFavoritesThenTeams(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	if got := projectGIDs(t, s, true, ""); got != "p2,p1,p3,p4" {
		t.Fatalf("by team = %s, want p2,p1,p3,p4", got)
	}
	must(t, s.ReplaceFavoriteProjects(ctx, "w1", []asana.Ref{{GID: "p4"}, {GID: "p3"}, {GID: "gone"}}))
	if got := projectGIDs(t, s, true, ""); got != "p4,p3,p2,p1" {
		t.Fatalf("with favorites = %s, want p4,p3,p2,p1", got)
	}
	ps, _ := s.ListProjects(ctx, true)
	if !ps[0].Favorite || ps[2].Favorite || ps[2].TeamGID != "tm1" {
		t.Fatalf("flags: %+v", ps)
	}
	// Replacing the favorites drops the old ones.
	must(t, s.ReplaceFavoriteProjects(ctx, "w1", nil))
	if got := projectGIDs(t, s, true, ""); got != "p2,p1,p3,p4" {
		t.Fatalf("after unfavoriting = %s", got)
	}
}

func TestListProjectsInTeam(t *testing.T) {
	s := projectStore(t)
	for team, want := range map[string]string{"開発": "p1,p3", "tm1": "p2", "営": "p2"} {
		if got := projectGIDs(t, s, true, team); got != want {
			t.Errorf("team %q = %s, want %s", team, got, want)
		}
	}
	if got := projectGIDs(t, s, false, "営業"); got != "p2,p5" {
		t.Errorf("all of 営業 = %s", got)
	}
	if _, err := s.ListProjectsInTeam(context.Background(), true, "経理"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown team: %v", err)
	}
}

// The teams for a new project are the ones I am in; before they are fetched, those of
// the cached projects. One team is picked by itself; several must be chosen.
func TestTeamsForNewProject(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	teams, err := s.ListTeams(ctx)
	must(t, err)
	if len(teams) != 2 || teams[0].Name != "営業" || teams[1].Name != "開発" {
		t.Fatalf("teams from projects = %+v", teams)
	}
	if _, err := s.TeamForNewProject(ctx, ""); !errors.Is(err, ErrAmbiguous) || !strings.Contains(err.Error(), "営業、開発") {
		t.Fatalf("two teams, none given: %v", err)
	}
	if g, err := s.TeamForNewProject(ctx, "開"); err != nil || g != "tm2" {
		t.Fatalf("by partial name = %q %v", g, err)
	}
	must(t, s.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "tm9", Name: "デザイン"}}))
	if g, err := s.TeamForNewProject(ctx, ""); err != nil || g != "tm9" {
		t.Fatalf("the only team I am in = %q %v", g, err)
	}
	// A team of a cached project still resolves by name.
	if g, err := s.ResolveTeam(ctx, "営業"); err != nil || g != "tm1" {
		t.Fatalf("resolve 営業 = %q %v", g, err)
	}
	must(t, s.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "tm9", Name: "開発 A"}, {GID: "tm8", Name: "開発 B"}}))
	if _, err := s.ResolveTeam(ctx, "発"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("partial name of two teams: %v", err)
	}
	if g, err := s.ResolveTeam(ctx, "開発"); err != nil || g != "tm2" {
		t.Fatalf("an exact name wins over partial ones: %q %v", g, err)
	}
}

func TestTeamForNewProjectWithoutTeams(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t) // a workspace whose projects have no team
	must(t, s.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "個人"}}))
	if g, err := s.TeamForNewProject(ctx, ""); err != nil || g != "" {
		t.Fatalf("no teams: %q %v", g, err)
	}
}

// #39: an edit is applied locally and queued; edits of the same project are one entry;
// editing back removes it.
func TestUpdateProjectLocal(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"owner": "", "start_on": "2026-12-01"}, testNow))
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"notes": "新しい説明", "html_notes": "<body>新しい説明</body>", "color": "none"}, testNow))
	o, err := s.ProjectOverview(ctx, "p1")
	must(t, err)
	if o.Owner != nil || o.StartOn != "2026-12-01" || o.Notes != "新しい説明" || o.Color != "none" || !o.Pending {
		t.Fatalf("local values: %+v", o)
	}
	ops, _ := s.PendingOps(ctx)
	if len(ops) != 1 || ops[0].Kind != OpUpdateProject || ops[0].TaskGID != "p1" || ops[0].TaskName != "A 開発" {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].Base["owner"] != "u1" || ops[0].Base["start_on"] != "" || ops[0].Base["color"] != "dark-blue" || ops[0].Payload["notes"] != "新しい説明" {
		t.Fatalf("base/payload = %+v / %+v", ops[0].Base, ops[0].Payload)
	}
	// Back to the original values: nothing left to send.
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"owner": "u1", "start_on": "", "notes": "説明", "html_notes": "<body>説明</body>", "color": "dark-blue"}, testNow))
	if ops, _ := s.PendingOps(ctx); len(ops) != 0 {
		t.Fatalf("reverted edit still queued: %+v", ops)
	}
}

func TestUpdateProjectLocalRejects(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	for name, changes := range map[string]map[string]any{
		"color":            {"color": "rainbow"},
		"date":             {"due_on": "12/31"},
		"field":            {"name": "x"},
		"type":             {"owner": 1},
		"start after due":  {"start_on": "2027-01-01"},
		"start, due clear": {"start_on": "2026-12-01", "due_on": ""},
		"nothing":          {},
	} {
		if err := s.UpdateProjectLocal(ctx, "p1", changes, testNow); err == nil {
			t.Errorf("%s: accepted %v", name, changes)
		}
	}
	if err := s.UpdateProjectLocal(ctx, "p2", map[string]any{"start_on": "2026-12-01"}, testNow); err == nil {
		t.Errorf("a start date without a due date was accepted")
	}
	if err := s.UpdateProjectLocal(ctx, "nope", map[string]any{"color": "none"}, testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("a rejected edit was queued: %+v", ops)
	}
	if o, _ := s.ProjectOverview(ctx, "p1"); o.StartOn != "" || o.Color != "dark-blue" {
		t.Fatalf("a rejected edit was applied: %+v", o)
	}
}

// A sync (or any refresh of the project) does not undo an edit that is not sent yet.
func TestUnsentProjectEditSurvivesSync(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"due_on": "2027-03-31"}, testNow))
	must(t, s.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "A 開発（改名）", DueOn: "2026-12-31", Color: "dark-red"}}, "me"))
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.DueOn != "2027-03-31" || o.Name != "A 開発（改名）" || o.Color != "dark-red" {
		t.Fatalf("after sync: %+v", o)
	}
}

// Discarding the edit restores the values from before it.
func TestDiscardProjectEdit(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"owner": "me", "color": "light-green"}, testNow))
	ops, _ := s.PendingOps(ctx)
	must(t, s.DiscardOp(ctx, ops[0].ID))
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.Owner == nil || o.Owner.GID != "u1" || o.Color != "dark-blue" || o.Pending {
		t.Fatalf("after discard: %+v", o)
	}
}

func TestProjectOverview(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	o, err := s.ProjectOverview(ctx, "p1")
	must(t, err)
	if o.Owner == nil || o.Owner.Name != "花子" || o.DueOn != "2026-12-31" || o.HTMLNotes != "<body>説明</body>" || o.TeamName != "開発" {
		t.Fatalf("fields: %+v", o)
	}
	if o.Counts != nil || o.Brief != nil || o.FetchedAt != "" || o.Status != nil {
		t.Fatalf("nothing fetched yet: %+v", o)
	}
	must(t, s.SaveProjectOverview(ctx, "p1", &asana.TaskCounts{NumTasks: 10, NumCompletedTasks: 4, NumMilestones: 2, NumCompletedMiles: 1},
		BriefAvailable, &asana.ProjectBrief{Title: "目的", Text: "年内に", HTMLText: "<body>年内に</body>"}, testNow))
	must(t, s.ReplaceStatusUpdates(ctx, "p1", []asana.StatusUpdate{{GID: "su1", StatusType: "at_risk", Title: "遅れ気味", CreatedAt: testNow}}))
	o, _ = s.ProjectOverview(ctx, "p1")
	if o.Counts == nil || *o.Counts != (readmodel.TaskCounts{Tasks: 10, CompletedTasks: 4, Milestones: 2, CompletedMilestones: 1}) {
		t.Fatalf("counts: %+v", o.Counts)
	}
	if o.Brief == nil || o.Brief.Title != "目的" || o.Status == nil || o.Status.Title != "遅れ気味" || o.FetchedAt == "" {
		t.Fatalf("brief/status: %+v", o)
	}
	// Counts not fetched this time keep the cached ones; a brief that became unavailable
	// (402) is not shown.
	must(t, s.SaveProjectOverview(ctx, "p1", nil, BriefUnavailable, nil, testNow))
	o, _ = s.ProjectOverview(ctx, "p1")
	if o.Counts == nil || o.Counts.Tasks != 10 || o.Brief != nil {
		t.Fatalf("after unavailable: %+v %+v", o.Counts, o.Brief)
	}
	// "" keeps the cached brief.
	must(t, s.SaveProjectOverview(ctx, "p1", nil, BriefAvailable, &asana.ProjectBrief{Text: "b"}, testNow))
	must(t, s.SaveProjectOverview(ctx, "p1", nil, "", nil, testNow))
	if o, _ = s.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Text != "b" {
		t.Fatalf("kept brief: %+v", o.Brief)
	}
	if _, err := s.ProjectOverview(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

// Projects and workspaces that are gone take their favorites, overviews and teams along.
func TestPruneProjectsAndWorkspacesTakeTheirRows(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.ReplaceFavoriteProjects(ctx, "w1", []asana.Ref{{GID: "p1"}}))
	must(t, s.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "tm1", Name: "営業"}}))
	must(t, s.SaveProjectOverview(ctx, "p1", &asana.TaskCounts{NumTasks: 1}, BriefNone, nil, testNow))
	must(t, s.SetSyncState(ctx, KeyTeams+"w1", "x"))
	must(t, s.PruneProjects(ctx, "w1", []asana.Project{{GID: "p2"}}))
	for _, table := range []string{"project_favorites", "project_overviews"} {
		var n int
		must(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n))
		if n != 0 {
			t.Errorf("%s keeps %d rows of a pruned project", table, n)
		}
	}
	must(t, s.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w2", Name: "別"}}))
	must(t, s.PruneWorkspaces(ctx, []asana.Workspace{{GID: "w2"}}))
	var n int
	must(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM teams").Scan(&n))
	if v, _ := s.SyncState(ctx, KeyTeams+"w1"); n != 0 || v != "" {
		t.Errorf("a pruned workspace keeps %d teams, key %q", n, v)
	}
}

// A store from before #38/#39 gets the new tables and columns and keeps its projects.
func TestMigrateStoreFromBeforeProjectFields(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(ctx, path)
	must(t, err)
	must(t, st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "残る"}}, "me"))
	rollbackTo(t, st.db, len(migrations)-1)
	st.Close()

	st, err = Open(ctx, path)
	must(t, err)
	defer st.Close()
	o, err := st.ProjectOverview(ctx, "p1")
	if err != nil || o.Name != "残る" {
		t.Fatalf("project after migration: %+v %v", o, err)
	}
	must(t, st.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "tm1", Name: "営業"}}))
}
