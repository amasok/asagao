package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

func mcpOpKinds(t *testing.T, s *store.Store) []string {
	t.Helper()
	ops, err := s.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, op := range ops {
		out = append(out, string(op.Kind)+":"+op.TaskGID)
	}
	return out
}

// #32: delete_task with dry_run tells what goes and changes nothing; without it the delete
// is queued (offline here) and the task leaves the lists.
func TestDeleteTaskTool(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{App: wire.UseCases(st)})
	var out DeleteTaskOutput
	if res := call(t, cs, "delete_task", map[string]any{"gid": "t1", "dry_run": true}, &out); res.IsError {
		t.Fatalf("dry run: %v", res.Content)
	}
	if !out.DryRun || out.Plan.Name != "ソファを買う" || out.Plan.Local {
		t.Errorf("dry run output %+v", out)
	}
	if got := mcpOpKinds(t, st); len(got) != 0 {
		t.Fatalf("queued by a dry run: %v", got)
	}
	out = DeleteTaskOutput{}
	if res := call(t, cs, "delete_task", map[string]any{"gid": "t1"}, &out); res.IsError {
		t.Fatalf("delete: %v", res.Content)
	}
	if out.DryRun || out.Sent || out.Plan.GID != "t1" {
		t.Errorf("output %+v", out)
	}
	if got := mcpOpKinds(t, st); !slices.Equal(got, []string{"delete_task:t1"}) {
		t.Fatalf("outbox %v", got)
	}
	var list struct {
		Tasks []store.TaskSummary `json:"tasks"`
	}
	call(t, cs, "list_tasks", map[string]any{"assignee": "any"}, &list)
	for _, ts := range list.Tasks {
		if ts.GID == "t1" {
			t.Errorf("t1 still listed")
		}
	}
	if res := call(t, cs, "delete_task", map[string]any{"gid": "nope"}, nil); !res.IsError {
		t.Error("an unknown task")
	}
}

func TestSetParentAndMoveToAPlaceTools(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{App: wire.UseCases(st)})
	if res := call(t, cs, "set_parent", map[string]any{"gid": "t2", "parent": "t1"}, nil); res.IsError {
		t.Fatalf("set_parent: %v", res.Content)
	}
	if res := call(t, cs, "move_task", map[string]any{"gid": "t3", "insert_before": "t2"}, nil); res.IsError {
		t.Fatalf("move_task: %v", res.Content)
	}
	for _, bad := range []map[string]any{
		{"gid": "t2", "parent": "t2"},
		{"gid": "t2", "parent": "", "insert_after": "t1"},
	} {
		if res := call(t, cs, "set_parent", bad, nil); !res.IsError {
			t.Errorf("set_parent %v: no error", bad)
		}
	}
	if res := call(t, cs, "move_task", map[string]any{"gid": "t3"}, nil); !res.IsError {
		t.Error("move_task without a section or a place")
	}
	ops, _ := st.ListOps(context.Background())
	if got := mcpOpKinds(t, st); !slices.Equal(got, []string{"set_parent:t2", "move_task:t3"}) {
		t.Fatalf("outbox %v", got)
	}
	if ops[1].Payload["insert_before"] != "t2" || ops[1].Payload["section"] != "s2" {
		t.Errorf("move payload %v", ops[1].Payload)
	}
}
