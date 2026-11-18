package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// #54: list_tasks on a project whose tasks are not cached fetches them first (online), or
// says they are missing (offline); search_tasks says how many projects it did not look in.

func lazyServer(t *testing.T) (*httptest.Server, func() []string) {
	var mu gosync.Mutex
	var log []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		log = append(log, r.Method+" "+r.URL.Path+" "+q.Get("project"))
		mu.Unlock()
		switch p := r.URL.Path; {
		case p == "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case p == "/events":
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, `{"errors":[{"message":"Sync token invalid"}],"sync":"T1"}`)
		case p == "/projects/p2/sections":
			fmt.Fprint(w, `{"data":[{"gid":"s9","name":"Todo"}]}`)
		case p == "/projects/p2/custom_field_settings", p == "/status_updates":
			fmt.Fprint(w, `{"data":[]}`)
		case p == "/tasks" && q.Get("project") == "p2":
			fmt.Fprint(w, `{"data":[{"gid":"x1","name":"見積もり","assignee":{"gid":"u2"},"memberships":[{"project":{"gid":"p2"},"section":{"gid":"s9"}}]}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"not here"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), log...)
	}
}

func TestListTasksFetchesUnfetchedProject(t *testing.T) {
	st := seedProjects(t)
	srv, requests := lazyServer(t)
	cs := connect(t, &Server{Store: st, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14, LazyProjects: true}}, nil
	}})
	var out TasksOutput
	call(t, cs, "list_tasks", map[string]any{"project": "仕事", "assignee": "any"}, &out)
	if out.Count != 1 || out.Tasks[0].GID != "x1" || out.Note != "" {
		t.Fatalf("list_tasks = %+v (requests %v)", out, requests())
	}
	if s, _ := st.ProjectTaskState(context.Background(), "p2"); s != readmodel.TasksAll {
		t.Fatalf("p2 state %q", s)
	}
	// Cached now: listed again without asking Asana.
	n := len(requests())
	call(t, cs, "list_tasks", map[string]any{"project": "仕事", "assignee": "any"}, &out)
	if len(requests()) != n || out.Count != 1 {
		t.Fatalf("second list_tasks asked Asana again: %v", requests()[n:])
	}
}

func TestListTasksUnfetchedProjectOffline(t *testing.T) {
	st := seedProjects(t)
	cs := connect(t, &Server{Store: st}) // offline
	var out TasksOutput
	call(t, cs, "list_tasks", map[string]any{"project": "仕事", "assignee": "any"}, &out)
	if out.Count != 0 || !strings.Contains(out.Note, "not in the local cache") || !strings.Contains(out.Note, "next sync") {
		t.Fatalf("offline list_tasks = %+v", out)
	}
	// Recorded: the next sync fetches it.
	if s, _ := st.ProjectTaskState(context.Background(), "p2"); s != readmodel.TasksWanted {
		t.Fatalf("p2 state %q, want wanted", s)
	}
	var ps ProjectsOutput
	call(t, cs, "list_projects", nil, &ps)
	for _, p := range ps.Projects {
		if p.Unfetched != (p.GID == "p2") {
			t.Errorf("%s unfetched = %v", p.GID, p.Unfetched)
		}
	}
	var so SearchOutput
	call(t, cs, "search_tasks", map[string]any{"query": "ソファ"}, &so)
	if so.Count != 1 || !strings.Contains(so.Note, "1 of the user's projects are not cached") {
		t.Fatalf("search_tasks = %+v", so)
	}
	if err := st.SetSyncState(context.Background(), store.KeyProjectTasks+"p2", readmodel.TasksAll); err != nil {
		t.Fatal(err)
	}
	so = SearchOutput{} // decoded afresh: an omitted note must not keep the old one
	call(t, cs, "search_tasks", map[string]any{"query": "ソファ"}, &so)
	if so.Note != "" {
		t.Fatalf("every project cached, note %q", so.Note)
	}
}
