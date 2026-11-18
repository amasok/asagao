package sync

import (
	"context"
	"errors"
	"net/http"
	"testing"

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
