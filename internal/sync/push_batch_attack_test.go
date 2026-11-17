package sync

import (
	"context"
	"fmt"
	"net/http"
	stdsync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// A batch that keeps answering 5xx (and the single requests too) must not be retried for
// ever: the attempts are counted as for single sends, so the entries end up failed.
func TestAttackBatchKeeps500GivesUp(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	for _, g := range addBatchTasks(t, ws, st, 3) {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
	}
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut {
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return writeBatchHook(func(rw http.ResponseWriter) bool {
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		})(rw, r)
	})
	for i := 0; i < 8; i++ {
		_, _ = s.Push(ctx)
	}
	ops, _ := st.ListOps(ctx)
	for _, op := range ops {
		if op.State != store.OpFailed {
			t.Fatalf("op %d still %s after repeated 5xx (attempts %d)", op.ID, op.State, op.Attempts)
		}
		if op.Attempts > maxSendAttempts {
			t.Fatalf("op %d attempts %d > %d", op.ID, op.Attempts, maxSendAttempts)
		}
	}
	if len(ops) != 3 {
		t.Fatalf("ops = %d", len(ops))
	}
}

// One task that is gone (not returned by the read batch) must not hold back its neighbours.
func TestAttackBatchOneTaskGone(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 4)
	for _, g := range gids {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
	}
	delete(ws.tasks, "b2")
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Sent != 3 || len(res.Discarded) != 1 || res.Remaining != 0 {
		t.Fatalf("push = %+v", res)
	}
	for _, g := range []string{"b1", "b3", "b4"} {
		if !ws.tasks[g].Completed {
			t.Fatalf("%s not completed", g)
		}
	}
}

// A 200 answer that is not a batch answer (a proxy page) after the writes were applied: the
// entries are sent again alone, and nothing is stuck or sent twice to a non-idempotent target.
func TestAttackBatchGarbage200(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	for _, g := range addBatchTasks(t, ws, st, 3) {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
	}
	ws.setHook(writeBatchHook(func(rw http.ResponseWriter) bool {
		rw.WriteHeader(http.StatusOK)
		fmt.Fprint(rw, `<html>proxy</html>`)
		return true
	}))
	res, err := s.Push(ctx)
	if err != nil || res.Remaining != 0 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, g := range []string{"b1", "b2", "b3"} {
		if !ws.tasks[g].Completed {
			t.Fatalf("%s not completed", g)
		}
	}
}

// One action answered 429 inside a batch: that entry and the rest stay queued, the push
// stops with the rate-limit error, nothing is lost, and the attempt is not counted.
func TestAttackBatchInnerRateLimit(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	for _, g := range addBatchTasks(t, ws, st, 3) {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
	}
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut && r.URL.Path == "/tasks/b2" {
			rw.Header().Set("Retry-After", "1")
			rw.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(rw, `{"errors":[{"message":"rate limited"}]}`)
			return true
		}
		return false
	})
	_, err := s.Push(ctx)
	if !isRateLimited(err) {
		t.Fatalf("err = %v", err)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].TaskGID != "b2" || ops[0].State != store.OpPending || ops[0].Verify || ops[0].Attempts != 0 {
		t.Fatalf("left: %+v", ops)
	}
	ws.setHook(nil)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || !ws.tasks["b2"].Completed {
		t.Fatalf("next push = %+v, %v", res, err)
	}
}

// Two pushes at once on the same store: every change reaches Asana once.
func TestAttackBatchConcurrentPush(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 12)
	for _, g := range gids {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
	}
	var wg stdsync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Push(ctx) }()
	}
	wg.Wait()
	_, _ = s.Push(ctx)
	if n := ws.countCalls("PUT /tasks/"); n != 12 {
		t.Fatalf("PUT /tasks calls = %d, want 12 (each once)", n)
	}
	if p, c, f := pendingCount(t, st); p+c+f != 0 {
		t.Fatalf("outbox = %d %d %d", p, c, f)
	}
}

// An update of a task created offline, queued between others: it waits for the create, and
// the batch around it is cut.
func TestAttackBatchTempGidInMiddle(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	addBatchTasks(t, ws, st, 3)
	_ = st.UpdateTaskLocal(ctx, "b1", map[string]any{"name": "B1"}, now)
	local, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "new", WorkspaceGID: "w1", ProjectGID: "p1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateTaskLocal(ctx, local, map[string]any{"name": "new2"}, now)
	_ = st.UpdateTaskLocal(ctx, "b2", map[string]any{"name": "B2"}, now)
	_ = st.UpdateTaskLocal(ctx, "b3", map[string]any{"name": "B3"}, now)
	res, err := s.Push(ctx)
	if err != nil || res.Remaining != 0 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if ws.tasks["b1"].Name != "B1" || ws.tasks["b2"].Name != "B2" || ws.tasks["b3"].Name != "B3" {
		t.Fatal("updates missing")
	}
	if n := ws.countCalls("POST /tasks "); n != 1 {
		t.Fatalf("creates = %d", n)
	}
}

// The parent is gone (its queued changes and its subtask's are dropped) while the subtask's
// own change in the same group got a 5xx: the drop must not make the push fail or resurrect
// the subtask's entry.
func TestAttackBatchParentGoneChildFails(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	addBatchTasks(t, ws, st, 3)
	ws.tasks["c1"] = &serverTask{Name: "c1"}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "c1", Name: "c1", Workspace: &asana.Ref{GID: "w1"}, Parent: &asana.Ref{GID: "b1"}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateTaskLocal(ctx, "b1", map[string]any{"name": "B1"}, now)
	_ = st.UpdateTaskLocal(ctx, "c1", map[string]any{"name": "C1"}, now)
	_ = st.UpdateTaskLocal(ctx, "b2", map[string]any{"name": "B2"}, now)
	delete(ws.tasks, "b1")
	ws.setFailAfter("PUT /tasks/c1", 1)
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatalf("push: %v (%+v)", err, res)
	}
	if ws.tasks["b2"].Name != "B2" {
		t.Fatal("b2 not sent")
	}
	ops, _ := st.ListOps(ctx)
	for _, op := range ops {
		if op.TaskGID == "b1" || op.TaskGID == "c1" {
			t.Fatalf("entry of the dropped tree is back: %+v", op)
		}
	}
	if res.Remaining != 0 {
		t.Fatalf("res = %+v", res)
	}
}

// More tasks gone than maxDroppedTasks in one group: the safeguard still holds.
func TestAttackBatchManyGoneKeepsSafeguard(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 8)
	for _, g := range gids {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
		delete(ws.tasks, g)
	}
	res, _ := s.Push(ctx)
	if len(res.Discarded) > maxDroppedTasks {
		t.Fatalf("discarded %d > %d", len(res.Discarded), maxDroppedTasks)
	}
}

// An entry waiting to be checked in Asana (verify) is never put in a batch: it is checked
// first, on its own.
func TestAttackBatchVerifyEntryNotBatched(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	for _, g := range addBatchTasks(t, ws, st, 3) {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
	}
	ops, _ := st.ListOps(ctx)
	if err := st.MarkOpVerify(ctx, ops[1].ID, "maybe sent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	for _, b := range ws.writeBatches() {
		for _, a := range b {
			if a["relative_path"] == "/tasks/b2" {
				t.Fatalf("a verify entry was sent in a batch: %v", b)
			}
		}
	}
}

// After a batch 5xx, each entry has been counted once for the batch and once for the
// request sent again alone, as for a single send.
func TestAttackBatchCountsAttempts(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	for _, g := range addBatchTasks(t, ws, st, 2) {
		_ = st.UpdateTaskLocal(ctx, g, map[string]any{"completed": true}, now)
	}
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut {
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return writeBatchHook(func(rw http.ResponseWriter) bool {
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		})(rw, r)
	})
	_, _ = s.Push(ctx)
	ops, _ := st.ListOps(ctx)
	if len(ops) != 2 {
		t.Fatalf("ops = %d", len(ops))
	}
	for _, op := range ops {
		if op.Attempts != 2 {
			t.Fatalf("op %d attempts = %d, want 2", op.ID, op.Attempts)
		}
	}
}

// Moved tasks are read again, so the local cache shows the new section at once.
func TestAttackBatchMoveRefreshesCache(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 2)
	for _, g := range gids {
		_ = st.MoveTaskLocal(ctx, g, "p1", "s2", now)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ws.countCalls("POST /sections/s2/addTask"); got != 2 {
		t.Fatalf("moves = %d", got)
	}
	if n := len(ws.outerCalls("POST /batch")); n < 2 { // write, read again
		t.Fatalf("batches = %d: the moved tasks were not read again", n)
	}
}
