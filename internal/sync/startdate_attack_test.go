package sync

import (
	"context"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// A request that sets a start date must never go out without a due date, and never with
// both due_on and due_at, also when Asana's due date changed after the edit was queued.
func TestPushStartOnAttackNeverInvalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		task   map[string]any // as cached
		server map[string]any // changes in Asana before the push
	}{
		{"due removed in Asana", map[string]any{"due_on": "2026-12-10"}, map[string]any{"due_on": nil}},
		{"due moved before the start", map[string]any{"due_on": "2026-12-10"}, map[string]any{"due_on": "2026-11-20"}},
		{"due became a time", map[string]any{"due_on": "2026-12-10"}, map[string]any{"due_on": "2026-12-10", "due_at": "2026-12-10T03:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, st := setupStart(t, tc.task)
			ctx := context.Background()
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.server {
				f.task[k] = v
			}
			f.task["modified_at"] = "2026-11-14T00:00:00Z"
			res, _ := s.Push(ctx)
			for _, put := range f.puts {
				_, on := put["due_on"]
				_, at := put["due_at"]
				if on && at {
					t.Errorf("both due_on and due_at: %v", put)
				}
				if v, ok := put["start_on"]; ok && v != nil && put["due_on"] == nil && !at {
					t.Errorf("start date sent without a due date: %v", put)
				}
				if v, ok := put["start_on"]; ok && v != nil {
					if d, _ := put["due_on"].(string); d != "" && d < "2026-12-01" {
						t.Errorf("start after due sent: %v", put)
					}
				}
			}
			ops, _ := st.ListOps(ctx)
			t.Logf("push %+v; ops %+v; puts %v", res, ops, f.puts)
		})
	}
}

// Asana's start date changed too: a conflict, not an overwrite.
func TestPushStartOnAttackConflict(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.task["start_on"] = "2026-12-05"
	f.task["modified_at"] = "2026-11-14T00:00:00Z"
	res, _ := s.Push(ctx)
	if res.Sent != 0 || len(f.puts) != 0 {
		t.Fatalf("overwrote a start date set in Asana: %+v %v", res, f.puts)
	}
}

// A 402 on a PUT that does not touch the start date does not hide start dates, a 402
// for one workspace does not hide them in another, and a conflict does not forget one.
func TestPushStartOnAttack402Scope(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.plan402 = true
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "改名"}, now); err != nil {
		t.Fatal(err)
	}
	s.Push(ctx)
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanStartOn, "w1")); v != "" {
		t.Fatalf("a rename's 402 hid start dates: %q", v)
	}
}

// A create with a start date that Asana refuses with 402 records it for the workspace of
// the project, and the task is created without it (the start date is discarded).
func TestPushStartOnAttackCreate402(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.plan402 = true
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "新規", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, _ := s.Push(ctx)
	if res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("a create refused for its start date should be sent without it: %+v", res)
	}
	ws, _ := st.TaskWorkspace(ctx, "t1")
	key := readmodel.PlanKey(readmodel.PlanStartOn, "w1")
	if v, _ := st.SyncState(ctx, key); v == "" {
		t.Fatalf("402 on create not recorded for w1 (t1's workspace %q)", ws)
	}
}
