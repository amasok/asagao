package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
)

// #57: a task's start date is edited locally and queued, with Asana's rules checked first.

func startOf(t *testing.T, s *Store, gid string) string {
	t.Helper()
	d, err := s.GetTask(context.Background(), gid)
	if err != nil {
		t.Fatal(err)
	}
	return d.StartOn
}

func onlyUpdateOp(t *testing.T, s *Store) Op {
	t.Helper()
	ops, err := s.PendingOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Kind != OpUpdateTask {
		t.Fatalf("ops = %+v, want one update_task", ops)
	}
	return ops[0]
}

func TestUpdateTaskLocalStartOn(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t) // t1 is due 2026-11-20
	now := time.Now()
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-15"}, now); err != nil {
		t.Fatal(err)
	}
	if got := startOf(t, s, "t1"); got != "2026-11-15" {
		t.Fatalf("start_on = %q", got)
	}
	op := onlyUpdateOp(t, s)
	if op.Payload["start_on"] != "2026-11-15" || op.Base["start_on"] != "" {
		t.Fatalf("payload %v base %v", op.Payload, op.Base)
	}
	// The same day as the due date is allowed; going back to none removes the entry.
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-20"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	if ops, _ := s.PendingOps(ctx); len(ops) != 0 {
		t.Fatalf("reverted edit left %+v", ops)
	}
}

func TestUpdateTaskLocalStartOnRefused(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, tc := range []struct {
		name    string
		gid     string
		first   map[string]any // applied before (must succeed)
		changes map[string]any
		want    string
	}{
		{"after the due date", "t1", nil, map[string]any{"start_on": "2026-11-21"}, "開始日 (2026-11-21) が期日 (2026-11-20) より後です"},
		{"no due date", "t3", nil, map[string]any{"start_on": "2026-11-01"}, "開始日を付けるには期日も必要です"},
		{"due date moved before it", "t1", map[string]any{"start_on": "2026-11-15"}, map[string]any{"due_on": "2026-11-14"}, "開始日 (2026-11-15) が期日 (2026-11-14) より後です"},
		{"due date cleared, start kept", "t1", map[string]any{"start_on": "2026-11-15"}, map[string]any{"due_on": ""}, "期日だけを消すことはできません"},
		{"both, start after due", "t3", nil, map[string]any{"start_on": "2026-12-02", "due_on": "2026-12-01"}, "より後です"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if tc.first != nil {
				if err := s.UpdateTaskLocal(ctx, tc.gid, tc.first, now); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := s.PendingOps(ctx)
			beforeStart := startOf(t, s, tc.gid)
			err := s.UpdateTaskLocal(ctx, tc.gid, tc.changes, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			after, _ := s.PendingOps(ctx)
			if len(after) != len(before) || (len(after) == 1 && len(after[0].Payload) != len(before[0].Payload)) {
				t.Fatalf("refused edit was queued: %+v", after)
			}
			if got := startOf(t, s, tc.gid); got != beforeStart {
				t.Fatalf("refused edit changed start_on to %q", got)
			}
		})
	}
}

func TestUpdateTaskLocalStartOnWithDue(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	// t3 has no due date: both in one edit.
	if err := s.UpdateTaskLocal(ctx, "t3", map[string]any{"start_on": "2026-12-01", "due_on": "2026-12-05"}, now); err != nil {
		t.Fatal(err)
	}
	// Clearing both together is allowed.
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-15"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "", "due_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetTask(ctx, "t1")
	if d.StartOn != "" || d.DueOn != "" {
		t.Fatalf("t1 start %q due %q", d.StartOn, d.DueOn)
	}
}

func TestUpdateTaskLocalStartOnDueAt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	at := time.Date(2026, 12, 10, 12, 0, 0, 0, time.Local)
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "会議", DueAt: &at,
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t9", map[string]any{"start_on": "2026-12-01"}, time.Now()); err != nil {
		t.Fatalf("due at a time: %v", err)
	}
	if err := s.UpdateTaskLocal(ctx, "t9", map[string]any{"start_on": "2026-12-11"}, time.Now()); err == nil {
		t.Fatal("start after the day of due_at was accepted")
	}
}

func TestUpdateTaskLocalStartOnPlan(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	key := planKey(plan.StartOn, "w1")
	if err := s.RecordPlanRefusal(ctx, plan.StartOn, "w1", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-15"}, now)
	var pe *plan.UnavailableError
	if !errors.As(err, &pe) || pe.Feature != plan.StartOn {
		t.Fatalf("err = %v, want a plan error", err)
	}
	if ops, _ := s.PendingOps(ctx); len(ops) != 0 {
		t.Fatalf("queued: %+v", ops)
	}
	// Another workspace is not affected; and after plan.RecheckAfter it is offered again.
	if err := s.ClearPlanRefusal(ctx, plan.StartOn, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlanRefusal(ctx, plan.StartOn, "w2", now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-15"}, now); err != nil {
		t.Fatalf("w2's 402 hid it in w1: %v", err)
	}
	if err := s.SetSyncState(ctx, key, now.Add(-plan.RecheckAfter-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t2", map[string]any{"start_on": "2026-11-01"}, now); err != nil {
		t.Fatalf("old 402: %v", err)
	}
	// Clearing a start date is always allowed.
	if err := s.SetSyncState(ctx, key, now.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": ""}, now); err != nil {
		t.Fatalf("clearing under a 402: %v", err)
	}
}

func TestCreateTaskLocalStartOn(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	gid, err := s.CreateTaskLocal(ctx, NewTask{Name: "設計", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := startOf(t, s, gid); got != "2026-12-01" {
		t.Fatalf("start_on = %q", got)
	}
	ops, _ := s.PendingOps(ctx)
	if len(ops) != 1 || ops[0].Payload["start_on"] != "2026-12-01" || ops[0].Payload["due_on"] != "2026-12-10" {
		t.Fatalf("ops = %+v", ops)
	}
	// An edit before it is sent goes into the create, and is checked too.
	if err := s.UpdateTaskLocal(ctx, gid, map[string]any{"due_on": ""}, now); err == nil {
		t.Fatal("clearing the due date of a new task with a start date was accepted")
	}
	for _, nt := range []NewTask{
		{Name: "x", ProjectGID: "p1", StartOn: "2026-12-01"},
		{Name: "x", ProjectGID: "p1", StartOn: "2026-12-11", DueOn: "2026-12-10"},
		{Name: "x", ProjectGID: "p1", StartOn: "12/01", DueOn: "2026-12-10"},
	} {
		if _, err := s.CreateTaskLocal(ctx, nt, now); err == nil {
			t.Errorf("%+v was accepted", nt)
		}
	}
	if err := s.RecordPlanRefusal(ctx, plan.StartOn, "w1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTaskLocal(ctx, NewTask{Name: "y", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err == nil {
		t.Fatal("create with a start date under a 402 was accepted")
	}
	if ops, _ := s.PendingOps(ctx); len(ops) != 1 {
		t.Fatalf("refused creates were queued: %d ops", len(ops))
	}
}

// A subtask's workspace is its parent's as TaskWorkspace finds it (the parent's own, else
// its project's), so a 402 recorded for that workspace applies to the subtask too.
func TestCreateSubtaskStartOnPlanWorkspace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t) // t1 has no workspace of its own; its project p1 is in w1
	now := time.Now()
	if err := s.RecordPlanRefusal(ctx, plan.StartOn, "w1", now); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateTaskLocal(ctx, NewTask{Name: "採寸", ParentGID: "t1", DueOn: "2026-11-19", StartOn: "2026-11-18"}, now)
	var pe *plan.UnavailableError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a plan error", err)
	}
	if _, err := s.CreateTaskLocal(ctx, NewTask{Name: "x", ParentGID: "nope"}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown parent: %v", err)
	}
}
