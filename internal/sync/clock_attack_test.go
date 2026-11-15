package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// #21 (tester): CreateProject is often the first request of a process (the CLI, the MCP
// server). If the connection drops after Asana created the project, no response has told
// the offset yet; the listing that recovery fetches does. With the PC clock 3 hours
// ahead, the project must still be recognised, not reported as "maybe not created".
func TestCreateProjectDroppedConnectionPCClockAhead(t *testing.T) {
	ctx := context.Background()
	shift := -3 * time.Hour // Asana's clock is 3 hours behind this PC's
	asanaNow := func() time.Time { return time.Now().Add(shift).UTC() }
	var posts int
	var projects []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", asanaNow().Format(http.TimeFormat))
		switch {
		case r.URL.Path == "/projects" && r.Method == http.MethodPost:
			posts++
			projects = append(projects, fmt.Sprintf(`{"gid":"P%d","name":"新規","workspace":{"gid":"w1"},"created_at":%q}`,
				posts, asanaNow().Format(time.RFC3339)))
			conn, _, err := w.(http.Hijacker).Hijack() // created, then the connection drops
			if err == nil {
				conn.Close()
			}
		case r.URL.Path == "/projects":
			fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(projects, ","))
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1"}})
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	s := &Syncer{API: api, Store: st, sleep: func(context.Context, time.Duration) error { return nil }}
	p, err := s.CreateProject(ctx, "新規")
	if posts != 1 {
		t.Fatalf("posts = %d: a project that may exist must not be created again", posts)
	}
	if err != nil || p == nil || p.GID != "P1" {
		t.Fatalf("created project not recognised with the PC clock ahead: %+v %v", p, err)
	}
}

// Same as the existing PC-ahead test, the other way round and for comments: Asana's clock
// ahead (PC behind) must not break finding what a 5xx create made.
func TestPushCreate5xxCreatedClockSkewBothWays(t *testing.T) {
	for _, shift := range []time.Duration{3 * time.Hour, -3 * time.Hour, 10 * time.Minute, -10 * time.Minute} {
		t.Run("task "+shift.String(), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ws.shift = shift
			ctx := context.Background()
			_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "ずれ"}, now)
			ws.setFailAfter("POST /tasks", 1)
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Remaining != 0 {
				t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
			}
			if n := ws.countCalls("POST /tasks "); n != 1 {
				t.Fatalf("task created %d times", n)
			}
		})
		t.Run("comment "+shift.String(), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ws.shift = shift
			ctx := context.Background()
			if _, err := st.AddCommentLocal(ctx, "t1", "コメント", store.UserRef{GID: "me", Name: "Me"}, now); err != nil {
				t.Fatal(err)
			}
			ws.setFailAfter("POST /tasks/t1/stories", 1)
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Remaining != 0 {
				t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
			}
			if n := ws.countCalls("POST /tasks/t1/stories"); n != 1 {
				t.Fatalf("comment posted %d times: duplicate", n)
			}
		})
	}
}

// The process that got the 5xx cannot look (offline right after it), and a new process
// (a new client: offset 0 until its first response) reconciles later. The send time was
// recorded by Asana's clock, so the created task is still found with the PC 3h ahead.
func TestPushReconcileInNewProcessPCClockAhead(t *testing.T) {
	ws, srv, s, st := setupPush(t)
	ws.shift = -3 * time.Hour
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "後で照合"}, now)
	ws.setFailAfter("POST /tasks", 1)
	posted := false
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/tasks" {
			posted = true
			return false
		}
		if posted && r.Method == http.MethodGet { // offline after the POST
			rw.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	if res, err := s.Push(ctx); res.Sent != 0 || res.Remaining != 1 {
		t.Fatalf("first push = %+v %v\n%v", res, err, ws.calls)
	}
	ws.setHook(nil)

	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	s2 := &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}
	later(s2, 10*time.Minute)
	res, err := s2.Push(ctx)
	if err != nil || res.Sent != 1 || res.Remaining != 0 {
		t.Fatalf("second push = %+v %v\n%v", res, err, ws.calls)
	}
	if n := ws.countCalls("POST /tasks "); n != 1 {
		t.Fatalf("task created %d times: duplicate", n)
	}
}

// #20 (tester): a full sync whose workspace list comes back empty (a broken answer) must
// not delete workspaces, projects or their keys.
func TestFullSyncEmptyWorkspaceListKeepsProjectsAndKeys(t *testing.T) {
	fake := &fakeAsana{t: t, projectTasks: `[]`}
	empty := false
	h := fake.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if empty && r.URL.Path == "/workspaces" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, store.KeyCollapsed+"123", `["x"]`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "123"}}, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, store.KeyCustomFields+"p1", "2026-11-16T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	empty = true
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	if ws, err := st.DefaultWorkspace(ctx); err != nil || ws != "w1" {
		t.Fatalf("workspace deleted on an empty answer: %q %v", ws, err)
	}
	for _, k := range []string{store.KeyCustomFields + "p1", store.KeyCollapsed + "123"} {
		if v, _ := st.SyncState(ctx, k); v == "" {
			t.Errorf("%s deleted on an empty workspace list", k)
		}
	}
}
