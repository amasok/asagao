package mcpserver

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// get_task shows a custom task type (#59), its status and the types of its projects.
func TestGetTaskCustomType(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	if err := st.ReplaceProjectCustomTypes(ctx, "p1", []asana.CustomType{{GID: "ct1", Name: "バグ", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs1", Name: "対応中", CompletionState: "Incomplete"}, {GID: "cs2", Name: "修正済み", CompletionState: "Complete"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}},
		CustomType:  &asana.Ref{GID: "ct1"}, CustomTypeStatusOption: &asana.Ref{GID: "cs2"}}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var out struct {
		Task struct {
			ResourceSubtype string                `json:"resource_subtype"`
			CustomType      struct{ Name string } `json:"custom_type"`
			Status          struct {
				Name     string
				Complete bool
			} `json:"custom_type_status"`
			CustomTypes []struct {
				Name     string
				Statuses []struct{ Name string }
			} `json:"custom_types"`
		} `json:"task"`
	}
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &out)
	if out.Task.ResourceSubtype != "custom" || out.Task.CustomType.Name != "バグ" || out.Task.Status.Name != "修正済み" || !out.Task.Status.Complete ||
		len(out.Task.CustomTypes) != 1 || len(out.Task.CustomTypes[0].Statuses) != 2 {
		t.Fatalf("get_task %+v", out.Task)
	}
}
