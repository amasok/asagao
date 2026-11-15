package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
)

// serverTask is the fake server's state for one task.
type serverTask struct {
	Name      string    `json:"name"`
	Notes     string    `json:"notes"`
	Completed bool      `json:"completed"`
	DueOn     string    `json:"due_on"`
	Section   string    `json:"-"`
	Project   string    `json:"-"`
	Created   time.Time `json:"-"`
}

type writeServer struct {
	t     *testing.T
	mu    stdsync.Mutex
	tasks map[string]*serverTask
	calls []string // "METHOD path body"
	next  int
	// hook, if set, may answer a request itself (returning true) to inject failures.
	hook func(rw http.ResponseWriter, r *http.Request) bool
	// failAfter["POST /tasks"] = n: the next n such requests are applied, then answered 500.
	failAfter map[string]int
	stories   map[string][]string // task gid -> story JSON objects
	atts      map[string][]string // parent gid -> attachment JSON objects
	statuses  map[string][]string // project gid -> status update JSON objects
	tags      []string            // tag JSON objects
	requests  int
}

// maxRequests stops a Push that loops: the fake server fails the test and rejects
// everything after this many requests, which ends the loop (4xx marks entries failed).
const maxRequests = 200

func (w *writeServer) taskJSON(gid string) string {
	st := w.tasks[gid]
	ms := "[]"
	if st.Project != "" {
		ms = fmt.Sprintf(`[{"project":{"gid":%q},"section":{"gid":%q}}]`, st.Project, st.Section)
	}
	// name and notes as JSON strings (%q writes Go escapes such as \x1b, which are not JSON)
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return fmt.Sprintf(`{"gid":%q,"name":%s,"notes":%s,"completed":%t,"due_on":%q,"memberships":%s,"modified_at":"2026-11-13T00:00:00Z","created_at":%q,"created_by":{"gid":"me"}}`,
		gid, quote(st.Name), quote(st.Notes), st.Completed, st.DueOn, ms, st.Created.Format(time.RFC3339))
}

// addTask registers a task created just now (e.g. by a request whose answer was lost).
func (w *writeServer) addTask(gid, name, project string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tasks[gid] = &serverTask{Name: name, Project: project, Created: time.Now().UTC()}
}

// addComment registers a comment created just now.
func (w *writeServer) addComment(task, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next++
	w.addStoryLocked(task, text)
}

func (w *writeServer) addStoryLocked(task, text string) string {
	if w.stories == nil {
		w.stories = map[string][]string{}
	}
	js := fmt.Sprintf(`{"gid":"story%d","type":"comment","text":%q,"created_by":{"gid":"me","name":"Me"},"created_at":%q}`,
		w.next, text, time.Now().UTC().Format(time.RFC3339))
	w.stories[task] = append(w.stories[task], js)
	return js
}

// addRichStoryLocked stores a rich-text comment the way Asana does: mentions are filled in
// with the user's name and profile link, and the plain text is Asana's own rendering
// (links with their URL, mentions as profile URLs, list bullets), which differs from the
// text asana-tui derives from Markdown.
func (w *writeServer) addRichStoryLocked(task, htmlText string) string {
	mention := regexp.MustCompile(`<a data-asana-gid="([^"]+)"/>`)
	full := mention.ReplaceAllString(htmlText, `<a href="https://app.asana.com/0/profile/$1" data-asana-gid="$1" data-asana-type="user">@Partner</a>`)
	plain := mention.ReplaceAllString(htmlText, `https://app.asana.com/0/profile/$1`)
	plain = regexp.MustCompile(`<a href="([^"]+)">([^<]*)</a>`).ReplaceAllString(plain, `$2 ($1)`)
	plain = strings.ReplaceAll(plain, "<li>", "\n• ")
	plain = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(plain, "")
	if w.stories == nil {
		w.stories = map[string][]string{}
	}
	js := fmt.Sprintf(`{"gid":"story%d","type":"comment","text":%q,"html_text":%q,"created_by":{"gid":"me","name":"Me"},"created_at":%q}`,
		w.next, plain, full, time.Now().UTC().Format(time.RFC3339))
	w.stories[task] = append(w.stories[task], js)
	return js
}

func (w *writeServer) setFailAfter(key string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failAfter == nil {
		w.failAfter = map[string]int{}
	}
	w.failAfter[key] = n
}

func (w *writeServer) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(b, &body)
	if r.Method != http.MethodGet {
		w.calls = append(w.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(b)))
	}
	if w.requests++; w.requests > maxRequests {
		w.t.Errorf("more than %d requests: Push is looping (%s %s)", maxRequests, r.Method, r.URL.Path)
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	if w.hook != nil && w.hook(rw, r) {
		return
	}
	if key := r.Method + " " + r.URL.Path; w.failAfter[key] > 0 {
		w.failAfter[key]--
		w.handle(httptest.NewRecorder(), r, b, body.Data) // applied, but the answer is lost
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.handle(rw, r, b, body.Data)
}

func (w *writeServer) handle(rw http.ResponseWriter, r *http.Request, b []byte, data map[string]any) {
	body := struct{ Data map[string]any }{data}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/users/me":
		fmt.Fprint(rw, `{"data":{"gid":"me","name":"Me"}}`)
	case r.Method == http.MethodPost && r.URL.Path == "/tasks":
		w.next++
		gid := fmt.Sprintf("new%d", w.next)
		st := &serverTask{Created: time.Now().UTC()}
		st.Name, _ = body.Data["name"].(string)
		st.Completed, _ = body.Data["completed"].(bool)
		if ps, ok := body.Data["projects"].([]any); ok && len(ps) > 0 {
			st.Project, _ = ps[0].(string)
		}
		w.tasks[gid] = st
		fmt.Fprintf(rw, `{"data":%s}`, w.taskJSON(gid))
	case len(parts) == 3 && parts[0] == "tasks" && (parts[2] == "addFollowers" || parts[2] == "removeFollowers"):
		if _, ok := w.tasks[parts[1]]; !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"task not found"}]}`)
			return
		}
		fmt.Fprintf(rw, `{"data":%s}`, w.taskJSON(parts[1]))
	case r.Method == http.MethodPost && r.URL.Path == "/attachments":
		w.next++
		parent, name := "", "f.txt"
		if m := regexp.MustCompile(`name="parent"\r\n\r\n(\S+)`).FindSubmatch(b); m != nil {
			parent = string(m[1])
		}
		if m := regexp.MustCompile(`filename="([^"]+)"`).FindSubmatch(b); m != nil {
			name = string(m[1])
		}
		js := fmt.Sprintf(`{"gid":"att%d","name":%q,"view_url":"https://x/att%d","created_at":%q}`, w.next, name, w.next, time.Now().UTC().Format(time.RFC3339))
		if w.atts == nil {
			w.atts = map[string][]string{}
		}
		w.atts[parent] = append(w.atts[parent], js)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/status_updates" && r.Method == http.MethodPost:
		w.next++
		parent, _ := body.Data["parent"].(string)
		js := fmt.Sprintf(`{"gid":"su%d","status_type":%q,"title":%q,"text":%q,"author":{"gid":"me"},"created_at":%q}`,
			w.next, body.Data["status_type"], body.Data["title"], body.Data["text"], time.Now().UTC().Format(time.RFC3339))
		if w.statuses == nil {
			w.statuses = map[string][]string{}
		}
		w.statuses[parent] = append(w.statuses[parent], js)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/status_updates":
		fmt.Fprintf(rw, `{"data":[%s]}`, strings.Join(w.statuses[r.URL.Query().Get("parent")], ","))
	case r.URL.Path == "/tags" && r.Method == http.MethodPost:
		w.next++
		js := fmt.Sprintf(`{"gid":"tag%d","name":%q}`, w.next, body.Data["name"])
		w.tags = append(w.tags, js)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/tags":
		fmt.Fprintf(rw, `{"data":[%s]}`, strings.Join(w.tags, ","))
	case len(parts) == 3 && parts[0] == "tasks" && (parts[2] == "addTag" || parts[2] == "removeTag"):
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "duplicate":
		src, ok := w.tasks[parts[1]]
		if !ok {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		w.next++
		gid := fmt.Sprintf("dup%d", w.next)
		name, _ := body.Data["name"].(string)
		w.tasks[gid] = &serverTask{Name: name, Project: src.Project, Section: src.Section, Created: time.Now().UTC()}
		fmt.Fprintf(rw, `{"data":{"gid":"job%d","new_task":{"gid":%q,"name":%q}}}`, w.next, gid, name)
	case r.Method == http.MethodGet && r.URL.Path == "/attachments":
		fmt.Fprintf(rw, `{"data":[%s]}`, strings.Join(w.atts[r.URL.Query().Get("parent")], ","))
	case r.Method == http.MethodGet && r.URL.Path == "/tasks":
		var list []string
		for gid, st := range w.tasks {
			if st.Project == r.URL.Query().Get("project") {
				list = append(list, w.taskJSON(gid))
			}
		}
		fmt.Fprintf(rw, `{"data":[%s]}`, strings.Join(list, ","))
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "subtasks" && r.Method == http.MethodPost:
		if _, ok := w.tasks[parts[1]]; !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"parent not found"}]}`)
			return
		}
		w.next++
		gid := fmt.Sprintf("new%d", w.next)
		name, _ := body.Data["name"].(string)
		w.tasks[gid] = &serverTask{Name: name, Created: time.Now().UTC()}
		fmt.Fprintf(rw, `{"data":%s}`, w.taskJSON(gid))
	case len(parts) == 3 && parts[0] == "sections" && parts[2] == "addTask":
		if parts[1] == "gone" {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"section not found"}]}`)
			return
		}
		gid, _ := body.Data["task"].(string)
		w.tasks[gid].Section = parts[1]
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "stories":
		if _, ok := w.tasks[parts[1]]; !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"not found"}]}`)
			return
		}
		if r.Method == http.MethodGet {
			fmt.Fprintf(rw, `{"data":[%s]}`, strings.Join(w.stories[parts[1]], ","))
			return
		}
		w.next++
		text, _ := body.Data["text"].(string)
		if h, ok := body.Data["html_text"].(string); ok {
			fmt.Fprintf(rw, `{"data":%s}`, w.addRichStoryLocked(parts[1], h))
			return
		}
		fmt.Fprintf(rw, `{"data":%s}`, w.addStoryLocked(parts[1], text))
	case len(parts) == 2 && parts[0] == "tasks":
		st, ok := w.tasks[parts[1]]
		if !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"task not found"}]}`)
			return
		}
		if r.Method == http.MethodPut {
			for k, v := range body.Data {
				switch k {
				case "name":
					st.Name, _ = v.(string)
				case "completed":
					st.Completed, _ = v.(bool)
				case "due_on":
					st.DueOn, _ = v.(string)
				}
			}
		}
		fmt.Fprintf(rw, `{"data":%s}`, w.taskJSON(parts[1]))
	default:
		w.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		rw.WriteHeader(http.StatusTeapot)
	}
}

func setupPush(t *testing.T) (*writeServer, *httptest.Server, *Syncer, *store.Store) {
	t.Helper()
	ws := &writeServer{t: t, tasks: map[string]*serverTask{
		"t1": {Name: "A", Project: "p1", Section: "s1"},
	}}
	srv := httptest.NewServer(ws)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, store.KeyMeGID, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	return ws, srv, &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}, st
}

var now = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func pendingCount(t *testing.T, st *store.Store) (int, int, int) {
	t.Helper()
	p, c, f, err := st.OutboxCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p, c, f
}

func TestLocalUpdateMergesAndReverts(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetTask(ctx, "t1")
	if !d.Completed || !d.Pending {
		t.Fatalf("local change not applied: %+v", d.TaskSummary)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now); err != nil {
		t.Fatal(err)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || len(ops[0].Payload) != 2 {
		t.Fatalf("edits should merge into one entry: %+v", ops)
	}
	// Reverting both fields removes the entry.
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": false}, now)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "A"}, now)
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatalf("reverted edits should leave no pending entry, got %d", p)
	}
}

func TestPullDoesNotOverwritePendingChange(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	// A sync pulls the server's (old) version of the task.
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A (renamed on server)", Completed: false}}); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetTask(ctx, "t1")
	if !d.Completed {
		t.Fatal("pull overwrote the unsent completion")
	}
	if d.Name != "A (renamed on server)" {
		t.Fatalf("fields we did not change should follow the server, got %q", d.Name)
	}
}

func TestPushUpdate(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true, "due_on": "2026-11-20"}, now)
	ws.tasks["t1"].Notes = "edited on server" // a different field: not a conflict

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if !ws.tasks["t1"].Completed || ws.tasks["t1"].DueOn != "2026-11-20" {
		t.Fatalf("server not updated: %+v", ws.tasks["t1"])
	}
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatal("sent entry should be removed")
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Pending || d.Notes != "edited on server" {
		t.Fatalf("store should reflect the server after push: %+v", d)
	}
}

func TestPushConflictThenKeepLocal(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "C (local)"}, now)
	ws.tasks["t1"].Name = "B (server)"

	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 || res.Sent != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, c := range ws.calls {
		if strings.HasPrefix(c, "PUT") {
			t.Fatalf("conflicting change must not be sent: %v", ws.calls)
		}
	}
	ops, _ := st.ListOps(ctx)
	if ops[0].State != store.OpConflict || ops[0].Conflict["name"] != "B (server)" {
		t.Fatalf("op = %+v", ops[0])
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "C (local)" {
		t.Fatalf("local value should stay visible while in conflict, got %q", d.Name)
	}

	if err := st.ResolveKeepLocal(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push after resolve = %+v, %v", res, err)
	}
	if ws.tasks["t1"].Name != "C (local)" {
		t.Fatalf("server name = %q", ws.tasks["t1"].Name)
	}
}

func TestDiscardRestoresBase(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "C"}, now)
	ws.tasks["t1"].Name = "B"
	_, _ = s.Push(ctx)
	ops, _ := st.ListOps(ctx)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "A" || d.Pending {
		t.Fatalf("discard should restore the pre-edit value until the next sync: %+v", d.TaskSummary)
	}
}

func TestOfflineCreateCommentAndComplete(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", SectionGID: "s2", Name: "牛乳を買う", AssigneeGID: "me"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !store.IsLocalGID(gid) {
		t.Fatalf("gid = %q", gid)
	}
	if _, err := st.AddCommentLocal(ctx, gid, "低脂肪のやつ", store.UserRef{GID: "me", Name: "Me"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"completed": true}, now); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := pendingCount(t, st); p != 2 {
		t.Fatalf("completion should fold into the create: %d pending", p)
	}
	// Visible locally, with the pending comment.
	d, err := st.GetTask(ctx, gid)
	if err != nil || !d.Completed || len(d.Stories) != 1 || !d.Stories[0].Pending {
		t.Fatalf("local task %+v, %v", d, err)
	}

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v, %v (calls %v)", res, err, ws.calls)
	}
	created := ws.tasks["new1"]
	if created == nil || created.Name != "牛乳を買う" || !created.Completed || created.Section != "s2" {
		t.Fatalf("server task = %+v", created)
	}
	if !strings.Contains(ws.calls[len(ws.calls)-1], "/tasks/new1/stories") {
		t.Fatalf("comment should be posted to the real gid: %v", ws.calls)
	}
	if _, err := st.GetTask(ctx, gid); err == nil {
		t.Fatal("local gid should be replaced")
	}
	d, err = st.GetTask(ctx, "new1")
	if err != nil || d.Pending || len(d.Stories) != 1 || !strings.HasPrefix(d.Stories[0].GID, "story") {
		t.Fatalf("store after push %+v, %v", d, err)
	}
	if len(d.Memberships) != 1 || d.Memberships[0].SectionGID != "s2" {
		t.Fatalf("memberships %+v", d.Memberships)
	}
}

func TestPushOfflineKeepsQueue(t *testing.T) {
	_, srv, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	srv.Close() // network down

	res, err := s.Push(ctx)
	if err == nil || res.Remaining != 1 {
		t.Fatalf("expected offline error, got %+v, %v", res, err)
	}
	if p, _, f := pendingCount(t, st); p != 1 || f != 0 {
		t.Fatalf("entry should stay pending: p=%d f=%d", p, f)
	}
}

func TestPruneKeepsLocalTasks(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "offline"}, now)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	if n, err := st.PruneTasks(ctx, map[string]bool{}); err != nil || n != 0 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := st.GetTask(ctx, gid); err != nil {
		t.Fatal("offline task was pruned")
	}
}

func sectionOf(t *testing.T, st *store.Store, gid string) string {
	t.Helper()
	d, err := st.GetTask(context.Background(), gid)
	if err != nil || len(d.Memberships) == 0 {
		t.Fatalf("task %s: %+v %v", gid, d, err)
	}
	return d.Memberships[0].SectionGID
}

func TestMoveTask(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "Todo"}, {GID: "s2", Name: "Doing"}, {GID: "s3", Name: "Done"}}); err != nil {
		t.Fatal(err)
	}

	// Move and move back: nothing to send.
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s2", now)
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s1", now)
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatalf("moving back should cancel the move, %d pending", p)
	}

	// Two moves merge into one entry.
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s2", now)
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s3", now)
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Payload["section"] != "s3" || ops[0].Payload["section_name"] != "Done" {
		t.Fatalf("ops = %+v", ops)
	}
	// A pull with the old section does not undo the local move.
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	if got := sectionOf(t, st, "t1"); got != "s3" {
		t.Fatalf("pull undid the move: %s", got)
	}

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if ws.tasks["t1"].Section != "s3" {
		t.Fatalf("server section = %q (calls %v)", ws.tasks["t1"].Section, ws.calls)
	}
	if got := sectionOf(t, st, "t1"); got != "s3" {
		t.Fatalf("store section after push = %s", got)
	}

	// Discard restores the previous section.
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s1", now)
	ops, _ = st.ListOps(ctx)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := sectionOf(t, st, "t1"); got != "s3" {
		t.Fatalf("discard should restore s3, got %s", got)
	}

	// Errors: unknown section, task not in project.
	if err := st.MoveTaskLocal(ctx, "t1", "p1", "nope", now); err == nil {
		t.Error("unknown section should fail")
	}
}

func TestRenameSection(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "In Pregoress"}}); err != nil {
		t.Fatal(err)
	}
	_ = st.RenameSectionLocal(ctx, "s1", "In Progress", now)
	// A sync that brings the old name back keeps the unsent rename.
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "In Pregoress"}})
	if name, _ := st.SectionName(ctx, "s1"); name != "In Progress" {
		t.Fatalf("rename lost after sync: %q", name)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != store.OpRenameSection || ops[0].TaskName != "In Progress" {
		t.Fatalf("ops = %+v", ops)
	}
	_ = st.RenameSectionLocal(ctx, "s1", "In Pregoress", now) // back to the original
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatal("renaming back should cancel")
	}
	if err := st.RenameSectionLocal(ctx, "s1", "  ", now); err == nil {
		t.Fatal("empty name should fail")
	}
}

func TestPushRenameSection(t *testing.T) {
	ctx := context.Background()
	serverName := "In Pregoress"
	var put string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var body struct{ Data map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			put, serverName = body.Data["name"], body.Data["name"]
		}
		fmt.Fprintf(w, `{"data":{"gid":"s1","name":%q}}`, serverName)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st}
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "In Pregoress"}})

	_ = st.RenameSectionLocal(ctx, "s1", "In Progress", now)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || put != "In Progress" {
		t.Fatalf("push = %+v %v put=%q", res, err, put)
	}

	// Conflict: renamed on the server to something else meanwhile.
	_ = st.RenameSectionLocal(ctx, "s1", "Doing", now)
	serverName = "WIP"
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("expected conflict: %+v %v", res, err)
	}
}

func TestDownloadAttachment(t *testing.T) {
	ctx := context.Background()
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the offline syncer below uses the default cache dir
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":{"gid":"a1","name":"receipt.png","host":"asana","download_url":"https://files.example/a1?sig=x"}}`)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T"}})
	_ = st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "receipt.png"}, {GID: "a2", Name: "memo.pdf"}})
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	var downloaded []string
	s := &Syncer{API: api, Store: st, CacheDir: t.TempDir(), Download: func(_ context.Context, url string) ([]byte, error) {
		downloaded = append(downloaded, url)
		return []byte("PNGDATA"), nil
	}}

	path, err := s.DownloadAttachment(ctx, "t1", "a1")
	if err != nil || !strings.HasSuffix(path, "a1-receipt.png") {
		t.Fatalf("path %q, %v", path, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "PNGDATA" || len(downloaded) != 1 || downloaded[0] != "https://files.example/a1?sig=x" {
		t.Fatalf("download: %q %v", b, downloaded)
	}
	// Cached: no second API call or download, also when offline.
	offline := &Syncer{Store: st}
	if p2, err := offline.DownloadAttachment(ctx, "t1", "a1"); err != nil || p2 != path || calls != 1 {
		t.Fatalf("cached path %q %v calls=%d", p2, err, calls)
	}
	// A sync of the attachment list keeps the downloaded path.
	_ = st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "receipt.png"}})
	d, _ := st.GetTask(ctx, "t1")
	if len(d.Attachments) != 1 || d.Attachments[0].LocalPath != path || !d.Attachments[0].IsImage {
		t.Fatalf("attachments %+v", d.Attachments)
	}
	if _, err := offline.DownloadAttachment(ctx, "t1", "a2"); err == nil {
		t.Fatal("offline download of an uncached file should fail")
	}
}

func TestDownloadInlineImageByGID(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/attachments/1220507679870013" {
			t.Errorf("unexpected %s", r.URL)
		}
		fmt.Fprint(w, `{"data":{"gid":"1220507679870013","name":"image.png","host":"asana","download_url":"https://files.example/x","parent":{"gid":"t1"}}}`)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T"}})
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st, CacheDir: t.TempDir(),
		Download: func(context.Context, string) ([]byte, error) { return []byte("img"), nil }}
	path, err := s.DownloadAttachment(ctx, "t1", "1220507679870013")
	if err != nil || !strings.HasSuffix(path, "1220507679870013-image.png") {
		t.Fatalf("path %q %v", path, err)
	}
	d, _ := st.GetTask(ctx, "t1")
	if len(d.Attachments) != 1 || d.Attachments[0].LocalPath != path {
		t.Fatalf("inline image should be recorded as an attachment: %+v", d.Attachments)
	}
}

func TestFetchDetailsAttachmentsOptional(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/attachments":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":[{"message":"forbidden scope attachments:read"}]}`)
		case strings.HasSuffix(r.URL.Path, "/stories"):
			fmt.Fprint(w, `{"data":[{"gid":"c1","type":"comment","text":"hi"}]}`)
		case strings.HasSuffix(r.URL.Path, "/subtasks"):
			fmt.Fprint(w, `{"data":[]}`)
		default:
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"T"}}`)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	err = (&Syncer{API: api, Store: st}).FetchDetails(ctx, "t1")
	var attErr *AttachmentsError
	if !errors.As(err, &attErr) {
		t.Fatalf("err = %v", err)
	}
	if d, _ := st.GetTask(ctx, "t1"); len(d.Stories) != 1 || d.DetailsFetchedAt == "" {
		t.Fatalf("comments should still be stored: %+v", d)
	}
}

func TestPushSubtaskOfOfflineParent(t *testing.T) {
	ctx := context.Background()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tasks":
			fmt.Fprint(w, `{"data":{"gid":"P1","name":"親"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/P1/subtasks":
			fmt.Fprint(w, `{"data":{"gid":"C1","name":"子","parent":{"gid":"P1","name":"親"}}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1"}})
	parent, _ := st.CreateTaskLocal(ctx, store.NewTask{WorkspaceGID: "w1", Name: "親"}, now)
	child, err := st.CreateTaskLocal(ctx, store.NewTask{ParentGID: parent, Name: "子"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetTask(ctx, parent); d.NumSubtasks != 1 || len(d.Subtasks) != 1 || d.Subtasks[0].GID != child {
		t.Fatalf("local parent should list the subtask: %+v", d)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v %v (%v)", res, err, calls)
	}
	if !strings.Contains(calls[1], "POST /tasks/P1/subtasks") || strings.Contains(calls[1], "workspace") {
		t.Fatalf("subtask should be created under the real parent gid: %v", calls)
	}
	if d, err := st.GetTask(ctx, "C1"); err != nil || d.ParentGID != "P1" || d.ParentName != "親" {
		t.Fatalf("subtask after push: %+v %v", d, err)
	}
}

func TestPushRichNotesAndComment(t *testing.T) {
	ctx := context.Background()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(b))
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/stories"):
			fmt.Fprint(w, `{"data":{"gid":"s1","type":"comment","text":"hi","html_text":"<body><strong>hi</strong></body>"}}`)
		default:
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"T","notes":"old","html_notes":"<body>old</body>"}}`)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Notes: "old", HTMLNotes: "<body>old</body>"}})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"notes": "new", "html_notes": "<body><strong>new</strong></body>"}, now)
	if d, _ := st.GetTask(ctx, "t1"); d.HTMLNotes != "<body><strong>new</strong></body>" || d.Notes != "new" {
		t.Fatalf("local rich notes: %q / %q", d.HTMLNotes, d.Notes)
	}
	_, _ = st.AddRichCommentLocal(ctx, "t1", "hi", "<body><strong>hi</strong></body>", store.UserRef{GID: "me"}, now)
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if !strings.Contains(bodies[0], `"html_notes":"\u003cbody\u003e\u003cstrong\u003enew`) || strings.Contains(bodies[0], `"notes"`) {
		t.Fatalf("update should send html_notes only: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"html_text"`) || strings.Contains(bodies[1], `"text":"hi"`) {
		t.Fatalf("comment should send html_text: %s", bodies[1])
	}
}

func TestPushTags(t *testing.T) {
	ctx := context.Background()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b))
		if r.URL.Path == "/tags" {
			fmt.Fprint(w, `{"data":{"gid":"G9","name":"新タグ"}}`)
			return
		}
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Workspace: &asana.Ref{GID: "w1"}, Tags: []asana.Ref{{GID: "g1", Name: "旧"}}}})
	if _, err := st.AddTagLocal(ctx, "t1", "", "新タグ", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveTagLocal(ctx, "t1", "g1", now); err != nil {
		t.Fatal(err)
	}
	// A sync that still has the old tags keeps the local changes.
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Workspace: &asana.Ref{GID: "w1"}, Tags: []asana.Ref{{GID: "g1", Name: "旧"}}}})
	if d, _ := st.GetTask(ctx, "t1"); !slices.Equal(d.Tags, []string{"新タグ"}) {
		t.Fatalf("tags after pull = %v", d.Tags)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v %v (%v)", res, err, calls)
	}
	want := []string{"POST /tags", "POST /tasks/t1/addTag", "POST /tasks/t1/removeTag"}
	for i, w := range want {
		if !strings.HasPrefix(calls[i], w) {
			t.Fatalf("call %d = %q, want %q", i, calls[i], w)
		}
	}
	if !strings.Contains(calls[1], `"tag":"G9"`) {
		t.Fatalf("addTag should use the created tag's gid: %s", calls[1])
	}
	if tags, _ := st.TaskTags(ctx, "t1"); len(tags) != 1 || tags[0].GID != "G9" {
		t.Fatalf("local tag gid should be replaced: %+v", tags)
	}
}

func TestCachedFileNamesAreLinkSafe(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T"}})
	_ = st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "77", Name: "スクリーンショット 2026-11-13 15.53.03 (2).png"}})
	// A file cached by an older version, with spaces in its name.
	old := filepath.Join(dir, "77-スクリーンショット 2026-11-13 15.53.03 (2).png")
	_ = os.WriteFile(old, []byte("png"), 0o600)
	_ = st.SetAttachmentPath(ctx, "77", old)

	path, err := (&Syncer{Store: st, CacheDir: dir}).DownloadAttachment(ctx, "t1", "77")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "77-スクリーンショット_2026-11-13_15.53.03__2_.png"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("cached file should be renamed")
	}
	if a, _ := st.GetAttachment(ctx, "77"); a.LocalPath != path {
		t.Fatalf("stored path = %q", a.LocalPath)
	}
}

func TestPushTaskExtras(t *testing.T) {
	ctx := context.Background()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			body := string(b)
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
				body = "multipart:" + map[bool]string{true: "has-file", false: "no-file"}[strings.Contains(body, "見積.pdf") && strings.Contains(body, "PDF!")]
			}
			calls = append(calls, r.Method+" "+r.URL.Path+" "+body)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/duplicate"):
			fmt.Fprint(w, `{"data":{"gid":"job1","new_task":{"gid":"T2","name":"コピー"}}}`)
		case r.URL.Path == "/attachments":
			fmt.Fprint(w, `{"data":{"gid":"A9","name":"見積.pdf","view_url":"https://x/a9"}}`)
		case r.Method == http.MethodGet:
			fmt.Fprintf(w, `{"data":{"gid":%q,"name":"コピー"}}`, strings.TrimPrefix(r.URL.Path, "/tasks/"))
		default:
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"T","liked":true}}`)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p2", Name: "家計"}}, "me")
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Followers: []asana.Ref{}}})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetFollowerLocal(ctx, "t1", "u2", true, now))
	must(st.SetFollowerLocal(ctx, "t1", "u3", true, now))
	must(st.SetFollowerLocal(ctx, "t1", "u3", false, now)) // cancels the add
	must(st.SetProjectLocal(ctx, "t1", "p2", "", true, now))
	must(st.UpdateTaskLocal(ctx, "t1", map[string]any{"liked": true}, now))
	copyGID, err := st.DuplicateTaskLocal(ctx, "t1", "", now)
	must(err)
	file := filepath.Join(t.TempDir(), "見積.pdf")
	_ = os.WriteFile(file, []byte("PDF!"), 0o600)
	_, err = st.UploadAttachmentLocal(ctx, "t1", file, t.TempDir(), now)
	must(err)
	// A pull that does not know about the follower/project keeps them.
	must(st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Followers: []asana.Ref{}}}))
	if d, _ := st.GetTask(ctx, "t1"); len(d.Followers) != 1 || len(d.Memberships) != 1 || !d.Liked {
		t.Fatalf("local changes lost on pull: %+v", d)
	}

	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if err != nil || res.Sent != 5 || res.Failed != 0 {
		t.Fatalf("push = %+v %v\n%v", res, err, calls)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		`POST /tasks/t1/addFollowers {"data":{"followers":["u2"]}}`,
		`POST /tasks/t1/addProject {"data":{"project":"p2"}}`,
		`PUT /tasks/t1 {"data":{"liked":true}}`,
		`POST /tasks/t1/duplicate {"data":{"include":"notes,assignee,subtasks,attachments,tags,followers,projects,dates,parent","name":"T のコピー"}}`,
		`POST /attachments multipart:has-file`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing call %q in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "u3") {
		t.Fatal("cancelled follower must not be sent")
	}
	if _, err := st.GetTask(ctx, copyGID); err == nil {
		t.Fatal("local copy should be replaced by the real duplicate")
	}
	if d, _ := st.GetTask(ctx, "t1"); len(d.Attachments) != 1 || d.Attachments[0].GID != "A9" {
		t.Fatalf("uploaded attachment = %+v", d.Attachments)
	}
}

func TestProjectOperations(t *testing.T) {
	ctx := context.Background()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b))
		switch {
		case r.URL.Path == "/users/me": // Push checks the account first
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case r.URL.Path == "/projects" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"data":{"gid":"P9","name":"新規","workspace":{"gid":"w1"},"members":[{"gid":"me","name":"Me"}]}}`)
		case r.URL.Path == "/projects/P9/sections":
			fmt.Fprint(w, `{"data":[{"gid":"S1","name":"Untitled section"}]}`)
		case strings.HasSuffix(r.URL.Path, "/addMembers"):
			fmt.Fprint(w, `{"data":{"gid":"P9","name":"新規","workspace":{"gid":"w1"},"members":[{"gid":"me","name":"Me"},{"gid":"u2","name":"Partner"}]}}`)
		case r.URL.Path == "/status_updates" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"data":{"gid":"ST1","status_type":"on_track","title":"t","text":"x","created_at":"2026-11-13T00:00:00Z"}}`)
		case r.URL.Path == "/status_updates":
			fmt.Fprint(w, `{"data":[{"gid":"ST0","status_type":"off_track","title":"old","text":"y","author":{"gid":"me","name":"Me"},"created_at":"2026-11-01T00:00:00Z"}]}`)
		default:
			fmt.Fprint(w, `{"data":{"gid":"P9","name":"新規","archived":true}}`)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1"}})
	_ = st.SetSyncState(ctx, store.KeyMeGID, "me")
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st}

	p, err := s.CreateProject(ctx, "新規")
	if err != nil || p.GID != "P9" {
		t.Fatalf("create = %+v %v", p, err)
	}
	ps, _ := st.ListProjects(ctx, true)
	if len(ps) != 1 || ps[0].Name != "新規" || len(ps[0].Sections) != 1 {
		t.Fatalf("stored project = %+v", ps)
	}
	if err := s.SetProjectMembers(ctx, "P9", []string{"u2"}, nil); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.ProjectMembers(ctx, "P9"); len(m) != 2 {
		t.Fatalf("members = %+v", m)
	}
	if !strings.Contains(strings.Join(calls, "\n"), `POST /projects/P9/addMembers {"data":{"members":"u2"}}`) {
		t.Fatalf("addMembers body: %v", calls)
	}

	// Status updates: fetched list + a local one sent on push.
	if err := s.FetchStatusUpdates(ctx, "P9"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateStatusLocal(ctx, "P9", "on_track", "t", "x", "<body>x</body>", store.UserRef{Name: "Me"}, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	ups, _ := st.StatusUpdates(ctx, "P9")
	if len(ups) != 2 || ups[0].GID != "ST1" || ups[1].GID != "ST0" {
		t.Fatalf("statuses = %+v", ups)
	}
	if ps, _ := st.ListProjects(ctx, true); ps[0].Status != "on_track" {
		t.Fatalf("latest status = %q", ps[0].Status)
	}

	if err := s.SetProjectArchived(ctx, "P9", true); err != nil {
		t.Fatal(err)
	}
	if ps, _ := st.ListProjects(ctx, false); len(ps) != 0 {
		t.Fatal("archived project should leave the list")
	}
	if _, err := (&Syncer{Store: st}).CreateProject(ctx, "x"); err != ErrOffline {
		t.Fatalf("offline create = %v", err)
	}
}

func (w *writeServer) setHook(h func(rw http.ResponseWriter, r *http.Request) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hook = h
}

// countCalls counts recorded write calls starting with prefix ("POST /tasks ").
func (w *writeServer) countCalls(prefix string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestPushOfflineTaskWithFollowerAndAttachment(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "オフライン"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetFollowerLocal(ctx, gid, "u2", true, now); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(file, []byte("hello"), 0o600)
	if _, err := st.UploadAttachmentLocal(ctx, gid, file, t.TempDir(), now); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceStories(ctx, gid, nil, now); err != nil { // a task_details row for the local task
		t.Fatal(err)
	}

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 3 || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
	}
	// A second push (the next sync) must not create the task again.
	if res, err := s.Push(ctx); err != nil || res.Sent != 0 {
		t.Fatalf("second push = %+v %v", res, err)
	}
	if n := ws.countCalls("POST /tasks "); n != 1 {
		t.Fatalf("task created %d times:\n%v", n, ws.calls)
	}
	real, _ := st.RealGID(ctx, gid)
	if store.IsLocalGID(real) {
		t.Fatalf("local gid not replaced: %s", real)
	}
	d, err := st.GetTask(ctx, real)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Followers) != 1 || d.Followers[0].GID != "u2" {
		t.Fatalf("followers after push = %+v", d.Followers)
	}
	if len(d.Attachments) != 1 || store.IsLocalGID(d.Attachments[0].GID) {
		t.Fatalf("attachments after push = %+v", d.Attachments)
	}
	if p, c, f := pendingCount(t, st); p+c+f != 0 {
		t.Fatalf("outbox not empty: %d/%d/%d", p, c, f)
	}
}

// dropConnection closes the connection without answering: the request reached the server
// but the client cannot tell what happened.
func dropConnection(rw http.ResponseWriter) {
	conn, _, err := rw.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	conn.Close()
}

func opByKind(t *testing.T, st *store.Store, kind string) *store.Op {
	t.Helper()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range ops {
		if ops[i].Kind == kind {
			return &ops[i]
		}
	}
	return nil
}

func TestPushSkipsWhileAnotherProcessPushes(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	// Another process (its own connection to the same database file) is pushing.
	other, err := store.Open(ctx, st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	unlock, err := other.TryLockPush()
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if !errors.Is(err, ErrPushBusy) || !res.Busy || res.Remaining != 1 || res.Sent != 0 {
		t.Fatalf("push while locked = %+v %v", res, err)
	}
	if len(ws.calls) != 0 {
		t.Fatalf("nothing should be sent while another process pushes: %v", ws.calls)
	}
	unlock()
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push after unlock = %+v %v", res, err)
	}
}

func TestPushNonNetworkErrorFailsEntryAndContinues(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	ws.tasks["t2"] = &serverTask{Name: "X"}
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "X"}})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "Y"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut && r.URL.Path == "/tasks/t1" {
			fmt.Fprint(rw, `<html>proxy error</html>`) // 200 but not JSON
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 1 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if ws.tasks["t2"].Name != "Y" {
		t.Fatal("the next entry should still be sent")
	}
	op := opByKind(t, st, store.OpUpdateTask)
	if op == nil || op.TaskGID != "t1" || op.State != store.OpFailed || !strings.Contains(op.LastError, "decode") {
		t.Fatalf("failed entry = %+v", op)
	}
}

func TestPushCreateRetriesFailedSectionMove(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", SectionGID: "s2", Name: "セクションへ"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/addTask") {
			rw.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(rw, `{"errors":[{"message":"boom"}]}`)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 || res.Remaining != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	real, _ := st.RealGID(ctx, gid)
	op := opByKind(t, st, store.OpMoveTask)
	if store.IsLocalGID(real) || op == nil || op.State != store.OpPending || op.TaskGID != real || op.Payload["section"] != "s2" {
		t.Fatalf("the move should stay queued for the created task %s: %+v", real, op)
	}
	if opByKind(t, st, store.OpCreateTask) != nil {
		t.Fatal("the create itself is done")
	}
	if got := sectionOf(t, st, real); got != "s2" {
		t.Fatalf("local section = %q, want the unsent s2", got)
	}
	ws.setHook(nil) // the next push sends the move by itself
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("next push = %+v %v", res, err)
	}
	if ws.countCalls("POST /tasks ") != 1 || ws.tasks[real].Section != "s2" {
		t.Fatalf("task should be created once and moved: %v", ws.calls)
	}
}
func TestPushRateLimitedKeepsQueue(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut {
			rw.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err == nil || res.Remaining != 1 || res.Failed != 0 {
		t.Fatalf("rate limited push = %+v %v", res, err)
	}
	if op := opByKind(t, st, store.OpUpdateTask); op == nil || op.State != store.OpPending {
		t.Fatalf("a rate-limited entry should stay queued: %+v", op)
	}
}

func TestPushServerAlreadyHasLocalValue(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	ws.tasks["t1"].Name = "B" // someone made the same change on the server
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("the same value on both sides is not a conflict: %+v %v", res, err)
	}
}

func TestPushClearsDueDateAndAssignee(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	ws.tasks["t1"].DueOn = "2026-11-20"
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", DueOn: "2026-11-20", Assignee: &asana.Ref{GID: "me"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"due_on": "", "assignee": ""}, now)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	put := ""
	for _, c := range ws.calls {
		if strings.HasPrefix(c, "PUT /tasks/t1 ") {
			put = c
		}
	}
	if !strings.Contains(put, `"due_on":null`) || !strings.Contains(put, `"assignee":null`) {
		t.Fatalf("clearing must send null: %q", put)
	}
}

// beginSendAll marks every pending entry "sending", as a process that died mid-push leaves them.
func beginSendAll(t *testing.T, st *store.Store) {
	t.Helper()
	ops, _ := st.PendingOps(context.Background())
	for _, op := range ops {
		if ok, err := st.BeginSend(context.Background(), op.ID, time.Now()); !ok || err != nil {
			t.Fatalf("begin send %d: %v %v", op.ID, ok, err)
		}
	}
}

func assertOutboxEmpty(t *testing.T, st *store.Store) {
	t.Helper()
	if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
		t.Fatalf("outbox should be empty: %+v", ops)
	}
}

// --- tasks ---

func TestPushCreate5xxButCreatedIsNotResent(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "届いていた"}, now)
	ws.setFailAfter("POST /tasks", 1)
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
	}
	if n := ws.countCalls("POST /tasks "); n != 1 {
		t.Fatalf("a create Asana already has must not be sent again (sent %d times)", n)
	}
	real, _ := st.RealGID(ctx, gid)
	if _, ok := ws.tasks[real]; !ok || store.IsLocalGID(real) {
		t.Fatalf("local task should become the one Asana created: %s", real)
	}
	assertOutboxEmpty(t, st)
}

func TestPushCreateVerifyOfflineRetriesOnNextPush(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "確認できない"}, now)
	ws.setFailAfter("POST /tasks", 1) // created, but the answer is lost
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/tasks" {
			dropConnection(rw) // and we cannot look it up either
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err == nil || res.Remaining != 1 || res.Failed != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	op := opByKind(t, st, store.OpCreateTask)
	if op == nil || op.State != store.OpPending || !op.Verify || op.SentAt == "" {
		t.Fatalf("entry should stay queued to be verified: %+v", op)
	}
	// The next sync, an hour later, still cannot look it up; the one after that can.
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	if res, err := s.Push(ctx); err == nil || res.Remaining != 1 {
		t.Fatalf("second push = %+v %v", res, err)
	}
	ws.setHook(nil)
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	res, err = s.Push(ctx)
	if err != nil || res.Sent != 1 || ws.countCalls("POST /tasks ") != 1 {
		t.Fatalf("next push should find the task (created since the first send) instead of creating it again: %+v %v\n%v", res, err, ws.calls)
	}
	assertOutboxEmpty(t, st)
}

func TestPushConnectionDroppedDuringCreate(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "届いたかも"}, now)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/tasks" {
			dropConnection(rw)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err == nil || res.Remaining != 2 {
		t.Fatalf("a dropped connection should stop the push with both entries queued: %+v %v", res, err)
	}
	if op := opByKind(t, st, store.OpCreateTask); op == nil || op.State != store.OpPending || !op.Verify {
		t.Fatalf("the create should be verified before it is sent again: %+v", op)
	}
	ws.setHook(nil)
	later(s, 2*time.Minute)
	if res, err := s.Push(ctx); err != nil || res.Sent != 2 || ws.countCalls("POST /tasks ") != 2 {
		t.Fatalf("not in Asana: sent once more = %+v %v\n%v", res, err, ws.calls)
	}
	n := 0
	for _, st := range ws.tasks {
		if st.Name == "届いたかも" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Asana should have the task once, has %d", n)
	}
}

func TestPushRecoversInterruptedCreate(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(fmt.Sprint("created=", created), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "作成途中"}, now)
			_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
			beginSendAll(t, st) // a previous process died while sending both
			if created {
				ws.addTask("srv1", "作成途中", "p1")
			}
			later(s, 2*time.Minute) // the app is started again some time later
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 2 || res.Failed != 0 {
				t.Fatalf("push = %+v %v", res, err)
			}
			wantPosts := map[bool]int{true: 0, false: 1}[created]
			if n := ws.countCalls("POST /tasks "); n != wantPosts {
				t.Fatalf("creates sent = %d, want %d", n, wantPosts)
			}
			if ws.tasks["t1"].Name != "B" {
				t.Fatal("an interrupted update is harmless to resend and should be sent")
			}
			if real, _ := st.RealGID(ctx, gid); created && real != "srv1" {
				t.Fatalf("local task should become srv1, got %s", real)
			}
			assertOutboxEmpty(t, st)
		})
	}
}

func TestPushCreateAmbiguousAsksUser(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "同名"}, now)
	beginSendAll(t, st)
	ws.addTask("srv1", "同名", "p1")
	ws.addTask("srv2", "同名", "p1")
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 || ws.countCalls("POST /tasks ") != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if op := opByKind(t, st, store.OpCreateTask); op == nil || op.State != store.OpFailed || op.LastError != store.AmbiguousMessage {
		t.Fatalf("two candidates: the user decides: %+v", op)
	}
}

// --- comments ---

func TestPushComment5xx(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(fmt.Sprint("created=", created), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			_, _ = st.AddCommentLocal(ctx, "t1", "こんにちは", store.UserRef{GID: "me"}, now)
			if created {
				ws.setFailAfter("POST /tasks/t1/stories", 1)
			} else {
				failed := false
				ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
					if r.Method == http.MethodPost && r.URL.Path == "/tasks/t1/stories" && !failed {
						failed = true
						rw.WriteHeader(http.StatusBadGateway)
						return true
					}
					return false
				})
			}
			if res := pushAndSettle(t, s); res.Sent != 1 || res.Failed != 0 {
				t.Fatalf("push = %+v", res)
			}
			if n := len(ws.stories["t1"]); n != 1 {
				t.Fatalf("Asana should have exactly one comment, has %d", n)
			}
			if d, _ := st.GetTask(ctx, "t1"); len(d.Stories) != 1 || store.IsLocalGID(d.Stories[0].GID) {
				t.Fatalf("local comment should be replaced: %+v", d.Stories)
			}
			assertOutboxEmpty(t, st)
		})
	}
}

func TestPushRecoversInterruptedComment(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.AddCommentLocal(ctx, "t1", "途中で落ちた", store.UserRef{GID: "me"}, now)
	beginSendAll(t, st)
	ws.addComment("t1", "途中で落ちた")
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || ws.countCalls("POST /tasks/t1/stories") != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	assertOutboxEmpty(t, st)
}

func TestPushCommentVerifyOfflineRetriesOnNextPush(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.AddCommentLocal(ctx, "t1", "確認できない", store.UserRef{GID: "me"}, now)
	ws.setFailAfter("POST /tasks/t1/stories", 1)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/tasks/t1/stories" {
			dropConnection(rw)
			return true
		}
		return false
	})
	if res, err := s.Push(ctx); err == nil || res.Remaining != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	ws.setHook(nil)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || len(ws.stories["t1"]) != 1 {
		t.Fatalf("next push = %+v %v (comments %d)", res, err, len(ws.stories["t1"]))
	}
}

// --- attachments ---

func TestPushUpload5xxButUploadedIsNotResent(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "見積.pdf")
	_ = os.WriteFile(file, []byte("PDF"), 0o600)
	if _, err := st.UploadAttachmentLocal(ctx, "t1", file, t.TempDir(), now); err != nil {
		t.Fatal(err)
	}
	ws.setFailAfter("POST /attachments", 1)
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || ws.countCalls("POST /attachments") != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if d, _ := st.GetTask(ctx, "t1"); len(d.Attachments) != 1 || store.IsLocalGID(d.Attachments[0].GID) {
		t.Fatalf("attachments = %+v", d.Attachments)
	}
	assertOutboxEmpty(t, st)
}

// --- tasks created offline wait for their parent ---

func TestPushChildWaitsForFailedParentCreate(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	parent, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "親"}, now)
	child, err := st.CreateTaskLocal(ctx, store.NewTask{ParentGID: parent, Name: "子"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.AddCommentLocal(ctx, parent, "メモ", store.UserRef{GID: "me"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/tasks" {
			rw.WriteHeader(http.StatusForbidden)
			fmt.Fprint(rw, `{"errors":[{"message":"forbidden"}]}`)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 || res.Sent != 0 || res.Remaining != 2 {
		t.Fatalf("only the parent fails; its subtask and comment wait: %+v %v\n%v", res, err, ws.calls)
	}
	ops, _ := st.ListOps(ctx)
	for _, op := range ops {
		if want := map[bool]string{true: store.OpFailed, false: store.OpPending}[op.Kind == store.OpCreateTask && op.TaskGID == parent]; op.State != want {
			t.Fatalf("entry %+v: state %s, want %s", op, op.State, want)
		}
	}
	if n := ws.countCalls("POST /tasks/"); n != 0 {
		t.Fatalf("nothing may be sent for a task that does not exist yet: %v", ws.calls)
	}
	// The user retries the parent: everything goes out.
	ws.setHook(nil)
	parentOp := opByKind(t, st, store.OpCreateTask)
	if err := st.RetryOp(ctx, parentOp.ID); err != nil {
		t.Fatal(err)
	}
	ws.mu.Lock()
	ws.hook = func(rw http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/subtasks") && r.Method == http.MethodPost {
			ws.next++
			gid := fmt.Sprintf("sub%d", ws.next)
			ws.tasks[gid] = &serverTask{Name: "子", Created: time.Now().UTC()}
			fmt.Fprintf(rw, `{"data":%s}`, ws.taskJSON(gid))
			return true
		}
		return false
	}
	ws.mu.Unlock()
	if res, err := s.Push(ctx); err != nil || res.Sent != 3 {
		t.Fatalf("after retry = %+v %v\n%v", res, err, ws.calls)
	}
	if real, _ := st.RealGID(ctx, child); store.IsLocalGID(real) {
		t.Fatal("subtask should be created after its parent")
	}
	assertOutboxEmpty(t, st)
}

func TestCreateProject5xx(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(fmt.Sprint("created=", created), func(t *testing.T) {
			ctx := context.Background()
			var posts int
			var projects []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/projects" && r.Method == http.MethodPost:
					posts++
					p := fmt.Sprintf(`{"gid":"P%d","name":"新規","workspace":{"gid":"w1"},"created_at":%q}`, posts, time.Now().UTC().Format(time.RFC3339))
					if posts == 1 {
						if created {
							projects = append(projects, p)
						}
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					projects = append(projects, p)
					fmt.Fprintf(w, `{"data":%s}`, p)
				case r.URL.Path == "/projects":
					old := `{"gid":"OLD","name":"新規","created_at":"2020-01-01T00:00:00Z"}` // same name, long ago
					fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(append([]string{old}, projects...), ","))
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
			waited := 0
			s := &Syncer{API: api, Store: st, sleep: func(context.Context, time.Duration) error { waited++; return nil }}
			p, err := s.CreateProject(ctx, "新規")
			if posts != 1 {
				t.Fatalf("a project that may exist must not be created again (posts = %d)", posts)
			}
			if created && (err != nil || p.GID != "P1" || waited != 0) {
				t.Fatalf("project = %+v %v (waited %d)", p, err, waited)
			}
			if !created && (err == nil || waited != 1) {
				t.Fatalf("not found after waiting: error, no resend; got %+v %v (waited %d)", p, err, waited)
			}
		})
	}
}

func TestPushEditsWaitForFailedCreate(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "A"}, {GID: "s2", Name: "B"}})
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "新規"}, now)
	create := opByKind(t, st, store.OpCreateTask)
	if ok, err := st.BeginSend(ctx, create.ID, now); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := st.MarkOpFailed(ctx, create.ID, errors.New("asana: 403")); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"name": "名前変更"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveTaskLocal(ctx, gid, "p1", "s2", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 0 || res.Remaining != 2 {
		t.Fatalf("edits of a task whose create failed should wait: %+v %v", res, err)
	}
	for _, kind := range []string{store.OpUpdateTask, store.OpMoveTask} {
		if op := opByKind(t, st, kind); op == nil || op.State != store.OpPending {
			t.Fatalf("%s should stay pending: %+v", kind, op)
		}
	}
	// A pull keeps the waiting edit (failed entries would not be reapplied).
	if d, _ := st.GetTask(ctx, gid); d.Name != "名前変更" {
		t.Fatalf("local name = %q", d.Name)
	}
	// The create is retried and goes through: the edits follow with the real gid.
	if err := st.RetryOp(ctx, create.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 3 || res.Failed != 0 {
		t.Fatalf("after retry = %+v %v\n%v", res, err, ws.calls)
	}
	real, _ := st.RealGID(ctx, gid)
	if st := ws.tasks[real]; st == nil || st.Name != "名前変更" || st.Section != "s2" {
		t.Fatalf("server task = %+v", st)
	}
	assertOutboxEmpty(t, st)
}

// failOnce makes the first request matching method+path answer 500; if applied, the
// request takes effect first (its answer is lost), otherwise it is not applied at all.
func failOnce(ws *writeServer, method, path string, applied bool) {
	if applied {
		ws.setFailAfter(method+" "+path, 1)
		return
	}
	failed := false
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == method && r.URL.Path == path && !failed {
			failed = true
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return false
	})
}

func TestPushStatusUpdate5xx(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(fmt.Sprint("created=", created), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			local, err := st.CreateStatusLocal(ctx, "p1", "on_track", "順調", "本文", "", store.UserRef{Name: "Me"}, now)
			if err != nil {
				t.Fatal(err)
			}
			failOnce(ws, http.MethodPost, "/status_updates", created)
			if res := pushAndSettle(t, s); res.Sent != 1 || res.Failed != 0 {
				t.Fatalf("push = %+v", res)
			}
			if n := len(ws.statuses["p1"]); n != 1 {
				t.Fatalf("Asana should have the status update once, has %d", n)
			}
			ups, _ := st.StatusUpdates(ctx, "p1")
			if len(ups) != 1 || ups[0].GID == local || store.IsLocalGID(ups[0].GID) {
				t.Fatalf("local status should be replaced: %+v", ups)
			}
			assertOutboxEmpty(t, st)
		})
	}
}

func TestPushDuplicate5xx(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(fmt.Sprint("created=", created), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			copyGID, err := st.DuplicateTaskLocal(ctx, "t1", "コピー", now)
			if err != nil {
				t.Fatal(err)
			}
			failOnce(ws, http.MethodPost, "/tasks/t1/duplicate", created)
			if !created { // a copy is made in the background: not finding it for minutes is normal
				later(s, 3*time.Minute)
				if res, err := s.Push(ctx); err != nil || res.Sent != 0 || ws.countCalls("POST /tasks/t1/duplicate") != 1 {
					t.Fatalf("a duplicate is not resent within its settle time: %+v %v", res, err)
				}
			}
			if res := pushAndSettle(t, s); res.Sent != 1 || res.Failed != 0 {
				t.Fatalf("push = %+v\n%v", res, ws.calls)
			}
			var copies []string
			for gid, task := range ws.tasks {
				if task.Name == "コピー" {
					copies = append(copies, gid)
				}
			}
			if len(copies) != 1 {
				t.Fatalf("Asana should have one copy, has %v", copies)
			}
			if real, _ := st.RealGID(ctx, copyGID); real != copies[0] {
				t.Fatalf("local copy should become %s, got %s", copies[0], real)
			}
			assertOutboxEmpty(t, st)
		})
	}
}

func TestPushNewTag5xx(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(fmt.Sprint("created=", created), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			if _, err := st.AddTagLocal(ctx, "t1", "", "新タグ", now); err != nil {
				t.Fatal(err)
			}
			failOnce(ws, http.MethodPost, "/tags", created)
			if res := pushAndSettle(t, s); res.Sent != 1 || res.Failed != 0 {
				t.Fatalf("push = %+v\n%v", res, ws.calls)
			}
			if len(ws.tags) != 1 {
				t.Fatalf("Asana should have the tag once: %v", ws.tags)
			}
			if tags, _ := st.TaskTags(ctx, "t1"); len(tags) != 1 || store.IsLocalGID(tags[0].GID) {
				t.Fatalf("task tags = %+v", tags)
			}
			if n := ws.countCalls("POST /tasks/t1/addTag"); n != 1 {
				t.Fatalf("addTag sent %d times", n)
			}
			assertOutboxEmpty(t, st)
		})
	}
}

// pushQuickly runs Push with a short deadline, so a loop that never ends fails the test at
// once instead of hanging until go test's timeout.
func pushQuickly(t *testing.T, s *Syncer) (PushResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.Push(ctx)
	if ctx.Err() != nil {
		t.Fatalf("Push did not finish (loop?): %+v %v", res, err)
	}
	return res, err
}

// Asana keeps failing and never has the result: the first push sends the entry twice
// (send, check, send again), each later push once (check, send again); in between it waits.
func later(s *Syncer, d time.Duration) { s.now = func() time.Time { return time.Now().Add(d) } }

// pushAndSettle pushes, and if something is left waiting for Asana, pushes again once
// the wait is over. It returns the combined result.
func pushAndSettle(t *testing.T, s *Syncer) PushResult {
	t.Helper()
	res, err := s.Push(context.Background())
	if err != nil {
		t.Fatalf("push = %+v %v", res, err)
	}
	if res.Remaining > 0 {
		later(s, 10*time.Minute)
		res2, err := s.Push(context.Background())
		if err != nil {
			t.Fatalf("second push = %+v %v", res2, err)
		}
		res.Sent += res2.Sent
		res.Failed += res2.Failed
		res.Remaining = res2.Remaining
	}
	return res
}

// Right after a 5xx Asana may not list the task yet: not finding it then is not trusted.
// Once settleTime has passed, not finding it means it was not created: sent once more.
func TestPushCreate5xxNotCreatedWaitsThenResendsOnce(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "届いていない"}, now)
	failOnce(ws, http.MethodPost, "/tasks", false)
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 0 || res.Failed != 0 || res.Remaining != 1 || ws.countCalls("POST /tasks ") != 1 {
		t.Fatalf("first push must not resend right away: %+v %v\n%v", res, err, ws.calls)
	}
	if op := opByKind(t, st, store.OpCreateTask); op == nil || op.State != store.OpPending || !op.Verify {
		t.Fatalf("entry should wait to be verified: %+v", op)
	}
	later(s, 30*time.Second) // still too early
	if res, err := s.Push(ctx); err != nil || res.Sent != 0 || ws.countCalls("POST /tasks ") != 1 {
		t.Fatalf("push within settleTime = %+v %v", res, err)
	}
	later(s, 2*time.Minute)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || ws.countCalls("POST /tasks ") != 2 {
		t.Fatalf("after settleTime it is sent exactly once more: %+v %v\n%v", res, err, ws.calls)
	}
	if real, _ := st.RealGID(ctx, gid); store.IsLocalGID(real) {
		t.Fatal("task not created")
	}
	assertOutboxEmpty(t, st)
}

// Asana keeps failing: one push sends an entry at most twice, and after maxSendAttempts
// requests (over several pushes) it is marked failed for the user instead of retrying forever.
func TestPushGivesUpAfterMaxSendAttempts(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		queue      func(t *testing.T, st *store.Store)
	}{
		{"create", "/tasks", func(t *testing.T, st *store.Store) {
			_, _ = st.CreateTaskLocal(context.Background(), store.NewTask{ProjectGID: "p1", Name: "x"}, now)
		}},
		{"comment", "/tasks/t1/stories", func(t *testing.T, st *store.Store) {
			_, _ = st.AddCommentLocal(context.Background(), "t1", "x", store.UserRef{GID: "me"}, now)
		}},
		{"update", "/tasks/t1", func(t *testing.T, st *store.Store) {
			_ = st.UpdateTaskLocal(context.Background(), "t1", map[string]any{"name": "B"}, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			tc.queue(t, st)
			ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodGet && r.URL.Path == tc.path {
					rw.WriteHeader(http.StatusServiceUnavailable)
					return true
				}
				return false
			})
			failedAt := 0
			for push := 1; push <= 10 && failedAt == 0; push++ {
				before := len(ws.calls)
				res, err := pushQuickly(t, s)
				if err != nil {
					t.Fatalf("push %d: %v", push, err)
				}
				if n := len(ws.calls) - before; n > 2 {
					t.Fatalf("push %d sent %d requests, want at most 2", push, n)
				}
				if res.Failed == 1 {
					failedAt = push
				}
				later(s, time.Duration(push)*2*time.Minute)
			}
			if failedAt == 0 || len(ws.calls) != maxSendAttempts {
				t.Fatalf("failed at push %d after %d requests, want failure after %d", failedAt, len(ws.calls), maxSendAttempts)
			}
			for _, op := range must1(st.ListOps(context.Background())) {
				if op.State != store.OpFailed || !strings.Contains(op.LastError, "回送っても") {
					t.Fatalf("entry = %+v", op)
				}
			}
		})
	}
}

func must1[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Two creates with the same name: the one whose answer was lost must not be taken for the
// other's task (which is already recorded as the other's result).
func TestPushSameNameCreatesAreNotMerged(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	a, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "同じ名前"}, now)
	b, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "同じ名前"}, now)
	failOnce(ws, http.MethodPost, "/tasks", false) // a's create is not applied; b's is
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("first push = %+v %v", res, err)
	}
	later(s, 2*time.Minute)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("second push = %+v %v\n%v", res, err, ws.calls)
	}
	realA, _ := st.RealGID(ctx, a)
	realB, _ := st.RealGID(ctx, b)
	if store.IsLocalGID(realA) || store.IsLocalGID(realB) || realA == realB {
		t.Fatalf("each create needs its own task: a=%s b=%s", realA, realB)
	}
	for _, gid := range []string{realA, realB} {
		if d, err := st.GetTask(ctx, gid); err != nil || len(d.Memberships) != 1 {
			t.Fatalf("task %s = %+v %v", gid, d, err)
		}
	}
	if n := ws.countCalls("POST /tasks "); n != 3 {
		t.Fatalf("POST /tasks sent %d times, want 3 (a, b, a again)", n)
	}
	assertOutboxEmpty(t, st)
}

// A task made by hand in Asana with the same name (but another due date) is not ours.
func TestPushCreateIgnoresSameNameWithOtherFields(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "買い物", DueOn: "2026-11-20"}, now)
	beginSendAll(t, st)
	ws.addTask("hand", "買い物", "p1") // no due date
	later(s, 2*time.Minute)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || ws.countCalls("POST /tasks ") != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
}

// If the check itself is refused (the task is gone), the entry fails instead of waiting forever.
func TestPushVerifyRejectedFails(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.AddCommentLocal(ctx, "t1", "x", store.UserRef{GID: "me"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/tasks/t1/stories" {
			if r.Method == http.MethodPost {
				rw.WriteHeader(http.StatusBadGateway)
			} else {
				rw.WriteHeader(http.StatusForbidden)
				fmt.Fprint(rw, `{"errors":[{"message":"forbidden"}]}`)
			}
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if op := opByKind(t, st, store.OpAddComment); op == nil || op.State != store.OpFailed || !strings.Contains(op.LastError, "403") {
		t.Fatalf("entry = %+v", op)
	}
}

// A rich-text comment (link, mention, list) is found although Asana's plain text differs
// from ours.
func TestPushRichComment5xxButCreated(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	md := "見て [資料](https://example.com/doc) と [@Partner](asana:user:u2)\n\n- 一つ目\n- 二つ目"
	_, err := st.AddRichCommentLocal(ctx, "t1", richtext.PlainFromMarkdown(md), richtext.FromMarkdown(md), store.UserRef{GID: "me"}, now)
	if err != nil {
		t.Fatal(err)
	}
	ws.setFailAfter("POST /tasks/t1/stories", 1)
	if res := pushAndSettle(t, s); res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("push = %+v", res)
	}
	if n := len(ws.stories["t1"]); n != 1 {
		t.Fatalf("Asana should have the comment once, has %d:\n%v", n, ws.stories["t1"])
	}
	assertOutboxEmpty(t, st)
}

// A create found in Asana is moved to its section in the same push.
func TestPushFoundCreateMovesToSectionAtOnce(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "A"}, {GID: "s2", Name: "B"}})
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", SectionGID: "s2", Name: "移動も"}, now)
	ws.setFailAfter("POST /tasks", 1)
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
	}
	real, _ := st.RealGID(ctx, gid)
	if ws.tasks[real] == nil || ws.tasks[real].Section != "s2" || ws.countCalls("POST /tasks ") != 1 {
		t.Fatalf("task should be found and moved to s2 in the same push: %v", ws.calls)
	}
}

// Without a send time there is no telling which tasks could be ours: the user decides.
func TestPushVerifyWithoutSentAtAsksUser(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "x"}, now)
	op := opByKind(t, st, store.OpCreateTask)
	if err := st.MarkOpVerify(ctx, op.ID, ""); err != nil { // verify, but never sent_at
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 || ws.countCalls("POST /tasks ") != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if op := opByKind(t, st, store.OpCreateTask); op.State != store.OpFailed || op.LastError != store.AmbiguousMessage {
		t.Fatalf("entry = %+v", op)
	}
}

// Two identical comments: the one whose create was lost is sent again, not matched to the other.
func TestPushSameTextCommentsAreNotMerged(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.AddCommentLocal(ctx, "t1", "了解", store.UserRef{GID: "me"}, now)
	_, _ = st.AddCommentLocal(ctx, "t1", "了解", store.UserRef{GID: "me"}, now)
	failOnce(ws, http.MethodPost, "/tasks/t1/stories", false)
	if res := pushAndSettle(t, s); res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v", res)
	}
	if n := len(ws.stories["t1"]); n != 2 {
		t.Fatalf("Asana should have both comments, has %d", n)
	}
	d, _ := st.GetTask(ctx, "t1")
	if len(d.Stories) != 2 || d.Stories[0].GID == d.Stories[1].GID {
		t.Fatalf("local comments = %+v", d.Stories)
	}
}

// Pushes while offline do not use up the attempts: Asana never saw those requests.
func TestPushOfflineDoesNotCountAttempts(t *testing.T) {
	ws, srv, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	online := s.API
	offline := asana.NewClient(srv.Client())
	offline.BaseURL = "http://127.0.0.1:1" // nothing listens: connection refused
	offline.MaxRetries = 0
	s.API = offline
	for i := 0; i < maxSendAttempts+2; i++ {
		if _, err := s.Push(ctx); err == nil {
			t.Fatal("offline push should fail")
		}
	}
	if op := opByKind(t, st, store.OpUpdateTask); op.Attempts != 0 {
		t.Fatalf("attempts = %d after offline pushes, want 0", op.Attempts)
	}
	s.API = online
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	if res, err := s.Push(ctx); err != nil || res.Failed != 0 {
		t.Fatalf("a first 5xx after being offline must not give up: %+v %v", res, err)
	}
}

// While a create waits to be verified, edits to the task are queued separately: the check
// looks for what was sent, and the edits follow once the task is found.
func TestPushEditWhileVerifyingDoesNotDuplicate(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "A"}, {GID: "s2", Name: "B"}})
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "元の名前"}, now)
	ws.setFailAfter("POST /tasks", 1) // created, but the answer is lost
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/tasks" {
			dropConnection(rw) // and it cannot be looked up yet
			return true
		}
		return false
	})
	if _, err := s.Push(ctx); err == nil {
		t.Fatal("first push should stop (offline check)")
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"name": "新しい名前", "due_on": "2026-12-01", "assignee": "u2"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveTaskLocal(ctx, gid, "p1", "s2", now); err != nil {
		t.Fatal(err)
	}
	if op := opByKind(t, st, store.OpCreateTask); op.Payload["name"] != "元の名前" || op.Payload["section"] != "s1" {
		t.Fatalf("the create being verified must keep what was sent: %+v", op.Payload)
	}
	ws.setHook(nil)
	later(s, 10*time.Minute)
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
	}
	if n := ws.countCalls("POST /tasks "); n != 1 {
		t.Fatalf("the task was created %d times", n)
	}
	real, _ := st.RealGID(ctx, gid)
	srvTask := ws.tasks[real]
	if srvTask == nil || srvTask.Name != "新しい名前" || srvTask.DueOn != "2026-12-01" || srvTask.Section != "s2" {
		t.Fatalf("server task = %+v", srvTask)
	}
	put := ""
	for _, c := range ws.calls {
		if strings.HasPrefix(c, "PUT /tasks/"+real+" ") {
			put = c
		}
	}
	if !strings.Contains(put, `"assignee":"u2"`) {
		t.Fatalf("assignee change not sent: %q", put)
	}
}

// After a resend that also got a 5xx, the wait starts again from that resend.
func TestPushSettleRestartsAfterResend(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "遅い"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/tasks" {
			rw.WriteHeader(http.StatusServiceUnavailable) // not applied (yet)
			return true
		}
		return false
	})
	posts := func() int { return ws.countCalls("POST /tasks ") }
	_, _ = s.Push(ctx) // send 1 -> 503; too early to resend
	later(s, 2*time.Minute)
	_, _ = s.Push(ctx) // not found -> send 2 -> 503
	if posts() != 2 {
		t.Fatalf("posts = %d after the second push", posts())
	}
	later(s, 2*time.Minute+10*time.Second) // 10 s after send 2
	_, _ = s.Push(ctx)
	if posts() != 2 {
		t.Fatalf("resent only 10 s after the previous 5xx (posts = %d)", posts())
	}
	later(s, 4*time.Minute) // a minute after send 2
	_, _ = s.Push(ctx)
	if posts() != 3 {
		t.Fatalf("posts = %d, want a third send once the wait is over", posts())
	}
}

// A comment of only emoji has no letters to compare: it must match exactly, not any other
// such comment.
func TestPushEmojiCommentMatchesExactly(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.AddCommentLocal(ctx, "t1", "👍", store.UserRef{GID: "me"}, now)
	beginSendAll(t, st)
	ws.addComment("t1", "🎉") // another emoji comment of ours, not this one
	later(s, 2*time.Minute)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || ws.countCalls("POST /tasks/t1/stories") != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if n := len(ws.stories["t1"]); n != 2 {
		t.Fatalf("both emoji comments should exist, have %d", n)
	}
}

// Retrying an entry that failed after maxSendAttempts gives it a fresh set of attempts.
func TestRetryResetsAttempts(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	for i := 0; i < 10; i++ {
		if res, _ := s.Push(ctx); res.Failed == 1 {
			break
		}
	}
	op := opByKind(t, st, store.OpUpdateTask)
	if op.State != store.OpFailed {
		t.Fatalf("entry = %+v", op)
	}
	if err := st.RetryOp(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Failed != 0 {
		t.Fatalf("one more 5xx after a retry must not fail at once: %+v %v", res, err)
	}
}

// --- changes to things deleted in Asana are dropped and recorded ---

func TestPushDropsChangesToDeletedTask(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "消されたタスク", Workspace: &asana.Ref{GID: "w1"}}})
	_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"completed": true}, now)
	_, _ = st.AddCommentLocal(ctx, "t2", "メモ", store.UserRef{GID: "me"}, now)
	sub, _ := st.CreateTaskLocal(ctx, store.NewTask{ParentGID: "t2", Name: "子"}, now)
	_, _ = st.AddCommentLocal(ctx, sub, "子へのメモ", store.UserRef{GID: "me"}, now)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now) // unrelated
	// t2 was deleted in Asana (the fake server never had it).

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 || res.Remaining != 0 || len(res.Discarded) != 4 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if ws.tasks["t1"].Name != "B" {
		t.Fatal("the unrelated change should be sent")
	}
	if n := ws.countCalls("POST /tasks/t2/"); n != 0 {
		t.Fatalf("nothing more should be sent for the deleted task's subtree: %v", ws.calls)
	}
	for _, gid := range []string{"t2", sub} {
		if _, err := st.GetTask(ctx, gid); err == nil {
			t.Fatalf("task %s should be removed locally", gid)
		}
	}
	assertOutboxEmpty(t, st)
	// The front ends show the dropped changes once.
	pending, err := st.PendingDiscards(ctx)
	if err != nil || len(pending) != 4 {
		t.Fatalf("pending discards = %+v %v", pending, err)
	}
	want := []string{"完了にする", "コメント「メモ」", "サブタスク「子」の作成", "コメント「子へのメモ」"}
	ids := []int64{}
	for i, d := range pending {
		if d.Summary != want[i] || !strings.Contains(d.Reason, "404") {
			t.Fatalf("discard %d = %+v, want summary %q", i, d, want[i])
		}
		ids = append(ids, d.ID)
	}
	if pending[0].TaskName != "消されたタスク" {
		t.Fatalf("task name = %q", pending[0].TaskName)
	}
	if err := st.MarkDiscardsNotified(ctx, ids); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.PendingDiscards(ctx); len(again) != 0 {
		t.Fatalf("shown discards should not be pending: %+v", again)
	}
}

// A 404 about something else than the task (here the section) drops only that change.
func TestPushDropsOnlyChangeWhoseTargetIsGone(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "A"}, {GID: "gone", Name: "消えたセクション"}})
	if err := st.MoveTaskLocal(ctx, "t1", "p1", "gone", now); err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || len(res.Discarded) != 1 || res.Discarded[0].Kind != store.OpMoveTask {
		t.Fatalf("push = %+v %v", res, err)
	}
	if ws.tasks["t1"].Name != "B" {
		t.Fatal("the task still exists: its other changes are sent")
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil || d.Memberships[0].SectionGID != "s1" {
		t.Fatalf("the dropped move is undone locally: %+v %v", d, err)
	}
}

// A subtask created offline under a task deleted in Asana is dropped.
func TestPushDropsSubtaskOfDeletedParent(t *testing.T) {
	_, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "親", Workspace: &asana.Ref{GID: "w1"}}})
	sub, _ := st.CreateTaskLocal(ctx, store.NewTask{ParentGID: "t2", Name: "子"}, now)
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || res.Discarded[0].TaskGID != sub {
		t.Fatalf("push = %+v %v", res, err)
	}
	if d, _ := st.GetTask(ctx, "t2"); d == nil || d.NumSubtasks != 0 {
		t.Fatalf("parent should no longer count the dropped subtask: %+v", d)
	}
}

// The check after a 5xx finds the task gone: everything for it is dropped.
func TestPushVerifyFindsTaskDeleted(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.AddCommentLocal(ctx, "t1", "x", store.UserRef{GID: "me"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/tasks/t1/stories" && r.Method == http.MethodPost {
			delete(ws.tasks, "t1") // deleted meanwhile
			rw.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || res.Failed != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if _, err := st.GetTask(ctx, "t1"); err == nil {
		t.Fatal("deleted task should be removed locally")
	}
	assertOutboxEmpty(t, st)
}

// If Asana cannot be asked whether the task still exists, nothing is dropped yet.
func TestPushKeepsChangeWhenDeletionCannotBeChecked(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "A"}, {GID: "gone", Name: "?"}})
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "gone", now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/tasks/t1" {
			dropConnection(rw)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err == nil || len(res.Discarded) != 0 || res.Remaining != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if op := opByKind(t, st, store.OpMoveTask); op == nil || op.State != store.OpPending {
		t.Fatalf("entry should stay queued: %+v", op)
	}
}

// A status update on a project deleted in Asana is dropped.
func TestPushDropsStatusOfDeletedProject(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	if _, err := st.CreateStatusLocal(ctx, "p1", "on_track", "順調", "本文", "", store.UserRef{Name: "Me"}, now); err != nil {
		t.Fatal(err)
	}
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/status_updates" {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"project not found"}]}`)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || res.Discarded[0].Summary != "ステータス更新「順調」" {
		t.Fatalf("push = %+v %v", res, err)
	}
	if ups, _ := st.StatusUpdates(ctx, "p1"); len(ups) != 0 {
		t.Fatalf("local status should be removed: %+v", ups)
	}
}

// Logged in as someone else: Asana answers 404 for the old account's tasks. Nothing is
// dropped; the push stops with ErrAccountChanged. With a known local account Push checks
// it before sending anything (nothing is sent as the other user); without one (no sync
// yet) the check comes before the first drop.
func TestPushDoesNotDropWhenAccountChanged(t *testing.T) {
	for _, tc := range []struct {
		name, storedMe, apiMe string
		failed                int // entries marked failed (the one whose 404 led to the check)
	}{
		{"other user", "me", "someone-else", 0},
		{"local user unknown", "", "me", 1}, // no sync yet: cannot tell whose data this is
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			_ = st.SetSyncState(ctx, store.KeyMeGID, tc.storedMe)
			_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "前のアカウントのタスク"}})
			_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"completed": true}, now)
			_, _ = st.AddCommentLocal(ctx, "t2", "メモ", store.UserRef{GID: "me"}, now)
			ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/users/me" {
					fmt.Fprintf(rw, `{"data":{"gid":%q}}`, tc.apiMe)
					return true
				}
				return false
			})
			res, err := s.Push(ctx)
			if !errors.Is(err, ErrAccountChanged) || len(res.Discarded) != 0 || res.Failed != tc.failed {
				t.Fatalf("push = %+v %v", res, err)
			}
			if _, err := st.GetTask(ctx, "t2"); err != nil {
				t.Fatal("the task must not be removed")
			}
			ops, _ := st.ListOps(ctx)
			first := store.OpPending
			if tc.failed == 1 {
				first = store.OpFailed
			}
			if len(ops) != 2 || ops[0].State != first || ops[1].State != store.OpPending || (tc.failed == 0 && ops[0].Attempts+ops[1].Attempts != 0) {
				t.Fatalf("entries = %+v", ops)
			}
			if d, _ := st.PendingDiscards(ctx); len(d) != 0 {
				t.Fatalf("nothing should be recorded as dropped: %+v", d)
			}
		})
	}
}

// One push drops at most maxDroppedTasks tasks; the rest are left failed for the user.
func TestPushLimitsDroppedTasksPerPush(t *testing.T) {
	_, _, s, st := setupPush(t)
	ctx := context.Background()
	for i := 0; i < maxDroppedTasks+2; i++ {
		gid := fmt.Sprintf("gone%d", i)
		_ = st.UpsertTasks(ctx, []asana.Task{{GID: gid, Name: gid}})
		_ = st.UpdateTaskLocal(ctx, gid, map[string]any{"completed": true}, now)
	}
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != maxDroppedTasks || res.Failed != 2 {
		t.Fatalf("push = %+v %v", res, err)
	}
	for _, op := range must1(st.ListOps(ctx)) {
		if op.State != store.OpFailed || !strings.Contains(op.LastError, "上限") {
			t.Fatalf("entry = %+v", op)
		}
	}
}

// What the user wrote is kept with the record of the dropped change.
func TestDroppedChangeKeepsPayload(t *testing.T) {
	_, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "消された"}})
	long := strings.Repeat("長い本文。", 30)
	_, _ = st.AddCommentLocal(ctx, "t2", long, store.UserRef{GID: "me"}, now)
	if res, err := s.Push(ctx); err != nil || len(res.Discarded) != 1 || res.Discarded[0].Payload["text"] != long {
		t.Fatalf("push = %+v %v", res, err)
	}
	d, _ := st.PendingDiscards(ctx)
	if len(d) != 1 || d[0].Payload["text"] != long || len([]rune(d[0].Summary)) > 60 {
		t.Fatalf("recorded = %+v", d)
	}
}

// 403 / 400 are not deletions: the change fails and nothing is dropped. Neither is a 403
// when asking whether the task still exists.
func TestPushRejectionsAreNotDropped(t *testing.T) {
	for _, tc := range []struct {
		name          string
		put, probe    int // status for PUT /tasks/t1 and GET /tasks/t1 (0: normal)
		wantInLastErr string
	}{
		{"put 403", 403, 0, "403"},
		{"put 400", 400, 0, "400"},
		{"probe 403", 404, 403, "404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
			gets := 0
			ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/tasks/t1" {
					return false
				}
				if r.Method == http.MethodPut {
					rw.WriteHeader(tc.put)
					fmt.Fprintf(rw, `{"errors":[{"message":"status %d"}]}`, tc.put)
					return true
				}
				if gets++; gets > 1 && tc.probe != 0 { // the first GET is pushUpdate's own read
					rw.WriteHeader(tc.probe)
					return true
				}
				return false
			})
			res, err := s.Push(ctx)
			if err != nil || res.Failed != 1 || len(res.Discarded) != 0 {
				t.Fatalf("push = %+v %v", res, err)
			}
			op := opByKind(t, st, store.OpUpdateTask)
			if op == nil || op.State != store.OpFailed || !strings.Contains(op.LastError, tc.wantInLastErr) {
				t.Fatalf("entry = %+v", op)
			}
			if _, err := st.GetTask(ctx, "t1"); err != nil {
				t.Fatal("task must stay")
			}
		})
	}
}

// If Asana cannot say who is logged in (5xx), nothing is dropped and the change waits.
func TestPushAccountCheck5xxKeepsChange(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "消えたかも"}})
	_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"completed": true}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/users/me" {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err == nil || len(res.Discarded) != 0 || res.Failed != 0 || res.Remaining != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if op := opByKind(t, st, store.OpUpdateTask); op == nil || op.State != store.OpPending {
		t.Fatalf("entry = %+v", op)
	}
}

// A change queued by another process while this one pushes (it got ErrPushBusy and was
// told this process sends it) is sent before the lock is released.
func TestPushSendsChangesQueuedMeanwhile(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	ws.tasks["t2"] = &serverTask{Name: "X"}
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "X"}})
	other, err := store.Open(ctx, st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	queued := false
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut && r.URL.Path == "/tasks/t1" && !queued {
			queued = true
			if err := other.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "Y"}, now); err != nil {
				t.Error(err)
			}
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if ws.tasks["t2"].Name != "Y" {
		t.Fatal("the change queued meanwhile should be sent by this push")
	}
}

// The drop limit covers the whole push, including the pass for changes queued meanwhile,
// and the account is checked once.
func TestPushDropLimitSpansCatchUp(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	other, err := store.Open(ctx, st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for i := 0; i < maxDroppedTasks+2; i++ {
		gid := fmt.Sprintf("gone%d", i)
		_ = st.UpsertTasks(ctx, []asana.Task{{GID: gid, Name: gid}})
		if i < maxDroppedTasks {
			_ = st.UpdateTaskLocal(ctx, gid, map[string]any{"completed": true}, now)
		}
	}
	queued, mes := false, 0
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/users/me" {
			mes++
		}
		if r.URL.Path == "/tasks/gone0" && !queued { // during the first pass
			queued = true
			for _, gid := range []string{fmt.Sprintf("gone%d", maxDroppedTasks), fmt.Sprintf("gone%d", maxDroppedTasks+1)} {
				if err := other.UpdateTaskLocal(ctx, gid, map[string]any{"completed": true}, now); err != nil {
					t.Error(err)
				}
			}
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != maxDroppedTasks || res.Failed != 2 {
		t.Fatalf("push = %d dropped, %d failed, %v", len(res.Discarded), res.Failed, err)
	}
	if mes != 1 {
		t.Fatalf("account checked %d times, want once", mes)
	}
}

func TestDownloadAttachmentFindsCachedFileWithoutRecordedPath(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T"}})
	_ = st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "78", Name: "shot 1.png"}})
	// Renamed by an earlier run whose path update did not reach the database.
	cached := filepath.Join(dir, "78-shot_1.png")
	_ = os.WriteFile(cached, []byte("png"), 0o600)
	_ = st.SetAttachmentPath(ctx, "78", filepath.Join(dir, "78-shot 1.png")) // no longer exists

	path, err := (&Syncer{Store: st, CacheDir: dir}).DownloadAttachment(ctx, "t1", "78") // offline
	if err != nil || path != cached {
		t.Fatalf("path = %q, %v; want %q", path, err, cached)
	}
	if a, _ := st.GetAttachment(ctx, "78"); a.LocalPath != cached {
		t.Fatalf("stored path = %q", a.LocalPath)
	}
}

// The store hands out task names without control characters; a server name that has them
// must still count as unchanged, and "keep local" must get out of a conflict.
func TestPushWithControlCharactersOnServer(t *testing.T) {
	const evil = "\x1b]52;c;ZXZpbA==\x07\r"
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	ws.tasks["t1"].Name = "A" + evil
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A" + evil, Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "C (local)"}, now)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}

	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "D (local)"}, now)
	ws.tasks["t1"].Name = "B (server)" + evil
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	ops, _ := st.ListOps(ctx)
	if err := st.ResolveKeepLocal(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("push after keep local = %+v, %v", res, err)
	}
	if ws.tasks["t1"].Name != "D (local)" {
		t.Fatalf("server name = %q", ws.tasks["t1"].Name)
	}
}
