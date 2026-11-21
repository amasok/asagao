package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/task"
)

// #62: a start date on a milestone (as the cache knows the task) is refused before it is
// queued, from every entrance (they all write through UpdateTaskLocal).
func TestUpdateTaskLocalMilestoneStartRefused(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := newTestStore(t)
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "m1", Name: "リリース", ResourceSubtype: "milestone", DueOn: "2026-12-10",
		Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	for _, changes := range []map[string]any{
		{"start_on": "2026-12-01"},
		{"start_on": "2026-12-01", "due_on": "2026-12-11"},
		{"start_on": "2026-12-01", "name": "リリース 2"},
	} {
		if err := s.UpdateTaskLocal(ctx, "m1", changes, now); !errors.Is(err, task.ErrMilestoneStart) {
			t.Fatalf("UpdateTaskLocal(m1, %v) = %v", changes, err)
		}
	}
	// t1 (due 2026-11-20) made a milestone with a start date in one edit.
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone", "start_on": "2026-11-15"}, now); !errors.Is(err, task.ErrMilestoneStart) {
		t.Fatalf("type and start in one edit: %v", err)
	}
	if ops, _ := s.PendingOps(ctx); len(ops) != 0 {
		t.Fatalf("a refused edit was queued: %+v", ops)
	}
	if got := startOf(t, s, "m1"); got != "" {
		t.Fatalf("m1 start_on = %q", got)
	}
	// Made a milestone locally first (not sent yet): the start date is refused too.
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-15"}, now); !errors.Is(err, task.ErrMilestoneStart) {
		t.Fatalf("start on a local milestone: %v", err)
	}
	// A task with a start date is made a milestone only with the start date cleared.
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "default_task"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-15"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone"}, now); !errors.Is(err, task.ErrMilestoneStart) {
		t.Fatalf("a task with a start date made a milestone: %v", err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone", "start_on": ""}, now); err != nil {
		t.Fatalf("made a milestone with its start date cleared: %v", err)
	}
	// Back to a task with a start date in one edit: allowed.
	if err := s.UpdateTaskLocal(ctx, "m1", map[string]any{"resource_subtype": "default_task", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	if got := startOf(t, s, "m1"); got != "2026-12-01" {
		t.Fatalf("m1 start_on = %q", got)
	}
}
