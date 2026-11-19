package sync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// Only a 402 is remembered as a plan limit; other failures are not, for search and typeahead alike (#36).
func TestSearchOnlyA402IsRemembered(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusUnauthorized, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx := context.Background()
			f := &searchServer{typeStatus: status}
			s, st := setupSearch(t, f)
			s.API.(*asana.Client).MaxRetries = 0
			if _, err := s.FindUsers(ctx, "hana"); err == nil {
				t.Fatal("failure swallowed")
			}
			if _, err := s.FindTags(ctx, "buy"); err == nil {
				t.Fatal("failure swallowed")
			}
			f.searchStatus = http.StatusPaymentRequired
			if _, err := s.SearchTasks(ctx, "x", nil, 10); err == nil {
				t.Fatal("typeahead failure swallowed")
			}
			for _, feat := range []string{readmodel.PlanSearch + "", readmodel.PlanTypeahead} {
				v, _ := st.SyncState(ctx, readmodel.PlanKey(feat, "w1"))
				if feat == readmodel.PlanSearch && v == "" {
					t.Error("search 402 not remembered")
				}
				if feat == readmodel.PlanTypeahead && v != "" {
					t.Errorf("typeahead %d remembered as a plan limit", status)
				}
			}
			// asked again next time (not stuck)
			f.typeStatus = 0
			if n, err := s.FindUsers(ctx, "hana"); err != nil || n < 1 {
				t.Fatalf("recovery: %d %v", n, err)
			}
		})
	}
}

// A typeahead 402 from a name lookup is remembered and stops the task search too.
func TestFindNames402StopsEverything(t *testing.T) {
	ctx := context.Background()
	f := &searchServer{typeStatus: http.StatusPaymentRequired, searchStatus: http.StatusPaymentRequired}
	s, _ := setupSearch(t, f)
	if n, err := s.FindTags(ctx, "x"); n != 0 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	for i := 0; i < 3; i++ {
		if n, err := s.FindUsers(ctx, "x"); n != 0 || err != nil {
			t.Fatalf("%d %v", n, err)
		}
	}
	if _, err := s.SearchTasks(ctx, "x", nil, 10); !errors.Is(err, ErrRemoteSearchUnavailable) {
		t.Fatalf("err %v", err)
	}
	if f.typeCalls["tag"]+f.typeCalls["user"]+f.typeCalls["task"] != 1 {
		t.Fatalf("typeahead asked again: %v", f.typeCalls)
	}
}

// Blank queries never reach Asana.
func TestFindNamesBlank(t *testing.T) {
	f := &searchServer{}
	s, _ := setupSearch(t, f)
	for _, q := range []string{"", " ", "\t\n"} {
		if n, err := s.FindUsers(context.Background(), q); n != 0 || err != nil {
			t.Fatal(n, err)
		}
		if n, err := s.FindTags(context.Background(), q); n != 0 || err != nil {
			t.Fatal(n, err)
		}
	}
	if len(f.typeCalls) != 0 {
		t.Fatalf("asked: %v", f.typeCalls)
	}
}

// A found task that has an unsent local edit keeps the edit; completed filter drops the others.
func TestSearchKeepsUnsentEditsAndFilters(t *testing.T) {
	ctx := context.Background()
	f := &searchServer{}
	s, st := setupSearch(t, f)
	if err := st.UpsertTasks(ctx, []model.Task{{GID: "t2", Name: "古い名前", Workspace: &model.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "手元の名前"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SearchTasks(ctx, "牛乳", nil, 10); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetTask(ctx, "t2")
	if err != nil || d.Name != "手元の名前" {
		t.Fatalf("local edit lost: %+v %v", d, err)
	}
	// completed=false drops the completed t1 and the cache does not get it
	f2 := &searchServer{}
	s2, st2 := setupSearch(t, f2)
	open := false
	res, err := s2.SearchTasks(ctx, "x", &open, 10)
	if err != nil || len(res.GIDs) != 1 || res.GIDs[0] != "t2" {
		t.Fatalf("res %+v %v", res, err)
	}
	if _, err := st2.GetTask(ctx, "t1"); err == nil {
		t.Fatal("filtered-out task cached")
	}
	if len(f2.searchQueries) != 1 {
		t.Fatal(f2.searchQueries)
	}
	if q, _ := url.ParseQuery(f2.searchQueries[0]); q.Get("completed") != "false" {
		t.Fatalf("completed not sent: %v", q)
	}
}

// limit is clamped to 1..100 whatever the caller passes.
func TestSearchLimitClamped(t *testing.T) {
	for in, want := range map[int]string{-5: "1", 0: "1", 1: "1", 100: "100", 101: "100", 1 << 30: "100"} {
		f := &searchServer{}
		s, _ := setupSearch(t, f)
		_, _ = s.SearchTasks(context.Background(), "x", nil, in)
		q, _ := url.ParseQuery(f.searchQueries[0])
		if q.Get("limit") != want {
			t.Errorf("limit %d sent as %q, want %s", in, q.Get("limit"), want)
		}
	}
}

// Hostile text goes out as one escaped query value, hostile custom IDs as one path segment.
func TestSearchInjection(t *testing.T) {
	ctx := context.Background()
	f := &searchServer{}
	s, _ := setupSearch(t, f)
	text := "a&completed=true&limit=1 #x%00\n牛乳"
	_, _ = s.SearchTasks(ctx, text, nil, 20)
	q, _ := url.ParseQuery(f.searchQueries[0])
	if q.Get("text") != text || q.Get("completed") != "" || q.Get("limit") != "20" {
		t.Fatalf("query %v", q)
	}
	for _, id := range []string{"../x", "a/b", "ENG-1?x=1", "..", ".", "", "a%2Fb", "ENG 1"} {
		if _, err := s.TaskByCustomID(ctx, id); err == nil {
			t.Errorf("custom ID %q found a task", id)
		}
	}
}

// Many searches at once under -race: the cache and the plan flag stay consistent.
func TestSearchConcurrent(t *testing.T) {
	ctx := context.Background()
	f := &searchServer{searchStatus: http.StatusPaymentRequired, t: t, typeCalls: map[string]int{}}
	var mu gosync.Mutex
	h := f.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
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
	s := &Syncer{API: api, Store: st, now: time.Now}
	var wg gosync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := s.SearchTasks(ctx, "x", nil, 10); err != nil || len(res.GIDs) == 0 {
				t.Errorf("search %+v %v", res, err)
			}
			if _, err := s.FindUsers(ctx, "h"); err != nil {
				t.Error(err)
			}
			if _, err := s.FindTags(ctx, "b"); err != nil {
				t.Error(err)
			}
			if gid, err := s.TaskByCustomID(ctx, "ENG-123"); err != nil || gid != "t3" {
				t.Errorf("custom %q %v", gid, err)
			}
		}()
	}
	wg.Wait()
	if users, _ := st.ListUserContacts(ctx); len(users) != 2 {
		t.Errorf("users %+v", users)
	}
	if tags, _ := st.ListTags(ctx); len(tags) != 1 {
		t.Errorf("tags %+v", tags)
	}
}

// A custom ID 402 is remembered like the search plan: Asana is not asked again, and the
// other lookups (search, names) are not affected (#36).
func TestTaskByCustomID402Remembered(t *testing.T) {
	ctx := context.Background()
	f := &searchServer{customStatus: http.StatusPaymentRequired}
	s, st := setupSearch(t, f)
	s.API.(*asana.Client).MaxRetries = 0
	for i := 0; i < 3; i++ {
		if _, err := s.TaskByCustomID(ctx, "ENG-123"); !errors.Is(err, ErrCustomIDUnavailable) {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomID, "w1")); v == "" {
		t.Fatal("402 not remembered")
	}
	f.customStatus = 0
	if _, err := s.SearchTasks(ctx, "x", nil, 10); err != nil {
		t.Fatalf("search affected by the custom ID plan: %v", err)
	}
	if n, err := s.FindUsers(ctx, "h"); err != nil || n == 0 {
		t.Fatalf("names affected: %d %v", n, err)
	}
	// 500 / 403 are not remembered
	for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden} {
		f2 := &searchServer{customStatus: status}
		s2, st2 := setupSearch(t, f2)
		s2.API.(*asana.Client).MaxRetries = 0
		if _, err := s2.TaskByCustomID(ctx, "ENG-123"); err == nil || errors.Is(err, ErrCustomIDUnavailable) {
			t.Fatalf("%d: %v", status, err)
		}
		if v, _ := st2.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomID, "w1")); v != "" {
			t.Fatalf("%d remembered as a plan limit", status)
		}
	}
}
