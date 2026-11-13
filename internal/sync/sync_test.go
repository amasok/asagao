package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

func TestFullSync(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"gid":"u1","name":"Me","email":"me@example.com"}}`)
	})
	mux.HandleFunc("/workspaces", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"gid":"w1","name":"WS"}]}`)
	})
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"gid":"p1","name":"Mine","members":[{"gid":"u1"}]},
			{"gid":"p2","name":"Others","members":[{"gid":"u2"}]}]}`)
	})
	mux.HandleFunc("/projects/p1/sections", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"gid":"s1","name":"Todo"},{"gid":"s2","name":"Done"}]}`)
	})
	mux.HandleFunc("/projects/p2/sections", func(w http.ResponseWriter, r *http.Request) {
		t.Error("non-member project should not be synced")
	})
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("completed_since") != "2026-10-30T00:00:00Z" {
			t.Errorf("completed_since = %q", r.URL.Query().Get("completed_since"))
		}
		switch {
		case r.URL.Query().Get("assignee") == "me":
			fmt.Fprint(w, `{"data":[{"gid":"t1","name":"My task","assignee":{"gid":"u1","name":"Me"},
				"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}],"tags":[{"gid":"g1","name":"urgent"}]}]}`)
		case r.URL.Query().Get("project") == "p1":
			fmt.Fprint(w, `{"data":[
				{"gid":"t1","name":"My task","assignee":{"gid":"u1"},"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}]},
				{"gid":"t2","name":"Done task","completed":true,"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s2"}}]}]}`)
		default:
			t.Errorf("unexpected tasks query %v", r.URL.Query())
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL

	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	s := &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}, now: func() time.Time { return now }}
	res, err := s.Full(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workspaces != 1 || res.Projects != 1 || res.Tasks != 3 {
		t.Fatalf("result %+v", res)
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Projects != 2 || stats.Sections != 2 || stats.Tasks != 2 || stats.OpenTasks != 1 {
		t.Fatalf("stats %+v", stats)
	}
	if stats.LastSync == nil || !stats.LastSync.Equal(now) {
		t.Fatalf("last sync %v", stats.LastSync)
	}

	// Re-running is idempotent.
	if _, err := s.Full(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.Stats(ctx); again.Tasks != 2 || again.Sections != 2 {
		t.Fatalf("after second sync %+v", again)
	}
}
