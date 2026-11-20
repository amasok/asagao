package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// task field --add-option adds an option the field does not have and sets it; without the
// flag the refusal says how.
func TestTaskFieldAddOption(t *testing.T) {
	path := fieldsCLIEnv(t)
	out, err := runCLI(t, "task", "field", "t1", "優先度", "最高")
	if err == nil || !strings.Contains(err.Error()+out, "--add-option") {
		t.Fatalf("refusal should name --add-option: %v\n%s", err, out)
	}
	// "高" is a part of "最高" but not the same: it is added, not taken
	if out, err := runCLI(t, "task", "field", "t1", "優先度", "最高", "--add-option"); err != nil || !strings.Contains(out, "最高") {
		t.Fatalf("--add-option: %v\n%s", err, out)
	}
	var kinds []string
	for _, op := range cliOps(t, path) {
		kinds = append(kinds, op.Kind)
	}
	if strings.Join(kinds, ",") != store.OpCreateEnumOption+","+store.OpSetCustomField {
		t.Fatalf("ops = %v", kinds)
	}
	if out, err := runCLI(t, "task", "show", "t1"); err != nil || !strings.Contains(out, "最高") {
		t.Fatalf("task show: %v\n%s", err, out)
	}
	// an existing option (ignoring case) with the flag: just set
	if out, err := runCLI(t, "task", "field", "t2", "優先度", "中", "--add-option"); err != nil {
		t.Fatalf("existing: %v\n%s", err, out)
	}
	if n := len(cliOps(t, path)); n != 3 {
		t.Fatalf("%d ops", n)
	}
}

// Not offered after Asana refused it.
func TestTaskFieldAddOptionRefused(t *testing.T) {
	path := fieldsCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetFieldOptionsLocked(context.Background(), "cf1", time.Now()); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if out, err := runCLI(t, "task", "field", "t1", "優先度", "最高", "--add-option"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

// project fields lists, adds a workspace field, and removes one only with --yes.
func TestProjectFieldsCommand(t *testing.T) {
	path := fieldsCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceWorkspaceCustomFields(context.Background(), "w1", []asana.CustomField{
		{GID: "cf1", Name: "優先度", ResourceSubtype: "enum", EnumOptions: []asana.EnumOption{}},
		{GID: "cf9", Name: "見積もり", ResourceSubtype: "number"}}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out, err := runCLI(t, "project", "fields", "新居", "--all")
	if err != nil || !strings.Contains(out, "優先度") || !strings.Contains(out, "[追加できるフィールド]\n見積もり") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "project", "fields", "新居", "--add", "見積もり"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "project", "fields", "新居"); err != nil || !strings.Contains(out, "見積もり") || !strings.Contains(out, "[未送信]") {
		t.Fatalf("after add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "project", "fields", "新居", "--remove", "優先度"); err == nil {
		t.Fatalf("removed without --yes\n%s", out)
	}
	if out, err := runCLI(t, "project", "fields", "新居", "--remove", "優先度", "--yes"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	var kinds []string
	for _, op := range cliOps(t, path) {
		kinds = append(kinds, op.Kind)
	}
	if strings.Join(kinds, ",") != store.OpAddFieldSetting+","+store.OpRemoveFieldSetting {
		t.Fatalf("ops = %v", kinds)
	}
}
