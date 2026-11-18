package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// hookedLazy is setupLazy with a hook in front of the fake: fail returns true to answer
// the request itself (the hook writes the response).
func hookedLazy(t *testing.T, cfg Config, hook func(w http.ResponseWriter, r *http.Request) bool) *lazyEnv {
	t.Helper()
	fake := &lazyAsana{t: t}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hook(w, r) {
			fake.mu.Lock()
			fake.log = append(fake.log, "FAILED "+r.Method+" "+r.URL.RequestURI())
			fake.mu.Unlock()
			return
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
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

// A 4xx on the completed fetch: the project stays open (incomplete tasks cached, token
// saved), and the next sync fetches the completed tasks.
func TestAttackCompletedFetchFailsThenRecovers(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	e := hookedLazy(t, lazyCfg, func(w http.ResponseWriter, r *http.Request) bool {
		if failing.Load() && r.URL.Path == "/tasks" && r.URL.Query().Get("project") == "p2" &&
			r.URL.Query().Get("completed_since") == lazyWindow {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"errors":[{"message":"bad"}]}`))
			return true
		}
		return false
	})
	if _, err := e.s.Run(context.Background(), false); err == nil {
		t.Fatal("want the failure of the completed fetch")
	}
	if got := e.state(t, readmodel.KeyProjectTasks+"p2"); got != readmodel.TasksOpen {
		t.Fatalf("p2 state %q, want open", got)
	}
	if !e.cached(t, "b1") || e.cached(t, "b2") {
		t.Fatal("want b1 cached and b2 not")
	}
	failing.Store(false)
	e.now = e.now.Add(10 * time.Minute)
	e.run(t)
	if !e.cached(t, "b2") || e.state(t, readmodel.KeyProjectTasks+"p2") != readmodel.TasksAll {
		t.Fatal("the next sync did not fetch p2's completed tasks")
	}
}

// Cancelled in the middle of the completed fetch: nothing is marked all.
func TestAttackCancelDuringCompleted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := hookedLazy(t, lazyCfg, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/tasks" && r.URL.Query().Get("completed_since") == lazyWindow {
			cancel()
		}
		return false
	})
	if _, err := e.s.Run(ctx, false); err == nil {
		t.Fatal("want an error after the cancel")
	}
	for _, k := range []string{"p2", "p1"} {
		if got := e.state(t, readmodel.KeyProjectTasks+k); got == readmodel.TasksAll {
			t.Errorf("%s marked all after a cancelled fetch", k)
		}
	}
	if got := e.state(t, readmodel.KeyMyTasks+"w1"); got == readmodel.TasksAll {
		t.Errorf("my tasks marked all after a cancelled fetch")
	}
}

// completed_days negative must not ask for a time to come (that would skip everything).
func TestAttackCompletedDaysNegative(t *testing.T) {
	e := setupLazy(t, Config{CompletedDays: -5, LazyProjects: true})
	e.run(t)
	e.now = e.now.Add(10 * time.Minute)
	e.run(t)
	for _, q := range taskQueries(e.fake.requests()) {
		for _, f := range strings.Fields(q) {
			if v, ok := strings.CutPrefix(f, "completed_since="); ok {
				ts, err := time.Parse(time.RFC3339, v)
				if err != nil || ts.After(e.now) {
					t.Errorf("completed_since in the future: %s", q)
				}
			}
		}
	}
}

// A queued local change on a task of a project not opened yet survives opening it.
func TestAttackOpenKeepsQueuedChange(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	if _, err := e.s.OpenProject(ctx, "p1", false); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateTaskLocal(ctx, "a1", map[string]any{"name": "LOCAL"}, e.now); err != nil {
		t.Fatal(err)
	}
	// forget p1 again, as if its state were lost, and open once more
	if _, err := e.s.OpenProject(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	tk, err := e.st.GetTask(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Name != "LOCAL" {
		t.Fatalf("queued change overwritten: name %q", tk.Name)
	}
}

// Opening an unknown (not cached) project is an error, not a panic or a recorded state.
func TestAttackOpenUnknownProject(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	e.run(t)
	e.fake.reset()
	_, err := e.s.OpenProject(context.Background(), "nope", false)
	if err == nil {
		t.Log("no error for an unknown project")
	}
	if got := touches(e.fake.requests(), "nope"); len(got) > 0 {
		t.Errorf("requests for an unknown project: %v", got)
	}
}

// Events token expired (412) for an opened project: refetched without losing completed state.
func TestAttackEventsTokenExpiredOnOpenedProject(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	if _, err := e.s.OpenProject(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSyncState(ctx, readmodel.KeyEvents+"p1", ""); err != nil { // token lost: 412 path
		t.Fatal(err)
	}
	e.now = e.now.Add(10 * time.Minute)
	e.run(t)
	if !e.cached(t, "a1") || !e.cached(t, "a2") {
		t.Fatal("tasks lost after a token reset")
	}
	if got := e.state(t, readmodel.KeyProjectTasks+"p1"); got != readmodel.TasksAll {
		t.Fatalf("state %q", got)
	}
}

// A full sync that defers the completed tasks must not prune completed tasks cached in a
// project that is TasksAll.
func TestAttackFullSyncDeferKeepsAllProjectCompleted(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	if _, err := e.s.OpenProject(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	e.s.DeferCompleted = true
	e.now = e.now.Add(48 * time.Hour)
	res, err := e.s.Run(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !e.cached(t, "a1") || !e.cached(t, "a2") {
		t.Fatalf("full sync pruned p1's tasks (a1 %v a2 %v, pruned %d)", e.cached(t, "a1"), e.cached(t, "a2"), res.Pruned)
	}
}

// A full sync with DeferCompleted, My Tasks open: its completed tasks already cached are kept.
func TestAttackFullSyncDeferKeepsMyCompleted(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	if _, err := e.s.FetchCompleted(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.cached(t, "m2") {
		t.Fatal("setup: m2 not cached")
	}
	e.s.DeferCompleted = true
	e.now = e.now.Add(48 * time.Hour)
	if _, err := e.s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !e.cached(t, "m1") || !e.cached(t, "m2") {
		t.Fatal("full sync pruned My Tasks")
	}
}

// Many OpenProject calls at once on the same project: one fetch, no error but ErrPushBusy.
func TestAttackConcurrentOpenProject(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	e.run(t)
	e.fake.reset()
	errs := make(chan error, 8)
	for range 8 {
		go func() { _, err := e.s.OpenProject(context.Background(), "p1", true); errs <- err }()
	}
	for range 8 {
		if err := <-errs; err != nil && !errorsIsBusy(err) {
			t.Errorf("open: %v", err)
		}
	}
	n := 0
	for _, q := range taskQueries(e.fake.requests()) {
		if strings.Contains(q, "project=p1") {
			n++
		}
	}
	if n > 2 {
		t.Errorf("p1's tasks requested %d times, want at most 2 (incomplete, completed)", n)
	}
	// A busy open still recorded the project, so it ends up fetched.
	e.now = e.now.Add(10 * time.Minute)
	e.run(t)
	if !e.cached(t, "a1") || !e.cached(t, "a2") {
		t.Fatal("p1 not fetched")
	}
}

func errorsIsBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), readmodel.ErrPushBusy.Error())
}

// Offline (every request fails): OpenProject records the project, leaves it wanted, and
// the next online sync fetches it.
func TestAttackOpenOffline(t *testing.T) {
	var offline atomic.Bool
	e := hookedLazy(t, lazyCfg, func(w http.ResponseWriter, r *http.Request) bool {
		if offline.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				c, _, _ := hj.Hijack()
				c.Close()
			}
			return true
		}
		return false
	})
	e.run(t)
	offline.Store(true)
	if _, err := e.s.OpenProject(context.Background(), "p1", true); err == nil {
		t.Fatal("want an error offline")
	}
	if got := e.state(t, readmodel.KeyProjectTasks+"p1"); got != readmodel.TasksWanted {
		t.Fatalf("state %q, want wanted", got)
	}
	if e.cached(t, "a1") {
		t.Fatal("a1 cached offline")
	}
	offline.Store(false)
	e.now = e.now.Add(10 * time.Minute)
	e.run(t)
	if !e.cached(t, "a1") || !e.cached(t, "a2") {
		t.Fatal("online sync did not fetch the opened project")
	}
}

// Project name from Asana with control characters must not reach an error message raw.
func TestAttackProjectNameInError(t *testing.T) {
	e := hookedLazy(t, lazyCfg, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/projects" && r.URL.Query().Get("workspace") != "" || r.URL.Path == "/projects":
			w.Write([]byte(`{"data":[{"gid":"p1","name":"Evil\u001b]0;pwn\u0007","members":[{"gid":"u1"}]}]}`))
			return true
		case r.URL.Path == "/tasks" && r.URL.Query().Get("project") == "p1":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"errors":[{"message":"bad"}]}`))
			return true
		}
		return false
	})
	e.run(t) // p1 is not wanted: no error
	_, err := e.s.OpenProject(context.Background(), "p1", true)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("control characters in the error: %q", err)
	}
}

// Lots of projects: a sync requests the tasks of the wanted ones only.
func TestAttackManyProjectsRequestCount(t *testing.T) {
	e := hookedLazy(t, lazyCfg, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/projects" {
			var sb strings.Builder
			sb.WriteString(`{"data":[`)
			for i := range 300 {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(`{"gid":"x` + strconv.Itoa(i) + `","name":"P","members":[{"gid":"u1"}]}`)
			}
			sb.WriteString(`]}`)
			w.Write([]byte(sb.String()))
			return true
		}
		return false
	})
	e.run(t)
	e.now = e.now.Add(10 * time.Minute)
	e.fake.reset()
	e.run(t)
	if n := len(e.fake.requests()); n > 40 {
		t.Errorf("a sync of 300 unopened projects sent %d requests", n)
	}
}

// Opening a project whose incomplete tasks are cached, without the completed ones, costs no request.
func TestAttackOpenOpenProjectIsFree(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	e.run(t)
	if _, err := e.s.OpenProject(context.Background(), "p1", false); err != nil {
		t.Fatal(err)
	}
	e.fake.reset()
	out, err := e.s.OpenProject(context.Background(), "p1", false)
	if err != nil || out.Fetched || out.Requests != 0 || len(e.fake.requests()) != 0 {
		t.Fatalf("%+v %v %v", out, err, e.fake.requests())
	}
}
