package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// Custom task types (#59) over MCP: get_task shows the type, its status and the types to
// choose from; update_task takes them by name.
func TestCustomTypeOverMCP(t *testing.T) {
	st := seed(t)
	if err := st.ReplaceProjectCustomTypes(context.Background(), "p1", []asana.CustomType{{GID: "ct1", Name: "バグ", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs1", Name: "対応中", CompletionState: "Incomplete"}, {GID: "cs2", Name: "修正済み", CompletionState: "Complete"}}}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})

	if msg := toolError(call(t, cs, "update_task", map[string]any{"gid": "t1", "custom_type_status": "対応中"}, nil)); !strings.HasPrefix(msg, "custom_type_status: ") {
		t.Fatalf("status on a plain task: %q", msg)
	}
	if msg := toolError(call(t, cs, "update_task", map[string]any{"gid": "t1", "custom_type": "機能"}, nil)); !strings.HasPrefix(msg, "custom_type: ") || !strings.Contains(msg, "バグ") {
		t.Fatalf("unknown type: %q", msg)
	}
	if len(updateOps(t, st)) != 0 {
		t.Fatal("an error queued a change")
	}
	call(t, cs, "update_task", map[string]any{"gid": "t1", "custom_type": "バグ", "custom_type_status": "修正済み"}, nil)
	ops := updateOps(t, st)
	if len(ops) != 1 || ops[0].Payload["resource_subtype"] != "custom" || ops[0].Payload["custom_type"] != "ct1" || ops[0].Payload["custom_type_status_option"] != "cs2" {
		t.Fatalf("ops %+v", ops)
	}
	var out struct {
		Task struct {
			ResourceSubtype string                  `json:"resource_subtype"`
			Completed       bool                    `json:"completed"`
			CustomType      struct{ Name string }   `json:"custom_type"`
			Status          struct{ Name string }   `json:"custom_type_status"`
			CustomTypes     []struct{ Name string } `json:"custom_types"`
		} `json:"task"`
	}
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &out)
	if out.Task.ResourceSubtype != "custom" || out.Task.CustomType.Name != "バグ" || out.Task.Status.Name != "修正済み" || !out.Task.Completed ||
		len(out.Task.CustomTypes) != 1 {
		t.Fatalf("get_task %+v", out.Task)
	}
	// resource_subtype also takes a custom type's name.
	call(t, cs, "update_task", map[string]any{"gid": "t2", "resource_subtype": "バグ"}, nil)
	if ops := updateOps(t, st); len(ops) != 2 || ops[1].Payload["custom_type"] != "ct1" || ops[1].Payload["custom_type_status_option"] != "cs1" {
		t.Fatalf("ops %+v", ops)
	}
}
