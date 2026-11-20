package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// setupFieldOps is setupFields with project p1 in workspace w1, a multi_enum field cf4
// (ラベル: m1 A, m2 B) on it, and a text field cf5 of the workspace that p1 does not have.
func setupFieldOps(t *testing.T) *Store {
	t.Helper()
	s := setupFields(t)
	ctx := context.Background()
	must(t, s.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"))
	settings := []asana.CustomFieldSetting{
		{IsImportant: true, CustomField: asana.CustomField{GID: "cf1", Name: "優先度", ResourceSubtype: CFEnum, EnumOptions: []asana.EnumOption{
			{GID: "o1", Name: "高", Color: "red"}, {GID: "o2", Name: "中"}}}},
		{CustomField: asana.CustomField{GID: "cf4", Name: "ラベル", ResourceSubtype: CFMultiEnum, EnumOptions: []asana.EnumOption{
			{GID: "m1", Name: "A"}, {GID: "m2", Name: "B"}}}},
	}
	must(t, s.ReplaceProjectCustomFields(ctx, "p1", settings))
	must(t, s.ReplaceWorkspaceCustomFields(ctx, "w1", []asana.CustomField{settings[0].CustomField, settings[1].CustomField,
		{GID: "cf5", Name: "メモ", ResourceSubtype: CFText}}))
	return s
}

func fieldOpsOfKind(t *testing.T, s *Store, kind Kind) []Op {
	t.Helper()
	ops, err := s.ListOps(context.Background())
	must(t, err)
	return slices.DeleteFunc(ops, func(o Op) bool { return o.Kind != kind })
}

func TestAddEnumOptionLocal(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	gid, created, err := s.AddEnumOptionLocal(ctx, "cf1", " 低 ", testNow)
	if err != nil || !created || !IsLocalGID(gid) {
		t.Fatalf("add = %q %v %v", gid, created, err)
	}
	f := fieldOf(t, s, "t1", "cf1")
	if !slices.ContainsFunc(f.Options, func(o EnumOptionView) bool { return o.GID == gid && o.Name == "低" && o.Enabled }) {
		t.Fatalf("options = %+v", f.Options)
	}
	if ops := fieldOpsOfKind(t, s, OpCreateEnumOption); len(ops) != 1 || ops[0].TaskGID != gid || ops[0].Payload["name"] != "低" {
		t.Fatalf("ops = %+v", ops)
	}
	// the same name again (or another case of it), or an option it has: nothing new
	for _, name := range []string{"低", "高"} {
		again, created, err := s.AddEnumOptionLocal(ctx, "cf1", name, testNow)
		if err != nil || created || (name == "低" && again != gid) || (name == "高" && again != "o1") {
			t.Fatalf("add %s = %q %v %v", name, again, created, err)
		}
	}
	must(t, s.UpsertTasks(ctx, nil))
	if again, created, _ := s.AddEnumOptionLocal(ctx, "cf4", "a", testNow); created || again != "m1" {
		t.Fatalf("ignoring case = %q %v", again, created)
	}
	if n := len(fieldOpsOfKind(t, s, OpCreateEnumOption)); n != 1 {
		t.Fatalf("%d creates", n)
	}
	for _, c := range []struct{ field, name string }{{"cf1", "  "}, {"cf2", "x"}, {"cf3", "x"}, {"nope", "x"}} {
		if _, _, err := s.AddEnumOptionLocal(ctx, c.field, c.name, testNow); err == nil {
			t.Errorf("%s %q: no error", c.field, c.name)
		}
	}
	// control characters do not reach the name
	g2, _, err := s.AddEnumOptionLocal(ctx, "cf1", "x\x1b[31m", testNow)
	must(t, err)
	for _, o := range fieldOf(t, s, "t1", "cf1").Options {
		if o.GID == g2 && o.Name != "x[31m" {
			t.Fatalf("name = %q", o.Name)
		}
	}
}

// An option is not added before the field's options are known.
func TestAddEnumOptionUnknownOptions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", CustomFields: []asana.CustomField{priorityField("o1")}}}))
	if _, _, err := s.AddEnumOptionLocal(ctx, "cf1", "低", testNow); !errors.Is(err, ErrOptionsUnknown) {
		t.Fatalf("err = %v", err)
	}
}

// Asana created the option: its gid replaces the local one in the options, the values and
// the queued changes (a multi_enum value stays sorted).
func TestCompleteCreateEnumOption(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	local, _, _ := s.AddEnumOptionLocal(ctx, "cf4", "C", testNow)
	_, err := s.SetCustomFieldLocal(ctx, "t1", "cf4", []string{local, "m2"}, testNow)
	must(t, err)
	create := fieldOpsOfKind(t, s, OpCreateEnumOption)[0]
	must(t, s.CompleteCreateEnumOption(ctx, create.ID, local, asana.EnumOption{GID: "a0", Name: "C", Color: "blue"}))
	f := fieldOf(t, s, "t1", "cf4")
	if fmt.Sprint(f.Value) != "[a0 m2]" || f.Display != "C, B" && f.Display != "B, C" {
		t.Fatalf("value = %+v", f)
	}
	if !slices.ContainsFunc(f.Options, func(o EnumOptionView) bool { return o.GID == "a0" && o.Color == "blue" }) {
		t.Fatalf("options = %+v", f.Options)
	}
	set := fieldOpsOfKind(t, s, OpSetCustomField)
	if len(set) != 1 || fmt.Sprint(set[0].Payload["value"]) != "[a0 m2]" {
		t.Fatalf("queued = %+v", set)
	}
	if real, _ := s.RealEnumOptionGID(ctx, local); real != "a0" {
		t.Fatalf("result = %q", real)
	}
	if len(fieldOpsOfKind(t, s, OpCreateEnumOption)) != 0 {
		t.Fatal("create left")
	}
	// the mapping is kept while something still names the local gid
	for _, q := range resultTables[resultPrefixEnumOption] {
		var n int
		must(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM ("+q+")").Scan(&n))
		if n != 0 {
			t.Fatalf("%s: %d local gids left", q, n)
		}
	}
}

// The local gids of options are found in the JSON columns (to keep their mappings).
func TestLocalOptionGIDsFoundForPrune(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	local, _, _ := s.AddEnumOptionLocal(ctx, "cf1", "低", testNow)
	_, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", local, testNow)
	must(t, err)
	local2, _, _ := s.AddEnumOptionLocal(ctx, "cf4", "C", testNow)
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf4", []string{"m1", local2}, testNow)
	must(t, err)
	found := map[string]int{}
	for _, q := range resultTables[resultPrefixEnumOption] {
		rows, err := s.db.QueryContext(ctx, q)
		must(t, err)
		for rows.Next() {
			var g string
			must(t, rows.Scan(&g))
			found[g]++
		}
		rows.Close()
	}
	if found[local] != 2 || found[local2] != 2 || len(found) != 2 {
		t.Fatalf("found %v", found)
	}
}

// Discarding the creation of an option drops it from the field and from the values: a
// change that only set it is dropped (the value goes back), a multi_enum change keeps its
// other options, a task created offline is created without it.
func TestDiscardCreateEnumOption(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	local, _, _ := s.AddEnumOptionLocal(ctx, "cf1", "低", testNow)
	_, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", local, testNow)
	must(t, err)
	localM, _, _ := s.AddEnumOptionLocal(ctx, "cf4", "C", testNow)
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf4", []string{"m2", localM}, testNow)
	must(t, err)
	task, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", Name: "新"}, testNow)
	must(t, err)
	_, err = s.SetCustomFieldLocal(ctx, task, "cf1", local, testNow)
	must(t, err)

	for _, op := range fieldOpsOfKind(t, s, OpCreateEnumOption) {
		must(t, s.DiscardOp(ctx, op.ID))
	}
	if f := fieldOf(t, s, "t1", "cf1"); f.Value != "o1" || f.Display != "高" ||
		slices.ContainsFunc(f.Options, func(o EnumOptionView) bool { return o.GID == local }) {
		t.Fatalf("enum = %+v", f)
	}
	if f := fieldOf(t, s, "t1", "cf4"); fmt.Sprint(f.Value) != "[m2]" || f.Display != "B" {
		t.Fatalf("multi = %+v", f)
	}
	set := fieldOpsOfKind(t, s, OpSetCustomField)
	if len(set) != 1 || fmt.Sprint(set[0].Payload["value"]) != "[m2]" || set[0].Payload["display"] != "B" {
		t.Fatalf("queued = %+v", set)
	}
	creates := fieldOpsOfKind(t, s, OpCreateTask)
	if len(creates) != 1 || creates[0].Payload["custom_fields"] != nil {
		t.Fatalf("create = %+v", creates)
	}
	if f := fieldOf(t, s, task, "cf1"); f.Value != nil {
		t.Fatalf("local task = %+v", f)
	}
}

func TestSetProjectFieldLocal(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	attached := func() string {
		fields, err := s.ProjectFields(ctx, "p1")
		must(t, err)
		var out []string
		for _, f := range fields {
			out = append(out, fmt.Sprintf("%s:%v:%v", f.GID, f.Attached, f.Pending))
		}
		return fmt.Sprint(out)
	}
	if got := attached(); got != "[cf1:true:false cf4:true:false cf5:false:false]" {
		t.Fatalf("before = %s", got)
	}
	must(t, s.SetProjectFieldLocal(ctx, "p1", "cf5", true, testNow))
	if got := attached(); got != "[cf1:true:false cf4:true:false cf5:true:true]" {
		t.Fatalf("added = %s", got)
	}
	if f := fieldOf(t, s, "t1", "cf5"); !f.Editable || f.Value != nil {
		t.Fatalf("task's field = %+v", f)
	}
	if err := s.SetProjectFieldLocal(ctx, "p1", "cf5", true, testNow); err == nil {
		t.Fatal("added twice")
	}
	// removing what is not sent yet drops the change
	must(t, s.SetProjectFieldLocal(ctx, "p1", "cf5", false, testNow))
	if got := attached(); got != "[cf1:true:false cf4:true:false cf5:false:false]" {
		t.Fatalf("undone = %s", got)
	}
	if n := len(fieldOpsOfKind(t, s, OpAddFieldSetting)) + len(fieldOpsOfKind(t, s, OpRemoveFieldSetting)); n != 0 {
		t.Fatalf("%d ops", n)
	}
	// removing, and discarding the removal, puts it back where it was
	must(t, s.SetProjectFieldLocal(ctx, "p1", "cf1", false, testNow))
	if got := attached(); got != "[cf4:true:false cf5:false:false cf1:false:true]" {
		t.Fatalf("removed = %s", got)
	}
	must(t, s.DiscardOp(ctx, fieldOpsOfKind(t, s, OpRemoveFieldSetting)[0].ID))
	if got := attached(); got != "[cf1:true:false cf4:true:false cf5:false:false]" {
		t.Fatalf("restored = %s", got)
	}
	if f := fieldOf(t, s, "t1", "cf1"); !f.Important {
		t.Fatalf("importance lost: %+v", f)
	}
	for _, c := range []struct{ project, field string }{{"p9", "cf1"}, {"p1", "nope"}} {
		if err := s.SetProjectFieldLocal(ctx, c.project, c.field, true, testNow); !errors.Is(err, ErrNotFound) {
			t.Errorf("%v: err = %v", c, err)
		}
	}
}

func TestResolveProjectField(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	for arg, want := range map[string]string{"メモ": "cf5", "cf4": "cf4", "ラベ": "cf4", "優先度": "cf1"} {
		f, err := s.ResolveProjectField(ctx, "p1", arg)
		if err != nil || f.GID != want {
			t.Errorf("%s = %+v, %v", arg, f, err)
		}
	}
	if _, err := s.ResolveProjectField(ctx, "p1", "なし"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// A local option gid still held by the caller (an open chooser) after Asana created the
// option is set as its Asana gid, never queued as the local one.
func TestSetCustomFieldWithCreatedLocalOption(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	local, _, _ := s.AddEnumOptionLocal(ctx, "cf1", "低", testNow)
	localM, _, _ := s.AddEnumOptionLocal(ctx, "cf4", "C", testNow)
	for _, op := range fieldOpsOfKind(t, s, OpCreateEnumOption) {
		real := "a-" + op.Payload["name"].(string)
		must(t, s.CompleteCreateEnumOption(ctx, op.ID, op.TaskGID, asana.EnumOption{GID: real, Name: op.Payload["name"].(string)}))
	}
	display, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", local, testNow)
	if err != nil || display != "低" {
		t.Fatalf("set = %q, %v", display, err)
	}
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf4", []string{"m2", localM}, testNow)
	must(t, err)
	if f := fieldOf(t, s, "t1", "cf1"); f.Value != "a-低" {
		t.Fatalf("enum = %+v", f)
	}
	if f := fieldOf(t, s, "t1", "cf4"); fmt.Sprint(f.Value) != "[a-C m2]" {
		t.Fatalf("multi = %+v", f)
	}
	for _, op := range fieldOpsOfKind(t, s, OpSetCustomField) {
		if v := fmt.Sprint(op.Payload["value"]); v != "a-低" && v != "[a-C m2]" {
			t.Fatalf("queued %s", v)
		}
	}
}

// The project's fields are listed in the project's order (not by gid).
func TestProjectFieldsInProjectOrder(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	must(t, s.ReplaceProjectCustomFields(ctx, "p1", []asana.CustomFieldSetting{
		{CustomField: asana.CustomField{GID: "cf4", Name: "ラベル", ResourceSubtype: CFMultiEnum, EnumOptions: []asana.EnumOption{}}},
		{CustomField: asana.CustomField{GID: "cf1", Name: "優先度", ResourceSubtype: CFEnum, EnumOptions: []asana.EnumOption{}}},
	}))
	fields, err := s.ProjectFields(ctx, "p1")
	must(t, err)
	var got []string
	for _, f := range fields {
		got = append(got, f.GID)
	}
	if fmt.Sprint(got) != "[cf4 cf1 cf5]" {
		t.Fatalf("order = %v", got)
	}
}
