package mcpserver

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/wire"
)

// #62: update_task refuses a start date on a milestone with the reason, and changes nothing.
func TestAttackMCPMilestoneStartRefused(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "m1", Name: "リリース", ResourceSubtype: "milestone", DueOn: "2026-12-10",
		Assignee: &asana.Ref{GID: "me"}, Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{App: wire.UseCases(st)})
	for _, args := range []map[string]any{
		{"gid": "m1", "start_on": "2026-12-01"},
		{"gid": "m1", "start_on": "2026-12-01", "name": "x"},
		{"gid": "t1", "start_on": "2026-11-15", "resource_subtype": "milestone"},
	} {
		gid, _ := args["gid"].(string)
		before := take(t, st, gid)
		if msg := toolError(call(t, cs, "update_task", args, nil)); !strings.Contains(msg, "マイルストーン") {
			t.Errorf("%v: error = %q", args, msg)
		}
		if after := take(t, st, gid); !reflect.DeepEqual(before, after) {
			t.Errorf("%v: a refused update changed the store", args)
		}
	}
	// clearing, and clearing with the type change, go through
	for _, args := range []map[string]any{
		{"gid": "m1", "start_on": ""},
		{"gid": "t1", "resource_subtype": "milestone", "start_on": ""},
	} {
		if res := call(t, cs, "update_task", args, nil); res.IsError {
			t.Errorf("%v: %s", args, toolError(res))
		}
	}
	// and back to a task with a start date in the same call
	if res := call(t, cs, "update_task", map[string]any{"gid": "m1", "resource_subtype": "default_task", "start_on": "2026-12-01"}, nil); res.IsError {
		t.Errorf("type back with a start date: %s", toolError(res))
	}
}
