package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// eventsAsana is a fake Asana with one workspace (w1) and one member project (p1) that
// serves GET /events and POST /batch (#27).
type eventsAsana struct {
	t  *testing.T
	mu stdsync.Mutex
	// tasks in p1 by gid (JSON objects); a missing gid answers 404.
	tasks map[string]string
	// events answers GET /events?resource=p1: sync token received -> the response.
	events func(token string) (status int, body string)
	log    []string // "GET /tasks?project=p1&modified_since=..." etc.
	// hook, if set, may answer a request itself (returning true), e.g. to inject failures.
	hook func(w http.ResponseWriter, r *http.Request) bool
}

// setHook sets (or with nil, clears) the hook under the lock the server goroutines take.
func (f *eventsAsana) setHook(h func(w http.ResponseWriter, r *http.Request) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = h
}

func newEventsAsana(t *testing.T) *eventsAsana {
	return &eventsAsana{t: t, tasks: map[string]string{
		"t1": `{"gid":"t1","name":"One","memberships":[{"project":{"gid":"p1"}}]}`,
		"t2": `{"gid":"t2","name":"Two","memberships":[{"project":{"gid":"p1"}}]}`,
	}}
}

func (f *eventsAsana) setTask(gid, name string, extra string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[gid] = fmt.Sprintf(`{"gid":%q,"name":%q,"memberships":[{"project":{"gid":"p1"}}]%s}`, gid, name, extra)
}

func (f *eventsAsana) calls(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.log {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (f *eventsAsana) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = nil
}

func (f *eventsAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	entry := r.Method + " " + r.URL.Path
	for _, k := range []string{"project", "assignee", "modified_since", "sync"} {
		if v := q.Get(k); v != "" {
			entry += " " + k + "=" + v
		}
	}
	f.log = append(f.log, entry)
	if f.hook != nil && f.hook(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/users/me":
		fmt.Fprint(w, `{"data":{"gid":"u1","name":"Me"}}`)
	case r.URL.Path == "/workspaces":
		fmt.Fprint(w, `{"data":[{"gid":"w1","name":"WS"}]}`)
	case r.URL.Path == "/projects":
		fmt.Fprint(w, `{"data":[{"gid":"p1","name":"P","members":[{"gid":"u1"}]}]}`)
	case r.URL.Path == "/projects/p1/custom_field_settings":
		fmt.Fprint(w, `{"data":[]}`)
	case r.URL.Path == "/projects/p1/sections":
		fmt.Fprint(w, `{"data":[{"gid":"s1","name":"Todo"}]}`)
	case r.URL.Path == "/tasks" && q.Get("assignee") == "me":
		fmt.Fprint(w, `{"data":[]}`)
	case r.URL.Path == "/tasks" && q.Get("project") == "p1":
		var all []string
		for _, js := range f.tasks {
			all = append(all, js)
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(all, ","))
	case strings.HasPrefix(r.URL.Path, "/tasks/") && strings.HasSuffix(r.URL.Path, "/subtasks"):
		fmt.Fprint(w, `{"data":[]}`)
	case strings.HasPrefix(r.URL.Path, "/tasks/") && r.Method == http.MethodGet:
		gid := strings.TrimPrefix(r.URL.Path, "/tasks/")
		if js, ok := f.tasks[gid]; ok {
			fmt.Fprintf(w, `{"data":%s}`, js)
		} else {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"not found"}]}`)
		}
	case r.URL.Path == "/events":
		if q.Get("resource") != "p1" {
			f.t.Errorf("events for %q", q.Get("resource"))
		}
		status, body := http.StatusPreconditionFailed, `{"errors":[{"message":"Sync token invalid or too old"}],"sync":"T0"}`
		if f.events != nil {
			status, body = f.events(q.Get("sync"))
		}
		if status == 0 { // drop the connection: offline in the middle of the sync
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	case r.URL.Path == "/batch" && r.Method == http.MethodPost:
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Data struct {
				Actions []struct {
					Method       string         `json:"method"`
					RelativePath string         `json:"relative_path"`
					Options      map[string]any `json:"options"`
				} `json:"actions"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			f.t.Errorf("batch body: %v", err)
		}
		if n := len(req.Data.Actions); n == 0 || n > asana.MaxBatchActions {
			f.t.Errorf("batch with %d actions", n)
		}
		var results []string
		for _, a := range req.Data.Actions {
			gid := strings.TrimPrefix(a.RelativePath, "/tasks/")
			f.log = append(f.log, "BATCH "+gid)
			if a.Method != "get" || a.Options["fields"] == nil {
				f.t.Errorf("batch action %+v", a)
			}
			if js, ok := f.tasks[gid]; ok {
				results = append(results, fmt.Sprintf(`{"status_code":200,"body":{"data":%s}}`, js))
			} else {
				results = append(results, `{"status_code":404,"body":{"errors":[{"message":"Not Found"}]}}`)
			}
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(results, ","))
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"not here"}]}`)
	}
}

func eventsJSON(events []string, sync string, hasMore bool) string {
	return fmt.Sprintf(`{"data":[%s],"sync":%q,"has_more":%t}`, strings.Join(events, ","), sync, hasMore)
}

func taskEvent(gid, action string) string {
	return fmt.Sprintf(`{"action":%q,"resource":{"gid":%q,"resource_type":"task"},"created_at":"2026-11-13T00:00:00Z"}`, action, gid)
}

type eventsEnv struct {
	fake *eventsAsana
	srv  *httptest.Server
	st   *store.Store
	s    *Syncer
	now  time.Time
}

func setupEvents(t *testing.T) *eventsEnv {
	t.Helper()
	e := &eventsEnv{fake: newEventsAsana(t), now: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	e.srv = httptest.NewServer(e.fake)
	t.Cleanup(e.srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.st = st
	api := asana.NewClient(e.srv.Client())
	api.BaseURL = e.srv.URL
	api.MaxRetries = 0 // a 429 or 5xx reaches the syncer at once
	e.s = &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}, now: func() time.Time { return e.now }}
	return e
}

// run syncs d after the previous run and returns the result.
func (e *eventsEnv) run(t *testing.T, d time.Duration) Result {
	t.Helper()
	e.now = e.now.Add(d)
	e.fake.reset()
	res, err := e.s.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("run: %v\n%v", err, e.fake.log)
	}
	return res
}

func (e *eventsEnv) token(t *testing.T) string {
	v, err := e.st.SyncState(context.Background(), store.KeyEvents+"p1")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *eventsEnv) taskName(t *testing.T, gid string) string {
	t.Helper()
	d, err := e.st.GetTask(context.Background(), gid)
	if err != nil {
		return ""
	}
	return d.Name
}

// The first sync gets a token from a 412 and fetches the project; later syncs read only
// the events (all pages) and fetch just the tasks they name, in batches.
func TestEventsFirst412ThenPagedEvents(t *testing.T) {
	e := setupEvents(t)
	res := e.run(t, 0)
	if !res.Full || res.Fallbacks != 1 || e.token(t) != "T0" {
		t.Fatalf("first run = %+v token %q", res, e.token(t))
	}
	if e.taskName(t, "t1") != "One" || e.taskName(t, "t2") != "Two" {
		t.Fatal("project not fetched after the first 412")
	}

	// Changes in Asana: t1 renamed, t2 deleted, t3 added with a subtask (on page 2).
	e.fake.setTask("t1", "One (renamed)", "")
	delete(e.fake.tasks, "t2")
	e.fake.setTask("t3", "Three", `,"num_subtasks":1`)
	e.fake.events = func(token string) (int, string) {
		switch token {
		case "T0":
			return 200, eventsJSON([]string{taskEvent("t1", "changed"), taskEvent("t2", "deleted"), taskEvent("t1", "changed")}, "T1", true)
		case "T1":
			return 200, eventsJSON([]string{taskEvent("t3", "added"),
				`{"action":"added","resource":{"gid":"st1","resource_type":"story"},"parent":{"gid":"t1","resource_type":"task"}}`}, "T2", false)
		case "T2":
			return 200, eventsJSON(nil, "T2", false)
		}
		t.Errorf("unexpected token %q", token)
		return 400, `{}`
	}
	res = e.run(t, 5*time.Minute)
	if res.Full || res.Poll || res.Fallbacks != 0 || res.Events != 5 {
		t.Fatalf("events run = %+v", res)
	}
	if got := e.fake.calls("GET /events"); len(got) != 2 {
		t.Fatalf("events pages read = %v", got)
	}
	if got := e.fake.calls("GET /tasks project=p1"); len(got) != 0 {
		t.Fatalf("project fetched although events were usable: %v", got)
	}
	if got := e.fake.calls("GET /projects/p1/sections"); len(got) != 0 {
		t.Fatalf("sections fetched without a section event: %v", got)
	}
	if got := e.fake.calls("BATCH "); strings.Join(got, ",") != "BATCH t1,BATCH t2,BATCH t3" {
		t.Fatalf("batched fetches = %v (each changed task once, stories ignored)", got)
	}
	if got := e.fake.calls("GET /tasks/t3/subtasks"); len(got) != 1 {
		t.Fatalf("subtasks of a task added to the project = %v", got)
	}
	if got := e.fake.calls("GET /tasks/t1/subtasks"); len(got) != 0 {
		t.Fatalf("subtasks of a changed task are reported by their own events: %v", got)
	}
	if e.taskName(t, "t1") != "One (renamed)" || e.taskName(t, "t3") != "Three" {
		t.Fatal("changed tasks not stored")
	}
	if e.taskName(t, "t2") != "" {
		t.Fatal("deleted task still in the store")
	}
	if e.token(t) != "T2" {
		t.Fatalf("token = %q, want the last page's", e.token(t))
	}
	// My Tasks (tasks in no project) is still fetched each time, with modified_since.
	if got := e.fake.calls("GET /tasks assignee=me"); len(got) != 1 || !strings.Contains(got[0], "modified_since=2026-11-13T08:58:00Z") {
		t.Fatalf("my tasks = %v", got)
	}

	// Nothing changed: one events request for the project, nothing else about it.
	e.run(t, 5*time.Minute)
	if got := e.fake.calls("GET /events"); len(got) != 1 {
		t.Fatalf("events = %v", got)
	}
	if n := len(e.fake.calls("BATCH ")) + len(e.fake.calls("GET /tasks project")) + len(e.fake.calls("GET /projects/p1/")); n != 0 {
		t.Fatalf("idle sync fetched the project: %v", e.fake.log)
	}
}

// More than 10 changed tasks are fetched 10 per batch.
func TestEventsBatchesOfTen(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	var evs []string
	for i := range 23 {
		gid := fmt.Sprintf("n%d", i)
		e.fake.setTask(gid, "N", "")
		evs = append(evs, taskEvent(gid, "added"))
	}
	e.fake.events = func(string) (int, string) { return 200, eventsJSON(evs, "T1", false) }
	e.run(t, 5*time.Minute)
	if got := e.fake.calls("POST /batch"); len(got) != 3 {
		t.Fatalf("batch requests = %d, want 3", len(got))
	}
	if e.taskName(t, "n22") != "N" {
		t.Fatal("tasks of the last batch not stored")
	}
}

// An expired token (412) means events were lost: the project is fetched with
// modified_since since the last sync, and the new token is kept.
func TestEventsExpiredTokenFallsBack(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.fake.events = func(token string) (int, string) {
		if token == "T0" {
			return 412, `{"errors":[{"message":"Sync token invalid or too old"}],"sync":"T9"}`
		}
		return 200, eventsJSON(nil, token, false)
	}
	e.fake.setTask("t1", "changed while the token expired", "")
	res := e.run(t, 5*time.Minute)
	if res.Fallbacks != 1 || res.Poll {
		t.Fatalf("result = %+v", res)
	}
	if got := e.fake.calls("GET /tasks project=p1"); len(got) != 1 || !strings.Contains(got[0], "modified_since=2026-11-13T08:58:00Z") {
		t.Fatalf("fallback fetch = %v", got)
	}
	if e.token(t) != "T9" || e.taskName(t, "t1") != "changed while the token expired" {
		t.Fatalf("token %q, t1 %q", e.token(t), e.taskName(t, "t1"))
	}
	// Back to events.
	e.run(t, 5*time.Minute)
	if got := e.fake.calls("GET /tasks project=p1"); len(got) != 0 {
		t.Fatalf("still falling back: %v", got)
	}
}

// Events are delivered at most once: a change without an event is caught by the hourly
// poll, which looks back to the previous poll.
func TestEventsMissedChangeCaughtByPoll(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0) // full, at 09:00
	e.fake.events = func(token string) (int, string) { return 200, eventsJSON(nil, "T0", false) }
	e.fake.setTask("t1", "no event for this", "")
	for range 11 { // every 5 minutes until 09:55
		if res := e.run(t, 5*time.Minute); res.Poll {
			t.Fatalf("poll too early: %+v", res)
		}
	}
	if e.taskName(t, "t1") != "One" {
		t.Fatal("test assumes the change is missed by events")
	}
	res := e.run(t, 5*time.Minute) // 10:00
	if !res.Poll || res.Full {
		t.Fatalf("expected a poll an hour after the last: %+v", res)
	}
	if got := e.fake.calls("GET /tasks project=p1"); len(got) != 1 || !strings.Contains(got[0], "modified_since=2026-11-13T08:58:00Z") {
		t.Fatalf("poll = %v (must look back to the previous poll)", got)
	}
	if e.taskName(t, "t1") != "no event for this" {
		t.Fatal("missed change not caught by the poll")
	}
	if got := e.fake.calls("GET /projects/p1/sections"); len(got) != 1 {
		t.Fatalf("poll also refreshes sections: %v", got)
	}
	if res := e.run(t, 5*time.Minute); res.Poll {
		t.Fatalf("poll again right after: %+v", res)
	}
}

// 429, 5xx and a dropped connection on /events do not stop the sync: the project is
// fetched with modified_since and the token is kept for the next sync.
func TestEventsFailuresFallBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"429", http.StatusTooManyRequests},
		{"500", http.StatusInternalServerError},
		{"503", http.StatusServiceUnavailable},
		{"offline", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupEvents(t)
			e.run(t, 0)
			e.fake.events = func(string) (int, string) { return tc.status, `{"errors":[{"message":"busy"}]}` }
			e.fake.setTask("t1", "changed", "")
			res := e.run(t, 5*time.Minute)
			if res.Fallbacks != 1 {
				t.Fatalf("result = %+v", res)
			}
			if got := e.fake.calls("GET /tasks project=p1"); len(got) != 1 || !strings.Contains(got[0], "modified_since=2026-11-13T08:58:00Z") {
				t.Fatalf("fallback = %v", got)
			}
			if e.taskName(t, "t1") != "changed" || e.token(t) != "T0" {
				t.Fatalf("t1 %q token %q", e.taskName(t, "t1"), e.token(t))
			}
			// Recovered: events again, from the kept token.
			e.fake.events = func(token string) (int, string) {
				if token != "T0" {
					t.Errorf("token = %q", token)
				}
				return 200, eventsJSON(nil, "T1", false)
			}
			if res := e.run(t, 5*time.Minute); res.Fallbacks != 0 || e.token(t) != "T1" {
				t.Fatalf("after recovery = %+v token %q", res, e.token(t))
			}
		})
	}
}

// A failure on the second page keeps the first page's token and changes, and falls back.
func TestEventsFailureOnSecondPage(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.fake.setTask("t1", "page one", "")
	e.fake.events = func(token string) (int, string) {
		if token == "T0" {
			return 200, eventsJSON([]string{taskEvent("t1", "changed")}, "T1", true)
		}
		return 503, `{}`
	}
	res := e.run(t, 5*time.Minute)
	if res.Fallbacks != 1 || e.token(t) != "T1" || e.taskName(t, "t1") != "page one" {
		t.Fatalf("result %+v token %q", res, e.token(t))
	}
}

// Offline altogether: the sync fails as before and nothing is lost; the next sync reads
// the events from the same token.
func TestEventsOfflineKeepsToken(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.srv.Close()
	e.now = e.now.Add(5 * time.Minute)
	if _, err := e.s.Run(context.Background(), false); err == nil {
		t.Fatal("offline sync should fail")
	}
	if e.token(t) != "T0" {
		t.Fatalf("token = %q", e.token(t))
	}
}

// Asana cannot give the tasks the events name (the whole batch fails, or one task always
// answers 500): the sync does not stop; the project is fetched with modified_since,
// which covers those changes, and the rest of the sync goes on.
func TestEventsBatchFailureFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		batch func(w http.ResponseWriter) bool // answers the batch itself if it returns true
	}{
		{"batch 502", func(w http.ResponseWriter) bool { w.WriteHeader(http.StatusBadGateway); return true }},
		{"batch 429", func(w http.ResponseWriter) bool { w.WriteHeader(http.StatusTooManyRequests); return true }},
		{"one task 500", func(w http.ResponseWriter) bool {
			fmt.Fprint(w, `{"data":[{"status_code":500,"body":{"errors":[{"message":"broken"}]}}]}`)
			return true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupEvents(t)
			e.run(t, 0)
			e.fake.events = func(string) (int, string) { return 200, eventsJSON([]string{taskEvent("t1", "changed")}, "T1", false) }
			e.fake.setHook(func(w http.ResponseWriter, r *http.Request) bool {
				return r.URL.Path == "/batch" && tc.batch(w)
			})
			e.fake.setTask("t1", "via the fallback", "")
			res := e.run(t, 5*time.Minute)
			if res.Fallbacks != 1 || res.Poll {
				t.Fatalf("result = %+v", res)
			}
			if got := e.fake.calls("GET /tasks project=p1"); len(got) != 1 || !strings.Contains(got[0], "modified_since=2026-11-13T08:58:00Z") {
				t.Fatalf("fallback fetch = %v", got)
			}
			if e.taskName(t, "t1") != "via the fallback" || e.token(t) != "T1" {
				t.Fatalf("t1 %q token %q", e.taskName(t, "t1"), e.token(t))
			}
			if got := e.fake.calls("GET /users/me/notifications"); len(got) == 0 {
				t.Fatalf("the sync stopped at the project: %v", e.fake.log)
			}
			if v, _ := e.st.SyncState(context.Background(), store.KeyLastSync); v != "2026-11-13T09:05:00Z" {
				t.Fatalf("last_sync = %q", v)
			}
		})
	}
}

// Offline while fetching the changed tasks: the sync fails as before and keeps the token,
// so the next sync reads the same events.
func TestEventsBatchOfflineKeepsToken(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.fake.events = func(string) (int, string) { return 200, eventsJSON([]string{taskEvent("t1", "changed")}, "T1", false) }
	e.fake.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/batch" {
			return false
		}
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return true
	})
	e.now = e.now.Add(5 * time.Minute)
	if _, err := e.s.Run(context.Background(), false); err == nil {
		t.Fatal("want error")
	}
	if e.token(t) != "T0" {
		t.Fatalf("token advanced past events that were not applied: %q", e.token(t))
	}
	e.fake.setHook(nil)
	e.fake.setTask("t1", "applied later", "")
	e.run(t, 5*time.Minute)
	if e.token(t) != "T1" || e.taskName(t, "t1") != "applied later" {
		t.Fatalf("token %q t1 %q", e.token(t), e.taskName(t, "t1"))
	}
}

// An unsent local edit wins over the task fetched for an event (as with any pull).
func TestEventsKeepUnsentEdits(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	ctx := context.Background()
	if err := e.st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "local edit"}, e.now); err != nil {
		t.Fatal(err)
	}
	e.s.PullOnly = true // keep it queued
	e.fake.setTask("t1", "server edit", "")
	e.fake.events = func(string) (int, string) {
		return 200, eventsJSON([]string{taskEvent("t1", "changed")}, "T1", false)
	}
	e.run(t, 5*time.Minute)
	if got := e.taskName(t, "t1"); got != "local edit" {
		t.Fatalf("name = %q, the unsent edit must win", got)
	}
	// A task with queued changes is not removed when Asana says it is gone.
	delete(e.fake.tasks, "t1")
	e.fake.events = func(string) (int, string) {
		return 200, eventsJSON([]string{taskEvent("t1", "deleted")}, "T2", false)
	}
	e.run(t, 5*time.Minute)
	if e.taskName(t, "t1") == "" {
		t.Fatal("task with queued changes removed")
	}
}

// Section, custom field and status events refresh just those.
func TestEventsProjectParts(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.fake.events = func(string) (int, string) {
		return 200, eventsJSON([]string{`{"action":"changed","resource":{"gid":"s1","resource_type":"section"}}`}, "T1", false)
	}
	e.run(t, 5*time.Minute)
	if got := e.fake.calls("GET /projects/p1/sections"); len(got) != 1 {
		t.Fatalf("sections = %v", got)
	}
	if got := e.fake.calls("GET /tasks project"); len(got) != 0 {
		t.Fatalf("tasks = %v", got)
	}
}

// The token goes away with its project (#20).
func TestEventsTokenPrunedWithProject(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	ctx := context.Background()
	if err := e.st.PruneProjects(ctx, "w1", nil); err != nil {
		t.Fatal(err)
	}
	if e.token(t) != "" {
		t.Fatal("token of a removed project kept")
	}
}
