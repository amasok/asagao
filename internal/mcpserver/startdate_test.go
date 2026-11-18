package mcpserver

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// #57: create_task and update_task take start_on.

func TestUpdateTaskStartOn(t *testing.T) {
	st := seed(t) // t1 is due 2026-11-20
	ctx := context.Background()
	cs := connect(t, &Server{Store: st})
	var w WriteOutput
	if res := call(t, cs, "update_task", map[string]any{"gid": "t1", "start_on": "2026-11-15"}, &w); res.IsError {
		t.Fatalf("update_task: %s", toolError(res))
	}
	d, _ := st.GetTask(ctx, "t1")
	if d.StartOn != "2026-11-15" {
		t.Fatalf("start_on = %q", d.StartOn)
	}
	var got struct {
		Task struct {
			StartOn string `json:"start_on"`
		} `json:"task"`
	}
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &got)
	if got.Task.StartOn != "2026-11-15" {
		t.Errorf("get_task start_on = %q", got.Task.StartOn)
	}

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"gid": "t1", "start_on": "2026-11-21"}, "より後です"},
		{map[string]any{"gid": "t1", "due_on": ""}, "期日だけを消す"},
		{map[string]any{"gid": "t1", "start_on": "someday"}, "start_on"},
		{map[string]any{"gid": "t3", "start_on": "2026-11-01"}, "期日も必要です"},
	} {
		before := take(t, st, "t1")
		if msg := toolError(call(t, cs, "update_task", tc.args, nil)); !strings.Contains(msg, tc.want) {
			t.Errorf("%v: error = %q, want %q", tc.args, msg, tc.want)
		}
		if after := take(t, st, "t1"); !reflect.DeepEqual(before, after) {
			t.Errorf("%v: a refused update changed the store", tc.args)
		}
	}
	// Both together: clearing is allowed.
	if res := call(t, cs, "update_task", map[string]any{"gid": "t1", "start_on": "", "due_on": ""}, nil); res.IsError {
		t.Fatalf("clear both: %s", toolError(res))
	}

	// A recent 402 for the workspace: refused, saying why.
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanStartOn, "w1"), testNow.Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if msg := toolError(call(t, cs, "update_task", map[string]any{"gid": "t2", "start_on": "2026-11-01"}, nil)); !strings.Contains(msg, "タスクの開始日") {
		t.Errorf("under a 402: %q", msg)
	}
}

func TestCreateTaskStartOn(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	cs := connect(t, &Server{Store: st})
	var w WriteOutput
	if res := call(t, cs, "create_task", map[string]any{"name": "設計", "project": "p1", "start_on": "2026-12-01", "due_on": "2026-12-10"}, &w); res.IsError {
		t.Fatalf("create_task: %s", toolError(res))
	}
	if d, _ := st.GetTask(ctx, w.TaskGID); d == nil || d.StartOn != "2026-12-01" {
		t.Fatalf("created: %+v", d)
	}
	ops, _ := st.ListOps(ctx)
	for _, args := range []map[string]any{
		{"name": "x", "start_on": "2026-12-01"},
		{"name": "x", "start_on": "someday", "due_on": "2026-12-10"},
	} {
		if msg := toolError(call(t, cs, "create_task", args, nil)); !strings.HasPrefix(msg, "start_on: ") {
			t.Errorf("%v: error = %q, want a start_on error", args, msg)
		}
	}
	if after, _ := st.ListOps(ctx); len(after) != len(ops) {
		t.Errorf("refused creates were queued")
	}
}
