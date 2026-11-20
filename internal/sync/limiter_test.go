package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// stopLimiter refuses every request, as a limiter whose wait cannot finish does.
type stopLimiter struct{ err error }

func (l stopLimiter) Wait(context.Context) error { return l.err }

// A request the rate limiter of the Asana client held back (#76) was not sent: the entries
// stay pending, not failed, and a create is not checked against Asana. This holds for an
// error that does not wrap ctx's too (*rate.Limiter's "would exceed context deadline").
func TestPushLimiterErrorLeavesEntriesUnsent(t *testing.T) {
	for _, werr := range []error{
		errors.New("rate: Wait(n=1) would exceed context deadline"),
		context.Canceled,
	} {
		t.Run(werr.Error(), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			if _, err := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "新しいタスク"}, now); err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now); err != nil {
				t.Fatal(err)
			}
			s.API.(*asana.Client).Limiter = stopLimiter{werr}
			s.accountOK.Store(true)
			_, _ = s.Push(ctx)
			ops, err := st.ListOps(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(ops) != 2 {
				t.Fatalf("ops = %+v", ops)
			}
			for _, op := range ops {
				if op.State != store.OpPending || op.Verify {
					t.Errorf("%s: state %s, verify %v; want pending, not to be verified", op.Kind, op.State, op.Verify)
				}
			}
			if len(ws.calls) != 0 {
				t.Errorf("nothing may reach Asana: %v", ws.calls)
			}
		})
	}
}
