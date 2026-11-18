package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// search_tasks with remote, and names the cache lacks (Issue #36).

type findAsana struct {
	searchStatus int
	searches     int
	typeaheads   []string
	puts         []map[string]any
}

func (f *findAsana) serve(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.Method + " " + r.URL.Path {
		case "GET /workspaces/w1/tasks/search":
			f.searches++
			if f.searchStatus != 0 {
				w.WriteHeader(f.searchStatus)
				fmt.Fprint(w, `{"errors":[{"message":"premium only"}]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"gid":"t9"}]}`)
		case "GET /workspaces/w1/typeahead":
			f.typeaheads = append(f.typeaheads, q.Get("resource_type")+":"+q.Get("query"))
			switch q.Get("resource_type") {
			case "task":
				fmt.Fprint(w, `{"data":[{"gid":"t9","name":"古い見積もり"}]}`)
			case "user":
				fmt.Fprint(w, `{"data":[{"gid":"u7","name":"Hanako Yamada","email":"hanako@example.com"}]}`)
			default:
				fmt.Fprint(w, `{"data":[]}`)
			}
		case "GET /tasks/t9":
			fmt.Fprint(w, `{"data":{"gid":"t9","name":"古い見積もり","completed":true,"workspace":{"gid":"w1"}}}`)
		case "GET /tasks/t1":
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"ソファを買う","assignee":{"gid":"me"},"workspace":{"gid":"w1"}}}`)
		case "GET /users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case "PUT /tasks/t1":
			b, _ := io.ReadAll(r.Body)
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.Unmarshal(b, &body)
			f.puts = append(f.puts, body.Data)
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"ソファを買う","assignee":{"gid":"u7"},"workspace":{"gid":"w1"}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"errors":[{"message":"not in the fake: %s %s"}]}`, r.Method, r.URL.Path)
		}
	}))
}

func findServer(t *testing.T, st *store.Store, f *findAsana, readOnly bool) *Server {
	srv := f.serve(t)
	t.Cleanup(srv.Close)
	return &Server{Store: st, ReadOnly: readOnly, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL, api.MaxRetries = srv.URL, 0
		return &sync.Syncer{API: api, Store: st, PullOnly: readOnly}, nil
	}}
}

func TestSearchTasksRemote(t *testing.T) {
	st := seed(t)
	f := &findAsana{}
	cs := connect(t, findServer(t, st, f, true)) // read-only: searching Asana is a read
	var out SearchOutput
	call(t, cs, "search_tasks", map[string]any{"query": "見積もり"}, &out)
	if out.Count != 0 || f.searches != 0 {
		t.Fatalf("without remote: %+v, searches %d", out, f.searches)
	}
	call(t, cs, "search_tasks", map[string]any{"query": "見積もり", "remote": true}, &out)
	if out.Count != 1 || out.Tasks[0].GID != "t9" || out.RemoteFound != 1 || out.Note != "" {
		t.Fatalf("remote: %+v", out)
	}
	if _, err := st.GetTask(context.Background(), "t9"); err != nil {
		t.Fatalf("not cached: %v", err)
	}
	// without the search API: typeahead, and the note says so
	f.searchStatus = http.StatusPaymentRequired
	call(t, cs, "search_tasks", map[string]any{"query": "古い", "remote": true, "completion": "completed"}, &out)
	if out.Count != 1 || !strings.Contains(out.Note, "start of task names") || len(f.typeaheads) != 1 {
		t.Fatalf("typeahead: %+v %v", out, f.typeaheads)
	}
}

func TestSearchTasksRemoteOffline(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st}) // no syncer: offline
	var out SearchOutput
	call(t, cs, "search_tasks", map[string]any{"query": "ソファ", "remote": true}, &out)
	if out.Count != 1 || !strings.Contains(out.Note, "could not be searched") {
		t.Fatalf("offline: %+v", out)
	}
}

func TestUpdateTaskLooksUpAnAssigneeTheCacheLacks(t *testing.T) {
	st := seed(t)
	f := &findAsana{}
	cs := connect(t, findServer(t, st, f, false))
	var w WriteOutput
	res := call(t, cs, "update_task", map[string]any{"gid": "t1", "assignee": "hanako"}, &w)
	if res.IsError || len(f.typeaheads) != 1 || f.typeaheads[0] != "user:hanako" {
		t.Fatalf("update_task: %v %+v typeahead %v", toolError(res), w, f.typeaheads)
	}
	if len(f.puts) != 1 || f.puts[0]["assignee"] != "u7" {
		t.Fatalf("puts %v, out %+v", f.puts, w)
	}
}
