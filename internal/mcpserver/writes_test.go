package mcpserver

import (
	"context"
	"fmt"
	"github.com/amasok/asagao/internal/wire"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// snapshot is everything a failed write must leave untouched.
type snapshot struct {
	Task     *store.TaskDetail
	Projects []store.ProjectView
	Ops      []store.Op
}

func take(t *testing.T, st *store.Store, taskGID string) snapshot {
	t.Helper()
	ctx := context.Background()
	d, err := st.GetTask(ctx, taskGID)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := st.ListProjects(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot{d, ps, ops}
}

// A write that fails on any of its inputs must not save the other parts.
func TestFailedWritesChangeNothing(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	if err := st.UpsertUsers(ctx, []asana.User{{GID: "u2", Name: "Partner"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p2", Name: "家計"}, {GID: "p3", Name: "家計簿"}}, "me"); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{App: wire.UseCases(st)}) // offline: writes are only queued

	valid := map[string]any{"gid": "t1", "name": "新しい名前", "due_on": "tomorrow", "completed": true,
		"add_tags": []string{"新タグ"}, "add_followers": []string{"Partner"}, "add_projects": []string{"p2"}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for k, v := range valid {
			m[k] = v
		}
		m[k] = v
		return m
	}
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want string // part of the error
	}{
		{"update_task: unknown tag to remove", "update_task", with("remove_tags", []string{"ない"}), "no such tag"},
		{"update_task: unknown follower", "update_task", with("add_followers", []string{"Partner", "誰か"}), "add_followers"},
		{"update_task: unknown follower to remove", "update_task", with("remove_followers", []string{"誰か"}), "remove_followers"},
		{"update_task: follower none", "update_task", with("add_followers", []string{"none"}), "add_followers"},
		{"update_task: unknown project", "update_task", with("add_projects", []string{"ない"}), "add_projects"},
		{"update_task: ambiguous project", "update_task", with("remove_projects", []string{"家"}), "remove_projects"},
		{"update_task: unknown assignee", "update_task", with("assignee", "誰か"), "誰か"},
		{"update_task: bad due date", "update_task", with("due_on", "someday"), "due_on"},
		{"update_task: unknown task", "update_task", with("gid", "nope"), "nope"},
		{"create_task: bad assignee", "create_task", map[string]any{"name": "x", "assignee": "誰か", "tags": []string{"新タグ"}}, "誰か"},
		{"create_task: empty tag", "create_task", map[string]any{"name": "x", "tags": []string{"#"}}, "タグ名"},
		{"move_task: unknown section", "move_task", map[string]any{"gid": "t1", "section": "ない"}, "ない"},
		{"move_task: unknown project", "move_task", map[string]any{"gid": "t1", "section": "手続き", "project": "ない"}, "ない"},
		{"move_task: unknown task", "move_task", map[string]any{"gid": "nope", "section": "手続き"}, "nope"},
		{"rename_section: unknown section", "rename_section", map[string]any{"project": "新居", "section": "ない", "name": "x"}, "ない"},
		{"rename_section: unknown project", "rename_section", map[string]any{"project": "ない", "section": "家具", "name": "x"}, "ない"},
		{"update_project: unknown member", "update_project", map[string]any{"project": "新居", "archived": true, "add_members": []string{"誰か"}}, "add_members"},
		{"update_project: nothing to do", "update_project", map[string]any{"project": "新居"}, "nothing to update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := take(t, st, "t1")
			msg := toolError(call(t, cs, tc.tool, tc.args, nil))
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error = %q, want it to mention %q", msg, tc.want)
			}
			if after := take(t, st, "t1"); !reflect.DeepEqual(before, after) {
				t.Fatalf("a failed %s changed the store:\nbefore %+v\nafter  %+v", tc.tool, before, after)
			}
			tags, _ := st.ListTags(ctx)
			for _, tag := range tags {
				if tag.Name == "新タグ" {
					t.Fatal("a failed write created a tag")
				}
			}
		})
	}

	// The same update without the bad part goes through as a whole.
	var w WriteOutput
	call(t, cs, "update_task", valid, &w)
	d, _ := st.GetTask(ctx, "t1")
	if d.Name != "新しい名前" || !d.Completed || len(d.Tags) != 1 || len(d.Followers) != 1 || len(d.Memberships) != 2 {
		t.Fatalf("valid update: %+v", d)
	}
}

// projectAPI records project calls and can fail one kind of request.
type projectAPI struct {
	mu       stdsync.Mutex
	calls    []string
	failPath string // "archive" or "members"
}

func (p *projectAPI) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := "archive"
		if strings.Contains(r.URL.Path, "Members") {
			kind = "members"
		}
		p.mu.Lock()
		p.calls = append(p.calls, kind)
		p.mu.Unlock()
		if kind == p.failPath {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":[{"message":"forbidden"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"gid":"p1","name":"[家族]新居プロジェクト","members":[{"gid":"me"},{"gid":"u2"}]}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateProjectOrderAndPartialFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      map[string]any
		failPath  string
		wantCalls []string
		wantErr   string
	}{
		{"archive after members", map[string]any{"archived": true, "add_members": []string{"Partner"}}, "", []string{"members", "archive"}, ""},
		{"unarchive before members", map[string]any{"archived": false, "add_members": []string{"Partner"}}, "", []string{"archive", "members"}, ""},
		{"members fail: archive not tried", map[string]any{"archived": true, "add_members": []string{"Partner"}}, "members", []string{"members"}, "forbidden"},
		{"archive fails after members", map[string]any{"archived": true, "add_members": []string{"Partner"}}, "archive", []string{"members", "archive"}, "members was applied"},
		{"unknown member: nothing sent", map[string]any{"archived": true, "remove_members": []string{"誰か"}}, "", nil, "remove_members"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := seed(t)
			_ = st.UpsertUsers(context.Background(), []asana.User{{GID: "u2", Name: "Partner"}})
			api := &projectAPI{failPath: tc.failPath}
			srv := api.server(t)
			cs := connect(t, &Server{App: wire.UseCases(st), NewSyncer: func(context.Context) (*sync.Syncer, error) {
				c := asana.NewClient(srv.Client())
				c.BaseURL = srv.URL
				return &sync.Syncer{API: c, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
			}})
			args := map[string]any{"project": "新居"}
			for k, v := range tc.args {
				args[k] = v
			}
			msg := toolError(call(t, cs, "update_project", args, nil))
			if (tc.wantErr == "") != (msg == "") || !strings.Contains(msg, tc.wantErr) {
				t.Fatalf("error = %q, want %q", msg, tc.wantErr)
			}
			if !reflect.DeepEqual(api.calls, tc.wantCalls) {
				t.Fatalf("calls = %v, want %v", api.calls, tc.wantCalls)
			}
		})
	}
}

func TestResolveOutboxStateRules(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	cs := connect(t, &Server{App: wire.UseCases(st)})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow)
	_ = st.RenameSectionLocal(ctx, "s1", "家具2", testNow)
	ops, _ := st.ListOps(ctx)
	task, section := ops[0].ID, ops[1].ID

	for _, action := range []string{"keep_local", "keep_remote", "retry"} {
		if msg := toolError(call(t, cs, "resolve_outbox", map[string]any{"id": task, "action": action}, nil)); !strings.Contains(msg, "ではありません") {
			t.Errorf("%s on a pending entry: error = %q", action, msg)
		}
	}
	if msg := toolError(call(t, cs, "resolve_outbox", map[string]any{"id": task, "action": "nope"}, nil)); !strings.Contains(msg, "keep_local") {
		t.Errorf("bad action error = %q", msg)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 2 {
		t.Fatalf("refused actions must keep the entries: %+v", ops)
	}
	// discarding a section rename does not try to fetch the section as a task
	var w WriteOutput
	call(t, cs, "resolve_outbox", map[string]any{"id": section, "action": "discard"}, &w)
	if strings.Contains(w.Note, "next sync") {
		t.Errorf("section discard should not refresh: %+v", w)
	}
	// offline: dropping a task change says the server's values come with the next sync
	call(t, cs, "resolve_outbox", map[string]any{"id": task, "action": "discard"}, &w)
	if !strings.Contains(w.Note, "next sync") {
		t.Errorf("task discard offline should mention the next sync: %+v", w)
	}
}

// While another process holds the push lock, a write is queued and says so (not "offline").
func TestPushBusyIsNotOffline(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	srv := fakeAsana(t)
	cs := connect(t, &Server{App: wire.UseCases(st), NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}})
	unlock, err := st.TryLockPush() // "another process" is sending
	if err != nil {
		t.Fatal(err)
	}
	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t2", "completed": true}, &w)
	unlock()
	if w.Sent || !strings.Contains(w.Note, "another asagao process") || strings.Contains(w.Note, "could not be reached") {
		t.Fatalf("busy push = %+v", w)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].State != store.OpPending {
		t.Fatalf("the change should stay queued: %+v", ops)
	}

	// an entry being sent cannot be resolved
	if ok, err := st.BeginSend(ctx, ops[0].ID, testNow); !ok || err != nil {
		t.Fatalf("BeginSend = %v, %v", ok, err)
	}
	if msg := toolError(call(t, cs, "resolve_outbox", map[string]any{"id": ops[0].ID, "action": "discard"}, nil)); !strings.Contains(msg, "送信中") {
		t.Fatalf("resolving a sending entry: error = %q", msg)
	}
}

// Logged in as someone else: writes stay queued and both writes and sync explain what
// happened and what the user can do; the server itself never resets.
func TestAccountChangedIsExplained(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/me" {
			fmt.Fprint(w, `{"data":{"gid":"other","name":"Other"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound) // another account sees nothing of ours
		fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
	}))
	t.Cleanup(srv.Close)
	cs := connect(t, &Server{App: wire.UseCases(st), NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}})

	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t2", "completed": true}, &w)
	if w.Sent || !strings.Contains(w.Note, "NOT sent") || !strings.Contains(w.Note, "asagao reset") || !strings.Contains(w.Note, "1 unsent") {
		t.Fatalf("write while the account changed: %+v", w)
	}
	if n, _ := st.UnsentCount(ctx); n != 1 {
		t.Fatalf("the change must stay queued: %d", n)
	}
	msg := toolError(call(t, cs, "sync", nil, nil))
	if !strings.Contains(msg, "sync stopped") || !strings.Contains(msg, "auth login") || !strings.Contains(msg, "cannot reset") {
		t.Fatalf("sync error = %q", msg)
	}
	if _, err := st.GetTask(ctx, "t1"); err != nil {
		t.Fatalf("local data must be kept: %v", err)
	}
}

// A sync while another process syncs is not an error: the output says why nothing happened.
func TestSyncBusyIsNotAnError(t *testing.T) {
	st := seed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/me" { // Run checks the account, then finds the lock taken
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
			return
		}
		t.Errorf("nothing else should be requested: %s %s", r.Method, r.URL)
	}))
	t.Cleanup(srv.Close)
	cs := connect(t, &Server{App: wire.UseCases(st), NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}})
	unlock, err := st.TryLockPush()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var out SyncOutput
	res := call(t, cs, "sync", nil, &out)
	if res.IsError || !strings.Contains(out.Note, "another asagao process") || strings.Contains(out.Note, "offline") {
		t.Fatalf("busy sync: error=%q out=%+v", toolError(res), out)
	}
}
