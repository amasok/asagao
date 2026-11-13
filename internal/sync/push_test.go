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

// serverTask is the fake server's state for one task.
type serverTask struct {
	Name      string `json:"name"`
	Notes     string `json:"notes"`
	Completed bool   `json:"completed"`
	DueOn     string `json:"due_on"`
	Section   string `json:"-"`
	Project   string `json:"-"`
}

type writeServer struct {
	t     *testing.T
	mu    stdsync.Mutex
	tasks map[string]*serverTask
	calls []string // "METHOD path body"
	next  int
}

func (w *writeServer) taskJSON(gid string) string {
	st := w.tasks[gid]
	ms := "[]"
	if st.Project != "" {
		ms = fmt.Sprintf(`[{"project":{"gid":%q},"section":{"gid":%q}}]`, st.Project, st.Section)
	}
	return fmt.Sprintf(`{"gid":%q,"name":%q,"notes":%q,"completed":%t,"due_on":%q,"memberships":%s,"modified_at":"2026-11-13T00:00:00Z"}`,
		gid, st.Name, st.Notes, st.Completed, st.DueOn, ms)
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
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/tasks":
		w.next++
		gid := fmt.Sprintf("new%d", w.next)
		st := &serverTask{}
		st.Name, _ = body.Data["name"].(string)
		st.Completed, _ = body.Data["completed"].(bool)
		if ps, ok := body.Data["projects"].([]any); ok && len(ps) > 0 {
			st.Project, _ = ps[0].(string)
		}
		w.tasks[gid] = st
		fmt.Fprintf(rw, `{"data":%s}`, w.taskJSON(gid))
	case len(parts) == 3 && parts[0] == "sections" && parts[2] == "addTask":
		gid, _ := body.Data["task"].(string)
		w.tasks[gid].Section = parts[1]
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "stories":
		if _, ok := w.tasks[parts[1]]; !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"not found"}]}`)
			return
		}
		fmt.Fprintf(rw, `{"data":{"gid":"story-%s","type":"comment","text":%q,"created_by":{"gid":"me","name":"Me"},"created_at":"2026-11-13T00:00:00Z"}}`,
			parts[1], body.Data["text"])
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
	if err != nil || d.Pending || len(d.Stories) != 1 || d.Stories[0].GID != "story-new1" {
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

func TestPushDeletedTaskFails(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	delete(ws.tasks, "t1")
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	ops, _ := st.ListOps(ctx)
	if ops[0].State != store.OpFailed || !strings.Contains(ops[0].LastError, "404") {
		t.Fatalf("op = %+v", ops[0])
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
