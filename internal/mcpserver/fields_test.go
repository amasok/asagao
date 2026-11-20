package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"reflect"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// seedFields gives project p1 the fields 優先度 (enum 高/中), 見積 (number) and 締切 (date).
func seedFields(t *testing.T) *store.Store {
	t.Helper()
	st := seed(t)
	ctx := context.Background()
	on := true
	err := st.ReplaceProjectCustomFields(ctx, "p1", []asana.CustomFieldSetting{
		{GID: "s1", IsImportant: true, CustomField: asana.CustomField{GID: "cf1", Name: "優先度", ResourceSubtype: "enum",
			EnumOptions: []asana.EnumOption{{GID: "o1", Name: "高", Enabled: &on}, {GID: "o2", Name: "中", Enabled: &on}}}},
		{GID: "s2", CustomField: asana.CustomField{GID: "cf2", Name: "見積", ResourceSubtype: "number", Precision: 1}},
		{GID: "s3", CustomField: asana.CustomField{GID: "cf3", Name: "締切", ResourceSubtype: "date"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := func(gid string) *asana.Ref { return &asana.Ref{GID: gid} }
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", Workspace: ref("w1"), Assignee: &asana.Ref{GID: "me", Name: "Me"},
		Memberships: []asana.Membership{{Project: ref("p1"), Section: ref("s1")}},
		CustomFields: []asana.CustomField{{GID: "cf1", Name: "優先度", ResourceSubtype: "enum"},
			{GID: "cf2", Name: "見積", ResourceSubtype: "number", Precision: 1}, {GID: "cf3", Name: "締切", ResourceSubtype: "date"}}}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func fieldByName(t *testing.T, d *store.TaskDetail, name string) store.CustomFieldView {
	t.Helper()
	for _, f := range d.CustomFields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no field %s in %+v", name, d.CustomFields)
	return store.CustomFieldView{}
}

// update_task with any bad part (an unknown option, a non-number, an unknown field) changes
// nothing at all, not even the valid parts given with it.
func TestUpdateTaskCustomFieldsAllOrNothing(t *testing.T) {
	st := seedFields(t)
	cs := connect(t, &Server{App: wire.UseCases(st)})
	before := take(t, st, "t1")
	for _, args := range []map[string]any{
		{"gid": "t1", "name": "新しい名前", "custom_fields": map[string]any{"優先度": "最高"}},
		{"gid": "t1", "custom_fields": map[string]any{"優先度": "高", "見積": "たくさん"}},
		{"gid": "t1", "custom_fields": map[string]any{"優先度": "高", "存在しない": "x"}},
		{"gid": "t1", "add_dependencies": []string{"t2"}, "custom_fields": map[string]any{"締切": "そのうち"}},
	} {
		res := call(t, cs, "update_task", args, nil)
		if msg := toolError(res); msg == "" {
			t.Fatalf("update_task(%v) should fail", args)
		}
		if after := take(t, st, "t1"); !reflect.DeepEqual(before, after) {
			t.Fatalf("a failed update_task(%v) changed the task:\nbefore %+v\nafter  %+v", args, before, after)
		}
	}
}

// Fields are set by name (enum by option name), shown by get_task, and queued offline.
func TestUpdateTaskCustomFieldsByName(t *testing.T) {
	st := seedFields(t)
	cs := connect(t, &Server{App: wire.UseCases(st)})
	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t1", "custom_fields": map[string]any{"優先度": "中", "見積": 3.5, "締切": "2026-12-01"}}, &w)
	if w.Sent {
		t.Fatalf("offline: %+v", w)
	}
	var out GetTaskOutput
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &out)
	if f := fieldByName(t, out.Task, "優先度"); f.Display != "中" || len(f.Options) != 2 || !f.Editable {
		t.Fatalf("優先度 = %+v", f)
	}
	if f := fieldByName(t, out.Task, "見積"); !strings.HasPrefix(f.Display, "3.5") {
		t.Fatalf("見積 = %+v", f)
	}
	if f := fieldByName(t, out.Task, "締切"); !strings.Contains(f.Display, "2026") {
		t.Fatalf("締切 = %+v", f)
	}
	ops, _ := st.ListOps(context.Background())
	if len(ops) != 3 {
		t.Fatalf("one queued change per field: %+v", ops)
	}
	// list_tasks filters by field value.
	var list struct {
		Tasks []store.TaskSummary `json:"tasks"`
	}
	call(t, cs, "list_tasks", map[string]any{"field": "優先度=中"}, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].GID != "t1" {
		t.Fatalf("filter 優先度=中: %+v", list.Tasks)
	}
	call(t, cs, "list_tasks", map[string]any{"field": "優先度=高"}, &list)
	if len(list.Tasks) != 0 {
		t.Fatalf("filter 優先度=高: %+v", list.Tasks)
	}
	// Clearing with null.
	call(t, cs, "update_task", map[string]any{"gid": "t1", "custom_fields": map[string]any{"優先度": nil}}, &w)
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &out)
	if f := fieldByName(t, out.Task, "優先度"); f.Value != nil || f.Display != "" {
		t.Fatalf("cleared 優先度 = %+v", f)
	}
}
