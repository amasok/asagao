package sync

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// projectServer serves one project (p1) for the project overview and edits (#39) and the
// teams and favorites (#38).
type projectServer struct {
	t  *testing.T
	mu gosync.Mutex
	// the project as Asana has it
	notes, owner, startOn, dueOn, color string
	brief                               string // project_brief gid ("" none)
	briefStatus                         int    // status of GET /project_briefs/{gid} (0: 200)
	countsStatus                        int    // status of GET task_counts (0: 200)
	teamsStatus, favoritesStatus        int
	puts                                []map[string]any
	putStatus                           int
	ownerName                           string
	calls                               map[string]int
}

func (p *projectServer) projectJSON() string {
	owner := "null"
	if p.owner != "" {
		owner = fmt.Sprintf(`{"gid":%q,"name":%s}`, p.owner, jsonStr(cmp.Or(p.ownerName, "花子")))
	}
	brief := "null"
	if p.brief != "" {
		brief = fmt.Sprintf(`{"gid":%q}`, p.brief)
	}
	return fmt.Sprintf(`{"gid":"p1","name":"新居","notes":%q,"html_notes":%q,"owner":%s,"start_on":%s,"due_on":%s,"color":%q,
		"workspace":{"gid":"w1"},"members":[{"gid":"me"}],"project_brief":%s}`,
		p.notes, "<body>"+p.notes+"</body>", owner, jsonDate(p.startOn), jsonDate(p.dueOn), p.color, brief)
}

func jsonStr(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonDate(d string) string {
	if d == "" {
		return "null"
	}
	return `"` + d + `"`
}

func (p *projectServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[r.Method+" "+r.URL.Path]++
	fail := func(status int) bool {
		if status == 0 {
			return false
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"errors":[{"message":"nope"}]}`)
		return true
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /users/me": // push checks the account
		fmt.Fprint(w, `{"data":{"gid":"me","name":"私"}}`)
	case "GET /projects/p1":
		fmt.Fprintf(w, `{"data":%s}`, p.projectJSON())
	case "PUT /projects/p1":
		if fail(p.putStatus) {
			return
		}
		var body struct{ Data map[string]any }
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.puts = append(p.puts, body.Data)
		str := func(v any) string { s, _ := v.(string); return s }
		for k, v := range body.Data {
			switch k {
			case "html_notes":
				p.notes = str(v)[len("<body>") : len(str(v))-len("</body>")]
			case "notes":
				p.notes = str(v)
			case "owner":
				p.owner = str(v)
			case "start_on":
				p.startOn = str(v)
			case "due_on":
				p.dueOn = str(v)
			case "color":
				p.color = str(v)
			}
		}
		fmt.Fprintf(w, `{"data":%s}`, p.projectJSON())
	case "GET /projects/p1/task_counts":
		if !fail(p.countsStatus) {
			fmt.Fprint(w, `{"data":{"num_tasks":8,"num_completed_tasks":2,"num_milestones":1,"num_completed_milestones":0}}`)
		}
	case "GET /project_briefs/b1":
		if !fail(p.briefStatus) {
			fmt.Fprint(w, `{"data":{"gid":"b1","title":"目的","text":"年内に引っ越す","html_text":"<body>年内に引っ越す</body>"}}`)
		}
	case "GET /users/me/teams":
		if r.URL.Query().Get("organization") != "w1" {
			p.t.Errorf("teams query %v", r.URL.Query())
		}
		if !fail(p.teamsStatus) {
			fmt.Fprint(w, `{"data":[{"gid":"tm1","name":"営業"},{"gid":"tm2","name":"開発"}]}`)
		}
	case "GET /users/me/favorites":
		if q := r.URL.Query(); q.Get("resource_type") != "project" || q.Get("workspace") != "w1" {
			p.t.Errorf("favorites query %v", q)
		}
		if !fail(p.favoritesStatus) {
			fmt.Fprint(w, `{"data":[{"gid":"p1","name":"新居"}]}`)
		}
	default:
		p.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
	}
}

func setupProject(t *testing.T) (*projectServer, *Syncer, *store.Store) {
	t.Helper()
	ps := &projectServer{t: t, notes: "説明", owner: "u1", dueOn: "2026-12-31", color: "dark-blue", calls: map[string]int{}}
	srv := httptest.NewServer(ps)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	s := &Syncer{API: api, Store: st, now: func() time.Time { return now }}
	if err := st.SetSyncState(ctx, store.KeyMeGID, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "会社", IsOrganization: true}}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.API.Project(ctx, "p1")
	if err := s.saveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	return ps, s, st
}

// An edit made offline is sent with PUT /projects: rich notes without the plain ones, a
// removed owner as null, and a start date with the due date Asana has.
func TestPushUpdateProject(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"notes": "新しい", "html_notes": "<body>新しい</body>",
		"owner": "", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if len(ps.puts) != 1 {
		t.Fatalf("puts = %v", ps.puts)
	}
	put := ps.puts[0]
	if _, plain := put["notes"]; plain || put["html_notes"] != "<body>新しい</body>" {
		t.Errorf("notes sent as %v", put)
	}
	if v, ok := put["owner"]; !ok || v != nil {
		t.Errorf("owner = %v (%v), want null", v, ok)
	}
	if put["start_on"] != "2026-12-01" || put["due_on"] != "2026-12-31" {
		t.Errorf("dates = %v / %v", put["start_on"], put["due_on"])
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("sent entry left: %+v", ops)
	}
	o, _ := st.ProjectOverview(ctx, "p1")
	if o.Notes != "新しい" || o.Owner != nil || o.StartOn != "2026-12-01" || o.Pending {
		t.Fatalf("stored after push: %+v", o)
	}
}

// A field changed in Asana since the edit is a conflict; fields changed only here, or to
// the same value there, are not.
func TestPushUpdateProjectConflict(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"due_on": "2027-01-31", "color": "light-green"}, now); err != nil {
		t.Fatal(err)
	}
	ps.dueOn = "2027-02-28"  // changed there too
	ps.color = "light-green" // the same change there
	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 || len(ps.puts) != 0 {
		t.Fatalf("push = %+v %v puts=%v", res, err, ps.puts)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Conflict["due_on"] != "2027-02-28" || ops[0].Conflict["color"] != nil {
		t.Fatalf("conflict = %+v", ops)
	}
	// Keep mine: sent on the next push.
	if err := st.ResolveKeepLocal(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || ps.dueOn != "2027-01-31" {
		t.Fatalf("after keep local: %+v %v due=%s", res, err, ps.dueOn)
	}
}

// A project without a color is "" here but "none" in Asana's answer (a project just made,
// found by scripts/realcheck #31): the same value, so editing it is not a conflict.
func TestPushUpdateProjectNoColorIsNone(t *testing.T) {
	for _, c := range []struct{ stored, asana string }{{"", "none"}, {"none", ""}} {
		t.Run(c.stored+"/"+c.asana, func(t *testing.T) {
			ctx := context.Background()
			ps, s, st := setupProject(t)
			ps.color = c.stored
			p, _ := s.API.Project(ctx, "p1")
			if err := s.saveProject(ctx, p); err != nil {
				t.Fatal(err)
			}
			ps.color = c.asana
			if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "dark-green", "due_on": "2030-01-20"}, now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Conflicts != 0 || ps.color != "dark-green" {
				ops, _ := st.ListOps(ctx)
				t.Fatalf("push = %+v %v color=%q ops=%+v", res, err, ps.color, ops)
			}
		})
	}
}

// Asana renames the written color names (dark-green comes back as green, scripts/realcheck
// #31): a project the cache has as dark-green is not a conflict with Asana's green.
func TestPushUpdateProjectRenamedColorIsSame(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	ps.color = "dark-green"
	p, _ := s.API.Project(ctx, "p1")
	if err := s.saveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	ps.color = "green"
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "dark-red"}, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Conflicts != 0 {
		ops, _ := st.ListOps(ctx)
		t.Fatalf("push = %+v %v ops=%+v", res, err, ops)
	}
	// A real change there (green → red) is still a conflict.
	ps.color = "red"
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "light-blue"}, now); err != nil {
		t.Fatal(err)
	}
	ps.color = "indigo" // dark-purple, set in Asana meanwhile
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("real change: push = %+v %v", res, err)
	}
}

// A project deleted in Asana drops the edit (recorded), like a renamed section's.
func TestPushUpdateProjectGone(t *testing.T) {
	ctx := context.Background()
	_, s, st := setupProject(t)
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "none"}, now); err != nil {
		t.Fatal(err)
	}
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"私"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"project: Not a recognized ID"}]}`)
		}
	}))
	defer gone.Close()
	api := asana.NewClient(gone.Client())
	api.BaseURL, api.MaxRetries = gone.URL, 0
	s.API = api
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || res.Discarded[0].Kind != "update_project" {
		t.Fatalf("push = %+v %v", res, err)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("entry left: %+v", ops)
	}
}

func TestFetchProjectOverview(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	ps.brief = "b1"
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	o, _ := st.ProjectOverview(ctx, "p1")
	if o.Counts == nil || o.Counts.Tasks != 8 || o.Counts.CompletedTasks != 2 || o.Counts.Milestones != 1 {
		t.Fatalf("counts = %+v", o.Counts)
	}
	if o.Brief == nil || o.Brief.Title != "目的" || o.Brief.Text != "年内に引っ越す" {
		t.Fatalf("brief = %+v", o.Brief)
	}

	// The brief is a preview API: 402 (or 403) means it is not shown, and is no error.
	for _, status := range []int{http.StatusPaymentRequired, http.StatusForbidden} {
		ps.briefStatus = status
		if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief != nil || o.Counts == nil {
			t.Fatalf("%d: brief %+v counts %+v", status, o.Brief, o.Counts)
		}
	}
	// The 402 is recorded for the workspace (#60; a 403 only for the project), and the
	// brief is not asked for there for a while; forget it to go on.
	key := readmodel.PlanKey(readmodel.PlanProjectBrief, "w1")
	if v, _ := st.SyncState(ctx, key); v == "" {
		t.Fatal("refusal not recorded for the workspace")
	}
	ps.briefStatus = 0
	refused := ps.calls["GET /project_briefs/b1"]
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if ps.calls["GET /project_briefs/b1"] != refused {
		t.Fatal("asked for the brief again right after a refusal")
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.BriefState != store.BriefUnavailable {
		t.Fatalf("brief state %q while refused", o.BriefState)
	}
	_ = st.SetSyncState(ctx, key, "")
	// A project without a brief does not ask for one.
	ps.brief, ps.briefStatus = "", 0
	before := ps.calls["GET /project_briefs/b1"]
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if ps.calls["GET /project_briefs/b1"] != before {
		t.Fatalf("asked for a brief the project does not have")
	}

	// A failure of the counts keeps the cached counts and is reported.
	ps.countsStatus = http.StatusInternalServerError
	if err := s.FetchProjectOverview(ctx, "p1"); err == nil {
		t.Fatal("a failed task_counts was not reported")
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Counts == nil || o.Counts.Tasks != 8 {
		t.Fatalf("cached counts lost: %+v", o.Counts)
	}
	// A temporary failure of the brief keeps the cached one.
	ps.countsStatus, ps.brief = 0, "b1"
	_ = s.FetchProjectOverview(ctx, "p1")
	ps.briefStatus = http.StatusInternalServerError
	if err := s.FetchProjectOverview(ctx, "p1"); err == nil {
		t.Fatal("a failed brief was not reported")
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief == nil {
		t.Fatalf("cached brief lost")
	}

	// Offline: an error, nothing changes.
	if err := (&Syncer{Store: st}).FetchProjectOverview(ctx, "p1"); err != ErrOffline {
		t.Fatalf("offline: %v", err)
	}
}

// Fetching the overview refreshes the project, but keeps an edit that is not sent yet.
func TestFetchProjectOverviewKeepsUnsentEdit(t *testing.T) {
	ctx := context.Background()
	_, s, st := setupProject(t)
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "light-red"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Color != "light-red" {
		t.Fatalf("color = %q", o.Color)
	}
}

// #38: the teams I am in and my favorites are fetched the first time and with the poll;
// a plan without them (402/403) has none; other failures are retried next time.
func TestSyncTeamsAndFavorites(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	w := asana.Workspace{GID: "w1", Name: "会社", IsOrganization: true}
	if err := s.syncTeamsAndFavorites(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	teams, _ := st.ListTeams(ctx)
	if len(teams) != 2 || teams[1].Name != "開発" {
		t.Fatalf("teams = %+v", teams)
	}
	if projects, _ := st.ListProjects(ctx, true); len(projects) != 1 || !projects[0].Favorite {
		t.Fatalf("favorite not stored: %+v", projects)
	}
	// Not again until the poll.
	if err := s.syncTeamsAndFavorites(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	if ps.calls["GET /users/me/teams"] != 1 || ps.calls["GET /users/me/favorites"] != 1 {
		t.Fatalf("fetched again without a poll: %v", ps.calls)
	}
	// A failure keeps what is cached and tries again on the next sync.
	ps.favoritesStatus = http.StatusInternalServerError
	if err := s.syncTeamsAndFavorites(ctx, w, true); err != nil {
		t.Fatal(err)
	}
	if projects, _ := st.ListProjects(ctx, true); !projects[0].Favorite {
		t.Fatalf("favorites dropped after a failure")
	}
	ps.favoritesStatus = http.StatusPaymentRequired
	if err := s.syncTeamsAndFavorites(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	if ps.calls["GET /users/me/favorites"] != 3 {
		t.Fatalf("not retried after a failure: %v", ps.calls)
	}
	if projects, _ := st.ListProjects(ctx, true); projects[0].Favorite {
		t.Fatalf("402: favorites kept")
	}
	// A workspace that is not an organization has no teams to ask for.
	if err := s.syncTeamsAndFavorites(ctx, asana.Workspace{GID: "w1"}, true); err != nil {
		t.Fatal(err)
	}
	if ps.calls["GET /users/me/teams"] != 3 {
		t.Fatalf("teams asked of a workspace without them: %v", ps.calls)
	}
}

// A new project is created in the team given.
func TestCreateProjectInTeam(t *testing.T) {
	ctx := context.Background()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /projects":
			var b struct{ Data map[string]any }
			_ = json.NewDecoder(r.Body).Decode(&b)
			body = b.Data
			fmt.Fprint(w, `{"data":{"gid":"p9","name":"新規","workspace":{"gid":"w1"},"team":{"gid":"tm2","name":"開発"}}}`)
		case "GET /projects/p9/sections":
			fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "会社"}})
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	if _, err := (&Syncer{API: api, Store: st}).CreateProject(ctx, "新規", "tm2"); err != nil {
		t.Fatal(err)
	}
	if body["team"] != "tm2" || body["workspace"] != "w1" {
		t.Fatalf("body = %v", body)
	}
}
