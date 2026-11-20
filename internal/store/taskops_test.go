package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	asana "github.com/amasok/asagao/internal/asana/model"
)

var now = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func listed(t *testing.T, s *Store, f TaskFilter) []string {
	t.Helper()
	ts, err := s.ListTasks(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return gids(ts)
}

func opKindsOf(t *testing.T, s *Store) []string {
	t.Helper()
	ops, err := s.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, op := range ops {
		out = append(out, string(op.Kind)+" "+op.TaskGID)
	}
	return out
}

// #32: a queued delete hides the task and its subtasks until it is sent; discarding it
// brings them back, with the changes queued for them.
func TestDeleteTaskLocalHidesUntilSent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1a", map[string]any{"name": "測り直す"}, now))
	local, err := s.CreateTaskLocal(ctx, NewTask{Name: "オフラインの子", ParentGID: "t1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanDeleteTask(ctx, "t1")
	if err != nil || plan.Name != "ソファを買う" || plan.Subtasks != 2 || plan.Queued != 2 || plan.Local {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if ops := opKindsOf(t, s); len(ops) != 2 {
		t.Fatalf("PlanDeleteTask wrote: %v", ops)
	}
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	// the subtask made offline is gone at once; the edit of t1a waits with the delete
	if got := opKindsOf(t, s); !slices.Equal(got, []string{"update_task t1a", "delete_task t1"}) {
		t.Fatalf("outbox %v", got)
	}
	if _, err := s.GetTask(ctx, local); !errors.Is(err, ErrNotFound) {
		t.Errorf("local subtask: %v", err)
	}
	for _, f := range []TaskFilter{{}, {ProjectGID: "p1"}, {ParentGID: "t1"}, {Query: "ソファ"}} {
		if got := listed(t, s, f); slices.Contains(got, "t1") || slices.Contains(got, "t1a") {
			t.Errorf("listed %+v: %v", f, got)
		}
	}
	if got := listed(t, s, TaskFilter{ProjectGID: "p1"}); !slices.Equal(got, []string{"t2", "t3"}) {
		t.Errorf("others: %v", got)
	}
	// a pull does not bring it back
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}))
	if got := listed(t, s, TaskFilter{}); slices.Contains(got, "t1") {
		t.Errorf("after a pull: %v", got)
	}
	// deleting again changes nothing
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	ops, _ := s.ListOps(ctx)
	if len(ops) != 2 {
		t.Fatalf("outbox %+v", ops)
	}
	must(t, s.DiscardOp(ctx, ops[1].ID))
	if got := listed(t, s, TaskFilter{}); !slices.Contains(got, "t1") || !slices.Contains(got, "t1a") {
		t.Errorf("after discarding: %v", got)
	}
	if d, _ := s.GetTask(ctx, "t1a"); d.Name != "測り直す" {
		t.Errorf("the queued edit is lost: %q", d.Name)
	}
}

func TestCompleteDeleteRemovesTheTree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1a", map[string]any{"name": "測り直す"}, now))
	must(t, s.SetDependencyLocal(ctx, "t2", "t1a", true, now))
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	ops, _ := s.ListOps(ctx)
	must(t, s.CompleteDelete(ctx, ops[2].ID, "t1"))
	for _, g := range []string{"t1", "t1a"} {
		if _, err := s.GetTask(ctx, g); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", g, err)
		}
	}
	// the dependency of t2 on t1a went with it (Asana drops it); t2's own change stays queued
	if got := opKindsOf(t, s); !slices.Equal(got, []string{"add_dependency t2"}) {
		t.Errorf("outbox %v", got)
	}
	if d, _ := s.GetTask(ctx, "t2"); len(d.Dependencies) != 0 {
		t.Errorf("dependencies %+v", d.Dependencies)
	}
}

func TestDeleteLocalTaskIsImmediate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	gid, err := s.CreateTaskLocal(ctx, NewTask{Name: "下書き", ProjectGID: "p1", SectionGID: "s1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTaskLocal(ctx, NewTask{Name: "その子", ParentGID: gid}, now); err != nil {
		t.Fatal(err)
	}
	p, err := s.DeleteTaskLocal(ctx, gid, now)
	if err != nil || !p.Local || p.Subtasks != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if got := opKindsOf(t, s); len(got) != 0 {
		t.Errorf("outbox %v", got)
	}
	if _, err := s.GetTask(ctx, gid); !errors.Is(err, ErrNotFound) {
		t.Errorf("still there: %v", err)
	}
	if _, err := s.DeleteTaskLocal(ctx, "nope", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
}

func TestSetParentLocal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	num := func(g string) int {
		d, err := s.GetTask(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		return d.NumSubtasks
	}
	before1, before2 := num("t1"), num("t2")
	must(t, s.SetParentLocal(ctx, "t1a", "t2", Position{}, now))
	if d, _ := s.GetTask(ctx, "t1a"); d.ParentGID != "t2" {
		t.Fatalf("parent %q", d.ParentGID)
	}
	if num("t1") != max(0, before1-1) || num("t2") != before2+1 {
		t.Errorf("subtask counts %d %d", num("t1"), num("t2"))
	}
	if got := listed(t, s, TaskFilter{ParentGID: "t2"}); !slices.Equal(got, []string{"t1a"}) {
		t.Errorf("subtasks of t2: %v", got)
	}
	// a pull of the old state keeps the local change
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1a", Name: "サイズを測る", Parent: &asana.Ref{GID: "t1"}}}))
	if d, _ := s.GetTask(ctx, "t1a"); d.ParentGID != "t2" {
		t.Errorf("after a pull: %q", d.ParentGID)
	}
	// another change joins the entry; then a top-level task
	must(t, s.SetParentLocal(ctx, "t1a", "t3", Position{}, now))
	must(t, s.SetParentLocal(ctx, "t1a", "", Position{}, now))
	ops, _ := s.ListOps(ctx)
	if len(ops) != 1 || ops[0].Payload["parent"] != "" || ops[0].Base["parent"] != "t1" {
		t.Fatalf("outbox %+v", ops)
	}
	// discarding puts it back under t1
	must(t, s.DiscardOp(ctx, ops[0].ID))
	if d, _ := s.GetTask(ctx, "t1a"); d.ParentGID != "t1" {
		t.Errorf("after discarding: %q", d.ParentGID)
	}
	// back where it started: nothing to send
	must(t, s.SetParentLocal(ctx, "t1a", "t2", Position{}, now))
	must(t, s.SetParentLocal(ctx, "t1a", "t1", Position{}, now))
	if got := opKindsOf(t, s); len(got) != 0 {
		t.Errorf("outbox %v", got)
	}
	// a place among the subtasks is sent even under the same parent
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1b", Name: "色を選ぶ", Parent: &asana.Ref{GID: "t1"}}}))
	must(t, s.SetParentLocal(ctx, "t1a", "t1", Position{After: "t1b"}, now))
	ops, _ = s.ListOps(ctx)
	if len(ops) != 1 || ops[0].Payload["insert_after"] != "t1b" {
		t.Errorf("outbox %+v", ops)
	}
}

func TestSetParentLocalRefuses(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for name, f := range map[string]func() error{
		"itself":            func() error { return s.SetParentLocal(ctx, "t1", "t1", Position{}, now) },
		"under its subtask": func() error { return s.SetParentLocal(ctx, "t1", "t1a", Position{}, now) },
		"unknown task":      func() error { return s.SetParentLocal(ctx, "nope", "t1", Position{}, now) },
		"unknown parent":    func() error { return s.SetParentLocal(ctx, "t2", "nope", Position{}, now) },
		"anchor elsewhere":  func() error { return s.SetParentLocal(ctx, "t2", "t1", Position{Before: "t3"}, now) },
		"both places":       func() error { return s.SetParentLocal(ctx, "t2", "t1", Position{Before: "t1a", After: "t1a"}, now) },
		"place, no parent":  func() error { return s.SetParentLocal(ctx, "t1a", "", Position{Before: "t2"}, now) },
	} {
		if err := f(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if got := opKindsOf(t, s); len(got) != 0 {
		t.Errorf("outbox %v", got)
	}
}

func TestMoveTaskAtLocal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// within its own section, with a place: queued
	must(t, s.MoveTaskAtLocal(ctx, "t2", "p1", "s2", Position{After: "t3"}, now))
	ops, _ := s.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != OpMoveTask || ops[0].Payload["insert_after"] != "t3" || ops[0].Payload["section"] != "s2" {
		t.Fatalf("outbox %+v", ops)
	}
	// the next move says where; without a place it drops the old one
	must(t, s.MoveTaskAtLocal(ctx, "t2", "p1", "s1", Position{Before: "t1"}, now))
	must(t, s.MoveTaskLocal(ctx, "t2", "p1", "s1", now)) // same section, no place: nothing new
	ops, _ = s.ListOps(ctx)
	if len(ops) != 1 || ops[0].Payload["insert_before"] != "t1" || ops[0].Payload["insert_after"] != nil {
		t.Fatalf("outbox %+v", ops)
	}
	if p, sec, err := s.ResolveMoveNear(ctx, "t3", "", "t1"); p != "p1" || sec != "s1" || err != nil {
		t.Errorf("ResolveMoveNear = %s %s %v", p, sec, err)
	}
	if _, _, err := s.ResolveMoveNear(ctx, "t3", "", "t4"); err == nil {
		t.Error("an anchor in another project")
	}
	for name, pos := range map[string]Position{
		"anchor in another section": {Before: "t3"},
		"the task itself":           {Before: "t2"},
		"both":                      {Before: "t1", After: "t1"},
	} {
		if err := s.MoveTaskAtLocal(ctx, "t2", "p1", "s1", pos, now); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A task being deleted (or under one) is not a parent, a place, or moved: Asana would
// delete what goes under it.
func TestTaskOpsRefuseTasksBeingDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func() error{
		"under it":              func() error { return s.SetParentLocal(ctx, "t2", "t1", Position{}, now) },
		"under its subtask":     func() error { return s.SetParentLocal(ctx, "t2", "t1a", Position{}, now) },
		"its subtask moved out": func() error { return s.SetParentLocal(ctx, "t1a", "t2", Position{}, now) },
		"next to it":            func() error { return s.MoveTaskAtLocal(ctx, "t2", "p1", "s1", Position{Before: "t1"}, now) },
		"it moved":              func() error { return s.MoveTaskAtLocal(ctx, "t1", "p1", "s2", Position{}, now) },
	} {
		if err := f(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if got := opKindsOf(t, s); len(got) != 1 {
		t.Errorf("outbox %v", got)
	}
}

// A move (or parent change) to a place goes after the changes queued since: its place is
// next to a task that may move in them.
func TestMoveToAPlaceGoesToTheEndOfTheQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.MoveTaskLocal(ctx, "t2", "p1", "s1", now))                          // t2 to 家具
	must(t, s.MoveTaskAtLocal(ctx, "t3", "p1", "s1", Position{After: "t2"}, now)) // t3 after t2
	must(t, s.MoveTaskAtLocal(ctx, "t2", "p1", "s1", Position{After: "t3"}, now)) // then t2 after t3
	ops, _ := s.ListOps(ctx)
	if got := opKindsOf(t, s); !slices.Equal(got, []string{"move_task t3", "move_task t2"}) {
		t.Fatalf("outbox %v", got)
	}
	if ops[1].Base["section"] != "s2" || ops[1].Payload["insert_after"] != "t3" {
		t.Errorf("requeued entry %+v", ops[1])
	}
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t2b", Name: "子", Parent: &asana.Ref{GID: "t1"}}}))
	must(t, s.SetParentLocal(ctx, "t2", "t1", Position{}, now))
	must(t, s.SetParentLocal(ctx, "t3", "t1", Position{After: "t2"}, now))
	must(t, s.SetParentLocal(ctx, "t2", "t1", Position{After: "t3"}, now))
	if got := opKindsOf(t, s); !slices.Equal(got[2:], []string{"set_parent t3", "set_parent t2"}) {
		t.Fatalf("outbox %v", got)
	}
}

// A parent made offline that goes away (its create discarded, or dropped as gone) takes
// the parent changes to it along: the tasks go back to their parents and stay.
func TestParentCreatedOfflineGoesAway(t *testing.T) {
	for _, how := range []string{"discard", "dropped"} {
		t.Run(how, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			parent, err := s.CreateTaskLocal(ctx, NewTask{Name: "親", ProjectGID: "p1", SectionGID: "s1"}, now)
			if err != nil {
				t.Fatal(err)
			}
			must(t, s.SetParentLocal(ctx, "t1a", parent, Position{}, now))
			ops, _ := s.ListOps(ctx)
			if how == "discard" {
				must(t, s.DiscardOp(ctx, ops[0].ID))
			} else if _, err := s.DiscardDeletedTask(ctx, parent, "gone", now); err != nil {
				t.Fatal(err)
			}
			d, err := s.GetTask(ctx, "t1a")
			if err != nil || d.ParentGID != "t1" {
				t.Fatalf("t1a: %+v %v", d, err)
			}
			if got := opKindsOf(t, s); len(got) != 0 {
				t.Errorf("outbox %v", got)
			}
		})
	}
}

// A pull of the task keeps a queued parent change, and does not count it twice.
func TestPullKeepsQueuedParentAndCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.SetParentLocal(ctx, "t1a", "t2", Position{}, now))
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1a", Name: "サイズを測る", Parent: &asana.Ref{GID: "t1"}}}))
	d, _ := s.GetTask(ctx, "t2")
	if d.NumSubtasks != 1 {
		t.Errorf("t2 subtasks %d", d.NumSubtasks)
	}
}
