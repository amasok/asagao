package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/termsafe"
)

// customIDAttackServer exercises the full HTTP surface of TaskByCustomID fallback including
// POST /batch (TasksByGID's primary path), GET /tasks/{gid}, search, and fault injection.
type customIDAttackServer struct {
	mu gosync.Mutex

	customStatus int
	customBody   string // optional override

	searchStatus int
	searchBody   string // optional override
	searchRefs   []model.Ref
	onSearch     func(w http.ResponseWriter, r *http.Request) bool // return true if handled

	tasks         map[string]string // gid -> raw JSON of task object
	taskStatus    map[string]int    // per-gid HTTP status override (e.g. 403, 404, 500)
	refuseCF      bool              // refuse requests asking for custom_fields with 400
	refuseCFChunk int               // if > 0, refuse custom_fields starting on this 1-based batch/chunk call
	onBatch       func(call int, w http.ResponseWriter, r *http.Request) bool

	customCalls  int
	searchCalls  int
	batchCalls   int
	taskGetCalls int
	batchFields  [][]string
	batchChunks  [][]string
	fetchedGIDs  []string
}

func (f *customIDAttackServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /workspaces/w1/tasks/custom_id/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.customCalls++
		st := f.customStatus
		body := f.customBody
		f.mu.Unlock()
		if st == 0 {
			st = http.StatusInternalServerError
		}
		w.WriteHeader(st)
		if body != "" {
			fmt.Fprint(w, body)
			return
		}
		fmt.Fprintf(w, `{"errors":[{"message":"custom_id error %d"}]}`, st)
	})
	mux.HandleFunc("GET /workspaces/w1/tasks/search", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.searchCalls++
		hook := f.onSearch
		st := f.searchStatus
		body := f.searchBody
		refs := append([]model.Ref(nil), f.searchRefs...)
		f.mu.Unlock()
		if hook != nil && hook(w, r) {
			return
		}
		if body != "" {
			if st != 0 {
				w.WriteHeader(st)
			}
			fmt.Fprint(w, body)
			return
		}
		if st != 0 {
			w.WriteHeader(st)
			fmt.Fprintf(w, `{"errors":[{"message":"search %d"}]}`, st)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": refs})
	})
	mux.HandleFunc("POST /batch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Data struct {
				Actions []struct {
					Method       string         `json:"method"`
					RelativePath string         `json:"relative_path"`
					Options      map[string]any `json:"options"`
				} `json:"actions"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"errors":[{"message":"bad batch json"}]}`)
			return
		}
		f.mu.Lock()
		f.batchCalls++
		call := f.batchCalls
		hook := f.onBatch
		refuseCF := f.refuseCF || (f.refuseCFChunk > 0 && call >= f.refuseCFChunk)
		chunk := make([]string, 0, len(req.Data.Actions))
		for _, a := range req.Data.Actions {
			gid := strings.TrimPrefix(a.RelativePath, "/tasks/")
			chunk = append(chunk, gid)
			f.fetchedGIDs = append(f.fetchedGIDs, gid)
			if rawFields, ok := a.Options["fields"].([]any); ok {
				fs := make([]string, 0, len(rawFields))
				for _, rf := range rawFields {
					if s, ok := rf.(string); ok {
						fs = append(fs, s)
					}
				}
				f.batchFields = append(f.batchFields, fs)
			}
		}
		f.batchChunks = append(f.batchChunks, chunk)
		f.mu.Unlock()

		if hook != nil && hook(call, w, r) {
			return
		}

		type batchRes struct {
			StatusCode int             `json:"status_code"`
			Body       json.RawMessage `json:"body"`
		}
		results := make([]batchRes, len(req.Data.Actions))
		for i, a := range req.Data.Actions {
			gid := strings.TrimPrefix(a.RelativePath, "/tasks/")
			hasCF := false
			if rawFields, ok := a.Options["fields"].([]any); ok {
				for _, rf := range rawFields {
					if s, ok := rf.(string); ok && strings.HasPrefix(s, "custom_fields") {
						hasCF = true
						break
					}
				}
			}
			f.mu.Lock()
			st := f.taskStatus[gid]
			raw, ok := f.tasks[gid]
			f.mu.Unlock()
			switch {
			case refuseCF && hasCF:
				results[i] = batchRes{StatusCode: http.StatusBadRequest, Body: json.RawMessage(`{"errors":[{"message":"custom_fields refused"}]}`)}
			case st != 0:
				results[i] = batchRes{StatusCode: st, Body: json.RawMessage(fmt.Sprintf(`{"errors":[{"message":"task %d"}]}`, st))}
			case !ok:
				results[i] = batchRes{StatusCode: http.StatusNotFound, Body: json.RawMessage(`{"errors":[{"message":"Not Found"}]}`)}
			default:
				results[i] = batchRes{StatusCode: http.StatusOK, Body: json.RawMessage(fmt.Sprintf(`{"data":%s}`, raw))}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": results})
	})
	mux.HandleFunc("GET /tasks/{gid}", func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		f.mu.Lock()
		f.taskGetCalls++
		f.fetchedGIDs = append(f.fetchedGIDs, gid)
		refuseCF := f.refuseCF || (f.refuseCFChunk > 0 && f.batchCalls >= f.refuseCFChunk)
		st := f.taskStatus[gid]
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
	return mux
}

func setupCustomIDAttack(t *testing.T, f *customIDAttackServer) (*Syncer, *store.Store, *httptest.Server) {
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

func customIDTaskJSON(gid, name, customID string) string {
	return fmt.Sprintf(
		`{"gid":%q,"name":%q,"notes":"detail","custom_fields":[{"gid":"cf-id","name":"ID","resource_subtype":"text","representation_type":"custom_id","id_prefix":"RC","is_value_read_only":true,"display_value":%q,"text_value":%q}]}`,
		gid, name, customID, customID,
	)
}

// Hostile candidate mix spanning multiple batch chunks: name/notes hits, plain text CF,
// prefix matches (RC-10, RC-100), wrong case (rc-1), 404/403 (gone) tasks, local: GIDs,
// empty GIDs, and duplicate GIDs before the real RC-1 appears in chunk 2 (11th+ unique).
func TestTaskByCustomID500FallbackNoisySecondChunk(t *testing.T) {
	f := &customIDAttackServer{
		searchRefs: []model.Ref{
			{GID: "noise-name", Name: "RC-1"},
			{GID: "noise-notes", Name: "notes hit"},
			{GID: "noise-textcf", Name: "text cf hit"},
			{GID: "noise-rc10", Name: "RC-10"},
			{GID: "noise-rc100", Name: "RC-100"},
			{GID: "noise-case", Name: "rc-1"},
			{GID: "gone-404", Name: "deleted"},
			{GID: "gone-403", Name: "forbidden"},
			{GID: "local:abc", Name: "local task"},
			{GID: "", Name: "empty gid"},
			{GID: "noise-name", Name: "duplicate 1"},
			{GID: "noise-rc10", Name: "duplicate 2"},
			{GID: "noise-extra1", Name: "fill chunk 1 #9"},
			{GID: "noise-extra2", Name: "fill chunk 1 #10"},
			// Chunk 2 (unique remote GID #11, #12, #13):
			{GID: "noise-chunk2", Name: "chunk2 decoy"},
			{GID: "real-rc1", Name: "The real RC-1"},
			// Chunk 3 (must never be fetched because chunk 2 already matched):
			{GID: "chunk3-1"}, {GID: "chunk3-2"}, {GID: "chunk3-3"}, {GID: "chunk3-4"},
			{GID: "chunk3-5"}, {GID: "chunk3-6"}, {GID: "chunk3-7"}, {GID: "chunk3-8"},
			{GID: "chunk3-9"}, {GID: "chunk3-10"}, {GID: "chunk3-11"},
		},
		taskStatus: map[string]int{
			"gone-404": http.StatusNotFound,
			"gone-403": http.StatusForbidden,
		},
		tasks: map[string]string{
			"noise-name":   `{"gid":"noise-name","name":"RC-1","notes":"RC-1"}`,
			"noise-notes":  `{"gid":"noise-notes","name":"other","notes":"see RC-1"}`,
			"noise-textcf": `{"gid":"noise-textcf","name":"t","custom_fields":[{"gid":"cf-txt","name":"Memo","resource_subtype":"text","representation_type":"text","display_value":"RC-1","text_value":"RC-1"}]}`,
			"noise-rc10":   customIDTaskJSON("noise-rc10", "RC-10 task", "RC-10"),
			"noise-rc100":  customIDTaskJSON("noise-rc100", "RC-100 task", "RC-100"),
			"noise-case":   `{"gid":"noise-case","name":"lower","custom_fields":[{"gid":"cf-low","name":"ID","resource_subtype":"text","representation_type":"custom_id","id_prefix":"rc","display_value":"rc-1","text_value":"rc-1"}]}`,
			"noise-extra1": `{"gid":"noise-extra1","name":"x1"}`,
			"noise-extra2": `{"gid":"noise-extra2","name":"x2"}`,
			"noise-chunk2": `{"gid":"noise-chunk2","name":"decoy","custom_fields":[{"gid":"cf-id","representation_type":"custom_id","id_prefix":"RC","display_value":"RC-11"}]}`,
			"real-rc1":     customIDTaskJSON("real-rc1", "本命 RC-1", "RC-1"),
		},
	}
	s, st, _ := setupCustomIDAttack(t, f)
	ctx := context.Background()

	gid, err := s.TaskByCustomID(ctx, "RC-1")
	if err != nil || gid != "real-rc1" {
		t.Fatalf("TaskByCustomID = %q, %v; want real-rc1, nil", gid, err)
	}
	// Must use POST /batch for 2 chunks only (stopped before chunk 3).
	if f.batchCalls != 2 {
		t.Fatalf("batchCalls = %d (chunks=%v), want 2", f.batchCalls, f.batchChunks)
	}
	// Batch actions must request custom ID metadata fields.
	if len(f.batchFields) == 0 || !slices.Contains(f.batchFields[0], "custom_fields.representation_type") || !slices.Contains(f.batchFields[0], "custom_fields.id_prefix") {
		t.Fatalf("batch fields missing custom ID metadata: %v", f.batchFields)
	}
	// Local, empty, and duplicate GIDs must never be fetched.
	seen := map[string]int{}
	for _, g := range f.fetchedGIDs {
		if g == "" || strings.HasPrefix(g, "local:") {
			t.Errorf("invalid gid fetched: %q", g)
		}
		seen[g]++
		if seen[g] > 1 {
			t.Errorf("duplicate gid fetched %d times: %q", seen[g], g)
		}
	}
	// Only real-rc1 is cached; none of the decoys are cached.
	if d, err := st.GetTask(ctx, "real-rc1"); err != nil || d.Name != "本命 RC-1" {
		t.Fatalf("real-rc1 not cached properly: %+v, err=%v", d, err)
	}
	for gid := range f.tasks {
		if gid == "real-rc1" {
			continue
		}
		if _, err := st.GetTask(ctx, gid); !errors.Is(err, readmodel.ErrNotFound) {
			t.Errorf("decoy task %s should not be cached: %v", gid, err)
		}
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "real-rc1" {
		t.Fatalf("sync_state custom_id:RC-1 = %q, want real-rc1", v)
	}
}

// When SearchTasks hits Asana's 100-result cap (customIDSearchLimit) and none of the 100
// returned tasks carry customID, the search was truncated and cannot prove the custom ID
// does not exist: TaskByCustomID must NOT return ErrNotFound and must NOT wipe the cached
// custom_id:<ID> mapping.
func TestTaskByCustomID500FallbackTruncatedAtSearchLimitKeepsCache(t *testing.T) {
	t.Run("100 non-matching results returns original 500 and preserves cache", func(t *testing.T) {
		f := &customIDAttackServer{tasks: map[string]string{}}
		for i := 1; i <= customIDSearchLimit; i++ {
			gid := fmt.Sprintf("n%d", i)
			f.searchRefs = append(f.searchRefs, model.Ref{GID: gid, Name: "RC-1 mention"})
			f.tasks[gid] = customIDTaskJSON(gid, fmt.Sprintf("RC-1 mention #%d", i), fmt.Sprintf("RC-%d", 100+i))
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "known-gid"); err != nil {
			t.Fatal(err)
		}

		_, err := s.TaskByCustomID(ctx, "RC-1")
		if errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("truncated 100-result search wrongly returned ErrNotFound: %v", err)
		}
		var apiErr *model.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want original 500 *model.Error", err)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "known-gid" {
			t.Fatalf("truncated search wiped cached gid: got %q, want known-gid", v)
		}
		for gid := range f.tasks {
			if _, err := st.GetTask(ctx, gid); !errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("non-matching task %s was cached", gid)
			}
		}
	})

	t.Run("100 raw results with duplicates still counts as truncated", func(t *testing.T) {
		f := &customIDAttackServer{tasks: map[string]string{}}
		for i := 0; i < customIDSearchLimit; i++ {
			gid := fmt.Sprintf("d%d", i%50)
			f.searchRefs = append(f.searchRefs, model.Ref{GID: gid})
			f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":"RC-1 ref"}`, gid)
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "known-gid"); err != nil {
			t.Fatal(err)
		}

		_, err := s.TaskByCustomID(ctx, "RC-1")
		if errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("truncated 100-ref search with duplicates wrongly returned ErrNotFound: %v", err)
		}
		var apiErr *model.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want original 500 *model.Error", err)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "known-gid" {
			t.Fatalf("cached gid wiped: got %q, want known-gid", v)
		}
	})

	t.Run("100 results where the 100th matches still succeeds", func(t *testing.T) {
		f := &customIDAttackServer{tasks: map[string]string{}}
		for i := 1; i <= customIDSearchLimit; i++ {
			gid := fmt.Sprintf("m%d", i)
			f.searchRefs = append(f.searchRefs, model.Ref{GID: gid})
			if i == customIDSearchLimit {
				f.tasks[gid] = customIDTaskJSON(gid, "100th is real", "RC-1")
			} else {
				f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":"other"}`, gid)
			}
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		gid, err := s.TaskByCustomID(ctx, "RC-1")
		if err != nil || gid != "m100" {
			t.Fatalf("got %q, %v; want m100, nil", gid, err)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "m100" {
			t.Fatalf("sync_state = %q, want m100", v)
		}
	})

	t.Run("99 non-matching results is not truncated and returns ErrNotFound", func(t *testing.T) {
		f := &customIDAttackServer{tasks: map[string]string{}}
		for i := 1; i < customIDSearchLimit; i++ {
			gid := fmt.Sprintf("u%d", i)
			f.searchRefs = append(f.searchRefs, model.Ref{GID: gid})
			f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":"RC-1 mention"}`, gid)
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "stale-gid"); err != nil {
			t.Fatal(err)
		}
		_, err := s.TaskByCustomID(ctx, "RC-1")
		if !errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound for non-truncated 99 results", err)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "" {
			t.Fatalf("stale gid not cleared: %q", v)
		}
	})
}

// Search API errors on 500 fallback (402, 500, 429, 401, broken/empty JSON, disconnect,
// context cancel) never wipe custom_id:<ID> and never wrongly mark PlanCustomID as 402;
// only a search 402 marks PlanSearch.
func TestTaskByCustomID500FallbackSearchFailuresPreserveState(t *testing.T) {
	type tc struct {
		name           string
		setup          func(f *customIDAttackServer, cancel context.CancelFunc)
		wantErr500     bool
		wantCanceled   bool
		wantPlanSearch bool
	}
	cases := []tc{
		{
			name:           "search 402 records PlanSearch only and returns original 500",
			setup:          func(f *customIDAttackServer, _ context.CancelFunc) { f.searchStatus = http.StatusPaymentRequired },
			wantErr500:     true,
			wantPlanSearch: true,
		},
		{
			name:  "search 500",
			setup: func(f *customIDAttackServer, _ context.CancelFunc) { f.searchStatus = http.StatusInternalServerError },
		},
		{
			name:  "search 429",
			setup: func(f *customIDAttackServer, _ context.CancelFunc) { f.searchStatus = http.StatusTooManyRequests },
		},
		{
			name:  "search 401",
			setup: func(f *customIDAttackServer, _ context.CancelFunc) { f.searchStatus = http.StatusUnauthorized },
		},
		{
			name:  "search 200 broken JSON",
			setup: func(f *customIDAttackServer, _ context.CancelFunc) { f.searchBody = `{"data":[{"gid":"t1"` },
		},
		{
			name:  "search 200 empty body",
			setup: func(f *customIDAttackServer, _ context.CancelFunc) { f.searchBody = " " },
		},
		{
			name: "search network disconnect (connection reset)",
			setup: func(f *customIDAttackServer, _ context.CancelFunc) {
				f.onSearch = func(w http.ResponseWriter, _ *http.Request) bool {
					if hj, ok := w.(http.Hijacker); ok {
						if conn, _, err := hj.Hijack(); err == nil {
							_ = conn.Close()
							return true
						}
					}
					return false
				}
			},
		},
		{
			name: "search context canceled mid-request",
			setup: func(f *customIDAttackServer, cancel context.CancelFunc) {
				f.onSearch = func(_ http.ResponseWriter, r *http.Request) bool {
					cancel()
					<-r.Context().Done()
					return true
				}
			},
			wantCanceled: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &customIDAttackServer{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.setup(f, cancel)
			s, st, _ := setupCustomIDAttack(t, f)
			if err := st.SetSyncState(context.Background(), readmodel.KeyCustomID+"RC-1", "old-gid"); err != nil {
				t.Fatal(err)
			}

			_, err := s.TaskByCustomID(ctx, "RC-1")
			if err == nil || errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("err = %v, want non-NotFound error", err)
			}
			if c.wantErr500 {
				var apiErr *model.Error
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
					t.Fatalf("err = %v, want 500 *model.Error", err)
				}
			}
			if c.wantCanceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			bg := context.Background()
			if v, _ := st.SyncState(bg, readmodel.KeyCustomID+"RC-1"); v != "old-gid" {
				t.Fatalf("cached gid wiped: got %q, want old-gid", v)
			}
			if v, _ := st.SyncState(bg, readmodel.PlanKey(readmodel.PlanCustomID, "w1")); v != "" {
				t.Fatalf("PlanCustomID wrongly marked 402: %q", v)
			}
			ps, _ := st.SyncState(bg, readmodel.PlanKey(readmodel.PlanSearch, "w1"))
			if c.wantPlanSearch && ps == "" {
				t.Fatal("PlanSearch 402 not recorded")
			}
			if !c.wantPlanSearch && ps != "" {
				t.Fatalf("PlanSearch wrongly marked 402: %q", ps)
			}
		})
	}

	t.Run("expired PlanSearch is rechecked and cleared on success, and another workspace PlanSearch does not block", func(t *testing.T) {
		f := &customIDAttackServer{
			searchRefs: []model.Ref{{GID: "t-ok"}},
			tasks:      map[string]string{"t-ok": customIDTaskJSON("t-ok", "recovered", "RC-1")},
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		expired := s.now().Add(-readmodel.PlanRecheckAfter - time.Minute).Format(time.RFC3339)
		if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanSearch, "w1"), expired); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanSearch, "w2"), s.now().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		gid, err := s.TaskByCustomID(ctx, "RC-1")
		if err != nil || gid != "t-ok" {
			t.Fatalf("got %q, %v; want t-ok, nil", gid, err)
		}
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanSearch, "w1")); v != "" {
			t.Fatalf("expired PlanSearch for w1 not cleared: %q", v)
		}
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanSearch, "w2")); v == "" {
			t.Fatal("PlanSearch for w2 should remain untouched")
		}
	})
}

// TasksByGID failures on 500 fallback: batch errors, Partial tasks (via batch 400 fallback
// or pre-existing plainTasks), chunk 1 OK + chunk 2 failing, and spoofed local/empty GIDs.
func TestTaskByCustomID500FallbackTasksByGIDFailures(t *testing.T) {
	t.Run("chunk 1 ok, chunk 2 batch 500/429/401 keeps cache and caches nothing", func(t *testing.T) {
		for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusUnauthorized} {
			t.Run(fmt.Sprintf("chunk2_%d", status), func(t *testing.T) {
				f := &customIDAttackServer{
					tasks: map[string]string{},
					onBatch: func(call int, w http.ResponseWriter, _ *http.Request) bool {
						if call == 2 {
							w.WriteHeader(status)
							fmt.Fprintf(w, `{"errors":[{"message":"chunk2 %d"}]}`, status)
							return true
						}
						return false
					},
				}
				for i := 1; i <= 15; i++ {
					gid := fmt.Sprintf("g%d", i)
					f.searchRefs = append(f.searchRefs, model.Ref{GID: gid})
					f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":"other"}`, gid)
				}
				s, st, _ := setupCustomIDAttack(t, f)
				ctx := context.Background()
				if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "old-gid"); err != nil {
					t.Fatal(err)
				}
				_, err := s.TaskByCustomID(ctx, "RC-1")
				if err == nil || errors.Is(err, readmodel.ErrNotFound) {
					t.Fatalf("err = %v, want non-NotFound error", err)
				}
				if f.batchCalls != 2 {
					t.Fatalf("batchCalls = %d, want 2", f.batchCalls)
				}
				if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "old-gid" {
					t.Fatalf("cached gid lost: %q", v)
				}
				for i := 1; i <= 10; i++ {
					if _, err := st.GetTask(ctx, fmt.Sprintf("g%d", i)); !errors.Is(err, readmodel.ErrNotFound) {
						t.Errorf("chunk 1 non-matching task g%d cached", i)
					}
				}
			})
		}
	})

	t.Run("chunk 1 ok, chunk 2 Partial returns original 500 and keeps cache", func(t *testing.T) {
		f := &customIDAttackServer{
			refuseCFChunk: 2,
			tasks:         map[string]string{},
		}
		for i := 1; i <= 12; i++ {
			gid := fmt.Sprintf("p%d", i)
			f.searchRefs = append(f.searchRefs, model.Ref{GID: gid})
			f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":"plain"}`, gid)
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "old-gid"); err != nil {
			t.Fatal(err)
		}
		_, err := s.TaskByCustomID(ctx, "RC-1")
		var apiErr *model.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want original 500 *model.Error", err)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "old-gid" {
			t.Fatalf("cached gid lost: %q", v)
		}
	})

	t.Run("batch broken JSON and mismatched result length keep cache", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
		}{
			{"broken json", `{"data":[{"status_code":200`},
			{"wrong length", `{"data":[]}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := &customIDAttackServer{
					searchRefs: []model.Ref{{GID: "t1"}},
					onBatch: func(_ int, w http.ResponseWriter, _ *http.Request) bool {
						fmt.Fprint(w, tc.body)
						return true
					},
				}
				s, st, _ := setupCustomIDAttack(t, f)
				ctx := context.Background()
				if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "old-gid"); err != nil {
					t.Fatal(err)
				}
				_, err := s.TaskByCustomID(ctx, "RC-1")
				if err == nil || errors.Is(err, readmodel.ErrNotFound) {
					t.Fatalf("err = %v, want non-NotFound error", err)
				}
				if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "old-gid" {
					t.Fatalf("cached gid lost: %q", v)
				}
			})
		}
	})

	t.Run("all candidates gone (404) returns ErrNotFound and clears cache", func(t *testing.T) {
		f := &customIDAttackServer{
			searchRefs: []model.Ref{{GID: "gone1"}, {GID: "gone2"}},
			taskStatus: map[string]int{"gone1": http.StatusNotFound, "gone2": http.StatusNotFound},
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "old-gid"); err != nil {
			t.Fatal(err)
		}
		_, err := s.TaskByCustomID(ctx, "RC-1")
		if !errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "" {
			t.Fatalf("stale gid not cleared: %q", v)
		}
	})

	t.Run("spoofed local: or empty GID inside task payload is ignored", func(t *testing.T) {
		f := &customIDAttackServer{
			searchRefs: []model.Ref{{GID: "t1"}, {GID: "t2"}},
			tasks: map[string]string{
				"t1": customIDTaskJSON("local:spoof", "spoof local", "RC-1"),
				"t2": customIDTaskJSON("", "spoof empty", "RC-1"),
			},
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		_, err := s.TaskByCustomID(ctx, "RC-1")
		if !errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if _, err := st.GetTask(ctx, "local:spoof"); !errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("spoofed local task cached: %v", err)
		}
	})
}

// Non-500 responses from TaskByCustomID (502, 503, 504, 404, 403, 402, 401, 429, broken JSON)
// must NEVER fall back to SearchTasks.
func TestTaskByCustomIDNon500NeverFallsBackToSearch(t *testing.T) {
	cases := []struct {
		name           string
		status         int
		body           string
		wantNotFound   bool
		wantPlanCustom bool
		wantCacheKept  bool
	}{
		{name: "502 Bad Gateway", status: http.StatusBadGateway, wantCacheKept: true},
		{name: "503 Service Unavailable", status: http.StatusServiceUnavailable, wantCacheKept: true},
		{name: "504 Gateway Timeout", status: http.StatusGatewayTimeout, wantCacheKept: true},
		{name: "404 Not Found", status: http.StatusNotFound, wantNotFound: true, wantCacheKept: false},
		{name: "403 Forbidden", status: http.StatusForbidden, wantCacheKept: true},
		{name: "402 Payment Required", status: http.StatusPaymentRequired, wantPlanCustom: true, wantCacheKept: true},
		{name: "401 Unauthorized", status: http.StatusUnauthorized, wantCacheKept: true},
		{name: "429 Too Many Requests", status: http.StatusTooManyRequests, wantCacheKept: true},
		{name: "200 broken JSON", status: http.StatusOK, body: `{"data":{"gid":"t1"`, wantCacheKept: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &customIDAttackServer{customStatus: tc.status, customBody: tc.body}
			s, st, _ := setupCustomIDAttack(t, f)
			ctx := context.Background()
			if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "old-gid"); err != nil {
				t.Fatal(err)
			}

			_, err := s.TaskByCustomID(ctx, "RC-1")
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.wantNotFound && !errors.Is(err, readmodel.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if tc.wantPlanCustom && !errors.Is(err, ErrCustomIDUnavailable) {
				t.Fatalf("err = %v, want ErrCustomIDUnavailable", err)
			}
			if f.searchCalls != 0 || f.batchCalls != 0 || f.taskGetCalls != 0 {
				t.Fatalf("unexpected fallback calls: search=%d batch=%d get=%d", f.searchCalls, f.batchCalls, f.taskGetCalls)
			}
			gotCache, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1")
			if tc.wantCacheKept && gotCache != "old-gid" {
				t.Fatalf("cached gid = %q, want old-gid", gotCache)
			}
			if !tc.wantCacheKept && gotCache != "" {
				t.Fatalf("cached gid = %q, want cleared", gotCache)
			}
			pc, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomID, "w1"))
			if tc.wantPlanCustom && pc == "" {
				t.Fatal("PlanCustomID 402 not recorded")
			}
			if !tc.wantPlanCustom && pc != "" {
				t.Fatalf("PlanCustomID wrongly recorded: %q", pc)
			}
			if ps, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanSearch, "w1")); ps != "" {
				t.Fatalf("PlanSearch wrongly recorded: %q", ps)
			}
		})
	}
}

// Control characters (ESC/OSC/CSI/APC/C1) in custom_id, display_value, text_value, task name,
// notes, and Asana error messages are neutralized by termsafe.
func TestTaskByCustomID500FallbackTerminalSafety(t *testing.T) {
	osc := "\x1b]52;c;ZXZpbA==\x07"
	csi := "\x1b[2J\x1b[31m"
	c1 := "\u009b31m"
	apc := "\x1b_Ga=T,f=100;AAAA\x1b\\"

	t.Run("matched task fields are termsafe on read", func(t *testing.T) {
		taskJSON, err := json.Marshal(map[string]any{
			"gid":   "t-safe",
			"name":  "名前" + osc + csi + c1 + apc,
			"notes": "本文" + osc + "\r\x7f",
			"custom_fields": []map[string]any{
				{
					"gid":                 "cf-id",
					"name":                "ID" + osc + csi,
					"resource_subtype":    "text",
					"representation_type": "custom_id",
					"id_prefix":           "RC",
					"is_value_read_only":  true,
					"display_value":       "RC-1",
					"text_value":          "RC-1" + osc + c1,
				},
				{
					"gid":                 "cf-memo",
					"name":                "メモ" + apc,
					"resource_subtype":    "text",
					"representation_type": "text",
					"display_value":       "表示" + osc + csi,
					"text_value":          "値" + osc + c1,
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		f := &customIDAttackServer{
			searchRefs: []model.Ref{{GID: "t-safe"}},
			tasks:      map[string]string{"t-safe": string(taskJSON)},
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()

		gid, err := s.TaskByCustomID(ctx, "RC-1")
		if err != nil || gid != "t-safe" {
			t.Fatalf("got %q, %v", gid, err)
		}
		d, err := st.GetTask(ctx, "t-safe")
		if err != nil {
			t.Fatal(err)
		}
		for label, val := range map[string]string{"name": d.Name, "notes": d.Notes} {
			if !termsafe.Safe(val) {
				t.Errorf("%s not termsafe: %q", label, val)
			}
		}
		if len(d.CustomFields) < 2 {
			t.Fatalf("custom fields missing: %+v", d.CustomFields)
		}
		for _, cf := range d.CustomFields {
			if !termsafe.Safe(cf.Name) {
				t.Errorf("cf.Name not termsafe: %q", cf.Name)
			}
			if !termsafe.Safe(cf.Display) {
				t.Errorf("cf.Display not termsafe: %q", cf.Display)
			}
			if v, ok := cf.Value.(string); ok && !termsafe.Safe(v) {
				t.Errorf("cf.Value not termsafe: %q", v)
			}
		}
	})

	t.Run("control characters in custom_id query and 500 error message are termsafe in errors", func(t *testing.T) {
		evilID := "RC-1" + osc + c1
		errBody, _ := json.Marshal(map[string]any{
			"errors": []map[string]string{{"message": "boom " + osc + csi + c1}},
		})
		f := &customIDAttackServer{
			customStatus: http.StatusInternalServerError,
			customBody:   string(errBody),
			searchStatus: http.StatusPaymentRequired,
		}
		s, _, _ := setupCustomIDAttack(t, f)
		_, err := s.TaskByCustomID(context.Background(), evilID)
		if err == nil {
			t.Fatal("expected error")
		}
		if !termsafe.Safe(err.Error()) {
			t.Errorf("500 error message not termsafe: %q", err.Error())
		}

		// Not-found error message with control characters in customID is also termsafe.
		f2 := &customIDAttackServer{searchRefs: nil}
		s2, _, _ := setupCustomIDAttack(t, f2)
		_, err = s2.TaskByCustomID(context.Background(), evilID)
		if !errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if !termsafe.Safe(err.Error()) {
			t.Errorf("not-found error message not termsafe: %q", err.Error())
		}
	})

	t.Run("display_value carrying control characters does not match clean custom_id, and matching dirty id is still termsafe in store", func(t *testing.T) {
		dirtyID := "RC-1" + osc
		taskJSON, _ := json.Marshal(map[string]any{
			"gid":  "t-dirty",
			"name": "task",
			"custom_fields": []map[string]any{{
				"gid":                 "cf-id",
				"name":                "ID",
				"resource_subtype":    "text",
				"representation_type": "custom_id",
				"id_prefix":           "RC",
				"display_value":       dirtyID,
				"text_value":          dirtyID,
			}},
		})
		f := &customIDAttackServer{
			searchRefs: []model.Ref{{GID: "t-dirty"}},
			tasks:      map[string]string{"t-dirty": string(taskJSON)},
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		if _, err := s.TaskByCustomID(ctx, "RC-1"); !errors.Is(err, readmodel.ErrNotFound) {
			t.Fatalf("clean RC-1 matched dirty display_value: %v", err)
		}
		gid, err := s.TaskByCustomID(ctx, dirtyID)
		if err != nil || gid != "t-dirty" {
			t.Fatalf("dirtyID lookup = %q, %v", gid, err)
		}
		d, err := st.GetTask(ctx, "t-dirty")
		if err != nil {
			t.Fatal(err)
		}
		for _, cf := range d.CustomFields {
			if !termsafe.Safe(cf.Display) {
				t.Errorf("cf.Display not termsafe: %q", cf.Display)
			}
			if v, ok := cf.Value.(string); ok && !termsafe.Safe(v) {
				t.Errorf("cf.Value not termsafe: %q", v)
			}
		}
	})
}

// Concurrent calls to TaskByCustomID on 500 fallback under -race: no races, no store errors,
// and consistent cache & plan state.
func TestTaskByCustomID500FallbackConcurrent(t *testing.T) {
	t.Run("parallel fallback lookups with hits and misses", func(t *testing.T) {
		f := &customIDAttackServer{tasks: map[string]string{}}
		for i := 1; i <= 12; i++ {
			gid := fmt.Sprintf("c%d", i)
			f.searchRefs = append(f.searchRefs, model.Ref{GID: gid})
			switch i {
			case 2:
				f.tasks[gid] = customIDTaskJSON(gid, "hit 1", "RC-1")
			case 12:
				f.tasks[gid] = customIDTaskJSON(gid, "hit 2", "RC-2")
			default:
				f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":"noise"}`, gid)
			}
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()

		var wg gosync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				switch n % 3 {
				case 0:
					gid, err := s.TaskByCustomID(ctx, "RC-1")
					if err != nil || gid != "c2" {
						t.Errorf("RC-1 = %q, %v", gid, err)
					}
				case 1:
					gid, err := s.TaskByCustomID(ctx, "RC-2")
					if err != nil || gid != "c12" {
						t.Errorf("RC-2 = %q, %v", gid, err)
					}
				case 2:
					_, err := s.TaskByCustomID(ctx, "RC-999")
					if !errors.Is(err, readmodel.ErrNotFound) {
						t.Errorf("RC-999 = %v, want ErrNotFound", err)
					}
				}
			}(i)
		}
		wg.Wait()

		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "c2" {
			t.Errorf("RC-1 cache = %q, want c2", v)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-2"); v != "c12" {
			t.Errorf("RC-2 cache = %q, want c12", v)
		}
		if _, err := st.GetTask(ctx, "c2"); err != nil {
			t.Errorf("c2 not cached: %v", err)
		}
		if _, err := st.GetTask(ctx, "c12"); err != nil {
			t.Errorf("c12 not cached: %v", err)
		}
	})

	t.Run("parallel fallback hitting search 402", func(t *testing.T) {
		var searchHits atomic.Int32
		f := &customIDAttackServer{
			searchStatus: http.StatusPaymentRequired,
			onSearch: func(w http.ResponseWriter, _ *http.Request) bool {
				searchHits.Add(1)
				w.WriteHeader(http.StatusPaymentRequired)
				fmt.Fprint(w, `{"errors":[{"message":"premium only"}]}`)
				return true
			},
		}
		s, st, _ := setupCustomIDAttack(t, f)
		ctx := context.Background()
		if err := st.SetSyncState(ctx, readmodel.KeyCustomID+"RC-1", "old-gid"); err != nil {
			t.Fatal(err)
		}

		var wg gosync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.TaskByCustomID(ctx, "RC-1")
				var apiErr *model.Error
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
					t.Errorf("err = %v, want 500 *model.Error", err)
				}
			}()
		}
		wg.Wait()

		if searchHits.Load() < 1 {
			t.Fatal("expected at least 1 search call")
		}
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanSearch, "w1")); v == "" {
			t.Error("PlanSearch 402 not recorded")
		}
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomID, "w1")); v != "" {
			t.Errorf("PlanCustomID wrongly recorded: %q", v)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyCustomID+"RC-1"); v != "old-gid" {
			t.Errorf("cached gid lost: %q", v)
		}
	})
}
