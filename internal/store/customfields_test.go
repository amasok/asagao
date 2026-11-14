package store

import (
	"context"
	"testing"

	"github.com/amasok/asana-tui/internal/asana"
)

func strp(s string) *string { return &s }

// priorityField is an enum field "優先度" with options 高 / 中 / 低 (低 disabled).
func priorityField(value string) asana.CustomField {
	f := asana.CustomField{GID: "cf1", Name: "優先度", ResourceSubtype: CFEnum}
	if value != "" {
		f.EnumValue = &asana.EnumOption{GID: value, Name: map[string]string{"o1": "高", "o2": "中"}[value], Color: "red"}
		f.DisplayValue = strp(f.EnumValue.Name)
	}
	return f
}

func setupFields(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	no := false
	must(t, s.ReplaceProjectCustomFields(ctx, "p1", []asana.CustomFieldSetting{
		{IsImportant: true, CustomField: asana.CustomField{GID: "cf1", Name: "優先度", ResourceSubtype: CFEnum, EnumOptions: []asana.EnumOption{
			{GID: "o1", Name: "高", Color: "red"}, {GID: "o2", Name: "中"}, {GID: "o3", Name: "低", Enabled: &no}}}},
		{CustomField: asana.CustomField{GID: "cf2", Name: "見積もり", ResourceSubtype: CFNumber, Precision: 1}},
		{CustomField: asana.CustomField{GID: "cf3", Name: "計算", ResourceSubtype: CFNumber, IsFormulaField: true}},
	}))
	n := 2.0
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", Assignee: &asana.Ref{GID: "me"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}},
		CustomFields: []asana.CustomField{priorityField("o1"),
			{GID: "cf2", Name: "見積もり", ResourceSubtype: CFNumber, Precision: 1, NumberValue: &n, DisplayValue: strp("2.0")}}}}))
	return s
}

func fieldOf(t *testing.T, s *Store, task, field string) CustomFieldView {
	t.Helper()
	d, err := s.GetTask(context.Background(), task)
	must(t, err)
	for _, f := range d.CustomFields {
		if f.GID == field {
			return f
		}
	}
	t.Fatalf("task %s has no field %s: %+v", task, field, d.CustomFields)
	return CustomFieldView{}
}

func TestCustomFieldsStoredAndListed(t *testing.T) {
	s := setupFields(t)
	ctx := context.Background()
	f := fieldOf(t, s, "t1", "cf1")
	if f.Value != "o1" || f.Display != "高" || !f.Important || !f.Editable || f.Color != "red" {
		t.Fatalf("priority = %+v", f)
	}
	if len(f.Options) != 2 { // the disabled option is not offered
		t.Fatalf("options = %+v", f.Options)
	}
	// a project field without a value on the task is listed too, and a formula is read-only
	if calc := fieldOf(t, s, "t1", "cf3"); calc.Editable || calc.Value != nil {
		t.Fatalf("formula = %+v", calc)
	}
	tasks, err := s.ListTasks(ctx, TaskFilter{Field: "優先度", FieldValue: "高"})
	must(t, err)
	if len(tasks) != 1 || tasks[0].GID != "t1" || len(tasks[0].CustomFields) != 2 {
		t.Fatalf("filtered = %+v", tasks)
	}
	if tasks, _ := s.ListTasks(ctx, TaskFilter{Field: "優先度", FieldValue: "中"}); len(tasks) != 0 {
		t.Fatalf("filter by 中 = %+v", tasks)
	}
}

func TestSetCustomFieldQueuesMergesAndReverts(t *testing.T) {
	s := setupFields(t)
	ctx := context.Background()
	v, err := s.ParseCustomFieldValue(ctx, "cf1", "中", testNow)
	must(t, err)
	display, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", v, testNow)
	must(t, err)
	if display != "中" || fieldOf(t, s, "t1", "cf1").Display != "中" {
		t.Fatalf("display = %q", display)
	}
	op := onlyOp(t, s)
	if op.Kind != OpSetCustomField || op.Payload["value"] != "o2" || op.Base["value"] != "o1" {
		t.Fatalf("op = %+v", op)
	}
	if got := DescribeChange(op); got != "「優先度」を「中」に変更" {
		t.Fatalf("describe = %q", got)
	}
	// a pull keeps the unsent value
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", CustomFields: []asana.CustomField{priorityField("o1")}}}))
	if f := fieldOf(t, s, "t1", "cf1"); f.Value != "o2" {
		t.Fatalf("after pull = %+v", f)
	}
	// back to the server value: nothing to send
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf1", "o1", testNow)
	must(t, err)
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestDiscardCustomFieldRestoresBase(t *testing.T) {
	s := setupFields(t)
	ctx := context.Background()
	_, err := s.SetCustomFieldLocal(ctx, "t1", "cf2", 3.5, testNow)
	must(t, err)
	if f := fieldOf(t, s, "t1", "cf2"); f.Display != "3.5" {
		t.Fatalf("number = %+v", f)
	}
	must(t, s.DiscardOp(ctx, onlyOp(t, s).ID))
	if f := fieldOf(t, s, "t1", "cf2"); f.Value != 2.0 || f.Display != "2.0" {
		t.Fatalf("after discard = %+v", f)
	}
}

func TestParseCustomFieldValue(t *testing.T) {
	s := setupFields(t)
	ctx := context.Background()
	for _, c := range []struct {
		field string
		in    any
		want  any
		bad   bool
	}{
		{"cf1", "高", "o1", false},
		{"cf1", "o2", "o2", false},
		{"cf1", "低", nil, true}, // disabled
		{"cf1", "", nil, false}, // clears
		{"cf2", "1,234.5", 1234.5, false},
		{"cf2", "abc", nil, true},
		{"cf3", "1", nil, true}, // formula
	} {
		got, err := s.ParseCustomFieldValue(ctx, c.field, c.in, testNow)
		if (err != nil) != c.bad || (!c.bad && !sameValue(got, c.want)) {
			t.Errorf("%s %v = %v, %v; want %v (error %v)", c.field, c.in, got, err, c.want, c.bad)
		}
	}
	if _, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", "nope", testNow); err == nil {
		t.Error("an unknown option must be refused")
	}
}

func TestCustomFieldOnLocalTaskFoldsIntoCreate(t *testing.T) {
	s := setupFields(t)
	ctx := context.Background()
	gid, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "新しい"}, testNow)
	must(t, err)
	f, err := s.ResolveCustomField(ctx, gid, "優先")
	must(t, err)
	_, err = s.SetCustomFieldLocal(ctx, gid, f.GID, "o1", testNow)
	must(t, err)
	op := onlyOp(t, s)
	cfs, _ := op.Payload["custom_fields"].(map[string]any)
	if op.Kind != OpCreateTask || cfs["cf1"] != "o1" {
		t.Fatalf("create = %+v", op)
	}
}

// In a conflict, choosing the value Asana has now resolves it: nothing is left to send.
func TestSetCustomFieldToServerValueResolvesConflict(t *testing.T) {
	s := setupFields(t)
	ctx := context.Background()
	_, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", testNow)
	must(t, err)
	must(t, s.MarkOpConflict(ctx, onlyOp(t, s).ID, map[string]any{"value": nil, "display": ""}))
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf1", nil, testNow)
	must(t, err)
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestReplaceLocalTaskGIDMovesCustomFields(t *testing.T) {
	s := setupFields(t)
	ctx := context.Background()
	gid, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "新しい"}, testNow)
	must(t, err)
	_, err = s.SetCustomFieldLocal(ctx, gid, "cf1", "o1", testNow)
	must(t, err)
	must(t, s.ReplaceLocalTaskGID(ctx, gid, "real1"))
	if f := fieldOf(t, s, "real1", "cf1"); f.Value != "o1" {
		t.Fatalf("field after the gid change = %+v", f)
	}
}

func TestCustomFieldAPIValue(t *testing.T) {
	if got := CustomFieldAPIValue(CFDate, "2026-12-01"); !sameValue(got, map[string]any{"date": "2026-12-01"}) {
		t.Errorf("date = %v", got)
	}
	if got := CustomFieldAPIValue(CFDate, nil); got != nil {
		t.Errorf("clearing a date = %v", got)
	}
	if got := CustomFieldAPIValue(CFMultiEnum, []any{"b", "a"}); !sameValue(got, []string{"a", "b"}) {
		t.Errorf("multi_enum = %v", got)
	}
}
