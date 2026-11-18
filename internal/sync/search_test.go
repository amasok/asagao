package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// searchServer serves the search API, typeahead and the tasks they find (Issue #36). POST
// /batch is not served, so tasks are read one by one (GET /tasks/{gid}).
type searchServer struct {
	t             *testing.T
	searchStatus  int // != 0: the search API answers this status
	typeStatus    int // != 0: typeahead answers this status
	searchCalls   int
	typeCalls     map[string]int // resource_type -> calls
	searchQueries []string
	customStatus  int
}

func (f *searchServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /workspaces/w1/tasks/search", func(w http.ResponseWriter, r *http.Request) {
		f.searchCalls++
		f.searchQueries = append(f.searchQueries, r.URL.RawQuery)
		if f.searchStatus != 0 {
			w.WriteHeader(f.searchStatus)
			fmt.Fprint(w, `{"errors":[{"message":"premium only"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"gid":"t2"},{"gid":"t1"},{"gid":"t2"},{"gid":"local:x"}]}`)
	})
	mux.HandleFunc("GET /workspaces/w1/typeahead", func(w http.ResponseWriter, r *http.Request) {
		rt := r.URL.Query().Get("resource_type")
		f.typeCalls[rt]++
		if f.typeStatus != 0 {
			w.WriteHeader(f.typeStatus)
			fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
			return
		}
		switch rt {
		case "task":
			fmt.Fprint(w, `{"data":[{"gid":"t1","name":"古い完了タスク"},{"gid":"t3","name":"開いたタスク"}]}`)
		case "user":
			fmt.Fprint(w, `{"data":[{"gid":"u7","name":"Hanako Yamada","email":"hanako@example.com"},{"gid":"u1","name":"Me","email":""}]}`)
		case "tag":
			fmt.Fprint(w, `{"data":[{"gid":"g7","name":"買い物","color":"dark-red"}]}`)
		}
	})
	for _, task := range []string{
		`{"gid":"t1","name":"古い完了タスク","completed":true,"notes":"本文","memberships":[{"project":{"gid":"p9","name":"よそ"}}]}`,
		`{"gid":"t2","name":"説明に語があるタスク","notes":"牛乳"}`,
		`{"gid":"t3","name":"開いたタスク"}`,
	} {
		gid := task[8:10]
		mux.HandleFunc("GET /tasks/"+gid, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"data":%s}`, task)
		})
	}
	mux.HandleFunc("GET /workspaces/w1/tasks/custom_id/{id}", func(w http.ResponseWriter, r *http.Request) {
		if f.customStatus != 0 {
			w.WriteHeader(f.customStatus)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
			return
		}
		if r.PathValue("id") != "ENG-123" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"gid":"t3","name":"開いたタスク"}}`)
	})
	return mux
}

func setupSearch(t *testing.T, f *searchServer) (*Syncer, *store.Store) {
	t.Helper()
	f.t = t
	f.typeCalls = map[string]int{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertWorkspaces(ctx, []model.Workspace{{GID: "w1", Name: "WS"}}); err != nil {
		t.Fatal(err)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	return &Syncer{API: api, Store: st, now: func() time.Time { return now }}, st
}

func TestSearchTasksCachesWhatTheSearchAPIFinds(t *testing.T) {
	f := &searchServer{}
	s, st := setupSearch(t, f)
	ctx := context.Background()
	res, err := s.SearchTasks(ctx, "  牛乳 ", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	// Asana's order, without repeats or local gids
	if !slices.Equal(res.GIDs, []string{"t2", "t1"}) || res.ByName {
		t.Fatalf("res %+v", res)
	}
	if len(f.searchQueries) != 1 || f.typeCalls["task"] != 0 {
		t.Fatalf("calls: search %v typeahead %v", f.searchQueries, f.typeCalls)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil || d.Name != "古い完了タスク" || !d.Completed || d.Notes != "本文" {
		t.Fatalf("t1 not cached in full: %+v %v", d, err)
	}
}

func TestSearchTasks402SwitchesToTypeaheadAndRemembers(t *testing.T) {
	f := &searchServer{searchStatus: http.StatusPaymentRequired}
	s, st := setupSearch(t, f)
	ctx := context.Background()
	open := false
	res, err := s.SearchTasks(ctx, "開いた", &open, 20)
	if err != nil {
		t.Fatal(err)
	}
	// typeahead does not filter: the completed t1 is left out here
	if !slices.Equal(res.GIDs, []string{"t3"}) || !res.ByName {
		t.Fatalf("res %+v", res)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanSearch); v == "" {
		t.Fatal("402 not remembered")
	}
	if _, err := st.GetTask(ctx, "t1"); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("filtered-out task cached: %v", err)
	}
	// next time the search API is not asked
	if _, err := s.SearchTasks(ctx, "開いた", nil, 20); err != nil {
		t.Fatal(err)
	}
	if f.searchCalls != 1 || f.typeCalls["task"] != 2 {
		t.Fatalf("search %d typeahead %v", f.searchCalls, f.typeCalls)
	}
}

func TestSearchTasksOtherErrorsDoNotSwitch(t *testing.T) {
	f := &searchServer{searchStatus: http.StatusForbidden}
	s, st := setupSearch(t, f)
	ctx := context.Background()
	if _, err := s.SearchTasks(ctx, "x", nil, 20); err == nil {
		t.Fatal("403 not reported")
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanSearch); v != "" {
		t.Fatal("403 remembered as a plan limit")
	}
	if f.typeCalls["task"] != 0 {
		t.Fatal("typeahead asked after a 403")
	}
}

func TestSearchTasksWithoutTypeahead(t *testing.T) {
	f := &searchServer{searchStatus: http.StatusPaymentRequired, typeStatus: http.StatusPaymentRequired}
	s, st := setupSearch(t, f)
	ctx := context.Background()
	if _, err := s.SearchTasks(ctx, "x", nil, 20); !errors.Is(err, ErrRemoteSearchUnavailable) {
		t.Fatalf("err %v", err)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanTypeahead); v == "" {
		t.Fatal("typeahead 402 not remembered")
	}
	if _, err := s.SearchTasks(ctx, "x", nil, 20); !errors.Is(err, ErrRemoteSearchUnavailable) {
		t.Fatalf("again: %v", err)
	}
	if n, err := s.FindUsers(ctx, "hanako"); n != 0 || err != nil {
		t.Fatalf("FindUsers %d %v", n, err)
	}
	if f.searchCalls != 1 || f.typeCalls["task"] != 1 || f.typeCalls["user"] != 0 {
		t.Fatalf("asked again: search %d typeahead %v", f.searchCalls, f.typeCalls)
	}
}

func TestSearchTasksEmptyText(t *testing.T) {
	f := &searchServer{}
	s, _ := setupSearch(t, f)
	if _, err := s.SearchTasks(context.Background(), "   ", nil, 20); err == nil {
		t.Fatal("empty text searched")
	}
	if f.searchCalls != 0 {
		t.Fatal("asked Asana")
	}
}

func TestFindUsersAddsOnlyNewUsers(t *testing.T) {
	f := &searchServer{}
	s, st := setupSearch(t, f)
	ctx := context.Background()
	if err := st.UpsertUser(ctx, model.User{GID: "u1", Name: "Me", Email: "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.FindUsers(ctx, "hanako")
	if err != nil || n != 1 {
		t.Fatalf("added %d %v", n, err)
	}
	users, err := st.ListUserContacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, u := range users {
		got[u.GID] = u.Name + " " + u.Email
	}
	// the cached email is not blanked by the found record
	if got["u7"] != "Hanako Yamada hanako@example.com" || got["u1"] != "Me me@example.com" {
		t.Fatalf("users %v", got)
	}
}

func TestFindTagsKeepsADeletedTagDeleted(t *testing.T) {
	f := &searchServer{}
	s, st := setupSearch(t, f)
	ctx := context.Background()
	n, err := s.FindTags(ctx, "買い")
	if err != nil || n != 1 {
		t.Fatalf("added %d %v", n, err)
	}
	tags, _ := st.ListTags(ctx)
	if len(tags) != 1 || tags[0].GID != "g7" || tags[0].Color != "dark-red" {
		t.Fatalf("tags %+v", tags)
	}
	// deleted here, not sent yet: finding it again does not bring it back
	if err := st.DeleteTagLocal(ctx, "g7", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FindTags(ctx, "買い"); err != nil || n != 0 {
		t.Fatalf("re-added %d %v", n, err)
	}
	if tags, _ := st.ListTags(ctx); len(tags) != 0 {
		t.Fatalf("deleted tag back: %+v", tags)
	}
}

func TestTaskByCustomID(t *testing.T) {
	f := &searchServer{}
	s, st := setupSearch(t, f)
	ctx := context.Background()
	gid, err := s.TaskByCustomID(ctx, "ENG-123")
	if err != nil || gid != "t3" {
		t.Fatalf("gid %q %v", gid, err)
	}
	if _, err := st.GetTask(ctx, "t3"); err != nil {
		t.Fatalf("not cached: %v", err)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"ENG-123"); v != "t3" {
		t.Fatalf("gid not remembered: %q", v)
	}
	if _, err := s.TaskByCustomID(ctx, "ENG-999"); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	// an ID that Asana no longer knows is forgotten
	f.customStatus = http.StatusNotFound
	if _, err := s.TaskByCustomID(ctx, "ENG-123"); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("gone: %v", err)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"ENG-123"); v != "" {
		t.Fatalf("gone gid remembered: %q", v)
	}
	f.customStatus = http.StatusInternalServerError
	s.API.(*asana.Client).MaxRetries = 0
	if _, err := s.TaskByCustomID(ctx, "ENG-123"); err == nil || errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("500 taken as not found: %v", err)
	}
}
