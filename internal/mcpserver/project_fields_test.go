package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// update_task add_missing_options adds an option the field does not have, then sets it;
// without it the unknown option is refused and nothing changes.
func TestUpdateTaskAddMissingOptions(t *testing.T) {
	st := seedFields(t)
	cs := connect(t, &Server{Store: st})
	if res := call(t, cs, "update_task", map[string]any{"gid": "t1", "custom_fields": map[string]any{"優先度": "最高"}}, nil); toolError(res) == "" {
		t.Fatal("unknown option accepted")
	}
	var w WriteOutput
	if res := call(t, cs, "update_task", map[string]any{"gid": "t1", "add_missing_options": true,
		"custom_fields": map[string]any{"優先度": "最高", "見積": 2}}, &w); toolError(res) != "" {
		t.Fatalf("error: %s", toolError(res))
	}
	var out GetTaskOutput
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &out)
	if f := fieldByName(t, out.Task, "優先度"); f.Display != "最高" || !f.CanAddOption {
		t.Fatalf("優先度 = %+v", f)
	}
	ops, _ := st.ListOps(context.Background())
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.Kind)
	}
	if strings.Join(kinds, ",") != store.OpCreateEnumOption+","+store.OpSetCustomField+","+store.OpSetCustomField {
		t.Fatalf("ops = %v", kinds)
	}
	// refused before (403): an error, nothing written
	if err := st.SetSyncState(context.Background(), readmodel.KeyFieldOptionsLocked+"cf1", "x"); err != nil {
		t.Fatal(err)
	}
	if res := call(t, cs, "update_task", map[string]any{"gid": "t1", "add_missing_options": true,
		"name": "x", "custom_fields": map[string]any{"優先度": "別"}}, nil); !strings.Contains(toolError(res), "403") {
		t.Fatalf("error = %q", toolError(res))
	}
	if after, _ := st.ListOps(context.Background()); len(after) != len(ops) {
		t.Fatalf("written: %+v", after)
	}
}

// get_project lists the project's fields (and the workspace's others on request);
// update_project_custom_fields adds and removes them, checking every name first.
func TestUpdateProjectCustomFields(t *testing.T) {
	st := seedFields(t)
	ctx := context.Background()
	if err := st.ReplaceWorkspaceCustomFields(ctx, "w1", []asana.CustomField{{GID: "cf1", Name: "優先度", ResourceSubtype: "enum", EnumOptions: []asana.EnumOption{}},
		{GID: "cf9", Name: "メモ", ResourceSubtype: "text"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var p ProjectOutput
	call(t, cs, "get_project", map[string]any{"project": "p1", "include_available_fields": true}, &p)
	if len(p.CustomFields) != 3 || len(p.AvailableCustomFields) != 1 || p.AvailableCustomFields[0].GID != "cf9" {
		t.Fatalf("get_project = %+v / %+v", p.CustomFields, p.AvailableCustomFields)
	}
	for _, args := range []map[string]any{
		{"project": "p1", "add": []string{"メモ"}, "remove": []string{"存在しない"}},
		{"project": "p1", "add": []string{"メモ", "優先度"}}, // already there
		{"project": "p1"},
	} {
		if res := call(t, cs, "update_project_custom_fields", args, nil); toolError(res) == "" {
			t.Fatalf("%v accepted", args)
		}
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("a refused call wrote: %+v", ops)
	}
	var w ProjectWriteOutput
	if res := call(t, cs, "update_project_custom_fields", map[string]any{"project": "p1", "add": []string{"メモ"}, "remove": []string{"見積"}}, &w); toolError(res) != "" {
		t.Fatalf("error: %s", toolError(res))
	}
	p = ProjectOutput{}
	call(t, cs, "get_project", map[string]any{"project": "p1"}, &p)
	var names []string
	for _, f := range p.CustomFields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "優先度,締切,メモ" || p.AvailableCustomFields != nil {
		t.Fatalf("after = %v / %+v", names, p.AvailableCustomFields)
	}
}
