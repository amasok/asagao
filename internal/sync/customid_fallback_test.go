package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// customIDFallbackServer serves the custom ID, search, typeahead and task endpoints for
// Issue #65 tests (500 from TaskByCustomID falling back to SearchTasks + TasksByGID).
type customIDFallbackServer struct {
	mu           gosync.Mutex
	customStatus int
	searchStatus int
	taskStatus   int // != 0: GET /tasks/{gid} answers this status
	refuseCF     bool
	searchRefs   []model.Ref
	tasks        map[string]string // gid -> raw JSON of task data
	customFields map[string]string // cf gid -> raw JSON of custom field definition
	cfStatus     map[string]int    // cf gid -> HTTP status to return

	customCalls    int
	searchCalls    int
	typeaheadCalls int
	cfCalls        map[string]int
	searchQueries  []string
	fetchedTasks   []string
}

func (f *customIDFallbackServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /workspaces/w1/tasks/custom_id/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.customCalls++
		st := f.customStatus
		f.mu.Unlock()
		if st == 0 {
			st = http.StatusInternalServerError
		}
		w.WriteHeader(st)
		fmt.Fprint(w, `{"errors":[{"message":"Oops! An unexpected error occurred while processing this request."}]}`)
	})
	mux.HandleFunc("GET /workspaces/w1/tasks/search", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.searchCalls++
		f.searchQueries = append(f.searchQueries, r.URL.RawQuery)
		st := f.searchStatus
		refs := append([]model.Ref(nil), f.searchRefs...)
		f.mu.Unlock()
		if st != 0 {
			w.WriteHeader(st)
			fmt.Fprintf(w, `{"errors":[{"message":"search %d"}]}`, st)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": refs})
	})
	mux.HandleFunc("GET /workspaces/w1/typeahead", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.typeaheadCalls++
		f.mu.Unlock()
		fmt.Fprint(w, `{"data":[]}`)
	})
	mux.HandleFunc("GET /tasks/{gid}", func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		f.mu.Lock()
		f.fetchedTasks = append(f.fetchedTasks, gid)
		st := f.taskStatus
		refuseCF := f.refuseCF
		raw, ok := f.tasks[gid]
		f.mu.Unlock()
		if refuseCF && strings.Contains(r.URL.Query().Get("opt_fields"), "custom_fields") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"errors":[{"message":"custom_fields refused"}]}`)
			return
		}
		if st != 0 {
			w.WriteHeader(st)
			fmt.Fprintf(w, `{"errors":[{"message":"task %d"}]}`, st)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
			return
		}
		fmt.Fprintf(w, `{"data":%s}`, raw)
	})
	mux.HandleFunc("GET /custom_fields/{gid}", func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		f.mu.Lock()
		if f.cfCalls == nil {
			f.cfCalls = map[string]int{}
		}
		f.cfCalls[gid]++
		st := f.cfStatus[gid]
		raw, ok := f.customFields[gid]
		f.mu.Unlock()
		if st != 0 {
			w.WriteHeader(st)
			fmt.Fprintf(w, `{"errors":[{"message":"cf %d"}]}`, st)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
			return
		}
		fmt.Fprintf(w, `{"data":%s}`, raw)
	})
	return mux
}

func setupCustomIDFallback(t *testing.T, f *customIDFallbackServer) (*Syncer, *store.Store, *httptest.Server) {
	t.Helper()
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
	api.MaxRetries = 0
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	return &Syncer{API: api, Store: st, now: func() time.Time { return now }}, st, srv
}

// When TaskByCustomID returns 500 (#65), Syncer falls back to the search API (text=<ID>),
// fetches candidate tasks in full and adopts only the task whose ID custom field equals ID.
// Unrelated candidates (name/notes mentions, plain text custom fields, prefix matches) are
// neither adopted nor cached.
func TestTaskByCustomID500FallsBackToSearch(t *testing.T) {
	f := &customIDFallbackServer{
		searchRefs: []model.Ref{
			{GID: "t1", Name: "RC-1 について"},
			{GID: "t2", Name: "テキスト欄に RC-1"},
			{GID: "t3", Name: "別の ID RC-10"},
			{GID: "local:x", Name: "local"},
			{GID: "t4", Name: "本命タスク"},
		},
		tasks: map[string]string{
			"t1": `{"gid":"t1","name":"RC-1 について","notes":"RC-1 を参照"}`,
			"t2": `{"gid":"t2","name":"テキスト欄に RC-1","custom_fields":[{"gid":"cf-txt","name":"メモ","resource_subtype":"text","representation_type":"text","display_value":"RC-1","text_value":"RC-1"}]}`,
			"t3": `{"gid":"t3","name":"別の ID RC-10","custom_fields":[{"gid":"cf-id","name":"ID","resource_subtype":"text","representation_type":"custom_id","id_prefix":"RC","display_value":"RC-10","text_value":"RC-10"}]}`,
			"t4": `{"gid":"t4","name":"本命\u001b[31mタスク","completed":true,"notes":"詳細本文","custom_fields":[{"gid":"cf-id","name":"ID","resource_subtype":"text","representation_type":"custom_id","id_prefix":"RC","is_value_read_only":true,"display_value":"RC-1","text_value":"RC-1"}]}`,
		},
	}
	s, st, _ := setupCustomIDFallback(t, f)
	ctx := context.Background()

	gid, err := s.TaskByCustomID(ctx, "RC-1")
	if err != nil || gid != "t4" {
		t.Fatalf("TaskByCustomID = %q, %v; want t4, nil", gid, err)
	}
	if f.customCalls != 1 || f.searchCalls != 1 || f.typeaheadCalls != 0 {
		t.Fatalf("calls: custom=%d search=%d typeahead=%d", f.customCalls, f.searchCalls, f.typeaheadCalls)
	}
	q, err := url.ParseQuery(f.searchQueries[0])
	if err != nil || q.Get("text") != "RC-1" || q.Has("completed") {
		t.Fatalf("search query = %q, want text=RC-1 without completed filter", f.searchQueries[0])
	}
	// Found task is cached in full (with termsafe stripping control characters on read).
	d, err := st.GetTask(ctx, "t4")
	if err != nil || d.Name != "本命[31mタスク" || !d.Completed || d.Notes != "詳細本文" {
		t.Fatalf("cached t4 = %+v, err=%v", d, err)
	}
	// Non-matching candidates are not cached.
	for _, other := range []string{"t1", "t2", "t3"} {
		if _, err := st.GetTask(ctx, other); !errors.Is(err, readmodel.ErrNotFound) {
			t.Errorf("non-matching candidate %s was cached: %v", other, err)
		}
	}
	if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "t4" {
		t.Fatalf("sync_state custom_id:RC-1 = %q, want t4", v)
	}
}

// When the 500 fallback search finds no task carrying the exact custom ID (either empty
// results or only text/prefix/case-mismatched candidates), TaskByCustomID returns
// readmodel.ErrNotFound and forgets any previously remembered gid.
func TestTaskByCustomID500FallbackNotFoundClearsCache(t *testing.T) {
	cases := []struct {
		name  string
		query string
		refs  []model.Ref
		tasks map[string]string
	}{
		{
			name:  "empty search results",
			query: "RC-9",
			refs:  nil,
		},
		{
			name:  "only notes, text custom field, prefix match, and wrong case",
			query: "RC-9",
			refs:  []model.Ref{{GID: "t1"}, {GID: "t2"}, {GID: "t3"}, {GID: "t4"}},
			tasks: map[string]string{
				"t1": `{"gid":"t1","name":"RC-9 の件","notes":"RC-9"}`,
				"t2": `{"gid":"t2","name":"t2","custom_fields":[{"gid":"cf1","resource_subtype":"text","representation_type":"text","display_value":"RC-9","text_value":"RC-9"}]}`,
				"t3": `{"gid":"t3","name":"t3","custom_fields":[{"gid":"cf2","resource_subtype":"text","representation_type":"custom_id","id_prefix":"RC","display_value":"RC-90"}]}`,
				"t4": `{"gid":"t4","name":"t4","custom_fields":[{"gid":"cf2","resource_subtype":"text","representation_type":"custom_id","id_prefix":"rc","display_value":"rc-9"}]}`,
			},
		},
		{
			name:  "lowercase query does not match uppercase ID custom field",
			query: "rc-1",
			refs:  []model.Ref{{GID: "t1"}},
			tasks: map[string]string{
				"t1": `{"gid":"t1","name":"t1","custom_fields":[{"gid":"cf2","resource_subtype":"text","representation_type":"custom_id","id_prefix":"RC","display_value":"RC-1"}]}`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &customIDFallbackServer{searchRefs: tc.refs, tasks: tc.tasks}
			s, st, _ := setupCustomIDFallback(t, f)
			ctx := context.Background()
			if err := st.SetCustomIDTaskGID(ctx, tc.query, "old-gid"); err != nil {
				t.Fatal(err)
			}
			_, err := s.TaskByCustomID(ctx, tc.query)
			if !errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if v, _ := st.CustomIDTaskGID(ctx, tc.query); v != "" {
				t.Fatalf("stale gid not cleared: %q", v)
			}
			for gid := range tc.tasks {
				if _, err := st.GetTask(ctx, gid); !errors.Is(err, readmodel.ErrNotFound) {
					t.Errorf("non-matching task %s cached", gid)
				}
			}
		})
	}
}

// When plan.Search is already hidden (402) or the fallback SearchTasks answers 402,
// plan.Search is recorded/kept, plan.CustomID is NOT marked 402, typeahead is NOT used, the
// original 500 error is returned, and any cached custom_id gid is kept.
func TestTaskByCustomID500FallbackSearchPlan402(t *testing.T) {
	t.Run("plan.Search already hidden", func(t *testing.T) {
		f := &customIDFallbackServer{}
		s, st, _ := setupCustomIDFallback(t, f)
		ctx := context.Background()
		if err := st.SetCustomIDTaskGID(ctx, "RC-1", "old-gid"); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordPlanRefusal(ctx, plan.Search, "w1", s.now()); err != nil {
			t.Fatal(err)
		}

		_, err := s.TaskByCustomID(ctx, "RC-1")
		var apiErr *model.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want 500 *model.Error", err)
		}
		if f.searchCalls != 0 || f.typeaheadCalls != 0 {
			t.Fatalf("search=%d typeahead=%d, want 0", f.searchCalls, f.typeaheadCalls)
		}
		if _, ok, _ := st.PlanRefusedAt(ctx, plan.CustomID, "w1"); ok {
			t.Fatal("plan.CustomID wrongly marked 402")
		}
		if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "old-gid" {
			t.Fatalf("cached gid lost: %q", v)
		}
	})

	t.Run("fallback SearchTasks answers 402", func(t *testing.T) {
		f := &customIDFallbackServer{searchStatus: http.StatusPaymentRequired}
		s, st, _ := setupCustomIDFallback(t, f)
		ctx := context.Background()
		if err := st.SetCustomIDTaskGID(ctx, "RC-1", "old-gid"); err != nil {
			t.Fatal(err)
		}

		_, err := s.TaskByCustomID(ctx, "RC-1")
		var apiErr *model.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want original 500 *model.Error", err)
		}
		if f.searchCalls != 1 || f.typeaheadCalls != 0 {
			t.Fatalf("search=%d typeahead=%d, want 1, 0", f.searchCalls, f.typeaheadCalls)
		}
		if _, ok, _ := st.PlanRefusedAt(ctx, plan.Search, "w1"); !ok {
			t.Fatal("plan.Search 402 not recorded")
		}
		if _, ok, _ := st.PlanRefusedAt(ctx, plan.CustomID, "w1"); ok {
			t.Fatal("plan.CustomID wrongly marked 402")
		}
		if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "old-gid" {
			t.Fatalf("cached gid lost: %q", v)
		}
	})
}

// Failures during fallback SearchTasks or TasksByGID (401, 404, 429, 500, network error)
// are returned as errors, never rounded to ErrNotFound, and never wipe the cached gid.
func TestTaskByCustomID500FallbackErrorsKeepCache(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("search %d", status), func(t *testing.T) {
			f := &customIDFallbackServer{searchStatus: status}
			s, st, _ := setupCustomIDFallback(t, f)
			ctx := context.Background()
			if err := st.SetCustomIDTaskGID(ctx, "RC-1", "old-gid"); err != nil {
				t.Fatal(err)
			}
			_, err := s.TaskByCustomID(ctx, "RC-1")
			if err == nil || errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("err = %v, want non-NotFound error", err)
			}
			if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "old-gid" {
				t.Fatalf("cached gid lost: %q", v)
			}
		})
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("TasksByGID %d", status), func(t *testing.T) {
			f := &customIDFallbackServer{
				searchRefs: []model.Ref{{GID: "t1"}},
				taskStatus: status,
			}
			s, st, _ := setupCustomIDFallback(t, f)
			ctx := context.Background()
			if err := st.SetCustomIDTaskGID(ctx, "RC-1", "old-gid"); err != nil {
				t.Fatal(err)
			}
			_, err := s.TaskByCustomID(ctx, "RC-1")
			if err == nil || errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("err = %v, want non-NotFound error", err)
			}
			if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "old-gid" {
				t.Fatalf("cached gid lost: %q", v)
			}
		})
	}

	t.Run("network error during search", func(t *testing.T) {
		f := &customIDFallbackServer{}
		s, st, srv := setupCustomIDFallback(t, f)
		// Close server after TaskByCustomID returns 500.
		s.API = &closeAfterCustomIDAPI{AsanaAPI: s.API, close: srv.Close}
		ctx := context.Background()
		if err := st.SetCustomIDTaskGID(ctx, "RC-1", "old-gid"); err != nil {
			t.Fatal(err)
		}
		_, err := s.TaskByCustomID(ctx, "RC-1")
		if err == nil || errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("err = %v, want network error", err)
		}
		if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "old-gid" {
			t.Fatalf("cached gid lost: %q", v)
		}
	})
}

type closeAfterCustomIDAPI struct {
	AsanaAPI
	close func()
}

func (a *closeAfterCustomIDAPI) TaskByCustomID(ctx context.Context, ws, id string) (*model.Task, error) {
	t, err := a.AsanaAPI.TaskByCustomID(ctx, ws, id)
	a.close()
	return t, err
}

// Non-500 errors from TaskByCustomID (403, 404, 502, 503) do not fall back to SearchTasks.
func TestTaskByCustomIDNon500DoesNotFallbackToSearch(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := &customIDFallbackServer{customStatus: status}
			s, _, _ := setupCustomIDFallback(t, f)
			_, err := s.TaskByCustomID(context.Background(), "RC-1")
			if err == nil {
				t.Fatal("expected error")
			}
			if f.searchCalls != 0 {
				t.Fatalf("status %d called search %d times", status, f.searchCalls)
			}
		})
	}
}

// When an expired (>24h) plan.CustomID 402 record is retried and TaskByCustomID returns 500,
// a successful fallback resolution clears the stale plan.CustomID refusal, while a failed
// fallback leaves it alone.
func TestTaskByCustomID500FallbackClearsExpiredCustomID402(t *testing.T) {
	t.Run("clears expired plan.CustomID when fallback resolves", func(t *testing.T) {
		f := &customIDFallbackServer{
			searchRefs: []model.Ref{{GID: "t4"}},
			tasks: map[string]string{
				"t4": `{"gid":"t4","name":"t4","custom_fields":[{"gid":"cf","representation_type":"custom_id","id_prefix":"RC","display_value":"RC-1"}]}`,
			},
		}
		s, st, _ := setupCustomIDFallback(t, f)
		ctx := context.Background()
		expired := s.now().Add(-plan.RecheckAfter - time.Minute)
		if err := st.RecordPlanRefusal(ctx, plan.CustomID, "w1", expired); err != nil {
			t.Fatal(err)
		}
		gid, err := s.TaskByCustomID(ctx, "RC-1")
		if err != nil || gid != "t4" {
			t.Fatalf("got %q, %v", gid, err)
		}
		if _, ok, _ := st.PlanRefusedAt(ctx, plan.CustomID, "w1"); ok {
			t.Fatal("expired plan.CustomID not cleared")
		}
	})

	t.Run("keeps expired plan.CustomID when fallback fails", func(t *testing.T) {
		f := &customIDFallbackServer{searchStatus: http.StatusPaymentRequired}
		s, st, _ := setupCustomIDFallback(t, f)
		ctx := context.Background()
		expired := s.now().Add(-plan.RecheckAfter - time.Minute)
		if err := st.RecordPlanRefusal(ctx, plan.CustomID, "w1", expired); err != nil {
			t.Fatal(err)
		}
		_, err := s.TaskByCustomID(ctx, "RC-1")
		var apiErr *model.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want 500 *model.Error", err)
		}
		if got, ok, _ := st.PlanRefusedAt(ctx, plan.CustomID, "w1"); !ok || !got.Equal(expired) {
			t.Fatalf("expired plan.CustomID changed on failed fallback: got (%v, %v), want %v", got, ok, expired)
		}
	})
}

// Candidate fetching runs in MaxBatchActions chunks and stops as soon as a chunk contains
// the matching task.
func TestTaskByCustomID500FallbackStopsAtMatchingBatch(t *testing.T) {
	f := &customIDFallbackServer{tasks: map[string]string{}}
	for i := 1; i <= 15; i++ {
		gid := fmt.Sprintf("g%d", i)
		f.searchRefs = append(f.searchRefs, model.Ref{GID: gid})
		if i == 2 {
			f.tasks[gid] = `{"gid":"g2","name":"hit","custom_fields":[{"gid":"cf","representation_type":"custom_id","id_prefix":"RC","display_value":"RC-1"}]}`
		} else {
			f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":"other"}`, gid)
		}
	}
	s, _, _ := setupCustomIDFallback(t, f)
	gid, err := s.TaskByCustomID(context.Background(), "RC-1")
	if err != nil || gid != "g2" {
		t.Fatalf("got %q, %v", gid, err)
	}
	if len(f.fetchedTasks) > model.MaxBatchActions {
		t.Fatalf("fetched %d tasks (%v), expected early stop at first chunk (%d)", len(f.fetchedTasks), f.fetchedTasks, model.MaxBatchActions)
	}
}

// If TasksByGID falls back to plain fields (Task.Partial == true, without custom_fields),
// we cannot verify whether a candidate carries customID, so TaskByCustomID returns the
// original 500 error instead of falsely reporting ErrNotFound and clearing cached state.
func TestTaskByCustomID500FallbackPartialTasksReturns500(t *testing.T) {
	f := &customIDFallbackServer{
		refuseCF:   true,
		searchRefs: []model.Ref{{GID: "t1"}},
		tasks:      map[string]string{"t1": `{"gid":"t1","name":"plain"}`},
	}
	s, st, _ := setupCustomIDFallback(t, f)
	ctx := context.Background()
	if err := st.SetCustomIDTaskGID(ctx, "RC-1", "old-gid"); err != nil {
		t.Fatal(err)
	}
	_, err := s.TaskByCustomID(ctx, "RC-1")
	var apiErr *model.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v, want 500 *model.Error", err)
	}
	if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "old-gid" {
		t.Fatalf("cached gid lost: %q", v)
	}
}

// Even if an earlier candidate in the same or a previous chunk is Partial, scanning
// continues so a later full task carrying customID is still found and cached.
func TestTaskByCustomID500FallbackPartialThenMatchFindsTask(t *testing.T) {
	str := func(s string) *string { return &s }
	matchTask := model.Task{
		GID:  "t-match",
		Name: "本命タスク",
		CustomFields: []model.CustomField{{
			GID:                "cf-id",
			RepresentationType: "custom_id",
			IDPrefix:           "RC",
			DisplayValue:       str("RC-1"),
		}},
	}
	for _, tc := range []struct {
		name string
		refs []model.Ref
	}{
		{
			name: "partial then match in same chunk",
			refs: []model.Ref{{GID: "t-partial"}, {GID: "t-match"}},
		},
		{
			name: "partial in chunk 1, match in chunk 2",
			refs: []model.Ref{
				{GID: "t-partial"}, {GID: "n2"}, {GID: "n3"}, {GID: "n4"}, {GID: "n5"},
				{GID: "n6"}, {GID: "n7"}, {GID: "n8"}, {GID: "n9"}, {GID: "n10"},
				{GID: "t-match"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &customIDFallbackServer{}
			s, st, _ := setupCustomIDFallback(t, f)
			s.API = &partialMixAPI{
				AsanaAPI: s.API,
				refs:     tc.refs,
				tasks: map[string]model.Task{
					"t-partial": {GID: "t-partial", Name: "partial", Partial: true},
					"t-match":   matchTask,
				},
			}
			ctx := context.Background()
			gid, err := s.TaskByCustomID(ctx, "RC-1")
			if err != nil || gid != "t-match" {
				t.Fatalf("got %q, %v; want t-match, nil", gid, err)
			}
			if d, err := st.GetTask(ctx, "t-match"); err != nil || d.Name != "本命タスク" {
				t.Fatalf("matched task not cached: %+v, err=%v", d, err)
			}
			if _, err := st.GetTask(ctx, "t-partial"); !errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("partial task should not be cached: %v", err)
			}
			if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "t-match" {
				t.Fatalf("sync_state = %q, want t-match", v)
			}
		})
	}
}

type partialMixAPI struct {
	AsanaAPI
	refs  []model.Ref
	tasks map[string]model.Task
}

func (a *partialMixAPI) SearchTasks(_ context.Context, _, _ string, _ *bool, _ int) ([]model.Ref, error) {
	return a.refs, nil
}

func (a *partialMixAPI) TasksByGID(_ context.Context, gids []string) ([]model.Task, []string, error) {
	out := make([]model.Task, 0, len(gids))
	for _, g := range gids {
		if t, ok := a.tasks[g]; ok {
			out = append(out, t)
		} else {
			out = append(out, model.Task{GID: g, Name: "other"})
		}
	}
	return out, nil, nil
}

// On real Asana, GET /tasks/{gid} omits representation_type and id_prefix inside
// task.custom_fields[], returning only resource_subtype: "text" and is_value_read_only:
// true. Fallback looks up GET /custom_fields/{gid} (memoized per field gid) to tell ID
// fields apart from plain text custom fields (#65).
func TestTaskByCustomID500FallbackRealAsanaCustomFieldDef(t *testing.T) {
	t.Run("adopts task when GET /custom_fields/{gid} confirms custom_id and skips plain text", func(t *testing.T) {
		f := &customIDFallbackServer{
			searchRefs: []model.Ref{{GID: "t-plain1"}, {GID: "t-plain2"}, {GID: "t-real"}},
			tasks: map[string]string{
				"t-plain1": `{"gid":"t-plain1","name":"テキスト1","custom_fields":[{"gid":"cf-txt","name":"Memo","resource_subtype":"text","type":"text","is_value_read_only":false,"display_value":"RC-1","text_value":"RC-1"}]}`,
				"t-plain2": `{"gid":"t-plain2","name":"テキスト2","custom_fields":[{"gid":"cf-txt","name":"Memo","resource_subtype":"text","type":"text","is_value_read_only":false,"display_value":"RC-1","text_value":"RC-1"}]}`,
				"t-real":   `{"gid":"t-real","name":"タスクタイプ","custom_fields":[{"gid":"cf-id","name":"asagao-realcheck-id","resource_subtype":"text","type":"text","is_value_read_only":true,"display_value":"RC-1","text_value":"RC-1"}]}`,
			},
			customFields: map[string]string{
				"cf-txt": `{"gid":"cf-txt","name":"Memo","resource_subtype":"text","type":"text","representation_type":"text"}`,
				"cf-id":  `{"gid":"cf-id","name":"asagao-realcheck-id","resource_subtype":"text","type":"text","is_value_read_only":true,"id_prefix":"RC","representation_type":"custom_id"}`,
			},
		}
		s, st, _ := setupCustomIDFallback(t, f)
		ctx := context.Background()
		gid, err := s.TaskByCustomID(ctx, "RC-1")
		if err != nil || gid != "t-real" {
			t.Fatalf("got %q, %v; want t-real, nil", gid, err)
		}
		if f.cfCalls["cf-txt"] != 1 || f.cfCalls["cf-id"] != 1 {
			t.Fatalf("cfCalls = %v; want cf-txt=1 (memoized across tasks), cf-id=1", f.cfCalls)
		}
		if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "t-real" {
			t.Fatalf("sync_state = %q, want t-real", v)
		}
		if _, err := st.GetTask(ctx, "t-plain1"); !errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("plain text task wrongly cached: %v", err)
		}
	})

	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusPaymentRequired, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("custom_fields status %d preserves cache and never falsely returns not_found", status), func(t *testing.T) {
			f := &customIDFallbackServer{
				searchRefs: []model.Ref{{GID: "t-cand"}},
				tasks: map[string]string{
					"t-cand": `{"gid":"t-cand","name":"候補","custom_fields":[{"gid":"cf-id","name":"ID","resource_subtype":"text","is_value_read_only":true,"display_value":"RC-1","text_value":"RC-1"}]}`,
				},
				cfStatus: map[string]int{"cf-id": status},
			}
			s, st, _ := setupCustomIDFallback(t, f)
			ctx := context.Background()
			if err := st.SetCustomIDTaskGID(ctx, "RC-1", "known-gid"); err != nil {
				t.Fatal(err)
			}
			gid, err := s.TaskByCustomID(ctx, "RC-1")
			if err == nil || gid != "" || errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("got gid=%q err=%v; want non-ErrNotFound error", gid, err)
			}
			if v, _ := st.CustomIDTaskGID(ctx, "RC-1"); v != "known-gid" {
				t.Fatalf("cached gid lost: %q", v)
			}
		})
	}
}
