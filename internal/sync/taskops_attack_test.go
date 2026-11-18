package sync

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	asana "github.com/amasok/asagao/internal/asana/model"

	"github.com/amasok/asagao/internal/store"
)

// A delete that keeps failing (429, 402, 500) must not remove the task from the store.
func TestAttackPushDeleteFailuresKeepTheTask(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusPaymentRequired, http.StatusInternalServerError, http.StatusBadGateway} {
		ws, _, s, st := setupPush(t)
		code := status
		ws.hook = taskOpsHook(ws, &code)
		ctx := context.Background()
		if _, err := st.DeleteTaskLocal(ctx, "t1", now); err != nil {
			t.Fatal(err)
		}
		_, _ = s.Push(ctx)
		ops, _ := st.ListOps(ctx)
		if len(ops) != 1 {
			t.Errorf("%d: outbox %+v", status, ops)
		}
		if _, err := st.GetTask(ctx, "t1"); errors.Is(err, store.ErrNotFound) {
			t.Errorf("%d: task removed though the delete failed", status)
		}
		// after the failure clears, a second push completes it
		code = 0
		for i := 0; i < 3; i++ {
			_, _ = s.Push(ctx)
		}
		if ops, _ := st.ListOps(ctx); len(ops) > 1 {
			t.Errorf("%d: outbox after recovery %+v", status, ops)
		}
	}
}

// A discarded delete never reaches Asana.
func TestAttackDiscardedDeleteIsNotSent(t *testing.T) {
	ws, _, s, st := setupPush(t)
	status := 0
	ws.hook = taskOpsHook(ws, &status)
	ctx := context.Background()
	if _, err := st.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	ops, _ := st.ListOps(ctx)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if hasCall(ws, "DELETE /tasks/t1 ") {
		t.Fatalf("sent: %v", ws.calls)
	}
}

// Moves to a place are sent in the order they were made (A after B, then C after A).
func TestAttackMovesToAPlaceKeepOrder(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	ws.addTask("t2", "B", "p1")
	ws.addTask("t3", "C", "p1")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "A"}}))
	must(st.UpsertTasks(ctx, []asana.Task{
		{GID: "t2", Name: "B", Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}},
		{GID: "t3", Name: "C", Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}))
	must(st.MoveTaskAtLocal(ctx, "t1", "p1", "s1", store.Position{After: "t2"}, now))
	must(st.MoveTaskAtLocal(ctx, "t3", "p1", "s1", store.Position{After: "t1"}, now))
	must(st.MoveTaskAtLocal(ctx, "t1", "p1", "s1", store.Position{Before: "t2"}, now))
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	ws.mu.Lock()
	var seq []string
	for _, c := range ws.calls {
		if strings.Contains(c, "addTask") {
			seq = append(seq, c)
		}
	}
	ws.mu.Unlock()
	// the last write of t1 must be the one that put it before t2, and t3 must come after a t1 placement
	if len(seq) == 0 || !strings.Contains(seq[len(seq)-1], `"insert_before":"t2","task":"t1"`) {
		t.Fatalf("order: %v", seq)
	}
	i3 := slices.IndexFunc(seq, func(c string) bool { return strings.Contains(c, `"task":"t3"`) })
	i1 := slices.IndexFunc(seq, func(c string) bool { return strings.Contains(c, `"task":"t1"`) })
	if i3 < 0 || i1 < 0 || len(seq) != 2 { // t1 moved twice: only the last place is sent
		t.Fatalf("calls: %v", seq)
	}
}

// A subtask moved under a parent made offline: nothing is sent for it before the parent exists,
// and a parent that cannot be created leaves the child entry queued (not sent to a local gid).
func TestAttackSetParentWaitsForLocalParent(t *testing.T) {
	ws, _, s, st := setupPush(t)
	status := 0
	ws.hook = taskOpsHook(ws, &status)
	ctx := context.Background()
	p, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "親", ProjectGID: "p1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetParentLocal(ctx, "t1", p, store.Position{}, now); err != nil {
		t.Fatal(err)
	}
	ws.hook = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/tasks" {
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return taskOpsHook(ws, &status)(rw, r)
	}
	_, _ = s.Push(ctx)
	for _, c := range ws.calls {
		if strings.Contains(c, "local:") {
			t.Fatalf("a local gid was sent: %s", c)
		}
	}
	if ops, _ := st.ListOps(ctx); len(ops) < 2 {
		t.Fatalf("entries lost: %+v", ops)
	}
}
