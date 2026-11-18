package store

import (
	"context"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

func plan402(t *testing.T, s *Store, ws string, at time.Time) {
	t.Helper()
	if err := s.SetSyncState(context.Background(), readmodel.PlanKey(readmodel.PlanStartOn, ws), at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

// Dates that are not dates never get into an edit or a create.
func TestStartOnAttackBadFormats(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	for _, bad := range []string{"2026-02-30", "2026-1-5", " 2026-11-15", "2026-11-15 ", "2026-11-15T00:00:00Z", "２０２６-11-15", "2026-11-15\n", "0000-00-00", "tomorrow", "2026/11/15"} {
		if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": bad}, now); err == nil {
			t.Errorf("update start_on %q accepted", bad)
		}
		if _, err := s.CreateTaskLocal(ctx, NewTask{Name: "x", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: bad}, now); err == nil {
			t.Errorf("create start_on %q accepted", bad)
		}
	}
	if ops, _ := s.PendingOps(ctx); len(ops) != 0 {
		t.Fatalf("queued: %+v", ops)
	}
}

// An unsent create absorbs edits; each of them is checked against the folded values.
func TestStartOnAttackUnsentCreate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	gid, err := s.CreateTaskLocal(ctx, NewTask{Name: "新規", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, gid, map[string]any{"due_on": "2026-11-30"}, now); err == nil {
		t.Error("due before the folded start accepted")
	}
	if err := s.UpdateTaskLocal(ctx, gid, map[string]any{"start_on": "2026-12-11"}, now); err == nil {
		t.Error("start after the folded due accepted")
	}
	if err := s.UpdateTaskLocal(ctx, gid, map[string]any{"start_on": "2026-12-10"}, now); err != nil {
		t.Errorf("same day: %v", err)
	}
	if err := s.UpdateTaskLocal(ctx, gid, map[string]any{"start_on": "", "due_on": ""}, now); err != nil {
		t.Errorf("both cleared: %v", err)
	}
	ops, _ := s.PendingOps(ctx)
	if len(ops) != 1 || ops[0].Kind != OpCreateTask {
		t.Fatalf("ops = %+v", ops)
	}
	if v, ok := ops[0].Payload["start_on"]; ok && v != "" {
		t.Fatalf("start survived: %v", ops[0].Payload)
	}
	if v, ok := ops[0].Payload["due_on"]; ok && v != "" {
		t.Fatalf("due survived: %v", ops[0].Payload)
	}
	// without a due date a start date is refused
	if err := s.UpdateTaskLocal(ctx, gid, map[string]any{"start_on": "2026-12-01"}, now); err == nil {
		t.Error("start without due accepted")
	}
}

// Several edits fold into one entry that keeps the first base.
func TestStartOnAttackFold(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t) // t1 due 2026-11-20
	now := time.Now()
	for _, d := range []string{"2026-11-10", "2026-11-12", "2026-11-14"} {
		if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": d}, now); err != nil {
			t.Fatal(err)
		}
	}
	op := onlyUpdateOp(t, s)
	if op.Payload["start_on"] != "2026-11-14" || op.Base["start_on"] != "" {
		t.Fatalf("payload %v base %v", op.Payload, op.Base)
	}
	// move due and start together past the old values
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"due_on": "2026-12-31", "start_on": "2026-12-25"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"due_on": "2026-12-24"}, now); err == nil {
		t.Fatal("due before start accepted after a fold")
	}
	if got := startOf(t, s, "t1"); got != "2026-12-25" {
		t.Fatalf("start = %q", got)
	}
}

// A due_at task: the day of the due time counts; clearing due_on alone with a start is refused.
func TestStartOnAttackDueAt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	at := time.Date(2026, 12, 10, 23, 30, 0, 0, time.Local)
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "会議", DueAt: &at,
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t9", map[string]any{"start_on": "2026-12-10"}, now); err != nil {
		t.Fatalf("same day as due_at: %v", err)
	}
	if err := s.UpdateTaskLocal(ctx, "t9", map[string]any{"due_on": ""}, now); err == nil {
		t.Fatal("clearing the due date under a start date was accepted")
	}
	if err := s.UpdateTaskLocal(ctx, "t9", map[string]any{"due_on": "2026-12-09"}, now); err == nil {
		t.Fatal("due before start accepted")
	}
}

// The 402 sign is kept per workspace and the same key is used for a task in a project, a
// subtask and a task of the workspace without a project.
func TestStartOnAttackWorkspaceKeys(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	if err := s.UpsertTasks(ctx, []asana.Task{
		{GID: "m1", Name: "自分のみ", DueOn: "2026-12-10", Assignee: &asana.Ref{GID: "me"}},
		{GID: "m2", Name: "別WS", DueOn: "2026-12-10", Workspace: &asana.Ref{GID: "w2"}},
	}); err != nil {
		t.Fatal(err)
	}
	plan402(t, s, "w1", now)
	sub, err := s.CreateTaskLocal(ctx, NewTask{Name: "子", ParentGID: "t1", DueOn: "2026-12-10"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTaskLocal(ctx, NewTask{Name: "子2", ParentGID: "t1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err == nil {
		t.Error("a subtask of a w1 task got a start date under w1's 402")
	}
	if err := s.UpdateTaskLocal(ctx, sub, map[string]any{"start_on": "2026-12-01"}, now); err == nil {
		t.Error("a new subtask of a w1 task got a start date under w1's 402")
	}
	ws, _ := s.TaskWorkspace(ctx, "m1")
	t.Logf("workspace of a task without project: %q", ws)
	if ws == "w1" {
		if err := s.UpdateTaskLocal(ctx, "m1", map[string]any{"start_on": "2026-12-01"}, now); err == nil {
			t.Error("w1 task without project got a start date")
		}
	}
	if err := s.UpdateTaskLocal(ctx, "m2", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Errorf("w2 task refused by w1's 402: %v", err)
	}
}

// A 402 stamp that is garbage, in the future, or exactly at the recheck border.
func TestStartOnAttackStampEdges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	key := readmodel.PlanKey(readmodel.PlanStartOn, "w1")
	for _, v := range []string{"garbage", "", "0001-01-01T00:00:00Z"} {
		if err := s.SetSyncState(ctx, key, v); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-10"}, now); err != nil {
			t.Errorf("stamp %q hides start dates: %v", v, err)
		}
		if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": ""}, now); err != nil {
			t.Fatal(err)
		}
	}
}
