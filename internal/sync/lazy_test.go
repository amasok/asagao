package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// Projects fetched when they are opened (#54).
//
// lazyAsana serves a workspace with p1 (I am a member), p2 (a member and my favorite) and
// p3 (not a member). Each scope has an incomplete and a completed task; GET /tasks returns
// the completed ones only when completed_since is before lazyNow (as Asana does).
type lazyAsana struct {
	t   *testing.T
	mu  gosync.Mutex
	log []string
}

var lazyNow = time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)

const (
	lazyStamp  = "2026-11-13T00:00:00Z" // completed_since for the incomplete tasks only
	lazyWindow = "2026-10-30T00:00:00Z" // 14 days of completed tasks
)

func lazyTask(gid, project string, completed bool, subtasks int) string {
	done := ""
	if completed {
		done = `,"completed":true,"completed_at":"2026-11-10T00:00:00Z"`
	}
	return fmt.Sprintf(`{"gid":%q,"name":%q,"num_subtasks":%d,"modified_at":"2026-11-10T00:00:00Z","memberships":[{"project":{"gid":%q}}]%s}`,
		gid, gid, subtasks, project, done)
}

func (f *lazyAsana) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func (f *lazyAsana) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = nil
}

func (f *lazyAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	entry := r.Method + " " + r.URL.Path
	for _, k := range []string{"project", "assignee", "resource", "sync", "completed_since", "modified_since"} {
		if v := q.Get(k); v != "" {
			entry += " " + k + "=" + v
		}
	}
	f.mu.Lock()
	f.log = append(f.log, entry)
	f.mu.Unlock()
	withCompleted := false
	if cs := q.Get("completed_since"); cs != "" {
		since, err := time.Parse(time.RFC3339, cs)
		if err != nil {
			f.t.Errorf("completed_since %q", cs)
		}
		withCompleted = since.Before(lazyNow.Add(-time.Hour))
	}
	tasks := func(open, done string) {
		list := []string{open}
		if withCompleted {
			list = append(list, done)
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(list, ","))
	}
	switch p := r.URL.Path; {
	case p == "/users/me":
		fmt.Fprint(w, `{"data":{"gid":"u1","name":"Me"}}`)
	case p == "/workspaces":
		fmt.Fprint(w, `{"data":[{"gid":"w1","name":"WS"}]}`)
	case p == "/users/me/favorites":
		fmt.Fprint(w, `{"data":[{"gid":"p2","name":"Fav"}]}`)
	case p == "/projects":
		fmt.Fprint(w, `{"data":[
			{"gid":"p1","name":"Member","members":[{"gid":"u1"}]},
			{"gid":"p2","name":"Fav","members":[{"gid":"u1"}]},
			{"gid":"p3","name":"Other","members":[{"gid":"u2"}]}]}`)
	case strings.HasPrefix(p, "/projects/") && strings.HasSuffix(p, "/sections"):
		gid := strings.Split(p, "/")[2]
		fmt.Fprintf(w, `{"data":[{"gid":"s-%s","name":"Todo"}]}`, gid)
	case strings.HasPrefix(p, "/projects/") && strings.HasSuffix(p, "/custom_field_settings"):
		fmt.Fprint(w, `{"data":[]}`)
	case p == "/tasks" && q.Get("assignee") == "me":
		tasks(lazyTask("m1", "p3", false, 0), lazyTask("m2", "p3", true, 0))
	case p == "/tasks" && q.Get("project") == "p1":
		tasks(lazyTask("a1", "p1", false, 0), lazyTask("a2", "p1", true, 0))
	case p == "/tasks" && q.Get("project") == "p2":
		tasks(lazyTask("b1", "p2", false, 1), lazyTask("b2", "p2", true, 1))
	case p == "/tasks" && q.Get("project") == "p3":
		tasks(lazyTask("c1", "p3", false, 0), lazyTask("c2", "p3", true, 0))
	case strings.HasPrefix(p, "/tasks/") && strings.HasSuffix(p, "/subtasks"):
		parent := strings.Split(p, "/")[2]
		fmt.Fprintf(w, `{"data":[{"gid":"%s-sub","name":"sub","parent":{"gid":%q}}]}`, parent, parent)
	case p == "/events":
		if q.Get("sync") == "" {
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprintf(w, `{"errors":[{"message":"Sync token invalid or too old"}],"sync":"T-%s"}`, q.Get("resource"))
			return
		}
		fmt.Fprintf(w, `{"data":[],"sync":%q,"has_more":false}`, q.Get("sync"))
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"not here"}]}`)
	}
}

type lazyEnv struct {
	fake *lazyAsana
	api  *asana.Client
	st   *store.Store
	s    *Syncer
	now  time.Time
}

func setupLazy(t *testing.T, cfg Config) *lazyEnv {
	t.Helper()
	fake := &lazyAsana{t: t}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	e := &lazyEnv{fake: fake, api: api, st: st, now: lazyNow}
	e.s = &Syncer{API: api, Store: st, Cfg: cfg, now: func() time.Time { return e.now }}
	return e
}

func (e *lazyEnv) run(t *testing.T) Result {
	t.Helper()
	res, err := e.s.Run(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// taskQueries are the GET /tasks requests sent, in order.
func taskQueries(log []string) []string {
	var out []string
	for _, l := range log {
		if strings.HasPrefix(l, "GET /tasks ") {
			out = append(out, l)
		}
	}
	return out
}

func touches(log []string, project string) []string {
	var out []string
	for _, l := range log {
		if strings.Contains(l, "project="+project) || strings.Contains(l, "/projects/"+project+"/") ||
			strings.Contains(l, "resource="+project) {
			out = append(out, l)
		}
	}
	return out
}

func (e *lazyEnv) cached(t *testing.T, gid string) bool {
	t.Helper()
	_, err := e.st.GetTask(context.Background(), gid)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func (e *lazyEnv) state(t *testing.T, key string) string {
	t.Helper()
	v, err := e.st.SyncState(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var lazyCfg = Config{CompletedDays: 14, LazyProjects: true}

// The first sync lists every project but fetches the tasks only of my favorite (and My
// Tasks): p1 and p3 get no request at all, not even their events.
func TestLazySyncFetchesProjectListOnly(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	res := e.run(t)
	log := e.fake.requests()
	for _, p := range []string{"p1", "p3"} {
		if got := touches(log, p); len(got) > 0 {
			t.Errorf("project %s not opened, but requested: %v", p, got)
		}
	}
	if len(touches(log, "p2")) == 0 {
		t.Error("the favorite p2 was not fetched")
	}
	if res.Projects != 1 || res.Listed != 2 {
		t.Errorf("projects %d, listed %d; want 1 synced, 2 listed", res.Projects, res.Listed)
	}
	if res.Requests != int64(len(log)) {
		t.Errorf("res.Requests = %d, the server got %d", res.Requests, len(log))
	}
	ps, err := e.st.ListProjects(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	unfetched := map[string]bool{}
	for _, p := range ps {
		unfetched[p.GID] = p.Unfetched
	}
	if len(ps) != 3 || !unfetched["p1"] || unfetched["p2"] || !unfetched["p3"] {
		t.Errorf("projects listed %d, unfetched %v; want p1 and p3 unfetched", len(ps), unfetched)
	}
	// My Tasks is fetched whatever project its tasks are in.
	for _, gid := range []string{"m1", "m2", "b1", "b2", "b1-sub", "b2-sub"} {
		if !e.cached(t, gid) {
			t.Errorf("%s not cached", gid)
		}
	}
	for _, gid := range []string{"a1", "c1"} {
		if e.cached(t, gid) {
			t.Errorf("%s of a project not opened is cached", gid)
		}
	}
	if n, err := e.st.CountUnfetchedProjects(context.Background(), true); err != nil || n != 1 {
		t.Errorf("unfetched member projects = %d (%v), want 1 (p1)", n, err)
	}
}

// What is fetched for the first time comes with its incomplete tasks first: the completed
// ones are asked for only after every incomplete one, with modified_since so the incomplete
// ones that did not change are not sent again. Later syncs ask for both at once.
func TestLazyFetchesIncompleteFirst(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	e.run(t)
	qs := taskQueries(e.fake.requests())
	lastOpen, firstDone := -1, len(qs)
	for i, q := range qs {
		switch {
		case strings.Contains(q, "completed_since="+lazyStamp):
			lastOpen = i
			if strings.Contains(q, "modified_since=") {
				t.Errorf("first fetch with modified_since: %s", q)
			}
		case strings.Contains(q, "completed_since="+lazyWindow):
			firstDone = min(firstDone, i)
			if !strings.Contains(q, "modified_since="+lazyWindow) {
				t.Errorf("completed tasks asked for without modified_since = the window: %s", q)
			}
		default:
			t.Errorf("unexpected query %s", q)
		}
	}
	if lastOpen < 0 || firstDone == len(qs) || lastOpen > firstDone {
		t.Fatalf("incomplete tasks must all come first:\n%s", strings.Join(qs, "\n"))
	}
	if n := len(qs) - firstDone; n != 2 { // My Tasks and p2
		t.Errorf("%d queries for completed tasks, want 2:\n%s", n, strings.Join(qs, "\n"))
	}
	subs := 0
	for _, l := range e.fake.requests() {
		if strings.HasSuffix(l, "/subtasks") {
			subs++
		}
	}
	if subs != 2 { // b1's with b1, b2's with b2: b1's is not fetched again
		t.Errorf("%d subtask requests, want 2", subs)
	}
	if got := e.state(t, readmodel.KeyMyTasks+"w1"); got != readmodel.TasksAll {
		t.Errorf("my tasks state %q", got)
	}
	if got := e.state(t, readmodel.KeyProjectTasks+"p2"); got != readmodel.TasksAll {
		t.Errorf("p2 state %q", got)
	}

	e.fake.reset()
	e.now = e.now.Add(2 * time.Hour) // a poll: every synced project is fetched again
	e.run(t)
	for _, q := range taskQueries(e.fake.requests()) {
		if !strings.Contains(q, "completed_since=2026-10-30T02:00:00Z") || !strings.Contains(q, "modified_since=") {
			t.Errorf("later sync, want one query with the window and modified_since: %s", q)
		}
	}
}

// With DeferCompleted (the TUI), Run leaves the completed tasks to FetchCompleted, so the
// incomplete ones can be shown first.
func TestLazyDeferCompleted(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	e.s.DeferCompleted = true
	res := e.run(t)
	if !res.CompletedPending {
		t.Fatal("CompletedPending not set")
	}
	for _, q := range taskQueries(e.fake.requests()) {
		if strings.Contains(q, "completed_since="+lazyWindow) {
			t.Errorf("Run asked for the completed tasks: %s", q)
		}
	}
	if e.cached(t, "m2") || e.cached(t, "b2") {
		t.Fatal("completed tasks cached before FetchCompleted")
	}
	if !e.cached(t, "m1") || !e.cached(t, "b1") {
		t.Fatal("incomplete tasks not cached")
	}
	e.fake.reset()
	fres, err := e.s.FetchCompleted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !e.cached(t, "m2") || !e.cached(t, "b2") || !e.cached(t, "b2-sub") {
		t.Fatal("FetchCompleted did not fetch the completed tasks")
	}
	var subs []string
	for _, l := range e.fake.requests() {
		if strings.HasSuffix(l, "/subtasks") {
			subs = append(subs, l)
		}
	}
	if !slices.Equal(subs, []string{"GET /tasks/b2/subtasks"}) { // b1's came with b1
		t.Errorf("subtasks fetched by FetchCompleted: %v, want b2's only", subs)
	}
	if fres.Requests != int64(len(e.fake.requests())) || fres.Requests == 0 {
		t.Errorf("FetchCompleted requests %d, server got %d", fres.Requests, len(e.fake.requests()))
	}
	if pending, _ := e.s.completedPending(context.Background()); pending {
		t.Error("still pending after FetchCompleted")
	}
	// Nothing left: a second call sends nothing.
	e.fake.reset()
	if _, err := e.s.FetchCompleted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.fake.requests(); len(got) != 0 {
		t.Errorf("nothing pending, but sent %v", got)
	}
}

// Opening a project fetches its incomplete tasks (and its events token): from then on, every
// sync keeps it up to date from its events and fetches its completed tasks.
func TestOpenProjectFetchesAndTracks(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	e.fake.reset()
	out, err := e.s.OpenProject(ctx, "p1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Fetched || out.State != readmodel.TasksOpen || out.Requests != int64(len(e.fake.requests())) {
		t.Fatalf("open %+v (server got %d)", out, len(e.fake.requests()))
	}
	if !e.cached(t, "a1") || e.cached(t, "a2") {
		t.Fatal("open: want the incomplete a1 only")
	}
	if got := e.state(t, readmodel.KeyEvents+"p1"); got != "T-p1" {
		t.Errorf("events token %q", got)
	}
	if ps, _ := e.st.ListProjects(ctx, true); ps[slices.IndexFunc(ps, func(p readmodel.ProjectView) bool { return p.GID == "p1" })].Unfetched {
		t.Error("p1 still unfetched")
	}

	// The next sync reads p1's events and fetches its completed tasks.
	e.fake.reset()
	e.now = e.now.Add(10 * time.Minute)
	e.run(t)
	log := e.fake.requests()
	if !slices.Contains(log, "GET /events resource=p1 sync=T-p1") {
		t.Errorf("p1's events not read: %v", touches(log, "p1"))
	}
	if !e.cached(t, "a2") || e.state(t, readmodel.KeyProjectTasks+"p1") != readmodel.TasksAll {
		t.Fatal("the sync after the open did not fetch p1's completed tasks")
	}
	if got := touches(log, "p3"); len(got) > 0 {
		t.Errorf("p3 was never opened: %v", got)
	}

	// Opened again: nothing to fetch.
	e.fake.reset()
	if out, err = e.s.OpenProject(ctx, "p1", true); err != nil || out.Fetched || out.Requests != 0 {
		t.Fatalf("second open %+v %v", out, err)
	}
	if got := e.fake.requests(); len(got) != 0 {
		t.Errorf("second open sent %v", got)
	}
}

// OpenProject with the completed tasks (the CLI and MCP) fetches them right away.
func TestOpenProjectWithCompleted(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	e.run(t)
	out, err := e.s.OpenProject(context.Background(), "p3", true)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != readmodel.TasksAll || !e.cached(t, "c1") || !e.cached(t, "c2") {
		t.Fatalf("open with completed: %+v, c1 %v c2 %v", out, e.cached(t, "c1"), e.cached(t, "c2"))
	}
}

// While another process syncs, OpenProject fetches nothing but records the project: the
// next sync fetches it (incomplete first).
func TestOpenProjectWhileBusy(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	unlock, err := e.st.TryLockPush()
	if err != nil {
		t.Fatal(err)
	}
	e.fake.reset()
	_, err = e.s.OpenProject(ctx, "p1", false)
	unlock()
	if !errors.Is(err, ErrPushBusy) {
		t.Fatalf("err = %v, want ErrPushBusy", err)
	}
	if got := touches(e.fake.requests(), "p1"); len(got) > 0 {
		t.Errorf("busy open sent %v", got)
	}
	if got := e.state(t, readmodel.KeyProjectTasks+"p1"); got != readmodel.TasksWanted {
		t.Fatalf("state %q, want wanted", got)
	}
	e.now = e.now.Add(10 * time.Minute)
	e.run(t)
	if !e.cached(t, "a1") || !e.cached(t, "a2") {
		t.Fatal("the next sync did not fetch the project opened while busy")
	}
}

// A store synced before #54: its projects have events tokens, and its My Tasks was fetched
// with the completed tasks. They go on as before (no first fetch again).
func TestLazyStoreSyncedBefore(t *testing.T) {
	e := setupLazy(t, Config{CompletedDays: 14})
	e.run(t) // as before #54: every member project (p1, p2)
	e.s.Cfg = lazyCfg
	e.fake.reset()
	e.now = e.now.Add(10 * time.Minute) // not a poll
	e.run(t)
	log := e.fake.requests()
	if !slices.Contains(log, "GET /events resource=p1 sync=T-p1") {
		t.Errorf("p1 (synced before) not kept up to date: %v", touches(log, "p1"))
	}
	for _, q := range taskQueries(log) {
		if strings.Contains(q, "completed_since="+lazyStamp) {
			t.Errorf("fetched again for the first time: %s", q)
		}
	}
	if got := touches(log, "p3"); len(got) > 0 {
		t.Errorf("p3 requested: %v", got)
	}
	if ps, _ := e.st.ListProjects(context.Background(), true); len(ps) != 2 || ps[0].Unfetched || ps[1].Unfetched {
		t.Errorf("projects synced before show as unfetched: %+v", ps)
	}
}

// completed_days = 0 keeps no completed tasks: nothing is left for later.
func TestLazyCompletedDaysZero(t *testing.T) {
	e := setupLazy(t, Config{CompletedDays: 0, LazyProjects: true})
	e.s.DeferCompleted = true
	res := e.run(t)
	if res.CompletedPending {
		t.Error("CompletedPending with completed_days = 0")
	}
	for _, q := range taskQueries(e.fake.requests()) {
		if !strings.Contains(q, "completed_since="+lazyStamp) {
			t.Errorf("query %s", q)
		}
	}
	if got := e.state(t, readmodel.KeyProjectTasks+"p2"); got != readmodel.TasksAll {
		t.Errorf("p2 state %q", got)
	}
	// A negative number is 0, not a completed_since in the future.
	e2 := setupLazy(t, Config{CompletedDays: -3, LazyProjects: true})
	e2.run(t)
	for _, q := range taskQueries(e2.fake.requests()) {
		if !strings.Contains(q, "completed_since="+lazyStamp) {
			t.Errorf("negative completed_days: %s", q)
		}
	}
}

// lazy_projects = false syncs as before #54: every member project, the completed tasks with
// the incomplete ones.
func TestLazyOff(t *testing.T) {
	e := setupLazy(t, Config{CompletedDays: 14})
	res := e.run(t)
	log := e.fake.requests()
	if len(touches(log, "p1")) == 0 || len(touches(log, "p3")) > 0 || res.Projects != 2 || res.Listed != 1 {
		t.Errorf("synced %d listed %d; p1 %v p3 %v", res.Projects, res.Listed, touches(log, "p1"), touches(log, "p3"))
	}
	for _, q := range taskQueries(log) {
		if !strings.Contains(q, "completed_since="+lazyWindow) || strings.Contains(q, "modified_since=") {
			t.Errorf("query %s", q)
		}
	}
	// all_projects: every project from the start, in either mode.
	e2 := setupLazy(t, Config{CompletedDays: 14, LazyProjects: true, AllProjects: true})
	if res := e2.run(t); res.Projects != 3 || res.Listed != 0 {
		t.Errorf("all_projects: synced %d listed %d", res.Projects, res.Listed)
	}
}
