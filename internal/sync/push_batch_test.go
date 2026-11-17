package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// addBatchTasks adds tasks b1..bn (in project p1, section s1) to the fake server and the store.
func addBatchTasks(t *testing.T, ws *writeServer, st *store.Store, n int) []string {
	t.Helper()
	ctx := context.Background()
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "Todo"}, {GID: "s2", Name: "Doing"}}); err != nil {
		t.Fatal(err)
	}
	var gids []string
	for i := 1; i <= n; i++ {
		gid := fmt.Sprintf("b%d", i)
		ws.tasks[gid] = &serverTask{Name: gid, Project: "p1", Section: "s1"}
		if err := st.UpsertTasks(ctx, []asana.Task{{GID: gid, Name: gid, Workspace: &asana.Ref{GID: "w1"},
			Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
			t.Fatal(err)
		}
		gids = append(gids, gid)
	}
	return gids
}

// outerCalls returns the requests (not batch actions) whose "METHOD path" starts with prefix.
func (w *writeServer) outerCalls(prefix string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, c := range w.outer {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// batchActions decodes the actions of each POST /batch with a write in it, in order.
func (w *writeServer) writeBatches() [][]map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out [][]map[string]any
	for _, c := range w.calls {
		body, ok := strings.CutPrefix(c, "POST /batch ")
		if !ok {
			continue
		}
		var req struct {
			Data struct {
				Actions []map[string]any `json:"actions"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			w.t.Fatal(err)
		}
		if len(req.Data.Actions) > 0 && req.Data.Actions[0]["method"] != "get" {
			out = append(out, req.Data.Actions)
		}
	}
	return out
}

// Completing many tasks at once sends the updates 10 per POST /batch, after reading the
// tasks (for conflicts) 10 per batch too, instead of 2 requests per task.
func TestPushBatchesUpdatesOfManyTasks(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 12)
	for _, g := range gids {
		if err := st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 12 || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, g := range gids {
		if !ws.tasks[g].Completed {
			t.Fatalf("%s not completed on the server", g)
		}
		if d, _ := st.GetTask(ctx, g); d.Pending || !d.Completed {
			t.Fatalf("%s in the store: %+v", g, d.TaskSummary)
		}
	}
	if got := ws.outerCalls("PUT /tasks/"); len(got) != 0 {
		t.Fatalf("updates sent one by one: %v", got)
	}
	if got := ws.outerCalls("POST /batch"); len(got) != 4 { // read 10 + write 10, read 2 + write 2
		t.Fatalf("batches = %d, want 4 (%v)", len(got), ws.outer)
	}
	batches := ws.writeBatches()
	if len(batches) != 2 || len(batches[0]) != 10 || len(batches[1]) != 2 {
		t.Fatalf("write batches = %v", batches)
	}
	a := batches[0][0]
	if a["method"] != "put" || a["relative_path"] != "/tasks/b1" || fmt.Sprint(a["data"]) != "map[completed:true]" {
		t.Fatalf("first action = %v", a)
	}
	if _, ok := a["options"].(map[string]any)["fields"]; !ok {
		t.Fatalf("an update should ask for the task's fields: %v", a)
	}
}

// Moving several tasks to a section goes out in one batch; the tasks are read again after.
func TestPushBatchesMoves(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 3)
	for _, g := range gids {
		if err := st.MoveTaskLocal(ctx, g, "p1", "s2", now); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 3 || res.Remaining != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, g := range gids {
		if ws.tasks[g].Section != "s2" || sectionOf(t, st, g) != "s2" {
			t.Fatalf("%s: server %q, store %q", g, ws.tasks[g].Section, sectionOf(t, st, g))
		}
	}
	if got := ws.outerCalls("POST /sections/"); len(got) != 0 {
		t.Fatalf("moves sent one by one: %v", got)
	}
	if b := ws.writeBatches(); len(b) != 1 || len(b[0]) != 3 || b[0][0]["relative_path"] != "/sections/s2/addTask" {
		t.Fatalf("write batches = %v", b)
	}
}

// A group never has two changes to one task, and stops at an entry that is not a plain
// change to an existing task (a create): the order of the queue is kept.
func TestPushBatchKeepsOrder(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	addBatchTasks(t, ws, st, 3)
	_ = st.UpdateTaskLocal(ctx, "b1", map[string]any{"name": "B1"}, now)
	_ = st.UpdateTaskLocal(ctx, "b2", map[string]any{"name": "B2"}, now)
	_ = st.MoveTaskLocal(ctx, "b1", "p1", "s2", now) // the same task again: a new group
	_ = st.MoveTaskLocal(ctx, "b3", "p1", "s2", now)
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "new", WorkspaceGID: "w1", ProjectGID: "p1"}, now); err != nil {
		t.Fatal(err)
	}
	_ = st.MoveTaskLocal(ctx, "b2", "p1", "s2", now)
	_ = st.UpdateTaskLocal(ctx, "b3", map[string]any{"name": "B3"}, now)

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 7 || res.Remaining != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	var groups []string
	for _, b := range ws.writeBatches() {
		var paths []string
		for _, a := range b {
			paths = append(paths, fmt.Sprint(a["method"], " ", a["relative_path"]))
		}
		groups = append(groups, strings.Join(paths, ", "))
	}
	want := []string{
		"put /tasks/b1, put /tasks/b2",
		"post /sections/s2/addTask, post /sections/s2/addTask", // b1, b3
		"post /sections/s2/addTask, put /tasks/b3",             // b2, b3 after the create
	}
	if strings.Join(groups, " | ") != strings.Join(want, " | ") {
		t.Fatalf("write batches:\n%s\nwant:\n%s", strings.Join(groups, "\n"), strings.Join(want, "\n"))
	}
	if got := ws.outerCalls("POST /tasks"); len(got) != 1 {
		t.Fatalf("create = %v", got)
	}
	// The create went out between the groups.
	var order []string
	for _, c := range ws.outer {
		if c == "POST /tasks" || c == "POST /batch" {
			order = append(order, c)
		}
	}
	if i := strings.Index(strings.Join(order, ","), "POST /tasks"); i < 0 || strings.Count(strings.Join(order, ",")[:i], "POST /batch") < 3 {
		t.Fatalf("create sent out of order: %v", order)
	}
	if ws.tasks["b1"].Name != "B1" || ws.tasks["b3"].Name != "B3" || ws.tasks["b2"].Section != "s2" {
		t.Fatalf("server = %+v %+v %+v", ws.tasks["b1"], ws.tasks["b2"], ws.tasks["b3"])
	}
}

// What Asana answers to each request of a batch is handled as for the entry alone: a
// conflict, a 404 (the change is dropped), a 400 (sent alone, then failed), a 500 (checked
// and sent again).
func TestPushBatchPerRequestResults(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	addBatchTasks(t, ws, st, 5)
	_ = st.UpdateTaskLocal(ctx, "b1", map[string]any{"name": "B1"}, now)
	_ = st.UpdateTaskLocal(ctx, "b2", map[string]any{"name": "B2"}, now)
	ws.tasks["b2"].Name = "changed on the server"
	_ = st.SetFollowerLocal(ctx, "b3", "u9", true, now) // Asana: 404 (e.g. the user is gone)
	_ = st.UpdateTaskLocal(ctx, "b4", map[string]any{"name": "bad"}, now)
	_ = st.UpdateTaskLocal(ctx, "b5", map[string]any{"name": "B5"}, now)
	ws.setFailAfter("PUT /tasks/b5", 1) // applied, answered 500
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/tasks/b3/addFollowers":
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"user not found"}]}`)
			return true
		case r.Method == http.MethodPut && r.URL.Path == "/tasks/b4":
			rw.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(rw, `{"errors":[{"message":"invalid name"}]}`)
			return true
		}
		return false
	})
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || res.Conflicts != 1 || res.Failed != 1 || len(res.Discarded) != 1 || res.Remaining != 0 {
		t.Fatalf("push = %+v", res)
	}
	if ws.tasks["b1"].Name != "B1" || ws.tasks["b5"].Name != "B5" || ws.tasks["b2"].Name != "changed on the server" {
		t.Fatalf("server: b1 %q b2 %q b5 %q", ws.tasks["b1"].Name, ws.tasks["b2"].Name, ws.tasks["b5"].Name)
	}
	if got := ws.outerCalls("PUT /tasks/b4"); len(got) != 1 {
		t.Fatalf("a 400 should be sent again alone once: %v", got)
	}
	if got := ws.outerCalls("PUT /tasks/b5"); len(got) != 1 {
		t.Fatalf("a 500 should be checked and sent again alone: %v", got)
	}
	ops, _ := st.ListOps(ctx)
	states := map[string]string{}
	for _, op := range ops {
		states[op.TaskGID] = op.State
	}
	if states["b2"] != store.OpConflict || states["b4"] != store.OpFailed || len(ops) != 2 {
		t.Fatalf("left in the outbox: %+v", ops)
	}
}

// writeBatchHook answers the POST /batch requests that write with answer (reads pass).
func writeBatchHook(answer func(rw http.ResponseWriter) bool) func(rw http.ResponseWriter, r *http.Request) bool {
	return func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/batch" {
			return false
		}
		b, _ := io.ReadAll(r.Body)
		if len(b) == 0 {
			panic("no body")
		}
		if !strings.Contains(string(b), `"method":"get"`) {
			return answer(rw)
		}
		return false
	}
}

// The batch as a whole fails: each entry is handled as if its own request had failed so.
func TestPushBatchFails(t *testing.T) {
	queue := func(t *testing.T) (*writeServer, *Syncer, *store.Store) {
		ws, _, s, st := setupPush(t)
		for _, g := range addBatchTasks(t, ws, st, 3) {
			if err := st.UpdateTaskLocal(context.Background(), g, map[string]any{"completed": true}, now); err != nil {
				t.Fatal(err)
			}
		}
		return ws, s, st
	}
	completed := func(ws *writeServer) int {
		n := 0
		for _, g := range []string{"b1", "b2", "b3"} {
			if ws.tasks[g].Completed {
				n++
			}
		}
		return n
	}

	t.Run("500: checked in Asana, then sent again", func(t *testing.T) {
		ws, s, st := queue(t)
		failed := false
		ws.setHook(writeBatchHook(func(rw http.ResponseWriter) bool {
			if failed {
				return false
			}
			failed = true
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		}))
		res, err := s.Push(context.Background())
		if err != nil || res.Sent != 3 || res.Failed != 0 || res.Remaining != 0 || completed(ws) != 3 {
			t.Fatalf("push = %+v, %v (completed %d)", res, err, completed(ws))
		}
		if p, c, f := pendingCount(t, st); p+c+f != 0 {
			t.Fatalf("outbox = %d %d %d", p, c, f)
		}
		if len(ws.writeBatches()) != 1 || len(ws.outerCalls("PUT /tasks/")) != 3 {
			t.Fatalf("want one failed batch, then each alone: %v", ws.outer)
		}
	})

	t.Run("connection dropped: kept for the next push", func(t *testing.T) {
		ws, s, st := queue(t)
		ws.setHook(writeBatchHook(func(rw http.ResponseWriter) bool { dropConnection(rw); return true }))
		res, err := s.Push(context.Background())
		if err == nil || !isNetworkError(err) || res.Remaining != 3 || res.Failed != 0 {
			t.Fatalf("push = %+v, %v", res, err)
		}
		ops, _ := st.ListOps(context.Background())
		for _, op := range ops {
			if op.State != store.OpPending {
				t.Fatalf("entry %+v", op)
			}
		}
		ws.setHook(nil)
		if res, err := s.Push(context.Background()); err != nil || res.Sent != 3 || completed(ws) != 3 {
			t.Fatalf("next push = %+v, %v", res, err)
		}
	})

	t.Run("429: not sent, attempts not counted", func(t *testing.T) {
		ws, s, st := queue(t)
		ws.setHook(writeBatchHook(func(rw http.ResponseWriter) bool {
			rw.WriteHeader(http.StatusTooManyRequests)
			return true
		}))
		res, err := s.Push(context.Background())
		if !isRateLimited(err) || res.Remaining != 3 || completed(ws) != 0 {
			t.Fatalf("push = %+v, %v", res, err)
		}
		ops, _ := st.ListOps(context.Background())
		for _, op := range ops {
			if op.State != store.OpPending || op.Verify || op.Attempts != 0 {
				t.Fatalf("entry %+v", op)
			}
		}
	})

	t.Run("applied, then 500: sent again alone, no conflict", func(t *testing.T) {
		ws, s, st := queue(t)
		failed := false
		ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
			b, _ := io.ReadAll(r.Body)
			if r.URL.Path != "/batch" || strings.Contains(string(b), `"method":"get"`) || failed {
				return false
			}
			failed = true
			var req struct{ Data map[string]any }
			_ = json.Unmarshal(b, &req)
			ws.serveBatch(httptest.NewRecorder(), req.Data) // applied, but the answer is lost
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		})
		res, err := s.Push(context.Background())
		if err != nil || res.Sent != 3 || res.Failed != 0 || res.Remaining != 0 || completed(ws) != 3 {
			t.Fatalf("push = %+v, %v (completed %d)", res, err, completed(ws))
		}
		if p, c, f := pendingCount(t, st); p+c+f != 0 {
			t.Fatalf("outbox = %d %d %d", p, c, f)
		}
		// Updates are harmless to repeat: sent again alone; the check finds the server
		// already has the value, so it is no conflict.
		if got := ws.outerCalls("PUT /tasks/"); len(got) != 3 {
			t.Fatalf("resent = %v", got)
		}
	})

	t.Run("401: kept for after a new login", func(t *testing.T) {
		ws, s, st := queue(t)
		ws.setHook(writeBatchHook(func(rw http.ResponseWriter) bool {
			rw.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(rw, `{"errors":[{"message":"Not Authorized"}]}`)
			return true
		}))
		res, err := s.Push(context.Background())
		if err == nil || res.Remaining != 3 || res.Failed != 0 || completed(ws) != 0 {
			t.Fatalf("push = %+v, %v", res, err)
		}
		ops, _ := st.ListOps(context.Background())
		for _, op := range ops {
			if op.State != store.OpPending || op.Verify || op.Attempts != 0 {
				t.Fatalf("entry %+v", op)
			}
		}
	})

	t.Run("429: an entry to send alone waits too", func(t *testing.T) {
		ws, s, _ := queue(t)
		delete(ws.tasks, "b2") // not in the reading batch: it would be sent alone
		ws.setHook(writeBatchHook(func(rw http.ResponseWriter) bool {
			rw.WriteHeader(http.StatusTooManyRequests)
			return true
		}))
		res, err := s.Push(context.Background())
		if !isRateLimited(err) || res.Remaining != 3 {
			t.Fatalf("push = %+v, %v", res, err)
		}
		if got := ws.outerCalls("GET /tasks/b2"); len(got) != 0 {
			t.Fatalf("sent alone while rate limited: %v", got)
		}
	})

	t.Run("refused: sent one by one", func(t *testing.T) {
		ws, s, _ := queue(t)
		ws.setHook(writeBatchHook(func(rw http.ResponseWriter) bool {
			rw.WriteHeader(http.StatusForbidden)
			fmt.Fprint(rw, `{"errors":[{"message":"batch not allowed"}]}`)
			return true
		}))
		res, err := s.Push(context.Background())
		if err != nil || res.Sent != 3 || completed(ws) != 3 || len(ws.outerCalls("PUT /tasks/")) != 3 {
			t.Fatalf("push = %+v, %v (%v)", res, err, ws.outer)
		}
	})
}
