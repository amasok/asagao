package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// #54: a project whose tasks are not cached is marked in the sidebar; once the cursor rests
// on it, its incomplete tasks are fetched and shown, then its completed ones.

// lazyFixture is fixtureStore with p9, a project I am a member of whose tasks are not cached.
func lazyFixture(t *testing.T) *store.Store {
	t.Helper()
	st := fixtureStore(t)
	if err := st.UpsertProjects(context.Background(), "w1", []asana.Project{{GID: "p9", Name: "龍プロジェクト", Members: []asana.Ref{{GID: "me"}}}}, "me"); err != nil {
		t.Fatal(err)
	}
	return st
}

// lazyTUIServer answers what OpenProject and FetchCompleted ask for p9 and My Tasks.
func lazyTUIServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		withCompleted := false
		if cs, err := time.Parse(time.RFC3339, q.Get("completed_since")); err == nil {
			withCompleted = cs.Before(time.Now().Add(-time.Hour))
		}
		task := func(gid, name, project string, completed bool) string {
			return fmt.Sprintf(`{"gid":%q,"name":%q,"completed":%t,"assignee":{"gid":"me"},"memberships":[{"project":{"gid":%q},"section":{"gid":"s9"}}]}`,
				gid, name, completed, project)
		}
		list := func(open, done string) {
			if withCompleted {
				fmt.Fprintf(w, `{"data":[%s,%s]}`, open, done)
			} else {
				fmt.Fprintf(w, `{"data":[%s]}`, open)
			}
		}
		switch p := r.URL.Path; {
		case p == "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case p == "/events":
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, `{"errors":[{"message":"Sync token invalid"}],"sync":"T1"}`)
		case p == "/projects/p9/sections":
			fmt.Fprint(w, `{"data":[{"gid":"s9","name":"やること"}]}`)
		case p == "/projects/p9/custom_field_settings", p == "/status_updates":
			fmt.Fprint(w, `{"data":[]}`)
		case p == "/tasks" && q.Get("project") == "p9":
			list(task("x1", "龍の未完了", "p9", false), task("x2", "龍の完了", "p9", true))
		case p == "/tasks" && q.Get("assignee") == "me":
			list(task("t1", "ソファを買う", "p1", false), task("md", "済んだ自分のタスク", "p1", true))
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"not here"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lazySyncer(t *testing.T, st *store.Store) *sync.Syncer {
	srv := lazyTUIServer(t)
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14, LazyProjects: true}}
}

func TestTUIUnfetchedProjectOffline(t *testing.T) {
	st := lazyFixture(t)
	h := newHarnessWith(t, st)
	if !strings.Contains(h.screen(), unfetchedMark+"龍プロジェクト") {
		t.Fatalf("sidebar does not mark p9:\n%s", h.screen())
	}
	if strings.Contains(h.screen(), unfetchedMark+"新居プロジェクト") {
		t.Fatal("a cached project is marked")
	}
	h.key("h", "G") // rests on p9 (the tick fires in the harness)
	if h.m.currentProject() != "p9" {
		t.Fatalf("current project %q", h.m.currentProject())
	}
	if !strings.Contains(h.screen(), "まだ取得していません") {
		t.Fatalf("no note for the unfetched project:\n%s", h.screen())
	}
	if s, _ := st.ProjectTaskState(context.Background(), "p9"); s != readmodel.TasksWanted {
		t.Fatalf("p9 state %q: offline, the open must be recorded for the next sync", s)
	}
}

func TestTUIOpenProjectFetchesIncompleteThenCompleted(t *testing.T) {
	st := lazyFixture(t)
	h := newHarnessWith(t, st)
	h.m.syncer = lazySyncer(t, st)
	h.key("h", "G")
	ctx := context.Background()
	if s, _ := st.ProjectTaskState(ctx, "p9"); s != readmodel.TasksAll {
		t.Fatalf("p9 state %q, want all (incomplete, then completed)", s)
	}
	for _, gid := range []string{"x1", "x2"} {
		if _, err := st.GetTask(ctx, gid); err != nil {
			t.Fatalf("%s not cached: %v", gid, err)
		}
	}
	scr := h.screen()
	if !strings.Contains(scr, "龍の未完了") || strings.Contains(scr, "まだ取得していません") || strings.Contains(scr, unfetchedMark+"龍") {
		t.Fatalf("after the open:\n%s", scr)
	}
}

// Moving through the sidebar fetches nothing: only the project the cursor rests on.
func TestTUIPassingByFetchesNothing(t *testing.T) {
	st := lazyFixture(t)
	h := newHarnessWith(t, st)
	h.m.syncer = lazySyncer(t, st)
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	h.run(cmd)
	_, cmd = h.m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}) // on p9: a tick is pending
	_, cmd2 := h.m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	h.run(cmd2)
	h.run(cmd) // the tick for p9 fires after the cursor left it
	if s, _ := st.ProjectTaskState(context.Background(), "p9"); s != "" {
		t.Fatalf("p9 state %q: passed by, it must not be fetched", s)
	}
}

// A sync that left the completed tasks for later (the TUI asks so) fetches them next.
func TestTUIFetchesCompletedAfterSync(t *testing.T) {
	st := lazyFixture(t)
	ctx := context.Background()
	if err := st.SetMyTaskState(ctx, "w1", readmodel.TasksOpen); err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, st)
	s := lazySyncer(t, st)
	h.send(syncDoneMsg{syncer: s, res: sync.Result{CompletedPending: true}, at: testNow})
	if _, err := st.GetTask(ctx, "md"); err != nil {
		t.Fatalf("my completed task not fetched after the sync: %v", err)
	}
	if h.m.syncing {
		t.Fatal("still syncing")
	}
	if v, _ := st.MyTaskState(ctx, "w1"); v != readmodel.TasksAll {
		t.Fatalf("my tasks state %q", v)
	}
}
