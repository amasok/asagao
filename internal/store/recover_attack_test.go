package store

import (
	"context"
	"testing"
)

// RecoverInterruptedOps: an entry sent when the process died is verified first unless
// sending it twice is harmless (domain/outbox.SafeToResend), looked up in the store for a
// local tag.
func TestRecoverInterruptedOpsVerifyDecision(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	upd := queueUpdate(t, s)
	createdTag, err := s.AddTagLocal(ctx, "t1", "", "created", testNow)
	if err != nil {
		t.Fatal(err)
	}
	uncreatedTag, err := s.AddTagLocal(ctx, "t1", "", "uncreated", testNow)
	if err != nil {
		t.Fatal(err)
	}
	comment, err := s.AddCommentLocal(ctx, "t1", "hi", UserRef{}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	// the tag "created" now has a real gid recorded
	if _, err := s.db.ExecContext(ctx, "INSERT INTO sync_state(key, value) VALUES(?, ?)", ResultTag+createdTag, "999"); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]bool{} // id -> verify
	for _, op := range ops {
		switch {
		case op.ID == upd:
			want[op.ID] = false
		case op.Kind == OpAddTag && op.Payload["tag"] == createdTag:
			want[op.ID] = false
		case op.Kind == OpAddTag && op.Payload["tag"] == uncreatedTag:
			want[op.ID] = true
		case op.Kind == OpAddComment && op.TaskGID == "t1" && comment != "":
			want[op.ID] = true
		default:
			continue
		}
		if ok, err := s.BeginSend(ctx, op.ID, testNow); err != nil || !ok {
			t.Fatalf("BeginSend(%d) = %v, %v", op.ID, ok, err)
		}
	}
	requeued, verify, err := s.RecoverInterruptedOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if requeued != 2 || verify != 2 {
		t.Errorf("requeued, verify = %d, %d; want 2, 2", requeued, verify)
	}
	for id, v := range want {
		op, err := s.GetOp(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.State != OpPending || op.Verify != v {
			t.Errorf("op %d (%s): state %s verify %v, want pending / %v", id, op.Kind, op.State, op.Verify, v)
		}
	}
}

func queueUpdate(t *testing.T, s *Store) int64 {
	t.Helper()
	if err := s.UpdateTaskLocal(context.Background(), "t1", map[string]any{"name": "ローカル"}, testNow); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOps(context.Background())
	if err != nil || len(ops) == 0 {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Kind == OpUpdateTask {
			return op.ID
		}
	}
	t.Fatal("no update op")
	return 0
}
