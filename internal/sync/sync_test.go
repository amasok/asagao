package sync

import (
	"context"
	"errors"
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

// fakeAsana serves a tiny workspace. Tasks returned for project p1 can be changed between syncs.
type fakeAsana struct {
	t              *testing.T
	projectTasks   string // JSON array for GET /tasks?project=p1
	modifiedSince  []string
	completedSince []string
	subtaskCalls   int
	t1aSubtasks    string // JSON array: if set, t1a has these subtasks (grandchildren of t1)
}

func (f *fakeAsana) handler() http.Handler {
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
		f.t.Error("non-member project should not be synced")
	})
	mux.HandleFunc("/tasks/t1/subtasks", func(w http.ResponseWriter, r *http.Request) {
		f.subtaskCalls++
		n := 0
		if f.t1aSubtasks != "" {
			n = 1
		}
		fmt.Fprintf(w, `{"data":[{"gid":"t1a","name":"Sub","num_subtasks":%d,"parent":{"gid":"t1"}}]}`, n)
	})
	mux.HandleFunc("/tasks/t1a/subtasks", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":%s}`, f.t1aSubtasks)
	})
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.completedSince = append(f.completedSince, q.Get("completed_since"))
		switch {
		case q.Get("assignee") == "me":
			f.modifiedSince = append(f.modifiedSince, q.Get("modified_since"))
			fmt.Fprint(w, `{"data":[{"gid":"t1","name":"My task","num_subtasks":1,"assignee":{"gid":"u1","name":"Me"},
				"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}],"tags":[{"gid":"g1","name":"urgent"}]}]}`)
		case q.Get("project") == "p1":
			fmt.Fprintf(w, `{"data":%s}`, f.projectTasks)
		default:
			f.t.Errorf("unexpected tasks query %v", q)
		}
	})
	return mux
}

func TestRunFullThenIncrementalThenPrune(t *testing.T) {
	fake := &fakeAsana{t: t, projectTasks: `[
		{"gid":"t1","name":"My task","num_subtasks":1,"assignee":{"gid":"u1"},"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}]},
		{"gid":"t2","name":"Done task","completed":true,"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s2"}}]}]`}
	srv := httptest.NewServer(fake.handler())
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

	// 1st run: full (never synced).
	res, err := s.Run(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Full || res.Projects != 1 {
		t.Fatalf("first run %+v", res)
	}
	if fake.completedSince[0] != "2026-10-30T00:00:00Z" || fake.modifiedSince[0] != "" {
		t.Fatalf("full sync query: completed_since=%q modified_since=%q", fake.completedSince[0], fake.modifiedSince[0])
	}
	stats, _ := st.Stats(ctx)
	if stats.Tasks != 3 || stats.OpenTasks != 2 || stats.Sections != 2 { // t1, t1a, t2
		t.Fatalf("stats after full %+v", stats)
	}
	if me, _ := st.MeGID(ctx); me != "u1" {
		t.Fatalf("me gid = %q", me)
	}

	// 2nd run an hour later: incremental with modified_since = last sync - margin.
	now = now.Add(time.Hour)
	fake.projectTasks = `[]` // nothing changed; t2 must NOT be removed by an incremental sync
	res, err = s.Run(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Full {
		t.Fatalf("second run should be incremental")
	}
	if got := fake.modifiedSince[1]; got != "2026-11-12T23:58:00Z" {
		t.Fatalf("modified_since = %q", got)
	}
	if stats, _ := st.Stats(ctx); stats.Tasks != 3 {
		t.Fatalf("incremental sync removed tasks: %+v", stats)
	}

	// 3rd run, forced full: t2 is gone from Asana and gets pruned; t1's subtask is kept.
	res, err = s.Run(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Full || res.Pruned != 1 {
		t.Fatalf("forced full %+v", res)
	}
	if _, err := st.GetTask(ctx, "t2"); err == nil {
		t.Fatal("t2 should have been pruned")
	}
	if d, err := st.GetTask(ctx, "t1"); err != nil || len(d.Subtasks) != 1 {
		t.Fatalf("t1 = %+v, %v", d, err)
	}

	// Full sync happens automatically after FullSyncInterval.
	now = now.Add(FullSyncInterval)
	if res, err = s.Run(ctx, false); err != nil || !res.Full {
		t.Fatalf("expected automatic full sync: %+v %v", res, err)
	}
}

func TestRunFetchesNestedSubtasks(t *testing.T) {
	fake := &fakeAsana{t: t, projectTasks: `[]`,
		t1aSubtasks: `[{"gid":"t1a1","name":"孫","parent":{"gid":"t1a"}}]`}
	srv := httptest.NewServer(fake.handler())
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
	if d, err := st.GetTask(ctx, "t1a"); err != nil || len(d.Subtasks) != 1 || d.Subtasks[0].GID != "t1a1" {
		t.Fatalf("grandchild not synced: %+v %v", d, err)
	}
	// A full sync keeps (does not prune) the grandchild.
	if res, err := s.Run(ctx, true); err != nil || res.Pruned != 0 {
		t.Fatalf("second full sync = %+v %v", res, err)
	}
}

// Asana answers as another account than the one the store was synced with: Run stops
// before sending or fetching anything, and keeps the stored account.
func TestRunStopsWhenAccountChanged(t *testing.T) {
	fake := &fakeAsana{t: t, projectTasks: `[]`}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SetSyncState(ctx, store.KeyMeGID, "old-account")
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "前のアカウント"}})
	_ = st.UpdateTaskLocal(ctx, "t9", map[string]any{"completed": true}, time.Now())
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	_, err = (&Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}).Run(ctx, false)
	if !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("run = %v", err)
	}
	if me, _ := st.SyncState(ctx, store.KeyMeGID); me != "old-account" {
		t.Fatalf("stored account overwritten: %q", me)
	}
	if p, _, _, _ := st.OutboxCounts(ctx); p != 1 || len(fake.modifiedSince) != 0 {
		t.Fatalf("nothing may be sent or fetched: pending=%d fetches=%d", p, len(fake.modifiedSince))
	}
}
