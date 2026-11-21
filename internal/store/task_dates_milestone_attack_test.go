package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/task"
)

// #62: the cache's type unknown: a start date is let through, but a change to a milestone
// is judged; a task with a start date is not made a milestone without clearing it; the other
// edits of a milestone (and clearing its start date) pass.
func TestAttackMilestoneStartUnknownType(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := newTestStore(t)
	if err := s.UpsertTasks(ctx, []asana.Task{
		{GID: "u1", Name: "不明", DueOn: "2026-12-10", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "u2", Name: "不明で開始あり", DueOn: "2026-12-10", StartOn: "2026-12-01", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "s1", Name: "開始あり", DueOn: "2026-12-10", StartOn: "2026-12-01", ResourceSubtype: "default_task", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "m1", Name: "開始つきマイルストーン", DueOn: "2026-12-10", StartOn: "2026-12-01", ResourceSubtype: "milestone", Workspace: &asana.Ref{GID: "w1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "u1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatalf("unknown type, start date: %v", err)
	}
	for _, c := range []struct {
		gid string
		ch  map[string]any
	}{
		{"u2", map[string]any{"resource_subtype": "milestone"}},
		{"s1", map[string]any{"resource_subtype": "milestone"}},
		{"s1", map[string]any{"resource_subtype": "milestone", "due_on": "2026-12-11"}},
		{"s1", map[string]any{"resource_subtype": "milestone", "start_on": "2026-12-02"}},
		{"u1", map[string]any{"resource_subtype": "milestone", "name": "x"}}, // start 12-01 queued above
	} {
		if err := s.UpdateTaskLocal(ctx, c.gid, c.ch, now); !errors.Is(err, task.ErrMilestoneStart) {
			t.Errorf("%s %v: err = %v", c.gid, c.ch, err)
		}
	}
	for _, c := range []struct {
		gid string
		ch  map[string]any
	}{
		{"s1", map[string]any{"resource_subtype": "milestone", "start_on": ""}},
		{"m1", map[string]any{"name": "改名"}},
		{"m1", map[string]any{"due_on": "2026-12-12"}},
		{"m1", map[string]any{"start_on": ""}},
		{"u2", map[string]any{"resource_subtype": "default_task"}},
	} {
		if err := s.UpdateTaskLocal(ctx, c.gid, c.ch, now); err != nil {
			t.Errorf("%s %v: %v", c.gid, c.ch, err)
		}
	}
	if got := startOf(t, s, "s1"); got != "" {
		t.Errorf("s1 start_on = %q", got)
	}
}
