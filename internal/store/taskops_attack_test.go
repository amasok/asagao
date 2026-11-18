package store

import (
	"context"
	"slices"
	"testing"
	"time"

	asana "github.com/amasok/asagao/internal/asana/model"
)

// A task queued for deletion stays hidden after a full-sync prune.
func TestAttackDeletedTaskStaysHiddenAfterPrune(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneTasks(ctx, map[string]bool{"t2": true}); err != nil {
		t.Fatal(err)
	}
	if got := listed(t, s, TaskFilter{}); slices.Contains(got, "t1") || slices.Contains(got, "t1a") {
		t.Fatalf("hidden task came back: %v", got)
	}
	if got := opKindsOf(t, s); !slices.Contains(got, "delete_task t1") {
		t.Fatalf("delete lost: %v", got)
	}
}

// Discarding a queued delete brings back the task and its subtask.
func TestAttackDiscardDeleteRestores(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	ops, _ := s.ListOps(ctx)
	must(t, s.DiscardOp(ctx, ops[0].ID))
	if got := listed(t, s, TaskFilter{ParentGID: "t1"}); !slices.Equal(got, []string{"t1a"}) {
		t.Fatalf("subtask after discard: %v", got)
	}
}

// Moving a task under a parent whose delete is queued would be deleted with it.
func TestAttackSetParentUnderDeletedTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParentLocal(ctx, "t2", "t1", Position{}, now); err == nil {
		t.Fatal("moved a task under a task being deleted")
	}
}

// Pulling a task whose parent change is queued keeps the queued parent.
func TestAttackPullKeepsQueuedParent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.SetParentLocal(ctx, "t2", "t3", Position{}, now))
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "転居届を出す"}}))
	d, err := s.GetTask(ctx, "t2")
	if err != nil || d.Parent == nil || d.Parent.GID != "t3" {
		t.Fatalf("parent after pull: %+v %v", d.Parent, err)
	}
}

// Cycles through a grandchild, through a queued (not yet pulled) parent change.
func TestAttackCycleThroughQueuedParents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.SetParentLocal(ctx, "t2", "t1a", Position{}, now)) // t1 > t1a > t2
	if err := s.SetParentLocal(ctx, "t1", "t2", Position{}, now); err == nil {
		t.Fatal("cycle t1 under its grandchild accepted")
	}
	if err := s.SetParentLocal(ctx, "t1", "t1a", Position{}, now); err == nil {
		t.Fatal("cycle t1 under its child accepted")
	}
}

// Anchors: a local anchor that is not a sibling, an unknown anchor, both before and after.
func TestAttackSetParentBadAnchors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for name, pos := range map[string]Position{
		"unknown": {Before: "nope"},
		"self":    {After: "t2"},
		"both":    {Before: "t1a", After: "t1a"},
		"nonsib":  {Before: "t3"},
	} {
		if err := s.SetParentLocal(ctx, "t2", "t1", pos, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got := opKindsOf(t, s); len(got) != 0 {
		t.Errorf("rejected calls queued: %v", got)
	}
}

// Deleting something unknown / the same task twice / a sending delete.
func TestAttackDeleteUnknownAndRepeat(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.DeleteTaskLocal(ctx, "zzz", now); err == nil {
		t.Fatal("unknown task deleted")
	}
	for i := 0; i < 3; i++ {
		if _, err := s.DeleteTaskLocal(ctx, "t2", now); err != nil {
			t.Fatal(err)
		}
	}
	if got := opKindsOf(t, s); len(got) != 1 {
		t.Fatalf("outbox %v", got)
	}
}

// ReplaceStories: a story created in the future (clock skew) is kept; one with an empty
// created_at is dropped when absent from the list.
func TestAttackReplaceStoriesEmptyCreatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{{GID: "c9", Type: "comment", Text: "x"}}, now))
	must(t, s.ReplaceStories(ctx, "t1", nil, now.Add(time.Hour)))
	if got := storyGIDs(t, s, "t1"); len(got) != 0 {
		t.Fatalf("stale story kept: %v", got)
	}
}
