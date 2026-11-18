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
