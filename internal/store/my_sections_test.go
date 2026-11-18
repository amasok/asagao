package store

import (
	"context"
	"errors"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
)

// newMyTasksStore: newTestStore with My Tasks sections 今日やる (m1) and あとで (m2) in w1,
// and my task mt in m2 (others' task ot).
func newMyTasksStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.ReplaceMyTaskSections(ctx, "w1", []asana.Section{{GID: "m1", Name: "今日やる"}, {GID: "m2", Name: "あとで"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTasks(ctx, []asana.Task{
		{GID: "mt", Name: "私の", Workspace: &asana.Ref{GID: "w1"}, Assignee: &asana.Ref{GID: "me"},
			AssigneeSection: model.OptionalRef{Set: true, Ref: &asana.Ref{GID: "m2", Name: "あとで"}}},
		{GID: "ot", Name: "他人の", Workspace: &asana.Ref{GID: "w1"}, Assignee: &asana.Ref{GID: "u2"},
			AssigneeSection: model.OptionalRef{Set: true, Ref: &asana.Ref{GID: "x9", Name: "他人のセクション"}}},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMoveMySectionLocal(t *testing.T) {
	s := newMyTasksStore(t)
	ctx := context.Background()
	if secs, _ := s.ListMyTaskSections(ctx, "w1"); len(secs) != 2 {
		t.Fatalf("my sections = %v (another's section must not be added)", secs)
	}
	if err := s.MoveMySectionLocal(ctx, "ot", "m1", testNow); !errors.Is(err, ErrNotMine) {
		t.Errorf("moving another's task: err = %v", err)
	}
	if err := s.MoveMySectionLocal(ctx, "mt", "nope", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown section: err = %v", err)
	}
	if g, err := s.ResolveMySection(ctx, "mt", "今日やる"); err != nil || g != "m1" {
		t.Fatalf("ResolveMySection = %q, %v", g, err)
	}
	if err := s.MoveMySectionLocal(ctx, "mt", "m1", testNow); err != nil {
		t.Fatal(err)
	}
	op := firstOp(t, s, OpMoveMySection)
	if op.Payload["section"] != "m1" || op.Payload["section_name"] != "今日やる" || op.Base["section"] != "m2" {
		t.Fatalf("op = %+v", op)
	}
	// Discarding puts it back.
	if err := s.DiscardOp(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.GetTask(ctx, "mt"); d.MySection == nil || d.MySection.GID != "m2" {
		t.Fatalf("after discard: %+v", d.MySection)
	}
	// Moving there and back leaves nothing to send.
	_ = s.MoveMySectionLocal(ctx, "mt", "m1", testNow)
	_ = s.MoveMySectionLocal(ctx, "mt", "m2", testNow)
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

// A section only known from my tasks keeps its name when the list is read again without it.
func TestReplaceMyTaskSectionsKeepsTaskSections(t *testing.T) {
	s := newMyTasksStore(t)
	ctx := context.Background()
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "mt", Name: "私の", Workspace: &asana.Ref{GID: "w1"}, Assignee: &asana.Ref{GID: "me"},
		AssigneeSection: model.OptionalRef{Set: true, Ref: &asana.Ref{GID: "m3", Name: "古いセクション"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceMyTaskSections(ctx, "w1", []asana.Section{{GID: "m2", Name: "あとで"}, {GID: "m1", Name: "今日やる"}}); err != nil {
		t.Fatal(err)
	}
	secs, _ := s.ListMyTaskSections(ctx, "w1")
	if len(secs) != 3 || secs[0].GID != "m2" || secs[1].GID != "m1" || secs[2].Name != "古いセクション" {
		t.Fatalf("my sections = %v", secs)
	}
	if d, _ := s.GetTask(ctx, "mt"); d.MySection == nil || d.MySection.Name != "古いセクション" {
		t.Errorf("mt = %+v", d.MySection)
	}
	// One no task is in any more goes.
	_ = s.ReplaceMyTaskSections(ctx, "w1", nil)
	_ = s.UpsertTasks(ctx, []asana.Task{{GID: "mt", Name: "私の", Workspace: &asana.Ref{GID: "w1"}, Assignee: &asana.Ref{GID: "me"},
		AssigneeSection: model.OptionalRef{Set: true, Ref: &asana.Ref{GID: "m1", Name: "今日やる"}}}})
	_ = s.ReplaceMyTaskSections(ctx, "w1", []asana.Section{{GID: "m1", Name: "今日やる"}})
	if secs, _ := s.ListMyTaskSections(ctx, "w1"); len(secs) != 1 {
		t.Errorf("my sections = %v", secs)
	}
}

// A task being deleted is not moved in My Tasks either (as for its parent and section).
func TestMoveMySectionRefusesATaskBeingDeleted(t *testing.T) {
	s := newMyTasksStore(t)
	ctx := context.Background()
	if _, err := s.DeleteTaskLocal(ctx, "mt", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveMySectionLocal(ctx, "mt", "m1", testNow); err == nil {
		t.Fatal("moved a task being deleted")
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 1 {
		t.Errorf("outbox %+v", ops)
	}
}
