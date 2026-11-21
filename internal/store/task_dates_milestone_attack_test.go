package store

import (
	"context"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

// #62: a task whose type the cache does not know is not refused a start date; the type
// edit alone on a task that has one is not refused either (only a start date edit is).
func TestAttackMilestoneStartUnknownType(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := newTestStore(t)
	if err := s.UpsertTasks(ctx, []asana.Task{
		{GID: "u1", Name: "不明", DueOn: "2026-12-10", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "s1", Name: "開始あり", DueOn: "2026-12-10", StartOn: "2026-12-01", ResourceSubtype: "default_task", Workspace: &asana.Ref{GID: "w1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "u1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatalf("unknown type: %v", err)
	}
	if err := s.UpdateTaskLocal(ctx, "s1", map[string]any{"resource_subtype": "milestone"}, now); err != nil {
		t.Fatalf("type edit alone: %v", err)
	}
	// clearing the start date of a milestone, with a due date change
	if err := s.UpdateTaskLocal(ctx, "s1", map[string]any{"start_on": "", "due_on": "2026-12-11"}, now); err != nil {
		t.Fatalf("clear on a milestone: %v", err)
	}
}
